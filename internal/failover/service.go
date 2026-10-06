// Package failover stopt met opzet keepalived of een dienst op de node die
// een VIP heeft, meet vanaf de server hoe lang het VIP onbereikbaar is en
// welke node het overneemt, en zet daarna altijd alles terug. De storing is
// één service-stap via apply.steps: stoppen, en bij het herstel starten.
package failover

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/health"
	"github.com/Jonasz1996/clusterforge/internal/jobs"
	"github.com/Jonasz1996/clusterforge/internal/planner"
	"github.com/Jonasz1996/clusterforge/internal/proxmox"
	"github.com/Jonasz1996/clusterforge/internal/status"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/internal/templates"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// Scenario's.
const (
	KeepalivedStop = "keepalived_stop"
	ServiceStop    = "service_stop"
	// VMHardStop zet de VM van de eigenaar hard uit via Proxmox. Alleen in
	// lab en test: een hard gestopte VM heeft geen agent, en alleen de
	// server zet hem weer aan.
	VMHardStop = "vm_hard_stop"
)

// Hoe een run gestart is, zoals in test_runs.trigger.
const (
	TriggerManual   = "manual"
	TriggerSchedule = "schedule"
)

// Soorten taak.
const (
	KindTest    = "failover.test"
	KindRestore = "failover.restore"
)

// Units zijn de diensten die service_stop mag stoppen. Naar de agent gaat
// nooit een andere unit dan deze of keepalived.
var Units = []string{"nginx", "haproxy"}

// databases zijn de diensten waaraan een databasenode te herkennen is. Een
// VIP dat naar een replica verhuist, kan schrijfacties laten mislukken.
var databases = []string{"postgresql", "mariadb", "mysql"}

// Scenario is een soort storing met zijn standaardverwachting.
type Scenario struct {
	Key            string
	DefaultSeconds int
}

// Scenarios staan in de volgorde van het formulier.
var Scenarios = []Scenario{{KeepalivedStop, 5}, {ServiceStop, 10}, {VMHardStop, 10}}

// Describe zegt in gewone taal wat een test doet.
func Describe(scenario, service string) string {
	switch scenario {
	case KeepalivedStop:
		return "keepalived stoppen op de eigenaar"
	case ServiceStop:
		if service == "" {
			service = "de dienst"
		}
		return service + " stoppen op de eigenaar"
	case VMHardStop:
		return "de VM van de eigenaar hard uitzetten"
	}
	return scenario
}

// unitOf is de systemd-unit die de storing stopt, of bij vm_hard_stop de
// unit die na de terugkeer weer moet draaien.
func unitOf(scenario, service string) string {
	if scenario == ServiceStop {
		return service
	}
	return "keepalived"
}

// FieldError is een fout in één veld van de invoer.
type FieldError struct{ Field, Message string }

func (e *FieldError) Error() string { return e.Message }

var ErrNotFound = errors.New("niet gevonden")

// Waarom iets op prod niet kan. Op prod start een test alleen met de hand,
// met de bevestiging bij prod, en nooit met vm_hard_stop.
const (
	msgProdSchedule = "op prod kan een failovertest niet gepland worden; daar start je hem alleen met de hand"
	msgProdHardStop = "VM hard uitzetten kan alleen in lab en test: een hard gestopte VM heeft geen agent, en alleen ClusterForge zet hem weer aan"
)

// ConflictError betekent dat het nu niet kan, met een stabiele code.
type ConflictError struct{ Code, Msg string }

func (e *ConflictError) Error() string { return e.Msg }

// Check is één voorwaarde van de voorcontrole.
type Check struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

// PrecheckError is een voorcontrole die niet slaagde. Er is geen force: een
// test die niet veilig kan, start niet.
type PrecheckError struct{ Checks []Check }

func (e *PrecheckError) Error() string {
	var msgs []string
	for _, c := range e.Checks {
		if !c.OK {
			msgs = append(msgs, c.Detail)
		}
	}
	return "de test start niet: " + strings.Join(msgs, "; ")
}

// Commander stuurt een commando naar de agent van een node.
type Commander interface {
	Command(ctx context.Context, nodeID uuid.UUID, cmd protocol.Command) (protocol.Result, error)
}

// Proxmox zet voor vm_hard_stop de VM van een node uit en weer aan.
type Proxmox interface {
	// GuestStatus leest de VM live: of hij draait en of Proxmox HA hem
	// beheert.
	GuestStatus(ctx context.Context, connID uuid.UUID, vmid int) (proxmox.Resource, error)
	// PowerVM vraagt start of stop en geeft het id van de Proxmox-taak.
	PowerVM(ctx context.Context, connID uuid.UUID, vmid int, action string) (string, error)
	// WaitVMTask wacht tot een Proxmox-taak klaar is.
	WaitVMTask(ctx context.Context, connID uuid.UUID, upid string, st *jobs.Step) error
}

