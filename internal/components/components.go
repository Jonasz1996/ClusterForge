// Package components zet een onderdeel op de nodes van een cluster dat er
// al is, naast wat er draait: in fase 5 de database. Een onderdeel heeft het
// formaat van een template, maar zonder VM's; zijn spec met revisies staat
// in cluster_components, los van de template of de baseline van het
// cluster. Zo krijgen ook de CT's die Jonas zelf bouwde een database.
//
// Toepassen is een taak die de nodes een voor een doet en na elke node
// wacht tot de database daar gezond is: bij Galera Synced, in een Primary
// component met de verwachte grootte.
package components

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Jonasz1996/clusterforge/internal/databases"
	"github.com/Jonasz1996/clusterforge/internal/deploy"
	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/jobs"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/internal/templates"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// Kind is de taak die een onderdeel op zijn nodes zet.
const Kind = "component.apply"

// Rollen van de nodes van een database.
const (
	// RoleData heeft alle data en kan schrijven.
	RoleData = "data"
	// RoleArbiter stemt mee zonder data: garbd bij Galera.
	RoleArbiter = "arbiter"
)

// Templates zijn de onderdelen per engine.
var Templates = map[string]string{protocol.DBMariaDB: "mariadb-galera", protocol.DBPostgreSQL: "postgresql-patroni"}

// De ingebouwde onderdelen staan per versie in builtin/<naam>/<versie>/,
// zoals de templates. Een versie blijft staan zolang een cluster haar kan
// gebruiken.
//
//go:embed builtin
var builtinFS embed.FS

var registry *templates.Registry

func init() {
	sub, _ := fs.Sub(builtinFS, "builtin")
	r, err := templates.Load(sub)
	if err != nil {
		panic("ingebouwde onderdelen: " + err.Error())
	}
	for _, t := range r.All() {
		if t.ClusterType != templates.ComponentType {
			panic(fmt.Sprintf("onderdeel %s: cluster_type moet %s zijn", t.Name, templates.ComponentType))
		}
	}
	registry = r
}

// Registry geeft de ingebouwde onderdelen in alle versies.
func Registry() *templates.Registry { return registry }

var (
	// ErrNotFound: het onderdeel bestaat niet, of hoort bij een ander
	// cluster.
	ErrNotFound = errors.New("dit cluster heeft dat onderdeel niet")
	// ErrStale: het plan veranderde sinds de gebruiker het zag.
	ErrStale = errors.New("het plan is veranderd sinds je het bekeek; bekijk het opnieuw")
)

// ConflictError betekent dat het nu niet kan, met de reden.
type ConflictError struct{ Msg string }

func (e *ConflictError) Error() string { return e.Msg }

// InvalidError is een fout in de invoer, bij een veld.
type InvalidError struct{ Field, Msg string }

func (e *InvalidError) Error() string { return e.Msg }

// Bus stuurt commando's en databaseverzoeken naar de agents.
type Bus interface {
	deploy.Commander
	databases.Requester
}

