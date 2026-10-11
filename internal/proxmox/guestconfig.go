package proxmox

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"golang.org/x/sync/errgroup"

	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/internal/templates"
)

// ConfigInterval is hoe vaak de configuratie van een VM opnieuw gelezen
// wordt.
const ConfigInterval = 10 * time.Minute

// configBatch is hoeveel configuraties één ronde hoogstens leest; de rest
// volgt in de volgende ronde.
const configBatch = 40

// Disk is een schijf of mountpoint van een gast. Storage is leeg voor een
// map van de host (Path), die alleen op die host bestaat.
type Disk struct {
	Key       string `json:"key"`
	Storage   string `json:"storage,omitempty"`
	Path      string `json:"path,omitempty"`
	SizeBytes int64  `json:"size_bytes,omitempty"`
}

// NIC is een netwerkkaart van een gast.
type NIC struct {
	Key    string `json:"key"`
	Bridge string `json:"bridge"`
	Tag    int    `json:"tag,omitempty"`
	MAC    string `json:"mac,omitempty"`
	// LinkDown is true als de kaart losgekoppeld staat.
	LinkDown bool `json:"link_down,omitempty"`
}

// GuestConfig is wat ClusterForge uit de configuratie van een VM of
// container bewaart.
type GuestConfig struct {
	Disks     []Disk
	NICs      []NIC
	OnBoot    bool
	Addresses []string
}

// GuestInterface is een interface van een draaiende container, zoals
// Proxmox hem ziet.
type GuestInterface struct {
	Name   string `json:"name"`
	HWAddr string `json:"hwaddr"`
	Inet   string `json:"inet"`
	Inet6  string `json:"inet6"`
}

var (
	diskKeyRE = regexp.MustCompile(`^(scsi|virtio|sata|ide|mp)[0-9]+$|^(rootfs|efidisk0|tpmstate0)$`)
	netKeyRE  = regexp.MustCompile(`^net[0-9]+$`)
	ipcfgRE   = regexp.MustCompile(`^ipconfig[0-9]+$`)
)

// ParseGuestConfig haalt schijven, netwerkkaarten, onboot en vaste adressen
// uit de configuratie die GET .../config geeft. Een lege cd-speler en een
// niet gekoppelde schijf (unusedN) tellen niet.
func ParseGuestConfig(cfg map[string]any) GuestConfig {
	var out GuestConfig
	keys := make([]string, 0, len(cfg))
	for k := range cfg {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, compareKeys)
	for _, k := range keys {
		v, isStr := cfg[k].(string)
		switch {
		case k == "onboot":
			out.OnBoot = cfgBool(cfg[k])
		case !isStr:
		case diskKeyRE.MatchString(k):
			if d, ok := parseDisk(k, v); ok {
				out.Disks = append(out.Disks, d)
			}
		case netKeyRE.MatchString(k):
			n, addrs := parseNIC(k, v)
			if n.Bridge != "" || n.MAC != "" {
				out.NICs = append(out.NICs, n)
			}
			out.Addresses = append(out.Addresses, addrs...)
		case ipcfgRE.MatchString(k):
			out.Addresses = append(out.Addresses, staticIPs(v)...)
		}
	}
	out.Addresses = uniq(out.Addresses)
	return out
}

// compareKeys sorteert net2 voor net10.
func compareKeys(a, b string) int {
	pa, na := splitIndex(a)
	pb, nb := splitIndex(b)
	if pa != pb {
		return strings.Compare(pa, pb)
	}
	return na - nb
}

func splitIndex(k string) (string, int) {
	i := len(k)
	for i > 0 && k[i-1] >= '0' && k[i-1] <= '9' {
		i--
	}
	n, _ := strconv.Atoi(k[i:])
	return k[:i], n
}

func cfgBool(v any) bool {
	switch x := v.(type) {
	case float64:
		return x != 0
	case string:
		return x == "1" || x == "true"
	case bool:
		return x
	}
	return false
}

// Option leest een optie uit een Proxmox-eigenschap, zoals bridge uit
// "virtio=BC:24:11:00:00:01,bridge=vmbr0".
func Option(v, name string) (string, bool) { return OptionOr(v, name, "") }

