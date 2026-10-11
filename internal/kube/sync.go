package kube

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Jonasz1996/clusterforge/internal/store"
)

// Run vat de Kubernetes-clusters elke Interval samen, tot ctx afloopt.
func (s *Service) Run(ctx context.Context) {
	if s.Interval <= 0 {
		return
	}
	t := time.NewTicker(s.Interval)
	defer t.Stop()
	for {
		if err := s.SyncAll(ctx); err != nil && ctx.Err() == nil {
			s.log.Warn("Kubernetes-clusters samenvatten mislukt", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// SyncAll vat elk Kubernetes-cluster samen. Een cluster waarvan de API niet
// antwoordt, krijgt de fout in kube_clusters; de andere gaan door.
func (s *Service) SyncAll(ctx context.Context) error {
	ids, err := s.q.ListKubeClusterIDs(ctx)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err := s.Sync(ctx, id); err != nil && ctx.Err() != nil {
			return err
		}
	}
	return nil
}

// Sync vat één cluster samen in kube_clusters en kube_nodes.
func (s *Service) Sync(ctx context.Context, clusterID uuid.UUID) error {
	var (
		version   string
		via       uuid.UUID
		nodes     []Node
		pods      []Pod
		workloads []Workload
		errs      [3]error
	)
	body, via, err := s.get(ctx, clusterID, "/version", nil)
	if err == nil {
		version, err = parseVersion(body)
	}
	if err == nil {
		var wg sync.WaitGroup
		wg.Go(func() { nodes, errs[0] = s.Nodes(ctx, clusterID) })
		wg.Go(func() { pods, errs[1] = s.Pods(ctx, clusterID, "", "") })
		wg.Go(func() { workloads, errs[2] = s.Workloads(ctx, clusterID, "") })
		wg.Wait()
		err = errors.Join(errs[:]...)
	}
	if err != nil {
		if serr := s.q.SaveKubeClusterError(ctx, store.SaveKubeClusterErrorParams{ClusterID: clusterID, Error: err.Error()}); serr != nil {
			return serr
		}
		return err
	}
	links, err := s.Links(ctx, clusterID, nodes)
	if err != nil {
		return err
	}
	perNode := PodsPerNode(pods)
	pc := CountPods(pods)
	ready, wready := 0, 0
	for _, n := range nodes {
		if n.Ready() {
			ready++
		}
	}
	for _, w := range workloads {
		if w.Healthy() {
			wready++
		}
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		viaID := &via
		if via == uuid.Nil {
			viaID = nil
		}
		if err := q.SaveKubeCluster(ctx, store.SaveKubeClusterParams{
			ClusterID: clusterID, Version: version, NodesTotal: int32(len(nodes)), NodesReady: int32(ready),
			PodsTotal: int32(pc.Total), PodsRunning: int32(pc.Running), PodsPending: int32(pc.Pending), PodsProblem: int32(pc.Problem),
			WorkloadsTotal: int32(len(workloads)), WorkloadsReady: int32(wready), ViaNodeID: viaID,
		}); err != nil {
			return err
		}
		if err := q.DeleteKubeNodes(ctx, clusterID); err != nil {
			return err
		}
		for _, n := range nodes {
			var nodeID *uuid.UUID
			if l, ok := links[n.Metadata.Name]; ok {
				nodeID = &l.NodeID
			}
			created := n.Metadata.CreationTimestamp
			if err := q.InsertKubeNode(ctx, store.InsertKubeNodeParams{
				ClusterID: clusterID, Name: n.Metadata.Name, NodeID: nodeID, Roles: nonNil(n.Roles()), Ready: n.Ready(),
				Unschedulable: n.Spec.Unschedulable, Kubelet: n.Status.NodeInfo.KubeletVersion, InternalIp: n.Address("InternalIP"),
				CpuMillis: MilliCPU(n.Status.Allocatable["cpu"]), MemoryBytes: Bytes(n.Status.Allocatable["memory"]),
				Pods: int32(perNode[n.Metadata.Name]), CreatedAt: &created,
			}); err != nil {
				return err
			}
		}
		return nil
	})
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
