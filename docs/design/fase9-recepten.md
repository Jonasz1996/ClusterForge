# Fase 9: clusters samenstellen met recepten

Vandaag kan je met Cluster uitrollen alleen kiezen uit ingebouwde templates
(keepalived-nginx, cron, docker, generic, kubernetes, en sinds PR #108
zabbix). Een eigen applicatie vraagt een nieuwe template in de code, met een
nieuw image. Dat schaalt niet: elke app die je wil draaien, wordt een PR.

Fase 9 maakt twee dingen los van elkaar:

1. **Clusterservices**: wat ClusterForge al kan en wat elk HA-cluster nodig
   heeft. Je vinkt aan wat je wil: VIP met keepalived, een databasecluster
   (Galera of Patroni, met of zonder scheidsrechter), bestandssync,
   cronjobs. Dat is geen nieuwe code, maar de bestaande onderdelen uit fase 5
   en 6, die je nu na de uitrol los toevoegt.
2. **Recept**: hoe jouw software erop komt, in een tekstformaat dat op een
   Dockerfile lijkt. Je schrijft het in de webinterface; ClusterForge bewaart
   het met versies, controleert het en laat de agents het uitvoeren.

Op de nodes blijft het zoals nu: één keer de agent installeren met het
token, daarna niets meer met de hand.

## Wat je ziet

**Recepten** (nieuw menu-item): een lijst van je recepten met versies. Een
recept openen geeft een editor met links de tekst en rechts, terwijl je
typt, wat ClusterForge ervan maakt: de stappen per node, welke stappen maar
op één node lopen, welke geheimen het recept vraagt en welke
clusterservices het nodig heeft. Een fout staat bij de regel. Opslaan maakt
een nieuwe versie; een versie die al gebruikt is, verandert nooit meer.
Bovenaan staat "Voorbeeld: Zabbix 8.0" om mee te beginnen.

**Cluster uitrollen** krijgt een derde soort naast de templates: "Zelf
samenstellen". Dat is één formulier:

