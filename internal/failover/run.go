package failover

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/health"
	"github.com/Jonasz1996/clusterforge/internal/jobs"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// De vijf vaste stappen van failover.test. Stappen worden aan hun volgorde
// herkend, dus de taak doorloopt ze altijd in deze volgorde.
const (
	stepPrecheck = "Voorcontrole"
	stepMeasure  = "Storing en meten"
	stepRestore  = "Herstellen"
	stepReturn   = "Terugkeer controleren"
	stepReport   = "Rapport"
)

// TimelineEvent is één moment in het rapport, in ms na de storing.
type TimelineEvent struct {
	TMS  int64  `json:"t_ms"`
	Kind string `json:"kind"`
	Text string `json:"text"`
}

// Segment is een stuk van de probe met één uitkomst.
type Segment struct {
	FromMS int64 `json:"from_ms"`
	ToMS   int64 `json:"to_ms"`
	OK     bool  `json:"ok"`
}

// Measurement is wat de meting na de storing zag.
type Measurement struct {
	// DowntimeMS loopt van de eerste mislukte probe tot de eerste van drie
	// goede op rij: wat een client merkt.
	DowntimeMS int64 `json:"downtime_ms"`
	// Recovered: de probe slaagde aan het eind drie keer op rij.
	Recovered bool `json:"recovered"`
	// TakenOver: een verse heartbeat toont elk VIP van het doel op een
	// andere node.
	TakenOver      bool       `json:"taken_over"`
	TakeoverNodeID *uuid.UUID `json:"takeover_node_id,omitempty"`
	TakeoverNode   string     `json:"takeover_node,omitempty"`
	TakeoverMS     int64      `json:"takeover_ms,omitempty"`
	WindowMS       int64      `json:"window_ms"`
	EndMS          int64      `json:"end_ms"`
	// Canceled: iemand brak de test af tijdens de meting.
	Canceled bool            `json:"canceled,omitempty"`
	Probe    []Segment       `json:"probe"`
	Timeline []TimelineEvent `json:"timeline"`
}

// Measurements staan in test_runs.measurements.
type Measurements struct {
	DowntimeMS     *int64     `json:"downtime_ms,omitempty"`
	ExpectMS       int64      `json:"expect_ms"`
	WindowMS       int64      `json:"window_ms,omitempty"`
	TakeoverNodeID *uuid.UUID `json:"takeover_node_id,omitempty"`
	TakeoverNode   string     `json:"takeover_node,omitempty"`
	TakeoverMS     *int64     `json:"takeover_ms,omitempty"`
	FailbackMS     *int64     `json:"failback_ms,omitempty"`
	ReturnedTo     string     `json:"returned_to,omitempty"`
	EndMS          int64      `json:"end_ms"`
	Probe          []Segment  `json:"probe"`
}

// measureState is de state van de stap Storing en meten. InjectedAt staat
// vast voor het commando de deur uitgaat; een hervatte stap stuurt de
// storing dus nooit opnieuw.
type measureState struct {
	InjectedAt  time.Time    `json:"injected_at"`
	InjectError string       `json:"inject_error,omitempty"`
	Interrupted bool         `json:"interrupted,omitempty"`
	Measurement *Measurement `json:"measurement,omitempty"`
}

type restoreState struct {
	ClearedAt time.Time `json:"cleared_at"`
}

type returnState struct {
	Owner      string          `json:"owner"`
	FailbackMS int64           `json:"failback_ms"`
	EndMS      int64           `json:"end_ms"`
	Probe      []Segment       `json:"probe"`
	Timeline   []TimelineEvent `json:"timeline"`
}

// testRun is één uitvoering van failover.test.
type testRun struct {
	s       *Service
	j       *jobs.Job
	run     store.TestRun
	def     Definition
	cluster uuid.UUID
	plan    Plan

	injectedAt time.Time
	clearedAt  time.Time
	// ran zegt welke stappen in deze poging liepen; van de andere komt de
	// state uit een eerdere poging.
	ran map[string]bool
}

func (s *Service) loadRun(ctx context.Context, j *jobs.Job) (store.TestRun, Definition, error) {
	var p runParams
	if err := j.Decode(&p); err != nil {
		return store.TestRun{}, Definition{}, err
	}
	row, err := s.q.GetTestRun(ctx, p.RunID)
	if err != nil {
		return store.TestRun{}, Definition{}, fmt.Errorf("run %s: %w", p.RunID, err)
	}
	var def Definition
	if err := json.Unmarshal(row.TestRun.Definition, &def); err != nil {
		return store.TestRun{}, Definition{}, err
	}
	if row.TestRun.ClusterID == nil {
		return store.TestRun{}, Definition{}, errors.New("de run hoort bij geen cluster meer")
	}
	return row.TestRun, def, nil
}

