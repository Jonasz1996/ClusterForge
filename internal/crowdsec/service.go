package crowdsec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/jobs"
	"github.com/Jonasz1996/clusterforge/internal/noderun"
	"github.com/Jonasz1996/clusterforge/internal/secrets"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// Kind is de taak die CrowdSec op een node toepast of verwijdert.
const Kind = "crowdsec.apply"

// AlertRetention is hoe lang alerts bewaard blijven.
const AlertRetention = 90 * 24 * time.Hour

// RetryFailed is hoe lang een mislukte automatische apply wacht.
const RetryFailed = time.Hour

// Bus stuurt CrowdSec- en runverzoeken naar een agent.
type Bus interface {
	CrowdSec(ctx context.Context, nodeID uuid.UUID, req protocol.CrowdSecRequest) (protocol.CrowdSecResponse, error)
	noderun.Bus
}

// Service beheert CrowdSec op de nodes.
type Service struct {
	pool   *pgxpool.Pool
	ev     *events.Writer
	runner *jobs.Runner
	bus    Bus
	log    *slog.Logger
	Run    *noderun.Runner
	// PollEvery is hoe vaak de alerts en bans opgehaald worden;
	// StatusEvery hoe vaak de stand.
	PollEvery   time.Duration
	StatusEvery time.Duration
	Now         func() time.Time
	// Box versleutelt de enroll-sleutel van de CrowdSec Console; nil
	// zonder masterkey.
	Box *secrets.Box

	// HTTP haalt publieke lijsten op en stuurt meldingen; nil is een
	// gewone client.
	HTTP *http.Client

	tick      sync.Mutex
	lastPrune time.Time

	listMu   sync.Mutex
	listKick chan struct{}
	notifyMu sync.Mutex
	notified map[string]time.Time
	// alertCursor is de hoogste alert-id die al gemeld is.
	alertCursor int64
}

func NewService(pool *pgxpool.Pool, ev *events.Writer, runner *jobs.Runner, bus Bus, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	s := &Service{
		pool: pool, ev: ev, runner: runner, bus: bus, log: log, Run: &noderun.Runner{Bus: bus},
		PollEvery: time.Minute, StatusEvery: 5 * time.Minute, Now: time.Now,
		listKick: make(chan struct{}, 1), notified: map[string]time.Time{}, alertCursor: -1,
	}
	if runner != nil {
		runner.Register(Kind, s.apply)
	}
	return s
}

// FieldError: een ongeldige invoer.
type FieldError struct{ Msg string }

func (e *FieldError) Error() string { return e.Msg }

func (s *Service) event(ctx context.Context, actor events.Actor, n *Node, action string, payload map[string]any) {
	if s.ev == nil {
		return
	}
	e := events.Event{Actor: actor, SubjectType: "crowdsec", SubjectID: "settings", Action: action, Payload: payload}
	if n != nil {
		e.SubjectType, e.SubjectID, e.ClusterID = "node", n.NodeID.String(), n.ClusterID
		if e.Payload == nil {
			e.Payload = map[string]any{}
		}
		e.Payload["hostname"] = n.Hostname
	}
	if err := s.ev.Write(ctx, nil, e); err != nil {
		s.log.Warn("event schrijven mislukt", "action", action, "err", err)
	}
}

