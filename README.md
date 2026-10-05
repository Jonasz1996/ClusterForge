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
| 6. Node lifecycle | Reboot, maintenance, drain | gepland |
| 7. Templates en deployment | Clusters uit templates | gepland |

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

Nodes in onderhoud, draining, opbouw of uit dienst tellen niet mee voor het cluster. Elke statuswissel en elke VIP-verhuis komt in de activiteitenlog, en de webinterface werkt live bij.

Een node zonder agent of heartbeat die aan een Proxmox-VM gekoppeld is, krijgt zijn status van Proxmox: staat de VM uit, dan is de node Down met als reden "VM staat uit in Proxmox".

## Proxmox

ClusterForge praat met de REST-API van Proxmox VE (8 of nieuwer) via een API-token. Maak dat token één keer aan op een van je Proxmox-hosts, als root:

```sh
pveum role add ClusterForge --privs "VM.Audit VM.PowerMgmt VM.Snapshot VM.Migrate Sys.Audit Datastore.Audit Datastore.AllocateSpace"
pveum user add clusterforge@pve --comment "ClusterForge"
pveum acl modify / --users clusterforge@pve --roles ClusterForge
pveum user token add clusterforge@pve cf --privsep 0
```

Het laatste commando toont het secret één keer. Klik in de webinterface bij Proxmox op "Proxmox koppelen" en vul het API-adres (`https://pve1.example.lan:8006`), de token-id (`clusterforge@pve!cf`) en het secret in. Heeft Proxmox een zelfondertekend certificaat, klik dan naast de vingerafdruk op "Ophalen" en vergelijk de vingerafdruk met die op de host (`openssl x509 -in /etc/pve/local/pve-ssl.pem -noout -fingerprint -sha256`); ClusterForge vertrouwt daarna alleen dat certificaat. Het secret wordt versleuteld met `CF_MASTER_KEY` opgeslagen. Verlies je die sleutel, dan vul je het secret opnieuw in via Bewerken.

Is je Proxmox een cluster, dan is één koppeling genoeg: ClusterForge ziet via elke host alle hosts, VM's, containers en storage. Losse hosts koppel je elk apart.

Elke 20 seconden haalt ClusterForge de stand op. Bij Proxmox zie je de hosts met hun belasting, alle VM's en containers en de storage. Een VM koppel je aan een node met "Koppelen" (of maak er meteen een node van), waarna de node zijn VM-status, host en acties toont. Start, afsluiten, hard uitzetten, herstarten, snapshot maken en live migreren naar een andere host doe je vanaf de node of vanuit het overzicht; viewers kunnen alleen kijken. Verandert een gekoppelde VM buiten ClusterForge om (gestart, gestopt, verhuisd of verdwenen), dan komt dat in de activiteitenlog.

Wil je ook de CPU, het geheugen en de schijven van de Proxmox-hosts zelf in grafieken, zet dan ook daar de agent op.

## Taken

Alles wat even duurt, zoals een VM migreren, loopt als taak op de achtergrond. Bij Taken zie je wat er loopt en wat er gebeurd is, met per stap het logboek uit Proxmox. Een lopende taak kun je annuleren; ClusterForge stopt dan ook de taak in Proxmox. Valt de server weg tijdens een taak, dan gaat hij na de herstart verder waar hij was, zonder de actie in Proxmox nog eens te starten.

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
internal/secrets/          versleutelen van geheimen met de masterkey
pkg/protocol/              berichten tussen server en agent
internal/webui/            ingebedde webinterface
migrations/                goose SQL-migraties
api/openapi.yaml           API-specificatie, bron van waarheid
web/                       Next.js-app (static export)
deploy/                    Dockerfile en compose-bestanden
```

Een release maak je met een tag (`git tag v0.1.0 && git push --tags`); GitHub Actions bouwt dan de image `ghcr.io/jonasz1996/clusterforge-server` voor amd64 en arm64.