// runTest voert een failovertest uit. Na de storing volgt altijd herstel:
// ook bij annuleren (de herstelstappen lopen los van de annulering), bij
// een fout, bij een nette stop van de server (noodherstel in een defer) en
// na een crash (de hervatte taak herstelt).
func (s *Service) runTest(ctx context.Context, j *jobs.Job) error {
	run, def, err := s.loadRun(ctx, j)
	if err != nil {
		return err
	}
	r := &testRun{s: s, j: j, run: run, def: def, cluster: *run.ClusterID, ran: map[string]bool{}}
	defer s.changed()
	return r.execute(ctx)
}

func (r *testRun) step(ctx context.Context, name string, state any, fn func(ctx context.Context, st *jobs.Step) error) error {
	err := r.j.Step(ctx, name, func(ctx context.Context, st *jobs.Step) error {
		r.ran[name] = true
		return fn(ctx, st)
	})
	if !r.ran[name] {
		r.j.Done(name, state)
	}
	return err
}

func (r *testRun) execute(ctx context.Context) error {
	if err := r.step(ctx, stepPrecheck, &r.plan, r.precheck); err != nil {
		if jobs.Interrupted(ctx) {
			return err
		}
		return r.finish(ctx, outcome{precheckErr: err, canceled: jobs.Canceled(ctx)})
	}
	if r.plan.Target.ID == uuid.Nil && r.run.NodeID != nil {
		r.plan.Target.ID, r.plan.Target.Hostname = *r.run.NodeID, r.run.Hostname
	}
	if r.plan.Skipped != "" {
		return r.finish(ctx, outcome{skipped: r.plan.Skipped})
	}

	var ms measureState
	measureErr := r.step(ctx, stepMeasure, &ms, func(ctx context.Context, st *jobs.Step) error {
		return r.measureStep(ctx, st, &ms)
	})
	o := outcome{
		injected: !ms.InjectedAt.IsZero(), interrupted: ms.Interrupted, injectErr: ms.InjectError, m: ms.Measurement,
		canceled: jobs.Canceled(ctx),
	}
	if !o.injected {
		if jobs.Interrupted(ctx) {
			return measureErr
		}
		o.precheckErr = measureErr
		return r.finish(ctx, o)
	}
	r.injectedAt = ms.InjectedAt
	cleared := false
	// Laatste laag bij een fout, een panic of een nette stop van de server:
	// de dienst starten, met een eigen limiet en los van de taak. NATS blijft
	// open tot de server de runner gestopt heeft.
	defer func() {
		if !cleared {
			r.emergency()
		}
	}()
	if jobs.Interrupted(ctx) {
		return measureErr
	}

	// Vanaf hier lopen de stappen door als iemand annuleert; alleen een stop
	// van de server breekt ze af, en dan hervat de taak.
	dctx, cancel := jobs.Detach(ctx)
	defer cancel()
	var rs restoreState
	o.restoreErr = r.step(dctx, stepRestore, &rs, r.restore)
	if jobs.Interrupted(dctx) {
		return o.restoreErr
	}
	if o.restoreErr == nil {
		cleared = true
		if r.clearedAt.IsZero() {
			r.clearedAt = rs.ClearedAt
		}
		var ret returnState
		o.restoreErr = r.step(dctx, stepReturn, &ret, func(ctx context.Context, st *jobs.Step) error {
			return r.returnStep(ctx, st, &ret)
		})
		if jobs.Interrupted(dctx) {
			return o.restoreErr
		}
		if o.restoreErr == nil {
			o.ret = &ret
		}
	}
	return r.finish(ctx, o)
}

func (r *testRun) precheck(ctx context.Context, st *jobs.Step) error {
	c, err := r.s.q.GetCluster(ctx, r.cluster)
	if err != nil {
		return err
	}
	plan, err := r.s.precheck(ctx, c, r.def, r.j.ID)
	if ctx.Err() != nil {
		return context.Cause(ctx)
	}
	var pe *PrecheckError
	switch {
	case errors.As(err, &pe):
		var failed []string
		for _, ch := range pe.Checks {
			if !ch.OK {
				failed = append(failed, ch.Detail)
			}
		}
		plan.Skipped = strings.Join(failed, "; ")
	case err != nil:
		return err
	case r.run.NodeID != nil && plan.Target.ID != *r.run.NodeID:
		plan.Skipped = fmt.Sprintf("%s staat sinds de start op %s in plaats van op %s", r.def.VIP, plan.Target.Hostname, r.run.Hostname)
	}
	for _, ch := range plan.Checks {
		mark := "✓"
		if !ch.OK {
			mark = "✗"
		}
		st.Logf("%s %s: %s", mark, ch.Name, ch.Detail)
	}
	if plan.Skipped != "" {
		st.Logf("de test start niet; er verandert niets")
	}
	if checks, err := json.Marshal(plan.Checks); err == nil {
		_ = r.s.q.SetTestRunChecks(ctx, store.SetTestRunChecksParams{ID: r.run.ID, Checks: checks})
	}
	r.plan = plan
	return st.SetState(ctx, plan)
}

