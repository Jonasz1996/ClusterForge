package metrics

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"golang.org/x/sync/errgroup"
)

// ErrDisabled betekent dat er geen VictoriaMetrics is ingesteld.
var ErrDisabled = errors.New("metrics staan uit")

// Ranges zijn de periodes die de webinterface kan tonen.
var Ranges = map[string]time.Duration{
	"1h": time.Hour, "6h": 6 * time.Hour, "24h": 24 * time.Hour, "7d": 7 * 24 * time.Hour,
}

// points is ongeveer het aantal punten per lijn.
const points = 240

type Unit string

const (
	UnitPercent        Unit = "percent"
	UnitLoad           Unit = "load"
	UnitBytesPerSecond Unit = "bytes_per_second"
	UnitCelsius        Unit = "celsius"
)

// Panel is één grafiek. De queries zijn vast; alleen het id van de node of
// het cluster wordt ingevuld, zodat de browser geen eigen PromQL stuurt.
type Panel struct {
	ID      string
	Title   string
	Unit    Unit
	Queries []PanelQuery
}

type PanelQuery struct {
	// Expr bevat $sel voor de labelselector en $w voor het rate-venster.
	Expr string
	// Legend is de naam van de lijn; {label} wordt vervangen door de waarde
	// van dat label.
	Legend string
}

var NodePanels = []Panel{
	{ID: "cpu", Title: "CPU", Unit: UnitPercent, Queries: []PanelQuery{
		{`100 * (1 - avg(rate(node_cpu_seconds_total{$sel,mode="idle"}[$w])))`, "gebruik"},
		{`100 * avg(rate(node_cpu_seconds_total{$sel,mode="iowait"}[$w]))`, "iowait"},
	}},
	{ID: "load", Title: "Load", Unit: UnitLoad, Queries: []PanelQuery{
		{`node_load1{$sel}`, "1 min"}, {`node_load5{$sel}`, "5 min"}, {`node_load15{$sel}`, "15 min"},
	}},
	{ID: "memory", Title: "Geheugen", Unit: UnitPercent, Queries: []PanelQuery{
		{`100 * (1 - node_memory_MemAvailable_bytes{$sel} / node_memory_MemTotal_bytes{$sel})`, "RAM"},
		{`100 * (1 - node_memory_SwapFree_bytes{$sel} / (node_memory_SwapTotal_bytes{$sel} > 0))`, "swap"},
	}},
	{ID: "disk", Title: "Schijfbezetting", Unit: UnitPercent, Queries: []PanelQuery{
		{`100 * (node_filesystem_size_bytes{$sel} - node_filesystem_free_bytes{$sel}) / (node_filesystem_size_bytes{$sel} - node_filesystem_free_bytes{$sel} + node_filesystem_avail_bytes{$sel})`, "{mountpoint}"},
	}},
	{ID: "diskio", Title: "Schijf-I/O", Unit: UnitBytesPerSecond, Queries: []PanelQuery{
		{`rate(node_disk_read_bytes_total{$sel}[$w])`, "{device} lezen"},
		{`rate(node_disk_written_bytes_total{$sel}[$w])`, "{device} schrijven"},
	}},
	{ID: "network", Title: "Netwerk", Unit: UnitBytesPerSecond, Queries: []PanelQuery{
		{`rate(node_network_receive_bytes_total{$sel}[$w])`, "{device} in"},
		{`rate(node_network_transmit_bytes_total{$sel}[$w])`, "{device} uit"},
	}},
	{ID: "temperature", Title: "Temperatuur", Unit: UnitCelsius, Queries: []PanelQuery{
		{`node_hwmon_temp_celsius{$sel}`, "{chip} {sensor}"},
	}},
}

var ClusterPanels = []Panel{
	{ID: "cpu", Title: "CPU per node", Unit: UnitPercent, Queries: []PanelQuery{
		{`100 * (1 - avg by (node) (rate(node_cpu_seconds_total{$sel,mode="idle"}[$w])))`, "{node}"},
	}},
	{ID: "memory", Title: "Geheugen per node", Unit: UnitPercent, Queries: []PanelQuery{
		{`100 * (1 - node_memory_MemAvailable_bytes{$sel} / node_memory_MemTotal_bytes{$sel})`, "{node}"},
	}},
	{ID: "load", Title: "Load (1 min) per node", Unit: UnitLoad, Queries: []PanelQuery{
		{`node_load1{$sel}`, "{node}"},
	}},
	{ID: "network", Title: "Netwerkverkeer per node (in + uit)", Unit: UnitBytesPerSecond, Queries: []PanelQuery{
		{`sum by (node) (rate(node_network_receive_bytes_total{$sel}[$w]) + rate(node_network_transmit_bytes_total{$sel}[$w]))`, "{node}"},
	}},
}

// Result is het antwoord voor de webinterface: per paneel lijnen op een
// gedeelde tijdas. Ontbrekende punten zijn nil.
type Result struct {
	Start, End time.Time
	Step       time.Duration
	Timestamps []int64
	Panels     []PanelResult
}

