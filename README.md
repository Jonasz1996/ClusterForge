# ClusterForge

Eén webinterface om traditionele Linux HA-clusters (Proxmox, Debian/Ubuntu, Keepalived, gedeelde databases, Docker, cronclusters) te beheren, monitoren, deployen en onderhouden. Geen Kubernetes-kloon.

Het technisch ontwerp staat in [docs/design/mvp-fase-1.md](docs/design/mvp-fase-1.md).

## Stand van zaken

| Mijlpaal | Inhoud | Status |
| --- | --- | --- |
| 1. Fundament | Server, database, login met TOTP, webinterface, CI | klaar |
| 2. Inventory | Clusters, nodes, VIP's | klaar |
| 3. Agent | Enrollment, heartbeat, facts | klaar |
| 4. Monitoring | Metrics, status, dashboards | gepland |
| 5. Proxmox | Sync en VM-acties | gepland |
| 6. Node lifecycle | Reboot, maintenance, drain | gepland |
| 7. Templates en deployment | Clusters uit templates | gepland |

## Draaien met Docker Compose

Stap voor stap in een Debian-container op Proxmox: [docs/install-proxmox-lxc.md](docs/install-proxmox-lxc.md).

```sh
cd deploy
cp .env.example .env          # zet minstens POSTGRES_PASSWORD
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

Het script downloadt `cf-agent` van je eigen server, controleert de checksum, meldt de node aan en start de systemd-service `cf-agent`. Bestaat er nog geen node met die hostname, dan maakt de aanmelding er een aan. De agent stuurt elke 10 seconden een heartbeat en elk kwartier (en bij elke nieuwe verbinding) zijn facts: OS, kernel, CPU, geheugen, schijven, netwerk, services, updates, Docker en Keepalived.

```text
cf-agent enroll -server URL -token cfe_...      meldt deze machine aan (doet het installatiescript)
cf-agent run                                    verbindt met ClusterForge (zo start systemd hem)
cf-agent facts                                  toont de facts van deze machine
cf-agent version                                toont de versie
```

De sleutel van de agent staat in `/etc/clusterforge/agent.json`. Intrekken kan bij de node in de webinterface; de verbinding valt dan meteen weg.

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
internal/agent/            code van cf-agent zelf: aanmelden, verbinden, facts verzamelen
pkg/protocol/              berichten tussen server en agent
internal/webui/            ingebedde webinterface
migrations/                goose SQL-migraties
api/openapi.yaml           API-specificatie, bron van waarheid
web/                       Next.js-app (static export)
deploy/                    Dockerfile en compose-bestanden
```

Een release maak je met een tag (`git tag v0.1.0 && git push --tags`); GitHub Actions bouwt dan de image `ghcr.io/jonasz1996/clusterforge-server` voor amd64 en arm64.
