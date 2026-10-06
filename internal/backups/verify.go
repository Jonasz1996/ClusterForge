package backups

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/jobs"
	"github.com/Jonasz1996/clusterforge/internal/proxmox"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

// KindVerify is de taak die een back-up terugzet in een sandbox, opstart,
// controleert en weer verwijdert. Niet Retryable en niet te hervatten: na een
// herstart van de server ruimt hij alleen op.
const KindVerify = "backup.verify"

// De zes stappen, altijd in deze volgorde.
const (
	stepChoose  = "Kiezen"
	stepRestore = "Terugzetten"
	stepIsolate = "Isoleren"
	stepStart   = "Starten"
	stepCheck   = "Controleren"
	stepCleanup = "Opruimen"
)

var (
	errRestarted        = errors.New("onderbroken door herstart van de server")
	errJobLimit         = errors.New("de controle duurde te lang")
	errSecondConnection = errors.New("tweede verbinding")
)

// Connections telt de open NATS-verbindingen met de sleutel van een agent.
type Connections interface {
	Connections(nkeyPublic string) (int, error)
}

// EnableVerify zet de back-upcontrole aan.
func (s *Service) EnableVerify(runner *jobs.Runner, conns Connections) {
	s.runner, s.conns = runner, conns
	// Niet Retryable: een nieuwe controle start vanuit de actuele toestand.
	runner.Register(KindVerify, s.runVerify)
	runner.OnFinished(KindVerify, s.verifyFinished)
}

// Definition is de invoer van een controle, in test_runs.definition.
type Definition struct {
	Volid          string    `json:"volid"`
	BackupTime     time.Time `json:"backup_time"`
	Size           int64     `json:"size"`
	Format         string    `json:"format"`
	BackupStorage  string    `json:"backup_storage"`
	ConnectionID   uuid.UUID `json:"connection_id"`
	ConnectionName string    `json:"connection_name"`
	SourceVMID     int       `json:"source_vmid"`
	GuestName      string    `json:"guest_name"`
	// Chosen is true als iemand deze back-up koos; anders de nieuwste.
	Chosen bool `json:"chosen"`
}

// Measurements staan in test_runs.measurements. Hersteltijd is terugzetten
// plus opstarten.
type Measurements struct {
	RestoreSeconds *float64   `json:"restore_seconds,omitempty"`
	BootSeconds    *float64   `json:"boot_seconds,omitempty"`
	CheckSeconds   *float64   `json:"check_seconds,omitempty"`
	CleanupSeconds *float64   `json:"cleanup_seconds,omitempty"`
	TotalSeconds   *float64   `json:"total_seconds,omitempty"`
	Host           string     `json:"host,omitempty"`
	Storage        string     `json:"storage,omitempty"`
	SandboxVMID    int        `json:"sandbox_vmid,omitempty"`
	Hostname       string     `json:"hostname,omitempty"`
	OS             string     `json:"os,omitempty"`
	Filesystems    int        `json:"filesystems,omitempty"`
	DestroyedAt    *time.Time `json:"destroyed_at,omitempty"`
}

// Check is één controle in het rapport.
type Check struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
	// Warning: niet in orde, maar geen reden om de back-up af te keuren.
	Warning bool `json:"warning,omitempty"`
}

// TimelineEvent is een moment in de controle, in ms na de start van de taak.
type TimelineEvent struct {
	TMS  int64  `json:"t_ms"`
	Kind string `json:"kind"`
	Text string `json:"text"`
}

type verifyParams struct {
	RunID uuid.UUID `json:"run_id"`
}

// ConflictError is een controle of opruiming die nu niet kan.
type ConflictError struct {
	Code string
	Msg  string
}

func (e ConflictError) Error() string { return e.Msg }

