package backups

// De uitvoering van de DR-repetitie, in zes stappen: de voorcontrole
// opnieuw, alle back-ups tegelijk terugzetten, de netwerkkaarten naar de
// afgesloten bridge, starten, controleren en alles weer verwijderen. Zolang
// de sandboxes kunnen draaien, bewaakt ClusterForge het echte cluster: bij
// een tweede agentverbinding, een verhuisd VIP of split-brain gaan alle
// sandboxes meteen hard uit.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/jobs"
	"github.com/Jonasz1996/clusterforge/internal/proxmox"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// De zes stappen van de repetitie.
const (
	stepDRPlan    = "Voorcontrole"
	stepDRRestore = "Terugzetten"
	stepDRIsolate = "Afsluiten"
	stepDRStart   = "Starten"
	stepDRCheck   = "Controleren"
	stepDRCleanup = "Opruimen"
)

var (
	errRehearsalLimit = errors.New("de DR-repetitie duurde te lang")
	// errAborted: de bewaking zag iets aan het echte cluster.
	errAborted = errors.New("afgebroken door de bewaking")
)

// RehearsalNodeResult is een node in het rapport.
type RehearsalNodeResult struct {
	NodeID         uuid.UUID `json:"node_id"`
	Hostname       string    `json:"hostname"`
	SourceVMID     int       `json:"source_vmid"`
	SandboxVMID    int       `json:"sandbox_vmid,omitempty"`
	BackupTime     time.Time `json:"backup_time"`
	RestoreSeconds *float64  `json:"restore_seconds,omitempty"`
	BootSeconds    *float64  `json:"boot_seconds,omitempty"`
	// Addresses zijn de adressen die cf-agent in de sandbox meldde.
	Addresses    []string `json:"addresses"`
	AgentVersion string   `json:"agent_version,omitempty"`
	Destroyed    bool     `json:"destroyed"`
}

// RehearsalVIPResult is een VIP in het rapport: wie het in de sandbox had
// en of het antwoordde.
type RehearsalVIPResult struct {
	Address string   `json:"address"`
	Holders []string `json:"holders"`
	// Checked is false als geen node zijn adressen kon melden.
	Checked bool   `json:"checked"`
	Probe   string `json:"probe"`
	ProbeOK *bool  `json:"probe_ok,omitempty"`
}

// RehearsalMeasurements staan in test_runs.measurements.
type RehearsalMeasurements struct {
	// RTO is de tijd van de start van het terugzetten tot alle controles
	// geslaagd waren; leeg als het cluster niet werkend terugkwam. RPO is de
	// leeftijd van de oudste back-up bij de start van het terugzetten.
	RTOSeconds       *float64 `json:"rto_seconds,omitempty"`
	RPOSeconds       *float64 `json:"rpo_seconds,omitempty"`
	RTOTargetMinutes int      `json:"rto_target_minutes"`
	RPOTargetHours   int      `json:"rpo_target_hours"`
	RTOMet           *bool    `json:"rto_met,omitempty"`
	RPOMet           *bool    `json:"rpo_met,omitempty"`

	RestoreSeconds *float64 `json:"restore_seconds,omitempty"`
	BootSeconds    *float64 `json:"boot_seconds,omitempty"`
	CheckSeconds   *float64 `json:"check_seconds,omitempty"`
	CleanupSeconds *float64 `json:"cleanup_seconds,omitempty"`
	TotalSeconds   *float64 `json:"total_seconds,omitempty"`

	Host        string                `json:"host,omitempty"`
	Storage     string                `json:"storage,omitempty"`
	Bridge      string                `json:"bridge,omitempty"`
	Nodes       []RehearsalNodeResult `json:"nodes"`
	VIPs        []RehearsalVIPResult  `json:"vips"`
	DestroyedAt *time.Time            `json:"destroyed_at,omitempty"`
	// Aborted is waarom de bewaking de repetitie afbrak.
	Aborted string `json:"aborted,omitempty"`
}

// rehearsal is één uitvoering van dr.rehearsal.
type rehearsal struct {
	s     *Service
	j     *jobs.Job
	run   store.TestRun
	def   RehearsalDefinition
	start time.Time
	// t0 is de start van het terugzetten: daar begint de RTO.
	t0 time.Time

	api proxmox.API
	// planFailed: de voorcontrole in de taak faalde; er is niets gedaan.
	planFailed bool

	mu       sync.Mutex
	sbs      map[uuid.UUID]store.BackupSandbox
	checks   []Check
	timeline []TimelineEvent
	m        RehearsalMeasurements

	interrupt context.CancelCauseFunc
	stopWatch func()
}

func (s *Service) runRehearsal(ctx context.Context, j *jobs.Job) error {
	var p rehearsalParams
	if err := j.Decode(&p); err != nil {
		return err
	}
	row, err := s.q.GetTestRun(ctx, p.RunID)
	if err != nil {
		return fmt.Errorf("run %s: %w", p.RunID, err)
	}
	r := &rehearsal{s: s, j: j, run: row.TestRun, start: s.Now(), sbs: map[uuid.UUID]store.BackupSandbox{}}
	if err := json.Unmarshal(row.TestRun.Definition, &r.def); err != nil {
		return err
	}
	if row.TestRun.Result != nil {
		return nil
	}
	r.measure()
	if j.Attempts > 1 {
		return r.abandon(ctx)
	}
	ctx, cancel := context.WithTimeoutCause(ctx, s.RehearsalLimit, errRehearsalLimit)
	defer cancel()
	return r.execute(ctx)
}

// measure zet de vaste gegevens van de definitie in het rapport.
func (r *rehearsal) measure() {
	d := r.def
	r.m.RTOTargetMinutes, r.m.RPOTargetHours = d.RTOTargetMinutes, d.RPOTargetHours
	r.m.Host, r.m.Storage, r.m.Bridge = d.Host, d.Storage, d.Bridge
	r.m.Nodes = make([]RehearsalNodeResult, 0, len(d.Nodes))
	for _, n := range d.Nodes {
		r.m.Nodes = append(r.m.Nodes, RehearsalNodeResult{
			NodeID: n.NodeID, Hostname: n.Hostname, SourceVMID: n.SourceVMID, BackupTime: n.BackupTime, Addresses: []string{},
		})
	}
	r.m.VIPs = make([]RehearsalVIPResult, 0, len(d.VIPs))
	for _, v := range d.VIPs {
		r.m.VIPs = append(r.m.VIPs, RehearsalVIPResult{Address: v.Address, Holders: []string{}, Probe: v.Probe.String()})
	}
}

// node is het rapport van een node; aanroepen met r.mu.
func (r *rehearsal) node(id uuid.UUID) *RehearsalNodeResult {
	for i := range r.m.Nodes {
		if r.m.Nodes[i].NodeID == id {
			return &r.m.Nodes[i]
		}
	}
	return &RehearsalNodeResult{}
}

