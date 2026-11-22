# Verhuizen naar het cluster, stap 1 op de host van ClusterForge: een dump
# van de database in de exportmap, die de server (alleen lezen) aan de
# nieuwe VIP-node geeft met een eenmalige sleutel.
#
# Vooraf gezet door de server: JOB (id van de taak).
set -u
PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
export PATH
say() { echo "== $*"; }
fail() { echo "FOUT: $*" >&2; exit 1; }

found=$(docker ps --filter label=com.docker.compose.service=server \
  --format '{{.ID}}|{{.Image}}|{{.Label "com.docker.compose.project"}}|{{.Label "com.docker.compose.project.working_dir"}}' |
  grep 'clusterforge-server' || true)
[ -n "$found" ] || fail "geen draaiende servercontainer van ClusterForge op deze node; kies de juiste node"
[ "$(echo "$found" | wc -l)" = 1 ] || fail "meer dan één servercontainer van ClusterForge op deze node"
CID=$(echo "$found" | cut -d'|' -f1)
PROJECT=$(echo "$found" | cut -d'|' -f3)
DIR=$(echo "$found" | cut -d'|' -f4)
CFILE=$DIR/docker-compose.yml
[ -f "$CFILE" ] || CFILE=$DIR/compose.yaml
compose() { docker compose -p "$PROJECT" --project-directory "$DIR" -f "$CFILE" "$@"; }

EXP=$(docker inspect -f '{{range .Mounts}}{{if eq .Destination "/var/lib/clusterforge/export"}}{{.Source}}{{end}}{{end}}' "$CID")
[ -n "$EXP" ] || fail "de server heeft geen exportmap (/var/lib/clusterforge/export); werk ClusterForge eerst bij naar deze versie met het nieuwe compose-bestand"
mkdir -p "$EXP" || fail "map $EXP"
chmod 0711 "$EXP"
rm -f "$EXP"/clusterforge-*.dump "$EXP"/clusterforge-*.dump.tmp

say "Dump van de database"
f=$EXP/clusterforge-$JOB.dump
(umask 077 && compose exec -T postgres pg_dump -U clusterforge -Fc clusterforge > "$f.tmp") || fail "pg_dump"
test -s "$f.tmp" || fail "de dump is leeg"
# De server draait als 65532 (nonroot) en leest de dump alleen.
chown 65532:65532 "$f.tmp" && chmod 0400 "$f.tmp" && mv "$f.tmp" "$f" || fail "rechten op de dump"
echo "  $f ($(du -h "$f" | cut -f1))"
echo "sha256=$(sha256sum "$f" | cut -d' ' -f1)"
echo "schema=$(compose exec -T postgres psql -X -q -At -U clusterforge -d clusterforge -c 'SELECT coalesce(max(version_id), 0) FROM goose_db_version WHERE is_applied' | tr -d '[:space:]')"
