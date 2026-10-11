package topo

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// Inventory is wat de kaart naast Proxmox nodig heeft: de nodes met hun
// netwerk uit de facts, de clusters en de VIP's met hun eigenaar.
type Inventory struct {
	Nodes    []InvNode
	Clusters []InvCluster
	VIPs     []InvVIP
}

// InvNode is een node met het netwerk dat zijn agent meldt.
type InvNode struct {
	ID        uuid.UUID
	Hostname  string
	ClusterID *uuid.UUID
	Cluster   string
	Env       string
	Status    string
	Reason    string
	Lifecycle string
	PrimaryIP string
	HasAgent  bool
	// Conn en VMID zijn de gekoppelde VM; VMID is 0 zonder koppeling.
	Conn uuid.UUID
	VMID int

	Interfaces []protocol.Interface
	Routes     []protocol.Route
	Gateway    string
	Keepalived *protocol.Keepalived
}

type InvCluster struct {
	ID     uuid.UUID
	Name   string
	Env    string
	Status string
	Reason string
}

type InvVIP struct {
	ID        uuid.UUID
	ClusterID uuid.UUID
	Address   string
	Interface string
	VRID      int
	Owner     *uuid.UUID
}

// netFacts is het deel van de facts dat de kaart leest.
type netFacts struct {
	Interfaces     []protocol.Interface `json:"interfaces"`
	Routes         []protocol.Route     `json:"routes"`
	DefaultGateway string               `json:"default_gateway"`
	Keepalived     *protocol.Keepalived `json:"keepalived"`
}

// LoadInventory leest de nodes, clusters en VIP's.
func (s *Service) LoadInventory(ctx context.Context) (Inventory, error) {
	var inv Inventory
	nodes, err := s.q.ListMapNodes(ctx)
	if err != nil {
		return inv, err
	}
	for _, n := range nodes {
		var f netFacts
		_ = json.Unmarshal(n.Facts, &f)
		in := InvNode{
			ID: n.ID, Hostname: n.Hostname, ClusterID: n.ClusterID, Cluster: n.Cluster, Env: n.Environment,
			Status: n.Status, Reason: n.StatusReason, Lifecycle: string(n.Lifecycle), HasAgent: n.HasAgent,
			Interfaces: f.Interfaces, Routes: f.Routes, Gateway: f.DefaultGateway, Keepalived: f.Keepalived,
		}
		if n.PrimaryIp != nil {
			in.PrimaryIP = n.PrimaryIp.String()
		}
		if n.ProxmoxID != nil && n.PveVmid != nil {
			in.Conn, in.VMID = *n.ProxmoxID, int(*n.PveVmid)
		}
		inv.Nodes = append(inv.Nodes, in)
	}
	clusters, err := s.q.ListMapClusters(ctx)
	if err != nil {
		return inv, err
	}
	for _, c := range clusters {
		inv.Clusters = append(inv.Clusters, InvCluster{ID: c.ID, Name: c.Name, Env: c.Environment, Status: c.Status, Reason: c.StatusReason})
	}
	vips, err := s.q.ListMapVIPs(ctx)
	if err != nil {
		return inv, err
	}
	for _, v := range vips {
		iv := InvVIP{ID: v.ID, ClusterID: v.ClusterID, Address: v.Address.String(), Interface: v.Interface, Owner: v.OwnerNodeID}
		if v.Vrid != nil {
			iv.VRID = int(*v.Vrid)
		}
		inv.VIPs = append(inv.VIPs, iv)
	}
	return inv, nil
}

// Cluster zoekt een cluster.
func (inv Inventory) Cluster(id uuid.UUID) (InvCluster, bool) {
	for _, c := range inv.Clusters {
		if c.ID == id {
			return c, true
		}
	}
	return InvCluster{}, false
}

// Node zoekt een node.
func (inv Inventory) Node(id uuid.UUID) (InvNode, bool) {
	for _, n := range inv.Nodes {
		if n.ID == id {
			return n, true
		}
	}
	return InvNode{}, false
}
