#!/bin/sh
# Installeert cf-agent als systemd-service (op OPNsense/FreeBSD als
# rc.d-service) en meldt hem aan bij ClusterForge.
#
#   curl -fsSL https://clusterforge.example/install/agent.sh \
#     | sudo sh -s -- --server https://clusterforge.example --token cfe_...
#
# Opnieuw draaien met een nieuw token werkt de agent bij en meldt hem opnieuw aan.
#
# Met --no-enroll in plaats van --token wordt de agent alleen geïnstalleerd,
# voor een eigen golden image: de agent meldt zich dan aan zodra ClusterForge
# /etc/clusterforge/enroll.json in de nieuwe VM zet.
#
# Met --upgrade in plaats van --token wordt een aangemelde agent bijgewerkt
# naar de versie van de server: nieuwe binary na een checksumcontrole, de
# aanmelding in /etc/clusterforge/agent.json blijft, en de service herstart.
#
# De server moet https zijn. Met --allow-http mag http, alleen in een
# vertrouwd netwerk: wie het verkeer kan onderscheppen, kan anders root-
# commando's naar deze machine sturen.
#
# Beide plaatsen cf-agent-fault-clear.service: die draait bij het opstarten
# de storingen van een rampoefening terug die nog in de state staan, ook als
# de agent zelf niet start.
set -eu

SERVER=""
TOKEN=""
NO_ENROLL=0
UPGRADE=0
ALLOW_HTTP=0
# CF_INSTALL_ROOT zet alles onder een andere map; alleen voor de tests.
ROOT="${CF_INSTALL_ROOT:-}"
BIN="$ROOT/usr/local/bin/cf-agent"
ETC="$ROOT/etc/clusterforge"
while [ $# -gt 0 ]; do
	case "$1" in
	--server) SERVER="${2:-}"; shift 2 ;;
	--token) TOKEN="${2:-}"; shift 2 ;;
	--no-enroll) NO_ENROLL=1; shift ;;
	--upgrade) UPGRADE=1; shift ;;
	--allow-http) ALLOW_HTTP=1; shift ;;
	*) echo "onbekende optie: $1" >&2; exit 2 ;;
	esac
done
if [ -z "$SERVER" ] || { [ -z "$TOKEN" ] && [ "$NO_ENROLL" = 0 ] && [ "$UPGRADE" = 0 ]; } ||
	[ $((NO_ENROLL + UPGRADE)) -gt 1 ] || { [ -n "$TOKEN" ] && [ $((NO_ENROLL + UPGRADE)) -gt 0 ]; }; then
	echo "gebruik: agent.sh --server https://clusterforge.example --token cfe_..." >&2
	echo "     of: agent.sh --server https://clusterforge.example --no-enroll" >&2
	echo "     of: agent.sh --server https://clusterforge.example --upgrade" >&2
	exit 2
fi
# Over http kan wie het verkeer onderschept een eigen agent of server
# aanbieden, en die draait als root; daarom alleen op uitdrukkelijke vraag.
case "$SERVER" in
https://*) ;;
http://*)
	if [ "$ALLOW_HTTP" = 0 ]; then
		echo "--server gebruikt http: dat is onveilig; gebruik https://, of voeg --allow-http toe in een vertrouwd netwerk" >&2
		exit 2
	fi
	;;
