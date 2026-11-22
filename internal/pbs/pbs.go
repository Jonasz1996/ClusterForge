// Package pbs beheert Proxmox Backup Servers via de cf-agent die op de
// server draait (fase 32). Lezen gaat rechtstreeks (proxmox-backup-debug
// api get, met een korte cache), veranderen als taak met de run-actie
// pbs_api. Welke aanroepen de GUI mag doen, staat in policy.go.
package pbs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/jobs"
	"github.com/Jonasz1996/clusterforge/internal/noderun"
	"github.com/Jonasz1996/clusterforge/internal/secrets"
	"github.com/Jonasz1996/clusterforge/internal/status"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// Kind is de soort taak van een aanroep op een PBS.
const Kind = "pbs.action"

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

// ErrNotFound: geen PBS met een agent onder dat id.
var ErrNotFound = errors.New("geen Proxmox Backup Server met een agent")

type Service struct {
	q    *store.Queries
	ev   *events.Writer
	log  *slog.Logger
	jobs *jobs.Runner
	bus  Bus
	Run  *noderun.Runner
	// Box versleutelt wachtwoorden in de taken; nil zonder masterkey.
	Box   *secrets.Box
	cache getCache
	// Now is er voor tests.
	Now func() time.Time
	// Hosts koppelt een PBS aan een Proxmox-cluster (M5); nil zonder.
	Hosts StorageAdder
	watch watchState
}

func NewService(pool *pgxpool.Pool, ev *events.Writer, log *slog.Logger, runner *jobs.Runner, bus Bus) *Service {
	s := &Service{q: store.New(pool), ev: ev, log: log, jobs: runner, bus: bus, Run: &noderun.Runner{Bus: bus}, Now: time.Now}
	if runner != nil {
		runner.Register(Kind, s.run)
	}
	return s
}

// Server is een Proxmox Backup Server met een agent.
type Server struct {
	NodeID   uuid.UUID `json:"node_id"`
	Hostname string    `json:"hostname"`
	Node     string    `json:"node"`
	// Address is het adres van de node in ClusterForge; Proxmox bereikt de
	// PBS meestal daarop.
	Address     string     `json:"address,omitempty"`
	Version     string     `json:"version,omitempty"`
	Datastores  []string   `json:"datastores"`
	Protocol    int        `json:"protocol"`
	Agent       string     `json:"agent_version,omitempty"`
	Online      bool       `json:"online"`
	HeartbeatAt *time.Time `json:"heartbeat_at,omitempty"`
	// Why zegt waarom ClusterForge de server niet kan beheren; leeg als het kan.
	Why string `json:"why,omitempty"`
}

func (v Server) ready() string {
	switch {
	case !v.Online:
		return fmt.Sprintf("de agent op %s is offline", v.Hostname)
	case v.Protocol < protocol.PBSSince:
		return fmt.Sprintf("de agent op %s is te oud voor PBS-beheer (protocol %d, %d nodig); werk hem bij via Nodes → Agents bijwerken", v.Hostname, v.Protocol, protocol.PBSSince)
	}
	return ""
}

// Servers geeft alle PBS'en met een agent.
func (s *Service) Servers(ctx context.Context) ([]Server, error) {
	rows, err := s.q.ListPBSAgents(ctx)
	if err != nil {
		return nil, err
	}
	out := []Server{}
	for _, r := range rows {
		var f protocol.PBSFacts
		if json.Unmarshal(r.Pbs, &f) != nil {
			continue
		}
		v := Server{NodeID: r.ID, Hostname: r.Hostname, Address: r.Address, Node: f.Node, Version: f.Version, Datastores: f.Datastores, HeartbeatAt: r.HeartbeatAt}
		if v.Datastores == nil {
			v.Datastores = []string{}
		}
		if v.Node == "" {
			v.Node = r.Hostname
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

// Server geeft één PBS.
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

// getCache houdt antwoorden een paar seconden bij: kaarten van dezelfde
// pagina vragen vaak hetzelfde.
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

// Get leest een pad van de PBS-API. Een kijker leest niets onder /access
// of de configuratie van remotes, ACME en meldingen.
func (s *Service) Get(ctx context.Context, id uuid.UUID, path string, params map[string]string, admin bool) (json.RawMessage, error) {
	req := protocol.PVERequest{Op: protocol.PBSOpGet, Path: path, Params: params}
	if err := req.ValidatePBSGet(); err != nil {
		return nil, ValidationError{err.Error()}
	}
	if !admin && readDenied(path) {
		return nil, ValidationError{"alleen een beheerder mag " + path + " lezen"}
	}
	v, err := s.Server(ctx, id)
	if err != nil {
		return nil, err
	}
	if v.Why != "" {
		return nil, ValidationError{v.Why}
	}
	key := id.String() + path + "?" + canonical(params)
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
	s.cache.put(key, res.Data)
	return res.Data, nil
}

// GetInto leest een pad in out.
func (s *Service) GetInto(ctx context.Context, id uuid.UUID, path string, params map[string]string, out any) error {
	data, err := s.Get(ctx, id, path, params, true)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, out)
}

func canonical(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k + "=" + m[k] + "&")
	}
	return b.String()
}

