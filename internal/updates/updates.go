// Package updates werkt de software op de nodes bij via de agents (fase
// 10): pakketten met apt en Docker Compose-projecten, als taak updates.run,
// één node tegelijk met de VIP-houder als laatste, en na elke node wachten
// tot hij gezond terug is. Op een node van een databasecluster blijven de
// databasepakketten staan; die gaan via Bijwerken op de databasekaart.
package updates

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/health"
	"github.com/Jonasz1996/clusterforge/internal/jobs"
	"github.com/Jonasz1996/clusterforge/internal/lifecycle"
	"github.com/Jonasz1996/clusterforge/internal/noderun"
	"github.com/Jonasz1996/clusterforge/internal/status"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// Kind is de soort taak.
const Kind = "updates.run"

// DatabasePackages blijven staan op een node van een databasecluster.
var DatabasePackages = []string{
	"mariadb-*", "galera-*", "libmariadb*", "mysql-common",
	"postgresql*", "libpq*", "patroni", "python3-etcd",
}

// Commander stuurt een commando naar een agent.
type Commander interface {
	Command(ctx context.Context, nodeID uuid.UUID, cmd protocol.Command) (protocol.Result, error)
	noderun.Bus
}

// ConflictError: het verzoek kan nu niet.
type ConflictError struct{ Msg string }

func (e *ConflictError) Error() string { return e.Msg }

// FieldError: een veld van het verzoek klopt niet.
type FieldError struct{ Msg string }

func (e *FieldError) Error() string { return e.Msg }

// Service toont en start updates.
type Service struct {
	pool   *pgxpool.Pool
	q      *store.Queries
	ev     *events.Writer
	runner *jobs.Runner
	bus    Commander
	Run    *noderun.Runner
	Gate   *health.Gate
	// DB schakelt bij PostgreSQL over voor de failover van de master. Mag
	// nil zijn.
	DB lifecycle.DB
	// Assist start na een mislukte automatische Docker-update een voorstel
	// voor een fix. Mag nil zijn.
	Assist func(ctx context.Context, nodeID uuid.UUID, jobID uuid.UUID, rep protocol.DockerReport)
	// MoveTimeout is hoe lang de VIP's mogen doen over verhuizen.
	MoveTimeout time.Duration
	// Notes haalt release notes op voor Fix voorstellen.
	Notes *Sources
	// ReadyTimeout is hoe lang een node na de update mag doen over gezond
	// terugkomen; RebootTimeout na een herstart.
	ReadyTimeout  time.Duration
	RebootTimeout time.Duration
	Now           func() time.Time
	// HTTPGet haalt een pagina op voor de webtest na de VIP-wissel; nil
	// gebruikt HTTP.
	HTTPGet func(ctx context.Context, url string) (int, string, error)
	// Containers geeft de stand van de containers op een node (zie
	// dockerhost.Running). Na de update van een node wacht de taak tot de
	// containers die ervoor draaiden weer draaien en gezond zijn, voor ze
	// aan de volgende node begint (testlijst 1.8). Mag nil zijn.
	Containers func(ctx context.Context, nodeID uuid.UUID) (map[string]string, error)
	// ContainerPoll is hoe vaak dat gevraagd wordt; nul is vijf seconden.
	ContainerPoll time.Duration
}

func NewService(pool *pgxpool.Pool, ev *events.Writer, runner *jobs.Runner, bus Commander) *Service {
	s := &Service{
		pool: pool, q: store.New(pool), ev: ev, runner: runner, bus: bus,
		Run: &noderun.Runner{Bus: bus}, Gate: health.NewGate(pool),
		ReadyTimeout: 3 * time.Minute, RebootTimeout: 10 * time.Minute, MoveTimeout: 2 * time.Minute, Now: time.Now,
	}
	if runner != nil {
		runner.Register(Kind, s.run)
		runner.Register(QueueKind, s.runQueue)
	}
	return s
}

// Node is een node zoals de pagina Updates hem toont.
type Node struct {
	NodeID      uuid.UUID
	Hostname    string
	ClusterID   *uuid.UUID
	ClusterName string
	ClusterSlug string
	ClusterEnv  string
	Online      bool
	Protocol    int
	// Supported: de agent kent runs (protocol 25).
	Supported bool
	// Smart: de agent kent de updates van fase 15 (protocol 29).
	Smart          bool
	Upgrades       *protocol.Upgrades
	RebootRequired bool
	AptUpdatedAt   time.Time
	Docker         *protocol.Docker
	HasDatabase    bool
	VIPHolder      bool
	FactsAt        *time.Time
	// PVEHost: de node is een Proxmox-host; die werk je bij via
	// Proxmox → Host-updates, niet met "Alles bijwerken" (fase 47).
	PVEHost bool
}

