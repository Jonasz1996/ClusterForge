package heal

// De taak heal.run voert een herstelplan uit dat Jonas bevestigde, of dat
// vanzelf mocht. Ze doet alleen wat in het plan staat, en kijkt bij elke
// stap opnieuw: zit er intussen een node in een Primary component, dan
// begint ze het cluster niet opnieuw maar laat ze de rest daar aansluiten.

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Jonasz1996/clusterforge/internal/databases"
	"github.com/Jonasz1996/clusterforge/internal/deploy"
	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/galera"
	"github.com/Jonasz1996/clusterforge/internal/jobs"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/internal/templates"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// Kind is de taak die een herstelplan uitvoert.
const Kind = "heal.run"

// synced is wsrep_local_state van een node die meedoet.
const synced = 4

// ConflictError: het plan kan nu niet; de tekst zegt waarom.
type ConflictError struct{ Msg string }

func (e *ConflictError) Error() string { return e.Msg }

// ErrStale: het plan is veranderd sinds Jonas het zag.
var ErrStale = errors.New("het plan is veranderd sinds je het bekeek; bekijk het opnieuw")

// runParams gaan mee met de taak: het plan zoals het bevestigd werd.
type runParams struct {
	Key         string     `json:"key"`
	ClusterID   *uuid.UUID `json:"cluster_id,omitempty"`
	ClusterName string     `json:"cluster_name"`
	Engine      string     `json:"engine"`
	What        string     `json:"what"`
	// Galera is wsrep_cluster_name, voor de controle dat een node in het
	// goede cluster meedoet.
	Galera string `json:"galera,omitempty"`
	Plan   Plan   `json:"plan"`
	Auto   bool   `json:"auto,omitempty"`
	Reason string `json:"reason,omitempty"`
	// Bootstrap: het plan kan het Galera-cluster opnieuw beginnen; vanzelf
	// gebeurt dat hoogstens eens per BootstrapEvery.
	Bootstrap bool `json:"bootstrap,omitempty"`
}

// EnableRun laat de runner heal.run uitvoeren.
func (s *Service) EnableRun(runner *jobs.Runner, ev *events.Writer) {
	s.runner, s.ev = runner, ev
	runner.Register(Kind, s.run)
}

// RunPlan voert het plan van een database uit, als het nog het plan met deze
// hash is.
func (s *Service) RunPlan(ctx context.Context, actor events.Actor, key, hash string) (store.Job, error) {
	rep, in, err := s.Report(ctx, key)
	if err != nil {
		return store.Job{}, err
	}
	p := rep.Plan
	switch {
	case p == nil:
		return store.Job{}, &ConflictError{"er is niets te herstellen"}
	case p.Managed != nil:
		return store.Job{}, &ConflictError{"deze database is een onderdeel van ClusterForge; herstel haar met Opnieuw starten of Toepassen bij het onderdeel"}
	case p.Hash != hash:
		return store.Job{}, ErrStale
	case p.Blocked != "":
		return store.Job{}, &ConflictError{"het plan kan nu niet: " + p.Blocked}
	}
	return s.enqueue(ctx, actor, rep, in, runParams{}, "")
}

