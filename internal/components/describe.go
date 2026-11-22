package components

import (
	"context"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/databases"
	"github.com/Jonasz1996/clusterforge/internal/status"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// ClusterDatabases zijn de databases op de nodes van één cluster, zoals de
// documentatie en de assistent ze beschrijven: de onderdelen Database, en
// de databaseservers die ClusterForge alleen leest. Er staan geen
// wachtwoorden, hashes of rijen in.
type ClusterDatabases struct {
	Components []DescribedComponent
	// Unmanaged zijn de servers op nodes van het cluster zonder onderdeel,
	// zoals een gedeeld databasecluster dat ClusterForge niet uitrolde.
	Unmanaged []databases.Group
}

// DescribedComponent is een toegepast onderdeel Database.
type DescribedComponent struct {
	ID     uuid.UUID
	Engine string
	Title  string
	// Port is de poort van de database. VIP is het adres waarlangs de app
	// de database vindt, leeg zonder; OwnVIP zegt of de database het
	// VIP zelf heeft, en niet het VIP van de app volgt.
	Port   int
	VIP    string
	OwnVIP bool
	// Nodes in de volgorde van toepassen: de datanodes, dan de
	// scheidsrechter.
	Nodes []Node
	// Health is wat de bewaking laatst zag; leeg als dat te oud is.
	Health, HealthReason string
	// AutoRecover: ClusterForge begint Galera vanzelf opnieuw na een
	// volledige uitval.
	AutoRecover bool
	Backup      BackupSettings
	Dumps       []DatabaseDumps
	// Group is wat ClusterForge elke minuut van de servers leest; nil als
	// het ze nog niet las.
	Group *databases.Group
}

// DescribeDatabases geeft per cluster zijn databases. Het leest alleen wat
// ClusterForge al bijhoudt, en vraagt de agents niets.
func (s *Service) DescribeDatabases(ctx context.Context) (map[uuid.UUID]*ClusterDatabases, error) {
	rows, err := s.q.ListWatchedComponents(ctx)
	if err != nil {
		return nil, err
	}
	backups, err := s.DatabaseBackups(ctx)
	if err != nil {
		return nil, err
	}
	var groups []databases.Group
	if s.Overview != nil {
		if groups, err = s.Overview(ctx); err != nil {
			return nil, err
		}
	}
	now := s.Now()
	out := map[uuid.UUID]*ClusterDatabases{}
	of := func(id uuid.UUID) *ClusterDatabases {
		if out[id] == nil {
			out[id] = &ClusterDatabases{}
		}
		return out[id]
	}
	managed := map[string]bool{}
	for _, c := range rows {
		if c.Engine != protocol.DBMariaDB && c.Engine != protocol.DBPostgreSQL {
			continue
		}
		spec, err := parseSpec(c.Spec)
		if err != nil {
			return nil, err
		}
		d := DescribedComponent{
			ID: c.ID, Engine: c.Engine, Title: title(spec), Port: enginePort(c.Engine), VIP: spec.vipAddress(), Nodes: spec.order(),
			AutoRecover: c.AutoRecover,
			Backup:      settingsOf(store.ClusterComponent{BackupOn: c.BackupOn, BackupAt: c.BackupAt, BackupKeepDays: c.BackupKeepDays, BackupNextAt: c.BackupNextAt}),
		}
		d.OwnVIP = spec.VIP != nil && spec.VIP.Own
		if c.Health != "" && c.HealthAt != nil && now.Sub(*c.HealthAt) <= status.DBHealthStale {
			d.Health, d.HealthReason = c.Health, c.HealthReason
		}
		for _, b := range backups {
			if b.ComponentID == c.ID {
				d.Dumps = append(d.Dumps, b.DatabaseDumps)
			}
		}
		key := c.Engine + "/" + c.ClusterID.String()
		for i := range groups {
			if groups[i].Key() == key {
				g := groups[i]
				d.Group = &g
			}
		}
		managed[key] = true
		cd := of(c.ClusterID)
		cd.Components = append(cd.Components, d)
	}
	for _, g := range groups {
		if g.ClusterID == nil || managed[g.Key()] {
			continue
		}
		cd := of(*g.ClusterID)
		cd.Unmanaged = append(cd.Unmanaged, g)
	}
	return out, nil
}
