package components

// 'Database erbij' bij Cluster uitrollen: een nieuw cluster krijgt meteen
// een database op al zijn nodes, om er een app naartoe te verhuizen. De
// wens gaat mee in de transactie van de uitrol; lukt de uitrol, dan plant
// ClusterForge de database en voegt haar toe, zoals 'Database toevoegen'.
// Houdt het plan haar tegen, dan staat de reden in het logboek.
//
// De database volgt nog geen VIP: het VIP van een uitgerold cluster hoort
// bij zijn template.

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// WithDeploy geeft wat de uitrol in zijn transactie doet om na afloop een
// database met deze engine toe te voegen.
func (s *Service) WithDeploy(engine string, ownVIP bool) (func(ctx context.Context, q *store.Queries, c store.Cluster, revision int32, j store.Job) error, error) {
	// PostgreSQL heeft een eigen VIP nodig: dat geeft alleen een template
	// (Template.Database.VIP); anders voeg je het toe als het cluster er is.
	if engine != protocol.DBMariaDB && (engine != protocol.DBPostgreSQL || !ownVIP) {
		if engine == protocol.DBPostgreSQL {
			return nil, &InvalidError{"database.engine", "PostgreSQL heeft een eigen VIP nodig; voeg het toe met Database toevoegen als het cluster er is"}
		}
		return nil, &InvalidError{"database.engine", "kies mariadb"}
	}
	return func(ctx context.Context, q *store.Queries, c store.Cluster, _ int32, j store.Job) error {
		if c.Type == "kubernetes" {
			return &InvalidError{"database", "een Kubernetes-cluster draait zijn databases in Kubernetes zelf; Database erbij is er voor clusters met keepalived, Docker of een eigen template"}
		}
		return q.InsertDeployDatabase(ctx, store.InsertDeployDatabaseParams{ClusterID: c.ID, Engine: engine, JobID: &j.ID, RequestedBy: j.RequestedBy})
	}, nil
}

// deployFinished voegt de database toe als de uitrol van een cluster dat
// erom vroeg gelukt is. Een mislukte uitrol houdt de wens vast voor als
// hij opnieuw gaat.
func (s *Service) deployFinished(ctx context.Context, j store.Job) {
	if j.ClusterID == nil || j.Status != store.JobStatusSucceeded {
		return
	}
	w, err := s.q.TakeDeployDatabase(ctx, *j.ClusterID)
	if errors.Is(err, pgx.ErrNoRows) {
		return
	}
	if err != nil {
		s.log.Error("database na de uitrol lezen mislukt", "cluster", *j.ClusterID, "err", err)
		return
	}
	actor := events.System()
	if w.RequestedBy != nil {
		actor = events.User(*w.RequestedBy)
	}
	// Een template met een eigen database (zoals Zabbix) zet haar alleen op
	// de nodes van één rol; de proxy ernaast krijgt er geen.
	only, roleOnly, err := s.dep.DatabaseNodes(j)
	if err != nil {
		s.log.Error("uitrol lezen voor de database mislukt", "cluster", *j.ClusterID, "err", err)
	}
	if !roleOnly {
		only = nil
	}
	vip, err := s.dep.DatabaseVIP(j)
	if err != nil {
		s.log.Error("VIP van de database lezen mislukt", "cluster", w.ClusterID, "err", err)
	}
	reason, hosts := s.addAfterDeploy(ctx, actor, w, only, s.dep.DatabaseSettings(j), vip)
	if reason == "" {
		return
	}
	c, err := s.q.GetCluster(ctx, w.ClusterID)
	if err == nil {
		err = s.ev.Write(ctx, s.q, events.Event{
			Actor: actor, SubjectType: "cluster", SubjectID: c.ID.String(), ClusterID: &c.ID, Action: "cluster.database_not_added",
			Payload: map[string]any{"name": c.Name, "engine": w.Engine, "nodes": hosts, "reason": reason},
		})
	}
	if err != nil {
		s.log.Error("event schrijven mislukt", "action", "cluster.database_not_added", "err", err)
	}
}

// addAfterDeploy plant de database op alle nodes van het cluster, of alleen
// op only als die niet leeg is, met settings als instellingen, en voegt haar
// toe; de reden als dat niet kan.
func (s *Service) addAfterDeploy(ctx context.Context, actor events.Actor, w store.DeployDatabase, only []uuid.UUID, settings map[string]any, vip string) (string, []string) {
	cands, err := s.q.ListComponentCandidates(ctx, &w.ClusterID)
	if err != nil {
		return err.Error(), nil
	}
	// Een samengesteld cluster (fase 9) kiest de instellingen zelf, zoals
	// buffer_pool; wat ontbreekt, krijgt de standaard.
	req := Request{Engine: w.Engine, Settings: settings, VIPAddress: vip}
	var hosts []string
	for _, r := range cands {
		if len(only) > 0 && !slices.Contains(only, r.ID) {
			continue
		}
		req.Nodes = append(req.Nodes, r.ID)
		hosts = append(hosts, r.Hostname)
	}
	p, err := s.Plan(ctx, w.ClusterID, req)
	var ie *InvalidError
	switch {
	case errors.As(err, &ie):
		return ie.Msg, hosts
	case err != nil:
		return err.Error(), hosts
	case p.Blocked:
		var msgs []string
		for _, f := range p.Findings {
			if f.Level == Blocked {
				msgs = append(msgs, f.Message)
			}
		}
		return strings.Join(msgs, "; "), hosts
	}
	if _, err := s.Add(ctx, actor, w.ClusterID, req, p.Hash); err != nil {
		var ce *ConflictError
		if errors.As(err, &ce) {
			return ce.Msg, hosts
		}
		return err.Error(), hosts
	}
	return "", hosts
}