// StartVerify zet een controle van de nieuwste back-up van de VM van een
// node in de wachtrij, of van volid als die gegeven is.
func (s *Service) StartVerify(ctx context.Context, actor events.Actor, nodeID uuid.UUID, volid string) (store.TestRun, error) {
	if s.runner == nil {
		return store.TestRun{}, ConflictError{"conflict", "de back-upcontrole staat uit op deze server"}
	}
	n, err := s.q.GetVerifyNode(ctx, nodeID)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.TestRun{}, ErrNotFound
	}
	if err != nil {
		return store.TestRun{}, err
	}
	switch {
	case n.ProxmoxID == nil || n.PveVmid == nil:
		return store.TestRun{}, ConflictError{"conflict", "deze node is niet aan een VM in Proxmox gekoppeld"}
	case n.LastError != nil && *n.LastError != "":
		return store.TestRun{}, ConflictError{"conflict", "Proxmox is niet bereikbaar: " + *n.LastError}
	case deref(n.GuestType) == "lxc":
		return store.TestRun{}, ConflictError{"conflict", "een LXC-container heeft geen guest agent; die krijgt alleen versheid en dekking, geen controle in een sandbox"}
	}
	var b store.ProxmoxBackup
	volid = strings.TrimSpace(volid)
	if volid != "" {
		b, err = s.q.GetBackup(ctx, store.GetBackupParams{ConnectionID: *n.ProxmoxID, Volid: volid})
		if errors.Is(err, pgx.ErrNoRows) || err == nil && b.Vmid != *n.PveVmid {
			return store.TestRun{}, ValidationError{"deze back-up hoort niet bij de VM van " + n.Hostname}
		}
	} else {
		b, err = s.q.GetLatestGuestBackup(ctx, store.GetLatestGuestBackupParams{ConnectionID: *n.ProxmoxID, Vmid: *n.PveVmid})
		if errors.Is(err, pgx.ErrNoRows) {
			return store.TestRun{}, ConflictError{"conflict", "Proxmox heeft geen back-up van deze VM"}
		}
	}
	if err != nil {
		return store.TestRun{}, err
	}
	if b.GuestType == "lxc" {
		return store.TestRun{}, ConflictError{"conflict", "dit is een back-up van een LXC-container; die krijgt alleen versheid en dekking"}
	}
	def := Definition{
		Volid: b.Volid, BackupTime: b.Ctime, Size: b.SizeBytes, Format: b.Format, BackupStorage: b.Storage,
		ConnectionID: *n.ProxmoxID, ConnectionName: deref(n.ConnectionName), SourceVMID: int(*n.PveVmid),
		GuestName: deref(n.GuestName), Chosen: volid != "",
	}
	defJSON, err := json.Marshal(def)
	if err != nil {
		return store.TestRun{}, err
	}
	var run store.TestRun
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		var err error
		run, err = q.InsertTestRun(ctx, store.InsertTestRunParams{
			Kind: KindVerify, ClusterID: n.ClusterID, NodeID: &n.ID, Hostname: n.Hostname,
			Definition: defJSON, Checks: []byte("[]"), RequestedBy: userOf(actor),
		})
		if err != nil {
			return err
		}
		// Geen clusterslot: de controle raakt de productie-VM niet aan. Het
		// testslot houdt het bij één sandbox tegelijk.
		j, err := s.runner.EnqueueTx(ctx, q, jobs.Spec{
			Kind: KindVerify, Title: "Back-upcontrole: " + n.Hostname, Params: verifyParams{RunID: run.ID},
			ClusterID: n.ClusterID, NodeID: &n.ID, ProxmoxID: n.ProxmoxID, Actor: actor,
		})
		if err != nil {
			return err
		}
		run.JobID = &j.ID
		return q.SetTestRunJob(ctx, store.SetTestRunJobParams{ID: run.ID, JobID: &j.ID})
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.ConstraintName == "jobs_test_slot" {
		return store.TestRun{}, ConflictError{"busy", "er loopt al een failovertest of back-upcontrole; er loopt er hoogstens één tegelijk"}
	}
	if err != nil {
		return store.TestRun{}, err
	}
	s.runner.Kick()
	return run, nil
}

// verify is één uitvoering van backup.verify.
type verify struct {
	s     *Service
	j     *jobs.Job
	run   store.TestRun
	def   Definition
	start time.Time

	api  proxmox.API
	cfg  map[string]string
	host string
	stor string
	sb   *store.BackupSandbox

	mu       sync.Mutex
	checks   []Check
	timeline []TimelineEvent
	m        Measurements
	skipped  string
	// interrupt breekt Starten en Controleren af als de bewaking een tweede
	// verbinding ziet.
	interrupt context.CancelCauseFunc
	stopWatch func()
	maxConns  int
	watched   bool
}

func (s *Service) runVerify(ctx context.Context, j *jobs.Job) error {
	var p verifyParams
	if err := j.Decode(&p); err != nil {
		return err
	}
	row, err := s.q.GetTestRun(ctx, p.RunID)
	if err != nil {
		return fmt.Errorf("run %s: %w", p.RunID, err)
	}
	v := &verify{s: s, j: j, run: row.TestRun, start: s.Now()}
	if err := json.Unmarshal(row.TestRun.Definition, &v.def); err != nil {
		return err
	}
	if row.TestRun.Result != nil {
		return nil
	}
	if j.Attempts > 1 {
		return v.abandon(ctx)
	}
	ctx, cancel := context.WithTimeoutCause(ctx, s.JobLimit, errJobLimit)
	defer cancel()
	return v.execute(ctx)
}

func (v *verify) execute(ctx context.Context) error {
	err := v.j.Step(ctx, stepChoose, v.choose)
	if err == nil && v.skipped == "" {
		err = v.j.Step(ctx, stepRestore, v.restore)
	}
	if err == nil && v.skipped == "" {
		err = v.j.Step(ctx, stepIsolate, v.isolate)
	}
	if err == nil && v.skipped == "" {
		wctx, interrupt := context.WithCancelCause(ctx)
		v.interrupt = interrupt
		err = v.j.Step(wctx, stepStart, v.boot)
		if err == nil {
			err = v.j.Step(wctx, stepCheck, v.check)
		}
		if err != nil && errors.Is(context.Cause(wctx), errSecondConnection) {
			err = errSecondConnection
		}
		interrupt(nil)
	}
	v.unwatch()
	if jobs.Interrupted(ctx) {
		// Na de herstart ruimt de taak op; tot dan staat de sandbox uit.
		v.emergencyStop()
		return err
	}
	// Opruimen loopt altijd, ook na annuleren of de tijdslimiet.
	dctx, cancel := jobs.Detach(ctx)
	defer cancel()
	dctx, cancelLimit := context.WithTimeout(dctx, 20*time.Minute)
	defer cancelLimit()
	cerr := v.j.Step(dctx, stepCleanup, v.cleanup)
	if jobs.Interrupted(dctx) {
		return cerr
	}
	return v.finish(ctx, err)
}