type Service struct {
	pool *pgxpool.Pool
	q    *store.Queries
	ev   *events.Writer
	log  *slog.Logger
	jobs *jobs.Runner
	bus  Bus
	dep  *deploy.Service

	// Memory geeft per node het geheugengebruik in procent, zeven dagen
	// lang 95% van de tijd niet overschreden. Mag nil zijn; het plan zegt
	// dan dat het niet weet wat de app gebruikt.
	Memory func(ctx context.Context) (map[uuid.UUID]float64, error)
	// Overview geeft de databaseservers die ClusterForge ziet, voor een
	// verhuizing. Mag nil zijn; verhuizen kan dan niet.
	Overview func(ctx context.Context) ([]databases.Group, error)
	// Changed wordt aangeroepen als een onderdeel toegepast is. Mag nil zijn.
	Changed func()
	// DBChanged wordt aangeroepen als er een database of gebruiker bij kwam
	// of weg ging, zodat het menu Databases meteen klopt. Mag nil zijn.
	DBChanged func()

	// Poll is de pauze tussen twee keer kijken of een node Synced is, en
	// GateTimeout hoe lang dat mag duren. Een nieuwe node kopieert dan alle
	// data, dus dat kan bij een grote database lang zijn. VIPTimeout is hoe
	// lang het VIP na het herladen van keepalived weg mag zijn.
	Poll        time.Duration
	GateTimeout time.Duration
	VIPTimeout  time.Duration
	// Timeout is hoe lang een databaseverzoek aan een agent mag duren.
	Timeout time.Duration
	// DumpPoll is hoe vaak ClusterForge vraagt hoe ver een dump of het
	// terugzetten ervan is.
	DumpPoll time.Duration
	// MoveDrain is hoe lang een verhuizing wacht tot niemand meer met de
	// oude database verbonden is.
	MoveDrain time.Duration
	// Retry is de wachttijd tussen pogingen als een agent even weg is.
	Retry time.Duration
	// Fresh is hoe oud de laatste heartbeat van een agent mag zijn.
	Fresh time.Duration
	Now   func() time.Time

	// De bewaking (watch.go): WatchInterval is hoe vaak ClusterForge kijkt,
	// nul zet het uit. RecoverGrace is hoe lang de database of een node weg
	// moet zijn voor ClusterForge hem vanzelf opnieuw start, HungAfter hoe
	// lang MariaDB mag blijven starten, NodeOutAfter na hoeveel tijd een
	// node die niet meedoet in het logboek komt, en AutoBackoff hoe lang
	// ClusterForge na een eigen poging wacht voor hij het weer probeert.
	WatchInterval time.Duration
	RecoverGrace  time.Duration
	HungAfter     time.Duration
	NodeOutAfter  time.Duration
	AutoBackoff   time.Duration

	// moveMu: overschakelen, annuleren en het slot van verhuizingen één
	// voor één.
	moveMu sync.Mutex
}

func NewService(pool *pgxpool.Pool, ev *events.Writer, log *slog.Logger, runner *jobs.Runner, bus Bus, dep *deploy.Service) *Service {
	s := &Service{
		pool: pool, q: store.New(pool), ev: ev, log: log, jobs: runner, bus: bus, dep: dep,
		Poll: 3 * time.Second, GateTimeout: 30 * time.Minute, VIPTimeout: 2 * time.Minute, Timeout: 15 * time.Second, Retry: 10 * time.Second, DumpPoll: 5 * time.Second,
		MoveDrain: 30 * time.Second,
		Fresh:     2 * time.Minute, Now: time.Now,
		WatchInterval: 20 * time.Second, RecoverGrace: 90 * time.Second, HungAfter: 15 * time.Minute, NodeOutAfter: 5 * time.Minute,
		AutoBackoff: 30 * time.Minute,
	}
	runner.Register(Kind, s.run)
	runner.Register(RecoverKind, s.runRecover)
	// Elke stap kan opnieuw: een node die al meedoet, sluit niet opnieuw
	// aan, en het cluster begint nooit twee keer.
	runner.Retryable(Kind)
	runner.Retryable(RecoverKind)
	// Beheer onthoudt per stap of de actie al gestuurd is, en wacht dan
	// verder.
	runner.Register(ManageKind, s.runManage)
	runner.Retryable(ManageKind)
	// Een hervatte dump wacht verder op dezelfde; terugzetten kan niet
	// opnieuw, want de nieuwe database staat er dan al.
	runner.Register(DumpKind, s.runDump)
	runner.Register(RestoreKind, s.runRestore)
	runner.Retryable(DumpKind)
	runner.OnFinished(DumpKind, s.failDumpsOf)
	// Een proefverhuizing gaat na een herstart verder op dezelfde import.
	runner.Register(MigrateTrialKind, s.runTrial)
	runner.OnFinished(MigrateTrialKind, s.failMigrationsOf)
	// De echte verhuizing gaat na een herstart verder, maar kan niet
	// opnieuw: dan staat de nieuwe database er al.
	runner.Register(MigrateKind, s.runMove)
	runner.OnFinished(MigrateKind, s.failMigrationsOf)
	// 'Database erbij' bij Cluster uitrollen.
	if dep != nil {
		dep.OnFinished(s.deployFinished)
	}
	return s
}

// Spec is de gewenste staat van een onderdeel, in de vorm van die van een
// uitrol. Params staan erin zonder geheimen; Secrets zijn de namen van de
// geheime parameters.
type Spec struct {
	Template deploy.SpecTemplate   `json:"template"`
	Cluster  templates.ClusterInfo `json:"cluster"`
	Params   map[string]any        `json:"params"`
	Secrets  []string              `json:"secrets"`
	Nodes    []Node                `json:"nodes"`
	// VIP is het VIP van de app dat de database volgt; nil zonder.
	VIP *VIP `json:"vip,omitempty"`
}

