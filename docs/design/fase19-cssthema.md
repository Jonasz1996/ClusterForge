# Fase 19: cssthema als HA-cluster

Gevraagd op 2026-11-16. cssthema (github.com/Jonasz1996/cssthema) is de
centrale CSS-themabouwer achter Nginx Proxy Manager. Hij draait nu op één
Debian 13-CT (`jbogaertcssthema`, 192.168.114.154, VLAN 114) zonder Docker,
geïnstalleerd met `deploy/debian/install.sh`: code in `/opt/cssthema`, venv
en Node in `/usr/local/lib/cssthema`, `cssthema-api` (FastAPI) en
`cssthema-worker` (arq), nginx op 80, Redis en een lokale PostgreSQL 17.
Doel: drie nodes (de bestaande wordt node 1), overname **zonder verlies**.

## Wat cssthema bewaart

- **PostgreSQL** `cssthema`: thema's, versies, paletten, hosts, services,
  metadata van de opslag. Migratie 0001 maakt de extensie `pg_trgm`.
- **`/var/lib/cssthema/css-files`**: handgemaakte CSS en thema-scripts
  (`.js`), die nginx rechtstreeks serveert.
- **`/var/lib/cssthema/storage`**: bestanden per sha256 (vastgelegde
  pagina's van Services); de rij staat in de database.
- **`/etc/cssthema/cssthema.env`**: `SECRET_KEY`, `ENCRYPTION_KEY`,
  `DASHBOARD_HOST`, `PUBLIC_BASE_URL`, `TRUSTED_PROXIES`, `EXCLUDED_HOSTS`.
- **Redis**: alleen cache en de wachtrij van de worker.

## Opbouw

- Template `cssthema` 1.0.0, rol `node`, 3 of 5 nodes. De bestaande node is
  node 1; de andere zijn nieuwe CT's.
- **PostgreSQL met Patroni** op alle nodes (`database: engine: postgresql`),
  eigen VIP `db_vip` op de primary. ClusterForge maakt gebruiker en database
  `cssthema` (eigenaar; `pg_trgm` is trusted, dus de eigenaar mag hem maken).
- **cssthema** in dezelfde mappen als `install.sh` (`/opt/cssthema`,
  `/usr/local/lib/cssthema/venv`, `.../node`), gebouwd door
  `cssthema-ctl.sh bouwen` op de gekozen versie (commit, tag of branch), met
  `uv sync --frozen` en `pnpm`/`vite` zoals `install.sh`. De nginx-site is
  die van `install.sh`. Nooit meer `install.sh` op een clusternode draaien:
  bijwerken gaat via ClusterForge (Wijzigen → Versie).
- **Units**: `cf-cssthema-api` op elke node (enabled, `Restart=always`,
  `ExecStartPre=+cssthema-ctl.sh voorstart`, daarna `alembic upgrade head`;
  de migraties van cssthema nemen zelf een advisory lock).
  `cf-cssthema-worker` alleen op de VIP-node: keepalived start hem
  (`master`) en stopt hem (`vip-weg`); `ExecCondition` laat hem alleen op de
  VIP-node na de overname starten. Zo draait de dagelijkse hostcontrole één
  keer.
- **Configuratie**: `voorstart` schrijft `/etc/cf-cssthema/cssthema.env`
  (0640 root:cssthema) uit `base.env` van de template (database via
  `db_vip`, Redis lokaal) en, na een overname, de instellingen van de oude
  node (`/opt/cf-cssthema/files/.cf/cssthema.env`, 0600). De overgenomen
  waarden winnen, behalve `POSTGRES_*`, `REDIS_URL` en `BACKUP_*`. Ook de
  twee conf.d-bestanden van nginx (`TRUSTED_PROXIES`, `DASHBOARD_HOST`).
- **Syncs** (elke minuut, van de VIP-node, zonder `--delete`):
  `cssthema-instellingen` (`/opt/cf-cssthema/files/`) en
  `cssthema-bestanden` (`/var/lib/cssthema/`, zonder
  `geinstalleerde-commit` en `laatste-backup`).
- **Keepalived** met `nopreempt` en `cssthema-ctl.sh check` (nginx +
  `/readyz`) zonder gewicht: een ongezonde node geeft het VIP af, een gezonde
  VIP-node houdt het. Terugspringen zou het VIP naar een node brengen die de
  laatste bestanden nog niet heeft. Een nieuwe VIP-node leegt eerst Redis en
  de microcache van nginx.
- **ufw**: als ufw actief is, regels voor etcd, Patroni, PostgreSQL, SSH
  (syncs) en VRRP tussen de nodes.
- **Back-up**: `cf-cssthema-backup.timer` elke nacht; alleen de node met het
  database-VIP maakt `pg_dump -Fc`, `css-files.tar.gz`, `storage.tar.gz`,
  de instellingen en de commit in `/var/backups/cf-cssthema` (0700),
  laatste 14.

Venster: een bestand dat in de laatste minuut vóór een onverwachte uitval
van de VIP-node gemaakt werd, staat nog niet op de andere nodes.
`cssthema-ctl.sh status` telt de bestanden per node.

## Markering en veiligheid

`COMMENT ON DATABASE cssthema` = `cf-cssthema key=<sha256[:16] van
SECRET_KEY> ready=<tijd> …`. `cf-cssthema-api` start alleen met de
markering en dezelfde sleutel, en nooit zolang de oude `cssthema-api` op
die node enabled is. Zonder markering wacht elke node (`modus overnemen`,
standaard) of neemt alleen node 1 een lege database in gebruik (`nieuw`).

## Node 1: de bestaande installatie

- Uitrolstap `voorbereiden` (alleen met `/etc/systemd/system/cssthema-api.service`):
  gecontroleerde `pg_dump` naar `/var/backups/cssthema/cf-voorbereiden-*`,
  dan `pg_renamecluster <v> main cssthemaoud` op poort 5433,
  `POSTGRES_PORT=5433` in de oude env, een drop-in `PGPORT=5433` voor
  `cssthema-backup`, een regel in `user_clusters` (psql zonder poort → 5432,
  Patroni), cssthema weer starten en wachten op `/readyz`. Mislukt iets, dan
  gaat alles terug. Patroni ziet `cssthemaoud` als opzijgezet cluster.
- `bouwen` weigert andere code dan de draaiende commit zolang de oude
  cssthema draait. Tot de overname bedient de oude cssthema het VIP.
- `cssthema-ctl.sh overnemen [--andere-versie]` op node 1:
  1. Controles: VIP hier, database van het cluster leeg en zonder
     markering, commit en migratie van de oude database.
  2. Oude `cssthema-api` en `-worker` stoppen en uitschakelen. Vanaf hier
     draait elke fout alles terug.
  3. Telling per tabel (aantal + md5 tot 64 MB) in de oude database.
  4. `pg_dump -Fc` naar `/var/backups/cssthema/cf-overname-*.dump`,
     gecontroleerd met `pg_restore --list`, plus de env.
  5. `pg_restore --single-transaction --exit-on-error --no-owner` in het
     cluster, zonder `COMMENT ON EXTENSION` (alleen voor een superuser).
  6. Telling in het cluster: alles moet gelijk zijn.
  7. Instellingen overnemen, markering, `cf-cssthema-api` starten, wachten
     op `/readyz`, worker starten, oude back-uptimer uit.
- `afronden`: PostgreSQL `cssthemaoud` stoppen en niet meer vanzelf starten
  (data blijft).
- `terug`: dump van het cluster, markering weg, `cf-cssthema-api` uit,
  `cssthemaoud` en de oude units weer aan. De bestanden zijn dezelfde map.

## Mijlpalen

- M1: ontwerp + template, overname en terugweg, tests (render, shellcheck,
  overname/terugdraaien/terug tegen een echte PostgreSQL met `pg_trgm`).
- M2: na het eerste gebruik bij Jonas: verbeteringen uit de praktijk.

Geen agent- of migratiewijziging (geen protocol- of migratienummer).
`cf-cssthema-api` en `-worker` komen bij de bewaakte units, cssthema in de
firewallcatalogus, `cssthemaoud` bij de opzijgezette PostgreSQL-clusters.
