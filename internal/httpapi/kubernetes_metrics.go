package httpapi

import (
	"net/http"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/httpapi/gen"
	"github.com/Jonasz1996/clusterforge/internal/kube"
	"github.com/Jonasz1996/clusterforge/internal/metrics"
)

// GetKubeMetrics geeft CPU en geheugen uit metrics-server: per namespace
// voor het cluster, of per pod voor een app of één pod. De namen gaan pas
// in de query als ze geldige Kubernetes-namen zijn.
func (s *Server) GetKubeMetrics(w http.ResponseWriter, r *http.Request, clusterID uuid.UUID, params gen.GetKubeMetricsParams) {
	if _, err := s.kube.Cluster(r.Context(), clusterID); err != nil {
		s.kubeError(w, r, err)
		return
	}
	val := func(p *string) string {
		if p == nil {
			return ""
		}
		return *p
	}
	ns, kind, name, pod := val(params.Namespace), val(params.Kind), val(params.Name), val(params.Pod)
	for _, v := range []string{ns, name, pod} {
		if v != "" && !kube.ValidName(v) {
			writeError(w, http.StatusBadRequest, "bad_request", "ongeldige naam: "+v)
			return
		}
	}
	switch {
	case (kind == "") != (name == ""):
		writeError(w, http.StatusBadRequest, "bad_request", "geef kind en name allebei, of geen van beide")
		return
	case kind != "" && !kube.IsApp(kind):
		writeError(w, http.StatusBadRequest, "bad_request", "kind moet Deployment, StatefulSet of DaemonSet zijn")
		return
	case (name != "" || pod != "") && ns == "":
		writeError(w, http.StatusBadRequest, "bad_request", "een app of pod heeft een namespace nodig")
		return
	case name != "" && pod != "":
		writeError(w, http.StatusBadRequest, "bad_request", "kies een app of een pod, niet allebei")
		return
	}
	panels := metrics.KubePodPanels
	if ns == "" {
		panels = metrics.KubeClusterPanels
	}
	s.writeMetrics(w, r, panels, metrics.KubeSelector(clusterID, ns, kind, name, pod), params.Range, nil, nil)
}
