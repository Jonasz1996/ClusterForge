// Package commands bewaart de eigen commando's van de beheerder (fase 10)
// en draait ze als taak command.run: één node tegelijk, de VIP-houder als
// laatste, met de uitvoer live in de stap van elke node.
package commands

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/jobs"
	"github.com/Jonasz1996/clusterforge/internal/noderun"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/internal/updates"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// Kind is de soort taak.
const Kind = "command.run"

// DefaultTimeout is de tijdslimiet zonder keuze.
const DefaultTimeout = 30 * time.Minute

var (
	ErrNotFound = errors.New("commando niet gevonden")
	runAs       = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)
)

// FieldError: een veld klopt niet.
type FieldError struct{ Field, Msg string }

func (e *FieldError) Error() string { return e.Msg }

// ConflictError: het kan nu niet.
type ConflictError struct{ Msg string }

func (e *ConflictError) Error() string { return e.Msg }

// Service bewaart en draait commando's.
type Service struct {
	pool    *pgxpool.Pool
	q       *store.Queries
	ev      *events.Writer
	runner  *jobs.Runner
	updates *updates.Service
	Run     *noderun.Runner
}

func NewService(pool *pgxpool.Pool, ev *events.Writer, runner *jobs.Runner, bus noderun.Bus, up *updates.Service) *Service {
	s := &Service{pool: pool, q: store.New(pool), ev: ev, runner: runner, updates: up, Run: &noderun.Runner{Bus: bus}}
	if runner != nil {
		runner.Register(Kind, s.run)
	}
	return s
}

// Input is een commando zoals de beheerder het bewaart.
type Input struct {
	Name           string
	Description    string
	Script         string
	RunAs          string
	TimeoutSeconds int
}

func (in *Input) normalize() error {
	in.Name = strings.TrimSpace(in.Name)
	in.Description = strings.TrimSpace(in.Description)
	in.RunAs = strings.TrimSpace(in.RunAs)
	// Windows-regeleinden breken een shellscript.
	in.Script = strings.ReplaceAll(in.Script, "\r\n", "\n")
	if in.RunAs == "" {
		in.RunAs = "root"
	}
	if in.TimeoutSeconds == 0 {
		in.TimeoutSeconds = int(DefaultTimeout.Seconds())
	}
	switch {
	case in.Name == "" || utf8.RuneCountInString(in.Name) > 100:
		return &FieldError{"name", "geef een naam van hoogstens 100 tekens"}
	case utf8.RuneCountInString(in.Description) > 2000:
		return &FieldError{"description", "de omschrijving is langer dan 2000 tekens"}
	case strings.TrimSpace(in.Script) == "":
		return &FieldError{"script", "het script is leeg"}
	case len(in.Script) > protocol.MaxRunScript:
		return &FieldError{"script", fmt.Sprintf("het script is langer dan %d bytes", protocol.MaxRunScript)}
	case !utf8.ValidString(in.Script):
		return &FieldError{"script", "het script is geen geldige UTF-8"}
	case !runAs.MatchString(in.RunAs):
		return &FieldError{"run_as", "ongeldige gebruiker; kleine letters, cijfers, _ en -"}
	case in.TimeoutSeconds < 10 || time.Duration(in.TimeoutSeconds)*time.Second > protocol.RunTimeoutMax:
		return &FieldError{"timeout_seconds", "de tijdslimiet moet tussen 10 seconden en 24 uur liggen"}
	}
	return nil
}

// Hash is de sha256 van een script, voor het logboek.
func Hash(script string) string {
	sum := sha256.Sum256([]byte(script))
	return hex.EncodeToString(sum[:])
}

func uniqueErr(err error) error {
	var pe *pgconn.PgError
	if errors.As(err, &pe) && pe.Code == "23505" {
		return &FieldError{"name", "er is al een commando met deze naam"}
	}
	return err
}

// Create bewaart een nieuw commando als revisie 1.
func (s *Service) Create(ctx context.Context, user uuid.UUID, in Input) (store.Command, error) {
	if err := in.normalize(); err != nil {
		return store.Command{}, err
	}
	var c store.Command
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		var err error
		c, err = q.InsertCommand(ctx, store.InsertCommandParams{
			Name: in.Name, Description: in.Description, Script: in.Script, RunAs: in.RunAs,
			TimeoutSeconds: int32(in.TimeoutSeconds), UpdatedBy: &user,
		})
		if err != nil {
			return uniqueErr(err)
		}
		if err := q.InsertCommandRevision(ctx, store.InsertCommandRevisionParams{CommandID: c.ID, Revision: 1, Script: in.Script, CreatedBy: &user}); err != nil {
			return err
		}
		return s.ev.Write(ctx, q, events.Event{Actor: events.User(user), SubjectType: "command", SubjectID: c.ID.String(), Action: "command.saved",
			Payload: map[string]any{"name": c.Name, "revision": 1, "sha256": Hash(c.Script), "run_as": c.RunAs}})
	})
	return c, err
}