// UpdateSettings bewaart de instellingen.
func (s *Service) UpdateSettings(ctx context.Context, actor events.Actor, userID *uuid.UUID, in Settings) (Settings, error) {
	switch in.ShareBans {
	case "off", "cluster", "all":
	default:
		return Settings{}, &FieldError{"bans delen is off, cluster of all"}
	}
	in.Whitelist = cleanList(in.Whitelist)
	if err := ValidWhitelist(in.Whitelist); err != nil {
		return Settings{}, &FieldError{err.Error()}
	}
	if len(in.Whitelist) > 200 {
		return Settings{}, &FieldError{"hoogstens 200 adressen in de whitelist"}
	}
	if _, err := s.pool.Exec(ctx, `UPDATE crowdsec_settings SET auto_apply = $1, bouncer_default = $2, share_bans = $3,
		whitelist = $4, updated_at = now(), updated_by = $5`, in.AutoApply, in.BouncerDefault, in.ShareBans, in.Whitelist, userID); err != nil {
		return Settings{}, err
	}
	s.event(ctx, actor, nil, "crowdsec.settings_changed", map[string]any{
		"auto_apply": in.AutoApply, "bouncer_default": in.BouncerDefault, "share_bans": in.ShareBans, "whitelist": in.Whitelist,
	})
	// Een andere whitelist verandert wat de lijsten mogen blokkeren.
	if err := s.RecomputeAll(ctx); err != nil {
		return Settings{}, err
	}
	s.kickLists()
	return s.Settings(ctx)
}

func cleanList(in []string) []string {
	out := []string{}
	for _, v := range in {
		if v = strings.TrimSpace(v); v != "" && !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	return out
}

// Enable zet CrowdSec aan (of weer aan) op de nodes. Het plan volgt bij de
// volgende ronde van de poller; Tick doet die meteen.
func (s *Service) Enable(ctx context.Context, actor events.Actor, ids []uuid.UUID, bouncer *bool) ([]string, error) {
	set, err := s.Settings(ctx)
	if err != nil {
		return nil, err
	}
	b := set.BouncerDefault
	if bouncer != nil {
		b = *bouncer
	}
	var skipped []string
	for _, id := range ids {
		n, err := s.Node(ctx, id)
		if err != nil {
			return nil, err
		}
		if !n.Supported {
			skipped = append(skipped, n.Hostname+": agent te oud (protocol 28 nodig; Nodes → Agents bijwerken)")
			continue
		}
		if _, err := s.pool.Exec(ctx, `INSERT INTO crowdsec_nodes (node_id, managed, bouncer) VALUES ($1, true, $2)
			ON CONFLICT (node_id) DO UPDATE SET managed = true, apply_failed_at = NULL`, id, b); err != nil {
			return nil, err
		}
		s.event(ctx, actor, &n, "crowdsec.node_managed", nil)
	}
	return skipped, nil
}

// Disable stopt het beheer; CrowdSec blijft op de node staan.
func (s *Service) Disable(ctx context.Context, actor events.Actor, ids []uuid.UUID) error {
	for _, id := range ids {
		n, err := s.Node(ctx, id)
		if err != nil {
			return err
		}
		if _, err := s.pool.Exec(ctx, `UPDATE crowdsec_nodes SET managed = false WHERE node_id = $1`, id); err != nil {
			return err
		}
		s.event(ctx, actor, &n, "crowdsec.node_unmanaged", nil)
	}
	return nil
}

// Configure bewaart de keuzes voor een node.
func (s *Service) Configure(ctx context.Context, actor events.Actor, id uuid.UUID, bouncer bool, extra, disabled []string) (Node, error) {
	extra, disabled = cleanList(extra), cleanList(disabled)
	for _, k := range append(slices.Clone(extra), disabled...) {
		if _, ok := ProfileByKey(k); !ok {
			return Node{}, &FieldError{fmt.Sprintf("onbekend profiel %q", k)}
		}
	}
	n, err := s.Node(ctx, id)
	if err != nil {
		return Node{}, err
	}
	if !n.Enabled {
		return Node{}, &FieldError{"zet CrowdSec eerst aan op " + n.Hostname}
	}
	if _, err := s.pool.Exec(ctx, `UPDATE crowdsec_nodes SET bouncer = $2, extra_profiles = $3, disabled_profiles = $4,
		apply_failed_at = NULL WHERE node_id = $1`, id, bouncer, extra, disabled); err != nil {
		return Node{}, err
	}
	s.event(ctx, actor, &n, "crowdsec.node_configured", map[string]any{"bouncer": bouncer, "extra_profiles": extra, "disabled_profiles": disabled})
	if err := s.refreshPlan(ctx, id); err != nil {
		return Node{}, err
	}
	return s.Node(ctx, id)
}

// refreshPlan rekent het plan van een node opnieuw uit met de bewaarde stand.
func (s *Service) refreshPlan(ctx context.Context, id uuid.UUID) error {
	n, err := s.Node(ctx, id)
	if err != nil || n.Status == nil {
		return err
	}
	set, err := s.Settings(ctx)
	if err != nil {
		return err
	}
	return s.savePlan(ctx, n, Build(n.Signals(), n.Config(), set.Whitelist))
}

func (s *Service) savePlan(ctx context.Context, n Node, p Plan) error {
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `UPDATE crowdsec_nodes SET plan = $2, plan_hash = $3 WHERE node_id = $1`, n.NodeID, b, p.Hash)
	return err
}

