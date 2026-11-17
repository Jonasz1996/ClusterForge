package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/homeapps"
	"github.com/Jonasz1996/clusterforge/internal/publish"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/internal/zabbixmon"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// Homepage-API v2 (fase 36): per app één tegel met alles wat ClusterForge
// erover weet. Apps worden vanzelf herkend (internal/homeapps); de homepage
// haalt /homepage/layout op en maakt, wijzigt en verwijdert zelf de tegels.
// Zelfde tokens als v1; v1 blijft ongewijzigd.

const dashboardV2Base = "/api/dashboard/v2"

// MaxTileFields: de homepage toont hoogstens 8 velden op een tegel.
const MaxTileFields = 8

func (s *Server) dashboardV2Routes(r chi.Router) {
	r.Use(s.requireAPIToken)
	r.Get("/", s.dashV2Overview)
	r.Get("/apps", s.dashV2Apps)
	r.Get("/apps/{app}", s.dashV2App)
	r.Get("/homepage/layout", s.dashV2Layout)
	r.Get("/clusters", s.dashboardClusters)
	r.Get("/clusters/{cluster}", s.dashboardCluster)
	r.Get("/nodes", s.dashboardNodes)
	r.Get("/sources/{source}", s.dashV2Source)
	r.NotFound(func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "not_found", "onbekend endpoint")
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "de homepage-API kan alleen lezen (GET)")
	})
}

// Field is één veld op een tegel.
type v2Field struct {
	Key    string `json:"key"`
	Label  string `json:"label"`
	Value  any    `json:"value"`
	Format string `json:"format,omitempty"`
	// Level: ok, warn of down; leeg = neutraal.
	Level string `json:"level,omitempty"`
}

type v2Section struct {
	Type  string           `json:"type"`
	Title string           `json:"title"`
	Items []map[string]any `json:"items"`
}

type v2Tile struct {
	Key         string         `json:"key"`
	Slug        string         `json:"slug"`
	Name        string         `json:"name"`
	Group       string         `json:"group"`
	Icon        string         `json:"icon"`
	Status      string         `json:"status"`
	StatusLabel string         `json:"status_label"`
	Reason      string         `json:"reason"`
	OK          bool           `json:"ok"`
	URL         string         `json:"url"`
	Href        string         `json:"clusterforge_url"`
	API         string         `json:"api"`
	Where       v2Where        `json:"where"`
	Fields      []v2Field      `json:"fields"`
	Values      map[string]any `json:"values"`
	Sections    []v2Section    `json:"sections"`
	App         homeapps.App   `json:"app"`
	UpdatedAt   time.Time      `json:"updated_at"`
}

type v2Where struct {
	Cluster     string `json:"cluster,omitempty"`
	ClusterSlug string `json:"cluster_slug,omitempty"`
	Node        string `json:"node,omitempty"`
	Nodes       string `json:"nodes"`
	VIP         string `json:"vip,omitempty"`
	Container   string `json:"container,omitempty"`
	Unit        string `json:"unit,omitempty"`
	Image       string `json:"image,omitempty"`
}

// fieldInfo: label en opmaak van elk veld dat een tegel kan tonen.
type fieldInfo struct {
	Label  string `json:"label"`
	Format string `json:"format,omitempty"`
}

var tileFields = map[string]fieldInfo{
	"status":              {Label: "Status"},
	"nodes":               {Label: "Nodes"},
	"node":                {Label: "Actief op"},
	"vip":                 {Label: "VIP"},
	"cluster":             {Label: "Cluster"},
	"version":             {Label: "Versie"},
	"domain":              {Label: "Adres"},
	"authentik":           {Label: "Authentik"},
	"http_status":         {Label: "HTTP", Format: "number"},
	"cluster_health":      {Label: "Clusterscore", Format: "percent"},
	"zabbix_problems":     {Label: "Zabbix", Format: "number"},
	"crowdsec_bans":       {Label: "Bans", Format: "number"},
	"crowdsec_alerts_24h": {Label: "Alerts 24u", Format: "number"},
	"updates":             {Label: "Updates", Format: "number"},
	"updates_security":    {Label: "Beveiliging", Format: "number"},
	"reboot_required":     {Label: "Herstart nodig"},
	"cpu":                 {Label: "CPU", Format: "percent"},
	"memory":              {Label: "RAM", Format: "percent"},
	// App-cijfers van de agent (protocol 52).
	"streams":          {Label: "Streams", Format: "number"},
	"transcodes":       {Label: "Transcodes", Format: "number"},
	"libraries":        {Label: "Bibliotheken", Format: "number"},
	"libraries_movies": {Label: "Filmbibliotheken", Format: "number"},
	"libraries_shows":  {Label: "Seriebibliotheken", Format: "number"},
	"queue":            {Label: "Wachtrij", Format: "number"},
	"missing":          {Label: "Ontbreekt", Format: "number"},
	"health_issues":    {Label: "Meldingen", Format: "number"},
	"indexers":         {Label: "Indexers", Format: "number"},
	// Verkeer van NPM en Authentik (fase 39), laatste 24 uur.
	"requests_24h": {Label: "Aanvragen 24u", Format: "number"},
	"visitors_24h": {Label: "Bezoekers 24u", Format: "number"},
	"errors_24h":   {Label: "Fouten 24u", Format: "number"},
	"denied_24h":   {Label: "Geweigerd 24u", Format: "number"},
	"opened_24h":   {Label: "Geopend 24u", Format: "number"},
}

