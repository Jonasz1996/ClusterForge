package components

// Opnieuw beginnen na een volledige uitval. Zit geen enkele datanode meer
// in een Primary component, dan begint Galera niet vanzelf: na een gewone
// stop van alle nodes, of als de nodes ver na elkaar opstarten, geven ze het
// op. De node met de nieuwste data moet het cluster beginnen; de andere
// sluiten bij hem aan. De taak component.recover doet dat: posities lezen,
// de beste node kiezen (internal/galera), het cluster daar beginnen en de
// andere nodes een voor een laten aansluiten.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Jonasz1996/clusterforge/internal/deploy"
	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/galera"
	"github.com/Jonasz1996/clusterforge/internal/jobs"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/internal/templates"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// RecoverKind is de taak die een database opnieuw begint.
const RecoverKind = "component.recover"

// Wat de taak met een datanode doet.
const (
	// ActionBegin: de node begint het cluster.
	ActionBegin = "begin"
	// ActionJoin: de node sluit aan.
	ActionJoin = "join"
	// ActionNone: de node doet al mee.
	ActionNone = "none"
	// ActionSkip: de node antwoordt niet en doet niet mee.
	ActionSkip = "skip"
	// ActionLater: welke node begint, beslist de taak pas na het lezen van
	// de posities na een crash.
	ActionLater = "later"
)

// obs is wat een datanode over zijn MariaDB zegt; pos is nil als de agent
// niet antwoordde, en err zegt dan waarom.
type obs struct {
	node Node
	pos  *protocol.DBPositionResult
	err  error
}

func (o obs) answered() bool { return o.err == nil && o.pos != nil }

// primary is true als de node in een Primary component van dit cluster
// zit.
func (o obs) primary(slug string) bool {
	return o.answered() && o.pos.Running && o.pos.Galera != nil && o.pos.Galera.ClusterName == slug && o.pos.Galera.ClusterStatus == "Primary"
}

// synced is true als de node meedoet: Synced in een Primary component.
func (o obs) synced(slug string) bool { return o.primary(slug) && o.pos.Galera.StateCode == synced }

// starting is true als systemd MariaDB nog aan het starten is, zoals een
// node die op de andere wacht.
func (o obs) starting() bool {
	return o.answered() && !o.pos.Running && o.pos.Active != "inactive" && o.pos.Active != "failed"
}

// describe zegt in een paar woorden hoe het met de node staat.
func (o obs) describe(slug string) string {
	switch {
	case !o.answered():
		return "antwoordt niet: " + o.err.Error()
	case o.pos.Running && o.pos.Galera == nil:
		return firstNonEmpty(o.pos.Error, "MariaDB draait zonder Galera")
	case o.pos.Running && o.pos.Galera.ClusterName != slug:
		return "doet mee in cluster " + o.pos.Galera.ClusterName
	case o.pos.Running:
		g := o.pos.Galera
		if g.ClusterStatus != "Primary" {
			return fmt.Sprintf("MariaDB draait, maar zonder meerderheid (%s)", g.ClusterStatus)
		}
		return fmt.Sprintf("%s in een cluster van %d", firstNonEmpty(g.State, "?"), g.ClusterSize)
	case o.pos.Active == "failed":
		return "MariaDB startte niet (failed)"
	case o.pos.Active == "inactive":
		return "MariaDB staat uit"
	}
	return "MariaDB start nog (" + o.pos.Active + ")"
}

