package kubenodes

// Dit bestand is naast de sandboxen van de back-upcontrole de enige plek die
// een VM mag verwijderen: de VM van een worker die ClusterForge zelf maakte,
// pas als hij leeg is, uit staat en niet meer in Kubernetes zit.
// TestSandboxCallers en TestDestroyGuard houden dat zo.

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Jonasz1996/clusterforge/internal/deploy"
	"github.com/Jonasz1996/clusterforge/internal/jobs"
	"github.com/Jonasz1996/clusterforge/internal/proxmox"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/internal/templates"
)

// GuardError betekent dat een controle voor uitzetten of verwijderen faalde;
// de VM is niet aangeraakt.
type GuardError struct{ Msg string }

func (e *GuardError) Error() string { return e.Msg }

// errVMGone: de VM bestaat niet meer in Proxmox.
var errVMGone = errors.New("de VM bestaat niet meer in Proxmox")

// workerVM is de VM van een worker die alle controles van guard doorstond.
type workerVM struct {
	api proxmox.API
	res proxmox.Resource
}

func (v workerVM) guest() proxmox.Guest {
	return proxmox.Guest{Type: "qemu", Node: v.res.Node, VMID: v.res.VMID}
}

// guard controleert, telkens opnieuw, dat de VM van p de VM is van een worker
// die weggehaald wordt: de node hoort nog bij dit Kubernetes-cluster, is uit
// dienst, heeft dezelfde VM en hostname als bij het plannen, en is een worker
// in de spec. In Proxmox is het een gewone VM met die naam, geen golden
// image, geen template en geen sandbox.
func (s *Service) guard(ctx context.Context, clusterID uuid.UUID, p params) (workerVM, error) {
	return s.check(ctx, clusterID, p, true)
}

// check is guard; voor het plannen hoeft de node nog niet uit dienst te zijn.
func (s *Service) check(ctx context.Context, clusterID uuid.UUID, p params, retired bool) (workerVM, error) {
	row, err := s.q.GetNode(ctx, p.NodeID)
	if errors.Is(err, pgx.ErrNoRows) {
		return workerVM{}, &GuardError{p.Hostname + " bestaat niet meer in ClusterForge"}
	}
	if err != nil {
		return workerVM{}, err
	}
	n := row.Node
	switch {
	case n.ClusterID == nil || *n.ClusterID != clusterID:
		return workerVM{}, &GuardError{p.Hostname + " hoort niet meer bij dit cluster"}
	case n.Hostname != p.Hostname:
		return workerVM{}, &GuardError{fmt.Sprintf("de node heet nu %s, niet meer %s", n.Hostname, p.Hostname)}
	case n.ProxmoxID == nil || *n.ProxmoxID != p.ProxmoxID || n.PveVmid == nil || int(*n.PveVmid) != p.VMID:
		return workerVM{}, &GuardError{fmt.Sprintf("%s is niet meer gekoppeld aan VM %d", p.Hostname, p.VMID)}
	case retired && n.Lifecycle != store.NodeLifecycleDecommissioned:
		return workerVM{}, &GuardError{fmt.Sprintf("%s is niet uit dienst maar %s", p.Hostname, n.Lifecycle)}
	}
	c, err := s.kube.Cluster(ctx, clusterID)
	if err != nil {
		return workerVM{}, err
	}
	inv, _, err := s.dep.Nodes(ctx, clusterID)
	if err != nil {
		return workerVM{}, err
	}
	spec, err := deploy.ParseSpec(c.Spec, templates.ClusterInfo{Name: c.Name, Slug: c.Slug, Environment: string(c.Environment)}, inv)
	if err != nil {
		return workerVM{}, err
	}
	i := slices.IndexFunc(spec.Nodes, func(x deploy.SpecNode) bool { return x.NodeID == p.NodeID })
	switch {
	case i < 0:
		return workerVM{}, &GuardError{p.Hostname + " staat niet in de specificatie"}
	case spec.Nodes[i].Role != "worker":
		return workerVM{}, &GuardError{fmt.Sprintf("%s is geen worker maar %s", p.Hostname, spec.Nodes[i].Role)}
	case spec.Target.ProxmoxID != p.ProxmoxID:
		return workerVM{}, &GuardError{"de VM staat niet in de Proxmox-omgeving waar het cluster naar uitrolt"}
	case spec.Target.ImageVMID == p.VMID:
		return workerVM{}, &GuardError{fmt.Sprintf("VM %d is de golden image", p.VMID)}
	}
	sandbox, err := s.q.IsSandboxGuest(ctx, store.IsSandboxGuestParams{ConnectionID: p.ProxmoxID, Vmid: int32(p.VMID)}) //nolint:gosec // een VMID past in int32
	if err != nil {
		return workerVM{}, err
	}
	if sandbox {
		return workerVM{}, &GuardError{fmt.Sprintf("VM %d is een sandbox van een back-upcontrole", p.VMID)}
	}

	api, _, err := s.pve.APIFor(ctx, p.ProxmoxID)
	if err != nil {
		return workerVM{}, err
	}
	res, err := api.Resources(ctx)
	if err != nil {
		return workerVM{}, err
	}
	for _, r := range res {
		if (r.Type != "qemu" && r.Type != "lxc") || r.VMID != p.VMID {
			continue
		}
		switch {
		case r.Type != "qemu":
			return workerVM{}, &GuardError{fmt.Sprintf("%d is een container in Proxmox, geen VM", p.VMID)}
		case r.Template != 0:
			return workerVM{}, &GuardError{fmt.Sprintf("VM %d is een template in Proxmox", p.VMID)}
		case r.Name != p.Hostname:
			return workerVM{}, &GuardError{fmt.Sprintf("VM %d heet %q in Proxmox, niet %s", p.VMID, r.Name, p.Hostname)}
		case r.Pool == proxmox.SandboxPool:
			return workerVM{}, &GuardError{fmt.Sprintf("VM %d zit in pool %s", p.VMID, proxmox.SandboxPool)}
		}
		return workerVM{api: api, res: r}, nil
	}
	return workerVM{}, errVMGone
}

