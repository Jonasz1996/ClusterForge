# Fase 16: firewall per node via de agents

Gevraagd door Jonas op 2026-11-15: "volgende fase is een lokale ufw gemanaged
door de agents die kijken welke software / containers er op draaien welke
poorten gebruikt zijn en sluiten de rest." Aanvulling: "Zet ook monitoring op
de gesloten poorten en maak een duidelijk dashboard wat er open / gesloten
staat per cluster en welk verkeer naar gesloten poorten."

Aanleiding: dezelfde dag brak de uitrol van Authentik omdat ufw (default deny)
op de oude nodes etcd (2379/2380), Patroni (8008), PostgreSQL (5432) en VRRP
tegenhield, en de VRRP-regel alleen multicast van 192.168.0.0/24 toeliet.
Handgeschreven regels per node lopen achter op de software; ClusterForge weet
welke software er draait en welke nodes bij elkaar horen.

## Doel

1. Op elke node die Jonas aanzet, beheert ClusterForge ufw: open wat de
   software op de node gebruikt, voor wie het nodig is, en de rest dicht
   (default deny incoming).
2. Poorten die alleen tussen clusterleden nodig zijn (etcd, Patroni, Galera,
   Redis, VRRP), gaan alleen open voor de andere nodes van het cluster.
3. Nooit buitensluiten: SSH blijft altijd open, uitgaand verkeer (de agent
   naar de server, 4222/8080) wordt nooit beperkt, en een toepassing die de
   node onbereikbaar maakt, draait zichzelf terug.
4. Eerst een voorbeeld met het verschil, dan pas toepassen.
5. Monitoring van het verkeer dat op een dichte poort botst, en een dashboard
   per cluster: wat staat open, voor wie, wat is dicht, wie klopt er aan.

Niet in deze fase: Docker-poorten filteren (`DOCKER-USER`), uitgaand verkeer
beperken, de firewall van OPNsense of Proxmox.

## Opbouw

| Waar | Wat |
| --- | --- |
| Node | ufw (Debian-pakket; de agent installeert het als het ontbreekt). Alle regels van ClusterForge hebben een commentaar dat met `cf:` begint |
| Agent (protocol 30) | nieuw onderwerp `cf.node.<id>.firewall`: `status`, `apply`, `confirm`, `rollback`, `disable`, `probe`, `blocked` |
| Server | `internal/firewall`: catalogus (software → poorten → voor wie), plan per node, verschil, taak `firewall.apply`, poller, monitoring |
| Database | migratie 00067: instellingen, stand per node, eigen regels, geziene poorten, geblokkeerd verkeer per uur |
| GUI | pagina **Firewall** (menu Beveiliging): Overzicht per cluster, Verkeer, Nodes, Regels, Instellingen; kaart Firewall op de node |

## Wat de agent ziet (`status`)

- ufw: geïnstalleerd, versie, actief, standaardbeleid, logging.
- De regels van ufw, gelezen uit `/etc/ufw/user.rules` en `user6.rules` (de
  `### tuple ###`-regels: actie, protocol, poort, bron, doel, richting,
  commentaar). Een regel met commentaar `cf:…` is van ClusterForge.
- Luisterende poorten (TCP LISTEN en gebonden UDP) met adres, proces, de
  systemd-unit en, voor een proces in een container, de container. Zo ziet
  ClusterForge ook Authentik met `network_mode: host`.
- Docker-containers met hun gepubliceerde poorten en netwerkmodus.
- Of het tellen van geblokkeerd verkeer werkt (zie Monitoring).
- Een openstaande toepassing die nog bevestigd moet worden.

## Het plan

De server rekent per node een plan uit (`internal/firewall/plan.go`):

**Bronnen** (voor wie een poort open gaat):

