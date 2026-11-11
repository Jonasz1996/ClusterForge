# Fase 11: Nginx Proxy Manager als HA-cluster

Gevraagd door Jonas op 2026-11-10: "zou je npm kunnen clusteren? dit werkt
wel met sqlite, had vroeger al geprobeerd maar door sync werd db steeds read
only. Config blijft normaal hetzelfde tenzij ik manueel aanpassingen doe; 1
sync om de zoveel min / 1 u is goed."

NPM = Nginx Proxy Manager (`jc21/nginx-proxy-manager`, Docker), met zijn
configuratie in SQLite (`/data/database.sqlite`), de rest van `/data`
(gegenereerde nginx-configuratie, `keys.json`, eigen certificaten, htpasswd)
en de Let's Encrypt-certificaten in `/etc/letsencrypt`.

## Waarom het vroeger read-only werd

Een gewone bestandssync (rsync, lsyncd, Syncthing) van `/data` tussen twee
draaiende NPM's gaat op drie manieren mis:

1. **Kopie van een open database.** rsync leest `database.sqlite` terwijl NPM
   erin schrijft; de kopie is een mengsel van voor en na. Met WAL staat het
   nieuwste deel zelfs nog in `database.sqlite-wal`, dat apart (en op een
   ander moment) meekomt.
