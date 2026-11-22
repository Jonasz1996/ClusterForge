package crowdsec

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// Stats zijn de cijfers bovenaan de pagina.
type Stats struct {
	Managed      int `json:"managed"`
	Running      int `json:"running"`
	Alerts24h    int `json:"alerts_24h"`
	ActiveBans   int `json:"active_bans"`
	Blocklist    int `json:"blocklist"`
	Bouncers     int `json:"bouncers"`
	BouncersDown int `json:"bouncers_down"`
}

type HourCount struct {
	Hour  time.Time `json:"hour"`
	Count int       `json:"count"`
}

type TopEntry struct {
	Key     string `json:"key"`
	Country string `json:"country"`
	AS      string `json:"as"`
	Count   int    `json:"count"`
}

// Warning is een probleem op de pagina.
type Warning struct {
	NodeID   uuid.UUID `json:"node_id"`
	Hostname string    `json:"hostname"`
	Text     string    `json:"text"`
}

// CatalogEntry is een profiel voor de tab Profielen.
type CatalogEntry struct {
	Key         string   `json:"key"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Collections []string `json:"collections"`
	Community   bool     `json:"community"`
	Nodes       []string `json:"nodes"`
}

// Overview is alles voor de pagina CrowdSec, behalve alerts en bans.
type Overview struct {
	Protocol     int            `json:"protocol"`
	Settings     Settings       `json:"settings"`
	Stats        Stats          `json:"stats"`
	Hourly       []HourCount    `json:"hourly"`
	TopScenarios []TopEntry     `json:"top_scenarios"`
	TopIPs       []TopEntry     `json:"top_ips"`
	Warnings     []Warning      `json:"warnings"`
	Nodes        []Node         `json:"nodes"`
	Catalog      []CatalogEntry `json:"catalog"`
}

func (s *Service) Overview(ctx context.Context) (Overview, error) {
	set, err := s.Settings(ctx)
	if err != nil {
		return Overview{}, err
	}
	nodes, err := s.Nodes(ctx)
	if err != nil {
		return Overview{}, err
	}
	if nodes == nil {
		nodes = []Node{}
	}
	o := Overview{Protocol: protocol.CrowdSecSince, Settings: set, Nodes: nodes, Warnings: []Warning{}}
	now := s.Now()
	onNode := map[string][]string{}
	for _, n := range nodes {
		if !n.Enabled {
			continue
		}
		warn := func(f string, a ...any) {
			o.Warnings = append(o.Warnings, Warning{NodeID: n.NodeID, Hostname: n.Hostname, Text: fmt.Sprintf(f, a...)})
		}
		if n.Managed {
			o.Stats.Managed++
		}
		if n.Plan != nil {
			for _, p := range n.Plan.Profiles {
				if p.Origin != "off" {
					onNode[p.Key] = append(onNode[p.Key], n.Hostname)
				}
			}
		}
		if n.Blocklist > o.Stats.Blocklist {
			o.Stats.Blocklist = n.Blocklist
		}
		switch {
		case !n.Supported:
			warn("agent te oud voor CrowdSec (protocol %d nodig)", protocol.CrowdSecSince)
			continue
		case !n.Managed:
			continue
		case !n.Online:
			warn("offline")
			continue
		case n.ApplyError != "":
			warn("toepassen mislukt: %s", n.ApplyError)
		}
		st := n.Status
		if st == nil {
			continue
		}
		if !st.Installed {
			if !n.ApplyRunning {
				warn("CrowdSec staat er nog niet op")
			}
			continue
		}
		if st.Active == "active" {
			o.Stats.Running++
		} else {
			warn("CrowdSec draait niet (%s)", st.Active)
		}
		if n.Bouncer {
			o.Stats.Bouncers++
			if st.Bouncer != "active" {
				o.Stats.BouncersDown++
				warn("bouncer draait niet (%s): bans worden niet tegengehouden", firstNonEmpty(st.Bouncer, "niet geïnstalleerd"))
			}
		}
		for _, src := range st.Sources {
			if src.Read == 0 && n.AppliedAt != nil && now.Sub(*n.AppliedAt) > time.Hour {
				warn("bron %s leest niets", src.Source)
			} else if src.Read > 100 && src.Parsed == 0 {
				warn("bron %s: geen enkele regel herkend (verkeerd type?)", src.Source)
			}
		}
		if n.PollError != "" {
			warn("ophalen: %s", n.PollError)
		}
		if !st.CAPI {
			warn("niet aangemeld bij de Central API: geen community-blocklist")
		}
	}
	for _, p := range Catalog {
		e := CatalogEntry{Key: p.Key, Name: p.Name, Description: p.Description, Collections: p.Collections, Community: p.Community, Nodes: onNode[p.Key]}
		if e.Nodes == nil {
			e.Nodes = []string{}
		}
		o.Catalog = append(o.Catalog, e)
	}
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM crowdsec_alerts WHERE started_at > $1`, now.Add(-24*time.Hour)).Scan(&o.Stats.Alerts24h); err != nil {
		return Overview{}, err
	}
	if err := s.pool.QueryRow(ctx, `SELECT count(DISTINCT value) FROM crowdsec_decisions WHERE until > $1`, now).Scan(&o.Stats.ActiveBans); err != nil {
		return Overview{}, err
	}
	start := now.Truncate(time.Hour).Add(-23 * time.Hour)
	counts := map[int64]int{}
	rows, err := s.pool.Query(ctx, `SELECT date_trunc('hour', started_at), count(*) FROM crowdsec_alerts WHERE started_at >= $1 GROUP BY 1`, start)
	if err != nil {
		return Overview{}, err
	}
	for rows.Next() {
		var h time.Time
		var c int
		if err := rows.Scan(&h, &c); err != nil {
			rows.Close()
			return Overview{}, err
		}
		counts[h.Unix()] = c
	}
	rows.Close()
	for i := range 24 {
		h := start.Add(time.Duration(i) * time.Hour)
		o.Hourly = append(o.Hourly, HourCount{Hour: h, Count: counts[h.Unix()]})
	}
	week := now.Add(-7 * 24 * time.Hour)
	if o.TopScenarios, err = s.top(ctx, `SELECT scenario, '', '', count(*) FROM crowdsec_alerts WHERE started_at > $1
		GROUP BY scenario ORDER BY count(*) DESC, scenario LIMIT 10`, week); err != nil {
		return Overview{}, err
	}
	if o.TopIPs, err = s.top(ctx, `SELECT value, max(country), max(as_name), count(*) FROM crowdsec_alerts WHERE started_at > $1
		GROUP BY value ORDER BY count(*) DESC, value LIMIT 10`, week); err != nil {
		return Overview{}, err
	}
	return o, nil
}