| Blok | Wat je kiest |
| --- | --- |
| Waar | Bestaande nodes (zoals CT's, uit PR #108) of nieuwe VM's uit het golden image |
| Rollen | Een of meer rollen (bv. `server`, `proxy`), elk met nodes en één recept (versie) |
| VIP | Aan/uit, adres, op welke rol, wanneer het mag verhuizen (zie "Gezondheid") |
| Database | Geen, MariaDB Galera of PostgreSQL Patroni; op welke rol; scheidsrechter op een andere node |
| Bestandssync | Mappen die van de VIP-node naar de andere nodes gaan (fase 6) |
| Cronjobs | Jobs die op de VIP-node of op elke node lopen (fase 6) |
| Geheimen | Wat de recepten vragen; leeg laten maakt er een |

Daaronder staat het plan, zoals nu bij een template: per node de stappen,
in volgorde, met wat maar één keer loopt. Uitrollen gaat zoals elke uitrol,
met de omgeving (Test, Productie met TOTP).

Op een bestaand cluster staat bij de kaart "Recept" de knop **Nieuwe versie
toepassen**: diff van de stappen, dan node per node, VIP-houder laatst.

## Het receptformaat

Eén instructie per regel, in hoofdletters zoals in een Dockerfile. Een `\`
aan het einde zet de regel voort; `#` is commentaar. Waarden tussen
`{{ … }}` vult ClusterForge in per node.

Jouw Zabbix-voorbeeld, als recept. De versie van Zabbix is een veld bij het
uitrollen (`PARAM zabbix_version version`): 8.0 nu, een nieuwere later, zonder
het recept te wijzigen. Het volledige voorbeeld met de webconfiguratie staat in
`internal/recipes/examples/zabbix.cfr`.

```dockerfile
RECIPE zabbix 1.0
FROM debian:13
NEEDS database mariadb           # zonder databasecluster kan je dit niet uitrollen

PARAM zabbix_version version 8.0 # 7.0, 7.4, 8.0 of een nieuwere versie
SECRET db_password               # ClusterForge maakt het, bewaart het versleuteld

# a. Repository van Zabbix, voor de gekozen versie
REPO_DEB https://repo.zabbix.com/zabbix/{{ param.zabbix_version }}/release/debian/pool/main/z/zabbix-release/zabbix-release_latest_{{ param.zabbix_version }}+debian13_all.deb

# b. Pakketten
APT zabbix-server-mysql zabbix-frontend-php zabbix-apache-conf zabbix-sql-scripts zabbix-agent

# c. Database, gebruiker en schema (op het databasecluster van dit cluster)
DATABASE zabbix CHARSET utf8mb4 COLLATE utf8mb4_bin
DBUSER zabbix PASSWORD {{ secret.db_password }} GRANT ALL ON zabbix
IMPORT zabbix FROM /usr/share/zabbix/sql-scripts/mysql/server.sql.gz \
    TRUST_FUNCTIONS UNLESS "SELECT 1 FROM config"

# d. Configuratie
SET /etc/zabbix/zabbix_server.conf DBHost={{ db.host }}
SET /etc/zabbix/zabbix_server.conf DBPassword={{ secret.db_password }} MODE 0640 GROUP zabbix

# e. Diensten
SERVICE zabbix-server zabbix-agent apache2 ENABLE START

HEALTH service zabbix-server
HEALTH http http://localhost/zabbix/ 200
```

Wat er verschilt met de handleiding van Zabbix, en waarom:

- Geen `mysql -uroot -p` en geen wachtwoord in het recept. `DATABASE`,
  `DBUSER` en `IMPORT` lopen via de agent op de schrijvende datanode, zoals
  nu "Database toevoegen" (internal/components/dbadmin.go). Galera zet ze
  zelf op de andere nodes.
- `log_bin_trust_function_creators` aan- en uitzetten doet `IMPORT …
  TRUST_FUNCTIONS` zelf, alleen op de node die importeert, en ook als de
  import mislukt.
- `DBHost` is het VIP van het cluster (of `localhost` zonder VIP). Zo werkt
  elke node na een failover.
- `systemctl restart` schrijf je niet: een `SERVICE`-regel herstart de dienst
  als iets erboven in dit recept iets veranderde, en anders niet.

### Instructies

| Instructie | Doet | Loopt |
| --- | --- | --- |
| `RECIPE naam versie` | Naam en versie; verplicht, eerste regel | – |
| `FROM debian:13` | Welk OS de nodes moeten hebben; het plan weigert een andere node | – |
| `NEEDS database mariadb\|postgresql`, `NEEDS vip` | Clusterservices die aan moeten staan | – |
| `PARAM naam type [standaard]` | Een gewone instelling in het formulier (int, string, ipv4, size, bool, version; een version zoals 8.0 mag in een URL of RUN) | – |
| `SECRET naam [lengte]` | Een geheim; leeg gelaten maakt ClusterForge er een | – |
| `REPO_DEB url` | Download het release-pakket, `dpkg -i`, `apt update`; slaat over als het pakket er al is | elke node |
| `REPO naam url suite comp KEY url` | Een apt-bron met een eigen sleutel in `/etc/apt/keyrings` | elke node |
| `APT pakket …` | Installeert (`ABSENT` verwijdert) | elke node |
| `FILE pad [MODE] [OWNER] [GROUP] <<EOF … EOF` | Schrijft een bestand als het anders is | elke node |
| `SET pad sleutel=waarde [MODE …]` | Zet één sleutel in een bestaand `sleutel=waarde`-bestand; maakt de regel als hij er niet is | elke node |
| `DIR pad [MODE] [OWNER]`, `USER naam [SYSTEM]` | Map of gebruiker | elke node |
| `DATABASE naam [CHARSET] [COLLATE]` | Maakt de database als ze er niet is | één keer, schrijvende datanode |
| `DBUSER naam PASSWORD … GRANT ALL ON db` | Gebruiker vanaf de nodes en het VIP | één keer, schrijvende datanode |
| `IMPORT db FROM pad [TRUST_FUNCTIONS] [UNLESS "sql"]` | Laadt een `.sql` of `.sql.gz` van de node; `UNLESS` maakt het herhaalbaar | één keer |
| `DBCONFIG sleutel=waarde [MIN]` | Een instelling van de databaseserver die de app nodig heeft (zie hieronder) | elke datanode |
| `SERVICE naam … ENABLE\|DISABLE START\|STOP [RELOAD\|NORESTART]` | Zet diensten aan; herstart (of herlaadt) als een `FILE`, `SET` of `RUN` erboven iets veranderde, sinds de vorige regel voor die dienst | elke node |
| `RUN [CREATES pad] [UNLESS "commando"] [TITLE "tekst"] commando` | Uitwijkmogelijkheid; `CREATES` of `UNLESS` is verplicht (zoals de bestaande commandostap), zodat een tweede uitrol het niet opnieuw doet | elke node |
| `ONCE RUN\|FILE\|SET …` | Laat die instructie maar op één node lopen (de VIP-houder); een dienst herstart er niet voor | één keer |
| `ROLE naam` | Alles eronder geldt alleen voor die rol (een recept kan meer rollen hebben) | – |
| `HEALTH service naam`, `HEALTH http url code`, `HEALTH port n`, `HEALTH db` | Wat "gezond" is op een node | – |

Waarden in `{{ … }}`: `vip`, `node.name`, `node.ip`, `cluster.name`,
`cluster.nodes` (lijst), `role.nodes`, `db.host` (het VIP of `localhost`),
`db.port`, `db.name`, `db.user`, `param.<naam>`, `secret.<naam>`.

### Geheimen

- Een geheim staat nooit in de tekst van een recept, alleen `{{ secret.x }}`.
- In een `RUN` (en `CREATES`/`UNLESS`) mogen alleen waarden met een vaste
  vorm: `vip`, `node.name`, `node.ip`, `db.host`, `db.port` en params van
  type int, bool, ipv4 of size. Een geheim of tekst kan tekens bevatten die
  uit het commando breken; zet die met `SET` of `FILE`. Paden hebben nooit
  `{{ }}`. Dat is dezelfde regel als voor templates (`checkUnsafe`).
  De editor weigert een regel als `PASSWORD hunter2`: na `PASSWORD` moet een
  `{{ secret.… }}` komen.
- De waarde staat versleuteld in Postgres (internal/secrets), per cluster,
  zoals nu de `secret`-parameters van een template.
- Een `FILE` of `SET` waar een geheim in komt, krijgt standaard mode `0600`;
  het plan, het logboek en de GitOps-export tonen `••••`.
- Een geheim tonen kan bij het cluster, met TOTP, en komt in het logboek.

## Database-instellingen en de app koppelen

Gevraagd op 2026-11-04 bij de eerste Zabbix-uitrol: je wil bij een zelf
samengesteld cluster ook de instellingen van de database kunnen kiezen, en
weten wat de app als verbinding moet krijgen.

**Bij het uitrollen** heeft het blok Database in "Zelf samenstellen" dezelfde
velden als nu de kaart Instellingen van een database (buffer pool,
max_connections), plus:

- de bron van MariaDB: Debian (11.8) of MariaDB.org met een versie, zoals bij
  Versies (fase 8);
- extra instellingen als `sleutel = waarde` voor `[mysqld]`, zoals
  `innodb_log_file_size = 512M` of `max_allowed_packet = 64M`. ClusterForge
  weigert sleutels die het zelf beheert (`wsrep_*`, `bind-address`, poorten,
  `datadir`) en zegt waarom;
- de standaard tekenset (`utf8mb4`) en collatie.

Na de uitrol wijzig je ze op de kaart Instellingen van de database, met de
bestaande uitrol node voor node (VIP-houder laatst, wachten op Synced).

**Een recept kan instellingen vragen** met een nieuwe instructie:

```dockerfile
DBCONFIG innodb_lock_wait_timeout=120
DBCONFIG max_allowed_packet=64M MIN
```

Zonder `MIN` zet het recept de waarde; met `MIN` is het een ondergrens en
houdt ClusterForge een hogere waarde die je zelf koos. Botst een `DBCONFIG`
met wat je in het blok Database koos, dan toont het plan dat vóór de uitrol.
De waarden komen in een eigen bestand `99-clusterforge-app.cnf`, los van de
Galera-configuratie.

**De app koppelen.** Elke database-instructie in een recept vult vaste
waarden die het recept kan gebruiken: `{{ db.host }}` (het VIP, of
`localhost` zonder VIP), `{{ db.port }}` (3306), `{{ db.name }}` en
`{{ db.user }}` (de eerste `DATABASE` en `DBUSER` van het recept). Daarmee
schrijft het recept de configuratie van de app zelf, zodat een
installatiewizard zoals die van de Zabbix-frontend niet meer nodig is: in het
voorbeeld komt `zabbix.conf.php` er met een `FILE`.

Op de pagina van het cluster staat een kaart **Verbinding** met host, poort,
database en gebruiker, voor een app die je toch met de hand koppelt. Het
wachtwoord toont ClusterForge alleen na TOTP, en dat komt in het logboek.

## Gezondheid, VIP en database samen

De `HEALTH`-regels van alle recepten op een rol worden één controlescript
voor keepalived op die rol: het VIP gaat alleen naar een node waar alles
gezond is. Staat Database aan op dezelfde rol, dan komt daar ook
`cf-agent check db` bij, zoals nu in het zabbix-template: het VIP volgt een
node waarvan de database mag schrijven. De standaard is `nopreempt` en geen
failback; dat kan je per cluster omzetten.

Dezelfde regels zijn ook de controles na de uitrol en voeden de health
score.

## Volgorde van een uitrol

1. Nodes klaarzetten (VM's klonen, of de bestaande nodes nakijken: agent
   online, juiste OS, geen ander cluster).
2. Clusterservices: database (Galera vormen, scheidsrechter), dan keepalived
   met het VIP.
3. Recepten, per rol, node per node. Wat `ONCE` is of een database-instructie
   loopt op één node, nadat de database Synced is, en de andere nodes wachten
   tot die stap klaar is.
4. Bestandssync en cronjobs aanzetten.
5. Controles: `HEALTH` op elke node, VIP aanwezig.

Mislukt een stap, dan stopt de uitrol op die node met de uitvoer in het
logboek. Opnieuw uitrollen gaat verder: elke instructie is idempotent
(bestanden alleen als ze anders zijn, `APT` alleen wat ontbreekt, `IMPORT` met
`UNLESS`, `RUN` met `CREATES`/`UNLESS`).

## Hoe het in de code past

Geen nieuwe uitrolmachine. Een samengesteld cluster wordt in de server
omgezet naar een gewone `templates.Template` (rollen, stappen, checks,
services, `database`-blok uit PR #108), en gaat dan door dezelfde paden als
een ingebouwde template: plan, uitrol, GitOps, drift, Opnieuw toepassen.

- `internal/recipes` (nieuw): parser en compiler van recept naar
  `templates.Role`-stappen. Bestaande stapsoorten: `package`, `file`,
  `service`, `user`, `directory`, `command`. `APT`, `FILE`, `DIR`, `USER`,
  `SERVICE`, `RUN` en `REPO` (als bestand + sleutel + `apt update`) passen
  daar al in.
- Nieuwe agentstappen (één protocolverhoging): `repo_deb`, `ini_set` (voor
  `SET`) en `once` (een stap die de server aan één node geeft en waar de
  andere op wachten). `DATABASE`, `DBUSER` en `IMPORT` gebruiken de bestaande
  databasecommando's van de agent (`internal/agent/dbsql`). Een node met een
  oudere agent blokkeert het plan met `agent_behind`, zoals de verhuizing nu.
- Opslag: tabellen `recipes` en `recipe_versions` (tekst, gecompileerde vorm,
  wie en wanneer), en per cluster welke versie per rol draait.
- GitOps: een samengesteld cluster exporteert als `cluster.yaml` met de
  keuzes en de recepten als `recipes/<naam>.cfr`; geheimen nooit.
- De ingebouwde templates blijven. Later kan `zabbix` als voorbeeldrecept
  meekomen, maar het template uit PR #108 blijft werken.

## Mijlpalen

| # | Wat | Zichtbaar voor jou |
| --- | --- | --- |
| M1 | Parser en compiler, met tests op het Zabbix-recept | Nog niets; CI |
| M2 | Pagina Recepten: editor, live controle, voorbeeld, versies | Recepten schrijven en zien wat ze doen |
| M3 | Agentstappen `repo_deb`, `ini_set`, `once` (protocolverhoging) | Agents bijwerken via de GUI |
| M4 | Database-instructies (`DATABASE`, `DBUSER`, `IMPORT`, `DBCONFIG`), geheimen en de kaart Verbinding | Database met schema en de juiste instellingen, zonder commando's |
| M5 | Cluster uitrollen → Zelf samenstellen (rollen, VIP, database met instellingen, sync, cronjobs) | Een cluster uitrollen met een eigen recept |
| M6 | Keepalived-check uit `HEALTH`, controles na de uitrol, health score | VIP volgt een gezonde node |
| M7 | Nieuwe receptversie toepassen op een bestaand cluster (diff, node per node) | Updates van je app via de GUI |
| M8 | GitOps-export en drift voor recepten | Recepten in Git |

Alle mijlpalen staan in PR #109, die op PR #108 staat (bestaande nodes en
CT's uitrollen, `database`-blok in templates).

### Stand na M5

- Zelf samenstellen (Cluster uitrollen → Zelf samenstellen) maakt een template
  `eigen-<naam>` die in de tabel `blueprints` staat; dezelfde samenstelling
  geeft dezelfde versie, een andere een nieuwe (1.0.0, 1.0.1, …).
- De keepalived-check uit `HEALTH` (M6) zit er al in: `service`, `http`,
  `port` en `db`, en met de database op dezelfde rol alleen een node waarvan
  MariaDB mag schrijven.
- `db.host` is het VIP als het op de databaserol staat, of `localhost` als de
  app op die nodes zelf draait. Een app op andere nodes zonder VIP daar kan
  niet: dan weet ze niet waar de database is.
- `IMPORT` leest het bestand op een node met de database; een recept met
  `IMPORT` staat daarom op de databaserol.
- Sync en cronjobs zet je na de uitrol aan op de clusterpagina (fase 6); ze
  zitten niet in de samenstelling.
- Nog te doen: de kaart Verbinding, een nieuwe receptversie toepassen op een
  bestaand cluster (M7) en GitOps (M8).

## Niet in deze fase

- Docker-images en compose als recept: daarvoor is er de docker-template.
- Ansible of shellscripts als recept inladen.
- Recepten delen tussen ClusterForge-installaties.
- Een heredoc (`<<EOF`) in `RUN`; die kan alleen bij `FILE`. Schrijf het
  script met `FILE` en roep het aan met `RUN`.

## Vragen die het bouwen niet tegenhouden

- Zabbix 8.0 uit repo.zabbix.com (jouw voorbeeld) of 7.0 LTS uit Debian (het
  template in PR #108)? Het recept kan allebei; het voorbeeld volgt jouw
  tekst.
- Apache (zoals in jouw voorbeeld) of Nginx (zoals in PR #108)? Ook dat staat
  in het recept zelf, niet in ClusterForge.
