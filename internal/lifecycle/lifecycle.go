// Package lifecycle voert acties op een node uit via zijn agent: facts
// verzamelen, herstarten, afsluiten en onderhoud. Elke actie is een taak met
// stappen. Onderhoud zet keepalived op de node uit, zodat de VIP's naar de
// andere nodes gaan voor er iets met de node gebeurt.
package lifecycle

import (
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
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/jobs"
	"github.com/Jonasz1996/clusterforge/internal/status"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// KindNodeAction is de soort taak voor acties op een node.
const KindNodeAction = "node.action"

// Acties op een node.
const (
	ActionRefreshFacts = "refresh_facts"
	ActionReboot       = "reboot"
	ActionShutdown     = "shutdown"
	ActionMaintenance  = "maintenance"
	ActionActivate     = "activate"
)

var (
	ErrNotFound = errors.New("niet gevonden")
)

// ValidationError is een fout in de vraag zelf.
type ValidationError struct{ Msg string }

func (e ValidationError) Error() string { return e.Msg }

// ConflictError betekent dat de actie nu niet kan.
type ConflictError struct{ Msg string }

func (e ConflictError) Error() string { return e.Msg }

// NeedsForceError betekent dat de actie een VIP onbereikbaar kan maken; met
// Force gaat ze toch door.
type NeedsForceError struct{ Msg string }

func (e NeedsForceError) Error() string { return e.Msg }

// Commander stuurt een commando naar de agent van een node; agentbus.Bus
// doet dat in het echt.
type Commander interface {
	Command(ctx context.Context, nodeID uuid.UUID, cmd protocol.Command) (protocol.Result, error)
}

type Service struct {
	pool *pgxpool.Pool
	q    *store.Queries
	ev   *events.Writer
	log  *slog.Logger
	jobs *jobs.Runner
	bus  Commander

	// Poll is hoe vaak een taak naar de toestand van de node kijkt.
	Poll time.Duration
	// MoveTimeout is hoe lang een taak wacht tot de VIP's verhuisd zijn.
	MoveTimeout time.Duration
	// BootTimeout is hoe lang een taak wacht tot een herstarte node terug is.
	BootTimeout time.Duration
	// OffTimeout is hoe lang een taak wacht tot een node uit is.
	OffTimeout time.Duration
	// OffAfter is hoe lang een node geen heartbeat stuurt voor hij als uit
	// geldt.
	OffAfter time.Duration
	// ReadyTimeout is hoe lang een taak na het onderhoud op een nieuwe
	// heartbeat wacht.
	ReadyTimeout time.Duration
	// PowerDelay is de pauze in seconden tussen het antwoord van de agent en
	// de herstart.
	PowerDelay int
	// Offline is de leeftijd van de laatste heartbeat waarop een agent als
	// offline geldt; Stale die waarop een andere node geen VIP meer kan
	// overnemen.
	Offline, Stale time.Duration
	// Changed wordt aangeroepen na een wijziging van de lifecycle, zodat de
	// status meteen opnieuw berekend wordt. Mag nil zijn.
	Changed func()
}

func NewService(pool *pgxpool.Pool, ev *events.Writer, log *slog.Logger, runner *jobs.Runner, bus Commander) *Service {
	s := &Service{
		pool: pool, q: store.New(pool), ev: ev, log: log, jobs: runner, bus: bus,
		Poll: 2 * time.Second, MoveTimeout: 2 * time.Minute, BootTimeout: 15 * time.Minute,
		OffTimeout: 10 * time.Minute, OffAfter: 3 * protocol.HeartbeatInterval, ReadyTimeout: 3 * time.Minute,
		PowerDelay: 3, Offline: status.HeartbeatDown, Stale: status.HeartbeatLate,
	}
	runner.Register(KindNodeAction, s.run)
	return s
}

// ActionRequest is wat een gebruiker vraagt.
type ActionRequest struct {
	Action string
	Reason string
	// Drain haalt bij een herstart of afsluiten eerst de VIP's weg; nil is ja.
	Drain *bool
	// Force gaat door ook als geen andere node een VIP kan overnemen.
	Force bool
}

// params gaan mee met de taak. Alles wat bepaalt welke stappen er zijn, ligt
// hier vast, zodat een hervatte taak dezelfde stappen doorloopt.
type params struct {
	Action   string `json:"action"`
	Hostname string `json:"hostname"`
	Reason   string `json:"reason,omitempty"`
	// UseAgent is false voor onderhoud aan een node zonder agent: dan
	// verandert alleen de lifecycle.
	UseAgent bool `json:"use_agent"`
	// Keepalived: de node heeft keepalived.
	Keepalived bool `json:"keepalived"`
	// Drain zet keepalived uit voor de rest gebeurt.
	Drain bool `json:"drain"`
	Force bool `json:"force,omitempty"`
	// VIPs zijn de adressen die de node had toen de taak gevraagd werd.
	VIPs []string `json:"vips,omitempty"`
	// Previous is de lifecycle van voor de taak; een herstart zet die terug.
	Previous store.NodeLifecycle `json:"previous"`
	// CommandID is de basis voor de id's van de commando's, zodat een
	// herhaald commando na een hervatting als hetzelfde herkend wordt.
	CommandID string `json:"command_id"`
}

var titles = map[string]string{
	ActionRefreshFacts: "Facts verzamelen", ActionReboot: "Herstarten", ActionShutdown: "Afsluiten",
	ActionMaintenance: "Onderhoud", ActionActivate: "Onderhoud beëindigen",
}

// RequestAction controleert de vraag en zet een taak in de wachtrij.
func (s *Service) RequestAction(ctx context.Context, actor events.Actor, nodeID uuid.UUID, req ActionRequest) (store.Job, error) {
	title, ok := titles[req.Action]
	if !ok {
		return store.Job{}, ValidationError{"onbekende actie " + req.Action}
	}
	reason := strings.TrimSpace(req.Reason)
	if len(reason) > 200 {
		return store.Job{}, ValidationError{"de reden is te lang (hoogstens 200 tekens)"}
	}
	rt, err := s.q.GetNodeRuntime(ctx, nodeID)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.Job{}, ErrNotFound
	}
	if err != nil {
		return store.Job{}, err
	}
	if busy, err := s.q.GetActiveNodeJob(ctx, store.GetActiveNodeJobParams{NodeID: &nodeID, Kind: KindNodeAction}); err == nil {
		return store.Job{}, ConflictError{"er loopt al een taak voor deze node: " + busy.Title}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return store.Job{}, err
	}

	services := map[string]string{}
	_ = json.Unmarshal(rt.Services, &services)
	_, keepalived := services["keepalived"]
	online := rt.HasAgent && rt.HeartbeatAt != nil && time.Since(*rt.HeartbeatAt) <= s.Offline
	agentErr := func() error {
		switch {
		case !rt.HasAgent:
			return ConflictError{"deze node heeft geen agent"}
		case !online:
			return ConflictError{"de agent van deze node is offline"}
		case rt.AgentProtocol < protocol.CommandsSince:
			return ConflictError{"de agent op deze node is te oud voor commando's; installeer hem opnieuw met het installatiescript"}
		}
		return nil
	}
	held, err := s.heldVIPs(ctx, rt.ClusterID, nodeID)
	if err != nil {
		return store.Job{}, err
	}
	p := params{
		Action: req.Action, Hostname: rt.Hostname, Reason: reason, Keepalived: keepalived, Force: req.Force,
		Previous: rt.Lifecycle, CommandID: uuid.NewString(),
	}
	switch req.Action {
	case ActionRefreshFacts:
		if err := agentErr(); err != nil {
			return store.Job{}, err
		}
		p.UseAgent = true
	case ActionMaintenance:
		switch rt.Lifecycle {
		case store.NodeLifecycleMaintenance:
			return store.Job{}, ConflictError{"deze node staat al in onderhoud"}
		case store.NodeLifecycleDecommissioned:
			return store.Job{}, ConflictError{"deze node is uit dienst"}
		}
		// Een node zonder agent, of met een agent die weg is, zet je alleen
		// op onderhoud. Een te oude agent zou de VIP's laten staan.
		if rt.HasAgent && online {
			if err := agentErr(); err != nil {
				return store.Job{}, err
			}
			p.UseAgent, p.Drain, p.VIPs = true, true, held
		}
	case ActionActivate:
		if rt.Lifecycle != store.NodeLifecycleMaintenance && rt.Lifecycle != store.NodeLifecycleDraining {
			return store.Job{}, ConflictError{"deze node staat niet in onderhoud"}
		}
		if rt.HasAgent {
			// Zonder agent blijft keepalived uit staan; eerst de node starten.
			if !online {
				return store.Job{}, ConflictError{"de agent van deze node is offline; start de node eerst, daarna kun je het onderhoud beëindigen"}
			}
			if err := agentErr(); err != nil {
				return store.Job{}, err
			}
			p.UseAgent = true
		}
	case ActionReboot, ActionShutdown:
		if err := agentErr(); err != nil {
			return store.Job{}, err
		}
		if rt.Lifecycle == store.NodeLifecycleDecommissioned {
			return store.Job{}, ConflictError{"deze node is uit dienst"}
		}
		p.UseAgent = true
		// In onderhoud staat keepalived al uit.
		if rt.Lifecycle != store.NodeLifecycleMaintenance && keepalived && (req.Drain == nil || *req.Drain) {
			p.Drain, p.VIPs = true, held
		}
	}
	if p.Drain && len(p.VIPs) > 0 && !req.Force {
		if err := s.checkTakeover(ctx, *rt.ClusterID, nodeID, p.VIPs); err != nil {
			return store.Job{}, err
		}
	}
	j, err := s.jobs.Enqueue(ctx, jobs.Spec{
		Kind: KindNodeAction, Title: title + ": " + rt.Hostname, Params: p,
		NodeID: &nodeID, ClusterID: rt.ClusterID, Actor: actor,
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.ConstraintName == "jobs_node_action_key" {
		return store.Job{}, ConflictError{"er loopt al een taak voor deze node"}
	}
	return j, err
}

// heldVIPs zijn de adressen van het cluster die deze node nu heeft.
func (s *Service) heldVIPs(ctx context.Context, clusterID *uuid.UUID, nodeID uuid.UUID) ([]string, error) {
	if clusterID == nil {
		return nil, nil
	}
	vips, err := s.q.ListVIPsByCluster(ctx, *clusterID)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, v := range vips {
		if v.Vip.OwnerNodeID != nil && *v.Vip.OwnerNodeID == nodeID {
			out = append(out, v.Vip.Address.String())
		}
	}
	return out, nil
}

// checkTakeover kijkt of een andere node de VIP's kan overnemen: actief,
// online en met keepalived dat draait.
func (s *Service) checkTakeover(ctx context.Context, clusterID, nodeID uuid.UUID, vips []string) error {
	peers, err := s.q.ListClusterPeers(ctx, store.ListClusterPeersParams{ClusterID: &clusterID, NodeID: nodeID})
	if err != nil {
		return err
	}
	for _, p := range peers {
		services := map[string]string{}
		_ = json.Unmarshal(p.Services, &services)
		if p.Lifecycle == store.NodeLifecycleActive && p.HeartbeatAt != nil &&
			time.Since(*p.HeartbeatAt) <= s.Stale && services["keepalived"] == "active" {
			return nil
		}
	}
	list := strings.Join(vips, ", ")
	return NeedsForceError{fmt.Sprintf(
		"geen andere node in het cluster kan %s overnemen: er is geen actieve, online node waarop keepalived draait. Ga je toch door, dan is %s onbereikbaar tot deze node terug is",
		list, list)}
}

func (s *Service) changed() {
	if s.Changed != nil {
		s.Changed()
	}
}

// run voert een node-actie uit.
func (s *Service) run(ctx context.Context, j *jobs.Job) error {
	var p params
	if err := j.Decode(&p); err != nil {
		return err
	}
	if j.NodeID == nil {
		return errors.New("taak zonder node")
	}
	r := &runCtx{s: s, j: j, p: p, node: *j.NodeID, actor: events.System()}
	if j.RequestedBy != nil {
		r.actor = events.User(*j.RequestedBy)
	}
	defer s.changed()
	switch p.Action {
	case ActionRefreshFacts:
		return j.Step(ctx, "Facts verzamelen", func(ctx context.Context, st *jobs.Step) error {
			_, err := r.command(ctx, st, "facts", protocol.CmdFactsCollect, 0)
			return err
		})
	case ActionMaintenance:
		if err := r.drain(ctx); err != nil {
			return err
		}
		return r.lifecycleStep(ctx, "Node in onderhoud zetten", store.NodeLifecycleMaintenance)
	case ActionActivate:
		if err := r.undrain(ctx); err != nil {
			return err
		}
		return r.lifecycleStep(ctx, "Node weer actief maken", store.NodeLifecycleActive)
	case ActionReboot, ActionShutdown:
		if err := r.drain(ctx); err != nil {
			return err
		}
		if p.Previous != store.NodeLifecycleMaintenance {
			if err := r.lifecycleStep(ctx, "Node in onderhoud zetten", store.NodeLifecycleMaintenance); err != nil {
				return err
			}
		}
		if p.Action == ActionShutdown {
			// De node blijft in onderhoud tot iemand hem weer start en het
			// onderhoud beëindigt.
			return r.powerOff(ctx)
		}
		if err := r.reboot(ctx); err != nil {
			return err
		}
		if p.Previous == store.NodeLifecycleMaintenance {
			return nil
		}
		if err := r.undrain(ctx); err != nil {
			return err
		}
		back := p.Previous
		if back == store.NodeLifecycleDraining {
			back = store.NodeLifecycleActive
		}
		return r.lifecycleStep(ctx, "Node weer actief maken", back)
	}
	return fmt.Errorf("onbekende actie %q", p.Action)
}

// runCtx is één uitvoering van een node-actie.
type runCtx struct {
	s     *Service
	j     *jobs.Job
	p     params
	node  uuid.UUID
	actor events.Actor
}

// command stuurt een commando en neemt de uitvoer van de agent over in de
// stap. Een agent die even weg is, krijgt nog twee kansen; het id blijft
// gelijk, dus de agent voert het niet dubbel uit.
func (r *runCtx) command(ctx context.Context, st *jobs.Step, suffix, action string, delay int) (protocol.Result, error) {
	cmd := protocol.Command{
		ID: r.p.CommandID + "-" + suffix, Action: action, Reason: r.p.Reason, DelaySeconds: delay,
	}
	var res protocol.Result
	var err error
	for attempt := range 3 {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return res, context.Cause(ctx)
			case <-time.After(5 * r.s.Poll):
			}
		}
		cmd.Deadline = time.Now().Add(2 * time.Minute)
		cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		res, err = r.s.bus.Command(cctx, r.node, cmd)
		cancel()
		if err == nil || ctx.Err() != nil {
			break
		}
		st.Logf("agent niet bereikt: %v", err)
		_ = st.Flush(ctx)
	}
	for _, line := range res.Output {
		st.Logf("%s", line)
	}
	_ = st.Flush(ctx)
	switch {
	case ctx.Err() != nil:
		return res, context.Cause(ctx)
	case err != nil:
		return res, err
	case !res.OK:
		return res, fmt.Errorf("de agent meldt: %s", res.Error)
	}
	return res, nil
}

