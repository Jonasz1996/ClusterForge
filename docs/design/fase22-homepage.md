# Fase 22: de homepage als HA-cluster

Gevraagd op 2026-11-16. De homepage (github.com/Jonasz1996/homepage) is het
eigen dashboard van Jonas: tegels, monitoring (pings, http, push, back-ups,
Proxmox, Zabbix), meldingen, een syslog-ontvanger en SSH-werkvensters. Ze
draait nu op één Debian-CT (`jbogaerthomepage`, CT 156) zonder Docker,
geïnstalleerd met `deploy/install.sh`: code in `/opt/homepage`, een venv per
`requirements.lock` (`.venv` → `.venv-<hash>`), de frontend in
`frontend/dist`, `homepage-api` (FastAPI), `homepage-worker` (alle checks),
`homepage-syslog` (udp/tcp 514), nginx op 80 (443 met een eigen CA), een
lokale PostgreSQL met TimescaleDB, en `homepage-backup.timer` /
`homepage-backuptest.timer`. Doel: drie nodes (de bestaande wordt node 1),
overname **zonder verlies**, en **maar één node doet de checks**.

## Wat de homepage bewaart

- **PostgreSQL** `homepage`: alles (tegels, checks, resultaten, metingen,
  logregels, meldingen, versleutelde geheimen). Met TimescaleDB zijn
  `check_results`, `metrics`, `log_entries` en `check_hourly` hypertables
  met compressie en een bewaartermijn (taken van TimescaleDB).
- **`/etc/homepage/secret.key`**: de Fernet-sleutel voor alle opgeslagen
  wachtwoorden, API-sleutels en TOTP. Zonder dezelfde sleutel is de
  database onbruikbaar.
- **`/etc/homepage/homepage.env`**: o.a. `HOMEPAGE_NPM_IP`,
  `HOMEPAGE_LAN_OPEN`, `HOMEPAGE_LAN_NETS`, `HOMEPAGE_SITE_NAME`,
  `HOMEPAGE_SYSLOG_TARGET`.
- **`/var/backups/homepage`**: de dagelijkse dump (zonder logregels), die
  de worker nakijkt.
- Alle diensten draaien met `ProtectSystem=strict`: verder schrijft de
  homepage niets op schijf (behalve `/mnt/homepage-backup`, een kopie
  buiten de container, als die gemount is).

## Opbouw

- Template `homepage` 1.0.0, rol `node`, 3 of 5 nodes. De bestaande node is
  node 1; de andere zijn nieuwe CT's.
- **PostgreSQL met Patroni** op alle nodes, eigen VIP `db_vip`, met
  **TimescaleDB**: de template installeert de pakketbron en het pakket van
  Timescale (zoals `install.sh`) vóór Patroni, en zet
  `shared_preload_libraries: timescaledb` in de instellingen van de
  database. Daarvoor kreeg `postgresql-patroni` een nieuwe parameter
  `shared_preload_libraries` (optioneel, alleen `timescaledb`; leeg rendert
  `patroni.yml` zoals voorheen, dus geen drift op bestaande clusters). Ook
  achteraf te zetten via Databases → Instellingen (herstart node voor node).
- **De homepage** in dezelfde mappen als `install.sh` (`/opt/homepage`,
  `.venv-<hash>`, `frontend/dist`), gebouwd door `homepage-ctl.sh bouwen` op
  de gekozen versie (commit, tag of branch). Op node 1 is de oude homepage
  al gebouwd: zolang ze draait, weigert `bouwen` andere code en bouwt het
  niets opnieuw. Nooit meer `install.sh`/`bootstrap.sh` op een clusternode:
  bijwerken gaat via ClusterForge (Wijzigen → Versie).
- **Units**:
  - `cf-homepage-api` op elke node (enabled, `Restart=always`):
    `ExecStartPre=+homepage-ctl.sh voorstart` (markering en sleutel,
    configuratie, nginx), dan `homepage-ctl.sh migreren`: `alembic upgrade
    head` onder een advisory lock in de database (de migraties van de
    homepage nemen zelf geen slot).
  - `cf-homepage-syslog` op elke node, pas na de overname (tot dan houdt de
    oude homepage poort 514 vast). De berichten komen op het VIP binnen;
    `HOMEPAGE_SYSLOG_TARGET` wordt het VIP.
  - `cf-homepage-worker`: **de checks, op precies één node** (zie onder).
  - `cf-homepage-backup.timer` (03:30): `deploy/homepage-backup.sh` van de
    homepage zelf, als postgres met `PG*` naar het database-VIP, alleen op
    de node met de checks; de worker kijkt de dump daar na. Een sync brengt
    de dumps elke nacht naar de andere nodes.
  - `cf-homepage-volg.timer` (elke 30 s): vangnet naast keepalived.
