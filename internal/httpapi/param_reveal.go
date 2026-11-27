package httpapi

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Jonasz1996/clusterforge/internal/events"
)

// revealParam toont een versleutelde parameter van een cluster uit een
// template (fase 48, 4.4), zoals het beheerderswachtwoord van de
// RustDesk-API. Net als het wachtwoord van een databasegebruiker: alleen
// een beheerder, na je code (of wachtwoord zonder tweestapsverificatie),
// en het komt in het logboek. Buiten de OpenAPI, zoals /replacements.
func (s *Server) revealParam(w http.ResponseWriter, r *http.Request) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	clusterID, err := uuid.Parse(chi.URLParam(r, "clusterId"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "validation", "ongeldig id")
		return
	}
	var in struct {
		Name     string `json:"name"`
		Password string `json:"password"`
		TotpCode string `json:"totp_code"`
	}
	if !decode(w, r, &in) {
		return
	}
	ctx := r.Context()
	h, err := s.deploy.History(ctx, clusterID)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "not_found", "cluster niet gevonden")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	label := ""
	for _, hp := range h.Params {
		if hp.Secret && hp.Name == in.Name {
			label = hp.Label
		}
	}
	if label == "" {
		writeError(w, http.StatusNotFound, "not_found", "geen versleutelde parameter met deze naam")
		return
	}
	if s.authError(w, r, s.auth.Confirm(ctx, p, in.Password, in.TotpCode, "param_secret")) {
		return
	}
	values, err := s.deploy.OpenSecrets(ctx, clusterID, []string{in.Name})
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	v, found := values[in.Name]
	if !found {
		writeError(w, http.StatusNotFound, "not_found", "deze parameter heeft geen opgeslagen waarde")
		return
	}
	if err := s.ev.Write(ctx, s.q, events.Event{
		Actor: events.User(p.User.ID), SubjectType: "cluster", SubjectID: clusterID.String(), ClusterID: &clusterID,
		Action: "cluster.secret_revealed", Payload: map[string]any{"secret": in.Name, "param": label},
	}); err != nil {
		s.internalError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]string{"name": in.Name, "value": v})
}
