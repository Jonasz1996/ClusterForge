package httpapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/crowdsec"
	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/httpapi/gen"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// CrowdSec (fase 14). De antwoorden zijn de structs van internal/crowdsec;
// hun JSON volgt de schema's CrowdSec* in openapi.yaml.

func (s *Server) crowdSecError(w http.ResponseWriter, r *http.Request, err error) {
	var fe *crowdsec.FieldError
	switch {
	case errors.As(err, &fe):
		writeError(w, http.StatusBadRequest, "invalid", fe.Msg)
	case errors.Is(err, crowdsec.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", err.Error())
	default:
		s.internalError(w, r, err)
	}
}

func (s *Server) GetCrowdSec(w http.ResponseWriter, r *http.Request) {
	o, err := s.crowdsec.Overview(r.Context())
	if err != nil {
		s.crowdSecError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, o)
}

func (s *Server) UpdateCrowdSecSettings(w http.ResponseWriter, r *http.Request) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var in gen.CrowdSecSettingsInput
	if !decode(w, r, &in) {
		return
	}
	uid := p.User.ID
	set, err := s.crowdsec.UpdateSettings(r.Context(), events.User(p.User.ID), &uid, crowdsec.Settings{
		AutoApply: in.AutoApply, BouncerDefault: in.BouncerDefault, ShareBans: string(in.ShareBans), Whitelist: in.Whitelist,
	})
	if err != nil {
		s.crowdSecError(w, r, err)
		return
	}
	s.kickCrowdSec()
	writeJSON(w, http.StatusOK, set)
}

// kickCrowdSec draait meteen een ronde, zodat een nieuwe instelling of
// node niet een minuut op zich laat wachten.
func (s *Server) kickCrowdSec() {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if err := s.crowdsec.Tick(ctx); err != nil {
			s.log.Warn("CrowdSec-ronde mislukt", "err", err)
		}
	}()
}

func (s *Server) SetCrowdSecNodes(w http.ResponseWriter, r *http.Request) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var in gen.CrowdSecNodesInput
	if !decode(w, r, &in) {
		return
	}
	ctx := r.Context()
	nodes, err := s.crowdsec.Nodes(ctx)
	if err != nil {
		s.crowdSecError(w, r, err)
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
		if !in.Managed && !n.Enabled {
			continue
		}
		ids = append(ids, n.NodeID)
	}
	if len(ids) == 0 {
		writeError(w, http.StatusBadRequest, "invalid", "geen nodes met een agent gevonden")
		return
	}
	skipped := []string{}
	if in.Managed {
		sk, err := s.crowdsec.Enable(ctx, events.User(p.User.ID), ids, in.Bouncer)
		if err != nil {
			s.crowdSecError(w, r, err)
			return
		}
		skipped = append(skipped, sk...)
	} else if err := s.crowdsec.Disable(ctx, events.User(p.User.ID), ids); err != nil {
		s.crowdSecError(w, r, err)
		return
	}
	s.kickCrowdSec()
	writeJSON(w, http.StatusOK, map[string]any{"skipped": skipped})
}

func uuidStrings(ids []uuid.UUID) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = id.String()
	}
	return out
}

func (s *Server) GetCrowdSecNode(w http.ResponseWriter, r *http.Request, nodeId uuid.UUID) {
	n, err := s.crowdsec.Node(r.Context(), nodeId)
	if err != nil {
		s.crowdSecError(w, r, err)
		return
	}
	alerts, err := s.crowdsec.NodeAlerts(r.Context(), nodeId, 20)
	if err != nil {
		s.crowdSecError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"node": n, "alerts": alerts})
}

