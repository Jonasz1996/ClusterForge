# Fase 21: Docker-stacks

Doel: Docker-hosts met een twintigtal losse containers en alleen keepalived
(Docker, DockerDubbel1-2, DockerTrippel1-3) beheren via de agents: één
gewenste staat per cluster, gelijk op alle nodes, starten/stoppen/bijwerken
vanuit de GUI, gezondheid per container, keepalived gekoppeld aan de
containers en de data mee met een sync. Bestaande hosts en containers worden
overgenomen zonder dataverlies.

## Model

- `docker_stacks` (migratie 00070): per cluster en projectnaam de map, de
  compose-bestanden, de bestanden versleuteld (secrets.Box, aad
  `docker-stack:<id>`), plaatsing `alle | vip | node`, `desired`
  (running/stopped), `critical`, sync-mappen en -schema, revisie.
- `docker_stack_revisions`: elke versie van de bestanden.
- `docker_stack_nodes`: per node de toegepaste revisie en de laatste fout.
- Een cluster zonder VIP of met één node behandelt `vip` als `alle`.

## Agent (protocol 33)

- Subject `stacks`: `list` (containers met staat, health, herstarts, poorten,
  mounts; Compose-projecten; bestanden van een project) en `logs`
  (gemaskeerd, begrensd).
- Run `stack` (via `cf-agent run-exec`): schrijft
  `/etc/clusterforge/stacks.list` en per stack de bestanden (oude in de
  run-map), `config -q`, dan `up -d --remove-orphans` / `stop` / `restart` /
  `down` / `pull`. Na `up` wacht de agent tot alles draait en gezond is;
  nieuwe herstarts tellen als fout en dan gaan de oude bestanden terug.
- Sync met voorbereiding `cf-agent stack-pause <projecten>`: eerst een live
  rsync, dan pauze, tweede rsync, unpause (hoogstens 10 minuten pauze).

## Server

- `internal/stacks`: overzicht (agents parallel gevraagd), overnemen,
  bewaren (geheimen terug uit de plaatshouders), acties als taak per cluster
  (`stacks.apply`), stap per node met de VIP-node als laatste; een fout op
  een andere node stopt de taak vóór de VIP-node. Daarna de syncs
  (`cf-stack-<naam>`, via cronjobs.EnsureTemplateSyncs).
- API: `/clusters/{id}/stacks`, `/stacks/import`, `/stacks/jobs`,
  `/stacks/logs`, `/stacks/{id}` (GET/PUT/DELETE). Prod vraagt de slug.

## Template docker-ha 1.0.0

- Docker alleen installeren als het ontbreekt (docker-ce blijft).
- Met VIP: keepalived (nopreempt, unicast, geen failback) met
  `docker-ha.sh check`: Docker moet antwoorden, en op de VIP-node moeten de
  kritieke stacks gezond draaien (gepauzeerd telt als goed) na een
  opstarttijd. `notify_master/backup/fault` starten of stoppen de stacks met
  plaatsing vip op de achtergrond (flock, volgens wie het VIP nu heeft).
- Zonder VIP: geen keepalived.
- De oude `sync_docker.sh` in de crontab van root gaat in commentaar.

## Overname van de bestaande hosts

1. Agent installeren op elke host (Nodes → Agent installeren).
2. Cluster uitrollen → Bestaande nodes, template docker-ha, VIP in het
   subnet van de nodes (het oude VIP mag blijven als het daar ligt).
3. Containers → Overnemen per project, bestanden nakijken, Toepassen.