// Update bewaart een gewijzigd commando; een andere tekst is een nieuwe
// revisie.
func (s *Service) Update(ctx context.Context, user uuid.UUID, id uuid.UUID, in Input) (store.Command, error) {
	if err := in.normalize(); err != nil {
		return store.Command{}, err
	}
	var c store.Command
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		old, err := q.LockCommand(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		rev := old.Revision
		if old.Script != in.Script {
			rev++
		}
		c, err = q.UpdateCommand(ctx, store.UpdateCommandParams{
			ID: id, Name: in.Name, Description: in.Description, Script: in.Script, RunAs: in.RunAs,
			TimeoutSeconds: int32(in.TimeoutSeconds), Revision: rev, UpdatedBy: &user,
		})
		if err != nil {
			return uniqueErr(err)
		}
		if rev != old.Revision {
			if err := q.InsertCommandRevision(ctx, store.InsertCommandRevisionParams{CommandID: id, Revision: rev, Script: in.Script, CreatedBy: &user}); err != nil {
				return err
			}
		}
		return s.ev.Write(ctx, q, events.Event{Actor: events.User(user), SubjectType: "command", SubjectID: id.String(), Action: "command.saved",
			Payload: map[string]any{"name": c.Name, "revision": rev, "sha256": Hash(c.Script), "run_as": c.RunAs}})
	})
	return c, err
}

// Delete haalt een commando weg; de taken van zijn runs blijven.
func (s *Service) Delete(ctx context.Context, actor events.Actor, id uuid.UUID) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		old, err := q.LockCommand(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if _, err := q.DeleteCommand(ctx, id); err != nil {
			return err
		}
		return s.ev.Write(ctx, q, events.Event{Actor: actor, SubjectType: "command", SubjectID: id.String(), Action: "command.deleted",
			Payload: map[string]any{"name": old.Name}})
	})
}

// RunRequest zegt waar een commando draait.
type RunRequest struct {
	NodeIDs         []uuid.UUID
	ClusterID       *uuid.UUID
	ContinueOnError bool
}

// Target zijn de nodes van een run, met het cluster als dat er één is.
type Target struct {
	Nodes []updates.Node
	// Prod zijn de prodclusters erin, op slug.
	Prod []string
}

// Select kiest de nodes, in de volgorde van de run.
func (s *Service) Select(ctx context.Context, req RunRequest) (Target, error) {
	if (len(req.NodeIDs) > 0) == (req.ClusterID != nil) {
		return Target{}, &FieldError{"node_ids", "kies nodes of een cluster"}
	}
	all, err := s.updates.Overview(ctx)
	if err != nil {
		return Target{}, err
	}
	var t Target
	for _, n := range all {
		switch {
		case req.ClusterID != nil && (n.ClusterID == nil || *n.ClusterID != *req.ClusterID):
			continue
		case len(req.NodeIDs) > 0 && !slices.Contains(req.NodeIDs, n.NodeID):
			continue
		case !n.Supported:
			return Target{}, &ConflictError{fmt.Sprintf("de agent op %s heeft protocol %d en kan nog geen commando's draaien (vanaf %d); werk de agents eerst bij", n.Hostname, n.Protocol, protocol.RunSince)}
		case !n.Online:
			return Target{}, &ConflictError{"de agent op " + n.Hostname + " is niet verbonden"}
		}
		t.Nodes = append(t.Nodes, n)
		if n.Prod() && !slices.Contains(t.Prod, n.ClusterSlug) {
			t.Prod = append(t.Prod, n.ClusterSlug)
		}
	}
	if len(req.NodeIDs) > 0 && len(t.Nodes) < len(req.NodeIDs) {
		return Target{}, &ConflictError{"niet elke gevraagde node heeft een agent"}
	}
	if len(t.Nodes) == 0 {
		return Target{}, &ConflictError{"dit cluster heeft geen nodes met een agent"}
	}
	t.Nodes = updates.Order(t.Nodes)
	return t, nil
}

type params struct {
	CommandID       uuid.UUID `json:"command_id"`
	Name            string    `json:"name"`
	Revision        int       `json:"revision"`
	Script          string    `json:"script"`
	RunAs           string    `json:"run_as"`
	TimeoutSeconds  int       `json:"timeout_seconds"`
	ContinueOnError bool      `json:"continue_on_error,omitempty"`
	Nodes           []node    `json:"nodes"`
}

type node struct {
	ID        uuid.UUID `json:"id"`
	Hostname  string    `json:"hostname"`
	VIPHolder bool      `json:"vip_holder,omitempty"`
}

