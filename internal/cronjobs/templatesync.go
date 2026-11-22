package cronjobs

// Syncs die een template vraagt (fase 11). Na een gelukte uitrol of
// wijziging van een cluster zet ClusterForge elke sync van zijn template als
// beheerde job van de soort sync, vanaf de node met het VIP: een nieuwe job,
// of de job met dezelfde naam aangepast. Zette de beheerder die job uit, dan
// blijft hij uit; zijn herhalingen blijven ook.

import (
	"context"
	"slices"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// TemplateSync is een sync die de template van een cluster vraagt, met de
// parameters ingevuld.
type TemplateSync struct {
	Name           string
	Schedule       string
	Source         string
	Target         string
	Before         string
	Delete         bool
	Exclude        []string
	Alert          bool
	TimeoutSeconds int
}

// EnsureTemplateSyncs zet de syncs van een template in het cluster. Wat niet
// lukt, komt als cluster.sync_not_added in het logboek; de rest gaat door.
func (s *Service) EnsureTemplateSyncs(ctx context.Context, clusterID uuid.UUID, syncs []TemplateSync) {
	if len(syncs) == 0 {
		return
	}
	c, err := s.q.GetCluster(ctx, clusterID)
	if err != nil {
		s.log.Error("cluster lezen voor de syncs van zijn template mislukt", "cluster", clusterID, "err", err)
		return
	}
	jobs, err := s.q.ListCronJobs(ctx)
	if err != nil {
		s.log.Error("cronjobs lezen mislukt", "err", err)
		return
	}
	actor := events.System()
	for _, ts := range syncs {
		in := JobInput{
			Name: ts.Name, Schedule: ts.Schedule, RunOn: protocol.CronOnVIP, User: "root", TimeoutSeconds: ts.TimeoutSeconds,
			Enabled: true, Alert: ts.Alert, Kind: protocol.CronKindSync,
			Sync: &SyncInput{Source: ts.Source, Target: ts.Target, Delete: ts.Delete, Exclude: ts.Exclude, Before: ts.Before},
		}
		i := slices.IndexFunc(jobs, func(j store.ListCronJobsRow) bool { return j.ClusterID == clusterID && j.Name == ts.Name })
		if i < 0 {
			_, err = s.CreateJob(ctx, actor, nil, clusterID, c.Name, in)
		} else {
			old := jobs[i]
			in.Enabled, in.Retries = old.Enabled, int(old.Retries)
			if old.Kind == protocol.CronKindSync && sameTemplateSync(old, in) {
				continue
			}
			if old.Kind != protocol.CronKindSync {
				err = &InvalidError{"name", "er is al een cronjob " + ts.Name + " die geen sync is; geef hem een andere naam"}
			} else {
				_, err = s.UpdateJob(ctx, actor, old.ID, in)
			}
		}
		if err == nil {
			continue
		}
		s.log.Warn("sync van de template niet gezet", "cluster", c.Name, "sync", ts.Name, "err", err)
		if werr := s.ev.Write(ctx, s.q, events.Event{
			Actor: actor, SubjectType: "cluster", SubjectID: c.ID.String(), ClusterID: &c.ID, Action: "cluster.sync_not_added",
			Payload: map[string]any{"name": c.Name, "sync": ts.Name, "reason": err.Error()},
		}); werr != nil {
			s.log.Error("event schrijven mislukt", "action", "cluster.sync_not_added", "err", werr)
		}
	}
}

// sameTemplateSync zegt of job j al is wat de template vraagt.
func sameTemplateSync(j store.ListCronJobsRow, in JobInput) bool {
	sy := in.Sync
	target := sy.Target
	if target == "" {
		target = sy.Source
	}
	timeout := in.TimeoutSeconds
	if timeout == 0 {
		timeout = defaultTimeout
	}
	ex := sy.Exclude
	if ex == nil {
		ex = []string{}
	}
	return j.Schedule == in.Schedule && j.RunOn == in.RunOn && j.SyncSource == sy.Source && j.SyncTarget == target &&
		j.SyncBefore == sy.Before && j.SyncDelete == sy.Delete && slices.Equal(j.SyncExclude, ex) && j.Alert == in.Alert &&
		int(j.TimeoutSeconds) == timeout && j.SyncToCluster == nil
}
