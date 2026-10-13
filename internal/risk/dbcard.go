package risk

import (
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/status"
)

// Een database zonder geslaagde failovertest in deze tijd is niet bewezen;
// een verhuisde database die na deze tijd nog op het oude cluster staat, is
// vergeten.
const (
	dbTestStale   = 90 * day
	dbUnretiredAt = 30 * day
	// dbMemoryP95 is het geheugengebruik (p95 over 7 dagen) waarboven een
	// node met een database te weinig vrij heeft.
	dbMemoryP95 = 85.0
)

// DatabaseCardInput is wat de kaart Database van een cluster nodig heeft:
// zijn onderdelen Database, en wat ClusterForge leest van de databaseservers
// op zijn nodes.
type DatabaseCardInput struct {
	Components []DBComponentInput
	// Shared zijn de databases op de servers van dit cluster die meer dan
	// één cluster, losse node of onbekend adres gebruikt.
	Shared []SharedDBInput
	// Moved zijn de verhuisde databases die nog op de servers van dit
	// cluster staan.
	Moved []MovedDBInput
}

// DBComponentInput is een onderdeel Database.
type DBComponentInput struct {
	ID     uuid.UUID
	Title  string
	Engine string
	// Nodes zijn de datanodes, Arbiters de scheidsrechters.
	Nodes    []uuid.UUID
	Arbiters []uuid.UUID
	// VIP is het adres waarlangs de app de database vindt; leeg zonder VIP.
	VIP string
	// Health is wat de bewaking laatst zag; nil als dat te oud is.
	Health *status.DBHealth
	// BackupOn: de nachtelijke dumps staan aan.
	BackupOn bool
	Dumps    []DBDumpInput
	// LastTest is de laatste geslaagde failovertest met een schrijftest via
	// het VIP; nil als er nog geen was.
	LastTest *time.Time
}

// DBDumpInput is de nieuwste goede dump van één database.
type DBDumpInput struct {
	Database string
	// Latest is nil zonder goede dump; Stale is true als hij ouder is dan
	// een dag, of als er geen is.
	Latest *time.Time
	Stale  bool
	// NoPrimaryKey en NotInnoDB zijn de tabellen volgens die dump, alleen
	// bij Galera; nil als het onbekend is.
	NoPrimaryKey []string
	NotInnoDB    []string
}

// SharedDBInput is een database die meer dan één cluster gebruikt.
type SharedDBInput struct {
	Engine, Database string
	// Users zijn de clusters, losse nodes en adressen.
	Users []string
}

// MovedDBInput is een verhuisde database die nog op de oude server staat.
type MovedDBInput struct {
	ID         uuid.UUID
	Engine     string
	Database   string
	SourceKey  string
	To         string
	SwitchedAt time.Time
}

// --- Database ---

