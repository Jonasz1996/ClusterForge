# Fase 15: apt en Docker automatisch en clusterbewust bijwerken

Gevraagd door Jonas op 2026-11-15:

> Apt update && apt full-upgrade -y met parameters bij voor default antwoord
> bij vb overschrijven config file. Anders zijn die config files lockt door
> apt en werkt het niet meer.
>
> Docker heb ik allemaal met docker compose gedaan en volume image docker
> compose down compose file aanpassen docker compose up. Gemapte files en
> compose file staan op verschillende plekken. Eerst docker ps -a, dan loopen
> docker inspect grep waar config staat.
>
> Agents spreken per cluster af: backup nodes eerst, testen of alles op is,
> failoveren, master doen.
>
> Ook testen of de container werkt en docker log uitlezen. Als voor docker
> vb de -e parameter verandert: de webpagina uitlezen van waar het komt en
> fixen voorstellen. Alle images komen van Docker Hub of GitHub.

Fase 10 (PR #110) legde de basis: runs op de agent (`cf.node.<id>.run`),
taak `updates.run` met één node tegelijk, de VIP-houder als laatste en een
gezondheidspoort na elke node. Deze fase bouwt daarop verder en maakt er een
volledig automatische, clusterbewuste update van. Alles blijft in de GUI;
op de node typ je niets.

## Wat er verandert ten opzichte van fase 10

| Fase 10 | Fase 15 |
| --- | --- |
| apt `upgrade` standaard, `full-upgrade` als optie | `full-upgrade` standaard; nooit een vraag, nooit een slot dat blijft hangen |
| `dpkg --configure -a` als dpkg onderbroken was | ook `apt-get -f install`, en na een mislukte upgrade één keer herstellen en opnieuw |
| Compose-projecten via `docker compose ls` | via `docker ps -a` + `docker inspect`: werkmap, compose-bestanden, env-bestand, gemapte mappen per container |
| `pull` + `up -d` | `pull`; alleen als er een nieuw image is `down` + `up -d`; daarna testen of elke container draait (en gezond is), logs uitlezen, en bij een fout terugzetten naar het vorige image |
| — | image-tag aanpassen in het compose-bestand vanuit de GUI (nieuwe versie) |
| — | compose-bestand bekijken en aanpassen in de GUI, zonder dat wachtwoorden de node verlaten |
| — | release notes van Docker Hub / GitHub ophalen en de assistent een fix laten voorstellen |
| VIP-houder als laatste | de VIP-houder eerst **failoveren** (keepalived uit, VIP naar een al bijgewerkte node, Patroni eerst overschakelen), dan bijwerken, dan keepalived terug |
| alleen met de hand | **automatisch** in een venster, aan/uit per omgeving en per cluster |

## Volgorde per cluster ("de agents spreken af")

De server is de dirigent; per cluster loopt hoogstens één taak (het
clusterslot dat er al is). Voor elk cluster:

1. **Vooraf**: elke node verbonden, agent vanaf protocol 29, de VIP's hebben
   een vaste houder. Is dat niet zo, dan begint de taak niet.
2. **De back-upnodes** (geen VIP), één voor één: diensten onthouden, apt,
   Docker, eventueel herstarten, en wachten tot alles weer op is: dezelfde
   diensten `active` in twee verse heartbeats, elke Compose-container
   `running` (en `healthy` als er een healthcheck is), de VIP's met een
   vaste houder.
3. **Failover van de master**: bij Patroni eerst overschakelen als de master
   de primary is; dan keepalived op de master uit (onderhoud, zoals de knop
   Onderhoud bij de node) en wachten tot elk VIP op een andere node staat.
   ClusterForge controleert dat die node in deze taak al bijgewerkt is.
4. **De master bijwerken**, zoals in stap 2.
5. **Keepalived terug** op de master; wachten tot de VIP's weer een vaste
   houder hebben.

Bij de eerste fout stopt de taak. De master blijft dan onaangeroerd. Ging
het mis op de master zelf, dan blijft keepalived daar uit, zodat het VIP op
de gezonde, bijgewerkte node blijft; de node staat op Onderhoud en het
logboek zegt het. "Onderhoud beëindigen" bij de node zet hem terug.

## apt (agent)

Elke `apt-get` en `dpkg` krijgt:

```
DEBIAN_FRONTEND=noninteractive DEBCONF_NONINTERACTIVE_SEEN=true
UCF_FORCE_CONFFOLD=1 NEEDRESTART_MODE=a APT_LISTCHANGES_FRONTEND=none
-o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold
-o DPkg::Lock::Timeout=600 -o APT::Get::Assume-Yes=true
```

- `--force-confdef --force-confold`: bij een gewijzigd configuratiebestand
  blijft jouw versie staan (de nieuwe komt ernaast als `.dpkg-dist`); dpkg
  stelt nooit de vraag die het slot vasthoudt. `UCF_FORCE_CONFFOLD` doet
  hetzelfde voor bestanden die via ucf lopen.
- `DPkg::Lock::Timeout=600`: draait unattended-upgrades of een andere apt,
  dan wacht de run tot 10 minuten op het slot in plaats van te falen. Een
  achtergebleven lockbestand zonder proces is geen probleem: het slot is een
  fcntl-lock op het bestand, geen bestaan van het bestand. ClusterForge
  verwijdert nooit lockbestanden.
- **Herstel**: is dpkg onderbroken (`dpkg --audit` meldt iets), dan eerst
  `dpkg --configure -a` en `apt-get -f install`. Mislukt de upgrade met een
  dpkg-fout ("dpkg was interrupted", "unmet dependencies", "Sub-process
  /usr/bin/dpkg returned an error"), dan één keer herstellen en opnieuw. Na
  afloop moet `dpkg --audit` leeg zijn, anders is de run mislukt.
- Standaard `full-upgrade`; `upgrade` blijft kiesbaar.
- Databasepakketten op een databasecluster blijven vastgehouden zoals in
  fase 10.
- Nieuwe `.dpkg-dist`-bestanden van deze run worden in het rapport genoemd,
  zodat je weet waar een nieuwe standaardconfiguratie naast de jouwe staat.

## Docker (agent)

### Ontdekken

```
docker ps -a --filter label=com.docker.compose.project -q
docker inspect <ids>
```

Per container leest de agent: `com.docker.compose.project`,
`.project.working_dir`, `.project.config_files`,
`.project.environment_file`, `.service`, het image (`Config.Image`) en zijn
id, de toestand en health, het aantal herstarts en de mounts (bron →
doel). Zo vindt hij elk project, ook als het compose-bestand en de gemapte
mappen op verschillende plekken staan, en ook als het project nu gestopt is.
Facts krijgen per project de werkmap, de bestanden, het env-bestand en per
dienst het image, de toestand en de gemapte mappen. Een compose-bestand dat
niet meer bestaat, wordt gemeld.

### Bijwerken, per project

1. Vóór: per dienst het image en zijn id onthouden; welke diensten draaiden.
2. Gevraagde nieuwe tag? Dan het compose-bestand aanpassen (de exacte
   `image:`-regel, of de variabele in het env-bestand als de regel
   `${VAR}` gebruikt), met een kopie in de map van de run.
3. `docker compose -p <p> --project-directory <werkmap> -f <bestanden>
   [--env-file <env>] pull`.
4. Geen nieuw image en geen gewijzigd bestand: niets doen ("al bij"), tenzij
   "altijd opnieuw maken" aan staat.
5. `down`, dan `up -d`.
6. **Testen**: tot 2 minuten (instelbaar) elke 2 s kijken; geslaagd als
   elke dienst die vóór de update draaide, 20 s aan één stuk `running` is,
   met health `healthy` als er een healthcheck is, zonder herstarts.
7. **Logs**: `docker compose logs --tail 100 --timestamps` per project
   komen in het rapport en in de uitvoer, met wachtwoorden gemaskeerd.
8. Mislukt de test en staat **terugzetten** aan (standaard): de oude
   compose-bestanden terug, de oude image-id's opnieuw taggen op hun naam,
   `down` + `up -d`, en opnieuw testen. De run is dan toch mislukt, zodat
   de taak niet verder gaat naar de master.
9. Voor elke dienst met een nieuw image: de omgevingsvariabelen van het oude
   en het nieuwe image (namen, geen waarden) vergelijken, en de labels
   `org.opencontainers.image.source` en `.version`. Nieuwe of verdwenen
   variabelen staan in het rapport.
10. `docker image prune -f` als dat gevraagd is, pas na een geslaagde test.

### Compose-bestand in de GUI

Updates → project → **Compose-bestand**: de agent leest de bestanden van het
project (compose-bestanden en het env-bestand). Waarden van sleutels die op
een wachtwoord lijken (`PASSWORD`, `SECRET`, `TOKEN`, `KEY`, …) worden
vervangen door `«geheim-1»`, `«geheim-2»`, …; die waarden verlaten de node
nooit. Opslaan stuurt de nieuwe tekst met dezelfde plaatshouders en de
sha256 van wat gelezen werd; de agent zet de echte waarden terug, weigert
als het bestand intussen veranderde, controleert met `docker compose config
-q` en doet dan stappen 3-8 (met terugzetten als het niet goed komt).

## Fix voorstellen (server + assistent)

Een mislukte of verdachte Docker-update (container start niet, herstart,
fouten in de log, nieuwe/verdwenen variabelen) krijgt in de taak en op de
pagina Updates de knop **Fix voorstellen**. ClusterForge:

1. haalt de bron van het image op:
   - `ghcr.io/<eigenaar>/<repo>` of label `org.opencontainers.image.source`
     naar GitHub: de releases tussen de oude en de nieuwe versie
     (`api.github.com/repos/<eigenaar>/<repo>/releases`), anders de README;
   - Docker Hub (`<naam>` of `<ns>/<naam>`): de beschrijving van de
     repository (`hub.docker.com/v2/repositories/<ns>/<naam>`), waar de
     variabelen meestal staan;
2. geeft de assistent: het rapport (diensten, images, oude/nieuwe versie,
   verschil in variabelen), de logs, het compose-bestand (gemaskeerd) en de
   release notes;
3. de assistent antwoordt met de oorzaak, het bewijs uit log en release
   notes, en een aangepast compose-bestand.

**Toepassen** opent dat voorstel in de editor van het compose-bestand; je
kijkt na en slaat op. De assistent voert zelf niets uit. Bij een
automatische update die mislukt, start ClusterForge het voorstel vanzelf
(als de assistent ingesteld is) en staat het in het gesprek bij de node.

Release notes ophalen gaat van de ClusterForge-server naar het internet
(`api.github.com`, `hub.docker.com`); de nodes hoeven daar niets voor.

## Automatisch bijwerken

Updates → **Automatisch**:

- per omgeving (lab, test, prod) een beleid: aan/uit, wat (apt, Docker,
  opruimen, herstarten als nodig) en het venster (zoals
  `zo 03:00-05:00`, `dagelijks 02:00-04:00`; de tijdzone van de server);
- per cluster: **zoals de omgeving**, **eigen beleid** of **uit**.

Standaard staat alles uit. Een beleid voor prod (of een prodcluster)
aanzetten of wijzigen vraagt de slug (`prod` voor de omgeving) en je
TOTP-code, zoals elke prodactie.

De planner (die er al is voor de failovertests) kijkt elke minuut: een
cluster met een beleid dat aan staat, waarvan het venster nu loopt en dat in
dit venster nog niet aan de beurt was, krijgt een taak `updates.run` als er
iets te doen is (apt-updates in de facts, of Docker aan). Een cluster waar
een node offline is of een te oude agent heeft, wordt overgeslagen met een
melding in het logboek. Clusters zonder beleid en nodes zonder cluster
werken nooit vanzelf bij.

## Agent: protocol 29

- `AptRun`: standaard full; herstel en slot zoals hierboven.
- `DockerRun`: `Recreate` (alleen bij nieuw image / altijd), `HealthSeconds`,
  `Rollback`, `Tags` (project, oud image, nieuw image), `Edit` (bestand,
  tekst met plaatshouders, sha256), `Read` (alleen lezen voor de editor).
- `RunStatus.Docker`: per project het rapport (diensten, oud en nieuw image,
  versies, bron, variabelen erbij/weg, toestand, logs, teruggezet) en bij
  `Read` de bestanden.
- Facts: `ComposeProject` met werkmap, env-bestand, diensten en mounts.

Een agent ouder dan 29 kan nog de updates van fase 10 doen; de nieuwe opties
en automatisch bijwerken vragen 29 ("agent te oud, werk hem eerst bij").

## Server

- `internal/updates`: failoverstappen, nieuwe Docker-opties, beleid en de
  planner-taak `automatisch bijwerken`.
- `internal/updates/source.go`: release notes van GitHub en Docker Hub, met
  een time-out van 10 s en hoogstens 30 KiB per bron.
- Migratie 00066: `update_policies` (per omgeving of per cluster).
- API: `GET/PUT /updates/policies`, `POST /updates/compose` (lezen),
  `PUT /updates/compose` (opslaan en opnieuw opstarten),
  `POST /updates/fix` (voorstel, stroomt zoals de andere uitleg).

## Mijlpalen

1. **Agent**: protocol 29, apt-herstel, Docker ontdekken via inspect, nieuwe
   updatevolgorde met test, logs, terugzetten, tags, compose lezen/schrijven.
2. **Server: clusterbewust**: failover van de master, controles vooraf,
   nieuwe opties in `updates.run`.
3. **Server: automatisch**: migratie, beleid, planner, API.
4. **Fix voorstellen**: release notes, assistent-opdracht, API.
5. **Webinterface**: dialoog uitgebreid, projecten met diensten en mappen,
   compose-editor, Fix voorstellen, tab Automatisch; README.

## Niet in deze fase

- Kijken of er een nieuwe tag is zonder te pullen (registry-API per image):
  `pull` zegt het al; een lijst "nieuwe versie beschikbaar" kan later.
- Losse containers zonder Compose: blijven buiten schot.
- Automatisch een voorstel toepassen: altijd eerst door jou nagekeken.
