package components

// Onderhoud, herstarten en afsluiten van een node met een database van
// ClusterForge (internal/lifecycle). Vooraf zegt NodeDB of de actie de
// database stillegt of het VIP van de database zonder node laat; dan gaat ze
// alleen door als de gebruiker het toch wil. Is de node de primary van
// Patroni, dan schakelt LeaveDB eerst over naar de synchrone replica, zodat
// het VIP meegaat voor keepalived op de node uitgaat. Na een herstart of het
// einde van het onderhoud wacht RejoinDB tot de node weer meedoet: Synced
// bij Galera, de primary of een replica die bijhoudt bij Patroni.

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/jobs"
	"github.com/Jonasz1996/clusterforge/internal/lifecycle"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// De acties op een node, zoals internal/lifecycle ze noemt.
const (
	nodeMaintenance = "maintenance"
	nodeActivate    = "activate"
)

// NodeDB zoekt de toegepaste databases op een node, zoals MariaDB en
// PostgreSQL op dezelfde nodes. risk is niet leeg als de actie (maintenance,
// reboot, shutdown of activate) een database stillegt, haar meerderheid kost
// of het VIP van een database zonder node laat.
func (s *Service) NodeDB(ctx context.Context, nodeID uuid.UUID, action string) ([]lifecycle.NodeDatabase, string, error) {
	rows, err := s.q.ListNodeComponents(ctx, nodeID)
	if err != nil {
		return nil, "", err
	}
	var dbs []lifecycle.NodeDatabase
	var risks []string
	for _, r := range rows {
		spec, err := parseSpec(r.Spec)
		if err != nil {
			return nil, "", err
		}
		i := slices.IndexFunc(spec.Nodes, func(n Node) bool { return n.NodeID == nodeID })
		if i < 0 {
			continue
		}
		if action != nodeActivate {
			stops := action != nodeMaintenance
			risk := ""
			if r.Engine == protocol.DBPostgreSQL {
				risk, err = s.pgRisk(ctx, r.ClusterID, spec, spec.Nodes[i], stops)
				if err != nil {
					return nil, "", err
				}
			} else {
				risk = s.galeraRisk(ctx, spec, spec.Nodes[i], stops)
			}
			if risk != "" {
				risks = append(risks, risk)
			}
		}
		dbs = append(dbs, lifecycle.NodeDatabase{ID: r.ID, Engine: r.Engine, Title: title(spec)})
	}
	return dbs, strings.Join(risks, "; "), nil
}

// galeraRisk zegt wat het voor Galera betekent als n wegvalt (stops) of
// alleen keepalived uitzet.
func (s *Service) galeraRisk(ctx context.Context, spec Spec, n Node, stops bool) string {
	if n.Role != RoleData {
		return ""
	}
	slug := spec.Cluster.Slug
	var self *obs
	others := 0
	all := s.observe(ctx, dataNodes(spec, nil), false)
	for i, o := range all {
		if o.node.NodeID == n.NodeID {
			self = &all[i]
		} else if o.synced(slug) {
			others++
		}
	}
	var risks []string
	switch {
	case self == nil || !self.synced(slug) || others > 0:
	case stops:
		risks = append(risks, fmt.Sprintf("MariaDB van %s doet alleen op %s nog mee; de database stopt tot %s terug is", spec.Cluster.Name, n.Hostname, n.Hostname))
	case spec.VIP != nil:
		risks = append(risks, fmt.Sprintf("MariaDB van %s doet alleen op %s nog mee; zonder keepalived daar heeft het VIP %s geen node", spec.Cluster.Name,
			n.Hostname, spec.VIP.Address))
	}
	if stops && self != nil && self.answered() && self.pos.Galera != nil && strings.HasPrefix(self.pos.Galera.State, "Donor") {
		risks = append(risks, fmt.Sprintf("MariaDB op %s geeft nu de data aan een andere node (SST); die kopie stopt", n.Hostname))
	}
	return strings.Join(risks, "; ")
}

// pgRisk zegt wat het voor Patroni betekent als n wegvalt (stops) of
// alleen keepalived uitzet.
func (s *Service) pgRisk(ctx context.Context, clusterID uuid.UUID, spec Spec, n Node, stops bool) (string, error) {
	slug := spec.Cluster.Slug
	nodes, err := s.pgNodes(ctx, clusterID, spec)
	if err != nil {
		return "", err
	}
	var risks []string
	if i := slices.IndexFunc(nodes, func(p pgNode) bool { return p.node.NodeID == n.NodeID }); i >= 0 && nodes[i].err == nil && isPrimary(nodes[i].server, slug) {
		cand, why := syncStandby(nodes, n, slug)
		if cand == nil {
			what := "kan niemand schrijven tot " + n.Hostname + " terug is"
			if !stops && spec.VIP != nil {
				what = fmt.Sprintf("heeft het VIP %s geen node zolang %s in onderhoud is", spec.VIP.Address, n.Hostname)
			}
			risks = append(risks, fmt.Sprintf("ClusterForge kan niet overschakelen van %s, de primary van PostgreSQL: %s. Zonder overschakelen %s", n.Hostname, why, what))
		}
	}
	if stops {
		// etcd draait op elke node van de database, ook op de
		// scheidsrechter, en heeft een meerderheid nodig.
		total, up := len(spec.Nodes), 0
		for _, x := range spec.Nodes {
			if x.NodeID == n.NodeID {
				continue
			}
			if x.Role != RoleData {
				rt, err := s.q.GetNodeRuntime(ctx, x.NodeID)
				if err != nil && !errors.Is(err, pgx.ErrNoRows) {
					return "", err
				}
				if err == nil && rt.HeartbeatAt != nil && s.Now().Sub(*rt.HeartbeatAt) <= s.Fresh {
					up++
				}
				continue
			}
			for _, p := range nodes {
				if p.node.NodeID == x.NodeID && p.err == nil && p.server != nil && p.server.Patroni != nil && p.server.Patroni.Error == "" && p.server.Patroni.Scope == slug {
					up++
				}
			}
		}
		if up < total/2+1 {
			risks = append(risks, fmt.Sprintf("zonder %s doen nog %d van de %d nodes mee in etcd, en dat is geen meerderheid; Patroni zet PostgreSQL dan op alleen lezen",
				n.Hostname, up, total))
		}
	}
	return strings.Join(risks, "; "), nil
}

