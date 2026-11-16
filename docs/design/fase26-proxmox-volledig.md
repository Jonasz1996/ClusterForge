# Fase 26: Proxmox volledig beheren vanuit ClusterForge

Gevraagd door Jonas op 2026-11-16: "Nu de agents daar lokaal commando's kunnen
uitvoeren (pct, qm set) kunnen we nog andere dingen managen en uitlezen: HA,
replicatie, tags, back-ups, nieuwe standalone node/host aanmaken. Volledig
vervangen van de Proxmox-GUI." Daarna aangevuld met: hardwarebewaking,
Proxmox-firewall, certificaten, host-updates, onderhoudsmodus, capaciteit,
opruimen, ISO's en templates, cloud-init, SDN, gebruikers en tokens, corosync,
takenlog en syslog, PBS-verificatie, passthrough (scannen, instellen, met een
herstartmelding) en een console.

## Eén deur naar de Proxmox-API

Fase 17 gaf de agent op een Proxmox-host getypte acties (`pct`, `qm`,
`pveam`). Voor alles hierboven zouden dat er honderden worden, met een
agentupdate per functie. Daarom krijgt de agent (protocol 35) één afgebakende
deur naar de API van Proxmox, via `pvesh` als root op de host:

| Wat | Hoe |
| --- | --- |
| Lezen | verzoek `get` op `cf.node.<id>.pve`: `pvesh get <pad> --output-format json`, hoogstens 900 kB |
| Veranderen | run `pve` met actie `api`: `pvesh create\|set\|delete <pad> --veld=waarde …`. Start Proxmox een taak (UPID), dan wacht de agent tot ze klaar is, toont haar log en meldt een mislukte taak als fout |
| Hardware | verzoek `hardware`: PCI met IOMMU-groepen, drivers en wat de host zelf gebruikt, USB, vfio, kernelregel nu en na een herstart, temperaturen, ECC |
| Passthrough | actie `passthrough`: IOMMU op de kernelregel (grub of `/etc/kernel/cmdline`), `clusterforge-vfio.conf` met vfio-ids en blacklist, modules, `update-initramfs` en `update-grub`/`proxmox-boot-tool refresh`; daarna "herstart nodig" |
| Rest | `volume_free` (`pvesm free`), `zpool_scrub`, `maintenance` (`ha-manager crm-command node-maintenance`) |

De agent controleert alleen de vorm (methode, pad zonder `..`, veldnamen). Wat
de GUI mag, staat op de server in `internal/pvehost/policy.go`: per methode en
pad de toegelaten velden, de titel van de taak, en wat Jonas moet overtikken.
Nooit: `args` en `hookscript` (code op de host) en `skiplock`. Root@pam
verwijderen kan niet.

Overtikken vragen onder meer: een host herstarten of uitzetten, alle gasten
stoppen, terugzetten over een bestaande gast, een back-up of storage
verwijderen, schijven wissen of er een pool op maken, netwerk- en
SDN-wijzigingen toepassen, een gast naar template omzetten.

Wachtwoorden en sleutels (PBS-wachtwoord, ACME-sleutels, cloud-init) gaan
versleuteld met de masterkey in de taak, verschijnen als `[geheim]` in de
uitvoer en het logboek, en de agent haalt ze na de run uit `spec.json`.

Een pad onder `/nodes/<host>` gaat naar de agent op die host; de rest naar een
agent in hetzelfde Proxmox-cluster (pvesh stuurt het zo nodig door). Antwoorden
blijven 4 seconden in een cache, want pvesh is traag.

## Datacenter

Een Proxmox-cluster (of losse host) heet in ClusterForge een datacenter,
herkend aan de clusternaam in de facts. `GET /pve/datacenters/{dc}/overview`
leest `/cluster/resources`, `/cluster/status`, de HA-status, corosync en per
host `/nodes/<host>/status`, en rekent uit:

- quorum, QDevice, stemmen en links per host, onderhoudsmodus;
- per host CPU, geheugen, vCPU's op threads, en het geheugen dat aan
  draaiende gasten is toegewezen;
- of alles wat draait nog past als de grootste host uitvalt (met 10% reserve);
- waarschuwingen: geen quorum, host offline, geheugen overcommit (>90%,
  >100%), meer dan 4 vCPU per thread, storage >85/95%, HA-fouten, twee hosts
  zonder QDevice.

## Mijlpalen

1. Fundament en Datacenter: protocol 35, policy, overzicht, Proxmox-taken met
   log en stoppen, onderhoudsmodus, host herstarten/uitzetten, alles
   starten/stoppen.
2. Gasten: detailpagina, VM's en CT's los aanmaken (met cloud-init),
   hardware en opties, klonen, template, tags, passthrough toewijzen en
   instellen met herstartmelding.
3. Back-ups: vzdump-jobs, nu een back-up, inhoud per storage, terugzetten,
   PBS-verificatie en een terugzettest.
4. HA en replicatie, clusterbewuste host-updates (één host tegelijk, eerst
   leeg via migratie).
5. Storage, ISO's en templates, opruimen, hardwarebewaking.
6. Netwerk en SDN, Proxmox-firewall, ACME, gebruikers en tokens, syslog.
7. Console (noVNC/xterm) via de agent.

## Terugzettest (M3)

`POST /pve/datacenters/{dc}/restore-test` zet een back-up terug als nieuwe gast
met een vrij ID (`unique=1`), zet de netwerkkaarten los (VM: `link_down=1`, CT:
weg), haalt doorgegeven hardware weg (`hostpciN`, `usbN`, `devN`), start hem en
wacht tot hij draait; een VM met guest agent tot `agent/info` antwoordt. Daarna
stopt en verwijdert ClusterForge hem altijd, maar alleen als de beschrijving de
markering `clusterforge-terugzettest` draagt. De stappen staan in de taak; de
pagina Back-ups → Terugzettests toont de uitkomsten. `script` (vzdump-hook) is
voor de GUI verboden, net als `args` en `hookscript`.
