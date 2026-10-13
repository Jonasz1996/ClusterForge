package risk

import (
	"context"
	"fmt"
	"slices"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/deps"
	"github.com/Jonasz1996/clusterforge/internal/status"
)

// Database is een onderdeel Database van een cluster. Zo kent de wat-als de
// meerderheid: Galera en etcd stoppen als niet meer dan de helft van hun
// leden overblijft, ook al draait de database op een node nog.
type Database struct {
	ClusterID uuid.UUID
	Title     string
	// Unit is de dienst van de database op de nodes: mariadb of patroni.
	Unit string
	// Patroni is true voor PostgreSQL met Patroni. Dan telt etcd, met al
	// zijn leden, ook die er nu niet zijn. Galera telt de leden die nu in
	// zijn Primary component zitten: na een uitval gaat het met de rest
	// verder.
	Patroni  bool
	Nodes    []uuid.UUID
	Arbiters []uuid.UUID
	// Health is wat de bewaking laatst zag; nil als dat te oud is.
	Health *status.DBHealth
	// Members zijn de datanodes die de bewaking toen in de Primary
	// component van Galera zag; nil als dat niet bekend is.
	Members []uuid.UUID
}

// DatabaseSource geeft de onderdelen Database van alle clusters, voor de
// wat-als, en per cluster wat de kaart Database nodig heeft.
type DatabaseSource interface {
	WhatIfDatabases(ctx context.Context) ([]Database, error)
	ScoreDatabases(ctx context.Context) (map[uuid.UUID]*DatabaseCardInput, error)
}

// dbPrediction is wat er met een database gebeurt.
type dbPrediction struct {
	health status.DBHealth
	// stops is true als de database door de uitval stopt; why zegt dan
	// waarom, voor haar dienst.
	stops bool
	why   string
}

// predict rekent uit hoe het met de database gaat als de nodes in failed
// tegelijk uitvallen. Met baseline draait elke actieve node. Zonder nieuwe
// uitval blijft de database zoals de bewaking haar laatst zag, en een
// database die down is, blijft down.
func (d Database) predict(g *deps.Graph, failed map[uuid.UUID]string, baseline bool) dbPrediction {
	hostname := func(id uuid.UUID) string {
		n, _ := g.Node(id)
		return n.Hostname
	}
	running := func(id uuid.UUID) bool {
		n, ok := g.Node(id)
		return ok && n.Active && (baseline || n.Status == status.Healthy || n.Status == status.Degraded)
	}
	fails := func(id uuid.UUID) bool {
		_, ok := failed[id]
		return ok
	}
	all := append(slices.Clone(d.Nodes), d.Arbiters...)
	lost := false
	for _, id := range all {
		if running(id) && fails(id) {
			lost = true
		}
	}
	if !baseline && d.Health != nil && (!lost || d.Health.Status == status.Down) {
		return dbPrediction{health: status.DBHealth{Status: d.Health.Status, Reason: d.Health.Reason}}
	}

	// counts is true voor een lid dat nu meetelt: zijn node draait, en bij
	// Galera zag de bewaking een datanode in de Primary component.
	counts := func(id uuid.UUID) bool {
		if !running(id) {
			return false
		}
		if d.Patroni || baseline || d.Members == nil || !slices.Contains(d.Nodes, id) {
			return true
		}
		return slices.Contains(d.Members, id)
	}
	var base []uuid.UUID
	var outside []string
	for _, id := range all {
		switch {
		case counts(id):
			base = append(base, id)
		case d.Patroni:
			// etcd telt een lid dat weg is ook mee.
			base = append(base, id)
		default:
			outside = append(outside, hostname(id))
		}
	}
	alive, data := 0, 0
	var gone []string
	for _, id := range base {
		if counts(id) && !fails(id) {
			alive++
			if slices.Contains(d.Nodes, id) {
				data++
			}
			continue
		}
		gone = append(gone, hostname(id))
	}
	slices.Sort(gone)
	slices.Sort(outside)

	server, stop, voter := "MariaDB", "dus MariaDB stopt", "is er"
	if d.Patroni {
		server, stop, voter = "PostgreSQL", "dus PostgreSQL stopt met schrijven", "heeft etcd"
	}
	note := ""
	if len(outside) > 0 {
		note = "; " + list(outside) + " " + doesNot(len(outside)) + " nu niet mee"
	}
	switch {
	case data == 0:
		why := server + " draait op geen enkele datanode meer"
		return dbPrediction{health: status.DBHealth{Status: status.Down, Reason: d.Title + ": " + why}, stops: lost, why: why}
	case 2*alive <= len(base):
		why := fmt.Sprintf("zonder %s %s geen meerderheid (%d van de %d leden%s)", list(gone), voter, alive, len(base), note)
		return dbPrediction{health: status.DBHealth{Status: status.Down, Reason: d.Title + ": " + why + ", " + stop}, stops: lost, why: why}
	}

	var problems []string
	for _, id := range d.Nodes {
		switch {
		case counts(id) && fails(id):
			problems = append(problems, hostname(id)+" valt uit")
		case !counts(id):
			problems = append(problems, hostname(id)+" doet niet mee")
		}
	}
	for _, id := range d.Arbiters {
		switch {
		case counts(id) && fails(id):
			problems = append(problems, "de scheidsrechter "+hostname(id)+" valt uit")
		case !counts(id):
			problems = append(problems, "de scheidsrechter "+hostname(id)+" is offline")
		}
	}
	if len(problems) == 0 {
		return dbPrediction{health: status.DBHealth{Status: status.Healthy}}
	}
	reason := fmt.Sprintf("%s: %s; nog %d van de %d leden, genoeg voor een meerderheid", d.Title, list(problems), alive, len(base))
	return dbPrediction{health: status.DBHealth{Status: status.Degraded, Reason: reason}}
}

func doesNot(n int) string {
	if n == 1 {
		return "doet"
	}
	return "doen"
}

// dbServices zijn de diensten van het cluster met de unit van de database.
func dbServices(g *deps.Graph, cluster uuid.UUID, unit string) []uuid.UUID {
	var out []uuid.UUID
	for _, s := range g.Services {
		if s.State == deps.StateConfirmed && s.ClusterID != nil && *s.ClusterID == cluster && s.Unit == unit {
			out = append(out, s.ID)
		}
	}
	return out
}
