package crowdsec

import (
	"encoding/base64"
	"fmt"
	"sort"
	"strings"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// Script maakt het bash-script dat het plan op een node toepast. Het is
// idempotent: nog eens draaien verandert niets. Faalt de configuratietest,
// dan zet het de vorige bestanden terug en faalt het.
func Script(p Plan) string {
	var b strings.Builder
	w := func(format string, args ...any) { fmt.Fprintf(&b, format+"\n", args...) }
	w("#!/bin/bash")
	w("# CrowdSec toepassen (ClusterForge, plan %s)", p.Hash)
	w("set -euo pipefail")
	w("export DEBIAN_FRONTEND=noninteractive LC_ALL=C")
	w("STATE=%s", protocol.CrowdSecStateFile)
	w("BACKUP=$(mktemp -d /var/tmp/cf-crowdsec.XXXXXX)")
	w("trap 'rm -rf \"$BACKUP\"' EXIT")
	b.WriteString(nginxGuardScript)
	w("cf_nginx_guard")
	w("")
	w(`echo "== CrowdSec installeren"`)
	w("if ! command -v cscli >/dev/null 2>&1; then")
	w("  apt-get update -q")
	w("  apt-get install -y -q curl gnupg ca-certificates")
	w("  install -d -m 0755 /etc/apt/keyrings")
	w("  curl -fsSL https://packagecloud.io/crowdsec/crowdsec/gpgkey | gpg --dearmor --yes -o /etc/apt/keyrings/crowdsec_crowdsec-archive-keyring.gpg")
	w(`  echo "deb [signed-by=/etc/apt/keyrings/crowdsec_crowdsec-archive-keyring.gpg] https://packagecloud.io/crowdsec/crowdsec/any any main" > /etc/apt/sources.list.d/crowdsec_crowdsec.list`)
	w("  apt-get update -q")
	w("  # Botst de standaardpoort 8080 (bv. de ClusterForge-server zelf), dan start")
	w("  # CrowdSec bij de installatie niet; de poort gaat hieronder om en dan opnieuw.")
	w("  apt-get install -y -q crowdsec || INSTALL_RETRY=1")
	w("else")
	w(`  echo "CrowdSec staat er al: $(cscli version 2>&1 | grep -m1 -o 'v[0-9][0-9.]*' || true)"`)
	w("fi")
	if p.Bouncer {
		w("if ! command -v crowdsec-firewall-bouncer >/dev/null 2>&1; then")
		w(`  echo "== nftables-bouncer installeren"`)
		w("  apt-get install -y -q crowdsec-firewall-bouncer-nftables")
		w("fi")
	}
	w("")
	b.WriteString(lapiPortScript)
	w("if [ -n \"${INSTALL_RETRY:-}\" ]; then dpkg --configure -a; fi")
	w("")
	w(`echo "== Collections"`)
	w("cscli hub update >/dev/null")
	cols := strings.Join(p.Collections, " ")
	w("WANT=%q", cols)
	w("for c in $WANT; do cscli collections install \"$c\" >/dev/null && echo \"  $c\"; done")
	w("# Wat ClusterForge eerder zette en niet meer nodig is, gaat weg; wat je zelf installeerde nooit.")
	w("if [ -s \"$STATE\" ]; then")
	w("  for c in $(sed -n 's/.*\"collections\":\\[\\([^]]*\\)\\].*/\\1/p' \"$STATE\" | tr -d '\"' | tr ',' ' '); do")
	w("    case \" $WANT \" in *\" $c \"*) ;; *) echo \"  weg: $c\"; cscli collections remove \"$c\" >/dev/null || true ;; esac")
	w("  done")
	w("fi")
	w("")
	w(`echo "== Configuratie"`)
	w("# ClusterForge beheert de acquisitie: de standaard acquis.yaml van het pakket gaat opzij,")
	w("# anders leest CrowdSec dezelfde logs twee keer.")
	w("if [ -f /etc/crowdsec/acquis.yaml ] && ! grep -q 'ClusterForge' /etc/crowdsec/acquis.yaml; then")
	w("  cp -a /etc/crowdsec/acquis.yaml /etc/crowdsec/acquis.yaml.cf-orig")
	w("fi")
	w("mkdir -p %s /etc/crowdsec/parsers/s02-enrich \"$BACKUP/acquis.d\"", acquisDir)
	w("cp -a %s/. \"$BACKUP/acquis.d/\" 2>/dev/null || true", acquisDir)
	w("for f in /etc/crowdsec/acquis.yaml %s %s; do [ -f \"$f\" ] && cp -a \"$f\" \"$BACKUP/$(basename \"$f\")\"; done; true", whitelistFile, bouncerLocal)
	w("printf '# Beheerd door ClusterForge: de bronnen staan in acquis.d/cf-*.yaml.\\n' > /etc/crowdsec/acquis.yaml")
	w("rm -f %s/cf-*.yaml", acquisDir)
	paths := make([]string, 0, len(p.Files))
	for k := range p.Files {
		paths = append(paths, k)
	}
	sort.Strings(paths)
	bouncerChanged := false
	for _, f := range paths {
		if f == bouncerLocal {
			bouncerChanged = true
			w("OLD_BOUNCER=$(cat %s 2>/dev/null || true)", f)
		}
		w("echo %s | base64 -d > %s.cf-new && mv %s.cf-new %s", base64.StdEncoding.EncodeToString([]byte(p.Files[f])), f, f, f)
		w(`echo "  %s"`, f)
	}
	if !p.Bouncer {
		w("rm -f %s", bouncerLocal)
	}
	w("")
	w(`echo "== Configuratie testen"`)
	w("if ! OUT=$(crowdsec -c /etc/crowdsec/config.yaml -t 2>&1); then")
	w(`  echo "$OUT" | tail -n 20 >&2`)
	w(`  echo "De nieuwe configuratie klopt niet: de vorige gaat terug. CrowdSec draait verder zoals het was." >&2`)
	w("  rm -f %s/cf-*.yaml", acquisDir)
	w("  cp -a \"$BACKUP/acquis.d/.\" %s/ 2>/dev/null || true", acquisDir)
	w("  for f in acquis.yaml:/etc/crowdsec/acquis.yaml cf-whitelist.yaml:%s crowdsec-firewall-bouncer.yaml.local:%s; do", whitelistFile, bouncerLocal)
	w("    src=\"$BACKUP/${f%%%%:*}\"; dst=\"${f#*:}\"; if [ -f \"$src\" ]; then cp -a \"$src\" \"$dst\"; fi")
	w("  done")
	w("  exit 1")
	w("fi")
	w("")
	w(`echo "== Diensten"`)
	w("systemctl enable crowdsec >/dev/null 2>&1 || true")
	w("systemctl restart crowdsec")
	w("sleep 2")
	w("systemctl is-active --quiet crowdsec || { journalctl -u crowdsec -n 30 --no-pager >&2; exit 1; }")
	if p.Bouncer {
		w("systemctl enable crowdsec-firewall-bouncer >/dev/null 2>&1 || true")
		if bouncerChanged {
			w("if [ \"$OLD_BOUNCER\" != \"$(cat %s)\" ] || [ -n \"${BOUNCER_PORT_CHANGED:-}\" ] || ! systemctl is-active --quiet crowdsec-firewall-bouncer; then systemctl restart crowdsec-firewall-bouncer; fi", bouncerLocal)
		} else {
			w("if [ -n \"${BOUNCER_PORT_CHANGED:-}\" ]; then systemctl restart crowdsec-firewall-bouncer; fi")
		}
		w("systemctl is-active --quiet crowdsec-firewall-bouncer || { journalctl -u crowdsec-firewall-bouncer -n 30 --no-pager >&2; exit 1; }")
	} else {
		w("if systemctl list-unit-files crowdsec-firewall-bouncer.service >/dev/null 2>&1; then systemctl disable --now crowdsec-firewall-bouncer >/dev/null 2>&1 || true; fi")
	}
	w("# Na het toepassen: nginx moet nog altijd door nginx -t.")
	w("cf_nginx_guard")
	w("")
	cq := make([]string, len(p.Collections))
	for i, c := range p.Collections {
		cq[i] = `"` + c + `"`
	}
	w("mkdir -p \"$(dirname \"$STATE\")\"")
	w("printf '{\"hash\":\"%s\",\"collections\":[%s],\"applied_at\":\"%%s\"}\\n' \"$(date -u +%%Y-%%m-%%dT%%H:%%M:%%SZ)\" > \"$STATE\"", p.Hash, strings.ReplaceAll(strings.Join(cq, ","), `"`, `\"`))
	w(`echo "Klaar: plan %s toegepast."`, p.Hash)
	return b.String()
}

// nginxGuardScript beschermt nginx op de host. Het pakket
// crowdsec-nginx-bouncer zet /etc/nginx/conf.d/crowdsec_nginx.conf, die de
// lua-modules (lua-resty-*, libnginx-mod-http-lua) nodig heeft. Ontbreken die,
// dan faalt nginx -t, en daarmee elke apt-run die nginx herstart. ClusterForge
// installeert die bouncer nooit (de firewall-bouncer beschermt de node); staat
// hij er toch en breekt hij nginx, dan gaat zijn config opzij, wordt nginx
// herladen en dpkg afgewerkt. Breekt nginx om een andere reden, dan blijft
// alles staan en meldt het script dat.
const nginxGuardScript = `cf_nginx_guard() {
  command -v nginx >/dev/null 2>&1 || return 0
  nginx -t >/dev/null 2>&1 && return 0
  local moved="" f
  for f in /etc/nginx/conf.d/crowdsec_nginx.conf /etc/nginx/sites-enabled/crowdsec_nginx.conf; do
    if [ -e "$f" ]; then mv "$f" "$f.cf-uit"; moved="$moved $f"; fi
  done
  if [ -z "$moved" ]; then
    echo "LET OP: nginx -t faalt, niet door CrowdSec:"; nginx -t 2>&1 | tail -n 3 || true
    return 0
  fi
  if nginx -t >/dev/null 2>&1; then
    echo "nginx: de CrowdSec-nginx-bouncer werkt hier niet (lua-modules ontbreken) en brak nginx; uitgezet:$moved (.cf-uit). De firewall-bouncer beschermt de node."
    systemctl reload nginx >/dev/null 2>&1 || systemctl restart nginx >/dev/null 2>&1 || true
    dpkg --configure -a 2>&1 | tail -n 3 || true
  else
    for f in $moved; do mv "$f.cf-uit" "$f"; done
    echo "LET OP: nginx -t faalt ook zonder de CrowdSec-config; niets gewijzigd:"; nginx -t 2>&1 | tail -n 3 || true
  fi
}
`

// lapiPortScript houdt de lokale API van CrowdSec weg van poorten die iets
// anders nodig heeft. Standaard luistert die op 127.0.0.1:8080, net als de
// ClusterForge-server en veel containers; wie dan als laatste start, faalt.
// De poort gaat om als iets anders dan CrowdSec op die poort luistert, of
// als een container (ook een gestopte) die poort op de host publiceert. Een
// eerder gekozen andere poort blijft staan; elke keer toepassen controleert
// het opnieuw, zodat ClusterForge nooit naar 8080 terugzet.
const lapiPortScript = `echo "== Poort van de lokale API"
CFG=/etc/crowdsec/config.yaml
CRED=/etc/crowdsec/local_api_credentials.yaml
BCFG=/etc/crowdsec/bouncers/crowdsec-firewall-bouncer.yaml
cf_port_taken() {
  local p=$1
  if ss -ltnpH "sport = :$p" 2>/dev/null | grep -v '"crowdsec"' | grep -q .; then return 0; fi
  if command -v docker >/dev/null 2>&1; then
    local ids; ids=$(docker ps -aq 2>/dev/null || true)
    if [ -n "$ids" ] && docker inspect -f '{{json .HostConfig.PortBindings}}' $ids 2>/dev/null | grep -q "\"HostPort\":\"$p\""; then return 0; fi
  fi
  return 1
}
if [ -f "$CFG" ]; then
  CUR=$(sed -n 's/^[[:space:]]*listen_uri:[[:space:]]*["]\{0,1\}[^:"]*:\([0-9]\{1,5\}\).*/\1/p' "$CFG" | head -n1)
  CUR=${CUR:-8080}
  NEW=$CUR
  if cf_port_taken "$CUR"; then
    NEW=""
    for p in $(seq 8081 8099); do
      if ! cf_port_taken "$p" && ! ss -ltnH "sport = :$p" 2>/dev/null | grep -q .; then NEW=$p; break; fi
    done
    [ -n "$NEW" ] || { echo "Geen vrije poort tussen 8081 en 8099 voor de lokale API van CrowdSec." >&2; exit 1; }
    echo "  poort $CUR is bezet door iets anders: de lokale API gaat naar 127.0.0.1:$NEW"
  else
    echo "  127.0.0.1:$CUR"
  fi
  sed -i "s#^\([[:space:]]*listen_uri:[[:space:]]*\)[\"]\{0,1\}[^\"[:space:]]*[\"]\{0,1\}#\1127.0.0.1:$NEW#" "$CFG"
  [ -f "$CRED" ] && sed -i "s#^url:.*#url: http://127.0.0.1:$NEW#" "$CRED"
  [ -f "$BCFG" ] && sed -i "s#^api_url:.*#api_url: http://127.0.0.1:$NEW/#" "$BCFG"
  if [ "$NEW" != "$CUR" ] && systemctl is-active --quiet crowdsec-firewall-bouncer 2>/dev/null; then BOUNCER_PORT_CHANGED=1; fi
fi
`

// RemoveScript haalt CrowdSec en de bouncer van een node.
func RemoveScript() string {
	return strings.Join([]string{
		"#!/bin/bash",
		"# CrowdSec verwijderen (ClusterForge)",
		"set -euo pipefail",
		"export DEBIAN_FRONTEND=noninteractive",
		"apt-get purge -y -q crowdsec-firewall-bouncer-nftables crowdsec-firewall-bouncer-iptables crowdsec 2>&1 | tail -n 5 || true",
		"rm -f " + protocol.CrowdSecStateFile,
		"nft delete table ip crowdsec 2>/dev/null || true",
		"nft delete table ip6 crowdsec6 2>/dev/null || true",
		`echo "CrowdSec is verwijderd."`,
	}, "\n") + "\n"
}
