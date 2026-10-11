package risk

import (
	"cmp"
	"net/url"
	"slices"
	"strings"

	"github.com/google/uuid"
)

// SPOF is een onderdeel waarvan de uitval alleen al een prod-cluster of een
// bevestigde prod-dienst down maakt, ook nadat HA herstart wat het kan.
type SPOF struct {
	Component Component     `json:"component"`
	Clusters  []SPOFCluster `json:"clusters"`
	Services  []SPOFService `json:"services"`
	// Down is hoeveel clusters en diensten, in alle omgevingen, down gaan.
	Down int `json:"down"`
	// Self is true als ClusterForge zelf ook uitvalt.
	Self bool `json:"self"`
}

type SPOFCluster struct {
	ID     uuid.UUID `json:"id"`
	Name   string    `json:"name"`
	Reason string    `json:"reason"`
}

type SPOFService struct {
	ID        uuid.UUID `json:"id"`
	Name      string    `json:"name"`
	Group     string    `json:"group"`
	ClusterID uuid.UUID `json:"-"`
	Reason    string    `json:"reason"`
}

// spofKinds zijn de onderdelen die de SPOF-lijst afloopt. Een dienst of een
// cluster zelf is geen single point of failure.
var spofKinds = []string{KindHost, KindStorage, KindBridge, KindNode}

// SPOFs draait de wat-als voor elk onderdeel apart, alsof de rest gezond
// is, en geeft de onderdelen die prod down maken. De meeste uitval eerst.
func (w WhatIfWorld) SPOFs() []SPOF {
	out := []SPOF{}
	if w.Graph == nil {
		return out
	}
	healthy := healthyGraph(w.Graph)
	for _, c := range w.Components() {
		if !slices.Contains(spofKinds, c.Kind) {
			continue
		}
		r := w.run([]Component{c}, true, healthy)
		sp := SPOF{Component: c, Clusters: []SPOFCluster{}, Services: []SPOFService{}, Self: r.Self != nil && r.Self.Outcome == "down"}
		for _, co := range r.Clusters {
			if co.Final().Status != "down" || co.Before.Status == "down" {
				continue
			}
			sp.Down++
			if co.Env == "prod" {
				sp.Clusters = append(sp.Clusters, SPOFCluster{ID: co.ID, Name: co.Name, Reason: co.Final().Reason})
			}
		}
		for _, s := range r.Services {
			if s.Impact != "down" || s.Recovers {
				continue
			}
			sp.Down++
			if s.Env == "prod" {
				sp.Services = append(sp.Services, SPOFService{ID: s.ID, Name: s.Name, Group: s.Group, ClusterID: serviceCluster(w, s.ID), Reason: s.Reason})
			}
		}
		if len(sp.Clusters)+len(sp.Services) > 0 {
			out = append(out, sp)
		}
	}
	slices.SortStableFunc(out, func(a, b SPOF) int {
		return cmp.Or(cmp.Compare(b.Down, a.Down), cmp.Compare(slices.Index(kindOrder, a.Component.Kind), slices.Index(kindOrder, b.Component.Kind)),
			cmp.Compare(strings.ToLower(a.Component.Name), strings.ToLower(b.Component.Name)))
	})
	return out
}

// serviceCluster is het cluster van een dienst, of van zijn node.
func serviceCluster(w WhatIfWorld, id uuid.UUID) uuid.UUID {
	s, ok := w.Graph.Service(id)
	switch {
	case !ok:
	case s.ClusterID != nil:
		return *s.ClusterID
	case s.NodeID != nil:
		if n, ok := w.Graph.Node(*s.NodeID); ok && n.ClusterID != nil {
			return *n.ClusterID
		}
	}
	return uuid.Nil
}

// SPOFInput is een SPOF zoals de score van één cluster hem ziet.
type SPOFInput struct {
	Key  string
	Kind string
	// Name is de naam van het onderdeel, zoals "pve1" of "local-lvm op
	// pve1"; Host de host bij een Proxmox-host.
	Name string
	// Host is de Proxmox-host van een host, lokale storage of bridge.
	Host string
	ID   uuid.UUID
	// ClusterDown is true als het cluster zelf down gaat; anders alleen
	// Services.
	ClusterDown bool
	Services    []string
}

// SPOFsByCluster verdeelt de SPOF's over de clusters die ze raken.
func SPOFsByCluster(spofs []SPOF) map[uuid.UUID][]SPOFInput {
	out := map[uuid.UUID][]SPOFInput{}
	for _, sp := range spofs {
		per := map[uuid.UUID]*SPOFInput{}
		var order []uuid.UUID
		get := func(id uuid.UUID) *SPOFInput {
			if per[id] == nil {
				c := sp.Component
				per[id] = &SPOFInput{Key: c.Key, Kind: c.Kind, Name: c.Name, Host: c.host, ID: c.componentID()}
				if c.Kind == KindHost {
					per[id].Name = c.host
				}
				order = append(order, id)
			}
			return per[id]
		}
		for _, c := range sp.Clusters {
			get(c.ID).ClusterDown = true
		}
		for _, s := range sp.Services {
			if s.ClusterID != uuid.Nil {
				in := get(s.ClusterID)
				in.Services = append(in.Services, s.Name)
			}
		}
		for _, id := range order {
			out[id] = append(out[id], *per[id])
		}
	}
	return out
}

// spofSubject noemt het onderdeel in een zin: "Proxmox-host pve1".
func spofSubject(s SPOFInput) string {
	switch s.Kind {
	case KindHost:
		return "Proxmox-host " + s.Name
	case KindStorage:
		return "storage " + strings.TrimSuffix(s.Name, " (gedeeld)")
	case KindBridge:
		return "bridge " + s.Name
	case KindNode:
		return "node " + s.Name
	}
	return s.Name
}

// spofLink opent de wat-als met het onderdeel gekozen.
func spofLink(key string) string { return "/risico?tab=wat-als&onderdeel=" + url.QueryEscape(key) }
