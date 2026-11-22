package disaster

// De DR-repetitie als soort rampoefening: het scenario zegt welke bridge en
// hoe vaak; de back-upcontrole doet het werk. Ze raakt het echte cluster
// niet aan en mag daarom ook op prod.

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Jonasz1996/clusterforge/internal/backups"
	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/failover"
	"github.com/Jonasz1996/clusterforge/internal/planner"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

// DRRehearsal zet elke node van het cluster terug uit zijn nieuwste
// back-up, in sandboxes aan een afgesloten bridge, en meet RTO en RPO.
const DRRehearsal = "dr_rehearsal"

// Planningen van een DR-repetitie.
const (
	Weekly  = "weekly"
	Monthly = "monthly"
)

// ScheduleLabel zegt in gewone taal wanneer een repetitie draait.
func ScheduleLabel(schedule string) string {
	switch schedule {
	case Weekly:
		return "elke week in het testvenster"
	case Monthly:
		return "in het eerste testvenster van elke maand"
	}
	return "alleen met de hand"
}

// nextRehearsal is de volgende geplande repetitie: due is de run die net
// aan de beurt was, of nil bij een nieuwe planning.
func nextRehearsal(w planner.Window, schedule string, due *time.Time, now time.Time) *time.Time {
	var t time.Time
	switch schedule {
	case Weekly:
		if due == nil {
			t = w.First(now)
		} else {
			t = w.Following(*due, now)
		}
	case Monthly:
		t = firstOfMonth(w, now, due)
	default:
		return nil
	}
	if t.IsZero() {
		return nil
	}
	return &t
}

// firstOfMonth is het eerste venster van een maand: van deze maand als dat
// nog komt of nu loopt, anders van de volgende. Na een run is het altijd
// een latere maand dan die van de run.
func firstOfMonth(w planner.Window, now time.Time, due *time.Time) time.Time {
	loc := now.Location()
	if w.Loc != nil {
		loc = w.Loc
	}
	first := func(y int, m time.Month) (time.Time, time.Time) {
		day := time.Date(y, m, 1, 0, 0, 0, 0, loc)
		start, end := w.Next(day)
		if !start.IsZero() && start.Before(day) {
			// Het venster van de avond ervoor telt bij de vorige maand.
			start, end = w.Next(end)
		}
		return start, end
	}
	n := now.In(loc)
	y, m := n.Year(), n.Month()
	if due != nil {
		d := due.In(loc)
		y, m = d.Year(), d.Month()+1
	}
	for range 3 {
		start, end := first(y, m)
		switch {
		case start.IsZero():
			return time.Time{}
		case end.After(now):
			if start.Before(now) {
				return now
			}
			return start
		}
		m++
	}
	return time.Time{}
}

// rehearsalInput is wat de back-upcontrole van het scenario nodig heeft:
// de bridge en de VIP's met hun probe.
func (s *Service) rehearsalInput(ctx context.Context, sc Scenario) (backups.RehearsalInput, error) {
	in := backups.RehearsalInput{ScenarioID: sc.ID, Name: sc.Name, ClusterID: sc.ClusterID, Bridge: sc.P.Bridge}
	c, err := s.q.GetCluster(ctx, sc.ClusterID)
	if errors.Is(err, pgx.ErrNoRows) {
		return in, ErrNotFound
	}
	if err != nil {
		return in, err
	}
	vips, err := s.q.ListVIPsByCluster(ctx, sc.ClusterID)
	if err != nil {
		return in, err
	}
	for _, v := range vips {
		addr := v.Vip.Address.String()
		in.VIPs = append(in.VIPs, backups.RehearsalVIP{Address: addr, Probe: s.Probes(c, addr)})
	}
	return in, nil
}

// previewRehearsal is de voorcontrole van een DR-repetitie.
func (s *Service) previewRehearsal(ctx context.Context, sc Scenario) (Preview, error) {
	if s.Backups == nil {
		return Preview{}, &failover.ConflictError{Code: "unavailable", Msg: msgNoBackups}
	}
	in, err := s.rehearsalInput(ctx, sc)
	if err != nil {
		return Preview{}, err
	}
	p, err := s.Backups.PlanRehearsal(ctx, in)
	if err != nil {
		return Preview{}, rehearsalError(err)
	}
	out := Preview{
		Definition: Definition{
			ScenarioID: sc.ID, ClusterID: sc.ClusterID, Name: sc.Name, Kind: sc.Kind, Description: Describe(sc.Kind, sc.P),
			Params: sc.P, Expect: sc.E, Targets: []Target{}, Clusters: []ClusterPlan{}, Skipped: []string{}, Notes: []string{},
		},
		OK: p.OK, Confirm: []string{}, Rehearsal: &p,
	}
	for _, c := range p.Checks {
		out.Checks = append(out.Checks, failover.Check{Name: c.Name, OK: c.OK || c.Warning, Detail: c.Detail})
	}
	return out, nil
}