- **Configuratie**: `voorstart` schrijft `/etc/cf-homepage/homepage.env`
  (0640 root:homepage): de instellingen van de oude node
  (`/opt/cf-homepage/files/.cf/homepage.env`, 0600), behalve database,
  paden, syslog-doel en verbindingen per proces, met daarover de database
  van het cluster (`HOMEPAGE_DATABASE_URL` naar `db_vip`, `PG*` voor psql
  en pg_dump) en `HOMEPAGE_SECRET_KEY_FILE=/etc/cf-homepage/secret.key`.
  Verbindingen: api 15 + syslog 5 per node, worker 40 op één node: 100 van
  de 200.
- **nginx**: dezelfde site, map en headers als `install.sh`
  (`deploy/nginx.conf`, `nginx-map.conf`), met `HOMEPAGE_NPM_IP`,
  `HOMEPAGE_LAN_OPEN` en `HOMEPAGE_LAN_NETS` van de oude node. **Alleen
  http op poort 80**: Nginx Proxy Manager wijst naar `http://VIP:80` (het
  eigen certificaat van `install.sh` geldt maar voor één container). Op node
  1 blijft de nginx van de oude homepage tot de overname; `voorstart` bewaart
  hem eerst in `/var/lib/cf-homepage/nginx-oud` (voor `terug`).
- **Syncs** (van de VIP-node, zonder `--delete`): `homepage-instellingen`
  (`/opt/cf-homepage/files/`, elke minuut) en `homepage-backups`
  (`/var/backups/homepage/`, 04:20).
- **Keepalived** met `nopreempt` en `homepage-ctl.sh check`: nginx draait
  en de api antwoordt (200 of 503 van `/api/healthz`). Niet "gezond" van
  `/api/healthz`: dat zegt ook of de worker leeft, en die draait maar op één
  node; dan zou geen enkele node het VIP nog willen.
- **ufw**: als ufw actief is, regels voor etcd, Patroni, PostgreSQL, SSH
  (syncs) en VRRP tussen de nodes.

## Maar één node doet de checks

De worker van de homepage doet alles wat naar buiten gaat: pings, http,
push-signalen, back-ups nakijken, Proxmox en Zabbix, meldingen naar de gsm.
Twee workers zouden alles dubbel doen en dubbel melden. Drie lagen:

1. **keepalived** start de worker op de node die het VIP krijgt
   (`notify_master`) en stopt hem bij het afgeven (`notify_backup`/`fault`).
2. **`cf-homepage-volg.timer`** (elke 30 s, door de agent uitgerold): een
   gemiste melding van keepalived laat de worker nooit op de verkeerde node
   draaien of nergens. `ExecCondition` (`worker-mag`): VIP hier, overname
   gebeurd, oude homepage uit.
3. **Een slot in de database**: `homepage-ctl.sh worker` neemt eerst
   `pg_advisory_lock(7316222)` in een eigen sessie (met TCP-keepalives) en
   start pas dan de worker. Denken twee nodes even allebei dat ze het VIP
   hebben (split-brain van keepalived), dan wacht de tweede. Valt de sessie
   weg (failover van PostgreSQL, netwerk), dan stopt de worker meteen en
   probeert systemd het opnieuw.

`homepage-ctl.sh status` toont welke node het slot heeft
(`application_name` = `cf-homepage-checks-<node>`); ClusterForge toont
`cf-homepage-worker` per node bij de bewaakte units (actief op één node).

## Markering en veiligheid

`COMMENT ON DATABASE homepage` = `cf-homepage key=<sha256[:16] van
secret.key> ready=<tijd> …`. `cf-homepage-api` start alleen met de markering
en dezelfde sleutel, en nooit zolang de oude `homepage-api` op die node
enabled is.

## Node 1: de bestaande installatie