func (r *rehearsal) execute(ctx context.Context) error {
	err := r.j.Step(ctx, stepDRPlan, r.plan)
	if err == nil && !r.planFailed {
		err = r.j.Step(ctx, stepDRRestore, r.restore)
	}
	if err == nil && !r.planFailed {
		err = r.j.Step(ctx, stepDRIsolate, r.isolate)
	}
	if err == nil && !r.planFailed {
		wctx, interrupt := context.WithCancelCause(ctx)
		r.interrupt = interrupt
		err = r.j.Step(wctx, stepDRStart, r.boot)
		if err == nil {
			err = r.j.Step(wctx, stepDRCheck, r.check)
		}
		if err != nil && errors.Is(context.Cause(wctx), errAborted) {
			err = errAborted
		}
		interrupt(nil)
	}
	r.unwatch()
	if jobs.Interrupted(ctx) {
		// Na de herstart ruimt de taak op; tot dan staan de sandboxes uit.
		r.emergencyStop()
		return err
	}
	// Opruimen loopt altijd, ook na annuleren, afbreken of de tijdslimiet.
	dctx, cancel := jobs.Detach(ctx)
	defer cancel()
	dctx, cancelLimit := context.WithTimeout(dctx, 30*time.Minute)
	defer cancelLimit()
	cerr := r.j.Step(dctx, stepDRCleanup, r.cleanup)
	if jobs.Interrupted(dctx) {
		return cerr
	}
	return r.finish(ctx, err)
}

// abandon is de taak na een herstart van de server: de onderbroken stap
// faalt, en dan alleen opruimen.
func (r *rehearsal) abandon(ctx context.Context) error {
	for _, p := range r.j.Previous() {
		if p.Name == stepDRCleanup {
			break
		}
		if p.Status != store.JobStatusRunning {
			r.j.Keep()
			continue
		}
		_ = r.j.Step(ctx, p.Name, func(context.Context, *jobs.Step) error { return errRestarted })
	}
	dctx, cancel := jobs.Detach(ctx)
	defer cancel()
	cerr := r.j.Step(dctx, stepDRCleanup, r.cleanup)
	if jobs.Interrupted(dctx) {
		return cerr
	}
	return r.finish(ctx, errRestarted)
}

func (r *rehearsal) event(text string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.timeline = append(r.timeline, TimelineEvent{TMS: r.s.Now().Sub(r.start).Milliseconds(), Kind: "step", Text: text})
}

func (r *rehearsal) addCheck(c ...Check) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.checks = append(r.checks, c...)
}

// plan doet de voorcontrole opnieuw, want sinds de start kan er een nieuwere
// back-up zijn of iets aan de bridge veranderd. De definitie in de run wordt
// wat er echt teruggaat.
func (r *rehearsal) plan(ctx context.Context, st *jobs.Step) error {
	s := r.s
	p, err := s.PlanRehearsal(ctx, RehearsalInput{
		ScenarioID: r.def.ScenarioID, Name: r.def.Name, ClusterID: r.def.ClusterID, Bridge: r.def.Bridge, VIPs: r.def.VIPs,
		ownJob: r.j.ID,
	})
	if err != nil {
		return err
	}
	for _, c := range p.Checks {
		st.Logf("%s %s: %s", mark(c), c.Name, c.Detail)
	}
	for _, f := range p.Fix {
		st.Logf("  %s", f)
	}
	if !p.OK {
		r.planFailed = true
		r.def.Fix = p.Fix
		for _, c := range p.Checks {
			if !c.OK {
				r.addCheck(c)
			}
		}
		r.saveDefinition(ctx)
		return nil
	}
	r.def = p.RehearsalDefinition
	r.measure()
	r.saveDefinition(ctx)
	var warn []Check
	for _, c := range p.Checks {
		if c.Warning {
			warn = append(warn, c)
		}
	}
	r.addCheck(Check{Name: "Voorcontrole", OK: true, Detail: fmt.Sprintf("%s, alles op %s, storage %s, aan %s",
		count(len(r.def.Nodes), "node", "nodes"), r.def.Host, r.def.Storage, r.def.Bridge)})
	r.addCheck(warn...)
	r.event(fmt.Sprintf("Voorcontrole geslaagd: %s naar %s op %s", count(len(r.def.Nodes), "node", "nodes"), r.def.Bridge, r.def.Host))
	return nil
}

func (r *rehearsal) saveDefinition(ctx context.Context) {
	b, err := json.Marshal(r.def)
	if err == nil {
		err = r.s.q.SetTestRunDefinition(ctx, store.SetTestRunDefinitionParams{ID: r.run.ID, Definition: b})
	}
	if err != nil {
		r.s.log.Warn("definitie van de DR-repetitie bewaren", "run", r.run.ID, "err", err)
	}
}

func mark(c Check) string {
	switch {
	case c.OK:
		return "✓"
	case c.Warning:
		return "!"
	}
	return "✗"
}

// restore reserveert een VMID per node en zet alle back-ups tegelijk terug,
// zoals bij een echte ramp.
func (r *rehearsal) restore(ctx context.Context, st *jobs.Step) error {
	s := r.s
	api, _, err := s.pve.APIFor(ctx, r.def.ConnectionID)
	if err != nil {
		return err
	}
	r.api = api
	res, err := api.Resources(ctx)
	if err != nil {
		return err
	}
	used := map[int]bool{}
	for _, x := range res {
		if x.VMID > 0 {
			used[x.VMID] = true
		}
	}
	next, err := api.NextID(ctx)
	if err != nil {
		return fmt.Errorf("vrij VMID opvragen: %w", err)
	}
	r.t0 = s.Now()
	oldest, _ := r.def.oldest()
	r.m.RPOSeconds = secs(r.t0.Sub(oldest))

	type pending struct {
		n    RehearsalNode
		sb   store.BackupSandbox
		upid string
	}
	var todo []pending
	for _, n := range r.def.Nodes {
		for used[next] {
			next++
		}
		vmid := next
		used[vmid] = true
		nodeID := n.NodeID
		sb, err := s.q.InsertBackupSandbox(context.WithoutCancel(ctx), store.InsertBackupSandboxParams{
			ConnectionID: r.def.ConnectionID, Vmid: int32(vmid), SourceVmid: int32(n.SourceVMID), SourceNodeID: &nodeID,
			RunID: &r.run.ID, Volid: n.Volid, Host: r.def.Host, Storage: r.def.Storage,
		})
		if err != nil {
			return fmt.Errorf("sandbox registreren: %w", err)
		}
		r.mu.Lock()
		r.sbs[n.NodeID] = sb
		r.node(n.NodeID).SandboxVMID = vmid
		r.mu.Unlock()
		upid, err := s.restoreSandbox(ctx, api, sb, n.Volid)
		if err != nil {
			if strings.Contains(err.Error(), "pool") {
				err = fmt.Errorf("%w; bestaat pool %s in Proxmox? (pveum pool add %s)", err, proxmox.SandboxPool, proxmox.SandboxPool)
			}
			r.addCheck(Check{Name: n.Hostname + ": Terugzetten", Detail: "terugzetten starten mislukte: " + err.Error()})
			return fmt.Errorf("%s terugzetten: %w", n.Hostname, err)
		}
		st.Logf("%s: back-up van %s terugzetten als VM %d", n.Hostname, n.BackupTime.Local().Format("02-01-2006 15:04"), vmid)
		r.event(fmt.Sprintf("Terugzetten van %s begonnen als VM %d", n.Hostname, vmid))
		todo = append(todo, pending{n, sb, upid})
	}
	_ = st.Flush(ctx)

	// Alle taken tegelijk volgen. De uitvoer van één taak zou die van de
	// andere overschrijven, dus het log krijgt alleen het einde van elk.
	errs := make([]error, len(todo))
	var wg sync.WaitGroup
	for i, p := range todo {
		wg.Go(func() {
			err := s.pve.WaitTask(ctx, api, p.upid, nil)
			d := s.Now().Sub(r.t0)
			if err != nil {
				errs[i] = fmt.Errorf("%s: %w", p.n.Hostname, err)
				if ctx.Err() == nil {
					r.addCheck(Check{Name: p.n.Hostname + ": Terugzetten", Detail: "Proxmox kon de back-up niet terugzetten: " + err.Error()})
				}
				st.Logf("%s: terugzetten mislukt: %v", p.n.Hostname, err)
				return
			}
			sb, serr := s.setSandboxState(ctx, p.sb, sbPresent, "", events.System())
			r.mu.Lock()
			if serr == nil {
				r.sbs[p.n.NodeID] = sb
			}
			r.node(p.n.NodeID).RestoreSeconds = secs(d)
			r.mu.Unlock()
			st.Logf("%s: teruggezet als VM %d na %s", p.n.Hostname, p.sb.Vmid, duration(d))
			r.event(fmt.Sprintf("%s teruggezet na %s", p.n.Hostname, duration(d)))
		})
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		return err
	}
	d := s.Now().Sub(r.t0)
	r.m.RestoreSeconds = secs(d)
	var vms []string
	for _, n := range r.m.Nodes {
		vms = append(vms, fmt.Sprintf("%s als VM %d", n.Hostname, n.SandboxVMID))
	}
	r.addCheck(Check{Name: "Terugzetten", OK: true, Detail: fmt.Sprintf("alle back-ups tegelijk in %s op %s, storage %s: %s",
		duration(d), r.def.Host, r.def.Storage, strings.Join(vms, ", "))})
	return nil
}

