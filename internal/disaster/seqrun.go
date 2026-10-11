package disaster

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/failover"
	"github.com/Jonasz1996/clusterforge/internal/jobs"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// StepResult is wat één stap van een reeks deed. AtMS is het tijdstip
// waarop de commando's weggingen, in ms na de start van de storing.
type StepResult struct {
	Step    int            `json:"step"`
	AtMS    int64          `json:"at_ms"`
	Text    string         `json:"text"`
	Targets []TargetResult `json:"targets"`
}

var errStepFailed = errors.New("een stap van de reeks lukte niet")

// check weigert een definitie met een dienst of storing die nooit naar een
// agent mag.
func (d Definition) check() error {
	if d.Kind == Sequence {
		for _, ps := range d.Steps {
			if !slices.ContainsFunc(Actions, func(a Action) bool { return a.Key == ps.Action }) {
				return fmt.Errorf("onbekende stap %q", ps.Action)
			}
			if ps.Action == ActServiceStop && !slices.Contains(stopUnits, ps.Unit) {
				return fmt.Errorf("onbekende dienst %q", ps.Unit)
			}
		}
		return nil
	}
	for _, t := range d.Targets {
		if t.VM == nil && t.Unit != "keepalived" && !slices.Contains(Units, t.Unit) {
			return fmt.Errorf("onbekende dienst %q", t.Unit)
		}
	}
	return nil
}

// measureSequence voert de stappen van een reeks uit op hun tijdstip en
// verlengt de lease van elke storing, terwijl de meting loopt. Lukt een stap
// niet, dan stopt de meting en volgt het herstel.
func (r *disasterRun) measureSequence(ctx context.Context, st *jobs.Step, ms *measureState) error {
	sctx, stop := context.WithCancelCause(ctx)
	defer stop(nil)
	done := make(chan struct{})
	go func() {
		defer close(done)
		r.runSteps(sctx, st, ms, stop)
	}()
	m := r.measure(sctx, st, ms.InjectedAt)
	stop(nil)
	<-done
	if jobs.Interrupted(ctx) {
		return context.Cause(ctx)
	}
	for _, sr := range ms.Steps {
		m.Timeline = append(m.Timeline, failover.TimelineEvent{TMS: sr.AtMS, Kind: "fault", Text: fmt.Sprintf("Stap %d: %s", sr.Step, sr.Text)})
	}
	m.Timeline = sortTimeline(m.Timeline)
	ms.Measurement = &m
	if ms.InjectError != "" {
		_ = st.SetState(ctx, ms)
		return errors.New(ms.InjectError)
	}
	return st.SetState(ctx, ms)
}

type renewResult struct {
	node uuid.UUID
	res  protocol.Result
	err  error
}