type Service struct {
	pool *pgxpool.Pool
	q    *store.Queries
	ev   *events.Writer
	log  *slog.Logger
	jobs *jobs.Runner
	bus  Commander

	// Proxmox zet VM's uit en aan voor vm_hard_stop; nil zonder masterkey.
	Proxmox Proxmox
	// Window is het testvenster waarin geplande tests draaien.
	Window planner.Window
	// BootTimeout is hoe lang de terugkeer na vm_hard_stop mag duren.
	BootTimeout time.Duration
	// Gate wacht op verse heartbeats.
	Gate *health.Gate
	// Templates geven de standaardprobe van een cluster uit een template.
	Templates *templates.Registry
	// Prober vraagt het VIP op; tests zetten er een nagespeelde.
	Prober Prober
	// ProbeInterval is hoe vaak de meting het VIP opvraagt.
	ProbeInterval time.Duration
	// ReturnTimeout is hoe lang de terugkeer na het herstel mag duren.
	ReturnTimeout time.Duration
	// Fresh is hoe oud een heartbeat in de voorcontrole mag zijn.
	Fresh time.Duration
	// MaxWindow is de langste meting, wat de verwachting ook is.
	MaxWindow time.Duration
	// Retry is de pauze voor een nieuwe poging om de agent te bereiken.
	Retry time.Duration
	// EmergencyTimeout is hoe lang het noodherstel bij een stop van de
	// server mag duren.
	EmergencyTimeout time.Duration
	// Changed wordt aangeroepen na een run, zodat de status meteen opnieuw
	// berekend wordt. Mag nil zijn.
	Changed func()
}

func NewService(pool *pgxpool.Pool, ev *events.Writer, log *slog.Logger, runner *jobs.Runner, bus Commander) *Service {
	s := &Service{
		pool: pool, q: store.New(pool), ev: ev, log: log, jobs: runner, bus: bus,
		Gate: health.NewGate(pool), Templates: templates.BuiltinRegistry(),
		Prober: NetProber{Timeout: 400 * time.Millisecond}, ProbeInterval: 250 * time.Millisecond,
		ReturnTimeout: 3 * time.Minute, Fresh: status.HeartbeatLate, MaxWindow: 120 * time.Second,
		Retry: 5 * time.Second, EmergencyTimeout: 10 * time.Second, Window: planner.DefaultWindow, BootTimeout: 15 * time.Minute,
	}
	// Geen van beide is Retryable: een retry uren later voert oude
	// beslissingen uit. Een mislukte test start je opnieuw.
	runner.Register(KindTest, s.runTest)
	runner.Register(KindRestore, s.runRestore)
	runner.OnFinished(KindTest, s.testFinished)
	return s
}

func (s *Service) changed() {
	if s.Changed != nil {
		s.Changed()
	}
}

// Input is een nieuwe of gewijzigde test.
type Input struct {
	Name               string
	VIPID              uuid.UUID
	Scenario           string
	Service            string
	MaxTakeoverSeconds int
	ExpectFailback     bool
	Probe              Probe
	// Scheduled zet de test gepland in het testvenster; niet op prod.
	Scheduled bool
}

