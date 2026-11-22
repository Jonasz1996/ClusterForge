package upkeep

import (
	"fmt"
	"strconv"
	"strings"
)

// Modes van een beurt.
const (
	ModeRun     = "run"
	ModePreview = "preview"
	ModeUrgent  = "urgent"
)

// NodeEnv is wat de server over de node weet en het script nodig heeft.
type NodeEnv struct {
	// GaleraExpect is het aantal Galera-nodes dat het cluster hoort te
	// hebben; 0 als de server er geen kent. Het script doet niets aan de
	// database als Galera kleiner is.
	GaleraExpect int
	// Degraded: een andere node van het cluster is niet verbonden. Dan
	// doet het script geen zwaar databasewerk (OPTIMIZE, REINDEX).
	Degraded bool
}

// title is de naam van een onderdeel in gewone taal.
var titles = map[string]string{
	"journal":      "Systeemlogboek (journal) inkorten",
	"apt":          "Gedownloade updates (apt-cache) wissen",
	"logrotate":    "Logbestanden roteren (logrotate)",
	"oldlogs":      "Oude, geroteerde logbestanden wissen",
	"coredumps":    "Crashbestanden (core dumps) wissen",
	"tmp":          "Oude tijdelijke bestanden wissen",
	"docker":       "Ongebruikte Docker-images wissen",
	"docker_cache": "Docker-bouwcache wissen",
	"docker_logs":  "Te grote containerlogs inkorten",
}

// Title geeft de naam van een onderdeel.
func Title(id string) string {
	if t, ok := titles[id]; ok {
		return t
	}
	return id
}

const header = `set -u
export LC_ALL=C
MODE=@MODE@
DEGRADED=@DEGRADED@
GALERA_EXPECT=@GALERA@
TAB="$(printf '\t')"
if [ "$(uname -s)" != Linux ]; then
  echo "Dit is geen Linux; onderhoud overgeslagen."
  printf 'CFUPKEEP\tskip\tgeen Linux\n'
  exit 0
fi
used() { df -l -B1 --output=used -x tmpfs -x devtmpfs -x squashfs -x overlay -x efivarfs 2>/dev/null | tail -n +2 | awk '{s+=$1} END {print s+0}'; }
fsum() { awk '{s+=$1} END {print s+0}'; }
sz() { du -sxb "$@" 2>/dev/null | awk '{s+=$1} END {print s+0}'; }
item() { printf 'CFUPKEEP\titem\t%s\t%s\t%s\n' "$1" "$2" "$3"; }
size() { printf 'CFUPKEEP\tsize\t%s\t%s\n' "$1" "$2"; }
dbline() { printf 'CFUPKEEP\tdb\t%s\t%s\t%s\t%s\t%s\n' "$1" "$2" "$3" "$4" "$5"; }
step() { if [ "$MODE" = preview ]; then echo "Bekijken: $1"; else echo "Bezig: $1"; fi; }
printf 'CFUPKEEP\tused\tbefore\t%s\n' "$(used)"
rc=0
`

const journalPart = `step "systeemlogboek"
if command -v journalctl >/dev/null 2>&1; then
  s=$(sz /var/log/journal /run/log/journal); r=$((s - @JOURNALMB@ * 1048576)); [ "$r" -lt 0 ] && r=0
  size journal "$r"
  if [ "$MODE" != preview ]; then
    if journalctl --vacuum-size=@JOURNALMB@M >/dev/null 2>&1; then item journal ok ""; else item journal fail "journalctl gaf een fout"; fi
  fi
else
  item journal skip "geen journald op deze node"
fi
`

const aptPart = `step "apt-cache"
if command -v apt-get >/dev/null 2>&1; then
  size apt "$(find /var/cache/apt -xdev -type f -name '*.deb' -printf '%s\n' 2>/dev/null | fsum)"
  if [ "$MODE" != preview ]; then
    if apt-get clean >/dev/null 2>&1; then item apt ok ""; else item apt fail "apt-get clean gaf een fout (loopt er een andere apt?)"; fi
  fi
else
  item apt skip "geen apt op deze node"
fi
`

const logrotatePart = `step "logrotate"
if command -v logrotate >/dev/null 2>&1 && [ -f /etc/logrotate.conf ]; then
  size logrotate 0
  if [ "$MODE" != preview ]; then
    if logrotate /etc/logrotate.conf >/dev/null 2>&1; then item logrotate ok ""; else item logrotate fail "logrotate gaf een fout"; fi
  fi
else
  item logrotate skip "geen logrotate op deze node"
fi
`

