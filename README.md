# ClusterForge

Eén webinterface om traditionele Linux HA-clusters (Proxmox, Debian/Ubuntu, Keepalived, gedeelde databases, Docker, cronclusters) te beheren, monitoren, deployen en onderhouden. Geen Kubernetes-kloon.

Het technisch ontwerp staat in [docs/design/mvp-fase-1.md](docs/design/mvp-fase-1.md).

## Stand van zaken

| Mijlpaal | Inhoud | Status |
| --- | --- | --- |
| 1. Fundament | Server, database, login met TOTP, webinterface, CI | klaar |
| 2. Inventory | Clusters, nodes, VIP's | klaar |
| 3. Agent | Enrollment, heartbeat, facts | klaar |
| 4. Monitoring | Metrics, status, dashboards | klaar |
| 5. Proxmox | Sync en VM-acties | klaar |
| 6. Node lifecycle | Reboot, maintenance, drain | klaar |
| 7. Templates en deployment | Clusters uit templates | klaar |

| Fase 2 | Inhoud | Status |
| --- | --- | --- |
| 1. Logboek | Wie deed wat, wanneer en vanaf waar; filters en export | klaar |
| 2. Back-ups | Versheid van de Proxmox-back-ups per VM, VM's zonder back-upjob | klaar |

## Draaien met Docker Compose

Stap voor stap in een Debian-container op Proxmox: [docs/install-proxmox-lxc.md](docs/install-proxmox-lxc.md).

```sh
cd deploy
cp .env.example .env          # zet minstens POSTGRES_PASSWORD en CF_MASTER_KEY
docker compose up -d --build
docker compose exec -it server clusterforge-server admin create -username jonas
```

De server luistert op `127.0.0.1:8080`. Zet hem achter je reverse proxy met TLS: de sessiecookie heeft de `Secure`-vlag en werkt dus niet over gewone http (tenzij `CF_SECURE_COOKIES=false`).

Agents verbinden zelf naar de server, op poort 4222 (NATS met TLS). Die poort moet dus bereikbaar zijn vanaf je nodes; ClusterForge maakt nooit zelf een verbinding naar een node.

### Configuratie

| Variabele | Standaard | Betekenis |
| --- | --- | --- |
| `CF_DATABASE_URL` | (verplicht) | PostgreSQL-connectiestring |
| `CF_LISTEN` | `:8080` | Adres van de HTTP-server |
| `CF_SECURE_COOKIES` | `true` | `Secure`-vlag op de sessiecookie |
| `CF_TRUST_PROXY_HEADERS` | `false` | Client-IP uit `X-Forwarded-For` halen; alleen achter je eigen proxy |
| `CF_SESSION_TTL` | `12h` | Sessie verloopt na zoveel tijd zonder activiteit |
| `CF_LOG_LEVEL` | `info` | `debug`, `info`, `warn` of `error` |
| `CF_NATS_LISTEN` | `:4222` | Adres waarop agents met NATS verbinden |
| `CF_NATS_ADVERTISE` | (leeg) | `host[:poort]` waarmee agents NATS bereiken; leeg betekent de hostnaam waarmee de agent zich aanmeldt |
| `CF_AGENT_DIR` | `/usr/share/clusterforge/agents` | Map met de `cf-agent`-binaries die de server aanbiedt om te downloaden |
| `CF_VICTORIAMETRICS_URL` | (leeg) | VictoriaMetrics voor de metrics, bijvoorbeeld `http://victoriametrics:8428`; leeg zet de grafieken uit |
| `CF_GRAFANA_NODE_URL` | (leeg) | Link naar Grafana bij elke node, met `{hostname}`, `{node_id}`, `{cluster}`, `{cluster_id}` en `{env}` |
| `CF_GRAFANA_CLUSTER_URL` | (leeg) | Link naar Grafana bij elk cluster, met `{cluster}`, `{cluster_id}` en `{env}` |
| `CF_MASTER_KEY` | (leeg) | Sleutel van 32 bytes (base64 of hex) waarmee de server geheimen zoals het Proxmox-token versleutelt; maak er een met `openssl rand -hex 32`. Zonder sleutel kan Proxmox niet gekoppeld worden |
| `CF_MASTER_KEY_FILE` | (leeg) | Bestand met de masterkey, in plaats van `CF_MASTER_KEY` |

### Commando's

