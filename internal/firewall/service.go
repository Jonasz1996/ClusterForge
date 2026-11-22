package firewall

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/jobs"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// Kind is de taak die het plan op een node toepast.
const Kind = "firewall.apply"

// BlockedRetention is hoe lang het geblokkeerde verkeer bewaard blijft.
const BlockedRetention = 30 * 24 * time.Hour

// RetryFailed is hoe lang een mislukte automatische toepassing wacht.
const RetryFailed = time.Hour

// Bus stuurt firewallverzoeken naar een agent.
type Bus interface {
	Firewall(ctx context.Context, nodeID uuid.UUID, req protocol.FirewallRequest) (protocol.FirewallResponse, error)
}

// Service beheert ufw op de nodes.
type Service struct {
	pool   *pgxpool.Pool
	ev     *events.Writer
	runner *jobs.Runner
	bus    Bus
	log    *slog.Logger
	// BlockedEvery is hoe vaak het geblokkeerde verkeer opgehaald wordt;
	// StatusEvery hoe vaak de stand.
	BlockedEvery time.Duration
	StatusEvery  time.Duration
	// CheckDelay is hoe lang de taak na het toepassen wacht vóór de
	// controle.
	CheckDelay time.Duration
	Now        func() time.Time

	tick      sync.Mutex
	lastPrune time.Time
}

func NewService(pool *pgxpool.Pool, ev *events.Writer, runner *jobs.Runner, bus Bus, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	s := &Service{pool: pool, ev: ev, runner: runner, bus: bus, log: log,
		BlockedEvery: time.Minute, StatusEvery: 5 * time.Minute, CheckDelay: 3 * time.Second, Now: time.Now}
	if runner != nil {
		runner.Register(Kind, s.apply)
	}
	return s
}

// FieldError: een ongeldige invoer.
type FieldError struct{ Msg string }

func (e *FieldError) Error() string { return e.Msg }

func (s *Service) event(ctx context.Context, actor events.Actor, n *Node, action string, payload map[string]any) {
	if s.ev == nil {
		return
	}
	e := events.Event{Actor: actor, SubjectType: "firewall", SubjectID: "settings", Action: action, Payload: payload}
	if n != nil {
		e.SubjectType, e.SubjectID, e.ClusterID = "node", n.NodeID.String(), n.ClusterID
		if e.Payload == nil {
			e.Payload = map[string]any{}
		}
		e.Payload["hostname"] = n.Hostname
	}
	if err := s.ev.Write(ctx, nil, e); err != nil {
		s.log.Warn("event schrijven mislukt", "action", action, "err", err)
	}
}