var defaultTileFields = []string{"status", "nodes", "version", "zabbix_problems"}

// Standaardvelden voor apps met een app-meter.
var defaultStatsFields = map[string][]string{
	protocol.AppStatsPlex: {"status", "streams", "transcodes", "libraries", "version"},
	protocol.AppStatsArr:  {"status", "queue", "missing", "health_issues", "version"},
}

// --- app-cijfers van de agent, op de achtergrond ververst ---

// AppStatsMaxAge: zo vaak vraagt de server een app opnieuw aan de agent.
const AppStatsMaxAge = 60 * time.Second

type appStatsEntry struct {
	at      time.Time
	values  map[string]any
	err     string
	running bool
}

type appStatsCache struct {
	mu sync.Mutex
	m  map[string]*appStatsEntry
}

// appStatsFor geeft de laatste cijfers en start op de achtergrond een nieuwe
// meting als ze ouder zijn dan AppStatsMaxAge: de API wacht nooit op een app.
func (s *Server) appStatsFor(a homeapps.App, protoByNode map[uuid.UUID]int) (map[string]any, string) {
	if a.Stats != protocol.AppStatsPlex && a.Stats != protocol.AppStatsArr || s.publish == nil {
		return nil, ""
	}
	var in *homeapps.Instance
	for i := range a.Instances {
		x := &a.Instances[i]
		if x.Online && (in == nil || x.Node == a.ActiveNode) {
			in = x
		}
	}
	if in == nil {
		return nil, ""
	}
	if protoByNode[in.NodeID] < protocol.AppStatsSince {
		return nil, "agent te oud voor app-cijfers (bijwerken naar protocol 52)"
	}
	c := &s.homeStats
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[string]*appStatsEntry{}
	}
	e := c.m[a.Key]
	if e == nil {
		e = &appStatsEntry{}
		c.m[a.Key] = e
	}
	if !e.running && time.Since(e.at) > AppStatsMaxAge {
		e.running = true
		req := protocol.WebRequest{Op: protocol.WebOpAppStats, Deadline: time.Now().Add(20 * time.Second),
			App: &protocol.WebAppStats{Kind: a.Stats, Port: in.Port, Container: in.Container, HTTPS: in.Kind == "https"}}
		node, key := in.NodeID, a.Key
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			res, err := s.publish.AgentWeb(ctx, node, req)
			msg := ""
			switch {
			case err != nil:
				msg = err.Error()
			case res.Error != "":
				msg = res.Error
			}
			c.mu.Lock()
			defer c.mu.Unlock()
			x := c.m[key]
			x.running, x.at, x.err = false, time.Now(), msg
			if msg == "" && res.App != nil {
				x.values = res.App.Values
			}
		}()
	}
	return e.values, e.err
}

// --- de cijfers, 15 s bewaard ---

type homeAppsCache struct {
	mu   sync.Mutex
	at   time.Time
	data homeAppsSet
}

type homeAppsSet struct {
	at     time.Time
	res    homeapps.Result
	values map[string]map[string]any
	fields map[string][]v2Field
	sects  map[string][]v2Section
}

func (s *Server) homeAppsData(ctx context.Context) (homeAppsSet, error) {
	c := &s.homeCache
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.at.IsZero() && time.Since(c.at) < dashboardMaxAge {
		return c.data, nil
	}
	d, err := s.buildHomeApps(ctx)
	if err != nil {
		return homeAppsSet{}, err
	}
	c.at, c.data = time.Now(), d
	return d, nil
}

// invalidateHomeApps: na een bijsturing in de GUI meteen opnieuw rekenen.
func (s *Server) invalidateHomeApps() {
	s.homeCache.mu.Lock()
	s.homeCache.at = time.Time{}
	s.homeCache.mu.Unlock()
}

var errHomeAppsUnavailable = errors.New("de dienstenkaart (fase 35) is niet beschikbaar")

