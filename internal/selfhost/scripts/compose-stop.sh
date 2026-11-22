# Verhuizen naar het cluster, laatste stap op de oude host: de oude server
# (en de scanner) stoppen, over tien seconden, zodat deze taak nog kan
# afronden. Ze starten niet meer vanzelf; de data en de dump blijven staan.
set -u
PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
export PATH
fail() { echo "FOUT: $*" >&2; exit 1; }
ids=$(docker ps --filter label=com.docker.compose.service=server --format '{{.ID}} {{.Image}}' | awk '$2 ~ /clusterforge-server/ {print $1}')
[ -n "$ids" ] || fail "geen draaiende servercontainer van ClusterForge op deze node"
proj=$(docker inspect -f '{{index .Config.Labels "com.docker.compose.project"}}' $ids | head -n 1)
scan=$(docker ps --filter "label=com.docker.compose.project=$proj" --filter label=com.docker.compose.service=scanner -q)
for c in $ids $scan; do docker update --restart=no "$c" >/dev/null || fail "docker update $c"; done
systemd-run --quiet --collect --unit "cf-oude-server-stoppen-$(date +%s)" --on-active=10 \
  docker stop -t 30 $ids $scan || fail "systemd-run"
echo "De oude server ($(echo $ids)) en scanner ($(echo ${scan:-geen})) stoppen over tien seconden en starten niet meer vanzelf."
echo "Terugweg: docker update --restart=unless-stopped $ids $scan && docker start $ids $scan (zet dan de agents terug)."
