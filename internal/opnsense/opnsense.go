// Package opnsense beheert OPNsense-firewalls via de cf-agent op elke
// firewall (fase 34). Lezen gaat rechtstreeks over de agent (lokale API,
// configctl met een vaste lijst, revisies van config.xml); veranderen gaat
// als taak opnsense.change: voorcontrole, toepassen op de master met een
// terugdraaitimer op de firewall zelf, proeven, bevestigen of terugdraaien,
// en daarna de configsync naar de backup. Welke aanroepen de GUI mag doen,
// staat in policy.go.
package opnsense

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/jobs"
	"github.com/Jonasz1996/clusterforge/internal/noderun"
	"github.com/Jonasz1996/clusterforge/internal/secrets"
	"github.com/Jonasz1996/clusterforge/internal/status"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// RequestTimeout is hoe lang een leesverzoek mag duren.
const RequestTimeout = 60 * time.Second

// Bus praat met de agents.
type Bus interface {
	noderun.Bus
	PVE(ctx context.Context, nodeID uuid.UUID, req protocol.PVERequest) (protocol.PVEResponse, error)
}

// ValidationError is een fout in een vraag.
type ValidationError struct{ Msg string }

func (e ValidationError) Error() string { return e.Msg }

// ErrNotFound: geen OPNsense-firewall met een agent onder dat id.
var ErrNotFound = errors.New("geen OPNsense-firewall met een agent")

type Service struct {
	q    *store.Queries
	ev   *events.Writer
	log  *slog.Logger
	jobs *jobs.Runner
	bus  Bus
	Run  *noderun.Runner
	// Probe test of een TCP-poort antwoordt; nil gebruikt net.Dial. Tests
	// vervangen hem.
	Probe func(ctx context.Context, addr string) error
	// Wait is de pauze tussen toepassen en proeven; tests zetten 0.
	Wait time.Duration
	// Settle is hoe lang de proeven opnieuw proberen; 0 is de helft van de
	// terugdraaitijd. Tests verkorten hem.
	Settle time.Duration
	// Poll is de pauze tussen twee keer CARP-rollen of firmwarestatus
	// lezen; 0 is 2 s. Tests verkorten hem.
	Poll time.Duration
	// Takeover is hoe lang een overname mag duren; 0 is TakeoverWithin.
	Takeover time.Duration
	cache    getCache
	// Now is er voor tests.
	Now func() time.Time
	// Box versleutelt een geplakte API-sleutel in de taak; nil zonder
	// masterkey.
	Box *secrets.Box
}

func NewService(pool *pgxpool.Pool, ev *events.Writer, log *slog.Logger, runner *jobs.Runner, bus Bus) *Service {
	s := &Service{q: store.New(pool), ev: ev, log: log, jobs: runner, bus: bus, Run: &noderun.Runner{Bus: bus}, Now: time.Now, Wait: 8 * time.Second}
	if runner != nil {
		runner.Register(KindChange, s.runChange)
		runner.Register(KindSync, s.runSync)
		runner.Register(KindKey, s.runKey)
		runner.Register(KindCarp, s.runCarp)
		runner.Register(KindFirmware, s.runFirmware)
		runner.Register(KindBackup, s.runBackup)
	}
	return s
}

// Firewall is een OPNsense-firewall met een agent.
type Firewall struct {
	NodeID      uuid.UUID               `json:"node_id"`
	Hostname    string                  `json:"hostname"`
	Address     string                  `json:"address,omitempty"`
	ClusterID   *uuid.UUID              `json:"cluster_id,omitempty"`
	ClusterName string                  `json:"cluster_name,omitempty"`
	Environment string                  `json:"environment,omitempty"`
	Facts       *protocol.OPNsenseFacts `json:"facts"`
	// Role is master, backup, mixed of leeg (geen CARP).
	Role        string     `json:"role"`
	Protocol    int        `json:"protocol"`
	Agent       string     `json:"agent_version,omitempty"`
	Online      bool       `json:"online"`
	HeartbeatAt *time.Time `json:"heartbeat_at,omitempty"`
	// Why zegt waarom ClusterForge de firewall niet kan beheren; leeg als
	// het kan.
	Why string `json:"why,omitempty"`
}