func (r *testRun) measureStep(ctx context.Context, st *jobs.Step, ms *measureState) error {
	if st.State(ms) && !ms.InjectedAt.IsZero() {
		if ms.Measurement != nil || ms.Interrupted || ms.InjectError != "" {
			return nil
		}
		// De server stopte na de storing: wat we zagen, is onvolledig.
		ms.Interrupted = true
		st.Logf("ClusterForge stopte tijdens de meting; die telt niet. Nu volgt het herstel.")
		return st.SetState(ctx, ms)
	}
	if ctx.Err() != nil {
		return context.Cause(ctx)
	}
	host := r.plan.Target.Hostname
	ms.InjectedAt = time.Now()
	if err := st.SetState(ctx, ms); err != nil {
		ms.InjectedAt = time.Time{}
		return err
	}
	st.Logf("%s stoppen op %s", r.def.Unit, host)
	_ = st.Flush(ctx)
	err := r.s.apply(ctx, st, r.plan.Target.ID, r.run.ID.String()+"-inject", r.reason(), r.def.Unit, "stopped", 1)
	switch {
	case err == nil:
		r.event(ctx, "failover.fault_injected", nil)
	case jobs.Interrupted(ctx):
		// De server stopt terwijl het commando onderweg is, dus de storing
		// is er misschien al. De hervatte stap telt de meting niet en het
		// herstel volgt.
		return context.Cause(ctx)
	case jobs.Canceled(ctx):
		// Afgebroken terwijl het commando onderweg was: ook dan is de
		// storing er misschien al. De meting telt als afgebroken en het
		// herstel volgt.
	default:
		ms.InjectError = err.Error()
		_ = st.SetState(ctx, ms)
		return err
	}
	m := r.measure(ctx, st, ms.InjectedAt)
	if jobs.Interrupted(ctx) {
		return context.Cause(ctx)
	}
	ms.Measurement = &m
	return st.SetState(ctx, ms)
}

func (r *testRun) reason() string { return "failovertest " + r.def.Name }

// measure vraagt het VIP op tot de probe drie keer na elkaar slaagt en een
// verse heartbeat de overname bevestigt, of tot het meetvenster om is.
func (r *testRun) measure(ctx context.Context, st *jobs.Step, injectedAt time.Time) Measurement {
	s, def := r.s, r.def
	window := min(2*def.expect()+10*time.Second, s.MaxWindow)
	deadline := injectedAt.Add(window)
	m := Measurement{WindowMS: window.Milliseconds()}
	pl := newProbeLog(injectedAt, def.VIP)
	st.Logf("meten: %s elke %d ms opvragen (%s), hoogstens %s", def.VIP, s.ProbeInterval.Milliseconds(), def.Probe, seconds(window))
	_ = st.Flush(ctx)
	vips := r.plan.TargetVIPs
	if len(vips) == 0 {
		vips = []string{def.VIP}
	}
	var takeover []TimelineEvent
	tick := time.NewTicker(s.ProbeInterval)
	defer tick.Stop()
	var lastLook time.Time
	end := time.Now()
	for time.Now().Before(deadline) {
		at := time.Now()
		pctx, cancel := context.WithDeadline(ctx, deadline)
		err := s.Prober.Probe(pctx, def.VIP, def.Probe)
		cancel()
		if ctx.Err() != nil {
			break
		}
		pl.add(at, err)
		end = time.Now()
		if !m.TakenOver && time.Since(lastLook) >= s.Gate.Poll {
			lastLook = time.Now()
			if h, ok := r.takenOver(ctx, vips, injectedAt); ok {
				m.TakenOver, m.TakeoverNodeID, m.TakeoverNode = true, &h.ID, h.Hostname
				m.TakeoverMS = lastLook.Sub(injectedAt).Milliseconds()
				text := strings.Join(vips, ", ") + " staat nu op " + h.Hostname
				takeover = append(takeover, TimelineEvent{TMS: m.TakeoverMS, Kind: "takeover", Text: text})
				st.Logf("%s (heartbeat na %s)", text, seconds(lastLook.Sub(injectedAt)))
				_ = st.Flush(ctx)
			}
		}
		if m.TakenOver && pl.recovered() {
			break
		}
		select {
		case <-ctx.Done():
		case <-tick.C:
		}
	}
	if ctx.Err() != nil {
		m.Canceled = jobs.Canceled(ctx)
		end = time.Now()
	}
	m.EndMS = end.Sub(injectedAt).Milliseconds()
	m.Recovered = pl.recovered()
	m.DowntimeMS = pl.downtime(end).Milliseconds()
	var probeEvents []TimelineEvent
	m.Probe, probeEvents = pl.finish(end)
	m.Timeline = sortTimeline(append(probeEvents, takeover...))
	switch {
	case m.Canceled:
		st.Logf("afgebroken; nu volgt het herstel")
	case !m.TakenOver:
		st.Logf("geen andere node nam het VIP over binnen %s", seconds(window))
	case !m.Recovered:
		st.Logf("%s bleef onbereikbaar tot het einde van de meting", def.VIP)
	default:
		st.Logf("onderbreking: %s (verwacht hoogstens %d s)", seconds(time.Duration(m.DowntimeMS)*time.Millisecond), def.MaxTakeoverSeconds)
	}
	return m
}

