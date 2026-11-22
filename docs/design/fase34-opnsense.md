# Fase 34: OPNsense-firewallcluster volledig beheren

Gevraagd door Jonas op 2026-11-17: "hierna mag je agents bouwen voor
OPNsense-firewall en volledig beheer daarvan; OPNsense draait ook in cluster
via pfsync en CARP."

Dit document is het onderzoek en het stappenplan. Bouwen begint pas als Jonas
het stappenplan goedkeurt én fase 33 (Zabbix-dashboards, PR #139, branch
`claude/project-thread-6m7z4z`) zijn gecombineerde image heeft gemeld. We
bouwen op die stam; daarna mijlpaal per mijlpaal, elk apart gemeld met
image-tag en updatecommando.

Nummers: agentprotocol **50** (en hoger als een latere mijlpaal de agent
opnieuw raakt), migraties vanaf **00105**.

## Uitgangspunten

- **Een firewall is kritiek.** Een fout sluit Jonas buiten zijn eigen netwerk.
  Daarom: geen wijziging zonder voorbeeld (wat verandert er, als diff), elke
  wijziging met een herstelpunt, en een **terugdraaitimer die op de firewall
  zelf loopt**. Als de bevestiging niet binnen de tijd komt, zet de agent de
  oude toestand terug, ook als het netwerk naar de server net stuk is.
- **Alleen op de master wijzigen.** De server wijzigt alleen op de node die
  voor *alle* CARP-VIP's MASTER is. Daarna synchroniseert OPNsense zelf naar
  de backup (XMLRPC) en controleert ClusterForge dat de backup dezelfde
  configuratie heeft. Split-brain (twee masters), een node in INIT of een
  verhoogde demotion blokkeert elke wijziging.
- **Alles via de GUI.** Op de firewalls alleen één keer het
  installatiecommando van de agent (Nodes → Agent installeren). Geen
  handmatige API-sleutel, geen shell-werk daarna.
- **Hetzelfde patroon als Proxmox (fase 26) en PBS (fase 32).** De agent geeft
  de lokale OPNsense-API door; de server bepaalt met een allowlist wat de GUI
  mag (methode, pad, velden, wat geheim is, wat een overgetikte bevestiging
  vraagt). Nieuwe schermen vragen dan geen agentupdate.
- **Lezen eerst.** M0 en M1 lezen alleen en bouwen het veilige
  wijzigingsmechanisme; schrijven in de echte schermen begint in M2.

## Hoe OPNsense in elkaar zit (onderzoek)

| Onderdeel | Wat het is | Wat wij ermee doen |
|---|---|---|
| Besturingssysteem | FreeBSD 14 (OPNsense 25.x/26.x), `pkg`, geen systemd | Aparte FreeBSD-build van cf-agent; service via rc.d. |
| Configuratie | Eén bestand `/conf/config.xml`; elke opslag maakt een revisie in `/conf/backup/config-<tijd>.xml` | Herstelpunt en diff voor/na elke wijziging; revisies tonen en terugzetten. |
| REST-API | `https://<fw>/api/<module>/<controller>/<command>`, Basic-auth met key/secret van een gebruiker; MVC-modellen valideren en `reconfigure` past toe | Hoofdweg voor lezen én schrijven: de API valideert, past correct toe en schrijft revisies. |
| `configctl` (configd) | Lokale acties: services herstarten, filter herladen, templates genereren, firmware | Alleen als de API iets niet biedt (bv. onderhoudsmodus, status zonder API). |
| Legacy-pagina's | Delen die nog geen MVC/API hebben (afhankelijk van versie: oudere firewallregels, interfacetoewijzing, sommige HA-velden) | Lezen uit `config.xml`; schrijven alleen als er een API is. Geen rechtstreekse XML-bewerkingen in M2-M5. |
| CARP | VIP's per interface, `advskew`/`advbase`, sysctl `net.inet.carp.demotion`, onderhoudsmodus ("Persistent CARP Maintenance Mode") | Status per VIP per node, onderhoudsmodus als knop, failovertest. |
| pfsync | Synchroniseert de staten (verbindingen) naar de backup over een eigen interface | Status en teller tonen; waarschuwen als pfsync stil valt. |
| HA-sync (XMLRPC) | Master duwt gekozen onderdelen van de config naar de backup ("Synchronize config to IP", per onderdeel aan/uit) | Na elke wijziging sync starten en controleren; drift master↔backup tonen. Let op: niet alles wordt gesynchroniseerd (interfaces, HA-instellingen zelf, sommige plugins). |
| Firmware | `opnsense-update` / API `core/firmware/*` (status, update, upgrade, changelog) | Clusterbewust bijwerken (backup eerst). |
| Logs | `/var/log/filter/latest.log` (pf), systeem, diensten | Naar de Logs-pagina (fase 23) en SOC (fase 25). |

