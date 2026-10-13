package components

import (
	"context"
	"slices"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/databases"
	"github.com/Jonasz1996/clusterforge/internal/risk"
	"github.com/Jonasz1996/clusterforge/internal/status"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// ScoreDatabases geeft per cluster wat de kaart Database nodig heeft: zijn
// onderdelen Database met hun dumps en failovertests, en van de
// databaseservers op zijn nodes de gedeelde en de verhuisde databases.
// Het leest alleen wat ClusterForge al bijhoudt; de score rekent elke
// minuut.
func (s *Service) ScoreDatabases(ctx context.Context) (map[uuid.UUID]*risk.DatabaseCardInput, error) {
	rows, err := s.q.ListWatchedComponents(ctx)
	if err != nil {
		return nil, err
	}
	backups, err := s.DatabaseBackups(ctx)
	if err != nil {
		return nil, err
	}
	passes, err := s.q.ListDatabaseTestPasses(ctx)
	if err != nil {
		return nil, err
	}
	groups, err := s.Overview(ctx)
	if err != nil {
		return nil, err
	}
	moves, err := s.q.ListDoneDBMigrations(ctx)
	if err != nil {
		return nil, err
	}
	now := s.Now()
	out := map[uuid.UUID]*risk.DatabaseCardInput{}
	of := func(id uuid.UUID) *risk.DatabaseCardInput {
		if out[id] == nil {
			out[id] = &risk.DatabaseCardInput{}
		}
		return out[id]
	}

	for _, c := range rows {
		if c.Engine != protocol.DBMariaDB && c.Engine != protocol.DBPostgreSQL {
			continue
		}
		spec, err := parseSpec(c.Spec)
		if err != nil {
			return nil, err
		}
		in := risk.DBComponentInput{ID: c.ID, Title: title(spec), Engine: c.Engine, VIP: spec.vipAddress(), BackupOn: c.BackupOn}
		for _, n := range spec.order() {
			switch n.Role {
			case RoleData:
				in.Nodes = append(in.Nodes, n.NodeID)
			case RoleArbiter:
				in.Arbiters = append(in.Arbiters, n.NodeID)
			}
		}
		if c.Health != "" && c.HealthAt != nil && now.Sub(*c.HealthAt) <= status.DBHealthStale {
			in.Health = &status.DBHealth{Status: status.Status(c.Health), Reason: c.HealthReason, At: *c.HealthAt}
		}
		for _, b := range backups {
			if b.ComponentID != c.ID {
				continue
			}
			d := risk.DBDumpInput{Database: b.Database, Stale: b.Freshness == DumpStale || b.Freshness == DumpMissing}
			if l := b.Latest; l != nil {
				t := l.StartedAt
				d.Latest = &t
				d.NoPrimaryKey, d.NotInnoDB = l.NoPrimaryKey, l.NotInnodb
			}
			in.Dumps = append(in.Dumps, d)
		}
		for _, p := range passes {
			if p.ClusterID == c.ClusterID && p.Title == in.Title && (in.LastTest == nil || p.FinishedAt.After(*in.LastTest)) {
				t := p.FinishedAt
				in.LastTest = &t
			}
		}
		d := of(c.ClusterID)
		d.Components = append(d.Components, in)
	}

	byKey := map[string]databases.Group{}
	for _, g := range groups {
		byKey[g.Key()] = g
		if g.ClusterID == nil {
			continue
		}
		var shared []risk.SharedDBInput
		for _, db := range g.Databases {
			if !db.Shared || db.Name == protocol.DBProbeDatabase {
				continue
			}
			sh := risk.SharedDBInput{Engine: g.Engine, Database: db.Name}
			for _, u := range db.Users {
				if u.Kind != databases.UserLocal {
					sh.Users = append(sh.Users, userName(u))
				}
			}
			shared = append(shared, sh)
		}
		// Een cluster met een databaseserver krijgt de kaart, ook zonder
		// onderdeel.
		d := of(*g.ClusterID)
		d.Shared = append(d.Shared, shared...)
	}
	// Alleen de nieuwste verhuizing van een database telt, zoals bij het
	// afbouwen.
	seen := map[string]bool{}
	for _, r := range moves {
		m := r.DbMigration
		k := m.SourceKey + "/" + m.Database
		if seen[k] {
			continue
		}
		seen[k] = true
		g, ok := byKey[m.SourceKey]
		if !ok || g.ClusterID == nil || m.RetiredAt != nil || m.SwitchedAt == nil {
			continue
		}
		if !slices.ContainsFunc(g.Databases, func(d databases.Database) bool { return d.Name == m.Database }) && complete(g) {
			continue
		}
		d := of(*g.ClusterID)
		d.Moved = append(d.Moved, risk.MovedDBInput{
			ID: m.ID, Engine: g.Engine, Database: m.Database, SourceKey: m.SourceKey, To: r.ClusterName, SwitchedAt: *m.SwitchedAt,
		})
	}
	return out, nil
}

// userName is wie een database gebruikt, in een paar woorden.
func userName(u databases.User) string {
	switch u.Kind {
	case databases.UserUnknown:
		return "het onbekende adres " + u.Name
	}
	return u.Name
}
