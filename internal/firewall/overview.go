package firewall

import (
	"context"
	"errors"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// Rule is een eigen regel.
type Rule struct {
	ID          uuid.UUID  `json:"id"`
	ClusterID   *uuid.UUID `json:"cluster_id"`
	ClusterName string     `json:"cluster_name"`
	NodeID      *uuid.UUID `json:"node_id"`
	Hostname    string     `json:"hostname"`
	Action      string     `json:"action"`
	Proto       string     `json:"proto"`
	Port        string     `json:"port"`
	Source      string     `json:"source"`
	Comment     string     `json:"comment"`
	CreatedAt   time.Time  `json:"created_at"`
}

// Rules geeft de eigen regels.
func (s *Service) Rules(ctx context.Context) ([]Rule, error) {
	rows, err := s.pool.Query(ctx, `SELECT r.id, r.cluster_id, coalesce(c.name, ''), r.node_id, coalesce(n.hostname, ''), r.action, r.proto, r.port,
		r.source, r.comment, r.created_at FROM firewall_rules r LEFT JOIN clusters c ON c.id = r.cluster_id LEFT JOIN nodes n ON n.id = r.node_id
		ORDER BY r.created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Rule{}
	for rows.Next() {
		var r Rule
		if err := rows.Scan(&r.ID, &r.ClusterID, &r.ClusterName, &r.NodeID, &r.Hostname, &r.Action, &r.Proto, &r.Port, &r.Source, &r.Comment, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// AddRule bewaart een eigen regel.
func (s *Service) AddRule(ctx context.Context, actor events.Actor, userID *uuid.UUID, in Rule) (Rule, error) {
	switch {
	case in.Action != "open" && in.Action != "close":
		return Rule{}, &FieldError{"actie is open of close"}
	case in.Proto != "tcp" && in.Proto != "udp":
		return Rule{}, &FieldError{"protocol is tcp of udp"}
	case protocol.ValidPort(in.Port) != nil:
		return Rule{}, &FieldError{protocol.ValidPort(in.Port).Error()}
	case in.ClusterID != nil && in.NodeID != nil:
		return Rule{}, &FieldError{"kies een cluster of een node, niet allebei"}
	case len(in.Comment) > 60:
		return Rule{}, &FieldError{"commentaar hoogstens 60 tekens"}
	}
	if in.Action == "open" {
		if in.Source == "" {
			in.Source = ClassLAN
		}
		if !slices.Contains(Classes, in.Source) && (in.Source == "any" || protocol.ValidSource(in.Source) != nil) {
			return Rule{}, &FieldError{"bron is any, lan, cluster, admin, containers of een adres/netwerk"}
		}
	} else {
		in.Source = ""
	}
	var id uuid.UUID
	if err := s.pool.QueryRow(ctx, `INSERT INTO firewall_rules (cluster_id, node_id, action, proto, port, source, comment, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING id`, in.ClusterID, in.NodeID, in.Action, in.Proto, in.Port, in.Source, in.Comment, userID).Scan(&id); err != nil {
		return Rule{}, err
	}
	s.event(ctx, actor, nil, "firewall.rule_added", map[string]any{"rule_id": id.String(), "action": in.Action, "proto": in.Proto,
		"port": in.Port, "source": in.Source, "comment": in.Comment})
	rules, err := s.Rules(ctx)
	if err != nil {
		return Rule{}, err
	}
	for _, r := range rules {
		if r.ID == id {
			return r, nil
		}
	}
	return Rule{}, ErrNotFound
}

// DeleteRule haalt een eigen regel weg.
func (s *Service) DeleteRule(ctx context.Context, actor events.Actor, id uuid.UUID) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM firewall_rules WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrRuleNotFound
	}
	s.event(ctx, actor, nil, "firewall.rule_removed", map[string]any{"rule_id": id.String()})
	return nil
}

// ErrRuleNotFound: geen eigen regel met dat id.
var ErrRuleNotFound = errors.New("regel niet gevonden")

// Cell is de stand van een poort op één node.
type Cell struct {
	State string   `json:"state"`
	Class string   `json:"class"`
	From  []string `json:"from"`
	// Applied: de regel staat ook echt op de node.
	Applied bool `json:"applied"`
	// Blocked24h: pakketten naar deze poort die ufw weggooide.
	Blocked24h int64 `json:"blocked_24h"`
}

// PortRow is één poort in de tabel van een cluster.
type PortRow struct {
	Proto    string          `json:"proto"`
	Port     string          `json:"port"`
	Software string          `json:"software"`
	Label    string          `json:"label"`
	Cells    map[string]Cell `json:"cells"`
}

// ClusterView is de tabel poort × node van een cluster.
type ClusterView struct {
	ID    *uuid.UUID `json:"id"`
	Name  string     `json:"name"`
	Slug  string     `json:"slug"`
	Nodes []NodeRef  `json:"nodes"`
	Ports []PortRow  `json:"ports"`
}

type NodeRef struct {
	ID       uuid.UUID `json:"id"`
	Hostname string    `json:"hostname"`
	Mode     string    `json:"mode"`
	Active   bool      `json:"active"`
}

type Warning struct {
	NodeID   uuid.UUID `json:"node_id"`
	Hostname string    `json:"hostname"`
	Text     string    `json:"text"`
}

type HourCount struct {
	Hour  time.Time `json:"hour"`
	Count int64     `json:"count"`
}

// TrafficRow is verkeer van één bron naar één poort op één node.
type TrafficRow struct {
	NodeID      uuid.UUID  `json:"node_id"`
	Hostname    string     `json:"hostname"`
	ClusterID   *uuid.UUID `json:"cluster_id"`
	ClusterName string     `json:"cluster_name"`
	Src         string     `json:"src"`
	Proto       string     `json:"proto"`
	Port        int        `json:"port"`
	Count       int64      `json:"count"`
	First       time.Time  `json:"first"`
	Last        time.Time  `json:"last"`
	// SrcNode is de node met dat adres, als ClusterForge hem kent;
	// SameCluster: die node zit in hetzelfde cluster (dan is het bijna
	// altijd een vergeten regel).
	SrcNode     string `json:"src_node"`
	SameCluster bool   `json:"same_cluster"`
	// Software is wat de catalogus over de poort weet.
	Software string `json:"software"`
}

type Stats struct {
	Managed    int   `json:"managed"`
	Preview    int   `json:"preview"`
	Active     int   `json:"active"`
	Pending    int   `json:"pending"`
	Failed     int   `json:"failed"`
	OpenPorts  int   `json:"open_ports"`
	Blocked24h int64 `json:"blocked_24h"`
	Sources24h int   `json:"sources_24h"`
}

// Overview is de pagina Firewall.
type Overview struct {
	Protocol int           `json:"protocol"`
	Settings Settings      `json:"settings"`
	Stats    Stats         `json:"stats"`
	Hourly   []HourCount   `json:"hourly"`
	Clusters []ClusterView `json:"clusters"`
	Warnings []Warning     `json:"warnings"`
	Nodes    []Node        `json:"nodes"`
	Rules    []Rule        `json:"rules"`
	Catalog  []Software    `json:"catalog"`
	Classes  []string      `json:"classes"`
}

// Overview geeft alles voor de pagina.
func (s *Service) Overview(ctx context.Context) (Overview, error) {
	o := Overview{Protocol: protocol.FirewallSince, Catalog: Catalog, Classes: Classes, Warnings: []Warning{}, Clusters: []ClusterView{}}
	var err error
	if o.Settings, err = s.Settings(ctx); err != nil {
		return o, err
	}
	if o.Nodes, err = s.Nodes(ctx); err != nil {
		return o, err
	}
	if o.Nodes == nil {
		o.Nodes = []Node{}
	}
	if o.Rules, err = s.Rules(ctx); err != nil {
		return o, err
	}
	if o.Hourly, err = s.Hourly(ctx, nil, nil); err != nil {
		return o, err
	}
	if err := s.pool.QueryRow(ctx, `SELECT coalesce(sum(count), 0)::bigint, count(DISTINCT src) FROM firewall_blocked WHERE hour > now() - interval '24 hours'`).
		Scan(&o.Stats.Blocked24h, &o.Stats.Sources24h); err != nil {
		return o, err
	}
	perPort, err := s.blockedPerPort(ctx)
	if err != nil {
		return o, err
	}
	now := s.Now()
	clusters := map[string]*ClusterView{}
	var order []string
	for _, n := range o.Nodes {
		if !n.Enabled() {
			continue
		}
		switch n.Mode {
		case "managed":
			o.Stats.Managed++
		case "preview":
			o.Stats.Preview++
		}
		if n.Status != nil && n.Status.Active {
			o.Stats.Active++
		}
		if n.Mode == "managed" && n.Pending() {
			o.Stats.Pending++
		}
		if n.ApplyError != "" {
			o.Stats.Failed++
		}
		o.Warnings = append(o.Warnings, warnings(n, now)...)
		key := "~"
		if n.ClusterID != nil {
			key = n.ClusterID.String()
		}
		cv, ok := clusters[key]
		if !ok {
			cv = &ClusterView{ID: n.ClusterID, Name: n.ClusterName, Slug: n.ClusterSlug, Nodes: []NodeRef{}, Ports: []PortRow{}}
			if n.ClusterID == nil {
				cv.Name = "Zonder cluster"
			}
			clusters[key] = cv
			order = append(order, key)
		}
		cv.Nodes = append(cv.Nodes, NodeRef{ID: n.NodeID, Hostname: n.Hostname, Mode: n.Mode, Active: n.Status != nil && n.Status.Active})
		addCells(cv, n, perPort[n.NodeID])
		if n.Plan != nil {
			for _, e := range n.Plan.Entries {
				if e.State == "open" {
					o.Stats.OpenPorts++
				}
			}
		}
	}
	for _, k := range order {
		cv := clusters[k]
		sort.SliceStable(cv.Ports, func(i, j int) bool {
			pi, _ := strconv.Atoi(firstPort(cv.Ports[i].Port))
			pj, _ := strconv.Atoi(firstPort(cv.Ports[j].Port))
			if pi != pj {
				return pi < pj
			}
			return cv.Ports[i].Proto < cv.Ports[j].Proto
		})
		o.Clusters = append(o.Clusters, *cv)
	}
	return o, nil
}

func firstPort(p string) string {
	if p == "" {
		return "0"
	}
	lo, _, _ := strings.Cut(p, ":")
	return lo
}

// addCells zet het plan van een node in de tabel van zijn cluster, plus de
// dichte poorten waarop iemand klopte.
func addCells(cv *ClusterView, n Node, blocked map[string]int64) {
	row := func(proto, port string) *PortRow {
		for i := range cv.Ports {
			if cv.Ports[i].Proto == proto && cv.Ports[i].Port == port {
				return &cv.Ports[i]
			}
		}
		cv.Ports = append(cv.Ports, PortRow{Proto: proto, Port: port, Cells: map[string]Cell{}})
		return &cv.Ports[len(cv.Ports)-1]
	}
	id := n.NodeID.String()
	if n.Plan != nil {
		for _, e := range n.Plan.Entries {
			if e.State == "ignored" {
				continue
			}
			r := row(e.Proto, e.Port)
			if r.Software == "" || r.Software == "?" {
				r.Software, r.Label = e.Software, e.Label
			}
			c := r.Cells[id]
			if c.State == "" || c.State != "open" && e.State == "open" {
				c.State, c.Class = e.State, e.Class
			}
			if e.State == "open" {
				for _, f := range e.From {
					if !slices.Contains(c.From, f) {
						c.From = append(c.From, f)
					}
				}
				c.Applied = applied(n, e)
			}
			if c.From == nil {
				c.From = []string{}
			}
			r.Cells[id] = c
		}
	}
	for k, cnt := range blocked {
		proto, port, _ := strings.Cut(k, "/")
		r := row(proto, port)
		if r.Software == "" {
			if sw, ok := portSoftware(proto, port); ok {
				r.Software = sw
			}
		}
		c, ok := r.Cells[id]
		if !ok {
			c = Cell{State: "closed", From: []string{}}
		}
		c.Blocked24h = cnt
		r.Cells[id] = c
	}
}

// applied: een open poort heeft een regel op de node.
func applied(n Node, e Entry) bool {
	if n.Status == nil || !n.Status.Active {
		return false
	}
	for _, r := range n.Status.Rules {
		if r.Action == "allow" && r.Proto == e.Proto && r.Port == e.Port && (r.Direction == "" || r.Direction == "in") {
			return true
		}
	}
	return false
}

// portSoftware zoekt de software bij een poort in de catalogus.
func portSoftware(proto, port string) (string, bool) {
	for _, s := range Catalog {
		for _, p := range s.Ports {
			if p.Proto == proto && p.Port == port {
				return s.Name, true
			}
		}
	}
	return "", false
}

func warnings(n Node, now time.Time) []Warning {
	var out []Warning
	w := func(t string) { out = append(out, Warning{NodeID: n.NodeID, Hostname: n.Hostname, Text: t}) }
	switch {
	case !n.Supported:
		w("agent te oud (protocol 30 nodig; Nodes → Agents bijwerken)")
		return out
	case !n.Online:
		w("offline")
		return out
	}
	if n.StatusError != "" {
		w("stand ophalen mislukt: " + n.StatusError)
	}
	if n.ApplyError != "" {
		w("toepassen mislukt: " + n.ApplyError)
	}
	if st := n.Status; st != nil {
		if st.Pending != nil && now.After(st.Pending.Deadline.Add(time.Minute)) {
			w("een toepassing wacht nog op bevestiging")
		}
		if st.RolledBack != nil && (n.AppliedAt == nil || st.RolledBack.At.After(*n.AppliedAt)) && now.Sub(st.RolledBack.At) < 7*24*time.Hour {
			w("toepassing teruggedraaid: " + st.RolledBack.Reason)
		}
		if n.Mode == "managed" && st.Installed && !st.Active {
			w("ufw staat uit terwijl de node beheerd wordt")
		}
		if st.Active && st.Monitor == "kernel-log" {
			w("verkeer tellen via NFLOG lukt niet (" + st.MonitorError + "); laad de module nfnetlink_log op de Proxmox-host")
		}
	}
	if n.Plan != nil {
		for _, t := range n.Plan.Warnings {
			w(t)
		}
	}
	return out
}

func (s *Service) blockedPerPort(ctx context.Context) (map[uuid.UUID]map[string]int64, error) {
	rows, err := s.pool.Query(ctx, `SELECT node_id, proto, port, sum(count)::bigint FROM firewall_blocked WHERE hour > now() - interval '24 hours'
		GROUP BY node_id, proto, port`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[uuid.UUID]map[string]int64{}
	for rows.Next() {
		var id uuid.UUID
		var proto string
		var port int
		var n int64
		if err := rows.Scan(&id, &proto, &port, &n); err != nil {
			return nil, err
		}
		if out[id] == nil {
			out[id] = map[string]int64{}
		}
		p := strconv.Itoa(port)
		if port == 0 {
			p = ""
		}
		out[id][proto+"/"+p] = n
	}
	return out, rows.Err()
}

// Hourly geeft het geblokkeerde verkeer per uur van de laatste 24 uur.
func (s *Service) Hourly(ctx context.Context, nodeID, clusterID *uuid.UUID) ([]HourCount, error) {
	rows, err := s.pool.Query(ctx, `SELECT h, coalesce(sum(b.count), 0)::bigint
		FROM generate_series(date_trunc('hour', now()) - interval '23 hours', date_trunc('hour', now()), interval '1 hour') h
		LEFT JOIN firewall_blocked b ON b.hour = h AND ($1::uuid IS NULL OR b.node_id = $1)
			AND ($2::uuid IS NULL OR b.node_id IN (SELECT id FROM nodes WHERE cluster_id = $2))
		GROUP BY h ORDER BY h`, nodeID, clusterID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (HourCount, error) {
		var h HourCount
		err := r.Scan(&h.Hour, &h.Count)
		return h, err
	})
}

// TrafficQuery filtert het verkeer.
type TrafficQuery struct {
	NodeID    *uuid.UUID
	ClusterID *uuid.UUID
	Port      *int
	Hours     int
	Limit     int
}

// Traffic geeft het verkeer naar dichte poorten, meeste pakketten eerst.
func (s *Service) Traffic(ctx context.Context, q TrafficQuery) ([]TrafficRow, error) {
	if q.Hours <= 0 || q.Hours > 24*30 {
		q.Hours = 24
	}
	if q.Limit <= 0 || q.Limit > 1000 {
		q.Limit = 200
	}
	rows, err := s.pool.Query(ctx, `SELECT b.node_id, n.hostname, n.cluster_id, coalesce(c.name, ''), b.src, b.proto, b.port,
		sum(b.count)::bigint, min(b.first_seen), max(b.last_seen)
		FROM firewall_blocked b JOIN nodes n ON n.id = b.node_id LEFT JOIN clusters c ON c.id = n.cluster_id
		WHERE b.hour > now() - make_interval(hours => $1) AND ($2::uuid IS NULL OR b.node_id = $2) AND ($3::uuid IS NULL OR n.cluster_id = $3)
			AND ($4::int IS NULL OR b.port = $4)
		GROUP BY b.node_id, n.hostname, n.cluster_id, c.name, b.src, b.proto, b.port
		ORDER BY sum(b.count) DESC, max(b.last_seen) DESC LIMIT $5`, q.Hours, q.NodeID, q.ClusterID, q.Port, q.Limit)
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (TrafficRow, error) {
		var t TrafficRow
		err := r.Scan(&t.NodeID, &t.Hostname, &t.ClusterID, &t.ClusterName, &t.Src, &t.Proto, &t.Port, &t.Count, &t.First, &t.Last)
		return t, err
	})
	if err != nil {
		return nil, err
	}
	nodes, err := s.Nodes(ctx)
	if err != nil {
		return nil, err
	}
	type who struct {
		host    string
		cluster *uuid.UUID
	}
	byAddr := map[string]who{}
	for _, n := range nodes {
		for _, a := range n.addresses() {
			byAddr[a] = who{n.Hostname, n.ClusterID}
		}
	}
	for i := range out {
		t := &out[i]
		if w, ok := byAddr[t.Src]; ok {
			t.SrcNode = w.host
			t.SameCluster = w.cluster != nil && t.ClusterID != nil && *w.cluster == *t.ClusterID
		}
		if sw, ok := portSoftware(t.Proto, strconv.Itoa(t.Port)); ok {
			t.Software = sw
		}
	}
	if out == nil {
		out = []TrafficRow{}
	}
	return out, nil
}

// NodeDetail is één node met zijn verkeer.
type NodeDetail struct {
	Node    Node         `json:"node"`
	Hourly  []HourCount  `json:"hourly"`
	Traffic []TrafficRow `json:"traffic"`
}

// Detail geeft één node met het verkeer van de laatste 24 uur.
func (s *Service) Detail(ctx context.Context, id uuid.UUID) (NodeDetail, error) {
	n, err := s.Node(ctx, id)
	if err != nil {
		return NodeDetail{}, err
	}
	d := NodeDetail{Node: n}
	if d.Hourly, err = s.Hourly(ctx, &id, nil); err != nil {
		return d, err
	}
	d.Traffic, err = s.Traffic(ctx, TrafficQuery{NodeID: &id, Hours: 24, Limit: 50})
	return d, err
}