// sandbox is de sandbox van een node.
func (r *rehearsal) sandbox(id uuid.UUID) (store.BackupSandbox, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	sb, ok := r.sbs[id]
	return sb, ok
}

// mine zijn de VMID's van de sandboxes van deze repetitie.
func (r *rehearsal) mine() map[int]bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := map[int]bool{}
	for _, sb := range r.sbs {
		out[int(sb.Vmid)] = true
	}
	return out
}

// drNet zet een netwerkkaart uit de back-up op de afgesloten bridge: met het
// MAC-adres van de bron, zodat het besturingssysteem zijn eigen config
// vindt, zonder VLAN-tag en trunks, want de bridge heeft geen poorten.
func drNet(orig, bridge string, down bool) string {
	var out []string
	for i, part := range strings.Split(orig, ",") {
		k, _, found := strings.Cut(part, "=")
		if i > 0 && found && slices.Contains([]string{"bridge", "tag", "trunks", "link_down"}, k) {
			continue
		}
		out = append(out, part)
	}
	out = append(out, "bridge="+bridge)
	if down {
		out = append(out, "link_down=1")
	}
	return strings.Join(out, ",")
}

// wasDown is true als de kaart in de bron al losgekoppeld stond; die blijft
// in de repetitie ook los.
func wasDown(orig string) bool {
	v, _ := proxmox.OptionOr(orig, "link_down", "")
	return v == "1"
}

// drIsolation is de wijziging die een teruggezette VM afsluit en zijn
// netwerkkaarten, nog losgekoppeld, aan de afgesloten bridge hangt.
func drIsolation(cfg, nets map[string]string, bridge, description string) map[string]string {
	out := isolation(cfg, description)
	for k := range cfg {
		if !netKey(k) {
			continue
		}
		orig, ok := nets[k]
		if !ok {
			orig = cfg[k]
		}
		out[k] = drNet(orig, bridge, true)
	}
	return out
}

// drProblems controleert een teruggelezen config. up zijn de kaarten die
// aan mogen staan; alle andere moeten link_down=1 hebben.
func drProblems(cfg map[string]string, bridge string, up map[string]bool) []string {
	var out []string
	for _, k := range slices.Sorted(maps.Keys(cfg)) {
		if !netKey(k) {
			continue
		}
		v := cfg[k]
		if b, _ := proxmox.OptionOr(v, "bridge", ""); b != bridge {
			out = append(out, fmt.Sprintf("%s hangt aan %q in plaats van %s", k, b, bridge))
		}
		for _, o := range []string{"tag", "trunks"} {
			if _, ok := proxmox.OptionOr(v, o, ""); ok {
				out = append(out, k+" heeft nog "+o)
			}
		}
		down, _ := proxmox.OptionOr(v, "link_down", "")
		switch {
		case up[k] && down == "1":
			out = append(out, k+" staat nog los")
		case !up[k] && down != "1":
			out = append(out, k+" heeft geen link_down=1")
		}
	}
	return append(out, sandboxProblems(cfg)...)
}

func drDescription(n RehearsalNode, cluster, bridge, runID string) string {
	return fmt.Sprintf("Tijdelijke sandbox van ClusterForge: DR-repetitie van %s, node %s (VM %d), back-up %s. "+
		"Netwerk alleen op de afgesloten bridge %s. ClusterForge verwijdert deze VM zelf na de repetitie (run %s).",
		cluster, n.Hostname, n.SourceVMID, n.Volid, bridge, runID)
}

// configure wijzigt de config van een sandbox en wacht op de taak.
func (r *rehearsal) configure(ctx context.Context, sb store.BackupSandbox, change map[string]string) error {
	vm, err := r.s.guard(ctx, sb.ID)
	if err != nil {
		return err
	}
	form := url.Values{}
	for k, v := range change {
		form.Set(k, v)
	}
	upid, err := vm.configure(ctx, form)
	if err == nil && upid != "" {
		err = r.s.pve.WaitTask(ctx, vm.api, upid, nil)
	}
	return err
}

// readBack leest de config van een sandbox opnieuw uit Proxmox.
func (r *rehearsal) readBack(ctx context.Context, sb store.BackupSandbox) (map[string]string, error) {
	vm, err := r.s.guard(ctx, sb.ID)
	if err != nil {
		return nil, err
	}
	return vm.config(ctx)
}

