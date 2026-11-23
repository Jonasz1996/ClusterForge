package agentupgrade

import (
	"context"
	"errors"
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
// waar een andere taak loopt, komt de volgende keer. Op prod nooit meer dan
// één cluster tegelijk: eerst de rij van "ook productie", dan wat vanzelf
// mag.
func (s *Service) AutoRun(ctx context.Context) error {
	ov, err := s.Overview(ctx)
	if err != nil {
		return err
	}
	prodBusy := false
	for cluster := range ov.Running {
		if ov.isProd(cluster) {
			prodBusy = true
		}
	}
	if !prodBusy && len(ov.ProdQueue) > 0 {
		if prodBusy, err = s.nextQueued(ctx, ov); err != nil {
			return err
		}
	}
	if !ov.Auto || (ov.NightOnly && !s.InWindow(s.Now())) {
		return nil
	}
	queued := map[uuid.UUID]bool{}
	for _, q := range ov.ProdQueue {
		queued[q.ClusterID] = true
	}
	var todo []Agent
	var prodCluster *uuid.UUID
	for _, a := range ov.Agents {
		key := uuid.Nil
		if a.ClusterID != nil {
			key = *a.ClusterID
		}
		if _, busy := ov.Running[key]; busy || !a.Auto || !a.Online {
			continue
		}
		if a.Prod() {
			// Eén prodcluster tegelijk; de rij gaat voor.
			if prodBusy || queued[key] || (prodCluster != nil && *prodCluster != key) {
				continue
			}
			prodCluster = &key
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

// isProd zegt of een cluster een prodcluster is, volgens zijn agents.
func (ov Overview) isProd(cluster uuid.UUID) bool {
	for _, a := range ov.Agents {
		if a.ClusterID != nil && *a.ClusterID == cluster {
			return a.Prod()
		}
	}
	return false
}

// nextQueued werkt de rij van "ook productie" af: een gelukte taak haalt
// zijn cluster uit de rij, een mislukte of geannuleerde stopt de hele rij,
// en het volgende cluster met agents om bij te werken krijgt een taak.
// started zegt of er nu een prodtaak loopt.
func (s *Service) nextQueued(ctx context.Context, ov Overview) (started bool, err error) {
	for _, q := range ov.ProdQueue {
		if q.JobID != nil {
			j, err := s.q.GetJob(ctx, *q.JobID)
			switch {
			case errors.Is(err, pgx.ErrNoRows):
			case err != nil:
				return false, err
			case j.Job.Status == store.JobStatusQueued || j.Job.Status == store.JobStatusRunning:
				return true, nil
			case j.Job.Status != store.JobStatusSucceeded:
				return false, s.stopQueue(ctx, events.System(), "de taak in "+q.Name+" is niet gelukt ("+j.Job.Error+")", q.JobID)
			}
			if err := s.q.DeleteAgentUpgradeProdQueue(ctx, q.ClusterID); err != nil {
				return false, err
			}
			continue
		}
		var agents []Agent
		for _, a := range ov.Agents {
			if a.ClusterID != nil && *a.ClusterID == q.ClusterID && a.Upgradable() {
				agents = append(agents, a)
			}
		}
		if len(agents) == 0 {
			if err := s.q.DeleteAgentUpgradeProdQueue(ctx, q.ClusterID); err != nil {
				return false, err
			}
			continue
		}
		actor := events.System()
		if q.RequestedBy != nil {
			actor = events.User(*q.RequestedBy)
		}
		jobs, _, err := s.Start(ctx, actor, agents, false)
		var ce *ConflictError
		if errors.As(err, &ce) {
			// Er loopt al een andere taak op dit cluster: later opnieuw.
			return true, nil
		}
		if err != nil {
			return false, err
		}
		if len(jobs) == 1 {
			if err := s.q.SetAgentUpgradeProdQueueJob(ctx, store.SetAgentUpgradeProdQueueJobParams{ClusterID: q.ClusterID, JobID: &jobs[0].ID}); err != nil {
				return false, err
			}
		}
		return true, nil
	}
	return false, nil
}

// QueueProd zet prodclusters in de rij van "ook productie", in deze
// volgorde. ClusterForge werkt ze één voor één bij.
func (s *Service) QueueProd(ctx context.Context, actor events.Actor, userID *uuid.UUID, clusters []QueuedCluster) error {
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		names := make([]string, 0, len(clusters))
		for _, c := range clusters {
			if err := q.AddAgentUpgradeProdQueue(ctx, store.AddAgentUpgradeProdQueueParams{ClusterID: c.ClusterID, RequestedBy: userID}); err != nil {
				return err
			}
			names = append(names, c.Name)
		}
		return s.ev.Write(ctx, q, events.Event{
			Actor: actor, SubjectType: "settings", SubjectID: "agent-upgrade", Action: "agents.prod_upgrade_queued",
			Payload: map[string]any{"clusters": names},
		})
	})
	if err == nil {
		s.Kick()
	}
	return err
}

// ClearProdQueue haalt de clusters uit de rij die nog niet begonnen zijn
// en stopt zo de rest van een klik "ook productie". Een taak die loopt,
// loopt verder; die annuleer je bij Taken.
func (s *Service) ClearProdQueue(ctx context.Context, actor events.Actor) error {
	return s.stopQueue(ctx, actor, "gestopt met de knop", nil)
}

func (s *Service) stopQueue(ctx context.Context, actor events.Actor, why string, job *uuid.UUID) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		n, err := q.ClearAgentUpgradeProdQueue(ctx)
		if err != nil || n == 0 {
			return err
		}
		payload := map[string]any{"why": why}
		if job != nil {
			payload["job_id"] = *job
		}
		return s.ev.Write(ctx, q, events.Event{
			Actor: actor, SubjectType: "settings", SubjectID: "agent-upgrade", Action: "agents.prod_upgrade_stopped", Payload: payload,
		})
	})
}