func (s Spec) vipAddress() string {
	if s.VIP == nil {
		return ""
	}
	return s.VIP.Address
}

// Node is een node van een onderdeel, met zijn rol en volgnummer binnen die
// rol.
type Node struct {
	NodeID   uuid.UUID `json:"node_id"`
	Hostname string    `json:"hostname"`
	Role     string    `json:"role"`
	Index    int       `json:"index"`
}

// deploySpec zet de spec om naar die van een uitrol, zodat renderen
// hetzelfde gaat als bij een template.
func (s Spec) deploySpec() deploy.Spec {
	out := deploy.Spec{Template: s.Template, Cluster: s.Cluster, Params: s.Params, Secrets: s.Secrets}
	for _, n := range s.Nodes {
		out.Nodes = append(out.Nodes, deploy.SpecNode{NodeID: n.NodeID, Hostname: n.Hostname, Role: n.Role, Index: n.Index})
	}
	return out
}

// order is de volgorde van toepassen: de datanodes op volgnummer, daarna
// de scheidsrechter, die een cluster nodig heeft om bij aan te sluiten.
func (s Spec) order() []Node {
	out := slices.Clone(s.Nodes)
	slices.SortStableFunc(out, func(a, b Node) int {
		if a.Role != b.Role {
			if a.Role == RoleData {
				return -1
			}
			return 1
		}
		return a.Index - b.Index
	})
	return out
}

func (s Spec) count(role string) int {
	n := 0
	for _, x := range s.Nodes {
		if x.Role == role {
			n++
		}
	}
	return n
}

func (s Spec) hosts(role string) []string {
	var out []string
	for _, n := range s.order() {
		if role == "" || n.Role == role {
			out = append(out, n.Hostname)
		}
	}
	return out
}

func parseSpec(b []byte) (Spec, error) {
	var s Spec
	if err := json.Unmarshal(b, &s); err != nil {
		return s, fmt.Errorf("spec van het onderdeel: %w", err)
	}
	return s, nil
}

// secretName is de naam van een geheim van een onderdeel in secrets: met de
// engine ervoor, zodat het nooit botst met een geheim van de template.
func secretName(engine, param string) string { return engine + "." + param }

// Toestand van een onderdeel, voor het scherm.
const (
	StateInstalling = "installing"
	StateApplying   = "applying"
	StateReady      = "ready"
	StateFailed     = "failed"
)

// Component is een onderdeel zoals het scherm het toont.
type Component struct {
	ID              uuid.UUID
	ClusterID       uuid.UUID
	Engine          string
	Template        string
	Version         string
	Title           string
	Revision        int
	AppliedRevision *int
	Params          map[string]any
	Nodes           []Node
	// VIP is het VIP dat de database volgt, met de nodes die het in
	// keepalived hebben en de node die het nu heeft; nil zonder.
	VIP       *ComponentVIP
	State     string
	JobID     *uuid.UUID
	JobError  string
	CreatedAt time.Time
	UpdatedAt time.Time
	// AutoRecover: na een volledige uitval vanzelf opnieuw beginnen.
	AutoRecover bool
	// DownSince is sinds wanneer de database weg is, met de reden; nil
	// zolang ze draait.
	DownSince  *time.Time
	DownReason string
	// NodesOut zijn de nodes die niet meedoen terwijl het cluster draait.
	NodesOut []NodeOut
	// Recovery is de laatste taak die de database opnieuw startte; nil als
	// er nog geen was.
	Recovery *Recovery
}

// NodeOut is een node die niet meedoet, met sinds wanneer en waarom.
type NodeOut struct {
	Hostname string
	Since    time.Time
	Reason   string
}

// Recovery is de laatste taak die de database opnieuw startte.
type Recovery struct {
	JobID  uuid.UUID
	Status string
	Error  string
}

// ComponentVIP is het VIP van een onderdeel, voor het scherm. Own is een
// eigen VIP van de database, met VRRP-id VRID.
type ComponentVIP struct {
	ID      uuid.UUID
	Address string
	Nodes   []string
	// Owner is de node die het VIP nu heeft, leeg als niemand.
	Owner string
	Own   bool
	VRID  int
}

