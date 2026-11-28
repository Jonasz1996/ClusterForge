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
  - **verwijderen**: host van een verdwenen dienst (doel antwoordt niet, geen
    voorstel) en Authentik-app/-provider zonder NPM-host. Altijd eerst
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
Testclusters (test, lab) krijgen standaard `app.test.<domein>` (of
`app.lab.<domein>`), zodat test en productie nooit om een naam vechten.

## Nummers

Migraties 00163 (tabellen) en 00164 (uitzonderingen van Jonas). Geen nieuw
agentprotocol: uitschakelen gaat via de bestaande NPM-API
(`/nginx/proxy-hosts/{id}/disable|enable`).
