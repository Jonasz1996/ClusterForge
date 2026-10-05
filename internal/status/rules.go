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
}

// Node past de regels voor één node toe.
func Node(in NodeInput) Result {
	switch {
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
	// down is of geen agent heeft.
	Addresses []string
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

// Cluster past de regels voor een cluster toe.
func Cluster(nodes []ClusterNode, vips []ClusterVIP) ClusterResult {
	res := ClusterResult{Holders: map[uuid.UUID][]uuid.UUID{}}
	hostname := map[uuid.UUID]string{}
	for _, n := range nodes {
		hostname[n.ID] = n.Hostname
	}
	for _, v := range vips {
		holders := []uuid.UUID{}
		for _, n := range nodes {
			if slices.Contains(n.Addresses, v.Address) {
				holders = append(holders, n.ID)
			}
		}
		res.Holders[v.ID] = holders
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
