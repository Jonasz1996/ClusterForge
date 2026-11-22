// Package pvehost beheert Proxmox-hosts via de cf-agent die op de host
// draait (fase 17): welke node de agent van welke host is, wat er op de host
// staat, en acties met pct, qm en pveam als taak.
package pvehost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Jonasz1996/clusterforge/internal/agents"
	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/jobs"
	"github.com/Jonasz1996/clusterforge/internal/noderun"
	"github.com/Jonasz1996/clusterforge/internal/secrets"
	"github.com/Jonasz1996/clusterforge/internal/status"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// Kind is de soort taak van een actie op een Proxmox-host.
const Kind = "pvehost.action"

// Bus praat met de agents.
type Bus interface {
	noderun.Bus
	PVE(ctx context.Context, nodeID uuid.UUID, req protocol.PVERequest) (protocol.PVEResponse, error)
}

// Syncer leest een Proxmox-koppeling opnieuw na een actie; mag nil zijn.
type Syncer interface {
	Sync(ctx context.Context, id uuid.UUID) error
}

// ValidationError is een fout in een vraag.
type ValidationError struct{ Msg string }

func (e ValidationError) Error() string { return e.Msg }

// ErrNotFound: geen Proxmox-host met een agent onder dat id.
var ErrNotFound = errors.New("geen Proxmox-host met een agent")

type Service struct {
	pool   *pgxpool.Pool
	q      *store.Queries
	ev     *events.Writer
	log    *slog.Logger
	jobs   *jobs.Runner
	bus    Bus
	agents *agents.Service
	Run    *noderun.Runner
	// Sync leest Proxmox opnieuw na een actie.
	Sync Syncer
	// AgentSHA256 geeft de sha256 van de cf-agent die de server aanbiedt,
	// per architectuur.
	AgentSHA256 func() map[string]string
	// Box versleutelt geheimen (wachtwoorden, sleutels) in de taken van
	// fase 26; nil zonder masterkey.
	Box   *secrets.Box
	cache getCache
	// consoles zijn de open consolesessies (M7).
	consoles consoleHub
	// RestorePoll is hoe vaak een terugzettest kijkt of de gast draait (tests korter).
	RestorePoll time.Duration
	// HostWait vervangt de wachttijden van host-updates (tests).
	HostWait time.Duration
}

func NewService(pool *pgxpool.Pool, ev *events.Writer, log *slog.Logger, runner *jobs.Runner, bus Bus, ag *agents.Service) *Service {
	s := &Service{
		pool: pool, q: store.New(pool), ev: ev, log: log, jobs: runner, bus: bus, agents: ag,
		Run: &noderun.Runner{Bus: bus},
	}
	if runner != nil {
		runner.Register(Kind, s.run)
	}
	return s
}

// Host is een Proxmox-host met een agent.
type Host struct {
	NodeID   uuid.UUID `json:"node_id"`
	Hostname string    `json:"hostname"`
	// PVENode is de naam van de host in Proxmox.
	PVENode     string     `json:"pve_node"`
	Cluster     string     `json:"cluster,omitempty"`
	Version     string     `json:"version,omitempty"`
	Protocol    int        `json:"protocol"`
	Agent       string     `json:"agent_version,omitempty"`
	Online      bool       `json:"online"`
	HeartbeatAt *time.Time `json:"heartbeat_at,omitempty"`
	// Supported: de agent kent fase 17 (protocol PVESince).
	Supported bool `json:"supported"`
	// ConnectionID is de Proxmox-koppeling waar deze host in zit; nil als
	// geen koppeling hem kent.
	ConnectionID   *uuid.UUID `json:"connection_id,omitempty"`
	ConnectionName string     `json:"connection_name,omitempty"`
}

// Ready zegt waarom de host geen acties kan doen; leeg als het kan.
func (h Host) Ready() string {
	switch {
	case !h.Online:
		return fmt.Sprintf("de agent op %s is offline", h.PVENode)
	case !h.Supported:
		return fmt.Sprintf("de agent op %s is te oud (protocol %d, %d nodig); werk hem bij via Nodes → Agents bijwerken", h.PVENode, h.Protocol, protocol.PVESince)
	}
	return ""
}

// Missing is een Proxmox-host uit een koppeling zonder agent.
type Missing struct {
	ConnectionID   uuid.UUID `json:"connection_id"`
	ConnectionName string    `json:"connection_name"`
	PVENode        string    `json:"pve_node"`
	Online         bool      `json:"online"`
}

