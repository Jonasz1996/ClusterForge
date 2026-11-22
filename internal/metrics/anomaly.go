package metrics

import (
	"context"
	"time"
)

// Recent is het kwartier voor een storing dat de afwijkingen vergelijken
// met de twee uur daarvoor.
const Recent = 15 * time.Minute

// Baseline is de periode voor Recent waarmee vergeleken wordt.
const Baseline = 2 * time.Hour

// Anomaly is een afwijking in de metrics van een node vlak voor een storing.
type Anomaly struct {
	// Metric is cpu, memory, disk of network_errors.
	Metric string
	// Label is het mountpoint bij disk.
	Label string
	// Before is het gemiddelde in de twee uur ervoor, After het hoogste punt
	// in het kwartier ervoor.
	Before, After float64
	// At is het eerste punt in het kwartier boven de drempel.
	At time.Time
}

// anomalyPanels zijn de vaste reeksen waarin afwijkingen gezocht worden.
var anomalyPanels = []Panel{
	{ID: "cpu", Queries: []PanelQuery{{`100 * (1 - avg(rate(node_cpu_seconds_total{$sel,mode="idle"}[$w])))`, ""}}},
	{ID: "memory", Queries: []PanelQuery{{`100 * (1 - node_memory_MemAvailable_bytes{$sel} / node_memory_MemTotal_bytes{$sel})`, ""}}},
	{ID: "disk", Queries: []PanelQuery{{diskExpr, "{mountpoint}"}}},
	{ID: "network_errors", Queries: []PanelQuery{{`sum(rate(node_network_receive_errs_total{$sel}[$w]) + rate(node_network_transmit_errs_total{$sel}[$w]))`, ""}}},
}

// Anomalies vergelijkt het kwartier voor at met de twee uur daarvoor, met
// vaste drempels. Zonder VictoriaMetrics geeft het ErrDisabled.
func (c *Client) Anomalies(ctx context.Context, selector string, at time.Time) ([]Anomaly, error) {
	step := time.Minute
	end := at.Truncate(step)
	start := end.Add(-Baseline - Recent)
	res, err := c.query(ctx, anomalyPanels, selector, start, end, step)
	if err != nil {
		return nil, err
	}
	split := end.Add(-Recent).Unix()
	var out []Anomaly
	for _, p := range res.Panels {
		for _, s := range p.Series {
			if a := Detect(p.ID, s.Label, res.Timestamps, s.Values, split); a != nil {
				out = append(out, *a)
			}
		}
	}
	return out, nil
}

// Detect zoekt een afwijking in één reeks: de punten voor split zijn de
// basis, die erna het kwartier voor de storing. Elk deel heeft minstens drie
// punten nodig.
func Detect(metric, label string, ts []int64, values []*float64, split int64) *Anomaly {
	var before []float64
	type point struct {
		t int64
		v float64
	}
	var after []point
	for i, v := range values {
		if v == nil || i >= len(ts) {
			continue
		}
		if ts[i] < split {
			before = append(before, *v)
		} else {
			after = append(after, point{ts[i], *v})
		}
	}
	if len(before) < 3 || len(after) < 3 {
		return nil
	}
	var sum float64
	for _, v := range before {
		sum += v
	}
	mean := sum / float64(len(before))
	var peak float64
	for _, p := range after {
		peak = max(peak, p.v)
	}
	// threshold is het punt vanaf waar het telt; ok zegt of er genoeg
	// verschil is met de basis.
	var threshold float64
	var ok bool
	switch metric {
	case "cpu":
		threshold, ok = 90, peak >= 90 && peak-mean >= 30
	case "memory":
		threshold, ok = 95, peak >= 95 && mean < 90
	case "disk":
		threshold, ok = 95, peak >= 95 && mean < 95
	case "network_errors":
		threshold, ok = 1, peak >= 1 && mean < 0.1
	}
	if !ok {
		return nil
	}
	a := &Anomaly{Metric: metric, Label: label, Before: mean, After: peak}
	for _, p := range after {
		if p.v >= threshold {
			a.At = time.Unix(p.t, 0).UTC()
			break
		}
	}
	return a
}