const oldLogsPart = `step "oude logbestanden"
size oldlogs "$(find /var/log -xdev -type f \( -name '*.gz' -o -name '*.[0-9]' -o -name '*.[0-9].log' -o -name '*.old' -o -name '*.xz' \) -mtime +@OLDDAYS@ -printf '%s\n' 2>/dev/null | fsum)"
if [ "$MODE" != preview ]; then
  if find /var/log -xdev -type f \( -name '*.gz' -o -name '*.[0-9]' -o -name '*.[0-9].log' -o -name '*.old' -o -name '*.xz' \) -mtime +@OLDDAYS@ -delete 2>/dev/null; then item oldlogs ok ""; else item oldlogs fail "niet alles kon weg"; fi
fi
`

const coredumpsPart = `step "crashbestanden"
if [ -d /var/lib/systemd/coredump ]; then
  size coredumps "$(sz /var/lib/systemd/coredump)"
  if [ "$MODE" != preview ]; then
    if find /var/lib/systemd/coredump -type f -delete 2>/dev/null; then item coredumps ok ""; else item coredumps fail "niet alles kon weg"; fi
  fi
else
  size coredumps 0
  [ "$MODE" != preview ] && item coredumps ok "geen crashbestanden"
fi
`

const tmpPart = `step "tijdelijke bestanden"
size tmp "$(find /tmp /var/tmp -xdev -type f -atime +@TMPDAYS@ -printf '%s\n' 2>/dev/null | fsum)"
if [ "$MODE" != preview ]; then
  if find /tmp /var/tmp -xdev -type f -atime +@TMPDAYS@ -delete 2>/dev/null; then item tmp ok ""; else item tmp fail "niet alles kon weg"; fi
fi
`

const dockerHead = `DOCKER=0
if command -v docker >/dev/null 2>&1 && docker info >/dev/null 2>&1; then DOCKER=1; fi
dockerdf() { docker system df --format '{{.Type}}	{{.Reclaimable}}' 2>/dev/null | awk -F '	' -v t="$1" '$1 == t {print $2}' | head -n 1; }
`

const dockerImagesPart = `step "Docker-images"
if [ "$DOCKER" = 1 ]; then
  printf 'CFUPKEEP\tdsize\tdocker\t%s\n' "$(dockerdf Images)"
  if [ "$MODE" != preview ]; then
    if docker image prune -af --filter until=@IMGHOURS@h >/dev/null 2>&1; then item docker ok "images die geen container gebruikt en ouder dan @IMGDAYS@ dagen"; else item docker fail "docker image prune gaf een fout"; fi
  fi
else
  item docker skip "geen Docker op deze node"
fi
`

const dockerCachePart = `step "Docker-bouwcache"
if [ "$DOCKER" = 1 ]; then
  printf 'CFUPKEEP\tdsize\tdocker_cache\t%s\n' "$(dockerdf 'Build Cache')"
  if [ "$MODE" != preview ]; then
    if docker builder prune -af --filter until=@IMGHOURS@h >/dev/null 2>&1; then item docker_cache ok ""; else item docker_cache fail "docker builder prune gaf een fout"; fi
  fi
else
  item docker_cache skip "geen Docker op deze node"
fi
`

const dockerLogsPart = `step "containerlogs"
if [ -d /var/lib/docker/containers ]; then
  size docker_logs "$(find /var/lib/docker/containers -xdev -type f -name '*-json.log' -size +@LOGSMB@M -printf '%s\n' 2>/dev/null | fsum)"
  if [ "$MODE" != preview ]; then
    n=0
    for f in $(find /var/lib/docker/containers -xdev -type f -name '*-json.log' -size +@LOGSMB@M 2>/dev/null); do
      : > "$f" && n=$((n + 1))
    done
    item docker_logs ok "$n logbestanden ingekort"
  fi
else
  item docker_logs skip "geen Docker op deze node"
fi
`