*) echo "--server moet beginnen met https://" >&2; exit 2 ;;
esac
# install_freebsd installeert cf-agent op FreeBSD, in de praktijk OPNsense
# (fase 34): een rc.d-script onder daemon(8), aangezet in /etc/rc.conf.d.
# OPNsense start bij het opstarten (en na "reload all") elk rc.d-script dat
# daar aan staat. De agent maakt daarna zelf een API-gebruiker in OPNsense.
install_freebsd() {
	if [ "$NO_ENROLL" = 1 ]; then
		echo "--no-enroll is er niet op FreeBSD; gebruik --token" >&2
		exit 2
	fi
	case "${CF_INSTALL_ARCH:-$(uname -m)}" in
	amd64 | x86_64) ;;
	*) echo "op FreeBSD wordt alleen amd64 ondersteund, niet $(uname -m)" >&2; exit 1 ;;
	esac
	if [ "$UPGRADE" = 1 ] && { [ ! -f "$ETC/agent.json" ] || [ ! -x "$BIN" ]; }; then
		echo "deze machine heeft nog geen aangemelde cf-agent; installeer hem met --token" >&2
		exit 1
	fi
	if [ ! -f "$ROOT/usr/local/opnsense/version/core" ]; then
		echo "let op: dit is geen OPNsense; cf-agent draait, maar zonder firewallbeheer" >&2
	fi
	SERVER="${SERVER%/}"
	TMP="$(mktemp -d)"
	trap 'rm -rf "$TMP"' EXIT
	echo "cf-agent downloaden (freebsd-amd64)"
	curl -fsSL "$SERVER/downloads/cf-agent-freebsd-amd64" -o "$TMP/cf-agent"
	curl -fsSL "$SERVER/downloads/cf-agent-freebsd-amd64.sha256" -o "$TMP/cf-agent.sha256"
	EXPECTED="$(cut -d' ' -f1 "$TMP/cf-agent.sha256")"
	if command -v sha256 >/dev/null 2>&1; then
		ACTUAL="$(sha256 -q "$TMP/cf-agent")"
	else
		ACTUAL="$(sha256sum "$TMP/cf-agent" | cut -d' ' -f1)"
	fi
	if [ "$EXPECTED" != "$ACTUAL" ]; then
		echo "checksum klopt niet; download afgebroken" >&2
		exit 1
	fi
	mkdir -p "$ROOT/usr/local/bin" "$ROOT/usr/local/etc/rc.d" "$ROOT/etc/rc.conf.d"
	OLD="$("$BIN" version 2>/dev/null || echo geen)"
	install -m 0755 "$TMP/cf-agent" "$BIN.new"
	mv -f "$BIN.new" "$BIN"
	install -d -m 0700 "$ETC"
	cat >"$ROOT/usr/local/etc/rc.d/cf_agent" <<'RC'
#!/bin/sh
#
# PROVIDE: cf_agent
# REQUIRE: LOGIN NETWORKING
# KEYWORD: shutdown
#
# ClusterForge-agent. daemon(8) start hem opnieuw als hij stopt (-r) en
# stuurt zijn uitvoer naar syslog met de tag cf-agent.

. /etc/rc.subr

name=cf_agent
rcvar=cf_agent_enable

load_rc_config $name
: ${cf_agent_enable:="NO"}

pidfile="/var/run/${name}.pid"
procname="/usr/sbin/daemon"
command="/usr/sbin/daemon"
command_args="-r -R 5 -S -T cf-agent -P ${pidfile} /usr/local/bin/cf-agent run"

run_rc_command "$1"
RC
	chmod 0755 "$ROOT/usr/local/etc/rc.d/cf_agent"
	echo 'cf_agent_enable="YES"' >"$ROOT/etc/rc.conf.d/cf_agent"
	if [ "$UPGRADE" = 1 ]; then
		service cf_agent restart
		echo "klaar: cf-agent bijgewerkt van $OLD naar $("$BIN" version); de aanmelding is gebleven"
		exit 0
	fi
	echo "aanmelden bij $SERVER"
	if [ "$ALLOW_HTTP" = 1 ]; then
		CF_ENROLL_TOKEN="$TOKEN" "$BIN" enroll -server "$SERVER" -allow-http
	else
		CF_ENROLL_TOKEN="$TOKEN" "$BIN" enroll -server "$SERVER"
	fi
	service cf_agent restart
	echo "klaar: cf-agent draait en is aangemeld bij $SERVER"
}

if [ "$(id -u)" != 0 ]; then
	echo "draai dit script als root, bijvoorbeeld met sudo" >&2
	exit 1
fi
# CF_INSTALL_OS doet alsof het script op een ander systeem draait; alleen
# voor de tests.
OS="${CF_INSTALL_OS:-$(uname -s)}"
if [ "$OS" = FreeBSD ]; then
	install_freebsd
	exit 0
fi
if ! command -v systemctl >/dev/null 2>&1; then
	echo "systemd is nodig voor cf-agent" >&2
	exit 1
fi
if [ "$NO_ENROLL" = 1 ] && [ -f "$ETC/agent.json" ]; then
	echo "deze machine is al aangemeld (/etc/clusterforge/agent.json); --no-enroll is voor een golden image" >&2
	exit 1
