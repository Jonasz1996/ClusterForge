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
	w("")
	w(`echo "== CrowdSec installeren"`)
	w("if ! command -v cscli >/dev/null 2>&1; then")
	w("  apt-get update -q")
	w("  apt-get install -y -q curl gnupg ca-certificates")
	w("  install -d -m 0755 /etc/apt/keyrings")
	w("  curl -fsSL https://packagecloud.io/crowdsec/crowdsec/gpgkey | gpg --dearmor --yes -o /etc/apt/keyrings/crowdsec_crowdsec-archive-keyring.gpg")
	w(`  echo "deb [signed-by=/etc/apt/keyrings/crowdsec_crowdsec-archive-keyring.gpg] https://packagecloud.io/crowdsec/crowdsec/any any main" > /etc/apt/sources.list.d/crowdsec_crowdsec.list`)
	w("  apt-get update -q")
	w("  apt-get install -y -q crowdsec")
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
			w("if [ \"$OLD_BOUNCER\" != \"$(cat %s)\" ] || ! systemctl is-active --quiet crowdsec-firewall-bouncer; then systemctl restart crowdsec-firewall-bouncer; fi", bouncerLocal)
		}
		w("systemctl is-active --quiet crowdsec-firewall-bouncer || { journalctl -u crowdsec-firewall-bouncer -n 30 --no-pager >&2; exit 1; }")
	} else {
		w("if systemctl list-unit-files crowdsec-firewall-bouncer.service >/dev/null 2>&1; then systemctl disable --now crowdsec-firewall-bouncer >/dev/null 2>&1 || true; fi")
	}
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