// List geeft de onderdelen van een cluster.
func (s *Service) List(ctx context.Context, clusterID uuid.UUID) ([]Component, error) {
	rows, err := s.q.ListClusterComponents(ctx, clusterID)
	if err != nil {
		return nil, err
	}
	vips, err := s.q.ListVIPsByCluster(ctx, clusterID)
	if err != nil {
		return nil, err
	}
	out := make([]Component, 0, len(rows))
	for _, r := range rows {
		c, err := view(r.ClusterComponent)
		if err != nil {
			return nil, err
		}
		if r.ClusterComponent.RecoverJobID != nil && r.RecoverStatus.Valid {
			c.Recovery = &Recovery{JobID: *r.ClusterComponent.RecoverJobID, Status: string(r.RecoverStatus.JobStatus), Error: deref(r.RecoverError)}
		}
		if c.VIP != nil {
			for _, v := range vips {
				if v.Vip.ID == c.VIP.ID {
					c.VIP.Owner = deref(v.OwnerHostname)
				}
			}
		}
		running := r.JobStatus.Valid && (r.JobStatus.JobStatus == store.JobStatusQueued || r.JobStatus.JobStatus == store.JobStatusRunning)
		failed := r.JobStatus.Valid && (r.JobStatus.JobStatus == store.JobStatusFailed || r.JobStatus.JobStatus == store.JobStatusCanceled)
		switch {
		case running && c.AppliedRevision == nil:
			c.State = StateInstalling
		case running:
			c.State = StateApplying
		case failed:
			c.State = StateFailed
			c.JobError = deref(r.JobError)
		case c.AppliedRevision != nil:
			c.State = StateReady
		default:
			c.State = StateFailed
		}
		out = append(out, c)
	}
	return out, nil
}

func view(r store.ClusterComponent) (Component, error) {
	spec, err := parseSpec(r.Spec)
	if err != nil {
		return Component{}, err
	}
	c := Component{
		ID: r.ID, ClusterID: r.ClusterID, Engine: r.Engine, Template: spec.Template.Name, Version: spec.Template.Version,
		Revision: int(r.Revision), Params: spec.Params, Nodes: spec.order(), JobID: r.JobID, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
		AutoRecover: r.AutoRecover, DownSince: r.DownSince, DownReason: r.DownReason, NodesOut: []NodeOut{},
	}
	out := map[string]nodeOut{}
	_ = json.Unmarshal(r.NodesOut, &out)
	for _, n := range spec.order() {
		if o, ok := out[n.NodeID.String()]; ok {
			c.NodesOut = append(c.NodesOut, NodeOut{Hostname: o.Hostname, Since: o.Since, Reason: o.Reason})
		}
	}
	if r.AppliedRevision != nil {
		v := int(*r.AppliedRevision)
		c.AppliedRevision = &v
	}
	if v := spec.VIP; v != nil {
		c.VIP = &ComponentVIP{ID: v.ID, Address: v.Address, Nodes: []string{}, Own: v.Own, VRID: v.VRID}
		for _, n := range spec.order() {
			if slices.Contains(v.Nodes, n.NodeID) {
				c.VIP.Nodes = append(c.VIP.Nodes, n.Hostname)
			}
		}
	}
	if tpl, ok := registry.Get(spec.Template.Name, spec.Template.Version); ok {
		c.Title = tpl.Title
	}
	if c.Params == nil {
		c.Params = map[string]any{}
	}
	return c, nil
}

// get leest een onderdeel van een cluster.
func (s *Service) get(ctx context.Context, q *store.Queries, clusterID, id uuid.UUID, lock bool) (store.ClusterComponent, error) {
	var r store.ClusterComponent
	var err error
	if lock {
		r, err = q.LockClusterComponent(ctx, id)
	} else {
		r, err = q.GetClusterComponent(ctx, id)
	}
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && r.ClusterID != clusterID) {
		return r, ErrNotFound
	}
	return r, err
}

