package publish

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strings"
	"time"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// Testlijst 3 (23/11): ClusterForge kijkt zelf of een doel antwoordt, vóór
// het iets meldt of voorstelt.
//   - Een doel dat ClusterForge niet ontdekte maar wel antwoordt (Authentik
//     op .103.244:9000 gaf 302) is geen "draait niets".
//   - Een gok (zelfde poort, naam die erop lijkt) wordt alleen getoond als
//     de <title> van het nieuwe doel bij de naam van de host past
//     (adguard → "Web Check" valt weg).
//   - Een doel dat niet antwoordt en geen dienst met dezelfde naam heeft,
//     krijgt "opruimen?" in plaats van een gegokt nieuw doel; een heel net
//     dat niet meer antwoordt (192.168.0.x) is één melding.

// Probe is het antwoord van één adres, gezien vanaf de NPM-node.
type Probe struct {
	Status int       `json:"status,omitempty"`
	Title  string    `json:"title,omitempty"`
	Error  string    `json:"error,omitempty"`
	At     time.Time `json:"at"`
}

// Alive: het adres gaf een HTTP-antwoord (welke code ook).
func (p Probe) Alive() bool { return p.Error == "" && p.Status > 0 }

// Probes zijn de laatste antwoorden per URL.
type Probes map[string]Probe

const (
	probeFresh   = 10 * time.Minute
	probePerTurn = 40
)

func targetURL(scheme, host string, port int) string {
	if scheme == "" {
		scheme = "http"
	}
	return fmt.Sprintf("%s://%s/", scheme, net.JoinHostPort(host, fmt.Sprint(port)))
}

func (s *Service) probes(ctx context.Context) Probes {
	out := Probes{}
	_, _, _ = s.read(ctx, "probes", &out)
	return out
}

// ProbeTargets bekijkt de doelen die ClusterForge niet kent en de gokken.
// Het loopt na elke leesronde; een adres wordt hoogstens om de tien minuten
// opnieuw bekeken.
func (s *Service) ProbeTargets(ctx context.Context, st Settings) {
	if !st.NPMLinked() {
		return
	}
	ov, err := s.Overview(ctx)
	if err != nil {
		return
	}
	old := s.probes(ctx)
	want := map[string]bool{}
	for _, h := range ov.Hosts {
		if !h.Enabled || h.ServiceKey != "" || h.ForwardHost == "" {
			continue
		}
		want[targetURL(h.ForwardScheme, h.ForwardHost, h.ForwardPort)] = true
		if h.guess != nil {
			want[targetURL(h.guess.Scheme, h.guess.Host, h.guess.Port)] = true
		}
	}
	now := s.Now()
	var todo []string
	for u := range want {
		if p, ok := old[u]; !ok || now.Sub(p.At) > probeFresh {
			todo = append(todo, u)
		}
	}
	sort.Strings(todo)
	if len(todo) > probePerTurn {
		todo = todo[:probePerTurn]
	}
	next := Probes{}
	for u := range want {
		if p, ok := old[u]; ok {
			next[u] = p
		}
	}
	if len(todo) > 0 {
		node, err := s.npmNode(ctx, st)
		if err != nil {
			return
		}
		for _, u := range todo {
			res, err := s.web(ctx, node, protocol.WebRequest{Op: protocol.WebOpCheckURL, Check: &protocol.WebCheck{URL: u}})
			p := Probe{At: now}
			switch {
			case err != nil && res.Check == nil:
				p.Error = trimText(err.Error(), 160)
			case res.Check != nil:
				p.Status, p.Title, p.Error = res.Check.Status, trimText(res.Check.Title, 120), trimText(res.Check.Error, 160)
			}
			next[u] = p
		}
	}
	s.saveRead(ctx, "probes", next, nil)
}

// titleFits zegt of de titel van een pagina bij de naam van de host past.
func titleFits(title, domain, service string) bool {
	t := norm(title)
	if t == "" {
		return false
	}
	label := strings.ToLower(domain)
	if i := strings.Index(label, "."); i > 0 {
		label = label[:i]
	}
	for _, w := range []string{norm(label), norm(service)} {
		if len(w) >= 3 && (strings.Contains(t, w) || len(t) >= 4 && strings.Contains(w, t)) {
			return true
		}
	}
	return false
}

// DeadNet is een net waar alle doelen van hosts niet meer antwoorden.
type DeadNet struct {
	Prefix string   `json:"prefix"`
	Hosts  []int    `json:"hosts"`
	Names  []string `json:"names"`
}

// net24 geeft 192.168.0.x voor een IPv4-adres; leeg voor een naam.
func net24(host string) string {
	ip := net.ParseIP(host).To4()
	if ip == nil {
		return ""
	}
	return fmt.Sprintf("%d.%d.%d.x", ip[0], ip[1], ip[2])
}

// deadNets groepeert de hosts met een dood doel per /24. Een net telt als
// verdwenen als er minstens drie hosts naar wijzen, geen enkele node van
// ClusterForge erin zit en niets in dat net antwoordt.
func deadNets(hosts []HostView, nodes []nodeRow) []DeadNet {
	live := map[string]bool{}
	for _, n := range nodes {
		if p := net24(n.PrimaryIP); p != "" {
			live[p] = true
		}
	}
	by := map[string]*DeadNet{}
	alive := map[string]bool{}
	for _, h := range hosts {
		p := net24(h.ForwardHost)
		if p == "" || !h.Enabled {
			continue
		}
		if !h.Cleanup {
			if h.ServiceKey != "" || h.TargetAlive {
				alive[p] = true
			}
			continue
		}
		d := by[p]
		if d == nil {
			d = &DeadNet{Prefix: p}
			by[p] = d
		}
		d.Hosts = append(d.Hosts, h.ID)
		if len(h.Domains) > 0 {
			d.Names = append(d.Names, h.Domains[0])
		}
	}
	var out []DeadNet
	for p, d := range by {
		if len(d.Hosts) < 3 || live[p] || alive[p] {
			continue
		}
		sort.Strings(d.Names)
		out = append(out, *d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Prefix < out[j].Prefix })
	return out
}