// position vraagt een node waar zijn Galera staat. Met recover mag de agent
// galera_recovery draaien; dat duurt langer.
func (s *Service) position(ctx context.Context, n Node, recover bool) (*protocol.DBPositionResult, error) {
	timeout := s.Timeout
	if recover {
		timeout = protocol.DBRecoverTimeout + 30*time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	res, err := s.bus.DB(ctx, n.NodeID, protocol.DBRequest{
		ID: uuid.NewString(), Op: protocol.DBPosition, Deadline: time.Now().Add(timeout), Position: &protocol.DBPositionRequest{Recover: recover},
	})
	switch {
	case err != nil:
		return nil, err
	case strings.HasPrefix(res.Error, "onbekend databaseverzoek"):
		return nil, errors.New("de agent kent dit verzoek nog niet; werk hem bij met het installatiescript en --upgrade")
	case res.Error != "":
		return nil, errors.New(res.Error)
	case res.Position == nil:
		return nil, errors.New("de agent gaf geen positie")
	}
	return res.Position, nil
}

// observe vraagt alle nodes tegelijk waar ze staan.
func (s *Service) observe(ctx context.Context, nodes []Node, recover bool) []obs {
	out := make([]obs, len(nodes))
	var wg sync.WaitGroup
	for i, n := range nodes {
		wg.Go(func() {
			p, err := s.position(ctx, n, recover)
			out[i] = obs{node: n, pos: p, err: err}
		})
	}
	wg.Wait()
	return out
}

func dataNodes(spec Spec, skip []uuid.UUID) []Node {
	var out []Node
	for _, n := range spec.order() {
		if n.Role == RoleData && !slices.Contains(skip, n.NodeID) {
			out = append(out, n)
		}
	}
	return out
}

// RecoverNode is wat het plan van Opnieuw starten over een datanode zegt.
type RecoverNode struct {
	NodeID   uuid.UUID
	Hostname string
	Index    int
	Answered bool
	// State zegt in woorden hoe het met de node staat.
	State   string
	Active  string
	Running bool
	// UUID, Seqno en From zijn zijn positie; Seqno is -1 als die onbekend
	// is, zoals na een crash: de taak leest haar dan uit InnoDB.
	UUID            string
	Seqno           int64
	SafeToBootstrap bool
	From            string
	Action          string
}

// RecoverPlan is wat Opnieuw starten zou doen.
type RecoverPlan struct {
	ClusterID   uuid.UUID
	ClusterName string
	Slug        string
	Prod        bool
	Title       string
	Nodes       []RecoverNode
	// Primary is de node die al in een Primary component zit; leeg als de
	// database helemaal weg is.
	Primary string
	// Begin is de node die het cluster begint; leeg als het cluster nog
	// draait, of als dat pas na het lezen van de posities te zeggen is.
	Begin    string
	Findings []Finding
	Blocked  bool
	Hash     string
}

// RecoverPlan bekijkt wat Opnieuw starten zou doen. Er verandert niets: de
// agents lezen alleen, zonder galera_recovery.
func (s *Service) RecoverPlan(ctx context.Context, clusterID, id uuid.UUID) (RecoverPlan, error) {
	c, err := s.q.GetCluster(ctx, clusterID)
	if err != nil {
		return RecoverPlan{}, err
	}
	r, err := s.get(ctx, s.q, clusterID, id, false)
	if err != nil {
		return RecoverPlan{}, err
	}
	if err := mariaOnly(r, "Opnieuw starten na een uitval"); err != nil {
		return RecoverPlan{}, err
	}
	if r.AppliedRevision == nil {
		return RecoverPlan{}, &ConflictError{"deze database is nog nooit helemaal uitgerold; pas haar eerst toe"}
	}
	spec, err := parseSpec(r.Spec)
	if err != nil {
		return RecoverPlan{}, err
	}
	p := RecoverPlan{ClusterID: clusterID, ClusterName: c.Name, Slug: c.Slug, Prod: c.Environment == store.EnvironmentProd, Title: title(spec)}
	all := s.observe(ctx, dataNodes(spec, nil), false)
	slug := spec.Cluster.Slug
	add := func(level, code, host, msg string) {
		p.Findings = append(p.Findings, Finding{Level: level, Code: code, Hostname: host, Message: msg})
		if level == Blocked {
			p.Blocked = true
		}
	}
	var cands []galera.Candidate
	var answered []obs
	unknown := false
	for _, o := range all {
		n := RecoverNode{NodeID: o.node.NodeID, Hostname: o.node.Hostname, Index: o.node.Index, Answered: o.answered(), State: o.describe(slug), Seqno: -1}
		if o.answered() {
			n.Active, n.Running = o.pos.Active, o.pos.Running
			n.UUID, n.Seqno, n.SafeToBootstrap, n.From = o.pos.UUID, o.pos.Seqno, o.pos.SafeToBootstrap, o.pos.From
			answered = append(answered, o)
			pos := galera.Position{UUID: o.pos.UUID, Seqno: o.pos.Seqno, SafeToBootstrap: o.pos.SafeToBootstrap}
			// Van een node die draait zonder Primary component telt de
			// positie pas als hij gestopt is; de taak doet dat eerst.
			if o.pos.Running {
				pos.Seqno = -1
			}
			unknown = unknown || !pos.Known()
			cands = append(cands, galera.Candidate{Name: o.node.Hostname, Index: o.node.Index, Position: pos})
		}
		if o.primary(slug) && p.Primary == "" {
			p.Primary = o.node.Hostname
		}
		p.Nodes = append(p.Nodes, n)
	}
	missing := len(all) - len(answered)
	switch {
	case len(answered) == 0:
		add(Blocked, "no_nodes", "", "geen enkele datanode antwoordt; start de nodes eerst")
	case p.Primary != "":
		add(Warning, "running", p.Primary, fmt.Sprintf("de database draait op %s; de andere nodes sluiten daar aan, zonder het cluster opnieuw te beginnen", p.Primary))
	case missing > 0:
		var hosts []string
		for _, o := range all {
			if !o.answered() {
				hosts = append(hosts, o.node.Hostname)
			}
		}
		add(Warning, "missing", joinHosts(hosts), fmt.Sprintf("%s antwoordt niet en doet niet mee. Heeft %s nieuwere data dan de andere nodes, dan is die "+
			"weg zodra %s later bij het nieuwe cluster aansluit. Kan %s nog terugkomen, start hem dan eerst.", joinHosts(hosts), joinHosts(hosts), joinHosts(hosts), joinHosts(hosts)))
	}
	switch {
	case p.Primary != "" || len(cands) == 0:
	case unknown:
		add(Warning, "after_crash", "", "na een crash staat de positie alleen in InnoDB; de taak leest haar eerst met galera_recovery en begint dan op de node met de nieuwste data")
	default:
		best, err := galera.Choose(cands)
		if err != nil {
			add(Blocked, "positions", "", err.Error())
		} else {
			p.Begin = best.Name
		}
	}
	for i := range p.Nodes {
		n := &p.Nodes[i]
		switch {
		case !n.Answered:
			n.Action = ActionSkip
		case p.Primary != "" && slices.ContainsFunc(answered, func(o obs) bool { return o.node.NodeID == n.NodeID && o.synced(slug) }):
			n.Action = ActionNone
		case p.Primary != "":
			n.Action = ActionJoin
		case p.Begin == "":
			n.Action = ActionLater
		case n.Hostname == p.Begin:
			n.Action = ActionBegin
		default:
			n.Action = ActionJoin
		}
	}
	p.Hash = recoverHash(r, p)
	return p, nil
}

// recoverHash vat samen wat er zou gebeuren: welke nodes meedoen en of het
// cluster nog draait, zonder de posities die bij het starten pas vastliggen.
func recoverHash(r store.ClusterComponent, p RecoverPlan) string {
	v := struct {
		Component uuid.UUID `json:"component"`
		Revision  int32     `json:"revision"`
		Running   bool      `json:"running"`
		Nodes     []string  `json:"nodes"`
		Blocked   bool      `json:"blocked"`
	}{Component: r.ID, Revision: deref(r.AppliedRevision), Running: p.Primary != "", Blocked: p.Blocked}
	for _, n := range p.Nodes {
		if n.Answered {
			v.Nodes = append(v.Nodes, n.NodeID.String())
		}
	}
	b, _ := json.Marshal(v)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// recoverParams gaan mee met de taak.
type recoverParams struct {
	ComponentID uuid.UUID `json:"component_id"`
	// Auto: ClusterForge begon zelf, na een volledige uitval.
	Auto bool `json:"auto"`
	// Skip zijn de datanodes die niet antwoordden toen Jonas bevestigde; ze
	// doen niet mee.
	Skip []uuid.UUID `json:"skip,omitempty"`
}

// Recover begint de database opnieuw volgens het plan met deze hash.
func (s *Service) Recover(ctx context.Context, actor events.Actor, clusterID, id uuid.UUID, hash string) (store.Job, error) {
	p, err := s.RecoverPlan(ctx, clusterID, id)
	if err != nil {
		return store.Job{}, err
	}
	if p.Hash != hash {
		return store.Job{}, ErrStale
	}
	if p.Blocked {
		for _, f := range p.Findings {
			if f.Level == Blocked {
				return store.Job{}, &ConflictError{f.Message}
			}
		}
	}
	params := recoverParams{ComponentID: id}
	for _, n := range p.Nodes {
		if !n.Answered {
			params.Skip = append(params.Skip, n.NodeID)
		}
	}
	return s.enqueueRecover(ctx, actor, clusterID, id, params, "")
}

// enqueueRecover zet de taak in de wachtrij, met een event dat zegt wie
// hem begon en waarom.
func (s *Service) enqueueRecover(ctx context.Context, actor events.Actor, clusterID, id uuid.UUID, params recoverParams, reason string) (store.Job, error) {
	c, err := s.q.GetCluster(ctx, clusterID)
	if err != nil {
		return store.Job{}, err
	}
	var j store.Job
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		r, err := s.get(ctx, q, clusterID, id, true)
		if err != nil {
			return err
		}
		if err := mariaOnly(r, "Opnieuw starten na een uitval"); err != nil {
			return err
		}
		spec, err := parseSpec(r.Spec)
		if err != nil {
			return err
		}
		what := "Database opnieuw starten"
		if params.Auto {
			what = "Database vanzelf opnieuw starten"
		}
		j, err = s.jobs.EnqueueForClusterTx(ctx, q, jobs.Spec{
			Kind: RecoverKind, Title: fmt.Sprintf("%s: %s op %s", what, title(spec), c.Name), Params: params, ClusterID: &clusterID, Actor: actor,
		})
		if err != nil {
			return err
		}
		if err := q.SetClusterComponentRecoverJob(ctx, store.SetClusterComponentRecoverJobParams{ID: r.ID, JobID: &j.ID}); err != nil {
			return err
		}
		if params.Auto {
			if err := q.SetClusterComponentAutoRecovered(ctx, store.SetClusterComponentAutoRecoveredParams{ID: r.ID, At: ptr(s.Now())}); err != nil {
				return err
			}
		}
		var skip []string
		for _, n := range spec.order() {
			if slices.Contains(params.Skip, n.NodeID) {
				skip = append(skip, n.Hostname)
			}
		}
		return s.ev.Write(ctx, q, events.Event{
			Actor: actor, SubjectType: "cluster", SubjectID: clusterID.String(), ClusterID: &clusterID, Action: "cluster.database_recovering",
			Payload: map[string]any{
				"name": c.Name, "component": title(spec), "auto": params.Auto, "reason": reason, "skip": skip, "job_id": j.ID, "title": j.Title,
			},
		})
	})
	var busy jobs.BusyError
	if errors.As(err, &busy) {
		return store.Job{}, &ConflictError{busy.Error()}
	}
	if err == nil {
		s.jobs.Kick()
	}
	return j, err
}

