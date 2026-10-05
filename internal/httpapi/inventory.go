package httpapi

import (
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/oapi-codegen/nullable"

	"github.com/Jonasz1996/clusterforge/internal/auth"
	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/httpapi/gen"
	"github.com/Jonasz1996/clusterforge/internal/inventory"
	"github.com/Jonasz1996/clusterforge/internal/status"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

type list[T any] struct {
	Items []T `json:"items"`
}

func (s *Server) ListUsers(w http.ResponseWriter, r *http.Request) {
	rows, err := s.q.ListUsers(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	items := make([]gen.UserRef, 0, len(rows))
	for _, u := range rows {
		items = append(items, gen.UserRef{Id: u.ID, Username: u.Username})
	}
	writeJSON(w, http.StatusOK, list[gen.UserRef]{items})
}

// --- clusters ---

func (s *Server) ListClusters(w http.ResponseWriter, r *http.Request) {
	rows, err := s.q.ListClusters(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	owners, err := s.q.ListVIPOwners(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	vips := map[uuid.UUID][]gen.VipOwner{}
	for _, v := range owners {
		vips[v.ClusterID] = append(vips[v.ClusterID], gen.VipOwner{Address: v.Address.String(), OwnerHostname: nullableOf(v.OwnerHostname)})
	}
	items := make([]gen.ClusterListItem, 0, len(rows))
	for _, row := range rows {
		c := toAPICluster(row.Cluster)
		items = append(items, gen.ClusterListItem{
			Id: c.Id, Slug: c.Slug, Name: c.Name, Description: c.Description, Type: c.Type,
			Environment: c.Environment, GitRepoUrl: c.GitRepoUrl, Tags: c.Tags,
			Status: c.Status, StatusReason: c.StatusReason, StatusSince: c.StatusSince,
			CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt,
			NodeCount: int(row.NodeCount), VipCount: int(row.VipCount),
			Vips: nonNil(vips[row.Cluster.ID]),
		})
	}
	writeJSON(w, http.StatusOK, list[gen.ClusterListItem]{items})
}

func (s *Server) GetCluster(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	s.writeClusterDetail(w, r, http.StatusOK, id)
}

func (s *Server) CreateCluster(w http.ResponseWriter, r *http.Request) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var req gen.ClusterInput
	if !decode(w, r, &req) {
		return
	}
	f := inventory.ClusterFields{
		Slug: req.Slug, Name: req.Name, Type: string(req.Type), Environment: store.Environment(req.Environment),
		Description: deref(req.Description), GitRepoURL: deref(req.GitRepoUrl), Tags: deref(req.Tags),
		OwnerIDs: deref(req.OwnerIds),
	}
	c, err := s.inv.CreateCluster(r.Context(), events.User(p.User.ID), f)
	if s.inventoryError(w, r, err) {
		return
	}
	s.writeClusterDetail(w, r, http.StatusCreated, c.ID)
}

func (s *Server) UpdateCluster(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var req gen.ClusterPatch
	if !decode(w, r, &req) {
		return
	}
	_, err := s.inv.UpdateCluster(r.Context(), events.User(p.User.ID), id, func(f *inventory.ClusterFields) {
		set(&f.Slug, req.Slug)
		set(&f.Name, req.Name)
		set(&f.Description, req.Description)
		set(&f.GitRepoURL, req.GitRepoUrl)
		set(&f.Tags, req.Tags)
		set(&f.OwnerIDs, req.OwnerIds)
		if req.Type != nil {
			f.Type = string(*req.Type)
		}
		if req.Environment != nil {
			f.Environment = store.Environment(*req.Environment)
		}
	})
	if s.inventoryError(w, r, err) {
		return
	}
	s.writeClusterDetail(w, r, http.StatusOK, id)
}

func (s *Server) DeleteCluster(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	if s.inventoryError(w, r, s.inv.DeleteCluster(r.Context(), events.User(p.User.ID), id)) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) writeClusterDetail(w http.ResponseWriter, r *http.Request, status int, id uuid.UUID) {
	ctx := r.Context()
	c, err := s.q.GetCluster(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "not_found", "cluster niet gevonden")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	owners, err := s.q.ListClusterOwners(ctx, id)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	nodes, err := s.q.ListNodesByCluster(ctx, &id)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	vips, err := s.q.ListVIPsByCluster(ctx, id)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	base := toAPICluster(c)
	d := gen.ClusterDetail{
		Id: base.Id, Slug: base.Slug, Name: base.Name, Description: base.Description, Type: base.Type,
		Environment: base.Environment, GitRepoUrl: base.GitRepoUrl, Tags: base.Tags,
		Status: base.Status, StatusReason: base.StatusReason, StatusSince: base.StatusSince,
		CreatedAt: base.CreatedAt, UpdatedAt: base.UpdatedAt,
		Owners: make([]gen.UserRef, 0, len(owners)),
		Nodes:  make([]gen.Node, 0, len(nodes)),
		Vips:   make([]gen.Vip, 0, len(vips)),
	}
	for _, o := range owners {
		d.Owners = append(d.Owners, gen.UserRef{Id: o.ID, Username: o.Username})
	}
	for _, n := range nodes {
		d.Nodes = append(d.Nodes, toAPINode(nodeRow(n)))
	}
	for _, v := range vips {
		d.Vips = append(d.Vips, toAPIVip(v.Vip, v.OwnerHostname))
	}
	writeJSON(w, status, d)
}

func toAPICluster(c store.Cluster) gen.Cluster {
	return gen.Cluster{
		Id: c.ID, Slug: c.Slug, Name: c.Name, Description: c.Description, Type: gen.ClusterType(c.Type),
		Environment: gen.Environment(c.Environment), GitRepoUrl: c.GitRepoUrl, Tags: nonNil(c.Tags),
		Status: gen.Status(c.Status), StatusReason: c.StatusReason, StatusSince: nullableOf(c.StatusSince),
		CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt,
	}
}

// --- nodes ---

func (s *Server) ListNodes(w http.ResponseWriter, r *http.Request) {
	rows, err := s.q.ListNodes(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	items := make([]gen.Node, 0, len(rows))
	for _, row := range rows {
		items = append(items, toAPINode(nodeRow(row)))
	}
	writeJSON(w, http.StatusOK, list[gen.Node]{items})
}

func (s *Server) GetNode(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	s.writeNode(w, r, http.StatusOK, id)
}

func (s *Server) CreateNode(w http.ResponseWriter, r *http.Request) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var req gen.NodeInput
	if !decode(w, r, &req) {
		return
	}
	f := inventory.NodeFields{
		Hostname: req.Hostname, Role: deref(req.Role), Description: deref(req.Description), Tags: deref(req.Tags),
		ClusterID: nullablePtr(req.ClusterId), PrimaryIP: req.PrimaryIp.GetOrEmpty(),
	}
	if req.Lifecycle != nil {
		f.Lifecycle = store.NodeLifecycle(*req.Lifecycle)
	}
	n, err := s.inv.CreateNode(r.Context(), events.User(p.User.ID), f)
	if s.inventoryError(w, r, err) {
		return
	}
	s.writeNode(w, r, http.StatusCreated, n.ID)
}

func (s *Server) UpdateNode(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var req gen.NodePatch
	if !decode(w, r, &req) {
		return
	}
	_, err := s.inv.UpdateNode(r.Context(), events.User(p.User.ID), id, func(f *inventory.NodeFields) {
		set(&f.Hostname, req.Hostname)
		set(&f.Role, req.Role)
		set(&f.Description, req.Description)
		set(&f.Tags, req.Tags)
		if req.Lifecycle != nil {
			f.Lifecycle = store.NodeLifecycle(*req.Lifecycle)
		}
		if req.ClusterId.IsSpecified() {
			f.ClusterID = nullablePtr(req.ClusterId)
		}
		if req.PrimaryIp.IsSpecified() {
			f.PrimaryIP = req.PrimaryIp.GetOrEmpty()
		}
	})
	if s.inventoryError(w, r, err) {
		return
	}
	s.writeNode(w, r, http.StatusOK, id)
}

func (s *Server) DeleteNode(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	if s.inventoryError(w, r, s.inv.DeleteNode(r.Context(), events.User(p.User.ID), id)) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) writeNode(w http.ResponseWriter, r *http.Request, status int, id uuid.UUID) {
	row, err := s.q.GetNode(r.Context(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "not_found", "node niet gevonden")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, status, toAPINode(nodeRow(row)))
}

// nodeRow heeft dezelfde velden als de rijen van ListNodes, GetNode en
// ListNodesByCluster, zodat die er rechtstreeks naar om te zetten zijn.
type nodeRow struct {
	Node            store.Node
	ClusterSlug     *string
	ClusterName     *string
	AgentID         *uuid.UUID
	AgentVersion    *string
	AgentEnrolledAt *time.Time
	AgentLastSeenAt *time.Time
}

func toAPINode(r nodeRow) gen.Node {
	n := r.Node
	var ip *string
	if n.PrimaryIp != nil {
		s := n.PrimaryIp.String()
		ip = &s
	}
	clusterSlug, clusterName := r.ClusterSlug, r.ClusterName
	if n.ClusterID == nil {
		clusterSlug, clusterName = nil, nil
	}
	agent := nullable.NewNullNullable[gen.AgentSummary]()
	if r.AgentID != nil {
		agent = nullable.NewNullableWithValue(gen.AgentSummary{
			Id: *r.AgentID, Version: deref(r.AgentVersion), EnrolledAt: deref(r.AgentEnrolledAt),
			LastSeenAt: nullableOf(r.AgentLastSeenAt), Connection: connection(r.AgentLastSeenAt, time.Now()),
		})
	}
	return gen.Node{
		Id: n.ID, Hostname: n.Hostname, Role: n.Role, Description: n.Description,
		Lifecycle: gen.NodeLifecycle(n.Lifecycle), Tags: nonNil(n.Tags),
		ClusterId: nullableOf(n.ClusterID), ClusterSlug: nullableOf(clusterSlug), ClusterName: nullableOf(clusterName),
		PrimaryIp: nullableOf(ip), CreatedAt: n.CreatedAt, UpdatedAt: n.UpdatedAt, Agent: agent,
		Status: gen.Status(n.Status), StatusReason: n.StatusReason, StatusSince: nullableOf(n.StatusSince),
	}
}

// connection leidt de verbindingsstatus af uit de laatste heartbeat.
func connection(lastSeen *time.Time, now time.Time) gen.AgentConnection {
	switch {
	case lastSeen == nil:
		return gen.Offline
	case now.Sub(*lastSeen) <= status.HeartbeatLate:
		return gen.Online
	case now.Sub(*lastSeen) <= status.HeartbeatDown:
		return gen.Late
	default:
		return gen.Offline
	}
}

// --- VIP's ---

func (s *Server) CreateVip(w http.ResponseWriter, r *http.Request, clusterID uuid.UUID) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var req gen.VipInput
	if !decode(w, r, &req) {
		return
	}
	f := inventory.VIPFields{
		Address: req.Address, Interface: deref(req.Interface), Description: deref(req.Description),
		VRID: nullablePtr(req.Vrid),
	}
	v, err := s.inv.CreateVIP(r.Context(), events.User(p.User.ID), clusterID, f)
	if s.inventoryError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusCreated, toAPIVip(v, nil))
}

func (s *Server) UpdateVip(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var req gen.VipPatch
	if !decode(w, r, &req) {
		return
	}
	v, err := s.inv.UpdateVIP(r.Context(), events.User(p.User.ID), id, func(f *inventory.VIPFields) {
		set(&f.Address, req.Address)
		set(&f.Interface, req.Interface)
		set(&f.Description, req.Description)
		if req.Vrid.IsSpecified() {
			f.VRID = nullablePtr(req.Vrid)
		}
	})
	if s.inventoryError(w, r, err) {
		return
	}
	var owner *string
	if v.OwnerNodeID != nil {
		if n, err := s.q.GetNode(r.Context(), *v.OwnerNodeID); err == nil {
			owner = &n.Node.Hostname
		}
	}
	writeJSON(w, http.StatusOK, toAPIVip(v, owner))
}

func (s *Server) DeleteVip(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	if s.inventoryError(w, r, s.inv.DeleteVIP(r.Context(), events.User(p.User.ID), id)) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func toAPIVip(v store.Vip, ownerHostname *string) gen.Vip {
	var vrid *int
	if v.Vrid != nil {
		n := int(*v.Vrid)
		vrid = &n
	}
	return gen.Vip{
		Id: v.ID, ClusterId: v.ClusterID, Address: v.Address.String(), Interface: v.Interface,
		Description: v.Description, Vrid: nullableOf(vrid), OwnerNodeId: nullableOf(v.OwnerNodeID),
		OwnerHostname: nullableOf(ownerHostname), OwnerSince: nullableOf(v.OwnerSince),
	}
}

// --- hulpfuncties ---

func requireAdmin(w http.ResponseWriter, r *http.Request) (auth.Principal, bool) {
	p, _ := principalFrom(r.Context())
	if p.User.Role != store.UserRoleAdmin {
		writeError(w, http.StatusForbidden, "forbidden", "alleen voor beheerders")
		return p, false
	}
	return p, true
}

// inventoryError schrijft een passende foutrespons en geeft true terug als
// err niet nil was.
func (s *Server) inventoryError(w http.ResponseWriter, r *http.Request, err error) bool {
	if err == nil {
		return false
	}
	var ve inventory.ValidationError
	var ce inventory.ConflictError
	switch {
	case errors.As(err, &ve):
		writeError(w, http.StatusBadRequest, "validation", ve.Msg)
	case errors.As(err, &ce):
		writeError(w, http.StatusConflict, "conflict", ce.Msg)
	case errors.Is(err, inventory.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "niet gevonden")
	default:
		s.internalError(w, r, err)
	}
	return true
}

func deref[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}
	return *p
}

// set overschrijft dst als src is meegestuurd.
func set[T any](dst *T, src *T) {
	if src != nil {
		*dst = *src
	}
}

// nullableOf zet een pointer om naar een expliciet null of een waarde, zodat
// het veld altijd in de JSON staat.
func nullableOf[T any](p *T) nullable.Nullable[T] {
	if p == nil {
		return nullable.NewNullNullable[T]()
	}
	return nullable.NewNullableWithValue(*p)
}

// nullablePtr geeft nil voor null of niet meegestuurd.
func nullablePtr[T any](n nullable.Nullable[T]) *T {
	v, err := n.Get()
	if err != nil {
		return nil
	}
	return &v
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
