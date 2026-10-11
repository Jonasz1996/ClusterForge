package proxmox

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Jonasz1996/clusterforge/internal/store"
)

// NetworkInterval is hoe vaak het netwerk van een host opnieuw gelezen
// wordt.
const NetworkInterval = 10 * time.Minute

// HostIface is een interface van een Proxmox-host: een bridge, bond,
// VLAN-interface of fysieke poort.
type HostIface struct {
	Iface string
	// Type is bridge, bond, eth, vlan, alias, OVSBridge, OVSBond, OVSPort,
	// OVSIntPort of unknown.
	Type   string
	Active bool
	// Ports zijn de poorten van een bridge of de slaves van een bond.
	Ports []string
	// VLANID en VLANRaw zijn het VLAN en de onderliggende interface van een
	// VLAN-interface, zoals 20 en vmbr0 bij vmbr0.20.
	VLANID    int
	VLANRaw   string
	CIDR      string
	CIDR6     string
	Gateway   string
	Gateway6  string
	VLANAware bool
	MTU       int
	Comments  string
}

// Bridge is true voor een Linux- of OVS-bridge, waar gasten aan hangen.
func (i HostIface) Bridge() bool { return i.Type == "bridge" || i.Type == "OVSBridge" }

// ParseHostNetwork leest wat GET /nodes/{node}/network geeft. Proxmox geeft
// getallen soms als tekst en vlaggen als 0 of 1.
func ParseHostNetwork(raw []map[string]any) []HostIface {
	out := make([]HostIface, 0, len(raw))
	for _, r := range raw {
		i := HostIface{
			Iface: str(r["iface"]), Type: str(r["type"]), Active: on(r["active"]),
			CIDR: str(r["cidr"]), CIDR6: str(r["cidr6"]), Gateway: str(r["gateway"]), Gateway6: str(r["gateway6"]),
			VLANAware: on(r["bridge_vlan_aware"]), MTU: num(r["mtu"]), Comments: strings.TrimSpace(str(r["comments"])),
			VLANID: num(r["vlan-id"]), VLANRaw: str(r["vlan-raw-device"]),
		}
		if i.Iface == "" {
			continue
		}
		if i.Type == "" {
			i.Type = "unknown"
		}
		for _, k := range []string{"bridge_ports", "slaves", "ovs_ports"} {
			i.Ports = append(i.Ports, strings.Fields(str(r[k]))...)
		}
		if i.CIDR == "" && str(r["address"]) != "" {
			i.CIDR = str(r["address"])
			if mask := str(r["netmask"]); mask != "" {
				i.CIDR += "/" + mask
			}
		}
		// vmbr0.20 en vlan20 zonder vlan-id: Proxmox leidt het af uit de naam.
		if i.Type == "vlan" && i.VLANID == 0 {
			if raw, id, ok := strings.Cut(i.Iface, "."); ok {
				i.VLANID, _ = strconv.Atoi(id)
				if i.VLANRaw == "" {
					i.VLANRaw = raw
				}
			} else if id, ok := strings.CutPrefix(i.Iface, "vlan"); ok {
				i.VLANID, _ = strconv.Atoi(id)
			}
		}
		out = append(out, i)
	}
	slices.SortFunc(out, func(a, b HostIface) int { return strings.Compare(a.Iface, b.Iface) })
	return out
}

func str(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case json.Number:
		return x.String()
	}
	return ""
}

func num(v any) int {
	n, _ := strconv.Atoi(str(v))
	return n
}

func on(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case float64:
		return x == 1
	case string:
		return x == "1" || x == "true"
	}
	return false
}

// HostNetwork geeft het netwerk van een host. Daarvoor volstaat Sys.Audit.
func (c *Client) HostNetwork(ctx context.Context, node string) ([]map[string]any, error) {
	var out []map[string]any
	err := c.do(ctx, http.MethodGet, "/nodes/"+node+"/network", nil, &out)
	return out, err
}

