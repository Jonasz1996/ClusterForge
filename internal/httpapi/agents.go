package httpapi

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/oapi-codegen/nullable"

	"github.com/Jonasz1996/clusterforge/internal/agents"
	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/httpapi/gen"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// AgentBus is wat de API van de NATS-kant nodig heeft.
type AgentBus interface {
	agents.Disconnecter
	Fingerprint() string
	Port() int
}

func (s *Server) ListEnrollmentTokens(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	rows, err := s.q.ListActiveEnrollmentTokens(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	items := make([]gen.EnrollmentToken, 0, len(rows))
	for _, t := range rows {
		items = append(items, gen.EnrollmentToken{
			Id: t.ID, Description: t.Description, NodeId: nullableOf(t.NodeID), NodeHostname: nullableOf(t.NodeHostname),
			ClusterId: nullableOf(t.ClusterID), ClusterName: nullableOf(t.ClusterName), MaxUses: int(t.MaxUses),
			Uses: int(t.Uses), ExpiresAt: t.ExpiresAt, CreatedAt: t.CreatedAt, CreatedBy: nullableOf(t.CreatedByUsername),
		})
	}
	writeJSON(w, http.StatusOK, list[gen.EnrollmentToken]{items})
}

func (s *Server) CreateEnrollmentToken(w http.ResponseWriter, r *http.Request) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var req gen.EnrollmentTokenInput
	if !decode(w, r, &req) {
		return
	}
	in := agents.TokenInput{
		Description: deref(req.Description), NodeID: nullablePtr(req.NodeId), ClusterID: nullablePtr(req.ClusterId),
		MaxUses: deref(req.MaxUses), TTL: time.Duration(deref(req.TtlHours)) * time.Hour,
	}
	token, t, err := s.agents.CreateToken(r.Context(), p.User.ID, in)
	if s.agentsError(w, r, err) {
		return
	}
	resp := gen.NewEnrollmentToken{
		Id: t.ID, Token: token, Description: t.Description, NodeId: nullableOf(t.NodeID), ClusterId: nullableOf(t.ClusterID),
		MaxUses: int(t.MaxUses), Uses: int(t.Uses), ExpiresAt: t.ExpiresAt, CreatedAt: t.CreatedAt,
		CreatedBy:    nullable.NewNullableWithValue(p.User.Username),
		NodeHostname: nullable.NewNullNullable[string](), ClusterName: nullable.NewNullNullable[string](),
	}
	if t.NodeID != nil {
		if n, err := s.q.GetNode(r.Context(), *t.NodeID); err == nil {
			resp.NodeHostname = nullable.NewNullableWithValue(n.Node.Hostname)
		}
	}
	if t.ClusterID != nil {
		if c, err := s.q.GetCluster(r.Context(), *t.ClusterID); err == nil {
			resp.ClusterName = nullable.NewNullableWithValue(c.Name)
		}
	}
	writeJSON(w, http.StatusCreated, resp)
}

