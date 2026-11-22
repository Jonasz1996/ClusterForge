package pvehost

// Fase 26 M4: Proxmox-hosts clusterbewust bijwerken. Eén host tegelijk:
// onderhoudsmodus aan (HA verhuist zijn gasten), de andere draaiende gasten
// live verhuizen, wachten tot de host leeg is, apt full-upgrade via de
// agent, herstarten als het moet, wachten tot hij terug in het cluster
// zit, onderhoudsmodus uit en de verhuisde gasten terugzetten. Pas dan de
// volgende host. Bij een fout stopt de taak en blijft de host in
// onderhoud, zodat er niets halfs gebeurt op de volgende.

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/jobs"
	"github.com/Jonasz1996/clusterforge/internal/noderun"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// HostUpdate is een gevraagde ronde host-updates.
type HostUpdate struct {
	Datacenter string `json:"datacenter"`
	// Hosts zijn de Proxmox-namen, in de volgorde van bijwerken.
	Hosts []string `json:"hosts"`
	// Reboot: needed (alleen als het moet), always of never.
	Reboot string `json:"reboot"`
	// ShutdownStuck sluit gasten die niet kunnen verhuizen (passthrough,
	// lokale hardware) netjes af en start ze na de update weer.
	ShutdownStuck bool `json:"shutdown_stuck"`
}

// Validate controleert de vorm.
func (u HostUpdate) Validate() error {
	if len(u.Hosts) == 0 {
		return ValidationError{"kies minstens één host"}
	}
	seen := map[string]bool{}
	for _, h := range u.Hosts {
		if !nodeRe.MatchString(h) || seen[h] {
			return ValidationError{"ongeldige of dubbele host " + h}
		}
		seen[h] = true
	}
	switch u.Reboot {
	case "needed", "always", "never":
	default:
		return ValidationError{"reboot is needed, always of never"}
	}
	return nil
}

// StartHostUpdate zet de ronde in de wachtrij. confirm is de naam van het
// datacenter.
func (s *Service) StartHostUpdate(ctx context.Context, actor events.Actor, u HostUpdate, confirm string) (store.Job, error) {
	if err := u.Validate(); err != nil {
		return store.Job{}, err
	}
	dc, err := s.Datacenter(ctx, u.Datacenter)
	if err != nil {
		return store.Job{}, err
	}
	if strings.TrimSpace(confirm) != dc.Name {
		return store.Job{}, ValidationError{fmt.Sprintf("tik %s over om te bevestigen", dc.Name)}
	}
	var via *Host
	for _, name := range u.Hosts {
		h, ok := hostByName(dc, name)
		if !ok {
			return store.Job{}, ValidationError{"geen agent op " + name}
		}
		if why := h.APIReady(); why != "" {
			return store.Job{}, ValidationError{why}
		}
		if via == nil {
			via = &h
		}
	}
	nodeID := via.NodeID
	title := fmt.Sprintf("Proxmox-hosts bijwerken: %s", strings.Join(u.Hosts, ", "))
	spec := jobs.Spec{
		Kind: Kind, Title: title, Actor: actor, NodeID: &nodeID, ProxmoxID: via.ConnectionID,
		Params: params{NodeID: nodeID, PVENode: via.PVENode, ConnID: via.ConnectionID, HostUpdate: &u},
	}
	j, err := s.jobs.Enqueue(ctx, spec)
	if err != nil {
		return store.Job{}, err
	}
	s.audit(ctx, actor, *via, "pvehost.host_update_requested", map[string]any{
		"datacenter": u.Datacenter, "hosts": u.Hosts, "reboot": u.Reboot, "shutdown_stuck": u.ShutdownStuck, "job_id": j.ID.String(),
	})
	s.jobs.Kick()
	return j, nil
}

func hostByName(dc Datacenter, name string) (Host, bool) {
	for _, h := range dc.Hosts {
		if h.PVENode == name {
			return h, true
		}
	}
	return Host{}, false
}