func cleanList(in []string) []string {
	out := []string{}
	for _, v := range in {
		if v = strings.TrimSpace(v); v != "" && !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	return out
}

func validNets(list []string) error {
	for _, v := range list {
		if _, err := netip.ParsePrefix(v); err == nil {
			continue
		}
		if _, err := netip.ParseAddr(v); err == nil {
			continue
		}
		return fmt.Errorf("ongeldig netwerk %q", v)
	}
	return nil
}

// UpdateSettings bewaart de instellingen.
func (s *Service) UpdateSettings(ctx context.Context, actor events.Actor, userID *uuid.UUID, in Settings) (Settings, error) {
	in.LAN, in.Admin = cleanList(in.LAN), cleanList(in.Admin)
	switch {
	case len(in.LAN) == 0:
		return Settings{}, &FieldError{"geef minstens één eigen netwerk"}
	case validNets(in.LAN) != nil:
		return Settings{}, &FieldError{validNets(in.LAN).Error()}
	case validNets(in.Admin) != nil:
		return Settings{}, &FieldError{validNets(in.Admin).Error()}
	case !slices.Contains([]string{ClassLAN, ClassAdmin, ClassAny}, in.SSHFrom):
		return Settings{}, &FieldError{"SSH vanaf is lan, admin of any"}
	case in.UnknownPorts != ClassLAN && in.UnknownPorts != "closed":
		return Settings{}, &FieldError{"onbekende poorten is lan of closed"}
	case in.ConfirmSeconds < protocol.MinFirewallConfirm || in.ConfirmSeconds > protocol.MaxFirewallConfirm:
		return Settings{}, &FieldError{fmt.Sprintf("wachttijd tussen %d en %d seconden", protocol.MinFirewallConfirm, protocol.MaxFirewallConfirm)}
	case in.GraceHours < 0 || in.GraceHours > 720:
		return Settings{}, &FieldError{"poorten onthouden: 0 tot 720 uur"}
	case len(in.LAN)+len(in.Admin) > 100:
		return Settings{}, &FieldError{"hoogstens 100 netwerken"}
	}
	if _, err := s.pool.Exec(ctx, `UPDATE firewall_settings SET auto_apply = $1, lan = $2, admin = $3, ssh_from = $4, unknown_ports = $5,
		strict = $6, confirm_seconds = $7, grace_hours = $8, monitor = $9, updated_at = now(), updated_by = $10`,
		in.AutoApply, in.LAN, in.Admin, in.SSHFrom, in.UnknownPorts, in.Strict, in.ConfirmSeconds, in.GraceHours, in.Monitor, userID); err != nil {
		return Settings{}, err
	}
	s.event(ctx, actor, nil, "firewall.settings_changed", map[string]any{
		"auto_apply": in.AutoApply, "lan": in.LAN, "admin": in.Admin, "ssh_from": in.SSHFrom, "unknown_ports": in.UnknownPorts,
		"strict": in.Strict, "confirm_seconds": in.ConfirmSeconds, "grace_hours": in.GraceHours, "monitor": in.Monitor,
	})
	return s.Settings(ctx)
}

// SetMode zet de stand van nodes: off, preview of managed. Het geeft de
// nodes terug die overgeslagen zijn.
func (s *Service) SetMode(ctx context.Context, actor events.Actor, ids []uuid.UUID, mode string) ([]string, error) {
	if mode != "off" && mode != "preview" && mode != "managed" {
		return nil, &FieldError{"stand is off, preview of managed"}
	}
	var skipped []string
	for _, id := range ids {
		n, err := s.Node(ctx, id)
		if err != nil {
			return nil, err
		}
		if mode != "off" && !n.Supported {
			skipped = append(skipped, n.Hostname+": agent te oud (protocol 30 nodig; Nodes → Agents bijwerken)")
			continue
		}
		if n.Mode == mode {
			continue
		}
		if _, err := s.pool.Exec(ctx, `INSERT INTO firewall_nodes (node_id, mode) VALUES ($1, $2)
			ON CONFLICT (node_id) DO UPDATE SET mode = $2, apply_failed_at = NULL`, id, mode); err != nil {
			return nil, err
		}
		s.event(ctx, actor, &n, "firewall.mode_changed", map[string]any{"mode": mode, "previous": n.Mode})
	}
	return skipped, nil
}

func (s *Service) request(ctx context.Context, id uuid.UUID, req protocol.FirewallRequest) (protocol.FirewallResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, protocol.FirewallRequestTimeout)
	defer cancel()
	res, err := s.bus.Firewall(ctx, id, req)
	if err == nil && res.Error != "" {
		err = errors.New(res.Error)
	}
	return res, err
}