// abandon is de taak na een herstart van de server: de onderbroken stap
// faalt, de rest blijft zoals hij was, en dan alleen opruimen.
func (v *verify) abandon(ctx context.Context) error {
	for _, p := range v.j.Previous() {
		if p.Name == stepCleanup {
			break
		}
		if p.Status != store.JobStatusRunning {
			v.j.Keep()
			continue
		}
		_ = v.j.Step(ctx, p.Name, func(context.Context, *jobs.Step) error { return errRestarted })
	}
	dctx, cancel := jobs.Detach(ctx)
	defer cancel()
	cerr := v.j.Step(dctx, stepCleanup, v.cleanup)
	if jobs.Interrupted(dctx) {
		return cerr
	}
	return v.finish(ctx, errRestarted)
}

func (v *verify) event(text string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.timeline = append(v.timeline, TimelineEvent{TMS: v.s.Now().Sub(v.start).Milliseconds(), Kind: "step", Text: text})
}

func (v *verify) addCheck(c Check) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.checks = append(v.checks, c)
}

// choose kiest host en sandbox-storage en leest de config uit de back-up.
func (v *verify) choose(ctx context.Context, st *jobs.Step) error {
	s := v.s
	row, err := s.q.GetBackup(ctx, store.GetBackupParams{ConnectionID: v.def.ConnectionID, Volid: v.def.Volid})
	if errors.Is(err, pgx.ErrNoRows) {
		v.skipped = "de back-up " + v.def.Volid + " bestaat niet meer in Proxmox"
		return nil
	}
	if err != nil {
		return err
	}
	api, _, err := s.pve.APIFor(ctx, v.def.ConnectionID)
	if err != nil {
		return err
	}
	v.api = api
	res, err := api.Resources(ctx)
	if err != nil {
		return err
	}
	hosts := map[string]proxmox.Resource{}
	for _, r := range res {
		if r.Type == "node" && r.Status == "online" {
			hosts[r.Node] = r
		}
	}
	// Op welke hosts is de back-upstorage te lezen?
	var readable []string
	shared := false
	for _, r := range res {
		if r.Type != "storage" || r.Storage != v.def.BackupStorage || r.Status == "unknown" {
			continue
		}
		if r.Shared == 1 || r.Plugin == "pbs" {
			shared = true
		}
		if _, online := hosts[r.Node]; online {
			readable = append(readable, r.Node)
		}
	}
	if len(readable) == 0 {
		v.skipped = "geen host die online is, kan back-upstorage " + v.def.BackupStorage + " lezen"
		return nil
	}
	if shared {
		// De host met het meeste vrije geheugen.
		slices.SortFunc(readable, func(a, b string) int {
			fa, fb := hosts[a].MaxMem-hosts[a].Mem, hosts[b].MaxMem-hosts[b].Mem
			if fa != fb {
				if fa > fb {
					return -1
				}
				return 1
			}
			return strings.Compare(a, b)
		})
		v.host = readable[0]
	} else {
		if !slices.Contains(readable, row.PveNode) {
			v.skipped = "host " + row.PveNode + " met deze back-up is niet online"
			return nil
		}
		v.host = row.PveNode
	}
	st.Logf("back-up %s van %s", v.def.Volid, v.def.BackupTime.Local().Format("02-01-2006 15:04"))
	st.Logf("terugzetten op host %s", v.host)

	cfg, err := api.ExtractConfig(ctx, v.host, v.def.Volid)
	if err != nil {
		return fmt.Errorf("config uit de back-up lezen: %w", err)
	}
	v.cfg = cfg
	if cfg["template"] == "1" {
		v.skipped = "de back-up is van een template"
		return nil
	}
	if bad := notIsolatable(cfg); len(bad) > 0 {
		v.skipped = "niet te isoleren: " + strings.Join(bad, ", ")
		return nil
	}

	need := diskBytes(cfg)
	var best *proxmox.Resource
	for i, r := range res {
		if r.Type != "storage" || r.Node != v.host || r.Status != "available" {
			continue
		}
		if s.SandboxStorage != "" {
			if r.Storage == s.SandboxStorage {
				best = &res[i]
			}
			continue
		}
		if !slices.Contains(strings.Split(r.Content, ","), "images") {
			continue
		}
		if best == nil || r.MaxDisk-r.Disk > best.MaxDisk-best.Disk {
			best = &res[i]
		}
	}
	switch {
	case best == nil && s.SandboxStorage != "":
		v.skipped = fmt.Sprintf("sandbox-storage %s is niet beschikbaar op host %s", s.SandboxStorage, v.host)
		return nil
	case best == nil:
		v.skipped = "host " + v.host + " heeft geen storage voor VM-schijven (content images)"
		return nil
	case best.MaxDisk <= 0:
		v.skipped = "de vrije ruimte op storage " + best.Storage + " is onbekend"
		return nil
	case float64(best.Disk+need) > 0.85*float64(best.MaxDisk):
		v.skipped = fmt.Sprintf("storage %s zou boven 85 %% komen: nu %d %% gebruikt, de VM vraagt %s",
			best.Storage, best.Disk*100/best.MaxDisk, gb(need))
		return nil
	}
	v.stor = best.Storage
	h := hosts[v.host]
	mem := memoryBytes(cfg)
	if free := h.MaxMem - h.Mem; free < mem+1<<30 {
		v.skipped = fmt.Sprintf("host %s heeft te weinig vrij geheugen: nodig %s (de VM plus 1 GB), vrij %s",
			v.host, gb(mem+1<<30), gb(free))
		return nil
	}
	st.Logf("sandbox-storage %s, %s nodig", v.stor, gb(need))
	v.event(fmt.Sprintf("Back-up gekozen; terugzetten op %s, storage %s", v.host, v.stor))
	return nil
}

