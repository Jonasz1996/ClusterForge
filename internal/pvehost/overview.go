package pvehost

// Fase 26: het overzicht van een Proxmox-cluster: quorum en corosync, de
// hosts, wat er draait, storage, HA, en of de capaciteit klopt
// (overcommit, uitval van de grootste host).

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// num leest een getal uit JSON van Proxmox: soms een getal, soms tekst.
type num float64

func (n *num) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "null" {
		*n = 0
		return nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		*n = 0
		return nil //nolint:nilerr // onbekende waarde telt als 0
	}
	*n = num(f)
	return nil
}

// Resource is één regel van /cluster/resources.
type Resource struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Node     string `json:"node"`
	Status   string `json:"status"`
	Name     string `json:"name"`
	VMID     num    `json:"vmid"`
	CPU      num    `json:"cpu"`
	MaxCPU   num    `json:"maxcpu"`
	Mem      num    `json:"mem"`
	MaxMem   num    `json:"maxmem"`
	Disk     num    `json:"disk"`
	MaxDisk  num    `json:"maxdisk"`
	Uptime   num    `json:"uptime"`
	Template num    `json:"template"`
	HAState  string `json:"hastate"`
	Tags     string `json:"tags"`
	Lock     string `json:"lock"`
	Storage  string `json:"storage"`
	Shared   num    `json:"shared"`
	Plugin   string `json:"plugintype"`
	Content  string `json:"content"`
	Pool     string `json:"pool"`
}

type clusterStatus struct {
	Type    string `json:"type"`
	Name    string `json:"name"`
	Quorate num    `json:"quorate"`
	Nodes   num    `json:"nodes"`
	Version num    `json:"version"`
	Online  num    `json:"online"`
	IP      string `json:"ip"`
	Local   num    `json:"local"`
	NodeID  num    `json:"nodeid"`
}

type haEntry struct {
	ID     string `json:"id"`
	Type   string `json:"type"`
	Node   string `json:"node"`
	Status string `json:"status"`
	SID    string `json:"sid"`
	State  string `json:"state"`
	Quorum num    `json:"quorate"`
}

type corosyncNode struct {
	Node  string `json:"node"`
	Votes num    `json:"quorum_votes"`
	Ring0 string `json:"ring0_addr"`
	Ring1 string `json:"ring1_addr"`
}

type nodeStatus struct {
	CPUInfo struct {
		Model   string `json:"model"`
		CPUs    num    `json:"cpus"`
		Sockets num    `json:"sockets"`
	} `json:"cpuinfo"`
	Kernel  string   `json:"kversion"`
	PVE     string   `json:"pveversion"`
	LoadAvg []string `json:"loadavg"`
	Current struct {
		Release string `json:"release"`
	} `json:"current-kernel"`
	Boot struct {
		Mode       string `json:"mode"`
		SecureBoot num    `json:"secureboot"`
	} `json:"boot-info"`
	KSM struct {
		Shared num `json:"shared"`
	} `json:"ksm"`
}

// OverviewNode is één host in het overzicht.
type OverviewNode struct {
	Name   string  `json:"name"`
	Online bool    `json:"online"`
	IP     string  `json:"ip,omitempty"`
	CPU    float64 `json:"cpu"`
	Cores  int     `json:"cores"`
	Mem    int64   `json:"mem"`
	MaxMem int64   `json:"maxmem"`
	Uptime int64   `json:"uptime"`
	// Corosync.
	Votes int      `json:"votes"`
	Links []string `json:"links,omitempty"`
	// HA: active, idle, maintenance mode, …; leeg zonder HA.
	LRM string `json:"lrm,omitempty"`
	// Uit /nodes/<host>/status, als de agent antwoordde.
	CPUModel string   `json:"cpu_model,omitempty"`
	Kernel   string   `json:"kernel,omitempty"`
	PVE      string   `json:"pve,omitempty"`
	Load     []string `json:"load,omitempty"`
	Boot     string   `json:"boot,omitempty"`
	KSM      int64    `json:"ksm,omitempty"`
	// Gasten op deze host.
	Running   int    `json:"running"`
	Guests    int    `json:"guests"`
	VCPURun   int    `json:"vcpu_running"`
	VCPUAll   int    `json:"vcpu_all"`
	MemRun    int64  `json:"mem_assigned_running"`
	MemAll    int64  `json:"mem_assigned_all"`
	HasAgent  bool   `json:"has_agent"`
	AgentNode string `json:"agent_node_id,omitempty"`
}