// status haalt de stand van een node, onthoudt de poorten en rekent het
// plan opnieuw uit.
func (s *Service) status(ctx context.Context, n Node) (Node, error) {
	res, err := s.request(ctx, n.NodeID, protocol.FirewallRequest{Op: protocol.FirewallOpStatus})
	if err != nil {
		_, _ = s.pool.Exec(ctx, `UPDATE firewall_nodes SET status_error = $2 WHERE node_id = $1`, n.NodeID, err.Error())
		return n, err
	}
	if res.Status == nil {
		return n, errors.New("leeg antwoord van de agent")
	}
	st := res.Status
	b, err := json.Marshal(st)
	if err != nil {
		return n, err
	}
	applied := n.AppliedHash
	switch {
	case st.Applied != nil && st.Pending == nil:
		applied = st.Applied.Hash
	case !st.Active:
		applied = ""
	}
	if _, err := s.pool.Exec(ctx, `UPDATE firewall_nodes SET status = $2, status_at = now(), status_error = '', applied_hash = $3
		WHERE node_id = $1`, n.NodeID, b, applied); err != nil {
		return n, err
	}
	if err := s.rememberPorts(ctx, n.NodeID, st.Listeners); err != nil {
		return n, err
	}
	if n, err = s.Node(ctx, n.NodeID); err != nil {
		return n, err
	}
	return s.refreshPlan(ctx, n)
}

// rememberPorts bewaart de luisterende poorten. Een poort op een echt adres
// gaat voor een poort op loopback.
func (s *Service) rememberPorts(ctx context.Context, id uuid.UUID, ls []protocol.FirewallListener) error {
	best := map[string]protocol.FirewallListener{}
	var keys []string
	for _, l := range ls {
		k := l.Proto + "/" + strconv.Itoa(l.Port)
		cur, ok := best[k]
		if !ok {
			keys = append(keys, k)
		}
		if !ok || cur.Local() && !l.Local() {
			best[k] = l
		}
	}
	batch := &pgxBatch{}
	for _, k := range keys {
		l := best[k]
		batch.add(`INSERT INTO firewall_ports (node_id, proto, port, addr, process, unit, container, image) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
			ON CONFLICT (node_id, proto, port) DO UPDATE SET addr = $4, process = $5, unit = $6, container = $7, image = $8, last_seen = now()`,
			id, l.Proto, l.Port, l.Addr, l.Process, l.Unit, l.Container, l.Image)
	}
	return batch.run(ctx, s.pool)
}