// mariadbPart onderhoudt MariaDB of MySQL. ANALYZE en OPTIMIZE met
// NO_WRITE_TO_BINLOG gaan niet naar de andere Galera-nodes: elke node doet
// zijn eigen beurt, na elkaar. OPTIMIZE op Galera loopt met RSU: de node
// gaat even uit de pas (HAProxy haalt hem uit de pool) en haalt daarna in;
// het script wacht tot hij weer Synced is.
const mariadbPart = `MY=$(command -v mariadb || command -v mysql || true)
if [ -n "$MY" ] && { pgrep -x mariadbd >/dev/null 2>&1 || pgrep -x mysqld >/dev/null 2>&1; }; then
  step "MariaDB"
  q() { "$MY" -N -B -e "$1" 2>/dev/null; }
  if ! q 'SELECT 1' >/dev/null; then
    dbline mariadb unknown skip 0 "MariaDB draait, maar root kan niet inloggen via de socket"
  else
    role=standalone
    wsrep=$(q "SHOW GLOBAL STATUS LIKE 'wsrep_cluster_size'" | awk '{print $2}')
    okdb=1
    if [ -n "$wsrep" ] && [ "$wsrep" -gt 0 ] 2>/dev/null; then
      role=galera
      st=$(q "SHOW GLOBAL STATUS LIKE 'wsrep_local_state'" | awk '{print $2}')
      cs=$(q "SHOW GLOBAL STATUS LIKE 'wsrep_cluster_status'" | awk '{print $2}')
      if [ "$st" != 4 ] || [ "$cs" != Primary ] || [ "$wsrep" -lt "$GALERA_EXPECT" ]; then
        okdb=0
        dbline mariadb galera skip 0 "Galera is niet volledig gezond (grootte $wsrep van $GALERA_EXPECT, staat $st, $cs): de database blijft ongemoeid"
      fi
    fi
    if [ "$okdb" = 1 ]; then
      tables=$(q "SELECT table_schema, table_name FROM information_schema.tables WHERE table_type = 'BASE TABLE' AND engine IN ('InnoDB','Aria','MyISAM') AND table_schema NOT IN ('mysql','information_schema','performance_schema','sys')")
      ntab=$(printf '%s\n' "$tables" | grep -c . || true)
@ANALYZE@
@OPTIMIZE@
    fi
  fi
fi
`

const mariadbAnalyze = `      if [ "$MODE" = preview ]; then
        dbline mariadb "$role" analyze "$ntab" "ANALYZE van $ntab tabellen, alleen op deze node"
      else
        n=0; f=0
        printf '%s\n' "$tables" | while IFS="$TAB" read -r s t; do
          case "$s$t" in *[!A-Za-z0-9_\$-]*) continue ;; esac
          [ -z "$t" ] && continue
          q "ANALYZE NO_WRITE_TO_BINLOG TABLE ` + "\\`$s\\`.\\`$t\\`" + `" >/dev/null || echo "ANALYZE van $s.$t lukte niet"
        done
        dbline mariadb "$role" analyze "$ntab" "statistieken van $ntab tabellen bijgewerkt"
      fi
`

const mariadbOptimize = `      frag=$(q "SELECT table_schema, table_name, data_free, data_length + index_length FROM information_schema.tables WHERE table_type = 'BASE TABLE' AND engine = 'InnoDB' AND table_schema NOT IN ('mysql','information_schema','performance_schema','sys') AND data_free >= @MINMB@ * 1048576 AND data_free * 100 >= @MINPCT@ * (data_length + index_length + data_free)")
      nfrag=$(printf '%s\n' "$frag" | grep -c . || true)
      printf '%s\n' "$frag" | while IFS="$TAB" read -r s t free total; do
        [ -n "$t" ] && printf 'CFUPKEEP\ttable\tmariadb\t%s.%s\t%s\t%s\n' "$s" "$t" "$free" "$total"
      done
      if [ "$nfrag" = 0 ]; then
        dbline mariadb "$role" optimize 0 "geen gefragmenteerde tabellen"
      elif [ "$DEGRADED" = 1 ]; then
        dbline mariadb "$role" skip "$nfrag" "een andere node van het cluster is niet verbonden: OPTIMIZE wacht op een volgende beurt"
      elif [ "$MODE" = preview ]; then
        dbline mariadb "$role" optimize "$nfrag" "OPTIMIZE van $nfrag gefragmenteerde tabellen"
      else
        pre=""
        [ "$role" = galera ] && pre="SET SESSION wsrep_OSU_method='RSU'; "
        printf '%s\n' "$frag" | while IFS="$TAB" read -r s t free total; do
          case "$s$t" in *[!A-Za-z0-9_\$-]*) echo "overgeslagen: $s.$t (vreemde tekens)"; continue ;; esac
          [ -z "$t" ] && continue
          echo "OPTIMIZE $s.$t"
          q "${pre}OPTIMIZE NO_WRITE_TO_BINLOG TABLE ` + "\\`$s\\`.\\`$t\\`" + `" >/dev/null || echo "OPTIMIZE van $s.$t lukte niet"
          if [ "$role" = galera ]; then
            i=0
            while [ "$(q "SHOW GLOBAL STATUS LIKE 'wsrep_local_state'" | awk '{print $2}')" != 4 ]; do
              i=$((i + 5)); [ "$i" -gt 900 ] && { echo "CFUPKEEP_STOP"; exit 3; }
              sleep 5
            done
          fi
        done
        if [ $? -ne 0 ]; then
          dbline mariadb "$role" fail "$nfrag" "deze node is na OPTIMIZE niet binnen 15 minuten terug Synced"
          rc=3
        else
          dbline mariadb "$role" optimize "$nfrag" "$nfrag tabellen opnieuw opgebouwd"
        fi
      fi
`