// powerOff zet de VM van de worker uit: eerst netjes, en na ShutdownTimeout
// hard. Een VM die al uit staat of al weg is, is goed.
func (s *Service) powerOff(ctx context.Context, st *jobs.Step, clusterID uuid.UUID, p params) error {
	vm, err := s.guard(ctx, clusterID, p)
	if errors.Is(err, errVMGone) {
		st.Logf("VM %d bestaat al niet meer in Proxmox", p.VMID)
		return nil
	}
	if err != nil {
		return err
	}
	if vm.res.Status == "stopped" {
		st.Logf("VM %d staat al uit", p.VMID)
		return nil
	}
	if vm.res.Lock != "" {
		return &GuardError{fmt.Sprintf("VM %d is vergrendeld in Proxmox (lock %s); probeer het opnieuw als dat klaar is", p.VMID, vm.res.Lock)}
	}
	st.Logf("VM %d netjes afsluiten", p.VMID)
	_ = st.Flush(ctx)
	upid, err := vm.api.Power(ctx, vm.guest(), "shutdown")
	if err != nil {
		return fmt.Errorf("afsluiten: %w", err)
	}
	deadline := time.Now().Add(s.ShutdownTimeout)
	for {
		select {
		case <-ctx.Done():
			return context.Cause(ctx)
		case <-time.After(s.Poll):
		}
		vm, err = s.guard(ctx, clusterID, p)
		if errors.Is(err, errVMGone) {
			st.Logf("VM %d bestaat niet meer in Proxmox", p.VMID)
			return nil
		}
		if err != nil {
			return err
		}
		if vm.res.Status == "stopped" {
			st.Logf("VM %d staat uit", p.VMID)
			return nil
		}
		if time.Now().After(deadline) {
			break
		}
	}
	st.Logf("VM %d is na %s nog niet uit; hard uitzetten", p.VMID, s.ShutdownTimeout)
	// De afsluittaak eerst stoppen, anders wacht stop daarop.
	_ = vm.api.StopTask(ctx, upid)
	upid, err = vm.api.Power(ctx, vm.guest(), "stop")
	if err == nil {
		err = s.pve.WaitTask(ctx, vm.api, upid, st)
	}
	if err != nil {
		return fmt.Errorf("hard uitzetten: %w", err)
	}
	st.Logf("VM %d staat uit", p.VMID)
	return nil
}

// destroyWorker verwijdert de VM van de worker met purge, alleen als guard
// klopt, Kubernetes de node niet meer kent, de VM uit staat, niet
// vergrendeld is en geen protection heeft. Protection zet ClusterForge niet
// zelf uit: wie dat aanzette, wilde de VM houden. Een VM die al weg is, is
// goed.
func (s *Service) destroyWorker(ctx context.Context, st *jobs.Step, clusterID uuid.UUID, p params) error {
	vm, err := s.guard(ctx, clusterID, p)
	if errors.Is(err, errVMGone) {
		st.Logf("VM %d bestaat al niet meer in Proxmox", p.VMID)
		return nil
	}
	if err != nil {
		return err
	}
	gone, err := s.kube.NodeGone(ctx, clusterID, p.Node)
	switch {
	case err != nil:
		return fmt.Errorf("nakijken of %s uit Kubernetes is: %w", p.Node, err)
	case !gone:
		return &GuardError{p.Node + " staat nog in Kubernetes"}
	case vm.res.Status != "stopped":
		return &GuardError{fmt.Sprintf("VM %d staat niet uit maar %s", p.VMID, vm.res.Status)}
	case vm.res.Lock != "":
		return &GuardError{fmt.Sprintf("VM %d is vergrendeld in Proxmox (lock %s); probeer het opnieuw als dat klaar is", p.VMID, vm.res.Lock)}
	}
	if err := protected(ctx, vm, p.VMID); err != nil {
		return err
	}
	st.Logf("VM %d verwijderen (purge)", p.VMID)
	upid, err := vm.api.Destroy(ctx, vm.guest())
	if err == nil {
		err = s.pve.WaitTask(ctx, vm.api, upid, st)
	}
	if err != nil {
		return fmt.Errorf("verwijderen: %w", err)
	}
	// Pas weg als Proxmox hem ook echt niet meer toont.
	if _, err := s.guard(ctx, clusterID, p); !errors.Is(err, errVMGone) {
		if err == nil {
			err = fmt.Errorf("VM %d staat er na het verwijderen nog", p.VMID)
		}
		return err
	}
	st.Logf("VM %d is verwijderd", p.VMID)
	return nil
}

// protected is een GuardError als de VM protection aan heeft in Proxmox.
func protected(ctx context.Context, vm workerVM, vmid int) error {
	cfg, err := vm.api.Config(ctx, vm.guest())
	if err != nil {
		return fmt.Errorf("config lezen: %w", err)
	}
	if fmt.Sprint(cfg["protection"]) == "1" {
		return &GuardError{fmt.Sprintf("VM %d heeft protection aan in Proxmox. Zet het daar uit als hij weg mag, en probeer het opnieuw", vmid)}
	}
	return nil
}
