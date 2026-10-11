package disaster

import (
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
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/failover"
	"github.com/Jonasz1996/clusterforge/internal/jobs"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// De vijf vaste stappen van disaster.run, in deze volgorde.
const (
	stepPrecheck = "Voorcontrole"
	stepMeasure  = "Storing en meten"
	stepRestore  = "Herstellen"
	stepRecover  = "Herstel controleren"
	stepReport   = "Rapport"
)

// runParams gaan mee met de taak; de rest staat in test_runs.
type runParams struct {
	RunID uuid.UUID `json:"run_id"`
}

// Start doet de voorcontrole, controleert de bevestiging en zet de
// rampoefening in de wachtrij: de run en de taak in één transactie, door het
// clusterslot van elk geraakt cluster en het testslot.
func (s *Service) Start(ctx context.Context, actor events.Actor, id uuid.UUID, confirm []string) (store.TestRun, error) {
	row, err := s.q.GetDisasterScenario(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.TestRun{}, ErrNotFound
	}
	if err != nil {
		return store.TestRun{}, err
	}
	sc := Decode(row)
	p, err := s.plan(ctx, sc, uuid.Nil)
	if err != nil {
		return store.TestRun{}, err
	}
	if !p.OK {
		return store.TestRun{}, &failover.PrecheckError{Checks: p.Checks}
	}
	got := slices.Clone(confirm)
	for i := range got {
		got[i] = strings.TrimSpace(got[i])
	}
	slices.Sort(got)
	got = slices.Compact(got)
	if !slices.Equal(got, p.Confirm) {
		return store.TestRun{}, &failover.ConflictError{Code: "needs_confirmation",
			Msg: "tik ter bevestiging de slug van elk geraakt cluster in: " + strings.Join(p.Confirm, ", ")}
	}
	def := p.Definition
	defJSON, err := json.Marshal(def)
	if err != nil {
		return store.TestRun{}, err
	}
	checks, err := json.Marshal(p.Checks)
	if err != nil {
		return store.TestRun{}, err
	}
	var run store.TestRun
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		var err error
		run, err = q.InsertDisasterRun(ctx, store.InsertDisasterRunParams{
			ClusterID: &sc.ClusterID, ScenarioID: &sc.ID, ClusterIds: def.clusterIDs(), Definition: defJSON, Checks: checks,
			RequestedBy: userOf(actor),
		})
		if err != nil {
			return err
		}
		j, err := s.jobs.EnqueueForClusterTx(ctx, q, jobs.Spec{
			Kind: KindRun, Title: "Rampoefening: " + def.Name, Params: runParams{RunID: run.ID},
			ClusterID: &sc.ClusterID, Clusters: def.targetClusters(), Actor: actor,
		})
		if err != nil {
			return err
		}
		run.JobID = &j.ID
		if err := q.SetTestRunJob(ctx, store.SetTestRunJobParams{ID: run.ID, JobID: &j.ID}); err != nil {
			return err
		}
		return s.ev.Write(ctx, q, events.Event{
			Actor: actor, SubjectType: "test_run", SubjectID: run.ID.String(), ClusterID: &sc.ClusterID,
			Action: "disaster.started", Payload: eventPayload(run.ID, def, nil),
		})
	})
	if err != nil {
		return store.TestRun{}, slotError(err)
	}
	s.jobs.Kick()
	return run, nil
}

// slotError maakt van een bezet slot een ConflictError.
func slotError(err error) error {
	var pgErr *pgconn.PgError
	var busy jobs.BusyError
	switch {
	case errors.As(err, &pgErr) && pgErr.ConstraintName == "jobs_test_slot":
		return &failover.ConflictError{Code: "busy", Msg: "er loopt al een test of back-upcontrole; er loopt er hoogstens één tegelijk"}
	case errors.As(err, &busy):
		return &failover.ConflictError{Code: "busy", Msg: busy.Error()}
	}
	return err
}

func eventPayload(runID uuid.UUID, def Definition, extra map[string]any) map[string]any {
	targets := make([]string, len(def.Targets))
	for i, t := range def.Targets {
		targets[i] = t.Hostname
	}
	clusters := make([]string, len(def.Clusters))
	for i, c := range def.Clusters {
		clusters[i] = c.Name
	}
	out := map[string]any{
		"run_id": runID, "scenario_id": def.ScenarioID, "name": def.Name, "kind": def.Kind, "description": def.Description,
		"targets": targets, "clusters": clusters,
	}
	maps.Copy(out, extra)
	return out
}

// precheckState is de state van de voorcontrole in de taak.
type precheckState struct {
	Skipped string           `json:"skipped,omitempty"`
	Checks  []failover.Check `json:"checks"`
}

// TargetResult is wat de storing of het herstel op één doel deed.
type TargetResult struct {
	NodeID   uuid.UUID `json:"node_id"`
	Hostname string    `json:"hostname"`
	Error    string    `json:"error,omitempty"`
	// Task is bij een VM de Proxmox-taak.
	Task string `json:"task,omitempty"`
}

