// Package aiverslag koppelt ClusterForge met AI-verslag, Jonas' AI-analyse
// van zijn OPNsense-netwerk (fase 38). De cf-agent op de machine van
// AI-verslag koppelt zichzelf lokaal als app en geeft de API door: lezen
// rechtstreeks (aiv_get, korte cache), schrijven als taak met de run-actie
// aiv_call. Welke paden de GUI gebruikt, staat in policy.go.
package aiverslag

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/jobs"
	"github.com/Jonasz1996/clusterforge/internal/noderun"
	"github.com/Jonasz1996/clusterforge/internal/status"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// Kind is de soort taak van een aanroep op AI-verslag.
const Kind = "aiverslag.action"

// RequestTimeout is hoe lang een leesverzoek mag duren.
const RequestTimeout = 70 * time.Second

// Bus praat met de agents.
type Bus interface {
	noderun.Bus
	PVE(ctx context.Context, nodeID uuid.UUID, req protocol.PVERequest) (protocol.PVEResponse, error)
}

// ValidationError is een fout in een vraag.
type ValidationError struct{ Msg string }

func (e ValidationError) Error() string { return e.Msg }

// ErrNotFound: geen AI-verslag met een agent onder dat id.
var ErrNotFound = errors.New("geen AI-verslag met een agent")

type Service struct {
	q     *store.Queries
	ev    *events.Writer
	log   *slog.Logger
	jobs  *jobs.Runner
	bus   Bus
	Run   *noderun.Runner
	cache getCache
	watch watchState
	// Version is de versie van ClusterForge (in de context voor ext56).
	Version string
}

func NewService(pool *pgxpool.Pool, ev *events.Writer, log *slog.Logger, runner *jobs.Runner, bus Bus) *Service {
	s := &Service{ev: ev, log: log, jobs: runner, bus: bus, Run: &noderun.Runner{Bus: bus}}
	if pool != nil {
		s.q = store.New(pool)
	}
	if runner != nil {
		runner.Register(Kind, s.run)
		runner.Register(InstallKind, s.runInstall)
	}
	return s
}

// Instance is een machine met AI-verslag en een agent.
type Instance struct {
	NodeID      uuid.UUID          `json:"node_id"`
	Hostname    string             `json:"hostname"`
	Address     string             `json:"address,omitempty"`
	Facts       protocol.AIVFacts  `json:"facts"`
	Units       []protocol.AIVUnit `json:"units"`
	Protocol    int                `json:"protocol"`
	Agent       string             `json:"agent_version,omitempty"`
	Online      bool               `json:"online"`
	HeartbeatAt *time.Time         `json:"heartbeat_at,omitempty"`
	// Why zegt waarom ClusterForge AI-verslag niet kan lezen; leeg als het kan.
	Why string `json:"why,omitempty"`
}

func (v Instance) ready() string {
	switch {
	case !v.Online:
		return fmt.Sprintf("de agent op %s is offline", v.Hostname)
	case v.Protocol < protocol.AIVSince:
		return fmt.Sprintf("de agent op %s is te oud voor AI-verslag (protocol %d, %d nodig); werk hem bij via Nodes → Agents bijwerken", v.Hostname, v.Protocol, protocol.AIVSince)
	case !v.Facts.Reachable:
		return fmt.Sprintf("de webdienst van AI-verslag op %s antwoordt niet (%s); kijk aiverslag-web.service na", v.Hostname, v.Facts.URL)
	}
	return ""
}

// Instances geeft alle machines met AI-verslag.
func (s *Service) Instances(ctx context.Context) ([]Instance, error) {
	rows, err := s.q.ListAIVAgents(ctx)
	if err != nil {
		return nil, err
	}
	out := []Instance{}
	for _, r := range rows {
		var f protocol.AIVFacts
		if json.Unmarshal(r.Aiverslag, &f) != nil {
			continue
		}
		v := Instance{NodeID: r.ID, Hostname: r.Hostname, Address: r.Address, Facts: f, Units: f.Units, HeartbeatAt: r.HeartbeatAt}
		if v.Units == nil {
			v.Units = []protocol.AIVUnit{}
		}
		v.Facts.Units = nil
		if r.AgentProtocol != nil {
			v.Protocol = int(*r.AgentProtocol)
		}
		v.Online = r.HeartbeatAt != nil && time.Since(*r.HeartbeatAt) < status.HeartbeatLate
		if r.AgentVersion != nil {
			v.Agent = *r.AgentVersion
		}
		v.Why = v.ready()
		out = append(out, v)
	}
	return out, nil
}

