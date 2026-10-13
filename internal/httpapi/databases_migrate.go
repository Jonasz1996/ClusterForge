package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/google/uuid"
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

// StartDatabaseMove begint de echte verhuizing; raakt ze een prodcluster,
// dan met de slug en tweestapsverificatie.
func (s *Server) StartDatabaseMove(w http.ResponseWriter, r *http.Request) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var in gen.DatabaseMoveInput
	if !decode(w, r, &in) {
		return
	}
	ctx := r.Context()
	prod, slug, err := s.components.MoveProd(ctx, in.Source, in.ComponentId)
	if err != nil {
		s.migrationError(w, r, err)
		return
	}
	if !confirmProd(w, p, prod, slug, in.Confirm, "een database verhuizen") {
		return
	}
	j, err := s.components.StartMove(ctx, events.User(p.User.ID), in.Source, in.Database, in.ComponentId, in.Force != nil && *in.Force)
	if err != nil {
		s.migrationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, toAPIJob(j, &p.User.Username))
}

// SwitchDatabaseMove: de app draait op de nieuwe database; de oude gaat op
// slot.
func (s *Server) SwitchDatabaseMove(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var in gen.SwitchDatabaseMoveJSONBody
	if r.ContentLength != 0 && !decode(w, r, &in) {
		return
	}
	row, err := s.components.SwitchMove(r.Context(), events.User(p.User.ID), id, in.Force != nil && *in.Force)
	if err != nil {
		s.migrationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toAPIMigrationRun(row.DbMigration, row.ClusterName))
}

// CancelDatabaseMove haalt de kopie van een verhuizing weer weg.
func (s *Server) CancelDatabaseMove(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var in gen.CancelDatabaseMoveJSONBody
	if !decode(w, r, &in) {
		return
	}
	row, err := s.components.CancelMove(r.Context(), events.User(p.User.ID), id, in.Confirm)
	if err != nil {
		s.migrationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toAPIMigrationRun(row.DbMigration, row.ClusterName))
}

// LockDatabaseMove zet de oude database van een verhuisde app op slot, of
// haalt het slot eraf.
func (s *Server) LockDatabaseMove(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var in gen.LockDatabaseMoveJSONBody
	if !decode(w, r, &in) {
		return
	}
	row, err := s.components.LockMove(r.Context(), events.User(p.User.ID), id, in.Locked)
	if err != nil {
		s.migrationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toAPIMigrationRun(row.DbMigration, row.ClusterName))
}

// RetireDatabaseMove bouwt de oude database van een verhuisde app af: een
// laatste dump, en dan de database en haar gebruikers weg. Name is de naam
// van de database; op prod ook de slug en tweestapsverificatie.
func (s *Server) RetireDatabaseMove(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var in gen.ComponentDropInput
	if !decode(w, r, &in) {
		return
	}
	ctx := r.Context()
	prod, slug, err := s.components.RetireProd(ctx, id)
	if err != nil {
		s.migrationError(w, r, err)
		return
	}
	if !confirmProd(w, p, prod, slug, in.Confirm, "een oude database afbouwen") {
		return
	}
	j, err := s.components.StartRetire(ctx, events.User(p.User.ID), id, in.Name)
	if err != nil {
		s.migrationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, toAPIJob(j, &p.User.Username))
}