func (s *Server) ConfigureCrowdSecNode(w http.ResponseWriter, r *http.Request, nodeId uuid.UUID) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var in gen.CrowdSecNodeInput
	if !decode(w, r, &in) {
		return
	}
	n, err := s.crowdsec.Configure(r.Context(), events.User(p.User.ID), nodeId, in.Bouncer, in.ExtraProfiles, in.DisabledProfiles)
	if err != nil {
		s.crowdSecError(w, r, err)
		return
	}
	s.kickCrowdSec()
	writeJSON(w, http.StatusOK, n)
}

func (s *Server) ApplyCrowdSecNode(w http.ResponseWriter, r *http.Request, nodeId uuid.UUID) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var in gen.ApplyCrowdSecNodeJSONBody
	if r.ContentLength != 0 && !decode(w, r, &in) {
		return
	}
	id, err := s.crowdsec.Apply(r.Context(), events.User(p.User.ID), nodeId, in.Remove != nil && *in.Remove)
	if err != nil {
		s.crowdSecError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"job_id": id})
}

func (s *Server) RefreshCrowdSecNode(w http.ResponseWriter, r *http.Request, nodeId uuid.UUID) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	n, err := s.crowdsec.Refresh(r.Context(), nodeId)
	if err != nil {
		s.crowdSecError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, n)
}

func (s *Server) ListCrowdSecAlerts(w http.ResponseWriter, r *http.Request, params gen.ListCrowdSecAlertsParams) {
	f := crowdsec.AlertFilter{NodeID: params.NodeId, ClusterID: params.ClusterId}
	if params.Scenario != nil {
		f.Scenario = *params.Scenario
	}
	if params.Value != nil {
		f.Value = *params.Value
	}
	if params.Limit != nil {
		f.Limit = *params.Limit
	}
	items, err := s.crowdsec.Alerts(r.Context(), f)
	if err != nil {
		s.crowdSecError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) ListCrowdSecDecisions(w http.ResponseWriter, r *http.Request) {
	items, err := s.crowdsec.Bans(r.Context())
	if err != nil {
		s.crowdSecError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) DecideCrowdSec(w http.ResponseWriter, r *http.Request) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var in gen.CrowdSecDecisionInput
	if !decode(w, r, &in) {
		return
	}
	d := protocol.CrowdSecDecision{Delete: in.Delete != nil && *in.Delete, Scope: string(in.Scope), Value: in.Value, Type: "ban"}
	if in.Duration != nil {
		d.Duration = *in.Duration
	}
	if in.Reason != nil {
		d.Reason = *in.Reason
	}
	if d.Reason == "" && !d.Delete {
		d.Reason = "handmatig via ClusterForge"
	}
	t := crowdsec.BanTarget{ClusterID: in.ClusterId}
	if in.NodeIds != nil {
		t.NodeIDs = *in.NodeIds
	}
	res, err := s.crowdsec.Ban(r.Context(), events.User(p.User.ID), d, t)
	if err != nil {
		s.crowdSecError(w, r, err)
		return
	}
	if s.soc != nil {
		s.soc.Forget()
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) SetCrowdSecConsoleKey(w http.ResponseWriter, r *http.Request) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var in gen.CrowdSecConsoleKeyInput
	if !decode(w, r, &in) {
		return
	}
	uid := p.User.ID
	set, err := s.crowdsec.SetConsoleKey(r.Context(), events.User(p.User.ID), &uid, in.Key)
	if err != nil {
		s.crowdSecError(w, r, err)
		return
	}
	s.kickCrowdSec()
	writeJSON(w, http.StatusOK, set)
}

func (s *Server) EnrollCrowdSecConsole(w http.ResponseWriter, r *http.Request) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var in gen.CrowdSecEnrollInput
	if !decode(w, r, &in) {
		return
	}
	n, err := s.crowdsec.ConsoleEnroll(r.Context(), events.User(p.User.ID), in.NodeId)
	if err != nil {
		s.crowdSecError(w, r, err)
		return
	}
	s.kickCrowdSec()
	writeJSON(w, http.StatusOK, gen.CrowdSecEnrollResult{Nodes: n})
}