// readVia leest een pad bij een agent in het datacenter die niet op skip
// draait (die host kan net herstarten).
func (s *Service) readVia(ctx context.Context, dcID, skip, path string, v any) error {
	dc, err := s.Datacenter(ctx, dcID)
	if err != nil {
		return err
	}
	last := errors.New("geen andere host met een agent")
	for _, h := range dc.Hosts {
		if h.PVENode == skip || h.APIReady() != "" {
			continue
		}
		if last = s.nodeGet(ctx, h.NodeID, path, v); last == nil {
			return nil
		}
	}
	return last
}

type planGuest struct {
	Type   string `json:"type"`
	VMID   int    `json:"vmid"`
	Name   string `json:"name"`
	Target string `json:"target,omitempty"`
}

type hostPlan struct {
	Host  string      `json:"host"`
	Moves []planGuest `json:"moves"`
	Stops []planGuest `json:"stops"`
	HA    []string    `json:"ha"`
}

type updatePlan struct {
	Hosts []hostPlan `json:"hosts"`
}

// PlanHostUpdate toont wat een ronde zou doen, zonder iets te veranderen.
func (s *Service) PlanHostUpdate(ctx context.Context, u HostUpdate) (any, []string, error) {
	if u.Reboot == "" {
		u.Reboot = "needed"
	}
	if err := u.Validate(); err != nil {
		return nil, nil, err
	}
	dc, err := s.Datacenter(ctx, u.Datacenter)
	if err != nil {
		return nil, nil, err
	}
	var lines []string
	pl, err := s.plan(ctx, u, dc, func(f string, a ...any) { lines = append(lines, fmt.Sprintf(f, a...)) })
	if err != nil {
		return nil, nil, ValidationError{err.Error()}
	}
	return pl, lines, nil
}

func (g planGuest) label() string {
	k := "VM"
	if g.Type == "lxc" {
		k = "CT"
	}
	if g.Name != "" {
		return fmt.Sprintf("%s %d (%s)", k, g.VMID, g.Name)
	}
	return fmt.Sprintf("%s %d", k, g.VMID)
}

func (g planGuest) path(node string) string {
	return fmt.Sprintf("/nodes/%s/%s/%d", node, g.Type, g.VMID)
}

// plan zegt per host welke gasten verhuizen (en waarheen) en welke
// afgesloten worden. Doelen zijn hosts met de meeste vrije RAM die niet
// zelf in deze ronde zitten, of die al bijgewerkt zijn.
func (s *Service) plan(ctx context.Context, u HostUpdate, dc Datacenter, log func(string, ...any)) (updatePlan, error) {
	var res []Resource
	if err := s.readVia(ctx, u.Datacenter, "", "/cluster/resources", &res); err != nil {
		return updatePlan{}, err
	}
	free := map[string]float64{}
	online := map[string]bool{}
	for _, r := range res {
		if r.Type == "node" && r.Status == "online" {
			online[r.Node] = true
			free[r.Node] = float64(r.MaxMem) - float64(r.Mem)
		}
	}
	var out updatePlan
	var stuck []string
	for i, name := range u.Hosts {
		hp := hostPlan{Host: name}
		if !online[name] {
			return updatePlan{}, fmt.Errorf("%s is niet online in het cluster", name)
		}
		var guests []Resource
		for _, r := range res {
			if (r.Type == "qemu" || r.Type == "lxc") && r.Node == name && r.Status == "running" && r.Template == 0 {
				guests = append(guests, r)
			}
		}
		sort.Slice(guests, func(a, b int) bool { return guests[a].VMID < guests[b].VMID })
		for _, g := range guests {
			pg := planGuest{Type: r2type(g.Type), VMID: int(g.VMID), Name: g.Name}
			if g.HAState != "" {
				hp.HA = append(hp.HA, pg.label())
				continue
			}
			if dc.Standalone {
				hp.Stops = append(hp.Stops, pg)
				continue
			}
			if pg.Type == "qemu" {
				var pre struct {
					LocalResources []string `json:"local_resources"`
				}
				if err := s.readVia(ctx, u.Datacenter, "", pg.path(name)+"/migrate", &pre); err == nil && len(pre.LocalResources) > 0 {
					stuck = append(stuck, fmt.Sprintf("%s op %s (%s)", pg.label(), name, strings.Join(pre.LocalResources, ", ")))
					hp.Stops = append(hp.Stops, pg)
					continue
				}
			}
			// Doel: een andere online host, liefst al bijgewerkt of niet in
			// deze ronde, met de meeste vrije RAM.
			best, bestFree := "", -1.0
			for n, f := range free {
				if n == name || !online[n] {
					continue
				}
				pending := slices.Contains(u.Hosts[i+1:], n)
				score := f
				if pending {
					score -= 1e15
				}
				if score > bestFree {
					best, bestFree = n, score
				}
			}
			if best == "" {
				stuck = append(stuck, fmt.Sprintf("%s op %s (geen andere host)", pg.label(), name))
				hp.Stops = append(hp.Stops, pg)
				continue
			}
			pg.Target = best
			free[best] -= float64(g.MaxMem)
			free[name] += float64(g.MaxMem)
			hp.Moves = append(hp.Moves, pg)
		}
		out.Hosts = append(out.Hosts, hp)
	}
	if len(stuck) > 0 && !u.ShutdownStuck && !dc.Standalone {
		return updatePlan{}, fmt.Errorf("deze gasten kunnen niet verhuizen: %s. Zet ze uit, of kies \"niet-verhuisbare gasten afsluiten\"", strings.Join(stuck, "; "))
	}
	for _, hp := range out.Hosts {
		log("%s: %d verhuizen, %d onder HA, %d afsluiten", hp.Host, len(hp.Moves), len(hp.HA), len(hp.Stops))
		for _, m := range hp.Moves {
			log("  %s → %s", m.label(), m.Target)
		}
		for _, m := range hp.Stops {
			log("  %s gaat even uit", m.label())
		}
	}
	return out, nil
}