Elk API-pad uit dit document wordt in M0 tegen Jonas' versie getest
(`core/firmware/info` geeft de versie); de lijst met paden is versie-afhankelijk
en de allowlist houdt per pad bij vanaf welke OPNsense-versie het bestaat.

## Keuze: API-sleutel versus configctl/config.xml

| Optie | Voor | Tegen |
|---|---|---|
| A. Jonas maakt een API-sleutel en plakt die in ClusterForge; server praat rechtstreeks met de firewall | Geen agent nodig | Handwerk per firewall (tegen "alles via GUI"); sleutel met volle rechten op de server; server moet de firewall-GUI kunnen bereiken; geen lokale terugdraaitimer mogelijk. |
| B. Agent bewerkt `config.xml` en roept `configctl` aan | Alles kan | Geen validatie; makkelijk een kapotte config; reconfigure-volgorde zelf nabootsen. Te riskant voor een firewall. |
| **C. Agent op de firewall maakt zelf een lokale API-gebruiker en sleutel aan en praat met `https://127.0.0.1/api`** | Geen handwerk; sleutel verlaat de firewall nooit (bestand alleen voor root); API valideert; agent kan lokaal terugdraaien; werkt ook als de GUI niet vanaf het LAN bereikbaar is | Aanmaken van de gebruiker gebeurt via een klein PHP-script met OPNsense' eigen modelklassen (de manier waarop OPNsense het zelf doet); moet per versie getest worden. |

**Keuze: C**, met A als terugval: lukt het automatisch aanmaken niet op een
versie, dan toont het scherm een formulier om een sleutel te plakken (die gaat
dan versleuteld naar de agent, niet in de database van de server).

Details van C:

- Gebruiker `clusterforge` (geen wachtwoordlogin, alleen API), lid van een
  eigen groep. Rechten: in M0 lezen; vanaf M2 de pagina's die we beheren. De
  sleutel staat in `/usr/local/etc/cf-agent/opnsense.key` (0600, root).
- De gebruiker en sleutel worden opnieuw gemaakt als ze verdwijnen (bv. door
  een terugzetting van een oude config), en de oude sleutel wordt ingetrokken
  bij het verwijderen van de agent.
- De agent luistert nergens: hij verbindt naar buiten naar de server
  (NATS 4222, download 8080), zoals op Linux. Verkeer van de firewall zelf
  naar buiten mag standaard; er hoeft geen regel bij.

## Agent op FreeBSD/OPNsense

- `GOOS=freebsd GOARCH=amd64` bouwt vandaag al zonder fouten (getest op deze
  stam); arm64 bouwen we mee. De serverimage levert `cf-agent-freebsd-amd64`
  naast de Linux-builds.