// VIPMeasure is wat de probe op één VIP zag.
type VIPMeasure struct {
	ClusterID uuid.UUID      `json:"cluster_id"`
	Address   string         `json:"address"`
	Probe     failover.Probe `json:"probe"`
	// LongestMS is de langste onderbreking aan één stuk, DownMS alle
	// onderbrekingen samen.
	LongestMS int64              `json:"longest_ms"`
	DownMS    int64              `json:"down_ms"`
	Error     string             `json:"error,omitempty"`
	Segments  []failover.Segment `json:"segments"`
}

// StatusMeasure is de slechtste status van een cluster tijdens de storing.
type StatusMeasure struct {
	ClusterID uuid.UUID `json:"cluster_id"`
	Worst     string    `json:"worst"`
	Reason    string    `json:"reason"`
}

// Measurement is wat de meting tijdens de storing zag.
type Measurement struct {
	WindowMS int64                    `json:"window_ms"`
	EndMS    int64                    `json:"end_ms"`
	Canceled bool                     `json:"canceled,omitempty"`
	VIPs     []VIPMeasure             `json:"vips"`
	Status   []StatusMeasure          `json:"status"`
	Timeline []failover.TimelineEvent `json:"timeline"`
}

// measureState is de state van de stap Storing en meten. InjectedAt staat
// vast voor de commando's de deur uitgaan; een hervatte stap stuurt de
// storing dus nooit opnieuw.
type measureState struct {
	InjectedAt  time.Time      `json:"injected_at"`
	Injected    []TargetResult `json:"injected,omitempty"`
	InjectError string         `json:"inject_error,omitempty"`
	Interrupted bool           `json:"interrupted,omitempty"`
	Measurement *Measurement   `json:"measurement,omitempty"`
}

type restoreState struct {
	ClearedAt time.Time `json:"cleared_at"`
}

// ClusterRecovery is wanneer een cluster na het terugzetten weer gezond was.
type ClusterRecovery struct {
	ClusterID uuid.UUID `json:"cluster_id"`
	// RecoveredMS is in ms na het terugzetten; nil als het niet herstelde.
	RecoveredMS *int64 `json:"recovered_ms"`
	// Waiting zegt waarop het cluster nog wachtte als het niet herstelde.
	Waiting string `json:"waiting,omitempty"`
}

// recoveryState is de state van de stap Herstel controleren.
type recoveryState struct {
	ReadyMS  int64                    `json:"ready_ms"`
	EndMS    int64                    `json:"end_ms"`
	Clusters []ClusterRecovery        `json:"clusters"`
	VIPs     []VIPMeasure             `json:"vips"`
	Timeline []failover.TimelineEvent `json:"timeline"`
}

// disasterRun is één uitvoering van disaster.run.
type disasterRun struct {
	s   *Service
	j   *jobs.Job
	run store.TestRun
	def Definition

	injectedAt time.Time
	clearedAt  time.Time
	ran        map[string]bool
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
	// Alleen de vaste units gaan ooit naar een agent, ook als de definitie
	// anders zegt.
	for _, t := range def.Targets {
		if t.VM == nil && t.Unit != "keepalived" && !slices.Contains(Units, t.Unit) {
			return store.TestRun{}, Definition{}, fmt.Errorf("onbekende dienst %q", t.Unit)
		}
	}
	return row.TestRun, def, nil
}

// runDisaster voert een rampoefening uit. Na de storing volgt altijd
// herstel: ook bij annuleren, bij een fout, bij een nette stop van de server
// (noodherstel in een defer) en na een crash (de hervatte taak herstelt).
func (s *Service) runDisaster(ctx context.Context, j *jobs.Job) error {
	run, def, err := s.loadRun(ctx, j)
	if err != nil {
		return err
	}
	r := &disasterRun{s: s, j: j, run: run, def: def, ran: map[string]bool{}}
	defer s.changed()
	return r.execute(ctx)
}

func (r *disasterRun) step(ctx context.Context, name string, state any, fn func(ctx context.Context, st *jobs.Step) error) error {
	err := r.j.Step(ctx, name, func(ctx context.Context, st *jobs.Step) error {
		r.ran[name] = true
		return fn(ctx, st)
	})
	if !r.ran[name] {
		r.j.Done(name, state)
	}
	return err
}

func (r *disasterRun) execute(ctx context.Context) error {
	var pre precheckState
	if err := r.step(ctx, stepPrecheck, &pre, func(ctx context.Context, st *jobs.Step) error {
		return r.precheck(ctx, st, &pre)
	}); err != nil {
		if jobs.Interrupted(ctx) {
			return err
		}
		return r.finish(ctx, outcome{precheckErr: err, canceled: jobs.Canceled(ctx)})
	}
	if pre.Skipped != "" {
		return r.finish(ctx, outcome{skipped: pre.Skipped})
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
	// alles starten, met een eigen limiet en los van de taak.
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
		var rec recoveryState
		o.restoreErr = r.step(dctx, stepRecover, &rec, func(ctx context.Context, st *jobs.Step) error {
			return r.recoverStep(ctx, st, &rec)
		})
		if jobs.Interrupted(dctx) {
			return o.restoreErr
		}
		if o.restoreErr == nil {
			o.rec = &rec
		}
	}
	return r.finish(ctx, o)
}