func r2type(t string) string {
	if t == "lxc" {
		return "lxc"
	}
	return "qemu"
}

// runHostUpdate doorloopt het plan. De stappen liggen vast zodra het plan
// er is, zodat een hervatte taak dezelfde volgorde ziet.
func (s *Service) runHostUpdate(ctx context.Context, j *jobs.Job, p params) error {
	u := *p.HostUpdate
	dc, err := s.Datacenter(ctx, u.Datacenter)
	if err != nil {
		return err
	}
	var pl updatePlan
	if err := j.Step(ctx, "Plan maken", func(ctx context.Context, st *jobs.Step) error {
		var e error
		pl, e = s.plan(ctx, u, dc, st.Logf)
		if e != nil {
			return e
		}
		return st.SetState(ctx, pl)
	}); err != nil {
		return err
	}
	if len(pl.Hosts) == 0 && !j.Done("Plan maken", &pl) {
		return errors.New("geen plan")
	}
	for _, hp := range pl.Hosts {
		if err := s.updateOneHost(ctx, j, u, dc, hp); err != nil {
			return fmt.Errorf("%s: %w (de host blijft in onderhoudsmodus; los het op en haal hem eruit via Datacenter, of start de ronde opnieuw)", hp.Host, err)
		}
	}
	return nil
}

