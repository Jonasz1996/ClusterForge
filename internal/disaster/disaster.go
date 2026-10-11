// Package disaster voert rampoefeningen uit in lab en test: een hele
// Proxmox-host, een dienst op alle nodes of het VIP van een cluster valt met
// opzet weg. Vooraf voorspelt de wat-als welke clusters geraakt worden.
// Tijdens de storing meet de server met de probes van de failovertest op de
// VIP's van elk geraakt cluster en van elk cluster dat er volgens de graaf
// van afhangt, en leest hij de status van die clusters. Het rapport zet
// voorspeld en gemeten naast elkaar. Daarna zet ClusterForge altijd alles
// terug, met dezelfde vangnetten als de failovertest.
package disaster

import (
	"cmp"
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

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/failover"
	"github.com/Jonasz1996/clusterforge/internal/health"
	"github.com/Jonasz1996/clusterforge/internal/jobs"
	"github.com/Jonasz1996/clusterforge/internal/proxmox"
	"github.com/Jonasz1996/clusterforge/internal/risk"
	"github.com/Jonasz1996/clusterforge/internal/status"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/internal/topo"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// Soorten rampoefening.
const (
	// HostDown zet alle gekoppelde VM's op één Proxmox-host tegelijk hard
	// uit. De host zelf blijft aan.
	HostDown = "host_down"
	// ServiceDown stopt één dienst op alle nodes van het cluster.
	ServiceDown = "service_down"
	// VIPLoss stopt keepalived op alle nodes, zodat het VIP geen houder
	// heeft.
	VIPLoss = "vip_loss"
	// Sequence is een reeks van hoogstens vijf storingen met een tijdstip:
	// de vaste storingen van de agent (protocol 6) en een dienst stoppen.
	// Aan het einde van de storing heft ClusterForge ze allemaal op.
	Sequence = "sequence"
)

// Soorten taak.
const (
	KindRun     = "disaster.run"
	KindRestore = "disaster.restore"
)

// Units zijn de diensten die service_down mag stoppen: die van de
// failovertest en, alleen hier in lab en test, de databases. Naar de agent
// gaat nooit een andere unit dan deze of keepalived.
var Units = append(slices.Clone(failover.Units), failover.Databases...)

// Kind is een soort met de standaardduur van de storing en de verwachte
// hersteltijd.
type Kind struct {
	Key             string
	Label           string
	FaultSeconds    int
	RecoverySeconds int
}

// Kinds staan in de volgorde van het formulier. Na een harde stop moeten de
// VM's opstarten, dus het herstel duurt langer.
var Kinds = []Kind{
	{HostDown, "Proxmox-host valt uit", 120, 600},
	{ServiceDown, "Dienst valt weg op alle nodes", 60, 120},
	{VIPLoss, "VIP valt weg", 60, 120},
	{Sequence, "Reeks van storingen", 90, 120},
}

func kindOf(key string) (Kind, bool) {
	i := slices.IndexFunc(Kinds, func(k Kind) bool { return k.Key == key })
	if i < 0 {
		return Kind{}, false
	}
	return Kinds[i], true
}

// Grenzen van de verwachting. Een storing duurt kort genoeg om nooit lang
// iets kapot te laten, en lang genoeg om uitval van een korte overname te
// onderscheiden.
const (
	MinFaultSeconds    = 20
	MaxFaultSeconds    = 600
	MinRecoverySeconds = 10
	MaxRecoverySeconds = 1800
)

// MinHostDownSeconds is de kortste storing bij host_down: na een harde stop
// is een node pas down als hij 90 s geen heartbeat stuurde. Bij een kortere
// storing ziet de meting een cluster zonder VIP niet uitvallen.
var MinHostDownSeconds = int((status.HeartbeatDown + 10*time.Second).Seconds())

// MinFault is de kortste storing van een soort.
func MinFault(kind string) int {
	if kind == HostDown {
		return MinHostDownSeconds
	}
	return MinFaultSeconds
}

// Params zegt wat er uitvalt.
type Params struct {
	// ConnectionID en Host zijn bij host_down de Proxmox-host.
	ConnectionID *uuid.UUID `json:"connection_id,omitempty"`
	Host         string     `json:"host,omitempty"`
	// Unit is bij service_down de dienst.
	Unit string `json:"unit,omitempty"`
	// Steps zijn bij sequence de stappen, op volgorde van tijdstip.
	Steps []Step `json:"steps,omitempty"`
}

// Expect is de verwachting: hoe lang de storing duurt en hoe snel na het
// terugzetten alles weer gezond moet zijn.
type Expect struct {
	FaultSeconds    int `json:"fault_seconds"`
	RecoverySeconds int `json:"recovery_seconds"`
}

func (e Expect) fault() time.Duration    { return time.Duration(e.FaultSeconds) * time.Second }
func (e Expect) recovery() time.Duration { return time.Duration(e.RecoverySeconds) * time.Second }

// Describe zegt in gewone taal wat een rampoefening doet.
func Describe(kind string, p Params) string {
	switch kind {
	case HostDown:
		return "Proxmox-host " + p.Host + " valt uit: alle gekoppelde VM's erop hard uitzetten"
	case ServiceDown:
		return p.Unit + " stoppen op alle nodes"
	case VIPLoss:
		return "keepalived stoppen op alle nodes, zodat het VIP geen houder heeft"
	case Sequence:
		return describeSteps(p.Steps)
	}
	return kind
}

// unitOf is de unit die de storing stopt en het herstel weer start, of bij
// host_down leeg.
func unitOf(kind string, p Params) string {
	switch kind {
	case ServiceDown:
		return p.Unit
	case VIPLoss:
		return "keepalived"
	}
	return ""
}

var ErrNotFound = failover.ErrNotFound

// msgProd is waarom een rampoefening op prod niet kan.
const msgProd = "live rampoefeningen kunnen alleen in lab en test; voor prod is er de voorspelling bij Risico, Wat als"

// Service beheert de scenario's en voert ze uit.
type Service struct {
	pool *pgxpool.Pool
	q    *store.Queries
	ev   *events.Writer
	log  *slog.Logger
	jobs *jobs.Runner
	bus  failover.Commander

	// Proxmox zet bij host_down de VM's uit en aan; nil zonder masterkey.
	Proxmox failover.Proxmox
	// World leest wat de wat-als nodig heeft: de infrastructuur en de
	// afhankelijkheden.
	World func(ctx context.Context) (risk.WhatIfWorld, error)
	// Probes geeft de standaardprobe van een VIP, zoals bij de
	// failovertest.
	Probes func(c store.Cluster, vip string) failover.Probe
	// Gate wacht op verse heartbeats.
	Gate *health.Gate
	// Prober vraagt de VIP's op; tests zetten er een nagespeelde.
	Prober failover.Prober
	// ProbeInterval is hoe vaak de meting elk VIP opvraagt.
	ProbeInterval time.Duration
	// StatusPoll is hoe vaak de meting de status van de clusters leest.
	StatusPoll time.Duration
	// ShortOutage: een onderbreking die korter duurt, telt als overname van
	// het VIP en niet als uitval.
	ShortOutage time.Duration
	// Fresh is hoe oud een heartbeat in de voorcontrole mag zijn.
	Fresh time.Duration
	// Retry is de pauze voor een nieuwe poging om een agent te bereiken.
	Retry time.Duration
	// ReturnTimeout en BootTimeout zijn hoe lang het herstel hoogstens mag
	// duren, na het starten van een dienst of van een VM. De verwachting kan
	// ze langer maken.
	ReturnTimeout time.Duration
	BootTimeout   time.Duration
	// EmergencyTimeout is hoe lang het noodherstel bij een stop van de
	// server mag duren.
	EmergencyTimeout time.Duration
	// Lease is hoe lang een storing van een reeks blijft zonder verlenging,
	// RenewInterval hoe vaak de server verlengt zolang hij meet.
	Lease         time.Duration
	RenewInterval time.Duration
	// Changed wordt aangeroepen na een run; mag nil zijn.
	Changed func()
}

func NewService(pool *pgxpool.Pool, ev *events.Writer, log *slog.Logger, runner *jobs.Runner, bus failover.Commander) *Service {
	s := &Service{
		pool: pool, q: store.New(pool), ev: ev, log: log, jobs: runner, bus: bus,
		Gate: health.NewGate(pool), Prober: failover.NetProber{Timeout: 400 * time.Millisecond},
		ProbeInterval: 250 * time.Millisecond, StatusPoll: 2 * time.Second, ShortOutage: 10 * time.Second,
		Fresh: status.HeartbeatLate, Retry: 5 * time.Second, ReturnTimeout: 3 * time.Minute, BootTimeout: 15 * time.Minute,
		EmergencyTimeout: 10 * time.Second, Lease: protocol.FaultLease, RenewInterval: 10 * time.Second,
		Probes: func(store.Cluster, string) failover.Probe { return failover.DefaultProbe },
	}
	// Geen van beide is Retryable: een nieuwe poging is een nieuwe taak.
	runner.Register(KindRun, s.runDisaster)
	runner.Register(KindRestore, s.runRestore)
	runner.OnFinished(KindRun, s.runFinished)
	return s
}

func (s *Service) changed() {
	if s.Changed != nil {
		s.Changed()
	}
}

// Input is een nieuw of gewijzigd scenario.
type Input struct {
	Name            string
	Kind            string
	ConnectionID    *uuid.UUID
	Host            string
	Unit            string
	Steps           []Step
	FaultSeconds    int
	RecoverySeconds int
}

func (in *Input) params() Params {
	return Params{ConnectionID: in.ConnectionID, Host: in.Host, Unit: in.Unit, Steps: in.Steps}
}

func fieldErr(field, msg string) error { return &failover.FieldError{Field: field, Message: msg} }

// validate controleert de invoer tegen wat het cluster nu heeft.
func (in *Input) validate(o Options) error {
	in.Name = strings.TrimSpace(in.Name)
	switch n := len([]rune(in.Name)); {
	case n == 0:
		return fieldErr("name", "geef het scenario een naam")
	case n > 200:
		return fieldErr("name", "de naam is te lang (hoogstens 200 tekens)")
	}
	if _, ok := kindOf(in.Kind); !ok {
		return fieldErr("kind", "kies een Proxmox-host die uitvalt, een dienst die wegvalt, een VIP dat wegvalt of een reeks van storingen")
	}
	if !o.Allowed {
		return fieldErr("kind", o.Reason)
	}
	if in.Kind != Sequence {
		in.Steps = nil
	}
	switch in.Kind {
	case HostDown:
		in.Unit = ""
		in.Host = strings.TrimSpace(in.Host)
		if in.ConnectionID == nil || !slices.ContainsFunc(o.Hosts, func(h HostOption) bool {
			return h.ConnectionID == *in.ConnectionID && h.Host == in.Host
		}) {
			return fieldErr("host", "kies een Proxmox-host waarop een node van dit cluster draait")
		}
	case ServiceDown:
		in.ConnectionID, in.Host = nil, ""
		if !slices.Contains(Units, in.Unit) {
			return fieldErr("unit", "kies welke dienst wegvalt: "+strings.Join(Units, ", "))
		}
		if !slices.Contains(o.Units, in.Unit) {
			return fieldErr("unit", in.Unit+" staat op geen enkele node van dit cluster")
		}
	case VIPLoss:
		in.ConnectionID, in.Host, in.Unit = nil, "", ""
	case Sequence:
		in.ConnectionID, in.Host, in.Unit = nil, "", ""
	}
	if in.Kind == HostDown && in.FaultSeconds < MinHostDownSeconds {
		return fieldErr("fault_seconds", fmt.Sprintf("na een harde stop is een node pas na %d s zonder heartbeat down; laat de storing minstens %d seconden duren",
			int(status.HeartbeatDown.Seconds()), MinHostDownSeconds))
	}
	if in.FaultSeconds < MinFaultSeconds || in.FaultSeconds > MaxFaultSeconds {
		return fieldErr("fault_seconds", fmt.Sprintf("de storing duurt tussen %d en %d seconden", MinFaultSeconds, MaxFaultSeconds))
	}
	if in.RecoverySeconds < MinRecoverySeconds || in.RecoverySeconds > MaxRecoverySeconds {
		return fieldErr("recovery_seconds", fmt.Sprintf("de verwachte hersteltijd ligt tussen %d en %d seconden", MinRecoverySeconds, MaxRecoverySeconds))
	}
	if in.Kind == Sequence {
		steps, err := validateSteps(in.Steps, in.FaultSeconds, o)
		if err != nil {
			return err
		}
		in.Steps = steps
	}
	return nil
}

func userOf(actor events.Actor) *uuid.UUID {
	if actor.Type != store.ActorTypeUser {
		return nil
	}
	if id, err := uuid.Parse(actor.ID); err == nil {
		return &id
	}
	return nil
}

// HostOption is een Proxmox-host met een node van dit cluster.
type HostOption struct {
	ConnectionID uuid.UUID
	Connection   string
	Host         string
	// Nodes zijn de nodes van dit cluster op de host, Others die van
	// andere clusters of zonder cluster.
	Nodes  []string
	Others []string
}

// KindOption zegt of een soort hier kan, en anders waarom niet.
type KindOption struct {
	Kind
	Available bool
	Reason    string
}

// Options is wat het formulier nodig heeft.
type Options struct {
	// Allowed is false op prod, met de reden.
	Allowed bool
	Reason  string
	Slug    string
	Kinds   []KindOption
	Hosts   []HostOption
	// Units zijn de diensten die service_down hier kan stoppen.
	Units []string
	VIPs  []string
	// Nodes en Actions zijn er voor de stappen van een reeks.
	Nodes   []NodeOption
	Actions []ActionOption
}

// Options zegt welke rampoefeningen dit cluster nu kan.
func (s *Service) Options(ctx context.Context, clusterID uuid.UUID) (Options, error) {
	c, err := s.q.GetCluster(ctx, clusterID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Options{}, ErrNotFound
	}
	if err != nil {
		return Options{}, err
	}
	nodes, err := s.q.ListFailoverNodes(ctx, &clusterID)
	if err != nil {
		return Options{}, err
	}
	vips, err := s.q.ListVIPsByCluster(ctx, clusterID)
	if err != nil {
		return Options{}, err
	}
	out := Options{Allowed: c.Environment != store.EnvironmentProd, Slug: c.Slug, Hosts: []HostOption{}, Units: []string{}, VIPs: []string{}}
	if !out.Allowed {
		out.Reason = msgProd
	}
	for _, v := range vips {
		out.VIPs = append(out.VIPs, v.Vip.Address.String())
	}
	keepalived := false
	for _, n := range nodes {
		st := health.ServiceStates(n.Services)
		if _, ok := st["keepalived"]; ok {
			keepalived = true
		}
		for _, u := range Units {
			if _, ok := st[u]; ok && !slices.Contains(out.Units, u) {
				out.Units = append(out.Units, u)
			}
		}
	}
	slices.SortFunc(out.Units, func(a, b string) int { return cmp.Compare(slices.Index(Units, a), slices.Index(Units, b)) })
	var m topo.Model
	if s.World != nil {
		if w, err := s.World(ctx); err == nil {
			out.Hosts = hostOptions(w.Topo, nodes)
			m = w.Topo
		} else {
			s.log.Warn("hosts voor rampoefeningen lezen", "err", err)
		}
	}
	fns, err := s.faultNodes(ctx, clusterID, out.VIPs, m)
	if err != nil {
		return Options{}, err
	}
	out.Nodes = make([]NodeOption, 0, len(fns))
	for _, n := range fns {
		out.Nodes = append(out.Nodes, n.NodeOption)
	}
	out.Actions = actionOptions(fns)
	for _, k := range Kinds {
		o := KindOption{Kind: k, Available: true}
		switch {
		case !out.Allowed:
			o.Available, o.Reason = false, msgProd
		case k.Key == HostDown && s.Proxmox == nil:
			o.Available, o.Reason = false, "geen Proxmox-koppeling; zet CF_MASTER_KEY en koppel Proxmox"
		case k.Key == HostDown && len(out.Hosts) == 0:
			o.Available, o.Reason = false, "geen node van dit cluster is aan een draaiende VM in Proxmox gekoppeld"
		case k.Key == ServiceDown && len(out.Units) == 0:
			o.Available, o.Reason = false, "op geen enkele node staat "+strings.Join(Units, ", ")
		case k.Key == VIPLoss && len(out.VIPs) == 0:
			o.Available, o.Reason = false, "dit cluster heeft geen VIP"
		case k.Key == VIPLoss && !keepalived:
			o.Available, o.Reason = false, "op geen enkele node staat keepalived"
		case k.Key == Sequence && len(out.Nodes) == 0:
			o.Available, o.Reason = false, "geen actieve node van dit cluster heeft een agent"
		case k.Key == Sequence && !slices.ContainsFunc(out.Actions, func(a ActionOption) bool { return a.Available }):
			o.Available, o.Reason = false, "geen enkele storing kan hier; zie de redenen per stap"
		}
		out.Kinds = append(out.Kinds, o)
	}
	return out, nil
}

// hostOptions zijn de hosts waarop een gekoppelde VM van dit cluster draait.
func hostOptions(m topo.Model, nodes []store.ListFailoverNodesRow) []HostOption {
	mine := map[uuid.UUID]bool{}
	for _, n := range nodes {
		mine[n.ID] = true
	}
	type key struct {
		conn uuid.UUID
		host string
	}
	byHost := map[key]*HostOption{}
	var order []key
	for _, g := range m.Guests {
		if g.NodeID == nil || !mine[*g.NodeID] || g.Template || g.Pool == proxmox.SandboxPool || g.Host == "" {
			continue
		}
		k := key{g.Conn, g.Host}
		if byHost[k] == nil {
			byHost[k] = &HostOption{ConnectionID: g.Conn, Connection: m.ConnName(g.Conn), Host: g.Host, Nodes: []string{}, Others: []string{}}
			order = append(order, k)
		}
		byHost[k].Nodes = append(byHost[k].Nodes, g.Label())
	}
	for _, g := range m.Guests {
		k := key{g.Conn, g.Host}
		if byHost[k] == nil || g.NodeID == nil || mine[*g.NodeID] || g.Template || g.Pool == proxmox.SandboxPool {
			continue
		}
		byHost[k].Others = append(byHost[k].Others, g.Label())
	}
	out := make([]HostOption, 0, len(order))
	for _, k := range order {
		h := byHost[k]
		slices.Sort(h.Nodes)
		slices.Sort(h.Others)
		out = append(out, *h)
	}
	slices.SortFunc(out, func(a, b HostOption) int {
		return cmp.Or(cmp.Compare(strings.ToLower(a.Connection), strings.ToLower(b.Connection)), cmp.Compare(a.Host, b.Host))
	})
	return out
}

// Scenario is een scenario met zijn gelezen params en verwachting.
type Scenario struct {
	store.DisasterScenario
	P Params
	E Expect
}

// Decode leest de params en de verwachting van een rij.
func Decode(row store.DisasterScenario) Scenario {
	sc := Scenario{DisasterScenario: row}
	_ = json.Unmarshal(row.Params, &sc.P)
	_ = json.Unmarshal(row.Expect, &sc.E)
	return sc
}

func (s *Service) payload(c store.Cluster, in Input) map[string]any {
	return map[string]any{
		"name": in.Name, "cluster": c.Name, "kind": in.Kind, "description": Describe(in.Kind, in.params()),
		"fault_seconds": in.FaultSeconds, "recovery_seconds": in.RecoverySeconds,
	}
}

// Create maakt een scenario.
func (s *Service) Create(ctx context.Context, actor events.Actor, clusterID uuid.UUID, in Input) (store.DisasterScenario, error) {
	o, err := s.Options(ctx, clusterID)
	if err != nil {
		return store.DisasterScenario{}, err
	}
	if err := in.validate(o); err != nil {
		return store.DisasterScenario{}, err
	}
	c, err := s.q.GetCluster(ctx, clusterID)
	if err != nil {
		return store.DisasterScenario{}, err
	}
	params, _ := json.Marshal(in.params())
	expect, _ := json.Marshal(Expect{FaultSeconds: in.FaultSeconds, RecoverySeconds: in.RecoverySeconds})
	var out store.DisasterScenario
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		var err error
		out, err = q.InsertDisasterScenario(ctx, store.InsertDisasterScenarioParams{
			ClusterID: clusterID, Name: in.Name, Kind: in.Kind, Params: params, Expect: expect, CreatedBy: userOf(actor),
		})
		if err != nil {
			return err
		}
		return s.ev.Write(ctx, q, events.Event{
			Actor: actor, SubjectType: "disaster_scenario", SubjectID: out.ID.String(), ClusterID: &clusterID,
			Action: "disaster_scenario.created", Payload: s.payload(c, in),
		})
	})
	return out, err
}

