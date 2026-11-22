package publish

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// TargetSuggestion is een voorstel voor het nieuwe doel van een host die
// naar een oud of onbekend adres wijst (fase 42).
type TargetSuggestion struct {
	Scheme      string `json:"forward_scheme"`
	Host        string `json:"forward_host"`
	Port        int    `json:"forward_port"`
	ServiceKey  string `json:"service_key"`
	ServiceName string `json:"service_name"`
	ClusterName string `json:"cluster_name,omitempty"`
	VIP         bool   `json:"vip"`
	// Why zegt waarop het voorstel steunt (naam, poort, ...).
	Why string `json:"why"`
	// Sure: één duidelijke kandidaat. Anders is het een gok.
	Sure bool `json:"sure"`
}

// norm houdt alleen letters en cijfers over.
func norm(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// serviceWords zijn de namen waaronder een dienst bekend is.
func serviceWords(r ServiceRow) []string {
	_, img := imageLast(r.Image)
	var out []string
	for _, w := range []string{r.Label, r.Container, img, strings.TrimSuffix(r.Unit, ".service"), r.Title, r.Name} {
		if n := norm(w); len(n) >= 3 {
			out = append(out, n)
		}
	}
	return out
}

// suggestTarget zoekt de dienst die bij een host hoort: eerst op naam
// (subdomein tegen container, image, unit, titel), dan op poort. Het doel is
// het VIP als de dienst op een cluster draait.
func suggestTarget(domain string, oldPort int, services []ServiceRow) *TargetSuggestion {
	label := strings.ToLower(domain)
	if i := strings.Index(label, "."); i > 0 {
		label = label[:i]
	}
	l := norm(label)
	type cand struct {
		i     int
		score int
		why   string
	}
	var best []cand
	add := func(i, score int, why string) {
		switch {
		case len(best) == 0 || score > best[0].score:
			best = []cand{{i, score, why}}
		case score == best[0].score:
			best = append(best, cand{i, score, why})
		}
	}
	for i, r := range services {
		if r.Proto != "tcp" || !r.Web || r.Target == "" {
			continue
		}
		score, why := 0, ""
		for _, w := range serviceWords(r) {
			switch {
			case w == l:
				score, why = max(score, 100), "naam "+label
			case len(l) >= 4 && len(w) >= 4 && (strings.Contains(w, l) || strings.Contains(l, w)) && score < 60:
				score, why = 60, "naam lijkt op "+label
			}
		}
		if score == 0 && oldPort > 0 && r.Port == oldPort && !r.Internal {
			score, why = 30, fmt.Sprintf("zelfde poort %d", oldPort)
		}
		if score == 0 {
			continue
		}
		// Bij gelijke naam: de echte dienst voor de interne poort, het VIP
		// voor één node, en de oude poort.
		if r.Internal {
			score -= 20
		}
		if r.TargetVIP {
			score += 5
		}
		if r.Port == oldPort {
			score += 3
		}
		if r.Kind == "https" {
			score++
		}
		add(i, score, why)
	}
	if len(best) == 0 {
		return nil
	}
	r := services[best[0].i]
	sure := len(best) == 1 && best[0].score >= 60
	why := best[0].why
	if len(best) > 1 {
		why += fmt.Sprintf(" (%d kandidaten, de eerste gekozen)", len(best))
	}
	return &TargetSuggestion{
		Scheme: r.Kind, Host: r.Target, Port: r.Port, ServiceKey: r.Key, ServiceName: r.Name, ClusterName: r.ClusterName,
		VIP: r.TargetVIP, Why: why, Sure: sure,
	}
}

// AdvFix is een adres in de eigen Advanced van een host (proxy_pass) dat
// ClusterForge niet kent, met het voorstel om het te vervangen (fase 42c).
type AdvFix struct {
	Old string `json:"old"`
	New string `json:"new"`
	Why string `json:"why"`
}

var proxyPassRe = regexp.MustCompile(`proxy_pass\s+(https?)://(\d{1,3}(?:\.\d{1,3}){3})(?::(\d{1,5}))?([^\s;]*)`)

// advancedFixes zoekt vaste adressen in proxy_pass van Advanced die bij
// geen bekende dienst horen. De outpost van Authentik wordt de outpost uit
// de instellingen; een ander adres de dienst met dezelfde naam.
func advancedFixes(adv, domain string, idx map[string]int, services []ServiceRow, outpost string) []AdvFix {
	var out []AdvFix
	seen := map[string]bool{}
	for _, m := range proxyPassRe.FindAllStringSubmatch(adv, -1) {
		scheme, ip, portS, path := m[1], m[2], m[3], m[4]
		port := 80
		if scheme == "https" {
			port = 443
		}
		if portS != "" {
			port, _ = strconv.Atoi(portS)
		}
		old := scheme + "://" + ip
		if portS != "" {
			old += ":" + portS
		}
		if seen[old] {
			continue
		}
		seen[old] = true
		if _, ok := idx[ip+":"+strconv.Itoa(port)]; ok {
			continue
		}
		if strings.Contains(path, "outpost.goauthentik.io") {
			if outpost != "" && outpost != old {
				out = append(out, AdvFix{Old: old, New: outpost, Why: "outpost van Authentik uit de instellingen"})
			}
			continue
		}
		if sg := suggestTarget(domain, port, services); sg != nil && sg.Sure {
			out = append(out, AdvFix{Old: old, New: fmt.Sprintf("%s://%s:%d", sg.Scheme, sg.Host, sg.Port), Why: sg.ServiceName + ", " + sg.Why})
		}
	}
	return out
}

// applyAdvFixes vervangt de oude adressen in proxy_pass.
func applyAdvFixes(adv string, fixes []AdvFix) string {
	for _, f := range fixes {
		re := regexp.MustCompile(`(proxy_pass\s+)` + regexp.QuoteMeta(f.Old) + `([/;\s])`)
		adv = re.ReplaceAllString(adv, "${1}"+strings.ReplaceAll(f.New, "$", "$$")+"${2}")
	}
	return adv
}
