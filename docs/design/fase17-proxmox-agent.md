# Fase 17: agent op de Proxmox-hosts en volledig automatisch uitrollen

Gevraagd door Jonas op 2026-11-15: "een agent die proxmox beheert vanuit
clusterforge, pct / qm set start stop enzo, en dit verwerken in de gui", en
"bij uitrollen van een cluster nieuwe containers kunnen aanmaken: cpu, ram,
disk, id, ip, welke node meegeven, ssh key tussen die node en proxmox, die
dan automatisch de agent enrolt, toevoegt en de cluster opzet zoals nu, maar
dan volledig automatisch. Alles vanuit één scherm Cluster uitrollen."

## Waarom een agent op de host

De Proxmox-API (fase 1) kan VM's en CT's klonen, starten en verhuizen, maar
niet *in* een CT kijken: een CT heeft geen QEMU guest agent. Daarom vroeg een
uitrol met CT's tot nu toe een CT-template die Jonas zelf met
`ct-template.sh` maakte, met cf-agent en een `claim.json` erin.

Op de host zelf kan wel alles: `pct create`, `pct exec`, `pct push`, `qm`,
`pveam`. Een cf-agent op de host (zoals op elke andere node, één keer
geïnstalleerd met een token via Nodes → Agent installeren) maakt de CT's,
zet cf-agent en een aanmeldbestand erin, en zet de SSH-sleutel van de host in
de CT. Geen CT-template meer nodig, geen claim op IP-adres, en alles is te
volgen in de GUI.

## Opbouw

| Waar | Wat |
| --- | --- |
| Agent (protocol 31) | facts `proxmox` (versie, hostnaam in Proxmox, clusternaam) als `pveversion` bestaat. Nieuw onderwerp `cf.node.<id>.pve` voor snelle lees-verzoeken; nieuwe soort run `pve` (fase 10) voor alles wat iets verandert, met live uitvoer |
| Server | `internal/pvehost`: welke node de agent van welke Proxmox-host is (uit de facts, gekoppeld aan de hosts van de Proxmox-koppeling), inventaris per host, taak `pvehost.action` |
| Uitrol | `target.create: "ct"`: nieuwe CT's via de agent op de gekozen host, per node CTID, IP, CPU, geheugen, schijf, host en storage; daarna zoals nu |
| GUI | pagina **Proxmox → Hosts** (CT's en VM's per host met acties, CT maken, containertemplates), formulier **Cluster uitrollen** met de keuze "Nieuwe CT's (agent op de host)" |
| Database | geen migratie nodig: de koppeling host ↔ node komt uit `node_facts` en `proxmox_resources`, de rest staat in de taken en het logboek |

## De agent op een Proxmox-host

De agent merkt zelf dat hij op een Proxmox-host draait (`/usr/bin/pveversion`
en `/etc/pve`), en meldt in de facts:

```json
"proxmox": { "version": "9.0.10", "node": "ultra7plus", "cluster": "homelab" }
```

De server koppelt die node aan de host met dezelfde naam bij de
Proxmox-koppeling. Een host zonder agent blijft gewoon via de API werken;
alleen CT's maken en de agent in een CT zetten vragen de agent.

### Lezen: `cf.node.<id>.pve`

| op | Wat |
| --- | --- |
| `inventory` | `pct list` en `qm list` met per gast de configuratie (cores, geheugen, schijven, netwerk, onboot, tags, features, unprivileged), snapshots (`pct/qm listsnapshot`), storages (`pvesm status`), bridges (uit `/sys/class/net`), containertemplates per storage (`pveam list`), de publieke SSH-sleutels van root op de host |
| `available` | `pveam available --section system` (na `pveam update` als de lijst ouder is dan een dag) |

### Veranderen: run `pve`

Elke actie is een getypt verzoek, geen vrije shell. De agent bouwt zelf het
argv voor `pct`, `qm` of `pveam`; de server en de agent controleren met
dezelfde `Validate` (`pkg/protocol/pve.go`).

| actie | CT (`pct`) | VM (`qm`) |
| --- | --- | --- |
| `start`, `stop`, `shutdown`, `reboot`, `suspend`, `resume` | ja | ja |
| `set` (cores, memory, swap, onboot, tags, description, nameserver, features voor CT) | ja | cores, memory, onboot, tags, description |
| `resize` (schijf groter) | ja | ja |
| `snapshot`, `rollback`, `delsnapshot` | ja | ja |
| `migrate` (naar een andere host; CT met `--restart`, VM `--online` als hij draait) | ja | ja |
| `destroy` (alleen gestopt, met `--purge`) | ja | ja |
| `create_ct` | ja | – |
| `template_download` (`pveam download`) | – | – |
| `bootstrap` (cf-agent + aanmeldbestand + SSH-vertrouwen in een CT) | ja | – |

`destroy` vraagt in de GUI de naam van de gast overtikken; bij een node in een
prod-cluster ook TOTP (zoals overal in ClusterForge).

### `create_ct`

