package proxmox

import (
	"context"
	"fmt"
	"slices"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/jobs"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

// directPower zijn de acties die andere diensten, zoals de failovertest,
// rechtstreeks mogen vragen.
var directPower = []string{"start", "stop"}

// GuestStatus leest een VM live uit Proxmox: waar hij staat, of hij draait
// en of Proxmox HA hem beheert.
func (s *Service) GuestStatus(ctx context.Context, connID uuid.UUID, vmid int) (Resource, error) {
	api, _, err := s.apiByID(ctx, connID)
	if err != nil {
		return Resource{}, err
	}
	res, err := api.Resources(ctx)
	if err != nil {
		return Resource{}, err
	}
	for _, r := range res {
		if (r.Type == "qemu" || r.Type == "lxc") && r.VMID == vmid {
			return r, nil
		}
	}
	return Resource{}, fmt.Errorf("VM %d bestaat niet in Proxmox", vmid)
}

// PowerVM start of stopt een VM hard en geeft het id van de Proxmox-taak.
// Een sandbox van de back-upcontrole raakt het nooit aan.
func (s *Service) PowerVM(ctx context.Context, connID uuid.UUID, vmid int, action string) (string, error) {
	if !slices.Contains(directPower, action) {
		return "", fmt.Errorf("onbekende actie %q", action)
	}
	sandbox, err := s.q.IsSandboxGuest(ctx, store.IsSandboxGuestParams{ConnectionID: connID, Vmid: int32(vmid)}) //nolint:gosec // een VMID past in int32
	if err != nil {
		return "", err
	}
	if sandbox {
		return "", ConflictError{"dit is een tijdelijke sandbox van een back-upcontrole; ClusterForge start, stopt en verwijdert hem zelf"}
	}
	api, _, err := s.apiByID(ctx, connID)
	if err != nil {
		return "", err
	}
	g, err := locate(ctx, api, vmid)
	if err != nil {
		return "", err
	}
	return api.Power(ctx, g, action)
}

// WaitVMTask wacht tot een Proxmox-taak van PowerVM klaar is en neemt zijn
// uitvoer over in st, als die er is.
func (s *Service) WaitVMTask(ctx context.Context, connID uuid.UUID, upid string, st *jobs.Step) error {
	api, _, err := s.apiByID(ctx, connID)
	if err != nil {
		return err
	}
	return s.waitTask(ctx, api, upid, st)
}

// PowerAndWait start of stopt een VM en wacht tot Proxmox klaar is.
func (s *Service) PowerAndWait(ctx context.Context, connID uuid.UUID, vmid int, action string, st *jobs.Step) error {
	upid, err := s.PowerVM(ctx, connID, vmid, action)
	if err != nil {
		return err
	}
	return s.WaitVMTask(ctx, connID, upid, st)
}
