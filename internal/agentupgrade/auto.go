package agentupgrade

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

// Kick laat Run meteen kijken.
func (s *Service) Kick() {
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

// Run werkt verouderde agents vanzelf bij, elke Interval, tot ctx afloopt.
// Na de start wacht het StartDelay: na een serverupdate komen de agents
// eerst terug.
func (s *Service) Run(ctx context.Context) {
	if s.Interval <= 0 {
		return
	}
	select {
	case <-ctx.Done():
		return
	case <-time.After(s.StartDelay):
	}
	t := time.NewTicker(s.Interval)
	defer t.Stop()
	for {
		if err := s.AutoRun(ctx); err != nil && ctx.Err() == nil {
			s.log.Warn("agents vanzelf bijwerken mislukt", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-s.kick:
		}
	}
}

// AutoRun start een taak per cluster met agents die vanzelf bijgewerkt
// mogen worden en verbonden zijn, als daar nog geen taak loopt. Een cluster
// waar een andere taak loopt, komt de volgende keer.
func (s *Service) AutoRun(ctx context.Context) error {
	ov, err := s.Overview(ctx)
	if err != nil || !ov.Auto {
		return err
	}
	var todo []Agent
	for _, a := range ov.Agents {
		key := uuid.Nil
		if a.ClusterID != nil {
			key = *a.ClusterID
		}
		if _, busy := ov.Running[key]; busy || !a.Auto || !a.Online {
			continue
		}
		todo = append(todo, a)
	}
	if len(todo) == 0 {
		return nil
	}
	started, skipped, err := s.Start(ctx, events.System(), todo, true)
	for _, j := range started {
		s.log.Info("agents vanzelf bijwerken", "job", j.ID, "title", j.Title)
	}
	for _, why := range skipped {
		s.log.Debug("agents vanzelf bijwerken overgeslagen", "why", why)
	}
	if _, ok := err.(*ConflictError); ok {
		return nil
	}
	return err
}

// SetAuto zet vanzelf bijwerken aan of uit voor alle clusters.
func (s *Service) SetAuto(ctx context.Context, actor events.Actor, userID *uuid.UUID, enabled bool) error {
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		if err := q.SetAgentUpgradeAuto(ctx, store.SetAgentUpgradeAutoParams{Auto: enabled, UpdatedBy: userID}); err != nil {
			return err
		}
		return s.ev.Write(ctx, q, events.Event{
			Actor: actor, SubjectType: "settings", SubjectID: "agent-upgrade", Action: "settings.agent_upgrade_auto",
			Payload: map[string]any{"enabled": enabled},
		})
	})
	if err == nil && enabled {
		s.Kick()
	}
	return err
}

// SetClusterAuto zet vanzelf bijwerken aan of uit voor één cluster.
func (s *Service) SetClusterAuto(ctx context.Context, actor events.Actor, userID *uuid.UUID, clusterID uuid.UUID, enabled bool) error {
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		c, err := q.GetCluster(ctx, clusterID)
		if err != nil {
			return err
		}
		if enabled {
			err = q.DeleteAgentUpgradeOff(ctx, clusterID)
		} else {
			err = q.SetAgentUpgradeOff(ctx, store.SetAgentUpgradeOffParams{ClusterID: clusterID, UpdatedBy: userID})
		}
		if err != nil {
			return err
		}
		return s.ev.Write(ctx, q, events.Event{
			Actor: actor, SubjectType: "cluster", SubjectID: clusterID.String(), ClusterID: &clusterID,
			Action: "cluster.agent_upgrade_auto", Payload: map[string]any{"name": c.Name, "enabled": enabled},
		})
	})
	if err == nil && enabled {
		s.Kick()
	}
	return err
}
