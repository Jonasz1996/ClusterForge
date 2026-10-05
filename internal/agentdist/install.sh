#!/bin/sh
# Installeert cf-agent als systemd-service en meldt hem aan bij ClusterForge.
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
set -eu

SERVER=""
TOKEN=""
NO_ENROLL=0
UPGRADE=0
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
if [ "$(id -u)" != 0 ]; then
	echo "draai dit script als root, bijvoorbeeld met sudo" >&2
	exit 1
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

if [ "$UPGRADE" = 1 ]; then
	OLD="$("$BIN" version 2>/dev/null || echo onbekend)"
	# Eerst naast de oude zetten en dan in één keer vervangen, zodat er nooit
	# een halve binary staat.
	install -m 0755 "$TMP/cf-agent" "$BIN.new"
	mv -f "$BIN.new" "$BIN"
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

systemctl daemon-reload
if [ "$NO_ENROLL" = 1 ]; then
	systemctl enable cf-agent >/dev/null 2>&1
	echo "klaar: cf-agent is geïnstalleerd maar niet aangemeld; hij wacht op /etc/clusterforge/enroll.json"
	exit 0
fi

echo "aanmelden bij $SERVER"
"$BIN" enroll -server "$SERVER" -token "$TOKEN"

systemctl enable cf-agent >/dev/null 2>&1
systemctl restart cf-agent
echo "klaar: cf-agent draait en is aangemeld bij $SERVER"
