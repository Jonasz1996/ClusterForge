package components

// PostgreSQL met Patroni: eerst etcd op alle nodes, omdat etcd pas begint
// als de meerderheid er is, en daarna Patroni node voor node. De eerste
// datanode maakt het cluster, de andere worden een replica die bijhoudt.
// Pas dan krijgt de volgende node Patroni.

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/deploy"
	"github.com/Jonasz1996/clusterforge/internal/jobs"
	"github.com/Jonasz1996/clusterforge/internal/templates"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// EtcdMarker is het merkteken dat etcd van het cluster met deze slug op de
// node gestart is. De stap die etcd start, heeft het als creates; tot en
// met die stap doet de uitrol eerst alle nodes.
func EtcdMarker(slug string) string { return "/var/lib/clusterforge/etcd-" + slug }

// checkExistingPG kijkt wat er al aan PostgreSQL op een datanode staat. Een
// leeg cluster main van Debian haalt het onderdeel weg; al het andere
// blijft staan, dus dan kan het niet.
func checkExistingPG(pn *PlanNode, n *Node, st dbStatus, slug string, add func(string, string, *Node, string, string)) {
	srv := st.server
	var own []string
	if srv != nil {
		for _, d := range srv.Databases {
			if d.Name != "postgres" {
				own = append(own, d.Name)
			}
		}
	}
	switch {
	case st.err != nil:
		add(Blocked, "no_answer", n, fmt.Sprintf("de agent op %s zei niet welke PostgreSQL er draait: %v", n.Hostname, st.err), "")
	case srv == nil:
	case srv.Unit == "patroni":
		scope := ""
		if srv.Patroni != nil {
			scope = srv.Patroni.Scope
		}
		pn.Existing = strings.TrimSpace("Patroni " + scope)
		if scope == slug {
			add(Blocked, "patroni_exists", n, fmt.Sprintf("op %s draait al Patroni-cluster %s; ClusterForge kent het niet als onderdeel. "+
				"Haal het eerst weg of neem andere nodes", n.Hostname, slug), "")
		} else {
			add(Blocked, "patroni_exists", n, fmt.Sprintf("op %s draait al Patroni (%s)", n.Hostname, cmp.Or(scope, "een ander cluster")), "")
		}
	case len(own) > 0:
		pn.Existing = "PostgreSQL " + srv.Version + " met " + strings.Join(own, ", ")
		add(Blocked, "postgresql_in_use", n, fmt.Sprintf("op %s staat al een PostgreSQL met databases (%s). ClusterForge zet er geen cluster overheen; "+
			"verhuis ze eerst of neem andere nodes", n.Hostname, strings.Join(own, ", ")), "")
	case srv.Unit != "postgresql" && !strings.HasSuffix(srv.Unit, "-main"):
		pn.Existing = "PostgreSQL " + srv.Version + " (" + srv.Unit + ")"
		add(Blocked, "postgresql_other", n, fmt.Sprintf("op %s draait al een PostgreSQL-cluster (%s) dat niet main heet; ClusterForge haalt alleen "+
			"een leeg cluster main weg. Haal het eerst weg of neem andere nodes", n.Hostname, srv.Unit), "")
	case srv.Running:
		pn.Existing = "PostgreSQL " + srv.Version + " zonder eigen databases"
		add(Warning, "postgresql_takeover", n, fmt.Sprintf("op %s draait al een leeg PostgreSQL-cluster main; ClusterForge haalt het weg en "+
			"Patroni start PostgreSQL voortaan zelf", n.Hostname), "")
	default:
		// Een stilstaand cluster toont geen databases; misschien staan ze er
		// wel.
		pn.Existing = "PostgreSQL staat erop maar draait niet"
		add(Blocked, "postgresql_stopped", n, fmt.Sprintf("op %s staat PostgreSQL (%s) maar die draait niet, dus ClusterForge ziet niet welke "+
			"databases erin staan en zet er geen cluster overheen. Start hem eerst; kijk op de pagina Databases bij Zelfherstel als hij niet start",
			n.Hostname, srv.Unit), "systemctl start "+srv.Unit)
	}
}