// recoverState is wat de eerste stap besliste; een hervatte taak gaat er
// mee verder.
type recoverState struct {
	// Primary is de node die al in een Primary component zat, Begin de
	// node die het cluster begint. Hoogstens een van beide is gezet.
	Primary *uuid.UUID `json:"primary,omitempty"`
	Begin   *uuid.UUID `json:"begin,omitempty"`
}

const stepPositions = "Posities lezen"

func (s *Service) runRecover(ctx context.Context, j *jobs.Job) error {
	var p recoverParams
	if err := j.Decode(&p); err != nil {
		return err
	}
	actor := events.System()
	if j.RequestedBy != nil {
		actor = events.User(*j.RequestedBy)
	}
	comp, err := s.q.GetClusterComponent(ctx, p.ComponentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return errors.New("het onderdeel bestaat niet meer")
	}
	if err != nil {
		return err
	}
	if comp.AppliedRevision == nil {
		return errors.New("deze database is nog nooit helemaal uitgerold")
	}
	r, err := s.load(ctx, jobParams{ComponentID: comp.ID, Revision: int(*comp.AppliedRevision)})
	if err != nil {
		return err
	}
	slug := r.spec.Cluster.Slug
	nodes := dataNodes(r.spec, p.Skip)
	var dec recoverState
	if err := j.Step(ctx, stepPositions, func(ctx context.Context, st *jobs.Step) error {
		d, err := s.decide(ctx, j.ID.String(), st, r, nodes)
		if err != nil {
			return err
		}
		dec = d
		return st.SetState(ctx, d)
	}); err != nil {
		return err
	}
	if dec.Primary == nil && dec.Begin == nil && !j.Done(stepPositions, &dec) {
		return errors.New("de beslissing van de eerste stap ontbreekt")
	}
	joined := 0
	if dec.Begin != nil {
		i := slices.IndexFunc(nodes, func(n Node) bool { return n.NodeID == *dec.Begin })
		if i < 0 {
			return errors.New("de gekozen node hoort niet meer bij de database")
		}
		n := nodes[i]
		if err := j.Step(ctx, n.Hostname+": het cluster beginnen", func(ctx context.Context, st *jobs.Step) error {
			return s.recoverBegin(ctx, j.ID.String(), st, r, n)
		}); err != nil {
			return err
		}
		joined = 1
	}
	for _, n := range nodes {
		if dec.Begin != nil && n.NodeID == *dec.Begin {
			continue
		}
		joined++
		want := joined
		if err := j.Step(ctx, n.Hostname+": aansluiten", func(ctx context.Context, st *jobs.Step) error {
			return s.recoverJoin(ctx, j.ID.String(), st, n, slug, want)
		}); err != nil {
			return err
		}
	}
	for _, n := range r.spec.order() {
		if n.Role != RoleArbiter || slices.Contains(p.Skip, n.NodeID) {
			continue
		}
		joined++
		want := joined
		if err := j.Step(ctx, n.Hostname+": scheidsrechter", func(ctx context.Context, st *jobs.Step) error {
			return s.recoverArbiter(ctx, j.ID.String(), st, nodes[0], n, slug, want)
		}); err != nil {
			return err
		}
	}
	return j.Step(ctx, "Controleren", func(ctx context.Context, st *jobs.Step) error {
		if err := s.verifyNodes(ctx, st, slug, nodes, joined); err != nil {
			return err
		}
		if err := s.verifyVIP(ctx, st, r.spec); err != nil {
			return err
		}
		begin := ""
		if dec.Begin != nil {
			for _, n := range nodes {
				if n.NodeID == *dec.Begin {
					begin = n.Hostname
				}
			}
		}
		var skip []string
		for _, n := range r.spec.order() {
			if slices.Contains(p.Skip, n.NodeID) {
				skip = append(skip, n.Hostname)
			}
		}
		if err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
			q := store.New(tx)
			if err := q.SetClusterComponentHealth(ctx, store.SetClusterComponentHealthParams{ID: comp.ID, DownReason: "", NodesOut: []byte("{}")}); err != nil {
				return err
			}
			return s.ev.Write(ctx, q, events.Event{
				Actor: actor, SubjectType: "cluster", SubjectID: comp.ClusterID.String(), ClusterID: &comp.ClusterID, Action: "cluster.database_recovered",
				Payload: map[string]any{
					"name": r.spec.Cluster.Name, "component": r.tpl.Title, "begin": begin, "nodes": hostsOf(nodes), "skip": skip, "auto": p.Auto,
					"job_id": j.ID, "title": j.Title,
				},
			})
		}); err != nil {
			return err
		}
		if begin != "" {
			st.Logf("de database van %s draait weer; %s begon het cluster", r.spec.Cluster.Name, begin)
		} else {
			st.Logf("de database van %s draait weer met alle %d nodes", r.spec.Cluster.Name, joined)
		}
		if s.Changed != nil {
			s.Changed()
		}
		return nil
	})
}

