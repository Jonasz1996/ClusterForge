# Fase 25: SOC-dashboard

Gevraagd 2026-11-16: "professioneel SOC-dashboard en verdere optimalisatie /
gebruiksvriendelijkheid van de GUI, voorbeeld het SOC-dashboard in de
aiverslag-repo. Kwaliteit boven kwantiteit."

## Wat we overnemen van aiverslag (v4/start.js, v4/web.py)

- **Eén dreigingsniveau** bovenaan: kritiek · verhoogd · waakzaam · normaal,
  altijd met de *reden* erbij (hoogstens twee zinnen). Geen kleur zonder uitleg.
- **KPI-tegels met trend** (sparkline, verschil met de vorige periode); elke
  tegel is een link naar de plek waar je het oplost.
- **"Nu belangrijk"**: één gesorteerde lijst (prioriteit = ernst × zekerheid),
  incidenten eerst, dubbels weg.
- **Kaartje bij een IP**: alles wat we over een adres weten op één plek
  (land, netwerk, op welke nodes, welke poorten/scenario's, geband of niet).
- Zuinig: niet verversen als het tabblad verborgen is; antwoord 15 s gecachet.

Niet overgenomen: dreigingsfeeds (KEV, URLhaus …) en een AI-briefing. CrowdSec
levert de reputatie al via de Central API; een briefing komt er pas als de
deterministische samenvatting niet volstaat.

## Bronnen (niets nieuws op de nodes, geen nieuw agentprotocol)

| Bron | Tabel | Wat |
|---|---|---|
| CrowdSec (fase 14) | `crowdsec_alerts`, `crowdsec_decisions`, `crowdsec_nodes` | aanvallen, bans, bouncer/engine-status |
| Firewall (fase 16) | `firewall_blocked`, `firewall_nodes`, `firewall_settings` | verkeer op dichte poorten, beheerd of niet |
| Inloggen op ClusterForge | `events` (`auth.login_failed`, `auth.rate_limited`, `auth.reauth_failed`) | aanvallen op de beheertool zelf |
| Incidenten | `incidents` | open storingen |
| Updates | facts van de node (`upgrades.security`) | openstaande beveiligingsupdates |
| Agents | nodes + agentstatus | offline = blinde vlek |
| Logboek | `events` categorie `security` | wie zette wat aan/uit, bans |

Logs (fase 23) worden niet bewaard; vanuit een aanvaller ga je met één klik
naar Logs met het IP als zoekterm.

## Dreigingsniveau (vaste regels, uitlegbaar)

- **Kritiek**: open incident met ernst *critical*; een bouncer ligt plat op een
  node die de laatste 24 u aangevallen werd (bans worden niet tegengehouden);
  brute force op ClusterForge (≥ 10 mislukte logins van één extern IP in 1 u);
  een extern IP valt ≥ 3 nodes aan en is nergens geband.
- **Verhoogd**: open incident *high*; aanvallen 24 u > 3× het daggemiddelde van
  de week ervoor (en ≥ 20); CrowdSec draait niet op een beheerde node;
  firewall of CrowdSec toepassen mislukt; mislukte logins van buiten het LAN.
- **Waakzaam**: gaten in de bescherming (actieve node zonder beheerde firewall
  of zonder CrowdSec/bouncer), openstaande beveiligingsupdates, offline agents,
  tweestapsverificatie uit.
- **Normaal**: niets van het bovenstaande.

## Beveiligingsscore 0–100 ("waarom niet 100?")

Vier delen, elk met de aftrekposten en een link:

- Dekking (35): per actieve node firewall beheerd, CrowdSec + bouncer actief,
  agent online.
- Patches (25): aandeel nodes zonder openstaande beveiligingsupdates.
- Toegang (15): tweestap voor elke beheerder, SSH niet open voor *any*.
- Dreiging (25): af voor open kritieke/verhoogde punten.

Elk uur een momentopname (migratie 00072 `soc_snapshots`) voor de trend.

## Pagina "SOC" (Beveiliging, bovenaan)

1. Band: niveau + redenen, venster 24 u / 7 d, bijgewerkt-tijd.
2. Tegels: Score · Aanvallen · Actieve bans · Geblokkeerd verkeer · Mislukte
   logins · Beveiligingsupdates.
3. Nu belangrijk (met actieknop) | Activiteit per uur (gestapeld: CrowdSec,
   firewall, logins).
4. Aanvallers: samengevoegd per IP over de drie bronnen; extern/intern
   (intern = LAN-netwerken uit de firewallinstellingen); kaartje met acties
   (bannen/opheffen via CrowdSec, Zoek in logs, kopiëren).
5. Doelwitten: meest aangevallen nodes, poorten en scenario's.
6. Dekking per node: agent, firewall, CrowdSec, bouncer, updates; filter
   "alleen gaten".
7. Beveiligingslogboek: de laatste beveiligingsevents.

Kijkers zien alles behalve de acties.

## Mijlpalen

- **M1** `internal/soc` + `GET /soc` + migratie 00072 + pagina SOC (band,
  score met uitleg, tegels, nu belangrijk, activiteit, aanvallers, dekking).
- **M2** aanvallerkaart met acties, doelwitten, beveiligingslogboek, punten
  "gezien" zetten, SOC-niveau in "Aandacht nodig" en in de dashboard-API
  (homepage-tegel).
- **M3** GUI-optimalisatie, afgestemd met de revisie (fase 24) zodat niets
  dubbel gebeurt.
