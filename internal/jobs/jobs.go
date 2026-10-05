// Package jobs voert taken uit die langer duren dan een HTTP-request, zoals
// een VM starten of migreren. Taken staan in PostgreSQL; een taak bestaat uit
// stappen die na een herstart van de server hervat worden.
package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

var (
	// ErrCanceled is de oorzaak als een gebruiker een taak annuleert.
	ErrCanceled = errors.New("geannuleerd")
	// ErrNotFound betekent dat de taak niet bestaat.
	ErrNotFound = errors.New("taak niet gevonden")
	// ErrFinished betekent dat de taak al klaar is en niet meer te annuleren.
	ErrFinished = errors.New("taak is al klaar")

	// errShutdown is de oorzaak als de server stopt; de taak wordt dan later
	// hervat in plaats van als mislukt gemarkeerd.
	errShutdown = errors.New("server stopt")
)

// HandlerFunc voert een taak uit. Een fout maakt de taak mislukt; de context
// wordt geannuleerd met ErrCanceled als een gebruiker annuleert.
type HandlerFunc func(ctx context.Context, j *Job) error

// Spec beschrijft een nieuwe taak.
type Spec struct {
	Kind  string
	Title string
	// Params gaat als JSON naar de handler.
	Params    any
	ClusterID *uuid.UUID
	NodeID    *uuid.UUID
	ProxmoxID *uuid.UUID
	Actor     events.Actor
}

type Runner struct {
	pool     *pgxpool.Pool
	q        *store.Queries
	ev       *events.Writer
	log      *slog.Logger
	handlers map[string]HandlerFunc

	// Workers is het aantal taken dat tegelijk loopt.
	Workers int
	// Poll is hoe vaak een worker zonder seintje naar nieuwe taken kijkt.
	Poll time.Duration
	// Heartbeat is hoe vaak een lopende taak laat weten dat hij nog leeft en
	// kijkt of hij geannuleerd is.
	Heartbeat time.Duration
	// StaleAfter is hoe lang een heartbeat mag stilstaan voor de taak als
	// onderbroken geldt en opnieuw in de wachtrij komt.
	StaleAfter time.Duration
	// MaxAttempts is hoe vaak een taak onderbroken mag worden.
	MaxAttempts int32
	// Finished wordt aangeroepen als een taak klaar is. Mag nil zijn.
	Finished func(store.Job)

	kick    chan struct{}
	mu      sync.Mutex
	running map[uuid.UUID]context.CancelCauseFunc
}

func NewRunner(pool *pgxpool.Pool, ev *events.Writer, log *slog.Logger) *Runner {
	return &Runner{
		pool: pool, q: store.New(pool), ev: ev, log: log, handlers: map[string]HandlerFunc{},
		Workers: 4, Poll: 5 * time.Second, Heartbeat: 5 * time.Second, StaleAfter: 30 * time.Second, MaxAttempts: 3,
		kick: make(chan struct{}, 1), running: map[uuid.UUID]context.CancelCauseFunc{},
	}
}

// Register koppelt een soort taak aan zijn handler. Alleen voor Run.
func (r *Runner) Register(kind string, h HandlerFunc) { r.handlers[kind] = h }

// Kick laat een wachtende worker meteen naar nieuwe taken kijken.
func (r *Runner) Kick() {
	select {
	case r.kick <- struct{}{}:
	default:
	}
}

// Enqueue zet een taak in de wachtrij en legt vast wie hem vroeg.
func (r *Runner) Enqueue(ctx context.Context, s Spec) (store.Job, error) {
	params, err := json.Marshal(s.Params)
	if err != nil {
		return store.Job{}, err
	}
	var requestedBy *uuid.UUID
	if s.Actor.Type == store.ActorTypeUser {
		if id, err := uuid.Parse(s.Actor.ID); err == nil {
			requestedBy = &id
		}
	}
	var j store.Job
	err = pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		j, err = q.CreateJob(ctx, store.CreateJobParams{
			Kind: s.Kind, Title: s.Title, Params: params, ClusterID: s.ClusterID, NodeID: s.NodeID,
			ProxmoxID: s.ProxmoxID, RequestedBy: requestedBy,
		})
		if err != nil {
			return err
		}
		return r.ev.Write(ctx, q, events.Event{
			Actor: s.Actor, SubjectType: "job", SubjectID: j.ID.String(), ClusterID: j.ClusterID,
			Action: "job.queued", Payload: map[string]any{"kind": j.Kind, "title": j.Title, "node_id": j.NodeID},
		})
	})
	if err == nil {
		r.Kick()
	}
	return j, err
}

