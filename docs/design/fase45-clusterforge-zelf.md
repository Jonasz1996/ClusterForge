# Fase 45: ClusterForge zelf bijwerken en clusteren

Gevraagd 22/11: "moet vanuit webinterface zichzelf kunnen updaten" en "clusterforge zelf clusteren, heb daar 1 node van met alles op".

## Bijwerken (taak `clusterforge.update`)

- Eén server (docker compose): de agent op de host voert `internal/selfhost/scripts/compose-update.sh` uit als run. De server herstart halverwege; de run loopt door op de node en de taak leest na de herstart verder (noderun bewaart het run-id). Geen docker.sock in een container.
- Stappen: pull (ook `-scanner`), `pg_dump -Fc` + kopie `.env`/compose naar `/root/cf-backup`, compose uit git (checkout van de commit) of uit het image (`/usr/share/clusterforge/deploy/docker-compose.yml`), `CF_IMAGE`, `up -d`, wachten op healthy met het nieuwe image-id. Fout: bestanden terug; veranderde `goose_db_version` = database terug uit de dump; `up -d`; de taak faalt met de uitleg.
- Cluster (`CF_SELF_CLUSTER`): `clusterforge-ctl.sh rol` op elke node, `pull` op de nodes zonder VIP, `bijwerken` op de VIP-node (pg_dump via Patroni, marker zodat keepalived het VIP niet afgeeft, terugdraaien met `DROP OWNED` + `pg_restore`), `set-image` op de rest.
- Bevestigen met TOTP (of wachtwoord), alleen admin. Tag controleren tegen de registry (anoniem bearer-token).

## Template `clusterforge` 1.0.0

Actief/passief: de server (met NATS en de scanner) draait alleen op de VIP-node (`cf-clusterforge`, niet enabled, gestart door keepalived), host-netwerk. PostgreSQL met Patroni via `database:` (db-VIP, gebruiker/database clusterforge). VictoriaMetrics op elke node (`cf-clusterforge-vm`); de server schrijft ook naar de andere nodes (`CF_VICTORIAMETRICS_MIRRORS`, per spiegel een buffer). Hoofdsleutel, image en overgenomen instellingen gaan met een sync van de VIP-node naar de rest. Vóór de verhuizing houdt node 1 het VIP; een node zonder hoofdsleutel neemt het nooit. NATS is niet geclusterd: het certificaat staat in de database, dus de agents zien na een failover dezelfde server.

## Verhuizen (taak `clusterforge.move`, op de oude server)

1. Controle: rollen, agents te oud (< protocol 47) blokkeren tenzij "toch".
2. Dump op de host in de exportmap (bind `/var/lib/clusterforge-export` → `/var/lib/clusterforge/export:ro`), sha256 door de server.
3. `overnemen <url> <sleutel>` op de VIP-node: `/selfexport/<sleutel>/{meta,dump,metrics}` (sleutel 32 bytes, versleuteld in de params, alleen geldig zolang de taak loopt), `pg_restore`, lopende taken in de kopie op geannuleerd, adres voor agents = VIP, server starten, metrics importeren op elke node.
4. `ontvang` op de andere nodes.
5. Alle agents `agent.reconfigure` naar het VIP; wie na de terugvaltijd terug is op de oude server, telt als mislukt (dan blijft de oude server draaien).
6. Oude server en scanner stoppen (`restart=no`, over tien seconden).

Geen agentprotocol of migratie nodig.