// takenOver kijkt of een verse heartbeat elk VIP van het doel bij één
// andere node toont, en geeft de houder van het VIP van de test.
func (r *testRun) takenOver(ctx context.Context, vips []string, since time.Time) (health.Holder, bool) {
	snap, err := r.s.Gate.Look(ctx, r.cluster, vips, since)
	if err != nil {
		return health.Holder{}, false
	}
	for _, v := range vips {
		if o, ok := snap.Owner(v); !ok || o.ID == r.plan.Target.ID {
			return health.Holder{}, false
		}
	}
	if o, ok := snap.Owner(r.def.VIP); ok {
		return o, true
	}
	o, _ := snap.Owner(vips[0])
	return o, true
}

// restore start de gestopte dienst weer.
func (r *testRun) restore(ctx context.Context, st *jobs.Step) error {
	host := r.plan.Target.Hostname
	st.Logf("%s weer starten op %s", r.def.Unit, host)
	_ = st.Flush(ctx)
	if err := r.s.apply(ctx, st, r.plan.Target.ID, r.run.ID.String()+"-clear", r.reason(), r.def.Unit, "started", 3); err != nil {
		return err
	}
	r.clearedAt = time.Now()
	r.event(ctx, "failover.fault_cleared", nil)
	return st.SetState(ctx, restoreState{ClearedAt: r.clearedAt})
}

var errReturnTimeout = errors.New("de terugkeer duurde te lang")

// returnStep wacht met verse heartbeats tot de dienst weer draait en elk VIP
// één houder heeft, met expect_failback bij de oorspronkelijke node. De
// probe loopt mee en meet de korte onderbreking bij die terugkeer.
func (r *testRun) returnStep(ctx context.Context, st *jobs.Step, ret *returnState) error {
	s, def, target := r.s, r.def, r.plan.Target
	ctx, cancel := context.WithTimeoutCause(ctx, s.ReturnTimeout, errReturnTimeout)
	defer cancel()
	since := r.clearedAt
	pl := newProbeLog(r.injectedAt, def.VIP)
	pctx, pstop := context.WithCancel(ctx)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		s.probeLoop(pctx, def, pl)
	}()
	stop := sync.OnceFunc(func() { pstop(); wg.Wait() })
	defer stop()

	last := ""
	progress := func(msg string) {
		last = msg
		st.Logf("%s", msg)
		_ = st.Flush(ctx)
	}
	failed := func(err error) error {
		if errors.Is(context.Cause(ctx), errReturnTimeout) {
			msg := "de terugkeer lukte niet binnen " + seconds(s.ReturnTimeout)
			if last != "" {
				msg += ": " + last
			}
			return errors.New(msg)
		}
		return err
	}

	ms := func(t time.Time) int64 { return t.Sub(r.injectedAt).Milliseconds() }
	var timeline []TimelineEvent
	st.Logf("wachten op twee verse heartbeats van %s met %s actief", target.Hostname, def.Unit)
	_ = st.Flush(ctx)
	if err := s.Gate.NodeReady(ctx, target.ID, since, []string{def.Unit}, progress); err != nil {
		return failed(err)
	}
	timeline = append(timeline, TimelineEvent{TMS: ms(time.Now()), Kind: "ready", Text: def.Unit + " draait weer op " + target.Hostname})
	st.Logf("%s draait weer op %s", def.Unit, target.Hostname)

	want := map[string]uuid.UUID{}
	if def.ExpectFailback {
		for _, v := range r.plan.TargetVIPs {
			want[v] = target.ID
		}
		want[def.VIP] = target.ID
	}
	vips := r.plan.VIPs
	if len(vips) == 0 {
		vips = []string{def.VIP}
	}
	snap, err := s.Gate.Settled(ctx, r.cluster, vips, since, want, progress)
	if err != nil {
		return failed(err)
	}
	if o, ok := snap.Owner(def.VIP); ok {
		ret.Owner = o.Hostname
	}
	text := def.VIP + " staat op " + ret.Owner
	if def.ExpectFailback {
		text = def.VIP + " is terug op " + ret.Owner
	}
	timeline = append(timeline, TimelineEvent{TMS: ms(time.Now()), Kind: "return", Text: text})
	st.Logf("%s", text)
	_ = st.Flush(ctx)

	// Het VIP moet daarna ook echt antwoorden.
	if err := waitFor(ctx, s.ProbeInterval, pl.recovered); err != nil {
		if errors.Is(context.Cause(ctx), errReturnTimeout) {
			return fmt.Errorf("na de terugkeer antwoordt %s niet op %s", def.VIP, def.Probe)
		}
		return err
	}
	stop()
	end := time.Now()
	ret.FailbackMS = pl.downtime(end).Milliseconds()
	ret.EndMS = ms(end)
	var probeEvents []TimelineEvent
	ret.Probe, probeEvents = pl.finish(end)
	for i := range probeEvents {
		if probeEvents[i].Kind == "down" {
			probeEvents[i].Text += " bij de terugkeer"
		}
	}
	ret.Timeline = sortTimeline(append(timeline, probeEvents...))
	if ret.FailbackMS > 0 {
		st.Logf("onderbreking bij de terugkeer: %s", seconds(time.Duration(ret.FailbackMS)*time.Millisecond))
	}
	return st.SetState(ctx, *ret)
}

