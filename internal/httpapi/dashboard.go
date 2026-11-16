package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/httpapi/gen"
	"github.com/Jonasz1996/clusterforge/internal/status"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

// Dashboard-API (fase 20): een kleine, alleen-lezen API voor een eigen
// dashboard zoals homepage. Ze staat naast /api/v1 onder
// /api/dashboard/v1, met een API-token in plaats van een sessie, en geeft per
// cluster vaste, platte velden: de customapi-widget van homepage toont een
// veld met één naam (bv. "nodes" of "vip_owner") en kan niets optellen.

const (
	dashboardBase   = "/api/dashboard/v1"
	apiTokenPrefix  = "cfa_"
	dashboardMaxAge = 15 * time.Second
)

func hashAPIToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

func newAPIToken() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return apiTokenPrefix + strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b)), nil
}

// --- beheer in de GUI (onder /api/v1, met sessie) ---

func (s *Server) ListApiTokens(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	rows, err := s.q.ListAPITokens(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	names, err := s.clusterNames(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	items := make([]gen.ApiToken, 0, len(rows))
	for _, t := range rows {
		items = append(items, toAPIToken(store.ApiToken{
			ID: t.ID, Name: t.Name, Prefix: t.Prefix, ClusterIds: t.ClusterIds, ExpiresAt: t.ExpiresAt,
			CreatedAt: t.CreatedAt, CreatedBy: t.CreatedBy, LastUsedAt: t.LastUsedAt, LastUsedIp: t.LastUsedIp,
		}, t.CreatedByName, names))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "base_path": dashboardBase})
}

func (s *Server) CreateApiToken(w http.ResponseWriter, r *http.Request) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var req gen.ApiTokenInput
	if !decode(w, r, &req) {
		return
	}
	name := strings.TrimSpace(req.Name)
	ttl := deref(req.TtlDays)
	ids := deref(req.ClusterIds)
	switch {
	case name == "" || len(name) > 100:
		writeError(w, http.StatusBadRequest, "validation", "geef het token een naam van hoogstens 100 tekens")
		return
	case ttl < 0 || ttl > 3650:
		writeError(w, http.StatusBadRequest, "validation", "geldigheid moet tussen 0 (nooit verlopen) en 3650 dagen liggen")
		return
	}
	names, err := s.clusterNames(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	clusterIDs := make([]uuid.UUID, 0, len(ids))
	for _, id := range ids {
		if _, ok := names[id]; !ok {
			writeError(w, http.StatusBadRequest, "validation", "onbekend cluster")
			return
		}
		if !slices.Contains(clusterIDs, id) {
			clusterIDs = append(clusterIDs, id)
		}
	}
	var expires *time.Time
	if ttl > 0 {
		e := time.Now().Add(time.Duration(ttl) * 24 * time.Hour)
		expires = &e
	}
	token, err := newAPIToken()
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	var t store.ApiToken
	err = pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		var err error
		t, err = q.CreateAPIToken(r.Context(), store.CreateAPITokenParams{
			Name: name, TokenHash: hashAPIToken(token), Prefix: token[:len(apiTokenPrefix)+6],
			ClusterIds: clusterIDs, ExpiresAt: expires, CreatedBy: &p.User.ID,
		})
		if err != nil {
			return err
		}
		return s.ev.Write(r.Context(), q, events.Event{
			Actor: events.User(p.User.ID), SubjectType: "api_token", SubjectID: t.ID.String(),
			Action:  "api_token.created",
			Payload: map[string]any{"name": t.Name, "prefix": t.Prefix, "cluster_ids": t.ClusterIds, "expires_at": t.ExpiresAt},
		})
	})
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, gen.NewApiToken{Token: token, Item: toAPIToken(t, p.User.Username, names)})
}