```text
clusterforge-server serve                        start de server (voert eerst de migraties uit)
clusterforge-server migrate                      voert alleen de migraties uit
clusterforge-server admin create -username X     maakt een gebruiker aan (-role admin|viewer)
clusterforge-server version                      toont de versie
```

`admin create` vraagt het wachtwoord in de terminal, of leest één regel van stdin als die geen terminal is.

## Agent installeren op een node

Maak in de webinterface bij Nodes → Agent installeren een token aan en voer het getoonde commando als root uit op de node:

```sh
curl -fsSL https://clusterforge.example/install/agent.sh | sudo sh -s -- --server https://clusterforge.example --token cfe_...
```

Het script downloadt `cf-agent` van je eigen server, controleert de checksum, meldt de node aan en start de systemd-service `cf-agent`. Bestaat er nog geen node met die hostname, dan maakt de aanmelding er een aan. De agent stuurt elke 10 seconden een heartbeat, elke 15 seconden metrics en elk kwartier (en bij elke nieuwe verbinding) zijn facts: OS, kernel, CPU, geheugen, schijven, netwerk, services, updates, Docker en Keepalived.

```text
cf-agent enroll -server URL -token cfe_...      meldt deze machine aan (doet het installatiescript)
cf-agent run                                    verbindt met ClusterForge (zo start systemd hem)
cf-agent facts                                  toont de facts van deze machine
cf-agent version                                toont de versie
```

De sleutel van de agent staat in `/etc/clusterforge/agent.json`. Intrekken kan bij de node in de webinterface; de verbinding valt dan meteen weg.

## Monitoring

De agent stuurt elke 15 seconden metrics mee over dezelfde verbinding: CPU, load, geheugen, schijven, schijf-I/O, netwerk en temperatuur. De namen volgen node_exporter, en de server voegt de labels `job="clusterforge"`, `instance` en `node` (hostname), `node_id`, `cluster`, `cluster_id` en `env` toe. Bestaande Grafana-dashboards voor node_exporter, zoals Node Exporter Full, werken daardoor met VictoriaMetrics als Prometheus-databron.

De status van nodes en clusters wordt elke 5 seconden opnieuw berekend:

| Status | Node | Cluster |
| --- | --- | --- |
| Gezond | Heartbeat binnen 30 s, geen probleem | Alle actieve nodes gezond, elk VIP precies één eigenaar |
| Verminderd | Heartbeat 30 tot 90 s oud, een schijf voor 90 % vol, een gefaalde service, of een service die enabled is maar niet draait | Minstens één actieve node niet gezond, maar de VIP's zijn in orde |
| Down | Geen heartbeat in 90 s | Een VIP zonder eigenaar, of alle nodes down |
| Split-brain | | Een VIP op twee nodes tegelijk |
| Onbekend | Geen agent | Geen agent op de actieve nodes |