// setLifecycle zet de lifecycle en schrijft een event als hij verandert.
func (r *runCtx) setLifecycle(ctx context.Context, to store.NodeLifecycle) error {
	err := pgx.BeginFunc(ctx, r.s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		row, err := q.SetNodeLifecycle(ctx, store.SetNodeLifecycleParams{NodeID: r.node, Lifecycle: to})
		if err != nil || row.Previous == to {
			return err
		}
		payload := map[string]any{"hostname": row.Hostname, "from": row.Previous, "to": to, "job_id": r.j.ID, "title": r.j.Title}
		if r.p.Reason != "" {
			payload["reason"] = r.p.Reason
		}
		return r.s.ev.Write(ctx, q, events.Event{
			Actor: r.actor, SubjectType: "node", SubjectID: r.node.String(), ClusterID: row.ClusterID,
			Action: "node.lifecycle_changed", Payload: payload,
		})
	})
	if err == nil {
		r.s.changed()
	}
	return err
}

func (r *runCtx) lifecycleStep(ctx context.Context, name string, to store.NodeLifecycle) error {
	return r.j.Step(ctx, name, func(ctx context.Context, st *jobs.Step) error {
		if err := r.setLifecycle(ctx, to); err != nil {
			return err
		}
		switch to {
		case store.NodeLifecycleMaintenance:
			st.Logf("%s telt niet mee voor de status van het cluster tot het onderhoud voorbij is", r.p.Hostname)
		case store.NodeLifecycleActive:
			st.Logf("%s telt weer mee voor de status van het cluster", r.p.Hostname)
		}
		return nil
	})
}

