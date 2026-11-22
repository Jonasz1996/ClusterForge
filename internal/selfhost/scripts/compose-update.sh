# ClusterForge werkt zichzelf bij (één server met docker compose).
#
# Draait als run van de agent op de host van ClusterForge, dus los van de
# server: die herstart halverwege, deze run loopt door en de taak leest na
# de herstart verder. Stappen: image halen, back-up van de database en .env,
# compose-bestand van de nieuwe versie, CF_IMAGE in .env, docker compose up,
# wachten tot de server gezond is met de nieuwe versie. Lukt dat niet, dan
# gaat alles terug: .env, compose en, als er migraties liepen, de database
# uit de back-up van daarnet.
#
# Vooraf gezet door de server: IMAGE (het nieuwe image), WAIT (seconden),
# KEEP (hoeveel back-ups van updates blijven staan) en DRY (1 = alleen
# controleren).
set -u
PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
export PATH

say() { echo "== $*"; }
warn() { echo "WAARSCHUWING: $*"; }
fail() { echo "FOUT: $*" >&2; exit 1; }

command -v docker >/dev/null 2>&1 || fail "docker ontbreekt op deze node; is dit de host van ClusterForge?"
docker compose version >/dev/null 2>&1 || fail "docker compose ontbreekt op deze node"

# De servercontainer van ClusterForge: service server, image clusterforge-server.
found=$(docker ps -a --filter label=com.docker.compose.service=server \
  --format '{{.ID}}|{{.Image}}|{{.Label "com.docker.compose.project"}}|{{.Label "com.docker.compose.project.working_dir"}}' |
  grep 'clusterforge-server' || true)
[ -n "$found" ] || fail "geen container van clusterforge-server (compose-service server) op deze node; kies de juiste node"
[ "$(echo "$found" | wc -l)" = 1 ] || fail "meer dan één servercontainer van ClusterForge op deze node: $(echo "$found" | cut -d'|' -f1 | tr '\n' ' ')"
CID=$(echo "$found" | cut -d'|' -f1)
PROJECT=$(echo "$found" | cut -d'|' -f3)
DIR=$(echo "$found" | cut -d'|' -f4)
ENVF=$DIR/.env
[ -d "$DIR" ] && [ -f "$ENVF" ] || fail "map $DIR met .env niet gevonden"
CFILE=$DIR/docker-compose.yml
[ -f "$CFILE" ] || CFILE=$DIR/compose.yaml
[ -f "$CFILE" ] || fail "geen docker-compose.yml in $DIR"
compose() { docker compose -p "$PROJECT" --project-directory "$DIR" -f "$CFILE" "$@"; }

envval() { sed -n "s/^$1=//p" "$ENVF" | tail -n 1 | sed "s/^['\"]//; s/['\"]\$//"; }
OLDIMAGE=$(envval CF_IMAGE)
OLDVER=$(docker exec "$CID" /usr/local/bin/clusterforge-server version 2>/dev/null || echo onbekend)
BACKUPS=${BACKUPS:-/root/cf-backup}
TS=$(date +%Y%m%d-%H%M%S)

say "ClusterForge in $DIR (project $PROJECT)"
echo "  nu:    ${OLDIMAGE:-zelf gebouwd image} (versie $OLDVER)"
echo "  nieuw: $IMAGE"

# Welke services horen erbij (met de profielen uit .env, zoals scanner)?
SERVICES=$(compose config --services 2>/dev/null) || fail "docker compose config faalt in $DIR"
has_service() { echo "$SERVICES" | grep -qx "$1"; }

say "Image halen"
docker pull -q "$IMAGE" || fail "docker pull $IMAGE (bestaat de tag? is ghcr.io bereikbaar?)"
if has_service scanner; then
  docker pull -q "$IMAGE-scanner" || fail "docker pull $IMAGE-scanner"
fi
NEWID=$(docker image inspect -f '{{.Id}}' "$IMAGE") || fail "image $IMAGE niet gevonden na pull"
NEWVER=$(docker run --rm --entrypoint /usr/local/bin/clusterforge-server "$IMAGE" version 2>/dev/null || echo onbekend)
echo "  versie in het image: $NEWVER"

if [ "${DRY:-0}" = 1 ]; then
  echo "Alleen gecontroleerd: het image staat klaar, er is niets veranderd."
  exit 0
