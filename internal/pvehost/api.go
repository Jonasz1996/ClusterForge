package pvehost

// Fase 26: de API van Proxmox via de agents op de hosts. Lezen gaat
// rechtstreeks (met een korte cache), veranderen als taak met de run-actie
// api. Een pad dat over een host gaat (/nodes/<host>/…), gaat naar de
// agent op die host; de rest naar een agent in hetzelfde Proxmox-cluster.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/secrets"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// APIReady zegt waarom de host de API niet doorgeeft; leeg als het kan.
func (h Host) APIReady() string {
	if why := h.Ready(); why != "" {
		return why
	}
	if h.Protocol < protocol.PVEAPISince {
		return fmt.Sprintf("de agent op %s is te oud voor volledig Proxmox-beheer (protocol %d, %d nodig); werk hem bij via Nodes → Agents bijwerken", h.PVENode, h.Protocol, protocol.PVEAPISince)
	}
	return ""
}

// Datacenter is een Proxmox-cluster, of een losse host.
type Datacenter struct {
	// ID is de clusternaam, of host:<naam> voor een losse host.
	ID   string `json:"id"`
	Name string `json:"name"`
	// Standalone: een host zonder Proxmox-cluster.
	Standalone bool   `json:"standalone"`
	Hosts      []Host `json:"hosts"`
	// Via is de host waarlangs ClusterForge het cluster leest; nil als geen
	// agent het kan.
	Via *uuid.UUID `json:"via,omitempty"`
	// Why zegt waarom er geen Via is.
	Why string `json:"why,omitempty"`
}

func dcID(h Host) string {
	if h.Cluster != "" {
		return h.Cluster
	}
	return "host:" + h.PVENode
}

// Datacenters groepeert de hosts met een agent per Proxmox-cluster.
func (s *Service) Datacenters(ctx context.Context) ([]Datacenter, error) {
	hosts, _, err := s.Hosts(ctx)
	if err != nil {
		return nil, err
	}
	by := map[string]*Datacenter{}
	var order []string
	for _, h := range hosts {
		id := dcID(h)
		dc, ok := by[id]
		if !ok {
			dc = &Datacenter{ID: id, Name: h.Cluster, Standalone: h.Cluster == ""}
			if dc.Standalone {
				dc.Name = h.PVENode
			}
			by[id] = dc
			order = append(order, id)
		}
		dc.Hosts = append(dc.Hosts, h)
	}
	sort.Strings(order)
	out := make([]Datacenter, 0, len(order))
	for _, id := range order {
		dc := by[id]
		sort.Slice(dc.Hosts, func(i, j int) bool { return dc.Hosts[i].PVENode < dc.Hosts[j].PVENode })
		for _, h := range dc.Hosts {
			if h.APIReady() == "" {
				nid := h.NodeID
				dc.Via = &nid
				break
			}
		}
		if dc.Via == nil && len(dc.Hosts) > 0 {
			dc.Why = dc.Hosts[0].APIReady()
		}
		out = append(out, *dc)
	}
	return out, nil
}

// Datacenter geeft één datacenter.
func (s *Service) Datacenter(ctx context.Context, id string) (Datacenter, error) {
	list, err := s.Datacenters(ctx)
	if err != nil {
		return Datacenter{}, err
	}
	for _, dc := range list {
		if dc.ID == id {
			return dc, nil
		}
	}
	return Datacenter{}, ErrNotFound
}

// route kiest de agent voor een pad: die op de host uit het pad als die
// klaar is, anders een andere in hetzelfde cluster (pvesh stuurt het dan
// door).
func (s *Service) route(ctx context.Context, dcID, path string) (Host, error) {
	dc, err := s.Datacenter(ctx, dcID)
	if err != nil {
		return Host{}, err
	}
	if rest, ok := strings.CutPrefix(path, "/nodes/"); ok {
		node, _, _ := strings.Cut(rest, "/")
		for _, h := range dc.Hosts {
			if h.PVENode == node && h.APIReady() == "" {
				return h, nil
			}
		}
	}
	for _, h := range dc.Hosts {
		if h.APIReady() == "" {
			return h, nil
		}
	}
	if dc.Why != "" {
		return Host{}, ValidationError{dc.Why}
	}
	return Host{}, ValidationError{"geen host met een agent in " + dc.Name}
}

