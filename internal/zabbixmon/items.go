package zabbixmon

import (
	"cmp"
	"context"
	"errors"
	"math"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Item is een item van een host met zijn laatste waarde (latest data).
type Item struct {
	ItemID    string     `json:"itemid"`
	Name      string     `json:"name"`
	Key       string     `json:"key"`
	Numeric   bool       `json:"numeric"`
	Units     string     `json:"units"`
	LastValue string     `json:"last_value"`
	PrevValue string     `json:"prev_value"`
	LastClock *time.Time `json:"last_clock"`
	// Component: de tag component (Zabbix 6.0+ groepeert items zo); leeg zonder.
	Component string `json:"component"`
	Error     string `json:"error"`
}

// HostItems is de latest data van één host.
type HostItems struct {
	HostID string `json:"hostid"`
	Items  []Item `json:"items"`
	// Truncated: er zijn meer items dan we tonen.
	Truncated bool `json:"truncated"`
}

// maxItems: zoveel items per host halen we hoogstens op.
const maxItems = 1500

type rawHostItem struct {
	ItemID    string   `json:"itemid"`
	HostID    string   `json:"hostid"`
	Name      string   `json:"name"`
	Key       string   `json:"key_"`
	ValueType zint     `json:"value_type"`
	Units     string   `json:"units"`
	LastValue string   `json:"lastvalue"`
	PrevValue string   `json:"prevvalue"`
	LastClock zint     `json:"lastclock"`
	State     zint     `json:"state"`
	Error     string   `json:"error"`
	Tags      []rawTag `json:"tags"`
}

// numeric: float (0) of unsigned (3).
func numeric(vt zint) bool { return vt == 0 || vt == 3 }

type cached[T any] struct {
	at time.Time
	v  T
}

// itemCache houdt antwoorden 30 s bij, zodat een pagina die ververst Zabbix
// niet steeds opnieuw vraagt.
type itemCache struct {
	mu    sync.Mutex
	items map[string]cached[HostItems]
	ser   map[string]cached[Series]
	lat   map[string]cached[[]LatestItem]
}

const cacheFor = 30 * time.Second

func (c *itemCache) getItems(k string, now time.Time) (HostItems, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.items[k]
	if !ok || now.Sub(v.at) > cacheFor {
		return HostItems{}, false
	}
	return v.v, true
}

func (c *itemCache) putItems(k string, v HostItems, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.items == nil || len(c.items) > 500 {
		c.items = map[string]cached[HostItems]{}
	}
	c.items[k] = cached[HostItems]{now, v}
}

func (c *itemCache) getLatest(k string, now time.Time) ([]LatestItem, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.lat[k]
	if !ok || now.Sub(v.at) > cacheFor {
		return nil, false
	}
	return v.v, true
}

func (c *itemCache) putLatest(k string, v []LatestItem, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.lat == nil || len(c.lat) > 200 {
		c.lat = map[string]cached[[]LatestItem]{}
	}
	c.lat[k] = cached[[]LatestItem]{now, v}
}

func (c *itemCache) getSeries(k string, now time.Time) (Series, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.ser[k]
	if !ok || now.Sub(v.at) > cacheFor {
		return Series{}, false
	}
	return v.v, true
}

func (c *itemCache) putSeries(k string, v Series, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ser == nil || len(c.ser) > 500 {
		c.ser = map[string]cached[Series]{}
	}
	c.ser[k] = cached[Series]{now, v}
}

// known: de host staat in de laatste ronde. Zo vraagt de browser alleen naar
// hosts die ClusterForge al kent.
func (s *Service) knownHost(hostID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.snap == nil {
		return false
	}
	return slices.ContainsFunc(s.snap.Hosts, func(h Host) bool { return h.HostID == hostID })
}

// Items geeft de items van een host met hun laatste waarde.
func (s *Service) Items(ctx context.Context, hostID string) (HostItems, error) {
	if !s.knownHost(hostID) {
		return HostItems{}, ErrNotFound
	}
	now := s.Now()
	if v, ok := s.cache.getItems(hostID, now); ok {
		return v, nil
	}
	api, _, err := s.Client(ctx)
	if err != nil {
		return HostItems{}, err
	}
	var raw []rawHostItem
	if err := api.Call(ctx, "item.get", map[string]any{
		"hostids":    []string{hostID},
		"output":     []string{"itemid", "hostid", "name", "key_", "value_type", "units", "lastvalue", "prevvalue", "lastclock", "state", "error"},
		"selectTags": []string{"tag", "value"},
		"monitored":  true,
		"webitems":   true,
		"sortfield":  "name",
		"limit":      maxItems + 1,
	}, &raw); err != nil {
		return HostItems{}, err
	}
	out := HostItems{HostID: hostID, Items: make([]Item, 0, len(raw))}
	if len(raw) > maxItems {
		raw, out.Truncated = raw[:maxItems], true
	}
	for _, r := range raw {
		it := Item{
			ItemID: r.ItemID, Name: r.Name, Key: r.Key, Numeric: numeric(r.ValueType), Units: r.Units,
			LastValue: r.LastValue, PrevValue: r.PrevValue, LastClock: r.LastClock.time(),
		}
		if r.State == 1 {
			it.Error = cmp.Or(r.Error, "item wordt niet ondersteund")
		}
		for _, t := range r.Tags {
			if t.Tag == "component" {
				it.Component = t.Value
				break
			}
		}
		if len(it.LastValue) > 500 {
			it.LastValue = it.LastValue[:500] + "…"
		}
		out.Items = append(out.Items, it)
	}
	s.cache.putItems(hostID, out, now)
	return out, nil
}

// maxLatest: zoveel items vraagt één dashboard hoogstens tegelijk.
const maxLatest = 200

// LatestItem is de laatste waarde van een los item, voor een paneel.
type LatestItem struct {
	Item
	HostID   string `json:"hostid"`
	HostName string `json:"host_name"`
}

// Latest geeft de laatste waarde van losse items (voor getal- en
// meterpanelen). Items van hosts die ClusterForge niet kent, of die weg zijn,
// ontbreken gewoon in het antwoord.
func (s *Service) Latest(ctx context.Context, itemIDs []string) ([]LatestItem, error) {
	ids := make([]string, 0, len(itemIDs))
	seen := map[string]bool{}
	for _, id := range itemIDs {
		id = strings.TrimSpace(id)
		if _, err := strconv.ParseUint(id, 10, 64); err != nil || seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	if len(ids) > maxLatest {
		return nil, &FieldError{"te veel items tegelijk"}
	}
	out := []LatestItem{}
	if len(ids) == 0 {
		return out, nil
	}
	slices.Sort(ids)
	now := s.Now()
	ck := strings.Join(ids, ",")
	if v, ok := s.cache.getLatest(ck, now); ok {
		return v, nil
	}
	api, _, err := s.Client(ctx)
	if err != nil {
		return nil, err
	}
	var raw []struct {
		rawHostItem
		Hosts []struct {
			HostID string `json:"hostid"`
			Name   string `json:"name"`
		} `json:"hosts"`
	}
	if err := api.Call(ctx, "item.get", map[string]any{
		"itemids":     ids,
		"output":      []string{"itemid", "hostid", "name", "key_", "value_type", "units", "lastvalue", "prevvalue", "lastclock", "state", "error"},
		"selectHosts": []string{"hostid", "name"},
		"webitems":    true,
	}, &raw); err != nil {
		return nil, err
	}
	s.mu.RLock()
	known := map[string]bool{}
	if s.snap != nil {
		for _, h := range s.snap.Hosts {
			known[h.HostID] = true
		}
	}
	s.mu.RUnlock()
	for _, r := range raw {
		if !known[r.HostID] {
			continue
		}
		it := LatestItem{Item: Item{
			ItemID: r.ItemID, Name: r.Name, Key: r.Key, Numeric: numeric(r.ValueType), Units: r.Units,
			LastValue: r.LastValue, PrevValue: r.PrevValue, LastClock: r.LastClock.time(),
		}, HostID: r.HostID}
		if len(r.Hosts) > 0 {
			it.HostName = r.Hosts[0].Name
		}
		if r.State == 1 {
			it.Error = cmp.Or(r.Error, "item wordt niet ondersteund")
		}
		if len(it.LastValue) > 500 {
			it.LastValue = it.LastValue[:500] + "…"
		}
		out = append(out, it)
	}
	s.cache.putLatest(ck, out, now)
	return out, nil
}

// Ranges zijn de vensters van een grafiek.
var Ranges = map[string]time.Duration{
	"1h": time.Hour, "6h": 6 * time.Hour, "24h": 24 * time.Hour, "7d": 7 * 24 * time.Hour, "30d": 30 * 24 * time.Hour,
}

// SeriesPoints: ongeveer zoveel punten per lijn.
const SeriesPoints = 240

// Series is een grafieklijn van één item: per tijdstip het gemiddelde, en het
// minimum en maximum van dat stukje (uit history of uit trends).
type Series struct {
	ItemID     string     `json:"itemid"`
	HostID     string     `json:"hostid"`
	Name       string     `json:"name"`
	Key        string     `json:"key"`
	Units      string     `json:"units"`
	Range      string     `json:"range"`
	Source     string     `json:"source"`
	Timestamps []int64    `json:"timestamps"`
	Avg        []*float64 `json:"avg"`
	Min        []*float64 `json:"min"`
	Max        []*float64 `json:"max"`
}

// ErrNotNumeric: van tekst of logs maken we geen grafiek.
var ErrNotNumeric = errors.New("dit item heeft geen getallen; daar maken we geen grafiek van")

// ItemSeries haalt de grafiek van een item. Tot 24 uur uit history, langer uit
// trends (één waarde per uur met min, gemiddelde en max).
func (s *Service) ItemSeries(ctx context.Context, itemID, rng string) (Series, error) {
	d, ok := Ranges[rng]
	if !ok {
		rng, d = "24h", Ranges["24h"]
	}
	if _, err := strconv.ParseUint(itemID, 10, 64); err != nil {
		return Series{}, ErrNotFound
	}
	now := s.Now()
	ck := itemID + "|" + rng
	if v, ok := s.cache.getSeries(ck, now); ok {
		return v, nil
	}
	api, _, err := s.Client(ctx)
	if err != nil {
		return Series{}, err
	}
	var items []rawHostItem
	if err := api.Call(ctx, "item.get", map[string]any{
		"itemids": []string{itemID},
		"output":  []string{"itemid", "hostid", "name", "key_", "value_type", "units"},
	}, &items); err != nil {
		return Series{}, err
	}
	if len(items) == 0 || !s.knownHost(items[0].HostID) {
		return Series{}, ErrNotFound
	}
	it := items[0]
	if !numeric(it.ValueType) {
		return Series{}, ErrNotNumeric
	}
	end := now.Unix()
	step := max(int64(d.Seconds())/SeriesPoints, 1)
	// De stukjes beginnen op een veelvoud van step, zodat lijnen van
	// verschillende items in één grafiek op dezelfde tijdstippen liggen.
	start := now.Add(-d).Unix() / step * step
	out := Series{ItemID: it.ItemID, HostID: it.HostID, Name: it.Name, Key: it.Key, Units: it.Units, Range: rng}
	b := newBuckets(start, end, step)
	if d > 24*time.Hour {
		out.Source = "trends"
		var tr []struct {
			Clock zint   `json:"clock"`
			Min   string `json:"value_min"`
			Avg   string `json:"value_avg"`
			Max   string `json:"value_max"`
		}
		if err := api.Call(ctx, "trend.get", map[string]any{
			"itemids": []string{itemID}, "time_from": start, "time_till": end,
			"output": []string{"clock", "value_min", "value_avg", "value_max"}, "limit": 24 * 32,
		}, &tr); err != nil {
			return Series{}, err
		}
		for _, t := range tr {
			lo, e1 := strconv.ParseFloat(t.Min, 64)
			av, e2 := strconv.ParseFloat(t.Avg, 64)
			hi, e3 := strconv.ParseFloat(t.Max, 64)
			if e1 == nil && e2 == nil && e3 == nil {
				b.add(int64(t.Clock), lo, av, hi)
			}
		}
	} else {
		out.Source = "history"
		var hs []struct {
			Clock zint   `json:"clock"`
			Value string `json:"value"`
		}
		if err := api.Call(ctx, "history.get", map[string]any{
			"itemids": []string{itemID}, "history": int(it.ValueType), "time_from": start, "time_till": end,
			"output": []string{"clock", "value"}, "sortfield": "clock", "sortorder": "ASC", "limit": 50000,
		}, &hs); err != nil {
			return Series{}, err
		}
		for _, h := range hs {
			if v, err := strconv.ParseFloat(strings.TrimSpace(h.Value), 64); err == nil {
				b.add(int64(h.Clock), v, v, v)
			}
		}
	}
	out.Timestamps, out.Min, out.Avg, out.Max = b.result()
	s.cache.putSeries(ck, out, now)
	return out, nil
}

// buckets dunt punten uit tot vaste stukjes tijd.
type buckets struct {
	start, step int64
	n           int
	cnt         []int
	sum, lo, hi []float64
}

func newBuckets(start, end, step int64) *buckets {
	n := max(int((end-start+step-1)/step), 1)
	return &buckets{start: start, step: step, n: n, cnt: make([]int, n), sum: make([]float64, n), lo: make([]float64, n), hi: make([]float64, n)}
}

func (b *buckets) add(t int64, lo, avg, hi float64) {
	i := int((t - b.start) / b.step)
	if i == b.n && t-b.start == int64(b.n)*b.step {
		i = b.n - 1 // precies het eindtijdstip
	}
	if i < 0 || i >= b.n || math.IsNaN(avg) || math.IsInf(avg, 0) {
		return
	}
	if b.cnt[i] == 0 {
		b.lo[i], b.hi[i] = lo, hi
	} else {
		b.lo[i], b.hi[i] = min(b.lo[i], lo), max(b.hi[i], hi)
	}
	b.cnt[i]++
	b.sum[i] += avg
}

// result geeft de tijdstippen (midden van elk stukje) met lege plekken als
// null, zodat de grafiek gaten toont waar Zabbix niets had.
func (b *buckets) result() ([]int64, []*float64, []*float64, []*float64) {
	ts := make([]int64, b.n)
	lo, avg, hi := make([]*float64, b.n), make([]*float64, b.n), make([]*float64, b.n)
	for i := range b.n {
		ts[i] = b.start + int64(i)*b.step + b.step/2
		if b.cnt[i] == 0 {
			continue
		}
		a, l, h := b.sum[i]/float64(b.cnt[i]), b.lo[i], b.hi[i]
		avg[i], lo[i], hi[i] = &a, &l, &h
	}
	return ts, lo, avg, hi
}
