#!/bin/bash
# Maakt op een Proxmox-host een CT-template voor ClusterForge: een
# Debian 13-container (LXC) met cf-agent. ClusterForge kloont hieruit de CT's
# van een nieuw cluster, net als VM's uit de golden image.
#
#   curl -fsSL https://clusterforge.example/install/ct-template.sh \
#     | bash -s -- --server https://clusterforge.example
#
# Een CT heeft geen QEMU guest agent, dus ClusterForge kan er geen
# aanmeldbestand in zetten. In de template staat daarom
# /etc/clusterforge/claim.json met het adres van ClusterForge: een gekloonde
# CT vraagt zelf om aanmelding, en dat lukt alleen tijdens een uitrol die op
# zijn hostname wacht, vanaf het adres dat die uitrol hem gaf.
#
# Draai het als root op een Proxmox-host. Het script haalt zo nodig de
# Debian 13-template op met pveam en verandert verder niets aan de host.
set -euo pipefail

SERVER=""
VMID=9100
STORAGE=local-lvm
TMPL_STORAGE=local
BRIDGE=vmbr0
NAME=debian-13-clusterforge-ct
OSTEMPLATE=""
REPLACE=0
ALLOW_HTTP=0

usage() {
	cat >&2 <<'EOF'
gebruik: ct-template.sh --server https://clusterforge.example [opties]

  --server URL         adres van ClusterForge; daar komt cf-agent vandaan, en
                       daar melden de CT's zich aan
  --allow-http         een http-adres toestaan (alleen in een vertrouwd netwerk)
  --vmid N             nummer van de template (standaard 9100)
  --storage NAAM       storage voor de schijf (standaard local-lvm); met gedeelde
                       storage (Ceph, NFS) kan ClusterForge op elke host klonen
  --tmpl-storage NAAM  storage voor de Debian-template van pveam (standaard local)
  --bridge NAAM        netwerkbridge (standaard vmbr0)
  --name NAAM          hostname van de template (standaard debian-13-clusterforge-ct)
  --ostemplate VOLID   een eigen containertemplate, zoals
                       local:vztmpl/debian-13-standard_13.1-2_amd64.tar.zst
  --replace            een bestaande template met dit nummer vervangen
EOF
	exit 2
}

fail() {
	echo "fout: $*" >&2
	exit 1
}

# Alles zit in main, zodat bash het hele script gelezen heeft voor er iets
# draait; zo kan geen commando de rest van een gepipet script opeten.
main() {
	while [ $# -gt 0 ]; do
		case "$1" in
		--replace) REPLACE=1; shift; continue ;;
		--allow-http) ALLOW_HTTP=1; shift; continue ;;
		-h | --help) usage ;;
		esac
		[ $# -ge 2 ] || usage
		case "$1" in
		--server) SERVER="$2" ;;
		--vmid) VMID="$2" ;;
		--storage) STORAGE="$2" ;;
		--tmpl-storage) TMPL_STORAGE="$2" ;;
		--bridge) BRIDGE="$2" ;;
		--name) NAME="$2" ;;
		--ostemplate) OSTEMPLATE="$2" ;;
		*) echo "onbekende optie: $1" >&2; usage ;;
		esac
		shift 2
	done

	[ -n "$SERVER" ] || usage
	SERVER="${SERVER%/}"
	case "$SERVER" in
	https://*) ;;
	http://*) [ "$ALLOW_HTTP" = 1 ] || fail "--server gebruikt http: dat is onveilig; gebruik https://, of voeg --allow-http toe in een vertrouwd netwerk" ;;
	*) fail "--server moet beginnen met https://" ;;
	esac
	[ "$(id -u)" = 0 ] || fail "draai dit script als root op een Proxmox-host"
	command -v pct >/dev/null 2>&1 || fail "pct ontbreekt; dit script hoort op een Proxmox-host"
	[[ "$VMID" =~ ^[0-9]+$ ]] && [ "$VMID" -ge 100 ] || fail "--vmid moet een getal vanaf 100 zijn"
	[[ "$NAME" =~ ^[a-zA-Z0-9][a-zA-Z0-9.-]*$ ]] || fail "--name mag alleen letters, cijfers, punten en streepjes bevatten"
	pvesm status --storage "$STORAGE" >/dev/null 2>&1 || fail "storage $STORAGE bestaat niet op deze host (zie pvesm status)"
	[ -e "/sys/class/net/$BRIDGE" ] || fail "bridge $BRIDGE bestaat niet op deze host"
	case "$(uname -m)" in
	x86_64 | amd64) ;;
	*) fail "alleen amd64 wordt ondersteund" ;;
	esac

	if pct status "$VMID" >/dev/null 2>&1; then
		pct config "$VMID" | grep -q '^template: 1' || fail "CT $VMID bestaat al en is geen template; kies een ander nummer met --vmid"
		[ "$REPLACE" = 1 ] || fail "template $VMID bestaat al; gebruik --replace om hem te vervangen"
	elif qm status "$VMID" >/dev/null 2>&1; then
		fail "VM $VMID bestaat al; kies een ander nummer met --vmid"
	fi

	if [ -z "$OSTEMPLATE" ]; then
		echo "==> Debian 13-containertemplate zoeken (pveam)"
		pveam update >/dev/null || echo "    pveam update mislukt; de lijst kan oud zijn"
		FILE="$(pveam available --section system | awk '{print $2}' | grep -E '^debian-13-standard_.*_amd64\.tar\.(zst|xz|gz)$' | sort -V | tail -n1 || true)"
		[ -n "$FILE" ] || fail "geen debian-13-standard in pveam available; geef er zelf een met --ostemplate"
		if ! pveam list "$TMPL_STORAGE" | awk '{print $1}' | grep -qx "$TMPL_STORAGE:vztmpl/$FILE"; then
			echo "    $FILE downloaden naar $TMPL_STORAGE"
			pveam download "$TMPL_STORAGE" "$FILE"
		fi
		OSTEMPLATE="$TMPL_STORAGE:vztmpl/$FILE"
	fi

	TMP="$(mktemp -d /var/tmp/clusterforge-ct.XXXXXX)"
	trap 'rm -rf "$TMP"' EXIT

	echo "==> cf-agent downloaden van $SERVER"
	curl -fsSL "$SERVER/downloads/cf-agent-linux-amd64" -o "$TMP/cf-agent"
	curl -fsSL "$SERVER/downloads/cf-agent-linux-amd64.sha256" -o "$TMP/cf-agent.sha256"
	[ "$(cut -d' ' -f1 "$TMP/cf-agent.sha256")" = "$(sha256sum "$TMP/cf-agent" | cut -d' ' -f1)" ] ||
		fail "checksum van cf-agent klopt niet"
	chmod 0755 "$TMP/cf-agent"
	AGENT_VERSION="$("$TMP/cf-agent" version)"

	if [ "$ALLOW_HTTP" = 1 ]; then
		printf '{"server": "%s", "allow_http": true}\n' "$SERVER" >"$TMP/claim.json"
	else
		printf '{"server": "%s"}\n' "$SERVER" >"$TMP/claim.json"
	fi

	cat >"$TMP/cf-agent.service" <<'UNIT'
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

	cat >"$TMP/cf-agent-fault-clear.service" <<'UNIT'
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

	# Elke kloon zijn eigen SSH-sleutels: de template heeft er geen meer.
	cat >"$TMP/cf-ssh-hostkeys.service" <<'UNIT'
