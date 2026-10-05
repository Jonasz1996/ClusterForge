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

// actionsAfter wacht tot er n events zijn: het event van een afgeronde taak
// komt net na zijn status.
func actionsAfter(t *testing.T, pool *pgxpool.Pool, id uuid.UUID, n int) []string {
	t.Helper()
	var got []string
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if got = actions(t, pool, id); len(got) >= n {
			break
		}
	}
	return got
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
		// Een event uit de handler hoort bij de taak.
		if err := r.ev.Write(ctx, nil, events.Event{
			Actor: events.System(), SubjectType: "node", SubjectID: "n1", Action: "node.updated",
		}); err != nil {
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
	statuses := sync.Map{}
	for _, kind := range []string{"test.ok", "test.fail", "test.onbekend"} {
		r.OnFinished(kind, func(_ context.Context, j store.Job) {
			statuses.Store(j.ID, j.Status)
			finished.Add(1)
		})
	}
	// Een tweede abonnee op dezelfde soort hoort het ook, en een panic in
	// de eerste houdt de tweede niet tegen.
	var second atomic.Int32
	r.OnFinished("test.ok", func(context.Context, store.Job) { panic("stuk") })
	r.OnFinished("test.ok", func(context.Context, store.Job) { second.Add(1) })
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
	if got := actionsAfter(t, pool, okJob.ID, 2); len(got) != 2 || got[0] != "job.queued" || got[1] != "job.succeeded" {
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
	// Finished komt na de status in de database; wacht er even op.
	for deadline := time.Now().Add(2 * time.Second); finished.Load() < 3 && time.Now().Before(deadline); {
		time.Sleep(10 * time.Millisecond)
	}
	if finished.Load() != 3 || second.Load() != 1 {
		t.Errorf("OnFinished %d keer, tweede abonnee %d keer", finished.Load(), second.Load())
	}
	for id, want := range map[uuid.UUID]store.JobStatus{okJob.ID: store.JobStatusSucceeded, failJob.ID: store.JobStatusFailed, unknown.ID: store.JobStatusFailed} {
		if got, _ := statuses.Load(id); got != want {
			t.Errorf("OnFinished kreeg status %v, wil %s", got, want)
		}
	}
	var jobRef string
	if err := pool.QueryRow(ctx, "SELECT payload->'origin'->>'job_id' FROM events WHERE action = 'node.updated'").Scan(&jobRef); err != nil || jobRef != okJob.ID.String() {
		t.Errorf("event uit de handler draagt job_id %q (%v)", jobRef, err)
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

	var finished atomic.Int32
	r.OnFinished("test.wait", func(_ context.Context, j store.Job) {
		if j.Status == store.JobStatusCanceled {
			finished.Add(1)
		}
	})

	// Een wachtende taak stopt meteen, en zijn module hoort het, ook al
	// draaide de handler nooit.
	queued, _ := r.Enqueue(ctx, Spec{Kind: "test.wait", Title: "Wacht", Actor: events.System()})
	if _, err := r.Cancel(ctx, queued.ID, events.System()); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, q, queued.ID, store.JobStatusCanceled)
	if finished.Load() != 1 {
		t.Errorf("OnFinished na annuleren in de wachtrij: %d", finished.Load())
	}
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
	if got := actionsAfter(t, pool, running.ID, 3); len(got) != 3 || got[1] != "job.cancel_requested" || got[2] != "job.canceled" {
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

func TestClusterSlot(t *testing.T) {
	ctx := context.Background()
	pool := storetest.DB(t)
	q := store.New(pool)
	r := newRunner(pool)
	cluster := func(slug string) uuid.UUID {
		c, err := q.CreateCluster(ctx, store.CreateClusterParams{Slug: slug, Name: "Cluster " + slug, Type: "keepalived", Environment: store.EnvironmentLab, Tags: []string{}})
		if err != nil {
			t.Fatal(err)
		}
		return c.ID
	}
	web, db := cluster("web"), cluster("db")
	node, err := q.CreateNode(ctx, store.CreateNodeParams{ClusterID: &web, Hostname: "web01", Lifecycle: store.NodeLifecycleActive, Tags: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	spec := func(title string, c *uuid.UUID, n *uuid.UUID) Spec {
		return Spec{Kind: "test.wait", Title: title, ClusterID: c, NodeID: n, Actor: events.System()}
	}

	// Een taak op een node van het cluster houdt het slot.
	first, err := r.EnqueueForCluster(ctx, spec("Herstarten: web01", &web, &node.ID))
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.EnqueueForCluster(ctx, spec("VM stoppen", &web, nil))
	var busy BusyError
	if !errors.As(err, &busy) || busy.JobID != first.ID || busy.Title != "Herstarten: web01" || busy.Cluster != "Cluster web" {
		t.Fatalf("tweede schrijvende taak: %v", err)
	}
	if busy.Error() != "in cluster Cluster web loopt al een taak (Herstarten: web01); wacht tot die klaar is" {
		t.Errorf("melding: %s", busy.Error())
	}
	// Lezen telt niet mee, en een ander cluster ook niet.
	if _, err := r.Enqueue(ctx, spec("Facts verversen", &web, &node.ID)); err != nil {
		t.Fatal(err)
	}
	if _, err := r.EnqueueForCluster(ctx, spec("Uitrollen", &db, nil)); err != nil {
		t.Fatal(err)
	}
	// Na afloop is het slot vrij.
	if _, err := r.Cancel(ctx, first.ID, events.System()); err != nil {
		t.Fatal(err)
	}
	stop, err := r.EnqueueForCluster(ctx, spec("VM stoppen", &web, nil))
	if err != nil {
		t.Fatalf("na annuleren: %v", err)
	}
	// Opnieuw proberen neemt het slot ook.
	r.Retryable("test.wait")
	if _, err := r.Retry(ctx, first.ID, events.System()); !errors.As(err, &busy) || busy.JobID != stop.ID {
		t.Fatalf("opnieuw terwijl een ander het slot heeft: %v", err)
	}
	if _, err := r.Cancel(ctx, stop.ID, events.System()); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Retry(ctx, first.ID, events.System()); err != nil {
		t.Fatalf("opnieuw: %v", err)
	}
	if _, err := r.EnqueueForCluster(ctx, spec("VM stoppen", &web, nil)); !errors.As(err, &busy) || busy.JobID != first.ID {
		t.Fatalf("na opnieuw: %v", err)
	}

	// Twee gelijktijdige aanvragen: precies één krijgt het slot.
	other := cluster("app")
	var ok, refused atomic.Int32
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := r.EnqueueForCluster(ctx, spec("Tegelijk", &other, nil))
			switch {
			case err == nil:
				ok.Add(1)
			case errors.As(err, &BusyError{}):
				refused.Add(1)
			default:
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if ok.Load() != 1 || refused.Load() != 7 {
		t.Fatalf("%d kregen het slot, %d geweigerd", ok.Load(), refused.Load())
	}
}