Nodes in onderhoud, draining, opbouw of uit dienst tellen niet mee voor het cluster. Elke statuswissel en elke VIP-verhuis komt in het [logboek](#logboek), en de webinterface werkt live bij.

Een node zonder agent of heartbeat die aan een Proxmox-VM gekoppeld is, krijgt zijn status van Proxmox: staat de VM uit, dan is de node Down met als reden "VM staat uit in Proxmox".

## Proxmox

ClusterForge praat met de REST-API van Proxmox VE (8 of nieuwer) via een API-token. Maak dat token één keer aan op een van je Proxmox-hosts, als root:

```sh
pveum role add ClusterForge --privs "VM.Audit VM.PowerMgmt VM.Snapshot VM.Migrate VM.Allocate VM.Clone VM.Config.CPU VM.Config.Memory VM.Config.Disk VM.Config.Network VM.Config.Cloudinit VM.Config.Options VM.Monitor VM.Backup Sys.Audit Datastore.Audit Datastore.AllocateSpace SDN.Use"
pveum user add clusterforge@pve --comment "ClusterForge"
pveum acl modify / --users clusterforge@pve --roles ClusterForge
pveum user token add clusterforge@pve cf --privsep 0
```

Dat is de rol voor Proxmox VE 8. Op Proxmox VE 9 bestaat `VM.Monitor` niet meer; zet daar `VM.GuestAgent.Audit VM.GuestAgent.FileWrite` in de plaats. Bestaat de rol al uit een eerdere versie, vervang dan `role add` door `role modify` met dezelfde lijst. De rechten om VM's te maken en de guest agent te gebruiken zijn alleen nodig om [clusters uit te rollen](#clusters-uitrollen). `VM.Backup` en `Datastore.AllocateSpace` zijn nodig om de [back-ups](#back-ups) te lezen: zonder die rechten laat Proxmox ze stilzwijgend weg. Een rol uit een eerdere versie mist `VM.Backup`.

Het laatste commando toont het secret één keer. Klik in de webinterface bij Proxmox op "Proxmox koppelen" en vul het API-adres (`https://pve1.example.lan:8006`), de token-id (`clusterforge@pve!cf`) en het secret in. Heeft Proxmox een zelfondertekend certificaat, klik dan naast de vingerafdruk op "Ophalen" en vergelijk de vingerafdruk met die op de host (`openssl x509 -in /etc/pve/local/pve-ssl.pem -noout -fingerprint -sha256`); ClusterForge vertrouwt daarna alleen dat certificaat. Het secret wordt versleuteld met `CF_MASTER_KEY` opgeslagen. Verlies je die sleutel, dan vul je het secret opnieuw in via Bewerken.

Is je Proxmox een cluster, dan is één koppeling genoeg: ClusterForge ziet via elke host alle hosts, VM's, containers en storage. Losse hosts koppel je elk apart.

Elke 20 seconden haalt ClusterForge de stand op. Bij Proxmox zie je de hosts met hun belasting, alle VM's en containers en de storage. Een VM koppel je aan een node met "Koppelen" (of maak er meteen een node van), waarna de node zijn VM-status, host en acties toont. Start, afsluiten, hard uitzetten, herstarten, snapshot maken en live migreren naar een andere host doe je vanaf de node of vanuit het overzicht; viewers kunnen alleen kijken. Verandert een gekoppelde VM buiten ClusterForge om (gestart, gestopt, verhuisd of verdwenen), dan komt dat in het logboek.

Wil je ook de CPU, het geheugen en de schijven van de Proxmox-hosts zelf in grafieken, zet dan ook daar de agent op.

## Back-ups

ClusterForge maakt zelf geen back-ups; dat blijven de back-upjobs van Proxmox (vzdump naar een storage of naar Proxmox Backup Server). Wel leest het elk kwartier welke back-ups er zijn en toont bij Back-ups per VM hoe oud de nieuwste is, op welke storage, hoe groot, en of Proxmox Backup Server hem geverifieerd heeft. Een VM is bewaakt als hij aan een node gekoppeld is, of als je hem op de pagina Back-ups bij "Ook bewaken" zet met zijn VMID en een label: zo houd je ook de container van ClusterForge zelf in het oog.

Is de nieuwste back-up ouder dan de maximale leeftijd, dan staat de VM op "Te oud"; zonder back-up op "Geen back-up". De maximale leeftijd is standaard 30 uur, wat past bij een dagelijkse back-upjob. Een admin stelt hem per cluster in, onderaan de kaart Back-ups op de clusterpagina; voor VM's op de lijst "Ook bewaken" geldt de standaard. Elke wissel komt één keer in het logboek, en de clusters en nodes tonen een back-upbadge. Daaronder staan de VM's en containers die in geen enkele back-upjob van Proxmox zitten. "Nu verversen" leest de back-ups meteen opnieuw, bijvoorbeeld na een back-upjob.

Toont Proxmox geen enkele back-up terwijl er VM's bewaakt worden, dan meldt ClusterForge dat één keer als leesfout in plaats van elke VM op "Geen back-up" te zetten: meestal mist het API-token dan `VM.Backup`. Staat een host uit, dan blijven de back-ups op zijn lokale storage staan zoals ze laatst gelezen zijn.

## Clusters uitrollen

Een template beschrijft een volledig cluster: hoeveel VM's, hoe groot, welke software erop komt en hoe ClusterForge achteraf controleert dat het werkt. De eerste template, Nginx met keepalived, zet twee tot vijf webservers achter één VIP. De templates zitten in de server ingebouwd; bij Templates zie je ze met hun parameters.

Wat je nodig hebt:

1. Een gekoppelde Proxmox, met een token dat VM's mag maken (zie de rol hierboven).
2. Een golden image: een VM-template met Debian 13, cloud-init, de QEMU guest agent en cf-agent. Maak hem als root op een Proxmox-host met het script dat ClusterForge zelf serveert:

   ```sh
   curl -fsSL https://clusterforge.example/install/golden-image.sh | bash -s -- --server https://clusterforge.example --storage local-lvm
   ```

   Het script haalt het cloud-image van Debian, zet er qemu-guest-agent en cf-agent in met `virt-customize` (uit `libguestfs-tools`, dat het zo nodig installeert) en maakt VM-template 9000. Met `--vmid`, `--storage`, `--bridge` en `--name` kies je iets anders, en `--replace` vervangt een eerdere template. Op gedeelde storage (Ceph, NFS) verdeelt ClusterForge de VM's over de hosts; op lokale storage komen ze allemaal op de host van de template.
3. De nieuwe VM's moeten ClusterForge kunnen bereiken: het webadres om zich aan te melden, en poort 4222 voor NATS.

Klik bij Clusters op "Cluster uitrollen", kies de template en vul de naam, de parameters (bij Nginx met keepalived het VIP), de golden image en het netwerk in. Onderaan zie je meteen welke nodes er komen, met hun adres en Proxmox-host; een fout staat bij het veld. Na "Uitrollen" loopt de taak:

1. Per node de VM klonen, cores, geheugen, netwerk en cloud-init instellen, de schijf vergroten en starten.
2. De agents aanmelden: wachten op de guest agent, via de guest agent een eenmalig aanmeldbestand in de VM zetten (`/etc/clusterforge/enroll.json`) en wachten tot cf-agent zich meldt.
3. Per node de stappen van de template: pakketten, bestanden en services. Elke stap meldt of hij iets veranderde.
4. Controleren: een node heeft het VIP en `http://<VIP>/` antwoordt.
5. De nodes worden actief en tellen mee voor de status van het cluster.

Loopt een stap mis, dan staat bij de taak waarom. Los het op en klik op "Opnieuw proberen"; de taak gaat verder bij de stap die misliep. Wil je het cluster niet meer, verwijder dan het cluster en de nodes, en de VM's in Proxmox; ClusterForge ruimt in deze versie niets vanzelf op. Geheimen van een template, zoals het VRRP-wachtwoord, staan versleuteld met `CF_MASTER_KEY` in de database en nooit in de taak of het logboek.

Een eigen golden image kan ook: installeer cf-agent erin met `agent.sh --server https://clusterforge.example --no-enroll`. De agent meldt zich dan aan zodra ClusterForge het aanmeldbestand in de nieuwe VM zet. Zorg ook voor cloud-init en qemu-guest-agent, en maak `/etc/machine-id` leeg voor je er een template van maakt.

## Onderhoud, herstarten en afsluiten

Bij elke node staat de kaart Beheer:

| Actie | Wat er gebeurt |
| --- | --- |
| Onderhoud | De agent zet keepalived uit (stop en disable), ClusterForge wacht tot de VIP's bij een andere node staan en zet de node dan in onderhoud. Hij telt niet mee voor de status van het cluster en keepalived blijft uit, ook na een herstart. |
| Onderhoud beëindigen | Keepalived komt terug zoals het voor het onderhoud stond, en de node is weer actief. |
| Herstarten | Eerst de VIP's weg en de node in onderhoud, dan herstarten, wachten tot de node terug is, keepalived weer aan en de node weer actief. |
| Afsluiten | Zoals herstarten, maar de node blijft uit en in onderhoud. Start hem in Proxmox of op de machine zelf en beëindig daarna het onderhoud. |

Kan geen andere node een VIP overnemen (geen actieve, online node waarop keepalived draait), dan vraagt ClusterForge eerst of je toch door wilt. Staan de VIP's na twee minuten nog niet ergens anders, dan zet de taak keepalived weer aan en blijft de node actief. Elke stap staat met het antwoord van de agent bij Taken, en elke wissel van lifecycle komt in het logboek, met de reden die je opgaf.

Een node zonder agent kun je alleen in en uit onderhoud zetten; de VIP's haal je dan zelf weg.

De agent voert alleen vaste soorten commando's uit: facts verzamelen, keepalived uit- en aanzetten voor onderhoud, herstarten en afsluiten met `systemctl`, en de stappen van een template (pakketten met apt, bestanden, services, gebruikers, mappen en commando's). Omdat hij als root bestanden schrijft, kan wie de server beheert alles op de nodes; bescherm de server en `CF_MASTER_KEY` daarom als een beheerwachtwoord. Hij onthoudt het onderhoud en het laatste herstartcommando in `/var/lib/clusterforge/agent-state.json`, zodat hij na een herstart niet nog eens herstart. Een agent van voor deze versie kan geen commando's uitvoeren; trek hem in en installeer hem opnieuw.

