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

## HA, replicatie en host-updates (M4)

- **HA** (`/proxmox/ha`): status (quorum, manager, LRM per host), gasten onder HA met verhuizen/verplaatsen/stoppen, en regels (PVE 9 `/cluster/ha/rules`: node- en resource-affiniteit). Op PVE 8 valt de pagina terug op HA-groepen.
- **Replicatie** (`/proxmox/replicatie`): jobs uit `/cluster/replication`, status per host uit `/nodes/{node}/replication`, nu synchroniseren, aan/uit, wijzigen. Verwijderen vraagt de job-id getypt, omdat Proxmox dan de kopie op de doelhost wist. Id is `<vmid>-<n>` met het eerste vrije n.
- **Host-updates** (`/proxmox/host-updates`, `POST /pve/datacenters/{dc}/host-update`): per host wachtende pakketten (`apt/update`) en of een herstart nodig is. Een ronde werkt hosts na elkaar bij: onderhoudsmodus aan, gasten verhuizen naar de host met het meeste vrije geheugen (HA-gasten doet Proxmox zelf), wachten tot de host leeg is, apt full-upgrade via de agent, herstarten (nodig/altijd/nooit), wachten tot de host terug is, onderhoudsmodus uit, gasten terug. Gasten die niet kunnen verhuizen (lokale resources) laten de ronde stoppen, tenzij "stilleggen" aan staat. `dry_run` geeft het plan zonder iets te veranderen; starten vraagt de datacenternaam getypt.
- Geen agentupdate (generieke pvesh, protocol 35) en geen migratie.

## Opslag, schijven, ISO's en opruimen (M5)

- Pagina `/proxmox/opslag` met vier tabbladen.
- **Opslag**: `/storage` met gebruik per host uit `/cluster/resources`; toevoegen/wijzigen voor map, NFS, SMB/CIFS, PBS, ZFS, LVM-thin en LVM (wachtwoorden gaan als geheim door de policy), aan/uit, weghalen met getypte naam.
- **Schijven**: per host `disks/list` (SMART-uitslag, slijtage), SMART-details, ZFS-pools met vdev-boom, laatste scrub en scrub starten/stoppen (bestaande agentactie zpool_scrub), LVM-thinpools met metadata. Nieuwe ZFS/LVM/LVM-thin/map op lege schijven, GPT en wissen: alle drie met getypte bevestiging (de policy eist die ook). Een kaart "Schijven vragen aandacht" (SMART niet in orde, SSD onder 10% levensduur, ZFS niet ONLINE) staat ook op de Datacenter-pagina. Temperaturen en ECC staan al op Hardware (M2).
- **ISO's en templates**: inhoud van alle opslag met iso/vztmpl, downloaden van URL met checksum, CT-template uit de Proxmox-lijst (`aplinfo`), weghalen met getypt volume-id.
- **Opruimen**: schijven van een ID dat niet bestaat, `unusedN`-schijven (link naar Hardware van de gast), ISO's die in geen cd-station zitten (alleen als elke gastconfig gelezen is) en achtergebleven terugzettests.
- Geen agentupdate en geen migratie.

## Netwerk, firewall, certificaten, toegang en syslog (M6)

- **Netwerk** (`/proxmox/netwerk`): interfaces per host (bridge, bond, VLAN; adres, gateway, MTU, VLAN-bewust). Wijzigingen staan klaar tot "Toepassen" (getypte hostnaam, ifreload) of "Vergeten". Adressen op .1-.3 van een /24 weigert de GUI (gateway en CARP-firewalls). Tabblad SDN: zones (VLAN, simple, QinQ), VNets, subnets en "SDN toepassen" (getypt "sdn").
- **Proxmox-firewall** (`/proxmox/firewall`): datacenter, host of gast; aan/uit, standaardbeleid, regels (dienst of protocol/poort, van/naar, interface, loggen, volgorde), aliassen en IP-sets. De datacenterfirewall aanzetten vraagt "firewall" (ook in de policy).
- **Certificaten** (`/proxmox/certificaten`): certificaat per host met vervaldatum, ACME-domeinen per host (HTTP of DNS-plugin), nu bestellen, eigen PEM-certificaat (sleutel als geheim), ACME-accounts en DNS-plugins (gegevens als geheim).
- **Gebruikers en tokens** (`/proxmox/toegang`, alleen beheerders): gebruikers, groepen, rechten (ACL), wachtwoord, API-tokens intrekken.
- **API-token maken**: `POST /pve/datacenters/{dc}/tokens` loopt synchroon zonder taak. De agent krijgt `PVEAPICall.Quiet` (protocol 36): het antwoord van Proxmox komt niet in de uitvoer, alleen in `RunStatus.PVE.Data`; de server gebruikt een run in het geheugen (geen taak, geen stap in de database) en geeft de waarde één keer terug. Een token via de gewone aanroep kan niet (geen policyregel). Vraagt minstens één host met agentprotocol 36.
- **Syslog** (`/proxmox/syslog`): `journal` van een host, filter op tekst of fouten/waarschuwingen, live volgen.
- Agentprotocol 36 (alleen voor tokens); geen migratie.

## Console (M7)

- Knop **Console** op een draaiende gast (VM: noVNC-scherm, CT: rootshell met `pct enter`), **Seriële console** bij een VM met `serial0` (`qm terminal`, Ctrl+O verlaat), en **Shell** per host op de Datacenter-pagina (`login -f root`). De console opent in een eigen tabblad (`/proxmox/console`), met Ctrl+Alt+Del, volledig scherm en opnieuw verbinden.
- Weg: browser → websocket `/api/v1/pve/console/{sessie}` → server → NATS → agent op de host waar de gast draait → programma. Openen is eerst een gewone aanroep (`POST /pve/datacenters/{dc}/console`), zodat "VM draait niet" een gewone foutmelding is; de browser heeft daarna 30 seconden voor de websocket.
- VNC: de agent start `qm vncproxy` met een eenmalig wachtwoord van 8 tekens (`LC_PVE_TICKET`); QEMU laat het na 30 seconden vervallen. Terminals lopen in een pseudo-terminal die meegroeit met het venster.
- Protocol 37 (`ConsoleSince`): subject `cf.node.<id>.console` om te openen, daarna frames op `…console.<sessie>.in` en `.out` (data, resize, ping, close). De agent sluit een sessie na 60 seconden zonder bericht en houdt er hoogstens 8 tegelijk open.
- Veiligheid: alleen admins; alleen wie de sessie opende, en maar één venster; de websocket weigert een andere Origin (geen misbruik van de cookie door een vreemde site). Het openen staat in het auditlog (`pvehost.console_opened`), het verkeer en het wachtwoord nergens.
- Achter een reverse proxy moet websocket-ondersteuning aan staan (in NPM: "Websockets Support").