// Cancel annuleert een taak. Een wachtende taak stopt meteen; een lopende
// krijgt het seintje en stopt bij de volgende gelegenheid.
func (r *Runner) Cancel(ctx context.Context, id uuid.UUID, actor events.Actor) (store.Job, error) {
	j, err := r.q.CancelQueuedJob(ctx, id)
	if err == nil {
		_ = r.ev.Write(ctx, nil, events.Event{
			Actor: actor, SubjectType: "job", SubjectID: id.String(), ClusterID: j.ClusterID,
			Action: "job.canceled", Payload: map[string]any{"kind": j.Kind, "title": j.Title},
		})
		return j, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return store.Job{}, err
	}
	j, err = r.q.RequestJobCancel(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		if _, err := r.q.GetJob(ctx, id); errors.Is(err, pgx.ErrNoRows) {
			return store.Job{}, ErrNotFound
		}
		return store.Job{}, ErrFinished
	}
	if err != nil {
		return store.Job{}, err
	}
	_ = r.ev.Write(ctx, nil, events.Event{
		Actor: actor, SubjectType: "job", SubjectID: id.String(), ClusterID: j.ClusterID,
		Action: "job.cancel_requested", Payload: map[string]any{"kind": j.Kind, "title": j.Title},
	})
	r.mu.Lock()
	cancel := r.running[id]
	r.mu.Unlock()
	if cancel != nil {
		cancel(ErrCanceled)
	}
	return j, nil
}

// Run voert taken uit tot ctx stopt. Lopende taken krijgen dan het seintje
// en worden bij de volgende start hervat.
func (r *Runner) Run(ctx context.Context) {
	r.requeueStale(ctx)
	var wg sync.WaitGroup
	for range r.Workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.worker(ctx)
		}()
	}
	t := time.NewTicker(r.StaleAfter / 2)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			wg.Wait()
			return
		case <-t.C:
			r.requeueStale(ctx)
		}
	}
}

func (r *Runner) requeueStale(ctx context.Context) {
	ids, err := r.q.RequeueStaleJobs(ctx, r.StaleAfter.Seconds())
	if err != nil {
		if ctx.Err() == nil {
			r.log.Error("onderbroken taken opnieuw inplannen", "err", err)
		}
		return
	}
	if len(ids) > 0 {
		r.log.Info("onderbroken taken worden hervat", "count", len(ids))
		r.Kick()
	}
}

func (r *Runner) worker(ctx context.Context) {
	poll := time.NewTimer(r.Poll)
	defer poll.Stop()
	for {
		for ctx.Err() == nil && r.runOne(ctx) {
		}
		poll.Reset(r.Poll)
		select {
		case <-ctx.Done():
			return
		case <-r.kick:
		case <-poll.C:
		}
	}
}

// runOne voert de oudste wachtende taak uit; false als er geen was.
func (r *Runner) runOne(ctx context.Context) bool {
	j, err := r.q.ClaimJob(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return false
	}
	if err != nil {
		if ctx.Err() == nil {
			r.log.Error("taak ophalen", "err", err)
		}
		return false
	}
	// Er kan meer werk liggen; een andere worker mag meekijken.
	r.Kick()
	r.execute(ctx, j)
	return true
}

func (r *Runner) execute(ctx context.Context, j store.Job) {
	log := r.log.With("job", j.ID, "kind", j.Kind)
	h, ok := r.handlers[j.Kind]
	switch {
	case !ok:
		r.finish(j, store.JobStatusFailed, "onbekend soort taak: "+j.Kind)
		return
	case j.CancelRequested:
		r.finish(j, store.JobStatusCanceled, "")
		return
	case j.Attempts > r.MaxAttempts:
		r.finish(j, store.JobStatusFailed, "de taak werd te vaak onderbroken")
		return
	}

	jctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	stop := context.AfterFunc(ctx, func() { cancel(errShutdown) })
	defer stop()
	r.mu.Lock()
	r.running[j.ID] = cancel
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		delete(r.running, j.ID)
		r.mu.Unlock()
	}()
	go r.heartbeat(jctx, j.ID, cancel)

	steps, err := r.q.ListJobSteps(jctx, j.ID)
	if err != nil {
		log.Error("stappen laden", "err", err)
		r.requeue(j.ID)
		return
	}
	job := &Job{Job: j, r: r, steps: steps}
	if j.Attempts > 1 {
		log.Info("taak wordt hervat", "attempt", j.Attempts)
	}
	err = safeRun(jctx, h, job)
	cause := context.Cause(jctx)
	switch {
	case err == nil:
		r.finish(j, store.JobStatusSucceeded, "")
	case errors.Is(cause, errShutdown):
		// Niet afronden: de taak gaat terug in de wachtrij en wordt hervat.
		r.requeue(j.ID)
	case errors.Is(err, ErrCanceled) || errors.Is(cause, ErrCanceled):
		r.finish(j, store.JobStatusCanceled, "")
	default:
		log.Warn("taak mislukt", "err", err)
		r.finish(j, store.JobStatusFailed, err.Error())
	}
}

func safeRun(ctx context.Context, h HandlerFunc, j *Job) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("interne fout: %v", p)
			slog.Error("panic in taak", "job", j.ID, "panic", p, "stack", string(debug.Stack()))
		}
	}()
	return h(ctx, j)
}

func (r *Runner) heartbeat(ctx context.Context, id uuid.UUID, cancel context.CancelCauseFunc) {
	t := time.NewTicker(r.Heartbeat)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		bctx, done := context.WithTimeout(context.Background(), 5*time.Second)
		cancelRequested, err := r.q.JobHeartbeat(bctx, id)
		done()
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			// Iemand anders heeft de taak overgenomen of afgerond.
			cancel(errors.New("taak loopt niet meer"))
		case err != nil:
			r.log.Warn("heartbeat van taak", "job", id, "err", err)
		case cancelRequested:
			cancel(ErrCanceled)
		}
	}
}