// restore reserveert een VMID en zet de back-up terug in de sandbox-pool.
func (v *verify) restore(ctx context.Context, st *jobs.Step) error {
	s := v.s
	vmid, err := v.api.NextID(ctx)
	if err != nil {
		return fmt.Errorf("vrij VMID opvragen: %w", err)
	}
	sb, err := s.q.InsertBackupSandbox(context.WithoutCancel(ctx), store.InsertBackupSandboxParams{
		ConnectionID: v.def.ConnectionID, Vmid: int32(vmid), SourceVmid: int32(v.def.SourceVMID),
		SourceNodeID: v.run.NodeID, RunID: &v.run.ID, Volid: v.def.Volid, Host: v.host, Storage: v.stor,
	})
	if err != nil {
		return fmt.Errorf("sandbox registreren: %w", err)
	}
	v.sb = &sb
	v.m.SandboxVMID, v.m.Host, v.m.Storage = vmid, v.host, v.stor
	st.Logf("VMID %d gereserveerd in het sandbox-register", vmid)
	v.event(fmt.Sprintf("Terugzetten begonnen als VM %d", vmid))
	t0 := s.Now()
	upid, err := s.restoreSandbox(ctx, v.api, sb, v.def.Volid)
	if err != nil {
		if strings.Contains(err.Error(), "pool") {
			err = fmt.Errorf("%w; bestaat pool %s in Proxmox? (pveum pool add %s)", err, proxmox.SandboxPool, proxmox.SandboxPool)
		}
		return fmt.Errorf("terugzetten starten: %w", err)
	}
	if err := s.pve.WaitTask(ctx, v.api, upid, st); err != nil {
		if ctx.Err() == nil {
			v.addCheck(Check{Name: "Terugzetten", Detail: "Proxmox kon de back-up niet terugzetten: " + err.Error()})
		}
		return err
	}
	d := s.Now().Sub(t0)
	v.m.RestoreSeconds = secs(d)
	sb2, err := s.setSandboxState(ctx, sb, sbPresent, "", events.System())
	if err == nil {
		v.sb = &sb2
	}
	v.addCheck(Check{Name: "Terugzetten", OK: true,
		Detail: fmt.Sprintf("in %s op %s, storage %s, als VM %d", duration(d), v.host, v.stor, vmid)})
	v.event("Teruggezet in " + duration(d))
	return nil
}

// isolate sluit de sandbox af en leest de config terug.
func (v *verify) isolate(ctx context.Context, st *jobs.Step) error {
	s := v.s
	vm, err := s.guard(ctx, v.sb.ID)
	if err != nil {
		return err
	}
	cfg, err := vm.config(ctx)
	if err != nil {
		return fmt.Errorf("config lezen: %w", err)
	}
	if bad := notIsolatable(cfg); len(bad) > 0 {
		v.skipped = "niet te isoleren: " + strings.Join(bad, ", ")
		st.Logf("%s", v.skipped)
		return nil
	}
	change := isolation(cfg, sandboxDescription(v.run.Hostname, v.def.SourceVMID, v.def.Volid, v.run.ID.String()))
	form := url.Values{}
	for k, val := range change {
		form.Set(k, val)
	}
	upid, err := vm.configure(ctx, form)
	if err == nil && upid != "" {
		err = s.pve.WaitTask(ctx, vm.api, upid, st)
	}
	if err != nil {
		return fmt.Errorf("config wijzigen: %w", err)
	}
	// Teruglezen: starten mag alleen als elke regel klopt.
	if vm, err = s.guard(ctx, v.sb.ID); err != nil {
		return err
	}
	cfg, err = vm.config(ctx)
	if err != nil {
		return fmt.Errorf("config teruglezen: %w", err)
	}
	if probs := isolationProblems(cfg); len(probs) > 0 {
		return errors.New("isolatie niet te bevestigen: " + strings.Join(probs, "; "))
	}
	v.cfg = cfg
	nets := 0
	for k := range cfg {
		if netKey(k) {
			nets++
		}
	}
	detail := fmt.Sprintf("%s met link_down, onboot en protection uit, tag en serienummer %s, pool %s",
		count(nets, "netwerkkaart", "netwerkkaarten"), sandboxTag, proxmox.SandboxPool)
	st.Logf("%s", detail)
	v.addCheck(Check{Name: "Isolatie", OK: true, Detail: detail})
	v.event("Netwerk afgesloten en config teruggelezen")
	return nil
}