// waitFor wacht tot ok true geeft of ctx afloopt.
func waitFor(ctx context.Context, every time.Duration, ok func() bool) error {
	t := time.NewTicker(every)
	defer t.Stop()
	for !ok() {
		select {
		case <-ctx.Done():
			return context.Cause(ctx)
		case <-t.C:
		}
	}
	return nil
}

func (s *Service) probeLoop(ctx context.Context, def Definition, pl *probeLog) {
	t := time.NewTicker(s.ProbeInterval)
	defer t.Stop()
	for {
		at := time.Now()
		err := s.Prober.Probe(ctx, def.VIP, def.Probe)
		if ctx.Err() != nil {
			return
		}
		pl.add(at, err)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// emergency start de dienst als de herstelstap niet lukte of niet liep,
// los van de taak en met een eigen limiet.
func (r *testRun) emergency() {
	ctx, cancel := context.WithTimeout(events.WithJob(context.Background(), r.j.ID), r.s.EmergencyTimeout)
	defer cancel()
	res, err := r.s.bus.Command(ctx, r.plan.Target.ID, serviceCommand(r.run.ID.String()+"-clear", r.reason()+" (noodherstel)", r.def.Unit, "started"))
	switch {
	case err != nil:
		r.s.log.Error("noodherstel van failovertest mislukt", "run", r.run.ID, "node", r.plan.Target.Hostname, "err", err)
	case !res.OK:
		r.s.log.Error("noodherstel van failovertest mislukt", "run", r.run.ID, "node", r.plan.Target.Hostname, "err", res.Error)
	default:
		r.s.log.Info("noodherstel van failovertest", "run", r.run.ID, "node", r.plan.Target.Hostname, "unit", r.def.Unit)
	}
}

func serviceCommand(id, reason, unit, state string) protocol.Command {
	return protocol.Command{
		ID: id, Action: protocol.CmdApply, Reason: reason, Deadline: time.Now().Add(time.Minute),
		Steps: []protocol.Step{{Service: &protocol.ServiceStep{Name: unit, State: state}}},
	}
}

// apply stuurt één service-stap. Een agent die even weg is, krijgt nog een
// kans als attempts dat toelaat; de stap is idempotent.
func (s *Service) apply(ctx context.Context, st *jobs.Step, nodeID uuid.UUID, id, reason, unit, state string, attempts int) error {
	var res protocol.Result
	var err error
	for attempt := range attempts {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return context.Cause(ctx)
			case <-time.After(s.Retry):
			}
		}
		cctx, cancel := context.WithTimeout(ctx, time.Minute)
		res, err = s.bus.Command(cctx, nodeID, serviceCommand(id, reason, unit, state))
		cancel()
		if err == nil || ctx.Err() != nil {
			break
		}
		st.Logf("agent niet bereikt: %v", err)
		_ = st.Flush(ctx)
	}
	for _, sr := range res.Steps {
		for _, line := range sr.Output {
			st.Logf("%s", line)
		}
	}
	_ = st.Flush(ctx)
	switch {
	case ctx.Err() != nil:
		return context.Cause(ctx)
	case err != nil:
		return err
	case !res.OK:
		return fmt.Errorf("de agent meldt: %s", res.Error)
	}
	return nil
}

func (r *testRun) event(ctx context.Context, action string, extra map[string]any) {
	payload := map[string]any{
		"run_id": r.run.ID, "test_id": r.def.TestID, "name": r.def.Name, "node_id": r.plan.Target.ID,
		"hostname": r.plan.Target.Hostname, "unit": r.def.Unit, "vip": r.def.VIP,
	}
	maps.Copy(payload, extra)
	if err := r.s.ev.Write(ctx, nil, events.Event{
		Actor: events.System(), SubjectType: "test_run", SubjectID: r.run.ID.String(), ClusterID: &r.cluster,
		Action: action, Payload: payload,
	}); err != nil {
		r.s.log.Warn("event van failovertest", "action", action, "err", err)
	}
}

// outcome is alles waaruit het rapport volgt.
type outcome struct {
	skipped     string
	precheckErr error
	injected    bool
	injectErr   string
	interrupted bool
	canceled    bool
	m           *Measurement
	restoreErr  error
	ret         *returnState
}