- Het installatiecommando herkent FreeBSD/OPNsense (`uname`,
  `/usr/local/opnsense/version/core`), zet de binary in
  `/usr/local/sbin/cf-agent`, een rc.d-script `cf_agent` en het
  inschakelen via `/etc/rc.conf.d/cf_agent`. OPNsense start bij het
  opstarten (syshook `start/20-freebsd` → `rc.freebsd`) elk rc.d-script dat
  in `/etc/rc.conf.d` aan staat; een eigen syshook is niet nodig (nagekeken
  in de broncode van OPNsense). Dat de agent een firmware-upgrade overleeft,
  testen we bij de eerste upgrade.
- Linux-onderdelen (apt, systemd, ufw, docker, `/proc`) melden op FreeBSD
  "niet beschikbaar" in plaats van fouten. Metrics (cpu, geheugen, schijf,
  netwerk, load, temperatuur) komen op FreeBSD uit `sysctl`/`getifaddrs`, zodat
  de firewalls in Monitoring en de gezondheidsscore meetellen.
- Zelf-update werkt zoals op Linux (download, sha256, vervangen,
  `service cf_agent restart`), met de bestaande terugval.
- Facts `opnsense`: versie, CARP-status per VIP, demotion, pfsync-interface en
  -peer, HA-sync-instellingen en laatste sync, interfaces met adressen,
  gateways met status, firmwarestatus, of de lokale API-sleutel werkt.

## Veilig wijzigen (het hart van deze fase)

Elke wijziging, uit welk scherm ook, gaat door dezelfde stappen:

1. **Voorcontrole (server).** Clusterstatus: precies één master voor alle
   VIP's, backup bereikbaar en BACKUP, pfsync ok, geen sync-fouten, geen
   andere lopende wijziging op dit cluster (clusterslot), agent op protocol
   ≥ 50. Bij prod: TOTP. Faalt iets, dan staat de knop uit met de reden.
2. **Voorbeeld/diff.** De server toont wat er verandert: velden oud → nieuw,
   en welke onderdelen opnieuw geladen worden. Wijzigingen die de toegang
   kunnen raken (regels op de interface waarlangs de server of Jonas' pc
   binnenkomt, NAT, interfaces, gateways, CARP, HA) krijgen een rode
   waarschuwing en vragen de clusternaam overtikken.
3. **Herstelpunt (agent).** Kopie van `config.xml`. (OPNsense' eigen
   *savepoint* van de firewallregels bestaat niet meer in de huidige
   broncode van OPNsense; we vertrouwen er dus niet op. Terugzetten gebeurt
   met `Config::restoreBackup`, dezelfde functie als *System →
   Configuration → History*.)
4. **Toepassen (agent).** Via de API op de master; de agent start meteen een
   **lokale terugdraaitimer** (standaard 120 s, instelbaar 60-600 s).
5. **Bevestigingsproeven.** Automatisch, alle moeten slagen:
   - de agent bereikt de server nog (NATS);
   - de server bereikt de firewall-GUI en de VIP's van buitenaf (probe vanaf
     de ClusterForge-LXC en vanaf een gekozen agent in Jonas' pc-VLAN of
     beheer-VLAN);
   - CARP-status onveranderd (geen onbedoelde failover);
   - gateways nog online;
   - eigen proeven per wijziging (bv. de nieuwe poortdoorsturing antwoordt).
   Slagen ze, dan bevestigt de server en annuleert de agent de timer. Slagen ze
   niet, of komt er niets, dan zet de agent zelf het herstelpunt terug en
   herlaadt hij de betrokken onderdelen. De taak toont het verloop.
6. **HA-sync.** Na bevestiging start ClusterForge "Synchronize config" op de
   master en controleert op de backup dat de gesynchroniseerde onderdelen
   gelijk zijn (hash per onderdeel). Is de backup na de sync anders, dan
   meldt de taak dat als drift met de diff.
7. **Logboek.** Wie, wat, diff, uitslag van de proeven, revisie voor/na.

Daarnaast:

- **Revisies.** Lijst van config-revisies per node, diff tussen twee
  revisies, en "terugzetten naar deze revisie" (zelfde stappen 1-7).