func (s *Server) DeleteApiToken(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var n int64
	err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		var err error
		if n, err = q.DeleteAPIToken(r.Context(), id); err != nil || n == 0 {
			return err
		}
		return s.ev.Write(r.Context(), q, events.Event{
			Actor: events.User(p.User.ID), SubjectType: "api_token", SubjectID: id.String(), Action: "api_token.deleted",
		})
	})
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if n == 0 {
		writeError(w, http.StatusNotFound, "not_found", "API-token niet gevonden")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// PreviewDashboardApi geeft wat de dashboard-API nu zou teruggeven, met de
// sessie van de gebruiker; een viewer mag alles al zien.
func (s *Server) PreviewDashboardApi(w http.ResponseWriter, r *http.Request) {
	o, err := s.dashboardData(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, withURLs(o.filter(nil), baseURL(r, s.cfg.TrustProxyHeaders)))
}

func (s *Server) clusterNames(ctx context.Context) (map[uuid.UUID]string, error) {
	rows, err := s.q.ListClusters(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[uuid.UUID]string, len(rows))
	for _, c := range rows {
		out[c.Cluster.ID] = c.Cluster.Name
	}
	return out, nil
}

func toAPIToken(t store.ApiToken, by string, names map[uuid.UUID]string) gen.ApiToken {
	out := gen.ApiToken{
		Id: t.ID, Name: t.Name, Prefix: t.Prefix, ClusterIds: nonNil(t.ClusterIds), ClusterNames: []string{},
		ExpiresAt: nullableOf(t.ExpiresAt), CreatedAt: t.CreatedAt, CreatedBy: by,
		LastUsedAt: nullableOf(t.LastUsedAt), LastUsedIp: t.LastUsedIp,
	}
	for _, id := range t.ClusterIds {
		if n, ok := names[id]; ok {
			out.ClusterNames = append(out.ClusterNames, n)
		} else {
			out.ClusterNames = append(out.ClusterNames, "(verwijderd cluster)")
		}
	}
	return out
}

// --- de dashboard-API zelf (met API-token) ---

type dashboardTokenKey struct{}

// dashboardRoutes hangt de dashboard-API onder /api/dashboard/v1. Alleen GET;
// er is niets om te wijzigen.
func (s *Server) dashboardRoutes(r chi.Router) {
	r.Use(s.requireAPIToken)
	r.Get("/", s.dashboardOverview)
	r.Get("/clusters", s.dashboardClusters)
	r.Get("/clusters/{cluster}", s.dashboardCluster)
	r.Get("/soc", s.dashboardSocHandler)
	r.NotFound(func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "not_found", "onbekend endpoint")
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "de dashboard-API kan alleen lezen (GET)")
	})
}

// requireAPIToken laat alleen een geldig API-token door, als
// "Authorization: Bearer cfa_…" of "X-API-Key: cfa_…". Een fout token remt
// het IP-adres net als een foute login.
func (s *Server) requireAPIToken(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "de dashboard-API kan alleen lezen (GET)")
			return
		}
		token, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if token == "" {
			token = r.Header.Get("X-API-Key")
		}
		token = strings.TrimSpace(token)
		ip := clientIP(r)
		if token == "" {
			writeError(w, http.StatusUnauthorized, "unauthenticated", "API-token ontbreekt (Authorization: Bearer cfa_…)")
			return
		}
		deny := func() {
			if !s.loginLimiter.allow(ip) {
				writeError(w, http.StatusTooManyRequests, "rate_limited", "te veel foute tokens; probeer het straks opnieuw")
				return
			}
			writeError(w, http.StatusUnauthorized, "unauthenticated", "onbekend, ingetrokken of verlopen API-token")
		}
		if !strings.HasPrefix(token, apiTokenPrefix) {
			deny()
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), s.sessionTimeout)
		t, err := s.q.GetAPITokenByHash(ctx, hashAPIToken(token))
		cancel()
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && t.ExpiresAt != nil && time.Now().After(*t.ExpiresAt)) {
			deny()
			return
		}
		if err != nil {
			s.internalError(w, r, fmt.Errorf("API-token controleren: %w", err))
			return
		}
		if err := s.q.TouchAPIToken(r.Context(), store.TouchAPITokenParams{ID: t.ID, LastUsedIp: ip}); err != nil {
			s.log.Warn("API-token: laatste gebruik bijhouden", "err", err)
		}
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), dashboardTokenKey{}, t)))
	})
}

