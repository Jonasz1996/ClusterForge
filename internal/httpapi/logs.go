package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/agentbus"
	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/httpapi/gen"
	"github.com/Jonasz1996/clusterforge/internal/logs"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// Logs (fase 23): bekijken, filteren en vergelijken van de logs op de
// nodes. Alleen voor beheerders: logs bevatten vaak gevoelige gegevens, ook
// na het maskeren van geheimen.

func (s *Server) logError(w http.ResponseWriter, r *http.Request, err error) {
	var fe *logs.FieldError
	switch {
	case errors.As(err, &fe):
		writeError(w, http.StatusBadRequest, "invalid", fe.Msg)
	case errors.Is(err, logs.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "niet gevonden")
	case errors.Is(err, agentbus.ErrAgentOffline), errors.Is(err, agentbus.ErrNoAnswer):
		writeError(w, http.StatusBadRequest, "agent", "de agent antwoordt niet: "+err.Error())
	default:
		s.internalError(w, r, err)
	}
}

func (s *Server) ListLogNodes(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	nodes, err := s.logs.Nodes(r.Context())
	if err != nil {
		s.logError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"nodes": nodes, "protocol": protocol.LogsSince})
}

func (s *Server) GetNodeLogSources(w http.ResponseWriter, r *http.Request, nodeID uuid.UUID) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	src, err := s.logs.Sources(r.Context(), nodeID)
	if err != nil {
		var fe *logs.FieldError
		if !errors.As(err, &fe) && !errors.Is(err, logs.ErrNotFound) && !errors.Is(err, agentbus.ErrAgentOffline) && !errors.Is(err, agentbus.ErrNoAnswer) {
			// Een fout van de agent zelf.
			writeError(w, http.StatusBadRequest, "agent", err.Error())
			return
		}
		s.logError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, src)
}

func toSources(in []gen.LogSource) []logs.Source {
	out := make([]logs.Source, 0, len(in))
	for _, s := range in {
		out = append(out, logs.Source{NodeID: s.NodeId, Kind: string(s.Kind), Name: s.Name})
	}
	return out
}

func (s *Server) QueryLogs(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	var in gen.LogQueryInput
	if !decode(w, r, &in) {
		return
	}
	q := logs.Query{Sources: toSources(in.Sources)}
	f := &q.Filter
	if in.Since != nil {
		f.Since = *in.Since
	}
	if in.Until != nil {
		f.Until = *in.Until
	}
	if in.Pattern != nil {
		f.Pattern = *in.Pattern
	}
	if in.Exclude != nil {
		f.Exclude = *in.Exclude
	}
	f.Regex = in.Regex != nil && *in.Regex
	f.IgnoreCase = in.IgnoreCase != nil && *in.IgnoreCase
	if in.Levels != nil {
		for _, l := range *in.Levels {
			f.Levels = append(f.Levels, string(l))
		}
	}
	if in.Limit != nil {
		f.Limit = *in.Limit
	}
	res, err := s.logs.Query(r.Context(), q)
	if err != nil {
		s.logError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) ListLogViews(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	views, err := s.logs.Views(r.Context())
	if err != nil {
		s.logError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"views": views})
}

func viewInput(in gen.LogViewInput) logs.ViewInput {
	v := logs.ViewInput{Name: in.Name, Sources: toSources(in.Sources)}
	if in.ClusterId.IsSpecified() && !in.ClusterId.IsNull() {
		id := in.ClusterId.MustGet()
		v.ClusterID = &id
	}
	if in.Filter != nil {
		v.Filter, _ = json.Marshal(*in.Filter)
	}
	return v
}

func (s *Server) CreateLogView(w http.ResponseWriter, r *http.Request) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var in gen.LogViewInput
	if !decode(w, r, &in) {
		return
	}
	uid := p.User.ID
	v, err := s.logs.SaveView(r.Context(), events.User(uid), &uid, nil, viewInput(in))
	if err != nil {
		s.logError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, v)
}

func (s *Server) UpdateLogView(w http.ResponseWriter, r *http.Request, viewID uuid.UUID) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var in gen.LogViewInput
	if !decode(w, r, &in) {
		return
	}
	uid := p.User.ID
	v, err := s.logs.SaveView(r.Context(), events.User(uid), &uid, &viewID, viewInput(in))
	if err != nil {
		s.logError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) DeleteLogView(w http.ResponseWriter, r *http.Request, viewID uuid.UUID) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	if err := s.logs.DeleteView(r.Context(), events.User(p.User.ID), viewID); err != nil {
		s.logError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
