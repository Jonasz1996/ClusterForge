package failover

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/planner"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

// RunScheduled start de geplande failovertests die aan de beurt zijn. De
// voorcontrole loopt vóór er een taak is: lukt ze niet, of is de run te
// laat, dan komt er een overgeslagen run zonder taak en schuift de test
// naar het volgende venster. Zolang er een andere test loopt, wacht hij.
func (s *Service) RunScheduled(ctx context.Context, now time.Time) error {
	due, err := s.q.ListDueFailoverTests(ctx, &now)
	if err != nil || len(due) == 0 {
		return err
	}
	busy, err := s.slotBusy(ctx)
	if err != nil {
		return err
	}
	free, err := s.q.GetTestSlotReleasedAt(ctx)
	if err != nil {
		return err
	}
	for _, row := range due {
		t := row.FailoverTest
		d, reason := s.Window.Decide(*t.NextRunAt, free, now, busy)
		switch d {
		case planner.Wait:
			continue
		case planner.Skip:
			if err := s.skip(ctx, row, nil, reason, nil, now); err != nil {
				return err
			}
			continue
		}
		started, err := s.runScheduled(ctx, row, now)
		if err != nil {
			return err
		}
		busy = busy || started
	}
	return nil
}

func (s *Service) slotBusy(ctx context.Context) (bool, error) {
	_, err := s.q.GetTestSlotJob(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// runScheduled doet de voorcontrole van een geplande test en start hem, of
// slaat hem over. started is true als er een taak is.
func (s *Service) runScheduled(ctx context.Context, row store.ListDueFailoverTestsRow, now time.Time) (started bool, err error) {
	t := row.FailoverTest
	c, err := s.q.GetCluster(ctx, t.ClusterID)
	if err != nil {
		return false, err
	}
	if c.Environment == store.EnvironmentProd {
		// Het cluster is na het plannen naar prod verhuisd; daar draait een
		// test alleen met de hand.
		return false, s.skip(ctx, row, nil, "het cluster staat nu in prod; daar start een failovertest alleen met de hand. De planning staat nu uit", nil, now)
	}
	def, err := definitionOf(store.GetFailoverTestRow{FailoverTest: t, VipAddress: row.VipAddress})
	if err != nil {
		return false, s.skip(ctx, row, nil, err.Error(), nil, now)
	}
	plan, err := s.precheck(ctx, c, def, uuid.Nil, TriggerSchedule)
	var pe *PrecheckError
	switch {
	case errors.As(err, &pe):
		if onlySlots(pe.Checks) {
			// Er loopt nog een test of een taak in het cluster: wachten, niet
			// overslaan. Duurt dat te lang, dan slaat Decide de run over.
			return false, nil
		}
		return false, s.skip(ctx, row, &def, failedText(pe.Checks), pe.Checks, now)
	case err != nil:
		return false, err
	}
	run, err := s.enqueue(ctx, events.System(), c, def, plan, TriggerSchedule)
	var ce *ConflictError
	if errors.As(err, &ce) && ce.Code == "busy" {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	s.log.Info("geplande failovertest gestart", "test", t.Name, "run", run.ID)
	return true, s.q.SetFailoverTestNextRun(ctx, store.SetFailoverTestNextRunParams{
		ID: t.ID, Scheduled: true, NextRunAt: ptr(s.Window.Following(*t.NextRunAt, now)),
	})
}

func onlySlots(checks []Check) bool {
	for _, c := range checks {
		if !c.OK && c.Name != "Testslot" && c.Name != "Clusterslot" {
			return false
		}
	}
	return true
}

func failedText(checks []Check) string {
	var out []string
	for _, c := range checks {
		if !c.OK {
			out = append(out, c.Detail)
		}
	}
	return strings.Join(out, "; ")
}

func ptr[T any](v T) *T { return &v }

// skip schrijft een overgeslagen geplande run zonder taak en schuift de
// test naar het volgende venster. Op prod gaat de planning uit.
func (s *Service) skip(ctx context.Context, row store.ListDueFailoverTestsRow, def *Definition, reason string, checks []Check, now time.Time) error {
	t := row.FailoverTest
	if def == nil {
		d := Definition{
			TestID: t.ID, Name: t.Name, Scenario: t.Scenario, Service: t.Service, Unit: unitOf(t.Scenario, t.Service),
			VIPID: t.VipID, VIP: row.VipAddress.String(), MaxTakeoverSeconds: int(t.MaxTakeoverSeconds), ExpectFailback: t.ExpectFailback,
		}
		_ = json.Unmarshal(t.Probe, &d.Probe)
		def = &d
	}
	defJSON, err := json.Marshal(def)
	if err != nil {
		return err
	}
	if checks == nil {
		checks = []Check{}
	}
	checkJSON, err := json.Marshal(checks)
	if err != nil {
		return err
	}
	c, err := s.q.GetCluster(ctx, t.ClusterID)
	if err != nil {
		return err
	}
	next := ptr(s.Window.Following(*t.NextRunAt, now))
	scheduled := c.Environment != store.EnvironmentProd
	if !scheduled {
		next = nil
	}
	summary := "Overgeslagen: " + reason
	result := "skipped"
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		run, err := q.InsertTestRun(ctx, store.InsertTestRunParams{
			Kind: KindTest, Trigger: TriggerSchedule, ClusterID: &t.ClusterID, TestID: &t.ID,
			Definition: defJSON, Checks: checkJSON,
		})
		if err != nil {
			return err
		}
		if _, err := q.FinishTestRun(ctx, store.FinishTestRunParams{
			ID: run.ID, Result: &result, Summary: summary, Checks: checkJSON, Timeline: []byte("[]"), Measurements: []byte("{}"),
		}); err != nil {
			return err
		}
		if err := q.SetFailoverTestNextRun(ctx, store.SetFailoverTestNextRunParams{ID: t.ID, Scheduled: scheduled, NextRunAt: next}); err != nil {
			return err
		}
		if !scheduled {
			if err := s.ev.Write(ctx, q, events.Event{
				Actor: events.System(), SubjectType: "failover_test", SubjectID: t.ID.String(), ClusterID: &t.ClusterID,
				Action: "failover_test.updated", Payload: map[string]any{
					"test": t.Name, "scheduled": map[string]any{"from": true, "to": false}, "reason": "het cluster staat in prod",
				},
			}); err != nil {
				return err
			}
		}
		return s.ev.Write(ctx, q, events.Event{
			Actor: events.System(), SubjectType: "test_run", SubjectID: run.ID.String(), ClusterID: &t.ClusterID,
			Action: "failover.finished", Payload: map[string]any{
				"run_id": run.ID, "test_id": t.ID, "name": t.Name, "unit": def.Unit, "vip": def.VIP,
				"result": result, "summary": summary, "trigger": TriggerSchedule,
			},
		})
	})
}
