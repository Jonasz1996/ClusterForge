package backups

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

// RunScheduled start de geplande back-upcontroles die aan de beurt zijn,
// per cluster voor de node die het langst niet gecontroleerd is. Kan geen
// enkele node, of is de run te laat, dan komt er een overgeslagen run
// zonder taak en schuift de planning naar het volgende venster. Zolang er
// een andere test loopt, wacht hij.
func (s *Service) RunScheduled(ctx context.Context, now time.Time) error {
	if s.runner == nil {
		return nil
	}
	due, err := s.q.ListDueBackupVerifies(ctx, &now)
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
		d, reason := s.Window.Decide(*row.NextRunAt, free, now, busy)
		switch d {
		case planner.Wait:
			continue
		case planner.Skip:
			if err := s.skip(ctx, row, reason, now); err != nil {
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

func (s *Service) runScheduled(ctx context.Context, row store.ListDueBackupVerifiesRow, now time.Time) (bool, error) {
	cands, err := s.q.ListVerifyCandidates(ctx, &row.ClusterID)
	if err != nil {
		return false, err
	}
	var why []string
	for _, c := range cands {
		n, def, err := s.prepare(ctx, c.ID, "")
		var ce ConflictError
		switch {
		case errors.As(err, &ce):
			why = append(why, c.Hostname+": "+ce.Msg)
			continue
		case err != nil:
			return false, err
		}
		run, err := s.enqueue(ctx, events.System(), n, def, TriggerSchedule)
		if errors.As(err, &ce) && ce.Code == "busy" {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		s.log.Info("geplande back-upcontrole gestart", "cluster", row.ClusterName, "node", n.Hostname, "run", run.ID)
		next := s.Window.Following(*row.NextRunAt, now)
		return true, s.q.SetBackupVerifyNextRun(ctx, store.SetBackupVerifyNextRunParams{ClusterID: row.ClusterID, NextRunAt: &next})
	}
	reason := "geen node van dit cluster is aan een VM in Proxmox gekoppeld"
	if len(why) > 0 {
		reason = "geen node met een back-up die te controleren is (" + strings.Join(why, "; ") + ")"
	}
	return false, s.skip(ctx, row, reason, now)
}

// skip schrijft een overgeslagen geplande controle zonder taak en schuift
// de planning naar het volgende venster.
func (s *Service) skip(ctx context.Context, row store.ListDueBackupVerifiesRow, reason string, now time.Time) error {
	next := s.Window.Following(*row.NextRunAt, now)
	result, summary := "skipped", "Overgeslagen: "+reason
	defJSON, err := json.Marshal(Definition{})
	if err != nil {
		return err
	}
	var run store.TestRun
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		r, err := q.InsertTestRun(ctx, store.InsertTestRunParams{
			Kind: KindVerify, Trigger: TriggerSchedule, ClusterID: &row.ClusterID, Definition: defJSON, Checks: []byte("[]"),
		})
		if err != nil {
			return err
		}
		if run, err = q.FinishTestRun(ctx, store.FinishTestRunParams{
			ID: r.ID, Result: &result, Summary: summary, Checks: []byte("[]"), Timeline: []byte("[]"), Measurements: []byte("{}"),
		}); err != nil {
			return err
		}
		return q.SetBackupVerifyNextRun(ctx, store.SetBackupVerifyNextRunParams{ClusterID: row.ClusterID, NextRunAt: &next})
	})
	if err != nil {
		return err
	}
	// Zonder node draagt de run de naam van het cluster.
	run.Hostname = "cluster " + row.ClusterName
	s.finishedEvent(ctx, run, Definition{}, Measurements{})
	return nil
}

// SetVerifySchedule zet de geplande controle van een cluster aan of uit.
func (s *Service) SetVerifySchedule(ctx context.Context, actor events.Actor, by *uuid.UUID, clusterID uuid.UUID, on bool) error {
	cur, err := s.Policy(ctx, clusterID)
	if err != nil {
		return err
	}
	if cur.VerifyEnabled == on {
		return nil
	}
	var next *time.Time
	if on {
		t := s.Window.First(s.Now())
		next = &t
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		c, err := q.LockCluster(ctx, clusterID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if err := q.SetBackupVerifySchedule(ctx, store.SetBackupVerifyScheduleParams{
			ClusterID: clusterID, VerifyEnabled: on, NextRunAt: next, UpdatedBy: by,
		}); err != nil {
			return err
		}
		return s.ev.Write(ctx, q, events.Event{
			Actor: actor, SubjectType: "cluster", SubjectID: clusterID.String(), ClusterID: &clusterID,
			Action:  "backup.policy_updated",
			Payload: map[string]any{"name": c.Name, "verify_enabled": map[string]any{"from": cur.VerifyEnabled, "to": on}},
		})
	})
}
