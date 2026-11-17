package httpapi

import (
	"errors"
	"net/http"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/httpapi/gen"
	"github.com/Jonasz1996/clusterforge/internal/jobs"
	"github.com/Jonasz1996/clusterforge/internal/publish"
)

// Publicatie via NPM en Authentik (fase 35). De antwoorden zijn de structs
// van internal/publish; de webinterface typt ze in web/src/lib/publish.ts.

func (s *Server) publishError(w http.ResponseWriter, err error) {
	var ve publish.ValidationError
	var busy jobs.BusyError
	switch {
	case errors.As(err, &ve):
		writeError(w, http.StatusBadRequest, "invalid", ve.Msg)
	case errors.Is(err, publish.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", err.Error())
	case errors.As(err, &busy):
		writeError(w, http.StatusConflict, "busy", "er loopt al een publicatietaak: "+busy.Title)
	default:
		s.log.Warn("Publicatie", "err", err)
		writeError(w, http.StatusBadGateway, "upstream", err.Error())
	}
}

func (s *Server) GetPublishSettings(w http.ResponseWriter, r *http.Request) {
	st, err := s.publish.Settings(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) SavePublishSettings(w http.ResponseWriter, r *http.Request) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var in publish.SettingsInput
	if !decode(w, r, &in) {
		return
	}
	st, err := s.publish.SaveSettings(r.Context(), events.User(p.User.ID), &p.User.ID, in)
	if err != nil {
		s.publishError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) TestPublishLink(w http.ResponseWriter, r *http.Request, what string) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	out, err := s.publish.Test(r.Context(), what)
	if err != nil {
		s.publishError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) GetPublishOverview(w http.ResponseWriter, r *http.Request) {
	o, err := s.publish.Overview(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, o)
}

func (s *Server) RefreshPublish(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	s.publish.Refresh(r.Context())
	s.GetPublishOverview(w, r)
}

func (s *Server) PlanPublish(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	var in publish.Action
	if !decode(w, r, &in) {
		return
	}
	pl, err := s.publish.Plan(r.Context(), in)
	if err != nil {
		s.publishError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, pl)
}

func (s *Server) StartPublish(w http.ResponseWriter, r *http.Request) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var in publish.Action
	if !decode(w, r, &in) {
		return
	}
	j, pl, err := s.publish.Start(r.Context(), events.User(p.User.ID), in)
	if err != nil {
		s.publishError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"job": toAPIJob(j, &p.User.Username), "plan": pl})
}

func (s *Server) AdoptPublishHosts(w http.ResponseWriter, r *http.Request) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var in struct {
		IDs []int `json:"ids"`
	}
	if !decode(w, r, &in) {
		return
	}
	n, err := s.publish.Adopt(r.Context(), events.User(p.User.ID), &p.User.ID, in.IDs)
	if err != nil {
		s.publishError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"adopted": n})
}

func (s *Server) AcceptPublishDrift(w http.ResponseWriter, r *http.Request, hostId int) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	if err := s.publish.Accept(r.Context(), events.User(p.User.ID), hostId); err != nil {
		s.publishError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) ReleasePublishHost(w http.ResponseWriter, r *http.Request, hostId int) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	if err := s.publish.Release(r.Context(), events.User(p.User.ID), hostId); err != nil {
		s.publishError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func windowParam[T ~string](w *T) string {
	if w == nil {
		return ""
	}
	return string(*w)
}

// GetPublishTraffic: verkeer van NPM (fase 39). Kijkers mogen het zien;
// de IP's van bezoekers zijn er ook voor kijkers, zoals bij CrowdSec.
func (s *Server) GetPublishTraffic(w http.ResponseWriter, r *http.Request, params gen.GetPublishTrafficParams) {
	v, err := s.publish.Traffic(r.Context(), windowParam(params.Window))
	if err != nil {
		s.publishError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// GetPublishLogins: logins en mislukte logins in Authentik (fase 39).
func (s *Server) GetPublishLogins(w http.ResponseWriter, r *http.Request, params gen.GetPublishLoginsParams) {
	v, err := s.publish.Logins(r.Context(), windowParam(params.Window))
	if err != nil {
		s.publishError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}