type OverviewStorage struct {
	Name   string `json:"name"`
	Node   string `json:"node,omitempty"`
	Shared bool   `json:"shared"`
	Type   string `json:"type"`
	Used   int64  `json:"used"`
	Total  int64  `json:"total"`
	Status string `json:"status"`
	// Content zoals images,rootdir,backup.
	Content string `json:"content,omitempty"`
}

// Warning is een waarschuwing over capaciteit of gezondheid.
type Warning struct {
	// Level is crit of warn.
	Level string `json:"level"`
	Text  string `json:"text"`
	// Area is quorum, node, memory, cpu, storage, ha, failover.
	Area string `json:"area"`
}

type Capacity struct {
	MemTotal    int64 `json:"mem_total"`
	MemUsed     int64 `json:"mem_used"`
	MemRunning  int64 `json:"mem_assigned_running"`
	MemAll      int64 `json:"mem_assigned_all"`
	Cores       int   `json:"cores"`
	VCPURunning int   `json:"vcpu_running"`
	VCPUAll     int   `json:"vcpu_all"`
	// Failover: wat draait past nog als de grootste host uitvalt.
	Failover     bool   `json:"failover"`
	FailoverHost string `json:"failover_host,omitempty"`
	// FailoverShort is hoeveel geheugen er dan tekort is.
	FailoverShort int64 `json:"failover_short,omitempty"`
}

type HASummary struct {
	Enabled   bool   `json:"enabled"`
	Quorum    string `json:"quorum,omitempty"`
	Master    string `json:"master,omitempty"`
	Resources int    `json:"resources"`
	// Problems zijn resources in error, fence of recovery.
	Problems []string `json:"problems"`
}

type Overview struct {
	Datacenter Datacenter `json:"datacenter"`
	Name       string     `json:"name"`
	Quorate    bool       `json:"quorate"`
	// ConfigVersion is de versie van corosync.conf.
	ConfigVersion int               `json:"config_version,omitempty"`
	QDevice       bool              `json:"qdevice"`
	Nodes         []OverviewNode    `json:"nodes"`
	Storages      []OverviewStorage `json:"storages"`
	VMs           int               `json:"vms"`
	CTs           int               `json:"cts"`
	Running       int               `json:"running"`
	Templates     int               `json:"templates"`
	HA            HASummary         `json:"ha"`
	Capacity      Capacity          `json:"capacity"`
	Warnings      []Warning         `json:"warnings"`
	// Errors zijn delen die niet gelezen konden worden.
	Errors []string `json:"errors"`
}

// Overview leest alles en rekent het overzicht uit.
func (s *Service) Overview(ctx context.Context, dcID string) (Overview, error) {
	dc, err := s.Datacenter(ctx, dcID)
	if err != nil {
		return Overview{}, err
	}
	in := overviewInput{dc: dc}
	if err := s.GetInto(ctx, dcID, "/cluster/resources", nil, &in.resources); err != nil {
		return Overview{}, err
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	soft := func(path string, v any) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.GetInto(ctx, dcID, path, nil, v); err != nil {
				mu.Lock()
				in.errors = append(in.errors, fmt.Sprintf("%s: %v", path, err))
				mu.Unlock()
			}
		}()
	}
	soft("/cluster/status", &in.status)
	if !dc.Standalone {
		soft("/cluster/ha/status/current", &in.ha)
		soft("/cluster/config/nodes", &in.corosync)
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Zonder QDevice geeft Proxmox een fout of een leeg object.
			var q map[string]any
			if s.GetInto(ctx, dcID, "/cluster/config/qdevice", nil, &q) == nil && len(q) > 0 {
				mu.Lock()
				in.qdevice = true
				mu.Unlock()
			}
		}()
	}
	in.nodeStatus = map[string]nodeStatus{}
	for _, h := range dc.Hosts {
		if h.APIReady() != "" {
			continue
		}
		wg.Add(1)
		go func(node string) {
			defer wg.Done()
			var ns nodeStatus
			if err := s.GetInto(ctx, dcID, "/nodes/"+node+"/status", nil, &ns); err == nil {
				mu.Lock()
				in.nodeStatus[node] = ns
				mu.Unlock()
			}
		}(h.PVENode)
	}
	wg.Wait()
	return analyze(in), nil
}

type overviewInput struct {
	dc         Datacenter
	resources  []Resource
	status     []clusterStatus
	ha         []haEntry
	corosync   []corosyncNode
	nodeStatus map[string]nodeStatus
	errors     []string
	qdevice    bool
}

const gib = 1 << 30

func pct(a, b int64) float64 {
	if b <= 0 {
		return 0
	}
	return float64(a) / float64(b) * 100
}