// OptionOr leest een optie zoals Option, maar de eerste waarde zonder naam
// heet def: size uit "local-lvm:vm-1-disk-0,size=32G", of current uit
// "current,serial=x" met def "current". De sandbox van de back-upcontrole en
// de kaart lezen dezelfde eigenschappen hiermee.
func OptionOr(v, name, def string) (string, bool) {
	for i, part := range strings.Split(v, ",") {
		k, val, found := strings.Cut(part, "=")
		if !found {
			if i == 0 && def != "" && name == def {
				return part, true
			}
			continue
		}
		if k == name {
			return val, true
		}
	}
	return "", false
}

// SetOption zet of vervangt één optie in een Proxmox-eigenschap.
func SetOption(v, name, value string) string {
	parts := strings.Split(v, ",")
	for i, part := range parts {
		if k, _, found := strings.Cut(part, "="); found && k == name {
			parts[i] = name + "=" + value
			return strings.Join(parts, ",")
		}
	}
	return v + "," + name + "=" + value
}

func parseDisk(key, v string) (Disk, bool) {
	vol, _, _ := strings.Cut(v, ",")
	vol = strings.TrimPrefix(vol, "file=")
	if vol == "" || vol == "none" || vol == "cdrom" {
		return Disk{}, false
	}
	d := Disk{Key: key}
	if size, ok := Option(v, "size"); ok {
		if b, err := templates.ParseSize(size); err == nil {
			d.SizeBytes = b
		}
	}
	if strings.HasPrefix(vol, "/") {
		d.Path = vol
		return d, true
	}
	st, _, found := strings.Cut(vol, ":")
	if !found || st == "" {
		return Disk{}, false
	}
	d.Storage = st
	return d, true
}

// models zijn de netwerkkaarten van een VM; het eerste deel van net0 is
// model=MAC.
var models = []string{"virtio", "e1000", "e1000e", "rtl8139", "vmxnet3", "ne2k_pci", "pcnet", "i82551", "i82557b", "i82559er", "ne2k_isa"}

func parseNIC(key, v string) (NIC, []string) {
	n := NIC{Key: key}
	var addrs []string
	for _, part := range strings.Split(v, ",") {
		k, val, found := strings.Cut(part, "=")
		if !found {
			continue
		}
		switch {
		case k == "bridge":
			n.Bridge = val
		case k == "tag":
			n.Tag, _ = strconv.Atoi(val)
		case k == "hwaddr" || k == "macaddr" || slices.Contains(models, k):
			if mac, err := net.ParseMAC(val); err == nil {
				n.MAC = mac.String()
			}
		case k == "link_down":
			n.LinkDown = val == "1"
		case k == "ip" || k == "ip6":
			addrs = append(addrs, staticIPs(k+"="+val)...)
		}
	}
	return n, addrs
}

// staticIPs leest de vaste adressen uit "ip=192.168.1.5/24,gw=..." zonder
// prefix; dhcp, auto en manual geven niets.
func staticIPs(v string) []string {
	var out []string
	for _, name := range []string{"ip", "ip6"} {
		val, ok := Option(v, name)
		if !ok {
			continue
		}
		addr, _, _ := strings.Cut(val, "/")
		if ip := net.ParseIP(addr); ip != nil {
			out = append(out, ip.String())
		}
	}
	return out
}

func uniq(s []string) []string {
	slices.Sort(s)
	return slices.Compact(s)
}

// interfaceIPs geeft de adressen van een draaiende container, zonder
// loopback.
func interfaceIPs(ifs []GuestInterface) []string {
	var out []string
	for _, i := range ifs {
		if i.Name == "lo" {
			continue
		}
		for _, a := range []string{i.Inet, i.Inet6} {
			addr, _, _ := strings.Cut(a, "/")
			if ip := net.ParseIP(addr); ip != nil && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() {
				out = append(out, ip.String())
			}
		}
	}
	return out
}

// ReadConfigsAll leest de configuraties die aan de beurt zijn, voor alle
// koppelingen.
func (s *Service) ReadConfigsAll(ctx context.Context) {
	conns, err := s.q.ListProxmoxConnections(ctx)
	if err != nil {
		return
	}
	for _, c := range conns {
		if c.LastError != "" {
			continue
		}
		if err := s.ReadConfigs(ctx, c.ID); err != nil && ctx.Err() == nil {
			s.log.Warn("Proxmox-configuraties lezen mislukt", "proxmox", c.Name, "err", err)
		}
	}
}