// verdict geeft het resultaat, de samenvatting en of alles hersteld is.
func verdict(def Definition, plan Plan, o outcome) (result, summary string, restored *bool) {
	yes, no := true, false
	switch {
	case o.skipped != "":
		return "skipped", "Overgeslagen: " + o.skipped, nil
	case !o.injected && o.canceled:
		return "canceled", "Afgebroken voor de storing; er is niets veranderd", nil
	case !o.injected:
		msg := "ERROR: de test kon niet starten"
		if o.precheckErr != nil {
			msg += ": " + o.precheckErr.Error()
		}
		return "error", msg + "; er is niets veranderd", nil
	case o.restoreErr != nil:
		return "error", fmt.Sprintf("ERROR: herstel mislukt: %v. %s staat mogelijk nog uit op %s; kies Opnieuw herstellen",
			o.restoreErr, def.Unit, plan.Target.Hostname), &no
	}
	back := "alles hersteld"
	if o.ret != nil && o.ret.Owner != "" {
		if def.ExpectFailback {
			back = "daarna terug op " + o.ret.Owner + ", alles hersteld"
		} else {
			back = "daarna op " + o.ret.Owner + ", alles hersteld"
		}
	}
	switch {
	case o.interrupted:
		return "error", "ERROR: meting onderbroken door een herstart van ClusterForge; " + back, &yes
	case o.injectErr != "" && o.canceled:
		return "canceled", "Afgebroken tijdens de storing; " + back, &yes
	case o.injectErr != "":
		return "error", "ERROR: storing niet gelukt: " + o.injectErr + "; " + back, &yes
	case o.m == nil:
		return "error", "ERROR: er is geen meting; " + back, &yes
	case o.m.Canceled:
		return "canceled", "Afgebroken tijdens de meting; " + back, &yes
	}
	m := o.m
	window := time.Duration(m.WindowMS) * time.Millisecond
	down := time.Duration(m.DowntimeMS) * time.Millisecond
	switch {
	case !m.TakenOver:
		return "fail", fmt.Sprintf("FAIL: geen andere node nam %s over binnen %s; %s", def.VIP, seconds(window), back), &yes
	case !m.Recovered:
		return "fail", fmt.Sprintf("FAIL: %s bleef onbereikbaar tot het einde van de meting (%s); %s", def.VIP, seconds(window), back), &yes
	case down > def.expect():
		return "fail", fmt.Sprintf("FAIL: %s onbereikbaar, meer dan de verwachte %d s; overgenomen door %s, %s",
			seconds(down), def.MaxTakeoverSeconds, m.TakeoverNode, back), &yes
	}
	what := seconds(down) + " onbereikbaar"
	if m.DowntimeMS == 0 {
		what = "geen onderbreking gemeten"
	}
	return "pass", fmt.Sprintf("PASS: %s, overgenomen door %s, %s", what, m.TakeoverNode, back), &yes
}

// report bouwt de tijdlijn en de metingen.
func (r *testRun) report(o outcome) ([]TimelineEvent, Measurements) {
	meas := Measurements{ExpectMS: r.def.expect().Milliseconds(), Probe: []Segment{}}
	timeline := []TimelineEvent{}
	if o.injected && o.injectErr == "" {
		timeline = append(timeline, TimelineEvent{TMS: 0, Kind: "fault", Text: r.def.Unit + " gestopt op " + r.plan.Target.Hostname})
	}
	if m := o.m; m != nil {
		timeline = append(timeline, m.Timeline...)
		meas.WindowMS, meas.EndMS = m.WindowMS, m.EndMS
		meas.Probe = append(meas.Probe, m.Probe...)
		if !m.Canceled {
			meas.DowntimeMS = &m.DowntimeMS
		}
		if m.TakenOver {
			meas.TakeoverNodeID, meas.TakeoverNode, meas.TakeoverMS = m.TakeoverNodeID, m.TakeoverNode, &m.TakeoverMS
		}
	}
	if !r.clearedAt.IsZero() {
		at := r.clearedAt.Sub(r.injectedAt).Milliseconds()
		timeline = append(timeline, TimelineEvent{TMS: at, Kind: "clear", Text: r.def.Unit + " weer gestart op " + r.plan.Target.Hostname})
		meas.EndMS = max(meas.EndMS, at)
	}
	if ret := o.ret; ret != nil {
		timeline = append(timeline, ret.Timeline...)
		meas.Probe = append(meas.Probe, ret.Probe...)
		meas.FailbackMS, meas.ReturnedTo = &ret.FailbackMS, ret.Owner
		meas.EndMS = max(meas.EndMS, ret.EndMS)
	}
	return sortTimeline(timeline), meas
}

func sortTimeline(t []TimelineEvent) []TimelineEvent {
	if t == nil {
		return []TimelineEvent{}
	}
	slices.SortStableFunc(t, func(a, b TimelineEvent) int { return cmp.Compare(a.TMS, b.TMS) })
	return t
}