func (s *Service) updateOneHost(ctx context.Context, j *jobs.Job, u HostUpdate, dc Datacenter, hp hostPlan) error {
	name := hp.Host
	h, ok := hostByName(dc, name)
	if !ok {
		return errors.New("geen agent meer")
	}
	cluster := !dc.Standalone

	if err := j.Step(ctx, name+": onderhoudsmodus aan", func(ctx context.Context, st *jobs.Step) error {
		if !cluster {
			st.Logf("Losse host: geen HA, geen onderhoudsmodus.")
			return nil
		}
		return s.hostAction(ctx, st, h, protocol.PVERun{Action: protocol.PVEMaintenance, Enable: true}, "onderhoudsmodus aan")
	}); err != nil {
		return err
	}
	for _, g := range hp.Moves {
		if err := j.Step(ctx, fmt.Sprintf("%s: %s naar %s", name, g.label(), g.Target), func(ctx context.Context, st *jobs.Step) error {
			return s.apiStep(ctx, st, h.NodeID, "create", g.path(name)+"/migrate", migrateParams(g), "verhuizen naar "+g.Target)
		}); err != nil {
			return err
		}
	}
	for _, g := range hp.Stops {
		if err := j.Step(ctx, fmt.Sprintf("%s: %s afsluiten", name, g.label()), func(ctx context.Context, st *jobs.Step) error {
			return s.apiStep(ctx, st, h.NodeID, "create", g.path(name)+"/status/shutdown", map[string]string{"timeout": "180"}, "afsluiten")
		}); err != nil {
			return err
		}
	}
	if err := j.Step(ctx, name+": wachten tot hij leeg is", func(ctx context.Context, st *jobs.Step) error {
		if !cluster {
			return nil
		}
		return s.waitEmpty(ctx, st, u.Datacenter, name)
	}); err != nil {
		return err
	}
	if err := j.Step(ctx, name+": pakketten bijwerken", func(ctx context.Context, st *jobs.Step) error {
		spec := protocol.RunSpec{ID: uuid.NewString(), Kind: protocol.RunApt, Title: "Pakketten bijwerken op " + name,
			Apt: &protocol.AptRun{Full: true, Smart: true}}
		res, err := s.Run.DoWith(ctx, st, st, h.NodeID, spec)
		if err != nil {
			return err
		}
		if res.State != protocol.RunOK {
			return errors.New(noderun.Describe(res))
		}
		return nil
	}); err != nil {
		return err
	}
	var rebooted bool
	var sent time.Time
	if err := j.Step(ctx, name+": herstarten", func(ctx context.Context, st *jobs.Step) error {
		need := u.Reboot == "always"
		if u.Reboot == "needed" {
			hw, err := s.Hardware(ctx, h.NodeID)
			if err != nil {
				return fmt.Errorf("niet te zien of %s moet herstarten: %w", name, err)
			}
			need = hw.RebootRequired
			if need {
				st.Logf("Herstart nodig: %s", strings.Join(hw.RebootReasons, "; "))
			}
		}
		if !need {
			st.Logf("Geen herstart nodig.")
			return nil
		}
		sent = time.Now()
		rebooted = true
		_ = st.SetState(ctx, map[string]any{"sent": sent})
		// De agent kan wegvallen voor hij antwoordt; dat is hier normaal.
		err := s.apiStep(ctx, st, h.NodeID, "create", "/nodes/"+name+"/status", map[string]string{"command": "reboot"}, "herstarten")
		if err != nil {
			st.Logf("(%v; de host herstart waarschijnlijk al)", err)
		}
		return nil
	}); err != nil {
		return err
	}
	var rs struct {
		Sent time.Time `json:"sent"`
	}
	if !rebooted && j.Done(name+": herstarten", &rs) && !rs.Sent.IsZero() {
		rebooted, sent = true, rs.Sent
	}
	if err := j.Step(ctx, name+": wachten tot hij terug is", func(ctx context.Context, st *jobs.Step) error {
		if !rebooted {
			st.Logf("Niet herstart.")
			return nil
		}
		return s.waitBack(ctx, st, u.Datacenter, h, sent)
	}); err != nil {
		return err
	}
	if err := j.Step(ctx, name+": onderhoudsmodus uit", func(ctx context.Context, st *jobs.Step) error {
		if !cluster {
			return nil
		}
		return s.hostAction(ctx, st, h, protocol.PVERun{Action: protocol.PVEMaintenance, Enable: false}, "onderhoudsmodus uit")
	}); err != nil {
		return err
	}
	for _, g := range hp.Moves {
		if err := j.Step(ctx, fmt.Sprintf("%s: %s terug", name, g.label()), func(ctx context.Context, st *jobs.Step) error {
			src, ok := hostByName(dc, g.Target)
			if !ok || src.APIReady() != "" {
				st.Logf("Geen agent op %s; %s blijft daar.", g.Target, g.label())
				return nil
			}
			back := g
			back.Target = name
			return s.apiStep(ctx, st, src.NodeID, "create", g.path(g.Target)+"/migrate", migrateParams(back), "terug naar "+name)
		}); err != nil {
			return err
		}
	}
	for _, g := range hp.Stops {
		if err := j.Step(ctx, fmt.Sprintf("%s: %s starten", name, g.label()), func(ctx context.Context, st *jobs.Step) error {
			var cur struct {
				Status string `json:"status"`
			}
			if err := s.nodeGet(ctx, h.NodeID, g.path(name)+"/status/current", &cur); err == nil && cur.Status == "running" {
				st.Logf("Draait al (onboot).")
				return nil
			}
			return s.apiStep(ctx, st, h.NodeID, "create", g.path(name)+"/status/start", nil, "starten")
		}); err != nil {
			return err
		}
	}
	return nil
}

