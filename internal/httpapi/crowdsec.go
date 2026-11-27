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
	case errors.Is(err, crowdsec.ErrNotFound), errors.Is(err, crowdsec.ErrListNotFound):
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
	t := crowdsec.BanTarget{ClusterID: in.ClusterId, Central: in.Central != nil && *in.Central,
		Permanent: in.Permanent != nil && *in.Permanent}
	uid := p.User.ID
	t.UserID = &uid
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

// Centrale blocklists (fase 47).

func (s *Server) ListCrowdSecLists(w http.ResponseWriter, r *http.Request) {
	items, err := s.crowdsec.Lists(r.Context())
	if err != nil {
		s.crowdSecError(w, r, err)
		return
	}
	if items == nil {
		items = []crowdsec.List{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "catalog": crowdsec.PublicCatalog})
}

func listInput(in gen.CrowdSecListInput) crowdsec.ListInput {
	out := crowdsec.ListInput{Enabled: in.Enabled, ClusterID: in.ClusterId}
	if in.Source != nil {
		out.Source = *in.Source
	}
	if in.Url != nil {
		out.URL = *in.Url
	}
	if in.Name != nil {
		out.Name = *in.Name
	}
	if in.Scope != nil {
		out.Scope = string(*in.Scope)
	}
	if in.RefreshMinutes != nil {
		out.RefreshMinutes = *in.RefreshMinutes
	}
	return out
}

func (s *Server) AddCrowdSecList(w http.ResponseWriter, r *http.Request) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var in gen.CrowdSecListInput
	if !decode(w, r, &in) {
		return
	}
	l, err := s.crowdsec.AddPublic(r.Context(), events.User(p.User.ID), listInput(in))
	if err != nil {
		s.crowdSecError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, l)
}

func (s *Server) PreviewCrowdSecList(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	var in gen.CrowdSecListInput
	if !decode(w, r, &in) {
		return
	}
	li := listInput(in)
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	d, err := s.crowdsec.PreviewSource(ctx, li.Source, li.URL)
	if err != nil {
		s.crowdSecError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) UpdateCrowdSecList(w http.ResponseWriter, r *http.Request, listId uuid.UUID) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var in gen.CrowdSecListInput
	if !decode(w, r, &in) {
		return
	}
	l, err := s.crowdsec.UpdateList(r.Context(), events.User(p.User.ID), listId, listInput(in))
	if err != nil {
		s.crowdSecError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, l)
}

