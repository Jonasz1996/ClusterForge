package httpapi

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/aiverslag"
	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// AI-verslag via de agent (fase 38), buiten de OpenAPI. De antwoorden zijn
// de structs van internal/aiverslag; de webinterface typt ze in
// web/src/lib/aiverslag.ts.

func (s *Server) aivError(w http.ResponseWriter, err error) {
	var ve aiverslag.ValidationError
	switch {
	case errors.As(err, &ve):
		writeError(w, http.StatusBadRequest, "invalid", ve.Msg)
	case errors.Is(err, aiverslag.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", err.Error())
	default:
		s.log.Warn("AI-verslag", "err", err)
		writeError(w, http.StatusBadGateway, "upstream", err.Error())
	}
}

func aivNode(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, "node"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "ongeldig node-id")
		return uuid.Nil, false
	}
	return id, true
}

func (s *Server) listAIV(w http.ResponseWriter, r *http.Request) {
	list, err := s.aiverslag.Instances(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": list, "protocol": protocol.AIVSince})
}

func (s *Server) aivOverview(w http.ResponseWriter, r *http.Request) {
	id, ok := aivNode(w, r)
	if !ok {
		return
	}
	o, err := s.aiverslag.Overview(r.Context(), id)
	if err != nil {
		s.aivError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, o)
}

// aivGet leest een pad: ?path=/api/…&andere=parameters.
func (s *Server) aivGet(w http.ResponseWriter, r *http.Request) {
	id, ok := aivNode(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	params := map[string]string{}
	for k, v := range q {
		if k != "path" && len(v) > 0 {
			params[k] = v[0]
		}
	}
	data, err := s.aiverslag.Get(r.Context(), id, q.Get("path"), params)
	if err != nil {
		s.aivError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": data})
}

func (s *Server) aivCall(w http.ResponseWriter, r *http.Request) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	id, ok := aivNode(w, r)
	if !ok {
		return
	}
	var in aiverslag.Call
	if !decode(w, r, &in) {
		return
	}
	prep, err := s.aiverslag.Prepare(r.Context(), id, in)
	if err != nil {
		s.aivError(w, err)
		return
	}
	if prep.Rule.Firewall && p.User.TotpEnabledAt == nil {
		writeError(w, http.StatusForbidden, "totp_required", "een fix van AI-verslag verandert de firewall; dat kan alleen een beheerder met tweestapsverificatie (zet die aan bij Instellingen)")
		return
	}
	if prep.Rule.Direct {
		data, err := s.aiverslag.CallNow(r.Context(), events.User(p.User.ID), prep)
		if err != nil {
			s.aivError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": data})
		return
	}
	j, err := s.aiverslag.StartCall(r.Context(), events.User(p.User.ID), prep)
	if err != nil {
		s.aivError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, toAPIJob(j, &p.User.Username))
}