func (in *Input) validate(env store.Environment) error {
	in.Name = strings.TrimSpace(in.Name)
	switch n := len([]rune(in.Name)); {
	case n == 0:
		return &FieldError{Field: "name", Message: "geef de test een naam"}
	case n > 200:
		return &FieldError{Field: "name", Message: "de naam is te lang (hoogstens 200 tekens)"}
	}
	switch in.Scenario {
	case KeepalivedStop:
		in.Service = ""
	case ServiceStop:
		if !slices.Contains(Units, in.Service) {
			return &FieldError{Field: "service", Message: "kies welke dienst stopt: " + strings.Join(Units, " of ")}
		}
	case VMHardStop:
		in.Service = ""
		if env == store.EnvironmentProd {
			return &FieldError{Field: "scenario", Message: msgProdHardStop}
		}
	default:
		return &FieldError{Field: "scenario", Message: "kies keepalived stoppen, een dienst stoppen of de VM hard uitzetten"}
	}
	if in.Scheduled && env == store.EnvironmentProd {
		return &FieldError{Field: "scheduled", Message: msgProdSchedule}
	}
	if in.MaxTakeoverSeconds < 1 || in.MaxTakeoverSeconds > 120 {
		return &FieldError{Field: "max_takeover_seconds", Message: "de verwachting ligt tussen 1 en 120 seconden"}
	}
	return in.Probe.Validate()
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

// vipOf geeft het adres van een VIP van dit cluster.
func (s *Service) vipOf(ctx context.Context, q *store.Queries, clusterID, vipID uuid.UUID) (string, error) {
	v, err := q.GetVIP(ctx, vipID)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && v.ClusterID != clusterID {
		return "", &FieldError{Field: "vip_id", Message: "kies een VIP van dit cluster"}
	}
	if err != nil {
		return "", err
	}
	return v.Address.String(), nil
}

// Create maakt een test.
func (s *Service) Create(ctx context.Context, actor events.Actor, clusterID uuid.UUID, in Input) (store.FailoverTest, error) {
	c, err := s.q.GetCluster(ctx, clusterID)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.FailoverTest{}, ErrNotFound
	}
	if err != nil {
		return store.FailoverTest{}, err
	}
	if err := in.validate(c.Environment); err != nil {
		return store.FailoverTest{}, err
	}
	var next *time.Time
	if in.Scheduled {
		t := s.Window.First(time.Now())
		next = &t
	}
	probe, err := json.Marshal(in.Probe)
	if err != nil {
		return store.FailoverTest{}, err
	}
	var out store.FailoverTest
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		vip, err := s.vipOf(ctx, q, clusterID, in.VIPID)
		if err != nil {
			return err
		}
		out, err = q.InsertFailoverTest(ctx, store.InsertFailoverTestParams{
			ClusterID: clusterID, VipID: in.VIPID, Name: in.Name, Scenario: in.Scenario, Service: in.Service,
			MaxTakeoverSeconds: int32(in.MaxTakeoverSeconds), ExpectFailback: in.ExpectFailback, //nolint:gosec // gecontroleerd: 1 tot 120
			Probe: probe, CreatedBy: userOf(actor), Scheduled: in.Scheduled, NextRunAt: next,
		})
		if err != nil {
			return err
		}
		return s.ev.Write(ctx, q, events.Event{
			Actor: actor, SubjectType: "failover_test", SubjectID: out.ID.String(), ClusterID: &clusterID,
			Action: "failover_test.created", Payload: map[string]any{
				"name": in.Name, "cluster": c.Name, "scenario": Describe(in.Scenario, in.Service), "vip": vip,
				"max_takeover_seconds": in.MaxTakeoverSeconds, "expect_failback": in.ExpectFailback, "probe": in.Probe.String(),
				"scheduled": in.Scheduled,
			},
		})
	})
	return out, err
}

// Update wijzigt een test. Oude runs houden hun definitie van bij de start.
func (s *Service) Update(ctx context.Context, actor events.Actor, id uuid.UUID, in Input) (store.FailoverTest, error) {
	probe, err := json.Marshal(in.Probe)
	if err != nil {
		return store.FailoverTest{}, err
	}
	var out store.FailoverTest
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		cur, err := q.GetFailoverTest(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		old := cur.FailoverTest
		c, err := q.GetCluster(ctx, old.ClusterID)
		if err != nil {
			return err
		}
		if err := in.validate(c.Environment); err != nil {
			return err
		}
		vip, err := s.vipOf(ctx, q, old.ClusterID, in.VIPID)
		if err != nil {
			return err
		}
		// Blijft de planning aan, dan blijft ook het volgende moment staan.
		next := old.NextRunAt
		switch {
		case !in.Scheduled:
			next = nil
		case !old.Scheduled || next == nil:
			t := s.Window.First(time.Now())
			next = &t
		}
		out, err = q.UpdateFailoverTest(ctx, store.UpdateFailoverTestParams{
			ID: id, VipID: in.VIPID, Name: in.Name, Scenario: in.Scenario, Service: in.Service,
			MaxTakeoverSeconds: int32(in.MaxTakeoverSeconds), ExpectFailback: in.ExpectFailback, //nolint:gosec // gecontroleerd: 1 tot 120
			Probe: probe, Scheduled: in.Scheduled, NextRunAt: next,
		})
		if err != nil {
			return err
		}
		var oldProbe Probe
		_ = json.Unmarshal(old.Probe, &oldProbe)
		payload := map[string]any{}
		diff := func(field string, from, to any) {
			if from != to {
				payload[field] = map[string]any{"from": from, "to": to}
			}
		}
		diff("name", old.Name, in.Name)
		diff("vip", cur.VipAddress.String(), vip)
		diff("scenario", Describe(old.Scenario, old.Service), Describe(in.Scenario, in.Service))
		diff("max_takeover_seconds", int(old.MaxTakeoverSeconds), in.MaxTakeoverSeconds)
		diff("expect_failback", old.ExpectFailback, in.ExpectFailback)
		diff("probe", oldProbe.String(), in.Probe.String())
		diff("scheduled", old.Scheduled, in.Scheduled)
		if len(payload) == 0 {
			return nil
		}
		payload["test"] = in.Name
		return s.ev.Write(ctx, q, events.Event{
			Actor: actor, SubjectType: "failover_test", SubjectID: id.String(), ClusterID: &old.ClusterID,
			Action: "failover_test.updated", Payload: payload,
		})
	})
	return out, err
}

