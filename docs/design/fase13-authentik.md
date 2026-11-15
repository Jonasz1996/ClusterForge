# Fase 13: Authentik als HA-cluster

Gevraagd op 2026-11-15. Er draaien twee losse Authentik-nodes. De tweede is
een kloon van de eerste en keepalived regelt de failover. Elke wijziging
(applicaties, ongeveer 100 proxy providers, outposts) moet op beide nodes
gebeuren. Het doel is één gedeelde staat, waarbij de bestaande configuratie
van één node zonder verlies overkomt.

## Waarom actief op alle nodes

De server en de worker van Authentik zijn stateless. Alle configuratie en
sessies staan in PostgreSQL; Redis is niet meer nodig sinds 2025.10. De
documentatie van Authentik ("High availability") raadt aan om meerdere
servers en workers tegen één (HA-)PostgreSQL te draaien, met gedeelde opslag
voor de media. Daarom draait Authentik hier op elke node tegelijk. Anders dan
bij NPM en Home Assistant hoeft er bij een failover niets gekopieerd of
gestart te worden.

## Opbouw

- Template `authentik` 1.0.0, met 3 of 5 nodes en één rol `node`.
- **PostgreSQL met Patroni** draait op alle nodes (`database: engine: postgresql`)
  en krijgt een eigen VIP (`db_vip`) op de primary. Daarna maakt ClusterForge
  de gebruiker en de database `authentik` (`db_user` met `grant_on`, die
  eigenaar wordt). `client_networks` is 172.16.0.0/12 voor de containers op
  de node die het VIP van de database heeft. `max_connections` is 300.
- **Authentik** draait als `cf-authentik.service` (enabled, `Restart=always`)
  met Docker Compose. Daarin zitten `server`, `worker` en een Redis per node,
  die alleen versies van vóór 2025.10 gebruiken. Het image is
  `ghcr.io/goauthentik/server:<version>`.
  `/opt/cf-authentik/files/{data,media,templates,certs}` zijn de mounts.
  De worker krijgt geen docker.sock. Een "lokale Docker"-outpost zou anders op
  een willekeurige node terechtkomen.
- **Keepalived** gebruikt `authentik-ctl.sh check` met `weight 50` en zonder
  `nopreempt`. Een node waar `/-/health/ready/` antwoordt, krijgt 50 punten
  bij. Het VIP staat dus altijd op een gezonde node, bij voorkeur op de node
  met de laagste index. Omdat alle nodes dezelfde database delen, kost
  terugspringen niets. Als geen enkele node gezond is, blijft er toch een
  node met het VIP. Zo slaagt de uitrolcheck `vip_owned` voordat de database
  er is.
- **Sync** `Authentik-media` draait elke minuut en kopieert
  `/opt/cf-authentik/files/` van de VIP-node naar de andere nodes, **zonder
  --delete**. Een icoon dat binnenkwam terwijl een andere node het VIP had,
  verdwijnt dus nooit. Het nadeel: een gewist bestand blijft op de andere
  nodes staan.

## Markering in de database

`COMMENT ON DATABASE authentik` bevat `cf-authentik key=<sha256[:16] van de
sleutel> ready=<tijd> …`. `authentik-ctl.sh up` start Authentik alleen in
deze gevallen:

1. Er is een markering en de sleutel van de node heeft dezelfde vingerafdruk.
   Na een overname komt de sleutel met de sync (`files/.cf/secret_key`, 0600).
   Tot die er is, wacht de node.
2. Er is geen markering, de node is node 1, er draait geen oude Authentik en
   de database is helemaal leeg. Dat is een nieuw cluster: node 1 zet de
   markering met de sleutel van de template.

In alle andere gevallen wacht cf-authentik en probeert het elke 15 seconden
opnieuw. Er begint dus nooit een node een lege Authentik naast een overname,
en er draait nooit een node met een andere sleutel. Met
`authentik-ctl.sh vrijgeven` neem je een database in gebruik die je zelf
terugzette.

## Overnemen

Zo verloopt de route voor Jonas, met bestaande nodes:

1. Rol het cluster uit op de bestaande nodes. De node met de juiste
   configuratie komt eerst (index 1), daarna de kloon en een nieuwe CT.
   Gebruik hetzelfde VIP, dezelfde VRRP-id en hetzelfde VRRP-wachtwoord als
   nu.
2. Tot de overname telt de oude Authentik op node 1 als gezond en houdt hij
   het VIP. Een oude Authentik op een andere node telt niet: de kloon krijgt
   het VIP nooit.
3. Draai `authentik-ctl.sh overnemen` (Commando's) op node 1. Het commando:
   - zoekt de oude server, worker en PostgreSQL-container uit hetzelfde
     compose-project;
   - eist dezelfde jaar.maand als het cluster, tenzij je `--andere-versie`
     meegeeft;
   - eist een cluster-database zonder markering en zonder tabellen;
   - stopt de oude server en worker;
   - telt negen kerntabellen (gebruikers, groepen, applicaties, providers,
     proxy providers, OAuth2 providers, outposts, flows, certificaten);
   - maakt een `pg_dump --no-owner --no-privileges` in de oude container;
   - zet die dump in één transactie terug (`psql -1`, `ON_ERROR_STOP`) als
     eigenaar `authentik`;
   - telt opnieuw. Een verschil draait alles terug;
   - kopieert `/media`, `/data`, `/templates` en `/certs` uit de mounts;
   - schrijft de oude `AUTHENTIK_SECRET_KEY` naar `files/.cf/secret_key`.
     Die sleutel past niet in een parameter van ClusterForge, want base64 is
     te lang en bevat `+/=`;
   - zet de markering, stopt het oude project met `restart=no` (de data
     blijft staan) en start `cf-authentik`.
   Mislukt er een stap, dan wordt de database van het cluster weer leeg
   gemaakt (`DROP SCHEMA public CASCADE`), wordt de sleutel weggehaald en
   start de oude Authentik weer.
4. De andere nodes krijgen de sleutel met de sync en starten dan vanzelf. Een
   oude Authentik op die nodes stoppen ze zelf, met `restart=no`.

Teruggaan kan zo: `systemctl disable --now cf-authentik` op elke node, en dan
op node 1 `docker start` van het oude project. Commando's toont dat commando.
De dump blijft bewaard in `/var/lib/cf-authentik/overname/`.

## Geen agent- of migratiewijziging

Alles past binnen de bestaande template, database- en syncfuncties: Patroni
via de template (zoals bij het mediaplatform), beheerde syncs (fase 11) en
Commando's (fase 10). Het agentprotocol en het schema blijven zoals ze zijn.

## Tests

- `TestRenderAuthentik`: bestanden, keepalived met gewicht en zonder
  nopreempt, Patroni, database-instructies, sync en shellcheck.
- `TestAuthentikOvernemen`: echte PostgreSQL met een bron (de oude container,
  via een nep-docker) en een doel met een eigen eigenaar. De test loopt deze
  gevallen door: weigering bij een andere versie, terugdraaien bij een
  mislukt herstel (`file_fdw`), een geslaagde overname met 100 proxy
  providers, een tweede overname die weigert, node 2 die wacht op de sleutel
  en daarna zijn oude Authentik stopt en zelf start.
- `TestAuthentikNieuwCluster`: node 2 begint nooit, node 1 begint met een
  lege database, en daarna volgt node 2.
