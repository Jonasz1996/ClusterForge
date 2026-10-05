package drift

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
	"golang.org/x/sync/errgroup"

	"github.com/Jonasz1996/clusterforge/internal/agentbus"
	"github.com/Jonasz1996/clusterforge/internal/deploy"
	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/status"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/internal/templates"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// Statussen van een controle.
const (
	InSync = "in_sync"
	Drift  = "drift"
	Error  = "error"
	// None: de node staat niet in de gewenste staat van zijn cluster.
	None = "none"
)

// Commander stuurt een commando naar de agent van een node; dat is de bus.
type Commander interface {
	Command(ctx context.Context, nodeID uuid.UUID, cmd protocol.Command) (protocol.Result, error)
}

// Service controleert nodes op drift: op de achtergrond met Run, na een
// taak via Kick en meteen met CheckNow. Een controle stuurt alleen
// state.inspect en verandert nooit iets op een node.
type Service struct {
	pool *pgxpool.Pool
	q    *store.Queries
	ev   *events.Writer
	log  *slog.Logger
	bus  Commander
	dep  *deploy.Service
	key  []byte

	// Interval is hoe vaak de scanner alle nodes controleert; 0 zet de
	// controles op de achtergrond uit.
	Interval time.Duration
	// StartDelay is de wachttijd na het starten, zodat de agents eerst
	// opnieuw verbinden.
	StartDelay time.Duration
	// ConfirmDelay: een nieuwe of andere set afwijkingen wordt na deze tijd
	// opnieuw bekeken, en alleen wat beide keren zo is, telt.
	ConfirmDelay time.Duration
	// Timeout is de tijd voor één controle op de achtergrond; NowTimeout
	// die voor Nu controleren.
	Timeout, NowTimeout time.Duration
	// Parallel is het aantal controles tegelijk op de achtergrond.
	Parallel int
	Now      func() time.Time

	work    chan work
	mu      sync.Mutex
	queued  map[uuid.UUID]bool
	started bool
}

type work struct {
	nodeID uuid.UUID
	// first is de eerste controle als dit de bevestiging is.
	first *observation
}

func NewService(pool *pgxpool.Pool, ev *events.Writer, log *slog.Logger, bus Commander, dep *deploy.Service, key []byte) *Service {
	return &Service{
		pool: pool, q: store.New(pool), ev: ev, log: log, bus: bus, dep: dep, key: key,
		Interval: 15 * time.Minute, StartDelay: 2 * time.Minute, ConfirmDelay: 30 * time.Second,
		Timeout: 30 * time.Second, NowTimeout: 20 * time.Second, Parallel: 4, Now: time.Now,
		work: make(chan work, 4096), queued: map[uuid.UUID]bool{},
	}
}

// Run controleert tot ctx afloopt.
func (s *Service) Run(ctx context.Context) {
	if s.Interval <= 0 {
		s.log.Info("driftcontrole op de achtergrond staat uit (CF_DRIFT_INTERVAL=0)")
		return
	}
	s.mu.Lock()
	s.started = true
	s.mu.Unlock()
	var wg sync.WaitGroup
	for range max(s.Parallel, 1) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case w := <-s.work:
					s.mu.Lock()
					delete(s.queued, w.nodeID)
					s.mu.Unlock()
					s.scheduled(ctx, w)
				}
			}
		}()
	}
	wait := s.StartDelay
	for {
		select {
		case <-ctx.Done():
			wg.Wait()
			return
		case <-time.After(wait):
		}
		wait = s.Interval
		if err := s.Kick(ctx, nil); err != nil && ctx.Err() == nil {
			s.log.Error("driftcontrole plannen mislukt", "err", err)
		}
	}
}

// Kick zet de nodes van een cluster in de wachtrij, of zonder cluster alle
// nodes van clusters met een gewenste staat. Zonder lopende scanner doet het
// niets.
func (s *Service) Kick(ctx context.Context, clusterID *uuid.UUID) error {
	s.mu.Lock()
	started := s.started
	s.mu.Unlock()
	if !started {
		return nil
	}
	rows, err := s.q.ListDriftNodes(ctx, store.ListDriftNodesParams{ClusterID: clusterID})
	if err != nil {
		return err
	}
	for _, r := range rows {
		s.enqueue(work{nodeID: r.ID})
	}
	return nil
}