- **Back-ups.** Bij elke wijziging en dagelijks een versleutelde kopie van
  `config.xml` in ClusterForge (geheimen versleuteld zoals bij fase 17/18).
  Zo kan een firewall die volledig stuk is opnieuw opgebouwd worden.
- **Noodknop.** "Terugdraaien naar laatste goede" werkt ook als de server de
  firewall niet meer via de GUI bereikt, zolang de agent nog verbonden is.

## Wat je in de GUI kunt (doel van "volledig beheer")

Compacte vensters zoals bij Proxmox en PBS: een pagina **Firewalls** met per
cluster een kop (master/backup, VIP's, sync, versie) en tabbladen.

| Tabblad | Lezen | Beheren |
|---|---|---|
| Overzicht | CARP per VIP en node, pfsync, laatste sync, gateways, cpu/geheugen/staten, versie en updates | Sync nu, onderhoudsmodus |
| Regels | Regels per interface en zwevende regels, tellers, aliassen, schema's | Regel toevoegen/wijzigen/aan-uit/volgorde, aliassen (ook lijsten en GeoIP), schema's |
| NAT | Port forwards, outbound, 1:1, NPT | Toevoegen/wijzigen met gekoppelde regel |
| Interfaces en VIP's | Interfaces, VLAN's, adressen, VIP's (CARP/IP alias) | VIP's en CARP-instellingen; interfaces/VLAN's alleen met de strengste bevestiging |
| Routering | Gateways, gatewaygroepen, statische routes, status | Wijzigen |
| DHCP en DNS | Kea/Dnsmasq/ISC (wat Jonas gebruikt), leases, Unbound-overrides | Reservaties, overrides, leases wissen |
| VPN | WireGuard, OpenVPN, IPsec: status en peers | Peers en tunnels beheren, herstarten |
| Diensten en plugins | Services en status, geïnstalleerde plugins | Start/stop/herstart, plugins (de)installeren |
| Diagnose | Staten (pf), live firewalllog, ping/traceroute/poorttest vanaf de firewall, pakketopname kort | Staat wissen |
| Revisies en back-ups | Revisies, diff, back-ups | Terugzetten |
| Firmware | Updates, changelog | Clusterbewust bijwerken |
| HA | HA-instellingen per node, wat wordt gesynchroniseerd, drift | Sync-onderdelen kiezen, failovertest |

Koppelingen met wat er al is:

- **Logs (fase 23)**: firewalllog en systeemlog per firewall.
- **SOC (fase 25) en CrowdSec (fase 14)**: geblokkeerd verkeer, top-bronnen;
  CrowdSec-plugin op OPNsense als die er is.
- **Failover-simulator (fase 2)**: test "master in onderhoudsmodus → backup
  neemt alle VIP's binnen N s over → terug".
- **Aandacht nodig**: split-brain, backup niet bereikbaar, sync mislukt, drift,
  gateway down, pfsync stil, firmware-update, verlopen certificaat.
- **Zabbix-dashboards (fase 33)**: als de firewalls in Zabbix zitten,
  koppeling node ↔ Zabbix-host zoals voor de andere nodes.

## Mijlpalen

| | Wat | Agent |
|---|---|---|
| **M0** | Agent op OPNsense: FreeBSD-build, installer met rc.d + syshook, metrics uit sysctl, facts `opnsense`, lokale API-gebruiker en sleutel. Firewall-node herkend in Nodes. | protocol 50 |
| **M1** | Pagina Firewalls (alleen lezen): clusterkop met CARP/pfsync/sync, overzicht, regels, NAT, interfaces, gateways, diensten, revisies met diff. Veilig-wijzigen-mechanisme volledig gebouwd en getest met één onschuldige wijziging (omschrijving van een alias). | – |
| **M2** | Regels, aliassen, NAT, schema's beheren. Herstelpunt + lokale timer + proeven + HA-sync. | – |
| **M3** | Interfaces/VIP's, gateways, routes, DHCP, Unbound. | – |
| **M4** | VPN, diensten, plugins, diagnose (staten, live log, ping/traceroute), logs naar fase 23/25. | – |
| **M5** | HA-beheer: onderhoudsmodus, failovertest, drift master↔backup, clusterbewuste firmware-updates (backup eerst → overschakelen → oude master → terug), back-ups in ClusterForge. | – |
| **M6** | Aandacht nodig, gezondheidsscore, dashboard-API, Zabbix-koppeling, README. | – |

Elke mijlpaal: tests (allowlist, voorcontroles, terugdraaien bij falende
proef, split-brain blokkeert), schermafdrukken desktop en gsm, image-tag en
volledig updatecommando.

Testen zonder Jonas' firewalls: een nep-OPNsense-API in de tests (zoals
`ghfake-dev` voor GitHub) en de FreeBSD-build in CI. Het echte gedrag (sleutel
aanmaken, savepoint, CARP-status, sync) verifieert de eerste keer op Jonas'
cluster in M0/M1, alleen lezend.