// isolate sluit elke sandbox af en hangt zijn kaarten, nog los, aan de
// afgesloten bridge. Pas als de config van elke sandbox klopt en de bridge
// nog steeds afgesloten is, gaan de kaarten aan.
func (r *rehearsal) isolate(ctx context.Context, st *jobs.Step) error {
	s := r.s
	nics, tags := 0, 0
	ups := map[uuid.UUID]map[string]bool{}
	for _, n := range r.def.Nodes {
		sb, ok := r.sandbox(n.NodeID)
		if !ok {
			return fmt.Errorf("%s heeft geen sandbox", n.Hostname)
		}
		cfg, err := r.readBack(ctx, sb)
		if err != nil {
			return fmt.Errorf("%s: config lezen: %w", n.Hostname, err)
		}
		if bad := notIsolatable(cfg); len(bad) > 0 {
			r.addCheck(Check{Name: n.Hostname + ": Afsluiten", Detail: "niet af te sluiten: " + strings.Join(bad, ", ")})
			return fmt.Errorf("%s is niet af te sluiten", n.Hostname)
		}
		desc := drDescription(n, r.def.ClusterName, r.def.Bridge, r.run.ID.String())
		if err := r.configure(ctx, sb, drIsolation(cfg, n.Nets, r.def.Bridge, desc)); err != nil {
			return fmt.Errorf("%s: config wijzigen: %w", n.Hostname, err)
		}
		if cfg, err = r.readBack(ctx, sb); err != nil {
			return fmt.Errorf("%s: config teruglezen: %w", n.Hostname, err)
		}
		if probs := drProblems(cfg, r.def.Bridge, nil); len(probs) > 0 {
			r.addCheck(Check{Name: n.Hostname + ": Afsluiten", Detail: "de config klopt niet: " + strings.Join(probs, "; ")})
			return fmt.Errorf("%s: afsluiting niet te bevestigen: %s", n.Hostname, strings.Join(probs, "; "))
		}
		up := map[string]bool{}
		for k, v := range cfg {
			if !netKey(k) {
				continue
			}
			nics++
			orig, ok := n.Nets[k]
			if ok && !wasDown(orig) {
				up[k] = true
			}
			if t, _ := proxmox.OptionOr(orig, "tag", ""); t != "" {
				tags++
			}
			st.Logf("%s: %s = %s", n.Hostname, k, v)
		}
		ups[n.NodeID] = up
	}
	r.event(fmt.Sprintf("Alle netwerkkaarten los aan %s; config teruggelezen", r.def.Bridge))

	// De bridge nog een keer, vlak voor de kaarten aangaan.
	ifaces, err := r.api.HostNetwork(ctx, r.def.Host)
	if err != nil {
		return fmt.Errorf("het netwerk van %s lezen: %w", r.def.Host, err)
	}
	if prob, fix := bridgeProblem(proxmox.ParseHostNetwork(ifaces), r.def.Host, r.def.Bridge); prob != "" {
		r.def.Fix = fix
		r.saveDefinition(ctx)
		r.addCheck(Check{Name: "Bridge", Code: "bridge_not_isolated", Detail: prob})
		return errors.New(prob)
	}
	res, err := r.api.Resources(ctx)
	if err != nil {
		return err
	}
	others, err := s.onBridge(ctx, r.api, res, r.def.Host, r.def.Bridge, r.mine())
	if err != nil {
		return err
	}
	if len(others) > 0 {
		detail := r.def.Bridge + " op " + r.def.Host + " is niet meer afgesloten: " + strings.Join(others, ", ")
		r.addCheck(Check{Name: "Bridge", Code: "bridge_in_use", Detail: detail})
		return errors.New(detail)
	}
	st.Logf("%s op %s heeft geen poorten, geen adres en geen andere VM", r.def.Bridge, r.def.Host)

	// Nu de kaarten aan, en weer teruglezen.
	for _, n := range r.def.Nodes {
		sb, _ := r.sandbox(n.NodeID)
		change := map[string]string{}
		for k := range ups[n.NodeID] {
			change[k] = drNet(n.Nets[k], r.def.Bridge, false)
		}
		if len(change) > 0 {
			if err := r.configure(ctx, sb, change); err != nil {
				return fmt.Errorf("%s: netwerkkaarten aanzetten: %w", n.Hostname, err)
			}
		}
		cfg, err := r.readBack(ctx, sb)
		if err != nil {
			return fmt.Errorf("%s: config teruglezen: %w", n.Hostname, err)
		}
		if probs := drProblems(cfg, r.def.Bridge, ups[n.NodeID]); len(probs) > 0 {
			r.addCheck(Check{Name: n.Hostname + ": Afsluiten", Detail: "de config klopt niet: " + strings.Join(probs, "; ")})
			return fmt.Errorf("%s: config niet te bevestigen: %s", n.Hostname, strings.Join(probs, "; "))
		}
	}
	detail := fmt.Sprintf("%s aan %s met het MAC-adres van de bron; %s op %s heeft geen poorten en geen adres; "+
		"onboot en protection uit, tag en serienummer %s, pool %s",
		count(nics, "netwerkkaart", "netwerkkaarten"), r.def.Bridge, r.def.Bridge, r.def.Host, sandboxTag, proxmox.SandboxPool)
	if tags > 0 {
		detail += fmt.Sprintf("; %s weggelaten, want de bridge heeft geen poorten", count(tags, "VLAN-tag", "VLAN-tags"))
	}
	r.addCheck(Check{Name: "Afsluiten", OK: true, Detail: detail})
	r.event("Netwerkkaarten aan op " + r.def.Bridge)
	return nil
}

// boot start alle sandboxes tegelijk en wacht tot elke guest agent
// antwoordt.
func (r *rehearsal) boot(ctx context.Context, st *jobs.Step) error {
	s := r.s
	r.watch(ctx, st)
	t0 := s.Now()
	for _, n := range r.def.Nodes {
		sb, _ := r.sandbox(n.NodeID)
		vm, err := s.guard(ctx, sb.ID)
		if err != nil {
			return err
		}
		upid, err := vm.power(ctx, "start")
		if err == nil {
			err = s.pve.WaitTask(ctx, vm.api, upid, nil)
		}
		if err != nil {
			if ctx.Err() == nil {
				r.addCheck(Check{Name: n.Hostname + ": Opstarten", Detail: "de VM start niet: " + err.Error()})
			}
			return fmt.Errorf("%s starten: %w", n.Hostname, err)
		}
		st.Logf("%s: VM %d gestart", n.Hostname, sb.Vmid)
	}
	r.event("Alle VM's gestart")
	_ = st.Flush(ctx)

	checks := make([]Check, len(r.def.Nodes))
	var wg sync.WaitGroup
	for i, n := range r.def.Nodes {
		wg.Go(func() { checks[i] = r.waitAgent(ctx, st, n, t0) })
	}
	wg.Wait()
	if ctx.Err() != nil {
		return context.Cause(ctx)
	}
	var failed []string
	var slowest time.Duration
	for i, c := range checks {
		if !c.OK && !c.Warning {
			failed = append(failed, r.def.Nodes[i].Hostname)
		}
		if b := r.m.Nodes[i].BootSeconds; b != nil {
			slowest = max(slowest, seconds(*b))
		}
	}
	r.addCheck(checks...)
	if len(failed) > 0 {
		return errors.New(strings.Join(failed, ", ") + " kwam niet op")
	}
	d := s.Now().Sub(t0)
	r.m.BootSeconds = secs(d)
	r.addCheck(Check{Name: "Opstarten", OK: true, Detail: fmt.Sprintf("alle %s draaien na %s", count(len(r.def.Nodes), "VM", "VM's"), duration(d))})
	return nil
}