func (f Firewall) ready() string {
	switch {
	case !f.Online:
		return fmt.Sprintf("de agent op %s is offline", f.Hostname)
	case f.Protocol < protocol.OPNsenseSince:
		return fmt.Sprintf("de agent op %s is te oud voor OPNsense-beheer (protocol %d, %d nodig); werk hem bij via Nodes → Agents bijwerken", f.Hostname, f.Protocol, protocol.OPNsenseSince)
	}
	return ""
}

// addresses zijn alle adressen van de firewall: het nodeadres en de
// adressen op zijn interfaces, zonder prefix.
func (f Firewall) addresses() []string {
	var out []string
	if f.Address != "" {
		out = append(out, f.Address)
	}
	if f.Facts != nil {
		for _, i := range f.Facts.Interfaces {
			for _, a := range i.Addresses {
				addr, _, _ := strings.Cut(a, "/")
				if !slices.Contains(out, addr) {
					out = append(out, addr)
				}
			}
		}
	}
	return out
}

// Firewalls geeft alle OPNsense-firewalls met een agent.
func (s *Service) Firewalls(ctx context.Context) ([]Firewall, error) {
	rows, err := s.q.ListOPNsenseAgents(ctx)
	if err != nil {
		return nil, err
	}
	out := []Firewall{}
	for _, r := range rows {
		var f protocol.OPNsenseFacts
		if json.Unmarshal(r.Opnsense, &f) != nil {
			continue
		}
		fw := Firewall{
			NodeID: r.ID, Hostname: r.Hostname, Address: r.Address, ClusterID: r.ClusterID, ClusterName: r.ClusterName,
			Environment: r.ClusterEnvironment, Facts: &f, Role: f.Role(), HeartbeatAt: r.HeartbeatAt,
		}
		if r.AgentProtocol != nil {
			fw.Protocol = int(*r.AgentProtocol)
		}
		if r.AgentVersion != nil {
			fw.Agent = *r.AgentVersion
		}
		fw.Online = r.HeartbeatAt != nil && s.Now().Sub(*r.HeartbeatAt) < status.HeartbeatLate
		fw.Why = fw.ready()
		out = append(out, fw)
	}
	return out, nil
}

// Firewall geeft één firewall.
func (s *Service) Firewall(ctx context.Context, id uuid.UUID) (Firewall, error) {
	list, err := s.Firewalls(ctx)
	if err != nil {
		return Firewall{}, err
	}
	for _, f := range list {
		if f.NodeID == id {
			return f, nil
		}
	}
	return Firewall{}, ErrNotFound
}

// Problem is iets wat aandacht vraagt.
type Problem struct {
	// Level is crit of warn.
	Level string `json:"level"`
	Text  string `json:"text"`
}

// VIPRow is één VIP met de toestand op elke node.
type VIPRow struct {
	Address   string            `json:"address"`
	Interface string            `json:"interface"`
	VHID      int               `json:"vhid"`
	Mode      string            `json:"mode"`
	Descr     string            `json:"descr,omitempty"`
	States    map[string]string `json:"states"`
}

// Cluster is een HA-paar (of een losse firewall).
type Cluster struct {
	// Key is het id van het ClusterForge-cluster, of anders van de eerste
	// node.
	Key         string     `json:"key"`
	Name        string     `json:"name"`
	ClusterID   *uuid.UUID `json:"cluster_id,omitempty"`
	Environment string     `json:"environment,omitempty"`
	Nodes       []Firewall `json:"nodes"`
	// Master is de node die voor elke CARP-VIP MASTER is; nil als er geen
	// (of meer dan één) is.
	Master *uuid.UUID `json:"master,omitempty"`
	// Level is ok, warn of crit.
	Level    string    `json:"level"`
	Problems []Problem `json:"problems"`
	VIPs     []VIPRow  `json:"vips"`
	// HA: er is een backup in dit cluster.
	HA bool `json:"ha"`
}

// Clusters groepeert de firewalls: per ClusterForge-cluster, en anders als
// paar als de een de pfsync- of syncpeer van de ander is.
func (s *Service) Clusters(ctx context.Context) ([]Cluster, error) {
	fws, err := s.Firewalls(ctx)
	if err != nil {
		return nil, err
	}
	return groupClusters(fws), nil
}