[Unit]
Description=ClusterForge: eigen SSH-hostsleutels voor deze CT
ConditionPathExists=/usr/bin/ssh-keygen
ConditionPathExists=!/etc/ssh/ssh_host_ed25519_key
Before=ssh.service

[Service]
Type=oneshot
ExecStart=/usr/bin/ssh-keygen -A

[Install]
WantedBy=multi-user.target
UNIT

	if [ "$REPLACE" = 1 ] && pct status "$VMID" >/dev/null 2>&1; then
		echo "==> oude template $VMID verwijderen"
		pct destroy "$VMID" --purge
	fi

	echo "==> CT $VMID ($NAME) maken uit $OSTEMPLATE op $STORAGE"
	pct create "$VMID" "$OSTEMPLATE" --hostname "$NAME" --ostype debian --unprivileged 1 --features nesting=1 \
		--rootfs "$STORAGE:4" --cores 1 --memory 1024 --swap 512 \
		--net0 "name=eth0,bridge=$BRIDGE,ip=dhcp" --tags clusterforge \
		--description "CT-template voor ClusterForge: Debian 13 met cf-agent $AGENT_VERSION. Gemaakt op $(date -I) met ct-template.sh." >/dev/null

	echo "==> cf-agent $AGENT_VERSION erin zetten"
	pct start "$VMID"
	for _ in $(seq 1 30); do
		pct exec "$VMID" -- true 2>/dev/null && break
		sleep 1
	done
	pct exec "$VMID" -- install -d -m 0700 /etc/clusterforge
	pct push "$VMID" "$TMP/cf-agent" /usr/local/bin/cf-agent --perms 0755
	pct push "$VMID" "$TMP/claim.json" /etc/clusterforge/claim.json --perms 0600
	for unit in cf-agent.service cf-agent-fault-clear.service cf-ssh-hostkeys.service; do
		pct push "$VMID" "$TMP/$unit" "/etc/systemd/system/$unit" --perms 0644
	done
	pct exec "$VMID" -- systemctl daemon-reload
	pct exec "$VMID" -- systemctl enable cf-agent.service cf-agent-fault-clear.service cf-ssh-hostkeys.service
	# Een kloon krijgt een nieuwe machine-id en eigen SSH-sleutels; de
	# template is nooit aangemeld.
	pct exec "$VMID" -- sh -c 'rm -f /etc/clusterforge/agent.json /etc/clusterforge/enroll.json /var/lib/dbus/machine-id /etc/ssh/ssh_host_*; : >/etc/machine-id; apt-get clean'

	pct shutdown "$VMID" --timeout 60 || pct stop "$VMID"
	pct template "$VMID"

	cat <<EOF

Klaar: CT-template $VMID ($NAME) op $(hostname).
In ClusterForge: Proxmox, Synchroniseren, en dan Clusters, Cluster uitrollen;
kies daar deze CT-template en vaste adressen.
EOF
}

main "$@" </dev/null
