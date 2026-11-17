package opnsense

// Interfaces en systeem (fase 40 M4): de delen van config.xml zonder API.
// ClusterForge leest ze uit de huidige config.xml van elke firewall (zonder
// geheimen) en verandert ze met Change.Legacy, per firewall, met herstelpunt
// en terugdraaitimer.

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// SysIface is een toegewezen interface.
type SysIface struct {
	Key         string `json:"key"`
	Device      string `json:"device"`
	Descr       string `json:"descr,omitempty"`
	Enabled     bool   `json:"enabled"`
	IPv4        string `json:"ipaddr,omitempty"`
	Subnet      string `json:"subnet,omitempty"`
	IPv6        string `json:"ipaddrv6,omitempty"`
	SubnetV6    string `json:"subnetv6,omitempty"`
	Gateway     string `json:"gateway,omitempty"`
	BlockPriv   bool   `json:"blockpriv"`
	BlockBogons bool   `json:"blockbogons"`
	MTU         string `json:"mtu,omitempty"`
}

// SysNode is wat één firewall heeft.
type SysNode struct {
	NodeID      uuid.UUID  `json:"node_id"`
	Hostname    string     `json:"hostname"`
	Role        string     `json:"role"`
	Host        string     `json:"host"`
	Domain      string     `json:"domain"`
	DNS         []string   `json:"dns"`
	NTP         string     `json:"ntp,omitempty"`
	Timezone    string     `json:"timezone,omitempty"`
	DNSOverride bool       `json:"dns_override"`
	Interfaces  []SysIface `json:"interfaces"`
	// Free zijn apparaten zonder toewijzing (vtnet3, vlan0.40 ...).
	Free []string `json:"free"`
	// NextKey is de sleutel voor een nieuwe toewijzing (opt4).
	NextKey string `json:"next_key"`
	// Legacy: de agent kan velden zonder API veranderen.
	Legacy bool   `json:"legacy"`
	Error  string `json:"error,omitempty"`
}

// SystemView is de tab Interfaces en systeem.
type SystemView struct {
	Cluster string    `json:"cluster"`
	Nodes   []SysNode `json:"nodes"`
}

type cfgIface struct {
	XMLName     xml.Name
	If          string  `xml:"if"`
	Descr       string  `xml:"descr"`
	Enable      *string `xml:"enable"`
	IPAddr      string  `xml:"ipaddr"`
	Subnet      string  `xml:"subnet"`
	IPAddrV6    string  `xml:"ipaddrv6"`
	SubnetV6    string  `xml:"subnetv6"`
	Gateway     string  `xml:"gateway"`
	BlockPriv   *string `xml:"blockpriv"`
	BlockBogons *string `xml:"blockbogons"`
	MTU         string  `xml:"mtu"`
}

type cfgXML struct {
	System struct {
		Hostname string   `xml:"hostname"`
		Domain   string   `xml:"domain"`
		DNS      []string `xml:"dnsserver"`
		NTP      string   `xml:"timeservers"`
		Timezone string   `xml:"timezone"`
		Override *string  `xml:"dnsallowoverride"`
	} `xml:"system"`
	Interfaces struct {
		Items []cfgIface `xml:",any"`
	} `xml:"interfaces"`
}

// parseSystem leest systeem en interfaces uit config.xml.
func parseSystem(text string, n *SysNode) error {
	var c cfgXML
	if err := xml.Unmarshal([]byte(text), &c); err != nil {
		return err
	}
	n.Host, n.Domain, n.NTP, n.Timezone = c.System.Hostname, c.System.Domain, c.System.NTP, c.System.Timezone
	n.DNS = []string{}
	for _, d := range c.System.DNS {
		if d = strings.TrimSpace(d); d != "" {
			n.DNS = append(n.DNS, d)
		}
	}
	n.DNSOverride = c.System.Override != nil
	n.Interfaces = []SysIface{}
	maxOpt := 0
	for _, i := range c.Interfaces.Items {
		k := i.XMLName.Local
		if !protocol.ValidOPNIfaceKey(k) {
			continue
		}
		if strings.HasPrefix(k, "opt") {
			if x, err := strconv.Atoi(k[3:]); err == nil {
				maxOpt = max(maxOpt, x)
			}
		}
		n.Interfaces = append(n.Interfaces, SysIface{
			Key: k, Device: i.If, Descr: i.Descr, Enabled: i.Enable != nil, IPv4: i.IPAddr, Subnet: i.Subnet,
			IPv6: i.IPAddrV6, SubnetV6: i.SubnetV6, Gateway: i.Gateway, BlockPriv: i.BlockPriv != nil, BlockBogons: i.BlockBogons != nil, MTU: i.MTU,
		})
	}
	sort.Slice(n.Interfaces, func(a, b int) bool { return ifaceOrder(n.Interfaces[a].Key) < ifaceOrder(n.Interfaces[b].Key) })
	n.NextKey = "opt" + strconv.Itoa(maxOpt+1)
	return nil
}

func ifaceOrder(k string) string {
	switch k {
	case "wan":
		return "0"
	case "lan":
		return "1"
	}
	return "2" + strings.Repeat("0", 4-min(len(k), 4)) + k
}

// physDev zijn apparaten die je kunt toewijzen.
var physDev = regexp.MustCompile(`^(vtnet|em|igb|igc|ix|ixl|ice|re|vmx|bge|bnxt|mlxen|cxl|axe|ue|lagg|vlan|bridge|qinq|wg|ovpns|ovpnc|gif|gre)[0-9][0-9.]*$`)

// freeDevices zijn de apparaten uit "interface list ifconfig" zonder
// toewijzing.
func freeDevices(ifconfig json.RawMessage, used []SysIface) []string {
	var m map[string]json.RawMessage
	if json.Unmarshal(ifconfig, &m) != nil {
		return []string{}
	}
	taken := map[string]bool{}
	for _, i := range used {
		taken[i.Device] = true
	}
	out := []string{}
	for d := range m {
		if physDev.MatchString(d) && !taken[d] {
			out = append(out, d)
		}
	}
	sort.Strings(out)
	return out
}

// System geeft interfaces en systeem van elke firewall van het cluster.
func (s *Service) System(ctx context.Context, id uuid.UUID) (SystemView, error) {
	c, err := s.ClusterOf(ctx, id)
	if err != nil {
		return SystemView{}, err
	}
	v := SystemView{Cluster: c.Name, Nodes: []SysNode{}}
	for _, f := range c.Nodes {
		n := SysNode{NodeID: f.NodeID, Hostname: f.Hostname, Role: f.Role, DNS: []string{}, Interfaces: []SysIface{}, Free: []string{},
			Legacy: f.Protocol >= protocol.OPNLegacySince}
		if f.Why != "" {
			n.Error = f.Why
			v.Nodes = append(v.Nodes, n)
			continue
		}
		text, err := s.revision(ctx, f.NodeID, "current")
		if err == nil {
			err = parseSystem(text, &n)
		}
		if err != nil {
			n.Error = "config.xml lezen mislukte: " + err.Error()
			v.Nodes = append(v.Nodes, n)
			continue
		}
		if raw, err := s.request(ctx, f.NodeID, protocol.PVERequest{Op: protocol.OPNOpConfigctl, Path: "interface list ifconfig"}, true); err == nil {
			n.Free = freeDevices(raw, n.Interfaces)
		}
		v.Nodes = append(v.Nodes, n)
	}
	return v, nil
}