```
pct create <ctid> <ostemplate> --hostname <naam> --ostype debian
  --unprivileged 1 --features nesting=1[,keyctl=1]
  --cores N --memory MB --swap MB --rootfs <storage>:<GB>
  --net0 name=eth0,bridge=<br>[,tag=<vlan>],ip=<ip>/<prefix>,gw=<gw>   (of ip=dhcp)
  --nameserver "<dns>" --onboot 1 --tags clusterforge;<slug>
  --description "Beheerd door ClusterForge …" --ssh-public-keys <bestand>
```

`keyctl=1` alleen als de template Docker gebruikt (Docker in een
onbevoegde CT). De SSH-sleutels zijn die van het formulier plus de publieke
sleutels van root op de host (`/root/.ssh/id_*.pub`): zo kan de host meteen
met `ssh root@<ct>` in de CT. Een CT die al bestaat met dezelfde hostname en
de tag van het cluster, telt als gemaakt (hervatten na een herstart).

### `bootstrap`

1. Wachten tot `pct exec <ctid> -- true` lukt en de CT een route heeft.
2. cf-agent van ClusterForge downloaden op de host
   (`<server>/downloads/cf-agent-linux-<arch>`) en de sha256 controleren tegen
   die uit het verzoek (de server kent hem; komt over NATS).
3. `pct push` van cf-agent, `/etc/clusterforge/enroll.json` (eenmalig token,
   1 uur geldig, alleen voor deze node) en de systemd-units (zoals
   `ct-template.sh`), `systemctl enable --now cf-agent`.
4. De hostsleutel van de CT in `/root/.ssh/known_hosts` van de host
   (`ssh-keyscan`), zodat de host de CT vertrouwt.

Het token staat alleen in de spec van de run op de host (map 0700, root); de
agent haalt het na de run uit `spec.json` en maskeert het in de uitvoer.

Dezelfde bootstrap kan ook los vanuit de GUI: **Agent erin zetten** bij een
bestaande CT zonder agent.

## Uitrollen met nieuwe CT's

Cluster uitrollen krijgt bij "Waar" drie keuzes:

1. **Nieuwe CT's (agent op de host)** — nieuw, de standaard als er hosts met
   een agent zijn.
2. Klonen uit een golden image of CT-template (zoals nu).
3. Bestaande nodes (zoals nu).

Bij nieuwe CT's kiest Jonas de containertemplate (standaard: de nieuwste
`debian-13-standard`, die de host zelf downloadt als hij ontbreekt), bridge,
VLAN, netwerk, gateway, DNS en SSH-sleutels. Het voorbeeld toont per node een
rij met **host, CTID, IP, CPU, geheugen, schijf en storage**, allemaal aan te
passen; leeg is "ClusterForge kiest":

- host: gespreid over de hosts met een online agent (minste nodes van dit
  cluster, dan meeste vrij geheugen);
- CTID: het eerste vrije nummer vanaf 100 dat niet in Proxmox staat en niet
  door een andere rij gekozen is;
- IP: het volgende vrije adres na het eerste adres;
- CPU, geheugen, schijf: uit de parameters van de template;
- storage: de gekozen storage, of de actieve storage met de meeste vrije
  ruimte op die host waar containers op mogen.

### Netwerkregels

- Een node of VIP op het eerste, tweede of derde adres van het subnet (`.1`,
  `.2`, `.3` in een /24) wordt geweigerd: daar staan de CARP-VIP en de twee
  firewalls (OPNsense).
- Zonder gateway is het het eerste adres van het subnet (`.1`).
- De firewall van fase 16 blijft per cluster uit tot Jonas hem aanzet; een
  nieuwe CT krijgt geen ufw van de uitrol. De agent in de CT praat alleen
  uitgaand met de server (4222 en 8080), dus een ufw met default deny
  incoming houdt de uitrol niet tegen.

### Stappen van de taak

Per node "CT <naam> maken":

1. `template` — de containertemplate op de host (downloaden als hij ontbreekt);
2. `create` — `pct create`; de node krijgt meteen zijn koppeling met
   Proxmox (`pve_vmid`);
3. `start` — `pct start`;
4. `agent` — `bootstrap` met een nieuw token.

Daarna "Agents aanmelden" (wachten tot elke agent zich meldt), en verder
precies zoals nu: software per node, controle, cluster in gebruik nemen.
Elke fase staat in de state van de stap, dus een hervatte of opnieuw
geprobeerde uitrol gaat verder waar hij was.

## Mijlpalen

1. **M1** protocol 31 + agent: facts, `pve`-verzoeken, run `pve` met alle
   acties, tests met een nep-`pct`/`qm`/`pveam`.
2. **M2** server en GUI: `internal/pvehost`, API `/pve-hosts`, taak
   `pvehost.action`, pagina Proxmox → Hosts.
3. **M3** uitrol: `target.create: "ct"`, placement met CTID/IP/CPU/RAM/disk,
   netwerkregels, de stappen hierboven, formulier Cluster uitrollen.
4. **M4** README en image.

Eén PR per fase op deze thread-branch (de stapel #1–#118 blijft lineair), met
de mijlpalen als aparte commits.