// precheck doet de voorcontrole opnieuw, met de sloten van deze taak. Zijn
// de doelen sinds de start veranderd, dan start de oefening niet: de admin
// bevestigde wat hij toen zag.
func (r *disasterRun) precheck(ctx context.Context, st *jobs.Step, pre *precheckState) error {
	sc := Scenario{
		DisasterScenario: store.DisasterScenario{ID: r.def.ScenarioID, ClusterID: r.def.ClusterID, Name: r.def.Name, Kind: r.def.Kind},
		P:                r.def.Params, E: r.def.Expect,
	}
	p, err := r.s.plan(ctx, sc, r.j.ID)
	if ctx.Err() != nil {
		return context.Cause(ctx)
	}
	if err != nil {
		return err
	}
	var failed []string
	for _, ch := range p.Checks {
		mark := "✓"
		if !ch.OK {
			mark = "✗"
			failed = append(failed, ch.Detail)
		}
		st.Logf("%s %s: %s", mark, ch.Name, ch.Detail)
	}
	pre.Checks = p.Checks
	switch {
	case len(failed) > 0:
		pre.Skipped = strings.Join(failed, "; ")
	case targetKey(p.Targets) != targetKey(r.def.Targets):
		pre.Skipped = "de doelen veranderden sinds de start: nu " + list(targetNames(p.Targets)) + ", bij de start " + list(targetNames(r.def.Targets))
	}
	if pre.Skipped != "" {
		st.Logf("de rampoefening start niet; er verandert niets")
	}
	if checks, err := json.Marshal(p.Checks); err == nil {
		_ = r.s.q.SetTestRunChecks(ctx, store.SetTestRunChecksParams{ID: r.run.ID, Checks: checks})
	}
	return st.SetState(ctx, pre)
}

func targetKey(ts []Target) string {
	keys := make([]string, len(ts))
	for i, t := range ts {
		k := t.NodeID.String() + "/" + t.Unit
		if t.VM != nil {
			k += fmt.Sprintf("/%s/%d", t.VM.ConnectionID, t.VM.VMID)
		}
		keys[i] = k
	}
	slices.Sort(keys)
	return strings.Join(keys, ",")
}

func targetNames(ts []Target) []string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = t.Hostname
	}
	return out
}

func (r *disasterRun) measureStep(ctx context.Context, st *jobs.Step, ms *measureState) error {
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
	ms.InjectedAt = time.Now()
	if err := st.SetState(ctx, ms); err != nil {
		ms.InjectedAt = time.Time{}
		return err
	}
	st.Logf("%s", faultText(r.def))
	_ = st.Flush(ctx)
	ms.Injected = r.s.each(ctx, r.def.Targets, func(ctx context.Context, t Target) (string, error) {
		if t.VM != nil {
			return r.s.Proxmox.PowerVM(ctx, t.VM.ConnectionID, t.VM.VMID, "stop")
		}
		return "", r.s.apply(ctx, st, t.NodeID, r.run.ID.String()+"-inject-"+t.NodeID.String(), r.reason(), t.Unit, "stopped", 1)
	})
	failed := failedTargets(ms.Injected)
	switch {
	case jobs.Interrupted(ctx):
		// De server stopt terwijl de commando's onderweg zijn, dus de storing
		// is er misschien al. De hervatte stap telt de meting niet.
		return context.Cause(ctx)
	case jobs.Canceled(ctx):
		// Afgebroken terwijl de commando's onderweg waren: het herstel volgt.
	case len(failed) > 0:
		ms.InjectError = strings.Join(failed, "; ")
		st.Logf("de storing lukte niet overal: %s", ms.InjectError)
		_ = st.SetState(ctx, ms)
		return errors.New(ms.InjectError)
	default:
		r.event(ctx, "disaster.fault_injected", nil)
	}
	_ = st.SetState(ctx, ms)
	m := r.measure(ctx, st, ms.InjectedAt)
	if jobs.Interrupted(ctx) {
		return context.Cause(ctx)
	}
	// De meting liep al terwijl Proxmox de VM's uitzette; nu moet blijken of
	// dat ook lukte.
	var bad []string
	for _, res := range ms.Injected {
		i := slices.IndexFunc(r.def.Targets, func(t Target) bool { return t.NodeID == res.NodeID })
		if res.Task == "" || i < 0 || r.def.Targets[i].VM == nil {
			continue
		}
		wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
		err := r.s.Proxmox.WaitVMTask(wctx, r.def.Targets[i].VM.ConnectionID, res.Task, nil)
		cancel()
		if err != nil {
			bad = append(bad, fmt.Sprintf("%s: %v", res.Hostname, err))
		}
	}
	if len(bad) > 0 {
		ms.InjectError = "VM uitzetten lukte niet: " + strings.Join(bad, "; ")
		st.Logf("%s", ms.InjectError)
	}
	ms.Measurement = &m
	return st.SetState(ctx, ms)
}