func (s *Server) DeleteEnrollmentToken(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	if s.agentsError(w, r, s.agents.DeleteToken(r.Context(), p.User.ID, id)) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) RevokeAgent(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	if s.agentsError(w, r, s.agents.Revoke(r.Context(), p.User.ID, id)) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// EnrollAgent is publiek: de agent heeft nog geen sessie, alleen het token.
func (s *Server) EnrollAgent(w http.ResponseWriter, r *http.Request) {
	if !s.enrollLimiter.allow(clientIP(r)) {
		w.Header().Set("Retry-After", "6")
		writeError(w, http.StatusTooManyRequests, "rate_limited", "te veel aanmeldpogingen, probeer het straks opnieuw")
		return
	}
	var req gen.EnrollRequest
	if !decode(w, r, &req) {
		return
	}
	a, err := s.agents.Enroll(r.Context(), protocol.EnrollRequest{
		Token: req.Token, NkeyPublic: req.NkeyPublic, Hostname: req.Hostname, MachineID: req.MachineId,
		AgentVersion: req.AgentVersion, ProtocolVersion: req.ProtocolVersion,
	})
	if errors.Is(err, agents.ErrInvalidToken) {
		reason := "unknown"
		var te agents.TokenError
		if errors.As(err, &te) {
			reason = te.Reason
		}
		host := req.Hostname
		if len(host) > 64 {
			host = strings.ToValidUTF8(host[:64], "")
		}
		s.throttle.Write(r.Context(), clientIP(r), events.Event{
			Actor: events.System(), SubjectType: "agent", SubjectID: "onbekend", Action: "agent.enroll_failed",
			Payload: map[string]any{"ip": clientIP(r), "hostname": host, "reason": reason},
		})
		writeError(w, http.StatusUnauthorized, "invalid_token", err.Error())
		return
	}
	if s.agentsError(w, r, err) {
		return
	}
	s.log.InfoContext(r.Context(), "agent aangemeld", "node", a.NodeID, "hostname", req.Hostname, "ip", clientIP(r))
	writeJSON(w, http.StatusOK, gen.EnrollResponse{
		AgentId: a.ID, NodeId: a.NodeID, NatsUrl: s.natsURL(r), NatsCertSha256: s.bus.Fingerprint(),
	})
}

// natsURL is het adres waarop de agent NATS bereikt: CF_NATS_ADVERTISE, of
// anders de hostnaam van deze request met de NATS-poort.
func (s *Server) natsURL(r *http.Request) string {
	if a := s.cfg.NATSAdvertise; a != "" {
		if _, _, err := net.SplitHostPort(a); err != nil {
			a = net.JoinHostPort(a, strconv.Itoa(s.bus.Port()))
		}
		return "tls://" + a
	}
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return "tls://" + net.JoinHostPort(host, strconv.Itoa(s.bus.Port()))
}

func (s *Server) GetNodeFacts(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	ctx := r.Context()
	if _, err := s.q.GetNode(ctx, id); errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "not_found", "node niet gevonden")
		return
	} else if err != nil {
		s.internalError(w, r, err)
		return
	}
	// facts gaan als ruwe JSON door; ze zijn al door de server zelf opgebouwd.
	type heartbeat struct {
		ReceivedAt    time.Time         `json:"received_at"`
		UptimeSeconds int64             `json:"uptime_seconds"`
		Load          [3]float64        `json:"load"`
		Addresses     []string          `json:"addresses"`
		Services      map[string]string `json:"services"`
	}
	resp := struct {
		Facts       json.RawMessage `json:"facts"`
		CollectedAt *time.Time      `json:"collected_at"`
		ChangedAt   *time.Time      `json:"changed_at"`
		Heartbeat   *heartbeat      `json:"heartbeat"`
	}{Facts: json.RawMessage("null")}

	f, err := s.q.GetNodeFacts(ctx, id)
	switch {
	case err == nil:
		resp.Facts, resp.CollectedAt, resp.ChangedAt = f.Facts, &f.CollectedAt, &f.ChangedAt
	case !errors.Is(err, pgx.ErrNoRows):
		s.internalError(w, r, err)
		return
	}
	st, err := s.q.GetNodeStatus(ctx, id)
	switch {
	case err == nil:
		hb := &heartbeat{
			ReceivedAt: st.HeartbeatAt, UptimeSeconds: st.UptimeSeconds, Load: [3]float64{st.Load1, st.Load5, st.Load15},
			Addresses: nonNil(st.Addresses), Services: map[string]string{},
		}
		_ = json.Unmarshal(st.Services, &hb.Services)
		resp.Heartbeat = hb
	case !errors.Is(err, pgx.ErrNoRows):
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) agentsError(w http.ResponseWriter, r *http.Request, err error) bool {
	if err == nil {
		return false
	}
	var ve agents.ValidationError
	var ce agents.ConflictError
	switch {
	case errors.As(err, &ve):
		writeError(w, http.StatusBadRequest, "validation", ve.Msg)
	case errors.As(err, &ce):
		writeError(w, http.StatusConflict, "conflict", ce.Msg)
	case errors.Is(err, agents.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "niet gevonden")
	default:
		s.internalError(w, r, err)
	}
	return true
}
