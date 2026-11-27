#!/bin/sh
# vzdump-hook voor de nodes van het mediaplatform (template media-platform van
# ClusterForge). Op elke Proxmox-host met zo'n node, als root:
#
#   install -m 0755 vzdump-media-platform.sh /usr/local/bin/vzdump-media-platform.sh
#   echo 'script: /usr/local/bin/vzdump-media-platform.sh' >> /etc/vzdump.conf
#
# (staat er al een "script:" in /etc/vzdump.conf, roep dit script dan vanuit
# dat script aan met dezelfde argumenten). Een back-upjob kan het ook zelf
# meekrijgen: Datacenter, Backup, de job, Advanced, of `--script` bij vzdump.
#
# Waarom: in de node staat de bestanden-mount van SeaweedFS (FUSE) op
# /srv/platform-data. vzdump bevriest een CT voor een snapshot (en in
# suspend-modus), en een FUSE-mount in de CT laat dat vastlopen; de host mag
# er ook niet in (Permission denied). Vóór de back-up zet de node zijn mount
# los (en geeft hij het VIP af als hij het had), daarna zet hij hem terug.
# De bestanden zelf staan op SeaweedFS op elke node en horen niet in deze
# back-up. Twee nodes tegelijk: de tweede wacht (hoogstens 2 uur) tot de
# eerste klaar is, zodat het platform blijft draaien.
#
# Andere gasten (VM's, CT's zonder platform-ctl.sh) laat het met rust. Lukt
# het loszetten niet, dan stopt de back-up van deze ene CT met een fout in
# plaats van vast te lopen.
fase=${1:-}
mode=${2:-}
vmid=${3:-}
case "$fase" in backup-start | backup-end | backup-abort) ;; *) exit 0 ;; esac
[ "${VMTYPE:-}" = lxc ] || exit 0
# In stop-modus staat de CT stil tijdens de back-up: niets te doen.
[ "$mode" = stop ] && exit 0
case "$vmid" in '' | *[!0-9]*) exit 0 ;; esac
CTL=/usr/local/lib/clusterforge/platform-ctl.sh
pct status "$vmid" 2>/dev/null | grep -q 'status: running' || exit 0
pct exec "$vmid" -- test -x "$CTL" 2>/dev/null || exit 0
case "$fase" in
  backup-start)
    echo "mediaplatform: de mount van CT $vmid los voor de back-up"
    exec pct exec "$vmid" -- "$CTL" backup-pauze 7200
    ;;
  *)
    echo "mediaplatform: de mount van CT $vmid terug"
    # Een fout hier laat de back-up zelf niet mislukken; de CT zet de mount
    # na 6 uur toch terug.
    pct exec "$vmid" -- "$CTL" backup-hervat || echo "mediaplatform: terugzetten mislukte; in CT $vmid: $CTL backup-hervat" >&2
    exit 0
    ;;
esac
