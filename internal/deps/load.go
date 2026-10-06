package deps

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/status"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

// Load leest de wereld uit de database en rekent de graaf uit. De status
// wordt bij het lezen uitgerekend, uit dezelfde heartbeats als de
// statusregels.
func (s *Service) Load(ctx context.Context) (*Graph, error) {
	g, _, err := s.load(ctx, s.q)
	return g, err
}

// load leest met q, ook binnen een transactie, en geeft de rijen van de
// diensten erbij.
func (s *Service) load(ctx context.Context, q *store.Queries) (*Graph, []store.Service, error) {
	clusters, err := q.ListDepClusters(ctx)
	if err != nil {
		return nil, nil, err
	}
	vips, err := q.ListAllVIPs(ctx)
	if err != nil {
		return nil, nil, err
	}
	nodes, err := q.ListNodeStatusInputs(ctx)
	if err != nil {
		return nil, nil, err
	}
	services, err := q.ListServices(ctx)
	if err != nil {
		return nil, nil, err
	}
	deps, err := q.ListServiceDependencies(ctx)
	if err != nil {
		return nil, nil, err
	}
	now := s.Now()
	var w World
	byCluster := map[uuid.UUID][]VIP{}
	for _, v := range vips {
		owner := uuid.Nil
		if v.OwnerNodeID != nil {
			owner = *v.OwnerNodeID
		}
		byCluster[v.ClusterID] = append(byCluster[v.ClusterID], VIP{Address: v.Address.String(), Owner: owner})
	}
	for _, c := range clusters {
		w.Clusters = append(w.Clusters, Cluster{
			ID: c.ID, Name: c.Name, Slug: c.Slug, Environment: string(c.Environment), Type: c.Type,
			Status: status.Status(c.Status), Reason: c.StatusReason, VIPs: byCluster[c.ID],
		})
	}
	for _, n := range nodes {
		units := map[string]string{}
		_ = json.Unmarshal(n.Services, &units)
		w.Nodes = append(w.Nodes, Node{
			ID: n.ID, Hostname: n.Hostname, ClusterID: n.ClusterID, Active: n.Lifecycle == "active",
			Fresh: n.HasAgent && n.HeartbeatAt != nil && now.Sub(*n.HeartbeatAt) <= status.HeartbeatDown,
			Units: units, Status: status.Status(n.Status), Reason: n.StatusReason,
		})
	}
	for _, sv := range services {
		svc := Svc{
			ID: sv.ID, ClusterID: sv.ClusterID, NodeID: sv.NodeID, Name: sv.Name, Kind: sv.Kind, Unit: sv.Unit,
			Address: sv.Address, Source: sv.Source, State: sv.State, Description: sv.Description,
			LastSeenAt: sv.LastSeenAt, CreatedAt: sv.CreatedAt, UpdatedAt: sv.UpdatedAt,
		}
		if sv.Port != nil {
			svc.Port = int(*sv.Port)
		}
		w.Services = append(w.Services, svc)
	}
	for _, d := range deps {
		w.Deps = append(w.Deps, Dep{
			ID: d.ID, From: d.FromServiceID, To: d.ToServiceID, Strength: d.Strength, Source: d.Source, State: d.State, Note: d.Note,
		})
	}
	return Build(w), services, nil
}