func failedTargets(rs []TargetResult) []string {
	var out []string
	for _, r := range rs {
		if r.Error != "" {
			out = append(out, r.Hostname+": "+r.Error)
		}
	}
	return out
}

func (r *disasterRun) reason() string { return "rampoefening " + r.def.Name }

// each voert fn tegelijk uit voor elk doel.
func (s *Service) each(ctx context.Context, ts []Target, fn func(ctx context.Context, t Target) (string, error)) []TargetResult {
	out := make([]TargetResult, len(ts))
	var wg sync.WaitGroup
	for i, t := range ts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			task, err := fn(ctx, t)
			out[i] = TargetResult{NodeID: t.NodeID, Hostname: t.Hostname, Task: task}
			if err != nil {
				out[i].Error = err.Error()
			}
		}()
	}
	wg.Wait()
	return out
}

// probeSet vraagt een reeks VIP's tegelijk op, elk met een eigen ProbeLog.
type probeSet struct {
	vips []VIPPlanOf
	logs []*failover.ProbeLog
}

// VIPPlanOf is een VIP met zijn cluster.
type VIPPlanOf struct {
	VIPPlan
	ClusterID uuid.UUID
	Cluster   string
}

func (d Definition) allVIPs() []VIPPlanOf {
	var out []VIPPlanOf
	for _, c := range d.Clusters {
		for _, v := range c.VIPs {
			out = append(out, VIPPlanOf{VIPPlan: v, ClusterID: c.ID, Cluster: c.Name})
		}
	}
	return out
}

func newProbeSet(start time.Time, vips []VIPPlanOf) *probeSet {
	ps := &probeSet{vips: vips}
	for _, v := range vips {
		ps.logs = append(ps.logs, failover.NewProbeLog(start, v.Address))
	}
	return ps
}

// run vraagt elk VIP op tot ctx afloopt.
func (ps *probeSet) run(ctx context.Context, s *Service) {
	var wg sync.WaitGroup
	for i, v := range ps.vips {
		wg.Add(1)
		go func() {
			defer wg.Done()
			t := time.NewTicker(s.ProbeInterval)
			defer t.Stop()
			for {
				at := time.Now()
				err := s.Prober.Probe(ctx, v.Address, v.Probe)
				if ctx.Err() != nil {
					return
				}
				ps.logs[i].Add(at, err)
				select {
				case <-ctx.Done():
					return
				case <-t.C:
				}
			}
		}()
	}
	wg.Wait()
}

// finish sluit de logs af en geeft per VIP de meting en de tijdlijn.
func (ps *probeSet) finish(end time.Time) ([]VIPMeasure, []failover.TimelineEvent) {
	out := []VIPMeasure{}
	timeline := []failover.TimelineEvent{}
	for i, v := range ps.vips {
		segs, evs := ps.logs[i].Finish(end)
		vm := VIPMeasure{ClusterID: v.ClusterID, Address: v.Address, Probe: v.Probe, Segments: segs}
		if vm.Segments == nil {
			vm.Segments = []failover.Segment{}
		}
		for _, sg := range segs {
			if !sg.OK {
				vm.DownMS += sg.ToMS - sg.FromMS
				vm.LongestMS = max(vm.LongestMS, sg.ToMS-sg.FromMS)
			}
		}
		vm.Error, _ = ps.logs[i].Failed()
		out = append(out, vm)
		for _, e := range evs {
			e.Text = v.Cluster + ": " + e.Text
			timeline = append(timeline, e)
		}
	}
	return out, timeline
}

