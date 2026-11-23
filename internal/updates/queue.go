package updates

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/jobs"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

// "Alles bijwerken" (fase 47): één taak die de clusters na elkaar bijwerkt,
// elk met een gewone clusterupdate (rollend, VIP-houder als laatste). Het
// volgende cluster begint pas als het vorige gelukt is; bij een fout stopt
// de rij, zoals een clusterupdate stopt bij de eerste node die faalt.
// Proxmox-hosts doen niet mee: die hebben hun eigen Host-updates.

const QueueKind = "updates.queue"

// QueuePoll is hoe vaak de rij naar de taak van het huidige cluster kijkt.
var QueuePoll = 5 * time.Second

// QueueGroup is één cluster in de rij, of de nodes zonder cluster.
type QueueGroup struct {
	ClusterID *uuid.UUID  `json:"cluster_id,omitempty"`
	Name      string      `json:"name"`
	NodeIDs   []uuid.UUID `json:"node_ids,omitempty"`
}

type queueParams struct {
	Groups     []QueueGroup `json:"groups"`
	Apt        *Apt         `json:"apt,omitempty"`
	Docker     *Docker      `json:"docker,omitempty"`
	Reboot     bool         `json:"reboot,omitempty"`
	NoFailover bool         `json:"no_failover,omitempty"`
}

type queueState struct {
	JobID   *uuid.UUID `json:"job_id,omitempty"`
	Skipped string     `json:"skipped,omitempty"`
}

// QueuePlan is wat "Alles bijwerken" zou doen.
type QueuePlan struct {
	Groups []QueueGroup
	// Prod zijn de slugs van de prodclusters in de rij.
	Prod []string
	// Hosts zijn de Proxmox-hosts die niet meedoen.
	Hosts []string
	// Skipped zijn clusters die niet meedoen, met de reden.
	Skipped []string
}

// PlanAll kiest de clusters: lab en test eerst, prod als laatste (alleen
// met includeProd), daarna de nodes zonder cluster. Proxmox-hosts en
// clusters met een node die offline is of een te oude agent heeft, doen niet
// mee.
func (s *Service) PlanAll(ctx context.Context, includeProd bool) (QueuePlan, error) {
	all, err := s.Overview(ctx)
	if err != nil {
		return QueuePlan{}, err
	}
	var plan QueuePlan
	type grp struct {
		g    QueueGroup
		env  string
		slug string
		bad  string
	}
	var groups []*grp
	byKey := map[uuid.UUID]*grp{}
	for _, n := range all {
		if n.PVEHost {
			plan.Hosts = append(plan.Hosts, n.Hostname)
			continue
		}
		key := uuid.Nil
		if n.ClusterID != nil {
			key = *n.ClusterID
		}
		g := byKey[key]
		if g == nil {
			g = &grp{g: QueueGroup{ClusterID: n.ClusterID, Name: n.ClusterName}, env: n.ClusterEnv, slug: n.ClusterSlug}
			if n.ClusterID == nil {
				g.g.Name = "nodes zonder cluster"
			}
			byKey[key] = g
			groups = append(groups, g)
		}
		switch {
		case !n.Online:
			if n.ClusterID != nil {
				g.bad = n.Hostname + " is offline"
			}
			continue
		case !n.Supported:
			if n.ClusterID != nil {
				g.bad = n.Hostname + " heeft een te oude agent"
			}
			continue
		}
		if n.ClusterID == nil {
			g.g.NodeIDs = append(g.g.NodeIDs, n.NodeID)
		}
	}
	rank := func(g *grp) int {
		switch {
		case g.g.ClusterID == nil:
			return 3
		case g.env == string(store.EnvironmentProd):
			return 2
		}
		return 1
	}
	slices.SortStableFunc(groups, func(a, b *grp) int { return rank(a) - rank(b) })
	for _, g := range groups {
		switch {
		case g.bad != "":
			plan.Skipped = append(plan.Skipped, fmt.Sprintf("%s: %s", g.g.Name, g.bad))
		case g.env == string(store.EnvironmentProd) && !includeProd:
			plan.Skipped = append(plan.Skipped, g.g.Name+": prod (vink prod aan om mee te nemen)")
		case g.g.ClusterID == nil && len(g.g.NodeIDs) == 0:
		default:
			if g.env == string(store.EnvironmentProd) {
				plan.Prod = append(plan.Prod, g.slug)
			}
			plan.Groups = append(plan.Groups, g.g)
		}
	}
	return plan, nil
}