// Instance geeft één machine.
func (s *Service) Instance(ctx context.Context, id uuid.UUID) (Instance, error) {
	list, err := s.Instances(ctx)
	if err != nil {
		return Instance{}, err
	}
	for _, v := range list {
		if v.NodeID == id {
			return v, nil
		}
	}
	return Instance{}, ErrNotFound
}

type cacheEntry struct {
	at   time.Time
	data json.RawMessage
}

type getCache struct {
	mu sync.Mutex
	m  map[string]cacheEntry
}

const getCacheTTL = 5 * time.Second

func (c *getCache) get(k string) (json.RawMessage, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[k]
	if !ok || time.Since(e.at) > getCacheTTL {
		return nil, false
	}
	return e.data, true
}

func (c *getCache) put(k string, v json.RawMessage) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil || len(c.m) > 300 {
		c.m = map[string]cacheEntry{}
	}
	c.m[k] = cacheEntry{at: time.Now(), data: v}
}

func (c *getCache) clear() {
	c.mu.Lock()
	c.m = nil
	c.mu.Unlock()
}

// Get leest een pad uit de allowlist (policy.go).
func (s *Service) Get(ctx context.Context, id uuid.UUID, path string, params map[string]string) (json.RawMessage, error) {
	if err := checkRead(path, params); err != nil {
		return nil, ValidationError{err.Error()}
	}
	v, err := s.Instance(ctx, id)
	if err != nil {
		return nil, err
	}
	if v.Why != "" {
		return nil, ValidationError{v.Why}
	}
	return s.get(ctx, id, path, params)
}

func cacheKey(id uuid.UUID, path string, params map[string]string) string {
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString(id.String() + path)
	for _, k := range keys {
		b.WriteString("&" + k + "=" + params[k])
	}
	return b.String()
}

func (s *Service) get(ctx context.Context, id uuid.UUID, path string, params map[string]string) (json.RawMessage, error) {
	key := cacheKey(id, path, params)
	if d, ok := s.cache.get(key); ok {
		return d, nil
	}
	req := protocol.PVERequest{Op: protocol.AIVOpGet, Path: path, Params: params}
	if err := req.ValidateAIVGet(); err != nil {
		return nil, ValidationError{err.Error()}
	}
	ctx, cancel := context.WithTimeout(ctx, RequestTimeout)
	defer cancel()
	res, err := s.bus.PVE(ctx, id, req)
	if err != nil {
		return nil, err
	}
	if res.Error != "" {
		return nil, errors.New(res.Error)
	}
	s.cache.put(key, res.Data)
	return res.Data, nil
}

// Call is een schrijvende aanroep vanuit de GUI.
type Call struct {
	Path string          `json:"path"`
	Body json.RawMessage `json:"body,omitempty"`
	// Confirm is wat Jonas overtikte bij een aanroep die dat vraagt.
	Confirm string `json:"confirm,omitempty"`
}

type params struct {
	NodeID   uuid.UUID            `json:"node_id"`
	Hostname string               `json:"hostname"`
	Call     protocol.AIVCallSpec `json:"call"`
}

// Prepared is een gecontroleerde aanroep.
type Prepared struct {
	Instance Instance
	Spec     protocol.AIVCallSpec
	Rule     Rule
}

// Prepare controleert een aanroep tegen de allowlist en de toestand.
func (s *Service) Prepare(ctx context.Context, id uuid.UUID, c Call) (Prepared, error) {
	rule, spec, err := checkCall(c.Path, c.Body)
	if err != nil {
		return Prepared{}, ValidationError{err.Error()}
	}
	if rule.Confirm != "" && strings.TrimSpace(c.Confirm) != rule.Confirm {
		return Prepared{}, ValidationError{fmt.Sprintf("tik %s over om te bevestigen", rule.Confirm)}
	}
	v, err := s.Instance(ctx, id)
	if err != nil {
		return Prepared{}, err
	}
	if v.Why != "" {
		return Prepared{}, ValidationError{v.Why}
	}
	return Prepared{Instance: v, Spec: spec, Rule: rule}, nil
}

