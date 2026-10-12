package components

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Jonasz1996/clusterforge/internal/deploy"
	"github.com/Jonasz1996/clusterforge/internal/deps"
	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/jobs"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/internal/templates"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// jobParams gaan mee met de taak: welk onderdeel in welke revisie.
type jobParams struct {
	ComponentID uuid.UUID `json:"component_id"`
	Revision    int       `json:"revision"`
}

// Marker is het merkteken dat een node meedoet in het Galera-cluster met
// deze slug. De stap die aansluit, heeft het als creates.
func Marker(slug string) string { return "/var/lib/clusterforge/galera-" + slug }

// synced is de toestand 4 van wsrep_local_state.
const synced = 4

// rendered is wat een taak nodig heeft om een node te doen.
type rendered struct {
	comp    store.ClusterComponent
	spec    Spec
	tpl     *templates.Template
	secrets map[string]string
	states  map[uuid.UUID]deploy.NodeState
}

func (s *Service) load(ctx context.Context, p jobParams) (*rendered, error) {
	comp, err := s.q.GetClusterComponent(ctx, p.ComponentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errors.New("het onderdeel bestaat niet meer")
	}
	if err != nil {
		return nil, err
	}
	rev, err := s.q.GetClusterComponentRevision(ctx, store.GetClusterComponentRevisionParams{ComponentID: comp.ID, Revision: int32(p.Revision)}) //nolint:gosec // een revisie past altijd
	if err != nil {
		return nil, fmt.Errorf("revisie %d van het onderdeel: %w", p.Revision, err)
	}
	spec, err := parseSpec(rev.Spec)
	if err != nil {
		return nil, err
	}
	tpl, ok := registry.Get(spec.Template.Name, spec.Template.Version)
	if !ok {
		return nil, fmt.Errorf("%s %s zit niet in deze server; zet de server terug op een versie die het kent", spec.Template.Name, spec.Template.Version)
	}
	names := make([]string, len(spec.Secrets))
	for i, n := range spec.Secrets {
		names[i] = secretName(comp.Engine, n)
	}
	opened, err := s.dep.OpenSecrets(ctx, comp.ClusterID, names)
	if err != nil {
		return nil, err
	}
	secrets := map[string]string{}
	for _, n := range spec.Secrets {
		secrets[n] = opened[secretName(comp.Engine, n)]
	}
	_, states, err := s.dep.Nodes(ctx, comp.ClusterID)
	if err != nil {
		return nil, err
	}
	return &rendered{comp: comp, spec: spec, tpl: tpl, secrets: secrets, states: states}, nil
}

func (s *Service) run(ctx context.Context, j *jobs.Job) error {
	var p jobParams
	if err := j.Decode(&p); err != nil {
		return err
	}
	actor := events.System()
	if j.RequestedBy != nil {
		actor = events.User(*j.RequestedBy)
	}
	r, err := s.load(ctx, p)
	if err != nil {
		return err
	}
	data := r.spec.count(RoleData)
	for _, n := range r.spec.order() {
		name := n.Hostname + ": MariaDB"
		// Na deze node doen er zoveel mee; een scheidsrechter komt na alle
		// datanodes.
		want := n.Index
		if n.Role == RoleArbiter {
			name, want = n.Hostname+": scheidsrechter", data+n.Index
		}
		if err := j.Step(ctx, name, func(ctx context.Context, st *jobs.Step) error {
			if n.Role == RoleArbiter {
				return s.applyArbiter(ctx, j, st, r, n, want)
			}
			return s.applyData(ctx, j, st, r, n, want)
		}); err != nil {
			return err
		}
	}
	if r.spec.VIP != nil {
		nodes, err := s.vipOrder(ctx, r.spec)
		if err != nil {
			return err
		}
		for _, n := range nodes {
			if err := j.Step(ctx, fmt.Sprintf("%s: VIP %s", n.Hostname, r.spec.VIP.Address), func(ctx context.Context, st *jobs.Step) error {
				return s.applyVIP(ctx, j, st, r, n)
			}); err != nil {
				return err
			}
		}
	}
	return j.Step(ctx, "Controleren", func(ctx context.Context, st *jobs.Step) error {
		if err := s.verify(ctx, st, r.spec); err != nil {
			return err
		}
		if err := s.verifyVIP(ctx, st, r.spec); err != nil {
			return err
		}
		if err := s.finish(ctx, actor, j, r, p.Revision); err != nil {
			return err
		}
		st.Logf("de database van %s draait op %s; revisie %d is toegepast", r.spec.Cluster.Name, joinHosts(r.spec.hosts(RoleData)), p.Revision)
		if s.Changed != nil {
			s.Changed()
		}
		return nil
	})
}