// runPatroni is de uitrol van PostgreSQL met Patroni.
func (s *Service) runPatroni(ctx context.Context, j *jobs.Job, r *rendered) error {
	slug := r.spec.Cluster.Slug
	// Eerst etcd: op elke node de stappen tot en met de start.
	for _, n := range r.spec.order() {
		if err := j.Step(ctx, n.Hostname+": etcd", func(ctx context.Context, st *jobs.Step) error {
			steps, err := deploy.RenderNode(r.tpl, r.spec.deploySpec(), r.secrets, r.states, n.NodeID)
			if err != nil {
				return err
			}
			i := slices.IndexFunc(steps, func(s templates.Step) bool { return s.Command != nil && s.Command.Creates == EtcdMarker(slug) })
			if i < 0 {
				return errors.New("de stap die etcd start, ontbreekt in het onderdeel")
			}
			if n.Role == RoleData {
				steps = steps[:i+1]
			}
			joins := slices.Contains(r.join, n.NodeID)
			if joins {
				// Een nieuwe node wordt eerst lid, en sluit dan aan bij de
				// leden die er zijn.
				if err := s.etcdJoin(ctx, j, st, r, n); err != nil {
					return err
				}
				steps = append(steps, etcdWait())
			}
			_, err = deploy.ApplySteps(ctx, s.bus, options(ctx, j, st, n, "-etcd", s.Retry), steps)
			if err == nil && joins {
				st.Logf("etcd doet mee op %s", n.Hostname)
			}
			return err
		}); err != nil {
			return err
		}
	}
	// Dan Patroni, node voor node.
	for _, n := range r.spec.order() {
		if n.Role != RoleData {
			continue
		}
		if err := j.Step(ctx, n.Hostname+": PostgreSQL", func(ctx context.Context, st *jobs.Step) error {
			steps, err := deploy.RenderNode(r.tpl, r.spec.deploySpec(), r.secrets, r.states, n.NodeID)
			if err != nil {
				return err
			}
			if _, err := deploy.ApplySteps(ctx, s.bus, options(ctx, j, st, n, "", s.Retry), steps); err != nil {
				return err
			}
			return s.waitPatroni(ctx, st, n, slug)
		}); err != nil {
			return err
		}
	}
	if (r.revision <= 1 && !r.restart) || len(r.join) > 0 {
		// De eerste revisie: Patroni nam de instellingen mee bij het begin.
		// Een nieuwe node leest ze uit etcd. Dat hangt af van de taak, niet
		// van wat er al toegepast is, zodat een hervatte taak dezelfde
		// stappen heeft.
		return nil
	}
	return s.patroniSettings(ctx, j, r, r.restart)
}

// patroniHealthy zegt of Patroni op een node meedoet in het cluster slug:
// de primary die draait, of een replica die de primary bijhoudt. Een
// oudere Patroni zegt niet of een replica bijhoudt; dan telt draaien.
func patroniHealthy(srv *protocol.DBServer, slug string) (string, bool) {
	switch {
	case srv == nil:
		return "Patroni draait niet", false
	case srv.Patroni == nil:
		return cmp.Or(srv.Error, "PostgreSQL draait zonder Patroni"), false
	case srv.Patroni.Error != "":
		return srv.Patroni.Error, false
	case srv.Patroni.Scope != slug:
		return fmt.Sprintf("Patroni hoort bij cluster %s in plaats van %s", srv.Patroni.Scope, slug), false
	}
	p := srv.Patroni
	state := fmt.Sprintf("%s, %s", cmp.Or(p.Role, "nog geen rol"), cmp.Or(p.State, "onbekend"))
	if p.Replication != "" {
		state += ", " + p.Replication
	}
	if p.Sync {
		state += ", synchroon"
	}
	if p.State != "running" {
		return state, false
	}
	switch p.Role {
	case "primary", "standby_leader":
		return state, true
	case "replica", "sync_standby":
		return state, p.Replication == "" || p.Replication == "streaming"
	}
	return state, false
}