// boot start de sandbox en wacht tot de guest agent antwoordt.
func (v *verify) boot(ctx context.Context, st *jobs.Step) error {
	s := v.s
	v.watch(ctx, st)
	vm, err := s.guard(ctx, v.sb.ID)
	if err != nil {
		return err
	}
	t0 := s.Now()
	upid, err := vm.power(ctx, "start")
	if err == nil {
		err = s.pve.WaitTask(ctx, vm.api, upid, st)
	}
	if err != nil {
		if ctx.Err() == nil {
			v.addCheck(Check{Name: "Opstarten", Detail: "de VM start niet: " + err.Error()})
		}
		return fmt.Errorf("starten: %w", err)
	}
	v.event("Gestart")
	if !hasAgent(v.cfg) {
		// Zonder guest agent is alleen te zien dat de VM blijft draaien.
		if err := sleep(ctx, s.NoAgentWait); err != nil {
			return err
		}
		vm, err := s.guard(ctx, v.sb.ID)
		if err != nil {
			return err
		}
		if vm.res.Status != "running" {
			v.addCheck(Check{Name: "Opstarten", Detail: "de VM stopte kort na het starten"})
			return errors.New("de VM stopte kort na het starten")
		}
		v.addCheck(Check{Name: "Opstarten", OK: true, Detail: fmt.Sprintf("de VM draait na %s", duration(s.Now().Sub(t0)))})
		v.addCheck(Check{Name: "Guest agent", Warning: true,
			Detail: "de VM heeft geen guest agent in zijn config; alleen terugzetten en starten zijn gecontroleerd"})
		return nil
	}
	st.Logf("wachten op de guest agent (hoogstens %s)", duration(s.BootTimeout))
	deadline := t0.Add(s.BootTimeout)
	for {
		pctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		err := vm.agentPing(pctx)
		cancel()
		if err == nil {
			break
		}
		if ctx.Err() != nil {
			return context.Cause(ctx)
		}
		if !s.Now().Before(deadline) {
			v.addCheck(Check{Name: "Opstarten", Detail: "de guest agent antwoordde niet binnen " + duration(s.BootTimeout)})
			return fmt.Errorf("geen antwoord van de guest agent binnen %s", duration(s.BootTimeout))
		}
		if err := sleep(ctx, s.Poll); err != nil {
			return err
		}
		if vm, err = s.guard(ctx, v.sb.ID); err != nil {
			return err
		}
		if vm.res.Status != "running" {
			v.addCheck(Check{Name: "Opstarten", Detail: "de VM stopte tijdens het opstarten"})
			return errors.New("de VM stopte tijdens het opstarten")
		}
	}
	d := s.Now().Sub(t0)
	v.m.BootSeconds = secs(d)
	v.addCheck(Check{Name: "Opstarten", OK: true, Detail: "de guest agent antwoordt na " + duration(d)})
	v.event("Guest agent antwoordt na " + duration(d))
	return nil
}

