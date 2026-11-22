// Package deps houdt bij welke diensten er draaien en welke dienst van welke
// andere afhangt: per cluster, op losse nodes en extern. Diensten komen uit
// templates, uit handmatige invoer en als voorstel uit de facts van de
// agents. De status van een dienst volgt uit de heartbeats en gaat mee langs
// de pijlen; de graaf beslist of blokkeert nooit iets.
package deps

import (
	"regexp"
	"slices"
)

// Kinds zijn de soorten diensten, in de volgorde van de keuzelijst. Een vaste
// lijst in Go, zoals de clustertypes, zodat een nieuwe soort geen migratie
// vraagt.
var Kinds = []string{"web", "lb", "vip", "database", "cache", "queue", "storage", "dns", "cron", "container", "app", "external", "other"}

// KnownKind is true voor een soort uit Kinds.
func KnownKind(k string) bool { return slices.Contains(Kinds, k) }

// Bronnen en staten van diensten en afhankelijkheden.
const (
	SourceTemplate   = "template"
	SourceManual     = "manual"
	SourceDiscovered = "discovered"

	StateConfirmed = "confirmed"
	StateSuggested = "suggested"
	StateIgnored   = "ignored"

	Hard = "hard"
	Soft = "soft"
)

var (
	// nameRe is hetzelfde als de CHECK in de migratie: hoogstens 63 tekens
	// uit letters, cijfers en @ . _ : / -.
	nameRe = regexp.MustCompile(`^[A-Za-z0-9@._:/-]{1,63}$`)
	// unitRe is een systemd-unit zonder .service.
	unitRe = regexp.MustCompile(`^[A-Za-z0-9@._:-]{1,63}$`)
)

// knownUnits maakt van een unit die een agent meldt een soort. Alleen deze
// units worden voorgesteld; al het andere, zoals ssh, cf-agent,
// qemu-guest-agent, containerd, corosync, pve*, ceph*, chrony en systemd-*,
// is infrastructuur en slaat de resolver over.
var knownUnits = map[string]string{
	"nginx": "web", "apache2": "web",
	"haproxy":    "lb",
	"keepalived": "vip",
	"mariadb":    "database", "mysql": "database", "postgresql": "database",
	"redis-server": "cache", "memcached": "cache",
	"rabbitmq-server": "queue",
	"docker":          "container",
	"cron":            "cron",
}

var (
	// postgresInstance is een PostgreSQL-cluster op Debian of Ubuntu, zoals
	// postgresql@16-main. postgresql.service zelf staat daar altijd active.
	postgresInstance = regexp.MustCompile(`^postgresql@[0-9]+(\.[0-9]+)?-main$`)
	phpFPM           = regexp.MustCompile(`^php[0-9]+\.[0-9]+-fpm$`)
)

// unitKind geeft de soort van een bekende unit. cron telt alleen in een
// cluster van type cron: op andere servers draait het altijd mee.
func unitKind(unit, clusterType string) (string, bool) {
	switch {
	case unit == "cron":
		return "cron", clusterType == "cron"
	case postgresInstance.MatchString(unit):
		return "database", true
	case phpFPM.MatchString(unit):
		return "app", true
	}
	k, ok := knownUnits[unit]
	return k, ok
}