// finish schrijft het rapport en geeft de fout voor de taak: geannuleerd
// blijft geannuleerd, een error maakt de taak mislukt, PASS en FAIL zijn een
// geslaagde taak.
func (r *testRun) finish(ctx context.Context, o outcome) error {
	dctx, cancel := jobs.Detach(ctx)
	defer cancel()
	result, summary, restored := verdict(r.def, r.plan, o)
	timeline, meas := r.report(o)
	err := r.j.Step(dctx, stepReport, func(ctx context.Context, st *jobs.Step) error {
		st.Logf("%s", summary)
		checks := r.run.Checks
		if len(r.plan.Checks) > 0 {
			checks, _ = json.Marshal(r.plan.Checks)
		}
		tl, err := json.Marshal(timeline)
		if err != nil {
			return err
		}
		mb, err := json.Marshal(meas)
		if err != nil {
			return err
		}
		_, err = r.s.q.FinishTestRun(ctx, store.FinishTestRunParams{
			ID: r.run.ID, Result: &result, Restored: restored, Summary: summary, Checks: checks, Timeline: tl, Measurements: mb,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		extra := map[string]any{"result": result, "summary": summary}
		if meas.DowntimeMS != nil {
			extra["downtime_ms"] = *meas.DowntimeMS
		}
		if restored != nil {
			extra["restored"] = *restored
		}
		r.event(ctx, "failover.finished", extra)
		return nil
	})
	switch {
	case jobs.Interrupted(dctx) || err != nil:
		return err
	case result == "canceled":
		return jobs.ErrCanceled
	case result == "error":
		return errors.New(strings.TrimPrefix(summary, "ERROR: "))
	}
	return nil
}

// testFinished rondt een run af waarvan de taak stopte zonder rapport: in
// de wachtrij geannuleerd, te vaak onderbroken of door een interne fout.
// Liep er een storing en lukte de terugkeer niet, dan is restored false en
// toont het cluster de rode balk.
func (s *Service) testFinished(ctx context.Context, j store.Job) {
	run, err := s.q.GetTestRunByJob(ctx, &j.ID)
	if err != nil || run.Result != nil {
		return
	}
	var def Definition
	_ = json.Unmarshal(run.Definition, &def)
	steps, err := s.q.ListJobSteps(ctx, j.ID)
	if err != nil {
		s.log.Error("stappen van failovertest lezen", "job", j.ID, "err", err)
		return
	}
	injected, returned := false, false
	for _, st := range steps {
		switch st.Name {
		case stepMeasure:
			var ms measureState
			injected = json.Unmarshal(st.State, &ms) == nil && !ms.InjectedAt.IsZero()
		case stepReturn:
			returned = st.Status == store.JobStatusSucceeded
		}
	}
	result, summary := "error", "ERROR: de taak stopte voor het rapport"
	if j.Error != "" {
		summary += ": " + j.Error
	}
	var restored *bool
	switch {
	case injected:
		restored = &returned
		if !returned {
			summary += fmt.Sprintf("; %s staat mogelijk nog uit op %s; kies Opnieuw herstellen", def.Unit, run.Hostname)
		}
	case j.Status == store.JobStatusCanceled:
		result, summary = "canceled", "Afgebroken voor de start; er is niets veranderd"
	}
	if _, err := s.q.FinishTestRun(ctx, store.FinishTestRunParams{
		ID: run.ID, Result: &result, Restored: restored, Summary: summary, Checks: run.Checks,
		Timeline: []byte("[]"), Measurements: []byte("{}"),
	}); err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			s.log.Error("failovertest afronden", "run", run.ID, "err", err)
		}
		return
	}
	payload := map[string]any{
		"run_id": run.ID, "test_id": def.TestID, "name": def.Name, "node_id": run.NodeID, "hostname": run.Hostname,
		"unit": def.Unit, "vip": def.VIP, "result": result, "summary": summary,
	}
	if restored != nil {
		payload["restored"] = *restored
	}
	_ = s.ev.Write(ctx, nil, events.Event{
		Actor: events.System(), SubjectType: "test_run", SubjectID: run.ID.String(), ClusterID: run.ClusterID,
		Action: "failover.finished", Payload: payload,
	})
	s.changed()
}