func firstNonEmpty(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}

func (s *Service) top(ctx context.Context, sql string, args ...any) ([]TopEntry, error) {
	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TopEntry{}
	for rows.Next() {
		var e TopEntry
		if err := rows.Scan(&e.Key, &e.Country, &e.AS, &e.Count); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// AlertFilter filtert de alerts.
type AlertFilter struct {
	NodeID    *uuid.UUID
	ClusterID *uuid.UUID
	Scenario  string
	Value     string
	Limit     int
}

type AlertRow struct {
	ID          int64      `json:"id"`
	NodeID      uuid.UUID  `json:"node_id"`
	Hostname    string     `json:"hostname"`
	ClusterID   *uuid.UUID `json:"cluster_id"`
	ClusterName string     `json:"cluster_name"`
	Scenario    string     `json:"scenario"`
	Message     string     `json:"message"`
	Scope       string     `json:"scope"`
	Value       string     `json:"value"`
	Country     string     `json:"country"`
	AS          string     `json:"as"`
	Events      int        `json:"events"`
	Decisions   int        `json:"decisions"`
	Simulated   bool       `json:"simulated"`
	StartedAt   time.Time  `json:"started_at"`
	StoppedAt   time.Time  `json:"stopped_at"`
}

func (s *Service) Alerts(ctx context.Context, f AlertFilter) ([]AlertRow, error) {
	if f.Limit <= 0 || f.Limit > 1000 {
		f.Limit = 200
	}
	var where []string
	var args []any
	add := func(cond string, v any) {
		args = append(args, v)
		where = append(where, strings.ReplaceAll(cond, "?", fmt.Sprintf("$%d", len(args))))
	}
	if f.NodeID != nil {
		add("a.node_id = ?", *f.NodeID)
	}
	if f.ClusterID != nil {
		add("n.cluster_id = ?", *f.ClusterID)
	}
	if f.Scenario != "" {
		add("a.scenario = ?", f.Scenario)
	}
	if f.Value != "" {
		add("a.value LIKE ? || '%'", f.Value)
	}
	sql := `SELECT a.id, a.node_id, n.hostname, n.cluster_id, coalesce(c.name, ''), a.scenario, a.message, a.scope, a.value,
		a.country, a.as_name, a.events, a.decisions, a.simulated, a.started_at, a.stopped_at
		FROM crowdsec_alerts a JOIN nodes n ON n.id = a.node_id LEFT JOIN clusters c ON c.id = n.cluster_id`
	if len(where) > 0 {
		sql += " WHERE " + strings.Join(where, " AND ")
	}
	args = append(args, f.Limit)
	sql += fmt.Sprintf(" ORDER BY a.started_at DESC, a.id DESC LIMIT $%d", len(args))
	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AlertRow{}
	for rows.Next() {
		var r AlertRow
		if err := rows.Scan(&r.ID, &r.NodeID, &r.Hostname, &r.ClusterID, &r.ClusterName, &r.Scenario, &r.Message, &r.Scope, &r.Value,
			&r.Country, &r.AS, &r.Events, &r.Decisions, &r.Simulated, &r.StartedAt, &r.StoppedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// BanRow is een geband adres, samengevoegd over de nodes.
type BanRow struct {
	Scope    string    `json:"scope"`
	Value    string    `json:"value"`
	Type     string    `json:"type"`
	Scenario string    `json:"scenario"`
	Origin   string    `json:"origin"`
	Country  string    `json:"country"`
	AS       string    `json:"as"`
	Until    time.Time `json:"until"`
	Nodes    []string  `json:"nodes"`
	// Shared: ClusterForge zette deze ban op andere nodes.
	Shared bool `json:"shared"`
}

func (s *Service) Bans(ctx context.Context) ([]BanRow, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT d.scope, d.value, min(d.type), max(d.scenario) FILTER (WHERE d.origin <> 'cscli' OR d.scenario NOT LIKE 'cf:%'),
		       max(d.scenario), string_agg(DISTINCT d.origin, ', '), max(d.country), max(d.as_name), max(d.until),
		       array_agg(DISTINCT n.hostname ORDER BY n.hostname),
		       EXISTS (SELECT 1 FROM crowdsec_shared sh WHERE sh.value = d.value AND sh.until > now())
		FROM crowdsec_decisions d JOIN nodes n ON n.id = d.node_id
		WHERE d.until > now()
		GROUP BY d.scope, d.value
		ORDER BY max(d.until) DESC, d.value
		LIMIT 2000`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []BanRow{}
	for rows.Next() {
		var r BanRow
		var own *string
		var any string
		if err := rows.Scan(&r.Scope, &r.Value, &r.Type, &own, &any, &r.Origin, &r.Country, &r.AS, &r.Until, &r.Nodes, &r.Shared); err != nil {
			return nil, err
		}
		r.Scenario = any
		if own != nil {
			r.Scenario = *own
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// NodeAlerts zijn de laatste alerts van een node, voor de kaart.
func (s *Service) NodeAlerts(ctx context.Context, id uuid.UUID, limit int) ([]AlertRow, error) {
	return s.Alerts(ctx, AlertFilter{NodeID: &id, Limit: limit})
}
