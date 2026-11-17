# Fase 29: het eigen mediaplatform beter beheren

Gevraagd 2026-11-17. Het platform
(github.com/Jonasz1996/alles-in-1-file-office-media-platform) draait sinds
fase PR #111 als cluster met template `media-platform` (SeaweedFS, Patroni,
keepalived; platform alleen op de node met het VIP). Deze fase maakt het
beheer ervan volledig via ClusterForge, zoals bij homepage, VaultX en
cssthema.

## M1: template (geen agentupdate)

- **Versie** (`platform_ref`, label "Versie"): branch, tag of commit
  (7 tot 40 hex). Wijzigen → Versie bouwt de nieuwe versie op elke node
  (`platform-src.sh`), en de node met het VIP herstart het platform alleen
  als er iets veranderde, **na een back-up van de database**
  (`platform-ctl.sh db-backup`, pg_dump -Fc via het db-VIP naar
  `/var/backups/cf-platform`, vijf nieuwste). De app voert zijn migraties
  zelf uit bij de start. Nooit `install.sh` op een clusternode.
- **Gezichten en zoeken op beeld** (platform 0.8.1, `compose.analyse.yaml`):
  params `analyse` en `beeld` (standaard aan). `platform-ctl.sh` neemt
  `compose.analyse.yaml` plus `/etc/clusterforge/platform-analyse.yaml`
  (image `cf-platform:local`, geen herstart door Docker) alleen mee als de
  versie het bestand heeft en `ANALYSE=aan`. `ANALYSE_WORKER_KEY` is
  afgeleid van de sleutel van media (sha256), dus op elke node gelijk zonder
  nieuw geheim. Modellen (`models.lock`, sha256 en grootte nagekeken zoals
  `install.sh`) op elke node in `/opt/cf-platform/modellen`; zoeken op beeld
  (±1,6 GB) haalt en zet de node op de achtergrond om (unit
  `cf-platform-beeld`, dienst `analyse-omzetten` van de versie zelf). De
  sleutels per eigenaar staan buiten de gedeelde data in
  `/var/lib/cf-platform/sleutels/gezichten`; sync `platform-sleutels` zet ze
  elke minuut van de VIP-node op de andere nodes.
- **Plaatsnamen**: param `places`; `platform-ctl.sh geo` haalt de bronnen
  uit `geo_bronnen` van `install.sh` van dezelfde versie, nagekeken op
  grootte en sha256, als uid 10001 naar `DATA_ROOT/geo` op de gedeelde
  bestanden. `up` doet het vóór de start als ze ontbreken.
- **Replicatie**: verandert `replication` (bv. 001 → 002), dan zet de
  leider van de masters de bestaande volumes om
  (`volume.configure.replication` + `volume.fix.replication`) op de
  achtergrond (unit `cf-platform-replicatie`). Waarom 170 GB en niet 230 GB
  per node: met 001 staat elk volume op twee van de drie nodes.
- **Stand**: `platform-ctl.sh status` (en timer `cf-platform-status`, elke
  minuut) schrijft `/var/lib/clusterforge/app-status/media-platform.json`.
  Elke node meldt mount, SeaweedFS, schijfruimte, media op de node, modellen;
  **alleen de node met het VIP** controleert het platform zelf (unit,
  containers gezond, `/health`, plaatsnamen, analyse). Zo meldt nooit meer
  dan één node een probleem van de app.
- Back-ups van de database: sync `platform-backups` (04:40) naar de andere
  nodes.

## M2: agent (protocol 48) en GUI

- Agent: leest `/var/lib/clusterforge/app-status/*.json` (hoogstens 8
  bestanden van 64 KB) en stuurt ze als `facts.apps`. Op de leider van de
  SeaweedFS-masters ook `/vol/status`: aantal volumes, volumes met te weinig
  kopieën en de nettogrootte. `cf-platform`, `cf-platform-mount` en
  `cf-platform-status` komen bij de gevolgde units.
- Server: kaart **Applicatie** op de clusterpagina (rol, versie, controles
  en waarden per node; media per node als balk naast de schijf; replicatie)
  en **Aandacht nodig**: een mislukte controle (rood of oranje), het
  platform actief op geen of op meer dan één node, verschillende versies op
  de nodes, volumes zonder genoeg kopieën, of een stand ouder dan 10 minuten.
