package httpapi

import (
	"encoding/base64"
	"net/http"

	"github.com/Jonasz1996/clusterforge/internal/httpapi/gen"
)

// Sessies van de ingelogde gebruiker (Instellingen → Sessies): zien waar je
// ingelogd bent en een vergeten sessie, bv. op een andere pc, beëindigen.
// Het kenmerk is de hash van het token, dus niet bruikbaar als cookie.

func (s *Server) ListMySessions(w http.ResponseWriter, r *http.Request) {
	p, _ := principalFrom(r.Context())
	rows, err := s.auth.Sessions(r.Context(), p)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	items := make([]gen.UserSession, 0, len(rows))
	for _, x := range rows {
		items = append(items, gen.UserSession{
			Id: base64.RawURLEncoding.EncodeToString(x.ID), Ip: x.IP, UserAgent: x.UserAgent,
			CreatedAt: x.CreatedAt, LastSeenAt: x.LastSeenAt, ExpiresAt: x.ExpiresAt, Current: x.Current,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "max_age_hours": int(s.cfg.SessionMaxAge.Hours())})
}

func (s *Server) EndMySession(w http.ResponseWriter, r *http.Request, id string) {
	p, _ := principalFrom(r.Context())
	raw, err := base64.RawURLEncoding.DecodeString(id)
	if err != nil || len(raw) != 32 {
		writeError(w, http.StatusNotFound, "not_found", "sessie niet gevonden")
		return
	}
	ok, err := s.auth.EndSession(r.Context(), p, raw)
	if s.authError(w, r, err) {
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "sessie niet gevonden of al beëindigd")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) EndOtherSessions(w http.ResponseWriter, r *http.Request) {
	p, _ := principalFrom(r.Context())
	n, err := s.auth.EndOtherSessions(r.Context(), p)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int64{"ended": n})
}