// input verzamelt alles voor het plan van een node.
func (s *Service) input(ctx context.Context, n Node, set Settings) (Input, error) {
	in := Input{Template: n.Template, Status: n.Status, Units: n.units(), Settings: set.plan()}
	if f := n.facts; f != nil && f.Keepalived != nil {
		in.Keepalived = true
		for _, inst := range f.Keepalived.Instances {
			for _, p := range inst.Peers {
				if !slices.Contains(in.Unicast, p) {
					in.Unicast = append(in.Unicast, p)
				}
			}
		}
	}
	rows, err := s.pool.Query(ctx, `SELECT proto, port, addr, process, unit, container, image, last_seen FROM firewall_ports
		WHERE node_id = $1 AND last_seen > $2 ORDER BY port, proto`, n.NodeID, s.Now().Add(-time.Duration(set.GraceHours)*time.Hour-10*time.Minute))
	if err != nil {
		return in, err
	}
	var statusAt time.Time
	if n.StatusAt != nil {
		statusAt = *n.StatusAt
	}
	for rows.Next() {
		var p Port
		var last time.Time
		if err := rows.Scan(&p.Proto, &p.Port, &p.Addr, &p.Process, &p.Unit, &p.Container, &p.Image, &last); err != nil {
			rows.Close()
			return in, err
		}
		p.Listening = !last.Before(statusAt.Add(-time.Minute))
		in.Ports = append(in.Ports, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return in, err
	}
	if n.ClusterID != nil {
		peers, err := s.nodes(ctx, nodesSQL+` WHERE n.cluster_id = $1 AND n.id <> $2`, *n.ClusterID, n.NodeID)
		if err != nil {
			return in, err
		}
		// Ook nodes zonder agent horen bij het cluster.
		extra, err := s.pool.Query(ctx, `SELECT host(primary_ip) FROM nodes WHERE cluster_id = $1 AND id <> $2 AND primary_ip IS NOT NULL`, *n.ClusterID, n.NodeID)
		if err != nil {
			return in, err
		}
		for extra.Next() {
			var ip string
			if err := extra.Scan(&ip); err == nil && !slices.Contains(in.Peers, ip) {
				in.Peers = append(in.Peers, ip)
			}
		}
		extra.Close()
		for _, p := range peers {
			for _, a := range p.addresses() {
				if !slices.Contains(in.Peers, a) {
					in.Peers = append(in.Peers, a)
				}
			}
		}
		vips, err := s.pool.Query(ctx, `SELECT host(address) FROM vips WHERE cluster_id = $1 ORDER BY address`, *n.ClusterID)
		if err != nil {
			return in, err
		}
		for vips.Next() {
			var v string
			if err := vips.Scan(&v); err == nil {
				in.VIPs = append(in.VIPs, v)
			}
		}
		vips.Close()
	}
	custom, err := s.Rules(ctx)
	if err != nil {
		return in, err
	}
	for _, r := range custom {
		if r.NodeID != nil && *r.NodeID == n.NodeID || r.ClusterID != nil && n.ClusterID != nil && *r.ClusterID == *n.ClusterID ||
			r.NodeID == nil && r.ClusterID == nil {
			in.Custom = append(in.Custom, Custom{ID: r.ID.String(), Action: r.Action, Proto: r.Proto, Port: r.Port, Source: r.Source, Comment: r.Comment})
		}
	}
	return in, nil
}

// refreshPlan rekent het plan van een node opnieuw uit.
func (s *Service) refreshPlan(ctx context.Context, n Node) (Node, error) {
	if n.Status == nil {
		return n, nil
	}
	set, err := s.Settings(ctx)
	if err != nil {
		return n, err
	}
	in, err := s.input(ctx, n, set)
	if err != nil {
		return n, err
	}
	p := Build(in)
	b, err := json.Marshal(p)
	if err != nil {
		return n, err
	}
	if _, err := s.pool.Exec(ctx, `UPDATE firewall_nodes SET plan = $2, plan_hash = $3 WHERE node_id = $1`, n.NodeID, b, p.Hash); err != nil {
		return n, err
	}
	return s.Node(ctx, n.NodeID)
}

// Refresh haalt de stand van een node meteen op en rekent het plan uit:
// het voorbeeld.
func (s *Service) Refresh(ctx context.Context, id uuid.UUID) (Node, error) {
	n, err := s.Node(ctx, id)
	if err != nil {
		return Node{}, err
	}
	switch {
	case !n.Supported:
		return Node{}, &FieldError{n.Hostname + ": agent te oud (protocol 30 nodig; Nodes → Agents bijwerken)"}
	case !n.Online:
		return Node{}, &FieldError{n.Hostname + " is offline"}
	case !n.Enabled():
		return Node{}, &FieldError{"zet de firewall eerst aan op " + n.Hostname + " (stand voorbeeld of beheerd)"}
	}
	n, err = s.status(ctx, n)
	if err != nil {
		return Node{}, &FieldError{n.Hostname + ": " + err.Error()}
	}
	return n, nil
}

type applyParams struct {
	NodeID   uuid.UUID `json:"node_id"`
	Hostname string    `json:"hostname"`
}

// Apply start de taak die het plan toepast.
func (s *Service) Apply(ctx context.Context, actor events.Actor, id uuid.UUID) (uuid.UUID, error) {
	n, err := s.Node(ctx, id)
	if err != nil {
		return uuid.Nil, err
	}
	switch {
	case !n.Supported:
		return uuid.Nil, &FieldError{n.Hostname + ": agent te oud (protocol 30 nodig; Nodes → Agents bijwerken)"}
	case !n.Online:
		return uuid.Nil, &FieldError{n.Hostname + " is offline"}
	case !n.Enabled():
		return uuid.Nil, &FieldError{"zet de firewall eerst aan op " + n.Hostname}
	case n.ApplyRunning:
		return uuid.Nil, &FieldError{"op " + n.Hostname + " loopt al een firewalltaak"}
	}
	j, err := s.runner.Enqueue(ctx, jobs.Spec{Kind: Kind, Title: "Firewall toepassen op " + n.Hostname, NodeID: &n.NodeID, ClusterID: n.ClusterID,
		Actor: actor, Params: applyParams{NodeID: n.NodeID, Hostname: n.Hostname}})
	if err != nil {
		return uuid.Nil, err
	}
	if _, err := s.pool.Exec(ctx, `UPDATE firewall_nodes SET apply_job_id = $2 WHERE node_id = $1`, n.NodeID, j.ID); err != nil {
		return uuid.Nil, err
	}
	s.runner.Kick()
	return j.ID, nil
}

type logger interface {
	Logf(format string, args ...any)
}

func logDiff(st logger, d protocol.FirewallDiff) {
	for _, r := range d.Add {
		st.Logf("+ %s", r)
	}
	for _, r := range d.Adopt {
		st.Logf("~ %s (overnemen)", r)
	}
	for _, r := range d.Remove {
		st.Logf("- %s", r)
	}
	st.Logf("%d erbij, %d overgenomen, %d weg, %d blijven, %d vreemde blijven", len(d.Add), len(d.Adopt), len(d.Remove), len(d.Keep), len(d.Foreign))
}

func (s *Service) apply(ctx context.Context, j *jobs.Job) error {
	var p applyParams
	if err := j.Decode(&p); err != nil {
		return err
	}
	actor := events.System()
	if j.RequestedBy != nil {
		actor = events.User(*j.RequestedBy)
	}
	set, err := s.Settings(ctx)
	if err != nil {
		return err
	}
	var n Node
	var plan Plan
	err = j.Step(ctx, "voorbeeld", func(ctx context.Context, st *jobs.Step) error {
		n, err = s.Node(ctx, p.NodeID)
		if err != nil {
			return err
		}
		if n, err = s.status(ctx, n); err != nil {
			return err
		}
		if n.Plan == nil || n.Status == nil {
			return errors.New("geen plan")
		}
		plan = *n.Plan
		if !n.Status.Installed {
			st.Logf("ufw staat niet op %s en wordt geïnstalleerd", p.Hostname)
		} else if !n.Status.Active {
			st.Logf("ufw staat uit en gaat aan (standaard: inkomend dicht, uitgaand open)")
		}
		logDiff(st, protocol.DiffFirewall(n.Status.Rules, plan.Rules, plan.Strict))
		for _, w := range plan.Warnings {
			st.Logf("let op: %s", w)
		}
		return st.SetState(ctx, plan)
	})
	if err != nil {
		return s.failed(ctx, actor, j, n, p, err)
	}
	// Na een herstart van de server slaat Step een gelukte stap over.
	if plan.Hash == "" {
		if !j.Done("voorbeeld", &plan) {
			return s.failed(ctx, actor, j, n, p, errors.New("het plan van de vorige poging is weg"))
		}
		if n, err = s.Node(ctx, p.NodeID); err != nil {
			return err
		}
	}
	applyID := j.ID.String()
	var res protocol.FirewallApplyResult
	err = j.Step(ctx, p.Hostname+": toepassen", func(ctx context.Context, st *jobs.Step) error {
		r, err := s.request(ctx, p.NodeID, protocol.FirewallRequest{Op: protocol.FirewallOpApply, Apply: &protocol.FirewallApply{
			ID: applyID, Hash: plan.Hash, Rules: plan.Rules, Strict: plan.Strict, Install: true,
			ConfirmSeconds: set.ConfirmSeconds, Monitor: set.Monitor,
		}})
		if r.Apply != nil {
			res = *r.Apply
			for _, l := range r.Apply.Log {
				st.Logf("%s", l)
			}
		}
		if err != nil {
			return err
		}
		return st.SetState(ctx, res)
	})
	if err != nil {
		return s.failed(ctx, actor, j, n, p, err)
	}
	if res.ApplyID == "" {
		_ = j.Done(p.Hostname+": toepassen", &res)
	}
	if res.Changed {
		err = j.Step(ctx, "controle", func(ctx context.Context, st *jobs.Step) error {
			return s.check(ctx, st, n, plan)
		})
		if err != nil {
			bg := context.WithoutCancel(ctx)
			if _, rerr := s.request(bg, p.NodeID, protocol.FirewallRequest{Op: protocol.FirewallOpRollback, ApplyID: applyID}); rerr != nil {
				err = fmt.Errorf("%w; terugdraaien vragen mislukte (%v): de node doet het zelf binnen %d s", err, rerr, set.ConfirmSeconds)
			} else {
				err = fmt.Errorf("%w; de vorige regels staan terug", err)
			}
			return s.failed(ctx, actor, j, n, p, err)
		}
		err = j.Step(ctx, "bevestigen", func(ctx context.Context, st *jobs.Step) error {
			if _, err := s.request(ctx, p.NodeID, protocol.FirewallRequest{Op: protocol.FirewallOpConfirm, ApplyID: applyID}); err != nil {
				return fmt.Errorf("bevestigen mislukt (%w); de node zet de vorige regels vanzelf terug", err)
			}
			st.Logf("bevestigd; de terugdraaitimer is gestopt")
			return nil
		})
		if err != nil {
			return s.failed(ctx, actor, j, n, p, err)
		}
	}
	bg := context.WithoutCancel(ctx)
	_, _ = s.pool.Exec(bg, `UPDATE firewall_nodes SET applied_hash = $2, applied_at = now(), apply_error = '', apply_failed_at = NULL, status_at = NULL
		WHERE node_id = $1`, p.NodeID, plan.Hash)
	s.event(bg, actor, &n, "firewall.applied", map[string]any{"job_id": j.ID.String(), "plan": plan.Hash, "changed": res.Changed,
		"added": len(res.Diff.Add), "removed": len(res.Diff.Remove)})
	if fresh, err := s.Node(bg, p.NodeID); err == nil {
		_, _ = s.status(bg, fresh)
	}
	return nil
}

func (s *Service) failed(ctx context.Context, actor events.Actor, j *jobs.Job, n Node, p applyParams, err error) error {
	bg := context.WithoutCancel(ctx)
	_, _ = s.pool.Exec(bg, `UPDATE firewall_nodes SET apply_error = $2, apply_failed_at = now(), status_at = NULL WHERE node_id = $1`, p.NodeID, err.Error())
	if n.NodeID == uuid.Nil {
		n = Node{NodeID: p.NodeID, Hostname: p.Hostname}
	}
	s.event(bg, actor, &n, "firewall.apply_failed", map[string]any{"job_id": j.ID.String(), "error": err.Error()})
	return err
}

// check controleert een node na het toepassen: de agent antwoordt nog en
// de andere clusterleden komen op de clusterpoorten die luisteren.
func (s *Service) check(ctx context.Context, st *jobs.Step, n Node, plan Plan) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(s.CheckDelay):
	}
	res, err := s.request(ctx, n.NodeID, protocol.FirewallRequest{Op: protocol.FirewallOpStatus})
	if err != nil {
		return fmt.Errorf("de agent antwoordt niet meer na het toepassen: %w", err)
	}
	if res.Status == nil || !res.Status.Active {
		return errors.New("ufw staat niet aan na het toepassen")
	}
	st.Logf("de agent antwoordt; ufw staat aan (%d regels)", len(res.Status.Rules))
	if n.Address == "" || n.ClusterID == nil {
		return nil
	}
	listening := map[string]bool{}
	for _, l := range res.Status.Listeners {
		if l.Proto == "tcp" && !l.Local() {
			listening[strconv.Itoa(l.Port)] = true
		}
	}
	var targets []string
	for _, e := range plan.Entries {
		if e.State == "open" && e.Proto == "tcp" && (e.Class == ClassCluster || e.Origin == "ssh") && listening[e.Port] {
			t := net.JoinHostPort(n.Address, e.Port)
			if !slices.Contains(targets, t) {
				targets = append(targets, t)
			}
		}
	}
	if len(targets) == 0 {
		return nil
	}
	peers, err := s.nodes(ctx, nodesSQL+` WHERE n.cluster_id = $1 AND n.id <> $2`, *n.ClusterID, n.NodeID)
	if err != nil {
		return err
	}
	for _, peer := range peers {
		if !peer.Online || !peer.Supported {
			continue
		}
		r, err := s.request(ctx, peer.NodeID, protocol.FirewallRequest{Op: protocol.FirewallOpProbe, Probe: targets})
		if err != nil {
			st.Logf("controle vanaf %s lukt niet: %v", peer.Hostname, err)
			continue
		}
		var bad []string
		for _, pr := range r.Probe {
			switch {
			case pr.OK:
				st.Logf("%s → %s: open (%d ms)", peer.Hostname, pr.Target, pr.Millis)
			case pr.Refused:
				st.Logf("%s → %s: geweigerd (luistert er nog iets?)", peer.Hostname, pr.Target)
			default:
				bad = append(bad, pr.Target)
				st.Logf("%s → %s: geen antwoord (%s)", peer.Hostname, pr.Target, pr.Error)
			}
		}
		if len(bad) > 0 {
			return fmt.Errorf("%s bereikt %s niet meer", peer.Hostname, strings.Join(bad, ", "))
		}
		return nil
	}
	st.Logf("geen ander clusterlid online met protocol 30: controle vanaf het cluster overgeslagen")
	return nil
}