func groupClusters(fws []Firewall) []Cluster {
	var out []Cluster
	used := map[uuid.UUID]bool{}
	byCluster := map[uuid.UUID]int{}
	for _, f := range fws {
		if f.ClusterID == nil {
			continue
		}
		if i, ok := byCluster[*f.ClusterID]; ok {
			out[i].Nodes = append(out[i].Nodes, f)
		} else {
			byCluster[*f.ClusterID] = len(out)
			out = append(out, Cluster{Key: f.ClusterID.String(), Name: f.ClusterName, ClusterID: f.ClusterID, Environment: f.Environment, Nodes: []Firewall{f}})
		}
		used[f.NodeID] = true
	}
	for i, f := range fws {
		if used[f.NodeID] {
			continue
		}
		used[f.NodeID] = true
		c := Cluster{Key: f.NodeID.String(), Name: f.Hostname, Nodes: []Firewall{f}}
		for _, g := range fws[i+1:] {
			if used[g.NodeID] || g.ClusterID != nil {
				continue
			}
			if peers(f, g) {
				used[g.NodeID] = true
				c.Nodes = append(c.Nodes, g)
				c.Name = f.Hostname + " + " + g.Hostname
			}
		}
		out = append(out, c)
	}
	for i := range out {
		out[i].assess()
	}
	return out
}

// peers is true als f naar g synchroniseert (of omgekeerd).
func peers(f, g Firewall) bool {
	match := func(a, b Firewall) bool {
		if a.Facts == nil {
			return false
		}
		for _, p := range []string{a.Facts.HA.PfsyncPeer, a.Facts.HA.SyncTo} {
			host := hostOf(p)
			if host != "" && slices.Contains(b.addresses(), host) {
				return true
			}
		}
		return false
	}
	return match(f, g) || match(g, f)
}

// hostOf haalt het adres uit een syncpeer: een IP of een URL.
func hostOf(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	if _, rest, ok := strings.Cut(p, "://"); ok {
		p = rest
	}
	p, _, _ = strings.Cut(p, "/")
	if a, err := netip.ParseAddrPort(p); err == nil {
		return a.Addr().String()
	}
	if a, err := netip.ParseAddr(strings.Trim(p, "[]")); err == nil {
		return a.String()
	}
	return p
}

// assess bepaalt master, VIP-tabel en problemen.
func (c *Cluster) assess() {
	c.Problems = []Problem{}
	c.HA = len(c.Nodes) > 1
	add := func(level, format string, args ...any) {
		c.Problems = append(c.Problems, Problem{Level: level, Text: fmt.Sprintf(format, args...)})
	}
	var masters []Firewall
	carp := false
	vips := map[string]*VIPRow{}
	for _, n := range c.Nodes {
		if n.Why != "" {
			add("crit", "%s: %s", n.Hostname, n.Why)
		}
		f := n.Facts
		if f == nil {
			continue
		}
		for _, v := range f.VIPs {
			key := fmt.Sprintf("%s/%d", v.Address, v.VHID)
			row, ok := vips[key]
			if !ok {
				row = &VIPRow{Address: v.Address, Interface: v.Interface, VHID: v.VHID, Mode: v.Mode, Descr: v.Descr, States: map[string]string{}}
				vips[key] = row
			}
			row.States[n.Hostname] = v.State
			if v.Mode == "carp" {
				carp = true
			}
		}
		switch n.Role {
		case "master":
			masters = append(masters, n)
		case "mixed":
			add("crit", "%s is niet voor alle CARP-VIP's in dezelfde toestand (een deel MASTER, een deel BACKUP of INIT)", n.Hostname)
		}
		if f.CARP.Maintenance {
			add("warn", "%s staat in CARP-onderhoudsmodus", n.Hostname)
		} else if f.CARP.Demotion > 0 {
			add("crit", "%s zet zichzelf terug (CARP-demotion %d): controleer de links van de interfaces met een CARP-VIP", n.Hostname, f.CARP.Demotion)
		}
		if carp && !f.CARP.Allow {
			add("warn", "CARP staat uit op %s (net.inet.carp.allow = 0)", n.Hostname)
		}
		if c.HA && f.HA.PfsyncInterface != "" && !f.HA.PfsyncUp {
			add("warn", "pfsync op %s meldt geen synchronisatie: na een failover gaan open verbindingen verloren", n.Hostname)
		}
		if !f.API.OK && n.Online {
			msg := f.API.Error
			if msg == "" {
				msg = "onbekende fout"
			}
			add("warn", "%s: de agent kan de lokale API niet gebruiken (%s); wijzigingen kunnen niet", n.Hostname, msg)
		}
	}
	for _, v := range vips {
		c.VIPs = append(c.VIPs, *v)
	}
	sort.Slice(c.VIPs, func(i, j int) bool {
		if c.VIPs[i].Interface != c.VIPs[j].Interface {
			return c.VIPs[i].Interface < c.VIPs[j].Interface
		}
		return c.VIPs[i].VHID < c.VIPs[j].VHID
	})
	if c.VIPs == nil {
		c.VIPs = []VIPRow{}
	}
	switch {
	case len(masters) == 1:
		id := masters[0].NodeID
		c.Master = &id
	case len(masters) > 1:
		var names []string
		for _, m := range masters {
			names = append(names, m.Hostname)
		}
		add("crit", "split-brain: %s zijn allebei MASTER; wijzigingen zijn geblokkeerd", strings.Join(names, " en "))
	case carp && c.HA:
		add("crit", "geen enkele firewall is MASTER voor alle CARP-VIP's")
	}
	if c.HA && c.Master != nil {
		var m Firewall
		for _, n := range c.Nodes {
			if n.NodeID == *c.Master {
				m = n
			}
		}
		if m.Facts != nil && m.Facts.HA.SyncTo == "" {
			add("warn", "op de master %s staat geen configsync naar de backup (System → High Availability → Synchronize Config to IP)", m.Hostname)
		}
		versions := map[string]bool{}
		for _, n := range c.Nodes {
			if n.Facts != nil {
				versions[n.Facts.Version] = true
			}
		}
		if len(versions) > 1 {
			add("warn", "de firewalls draaien verschillende OPNsense-versies")
		}
		for _, n := range c.Nodes {
			if n.NodeID != m.NodeID && n.Role != "backup" && n.Role != "" {
				add("warn", "%s is geen BACKUP (rol %s)", n.Hostname, n.Role)
			}
		}
	}
	c.Level = "ok"
	for _, p := range c.Problems {
		if p.Level == "crit" {
			c.Level = "crit"
			break
		}
		c.Level = "warn"
	}
}

