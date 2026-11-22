package zabbixagent

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
)

// Config is wat er op één node moet staan.
type Config struct {
	Hostname     string
	Server       string
	ServerActive string
	Version      string
	HostMetadata string
	PSKIdentity  string
	// PSKHash is de vingerafdruk van de sleutel: zo verandert de hash als
	// de sleutel verandert, zonder dat die ontsleuteld hoeft te worden.
	PSKHash string
}

var (
	serverRe   = regexp.MustCompile(`^[A-Za-z0-9.:;,_\-\[\]]+$`)
	versionRe  = regexp.MustCompile(`^[0-9]{1,2}\.[0-9]$`)
	metadataRe = regexp.MustCompile(`^[A-Za-z0-9 ._:,=/@+-]{0,255}$`)
	hostnameRe = regexp.MustCompile(`^[A-Za-z0-9._ -]{1,128}$`)
	identityRe = regexp.MustCompile(`[^A-Za-z0-9._-]+`)
)

// Identity is de PSK-identiteit van een node: cf- en de hostnaam.
func Identity(hostname string) string {
	s := identityRe.ReplaceAllString(hostname, "-")
	if len(s) > 120 {
		s = s[:120]
	}
	return "cf-" + s
}

// AgentConf is zabbix_agent2.conf.
func AgentConf(c Config) string {
	return fmt.Sprintf(`# Beheerd door ClusterForge (Zabbix-agents): wijzigingen hier worden overschreven.
# Eigen instellingen horen in /etc/zabbix/zabbix_agent2.d/*.conf.
PidFile=/run/zabbix/zabbix_agent2.pid
LogFile=/var/log/zabbix/zabbix_agent2.log
LogFileSize=0

Server=%s
ServerActive=%s
Hostname=%s
HostMetadata=%s

TLSConnect=psk
TLSAccept=psk
TLSPSKIdentity=%s
TLSPSKFile=/etc/zabbix/zabbix_agent2.psk

PluginSocket=/run/zabbix/agent.plugin.sock
ControlSocket=/run/zabbix/agent.sock

Include=/etc/zabbix/zabbix_agent2.d/plugins.d/*.conf
Include=/etc/zabbix/zabbix_agent2.d/*.conf
`, c.Server, c.ServerActive, c.Hostname, c.HostMetadata, c.PSKIdentity)
}

// Hash zegt of een node opnieuw toegepast moet worden.
func (c Config) Hash() string {
	sum := sha256.Sum256([]byte(scriptVersion + "\x00" + c.Version + "\x00" + c.PSKHash + "\x00" + AgentConf(c)))
	return hex.EncodeToString(sum[:])[:16]
}

// Validate controleert wat in het script komt.
func (c Config) Validate() error {
	switch {
	case !serverRe.MatchString(c.Server):
		return fmt.Errorf("bij Server: alleen adressen en namen gescheiden door komma's")
	case !serverRe.MatchString(c.ServerActive):
		return fmt.Errorf("bij ServerActive: alleen adressen en namen (met :poort) gescheiden door komma's of ;")
	case !versionRe.MatchString(c.Version):
		return fmt.Errorf("versie zoals 8.0")
	case !metadataRe.MatchString(c.HostMetadata):
		return fmt.Errorf("bij HostMetadata: letters, cijfers, spaties en . _ : , = / @ + -")
	case !hostnameRe.MatchString(c.Hostname):
		return fmt.Errorf("hostnaam %q past niet in een Zabbix-config", c.Hostname)
	}
	return nil
}

// scriptVersion verandert als het script iets anders gaat doen, zodat elke
// node het opnieuw krijgt.
const scriptVersion = "2"