func (s *Server) buildHomeApps(ctx context.Context) (homeAppsSet, error) {
	if s.publish == nil || s.pool == nil {
		return homeAppsSet{}, errHomeAppsUnavailable
	}
	now := time.Now()
	dash, err := s.dashboardData(ctx)
	if err != nil {
		return homeAppsSet{}, err
	}
	ov, err := s.publish.Overview(ctx)
	if err != nil {
		return homeAppsSet{}, err
	}
	st := homeapps.NewStore(s.pool)
	known, err := st.Known(ctx)
	if err != nil {
		return homeAppsSet{}, err
	}
	dec, err := st.Decisions(ctx)
	if err != nil {
		return homeAppsSet{}, err
	}
	in := homeapps.Input{Now: now, Services: ov.Services, Hosts: ov.Hosts, Known: known, Decisions: dec}
	templates := map[uuid.UUID]string{}
	for _, c := range ov.Clusters {
		templates[c.ID] = c.Template
	}
	clByID := map[uuid.UUID]int{}
	for i, c := range dash.clusters {
		in.Clusters = append(in.Clusters, homeapps.Cluster{ID: c.Id, Slug: c.Slug, Name: c.Name, Template: templates[c.Id], VIP: c.Vip, VIPOwner: c.VipOwner})
		clByID[c.Id] = i
	}
	nodeByName := map[string]dashboardNode{}
	for _, n := range dash.nodes {
		ip := ""
		if n.PrimaryIP != nil {
			ip = *n.PrimaryIP
		}
		in.Nodes = append(in.Nodes, homeapps.Node{ID: n.ID, Hostname: n.Hostname, IP: ip, ClusterID: n.ClusterID, Online: n.Online, Maintenance: n.Maintenance})
		nodeByName[n.Hostname] = n
	}
	res := homeapps.Build(in)
	if err := st.Save(ctx, now, res); err != nil {
		s.log.Warn("homepage-API: apps bewaren", "err", err)
	}
	protoByNode := map[uuid.UUID]int{}
	if rows, err := s.pool.Query(ctx, `SELECT node_id, protocol_version FROM agents WHERE revoked_at IS NULL AND node_id IS NOT NULL`); err == nil {
		for rows.Next() {
			var id uuid.UUID
			var p int32
			if rows.Scan(&id, &p) == nil {
				protoByNode[id] = int(p)
			}
		}
		rows.Close()
	}

	// Zabbix: open problemen per node.
	zbx := map[uuid.UUID]int{}
	if s.zabbixMon != nil {
		if zo, err := s.zabbixMon.Overview(ctx, "24h"); err == nil && zo.Configured {
			zbx = zabbixProblemsByNode(zo)
		}
	}

	// Verkeer van NPM en geopende apps in Authentik (fase 39).
	usage, opens := map[string]publish.HostUsage{}, map[string]int{}
	if u, o, err := s.publish.UsageByDomain(ctx); err == nil {
		usage, opens = u, o
	}

	out := homeAppsSet{at: now, res: res, values: map[string]map[string]any{}, fields: map[string][]v2Field{}, sects: map[string][]v2Section{}}
	for _, a := range res.Apps {
		v := map[string]any{
			"status":       a.StatusLabel,
			"status_code":  a.Status,
			"name":         a.Name,
			"ok":           a.Status == homeapps.StatusOK,
			"reason":       a.Reason,
			"url":          a.URL,
			"internal_url": a.InternalURL,
		}
		online := 0
		var updates, sec int
		reboot := false
		zp := 0
		for _, in := range a.Instances {
			if in.Online {
				online++
			}
			if n, ok := nodeByName[in.Node]; ok {
				updates += n.Updates
				sec += n.UpdatesSecurity
				reboot = reboot || n.RebootRequired
			}
			zp += zbx[in.NodeID]
		}
		v["nodes"] = fmt.Sprintf("%d/%d", online, len(a.Instances))
		v["nodes_online"], v["nodes_total"] = online, len(a.Instances)
		v["updates"], v["updates_security"], v["reboot_required"] = updates, sec, reboot
		v["zabbix_problems"] = zp
		if a.ActiveNode != "" {
			v["node"] = a.ActiveNode
		}
		if a.VIP != "" {
			v["vip"] = a.VIP
		}
		if a.Version != "" {
			v["version"] = a.Version
		}
		if len(a.Domains) > 0 {
			v["domain"] = a.Domains[0]
		}
		if a.Authentik != "" {
			v["authentik"] = a.Authentik
		}
		if len(a.Domains) > 0 {
			var u publish.HostUsage
			seen := false
			for _, d := range a.Domains {
				if x, ok := usage[strings.ToLower(d)]; ok {
					seen = true
					u.Requests += x.Requests
					u.Visitors += x.Visitors
					u.S5xx += x.S5xx
					u.Denied += x.Denied
				}
			}
			if seen {
				v["requests_24h"], v["visitors_24h"], v["errors_24h"], v["denied_24h"] = u.Requests, u.Visitors, u.S5xx, u.Denied
			}
		}
		if n, ok := opens[strings.ToLower(a.Name)]; ok {
			v["opened_24h"] = n
		}
		for _, in := range a.Instances {
			if in.Node == a.ActiveNode && in.HTTPStatus > 0 {
				v["http_status"] = in.HTTPStatus
			}
		}
		if a.Cluster != nil {
			v["cluster"] = a.Cluster.Name
			if i, ok := clByID[a.Cluster.ID]; ok {
				c := dash.clusters[i]
				if sc, err := c.Score.Get(); err == nil {
					v["cluster_health"] = sc
				}
				v["cluster_status"] = c.StatusLabel
				v["crowdsec_bans"] = c.CrowdsecBans
				v["crowdsec_alerts_24h"] = c.CrowdsecAlerts24h
			}
		}
		if stats, msg := s.appStatsFor(a, protoByNode); stats != nil || msg != "" {
			for k, x := range stats {
				if _, taken := v[k]; !taken || k == "version" {
					v[k] = x
				}
			}
			if msg != "" {
				v["stats_error"] = msg
			}
		}
		out.values[a.Key] = v
		out.fields[a.Key] = tileFieldsFor(a, v)
		out.sects[a.Key] = tileSections(a, v)
	}
	return out, nil
}