// Start zet een run in de wachtrij met de tekst van nu. Alle nodes in één
// cluster: dat cluster krijgt zijn slot, zodat er niets anders tegelijk
// loopt.
func (s *Service) Start(ctx context.Context, actor events.Actor, id uuid.UUID, req RunRequest, t Target) (store.Job, error) {
	c, err := s.q.GetCommand(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.Job{}, ErrNotFound
	}
	if err != nil {
		return store.Job{}, err
	}
	p := params{
		CommandID: c.ID, Name: c.Name, Revision: int(c.Revision), Script: c.Script, RunAs: c.RunAs,
		TimeoutSeconds: int(c.TimeoutSeconds), ContinueOnError: req.ContinueOnError,
	}
	var cluster *uuid.UUID
	same := true
	for i, n := range t.Nodes {
		p.Nodes = append(p.Nodes, node{ID: n.NodeID, Hostname: n.Hostname, VIPHolder: n.VIPHolder})
		switch {
		case i == 0:
			cluster = n.ClusterID
		case (cluster == nil) != (n.ClusterID == nil) || (cluster != nil && *cluster != *n.ClusterID):
			same = false
		}
	}
	spec := jobs.Spec{Kind: Kind, Params: p, Actor: actor}
	switch {
	case len(t.Nodes) == 1:
		spec.Title, spec.NodeID = fmt.Sprintf("%s op %s", c.Name, t.Nodes[0].Hostname), &t.Nodes[0].NodeID
	case same && cluster != nil:
		spec.Title = fmt.Sprintf("%s in %s", c.Name, t.Nodes[0].ClusterName)
	default:
		spec.Title = fmt.Sprintf("%s op %d nodes", c.Name, len(t.Nodes))
	}
	if same {
		spec.ClusterID = cluster
	}
	var j store.Job
	if spec.ClusterID != nil {
		j, err = s.runner.EnqueueForCluster(ctx, spec)
	} else {
		j, err = s.runner.Enqueue(ctx, spec)
	}
	var busy jobs.BusyError
	if errors.As(err, &busy) {
		return store.Job{}, &ConflictError{busy.Error()}
	}
	if err != nil {
		return store.Job{}, err
	}
	s.runner.Kick()
	return j, nil
}

func (s *Service) run(ctx context.Context, j *jobs.Job) error {
	var p params
	if err := j.Decode(&p); err != nil {
		return err
	}
	actor := events.System()
	if j.RequestedBy != nil {
		actor = events.User(*j.RequestedBy)
	}
	hash := Hash(p.Script)
	var failed []string
	for i, n := range p.Nodes {
		name := n.Hostname
		if n.VIPHolder {
			name += " (VIP-houder)"
		}
		spec := protocol.RunSpec{
			ID: fmt.Sprintf("%s-%d-%d-cmd", j.ID, j.Attempts, i), Kind: protocol.RunScript,
			Title:  fmt.Sprintf("%s (revisie %d) op %s als %s", p.Name, p.Revision, n.Hostname, p.RunAs),
			Script: p.Script, User: p.RunAs, TimeoutSeconds: p.TimeoutSeconds,
		}
		err := j.Step(ctx, name, func(ctx context.Context, st *jobs.Step) error {
			res, err := s.Run.Do(ctx, st, n.ID, spec)
			if err != nil {
				return err
			}
			rt, _ := s.q.GetNodeRuntime(ctx, n.ID)
			_ = s.ev.Write(ctx, nil, events.Event{Actor: actor, SubjectType: "node", SubjectID: n.ID.String(), ClusterID: rt.ClusterID,
				Action: "node.command_ran", Payload: map[string]any{
					"command": p.Name, "command_id": p.CommandID.String(), "revision": p.Revision, "sha256": hash,
					"hostname": n.Hostname, "result": res.State, "exit": res.Exit, "job_id": j.ID.String(),
				}})
			if res.State != protocol.RunOK {
				return errors.New(noderun.Describe(res))
			}
			return nil
		})
		switch {
		case err == nil:
		case ctx.Err() != nil:
			return err
		case p.ContinueOnError && i < len(p.Nodes)-1:
			failed = append(failed, n.Hostname)
		case p.ContinueOnError:
			failed = append(failed, n.Hostname)
			return fmt.Errorf("mislukt op %s", strings.Join(failed, ", "))
		case i < len(p.Nodes)-1:
			return fmt.Errorf("%s: %w; op de nodes erna is het commando niet gedraaid", n.Hostname, err)
		default:
			return fmt.Errorf("%s: %w", n.Hostname, err)
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("mislukt op %s", strings.Join(failed, ", "))
	}
	return nil
}
