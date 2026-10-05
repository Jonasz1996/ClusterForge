// Package status berekent de status van nodes en clusters uit heartbeats,
// metrics en VIP's. De regels zijn bewust eenvoudig en staan allemaal hier; de
// health score van fase 2 vervangt ze met dezelfde inputs.
package status

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
)

type Status string

const (
	Unknown    Status = "unknown"
	Healthy    Status = "healthy"
	Degraded   Status = "degraded"
	Down       Status = "down"
	SplitBrain Status = "split_brain"
)

const (
	// HeartbeatLate en HeartbeatDown zijn de leeftijden waarop een node
	// degraded en down wordt.
	HeartbeatLate = 30 * time.Second
	HeartbeatDown = 90 * time.Second
	// DiskFull is de bezetting vanaf waar een bestandssysteem een node
	// degraded maakt.
	DiskFull = 0.90
	// VIPGrace is hoe lang een VIP op twee nodes of op geen enkele mag
	// staan voor het telt: anderhalf heartbeatinterval. Bij een gewone
	// wissel meldt de nieuwe node het adres vaak al voordat de heartbeat van
	// de oude het kwijt is, of andersom.
	VIPGrace = 15 * time.Second
)

// ignoredInactive zijn units die normaal inactief zijn terwijl ze enabled
// staan, omdat een socket ze start.
var ignoredInactive = []string{"ssh"}

type Result struct {
	Status Status
	Reason string
}

type NodeInput struct {
	HasAgent bool
	// HeartbeatAge is de leeftijd van de laatste heartbeat; negatief als er
	// nog nooit een was.
	HeartbeatAge  time.Duration
	DiskUsedRatio float64
	DiskUsedMount string
	// Services is de toestand per gevolgde unit uit de laatste heartbeat.
	Services map[string]string
	// EnabledServices zijn de units die volgens de facts enabled staan.
	EnabledServices []string
	// VMStatus is de toestand van de gekoppelde VM in Proxmox (running,
	// stopped, paused); leeg zonder koppeling.
	VMStatus string
}

// Node past de regels voor één node toe.
func Node(in NodeInput) Result {
	// Zonder heartbeat weet Proxmox soms waarom.
	offline := !in.HasAgent || in.HeartbeatAge < 0 || in.HeartbeatAge > HeartbeatDown
	switch {
	case offline && in.VMStatus == "stopped":
		return Result{Down, "VM staat uit in Proxmox"}
	case offline && in.VMStatus == "paused":
		return Result{Down, "VM is gepauzeerd in Proxmox"}
	case !in.HasAgent:
		return Result{Unknown, "geen agent"}
	case in.HeartbeatAge < 0:
		return Result{Unknown, "nog geen heartbeat ontvangen"}
	case in.HeartbeatAge > HeartbeatDown:
		return Result{Down, "geen heartbeat meer"}
	}
	var problems []string
	if in.HeartbeatAge > HeartbeatLate {
		problems = append(problems, "heartbeat vertraagd")
	}
	if in.DiskUsedRatio >= DiskFull {
		problems = append(problems, fmt.Sprintf("schijf %s is %.0f %% vol", in.DiskUsedMount, in.DiskUsedRatio*100))
	}
	names := make([]string, 0, len(in.Services))
	for name := range in.Services {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		state := in.Services[name]
		switch {
		case state == "failed":
			problems = append(problems, "service "+name+" is gefaald")
		case state == "inactive" && slices.Contains(in.EnabledServices, name) && !slices.Contains(ignoredInactive, name):
			problems = append(problems, "service "+name+" draait niet")
		}
	}
	if len(problems) > 0 {
		return Result{Degraded, strings.Join(problems, "; ")}
	}
	return Result{Healthy, ""}
}

type ClusterNode struct {
	ID       uuid.UUID
	Hostname string
	// Counts is false voor nodes die niet actief zijn (maintenance, draining,
	// provisioning, uit dienst); hun status telt niet mee voor het cluster.
	Counts bool
	Result Result
	// Addresses zijn de adressen uit een recente heartbeat; leeg als de node
	// down is, geen agent heeft of zijn VM uit staat.
	Addresses []string
	// HeartbeatAge is de leeftijd van de heartbeat waar Addresses uit komen.
	HeartbeatAge time.Duration
}