// analyze rekent het overzicht uit wat Proxmox gaf.
func analyze(in overviewInput) Overview {
	o := Overview{Datacenter: in.dc, Name: in.dc.Name, QDevice: in.qdevice, Nodes: []OverviewNode{}, Storages: []OverviewStorage{}, Warnings: []Warning{}, Errors: in.errors}
	if o.Errors == nil {
		o.Errors = []string{}
	}
	o.HA.Problems = []string{}
	nodes := map[string]*OverviewNode{}
	var order []string
	node := func(name string) *OverviewNode {
		if n, ok := nodes[name]; ok {
			return n
		}
		n := &OverviewNode{Name: name}
		nodes[name] = n
		order = append(order, name)
		return n
	}
	o.Quorate = in.dc.Standalone
	for _, s := range in.status {
		switch s.Type {
		case "cluster":
			o.Name, o.Quorate, o.ConfigVersion = s.Name, s.Quorate == 1, int(s.Version)
		case "node":
			n := node(s.Name)
			n.Online, n.IP = s.Online == 1, s.IP
		}
	}
	for _, r := range in.resources {
		switch r.Type {
		case "node":
			n := node(r.Node)
			if len(in.status) == 0 {
				n.Online = r.Status == "online"
			}
			n.CPU, n.Cores, n.Mem, n.MaxMem, n.Uptime = float64(r.CPU), int(r.MaxCPU), int64(r.Mem), int64(r.MaxMem), int64(r.Uptime)
		case "qemu", "lxc":
			if r.Template == 1 {
				o.Templates++
				continue
			}
			if r.Type == "qemu" {
				o.VMs++
			} else {
				o.CTs++
			}
			n := node(r.Node)
			n.Guests++
			n.VCPUAll += int(r.MaxCPU)
			n.MemAll += int64(r.MaxMem)
			if r.Status == "running" {
				o.Running++
				n.Running++
				n.VCPURun += int(r.MaxCPU)
				n.MemRun += int64(r.MaxMem)
			}
		case "storage":
			if r.Shared == 1 {
				dup := false
				for _, s := range o.Storages {
					if s.Shared && s.Name == r.Storage {
						dup = true
					}
				}
				if dup {
					continue
				}
			}
			st := OverviewStorage{Name: r.Storage, Shared: r.Shared == 1, Type: r.Plugin, Used: int64(r.Disk), Total: int64(r.MaxDisk), Status: r.Status, Content: r.Content}
			if !st.Shared {
				st.Node = r.Node
			}
			o.Storages = append(o.Storages, st)
		}
	}
	for _, c := range in.corosync {
		n := node(c.Node)
		n.Votes = int(c.Votes)
		for _, l := range []string{c.Ring0, c.Ring1} {
			if l != "" {
				n.Links = append(n.Links, l)
			}
		}
	}
	for _, h := range in.dc.Hosts {
		n := node(h.PVENode)
		n.HasAgent = true
		n.AgentNode = h.NodeID.String()
	}
	for name, ns := range in.nodeStatus {
		n := node(name)
		n.CPUModel, n.Kernel, n.Load, n.Boot, n.KSM = ns.CPUInfo.Model, ns.Kernel, ns.LoadAvg, ns.Boot.Mode, int64(ns.KSM.Shared)
		if ns.Current.Release != "" {
			n.Kernel = ns.Current.Release
		}
		if v, _, ok := strings.Cut(strings.TrimPrefix(ns.PVE, "pve-manager/"), "/"); ok {
			n.PVE = v
		}
	}
	for _, e := range in.ha {
		switch e.Type {
		case "quorum":
			o.HA.Quorum = e.Status
		case "master":
			o.HA.Master = e.Node
		case "lrm":
			node(e.Node).LRM = e.Status
		case "service":
			o.HA.Resources++
			if st := firstNonEmpty(e.State, e.Status); strings.Contains(st, "error") || strings.Contains(st, "fence") || strings.Contains(st, "recovery") {
				o.HA.Problems = append(o.HA.Problems, e.SID+": "+st)
			}
		}
	}
	o.HA.Enabled = o.HA.Resources > 0
	sort.Strings(order)
	for _, name := range order {
		o.Nodes = append(o.Nodes, *nodes[name])
	}
	sort.Slice(o.Storages, func(i, j int) bool {
		if o.Storages[i].Name != o.Storages[j].Name {
			return o.Storages[i].Name < o.Storages[j].Name
		}
		return o.Storages[i].Node < o.Storages[j].Node
	})
	o.Capacity = capacity(o.Nodes)
	o.Warnings = warnings(o)
	return o
}

