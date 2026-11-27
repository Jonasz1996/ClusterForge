// Package upkeep laat de agents het periodieke onderhoud doen (fase 46):
// schijfruimte vrijhouden (systeemlogboek, logrotate, oude logs, apt-cache,
// crashbestanden, tijdelijke bestanden, Docker-images, bouwcache en te grote
// containerlogs) en de databases onderhouden (MariaDB/Galera ANALYZE en
// OPTIMIZE, PostgreSQL/Patroni VACUUM ANALYZE en REINDEX CONCURRENTLY).
//
// Afgesproken met de andere nodes: een cluster doet zijn beurt als één taak
// met het clusterslot, één node tegelijk, eerst de nodes zonder VIP of
// primary, dan de primary, de VIP-houder als laatste. Na elke node wacht de
// taak tot hij gezond is. Zwaar databasewerk slaat een node over als een
// andere node van het cluster niet verbonden is, en Galera alleen als het
// volledig en Synced is: het onderhoud breekt het quorum nooit.
//
// Alles loopt via scripttaken (runs, protocol 25): de agents hoeven niet
// bijgewerkt te worden.
package upkeep

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Jonasz1996/clusterforge/internal/databases"
	"github.com/Jonasz1996/clusterforge/internal/diskwatch"
	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/health"
	"github.com/Jonasz1996/clusterforge/internal/jobs"
	"github.com/Jonasz1996/clusterforge/internal/noderun"
	"github.com/Jonasz1996/clusterforge/internal/planner"
	"github.com/Jonasz1996/clusterforge/internal/status"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/internal/updates"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// Kind is de taak.
const Kind = "upkeep.run"

// Soorten beleid.
const (
	KindEnv        = "env"
	KindStandalone = "standalone"
	KindCluster    = "cluster"
)

// Modes van een cluster.
const (
	ModeInherit = "inherit"
	ModeOwn     = "own"
	ModeOff     = "off"
)

// Envs zijn de omgevingen.
var Envs = []string{string(store.EnvironmentLab), string(store.EnvironmentTest), string(store.EnvironmentProd)}

// ScopeStandalone is het beleid van de nodes zonder cluster.
const ScopeStandalone = "standalone"

// EnvScope en ClusterScope zijn de sleutels van een beleid.
func EnvScope(env string) string               { return "env:" + env }
func ClusterScope(id uuid.UUID) string         { return "cluster:" + id.String() }
func urgentScope(node uuid.UUID) string        { return "urgent:" + node.String() }
func isEnvScope(key string) (string, bool)     { return strings.CutPrefix(key, "env:") }
func isClusterScope(key string) (string, bool) { return strings.CutPrefix(key, "cluster:") }

// UrgentEvery is hoe vaak een node hoogstens een spoedbeurt krijgt.
const UrgentEvery = 6 * time.Hour

// Keep is hoe lang het logboek bewaard blijft.
const Keep = 180 * 24 * time.Hour

// ErrNotFound: onbekend beleid.
var ErrNotFound = errors.New("onbekend beleid")

// ConflictError: het kan nu niet.
type ConflictError struct{ Msg string }

func (e *ConflictError) Error() string { return e.Msg }

// Service is het automatische onderhoud.
type Service struct {
	pool   *pgxpool.Pool
	q      *store.Queries
	ev     *events.Writer
	runner *jobs.Runner
	log    *slog.Logger
	Run    *noderun.Runner
	Gate   *health.Gate
	// Nodes geeft de nodes met een agent (updates.Overview).
	Nodes func(ctx context.Context) ([]updates.Node, error)
	// DB geeft de databaseclusters; mag nil zijn.
	DB func(ctx context.Context) ([]databases.Group, error)
	// Disk geeft de volle nodes voor een spoedbeurt; mag nil zijn.
	Disk func(ctx context.Context) ([]diskwatch.Node, error)
	// ReadyTimeout is hoe lang een node na zijn beurt mag doen over gezond
	// zijn.
	ReadyTimeout time.Duration
	Now          func() time.Time
}

func NewService(pool *pgxpool.Pool, ev *events.Writer, runner *jobs.Runner, bus noderun.Bus, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	s := &Service{pool: pool, q: store.New(pool), ev: ev, runner: runner, log: log, Run: &noderun.Runner{Bus: bus},
		Gate: health.NewGate(pool), ReadyTimeout: 3 * time.Minute, Now: time.Now}
	if runner != nil {
		runner.Register(Kind, s.run)
	}
	return s
}

