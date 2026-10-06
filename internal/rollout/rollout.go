// Package rollout past een wijziging die meer dan één node raakt toe met
// één taak, cluster.apply: node voor node, met de VIP-eigenaar als laatste,
// en na elke node de gezondheidspoort. De taak stopt zodra iets niet gezond
// is, zodat de nodes daarna ongemoeid blijven. In deze versie is herstel van
// drift de enige modus; GitOps en het bijwerken van een templateversie
// gebruiken later dezelfde taak.
package rollout

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Jonasz1996/clusterforge/internal/deploy"
	"github.com/Jonasz1996/clusterforge/internal/drift"
	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/health"
	"github.com/Jonasz1996/clusterforge/internal/jobs"
	"github.com/Jonasz1996/clusterforge/internal/status"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/internal/templates"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// Kind is de soort taak.
const Kind = "cluster.apply"

// ModeRemediate: alleen de gekozen afwijkende stappen opnieuw toepassen.
const ModeRemediate = "remediate"

// FieldError is een fout in één veld van de invoer.
type FieldError struct{ Field, Message string }

func (e *FieldError) Error() string { return e.Message }

// ConflictError betekent dat het nu niet kan, met een stabiele code.
type ConflictError struct{ Code, Msg string }

func (e *ConflictError) Error() string { return e.Msg }

var ErrNotFound = errors.New("niet gevonden")

type Service struct {
	pool  *pgxpool.Pool
	q     *store.Queries
	ev    *events.Writer
	log   *slog.Logger
	jobs  *jobs.Runner
	bus   deploy.Commander
	dep   *deploy.Service
	drift *drift.Service

	// Gate wacht op verse heartbeats.
	Gate *health.Gate
	// ReadyTimeout is hoe lang een node na het toepassen mag doen over twee
	// gezonde heartbeats; SettleTimeout hoe lang daarna elk VIP over één
	// houder mag doen.
	ReadyTimeout, SettleTimeout time.Duration
	// CheckTimeout is de langste wachttijd per controle van de template na
	// een node; 0 is wat de template zegt.
	CheckTimeout time.Duration
	// Fresh is hoe oud de heartbeat van een node mag zijn die een VIP kan
	// overnemen.
	Fresh time.Duration
	// Retry is de pauze voor een nieuwe poging om de agent te bereiken.
	Retry time.Duration
	// Changed wordt aangeroepen na een taak, zodat de status meteen opnieuw
	// berekend wordt. Mag nil zijn.
	Changed func()
}

func NewService(pool *pgxpool.Pool, ev *events.Writer, log *slog.Logger, runner *jobs.Runner, bus deploy.Commander,
	dep *deploy.Service, drf *drift.Service) *Service {
	s := &Service{
		pool: pool, q: store.New(pool), ev: ev, log: log, jobs: runner, bus: bus, dep: dep, drift: drf,
		Gate: health.NewGate(pool), ReadyTimeout: 2 * time.Minute, SettleTimeout: time.Minute,
		Fresh: status.HeartbeatLate, Retry: 5 * time.Second,
	}
	// Niet Retryable: opnieuw proberen is een nieuw herstel vanuit wat nu
	// op de nodes staat.
	runner.Register(Kind, s.run)
	runner.OnFinished(Kind, s.finished)
	return s
}

// finished controleert het cluster meteen opnieuw, zodat verdwenen drift
// het event drift.resolved met deze taak krijgt.
func (s *Service) finished(ctx context.Context, j store.Job) {
	if j.ClusterID != nil {
		if err := s.drift.CheckAfterJob(ctx, *j.ClusterID, j.ID); err != nil {
			s.log.Warn("driftcontrole na herstel mislukt", "job", j.ID, "err", err)
		}
	}
	if s.Changed != nil {
		s.Changed()
	}
}

// Choice is wat een beheerder op één node koos om te herstellen.
type Choice struct {
	NodeID uuid.UUID
	Steps  []StepChoice
}

// StepChoice is één afwijkende stap, zoals file:/etc/keepalived/keepalived.conf,
// met de vingerafdrukken van de afwijkingen die de beheerder zag.
type StepChoice struct {
	Step         string
	Fingerprints []string
}