- Uitrolstap `voorbereiden` (alleen met `/etc/systemd/system/homepage-api.service`):
  gecontroleerde `pg_dump` naar `/var/backups/cf-homepage-overname/`, dan
  `pg_renamecluster <v> main homepageoud` op poort 5433,
  `&port=5433` in `HOMEPAGE_DATABASE_URL` van de oude homepage (asyncpg via
  de socket), drop-ins `PGPORT=5433` voor `homepage-backup` en
  `homepage-backuptest`, een regel in `user_clusters` (psql zonder poort →
  5432, Patroni), de homepage weer starten en wachten tot de api antwoordt.
  Mislukt iets, dan gaat alles terug.
- TimescaleDB op node 1 blijft zoals het is (een update zou de versie van
  de oude database kunnen weghalen).
- `homepage-ctl.sh overnemen [--andere-versie]` op node 1:
  1. Controles: VIP hier, database van het cluster leeg en zonder
     markering, **Patroni-primary op deze node** (anders: Databases →
     Primary maken), commit en migratie van de oude database, eigenaar van de
     tabellen = de gebruiker van het cluster, met TimescaleDB:
     `shared_preload_libraries` en dezelfde versie beschikbaar.
  2. Oude `homepage-api`, `-worker`, `-syslog` en de timers stoppen en
     uitschakelen; de taken van TimescaleDB stilleggen. Vanaf hier draait
     elke fout alles terug.
  3. Telling per tabel (aantal + md5 tot 64 MB; voor een hypertable telt de
     grootte van al zijn chunks).
  4. `pg_dump -Fc` (volledig, met logregels) naar
     `/var/backups/cf-homepage-overname/cf-overname-*.dump`, gecontroleerd
     met `pg_restore --list`, plus de env.
  5. Terugzetten als superuser op de primary: met TimescaleDB zoals
     `homepage-terugzetten` (`CREATE EXTENSION timescaledb VERSION`,
     `timescaledb_pre_restore()`, `pg_restore`, de fouten beoordeeld door
     `deploy/herstelfouten.sh`, `timescaledb_post_restore()`); zonder in één
     transactie met `--exit-on-error`.
  6. Telling in het cluster: alles moet gelijk zijn. Taken van TimescaleDB
     in het cluster weer aan.
  7. Instellingen, `secret.key` en `setup-token` naar `.cf` (0600), de
     versie van TimescaleDB in `.cf/timescaledb` (de andere nodes
     installeren die versie zelf bij hun start), markering,
     `cf-homepage-api` starten en wachten tot de api antwoordt, syslog en
     worker starten.
- Terugdraaien: database van het cluster weg en leeg opnieuw (`DROP
  DATABASE … WITH (FORCE)`, `CREATE DATABASE … OWNER homepage`),
  overgenomen bestanden weg, taken van TimescaleDB in de oude database weer
  aan, oude homepage aan.
- `afronden`: PostgreSQL `homepageoud` stoppen en niet meer vanzelf starten
  (data blijft).
- `terug`: dump van het cluster, markering weg, `cf-homepage-api` en
  `-syslog` uit, `homepageoud`, de taken van TimescaleDB, de oude nginx en
  de oude units weer aan.

## Geen agent-protocol, geen migratie

Geen nieuw protocolnummer en geen migratie in de server. De agent kent
`cf-homepage-api`, `-worker` en `-syslog` als bewaakte units (nieuwe agent:
Nodes → Agents bijwerken), de homepage staat in de firewallcatalogus (80,
443, syslog 514 tcp/udp), `homepageoud` bij de opzijgezette
PostgreSQL-clusters.

## Mijlpalen

- M1: ontwerp + template, overname en terugweg, slot van de checks, tests
  (render, shellcheck, overname/terugdraaien/terug en twee workers tegen
  een echte PostgreSQL).
- M2: na het eerste gebruik bij Jonas: verbeteringen uit de praktijk.

## Bekend

- De hersteltest van de back-up (`homepage-backuptest`, maandelijks) draait
  niet in het cluster: hij maakt een database, en de gebruiker homepage mag
  dat niet.
- `/mnt/homepage-backup` (kopie buiten de container) bestaat alleen op de
  node waar het gemount is; de worker op een andere node meldt dat.
- https rechtstreeks op de container (poort 443) vervalt: NPM → http://VIP:80.