// Prod is true voor een node in een prodcluster.
func (n Node) Prod() bool { return n.ClusterEnv == string(store.EnvironmentProd) }

// Overview geeft elke node met een agent, behalve appliances (OPNsense,
// TrueNAS).
func (s *Service) Overview(ctx context.Context) ([]Node, error) {
	rows, err := s.q.ListUpdateNodes(ctx)
	if err != nil {
		return nil, err
	}
	vips := map[uuid.UUID][]string{}
	out := make([]Node, 0, len(rows))
	now := s.Now()
	for _, r := range rows {
		n := Node{
			NodeID: r.ID, Hostname: r.Hostname, ClusterID: r.ClusterID, ClusterName: r.ClusterName, ClusterSlug: r.ClusterSlug,
			ClusterEnv: r.ClusterEnvironment, Protocol: int(r.ProtocolVersion), HasDatabase: r.HasDatabase, FactsAt: r.CollectedAt,
			Online: r.HeartbeatAt != nil && now.Sub(*r.HeartbeatAt) <= status.HeartbeatLate,
		}
		n.Supported = n.Protocol >= protocol.RunSince
		n.Smart = n.Protocol >= protocol.UpdatesSince
		if len(r.Facts) > 0 {
			var f protocol.Facts
			if json.Unmarshal(r.Facts, &f) == nil {
				if f.Appliance() != "" {
					// OPNsense en TrueNAS werken zichzelf bij; apt en het
					// automatische beleid horen daar niet (die zouden elke
					// keer opnieuw mislukken).
					continue
				}
				n.Upgrades, n.RebootRequired, n.AptUpdatedAt, n.Docker = f.Upgrades, f.RebootRequired, f.AptUpdatedAt, f.Docker
				n.PVEHost = f.Proxmox != nil && f.Proxmox.Node != ""
			}
		}
		if r.ClusterID != nil {
			cv, ok := vips[*r.ClusterID]
			if !ok {
				if cv, err = s.clusterVIPs(ctx, *r.ClusterID); err != nil {
					return nil, err
				}
				vips[*r.ClusterID] = cv
			}
			n.VIPHolder = slices.ContainsFunc(r.Addresses, func(a string) bool { return slices.Contains(cv, a) })
		}
		out = append(out, n)
	}
	return out, nil
}

func (s *Service) clusterVIPs(ctx context.Context, clusterID uuid.UUID) ([]string, error) {
	rows, err := s.q.ListVIPsByCluster(ctx, clusterID)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Vip.Address.String())
	}
	return out, nil
}

// Apt zegt hoe apt bijwerkt; nil is niet.
type Apt struct {
	Full       bool `json:"full,omitempty"`
	Autoremove bool `json:"autoremove,omitempty"`
}

// Docker zegt welke Compose-projecten; nil is niet.
type Docker struct {
	// Projects: leeg is alle projecten op elke node.
	Projects []string `json:"projects,omitempty"`
	Prune    bool     `json:"prune,omitempty"`
	// Vanaf fase 15 (protocol 29): ook bij geen nieuw image opnieuw maken,
	// hoe lang de test duurt, niet terugzetten, en nieuwe tags.
	Always        bool                `json:"always,omitempty"`
	HealthSeconds int                 `json:"health_seconds,omitempty"`
	NoRollback    bool                `json:"no_rollback,omitempty"`
	Tags          []protocol.ImageTag `json:"tags,omitempty"`
	// Edit schrijft een bestand uit de editor (één node, één project).
	Edit *protocol.ComposeEdit `json:"edit,omitempty"`
	// Skip zijn projecten die deze keer niet bijgewerkt worden (fase 47),
	// zoals een project waarvan de pull mislukte. Start zet per node de
	// projecten die overblijven.
	Skip []string `json:"skip,omitempty"`
}

// needsSmart is true als de Docker-opties protocol 29 vragen.
func (d *Docker) needsSmart() bool {
	return d != nil && (d.Always || d.HealthSeconds > 0 || d.NoRollback || len(d.Tags) > 0 || d.Edit != nil)
}

// Request is wat de beheerder vraagt.
type Request struct {
	// NodeIDs of ClusterID.
	NodeIDs   []uuid.UUID
	ClusterID *uuid.UUID
	Apt       *Apt
	Docker    *Docker
	// Reboot herstart een node als hij daar na de update om vraagt.
	Reboot bool
	// NoFailover werkt de VIP-houder bij zonder eerst zijn VIP's naar een
	// bijgewerkte node te verhuizen (zoals in fase 10).
	NoFailover bool
	// Auto: gestart door automatisch bijwerken.
	Auto bool
	// StartFrom hervat een clusterupdate vanaf deze node (fase 47): de
	// nodes ervoor in de volgorde zijn al bijgewerkt. Alleen met ClusterID.
	StartFrom *uuid.UUID
}

