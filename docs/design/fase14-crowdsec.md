# Fase 14: CrowdSec via ClusterForge

Gevraagd door Jonas op 2026-11-15: "we maken crowdsec config files voor meest
gebruikte software, webservers, databases. Agents managen crowdsec, ze lezen
geïnstalleerde software uit (bestaat al in CF) en passen de juiste templates
toe. Misschien ook een dashboard zoals CrowdSec maar dan binnen ClusterForge.
Wil dit als enige tool om management te doen, speciaal op maat gemaakt voor de
tools die ik gebruik."

## Doel

1. Op elke node die Jonas aanzet, installeert en beheert ClusterForge
   CrowdSec (engine + lokale API) en de nftables-bouncer, zonder één commando
   op de node.
2. ClusterForge ziet welke software er draait en kiest zelf de juiste
   **profielen**: collections uit de CrowdSec Hub plus de acquisitie (welke
   logs, met welk type). Komt er software bij of verdwijnt ze, dan past
   ClusterForge de node vanzelf aan.
3. Een pagina **CrowdSec** in ClusterForge: alerts, bans, bouncers en profielen
   per node en per cluster, met IP blokkeren/deblokkeren. Het CrowdSec-console
   (app.crowdsec.net) is niet nodig.
4. Een ban op één node geldt meteen op de andere nodes (instelbaar).

Niet in deze fase: updates van apt en Docker volledig automatisch (fase 15;
fase 10 legt daar de basis voor), AppSec/WAF, CrowdSec op OPNsense.

## Opbouw

| Waar | Wat |
| --- | --- |
| Node | CrowdSec uit de officiële repo (packagecloud `crowdsec/crowdsec`), elk met een eigen lokale API op 127.0.0.1:8080 (alleen lokaal); bouncer `crowdsec-firewall-bouncer-nftables` |
| Agent (protocol 28) | nieuw onderwerp `cf.node.<id>.crowdsec`: `status`, `poll`, `decide`. Leest via `cscli … -o json`; installeert zelf niets |
| Installeren/configureren | een run van fase 10 (`noderun`): ClusterForge maakt het script, de uitvoer staat live in de taak `crowdsec.apply` |
| Server | `internal/crowdsec`: catalogus met profielen, detectie, plan per node, taak `crowdsec.apply`, poller (elke minuut) en het delen van bans |
| Database | migratie 00065: instellingen, toestand per node, alerts (90 dagen), actieve bans |
| GUI | pagina CrowdSec (Overzicht, Alerts, Bans, Nodes, Profielen, Instellingen) en kaart CrowdSec op de node |

### Waarom elke node een eigen lokale API

Eén centrale LAPI (op de ClusterForge-LXC) zou elke node van één plek laten
afhangen en vraagt TLS en machine-registratie over het netwerk. Met een LAPI
per node blijft elke node zichzelf beschermen, ook als ClusterForge weg is.
ClusterForge voegt samen wat de nodes zien (dashboard) en verspreidt bans
(zie "Bans delen"). De community-blocklist (CAPI) krijgt elke node zelf.

## Profielen

Een profiel = software herkennen + collections + acquisitie. De catalogus zit
in de server (`internal/crowdsec/catalog.go`), zodat een nieuw profiel geen
agentupdate vraagt.

Herkenning, op wat ClusterForge al weet of de agent bij `status` meldt:

- systemd-units uit de facts (actief of enabled), bv. `nginx`, `mariadb`;
- Docker-containers (naam, image, mounts) uit `status`, bv. image
  `jc21/nginx-proxy-manager`; zo werkt het ook voor Docker-apps die
  ClusterForge niet zelf uitrolde (de bestaande Authentik in `/root`);
- bestanden die bestaan (globs die de server bij `status` meegeeft), bv.
  `/var/www/*/data/nextcloud.log`;
- het template van het cluster (`nginx-proxy-manager`, `authentik`,
  `home-assistant`, `zabbix`).