type PanelResult struct {
	ID, Title string
	Unit      Unit
	Series    []Series
}

type Series struct {
	Label  string
	Values []*float64
}

// Client leest uit VictoriaMetrics.
type Client struct {
	url    string
	client *http.Client
}

func NewClient(vmURL string) *Client {
	return &Client{url: strings.TrimRight(vmURL, "/"), client: &http.Client{Timeout: 20 * time.Second}}
}

func (c *Client) Enabled() bool { return c.url != "" }

// NodeSelector en ClusterSelector maken de labelselector voor een id; een
// uuid bevat geen tekens die in PromQL iets betekenen.
func NodeSelector(id uuid.UUID) string    { return `node_id="` + id.String() + `"` }
func ClusterSelector(id uuid.UUID) string { return `cluster_id="` + id.String() + `"` }

// Query voert de panelen uit voor selector over de periode rng.
func (c *Client) Query(ctx context.Context, panels []Panel, selector string, rng time.Duration, now time.Time) (Result, error) {
	if !c.Enabled() {
		return Result{}, ErrDisabled
	}
	step := stepFor(rng)
	end := now.Truncate(step)
	start := end.Add(-rng)
	window := max(2*step, time.Minute)
	res := Result{Start: start, End: end, Step: step}
	n := int(rng/step) + 1
	res.Timestamps = make([]int64, n)
	for i := range n {
		res.Timestamps[i] = start.Add(time.Duration(i) * step).Unix()
	}
	// series[paneel][query] vullen de goroutines; elk een eigen plek.
	series := make([][][]Series, len(panels))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(6)
	for pi, p := range panels {
		series[pi] = make([][]Series, len(p.Queries))
		for qi, pq := range p.Queries {
			expr := strings.NewReplacer("$sel", selector, "$w", promDuration(window)).Replace(pq.Expr)
			g.Go(func() error {
				raw, err := c.queryRange(gctx, expr, start, end, step)
				if err != nil {
					return err
				}
				series[pi][qi] = align(raw, pq.Legend, start, step, n)
				return nil
			})
		}
	}
	if err := g.Wait(); err != nil {
		return Result{}, err
	}
	res.Panels = make([]PanelResult, len(panels))
	for pi, p := range panels {
		res.Panels[pi] = PanelResult{ID: p.ID, Title: p.Title, Unit: p.Unit, Series: []Series{}}
		for _, s := range series[pi] {
			res.Panels[pi].Series = append(res.Panels[pi].Series, s...)
		}
	}
	return res, nil
}

// stepFor kiest de stap zo dat er ongeveer points punten zijn, als veelvoud
// van 15 s (het interval van de agent).
func stepFor(rng time.Duration) time.Duration {
	base := 15 * time.Second
	steps := (rng/points + base - 1) / base
	return max(steps, 1) * base
}

func promDuration(d time.Duration) string { return strconv.Itoa(int(d.Seconds())) + "s" }

type rawSeries struct {
	Metric map[string]string `json:"metric"`
	Values [][2]any          `json:"values"`
}

func (c *Client) queryRange(ctx context.Context, expr string, start, end time.Time, step time.Duration) ([]rawSeries, error) {
	v := url.Values{}
	v.Set("query", expr)
	v.Set("start", strconv.FormatInt(start.Unix(), 10))
	v.Set("end", strconv.FormatInt(end.Unix(), 10))
	v.Set("step", promDuration(step))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url+"/api/v1/query_range?"+v.Encode(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, err
	}
	var out struct {
		Status string `json:"status"`
		Error  string `json:"error"`
		Data   struct {
			Result []rawSeries `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("VictoriaMetrics: status %d", resp.StatusCode)
	}
	if out.Status != "success" {
		return nil, fmt.Errorf("VictoriaMetrics: %s", out.Error)
	}
	return out.Data.Result, nil
}

var legendLabel = regexp.MustCompile(`\{([a-zA-Z_][a-zA-Z0-9_]*)\}`)

// align zet de punten op de gedeelde tijdas en sorteert de lijnen op naam.
func align(raw []rawSeries, legend string, start time.Time, step time.Duration, n int) []Series {
	out := make([]Series, 0, len(raw))
	for _, r := range raw {
		s := Series{
			Label: strings.TrimSpace(legendLabel.ReplaceAllStringFunc(legend, func(m string) string {
				return r.Metric[m[1:len(m)-1]]
			})),
			Values: make([]*float64, n),
		}
		for _, p := range r.Values {
			ts, ok := p[0].(float64)
			str, ok2 := p[1].(string)
			if !ok || !ok2 {
				continue
			}
			v, err := strconv.ParseFloat(str, 64)
			if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
				continue
			}
			i := int(math.Round((ts - float64(start.Unix())) / step.Seconds()))
			if i >= 0 && i < n {
				s.Values[i] = &v
			}
		}
		out = append(out, s)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Label < out[j].Label })
	return out
}