## Wat de server mag (allowlist)

`internal/opnsense/policy.go`, zoals `internal/pbs/policy.go`: per regel
methode, pad (regex), toegelaten velden, geheime velden (nooit in taak/log,
zoals `Quiet` bij PVE-tokens), titel voor het logboek, en welk
bevestigingsniveau. Paden buiten de lijst weigert de server; de agent
controleert alleen de vorm (zoals `ValidatePBS`). Zo kan de agent niets dat de
GUI niet kan aanbieden.

## Vragen aan Jonas (bijsturen mag, anders ga ik uit van het voorstel)

1. OPNsense-versie en of het amd64 is (de VM 1000 op ultra7plus is amd64).
2. Gebruik je de nieuwe firewallregels (MVC/"automation") of de klassieke
   regels? Voorstel: beide lezen, schrijven via de API die je versie biedt.
3. Welke plugins/diensten draaien er (WireGuard, OpenVPN, IPsec, Kea of
   Dnsmasq, Unbound, ACME, CrowdSec, HAProxy)? Die komen in M3/M4 eerst.
4. Vanaf welk VLAN beheer je de firewalls (pc 192.168.90.x?) zodat de
   bevestigingsproef precies dat pad test.

## Stand van de bouw

- **M0 (agent)**: FreeBSD-build en installer (rc.d onder daemon(8)), metrics
  uit sysctl/netstat, facts `opnsense` (CARP per VIP, demotion,
  onderhoudsmodus, pfsync, HA-instellingen, interfaces, laatste revisie),
  lokale API-gebruiker `clusterforge` via `OPNsense\Auth\User` (zoals
  `OPNsense\Auth\API::createKey`), leesverzoeken (`opn_get`, `opn_search`,
  `opn_configctl` met vaste lijst, revisies zonder geheimen) en de
  run-acties `opn_api` / `opn_confirm` / `opn_rollback` / `opn_hasync` /
  `opn_key`. Protocol 50. De terugdraaitimer is een los proces
  (`daemon -f … cf-agent opn-rollback`), dus hij werkt ook als de agent
  zelf stopt; na een herstart zet de agent hem terug met de resterende tijd.
- **M1 (server en GUI)**: `internal/opnsense` groepeert de firewalls per
  ClusterForge-cluster of, zonder cluster, als paar via pfsync-peer of
  syncadres, en beoordeelt CARP (split-brain, gemengde rol, demotion,
  onderhoud, pfsync, API-sleutel, configsync, versies). Wijzigen gaat als
  taak `opnsense.change`: voorcontrole (precies één master, backup in
  BACKUP, sleutels ok, clusterslot), basislijn (TCP naar de GUI-poort van
  elke node en elke CARP-VIP, CARP-rol per node, gateways die al down
  zijn), toepassen op de master met timer, proeven tot de helft van de
  terugdraaitijd, bevestigen of meteen terugdraaien, daarna sync naar de
  backup en controle dat die een nieuwere revisie heeft. Op prod
  tweestapsverificatie. Extra leesverzoek `opn_facts` (facts van nu) voor
  de proeven. Allowlist in `policy.go`: aliassen (M1). GUI onder
  OPNsense: Overzicht (CARP-tabel, nodes, gateways, diensten, interfaces,
  Sync nu, sleutel plakken), Regels en aliassen (omschrijving van een alias
  veilig wijzigen), NAT, Revisies (twee revisies of master tegenover
  backup vergelijken).
