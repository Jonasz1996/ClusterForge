# Fase 10: updates en eigen commando's via de agents

Gevraagd door Jonas op 2026-11-09: "agents voorzien updates via apt en/of
docker; ze moeten ook eigen commando's kunnen draaien die ik vanuit de GUI
ingeef en aanpas", met als voorbeeld de Zabbix-upgrade:

```sh
grep -v -e '^HANodeName' -e '^NodeAddress' -e '^Include=' -e '^PidFile' -e '^SocketDir' \
  /etc/zabbix/cf-zabbix_server.conf > /root/zbx-upgrade.conf \
&& printf 'Include=/etc/zabbix/cf-zabbix_server-db.conf\nPidFile=/tmp/zbx-upgrade.pid\nSocketDir=/tmp\n' >> /root/zbx-upgrade.conf \
&& zabbix_server -f -c /root/zbx-upgrade.conf
```

De regel blijft: op een node typ je alleen één keer het installatiecommando
van de agent. Al de rest, ook pakketupdates en zo'n eenmalig commando, gaat
via de webinterface, met de uitvoer live erbij.

## Wat er al is, en wat deze fase niet opnieuw doet

| Er is al | Waar | Deze fase |
| --- | --- | --- |
| Agents werken zichzelf bij | #99/#100, Instellingen → Agents bijwerken | blijft zo; dit gaat over de software op de node |
| Database node voor node bijwerken (MariaDB/Galera, Patroni) | fase 5, databasekaart → Bijwerken (`component.update`) | blijft de weg voor databasepakketten; een gewone update houdt ze vast |
| MariaDB-versie en bron per cluster | ontwerp fase 8 (#101) | niet gebouwd; deze fase raakt geen bronnen |
| Beschikbare apt-updates tellen | facts (`apt-get -s dist-upgrade`), kaart bij de node | wordt de basis van de pagina Updates |
| Kapotte apt-bron, onderbroken dpkg | #104, `apply.steps` | dezelfde afhandeling vóór elke update |
| Beheerde cronjobs met "Nu draaien" | fase 6, `cf-agent cron-run` | eigen commando's zijn eenmalig, geen schema; een commando met schema blijft een cronjob |
| Recepten (installeren) | fase 9 | recepten zetten iets neer en zijn idempotent; een eigen commando doet één keer iets |

## Wat je ziet

### Updates (nieuw menu-item)

Per cluster een kaart, en daaronder de nodes zonder cluster:

- per node: aantal apt-updates (waarvan beveiliging), de pakketten, "herstart
  nodig" als `/var/run/reboot-required` er staat, en de Docker
  Compose-projecten met hun status;
- knop **Cluster bijwerken** en per node **Bijwerken**. Je kiest:
  - **Pakketten (apt)**: `upgrade` (standaard) of `full-upgrade`, met of
    zonder `autoremove`;
  - **Docker**: per Compose-project `pull` en `up -d`, alle projecten of de
    gekozen; daarna eventueel oude images opruimen (`image prune`);
  - **Herstarten als het nodig is** (standaard uit).
- Prod vraagt de slug van het cluster en je TOTP-code, zoals elke prodactie.

Een update is een taak (Taken), met per node een stap en de uitvoer van
apt-get of docker live in die stap.

Volgorde en veiligheid, zoals bij `cluster.apply`:

1. één node tegelijk, de nodes zonder VIP eerst en de VIP-houder als laatste;
2. vóór een node: de diensten die er nu `active` zijn, worden onthouden;
3. na een node: wachten tot die diensten weer `active` zijn in twee verse
   heartbeats, en tot de VIP's een vaste houder hebben;
4. de eerste node die niet gezond terugkomt, stopt de taak; de nodes erna
   blijven zoals ze waren.

Databasepakketten (`mariadb-*`, `galera-*`, `libmariadb*`, `mysql-common`,
`postgresql*`, `libpq*`, `patroni`) blijven tijdens een gewone update staan
op een node die bij een databasecluster hoort: apt-get krijgt een tijdelijke
`hold`, die de agent na de update zelf weer weghaalt (ook na een crash,
bij zijn volgende start). Die pakketten werk je bij met Bijwerken op de
databasekaart, die wacht op Synced en bij Patroni eerst overschakelt. Het
logboek van de stap zegt welke pakketten bleven staan.

### Commando's (nieuw menu-item)

- Een lijst van je eigen commando's: naam, omschrijving, wanneer het laatst
  draaide en met welke uitkomst.
- Een commando openen geeft een editor: het script (`/bin/sh` of, met een
  `#!`-regel bovenaan, die interpreter), als welke gebruiker (standaard root)
  en een tijdslimiet (standaard 30 minuten). Opslaan maakt een nieuwe
  revisie; een run onthoudt welke revisie hij draaide, met de tekst.
- **Uitvoeren**: kies losse nodes of een cluster. Eén node tegelijk, de
  VIP-houder als laatste; standaard stopt de run bij de eerste node waar het
  commando niet met exitcode 0 eindigt ("Verder bij een fout" kan je
  aanvinken). Prod vraagt slug en TOTP.
- De uitvoer komt live in de taak, per node een stap. **Stoppen** stopt het
  commando op de node (SIGTERM aan de hele procesgroep, na 10 s SIGKILL).

Alleen een beheerder maakt, wijzigt of draait commando's; een viewer ziet ze
en hun uitvoer. Het logboek krijgt elke wijziging en elke run met de
sha256 van de tekst; wachtwoorden in de uitvoer worden gemaskeerd zoals bij
cronjobs.

## De agent (protocol 25)

Eén nieuw mechanisme voor alle drie: een **run**. Een run is een opdracht
die langer mag duren dan een NATS-verzoek, die een herstart van de agent
(ook zijn eigen update) en van de server overleeft, en waarvan de uitvoer
op de node in een bestand staat.

Subject `cf.node.<id>.run`, verzoeken `start`, `poll` en `stop`:

- `start` met een `RunSpec` (id, soort `script` | `apt` | `docker`, en de
  instellingen van die soort). De agent schrijft de spec naar
  `/var/lib/clusterforge/runs/<id>/spec.json` en start
  `systemd-run --unit cf-run-<id> --collect cf-agent run-exec <id>`. Zo hangt
  de run niet aan het proces van cf-agent. Een `start` met een id dat er al
  is, start niets opnieuw en geeft de stand.
- `poll` met een offset geeft de uitvoer vanaf die offset (hoogstens 64 KiB
  per keer), de stand (`running`, `ok`, `failed`, `timeout`, `stopped`), de
  exitcode en of de node daarna herstart moet worden.
- `stop` stopt de unit.

`cf-agent run-exec <id>` voert uit:

- `script`: het script als bestand (mode 0700) en draait het als de gekozen
  gebruiker, met de procesgroep en tijdslimiet zoals `cron-run`;
- `apt`: `dpkg --configure -a` als dpkg onderbroken was, `apt-get update`
  (een 404-bron buiten Debian is een waarschuwing, zoals in #104), de
  tijdelijke holds, `apt-get -y upgrade` of `full-upgrade` met
  `--force-confdef --force-confold` en `DEBIAN_FRONTEND=noninteractive`,
  eventueel `autoremove`, holds weg, en `reboot-required` melden;
- `docker`: `docker compose ls -a --format json`, dan per gekozen project
  `docker compose -p <naam> -f <bestanden> pull` en `up -d`, en eventueel
  `docker image prune -f`.

De laatste twintig runs blijven op de node staan; de rest ruimt de agent op.
De facts krijgen `reboot_required` en per Compose-project naam, status en
bestanden.

Een agent ouder dan protocol 25 kent dit niet; de knoppen zeggen dan
"agent te oud, werk hem eerst bij" (Instellingen → Agents bijwerken).

## De server

- `internal/noderun`: start een run in een stap van een taak, vraagt elke
  seconde de nieuwe uitvoer, zet de laatste 500 regels in de stap en bewaart
  het run-id in de state van de stap. Na een herstart van de server gaat de
  taak verder met hetzelfde id: de agent start niets opnieuw en de server
  leest verder. Annuleren van de taak stuurt `stop`.
- Taak `updates.run` (package `internal/updates`): de volgorde, de gate na
  elke node (`health.Gate`), het vasthouden van databasepakketten op
  databaseclusters, herstarten als het mag (`system.reboot`, dan wachten op
  twee heartbeats).
- Taak `command.run` en tabellen `commands` en `command_revisions`
  (migratie 00063).
- API: `GET /updates`, `POST /updates`, `GET/POST /commands`,
  `GET/PUT/DELETE /commands/{id}`, `POST /commands/{id}/run`.

## Mijlpalen

1. **Agent**: protocol 25, runs (`script`, `apt`, `docker`), `run-exec`,
   facts `reboot_required` en Compose-projecten. Tests met een nep-systemd en
   nep-apt.
2. **Server: runs en updates**: `internal/noderun`, taak `updates.run`, API
   en tests.
3. **Server: eigen commando's**: migratie, opslag met revisies, taak
   `command.run`, API, logboek, tests.
4. **Webinterface**: pagina's Updates en Commando's, knop Bijwerken bij de
   node en het cluster, uitvoer in de taak.
5. **Docs**: README (Updates, Commando's) en het voorbeeld van Zabbix als
   commando.

## Niet in deze fase

- Vanzelf bijwerken in een testvenster (zoals de agents): kan later, met
  dezelfde taak.
- Losse Docker-containers zonder Compose: die kan ClusterForge niet
  opnieuw maken met dezelfde instellingen. Alleen Compose-projecten.
- Parameters in commando's (`{{ param }}`) en geheimen erin: later, met de
  geheimen van fase 9.
- Commando's op een schema: dat is een cronjob (fase 6).
