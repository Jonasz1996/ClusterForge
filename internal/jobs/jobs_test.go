package jobs

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/internal/store/storetest"
)

func newRunner(pool *pgxpool.Pool) *Runner {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	r := NewRunner(pool, events.NewWriter(store.New(pool), log), log)
	r.Poll, r.Heartbeat, r.StaleAfter = 50*time.Millisecond, 50*time.Millisecond, 400*time.Millisecond
	return r
}

// start laat de runner lopen tot de teruggegeven functie hem stopt.
func start(r *Runner) (stop func()) {
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); r.Run(ctx) }()
	return func() { cancel(); wg.Wait() }
}

func waitStatus(t *testing.T, q *store.Queries, id uuid.UUID, want store.JobStatus) store.Job {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		j, err := q.GetJob(context.Background(), id)
		if err == nil && j.Job.Status == want {
			return j.Job
		}
		if time.Now().After(deadline) {
			t.Fatalf("taak %s: status %s, want %s (err %v)", id, j.Job.Status, want, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func actions(t *testing.T, pool *pgxpool.Pool, id uuid.UUID) []string {
	t.Helper()
	rows, err := pool.Query(context.Background(), "SELECT action FROM events WHERE subject_id = $1 ORDER BY id", id.String())
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for rows.Next() {
		var a string
		_ = rows.Scan(&a)
		out = append(out, a)
	}
	return out
}

func TestRunSucceedsAndFails(t *testing.T) {
	ctx := context.Background()
	pool := storetest.DB(t)
	q := store.New(pool)
	r := newRunner(pool)
	r.Register("test.ok", func(ctx context.Context, j *Job) error {
		var p struct{ Name string }
		if err := j.Decode(&p); err != nil {
			return err
		}
		return j.Step(ctx, "groeten", func(ctx context.Context, s *Step) error {
			s.Logf("hallo %s", p.Name)
			return nil
		})
	})
	r.Register("test.fail", func(ctx context.Context, j *Job) error {
		return j.Step(ctx, "mislukken", func(ctx context.Context, s *Step) error { return errors.New("kapot") })
	})
	var finished atomic.Int32
	r.Finished = func(store.Job) { finished.Add(1) }
	stop := start(r)
	defer stop()

	okJob, err := r.Enqueue(ctx, Spec{Kind: "test.ok", Title: "Groet", Params: map[string]string{"Name": "web01"}, Actor: events.System()})
	if err != nil {
		t.Fatal(err)
	}
	failJob, _ := r.Enqueue(ctx, Spec{Kind: "test.fail", Title: "Faal", Actor: events.System()})
	unknown, _ := r.Enqueue(ctx, Spec{Kind: "test.onbekend", Title: "?", Actor: events.System()})

	waitStatus(t, q, okJob.ID, store.JobStatusSucceeded)
	steps, _ := q.ListJobSteps(ctx, okJob.ID)
	if len(steps) != 1 || steps[0].Status != store.JobStatusSucceeded || len(steps[0].Log) != 1 || steps[0].Log[0] != "hallo web01" {
		t.Errorf("steps = %+v", steps)
	}
	if got := actions(t, pool, okJob.ID); len(got) != 2 || got[0] != "job.queued" || got[1] != "job.succeeded" {
		t.Errorf("events = %v", got)
	}

	j := waitStatus(t, q, failJob.ID, store.JobStatusFailed)
	if j.Error != "kapot" {
		t.Errorf("error = %q", j.Error)
	}
	steps, _ = q.ListJobSteps(ctx, failJob.ID)
	if len(steps) != 1 || steps[0].Status != store.JobStatusFailed || steps[0].Error != "kapot" {
		t.Errorf("steps = %+v", steps)
	}
	if j := waitStatus(t, q, unknown.ID, store.JobStatusFailed); j.Error == "" {
		t.Error("onbekende soort zonder fout")
	}
	if finished.Load() != 3 {
		t.Errorf("Finished %d keer", finished.Load())
	}
}

func TestCancel(t *testing.T) {
	ctx := context.Background()
	pool := storetest.DB(t)
	q := store.New(pool)
	r := newRunner(pool)
	started := make(chan struct{})
	r.Register("test.wait", func(ctx context.Context, j *Job) error {
		return j.Step(ctx, "wachten", func(ctx context.Context, s *Step) error {
			close(started)
			<-ctx.Done()
			return context.Cause(ctx)
		})
	})

	// Een wachtende taak stopt meteen.
	queued, _ := r.Enqueue(ctx, Spec{Kind: "test.wait", Title: "Wacht", Actor: events.System()})
	if _, err := r.Cancel(ctx, queued.ID, events.System()); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, q, queued.ID, store.JobStatusCanceled)
	if _, err := r.Cancel(ctx, queued.ID, events.System()); !errors.Is(err, ErrFinished) {
		t.Errorf("tweede keer annuleren: %v", err)
	}
	if _, err := r.Cancel(ctx, uuid.New(), events.System()); !errors.Is(err, ErrNotFound) {
		t.Errorf("onbekende taak: %v", err)
	}

	stop := start(r)
	defer stop()
	running, _ := r.Enqueue(ctx, Spec{Kind: "test.wait", Title: "Wacht", Actor: events.System()})
	<-started
	if _, err := r.Cancel(ctx, running.ID, events.System()); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, q, running.ID, store.JobStatusCanceled)
	steps, _ := q.ListJobSteps(ctx, running.ID)
	if len(steps) != 1 || steps[0].Status != store.JobStatusCanceled {
		t.Errorf("steps = %+v", steps)
	}
	if got := actions(t, pool, running.ID); len(got) != 3 || got[1] != "job.cancel_requested" || got[2] != "job.canceled" {
		t.Errorf("events = %v", got)
	}
}

// Een taak die onderbroken wordt doordat de server stopt, gaat verder waar
// hij was: geslaagde stappen worden overgeslagen en de state blijft.
func TestResumeAfterShutdown(t *testing.T) {
	ctx := context.Background()
	pool := storetest.DB(t)
	q := store.New(pool)
	var firstRuns atomic.Int32
	blocked := make(chan struct{}, 1)
	handler := func(ctx context.Context, j *Job) error {
		if err := j.Step(ctx, "eerste", func(ctx context.Context, s *Step) error {
			firstRuns.Add(1)
			return nil
		}); err != nil {
			return err
		}
		return j.Step(ctx, "tweede", func(ctx context.Context, s *Step) error {
			var st struct{ UPID string }
			if s.State(&st) {
				s.Logf("hervat met %s", st.UPID)
				return nil
			}
			if err := s.SetState(ctx, struct{ UPID string }{"UPID:pve1:1"}); err != nil {
				return err
			}
			blocked <- struct{}{}
			<-ctx.Done()
			return ctx.Err()
		})
	}

	r1 := newRunner(pool)
	r1.Register("test.resume", handler)
	stop1 := start(r1)
	j, _ := r1.Enqueue(ctx, Spec{Kind: "test.resume", Title: "Hervat", Actor: events.System()})
	<-blocked
	stop1()
	waitStatus(t, q, j.ID, store.JobStatusQueued)

	r2 := newRunner(pool)
	r2.Register("test.resume", handler)
	stop2 := start(r2)
	defer stop2()
	done := waitStatus(t, q, j.ID, store.JobStatusSucceeded)
	if done.Attempts != 2 || firstRuns.Load() != 1 {
		t.Errorf("attempts %d, eerste stap %d keer", done.Attempts, firstRuns.Load())
	}
	steps, _ := q.ListJobSteps(ctx, j.ID)
	if len(steps) != 2 || steps[1].Log[len(steps[1].Log)-1] != "hervat met UPID:pve1:1" {
		t.Errorf("steps = %+v", steps)
	}
}

// Een taak van een gecrashte server (heartbeat staat stil) wordt hervat.
func TestRequeueStale(t *testing.T) {
	ctx := context.Background()
	pool := storetest.DB(t)
	q := store.New(pool)
	j, err := q.CreateJob(ctx, store.CreateJobParams{Kind: "test.ok", Title: "Oud", Params: []byte("{}")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "UPDATE jobs SET status = 'running', attempts = 1, heartbeat_at = now() - interval '1 hour' WHERE id = $1", j.ID); err != nil {
		t.Fatal(err)
	}
	r := newRunner(pool)
	r.Register("test.ok", func(ctx context.Context, j *Job) error { return nil })
	stop := start(r)
	defer stop()
	if done := waitStatus(t, q, j.ID, store.JobStatusSucceeded); done.Attempts != 2 {
		t.Errorf("attempts = %d", done.Attempts)
	}
}