2. **Database vervangen onder een draaiende NPM.** rsync zet een nieuw
   bestand (nieuwe inode) op de plaats van het open bestand. De NPM op het
   doel houdt het oude open, met een `-wal`/`-journal` die niet meer bij het
   bestand past: SQLite opent dan alleen-lezen ("attempt to write a readonly
   database") of ziet het als beschadigd.
3. **Twee schrijvers.** Draait NPM op beide nodes, dan schrijven ze allebei
   (certificaatvernieuwing, laatste login); wie het laatst synct, wint.

## Ontwerp

Eén regel lost alle drie op: **NPM draait alleen op de node met het VIP, en
een node die een kopie ontvangt, draait nooit NPM.**

| Stap | Waar | Hoe |
| --- | --- | --- |
| VIP | keepalived op elke node | `nopreempt`, failback uit; `npm-ctl.sh check` geeft het VIP af als Docker of NPM hier faalt, als NPM na de opstarttijd (180 s) 3 checks op rij niet draait of de API op de beheerpoort niet antwoordt, of als er naast de configuratie nog een andere NPM-container draait; is NPM gestopt, dan start de VIP-node het opnieuw; een node zonder VIP stopt een NPM die er nog draait |
| NPM starten/stoppen | `cf-npm.service` (niet enabled) | keepalived `notify_master` start, `notify_backup`/`fault` stopt; `restart: "no"` in compose, zodat Docker het nooit zelf start |
| Kopie maken | VIP-node, `npm-ctl.sh snapshot` | `sqlite3 .backup` (online backup-API, klopt terwijl NPM schrijft), `PRAGMA integrity_check`, de rest van `/data` zonder `database.sqlite*` en `logs/`, `letsencrypt/`, dan `MANIFEST` (tijd, node, image) en `SHA256SUMS` over alles; atomisch naar `/var/lib/cf-npm/outgoing` |
| Kopie versturen | ClusterForge-sync (fase 6) | beheerde cronjob `NPM-configuratie`, soort sync, vanaf het VIP, `--delete`, naar `/var/lib/cf-npm/incoming` op de andere nodes; elke 1–60 min (param `sync_minutes`, standaard 5) |
| Kopie gebruiken | nieuwe VIP-node, `npm-ctl.sh up` (ExecStart) | alleen als `created` nieuwer is dan wat er staat (`/var/lib/cf-npm/stamp`), `sha256sum -c` klopt en `integrity_check` ok is: eerst de oude `-wal/-shm/-journal` weg, dan rsync `--checksum` naar `/opt/cf-npm`; anders start NPM met wat er staat (met een waarschuwing in de journal) |

Zo is de kopie altijd consistent, komt een ontvangen kopie er alleen in
terwijl NPM op die node stilstaat, en heeft een node nooit twee bronnen.

### Waarom een voorbereiding in de sync

De sync van fase 6 kopieert een map. De kopie moet gemaakt worden vlak
vóór rsync, op dezelfde node, en rsync mag niet draaien als ze faalde.
Daarom krijgt een sync een **voorbereiding** (`SyncSpec.Before`, agentprotocol
27): een commando dat de bron eerst draait; faalt het, dan kopieert de sync
niets en blijft de vorige kopie op de andere nodes staan. De proefrun draait
de voorbereiding ook. Een agent met protocol < 27 kent het veld niet en zou
een oude of lege map kopiëren: de server zet zo'n sync niet op die node, en
het plan van de sync zegt "agent te oud" (Nodes → Agents bijwerken).

De voorbereiding is ook in de GUI in te vullen bij elke sync (Cronjobs →
Nieuwe cronjob → Sync → "Voorbereiding op de bron"), bijvoorbeeld voor een
andere app met SQLite.

### Syncs uit een template

Een template kan nu `syncs:` hebben. Na een gelukte uitrol (`deploy`) of
wijziging (`cluster.apply`) zet ClusterForge elke sync als beheerde cronjob
in het cluster: nieuw, of de job met dezelfde naam aangepast. Zette Jonas de
job uit, dan blijft hij uit; herhalingen blijven zoals hij ze zette. Lukt
het niet (bv. nog maar één node met een agent), dan staat
`cluster.sync_not_added` met de reden in het logboek.

### Niet nieuwer, niet terug in de tijd

`/var/lib/cf-npm/stamp` zegt tot wanneer de data op een node zeker actueel
is: na elke kopie (VIP-node), na het overzetten van een kopie, en als NPM
stopt terwijl het draaide. Een ontvangen kopie die niet nieuwer is, komt er
niet in. Zo zet een node die even weg was en het VIP terugkrijgt, nooit een
oudere kopie over wat hij zelf nog nieuwer had. Na een crash zonder stop
geldt de laatste kopie die hij zelf maakte. De klokken van de nodes moeten
gelijk lopen (CT's delen de klok van de Proxmox-host).

### Split-brain

Hebben twee nodes tegelijk het VIP, dan draait NPM op beide en maken ze
allebei een kopie; een doel met het VIP weigert altijd (`sync-receive`), dus
geen van beide overschrijft de ander. Na het herstel stopt de verliezer NPM
(stamp = dat moment) en krijgt hij bij de volgende sync de kopie van de
winnaar.

Horen de nodes elkaars VRRP niet (bv. een firewall die IP-protocol 112
tegenhoudt), dan blijven ze allebei master. Daarom kijkt `npm-ctl.sh check`
op de VIP-node met `arping -D` wie het VIP nog beantwoordt: is dat een node
met een hogere prioriteit (lagere index), dan faalt de check (FAULT, NPM
stopt) tot die node het VIP niet meer heeft (`/var/lib/cf-npm/yielded`).
En `cf-npm.service` heeft `BindsTo=keepalived.service`: stopt keepalived, dan
stopt NPM ook.

### Hoe oud is de data na een failover?

De nieuwe VIP-node start met de laatste kopie: wat op de vorige VIP-node
veranderde na zijn laatste sync, staat er niet in (hoogstens `sync_minutes`).
Wie veel aanpast, zet `sync_minutes` op 1 of 2. `npm-ctl.sh status` toont
tot wanneer de data op een node actueel is.

### Certificaten

Let's Encrypt vernieuwt alleen op de VIP-node (daar draait NPM, en daar komt
de HTTP-01-challenge via het VIP binnen). De vernieuwde certificaten gaan
met de volgende kopie mee; de symlinks in `live/` blijven symlinks.

## Template `nginx-proxy-manager` 1.0.0

- Rol `node`, 2–5 nodes (standaard 2), 1 vCPU, 1 GB, 10 GB; ook op bestaande
  nodes of nieuwe CT's (Cluster uitrollen → Waar).
- Pakketten: keepalived, sqlite3, rsync, openssh-server, curl, Docker.
- `/opt/cf-npm/compose.yaml` (`container_name: cf-npm`, poorten 80, 443 en
  de beheerpoort → 81), `/usr/local/lib/clusterforge/npm-ctl.sh`,
  `cf-npm.service`, keepalived.conf.
- Parameters: `vip`, `nodes`, `npm_image` (standaard
  `jc21/nginx-proxy-manager:latest`; een andere waarde herstart NPM op de
  VIP-node), `sync_minutes` (1, 2, 5, 10, 15, 30, 60), `admin_port` (81),
  `timezone`, `vrid`, `auth_pass`.
- Controles na de uitrol: VIP aanwezig, beheerpoort op het VIP antwoordt 200.
- Sync `NPM-configuratie` met voorbereiding `npm-ctl.sh snapshot`, alert bij
  een mislukte run.

### Een bestaande NPM overnemen

Op de VIP-node, via Commando's (fase 10) of een shell:

```sh
/usr/local/lib/clusterforge/npm-ctl.sh overnemen /pad/naar/npm   # map met data/ en letsencrypt/
```

Zonder map neemt het script de NPM over die op deze machine draait, met de
mappen uit zijn mounts. Zolang zo'n andere NPM draait, start cf-npm daar niet:
de oude NPM bedient het VIP tot de overname, zonder onderbreking.
Draait de oude NPM op dezelfde machine, dan stopt het script hem eerst
(`restart=no`, terug te starten met `docker start`). Database met `sqlite3
.backup`, de rest met rsync; daarna start NPM van het cluster en gaat de
eerste kopie binnen `sync_minutes` naar de andere nodes. Staat de oude NPM
op een andere machine, kopieer dan eerst zijn map naar de VIP-node (met NPM
daar gestopt).

### Nakijken

- `npm-ctl.sh status` op een node: VIP hier of niet, cf-npm, tot wanneer de
  data actueel is, de verstuurde en de ontvangen kopie.
- Cronjobs → NPM-configuratie: elke run met de uitvoer van de voorbereiding
  en per doel bestanden/bytes; een mislukte run opent een incident.
- Failovertest (fase 2): keepalived stoppen op de VIP-node; NPM start op de
  andere node met de laatste kopie.

## Mijlpalen (in deze PR)

1. Agent: `SyncSpec.Before`, protocol 27, proefrun met voorbereiding.
2. Server: kolom `cron_jobs.sync_before` (migratie 00064), API-veld
   `sync.before`, veld in het formulier en op de jobpagina; sync niet op
   agents < 27; plan meldt het.
3. Templates: `syncs:` met `RenderSyncs`; na uitrol/wijziging
   `cronjobs.EnsureTemplateSyncs`.
4. Template `nginx-proxy-manager` 1.0.0 met `npm-ctl.sh`; test met echte
   sqlite3 en rsync (kopie, failover, oude `-wal`, halve kopie, oudere kopie).