// Delete verwijdert een test; zijn runs blijven als geschiedenis.
func (s *Service) Delete(ctx context.Context, actor events.Actor, id uuid.UUID) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		cur, err := q.GetFailoverTest(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if _, err := q.DeleteFailoverTest(ctx, id); err != nil {
			return err
		}
		t := cur.FailoverTest
		return s.ev.Write(ctx, q, events.Event{
			Actor: actor, SubjectType: "failover_test", SubjectID: id.String(), ClusterID: &t.ClusterID,
			Action: "failover_test.deleted", Payload: map[string]any{"name": t.Name, "scenario": Describe(t.Scenario, t.Service)},
		})
	})
}

// ScenarioOption zegt of een scenario nu kan, en anders waarom niet.
type ScenarioOption struct {
	Key            string
	Label          string
	Available      bool
	Reason         string
	DefaultSeconds int
	// Units zijn de diensten die service_stop hier kan stoppen.
	Units []string
}

// VIPOption is een VIP van het cluster met zijn standaardprobe.
type VIPOption struct {
	ID            uuid.UUID
	Address       string
	OwnerHostname *string
	Probe         Probe
}

// Options is wat het formulier nodig heeft.
type Options struct {
	Scenarios       []ScenarioOption
	VIPs            []VIPOption
	DefaultFailback bool
	// Prod: een test start alleen met de hand, met de clusternaam als
	// bevestiging en tweestapsverificatie, en kan niet gepland worden.
	Prod bool
	Slug string
	// Window is het testvenster in gewone taal, NextWindow het begin van het
	// volgende venster.
	Window     string
	NextWindow time.Time
	// LastTested is de laatste echte test (PASS of FAIL); nil als nooit.
	LastTested *time.Time
}

// Options zegt welke scenario's dit cluster nu kan testen.
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
	tested, err := s.q.LastTestedAt(ctx, store.LastTestedAtParams{ClusterID: &clusterID, Kind: KindTest, Results: []string{"pass", "fail"}})
	if err != nil {
		return Options{}, err
	}
	tpl := s.templateOf(c)
	start, _ := s.Window.Next(time.Now())
	out := Options{
		DefaultFailback: tpl.failback, Prod: c.Environment == store.EnvironmentProd, Slug: c.Slug,
		Window: s.Window.String(), NextWindow: start,
	}
	if tested.Unix() > 0 {
		out.LastTested = &tested
	}
	for _, v := range vips {
		addr := v.Vip.Address.String()
		out.VIPs = append(out.VIPs, VIPOption{ID: v.Vip.ID, Address: addr, OwnerHostname: v.OwnerHostname, Probe: tpl.probe(addr)})
	}
	dbs := databasesOn(nodes)
	keepalived, units, unlinked := 0, []string{}, []string{}
	for _, n := range nodes {
		st := health.ServiceStates(n.Services)
		if _, ok := st["keepalived"]; ok {
			keepalived++
			if n.ProxmoxID == nil || n.PveVmid == nil {
				unlinked = append(unlinked, n.Hostname)
			}
		}
		for _, u := range Units {
			if _, ok := st[u]; ok && !slices.Contains(units, u) {
				units = append(units, u)
			}
		}
	}
	for _, sc := range Scenarios {
		o := ScenarioOption{Key: sc.Key, Label: Describe(sc.Key, ""), DefaultSeconds: sc.DefaultSeconds, Available: true, Units: []string{}}
		switch {
		case len(dbs) > 0:
			o.Available, o.Reason = false, "dit cluster draait een database ("+strings.Join(dbs, ", ")+"); een VIP dat naar een replica verhuist, kan schrijfacties laten mislukken"
		case len(vips) == 0:
			o.Available, o.Reason = false, "dit cluster heeft geen VIP"
		case keepalived < 2:
			o.Available, o.Reason = false, "er zijn geen twee nodes met keepalived die een VIP kunnen overnemen"
		case sc.Key == ServiceStop && len(units) == 0:
			o.Available, o.Reason = false, "op geen enkele node draait "+strings.Join(Units, " of ")
		case sc.Key == VMHardStop && out.Prod:
			o.Available, o.Reason = false, "niet op prod; alleen in lab en test"
		case sc.Key == VMHardStop && s.Proxmox == nil:
			o.Available, o.Reason = false, "geen Proxmox-koppeling; zet CF_MASTER_KEY en koppel Proxmox"
		case sc.Key == VMHardStop && len(unlinked) > 0:
			o.Available, o.Reason = false, "geen Proxmox-koppeling voor "+strings.Join(unlinked, ", ")+"; koppel elke node met keepalived aan zijn VM"
		}
		if sc.Key == ServiceStop {
			o.Label = "nginx of haproxy stoppen op de eigenaar"
			o.Units = units
		}
		out.Scenarios = append(out.Scenarios, o)
	}
	return out, nil
}

// databasesOn noemt de nodes waarop een database actief is.
func databasesOn(nodes []store.ListFailoverNodesRow) []string {
	var out []string
	for _, n := range nodes {
		st := health.ServiceStates(n.Services)
		for _, db := range databases {
			if st[db] == "active" {
				out = append(out, db+" op "+n.Hostname)
			}
		}
	}
	return out
}

