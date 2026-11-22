# Fase 23: logs bekijken, filteren en vergelijken

Gevraagd 2026-11-16: "Logs agents lezen /var/log en docker logs uit, in de GUI
kan ik ze parsen en vergelijken." Niets extra op de nodes: alleen de agent.

## Keuzes

- **Lezen op vraag, niets bewaren.** De server vraagt de agent (subject
  `cf.node.<id>.logs`, protocol 34) om één bron met een filter; de logs komen
  niet in PostgreSQL of VictoriaMetrics. Zo blijft de server klein en is er
  geen retentie te beheren. Alleen *weergaven* (bronnen + filter) staan in de
  database (migratie 00071 `log_views`).
- **Bronnen**: bestand onder `/var/log` (geen symlinks, geen binaire bestanden
  zoals wtmp, `.gz` gestreamd), `journalctl -o json -r` (alles of `-u unit`),
  `docker logs --timestamps`. Units en containernamen worden gevalideerd zodat
  ze nooit een optie worden.
- **Filteren op de node.** Zoekterm (RE2), weglaten, niveaus en tijdvak past de
  agent toe; hij leest een bestand van achter naar voor in groeiende vensters
  (1 MB ×4 tot 64 MB) tot er genoeg passende regels zijn of het begin van het
  tijdvak bereikt is. Journaal en containers: hoogstens 200 000 regels
  bekeken. Antwoord ≤ 640 KB (NATS-grens 1 MB), ≤ 5000 regels per bron.
- **Ontleden op de node**: tijd, niveau, programma; vervolgregels zonder tijd
  horen bij de regel ervoor (stacktraces). Geheimen gemaskeerd met
  `protocol.MaskSecrets`.
- **Vergelijken op de server en in de browser**: de server leest tot 12
  bronnen tegelijk en legt ze op één tijdlijn (merge op tijd); een bron die
  faalt staat met zijn fout in het antwoord. De browser maakt patronen
  (getallen/IP's/id's weg), velden (key=value, JSON, eigen benoemde groepen),
  het histogram en de kolommen naast elkaar.
- **Alleen beheerders**: ook gemaskeerd staan er vaak gevoelige gegevens in
  logs.

## Mijlpalen

- M1 agent: `internal/agent/logs.go`, `logparse.go`, protocol `pkg/protocol/logs.go`.
- M2 server: `internal/logs`, API `/logs/nodes`, `/logs/query`, `/logs/views`,
  `/nodes/{id}/log-sources`, migratie 00071.
- M3 web: pagina Logs, knoppen op node en containerlogs.
