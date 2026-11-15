package deploy

// Fase 17: een uitrol met nieuwe CT's. De agent op de Proxmox-host maakt de
// CT (pct create), start hem en zet cf-agent erin met een aanmeldbestand;
// daarna gaat de uitrol verder zoals altijd.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"path"
	"slices"
	"strings"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/jobs"
	"github.com/Jonasz1996/clusterforge/internal/noderun"
	"github.com/Jonasz1996/clusterforge/internal/proxmox"
	"github.com/Jonasz1996/clusterforge/internal/pvehost"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/internal/templates"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// HostAgents zijn de agents op de Proxmox-hosts; pvehost.Service.
type HostAgents interface {
	ForConnection(ctx context.Context, connID uuid.UUID) (map[string]pvehost.Host, error)
	Inventory(ctx context.Context, nodeID uuid.UUID) (protocol.PVEInventory, error)
	Do(ctx context.Context, log noderun.Log, ss noderun.StepState, nodeID uuid.UUID, op protocol.PVERun, title string) (protocol.RunStatus, error)
	BootstrapSpec(ctx context.Context, actor events.Actor, serverURL, ctName, ip string, nodeID *uuid.UUID) (*protocol.PVEBootstrapSpec, error)
}

// reserved zegt of a een van de eerste drie adressen van subnet is: daar
// staan bij Jonas de CARP-VIP (.1) en de twee firewalls (.2, .3).
func reserved(subnet netip.Prefix, a netip.Addr) bool {
	first := subnet.Masked().Addr()
	for range 3 {
		first = first.Next()
		if a == first {
			return true
		}
	}
	return false
}

// firstFree is het laatste octet van het eerste adres na de gereserveerde.
func firstFree(subnet netip.Prefix) int {
	a := subnet.Masked().Addr()
	for range 4 {
		a = a.Next()
	}
	return int(a.As4()[3])
}

// readyHosts zijn de hosts van de koppeling met een agent die CT's kan
// maken.
func (s *Service) readyHosts(ctx context.Context, connID uuid.UUID) (map[string]pvehost.Host, error) {
	if s.Hosts == nil {
		return nil, invalid("target.create", "deze server kan geen CT's maken via een Proxmox-host")
	}
	all, err := s.Hosts.ForConnection(ctx, connID)
	if err != nil {
		return nil, err
	}
	out := map[string]pvehost.Host{}
	for name, h := range all {
		if h.Ready() == "" {
			out[name] = h
		}
	}
	return out, nil
}

// checkNewCT controleert een uitrol met nieuwe CT's.
func (s *Service) checkNewCT(ctx context.Context, t *Target, p *params) error {
	hosts, err := s.readyHosts(ctx, t.ProxmoxID)
	if err != nil {
		return err
	}
	if len(hosts) == 0 {
		all, _ := s.Hosts.ForConnection(ctx, t.ProxmoxID)
		for _, h := range all {
			return invalid("target.create", "geen Proxmox-host met een agent die CT's kan maken: %s", h.Ready())
		}
		return invalid("target.create", "geen Proxmox-host met een agent; installeer de agent één keer op je Proxmox-hosts via Nodes → Agent installeren")
	}
	t.ImageVMID, t.ImageType = 0, "lxc"
	t.OSTemplate = strings.TrimSpace(t.OSTemplate)
	t.TemplateStorage = strings.TrimSpace(t.TemplateStorage)
	if t.OSTemplate != "" && !protocol.ValidPVEVolid(t.OSTemplate) {
		return invalid("target.ostemplate", "%q is geen containertemplate (zoals local:vztmpl/debian-13-standard_13.1-2_amd64.tar.zst)", t.OSTemplate)
	}
	if t.TemplateStorage == "" {
		t.TemplateStorage = "local"
	}
	if !protocol.ValidPVEStorage(t.TemplateStorage) {
		return invalid("target.template_storage", "ongeldige storage %q", t.TemplateStorage)
	}
	if t.Storage != "" {
		storages, err := s.pve.Storages(ctx, t.ProxmoxID)
		if err != nil {
			return err
		}
		if !slices.ContainsFunc(storages, func(st proxmox.StorageInfo) bool { return st.Name == t.Storage }) {
			return invalid("target.storage", "storage %s bestaat niet in Proxmox", t.Storage)
		}
	}
	p.ImageName = "Debian 13 (nieuwste debian-13-standard)"
	if t.OSTemplate != "" {
		_, file, _ := strings.Cut(t.OSTemplate, ":vztmpl/")
		p.ImageName = file
	}
	// Er is geen template om te klonen: niets te verhuizen, en de schijf
	// komt helemaal van de uitrol.
	p.ImageShared, p.ImageStorage, p.ImageSizeGiB = true, "", 0
	return nil
}

