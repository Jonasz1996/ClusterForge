# Fase 36: Homepage-API v2, alle info per app via ClusterForge

Gevraagd op 2026-11-17 door Jonas: "Nu dat op alle nodes een ClusterForge-agent
staat, kan de agent de belangrijkste items van die bepaalde applicatie
uitlezen en via de API presenteren, gelinkt aan de juiste app, container en
cluster. Zo moet homepage enkel API-calls doen naar ClusterForge en heeft hij
alle info: applicatiestats, CF-agent, Zabbix, CrowdSec, OPNsense..."

Dit document is het onderzoek en stappenplan. Er wordt pas gebouwd als fase 33
(Zabbix-dashboards, PR #139), fase 34 (OPNsense, PR #141) en fase 35 (NPM +
Authentik, PR #142) af zijn, op de hoogste stam daarvan. Daarna meteen bouwen,
zonder nieuwe akkoordronde.

## Doel in één zin

Elke tegel op Jonas' homepage haalt met **één token en één URL** bij
ClusterForge alles op wat er over die app te weten is: de stats van de app
zelf (door de agent lokaal uitgelezen), waar ze draait (cluster, node, VIP,
container), hoe gezond ze is, en wat Zabbix, CrowdSec, OPNsense, NPM,
Authentik, PBS en Proxmox erover weten. Homepage heeft dan geen eigen
API-sleutels van Plex, Nextcloud, Proxmox, ... meer nodig.

## Uitgangspunten

- **Alles via de GUI.** Op de nodes niets extra: de agent bestaat al. Welke
  apps, welke velden op de tegel, volgorde, groepen, tokens: allemaal in
  ClusterForge instellen.
- **Sleutels blijven op de node.** Waar een app haar sleutel lokaal bewaart
  (Plex `Preferences.xml`, `config.xml` van *arr-apps, Nextcloud `occ`), leest
  de agent die zelf en stuurt alleen de uitkomst door. Waar dat niet kan (Home
  Assistant long-lived token) plak je de sleutel één keer in de GUI; die wordt
  versleuteld bewaard (`internal/secrets`) en alleen naar de agent gestuurd.
- **Alleen lezen.** De v2-API kan niets wijzigen. Acties (herstarten, ...)
  blijven in ClusterForge zelf, achter TOTP.
- **v1 blijft werken.** `/api/dashboard/v1` (fase 20 + nodes-API fase 31 + SOC)
  verandert niet; bestaande tegels blijven gewoon draaien.
- **Kwaliteit boven kwantiteit.** Liever tien apps die echt goed werken dan
  vijftig half. Eigen apps kunnen via een generieke "eigen controle" in de
  GUI (M6).

## Wat er al is (onderzoek in de code)

| Nodig | Al aanwezig | Ontbreekt |
|---|---|---|
| Token-API voor homepage | `/api/dashboard/v1` met `cfa_`-tokens per cluster, cache 15 s, `?strict=1` (`internal/httpapi/dashboard*.go`, migratie 00069) | per-app-tegels, bronnen, scopes per bron |
| Stand van apps uit templates | `facts.apps` uit `/var/lib/clusterforge/app-status/<app>.json` (fase 29, `internal/agent/apps.go`, `internal/apps`) | apps die niet uit een template komen; app-eigen stats (streams, gebruikers, ...) |
| Containers per node | `internal/dockerhost` (fase 21/28): status, image, poorten, health, herstarts, cpu/geheugen | koppeling container → app |
| Diensten en poorten | `Facts.Services`, `Facts.Listeners` (`netfacts.go`), fase 35 dienstenkaart | — (hergebruiken) |
| VIP-eigenaar, clusterstatus | keepalived-facts, `internal/status`, health score | — |
| Node-metrics | VictoriaMetrics, nodes-API (fase 31) | — |
| Zabbix | `internal/zabbixmon` (fase 33): snapshot elke 15 min, `zabbix_host_links` node ↔ host | per-app-filter |
| CrowdSec | fase 14 (`internal/crowdsec`, agent `cscli`) en SOC (fase 25) | per-app/per-node samenvatting in v2 |
| PBS | fase 32 (`internal/pbs`): taken, datastores, dekking per node | samenvatting in v2 |
| Proxmox | fase 26 (`internal/pvehost`, `internal/proxmox`) | samenvatting in v2 |
| OPNsense | fase 34 (in aanbouw) | samenvatting in v2 |
| NPM + Authentik | fase 35 (in aanbouw): proxy hosts, certificaten, providers, dienstenkaart | link app ↔ publieke URL ↔ Authentik-app |
| Homepage-kant | Jonas' homepage: integratie `json` (`backend/app/integrations/jsonapi.py`): url + Bearer-token + max 8 velden met dot-pad, format number/percent/bytes; integraties hebben `summary()` (velden) en `detail()` (secties kv/bars/table) | v2-formaat dat daar rechtstreeks op past |

Besluit: **geen nieuwe monitoring naast de bestaande**. Fase 36 is een laag die
(1) de agent per app laat meten wat nu nog ontbreekt en (2) alles wat
ClusterForge al weet per app bundelt in één tegelformaat.

## Het centrale begrip: een app

Een **app** in ClusterForge = één ding dat Jonas als tegel wil zien.

| Veld | Voorbeeld |
|---|---|
| `slug` | `plex`, `nextcloud`, `media-platform`, `homepage` |
| naam, icoon, groep, volgorde | "Plex", `plex.png`, "Media", 1 |
| soort | `plex` (ingebouwde meter), `container`, `systemd`, `http`, `eigen` |
| waar | cluster (optioneel), node(s), container(naam of compose-dienst), systemd-unit, poort |
| VIP | van het cluster; ClusterForge weet welke node eigenaar is |
| publieke URL | uit NPM (fase 35) of zelf ingevuld |
| Authentik-app | uit fase 35, indien gekoppeld |
| Zabbix-host(s) | via `zabbix_host_links` (fase 33) |
| velden op de tegel | gekozen in de GUI, max 8 (homepage-limiet), volgorde instelbaar |

**Ontdekken in plaats van invullen.** ClusterForge stelt apps voor uit wat er
al is: clusters uit templates (homepage, media-platform, NPM, Authentik,
Zabbix, Nextcloud, Galera, Patroni, ...), containers (image `plexinc/pms-docker`
→ Plex, `lscr.io/linuxserver/sonarr` → Sonarr, ...), systemd-units
(`plexmediaserver`, `home-assistant@`, `adguardhome`, ...) en luisterende
poorten (32400 → Plex).

**Automatisch beheerd (bijsturing Jonas 17/11).** Herkende apps worden
**zonder klikken** toegevoegd, krijgen meters, een groep, een icoon en
standaard tegelvelden, en verschijnen vanzelf als tegel op de homepage.
Verdwijnt een app (container weg, node afgebouwd), dan verdwijnt ook de tegel
(eerst 24 u "weg?"-markering, daarna weg). De GUI is er alleen om bij te
sturen: verbergen, hernoemen, andere velden, groep, koppeling corrigeren. Wat
Jonas bijstuurt blijft staan; de rest houdt ClusterForge zelf bij. Alleen wat
niet zeker herkend wordt komt in *Twijfel* (één klik: app of negeren).

Eén app op een cluster volgt automatisch de VIP-eigenaar of de actieve node;
drie losse Plex-servers worden één app met drie instanties (één tegel, per
instantie een regel in het detailscherm).

Herkenning gebeurt met een ingebouwde catalogus (image-naam, unit-naam,
poort, bestand zoals `/var/www/nextcloud/occ`) die per CF-versie groeit. De
lijst met apps die Jonas na het installeren van alle agents doorgeeft, wordt
de eerste invulling van die catalogus.

## Wie meet wat

**1. De agent, lokaal op de node (nieuw, M1-M2).** De server stuurt per node
een lijst *meters* (soort + parameters, nooit vrije shell). De agent voert ze
elke 60 s uit (instelbaar 15 s-15 min) en stuurt de uitkomst als
`appstats`-bericht. Alleen de node die de app echt draait meet (VIP-eigenaar,
actieve app uit `facts.apps`, of de node met de container). Elke meter heeft
een time-out van 5 s en een maximale antwoordgrootte; een kapotte meter breekt
de rest niet.

Generieke meters (voor elke app):

| Meter | Velden |
|---|---|
| `http` | bereikbaar, statuscode, antwoordtijd, certificaat dagen geldig |
| `container` | status, health, herstarts 24 u, uptime, cpu %, geheugen, image-tag, update beschikbaar (fase 28) |
| `systemd` | actief, sinds, herstarts, geheugen |
| `port` | luistert ja/nee |

Ingebouwde app-meters (eerste set, gekozen uit wat Jonas draait):

| App | Hoe de agent leest | Belangrijkste velden |
|---|---|---|
| Plex (3x) | token uit `Preferences.xml`, API op 127.0.0.1:32400 | streams nu, transcodes, bandbreedte, bibliotheken (films/series/muziek), versie, update |
| Nextcloud (nc1-3) | `occ` als www-data / `serverinfo`-API | gebruikers, actief 24 u, bestanden, opslag gebruikt/vrij, versie, app-updates, achtergrondtaken laatste run |
| Media-platform | bestaand `app-status/media-platform.json` + SeaweedFS (fase 29) | actief op node, versie, volumes, onder-gerepliceerd, opslag |
| Homepage | `/api/healthz` + eigen DB (fase 22) | worker leeft, aantal tegels, checks rood |
| Home Assistant | REST-API met token uit de GUI | versie, entiteiten, onbeschikbaar, automatiseringen aan, updates |
| MariaDB/Galera | bestaande db-meters (fase 4/5) | clustergrootte, wsrep-status, verbindingen, queries/s |
| PostgreSQL/Patroni | bestaande db-meters | leider, replica-lag, verbindingen |
| Zabbix-server | (komt van de server, zie 2) | — |
| VaultX | http + container | bereikbaar, versie |
| *arr / Jellyfin / AdGuard / ... | `ApiKey` uit `config.xml` / lokale API | wachtrij, ontbrekend, schijf, ... (alleen als Jonas ze heeft, zie vraag 1) |

**2. De server, uit wat ClusterForge al weet (M3).** Geen extra belasting:
de laatste snapshot/stand van elke module wordt per app en per node gefilterd.

| Bron | Per app / node | Globaal |
|---|---|---|
| CF-agent | online, versie, protocol, laatste melding, node-cpu/ram/disk/load | agents online / totaal, te oud |
| Cluster | gezondheid %, VIP-eigenaar, nodes ok, onderhoud | clusters per status |
| Zabbix | open problemen per ernst voor de gekoppelde hosts | problemen per ernst |
| CrowdSec | bans/alerts 24 u op de nodes van de app (en op NPM voor haar URL) | actieve bans, alerts 24 u, top-scenario |
| OPNsense | (fase 34) | CARP-master, gateways, WAN in/uit, states, updates |
| NPM | proxy host van de app: online, certificaat dagen, SSL geforceerd | hosts, certificaten < 14 d |
| Authentik | app gekoppeld, logins 24 u, mislukt 24 u | outposts gezond, gebruikers |
| PBS | laatste back-up van de VM/CT van de app, ouderdom, geslaagd | datastore %, mislukte taken 24 u |
| Proxmox | VM/CT van de node: host, status, uptime | hosts, VM's/CT's draaiend |
| Aandacht | bestaande "Aandacht nodig" gefilterd op de app | totaal rood/amber |

## API v2

Basis `/api/dashboard/v2`, dezelfde `cfa_`-tokens (Bearer of `X-API-Key`),
cache 15 s, `ETag`, `?strict=1` geeft 503 als de status niet `ok` is (zodat de
tegelcheck van homepage rood kleurt).

| Endpoint | Inhoud |
|---|---|
| `GET /` | globaal: clusters/nodes/agents/apps per status, aandacht, bronnen kort |
| `GET /apps` | alle apps als tegels (zie formaat), `?group=`, `?cluster=` |
| `GET /apps/{slug}` | één tegel, volledig, met secties voor het detailscherm |
| `GET /clusters`, `/clusters/{slug}` | zoals v1, plus de apps van het cluster |
| `GET /nodes`, `/nodes/{host}` | zoals nodes-API, plus apps/containers op de node |
| `GET /sources/{bron}` | `agents`, `zabbix`, `crowdsec`, `opnsense`, `npm`, `authentik`, `pbs`, `proxmox`, `docker`, `soc` |
| `GET /homepage/layout` | tegels die homepage moet tonen (beheerd door CF), met ETag |
| `GET /homepage/services.yaml` | kant-en-klare homepage-config voor de tegels van dit token (groepen, volgorde, url, icoon, velden) |

**Eén tegelformaat** voor apps én bronnen, gebouwd op het model van Jonas'
homepage (`summary` = velden, `detail` = secties):

```json
{
  "slug": "plex",
  "name": "Plex",
  "status": "ok",            // ok | warn | down | unknown (te oud = unknown)
  "reason": "",
  "url": "https://plex.jbogaert.be",
  "where": {"cluster": "plex", "node": "plex1", "vip": "192.168.x.y", "container": "plex", "unit": null},
  "updated_at": "2026-11-17T05:30:00Z",
  "fields": [
    {"key": "streams", "label": "Streams", "value": 2, "format": "number", "level": "ok"},
    {"key": "transcodes", "label": "Transcodes", "value": 1, "format": "number"},
    {"key": "cpu", "label": "CPU", "value": 12.5, "format": "percent"}
  ],
  "values": {"streams": 2, "transcodes": 1, "cpu": 12.5, "zabbix_problems": 0, "crowdsec_bans_24h": 3},
  "sources": {"agent": {...}, "zabbix": {...}, "crowdsec": {...}, "npm": {...}, "pbs": {...}},
  "sections": [{"type": "kv", "title": "Bibliotheken", "items": [...]}, {"type": "bars", ...}, {"type": "table", ...}]
}
```

- `fields` = de velden die in de GUI voor de tegel gekozen zijn (max 8, in
  volgorde), al opgemaakt.
- `values` = **alle** velden plat, zodat de bestaande `json`-integratie van
  homepage met een eenvoudig pad werkt (`values.streams`), zonder
  array-indexen.
- `sources` en `sections` voor een detailscherm.
- Geen geheimen, geen interne paden; IP's alleen als het token
  `show_addresses` heeft (standaard aan, Jonas' eigen netwerk).

**Tokens.** Bestaande tokens krijgen er scopes bij (migratie): per cluster
(bestaat), per app, per bron, en `v1`/`v2`. Standaard: alles lezen. Bijhouden
laatst gebruikt (bestaat).

## Homepage-kant

Twee mogelijkheden, allebei met alleen het CF-token:

- **A. Zonder homepage aan te passen.** ClusterForge genereert per tegel een
  `json`-integratie (url `/api/dashboard/v2/apps/plex`, `fields` met
  `values.<key>`-paden). Werkt meteen, maar alleen de 8 velden op de tegel,
  geen detailscherm.
- **B. `clusterforge`-integratie + tegelsynchronisatie in de homepage-repo**
  (gekozen door Jonas, zelfde stijl als `pbs.py`/`proxmox.py`): in homepage
  één keer ClusterForge-URL + token instellen. Daarna **beheert ClusterForge
  de tegels**: homepage haalt elke minuut `GET /api/dashboard/v2/homepage/layout`
  (groepen, volgorde, tegels met `app:`-sleutel, icoon, link, ETag) en maakt,
  wijzigt en verwijdert zelf de tegels die van ClusterForge komen (gemarkeerd
  `managed_by: clusterforge`). Tegels die Jonas zelf in homepage maakte
  blijven onaangeroerd. `summary()` = `fields`, `detail()` = `sections`,
  status en link komen mee. Daarvoor moet de homepage-repo aan dit project
  gekoppeld worden (nu kan ik er alleen in lezen).

Variant A (`json`-integratie, per tegel de 8 velden) blijft als terugval
zolang B er niet is.

## GUI

**Instellingen → Homepage-API** (vervangt het huidige "Dashboard-API"-blok,
v1-tokens blijven zichtbaar):

- **Apps**: lijst met status, waar, laatste meting, "automatisch" of
  "bijgestuurd"; tabblad *Twijfel* (alleen wat niet zeker herkend is). Per app,
  alleen om bij te sturen (standaard ingevuld): naam, icoon, groep, volgorde,
  publieke URL, koppeling cluster/node/container/unit, meters aan/uit,
  interval, sleutel plakken (alleen waar nodig), **velden op de tegel kiezen**
  (sleep-volgorde, max 8, voorbeeldwaarde ernaast), voorbeeld van de tegel en
  de JSON.
- **Bronnen**: per bron aan/uit en welke velden globaal (bv. CrowdSec-tegel).
- **Tokens**: maken met scopes, `services.yaml`-blok kopiëren, testen.
- Op de clusterpagina en nodepagina: kaartje "Apps" met dezelfde velden.

## Agentprotocol en migraties

- **Protocol 52** (fase 35 neemt 51): nieuw subject `appstats` (server → agent:
  meterlijst met versie; agent → server: uitkomsten). Oudere agents: alleen
  serverbronnen + generieke info uit facts; GUI toont "agent bijwerken".
- **Migraties 00125-00129** (fase 35: 00115-00124): `00125_dashboard_apps`
  (apps, koppelingen, meters, tegelvelden, groepen), `00126_dashboard_app_suggestions`
  (genegeerde voorstellen), `00127_api_token_scopes`. Laatste meetwaarden in
  geheugen + één rij per app (`app_stats_latest`, voor na herstart); historiek
  voor grafiekjes in VictoriaMetrics (`cf_app_<key>{app=...}`), niet in
  PostgreSQL.
- Nummers worden bij het bouwen nog afgestemd met de threads van fase 33-35.

## Veiligheid

- Meters zijn een vaste lijst soorten met gevalideerde parameters (zoals de
  PBS/NPM-allowlist): geen vrije commando's, URL's alleen naar 127.0.0.1, het
  container-IP of het eigen VIP.
- Sleutels die de agent lokaal leest verlaten de node niet. GUI-sleutels
  versleuteld, nooit terug in de API of de logs.
- v2 is alleen lezen; foute tokens worden afgeremd (bestaande limiter), en audit van token
  aanmaken/intrekken.

## Mijlpalen

Elke mijlpaal is een commit met tests (Go-unit + httpapi-tests +
Playwright-schermen) en wordt apart gemeld zodra ze klaar is.

| M | Wat | Klaar als |
|---|---|---|
| M0 | Apps-model, herkenningscatalogus, automatisch toevoegen/verwijderen, GUI Apps + Twijfel, migraties 00125-00126 | Jonas' apps staan er vanzelf, correct gekoppeld |
| M1 | Protocol 52 `appstats`, generieke meters (http, container, systemd, port), actieve node bepalen | elke app heeft status + basisvelden |
| M2 | Ingebouwde meters: Plex, Nextcloud, media-platform, homepage, Home Assistant, Galera, Patroni | echte appstats op de tegels |
| M3 | Serverbronnen per app en globaal: agent, cluster, Zabbix, CrowdSec, PBS, Proxmox, aandacht; OPNsense/NPM/Authentik zodra fase 34/35 er zijn | `sources` gevuld |
| M4 | API v2 (endpoints, tegelformaat, scopes 00127, ETag, strict), OpenAPI, tests; v1 ongewijzigd | homepage kan alles via één token |
| M5 | `/homepage/layout`, GUI bijsturen (velden, groepen, verbergen), homepage-integratie B met tegelsynchronisatie | homepage toont vanzelf alle apps, zonder andere sleutels |
| M6 | Eigen controle via de GUI (http-json met pad-velden, sleutel uit bestand op de node of uit de GUI), grafiekjes (VictoriaMetrics), te-oud-afhandeling | ook apps zonder ingebouwde meter |

## Antwoorden van Jonas (17/11 05:38 UTC)

1. **Apps:** Jonas installeert de agents op alle servers die hij houdt en geeft
   daarna de lijst met apps. Die lijst vult de herkenningscatalogus (M0/M2).
2. **Homepage-integratie B: ja**, met als eis dat tegels automatisch herkend en
   door ClusterForge beheerd worden (verwerkt hierboven). Homepage-repo moet
   nog gekoppeld worden.
3. **Sleutels lokaal lezen:** geen antwoord; voorstel blijft: standaard aan,
   per app uit te zetten.

Breder doel: de ClusterForge-agents beheren en monitoren het hele homelab;
alles opvolgen en beheren vanuit ClusterForge. Fase 36 is daarin de laag die
elke app herkent en zichtbaar maakt.