// Rollback draait een toepassing die nog op bevestiging wacht meteen terug.
func (s *Service) Rollback(ctx context.Context, actor events.Actor, id uuid.UUID) error {
	n, err := s.Node(ctx, id)
	if err != nil {
		return err
	}
	if !n.Online || !n.Supported {
		return &FieldError{n.Hostname + ": offline of agent te oud"}
	}
	if _, err := s.request(ctx, id, protocol.FirewallRequest{Op: protocol.FirewallOpRollback}); err != nil {
		return &FieldError{err.Error()}
	}
	s.event(ctx, actor, &n, "firewall.rolled_back", nil)
	_, _ = s.status(ctx, n)
	return nil
}

// DisableUfw schakelt ufw uit op een node en zet de stand op voorbeeld.
func (s *Service) DisableUfw(ctx context.Context, actor events.Actor, id uuid.UUID) error {
	n, err := s.Node(ctx, id)
	if err != nil {
		return err
	}
	if !n.Online || !n.Supported {
		return &FieldError{n.Hostname + ": offline of agent te oud"}
	}
	if _, err := s.request(ctx, id, protocol.FirewallRequest{Op: protocol.FirewallOpDisable}); err != nil {
		return &FieldError{err.Error()}
	}
	if n.Mode == "managed" {
		if _, err := s.pool.Exec(ctx, `UPDATE firewall_nodes SET mode = 'preview' WHERE node_id = $1`, id); err != nil {
			return err
		}
	}
	_, _ = s.pool.Exec(ctx, `UPDATE firewall_nodes SET applied_hash = '' WHERE node_id = $1`, id)
	s.event(ctx, actor, &n, "firewall.disabled", nil)
	_, _ = s.status(ctx, n)
	return nil
}

