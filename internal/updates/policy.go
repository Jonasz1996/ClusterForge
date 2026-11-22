package updates

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/jobs"
	"github.com/Jonasz1996/clusterforge/internal/planner"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

// Automatisch bijwerken (fase 15): een beleid per omgeving, en per cluster
// zoals de omgeving, een eigen beleid of uit. De planner start in het
// venster één taak updates.run per cluster.

// Policy is wat er vanzelf bijgewerkt wordt en wanneer.
type Policy struct {
	Enabled       bool   `json:"enabled"`
	Apt           bool   `json:"apt"`
	AptFull       bool   `json:"apt_full"`
	AptAutoremove bool   `json:"apt_autoremove"`
	Docker        bool   `json:"docker"`
	DockerPrune   bool   `json:"docker_prune"`
	Reboot        bool   `json:"reboot"`
	Window        string `json:"window"`
}

// DefaultPolicy staat uit; aangezet werkt ze apt (full-upgrade) en Docker
// bij op zondag van 03:00 tot 05:00.
var DefaultPolicy = Policy{Apt: true, AptFull: true, Docker: true, Window: "zo 03:00-05:00"}

// Validate controleert het beleid.
func (p Policy) Validate() error {
	if _, err := planner.ParseWindow(p.Window); err != nil {
		return &FieldError{"venster: " + err.Error()}
	}
	if p.Enabled && !p.Apt && !p.Docker {
		return &FieldError{"kies pakketten (apt), Docker of beide"}
	}
	return nil
}

// Modes van een cluster.
const (
	ModeInherit = "inherit"
	ModeOwn     = "own"
	ModeOff     = "off"
)

// ClusterPolicy is het beleid van één cluster.
type ClusterPolicy struct {
	ClusterID uuid.UUID
	Name      string
	Slug      string
	Env       string
	Mode      string
	// Own is het eigen beleid (Mode own).
	Own *Policy
	// Effective is wat telt.
	Effective Policy
	LastRun   *store.UpdateAutoRun
	// NextStart is het begin van het volgende venster als het beleid aan
	// staat.
	NextStart *time.Time
}

// Policies geeft het beleid per omgeving en per cluster.
type Policies struct {
	Envs     map[string]Policy
	EnvBy    map[string]*string
	EnvAt    map[string]time.Time
	Clusters []ClusterPolicy
}

func fromRow(r store.ListUpdatePoliciesRow) Policy {
	return Policy{
		Enabled: r.Enabled, Apt: r.Apt, AptFull: r.AptFull, AptAutoremove: r.AptAutoremove,
		Docker: r.Docker, DockerPrune: r.DockerPrune, Reboot: r.Reboot, Window: r.UpdateWindow,
	}
}

// Envs zijn de omgevingen.
var Envs = []string{string(store.EnvironmentLab), string(store.EnvironmentTest), string(store.EnvironmentProd)}

