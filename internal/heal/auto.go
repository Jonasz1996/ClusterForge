package heal

// Vanzelf herstellen (mijlpaal 3): per cluster aan te zetten, standaard uit
// en nooit op prod. Elke minuut, na het bekijken van de nodes, voert
// AutoRun een plan uit dat vanzelf mag, met een rem: het probleem bestaat
// minstens AutoAfter, hoogstens AutoPerHour pogingen per uur, na een
// mislukte poging AutoBackoff wachten, het cluster opnieuw beginnen
// hoogstens eens per uur, en na AutoMaxFailed mislukte pogingen op rij
// pauze tot een beheerder het opnieuw aanzet.

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

const (
	AutoPerHour    = 3
	AutoBackoff    = 15 * time.Minute
	AutoMaxFailed  = 3
	BootstrapEvery = time.Hour
)

// AutoState is Vanzelf herstellen van een cluster, met waarom het plan nu
// wel of niet vanzelf gebeurt.
type AutoState struct {
	Enabled      bool
	PausedAt     *time.Time
	PausedReason string
	// Allowed is false op prod: daar kan het niet aan.
	Allowed bool
	// Due: AutoRun voert het plan nu uit. Anders zegt Note waarom niet;
	// leeg als er niets te doen is.
	Due  bool
	Note string
}

// ErrAutoProd: op prod herstelt ClusterForge nooit vanzelf.
var ErrAutoProd = &ConflictError{"op prod herstelt ClusterForge nooit vanzelf; daar vraagt elk herstel je bevestiging"}

// SetAuto zet Vanzelf herstellen van een cluster aan of uit. Aanzetten
// heft een pauze op.
func (s *Service) SetAuto(ctx context.Context, actor events.Actor, clusterID uuid.UUID, on bool) (store.HealAuto, error) {
	var a store.HealAuto
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		c, err := q.GetCluster(ctx, clusterID)
		if err != nil {
			return err
		}
		if on && c.Environment == store.EnvironmentProd {
			return ErrAutoProd
		}
		var by *uuid.UUID
		if id, err := uuid.Parse(actor.ID); err == nil && actor.Type == store.ActorTypeUser {
			by = &id
		}
		a, err = q.SetHealAuto(ctx, store.SetHealAutoParams{ClusterID: clusterID, Enabled: on, UpdatedBy: by})
		if err != nil {
			return err
		}
		return s.ev.Write(ctx, q, events.Event{
			Actor: actor, SubjectType: "cluster", SubjectID: clusterID.String(), ClusterID: &clusterID,
			Action: "cluster.heal_auto_changed", Payload: map[string]any{"name": c.Name, "enabled": on},
		})
	})
	if err == nil {
		s.Kick()
	}
	return a, err
}

// AutoRun voert elk plan uit dat nu vanzelf mag.
func (s *Service) AutoRun(ctx context.Context) error {
	reps, ins, err := s.reports(ctx)
	if err != nil {
		return err
	}
	now := s.Now()
	s.mu.Lock()
	keep := map[string]bool{}
	for _, r := range reps {
		if r.Plan != nil && r.Plan.Auto && r.Plan.Blocked == "" && r.Plan.Managed == nil {
			keep[r.Key] = true
			if _, ok := s.seen[r.Key]; !ok {
				s.seen[r.Key] = now
			}
		}
	}
	for k := range s.seen {
		if !keep[k] {
			delete(s.seen, k)
		}
	}
	s.mu.Unlock()
	var errs []error
	for i, r := range reps {
		if r.Auto == nil {
			continue
		}
		if paused, err := s.autoPause(ctx, r); err != nil {
			errs = append(errs, err)
			continue
		} else if paused {
			continue
		}
		// De toestand van zojuist kende seen nog niet.
		st, err := s.autoState(ctx, r, s.setting(ctx, *r.ClusterID), now)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if !st.Due {
			continue
		}
		var titles []string
		for _, p := range r.Problems {
			titles = append(titles, p.Title)
		}
		_, err = s.enqueue(ctx, events.System(), r, ins[i], runParams{Auto: true, Bootstrap: hasProblem(r, CodeNoPrimary)}, strings.Join(titles, "; "))
		var ce *ConflictError
		switch {
		case errors.As(err, &ce):
			// Er loopt al een taak op het cluster; de volgende ronde weer.
			s.log.Info("zelfherstel: vanzelf herstellen wacht", "cluster", r.ClusterName, "reden", ce.Msg)
		case err != nil:
			errs = append(errs, err)
		default:
			s.log.Info("zelfherstel: vanzelf herstellen gestart", "cluster", r.ClusterName, "engine", r.Engine)
		}
	}
	return errors.Join(errs...)
}

func (s *Service) setting(ctx context.Context, id uuid.UUID) store.HealAuto {
	a, err := s.q.GetHealAuto(ctx, id)
	if err != nil {
		return store.HealAuto{ClusterID: id}
	}
	return a
}

func hasProblem(r Report, code string) bool {
	return slices.ContainsFunc(r.Problems, func(p Problem) bool { return p.Code == code })
}

