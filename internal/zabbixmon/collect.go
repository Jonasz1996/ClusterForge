package zabbixmon

import (
	"cmp"
	"context"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Ernst van een trigger in Zabbix.
const (
	SevNotClassified = 0
	SevInfo          = 1
	SevWarning       = 2
	SevAverage       = 3
	SevHigh          = 4
	SevDisaster      = 5
)

// Beschikbaarheid van een host of interface.
const (
	AvailOK       = "ok"
	AvailDown     = "down"
	AvailUnknown  = "unknown"
	AvailNone     = "none"
	AvailDisabled = "disabled"
)

// Tag is een tag van een probleem.
type Tag struct {
	Tag   string `json:"tag"`
	Value string `json:"value"`
}

// NodeLink is de ClusterForge-node bij een Zabbix-host.
type NodeLink struct {
	NodeID      uuid.UUID  `json:"node_id"`
	Hostname    string     `json:"hostname"`
	ClusterID   *uuid.UUID `json:"cluster_id"`
	ClusterName string     `json:"cluster_name"`
	// Source: hoe de koppeling gevonden is: manual, agent, name of ip.
	Source string `json:"source"`
}

// HostRef is een host bij een probleem.
type HostRef struct {
	HostID string    `json:"hostid"`
	Name   string    `json:"name"`
	Node   *NodeLink `json:"node"`
}

// Problem is een open probleem in Zabbix.
type Problem struct {
	EventID      string    `json:"eventid"`
	TriggerID    string    `json:"triggerid"`
	Name         string    `json:"name"`
	Severity     int       `json:"severity"`
	Since        time.Time `json:"since"`
	Acknowledged bool      `json:"acknowledged"`
	Suppressed   bool      `json:"suppressed"`
	Opdata       string    `json:"opdata"`
	Tags         []Tag     `json:"tags"`
	Hosts        []HostRef `json:"hosts"`
}

// Interface is een interface van een host.
type Interface struct {
	Type      string `json:"type"`
	Address   string `json:"address"`
	Available string `json:"available"`
	Error     string `json:"error"`
}

// Host is een host in Zabbix.
type Host struct {
	HostID      string `json:"hostid"`
	Host        string `json:"host"`
	Name        string `json:"name"`
	Enabled     bool   `json:"enabled"`
	Maintenance bool   `json:"maintenance"`
	// Available is de samengevatte beschikbaarheid (ok, down, unknown, none
	// of disabled); Active die van de actieve checks.
	Available  string      `json:"available"`
	Active     string      `json:"active"`
	Interfaces []Interface `json:"interfaces"`
	Proxy      string      `json:"proxy"`
	Groups     []string    `json:"groups"`
	Node       *NodeLink   `json:"node"`
	// Problems: open (niet onderdrukte) problemen; Worst de hoogste ernst,
	// -1 zonder problemen.
	Problems int `json:"problems"`
	Worst    int `json:"worst"`
}

// Proxy is een Zabbix-proxy.
type Proxy struct {
	ProxyID string     `json:"proxyid"`
	Name    string     `json:"name"`
	Mode    string     `json:"mode"`
	State   string     `json:"state"`
	Seen    *time.Time `json:"last_seen"`
	Version string     `json:"version"`
	// Compatibility: current, outdated, unsupported of leeg.
	Compatibility string `json:"compatibility"`
	Hosts         int    `json:"hosts"`
}

// HANode is een node van het HA-cluster van de Zabbix-server.
type HANode struct {
	Name    string     `json:"name"`
	Address string     `json:"address"`
	Status  string     `json:"status"`
	Seen    *time.Time `json:"last_seen"`
}

// Process is een drukke interne Zabbix-procesgroep.
type Process struct {
	Name string  `json:"name"`
	Busy float64 `json:"busy"`
}

// Health zijn de gezondheidsitems van één Zabbix-server of -proxy.
type Health struct {
	HostID             string     `json:"hostid"`
	Name               string     `json:"name"`
	NVPS               *float64   `json:"nvps"`
	Queue              *float64   `json:"queue"`
	Queue10m           *float64   `json:"queue_10m"`
	PreprocessingQueue *float64   `json:"preprocessing_queue"`
	CacheConfig        *float64   `json:"cache_config"`
	CacheHistory       *float64   `json:"cache_history"`
	CacheValue         *float64   `json:"cache_value"`
	Values             *float64   `json:"values"`
	Uptime             *float64   `json:"uptime"`
	Busiest            []Process  `json:"busiest"`
	Updated            *time.Time `json:"updated"`
}

// NodeRef is een ClusterForge-node zonder Zabbix-host.
type NodeRef struct {
	NodeID      uuid.UUID  `json:"node_id"`
	Hostname    string     `json:"hostname"`
	IP          string     `json:"ip"`
	ClusterID   *uuid.UUID `json:"cluster_id"`
	ClusterName string     `json:"cluster_name"`
}

// Counts zijn de tellers bovenaan.
type Counts struct {
	// Per ernst, zonder onderdrukte problemen.
	Disaster      int `json:"disaster"`
	High          int `json:"high"`
	Average       int `json:"average"`
	Warning       int `json:"warning"`
	Info          int `json:"info"`
	NotClassified int `json:"not_classified"`
	Unacked       int `json:"unacked"`
	Suppressed    int `json:"suppressed"`
	// Hosts: ingeschakelde hosts.
	Hosts            int `json:"hosts"`
	HostsOK          int `json:"hosts_ok"`
	HostsDown        int `json:"hosts_down"`
	HostsUnknown     int `json:"hosts_unknown"`
	HostsDisabled    int `json:"hosts_disabled"`
	HostsMaintenance int `json:"hosts_maintenance"`
	// Nodes: actieve ClusterForge-nodes; Watched: met een Zabbix-host.
	Nodes   int `json:"nodes"`
	Watched int `json:"watched"`
}

// Snapshot is één verzamelronde.
type Snapshot struct {
	At        time.Time `json:"at"`
	Version   string    `json:"version"`
	Problems  []Problem `json:"problems"`
	Hosts     []Host    `json:"hosts"`
	Proxies   []Proxy   `json:"proxies"`
	HA        []HANode  `json:"ha"`
	Health    []Health  `json:"health"`
	Unwatched []NodeRef `json:"unwatched"`
	Counts    Counts    `json:"counts"`
	// Fouten per onderdeel (bv. geen rechten voor hanode.get); leeg = gelukt.
	ProxiesError string `json:"proxies_error"`
	HAError      string `json:"ha_error"`
	HealthError  string `json:"health_error"`
}

// CFNode is een ClusterForge-node voor de koppeling.
type CFNode struct {
	ID          uuid.UUID
	Hostname    string
	IP          string
	ClusterID   *uuid.UUID
	ClusterName string
	Lifecycle   string
	// ZabbixHostID: de host die de Zabbix-agentmodule aanmaakte of vond.
	ZabbixHostID string
}

// collector doet één ronde.
type collector struct {
	api API
}

// Collect haalt alles op en koppelt het aan de nodes. links: handmatige
// koppelingen (hostid → node; nil = bewust geen node).
func Collect(ctx context.Context, api API, nodes []CFNode, links map[string]*uuid.UUID, now time.Time) (Snapshot, error) {
	c := &collector{api: api}
	snap := Snapshot{At: now.UTC()}
	v, err := c.apiVersion(ctx)
	if err != nil {
		return snap, err
	}
	snap.Version = v

	var (
		wg                           sync.WaitGroup
		hosts                        []rawHost
		probs                        []rawProblem
		proxies                      []rawProxy
		ha                           []rawHANode
		items                        []rawItem
		hErr, pErr, xErr, aErr, iErr error
	)
	wg.Go(func() { hosts, hErr = c.hosts(ctx) })
	wg.Go(func() { probs, pErr = c.problems(ctx) })
	wg.Go(func() { proxies, xErr = c.proxies(ctx) })
	wg.Go(func() { ha, aErr = c.haNodes(ctx) })
	wg.Go(func() { items, iErr = c.healthItems(ctx) })
	wg.Wait()
	if hErr != nil {
		return snap, hErr
	}
	if pErr != nil {
		return snap, pErr
	}
	var tids []string
	for _, p := range probs {
		tids = append(tids, p.ObjectID)
	}
	slices.Sort(tids)
	tids = slices.Compact(tids)
	th, err := c.triggerHosts(ctx, tids)
	if err != nil {
		return snap, err
	}
	if xErr != nil {
		snap.ProxiesError = friendly(xErr)
	}
	if aErr != nil {
		snap.HAError = friendly(aErr)
	}
	if iErr != nil {
		snap.HealthError = friendly(iErr)
	}
	build(&snap, nodes, links, hosts, probs, th, proxies, ha, items)
	return snap, nil
}

// friendly maakt een API-fout leesbaar; gebrek aan rechten zeggen we zo.
func friendly(err error) string {
	m := err.Error()
	if strings.Contains(strings.ToLower(m), "permission") || strings.Contains(strings.ToLower(m), "no permissions") {
		return "het API-token heeft hier geen rechten voor (" + m + ")"
	}
	return m
}

var ifaceType = map[zint]string{1: "agent", 2: "snmp", 3: "ipmi", 4: "jmx"}

func availName(v zint) string {
	switch v {
	case 1:
		return AvailOK
	case 2:
		return AvailDown
	}
	return AvailUnknown
}

// hostAvailable vat de interfaces en de actieve checks samen: één
// onbereikbare interface maakt de host onbereikbaar.
func hostAvailable(h rawHost) (string, string) {
	if h.Status != 0 {
		return AvailDisabled, AvailDisabled
	}
	active := availName(h.ActiveAvailable)
	all := make([]string, 0, len(h.Interfaces)+1)
	for _, i := range h.Interfaces {
		all = append(all, availName(i.Available))
	}
	// Actieve checks tellen alleen als ze iets weten: zonder actieve items
	// blijft active_available onbekend.
	if h.ActiveAvailable != 0 {
		all = append(all, active)
	}
	if len(all) == 0 {
		return AvailNone, active
	}
	switch {
	case slices.Contains(all, AvailDown):
		return AvailDown, active
	case slices.Contains(all, AvailOK):
		return AvailOK, active
	}
	return AvailUnknown, active
}

func short(h string) string {
	h, _, _ = strings.Cut(strings.ToLower(strings.TrimSpace(h)), ".")
	return h
}

// match zoekt de node bij elke host: handmatig, dan de Zabbix-agentmodule,
// dan de naam, dan het IP-adres. Een node hoort bij hoogstens één host.
func match(nodes []CFNode, links map[string]*uuid.UUID, hosts []rawHost) map[string]*NodeLink {
	byID := map[uuid.UUID]CFNode{}
	for _, n := range nodes {
		byID[n.ID] = n
	}
	out := map[string]*NodeLink{}
	taken := map[uuid.UUID]bool{}
	fixed := map[string]bool{}
	link := func(hostID string, n CFNode, src string) {
		out[hostID] = &NodeLink{NodeID: n.ID, Hostname: n.Hostname, ClusterID: n.ClusterID, ClusterName: n.ClusterName, Source: src}
		taken[n.ID] = true
		fixed[hostID] = true
	}
	known := map[string]bool{}
	for _, h := range hosts {
		known[h.HostID] = true
	}
	// 1. Handmatig (ook "bewust geen node").
	for hid, nid := range links {
		if !known[hid] {
			continue
		}
		fixed[hid] = true
		if nid == nil {
			continue
		}
		if n, ok := byID[*nid]; ok && !taken[n.ID] {
			link(hid, n, "manual")
		}
	}
	// 2. De host die de Zabbix-agentmodule kent.
	for _, n := range nodes {
		if taken[n.ID] || n.ZabbixHostID == "" || !known[n.ZabbixHostID] || fixed[n.ZabbixHostID] {
			continue
		}
		link(n.ZabbixHostID, n, "agent")
	}
	// 3. Naam (technische of zichtbare naam, zonder domein).
	byName := map[string]CFNode{}
	for _, n := range nodes {
		if !taken[n.ID] {
			byName[short(n.Hostname)] = n
		}
	}
	for _, h := range hosts {
		if fixed[h.HostID] {
			continue
		}
		for _, nm := range []string{h.Host, h.Name} {
			if n, ok := byName[short(nm)]; ok && !taken[n.ID] {
				link(h.HostID, n, "name")
				break
			}
		}
	}
	// 4. IP-adres van een interface.
	byIP := map[netip.Addr]CFNode{}
	for _, n := range nodes {
		if a, err := netip.ParseAddr(n.IP); err == nil && !taken[n.ID] {
			byIP[a] = n
		}
	}
	for _, h := range hosts {
		if fixed[h.HostID] {
			continue
		}
		for _, i := range h.Interfaces {
			a, err := netip.ParseAddr(i.IP)
			if err != nil {
				continue
			}
			if n, ok := byIP[a]; ok && !taken[n.ID] {
				link(h.HostID, n, "ip")
				break
			}
		}
	}
	return out
}

func build(snap *Snapshot, nodes []CFNode, links map[string]*uuid.UUID, hosts []rawHost, probs []rawProblem,
	th map[string][]string, proxies []rawProxy, ha []rawHANode, items []rawItem) {
	nodeOf := match(nodes, links, hosts)
	proxyName := map[string]string{}
	for _, p := range proxies {
		proxyName[p.ProxyID] = p.Name
	}
	hostIdx := map[string]int{}
	snap.Hosts = make([]Host, 0, len(hosts))
	for _, h := range hosts {
		av, act := hostAvailable(h)
		out := Host{
			HostID: h.HostID, Host: h.Host, Name: cmp.Or(h.Name, h.Host), Enabled: h.Status == 0,
			Maintenance: h.MaintenanceStatus == 1, Available: av, Active: act,
			Interfaces: []Interface{}, Groups: []string{}, Node: nodeOf[h.HostID], Worst: -1,
		}
		// monitored_by 1: via een proxy (2 is een proxygroep, die tonen we niet).
		if h.MonitoredBy == 1 && h.ProxyID != "" && h.ProxyID != "0" {
			out.Proxy = proxyName[h.ProxyID]
		}
		for _, i := range h.Interfaces {
			addr := i.IP
			if addr == "" {
				addr = i.DNS
			}
			out.Interfaces = append(out.Interfaces, Interface{Type: cmp.Or(ifaceType[i.Type], "agent"), Address: addr, Available: availName(i.Available), Error: i.Error})
		}
		for _, g := range h.HostGroups {
			out.Groups = append(out.Groups, g.Name)
		}
		hostIdx[h.HostID] = len(snap.Hosts)
		snap.Hosts = append(snap.Hosts, out)
	}

	snap.Problems = make([]Problem, 0, len(probs))
	cnt := &snap.Counts
	for _, p := range probs {
		out := Problem{
			EventID: p.EventID, TriggerID: p.ObjectID, Name: p.Name, Severity: int(p.Severity),
			Acknowledged: p.Acknowledged == 1, Suppressed: p.Suppressed == 1, Opdata: p.Opdata,
			Tags: []Tag{}, Hosts: []HostRef{},
		}
		if t := p.Clock.time(); t != nil {
			out.Since = *t
		}
		for _, t := range p.Tags {
			out.Tags = append(out.Tags, Tag(t))
		}
		for _, hid := range th[p.ObjectID] {
			i, ok := hostIdx[hid]
			if !ok {
				continue
			}
			h := &snap.Hosts[i]
			out.Hosts = append(out.Hosts, HostRef{HostID: hid, Name: h.Name, Node: h.Node})
			if !out.Suppressed {
				h.Problems++
				h.Worst = max(h.Worst, out.Severity)
			}
		}
		if out.Suppressed {
			cnt.Suppressed++
		} else {
			switch out.Severity {
			case SevDisaster:
				cnt.Disaster++
			case SevHigh:
				cnt.High++
			case SevAverage:
				cnt.Average++
			case SevWarning:
				cnt.Warning++
			case SevInfo:
				cnt.Info++
			default:
				cnt.NotClassified++
			}
			if !out.Acknowledged {
				cnt.Unacked++
			}
		}
		snap.Problems = append(snap.Problems, out)
	}
	// Zwaarste eerst, daarna de oudste.
	slices.SortStableFunc(snap.Problems, func(a, b Problem) int {
		if c := cmp.Compare(b.Severity, a.Severity); c != 0 {
			return c
		}
		return a.Since.Compare(b.Since)
	})

	for _, h := range snap.Hosts {
		if !h.Enabled {
			cnt.HostsDisabled++
			continue
		}
		cnt.Hosts++
		if h.Maintenance {
			cnt.HostsMaintenance++
		}
		switch h.Available {
		case AvailOK:
			cnt.HostsOK++
		case AvailDown:
			cnt.HostsDown++
		default:
			cnt.HostsUnknown++
		}
	}

	watched := map[uuid.UUID]bool{}
	for _, l := range nodeOf {
		watched[l.NodeID] = true
	}
	snap.Unwatched = []NodeRef{}
	for _, n := range nodes {
		if n.Lifecycle == "decommissioned" {
			continue
		}
		cnt.Nodes++
		if watched[n.ID] {
			cnt.Watched++
			continue
		}
		snap.Unwatched = append(snap.Unwatched, NodeRef{NodeID: n.ID, Hostname: n.Hostname, IP: n.IP, ClusterID: n.ClusterID, ClusterName: n.ClusterName})
	}

	hostsPerProxy := map[string]int{}
	for _, h := range snap.Hosts {
		if h.Proxy != "" && h.Enabled {
			hostsPerProxy[h.Proxy]++
		}
	}
	snap.Proxies = make([]Proxy, 0, len(proxies))
	for _, p := range proxies {
		snap.Proxies = append(snap.Proxies, Proxy{
			ProxyID: p.ProxyID, Name: p.Name, Mode: map[zint]string{0: "active", 1: "passive"}[p.OperatingMode],
			State: map[zint]string{0: "unknown", 1: "offline", 2: "online"}[p.State], Seen: p.LastAccess.time(),
			Version: version(p.Version), Compatibility: map[zint]string{1: "current", 2: "outdated", 3: "unsupported"}[p.Compatibility],
			Hosts: hostsPerProxy[p.Name],
		})
	}

	snap.HA = make([]HANode, 0, len(ha))
	for _, n := range ha {
		addr := n.Address
		if addr != "" && n.Port != "" && n.Port != "0" {
			addr += ":" + n.Port
		}
		snap.HA = append(snap.HA, HANode{
			Name: n.Name, Address: addr, Seen: n.LastAccess.time(),
			Status: map[zint]string{0: "standby", 1: "stopped", 2: "unavailable", 3: "active"}[n.Status],
		})
	}

	snap.Health = health(items, snap.Hosts)
}

// health groepeert de interne items per Zabbix-server of -proxy.
func health(items []rawItem, hosts []Host) []Health {
	name := map[string]string{}
	for _, h := range hosts {
		name[h.HostID] = h.Name
	}
	by := map[string]*Health{}
	var order []string
	for _, it := range items {
		if it.State != 0 || it.LastClock <= 0 {
			continue
		}
		v, err := strconv.ParseFloat(strings.TrimSpace(it.LastValue), 64)
		if err != nil {
			continue
		}
		h := by[it.HostID]
		if h == nil {
			h = &Health{HostID: it.HostID, Name: cmp.Or(name[it.HostID], it.HostID), Busiest: []Process{}}
			by[it.HostID] = h
			order = append(order, it.HostID)
		}
		if t := it.LastClock.time(); t != nil && (h.Updated == nil || t.After(*h.Updated)) {
			h.Updated = t
		}
		val := v
		switch healthKey(it.Key) {
		case "nvps":
			h.NVPS = &val
		case "queue":
			h.Queue = &val
		case "queue_10m":
			h.Queue10m = &val
		case "preprocessing_queue":
			h.PreprocessingQueue = &val
		case "cache_config":
			h.CacheConfig = &val
		case "cache_history":
			h.CacheHistory = &val
		case "cache_value":
			h.CacheValue = &val
		case "values":
			h.Values = &val
		case "uptime":
			h.Uptime = &val
		case "process":
			k := strings.ReplaceAll(it.Key, " ", "")
			k = strings.TrimSuffix(strings.TrimPrefix(k, "zabbix[process,"), ",avg,busy]")
			// De naam met spaties zoals Zabbix hem toont ("history syncer").
			if i := strings.Index(it.Key, "process,"); i >= 0 {
				rest := it.Key[i+len("process,"):]
				if j := strings.Index(rest, ","); j > 0 {
					k = strings.TrimSpace(rest[:j])
				}
			}
			h.Busiest = append(h.Busiest, Process{Name: k, Busy: val})
		}
	}
	out := make([]Health, 0, len(order))
	for _, id := range order {
		h := by[id]
		slices.SortFunc(h.Busiest, func(a, b Process) int { return cmp.Compare(b.Busy, a.Busy) })
		if len(h.Busiest) > 5 {
			h.Busiest = h.Busiest[:5]
		}
		out = append(out, *h)
	}
	// Servers (met NVPS) eerst, dan op naam.
	slices.SortStableFunc(out, func(a, b Health) int {
		if (a.NVPS == nil) != (b.NVPS == nil) {
			if a.NVPS != nil {
				return -1
			}
			return 1
		}
		return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
	})
	return out
}
