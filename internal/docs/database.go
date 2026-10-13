package docs

import (
	"strconv"
	"strings"
)

// dumpDir is waar cf-agent de dumps van de databases zet.
const dumpDir = "/var/backups/clusterforge-db"

// Engines zoals ClusterForge ze noemt.
const (
	engineMariaDB    = "mariadb"
	enginePostgreSQL = "postgresql"
)

// patroniCtl is patronictl met het wachtwoord van de REST-API, dat alleen
// root leest.
const patroniCtl = "sudo sh -c 'set -a; . /etc/patroni/clusterforge.env; patronictl -c /etc/patroni/clusterforge.yml "

// database is het hoofdstuk Database: wat er op de nodes van het cluster
// draait, en runbooks om opnieuw te starten, een node opnieuw te
// synchroniseren, een dump terug te zetten, over te schakelen en te
// verhuizen.
func database(c *Cluster, _ Opts) string {
	d := &doc{}
	header(d, c, KindDatabase)
	db := c.DB
	if db == nil || len(db.Components)+len(db.Servers) == 0 {
		d.p("ClusterForge ziet geen database op de nodes van " + esc(c.Name) + ".")
		return d.String()
	}
	var managed, read []string
	for _, cp := range db.Components {
		managed = append(managed, esc(cp.Title)+" op "+hosts(cp.Data))
	}
	for _, s := range db.Servers {
		read = append(read, engineName(s.Engine)+" op "+hosts(s.Nodes))
	}
	intro := []string{}
	if len(managed) > 0 {
		intro = append(intro, esc(c.Name)+" heeft "+list(managed)+", uitgerold en beheerd door ClusterForge.")
	}
	if len(read) > 0 {
		there, them := "Er draait ", "die server, maar beheert hem"
		if len(managed) > 0 {
			there = "Daarnaast draait er "
		}
		if len(read) > 1 {
			them = "die servers, maar beheert ze"
		}
		intro = append(intro, there+list(read)+"; ClusterForge leest "+them+" niet.")
	}
	d.p(append(intro,
		"Dit hoofdstuk beschrijft wat ClusterForge bij de laatste ronde zag (het leest de databaseservers elke minuut), zonder wachtwoorden en zonder de inhoud van tabellen.",
		"Hoe het er nu voor staat, zie je in ClusterForge op de kaart Database van het cluster en bij Databases.")...)

	for _, cp := range db.Components {
		dbComponent(d, c, cp)
	}
	for _, s := range db.Servers {
		dbServers(d, s)
	}

	d.h2("Een database verhuizen")
	if len(db.Components) > 0 {
		var titles []string
		mariadb := false
		for _, cp := range db.Components {
			titles = append(titles, esc(cp.Title))
			mariadb = mariadb || cp.Engine == engineMariaDB
		}
		addr := "het adres van dat onderdeel"
		if cp := db.Components[0]; len(db.Components) == 1 {
			addr = "een datanode (" + strings.Replace(hosts(cp.Data), " en ", " of ", 1) + ") op poort " + strconv.Itoa(cp.Port)
			if cp.VIP != "" {
				addr = code(cp.VIP)
			}
		}
		block := "toont wat in de weg staat"
		if mariadb {
			block += ", zoals tabellen zonder primaire sleutel of buiten InnoDB"
		}
		target := titles[0]
		if len(titles) > 1 {
			target = "het onderdeel met dezelfde engine: " + list(titles)
		}
		d.p("Staat de database van een app van dit cluster nog op een andere server, zoals een gedeeld databasecluster, verhuis haar dan naar " + target + ".")
		d.steps([]string{
			"Kies in ClusterForge bij Databases **Verhuizen…** naast de database. Het plan verandert niets en " + block + ".",
			"**Proefverhuizing starten** zet een kopie naast de bestaande en telt de rijen; de app merkt er niets van. Zo weet je hoe lang de app straks in onderhoud moet.",
			"Zet de app in onderhoud en kies **Verhuizing starten**. De database komt onder dezelfde naam, met dezelfde gebruikers en wachtwoorden, op " + esc(c.Name) + ".",
			"Wijs de app naar " + addr + ", met dezelfde gebruiker en hetzelfde wachtwoord, en haal het onderhoud eraf. ClusterForge zet de oude database dan op slot: alleen nog lezen.",
			"Zeven dagen nadat niemand de oude database nog gebruikte, kies je **Oude database afbouwen**: een laatste dump op het oude cluster, en daarna gaan de database en de gebruikers die niets anders hebben weg.",
		})
	}
	if len(db.Servers) > 0 {
		d.p("De databases op de server die ClusterForge alleen leest, verhuis je zo naar een database op de nodes van hun app: bij Databases, **Verhuizen…** naast de database.",
			"Na het verhuizen staan ze hier op slot tot je ze afbouwt; bovenaan Databases staat wat er nog op deze server staat, en welke firewallregel weg kan.")
	}
	return d.String()
}

