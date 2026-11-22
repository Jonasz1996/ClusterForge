package zabbixmon

import (
	"cmp"
	"context"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// --- kerncijfers per host (M3) --------------------------------------------------------

// metricKeys zijn de items van het template Linux by Zabbix agent die de
// dashboards per host tonen.
var metricKeys = map[string]string{
	"system.cpu.util":                "cpu",
	"system.cpu.util[,,avg1]":        "cpu",
	"vm.memory.utilization":          "memory",
	"vfs.fs.dependent.size[/,pused]": "disk",
	"vfs.fs.size[/,pused]":           "disk",
	"system.cpu.load[all,avg1]":      "load",
	"system.uptime":                  "uptime",
}

// HostMetrics zijn de laatste kerncijfers van één host, met de items voor
// een grafiek.
type HostMetrics struct {
	HostID string            `json:"hostid"`
	CPU    *float64          `json:"cpu"`
	Memory *float64          `json:"memory"`
	Disk   *float64          `json:"disk"`
	Load   *float64          `json:"load"`
	Uptime *float64          `json:"uptime"`
	Items  map[string]string `json:"items"`
}

// Metrics zijn de kerncijfers van alle hosts.
type Metrics struct {
	At    time.Time     `json:"at"`
	Hosts []HostMetrics `json:"hosts"`
}

type dashCache struct {
	mu      sync.Mutex
	metrics cached[Metrics]
	events  map[string]cached[Events]
}

// Metrics haalt de kerncijfers van elke host in één aanvraag.
func (s *Service) Metrics(ctx context.Context) (Metrics, error) {
	now := s.Now()
	s.dash.mu.Lock()
	if c := s.dash.metrics; !c.at.IsZero() && now.Sub(c.at) < cacheFor {
		s.dash.mu.Unlock()
		return c.v, nil
	}
	s.dash.mu.Unlock()
	api, _, err := s.Client(ctx)
	if err != nil {
		return Metrics{}, err
	}
	keys := make([]string, 0, len(metricKeys))
	for k := range metricKeys {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	var raw []rawHostItem
	if err := api.Call(ctx, "item.get", map[string]any{
		"output":    []string{"itemid", "hostid", "key_", "value_type", "lastvalue", "lastclock", "state"},
		"filter":    map[string]any{"key_": keys},
		"monitored": true,
		"templated": false,
		"limit":     20000,
	}, &raw); err != nil {
		return Metrics{}, err
	}
	out := Metrics{At: now.UTC(), Hosts: []HostMetrics{}}
	by := map[string]*HostMetrics{}
	var order []string
	for _, r := range raw {
		name := metricKeys[r.Key]
		if name == "" {
			continue
		}
		h := by[r.HostID]
		if h == nil {
			h = &HostMetrics{HostID: r.HostID, Items: map[string]string{}}
			by[r.HostID] = h
			order = append(order, r.HostID)
		}
		h.Items[name] = r.ItemID
		if r.State != 0 || r.LastClock <= 0 || now.Sub(time.Unix(int64(r.LastClock), 0)) > time.Hour {
			continue
		}
		v, err := strconv.ParseFloat(strings.TrimSpace(r.LastValue), 64)
		if err != nil {
			continue
		}
		switch name {
		case "cpu":
			h.CPU = &v
		case "memory":
			h.Memory = &v
		case "disk":
			h.Disk = &v
		case "load":
			h.Load = &v
		case "uptime":
			h.Uptime = &v
		}
	}
	for _, id := range order {
		out.Hosts = append(out.Hosts, *by[id])
	}
	s.dash.mu.Lock()
	s.dash.metrics = cached[Metrics]{now, out}
	s.dash.mu.Unlock()
	return out, nil
}

// --- problemen over tijd (M3) ------------------------------------------------------------

// Events vat de problemen van een venster samen uit event.get.
type Events struct {
	Window string `json:"window"`
	// Buckets: per uur (24h) of per dag (7d, 30d) het aantal nieuwe
	// problemen per ernst (index = ernst 0-5).
	Buckets []EventBucket `json:"buckets"`
	Total   int           `json:"total"`
	// Resolved: opgelost binnen het venster; MTTR het gemiddelde in seconden.
	Resolved int      `json:"resolved"`
	MTTR     *float64 `json:"mttr"`
	// Heatmap: [weekdag 0=maandag][uur] aantal nieuwe problemen.
	Heatmap     [7][24]int `json:"heatmap"`
	TopTriggers []TopCount `json:"top_triggers"`
	TopHosts    []TopCount `json:"top_hosts"`
	Truncated   bool       `json:"truncated"`
}

// EventBucket is één stukje tijd.
type EventBucket struct {
	At       time.Time `json:"at"`
	Severity [6]int    `json:"severity"`
}

// TopCount is een trigger of host met hoe vaak.
type TopCount struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Count    int       `json:"count"`
	Severity int       `json:"severity"`
	Node     *NodeLink `json:"node"`
}

// maxEvents: zoveel events per venster hoogstens.
const maxEvents = 10000

type rawEvent struct {
	EventID  string `json:"eventid"`
	ObjectID string `json:"objectid"`
	Clock    zint   `json:"clock"`
	Name     string `json:"name"`
	Severity zint   `json:"severity"`
	REventID string `json:"r_eventid"`
	Hosts    []struct {
		HostID string `json:"hostid"`
		Name   string `json:"name"`
	} `json:"hosts"`
}

// Events haalt de problemen van het venster en telt ze.
func (s *Service) Events(ctx context.Context, window string) (Events, error) {
	d, ok := Windows[window]
	if !ok {
		window, d = "24h", Windows["24h"]
	}
	now := s.Now()
	s.dash.mu.Lock()
	if c, ok := s.dash.events[window]; ok && now.Sub(c.at) < time.Minute {
		s.dash.mu.Unlock()
		return c.v, nil
	}
	s.dash.mu.Unlock()
	api, _, err := s.Client(ctx)
	if err != nil {
		return Events{}, err
	}
	from := now.Add(-d)
	var evs []rawEvent
	if err := api.Call(ctx, "event.get", map[string]any{
		"output":      []string{"eventid", "objectid", "clock", "name", "severity", "r_eventid"},
		"selectHosts": []string{"hostid", "name"},
		"source":      0,
		"object":      0,
		"value":       1,
		"time_from":   from.Unix(),
		"time_till":   now.Unix(),
		"sortfield":   []string{"clock", "eventid"},
		"sortorder":   "DESC",
		"limit":       maxEvents,
	}, &evs); err != nil {
		return Events{}, err
	}
	// Hersteltijden: de klok van de herstel-events.
	var rids []string
	for _, e := range evs {
		if e.REventID != "" && e.REventID != "0" {
			rids = append(rids, e.REventID)
		}
	}
	rclock := map[string]int64{}
	for chunk := range slices.Chunk(rids, 2000) {
		var rs []struct {
			EventID string `json:"eventid"`
			Clock   zint   `json:"clock"`
		}
		if err := api.Call(ctx, "event.get", map[string]any{"eventids": chunk, "output": []string{"eventid", "clock"}, "source": 0, "object": 0}, &rs); err != nil {
			return Events{}, err
		}
		for _, r := range rs {
			rclock[r.EventID] = int64(r.Clock)
		}
	}
	nodeOf := map[string]*NodeLink{}
	s.mu.RLock()
	if s.snap != nil {
		for _, h := range s.snap.Hosts {
			nodeOf[h.HostID] = h.Node
		}
	}
	s.mu.RUnlock()
	out := summarize(evs, rclock, nodeOf, window, from, now)
	s.dash.mu.Lock()
	if s.dash.events == nil {
		s.dash.events = map[string]cached[Events]{}
	}
	s.dash.events[window] = cached[Events]{now, out}
	s.dash.mu.Unlock()
	return out, nil
}

func summarize(evs []rawEvent, rclock map[string]int64, nodeOf map[string]*NodeLink, window string, from, now time.Time) Events {
	out := Events{Window: window, TopTriggers: []TopCount{}, TopHosts: []TopCount{}, Truncated: len(evs) >= maxEvents}
	step := time.Hour
	if now.Sub(from) > 2*24*time.Hour {
		step = 24 * time.Hour
	}
	loc := brussels()
	// Stukjes op hele uren of dagen in Brussel, zodat "vandaag" klopt.
	start := from.In(loc).Truncate(time.Hour)
	if step > time.Hour {
		y, m, d := from.In(loc).Date()
		start = time.Date(y, m, d, 0, 0, 0, 0, loc)
	}
	idx := map[int64]int{}
	for t := start; !t.After(now); t = next(t, step) {
		idx[t.Unix()] = len(out.Buckets)
		out.Buckets = append(out.Buckets, EventBucket{At: t.UTC()})
	}
	trig := map[string]*TopCount{}
	host := map[string]*TopCount{}
	var ttr float64
	for _, e := range evs {
		t := time.Unix(int64(e.Clock), 0).In(loc)
		sev := min(max(int(e.Severity), 0), 5)
		out.Total++
		b := t.Truncate(time.Hour)
		if step > time.Hour {
			y, m, d := t.Date()
			b = time.Date(y, m, d, 0, 0, 0, 0, loc)
		}
		if i, ok := idx[b.Unix()]; ok {
			out.Buckets[i].Severity[sev]++
		}
		out.Heatmap[(int(t.Weekday())+6)%7][t.Hour()]++
		if rc, ok := rclock[e.REventID]; ok && rc >= int64(e.Clock) {
			out.Resolved++
			ttr += float64(rc - int64(e.Clock))
		}
		tc := trig[e.ObjectID]
		if tc == nil {
			tc = &TopCount{ID: e.ObjectID, Name: e.Name, Severity: sev}
			trig[e.ObjectID] = tc
		}
		tc.Count++
		tc.Severity = max(tc.Severity, sev)
		for _, h := range e.Hosts {
			hc := host[h.HostID]
			if hc == nil {
				hc = &TopCount{ID: h.HostID, Name: h.Name, Node: nodeOf[h.HostID]}
				host[h.HostID] = hc
			}
			hc.Count++
			hc.Severity = max(hc.Severity, sev)
		}
	}
	if out.Resolved > 0 {
		m := ttr / float64(out.Resolved)
		out.MTTR = &m
	}
	out.TopTriggers = top10(trig)
	out.TopHosts = top10(host)
	return out
}

func next(t time.Time, step time.Duration) time.Time {
	if step >= 24*time.Hour {
		return t.AddDate(0, 0, 1)
	}
	return t.Add(step)
}

func top10(m map[string]*TopCount) []TopCount {
	out := make([]TopCount, 0, len(m))
	for _, v := range m {
		out = append(out, *v)
	}
	slices.SortFunc(out, func(a, b TopCount) int {
		return cmp.Or(cmp.Compare(b.Count, a.Count), cmp.Compare(b.Severity, a.Severity), strings.Compare(a.Name, b.Name))
	})
	if len(out) > 10 {
		out = out[:10]
	}
	return out
}