func (s *Service) enqueue(ctx context.Context, actor events.Actor, rep Report, in Input, params runParams, reason string) (store.Job, error) {
	if s.runner == nil {
		return store.Job{}, errors.New("zelfherstel kan hier geen taken uitvoeren")
	}
	params.Key, params.ClusterID, params.ClusterName, params.Engine = rep.Key, rep.ClusterID, rep.ClusterName, rep.Engine
	params.What, params.Plan, params.Reason = what(rep, in.Group), *rep.Plan, reason
	for _, n := range in.Nodes {
		if n.Inspect != nil && n.Inspect.Galera != nil && n.Inspect.Galera.ClusterName != "" {
			params.Galera = n.Inspect.Galera.ClusterName
		}
	}
	for _, srv := range in.Group.Servers {
		if srv.Galera != nil && srv.Galera.ClusterName != "" {
			params.Galera = srv.Galera.ClusterName
		}
	}
	title := "Zelfherstel: " + params.What
	if params.Auto {
		title = "Zelfherstel (vanzelf): " + params.What
	}
	var j store.Job
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		if err := q.LockHealKey(ctx, rep.Key); err != nil {
			return err
		}
		switch active, err := q.HealJobActive(ctx, rep.Key); {
		case err != nil:
			return err
		case active:
			return &ConflictError{"Zelfherstel is al bezig met " + params.What}
		}
		spec := jobs.Spec{Kind: Kind, Title: title, Params: params, ClusterID: rep.ClusterID, Actor: actor}
		var err error
		if rep.ClusterID != nil {
			j, err = s.runner.EnqueueForClusterTx(ctx, q, spec)
		} else {
			spec.NodeID = &in.Group.Servers[0].NodeID
			j, err = s.runner.EnqueueTx(ctx, q, spec)
		}
		if err != nil {
			return err
		}
		return s.ev.Write(ctx, q, s.event(actor, params, "heal_started", map[string]any{
			"steps": params.Plan.Steps, "nodes": planHosts(params.Plan, func(PlanNode) bool { return true }), "job_id": j.ID, "title": j.Title,
			"problems": problemTitles(rep),
		}))
	})
	var busy jobs.BusyError
	if errors.As(err, &busy) {
		return store.Job{}, &ConflictError{busy.Error()}
	}
	if err == nil {
		s.runner.Kick()
	}
	return j, err
}

// event maakt een event over de database van de taak: cluster.<action> bij
// een cluster, zodat het in de tijdlijn van zijn incident staat, en
// node.<action> bij een losse node.
func (s *Service) event(actor events.Actor, p runParams, action string, payload map[string]any) events.Event {
	payload["what"], payload["engine"], payload["auto"] = p.What, p.Engine, p.Auto
	if p.Reason != "" {
		payload["reason"] = p.Reason
	}
	e := events.Event{Actor: actor, Action: "cluster." + action, Payload: payload}
	if p.ClusterID != nil {
		e.SubjectType, e.SubjectID, e.ClusterID = "cluster", p.ClusterID.String(), p.ClusterID
		payload["name"] = p.ClusterName
	} else if len(p.Plan.Nodes) > 0 {
		e.Action, e.SubjectType, e.SubjectID = "node."+action, "node", p.Plan.Nodes[0].NodeID.String()
		payload["hostname"] = p.Plan.Nodes[0].Hostname
	}
	return e
}

// problemTitles is wat er mis was, elke titel één keer, voor het hoofdstuk
// Herstel van de documentatie. Een node die Zelfherstel niet kon bekijken,
// is geen reden om te herstellen.
func problemTitles(r Report) []string {
	out := []string{}
	for _, p := range r.Problems {
		if p.Code != CodeNoInspect && !slices.Contains(out, p.Title) {
			out = append(out, p.Title)
		}
	}
	return out
}

func what(r Report, g databases.Group) string {
	name := engineName(g)
	if r.ClusterID != nil {
		return name + " van " + r.ClusterName
	}
	if len(g.Servers) > 0 {
		return name + " op " + g.Servers[0].Hostname
	}
	return name
}

func planHosts(p Plan, keep func(PlanNode) bool) []string {
	var out []string
	for _, n := range p.Nodes {
		if keep(n) {
			out = append(out, n.Hostname)
		}
	}
	return out
}