// startRehearsal start een DR-repetitie met de hand.
func (s *Service) startRehearsal(ctx context.Context, actor events.Actor, sc Scenario) (store.TestRun, error) {
	if s.Backups == nil {
		return store.TestRun{}, &failover.ConflictError{Code: "unavailable", Msg: msgNoBackups}
	}
	in, err := s.rehearsalInput(ctx, sc)
	if err != nil {
		return store.TestRun{}, err
	}
	run, err := s.Backups.StartRehearsal(ctx, actor, in, backups.TriggerManual)
	if err != nil {
		return store.TestRun{}, rehearsalError(err)
	}
	s.changed()
	return run, nil
}

// msgNoBackups: zonder Proxmox-koppeling is er geen back-upcontrole.
const msgNoBackups = "de DR-repetitie heeft de back-upcontrole nodig; zet CF_MASTER_KEY en koppel Proxmox"

// rehearsalError zet een fout van de back-upcontrole om naar die van de
// rampoefeningen.
func rehearsalError(err error) error {
	var pe *backups.PrecheckError
	var ce backups.ConflictError
	var ve backups.ValidationError
	switch {
	case errors.As(err, &pe):
		out := &failover.PrecheckError{}
		for _, c := range pe.Checks {
			out.Checks = append(out.Checks, failover.Check{Name: c.Name, OK: c.OK || c.Warning, Detail: c.Detail})
		}
		return out
	case errors.As(err, &ce):
		return &failover.ConflictError{Code: ce.Code, Msg: ce.Msg}
	case errors.As(err, &ve):
		return fieldErr("bridge", ve.Msg)
	case errors.Is(err, backups.ErrNotFound):
		return ErrNotFound
	}
	return err
}

// RunScheduled start de geplande DR-repetities die aan de beurt zijn, in het
// testvenster en alleen als er geen andere test loopt. Is het venster
// voorbij, dan komt er een overgeslagen run en schuift de planning door.
func (s *Service) RunScheduled(ctx context.Context, now time.Time) error {
	if s.Backups == nil {
		return nil
	}
	due, err := s.q.ListDueRehearsals(ctx, now)
	if err != nil || len(due) == 0 {
		return err
	}
	busy := true
	if _, err := s.q.GetTestSlotJob(ctx); errors.Is(err, pgx.ErrNoRows) {
		busy = false
	} else if err != nil {
		return err
	}
	free, err := s.q.GetTestSlotReleasedAt(ctx)
	if err != nil {
		return err
	}
	for _, row := range due {
		sc := Decode(row.DisasterScenario)
		d, reason := s.Window.Decide(*sc.NextRunAt, free, now, busy)
		if d == planner.Wait {
			continue
		}
		next := nextRehearsal(s.Window, sc.Schedule, sc.NextRunAt, now)
		in, err := s.rehearsalInput(ctx, sc)
		if err != nil {
			return err
		}
		if d == planner.Skip {
			if err := s.Backups.SkipRehearsal(ctx, in, row.ClusterName, reason); err != nil {
				return err
			}
			if err := s.q.SetScenarioNextRun(ctx, store.SetScenarioNextRunParams{ID: sc.ID, NextRunAt: next}); err != nil {
				return err
			}
			continue
		}
		run, err := s.Backups.StartRehearsal(ctx, events.System(), in, backups.TriggerSchedule)
		var ce backups.ConflictError
		if errors.As(err, &ce) && ce.Code == "busy" {
			busy = true
			continue
		}
		if err != nil {
			s.log.Error("geplande DR-repetitie starten", "scenario", sc.Name, "err", err)
			if err := s.Backups.SkipRehearsal(ctx, in, row.ClusterName, "de repetitie kon niet starten: "+err.Error()); err != nil {
				return err
			}
		} else {
			s.log.Info("geplande DR-repetitie gestart", "cluster", row.ClusterName, "scenario", sc.Name, "run", run.ID)
			busy = busy || run.Result == nil
		}
		if err := s.q.SetScenarioNextRun(ctx, store.SetScenarioNextRunParams{ID: sc.ID, NextRunAt: next}); err != nil {
			return err
		}
		s.changed()
	}
	return nil
}
