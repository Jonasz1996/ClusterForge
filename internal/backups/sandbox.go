package backups

// Het sandbox-register en de bewaakte functies. ClusterForge start, wijzigt en
// verwijdert alleen een VM die het zelf als sandbox terugzette. Dit bestand is
// de enige plek die Restore, Destroy, AgentInfo, AgentRunVerify en
// AgentExecStatus van de Proxmox-API aanroept; TestSandboxCallers bewaakt dat.

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/jobs"
	"github.com/Jonasz1996/clusterforge/internal/proxmox"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

// Standen in backup_sandboxes.
const (
	sbReserved      = "reserved"
	sbPresent       = "present"
	sbDestroyed     = "destroyed"
	sbDestroyFailed = "destroy_failed"
	sbNone          = "none"
)

func liveState(state string) bool {
	return state == sbReserved || state == sbPresent || state == sbDestroyFailed
}

var (
	// errGone: het VMID bestaat niet in Proxmox; er valt niets op te ruimen.
	errGone = errors.New("de VM bestaat niet in Proxmox")
	// errNotInPool: het VMID bestaat, maar niet in de sandbox-pool. Dan is
	// het geen sandbox (meer) en raakt ClusterForge het nooit aan.
	errNotInPool = errors.New("de VM zit niet in pool " + proxmox.SandboxPool)
	// errLocked: Proxmox heeft de VM vergrendeld, bijvoorbeeld omdat het
	// terugzetten nog loopt.
	errLocked = errors.New("de VM is vergrendeld in Proxmox")
)

// guardError betekent dat een voorwaarde van de bewaakte functies niet
// klopt; de VM wordt dan niet aangeraakt.
type guardError struct{ msg string }

func (e guardError) Error() string { return "geweigerd: " + e.msg }

// sandboxVM is een sandbox zoals Proxmox hem nu toont, nadat alle vier de
// voorwaarden klopten. Alleen via deze waarde gaat er iets naar de VM.
type sandboxVM struct {
	api proxmox.API
	sb  store.BackupSandbox
	res proxmox.Resource
}

func (v sandboxVM) guest() proxmox.Guest {
	return proxmox.Guest{Type: "qemu", Node: v.res.Node, VMID: int(v.sb.Vmid)}
}

// guard controleert de vier voorwaarden: de sandbox staat levend in het
// register, het is niet het bron-VMID, geen node is aan het VMID gekoppeld,
// en Proxmox toont nu een VM met dat VMID in pool cf-sandbox. De laatste
// komt rechtstreeks uit Proxmox, niet uit de laatste sync.
func (s *Service) guard(ctx context.Context, id uuid.UUID) (sandboxVM, error) {
	sb, err := s.q.GetBackupSandbox(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return sandboxVM{}, guardError{"de sandbox staat niet in het register"}
	}
	if err != nil {
		return sandboxVM{}, err
	}
	if !liveState(sb.State) {
		return sandboxVM{sb: sb}, guardError{"de sandbox is al opgeruimd"}
	}
	if sb.Vmid == sb.SourceVmid {
		return sandboxVM{sb: sb}, guardError{"het VMID is dat van de bron-VM"}
	}
	if err := s.notLinked(ctx, sb.ConnectionID, sb.Vmid); err != nil {
		return sandboxVM{sb: sb}, err
	}
	api, _, err := s.pve.APIFor(ctx, sb.ConnectionID)
	if err != nil {
		return sandboxVM{sb: sb}, err
	}
	res, err := api.Resources(ctx)
	if err != nil {
		return sandboxVM{sb: sb}, err
	}
	for _, r := range res {
		if (r.Type != "qemu" && r.Type != "lxc") || r.VMID != int(sb.Vmid) {
			continue
		}
		if !inSandboxPool(r) {
			return sandboxVM{sb: sb}, errNotInPool
		}
		return sandboxVM{api: api, sb: sb, res: r}, nil
	}
	return sandboxVM{sb: sb}, errGone
}

// notLinked weigert een VMID waaraan een node gekoppeld is.
func (s *Service) notLinked(ctx context.Context, connID uuid.UUID, vmid int32) error {
	n, err := s.q.GetNodeByGuest(ctx, store.GetNodeByGuestParams{ProxmoxID: &connID, PveVmid: &vmid})
	switch {
	case err == nil:
		return guardError{fmt.Sprintf("node %s is aan VM %d gekoppeld", n.Hostname, vmid)}
	case errors.Is(err, pgx.ErrNoRows):
		return nil
	default:
		return err
	}
}