// Plan is wat de taak gaat doen, in haar volgorde. Het venster toont het en
// de taak krijgt het als parameters.
type Plan struct {
	Mode        string     `json:"mode"`
	ClusterID   uuid.UUID  `json:"cluster_id"`
	Cluster     string     `json:"cluster"`
	Slug        string     `json:"slug"`
	Environment string     `json:"environment"`
	Template    string     `json:"template"`
	Version     string     `json:"version"`
	Revision    int        `json:"revision"`
	Nodes       []PlanNode `json:"nodes"`
	// Notes zeggen in gewone zinnen wat er hoogstens kan gebeuren.
	Notes []string `json:"notes"`
}

// NeedsConfirmation: op prod moet de beheerder de slug intikken.
func (p Plan) NeedsConfirmation() bool { return p.Environment == string(store.EnvironmentProd) }

// PlanNode is één node van de taak.
type PlanNode struct {
	NodeID   uuid.UUID  `json:"node_id"`
	Hostname string     `json:"hostname"`
	VIPs     []string   `json:"vips"`
	Steps    []PlanStep `json:"steps"`
	// Ignored zijn genegeerde stappen die blijven zoals ze zijn.
	Ignored []string `json:"ignored"`
	// Services zijn de services die het herstel herlaadt, herstart of
	// stopt.
	Services []string `json:"services"`
}

// PlanStep is één stap die opnieuw wordt toegepast.
type PlanStep struct {
	Step         string   `json:"step"`
	Title        string   `json:"title"`
	Action       string   `json:"action"`
	Fingerprints []string `json:"fingerprints"`
}

// minProtocol: herstel inspecteert eerst (state.inspect) en past dan toe.
const minProtocol = max(protocol.InspectSince, protocol.ApplySince)

// PlanRemediation controleert een keuze en zet haar om in een plan: per node
// de gekozen stappen in de volgorde van de template, de nodes zonder VIP
// eerst en de eigenaar als laatste. Er verandert nog niets.
func (s *Service) PlanRemediation(ctx context.Context, clusterID uuid.UUID, choices []Choice) (Plan, error) {
	c, err := s.q.GetCluster(ctx, clusterID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Plan{}, ErrNotFound
	}
	if err != nil {
		return Plan{}, err
	}
	notTemplate := &ConflictError{Code: "not_template",
		Msg: "herstellen kan alleen bij een cluster uit een template; van een baseline kent ClusterForge bij bestanden alleen een vingerafdruk"}
	if c.TemplateName == nil {
		return Plan{}, notTemplate
	}
	d, err := s.dep.Desired(ctx, clusterID)
	switch {
	case errors.Is(err, deploy.ErrNoSpec):
		return Plan{}, notTemplate
	case err != nil:
		return Plan{}, &ConflictError{Code: "no_desired_state", Msg: "de gewenste staat is niet te lezen, dus herstellen kan niet: " + err.Error()}
	}
	if len(d.Membership) > 0 {
		return Plan{}, &ConflictError{Code: "membership", Msg: "het lidmaatschap wijkt af van de specificatie (" + strings.Join(d.Membership, "; ") +
			"); een herstel zou de lijst met peers herschrijven en een node buitensluiten"}
	}
	if len(choices) == 0 {
		return Plan{}, &FieldError{Field: "nodes", Message: "kies minstens één afwijking om te herstellen"}
	}
	rep, err := s.drift.Report(ctx, clusterID, nil)
	if err != nil {
		return Plan{}, err
	}
	rows, err := s.q.ListDriftNodes(ctx, store.ListDriftNodesParams{ClusterID: &clusterID})
	if err != nil {
		return Plan{}, err
	}
	owned, err := s.ownedVIPs(ctx, clusterID)
	if err != nil {
		return Plan{}, err
	}

	byNode := map[uuid.UUID]PlanNode{}
	for _, ch := range choices {
		if _, dup := byNode[ch.NodeID]; dup {
			return Plan{}, &FieldError{Field: "nodes", Message: "elke node mag maar één keer in de keuze staan"}
		}
		i := slices.IndexFunc(rows, func(r store.ListDriftNodesRow) bool { return r.ID == ch.NodeID })
		if i < 0 || !d.Has(ch.NodeID) {
			return Plan{}, &FieldError{Field: "nodes", Message: "een gekozen node hoort niet bij de gewenste staat van dit cluster"}
		}
		n := rows[i]
		if err := nodeUsable(n); err != nil {
			return Plan{}, err
		}
		j := slices.IndexFunc(rep.Nodes, func(r drift.NodeReport) bool { return r.NodeID == n.ID })
		if j < 0 {
			return Plan{}, &ConflictError{Code: "drift_changed", Msg: n.Hostname + " is nog niet gecontroleerd; controleer eerst"}
		}
		nr := rep.Nodes[j]
		if nr.CheckedAt == nil || nr.SpecRevision != d.Revision {
			return Plan{}, &ConflictError{Code: "drift_changed",
				Msg: fmt.Sprintf("de laatste controle van %s hoort niet bij spec-revisie %d; controleer opnieuw", n.Hostname, d.Revision)}
		}
		steps, err := d.Render(n.ID)
		if err != nil {
			return Plan{}, &ConflictError{Code: "no_desired_state", Msg: fmt.Sprintf("de stappen van %s zijn niet te renderen: %v", n.Hostname, err)}
		}
		pn, err := planNode(n.Hostname, steps, nr.Findings, ch.Steps)
		if err != nil {
			return Plan{}, err
		}
		pn.NodeID, pn.VIPs = n.ID, owned[n.ID]
		byNode[n.ID] = pn
	}

	p := Plan{
		Mode: ModeRemediate, ClusterID: c.ID, Cluster: c.Name, Slug: c.Slug, Environment: string(c.Environment),
		Template: d.Spec.Template.Name, Version: d.Spec.Template.Version, Revision: d.Revision, Nodes: []PlanNode{},
	}
	order := make([]uuid.UUID, len(d.Spec.Nodes))
	for i, sn := range d.Spec.Nodes {
		order[i] = sn.NodeID
	}
	p.Nodes = Order(order, byNode)
	p.Notes = notes(p)
	return p, nil
}