// StartCall zet een gecontroleerde aanroep als taak in de wachtrij.
func (s *Service) StartCall(ctx context.Context, actor events.Actor, p Prepared) (store.Job, error) {
	nodeID := p.Instance.NodeID
	j, err := s.jobs.Enqueue(ctx, jobs.Spec{
		Kind: Kind, Title: p.Rule.Title, Actor: actor, NodeID: &nodeID,
		Params: params{NodeID: nodeID, Hostname: p.Instance.Hostname, Call: p.Spec},
	})
	if err != nil {
		return store.Job{}, err
	}
	s.event(ctx, actor, p, map[string]any{"job_id": j.ID.String()})
	s.cache.clear()
	s.jobs.Kick()
	return j, nil
}

// CallNow voert een korte aanroep meteen uit (bv. een vraag stellen) en
// geeft het antwoord van AI-verslag; die komt in het logboek, niet als taak.
func (s *Service) CallNow(ctx context.Context, actor events.Actor, p Prepared) (json.RawMessage, error) {
	if !p.Rule.Direct {
		return nil, ValidationError{"deze aanroep loopt als taak"}
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	res, err := s.Do(ctx, &memLog{}, &memState{}, p.Instance.NodeID, p.Spec, p.Rule.Title)
	if err != nil {
		return nil, err
	}
	if res.State != protocol.RunOK {
		return nil, errors.New(noderun.Describe(res))
	}
	s.event(ctx, actor, p, nil)
	s.cache.clear()
	if res.PVE == nil || len(res.PVE.Data) == 0 {
		return json.RawMessage("null"), nil
	}
	return res.PVE.Data, nil
}

func (s *Service) event(ctx context.Context, actor events.Actor, p Prepared, extra map[string]any) {
	if s.ev == nil {
		return
	}
	payload := map[string]any{"host": p.Instance.Hostname, "path": p.Spec.Path, "title": p.Rule.Title}
	if p.Rule.LogBody && len(p.Spec.Body) > 0 {
		payload["body"] = p.Spec.Body
	}
	for k, v := range extra {
		payload[k] = v
	}
	if err := s.ev.Write(ctx, nil, events.Event{
		Actor: actor, SubjectType: "node", SubjectID: p.Instance.NodeID.String(), Action: "aiverslag.call_requested", Payload: payload,
	}); err != nil {
		s.log.Warn("event schrijven mislukt", "err", err)
	}
}

// Do voert een aanroep uit op de agent van nodeID.
func (s *Service) Do(ctx context.Context, log noderun.Log, ss noderun.StepState, nodeID uuid.UUID, call protocol.AIVCallSpec, title string) (protocol.RunStatus, error) {
	op := protocol.PVERun{Action: protocol.AIVCall, AIV: &call}
	spec := protocol.RunSpec{ID: uuid.NewString(), Kind: protocol.RunPVE, Title: title, PVE: &op}
	if err := spec.Validate(); err != nil {
		return protocol.RunStatus{}, err
	}
	return s.Run.DoWith(ctx, log, ss, nodeID, spec)
}

func (s *Service) run(ctx context.Context, j *jobs.Job) error {
	var p params
	if err := j.Decode(&p); err != nil {
		return err
	}
	defer s.cache.clear()
	return j.Step(ctx, j.Title, func(ctx context.Context, st *jobs.Step) error {
		res, err := s.Do(ctx, st, st, p.NodeID, p.Call, j.Title+" in AI-verslag op "+p.Hostname)
		if err != nil {
			return err
		}
		if res.State != protocol.RunOK {
			return errors.New(noderun.Describe(res))
		}
		return nil
	})
}

// memLog en memState laten een korte run buiten een taak lopen.
type memLog struct{}

func (*memLog) Logf(string, ...any)         {}
func (*memLog) Flush(context.Context) error { return nil }

type memState struct{ data []byte }

func (m *memState) State(v any) bool {
	if m.data == nil {
		return false
	}
	return json.Unmarshal(m.data, v) == nil
}

func (m *memState) SetState(_ context.Context, v any) error {
	d, err := json.Marshal(v)
	m.data = d
	return err
}