// measure vraagt tijdens de storing elk VIP op en leest de status van elk
// gemeten cluster, tot de storing lang genoeg duurde.
func (r *disasterRun) measure(ctx context.Context, st *jobs.Step, injectedAt time.Time) Measurement {
	s, def := r.s, r.def
	window := def.Expect.fault()
	m := Measurement{WindowMS: window.Milliseconds()}
	vips := def.allVIPs()
	st.Logf("meten: %d VIP's elke %d ms opvragen en de status van %d clusters volgen, %s lang", len(vips), s.ProbeInterval.Milliseconds(),
		len(def.Clusters), failover.Seconds(window))
	_ = st.Flush(ctx)
	mctx, cancel := context.WithDeadline(ctx, injectedAt.Add(window))
	defer cancel()
	ps := newProbeSet(injectedAt, vips)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		ps.run(mctx, s)
	}()
	worst := map[uuid.UUID]StatusMeasure{}
	var statusEvents []failover.TimelineEvent
	ids := def.clusterIDs()
	names := map[uuid.UUID]string{}
	for _, c := range def.Clusters {
		names[c.ID] = c.Name
		worst[c.ID] = StatusMeasure{ClusterID: c.ID, Worst: c.Before}
	}
	look := func() {
		rows, err := s.q.ListDisasterClusters(context.WithoutCancel(ctx), ids)
		if err != nil {
			return
		}
		for _, row := range rows {
			cur := worst[row.ID]
			if now, reason := effective(row); rank(now) > rank(cur.Worst) {
				worst[row.ID] = StatusMeasure{ClusterID: row.ID, Worst: now, Reason: reason}
				text := names[row.ID] + " werd " + StatusLabel(now)
				if reason != "" {
					text += ": " + reason
				}
				statusEvents = append(statusEvents, failover.TimelineEvent{TMS: time.Since(injectedAt).Milliseconds(), Kind: "step", Text: text})
				st.Logf("%s", text)
				_ = st.Flush(ctx)
			}
		}
	}
	t := time.NewTicker(s.StatusPoll)
	defer t.Stop()
	look()
loop:
	for {
		select {
		case <-mctx.Done():
			break loop
		case <-t.C:
			look()
		}
	}
	wg.Wait()
	end := time.Now()
	if ctx.Err() != nil {
		m.Canceled = jobs.Canceled(ctx)
	} else {
		look()
	}
	m.EndMS = end.Sub(injectedAt).Milliseconds()
	var probeEvents []failover.TimelineEvent
	m.VIPs, probeEvents = ps.finish(end)
	for _, c := range def.Clusters {
		m.Status = append(m.Status, worst[c.ID])
	}
	m.Timeline = sortTimeline(append(probeEvents, statusEvents...))
	if m.Canceled {
		st.Logf("afgebroken; nu volgt het herstel")
	} else {
		st.Logf("de storing duurde %s; nu volgt het herstel", failover.Seconds(end.Sub(injectedAt)))
	}
	return m
}

// restore start alles wat de storing stopte.
func (r *disasterRun) restore(ctx context.Context, st *jobs.Step) error {
	st.Logf("%s", clearText(r.def))
	_ = st.Flush(ctx)
	res := r.s.each(ctx, r.def.Targets, func(ctx context.Context, t Target) (string, error) {
		if t.VM != nil {
			return "", r.s.startVM(ctx, st, t)
		}
		return "", r.s.apply(ctx, st, t.NodeID, r.run.ID.String()+"-clear-"+t.NodeID.String(), r.reason(), t.Unit, "started", 3)
	})
	if failed := failedTargets(res); len(failed) > 0 {
		if ctx.Err() != nil {
			return context.Cause(ctx)
		}
		return errors.New(strings.Join(failed, "; "))
	}
	r.clearedAt = time.Now()
	r.event(ctx, "disaster.fault_cleared", nil)
	return st.SetState(ctx, restoreState{ClearedAt: r.clearedAt})
}

var errRecoverTimeout = errors.New("het herstel duurde te lang")

// hardLimit is hoe lang het herstel hoogstens mag duren: na een harde stop
// moeten de VM's eerst opstarten, en de verwachting kan langer zijn.
func (s *Service) hardLimit(def Definition) time.Duration {
	limit := s.ReturnTimeout
	if def.Kind == HostDown {
		limit = s.BootTimeout
	}
	return max(limit, def.Expect.recovery()+30*time.Second)
}

// readyUnits zijn de units die na het herstel weer moeten draaien. Na een
// harde stop is een verse heartbeat genoeg.
func readyUnits(t Target) []string {
	if t.Unit == "" {
		return nil
	}
	return []string{t.Unit}
}