## Taken

Alles wat even duurt, zoals een VM migreren, een node herstarten of een cluster uitrollen, loopt als taak op de achtergrond. Bij Taken zie je wat er loopt en wat er gebeurd is, met per stap het logboek uit Proxmox. Een lopende taak kun je annuleren; ClusterForge stopt dan ook de taak in Proxmox. Valt de server weg tijdens een taak, dan gaat hij na de herstart verder waar hij was, zonder de actie in Proxmox nog eens te starten.

## Logboek

Bij Logboek (alleen voor beheerders) staat alles wat er in ClusterForge veranderde, nieuwste eerst: wie het deed (een gebruiker, de agent op een node, of het systeem namens wie de taak aanvroeg), wanneer, vanaf welk IP-adres en wat er veranderde. Klik een regel open voor de oude en nieuwe waarde per veld, de taak en de ruwe gegevens. Je filtert op periode, persoon, soort, cluster, node (ook verwijderde) en tekst. De filters staan in de adresbalk, zodat je een gefilterde weergave kunt doorsturen. Exporteren geeft dezelfde selectie als NDJSON-bestand (één regel per gebeurtenis, tot 100.000 regels), en elke export komt zelf in het logboek. De pagina van een cluster, node of taak toont onderaan de laatste tien regels.

Regels kunnen niet gewijzigd of verwijderd worden, ook niet met `TRUNCATE` in de database. Een mislukte login bewaart de gebruiker en de reden, maar nooit wat er in het naamveld getypt werd: dat is soms een wachtwoord.