// PlanNode is een node in de volgorde van een beurt.
type PlanNode struct {
	NodeID       uuid.UUID  `json:"node_id"`
	Hostname     string     `json:"hostname"`
	Online       bool       `json:"online"`
	Lifecycle    string     `json:"lifecycle"`
	VIPHolder    bool       `json:"vip_holder"`
	DBEngine     string     `json:"db_engine,omitempty"`
	DBRole       string     `json:"db_role,omitempty"`
	GaleraExpect int        `json:"galera_expect,omitempty"`
	Order        int        `json:"order"`
	Why          string     `json:"why"`
	Last         *ResultRow `json:"last,omitempty"`
	Preview      *ResultRow `json:"preview,omitempty"`
	rank         int
}

// Order zet de nodes op volgorde: eerst de nodes zonder VIP of primary, dan
// de primary van de database, de VIP-houder als laatste.
func Order(nodes []PlanNode) []PlanNode {
	out := slices.Clone(nodes)
	for i := range out {
		n := &out[i]
		switch {
		case n.VIPHolder:
			n.rank, n.Why = 2, "VIP-houder: als laatste, als de rest klaar en gezond is"
		case n.DBRole == protocol.DBRolePrimary:
			n.rank, n.Why = 1, "primary van de database: na de replica's"
		case n.DBRole == protocol.DBRoleReplica:
			n.rank, n.Why = 0, "replica: eerst"
		case n.DBRole == protocol.DBRoleMember:
			n.rank, n.Why = 0, "Galera-node zonder VIP: eerst, één tegelijk"
		default:
			n.rank, n.Why = 0, "zonder VIP of primary: eerst"
		}
	}
	slices.SortStableFunc(out, func(a, b PlanNode) int {
		if a.rank != b.rank {
			return a.rank - b.rank
		}
		return strings.Compare(strings.ToLower(a.Hostname), strings.ToLower(b.Hostname))
	})
	for i := range out {
		out[i].Order = i + 1
	}
	return out
}

// LastRun is de laatste automatische start van een beleid.
type LastRun struct {
	StartedAt time.Time  `json:"started_at"`
	JobID     *uuid.UUID `json:"job_id,omitempty"`
	Note      string     `json:"note,omitempty"`
}

// Scope is één beleid met zijn nodes.
type Scope struct {
	Key         string     `json:"scope"`
	Kind        string     `json:"kind"`
	Name        string     `json:"name"`
	Slug        string     `json:"slug,omitempty"`
	Environment string     `json:"environment,omitempty"`
	ClusterID   *uuid.UUID `json:"cluster_id,omitempty"`
	Mode        string     `json:"mode"`
	Own         *Policy    `json:"own,omitempty"`
	Effective   Policy     `json:"effective"`
	UpdatedBy   *string    `json:"updated_by,omitempty"`
	UpdatedAt   *time.Time `json:"updated_at,omitempty"`
	NextStart   *time.Time `json:"next_start,omitempty"`
	LastRun     *LastRun   `json:"last_run,omitempty"`
	RunningJob  *uuid.UUID `json:"running_job,omitempty"`
	Nodes       []PlanNode `json:"nodes"`
}

// ResultRow is een regel uit het logboek.
type ResultRow struct {
	ID        int64      `json:"id"`
	JobID     uuid.UUID  `json:"job_id"`
	NodeID    uuid.UUID  `json:"node_id"`
	ClusterID *uuid.UUID `json:"cluster_id,omitempty"`
	Hostname  string     `json:"hostname"`
	Mode      string     `json:"mode"`
	At        time.Time  `json:"at"`
	OK        bool       `json:"ok"`
	Freed     int64      `json:"freed"`
	Result    Result     `json:"result"`
}

type policyRow struct {
	policy Policy
	by     *string
	at     time.Time
}

func (s *Service) policies(ctx context.Context) (map[string]policyRow, error) {
	rows, err := s.pool.Query(ctx, `SELECT p.scope, p.policy, u.username, p.updated_at FROM upkeep_policies p LEFT JOIN users u ON u.id = p.updated_by`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]policyRow{}
	for rows.Next() {
		var key string
		var raw []byte
		var r policyRow
		if err := rows.Scan(&key, &raw, &r.by, &r.at); err != nil {
			return nil, err
		}
		r.policy = DefaultPolicy
		if err := json.Unmarshal(raw, &r.policy); err != nil {
			return nil, err
		}
		out[key] = r
	}
	return out, rows.Err()
}

