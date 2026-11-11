# Fase 12: Home Assistant als HA-cluster

Gevraagd 2026-11-11: een Home Assistant-cluster op drie nieuwe nodes, elk met
een lid van een lokaal databasecluster. Zelfde aanpak als Zabbix (Galera op
de nodes, VIP volgt de database) en Nginx Proxy Manager (app alleen op de
VIP-node, consistente kopie van de configuratie met een beheerde sync).

## Opbouw

- Template `home-assistant` 1.0.0, 3 of 5 nodes, één rol `node`.
- MariaDB Galera op alle nodes (`database: engine: mariadb`), na de uitrol
  vanzelf, met de database `homeassistant` (utf8mb4) en de gebruiker
  `homeassistant` (database-instructies, zoals een recept; kaart Verbinding).
- Home Assistant (Docker, `network_mode: host`) draait alleen op de node met
  het VIP (`cf-homeassistant.service`, niet enabled, gestart door keepalived).
- De recorder schrijft naar Galera via het VIP: de geschiedenis is er na een
  failover meteen. `/config/cf-recorder.yaml` en `/config/cf-http.yaml` zijn
  per node read-only gemount uit `/etc/cf-homeassistant/`; configuration.yaml
  neemt ze op met `recorder: !include cf-recorder.yaml` en
  `http: !include cf-http.yaml` (ha-ctl.sh zet die regels erbij als ze
  ontbreken; een eigen `recorder:` blijft staan, met een waarschuwing).
- keepalived (`ha-ctl.sh check`): VIP alleen op een node met Docker, een niet
  gefaalde Home Assistant, en (zodra Galera er is) een MariaDB die Synced is
  in een Primary component (`cf-agent check db mariadb`). Split-brain: een
  node geeft het VIP af als een node met lagere index het ook heeft (zoals
  NPM, 65bbd9c).
- `ha-ctl.sh up` wacht tot de database via het VIP bereikbaar is; zonder
  database zou de recorder uitvallen.

## Sync van /config

`ha-ctl.sh snapshot` op de VIP-node (voorbereiding van de sync
"Home Assistant-configuratie"): rsync van /config zonder logs,
`home-assistant_v2.db`, `backups/`, `tts/`, `deps/`, `.cache/` en de
ClusterForge-bestanden; elke andere `*.db` (zoals `zigbee.db` van ZHA) met
`sqlite3 .backup` + `integrity_check`; MANIFEST en SHA256SUMS. `.storage` en de
YAML schrijft Home Assistant atomair, dus rsync leest altijd hele bestanden.
`up` zet een nieuwere, volledige kopie op zijn plaats vóór de start (zelfde
regels als NPM: stamp, sha256, integrity_check, -wal/-shm weg). Alleen node 1
mag leeg starten.

## Overnemen

`ha-ctl.sh overnemen [<map>]` op de VIP-node: een bestaande /config (of de
Home Assistant in Docker die daar draait) naar het cluster. De oude
geschiedenis (`home-assistant_v2.db`) gaat niet mee.

## Grenzen

- USB-sticks (Zigbee/Z-Wave/Bluetooth) verhuizen niet mee; gebruik een
  netwerkcoördinator of MQTT/Zigbee2MQTT elders.
- Back-ups van Home Assistant zelf (`/config/backups`) blijven per node.
- VRRP (IP-protocol 112, unicast) moet tussen de nodes door de firewall.
