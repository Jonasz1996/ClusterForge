# Fase 40: OPNsense volledig en automatisch beheren

Gevraagd door Jonas op 2026-11-17 18:43 UTC: "automatisch beheer van de
firewall door logs uit te lezen, kijken wat er op de agents openstaat,
reservaties, alles; zodat ik de OPNsense-interface niet meer nodig heb."

Fase 34 gaf het lezen en het veilig wijzigen (herstelpunt, terugdraaitimer,
proeven, sync naar de backup). Fase 40 bouwt daarop verder en voegt twee dingen toe:

1. **Automatisch**: ClusterForge weet welke nodes, clusters, VIP's, VLAN's en
   open poorten er zijn. Daaruit maakt hij zelf aliassen, DNS-overrides,
   DHCP-reservaties en regelvoorstellen. Uit het filterlog leert hij wat er
   geblokkeerd wordt en wat nooit gebruikt wordt.
2. **Volledig**: wat in fase 34 nog ontbrak om de webinterface van OPNsense
   niet meer nodig te hebben.

Alles blijft gaan via de veilige wijziging van fase 34 (`opnsense.change`).
Er komt geen tweede manier om te schrijven.

## Grondregels

- **Alleen op de firewall de agent.** Al de rest via de GUI.
- **Drie standen per soort**, zoals ufw in fase 16:
  - *uit*;
  - *voorbeeld*: alleen tonen wat er zou veranderen;
  - *beheerd*: toepassen wanneer het plan verandert, altijd als één veilige wijziging met de gewone proeven.
- **ClusterForge raakt alleen wat van hem is.** Zijn objecten herkent hij aan:
  - de omschrijving `cf:` voor regels, overrides en reservaties;
  - de naam `cf_` voor aliassen;
  - de categorie `ClusterForge` voor regels.
  
  Wat met de hand gemaakt is, blijft staan. Hij toont het hoogstens als voorstel.
- **Nooit automatisch iets dat de toegang kan raken.** Een nieuwe regel die
  verkeer *toelaat* komt nooit vanzelf: het blijft een voorstel met één klik.
  Een regel *wegnemen* of *blokkeren* komt ook nooit vanzelf. Vanzelf in de
  stand beheerd gaan alleen de aliassen, de DNS-overrides en de
  DHCP-reservaties.
- **Prod** vraagt een code, zoals in fase 34.

## Mijlpalen

### M0: stappenplan
Dit document en een draft PR.

### M1: verkeer uit het filterlog
- **Agent (protocol 56)**:
  - Een leesverzoek `opn_filterlog` leest `/var/log/filter/latest.log`
    stukje bij stuk, met inode en offset zoals bij de NPM-logs van fase 39.
  - Het filterlog van pf is CSV. De agent ontleedt het en telt per minuut per
    (actie, interface, richting, bron, doel, poort, protocol, regel-id).
  - Zo gaat er weinig over de lijn en blijft er niets op de firewall staan.
- **Server**:
  - Elke minuut van de master en elke backup lezen.
  - Opslaan in `opnsense_flows`: per uur, 30 dagen bewaren.
  - Elk adres koppelen aan een node, VIP, cluster of VLAN-net. Dat lukt via de
    adressen van de agents, de VIP's, de interfaces van OPNsense en de leases.
- **GUI-tab Verkeer**:
  - De meest geblokkeerde en de meest toegelaten stromen, met de namen van de
    nodes in plaats van IP's;
  - per regel hoe vaak hij raakte;
  - per node wat er binnen- en buitenkomt.

### M2: automatisch aliassen, DNS en reservaties
Elk van de drie soorten hieronder heeft zijn eigen stand (uit, voorbeeld of beheerd).

- **Aliassen** worden bijgewerkt zodra er een node, VIP of cluster bijkomt of
  verdwijnt:
  - `cf_<cluster>` met de nodes en VIP's van elk cluster;
  - `cf_nodes` met alle nodes met een agent;
  - `cf_vlan_<naam>` met het net van elke VLAN.
- **DNS**: een Unbound-host-override per node en per cluster-VIP, onder een
  domein uit Instellingen (bijvoorbeeld `lan.jbogaert.be`).