func (s *Service) lastRuns(ctx context.Context) (map[string]LastRun, error) {
	rows, err := s.pool.Query(ctx, `SELECT scope, started_at, job_id, note FROM upkeep_runs`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]LastRun{}
	for rows.Next() {
		var key string
		var r LastRun
		if err := rows.Scan(&key, &r.StartedAt, &r.JobID, &r.Note); err != nil {
			return nil, err
		}
		out[key] = r
	}
	return out, rows.Err()
}

func (s *Service) record(ctx context.Context, key string, at time.Time, job *uuid.UUID, note string) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO upkeep_runs (scope, started_at, job_id, note) VALUES ($1, $2, $3, $4)
		ON CONFLICT (scope) DO UPDATE SET started_at = $2, job_id = $3, note = $4`, key, at, job, note)
	return err
}

// running geeft per beleid de taak die nog wacht of loopt.
func (s *Service) running(ctx context.Context) (map[string]uuid.UUID, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, params->>'scope' FROM jobs WHERE kind = $1 AND status IN ('queued', 'running') ORDER BY created_at`, Kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]uuid.UUID{}
	for rows.Next() {
		var id uuid.UUID
		var key *string
		if err := rows.Scan(&id, &key); err != nil {
			return nil, err
		}
		if key != nil {
			if _, ok := out[*key]; !ok {
				out[*key] = id
			}
		}
	}
	return out, rows.Err()
}