// Update wijzigt een scenario. Oude runs houden hun definitie van bij de
// start.
func (s *Service) Update(ctx context.Context, actor events.Actor, id uuid.UUID, in Input) (store.DisasterScenario, error) {
	cur, err := s.q.GetDisasterScenario(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.DisasterScenario{}, ErrNotFound
	}
	if err != nil {
		return store.DisasterScenario{}, err
	}
	o, err := s.Options(ctx, cur.ClusterID)
	if err != nil {
		return store.DisasterScenario{}, err
	}
	if err := in.validate(o); err != nil {
		return store.DisasterScenario{}, err
	}
	params, _ := json.Marshal(in.params())
	expect, _ := json.Marshal(Expect{FaultSeconds: in.FaultSeconds, RecoverySeconds: in.RecoverySeconds})
	var out store.DisasterScenario
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		var err error
		out, err = q.UpdateDisasterScenario(ctx, store.UpdateDisasterScenarioParams{ID: id, Name: in.Name, Kind: in.Kind, Params: params, Expect: expect})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		old := Decode(cur)
		payload := map[string]any{}
		diff := func(field string, from, to any) {
			if from != to {
				payload[field] = map[string]any{"from": from, "to": to}
			}
		}
		diff("name", old.Name, in.Name)
		diff("description", Describe(old.Kind, old.P), Describe(in.Kind, in.params()))
		diff("fault_seconds", old.E.FaultSeconds, in.FaultSeconds)
		diff("recovery_seconds", old.E.RecoverySeconds, in.RecoverySeconds)
		if len(payload) == 0 {
			return nil
		}
		payload["scenario"] = in.Name
		return s.ev.Write(ctx, q, events.Event{
			Actor: actor, SubjectType: "disaster_scenario", SubjectID: id.String(), ClusterID: &cur.ClusterID,
			Action: "disaster_scenario.updated", Payload: payload,
		})
	})
	return out, err
}

// Delete verwijdert een scenario; zijn runs blijven als geschiedenis.
func (s *Service) Delete(ctx context.Context, actor events.Actor, id uuid.UUID) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		cur, err := q.GetDisasterScenario(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if _, err := q.DeleteDisasterScenario(ctx, id); err != nil {
			return err
		}
		sc := Decode(cur)
		return s.ev.Write(ctx, q, events.Event{
			Actor: actor, SubjectType: "disaster_scenario", SubjectID: id.String(), ClusterID: &cur.ClusterID,
			Action: "disaster_scenario.deleted", Payload: map[string]any{"name": cur.Name, "description": Describe(cur.Kind, sc.P)},
		})
	})
}