// syncStandby is de synchrone replica waar ClusterForge naar kan
// overschakelen als n wegvalt; nil met de reden als die er niet is.
func syncStandby(nodes []pgNode, n Node, slug string) (*pgNode, string) {
	for i, p := range nodes {
		if p.node.NodeID == n.NodeID || p.err != nil || p.server == nil || p.server.Patroni == nil || !p.server.Patroni.Sync {
			continue
		}
		if state, ok := patroniHealthy(p.server, slug); !ok {
			return nil, fmt.Sprintf("de synchrone replica %s houdt niet bij (%s)", p.node.Hostname, state)
		}
		if err := patroniProtocol(p); err != nil {
			return nil, err.Error()
		}
		return &nodes[i], ""
	}
	return nil, "er is geen synchrone replica"
}

// nodeComponent leest het onderdeel en de node daarin.
func (s *Service) nodeComponent(ctx context.Context, componentID, nodeID uuid.UUID) (store.ClusterComponent, Spec, Node, error) {
	comp, err := s.q.GetClusterComponent(ctx, componentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return comp, Spec{}, Node{}, errors.New("de database bestaat niet meer")
	}
	if err != nil {
		return comp, Spec{}, Node{}, err
	}
	spec, err := parseSpec(comp.Spec)
	if err != nil {
		return comp, Spec{}, Node{}, err
	}
	i := slices.IndexFunc(spec.Nodes, func(n Node) bool { return n.NodeID == nodeID })
	if i < 0 {
		return comp, spec, Node{}, errors.New("de node hoort niet meer bij de database")
	}
	return comp, spec, spec.Nodes[i], nil
}

// LeaveDB maakt een node klaar om weg te gaan: is hij de primary van
// Patroni, dan schakelt ClusterForge over naar de synchrone replica en
// wacht tot het VIP meegaat. Met force gaat de taak ook door als dat niet
// kan.
func (s *Service) LeaveDB(ctx context.Context, st *jobs.Step, actor events.Actor, j *jobs.Job, componentID, nodeID uuid.UUID, force bool) error {
	comp, spec, n, err := s.nodeComponent(ctx, componentID, nodeID)
	if err != nil {
		return err
	}
	return s.switchAway(ctx, st, actor, j, comp, spec, n, force, "maintenance")
}

// switchAway schakelt over naar de synchrone replica als n de primary van
// Patroni is, en wacht tot het VIP meegaat. reason komt in het event, zoals
// maintenance of update.
func (s *Service) switchAway(ctx context.Context, st *jobs.Step, actor events.Actor, j *jobs.Job, comp store.ClusterComponent, spec Spec, n Node, force bool, reason string) error {
	if comp.Engine != protocol.DBPostgreSQL || n.Role != RoleData {
		return nil
	}
	slug := spec.Cluster.Slug
	srv, err := s.server(ctx, n.NodeID, protocol.DBPostgreSQL)
	if err != nil {
		return fmt.Errorf("%s zegt niet of hij de primary is: %w", n.Hostname, err)
	}
	if !isPrimary(srv, slug) {
		st.Logf("%s is niet de primary van PostgreSQL; er is niets over te schakelen", n.Hostname)
		return nil
	}
	nodes, err := s.pgNodes(ctx, comp.ClusterID, spec)
	if err != nil {
		return err
	}
	cand, why := syncStandby(nodes, n, slug)
	if cand == nil {
		if force {
			st.Logf("%s is de primary en ClusterForge kan niet overschakelen: %s. Je ging toch door, dus PostgreSQL schrijft niet tot %s terug is", n.Hostname, why, n.Hostname)
			return nil
		}
		return fmt.Errorf("ClusterForge kan niet overschakelen van %s, de primary van PostgreSQL: %s", n.Hostname, why)
	}
	if err := s.switchover(ctx, st, spec, n, cand.node); err != nil {
		return err
	}
	if err := s.waitPrimary(ctx, st, cand.node, slug); err != nil {
		return err
	}
	if spec.VIP != nil {
		if err := s.waitOwnVIP(ctx, st, spec, cand.node.Hostname); err != nil {
			return err
		}
	}
	return s.manageDone(ctx, actor, j, comp, spec, "cluster.database_switchover", map[string]any{"from": n.Hostname, "to": cand.node.Hostname, reason: true})
}

// RejoinDB wacht tot een node na een herstart of onderhoud weer meedoet in
// de database: Synced bij Galera, de primary of een replica die bijhoudt
// bij Patroni.
func (s *Service) RejoinDB(ctx context.Context, st *jobs.Step, componentID, nodeID uuid.UUID) error {
	comp, spec, n, err := s.nodeComponent(ctx, componentID, nodeID)
	if err != nil {
		return err
	}
	if n.Role != RoleData {
		st.Logf("%s is de scheidsrechter en heeft geen data", n.Hostname)
		return nil
	}
	if comp.Engine == protocol.DBPostgreSQL {
		return s.waitPatroni(ctx, st, n, spec.Cluster.Slug)
	}
	return s.waitSynced(ctx, st, n, spec.Cluster.Slug, 1)
}