// check stelt de guest agent de vaste vragen.
func (v *verify) check(ctx context.Context, st *jobs.Step) error {
	s := v.s
	if !hasAgent(v.cfg) {
		st.Logf("geen guest agent: niets te vragen")
		v.connectionCheck()
		return nil
	}
	t0 := s.Now()
	vm, err := s.guard(ctx, v.sb.ID)
	if err != nil {
		return err
	}
	ask := func(command string, out any) error {
		actx, cancel := context.WithTimeout(ctx, s.AgentTimeout)
		defer cancel()
		raw, err := vm.agentInfo(actx, command)
		if err != nil {
			return err
		}
		return json.Unmarshal(raw, out)
	}

	var host struct {
		Name string `json:"host-name"`
	}
	if err := ask("get-host-name", &host); err != nil {
		v.addCheck(Check{Name: "Hostname", Detail: "de guest agent gaf geen hostname: " + err.Error()})
	} else {
		v.m.Hostname = host.Name
		want := v.run.Hostname
		if sameHost(host.Name, want) {
			v.addCheck(Check{Name: "Hostname", OK: true, Detail: host.Name})
		} else {
			v.addCheck(Check{Name: "Hostname", Warning: true,
				Detail: fmt.Sprintf("de VM noemt zich %s, de node heet %s", host.Name, want)})
		}
	}

	var osinfo struct {
		Name    string `json:"name"`
		Pretty  string `json:"pretty-name"`
		Version string `json:"version-id"`
	}
	if err := ask("get-osinfo", &osinfo); err != nil {
		v.addCheck(Check{Name: "Besturingssysteem", Detail: "de guest agent gaf geen besturingssysteem: " + err.Error()})
	} else {
		name := osinfo.Pretty
		if name == "" {
			name = strings.TrimSpace(osinfo.Name + " " + osinfo.Version)
		}
		v.m.OS = name
		v.addCheck(Check{Name: "Besturingssysteem", OK: name != "", Detail: or(name, "onbekend")})
	}

	var fs []struct {
		Mountpoint string `json:"mountpoint"`
		Type       string `json:"type"`
		Total      int64  `json:"total-bytes"`
		Used       int64  `json:"used-bytes"`
	}
	if err := ask("get-fsinfo", &fs); err != nil {
		v.addCheck(Check{Name: "Bestandssystemen", Detail: "de guest agent gaf geen bestandssystemen: " + err.Error()})
	} else {
		var parts []string
		root := false
		for _, f := range fs {
			if f.Mountpoint == "/" {
				root = true
			}
			p := f.Mountpoint + " (" + f.Type
			if f.Total > 0 {
				p += fmt.Sprintf(", %s van %s", gb(f.Used), gb(f.Total))
			}
			parts = append(parts, p+")")
		}
		v.m.Filesystems = len(fs)
		detail := count(len(fs), "bestandssysteem", "bestandssystemen")
		if len(parts) > 0 {
			detail += ": " + strings.Join(parts, ", ")
		}
		if !root {
			detail = "geen root-bestandssysteem (/) gekoppeld; " + detail
		}
		v.addCheck(Check{Name: "Bestandssystemen", OK: root, Detail: detail})
	}
	v.connectionCheck()
	v.m.CheckSeconds = secs(s.Now().Sub(t0))
	v.event("Controles klaar")
	return nil
}

