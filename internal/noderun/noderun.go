// Package noderun voert een run (fase 10) op een node uit binnen een stap
// van een taak: starten, de uitvoer elke seconde ophalen en in de stap
// zetten, en de uitkomst teruggeven. Het run-id staat in de state van de
// stap; na een herstart van de server leest de taak verder bij dezelfde
// run, want de agent start een bekend id niet opnieuw.
package noderun

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/jobs"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// Bus stuurt runverzoeken naar een agent.
type Bus interface {
	Run(ctx context.Context, nodeID uuid.UUID, req protocol.RunRequest) (protocol.RunResponse, error)
}

// Runner voert runs uit.
type Runner struct {
	Bus Bus
	// Poll is hoe vaak de uitvoer opgehaald wordt; nul is een seconde.
	Poll time.Duration
	// Offline is hoe lang een agent onbereikbaar mag zijn tijdens een run;
	// nul is vijf minuten.
	Offline time.Duration
}

// state is de state van de stap.
type state struct {
	RunID  string `json:"run_id"`
	Offset int64  `json:"offset"`
	// Status is de eindstand, zodra de run afgelopen is.
	Status *protocol.RunStatus `json:"status,omitempty"`
}

// Result leest de eindstand van een run uit een stap van de taak die al
// lukte; false als er geen is.
func Result(j *jobs.Job, step string) (protocol.RunStatus, bool) {
	var s state
	if !j.Done(step, &s) || s.Status == nil {
		return protocol.RunStatus{}, false
	}
	return *s.Status, true
}

// Log is de uitvoer van een stap: jobs.Step, of iets anders in tests.
type Log interface {
	Logf(format string, args ...any)
	Flush(ctx context.Context) error
}

// StepState bewaart de state van een stap; jobs.Step doet dat.
type StepState interface {
	State(v any) bool
	SetState(ctx context.Context, v any) error
}

// Do voert spec uit op de node, in stap st, en geeft de eindstand. Een run
// die niet ok eindigt, is geen fout: de beller beslist. Een fout is een
// run die niet kon starten of waarvan de stand onbekend bleef.
func (r *Runner) Do(ctx context.Context, st *jobs.Step, nodeID uuid.UUID, spec protocol.RunSpec) (protocol.RunStatus, error) {
	return r.do(ctx, st, st, nodeID, spec)
}

func (r *Runner) do(ctx context.Context, log Log, ss StepState, nodeID uuid.UUID, spec protocol.RunSpec) (protocol.RunStatus, error) {
	poll := r.Poll
	if poll <= 0 {
		poll = time.Second
	}
	offline := r.Offline
	if offline <= 0 {
		offline = 5 * time.Minute
	}
	// Een run uit een eerdere poging die nog liep (herstart van de server),
	// leest de taak verder; een afgelopen run (opnieuw proberen) begint
	// opnieuw met het nieuwe id.
	var s state
	if !ss.State(&s) || s.RunID == "" || s.Status != nil {
		s = state{RunID: spec.ID}
		res, err := r.request(ctx, nodeID, protocol.RunRequest{Op: protocol.RunStart, Spec: &spec})
		if err != nil {
			return protocol.RunStatus{}, fmt.Errorf("de run starten: %w", err)
		}
		if res.Status == nil && res.Error != "" {
			return protocol.RunStatus{}, errors.New(res.Error)
		}
		if err := ss.SetState(ctx, s); err != nil {
			return protocol.RunStatus{}, err
		}
	}
	var lastOK time.Time
	partial := ""
	for {
		res, err := r.request(ctx, nodeID, protocol.RunRequest{Op: protocol.RunPoll, RunID: s.RunID, Offset: s.Offset})
		switch {
		case ctx.Err() != nil:
			return r.stopped(ctx, log, nodeID, s)
		case err != nil:
			if lastOK.IsZero() {
				lastOK = time.Now()
			}
			if time.Since(lastOK) > offline {
				return protocol.RunStatus{}, fmt.Errorf("de agent antwoordt al %s niet meer: %w; de run kan op de node nog lopen", offline, err)
			}
		case res.Status == nil:
			return protocol.RunStatus{}, errors.New(firstNonEmpty(res.Error, "de agent kent deze run niet"))
		default:
			lastOK = time.Time{}
			if res.Skipped {
				log.Logf("… (uitvoer overgeslagen)")
			}
			partial = writeLines(log, partial+res.Output)
			moved := res.Offset != s.Offset
			s.Offset = res.Offset
			if res.Status.Done() {
				if partial != "" {
					log.Logf("%s", partial)
				}
				s.Status = res.Status
				_ = ss.SetState(ctx, s)
				return *res.Status, nil
			}
			if moved {
				if err := ss.SetState(ctx, s); err != nil {
					return protocol.RunStatus{}, err
				}
			} else {
				_ = log.Flush(ctx)
			}
		}
		select {
		case <-ctx.Done():
			return r.stopped(ctx, log, nodeID, s)
		case <-time.After(poll):
		}
	}
}

// stopped stopt de run op de node als de taak geannuleerd is; bij een
// herstart van de server loopt hij door en leest de hervatte taak verder.
func (r *Runner) stopped(ctx context.Context, log Log, nodeID uuid.UUID, s state) (protocol.RunStatus, error) {
	if !jobs.Canceled(ctx) {
		return protocol.RunStatus{}, context.Cause(ctx)
	}
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	if _, err := r.request(sctx, nodeID, protocol.RunRequest{Op: protocol.RunStop, RunID: s.RunID}); err != nil {
		log.Logf("de run stoppen op de node mislukte: %v", err)
	} else {
		log.Logf("de run is op de node gestopt")
	}
	return protocol.RunStatus{}, context.Cause(ctx)
}

func (r *Runner) request(ctx context.Context, nodeID uuid.UUID, req protocol.RunRequest) (protocol.RunResponse, error) {
	rctx, cancel := context.WithTimeout(ctx, protocol.RunRequestTimeout)
	defer cancel()
	req.ID = uuid.NewString()
	req.Deadline = time.Now().Add(protocol.RunRequestTimeout)
	return r.Bus.Run(rctx, nodeID, req)
}

// writeLines zet de hele regels in de log en geeft de rest terug.
func writeLines(log Log, text string) string {
	for {
		i := strings.IndexByte(text, '\n')
		if i < 0 {
			return text
		}
		log.Logf("%s", strings.TrimRight(text[:i], "\r"))
		text = text[i+1:]
	}
}

func firstNonEmpty(s ...string) string {
	for _, x := range s {
		if x != "" {
			return x
		}
	}
	return ""
}

// Describe zegt in gewone woorden hoe een run eindigde.
func Describe(st protocol.RunStatus) string {
	exit := ""
	if st.Exit != nil {
		exit = fmt.Sprintf(" (exitcode %d)", *st.Exit)
	}
	switch st.State {
	case protocol.RunOK:
		return "gelukt"
	case protocol.RunTimeout:
		return "gestopt na de tijdslimiet"
	case protocol.RunStopped:
		return "gestopt"
	}
	if st.Error != "" {
		return "mislukt: " + st.Error + exit
	}
	return "mislukt" + exit
}