type cacheEntry struct {
	at   time.Time
	data json.RawMessage
}

// getCache houdt antwoorden een paar seconden bij: pvesh is traag, en
// verschillende kaarten van dezelfde pagina vragen vaak hetzelfde.
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

// Get leest een pad van de Proxmox-API. admin mag alles lezen; een kijker
// niet waar geheimen kunnen staan.
func (s *Service) Get(ctx context.Context, dcID, path string, params map[string]string, admin bool) (json.RawMessage, error) {
	req := protocol.PVERequest{Op: protocol.PVEOpGet, Path: path, Params: params}
	if err := req.ValidateGet(); err != nil {
		return nil, ValidationError{err.Error()}
	}
	if !admin && readDenied(path) {
		return nil, ValidationError{"alleen een beheerder mag " + path + " lezen"}
	}
	h, err := s.route(ctx, dcID, path)
	if err != nil {
		return nil, err
	}
	key := h.NodeID.String() + path + "?" + canonical(params)
	if v, ok := s.cache.get(key); ok {
		return v, nil
	}
	ctx, cancel := context.WithTimeout(ctx, protocol.PVERequestTimeout)
	defer cancel()
	res, err := s.bus.PVE(ctx, h.NodeID, req)
	if err != nil {
		return nil, err
	}
	if res.Error != "" {
		return nil, errors.New(res.Error)
	}
	s.cache.put(key, res.Data)
	return res.Data, nil
}