// dbComponent beschrijft een onderdeel Database, met zijn runbooks.
func dbComponent(d *doc, c *Cluster, cp DBComponent) {
	d.h2(esc(cp.Title))
	pg := cp.Engine == enginePostgreSQL
	nodes := hosts(cp.Data)
	if len(cp.Arbiters) > 0 {
		nodes += "; scheidsrechter " + hosts(cp.Arbiters)
		if pg {
			nodes += " (alleen etcd)"
		} else {
			nodes += " (alleen garbd)"
		}
	}
	addr := "elke datanode op poort " + strconv.Itoa(cp.Port) + "; zonder VIP kiest de app zelf een node"
	if cp.VIP != "" {
		addr = code(cp.VIP+":"+strconv.Itoa(cp.Port)) + ", via het VIP"
		switch {
		case cp.OwnVIP && pg:
			addr += ", dat alleen op de primary staat"
		case cp.OwnVIP:
			addr += ", dat alleen op een node staat die mag schrijven"
		case pg:
			addr += " van de app, dat alleen op de primary staat"
		default:
			addr += " van de app, dat alleen op een node staat die mag schrijven"
		}
	}
	version := ""
	if cp.Version != "" {
		version = engineName(cp.Engine) + " " + code(cp.Version)
	}
	name := "Galera-cluster"
	if pg {
		name = "Patroni-cluster (scope)"
	}
	dumps := "staan uit"
	if cp.BackupOn {
		where := "op een datanode die Synced is"
		switch {
		case pg:
			where = "op de primary"
		case cp.VIP != "":
			where = "op een datanode zonder het VIP"
		}
		dumps = "elke nacht om " + esc(cp.BackupAt) + ", " + plural(cp.KeepDays, "dag", "dagen") + " bewaard, in " + code(dumpDir) + " " + where
	}
	rows := [][2]string{
		{"Versie", version},
		{"Nodes", nodes},
		{"Adres voor de app", addr},
		{name, code(c.Slug)},
		{"Dumps", dumps},
	}
	if !pg {
		recover := "ClusterForge begint het cluster vanzelf opnieuw op de node met de nieuwste data"
		if !cp.AutoRecover {
			recover = "ClusterForge begint het cluster niet vanzelf opnieuw; dat staat uit op de kaart Database"
		}
		rows = append(rows, [2]string{"Na een volledige uitval", recover})
	}
	d.facts(rows)

	d.h3("Databases")
	dbTable(d, cp.Databases, true)

	example := "<database>"
	for _, x := range cp.Databases {
		if !strings.Contains(x.Name, "_herstel_") {
			example = x.Name
			break
		}
	}
	if pg {
		patroniRunbooks(d, c, cp, example)
	} else {
		galeraRunbooks(d, c, cp, example)
	}
}