// Select geeft de nodes van een verzoek, in de volgorde van de taak per
// cluster: zonder VIP eerst, de VIP-houder als laatste.
func (s *Service) Select(ctx context.Context, req Request) ([]Node, error) {
	if req.Apt == nil && req.Docker == nil {
		return nil, &FieldError{"kies pakketten (apt), Docker of beide"}
	}
	if (len(req.NodeIDs) > 0) == (req.ClusterID != nil) {
		return nil, &FieldError{"kies nodes of een cluster"}
	}
	all, err := s.Overview(ctx)
	if err != nil {
		return nil, err
	}
	var out []Node
	for _, n := range all {
		switch {
		case req.ClusterID != nil && (n.ClusterID == nil || *n.ClusterID != *req.ClusterID):
			continue
		case len(req.NodeIDs) > 0 && !slices.Contains(req.NodeIDs, n.NodeID):
			continue
		}
		switch {
		case !n.Supported:
			return nil, &ConflictError{fmt.Sprintf("de agent op %s heeft protocol %d en kent updates nog niet (vanaf %d); werk de agents eerst bij", n.Hostname, n.Protocol, protocol.RunSince)}
		case !n.Online:
			return nil, &ConflictError{"de agent op " + n.Hostname + " is niet verbonden"}
		case !n.Smart && req.Docker.needsSmart():
			return nil, &ConflictError{fmt.Sprintf("de agent op %s heeft protocol %d; nieuwe tags, de test en terugzetten vragen protocol %d. Werk de agents eerst bij", n.Hostname, n.Protocol, protocol.UpdatesSince)}
		}
		out = append(out, n)
	}
	if len(req.NodeIDs) > 0 && len(out) < len(req.NodeIDs) {
		return nil, &ConflictError{"niet elke gevraagde node heeft een agent"}
	}
	if len(out) == 0 {
		return nil, &ConflictError{"dit cluster heeft geen nodes met een agent"}
	}
	out = Order(out)
	if req.StartFrom != nil {
		if req.ClusterID == nil {
			return nil, &FieldError{"hervatten kan alleen bij een cluster"}
		}
		i := slices.IndexFunc(out, func(n Node) bool { return n.NodeID == *req.StartFrom })
		if i < 0 {
			return nil, &ConflictError{"de node om mee verder te gaan hoort niet (meer) bij dit cluster"}
		}
		out = out[i:]
	}
	return out, nil
}

// Order zet de nodes per cluster op volgorde: zonder VIP eerst, de
// VIP-houders als laatste; de clusters blijven in hun volgorde.
func Order(nodes []Node) []Node {
	out := slices.Clone(nodes)
	key := func(n Node) uuid.UUID {
		if n.ClusterID == nil {
			return uuid.Nil
		}
		return *n.ClusterID
	}
	first := map[uuid.UUID]int{}
	for i, n := range out {
		if _, ok := first[key(n)]; !ok {
			first[key(n)] = i
		}
	}
	slices.SortStableFunc(out, func(a, b Node) int {
		if d := first[key(a)] - first[key(b)]; d != 0 {
			return d
		}
		switch {
		case a.VIPHolder == b.VIPHolder:
			return 0
		case a.VIPHolder:
			return 1
		}
		return -1
	})
	return out
}

// params zijn de parameters van de taak.
type params struct {
	Nodes      []jobNode `json:"nodes"`
	Apt        *Apt      `json:"apt,omitempty"`
	Docker     *Docker   `json:"docker,omitempty"`
	Reboot     bool      `json:"reboot,omitempty"`
	NoFailover bool      `json:"no_failover,omitempty"`
	Auto       bool      `json:"auto,omitempty"`
}

type jobNode struct {
	ID          uuid.UUID `json:"id"`
	Hostname    string    `json:"hostname"`
	VIPHolder   bool      `json:"vip_holder,omitempty"`
	HasDatabase bool      `json:"has_database,omitempty"`
	// Smart: de agent had protocol 29 toen de taak begon.
	Smart bool `json:"smart,omitempty"`
	// Projects: met Docker.Skip de projecten die op deze node bijgewerkt
	// worden; SkipDocker als er geen overblijven (fase 47).
	Projects   []string `json:"projects,omitempty"`
	SkipDocker bool     `json:"skip_docker,omitempty"`
}