- **DHCP-reservaties** voor elke node met een vast adres, met het MAC-adres uit
  de facts van de agent. De reservatie gaat in de DHCP-dienst die het net
  bedient: Kea, Dnsmasq of ISC.
  - ISC heeft geen API. Daarvoor gebruikt de agent een klein config-script met
    het herstelpunt van fase 34.
  - Een lease kun je ook met één klik als reservatie vastzetten.
- **Conflicten**:
  - Een adres dat al voor een ander MAC gereserveerd is: ClusterForge toont
    het en raakt het niet.
  - Een node die op .1-.3 van een VLAN staat (gateways): dat gaat naar Aandacht
    nodig.

### M3: regelvoorstellen
- **Uit open poorten**:
  - Fase 16 kent per node de poorten die openstaan en voor wie.
  - Fase 35 kent per dienst het VIP.
  - Daaruit volgt welk verkeer tussen VLAN's de firewall moet doorlaten, zoals
    pc-VLAN naar het VIP van Nextcloud op 443, of de agents naar de server op
    4222 en 8080.
  - Ontbreekt zo'n regel, dan wordt het een voorstel met de juiste aliassen.
- **Uit het filterlog**:
  - Een stroom die herhaaldelijk geblokkeerd wordt van een bekende bron naar
    een open poort van een node wordt een voorstel "toelaten".
  - Een onbekende bron die veel poorten probeert wordt een voorstel
    "blokkeren", met een link naar CrowdSec.
- **Opruimen**:
  - regels die 30 dagen niet geraakt hebben;
  - regels die door een eerdere regel nooit kunnen raken;
  - dubbele regels;
  - regels die alles toelaten (any → any).
  
  Dit is altijd een voorstel: uitzetten, niet wissen.
- Een voorstel heeft de knoppen *Toepassen* (veilige wijziging) en *Negeren*
  (wordt onthouden).

### M4: wat nog ontbrak (netwerk)
- **Interfaces**: toewijzen, aan of uit, IPv4 statisch of DHCP, beschrijving.
  - Er is geen API voor; de agent doet het met een config-script.
  - Het herstelpunt, de terugdraaitimer en de proeven blijven zoals in fase 34.
  - Overtikken van de clusternaam is altijd nodig.
- **DHCP**:
  - Kea: subnetten en pools;
  - Dnsmasq: bereiken;
  - ISC: bereiken en statische toewijzingen (config-script).
- **DNS**: de algemene instellingen van Unbound, doorsturen naar upstream
  (DoT), blocklists en de cache leegmaken.
- **Systeem**: hostnaam, domein, DNS-servers, NTP, syslog naar buiten en
  cronjobs.

### M5: wat nog ontbrak (rest) en overal zichtbaar
- **Certificaten**: bekijken, verlopen in Aandacht nodig, importeren.
- **Gebruikers en groepen**. De gebruiker `clusterforge` zelf blijft
  onaantastbaar.
- **IPsec-verbindingen** beheren.
- **IDS (Suricata)**: aan of uit, regelsets en meldingen.
- **Traffic shaper** en **monit**.
- **Aandacht nodig**:
  - een voorstel dat wacht;
  - een geblokkeerde stroom van een eigen node;
  - een certificaat dat binnen 14 dagen verloopt;
  - een conflict bij een reservatie.
- **Dashboard-API** `/opnsense`: het aantal open voorstellen en de top van wat geblokkeerd wordt.
- README.

## Nummers
- **Protocol 56.**
- **Migraties vanaf 00121**:
  - `00121_opnsense_flows` (M1);
  - `00122_opnsense_auto` met de standen, de genegeerde voorstellen en de
    instellingen (M2/M3).

## Open punten (voorstel wordt gevolgd tenzij Jonas anders zegt)
1. **Het domein voor de DNS-overrides.** Voorstel: `lan.<hoofddomein>` uit
   Instellingen → Domeinen.
2. **De standaardstand.** Voorstel: voorbeeld voor alles. Jonas zet zelf
   aliassen, DNS en reservaties op beheerd.
3. **ISC DHCP.** Voorstel: ondersteunen via het config-script, omdat veel
   installaties het nog gebruiken. Wie Kea of Dnsmasq gebruikt, merkt er niets van.