func (s *Server) dashboardScoped(w http.ResponseWriter, r *http.Request) (gen.DashboardOverview, bool) {
	t, _ := r.Context().Value(dashboardTokenKey{}).(store.ApiToken)
	o, err := s.dashboardData(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return gen.DashboardOverview{}, false
	}
	return withURLs(o.filter(t.ClusterIds), baseURL(r, s.cfg.TrustProxyHeaders)), true
}

func (s *Server) dashboardOverview(w http.ResponseWriter, r *http.Request) {
	if o, ok := s.dashboardScoped(w, r); ok {
		writeJSON(w, http.StatusOK, o)
	}
}

func (s *Server) dashboardClusters(w http.ResponseWriter, r *http.Request) {
	if o, ok := s.dashboardScoped(w, r); ok {
		writeJSON(w, http.StatusOK, o.Clusters)
	}
}

// dashboardCluster zoekt het cluster op slug, id of naam (hoofdletters
// tellen niet).
func (s *Server) dashboardCluster(w http.ResponseWriter, r *http.Request) {
	o, ok := s.dashboardScoped(w, r)
	if !ok {
		return
	}
	key := chi.URLParam(r, "cluster")
	for _, c := range o.Clusters {
		if strings.EqualFold(c.Slug, key) || c.Id.String() == strings.ToLower(key) || strings.EqualFold(c.Name, key) {
			// ?strict=1: 503 zolang het cluster niet in orde is, zodat de
			// check van een dashboardtegel vanzelf rood wordt.
			code := http.StatusOK
			if r.URL.Query().Get("strict") == "1" && !c.Ok {
				code = http.StatusServiceUnavailable
			}
			writeJSON(w, code, c)
			return
		}
	}
	writeError(w, http.StatusNotFound, "not_found", "cluster niet gevonden (of niet zichtbaar met dit token)")
}

// baseURL is het adres van ClusterForge zoals de client het gebruikte, voor
// de links naar de clusterpagina.
func baseURL(r *http.Request, trustProxy bool) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if trustProxy {
		if p := r.Header.Get("X-Forwarded-Proto"); p == "https" || p == "http" {
			scheme = p
		}
	}
	return scheme + "://" + r.Host
}

func withURLs(o gen.DashboardOverview, base string) gen.DashboardOverview {
	for i := range o.Clusters {
		o.Clusters[i].Url = base + o.Clusters[i].Url
	}
	return o
}

// --- de cijfers ---

// dashboardCache houdt het laatste overzicht 15 seconden bij, zodat
// een dashboard dat elke paar seconden per cluster vraagt de database niet
// belast.
type dashboardCache struct {
	mu   sync.Mutex
	at   time.Time
	data dashboardSet
}

type dashboardSet struct {
	at       time.Time
	clusters []gen.DashboardCluster
}

// filter geeft het overzicht voor een token: alleen ids, of alles als ids leeg is.
func (d dashboardSet) filter(ids []uuid.UUID) gen.DashboardOverview {
	o := gen.DashboardOverview{GeneratedAt: d.at, Clusters: []gen.DashboardCluster{}}
	for _, c := range d.clusters {
		if len(ids) > 0 && !slices.Contains(ids, c.Id) {
			continue
		}
		// Een kopie, zodat withURLs de cache niet raakt.
		c.NodesDown = slices.Clone(c.NodesDown)
		c.Problems = slices.Clone(c.Problems)
		c.Vips = slices.Clone(c.Vips)
		o.Clusters = append(o.Clusters, c)
		o.ClustersTotal++
		switch c.Status {
		case string(status.Healthy):
			o.ClustersHealthy++
		case string(status.Down), string(status.SplitBrain):
			o.ClustersDown++
		case string(status.Degraded):
			o.ClustersDegraded++
		}
		o.NodesOnline += c.NodesOnline
		o.NodesTotal += c.NodesTotal
		o.IncidentsOpen += c.IncidentsOpen
		o.Updates += c.Updates
		o.UpdatesSecurity += c.UpdatesSecurity
		o.CrowdsecBans += c.CrowdsecBans
		o.CrowdsecAlerts24h += c.CrowdsecAlerts24h
	}
	o.Nodes = fmt.Sprintf("%d/%d", o.NodesOnline, o.NodesTotal)
	return o
}