func zabbixProblemsByNode(o zabbixmon.Overview) map[uuid.UUID]int {
	out := map[uuid.UUID]int{}
	for _, p := range o.Problems {
		if p.Suppressed {
			continue
		}
		seen := map[uuid.UUID]bool{}
		for _, h := range p.Hosts {
			if h.Node != nil && !seen[h.Node.NodeID] {
				seen[h.Node.NodeID] = true
				out[h.Node.NodeID]++
			}
		}
	}
	return out
}

func tileFieldsFor(a homeapps.App, v map[string]any) []v2Field {
	keys := a.Override.Fields
	if len(keys) == 0 {
		keys = defaultTileFields
		if d, ok := defaultStatsFields[a.Stats]; ok {
			keys = d
		}
	}
	var out []v2Field
	for _, k := range keys {
		val, ok := v[k]
		if !ok {
			continue
		}
		fi, known := tileFields[k]
		if !known {
			fi = fieldInfo{Label: k}
		}
		f := v2Field{Key: k, Label: fi.Label, Value: val, Format: fi.Format}
		switch k {
		case "status":
			f.Level = map[string]string{homeapps.StatusOK: "ok", homeapps.StatusWarn: "warn", homeapps.StatusDown: "down"}[a.Status]
		case "zabbix_problems", "updates_security":
			if n, _ := val.(int); n > 0 {
				f.Level = "warn"
			}
		}
		out = append(out, f)
		if len(out) == MaxTileFields {
			break
		}
	}
	if out == nil {
		out = []v2Field{}
	}
	return out
}

func tileSections(a homeapps.App, v map[string]any) []v2Section {
	var kv []map[string]any
	add := func(label string, key string) {
		if val, ok := v[key]; ok && val != "" {
			kv = append(kv, map[string]any{"label": label, "value": val})
		}
	}
	add("Status", "status")
	if a.Reason != "" {
		kv = append(kv, map[string]any{"label": "Reden", "value": a.Reason})
	}
	add("Cluster", "cluster")
	add("Actief op", "node")
	add("VIP", "vip")
	add("Adres", "domain")
	add("Authentik", "authentik")
	add("Versie", "version")
	add("Aanvragen 24u", "requests_24h")
	add("Bezoekers 24u", "visitors_24h")
	add("Serverfouten 24u", "errors_24h")
	add("Geopend via Authentik 24u", "opened_24h")
	add("Zabbix-problemen", "zabbix_problems")
	add("Updates", "updates")
	out := []v2Section{{Type: "kv", Title: "Overzicht", Items: kv}}
	if len(a.Instances) > 0 {
		var rows []map[string]any
		for _, in := range a.Instances {
			st := "online"
			if !in.Online {
				st = "offline"
			}
			rows = append(rows, map[string]any{"node": in.Node, "ip": in.IP, "status": st, "port": in.Port, "http": in.HTTPStatus,
				"container": in.Container, "unit": in.Unit})
		}
		out = append(out, v2Section{Type: "table", Title: "Waar het draait", Items: rows})
	}
	if len(a.Problems) > 0 {
		var rows []map[string]any
		for _, p := range a.Problems {
			rows = append(rows, map[string]any{"label": "Probleem", "value": p})
		}
		out = append(out, v2Section{Type: "kv", Title: "Problemen", Items: rows})
	}
	return out
}

// --- handlers ---

// visibleApps: wat het token mag zien. Een token voor enkele clusters ziet
// alleen apps in die clusters; verborgen apps staan er nooit in.
func (s *Server) visibleApps(w http.ResponseWriter, r *http.Request) (homeAppsSet, []homeapps.App, bool) {
	d, err := s.homeAppsData(r.Context())
	if errors.Is(err, errHomeAppsUnavailable) {
		writeError(w, http.StatusServiceUnavailable, "unavailable", err.Error())
		return d, nil, false
	}
	if err != nil {
		s.internalError(w, r, err)
		return d, nil, false
	}
	t, _ := r.Context().Value(dashboardTokenKey{}).(store.ApiToken)
	var out []homeapps.App
	for _, a := range d.res.Apps {
		if a.Hidden {
			continue
		}
		if len(t.ClusterIds) > 0 && (a.Cluster == nil || !slices.Contains(t.ClusterIds, a.Cluster.ID)) {
			continue
		}
		out = append(out, a)
	}
	return d, out, true
}

