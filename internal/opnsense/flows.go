package opnsense

// Verkeer uit het filterlog (fase 40 M1): elke minuut haalt de server bij
// elke firewall op wat pf sinds de vorige keer doorliet of blokkeerde, per
// uur samengevat, en bewaart dat 30 dagen. De GUI toont het met de namen van
// de nodes, VIP's en netten in plaats van IP's.

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// FlowsKeep is hoe lang de stromen bewaard blijven.
const FlowsKeep = 30 * 24 * time.Hour

// FlowsLoop leest elke minuut het filterlog van elke firewall.
func (s *Service) FlowsLoop(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	prune := time.Time{}
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		s.collectFlows(ctx)
		if time.Since(prune) > time.Hour {
			prune = time.Now()
			if _, err := s.pool.Exec(ctx, `DELETE FROM opnsense_flows WHERE hour < $1`, s.Now().Add(-FlowsKeep)); err != nil {
				s.log.Warn("oude OPNsense-stromen opruimen mislukt", "err", err)
			}
		}
	}
}

func (s *Service) collectFlows(ctx context.Context) {
	fws, err := s.Firewalls(ctx)
	if err != nil {
		s.log.Warn("OPNsense-stromen: firewalls lezen mislukt", "err", err)
		return
	}
	for _, f := range fws {
		if !f.Online || f.Protocol < protocol.OPNFlowsSince {
			continue
		}
		if err := s.CollectFlows(ctx, f.NodeID); err != nil {
			s.log.Debug("OPNsense-filterlog lezen mislukt", "firewall", f.Hostname, "err", err)
		}
	}
}

// CollectFlows leest het filterlog van één firewall en telt het op.
func (s *Service) CollectFlows(ctx context.Context, id uuid.UUID) error {
	var lines, skipped, dropped int
	var readErr error
	for range 5 {
		data, err := s.request(ctx, id, protocol.PVERequest{Op: protocol.OPNOpFilterLog}, false)
		if err != nil {
			readErr = err
			break
		}
		var res protocol.FilterLogResult
		if err := json.Unmarshal(data, &res); err != nil {
			readErr = err
			break
		}
		if err := s.storeFlows(ctx, id, res.Flows); err != nil {
			return err
		}
		lines, skipped, dropped = lines+res.Lines, skipped+res.Skipped, dropped+res.Dropped
		if !res.More {
			break
		}
	}
	msg := ""
	if readErr != nil {
		msg = readErr.Error()
	}
	_, err := s.pool.Exec(ctx, `INSERT INTO opnsense_flow_reads (node_id, read_at, lines, skipped, dropped, error)
		VALUES ($1, now(), $2, $3, $4, $5)
		ON CONFLICT (node_id) DO UPDATE SET read_at = now(), lines = opnsense_flow_reads.lines + $2,
			skipped = opnsense_flow_reads.skipped + $3, dropped = opnsense_flow_reads.dropped + $4, error = $5`,
		id, lines, skipped, dropped, msg)
	if err != nil {
		return err
	}
	return readErr
}

func (s *Service) storeFlows(ctx context.Context, id uuid.UUID, flows []protocol.FilterFlow) error {
	if len(flows) == 0 {
		return nil
	}
	n := len(flows)
	hours := make([]time.Time, n)
	actions, ifaces, dirs, protos, srcs, dsts, rules := make([]string, n), make([]string, n), make([]string, n), make([]string, n), make([]string, n), make([]string, n), make([]string, n)
	ports, counts := make([]int32, n), make([]int64, n)
	for i, f := range flows {
		hours[i], actions[i], ifaces[i], dirs[i], protos[i] = f.Hour, f.Action, f.Interface, f.Dir, f.Proto
		srcs[i], dsts[i], ports[i], rules[i], counts[i] = f.Src, f.Dst, int32(f.Port), f.Rule, int64(f.Count)
	}
	_, err := s.pool.Exec(ctx, `INSERT INTO opnsense_flows (node_id, hour, action, iface, dir, proto, src, dst, port, rule, count)
		SELECT $1, * FROM unnest($2::timestamptz[], $3::text[], $4::text[], $5::text[], $6::text[], $7::text[], $8::text[], $9::int[], $10::text[], $11::bigint[])
		ON CONFLICT (node_id, hour, action, iface, dir, proto, src, dst, port, rule)
		DO UPDATE SET count = opnsense_flows.count + excluded.count`,
		id, hours, actions, ifaces, dirs, protos, srcs, dsts, ports, rules, counts)
	return err
}

// FlowRow is één stroom over het venster, over alle firewalls van het
// cluster samen.
type FlowRow struct {
	Action    string    `json:"action"`
	Interface string    `json:"interface"`
	IfaceName string    `json:"iface_name,omitempty"`
	Dir       string    `json:"dir"`
	Proto     string    `json:"proto"`
	Src       string    `json:"src"`
	SrcName   string    `json:"src_name,omitempty"`
	Dst       string    `json:"dst"`
	DstName   string    `json:"dst_name,omitempty"`
	Port      int       `json:"port,omitempty"`
	Rule      string    `json:"rule,omitempty"`
	RuleName  string    `json:"rule_name,omitempty"`
	Count     int64     `json:"count"`
	First     time.Time `json:"first"`
	Last      time.Time `json:"last"`
}