// Hosts geeft de hosts met een agent en de hosts uit de koppelingen zonder.
func (s *Service) Hosts(ctx context.Context) ([]Host, []Missing, error) {
	rows, err := s.q.ListPVEHostAgents(ctx)
	if err != nil {
		return nil, nil, err
	}
	known, err := s.q.ListProxmoxHostNodes(ctx)
	if err != nil {
		return nil, nil, err
	}
	hosts := []Host{}
	have := map[string]bool{}
	for _, r := range rows {
		var f protocol.PVEFacts
		if json.Unmarshal(r.Proxmox, &f) != nil || f.Node == "" {
			continue
		}
		h := Host{NodeID: r.ID, Hostname: r.Hostname, PVENode: f.Node, Cluster: f.Cluster, Version: f.Version, HeartbeatAt: r.HeartbeatAt}
		if r.AgentProtocol != nil {
			h.Protocol = int(*r.AgentProtocol)
			h.Online = r.HeartbeatAt != nil && time.Since(*r.HeartbeatAt) < status.HeartbeatLate
		}
		if r.AgentVersion != nil {
			h.Agent = *r.AgentVersion
		}
		h.Supported = h.Protocol >= protocol.PVESince
		for _, k := range known {
			if k.PveNode == f.Node {
				id := k.ConnectionID
				h.ConnectionID, h.ConnectionName = &id, k.ConnectionName
				have[k.ConnectionID.String()+"/"+k.PveNode] = true
				break
			}
		}
		hosts = append(hosts, h)
	}
	missing := []Missing{}
	for _, k := range known {
		if !have[k.ConnectionID.String()+"/"+k.PveNode] {
			missing = append(missing, Missing{ConnectionID: k.ConnectionID, ConnectionName: k.ConnectionName, PVENode: k.PveNode, Online: k.Status == "online"})
		}
	}
	return hosts, missing, nil
}

// Host geeft de host van een node.
func (s *Service) Host(ctx context.Context, nodeID uuid.UUID) (Host, error) {
	hosts, _, err := s.Hosts(ctx)
	if err != nil {
		return Host{}, err
	}
	for _, h := range hosts {
		if h.NodeID == nodeID {
			return h, nil
		}
	}
	return Host{}, ErrNotFound
}

// ForConnection geeft de hosts met een agent van een Proxmox-koppeling,
// per naam in Proxmox.
func (s *Service) ForConnection(ctx context.Context, connID uuid.UUID) (map[string]Host, error) {
	hosts, _, err := s.Hosts(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]Host{}
	for _, h := range hosts {
		if h.ConnectionID != nil && *h.ConnectionID == connID {
			out[h.PVENode] = h
		}
	}
	return out, nil
}

// Inventory vraagt de agent wat er op de host staat.
func (s *Service) Inventory(ctx context.Context, nodeID uuid.UUID) (protocol.PVEInventory, error) {
	res, err := s.request(ctx, nodeID, protocol.PVEOpInventory)
	if err != nil {
		return protocol.PVEInventory{}, err
	}
	if res.Inventory == nil {
		return protocol.PVEInventory{}, errors.New("de agent gaf geen inventaris")
	}
	return *res.Inventory, nil
}

// Available geeft de containertemplates die de host kan downloaden.
func (s *Service) Available(ctx context.Context, nodeID uuid.UUID) ([]protocol.PVEAppliance, error) {
	res, err := s.request(ctx, nodeID, protocol.PVEOpAvailable)
	if err != nil {
		return nil, err
	}
	return res.Available, nil
}

func (s *Service) request(ctx context.Context, nodeID uuid.UUID, op string) (protocol.PVEResponse, error) {
	h, err := s.Host(ctx, nodeID)
	if err != nil {
		return protocol.PVEResponse{}, err
	}
	if why := h.Ready(); why != "" {
		return protocol.PVEResponse{}, ValidationError{why}
	}
	ctx, cancel := context.WithTimeout(ctx, protocol.PVERequestTimeout)
	defer cancel()
	res, err := s.bus.PVE(ctx, nodeID, protocol.PVERequest{Op: op})
	if err != nil {
		return protocol.PVEResponse{}, err
	}
	if res.Error != "" {
		return res, errors.New(res.Error)
	}
	return res, nil
}

