// Package truenas beheert TrueNAS SCALE / Community Edition via de
// cf-agent die erop draait (fase 37). Lezen gaat rechtstreeks (midclt call
// met een leesmethode, korte cache), veranderen als taak met de run-actie
// truenas_call. Welke methodes de GUI mag aanroepen, staat in policy.go.
package truenas

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
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

// Kind is de soort taak van een aanroep op TrueNAS.
const Kind = "truenas.action"

// RequestTimeout is hoe lang een leesverzoek mag duren.
const RequestTimeout = 60 * time.Second

// Bus praat met de agents.
type Bus interface {
	noderun.Bus
	PVE(ctx context.Context, nodeID uuid.UUID, req protocol.PVERequest) (protocol.PVEResponse, error)
}

// ValidationError is een fout in een vraag.
type ValidationError struct{ Msg string }

func (e ValidationError) Error() string { return e.Msg }

// ErrNotFound: geen TrueNAS met een agent onder dat id.
var ErrNotFound = errors.New("geen TrueNAS met een agent")

type Service struct {
	q     *store.Queries
	ev    *events.Writer
	log   *slog.Logger
	jobs  *jobs.Runner
	bus   Bus
	Run   *noderun.Runner
	cache getCache
}

func NewService(pool *pgxpool.Pool, ev *events.Writer, log *slog.Logger, runner *jobs.Runner, bus Bus) *Service {
	s := &Service{q: store.New(pool), ev: ev, log: log, jobs: runner, bus: bus, Run: &noderun.Runner{Bus: bus}}
	if runner != nil {
		runner.Register(Kind, s.run)
	}
	return s
}

// Server is een TrueNAS met een agent.
type Server struct {
	NodeID      uuid.UUID              `json:"node_id"`
	Hostname    string                 `json:"hostname"`
	Address     string                 `json:"address,omitempty"`
	Version     string                 `json:"version,omitempty"`
	Pools       []protocol.TrueNASPool `json:"pools"`
	Persistent  bool                   `json:"persistent"`
	Protocol    int                    `json:"protocol"`
	Agent       string                 `json:"agent_version,omitempty"`
	Online      bool                   `json:"online"`
	HeartbeatAt *time.Time             `json:"heartbeat_at,omitempty"`
	// Why zegt waarom ClusterForge de TrueNAS niet kan beheren; leeg als het kan.
	Why string `json:"why,omitempty"`
}

func (v Server) ready() string {
	switch {
	case !v.Online:
		return fmt.Sprintf("de agent op %s is offline", v.Hostname)
	case v.Protocol < protocol.TrueNASSince:
		return fmt.Sprintf("de agent op %s is te oud voor TrueNAS-beheer (protocol %d, %d nodig); werk hem bij via Nodes → Agents bijwerken", v.Hostname, v.Protocol, protocol.TrueNASSince)
	}
	return ""
}