// GetInto leest een pad in v.
func (s *Service) GetInto(ctx context.Context, dcID, path string, params map[string]string, v any) error {
	data, err := s.Get(ctx, dcID, path, params, true)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
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

// Hardware scant de hardware van een host.
func (s *Service) Hardware(ctx context.Context, nodeID uuid.UUID) (protocol.PVEHardware, error) {
	h, err := s.Host(ctx, nodeID)
	if err != nil {
		return protocol.PVEHardware{}, err
	}
	if why := h.APIReady(); why != "" {
		return protocol.PVEHardware{}, ValidationError{why}
	}
	ctx, cancel := context.WithTimeout(ctx, protocol.PVERequestTimeout)
	defer cancel()
	res, err := s.bus.PVE(ctx, nodeID, protocol.PVERequest{Op: protocol.PVEOpHardware})
	if err != nil {
		return protocol.PVEHardware{}, err
	}
	if res.Error != "" {
		return protocol.PVEHardware{}, errors.New(res.Error)
	}
	if res.Hardware == nil {
		return protocol.PVEHardware{}, errors.New("de agent gaf geen hardwarescan")
	}
	return *res.Hardware, nil
}

// Call is een aanroep van de API vanuit de GUI.
type Call struct {
	Datacenter string            `json:"datacenter"`
	Method     string            `json:"method"`
	Path       string            `json:"path"`
	Params     map[string]string `json:"params"`
	// Confirm is wat Jonas overtikte bij een gevaarlijke aanroep.
	Confirm string `json:"confirm"`
}

// sealed is een versleuteld geheim in de params van een taak.
type sealed struct {
	KeyID string `json:"key_id"`
	Nonce string `json:"nonce"`
	Data  []byte `json:"data"`
}

func sealAAD(nonce string) []byte { return []byte("pvehost-api:" + nonce) }

// StartCall zet een aanroep als taak in de wachtrij.
func (s *Service) StartCall(ctx context.Context, actor events.Actor, c Call) (store.Job, error) {
	if c.Params == nil {
		c.Params = map[string]string{}
	}
	call := protocol.PVEAPICall{Method: c.Method, Path: c.Path, Params: c.Params}
	if err := call.Validate(); err != nil {
		return store.Job{}, ValidationError{err.Error()}
	}
	ok, err := check(c.Method, c.Path, c.Params)
	if err != nil {
		return store.Job{}, ValidationError{err.Error()}
	}
	if ok.Confirm != "" && strings.TrimSpace(c.Confirm) != ok.Confirm {
		return store.Job{}, ValidationError{fmt.Sprintf("tik %s over om te bevestigen", ok.Confirm)}
	}
	h, err := s.route(ctx, c.Datacenter, c.Path)
	if err != nil {
		return store.Job{}, err
	}
	if err := s.guardCall(ctx, h, c); err != nil {
		return store.Job{}, err
	}
	// Geheimen gaan versleuteld in de taak, nooit leesbaar in de database.
	stored := map[string]string{}
	var seal *sealed
	if len(ok.Secret) > 0 {
		if s.Box == nil {
			return store.Job{}, ValidationError{"deze server heeft geen masterkey; zonder kan ClusterForge geen wachtwoorden of sleutels doorgeven"}
		}
		plain := map[string]string{}
		for _, k := range ok.Secret {
			plain[k] = c.Params[k]
		}
		data, _ := json.Marshal(plain)
		nonce := uuid.NewString()
		seal = &sealed{KeyID: s.Box.KeyID, Nonce: nonce, Data: s.Box.Seal(data, sealAAD(nonce))}
	}
	for k, v := range c.Params {
		if !slices.Contains(ok.Secret, k) {
			stored[k] = v
		}
	}
	call.Params, call.Secret = stored, ok.Secret
	op := protocol.PVERun{Action: protocol.PVEAPI, API: &call}
	title := capitalize(ok.Title)
	j, err := s.enqueue(ctx, actor, h, op, title, seal)
	if err != nil {
		return store.Job{}, err
	}
	shown := map[string]string{}
	for k, v := range stored {
		shown[k] = v
	}
	for _, k := range ok.Secret {
		shown[k] = "[geheim]"
	}
	s.audit(ctx, actor, h, "pvehost.api_requested", map[string]any{
		"datacenter": c.Datacenter, "method": c.Method, "path": c.Path, "params": shown, "job_id": j.ID.String(), "title": title,
	})
	s.cache.clear()
	return j, nil
}

// guardCall houdt aanroepen tegen die een node van een ClusterForge-cluster
// kapotmaken: terugzetten over een gast die een clusternode is.
func (s *Service) guardCall(ctx context.Context, h Host, c Call) error {
	if c.Method != "create" || c.Params["force"] != "1" || (!strings.HasSuffix(c.Path, "/qemu") && !strings.HasSuffix(c.Path, "/lxc")) {
		return nil
	}
	if h.ConnectionID == nil {
		return nil
	}
	var vmid int
	if _, err := fmt.Sscan(c.Params["vmid"], &vmid); err != nil {
		return ValidationError{"ongeldig VMID"}
	}
	link, err := s.q.GuestNodeLink(ctx, store.GuestNodeLinkParams{ConnectionID: h.ConnectionID, Vmid: int32p(vmid)})
	if err != nil {
		return nil //nolint:nilerr // geen koppeling: geen clusternode
	}
	if link.ClusterID != nil {
		return ValidationError{fmt.Sprintf("%d is node %s van cluster %s; terugzetten over een clusternode gaat via Back-ups bij dat cluster, of haal hem eerst uit het cluster", vmid, link.Hostname, link.ClusterName)}
	}
	return nil
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func (s *Service) audit(ctx context.Context, actor events.Actor, h Host, action string, payload map[string]any) {
	payload["host"] = h.PVENode
	if err := s.ev.Write(ctx, nil, events.Event{
		Actor: actor, SubjectType: "node", SubjectID: h.NodeID.String(), Action: action, Payload: payload,
	}); err != nil {
		s.log.Warn("event schrijven mislukt", "err", err)
	}
}

// openSealed haalt de geheimen terug in de aanroep.
func (s *Service) openSealed(p *params) error {
	if p.Sealed == nil || p.Op.API == nil {
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
	if p.Op.API.Params == nil {
		p.Op.API.Params = map[string]string{}
	}
	for k, v := range plain {
		p.Op.API.Params[k] = v
	}
	return nil
}

// AddStorage zet een storage in een Proxmox-cluster (fase 32: een PBS
// koppelen). Het wachtwoord gaat versleuteld in de taak.
func (s *Service) AddStorage(ctx context.Context, actor events.Actor, datacenter string, params map[string]string) (store.Job, error) {
	return s.StartCall(ctx, actor, Call{Datacenter: datacenter, Method: "create", Path: "/storage", Params: params})
}