// runRestore start de dienst van een run die niet volledig hersteld is en
// controleert de terugkeer.
func (s *Service) runRestore(ctx context.Context, j *jobs.Job) error {
	run, def, err := s.loadRun(ctx, j)
	if err != nil {
		return err
	}
	if run.NodeID == nil {
		return errors.New("de node van deze run bestaat niet meer")
	}
	defer s.changed()
	target, cluster := *run.NodeID, *run.ClusterID
	var rs restoreState
	ran := false
	err = j.Step(ctx, stepRestore, func(ctx context.Context, st *jobs.Step) error {
		ran = true
		st.Logf("%s weer starten op %s", def.Unit, run.Hostname)
		if err := s.apply(ctx, st, target, j.ID.String()+"-clear", "failovertest "+def.Name+" opnieuw herstellen", def.Unit, "started", 3); err != nil {
			return err
		}
		rs.ClearedAt = time.Now()
		_ = s.ev.Write(ctx, nil, events.Event{
			Actor: events.System(), SubjectType: "test_run", SubjectID: run.ID.String(), ClusterID: &cluster,
			Action: "failover.fault_cleared", Payload: map[string]any{
				"run_id": run.ID, "test_id": def.TestID, "name": def.Name, "node_id": target, "hostname": run.Hostname,
				"unit": def.Unit, "vip": def.VIP, "restore": true,
			},
		})
		return st.SetState(ctx, rs)
	})
	if err != nil {
		return err
	}
	if !ran {
		j.Done(stepRestore, &rs)
	}
	err = j.Step(ctx, stepReturn, func(ctx context.Context, st *jobs.Step) error {
		ctx, cancel := context.WithTimeoutCause(ctx, s.ReturnTimeout, errReturnTimeout)
		defer cancel()
		progress := func(msg string) {
			st.Logf("%s", msg)
			_ = st.Flush(ctx)
		}
		if err := s.Gate.NodeReady(ctx, target, rs.ClearedAt, []string{def.Unit}, progress); err != nil {
			return err
		}
		st.Logf("%s draait weer op %s", def.Unit, run.Hostname)
		rows, err := s.q.ListVIPsByCluster(ctx, cluster)
		if err != nil {
			return err
		}
		vips := []string{}
		for _, v := range rows {
			vips = append(vips, v.Vip.Address.String())
		}
		want := map[string]uuid.UUID{}
		if def.ExpectFailback {
			want[def.VIP] = target
		}
		snap, err := s.Gate.Settled(ctx, cluster, vips, rs.ClearedAt, want, progress)
		if err != nil {
			return err
		}
		if o, ok := snap.Owner(def.VIP); ok {
			st.Logf("%s staat op %s", def.VIP, o.Hostname)
		}
		return nil
	})
	if err != nil {
		return err
	}
	return j.Step(ctx, "Run bijwerken", func(ctx context.Context, st *jobs.Step) error {
		st.Logf("de run telt nu als hersteld")
		return s.q.SetTestRunRestored(ctx, store.SetTestRunRestoredParams{ID: run.ID, Summary: run.Summary + " Daarna opnieuw hersteld."})
	})
}

// probeLog houdt de uitkomsten van de probe bij, in ms na de storing.
type probeLog struct {
	mu        sync.Mutex
	start     time.Time
	vip       string
	segs      []Segment
	events    []TimelineEvent
	firstFail time.Time
	down      bool
	okRun     int
	okStart   time.Time
}

func newProbeLog(start time.Time, vip string) *probeLog { return &probeLog{start: start, vip: vip} }

func (p *probeLog) ms(t time.Time) int64 { return t.Sub(p.start).Milliseconds() }

func (p *probeLog) add(at time.Time, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	ok, ms := err == nil, p.ms(at)
	if n := len(p.segs); n > 0 {
		p.segs[n-1].ToMS = ms
		if p.segs[n-1].OK != ok {
			p.segs = append(p.segs, Segment{FromMS: ms, ToMS: ms, OK: ok})
		}
	} else {
		p.segs = append(p.segs, Segment{FromMS: ms, ToMS: ms, OK: ok})
	}
	if !ok {
		if !p.down {
			p.down = true
			p.events = append(p.events, TimelineEvent{TMS: ms, Kind: "down", Text: p.vip + " onbereikbaar: " + short(err.Error())})
		}
		if p.firstFail.IsZero() {
			p.firstFail = at
		}
		p.okRun = 0
		return
	}
	if p.okRun == 0 {
		p.okStart = at
	}
	p.okRun++
	if p.down && p.okRun == 3 {
		p.down = false
		p.events = append(p.events, TimelineEvent{TMS: p.ms(p.okStart), Kind: "up", Text: p.vip + " weer bereikbaar"})
	}
}

// recovered: de probe slaagde drie keer op rij na de laatste fout.
func (p *probeLog) recovered() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.okRun >= 3
}

// downtime loopt van de eerste fout tot de eerste van de drie goede op rij,
// of tot end als het VIP niet terugkwam.
func (p *probeLog) downtime(end time.Time) time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch {
	case p.firstFail.IsZero():
		return 0
	case p.okRun >= 3:
		return p.okStart.Sub(p.firstFail)
	}
	return end.Sub(p.firstFail)
}

// finish sluit het laatste stuk af op end.
func (p *probeLog) finish(end time.Time) ([]Segment, []TimelineEvent) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if n := len(p.segs); n > 0 {
		p.segs[n-1].ToMS = max(p.segs[n-1].ToMS, p.ms(end))
	}
	return slices.Clone(p.segs), slices.Clone(p.events)
}

func short(s string) string {
	if r := []rune(s); len(r) > 120 {
		return string(r[:120]) + "…"
	}
	return s
}
