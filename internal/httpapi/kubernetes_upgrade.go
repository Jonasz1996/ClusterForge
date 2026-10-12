package httpapi

import (
	"cmp"
	"net/http"
	"slices"

	"github.com/google/uuid"
	"github.com/oapi-codegen/nullable"

	"github.com/Jonasz1996/clusterforge/internal/httpapi/gen"
)

// Kubernetes bijwerken is overstappen naar een nieuwere versie van de
// template kubernetes, met de wijziging van de spec en de uitrol per node.
// Hier staat alleen wat het overzicht daarvoor toont.

func (s *Server) GetKubeUpgrade(w http.ResponseWriter, r *http.Request, clusterID uuid.UUID) {
	ctx := r.Context()
	c, err := s.kube.Cluster(ctx, clusterID)
	if err != nil {
		s.kubeError(w, r, err)
		return
	}
	if c.TemplateName == nil || c.TemplateVersion == nil {
		writeError(w, http.StatusConflict, "not_template", "dit cluster komt niet uit een template; ClusterForge kan zijn k3s niet bijwerken")
		return
	}
	reg := s.deploy.Templates
	out := gen.KubeUpgrade{
		Template: *c.TemplateName, Version: *c.TemplateVersion, Behind: c.AppliedRevision < c.SpecRevision, GitManaged: c.GitRepoID != nil,
		Nodes: []gen.KubeUpgradeNode{}, Next: nullable.NewNullNullable[gen.KubeUpgradeNext](),
	}
	if tpl, ok := reg.Get(out.Template, out.Version); ok {
		out.K3s = tpl.KubeVersion()
	}
	if next := reg.KubeNext(out.Template, out.Version); next != nil {
		out.Next = nullable.NewNullableWithValue(gen.KubeUpgradeNext{Version: next.Version, K3s: next.KubeVersion()})
	}
	switch {
	case out.GitManaged:
		out.Blocked = "Dit cluster volgt Git: zet in cluster.yaml de templateversie op de nieuwe versie en keur de wijziging goed."
	case out.Behind:
		out.Blocked = "Een eerdere toepassing op dit cluster is niet afgemaakt. Kies eerst Opnieuw toepassen op de pagina van het cluster."
	}
	nodes, err := s.kube.Nodes(ctx, clusterID)
	if err != nil {
		out.NodesError = err.Error()
	}
	for _, n := range nodes {
		out.Nodes = append(out.Nodes, gen.KubeUpgradeNode{
			Name: n.Metadata.Name, Server: slices.Contains(n.Roles(), "control-plane"), Kubelet: n.Status.NodeInfo.KubeletVersion,
			Ready: n.Ready(), Unschedulable: n.Spec.Unschedulable,
		})
	}
	slices.SortFunc(out.Nodes, func(a, b gen.KubeUpgradeNode) int {
		if a.Server != b.Server {
			if a.Server {
				return -1
			}
			return 1
		}
		return cmp.Compare(a.Name, b.Name)
	})
	writeJSON(w, http.StatusOK, out)
}
