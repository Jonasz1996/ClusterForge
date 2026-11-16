package httpapi

import (
	"net/http"

	"github.com/Jonasz1996/clusterforge/internal/httpapi/gen"
	"github.com/Jonasz1996/clusterforge/internal/soc"
)

// SOC (fase 25): het beveiligingsoverzicht. Het antwoord is soc.Overview;
// de JSON volgt het schema SocOverview in openapi.yaml.

func (s *Server) GetSoc(w http.ResponseWriter, r *http.Request, params gen.GetSocParams) {
	if s.soc == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Het beveiligingsoverzicht is niet beschikbaar.")
		return
	}
	win := soc.Window24h
	if params.Window != nil {
		win = soc.ParseWindow(string(*params.Window))
	}
	o, err := s.soc.Overview(r.Context(), win)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, o)
}