// recoverStep wacht tot elk doel weer draait en elk VIP van een geraakt
// cluster één houder heeft; lukt dat niet, dan is het herstel mislukt. Daarna
// wacht hij tot elk gemeten cluster weer antwoordt en minstens zo gezond is
// als bij de start. Wat dan niet herstelde, telt in het rapport.
func (r *disasterRun) recoverStep(ctx context.Context, st *jobs.Step, rec *recoveryState) error {
	s, def := r.s, r.def
	limit := s.hardLimit(def)
	deadline := r.clearedAt.Add(limit)
	wctx, cancel := context.WithDeadlineCause(ctx, deadline, errRecoverTimeout)
	defer cancel()
	ps := newProbeSet(r.injectedAt, def.allVIPs())
	pctx, pstop := context.WithCancel(wctx)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		ps.run(pctx, s)
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
		if errors.Is(context.Cause(wctx), errRecoverTimeout) {
			msg := "het herstel lukte niet binnen " + failover.Seconds(limit)
			if last != "" {
				msg += ": " + last
			}
			return errors.New(msg)
		}
		return err
	}
	ms := func(t time.Time) int64 { return t.Sub(r.injectedAt).Milliseconds() }
	var timeline []failover.TimelineEvent

	// 1. Elk doel draait weer, met twee verse heartbeats.
	for _, t := range def.Targets {
		if err := s.Gate.NodeReady(wctx, t.NodeID, r.clearedAt, readyUnits(t), progress); err != nil {
			return failed(err)
		}
	}
	// 2. Elk VIP van een geraakt cluster heeft één houder.
	for _, c := range def.Clusters {
		if c.Role != RoleTarget || len(c.VIPs) == 0 {
			continue
		}
		vips := make([]string, len(c.VIPs))
		for i, v := range c.VIPs {
			vips[i] = v.Address
		}
		if _, err := s.Gate.Settled(wctx, c.ID, vips, r.clearedAt, nil, progress); err != nil {
			return failed(err)
		}
	}
	ready := time.Now()
	rec.ReadyMS = ms(ready)
	timeline = append(timeline, failover.TimelineEvent{TMS: rec.ReadyMS, Kind: "ready", Text: list(targetNames(def.Targets)) + " weer klaar"})
	st.Logf("%s draaien weer", list(targetNames(def.Targets)))
	_ = st.Flush(ctx)

	// 3. Elk gemeten cluster antwoordt weer en is minstens zo gezond als bij
	// de start.
	recovered := map[uuid.UUID]time.Time{}
	waiting := map[uuid.UUID]string{}
	// statusLate: het cluster wachtte op zijn status; dan telt het moment
	// waarop die weer goed was.
	statusLate := map[uuid.UUID]bool{}
	check := func() bool {
		rows, err := s.q.ListDisasterClusters(context.WithoutCancel(ctx), def.clusterIDs())
		if err != nil {
			return false
		}
		cur := map[uuid.UUID]store.ListDisasterClustersRow{}
		for _, row := range rows {
			cur[row.ID] = row
		}
		done := true
		for _, c := range def.Clusters {
			if _, ok := recovered[c.ID]; ok {
				continue
			}
			at := r.clearedAt
			if c.Role == RoleTarget {
				at = ready
			}
			var why []string
			for i, v := range ps.vips {
				if v.ClusterID != c.ID {
					continue
				}
				t, ok := ps.logs[i].RecoveredAt()
				if !ok {
					why = append(why, v.Address+" antwoordt nog niet")
				} else if t.After(at) {
					at = t
				}
			}
			row, ok := cur[c.ID]
			if st, reason := effective(row); ok && rank(st) > max(rank(c.Before), 0) {
				w := "status " + StatusLabel(st)
				if reason != "" {
					w += " (" + reason + ")"
				}
				why = append(why, w)
				statusLate[c.ID] = true
			}
			if len(why) > 0 {
				waiting[c.ID] = strings.Join(why, ", ")
				done = false
				continue
			}
			delete(waiting, c.ID)
			if now := time.Now(); statusLate[c.ID] && now.After(at) {
				at = now
			}
			recovered[c.ID] = at
			text := c.Name + " weer gezond"
			timeline = append(timeline, failover.TimelineEvent{TMS: ms(recovered[c.ID]), Kind: "return", Text: text})
			st.Logf("%s, %s na het terugzetten", text, failover.Seconds(recovered[c.ID].Sub(r.clearedAt)))
			_ = st.Flush(ctx)
		}
		return done
	}
	t := time.NewTicker(min(s.StatusPoll, 500*time.Millisecond))
	defer t.Stop()
wait:
	for !check() {
		select {
		case <-wctx.Done():
			break wait
		case <-t.C:
		}
	}
	if ctx.Err() != nil {
		return context.Cause(ctx)
	}
	stop()
	end := time.Now()
	rec.EndMS = ms(end)
	var probeEvents []failover.TimelineEvent
	rec.VIPs, probeEvents = ps.finish(end)
	for i := range probeEvents {
		if probeEvents[i].Kind == "down" {
			probeEvents[i].Text += " na het terugzetten"
		}
	}
	for _, c := range def.Clusters {
		cr := ClusterRecovery{ClusterID: c.ID}
		if at, ok := recovered[c.ID]; ok {
			d := at.Sub(r.clearedAt).Milliseconds()
			cr.RecoveredMS = &d
		} else {
			cr.Waiting = waiting[c.ID]
			st.Logf("%s herstelde niet binnen %s: %s", c.Name, failover.Seconds(limit), cr.Waiting)
		}
		rec.Clusters = append(rec.Clusters, cr)
	}
	rec.Timeline = sortTimeline(append(timeline, probeEvents...))
	return st.SetState(ctx, *rec)
}