// StartAll zet de rij in de wachtrij.
func (s *Service) StartAll(ctx context.Context, actor events.Actor, req Request, plan QueuePlan) (store.Job, error) {
	if req.Apt == nil && req.Docker == nil {
		return store.Job{}, &FieldError{"kies pakketten (apt), Docker of beide"}
	}
	if len(plan.Groups) == 0 {
		return store.Job{}, &ConflictError{"er is niets om bij te werken"}
	}
	p := queueParams{Groups: plan.Groups, Apt: req.Apt, Docker: req.Docker, Reboot: req.Reboot, NoFailover: req.NoFailover}
	spec := jobs.Spec{Kind: QueueKind, Params: p, Actor: actor,
		Title: fmt.Sprintf("Alles bijwerken: %s, één na één", plural(len(plan.Groups), "cluster", "clusters"))}
	j, err := s.runner.Enqueue(ctx, spec)
	if err != nil {
		return store.Job{}, err
	}
	s.runner.Kick()
	return j, nil
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

func (s *Service) runQueue(ctx context.Context, j *jobs.Job) error {
	var p queueParams
	if err := j.Decode(&p); err != nil {
		return err
	}
	actor := s.actor(j)
	var done, skipped []string
	for i, g := range p.Groups {
		var qs queueState
		ran := false
		err := j.Step(ctx, g.Name, func(ctx context.Context, st *jobs.Step) error {
			ran = true
			st.State(&qs)
			if qs.JobID == nil {
				req := Request{ClusterID: g.ClusterID, NodeIDs: g.NodeIDs, Apt: p.Apt, Docker: p.Docker, Reboot: p.Reboot, NoFailover: p.NoFailover}
				nodes, err := s.Select(ctx, req)
				if err != nil {
					// Een cluster dat nu niet kan (een node ging offline), slaat
					// de rij over; de rest gaat door.
					st.Logf("overgeslagen: %v", err)
					qs.Skipped = err.Error()
					return st.SetState(ctx, qs)
				}
				id, err := s.startChild(ctx, st, actor, req, nodes)
				if err != nil {
					return err
				}
				qs.JobID = &id
				if err := st.SetState(ctx, qs); err != nil {
					return err
				}
				st.Logf("taak gestart; de uitvoer per node staat in die taak")
			}
			return s.waitChild(ctx, st, *qs.JobID)
		})
		if err != nil {
			if ctx.Err() != nil {
				return err
			}
			rest := len(p.Groups) - i - 1
			msg := fmt.Sprintf("%s: %v", g.Name, err)
			if rest > 0 {
				msg += fmt.Sprintf("; de %s erna %s niet bijgewerkt", plural(rest, "cluster", "clusters")[2:], map[bool]string{true: "is", false: "zijn"}[rest == 1])
			}
			return errors.New(msg)
		}
		if !ran {
			j.Done(g.Name, &qs)
		}
		if qs.Skipped != "" {
			skipped = append(skipped, g.Name)
		} else {
			done = append(done, g.Name)
		}
	}
	if len(skipped) > 0 {
		return fmt.Errorf("bijgewerkt: %d; overgeslagen omdat ze nu niet konden: %s", len(done), strings.Join(skipped, ", "))
	}
	return nil
}

// startChild start de clusterupdate; loopt er in het cluster al een taak,
// dan wacht de rij tot die klaar is (hoogstens een uur).
func (s *Service) startChild(ctx context.Context, st *jobs.Step, actor events.Actor, req Request, nodes []Node) (uuid.UUID, error) {
	deadline := s.Now().Add(time.Hour)
	for {
		started, skipped, err := s.Start(ctx, actor, req, nodes)
		var ce *ConflictError
		switch {
		case err == nil && len(started) > 0:
			return started[0].ID, nil
		case err != nil && !errors.As(err, &ce):
			return uuid.Nil, err
		case s.Now().After(deadline):
			return uuid.Nil, fmt.Errorf("er loopt al een uur een andere taak: %s", strings.Join(skipped, "; "))
		}
		st.Logf("wacht: %s", strings.Join(skipped, "; "))
		_ = st.Flush(ctx)
		select {
		case <-ctx.Done():
			return uuid.Nil, context.Cause(ctx)
		case <-time.After(15 * QueuePoll):
		}
	}
}

// waitChild wacht tot de clusterupdate klaar is. Annuleer je de rij, dan
// ook de taak van het cluster dat bezig is.
func (s *Service) waitChild(ctx context.Context, st *jobs.Step, id uuid.UUID) error {
	for {
		row, err := s.q.GetJob(context.WithoutCancel(ctx), id)
		if err != nil {
			return err
		}
		switch row.Job.Status {
		case store.JobStatusSucceeded:
			st.Logf("gelukt: %s", row.Job.Title)
			return nil
		case store.JobStatusFailed:
			return fmt.Errorf("%s mislukte: %s", row.Job.Title, row.Job.Error)
		case store.JobStatusCanceled:
			return fmt.Errorf("%s is geannuleerd", row.Job.Title)
		}
		select {
		case <-ctx.Done():
			if errors.Is(context.Cause(ctx), jobs.ErrCanceled) {
				_, _ = s.runner.Cancel(context.WithoutCancel(ctx), id, events.System())
			}
			return context.Cause(ctx)
		case <-time.After(QueuePoll):
		}
	}
}