// Script installeert zabbix-agent2 uit de repository van Zabbix, schrijft
// de config en de PSK, zet de Docker-plugin aan waar Docker draait en
// herstart de agent. De sleutel staat in het script; het script haalt
// zichzelf daarom weg en toont hem nooit.
func Script(c Config, psk string) string {
	var b strings.Builder
	fmt.Fprintf(&b, `#!/bin/bash
set -euo pipefail
trap 'rm -f "$0"' EXIT
export DEBIAN_FRONTEND=noninteractive
ZBX_VERSION=%s
APT_OPTS=(-y -o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold -o DPkg::Lock::Timeout=600)

# os-release in een subshell: het zet zelf VERSION en andere namen.
ID=$(. /etc/os-release && echo "${ID:-}")
VERSION_ID=$(. /etc/os-release && echo "${VERSION_ID:-}")
case "$ID" in
  debian|ubuntu) ;;
  *) echo "zabbix-agent2 via ClusterForge kan alleen op Debian of Ubuntu (dit is ${ID:-onbekend})"; exit 1 ;;
esac

if ! grep -rqs "zabbix/${ZBX_VERSION}/" /etc/apt/sources.list.d/; then
  deb="zabbix-release_latest_${ZBX_VERSION}+${ID}${VERSION_ID}_all.deb"
  url="https://repo.zabbix.com/zabbix/${ZBX_VERSION}/release/${ID}/pool/main/z/zabbix-release/${deb}"
  echo "Repository van Zabbix ${ZBX_VERSION} toevoegen ($deb)"
  tmp=$(mktemp -d)
  if command -v curl >/dev/null; then curl -fsSL -o "$tmp/$deb" "$url"; else wget -q -O "$tmp/$deb" "$url"; fi
  dpkg -i "$tmp/$deb"
  rm -rf "$tmp"
  apt-get update -q
  apt-get install "${APT_OPTS[@]}" zabbix-agent2
elif ! dpkg-query -W -f='${Status}' zabbix-agent2 2>/dev/null | grep -q "install ok installed"; then
  apt-get update -q
  apt-get install "${APT_OPTS[@]}" zabbix-agent2
fi
echo "zabbix-agent2 $(dpkg-query -W -f='${Version}' zabbix-agent2)"

# De oude agent (zabbix-agent) luistert ook op 10050.
if systemctl list-unit-files zabbix-agent.service >/dev/null 2>&1 && systemctl is-enabled --quiet zabbix-agent 2>/dev/null; then
  echo "zabbix-agent (de oude agent) uitzetten: zabbix-agent2 neemt het over"
  systemctl disable --now zabbix-agent
fi

install -d -m 0755 /etc/zabbix/zabbix_agent2.d/plugins.d
umask 077
cat > /etc/zabbix/zabbix_agent2.psk.new <<'CFPSK'
%s
CFPSK
chown zabbix:zabbix /etc/zabbix/zabbix_agent2.psk.new
chmod 0600 /etc/zabbix/zabbix_agent2.psk.new
mv -f /etc/zabbix/zabbix_agent2.psk.new /etc/zabbix/zabbix_agent2.psk
umask 022
cat > /etc/zabbix/zabbix_agent2.conf.new <<'CFCONF'
%sCFCONF
if [ -f /etc/zabbix/zabbix_agent2.conf ] && ! grep -q "Beheerd door ClusterForge" /etc/zabbix/zabbix_agent2.conf; then
  cp -a /etc/zabbix/zabbix_agent2.conf /etc/zabbix/zabbix_agent2.conf.voor-clusterforge
  echo "De vorige config staat in /etc/zabbix/zabbix_agent2.conf.voor-clusterforge"
fi
mv -f /etc/zabbix/zabbix_agent2.conf.new /etc/zabbix/zabbix_agent2.conf
chmod 0644 /etc/zabbix/zabbix_agent2.conf

if [ -S /var/run/docker.sock ] && getent group docker >/dev/null; then
  echo "Docker draait: Docker-plugin aan en zabbix in de groep docker"
  echo "Plugins.Docker.Endpoint=unix:///var/run/docker.sock" > /etc/zabbix/zabbix_agent2.d/plugins.d/docker.conf
  usermod -aG docker zabbix
fi

systemctl enable --quiet zabbix-agent2
systemctl restart zabbix-agent2
for i in 1 2 3 4 5; do
  sleep 1
  systemctl is-active --quiet zabbix-agent2 && break
done
if ! systemctl is-active --quiet zabbix-agent2; then
  journalctl -u zabbix-agent2 -n 20 --no-pager || true
  echo "zabbix-agent2 start niet"
  exit 1
fi
echo "zabbix-agent2 draait met PSK-identiteit %s"
`, c.Version, psk, AgentConf(c), c.PSKIdentity)
	return b.String()
}
