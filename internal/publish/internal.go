package publish

import (
	"strings"
)

// Interne diensten (beheerpoorten, frontends van HAProxy, REST-API's van
// clusters, ...) blijven op de dienstenkaart en worden verder beheerd, maar
// staan standaard niet bij de te publiceren diensten en worden geen tegel
// op de homepage. Een interne dienst die toch al in NPM staat, blijft
// gewoon zichtbaar.

type internalRule struct {
	// names: unit, container of laatste deel van de image begint hiermee.
	names []string
	// ports: alleen deze poorten (leeg = alle poorten).
	ports []int
	why   string
}

var internalRules = []internalRule{
	{names: []string{"haproxy"}, why: "HAProxy (frontends en statistieken), beheerd door ClusterForge"},
	{names: []string{"patroni"}, why: "REST-API van Patroni"},
	{names: []string{"spiceproxy"}, why: "SPICE-proxy van Proxmox"},
	{names: []string{"tailscaled", "tailscale"}, why: "poort van Tailscale"},
	{names: []string{"seaweedfs", "weed"}, why: "interne poort van SeaweedFS"},
	{names: []string{"crowdsec"}, why: "LAPI of metrics van CrowdSec"},
	{names: []string{"keepalived"}, why: "Keepalived"},
	{names: []string{"etcd"}, why: "etcd"},
	{names: []string{"node_exporter", "node-exporter", "prometheus-node-exporter"}, why: "metrics"},
	{names: []string{"nginx-proxy-manager"}, ports: []int{80, 443}, why: "NPM zelf (poort 80/443)"},
	{names: []string{"rustdesk-server", "hbbs", "hbbr", "cf-rustdesk-hbbs", "cf-rustdesk-hbbr"}, why: "RustDesk ID/relay (rechtstreeks via NAT, niet via NPM)"},
	{names: []string{"server", "authentik"}, ports: []int{9300}, why: "metrics van Authentik"},
}

// internalReason zegt waarom een dienst intern is ("" = gewone dienst).
func internalReason(r ServiceRow) string {
	if strings.Contains(strings.ToLower(r.Title), "statistics report for haproxy") {
		return internalRules[0].why
	}
	_, img := imageLast(r.Image)
	cands := []string{strings.ToLower(strings.TrimSuffix(r.Unit, ".service")), strings.ToLower(r.Container), img}
	for _, rule := range internalRules {
		if len(rule.ports) > 0 && !containsInt(rule.ports, r.Port) {
			continue
		}
		for _, c := range cands {
			if c == "" {
				continue
			}
			for _, n := range rule.names {
				if c == n || strings.HasPrefix(c, n+"-") || strings.HasPrefix(c, n+"_") || strings.HasPrefix(c, n+"@") ||
					(len(n) > 4 && strings.HasPrefix(c, n)) {
					// Authentik-metrics alleen bij de image van Authentik.
					if n == "server" && !strings.Contains(strings.ToLower(r.Image), "goauthentik") {
						continue
					}
					return rule.why
				}
			}
		}
	}
	return ""
}

// imageLast geeft repo en laatste deel van een image zonder tag of digest.
func imageLast(image string) (string, string) {
	image = strings.ToLower(image)
	if i := strings.Index(image, "@"); i >= 0 {
		image = image[:i]
	}
	if i := strings.LastIndex(image, ":"); i > strings.LastIndex(image, "/") {
		image = image[:i]
	}
	last := image
	if i := strings.LastIndex(image, "/"); i >= 0 {
		last = image[i+1:]
	}
	return image, last
}

// domainTypo: valt de naam onder een domein dat op één of twee tekens na
// het hoofddomein is (zoals bogaert.be voor jbogaert.be)? Geeft dat domein
// en de verbeterde naam terug.
func domainTypo(name, domain string) (string, string, bool) {
	name, domain = strings.ToLower(strings.TrimSuffix(name, ".")), strings.ToLower(domain)
	if domain == "" || inDomain(name, domain) {
		return "", "", false
	}
	labels := strings.Split(name, ".")
	n := len(strings.Split(domain, "."))
	if len(labels) < n {
		return "", "", false
	}
	tail := strings.Join(labels[len(labels)-n:], ".")
	if tail == domain || levenshtein(tail, domain) > 2 {
		return "", "", false
	}
	// Zelfde topdomein, anders is het gewoon een ander domein.
	if labels[len(labels)-1] != domain[strings.LastIndex(domain, ".")+1:] {
		return "", "", false
	}
	fixed := strings.Join(append(append([]string{}, labels[:len(labels)-n]...), domain), ".")
	return tail, fixed, true
}

func levenshtein(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur := make([]int, len(rb)+1)
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(rb)]
}