// postgresPart onderhoudt PostgreSQL. Een replica (ook bij Patroni) kan
// geen VACUUM: die volgt de primary vanzelf. Het script zoekt de socket
// zelf, zodat ook Patroni met een eigen poort werkt.
const postgresPart = `if command -v psql >/dev/null 2>&1 && pgrep -x postgres >/dev/null 2>&1 && id postgres >/dev/null 2>&1; then
  step "PostgreSQL"
  sock=""; port=""
  for d in /var/run/postgresql /run/postgresql /tmp; do
    for f in "$d"/.s.PGSQL.*; do
      [ -S "$f" ] || continue
      case "$f" in *.lock) continue ;; esac
      sock=$d; port=${f##*.}; break 2
    done
  done
  pq() { runuser -u postgres -- psql -X -A -t -q -h "$sock" -p "$port" "$@" 2>/dev/null; }
  if [ -z "$sock" ] || [ "$(pq -d postgres -c 'SELECT 1')" != 1 ]; then
    dbline postgresql unknown skip 0 "PostgreSQL draait, maar de gebruiker postgres kan niet verbinden via de socket"
  else
    rec=$(pq -d postgres -c 'SELECT pg_is_in_recovery()')
    if [ "$rec" = t ]; then
      dbline postgresql replica skip 0 "replica: VACUUM en REINDEX gebeuren op de primary, de replica volgt vanzelf"
    else
      role=primary
      dbs=$(pq -d postgres -c "SELECT datname FROM pg_database WHERE datallowconn AND NOT datistemplate ORDER BY 1")
      ndb=$(printf '%s\n' "$dbs" | grep -c . || true)
@VACUUM@
@REINDEX@
    fi
  fi
fi
`

const postgresVacuum = `      if [ "$MODE" = preview ]; then
        dead=0
        for db in $dbs; do
          d=$(pq -d "$db" -c 'SELECT coalesce(sum(n_dead_tup), 0) FROM pg_stat_user_tables'); dead=$((dead + ${d:-0}))
        done
        dbline postgresql "$role" analyze "$ndb" "VACUUM (ANALYZE) van $ndb databases; nu $dead dode rijen"
      else
        for db in $dbs; do
          echo "VACUUM (ANALYZE) $db"
          pq -d "$db" -c 'VACUUM (ANALYZE)' >/dev/null || echo "VACUUM van $db lukte niet"
        done
        dbline postgresql "$role" analyze "$ndb" "VACUUM (ANALYZE) van $ndb databases"
      fi
`