// clusterTemplate is wat een template over failovertests zegt.
type clusterTemplate struct {
	failback bool
	checks   []templates.Check
}

// probe is de http-controle van de template voor dit VIP, of anders de
// standaardprobe.
func (t clusterTemplate) probe(vip string) Probe {
	for _, ch := range t.checks {
		if ch.HTTP == nil {
			continue
		}
		u, err := url.Parse(ch.HTTP.URL)
		if err != nil || u.Hostname() != vip || u.Port() != "" || u.RawQuery != "" {
			continue
		}
		p := Probe{HTTP: &HTTPProbe{Path: u.Path, Expect: ch.HTTP.Expect}}
		if p.HTTP.Path == "" {
			p.HTTP.Path = "/"
		}
		if p.Validate() == nil {
			return p
		}
	}
	return DefaultProbe
}

// templateOf leest de template van een cluster. Templates met failback,
// zoals keepalived-nginx, gebruiken preempt met prioriteit 150 en 140, dus
// daar komt het VIP terug.
func (s *Service) templateOf(c store.Cluster) clusterTemplate {
	var spec struct {
		Template struct{ Name, Version string } `json:"template"`
		Cluster  templates.ClusterInfo          `json:"cluster"`
		Params   map[string]any                 `json:"params"`
	}
	if c.TemplateName == nil || json.Unmarshal(c.Spec, &spec) != nil {
		return clusterTemplate{}
	}
	out := clusterTemplate{}
	tpl, ok := s.Templates.Get(spec.Template.Name, spec.Template.Version)
	if !ok {
		return out
	}
	out.failback = tpl.Failback
	if spec.Cluster == (templates.ClusterInfo{}) {
		spec.Cluster = templates.ClusterInfo{Name: c.Name, Slug: c.Slug, Environment: string(c.Environment)}
	}
	out.checks, _ = tpl.RenderChecks(templates.Context{Params: spec.Params, Cluster: spec.Cluster})
	return out
}

// Definition is de invoer van een run bij de start. Een latere wijziging van
// de test verandert hem niet.
type Definition struct {
	TestID             uuid.UUID `json:"test_id"`
	Name               string    `json:"name"`
	Scenario           string    `json:"scenario"`
	Service            string    `json:"service,omitempty"`
	Unit               string    `json:"unit"`
	VIPID              uuid.UUID `json:"vip_id"`
	VIP                string    `json:"vip"`
	MaxTakeoverSeconds int       `json:"max_takeover_seconds"`
	ExpectFailback     bool      `json:"expect_failback"`
	Probe              Probe     `json:"probe"`
}

func (d Definition) expect() time.Duration { return time.Duration(d.MaxTakeoverSeconds) * time.Second }

func definitionOf(row store.GetFailoverTestRow) (Definition, error) {
	t := row.FailoverTest
	d := Definition{
		TestID: t.ID, Name: t.Name, Scenario: t.Scenario, Service: t.Service, Unit: unitOf(t.Scenario, t.Service),
		VIPID: t.VipID, VIP: row.VipAddress.String(), MaxTakeoverSeconds: int(t.MaxTakeoverSeconds), ExpectFailback: t.ExpectFailback,
	}
	if err := json.Unmarshal(t.Probe, &d.Probe); err != nil {
		return d, fmt.Errorf("probe van de test: %w", err)
	}
	// De unit komt alleen uit de vaste lijst, ook als de rij anders zegt.
	if d.Unit != "keepalived" && !slices.Contains(Units, d.Unit) {
		return d, fmt.Errorf("onbekende dienst %q", d.Unit)
	}
	return d, d.Probe.Validate()
}

// Plan is wat de voorcontrole vond; de taak bewaart het in de state van de
// eerste stap, zodat een hervatte taak hetzelfde doel houdt.
type Plan struct {
	// Skipped is de reden als de taak de test overslaat.
	Skipped string  `json:"skipped,omitempty"`
	Checks  []Check `json:"checks"`
	// Target is de node die het VIP had en de storing krijgt.
	Target health.Holder `json:"target"`
	// TargetVIPs zijn alle VIP's op het doel; keepalived stoppen raakt ze
	// allemaal.
	TargetVIPs []string `json:"target_vips"`
	// VIPs zijn alle VIP's van het cluster.
	VIPs []string `json:"vips"`
	// Peers kunnen het VIP overnemen.
	Peers []string `json:"peers"`
	// VM is bij vm_hard_stop de VM van het doel in Proxmox.
	VM *VMRef `json:"vm,omitempty"`
}

// VMRef is een VM in Proxmox.
type VMRef struct {
	ConnectionID uuid.UUID `json:"connection_id"`
	VMID         int       `json:"vmid"`
}

var statusLabels = map[string]string{
	string(status.Degraded): "verminderd", string(status.Down): "down", string(status.SplitBrain): "split-brain",
	string(status.Unknown): "onbekend",
}