// nodeUsable zegt of herstel nu op deze node kan.
func nodeUsable(n store.ListDriftNodesRow) error {
	switch {
	case n.Lifecycle != store.NodeLifecycleActive:
		return &ConflictError{Code: "node_not_active", Msg: n.Hostname + " is niet actief; herstel kan alleen op een actieve node"}
	case !n.HasAgent || n.AgentProtocol < minProtocol:
		return &ConflictError{Code: "agent_too_old", Msg: fmt.Sprintf("de agent op %s is te oud voor herstel; werk hem bij met install.sh --upgrade", n.Hostname)}
	case n.HeartbeatAt == nil || time.Since(*n.HeartbeatAt) > status.HeartbeatDown:
		return &ConflictError{Code: "agent_offline", Msg: fmt.Sprintf("de agent op %s is niet verbonden", n.Hostname)}
	}
	return nil
}

// ownedVIPs geeft per node de VIP's die hij volgens de laatste heartbeat
// heeft.
func (s *Service) ownedVIPs(ctx context.Context, clusterID uuid.UUID) (map[uuid.UUID][]string, error) {
	vips, err := s.q.ListVIPsByCluster(ctx, clusterID)
	if err != nil {
		return nil, err
	}
	out := map[uuid.UUID][]string{}
	for _, v := range vips {
		if v.Vip.OwnerNodeID != nil {
			out[*v.Vip.OwnerNodeID] = append(out[*v.Vip.OwnerNodeID], v.Vip.Address.String())
		}
	}
	return out, nil
}