// planVMIDs controleert de gekozen nummers en stelt er een voor bij nieuwe
// CT's: het eerste vrije vanaf 100 dat niet in Proxmox staat en niet
// gekozen is. Bij klonen kiest Proxmox het bij het maken.
func (s *Service) planVMIDs(ctx context.Context, p *params) error {
	rows, err := s.q.ListProxmoxResources(ctx, p.Target.ProxmoxID)
	if err != nil {
		return err
	}
	used := map[int]bool{}
	for _, row := range rows {
		if v := row.ProxmoxResource.Vmid; v != nil {
			used[int(*v)] = true
		}
	}
	chosen := map[int]bool{}
	for _, n := range p.Nodes {
		if !n.VMIDChosen {
			continue
		}
		switch {
		case n.VMID < 100 || n.VMID > 999999999:
			return invalid("target.placement", "%s: nummer %d moet tussen 100 en 999999999 liggen", n.Hostname, n.VMID)
		case used[n.VMID]:
			return invalid("target.placement", "%s: nummer %d is al in gebruik in Proxmox", n.Hostname, n.VMID)
		case chosen[n.VMID]:
			return invalid("target.placement", "nummer %d staat bij twee nodes", n.VMID)
		}
		chosen[n.VMID] = true
	}
	if !p.Target.NewCT() {
		return nil
	}
	next := 100
	for i := range p.Nodes {
		n := &p.Nodes[i]
		if n.VMIDChosen {
			continue
		}
		for used[next] || chosen[next] {
			next++
		}
		n.VMID = next
		next++
	}
	return nil
}

// bestStorage kiest op host de actieve storage voor containers met de
// meeste vrije ruimte.
func bestStorage(storages []proxmox.StorageInfo, host string) (string, bool) {
	best, free := "", int64(-1)
	for _, st := range storages {
		if !st.On(host) || !st.Holds("lxc") || !st.Active {
			continue
		}
		if st.Free > free {
			best, free = st.Name, st.Free
		}
	}
	return best, best != ""
}

// usesDocker zegt of de template Docker installeert: dan krijgt een
// onbevoegde CT keyctl=1 erbij. De stappen worden niet gerenderd (daar
// horen geheimen bij); de ruwe stappen noemen Docker of niet.
func usesDocker(tpl *templates.Template, _ *params) bool {
	if tpl.ClusterType == "docker" {
		return true
	}
	for _, r := range tpl.Roles {
		raw, err := json.Marshal(r.Steps)
		if err != nil || strings.Contains(strings.ToLower(string(raw)), "docker") {
			return true
		}
	}
	return false
}

// subState bewaart de state van een run binnen de state van de stap die
// de CT maakt.
type subState struct {
	st *jobs.Step
	vs *vmState
}

func (a subState) State(v any) bool {
	if len(a.vs.Run) == 0 {
		return false
	}
	return json.Unmarshal(a.vs.Run, v) == nil
}