// Loop haalt elke BlockedEvery het geblokkeerde verkeer op, elke
// StatusEvery de stand, en past gewijzigde plannen toe.
func (s *Service) Loop(ctx context.Context) {
	t := time.NewTicker(s.BlockedEvery)
	defer t.Stop()
	for {
		if err := s.Tick(ctx); err != nil && ctx.Err() == nil {
			s.log.Warn("firewallronde mislukt", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Tick is één ronde van de poller.
func (s *Service) Tick(ctx context.Context) error {
	s.tick.Lock()
	defer s.tick.Unlock()
	set, err := s.Settings(ctx)
	if err != nil {
		return err
	}
	nodes, err := s.Nodes(ctx)
	if err != nil {
		return err
	}
	now := s.Now()
	for _, n := range nodes {
		if !n.Enabled() || !n.Supported || !n.Online {
			continue
		}
		if n.StatusAt == nil || now.Sub(*n.StatusAt) >= s.StatusEvery {
			if n, err = s.status(ctx, n); err != nil {
				s.log.Debug("firewallstand ophalen mislukt", "node", n.Hostname, "err", err)
				continue
			}
		} else if n, err = s.refreshPlan(ctx, n); err != nil {
			return err
		}
		if n.Mode == "managed" && set.AutoApply && n.Pending() && !n.ApplyRunning && n.Status != nil && n.Status.Pending == nil &&
			(n.ApplyFailedAt == nil || now.Sub(*n.ApplyFailedAt) >= RetryFailed) {
			if _, err := s.Apply(ctx, events.System(), n.NodeID); err != nil {
				s.log.Warn("firewall automatisch toepassen mislukt", "node", n.Hostname, "err", err)
			}
		}
		if set.Monitor && n.Status != nil && n.Status.Installed {
			if err := s.blocked(ctx, n); err != nil {
				s.log.Debug("geblokkeerd verkeer ophalen mislukt", "node", n.Hostname, "err", err)
			}
		}
	}
	if now.Sub(s.lastPrune) > time.Hour {
		s.lastPrune = now
		if _, err := s.pool.Exec(ctx, `DELETE FROM firewall_blocked WHERE hour < $1`, now.Add(-BlockedRetention)); err != nil {
			return err
		}
		if _, err := s.pool.Exec(ctx, `DELETE FROM firewall_ports WHERE last_seen < $1`, now.Add(-30*24*time.Hour)); err != nil {
			return err
		}
	}
	return nil
}

// blocked haalt het weggegooide verkeer van een node op en telt het per uur.
func (s *Service) blocked(ctx context.Context, n Node) error {
	res, err := s.request(ctx, n.NodeID, protocol.FirewallRequest{Op: protocol.FirewallOpBlocked})
	if err != nil {
		return err
	}
	b := res.Blocked
	if b == nil {
		return errors.New("leeg antwoord")
	}
	batch := &pgxBatch{}
	for _, h := range b.Hits {
		if h.Count <= 0 || h.Last.IsZero() {
			continue
		}
		batch.add(`INSERT INTO firewall_blocked (node_id, hour, src, proto, port, count, first_seen, last_seen)
			VALUES ($1, date_trunc('hour', $6::timestamptz), $2, $3, $4, $5, $7, $6)
			ON CONFLICT (node_id, hour, src, proto, port) DO UPDATE SET count = firewall_blocked.count + $5,
			last_seen = greatest(firewall_blocked.last_seen, $6)`,
			n.NodeID, h.Src, h.Proto, h.Port, h.Count, h.Last, h.First)
	}
	batch.add(`UPDATE firewall_nodes SET blocked_at = now(), blocked_source = $2 WHERE node_id = $1`, n.NodeID, b.Source)
	return batch.run(ctx, s.pool)
}

// pgxBatch voert opdrachten in één transactie uit.
type pgxBatch struct {
	sql  []string
	args [][]any
}

func (b *pgxBatch) add(sql string, args ...any) {
	b.sql = append(b.sql, sql)
	b.args = append(b.args, args)
}

func (b *pgxBatch) run(ctx context.Context, pool *pgxpool.Pool) error {
	if len(b.sql) == 0 {
		return nil
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for i, q := range b.sql {
		if _, err := tx.Exec(ctx, q, b.args[i]...); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