func galeraRunbooks(d *doc, c *Cluster, cp DBComponent, db string) {
	all := hosts(cp.Data)
	d.h3("Opnieuw starten na een volledige uitval")
	auto := "Kies op de kaart Database **Opnieuw starten**; ClusterForge toont per node zijn seqno en begint het cluster op de node met de nieuwste data."
	if cp.AutoRecover {
		auto = "ClusterForge kijkt om de 20 seconden of de database draait, en begint het cluster vanzelf opnieuw op de node met de nieuwste data zodra alle datanodes antwoorden. Met de hand kies je op de kaart Database **Opnieuw starten**."
	}
	d.p("Valt één node uit, dan draait de database verder op de andere, en sluit hij bij het opstarten vanzelf weer aan.",
		"Na een gewone stop van alle nodes, zoals een herstart van de Proxmox-host, begint Galera niet vanzelf: elke node wacht op een cluster dat er niet is.",
		auto, "Kan ClusterForge er niet bij, doe het dan zo, als root:")
	d.p("**1.** Kijk op " + all + " tot welke transactie elke node de data heeft. Staat er `seqno: -1`, dan stopte MariaDB niet netjes; " +
		code("galera_recovery") + " haalt de positie dan uit InnoDB, alleen als MariaDB stilstaat.")
	d.code("sh", "cat /var/lib/mysql/grastate.dat\ngalera_recovery")
	d.p("**2.** Begin het cluster op de node met de hoogste seqno, en alleen daar: twee nodes die elk een cluster beginnen, geven twee clusters met dezelfde naam.")
	d.code("sh", "sed -i 's/^safe_to_bootstrap:.*/safe_to_bootstrap: 1/' /var/lib/mysql/grastate.dat\ngalera_new_cluster")
	d.p("**3.** Start MariaDB op de andere nodes, één voor één; elke node haalt wat hij mist bij de eerste.")
	d.code("sh", "systemctl start mariadb")
	members := len(cp.Data) + len(cp.Arbiters)
	check := "**4.** Kijk of alle nodes meedoen: " + code("wsrep_cluster_size") + " is " + strconv.Itoa(members) + "."
	if len(cp.Arbiters) > 0 {
		check += " De scheidsrechter " + hosts(cp.Arbiters) + " telt mee; start daar zo nodig " + code("systemctl restart garb") + "."
	}
	d.p(check)
	d.code("sh", "mariadb -e \"SHOW STATUS LIKE 'wsrep_cluster_size'\"")

	d.h3("Een node opnieuw synchroniseren")
	d.p("Voor een node die niet meer bijkomt, of waarvan de data niet klopt: kies op de kaart Database **Beheer…** en dan **Opnieuw synchroniseren** bij die node.",
		"De node haalt dan alle data opnieuw bij een node die Synced is (SST met "+code("mariabackup")+"); de andere nodes werken door.",
		"Doe het nooit op de enige node die Synced is. Met de hand, op die node:")
	d.code("sh", "systemctl stop mariadb\nrm -f /var/lib/mysql/grastate.dat\nsystemctl start mariadb\nmariadb -e \"SHOW STATUS LIKE 'wsrep_local_state_comment'\"   # Synced")

	dumpRunbook(d, cp, db)

	d.h3("Overschakelen")
	if cp.VIP == "" {
		d.p("Zonder VIP kiest de app zelf met welke node ze verbindt; zet in de app een andere node om over te schakelen.")
		return
	}
	d.p("De app schrijft naar de node met het VIP " + code(cp.VIP) + ". Om die node vrij te maken, zet je hem in ClusterForge in **Onderhoud** of kies je **Herstarten** bij de node:" +
		" keepalived stopt dan op die node, en het VIP gaat binnen enkele seconden naar een andere node die mag schrijven. Daarna wacht ClusterForge tot de node weer Synced is.")
	d.p("Met de hand, op de node met het VIP:")
	d.code("sh", "systemctl stop keepalived     # het VIP gaat naar een andere node\nsystemctl start keepalived    # na het onderhoud")
}

