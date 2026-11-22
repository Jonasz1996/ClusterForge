package httpapi

import (
	"errors"
	"net/http"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/agentbus"
	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/httpapi/gen"
	"github.com/Jonasz1996/clusterforge/internal/jobs"
	"github.com/Jonasz1996/clusterforge/internal/stacks"
)

// Docker-stacks (fase 21). De antwoorden zijn de structs van
// internal/stacks; hun JSON volgt de schema's Stack* in openapi.yaml.

func (s *Server) stackError(w http.ResponseWriter, r *http.Request, err error) {
	var fe *stacks.FieldError
	var be jobs.BusyError
	switch {
	case errors.As(err, &fe):
		writeError(w, http.StatusBadRequest, "invalid", fe.Msg)
	case errors.Is(err, stacks.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "niet gevonden")
	case errors.As(err, &be):
		writeError(w, http.StatusConflict, "busy", be.Error())
	case errors.Is(err, agentbus.ErrAgentOffline), errors.Is(err, agentbus.ErrNoAnswer):
		writeError(w, http.StatusBadRequest, "agent", "de agent antwoordt niet: "+err.Error())
	default:
		s.internalError(w, r, err)
	}
}

func (s *Server) GetClusterStacks(w http.ResponseWriter, r *http.Request, clusterID uuid.UUID) {
	o, err := s.stacks.Overview(r.Context(), clusterID)
	if err != nil {
		s.stackError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, o)
}

func (s *Server) ImportClusterStacks(w http.ResponseWriter, r *http.Request, clusterID uuid.UUID) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var in gen.StackImportInput
	if !decode(w, r, &in) {
		return
	}
	var items []stacks.ImportItem
	for _, it := range in.Items {
		item := stacks.ImportItem{Name: it.Name}
		if it.Placement != nil {
			item.Placement = string(*it.Placement)
		}
		if it.NodeIds != nil {
			item.NodeIDs = *it.NodeIds
		}
		if it.Critical != nil {
			item.Critical = *it.Critical
		}
		if it.SyncPaths != nil {
			item.SyncPaths = *it.SyncPaths
		}
		items = append(items, item)
	}
	uid := p.User.ID
	res, err := s.stacks.Import(r.Context(), events.User(uid), &uid, clusterID, in.NodeId, items)
	if err != nil {
		s.stackError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": res})
}

func (s *Server) StartClusterStackJob(w http.ResponseWriter, r *http.Request, clusterID uuid.UUID) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var in gen.StackJobInput
	if !decode(w, r, &in) {
		return
	}
	o, err := s.q.GetCluster(r.Context(), clusterID)
	if err != nil {
		s.stackError(w, r, stacks.ErrNotFound)
		return
	}
	if string(in.Action) != stacks.ActionLists && !s.confirmProd(w, r, p, string(o.Environment) == "prod", o.Slug, in.Confirm, "containers beheren") {
		return
	}
	params := stacks.Params{Action: string(in.Action), NodeID: in.NodeId}
	if in.StackIds != nil {
		params.StackIDs = *in.StackIds
	}
	if in.Services != nil {
		params.Services = *in.Services
	}
	id, err := s.stacks.Enqueue(r.Context(), events.User(p.User.ID), clusterID, params)
	if err != nil {
		s.stackError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"job_id": id})
}

func (s *Server) GetClusterStackLogs(w http.ResponseWriter, r *http.Request, clusterID uuid.UUID, params gen.GetClusterStackLogsParams) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	tail := 300
	if params.Tail != nil {
		tail = *params.Tail
	}
	logs, err := s.stacks.Logs(r.Context(), clusterID, params.NodeId, params.Container, tail)
	if err != nil {
		var fe *stacks.FieldError
		if !errors.As(err, &fe) && !errors.Is(err, stacks.ErrNotFound) {
			writeError(w, http.StatusBadRequest, "agent", err.Error())
			return
		}
		s.stackError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"logs": logs})
}

func (s *Server) GetStack(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	d, err := s.stacks.Detail(r.Context(), id)
	if err != nil {
		s.stackError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) UpdateStack(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var in gen.StackUpdateInput
	if !decode(w, r, &in) {
		return
	}
	up := stacks.Update{Critical: in.Critical}
	if in.Files != nil {
		up.Files = []stacks.MaskedFile{}
		for _, f := range *in.Files {
			mf := stacks.MaskedFile{Path: f.Path, Content: f.Content}
			if f.Mode != nil {
				mf.Mode = uint32(*f.Mode)
			}
			up.Files = append(up.Files, mf)
		}
	}
	if in.ComposeFiles != nil {
		up.ComposeFiles = *in.ComposeFiles
	}
	if in.Placement != nil {
		up.Placement = string(*in.Placement)
	}
	if in.NodeIds != nil {
		up.NodeIDs = *in.NodeIds
	}
	if in.Desired != nil {
		up.Desired = string(*in.Desired)
	}
	if in.SyncPaths != nil {
		up.SyncPaths = *in.SyncPaths
	}
	if in.SyncSchedule != nil {
		up.SyncSchedule = *in.SyncSchedule
	}
	if in.Notes != nil {
		up.Notes = *in.Notes
	} else if cur, err := s.stacks.Get(r.Context(), id); err == nil {
		up.Notes = cur.Notes
	}
	if in.Note != nil {
		up.Note = *in.Note
	}
	uid := p.User.ID
	st, err := s.stacks.Save(r.Context(), events.User(uid), &uid, id, up)
	if err != nil {
		s.stackError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) DeleteStack(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	jid, err := s.stacks.Delete(r.Context(), events.User(p.User.ID), id)
	if err != nil {
		s.stackError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"job_id": jid})
}
