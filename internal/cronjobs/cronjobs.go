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

// Run leest de cronjobs elke Interval, tot ctx afloopt.
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
		for _, e := range res.Entries {
			key := Key(e)
			// Twee gelijke regels in één bestand: de tweede krijgt een
			// eigen sleutel.
			for i := 2; seen[key]; i++ {
				key = fmt.Sprintf("%s-%d", Key(e)[:20], i)
			}
			seen[key] = true
			first := now
			if o, ok := oldByKey[key]; ok {
				first = o.FirstSeen
				delete(oldByKey, key)
			} else {
				added = append(added, e)
			}
			if err := q.InsertNodeCron(ctx, store.InsertNodeCronParams{
				NodeID: n.ID, Key: key, Source: e.Source, File: e.File, Line: int32(e.Line), UserName: e.User, Schedule: e.Schedule,
				Command: e.Command, Unit: e.Unit, Description: e.Description, Package: e.Package, Enabled: e.Enabled,
				Clusterforge: e.ClusterForge, LastRunAt: e.LastRun, LastEndAt: e.LastEnd, LastExit: int32Ptr(e.LastExit),
				LastResult: e.LastResult, Runs: int32(e.Runs), Failures: int32(e.Failures), NextRunAt: e.NextRun, FirstSeen: first,
			}); err != nil {
				return err
			}
		}
		if err := q.UpsertCronRead(ctx, store.UpsertCronReadParams{
			NodeID: n.ID, Daemon: res.Daemon, Logging: res.Logging, Journal: res.Journal, Truncated: res.Truncated, SyncedAt: now,
		}); err != nil {
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