// Start zet per cluster één taak in de wachtrij. Een cluster waar al een
// taak loopt, slaat het over met de reden in skipped.
func (s *Service) Start(ctx context.Context, actor events.Actor, req Request, nodes []Node) (started []store.Job, skipped []string, err error) {
	type group struct {
		cluster *uuid.UUID
		name    string
		nodes   []Node
	}
	var groups []*group
	byKey := map[uuid.UUID]*group{}
	for _, n := range nodes {
		key := uuid.Nil
		if n.ClusterID != nil {
			key = *n.ClusterID
		}
		g := byKey[key]
		if g == nil {
			g = &group{cluster: n.ClusterID, name: n.ClusterName}
			byKey[key] = g
			groups = append(groups, g)
		}
		g.nodes = append(g.nodes, n)
	}
	what := whatText(req.Apt, req.Docker)
	for _, g := range groups {
		p := params{Apt: req.Apt, Docker: req.Docker, Reboot: req.Reboot, NoFailover: req.NoFailover, Auto: req.Auto}
		for _, n := range g.nodes {
			jn := jobNode{ID: n.NodeID, Hostname: n.Hostname, VIPHolder: n.VIPHolder, HasDatabase: n.HasDatabase, Smart: n.Smart}
			if req.Docker != nil && len(req.Docker.Skip) > 0 {
				jn.Projects = keepProjects(n, req.Docker.Projects, req.Docker.Skip)
				jn.SkipDocker = len(jn.Projects) == 0
			}
			p.Nodes = append(p.Nodes, jn)
		}
		spec := jobs.Spec{Kind: Kind, Params: p, ClusterID: g.cluster, Actor: actor}
		switch {
		case len(g.nodes) == 1:
			spec.Title, spec.NodeID = fmt.Sprintf("%s bijwerken op %s", what, g.nodes[0].Hostname), &g.nodes[0].NodeID
		case g.cluster != nil:
			spec.Title = fmt.Sprintf("%s bijwerken in %s", what, g.name)
		default:
			spec.Title = what + " bijwerken op nodes zonder cluster"
		}
		if req.Auto {
			spec.Title = "Automatisch: " + spec.Title
		}
		if req.StartFrom != nil && len(g.nodes) > 0 {
			spec.Title += " (verder vanaf " + g.nodes[0].Hostname + ")"
		}
		var j store.Job
		var err error
		if g.cluster != nil {
			j, err = s.runner.EnqueueForCluster(ctx, spec)
		} else {
			j, err = s.runner.Enqueue(ctx, spec)
		}
		var busy jobs.BusyError
		switch {
		case errors.As(err, &busy):
			skipped = append(skipped, busy.Error())
		case err != nil:
			return started, skipped, err
		default:
			started = append(started, j)
		}
	}
	if len(started) == 0 && len(skipped) > 0 {
		return nil, skipped, &ConflictError{strings.Join(skipped, "; ")}
	}
	s.runner.Kick()
	return started, skipped, nil
}

func whatText(a *Apt, d *Docker) string {
	switch {
	case a != nil && d != nil:
		return "Pakketten en Docker"
	case d != nil:
		return "Docker"
	}
	return "Pakketten"
}

// prep is de state van de eerste stap per node: de diensten die vóór de
// update draaiden.
type prep struct {
	Services []string  `json:"services"`
	At       time.Time `json:"at"`
	// Containers zijn de containers die vóór de update draaiden.
	Containers []string `json:"containers,omitempty"`
}