// startVM zet een VM weer aan en wacht op Proxmox. Draait hij al, dan is er
// niets te doen.
func (s *Service) startVM(ctx context.Context, st *jobs.Step, t Target) error {
	if s.Proxmox == nil {
		return errors.New("ClusterForge heeft geen Proxmox-koppeling meer; start de VM in Proxmox")
	}
	if g, err := s.Proxmox.GuestStatus(ctx, t.VM.ConnectionID, t.VM.VMID); err == nil && g.Status == "running" {
		st.Logf("VM %d van %s draait al", t.VM.VMID, t.Hostname)
		return nil
	}
	upid, err := s.Proxmox.PowerVM(ctx, t.VM.ConnectionID, t.VM.VMID, "start")
	if err != nil {
		return err
	}
	// Meer VM's starten tegelijk; de uitvoer van één taak zou die van de
	// andere vervangen.
	if err := s.Proxmox.WaitVMTask(ctx, t.VM.ConnectionID, upid, nil); err != nil {
		return err
	}
	st.Logf("VM %d van %s gestart", t.VM.VMID, t.Hostname)
	_ = st.Flush(ctx)
	return nil
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
	}
	for _, sr := range res.Steps {
		for _, line := range sr.Output {
			st.Logf("%s", line)
		}
	}
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

// emergency start alles als de herstelstap niet lukte of niet liep, los van
// de taak en met een eigen limiet.
func (r *disasterRun) emergency() {
	ctx, cancel := context.WithTimeout(events.WithJob(context.Background(), r.j.ID), r.s.EmergencyTimeout)
	defer cancel()
	res := r.s.each(ctx, r.def.Targets, func(ctx context.Context, t Target) (string, error) {
		if t.VM != nil {
			if r.s.Proxmox == nil {
				return "", errors.New("geen Proxmox-koppeling")
			}
			return r.s.Proxmox.PowerVM(ctx, t.VM.ConnectionID, t.VM.VMID, "start")
		}
		res, err := r.s.bus.Command(ctx, t.NodeID, serviceCommand(r.run.ID.String()+"-clear-"+t.NodeID.String(), r.reason()+" (noodherstel)", t.Unit, "started"))
		if err == nil && !res.OK {
			err = errors.New(res.Error)
		}
		return "", err
	})
	for _, x := range res {
		if x.Error != "" {
			r.s.log.Error("noodherstel van rampoefening mislukt", "run", r.run.ID, "node", x.Hostname, "err", x.Error)
		} else {
			r.s.log.Info("noodherstel van rampoefening", "run", r.run.ID, "node", x.Hostname)
		}
	}
}

func (r *disasterRun) event(ctx context.Context, action string, extra map[string]any) {
	if err := r.s.ev.Write(ctx, nil, events.Event{
		Actor: events.System(), SubjectType: "test_run", SubjectID: r.run.ID.String(), ClusterID: &r.def.ClusterID,
		Action: action, Payload: eventPayload(r.run.ID, r.def, extra),
	}); err != nil {
		r.s.log.Warn("event van rampoefening", "action", action, "err", err)
	}
}

// faultText en clearText beschrijven de storing en het herstel.
func faultText(def Definition) string {
	names := list(targetNames(def.Targets))
	switch def.Kind {
	case HostDown:
		return def.Params.Host + " valt uit: " + names + " hard uitgezet"
	}
	return def.Unit + " gestopt op " + names
}

func clearText(def Definition) string {
	names := list(targetNames(def.Targets))
	if def.Kind == HostDown {
		return names + " weer gestart"
	}
	return def.Unit + " weer gestart op " + names
}

// StillDown zegt wat er mogelijk nog uit staat als het herstel niet lukte.
func StillDown(def Definition) string {
	names := list(targetNames(def.Targets))
	if def.Kind == HostDown {
		return "de VM's van " + names + " staan mogelijk nog uit"
	}
	return def.Unit + " staat mogelijk nog uit op " + names
}

// runFinished rondt een run af waarvan de taak stopte zonder rapport: in de
// wachtrij geannuleerd, te vaak onderbroken of door een interne fout. Liep
// er een storing en lukte het herstel niet, dan is restored false en toont
// elk gemeten cluster de rode balk.
func (s *Service) runFinished(ctx context.Context, j store.Job) {
	run, err := s.q.GetTestRunByJob(ctx, &j.ID)
	if err != nil || run.Result != nil {
		return
	}
	var def Definition
	_ = json.Unmarshal(run.Definition, &def)
	steps, err := s.q.ListJobSteps(ctx, j.ID)
	if err != nil {
		s.log.Error("stappen van rampoefening lezen", "job", j.ID, "err", err)
		return
	}
	injected, recovered := false, false
	for _, st := range steps {
		switch st.Name {
		case stepMeasure:
			var ms measureState
			injected = json.Unmarshal(st.State, &ms) == nil && !ms.InjectedAt.IsZero()
		case stepRecover:
			recovered = st.Status == store.JobStatusSucceeded
		}
	}
	result, summary := "error", "ERROR: de taak stopte voor het rapport"
	if j.Error != "" {
		summary += ": " + j.Error
	}
	var restored *bool
	switch {
	case injected:
		restored = &recovered
		if !recovered {
			summary += "; " + StillDown(def) + "; kies Opnieuw herstellen"
		}
	case j.Status == store.JobStatusCanceled:
		result, summary = "canceled", "Afgebroken voor de start; er is niets veranderd"
	}
	if _, err := s.q.FinishTestRun(ctx, store.FinishTestRunParams{
		ID: run.ID, Result: &result, Restored: restored, Summary: summary, Checks: run.Checks,
		Timeline: []byte("[]"), Measurements: []byte("{}"),
	}); err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			s.log.Error("rampoefening afronden", "run", run.ID, "err", err)
		}
		return
	}
	extra := map[string]any{"result": result, "summary": summary}
	if restored != nil {
		extra["restored"] = *restored
	}
	_ = s.ev.Write(ctx, nil, events.Event{
		Actor: events.System(), SubjectType: "test_run", SubjectID: run.ID.String(), ClusterID: run.ClusterID,
		Action: "disaster.finished", Payload: eventPayload(run.ID, def, extra),
	})
	s.changed()
}