func (s *Server) tile(r *http.Request, d homeAppsSet, a homeapps.App) v2Tile {
	base := baseURL(r, s.cfg.TrustProxyHeaders)
	t := v2Tile{
		Key: a.Key, Slug: a.Slug, Name: a.Name, Group: a.Group, Icon: a.Icon, Status: a.Status, StatusLabel: a.StatusLabel,
		Reason: a.Reason, OK: a.Status == homeapps.StatusOK, URL: a.URL,
		Href: base + "/homepage-apps?app=" + a.Slug, API: base + dashboardV2Base + "/apps/" + a.Slug,
		Fields: d.fields[a.Key], Values: d.values[a.Key], Sections: d.sects[a.Key], App: a, UpdatedAt: d.at,
	}
	if t.Values == nil {
		t.Values = map[string]any{"status": a.StatusLabel}
	}
	if t.Fields == nil {
		t.Fields = []v2Field{}
	}
	if t.Sections == nil {
		t.Sections = []v2Section{}
	}
	t.Where.Nodes, _ = t.Values["nodes"].(string)
	if a.Cluster != nil {
		t.Where.Cluster, t.Where.ClusterSlug = a.Cluster.Name, a.Cluster.Slug
	}
	t.Where.Node, t.Where.VIP = a.ActiveNode, a.VIP
	for _, in := range a.Instances {
		if in.Node == a.ActiveNode || t.Where.Container == "" && t.Where.Unit == "" {
			t.Where.Container, t.Where.Unit, t.Where.Image = in.Container, in.Unit, in.Image
		}
	}
	return t
}

func (s *Server) dashV2Apps(w http.ResponseWriter, r *http.Request) {
	d, apps, ok := s.visibleApps(w, r)
	if !ok {
		return
	}
	group := strings.TrimSpace(r.URL.Query().Get("group"))
	cluster := strings.TrimSpace(r.URL.Query().Get("cluster"))
	out := []v2Tile{}
	for _, a := range apps {
		if group != "" && !strings.EqualFold(a.Group, group) {
			continue
		}
		if cluster != "" && (a.Cluster == nil || !strings.EqualFold(a.Cluster.Slug, cluster) && !strings.EqualFold(a.Cluster.Name, cluster)) {
			continue
		}
		out = append(out, s.tile(r, d, a))
	}
	writeJSON(w, http.StatusOK, out)
}

// dashV2App: één tegel, op slug of key. ?strict=1 geeft 503 als de app
// niet gezond is (de tegelcheck van de homepage wordt dan rood).
func (s *Server) dashV2App(w http.ResponseWriter, r *http.Request) {
	d, apps, ok := s.visibleApps(w, r)
	if !ok {
		return
	}
	key := chi.URLParam(r, "app")
	for _, a := range apps {
		if strings.EqualFold(a.Slug, key) || a.Key == key {
			t := s.tile(r, d, a)
			code := http.StatusOK
			if r.URL.Query().Get("strict") == "1" && !t.OK {
				code = http.StatusServiceUnavailable
			}
			writeJSON(w, code, t)
			return
		}
	}
	writeError(w, http.StatusNotFound, "not_found", "app niet gevonden (of verborgen, of niet zichtbaar met dit token)")
}

type v2Overview struct {
	GeneratedAt time.Time      `json:"generated_at"`
	OK          bool           `json:"ok"`
	Apps        int            `json:"apps"`
	AppsOK      int            `json:"apps_ok"`
	AppsWarn    int            `json:"apps_warn"`
	AppsDown    int            `json:"apps_down"`
	AppsLabel   string         `json:"apps_label"`
	Doubts      int            `json:"doubts"`
	Clusters    any            `json:"clusters"`
	Sources     []string       `json:"sources"`
	Values      map[string]any `json:"values"`
}

func (s *Server) dashV2Overview(w http.ResponseWriter, r *http.Request) {
	d, apps, ok := s.visibleApps(w, r)
	if !ok {
		return
	}
	o := v2Overview{GeneratedAt: d.at, Apps: len(apps), Doubts: len(d.res.Doubts), Sources: v2Sources}
	for _, a := range apps {
		switch a.Status {
		case homeapps.StatusOK:
			o.AppsOK++
		case homeapps.StatusWarn:
			o.AppsWarn++
		case homeapps.StatusDown:
			o.AppsDown++
		}
	}
	o.OK = o.AppsDown == 0 && o.AppsWarn == 0
	o.AppsLabel = fmt.Sprintf("%d/%d gezond", o.AppsOK, o.Apps)
	if c, ok := s.dashboardScoped(w, r); ok {
		o.Clusters = c
		o.Values = map[string]any{
			"apps": o.AppsLabel, "apps_down": o.AppsDown, "apps_warn": o.AppsWarn,
			"clusters_healthy": c.ClustersHealthy, "clusters_total": c.ClustersTotal, "nodes": c.Nodes,
			"incidents": c.IncidentsOpen, "updates": c.Updates, "updates_security": c.UpdatesSecurity,
			"crowdsec_bans": c.CrowdsecBans, "crowdsec_alerts_24h": c.CrowdsecAlerts24h,
		}
		o.OK = o.OK && c.ClustersDown == 0
	} else {
		return
	}
	code := http.StatusOK
	if r.URL.Query().Get("strict") == "1" && !o.OK {
		code = http.StatusServiceUnavailable
	}
	writeJSON(w, code, o)
}

var v2Sources = []string{"agents", "crowdsec", "soc", "zabbix", "opnsense", "npm", "traffic", "logins"}