// Reapply past de huidige revisie opnieuw toe, bijvoorbeeld na een
// mislukte uitrol of als de driftcontrole een gewijzigd bestand ziet. Een
// node die al meedoet, sluit niet opnieuw aan; is zijn configuratie
// veranderd, dan herstart MariaDB daar, node voor node.
func (s *Service) Reapply(ctx context.Context, actor events.Actor, clusterID, id uuid.UUID) (store.Job, error) {
	c, err := s.q.GetCluster(ctx, clusterID)
	if err != nil {
		return store.Job{}, err
	}
	var j store.Job
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		r, err := s.get(ctx, q, clusterID, id, true)
		if err != nil {
			return err
		}
		spec, err := parseSpec(r.Spec)
		if err != nil {
			return err
		}
		j, err = s.jobs.EnqueueForClusterTx(ctx, q, jobs.Spec{
			Kind: Kind, Title: fmt.Sprintf("Database opnieuw toepassen: %s op %s", title(spec), c.Name),
			Params: jobParams{ComponentID: r.ID, Revision: int(r.Revision)}, ClusterID: &clusterID, Actor: actor,
		})
		if err != nil {
			return err
		}
		return q.SetClusterComponentJob(ctx, store.SetClusterComponentJobParams{ID: r.ID, JobID: &j.ID})
	})
	var busy jobs.BusyError
	if errors.As(err, &busy) {
		return store.Job{}, &ConflictError{busy.Error()}
	}
	if err == nil {
		s.jobs.Kick()
	}
	return j, err
}

// Remove haalt een onderdeel weg uit ClusterForge dat nog nooit helemaal
// gelukt is, zodat een nieuwe poging met andere nodes kan. Op de nodes
// verandert niets: wat de taak al installeerde, blijft staan.
func (s *Service) Remove(ctx context.Context, actor events.Actor, clusterID, id uuid.UUID) error {
	c, err := s.q.GetCluster(ctx, clusterID)
	if err != nil {
		return err
	}
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		r, err := s.get(ctx, q, clusterID, id, true)
		if err != nil {
			return err
		}
		spec, err := parseSpec(r.Spec)
		if err != nil {
			return err
		}
		if r.AppliedRevision != nil {
			return &ConflictError{"deze database draait; weghalen kan pas als de databases verhuisd zijn (mijlpaal 13)"}
		}
		if r.JobID != nil {
			jb, err := q.GetJob(ctx, *r.JobID)
			if err == nil && (jb.Job.Status == store.JobStatusQueued || jb.Job.Status == store.JobStatusRunning) {
				return &ConflictError{"de uitrol loopt nog; annuleer de taak eerst"}
			}
		}
		if err := q.DeleteClusterComponent(ctx, r.ID); err != nil {
			return err
		}
		// Een eigen VIP hoort bij de database en gaat mee weg.
		if spec.VIP != nil && spec.VIP.Own {
			if _, err := q.DeleteVIP(ctx, spec.VIP.ID); err != nil {
				return err
			}
		}
		if err := q.DeleteSecretsWithPrefix(ctx, store.DeleteSecretsWithPrefixParams{ClusterID: clusterID, Prefix: r.Engine + "."}); err != nil {
			return err
		}
		return s.ev.Write(ctx, q, events.Event{
			Actor: actor, SubjectType: "cluster", SubjectID: clusterID.String(), ClusterID: &clusterID, Action: "cluster.database_removed",
			Payload: map[string]any{"name": c.Name, "engine": r.Engine, "component": title(spec), "nodes": spec.hosts("")},
		})
	})
	return err
}

// mariaOnly houdt tegen wat er voor PostgreSQL nog niet is: verhuizen naar
// PostgreSQL komt in een volgende stap, het beheer na een uitval doet
// Patroni zelf.
func mariaOnly(r store.ClusterComponent, what string) error {
	switch r.Engine {
	case protocol.DBMariaDB:
		return nil
	case protocol.DBPostgreSQL:
		return &ConflictError{what + " kan bij PostgreSQL nog niet; dat komt in een volgende stap"}
	}
	return &ConflictError{what + " kan alleen bij MariaDB"}
}

func title(spec Spec) string {
	if tpl, ok := registry.Get(spec.Template.Name, spec.Template.Version); ok {
		return tpl.Title
	}
	return spec.Template.Name
}

func deref[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}
	return *p
}

func joinHosts(h []string) string {
	switch len(h) {
	case 0:
		return ""
	case 1:
		return h[0]
	}
	return strings.Join(h[:len(h)-1], ", ") + " en " + h[len(h)-1]
}
