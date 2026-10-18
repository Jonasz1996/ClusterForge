// Package cronjobs leest de cronjobs en systemd-timers op de nodes via de
// agent (agentprotocol 16, fase 6). Elke vijf minuten vraagt het elke node,
// bewaart het in node_crons, en schrijft een event als er een job bijkomt,
// verdwijnt of verandert. Het overzicht legt per cluster de jobs van de
// nodes naast elkaar.
package cronjobs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Jonasz1996/clusterforge/internal/agentbus"
	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/status"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// Requester stuurt een cronverzoek naar de agent van een node; de bus doet
// dat over NATS.
type Requester interface {
	Cron(ctx context.Context, nodeID uuid.UUID, req protocol.CronRequest) (protocol.CronResponse, error)
}

// Soorten fout in node_cron_reads.error_kind, zoals bij de databases.
const (
	ErrOldAgent = "old_agent"
	ErrOffline  = "offline"
	ErrNoAnswer = "no_answer"
	ErrAgent    = "agent"
)

// parallel is hoeveel nodes de sync tegelijk vraagt.
const parallel = 8

// maxEvents is hoeveel events één node per ronde hoogstens geeft; de rest
// komt samen in één event.
const maxEvents = 20

// Service leest de cronjobs.
type Service struct {
	pool *pgxpool.Pool
	q    *store.Queries
	bus  Requester
	ev   *events.Writer
	log  *slog.Logger
	// Interval is hoe vaak Run alles leest; nul of minder zet het uit.
	Interval time.Duration
	// Timeout is hoe lang een node mag doen over zijn antwoord.
	Timeout time.Duration
	Now     func() time.Time
	// Location is de tijdzone waarin een cronschema de volgende run krijgt;
	// nil is time.Local. cron rekent op de node in diens tijdzone, en
	// ClusterForge draait meestal in dezelfde.
	Location *time.Location

	kick chan struct{}
}

func NewService(pool *pgxpool.Pool, bus Requester, ev *events.Writer, log *slog.Logger) *Service {
	return &Service{
		pool: pool, q: store.New(pool), bus: bus, ev: ev, log: log, Interval: 5 * time.Minute, Timeout: protocol.CronTimeout, Now: time.Now,
		kick: make(chan struct{}, 1),
	}
}