const postgresReindex = `      ver=$(pq -d postgres -c 'SHOW server_version_num')
      if [ "${ver:-0}" -lt 120000 ]; then
        dbline postgresql "$role" skip 0 "REINDEX CONCURRENTLY vraagt PostgreSQL 12 of nieuwer"
      else
        nidx=0; nopx=0
        for db in $dbs; do
          if [ "$(pq -d "$db" -c "SELECT 1 FROM pg_extension WHERE extname = 'pgstattuple'")" != 1 ]; then nopx=$((nopx + 1)); continue; fi
          idx=$(pq -F "$TAB" -d "$db" -c "SELECT quote_ident(n.nspname) || '.' || quote_ident(c.relname), pg_relation_size(c.oid) FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace JOIN pg_am a ON a.oid = c.relam, LATERAL pgstatindex(c.oid) s WHERE c.relkind = 'i' AND a.amname = 'btree' AND n.nspname NOT IN ('pg_catalog', 'information_schema') AND n.nspname !~ '^pg_toast' AND pg_relation_size(c.oid) >= @MINMB@ * 1048576 AND s.avg_leaf_density < 100 - @MINPCT@")
          printf '%s\n' "$idx" | while IFS="$TAB" read -r name bytes; do
            [ -z "$name" ] && continue
            printf 'CFUPKEEP\ttable\tpostgresql\t%s/%s\t0\t%s\n' "$db" "$name" "$bytes"
            if [ "$MODE" != preview ] && [ "$DEGRADED" = 0 ]; then
              echo "REINDEX INDEX CONCURRENTLY $name in $db"
              pq -d "$db" -c "REINDEX INDEX CONCURRENTLY $name" >/dev/null || echo "REINDEX van $name lukte niet"
            fi
          done
          nidx=$((nidx + $(printf '%s\n' "$idx" | grep -c . || true)))
        done
        note="$nidx opgezwollen indexen"
        [ "$nopx" -gt 0 ] && note="$note; $nopx databases zonder de extensie pgstattuple konden niet gemeten worden"
        if [ "$DEGRADED" = 1 ] && [ "$nidx" -gt 0 ]; then
          dbline postgresql "$role" skip "$nidx" "een andere node van het cluster is niet verbonden: REINDEX wacht op een volgende beurt"
        else
          dbline postgresql "$role" optimize "$nidx" "$note"
        fi
      fi
`

const footer = `printf 'CFUPKEEP\tused\tafter\t%s\n' "$(used)"
echo "Klaar."
exit $rc
`

// Script maakt het onderhoudsscript voor één node. Alleen getallen uit
// het beleid komen erin; namen van tabellen leest het script zelf en
// laat het alleen toe met gewone tekens.
func Script(p Policy, mode string, env NodeEnv) string {
	if mode == ModeUrgent {
		p = p.DiskOnly()
	}
	var b strings.Builder
	b.WriteString(header)
	if p.Journal {
		b.WriteString(journalPart)
	}
	if p.Apt {
		b.WriteString(aptPart)
	}
	if p.Logrotate {
		b.WriteString(logrotatePart)
	}
	if p.OldLogs {
		b.WriteString(oldLogsPart)
	}
	if p.Coredumps {
		b.WriteString(coredumpsPart)
	}
	if p.Tmp {
		b.WriteString(tmpPart)
	}
	if p.DockerImages || p.DockerBuildCache {
		b.WriteString(dockerHead)
	}
	if p.DockerImages {
		b.WriteString(dockerImagesPart)
	}
	if p.DockerBuildCache {
		b.WriteString(dockerCachePart)
	}
	if p.DockerLogsMB > 0 {
		b.WriteString(dockerLogsPart)
	}
	if p.DBAnalyze || p.DBOptimize {
		my := mariadbPart
		pg := postgresPart
		an, opt, vac, rei := "", "", "", ""
		if p.DBAnalyze {
			an, vac = mariadbAnalyze, postgresVacuum
		}
		if p.DBOptimize {
			opt, rei = mariadbOptimize, postgresReindex
		}
		my = strings.NewReplacer("@ANALYZE@\n", an, "@OPTIMIZE@\n", opt).Replace(my)
		pg = strings.NewReplacer("@VACUUM@\n", vac, "@REINDEX@\n", rei).Replace(pg)
		b.WriteString(my)
		b.WriteString(pg)
	}
	b.WriteString(footer)
	m := mode
	if m == ModeUrgent {
		m = ModeRun
	}
	deg := "0"
	if env.Degraded {
		deg = "1"
	}
	itoa := strconv.Itoa
	return strings.NewReplacer(
		"@MODE@", m, "@DEGRADED@", deg, "@GALERA@", itoa(max(env.GaleraExpect, 0)),
		"@JOURNALMB@", itoa(p.JournalMB), "@OLDDAYS@", itoa(p.OldLogsDays), "@TMPDAYS@", itoa(p.TmpDays),
		"@IMGHOURS@", itoa(p.DockerImageDays*24), "@IMGDAYS@", itoa(p.DockerImageDays), "@LOGSMB@", itoa(p.DockerLogsMB),
		"@MINMB@", itoa(p.OptimizeMinMB), "@MINPCT@", itoa(p.OptimizeMinPct),
	).Replace(b.String())
}

// scriptID is uniek per taak, poging en node.
func scriptID(jobID fmt.Stringer, attempt, i int) string {
	return fmt.Sprintf("upkeep-%s-%d-%d", jobID, attempt, i)
}
