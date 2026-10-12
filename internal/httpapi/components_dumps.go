package httpapi

import (
	"errors"
	"net/http"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/components"
	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/httpapi/gen"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

func toAPIDump(d store.DbDump) gen.ComponentDump {
	return gen.ComponentDump{
		Id: d.ID, Database: d.Database, Node: d.Hostname, File: d.File, Trigger: gen.ComponentDumpTrigger(d.Trigger),
		Status: gen.ComponentDumpStatus(d.Status), StartedAt: d.StartedAt, FinishedAt: nullableOf(d.FinishedAt),
		SizeBytes: d.SizeBytes, RawBytes: d.RawBytes, Tables: int(d.Tables), TablesExpected: int(d.TablesExpected),
		Message: d.Message, JobId: nullableOf(d.JobID), RestoreName: components.RestoreName(d.Database, d.StartedAt),
	}
}

func toAPIDumpPtr(d *store.DbDump) *gen.ComponentDump {
	if d == nil {
		return nil
	}
	x := toAPIDump(*d)
	return &x
}

func toAPIBackupSettings(b components.BackupSettings) gen.ComponentBackupSettings {
	return gen.ComponentBackupSettings{On: b.On, At: b.At, KeepDays: b.KeepDays, NextAt: nullableOf(b.NextAt)}
}

// ListComponentDumps geeft de dumps van de databases van een cluster. Ook
// voor viewers.
func (s *Server) ListComponentDumps(w http.ResponseWriter, r *http.Request, clusterID, componentID uuid.UUID) {
	o, err := s.components.Dumps(r.Context(), clusterID, componentID)
	if err != nil {
		s.componentError(w, r, err)
		return
	}
	out := gen.ComponentDumps{
		Settings: toAPIBackupSettings(o.Settings), Databases: make([]gen.ComponentDatabaseDumps, 0, len(o.Databases)),
		Dumps: make([]gen.ComponentDump, 0, len(o.Dumps)), Active: nullableOf[gen.ComponentDumpJob](nil),
	}
	for _, d := range o.Databases {
		out.Databases = append(out.Databases, gen.ComponentDatabaseDumps{
			Database: d.Database, Latest: nullableOf(toAPIDumpPtr(d.Latest)), Failed: nullableOf(toAPIDumpPtr(d.Failed)),
			Freshness: gen.ComponentDatabaseDumpsFreshness(d.Freshness),
		})
	}
	for _, d := range o.Dumps {
		out.Dumps = append(out.Dumps, toAPIDump(d))
	}
	if a := o.Active; a != nil {
		out.Active = nullableOf(&gen.ComponentDumpJob{JobId: a.ID, Kind: a.Kind, Title: a.Title})
	}
	writeJSON(w, http.StatusOK, out)
}

// CreateComponentDump maakt meteen een dump van een database.
func (s *Server) CreateComponentDump(w http.ResponseWriter, r *http.Request, clusterID, componentID uuid.UUID) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var in gen.ComponentDumpInput
	if !decode(w, r, &in) {
		return
	}
	j, err := s.components.DumpNow(r.Context(), events.User(p.User.ID), clusterID, componentID, in.Database)
	if err != nil {
		s.componentError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, toAPIJob(j, &p.User.Username))
}

// RestoreComponentDump zet een dump terug in een nieuwe database.
func (s *Server) RestoreComponentDump(w http.ResponseWriter, r *http.Request, clusterID, componentID, dumpID uuid.UUID) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var in gen.ComponentRestoreInput
	if r.ContentLength != 0 && !decode(w, r, &in) {
		return
	}
	j, target, err := s.components.RestoreDump(r.Context(), events.User(p.User.ID), clusterID, componentID, dumpID, deref(in.Target))
	if errors.Is(err, components.ErrDumpNotFound) {
		writeError(w, http.StatusNotFound, "not_found", err.Error())
		return
	}
	if err != nil {
		s.componentError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, gen.ComponentRestoreStarted{Job: toAPIJob(j, &p.User.Username), Target: target})
}

// SetComponentBackup stelt de nachtelijke dump in.
func (s *Server) SetComponentBackup(w http.ResponseWriter, r *http.Request, clusterID, componentID uuid.UUID) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var in gen.ComponentBackupSettings
	if !decode(w, r, &in) {
		return
	}
	b, err := s.components.SetBackup(r.Context(), events.User(p.User.ID), clusterID, componentID, in.On, in.At, in.KeepDays)
	if err != nil {
		s.componentError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toAPIBackupSettings(b))
}

// ListDatabaseBackups geeft per database van een cluster de nieuwste dump.
func (s *Server) ListDatabaseBackups(w http.ResponseWriter, r *http.Request) {
	all, err := s.components.DatabaseBackups(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out := gen.DatabaseBackups{Items: make([]gen.DatabaseBackup, 0, len(all))}
	for _, b := range all {
		out.Items = append(out.Items, gen.DatabaseBackup{
			ClusterId: b.ClusterID, ClusterName: b.ClusterName, ComponentId: b.ComponentID, Engine: b.Engine, Database: b.Database,
			Latest: nullableOf(toAPIDumpPtr(b.Latest)), Failed: nullableOf(toAPIDumpPtr(b.Failed)),
			Freshness: gen.DatabaseBackupFreshness(b.Freshness), Settings: toAPIBackupSettings(b.Settings),
		})
	}
	writeJSON(w, http.StatusOK, out)
}