func options(ctx context.Context, j *jobs.Job, st *jobs.Step, n Node, suffix string, retry time.Duration) deploy.ApplyOptions {
	return deploy.ApplyOptions{
		NodeID: n.NodeID, Hostname: n.Hostname, CommandID: fmt.Sprintf("%s-%s%s", j.ID, n.NodeID, suffix), Retry: retry,
		Logf: st.Logf, Flush: func() { _ = st.Flush(ctx) },
	}
}

// applyData zet MariaDB op een datanode en wacht tot hij Synced is.
func (s *Service) applyData(ctx context.Context, j *jobs.Job, st *jobs.Step, r *rendered, n Node, want int) error {
	slug := r.spec.Cluster.Slug
	steps, err := deploy.RenderNode(r.tpl, r.spec.deploySpec(), r.secrets, r.states, n.NodeID)
	if err != nil {
		return err
	}
	before, _ := s.galera(ctx, n.NodeID)
	member := healthy(before, slug, 1)
	if n.Index == 1 {
		// De eerste node begint het cluster, maar alleen als er nog geen is:
		// doet een andere node al mee in een Primary component met deze
		// naam, dan sluit hij gewoon aan.
		other, err := s.primaryElsewhere(ctx, r.spec, n.NodeID)
		if err != nil {
			return err
		}
		if !other {
			if steps, err = bootstrap(steps, slug, member); err != nil {
				return err
			}
			if !member {
				st.Logf("%s begint het Galera-cluster %s", n.Hostname, slug)
			}
		}
	}
	changed, err := deploy.ApplySteps(ctx, s.bus, options(ctx, j, st, n, "", s.Retry), steps)
	if err != nil {
		return err
	}
	if member && changed > 0 {
		// De node deed al mee en kreeg een nieuwe configuratie: herstarten,
		// en pas verder als hij weer Synced is.
		st.Logf("%s deed al mee; MariaDB herstart om de nieuwe configuratie te lezen", n.Hostname)
		restart := []templates.Step{{Step: protocol.Step{Service: &protocol.ServiceStep{Name: "mariadb", State: "restarted"}}, Title: "service mariadb"}}
		if _, err := deploy.ApplySteps(ctx, s.bus, options(ctx, j, st, n, "-herstart", s.Retry), restart); err != nil {
			return err
		}
	}
	return s.waitSynced(ctx, st, n, slug, want)
}

// applyArbiter zet garbd op de scheidsrechter en wacht tot het cluster
// hem meetelt.
func (s *Service) applyArbiter(ctx context.Context, j *jobs.Job, st *jobs.Step, r *rendered, n Node, want int) error {
	steps, err := deploy.RenderNode(r.tpl, r.spec.deploySpec(), r.secrets, r.states, n.NodeID)
	if err != nil {
		return err
	}
	if _, err := deploy.ApplySteps(ctx, s.bus, options(ctx, j, st, n, "", s.Retry), steps); err != nil {
		return err
	}
	var first Node
	for _, x := range r.spec.order() {
		if x.Role == RoleData {
			first = x
			break
		}
	}
	return s.wait(ctx, st, n.Hostname+" telt mee", func(g *protocol.DBGalera, err error) (string, bool) {
		if err != nil {
			return fmt.Sprintf("%s niet te lezen: %v", first.Hostname, err), false
		}
		if g == nil {
			return first.Hostname + " draait geen Galera", false
		}
		return fmt.Sprintf("het cluster heeft %d leden volgens %s", g.ClusterSize, first.Hostname), healthy(g, r.spec.Cluster.Slug, want)
	}, first.NodeID)
}