// precheck controleert of de test nu veilig kan. self is de taak die de
// test uitvoert; zijn eigen sloten tellen niet als bezet. trigger zegt of de
// test met de hand of gepland start.
func (s *Service) precheck(ctx context.Context, c store.Cluster, def Definition, self uuid.UUID, trigger string) (Plan, error) {
	plan := Plan{Checks: []Check{}, TargetVIPs: []string{}, VIPs: []string{}, Peers: []string{}}
	add := func(name string, ok bool, detail string) {
		plan.Checks = append(plan.Checks, Check{Name: name, OK: ok, Detail: detail})
	}

	switch {
	case c.Environment != store.EnvironmentProd:
		add("Omgeving", true, "het cluster staat in "+string(c.Environment))
	case trigger == TriggerSchedule:
		add("Omgeving", false, msgProdSchedule)
	case def.Scenario == VMHardStop:
		add("Omgeving", false, msgProdHardStop)
	default:
		add("Omgeving", true, "het cluster staat in prod; de test start alleen met de hand en met bevestiging")
	}
	if c.Status == string(status.Healthy) {
		add("Cluster gezond", true, "het cluster is gezond")
	} else {
		detail := "het cluster is niet gezond maar " + or(statusLabels[c.Status], c.Status)
		if c.StatusReason != "" {
			detail += ": " + c.StatusReason
		}
		add("Cluster gezond", false, detail)
	}

	nodes, err := s.q.ListFailoverNodes(ctx, &c.ID)
	if err != nil {
		return plan, err
	}
	if dbs := databasesOn(nodes); len(dbs) > 0 {
		add("Geen database", false, "er draait een database ("+strings.Join(dbs, ", ")+"); een databasecluster krijgt geen failovertest")
	} else {
		add("Geen database", true, "geen node meldt postgresql, mariadb of mysql als actief")
	}

	vips, err := s.q.ListVIPsByCluster(ctx, c.ID)
	if err != nil {
		return plan, err
	}
	for _, v := range vips {
		plan.VIPs = append(plan.VIPs, v.Vip.Address.String())
	}
	if !slices.Contains(plan.VIPs, def.VIP) {
		plan.VIPs = append(plan.VIPs, def.VIP)
	}
	now := time.Now()
	snap, err := s.Gate.Look(ctx, c.ID, plan.VIPs, now.Add(-s.Fresh))
	if err != nil {
		return plan, err
	}
	if len(snap.Waiting) > 0 {
		add("Verse heartbeats", false, fmt.Sprintf("geen heartbeat van de laatste %s van %s", seconds(s.Fresh), strings.Join(snap.Waiting, ", ")))
	} else {
		add("Verse heartbeats", true, "alle actieve nodes stuurden een verse heartbeat")
	}

	owner, owned := snap.Owner(def.VIP)
	switch h := snap.Holders[def.VIP]; {
	case owned:
		add("Eén eigenaar", true, def.VIP+" staat op "+owner.Hostname)
		plan.Target = owner
		for _, v := range plan.VIPs {
			if o, ok := snap.Owner(v); ok && o.ID == owner.ID {
				plan.TargetVIPs = append(plan.TargetVIPs, v)
			}
		}
	case len(h) == 0:
		add("Eén eigenaar", false, "geen node meldt "+def.VIP+" in een verse heartbeat")
	default:
		add("Eén eigenaar", false, def.VIP+" staat op "+holderNames(h))
	}

	if owned {
		i := slices.IndexFunc(nodes, func(n store.ListFailoverNodesRow) bool { return n.ID == owner.ID })
		switch {
		case i < 0:
		case def.Scenario == VMHardStop:
			// De storing en het herstel gaan via Proxmox, niet via de agent.
		case nodes[i].AgentProtocol < protocol.ApplySince:
			add("Agent", false, "de agent op "+owner.Hostname+" is te oud voor deploystappen; werk hem bij met het installatiescript")
		default:
			add("Agent", true, "de agent op "+owner.Hostname+" kan de storing veroorzaken en herstellen")
		}
		if i >= 0 {
			st := health.ServiceStates(nodes[i].Services)
			if st[def.Unit] == "active" {
				add("Dienst draait", true, def.Unit+" draait op "+owner.Hostname)
			} else {
				add("Dienst draait", false, def.Unit+" draait niet op "+owner.Hostname+" ("+or(st[def.Unit], "onbekend")+")")
			}
			if def.Scenario == VMHardStop {
				plan.VM = s.vmCheck(ctx, nodes[i], add)
				if st["docker"] == "active" {
					add("Geen docker", false, "op "+owner.Hostname+" draait docker; containers met volumes kunnen data bevatten")
				} else {
					add("Geen docker", true, "op "+owner.Hostname+" draait geen docker")
				}
			}
		}
		for _, n := range nodes {
			if n.ID == owner.ID || n.Lifecycle != store.NodeLifecycleActive || !n.HasAgent ||
				n.HeartbeatAt == nil || now.Sub(*n.HeartbeatAt) > s.Fresh {
				continue
			}
			var kv []string
			_ = json.Unmarshal(n.KeepalivedVips, &kv)
			if health.ServiceStates(n.Services)["keepalived"] == "active" && slices.Contains(kv, def.VIP) {
				plan.Peers = append(plan.Peers, n.Hostname)
			}
		}
		if len(plan.Peers) > 0 {
			add("Reservenode", true, strings.Join(plan.Peers, ", ")+" kan "+def.VIP+" overnemen")
		} else {
			add("Reservenode", false, "geen andere actieve node met een verse heartbeat, keepalived actief en "+def.VIP+" in zijn configuratie")
		}
	}

	busy, err := s.q.GetClusterSlotJob(ctx, c.ID)
	switch {
	case errors.Is(err, pgx.ErrNoRows) || err == nil && busy.ID == self:
		add("Clusterslot", true, "er loopt geen andere taak in dit cluster")
	case err != nil:
		return plan, err
	default:
		add("Clusterslot", false, "in dit cluster loopt al een taak ("+busy.Title+")")
	}
	slot, err := s.q.GetTestSlotJob(ctx)
	switch {
	case errors.Is(err, pgx.ErrNoRows) || err == nil && slot.ID == self:
		add("Testslot", true, "er loopt geen andere test")
	case err != nil:
		return plan, err
	default:
		add("Testslot", false, "er loopt al een test ("+slot.Title+"); er loopt er hoogstens één tegelijk")
	}

	if err := s.probeTimes(ctx, def, 3); err != nil {
		add("Probe", false, fmt.Sprintf("%s antwoordt niet op %s: %v; de server moet het VIP rechtstreeks kunnen bereiken", def.VIP, def.Probe, err))
	} else {
		add("Probe", true, fmt.Sprintf("%s: %s, drie keer na elkaar", def.VIP, def.Probe))
	}

	for _, ch := range plan.Checks {
		if !ch.OK {
			return plan, &PrecheckError{Checks: plan.Checks}
		}
	}
	return plan, nil
}