fi
if [ "$UPGRADE" = 1 ] && { [ ! -f "$ETC/agent.json" ] || [ ! -x "$BIN" ]; }; then
	echo "deze machine heeft nog geen aangemelde cf-agent; installeer hem met --token" >&2
	exit 1
fi
case "$(uname -m)" in
x86_64 | amd64) ARCH=amd64 ;;
aarch64 | arm64) ARCH=arm64 ;;
*) echo "architectuur $(uname -m) wordt niet ondersteund" >&2; exit 1 ;;
esac
SERVER="${SERVER%/}"

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
fetch() {
	if command -v curl >/dev/null 2>&1; then
		curl -fsSL "$1" -o "$2"
	else
		wget -qO "$2" "$1"
	fi
}

echo "cf-agent downloaden ($ARCH)"
fetch "$SERVER/downloads/cf-agent-linux-$ARCH" "$TMP/cf-agent"
fetch "$SERVER/downloads/cf-agent-linux-$ARCH.sha256" "$TMP/cf-agent.sha256"
EXPECTED="$(cut -d' ' -f1 "$TMP/cf-agent.sha256")"
ACTUAL="$(sha256sum "$TMP/cf-agent" | cut -d' ' -f1)"
if [ "$EXPECTED" != "$ACTUAL" ]; then
	echo "checksum klopt niet; download afgebroken" >&2
	exit 1
fi

# fault_clear_unit plaatst de oneshot-unit die bij elke start achtergebleven
# storingen terugdraait, voor het netwerk en de agent.
fault_clear_unit() {
	install -d "$ROOT/etc/systemd/system"
	cat >"$ROOT/etc/systemd/system/cf-agent-fault-clear.service" <<'UNIT'
[Unit]
Description=ClusterForge: achtergebleven storingen van een rampoefening terugdraaien
Documentation=https://github.com/Jonasz1996/ClusterForge
DefaultDependencies=no
After=local-fs.target
Before=network-pre.target cf-agent.service
Wants=network-pre.target

[Service]
Type=oneshot
ExecStart=/usr/local/bin/cf-agent fault-clear

[Install]
WantedBy=multi-user.target
UNIT
}

if [ "$UPGRADE" = 1 ]; then
	OLD="$("$BIN" version 2>/dev/null || echo onbekend)"
	# Eerst naast de oude zetten en dan in één keer vervangen, zodat er nooit
	# een halve binary staat.
	install -m 0755 "$TMP/cf-agent" "$BIN.new"
	mv -f "$BIN.new" "$BIN"
	fault_clear_unit
	systemctl daemon-reload
	systemctl enable cf-agent-fault-clear >/dev/null 2>&1
	systemctl restart cf-agent
	echo "klaar: cf-agent bijgewerkt van $OLD naar $("$BIN" version); de aanmelding is gebleven"
	exit 0
fi

install -D -m 0755 "$TMP/cf-agent" "$BIN"
install -d -m 0700 "$ETC"

install -d "$ROOT/etc/systemd/system"
cat >"$ROOT/etc/systemd/system/cf-agent.service" <<'UNIT'
[Unit]
Description=ClusterForge agent
Documentation=https://github.com/Jonasz1996/ClusterForge
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=/usr/local/bin/cf-agent run
Restart=always
RestartSec=5
# De agent voert later beheeracties uit (herstarten, services), dus root.
User=root

[Install]
WantedBy=multi-user.target
UNIT

fault_clear_unit
systemctl daemon-reload
systemctl enable cf-agent-fault-clear >/dev/null 2>&1
if [ "$NO_ENROLL" = 1 ]; then
	systemctl enable cf-agent >/dev/null 2>&1
	echo "klaar: cf-agent is geïnstalleerd maar niet aangemeld; hij wacht op /etc/clusterforge/enroll.json"
	exit 0
fi

echo "aanmelden bij $SERVER"
# Het token via de omgeving, niet als argument: dat staat in ps.
if [ "$ALLOW_HTTP" = 1 ]; then
	CF_ENROLL_TOKEN="$TOKEN" "$BIN" enroll -server "$SERVER" -allow-http
else
	CF_ENROLL_TOKEN="$TOKEN" "$BIN" enroll -server "$SERVER"
fi

systemctl enable cf-agent >/dev/null 2>&1
systemctl restart cf-agent
echo "klaar: cf-agent draait en is aangemeld bij $SERVER"