// Do voert een actie uit op de host van nodeID, binnen een stap. log en ss
// zijn de uitvoer en state van die stap.
func (s *Service) Do(ctx context.Context, log noderun.Log, ss noderun.StepState, nodeID uuid.UUID, op protocol.PVERun, title string) (protocol.RunStatus, error) {
	spec := protocol.RunSpec{ID: uuid.NewString(), Kind: protocol.RunPVE, Title: title, PVE: &op}
	if err := spec.Validate(); err != nil {
		return protocol.RunStatus{}, err
	}
	return s.Run.DoWith(ctx, log, ss, nodeID, spec)
}

// Describe zegt in een paar woorden wat een actie doet.
func Describe(op protocol.PVERun) string {
	kind := "CT"
	if op.Type == "qemu" {
		kind = "VM"
	}
	id := kind + " " + strconv.Itoa(op.VMID)
	switch op.Action {
	case protocol.PVEStart:
		return id + " starten"
	case protocol.PVEStop:
		return id + " hard uitzetten"
	case protocol.PVEShutdown:
		return id + " afsluiten"
	case protocol.PVEReboot:
		return id + " herstarten"
	case protocol.PVESuspend:
		return id + " pauzeren"
	case protocol.PVEResume:
		return id + " hervatten"
	case protocol.PVESet:
		return id + " instellingen wijzigen"
	case protocol.PVEResize:
		return id + " schijf " + op.Disk + " " + op.Size
	case protocol.PVESnapshot:
		return id + " snapshot " + op.Snapshot
	case protocol.PVERollback:
		return id + " terugzetten naar " + op.Snapshot
	case protocol.PVEDelSnapshot:
		return id + " snapshot " + op.Snapshot + " verwijderen"
	case protocol.PVEMigrate:
		return id + " verhuizen naar " + op.Target
	case protocol.PVEDestroy:
		return id + " verwijderen"
	case protocol.PVECreateCT:
		return id + " (" + op.Create.Hostname + ") maken"
	case protocol.PVETemplate:
		if op.Template.File != "" {
			return "containertemplate " + op.Template.File
		}
		return "containertemplate " + strings.TrimSuffix(op.Template.Match, "_")
	case protocol.PVEBootstrap:
		return "cf-agent in " + id + " zetten"
	case protocol.PVEAPI:
		return op.API.Method + " " + op.API.Path
	case protocol.PVEPassthrough:
		if !op.Passthrough.IOMMU && len(op.Passthrough.VFIOIDs) == 0 {
			return "passthrough uitzetten"
		}
		return "passthrough instellen"
	case protocol.PVEVolumeFree:
		return op.Volume + " verwijderen"
	case protocol.PVEZpoolScrub:
		if op.Scrub.Stop {
			return "scrub van " + op.Scrub.Pool + " stoppen"
		}
		return "scrub van " + op.Scrub.Pool + " starten"
	case protocol.PVEMaintenance:
		if op.Enable {
			return "onderhoudsmodus aan"
		}
		return "onderhoudsmodus uit"
	}
	return op.Action
}

// Request vraagt een actie op een host als taak. Confirm is bij destroy de
// naam of het nummer van de gast, overgetikt.
type Request struct {
	NodeID  uuid.UUID
	Op      protocol.PVERun
	Confirm string
	// ServerURL is bij bootstrap het adres waarmee de CT ClusterForge
	// bereikt.
	ServerURL string
}

type params struct {
	NodeID  uuid.UUID       `json:"node_id"`
	PVENode string          `json:"pve_node"`
	ConnID  *uuid.UUID      `json:"connection_id,omitempty"`
	Op      protocol.PVERun `json:"op"`
	// Sealed zijn de versleutelde geheimen van een aanroep (fase 26).
	Sealed *sealed `json:"sealed,omitempty"`
	// RestoreTest is een terugzettest (fase 26 M3) in plaats van Op.
	RestoreTest *RestoreTest `json:"restore_test,omitempty"`
	// HostUpdate is een ronde host-updates (fase 26 M4).
	HostUpdate *HostUpdate `json:"host_update,omitempty"`
}