// drain zet keepalived uit en wacht tot de VIP's bij een andere node staan.
// Lukt dat niet, dan gaat keepalived weer aan en blijft de node actief.
func (r *runCtx) drain(ctx context.Context) error {
	if !r.p.UseAgent || !r.p.Drain {
		return nil
	}
	name := "Agent in onderhoud zetten"
	switch {
	case len(r.p.VIPs) > 0:
		name = "VIP's naar een andere node verhuizen"
	case r.p.Keepalived:
		name = "Keepalived uitzetten"
	}
	return r.j.Step(ctx, name, func(ctx context.Context, st *jobs.Step) (err error) {
		if r.p.Previous == store.NodeLifecycleActive {
			if err := r.setLifecycle(ctx, store.NodeLifecycleDraining); err != nil {
				return err
			}
		}
		defer func() {
			// Stopt de server, dan gaat de taak later verder; niets terugdraaien.
			if err != nil && !jobs.Interrupted(ctx) {
				r.rollback(ctx, st)
			}
		}()
		sent := time.Now()
		if _, err := r.command(ctx, st, "enter", protocol.CmdMaintenanceEnter, 0); err != nil {
			return err
		}
		if len(r.p.VIPs) == 0 {
			return nil
		}
		st.Logf("wachten tot %s bij een andere node staat", strings.Join(r.p.VIPs, ", "))
		_ = st.Flush(ctx)
		return r.waitMoved(ctx, st, sent)
	})
}