// ListDatabaseRetire geeft de oude databaseservers waarvan een database
// verhuisd is. Ook voor viewers.
func (s *Server) ListDatabaseRetire(w http.ResponseWriter, r *http.Request) {
	sources, err := s.components.RetireOverview(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	items := make([]gen.DatabaseRetireSource, 0, len(sources))
	for _, src := range sources {
		out := gen.DatabaseRetireSource{
			Name: src.Name, ClusterId: nullableOf(src.ClusterID), Keys: nonNil(src.Keys), Servers: nonNil(src.Servers), Readable: src.Readable,
			Empty: src.Empty, Remaining: make([]gen.DatabaseRetireRemaining, 0, len(src.Remaining)),
			Moves: make([]gen.DatabaseRetireMove, 0, len(src.Moves)), Rules: make([]gen.DatabaseRetireRule, 0, len(src.Rules)),
		}
		for _, d := range src.Remaining {
			out.Remaining = append(out.Remaining, gen.DatabaseRetireRemaining{SourceKey: d.SourceKey, Engine: d.Engine, Database: d.Database, Moved: d.Moved})
		}
		for _, mv := range src.Moves {
			m := gen.DatabaseRetireMove{
				Run: toAPIMigrationRun(mv.Migration, mv.ClusterName), Engine: mv.Engine, SourceKey: mv.Migration.SourceKey, Exists: mv.Exists,
				LastUsed: nullableOf(mv.LastUsed), LastBy: mv.LastBy, LastLocal: nullableOf(mv.LastLocal), LocalBy: mv.LocalBy,
				ReadyAt: nullableOf(mv.ReadyAt), Ready: mv.Ready, Active: nullable.NewNullNullable[gen.ComponentDumpJob](), ProdSlug: mv.ProdSlug,
			}
			if a := mv.Active; a != nil {
				m.Active = nullable.NewNullableWithValue(gen.ComponentDumpJob{JobId: a.ID, Kind: a.Kind, Title: a.Title})
			}
			out.Moves = append(out.Moves, m)
		}
		for _, rule := range src.Rules {
			out.Rules = append(out.Rules, gen.DatabaseRetireRule{
				ClusterId: rule.ClusterID, ClusterName: rule.ClusterName, Engine: rule.Engine, Address: rule.Address, Port: rule.Port,
				LastSeen: nullableOf(rule.LastSeen), State: gen.DatabaseRetireRuleState(rule.State),
			})
		}
		items = append(items, out)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) migrationError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, components.ErrSourceNotFound) || errors.Is(err, components.ErrMigrationNotFound) {
		writeError(w, http.StatusNotFound, "not_found", err.Error())
		return
	}
	s.componentError(w, r, err)
}