// SetProdAuto zet vanzelf bijwerken op prod aan of uit.
func (s *Service) SetProdAuto(ctx context.Context, actor events.Actor, userID *uuid.UUID, enabled bool) error {
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		if err := q.SetAgentUpgradeProdAuto(ctx, store.SetAgentUpgradeProdAutoParams{ProdAuto: enabled, UpdatedBy: userID}); err != nil {
			return err
		}
		return s.ev.Write(ctx, q, events.Event{
			Actor: actor, SubjectType: "settings", SubjectID: "agent-upgrade", Action: "settings.agent_upgrade_prod_auto",
			Payload: map[string]any{"enabled": enabled},
		})
	})
	if err == nil && enabled {
		s.Kick()
	}
	return err
}

// SetNightOnly: vanzelf bijwerken alleen 's nachts (testlijst 2, 2.10).
func (s *Service) SetNightOnly(ctx context.Context, actor events.Actor, userID *uuid.UUID, enabled bool) error {
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		if err := q.SetAgentUpgradeNightOnly(ctx, store.SetAgentUpgradeNightOnlyParams{NightOnly: enabled, UpdatedBy: userID}); err != nil {
			return err
		}
		return s.ev.Write(ctx, q, events.Event{
			Actor: actor, SubjectType: "settings", SubjectID: "agent-upgrade", Action: "settings.agent_upgrade_night_only",
			Payload: map[string]any{"enabled": enabled},
		})
	})
	if err == nil && !enabled {
		s.Kick()
	}
	return err
}

// Het nachtvenster voor vanzelf bijwerken, in de tijd van Brussel.
const (
	NightStart = 2
	NightEnd   = 5
)

func brussels() *time.Location {
	if l, err := time.LoadLocation("Europe/Brussels"); err == nil {
		return l
	}
	return time.Local
}

func inWindow(now time.Time) bool {
	h := now.In(brussels()).Hour()
	return h >= NightStart && h < NightEnd
}

// nextWindow is het begin van het volgende nachtvenster (of nu, erin).
func nextWindow(now time.Time) time.Time {
	t := now.In(brussels())
	if inWindow(now) {
		return now
	}
	start := time.Date(t.Year(), t.Month(), t.Day(), NightStart, 0, 0, 0, t.Location())
	if !start.After(t) {
		start = start.AddDate(0, 0, 1)
	}
	return start
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
