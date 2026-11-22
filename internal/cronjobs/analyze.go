package cronjobs

import (
	"regexp"
	"slices"
	"strings"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// Niveaus van een waarschuwing bij een cronjob.
const (
	// LevelDanger: de job kan schade doen naast wat ClusterForge doet.
	LevelDanger = "danger"
	// LevelWarning: de job doet iets wat ClusterForge ook doet.
	LevelWarning = "warning"
	// LevelInfo: ClusterForge kan het beter of overzichtelijker.
	LevelInfo = "info"
)

// Warning is een waarschuwing bij een job.
type Warning struct {
	// Kind is galera_bootstrap, db_restart, pg_failover of rsync.
	Kind  string
	Level string
	Text  string
}

// Variant is dezelfde job (zelfde bestand en gebruiker) die op een andere
// node anders staat.
type Variant struct {
	Hostname string
	Schedule string
	Command  string
}

// clusterContext is wat de waarschuwingen over een cluster moeten weten.
type clusterContext struct {
	// components zijn de engines met een onderdeel Database van
	// ClusterForge.
	components map[string]bool
}

var (
	galeraBootstrap = regexp.MustCompile(`(?i)galera_new_cluster|wsrep[-_]new[-_]cluster|safe_to_bootstrap|galera[^\s]*(?:bootstrap|recover|start)|bootstrap[^\s]*galera`)
	dbRestart       = regexp.MustCompile(`(?i)(?:systemctl|service)\s+(?:[^|;&]*\s)?(?:start|restart|reload-or-restart|try-restart)\s+(?:[^|;&]*\s)?(mariadb|mysql|mysqld|postgresql\S*|patroni)\b|(?:service)\s+(mariadb|mysql|postgresql)\s+(?:start|restart)`)
	pgFailover      = regexp.MustCompile(`(?i)pg_ctl(?:cluster)?\b[^|;&]*\bpromote|pg_promote|repmgr|pg_is_in_recovery`)
	rsyncRemote     = regexp.MustCompile(`\brsync\b.*(?:\S+@)?[A-Za-z0-9][A-Za-z0-9.-]*:\S*`)
)

// warnings geeft de waarschuwingen bij één job in een cluster.
func warnings(j Job, cc clusterContext) []Warning {
	// Een job die op geen enkele node aanstaat, doet niets meer.
	if j.Package != "" || j.ClusterForge || !slices.ContainsFunc(j.Nodes, func(n JobNode) bool { return n.Enabled }) {
		return nil
	}
	var out []Warning
	cmd := j.Command
	switch {
	case galeraBootstrap.MatchString(cmd) && cc.components[protocol.DBMariaDB]:
		out = append(out, Warning{Kind: "galera_bootstrap", Level: LevelDanger,
			Text: "Het onderdeel Database van ClusterForge start Galera zelf opnieuw na een uitval, op de node met de nieuwste data. " +
				"Deze cronjob kan daarnaast een tweede cluster starten. Haal hem weg."})
	case galeraBootstrap.MatchString(cmd):
		out = append(out, Warning{Kind: "galera_bootstrap", Level: LevelWarning,
			Text: "Een cronjob die Galera opnieuw start, kan bij een netwerkstoring twee clusters maken die elk verder schrijven. " +
				"Het onderdeel Database van ClusterForge doet dit veilig: alleen als alle nodes antwoorden, op de node met de nieuwste data."})
	}
	if m := dbRestart.FindStringSubmatch(cmd); m != nil {
		unit := strings.ToLower(m[1] + m[2])
		engine := protocol.DBMariaDB
		if strings.HasPrefix(unit, "postgresql") || unit == "patroni" {
			engine = protocol.DBPostgreSQL
		}
		if cc.components[engine] {
			out = append(out, Warning{Kind: "db_restart", Level: LevelWarning,
				Text: "ClusterForge bewaakt " + engineName(engine) + " op dit cluster en start de database zelf opnieuw; deze cronjob kan dat doorkruisen, bijvoorbeeld tijdens een failovertest of onderhoud."})
		}
	}
	if pgFailover.MatchString(cmd) && cc.components[protocol.DBPostgreSQL] {
		out = append(out, Warning{Kind: "pg_failover", Level: LevelDanger,
			Text: "Patroni kiest de primary en ClusterForge laat het VIP hem volgen. Een script dat zelf een primary zoekt of promote, kan twee primaries maken. Haal hem weg."})
	}
	if rsyncRemote.MatchString(cmd) {
		out = append(out, Warning{Kind: "rsync", Level: LevelInfo,
			Text: "Dit is een bestandssync. ClusterForge kan hem straks als beheerde sync draaien: alleen vanaf de node met het VIP, met een overzicht per run."})
	}
	return out
}

func engineName(engine string) string {
	if engine == protocol.DBPostgreSQL {
		return "PostgreSQL"
	}
	return "MariaDB"
}

// variantKey is wat dezelfde job op een andere node gemeen heeft, ook als
// het schema of commando verschilt.
func variantKey(j Job) string { return j.Source + "\x00" + j.File + "\x00" + j.User }

// analyze vult de verschillen en waarschuwingen van de jobs in een groep.
func analyze(g *Group, cc clusterContext) {
	byVariant := map[string][]int{}
	for i, j := range g.Jobs {
		// Een periodiek script heeft geen schema per node, en een timer
		// heet overal hetzelfde: beide vergelijkt de sleutel al.
		if j.Source == protocol.CronSourcePeriodic {
			continue
		}
		k := variantKey(j)
		byVariant[k] = append(byVariant[k], i)
	}
	for _, idx := range byVariant {
		if len(idx) < 2 {
			continue
		}
		for _, i := range idx {
			for _, o := range idx {
				if o == i {
					continue
				}
				for _, n := range g.Jobs[o].Nodes {
					if slices.ContainsFunc(g.Jobs[i].Nodes, func(x JobNode) bool { return x.NodeID == n.NodeID }) {
						// Twee regels in hetzelfde bestand op dezelfde node
						// zijn geen afwijking.
						continue
					}
					g.Jobs[i].Differs = append(g.Jobs[i].Differs, Variant{Hostname: n.Hostname, Schedule: g.Jobs[o].Schedule, Command: g.Jobs[o].Command})
				}
			}
		}
	}
	for i := range g.Jobs {
		g.Jobs[i].Warnings = warnings(g.Jobs[i], cc)
	}
}

func contexts(components []engineRow) map[uuid.UUID]clusterContext {
	out := map[uuid.UUID]clusterContext{}
	get := func(id uuid.UUID) clusterContext {
		cc, ok := out[id]
		if !ok {
			cc = clusterContext{components: map[string]bool{}}
			out[id] = cc
		}
		return cc
	}
	for _, c := range components {
		get(c.cluster).components[c.engine] = true
	}
	return out
}

type engineRow struct {
	cluster uuid.UUID
	engine  string
}
