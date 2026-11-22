package httpapi

import (
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/httpapi/gen"
	"github.com/Jonasz1996/clusterforge/internal/kube"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

func toAPIKubeSnapshot(sn kube.Snapshot, hostnames map[string]string, note string) gen.KubeSnapshot {
	out := gen.KubeSnapshot{
		Name: sn.Name, File: sn.File, Node: sn.Node, Kind: gen.KubeSnapshotKind(kube.SnapshotKind(sn.File)), Location: sn.Location,
		S3: sn.S3, SizeBytes: nullableOf(sn.SizeBytes), TakenAt: nullableOf(sn.TakenAt), Ready: sn.Ready, Error: sn.Error,
		Restorable: note == "", RestoreNote: note,
	}
	if h, ok := hostnames[sn.Node]; ok {
		out.Hostname = nullableOf(&h)
	}
	return out
}

// ListKubeSnapshots leest de snapshots op dat moment uit het cluster.
func (s *Server) ListKubeSnapshots(w http.ResponseWriter, r *http.Request, clusterID uuid.UUID) {
	ctx := r.Context()
	c, err := s.kube.Cluster(ctx, clusterID)
	if err != nil {
		s.kubeError(w, r, err)
		return
	}
	cs, err := s.kube.Snapshots(ctx, clusterID, true)
	if err != nil {
		s.kubeError(w, r, err)
		return
	}
	out := gen.KubeSnapshots{
		Items: make([]gen.KubeSnapshot, 0, len(cs.Items)), ReadAt: nullableOf(cs.ReadAt), Error: cs.Error,
		Freshness: gen.KubeSnapshotsFreshness(kube.Fresh(cs.Items, cs.Error, time.Now())), Servers: []gen.KubeSnapshotServer{},
	}
	for _, sn := range cs.Items {
		out.Items = append(out.Items, toAPIKubeSnapshot(sn, cs.Hostnames, kube.RestoreNote(c.Environment, cs.Init, sn)))
	}
	for node, host := range cs.Hostnames {
		out.Servers = append(out.Servers, gen.KubeSnapshotServer{Node: node, Hostname: host, Init: node == cs.Init})
	}
	slices.SortFunc(out.Servers, func(a, b gen.KubeSnapshotServer) int {
		if a.Init != b.Init {
			if a.Init {
				return -1
			}
			return 1
		}
		return strings.Compare(a.Node, b.Node)
	})
	writeJSON(w, http.StatusOK, out)
}

// SaveKubeSnapshot maakt een snapshot, bij voorkeur op de server die het
// cluster begon.
func (s *Server) SaveKubeSnapshot(w http.ResponseWriter, r *http.Request, clusterID uuid.UUID) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	c, err := s.kube.Cluster(ctx, clusterID)
	if err != nil {
		s.kubeError(w, r, err)
		return
	}
	saved, err := s.kube.SaveSnapshot(ctx, events.User(p.User.ID), clusterID)
	if err != nil {
		s.kubeError(w, r, err)
		return
	}
	out := gen.KubeSnapshotSaved{Hostname: saved.Hostname, Output: saved.Output}
	if out.Output == nil {
		out.Output = []string{}
	}
	if saved.Snapshot != nil {
		cs, err := s.kube.Snapshots(ctx, clusterID, false)
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		out.Snapshot = nullableOf(new(toAPIKubeSnapshot(*saved.Snapshot, cs.Hostnames, kube.RestoreNote(c.Environment, cs.Init, *saved.Snapshot))))
	}
	writeJSON(w, http.StatusCreated, out)
}

// RestoreKubeSnapshot geeft het plan, of zet het terugzetten als taak in
// de wachtrij. Alles wat na de snapshot in het cluster veranderde, is
// daarna weg; daarom vraagt het altijd de slug, ook in lab.
func (s *Server) RestoreKubeSnapshot(w http.ResponseWriter, r *http.Request, clusterID uuid.UUID) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var in gen.KubeRestoreInput
	if !decode(w, r, &in) {
		return
	}
	if strings.TrimSpace(in.Name) == "" {
		writeError(w, http.StatusBadRequest, "validation", "kies een snapshot")
		return
	}
	ctx := r.Context()
	c, err := s.kube.Cluster(ctx, clusterID)
	if err != nil {
		s.kubeError(w, r, err)
		return
	}
	if c.Environment == store.EnvironmentProd {
		writeError(w, http.StatusConflict, "conflict", kube.RestoreProdText)
		return
	}
	if deref(in.Preview) {
		plan, err := s.kube.PlanRestore(ctx, clusterID, in.Name)
		if err != nil {
			s.kubeError(w, r, err)
			return
		}
		cs, err := s.kube.Snapshots(ctx, clusterID, false)
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		out := gen.KubeRestorePlan{
			Snapshot: toAPIKubeSnapshot(plan.Snapshot, cs.Hostnames, ""), Init: plan.Init, Others: plan.Others, Workers: plan.Workers,
		}
		if out.Others == nil {
			out.Others = []string{}
		}
		writeJSON(w, http.StatusOK, out)
		return
	}
	if in.Confirm == nil || *in.Confirm != c.Slug {
		writeError(w, http.StatusConflict, "needs_confirmation", "alles wat na de snapshot veranderde is daarna weg; tik ter bevestiging de slug "+c.Slug+" in")
		return
	}
	j, err := s.kube.RequestRestore(ctx, events.User(p.User.ID), clusterID, in.Name)
	if err != nil {
		s.kubeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, toAPIJob(j, &p.User.Username))
}

// ListKubeSnapshotClusters is de stand van de snapshots per cluster, voor
// Back-ups.
func (s *Server) ListKubeSnapshotClusters(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rows, err := s.kube.SnapshotsOverview(ctx)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	now := time.Now()
	items := make([]gen.KubeSnapshotCluster, 0, len(rows))
	for _, o := range rows {
		nodes, err := s.q.ListKubeNodes(ctx, o.Cluster.ID)
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		hosts := map[string]string{}
		for _, n := range nodes {
			if n.NodeHostname != nil {
				hosts[n.Name] = *n.NodeHostname
			}
		}
		it := gen.KubeSnapshotCluster{
			ClusterId: o.Cluster.ID, Name: o.Cluster.Name, Slug: o.Cluster.Slug, Environment: gen.Environment(o.Cluster.Environment),
			Freshness: gen.KubeSnapshotClusterFreshness(kube.Fresh(o.Items, o.Cluster.SnapshotsError, now)),
			ReadAt:    nullableOf(o.Cluster.SnapshotsAt), Error: o.Cluster.SnapshotsError, Servers: []gen.KubeSnapshotClusterServer{},
		}
		var newest *time.Time
		for _, sv := range kube.PerServer(o.Items) {
			out := gen.KubeSnapshotClusterServer{Node: sv.Node, NewestAt: nullableOf(sv.Newest), Count: sv.Count}
			if h, ok := hosts[sv.Node]; ok {
				out.Hostname = nullableOf(&h)
			}
			if sv.Newest != nil && (newest == nil || sv.Newest.After(*newest)) {
				newest = sv.Newest
			}
			it.Servers = append(it.Servers, out)
		}
		it.NewestAt = nullableOf(newest)
		items = append(items, it)
	}
	writeJSON(w, http.StatusOK, list[gen.KubeSnapshotCluster]{items})
}