// restoreSandbox zet een back-up terug als de sandbox van een gereserveerde
// rij: alleen in pool cf-sandbox, met nieuwe MAC-adressen, nooit over een
// bestaande VM en nooit gestart.
func (s *Service) restoreSandbox(ctx context.Context, api proxmox.API, sb store.BackupSandbox, archive string) (string, error) {
	if sb.State != sbReserved {
		return "", guardError{"de sandbox is niet gereserveerd"}
	}
	if sb.Vmid == sb.SourceVmid {
		return "", guardError{"het VMID is dat van de bron-VM"}
	}
	if err := s.notLinked(ctx, sb.ConnectionID, sb.Vmid); err != nil {
		return "", err
	}
	res, err := api.Resources(ctx)
	if err != nil {
		return "", err
	}
	for _, r := range res {
		if (r.Type == "qemu" || r.Type == "lxc") && r.VMID == int(sb.Vmid) {
			return "", guardError{fmt.Sprintf("VM %d bestaat al in Proxmox", sb.Vmid)}
		}
	}
	return api.Restore(ctx, sb.Host, int(sb.Vmid), archive, sb.Storage, proxmox.SandboxPool)
}

// configure wijzigt de config van de sandbox.
func (v sandboxVM) configure(ctx context.Context, form url.Values) (string, error) {
	return v.api.SetConfig(ctx, v.guest(), form)
}

func (v sandboxVM) config(ctx context.Context) (map[string]string, error) {
	cfg, err := v.api.Config(ctx, v.guest())
	if err != nil {
		return nil, err
	}
	return configStrings(cfg), nil
}

// power start of stopt de sandbox.
func (v sandboxVM) power(ctx context.Context, action string) (string, error) {
	if action != "start" && action != "stop" {
		return "", fmt.Errorf("actie %q kan niet op een sandbox", action)
	}
	return v.api.Power(ctx, v.guest(), action)
}

func (v sandboxVM) agentPing(ctx context.Context) error { return v.api.AgentPing(ctx, v.guest()) }

// agentInfo stelt de guest agent van de sandbox een alleen lezende vraag.
func (v sandboxVM) agentInfo(ctx context.Context, command string) ([]byte, error) {
	raw, err := v.api.AgentInfo(ctx, v.guest(), command)
	return raw, err
}

// runVerify start cf-agent verify in de sandbox: het enige programma dat
// ClusterForge in een VM start, met een vaste opdrachtregel.
func (v sandboxVM) runVerify(ctx context.Context, request []byte) (int, error) {
	return v.api.AgentRunVerify(ctx, v.guest(), request)
}

func (v sandboxVM) execStatus(ctx context.Context, pid int) (proxmox.ExecStatus, error) {
	return v.api.AgentExecStatus(ctx, v.guest(), pid)
}

// lockSandbox zorgt dat de taak en de opruimer niet tegelijk met dezelfde
// sandbox bezig zijn.
func (s *Service) lockSandbox(id uuid.UUID) func() {
	s.sbMu.Lock()
	m, ok := s.sbLocks[id]
	if !ok {
		m = &sync.Mutex{}
		s.sbLocks[id] = m
	}
	s.sbMu.Unlock()
	m.Lock()
	return m.Unlock
}

// destroyOpts zijn de keuzes van één opruimpoging.
type destroyOpts struct {
	// lockWait is hoe lang gewacht wordt als Proxmox de VM vergrendeld
	// heeft; 0 geeft meteen errLocked.
	lockWait time.Duration
	actor    events.Actor
	step     *jobs.Step
}

