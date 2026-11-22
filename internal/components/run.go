package components

import (
	"cmp"
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
	galerapkg "github.com/Jonasz1996/clusterforge/internal/galera"
	"github.com/Jonasz1996/clusterforge/internal/jobs"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/internal/templates"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// jobParams gaan mee met de taak: welk onderdeel in welke revisie. Bij
// nieuwe instellingen staat de volgorde van de nodes erbij, met de schrijver
// als laatste, en herstart PostgreSQL op elke node (Restart). Join zijn de
// nodes die de taak toevoegt; de andere herstarten dan niet.
type jobParams struct {
	ComponentID uuid.UUID   `json:"component_id"`
	Revision    int         `json:"revision"`
	Order       []uuid.UUID `json:"order,omitempty"`
	Restart     bool        `json:"restart,omitempty"`
	Join        []uuid.UUID `json:"join,omitempty"`
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
	// vrrp is het wachtwoord van VRRP voor een eigen VIP.
	vrrp string
	// revision is de revisie die de taak toepast, order de volgorde van de
	// nodes, restart of PostgreSQL op elke node herstart en join de nodes
	// die erbij komen; uit de parameters van de taak.
	revision int
	order    []uuid.UUID
	restart  bool
	join     []uuid.UUID
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
	own := spec.VIP != nil && spec.VIP.Own
	if own {
		names = append(names, secretName(comp.Engine, vrrpSecret))
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
	r := &rendered{comp: comp, spec: spec, tpl: tpl, secrets: secrets, states: states, revision: p.Revision, order: p.Order, restart: p.Restart, join: p.Join}
	if own {
		r.vrrp = opened[secretName(comp.Engine, vrrpSecret)]
	}
	return r, nil
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
	if r.comp.Engine == protocol.DBPostgreSQL {
		if err := s.runPatroni(ctx, j, r); err != nil {
			return err
		}
	} else if err := s.runGalera(ctx, j, r); err != nil {
		return err
	}
	if r.spec.VIP != nil {
		nodes, err := s.vipOrder(ctx, r.spec)
		if err != nil {
			return err
		}
		for _, n := range nodes {
			if err := j.Step(ctx, fmt.Sprintf("%s: VIP %s", n.Hostname, r.spec.VIP.Address), func(ctx context.Context, st *jobs.Step) error {
				if r.spec.VIP.Own {
					return s.applyOwnVIP(ctx, j, st, r, n)
				}
				return s.applyVIP(ctx, j, st, r, n)
			}); err != nil {
				return err
			}
		}
	}
	return j.Step(ctx, "Controleren", func(ctx context.Context, st *jobs.Step) error {
		if r.comp.Engine == protocol.DBPostgreSQL {
			primary, err := s.verifyPatroni(ctx, st, r.spec)
			if err != nil {
				return err
			}
			if err := s.waitOwnVIP(ctx, st, r.spec, primary); err != nil {
				return err
			}
		} else {
			if err := s.verify(ctx, st, r.spec); err != nil {
				return err
			}
			if err := s.verifyVIP(ctx, st, r.spec); err != nil {
				return err
			}
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

// runGalera zet MariaDB op de nodes, een voor een: de datanodes op
// volgnummer, daarna de scheidsrechter. Bij nieuwe instellingen staat de
// node met het VIP als laatste van de datanodes.
func (s *Service) runGalera(ctx context.Context, j *jobs.Job, r *rendered) error {
	data := r.spec.count(RoleData)
	for _, n := range r.spec.ordered(r.order) {
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
	return nil
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
	srv, srvErr := s.mariadb(ctx, n.NodeID)
	var before *protocol.DBGalera
	if srvErr == nil && srv != nil && srv.Running {
		before = srv.Galera
	}
	member := healthy(before, slug, 1)
	// again: de eerste node begint het cluster ook opnieuw als hij het
	// merkteken van een eerdere poging heeft.
	again := false
	if n.Index == 1 {
		// De eerste node begint het cluster, maar alleen als er nog geen is:
		// doet een andere node al mee in een Primary component met deze
		// naam, dan sluit hij gewoon aan.
		other, err := s.primaryElsewhere(ctx, r.spec, n.NodeID)
		if err != nil {
			return err
		}
		if !other {
			// Na een uitrol die niet af kwam, staat het merkteken op de
			// eerste node terwijl het cluster niet meer draait. Heeft geen
			// andere datanode data van dit cluster, dan begint hij het
			// opnieuw; anders kan een andere node nieuwere data hebben.
			if !member && r.comp.AppliedRevision == nil {
				var why string
				if again, why = outside(srv, srvErr, n.Hostname); !again {
					st.Logf("zat %s al in het cluster, dan begint de taak het daar niet opnieuw: %s", n.Hostname, why)
				} else if again, why = onlyHolder(s.peers(ctx, r.spec, n.NodeID)); !again {
					st.Logf("zat %s al in het cluster, dan begint de taak het daar niet opnieuw: %s. Begin het dan met Zelfherstel op de pagina Databases (Nu herstellen), dat eerst de positie van elke node vergelijkt", n.Hostname, why)
				}
			}
			if steps, err = bootstrap(steps, slug, member, again); err != nil {
				return err
			}
			if !member {
				st.Logf("%s begint het Galera-cluster %s", n.Hostname, slug)
			}
		}
	}
	o := options(ctx, j, st, n, "", s.Retry)
	// tables zijn de databases die de stap dataTitle vond.
	var tables string
	if again {
		logf := o.Logf
		o.Logf = func(format string, a ...any) {
			line := fmt.Sprintf(format, a...)
			if v, ok := strings.CutPrefix(strings.TrimSpace(line), tablesPrefix); ok {
				tables = strings.TrimSpace(v)
			}
			logf("%s", line)
		}
	}
	changed, err := deploy.ApplySteps(ctx, s.bus, o, steps)
	if err != nil {
		if again && tables != "" && strings.HasPrefix(err.Error(), dataTitle+": ") {
			return fmt.Errorf("op %s staan al databases met tabellen (%s). ClusterForge begint het cluster daar niet zonder je bevestiging opnieuw: "+
				"die databases worden dan de data van het cluster, en de andere nodes krijgen er een kopie van. Wil je dat, begin het cluster dan met Zelfherstel op de pagina Databases (Nu herstellen) en kies daarna opnieuw Opnieuw toepassen",
				n.Hostname, strings.Join(strings.Fields(tables), ", "))
		}
		return err
	}
	switch {
	case member && changed > 0 && r.joining(n):
		// Er komt een node bij: zijn adres geldt hier bij de volgende start.
		// MariaDB die draait, hoort de nieuwe node via Galera zelf.
		st.Logf("%s krijgt de nieuwe node in zijn configuratie voor de volgende start; MariaDB herstart niet", n.Hostname)
	case member && changed > 0 && !s.joinable(ctx, r.spec, n.NodeID):
		// Een node die als enige meedoet, vindt na een herstart niemand om
		// bij aan te sluiten, en dan draait er geen cluster meer.
		st.Logf("%s doet als enige mee. Een herstart zou het cluster stoppen, want bij het starten vindt hij geen andere node; MariaDB herstart dus niet, en de nieuwe configuratie geldt bij zijn volgende start", n.Hostname)
	case member && changed > 0:
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

// joinable is true als een andere datanode meedoet in een Primary component
// van dit cluster: een node die herstart, sluit daar weer bij aan. Een node
// die niet antwoordt, telt niet mee.
func (s *Service) joinable(ctx context.Context, spec Spec, self uuid.UUID) bool {
	for _, n := range spec.Nodes {
		if n.Role != RoleData || n.NodeID == self {
			continue
		}
		if g, err := s.galera(ctx, n.NodeID); err == nil && g != nil && g.ClusterName == spec.Cluster.Slug && g.ClusterStatus == "Primary" {
			return true
		}
	}
	return false
}

// peer is wat een andere datanode over zijn MariaDB zegt, voor
// onlyHolder.
type peer struct {
	host string
	err  error
	// server: er staat MariaDB; running: hij draait.
	server  bool
	running bool
	// fresh: volgens InnoDB zat hij nog nooit in een Galera-cluster.
	fresh bool
}

// peers vraagt de andere datanodes naar hun MariaDB.
func (s *Service) peers(ctx context.Context, spec Spec, self uuid.UUID) []peer {
	var out []peer
	for _, n := range spec.order() {
		if n.Role != RoleData || n.NodeID == self {
			continue
		}
		srv, err := s.mariadb(ctx, n.NodeID)
		out = append(out, peerOf(n.Hostname, srv, err, func() (*protocol.DBPositionResult, error) { return s.position(ctx, n, true) }))
	}
	return out
}

// peerOf maakt een peer van de status van zijn MariaDB, en van zijn
// positie als die stilstaat; position mag die uit InnoDB lezen. Een unit
// die nog draait of start, telt als draaiend, ook als de agent er niet in
// kan. Alleen InnoDB zegt zeker dat een node nog nooit in een cluster zat;
// een positie die onbekend is, zegt niet of er data staat.
func peerOf(host string, srv *protocol.DBServer, err error, position func() (*protocol.DBPositionResult, error)) peer {
	p := peer{host: host}
	switch {
	case err != nil:
		p.err = err
		return p
	case srv == nil:
		return p
	}
	p.server = true
	if srv.Running || srv.Active == "active" || srv.Active == "activating" || srv.Active == "reloading" {
		p.running = true
		return p
	}
	pos, err := position()
	switch {
	case err != nil:
		p.err = err
	case pos.Running:
		p.running = true
	case pos.NeverInCluster():
		p.fresh = true
	case strings.Trim(pos.UUID, "0-") == "":
		// Geen geschiedenis in grastate.dat, maar InnoDB is niet gelezen.
		p.err = errors.New(cmp.Or(pos.Error, "de agent gaf geen positie"))
	}
	return p
}

// outside zegt of MariaDB op de eerste node zeker niet in een Primary
// component zit: er staat geen MariaDB, hij staat stil, of hij draait
// buiten een Primary component. Anders zegt why waarom niet; een donor
// doet bijvoorbeeld nog mee, ook al is hij niet Synced.
func outside(srv *protocol.DBServer, err error, host string) (ok bool, why string) {
	switch {
	case err != nil:
		return false, fmt.Sprintf("%s zegt niet hoe MariaDB ervoor staat (%v)", host, err)
	case srv == nil:
		return true, ""
	case srv.Running && srv.Galera != nil && srv.Galera.ClusterStatus == "Primary":
		return false, fmt.Sprintf("MariaDB op %s zit nog in een Primary component (%s)", host, cmp.Or(srv.Galera.State, "niet Synced"))
	case srv.Running && srv.Galera == nil && srv.Error != "":
		// De status van Galera kwam niet: misschien doet hij nog mee.
		return false, fmt.Sprintf("%s zegt niet of MariaDB in een Primary component zit (%s)", host, srv.Error)
	case srv.Running, srv.Active == "inactive", srv.Active == "failed":
		return true, ""
	}
	return false, fmt.Sprintf("MariaDB op %s is %s", host, cmp.Or(srv.Active, "in een onbekende toestand"))
}

// onlyHolder zegt of de eerste node als enige data van het cluster kan
// hebben: op elke andere datanode staat geen MariaDB, of een die stilstaat
// en nog nooit in een Galera-cluster zat. Anders zegt why waarom niet.
func onlyHolder(peers []peer) (ok bool, why string) {
	for _, p := range peers {
		switch {
		case p.err != nil:
			return false, fmt.Sprintf("%s zegt niet of daar data staat (%v)", p.host, p.err)
		case !p.server:
		case p.running:
			return false, "op " + p.host + " draait MariaDB"
		case !p.fresh:
			return false, p.host + " zat al in een Galera-cluster en kan nieuwere data hebben"
		}
	}
	return true, ""
}

// applyArbiter zet garbd op de scheidsrechter en wacht tot het cluster
// hem meetelt. Komt er een node bij, dan herstart garbd niet: het nieuwe
// adres geldt bij zijn volgende start.
func (s *Service) applyArbiter(ctx context.Context, j *jobs.Job, st *jobs.Step, r *rendered, n Node, want int) error {
	steps, err := deploy.RenderNode(r.tpl, r.spec.deploySpec(), r.secrets, r.states, n.NodeID)
	if err != nil {
		return err
	}
	if r.joining(n) {
		steps = quiet(steps)
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

// dataTitle is de stap die nakijkt of er geen databases met tabellen op de
// eerste node staan, voor hij het cluster opnieuw begint. Vindt ze er,
// dan schrijft ze tablesPrefix met hun namen.
const (
	dataTitle    = "nakijken of er geen databases met tabellen staan"
	tablesPrefix = "databases met tabellen: "
)

// userDatabases zet de databases met tabellen in de datamap van MariaDB op
// één regel, zonder die van MariaDB zelf. Een database met alleen db.opt is
// leeg. Het werkt ook als MariaDB stilstaat.
const userDatabases = "for d in " + galerapkg.DataDir + "/*/; do n=${d%/}; n=${n##*/}; case $n in mysql|performance_schema|sys|lost+found|'#'*) continue;; esac; " +
	`ls -A "$d" 2>/dev/null | grep -qvx db.opt && printf '%s ' "$n"; done`

// bootstrap vervangt de stap die aansluit door een die het cluster begint.
// Doet de node al mee in een Primary component met deze naam (een eerdere
// poging begon het al), dan zet ze alleen het merkteken. Met again begint
// de node het cluster ook als het merkteken er al staat, maar niet als hij
// al mag schrijven of als er databases met tabellen op staan.
func bootstrap(steps []templates.Step, slug string, member, again bool) ([]templates.Step, error) {
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
	if !again || member {
		return out, nil
	}
	// Begon de stap hierboven het cluster, dan mag de node schrijven en
	// slaan deze twee over.
	extra := []templates.Step{
		{Step: protocol.Step{Command: &protocol.CommandStep{
			Run:    `echo "` + tablesPrefix + `$(` + userDatabases + `)"; exit 3`,
			Unless: protocol.DBCheckCommand + ` || [ -z "$(` + userDatabases + `)" ]`,
		}}, Title: dataTitle},
		{Step: protocol.Step{Command: &protocol.CommandStep{Run: "(" + galerapkg.BootstrapAny + ") && touch " + m, Unless: protocol.DBCheckCommand}},
			Title: "het Galera-cluster opnieuw beginnen"},
	}
	return slices.Insert(out, i+1, extra...), nil
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