func hostsOf(nodes []Node) []string {
	out := make([]string, len(nodes))
	for i, n := range nodes {
		out[i] = n.Hostname
	}
	return out
}

// decide leest waar de nodes staan en kiest: zit er een al in een Primary
// component, dan sluiten de andere daar aan; anders begint de node met de
// nieuwste data.
func (s *Service) decide(ctx context.Context, id string, st *jobs.Step, r *rendered, nodes []Node) (recoverState, error) {
	slug := r.spec.Cluster.Slug
	all := s.observe(ctx, nodes, false)
	for _, o := range all {
		if !o.answered() {
			return recoverState{}, fmt.Errorf("%s antwoordt niet (%v); begin opnieuw met Opnieuw starten, dan doet %s niet mee", o.node.Hostname, o.err, o.node.Hostname)
		}
		st.Logf("%s: %s", o.node.Hostname, o.describe(slug))
	}
	for _, o := range all {
		if o.primary(slug) {
			st.Logf("%s zit al in een Primary component; de andere nodes sluiten daar aan", o.node.Hostname)
			return recoverState{Primary: &o.node.NodeID}, nil
		}
	}
	// Galera geeft de positie van een node pas als MariaDB er stilstaat.
	for _, o := range all {
		if !o.pos.Running && !o.starting() {
			continue
		}
		st.Logf("%s: MariaDB stoppen om de positie te lezen", o.node.Hostname)
		stop := []templates.Step{{Step: protocol.Step{Service: &protocol.ServiceStep{Name: "mariadb", State: "stopped"}}, Title: "service mariadb"}}
		if _, err := deployApplyAs(ctx, s, id, st, o.node, "-stop", stop); err != nil {
			return recoverState{}, err
		}
	}
	all = s.observe(ctx, nodes, true)
	var cands []galera.Candidate
	for _, o := range all {
		if !o.answered() {
			return recoverState{}, fmt.Errorf("%s antwoordt niet (%v)", o.node.Hostname, o.err)
		}
		pos := galera.Position{UUID: o.pos.UUID, Seqno: o.pos.Seqno, SafeToBootstrap: o.pos.SafeToBootstrap}
		if !pos.Known() {
			return recoverState{}, fmt.Errorf("de positie van %s is onbekend: %s", o.node.Hostname, firstNonEmpty(o.pos.Error, "geen seqno"))
		}
		safe := ""
		if pos.SafeToBootstrap {
			safe = ", stopte als laatste"
		}
		st.Logf("%s: seqno %d uit %s%s", o.node.Hostname, pos.Seqno, o.pos.From, safe)
		cands = append(cands, galera.Candidate{Name: o.node.Hostname, Index: o.node.Index, Position: pos})
	}
	best, err := galera.Choose(cands)
	if err != nil {
		return recoverState{}, err
	}
	i := slices.IndexFunc(nodes, func(n Node) bool { return n.Hostname == best.Name })
	st.Logf("%s heeft de nieuwste data (seqno %d) en begint het cluster", best.Name, best.Position.Seqno)
	return recoverState{Begin: &nodes[i].NodeID}, nil
}