// ReadConfigs leest de configuratie van de VM's en containers van een
// koppeling die nog niet of langer dan ConfigInterval geleden gelezen zijn,
// hoogstens configBatch per keer. Templates en de tijdelijke VM's van een
// back-upcontrole slaat ze over. Een fout bij één VM komt in zijn rij; de
// vorige gegevens blijven dan staan.
func (s *Service) ReadConfigs(ctx context.Context, id uuid.UUID) error {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	conn, err := s.q.GetProxmoxConnection(ctx, id)
	if err != nil {
		return translate(err)
	}
	rows, err := s.q.ListProxmoxResources(ctx, id)
	if err != nil {
		return err
	}
	read, err := s.q.ListGuestConfigReadAt(ctx, id)
	if err != nil {
		return err
	}
	readAt := map[int32]time.Time{}
	for _, r := range read {
		readAt[r.Vmid] = r.ReadAt
	}
	now := time.Now()
	type due struct {
		g       Guest
		running bool
		last    time.Time
	}
	var todo []due
	for _, row := range rows {
		r := row.ProxmoxResource
		if (r.Type != "qemu" && r.Type != "lxc") || r.Template || r.Vmid == nil {
			continue
		}
		var d Resource
		_ = json.Unmarshal(r.Data, &d)
		if d.Pool == SandboxPool {
			continue
		}
		last, seen := readAt[*r.Vmid]
		if seen && now.Sub(last) < ConfigInterval {
			continue
		}
		todo = append(todo, due{Guest{Type: r.Type, Node: r.PveNode, VMID: int(*r.Vmid)}, r.Status == "running", last})
	}
	// Nooit gelezen eerst, dan de oudste.
	slices.SortFunc(todo, func(a, b due) int { return a.last.Compare(b.last) })
	if len(todo) > configBatch {
		todo = todo[:configBatch]
	}
	if len(todo) > 0 {
		api, err := s.api(conn)
		if err != nil {
			return err
		}
		var g errgroup.Group
		g.SetLimit(4)
		for _, t := range todo {
			g.Go(func() error {
				s.readConfig(ctx, api, id, t.g, t.running)
				return nil
			})
		}
		_ = g.Wait()
	}
	return s.q.DeleteStaleGuestConfig(ctx, id)
}

func (s *Service) readConfig(ctx context.Context, api API, connID uuid.UUID, g Guest, running bool) {
	now := time.Now()
	raw, err := api.Config(ctx, g)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		msg := limit(err.Error(), 500)
		if err := s.q.SetGuestConfigError(ctx, store.SetGuestConfigErrorParams{ConnectionID: connID, Vmid: int32(g.VMID), Error: msg, ReadAt: now}); err != nil {
			s.log.Warn("fout bij configuratie bewaren", "vmid", g.VMID, "err", err)
		}
		return
	}
	cfg := ParseGuestConfig(raw)
	if g.Type == "lxc" && running {
		// Zonder het recht om dit te lezen blijven de vaste adressen.
		if ifs, err := api.Interfaces(ctx, g); err == nil {
			cfg.Addresses = uniq(append(cfg.Addresses, interfaceIPs(ifs)...))
		}
	}
	disks, _ := json.Marshal(nonNil(cfg.Disks))
	nets, _ := json.Marshal(nonNil(cfg.NICs))
	err = s.q.UpsertGuestConfig(ctx, store.UpsertGuestConfigParams{
		ConnectionID: connID, Vmid: int32(g.VMID), Disks: disks, Nets: nets, Onboot: cfg.OnBoot,
		Addresses: nonNil(cfg.Addresses), ReadAt: now,
	})
	if err != nil && ctx.Err() == nil {
		s.log.Warn("configuratie bewaren mislukt", "vmid", g.VMID, "err", err)
	}
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// Interfaces geeft de interfaces van een draaiende container. Daarvoor
// volstaat VM.Audit.
func (c *Client) Interfaces(ctx context.Context, g Guest) ([]GuestInterface, error) {
	var out []GuestInterface
	err := c.do(ctx, http.MethodGet, g.path()+"/interfaces", nil, &out)
	return out, err
}
