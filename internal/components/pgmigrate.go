package components

// Wat het plan van een verhuizing alleen bij PostgreSQL bekijkt: de
// eigenaar van de database, wat die mag lezen, pg_hba.conf van de oude
// server, de extensies en versies, en de rollen die meegaan.

import (
	"fmt"
	"net/netip"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/Jonasz1996/clusterforge/internal/agent/dbsql"
	"github.com/Jonasz1996/clusterforge/internal/databases"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

var (
	pgMajorRe = regexp.MustCompile(`^(\d+)`)
	// pgPlainIdentRe is een naam die psql niet tussen aanhalingstekens
	// nodig heeft, zodat het plan er een commando mee kan maken.
	pgPlainIdentRe = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,62}$`)
)

// checkPGSource bekijkt de database op de oude PostgreSQL: de tijdelijke
// gebruiker leest als lid van de eigenaar, dus die mag geen superuser zijn
// en moet alles kunnen lezen.
func checkPGSource(info *protocol.DBMigrateInfo, database string, sn *Node, add func(string, string, *Node, string, string)) {
	if info.OwnerSuper {
		add(Blocked, "owner_super", sn, fmt.Sprintf("de eigenaar van %s is %s, en dat is een superuser. ClusterForge leest een database van PostgreSQL "+
			"met een tijdelijke gebruiker die lid is van de eigenaar, en maakt nooit een gebruiker met de rechten van een superuser. Maak de "+
			"gebruiker van de app eigenaar van de database en haar tabellen, of verhuis haar zelf met pg_dump", database, info.Owner), "")
	}
	if len(info.Unreadable) > 0 && !info.OwnerSuper {
		var lines []string
		for _, name := range info.Unreadable {
			schema, table, ok := strings.Cut(name, ".")
			if !ok || !pgPlainIdentRe.MatchString(schema) || !pgPlainIdentRe.MatchString(table) {
				lines = nil
				break
			}
			if g := fmt.Sprintf("GRANT USAGE ON SCHEMA %s TO %s;", schema, dbsql.PGQuoteIdent(info.Owner)); !slices.Contains(lines, g) {
				lines = append(lines, g)
			}
			lines = append(lines, fmt.Sprintf("GRANT SELECT ON %s.%s TO %s;", schema, table, dbsql.PGQuoteIdent(info.Owner)))
		}
		command := ""
		if len(lines) > 0 {
			command = fmt.Sprintf("sudo -u postgres psql -X -d %s <<'SQL'\n%s\nSQL", database, strings.Join(lines, "\n"))
		}
		add(Blocked, "unreadable", sn, fmt.Sprintf("%s, de eigenaar van %s, mag niet alles in de database lezen: %s. De tijdelijke gebruiker leest "+
			"als lid van de eigenaar, dus de dump zou stoppen. Geef de eigenaar die rechten op de oude server", info.Owner, database,
			listSome(info.Unreadable, 8)), command)
	}
	if info.Settings > 0 {
		add(Info, "settings", sn, fmt.Sprintf("op %s %s met ALTER DATABASE of ALTER ROLE ... IN DATABASE, zoals een search_path. Een dump neemt "+
			"die niet mee; zet ze na de verhuizing zelf op het nieuwe cluster", database,
			plural(info.Settings, "staat één instelling", "instellingen staan")), "")
	}
}

// checkPGReach bekijkt wat de nieuwe node op het adres van de oude
// PostgreSQL zag: of pg_hba.conf de tijdelijke gebruiker binnenlaat, TLS,
// en of de extensies van de database op de nieuwe node te installeren
// zijn.
func checkPGReach(p *MigrationPlan, info *protocol.DBMigrateInfo, ip string, sn Node, tried []string, add func(string, string, *Node, string, string)) {
	r, t, src := p.Reach, p.Target, p.Source
	if src.Via == ViaVIP {
		add(Info, "via_vip", &t.Node, fmt.Sprintf("%s bereikt %s niet rechtstreeks (%s), maar het VIP %s wel. Staat daar HAProxy voor, dan moet "+
			"die naar de primary wijzen", t.Node.Hostname, ip, strings.Join(tried, "; "), src.Address), "")
	}
	switch r.Code {
	case 0:
		if !r.TLS {
			add(Info, "no_tls", &sn, fmt.Sprintf("PostgreSQL op %s kan geen TLS, dus de dump gaat onversleuteld over het netwerk naar %s. "+
				"In je eigen netwerk is dat meestal goed", src.Address, t.Node.Hostname), "")
		}
	case protocol.DBMigrateNoEntry:
		seen := r.Seen
		bits := 32
		if a, err := netip.ParseAddr(seen); err == nil && a.Is6() && !a.Is4In6() {
			bits = 128
		}
		msg := fmt.Sprintf("pg_hba.conf van PostgreSQL op %s laat de tijdelijke gebruiker vanaf %s niet in %s. Zet deze regel in pg_hba.conf op "+
			"elke server van %s en laad de configuratie opnieuw, zoals hieronder; beheert Patroni pg_hba.conf, zet de regel dan met "+
			"patronictl edit-config bij postgresql.pg_hba. Haal hem na de verhuizing weer weg", src.Address, seen, p.Database, src.Name)
		if seen != "" && !slices.Contains(t.Hosts, seen) {
			msg += fmt.Sprintf(". De oude server ziet %s als %s, niet als een van zijn eigen adressen (%s); staat er NAT of een proxy tussen?",
				t.Node.Hostname, seen, joinHosts(t.Hosts))
		}
		command := ""
		if seen != "" {
			command = fmt.Sprintf("echo \"host %s all %s/%d scram-sha-256\" | sudo tee -a \"$(sudo -u postgres psql -XAtc 'SHOW hba_file')\"\n"+
				"sudo -u postgres psql -Xc 'SELECT pg_reload_conf()'", p.Database, seen, bits)
		}
		add(Blocked, "pg_hba", &sn, msg, command)
	default:
		add(Blocked, "refused", &sn, fmt.Sprintf("PostgreSQL op %s weigert %s: %s", src.Address, t.Node.Hostname, r.Error), "")
	}
	if r.Extensions == nil {
		return
	}
	var missing []string
	for _, e := range info.Extensions {
		if !slices.Contains(r.Extensions, e) {
			missing = append(missing, e)
		}
	}
	if len(missing) > 0 {
		add(Blocked, "extensions", &t.Node, fmt.Sprintf("%s gebruikt %s, maar op %s %s niet te installeren. Installeer het pakket op elke node "+
			"van %s; de dump maakt de extensie dan zelf", p.Database, plural(len(missing), "de extensie "+missing[0], "de extensies "+
			strings.Join(missing, ", ")), t.Node.Hostname, plural(len(missing), "is die", "zijn die"), t.ClusterName), "")
	}
}

// pgVersionMajor is de hoofdversie van PostgreSQL, zoals 17 van 17.6 (Debian ...).
func pgVersionMajor(v string) (int, bool) {
	m := pgMajorRe.FindStringSubmatch(v)
	if m == nil {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	return n, err == nil
}

// checkPGVersions: pg_dump op de nieuwe node leest geen nieuwere server.
func checkPGVersions(source, target string, sn, tn Node, add func(string, string, *Node, string, string)) {
	s, ok1 := pgVersionMajor(source)
	t, ok2 := pgVersionMajor(target)
	switch {
	case !ok1 || !ok2:
	case t < s:
		add(Blocked, "downgrade", &tn, fmt.Sprintf("de nieuwe PostgreSQL (%d op %s) is ouder dan de oude (%d op %s), en pg_dump van een oudere versie "+
			"leest geen nieuwere server. Neem nodes met Debian 13 (PostgreSQL 17)", t, tn.Hostname, s, sn.Hostname), "")
	case t > s:
		add(Info, "upgrade", nil, fmt.Sprintf("van PostgreSQL %d naar %d: een dump gaat goed in een nieuwere versie", s, t), "")
	}
}

// checkPGAccounts zegt welke rollen van PostgreSQL niet meegaan. Een rol
// die iets in de database bezit of er rechten op heeft, moet mee, anders
// stopt de import bij zijn ALTER ... OWNER TO of GRANT.
func checkPGAccounts(accounts []protocol.DBMigrateAccount, clients []databases.User, database string, add func(string, string, *Node, string, string)) {
	var md5, commands, blocked, other, noSecret, global []string
	users := map[string]bool{}
	for _, a := range accounts {
		if slices.Contains(dbsql.PGSystemUsers, a.User) || strings.HasPrefix(a.User, protocol.DBMigrateUserPrefix) {
			continue
		}
		needed := slices.Contains(a.Privileges, dbsql.PGOwner) || slices.Contains(a.Privileges, dbsql.PGObjects)
		err := dbsql.CheckPGAccount(a)
		switch {
		case a.Plugin == dbsql.PGLoginMD5 && !a.Super && dbsql.CheckPGUser(a.User) == nil:
			md5 = append(md5, a.User)
			commands = append(commands, fmt.Sprintf(`sudo -u postgres psql -X -c "SET password_encryption = 'scram-sha-256'" -c "\password %s"`, a.User))
		case err != nil && needed:
			blocked = append(blocked, err.Error())
		case err != nil:
			other = append(other, err.Error())
		default:
			users[a.User] = true
			if a.Plugin == dbsql.PGLoginNoSecret {
				noSecret = append(noSecret, a.User)
			}
			if a.Global {
				global = append(global, a.User)
			}
		}
	}
	if len(md5) > 0 {
		add(Blocked, "accounts_md5", nil, fmt.Sprintf("het wachtwoord van %s staat als md5-hash op de oude server, en het nieuwe cluster laat alleen "+
			"scram-sha-256 toe, dus de app zou er niet mee inloggen. Zet het wachtwoord op de oude server opnieuw met scram-sha-256, met "+
			"hetzelfde wachtwoord als in de app; de app merkt daar niets van", joinHosts(md5)), strings.Join(commands, "\n"))
	}
	if len(blocked) > 0 {
		add(Blocked, "accounts_blocked", nil, fmt.Sprintf("deze rollen bezitten iets in %s of hebben er rechten op, maar gaan niet mee, dus de "+
			"import zou stoppen: %s", database, strings.Join(blocked, "; ")), "")
	}
	if len(other) > 0 {
		add(Warning, "accounts_other", nil, fmt.Sprintf("deze rollen gaan niet mee: %s", strings.Join(other, "; ")), "")
	}
	if len(noSecret) > 0 {
		add(Warning, "accounts_no_password", nil, fmt.Sprintf("%s logt op de oude server in zonder wachtwoord. Op het nieuwe cluster vraagt "+
			"PostgreSQL altijd een wachtwoord; geef %s er een bij Databases en gebruikers", joinHosts(noSecret),
			plural(len(noSecret), "hem", "elk")), "")
	}
	if len(global) > 0 {
		add(Info, "accounts_global", nil, fmt.Sprintf("%s mag op de oude server ook databases of rollen maken, of replicatie. Op het nieuwe cluster "+
			"krijgt %s alleen de rechten in %s", joinHosts(global), plural(len(global), "hij", "elk"), database), "")
	}
	var missing []string
	for _, c := range clients {
		for _, u := range c.DBUsers {
			if !users[u] && !strings.HasPrefix(u, protocol.DBMigrateUserPrefix) && !slices.Contains(dbsql.PGSystemUsers, u) &&
				!slices.Contains(missing, u) && !slices.Contains(md5, u) {
				missing = append(missing, u)
			}
		}
	}
	if len(missing) > 0 {
		add(Warning, "accounts_missing", nil, fmt.Sprintf("%s gebruikte %s, maar heeft er niets in: geen eigenaar, geen rechten op de database of "+
			"haar objecten, en geen lid van een rol die dat heeft. Die gaat niet mee", joinHosts(missing), database), "")
	}
}

// checkPGExisting zegt welke rollen al op het nieuwe cluster inloggen: de
// verhuizing geeft ze het wachtwoord van de oude server, zodat de app
// inlogt, maar wie ze daar nu met een ander wachtwoord gebruikt, niet meer.
func checkPGExisting(accounts []protocol.DBMigrateAccount, existing []protocol.DBUser, cluster string, add func(string, string, *Node, string, string)) {
	var users []string
	for _, a := range accounts {
		if a.Plugin == dbsql.PGLoginSCRAM && Movable(protocol.DBPostgreSQL, a) &&
			slices.ContainsFunc(existing, func(u protocol.DBUser) bool { return u.Name == a.User }) {
			users = append(users, a.User)
		}
	}
	if len(users) > 0 {
		add(Warning, "accounts_existing", nil, fmt.Sprintf("%s %s al op %s en %s bij de verhuizing het wachtwoord van de oude server. Gebruikt "+
			"iets op %s %s nu met een ander wachtwoord, zet dat daar dan op het wachtwoord van de oude server", joinHosts(users),
			plural(len(users), "staat", "staan"), cluster, plural(len(users), "krijgt", "krijgen"), cluster, plural(len(users), "hem", "ze")), "")
	}
}