// recoverBegin begint het cluster op n en wacht tot hij Synced is.
func (s *Service) recoverBegin(ctx context.Context, id string, st *jobs.Step, r *rendered, n Node) error {
	slug := r.spec.Cluster.Slug
	if g, _ := s.galera(ctx, n.NodeID); healthy(g, slug, 1) {
		st.Logf("%s zit al in een Primary component", n.Hostname)
		return nil
	}
	// Nooit twee clusters: is er ondertussen toch een, dan sluit n aan.
	other, err := s.primaryElsewhere(ctx, r.spec, n.NodeID)
	if err != nil {
		return err
	}
	if other {
		st.Logf("een andere node zit ondertussen in een Primary component; %s sluit daar aan", n.Hostname)
		return s.recoverJoin(ctx, id, st, n, slug, 1)
	}
	// Het commando slaat over als de node toch al mag schrijven.
	begin := []templates.Step{{Step: protocol.Step{Command: &protocol.CommandStep{Run: galera.Bootstrap, Unless: protocol.DBCheckCommand}},
		Title: "het Galera-cluster beginnen"}}
	if _, err := deployApplyAs(ctx, s, id, st, n, "-begin", begin); err != nil {
		return err
	}
	return s.waitSynced(ctx, st, n, slug, 1)
}

