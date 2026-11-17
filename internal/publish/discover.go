package publish

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// refreshServices laat elke node met een nieuwe agent zijn diensten in kaart
// brengen, vier tegelijk.
func (s *Service) refreshServices(ctx context.Context) {
	nodes, err := s.nodes(ctx)
	if err != nil {
		s.log.Warn("publicatie: nodes lezen", "err", err)
		return
	}
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	for _, n := range nodes {
		if n.Ready() != "" {
			continue
		}
		wg.Add(1)
		go func(n nodeRow) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			cctx, cancel := context.WithTimeout(ctx, 90*time.Second)
			defer cancel()
			res, err := s.bus.Web(cctx, n.ID, protocol.WebRequest{Op: protocol.WebOpDiscover, Probe: true})
			msg := ""
			switch {
			case err != nil:
				msg = err.Error()
			case res.Error != "":
				msg = res.Error
			}
			data, _ := json.Marshal(res.Services)
			if res.Services == nil {
				data = []byte("[]")
			}
			if _, err := s.pool.Exec(ctx, `INSERT INTO publish_services (node_id, services, error, checked_at) VALUES ($1, $2, $3, now())
				ON CONFLICT (node_id) DO UPDATE SET services = CASE WHEN EXCLUDED.error = '' THEN EXCLUDED.services ELSE publish_services.services END,
				error = EXCLUDED.error, checked_at = now()`, n.ID, data, msg); err != nil {
				s.log.Warn("publicatie: diensten bewaren", "node", n.Hostname, "err", err)
			}
		}(n)
	}
	wg.Wait()
}

// Service is een dienst zoals de dienstenkaart hem toont: één poort, op één
// node of op alle nodes van een cluster.
type ServiceRow struct {
	// Key is uniek: cluster- of node-id + proto + poort.
	Key         string      `json:"key"`
	ClusterID   *uuid.UUID  `json:"cluster_id,omitempty"`
	ClusterName string      `json:"cluster_name,omitempty"`
	Environment string      `json:"environment,omitempty"`
	Nodes       []NodeShort `json:"nodes"`
	// OnAll: de dienst draait op elke node van het cluster.
	OnAll bool   `json:"on_all"`
	Proto string `json:"proto"`
	Port  int    `json:"port"`
	Kind  string `json:"kind,omitempty"`
	// Name is wat de GUI toont: container, unit of titel.
	Name      string `json:"name"`
	Title     string `json:"title,omitempty"`
	Container string `json:"container,omitempty"`
	Image     string `json:"image,omitempty"`
	Unit      string `json:"unit,omitempty"`
	Status    int    `json:"status,omitempty"`
	Server    string `json:"server,omitempty"`
	// Target is waar NPM heen moet: het VIP als de dienst op alle nodes
	// draait, anders het IP van de node.
	Target    string `json:"target"`
	TargetVIP bool   `json:"target_vip"`
	// Label is het voorstel voor het subdomein.
	Label string `json:"label"`
	// Web: http of https, dus te publiceren.
	Web bool `json:"web"`
	// Published zijn de proxy hosts die er nu naartoe wijzen.
	Published []PublishedRef `json:"published"`
	// AuthentikDefault: nieuwe publicatie standaard achter Authentik.
	AuthentikDefault bool `json:"authentik_default"`
}

// NodeShort is een node bij een dienst.
type NodeShort struct {
	ID       uuid.UUID `json:"id"`
	Hostname string    `json:"hostname"`
	IP       string    `json:"ip,omitempty"`
}

// PublishedRef is een proxy host bij een dienst.
type PublishedRef struct {
	HostID    int    `json:"host_id"`
	Domain    string `json:"domain"`
	Authentik string `json:"authentik"`
}

type nodeServices struct {
	node     nodeRow
	services []protocol.WebService
	err      string
	at       time.Time
}

