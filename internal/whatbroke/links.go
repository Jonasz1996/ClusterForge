package whatbroke

import (
	"regexp"
	"strings"
)

// Vaste verbanden tussen een verandering en een dienst die faalt. Ze
// werken op namen, dus ze zijn een vermoeden: de analyse zegt erbij
// waarom.

// aliases zijn namen die bij elkaar horen: het pakket heet anders dan de
// unit of het proces.
var aliases = [][]string{
	{"mysql", "mariadb", "mysqld", "mariadbd"},
	{"apache2", "httpd", "apache"},
	{"ssh", "sshd", "openssh-server", "openssh"},
	{"postgresql", "postgres"},
	{"redis", "redis-server"},
	{"docker", "dockerd", "docker.io", "docker-ce", "containerd"},
	{"bind9", "named"},
	{"ntp", "ntpd", "chrony", "chronyd"},
	{"cron", "crond"},
	{"systemd-networkd", "networking", "ifupdown"},
}

// unitBase is de naam van een unit zonder .service en zonder instantie:
// "nginx.service" en "php8.2-fpm@www.service" worden "nginx" en
// "php8.2-fpm".
func unitBase(u string) string {
	u = strings.ToLower(strings.TrimSpace(u))
	u = strings.TrimSuffix(u, ".service")
	if i := strings.IndexByte(u, '@'); i > 0 {
		u = u[:i]
	}
	return u
}

// packageNames splitst het onderwerp van een pakketregel: ClusterForge zet
// meer pakketten in één regel, en dpkg zet er soms de architectuur achter.
func packageNames(subject string) []string {
	var out []string
	for _, p := range strings.Split(subject, ",") {
		p = strings.ToLower(strings.TrimSpace(p))
		if i := strings.IndexByte(p, ':'); i > 0 {
			p = p[:i]
		}
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// sameName is true als twee namen dezelfde software zijn, ook via een alias.
func sameName(a, b string) bool {
	if a == b {
		return true
	}
	for _, group := range aliases {
		if contains(group, a) && contains(group, b) {
			return true
		}
	}
	return false
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// packageMatches is true als het pakket bij de unit hoort: dezelfde naam,
// een alias, of een deel ervan zoals nginx-core, nginx-common of
// postgresql-16 bij nginx en postgresql.
func packageMatches(pkg, unit string) bool {
	u := unitBase(unit)
	if u == "" || pkg == "" {
		return false
	}
	if sameName(pkg, u) {
		return true
	}
	for _, name := range append([]string{u}, aliasesOf(u)...) {
		if len(name) >= 3 && strings.HasPrefix(pkg, name+"-") {
			return true
		}
	}
	return false
}

func aliasesOf(name string) []string {
	for _, group := range aliases {
		if contains(group, name) {
			return group
		}
	}
	return nil
}

// fileMatches is true als het pad bij de unit hoort: een map of bestand
// met de naam van de unit, zoals /etc/nginx/sites-enabled/default,
// /etc/haproxy/haproxy.cfg of /etc/systemd/system/nginx.service.
func fileMatches(path, unit string) bool {
	u := unitBase(unit)
	if u == "" {
		return false
	}
	names := append([]string{u}, aliasesOf(u)...)
	for _, seg := range strings.Split(strings.ToLower(path), "/") {
		if seg == "" {
			continue
		}
		base := seg
		if i := strings.IndexByte(base, '.'); i > 0 {
			base = base[:i]
		}
		for _, n := range names {
			if seg == n || base == n {
				return true
			}
		}
	}
	return false
}

// processMatches is true als het proces van een OOM-kill de unit is:
// dezelfde naam of een alias, of de een begint met de ander zoals mysqld en
// mysql of postgres en postgresql.
func processMatches(process, unit string) bool {
	p, u := strings.ToLower(strings.TrimSpace(process)), unitBase(unit)
	if p == "" || u == "" {
		return false
	}
	if sameName(p, u) {
		return true
	}
	if len(p) >= 4 && len(u) >= 4 && (strings.HasPrefix(p, u) || strings.HasPrefix(u, p)) {
		return true
	}
	return false
}

// failingUnit haalt de unit uit de reden van een node die verminderd is:
// "service nginx is gefaald; schijf / is 93 % vol".
var failingUnit = regexp.MustCompile(`service (\S+) (?:is gefaald|draait niet)`)

func unitsFromReason(reason string) []string {
	var out []string
	for _, m := range failingUnit.FindAllStringSubmatch(reason, -1) {
		out = append(out, unitBase(m[1]))
	}
	return out
}

// diskFromReason is true als de reden een volle schijf noemt.
func diskFromReason(reason string) bool {
	return strings.Contains(reason, "schijf ") && strings.Contains(reason, "vol")
}
