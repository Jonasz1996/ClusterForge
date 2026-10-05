package httpapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/oapi-codegen/nullable"

	"github.com/Jonasz1996/clusterforge/internal/httpapi/gen"
	"github.com/Jonasz1996/clusterforge/internal/metrics"
)

func (s *Server) GetNodeMetrics(w http.ResponseWriter, r *http.Request, id uuid.UUID, params gen.GetNodeMetricsParams) {
	if _, err := s.q.GetNode(r.Context(), id); errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "not_found", "node niet gevonden")
		return
	} else if err != nil {
		s.internalError(w, r, err)
		return
	}
	s.writeMetrics(w, r, metrics.NodePanels, metrics.NodeSelector(id), params.Range)
}

func (s *Server) GetClusterMetrics(w http.ResponseWriter, r *http.Request, id uuid.UUID, params gen.GetClusterMetricsParams) {
	if _, err := s.q.GetCluster(r.Context(), id); errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "not_found", "cluster niet gevonden")
		return
	} else if err != nil {
		s.internalError(w, r, err)
		return
	}
	s.writeMetrics(w, r, metrics.ClusterPanels, metrics.ClusterSelector(id), params.Range)
}

func (s *Server) writeMetrics(w http.ResponseWriter, r *http.Request, panels []metrics.Panel, selector string, rng *gen.MetricsRange) {
	name := gen.MetricsRange("1h")
	if rng != nil {
		name = *rng
	}
	d, ok := metrics.Ranges[string(name)]
	if !ok {
		writeError(w, http.StatusBadRequest, "bad_request", "onbekende periode")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	res, err := s.metrics.Query(ctx, panels, selector, d, time.Now())
	switch {
	case errors.Is(err, metrics.ErrDisabled):
		writeError(w, http.StatusServiceUnavailable, "metrics_disabled", "er is geen VictoriaMetrics ingesteld (CF_VICTORIAMETRICS_URL)")
		return
	case err != nil:
		s.log.WarnContext(r.Context(), "metrics opvragen mislukt", "err", err)
		writeError(w, http.StatusBadGateway, "metrics_unavailable", "VictoriaMetrics geeft geen antwoord")
		return
	}
	out := gen.Metrics{
		Range: name, Start: res.Start, End: res.End, StepSeconds: int(res.Step.Seconds()),
		Timestamps: res.Timestamps, Panels: make([]gen.MetricPanel, len(res.Panels)),
	}
	for i, p := range res.Panels {
		gp := gen.MetricPanel{Id: p.ID, Title: p.Title, Unit: gen.MetricPanelUnit(p.Unit), Series: make([]gen.MetricSeries, len(p.Series))}
		for j, ser := range p.Series {
			vals := make([]nullable.Nullable[float64], len(ser.Values))
			for k, v := range ser.Values {
				vals[k] = nullableOf(v)
			}
			gp.Series[j] = gen.MetricSeries{Label: ser.Label, Values: vals}
		}
		out.Panels[i] = gp
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) Stream(w http.ResponseWriter, r *http.Request) {
	s.hub.ServeHTTP(w, r)
}

func (s *Server) GetInfo(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, gen.ServerInfo{
		Version: s.version, MetricsEnabled: s.metrics.Enabled(),
		GrafanaNodeUrl: s.cfg.GrafanaNodeURL, GrafanaClusterUrl: s.cfg.GrafanaClusterURL,
	})
}