func (s *Service) run(ctx context.Context, j *jobs.Job) (err error) {
	var p runParams
	if err := j.Decode(&p); err != nil {
		return err
	}
	actor := events.System()
	if j.RequestedBy != nil {
		actor = events.User(*j.RequestedBy)
	}
	defer func() {
		action, payload := "heal_done", map[string]any{"job_id": j.ID, "title": j.Title}
		if err != nil {
			action, payload["error"] = "heal_failed", err.Error()
		}
		if werr := s.ev.Write(context.WithoutCancel(ctx), nil, s.event(actor, p, action, payload)); werr != nil && err == nil {
			err = werr
		}
		s.Kick()
		if s.Changed != nil {
			s.Changed()
		}
	}()
	plan := p.Plan
	for _, n := range plan.Nodes {
		if !n.SetAddress {
			continue
		}
		if err := j.Step(ctx, n.Hostname+": Galera-adressen bijwerken", func(ctx context.Context, st *jobs.Step) error {
			return s.setAddress(ctx, st, n)
		}); err != nil {
			return err
		}
	}
	for _, n := range plan.Nodes {
		if !n.Kill {
			continue
		}
		if err := j.Step(ctx, n.Hostname+": hangend proces stoppen", func(ctx context.Context, st *jobs.Step) error {
			return s.kill(ctx, j.ID.String(), st, n)
		}); err != nil {
			return err
		}
	}
	switch plan.Action {
	case ActionRestart:
		for _, n := range plan.Nodes {
			if !n.Start {
				continue
			}
			if err := j.Step(ctx, n.Hostname+": "+n.Unit+" starten", func(ctx context.Context, st *jobs.Step) error {
				return s.startServer(ctx, j.ID.String(), st, n, p.Engine)
			}); err != nil {
				return err
			}
		}
		return nil
	case ActionGalera:
		return s.runGalera(ctx, j, p)
	}
	return fmt.Errorf("onbekende actie %q", plan.Action)
}

// setAddress zet de Galera-adressen op één node.
func (s *Service) setAddress(ctx context.Context, st *jobs.Step, n PlanNode) error {
	st.Logf("wsrep_cluster_address: %s → %s", n.OldClusterAddress, n.ClusterAddress)
	if n.NodeAddress != "" {
		st.Logf("wsrep_node_address: %s → %s", n.OldNodeAddress, n.NodeAddress)
	}
	res, err := s.db(ctx, n.NodeID, protocol.DBRequest{Op: protocol.DBHeal, Heal: &protocol.DBHealRequest{
		Action: protocol.HealSetGaleraAddress, ClusterAddress: n.ClusterAddress, NodeAddress: n.NodeAddress,
	}}, s.Timeout)
	if err != nil {
		return err
	}
	if res.Heal == nil {
		return errors.New("de agent gaf geen antwoord")
	}
	for _, c := range res.Heal.Changed {
		st.Logf("%s gewijzigd; de oude versie staat in %s", c.File, c.Backup)
	}
	return nil
}

// kill stopt een hangend proces: TERM aan de cgroup van de unit, na een
// minuut KILL, en controleert dat de processen weg zijn. Een unit die op
// failed staat, kan systemd soms niet meer signaleren; dan krijgt elk pid
// het signaal zelf, maar alleen als het nog in de cgroup van de unit zit,
// zodat een intussen hergebruikt pid niets overkomt.
func (s *Service) kill(ctx context.Context, id string, st *jobs.Step, n PlanNode) error {
	var alive, list []string
	for _, pid := range n.KillPIDs {
		alive = append(alive, fmt.Sprintf("kill -0 %d 2>/dev/null", pid))
		list = append(list, strconv.Itoa(pid))
	}
	anyAlive := strings.Join(alive, " || ")
	unit := n.Unit + ".service"
	signal := func(sig string) string {
		return fmt.Sprintf("systemctl kill --signal=%s %s 2>/dev/null; for p in %s; do grep -qs '/%s$' /proc/$p/cgroup && kill -%s $p; done",
			sig, unit, strings.Join(list, " "), regexp.QuoteMeta(unit), sig)
	}
	run := fmt.Sprintf("%s; for i in $(seq 60); do { %s; } || exit 0; sleep 1; done; %s; sleep 2; ! { %s; }",
		signal("TERM"), anyAlive, signal("KILL"), anyAlive)
	st.Logf("%s: pid %s stoppen (TERM, na een minuut KILL)", n.Hostname, pids(n.KillPIDs))
	steps := []templates.Step{{Step: protocol.Step{Command: &protocol.CommandStep{Run: run, Unless: "! { " + anyAlive + "; }"}}, Title: "hangend proces stoppen"}}
	_, err := s.apply(ctx, id, st, n, "-kill", steps)
	return err
}