func toAPIMigrationPlan(p components.MigrationPlan) gen.DatabaseMigrationPlan {
	src := p.Source
	out := gen.DatabaseMigrationPlan{
		Source: gen.DatabaseMigrationSource{
			Key: src.Key, Name: src.Name, ClusterId: nullableOf(src.ClusterID), Slug: src.Slug, Environment: src.Environment, Engine: src.Engine,
			Kind: src.Kind, Version: src.Version, Summary: src.Summary, Servers: nonNil(src.Servers), Address: src.Address, Port: src.Port, Via: src.Via,
		},
		Database: p.Database, Info: nullable.NewNullNullable[gen.DatabaseMigrationInfo](),
		Clients: make([]gen.DatabaseUser, 0, len(p.Clients)), Targets: make([]gen.DatabaseMigrationOption, 0, len(p.Targets)),
		Target: nullable.NewNullNullable[gen.DatabaseMigrationTarget](), Reach: nullable.NewNullNullable[gen.DatabaseMigrationReach](),
		Findings: make([]gen.ComponentFinding, 0, len(p.Findings)), Blocked: p.Blocked, Runs: make([]gen.DatabaseMigrationRun, 0, len(p.Runs)),
		Active: nullable.NewNullNullable[gen.ComponentDumpJob](), Accounts: make([]gen.DatabaseMigrationAccount, 0, len(p.Accounts)),
		NewHosts: nonNil(p.NewHosts), Connections: make([]gen.DatabaseMigrationConnection, 0, len(p.Connections)),
		MoveFindings: toAPIFindings(p.MoveFindings), MoveBlocked: p.MoveBlocked,
	}
	for _, a := range p.Accounts {
		out.Accounts = append(out.Accounts, gen.DatabaseMigrationAccount{
			User: a.User, Host: a.Host, Plugin: a.Plugin, Privileges: nonNil(a.Privileges), Global: a.Global, MemberOf: nonNil(a.MemberOf),
			Moves: components.Movable(src.Engine, a),
		})
	}
	for _, c := range p.Connections {
		out.Connections = append(out.Connections, gen.DatabaseMigrationConnection{
			Server: c.Server, User: c.User, Address: c.Address, Hostname: c.Hostname, Count: c.Count,
		})
	}
	if src.Node != nil {
		out.Source.Node = src.Node.Hostname
	}
	// Een database die er niet (meer) is, zoals na het afbouwen, heeft geen
	// gegevens.
	if i := p.Info; i != nil && i.Exists {
		info := gen.DatabaseMigrationInfo{
			Version: i.Version, Charset: i.Charset, Collation: i.Collation, SizeBytes: i.SizeBytes, Tables: len(i.Tables),
			Views: i.Views, Routines: i.Routines, Triggers: i.Triggers, Events: i.Events, Owner: i.Owner, Extensions: nonNil(i.Extensions),
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
			ClusterId: t.ClusterID, ClusterName: t.ClusterName, Slug: t.Slug, Environment: t.Environment, ComponentId: t.ComponentID, Node: t.Node.Hostname, Version: t.Version,
			Address: t.Address, Port: t.Port, Hosts: nonNil(t.Hosts), BufferPoolBytes: t.BufferPoolBytes, DataBytes: t.DataBytes,
			Disks: make([]gen.DatabaseMigrationDisk, 0, len(t.Disks)), Exists: t.Exists,
		}
		for _, d := range t.Disks {
			tg.Disks = append(tg.Disks, gen.DatabaseMigrationDisk{Hostname: d.Hostname, Mount: d.Mount, FreeBytes: d.FreeBytes})
		}
		out.Target = nullable.NewNullableWithValue(tg)
	}
	if r := p.Reach; r != nil {
		out.Reach = nullable.NewNullableWithValue(gen.DatabaseMigrationReach{Reached: r.Reached, Version: r.Version, Tls: r.TLS, Code: r.Code, Error: r.Error,
			Seen: r.Seen})
	}
	out.Findings = toAPIFindings(p.Findings)
	for _, r := range p.Runs {
		out.Runs = append(out.Runs, toAPIMigrationRun(r.DbMigration, r.ClusterName))
	}
	if a := p.Active; a != nil {
		out.Active = nullable.NewNullableWithValue(gen.ComponentDumpJob{JobId: a.ID, Kind: a.Kind, Title: a.Title})
	}
	return out
}

func toAPIFindings(in []components.Finding) []gen.ComponentFinding {
	out := make([]gen.ComponentFinding, 0, len(in))
	for _, f := range in {
		out = append(out, gen.ComponentFinding{
			Level: f.Level, Code: f.Code, NodeId: nullableOf(f.NodeID), Hostname: f.Hostname, Message: f.Message, Command: f.Command,
		})
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
		Stage: m.Stage, Accounts: []gen.DatabaseMigrationMovedUser{}, Force: m.Force, TargetAddress: m.TargetAddress,
		CopiedAt: nullableOf(m.CopiedAt), SwitchedAt: nullableOf(m.SwitchedAt), Locked: m.Locked,
		RetireJobId: nullableOf(m.RetireJobID), RetiredAt: nullableOf(m.RetiredAt), RetireNode: m.RetireNode, RetireFile: m.RetireFile,
		RetireBytes: m.RetireBytes, RetireMessage: m.RetireMessage,
	}
	var accounts []components.MoveAccount
	_ = json.Unmarshal(m.Accounts, &accounts)
	for _, a := range accounts {
		u := gen.DatabaseMigrationMovedUser{User: a.User, OldHosts: []string{}, Hosts: nonNil(a.Hosts), Privileges: nonNil(a.Privileges), New: a.New}
		for _, o := range a.Old {
			if o.Host != "" {
				u.OldHosts = append(u.OldHosts, o.Host)
			}
		}
		out.Accounts = append(out.Accounts, u)
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
