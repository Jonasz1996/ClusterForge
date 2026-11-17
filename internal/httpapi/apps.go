package httpapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Jonasz1996/clusterforge/internal/apps"
	"github.com/Jonasz1996/clusterforge/internal/httpapi/gen"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

// De kaart Applicatie (fase 29): de stand die de ctl-scripts van een
// template per node melden, en SeaweedFS over het cluster.

func (s *Server) GetClusterApps(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	ctx := r.Context()
	if _, err := s.q.GetCluster(ctx, id); errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "not_found", "cluster niet gevonden")
		return
	} else if err != nil {
		s.internalError(w, r, err)
		return
	}
	byCluster, err := s.appOverviews(ctx, &id)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	o, ok := byCluster[id]
	if !ok {
		o = apps.Overview{Apps: []apps.App{}}
	}
	writeJSON(w, http.StatusOK, o)
}

// appOverviews bouwt het overzicht per cluster; nil is alle clusters.
func (s *Server) appOverviews(ctx context.Context, cluster *uuid.UUID) (map[uuid.UUID]apps.Overview, error) {
	rows, err := s.q.ListAppFacts(ctx, cluster)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	nodes := map[uuid.UUID][]apps.Node{}
	for _, r := range rows {
		if r.ClusterID == nil {
			continue
		}
		n := apps.Node{ID: r.ID, Hostname: r.Hostname, Facts: r.Facts,
			Online: connection(r.AgentLastSeenAt, now) != gen.Offline,
			Active: r.Lifecycle == store.NodeLifecycleActive}
		if r.PrimaryIp != nil {
			n.Address = r.PrimaryIp.String()
		}
		nodes[*r.ClusterID] = append(nodes[*r.ClusterID], n)
	}
	out := make(map[uuid.UUID]apps.Overview, len(nodes))
	for id, ns := range nodes {
		out[id] = apps.Evaluate(ns, now)
	}
	return out, nil
}