// KickNode zet één node in de wachtrij.
func (s *Service) KickNode(nodeID uuid.UUID) { s.enqueue(work{nodeID: nodeID}) }

func (s *Service) enqueue(w work) {
	s.mu.Lock()
	if !s.started || (w.first == nil && s.queued[w.nodeID]) {
		s.mu.Unlock()
		return
	}
	s.queued[w.nodeID] = true
	s.mu.Unlock()
	select {
	case s.work <- w:
	default:
		s.mu.Lock()
		delete(s.queued, w.nodeID)
		s.mu.Unlock()
	}
}

// observation is wat één controle zag, nog niet opgeslagen.
type observation struct {
	// skip zegt waarom de node nu niet gecontroleerd wordt; er wordt dan
	// niets geschreven.
	skip      string
	status    string
	source    string
	findings  []Finding
	unchecked []Unchecked
	err       string
	revision  int
	version   string
}

// scheduled is een controle op de achtergrond, met bevestiging.
func (s *Service) scheduled(ctx context.Context, w work) {
	rows, err := s.q.ListDriftNodes(ctx, store.ListDriftNodesParams{NodeID: &w.nodeID})
	if err != nil || len(rows) == 0 {
		return
	}
	n := rows[0]
	obs := s.observe(ctx, n, s.Timeout)
	if obs.skip != "" {
		return
	}
	if obs.status == InSync || obs.status == Drift {
		prev, err := s.q.GetDriftCheck(ctx, n.ID)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			s.log.Error("driftcontrole lezen mislukt", "node", n.Hostname, "err", err)
			return
		}
		prevKeys := lastKeys(prev)
		if w.first == nil {
			if s.ConfirmDelay > 0 && !sameKeys(Keys(obs.findings), prevKeys) {
				first := obs
				time.AfterFunc(s.ConfirmDelay, func() { s.enqueue(work{nodeID: n.ID, first: &first}) })
				return
			}
		} else {
			obs.findings = confirmed(prev, w.first.findings, obs.findings)
			if len(obs.findings) == 0 {
				obs.status = InSync
			} else {
				obs.status = Drift
			}
		}
	}
	if err := s.save(ctx, n, obs); err != nil && ctx.Err() == nil {
		s.log.Error("driftcontrole opslaan mislukt", "node", n.Hostname, "err", err)
	}
}

// confirmed houdt alleen een verandering die beide keren zo was: een
// afwijking telt als ze er beide keren was, en een eerdere afwijking
// verdwijnt pas als ze beide keren weg was.
func confirmed(prev store.DriftCheck, first, second []Finding) []Finding {
	in := func(fs []Finding, key string) int {
		return slices.IndexFunc(fs, func(f Finding) bool { return f.Key == key })
	}
	var old []Finding
	_ = json.Unmarshal(prev.Findings, &old)
	out := []Finding{}
	for _, f := range second {
		if in(first, f.Key) >= 0 || in(old, f.Key) >= 0 {
			out = append(out, f)
		}
	}
	for _, f := range first {
		if in(second, f.Key) < 0 && in(old, f.Key) >= 0 {
			out = append(out, f)
		}
	}
	return out
}

func lastKeys(prev store.DriftCheck) []string {
	var fs []Finding
	_ = json.Unmarshal(prev.Findings, &fs)
	return Keys(fs)
}

func sameKeys(a, b []string) bool {
	a, b = slices.Clone(a), slices.Clone(b)
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(a, b)
}

// Skip zegt waarom een node nu niet gecontroleerd wordt, of "".
func Skip(n store.ListDriftNodesRow, busy bool, now time.Time) string {
	switch {
	case n.Lifecycle != store.NodeLifecycleActive:
		return "node is niet actief"
	case !n.HasAgent:
		return "geen agent"
	case n.AgentProtocol < protocol.InspectSince:
		return AgentTooOld
	case n.HeartbeatAt == nil || now.Sub(*n.HeartbeatAt) > status.HeartbeatDown:
		return "agent niet verbonden"
	case busy:
		return "taak bezig"
	}
	return ""
}