func migrateParams(g planGuest) map[string]string {
	if g.Type == "lxc" {
		return map[string]string{"target": g.Target, "restart": "1", "timeout": "120"}
	}
	return map[string]string{"target": g.Target, "online": "1", "with-local-disks": "1"}
}

func (s *Service) hostAction(ctx context.Context, st *jobs.Step, h Host, op protocol.PVERun, title string) error {
	res, err := s.Do(ctx, st, st, h.NodeID, op, title+" op "+h.PVENode)
	if err != nil {
		return err
	}
	if res.State != protocol.RunOK {
		return errors.New(noderun.Describe(res))
	}
	return nil
}

func (s *Service) wait(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

// waitEmpty wacht tot er geen gast meer draait op de host (HA verhuist
// zijn gasten in onderhoudsmodus zelf).
func (s *Service) waitEmpty(ctx context.Context, st *jobs.Step, dcID, name string) error {
	deadline := time.Now().Add(s.hostWait(30 * time.Minute))
	for {
		var res []Resource
		if err := s.readVia(ctx, dcID, "", "/cluster/resources", &res); err == nil {
			var left []string
			for _, r := range res {
				if (r.Type == "qemu" || r.Type == "lxc") && r.Node == name && r.Status == "running" {
					left = append(left, strconv.Itoa(int(r.VMID)))
				}
			}
			if len(left) == 0 {
				st.Logf("Er draait niets meer op %s.", name)
				return nil
			}
			if time.Now().After(deadline) {
				return fmt.Errorf("na 30 minuten draaien er nog gasten op %s: %s", name, strings.Join(left, ", "))
			}
		}
		if err := s.wait(ctx, s.pollEvery()); err != nil {
			return err
		}
	}
}

// waitBack wacht tot de host na de herstart weer in het cluster zit en zijn
// agent verbonden is.
func (s *Service) waitBack(ctx context.Context, st *jobs.Step, dcID string, h Host, sent time.Time) error {
	deadline := time.Now().Add(s.hostWait(20 * time.Minute))
	for {
		cur, err := s.Host(ctx, h.NodeID)
		if err == nil && cur.Online && cur.HeartbeatAt != nil && cur.HeartbeatAt.After(sent) {
			var stt struct {
				Uptime num `json:"uptime"`
			}
			if err := s.nodeGet(ctx, h.NodeID, "/nodes/"+h.PVENode+"/status", &stt); err == nil && float64(stt.Uptime) > 0 &&
				time.Duration(float64(stt.Uptime))*time.Second < time.Since(sent)+time.Minute {
				var cs []clusterStatus
				if err := s.readVia(ctx, dcID, "", "/cluster/status", &cs); err == nil && hostOnline(cs, h.PVENode) {
					st.Logf("%s is terug (uptime %s).", h.PVENode, (time.Duration(float64(stt.Uptime)) * time.Second).String())
					return nil
				}
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s was na 20 minuten niet terug", h.PVENode)
		}
		if err := s.wait(ctx, s.pollEvery()); err != nil {
			return err
		}
	}
}

func hostOnline(cs []clusterStatus, name string) bool {
	for _, c := range cs {
		if c.Type == "node" && c.Name == name {
			return c.Online == 1
		}
	}
	return false
}

func (s *Service) hostWait(d time.Duration) time.Duration {
	if s.HostWait > 0 {
		return s.HostWait
	}
	return d
}