fi

pgq() { compose exec -T postgres psql -X -q -At -U clusterforge -d "$1" -v ON_ERROR_STOP=1 -c "$2"; }
schema() { pgq clusterforge "SELECT coalesce(max(version_id), 0) FROM goose_db_version WHERE is_applied" 2>/dev/null | tr -d '[:space:]'; }

say "Back-up van de database en .env"
mkdir -p "$BACKUPS" && chmod 0700 "$BACKUPS" || fail "map $BACKUPS"
DUMP=$BACKUPS/cf-$TS-voor-update.dump
(umask 077 && compose exec -T postgres pg_dump -U clusterforge -Fc clusterforge > "$DUMP") || fail "pg_dump (er is niets veranderd)"
test -s "$DUMP" || fail "de back-up $DUMP is leeg (er is niets veranderd)"
(umask 077 && cp -p "$ENVF" "$BACKUPS/env-$TS-voor-update") || fail "kopie van .env"
cp -p "$CFILE" "$BACKUPS/compose-$TS-voor-update.yml" 2>/dev/null
echo "  $DUMP ($(du -h "$DUMP" | cut -f1))"
SCHEMA_BEFORE=$(schema)
echo "  databaseversie: ${SCHEMA_BEFORE:-onbekend}"

# Het compose-bestand van de nieuwe versie: uit git als $DIR een checkout is
# en de commit van de tag te halen is, anders uit het image.
OLDHEAD=
REPO=$(git -C "$DIR" rev-parse --show-toplevel 2>/dev/null || true)
TAG=${IMAGE##*:}
compose_from_image() {
  tmp=$(docker create "$IMAGE" 2>/dev/null) || return 1
  ok=1
  if docker cp "$tmp:/usr/share/clusterforge/deploy/docker-compose.yml" "$CFILE.nieuw" >/dev/null 2>&1 && [ -s "$CFILE.nieuw" ]; then
    if cmp -s "$CFILE.nieuw" "$CFILE"; then
      echo "  compose-bestand ongewijzigd"
      rm -f "$CFILE.nieuw"
    else
      mv "$CFILE.nieuw" "$CFILE" && echo "  compose-bestand uit het image gezet (het oude staat in $BACKUPS)" && ok=0
    fi
  else
    rm -f "$CFILE.nieuw"
    echo "  het image heeft geen compose-bestand; het huidige blijft"
  fi
  docker rm "$tmp" >/dev/null 2>&1
  [ $ok = 0 ] && COMPOSE_CHANGED=1
  return 0
}
COMPOSE_CHANGED=0
say "Compose-bestand van de nieuwe versie"
if [ -n "$REPO" ] && echo "$TAG" | grep -Eq '^[0-9a-f]{7,40}$'; then
  if [ -n "$(git -C "$REPO" status --porcelain --untracked-files=no 2>/dev/null)" ]; then
    warn "$REPO heeft lokale wijzigingen; git blijft zoals het is"
    compose_from_image
  elif timeout 120 git -C "$REPO" fetch -q origin 2>/dev/null && git -C "$REPO" cat-file -e "$TAG^{commit}" 2>/dev/null; then
    OLDHEAD=$(git -C "$REPO" rev-parse HEAD)
    git -C "$REPO" checkout -q -B "cf-$TAG" "$TAG" || fail "git checkout $TAG"
    echo "  git: $REPO op commit $TAG (was $(echo "$OLDHEAD" | cut -c1-7))"
  else
    echo "  commit $TAG niet via git te halen (geen toegang tot de repository?); compose uit het image"
    compose_from_image
  fi
else
  compose_from_image
fi

# CF_IMAGE in .env zetten.
set_image() {
  if grep -q '^CF_IMAGE=' "$ENVF"; then
    sed -i "s|^CF_IMAGE=.*|CF_IMAGE=$1|" "$ENVF"
  else
    printf '\nCF_IMAGE=%s\n' "$1" >> "$ENVF"
  fi
}

restore_files() {
  cp -p "$BACKUPS/env-$TS-voor-update" "$ENVF"
  if [ -n "$OLDHEAD" ]; then
    git -C "$REPO" checkout -q --detach "$OLDHEAD" && echo "  git terug op $(echo "$OLDHEAD" | cut -c1-7)"
  elif [ "$COMPOSE_CHANGED" = 1 ]; then
    cp -p "$BACKUPS/compose-$TS-voor-update.yml" "$CFILE"
  fi
}

server_id() { compose ps -q server 2>/dev/null | head -n 1; }
# Gezond: de container is healthy, draait het verwachte image en de server
# antwoordt. Geeft 0 als dat binnen WAIT seconden lukt.
wait_healthy() {
  want=$1
  end=$(($(date +%s) + WAIT))
  last=
  while [ "$(date +%s)" -lt "$end" ]; do
    id=$(server_id)
    if [ -n "$id" ]; then
      img=$(docker inspect -f '{{.Image}}' "$id" 2>/dev/null)
      st=$(docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}' "$id" 2>/dev/null)
      if [ "$st" != "$last" ]; then
        echo "  server: $st"
        last=$st
      fi
      if [ "$st" = healthy ] && { [ -z "$want" ] || [ "$img" = "$want" ]; }; then
        return 0
      fi
    fi
    sleep 5
  done
  return 1
}

say "Nieuwe versie starten"
set_image "$IMAGE"
if ! compose up -d --no-build; then
  warn "docker compose up faalde"
  UPFAILED=1
else
  UPFAILED=0
fi

if [ "$UPFAILED" = 0 ] && wait_healthy "$NEWID"; then
  ver=$(docker exec "$(server_id)" /usr/local/bin/clusterforge-server version 2>/dev/null || echo "$NEWVER")
  say "Bijgewerkt"
  echo "  ClusterForge draait nu versie $ver ($IMAGE); databaseversie $(schema)."
  echo "  Terugweg: kies bij Instellingen → ClusterForge zelf de vorige versie, of zet de back-up terug ($DUMP)."
  # Oude back-ups van updates opruimen, de laatste $KEEP blijven.
  ls -1t "$BACKUPS"/cf-*-voor-update.dump 2>/dev/null | tail -n +$((KEEP + 1)) | while read -r f; do
    rm -f "$f"
    b=$(basename "$f" .dump)
    t=${b#cf-}
    t=${t%-voor-update}
    rm -f "$BACKUPS/env-$t-voor-update" "$BACKUPS/compose-$t-voor-update.yml"
  done
  exit 0
fi

# Terugdraaien.
id=$(server_id)
[ -n "$id" ] && docker logs --tail 40 "$id" 2>&1 | sed 's/^/  | /'
say "De nieuwe versie werd niet gezond binnen ${WAIT}s: terugdraaien naar ${OLDIMAGE:-het vorige image}"
restore_files
SCHEMA_NOW=$(schema)
if [ -n "$SCHEMA_BEFORE" ] && [ "$SCHEMA_NOW" != "$SCHEMA_BEFORE" ]; then
  echo "  de nieuwe versie voerde migraties uit ($SCHEMA_BEFORE → $SCHEMA_NOW): database terugzetten uit $DUMP"
  compose stop server >/dev/null 2>&1
  has_service scanner && compose stop scanner >/dev/null 2>&1
  pgq postgres "DROP DATABASE IF EXISTS clusterforge WITH (FORCE)" >/dev/null &&
    pgq postgres "CREATE DATABASE clusterforge OWNER clusterforge" >/dev/null ||
    fail "database leegmaken mislukte; zet hem met de hand terug: docker compose exec -T postgres pg_restore -U clusterforge -d clusterforge < $DUMP"
  compose exec -T postgres pg_restore -U clusterforge -d clusterforge --no-owner < "$DUMP" ||
    warn "pg_restore meldde fouten; kijk na of ClusterForge alles terug heeft"
fi
compose up -d --no-build || warn "docker compose up met de vorige versie faalde"
OLDID=
[ -n "$OLDIMAGE" ] && OLDID=$(docker image inspect -f '{{.Id}}' "$OLDIMAGE" 2>/dev/null)
if wait_healthy "$OLDID"; then
  fail "bijwerken naar $IMAGE mislukte; ClusterForge draait weer op ${OLDIMAGE:-het vorige image} (versie $OLDVER). Back-up: $DUMP"
fi
fail "bijwerken mislukte en de vorige versie wordt niet gezond; kijk met docker compose ps en docker compose logs server in $DIR. Back-up: $DUMP"
