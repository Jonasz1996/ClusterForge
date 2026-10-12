package httpapi

import (
	"net/http"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/components"
	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/httpapi/gen"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

// ListComponentDatabases geeft de databases en gebruikers in de database
// van een cluster. Ook voor viewers.
func (s *Server) ListComponentDatabases(w http.ResponseWriter, r *http.Request, clusterID, componentID uuid.UUID) {
	ctx := r.Context()
	if _, err := s.q.GetCluster(ctx, clusterID); err != nil {
		s.componentError(w, r, err)
		return
	}
	l, err := s.components.Databases(ctx, clusterID, componentID)
	if err != nil {
		s.componentError(w, r, err)
		return
	}
	out := gen.ComponentDatabases{
		Engine: gen.ComponentDatabasesEngine(l.Engine), Node: l.Node, Address: l.Address, Port: l.Port, Hosts: nonNil(l.Hosts),
		Databases: make([]gen.ComponentDatabase, 0, len(l.Databases)), Users: make([]gen.ComponentDatabaseUser, 0, len(l.Users)),
	}
	for _, d := range l.Databases {
		out.Databases = append(out.Databases, gen.ComponentDatabase{
			Name: d.Name, SizeBytes: d.SizeBytes, Tables: d.Tables, Connections: d.Connections, Users: nonNil(d.Users),
		})
	}
	for _, u := range l.Users {
		x := gen.ComponentDatabaseUser{Name: u.Name, Hosts: nonNil(u.Hosts), System: u.System, Global: nonNil(u.Global), Databases: []gen.ComponentDatabaseGrant{}}
		for _, g := range u.Databases {
			x.Databases = append(x.Databases, gen.ComponentDatabaseGrant{Database: g.Database, All: g.All, Privileges: nonNil(g.Privileges)})
		}
		out.Users = append(out.Users, x)
	}
	writeJSON(w, http.StatusOK, out)
}

// CreateComponentDatabase maakt een database met een eigen gebruiker; het
// wachtwoord staat één keer in het antwoord.
func (s *Server) CreateComponentDatabase(w http.ResponseWriter, r *http.Request, clusterID, componentID uuid.UUID) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var in gen.ComponentDatabaseInput
	if !decode(w, r, &in) {
		return
	}
	ctx := r.Context()
	if _, err := s.q.GetCluster(ctx, clusterID); err != nil {
		s.componentError(w, r, err)
		return
	}
	c, err := s.components.CreateDatabase(ctx, events.User(p.User.ID), clusterID, componentID, components.CreateDatabaseInput{Name: in.Name, User: deref(in.User)})
	if err != nil {
		s.componentError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusCreated, gen.ComponentDatabaseCreated{
		Database: c.Database, User: c.User, Password: c.Password, Hosts: nonNil(c.Hosts), Address: c.Address, Port: c.Port, Node: c.Node,
	})
}

// SetComponentUserPassword geeft een gebruiker een nieuw wachtwoord; het
// staat één keer in het antwoord.
func (s *Server) SetComponentUserPassword(w http.ResponseWriter, r *http.Request, clusterID, componentID uuid.UUID, user string) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var in gen.ComponentConfirmInput
	if r.ContentLength != 0 && !decode(w, r, &in) {
		return
	}
	ctx := r.Context()
	c, err := s.q.GetCluster(ctx, clusterID)
	if err != nil {
		s.componentError(w, r, err)
		return
	}
	if !confirmProd(w, p, c.Environment == store.EnvironmentProd, c.Slug, in.Confirm, "een nieuw wachtwoord geven") {
		return
	}
	np, err := s.components.SetPassword(ctx, events.User(p.User.ID), clusterID, componentID, user)
	if err != nil {
		s.componentError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, gen.ComponentUserPassword{User: np.User, Password: np.Password, Hosts: nonNil(np.Hosts)})
}

// DropComponentDatabase haalt een database weg, met haar naam ter
// bevestiging.
func (s *Server) DropComponentDatabase(w http.ResponseWriter, r *http.Request, clusterID, componentID uuid.UUID, name string) {
	s.dropInComponent(w, r, clusterID, componentID, "een database weghalen", func(p events.Actor, confirm string) error {
		return s.components.DropDatabase(r.Context(), p, clusterID, componentID, name, confirm)
	})
}

// DropComponentUser haalt een gebruiker weg, met zijn naam ter
// bevestiging.
func (s *Server) DropComponentUser(w http.ResponseWriter, r *http.Request, clusterID, componentID uuid.UUID, user string) {
	s.dropInComponent(w, r, clusterID, componentID, "een gebruiker weghalen", func(p events.Actor, confirm string) error {
		return s.components.DropUser(r.Context(), p, clusterID, componentID, user, confirm)
	})
}

func (s *Server) dropInComponent(w http.ResponseWriter, r *http.Request, clusterID, componentID uuid.UUID, what string, drop func(events.Actor, string) error) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var in gen.ComponentDropInput
	if !decode(w, r, &in) {
		return
	}
	c, err := s.q.GetCluster(r.Context(), clusterID)
	if err != nil {
		s.componentError(w, r, err)
		return
	}
	if !confirmProd(w, p, c.Environment == store.EnvironmentProd, c.Slug, in.Confirm, what) {
		return
	}
	if err := drop(events.User(p.User.ID), in.Name); err != nil {
		s.componentError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