// recoverJoin start MariaDB op n opnieuw, tenzij hij al meedoet, en wacht
// tot hij Synced is in een cluster van minstens want leden.
func (s *Service) recoverJoin(ctx context.Context, id string, st *jobs.Step, n Node, slug string, want int) error {
	if g, _ := s.galera(ctx, n.NodeID); healthy(g, slug, 1) {
		st.Logf("%s doet al mee", n.Hostname)
		return s.waitSynced(ctx, st, n, slug, want)
	}
	restart := []templates.Step{{Step: protocol.Step{Service: &protocol.ServiceStep{Name: "mariadb", State: "restarted"}}, Title: "service mariadb"}}
	if _, err := deployApplyAs(ctx, s, id, st, n, "-join", restart); err != nil {
		return err
	}
	return s.waitSynced(ctx, st, n, slug, want)
}

// recoverArbiter start garbd opnieuw en wacht tot het cluster hem meetelt.
func (s *Service) recoverArbiter(ctx context.Context, id string, st *jobs.Step, first, n Node, slug string, want int) error {
	restart := []templates.Step{{Step: protocol.Step{Service: &protocol.ServiceStep{Name: "garb", State: "restarted"}}, Title: "service garb"}}
	if _, err := deployApplyAs(ctx, s, id, st, n, "-garb", restart); err != nil {
		return err
	}
	return s.wait(ctx, st, n.Hostname+" telt mee", func(g *protocol.DBGalera, err error) (string, bool) {
		if err != nil {
			return fmt.Sprintf("%s niet te lezen: %v", first.Hostname, err), false
		}
		if g == nil {
			return first.Hostname + " draait geen Galera", false
		}
		return fmt.Sprintf("het cluster heeft %d leden volgens %s", g.ClusterSize, first.Hostname), healthy(g, slug, want)
	}, first.NodeID)
}