// Start zet een actie in de wachtrij.
func (s *Service) Start(ctx context.Context, actor events.Actor, req Request) (store.Job, error) {
	h, err := s.Host(ctx, req.NodeID)
	if err != nil {
		return store.Job{}, err
	}
	if why := h.Ready(); why != "" {
		return store.Job{}, ValidationError{why}
	}
	op := req.Op
	switch op.Action {
	case protocol.PVEAPI:
		return store.Job{}, ValidationError{"een API-aanroep gaat via het datacenter (StartCall)"}
	case protocol.PVEPassthrough, protocol.PVEVolumeFree, protocol.PVEZpoolScrub, protocol.PVEMaintenance:
		if why := h.APIReady(); why != "" {
			return store.Job{}, ValidationError{why}
		}
		if op.Action == protocol.PVEVolumeFree && strings.TrimSpace(req.Confirm) != op.Volume {
			return store.Job{}, ValidationError{"tik het volume over om het te verwijderen"}
		}
		if op.Action == protocol.PVEPassthrough && strings.TrimSpace(req.Confirm) != h.PVENode {
			return store.Job{}, ValidationError{fmt.Sprintf("tik %s over: passthrough verandert de kernelregel en vraagt een herstart", h.PVENode)}
		}
	case protocol.PVETemplate, protocol.PVECreateCT:
	default:
		inv, err := s.Inventory(ctx, req.NodeID)
		if err != nil {
			return store.Job{}, err
		}
		i := slices.IndexFunc(inv.Guests, func(g protocol.PVEGuest) bool { return g.VMID == op.VMID })
		if i < 0 {
			return store.Job{}, ValidationError{fmt.Sprintf("%d staat niet op %s", op.VMID, h.PVENode)}
		}
		g := inv.Guests[i]
		op.Type = g.Type
		if g.Template && op.Action != protocol.PVEDestroy && op.Action != protocol.PVESet {
			return store.Job{}, ValidationError{"dit is een template; die kun je niet starten, snapshotten of verhuizen"}
		}
		if err := s.guard(ctx, h, g, req); err != nil {
			return store.Job{}, err
		}
		if op.Action == protocol.PVEBootstrap {
			if op.Bootstrap, err = s.bootstrapSpec(ctx, actor, req.ServerURL, g, nil); err != nil {
				return store.Job{}, err
			}
		}
	}
	if err := op.Validate(); err != nil {
		return store.Job{}, ValidationError{err.Error()}
	}
	j, err := s.enqueue(ctx, actor, h, op, Describe(op)+" op "+h.PVENode, nil)
	if err != nil {
		return store.Job{}, err
	}
	payload := map[string]any{"host": h.PVENode, "action": op.Action, "vmid": op.VMID, "job_id": j.ID.String()}
	if op.Create != nil {
		payload["hostname"] = op.Create.Hostname
	}
	if err := s.ev.Write(ctx, nil, events.Event{
		Actor: actor, SubjectType: "node", SubjectID: req.NodeID.String(), Action: "pvehost.action_requested", Payload: payload,
	}); err != nil {
		s.log.Warn("event schrijven mislukt", "err", err)
	}
	s.jobs.Kick()
	return j, nil
}

func (s *Service) enqueue(ctx context.Context, actor events.Actor, h Host, op protocol.PVERun, title string, seal *sealed) (store.Job, error) {
	nodeID := h.NodeID
	spec := jobs.Spec{
		Kind: Kind, Title: title, Actor: actor, NodeID: &nodeID, ProxmoxID: h.ConnectionID,
		Params: params{NodeID: nodeID, PVENode: h.PVENode, ConnID: h.ConnectionID, Op: op, Sealed: seal},
	}
	return s.jobs.Enqueue(ctx, spec)
}

// guard houdt gevaarlijke acties tegen: verwijderen vraagt de naam, en een
// gast van een node in een cluster wordt niet zomaar verwijderd.
func (s *Service) guard(ctx context.Context, h Host, g protocol.PVEGuest, req Request) error {
	if req.Op.Action != protocol.PVEDestroy {
		return nil
	}
	c := strings.TrimSpace(req.Confirm)
	if c == "" || (c != g.Name && c != strconv.Itoa(g.VMID)) {
		return ValidationError{fmt.Sprintf("tik de naam (%s) of het nummer (%d) over om te verwijderen", g.Name, g.VMID)}
	}
	if g.Status == "running" {
		return ValidationError{"zet hem eerst uit (Afsluiten of Hard uitzetten)"}
	}
	if h.ConnectionID == nil {
		return nil
	}
	link, err := s.q.GuestNodeLink(ctx, store.GuestNodeLinkParams{ConnectionID: h.ConnectionID, Vmid: int32p(g.VMID)})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if link.ClusterID != nil {
		return ValidationError{fmt.Sprintf("%s is node %s van cluster %s; haal hem eerst uit het cluster (Node verwijderen)", g.Name, link.Hostname, link.ClusterName)}
	}
	return nil
}

