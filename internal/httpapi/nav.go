package httpapi

import (
	"net/http"

	"github.com/Jonasz1996/clusterforge/internal/httpapi/gen"
)

// GetNavUsage zegt welke onderdelen in gebruik zijn (fase 47). Het menu
// toont Kubernetes, Kaart, GitOps en de platformen alleen als ze iets
// beheren; zoeken (Ctrl K) en "Alles tonen" vinden ze altijd.
func (s *Server) GetNavUsage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u, err := s.q.NavUsage(ctx)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out := gen.NavUsage{
		Kubernetes: u.Kubernetes, Gitops: u.Gitops, Dependencies: u.Dependencies,
		Proxmox: u.Proxmox, Docker: u.Docker, Zabbix: u.ZabbixAgents,
	}
	if s.pbs != nil {
		if l, err := s.pbs.Servers(ctx); err == nil {
			out.Pbs = len(l) > 0
		}
	}
	if s.truenas != nil {
		if l, err := s.truenas.Servers(ctx); err == nil {
			out.Truenas = len(l) > 0
		}
	}
	if s.opnsense != nil {
		if l, err := s.opnsense.Clusters(ctx); err == nil {
			out.Opnsense = len(l) > 0
		}
	}
	if s.aiverslag != nil {
		if l, err := s.aiverslag.Instances(ctx); err == nil {
			out.Aiverslag = len(l) > 0
		}
	}
	if s.zabbixMon != nil {
		if _, _, ok := s.zabbixMon.Current(); ok {
			out.Zabbix = true
		}
	}
	writeJSON(w, http.StatusOK, out)
}