func databaseCard(in ClusterInput, now time.Time) part {
	var p part
	d := in.DB
	eb := envBump(in.Env)
	link := clusterLink(in.ID)
	if len(d.Components) == 0 {
		p.note = "ClusterForge beheert de databases van dit cluster niet; de kaart kijkt alleen naar gedeelde en verhuisde databases."
	}
	nodes := map[uuid.UUID]NodeInput{}
	for _, n := range in.Nodes {
		nodes[n.ID] = n
	}
	hostname := func(id uuid.UUID) string {
		if n, ok := nodes[id]; ok {
			return n.Hostname
		}
		return id.String()[:8]
	}
	for _, c := range d.Components {
		cs := Subject{"component", c.ID, c.Title}
		var data []string
		for _, id := range c.Nodes {
			data = append(data, hostname(id))
		}

		switch {
		case len(c.Nodes) == 1:
			p.add("database.no_ha", bump(Medium, eb, High), cs,
				fmt.Sprintf("%s draait op één node (%s): valt die uit, dan is de database weg tot hij terug is. Met drie nodes mag er één uitvallen",
					c.Title, data[0]), link)
		case len(c.Nodes) == 2 && len(c.Arbiters) == 0:
			p.add("database.no_ha", bump(Medium, eb, High), cs,
				fmt.Sprintf("%s heeft twee nodes zonder scheidsrechter: valt %s of %s uit, dan heeft de andere geen meerderheid en stopt de database. "+
					"Voeg een derde node of een scheidsrechter toe", c.Title, data[0], data[1]), link)
		}

		if h := c.Health; h != nil && h.Status == status.Degraded {
			reason := h.Reason
			if !strings.HasPrefix(reason, c.Title) {
				reason = c.Title + ": " + reason
			}
			p.add("database.degraded", bump(Medium, eb, High), cs, reason, link)
		}

		dbSameHost(&p, in, c, cs, nodes, eb)

		for _, id := range c.Nodes {
			n, ok := nodes[id]
			if !ok || n.Metrics == nil || n.Metrics.MemoryP95 == nil || *n.Metrics.MemoryP95 <= dbMemoryP95 {
				continue
			}
			p.add("database.memory", Medium, nodeSubject(n),
				fmt.Sprintf("Op %s was de afgelopen 7 dagen vaak minder dan 15 %% geheugen vrij (p95 %.0f %%). De database deelt het met de app; "+
					"raakt het op, dan stopt de kernel meestal de database", n.Hostname, *n.Metrics.MemoryP95), nodeLink(n.ID))
		}

		if !c.BackupOn {
			p.add("database.dumps_off", bump(Medium, eb, High), cs,
				"De nachtelijke dumps van "+c.Title+" staan uit: zonder dump is er niets om één database terug te zetten", link)
		} else {
			var stale []string
			for _, dp := range c.Dumps {
				switch {
				case dp.Latest == nil:
					stale = append(stale, dp.Database+" (nog geen)")
				case dp.Stale:
					stale = append(stale, dp.Database+" ("+days(now.Sub(*dp.Latest))+")")
				}
			}
			if len(stale) > 0 {
				of := c.Title
				if len(stale) > 1 {
					of = fmt.Sprintf("%d databases van %s", len(stale), c.Title)
				}
				p.add("database.dump_stale", bump(Medium, eb, High), cs,
					fmt.Sprintf("Van %s is de nieuwste goede dump ouder dan een dag: %s", of, names(stale, 4)), "/back-ups")
			}
		}

		var noPK, other []string
		for _, dp := range c.Dumps {
			for _, t := range dp.NoPrimaryKey {
				noPK = append(noPK, dp.Database+"."+t)
			}
			for _, t := range dp.NotInnoDB {
				other = append(other, dp.Database+"."+t)
			}
		}
		if len(other) > 0 {
			p.add("database.not_innodb", bump(Medium, eb, High), cs,
				fmt.Sprintf("%s niet in InnoDB: %s. Galera zet alleen InnoDB op de andere nodes, dus de rest staat alleen op de node waar hij geschreven werd. "+
					"Zet ze om met ALTER TABLE … ENGINE=InnoDB", plural(len(other), "tabel staat", "tabellen staan"), names(other, 4)), link)
		}
		if len(noPK) > 0 {
			text := fmt.Sprintf("%s zonder primaire sleutel: %s. Een UPDATE of DELETE erop is traag op de andere nodes van Galera, "+
				"want die zoeken de rij in de hele tabel", plural(len(noPK), "tabel", "tabellen"), names(noPK, 4))
			if slices.ContainsFunc(noPK, func(t string) bool { return strings.Contains(t, "nextcloud") }) {
				text += ". Nextcloud voegt ze zelf toe met occ db:add-missing-primary-keys"
			}
			p.add("database.no_primary_key", Low, cs, text, link)
		}

		if c.VIP != "" {
			switch {
			case c.LastTest == nil:
				p.add("database.no_test", bump(Medium, eb, High), cs,
					c.Title+" had nog geen geslaagde failovertest: of de app na een uitval weer kan schrijven via "+c.VIP+", is niet bewezen", "/tests")
			case now.Sub(*c.LastTest) > dbTestStale:
				p.add("database.no_test", bump(Medium, eb, High), cs,
					fmt.Sprintf("De laatste geslaagde failovertest van %s is %s geleden; test minstens elke 90 dagen of schrijven via %s na een uitval nog lukt",
						c.Title, days(now.Sub(*c.LastTest)), c.VIP), "/tests")
			}
		}
	}

	for _, sh := range d.Shared {
		p.add("database.shared", Low, Subject{"database", sharedID(in.ID, sh), sh.Database},
			fmt.Sprintf("%s wordt gebruikt door %s: een fout of zware query van de ene raakt de andere, en verhuizen kan alleen voor allemaal tegelijk",
				sh.Database, names(sh.Users, 4)), "/databases")
	}
	for _, mv := range d.Moved {
		age := now.Sub(mv.SwitchedAt)
		if age < dbUnretiredAt {
			continue
		}
		q := url.Values{"source": {mv.SourceKey}, "db": {mv.Database}}
		p.add("database.not_retired", Low, Subject{"db_migration", mv.ID, mv.Database},
			fmt.Sprintf("%s is %s geleden naar %s verhuisd en staat nog op slot op %s: bouw de oude database af",
				mv.Database, days(age), mv.To, in.Name), "/databases/verhuizen?"+q.Encode())
	}
	return p
}

// dbSameHost meldt datanodes van één database op dezelfde Proxmox-host.
// Kost die host de meerderheid, dan is het zwaarder.
func dbSameHost(p *part, in ClusterInput, c DBComponentInput, cs Subject, nodes map[uuid.UUID]NodeInput, eb int) {
	byHost := map[string][]string{}
	var hosts []string
	for _, id := range c.Nodes {
		n, ok := nodes[id]
		if !ok || n.Host == "" {
			continue
		}
		if byHost[n.Host] == nil {
			hosts = append(hosts, n.Host)
		}
		byHost[n.Host] = append(byHost[n.Host], n.Hostname)
	}
	members := len(c.Nodes) + len(c.Arbiters)
	for _, h := range hosts {
		on := byHost[h]
		if len(on) < 2 {
			continue
		}
		if 2*len(on) >= members {
			p.add("database.same_host", bump(Medium, eb, High), cs,
				fmt.Sprintf("%s van %s draaien op %s: valt %s uit, dan is er geen meerderheid meer en stopt de database. Zet ze op verschillende hosts",
					list(on), c.Title, h, h), clusterLink(in.ID))
			continue
		}
		p.add("database.same_host", Low, cs,
			fmt.Sprintf("%s van %s draaien op %s: valt %s uit, dan vallen er %d tegelijk weg", list(on), c.Title, h, h, len(on)), clusterLink(in.ID))
	}
}

// sharedID is een vast id voor een gedeelde database, om te accepteren.
func sharedID(cluster uuid.UUID, sh SharedDBInput) uuid.UUID {
	return uuid.NewSHA1(spofNamespace, []byte("shared-db:"+cluster.String()+":"+sh.Engine+":"+sh.Database))
}