// AgentTooOld is de reden voor een agent zonder state.inspect.
const AgentTooOld = "agent te oud voor driftcontrole"

// observe controleert één node, zonder iets op te slaan.
func (s *Service) observe(ctx context.Context, n store.ListDriftNodesRow, timeout time.Duration) observation {
	busy, err := s.q.NodeJobBusy(ctx, store.NodeJobBusyParams{NodeID: &n.ID, ClusterID: n.ClusterID})
	if err != nil {
		return observation{skip: err.Error()}
	}
	if reason := Skip(n, busy, s.Now()); reason != "" {
		return observation{skip: reason}
	}
	exp := s.expected(ctx, n)
	if exp.skip != "" || exp.status != "" {
		return exp.observation
	}
	steps, contents := exp.steps, exp.contents
	obs := exp.observation
	cmd := protocol.Command{
		ID: "drift-" + uuid.NewString(), Action: protocol.CmdInspect, Deadline: time.Now().Add(timeout), Inspect: Request(steps),
	}
	var res protocol.Result
	if len(cmd.Inspect) > 0 {
		cctx, cancel := context.WithTimeout(ctx, timeout)
		res, err = s.bus.Command(cctx, n.ID, cmd)
		cancel()
		switch {
		case errors.Is(err, agentbus.ErrAgentOffline) || errors.Is(err, agentbus.ErrNoAnswer) || ctx.Err() != nil:
			return observation{skip: "agent niet bereikbaar"}
		case err != nil:
			obs.status, obs.err = Error, err.Error()
			return obs
		case !res.OK:
			obs.status, obs.err = Error, "de agent: "+res.Error
			return obs
		}
	}
	aligned, err := Align(steps, res.Observations)
	if err != nil {
		obs.status, obs.err = Error, err.Error()
		return obs
	}
	cmp := compare(s.key, steps, contents, aligned)
	obs.findings, obs.unchecked = cmp.Findings, cmp.Unchecked
	obs.status = InSync
	if len(obs.findings) > 0 {
		obs.status = Drift
	}
	return obs
}

// expectation is wat een node hoort te hebben, of waarom dat niet te zeggen
// is (skip, of status none of error in observation).
type expectation struct {
	observation
	steps    []templates.Step
	contents []*contentWant
}

// expected leest de gewenste staat van de node: de gerenderde template, of
// zijn deel van de baseline.
func (s *Service) expected(ctx context.Context, n store.ListDriftNodesRow) expectation {
	c, err := s.q.GetCluster(ctx, *n.ClusterID)
	if err != nil {
		return expectation{observation: observation{skip: err.Error()}}
	}
	if c.TemplateName == nil {
		b, err := ParseBaseline(c.Spec)
		switch {
		case err != nil:
			return expectation{observation: observation{status: Error, source: "baseline", err: "baseline: " + err.Error()}}
		case b == nil:
			return expectation{observation: observation{skip: "geen gewenste staat"}}
		}
		obs := observation{source: "baseline", revision: int(c.SpecRevision)}
		bn, ok := b.Node(n.ID)
		if !ok {
			obs.status = None
			return expectation{observation: obs}
		}
		steps, contents := bn.Steps()
		return expectation{observation: obs, steps: steps, contents: contents}
	}
	d, err := s.dep.Desired(ctx, *n.ClusterID)
	switch {
	case errors.Is(err, deploy.ErrNoSpec):
		return expectation{observation: observation{skip: "geen gewenste staat"}}
	case err != nil:
		return expectation{observation: observation{status: Error, source: "template", err: "gewenste staat: " + err.Error()}}
	}
	obs := observation{source: "template", revision: d.Revision, version: d.Spec.Template.Version}
	if !d.Has(n.ID) {
		obs.status = None
		return expectation{observation: obs}
	}
	steps, err := d.Render(n.ID)
	if err != nil {
		obs.status, obs.err = Error, "niet te renderen: "+err.Error()
		return expectation{observation: obs}
	}
	return expectation{observation: obs, steps: steps, contents: templateContents(steps)}
}

