package httpapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/firewall"
	"github.com/Jonasz1996/clusterforge/internal/httpapi/gen"
)

// Firewall (fase 16). De antwoorden zijn de structs van internal/firewall;
// de webinterface typt ze in web/src/lib/firewall.ts.

func (s *Server) firewallError(w http.ResponseWriter, r *http.Request, err error) {
	var fe *firewall.FieldError
	switch {
	case errors.As(err, &fe):
		writeError(w, http.StatusBadRequest, "invalid", fe.Msg)
	case errors.Is(err, firewall.ErrNotFound), errors.Is(err, firewall.ErrRuleNotFound):
		writeError(w, http.StatusNotFound, "not_found", err.Error())
	default:
		s.internalError(w, r, err)
	}
}

// kickFirewall draait meteen een ronde, zodat een nieuwe stand of regel
// niet een minuut op zich laat wachten.
func (s *Server) kickFirewall() {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		if err := s.firewall.Tick(ctx); err != nil {
			s.log.Warn("firewallronde mislukt", "err", err)
		}
	}()
}

func (s *Server) GetFirewall(w http.ResponseWriter, r *http.Request) {
	o, err := s.firewall.Overview(r.Context())
	if err != nil {
		s.firewallError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, o)
}

func (s *Server) UpdateFirewallSettings(w http.ResponseWriter, r *http.Request) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var in gen.FirewallSettingsInput
	if !decode(w, r, &in) {
		return
	}
	uid := p.User.ID
	set, err := s.firewall.UpdateSettings(r.Context(), events.User(p.User.ID), &uid, firewall.Settings{
		AutoApply: in.AutoApply, LAN: in.Lan, Admin: in.Admin, SSHFrom: string(in.SshFrom), UnknownPorts: string(in.UnknownPorts),
		Strict: in.Strict, ConfirmSeconds: in.ConfirmSeconds, GraceHours: in.GraceHours, Monitor: in.Monitor,
	})
	if err != nil {
		s.firewallError(w, r, err)
		return
	}
	s.kickFirewall()
	writeJSON(w, http.StatusOK, set)
}

func (s *Server) SetFirewallNodes(w http.ResponseWriter, r *http.Request) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var in gen.FirewallNodesInput
	if !decode(w, r, &in) {
		return
	}
	ctx := r.Context()
	nodes, err := s.firewall.Nodes(ctx)
	if err != nil {
		s.firewallError(w, r, err)
		return
	}
	var ids []uuid.UUID
	for _, n := range nodes {
		switch {
		case in.All != nil && *in.All:
		case in.NodeIds != nil && len(*in.NodeIds) > 0:
			if !contains(uuidStrings(*in.NodeIds), n.NodeID.String()) {
				continue
			}
		case in.ClusterId != nil:
			if n.ClusterID == nil || *n.ClusterID != *in.ClusterId {
				continue
			}
		default:
			writeError(w, http.StatusBadRequest, "invalid", "kies nodes, een cluster of alle nodes")
			return
		}
		ids = append(ids, n.NodeID)
	}
	if len(ids) == 0 {
		writeError(w, http.StatusBadRequest, "invalid", "geen nodes met een agent gevonden")
		return
	}
	skipped, err := s.firewall.SetMode(ctx, events.User(p.User.ID), ids, string(in.Mode))
	if err != nil {
		s.firewallError(w, r, err)
		return
	}
	if skipped == nil {
		skipped = []string{}
	}
	s.kickFirewall()
	writeJSON(w, http.StatusOK, map[string]any{"skipped": skipped})
}

func (s *Server) GetFirewallNode(w http.ResponseWriter, r *http.Request, nodeId uuid.UUID) {
	d, err := s.firewall.Detail(r.Context(), nodeId)
	if err != nil {
		s.firewallError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) RefreshFirewallNode(w http.ResponseWriter, r *http.Request, nodeId uuid.UUID) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	n, err := s.firewall.Refresh(r.Context(), nodeId)
	if err != nil {
		s.firewallError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, n)
}

func (s *Server) ApplyFirewallNode(w http.ResponseWriter, r *http.Request, nodeId uuid.UUID) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	id, err := s.firewall.Apply(r.Context(), events.User(p.User.ID), nodeId)
	if err != nil {
		s.firewallError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"job_id": id})
}

func (s *Server) RollbackFirewallNode(w http.ResponseWriter, r *http.Request, nodeId uuid.UUID) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	if err := s.firewall.Rollback(r.Context(), events.User(p.User.ID), nodeId); err != nil {
		s.firewallError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) DisableFirewallNode(w http.ResponseWriter, r *http.Request, nodeId uuid.UUID) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	if err := s.firewall.DisableUfw(r.Context(), events.User(p.User.ID), nodeId); err != nil {
		s.firewallError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) ListFirewallTraffic(w http.ResponseWriter, r *http.Request, params gen.ListFirewallTrafficParams) {
	q := firewall.TrafficQuery{NodeID: params.NodeId, ClusterID: params.ClusterId, Port: params.Port}
	if params.Hours != nil {
		q.Hours = *params.Hours
	}
	if params.Limit != nil {
		q.Limit = *params.Limit
	}
	items, err := s.firewall.Traffic(r.Context(), q)
	if err != nil {
		s.firewallError(w, r, err)
		return
	}
	hourly, err := s.firewall.Hourly(r.Context(), params.NodeId, params.ClusterId)
	if err != nil {
		s.firewallError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "hourly": hourly})
}

func (s *Server) AddFirewallRule(w http.ResponseWriter, r *http.Request) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var in gen.FirewallRuleInput
	if !decode(w, r, &in) {
		return
	}
	rule := firewall.Rule{Action: string(in.Action), Proto: string(in.Proto), Port: in.Port, NodeID: in.NodeId, ClusterID: in.ClusterId}
	if in.Source != nil {
		rule.Source = *in.Source
	}
	if in.Comment != nil {
		rule.Comment = *in.Comment
	}
	uid := p.User.ID
	out, err := s.firewall.AddRule(r.Context(), events.User(p.User.ID), &uid, rule)
	if err != nil {
		s.firewallError(w, r, err)
		return
	}
	s.kickFirewall()
	writeJSON(w, http.StatusCreated, out)
}

func (s *Server) DeleteFirewallRule(w http.ResponseWriter, r *http.Request, ruleId uuid.UUID) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	if err := s.firewall.DeleteRule(r.Context(), events.User(p.User.ID), ruleId); err != nil {
		s.firewallError(w, r, err)
		return
	}
	s.kickFirewall()
	w.WriteHeader(http.StatusNoContent)
}