// BootstrapSpec maakt het verzoek om cf-agent in een CT te zetten, met een
// eenmalig token. Met nodeID meldt de agent zich als die node; anders maakt
// de aanmelding een nieuwe node.
func (s *Service) BootstrapSpec(ctx context.Context, actor events.Actor, serverURL string, ctName string, ip string, nodeID *uuid.UUID) (*protocol.PVEBootstrapSpec, error) {
	return s.bootstrapSpec(ctx, actor, serverURL, protocol.PVEGuest{Type: "lxc", Name: ctName}, nodeID, ip)
}

func (s *Service) bootstrapSpec(ctx context.Context, actor events.Actor, serverURL string, g protocol.PVEGuest, nodeID *uuid.UUID, ip ...string) (*protocol.PVEBootstrapSpec, error) {
	if g.Type != "lxc" {
		return nil, ValidationError{"cf-agent erin zetten kan alleen bij een CT; een VM krijgt hem via de golden image"}
	}
	serverURL = strings.TrimRight(strings.TrimSpace(serverURL), "/")
	if !strings.HasPrefix(serverURL, "https://") && !strings.HasPrefix(serverURL, "http://") {
		return nil, ValidationError{"geef het adres waarmee de CT ClusterForge bereikt, zoals http://192.168.113.153:8080"}
	}
	sums := map[string]string{}
	if s.AgentSHA256 != nil {
		// Een CT is Linux: alleen amd64 en arm64.
		for arch, sum := range s.AgentSHA256() {
			if arch == "amd64" || arch == "arm64" {
				sums[arch] = sum
			}
		}
	}
	if len(sums) == 0 {
		return nil, ValidationError{"deze server biedt geen cf-agent aan (geen sha256 naast de binaries); is het image compleet?"}
	}
	by := uuid.Nil
	if id, err := uuid.Parse(actor.ID); err == nil && actor.Type == store.ActorTypeUser {
		by = id
	}
	desc := "CT " + g.Name + " via de Proxmox-host"
	if len(desc) > 200 {
		desc = desc[:200]
	}
	token, _, err := s.agents.CreateToken(ctx, by, agents.TokenInput{Description: desc, NodeID: nodeID, MaxUses: 1, TTL: time.Hour})
	if err != nil {
		return nil, err
	}
	b := &protocol.PVEBootstrapSpec{Server: serverURL, AllowHTTP: strings.HasPrefix(serverURL, "http://"), Token: token, SHA256: sums}
	if len(ip) > 0 {
		b.KnownHost = ip[0]
	} else if a := ctAddress(g); a != "" {
		b.KnownHost = a
	}
	return b, nil
}

// ctAddress haalt het vaste adres uit net0 van een CT.
func ctAddress(g protocol.PVEGuest) string {
	for _, n := range g.Nets {
		for _, part := range strings.Split(n, ",") {
			if v, ok := strings.CutPrefix(strings.TrimSpace(part), "ip="); ok && v != "dhcp" && v != "manual" {
				a, _, _ := strings.Cut(v, "/")
				return a
			}
		}
	}
	return ""
}

func (s *Service) run(ctx context.Context, j *jobs.Job) error {
	var p params
	if err := j.Decode(&p); err != nil {
		return err
	}
	if p.RestoreTest != nil {
		return s.runRestoreTest(ctx, j, p)
	}
	if p.HostUpdate != nil {
		return s.runHostUpdate(ctx, j, p)
	}
	if err := s.openSealed(&p); err != nil {
		return err
	}
	title := Describe(p.Op)
	if p.Op.Action == protocol.PVEAPI {
		title = j.Title
	}
	err := j.Step(ctx, title, func(ctx context.Context, st *jobs.Step) error {
		res, err := s.Do(ctx, st, st, p.NodeID, p.Op, title+" op "+p.PVENode)
		if err != nil {
			return err
		}
		if res.State != protocol.RunOK {
			return errors.New(noderun.Describe(res))
		}
		return nil
	})
	if p.ConnID != nil && s.Sync != nil {
		sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		_ = s.Sync.Sync(sctx, *p.ConnID)
	}
	return err
}

func int32p(n int) *int32 {
	v := int32(n)
	return &v
}