// startServer start een losse server, of een replica of Patroni, en wacht
// tot hij antwoordt.
func (s *Service) startServer(ctx context.Context, id string, st *jobs.Step, n PlanNode, engine string) error {
	if err := s.startNoBlock(ctx, id, st, n); err != nil {
		return err
	}
	return s.wait(ctx, st, n.Hostname+" antwoordt", s.StartTimeout, func() (string, bool) {
		srv, err := s.server(ctx, n.NodeID, engine)
		switch {
		case err != nil:
			return err.Error(), false
		case srv == nil:
			return "de agent ziet de server niet", false
		case !srv.Running:
			return fmt.Sprintf("%s is %s%s", srv.Unit, srv.Active, prefixed(": ", srv.Error)), false
		}
		return srv.Unit + " draait", true
	})
}

// startNoBlock start de unit zonder te wachten: een node die een volledige
// kopie haalt, kan er uren over doen, en systemd blijft dan bezig.
func (s *Service) startNoBlock(ctx context.Context, id string, st *jobs.Step, n PlanNode) error {
	run := "systemctl reset-failed " + n.Unit + " 2>/dev/null; systemctl start --no-block " + n.Unit
	if n.Unit == "mariadb" {
		// Een gewone start begint nooit een cluster, ook niet als een
		// afgebroken galera_new_cluster de vlag liet staan.
		run = galera.ClearNewCluster + "; " + run
	}
	steps := []templates.Step{{Step: protocol.Step{Command: &protocol.CommandStep{
		Run: run, Unless: "systemctl is-active --quiet " + n.Unit,
	}}, Title: n.Unit + " starten"}}
	_, err := s.apply(ctx, id, st, n, "-start", steps)
	return err
}

func prefixed(p, s string) string {
	if s == "" {
		return ""
	}
	return p + s
}

// runGalera begint het cluster opnieuw als niemand Primary is, en laat de
// gestopte nodes aansluiten.
func (s *Service) runGalera(ctx context.Context, j *jobs.Job, p runParams) error {
	id := j.ID.String()
	nodes := p.Plan.Nodes
	name := p.Galera
	var begin *uuid.UUID
	if err := j.Step(ctx, "Posities lezen", func(ctx context.Context, st *jobs.Step) error {
		var state struct {
			Begin *uuid.UUID `json:"begin,omitempty"`
		}
		if st.State(&state) {
			begin = state.Begin
			return nil
		}
		b, err := s.decide(ctx, id, st, p)
		if err != nil {
			return err
		}
		begin = b
		state.Begin = b
		return st.SetState(ctx, state)
	}); err != nil {
		return err
	}
	joined := s.primaryCount(ctx, nodes, name)
	if begin != nil {
		i := slices.IndexFunc(nodes, func(n PlanNode) bool { return n.NodeID == *begin })
		n := nodes[i]
		if err := j.Step(ctx, n.Hostname+": het cluster beginnen", func(ctx context.Context, st *jobs.Step) error {
			return s.begin(ctx, id, st, p, n)
		}); err != nil {
			return err
		}
		joined = 1
	}
	for _, n := range nodes {
		if begin != nil && n.NodeID == *begin {
			continue
		}
		if !n.Start && begin == nil {
			continue
		}
		// Zonder galera.Package sluit n niet aan; starten zou mislukken of
		// tot JoinTimeout wachten. Controleren meldt het.
		if err := j.Step(ctx, n.Hostname+": aansluiten", func(ctx context.Context, st *jobs.Step) error {
			if g, _ := s.galera(ctx, n.NodeID); healthy(g, name, 1) {
				st.Logf("%s doet al mee", n.Hostname)
				return nil
			}
			if n.NoGaleraTools {
				st.Logf("%s overgeslagen: het pakket %s ontbreekt, en zonder sluit hij niet aan (ook niet met IST)", n.Hostname, galera.Package)
				return nil
			}
			if err := s.startNoBlock(ctx, id, st, n); err != nil {
				return err
			}
			want := joined + 1
			return s.waitSynced(ctx, st, n, name, want)
		}); err != nil {
			return err
		}
		// Te laag tellen is veilig: waitSynced wacht op minstens zoveel
		// leden.
		if !n.NoGaleraTools {
			joined++
		}
	}
	return j.Step(ctx, "Controleren", func(ctx context.Context, st *jobs.Step) error {
		var noTools []string
		for _, n := range nodes {
			if g, _ := s.galera(ctx, n.NodeID); n.NoGaleraTools && !healthy(g, name, 1) {
				noTools = append(noTools, n.Hostname)
			}
		}
		uuids := map[string][]string{}
		want := len(nodes) - len(noTools)
		for _, n := range nodes {
			if slices.Contains(noTools, n.Hostname) {
				continue
			}
			g, err := s.galera(ctx, n.NodeID)
			switch {
			case err != nil:
				return fmt.Errorf("%s: %w", n.Hostname, err)
			case !healthy(g, name, want):
				return fmt.Errorf("%s is %s in een %s component van %d leden; verwacht Synced in Primary met %d", n.Hostname, g.State, g.ClusterStatus, g.ClusterSize, want)
			}
			uuids[g.ClusterUUID] = append(uuids[g.ClusterUUID], n.Hostname)
		}
		if len(uuids) > 1 {
			return errors.New("de nodes zitten in verschillende clusters met dezelfde naam")
		}
		st.Logf("%d nodes Synced in één cluster", want)
		if len(noTools) > 0 {
			return fmt.Errorf("%s doet niet mee: daar ontbreekt het pakket %s. Installeer het in de versie van mariadb-server "+
				"(apt install %s=$(dpkg-query -W -f='${Version}' mariadb-server)) en start MariaDB daar, of herstel opnieuw",
				strings.Join(noTools, " en "), galera.Package, galera.Package)
		}
		return nil
	})
}

