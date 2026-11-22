package httpapi

import (
	"errors"
	"net/http"
	"regexp"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Jonasz1996/clusterforge/internal/deploy"
	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/httpapi/gen"
	"github.com/Jonasz1996/clusterforge/internal/templates"
)

// De kaart Verbinding (fase 9): hoe een app de database van een
// samengesteld cluster bereikt. De gebruikers en databases komen uit de
// DATABASE- en DBUSER-regels van de recepten; een wachtwoord toont
// ClusterForge alleen na je code of wachtwoord, en dat komt in het logboek.

var paramRefRe = regexp.MustCompile(`^\{\{\s*\.params\.([a-z0-9_]+)\s*\}\}$`)

func connectionUsers(tpl *templates.Template) []gen.ConnectionUser {
	users := []gen.ConnectionUser{}
	if tpl.Database == nil {
		return users
	}
	for _, st := range tpl.Database.Steps {
		if st.Kind != "db_user" {
			continue
		}
		m := paramRefRe.FindStringSubmatch(st.Password)
		if m == nil {
			continue
		}
		users = append(users, gen.ConnectionUser{Name: st.User, Database: st.GrantOn, Secret: m[1]})
	}
	return users
}

func (s *Server) GetClusterConnection(w http.ResponseWriter, r *http.Request, clusterID uuid.UUID) {
	out := gen.ClusterConnection{Databases: []string{}, Users: []gen.ConnectionUser{}, Port: 3306}
	d, err := s.deploy.Desired(r.Context(), clusterID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		writeError(w, http.StatusNotFound, "not_found", "cluster niet gevonden")
		return
	case errors.Is(err, deploy.ErrNoSpec):
		out.Reason = "dit cluster komt niet uit een template"
		writeJSON(w, http.StatusOK, out)
		return
	case err != nil:
		out.Reason = err.Error()
		writeJSON(w, http.StatusOK, out)
		return
	}
	db := d.Template.Database
	if db == nil || len(db.Steps) == 0 || db.Host == "" {
		out.Reason = "de template van dit cluster maakt zelf geen database voor een app"
		writeJSON(w, http.StatusOK, out)
		return
	}
	c, err := d.Context()
	if err == nil {
		out.Host, err = d.Template.DBHost(c)
	}
	if err != nil {
		out.Reason = err.Error()
		writeJSON(w, http.StatusOK, out)
		return
	}
	out.Available, out.Engine, out.Role = true, db.Engine, db.Role
	for _, st := range db.Steps {
		if st.Kind == "db_database" {
			out.Databases = append(out.Databases, st.Database)
		}
	}
	out.Users = connectionUsers(d.Template)
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) RevealClusterConnectionSecret(w http.ResponseWriter, r *http.Request, clusterID uuid.UUID) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var in gen.ConnectionRevealInput
	if !decode(w, r, &in) {
		return
	}
	ctx := r.Context()
	d, err := s.deploy.Desired(ctx, clusterID)
	if errors.Is(err, pgx.ErrNoRows) || errors.Is(err, deploy.ErrNoSpec) {
		writeError(w, http.StatusNotFound, "not_found", "cluster niet gevonden of niet uit een template")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	// Alleen de wachtwoorden van de databasegebruikers, geen ander geheim
	// van het cluster.
	user := ""
	for _, u := range connectionUsers(d.Template) {
		if u.Secret == in.Secret {
			user = u.Name
		}
	}
	if user == "" {
		writeError(w, http.StatusNotFound, "not_found", "geen databasegebruiker met dit geheim")
		return
	}
	if s.authError(w, r, s.auth.Confirm(ctx, p, deref(in.Password), deref(in.TotpCode), "connection_secret")) {
		return
	}
	values, err := s.deploy.OpenSecrets(ctx, clusterID, []string{in.Secret})
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if err := s.ev.Write(ctx, s.q, events.Event{
		Actor: events.User(p.User.ID), SubjectType: "cluster", SubjectID: clusterID.String(), ClusterID: &clusterID,
		Action: "cluster.secret_revealed", Payload: map[string]any{"secret": in.Secret, "user": user},
	}); err != nil {
		s.internalError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, gen.ConnectionSecret{Secret: in.Secret, Value: values[in.Secret]})
}