func (s *Service) run(ctx context.Context, j *jobs.Job) error {
	var p params
	if err := j.Decode(&p); err != nil {
		return err
	}
	failover := !p.NoFailover && len(p.Nodes) > 1
	for i, n := range p.Nodes {
		name := n.Hostname
		if n.VIPHolder {
			name += " (VIP-houder)"
		}
		// De state van een stap uit deze poging; een stap uit een eerdere
		// poging wordt overgeslagen en komt uit j.Done.
		var fs failState
		if n.VIPHolder && failover {
			if err := s.failoverStep(ctx, j, name, n, p.Nodes[:i], &fs); err != nil {
				return stop(ctx, err, n)
			}
		}
		prepStep := name + ": voorbereiden"
		var pr prep
		j.Done(prepStep, &pr)
		if err := j.Step(ctx, prepStep, func(ctx context.Context, st *jobs.Step) error {
			var err error
			pr, err = s.prepare(ctx, st, n)
			return err
		}); err != nil {
			return stop(ctx, err, n)
		}
		var aptRes protocol.RunStatus
		aptDone := false
		if p.Apt != nil {
			spec := protocol.RunSpec{
				ID: runID(j, i, "apt"), Kind: protocol.RunApt, Title: "Pakketten bijwerken op " + n.Hostname,
				TimeoutSeconds: int((time.Hour).Seconds()),
				Apt:            &protocol.AptRun{Full: p.Apt.Full, Autoremove: p.Apt.Autoremove, Smart: n.Smart},
			}
			if n.HasDatabase {
				spec.Apt.Hold = DatabasePackages
			}
			if err := j.Step(ctx, name+": pakketten (apt)", func(ctx context.Context, st *jobs.Step) error {
				if n.HasDatabase {
					st.Logf("dit cluster heeft een database: de databasepakketten blijven staan; werk die bij met Bijwerken op de databasekaart")
				}
				res, err := s.runOne(ctx, j, st, n, spec)
				if err != nil && runLost(res) {
					// Bijvoorbeeld een upgrade van systemd of een herstart
					// midden in dpkg: de volgende run maakt dpkg eerst af
					// (dpkg --configure -a) en gaat verder.
					st.Logf("de run op %s viel weg; ClusterForge wacht op de agent en hervat de update (dpkg --configure -a eerst)", n.Hostname)
					_ = st.Flush(ctx)
					if werr := s.waitAgent(ctx, n); werr != nil {
						return fmt.Errorf("%w; %w", err, werr)
					}
					again := spec
					again.ID = spec.ID + "-hervat"
					res, err = s.runOne(ctx, j, st, n, again)
				}
				aptRes, aptDone = res, true
				return err
			}); err != nil {
				return stop(ctx, err, n)
			}
		}
		if p.Docker != nil && n.SkipDocker {
			if err := j.Step(ctx, name+": Docker overgeslagen", func(ctx context.Context, st *jobs.Step) error {
				st.Logf("de gekozen Compose-projecten worden deze keer overgeslagen: %s", strings.Join(p.Docker.Skip, ", "))
				return nil
			}); err != nil {
				return stop(ctx, err, n)
			}
		}
		if p.Docker != nil && !n.SkipDocker {
			projects := p.Docker.Projects
			if len(n.Projects) > 0 {
				projects = n.Projects
			}
			spec := protocol.RunSpec{
				ID: runID(j, i, "docker"), Kind: protocol.RunDocker, Title: "Docker bijwerken op " + n.Hostname,
				TimeoutSeconds: int((time.Hour).Seconds()),
				Docker:         &protocol.DockerRun{Projects: projects, Prune: p.Docker.Prune},
			}
			if n.Smart {
				d := spec.Docker
				d.Smart, d.Always, d.HealthSeconds, d.NoRollback, d.Tags, d.Edit = true, p.Docker.Always, p.Docker.HealthSeconds, p.Docker.NoRollback, p.Docker.Tags, p.Docker.Edit
			}
			if err := j.Step(ctx, name+": Docker", func(ctx context.Context, st *jobs.Step) error {
				res, err := s.runOne(ctx, j, st, n, spec)
				s.saveReports(ctx, j, n, res.Docker)
				if err != nil && onlyUnchanged(res.Docker) {
					// Eén project kon zijn images niet halen, maar er
					// veranderde niets: de rest van het cluster gaat door.
					for _, d := range res.Docker {
						if d.Unchanged {
							st.Logf("let op: %s op %s niet bijgewerkt: %s", d.Project, n.Hostname, d.Problem)
						}
					}
					return nil
				}
				if err != nil && p.Auto && s.Assist != nil {
					for _, d := range res.Docker {
						if d.Result == protocol.DockerFailed || d.Result == protocol.DockerRolledBack {
							s.Assist(context.WithoutCancel(ctx), n.ID, j.ID, d)
						}
					}
				}
				return err
			}); err != nil {
				return stop(ctx, err, n)
			}
		}
		if p.Reboot {
			aptStep := name + ": pakketten (apt)"
			if err := j.Step(ctx, name+": herstarten als het nodig is", func(ctx context.Context, st *jobs.Step) error {
				res, ok := aptRes, aptDone
				if !ok {
					res, ok = noderun.Result(j, aptStep)
				}
				if !ok || !res.RebootRequired {
					st.Logf("%s vraagt geen herstart", n.Hostname)
					return nil
				}
				return s.reboot(ctx, st, n)
			}); err != nil {
				return stop(ctx, err, n)
			}
		}
		if n.VIPHolder && failover {
			if err := s.rejoinStep(ctx, j, name, n, fs); err != nil {
				return stop(ctx, err, n)
			}
		}
		if err := j.Step(ctx, name+": gezond terug", func(ctx context.Context, st *jobs.Step) error {
			return s.settle(ctx, st, n, pr)
		}); err != nil {
			return stop(ctx, err, n)
		}
	}
	return nil
}

// keepProjects zijn de Compose-projecten van een node zonder de
// overgeslagen; projects beperkt ze verder als het niet leeg is.
func keepProjects(n Node, projects, skip []string) []string {
	var out []string
	if n.Docker == nil {
		return nil
	}
	for _, p := range n.Docker.Projects {
		if slices.Contains(skip, p.Name) || (len(projects) > 0 && !slices.Contains(projects, p.Name)) {
			continue
		}
		out = append(out, p.Name)
	}
	return out
}

// stop zegt bij een fout dat de nodes erna niet aan de beurt kwamen.
func stop(ctx context.Context, err error, n jobNode) error {
	if ctx.Err() != nil {
		return err
	}
	return fmt.Errorf("%s: %w; de nodes erna zijn niet bijgewerkt", n.Hostname, err)
}