## Ontwikkelen

Nodig: Go 1.26, Node 22 met pnpm, Docker.

```sh
make dev-up            # PostgreSQL en VictoriaMetrics in Docker
make dev-admin USER=jonas
make dev-server        # Go-server op :8080
make dev-web           # Next.js op :3000, stuurt /api door naar :8080
```

Open http://localhost:3000.

| Taak | Commando |
| --- | --- |
| Tests (met integratietests tegen PostgreSQL) | `make test` |
| Lint en typecheck | `make lint` |
| Code opnieuw genereren na een wijziging in `api/openapi.yaml` of `internal/store/queries` | `make generate` |
| Binary met ingebedde webinterface | `make build` |
| `cf-agent` voor amd64 en arm64 (in `bin/agents`, voor `make dev-server`) | `make agent` |
| Container-image | `make docker` |

### Indeling

```text
cmd/clusterforge-server/   main: serve, migrate, admin create
cmd/cf-agent/              de agent op de nodes: enroll, run, facts
internal/auth/             wachtwoorden (argon2id), TOTP, sessies
internal/httpapi/          HTTP-handlers; gen/ is gegenereerd uit api/openapi.yaml
internal/store/            sqlc-queries; queries/ is de bron
internal/events/           append-only eventlog
internal/inventory/        clusters, nodes en VIP's: validatie en wijzigingen met events
internal/agents/           enrollmenttokens, aanmelden en intrekken van agents
internal/agentbus/         ingebedde NATS-server: authenticatie per node, heartbeats en facts
internal/agentdist/        installatiescript en downloads van cf-agent
internal/agent/            code van cf-agent zelf: aanmelden, verbinden, facts en metrics verzamelen
internal/metrics/          metrics naar VictoriaMetrics schrijven en de grafieken opvragen
internal/status/           statusregels voor nodes en clusters, VIP-eigenaars
internal/live/             live updates naar de webinterface (Server-Sent Events)
internal/proxmox/          Proxmox-API: koppelingen, sync elke 20 s en VM-acties; pvefake/ is een nep-Proxmox voor tests
internal/jobs/             taken op de achtergrond met stappen, logboek en annuleren
internal/lifecycle/        acties op nodes via de agent: onderhoud, herstarten, afsluiten
internal/secrets/          versleutelen van geheimen met de masterkey
pkg/protocol/              berichten tussen server en agent
internal/webui/            ingebedde webinterface
migrations/                goose SQL-migraties
api/openapi.yaml           API-specificatie, bron van waarheid
web/                       Next.js-app (static export)
deploy/                    Dockerfile en compose-bestanden
```

Een release maak je met een tag (`git tag v0.1.0 && git push --tags`); GitHub Actions bouwt dan de image `ghcr.io/jonasz1996/clusterforge-server` voor amd64 en arm64.
