package metrics

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"math"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"time"

	"github.com/google/uuid"
	"golang.org/x/sync/errgroup"
)

// NodeRisk zijn de vaste risicoqueries van één node over zeven dagen.
type NodeRisk struct {
	CPUP95    *float64
	MemoryP95 *float64
	SwapAvg   *float64
	// NetErrors is het aantal netwerkfouten in zeven dagen.
	NetErrors *float64
	// DiskDaysLeft is per mountpoint over hoeveel dagen het vol is volgens
	// de lineaire trend van zeven dagen; alleen voor bestandssystemen die
	// voller worden en waarvan minstens een dag metingen is.
	DiskDaysLeft map[string]float64
}

// riskQueries zijn vast; de browser of de assistent stuurt nooit zelf
// PromQL. Elke query geeft één waarde per node (of per node en mountpoint).
var riskQueries = map[string]string{
	"cpu":    `quantile_over_time(0.95, (100 * (1 - avg by (node_id) (rate(node_cpu_seconds_total{mode="idle"}[5m]))))[7d:5m])`,
	"memory": `max by (node_id) (quantile_over_time(0.95, (100 * (1 - node_memory_MemAvailable_bytes / node_memory_MemTotal_bytes))[7d:5m]))`,
	"swap":   `max by (node_id) (avg_over_time((100 * (1 - node_memory_SwapFree_bytes / (node_memory_SwapTotal_bytes > 0)))[7d:5m]))`,
	"net_errors": `sum by (node_id) (increase(node_network_receive_errs_total[7d])) + ` +
		`sum by (node_id) (increase(node_network_transmit_errs_total[7d]))`,
	// Dagen tot vol: wat er nog vrij is, gedeeld door wat er per dag
	// afgaat. Alleen met minstens een dag metingen (5760 punten van 15 s).
	"disk_days": `(node_filesystem_avail_bytes / -(deriv(node_filesystem_avail_bytes[7d]) < 0) / 86400)` +
		` and count_over_time(node_filesystem_avail_bytes[7d]) > 5760`,
}

type instant struct {
	Metric map[string]string `json:"metric"`
	Value  [2]any            `json:"value"`
}

// Risk voert de risicoqueries uit voor alle nodes. Zonder VictoriaMetrics
// geeft het ErrDisabled.
func (c *Client) Risk(ctx context.Context, now time.Time) (map[uuid.UUID]*NodeRisk, error) {
	if !c.Enabled() {
		return nil, ErrDisabled
	}
	names := slices.Sorted(maps.Keys(riskQueries))
	// Elke goroutine schrijft een eigen plek.
	results := make([][]instant, len(names))
	g, gctx := errgroup.WithContext(ctx)
	for i, name := range names {
		g.Go(func() error {
			res, err := c.queryInstant(gctx, riskQueries[name], now)
			if err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
			results[i] = res
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	out := map[uuid.UUID]*NodeRisk{}
	node := func(m map[string]string) *NodeRisk {
		id, err := uuid.Parse(m["node_id"])
		if err != nil {
			return nil
		}
		if out[id] == nil {
			out[id] = &NodeRisk{}
		}
		return out[id]
	}
	for i, name := range names {
		for _, s := range results[i] {
			v, ok := sampleValue(s.Value)
			n := node(s.Metric)
			if !ok || n == nil {
				continue
			}
			switch name {
			case "cpu":
				n.CPUP95 = &v
			case "memory":
				n.MemoryP95 = &v
			case "swap":
				n.SwapAvg = &v
			case "net_errors":
				n.NetErrors = &v
			case "disk_days":
				if n.DiskDaysLeft == nil {
					n.DiskDaysLeft = map[string]float64{}
				}
				n.DiskDaysLeft[s.Metric["mountpoint"]] = v
			}
		}
	}
	return out, nil
}

func sampleValue(v [2]any) (float64, bool) {
	str, ok := v[1].(string)
	if !ok {
		return 0, false
	}
	f, err := strconv.ParseFloat(str, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, false
	}
	return f, true
}

func (c *Client) queryInstant(ctx context.Context, expr string, at time.Time) ([]instant, error) {
	v := url.Values{}
	v.Set("query", expr)
	v.Set("time", strconv.FormatInt(at.Unix(), 10))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url+"/api/v1/query?"+v.Encode(), nil)
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
			Result []instant `json:"result"`
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