// runSteps stuurt elke stap op zijn tijdstip en verlengt de storingen elke
// RenewInterval, tot ctx afloopt. Alleen deze goroutine verandert ms zolang
// de meting loopt.
func (r *disasterRun) runSteps(ctx context.Context, st *jobs.Step, ms *measureState, fail context.CancelCauseFunc) {
	s, def := r.s, r.def
	if len(def.Steps) == 0 {
		return
	}
	start := ms.InjectedAt
	save := func() { _ = st.SetState(context.WithoutCancel(ctx), ms) }
	leased := map[uuid.UUID][]string{}
	hosts := map[uuid.UUID]string{}
	warned := map[uuid.UUID]bool{}
	tick := time.NewTicker(s.RenewInterval)
	defer tick.Stop()
	renewed := make(chan []renewResult, 1)
	renewing := false
	next := 0
	timer := time.NewTimer(time.Until(start.Add(def.Steps[0].at())))
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			if renewing || len(leased) == 0 {
				continue
			}
			renewing = true
			batch := maps.Clone(leased)
			go func() { renewed <- r.renewAll(ctx, batch) }()
		case res := <-renewed:
			renewing = false
			for _, x := range res {
				switch {
				case ctx.Err() != nil:
					return
				case x.err != nil:
					if !warned[x.node] {
						st.Logf("lease op %s niet verlengd: %v; de agent houdt de storing tot de lease afloopt", hosts[x.node], x.err)
						warned[x.node] = true
					}
				case !x.res.OK:
					ms.InjectError = fmt.Sprintf("%s: %s; de agent hief de storing voor het einde op", hosts[x.node], x.res.Error)
					st.Logf("%s", ms.InjectError)
					save()
					fail(errStepFailed)
					return
				default:
					warned[x.node] = false
				}
			}
		case <-timer.C:
			ps := def.Steps[next]
			// Een node die deze stap afsnijdt, krijgt eerst nog een verlenging
			// van zijn andere storingen: hun lease van 120 s overbrugt de tijd
			// dat ClusterForge hem niet bereikt.
			for _, sn := range ps.Nodes {
				if ids := leased[sn.NodeID]; sn.CutOff && len(ids) > 0 {
					if res, err := r.renewNode(ctx, sn.NodeID, ids); err != nil || !res.OK {
						st.Logf("lease op %s niet verlengd voor het afsnijden: %v %s", sn.Hostname, err, res.Error)
					}
					delete(leased, sn.NodeID)
				}
			}
			at := time.Since(start).Milliseconds()
			res := r.injectStep(ctx, st, next, ps)
			ms.Steps = append(ms.Steps, StepResult{Step: next + 1, AtMS: at, Text: ps.Text, Targets: res})
			ms.Injected = append(ms.Injected, res...)
			if ctx.Err() != nil {
				// Afgebroken terwijl de commando's onderweg waren: het herstel
				// volgt en heft alles op.
				save()
				return
			}
			if failed := failedTargets(res); len(failed) > 0 {
				ms.InjectError = fmt.Sprintf("stap %d: %s", next+1, strings.Join(failed, "; "))
				st.Logf("stap %d lukte niet overal: %s", next+1, strings.Join(failed, "; "))
				save()
				fail(errStepFailed)
				return
			}
			save()
			st.Logf("stap %d na %s: %s", next+1, failover.Seconds(time.Duration(at)*time.Millisecond), ps.Text)
			_ = st.Flush(ctx)
			r.event(ctx, "disaster.fault_injected", map[string]any{"step": next + 1, "text": ps.Text})
			for _, sn := range ps.Nodes {
				hosts[sn.NodeID] = sn.Hostname
				if isFault(ps.Action) && !sn.CutOff && !cutBefore(def, sn.NodeID, next) {
					leased[sn.NodeID] = append(leased[sn.NodeID], faultID(r.run.ID, next))
				}
			}
			if next++; next < len(def.Steps) {
				timer.Reset(time.Until(start.Add(def.Steps[next].at())))
			}
		}
	}
}

// cutBefore is true als een eerdere stap de node al afsneed.
func cutBefore(def Definition, nodeID uuid.UUID, i int) bool {
	for _, ps := range def.Steps[:i] {
		if slices.ContainsFunc(ps.Nodes, func(n StepNode) bool { return n.NodeID == nodeID && n.CutOff }) {
			return true
		}
	}
	return false
}

// injectStep stuurt één stap tegelijk naar al zijn nodes.
func (r *disasterRun) injectStep(ctx context.Context, st *jobs.Step, i int, ps PlannedStep) []TargetResult {
	s := r.s
	ts := make([]Target, len(ps.Nodes))
	for j, sn := range ps.Nodes {
		ts[j] = Target{NodeID: sn.NodeID, Hostname: sn.Hostname}
	}
	return s.each(ctx, ts, func(ctx context.Context, t Target) (string, error) {
		id := fmt.Sprintf("%s-step%d-%s", r.run.ID, i+1, t.NodeID)
		if ps.Action == ActServiceStop {
			return "", s.apply(ctx, st, t.NodeID, id, r.reason(), ps.Unit, "stopped", 1)
		}
		f := r.fault(i, ps, t.NodeID)
		return "", s.command(ctx, st, t.NodeID, func() protocol.Command {
			return protocol.Command{ID: id, Action: protocol.CmdFaultInject, Reason: r.reason(), Deadline: time.Now().Add(30 * time.Second), Fault: &f}
		}, 1)
	})
}