// bootstrap vervangt de stap die aansluit door een die het cluster begint.
// Doet de node al mee in een Primary component met deze naam (een eerdere
// poging begon het al), dan zet ze alleen het merkteken.
func bootstrap(steps []templates.Step, slug string, member bool) ([]templates.Step, error) {
	m := Marker(slug)
	i := slices.IndexFunc(steps, func(s templates.Step) bool { return s.Command != nil && s.Command.Creates == m })
	if i < 0 {
		return nil, errors.New("de stap die bij het cluster aansluit, ontbreekt in het onderdeel")
	}
	out := slices.Clone(steps)
	c := *out[i].Command
	c.Run = "systemctl stop mariadb && galera_new_cluster && touch " + m
	if member {
		c.Run = "touch " + m
	}
	out[i].Command, out[i].Title = &c, "het Galera-cluster beginnen"
	return out, nil
}

// galera vraagt de toestand van Galera op een node; nil als er geen MariaDB
// of geen Galera draait.
func (s *Service) galera(ctx context.Context, nodeID uuid.UUID) (*protocol.DBGalera, error) {
	srv, err := s.mariadb(ctx, nodeID)
	if err != nil || srv == nil {
		return nil, err
	}
	if !srv.Running {
		return nil, errors.New(firstNonEmpty(srv.Error, "MariaDB draait niet"))
	}
	return srv.Galera, nil
}

// healthy is true als de node Synced is in een Primary component van dit
// cluster met minstens want leden.
func healthy(g *protocol.DBGalera, slug string, want int) bool {
	return g != nil && g.ClusterName == slug && g.ClusterStatus == "Primary" && g.StateCode == synced && g.ClusterSize >= want
}

// primaryElsewhere is true als een andere datanode al meedoet in een
// Primary component van dit cluster.
func (s *Service) primaryElsewhere(ctx context.Context, spec Spec, self uuid.UUID) (bool, error) {
	for _, n := range spec.Nodes {
		if n.Role != RoleData || n.NodeID == self {
			continue
		}
		srv, err := s.mariadb(ctx, n.NodeID)
		switch {
		case ctx.Err() != nil:
			return false, context.Cause(ctx)
		case err != nil:
			// Een node die niet antwoordt, kan meedoen. Het cluster dan
			// opnieuw beginnen zou er twee maken.
			return false, fmt.Errorf("%s zegt niet of hij al meedoet (%w); ClusterForge begint het cluster pas als dat zeker niet zo is", n.Hostname, err)
		case srv == nil || !srv.Running || srv.Galera == nil:
		case srv.Galera.ClusterName == spec.Cluster.Slug && srv.Galera.ClusterStatus == "Primary":
			return true, nil
		}
	}
	return false, nil
}

// waitSynced wacht tot een datanode Synced is in een cluster van minstens
// want leden.
func (s *Service) waitSynced(ctx context.Context, st *jobs.Step, n Node, slug string, want int) error {
	return s.wait(ctx, st, fmt.Sprintf("%s Synced in een cluster van %d", n.Hostname, want), func(g *protocol.DBGalera, err error) (string, bool) {
		switch {
		case err != nil:
			return fmt.Sprintf("%s: %v", n.Hostname, err), false
		case g == nil:
			return n.Hostname + ": MariaDB draait zonder Galera", false
		case g.ClusterName != slug:
			return fmt.Sprintf("%s doet mee in cluster %s in plaats van %s", n.Hostname, g.ClusterName, slug), false
		}
		return fmt.Sprintf("%s: %s, %s, %d leden", n.Hostname, g.State, g.ClusterStatus, g.ClusterSize), healthy(g, slug, want)
	}, n.NodeID)
}