// waitAgent wacht tot de guest agent van een sandbox antwoordt, of tot een
// VM zonder guest agent een tijd blijft draaien.
func (r *rehearsal) waitAgent(ctx context.Context, st *jobs.Step, n RehearsalNode, t0 time.Time) Check {
	s := r.s
	name := n.Hostname + ": Opstarten"
	sb, _ := r.sandbox(n.NodeID)
	if !n.Agent {
		if err := sleep(ctx, s.NoAgentWait); err != nil {
			return Check{Name: name, Detail: err.Error()}
		}
		vm, err := s.guard(ctx, sb.ID)
		if err != nil {
			return Check{Name: name, Detail: err.Error()}
		}
		if vm.res.Status != "running" {
			return Check{Name: name, Detail: "de VM stopte kort na het starten"}
		}
		return Check{Name: name, Warning: true, Detail: "de VM draait, maar heeft geen guest agent; in deze VM is niets gecontroleerd"}
	}
	for {
		vm, err := s.guard(ctx, sb.ID)
		if err != nil {
			return Check{Name: name, Detail: err.Error()}
		}
		if vm.res.Status != "running" {
			return Check{Name: name, Detail: "de VM stopte tijdens het opstarten"}
		}
		pctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		err = vm.agentPing(pctx)
		cancel()
		if err == nil {
			break
		}
		if ctx.Err() != nil {
			return Check{Name: name, Detail: context.Cause(ctx).Error()}
		}
		if s.Now().Sub(t0) > s.BootTimeout {
			return Check{Name: name, Detail: "de guest agent antwoordde niet binnen " + duration(s.BootTimeout)}
		}
		if err := sleep(ctx, s.Poll); err != nil {
			return Check{Name: name, Detail: err.Error()}
		}
	}
	d := s.Now().Sub(t0)
	r.mu.Lock()
	r.node(n.NodeID).BootSeconds = secs(d)
	r.mu.Unlock()
	st.Logf("%s: de guest agent antwoordt na %s", n.Hostname, duration(d))
	r.event(fmt.Sprintf("%s: guest agent antwoordt na %s", n.Hostname, duration(d)))
	return Check{Name: name, OK: true, Detail: "de guest agent antwoordt na " + duration(d)}
}

// nodeVerify is de uitkomst van cf-agent verify in één sandbox.
type nodeVerify struct {
	n      RehearsalNode
	sb     store.BackupSandbox
	req    protocol.VerifyRequest
	res    *protocol.VerifyResult
	checks []Check
}

// addresses is true als de agent zijn adressen kan melden.
func (v nodeVerify) addresses() bool {
	return v.res != nil && v.res.ProtocolVersion >= protocol.RehearsalSince
}

// check stelt in elke sandbox de diepe controle, leidt uit de adressen af
// wie elk VIP heeft, en vraagt vanaf één node het VIP en de andere nodes op.
func (r *rehearsal) check(ctx context.Context, st *jobs.Step) error {
	s := r.s
	t0 := s.Now()
	vs := make([]nodeVerify, len(r.def.Nodes))
	var wg sync.WaitGroup
	for i, n := range r.def.Nodes {
		sb, _ := r.sandbox(n.NodeID)
		vs[i] = nodeVerify{n: n, sb: sb}
		if !n.Agent {
			continue
		}
		wg.Go(func() { r.verifyNode(ctx, st, &vs[i]) })
	}
	wg.Wait()
	if ctx.Err() != nil {
		return context.Cause(ctx)
	}
	for _, v := range vs {
		r.addCheck(v.checks...)
	}
	r.vipHolders(ctx, st, vs)
	if ctx.Err() != nil {
		return context.Cause(ctx)
	}
	r.probes(ctx, st, vs)
	if ctx.Err() != nil {
		return context.Cause(ctx)
	}
	now := s.Now()
	r.m.CheckSeconds = secs(now.Sub(t0))
	r.mu.Lock()
	ok := !slices.ContainsFunc(r.checks, func(c Check) bool { return !c.OK && !c.Warning })
	r.mu.Unlock()
	if ok {
		r.m.RTOSeconds = secs(now.Sub(r.t0))
		r.event("Het cluster werkt weer na " + duration(now.Sub(r.t0)))
	} else {
		r.event("Controles klaar; niet alles werkt")
	}
	return nil
}

// verifyNode doet de diepe controle in één sandbox, met de adressen.
func (r *rehearsal) verifyNode(ctx context.Context, st *jobs.Step, v *nodeVerify) {
	s := r.s
	prefix := v.n.Hostname + ": "
	req, notes, err := s.verifyRequest(ctx, &r.def.ClusterID, v.n.NodeID)
	for _, note := range notes {
		st.Logf("%s%s", prefix, note)
	}
	if err != nil {
		v.checks = append(v.checks, Check{Name: prefix + nameVerify, Warning: true, Detail: "de aanvraag voor cf-agent verify is niet te maken: " + err.Error()})
		return
	}
	req.Addresses = true
	v.req = req
	st.Logf("%scf-agent verify: %s", prefix, describe(req))
	res, d, problem := s.agentVerify(ctx, v.sb.ID, req, st, prefix)
	if problem != nil {
		problem.Name = prefix + problem.Name
		v.checks = append(v.checks, *problem)
		return
	}
	v.res = &res
	r.mu.Lock()
	nr := r.node(v.n.NodeID)
	nr.AgentVersion, nr.Addresses = res.AgentVersion, nonNil(res.Addresses)
	r.mu.Unlock()
	for _, c := range res.Checks {
		ch := deepCheck(c)
		ch.Name = prefix + ch.Name
		v.checks = append(v.checks, ch)
	}
	st.Logf("%scf-agent %s antwoordde na %s: %s", prefix, res.AgentVersion, duration(d), count(len(res.Checks), "controle", "controles"))
}