// verifyNodes controleert dat de nodes Synced zijn in één cluster met
// minstens want leden.
func (s *Service) verifyNodes(ctx context.Context, st *jobs.Step, slug string, nodes []Node, want int) error {
	uuids := map[string][]string{}
	for _, n := range nodes {
		g, err := s.galera(ctx, n.NodeID)
		switch {
		case err != nil:
			return fmt.Errorf("%s: %w", n.Hostname, err)
		case g == nil:
			return fmt.Errorf("%s draait geen Galera", n.Hostname)
		case !healthy(g, slug, want):
			return fmt.Errorf("%s is %s in een %s component van %d leden; verwacht Synced in Primary met %d", n.Hostname, g.State, g.ClusterStatus, g.ClusterSize, want)
		}
		uuids[g.ClusterUUID] = append(uuids[g.ClusterUUID], n.Hostname)
	}
	if len(uuids) > 1 {
		var parts []string
		for id, hosts := range uuids {
			parts = append(parts, joinHosts(hosts)+" in "+id)
		}
		slices.Sort(parts)
		return fmt.Errorf("de nodes zitten in verschillende clusters met dezelfde naam: %s", strings.Join(parts, "; "))
	}
	st.Logf("%d datanodes Synced in één cluster met %d leden", len(nodes), want)
	return nil
}

func deployApply(ctx context.Context, s *Service, j *jobs.Job, st *jobs.Step, n Node, suffix string, steps []templates.Step) (int, error) {
	return deployApplyAs(ctx, s, j.ID.String(), st, n, suffix, steps)
}

// deployApplyAs is deployApply met id voor het begin van het id van het
// commando, zoals het id van de taak.
func deployApplyAs(ctx context.Context, s *Service, id string, st *jobs.Step, n Node, suffix string, steps []templates.Step) (int, error) {
	o := deploy.ApplyOptions{
		NodeID: n.NodeID, Hostname: n.Hostname, CommandID: fmt.Sprintf("%s-%s%s", id, n.NodeID, suffix), Retry: s.Retry,
		Logf: st.Logf, Flush: func() { _ = st.Flush(ctx) },
	}
	return deploy.ApplySteps(ctx, s.bus, o, steps)
}

// SetAutoRecover zet vanzelf opnieuw starten aan of uit.
func (s *Service) SetAutoRecover(ctx context.Context, actor events.Actor, clusterID, id uuid.UUID, on bool) error {
	c, err := s.q.GetCluster(ctx, clusterID)
	if err != nil {
		return err
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		r, err := s.get(ctx, q, clusterID, id, true)
		if err != nil {
			return err
		}
		if err := mariaOnly(r, "Vanzelf opnieuw starten"); err != nil {
			return err
		}
		if r.AutoRecover == on {
			return nil
		}
		spec, err := parseSpec(r.Spec)
		if err != nil {
			return err
		}
		if err := q.SetClusterComponentAutoRecover(ctx, store.SetClusterComponentAutoRecoverParams{ID: r.ID, AutoRecover: on}); err != nil {
			return err
		}
		return s.ev.Write(ctx, q, events.Event{
			Actor: actor, SubjectType: "cluster", SubjectID: clusterID.String(), ClusterID: &clusterID, Action: "cluster.database_auto_recover",
			Payload: map[string]any{"name": c.Name, "component": title(spec), "on": on},
		})
	})
}