// destroySandbox ruimt een sandbox op langs de bewaakte weg: protection eraf,
// hard stoppen en verwijderen met purge. Bestaat de VM niet (meer) of zit hij
// niet in de sandbox-pool, dan sluit de rij met state none, zonder iets aan
// te raken. Een mislukte poging zet destroy_failed; de opruimer probeert
// het dan opnieuw.
func (s *Service) destroySandbox(ctx context.Context, id uuid.UUID, o destroyOpts) (store.BackupSandbox, error) {
	defer s.lockSandbox(id)()
	logf := func(format string, args ...any) {
		if o.step != nil {
			o.step.Logf(format, args...)
		}
	}
	vm, err := s.guard(ctx, id)
	var ge guardError
	switch {
	case errors.Is(err, errGone), errors.Is(err, errNotInPool):
		s.log.Info("sandbox afgesloten zonder opruimen", "vmid", vm.sb.Vmid, "reason", err.Error())
		logf("VM %d: %v; er valt niets op te ruimen", vm.sb.Vmid, err)
		return s.setSandboxState(ctx, vm.sb, sbNone, err.Error(), o.actor)
	case errors.As(err, &ge) && vm.sb.ID != uuid.Nil && !liveState(vm.sb.State):
		return vm.sb, nil
	case err != nil:
		return s.destroyFailed(ctx, vm.sb, err, o)
	}
	sb := vm.sb

	waited := time.Duration(0)
	for vm.res.Lock != "" {
		if waited >= o.lockWait {
			if o.lockWait == 0 {
				return sb, errLocked
			}
			return s.destroyFailed(ctx, sb, fmt.Errorf("%w (lock %s)", errLocked, vm.res.Lock), o)
		}
		logf("VM %d is vergrendeld (lock %s); wachten", sb.Vmid, vm.res.Lock)
		select {
		case <-ctx.Done():
			return sb, context.Cause(ctx)
		case <-time.After(s.Poll):
		}
		waited += s.Poll
		if vm, err = s.guard(ctx, id); err != nil {
			if errors.Is(err, errGone) || errors.Is(err, errNotInPool) {
				return s.setSandboxState(ctx, sb, sbNone, err.Error(), o.actor)
			}
			return s.destroyFailed(ctx, sb, err, o)
		}
	}

	cfg, err := vm.config(ctx)
	if err != nil {
		return s.destroyFailed(ctx, sb, fmt.Errorf("config lezen: %w", err), o)
	}
	if cfg["protection"] == "1" {
		logf("protection uitzetten")
		upid, err := vm.configure(ctx, url.Values{"protection": {"0"}})
		if err == nil && upid != "" {
			err = s.pve.WaitTask(ctx, vm.api, upid, o.step)
		}
		if err != nil {
			return s.destroyFailed(ctx, sb, fmt.Errorf("protection uitzetten: %w", err), o)
		}
	}
	if vm.res.Status == "running" {
		logf("VM %d hard stoppen", sb.Vmid)
		upid, err := vm.power(ctx, "stop")
		if err == nil {
			err = s.pve.WaitTask(ctx, vm.api, upid, o.step)
		}
		if err != nil {
			return s.destroyFailed(ctx, sb, fmt.Errorf("stoppen: %w", err), o)
		}
	}
	logf("VM %d verwijderen (purge)", sb.Vmid)
	upid, err := vm.api.Destroy(ctx, vm.guest())
	if err == nil {
		err = s.pve.WaitTask(ctx, vm.api, upid, o.step)
	}
	if err != nil {
		return s.destroyFailed(ctx, sb, fmt.Errorf("verwijderen: %w", err), o)
	}
	// Pas weg als Proxmox hem ook echt niet meer toont.
	if _, err := s.guard(ctx, id); !errors.Is(err, errGone) {
		if err == nil {
			err = errors.New("de VM staat er na het verwijderen nog")
		}
		return s.destroyFailed(ctx, sb, err, o)
	}
	logf("VM %d verwijderd", sb.Vmid)
	return s.setSandboxState(ctx, sb, sbDestroyed, "", o.actor)
}

// destroyFailed zet destroy_failed met de fout. Het event komt alleen bij de
// overgang, niet bij elke nieuwe poging van de opruimer.
func (s *Service) destroyFailed(ctx context.Context, sb store.BackupSandbox, cause error, o destroyOpts) (store.BackupSandbox, error) {
	if sb.ID == uuid.Nil {
		return sb, cause
	}
	if o.step != nil {
		o.step.Logf("opruimen mislukt: %v", cause)
	}
	out, err := s.setSandboxState(ctx, sb, sbDestroyFailed, cause.Error(), o.actor)
	if err != nil {
		return out, errors.Join(cause, err)
	}
	return out, cause
}

// setSandboxState bewaart de nieuwe stand en schrijft het event erbij.
func (s *Service) setSandboxState(ctx context.Context, sb store.BackupSandbox, state, msg string, actor events.Actor) (store.BackupSandbox, error) {
	ctx = context.WithoutCancel(ctx)
	var out store.BackupSandbox
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		var err error
		out, err = q.SetBackupSandboxState(ctx, store.SetBackupSandboxStateParams{ID: sb.ID, State: state, Error: msg})
		if err != nil {
			return err
		}
		action := map[string]string{
			sbPresent: "backup.sandbox_created", sbDestroyed: "backup.sandbox_destroyed",
			sbDestroyFailed: "backup.sandbox_destroy_failed",
		}[state]
		if action == "" || state == sb.State {
			return nil
		}
		return s.sandboxEvent(ctx, q, out, action, actor, map[string]any{"error": msg})
	})
	if err != nil {
		s.log.Error("stand van sandbox bewaren", "sandbox", sb.ID, "state", state, "err", err)
	}
	s.Kick()
	return out, err
}