// vipHolders zoekt per VIP wie het in de sandbox heeft. keepalived kan na
// het opstarten nog kiezen; zolang een VIP geen houder heeft, vraagt de
// controle de adressen opnieuw, tot VIPWait.
func (r *rehearsal) vipHolders(ctx context.Context, st *jobs.Step, vs []nodeVerify) {
	s := r.s
	if len(r.def.VIPs) == 0 {
		return
	}
	var able, unknown []string
	for _, v := range vs {
		if v.addresses() {
			able = append(able, v.n.Hostname)
		} else {
			unknown = append(unknown, v.n.Hostname)
		}
	}
	if len(able) == 0 {
		for i, vip := range r.def.VIPs {
			r.m.VIPs[i].Checked = false
			r.addCheck(Check{Name: "VIP " + vip.Address, Warning: true, Code: CodeAgentTooOld, Detail: fmt.Sprintf(
				"niet gecontroleerd: geen node meldde zijn adressen; daarvoor is cf-agent met protocol %d in de back-up nodig", protocol.RehearsalSince)})
		}
		return
	}
	holders := func() [][]string {
		out := make([][]string, len(r.def.VIPs))
		for i, vip := range r.def.VIPs {
			out[i] = []string{}
			for _, v := range vs {
				if v.addresses() && slices.Contains(v.res.Addresses, vip.Address) {
					out[i] = append(out[i], v.n.Hostname)
				}
			}
		}
		return out
	}
	t0 := s.Now()
	h := holders()
	for slices.ContainsFunc(h, func(x []string) bool { return len(x) == 0 }) && s.Now().Sub(t0) < s.VIPWait {
		if sleep(ctx, s.Poll) != nil {
			return
		}
		st.Logf("nog geen houder voor elk VIP; de adressen opnieuw vragen")
		var wg sync.WaitGroup
		for i := range vs {
			if !vs[i].addresses() {
				continue
			}
			wg.Go(func() {
				res, _, problem := s.agentVerify(ctx, vs[i].sb.ID, protocol.VerifyRequest{Services: []string{}, Addresses: true}, st, vs[i].n.Hostname+": ")
				if problem == nil {
					r.mu.Lock()
					vs[i].res.Addresses = res.Addresses
					r.node(vs[i].n.NodeID).Addresses = nonNil(res.Addresses)
					r.mu.Unlock()
				}
			})
		}
		wg.Wait()
		h = holders()
	}
	for i, vip := range r.def.VIPs {
		r.m.VIPs[i].Checked, r.m.VIPs[i].Holders = true, h[i]
		c := Check{Name: "VIP " + vip.Address}
		missing := ""
		if len(unknown) > 0 {
			missing = "; " + join(strings.Join(unknown, ", ")) + " meldde zijn adressen niet"
		}
		switch n := len(h[i]); {
		case n == 1 && len(unknown) == 0:
			c.OK, c.Detail = true, "één houder: "+h[i][0]
		case n == 1:
			c.Warning, c.Detail = true, "houder "+h[i][0]+", maar een tweede houder is niet uit te sluiten"+missing
		case n == 0 && len(unknown) > 0:
			c.Warning, c.Detail = true, "geen van de nodes die hun adressen meldden, heeft het VIP"+missing
		case n == 0:
			c.Detail = fmt.Sprintf("geen node heeft het VIP na %s; draait keepalived in de back-up?", duration(s.VIPWait))
		default:
			c.Detail = strings.Join(h[i], " en ") + " hebben allebei het VIP: split-brain in de sandbox"
		}
		r.addCheck(c)
		st.Logf("VIP %s: %s", vip.Address, c.Detail)
	}
}

// probes vraagt vanaf één node elk VIP op met zijn probe, en kijkt of elke
// andere node antwoordt.
func (r *rehearsal) probes(ctx context.Context, st *jobs.Step, vs []nodeVerify) {
	s := r.s
	// Liefst een node zonder VIP, zodat het VIP echt over het netwerk gaat.
	from := -1
	for i, v := range vs {
		if !v.addresses() {
			continue
		}
		if !slices.ContainsFunc(r.m.VIPs, func(x RehearsalVIPResult) bool { return slices.Contains(x.Holders, v.n.Hostname) }) {
			from = i
			break
		}
		if from < 0 {
			from = i
		}
	}
	if from < 0 {
		if len(vs) > 1 || len(r.def.VIPs) > 0 {
			r.addCheck(Check{Name: "Verbindingen", Warning: true, Code: CodeAgentTooOld, Detail: fmt.Sprintf(
				"niet gecontroleerd: daarvoor is cf-agent met protocol %d in de back-up nodig", protocol.RehearsalSince)})
		}
		return
	}
	src := vs[from]
	var probes []protocol.VerifyProbe
	var vipIdx []int
	for i, vip := range r.def.VIPs {
		if len(r.m.VIPs[i].Holders) == 0 && r.m.VIPs[i].Checked {
			continue
		}
		probes = append(probes, toProbe("VIP "+vip.Address, vip.Address, vip))
		vipIdx = append(vipIdx, i)
	}
	var skipped []string
	for _, v := range vs {
		if v.n.NodeID == src.n.NodeID {
			continue
		}
		if v.n.Address == "" {
			skipped = append(skipped, v.n.Hostname+" heeft geen adres in ClusterForge")
			continue
		}
		// Alleen bereikbaarheid: ook een geweigerde verbinding op poort 22
		// zegt dat de node antwoordt.
		probes = append(probes, protocol.VerifyProbe{Name: v.n.Hostname, Host: v.n.Address, Port: 22, Reach: true})
	}
	if len(skipped) > 0 {
		r.addCheck(Check{Name: "Verbindingen", Warning: true, Detail: "niet alles opgevraagd: " + strings.Join(skipped, "; ")})
	}
	if len(probes) == 0 {
		return
	}
	if len(probes) > 16 {
		r.addCheck(Check{Name: "Verbindingen", Warning: true, Detail: fmt.Sprintf("alleen de eerste 16 van %d verbindingen opgevraagd", len(probes))})
		probes = probes[:16]
	}
	prefix := "Van " + src.n.Hostname + " naar "
	st.Logf("vanaf %s: %s", src.n.Hostname, count(len(probes), "verbinding", "verbindingen"))
	res, _, problem := s.agentVerify(ctx, src.sb.ID, protocol.VerifyRequest{Services: []string{}, Probes: probes}, st, src.n.Hostname+": ")
	if problem != nil {
		problem.Name = prefix + "het VIP en de nodes"
		r.addCheck(*problem)
		return
	}
	for _, c := range res.Checks {
		if c.Kind != "probe" {
			continue
		}
		ch := deepCheck(c)
		ch.Name = prefix + c.Name
		r.addCheck(ch)
		for _, i := range vipIdx {
			if c.Name == "VIP "+r.def.VIPs[i].Address {
				ok := c.Status == protocol.VerifyOK
				r.m.VIPs[i].ProbeOK = &ok
			}
		}
	}
}

// toProbe maakt van de probe van een VIP een probe van de agent: HTTP op
// poort 80 of alleen TCP.
func toProbe(name, host string, vip RehearsalVIP) protocol.VerifyProbe {
	p := protocol.VerifyProbe{Name: name, Host: host}
	switch {
	case vip.Probe.TCP != nil:
		p.Port = vip.Probe.TCP.Port
	case vip.Probe.HTTP != nil:
		p.Port, p.Path, p.Expect = 80, vip.Probe.HTTP.Path, vip.Probe.HTTP.Expect
	default:
		p.Port, p.Path, p.Expect = 80, "/", 200
	}
	return p
}

// watch bewaakt het echte cluster zolang de sandboxes kunnen draaien.
func (r *rehearsal) watch(ctx context.Context, st *jobs.Step) {
	s := r.s
	keys := map[string]string{}
	if s.conns != nil {
		for _, n := range r.def.Nodes {
			if a, err := s.q.GetActiveAgentByNode(ctx, n.NodeID); err == nil {
				keys[a.NkeyPublic] = n.Hostname
			}
		}
	}
	st.Logf("bewaking van %s: agentverbindingen van %s, de VIP's en split-brain, elke %s",
		r.def.ClusterName, count(len(keys), "node", "nodes"), duration(s.ConnPoll))
	wctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	done := make(chan struct{})
	r.stopWatch = func() {
		cancel()
		<-done
	}
	go func() {
		defer close(done)
		t := time.NewTicker(s.ConnPoll)
		defer t.Stop()
		for {
			if reason := r.danger(wctx, keys); reason != "" {
				r.abort(reason)
				return
			}
			select {
			case <-wctx.Done():
				return
			case <-t.C:
			}
		}
	}()
}