// dashV2Source: GET /sources/{bron}: één tegel per bron. Zabbix, OPNsense en
// SOC zijn dezelfde als in v1.
func (s *Server) dashV2Source(w http.ResponseWriter, r *http.Request) {
	switch chi.URLParam(r, "source") {
	case "zabbix":
		s.dashboardZabbixHandler(w, r)
	case "opnsense":
		s.dashboardOpnsenseHandler(w, r)
	case "soc":
		s.dashboardSocHandler(w, r)
	case "agents":
		s.dashV2Agents(w, r)
	case "crowdsec":
		s.dashV2CrowdSec(w, r)
	case "npm", "authentik", "publish":
		s.dashV2Publish(w, r)
	case "traffic":
		s.dashV2Traffic(w, r)
	case "logins":
		s.dashV2Logins(w, r)
	default:
		writeError(w, http.StatusNotFound, "not_found", "onbekende bron; kies uit "+strings.Join(v2Sources, ", "))
	}
}

func strictCode(r *http.Request, ok bool) int {
	if r.URL.Query().Get("strict") == "1" && !ok {
		return http.StatusServiceUnavailable
	}
	return http.StatusOK
}

func (s *Server) dashV2Agents(w http.ResponseWriter, r *http.Request) {
	t, _ := r.Context().Value(dashboardTokenKey{}).(store.ApiToken)
	d, err := s.dashboardData(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	var total, online, maint, updates, sec, reboot int
	var offline []string
	for _, n := range d.nodes {
		if len(t.ClusterIds) > 0 && (n.ClusterID == nil || !slices.Contains(t.ClusterIds, *n.ClusterID)) {
			continue
		}
		total++
		switch {
		case n.Online:
			online++
		case n.Maintenance:
			maint++
		default:
			offline = append(offline, n.Hostname)
		}
		updates += n.Updates
		sec += n.UpdatesSecurity
		if n.RebootRequired {
			reboot++
		}
	}
	ok := len(offline) == 0
	writeJSON(w, strictCode(r, ok), map[string]any{
		"ok": ok, "agents": strconv.Itoa(online) + "/" + strconv.Itoa(total), "online": online, "total": total,
		"maintenance": maint, "offline": len(offline), "offline_nodes": nonNil(offline),
		"updates": updates, "updates_security": sec, "reboot_required": reboot,
		"url": baseURL(r, s.cfg.TrustProxyHeaders) + "/nodes",
	})
}

func (s *Server) dashV2CrowdSec(w http.ResponseWriter, r *http.Request) {
	c, ok := s.dashboardScoped(w, r)
	if !ok {
		return
	}
	managed, running := 0, 0
	for _, cl := range c.Clusters {
		managed += cl.CrowdsecManaged
		running += cl.CrowdsecRunning
	}
	good := running == managed
	writeJSON(w, strictCode(r, good), map[string]any{
		"ok": good, "bans": c.CrowdsecBans, "alerts_24h": c.CrowdsecAlerts24h,
		"engines": strconv.Itoa(running) + "/" + strconv.Itoa(managed), "running": running, "managed": managed,
		"url": baseURL(r, s.cfg.TrustProxyHeaders) + "/crowdsec",
	})
}

func (s *Server) dashV2Publish(w http.ResponseWriter, r *http.Request) {
	t, _ := r.Context().Value(dashboardTokenKey{}).(store.ApiToken)
	if len(t.ClusterIds) > 0 {
		writeError(w, http.StatusForbidden, "forbidden", "NPM en Authentik gaan over alle clusters; maak er een token voor alle clusters voor")
		return
	}
	if s.publish == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "publicatie is niet beschikbaar")
		return
	}
	ov, err := s.publish.Overview(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	certSoon := 0
	for _, h := range ov.Hosts {
		if h.CertExpires != nil && time.Until(*h.CertExpires) < 14*24*time.Hour {
			certSoon++
		}
	}
	good := ov.Counts.Problems == 0 && ov.NPM.Error == "" && ov.Authentik.Error == ""
	writeJSON(w, strictCode(r, good), map[string]any{
		"ok": good, "services": ov.Counts.Services, "web": ov.Counts.Web, "unpublished": ov.Counts.Unpublished,
		"hosts": ov.Counts.Hosts, "managed": ov.Counts.Managed, "protected": ov.Counts.Protected, "problems": ov.Counts.Problems,
		"certificates_expiring": certSoon, "npm_linked": ov.NPM.Linked, "npm_version": ov.NPM.Version, "npm_error": ov.NPM.Error,
		"authentik_linked": ov.Authentik.Linked, "authentik_version": ov.Authentik.Version, "authentik_error": ov.Authentik.Error,
		"url": baseURL(r, s.cfg.TrustProxyHeaders) + "/publicatie",
	})
}

// --- de indeling voor de homepage ---

type v2LayoutTile struct {
	Key         string `json:"key"`
	App         string `json:"app"`
	Name        string `json:"name"`
	Icon        string `json:"icon"`
	Href        string `json:"href"`
	Description string `json:"description"`
	API         string `json:"api"`
	Gone        bool   `json:"gone"`
}

type v2LayoutGroup struct {
	Name  string         `json:"name"`
	Tiles []v2LayoutTile `json:"tiles"`
}