// sandboxEvent schrijft een event over een sandbox, bij de run als die er
// nog is.
func (s *Service) sandboxEvent(ctx context.Context, q *store.Queries, sb store.BackupSandbox, action string, actor events.Actor, extra map[string]any) error {
	if actor.Type == "" {
		actor = events.System()
	}
	p := map[string]any{
		"vmid": sb.Vmid, "source_vmid": sb.SourceVmid, "host": sb.Host, "storage": sb.Storage, "volid": sb.Volid,
		"sandbox_id": sb.ID,
	}
	for k, v := range extra {
		if v != "" && v != nil {
			p[k] = v
		}
	}
	e := events.Event{Actor: actor, Action: action, Payload: p, SubjectType: "proxmox", SubjectID: sb.ConnectionID.String()}
	if sb.RunID != nil {
		if run, err := q.GetTestRun(ctx, *sb.RunID); err == nil {
			e.SubjectType, e.SubjectID, e.ClusterID = "test_run", sb.RunID.String(), run.TestRun.ClusterID
			p["hostname"] = run.TestRun.Hostname
		}
	}
	return s.ev.Write(ctx, q, e)
}

// cleaner ruimt sandboxes op: bij de start, elke CleanEvery en na elke
// controle.
func (s *Service) cleaner(ctx context.Context) {
	t := time.NewTicker(s.CleanEvery)
	defer t.Stop()
	for {
		s.Clean(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-s.cleanKick:
		}
	}
}

func (s *Service) kickCleaner() {
	select {
	case s.cleanKick <- struct{}{}:
	default:
	}
}

// Clean doet één ronde van de opruimer: elke sandbox waarvan de run klaar
// is, de taak niet meer loopt, verwijderen eerder mislukte of die ouder is
// dan MaxSandboxAge.
func (s *Service) Clean(ctx context.Context) {
	rows, err := s.q.ListLiveSandboxes(ctx)
	if err != nil {
		if ctx.Err() == nil {
			s.log.Error("sandboxes lezen", "err", err)
		}
		return
	}
	now := s.Now()
	for _, r := range rows {
		sb := r.BackupSandbox
		running := r.JobStatus.Valid && (r.JobStatus.JobStatus == store.JobStatusQueued || r.JobStatus.JobStatus == store.JobStatusRunning)
		due := sb.State == sbDestroyFailed || r.RunResult != nil || !running || now.Sub(sb.CreatedAt) > s.MaxSandboxAge
		if !due {
			continue
		}
		cctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		_, err := s.destroySandbox(cctx, sb.ID, destroyOpts{})
		cancel()
		switch {
		case errors.Is(err, errLocked):
			s.log.Info("sandbox nog vergrendeld; volgende ronde opnieuw", "vmid", sb.Vmid)
		case err != nil && ctx.Err() == nil:
			s.log.Warn("sandbox opruimen mislukt", "vmid", sb.Vmid, "err", err)
		}
	}
}

// Cleanup ruimt één sandbox nu op, op vraag van een beheerder.
func (s *Service) Cleanup(ctx context.Context, actor events.Actor, id uuid.UUID) (store.BackupSandbox, error) {
	rows, err := s.q.ListLiveSandboxes(ctx)
	if err != nil {
		return store.BackupSandbox{}, err
	}
	for _, r := range rows {
		if r.BackupSandbox.ID != id {
			continue
		}
		if r.RunResult == nil && r.JobStatus.Valid &&
			(r.JobStatus.JobStatus == store.JobStatusQueued || r.JobStatus.JobStatus == store.JobStatusRunning) {
			return r.BackupSandbox, ConflictError{"running", "de controle loopt nog; breek de taak af, dan ruimt ClusterForge de sandbox zelf op"}
		}
		sb, err := s.destroySandbox(ctx, id, destroyOpts{actor: actor})
		if errors.Is(err, errLocked) {
			return sb, ConflictError{"locked", "Proxmox heeft de VM vergrendeld, bijvoorbeeld omdat het terugzetten nog loopt; probeer het straks opnieuw"}
		}
		return sb, err
	}
	sb, err := s.q.GetBackupSandbox(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return sb, ErrNotFound
	}
	if err != nil {
		return sb, err
	}
	return sb, ConflictError{"conflict", "deze sandbox is al opgeruimd"}
}
