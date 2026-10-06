package planner

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// lockID is de advisory lock van de planner: ook als er ooit twee servers
// draaien, plant er maar één tegelijk.
const lockID = 4242002

// Task is één soort geplande runs, zoals de failovertests of de
// back-upcontroles. Tick start wat nu aan de beurt is en slaat over wat te
// laat is.
type Task struct {
	Name string
	Tick func(ctx context.Context, now time.Time) error
}

// Scheduler loopt elke Interval, en meteen na Kick, langs alle taken. Het
// testslot zorgt dat er hoogstens één test tegelijk loopt; de planner wacht
// dan tot het weer vrij is.
type Scheduler struct {
	pool  *pgxpool.Pool
	log   *slog.Logger
	tasks []Task
	kick  chan struct{}

	// Interval is hoe vaak de planner kijkt.
	Interval time.Duration
	// Now is de klok; tests zetten er een andere.
	Now func() time.Time
}

func NewScheduler(pool *pgxpool.Pool, log *slog.Logger, tasks ...Task) *Scheduler {
	return &Scheduler{pool: pool, log: log, tasks: tasks, kick: make(chan struct{}, 1), Interval: time.Minute, Now: time.Now}
}

// Kick laat de planner meteen kijken, bijvoorbeeld als een test klaar is en
// het testslot vrijkwam.
func (s *Scheduler) Kick() {
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

// Run plant tot ctx afloopt.
func (s *Scheduler) Run(ctx context.Context) {
	t := time.NewTicker(s.Interval)
	defer t.Stop()
	for {
		s.Tick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-s.kick:
		}
	}
}

// Tick loopt één keer langs alle taken, onder de advisory lock. Een fout in
// de ene taak houdt de andere niet tegen.
func (s *Scheduler) Tick(ctx context.Context) {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		if ctx.Err() == nil {
			s.log.Error("planner: geen verbinding met de database", "err", err)
		}
		return
	}
	defer conn.Release()
	var locked bool
	if err := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", lockID).Scan(&locked); err != nil || !locked {
		return
	}
	defer func() {
		uctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_, _ = conn.Exec(uctx, "SELECT pg_advisory_unlock($1)", lockID)
	}()
	for _, task := range s.tasks {
		if err := task.Tick(ctx, s.Now()); err != nil && ctx.Err() == nil {
			s.log.Error("planner: geplande runs starten mislukt", "task", task.Name, "err", err)
		}
	}
}
