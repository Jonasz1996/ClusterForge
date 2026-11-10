package deploy

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/Jonasz1996/clusterforge/internal/proxmox"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

var hostRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9.-]{0,62}$`)

func placementKey(role string, index int) string { return fmt.Sprintf("%s/%d", role, index) }

func placementIndex(list []Placement) map[string]Placement {
	out := map[string]Placement{}
	for _, pl := range list {
		out[placementKey(pl.Role, pl.Index)] = pl
	}
	return out
}

// checkPlacement controleert de vorm van de keuzes per node; of host en
// storage bestaan, controleert placeNodes.
func checkPlacement(t *Target) error {
	if len(t.Placement) > 100 {
		return invalid("target.placement", "te veel nodes in de plaatsing")
	}
	seen := map[string]bool{}
	var keep []Placement
	for _, pl := range t.Placement {
		pl.Role, pl.Host, pl.Storage = strings.TrimSpace(pl.Role), strings.TrimSpace(pl.Host), strings.TrimSpace(pl.Storage)
		if pl.Host == "" && pl.Storage == "" {
			continue
		}
		if pl.Role == "" || pl.Index < 1 {
			return invalid("target.placement", "elke keuze per node noemt een rol en een volgnummer")
		}
		if pl.Host != "" && !hostRe.MatchString(pl.Host) {
			return invalid("target.placement", "ongeldige Proxmox-host %q", pl.Host)
		}
		if pl.Storage != "" && !storageRe.MatchString(pl.Storage) {
			return invalid("target.placement", "ongeldige storage %q", pl.Storage)
		}
		k := placementKey(pl.Role, pl.Index)
		if seen[k] {
			return invalid("target.placement", "%s staat twee keer in de plaatsing", k)
		}
		seen[k] = true
		keep = append(keep, pl)
	}
	t.Placement = keep
	return nil
}

// placeNodes kiest per VM of CT de Proxmox-host en de storage. Wat in
// Target.Placement gekozen is, blijft; de rest verdeelt ClusterForge over
// de online hosts, zodat twee nodes van een cluster niet op dezelfde host
// staan als het kan. Staat de template niet op gedeelde storage, dan kloont
// de uitrol op de host van de template en verhuist de kopie daarna.
func (s *Service) placeNodes(ctx context.Context, p *params) error {
	hosts, err := s.pve.Hosts(ctx, p.Target.ProxmoxID)
	if err != nil {
		return err
	}
	storages, err := s.pve.Storages(ctx, p.Target.ProxmoxID)
	if err != nil {
		return err
	}
	img, err := s.q.GetProxmoxGuest(ctx, store.GetProxmoxGuestParams{ConnectionID: p.Target.ProxmoxID, Vmid: int32p(p.Target.ImageVMID)})
	if err != nil {
		return err
	}
	guestType := p.Target.ImageType
	if guestType == "" {
		guestType = img.Type
	}
	kind := "VM's"
	if guestType == "lxc" {
		kind = "CT's"
	}
	online := map[string]proxmox.HostInfo{}
	var names []string
	for _, h := range hosts {
		if h.Online {
			online[h.Name] = h
			names = append(names, h.Name)
		}
	}
	sort.Strings(names)
	if len(names) == 0 {
		return invalid("target.placement", "er is geen Proxmox-host online")
	}
	find := func(name, host string) (proxmox.StorageInfo, bool) {
		for _, st := range storages {
			if st.Name == name && st.On(host) {
				return st, true
			}
		}
		return proxmox.StorageInfo{}, false
	}
	// fits zegt waarom een node niet op host kan, of leeg als het kan.
	fits := func(n *plannedNode, host string) string {
		name := or(n.Storage, p.ImageStorage)
		if name == "" {
			return ""
		}
		st, ok := find(name, host)
		switch {
		case !ok:
			return fmt.Sprintf("storage %s bestaat niet op %s", name, host)
		case !st.Holds(guestType):
			return fmt.Sprintf("op storage %s mogen geen %s (zet in Proxmox bij de storage %s aan)", name, kind, map[bool]string{true: "Container", false: "Disk image"}[guestType == "lxc"])
		case !st.Active:
			return fmt.Sprintf("storage %s is niet actief op %s", name, host)
		}
		return ""
	}

	chosen := placementIndex(p.Target.Placement)
	count := map[string]int{}
	needMem := map[string]int64{}
	place := func(n *plannedNode, host string) {
		n.Host = host
		count[host]++
		needMem[host] += int64(n.VM.MemoryMiB) << 20
	}
	for i := range p.Nodes {
		n := &p.Nodes[i]
		pl := chosen[placementKey(n.Role, n.Index)]
		n.Storage = or(pl.Storage, p.Target.Storage)
		n.Host = ""
		if pl.Host == "" {
			continue
		}
		if _, ok := online[pl.Host]; !ok {
			return invalid("target.placement", "%s: Proxmox-host %s bestaat niet of is offline", n.Hostname, pl.Host)
		}
		if why := fits(n, pl.Host); why != "" {
			return invalid("target.placement", "%s: %s", n.Hostname, why)
		}
		place(n, pl.Host)
	}
	for i := range p.Nodes {
		n := &p.Nodes[i]
		if n.Host != "" {
			continue
		}
		best := ""
		var why string
		for _, h := range names {
			if w := fits(n, h); w != "" {
				why = w
				continue
			}
			if best == "" {
				best = h
				continue
			}
			// Eerst de host met de minste nodes van dit cluster, dan die
			// met het meeste vrije geheugen, dan zonder verhuizing.
			bh, hh := online[best], online[h]
			bf, hf := bh.FreeMem-needMem[best], hh.FreeMem-needMem[h]
			switch {
			case count[h] != count[best]:
				if count[h] < count[best] {
					best = h
				}
			case hf != bf:
				if hf > bf {
					best = h
				}
			case !p.ImageShared && h == img.PveNode:
				best = h
			}
		}
		if best == "" {
			field := "target.placement"
			if p.Target.Storage != "" && n.Storage == p.Target.Storage {
				field = "target.storage"
			}
			return invalid(field, "%s: geen online Proxmox-host past (%s)", n.Hostname, why)
		}
		place(n, best)
	}

	for _, h := range names {
		if needMem[h] > online[h].FreeMem {
			// Het veld met het geheugen van de eerste rol, zoals memory of
			// server_memory.
			field := "params.memory"
			if tpl, ok := s.Templates.Get(p.Template, p.Version); ok {
				if name := tpl.MemoryParam(); name != "" {
					field = "params." + name
				}
			}
			return invalid(field, "op %s is maar %d MB geheugen vrij; de nieuwe %s vragen %d MB",
				h, online[h].FreeMem>>20, kind, needMem[h]>>20)
		}
	}

	// Vrije ruimte per storage: een gedeelde storage telt één keer.
	type slot struct{ name, node string }
	needDisk := map[slot]int64{}
	chosenStorage := map[slot]bool{}
	for _, n := range p.Nodes {
		name := or(n.Storage, p.ImageStorage)
		st, ok := find(name, n.Host)
		if !ok {
			continue
		}
		k := slot{st.Name, st.Node}
		if st.Shared {
			k.node = ""
		}
		needDisk[k] += int64(max(n.VM.DiskGiB, p.ImageSizeGiB)) << 30
		if chosen[placementKey(n.Role, n.Index)].Storage != "" {
			chosenStorage[k] = true
		}
	}
	for _, st := range storages {
		k := slot{st.Name, st.Node}
		if st.Shared {
			k.node = ""
		}
		need := needDisk[k]
		if need == 0 || st.Total == 0 || need <= st.Free {
			continue
		}
		where := st.Name
		if !st.Shared {
			where += " op " + st.Node
		}
		field := "target.storage"
		if chosenStorage[k] || p.Target.Storage == "" {
			field = "target.placement"
		}
		return invalid(field, "op storage %s is maar %d GB vrij; de nieuwe %s vragen %d GB", where, st.Free>>30, kind, need>>30)
	}
	return nil
}