// planNode zet de keuze voor één node om in stappen, in de volgorde van de
// template. findings zijn de afwijkingen van de laatste controle, met de
// negeerregels van nu.
func planNode(hostname string, steps []templates.Step, findings []drift.Finding, chosen []StepChoice) (PlanNode, error) {
	pn := PlanNode{Hostname: hostname, Steps: []PlanStep{}, Ignored: []string{}, Services: []string{}}
	if len(chosen) == 0 {
		return pn, &FieldError{Field: "nodes", Message: "kies op " + hostname + " minstens één afwijking"}
	}
	want := map[string][]string{}
	for _, sc := range chosen {
		if _, dup := want[sc.Step]; dup {
			return pn, &FieldError{Field: "nodes", Message: sc.Step + " staat twee keer in de keuze voor " + hostname}
		}
		var fps []string
		ignored := false
		for _, f := range findings {
			if f.Step != sc.Step {
				continue
			}
			if f.Ignored {
				ignored = true
				continue
			}
			fps = append(fps, f.Fingerprint)
		}
		switch {
		case ignored && len(fps) == 0:
			return pn, &ConflictError{Code: "ignored", Msg: fmt.Sprintf("%s op %s wordt genegeerd; hef de regel op als je het wilt herstellen", sc.Step, hostname)}
		case len(fps) == 0:
			return pn, &ConflictError{Code: "drift_changed", Msg: fmt.Sprintf("%s op %s wijkt niet (meer) af; controleer opnieuw", sc.Step, hostname)}
		case !sameSet(fps, sc.Fingerprints):
			return pn, &ConflictError{Code: "drift_changed", Msg: fmt.Sprintf("%s op %s is veranderd sinds je het bekeek; controleer opnieuw", sc.Step, hostname)}
		}
		want[sc.Step] = fps
	}
	for _, ts := range steps {
		for _, id := range drift.StepIDs(ts) {
			fps, ok := want[id]
			if !ok {
				continue
			}
			pn.Steps = append(pn.Steps, PlanStep{Step: id, Title: title(ts, id), Action: Action(ts, id), Fingerprints: sorted(fps)})
			delete(want, id)
			for _, svc := range touched(ts) {
				if !slices.Contains(pn.Services, svc) {
					pn.Services = append(pn.Services, svc)
				}
			}
		}
	}
	for id := range want {
		return pn, &FieldError{Field: "nodes", Message: id + " is geen stap van de template op " + hostname}
	}
	for _, f := range findings {
		if f.Ignored && !slices.Contains(pn.Ignored, f.Step) {
			pn.Ignored = append(pn.Ignored, f.Step)
		}
	}
	return pn, nil
}

// Order zet de nodes in de volgorde van de taak: eerst die zonder VIP, dan
// de eigenaars, elk in de volgorde van de spec.
func Order(spec []uuid.UUID, nodes map[uuid.UUID]PlanNode) []PlanNode {
	out := []PlanNode{}
	for _, owners := range []bool{false, true} {
		for _, id := range spec {
			if n, ok := nodes[id]; ok && (len(n.VIPs) > 0) == owners {
				out = append(out, n)
			}
		}
	}
	return out
}

func title(ts templates.Step, id string) string {
	if name, ok := strings.CutPrefix(id, "package:"); ok && ts.Package != nil {
		return "Pakket " + name
	}
	if ts.Title != "" {
		return ts.Title
	}
	return id
}

var serviceStates = map[string]string{"started": "starten", "stopped": "stoppen", "restarted": "herstarten", "reloaded": "herladen"}

// Action zegt in gewone woorden wat het opnieuw toepassen van een stap doet,
// met de services die daarna herladen of herstart worden. id kiest bij een
// pakketstap het pakket.
func Action(ts templates.Step, id string) string {
	var out string
	switch {
	case ts.Package != nil:
		name, _ := strings.CutPrefix(id, "package:")
		if ts.Package.State == "absent" {
			out = "pakket " + name + " verwijderen"
		} else {
			out = "pakket " + name + " installeren"
		}
	case ts.File != nil:
		out = "bestand " + ts.File.Path + " overschrijven"
	case ts.Directory != nil:
		out = "map " + ts.Directory.Path + " aanmaken en rechten zetten"
	case ts.Service != nil:
		var parts []string
		if e := ts.Service.Enabled; e != nil {
			parts = append(parts, map[bool]string{true: "enablen", false: "disablen"}[*e])
		}
		if st := serviceStates[ts.Service.State]; st != "" {
			parts = append(parts, st)
		}
		if len(parts) == 0 {
			parts = []string{"controleren"}
		}
		out = "service " + ts.Service.Name + " " + strings.Join(parts, " en ")
	case ts.User != nil:
		out = "gebruiker " + ts.User.Name + " aanmaken"
	case ts.Command != nil && ts.Command.Creates != "":
		out = "het commando opnieuw uitvoeren dat " + ts.Command.Creates + " maakt"
	case ts.Command != nil:
		out = "het commando opnieuw uitvoeren"
	default:
		out = id + " opnieuw toepassen"
	}
	var then []string
	for _, h := range ts.Notify {
		then = append(then, h.Name+" "+serviceStates[h.State])
	}
	if len(then) > 0 {
		out += ", daarna " + strings.Join(then, " en ")
	}
	return out
}

