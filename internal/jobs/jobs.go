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
	"slices"
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
	// ErrNotRetryable betekent dat dit soort taak niet opnieuw kan.
	ErrNotRetryable = errors.New("deze taak kan niet opnieuw; start een nieuwe")
	// ErrNotFailed betekent dat de taak nog loopt of gelukt is.
	ErrNotFailed = errors.New("alleen een mislukte of geannuleerde taak kan opnieuw")

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

	// clusterSlot zet EnqueueForCluster: de taak houdt het slot van zijn
	// cluster tot hij klaar is.
	clusterSlot bool
}

// BusyError betekent dat er in het cluster al een schrijvende taak wacht of
// loopt.
type BusyError struct {
	Cluster string
	JobID   uuid.UUID
	Title   string
}

func (e BusyError) Error() string {
	return fmt.Sprintf("in cluster %s loopt al een taak (%s); wacht tot die klaar is", e.Cluster, e.Title)
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

	retryable []string
	finished  map[string][]FinishedFunc

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

// FinishedFunc hoort dat een taak klaar is, met zijn eindstatus.
type FinishedFunc func(ctx context.Context, j store.Job)

// OnFinished laat fn weten dat een taak van deze soort klaar is: gelukt,
// mislukt of geannuleerd, ook als zijn handler nooit draaide (geannuleerd in
// de wachtrij, te vaak onderbroken). Zo rondt elke module haar eigen
// gegevens af. Alleen bij het opstarten aanroepen.
func (r *Runner) OnFinished(kind string, fn FinishedFunc) {
	if r.finished == nil {
		r.finished = map[string][]FinishedFunc{}
	}
	r.finished[kind] = append(r.finished[kind], fn)
}

func (r *Runner) notifyFinished(j store.Job) {
	for _, fn := range r.finished[j.Kind] {
		func() {
			ctx, cancel := context.WithTimeout(events.WithJob(context.Background(), j.ID), 30*time.Second)
			defer cancel()
			defer func() {
				if p := recover(); p != nil {
					r.log.Error("panic na afloop van taak", "job", j.ID, "panic", p, "stack", string(debug.Stack()))
				}
			}()
			fn(ctx, j)
		}()
	}
}

// Kick laat een wachtende worker meteen naar nieuwe taken kijken.
func (r *Runner) Kick() {
	select {
	case r.kick <- struct{}{}:
	default:
	}
}

// Enqueue zet een taak in de wachtrij en legt vast wie hem vroeg.
func (r *Runner) Enqueue(ctx context.Context, s Spec) (store.Job, error) {
	var j store.Job
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		var err error
		j, err = r.EnqueueTx(ctx, store.New(tx), s)
		return err
	})
	if err == nil {
		r.Kick()
	}
	return j, err
}

// EnqueueTx zet een taak in de wachtrij binnen een transactie van de
// aanroeper. Roep na de commit Kick aan.
func (r *Runner) EnqueueTx(ctx context.Context, q *store.Queries, s Spec) (store.Job, error) {
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
	j, err := q.CreateJob(ctx, store.CreateJobParams{
		Kind: s.Kind, Title: s.Title, Params: params, ClusterID: s.ClusterID, NodeID: s.NodeID,
		ProxmoxID: s.ProxmoxID, RequestedBy: requestedBy, ClusterSlot: s.clusterSlot,
	})
	if err != nil {
		return store.Job{}, err
	}
	return j, r.ev.Write(ctx, q, events.Event{
		Actor: s.Actor, SubjectType: "job", SubjectID: j.ID.String(), ClusterID: j.ClusterID,
		Action: "job.queued", Payload: map[string]any{"kind": j.Kind, "title": j.Title, "node_id": j.NodeID},
	})
}

// EnqueueForCluster zet een schrijvende taak in de wachtrij, maar alleen als
// er in zijn cluster geen andere wacht of loopt; anders is het een
// BusyError. Een taak op een node van het cluster telt mee. Zo valt een
// herstel nooit samen met een reboot of een VM-stop in hetzelfde cluster.
// Zonder cluster is het gewoon Enqueue.
func (r *Runner) EnqueueForCluster(ctx context.Context, s Spec) (store.Job, error) {
	var j store.Job
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		var err error
		j, err = r.EnqueueForClusterTx(ctx, store.New(tx), s)
		return err
	})
	if err == nil {
		r.Kick()
	}
	return j, err
}

// EnqueueForClusterTx is EnqueueForCluster binnen een transactie van de
// aanroeper. De clusterrij blijft vergrendeld tot de commit.
func (r *Runner) EnqueueForClusterTx(ctx context.Context, q *store.Queries, s Spec) (store.Job, error) {
	s.clusterSlot = true
	if s.ClusterID != nil {
		if err := lockSlot(ctx, q, *s.ClusterID, uuid.Nil); err != nil {
			return store.Job{}, err
		}
	}
	return r.EnqueueTx(ctx, q, s)
}