// RuleHits is hoe vaak een regel raakte.
type RuleHits struct {
	Rule     string    `json:"rule"`
	RuleName string    `json:"rule_name,omitempty"`
	Action   string    `json:"action"`
	Count    int64     `json:"count"`
	Last     time.Time `json:"last"`
}

// FlowRead is de stand van het lezen op één firewall.
type FlowRead struct {
	NodeID   uuid.UUID  `json:"node_id"`
	Hostname string     `json:"hostname"`
	ReadAt   *time.Time `json:"read_at,omitempty"`
	Error    string     `json:"error,omitempty"`
	// Why: waarom er niet gelezen wordt (agent te oud).
	Why string `json:"why,omitempty"`
}

// FlowView is wat de tab Verkeer toont.
type FlowView struct {
	Window string     `json:"window"`
	Rows   []FlowRow  `json:"rows"`
	Rules  []RuleHits `json:"rules"`
	Reads  []FlowRead `json:"reads"`
	// Blocked en Passed zijn de totalen over het venster.
	Blocked int64 `json:"blocked"`
	Passed  int64 `json:"passed"`
}

// FlowQuery filtert de stromen.
type FlowQuery struct {
	// Window is 1h, 24h of 7d (standaard 24h), of 30d.
	Window string
	// Action is block, pass of leeg.
	Action string
	// Q zoekt in adressen, namen en poort.
	Q string
}

func windowOf(w string) (time.Duration, string) {
	switch w {
	case "1h":
		return time.Hour, w
	case "7d":
		return 7 * 24 * time.Hour, w
	case "30d":
		return 30 * 24 * time.Hour, w
	}
	return 24 * time.Hour, "24h"
}