func (r *Runner) requeue(id uuid.UUID) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := r.q.RequeueJob(ctx, id); err != nil {
		r.log.Error("taak terug in de wachtrij zetten", "job", id, "err", err)
	}
}

func (r *Runner) finish(j store.Job, status store.JobStatus, msg string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done, err := r.q.FinishJob(ctx, store.FinishJobParams{ID: j.ID, Status: status, Error: msg})
	if err != nil {
		r.log.Error("taak afronden", "job", j.ID, "err", err)
		return
	}
	payload := map[string]any{"kind": j.Kind, "title": j.Title, "node_id": j.NodeID}
	if msg != "" {
		payload["error"] = msg
	}
	_ = r.ev.Write(ctx, nil, events.Event{
		Actor: events.System(), SubjectType: "job", SubjectID: j.ID.String(), ClusterID: j.ClusterID,
		Action: "job." + string(status), Payload: payload,
	})
	if r.Finished != nil {
		r.Finished(done)
	}
}

// Job is een lopende taak, zoals de handler hem ziet.
type Job struct {
	store.Job
	r     *Runner
	steps []store.JobStep
	next  int32
}

// Decode leest de parameters van de taak.
func (j *Job) Decode(v any) error { return json.Unmarshal(j.Params, v) }

// Step voert één stap uit. Een stap die bij een eerdere poging al lukte,
// wordt overgeslagen; een onderbroken stap krijgt zijn bewaarde state terug.
// Stappen worden herkend aan hun volgorde, dus een handler moet ze altijd in
// dezelfde volgorde doorlopen.
func (j *Job) Step(ctx context.Context, name string, fn func(ctx context.Context, s *Step) error) error {
	seq := j.next
	j.next++
	s := &Step{job: j, seq: seq, name: name, state: []byte("{}")}
	for _, prev := range j.steps {
		if prev.Seq != seq {
			continue
		}
		if prev.Status == store.JobStatusSucceeded {
			return nil
		}
		s.state, s.log = prev.State, prev.Log
	}
	if err := s.save(ctx, store.JobStatusRunning, ""); err != nil {
		return err
	}
	err := fn(ctx, s)
	cause := context.Cause(ctx)
	switch {
	case err == nil:
		err = s.save(ctx, store.JobStatusSucceeded, "")
	case errors.Is(cause, errShutdown):
		// Blijft "running" tot de taak hervat wordt.
		_ = s.save(ctx, store.JobStatusRunning, "")
	case errors.Is(err, ErrCanceled) || errors.Is(cause, ErrCanceled):
		_ = s.save(ctx, store.JobStatusCanceled, "")
	default:
		_ = s.save(ctx, store.JobStatusFailed, err.Error())
	}
	return err
}

// Step is een stap in uitvoering.
type Step struct {
	job   *Job
	seq   int32
	name  string
	state []byte
	log   []string
	mu    sync.Mutex
}

// State leest de bewaarde state van een eerdere poging in v; false als er
// geen was.
func (s *Step) State(v any) bool {
	if len(s.state) == 0 || string(s.state) == "{}" {
		return false
	}
	return json.Unmarshal(s.state, v) == nil
}

// SetState bewaart v meteen, zodat een hervatte stap verder kan waar hij was.
func (s *Step) SetState(ctx context.Context, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.state = b
	s.mu.Unlock()
	return s.save(ctx, store.JobStatusRunning, "")
}

// SetLog vervangt de uitvoer van de stap; Flush of het einde van de stap
// bewaart hem.
func (s *Step) SetLog(lines []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.log = lines
}

// Logf voegt een regel aan de uitvoer toe.
func (s *Step) Logf(format string, args ...any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.log = append(s.log, fmt.Sprintf(format, args...))
}

// Flush bewaart de uitvoer tot nu toe.
func (s *Step) Flush(ctx context.Context) error { return s.save(ctx, store.JobStatusRunning, "") }

// maxLogLines houdt de uitvoer per stap binnen de perken.
const maxLogLines = 500

func (s *Step) save(ctx context.Context, status store.JobStatus, msg string) error {
	s.mu.Lock()
	log := s.log
	if len(log) > maxLogLines {
		log = append([]string{fmt.Sprintf("… %d regels weggelaten", len(log)-maxLogLines+1)}, log[len(log)-maxLogLines+1:]...)
	}
	params := store.SaveJobStepParams{
		JobID: s.job.ID, Seq: s.seq, Name: s.name, Status: status, State: s.state, Log: log, Error: msg,
	}
	s.mu.Unlock()
	if params.Log == nil {
		params.Log = []string{}
	}
	if status != store.JobStatusRunning {
		now := time.Now()
		params.FinishedAt = &now
	}
	// Ook na annuleren moet de stap nog bewaard worden.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return s.job.r.q.SaveJobStep(ctx, params)
}
