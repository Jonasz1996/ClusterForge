// Package apps maakt van de stand van apps per node (facts.apps, fase 29)
// en de volumes van SeaweedFS een overzicht per cluster, met wat aandacht
// vraagt: een mislukte controle, de app op geen of op twee nodes, versies
// die verschillen, volumes met te weinig kopieën of een oude stand.
package apps

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// StaleAfter: een stand die ouder is, telt niet meer als actueel.
const StaleAfter = 10 * time.Minute

// Rollen in de stand.
const (
	RoleActive  = "actief"
	RoleStandby = "stand-by"
	RoleBefore  = "voor-overstap"
)

// Node is wat de server van één node weet.
type Node struct {
	ID       uuid.UUID
	Hostname string
	Address  string
	Online   bool
	// Active: lifecycle active (niet in onderhoud of afbouwen).
	Active bool
	Facts  []byte
}

// Issue is iets dat aandacht vraagt. Tone is red of amber.
type Issue struct {
	Tone   string `json:"tone"`
	Title  string `json:"title"`
	Detail string `json:"detail,omitempty"`
}

// NodeApp is de stand van een app op één node.
type NodeApp struct {
	NodeID   uuid.UUID `json:"node_id"`
	Hostname string    `json:"hostname"`
	Online   bool      `json:"online"`
	Stale    bool      `json:"stale"`
	protocol.AppStatus
}

// App is een app over alle nodes van het cluster.
type App struct {
	App    string    `json:"app"`
	Title  string    `json:"title"`
	Nodes  []NodeApp `json:"nodes"`
	Issues []Issue   `json:"issues"`
}

// StorageNode is de opslag van SeaweedFS op één node.
type StorageNode struct {
	Hostname string `json:"hostname"`
	// Bytes: de volumes op deze node, volgens de leider (of de node zelf).
	Bytes uint64 `json:"bytes"`
	// DiskTotal en DiskFree van de schijf met de volumes, 0 als onbekend.
	DiskTotal uint64 `json:"disk_total"`
	DiskFree  uint64 `json:"disk_free"`
	Volumes   int    `json:"volumes"`
}

// Storage is SeaweedFS over het cluster, uit de facts van de leider.
type Storage struct {
	Leader             string         `json:"leader,omitempty"`
	VolumeCount        int            `json:"volume_count"`
	UnderReplicated    int            `json:"under_replicated"`
	UnderReplicatedIDs []int          `json:"under_replicated_ids,omitempty"`
	LogicalBytes       uint64         `json:"logical_bytes"`
	Replications       map[string]int `json:"replications,omitempty"`
	Nodes              []StorageNode  `json:"nodes"`
	Issues             []Issue        `json:"issues"`
}

// Overview is alles voor de kaart Applicatie van een cluster.
type Overview struct {
	Apps    []App    `json:"apps"`
	Storage *Storage `json:"storage,omitempty"`
}

// Evaluate bouwt het overzicht.
func Evaluate(nodes []Node, now time.Time) Overview {
	out := Overview{Apps: []App{}}
	byApp := map[string]*App{}
	var order []string
	var storage *Storage
	var leaderNodes []storageCandidate
	for _, n := range nodes {
		var f protocol.Facts
		if len(n.Facts) == 0 || json.Unmarshal(n.Facts, &f) != nil {
			continue
		}
		for _, a := range f.Apps {
			x := byApp[a.App]
			if x == nil {
				x = &App{App: a.App, Title: a.Title}
				if x.Title == "" {
					x.Title = a.App
				}
				byApp[a.App] = x
				order = append(order, a.App)
			}
			stale := !n.Online || a.UpdatedAt.IsZero() || now.Sub(a.UpdatedAt) > StaleAfter
			if a.Checks == nil {
				a.Checks = []protocol.AppCheck{}
			}
			if a.Values == nil {
				a.Values = []protocol.AppValue{}
			}
			x.Nodes = append(x.Nodes, NodeApp{NodeID: n.ID, Hostname: n.Hostname, Online: n.Online, Stale: stale, AppStatus: a})
		}
		if sw := f.Seaweed; sw != nil {
			leaderNodes = append(leaderNodes, storageCandidate{node: n, facts: sw})
		}
	}
	for _, name := range order {
		x := byApp[name]
		x.Issues = appIssues(x, nodes)
		out.Apps = append(out.Apps, *x)
	}
	storage = buildStorage(leaderNodes)
	out.Storage = storage
	return out
}