type v2Layout struct {
	Version     string          `json:"version"`
	GeneratedAt time.Time       `json:"generated_at"`
	ManagedBy   string          `json:"managed_by"`
	Groups      []v2LayoutGroup `json:"groups"`
}

// dashV2Layout: welke tegels de homepage moet tonen, in groepen en
// volgorde. De homepage maakt, wijzigt en verwijdert zelf de tegels met
// managed_by clusterforge. ETag/If-None-Match: 304 als er niets veranderde.
func (s *Server) dashV2Layout(w http.ResponseWriter, r *http.Request) {
	_, apps, ok := s.visibleApps(w, r)
	if !ok {
		return
	}
	base := baseURL(r, s.cfg.TrustProxyHeaders)
	l := v2Layout{ManagedBy: "clusterforge", Groups: []v2LayoutGroup{}}
	idx := map[string]int{}
	for _, a := range apps {
		i, ok := idx[a.Group]
		if !ok {
			i = len(l.Groups)
			idx[a.Group] = i
			l.Groups = append(l.Groups, v2LayoutGroup{Name: a.Group, Tiles: []v2LayoutTile{}})
		}
		desc := ""
		if a.Cluster != nil {
			desc = a.Cluster.Name
		} else if a.ActiveNode != "" {
			desc = a.ActiveNode
		}
		if a.Gone {
			desc = "weg? " + a.Reason
		}
		l.Groups[i].Tiles = append(l.Groups[i].Tiles, v2LayoutTile{
			Key: a.Key, App: a.Slug, Name: a.Name, Icon: a.Icon, Href: a.URL, Description: desc,
			API: base + dashboardV2Base + "/apps/" + a.Slug, Gone: a.Gone,
		})
	}
	data, _ := json.Marshal(l.Groups)
	sum := sha256.Sum256(data)
	l.Version = hex.EncodeToString(sum[:8])
	etag := `"` + l.Version + `"`
	w.Header().Set("ETag", etag)
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	l.GeneratedAt = time.Now().UTC()
	writeJSON(w, http.StatusOK, l)
}

// --- beheer in de GUI (onder /api/v1, met sessie) ---

type homeAppsAdmin struct {
	Apps      []homeapps.App       `json:"apps"`
	Doubts    []homeapps.Doubt     `json:"doubts"`
	Values    map[string]any       `json:"values"`
	Fields    map[string]fieldInfo `json:"fields"`
	Defaults  []string             `json:"default_fields"`
	Groups    []string             `json:"groups"`
	BasePath  string               `json:"base_path"`
	Generated time.Time            `json:"generated_at"`
}

func (s *Server) listHomeApps(w http.ResponseWriter, r *http.Request) {
	d, err := s.homeAppsData(r.Context())
	if errors.Is(err, errHomeAppsUnavailable) {
		writeError(w, http.StatusServiceUnavailable, "unavailable", err.Error())
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	vals := map[string]any{}
	for k, v := range d.values {
		vals[k] = v
	}
	writeJSON(w, http.StatusOK, homeAppsAdmin{
		Apps: nonNil(d.res.Apps), Doubts: nonNil(d.res.Doubts), Values: vals, Fields: tileFields, Defaults: defaultTileFields,
		Groups: []string{homeapps.GroupMedia, homeapps.GroupOffice, homeapps.GroupHome, homeapps.GroupDownloads, homeapps.GroupNetwork,
			homeapps.GroupSecurity, homeapps.GroupMonitoring, homeapps.GroupInfra, homeapps.GroupOther},
		BasePath: dashboardV2Base, Generated: d.at,
	})
}

func (s *Server) saveHomeAppOverride(w http.ResponseWriter, r *http.Request) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var in struct {
		Key      string            `json:"key"`
		Override homeapps.Override `json:"override"`
	}
	if !decode(w, r, &in) {
		return
	}
	if len(in.Override.Fields) > MaxTileFields {
		writeError(w, http.StatusBadRequest, "bad_request", fmt.Sprintf("hoogstens %d velden op een tegel", MaxTileFields))
		return
	}
	if len(in.Override.Name) > 80 || len(in.Override.Group) > 40 || len(in.Override.Icon) > 120 || len(in.Override.URL) > 300 {
		writeError(w, http.StatusBadRequest, "bad_request", "naam, groep, icoon of adres is te lang")
		return
	}
	if u := in.Override.URL; u != "" && !strings.HasPrefix(u, "https://") && !strings.HasPrefix(u, "http://") {
		writeError(w, http.StatusBadRequest, "bad_request", "het adres moet met http:// of https:// beginnen")
		return
	}
	err := homeapps.NewStore(s.pool).SetOverride(r.Context(), in.Key, in.Override)
	if errors.Is(err, homeapps.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "app niet gevonden")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	payload := map[string]any{"key": in.Key, "override": in.Override}
	if err := s.ev.Write(r.Context(), nil, events.Event{Actor: events.User(p.User.ID), SubjectType: "home_app", SubjectID: in.Key, Action: "home_app.override", Payload: payload}); err != nil {
		s.log.Warn("homepage-API: logboek", "err", err)
	}
	s.invalidateHomeApps()
	s.listHomeApps(w, r)
}