func (s *Service) loadServices(ctx context.Context, nodes []nodeRow) (map[uuid.UUID]nodeServices, error) {
	rows, err := s.pool.Query(ctx, `SELECT node_id, services, error, checked_at FROM publish_services`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byID := map[uuid.UUID]nodeRow{}
	for _, n := range nodes {
		byID[n.ID] = n
	}
	out := map[uuid.UUID]nodeServices{}
	for rows.Next() {
		var id uuid.UUID
		var data []byte
		var ns nodeServices
		if err := rows.Scan(&id, &data, &ns.err, &ns.at); err != nil {
			return nil, err
		}
		n, ok := byID[id]
		if !ok {
			continue
		}
		ns.node = n
		_ = json.Unmarshal(data, &ns.services)
		out[id] = ns
	}
	return out, rows.Err()
}

var labelClean = regexp.MustCompile(`[^a-z0-9-]+`)

// SuggestLabel maakt een subdomein van een naam: kleine letters, zonder
// cf-, -1, _server en dergelijke.
func SuggestLabel(names ...string) string {
	for _, n := range names {
		n = strings.ToLower(n)
		if i := strings.LastIndexByte(n, '/'); i >= 0 {
			n = n[i+1:]
		}
		n, _, _ = strings.Cut(n, ":")
		n = strings.TrimPrefix(n, "cf-")
		n = strings.ReplaceAll(n, "_", "-")
		n = labelClean.ReplaceAllString(n, "-")
		for _, suf := range []string{"-1", "-2", "-3", "-server", "-app", "-web"} {
			n = strings.TrimSuffix(n, suf)
		}
		n = strings.Trim(n, "-")
		if len(n) > 40 {
			n = strings.Trim(n[:40], "-")
		}
		if n != "" && n != "docker" && n != "docker-proxy" {
			return n
		}
	}
	return ""
}

// skipAuthentik zegt of een dienst standaard niet achter Authentik gaat.
func skipAuthentik(d Defaults, words ...string) bool {
	for _, w := range words {
		w = strings.ToLower(w)
		for _, skip := range d.AuthentikSkip {
			if skip != "" && strings.Contains(w, skip) {
				return true
			}
		}
	}
	return false
}

// buildServices voegt de ontdekking samen tot de dienstenkaart.
func buildServices(nodes []nodeRow, clusters []clusterRow, found map[uuid.UUID]nodeServices, d Defaults) []ServiceRow {
	clusterNodes := map[uuid.UUID][]nodeRow{}
	for _, n := range nodes {
		if n.ClusterID != nil {
			clusterNodes[*n.ClusterID] = append(clusterNodes[*n.ClusterID], n)
		}
	}
	clusterByID := map[uuid.UUID]clusterRow{}
	for _, c := range clusters {
		clusterByID[c.ID] = c
	}
	type agg struct {
		row   ServiceRow
		nodes []nodeRow
	}
	groups := map[string]*agg{}
	var order []string
	for _, n := range nodes {
		ns, ok := found[n.ID]
		if !ok {
			continue
		}
		for _, svc := range ns.services {
			owner := n.ID.String()
			if n.ClusterID != nil {
				owner = n.ClusterID.String()
			}
			key := owner + "/" + svc.Proto + "/" + strconv.Itoa(svc.Port)
			g, ok := groups[key]
			if !ok {
				name := svc.Container
				if name == "" {
					name = svc.Unit
				}
				if name == "" && svc.Title != "" {
					name = svc.Title
				}
				if name == "" {
					name = fmt.Sprintf("poort %d", svc.Port)
				}
				g = &agg{row: ServiceRow{
					Key: key, ClusterID: n.ClusterID, ClusterName: n.ClusterName, Environment: n.Environment,
					Proto: svc.Proto, Port: svc.Port, Kind: svc.Kind, Name: name, Title: svc.Title,
					Container: svc.Container, Image: svc.Image, Unit: svc.Unit, Status: svc.Status, Server: svc.Server,
					Web: svc.Kind == protocol.WebKindHTTP || svc.Kind == protocol.WebKindHTTPS,
				}}
				groups[key] = g
				order = append(order, key)
			} else if !g.row.Web && (svc.Kind == protocol.WebKindHTTP || svc.Kind == protocol.WebKindHTTPS) {
				g.row.Kind, g.row.Web, g.row.Status, g.row.Title = svc.Kind, true, svc.Status, svc.Title
			}
			g.nodes = append(g.nodes, n)
		}
	}
	out := make([]ServiceRow, 0, len(order))
	for _, key := range order {
		g := groups[key]
		r := g.row
		for _, n := range g.nodes {
			r.Nodes = append(r.Nodes, NodeShort{ID: n.ID, Hostname: n.Hostname, IP: n.PrimaryIP})
		}
		if r.ClusterID != nil {
			members := clusterNodes[*r.ClusterID]
			r.OnAll = len(members) > 1 && len(g.nodes) == len(members)
			c := clusterByID[*r.ClusterID]
			if len(c.VIPs) > 0 && (r.OnAll || len(members) > 1 && clusterHasFloating(c, g.nodes)) {
				r.Target, r.TargetVIP = c.VIPs[0].Address, true
			}
		}
		if r.Target == "" && len(g.nodes) > 0 {
			r.Target = g.nodes[0].PrimaryIP
		}
		r.Label = SuggestLabel(r.Container, r.Unit, r.ClusterName, r.Name)
		r.AuthentikDefault = d.Authentik && !skipAuthentik(d, r.Container, r.Image, r.Unit, r.ClusterName, r.Title, r.Label)
		r.Published = []PublishedRef{}
		out = append(out, r)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].ClusterName != out[j].ClusterName {
			return out[i].ClusterName < out[j].ClusterName
		}
		return out[i].Port < out[j].Port
	})
	return out
}

// clusterHasFloating: een dienst die alleen op de node met het VIP draait
// (zoals NPM zelf of de checks van een cluster) volgt het VIP; dan is het VIP
// het doel.
func clusterHasFloating(c clusterRow, on []nodeRow) bool {
	if len(on) != 1 {
		return false
	}
	for _, v := range c.VIPs {
		if v.Owner != nil && *v.Owner == on[0].ID {
			return true
		}
	}
	return false
}