// vmCheck zoekt de VM van het doel in Proxmox: die moet draaien en mag
// niet onder Proxmox HA staan, want de HA-manager zou meespelen.
func (s *Service) vmCheck(ctx context.Context, n store.ListFailoverNodesRow, add func(string, bool, string)) *VMRef {
	switch {
	case s.Proxmox == nil:
		add("VM", false, "ClusterForge heeft geen Proxmox-koppeling; zet CF_MASTER_KEY en koppel Proxmox")
		return nil
	case n.ProxmoxID == nil || n.PveVmid == nil:
		add("VM", false, n.Hostname+" is niet aan een VM in Proxmox gekoppeld")
		return nil
	}
	ref := &VMRef{ConnectionID: *n.ProxmoxID, VMID: int(*n.PveVmid)}
	g, err := s.Proxmox.GuestStatus(ctx, ref.ConnectionID, ref.VMID)
	switch {
	case err != nil:
		add("VM", false, fmt.Sprintf("VM %d van %s is niet te lezen in Proxmox: %v", ref.VMID, n.Hostname, err))
	case g.Status != "running":
		add("VM", false, fmt.Sprintf("VM %d van %s draait niet (%s)", ref.VMID, n.Hostname, or(g.Status, "onbekend")))
	case g.HAState != "":
		add("VM", false, fmt.Sprintf("VM %d van %s staat onder Proxmox HA (%s); de HA-manager zou de VM zelf weer starten of verplaatsen", ref.VMID, n.Hostname, g.HAState))
	default:
		add("VM", true, fmt.Sprintf("VM %d van %s draait op %s en staat niet onder Proxmox HA", ref.VMID, n.Hostname, g.Node))
		return ref
	}
	return nil
}

// probeTimes vraagt het VIP n keer na elkaar op; de eerste fout stopt.
func (s *Service) probeTimes(ctx context.Context, def Definition, n int) error {
	for i := range n {
		if i > 0 {
			select {
			case <-ctx.Done():
				return context.Cause(ctx)
			case <-time.After(s.ProbeInterval):
			}
		}
		if err := s.Prober.Probe(ctx, def.VIP, def.Probe); err != nil {
			return err
		}
	}
	return nil
}

func holderNames(hs []health.Holder) string {
	out := make([]string, len(hs))
	for i, h := range hs {
		out[i] = h.Hostname
	}
	return strings.Join(out, " en ")
}