// save legt een controle vast en schrijft een event bij elke overgang, in
// één transactie onder een slot per node.
func (s *Service) save(ctx context.Context, n store.ListDriftNodesRow, obs observation) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		if err := q.LockDriftNode(ctx, n.ID.String()); err != nil {
			return err
		}
		prev, err := q.GetDriftCheck(ctx, n.ID)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		var prevp *store.DriftCheck
		if err == nil {
			prevp = &prev
		}
		return s.write(ctx, q, n, obs, prevp, s.Now().Truncate(time.Microsecond))
	})
}

// Reevaluate rekent de laatste controles van een cluster opnieuw door met de
// negeerregels van nu, zonder de nodes opnieuw te bekijken. Zo geldt een
// nieuwe of opgeheven regel meteen, met het event dat bij de overgang hoort.
func (s *Service) Reevaluate(ctx context.Context, clusterID uuid.UUID) error {
	rows, err := s.q.ListDriftNodes(ctx, store.ListDriftNodesParams{ClusterID: &clusterID})
	if err != nil {
		return err
	}
	for _, n := range rows {
		err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
			q := store.New(tx)
			if err := q.LockDriftNode(ctx, n.ID.String()); err != nil {
				return err
			}
			prev, err := q.GetDriftCheck(ctx, n.ID)
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			if err != nil {
				return err
			}
			if prev.Status != InSync && prev.Status != Drift {
				return nil
			}
			obs := observation{
				status: prev.Status, source: prev.Source, revision: int(prev.SpecRevision), version: prev.TemplateVersion, unchecked: []Unchecked{},
			}
			_ = json.Unmarshal(prev.Findings, &obs.findings)
			_ = json.Unmarshal(prev.Unchecked, &obs.unchecked)
			return s.write(ctx, q, n, obs, &prev, prev.CheckedAt)
		})
		if err != nil {
			return fmt.Errorf("%s: %w", n.Hostname, err)
		}
	}
	return nil
}