func (r *rehearsal) unwatch() {
	if r.stopWatch != nil {
		r.stopWatch()
		r.stopWatch = nil
	}
}

// danger zegt wat er aan het echte cluster niet klopt, of niets.
func (r *rehearsal) danger(ctx context.Context, keys map[string]string) string {
	s := r.s
	for _, key := range slices.Sorted(maps.Keys(keys)) {
		if n, err := s.conns.Connections(key); err == nil && n > 1 {
			return fmt.Sprintf("tweede agentverbinding met de sleutel van %s", keys[key])
		}
	}
	if c, err := s.q.GetCluster(ctx, r.def.ClusterID); err == nil && c.Status == "split_brain" {
		return c.Name + " kreeg split-brain"
	}
	owners, err := s.q.ListClusterVIPOwners(ctx, r.def.ClusterID)
	if err != nil {
		return ""
	}
	for _, v := range r.def.VIPs {
		if v.OwnerID == nil {
			continue
		}
		for _, o := range owners {
			if o.Address.String() == v.Address && o.OwnerNodeID != nil && *o.OwnerNodeID != *v.OwnerID {
				return "het echte VIP " + v.Address + " verhuisde naar een andere node"
			}
		}
	}
	return ""
}

// abort breekt de repetitie af en zet alle sandboxes meteen hard uit.
func (r *rehearsal) abort(reason string) {
	s := r.s
	r.mu.Lock()
	if r.m.Aborted != "" {
		r.mu.Unlock()
		return
	}
	r.m.Aborted = reason
	r.mu.Unlock()
	s.log.Warn("DR-repetitie afgebroken; sandboxes gaan hard uit", "cluster", r.def.ClusterName, "reason", reason)
	r.event("Afgebroken: " + reason + "; alle sandboxes hard uitgezet")
	r.interrupt(errAborted)
	r.emergencyStop()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.ev.Write(ctx, nil, rehearsalEvent(r.run, r.def, "dr.aborted", events.System(), map[string]any{"reason": reason})); err != nil {
		s.log.Warn("event schrijven mislukt", "err", err)
	}
}

// emergencyStop zet alle sandboxes tegelijk hard uit, los van de taak.
func (r *rehearsal) emergencyStop() {
	r.mu.Lock()
	sbs := slices.Collect(maps.Values(r.sbs))
	r.mu.Unlock()
	var wg sync.WaitGroup
	for _, sb := range sbs {
		wg.Go(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			vm, err := r.s.guard(ctx, sb.ID)
			if err != nil || vm.res.Status != "running" {
				return
			}
			if _, err := vm.power(ctx, "stop"); err != nil {
				r.s.log.Warn("sandbox hard uitzetten mislukt", "vmid", sb.Vmid, "err", err)
			}
		})
	}
	wg.Wait()
}

// cleanup verwijdert elke sandbox van deze run.
func (r *rehearsal) cleanup(ctx context.Context, st *jobs.Step) error {
	s := r.s
	rows, err := s.q.ListRunSandboxes(ctx, &r.run.ID)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		st.Logf("geen sandboxes; niets op te ruimen")
		return nil
	}
	t0 := s.Now()
	var gone, left []string
	var last *time.Time
	var errs []error
	for _, row := range rows {
		sb := row
		if liveState(row.State) {
			sb, err = s.destroySandbox(ctx, row.ID, destroyOpts{lockWait: s.LockWait, step: st, own: true})
			if sb.ID == uuid.Nil {
				sb = row
			}
		}
		r.mu.Lock()
		for i := range r.m.Nodes {
			if r.m.Nodes[i].SandboxVMID == int(sb.Vmid) || sb.SourceNodeID != nil && r.m.Nodes[i].NodeID == *sb.SourceNodeID {
				r.m.Nodes[i].SandboxVMID = int(sb.Vmid)
				r.m.Nodes[i].Destroyed = sb.State == sbDestroyed || sb.State == sbNone
			}
		}
		r.mu.Unlock()
		switch {
		case liveState(sb.State):
			left = append(left, strconv.Itoa(int(sb.Vmid)))
			if err != nil {
				errs = append(errs, fmt.Errorf("VM %d: %w", sb.Vmid, err))
			}
		case sb.State == sbDestroyed:
			gone = append(gone, strconv.Itoa(int(sb.Vmid)))
			if sb.DestroyedAt != nil && (last == nil || sb.DestroyedAt.After(*last)) {
				last = sb.DestroyedAt
			}
		}
	}
	r.m.CleanupSeconds = secs(s.Now().Sub(t0))
	if len(left) > 0 {
		r.addCheck(Check{Name: "Opruimen", Warning: true, Detail: fmt.Sprintf("%s nog niet verwijderd (%v); de opruimer probeert het elke %s opnieuw",
			count(len(left), "sandbox-VM", "sandbox-VM's")+" "+strings.Join(left, ", "), errors.Join(errs...), duration(s.CleanEvery))})
		return errors.Join(errs...)
	}
	r.m.DestroyedAt = last
	if len(gone) > 0 {
		r.addCheck(Check{Name: "Opruimen", OK: true, Detail: count(len(gone), "sandbox-VM", "sandbox-VM's") + " verwijderd: " + strings.Join(gone, ", ")})
		r.event("Alle sandboxes verwijderd")
	}
	return nil
}

// finish schrijft het rapport.
func (r *rehearsal) finish(ctx context.Context, runErr error) error {
	s := r.s
	r.m.TotalSeconds = secs(s.Now().Sub(r.start))
	result, summary := r.verdict(ctx, runErr)
	ctx = context.WithoutCancel(ctx)
	r.mu.Lock()
	checks, _ := json.Marshal(nonNil(r.checks))
	timeline, _ := json.Marshal(nonNil(r.timeline))
	meas, _ := json.Marshal(r.m)
	r.mu.Unlock()
	run, err := s.q.FinishTestRun(ctx, store.FinishTestRunParams{
		ID: r.run.ID, Result: &result, Summary: summary, Checks: checks, Timeline: timeline, Measurements: meas,
	})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if err == nil {
		s.rehearsalFinishedEvent(ctx, run, r.def, r.m)
	}
	s.Kick()
	s.kickCleaner()
	switch result {
	case "canceled":
		return jobs.ErrCanceled
	case "error":
		return errors.New(summary)
	}
	return nil
}