// touched geeft de services die het toepassen van een stap kan herladen,
// herstarten of stoppen.
func touched(ts templates.Step) []string {
	var out []string
	if ts.Service != nil && ts.Service.State != "" && ts.Service.State != "started" {
		out = append(out, ts.Service.Name)
	}
	for _, h := range ts.Notify {
		out = append(out, h.Name)
	}
	return out
}

// notes zeggen wat er hoogstens kan gebeuren.
func notes(p Plan) []string {
	out := []string{"De taak gaat node voor node. Na elke node wacht ze tot die node gezond is, elk VIP één houder heeft en de controles van de template slagen; lukt dat niet, dan stopt ze en blijven de nodes daarna ongemoeid."}
	for _, n := range p.Nodes {
		if len(n.VIPs) == 0 {
			continue
		}
		if slices.Contains(n.Services, "keepalived") {
			out = append(out, fmt.Sprintf("%s heeft nu %s en komt daarom als laatste. Daar wordt keepalived herladen of herstart, dus het VIP kan kort naar een andere node gaan; de taak gaat alleen door als een andere node het kan overnemen.",
				n.Hostname, strings.Join(n.VIPs, ", ")))
		}
	}
	var ignored []string
	for _, n := range p.Nodes {
		for _, id := range n.Ignored {
			ignored = append(ignored, id+" op "+n.Hostname)
		}
	}
	if len(ignored) > 0 {
		out = append(out, "Genegeerd en dus niet hersteld: "+strings.Join(ignored, ", ")+".")
	}
	return out
}

func sameSet(a, b []string) bool {
	return slices.Equal(sorted(a), sorted(b))
}

func sorted(xs []string) []string {
	out := slices.Clone(xs)
	slices.Sort(out)
	return slices.Compact(out)
}

// StartRemediation zet het plan in de wachtrij, door het clusterslot, met
// het event drift.remediation_requested in dezelfde transactie.
func (s *Service) StartRemediation(ctx context.Context, actor events.Actor, p Plan) (store.Job, error) {
	var j store.Job
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		var err error
		j, err = s.jobs.EnqueueForClusterTx(ctx, q, jobs.Spec{
			Kind: Kind, Title: "Drift herstellen in " + p.Cluster, Params: p, ClusterID: &p.ClusterID, Actor: actor,
		})
		if err != nil {
			return err
		}
		hosts := make([]string, len(p.Nodes))
		plan := make([]map[string]any, len(p.Nodes))
		steps := 0
		for i, n := range p.Nodes {
			ids := make([]string, len(n.Steps))
			for k, st := range n.Steps {
				ids[k] = st.Step
			}
			hosts[i], steps = n.Hostname, steps+len(ids)
			plan[i] = map[string]any{"hostname": n.Hostname, "steps": ids}
		}
		return s.ev.Write(ctx, q, events.Event{
			Actor: actor, SubjectType: "cluster", SubjectID: p.ClusterID.String(), ClusterID: &p.ClusterID,
			Action: "drift.remediation_requested", Payload: map[string]any{
				"name": p.Cluster, "job_id": j.ID, "environment": p.Environment, "nodes": hosts, "steps": steps, "plan": plan,
			},
		})
	})
	var busy jobs.BusyError
	if errors.As(err, &busy) {
		return store.Job{}, &ConflictError{Code: "busy", Msg: busy.Error()}
	}
	if err != nil {
		return store.Job{}, err
	}
	s.jobs.Kick()
	return j, nil
}
