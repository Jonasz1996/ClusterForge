package httpapi

import (
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/httpapi/gen"
	"github.com/Jonasz1996/clusterforge/internal/kube"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

func toAPIKubeAccess(a kube.AccessRow, now time.Time) gen.KubeAccess {
	state := gen.KubeAccessStateActive
	switch {
	case a.RevokedAt != nil && a.RemovedAt == nil:
		state = gen.KubeAccessStateRevoking
	case a.RevokedAt != nil:
		state = gen.KubeAccessStateRevoked
	case a.RemovedAt != nil || !a.ExpiresAt.After(now):
		state = gen.KubeAccessStateExpired
	}
	return gen.KubeAccess{
		Id: a.ID, ClusterId: a.ClusterID, UserId: a.UserID, User: a.Username, Name: a.Name, Role: gen.KubeAccessRole(a.Role),
		Account: a.Account, State: state, CreatedAt: a.CreatedAt, ExpiresAt: a.ExpiresAt, RevokedAt: nullableOf(a.RevokedAt),
		RevokedBy: a.RevokedByName, RevokeReason: a.RevokeReason, RemovedAt: nullableOf(a.RemovedAt), RemoveError: a.RemoveError,
	}
}

func (s *Server) ListKubeAccess(w http.ResponseWriter, r *http.Request, clusterID uuid.UUID) {
	p, _ := principalFrom(r.Context())
	ctx := r.Context()
	if _, err := s.kube.Cluster(ctx, clusterID); err != nil {
		s.kubeError(w, r, err)
		return
	}
	var user *uuid.UUID
	if p.User.Role != store.UserRoleAdmin {
		user = &p.User.ID
	}
	rows, err := s.kube.ListAccess(ctx, clusterID, user)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	now := time.Now()
	items := make([]gen.KubeAccess, 0, len(rows))
	for _, a := range rows {
		items = append(items, toAPIKubeAccess(a, now))
	}
	writeJSON(w, http.StatusOK, list[gen.KubeAccess]{items})
}

// IssueKubeAccess geeft een kubeconfig. Het token erin geeft rechten op het
// hele cluster tot het verloopt, ook zonder ClusterForge; daarom vraagt het
// altijd je wachtwoord of code, zodat een gestolen sessie alleen niet
// genoeg is.
func (s *Server) IssueKubeAccess(w http.ResponseWriter, r *http.Request, clusterID uuid.UUID) {
	p, _ := principalFrom(r.Context())
	var in gen.KubeAccessInput
	if !decode(w, r, &in) {
		return
	}
	ctx := r.Context()
	c, err := s.kube.Cluster(ctx, clusterID)
	if err != nil {
		s.kubeError(w, r, err)
		return
	}
	role := string(in.Role)
	if role == protocol.KubeAccessAdmin && p.User.Role != store.UserRoleAdmin {
		writeError(w, http.StatusForbidden, "forbidden", "als viewer krijg je een kubeconfig die alleen leest (view)")
		return
	}
	ai := kube.AccessInput{UserID: p.User.ID, Username: p.User.Username, Name: in.Name, Role: role, TTL: time.Duration(in.Days) * 24 * time.Hour}
	if err := kube.ValidateAccess(ai); err != nil {
		s.kubeError(w, r, err)
		return
	}
	if role == protocol.KubeAccessAdmin && !confirmProd(w, p, c.Environment == store.EnvironmentProd, c.Slug, in.Confirm, "een admin-kubeconfig krijgen") {
		return
	}
	if s.authError(w, r, s.auth.Confirm(ctx, p, deref(in.Password), deref(in.TotpCode), "kube_access")) {
		return
	}
	iss, err := s.kube.Issue(ctx, c, ai)
	if err != nil {
		s.kubeError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusCreated, gen.KubeAccessIssued{
		Access:     toAPIKubeAccess(kube.AccessRow{KubeAccess: iss.Access, Username: p.User.Username}, time.Now()),
		Kubeconfig: string(iss.Kubeconfig), Filename: c.Slug + ".kubeconfig", Server: iss.Server,
	})
}

func (s *Server) RevokeKubeAccess(w http.ResponseWriter, r *http.Request, clusterID, accessID uuid.UUID) {
	p, _ := principalFrom(r.Context())
	ctx := r.Context()
	a, err := s.kube.Access(ctx, accessID)
	if err != nil || a.ClusterID != clusterID {
		writeError(w, http.StatusNotFound, "not_found", "geen kubeconfig met dit id in dit cluster")
		return
	}
	if p.User.Role != store.UserRoleAdmin && a.UserID != p.User.ID {
		writeError(w, http.StatusForbidden, "forbidden", "als viewer kun je alleen je eigen kubeconfigs intrekken")
		return
	}
	if _, err := s.kube.Revoke(ctx, events.User(p.User.ID), &p.User.ID, accessID, ""); err != nil {
		s.kubeError(w, r, err)
		return
	}
	a, err = s.kube.Access(ctx, accessID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toAPIKubeAccess(a, time.Now()))
}
