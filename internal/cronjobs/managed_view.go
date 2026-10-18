package cronjobs

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Jonasz1996/clusterforge/internal/cronexpr"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// ManagedJob is een beheerde job met per node of hij er staat en de
// laatste run.
type ManagedJob struct {
	store.ListCronJobsRow
	Describe    string
	OnCalendar  string
	NextRun     *time.Time
	Failures24h int
	// MissedAt is het geplande moment waarop de job niet draaide, als dat
	// het laatste was.
	MissedAt *time.Time
	Nodes    []ManagedNode
}

// ManagedNode is een beheerde job op één node.
type ManagedNode struct {
	NodeID   uuid.UUID
	Hostname string
	// Applied: de agent heeft de laatste versie van de job.
	Applied bool
	HasVIP  bool
	Error   string
	LastRun *RunSummary
}

// RunSummary is een run zonder uitvoer.
type RunSummary struct {
	ID        uuid.UUID
	NodeID    uuid.UUID
	Hostname  string
	Reason    string
	Result    string
	StartedAt time.Time
	EndedAt   *time.Time
	Exit      *int
}

func intPtr(v *int32) *int {
	if v == nil {
		return nil
	}
	x := int(*v)
	return &x
}

// Jobs geeft de beheerde jobs.
func (s *Service) Jobs(ctx context.Context) ([]ManagedJob, error) {
	w, err := s.world(ctx)
	if err != nil {
		return nil, err
	}
	latest, err := s.q.ListLatestCronRuns(ctx)
	if err != nil {
		return nil, err
	}
	failures, err := s.q.CountCronFailures(ctx)
	if err != nil {
		return nil, err
	}
	fails := map[uuid.UUID]int{}
	for _, f := range failures {
		fails[f.JobID] = int(f.Failures)
	}
	type key struct{ job, node uuid.UUID }
	last := map[key]RunSummary{}
	for _, r := range latest {
		last[key{r.JobID, r.NodeID}] = RunSummary{
			ID: r.ID, NodeID: r.NodeID, Hostname: r.Hostname, Reason: r.Reason, Result: r.Result, StartedAt: r.StartedAt,
			EndedAt: r.EndedAt, Exit: intPtr(r.ExitCode),
		}
	}
	// De hash die elke node zou moeten hebben.
	wantHash := map[uuid.UUID]string{}
	for _, n := range w.nodes {
		wantHash[n.ID] = protocol.CronSetHash(w.desired(n))
	}
	now := s.Now().In(s.loc())
	since := now.Add(-8 * 24 * time.Hour)
	starts, err := s.q.ListCronJobRunStarts(ctx, since)
	if err != nil {
		return nil, err
	}
	lastStart := map[uuid.UUID]time.Time{}
	for _, st := range starts {
		lastStart[st.JobID] = st.LastStart
	}
	out := make([]ManagedJob, 0, len(w.jobs))
	for _, j := range w.jobs {
		mj := ManagedJob{ListCronJobsRow: j, Failures24h: fails[j.ID]}
		if sc, err := cronexpr.Parse(j.Schedule); err == nil {
			mj.Describe = sc.Describe()
			mj.OnCalendar, _ = sc.OnCalendar()
			if j.Enabled {
				if next := sc.Next(now); !next.IsZero() {
					mj.NextRun = &next
				}
				// Gemist: het laatste geplande moment sinds de laatste
				// wijziging is langer dan missedGrace voorbij, en er begon
				// sindsdien geen run.
				from := j.UpdatedAt.In(s.loc())
				if from.Before(since) {
					from = since
				}
				if due := lastScheduled(sc, from, now.Add(-missedGrace)); !due.IsZero() && lastStart[j.ID].Before(due.Add(-time.Minute)) {
					mj.MissedAt = &due
				}
			}
		}
		for _, n := range w.nodes {
			if !onNode(j, n) {
				continue
			}
			st := w.sets[n.ID]
			mn := ManagedNode{NodeID: n.ID, Hostname: n.Hostname, Applied: st.Hash != "" && st.Hash == wantHash[n.ID], Error: st.Error}
			mn.HasVIP = slices.ContainsFunc(w.vips[j.ClusterID], func(v store.ListCronVIPsRow) bool { return v.OwnerNodeID != nil && *v.OwnerNodeID == n.ID })
			if r, ok := last[key{j.ID, n.ID}]; ok {
				mn.LastRun = &r
			}
			mj.Nodes = append(mj.Nodes, mn)
		}
		slices.SortFunc(mj.Nodes, func(a, b ManagedNode) int {
			return strings.Compare(strings.ToLower(a.Hostname), strings.ToLower(b.Hostname))
		})
		out = append(out, mj)
	}
	return out, nil
}

// Job geeft één job met zijn laatste runs, nieuwste eerst.
func (s *Service) Job(ctx context.Context, id uuid.UUID, limit int) (ManagedJob, []RunSummary, error) {
	jobs, err := s.Jobs(ctx)
	if err != nil {
		return ManagedJob{}, nil, err
	}
	i := slices.IndexFunc(jobs, func(j ManagedJob) bool { return j.ID == id })
	if i < 0 {
		return ManagedJob{}, nil, ErrJobNotFound
	}
	rows, err := s.q.ListCronRunsOf(ctx, store.ListCronRunsOfParams{JobID: id, Limit: int32(limit)})
	if err != nil {
		return ManagedJob{}, nil, err
	}
	runs := make([]RunSummary, 0, len(rows))
	for _, r := range rows {
		runs = append(runs, RunSummary{
			ID: r.ID, NodeID: r.NodeID, Hostname: r.Hostname, Reason: r.Reason, Result: r.Result, StartedAt: r.StartedAt,
			EndedAt: r.EndedAt, Exit: intPtr(r.ExitCode),
		})
	}
	return jobs[i], runs, nil
}

// ErrRunNotFound: de run bestaat niet.
var ErrRunNotFound = errors.New("run niet gevonden")

// RunRecord geeft één run met zijn uitvoer.
func (s *Service) RunRecord(ctx context.Context, id uuid.UUID) (store.GetCronRunRow, error) {
	r, err := s.q.GetCronRun(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, ErrRunNotFound
	}
	return r, err
}