// runID is uniek per taak, poging, node en soort.
func runID(j *jobs.Job, i int, kind string) string {
	return fmt.Sprintf("%s-%d-%d-%s", j.ID, j.Attempts, i, kind)
}

// prepare onthoudt welke gevolgde diensten nu draaien en controleert dat
// de agent er is.
func (s *Service) prepare(ctx context.Context, st *jobs.Step, n jobNode) (prep, error) {
	rt, err := s.q.GetNodeRuntime(ctx, n.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return prep{}, errors.New("de node bestaat niet meer")
	}
	if err != nil {
		return prep{}, err
	}
	switch {
	case !rt.HasAgent:
		return prep{}, errors.New("de node heeft geen agent meer")
	case rt.AgentProtocol < protocol.RunSince:
		return prep{}, fmt.Errorf("de agent heeft protocol %d en kent updates nog niet (vanaf %d)", rt.AgentProtocol, protocol.RunSince)
	case rt.HeartbeatAt == nil || s.Now().Sub(*rt.HeartbeatAt) > status.HeartbeatLate:
		return prep{}, errors.New("de agent is niet verbonden")
	}
	var active []string
	for name, state := range health.ServiceStates(rt.Services) {
		if state == "active" {
			active = append(active, name)
		}
	}
	slices.Sort(active)
	if i := slices.Index(active, "ufw"); i >= 0 && !s.ufwOn(ctx, n.ID) {
		// ufw.service is een oneshot die ook "active" is met een
		// uitgeschakelde firewall; na de update kan hij "inactive" zijn
		// zonder dat er iets veranderde. Alleen een ufw die echt filterde,
		// moet terugkomen.
		active = slices.Delete(active, i, i+1)
		st.Logf("ufw stond vóór de update uit; daar wacht ClusterForge niet op")
	}
	if len(active) > 0 {
		st.Logf("draait nu: %s; daar wacht ClusterForge na de update op", strings.Join(active, ", "))
	}
	pr := prep{Services: active, At: s.Now()}
	pr.Containers = s.runningContainers(ctx, n.ID)
	if len(pr.Containers) > 0 {
		st.Logf("%d containers draaien nu; na de update wacht ClusterForge tot ze weer draaien en gezond zijn, pas dan volgt de volgende node", len(pr.Containers))
	}
	return pr, st.SetState(ctx, pr)
}

// runOne voert een run uit; alles behalve ok is een fout.
func (s *Service) runOne(ctx context.Context, j *jobs.Job, st *jobs.Step, n jobNode, spec protocol.RunSpec) (protocol.RunStatus, error) {
	res, err := s.Run.Do(ctx, st, n.ID, spec)
	if err != nil {
		return res, err
	}
	if s.ev != nil {
		actor := events.System()
		if j.RequestedBy != nil {
			actor = events.User(*j.RequestedBy)
		}
		action := "node.software_updated"
		if res.State != protocol.RunOK {
			action = "node.software_update_failed"
		}
		_ = s.ev.Write(ctx, nil, events.Event{Actor: actor, SubjectType: "node", SubjectID: n.ID.String(), ClusterID: j.ClusterID,
			Action: action, Payload: map[string]any{
				"hostname": n.Hostname, "kind": spec.Kind, "result": res.State, "held": res.Held,
				"reboot_required": res.RebootRequired, "job_id": j.ID.String(),
			}})
	}
	if len(res.Held) > 0 {
		st.Logf("bleven staan: %s", strings.Join(res.Held, ", "))
	}
	for _, f := range res.ConfNew {
		st.Logf("nieuwe standaardconfiguratie naast de jouwe: %s", f)
	}
	for _, d := range res.Docker {
		st.Logf("%s", DescribeDocker(d))
	}
	if res.RebootRequired {
		st.Logf("%s vraagt om een herstart", n.Hostname)
	}
	if res.State != protocol.RunOK {
		return res, errors.New(noderun.Describe(res))
	}
	return res, nil
}

// webTemplates zijn de templates waarvan het VIP een webserver is.
var webTemplates = []string{"nginx-proxy-manager", "keepalived-nginx"}