| Profiel | Herkend aan | Collections | Logs (type) |
| --- | --- | --- | --- |
| Linux + SSH (altijd) | — | `crowdsecurity/linux` (sshd, whitelist-good-actors) | journald `ssh.service`/`sshd.service` (syslog), `/var/log/auth.log` als die er is |
| nginx | unit `nginx` | `crowdsecurity/nginx` (+ base-http, http-cve) | `/var/log/nginx/*.log` (nginx) |
| Apache | unit `apache2` | `crowdsecurity/apache2` | `/var/log/apache2/*.log` (apache2) |
| HAProxy | unit `haproxy` | `crowdsecurity/haproxy` | journald `haproxy.service` + `/var/log/haproxy.log` (syslog) |
| Nginx Proxy Manager | image `jc21/nginx-proxy-manager`, template `nginx-proxy-manager` | `crowdsecurity/nginx-proxy-manager` | `<mount van /data>/logs/*.log` (nginx-proxy-manager) |
| Traefik | image `traefik` / unit | `crowdsecurity/traefik` | container-log (traefik) |
| Caddy | unit of image `caddy` | `crowdsecurity/caddy` | journald / container-log (caddy) |
| MariaDB / Galera | unit `mariadb` | `crowdsecurity/mariadb` | journald `mariadb.service` (syslog) |
| MySQL | unit `mysql` | `crowdsecurity/mysql` | `/var/log/mysql/error.log` (mysql) |
| PostgreSQL | unit `postgresql@*` (Debian) | `crowdsecurity/pgsql` | `/var/log/postgresql/*.log` (postgres) |
| Authentik | image `goauthentik/server`, template `authentik` | `firix/authentik` (community) | container-log van de server-containers (authentik) |
| Home Assistant | image `home-assistant`, template `home-assistant` | `crowdsecurity/home-assistant` | `<mount van /config>/home-assistant.log` (home-assistant) |
| Nextcloud | bestand `…/data/nextcloud.log` | `crowdsecurity/nextcloud` | dat bestand (Nextcloud) |
| Grafana | unit `grafana-server` / image `grafana` | `LePresidente/grafana` | `/var/log/grafana/grafana.log` of container-log (grafana) |
| Gitea, Vaultwarden, Jellyfin, Immich | image | hub-collection van de community | container-log |
| Plex | unit `plexmediaserver` / image | `crowdsecurity/plex` (allowlist van Plex' eigen adressen) | — |
| Proxmox | unit `pvedaemon` | `fulljackz/proxmox` | journald `pvedaemon.service` |

Zabbix heeft geen eigen collection: de web-frontend valt onder Apache of nginx.
PostgreSQL onder Patroni logt via Patroni (programma `patroni`), dat de
pgsql-parser niet herkent: het profiel meldt dat als beperking in plaats van
een acquisitie die niets doet. Databases staan bij Jonas niet op het
internet; de grote winst zit bij SSH, NPM, Authentik en Home Assistant.

Per node kan Jonas een profiel **uitzetten** of **erbij zetten** (bv. als de
herkenning iets mist). Dat staat in `crowdsec_nodes.disabled_profiles` en
`extra_profiles`.

### Wat ClusterForge op de node schrijft

Alles van ClusterForge heeft `cf-` in de naam; de rest van `/etc/crowdsec`
blijft van CrowdSec en de pakketten.

- `/etc/crowdsec/acquis.d/cf-<profiel>.yaml` per profiel; oude `cf-*.yaml`
  die niet meer gelden, gaan weg.
- `/etc/crowdsec/parsers/s02-enrich/cf-whitelist.yaml`: de eigen netwerken
  (standaard 10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16, 127.0.0.0/8, ::1 en
  het adres van de ClusterForge-server). Zo bant CrowdSec nooit de proxy
  voor een backend, een node van het cluster of Jonas' pc. Aanpasbaar bij
  Instellingen.
- `/etc/crowdsec/bouncers/crowdsec-firewall-bouncer.yaml.local`: de
  nftables-hooks; met Docker ook `forward`, anders raakt een ban de
  gepubliceerde poorten van containers niet.
- `/var/lib/clusterforge/crowdsec.json`: de hash en de collections die
  ClusterForge zette; een collection die ClusterForge zette en niet meer
  nodig is, gaat weg (`cscli collections remove`), een die Jonas zelf
  installeerde nooit.

### Het script (taak `crowdsec.apply`)

1. Repo en sleutel van packagecloud, `apt-get install crowdsec` (alleen als
   het er niet is) en, als de bouncer aan staat, `crowdsec-firewall-bouncer-nftables`.
2. `cscli hub update`, collections installeren/weghalen.
3. Bestanden schrijven met een kopie van de vorige; `crowdsec -t` test de
   configuratie. Faalt de test, dan gaat de vorige terug en faalt de taak
   (CrowdSec blijft draaien met de oude configuratie).
4. `systemctl enable --now crowdsec` en `reload` (of `restart` na nieuwe
   collections); de bouncer `restart` bij een gewijzigde `.local`.
5. Hash wegschrijven.

Het script is idempotent; nog eens toepassen verandert niets.

### Automatisch

Na elke nieuwe facts-set en elke `status` rekent de server het plan per
beheerde node opnieuw uit. Wijkt de hash af van wat toegepast is, dan start
hij `crowdsec.apply` (instelling "Automatisch toepassen", standaard aan, ook
op prod: het script voegt alleen logs en collections toe en test eerst; de
whitelist houdt het eigen netwerk vrij). Een node met een mislukte apply
probeert het pas na 1 uur opnieuw, of meteen met "Toepassen".

Aanzetten gebeurt per node, per cluster of voor alle nodes ("CrowdSec op alle
nodes"), op de pagina CrowdSec → Nodes. Alleen admin. Uitzetten haalt
CrowdSec niet weg (stopt het beheer); "Verwijderen" doet `apt-get purge` van
crowdsec en de bouncer.

Een agent met protocol < 28 krijgt "agent te oud" (Nodes → Agents bijwerken).

## Gegevens naar ClusterForge (poller)

Elke minuut, per beheerde node met agent online:

- `poll {since}` → alerts sinds de laatst geziene (min 2 minuten overlap,
  ontdubbeld op `(node, alert_id, started_at)`), de actieve lokale bans
  (`cscli decisions list`, zonder CAPI/lijsten) en de grootte van de
  community-blocklist.
- Elke 5 minuten `status`: versie, diensten, collections, bouncers,
  acquisitiecijfers (gelezen/geparsed per bron), containers.

Alerts van de community-blocklist (`update : +N IPs`) komen niet in de lijst.
Alerts blijven 90 dagen.

## Bans delen

Instelling: **uit**, **binnen het cluster**, **alle beheerde nodes**
(standaard). Ziet de poller een nieuwe ban met origin `crowdsec` (een
scenario, geen hand-ban) op node A, dan zet ClusterForge op elke andere node
(volgens de instelling) dezelfde ban met de resterende duur en reden
`cf:<scenario>@<node>` (origin `cscli`). Een ban met origin `cscli` wordt nooit
verder gedeeld, dus geen lus. `crowdsec_shared` onthoudt wat al gedeeld is.

Handmatig (pagina Bans, alleen admin): **IP blokkeren** (IP of range, duur,
reden; op een node, een cluster of alle beheerde nodes) en **Deblokkeren**
(op alle nodes waar het IP geband is). Beide in het logboek.

## Dashboard

Pagina **CrowdSec** (menu Beveiliging):

- **Overzicht**: tegels (beheerde nodes, CrowdSec draait, alerts 24 u, actieve
  bans, community-blocklist, bouncers), alerts per uur (24 u), top-scenario's,
  top-IP's met land/AS, waarschuwingen (CrowdSec of bouncer staat stil, een
  bron leest niets, apply mislukt, agent te oud).
- **Alerts**: tabel met tijd, node, cluster, scenario, IP, land, AS, events,
  ban; filter op node/cluster/scenario/IP.
- **Bans**: actieve bans samengevoegd per IP (op welke nodes, tot wanneer,
  reden, gedeeld of eigen) met Deblokkeren; IP blokkeren.
- **Nodes**: per node beheerd ja/nee, versie, CrowdSec/bouncer actief,
  profielen (herkend / toegepast / uit), laatste apply, fout; Aanzetten,
  Toepassen, Uitzetten, Verwijderen; per cluster en "alle nodes".
- **Profielen**: de catalogus (wat herkend wordt, welke collections, welke
  logs) en op welke nodes elk profiel staat.
- **Instellingen**: automatisch toepassen, bouncer standaard aan, bans delen,
  whitelist.

Kaart **CrowdSec** op de node: stand, profielen, bronnen met
gelezen/geparsed, laatste alerts, knop naar de pagina.

## Protocol 28

`cf.node.<id>.crowdsec`, verzoek/antwoord (`crowdsec_request`/
`crowdsec_result`):

- `status {probes}` → `CrowdSecStatus`
- `poll {since}` → alerts, bans, blocklist
- `decide {add|delete}` → aantal

Rechten: het onderwerp komt bij de subscribe-rechten van de agent.

## Mijlpalen (in deze PR)

- M1 protocol 28 + agent (`status`, `poll`, `decide`) met tests.
- M2 `internal/crowdsec`: catalogus, detectie, plan, script; tests op de
  profielen van Jonas' stack (NPM, Authentik, HA, Galera, Zabbix-web).
- M3 migratie 00065, taak `crowdsec.apply`, poller, bans delen, API.
- M4 GUI: pagina CrowdSec, kaart op de node, README.

## Aanvulling: CrowdSec Console (protocol 31)

Jonas wil ook de Console-aanmelding via de agent. De enroll-sleutel staat in `crowdsec_settings.console_key_enc`
(AES-GCM met de masterkey, aad `crowdsec:console`), met een HMAC-vingerafdruk in `console_key_hash`. Per node
onthoudt `crowdsec_nodes.console_hash` met welke sleutel hij aangemeld is (migratie 00068). De poller meldt een
beheerde node met protocol 31 aan als CrowdSec draait en de vingerafdruk verschilt: op `enroll` van onderwerp
`crowdsec`, met de sleutel alleen in het NATS-verzoek. De agent draait `cscli console enroll --name <host>
--overwrite <sleutel>` en herstart CrowdSec; een fout gaat terug zonder de sleutel. Een mislukte aanmelding wacht
een uur (`RetryFailed`). API: `PUT /crowdsec/console` (sleutel, leeg = weg) en `POST /crowdsec/console/enroll`
(alle beheerde nodes of één node opnieuw).