// fault is de storing van stap i op een node. Ze duurt tot een minuut na
// het einde van de storing, zodat het herstel haar opheft; zonder verlenging
// verdwijnt ze na de lease. link_down op de interface naar ClusterForge
// heeft geen lease en eindigt precies aan het einde van de storing.
func (r *disasterRun) fault(i int, ps PlannedStep, nodeID uuid.UUID) protocol.Fault {
	f := protocol.Fault{
		ID: faultID(r.run.ID, i), Kind: ps.Action, Interface: ps.Interface, Port: ps.Port, DelayMS: ps.DelayMS,
		LossPercent: ps.LossPercent, FillPercent: ps.FillPercent,
	}
	rest := r.def.Expect.FaultSeconds - ps.AtSeconds
	if slices.ContainsFunc(ps.Nodes, func(n StepNode) bool { return n.NodeID == nodeID && n.CutOff }) {
		f.MaxSeconds = max(1, min(rest, int(protocol.FaultMaxCutOff.Seconds())))
		return f
	}
	f.MaxSeconds = max(1, min(rest+60, int(protocol.FaultMax.Seconds())))
	f.LeaseSeconds = r.s.leaseSeconds()
	if r.def.cutOffNode(nodeID) {
		f.LeaseSeconds = maxLease
	}
	return f
}

// maxLease is de langste lease die de agent aanneemt.
const maxLease = 120

func (s *Service) leaseSeconds() int {
	return min(max(int(s.Lease.Seconds()), 1), maxLease)
}

func (r *disasterRun) renewAll(ctx context.Context, batch map[uuid.UUID][]string) []renewResult {
	var mu sync.Mutex
	var wg sync.WaitGroup
	out := make([]renewResult, 0, len(batch))
	for node, ids := range batch {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := r.renewNode(ctx, node, ids)
			mu.Lock()
			out = append(out, renewResult{node: node, res: res, err: err})
			mu.Unlock()
		}()
	}
	wg.Wait()
	return out
}

func (r *disasterRun) renewNode(ctx context.Context, node uuid.UUID, ids []string) (protocol.Result, error) {
	cctx, cancel := context.WithTimeout(ctx, r.s.RenewInterval)
	defer cancel()
	return r.s.bus.Command(cctx, node, protocol.Command{
		ID: fmt.Sprintf("%s-renew-%d", r.run.ID, time.Now().UnixMilli()), Action: protocol.CmdFaultRenew, Reason: r.reason(),
		Deadline: time.Now().Add(r.s.Lease), FaultIDs: ids,
	})
}

// clearNode heft de storingen van een reeks op een node op en start de
// diensten die ze stopte. Een node die afgesneden was, krijgt meer pogingen:
// zijn interface komt pas aan het einde van de storing terug, en daarna moet
// de agent weer verbinden.
func (s *Service) clearNode(ctx context.Context, st *jobs.Step, def Definition, runID uuid.UUID, t Target, id, reason string, attempts int) error {
	if ids := def.faultIDs(runID, t.NodeID); len(ids) > 0 {
		n := attempts
		if def.cutOffNode(t.NodeID) {
			n = max(n, 12)
		}
		if err := s.command(ctx, st, t.NodeID, func() protocol.Command {
			return protocol.Command{ID: id + "-faults", Action: protocol.CmdFaultClear, Reason: reason, Deadline: time.Now().Add(time.Minute), FaultIDs: ids}
		}, n); err != nil {
			return fmt.Errorf("storingen opheffen: %w", err)
		}
	}
	for _, u := range def.stoppedUnits(t.NodeID) {
		if err := s.apply(ctx, st, t.NodeID, id+"-"+u, reason, u, "started", attempts); err != nil {
			return err
		}
	}
	return nil
}

// emergencyClear is clearNode voor het noodherstel: één poging, los van de
// taak.
func (s *Service) emergencyClear(ctx context.Context, def Definition, runID uuid.UUID, t Target, reason string) error {
	id := runID.String() + "-clear-" + t.NodeID.String()
	var errs []error
	if ids := def.faultIDs(runID, t.NodeID); len(ids) > 0 {
		res, err := s.bus.Command(ctx, t.NodeID, protocol.Command{
			ID: id + "-faults", Action: protocol.CmdFaultClear, Reason: reason, Deadline: time.Now().Add(time.Minute), FaultIDs: ids,
		})
		if err == nil && !res.OK {
			err = errors.New(res.Error)
		}
		errs = append(errs, err)
	}
	for _, u := range def.stoppedUnits(t.NodeID) {
		res, err := s.bus.Command(ctx, t.NodeID, serviceCommand(id+"-"+u, reason, u, "started"))
		if err == nil && !res.OK {
			err = errors.New(res.Error)
		}
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}