// ReadNetworksAll leest het netwerk van de hosts die aan de beurt zijn,
// voor alle koppelingen.
func (s *Service) ReadNetworksAll(ctx context.Context) {
	conns, err := s.q.ListProxmoxConnections(ctx)
	if err != nil {
		return
	}
	for _, c := range conns {
		if c.LastError != "" {
			continue
		}
		if err := s.ReadNetworks(ctx, c.ID); err != nil && ctx.Err() == nil {
			s.log.Warn("netwerk van Proxmox-hosts lezen mislukt", "proxmox", c.Name, "err", err)
		}
	}
}

// ReadNetworks leest het netwerk van elke online host van een koppeling die
// nog niet of langer dan NetworkInterval geleden gelezen is. Een fout komt
// bij de host; de vorige gegevens blijven dan staan.
func (s *Service) ReadNetworks(ctx context.Context, id uuid.UUID) error {
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
	reads, err := s.q.ListHostNetworkReads(ctx, id)
	if err != nil {
		return err
	}
	readAt := map[string]time.Time{}
	for _, r := range reads {
		readAt[r.PveNode] = r.ReadAt
	}
	now := time.Now()
	var hosts []string
	for _, row := range rows {
		r := row.ProxmoxResource
		if r.Type != "node" || r.Status != "online" {
			continue
		}
		if last, seen := readAt[r.PveNode]; seen && now.Sub(last) < NetworkInterval {
			continue
		}
		hosts = append(hosts, r.PveNode)
	}
	if len(hosts) > 0 {
		api, err := s.api(conn)
		if err != nil {
			return err
		}
		for _, h := range hosts {
			s.readNetwork(ctx, api, id, h)
		}
	}
	return s.q.DeleteStaleHostNetworks(ctx, id)
}

func (s *Service) readNetwork(ctx context.Context, api API, connID uuid.UUID, host string) {
	now := time.Now()
	raw, err := api.HostNetwork(ctx, host)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		err := s.q.SetHostNetworkRead(ctx, store.SetHostNetworkReadParams{ConnectionID: connID, PveNode: host, ReadAt: now, Error: limit(err.Error(), 500)})
		if err != nil {
			s.log.Warn("fout bij netwerk van host bewaren", "host", host, "err", err)
		}
		return
	}
	ifaces := ParseHostNetwork(raw)
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		if err := q.DeleteHostNetwork(ctx, store.DeleteHostNetworkParams{ConnectionID: connID, PveNode: host}); err != nil {
			return err
		}
		for _, i := range ifaces {
			err := q.InsertHostNetworkIface(ctx, store.InsertHostNetworkIfaceParams{
				ConnectionID: connID, PveNode: host, Iface: i.Iface, Type: i.Type, Active: i.Active, Ports: nonNil(i.Ports),
				VlanID: optInt(i.VLANID), VlanRawDevice: i.VLANRaw, Cidr: i.CIDR, Cidr6: i.CIDR6, Gateway: i.Gateway,
				Gateway6: i.Gateway6, VlanAware: i.VLANAware, Mtu: optInt(i.MTU), Comments: limit(i.Comments, 500),
			})
			if err != nil {
				return err
			}
		}
		return q.SetHostNetworkRead(ctx, store.SetHostNetworkReadParams{ConnectionID: connID, PveNode: host, ReadAt: now, Error: ""})
	})
	if err != nil && ctx.Err() == nil {
		s.log.Warn("netwerk van host bewaren mislukt", "host", host, "err", err)
	}
}

func optInt(n int) *int32 {
	if n == 0 {
		return nil
	}
	v := int32(n)
	return &v
}

// HostIfaceFrom zet een rij om.
func HostIfaceFrom(r store.ProxmoxHostNetwork) HostIface {
	i := HostIface{
		Iface: r.Iface, Type: r.Type, Active: r.Active, Ports: r.Ports, VLANRaw: r.VlanRawDevice, CIDR: r.Cidr,
		CIDR6: r.Cidr6, Gateway: r.Gateway, Gateway6: r.Gateway6, VLANAware: r.VlanAware, Comments: r.Comments,
	}
	if r.VlanID != nil {
		i.VLANID = int(*r.VlanID)
	}
	if r.Mtu != nil {
		i.MTU = int(*r.Mtu)
	}
	return i
}