// primaryCount telt de nodes die nu in een Primary component zitten.
func (s *Service) primaryCount(ctx context.Context, nodes []PlanNode, name string) int {
	n := 0
	for _, pn := range nodes {
		if g, _ := s.galera(ctx, pn.NodeID); g != nil && g.ClusterStatus == "Primary" && (name == "" || g.ClusterName == name) {
			n++
		}
	}
	return n
}

// decide kijkt of er intussen een node Primary is. Zo niet, dan stopt het
// MariaDB op elke node, leest de posities en kiest de node met de nieuwste
// data. Het geeft nil als de nodes kunnen aansluiten bij een Primary.
func (s *Service) decide(ctx context.Context, id string, st *jobs.Step, p runParams) (*uuid.UUID, error) {
	name := p.Galera
	for _, n := range p.Plan.Nodes {
		if g, _ := s.galera(ctx, n.NodeID); g != nil && g.ClusterStatus == "Primary" && (name == "" || g.ClusterName == name) {
			st.Logf("%s zit in een Primary component; de andere nodes sluiten daar aan", n.Hostname)
			return nil, nil
		}
	}
	for _, n := range p.Plan.Nodes {
		pos, err := s.position(ctx, n.NodeID, false)
		if err != nil {
			return nil, fmt.Errorf("%s antwoordt niet (%v); zonder alle nodes begint ClusterForge het cluster niet", n.Hostname, err)
		}
		if pos.Active == "inactive" || pos.Active == "failed" {
			continue
		}
		st.Logf("%s: MariaDB stoppen om de positie te lezen", n.Hostname)
		stop := []templates.Step{{Step: protocol.Step{Service: &protocol.ServiceStep{Name: n.Unit, State: "stopped"}}, Title: "service " + n.Unit}}
		if _, err := s.apply(ctx, id, st, n, "-stop", stop); err != nil {
			return nil, err
		}
	}
	var cands []galera.Candidate
	for i, n := range p.Plan.Nodes {
		pos, err := s.position(ctx, n.NodeID, true)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", n.Hostname, err)
		}
		gp := galera.Position{UUID: pos.UUID, Seqno: pos.Seqno, SafeToBootstrap: pos.SafeToBootstrap}
		if !gp.Known() {
			return nil, fmt.Errorf("de positie van %s is onbekend: %s; ClusterForge begint het cluster niet", n.Hostname, galera.PositionError(cmpOr(pos.Error, "geen seqno")))
		}
		safe := ""
		if gp.SafeToBootstrap {
			safe = ", stopte als laatste"
		}
		st.Logf("%s: seqno %d uit %s%s%s", n.Hostname, gp.Seqno, pos.From, galera.ViaNote(pos.Via), safe)
		cands = append(cands, galera.Candidate{Name: n.Hostname, Index: i, Position: gp})
	}
	best, err := galera.Choose(cands)
	if err != nil {
		return nil, err
	}
	if p.Plan.Nodes[best.Index].NoGaleraTools {
		// Zonder galera_new_cluster begint best niet. Een node met het
		// pakket en dezelfde seqno heeft dezelfde data (Choose eist
		// dezelfde uuid) en mag dan in zijn plaats.
		var with []galera.Candidate
		for _, c := range cands {
			if !p.Plan.Nodes[c.Index].NoGaleraTools {
				with = append(with, c)
			}
		}
		alt, err := galera.Choose(with)
		if err != nil || alt.Position.Seqno != best.Position.Seqno {
			return nil, fmt.Errorf("%s heeft de nieuwste data (seqno %d), maar daar ontbreekt het pakket %s met galera_new_cluster; ClusterForge begint het cluster niet. "+
				"Installeer het in de versie van mariadb-server (apt install %s=$(dpkg-query -W -f='${Version}' mariadb-server)) en herstel opnieuw",
				best.Name, best.Position.Seqno, galera.Package, galera.Package)
		}
		st.Logf("%s heeft dezelfde data als %s (seqno %d) en begint het cluster, want op %s ontbreekt %s", alt.Name, best.Name, alt.Position.Seqno, best.Name, galera.Package)
		return &p.Plan.Nodes[alt.Index].NodeID, nil
	}
	st.Logf("%s heeft de nieuwste data (seqno %d) en begint het cluster", best.Name, best.Position.Seqno)
	return &p.Plan.Nodes[best.Index].NodeID, nil
}

