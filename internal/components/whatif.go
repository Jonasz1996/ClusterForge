package components

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/risk"
	"github.com/Jonasz1996/clusterforge/internal/status"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// WhatIfDatabases geeft de toegepaste onderdelen Database van alle
// clusters, zodat de wat-als de meerderheid kent. Is wat de bewaking zag
// vers, dan komt het erbij, met de datanodes die bij Galera in de Primary
// component zaten.
func (s *Service) WhatIfDatabases(ctx context.Context) ([]risk.Database, error) {
	rows, err := s.q.ListWatchedComponents(ctx)
	if err != nil {
		return nil, err
	}
	now := s.Now()
	out := []risk.Database{}
	for _, c := range rows {
		if c.Engine != protocol.DBMariaDB && c.Engine != protocol.DBPostgreSQL {
			continue
		}
		spec, err := parseSpec(c.Spec)
		if err != nil {
			return nil, err
		}
		d := risk.Database{ClusterID: c.ClusterID, Title: title(spec), Unit: dbUnit(c.Engine), Patroni: c.Engine == protocol.DBPostgreSQL}
		for _, n := range spec.order() {
			switch n.Role {
			case RoleData:
				d.Nodes = append(d.Nodes, n.NodeID)
			case RoleArbiter:
				d.Arbiters = append(d.Arbiters, n.NodeID)
			}
		}
		if c.Health != "" && c.HealthAt != nil && now.Sub(*c.HealthAt) <= status.DBHealthStale {
			d.Health = &status.DBHealth{Status: status.Status(c.Health), Reason: c.HealthReason, At: *c.HealthAt}
			if !d.Patroni {
				d.Members = members(c.Observed)
			}
		}
		out = append(out, d)
	}
	return out, nil
}

// members zijn de nodes die de bewaking laatst zag meedoen; nil als ze dat
// niet bijhield.
func members(raw []byte) []uuid.UUID {
	var o struct {
		Nodes map[string]struct {
			Member *bool `json:"member"`
		} `json:"nodes"`
	}
	if json.Unmarshal(raw, &o) != nil || o.Nodes == nil {
		return nil
	}
	out := []uuid.UUID{}
	for id, n := range o.Nodes {
		if n.Member == nil {
			return nil
		}
		if u, err := uuid.Parse(id); err == nil && *n.Member {
			out = append(out, u)
		}
	}
	return out
}