| Soort | Betekenis |
| --- | --- |
| `any` | iedereen |
| `lan` | de eigen netwerken (instelling; standaard 10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16, fc00::/7) |
| `cluster` | de andere nodes van hetzelfde cluster (hun adressen uit inventaris en facts) en de VIP's |
| `admin` | de beheernetwerken (instelling, bv. 192.168.90.0/24 voor Jonas' pc); leeg = `lan` |

**Wat open gaat**, in deze volgorde:

1. **SSH** van de bron bij Instellingen (standaard `lan`), op de poort waar
   sshd luistert (22 als dat niet te zien is). Kan niet weg.
2. **Clustertemplates**: wat het template van het cluster nodig heeft, ook als
   de dienst (nog) niet draait. Precies wat vandaag misging: etcd moet open
   staan vóór etcd een cluster kan vormen. Bv. `authentik`: 2379-2380, 8008
   en 6379 voor `cluster`, 5432 en 9000/9443 voor `lan`, 5432 vanaf de
   Docker-netwerken.
3. **systemd-units** die enabled of actief zijn met vaste poorten uit de
   catalogus (`etcd`, `patroni`, `mariadb`, `keepalived`, `haproxy`, …), ook
   als ze op dit moment niet luisteren.
4. **Luisterende poorten**: elk proces dat op een niet-lokaal adres
   luistert, krijgt de bron uit de catalogus (bv. nginx 80/443 → `any`, NPM 81
   → `admin`, PostgreSQL 5432 → `lan`, Patroni 8008 → `cluster`). Onbekende
   software: instelling "Onbekende poorten" (standaard `lan`, of dicht); de
   pagina zet ze apart zodat Jonas ze kan indelen.
5. **VRRP** (keepalived): `proto vrrp` van elk ander clusterlid en van de
   `unicast_peer`-adressen. Geen multicastnet meer raden.
6. **Eigen regels** (pagina Regels): open of dicht per node, per cluster of
   overal, voor een bron naar keuze. "Dicht" wint van de detectie.

Wat alleen op 127.0.0.1/::1 luistert, blijft buiten beschouwing. Poorten die
Docker publiceert, gaan buiten ufw om (Docker zet ze zelf open in de
FORWARD-keten); het plan toont ze als "open via Docker".

Een geziene poort blijft 24 uur in het plan nadat hij verdween (instelling),
zodat een dienst die even stilstaat zijn poort niet verliest en kan
herstarten.

**Vreemde regels**: regels zonder `cf:` die het plan ook wil, neemt
ClusterForge over (krijgen het commentaar). Andere vreemde regels blijven
staan, tenzij "Strikt" aan staat (standaard aan): dan gaan ze weg. Het
voorbeeld toont ze altijd apart.

## Toepassen en terugdraaien

Taak `firewall.apply`:

1. **Voorbeeld**: verse `status`, plan, verschil (erbij, weg, overgenomen,
   blijft). Dit is ook wat de knop Voorbeeld toont zonder iets te doen.
2. **Toepassen** (`apply`): de agent
   - kopieert `/etc/ufw/user*.rules`, `after*.rules` en `ufw.conf` naar
     `/var/lib/clusterforge/firewall/backup/` en schrijft een
     terugdraaiscript;
   - zet een **terugdraaitimer** met `systemd-run --on-active=120s` (instelling)
     die buiten de agent om draait: als niemand bevestigt, zet systemd de
     vorige regels terug, ook als de agent vastloopt;
   - zet eerst alle nieuwe allow-regels, dan het standaardbeleid (deny in,
     allow uit), schakelt ufw in, haalt daarna de oude regels weg;
   - test een nieuwe TCP-verbinding naar de server (4222) en ziet of sshd nog
     luistert; mislukt iets, dan draait hij meteen terug.
3. **Controle**: de server vraagt een verse `status` en laat de andere
   clusterleden de clusterpoorten van deze node proberen (`probe`, alleen
   poorten die luisteren). Weigert een poort die open moet zijn, dan
   `rollback`.
4. **Bevestigen** (`confirm`): de timer stopt, de stand wordt
   `/var/lib/clusterforge/firewall.json`. Haalt de server de agent niet meer,
   dan zet de timer alles vanzelf terug en meldt de taak dat.

Na een herstart van de node tijdens de wachttijd (de timer is dan weg) ziet de
agent bij het opstarten de onbevestigde toepassing en draait die terug.

**Standen per node**: uit (niet beheerd), **voorbeeld** (plan en verschil
zien, niets toepassen; standaard bij aanzetten), **beheerd** (het plan wordt
toegepast, ook vanzelf als er software bijkomt of verdwijnt). "Uitzetten" stopt
het beheer; "ufw uitschakelen" zet `ufw disable`.

Een node met een mislukte toepassing probeert het pas na 1 uur opnieuw.

## Monitoring: verkeer naar dichte poorten

ufw logt met de LOG-target naar de kernel-log; in een LXC-container komt die
niet in de container (en `nf_log_all_netns` staat standaard uit). Daarom:

- In `/etc/ufw/after.rules` en `after6.rules` zet ClusterForge, tussen
  markeringen, vóór het standaardbeleid een regel
  `-A ufw-after-input -m conntrack --ctstate NEW -j NFLOG --nflog-group 4711`.
  Alleen nieuwe verbindingen die geen enkele regel toeliet, dus wat ufw zo
  meteen weggooit (ufw's eigen ruis zoals NetBIOS en broadcast gaat eerder
  weg).
- De agent luistert met netlink op die NFLOG-groep (werkt per
  netwerknamespace, ook in een LXC) en telt per bron, protocol en poort.
  Kan dat niet (module `nfnetlink_log` niet geladen op de host), dan valt hij
  terug op de kernel-log (`journalctl -k`, `[UFW BLOCK]`) en zegt de pagina
  dat.
- De server haalt elke minuut `blocked` op (de tellers sinds de vorige keer)
  en bewaart ze per uur (30 dagen).

Dashboard:

- **Overzicht**: per cluster een tabel poort × node: open (voor wie), dicht,
  via Docker, onbekend; tegels (beheerde nodes, open poorten, pogingen 24 u,
  unieke bronnen, waarschuwingen).
- **Verkeer**: pogingen naar dichte poorten per uur (24 u / 7 d), top-poorten,
  top-bronnen (met "is dit een clusterlid?": een clusterlid dat op een dichte
  poort klopt, is bijna altijd een vergeten regel en krijgt een knop
  "Openzetten voor het cluster").
- **Nodes**: stand, plan, verschil, Voorbeeld / Toepassen / Terugdraaien.
- **Regels**: eigen regels.
- **Instellingen**: netwerken, SSH, onbekende poorten, strikt, wachttijd
  bevestigen, geheugen van poorten, automatisch toepassen, verkeer tellen.

## Samen met CrowdSec

De nftables-bouncer van CrowdSec heeft een eigen tabel (`crowdsec`,
`crowdsec6`) met een hook op prioriteit -10; ufw (iptables-nft) zit in de
tabel `filter`. Een pakket moet door beide; ClusterForge raakt de tabellen van
CrowdSec nooit aan en doet nooit `nft flush ruleset` of `ufw reset`. De
lokale API van CrowdSec luistert op 127.0.0.1 en krijgt dus geen regel.

## Protocol 30

`cf.node.<id>.firewall`, verzoek/antwoord (`firewall_request`/
`firewall_result`): `status`, `apply {rules, strict, confirm_seconds, …}`,
`confirm {apply_id}`, `rollback {apply_id}`, `disable`, `probe {targets}`,
`blocked`. Een agent met protocol < 30 krijgt "agent te oud".

## Mijlpalen

- M1 protocol 30 + agent: status, apply met terugdraaitimer, confirm,
  rollback, probe, NFLOG-tellers; tests.
- M2 `internal/firewall`: catalogus, plan, verschil; tests op Jonas' stack
  (Authentik, Galera/Patroni, NPM, Zabbix, keepalived).
- M3 migratie 00067, taak `firewall.apply`, poller, monitoring, API.
- M4 GUI: pagina Firewall, kaart op de node, README.