func (s *Server) dashboardData(ctx context.Context) (dashboardSet, error) {
	c := &s.dashCache
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.at.IsZero() && time.Since(c.at) < dashboardMaxAge {
		return c.data, nil
	}
	d, err := s.buildDashboard(ctx)
	if err != nil {
		return dashboardSet{}, err
	}
	c.at, c.data = time.Now(), d
	return d, nil
}

var statusLabels = map[string]string{
	string(status.Healthy):    "Gezond",
	string(status.Degraded):   "Verminderd",
	string(status.Down):       "Down",
	string(status.SplitBrain): "Split-brain",
	string(status.Unknown):    "Onbekend",
}

var engineLabels = map[string]string{"mariadb": "MariaDB", "postgresql": "PostgreSQL"}

func (s *Server) buildDashboard(ctx context.Context) (dashboardSet, error) {
	now := time.Now()
	clusters, err := s.q.ListClusters(ctx)
	if err != nil {
		return dashboardSet{}, err
	}
	grades, err := s.risk.Grades(ctx)
	if err != nil {
		return dashboardSet{}, err
	}
	owners, err := s.q.ListVIPOwners(ctx)
	if err != nil {
		return dashboardSet{}, err
	}
	nodes, err := s.q.DashboardNodes(ctx)
	if err != nil {
		return dashboardSet{}, err
	}
	incidents, err := s.q.DashboardOpenIncidents(ctx)
	if err != nil {
		return dashboardSet{}, err
	}
	dbs, err := s.q.DashboardDBServers(ctx)
	if err != nil {
		return dashboardSet{}, err
	}
	cs, err := s.q.DashboardCrowdSec(ctx)
	if err != nil {
		return dashboardSet{}, err
	}
	fw, err := s.q.DashboardFirewall(ctx)
	if err != nil {
		return dashboardSet{}, err
	}
	upd, err := s.updates.Overview(ctx)
	if err != nil {
		return dashboardSet{}, err
	}

	byID := map[uuid.UUID]*gen.DashboardCluster{}
	out := make([]gen.DashboardCluster, len(clusters))
	for i, row := range clusters {
		cl := row.Cluster
		g := grades[cl.ID]
		out[i] = gen.DashboardCluster{
			Id: cl.ID, Slug: cl.Slug, Name: cl.Name, Environment: string(cl.Environment), Type: cl.Type,
			Url:    "/clusters/detail?id=" + cl.ID.String(),
			Status: cl.Status, StatusLabel: statusLabels[cl.Status], StatusReason: cl.StatusReason,
			Score: nullableOf(g.Score), Grade: g.Grade,
			NodesDown: []string{}, Vips: []gen.DashboardVip{}, Problems: []string{},
			Db: "none", DbLabel: "—", Crowdsec: "—", Firewall: "—",
		}
		if out[i].StatusLabel == "" {
			out[i].StatusLabel = cl.Status
		}
		byID[cl.ID] = &out[i]
	}
	for _, v := range owners {
		if c := byID[v.ClusterID]; c != nil {
			c.Vips = append(c.Vips, gen.DashboardVip{Address: v.Address.String(), Owner: deref(v.OwnerHostname)})
		}
	}
	for _, n := range nodes {
		c := byID[deref(n.ClusterID)]
		if c == nil {
			continue
		}
		c.NodesTotal++
		if n.HeartbeatAt != nil && now.Sub(*n.HeartbeatAt) <= status.HeartbeatDown {
			c.NodesOnline++
		} else if n.Lifecycle != "maintenance" {
			c.NodesDown = append(c.NodesDown, n.Hostname)
		}
	}
	for _, in := range incidents {
		if c := byID[in.ClusterID]; c != nil {
			c.IncidentsOpen = int(in.Open)
			c.IncidentsWorst = [...]string{"", "low", "high", "critical"}[min(max(in.Worst, 0), 3)]
		}
	}
	// Databases: per engine hoeveel servers draaien.
	type dbCount struct{ running, total int }
	dbBy := map[uuid.UUID]map[string]*dbCount{}
	for _, d := range dbs {
		if byID[d.ClusterID] == nil {
			continue
		}
		m := dbBy[d.ClusterID]
		if m == nil {
			m = map[string]*dbCount{}
			dbBy[d.ClusterID] = m
		}
		e := m[d.Engine]
		if e == nil {
			e = &dbCount{}
			m[d.Engine] = e
		}
		e.total++
		if d.Running && d.ErrorKind != "offline" {
			e.running++
		}
	}
	for id, m := range dbBy {
		c := byID[id]
		var parts []string
		for _, eng := range []string{"mariadb", "postgresql"} {
			if e := m[eng]; e != nil {
				c.DbRunning += e.running
				c.DbTotal += e.total
				parts = append(parts, fmt.Sprintf("%s %d/%d", engineLabels[eng], e.running, e.total))
			}
		}
		c.DbLabel = strings.Join(parts, " · ")
		switch c.DbRunning {
		case c.DbTotal:
			c.Db = "ok"
		case 0:
			c.Db = "down"
		default:
			c.Db = "degraded"
		}
	}
	for _, r := range cs {
		c := byID[r.ClusterID]
		if c == nil || r.Managed == 0 {
			continue
		}
		c.CrowdsecManaged, c.CrowdsecRunning = int(r.Managed), int(r.Running)
		c.CrowdsecBans, c.CrowdsecAlerts24h = int(r.Bans), int(r.Alerts24h)
		c.Crowdsec = fmt.Sprintf("%d/%d actief", r.Running, r.Managed)
		if r.BouncersDown > 0 {
			c.Problems = append(c.Problems, fmt.Sprintf("CrowdSec: %d bouncer(s) draaien niet", r.BouncersDown))
		}
	}
	for _, r := range fw {
		c := byID[r.ClusterID]
		if c == nil {
			continue
		}
		c.FirewallManaged, c.FirewallActive, c.FirewallBlocked24h = int(r.Managed), int(r.Active), r.Blocked24h
		switch {
		case r.Managed > 0:
			c.Firewall = fmt.Sprintf("%d/%d actief", r.Active, r.Managed)
		case r.Preview > 0:
			c.Firewall = "voorbeeld"
		}
		if r.Failed > 0 {
			c.Problems = append(c.Problems, fmt.Sprintf("Firewall: toepassen mislukt op %d node(s)", r.Failed))
		}
	}
	for _, n := range upd {
		c := byID[deref(n.ClusterID)]
		if c == nil {
			continue
		}
		if n.Upgrades != nil && n.Upgrades.Total > 0 {
			c.Updates += n.Upgrades.Total
			c.UpdatesSecurity += n.Upgrades.Security
			c.NodesWithUpdates++
		}
		if n.RebootRequired {
			c.RebootRequired++
		}
	}
	for i := range out {
		c := &out[i]
		c.Nodes = fmt.Sprintf("%d/%d", c.NodesOnline, c.NodesTotal)
		if len(c.Vips) > 0 {
			c.Vip, c.VipOwner = c.Vips[0].Address, c.Vips[0].Owner
		}
		c.Ok = c.Status == string(status.Healthy) && c.IncidentsOpen == 0
		var p []string
		if c.Status != string(status.Healthy) && c.StatusReason != "" {
			p = append(p, c.StatusReason)
		}
		if len(c.NodesDown) > 0 {
			p = append(p, "offline: "+strings.Join(c.NodesDown, ", "))
		}
		for _, v := range c.Vips {
			if v.Owner == "" {
				p = append(p, "VIP "+v.Address+" op geen enkele node")
			}
		}
		if c.Db == "degraded" || c.Db == "down" {
			p = append(p, "database: "+c.DbLabel)
		}
		if c.IncidentsOpen > 0 {
			p = append(p, fmt.Sprintf("%d open incident(en)", c.IncidentsOpen))
		}
		if c.UpdatesSecurity > 0 {
			p = append(p, fmt.Sprintf("%d beveiligingsupdate(s)", c.UpdatesSecurity))
		}
		if c.RebootRequired > 0 {
			p = append(p, fmt.Sprintf("%d node(s) willen een herstart", c.RebootRequired))
		}
		c.Problems = append(p, c.Problems...)
	}
	return dashboardSet{at: now, clusters: out}, nil
}
