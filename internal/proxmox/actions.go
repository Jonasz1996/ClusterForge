package proxmox

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/jobs"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

// KindVMAction is de soort taak voor acties op een VM of container.
const KindVMAction = "proxmox.vm_action"

// Actions zijn de acties op een VM of container.
var Actions = []string{"start", "stop", "shutdown", "reboot", "snapshot", "migrate"}

// ActionRequest is wat een gebruiker vraagt.
type ActionRequest struct {
	Action       string
	SnapshotName string
	Description  string
	VMState      bool
	Target       string
}

type vmActionParams struct {
	ConnectionID uuid.UUID `json:"connection_id"`
	VMID         int       `json:"vmid"`
	Name         string    `json:"name"`
	Action       string    `json:"action"`
	SnapshotName string    `json:"snapshot_name,omitempty"`
	Description  string    `json:"description,omitempty"`
	VMState      bool      `json:"vmstate,omitempty"`
	Target       string    `json:"target,omitempty"`
}

var snapshotName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{1,39}$`)

// actionTitles beschrijven een actie in de takenlijst.
var actionTitles = map[string]string{
	"start": "Starten", "stop": "Hard uitzetten", "shutdown": "Afsluiten", "reboot": "Herstarten",
	"snapshot": "Snapshot", "migrate": "Migreren",
}

// RequestAction controleert de vraag tegen de laatste sync en zet een taak
// in de wachtrij.
func (s *Service) RequestAction(ctx context.Context, actor events.Actor, connID uuid.UUID, vmid int, req ActionRequest) (store.Job, error) {
	if s.box == nil {
		return store.Job{}, ErrNoMasterKey
	}
	if _, err := s.q.GetProxmoxConnection(ctx, connID); err != nil {
		return store.Job{}, translate(err)
	}
	g, err := s.q.GetProxmoxGuest(ctx, store.GetProxmoxGuestParams{ConnectionID: connID, Vmid: int32p(vmid)})
	if err != nil {
		return store.Job{}, translate(err)
	}
	kind := "VM"
	if g.Type == "lxc" {
		kind = "container"
	}
	if g.Template {
		return store.Job{}, ValidationError{"dit is een template; die kun je niet starten, snapshotten of migreren"}
	}
	sandbox, err := s.q.IsSandboxGuest(ctx, store.IsSandboxGuestParams{ConnectionID: connID, Vmid: int32(vmid)})
	if err != nil {
		return store.Job{}, err
	}
	if sandbox {
		return store.Job{}, ConflictError{"dit is een tijdelijke sandbox van een back-upcontrole; ClusterForge start, stopt en verwijdert hem zelf"}
	}
	p := vmActionParams{ConnectionID: connID, VMID: vmid, Name: g.Name, Action: req.Action}
	title := fmt.Sprintf("%s: %s (%s %d)", actionTitles[req.Action], g.Name, kind, vmid)
	switch req.Action {
	case "start":
		if g.Status == "running" {
			return store.Job{}, ConflictError{fmt.Sprintf("%s %d draait al", kind, vmid)}
		}
	case "stop", "shutdown", "reboot":
		if g.Status != "running" {
			return store.Job{}, ConflictError{fmt.Sprintf("%s %d draait niet", kind, vmid)}
		}
	case "snapshot":
		p.SnapshotName = strings.TrimSpace(req.SnapshotName)
		if p.SnapshotName == "" {
			p.SnapshotName = "cf-" + time.Now().Format("20060102-150405")
		}
		if !snapshotName.MatchString(p.SnapshotName) {
			return store.Job{}, ValidationError{"een snapshotnaam begint met een letter en bevat alleen letters, cijfers, - en _ (2 tot 40 tekens)"}
		}
		p.Description = strings.TrimSpace(req.Description)
		if len(p.Description) > 500 {
			return store.Job{}, ValidationError{"beschrijving is te lang"}
		}
		p.VMState = req.VMState && g.Type == "qemu" && g.Status == "running"
		title = fmt.Sprintf("Snapshot %s: %s (%s %d)", p.SnapshotName, g.Name, kind, vmid)
	case "migrate":
		p.Target = strings.TrimSpace(req.Target)
		if p.Target == "" {
			return store.Job{}, ValidationError{"kies een doelhost"}
		}
		if p.Target == g.PveNode {
			return store.Job{}, ValidationError{fmt.Sprintf("%s %d staat al op %s", kind, vmid, p.Target)}
		}
		hosts, err := s.onlineHosts(ctx, connID)
		if err != nil {
			return store.Job{}, err
		}
		if !slices.Contains(hosts, p.Target) {
			return store.Job{}, ValidationError{"doelhost " + p.Target + " is niet online in Proxmox"}
		}
		title = fmt.Sprintf("Migreren naar %s: %s (%s %d)", p.Target, g.Name, kind, vmid)
	default:
		return store.Job{}, ValidationError{"onbekende actie " + req.Action}
	}

	spec := jobs.Spec{Kind: KindVMAction, Title: title, Params: p, ProxmoxID: &connID, Actor: actor}
	n, err := s.q.GetNodeByGuest(ctx, store.GetNodeByGuestParams{ProxmoxID: &connID, PveVmid: int32p(vmid)})
	switch {
	case err == nil:
		spec.NodeID, spec.ClusterID = &n.ID, n.ClusterID
	case !errors.Is(err, pgx.ErrNoRows):
		return store.Job{}, err
	}
	if spec.ClusterID == nil {
		return s.jobs.Enqueue(ctx, spec)
	}
	// Een VM van een node in een cluster neemt het clusterslot.
	j, err := s.jobs.EnqueueForCluster(ctx, spec)
	var busy jobs.BusyError
	if errors.As(err, &busy) {
		return store.Job{}, ConflictError{busy.Error()}
	}
	return j, err
}

func (s *Service) onlineHosts(ctx context.Context, connID uuid.UUID) ([]string, error) {
	rows, err := s.q.ListProxmoxResources(ctx, connID)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, r := range rows {
		if r.ProxmoxResource.Type == "node" && r.ProxmoxResource.Status == "online" {
			out = append(out, r.ProxmoxResource.PveNode)
		}
	}
	return out, nil
}

// actionTimeout is hoe lang ClusterForge op een Proxmox-taak wacht.
func actionTimeout(action string) time.Duration {
	switch action {
	case "migrate":
		return 6 * time.Hour
	case "snapshot":
		return time.Hour
	default:
		return 15 * time.Minute
	}
}

var errTooLong = errors.New("de Proxmox-taak duurt te lang; hij loopt in Proxmox misschien nog door")

// runVMAction voert een actie uit als taak: de Proxmox-taak starten, het id
// bewaren en wachten tot hij klaar is. Na een herstart van de server wacht
// de taak verder op dezelfde Proxmox-taak in plaats van opnieuw te beginnen.
func (s *Service) runVMAction(ctx context.Context, j *jobs.Job) error {
	var p vmActionParams
	if err := j.Decode(&p); err != nil {
		return err
	}
	// Na afloop meteen syncen, zodat de nieuwe toestand zichtbaar is.
	defer func() {
		sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		_ = s.Sync(sctx, p.ConnectionID)
	}()
	ctx, cancel := context.WithTimeoutCause(ctx, actionTimeout(p.Action), errTooLong)
	defer cancel()

	return j.Step(ctx, j.Title, func(ctx context.Context, st *jobs.Step) error {
		api, _, err := s.apiByID(ctx, p.ConnectionID)
		if err != nil {
			return err
		}
		var state struct {
			UPID string `json:"upid"`
		}
		if !st.State(&state) || state.UPID == "" {
			g, err := locate(ctx, api, p.VMID)
			if err != nil {
				return err
			}
			upid, err := issue(ctx, api, g, p)
			if err != nil {
				return err
			}
			state.UPID = upid
			if err := st.SetState(ctx, state); err != nil {
				return err
			}
		}
		return s.waitTask(ctx, api, state.UPID, st)
	})
}

// locate zoekt de VM in Proxmox zelf: sinds de laatste sync kan hij
// verhuisd zijn.
func locate(ctx context.Context, api API, vmid int) (Guest, error) {
	res, err := api.Resources(ctx)
	if err != nil {
		return Guest{}, err
	}
	for _, r := range res {
		if (r.Type == "qemu" || r.Type == "lxc") && r.VMID == vmid {
			return Guest{Type: r.Type, Node: r.Node, VMID: vmid}, nil
		}
	}
	return Guest{}, fmt.Errorf("VM %d bestaat niet meer in Proxmox", vmid)
}

func issue(ctx context.Context, api API, g Guest, p vmActionParams) (string, error) {
	switch p.Action {
	case "start", "stop", "shutdown", "reboot":
		return api.Power(ctx, g, p.Action)
	case "snapshot":
		return api.Snapshot(ctx, g, p.SnapshotName, p.Description, p.VMState)
	case "migrate":
		// Een draaiende VM gaat live over; een uitgeschakelde gewoon.
		running := false
		if res, err := api.Resources(ctx); err == nil {
			for _, r := range res {
				if r.VMID == g.VMID && (r.Type == "qemu" || r.Type == "lxc") {
					running = r.Status == "running"
				}
			}
		}
		return api.Migrate(ctx, g, p.Target, running)
	}
	return "", fmt.Errorf("onbekende actie %q", p.Action)
}

// WaitTask volgt een Proxmox-taak tot hij klaar is en neemt zijn uitvoer
// over in de stap. st mag nil zijn als er geen taak in ClusterForge bij hoort.
func (s *Service) WaitTask(ctx context.Context, api API, upid string, st *jobs.Step) error {
	return s.waitTask(ctx, api, upid, st)
}

// waitTask volgt een Proxmox-taak tot hij klaar is en neemt zijn uitvoer
// over in de stap.
func (s *Service) waitTask(ctx context.Context, api API, upid string, st *jobs.Step) error {
	t := time.NewTicker(s.TaskPoll)
	defer t.Stop()
	failures := 0
	for {
		status, err := api.TaskStatus(ctx, upid)
		switch {
		case err != nil && ctx.Err() == nil:
			// Een netwerkhapering mag; vijf keer na elkaar niet.
			failures++
			if failures >= 5 {
				return fmt.Errorf("status van de Proxmox-taak opvragen: %w", err)
			}
		case err == nil:
			failures = 0
			if st != nil {
				if lines, err := api.TaskLog(ctx, upid); err == nil {
					st.SetLog(lines)
				}
			}
			if !status.Running() {
				if st != nil {
					_ = st.Flush(ctx)
				}
				if status.OK() {
					return nil
				}
				return fmt.Errorf("taak in Proxmox mislukt: %s", status.ExitStatus)
			}
			if st != nil {
				_ = st.Flush(ctx)
			}
		}
		select {
		case <-ctx.Done():
			cause := context.Cause(ctx)
			if errors.Is(cause, jobs.ErrCanceled) {
				sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
				defer cancel()
				err := api.StopTask(sctx, upid)
				switch {
				case st == nil:
				case err != nil:
					st.Logf("Proxmox-taak stoppen mislukt: %v", err)
				default:
					st.Logf("Proxmox-taak gestopt")
				}
			}
			return cause
		case <-t.C:
		}
	}
}