// write slaat obs op tegenover de vorige controle prev (nil als er geen
// is). Alleen de afwijkingen die geen negeerregel dekt, tellen voor de
// status, de vingerafdruk en de events.
func (s *Service) write(ctx context.Context, q *store.Queries, n store.ListDriftNodesRow, obs observation, prev *store.DriftCheck, checkedAt time.Time) error {
	now := s.Now().Truncate(time.Microsecond)
	row := store.UpsertDriftCheckParams{
		NodeID: n.ID, Status: obs.status, Source: obs.source, SpecRevision: int32(obs.revision), //nolint:gosec // een revisie past altijd
		TemplateVersion: obs.version, CheckedAt: checkedAt, Error: obs.err,
	}
	if row.Source == "" {
		row.Source = "template"
	}
	var prevFindings []Finding
	if prev != nil {
		_ = json.Unmarshal(prev.Findings, &prevFindings)
	}
	prevDrift := prev != nil && prev.Fingerprint != ""
	emit := func(action string, payload map[string]any) error {
		payload["hostname"] = n.Hostname
		return s.ev.Write(ctx, q, events.Event{
			Actor: events.System(), SubjectType: "node", SubjectID: n.ID.String(), ClusterID: n.ClusterID,
			Action: action, Payload: payload,
		})
	}

	switch obs.status {
	case Error:
		// De afwijkingen van de laatste geslaagde controle blijven staan.
		row.Findings, row.Unchecked, row.Fingerprint, row.DriftSince = []byte("[]"), []byte("[]"), "", nil
		if prev != nil {
			row.Findings, row.Unchecked, row.Fingerprint, row.DriftSince = prev.Findings, prev.Unchecked, prev.Fingerprint, prev.DriftSince
			if obs.revision == 0 {
				row.SpecRevision, row.TemplateVersion, row.Source = prev.SpecRevision, prev.TemplateVersion, prev.Source
			}
		}
		if prev == nil || prev.Status != Error {
			if err := emit("drift.check_failed", map[string]any{"error": obs.err}); err != nil {
				return err
			}
		}
	case None:
		row.Findings, row.Unchecked = []byte("[]"), []byte("[]")
	default:
		ignores, err := q.ListActiveDriftIgnores(ctx, store.ListActiveDriftIgnoresParams{ClusterID: *n.ClusterID, NodeID: &n.ID, Now: now})
		if err != nil {
			return err
		}
		rules := make([]Ignore, 0, len(ignores))
		for _, r := range ignores {
			rules = append(rules, Ignore{ID: r.ID, NodeID: r.NodeID, Key: r.Key})
		}
		if obs.findings == nil {
			obs.findings = []Finding{}
		}
		keys := markIgnored(obs.findings, rules)
		row.Status = InSync
		if len(keys) > 0 {
			row.Status = Drift
		}
		for i := range obs.findings {
			obs.findings[i].Since = now
			if j := slices.IndexFunc(prevFindings, func(f Finding) bool { return f.Key == obs.findings[i].Key }); j >= 0 && prevDrift {
				obs.findings[i].Since = prevFindings[j].Since
			}
		}
		if row.Findings, err = json.Marshal(obs.findings); err != nil {
			return err
		}
		if obs.unchecked == nil {
			obs.unchecked = []Unchecked{}
		}
		if row.Unchecked, err = json.Marshal(obs.unchecked); err != nil {
			return err
		}
		row.Fingerprint = keysFingerprint(keys)
		switch {
		case len(keys) > 0 && !prevDrift:
			row.DriftSince = &now
			err = emit("drift.detected", map[string]any{
				"count": len(keys), "keys": firstKeys(keys), "source": row.Source, "spec_revision": obs.revision, "template_version": obs.version,
			})
		case len(keys) > 0:
			row.DriftSince = prev.DriftSince
			if row.Fingerprint != prev.Fingerprint {
				added, removed := diffKeys(activeKeys(prevFindings), keys)
				err = emit("drift.changed", map[string]any{"count": len(keys), "added": firstKeys(added), "removed": firstKeys(removed)})
			}
		case prevDrift:
			payload := map[string]any{"removed": firstKeys(activeKeys(prevFindings))}
			if prev.DriftSince != nil {
				payload["duration_seconds"] = int(now.Sub(*prev.DriftSince).Seconds())
			}
			err = emit("drift.resolved", payload)
		}
		if err != nil {
			return err
		}
	}
	return q.UpsertDriftCheck(ctx, row)
}

// keysFingerprint hasht de gesorteerde sleutels; zonder afwijkingen leeg.
func keysFingerprint(keys []string) string {
	if len(keys) == 0 {
		return ""
	}
	k := slices.Clone(keys)
	slices.Sort(k)
	sum := sha256.Sum256([]byte(strings.Join(k, "\n")))
	return hex.EncodeToString(sum[:])
}

// firstKeys houdt een event klein: hoogstens 20 sleutels.
func firstKeys(keys []string) []string {
	if len(keys) > 20 {
		return keys[:20]
	}
	return keys
}

func diffKeys(old, cur []string) (added, removed []string) {
	added, removed = []string{}, []string{}
	for _, k := range cur {
		if !slices.Contains(old, k) {
			added = append(added, k)
		}
	}
	for _, k := range old {
		if !slices.Contains(cur, k) {
			removed = append(removed, k)
		}
	}
	return added, removed
}

// CheckNow controleert de nodes meteen, zonder bevestiging, allemaal
// tegelijk. Zonder nodeID alle nodes van het cluster.
func (s *Service) CheckNow(ctx context.Context, clusterID uuid.UUID, nodeID *uuid.UUID) error {
	rows, err := s.q.ListDriftNodes(ctx, store.ListDriftNodesParams{ClusterID: &clusterID, NodeID: nodeID})
	if err != nil {
		return err
	}
	var g errgroup.Group
	for _, n := range rows {
		g.Go(func() error {
			obs := s.observe(ctx, n, s.NowTimeout)
			if obs.skip != "" {
				return nil
			}
			if err := s.save(ctx, n, obs); err != nil {
				return fmt.Errorf("%s: %w", n.Hostname, err)
			}
			return nil
		})
	}
	return g.Wait()
}