// Flows geeft de stromen van het cluster van firewall id.
func (s *Service) Flows(ctx context.Context, id uuid.UUID, q FlowQuery) (FlowView, error) {
	c, err := s.ClusterOf(ctx, id)
	if err != nil {
		return FlowView{}, err
	}
	d, w := windowOf(q.Window)
	v := FlowView{Window: w, Rows: []FlowRow{}, Rules: []RuleHits{}, Reads: []FlowRead{}}
	var ids []uuid.UUID
	for _, n := range c.Nodes {
		ids = append(ids, n.NodeID)
		r := FlowRead{NodeID: n.NodeID, Hostname: n.Hostname}
		if n.Protocol < protocol.OPNFlowsSince {
			r.Why = fmt.Sprintf("agent te oud (protocol %d, %d nodig): Nodes → Agents bijwerken", n.Protocol, protocol.OPNFlowsSince)
		}
		var at time.Time
		if err := s.pool.QueryRow(ctx, `SELECT read_at, error FROM opnsense_flow_reads WHERE node_id = $1`, n.NodeID).Scan(&at, &r.Error); err == nil {
			r.ReadAt = &at
		}
		v.Reads = append(v.Reads, r)
	}
	since := s.Now().Add(-d)
	rows, err := s.pool.Query(ctx, `SELECT action, iface, dir, proto, src, dst, port, rule, sum(count), min(hour), max(hour)
		FROM opnsense_flows WHERE node_id = ANY($1) AND hour >= $2 AND ($3 = '' OR action = $3)
		GROUP BY action, iface, dir, proto, src, dst, port, rule
		ORDER BY sum(count) DESC LIMIT 2000`, ids, since.Truncate(time.Hour), q.Action)
	if err != nil {
		return FlowView{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var r FlowRow
		if err := rows.Scan(&r.Action, &r.Interface, &r.Dir, &r.Proto, &r.Src, &r.Dst, &r.Port, &r.Rule, &r.Count, &r.First, &r.Last); err != nil {
			return FlowView{}, err
		}
		r.Last = r.Last.Add(time.Hour)
		v.Rows = append(v.Rows, r)
	}
	if err := rows.Err(); err != nil {
		return FlowView{}, err
	}
	names := s.names(ctx, c)
	rules := s.ruleNames(ctx, c)
	hits := map[string]*RuleHits{}
	var out []FlowRow
	needle := strings.ToLower(strings.TrimSpace(q.Q))
	for _, r := range v.Rows {
		r.SrcName, r.DstName = names.name(r.Src), names.name(r.Dst)
		r.IfaceName = names.iface[r.Interface]
		r.RuleName = rules[r.Rule]
		if r.Action == "block" || r.Action == "reject" {
			v.Blocked += r.Count
		} else {
			v.Passed += r.Count
		}
		if r.Rule != "" {
			h := hits[r.Rule+"|"+r.Action]
			if h == nil {
				h = &RuleHits{Rule: r.Rule, RuleName: r.RuleName, Action: r.Action}
				hits[r.Rule+"|"+r.Action] = h
			}
			h.Count += r.Count
			if r.Last.After(h.Last) {
				h.Last = r.Last
			}
		}
		if needle != "" && !strings.Contains(strings.ToLower(strings.Join([]string{r.Src, r.SrcName, r.Dst, r.DstName, fmt.Sprint(r.Port), r.IfaceName, r.RuleName}, " ")), needle) {
			continue
		}
		if len(out) < 300 {
			out = append(out, r)
		}
	}
	v.Rows = out
	if v.Rows == nil {
		v.Rows = []FlowRow{}
	}
	for _, h := range hits {
		v.Rules = append(v.Rules, *h)
	}
	sort.Slice(v.Rules, func(i, j int) bool { return v.Rules[i].Count > v.Rules[j].Count })
	return v, nil
}

// nameBook zet adressen en interfaces om in namen.
type nameBook struct {
	ip    map[string]string
	nets  []namedNet
	iface map[string]string
}

type namedNet struct {
	net  *net.IPNet
	name string
}

func (b nameBook) name(addr string) string {
	if n, ok := b.ip[addr]; ok {
		return n
	}
	ip := net.ParseIP(addr)
	if ip == nil {
		return ""
	}
	best, bits := "", -1
	for _, n := range b.nets {
		if ones, _ := n.net.Mask.Size(); n.net.Contains(ip) && ones > bits {
			best, bits = n.name, ones
		}
	}
	return best
}

// names kent de nodes met een agent, de VIP's van ClusterForge, de CARP-VIP's
// en interfaces van de firewalls en de netten erachter.
func (s *Service) names(ctx context.Context, c Cluster) nameBook {
	b := nameBook{ip: map[string]string{}, iface: map[string]string{}}
	rows, err := s.pool.Query(ctx, `SELECT n.hostname, coalesce(host(n.primary_ip), ''), coalesce(st.addresses, '{}')
		FROM nodes n LEFT JOIN node_status st ON st.node_id = n.id`)
	if err == nil {
		for rows.Next() {
			var host, primary string
			var addrs []string
			if rows.Scan(&host, &primary, &addrs) != nil {
				continue
			}
			for _, a := range append([]string{primary}, addrs...) {
				a, _, _ = strings.Cut(a, "/")
				if a != "" {
					if _, taken := b.ip[a]; !taken {
						b.ip[a] = host
					}
				}
			}
		}
		rows.Close()
	}
	rows, err = s.pool.Query(ctx, `SELECT host(v.address), c.name FROM vips v JOIN clusters c ON c.id = v.cluster_id`)
	if err == nil {
		for rows.Next() {
			var a, name string
			if rows.Scan(&a, &name) == nil {
				b.ip[a] = "VIP " + name
			}
		}
		rows.Close()
	}
	for _, n := range c.Nodes {
		if n.Facts == nil {
			continue
		}
		for _, v := range n.Facts.VIPs {
			a, _, _ := strings.Cut(v.Address, "/")
			label := v.Descr
			if label == "" {
				label = v.Interface
			}
			b.ip[a] = "CARP-VIP " + label
		}
		for _, i := range n.Facts.Interfaces {
			label := i.Descr
			if label == "" {
				label = i.Name
			}
			if i.Device != "" {
				b.iface[i.Device] = label
			}
			for _, a := range i.Addresses {
				ip, ipnet, err := net.ParseCIDR(a)
				if err != nil {
					continue
				}
				b.ip[ip.String()] = n.Hostname + " (" + label + ")"
				if !slices.ContainsFunc(b.nets, func(x namedNet) bool { return x.net.String() == ipnet.String() }) {
					b.nets = append(b.nets, namedNet{net: ipnet, name: label + "-net"})
				}
			}
		}
	}
	return b
}

// ruleNames haalt de omschrijving van elke regel bij zijn label, als de
// firewall dat kan geven.
func (s *Service) ruleNames(ctx context.Context, c Cluster) map[string]string {
	out := map[string]string{}
	var id uuid.UUID
	for _, n := range c.Nodes {
		if n.Why == "" {
			id = n.NodeID
			break
		}
	}
	if id == uuid.Nil {
		return out
	}
	data, err := s.request(ctx, id, protocol.PVERequest{Op: protocol.OPNOpGet, Path: "diagnostics/firewall/list_rule_ids"}, true)
	if err != nil {
		return out
	}
	return parseRuleIDs(data)
}

// parseRuleIDs leest het antwoord van list_rule_ids: een object label →
// omschrijving, of {"items": [{"id"|"rid", "description"|"descr"}]}.
func parseRuleIDs(data json.RawMessage) map[string]string {
	out := map[string]string{}
	var flat map[string]json.RawMessage
	if json.Unmarshal(data, &flat) != nil {
		return out
	}
	if items, ok := flat["items"]; ok {
		var list []map[string]any
		if json.Unmarshal(items, &list) == nil {
			for _, it := range list {
				id, _ := it["id"].(string)
				if id == "" {
					id, _ = it["rid"].(string)
				}
				d, _ := it["description"].(string)
				if d == "" {
					d, _ = it["descr"].(string)
				}
				if id != "" && d != "" {
					out[id] = d
				}
			}
			return out
		}
	}
	for k, v := range flat {
		var d string
		if json.Unmarshal(v, &d) == nil && d != "" {
			out[k] = d
		}
	}
	return out
}