// Policies leest het beleid.
func (s *Service) Policies(ctx context.Context) (Policies, error) {
	rows, err := s.q.ListUpdatePolicies(ctx)
	if err != nil {
		return Policies{}, err
	}
	out := Policies{Envs: map[string]Policy{}, EnvBy: map[string]*string{}, EnvAt: map[string]time.Time{}}
	for _, e := range Envs {
		out.Envs[e] = DefaultPolicy
	}
	own := map[uuid.UUID]Policy{}
	for _, r := range rows {
		switch {
		case r.Environment.Valid:
			e := string(r.Environment.Environment)
			out.Envs[e], out.EnvBy[e], out.EnvAt[e] = fromRow(r), r.UpdatedByName, r.UpdatedAt
		case r.ClusterID != nil:
			own[*r.ClusterID] = fromRow(r)
		}
	}
	runs, err := s.q.ListUpdateAutoRuns(ctx)
	if err != nil {
		return Policies{}, err
	}
	last := map[uuid.UUID]store.UpdateAutoRun{}
	for _, r := range runs {
		last[r.ClusterID] = r
	}
	cls, err := s.q.ListClusters(ctx)
	if err != nil {
		return Policies{}, err
	}
	now := s.Now()
	for _, c := range cls {
		cp := ClusterPolicy{ClusterID: c.Cluster.ID, Name: c.Cluster.Name, Slug: c.Cluster.Slug, Env: string(c.Cluster.Environment), Mode: ModeInherit}
		cp.Effective = out.Envs[cp.Env]
		if p, ok := own[c.Cluster.ID]; ok {
			cp.Own = &p
			cp.Mode = ModeOwn
			if !p.Enabled {
				cp.Mode = ModeOff
			}
			cp.Effective = p
		}
		if r, ok := last[c.Cluster.ID]; ok {
			cp.LastRun = &r
		}
		if cp.Effective.Enabled {
			if w, err := planner.ParseWindow(cp.Effective.Window); err == nil {
				start, _ := w.Next(now)
				if !start.IsZero() {
					cp.NextStart = &start
				}
			}
		}
		out.Clusters = append(out.Clusters, cp)
	}
	return out, nil
}

// SetEnvPolicy bewaart het beleid van een omgeving.
func (s *Service) SetEnvPolicy(ctx context.Context, actor events.Actor, userID *uuid.UUID, env string, p Policy) error {
	if err := p.Validate(); err != nil {
		return err
	}
	e := store.Environment(env)
	if !slices.Contains(Envs, env) {
		return &FieldError{"onbekende omgeving " + env}
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		if _, err := q.UpsertEnvUpdatePolicy(ctx, store.UpsertEnvUpdatePolicyParams{
			Environment: store.NullEnvironment{Environment: e, Valid: true}, Enabled: p.Enabled, Apt: p.Apt, AptFull: p.AptFull,
			AptAutoremove: p.AptAutoremove, Docker: p.Docker, DockerPrune: p.DockerPrune, Reboot: p.Reboot,
			UpdateWindow: p.Window, UpdatedBy: userID,
		}); err != nil {
			return err
		}
		if s.ev == nil {
			return nil
		}
		return s.ev.Write(ctx, q, events.Event{
			Actor: actor, SubjectType: "settings", SubjectID: "updates-" + env, Action: "settings.update_policy",
			Payload: map[string]any{"environment": env, "policy": p},
		})
	})
}

// SetClusterPolicy zet een cluster op de omgeving (inherit), een eigen
// beleid (own) of uit (off).
func (s *Service) SetClusterPolicy(ctx context.Context, actor events.Actor, userID *uuid.UUID, clusterID uuid.UUID, mode string, p Policy) error {
	switch mode {
	case ModeInherit:
	case ModeOff:
		p.Enabled = false
		if p.Window == "" {
			p.Window = DefaultPolicy.Window
		}
	case ModeOwn:
		p.Enabled = true
		if err := p.Validate(); err != nil {
			return err
		}
	default:
		return &FieldError{"kies inherit, own of off"}
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		c, err := q.GetCluster(ctx, clusterID)
		if err != nil {
			return err
		}
		if mode == ModeInherit {
			err = q.DeleteClusterUpdatePolicy(ctx, &clusterID)
		} else {
			_, err = q.UpsertClusterUpdatePolicy(ctx, store.UpsertClusterUpdatePolicyParams{
				ClusterID: &clusterID, Enabled: p.Enabled, Apt: p.Apt, AptFull: p.AptFull, AptAutoremove: p.AptAutoremove,
				Docker: p.Docker, DockerPrune: p.DockerPrune, Reboot: p.Reboot, UpdateWindow: p.Window, UpdatedBy: userID,
			})
		}
		if err != nil || s.ev == nil {
			return err
		}
		return s.ev.Write(ctx, q, events.Event{
			Actor: actor, SubjectType: "cluster", SubjectID: clusterID.String(), ClusterID: &clusterID,
			Action: "cluster.update_policy", Payload: map[string]any{"name": c.Name, "mode": mode, "policy": p},
		})
	})
}