// webCheck vraagt na de VIP-wissel echt een pagina op het VIP op: een node
// waar NPM niet kon starten (poort 80 bezet door nginx op de host) geeft
// "Welcome to nginx!" voor elke site, terwijl keepalived tevreden is.
// Onbereikbaar vanaf de server is alleen een waarschuwing (een firewall
// kan dat verkeer tegenhouden).
func (s *Service) webCheck(ctx context.Context, clusterID uuid.UUID, vips []string, progress func(string)) error {
	var tpl string
	if err := s.pool.QueryRow(ctx, `SELECT coalesce(template_name, '') FROM clusters WHERE id = $1`, clusterID).Scan(&tpl); err != nil || !slices.Contains(webTemplates, tpl) {
		return nil
	}
	get := s.HTTPGet
	if get == nil {
		get = httpGet
	}
	for _, vip := range vips {
		url := "http://" + vip + "/"
		var last error
		hard := false
		for try := 0; try < 6; try++ {
			if try > 0 {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(5 * time.Second):
				}
			}
			code, body, err := get(ctx, url)
			switch {
			case err != nil:
				last, hard = err, false
				continue
			case strings.Contains(body, "Welcome to nginx!"):
				hard = true
				last = fmt.Errorf("%s geeft de standaardpagina van nginx (\"Welcome to nginx!\"): NPM draait niet op de node met het VIP; kijk of poort 80 daar bezet is door nginx op de host", url)
				continue
			case code >= 500:
				hard = true
				last = fmt.Errorf("%s antwoordt met HTTP %d", url, code)
				continue
			}
			last = nil
			progress(fmt.Sprintf("%s antwoordt (HTTP %d)", url, code))
			break
		}
		if last == nil {
			continue
		}
		if hard {
			return fmt.Errorf("%w; de taak stopt hier", last)
		}
		progress(fmt.Sprintf("let op: %s is vanaf ClusterForge niet te bereiken (%v); de webtest is overgeslagen", url, last))
	}
	return nil
}