func (s *Server) DeleteCrowdSecList(w http.ResponseWriter, r *http.Request, listId uuid.UUID) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	if err := s.crowdsec.DeleteList(r.Context(), events.User(p.User.ID), listId); err != nil {
		s.crowdSecError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) ListCrowdSecListEntries(w http.ResponseWriter, r *http.Request, listId uuid.UUID, params gen.ListCrowdSecListEntriesParams) {
	q, limit := "", 0
	if params.Q != nil {
		q = *params.Q
	}
	if params.Limit != nil {
		limit = *params.Limit
	}
	items, total, err := s.crowdsec.Entries(r.Context(), listId, q, limit)
	if err != nil {
		s.crowdSecError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": total})
}

func (s *Server) ChangeCrowdSecListEntries(w http.ResponseWriter, r *http.Request, listId uuid.UUID) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var in gen.CrowdSecListEntriesInput
	if !decode(w, r, &in) {
		return
	}
	var add []crowdsec.EntryInput
	if in.Add != nil {
		for _, a := range *in.Add {
			e := crowdsec.EntryInput{Value: a.Value}
			if a.Reason != nil {
				e.Reason = *a.Reason
			}
			if a.ExpiresHours != nil {
				if *a.ExpiresHours < 0 || *a.ExpiresHours > 87600 {
					writeError(w, http.StatusBadRequest, "invalid", "duur is 0 (nooit) tot 10 jaar")
					return
				}
				e.ExpiresIn = time.Duration(*a.ExpiresHours) * time.Hour
			}
			add = append(add, e)
		}
	}
	var remove []string
	if in.Remove != nil {
		remove = *in.Remove
	}
	uid := p.User.ID
	d, err := s.crowdsec.ChangeEntries(r.Context(), events.User(p.User.ID), &uid, listId, add, remove, in.DryRun != nil && *in.DryRun)
	if err != nil {
		s.crowdSecError(w, r, err)
		return
	}
	if d.Applied && s.soc != nil {
		s.soc.Forget()
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) RefreshCrowdSecList(w http.ResponseWriter, r *http.Request, listId uuid.UUID) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	d, err := s.crowdsec.RefreshList(ctx, events.User(p.User.ID), listId)
	if err != nil {
		s.crowdSecError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) ListCrowdSecListVersions(w http.ResponseWriter, r *http.Request, listId uuid.UUID) {
	items, err := s.crowdsec.Versions(r.Context(), listId)
	if err != nil {
		s.crowdSecError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) RollbackCrowdSecList(w http.ResponseWriter, r *http.Request, listId uuid.UUID) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var in gen.CrowdSecRollbackInput
	if !decode(w, r, &in) {
		return
	}
	uid := p.User.ID
	d, err := s.crowdsec.Rollback(r.Context(), events.User(p.User.ID), &uid, listId, in.VersionId, in.DryRun != nil && *in.DryRun)
	if err != nil {
		s.crowdSecError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) UpdateCrowdSecAutoRules(w http.ResponseWriter, r *http.Request) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var in gen.CrowdSecAutoRules
	if !decode(w, r, &in) {
		return
	}
	uid := p.User.ID
	set, err := s.crowdsec.UpdateAutoRules(r.Context(), events.User(p.User.ID), &uid, crowdsec.AutoRules{
		Enabled: in.Enabled, MultiNodes: in.MultiNodes, MultiWindowMinutes: in.MultiWindowMinutes, RepeatAlerts: in.RepeatAlerts,
		RepeatWindowHours: in.RepeatWindowHours, DurationHours: in.DurationHours, Escalate: in.Escalate, MaxHours: in.MaxHours,
	})
	if err != nil {
		s.crowdSecError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, set)
}

func (s *Server) UpdateCrowdSecNotify(w http.ResponseWriter, r *http.Request) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var in gen.CrowdSecNotifyInput
	if !decode(w, r, &in) {
		return
	}
	var chans []crowdsec.ChannelInput
	for _, c := range in.Channels {
		ci := crowdsec.ChannelInput{Kind: string(c.Kind)}
		if c.Id != nil {
			ci.ID = *c.Id
		}
		if c.Name != nil {
			ci.Name = *c.Name
		}
		if c.Url != nil {
			ci.URL = *c.Url
		}
		if c.Token != nil {
			ci.Token = *c.Token
		}
		chans = append(chans, ci)
	}
	uid := p.User.ID
	set, err := s.crowdsec.UpdateNotify(r.Context(), events.User(p.User.ID), &uid, crowdsec.NotifySettings{
		AutoBlock: in.AutoBlock, NewAlerts: in.NewAlerts, AlertBurst: in.AlertBurst, NodeProblems: in.NodeProblems,
		ListProblems: in.ListProblems, ManualBans: in.ManualBans,
	}, chans)
	if err != nil {
		s.crowdSecError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, set)
}

func (s *Server) TestCrowdSecNotify(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	res, err := s.crowdsec.TestNotify(r.Context())
	if err != nil {
		s.crowdSecError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, gen.CrowdSecNotifyTest{Results: res})
}

func (s *Server) SetCrowdSecConsoleOff(w http.ResponseWriter, r *http.Request) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var in gen.CrowdSecConsoleOffInput
	if !decode(w, r, &in) {
		return
	}
	uid := p.User.ID
	set, err := s.crowdsec.DisconnectConsole(r.Context(), events.User(p.User.ID), &uid, in.On)
	if err != nil {
		s.crowdSecError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, set)
}
