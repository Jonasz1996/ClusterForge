package proxmox

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/internal/templates"
)

// API geeft de client van een koppeling, voor taken buiten dit pakket zoals
// een uitrol.
func (s *Service) API(ctx context.Context, id uuid.UUID) (API, error) {
	api, _, err := s.apiByID(ctx, id)
	return api, err
}

// Image beschrijft een golden image: een VM-template om nieuwe VM's uit te
// klonen.
type Image struct {
	VMID    int
	Name    string
	Node    string
	Disk    string
	Storage string
	SizeGiB int
	// CloudInit is true als de template een cloud-init-schijf heeft.
	CloudInit bool
	// Shared is true als de schijf op gedeelde storage staat; dan kan een
	// kloon naar elke host.
	Shared bool
}

// Image leest de configuratie van een golden image uit Proxmox.
func (s *Service) Image(ctx context.Context, connID uuid.UUID, vmid int) (Image, error) {
	r, err := s.q.GetProxmoxGuest(ctx, store.GetProxmoxGuestParams{ConnectionID: connID, Vmid: int32p(vmid)})
	if err != nil {
		return Image{}, translate(err)
	}
	if r.Type != "qemu" || !r.Template {
		return Image{}, ValidationError{fmt.Sprintf("VM %d is geen VM-template; maak er een met het golden-image-script", vmid)}
	}
	api, _, err := s.apiByID(ctx, connID)
	if err != nil {
		return Image{}, err
	}
	cfg, err := api.Config(ctx, Guest{Type: "qemu", Node: r.PveNode, VMID: vmid})
	if err != nil {
		return Image{}, &UpstreamError{err}
	}
	img := Image{VMID: vmid, Name: r.Name, Node: r.PveNode}
	var ok bool
	img.Disk, img.Storage, img.SizeGiB, ok = BootDisk(cfg)
	if !ok {
		return Image{}, ValidationError{fmt.Sprintf("VM-template %d heeft geen schijf om van op te starten", vmid)}
	}
	for _, v := range cfg {
		if s, isStr := v.(string); isStr && strings.Contains(s, "cloudinit") {
			img.CloudInit = true
		}
	}
	storages, err := s.Storages(ctx, connID)
	if err != nil {
		return Image{}, err
	}
	img.Shared = slices.ContainsFunc(storages, func(st StorageInfo) bool { return st.Name == img.Storage && st.Shared })
	return img, nil
}

// BootDisk zoekt de schijf waarvan de VM opstart: naam (scsi0), storage en
// grootte in hele GB, naar boven afgerond.
func BootDisk(cfg map[string]any) (disk, storage string, sizeGiB int, ok bool) {
	var order []string
	if boot, _ := cfg["boot"].(string); strings.HasPrefix(boot, "order=") {
		order = strings.Split(strings.TrimPrefix(boot, "order="), ";")
	}
	order = append(order, "scsi0", "virtio0", "sata0", "ide0")
	for _, name := range order {
		v, _ := cfg[name].(string)
		if v == "" || strings.Contains(v, "media=cdrom") || strings.Contains(v, "cloudinit") {
			continue
		}
		vol, opts, _ := strings.Cut(v, ",")
		st, _, found := strings.Cut(vol, ":")
		if !found {
			continue
		}
		for _, o := range strings.Split(opts, ",") {
			if size, isSize := strings.CutPrefix(o, "size="); isSize {
				if b, err := templates.ParseSize(size); err == nil {
					sizeGiB = int((b + 1<<30 - 1) >> 30)
				}
			}
		}
		return name, st, sizeGiB, true
	}
	return "", "", 0, false
}

// HostInfo is een Proxmox-host uit de laatste sync.
type HostInfo struct {
	Name    string
	Online  bool
	MaxMem  int64
	FreeMem int64
}

// Hosts geeft de hosts van een koppeling uit de laatste sync.
func (s *Service) Hosts(ctx context.Context, connID uuid.UUID) ([]HostInfo, error) {
	rows, err := s.q.ListProxmoxResources(ctx, connID)
	if err != nil {
		return nil, err
	}
	var out []HostInfo
	for _, row := range rows {
		r := row.ProxmoxResource
		if r.Type != "node" {
			continue
		}
		var d Resource
		_ = json.Unmarshal(r.Data, &d)
		out = append(out, HostInfo{Name: r.PveNode, Online: r.Status == "online", MaxMem: d.MaxMem, FreeMem: d.MaxMem - d.Mem})
	}
	return out, nil
}

// StorageInfo is een storage uit de laatste sync.
type StorageInfo struct {
	Name   string
	Node   string
	Shared bool
}

// Storages geeft de storages van een koppeling, één regel per host.
func (s *Service) Storages(ctx context.Context, connID uuid.UUID) ([]StorageInfo, error) {
	rows, err := s.q.ListProxmoxResources(ctx, connID)
	if err != nil {
		return nil, err
	}
	var out []StorageInfo
	for _, row := range rows {
		r := row.ProxmoxResource
		if r.Type != "storage" {
			continue
		}
		var d Resource
		_ = json.Unmarshal(r.Data, &d)
		out = append(out, StorageInfo{Name: r.Name, Node: r.PveNode, Shared: d.Shared == 1})
	}
	return out, nil
}