func patroniRunbooks(d *doc, c *Cluster, cp DBComponent, db string) {
	d.h3("Opnieuw starten na een volledige uitval")
	where := hosts(cp.Data)
	if len(cp.Arbiters) > 0 {
		where += ", en naar etcd op de scheidsrechter " + hosts(cp.Arbiters)
	}
	d.p("Patroni en etcd starten vanzelf bij het opstarten van de nodes. Zodra etcd een meerderheid heeft, kiest Patroni een primary uit de node met de nieuwste data, en volgen de andere als replica.",
		"Er is geen node die het cluster moet beginnen, zoals bij Galera; ClusterForge doet hier niets.",
		"Komt het niet terug, kijk dan als root op "+where+":")
	d.code("sh", "systemctl status etcd patroni\n"+patroniCtl+"list'")
	d.p("Heeft etcd geen meerderheid, start dan de nodes die uit staan. Zonder meerderheid zet Patroni PostgreSQL op alleen lezen, zodat er nooit twee primaries zijn.")

	d.h3("Een replica opnieuw opbouwen")
	d.p("Voor een replica die niet meer bijkomt, zoals na een volle schijf: kies op de kaart Database **Beheer…** en dan **Opnieuw opbouwen** bij die node.",
		"PostgreSQL gooit daar zijn data weg en kopieert alles opnieuw van de primary ("+code("pg_basebackup")+"). Op de primary kan het niet. Met de hand, op een node van het cluster:")
	example := "<node>"
	if len(cp.Data) > 1 {
		example = cp.Data[len(cp.Data)-1]
	}
	d.code("sh", patroniCtl+"reinit "+c.Slug+" "+example+" --force'")

	dumpRunbook(d, cp, db)

	d.h3("Overschakelen")
	d.p("Kies op de kaart Database **Beheer…** en dan **Primary maken** bij de synchrone replica: Patroni stopt de schrijvers op de primary, wacht tot de replica alles heeft en maakt haar de primary.",
		"De app kan een paar seconden niet schrijven; het VIP gaat mee. Onderhoud of een herstart van de primary in ClusterForge schakelt eerst zelf over. Met de hand:")
	candidate := "<synchrone replica>"
	if len(cp.Data) > 1 {
		candidate = cp.Data[1]
	}
	d.code("sh", patroniCtl+"list'   # wie is Sync Standby\n"+patroniCtl+"switchover "+c.Slug+" --candidate "+candidate+" --force'")
}

// dumpRunbook is het runbook om een dump terug te zetten.
func dumpRunbook(d *doc, cp DBComponent, db string) {
	pg := cp.Engine == enginePostgreSQL
	d.h3("Een dump terugzetten")
	d.p("Kies op de kaart Database **Databases en gebruikers**, kaart Dumps, **Terugzetten** bij een dump.",
		"ClusterForge zet hem in een nieuwe database naast de bestaande, "+code(db+"_herstel_<datum>")+", zodat de app er niets van merkt en je kunt vergelijken.",
		"Over de bestaande database heen terugzetten kan ClusterForge nog niet. Met de hand gaat het zo, met de app in onderhoud en na **Dump nu**:")
	dir := dumpDir + "/" + db
	if pg {
		d.p("Op de primary, want alleen die schrijft. Staat de dump na een failover op een andere node, kopieer hem dan eerst met " + code("scp") + ".")
		d.code("sh", "ls -l "+dir+"/\n"+
			"zstd -dc "+dir+"/<bestand>.pgsql.zst | tail -n 3   # -- PostgreSQL database dump complete\n"+
			"runuser -u postgres -- createdb -O <eigenaar> "+db+"_terug\n"+
			"zstd -dc "+dir+"/<bestand>.pgsql.zst | runuser -u postgres -- psql -X -v ON_ERROR_STOP=1 "+db+"_terug")
		d.p("Wijs de app daarna naar " + code(db+"_terug") + ", of hernoem de databases met " + code("ALTER DATABASE … RENAME TO …") + " terwijl niemand verbonden is.")
		return
	}
	d.p("Op de node met de dump; Galera zet de database zelf op de andere nodes.")
	d.code("sh", "ls -l "+dir+"/\n"+
		"zstd -dc "+dir+"/<bestand>.sql.zst | tail -n 1   # -- Dump completed\n"+
		"zstd -dc "+dir+"/<bestand>.sql.zst | mariadb "+db)
	d.p("Elke tabel uit de dump vervangt die in de database; een tabel die na de dump bijkwam, blijft staan.")
}