type applyParams struct {
	NodeID   uuid.UUID `json:"node_id"`
	Hostname string    `json:"hostname"`
	Remove   bool      `json:"remove,omitempty"`
}

// Apply start de taak die het plan toepast (of CrowdSec verwijdert).
func (s *Service) Apply(ctx context.Context, actor events.Actor, id uuid.UUID, remove bool) (uuid.UUID, error) {
	n, err := s.Node(ctx, id)
	if err != nil {
		return uuid.Nil, err
	}
	switch {
	case !n.Supported:
		return uuid.Nil, &FieldError{n.Hostname + ": agent te oud (protocol 28 nodig; Nodes → Agents bijwerken)"}
	case !n.Online:
		return uuid.Nil, &FieldError{n.Hostname + " is offline"}
	case !remove && !n.Enabled:
		return uuid.Nil, &FieldError{"zet CrowdSec eerst aan op " + n.Hostname}
	case n.ApplyRunning:
		return uuid.Nil, &FieldError{"op " + n.Hostname + " loopt al een CrowdSec-taak"}
	}
	title := "CrowdSec toepassen op " + n.Hostname
	if remove {
		title = "CrowdSec verwijderen van " + n.Hostname
	}
	j, err := s.runner.Enqueue(ctx, jobs.Spec{Kind: Kind, Title: title, NodeID: &n.NodeID, ClusterID: n.ClusterID, Actor: actor,
		Params: applyParams{NodeID: n.NodeID, Hostname: n.Hostname, Remove: remove}})
	if err != nil {
		return uuid.Nil, err
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO crowdsec_nodes (node_id, managed, bouncer, apply_job_id) VALUES ($1, $2, false, $3)
		ON CONFLICT (node_id) DO UPDATE SET apply_job_id = $3`, n.NodeID, !remove, j.ID); err != nil {
		return uuid.Nil, err
	}
	s.runner.Kick()
	return j.ID, nil
}

func (s *Service) apply(ctx context.Context, j *jobs.Job) error {
	var p applyParams
	if err := j.Decode(&p); err != nil {
		return err
	}
	script, hash, plan, err := s.prepare(ctx, p)
	if err != nil {
		return err
	}
	if err := j.Step(ctx, "plan", func(ctx context.Context, st *jobs.Step) error {
		if p.Remove {
			st.Logf("CrowdSec en de bouncer gaan van %s (apt-get purge); de alerts in ClusterForge blijven.", p.Hostname)
			return nil
		}
		logPlan(st, plan)
		return nil
	}); err != nil {
		return err
	}
	spec := protocol.RunSpec{ID: "crowdsec-" + j.ID.String(), Kind: protocol.RunScript, Title: j.Title, TimeoutSeconds: 1800, Script: script}
	err = j.Step(ctx, p.Hostname+": script", func(ctx context.Context, st *jobs.Step) error {
		res, err := s.Run.Do(ctx, st, p.NodeID, spec)
		if err != nil {
			return err
		}
		if res.State != protocol.RunOK {
			return errors.New(noderun.Describe(res))
		}
		return nil
	})
	n, nerr := s.Node(context.WithoutCancel(ctx), p.NodeID)
	if nerr != nil {
		return errors.Join(err, nerr)
	}
	actor := events.System()
	if j.RequestedBy != nil {
		actor = events.User(*j.RequestedBy)
	}
	bg := context.WithoutCancel(ctx)
	switch {
	case err != nil:
		_, _ = s.pool.Exec(bg, `UPDATE crowdsec_nodes SET apply_error = $2, apply_failed_at = now() WHERE node_id = $1`, p.NodeID, err.Error())
		s.event(bg, actor, &n, "crowdsec.apply_failed", map[string]any{"job_id": j.ID.String(), "error": err.Error(), "remove": p.Remove})
		return err
	case p.Remove:
		_, _ = s.pool.Exec(bg, `UPDATE crowdsec_nodes SET managed = false, applied_hash = '', plan_hash = '', plan = NULL, status = NULL,
			status_at = NULL, apply_error = '', console_hash = '', console_enrolled_at = NULL, console_error = '', console_failed_at = NULL
			WHERE node_id = $1`, p.NodeID)
		_, _ = s.pool.Exec(bg, `DELETE FROM crowdsec_decisions WHERE node_id = $1`, p.NodeID)
		s.event(bg, actor, &n, "crowdsec.removed", map[string]any{"job_id": j.ID.String()})
	default:
		_, _ = s.pool.Exec(bg, `UPDATE crowdsec_nodes SET applied_hash = $2, applied_at = now(), apply_error = '', apply_failed_at = NULL,
			status_at = NULL WHERE node_id = $1`, p.NodeID, hash)
		s.event(bg, actor, &n, "crowdsec.applied", map[string]any{"job_id": j.ID.String(), "plan": hash})
	}
	return nil
}

// prepare maakt het script voor de taak; een herhaalde poging rekent het
// opnieuw uit.
func (s *Service) prepare(ctx context.Context, p applyParams) (script, hash string, plan Plan, err error) {
	if p.Remove {
		return RemoveScript(), "", Plan{}, nil
	}
	n, err := s.Node(ctx, p.NodeID)
	if err != nil {
		return "", "", Plan{}, err
	}
	if n.Status == nil {
		if err := s.status(ctx, n); err != nil {
			return "", "", Plan{}, err
		}
		if n, err = s.Node(ctx, p.NodeID); err != nil {
			return "", "", Plan{}, err
		}
	}
	set, err := s.Settings(ctx)
	if err != nil {
		return "", "", Plan{}, err
	}
	plan = Build(n.Signals(), n.Config(), set.Whitelist)
	if err := s.savePlan(ctx, n, plan); err != nil {
		return "", "", Plan{}, err
	}
	return Script(plan), plan.Hash, plan, nil
}

type logger interface {
	Logf(format string, args ...any)
}

func logPlan(st logger, plan Plan) {
	for _, pp := range plan.Profiles {
		line := fmt.Sprintf("%s: %s", pp.Name, map[string]string{"auto": "herkend", "extra": "zelf aangezet", "off": "uitgezet"}[pp.Origin])
		if pp.Reason != "" {
			line += " (" + pp.Reason + ")"
		}
		st.Logf("%s", line)
		for _, src := range pp.Sources {
			st.Logf("    logs: %s", src)
		}
		if pp.Note != "" {
			st.Logf("    let op: %s", pp.Note)
		}
	}
	st.Logf("collections: %s", strings.Join(plan.Collections, ", "))
	st.Logf("bouncer: %v; whitelist: %s", plan.Bouncer, strings.Join(plan.Whitelist, ", "))
}

// Loop haalt elke PollEvery de stand, de alerts en de bans op, past
// gewijzigde plannen toe en deelt bans.
func (s *Service) Loop(ctx context.Context) {
	go s.ListLoop(ctx)
	t := time.NewTicker(s.PollEvery)
	defer t.Stop()
	for {
		if err := s.Tick(ctx); err != nil && ctx.Err() == nil {
			s.log.Warn("CrowdSec-ronde mislukt", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Tick is één ronde van de poller.
func (s *Service) Tick(ctx context.Context) error {
	s.tick.Lock()
	defer s.tick.Unlock()
	set, err := s.Settings(ctx)
	if err != nil {
		return err
	}
	nodes, err := s.Nodes(ctx)
	if err != nil {
		return err
	}
	now := s.Now()
	var fresh []sharedBan
	// De enroll-sleutel wordt pas ontsleuteld als een node hem nodig heeft.
	consoleKey, consoleErr := "", error(nil)
	consoleTried := false
	for i, n := range nodes {
		if !n.Enabled || !n.Managed || !n.Supported || !n.Online {
			continue
		}
		if n.StatusAt == nil || now.Sub(*n.StatusAt) >= s.StatusEvery {
			if err := s.status(ctx, n); err != nil {
				s.log.Debug("CrowdSec-stand ophalen mislukt", "node", n.Hostname, "err", err)
				continue
			}
			if n, err = s.Node(ctx, n.NodeID); err != nil {
				return err
			}
			nodes[i] = n
		}
		if n.Status != nil {
			plan := Build(n.Signals(), n.Config(), set.Whitelist)
			if plan.Hash != n.PlanHash {
				if err := s.savePlan(ctx, n, plan); err != nil {
					return err
				}
				n.PlanHash = plan.Hash
			}
			if set.AutoApply && n.Pending() && !n.ApplyRunning && (n.ApplyFailedAt == nil || now.Sub(*n.ApplyFailedAt) >= RetryFailed) {
				if _, err := s.Apply(ctx, events.System(), n.NodeID, false); err != nil {
					s.log.Warn("CrowdSec automatisch toepassen mislukt", "node", n.Hostname, "err", err)
				}
			}
		}
		if !set.ConsoleOff && n.wantsConsole(set.consoleKeyHash) && (n.ConsoleFailedAt == nil || now.Sub(*n.ConsoleFailedAt) >= RetryFailed) {
			if !consoleTried {
				consoleTried = true
				consoleKey, consoleErr = s.consoleKey(set)
			}
			if consoleErr != nil {
				_, _ = s.pool.Exec(ctx, `UPDATE crowdsec_nodes SET console_error = $2, console_failed_at = now() WHERE node_id = $1`, n.NodeID, consoleErr.Error())
			} else if err := s.enroll(ctx, n, consoleKey, set.consoleKeyHash); err != nil {
				s.log.Warn("CrowdSec Console aanmelden mislukt", "node", n.Hostname, "err", err)
			}
		}
		if n.Status != nil && n.Status.Installed {
			bans, err := s.poll(ctx, n)
			if err != nil {
				s.log.Debug("CrowdSec-alerts ophalen mislukt", "node", n.Hostname, "err", err)
				continue
			}
			fresh = append(fresh, bans...)
		}
	}
	if set.ShareBans != "off" && len(fresh) > 0 {
		s.share(ctx, set.ShareBans, nodes, fresh)
	}
	if now.Sub(s.lastPrune) > time.Hour {
		s.lastPrune = now
		if _, err := s.pool.Exec(ctx, `DELETE FROM crowdsec_alerts WHERE started_at < $1`, now.Add(-AlertRetention)); err != nil {
			return err
		}
		if _, err := s.pool.Exec(ctx, `DELETE FROM crowdsec_shared WHERE until < $1`, now); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) request(ctx context.Context, n Node, req protocol.CrowdSecRequest) (protocol.CrowdSecResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, protocol.CrowdSecRequestTimeout)
	defer cancel()
	res, err := s.bus.CrowdSec(ctx, n.NodeID, req)
	if err == nil && res.Error != "" {
		err = errors.New(res.Error)
	}
	return res, err
}

// status haalt de stand van een node en bewaart hem.
func (s *Service) status(ctx context.Context, n Node) error {
	res, err := s.request(ctx, n, protocol.CrowdSecRequest{Op: protocol.CrowdSecOpStatus, Probes: AllProbes()})
	if err != nil {
		_, _ = s.pool.Exec(ctx, `UPDATE crowdsec_nodes SET poll_error = $2 WHERE node_id = $1`, n.NodeID, "stand: "+err.Error())
		return err
	}
	b, err := json.Marshal(res.Status)
	if err != nil {
		return err
	}
	applied := n.AppliedHash
	if st := res.Status; st != nil {
		if st.Applied != nil {
			applied = st.Applied.Hash
		} else if !st.Installed {
			applied = ""
		}
	}
	_, err = s.pool.Exec(ctx, `UPDATE crowdsec_nodes SET status = $2, status_at = now(), applied_hash = $3 WHERE node_id = $1`, n.NodeID, b, applied)
	return err
}

// sharedBan is een nieuwe ban van een scenario op een node.
type sharedBan struct {
	node     Node
	scope    string
	value    string
	scenario string
	until    time.Time
}

// poll haalt de alerts en bans van een node en bewaart ze. Het geeft de
// bans van scenario's terug die nog niet gedeeld zijn.
func (s *Service) poll(ctx context.Context, n Node) ([]sharedBan, error) {
	since := s.Now().Add(-time.Hour)
	if n.lastAlertAt != nil {
		since = n.lastAlertAt.Add(-2 * time.Minute)
	}
	res, err := s.request(ctx, n, protocol.CrowdSecRequest{Op: protocol.CrowdSecOpPoll, Since: since})
	if err != nil {
		_, _ = s.pool.Exec(ctx, `UPDATE crowdsec_nodes SET poll_error = $2, poll_at = now() WHERE node_id = $1`, n.NodeID, err.Error())
		return nil, err
	}
	p := res.Poll
	if p == nil {
		return nil, errors.New("leeg antwoord")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	last := n.lastAlertAt
	for _, a := range p.Alerts {
		if _, err := tx.Exec(ctx, `INSERT INTO crowdsec_alerts (node_id, alert_id, scenario, message, scope, value, country, as_name,
			events, decisions, simulated, started_at, stopped_at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
			ON CONFLICT (node_id, alert_id, started_at) DO NOTHING`,
			n.NodeID, a.ID, a.Scenario, a.Message, a.Scope, a.Value, a.Country, a.AS, a.Events, a.Decisions, a.Simulated,
			a.StartedAt, a.StoppedAt); err != nil {
			return nil, err
		}
		if last == nil || a.StoppedAt.After(*last) {
			t := a.StoppedAt
			last = &t
		}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM crowdsec_decisions WHERE node_id = $1`, n.NodeID); err != nil {
		return nil, err
	}
	var fresh []sharedBan
	for _, d := range p.Decisions {
		if strings.HasPrefix(d.Scenario, protocol.CrowdSecListPrefix) {
			continue
		}
		if _, err := tx.Exec(ctx, `INSERT INTO crowdsec_decisions (node_id, decision_id, origin, type, scope, value, scenario, until, country, as_name)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10) ON CONFLICT DO NOTHING`,
			n.NodeID, d.ID, d.Origin, d.Type, d.Scope, d.Value, d.Scenario, d.Until, d.Country, d.AS); err != nil {
			return nil, err
		}
		if strings.EqualFold(d.Origin, "crowdsec") && d.Type == "ban" {
			fresh = append(fresh, sharedBan{node: n, scope: d.Scope, value: d.Value, scenario: d.Scenario, until: d.Until})
		}
	}
	var lists []byte
	if p.Lists != nil {
		if lists, err = json.Marshal(p.Lists); err != nil {
			return nil, err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE crowdsec_nodes SET poll_at = now(), poll_error = '', blocklist = $2, last_alert_at = $3, lists = $4 WHERE node_id = $1`,
		n.NodeID, p.Blocklist, last, lists); err != nil {
		return nil, err
	}
	return fresh, tx.Commit(ctx)
}

var reasonUnsafe = regexp.MustCompile(`[^\pL\pN @:._/+-]`)

// reason maakt een reden die cscli aanneemt.
func reason(s string) string {
	s = reasonUnsafe.ReplaceAllString(s, "-")
	if len(s) > 120 {
		s = s[:120]
	}
	return s
}

// duration is de resterende duur in hele minuten, voor cscli.
func duration(until, now time.Time) string {
	m := int(math.Ceil(until.Sub(now).Minutes()))
	if m < 1 {
		m = 1
	}
	return fmt.Sprintf("%dm", m)
}

// share zet nieuwe bans van een scenario op de andere nodes.
func (s *Service) share(ctx context.Context, mode string, nodes []Node, bans []sharedBan) {
	now := s.Now()
	for _, b := range bans {
		if !b.until.After(now.Add(time.Minute)) {
			continue
		}
		tag, err := s.pool.Exec(ctx, `INSERT INTO crowdsec_shared (scope, value, source_node, scenario, until) VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (scope, value) DO UPDATE SET source_node = $3, scenario = $4, until = $5, shared_at = now()
			WHERE crowdsec_shared.until < now()`, b.scope, b.value, b.node.NodeID, b.scenario, b.until)
		if err != nil {
			s.log.Warn("ban delen mislukt", "err", err)
			continue
		}
		if tag.RowsAffected() == 0 {
			continue // al gedeeld
		}
		d := protocol.CrowdSecDecision{Scope: b.scope, Value: b.value, Type: "ban", Duration: duration(b.until, now),
			Reason: reason("cf:" + b.scenario + "@" + b.node.Hostname)}
		if d.Validate() != nil {
			continue
		}
		var to []string
		for _, n := range nodes {
			if n.NodeID == b.node.NodeID || !n.Enabled || !n.Managed || !n.Supported || !n.Online || n.Status == nil || !n.Status.Installed {
				continue
			}
			if mode == "cluster" && (n.ClusterID == nil || b.node.ClusterID == nil || *n.ClusterID != *b.node.ClusterID) {
				continue
			}
			if _, err := s.request(ctx, n, protocol.CrowdSecRequest{Op: protocol.CrowdSecOpDecide, Decide: &d}); err != nil {
				s.log.Warn("ban delen mislukt", "node", n.Hostname, "value", b.value, "err", err)
				continue
			}
			to = append(to, n.Hostname)
		}
		if len(to) > 0 {
			bn := b.node
			s.event(ctx, events.System(), &bn, "crowdsec.ban_shared", map[string]any{
				"value": b.value, "scenario": b.scenario, "until": b.until, "to": to,
			})
		}
	}
}

// BanTarget zegt waar een handmatige ban komt.
type BanTarget struct {
	NodeIDs   []uuid.UUID
	ClusterID *uuid.UUID
	// Central zet het IP ook in de eigen blocklist: dan blijft het, ook op
	// nodes die later bijkomen. Permanent: zonder einddatum.
	Central   bool
	Permanent bool
	UserID    *uuid.UUID
}

// BanResult zegt per node hoe het ging.
type BanResult struct {
	Count  int      `json:"count"`
	Nodes  []string `json:"nodes"`
	Errors []string `json:"errors"`
}

// Ban zet (of met d.Delete: haalt) een ban op de gekozen nodes; zonder
// keuze op alle beheerde nodes met CrowdSec.
func (s *Service) Ban(ctx context.Context, actor events.Actor, d protocol.CrowdSecDecision, target BanTarget) (BanResult, error) {
	if err := d.Validate(); err != nil {
		return BanResult{}, &FieldError{err.Error()}
	}
	nodes, err := s.Nodes(ctx)
	if err != nil {
		return BanResult{}, err
	}
	res := BanResult{Nodes: []string{}, Errors: []string{}}
	for _, n := range nodes {
		if !n.Enabled || n.Status == nil || !n.Status.Installed {
			continue
		}
		switch {
		case len(target.NodeIDs) > 0:
			if !slices.Contains(target.NodeIDs, n.NodeID) {
				continue
			}
		case target.ClusterID != nil:
			if n.ClusterID == nil || *n.ClusterID != *target.ClusterID {
				continue
			}
		}
		if !n.Supported || !n.Online {
			res.Errors = append(res.Errors, n.Hostname+": offline of agent te oud")
			continue
		}
		r, err := s.request(ctx, n, protocol.CrowdSecRequest{Op: protocol.CrowdSecOpDecide, Decide: &d})
		if err != nil {
			res.Errors = append(res.Errors, n.Hostname+": "+err.Error())
			continue
		}
		res.Count += r.Count
		res.Nodes = append(res.Nodes, n.Hostname)
	}
	if d.Delete {
		// Ook uit de eigen en de automatische lijst, anders komt het IP
		// bij de volgende ronde terug.
		for _, slug := range []string{"eigen", "auto"} {
			l, err := s.listBySlug(ctx, slug)
			if err != nil {
				return res, err
			}
			if _, err := s.ChangeEntries(ctx, actor, target.UserID, l.ID, nil, []string{d.Value}, false); err != nil {
				return res, err
			}
		}
		if _, err := s.pool.Exec(ctx, `INSERT INTO crowdsec_offenders (value, offenses, last_at) VALUES ($1, 0, now())
			ON CONFLICT (value) DO UPDATE SET last_at = now()`, d.Value); err != nil {
			return res, err
		}
	} else if target.Central {
		l, err := s.listBySlug(ctx, "eigen")
		if err != nil {
			return res, err
		}
		in := EntryInput{Value: d.Value, Reason: d.Reason}
		if !target.Permanent {
			in.ExpiresIn, _ = time.ParseDuration(d.Duration)
		}
		diff, err := s.ChangeEntries(ctx, actor, target.UserID, l.ID, []EntryInput{in}, nil, false)
		if err != nil {
			return res, err
		}
		if len(diff.Refused) > 0 {
			res.Errors = append(res.Errors, "niet in de centrale lijst: "+diff.Refused[0].Why)
		}
	}
	if d.Delete {
		if _, err := s.pool.Exec(ctx, `DELETE FROM crowdsec_decisions WHERE value = $1`, d.Value); err != nil {
			return res, err
		}
		if _, err := s.pool.Exec(ctx, `DELETE FROM crowdsec_shared WHERE value = $1`, d.Value); err != nil {
			return res, err
		}
	}
	action := "crowdsec.ban_added"
	if d.Delete {
		action = "crowdsec.ban_removed"
	}
	s.event(ctx, actor, nil, action, map[string]any{
		"scope": d.Scope, "value": d.Value, "duration": d.Duration, "reason": d.Reason, "nodes": res.Nodes, "errors": res.Errors,
		"central": target.Central, "permanent": target.Permanent,
	})
	if set, err := s.Settings(ctx); err == nil && set.Notify.ManualBans {
		title := "IP geblokkeerd: " + d.Value
		if d.Delete {
			title = "IP gedeblokkeerd: " + d.Value
		}
		s.notify(ctx, title, fmt.Sprintf("op %d nodes; %s", len(res.Nodes), d.Reason), "info")
	}
	return res, nil
}

// Refresh haalt de stand van een node meteen op.
func (s *Service) Refresh(ctx context.Context, id uuid.UUID) (Node, error) {
	n, err := s.Node(ctx, id)
	if err != nil {
		return Node{}, err
	}
	if !n.Supported || !n.Online {
		return Node{}, &FieldError{n.Hostname + ": offline of agent te oud"}
	}
	if !n.Enabled {
		return Node{}, &FieldError{"zet CrowdSec eerst aan op " + n.Hostname}
	}
	if err := s.status(ctx, n); err != nil {
		return Node{}, &FieldError{n.Hostname + ": " + err.Error()}
	}
	if err := s.refreshPlan(ctx, id); err != nil {
		return Node{}, err
	}
	return s.Node(ctx, id)
}