func (a subState) SetState(ctx context.Context, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	a.vs.Run = b
	return a.st.SetState(ctx, *a.vs)
}

// hostAgent geeft de agent van de host van een node.
func (r *runCtx) hostAgent(ctx context.Context, host string) (pvehost.Host, error) {
	if r.s.Hosts == nil {
		return pvehost.Host{}, errors.New("deze server kan geen CT's maken via een Proxmox-host")
	}
	all, err := r.s.Hosts.ForConnection(ctx, r.p.Target.ProxmoxID)
	if err != nil {
		return pvehost.Host{}, err
	}
	h, ok := all[host]
	if !ok {
		return pvehost.Host{}, fmt.Errorf("Proxmox-host %s heeft geen agent; installeer hem via Nodes → Agent installeren", host)
	}
	if why := h.Ready(); why != "" {
		return pvehost.Host{}, errors.New(why)
	}
	return h, nil
}

// pveDo voert een actie uit op de host en maakt van een mislukte run een
// fout.
func (r *runCtx) pveDo(ctx context.Context, st *jobs.Step, vs *vmState, host pvehost.Host, op protocol.PVERun) (protocol.RunStatus, error) {
	title := pvehost.Describe(op) + " op " + host.PVENode
	res, err := r.s.Hosts.Do(ctx, st, subState{st, vs}, host.NodeID, op, title)
	if err != nil {
		return res, err
	}
	if res.State != protocol.RunOK {
		return res, errors.New(noderun.Describe(res))
	}
	return res, nil
}

