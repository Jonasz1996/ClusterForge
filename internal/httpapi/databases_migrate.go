package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/oapi-codegen/nullable"

	"github.com/Jonasz1996/clusterforge/internal/components"
	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/httpapi/gen"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// GetDatabaseMigration geeft het plan om een database te verhuizen. Ook
// voor viewers: het verandert niets.
func (s *Server) GetDatabaseMigration(w http.ResponseWriter, r *http.Request, params gen.GetDatabaseMigrationParams) {
	p, err := s.components.MigrationPlan(r.Context(), params.Source, params.Database, params.Component)
	if err != nil {
		s.migrationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toAPIMigrationPlan(p))
}

// StartDatabaseMigrationTrial begint een proefverhuizing.
func (s *Server) StartDatabaseMigrationTrial(w http.ResponseWriter, r *http.Request) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var in gen.DatabaseMigrationInput
	if !decode(w, r, &in) {
		return
	}
	j, err := s.components.StartTrial(r.Context(), events.User(p.User.ID), in.Source, in.Database, in.ComponentId)
	if err != nil {
		s.migrationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, toAPIJob(j, &p.User.Username))
}

func (s *Server) migrationError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, components.ErrSourceNotFound) {
		writeError(w, http.StatusNotFound, "not_found", err.Error())
		return
	}
	s.componentError(w, r, err)
}

func toAPIMigrationPlan(p components.MigrationPlan) gen.DatabaseMigrationPlan {
	src := p.Source
	out := gen.DatabaseMigrationPlan{
		Source: gen.DatabaseMigrationSource{
			Key: src.Key, Name: src.Name, ClusterId: nullableOf(src.ClusterID), Engine: src.Engine, Kind: src.Kind, Version: src.Version,
			Summary: src.Summary, Servers: nonNil(src.Servers), Address: src.Address, Port: src.Port, Via: src.Via,
		},
		Database: p.Database, Info: nullable.NewNullNullable[gen.DatabaseMigrationInfo](),
		Clients: make([]gen.DatabaseUser, 0, len(p.Clients)), Targets: make([]gen.DatabaseMigrationOption, 0, len(p.Targets)),
		Target: nullable.NewNullNullable[gen.DatabaseMigrationTarget](), Reach: nullable.NewNullNullable[gen.DatabaseMigrationReach](),
		Findings: make([]gen.ComponentFinding, 0, len(p.Findings)), Blocked: p.Blocked, Runs: make([]gen.DatabaseMigrationRun, 0, len(p.Runs)),
		Active: nullable.NewNullNullable[gen.ComponentDumpJob](),
	}
	if src.Node != nil {
		out.Source.Node = src.Node.Hostname
	}
	if i := p.Info; i != nil {
		info := gen.DatabaseMigrationInfo{
			Version: i.Version, Charset: i.Charset, Collation: i.Collation, SizeBytes: i.SizeBytes, Tables: len(i.Tables),
			Views: i.Views, Routines: i.Routines, Triggers: i.Triggers, Events: i.Events,
		}
		for _, t := range i.Tables {
			info.Rows += t.Rows
		}
		out.Info = nullable.NewNullableWithValue(info)
	}
	for _, u := range p.Clients {
		out.Clients = append(out.Clients, gen.DatabaseUser{
			Key: u.Key(), Kind: u.Kind, ClusterId: nullableOf(u.ClusterID), NodeId: nullableOf(u.NodeID), Name: u.Name,
			Nodes: nonNil(u.Nodes), DbUsers: nonNil(u.DBUsers), Connections: u.Connections, LastSeen: u.LastSeen, Dependency: u.Dependency,
		})
	}
	for _, o := range p.Targets {
		out.Targets = append(out.Targets, gen.DatabaseMigrationOption{ClusterId: o.ClusterID, ClusterName: o.ClusterName, ComponentId: o.ComponentID, Suggested: o.Suggested})
	}
	if t := p.Target; t != nil {
		tg := gen.DatabaseMigrationTarget{
			ClusterId: t.ClusterID, ClusterName: t.ClusterName, ComponentId: t.ComponentID, Node: t.Node.Hostname, Version: t.Version,
			Address: t.Address, Port: t.Port, Hosts: nonNil(t.Hosts), BufferPoolBytes: t.BufferPoolBytes, DataBytes: t.DataBytes,
			Disks: make([]gen.DatabaseMigrationDisk, 0, len(t.Disks)), Exists: t.Exists,
		}
		for _, d := range t.Disks {
			tg.Disks = append(tg.Disks, gen.DatabaseMigrationDisk{Hostname: d.Hostname, Mount: d.Mount, FreeBytes: d.FreeBytes})
		}
		out.Target = nullable.NewNullableWithValue(tg)
	}
	if r := p.Reach; r != nil {
		out.Reach = nullable.NewNullableWithValue(gen.DatabaseMigrationReach{Reached: r.Reached, Version: r.Version, Tls: r.TLS, Code: r.Code, Error: r.Error})
	}
	for _, f := range p.Findings {
		out.Findings = append(out.Findings, gen.ComponentFinding{
			Level: f.Level, Code: f.Code, NodeId: nullableOf(f.NodeID), Hostname: f.Hostname, Message: f.Message, Command: f.Command,
		})
	}
	for _, r := range p.Runs {
		out.Runs = append(out.Runs, toAPIMigrationRun(r.DbMigration, r.ClusterName))
	}
	if a := p.Active; a != nil {
		out.Active = nullable.NewNullableWithValue(gen.ComponentDumpJob{JobId: a.ID, Kind: a.Kind, Title: a.Title})
	}
	return out
}

func toAPIMigrationRun(m store.DbMigration, clusterName string) gen.DatabaseMigrationRun {
	out := gen.DatabaseMigrationRun{
		Id: m.ID, Kind: gen.DatabaseMigrationRunKind(m.Kind), Status: gen.DatabaseMigrationRunStatus(m.Status), ClusterId: m.ClusterID,
		ClusterName: clusterName, ComponentId: m.ComponentID, SourceName: m.SourceName, SourceNode: m.SourceNode, SourceAddress: m.SourceAddress,
		Database: m.Database, TargetDatabase: m.TargetDatabase, TargetNode: m.TargetNode, StartedAt: m.StartedAt, FinishedAt: nullableOf(m.FinishedAt),
		SizeBytes: m.SizeBytes, DumpBytes: m.DumpBytes, ImportMs: nullableOf(m.ImportMs), CountMs: nullableOf(m.CountMs),
		Differences: []gen.DatabaseMigrationCount{}, Objects: []gen.DatabaseMigrationObjects{}, Message: m.Message, JobId: nullableOf(m.JobID),
	}
	var tables []protocol.DBMigrateCount
	_ = json.Unmarshal(m.Tables, &tables)
	out.Tables = len(tables)
	for _, t := range tables {
		out.RowsSource += t.Source
		out.RowsTarget += t.Target
		if t.Source != t.Target {
			out.Differences = append(out.Differences, gen.DatabaseMigrationCount{Name: t.Name, Source: t.Source, Target: t.Target})
		}
	}
	_ = json.Unmarshal(m.Objects, &out.Objects)
	if out.Objects == nil {
		out.Objects = []gen.DatabaseMigrationObjects{}
	}
	return out
}