// ClusterOf geeft het cluster van een firewall.
func (s *Service) ClusterOf(ctx context.Context, id uuid.UUID) (Cluster, error) {
	list, err := s.Clusters(ctx)
	if err != nil {
		return Cluster{}, err
	}
	for _, c := range list {
		for _, n := range c.Nodes {
			if n.NodeID == id {
				return c, nil
			}
		}
	}
	return Cluster{}, ErrNotFound
}

type cacheEntry struct {
	at   time.Time
	data json.RawMessage
}

type getCache struct {
	mu sync.Mutex
	m  map[string]cacheEntry
}

const getCacheTTL = 4 * time.Second

func (c *getCache) get(k string) (json.RawMessage, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[k]
	if !ok || time.Since(e.at) > getCacheTTL {
		return nil, false
	}
	return e.data, true
}

func (c *getCache) put(k string, v json.RawMessage) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil || len(c.m) > 500 {
		c.m = map[string]cacheEntry{}
	}
	c.m[k] = cacheEntry{at: time.Now(), data: v}
}

func (c *getCache) clear() {
	c.mu.Lock()
	c.m = nil
	c.mu.Unlock()
}

// request stuurt een leesverzoek naar de agent van een firewall.
func (s *Service) request(ctx context.Context, id uuid.UUID, req protocol.PVERequest, cache bool) (json.RawMessage, error) {
	key := id.String() + "|" + req.Op + "|" + req.Path + "|" + canonical(req.Params)
	if cache {
		if d, ok := s.cache.get(key); ok {
			return d, nil
		}
	}
	ctx, cancel := context.WithTimeout(ctx, RequestTimeout)
	defer cancel()
	res, err := s.bus.PVE(ctx, id, req)
	if err != nil {
		return nil, err
	}
	if res.Error != "" {
		return nil, errors.New(res.Error)
	}
	if cache {
		s.cache.put(key, res.Data)
	}
	return res.Data, nil
}

// ready geeft de firewall als ClusterForge hem kan lezen.
func (s *Service) ready(ctx context.Context, id uuid.UUID) (Firewall, error) {
	f, err := s.Firewall(ctx, id)
	if err != nil {
		return f, err
	}
	if f.Why != "" {
		return f, ValidationError{f.Why}
	}
	return f, nil
}