func (s *Server) decideHomeAppDoubt(w http.ResponseWriter, r *http.Request) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var in struct {
		Key    string `json:"key"`
		Choice string `json:"choice"`
		Name   string `json:"name"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Key == "" || (in.Choice != "" && in.Choice != "ignore" && in.Choice != "app") || len(in.Name) > 80 {
		writeError(w, http.StatusBadRequest, "bad_request", "kies ignore of app (of leeg om terug te zetten)")
		return
	}
	if err := homeapps.NewStore(s.pool).Decide(r.Context(), in.Key, in.Choice, in.Name); err != nil {
		s.internalError(w, r, err)
		return
	}
	payload := map[string]any{"key": in.Key, "choice": in.Choice, "name": in.Name}
	if err := s.ev.Write(r.Context(), nil, events.Event{Actor: events.User(p.User.ID), SubjectType: "home_app", SubjectID: in.Key, Action: "home_app.doubt", Payload: payload}); err != nil {
		s.log.Warn("homepage-API: logboek", "err", err)
	}
	s.invalidateHomeApps()
	s.listHomeApps(w, r)
}

// publishAllClusters: verkeer en logins gaan over alle clusters.
func (s *Server) publishAllClusters(w http.ResponseWriter, r *http.Request) bool {
	t, _ := r.Context().Value(dashboardTokenKey{}).(store.ApiToken)
	if len(t.ClusterIds) > 0 {
		writeError(w, http.StatusForbidden, "forbidden", "NPM en Authentik gaan over alle clusters; maak er een token voor alle clusters voor")
		return false
	}
	if s.publish == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "publicatie is niet beschikbaar")
		return false
	}
	return true
}

// dashV2Traffic: GET /sources/traffic?window=24h: verkeer van NPM in één
// tegel, met de drukste hosts en de IP's die aandacht vragen.
func (s *Server) dashV2Traffic(w http.ResponseWriter, r *http.Request) {
	if !s.publishAllClusters(w, r) {
		return
	}
	v, err := s.publish.Traffic(r.Context(), r.URL.Query().Get("window"))
	if err != nil {
		s.publishError(w, err)
		return
	}
	top := []map[string]any{}
	for i, h := range v.Hosts {
		if i == 5 {
			break
		}
		top = append(top, map[string]any{"domain": h.Domain, "requests": h.Requests, "visitors": h.Visitors, "errors": h.S5xx})
	}
	watch := []map[string]any{}
	for _, p := range v.IPs {
		if len(watch) == 10 || (!p.Suspicious && (p.Ban == nil || p.AfterBan == 0)) {
			continue
		}
		state := "verdacht, geen ban"
		if p.Ban != nil {
			state = "geblokkeerd maar komt nog binnen"
		}
		watch = append(watch, map[string]any{"ip": p.IP, "requests": p.Requests, "denied": p.Denied, "state": state, "why": p.Why})
	}
	good := v.Compare.BannedPassing == 0 && v.Compare.Suspicious == 0
	errRate := 0.0
	if v.Totals.Requests > 0 {
		errRate = float64(v.Totals.S5xx) * 100 / float64(v.Totals.Requests)
	}
	writeJSON(w, strictCode(r, good), map[string]any{
		"ok": good, "window": v.Window, "requests": v.Totals.Requests, "visitors": v.Totals.Visitors,
		"errors": v.Totals.S5xx, "error_rate": errRate, "denied": v.Totals.Denied, "bytes": v.Totals.Bytes,
		"connections": v.Totals.Connections, "banned_seen": v.Compare.BannedSeen, "banned_passing": v.Compare.BannedPassing,
		"suspicious": v.Compare.Suspicious, "active_bans": v.Compare.ActiveBans, "top_hosts": top, "watch_ips": watch,
		"url": baseURL(r, s.cfg.TrustProxyHeaders) + "/publicatie/verkeer",
	})
}

// dashV2Logins: GET /sources/logins?window=24h: logins en mislukte logins.
func (s *Server) dashV2Logins(w http.ResponseWriter, r *http.Request) {
	if !s.publishAllClusters(w, r) {
		return
	}
	v, err := s.publish.Logins(r.Context(), r.URL.Query().Get("window"))
	if err != nil {
		s.publishError(w, err)
		return
	}
	ips := []map[string]any{}
	for i, f := range v.FailedIPs {
		if i == 10 {
			break
		}
		ips = append(ips, map[string]any{"ip": f.IP, "failed": f.Failed, "usernames": f.Usernames, "country": f.Country, "banned": f.Ban != nil})
	}
	good := v.Totals.Unbanned == 0
	writeJSON(w, strictCode(r, good), map[string]any{
		"ok": good, "window": v.Window, "logins": v.Totals.Logins, "failed": v.Totals.Failed, "users": v.Totals.Users,
		"failed_ips": v.Totals.FailedIPs, "failed_ips_not_banned": v.Totals.Unbanned, "app_opens": v.Totals.AppOpening,
		"top_failed_ips": ips, "url": baseURL(r, s.cfg.TrustProxyHeaders) + "/publicatie/logins",
	})
}