// latest geeft per node de laatste beurt en het laatste voorbeeld.
func (s *Service) latest(ctx context.Context) (last, preview map[uuid.UUID]*ResultRow, err error) {
	rows, err := s.pool.Query(ctx, `SELECT DISTINCT ON (node_id, mode = 'preview') `+resultCols+` FROM upkeep_results
		ORDER BY node_id, mode = 'preview', at DESC`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	last, preview = map[uuid.UUID]*ResultRow{}, map[uuid.UUID]*ResultRow{}
	for rows.Next() {
		r, err := scanResult(rows)
		if err != nil {
			return nil, nil, err
		}
		if r.Mode == ModePreview {
			preview[r.NodeID] = &r
		} else {
			last[r.NodeID] = &r
		}
	}
	return last, preview, rows.Err()
}

const resultCols = `id, job_id, node_id, cluster_id, hostname, mode, at, ok, freed, result`

func scanResult(rows pgx.Row) (ResultRow, error) {
	var r ResultRow
	var raw []byte
	if err := rows.Scan(&r.ID, &r.JobID, &r.NodeID, &r.ClusterID, &r.Hostname, &r.Mode, &r.At, &r.OK, &r.Freed, &raw); err != nil {
		return r, err
	}
	_ = json.Unmarshal(raw, &r.Result)
	return r, nil
}

// History is het logboek, het nieuwste eerst; node of cluster mag nil.
func (s *Service) History(ctx context.Context, node, cluster *uuid.UUID, limit int) ([]ResultRow, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx, `SELECT `+resultCols+` FROM upkeep_results
		WHERE ($1::uuid IS NULL OR node_id = $1) AND ($2::uuid IS NULL OR cluster_id = $2)
		ORDER BY at DESC LIMIT $3`, node, cluster, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ResultRow{}
	for rows.Next() {
		r, err := scanResult(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Overview geeft elk beleid: de omgevingen, de nodes zonder cluster en elk
// cluster, met de nodes in de volgorde van een beurt.
func (s *Service) Overview(ctx context.Context) ([]Scope, error) {
	pols, err := s.policies(ctx)
	if err != nil {
		return nil, err
	}
	runs, err := s.lastRuns(ctx)
	if err != nil {
		return nil, err
	}
	busy, err := s.running(ctx)
	if err != nil {
		return nil, err
	}
	last, preview, err := s.latest(ctx)
	if err != nil {
		return nil, err
	}
	var nodes []updates.Node
	if s.Nodes != nil {
		if nodes, err = s.Nodes(ctx); err != nil {
			return nil, err
		}
	}
	life := map[uuid.UUID]string{}
	rows, err := s.pool.Query(ctx, `SELECT id, lifecycle::text FROM nodes`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id uuid.UUID
		var l string
		if err := rows.Scan(&id, &l); err != nil {
			rows.Close()
			return nil, err
		}
		life[id] = l
	}
	rows.Close()
	type dbInfo struct {
		engine, role string
		expect       int
	}
	dbs := map[uuid.UUID]dbInfo{}
	if s.DB != nil {
		groups, err := s.DB(ctx)
		if err != nil {
			s.log.Warn("databases lezen voor onderhoud mislukt", "err", err)
		}
		for _, g := range groups {
			expect := 0
			for _, sv := range g.Servers {
				if sv.Galera != nil {
					expect++
				}
			}
			for _, sv := range g.Servers {
				dbs[sv.NodeID] = dbInfo{engine: g.Engine, role: sv.Role, expect: expect}
			}
		}
	}
	now := s.Now()
	effective := func(key string) (Policy, *policyRow) {
		if r, ok := pols[key]; ok {
			return r.policy, &r
		}
		return DefaultPolicy, nil
	}
	finish := func(sc *Scope) {
		if r, ok := runs[sc.Key]; ok {
			sc.LastRun = &r
		}
		if id, ok := busy[sc.Key]; ok {
			sc.RunningJob = &id
		}
		if sc.Effective.Enabled {
			if w, err := planner.ParseWindow(sc.Effective.Window); err == nil {
				if start, _ := w.Next(now); !start.IsZero() {
					sc.NextStart = &start
				}
			}
		}
	}
	var out []Scope
	for _, e := range Envs {
		p, row := effective(EnvScope(e))
		sc := Scope{Key: EnvScope(e), Kind: KindEnv, Name: envName(e), Environment: e, Mode: ModeOwn, Effective: p, Nodes: []PlanNode{}}
		if row != nil {
			sc.UpdatedBy, sc.UpdatedAt = row.by, &row.at
		}
		finish(&sc)
		out = append(out, sc)
	}
	plan := func(n updates.Node) PlanNode {
		pn := PlanNode{NodeID: n.NodeID, Hostname: n.Hostname, Online: n.Online, Lifecycle: life[n.NodeID], VIPHolder: n.VIPHolder,
			Last: last[n.NodeID], Preview: preview[n.NodeID]}
		if d, ok := dbs[n.NodeID]; ok {
			pn.DBEngine, pn.DBRole, pn.GaleraExpect = d.engine, d.role, d.expect
		}
		return pn
	}
	// Nodes zonder cluster.
	sa := Scope{Key: ScopeStandalone, Kind: KindStandalone, Name: "Nodes zonder cluster", Mode: ModeOwn, Nodes: []PlanNode{}}
	sa.Effective, _ = effective(ScopeStandalone)
	if r, ok := pols[ScopeStandalone]; ok {
		sa.UpdatedBy, sa.UpdatedAt = r.by, &r.at
	}
	var loose []PlanNode
	byCluster := map[uuid.UUID][]PlanNode{}
	for _, n := range nodes {
		if n.ClusterID == nil {
			loose = append(loose, plan(n))
			continue
		}
		byCluster[*n.ClusterID] = append(byCluster[*n.ClusterID], plan(n))
	}
	sa.Nodes = Order(loose)
	if sa.Nodes == nil {
		sa.Nodes = []PlanNode{}
	}
	finish(&sa)
	out = append(out, sa)
	cls, err := s.q.ListClusters(ctx)
	if err != nil {
		return nil, err
	}
	for _, c := range cls {
		id := c.Cluster.ID
		sc := Scope{Key: ClusterScope(id), Kind: KindCluster, Name: c.Cluster.Name, Slug: c.Cluster.Slug, Environment: string(c.Cluster.Environment),
			ClusterID: &id, Mode: ModeInherit, Nodes: Order(byCluster[id])}
		if sc.Nodes == nil {
			sc.Nodes = []PlanNode{}
		}
		sc.Effective, _ = effective(EnvScope(sc.Environment))
		if r, ok := pols[sc.Key]; ok {
			p := r.policy
			sc.Own, sc.Effective, sc.UpdatedBy, sc.UpdatedAt = &p, p, r.by, &r.at
			sc.Mode = ModeOwn
			if !p.Enabled {
				sc.Mode = ModeOff
			}
		}
		finish(&sc)
		out = append(out, sc)
	}
	return out, nil
}

func envName(e string) string {
	switch e {
	case "lab":
		return "Lab"
	case "test":
		return "Test"
	case "prod":
		return "Prod"
	}
	return e
}

// Scope geeft één beleid.
func (s *Service) Scope(ctx context.Context, key string) (Scope, error) {
	all, err := s.Overview(ctx)
	if err != nil {
		return Scope{}, err
	}
	for _, sc := range all {
		if sc.Key == key {
			return sc, nil
		}
	}
	return Scope{}, ErrNotFound
}

// SetPolicy bewaart een beleid. Bij een cluster zet mode het op de
// omgeving (inherit), een eigen beleid (own) of uit (off).
func (s *Service) SetPolicy(ctx context.Context, actor events.Actor, userID *uuid.UUID, key, mode string, p Policy) error {
	var clusterID *uuid.UUID
	env, isEnv := isEnvScope(key)
	raw, isCluster := isClusterScope(key)
	switch {
	case key == ScopeStandalone:
		mode = ModeOwn
	case isEnv:
		if !slices.Contains(Envs, env) {
			return ErrNotFound
		}
		mode = ModeOwn
	case isCluster:
		id, err := uuid.Parse(raw)
		if err != nil {
			return ErrNotFound
		}
		if _, err := s.q.GetCluster(ctx, id); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		clusterID = &id
	default:
		return ErrNotFound
	}
	switch mode {
	case ModeInherit:
	case ModeOff:
		p.Enabled = false
	case ModeOwn:
		if clusterID != nil {
			p.Enabled = true
		}
	default:
		return &FieldError{"kies inherit, own of off"}
	}
	if mode != ModeInherit {
		var err error
		if p, err = p.Normalize(); err != nil {
			return err
		}
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		if mode == ModeInherit {
			_, err = tx.Exec(ctx, `DELETE FROM upkeep_policies WHERE scope = $1`, key)
		} else {
			raw, _ := json.Marshal(p)
			_, err = tx.Exec(ctx, `INSERT INTO upkeep_policies (scope, cluster_id, policy, updated_by, updated_at) VALUES ($1, $2, $3, $4, now())
				ON CONFLICT (scope) DO UPDATE SET policy = $3, updated_by = $4, updated_at = now()`, key, clusterID, raw, userID)
		}
		if err != nil || s.ev == nil {
			return err
		}
		ev := events.Event{Actor: actor, SubjectType: "settings", SubjectID: "upkeep-" + key, Action: "settings.upkeep_policy",
			Payload: map[string]any{"scope": key, "mode": mode, "policy": p}}
		if clusterID != nil {
			ev.SubjectType, ev.SubjectID, ev.ClusterID = "cluster", clusterID.String(), clusterID
		}
		return s.ev.Write(ctx, store.New(tx), ev)
	})
}

type params struct {
	Scope     string     `json:"scope"`
	Name      string     `json:"name"`
	ClusterID *uuid.UUID `json:"cluster_id,omitempty"`
	Mode      string     `json:"mode"`
	Policy    Policy     `json:"policy"`
	Nodes     []jobNode  `json:"nodes"`
	Auto      bool       `json:"auto,omitempty"`
}

type jobNode struct {
	ID           uuid.UUID `json:"id"`
	Hostname     string    `json:"hostname"`
	GaleraExpect int       `json:"galera_expect,omitempty"`
	Why          string    `json:"why,omitempty"`
}

// Start zet een beurt (ModeRun) of een voorbeeld (ModePreview) van een
// beleid in de wachtrij. Een beurt van een cluster houdt het clusterslot:
// nooit samen met een update, uitrol of test van hetzelfde cluster.
func (s *Service) Start(ctx context.Context, actor events.Actor, key, mode string, auto bool) (uuid.UUID, error) {
	sc, err := s.Scope(ctx, key)
	if err != nil {
		return uuid.Nil, err
	}
	if sc.Kind == KindEnv {
		return uuid.Nil, &FieldError{"een omgeving heeft geen eigen nodes; start de beurt bij een cluster"}
	}
	return s.start(ctx, actor, sc, sc.Nodes, mode, auto)
}

func (s *Service) start(ctx context.Context, actor events.Actor, sc Scope, nodes []PlanNode, mode string, auto bool) (uuid.UUID, error) {
	if mode != ModeRun && mode != ModePreview && mode != ModeUrgent {
		return uuid.Nil, &FieldError{"onbekende soort beurt"}
	}
	p := sc.Effective
	if mode == ModeUrgent {
		p = p.DiskOnly()
	}
	if !p.anything() {
		return uuid.Nil, &FieldError{"dit beleid heeft geen enkel onderdeel aan; kies eerst wat er onderhouden moet worden"}
	}
	if len(nodes) == 0 {
		return uuid.Nil, &ConflictError{"er zijn hier geen nodes met een agent"}
	}
	if sc.RunningJob != nil && mode != ModeUrgent {
		return uuid.Nil, &ConflictError{"er loopt al een onderhoudsbeurt of voorbeeld voor " + sc.Name}
	}
	pr := params{Scope: sc.Key, Name: sc.Name, ClusterID: sc.ClusterID, Mode: mode, Policy: p, Auto: auto}
	for _, n := range nodes {
		pr.Nodes = append(pr.Nodes, jobNode{ID: n.NodeID, Hostname: n.Hostname, GaleraExpect: n.GaleraExpect, Why: n.Why})
	}
	title := "Onderhoud in " + sc.Name
	switch {
	case mode == ModePreview:
		title = "Onderhoud: voorbeeld voor " + sc.Name
	case mode == ModeUrgent:
		title = "Onderhoud: schijf vol op " + nodes[0].Hostname
	case len(nodes) == 1:
		title = "Onderhoud op " + nodes[0].Hostname
	}
	if auto && mode == ModeRun {
		title = "Automatisch: " + title
	}
	spec := jobs.Spec{Kind: Kind, Title: title, Params: pr, ClusterID: sc.ClusterID, Actor: actor}
	if len(nodes) == 1 {
		spec.NodeID = &nodes[0].NodeID
	}
	var j store.Job
	var err error
	if sc.ClusterID != nil && mode != ModePreview {
		j, err = s.runner.EnqueueForCluster(ctx, spec)
	} else {
		j, err = s.runner.Enqueue(ctx, spec)
	}
	if err != nil {
		var busy jobs.BusyError
		if errors.As(err, &busy) {
			return uuid.Nil, &ConflictError{busy.Error()}
		}
		return uuid.Nil, err
	}
	return j.ID, nil
}

// capture geeft de uitvoer door aan de stap en houdt de CFUPKEEP-regels bij.
type capture struct {
	st    *jobs.Step
	lines []string
}

func (c *capture) Logf(format string, args ...any) {
	line := fmt.Sprintf(format, args...)
	if strings.HasPrefix(line, "CFUPKEEP\t") {
		c.lines = append(c.lines, line)
		return
	}
	c.st.Logf("%s", line)
}

func (c *capture) Flush(ctx context.Context) error { return c.st.Flush(ctx) }

func (s *Service) run(ctx context.Context, j *jobs.Job) error {
	var p params
	if err := j.Decode(&p); err != nil {
		return err
	}
	preview := p.Mode == ModePreview
	ids := make([]uuid.UUID, len(p.Nodes))
	for i, n := range p.Nodes {
		ids[i] = n.ID
	}
	for i, n := range p.Nodes {
		name := fmt.Sprintf("%d. %s", i+1, n.Hostname)
		var services []string
		ran := false
		label := name + ": onderhoud"
		if preview {
			label = name + ": bekijken"
		}
		err := j.Step(ctx, label, func(ctx context.Context, st *jobs.Step) error {
			if n.Why != "" && !preview {
				st.Logf("volgorde: %s", n.Why)
			}
			rt, err := s.q.GetNodeRuntime(ctx, n.ID)
			if errors.Is(err, pgx.ErrNoRows) {
				st.Logf("%s bestaat niet meer; overgeslagen", n.Hostname)
				return nil
			}
			if err != nil {
				return err
			}
			switch {
			case !rt.HasAgent:
				st.Logf("%s heeft geen agent meer; overgeslagen", n.Hostname)
				return nil
			case rt.HeartbeatAt == nil || s.Now().Sub(*rt.HeartbeatAt) > status.HeartbeatLate:
				st.Logf("%s is niet verbonden; overgeslagen. De volgende beurt probeert opnieuw.", n.Hostname)
				return nil
			case rt.AgentProtocol < protocol.RunSince:
				st.Logf("de agent op %s heeft protocol %d en kan nog geen scripts draaien (vanaf %d); overgeslagen", n.Hostname, rt.AgentProtocol, protocol.RunSince)
				return nil
			case rt.Lifecycle != store.NodeLifecycleActive && !preview:
				st.Logf("%s staat op %s; overgeslagen", n.Hostname, rt.Lifecycle)
				return nil
			}
			for svc, state := range health.ServiceStates(rt.Services) {
				if state == "active" {
					services = append(services, svc)
				}
			}
			slices.Sort(services)
			env := NodeEnv{GaleraExpect: n.GaleraExpect}
			if off := s.offline(ctx, ids, n.ID); len(off) > 0 {
				env.Degraded = true
				st.Logf("niet verbonden in dit cluster: %s. Zwaar databasewerk (OPTIMIZE, REINDEX) wacht daarom op een volgende beurt.", strings.Join(off, ", "))
			}
			c := &capture{st: st}
			spec := protocol.RunSpec{ID: scriptID(j.ID, int(j.Attempts), i), Kind: protocol.RunScript, Title: j.Title,
				TimeoutSeconds: int((3 * time.Hour).Seconds()), Script: Script(p.Policy, p.Mode, env)}
			res, err := s.Run.DoWith(ctx, c, st, n.ID, spec)
			if err != nil {
				return err
			}
			out := Parse(c.lines, preview)
			out.Order, out.Why = i+1, n.Why
			ran = true
			s.save(context.WithoutCancel(ctx), j, p, n, out)
			s.summary(st, out, preview)
			if res.State != protocol.RunOK {
				return fmt.Errorf("%s; de nodes erna komen deze beurt niet aan de beurt", noderun.Describe(res))
			}
			return nil
		})
		if err != nil {
			return err
		}
		if preview || !ran {
			continue
		}
		if err := j.Step(ctx, name+": gezond", func(ctx context.Context, st *jobs.Step) error {
			since := s.Now()
			if len(services) > 0 {
				st.Logf("wachten tot draait wat er voor het onderhoud draaide: %s", strings.Join(services, ", "))
				_ = st.Flush(ctx)
			}
			rctx, cancel := context.WithTimeout(ctx, s.ReadyTimeout)
			defer cancel()
			if err := s.Gate.NodeReady(rctx, n.ID, since, services, func(m string) { st.Logf("%s", m); _ = st.Flush(ctx) }); err != nil {
				return fmt.Errorf("%s is na het onderhoud niet gezond: %w; de nodes erna komen deze beurt niet aan de beurt", n.Hostname, err)
			}
			st.Logf("%s is gezond", n.Hostname)
			if i < len(p.Nodes)-1 && p.Policy.PauseSeconds > 0 {
				st.Logf("%d seconden pauze voor de volgende node", p.Policy.PauseSeconds)
				_ = st.Flush(ctx)
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(time.Duration(p.Policy.PauseSeconds) * time.Second):
				}
			}
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}

// offline geeft de andere nodes van de beurt die niet verbonden zijn.
func (s *Service) offline(ctx context.Context, ids []uuid.UUID, self uuid.UUID) []string {
	var out []string
	for _, id := range ids {
		if id == self {
			continue
		}
		rt, err := s.q.GetNodeRuntime(ctx, id)
		if err != nil {
			continue
		}
		if rt.HeartbeatAt == nil || s.Now().Sub(*rt.HeartbeatAt) > status.HeartbeatLate {
			out = append(out, rt.Hostname)
		}
	}
	return out
}

func (s *Service) summary(st *jobs.Step, r Result, preview bool) {
	if r.Skipped != "" {
		st.Logf("overgeslagen: %s", r.Skipped)
		return
	}
	for _, it := range r.Items {
		switch {
		case preview && it.Status == "plan":
			st.Logf("• %s: ongeveer %s", it.Title, diskwatch.Human(it.Bytes))
		case it.Status == "fail":
			st.Logf("✗ %s: %s", it.Title, it.Note)
		case it.Status == "skip":
			st.Logf("– %s: %s", it.Title, it.Note)
		default:
			st.Logf("✓ %s", it.Title)
		}
	}
	for _, d := range r.DB {
		st.Logf("%s (%s): %s", databases.EngineName(d.Engine), d.Role, d.Note)
	}
	if preview {
		st.Logf("Komt vrij: ongeveer %s.", diskwatch.Human(r.Freed))
	} else {
		st.Logf("Vrijgemaakt: %s.", diskwatch.Human(r.Freed))
	}
}

func (s *Service) save(ctx context.Context, j *jobs.Job, p params, n jobNode, r Result) {
	raw, _ := json.Marshal(r)
	mode := p.Mode
	if _, err := s.pool.Exec(ctx, `INSERT INTO upkeep_results (job_id, node_id, cluster_id, hostname, mode, ok, freed, result)
		SELECT $1, $2, $3, $4, $5, $6, $7, $8 WHERE EXISTS (SELECT 1 FROM nodes WHERE id = $2)`,
		j.ID, n.ID, p.ClusterID, n.Hostname, mode, r.OK(), r.Freed, raw); err != nil {
		s.log.Warn("onderhoud bewaren mislukt", "node", n.Hostname, "err", err)
	}
	if mode == ModePreview || s.ev == nil {
		return
	}
	actor := events.System()
	if j.RequestedBy != nil {
		actor = events.User(*j.RequestedBy)
	}
	action := "node.upkeep_done"
	if !r.OK() {
		action = "node.upkeep_failed"
	}
	_ = s.ev.Write(ctx, nil, events.Event{Actor: actor, SubjectType: "node", SubjectID: n.ID.String(), ClusterID: p.ClusterID, Action: action,
		Payload: map[string]any{"hostname": n.Hostname, "freed": r.Freed, "mode": mode, "job_id": j.ID.String(), "db": r.DB}})
}

// Tick start wat nu aan de beurt is: elk beleid dat aan staat één keer per
// venster, en een spoedbeurt op een node met een volle schijf. De planner
// roept het elke minuut aan.
func (s *Service) Tick(ctx context.Context, now time.Time) error {
	scopes, err := s.Overview(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, sc := range scopes {
		p := sc.Effective
		if sc.Kind == KindEnv || !p.Enabled || len(sc.Nodes) == 0 {
			continue
		}
		w, err := planner.ParseWindow(p.Window)
		if err != nil {
			continue
		}
		start, end := w.Next(now)
		if start.IsZero() || now.Before(start) || !now.Before(end) {
			continue
		}
		if sc.LastRun != nil && !sc.LastRun.StartedAt.Before(start) {
			continue
		}
		if sc.RunningJob != nil {
			continue
		}
		id, err := s.start(ctx, events.System(), sc, sc.Nodes, ModeRun, true)
		var ce *ConflictError
		var fe *FieldError
		switch {
		case errors.As(err, &ce):
			// Er loopt al een taak op het cluster: de volgende minuut opnieuw.
			continue
		case errors.As(err, &fe):
			errs = append(errs, s.record(ctx, sc.Key, now, nil, "overgeslagen: "+fe.Msg))
		case err != nil:
			errs = append(errs, fmt.Errorf("%s: %w", sc.Name, err))
		default:
			s.log.Info("automatisch onderhoud gestart", "beleid", sc.Name, "job", id)
			errs = append(errs, s.record(ctx, sc.Key, now, &id, ""))
		}
	}
	errs = append(errs, s.urgent(ctx, now, scopes))
	if _, err := s.pool.Exec(ctx, `DELETE FROM upkeep_results WHERE at < $1`, now.Add(-Keep)); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// urgent ruimt een node boven de ernstige schijfdrempel meteen op.
func (s *Service) urgent(ctx context.Context, now time.Time, scopes []Scope) error {
	if s.Disk == nil {
		return nil
	}
	full, err := s.Disk(ctx)
	if err != nil {
		return err
	}
	if len(full) == 0 {
		return nil
	}
	runs, err := s.lastRuns(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, d := range full {
		if d.Level != diskwatch.LevelCrit || !d.Online {
			continue
		}
		if r, ok := runs[urgentScope(d.NodeID)]; ok && now.Sub(r.StartedAt) < UrgentEvery {
			continue
		}
		for _, sc := range scopes {
			if sc.Kind == KindEnv || !sc.Effective.Enabled || !sc.Effective.Urgent || !sc.Effective.diskAnything() {
				continue
			}
			i := slices.IndexFunc(sc.Nodes, func(n PlanNode) bool { return n.NodeID == d.NodeID })
			if i < 0 {
				continue
			}
			id, err := s.start(ctx, events.System(), sc, sc.Nodes[i:i+1], ModeUrgent, true)
			var ce *ConflictError
			if errors.As(err, &ce) {
				break
			}
			if err != nil {
				errs = append(errs, err)
				break
			}
			s.log.Info("spoedonderhoud gestart: schijf vol", "node", d.Hostname, "job", id)
			errs = append(errs, s.record(ctx, urgentScope(d.NodeID), now, &id, fmt.Sprintf("schijf %.0f %% vol", d.Ratio*100)))
			break
		}
	}
	return errors.Join(errs...)
}
