# Fase 33: Zabbix-monitoring in dashboards

Gevraagd door Jonas op 2026-11-17: "na de Zabbix-fix en de PBS-integratie komt
de Zabbix-monitoringintegratie: de Zabbix-API uitlezen, specifiek voor
ClusterForge/agents en algemene info, en in mooie dashboards zetten zoals
Grafana. Maak daar al een onderzoek en stappenplan voor."

Dit document is het onderzoek en het stappenplan. Er wordt nog niets gebouwd:
bouwen begint pas als de Zabbix-fix (PR #138, image `:53fb6e8`) en fase 32
(PBS) klaar zijn. Daarna bouwen we de mijlpalen na elkaar, zonder tussentijdse
goedkeuringsrondes, en melden we elke mijlpaal apart zodra hij klaar is.

## Uitgangspunten

- **ClusterForge blijft de enige beheertool.** De dashboards komen in de eigen
  webinterface, in dezelfde stijl als SOC en Monitoring. Grafana met de
  Zabbix-plugin is geen vereiste (zie "Waarom geen Grafana" onderaan).
- **Alleen de server praat met Zabbix.** Geen nieuw agentprotocol en niets
  nieuws op de nodes: de server leest de JSON-RPC-API van de Zabbix-server
  (VIP `192.168.199.50`) met het token dat al in *Monitoring → Zabbix-agents*
  staat (`zabbix_agent_settings.api_url` en `api_token_enc`, fase 28/#135).
- **Lezen eerst.** M1 t/m M5 lezen alleen. Schrijven naar Zabbix (probleem
  bevestigen, onderhoud zetten) komt pas in M6, met logboek en TOTP voor prod.
- **De browser stuurt nooit eigen API-aanvragen naar Zabbix.** Zoals bij de
  VictoriaMetrics-panelen (`internal/metrics`) kiest de browser alleen uit
  vaste panelen en vult hij hoogstens een node, cluster, item of tijdvenster
  in. De server bouwt de aanvraag, beperkt het aantal punten en cachet.
- **Zuinig voor Zabbix.** Eén verzamelronde per minuut voor de samenvatting
  (problemen, beschikbaarheid, proxy, HA), grafiekdata alleen op aanvraag met
  cache, nooit `output: extend` zonder `limit`, en niet verversen als het
  tabblad verborgen is.

## Wat er al is (stam `claude/project-thread-9p9qok-zbxfix`, `53fb6e8`)

| Onderdeel | Waar | Bruikbaar voor fase 33 |
|---|---|---|
| Zabbix-API-client (JSON-RPC, Bearer-token, foutafhandeling, `apiinfo.version`, `host.get`, `hostgroup.*`, `template.get`, `proxy.get`) | `internal/zabbixagent/api.go` | Wordt de basis. Verhuist naar een gedeeld pakket `internal/zabbix` zodat agentbeheer en dashboards dezelfde client gebruiken. |
| Instellingen met API-adres en versleuteld token, knop "API testen" | `zabbix_agent_settings`, `internal/zabbixagent/service.go` (`token`, `client`, `Test`) | Hergebruiken; geen tweede token nodig. |
| Koppeling node ↔ Zabbix-host | `zabbix_agent_nodes.zabbix_host_id` | Directe koppeling voor nodes met zabbix-agent2 van ClusterForge. Voor de rest matchen we op hostnaam en IP (M1). |
| Grafieken met uPlot, eenheden, donker thema | `web/src/components/charts/TimeChart.tsx`, `MetricsPanels.tsx` | Hergebruiken voor Zabbix-grafieken; eenheden aanvullen. |
| SOC-opbouw (band met niveau en reden, tegels met sparkline, "Nu belangrijk", snapshots voor trend) | `internal/soc`, `web/src/components/soc` | Zelfde patroon voor het Zabbix-overzicht. |
| Aandacht nodig | `internal/httpapi/attention.go` | Nieuwe regels voor Zabbix (M5). |
| Dashboard-API met API-tokens | `internal/httpapi/dashboard*.go`, `/api/dashboard/v1/{,clusters,nodes,soc}` | Nieuw endpoint `/api/dashboard/v1/zabbix` (M5). |
| Onderhoud / vensters | `internal/lifecycle`, fase 30 | Onderhoud doorzetten naar Zabbix (M6). |
| Pagina Zabbix-agents | `web/src/app/(app)/zabbix/page.tsx`, menu `nav.ts` | Wordt een tabblad naast het nieuwe overzicht. |

## De Zabbix 8.0-API: wat we nodig hebben

Alles via `POST /api_jsonrpc.php` met `Authorization: Bearer <token>`. De
methoden hieronder bestaan sinds 6.x en zijn in 7.x/8.0 stabiel; in M1 test de
server ze echt tegen Jonas' 8.0 (`apiinfo.version`) en de lijst met
API-wijzigingen 7.4 → 8.0 wordt dan nagelopen
(<https://www.zabbix.com/documentation/8.0/en/manual/api/changes>).

| Vraag | Methode | Belangrijke parameters / velden |
|---|---|---|
| Welke problemen staan open? | `problem.get` | `recent: false`, `severities`, `acknowledged`, `suppressed: false`, `selectTags`, `selectAcknowledges`, `sortfield: eventid`, `sortorder: DESC`, `limit`. Velden: `eventid`, `objectid` (triggerid), `clock`, `r_clock`, `severity`, `name`, `acknowledged`, `suppressed`, `opdata`, `cause_eventid`. |
| Bij welke host hoort een probleem? | `trigger.get` | `triggerids` uit `objectid`, `selectHosts: [hostid, host, name]`, `expandDescription`. (`problem.get` geeft zelf geen hosts.) |
| Geschiedenis van problemen (tijdlijn, MTTR) | `event.get` | `source: 0`, `object: 0`, `value: 1`, `time_from`/`time_till`, `selectHosts`, `select_acknowledges`, `r_eventid` voor herstel. |
| Hosts en beschikbaarheid | `host.get` | `output: [hostid, host, name, status, maintenance_status, active_available, monitored_by, proxyid]`, `selectInterfaces: [interfaceid, ip, dns, type, available, error]`, `selectHostGroups`, `selectTags`. `interfaces[].available` 0 onbekend, 1 ok, 2 onbereikbaar; `active_available` voor actieve checks. |
| Proxy's | `proxy.get` | `output: [proxyid, name, operating_mode, lastaccess, version, compatibility, state]`; `state` 0 onbekend, 1 offline, 2 online. Proxy `.199.14`. |
| HA-cluster van de Zabbix-server | `hanode.get` | `name`, `address`, `status` (0 standby, 1 gestopt, 2 onbereikbaar, 3 actief), `lastaccess`. Alleen voor Super admin; anders tonen we "geen rechten" in plaats van een fout. |
| Laatste waarden (latest data) | `item.get` | `hostids`, `search: {key_: …}` of `filter`, `output: [itemid, name, key_, value_type, units, lastvalue, lastclock, state, error, status]`, `selectTags`, `monitored: true`, `limit`. |
| Grafieklijnen | `history.get` / `trend.get` | `history.get`: `history` = value_type (0 float, 3 uint), `itemids`, `time_from`, `time_till`, `sortfield: clock`. Voor vensters langer dan ~24 u `trend.get` (`value_min`, `value_avg`, `value_max` per uur). |
| Gezondheid van Zabbix zelf | `item.get` op de host met template *Zabbix server health* | interne items: `zabbix[queue]`, `zabbix[queue,10m]`, `zabbix[requiredperformance]` (NVPS nodig), `zabbix[wcache,values]`, `zabbix[rcache,buffer,pused]`, `zabbix[process,<type>,avg,busy]`, `zabbix[preprocessing_queue]`. Idem *Zabbix proxy health* voor de proxy. |
| Onderhoud | `maintenance.get` / `.create` / `.update` | M6. |
| Bevestigen | `event.acknowledge` | `action` als bitmasker (2 bevestigen, 4 bericht, 8 ernst wijzigen, 1 sluiten), `message`. M6. |
| Services / SLA (optioneel) | `service.get`, `sla.get`, `sla.getsli` | Alleen als Jonas services in Zabbix gebruikt; later. |

Aandachtspunten uit het onderzoek:

- **Rechten van het token.** Het huidige token moet hosts kunnen aanmaken, dus
  hoort bij een Admin of Super admin. Voor lezen volstaat leesrecht op de
  hostgroepen; `hanode.get` vraagt Super admin. In M1 toont de pagina per
  onderdeel of het token het mag lezen, in plaats van stil leeg te blijven.
- **Ernst.** 0 niet ingedeeld, 1 informatie, 2 waarschuwing, 3 gemiddeld,
  4 hoog, 5 ramp. Kleuren volgen de bestaande tonen (rood = hoog/ramp,
  oranje = gemiddeld, geel = waarschuwing, grijs = info).
- **Onderdrukte problemen** (onderhoud of oorzaak-symptoom) tellen standaard
  niet mee, maar zijn met een filter zichtbaar.
- **Grote antwoorden.** `history.get` kan miljoenen rijen geven. De server
  vraagt per lijn hoogstens het venster dat getoond wordt, schakelt boven 24 u
  over op `trend.get` en dunt uit tot ongeveer 240 punten (zoals
  `internal/metrics`, `points = 240`).
- **Tijd.** Zabbix geeft Unix-seconden als string; de server zet om, de
  browser toont in `Europe/Brussels` (bestaande `nl-BE`-opmaak).
- **Bereikbaarheid.** Het API-adres is de VIP `.199.50`; bij een HA-wissel
  blijft het adres gelijk. Valt de API weg, dan toont de pagina de laatste
  stand met "bijgewerkt x min geleden" en komt er een regel in Aandacht nodig.

## Koppeling tussen ClusterForge-nodes en Zabbix-hosts

1. `zabbix_agent_nodes.zabbix_host_id` (node met agent2 van ClusterForge).
2. Anders: Zabbix-`host` gelijk aan de hostnaam van de node (zonder domein,
   hoofdletterongevoelig).
3. Anders: een interface-IP gelijk aan `nodes.primary_ip`.
4. Lukt geen van drie, dan staat de node als "niet in Zabbix" en de host als
   "geen ClusterForge-node". Jonas kan in de GUI handmatig koppelen of
   ontkoppelen (wordt bewaard).

Zo zien we ook het omgekeerde gat: hosts in Zabbix die ClusterForge niet kent
(bv. OPNsense, Proxmox-hosts zelf, switches) komen in "Overige hosts", en nodes
met een ClusterForge-agent maar zonder Zabbix-host in "Niet bewaakt door
Zabbix".

## Mijlpalen

### M1: fundament en Zabbix-overzicht

- Pakket `internal/zabbix`: de client uit `zabbixagent` verhuist hierheen (met
  de bestaande tests) en krijgt de leesmethoden hierboven. `zabbixagent` gebruikt
  daarna dit pakket.
- Verzamelaar (`Loop` elke 60 s, na een fout met terugval tot 5 min): problemen,
  hosts met beschikbaarheid, proxy's, HA-nodes, gezondheidsitems van server en
  proxy. Resultaat in het geheugen (één snapshot) + elk kwartier een kleine
  momentopname in de database voor trends (tellers per ernst, beschikbaar/
  onbereikbaar, NVPS, wachtrij). Patroon van `soc_snapshots`.
- Koppeling node ↔ host (zie boven), met de handmatige koppelingen in een
  tabel.
- Migratie (nummer af te stemmen, zie Nummers): `zabbix_snapshots`,
  `zabbix_host_links`.
- API: `GET /api/v1/zabbix/overview?window=24h|7d`, `GET /api/v1/zabbix/problems`
  (filters ernst, bevestigd, onderdrukt, cluster, node, tag), `GET /api/v1/zabbix/hosts`,
  `PUT/DELETE /api/v1/zabbix/hosts/{hostid}/link`. OpenAPI bijwerken.
- GUI: menu Monitoring → **Zabbix** met tabbladen *Overzicht* · *Problemen* ·
  *Hosts* · *Agents* (de bestaande pagina). Overzicht in SOC-stijl:
  - band met de toestand (rampen/hoog open, server of proxy plat, API
    onbereikbaar) en altijd de reden;
  - tegels met sparkline: open problemen per ernst, hosts beschikbaar /
    onbereikbaar, ClusterForge-nodes bewaakt, NVPS, wachtrij > 10 min,
    proxy-status, HA (actieve node);
  - "Nu belangrijk": problemen gesorteerd op ernst en duur, met node en
    cluster als link;
  - dekking: ClusterForge-nodes zonder Zabbix-host en omgekeerd.
- Kijkers zien alles; er zijn nog geen acties.

### M2: Zabbix bij nodes en clusters

- Node-detail krijgt een tabblad **Zabbix**: beschikbaarheid per interface en
  actief, open problemen, triggers die nu in probleem staan, laatste waarden
  (zoeken op naam/sleutel, gegroepeerd per tag `component`), knop "Open in
  Zabbix".
- Cluster-detail: samenvatting per node (bolletjes per ernst) en open
  problemen van alle nodes van het cluster.
- Grafieken bij een item: `GET /api/v1/zabbix/items/{itemid}/series?range=1h|6h|24h|7d|30d`,
  alleen voor numerieke items (value_type 0/3) van een gekoppelde of bekende
  host; boven 24 u trends met min/gem/max als band.
- Vaste node-grafieken uit het template *Linux by Zabbix agent* (CPU, geheugen,
  schijf, netwerk, load) naast de bestaande VictoriaMetrics-grafieken, zodat
  ook nodes zonder ClusterForge-metrics grafieken hebben.
- Status van de node: een open Zabbix-probleem van ernst hoog/ramp wordt
  zichtbaar in de statusreden (niet in de status zelf, die blijft van de
  ClusterForge-agent).

### M3: dashboards zoals Grafana (vaste dashboards)

- Een dashboardweergave met raster van panelen, tijdkiezer (1 u tot 30 d en
  eigen venster), automatisch verversen (uit / 30 s / 1 min / 5 min), gedeelde
  cursor over de grafieken, inzoomen door te slepen, volledig scherm en een
  **kioskmodus** voor een tv (`?kiosk=1`, zonder menu, donker).
- Paneeltypes: tijdlijn (uPlot), stat met sparkline, meter, balk/top-N,
  tabel, problemenlijst, beschikbaarheidsraster (heatmap host × tijd),
  honingraat per host (kleur = zwaarste open probleem), tekst.
- Ingebouwde dashboards, als code (JSON in de repo):
  1. **Infrastructuur**: honingraat van alle hosts, problemen per ernst over
     tijd, top-10 CPU / geheugen / schijf / netwerk.
  2. **ClusterForge-nodes**: per cluster een rij, per node CPU, RAM, schijf,
     beschikbaarheid, open problemen.
  3. **Zabbix zelf**: HA-nodes, NVPS, wachtrij, caches, drukke processen,
     proxy-gezondheid en `lastaccess`.
  4. **Problemen**: aantal over tijd, MTTR, top-triggers, top-hosts,
     bevestigd versus niet bevestigd (uit `event.get`).
- Variabelen bovenaan: cluster, node, hostgroep (zoals Grafana-templating).
- Panelen vragen data via één endpoint `POST /api/v1/zabbix/panels/query` met
  een lijst van paneelomschrijvingen; de server controleert elk (type, host,
  sleutel, venster) tegen de toegestane vormen, haalt het in parallel op en
  cachet 30 s.

### M4: eigen dashboards

- Jonas kan dashboards maken en aanpassen: panelen toevoegen, slepen,
  grootte wijzigen, per paneel kiezen wat het toont (item via zoekveld met
  sleutel/naam, of een vaste bron zoals "problemen van cluster X"), drempels
  en kleuren, eenheid.
- Bewaren in de database (migratie `zabbix_dashboards`: naam, JSON, eigenaar,
  gedeeld ja/nee, gewijzigd). Ingebouwde dashboards dupliceren als vertrekpunt.
- Exporteren/importeren als JSON, en mee in GitOps/Auto-documentatie als Jonas
  dat wil.
- Een dashboard als startpagina kiezen; snelkoppeling in Ctrl K.

### M5: koppelingen met de rest van ClusterForge

- **Aandacht nodig**: Zabbix-API onbereikbaar of token geweigerd; proxy
  offline; HA zonder actieve node; open problemen ramp/hoog langer dan 15 min
  niet bevestigd; ClusterForge-nodes zonder Zabbix-bewaking.
- **SOC**: Zabbix-problemen met tag `scope: security` (of uit
  beveiligingstemplates) in "Nu belangrijk"; dekking "bewaakt door Zabbix" als
  extra kolom (telt niet mee in de score, om de score niet te veranderen).
- **Tijdlijn, What Broke en Assistent**: Zabbix-problemen en herstel als
  gebeurtenissen naast ClusterForge-wijzigingen, zodat "wat veranderde er vlak
  voor dit probleem" ook Zabbix ziet.
- **Incidenten**: optioneel een incident openen vanuit een Zabbix-probleem (knop),
  met het event-id als verwijzing.
- **Dashboard-API**: `GET /api/dashboard/v1/zabbix` (tellers per ernst,
  beschikbaarheid, proxy, HA, top-5 problemen; `?cluster=`, `?strict=1`) voor
  Jonas' homepage, zelfde tokens als fase 20.

### M6: acties naar Zabbix (schrijven)

- Probleem **bevestigen** met bericht, en eventueel ernst wijzigen of sluiten
  (alleen als de trigger handmatig sluiten toelaat) via `event.acknowledge`.
  Admin, in het logboek; prod-clusters met TOTP zoals elders.
- **Onderhoud doorzetten**: node in onderhoud in ClusterForge (of een
  onderhoudsvenster uit fase 30) → `maintenance.create` in Zabbix voor die host
  met "met dataverzameling", en weer weg als het onderhoud eindigt. Zo komen er
  geen valse meldingen tijdens geplande reboots en updates.
- "Host ontbreekt in Zabbix" oplossen met één klik (verwijst naar de
  bestaande zabbix-agent2-uitrol van fase 28/#135).

## Nummers (afstemmen)

- **Agentprotocol**: niet nodig; alles loopt via de server.
- **Migraties**: vrij vanaf 00096, maar fase 32 (PBS, branch
  `claude/project-thread-xudhcp`) gebruikt mogelijk ook nummers vanaf 00096.
  Goose weigert migraties buiten volgorde, dus vóór het bouwen afspreken met de
  fase 32-thread. Voorstel: fase 32 neemt 00096-00099, fase 33 neemt
  00100-00104 (M1 `zabbix_snapshots` + `zabbix_host_links`, M4
  `zabbix_dashboards`, reserve voor M6).
- **Stam**: bouwen op de hoogste stam na #138 en fase 32 (één gecombineerd
  image op de hoogste stam, nooit een image van een lagere stam geven).

## Risico's en keuzes

| Punt | Keuze |
|---|---|
| Druk op de Zabbix-server | Eén verzamelronde per minuut met beperkte `output`; grafiekdata op aanvraag met cache; `trend.get` voor lange vensters. |
| Token met te veel rechten | Hetzelfde token als agentbeheer (al versleuteld). Optioneel later een apart leestoken; de instellingen krijgen dan een tweede veld. |
| API-wijzigingen tussen 7.x en 8.0 | Client test de versie; methoden met een gekende wijziging krijgen een versiecheck. Tests met opgenomen antwoorden van 8.0. |
| Zabbix plat | Laatste stand blijft zichtbaar met leeftijd; Aandacht nodig meldt het. |
| Dubbele grafieken (VictoriaMetrics én Zabbix) | Node-detail toont beide bronnen gescheiden; geen samenvoegen van reeksen. |

## Waarom geen Grafana (wel mogelijk)

Grafana met de Zabbix-plugin (`alexanderzobnin-zabbix-app`) kan dit ook, maar
dan heeft Jonas een tweede tool met eigen login, eigen tokens en dashboards
die de ClusterForge-koppeling (cluster, node, onderhoud, incidenten) niet
kennen. We bouwen daarom de dashboards in ClusterForge zelf, met dezelfde
Grafana-achtige bediening. Wie toch Grafana wil, kan in M4 een link per
dashboard naar een externe URL zetten.

## Testen

- Eenheidstests met opgenomen JSON-antwoorden (geen echte Zabbix in CI).
- Een test-HTTP-server die `api_jsonrpc.php` nabootst voor de verzamelaar,
  inclusief fouten (token geweigerd, rechten, time-out).
- Schermafdrukken per mijlpaal in `clusterforge/schermen/fase33-*.png`,
  desktop, donker en gsm.