// createCT maakt de CT van een node via de agent op zijn host.
func (r *runCtx) createCT(ctx context.Context, st *jobs.Step, n *plannedNode) error {
	var vs vmState
	st.State(&vs)
	if vs.Phase == "" {
		vs.Phase = "template"
	}
	next := func(phase string) error {
		vs.Phase, vs.Run = phase, nil
		return r.saveVM(ctx, st, vs)
	}
	host, err := r.hostAgent(ctx, n.Host)
	if err != nil {
		return err
	}
	t := r.p.Target

	if vs.Phase == "template" {
		spec := &protocol.PVETemplateSpec{Storage: t.TemplateStorage, Match: protocol.PVEDefaultTemplate}
		if spec.Storage == "" {
			spec.Storage = "local"
		}
		if t.OSTemplate != "" {
			storage, file, _ := strings.Cut(t.OSTemplate, ":vztmpl/")
			spec = &protocol.PVETemplateSpec{Storage: storage, File: path.Base(file)}
		}
		res, err := r.pveDo(ctx, st, &vs, host, protocol.PVERun{Action: protocol.PVETemplate, Template: spec})
		if err != nil {
			return fmt.Errorf("containertemplate op %s: %w", n.Host, err)
		}
		if res.PVE == nil || res.PVE.Volid == "" {
			return fmt.Errorf("de agent op %s gaf geen containertemplate terug", n.Host)
		}
		vs.OSTemplate = res.PVE.Volid
		if err := next("create"); err != nil {
			return err
		}
	}

	if vs.Phase == "create" {
		if vs.VMID == 0 {
			vmid := n.VMID
			if !n.VMIDChosen {
				// Het voorgestelde nummer kan intussen bezet zijn.
				inv, err := r.s.Hosts.Inventory(ctx, host.NodeID)
				if err != nil {
					return err
				}
				vmid = r.freeVMID(inv.UsedIDs, n)
			}
			vs.VMID = vmid
			if err := r.saveVM(ctx, st, vs); err != nil {
				return err
			}
		}
		storage := n.Storage
		c := &protocol.PVECreate{
			Hostname: n.Hostname, OSTemplate: vs.OSTemplate, Cores: n.VM.CPU, MemoryMiB: n.VM.MemoryMiB,
			SwapMiB: min(512, n.VM.MemoryMiB), Storage: storage, DiskGiB: n.VM.DiskGiB, Bridge: t.Bridge,
			DNS: t.DNS, HostKeys: true, Privileged: t.Privileged, Nesting: true, Keyctl: r.docker, OnBoot: true,
			Tags: []string{"clusterforge", strings.ToLower(r.p.Cluster.Slug)},
			Description: fmt.Sprintf("Beheerd door ClusterForge: cluster %s, rol %s, template %s %s.", r.p.Cluster.Name, n.Role, r.p.Template, r.p.Version),
		}
		if t.VLAN != nil {
			c.VLAN = *t.VLAN
		}
		if n.Address != "" {
			c.Address, c.Gateway = fmt.Sprintf("%s/%d", n.Address, n.Prefix), t.Gateway
		}
		for line := range strings.Lines(t.SSHKeys) {
			if l := strings.TrimSpace(line); l != "" {
				c.SSHKeys = append(c.SSHKeys, l)
			}
		}
		st.Logf("CT %d (%s) op %s: %d vCPU, %d MB geheugen, %d GB op %s, %s", vs.VMID, n.Hostname, n.Host, c.Cores, c.MemoryMiB, c.DiskGiB, storage,
			or(c.Address, "DHCP"))
		res, err := r.pveDo(ctx, st, &vs, host, protocol.PVERun{Action: protocol.PVECreateCT, Type: "lxc", VMID: vs.VMID, Create: c})
		if err != nil {
			return fmt.Errorf("CT maken: %w", err)
		}
		if res.PVE != nil && res.PVE.Existed {
			st.Logf("CT %d bestond al (eerdere poging)", vs.VMID)
		}
		if err := r.s.q.SetNodeProxmox(ctx, store.SetNodeProxmoxParams{
			ID: n.ID, ProxmoxID: &r.p.Target.ProxmoxID, PveVmid: int32p(vs.VMID),
		}); err != nil {
			return err
		}
		if err := next("start"); err != nil {
			return err
		}
	}

	if vs.Phase == "start" {
		running := false
		if inv, err := r.s.Hosts.Inventory(ctx, host.NodeID); err == nil {
			for _, g := range inv.Guests {
				if g.VMID == vs.VMID && g.Status == "running" {
					running = true
				}
			}
		}
		if !running {
			if _, err := r.pveDo(ctx, st, &vs, host, protocol.PVERun{Action: protocol.PVEStart, Type: "lxc", VMID: vs.VMID}); err != nil {
				return fmt.Errorf("CT starten: %w", err)
			}
		}
		if err := next("agent"); err != nil {
			return err
		}
	}

	if vs.Phase == "agent" {
		if ok, err := r.enrolled(ctx, n); err != nil {
			return err
		} else if !ok {
			b, err := r.s.Hosts.BootstrapSpec(ctx, r.actor, r.p.ServerURL, n.Hostname, n.Address, &n.ID)
			if err != nil {
				return err
			}
			if _, err := r.pveDo(ctx, st, &vs, host, protocol.PVERun{Action: protocol.PVEBootstrap, Type: "lxc", VMID: vs.VMID, Bootstrap: b}); err != nil {
				return fmt.Errorf("cf-agent in de CT zetten: %w", err)
			}
		}
		if err := next("done"); err != nil {
			return err
		}
	}
	st.Logf("CT %d draait op %s, met cf-agent", vs.VMID, n.Host)
	return nil
}

// freeVMID is het eerste vrije nummer vanaf het voorgestelde, dat ook
// geen andere node van deze uitrol heeft.
func (r *runCtx) freeVMID(usedIDs []int, n *plannedNode) int {
	used := map[int]bool{}
	for _, id := range usedIDs {
		used[id] = true
	}
	for _, o := range r.p.Nodes {
		if o.ID != n.ID && o.VMID > 0 {
			used[o.VMID] = true
		}
	}
	id := max(100, n.VMID)
	for used[id] {
		id++
	}
	return id
}