func or(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// runParams gaan mee met de taak; de rest staat in test_runs.
type runParams struct {
	RunID uuid.UUID `json:"run_id"`
}

// Start doet de voorcontrole en zet de test in de wachtrij: de run en de
// taak in één transactie, door het clusterslot en het testslot. Op prod
// controleert de API eerst de bevestiging.
func (s *Service) Start(ctx context.Context, actor events.Actor, testID uuid.UUID) (store.TestRun, error) {
	row, err := s.q.GetFailoverTest(ctx, testID)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.TestRun{}, ErrNotFound
	}
	if err != nil {
		return store.TestRun{}, err
	}
	c, err := s.q.GetCluster(ctx, row.FailoverTest.ClusterID)
	if err != nil {
		return store.TestRun{}, err
	}
	def, err := definitionOf(row)
	if err != nil {
		return store.TestRun{}, err
	}
	plan, err := s.precheck(ctx, c, def, uuid.Nil, TriggerManual)
	if err != nil {
		return store.TestRun{}, err
	}
	return s.enqueue(ctx, actor, c, def, plan, TriggerManual)
}

// enqueue maakt de run en de taak.
func (s *Service) enqueue(ctx context.Context, actor events.Actor, c store.Cluster, def Definition, plan Plan, trigger string) (store.TestRun, error) {
	defJSON, err := json.Marshal(def)
	if err != nil {
		return store.TestRun{}, err
	}
	checks, err := json.Marshal(plan.Checks)
	if err != nil {
		return store.TestRun{}, err
	}
	title := "Failovertest: " + def.Name
	if trigger == TriggerSchedule {
		title += " (gepland)"
	}
	var run store.TestRun
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		var err error
		run, err = q.InsertTestRun(ctx, store.InsertTestRunParams{
			Kind: KindTest, Trigger: trigger, ClusterID: &c.ID, TestID: &def.TestID, NodeID: &plan.Target.ID, Hostname: plan.Target.Hostname,
			Definition: defJSON, Checks: checks, RequestedBy: userOf(actor),
		})
		if err != nil {
			return err
		}
		j, err := s.jobs.EnqueueForClusterTx(ctx, q, jobs.Spec{
			Kind: KindTest, Title: title, Params: runParams{RunID: run.ID},
			ClusterID: &c.ID, NodeID: &plan.Target.ID, Actor: actor,
		})
		if err != nil {
			return err
		}
		run.JobID = &j.ID
		return q.SetTestRunJob(ctx, store.SetTestRunJobParams{ID: run.ID, JobID: &j.ID})
	})
	if err != nil {
		return store.TestRun{}, slotError(err)
	}
	s.jobs.Kick()
	return run, nil
}

// slotError maakt van een bezet slot een ConflictError.
func slotError(err error) error {
	var pgErr *pgconn.PgError
	var busy jobs.BusyError
	switch {
	case errors.As(err, &pgErr) && pgErr.ConstraintName == "jobs_test_slot":
		return &ConflictError{Code: "busy", Msg: "er loopt al een failovertest of back-upcontrole; er loopt er hoogstens één tegelijk"}
	case errors.As(err, &busy):
		return &ConflictError{Code: "busy", Msg: busy.Error()}
	}
	return err
}

// StartRestore zet een taak in de wachtrij die een run die niet volledig
// hersteld is, opnieuw herstelt: de dienst starten en de terugkeer
// controleren.
func (s *Service) StartRestore(ctx context.Context, actor events.Actor, runID uuid.UUID) (store.Job, error) {
	row, err := s.q.GetTestRun(ctx, runID)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.Job{}, ErrNotFound
	}
	if err != nil {
		return store.Job{}, err
	}
	run := row.TestRun
	switch {
	case run.Kind != KindTest:
		return store.Job{}, &ConflictError{Code: "conflict", Msg: "alleen een failovertest kan opnieuw hersteld worden"}
	case run.Restored == nil || *run.Restored:
		return store.Job{}, &ConflictError{Code: "conflict", Msg: "deze run hoeft niet hersteld te worden"}
	case run.NodeID == nil:
		return store.Job{}, &ConflictError{Code: "conflict", Msg: "de node van deze run bestaat niet meer"}
	}
	j, err := s.jobs.EnqueueForCluster(ctx, jobs.Spec{
		Kind: KindRestore, Title: "Failovertest herstellen: " + run.Hostname, Params: runParams{RunID: run.ID},
		ClusterID: run.ClusterID, NodeID: run.NodeID, Actor: actor,
	})
	if err != nil {
		return store.Job{}, slotError(err)
	}
	return j, nil
}

// seconds schrijft een duur als "3,4 s", "20 s" of "3 min".
func seconds(d time.Duration) string {
	d = d.Round(100 * time.Millisecond)
	switch {
	case d%time.Second == 0 && d < 2*time.Minute:
		return fmt.Sprintf("%d s", int(d.Seconds()))
	case d < time.Minute:
		return strings.Replace(fmt.Sprintf("%.1f s", d.Seconds()), ".", ",", 1)
	case d < 2*time.Minute:
		return fmt.Sprintf("%d s", int(d.Round(time.Second).Seconds()))
	}
	return fmt.Sprintf("%d min", int(d.Round(time.Minute).Minutes()))
}
