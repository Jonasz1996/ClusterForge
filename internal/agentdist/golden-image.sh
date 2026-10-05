#!/bin/bash
# Maakt op een Proxmox-host een golden image voor ClusterForge: een
# VM-template met Debian 13, cloud-init, de QEMU guest agent en cf-agent.
# ClusterForge kloont hieruit de VM's van een nieuw cluster.
#
#   curl -fsSL https://clusterforge.example/install/golden-image.sh \
#     | bash -s -- --server https://clusterforge.example
#
# Draai het als root op een Proxmox-host. Het script installeert zo nodig
# libguestfs-tools (voor virt-customize) en verandert verder niets aan de host.
set -euo pipefail

SERVER=""
VMID=9000
STORAGE=local-lvm
BRIDGE=vmbr0
NAME=debian-13-clusterforge
IMAGE_URL=https://cloud.debian.org/images/cloud/trixie/latest/debian-13-genericcloud-amd64.qcow2
REPLACE=0

usage() {
	cat >&2 <<'EOF'
gebruik: golden-image.sh --server https://clusterforge.example [opties]

  --server URL     adres van ClusterForge; daar komt cf-agent vandaan
  --vmid N         nummer van de template (standaard 9000)
  --storage NAAM   storage voor de schijf (standaard local-lvm); met gedeelde
                   storage (Ceph, NFS) kan ClusterForge op elke host klonen
  --bridge NAAM    netwerkbridge (standaard vmbr0)
  --name NAAM      naam van de template (standaard debian-13-clusterforge)
  --image URL      cloud-image (standaard Debian 13 genericcloud amd64)
  --replace        een bestaande template met dit nummer vervangen
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
		-h | --help) usage ;;
		esac
		[ $# -ge 2 ] || usage
		case "$1" in
		--server) SERVER="$2" ;;
		--vmid) VMID="$2" ;;
		--storage) STORAGE="$2" ;;
		--bridge) BRIDGE="$2" ;;
		--name) NAME="$2" ;;
		--image) IMAGE_URL="$2" ;;
		*) echo "onbekende optie: $1" >&2; usage ;;
		esac
		shift 2
	done

	[ -n "$SERVER" ] || usage
	SERVER="${SERVER%/}"
	[ "$(id -u)" = 0 ] || fail "draai dit script als root op een Proxmox-host"
	command -v qm >/dev/null 2>&1 || fail "qm ontbreekt; dit script hoort op een Proxmox-host"
	[[ "$VMID" =~ ^[0-9]+$ ]] && [ "$VMID" -ge 100 ] || fail "--vmid moet een getal vanaf 100 zijn"
	[[ "$NAME" =~ ^[a-zA-Z0-9][a-zA-Z0-9.-]*$ ]] || fail "--name mag alleen letters, cijfers, punten en streepjes bevatten"
	pvesm status --storage "$STORAGE" >/dev/null 2>&1 || fail "storage $STORAGE bestaat niet op deze host (zie pvesm status)"
	[ -e "/sys/class/net/$BRIDGE" ] || fail "bridge $BRIDGE bestaat niet op deze host"
	case "$(uname -m)" in
	x86_64 | amd64) ;;
	*) fail "alleen amd64 wordt ondersteund" ;;
	esac

	if qm status "$VMID" >/dev/null 2>&1; then
		if ! qm config "$VMID" | grep -q '^template: 1'; then
			fail "VM $VMID bestaat al en is geen template; kies een ander nummer met --vmid"
		fi
		[ "$REPLACE" = 1 ] || fail "template $VMID bestaat al; gebruik --replace om hem te vervangen"
	fi

	if ! command -v virt-customize >/dev/null 2>&1; then
		echo "==> libguestfs-tools installeren (voor virt-customize)"
		apt-get update -q
		DEBIAN_FRONTEND=noninteractive apt-get install -y -q libguestfs-tools
	fi

	TMP="$(mktemp -d /var/tmp/clusterforge-image.XXXXXX)"
	trap 'rm -rf "$TMP"' EXIT
	IMG="$TMP/$(basename "$IMAGE_URL")"

	echo "==> cloud-image downloaden: $IMAGE_URL"
	curl -fL --progress-bar "$IMAGE_URL" -o "$IMG"
	if curl -fsSL "$(dirname "$IMAGE_URL")/SHA512SUMS" -o "$TMP/SHA512SUMS" 2>/dev/null &&
		grep -q " $(basename "$IMAGE_URL")\$" "$TMP/SHA512SUMS"; then
		(cd "$TMP" && grep " $(basename "$IMAGE_URL")\$" SHA512SUMS | sha512sum -c --quiet -) ||
			fail "checksum van het cloud-image klopt niet"
		echo "    checksum klopt"
	else
		echo "    geen SHA512SUMS gevonden naast het image; checksum niet gecontroleerd"
	fi

	echo "==> cf-agent downloaden van $SERVER"
	curl -fsSL "$SERVER/downloads/cf-agent-linux-amd64" -o "$TMP/cf-agent"
	curl -fsSL "$SERVER/downloads/cf-agent-linux-amd64.sha256" -o "$TMP/cf-agent.sha256"
	[ "$(cut -d' ' -f1 "$TMP/cf-agent.sha256")" = "$(sha256sum "$TMP/cf-agent" | cut -d' ' -f1)" ] ||
		fail "checksum van cf-agent klopt niet"
	chmod 0755 "$TMP/cf-agent"
	AGENT_VERSION="$("$TMP/cf-agent" version)"

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

	echo "==> image aanpassen: qemu-guest-agent en cf-agent $AGENT_VERSION erin"
	# De guest agent start vanzelf zodra Proxmox zijn apparaat aanbiedt; cf-agent
	# wacht na het opstarten op /etc/clusterforge/enroll.json.
	virt-customize -q -a "$IMG" \
		--install qemu-guest-agent \
		--mkdir /etc/clusterforge \
		--chmod 0700:/etc/clusterforge \
		--upload "$TMP/cf-agent:/usr/local/bin/cf-agent" \
		--chmod 0755:/usr/local/bin/cf-agent \
		--upload "$TMP/cf-agent.service:/etc/systemd/system/cf-agent.service" \
		--run-command 'systemctl enable cf-agent.service' \
		--run-command 'rm -f /etc/clusterforge/agent.json /etc/clusterforge/enroll.json /var/lib/dbus/machine-id' \
		--truncate /etc/machine-id

	if [ "$REPLACE" = 1 ] && qm status "$VMID" >/dev/null 2>&1; then
		echo "==> oude template $VMID verwijderen"
		qm destroy "$VMID" --purge
	fi

	echo "==> template $VMID ($NAME) maken op $STORAGE"
	qm create "$VMID" --name "$NAME" --ostype l26 --memory 2048 --cores 2 \
		--net0 "virtio,bridge=$BRIDGE" --scsihw virtio-scsi-single --agent enabled=1 \
		--serial0 socket --vga serial0 --tags clusterforge \
		--description "Golden image voor ClusterForge: Debian 13 met cloud-init, qemu-guest-agent en cf-agent $AGENT_VERSION. Gemaakt op $(date -I) met golden-image.sh."
	qm set "$VMID" --scsi0 "$STORAGE:0,import-from=$IMG,discard=on" >/dev/null
	qm set "$VMID" --ide2 "$STORAGE:cloudinit" --boot order=scsi0 --ipconfig0 ip=dhcp >/dev/null
	qm template "$VMID"

	cat <<EOF

Klaar: VM-template $VMID ($NAME) op $(hostname).
In ClusterForge: Proxmox, Synchroniseren, en dan Clusters, Cluster uitrollen;
kies daar deze golden image.
EOF
}

main "$@" </dev/null
