# Fase 35: Nginx Proxy Manager en Authentik automatisch beheren

Gevraagd op 2026-11-17 door Jonas: "Agents lezen uit welke containers/services
er draaien op welke poorten/VIP, en beheren NPM/Authentik zoals bij het
dashboard. jbogaert.be als domein; in Instellingen kan je het domein voor
algemeen beheer veranderen."

Dit document is het onderzoek en stappenplan. Er wordt pas gebouwd als fase 33
(Zabbix-dashboards, PR #139) en fase 34 (OPNsense, PR #141) klaar zijn, op de
hoogste stam van die twee.

## Doel in één zin

ClusterForge weet van elke dienst op welke node, poort en VIP ze draait, en
kan ze met één knop publiceren als `<naam>.jbogaert.be` in NPM (proxy host +
certificaat) en desgewenst achter Authentik zetten (applicatie + proxy
provider + outpost + forward auth in NPM), met controle achteraf en terugdraaien.

## Uitgangspunten

- **Alles via de GUI.** Op de nodes niets extra; de agent is al geïnstalleerd.
- **Bestaande toestand respecteren.** Jonas heeft al ±105 proxy hosts in NPM
  (cluster VIP 192.168.102.244, nodes .245/.246, fase 11) en ±100 proxy
  providers in Authentik (cluster VIP 192.168.103.244, nodes .241-.243, fase
  13). ClusterForge **leest** die eerst, koppelt ze aan diensten en raakt ze
  pas aan na "Overnemen" per host. Niets wordt gewist dat ClusterForge niet
  zelf maakte, behalve met een overgetikte bevestiging.
- **Eén hoofddomein, aanpasbaar.** Instellingen → Domeinen: `jbogaert.be`.
  Elk beheerd subdomein is `<label>.<hoofddomein>`. Domein veranderen toont
  eerst een lijst van wat er zou wijzigen (zie M6).
- **Veilig wijzigen.** Vóór elke wijziging een NPM-snapshot (bestaat al:
  `npm-ctl.sh snapshot`), na elke wijziging een echte controle (HTTPS-aanvraag
  naar het nieuwe subdomein vanaf een node), bij falen automatisch terug.
- **Kwaliteit boven kwantiteit.** Liever een kleiner aantal mijlpalen die
  echt werken dan alle knoppen van NPM/Authentik nabouwen.

## Wat er al is (onderzoek in de code)

| Nodig | Al aanwezig | Ontbreekt |
|---|---|---|
| Luisterende poorten per node | `Facts.Listeners` (proto, adres, poort, systemd-unit) uit `/proc/net` (`internal/agent/netfacts.go`) | koppeling poort → container, soort (http/https/tcp) |
| Containers en gepubliceerde poorten | fase 21/28: `internal/dockerhost` leest `Ports` (PublicPort, IP) per container | in één "dienstenlijst" naast systemd-diensten |
| VIP en eigenaar | keepalived-facts en clusterstatus (VIP-eigenaar) | welke dienst er achter het VIP zit |
| NPM als cluster | template `nginx-proxy-manager` (fase 11), `npm-ctl.sh` met snapshot/overnemen, SQLite-sync | API-koppeling (lezen/schrijven proxy hosts) |
| Authentik als cluster | template `authentik` (fase 13), Patroni, VIP | API-koppeling (applicaties, providers, outposts) |
| API van een dienst via de agent | patroon van fase 26/32: agent voert API-aanroep lokaal uit, **server** beslist met een allowlist welke paden mogen (`internal/pbs/policy.go`) | NPM- en Authentik-allowlist |
| Geheimen versleuteld bewaren | `internal/secrets` | — |
| Aandacht / SOC / audit / dashboard-API | bestaan | nieuwe bronnen |

## Hoe NPM en Authentik praten (onderzoek)

**NPM** (`jc21/nginx-proxy-manager`, beheer op poort 81):
- `POST /api/tokens` met e-mail + wachtwoord → JWT (verloopt na ~1 dag; vernieuwen
  met `GET /api/tokens`). Geen aparte API-sleutels.
- `GET/POST/PUT/DELETE /api/nginx/proxy-hosts[/id]`: `domain_names`,
  `forward_scheme`, `forward_host`, `forward_port`, `certificate_id`,
  `ssl_forced`, `http2_support`, `hsts_enabled`, `allow_websocket_upgrade`,
  `block_exploits`, `access_list_id`, `advanced_config`, `locations`.
- `GET/POST /api/nginx/certificates` (Let's Encrypt HTTP- of DNS-challenge,
  ook wildcard met DNS-challenge), `GET /api/nginx/access-lists`.
- NPM draait in fase 11 **alleen op de VIP-node**; de SQLite gaat via de
  beheerde sync naar de andere node. Schrijven kan dus alleen op de node met
  het VIP; de volgende sync neemt het mee.

**Authentik** (API v3, poort 9000, token met `Bearer`):
- `/api/v3/core/applications/` (naam, slug, provider, launch-URL, groep, icoon).
- `/api/v3/providers/proxy/` met `mode: forward_single` (één domein) en
  `external_host: https://<sub>.jbogaert.be`, autorisatie- en
  invalidatieflow.
- `/api/v3/outposts/instances/`: de ingebouwde outpost ("authentik Embedded
  Outpost") krijgt de provider in `providers`.
- `/api/v3/flows/instances/` om de standaard autorisatieflow te vinden.
- Forward auth in NPM = `advanced_config` met `auth_request
  /outpost.goauthentik.io/auth/nginx` + `location /outpost.goauthentik.io`
  naar de outpost (Authentik-VIP:9000) — het officiële Authentik-recept.

## Keuze: via de agent of rechtstreeks vanaf de server?

**Via de agent** (zoals PBS/Proxmox):
- De agent op de NPM-node met het VIP praat met `127.0.0.1:81`, de agent op
  een Authentik-node met `127.0.0.1:9000`. Geen nieuwe firewallregels tussen
  server (.113.153) en de VLAN's 102/103; die beheert Jonas zelf en dit
  vermijdt dat het stuk gaat bij een regelwijziging.
- De server bepaalt met een **allowlist** welke methodes, paden en velden mogen;
  de agent controleert alleen de vorm. Wachtwoord/token gaat versleuteld mee
  in de opdracht en staat nooit op de node op schijf.
- Agentprotocol **51** (fase 34 neemt 50): opdrachten `svc.api` (algemene
  lokale HTTP-aanroep naar 127.0.0.1 met poort uit een vaste lijst) en
  `svc.probe` (zie M1). Oude agents: knop grijs met "agent bijwerken".

## NPM: database én config samen (aandachtspunt van Jonas)

Jonas (17/11): bij NPM volstaat het niet om de config-bestanden aan te passen,
de SQLite-database moet mee, en dat is lastig. Daarom:

- **ClusterForge schrijft nooit zelf** in `database.sqlite` of in
  `/data/nginx/proxy_host/*.conf`. Alle wijzigingen gaan via de **NPM-API**: die
  schrijft de rij in de database, genereert de nginx-config, doet `nginx -t`
  en herlaadt. Zo blijven DB en config altijd bij elkaar horen (een losse
  config wordt bij de volgende bewerking in de NPM-GUI of bij herstart toch
  weer overschreven vanuit de DB).
- **Vooraf**: `npm-ctl.sh snapshot` (sqlite3 `.backup` + `integrity_check`
  + `data/` met de configs + `letsencrypt/`, met SHA256SUMS). Dat is ook het
  terugzetpunt.
- **Achteraf, per wijziging** (agent op de VIP-node):
  1. host teruglezen via de API en velden vergelijken met wat gevraagd was,
  2. `data/nginx/proxy_host/<id>.conf` bestaat en bevat de domeinnaam en het
     doel, en er is geen `<id>.conf.err` (zo meldt NPM een mislukte config),
  3. `nginx -t` in de container,
  4. `PRAGMA integrity_check` op een kopie van de DB,
  5. de HTTPS-controle op het subdomein.
  Eén van die stappen faalt → terugzetten (eerst via de API; lukt dat niet,
  dan de snapshot met `npm-ctl.sh` en NPM herstarten), taak rood.
- **Daarna meteen de sync** naar de andere node(s), zodat een failover nooit
  een oude DB met nieuwe configs (of omgekeerd) oplevert; de sync kopieert
  DB, `data/` en `letsencrypt/` als één geheel (fase 11).
- **Testen**: in CI een echte NPM-container (`jc21/nginx-proxy-manager`,
  zelfde versie als bij Jonas) met een kopie-achtige DB van ~100 hosts:
  aanmaken, wijzigen, Authentik-snippet zetten en weghalen, terugdraaien na
  een opzettelijk foute wijziging, en na elke stap de vijf controles. Pas als
  dat groen is, komt M3 bij Jonas.

## Mijlpalen

Stand 17/11: M0-M6 gebouwd. Getest tegen een echte NPM (SQLite) in CI
(job "Echte NPM"): publiceren, een kapotte host terugdraaien via de API,
Authentik aan en uit (met een nagespeelde Authentik), drift herstellen, de
kopie van database en config terugzetten, het hoofddomein omzetten en
verwijderen met bevestiging. sqlite3 zet de agent zelf op de NPM-node als
het ontbreekt.

### M0 – Instellingen → Domeinen en koppelingen
- Nieuwe pagina **Instellingen → Domeinen en publicatie**:
  - Hoofddomein (standaard `jbogaert.be`), validatie (geldige DNS-naam).
  - Standaardwaarden voor nieuwe hosts: SSL afdwingen, HTTP/2, HSTS,
    websockets, "Block common exploits", standaardcertificaat (zie vragen),
    standaard Authentik aan/uit.
  - **NPM-koppeling**: kies het NPM-cluster (uit de clusters met template
    `nginx-proxy-manager`, of een losse node), e-mail + wachtwoord van een
    NPM-beheerder; knop "Testen" (token halen, aantal hosts tonen).
  - **Authentik-koppeling**: kies het Authentik-cluster, API-token (Authentik →
    Directory → Tokens, intent API), knop "Testen" (versie, aantal
    applicaties, ingebouwde outpost gevonden).
- Migratie **00115** (`publish_settings`, `publish_links`). Audit op alles.

### M1 – Dienstenkaart: wat draait waar
- Per node één lijst **Diensten**: systemd-diensten en containers met hun
  poorten, samengevoegd uit `Listeners` + Docker-poorten. Per poort
  doet de agent een **lichte probe** (`svc.probe`: TCP, dan HTTP en HTTPS
  `HEAD /`, met time-out 2 s, alleen op poorten die de node zelf open heeft):
  soort (http, https, tcp), statuscode, `<title>`/`Server`-kop. Zo herkent
  ClusterForge bv. Vaultwarden, Grafana, Home Assistant, Plex.
- Per cluster: welke dienst op **alle** nodes draait en dus achter het **VIP**
  bereikbaar is (VIP:poort), en welke maar op één node.
- Elke dienst krijgt een voorstel-label (`homepage`, `vaultx`, `grafana`…):
  containernaam, unitnaam of clusternaam, ontdaan van `cf-`/`-1`.
- Nieuwe pagina **Publicatie** (menu, onder Netwerk): alle diensten met kolom
  "Gepubliceerd als" (na M2), filter op cluster/VLAN/soort.
- Protocol 51.

### M2 – NPM lezen en koppelen
- ClusterForge leest alle proxy hosts, certificaten en access lists (elke
  5 min, en op "Vernieuwen") en koppelt elke host aan een dienst via
  `forward_host:forward_port` (VIP of node-IP).
- Toestand per host: **ok** (doel bestaat, dienst draait), **doel weg**
  (IP/poort bestaat niet of dienst staat stil), **naar node i.p.v. VIP**
  (host wijst naar één node terwijl de dienst een VIP heeft, zoals nu nog
  `vaultx.jbogaert.be` naar het oude IP), **certificaat verloopt** (< 21 d),
  **buiten hoofddomein**.
- Omgekeerd: diensten met een webpoort zonder proxy host ("niet
  gepubliceerd").
- Nog **niets schrijven**. Alleen lezen, zodat Jonas het overzicht kan
  controleren tegen zijn 105 hosts.

### M3 – Publiceren en bijwerken in NPM
- Knop **Publiceren** bij een dienst: label (voorstel), doel (VIP:poort,
  voorgekozen), schema http/https, websockets, certificaat, Authentik ja/nee
  (vanaf M4). Voorbeeldscherm met precies wat NPM krijgt.
- Uitvoeren als taak:
  1. `npm-ctl.sh snapshot` op de VIP-node (bestaat al),
  2. proxy host aanmaken of bijwerken via de API,
  3. certificaat: wildcard hergebruiken of Let's Encrypt aanvragen,
  4. **controle**: vanaf een andere node `https://<label>.<domein>` met
     `--resolve` naar het NPM-VIP; statuscode en certificaat moeten kloppen,
  5. niet goed → host terugzetten zoals hij was (of verwijderen als hij nieuw
     was), taak rood met de reden.
- **Overnemen** van een bestaande host: ClusterForge onthoudt hem als beheerd
  (desired state = huidige waarden), zonder iets te wijzigen. In bulk
  mogelijk ("alle hosts onder jbogaert.be met een gekend doel").
- **Doel naar VIP zetten**: voor hosts die naar één node wijzen, één klik.
- Verwijderen alleen voor beheerde hosts, met overgetikte naam.
- Na de wijziging start ClusterForge de beheerde NPM-sync meteen ("Nu
  draaien"), zodat de tweede node niet tot de volgende ronde achterloopt.

### M4 – Authentik
- Lezen: applicaties, proxy providers, outposts, flows. Koppeling provider ↔
  NPM-host via `external_host`. Toestand per host: **beschermd** (provider +
  in outpost + forward-auth-snippet in NPM), **half** (bv. provider bestaat
  maar niet in de outpost, of NPM mist het snippet), **open**.
- **Beveiligen met Authentik** (bij een host of bij Publiceren):
  1. proxy provider `forward_single` met `external_host`, standaard
     autorisatieflow,
  2. applicatie (slug = label, launch-URL, optioneel groep/policy "alleen
     groep X"),
  3. provider toevoegen aan de ingebouwde outpost,
  4. in NPM het forward-auth-snippet in `advanced_config` zetten (tussen
     markeringen `# clusterforge-authentik begin/eind`, zodat eigen regels
     van Jonas blijven staan),
  5. controle: zonder sessie moet het subdomein naar Authentik doorsturen
     (302 naar `/application/o/authorize` of de outpost),
  6. bij falen alles terug in omgekeerde volgorde.
- **Bescherming weghalen**: omgekeerd, met bevestiging.
- Het Authentik-outpost-doel in NPM is het **Authentik-VIP** (192.168.103.244:9000),
  niet één node.

### M5 – Bewaking, drift en clustertemplates
- Achtergrondronde vergelijkt beheerde hosts/providers met wat er echt in
  NPM/Authentik staat (drift: iemand wijzigde in de NPM-GUI) → **Aandacht
  nodig** met "Herstellen" (zet terug) of "Aanvaarden" (nieuwe waarden
  worden de gewenste).
- Meldingen: certificaat verloopt, doel weg, host open zonder Authentik
  terwijl de standaard "aan" is, dienst zonder publicatie na uitrol.
- **Clustertemplates** krijgen een optioneel blok `publish:` (label, poort,
  Authentik ja/nee). Na een uitrol of failover stelt ClusterForge de
  publicatie voor (niet automatisch zonder klik, tenzij Jonas dat in
  Instellingen aanzet). Bv. homepage → `homepage.jbogaert.be` → VIP .113.159.
- SOC/tijdlijn: elke publicatie/wijziging als gebeurtenis. Dashboard-API:
  aantallen (gepubliceerd, beschermd, problemen) voor de homepage.

### M6 – Domein wijzigen en afronden
- Hoofddomein veranderen in Instellingen toont eerst **voorbeeld**: welke
  beheerde hosts een nieuwe naam krijgen, welke certificaten nodig zijn, welke
  Authentik-providers een nieuwe `external_host` krijgen. Twee keuzes:
  "alleen nieuwe hosts" of "alles omzetten" (met de oude naam nog een tijd als
  extra naam op de host, zodat bladwijzers blijven werken).
- Documentatie (Auto-documentatie) krijgt een tabel "Publicatie".
- Schermafdrukken desktop/gsm, licht/donker.

## Migraties en protocol

- Migraties **00115-00124** gereserveerd (fase 34 heeft 00105+). Ik stem dit
  af met de threads van fase 33/34 voordat ik begin.
- Agentprotocol **51** (fase 34 = 50). Alleen M1 en M3/M4 (via `svc.api`)
  hebben het nodig; zonder bijgewerkte agent blijft lezen in de GUI grijs.

## Wat de server mag (allowlist)

- NPM: `GET` op proxy-hosts, certificates, access-lists, settings;
  `POST/PUT` proxy-hosts (velden hierboven), `POST` certificates (Let's
  Encrypt), `DELETE` proxy-hosts alleen voor beheerde id's.
- Authentik: `GET` op applications, providers/proxy, outposts/instances,
  flows/instances, core/groups; `POST/PATCH` op applications,
  providers/proxy, outposts/instances (alleen veld `providers`);
  `DELETE` alleen voor objecten die ClusterForge maakte (id in
  `publish_links`).
- Alles wat schrijft komt in het auditlog met voor- en na-waarde (geheimen
  gemaskeerd).

## Vragen aan Jonas (beantwoord 17/11: alle drie volgens voorstel)

1. **"Zoals bij dashboard"**: bedoel je dat subdomeinen gaan als
   `<naam>.jbogaert.be`, met de naam van de dienst als voorstel en
   aanpasbaar, zoals in je homepage-dashboard? *Voorstel: ja.*
2. **Certificaten**: één wildcard `*.jbogaert.be` (DNS-challenge, dan: welke
   DNS-provider, bv. Cloudflare?) of per host Let's Encrypt via HTTP? *Voorstel:
   bestaande wildcard hergebruiken als die er is, anders per host.*
3. **Authentik standaard aan** voor elke nieuwe host, of per host kiezen?
   *Voorstel: per host kiezen, standaard aan; dashboards met eigen login
   (Vaultwarden, Home Assistant, Plex) standaard uit.*
