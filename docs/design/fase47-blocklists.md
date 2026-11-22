# Fase 47: CrowdSec zonder cloud

Gevraagd door Jonas op 2026-11-22: na enkele dagen zat hij aan de gratis limiet
van de CrowdSec Console en kwamen er geen meldingen meer. Hij wil niet van de
CrowdSec-cloud afhangen: ClusterForge wordt de centrale server, de detectie
(profielen van fase 14) blijft, de blocklist wordt centraal en lokaal beheerd,
en hij wil zelf meer kunnen blokkeren, met de hand en automatisch over alle
nodes heen.

## De limiet

De gratis Console toont 500 alerts per maand; daarna verbergt ze nieuwe alerts
tot de 1e van de maand (docs.crowdsec.net/u/console/alerts/quotas). De
open-source engine, de lokale API, de bouncers en de community-blocklist (CAPI)
werken lokaal en zonder limiet. ClusterForge haalde de alerts al sinds fase 14
rechtstreeks van elke node (`cscli alerts list`), dus de detectie hangt niet van
de Console af.

## Keuze

CrowdSec-engine en bouncer blijven op de nodes. Alleen de Console gaat eruit,
en ClusterForge neemt haar rol over (lijsten, automatisch blokkeren, meldingen).
De agent vervangen zou de profielen, parsers en scenario's opnieuw vragen, voor
geen winst.

## Centrale lijsten

| Soort | Wat |
| --- | --- |
| `own` (Eigen blocklist) | met de hand; per waarde een reden en eventueel een einddatum |
| `auto` (Automatisch geblokkeerd) | de automatische regels (hieronder) |
| `public` | publieke lijsten uit de catalogus of een eigen https-URL |

Catalogus: FireHOL level 1, Spamhaus DROP v4/v6, abuse.ch Feodo Tracker,
blocklist.de, CINS Army, Emerging Threats compromised. Elke bron heeft een
kleinste interval volgens haar voorwaarden (Spamhaus: hoogstens één keer per
uur); ClusterForge haalt voorwaardelijk op (ETag, If-Modified-Since), met een
neutrale User-Agent, hoogstens 16 MB en 100 000 waarden.

**Nooit geblokkeerd** (Guard): de whitelist van CrowdSec, de adressen van alle
nodes, en vaste netwerken: privé, loopback, link-local, CGNAT, 0/8 en 224/3
(multicast; VRRP van Keepalived gebruikt 224.0.0.18), IPv6 ULA/link-local/
multicast en ruimer dan /8 (v4) of /16 (v6). FireHOL level 1 bevat zelf
10/8 en 192.168/16: die vallen dus weg. De Guard geldt bij elke uitrekening, dus
een nieuwe whitelist werkt meteen door.

Elke wijziging bewaart een **versie** (eigen en automatisch: 30, publiek: 5).
Terugdraaien toont eerst het voorbeeld; een teruggedraaide publieke lijst wordt
vastgezet tot "Nu ophalen".

## Naar de nodes (agent protocol 57)

- `list_import {tag, duration, values}`: `cscli decisions import --format values`
  per scope (ip en range), reden = de tag. Stukken van hoogstens 5000.
- `list_drop {drop}`: `cscli decisions delete --origin cscli-import --scenario <tag>`.
- `poll` meldt het aantal bans per tag (uit `cscli metrics`), en leest de
  gewone bans nu met `--origin crowdsec` en `--origin cscli`, zodat een lijst
  van tienduizenden IP's de poll niet zwaar maakt.
- `console_off`: `cscli console disable --all` en CrowdSec herstarten.

De tag is `cf-list:<slug>:<generatie*1000+teller>`. Elke andere inhoud is een
nieuwe generatie. De lijstronde (elke minuut, apart van de poller) zet de
nieuwe tag en haalt daarna de oude weg, dus zonder gat. Een lijst geldt 168 uur
op de node en wordt na 72 uur ververst; valt ClusterForge weg, dan vervallen de
bans vanzelf. Staat de tag niet (meer) op de node (CrowdSec leeggemaakt), dan
zet de ronde hem opnieuw. Elke `cf-list:`-tag die niet meer hoort, gaat weg.

## Automatisch blokkeren

Standaard aan: een IP met alerts op 2 nodes binnen 60 minuten, of 3 alerts
binnen 24 uur, gaat 7 dagen in de lijst `auto`; bij elke herhaling 2× zo lang,
tot 90 dagen. Na 90 dagen rust begint de teller opnieuw. Vrijgeven of
Deblokkeren onthoudt het moment: oudere alerts tellen niet meer mee.

"IP blokkeren" (pagina Bans) zet het IP op alle nodes standaard ook in de eigen
lijst (eventueel voor altijd). Deblokkeren haalt het ook uit de eigen en de
automatische lijst.

## Meldingen

Kanalen: ntfy, Discord, Slack, Telegram en een eigen JSON-webhook; adressen en
tokens versleuteld met de masterkey. Meldingen voor automatische blocks, veel
alerts (drempel per 10 minuten), CrowdSec of bouncer die stilstaat, lijsten die
mislukken, optioneel elke nieuwe alert (samenvatting) en wat met de hand
gebeurt. Hoogstens 30 per uur.

## Console loskoppelen

Instellingen → "Console loskoppelen": de enroll-sleutel gaat weg en elke node
met protocol 57 krijgt `console_off`. Een nieuwe sleutel bewaren zet de Console
weer aan.

## GUI

CrowdSec → **Blocklists** (lijsten, IP's blokkeren met voorbeeld, publieke
lijst toevoegen met voorbeeld, waarden, versies en terugdraaien, regels voor
automatisch blokkeren) en → **Meldingen**. Alles in het logboek
(`crowdsec.list_*`, `crowdsec.auto_*`, `crowdsec.notify_changed`,
`crowdsec.console_*`).

Migratie 00141 (00129 is fase 46).