// httpGet haalt een pagina op (hoogstens 64 KiB).
func httpGet(ctx context.Context, url string) (int, string, error) {
	rctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, "", err
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := client.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer func() { _ = res.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
	return res.StatusCode, string(body), nil
}

// onlyUnchanged is true als er projecten mislukten en geen ervan iets
// veranderde.
func onlyUnchanged(reps []protocol.DockerReport) bool {
	failed := 0
	for _, d := range reps {
		if d.Result == protocol.DockerFailed || d.Result == protocol.DockerRolledBack {
			if !d.Unchanged {
				return false
			}
			failed++
		}
	}
	return failed > 0
}

// ufwOn zegt of ufw volgens de laatste firewallstand van de node aan
// stond. Zonder stand telt ufw als aan.
func (s *Service) ufwOn(ctx context.Context, nodeID uuid.UUID) bool {
	var raw []byte
	err := s.pool.QueryRow(ctx, `SELECT status FROM firewall_nodes WHERE node_id = $1`, nodeID).Scan(&raw)
	if err != nil || len(raw) == 0 {
		return true
	}
	var fs protocol.FirewallStatus
	if json.Unmarshal(raw, &fs) != nil || !fs.Installed {
		return true
	}
	return fs.Active
}

// runLost is true voor een run die op de node wegviel zonder eindstand.
func runLost(res protocol.RunStatus) bool {
	return res.State == protocol.RunFailed && strings.Contains(res.Error, protocol.RunLostError)
}

// waitAgent wacht op een verse heartbeat van de agent.
func (s *Service) waitAgent(ctx context.Context, n jobNode) error {
	since := s.Now()
	wctx, cancel := context.WithTimeout(ctx, s.RebootTimeout)
	defer cancel()
	err := health.Poll(wctx, 2*time.Second, func(ctx context.Context) (bool, error) {
		rt, err := s.q.GetNodeRuntime(ctx, n.ID)
		if err != nil {
			return false, err
		}
		return rt.HeartbeatAt != nil && rt.HeartbeatAt.After(since), nil
	})
	if err != nil {
		return fmt.Errorf("de agent van %s kwam niet binnen %s terug: %w", n.Hostname, s.RebootTimeout, err)
	}
	return nil
}

// reboot herstart de node en wacht tot hij terug is.
func (s *Service) reboot(ctx context.Context, st *jobs.Step, n jobNode) error {
	sent := s.Now()
	st.Logf("%s herstart", n.Hostname)
	_ = st.Flush(ctx)
	res, err := s.bus.Command(ctx, n.ID, protocol.Command{
		ID: uuid.NewString(), Action: protocol.CmdReboot, Deadline: sent.Add(time.Minute), DelaySeconds: 3, Reason: "updates",
	})
	if err != nil {
		return fmt.Errorf("herstarten: %w", err)
	}
	if !res.OK {
		return fmt.Errorf("herstarten: %s", res.Error)
	}
	wctx, cancel := context.WithTimeout(ctx, s.RebootTimeout)
	defer cancel()
	err = health.Poll(wctx, 2*time.Second, func(ctx context.Context) (bool, error) {
		rt, err := s.q.GetNodeRuntime(ctx, n.ID)
		if err != nil {
			return false, err
		}
		up := time.Duration(rt.UptimeSeconds) * time.Second
		return rt.HeartbeatAt != nil && rt.HeartbeatAt.After(sent) && up < s.Now().Sub(sent), nil
	})
	if err != nil {
		return fmt.Errorf("%s kwam na de herstart niet binnen %s terug: %w", n.Hostname, s.RebootTimeout, err)
	}
	st.Logf("%s is terug", n.Hostname)
	return nil
}

// settle wacht tot de diensten van vóór de update weer draaien en de VIP's
// een vaste houder hebben, en vraagt nieuwe facts.
func (s *Service) settle(ctx context.Context, st *jobs.Step, n jobNode, pr prep) error {
	since := s.Now()
	progress := func(msg string) {
		st.Logf("%s", msg)
		_ = st.Flush(ctx)
	}
	st.Logf("wachten op twee gezonde heartbeats van %s", n.Hostname)
	_ = st.Flush(ctx)
	rctx, cancel := context.WithTimeout(ctx, s.ReadyTimeout)
	err := s.Gate.NodeReady(rctx, n.ID, since, pr.Services, progress)
	cancel()
	if err != nil {
		return fmt.Errorf("%s komt niet gezond terug: %w; de taak stopt hier", n.Hostname, err)
	}
	if err := s.waitContainers(ctx, n, pr.Containers, progress); err != nil {
		return err
	}
	if rt, err := s.q.GetNodeRuntime(ctx, n.ID); err == nil && rt.ClusterID != nil {
		vips, err := s.clusterVIPs(ctx, *rt.ClusterID)
		if err != nil {
			return err
		}
		if len(vips) > 0 {
			sctx, cancel := context.WithTimeout(ctx, s.ReadyTimeout)
			_, err := s.Gate.Settled(sctx, *rt.ClusterID, vips, since, nil, progress)
			cancel()
			if err != nil {
				return fmt.Errorf("de VIP's hebben geen vaste houder: %w; de taak stopt hier", err)
			}
			if err := s.webCheck(ctx, *rt.ClusterID, vips, progress); err != nil {
				return err
			}
		}
	}
	fctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	_, _ = s.bus.Command(fctx, n.ID, protocol.Command{ID: uuid.NewString(), Action: protocol.CmdFactsCollect, Deadline: s.Now().Add(30 * time.Second)})
	cancel()
	st.Logf("%s is gezond", n.Hostname)
	return nil
}

// runningContainers zijn de containers die nu draaien en niet ongezond
// zijn, op een node met Docker; leeg zonder Docker of zonder antwoord.
func (s *Service) runningContainers(ctx context.Context, nodeID uuid.UUID) []string {
	if s.Containers == nil {
		return nil
	}
	var docker bool
	if err := s.pool.QueryRow(ctx, `SELECT coalesce(jsonb_typeof(facts->'docker') = 'object', false) FROM node_facts WHERE node_id = $1`, nodeID).Scan(&docker); err != nil || !docker {
		return nil
	}
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	m, err := s.Containers(cctx, nodeID)
	if err != nil {
		return nil
	}
	var out []string
	for name, st := range m {
		if st == "running" || st == "healthy" || st == "starting" {
			out = append(out, name)
		}
	}
	slices.Sort(out)
	return out
}

// waitContainers wacht tot elke container uit want weer draait, en gezond
// is als hij een healthcheck heeft. Een Docker- of containerd-upgrade
// herstart alle containers van de node; zonder wachten lag bij een rolling
// update het hele cluster tegelijk plat (testlijst 1.8).
func (s *Service) waitContainers(ctx context.Context, n jobNode, want []string, progress func(string)) error {
	if len(want) == 0 || s.Containers == nil {
		return nil
	}
	poll := s.ContainerPoll
	if poll <= 0 {
		poll = 5 * time.Second
	}
	progress(fmt.Sprintf("wachten tot de %d containers op %s weer draaien en gezond zijn", len(want), n.Hostname))
	deadline := s.Now().Add(s.ReadyTimeout)
	var last string
	for {
		cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		m, err := s.Containers(cctx, n.ID)
		cancel()
		var waiting []string
		if err != nil {
			waiting = []string{"Docker antwoordt nog niet (" + err.Error() + ")"}
		} else {
			for _, name := range want {
				switch st := m[name]; st {
				case "running", "healthy":
				case "":
					waiting = append(waiting, name+" (weg)")
				default:
					waiting = append(waiting, name+" ("+st+")")
				}
			}
		}
		if len(waiting) == 0 {
			progress(fmt.Sprintf("alle %d containers op %s draaien en zijn gezond", len(want), n.Hostname))
			return nil
		}
		if msg := strings.Join(waiting, ", "); msg != last {
			progress("nog niet klaar: " + msg)
			last = msg
		}
		if !s.Now().Before(deadline) {
			return fmt.Errorf("de containers op %s zijn na %s nog niet gezond: %s; de taak stopt hier, de volgende nodes blijven ongemoeid", n.Hostname, s.ReadyTimeout, strings.Join(waiting, ", "))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(poll):
		}
	}
}