// wait vraagt een node om de paar seconden naar Galera tot ok, en schrijft
// elke nieuwe toestand in het logboek.
func (s *Service) wait(ctx context.Context, st *jobs.Step, what string, check func(*protocol.DBGalera, error) (string, bool), nodeID uuid.UUID) error {
	deadline := s.Now().Add(s.GateTimeout)
	last := ""
	for {
		g, err := s.galera(ctx, nodeID)
		if ctx.Err() != nil {
			return context.Cause(ctx)
		}
		state, ok := check(g, err)
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

// verify controleert het hele cluster: elke datanode Synced, allemaal in
// hetzelfde cluster, met evenveel leden als er nodes zijn.
func (s *Service) verify(ctx context.Context, st *jobs.Step, spec Spec) error {
	votes := len(spec.Nodes)
	uuids := map[string][]string{}
	for _, n := range spec.order() {
		if n.Role != RoleData {
			continue
		}
		g, err := s.galera(ctx, n.NodeID)
		switch {
		case err != nil:
			return fmt.Errorf("%s: %w", n.Hostname, err)
		case !healthy(g, spec.Cluster.Slug, votes):
			if g == nil {
				return fmt.Errorf("%s draait geen Galera", n.Hostname)
			}
			return fmt.Errorf("%s is %s in een %s component van %d leden; verwacht Synced in Primary met %d", n.Hostname, g.State, g.ClusterStatus, g.ClusterSize, votes)
		}
		uuids[g.ClusterUUID] = append(uuids[g.ClusterUUID], n.Hostname)
		if g.ClusterSize > votes {
			st.Logf("%s ziet %d leden, meer dan de %d van ClusterForge", n.Hostname, g.ClusterSize, votes)
		}
	}
	if len(uuids) > 1 {
		var parts []string
		for id, hosts := range uuids {
			parts = append(parts, joinHosts(hosts)+" in "+id)
		}
		slices.Sort(parts)
		return fmt.Errorf("de nodes zitten in verschillende clusters met dezelfde naam: %s", strings.Join(parts, "; "))
	}
	st.Logf("alle %d datanodes Synced in één cluster met %d leden", spec.count(RoleData), votes)
	return nil
}

// finish legt vast dat de revisie toegepast is en zet MariaDB in het
// overzicht van de afhankelijkheden, als die dienst er nog niet is.
func (s *Service) finish(ctx context.Context, actor events.Actor, j *jobs.Job, r *rendered, revision int) error {
	services, err := r.tpl.RenderServices(r.spec.Params)
	if err != nil {
		return err
	}
	clusterID := r.comp.ClusterID
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		comp, err := q.LockClusterComponent(ctx, r.comp.ID)
		if err != nil {
			return err
		}
		if err := q.SetClusterComponentApplied(ctx, store.SetClusterComponentAppliedParams{ID: comp.ID, Revision: ptr(int32(revision))}); err != nil { //nolint:gosec // een revisie past altijd
			return err
		}
		var add []templates.Service
		for _, svc := range services {
			_, err := q.FindServiceByName(ctx, store.FindServiceByNameParams{ClusterID: &clusterID, Name: svc.Name})
			switch {
			case errors.Is(err, pgx.ErrNoRows):
				add = append(add, svc)
			case err != nil:
				return err
			}
		}
		if err := deps.FromTemplateTx(ctx, q, s.ev, actor, clusterID, r.spec.Cluster.Name, add); err != nil {
			return err
		}
		first := comp.AppliedRevision == nil
		return s.ev.Write(ctx, q, events.Event{
			Actor: actor, SubjectType: "cluster", SubjectID: clusterID.String(), ClusterID: &clusterID, Action: "cluster.database_applied",
			Payload: map[string]any{
				"name": r.spec.Cluster.Name, "engine": comp.Engine, "component": r.tpl.Title, "version": r.tpl.Version, "revision": revision,
				"nodes": r.spec.hosts(RoleData), "arbiter": r.spec.hosts(RoleArbiter), "vip": r.spec.vipAddress(), "first": first, "job_id": j.ID, "title": j.Title,
			},
		})
	})
}

func ptr[T any](v T) *T { return &v }

func firstNonEmpty(s ...string) string {
	for _, x := range s {
		if x != "" {
			return x
		}
	}
	return ""
}