// rollback draait een mislukte drain terug: keepalived weer aan, de node
// weer zoals hij was.
func (r *runCtx) rollback(ctx context.Context, st *jobs.Step) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
	defer cancel()
	st.Logf("terugdraaien: keepalived weer aanzetten")
	if _, err := r.command(ctx, st, "rollback", protocol.CmdMaintenanceExit, 0); err != nil {
		st.Logf("terugdraaien mislukt: %v", err)
	}
	if err := r.setLifecycle(ctx, r.p.Previous); err != nil {
		st.Logf("lifecycle terugzetten mislukt: %v", err)
	}
	_ = st.Flush(ctx)
}

var errMoveTimeout = errors.New("de VIP's zijn niet op tijd verhuisd")

// waitMoved wacht tot de node geen VIP meer heeft en, zonder Force, elke VIP
// die hij had een andere eigenaar heeft.
func (r *runCtx) waitMoved(ctx context.Context, st *jobs.Step, sent time.Time) error {
	ctx, cancel := context.WithTimeoutCause(ctx, r.s.MoveTimeout, errMoveTimeout)
	defer cancel()
	return r.poll(ctx, func(ctx context.Context) (bool, error) {
		rt, err := r.s.q.GetNodeRuntime(ctx, r.node)
		if err != nil {
			return false, err
		}
		// Alleen een heartbeat van na het commando telt.
		if rt.HeartbeatAt == nil || !rt.HeartbeatAt.After(sent) {
			return false, nil
		}
		for _, a := range r.p.VIPs {
			if slices.Contains(rt.Addresses, a) {
				return false, nil
			}
		}
		if r.p.Force || rt.ClusterID == nil {
			st.Logf("deze node heeft geen VIP meer")
			return true, nil
		}
		vips, err := r.s.q.ListVIPsByCluster(ctx, *rt.ClusterID)
		if err != nil {
			return false, err
		}
		var moved []string
		for _, v := range vips {
			if !slices.Contains(r.p.VIPs, v.Vip.Address.String()) {
				continue
			}
			if v.Vip.OwnerNodeID == nil || *v.Vip.OwnerNodeID == r.node || v.OwnerHostname == nil {
				return false, nil
			}
			moved = append(moved, v.Vip.Address.String()+" staat nu op "+*v.OwnerHostname)
		}
		for _, m := range moved {
			st.Logf("%s", m)
		}
		return true, nil
	})
}

