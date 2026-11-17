# Fase 37: TrueNAS volledig beheren via de agent

Gevraagd 2026-11-17 12:11 UTC: "volgende fase is agent bouwen truenas en integratie".
Gestapeld op fase 36 (PR #143, branch `claude/project-thread-ndllsw`).
Protocol **53**, migraties vanaf **00117** (alleen als nodig).

## Uitgangspunt

- **TrueNAS SCALE / Community Edition** (Debian-basis, 24.10 Electric Eel en
  25.x). CORE (FreeBSD) volgt alleen als Jonas het vraagt: de FreeBSD-build
  van de agent bestaat al (fase 34), maar de bediening is anders.
- Zoals PBS (fase 32) en OPNsense (fase 34): de agent geeft de API door,
  de server beslist wat mag. Op TrueNAS is dat de middleware:
  `midclt call <methode> <json-args>` over de lokale socket, als root,
  zonder API-sleutel. Taken (`-job`) wacht de agent af met voortgang.
- Eén keer de agent installeren met het token (Nodes → Agent installeren);
  al de rest via de GUI.

## Installeren op TrueNAS

`/usr` is op SCALE alleen-lezen en een update maakt een nieuwe
boot-omgeving, zodat `/etc` en systemd-units verdwijnen. Daarom zet
install.sh op TrueNAS (herkend aan `/usr/bin/midclt` + `/etc/version`):

- de agent en zijn aanmelding in `/data/clusterforge` (blijft bij updates),
- `/etc/clusterforge` als koppeling daarheen en de systemd-unit,
- een **Init/Shutdown-script** (POSTINIT) via `initshutdownscript.create`
  dat na elke boot of update de koppeling en de unit terugzet en de agent
  start. Zo overleeft de agent een TrueNAS-update zonder handwerk.

## Protocol 53

- facts `truenas`: versie, hostnaam, pools (naam, status), apps-pool.
- leesverzoek `truenas_get` (onderwerp pve): de agent voert alleen
  leesmethodes uit (`*.query`, `*.get_instance`, `*.config` en een vaste
  lijst zoals `system.info`, `disk.temperatures`, `update.check_available`).
- run-actie `truenas_call`: elke methode, met `-job` en voortgang in het
  taaklog. Welke methodes en velden mogen, staat in `internal/truenas/policy.go`.

## Mijlpalen (meest nuttige eerst, na elke mijlpaal één image)

1. **M1 Agent + overzicht**: herkennen, installeren met blijvende service,
   menu TrueNAS → Overzicht: systeem, pools (gezondheid, gebruik, scrub),
   schijven met SMART en temperatuur, meldingen (alerts) met
   wegklikken, en lopende taken. Scrub starten.
2. **M2 Datasets en snapshots**: datasets met gebruik/quota/compressie,
   snapshot maken/verwijderen/terugrollen (overtikken), snapshot-taken,
   replicatie- en cloud-sync-taken met "nu uitvoeren" en laatste resultaat.
3. **M3 Shares, apps en diensten**: SMB/NFS-shares lezen en aan/uit,
   apps (status, starten/stoppen/herstarten, bijwerken), diensten.
4. **M4 Updates en aandacht**: updates controleren, downloaden en
   toepassen (eerst config-back-up in ClusterForge, prod vraagt TOTP),
   Aandacht nodig (degraded pool, SMART-fout, volle pool, mislukte
   replicatie, oude snapshot) en een score per TrueNAS.

## Vragen aan Jonas (bouwen gaat door met het voorstel)

1. SCALE of CORE, en welke versie? Voorstel: SCALE 24.10/25.x.
2. Hoeveel TrueNAS-machines, en is er replicatie tussen twee?
3. Draaien er apps op TrueNAS die ClusterForge moet beheren, of alleen opslag?

## Stand van de bouw

- (wordt bijgewerkt per mijlpaal)