// begin begint het cluster op n en wacht tot hij Synced is. Is er
// intussen toch een Primary, dan sluit n daar aan: nooit twee clusters.
func (s *Service) begin(ctx context.Context, id string, st *jobs.Step, p runParams, n PlanNode) error {
	name := p.Galera
	if g, _ := s.galera(ctx, n.NodeID); healthy(g, name, 1) {
		st.Logf("%s zit al in een Primary component", n.Hostname)
		return nil
	}
	for _, o := range p.Plan.Nodes {
		if o.NodeID == n.NodeID {
			continue
		}
		pos, err := s.position(ctx, o.NodeID, false)
		if err != nil {
			return fmt.Errorf("%s zegt niet of hij al meedoet (%w); ClusterForge begint het cluster pas als dat zeker niet zo is", o.Hostname, err)
		}
		if pos.Running && pos.Galera != nil && pos.Galera.ClusterStatus == "Primary" {
			st.Logf("%s zit intussen in een Primary component; %s sluit daar aan", o.Hostname, n.Hostname)
			if err := s.startNoBlock(ctx, id, st, n); err != nil {
				return err
			}
			return s.waitSynced(ctx, st, n, name, 2)
		}
	}
	if n.Unit != "mariadb" {
		return fmt.Errorf("%s draait %s; een Galera-cluster beginnen kan ClusterForge alleen met MariaDB (mariadb.service)", n.Hostname, n.Unit)
	}
	steps := []templates.Step{{Step: protocol.Step{Command: &protocol.CommandStep{Run: galera.Bootstrap, Unless: protocol.DBCheckCommand}},
		Title: "het Galera-cluster beginnen"}}
	if _, err := s.apply(ctx, id, st, n, "-begin", steps); err != nil {
		return err
	}
	return s.waitSynced(ctx, st, n, name, 1)
}

func (s *Service) waitSynced(ctx context.Context, st *jobs.Step, n PlanNode, name string, want int) error {
	return s.wait(ctx, st, fmt.Sprintf("%s Synced in een cluster van %d", n.Hostname, want), s.JoinTimeout, func() (string, bool) {
		g, err := s.galera(ctx, n.NodeID)
		switch {
		case err != nil:
			return fmt.Sprintf("%s: %v", n.Hostname, err), false
		case g == nil:
			return n.Hostname + ": MariaDB draait zonder Galera", false
		case name != "" && g.ClusterName != name:
			return fmt.Sprintf("%s doet mee in cluster %s in plaats van %s", n.Hostname, g.ClusterName, name), false
		}
		return fmt.Sprintf("%s: %s, %s, %d leden", n.Hostname, g.State, g.ClusterStatus, g.ClusterSize), healthy(g, name, want)
	})
}