// watch bewaakt zolang de sandbox kan draaien het aantal verbindingen met
// de sleutel van de bronnode. Komt er een bij, dan gaat de sandbox hard uit
// en eindigt de controle.
func (v *verify) watch(ctx context.Context, st *jobs.Step) {
	s := v.s
	if s.conns == nil || v.run.NodeID == nil {
		return
	}
	agent, err := s.q.GetActiveAgentByNode(ctx, *v.run.NodeID)
	if err != nil {
		st.Logf("de bronnode heeft geen agent; geen bewaking van tweede verbindingen nodig")
		return
	}
	wctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	done := make(chan struct{})
	v.watched = true
	v.stopWatch = func() {
		cancel()
		<-done
	}
	go func() {
		defer close(done)
		t := time.NewTicker(s.ConnPoll)
		defer t.Stop()
		for {
			n, err := s.conns.Connections(agent.NkeyPublic)
			if err == nil {
				v.mu.Lock()
				v.maxConns = max(v.maxConns, n)
				v.mu.Unlock()
			}
			if err == nil && n > 1 {
				s.log.Warn("tweede agentverbinding tijdens een back-upcontrole; sandbox gaat hard uit", "node", v.run.Hostname, "connections", n)
				v.event(fmt.Sprintf("Tweede verbinding met de sleutel van %s; sandbox hard uitgezet", v.run.Hostname))
				v.interrupt(errSecondConnection)
				v.emergencyStop()
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

func (v *verify) unwatch() {
	if v.stopWatch != nil {
		v.stopWatch()
		v.stopWatch = nil
	}
}

func (v *verify) connectionCheck() {
	v.mu.Lock()
	n, watched := v.maxConns, v.watched
	v.mu.Unlock()
	switch {
	case !watched:
		v.addCheck(Check{Name: "Agentverbinding", OK: true, Detail: "de bronnode heeft geen agent"})
	case n > 1:
		v.addCheck(Check{Name: "Agentverbinding", Detail: fmt.Sprintf("%d verbindingen met de sleutel van %s", n, v.run.Hostname)})
	default:
		v.addCheck(Check{Name: "Agentverbinding", OK: true, Detail: "geen tweede verbinding met de sleutel van " + v.run.Hostname})
	}
}

// emergencyStop zet de sandbox hard uit, los van de taak: bij een tweede
// verbinding en bij een stop van de server.
func (v *verify) emergencyStop() {
	if v.sb == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	vm, err := v.s.guard(ctx, v.sb.ID)
	if err != nil || vm.res.Status != "running" {
		return
	}
	if _, err := vm.power(ctx, "stop"); err != nil {
		v.s.log.Warn("sandbox hard uitzetten mislukt", "vmid", v.sb.Vmid, "err", err)
	}
}

// cleanup verwijdert de sandbox van deze run, als die er is.
func (v *verify) cleanup(ctx context.Context, st *jobs.Step) error {
	s := v.s
	if v.sb == nil {
		sb, err := s.q.GetRunSandbox(ctx, &v.run.ID)
		if errors.Is(err, pgx.ErrNoRows) {
			st.Logf("geen sandbox; niets op te ruimen")
			return nil
		}
		if err != nil {
			return err
		}
		v.sb = &sb
	}
	t0 := s.Now()
	sb, err := s.destroySandbox(ctx, v.sb.ID, destroyOpts{lockWait: s.LockWait, step: st})
	if sb.ID != uuid.Nil {
		v.sb = &sb
	}
	v.m.SandboxVMID = int(v.sb.Vmid)
	v.m.Host, v.m.Storage = v.sb.Host, v.sb.Storage
	// Ook een mislukte poging telt, zodat de tijdlijn het wachten op een
	// vergrendeling toont.
	v.m.CleanupSeconds = secs(s.Now().Sub(t0))
	if err != nil {
		v.addCheck(Check{Name: "Opruimen", Warning: true, Detail: fmt.Sprintf(
			"sandbox-VM %d is nog niet verwijderd (%v); de opruimer probeert het elke %s opnieuw", v.sb.Vmid, err, duration(s.CleanEvery))})
		return err
	}
	switch v.sb.State {
	case sbDestroyed:
		v.m.DestroyedAt = v.sb.DestroyedAt
		v.addCheck(Check{Name: "Opruimen", OK: true, Detail: fmt.Sprintf("sandbox-VM %d verwijderd", v.sb.Vmid)})
		v.event(fmt.Sprintf("VM %d verwijderd", v.sb.Vmid))
	case sbNone:
		st.Logf("VM %d bestond niet in Proxmox", v.sb.Vmid)
	}
	return nil
}

// finish schrijft het rapport.
func (v *verify) finish(ctx context.Context, runErr error) error {
	s := v.s
	v.m.TotalSeconds = secs(s.Now().Sub(v.start))
	// De uitkomst hangt af van waarom ctx stopte; daarna loopt alles los
	// van ctx.
	result, summary := v.verdict(ctx, runErr)
	ctx = context.WithoutCancel(ctx)
	checks, _ := json.Marshal(nonNil(v.checks))
	timeline, _ := json.Marshal(nonNil(v.timeline))
	meas, _ := json.Marshal(v.m)
	run, err := s.q.FinishTestRun(ctx, store.FinishTestRunParams{
		ID: v.run.ID, Result: &result, Summary: summary, Checks: checks, Timeline: timeline, Measurements: meas,
	})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if err == nil {
		s.finishedEvent(ctx, run, v.def, v.m)
	}
	s.Kick()
	switch result {
	case "canceled":
		return jobs.ErrCanceled
	case "error":
		return errors.New(summary)
	}
	return nil
}

// verdict maakt de uitkomst en de samenvatting.
func (v *verify) verdict(ctx context.Context, runErr error) (string, string) {
	s := v.s
	head := fmt.Sprintf("%s, back-up van %s", v.run.Hostname, v.def.BackupTime.Local().Format("02-01-2006 15:04"))
	var failed, warnings []string
	for _, c := range v.checks {
		switch {
		case c.OK:
		case c.Warning:
			warnings = append(warnings, c.Detail)
		default:
			failed = append(failed, c.Name+": "+c.Detail)
		}
	}
	tail := v.cleanupText()
	cause := context.Cause(ctx)
	switch {
	case errors.Is(runErr, errRestarted):
		return "error", join(head+": onderbroken door herstart van de server.", tail)
	case jobs.Canceled(ctx) || errors.Is(runErr, jobs.ErrCanceled):
		return "canceled", join(head+": afgebroken.", tail)
	case errors.Is(runErr, errSecondConnection):
		return "error", join(fmt.Sprintf("%s: controle afgebroken: tweede verbinding met de sleutel van %s.", head, v.run.Hostname), tail)
	case errors.Is(cause, errJobLimit) || errors.Is(runErr, errJobLimit):
		return "error", join(fmt.Sprintf("%s: afgebroken, de controle duurde langer dan %s.", head, duration(s.JobLimit)), tail)
	case v.skipped != "":
		return "skipped", join(fmt.Sprintf("%s: niet gecontroleerd, %s.", head, v.skipped), tail)
	case len(failed) > 0:
		return "fail", join(fmt.Sprintf("%s: back-up afgekeurd: %s mislukt. %s.", head,
			count(len(failed), "controle", "controles"), strings.Join(failed, "; ")), tail)
	case runErr != nil:
		return "error", join(fmt.Sprintf("%s: controle mislukt: %v.", head, runErr), tail)
	}
	var parts []string
	if v.m.RestoreSeconds != nil {
		p := "Terugzetten " + duration(seconds(*v.m.RestoreSeconds))
		if v.m.BootSeconds != nil {
			p += ", opstarten " + duration(seconds(*v.m.BootSeconds))
		}
		parts = append(parts, p+".")
	}
	var found []string
	if v.m.Hostname != "" {
		found = append(found, "Hostname "+v.m.Hostname)
	}
	if v.m.OS != "" {
		found = append(found, v.m.OS)
	}
	if v.m.Filesystems > 0 {
		found = append(found, count(v.m.Filesystems, "bestandssysteem", "bestandssystemen"))
	}
	if len(found) > 0 {
		parts = append(parts, strings.Join(found, ", ")+".")
	}
	if len(warnings) > 0 {
		return "warning", join(head+": geslaagd met waarschuwing: "+strings.Join(warnings, "; ")+".", append(parts, tail)...)
	}
	return "pass", join(head+": geslaagd.", append(parts, tail)...)
}

func (v *verify) cleanupText() string {
	if v.sb == nil {
		return ""
	}
	switch v.sb.State {
	case sbDestroyed:
		at := v.s.Now()
		if v.sb.DestroyedAt != nil {
			at = *v.sb.DestroyedAt
		}
		return fmt.Sprintf("Sandbox-VM %d verwijderd om %s.", v.sb.Vmid, at.Local().Format("15:04"))
	case sbNone:
		return ""
	}
	return fmt.Sprintf("Sandbox-VM %d is nog niet verwijderd; de opruimer probeert het opnieuw.", v.sb.Vmid)
}

func (s *Service) finishedEvent(ctx context.Context, run store.TestRun, def Definition, m Measurements) {
	p := map[string]any{
		"run_id": run.ID, "node_id": run.NodeID, "hostname": run.Hostname, "volid": def.Volid,
		"result": deref(run.Result), "summary": run.Summary,
	}
	if m.RestoreSeconds != nil {
		p["restore_seconds"] = *m.RestoreSeconds
	}
	if m.BootSeconds != nil {
		p["boot_seconds"] = *m.BootSeconds
	}
	if err := s.ev.Write(ctx, nil, events.Event{
		Actor: events.System(), SubjectType: "test_run", SubjectID: run.ID.String(), ClusterID: run.ClusterID,
		Action: "backup.verify_finished", Payload: p,
	}); err != nil {
		s.log.Warn("event schrijven mislukt", "err", err)
	}
}

// verifyFinished rondt een run af waarvan de taak stopte zonder rapport (in
// de wachtrij geannuleerd, te vaak onderbroken of een interne fout) en geeft
// de sandbox aan de opruimer.
func (s *Service) verifyFinished(ctx context.Context, j store.Job) {
	defer s.kickCleaner()
	run, err := s.q.GetTestRunByJob(ctx, &j.ID)
	if err != nil || run.Result != nil {
		return
	}
	var def Definition
	_ = json.Unmarshal(run.Definition, &def)
	result, summary := "error", fmt.Sprintf("%s: de taak stopte zonder rapport", run.Hostname)
	if j.Error != "" {
		summary += ": " + j.Error
	}
	if j.Status == store.JobStatusCanceled {
		result, summary = "canceled", run.Hostname+": afgebroken voor de start"
	}
	done, err := s.q.FinishTestRun(ctx, store.FinishTestRunParams{
		ID: run.ID, Result: &result, Summary: summary, Checks: run.Checks, Timeline: []byte("[]"), Measurements: []byte("{}"),
	})
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			s.log.Error("back-upcontrole afronden", "run", run.ID, "err", err)
		}
		return
	}
	s.finishedEvent(ctx, done, def, Measurements{})
	s.Kick()
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

// sameHost vergelijkt een hostname met de naam van de node, ook als een van
// beide een FQDN is.
func sameHost(a, b string) bool {
	short := func(s string) string {
		s = strings.ToLower(strings.TrimSpace(s))
		h, _, _ := strings.Cut(s, ".")
		return h
	}
	return strings.EqualFold(a, b) || short(a) != "" && short(a) == short(b)
}

func sleep(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return context.Cause(ctx)
	case <-time.After(d):
		return nil
	}
}

func secs(d time.Duration) *float64 {
	f := math.Round(d.Seconds()*10) / 10
	return &f
}

func seconds(f float64) time.Duration { return time.Duration(f * float64(time.Second)) }

// duration schrijft een duur als "41 s", "3 min 12 s" of "1 u 5 min".
func duration(d time.Duration) string {
	d = d.Round(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%d s", int(d.Seconds()))
	case d < time.Hour:
		m, sec := int(d/time.Minute), int(d%time.Minute/time.Second)
		if sec == 0 {
			return fmt.Sprintf("%d min", m)
		}
		return fmt.Sprintf("%d min %d s", m, sec)
	}
	h, m := int(d/time.Hour), int(d%time.Hour/time.Minute)
	if m == 0 {
		return fmt.Sprintf("%d u", h)
	}
	return fmt.Sprintf("%d u %d min", h, m)
}

// gb schrijft een grootte als "4,2 GB".
func gb(b int64) string {
	return strings.Replace(fmt.Sprintf("%.1f GB", float64(b)/(1<<30)), ".", ",", 1)
}

func count(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

func join(first string, rest ...string) string {
	out := first
	for _, r := range rest {
		if r != "" {
			out += " " + r
		}
	}
	return out
}