func capacity(nodes []OverviewNode) Capacity {
	var c Capacity
	var biggest OverviewNode
	for _, n := range nodes {
		if !n.Online {
			continue
		}
		c.MemTotal += n.MaxMem
		c.MemUsed += n.Mem
		c.Cores += n.Cores
		if n.MaxMem > biggest.MaxMem {
			biggest = n
		}
	}
	for _, n := range nodes {
		c.MemRunning += n.MemRun
		c.MemAll += n.MemAll
		c.VCPURunning += n.VCPURun
		c.VCPUAll += n.VCPUAll
	}
	online := 0
	for _, n := range nodes {
		if n.Online {
			online++
		}
	}
	c.Failover = true
	if online >= 2 {
		left := c.MemTotal - biggest.MaxMem
		// De hosts zelf houden ongeveer 10% voor Proxmox, ZFS-cache en zo.
		if need := c.MemRunning; float64(need) > float64(left)*0.9 {
			c.Failover = false
			c.FailoverHost = biggest.Name
			c.FailoverShort = need - int64(float64(left)*0.9)
		}
	}
	return c
}

func warnings(o Overview) []Warning {
	w := []Warning{}
	add := func(level, area, f string, a ...any) {
		w = append(w, Warning{Level: level, Area: area, Text: fmt.Sprintf(f, a...)})
	}
	if !o.Datacenter.Standalone && !o.Quorate {
		add("crit", "quorum", "Het cluster heeft geen quorum: niets kan starten of veranderen tot genoeg hosts weer online zijn")
	}
	online := 0
	for _, n := range o.Nodes {
		if !n.Online {
			add("crit", "node", "%s is offline", n.Name)
			continue
		}
		online++
		if n.LRM != "" && strings.Contains(n.LRM, "maintenance") {
			add("warn", "node", "%s staat in onderhoudsmodus", n.Name)
		}
		if p := pct(n.MemRun, n.MaxMem); p > 100 {
			add("crit", "memory", "%s: de draaiende gasten krijgen samen %.0f%% van het geheugen (%s van %s); zonder ballooning of KSM kan de host gaan swappen of OOM killen",
				n.Name, p, gibs(n.MemRun), gibs(n.MaxMem))
		} else if p > 90 {
			add("warn", "memory", "%s: de draaiende gasten krijgen samen %.0f%% van het geheugen", n.Name, p)
		}
		if n.Cores > 0 && n.VCPURun > 4*n.Cores {
			add("warn", "cpu", "%s: %d vCPU's op %d threads (meer dan 4 op 1)", n.Name, n.VCPURun, n.Cores)
		}
		if n.MaxMem > 0 && pct(n.Mem, n.MaxMem) > 95 {
			add("crit", "memory", "%s gebruikt %.0f%% van zijn geheugen", n.Name, pct(n.Mem, n.MaxMem))
		}
	}
	if !o.Capacity.Failover {
		add("warn", "failover", "Valt %s uit, dan past niet alles wat draait op de andere hosts: %s tekort", o.Capacity.FailoverHost, gibs(o.Capacity.FailoverShort))
	}
	for _, s := range o.Storages {
		where := s.Name
		if s.Node != "" {
			where += " op " + s.Node
		}
		if s.Status != "" && s.Status != "available" && s.Status != "active" {
			add("warn", "storage", "storage %s is %s", where, s.Status)
			continue
		}
		if p := pct(s.Used, s.Total); p > 95 {
			add("crit", "storage", "storage %s is %.0f%% vol", where, p)
		} else if p > 85 {
			add("warn", "storage", "storage %s is %.0f%% vol", where, p)
		}
	}
	for _, p := range o.HA.Problems {
		add("crit", "ha", "HA: %s", p)
	}
	if o.HA.Enabled && o.HA.Quorum != "" && o.HA.Quorum != "OK" {
		add("crit", "ha", "HA-quorum: %s", o.HA.Quorum)
	}
	if !o.Datacenter.Standalone && !o.QDevice && online == 2 && len(o.Nodes) == 2 {
		add("warn", "quorum", "Twee hosts zonder QDevice: valt er één uit, dan verliest het cluster zijn quorum")
	}
	sort.SliceStable(w, func(i, j int) bool { return w[i].Level == "crit" && w[j].Level != "crit" })
	return w
}

func gibs(b int64) string {
	return strconv.FormatFloat(float64(b)/gib, 'f', 1, 64) + " GB"
}

// MarshalJSON zorgt dat num als getal in JSON komt.
func (n num) MarshalJSON() ([]byte, error) { return json.Marshal(float64(n)) }