// Servers geeft alle TrueNAS-machines met een agent.
func (s *Service) Servers(ctx context.Context) ([]Server, error) {
	rows, err := s.q.ListTrueNASAgents(ctx)
	if err != nil {
		return nil, err
	}
	out := []Server{}
	for _, r := range rows {
		var f protocol.TrueNASFacts
		if json.Unmarshal(r.Truenas, &f) != nil {
			continue
		}
		v := Server{NodeID: r.ID, Hostname: r.Hostname, Address: r.Address, Version: f.Version, Pools: f.Pools, Persistent: f.Persistent, HeartbeatAt: r.HeartbeatAt}
		if v.Pools == nil {
			v.Pools = []protocol.TrueNASPool{}
		}
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

// Server geeft één TrueNAS.
func (s *Service) Server(ctx context.Context, id uuid.UUID) (Server, error) {
	list, err := s.Servers(ctx)
	if err != nil {
		return Server{}, err
	}
	for _, v := range list {
		if v.NodeID == id {
			return v, nil
		}
	}
	return Server{}, ErrNotFound
}

type cacheEntry struct {
	at   time.Time
	data json.RawMessage
}

type getCache struct {
	mu sync.Mutex
	m  map[string]cacheEntry
}

const getCacheTTL = 4 * time.Second

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
	if c.m == nil || len(c.m) > 500 {
		c.m = map[string]cacheEntry{}
	}
	c.m[k] = cacheEntry{at: time.Now(), data: v}
}

func (c *getCache) clear() {
	c.mu.Lock()
	c.m = nil
	c.mu.Unlock()
}

// Get roept een leesmethode aan. Een kijker leest geen methodes met
// geheimen (readDenied).
func (s *Service) Get(ctx context.Context, id uuid.UUID, method string, args json.RawMessage, admin bool) (json.RawMessage, error) {
	req := protocol.PVERequest{Op: protocol.TrueNASOpGet, Path: method, Args: args}
	if err := req.ValidateTrueNASGet(); err != nil {
		return nil, ValidationError{err.Error()}
	}
	if !admin && readDenied(method) {
		return nil, ValidationError{"alleen een beheerder mag " + method + " lezen"}
	}
	v, err := s.Server(ctx, id)
	if err != nil {
		return nil, err
	}
	if v.Why != "" {
		return nil, ValidationError{v.Why}
	}
	return s.get(ctx, id, req)
}

func (s *Service) get(ctx context.Context, id uuid.UUID, req protocol.PVERequest) (json.RawMessage, error) {
	key := id.String() + req.Path + "?" + string(req.Args)
	if d, ok := s.cache.get(key); ok {
		return d, nil
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
	data := scrubSecrets(res.Data)
	s.cache.put(key, data)
	return data, nil
}

// secretKey zijn velden die nooit de server verlaten, wie er ook leest:
// wachtwoorden en sleutels in cloud-sync, replicatie en dergelijke.
func secretKey(k string) bool {
	k = strings.ToLower(k)
	for _, w := range []string{"password", "passphrase", "secret", "private_key", "privatekey", "access_key", "token", "pass"} {
		if k == w || strings.HasSuffix(k, "_"+w) || strings.HasPrefix(k, w+"_") {
			return true
		}
	}
	return false
}

// scrubSecrets haalt geheime velden uit een antwoord. Bij credentials
// gaan ook de attributes weg (de sleutels van de cloudprovider).
func scrubSecrets(raw json.RawMessage) json.RawMessage {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return raw
	}
	changed := false
	var walk func(x any)
	walk = func(x any) {
		switch t := x.(type) {
		case map[string]any:
			for k, val := range t {
				if secretKey(k) {
					t[k] = "[geheim]"
					changed = true
					continue
				}
				if k == "credentials" {
					if m, ok := val.(map[string]any); ok {
						if _, has := m["attributes"]; has {
							delete(m, "attributes")
							changed = true
						}
					}
				}
				walk(val)
			}
		case []any:
			for _, e := range t {
				walk(e)
			}
		}
	}
	walk(v)
	if !changed {
		return raw
	}
	out, err := json.Marshal(v)
	if err != nil {
		return raw
	}
	return out
}

// Call is een aanroep vanuit de GUI.
type Call struct {
	Method string          `json:"method"`
	Args   json.RawMessage `json:"args"`
	// Confirm is wat Jonas overtikte bij een gevaarlijke aanroep.
	Confirm string `json:"confirm"`
}

type params struct {
	NodeID uuid.UUID                  `json:"node_id"`
	Server string                     `json:"server"`
	Call   protocol.TrueNASMethodCall `json:"call"`
}

// StartCall zet een aanroep als taak in de wachtrij.
func (s *Service) StartCall(ctx context.Context, actor events.Actor, id uuid.UUID, c Call) (store.Job, error) {
	if len(c.Args) == 0 {
		c.Args = json.RawMessage("[]")
	}
	call := protocol.TrueNASMethodCall{Method: c.Method, Args: c.Args}
	if err := call.Validate(); err != nil {
		return store.Job{}, ValidationError{err.Error()}
	}
	ok, err := check(c.Method, c.Args)
	if err != nil {
		return store.Job{}, ValidationError{err.Error()}
	}
	if ok.Confirm != "" && strings.TrimSpace(c.Confirm) != ok.Confirm {
		return store.Job{}, ValidationError{fmt.Sprintf("tik %s over om te bevestigen", ok.Confirm)}
	}
	v, err := s.Server(ctx, id)
	if err != nil {
		return store.Job{}, err
	}
	if v.Why != "" {
		return store.Job{}, ValidationError{v.Why}
	}
	call.Job = ok.Job
	title := capitalize(ok.Title)
	nodeID := id
	j, err := s.jobs.Enqueue(ctx, jobs.Spec{
		Kind: Kind, Title: title, Actor: actor, NodeID: &nodeID,
		Params: params{NodeID: id, Server: v.Hostname, Call: call},
	})
	if err != nil {
		return store.Job{}, err
	}
	if err := s.ev.Write(ctx, nil, events.Event{
		Actor: actor, SubjectType: "node", SubjectID: id.String(), Action: "truenas.call_requested",
		Payload: map[string]any{"server": v.Hostname, "method": c.Method, "args": json.RawMessage(c.Args), "job_id": j.ID.String(), "title": title},
	}); err != nil {
		s.log.Warn("event schrijven mislukt", "err", err)
	}
	s.cache.clear()
	s.jobs.Kick()
	return j, nil
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// Do voert een aanroep uit op de TrueNAS van nodeID, binnen een stap.
func (s *Service) Do(ctx context.Context, log noderun.Log, ss noderun.StepState, nodeID uuid.UUID, call protocol.TrueNASMethodCall, title string) (protocol.RunStatus, error) {
	op := protocol.PVERun{Action: protocol.TrueNASCall, TrueNAS: &call}
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
		res, err := s.Do(ctx, st, st, p.NodeID, p.Call, j.Title+" op "+p.Server)
		if err != nil {
			return err
		}
		if res.State != protocol.RunOK {
			return errors.New(noderun.Describe(res))
		}
		return nil
	})
}