// autoPause zet Vanzelf herstellen op pauze na AutoMaxFailed mislukte
// pogingen op rij sinds het aanging.
func (s *Service) autoPause(ctx context.Context, r Report) (bool, error) {
	a := s.setting(ctx, *r.ClusterID)
	if !a.Enabled || a.PausedAt != nil || a.EnabledAt == nil {
		return a.PausedAt != nil, nil
	}
	runs, err := s.q.ListHealAutoRuns(ctx, store.ListHealAutoRunsParams{ClusterID: r.ClusterID, Since: *a.EnabledAt})
	if err != nil {
		return false, err
	}
	if len(runs) < AutoMaxFailed {
		return false, nil
	}
	for _, run := range runs[:AutoMaxFailed] {
		if run.Status != store.JobStatusFailed {
			return false, nil
		}
	}
	reason := fmt.Sprintf("%d pogingen op rij mislukten", AutoMaxFailed)
	n, err := s.q.PauseHealAuto(ctx, store.PauseHealAutoParams{ClusterID: *r.ClusterID, Reason: reason})
	if err != nil || n == 0 {
		return true, err
	}
	s.log.Warn("zelfherstel: vanzelf herstellen gepauzeerd", "cluster", r.ClusterName, "reden", reason)
	return true, s.ev.Write(ctx, nil, events.Event{
		Actor: events.System(), SubjectType: "cluster", SubjectID: r.ClusterID.String(), ClusterID: r.ClusterID,
		Action: "cluster.heal_auto_paused", Payload: map[string]any{"name": r.ClusterName, "reason": reason, "job_id": runs[0].ID},
	})
}

// autoState zegt of het plan van r nu vanzelf mag, en anders waarom niet.
func (s *Service) autoState(ctx context.Context, r Report, a store.HealAuto, now time.Time) (AutoState, error) {
	st := AutoState{Enabled: a.Enabled, PausedAt: a.PausedAt, PausedReason: a.PausedReason, Allowed: r.ClusterEnv != string(store.EnvironmentProd)}
	p := r.Plan
	note := func(n string) (AutoState, error) {
		st.Note = n
		return st, nil
	}
	switch {
	case p == nil:
		return st, nil
	case p.Managed != nil:
		return note("Dit herstel hoort bij het onderdeel van ClusterForge, met zijn eigen Vanzelf opnieuw starten.")
	case !st.Allowed:
		return note("Op prod herstelt ClusterForge nooit vanzelf.")
	case !a.Enabled:
		if p.Auto {
			return note("Dit plan mag vanzelf, maar Vanzelf herstellen staat uit op dit cluster.")
		}
		return st, nil
	case a.PausedAt != nil:
		return note("Vanzelf herstellen is gepauzeerd: " + a.PausedReason + ". Zet het opnieuw aan om het weer te laten proberen.")
	case p.Blocked != "":
		return note("Het plan kan nu niet, ook niet vanzelf.")
	case !p.Auto:
		return note("Dit plan gebeurt niet vanzelf: " + p.AutoWhy)
	case r.Last != nil && (r.Last.Status == string(store.JobStatusQueued) || r.Last.Status == string(store.JobStatusRunning)):
		return note("Zelfherstel is al bezig.")
	}
	s.mu.Lock()
	since, ok := s.seen[r.Key]
	s.mu.Unlock()
	if !ok {
		since = now
	}
	if wait := since.Add(s.AutoAfter).Sub(now); wait > 0 {
		return note(fmt.Sprintf("Zelfherstel wacht tot het probleem %s bestaat, voor het geval de server zelf herstart (nog %s).", duration(int64(s.AutoAfter.Seconds())), duration(int64(wait.Seconds())+1)))
	}
	hour, err := s.q.ListHealAutoRuns(ctx, store.ListHealAutoRunsParams{ClusterID: r.ClusterID, Since: now.Add(-time.Hour)})
	if err != nil {
		return st, err
	}
	// Opnieuw aanzetten begint de telling opnieuw, maar niet die van het
	// cluster beginnen.
	runs := slices.DeleteFunc(slices.Clone(hour), func(x store.ListHealAutoRunsRow) bool {
		return a.EnabledAt != nil && x.CreatedAt.Before(*a.EnabledAt)
	})
	if len(runs) >= AutoPerHour {
		next := runs[AutoPerHour-1].CreatedAt.Add(time.Hour)
		return note(fmt.Sprintf("Al %d pogingen in het afgelopen uur; de volgende vanzelf om %s.", len(runs), next.Local().Format("15:04")))
	}
	if len(runs) > 0 && runs[0].Status == store.JobStatusFailed && runs[0].FinishedAt != nil {
		if next := runs[0].FinishedAt.Add(AutoBackoff); next.After(now) {
			return note(fmt.Sprintf("De vorige poging mislukte; de volgende vanzelf om %s.", next.Local().Format("15:04")))
		}
	}
	if hasProblem(r, CodeNoPrimary) {
		if slices.ContainsFunc(hour, func(x store.ListHealAutoRunsRow) bool {
			return x.Bootstrap && x.CreatedAt.After(now.Add(-BootstrapEvery))
		}) {
			return note("Het cluster begon het afgelopen uur al vanzelf opnieuw; nog een keer gebeurt alleen met Nu herstellen.")
		}
	}
	st.Due = true
	return note("Zelfherstel voert dit plan nu vanzelf uit.")
}
