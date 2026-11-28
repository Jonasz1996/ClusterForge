# Fase 49: Scannen, uitzonderingen en eigenaarschap (publicatie)

Gevraagd door Jonas op 28/11. Gebouwd op fase 35/42/48 (publicatie via NPM en
Authentik, voorstellen voor een nieuw doel, opruimen van dode doelen).

## Scannen

- Knop **Scannen** op Publicatie: leest de diensten, NPM, Authentik en elk doel
  nu (en wacht daarop), en maakt één plan in drie soorten:
  - **toevoegen**: webdienst zonder host (exact doel: VIP of één node), met
    Authentik volgens de uitzondering;
  - **bijwerken**: forward auth ontbreekt, Authentik half, verkeerd
    outpost-adres of oud adres in Advanced, verkeerd doel (zeker voorstel,
    naar het VIP, of een doel dat 404 geeft), Authentik weghalen bij "nooit"
    of "eigen login", open paden zetten, en **verhuizen** (zie eigenaarschap);
  - **verwijderen**: host van een verdwenen dienst en Authentik-app/-provider
    zonder NPM-host. Onbereikbaar is op zich geen reden (scan 28/11): de
    host komt alleen in het plan als ClusterForge het doel zelf heeft
    bekeken en het niet antwoordt, het doel een IP is dat niet in de
    inventaris staat (geen node, ook geen uitgeschakelde, en geen VIP), niet
    op .1 tot .3 eindigt (gateway/firewall) en er geen dienst met die naam
    meer draait. Anders staat het als opmerking onder het plan. Vlak voor het
    uitschakelen controleert de taak het doel nog eens vanaf de NPM-node;
    antwoordt het dan wel, dan blijft de host staan. Altijd eerst
    **uitschakelen met een kopie** (`publish_disabled`); terugzetten of
    definitief verwijderen staat onder Uitgeschakeld.
- Goedkeuren: alles, de gekozen regels of één regel. Dat is één taak
  (`action: scan`): kopie van NPM, per host een eigen kopie, de wijziging, een
  echte aanvraag als controle (ook op elk open pad) en terugdraaien bij een
  fout; de andere regels gaan door.
- Elke regel wordt bij het goedkeuren opnieuw berekend op wat NPM nu is; wat
  niet meer klopt, slaat het plan over met de reden.
- **Niets vanzelf**: `defaults.manual_only` (standaard aan) zet automatisch
  publiceren (fase 48) en Authentik aanvullen (fase 42d) stil.

## Uitzonderingen (`publish_policies`)

Op het eerste deel van de naam (ook zonder cijfers op het eind: proxmox50 →
proxmox). Soorten: `authentik`, `never`, `own_login`, `except_paths` (paden).
Zonder uitzondering: de lijst "eigen login" van de standaarden, anders
Authentik. Open paden komen in een eigen blok in Advanced
(`# clusterforge-extra`): een bestand als `location = /pad`, een map als
`location ^~ /pad`, zonder `auth_request`.

## Eigenaarschap (`publish_owners`)

Elke naam (NPM-host en Authentik-app samen) hoort bij één cluster of dienst:
vastgelegd bij een scan als de dienst bekend is, bij elke publicatie of
verhuizing, of met de hand. Wil een scan een naam maken die al bestaat, dan
wordt het een verhuizing: het doel gaat naar de nieuwe dienst en het oude
doel blijft **terugval** (`error_page 502 504 = @cf_fallback`, ook in de
location van Authentik) tot Jonas "terugval weghalen" kiest.
Testclusters (omgeving `test`) krijgen standaard `app.test.<domein>`, zodat
test en productie nooit om een naam vechten. Een labcluster (zoals media)
krijgt gewoon `app.<domein>` (scan 28/11).

## Doel en outpost (scan 28/11)

- Een cluster met een VIP wijst altijd naar dat VIP, ook als de dienst nu
  maar op één node draait. Het eigen VIP van een database (Patroni,
  omschrijving met "database") telt niet mee.
- Het outpost-adres van Authentik is het app-VIP van het Authentik-cluster,
  niet het eerste VIP op adres (dat was de database-VIP .240 in plaats van
  .244).
- Een betere poort op hetzelfde adres (vaultx 80 → 443) wordt alleen een
  opmerking, geen wijziging, en enkel als het huidige doel echt niet
  antwoordt.

## Nummers

Migraties 00163 (tabellen) en 00164 (uitzonderingen van Jonas). Geen nieuw
agentprotocol: uitschakelen gaat via de bestaande NPM-API
(`/nginx/proxy-hosts/{id}/disable|enable`).