// undrain zet keepalived terug en wacht op een verse heartbeat, zodat de
// status van de node klopt voor hij weer meetelt.
func (r *runCtx) undrain(ctx context.Context) error {
	if !r.p.UseAgent {
		return nil
	}
	return r.j.Step(ctx, "Keepalived weer aanzetten", func(ctx context.Context, st *jobs.Step) error {
		sent := time.Now()
		if _, err := r.command(ctx, st, "exit", protocol.CmdMaintenanceExit, 0); err != nil {
			return err
		}
		ctx, cancel := context.WithTimeoutCause(ctx, r.s.ReadyTimeout, errors.New("de node stuurt geen heartbeat meer"))
		defer cancel()
		return r.poll(ctx, func(ctx context.Context) (bool, error) {
			rt, err := r.s.q.GetNodeRuntime(ctx, r.node)
			if err != nil || rt.HeartbeatAt == nil || !rt.HeartbeatAt.After(sent) {
				return false, err
			}
			services := map[string]string{}
			_ = json.Unmarshal(rt.Services, &services)
			if state, ok := services["keepalived"]; ok {
				if state == "failed" {
					return false, errors.New("keepalived start niet; kijk op de node met journalctl -u keepalived")
				}
				st.Logf("keepalived: %s", state)
			}
			return true, nil
		})
	})
}