- **M2 (regels en NAT)**: allowlist uitgebreid met firewallregels
  (`firewall/filter`, ook verplaatsen), port forwards (`d_nat`), uitgaande
  NAT, 1:1 en NPT (toevoegen, wijzigen, aan/uit, verwijderen, daarna
  `apply`), en aliassen volledig. Alles in filter en NAT is niveau
  "toegang": clusternaam overtikken. Het formulier stuurt alleen de velden
  die veranderden en toont oud → nieuw. Schema's hebben in OPNsense nog
  geen MVC-API; die volgen als ze er zijn.
- **M3 (netwerk, DHCP en DNS)**: VIP's, VLAN's, gateways en statische
  routes (niveau "toegang"), Unbound host-/domein-overrides en -aliassen,
  Kea-reservaties en Dnsmasq-hosts; leases lezen. Geheime velden
  (password, psk, secret, privkey …) haalt de server uit de aanroepen en
  bewaart ze versleuteld in de taak; de agent krijgt ze en laat ze weg uit
  de uitvoer, ook het logboek ziet ze niet. Interfaces toewijzen heeft geen
  API in OPNsense en blijft daar.
- **M4 (VPN, diensten, plugins, diagnose)**: WireGuard-peers en
  -instanties, OpenVPN-instanties, status van WireGuard/OpenVPN/IPsec;
  diensten starten/stoppen/herstarten; plugins (alleen `os-*`)
  installeren of verwijderen op elke firewall apart (`each`: eigen
  herstelpunt per node, geen sync, niet te mengen met andere wijzigingen);
  firewalllog live, pf-staten zoeken en wissen (`query_*` als
  leesverzoek), ARP-tabel. De koppeling met Logs (fase 23) en SOC (fase
  25) komt in M6.
- **M5 (HA en firmware)**: tab HA en firmware. CARP-onderhoudsmodus per
  firewall (`opn_carp`: blijvende vlag + demotion; aanzetten wacht tot de
  andere firewall MASTER is en de VIP's antwoorden, anders meteen weer
  uit); failovertest (overnemen, teruggeven, tijden meten). Firmware
  (`opn_firmware` check/update/upgrade, taak `opnsense.firmware`): eerst
  elke backup, dan de master in onderhoudsmodus, dan onderhoudsmodus uit;
  stopt bij de eerste fout zonder de master aan te raken. Drift per
  onderdeel van config.xml (interfaces, system, HA en CARP-skew tellen niet
  mee). Back-ups in ClusterForge (migratie 00105, tabel opnsense_backups):
  volledige config.xml versleuteld met de masterkey, elke dag en na elke
  wijziging, ongewijzigd geeft geen nieuwe rij, 30 per firewall; downloaden
  altijd met code of wachtwoord en in het logboek. Een revisie terugzetten
  (`OPNChange.Restore`) gaat als gewone veilige wijziging op de master
  (overtikken, terugdraaitimer, proeven, sync).
- **M6 (overal zichtbaar)**: Aandacht nodig (problemen per firewallcluster,
  crit rood; onderhoudsmodus die blijft staan; geen back-up in 48 uur),
  dashboard-API `/api/dashboard/v1/opnsense` (platte velden plus een rij per
  cluster, `?strict=1`), het filterlog en de andere logs van beide firewalls
  via Logs (fase 23, werkt vanzelf met de agent), README-sectie OPNsense.