// Call is een aanroep van de API vanuit de GUI.
type Call struct {
	Method string            `json:"method"`
	Path   string            `json:"path"`
	Params map[string]string `json:"params"`
	// Confirm is wat Jonas overtikte bij een gevaarlijke aanroep.
	Confirm string `json:"confirm"`
}

type sealed struct {
	KeyID string `json:"key_id"`
	Nonce string `json:"nonce"`
	Data  []byte `json:"data"`
}

func sealAAD(nonce string) []byte { return []byte("pbs-api:" + nonce) }

type params struct {
	NodeID uuid.UUID           `json:"node_id"`
	Server string              `json:"server"`
	Call   protocol.PVEAPICall `json:"call"`
	Sealed *sealed             `json:"sealed,omitempty"`
}

// StartCall zet een aanroep als taak in de wachtrij.
func (s *Service) StartCall(ctx context.Context, actor events.Actor, id uuid.UUID, c Call) (store.Job, error) {
	if c.Params == nil {
		c.Params = map[string]string{}
	}
	call := protocol.PVEAPICall{Method: c.Method, Path: c.Path, Params: c.Params}
	if err := call.ValidatePBS(); err != nil {
		return store.Job{}, ValidationError{err.Error()}
	}
	ok, err := check(c.Method, c.Path, c.Params)
	if err != nil {
		return store.Job{}, ValidationError{err.Error()}
	}
	if ok.Quiet {
		return store.Job{}, ValidationError{"een API-token maken of vernieuwen gaat via Toegang → Token maken, zodat de waarde niet in een taak komt"}
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
	var seal *sealed
	stored := map[string]string{}
	for k, val := range c.Params {
		if !slices.Contains(ok.Secret, k) {
			stored[k] = val
		}
	}
	var secretKeys []string
	plain := map[string]string{}
	for _, k := range ok.Secret {
		if val, has := c.Params[k]; has {
			plain[k] = val
			secretKeys = append(secretKeys, k)
		}
	}
	if len(plain) > 0 {
		if s.Box == nil {
			return store.Job{}, ValidationError{"deze server heeft geen masterkey; zonder kan ClusterForge geen wachtwoorden doorgeven"}
		}
		data, _ := json.Marshal(plain)
		nonce := uuid.NewString()
		seal = &sealed{KeyID: s.Box.KeyID, Nonce: nonce, Data: s.Box.Seal(data, sealAAD(nonce))}
	}
	call.Params, call.Secret, call.Quiet = stored, secretKeys, ok.Quiet
	title := capitalize(ok.Title)
	nodeID := id
	j, err := s.jobs.Enqueue(ctx, jobs.Spec{
		Kind: Kind, Title: title, Actor: actor, NodeID: &nodeID,
		Params: params{NodeID: id, Server: v.Hostname, Call: call, Sealed: seal},
	})
	if err != nil {
		return store.Job{}, err
	}
	shown := map[string]string{}
	for k, val := range stored {
		shown[k] = val
	}
	for _, k := range secretKeys {
		shown[k] = "[geheim]"
	}
	if err := s.ev.Write(ctx, nil, events.Event{
		Actor: actor, SubjectType: "node", SubjectID: id.String(), Action: "pbs.api_requested",
		Payload: map[string]any{"server": v.Hostname, "method": c.Method, "path": c.Path, "params": shown, "job_id": j.ID.String(), "title": title},
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

func (s *Service) openSealed(p *params) error {
	if p.Sealed == nil {
		return nil
	}
	if s.Box == nil {
		return errors.New("geen masterkey meer: de geheimen van deze taak zijn niet te lezen")
	}
	data, err := s.Box.Open(p.Sealed.Data, sealAAD(p.Sealed.Nonce), p.Sealed.KeyID)
	if err != nil {
		if errors.Is(err, secrets.ErrWrongKey) {
			return errors.New("de masterkey is veranderd sinds deze taak gemaakt is; start hem opnieuw")
		}
		return err
	}
	var plain map[string]string
	if err := json.Unmarshal(data, &plain); err != nil {
		return err
	}
	if p.Call.Params == nil {
		p.Call.Params = map[string]string{}
	}
	for k, v := range plain {
		p.Call.Params[k] = v
	}
	return nil
}

// Do voert een aanroep uit op de PBS van nodeID, binnen een stap.
func (s *Service) Do(ctx context.Context, log noderun.Log, ss noderun.StepState, nodeID uuid.UUID, call protocol.PVEAPICall, title string) (protocol.RunStatus, error) {
	op := protocol.PVERun{Action: protocol.PBSAPI, API: &call}
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
	if err := s.openSealed(&p); err != nil {
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