var (
	errBootTimeout = errors.New("de node is niet op tijd terug; kijk op de console of in Proxmox")
	errOffTimeout  = errors.New("de node stuurt nog steeds heartbeats; hij is niet uitgegaan")
)

// rebootState bewaart wanneer het commando de deur uitging; een hervatte
// taak stuurt het dan niet opnieuw maar wacht verder.
type rebootState struct {
	SentAt time.Time `json:"sent_at"`
}

func (r *runCtx) sendPower(ctx context.Context, st *jobs.Step, action string) (time.Time, error) {
	var state rebootState
	if st.State(&state) && !state.SentAt.IsZero() {
		return state.SentAt, nil
	}
	sent := time.Now()
	if _, err := r.command(ctx, st, "power", action, r.s.PowerDelay); err != nil {
		return sent, err
	}
	return sent, st.SetState(ctx, rebootState{SentAt: sent})
}

// reboot herstart de node en wacht tot hij terug is. Een node is terug als
// hij een heartbeat stuurt met een uptime die na het commando begint.
func (r *runCtx) reboot(ctx context.Context) error {
	return r.j.Step(ctx, "Herstarten en wachten tot de node terug is", func(ctx context.Context, st *jobs.Step) error {
		sent, err := r.sendPower(ctx, st, protocol.CmdReboot)
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeoutCause(ctx, r.s.BootTimeout, errBootTimeout)
		defer cancel()
		wentAway := false
		return r.poll(ctx, func(ctx context.Context) (bool, error) {
			rt, err := r.s.q.GetNodeRuntime(ctx, r.node)
			if err != nil || rt.HeartbeatAt == nil {
				return false, err
			}
			if !wentAway && time.Since(*rt.HeartbeatAt) > 2*protocol.HeartbeatInterval {
				wentAway = true
				st.Logf("de node is offline")
				_ = st.Flush(ctx)
			}
			boot := rt.HeartbeatAt.Add(-time.Duration(rt.UptimeSeconds) * time.Second)
			// Een seconde speling: de uptime is afgerond.
			if !boot.After(sent.Add(-time.Second)) {
				return false, nil
			}
			st.Logf("de node is terug na %s", seconds(time.Since(sent)))
			return true, nil
		})
	})
}

// powerOff stuurt de node uit en wacht tot de heartbeats stoppen.
func (r *runCtx) powerOff(ctx context.Context) error {
	return r.j.Step(ctx, "Afsluiten en wachten tot de node uit is", func(ctx context.Context, st *jobs.Step) error {
		sent, err := r.sendPower(ctx, st, protocol.CmdShutdown)
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeoutCause(ctx, r.s.OffTimeout, errOffTimeout)
		defer cancel()
		return r.poll(ctx, func(ctx context.Context) (bool, error) {
			rt, err := r.s.q.GetNodeRuntime(ctx, r.node)
			if err != nil {
				return false, err
			}
			last := sent
			if rt.HeartbeatAt != nil && rt.HeartbeatAt.After(last) {
				last = *rt.HeartbeatAt
			}
			if time.Since(last) < r.s.OffAfter {
				return false, nil
			}
			st.Logf("de node is uit; start hem in Proxmox of op de machine zelf en beëindig daarna het onderhoud")
			return true, nil
		})
	})
}

// seconds schrijft een duur als "42 s" of "3 min 5 s".
func seconds(d time.Duration) string {
	s := int(d.Round(time.Second).Seconds())
	if s < 120 {
		return fmt.Sprintf("%d s", s)
	}
	return fmt.Sprintf("%d min %d s", s/60, s%60)
}

// poll roept check aan tot die true geeft, een fout geeft of ctx afloopt.
func (r *runCtx) poll(ctx context.Context, check func(context.Context) (bool, error)) error {
	t := time.NewTicker(r.s.Poll)
	defer t.Stop()
	for {
		ok, err := check(ctx)
		if err != nil && ctx.Err() == nil {
			return err
		}
		if ok {
			return nil
		}
		select {
		case <-ctx.Done():
			return context.Cause(ctx)
		case <-t.C:
		}
	}
}