// StartRestore zet een taak in de wachtrij die een rampoefening die niet
// volledig hersteld is, opnieuw herstelt.
func (s *Service) StartRestore(ctx context.Context, actor events.Actor, runID uuid.UUID) (store.Job, error) {
	row, err := s.q.GetTestRun(ctx, runID)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.Job{}, ErrNotFound
	}
	if err != nil {
		return store.Job{}, err
	}
	run := row.TestRun
	switch {
	case run.Kind != KindRun:
		return store.Job{}, &failover.ConflictError{Code: "conflict", Msg: "dit is geen rampoefening"}
	case run.Restored == nil || *run.Restored:
		return store.Job{}, &failover.ConflictError{Code: "conflict", Msg: "deze run hoeft niet hersteld te worden"}
	}
	var def Definition
	if err := json.Unmarshal(run.Definition, &def); err != nil {
		return store.Job{}, err
	}
	j, err := s.jobs.EnqueueForCluster(ctx, jobs.Spec{
		Kind: KindRestore, Title: "Rampoefening herstellen: " + def.Name, Params: runParams{RunID: run.ID},
		ClusterID: run.ClusterID, Clusters: def.targetClusters(), Actor: actor,
	})
	if err != nil {
		return store.Job{}, slotError(err)
	}
	return j, nil
}

// runRestore start alles van een run die niet volledig hersteld is en
// controleert dat het weer draait.
func (s *Service) runRestore(ctx context.Context, j *jobs.Job) error {
	run, def, err := s.loadRun(ctx, j)
	if err != nil {
		return err
	}
	defer s.changed()
	var rs restoreState
	ran := false
	err = j.Step(ctx, stepRestore, func(ctx context.Context, st *jobs.Step) error {
		ran = true
		st.Logf("%s", clearText(def))
		res := s.each(ctx, def.Targets, func(ctx context.Context, t Target) (string, error) {
			if t.VM != nil {
				return "", s.startVM(ctx, st, t)
			}
			return "", s.apply(ctx, st, t.NodeID, j.ID.String()+"-clear-"+t.NodeID.String(), "rampoefening "+def.Name+" opnieuw herstellen", t.Unit, "started", 3)
		})
		if failed := failedTargets(res); len(failed) > 0 {
			return errors.New(strings.Join(failed, "; "))
		}
		rs.ClearedAt = time.Now()
		_ = s.ev.Write(ctx, nil, events.Event{
			Actor: events.System(), SubjectType: "test_run", SubjectID: run.ID.String(), ClusterID: run.ClusterID,
			Action: "disaster.fault_cleared", Payload: eventPayload(run.ID, def, map[string]any{"restore": true}),
		})
		return st.SetState(ctx, rs)
	})
	if err != nil {
		return err
	}
	if !ran {
		j.Done(stepRestore, &rs)
	}
	err = j.Step(ctx, stepRecover, func(ctx context.Context, st *jobs.Step) error {
		ctx, cancel := context.WithTimeoutCause(ctx, s.hardLimit(def), errRecoverTimeout)
		defer cancel()
		progress := func(msg string) {
			st.Logf("%s", msg)
			_ = st.Flush(ctx)
		}
		for _, t := range def.Targets {
			if err := s.Gate.NodeReady(ctx, t.NodeID, rs.ClearedAt, readyUnits(t), progress); err != nil {
				return err
			}
		}
		for _, c := range def.Clusters {
			if c.Role != RoleTarget || len(c.VIPs) == 0 {
				continue
			}
			vips := make([]string, len(c.VIPs))
			for i, v := range c.VIPs {
				vips[i] = v.Address
			}
			if _, err := s.Gate.Settled(ctx, c.ID, vips, rs.ClearedAt, nil, progress); err != nil {
				return err
			}
		}
		st.Logf("%s draaien weer", list(targetNames(def.Targets)))
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