func healthy(g *protocol.DBGalera, name string, want int) bool {
	return g != nil && (name == "" || g.ClusterName == name) && g.ClusterStatus == "Primary" && g.StateCode == synced && g.ClusterSize >= want
}

// wait vraagt om de Poll tot check klaar zegt, en schrijft elke nieuwe
// toestand in het logboek.
func (s *Service) wait(ctx context.Context, st *jobs.Step, what string, timeout time.Duration, check func() (string, bool)) error {
	deadline := s.Now().Add(timeout)
	last := ""
	for {
		state, ok := check()
		if ctx.Err() != nil {
			return context.Cause(ctx)
		}
		if state != last {
			st.Logf("%s", state)
			_ = st.Flush(ctx)
			last = state
		}
		if ok {
			return nil
		}
		if s.Now().After(deadline) {
			return fmt.Errorf("niet gelukt binnen %s: %s (laatst: %s)", timeout, what, state)
		}
		select {
		case <-ctx.Done():
			return context.Cause(ctx)
		case <-time.After(s.Poll):
		}
	}
}

// galera is de toestand van Galera op een node; nil met een fout als
// MariaDB niet antwoordt.
func (s *Service) galera(ctx context.Context, id uuid.UUID) (*protocol.DBGalera, error) {
	pos, err := s.position(ctx, id, false)
	if err != nil {
		return nil, err
	}
	if !pos.Running {
		return nil, fmt.Errorf("MariaDB draait niet (%s)", pos.Active)
	}
	return pos.Galera, nil
}

func (s *Service) position(ctx context.Context, id uuid.UUID, recover bool) (*protocol.DBPositionResult, error) {
	timeout := s.Timeout
	if recover {
		timeout = protocol.DBRecoverTimeout + 30*time.Second
	}
	res, err := s.db(ctx, id, protocol.DBRequest{Op: protocol.DBPosition, Position: &protocol.DBPositionRequest{Recover: recover}}, timeout)
	if err != nil {
		return nil, err
	}
	if res.Position == nil {
		return nil, errors.New("de agent gaf geen positie")
	}
	return res.Position, nil
}

// server is de status van een server op een node.
func (s *Service) server(ctx context.Context, id uuid.UUID, engine string) (*protocol.DBServer, error) {
	res, err := s.db(ctx, id, protocol.DBRequest{Op: protocol.DBStatus, Quick: true}, s.Timeout)
	if err != nil {
		return nil, err
	}
	for i := range res.Servers {
		if res.Servers[i].Engine == engine {
			return &res.Servers[i], nil
		}
	}
	return nil, nil
}

func (s *Service) db(ctx context.Context, id uuid.UUID, req protocol.DBRequest, timeout time.Duration) (protocol.DBResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req.ID, req.Deadline = uuid.NewString(), time.Now().Add(timeout)
	res, err := s.bus.DB(ctx, id, req)
	switch {
	case err != nil:
		return res, err
	case strings.HasPrefix(res.Error, "onbekend databaseverzoek"):
		return res, errors.New("de agent kent dit verzoek nog niet; werk hem bij met het installatiescript en --upgrade")
	case res.Error != "":
		return res, errors.New(res.Error)
	}
	return res, nil
}

func (s *Service) apply(ctx context.Context, id string, st *jobs.Step, n PlanNode, suffix string, steps []templates.Step) (int, error) {
	o := deploy.ApplyOptions{
		NodeID: n.NodeID, Hostname: n.Hostname, CommandID: fmt.Sprintf("%s-%s%s", id, n.NodeID, suffix), Retry: s.Retry,
		Logf: st.Logf, Flush: func() { _ = st.Flush(ctx) },
	}
	return deploy.ApplySteps(ctx, s.bus, o, steps)
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