// AutoTick start de automatische updates die nu aan de beurt zijn. De
// planner roept het elke minuut aan.
func (s *Service) AutoTick(ctx context.Context, now time.Time) error {
	pol, err := s.Policies(ctx)
	if err != nil {
		return err
	}
	var nodes []Node
	var errs []error
	for _, cp := range pol.Clusters {
		p := cp.Effective
		if !p.Enabled {
			continue
		}
		w, err := planner.ParseWindow(p.Window)
		if err != nil {
			continue
		}
		start, end := w.Next(now)
		if start.IsZero() || now.Before(start) || !now.Before(end) {
			continue
		}
		if cp.LastRun != nil && !cp.LastRun.StartedAt.Before(start) {
			continue
		}
		if nodes == nil {
			if nodes, err = s.Overview(ctx); err != nil {
				return err
			}
		}
		if err := s.autoCluster(ctx, now, cp, nodes); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", cp.Name, err))
		}
	}
	return errors.Join(errs...)
}

func (s *Service) autoCluster(ctx context.Context, now time.Time, cp ClusterPolicy, all []Node) error {
	p := cp.Effective
	var nodes []Node
	var problems []string
	aptWork, dockerWork := false, false
	for _, n := range all {
		if n.ClusterID == nil || *n.ClusterID != cp.ClusterID {
			continue
		}
		nodes = append(nodes, n)
		switch {
		case !n.Online:
			problems = append(problems, n.Hostname+" is niet verbonden")
		case !n.Smart:
			problems = append(problems, fmt.Sprintf("de agent op %s heeft protocol %d", n.Hostname, n.Protocol))
		}
		if n.Upgrades == nil || n.Upgrades.Total > 0 {
			aptWork = true
		}
		if n.Docker != nil && len(n.Docker.Projects) > 0 {
			dockerWork = true
		}
	}
	record := func(job *uuid.UUID, note string) error {
		return s.q.SetUpdateAutoRun(ctx, store.SetUpdateAutoRunParams{ClusterID: cp.ClusterID, StartedAt: now, JobID: job, Note: note})
	}
	if len(nodes) == 0 {
		return nil
	}
	if len(problems) > 0 {
		note := "overgeslagen: " + strings.Join(problems, "; ")
		if s.ev != nil {
			id := cp.ClusterID
			_ = s.ev.Write(ctx, nil, events.Event{
				Actor: events.System(), SubjectType: "cluster", SubjectID: id.String(), ClusterID: &id,
				Action: "cluster.auto_update_skipped", Payload: map[string]any{"name": cp.Name, "reason": note},
			})
		}
		return record(nil, note)
	}
	req := Request{ClusterID: &cp.ClusterID, Reboot: p.Reboot, Auto: true}
	if p.Apt && aptWork {
		req.Apt = &Apt{Full: p.AptFull, Autoremove: p.AptAutoremove}
	}
	if p.Docker && dockerWork {
		req.Docker = &Docker{Prune: p.DockerPrune}
	}
	if req.Apt == nil && req.Docker == nil {
		return record(nil, "niets te doen")
	}
	ordered, err := s.Select(ctx, req)
	if err != nil {
		var ce *ConflictError
		if errors.As(err, &ce) {
			return record(nil, "overgeslagen: "+ce.Msg)
		}
		return err
	}
	started, _, err := s.Start(ctx, events.System(), req, ordered)
	if err != nil {
		var ce *ConflictError
		var busy jobs.BusyError
		if errors.As(err, &ce) || errors.As(err, &busy) {
			// Er loopt al een taak op het cluster; de volgende minuut opnieuw.
			return nil
		}
		return err
	}
	if len(started) == 0 {
		return nil
	}
	slog.Default().Info("automatisch bijwerken gestart", "cluster", cp.Name, "job", started[0].ID)
	return record(&started[0].ID, "")
}