// Kick laat Run meteen opnieuw lezen.
func (s *Service) Kick() {
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

// Run leest de cronjobs elke Interval en zet de beheerde jobs op de nodes,
// tot ctx afloopt.
func (s *Service) Run(ctx context.Context) {
	if s.Interval <= 0 {
		return
	}
	t := time.NewTicker(s.Interval)
	defer t.Stop()
	for {
		if err := s.SyncAll(ctx); err != nil && ctx.Err() == nil {
			s.log.Warn("cronjobs lezen mislukt", "err", err)
		}
		if err := s.ApplyAll(ctx); err != nil && ctx.Err() == nil {
			s.log.Warn("beheerde cronjobs op de nodes zetten mislukt", "err", err)
		}
		if err := s.Prune(ctx); err != nil && ctx.Err() == nil {
			s.log.Warn("oude runs van cronjobs weghalen mislukt", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-s.kick:
		}
	}
}

// SyncAll vraagt elke node naar zijn cronjobs. Een node die niet antwoordt,
// houdt zijn laatst bekende jobs met de fout erbij.
func (s *Service) SyncAll(ctx context.Context) error {
	nodes, err := s.q.ListCronCandidateNodes(ctx)
	if err != nil {
		return err
	}
	sem := make(chan struct{}, parallel)
	var wg sync.WaitGroup
	for _, n := range nodes {
		sem <- struct{}{}
		wg.Go(func() {
			defer func() { <-sem }()
			if err := s.syncNode(ctx, n); err != nil && ctx.Err() == nil {
				s.log.Warn("cronjobs van een node lezen mislukt", "node", n.Hostname, "err", err)
			}
		})
	}
	wg.Wait()
	return ctx.Err()
}

// SyncNode leest één node meteen, bijvoorbeeld na een knop in de webinterface.
func (s *Service) SyncNode(ctx context.Context, nodeID uuid.UUID) error {
	nodes, err := s.q.ListCronCandidateNodes(ctx)
	if err != nil {
		return err
	}
	for _, n := range nodes {
		if n.ID == nodeID {
			return s.syncNode(ctx, n)
		}
	}
	return nil
}

// Key is de sleutel van een job: dezelfde voor dezelfde job op elke node.
func Key(e protocol.CronEntry) string {
	h := sha256.Sum256([]byte(strings.Join([]string{e.Source, e.File, e.User, e.Schedule, e.Command}, "\x00")))
	return hex.EncodeToString(h[:12])
}

func (s *Service) syncNode(ctx context.Context, n store.ListCronCandidateNodesRow) error {
	now := s.Now()
	mark := func(kind, msg string) error {
		return s.q.MarkCronRead(ctx, store.MarkCronReadParams{NodeID: n.ID, Error: msg, ErrorKind: kind, SyncedAt: now})
	}
	switch {
	case n.AgentProtocol < protocol.CronSince:
		return mark(ErrOldAgent, fmt.Sprintf("de agent op %s heeft protocol %d; cronjobs lezen vraagt protocol %d", n.Hostname, n.AgentProtocol, protocol.CronSince))
	case n.HeartbeatAt == nil || now.Sub(*n.HeartbeatAt) > status.HeartbeatLate:
		return mark(ErrOffline, "de agent op "+n.Hostname+" is niet verbonden")
	}
	rctx, cancel := context.WithTimeout(ctx, s.Timeout)
	defer cancel()
	res, err := s.bus.Cron(rctx, n.ID, protocol.CronRequest{Op: protocol.CronList})
	switch {
	case errors.Is(err, agentbus.ErrAgentOffline):
		return mark(ErrOffline, "de agent op "+n.Hostname+" is niet verbonden")
	case errors.Is(err, agentbus.ErrNoAnswer):
		return mark(ErrNoAnswer, "de agent op "+n.Hostname+" antwoordde niet op tijd")
	case err != nil:
		return mark(ErrAgent, err.Error())
	case res.Error != "":
		return mark(ErrAgent, res.Error)
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		old, err := q.ListNodeCronsOf(ctx, n.ID)
		if err != nil {
			return err
		}
		oldByKey := map[string]store.NodeCron{}
		for _, o := range old {
			oldByKey[o.Key] = o
		}
		if err := q.DeleteNodeCrons(ctx, n.ID); err != nil {
			return err
		}
		seen := map[string]bool{}
		var added []protocol.CronEntry
		var results []result
		for _, e := range res.Entries {
			key := Key(e)
			// Twee gelijke regels in één bestand: de tweede krijgt een
			// eigen sleutel.
			for i := 2; seen[key]; i++ {
				key = fmt.Sprintf("%s-%d", Key(e)[:20], i)
			}
			seen[key] = true
			first := now
			o, known := oldByKey[key]
			if known {
				first = o.FirstSeen
				delete(oldByKey, key)
			} else {
				added = append(added, e)
			}
			// Een job die nu mislukt en het eerst niet deed, of weer lukt.
			if e.Package == "" {
				switch {
				case e.LastResult == protocol.CronFailed && (!known || o.LastResult != protocol.CronFailed):
					results = append(results, result{"node.cron_failed", e})
				case known && o.LastResult == protocol.CronFailed && e.LastResult == protocol.CronOK:
					results = append(results, result{"node.cron_recovered", e})
				}
			}
			if err := q.InsertNodeCron(ctx, store.InsertNodeCronParams{
				NodeID: n.ID, Key: key, Source: e.Source, File: e.File, Line: int32(e.Line), UserName: e.User, Schedule: e.Schedule,
				Command: e.Command, Unit: e.Unit, Description: e.Description, Package: e.Package, Enabled: e.Enabled,
				Clusterforge: e.ClusterForge, LastRunAt: e.LastRun, LastEndAt: e.LastEnd, LastExit: int32Ptr(e.LastExit),
				LastResult: e.LastResult, Runs: int32(e.Runs), Failures: int32(e.Failures), NextRunAt: e.NextRun, FirstSeen: first,
				SubjectID: new(SubjectID(n.ID, key)),
			}); err != nil {
				return err
			}
		}
		if err := q.UpsertCronRead(ctx, store.UpsertCronReadParams{
			NodeID: n.ID, Daemon: res.Daemon, Logging: res.Logging, Journal: res.Journal, Truncated: res.Truncated, SyncedAt: now,
		}); err != nil {
			return err
		}
		if err := s.resultEvents(ctx, q, n, results); err != nil {
			return err
		}
		// De eerste keer is alles nieuw; dat is geen wijziging op de node.
		if !n.ReadBefore {
			return nil
		}
		removed := make([]store.NodeCron, 0, len(oldByKey))
		for _, o := range oldByKey {
			removed = append(removed, o)
		}
		return s.changeEvents(ctx, q, n, added, removed)
	})
}

// resultEvents schrijft een event per job die begon te mislukken of weer
// lukt; ook de eerste keer, want een job die al mislukt is nieuws.
func (s *Service) resultEvents(ctx context.Context, q *store.Queries, n store.ListCronCandidateNodesRow, results []result) error {
	for _, r := range results {
		p := map[string]any{"hostname": n.Hostname, "file": r.entry.File, "user": r.entry.User, "schedule": r.entry.Schedule, "command": r.entry.Command}
		if r.entry.LastExit != nil {
			p["exit"] = *r.entry.LastExit
		}
		if r.entry.LastRun != nil {
			p["run_at"] = r.entry.LastRun.UTC().Format(time.RFC3339)
		}
		if err := s.ev.Write(ctx, q, events.Event{
			Actor: events.System(), SubjectType: "node", SubjectID: n.ID.String(), ClusterID: n.ClusterID, Action: r.action, Payload: p,
		}); err != nil {
			return err
		}
	}
	return nil
}

// result is een job die begon te mislukken of weer lukt.
type result struct {
	action string
	entry  protocol.CronEntry
}

// SubjectID is de vaste uuid van een job op een node, als onderwerp van een
// incident.
func SubjectID(nodeID uuid.UUID, key string) uuid.UUID {
	return uuid.NewSHA1(nodeID, []byte("cron:"+key))
}

func int32Ptr(v *int) *int32 {
	if v == nil {
		return nil
	}
	x := int32(*v)
	return &x
}

// changeEvents schrijft een event per job die bijkwam, verdween of
// veranderde. Een job in hetzelfde bestand die verdween en terugkwam met
// een ander schema of commando, is gewijzigd.
func (s *Service) changeEvents(ctx context.Context, q *store.Queries, n store.ListCronCandidateNodesRow, added []protocol.CronEntry, removed []store.NodeCron) error {
	type change struct {
		action  string
		payload map[string]any
	}
	var changes []change
	slices.SortFunc(removed, func(a, b store.NodeCron) int { return strings.Compare(a.File+a.Command, b.File+b.Command) })
	for _, e := range added {
		i := slices.IndexFunc(removed, func(o store.NodeCron) bool {
			return o.Source == e.Source && o.File == e.File && o.UserName == e.User && (o.Line == int32(e.Line) || o.Command == e.Command || o.Schedule == e.Schedule)
		})
		p := map[string]any{"hostname": n.Hostname, "source": e.Source, "file": e.File, "user": e.User, "schedule": e.Schedule, "command": e.Command}
		if i >= 0 {
			o := removed[i]
			removed = slices.Delete(removed, i, i+1)
			p["old_schedule"], p["old_command"] = o.Schedule, o.Command
			changes = append(changes, change{"node.cron_changed", p})
			continue
		}
		changes = append(changes, change{"node.cron_added", p})
	}
	for _, o := range removed {
		changes = append(changes, change{"node.cron_removed", map[string]any{
			"hostname": n.Hostname, "source": o.Source, "file": o.File, "user": o.UserName, "schedule": o.Schedule, "command": o.Command,
		}})
	}
	if len(changes) > maxEvents {
		files := []string{}
		for _, c := range changes {
			if f, _ := c.payload["file"].(string); !slices.Contains(files, f) {
				files = append(files, f)
			}
		}
		changes = []change{{"node.cron_changed", map[string]any{"hostname": n.Hostname, "count": len(changes), "files": files}}}
	}
	for _, c := range changes {
		if err := s.ev.Write(ctx, q, events.Event{
			Actor: events.System(), SubjectType: "node", SubjectID: n.ID.String(), ClusterID: n.ClusterID, Action: c.action, Payload: c.payload,
		}); err != nil {
			return err
		}
	}
	return nil
}

// Fouten van SetLogging.
var (
	ErrNotFound = errors.New("node niet gevonden of zonder agent")
	// ErrAgentTooOld: de agent kent CronLogging nog niet.
	ErrAgentTooOld = errors.New("agent te oud")
)

// AgentError is een fout van de agent of de verbinding, met een zin voor
// Jonas.
type AgentError struct{ Msg string }

func (e *AgentError) Error() string { return e.Msg }

// SetLogging laat cron op een node het einde en de exitcode van elke job
// loggen, of zet dat terug, en leest de node daarna opnieuw.
func (s *Service) SetLogging(ctx context.Context, actor events.Actor, nodeID uuid.UUID, enable bool) ([]string, error) {
	nodes, err := s.q.ListCronCandidateNodes(ctx)
	if err != nil {
		return nil, err
	}
	i := slices.IndexFunc(nodes, func(n store.ListCronCandidateNodesRow) bool { return n.ID == nodeID })
	if i < 0 {
		return nil, ErrNotFound
	}
	n := nodes[i]
	if n.AgentProtocol < protocol.CronLoggingSince {
		return nil, fmt.Errorf("%w: de agent op %s heeft protocol %d; dit vraagt protocol %d", ErrAgentTooOld, n.Hostname, n.AgentProtocol, protocol.CronLoggingSince)
	}
	rctx, cancel := context.WithTimeout(ctx, s.Timeout)
	defer cancel()
	res, err := s.bus.Cron(rctx, n.ID, protocol.CronRequest{Op: protocol.CronLogging, Enable: enable})
	switch {
	case errors.Is(err, agentbus.ErrAgentOffline):
		return nil, &AgentError{"de agent op " + n.Hostname + " is niet verbonden"}
	case errors.Is(err, agentbus.ErrNoAnswer):
		return nil, &AgentError{"de agent op " + n.Hostname + " antwoordde niet op tijd"}
	case err != nil:
		return nil, &AgentError{err.Error()}
	case res.Error != "":
		return nil, &AgentError{n.Hostname + ": " + res.Error}
	}
	if err := s.ev.Write(ctx, nil, events.Event{
		Actor: actor, SubjectType: "node", SubjectID: n.ID.String(), ClusterID: n.ClusterID, Action: "node.cron_logging",
		Payload: map[string]any{"hostname": n.Hostname, "enabled": enable},
	}); err != nil {
		return nil, err
	}
	if err := s.syncNode(ctx, n); err != nil {
		s.log.Warn("cronjobs opnieuw lezen na de logging mislukt", "node", n.Hostname, "err", err)
	}
	return res.Output, nil
}