type ClusterVIP struct {
	ID      uuid.UUID
	Address string
}

type ClusterResult struct {
	Result
	// Holders zijn per VIP de nodes die het adres nu op een interface hebben.
	Holders map[uuid.UUID][]uuid.UUID
}

// Holders geeft per VIP de nodes die het adres nu hebben. Melden meerdere
// nodes het, dan tellen nodes met een heartbeat ouder dan grace niet mee
// zolang er een verse melding is: hun adressen zijn van voor de wissel, zoals
// bij een node die net vastliep.
func Holders(nodes []ClusterNode, vips []ClusterVIP, grace time.Duration) map[uuid.UUID][]uuid.UUID {
	out := map[uuid.UUID][]uuid.UUID{}
	for _, v := range vips {
		var all, fresh []uuid.UUID
		for _, n := range nodes {
			if slices.Contains(n.Addresses, v.Address) {
				all = append(all, n.ID)
				if n.HeartbeatAge <= grace {
					fresh = append(fresh, n.ID)
				}
			}
		}
		switch {
		case len(all) > 1 && len(fresh) > 0:
			out[v.ID] = fresh
		case all == nil:
			out[v.ID] = []uuid.UUID{}
		default:
			out[v.ID] = all
		}
	}
	return out
}

// Settle geeft de houders die meetellen. Staat een VIP op precies één node,
// dan is dat de houder. Staat het op meerdere of op geen enkele, dan blijft
// de vorige eigenaar staan zolang dat korter dan VIPGrace duurt; pas daarna
// is het split-brain of een VIP zonder eigenaar.
func Settle(raw []uuid.UUID, owner *uuid.UUID, unsettledFor, grace time.Duration) []uuid.UUID {
	if len(raw) == 1 || owner == nil || unsettledFor >= grace {
		return raw
	}
	return []uuid.UUID{*owner}
}

// Cluster past de regels voor een cluster toe, met de houders zoals de
// heartbeats ze nu melden.
func Cluster(nodes []ClusterNode, vips []ClusterVIP) ClusterResult {
	return ClusterWith(nodes, vips, Holders(nodes, vips, VIPGrace))
}

// ClusterWith past de regels toe met houders die de aanroeper al bepaalde,
// bijvoorbeeld na Settle.
func ClusterWith(nodes []ClusterNode, vips []ClusterVIP, holders map[uuid.UUID][]uuid.UUID) ClusterResult {
	res := ClusterResult{Holders: holders}
	hostname := map[uuid.UUID]string{}
	for _, n := range nodes {
		hostname[n.ID] = n.Hostname
	}

	var counted, monitored, down []ClusterNode
	for _, n := range nodes {
		if !n.Counts {
			continue
		}
		counted = append(counted, n)
		if n.Result.Status != Unknown {
			monitored = append(monitored, n)
		}
		if n.Result.Status == Down {
			down = append(down, n)
		}
	}
	if len(counted) == 0 {
		res.Result = Result{Unknown, "geen actieve nodes"}
		return res
	}
	if len(monitored) == 0 {
		res.Result = Result{Unknown, "geen agent op de actieve nodes"}
		return res
	}

	var splits, orphans []string
	for _, v := range vips {
		h := res.Holders[v.ID]
		switch {
		case len(h) > 1:
			names := make([]string, len(h))
			for i, id := range h {
				names[i] = hostname[id]
			}
			slices.Sort(names)
			splits = append(splits, fmt.Sprintf("VIP %s op %s", v.Address, strings.Join(names, " en ")))
		case len(h) == 0:
			orphans = append(orphans, "VIP "+v.Address+" heeft geen eigenaar")
		}
	}
	switch {
	case len(splits) > 0:
		res.Result = Result{SplitBrain, strings.Join(splits, "; ")}
	case len(down) == len(monitored):
		res.Result = Result{Down, "alle nodes zijn down"}
	case len(orphans) > 0:
		res.Result = Result{Down, strings.Join(orphans, "; ")}
	default:
		var problems []string
		for _, n := range monitored {
			if n.Result.Status != Healthy {
				problems = append(problems, n.Hostname+": "+n.Result.Reason)
			}
		}
		if len(problems) > 0 {
			res.Result = Result{Degraded, strings.Join(problems, "; ")}
		} else {
			res.Result = Result{Healthy, ""}
		}
	}
	return res
}