// dbServers beschrijft databaseservers die ClusterForge alleen leest.
func dbServers(d *doc, s DBServers) {
	d.h2(engineName(s.Engine) + " op " + hosts(s.Nodes))
	version := ""
	if s.Version != "" {
		version = engineName(s.Engine) + " " + code(s.Version)
	}
	d.facts([][2]string{
		{"Versie", version},
		{"Soort", kindText[s.Kind]},
		{"Nodes", hosts(s.Nodes)},
	})
	d.p("ClusterForge rolde deze server niet uit: het leest hem elke minuut, maar start, synchroniseert of dumpt hem niet.")
	d.h3("Databases")
	dbTable(d, s.Databases, false)
}

var kindText = map[string]string{
	"galera": "Galera-cluster", "patroni": "Patroni", "replication": "primary met replica's", "single": "losse server",
}

// dbTable is de tabel van de databases.
func dbTable(d *doc, dbs []DBDatabase, dumps bool) {
	if len(dbs) == 0 {
		d.p("Er staat nog geen database van een app op.")
		return
	}
	head := []string{"Database", "Grootte", "Tabellen", "Gebruikt door"}
	if dumps {
		head = append(head, "Dump")
	}
	var rows [][]string
	for _, x := range dbs {
		sz := ""
		if x.SizeBytes > 0 {
			sz = roughSize(x.SizeBytes)
		}
		tables := ""
		if x.Tables > 0 {
			tables = strconv.Itoa(x.Tables)
		}
		users := make([]string, len(x.Users))
		for i, u := range x.Users {
			users[i] = esc(u)
		}
		row := []string{"**" + esc(x.Name) + "**", sz, tables, strings.Join(users, "; ")}
		if dumps {
			row = append(row, dumpText[x.Dump])
		}
		rows = append(rows, row)
	}
	d.table(head, rows)
	text := "Gebruikt door is wie de laatste dertig dagen met de database verbond, met de gebruiker in de database."
	if dumps {
		text += " Een goede dump is er een van de laatste 26 uur, compleet en met alle tabellen."
	}
	d.p(text)
}

var dumpText = map[string]string{"ok": "goed", "stale": "te oud", "missing": "nog geen", "off": "uit"}

// roughSize is een grootte met twee cijfers, zodat een database die wat
// groeit niet elke nacht een nieuwe versie van het hoofdstuk geeft.
func roughSize(b int64) string {
	unit, div := "kB", int64(1)<<10
	switch {
	case b >= 1<<40:
		unit, div = "TB", 1<<40
	case b >= 1<<30:
		unit, div = "GB", 1<<30
	case b >= 1<<20:
		unit, div = "MB", 1<<20
	}
	f := float64(b) / float64(div)
	switch {
	case f >= 100:
		return strconv.FormatInt(int64(f/10+0.5)*10, 10) + " " + unit
	case f >= 10:
		return strconv.FormatInt(int64(f+0.5), 10) + " " + unit
	}
	return decimal(f, 1) + " " + unit
}

func engineName(engine string) string {
	switch engine {
	case engineMariaDB:
		return "MariaDB"
	case enginePostgreSQL:
		return "PostgreSQL"
	}
	return esc(engine)
}

func hosts(names []string) string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = esc(n)
	}
	return list(out)
}