// waitPatroni wacht tot Patroni op een node meedoet: de eerste wordt de
// primary, de andere een replica die bijhoudt. Een nieuwe replica kopieert
// eerst alle data, dus dat kan bij een grote database even duren.
func (s *Service) waitPatroni(ctx context.Context, st *jobs.Step, n Node, slug string) error {
	return s.waitServer(ctx, st, n.Hostname+" doet mee in Patroni", n.NodeID, protocol.DBPostgreSQL, func(srv *protocol.DBServer, err error) (string, bool) {
		if err != nil {
			return fmt.Sprintf("%s: %v", n.Hostname, err), false
		}
		state, ok := patroniHealthy(srv, slug)
		return n.Hostname + ": " + state, ok
	})
}

// waitServer vraagt een node om de paar seconden naar de databaseserver van
// engine tot ok, en schrijft elke nieuwe toestand in het logboek.
func (s *Service) waitServer(ctx context.Context, st *jobs.Step, what string, nodeID uuid.UUID, engine string,
	check func(*protocol.DBServer, error) (string, bool)) error {
	deadline := s.Now().Add(s.GateTimeout)
	last := ""
	for {
		srv, err := s.server(ctx, nodeID, engine)
		if ctx.Err() != nil {
			return context.Cause(ctx)
		}
		state, ok := check(srv, err)
		if state != last {
			st.Logf("%s", state)
			_ = st.Flush(ctx)
			last = state
		}
		if ok {
			return nil
		}
		if s.Now().After(deadline) {
			return fmt.Errorf("niet gelukt binnen %s: %s (laatst: %s)", s.GateTimeout, what, state)
		}
		select {
		case <-ctx.Done():
			return context.Cause(ctx)
		case <-time.After(s.Poll):
		}
	}
}

// verifyPatroni controleert het hele cluster: één primary, de andere
// datanodes replica's die bijhouden, allemaal in hetzelfde cluster op
// dezelfde tijdlijn.
func (s *Service) verifyPatroni(ctx context.Context, st *jobs.Step, spec Spec) (string, error) {
	slug := spec.Cluster.Slug
	var primaries, syncs []string
	timelines := map[int][]string{}
	for _, n := range spec.order() {
		if n.Role != RoleData {
			continue
		}
		srv, err := s.server(ctx, n.NodeID, protocol.DBPostgreSQL)
		if err != nil {
			return "", fmt.Errorf("%s: %w", n.Hostname, err)
		}
		state, ok := patroniHealthy(srv, slug)
		if !ok {
			return "", fmt.Errorf("%s: %s", n.Hostname, state)
		}
		p := srv.Patroni
		if p.Role == "primary" {
			primaries = append(primaries, n.Hostname)
		}
		if p.Sync {
			syncs = append(syncs, n.Hostname)
		}
		timelines[p.Timeline] = append(timelines[p.Timeline], n.Hostname)
	}
	switch {
	case len(primaries) == 0:
		return "", errors.New("geen enkele node is de primary")
	case len(primaries) > 1:
		return "", fmt.Errorf("%s zijn allemaal primary (split-brain); kijk of etcd op alle nodes draait", joinHosts(primaries))
	case len(timelines) > 1:
		var parts []string
		for _, tl := range slices.Sorted(maps.Keys(timelines)) {
			parts = append(parts, fmt.Sprintf("%s op tijdlijn %d", joinHosts(timelines[tl]), tl))
		}
		return "", fmt.Errorf("de nodes zitten niet op dezelfde tijdlijn: %s", strings.Join(parts, ", "))
	}
	data := spec.count(RoleData)
	st.Logf("%s is de primary, de andere %d datanodes zijn replica's die bijhouden", primaries[0], data-1)
	if len(syncs) > 0 {
		st.Logf("synchrone replica: %s; een bevestigde schrijfactie staat ook daar", joinHosts(syncs))
	} else if data > 1 {
		st.Logf("nog geen synchrone replica; Patroni kiest er een zodra een replica bijhoudt")
	}
	return primaries[0], nil
}