// lockSlot vergrendelt de clusterrij tot de commit en geeft een BusyError
// als er in het cluster al een andere schrijvende taak dan self wacht of
// loopt.
func lockSlot(ctx context.Context, q *store.Queries, clusterID, self uuid.UUID) error {
	name, err := q.LockClusterForJob(ctx, clusterID)
	if err != nil {
		return err
	}
	busy, err := q.GetClusterSlotJob(ctx, clusterID)
	switch {
	case errors.Is(err, pgx.ErrNoRows) || err == nil && busy.ID == self:
		return nil
	case err != nil:
		return err
	}
	return BusyError{Cluster: name, JobID: busy.ID, Title: busy.Title}
}

// Retryable geeft aan dat een mislukte taak van deze soort opnieuw kan
// vanaf de stap die mislukte. Alleen voor soorten waarvan elke stap
// hervatbaar is.
func (r *Runner) Retryable(kind string) { r.retryable = append(r.retryable, kind) }

// CanRetry is true als een taak van deze soort opnieuw kan.
func (r *Runner) CanRetry(kind string) bool { return slices.Contains(r.retryable, kind) }

// Retry zet een mislukte of geannuleerde taak opnieuw in de wachtrij. Stappen
// die lukten, worden overgeslagen. Een taak die het slot van zijn cluster
// hield, moet het opnieuw kunnen nemen; anders is het een BusyError.
func (r *Runner) Retry(ctx context.Context, id uuid.UUID, actor events.Actor) (store.Job, error) {
	var j store.Job
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		cur, err := q.GetJob(ctx, id)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			return ErrNotFound
		case err != nil:
			return err
		case cur.Job.ClusterSlot && cur.Job.ClusterID != nil:
			if err := lockSlot(ctx, q, *cur.Job.ClusterID, id); err != nil {
				return err
			}
		}
		j, err = q.RetryJob(ctx, store.RetryJobParams{ID: id, Kinds: r.retryable})
		switch {
		case errors.Is(err, pgx.ErrNoRows) && !r.CanRetry(cur.Job.Kind):
			return ErrNotRetryable
		case errors.Is(err, pgx.ErrNoRows):
			return ErrNotFailed
		}
		return err
	})
	if err != nil {
		return store.Job{}, err
	}
	_ = r.ev.Write(ctx, nil, events.Event{
		Actor: actor, SubjectType: "job", SubjectID: id.String(), ClusterID: j.ClusterID,
		Action: "job.retried", Payload: map[string]any{"kind": j.Kind, "title": j.Title, "node_id": j.NodeID},
	})
	r.Kick()
	return j, nil
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
		r.notifyFinished(j)
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

	// Alles wat de handler doet, hoort in het logboek bij deze taak.
	jctx, cancel := context.WithCancelCause(events.WithJob(context.Background(), j.ID))
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
	// De heartbeat loopt tot de handler terugkeert, ook na annuleren: een
	// handler die dan nog herstelt, mag niet als onderbroken gelden.
	hctx, hstop := context.WithCancel(context.Background())
	defer hstop()
	go r.heartbeat(hctx, j.ID, cancel)

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
	r.notifyFinished(done)
}

// Interrupted is true als ctx afliep omdat de server stopt. De taak gaat na
// de herstart verder, dus een handler moet dan niets terugdraaien.
func Interrupted(ctx context.Context) bool { return errors.Is(context.Cause(ctx), errShutdown) }

// Detach geeft een context die doorloopt als een gebruiker de taak
// annuleert, maar stopt als de server stopt. Voor herstel dat altijd moet
// gebeuren; Interrupted werkt er gewoon op.
func Detach(ctx context.Context) (context.Context, context.CancelFunc) {
	d, cancel := context.WithCancelCause(context.WithoutCancel(ctx))
	stop := context.AfterFunc(ctx, func() {
		if Interrupted(ctx) {
			cancel(errShutdown)
		}
	})
	return d, func() {
		stop()
		cancel(nil)
	}
}

// Canceled is true als een gebruiker de taak annuleerde.
func Canceled(ctx context.Context) bool { return errors.Is(context.Cause(ctx), ErrCanceled) }

// Job is een lopende taak, zoals de handler hem ziet.
type Job struct {
	store.Job
	r     *Runner
	steps []store.JobStep
	next  int32
}

// Decode leest de parameters van de taak.
func (j *Job) Decode(v any) error { return json.Unmarshal(j.Params, v) }

// Done leest de state van een stap die bij een eerdere poging al lukte;
// false als er zo geen stap is. Step slaat zo'n stap over, dus een handler
// die zijn uitkomst later nodig heeft, haalt hem hier.
func (j *Job) Done(name string, v any) bool {
	for _, prev := range j.steps {
		if prev.Name == name && prev.Status == store.JobStatusSucceeded {
			return json.Unmarshal(prev.State, v) == nil
		}
	}
	return false
}

// Previous geeft de stappen van eerdere pogingen, op volgorde. Leeg bij de
// eerste poging.
func (j *Job) Previous() []store.JobStep { return slices.Clone(j.steps) }

// Keep laat de volgende stap zoals een eerdere poging hem achterliet en
// voert niets uit; voor een taak die na een herstart niet verder gaat.
func (j *Job) Keep() { j.next++ }

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