// verdict maakt de uitkomst en de samenvatting. RTO en RPO boven het doel
// zijn een waarschuwing: de repetitie lukte, maar het herstel duurt te lang
// of verliest te veel.
func (r *rehearsal) verdict(ctx context.Context, runErr error) (string, string) {
	s := r.s
	d := r.def
	head := d.ClusterName + " op " + d.Bridge
	var failed, warnings []string
	for _, c := range r.checks {
		switch {
		case c.OK:
		case c.Warning:
			warnings = append(warnings, c.Name+": "+c.Detail)
		default:
			failed = append(failed, c.Name+": "+c.Detail)
		}
	}
	tail := r.cleanupText()
	cause := context.Cause(ctx)
	switch {
	case r.planFailed:
		return "error", precheckSummary(head, strings.Join(failed, "; "), d.Fix)
	case errors.Is(runErr, errRestarted):
		return "error", join("ERROR: "+head+": onderbroken door herstart van de server.", tail)
	case r.m.Aborted != "" || errors.Is(runErr, errAborted):
		return "error", join("ERROR: "+head+": afgebroken, want "+or(r.m.Aborted, "de bewaking sloeg aan")+"; alle sandboxes zijn hard uitgezet.", tail)
	case jobs.Canceled(ctx) || errors.Is(runErr, jobs.ErrCanceled):
		return "canceled", join(head+": afgebroken.", tail)
	case errors.Is(cause, errRehearsalLimit) || errors.Is(runErr, errRehearsalLimit):
		return "error", join(fmt.Sprintf("ERROR: %s: afgebroken, de repetitie duurde langer dan %s.", head, duration(s.RehearsalLimit)), tail)
	case len(failed) > 0:
		return "fail", join(fmt.Sprintf("FAIL: %s: het cluster kwam niet werkend terug: %s mislukt. %s.", head,
			count(len(failed), "controle", "controles"), strings.Join(failed, "; ")), r.rpoText(), tail)
	case runErr != nil:
		return "error", join(fmt.Sprintf("ERROR: %s: repetitie mislukt: %v.", head, runErr), tail)
	}
	var missed []string
	if r.m.RTOSeconds != nil && d.RTOTargetMinutes > 0 {
		met := seconds(*r.m.RTOSeconds) <= time.Duration(d.RTOTargetMinutes)*time.Minute
		r.m.RTOMet = &met
		if !met {
			missed = append(missed, fmt.Sprintf("RTO %s boven het doel van %s", duration(seconds(*r.m.RTOSeconds)), minutes(d.RTOTargetMinutes)))
		}
	}
	if r.m.RPOSeconds != nil && d.RPOTargetHours > 0 {
		met := seconds(*r.m.RPOSeconds) <= time.Duration(d.RPOTargetHours)*time.Hour
		r.m.RPOMet = &met
		if !met {
			missed = append(missed, fmt.Sprintf("RPO %s boven het doel van %d u", duration(seconds(*r.m.RPOSeconds)), d.RPOTargetHours))
		}
	}
	parts := []string{r.rtoText(), r.rpoText()}
	var found []string
	for _, v := range r.m.VIPs {
		switch {
		case len(v.Holders) == 1 && v.ProbeOK != nil && *v.ProbeOK:
			found = append(found, fmt.Sprintf("VIP %s bij %s, %s antwoordt", v.Address, v.Holders[0], probeWord(v.Probe)))
		case len(v.Holders) == 1:
			found = append(found, fmt.Sprintf("VIP %s bij %s", v.Address, v.Holders[0]))
		}
	}
	if len(found) > 0 {
		parts = append(parts, strings.Join(found, "; ")+".")
	}
	parts = append(parts, tail)
	if len(missed) > 0 || len(warnings) > 0 {
		return "warning", join("WARNING: "+head+": het cluster kwam werkend terug, maar "+strings.Join(append(missed, warnings...), "; ")+".", parts...)
	}
	return "pass", join("PASS: "+head+": het cluster kwam werkend terug.", parts...)
}

func probeWord(p string) string {
	if strings.HasPrefix(p, "HTTP") {
		return "HTTP"
	}
	return "de poort"
}

func minutes(m int) string {
	if m%60 == 0 && m >= 60 {
		return fmt.Sprintf("%d u", m/60)
	}
	return fmt.Sprintf("%d min", m)
}

func (r *rehearsal) rtoText() string {
	if r.m.RTOSeconds == nil {
		return ""
	}
	t := "RTO " + duration(seconds(*r.m.RTOSeconds))
	if r.def.RTOTargetMinutes > 0 {
		t += " (doel " + minutes(r.def.RTOTargetMinutes) + ")"
	}
	return t + "."
}

func (r *rehearsal) rpoText() string {
	if r.m.RPOSeconds == nil {
		return ""
	}
	t := "RPO " + duration(seconds(*r.m.RPOSeconds))
	if r.def.RPOTargetHours > 0 {
		t += fmt.Sprintf(" (doel %d u)", r.def.RPOTargetHours)
	}
	return t + "."
}

func (r *rehearsal) cleanupText() string {
	n, left := 0, 0
	for _, x := range r.m.Nodes {
		switch {
		case x.SandboxVMID == 0:
		case x.Destroyed:
			n++
		default:
			left++
		}
	}
	switch {
	case left > 0:
		return fmt.Sprintf("%s nog niet verwijderd; de opruimer probeert het opnieuw.", count(left, "sandbox-VM is", "sandbox-VM's zijn"))
	case n > 0 && r.m.DestroyedAt != nil:
		return fmt.Sprintf("%s verwijderd om %s.", count(n, "sandbox-VM", "sandbox-VM's"), r.m.DestroyedAt.Local().Format("15:04"))
	case n > 0:
		return count(n, "sandbox-VM", "sandbox-VM's") + " verwijderd."
	}
	return ""
}

func (s *Service) rehearsalFinishedEvent(ctx context.Context, run store.TestRun, d RehearsalDefinition, m RehearsalMeasurements) {
	extra := map[string]any{"result": deref(run.Result), "summary": run.Summary}
	if m.RTOSeconds != nil {
		extra["rto_seconds"] = *m.RTOSeconds
	}
	if m.RPOSeconds != nil {
		extra["rpo_seconds"] = *m.RPOSeconds
	}
	if err := s.ev.Write(ctx, nil, rehearsalEvent(run, d, "dr.finished", events.System(), extra)); err != nil {
		s.log.Warn("event schrijven mislukt", "err", err)
	}
}

// rehearsalFinished rondt een run af waarvan de taak stopte zonder rapport
// en geeft de sandboxes aan de opruimer.
func (s *Service) rehearsalFinished(ctx context.Context, j store.Job) {
	defer s.kickCleaner()
	run, err := s.q.GetTestRunByJob(ctx, &j.ID)
	if err != nil || run.Result != nil {
		return
	}
	var d RehearsalDefinition
	_ = json.Unmarshal(run.Definition, &d)
	result, summary := "error", "ERROR: "+d.ClusterName+": de taak stopte zonder rapport"
	if j.Error != "" {
		summary += ": " + j.Error
	}
	if j.Status == store.JobStatusCanceled {
		result, summary = "canceled", d.ClusterName+": afgebroken voor de start; er is niets veranderd"
	}
	done, err := s.q.FinishTestRun(ctx, store.FinishTestRunParams{
		ID: run.ID, Result: &result, Summary: summary, Checks: run.Checks, Timeline: []byte("[]"), Measurements: []byte("{}"),
	})
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			s.log.Error("DR-repetitie afronden", "run", run.ID, "err", err)
		}
		return
	}
	s.rehearsalFinishedEvent(ctx, done, d, RehearsalMeasurements{})
	s.Kick()
}