// Get leest een pad van de lokale API (GET). Een kijker leest geen
// gebruikers, sleutels of back-ups.
func (s *Service) Get(ctx context.Context, id uuid.UUID, path string, admin bool) (json.RawMessage, error) {
	if !protocol.ValidOPNsenseAPIPath(path) {
		return nil, ValidationError{"ongeldig API-pad"}
	}
	if !readAllowed(path, admin) {
		return nil, ValidationError{"dit pad mag je niet lezen via ClusterForge: " + path}
	}
	if _, err := s.ready(ctx, id); err != nil {
		return nil, err
	}
	return s.request(ctx, id, protocol.PVERequest{Op: protocol.OPNOpGet, Path: path}, true)
}

// Search doet een zoekopdracht (searchItem, searchRule ...) met POST.
func (s *Service) Search(ctx context.Context, id uuid.UUID, path string, params map[string]string, admin bool) (json.RawMessage, error) {
	if !protocol.ValidOPNsenseAPIPath(path) {
		return nil, ValidationError{"ongeldig API-pad"}
	}
	if !readAllowed(path, admin) {
		return nil, ValidationError{"dit pad mag je niet lezen via ClusterForge: " + path}
	}
	if _, err := s.ready(ctx, id); err != nil {
		return nil, err
	}
	if params == nil {
		params = map[string]string{}
	}
	if _, ok := params["rowCount"]; !ok {
		params["rowCount"] = "-1"
	}
	return s.request(ctx, id, protocol.PVERequest{Op: protocol.OPNOpPost, Path: path, Params: params}, true)
}

// Configctl draait een leesactie van configctl (protocol.OPNReadActions).
func (s *Service) Configctl(ctx context.Context, id uuid.UUID, action string) (json.RawMessage, error) {
	if !slices.Contains(protocol.OPNReadActions, action) {
		return nil, ValidationError{"onbekende leesactie " + action}
	}
	if _, err := s.ready(ctx, id); err != nil {
		return nil, err
	}
	return s.request(ctx, id, protocol.PVERequest{Op: protocol.OPNOpConfigctl, Path: action}, true)
}

// Revisions geeft de revisies van config.xml.
func (s *Service) Revisions(ctx context.Context, id uuid.UUID) (json.RawMessage, error) {
	if _, err := s.ready(ctx, id); err != nil {
		return nil, err
	}
	return s.request(ctx, id, protocol.PVERequest{Op: protocol.OPNOpRevisions}, true)
}

// revision geeft de tekst van een revisie (zonder geheimen).
func (s *Service) revision(ctx context.Context, id uuid.UUID, name string) (string, error) {
	data, err := s.request(ctx, id, protocol.PVERequest{Op: protocol.OPNOpRevision, Path: name}, name != "current")
	if err != nil {
		return "", err
	}
	var text string
	if err := json.Unmarshal(data, &text); err != nil {
		return "", err
	}
	return text, nil
}

// Diff is het verschil tussen twee revisies.
type Diff struct {
	From    string `json:"from"`
	To      string `json:"to"`
	Diff    string `json:"diff"`
	Changed int    `json:"changed"`
}

// RevisionDiff vergelijkt twee revisies van dezelfde firewall, of dezelfde
// revisie ("current") van twee firewalls (master tegenover backup).
func (s *Service) RevisionDiff(ctx context.Context, id uuid.UUID, from, to string, otherID *uuid.UUID) (Diff, error) {
	if _, err := s.ready(ctx, id); err != nil {
		return Diff{}, err
	}
	a, err := s.revision(ctx, id, from)
	if err != nil {
		return Diff{}, err
	}
	bID := id
	if otherID != nil {
		if _, err := s.ready(ctx, *otherID); err != nil {
			return Diff{}, err
		}
		bID = *otherID
	}
	b, err := s.revision(ctx, bID, to)
	if err != nil {
		return Diff{}, err
	}
	if otherID != nil {
		// De revisie-informatie en de eigen identiteit verschillen altijd.
		a, b = stripVolatile(a), stripVolatile(b)
	}
	d, n := textDiff(a, b, from, to)
	return Diff{From: from, To: to, Diff: d, Changed: n}, nil
}

func canonical(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k + "=" + m[k] + "&")
	}
	return b.String()
}