func appIssues(a *App, nodes []Node) []Issue {
	issues := []Issue{}
	activeNode := map[uuid.UUID]bool{}
	for _, n := range nodes {
		activeNode[n.ID] = n.Active
	}
	var actives, stale []string
	versions := map[string][]string{}
	before := 0
	for _, n := range a.Nodes {
		if !activeNode[n.NodeID] {
			continue
		}
		if n.Stale {
			stale = append(stale, n.Hostname)
			continue
		}
		switch n.Role {
		case RoleActive:
			actives = append(actives, n.Hostname)
		case RoleBefore:
			before++
		}
		if n.Version != "" {
			versions[short(n.Version)] = append(versions[short(n.Version)], n.Hostname)
		}
		for _, c := range n.Checks {
			if c.OK {
				continue
			}
			tone := "amber"
			if c.Severity == "red" {
				tone = "red"
			}
			issues = append(issues, Issue{Tone: tone, Title: c.Label + " op " + n.Hostname, Detail: c.Detail})
		}
	}
	switch {
	case len(actives) > 1:
		slices.Sort(actives)
		issues = append(issues, Issue{Tone: "red", Title: a.Title + " draait op meer dan één node",
			Detail: strings.Join(actives, ", ") + " hebben allebei het VIP (split-brain van keepalived)"})
	case len(actives) == 0 && before == 0 && len(a.Nodes) > len(stale):
		issues = append(issues, Issue{Tone: "red", Title: a.Title + " draait op geen enkele node",
			Detail: "Geen node heeft het VIP: kijk naar keepalived en de mount"})
	}
	if len(versions) > 1 {
		var parts []string
		for v, hs := range versions {
			slices.Sort(hs)
			parts = append(parts, v+" op "+strings.Join(hs, ", "))
		}
		slices.Sort(parts)
		issues = append(issues, Issue{Tone: "amber", Title: a.Title + ": de nodes hebben niet dezelfde versie gebouwd",
			Detail: strings.Join(parts, "; ") + ". Na een failover draait dan een andere versie; rol de versie opnieuw uit (Wijzigen)."})
	}
	if len(stale) > 0 {
		slices.Sort(stale)
		issues = append(issues, Issue{Tone: "amber", Title: a.Title + ": geen recente stand van " + strings.Join(stale, ", "),
			Detail: "De timer van de stand draait niet, of de agent antwoordt niet"})
	}
	return issues
}

type storageCandidate struct {
	node  Node
	facts *protocol.SeaweedFacts
}

func buildStorage(cands []storageCandidate) *Storage {
	if len(cands) == 0 {
		return nil
	}
	st := &Storage{Nodes: []StorageNode{}, Issues: []Issue{}}
	var leader *protocol.SeaweedMaster
	for _, c := range cands {
		if m := c.facts.Master; m != nil && m.IsLeader && m.VolumeCount > 0 && c.node.Online {
			leader = m
			st.Leader = c.node.Hostname
			break
		}
	}
	for _, c := range cands {
		sn := StorageNode{Hostname: c.node.Hostname}
		if v := c.facts.Volume; v != nil {
			sn.Bytes, sn.Volumes = v.Bytes, v.Volumes
		}
		if leader != nil {
			for url, b := range leader.NodeBytes {
				host, _, _ := strings.Cut(url, ":")
				if host != "" && host == c.node.Address {
					sn.Bytes = b
				}
			}
		}
		for _, p := range c.facts.Processes {
			if p.Role != "volume" && p.Role != "server" {
				continue
			}
			for _, d := range p.Disks {
				if d.Total > sn.DiskTotal {
					sn.DiskTotal, sn.DiskFree = d.Total, d.Free
				}
			}
		}
		st.Nodes = append(st.Nodes, sn)
	}
	slices.SortFunc(st.Nodes, func(a, b StorageNode) int { return strings.Compare(a.Hostname, b.Hostname) })
	if leader == nil {
		return st
	}
	st.VolumeCount = leader.VolumeCount
	st.UnderReplicated = leader.UnderReplicated
	st.UnderReplicatedIDs = leader.UnderReplicatedIDs
	st.LogicalBytes = leader.LogicalBytes
	st.Replications = leader.Replications
	if st.UnderReplicated > 0 {
		st.Issues = append(st.Issues, Issue{Tone: "red",
			Title:  fmt.Sprintf("%d van %d SeaweedFS-volumes hebben te weinig kopieën", st.UnderReplicated, st.VolumeCount),
			Detail: "Valt een node uit, dan is die media weg. Op de leider: platform-ctl.sh replicatie, of weed shell volume.fix.replication"})
	}
	if len(st.Replications) > 1 {
		var parts []string
		for rp, n := range st.Replications {
			parts = append(parts, fmt.Sprintf("%d volumes op %s", n, rp))
		}
		slices.Sort(parts)
		st.Issues = append(st.Issues, Issue{Tone: "amber", Title: "SeaweedFS-volumes met verschillende replicatie",
			Detail: strings.Join(parts, ", ") + ". De omzetting loopt nog, of is niet gestart (platform-ctl.sh replicatie)."})
	}
	return st
}

func short(v string) string {
	if len(v) > 12 {
		return v[:12]
	}
	return v
}
