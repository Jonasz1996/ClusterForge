# ClusterForge installeren in een Debian-LXC op Proxmox

Deze stappen zetten ClusterForge op in een lege Debian-container met Docker Compose: de server, PostgreSQL en VictoriaMetrics draaien als containers in de LXC.

## 1. De container in Proxmox

Aanbevolen: Debian 13-template, 2 vCPU, 2 GB RAM, 16 GB disk, een vast IP-adres. Draaien gaat ruim binnen 1 GB; alleen het image zelf bouwen vraagt meer: de Go-compiler gebruikt tot 2,2 GB, bovenop de containers die al draaien. Met 2 GB RAM bouw je met de zuinige vlaggen uit [Bijwerken](#bijwerken), of je bouwt niet en haalt het kant-en-klare image.

Docker in een LXC heeft twee features nodig. Zet ze aan op de Proxmox-host (vervang `120` door je container-ID) en herstart de container:

```sh
pct set 120 -features nesting=1,keyctl=1
pct reboot 120
```

Of in de webinterface: container → Options → Features → `nesting` en `keyctl` aanvinken.

## 2. Docker installeren

In de container, als root:

```sh
apt update && apt full-upgrade -y
apt install -y ca-certificates curl git openssl

install -m 0755 -d /etc/apt/keyrings
curl -fsSL https://download.docker.com/linux/debian/gpg -o /etc/apt/keyrings/docker.asc
echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/debian $(. /etc/os-release && echo "$VERSION_CODENAME") stable" \
  > /etc/apt/sources.list.d/docker.list
apt update
apt install -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin

docker run --rm hello-world
```

## 3. De code ophalen

De repository is privé. De eenvoudigste manier is een deploy key met alleen leesrechten:

```sh
ssh-keygen -t ed25519 -f /root/.ssh/clusterforge -N "" -C "clusterforge-lxc"
cat /root/.ssh/clusterforge.pub
```

Voeg die publieke sleutel toe op GitHub: repository ClusterForge → Settings → Deploy keys → Add deploy key (zonder schrijfrechten). Daarna:

```sh
GIT_SSH_COMMAND="ssh -i /root/.ssh/clusterforge" git clone git@github.com:Jonasz1996/ClusterForge.git /opt/clusterforge
git -C /opt/clusterforge config core.sshCommand "ssh -i /root/.ssh/clusterforge"
```

## 4. Configureren

```sh
cd /opt/clusterforge/deploy
cp .env.example .env
sed -i "s/^POSTGRES_PASSWORD=.*/POSTGRES_PASSWORD=$(openssl rand -hex 24)/" .env
# Alleen als hij nog leeg is: een nieuwe sleutel maakt de opgeslagen tokens onleesbaar.
sed -i "s/^CF_MASTER_KEY=$/CF_MASTER_KEY=$(openssl rand -hex 32)/" .env
grep CF_MASTER_KEY .env
nano .env
```

`CF_MASTER_KEY` versleutelt het Proxmox-token in de database. Bewaar de waarde ook ergens anders, bijvoorbeeld in je wachtwoordmanager: zet je de LXC ooit opnieuw op met een back-up van de database maar zonder deze sleutel, dan moet je het token opnieuw invullen.

Kies in `.env` één van deze twee situaties:

| Situatie | `CF_BIND` | `CF_SECURE_COOKIES` | `CF_TRUST_PROXY_HEADERS` |
| --- | --- | --- | --- |
| Achter een reverse proxy met TLS op een andere machine (aanbevolen) | `0.0.0.0` | `true` | `true` |
| Eerst even testen via `http://<ip>:8080`, zonder TLS | `0.0.0.0` | `false` | `false` |

Met `CF_SECURE_COOKIES=true` en gewone http lukt inloggen niet: de browser weigert dan de sessiecookie. Zet het terug op `true` zodra er TLS voor staat. Zet `CF_TRUST_PROXY_HEADERS` alleen op `true` als poort 8080 niet rechtstreeks bereikbaar is voor andere machines, anders kan iemand een vals IP-adres meesturen.

Agents verbinden rechtstreeks met de LXC op poort 4222, niet via je reverse proxy. Zet daarom in `.env` het vaste IP-adres (of een DNS-naam die ernaar wijst) van de LXC:

```sh
CF_NATS_ADVERTISE=10.0.10.50
```

Laat je het leeg, dan krijgt een agent de hostnaam van de URL waarmee hij zich aanmeldt; achter een proxy op een andere machine is dat de proxy, en daar staat geen NATS.

## 5. Starten en een beheerder aanmaken

```sh
cd /opt/clusterforge/deploy
docker compose up -d --build
docker compose ps
docker compose exec -it server clusterforge-server admin create -username jonas
```

Het eerste `up --build` duurt een paar minuten. Open daarna `http://<ip>:8080` (of je proxy-URL), log in en zet meteen tweestapsverificatie aan bij Instellingen.

## 6. Reverse proxy

Voorbeeld voor Caddy op een andere machine, met automatische TLS:

```caddyfile
clusterforge.example.lan {
    reverse_proxy 10.0.10.50:8080
}
```

Voor nginx: `proxy_pass http://10.0.10.50:8080;` met `proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;` en `proxy_set_header Host $host;`.

Beperk poort 8080 op de LXC tot je proxy, bijvoorbeeld met de Proxmox-firewall op de container. Poort 4222 moet wel open staan voor de servers waarop je een agent zet; die verbinding is altijd TLS, met een certificaat dat de agent bij het aanmelden vastlegt.

## 7. Agents op je servers zetten

Log in, ga naar Nodes → Agent installeren en maak een token. Standaard is het 24 uur geldig en één keer bruikbaar; zet "Aantal keer bruikbaar" hoger als je meerdere servers tegelijk aanmeldt. Kies eventueel meteen het cluster waar de nieuwe nodes in horen. Kopieer het commando en voer het als root uit op elke server:

```sh
curl -fsSL https://clusterforge.example.lan/install/agent.sh | sudo sh -s -- --server https://clusterforge.example.lan --token cfe_...
```

Het script installeert `/usr/local/bin/cf-agent` en de systemd-service `cf-agent`. Binnen enkele seconden staat de server bij Nodes als Online, met zijn facts op de detailpagina. Nodig op de server: systemd en `curl` of `wget`.

Een server opnieuw installeren of een agent intrekken: trek hem in bij de node in de webinterface en voer het commando opnieuw uit met een nieuw token.

Zodra de agent draait, verschijnen de status en de grafieken van de node vanzelf. De metrics komen in de VictoriaMetrics uit de compose-stack; daar is niets voor in te stellen.

## 8. Grafana koppelen (optioneel)

Heb je al een Grafana, dan kan die de metrics rechtstreeks uit VictoriaMetrics lezen:

1. Zet in `.env` `CF_VM_BIND=0.0.0.0` en voer `docker compose up -d` opnieuw uit. VictoriaMetrics heeft geen login: beperk poort 8428 met de Proxmox-firewall tot je Grafana.
2. Voeg in Grafana een databron van het type Prometheus toe met URL `http://<ip van de LXC>:8428`.
3. Importeer het dashboard Node Exporter Full (id 1860). Kies bij job `clusterforge`; de nodes staan onder hun hostname.
4. Voor een link vanuit ClusterForge naar dat dashboard zet je in `.env`:

   ```sh
   CF_GRAFANA_NODE_URL=https://grafana.example.lan/d/rYdddlPWk/node-exporter-full?var-job=clusterforge&var-node={hostname}
   ```

   Pas het adres en de uid van het dashboard aan als die bij jou anders zijn, en voer `docker compose up -d` opnieuw uit.

## 9. Proxmox koppelen

Maak op een van je Proxmox-hosts een API-token voor ClusterForge, als root:

```sh
pveum role add ClusterForge --privs "VM.Audit VM.PowerMgmt VM.Snapshot VM.Migrate VM.Allocate VM.Clone VM.Config.CPU VM.Config.Memory VM.Config.Disk VM.Config.Network VM.Config.Cloudinit VM.Config.Options VM.Monitor VM.Backup Sys.Audit Datastore.Audit Datastore.AllocateSpace SDN.Use"
pveum user add clusterforge@pve --comment "ClusterForge"
pveum acl modify / --users clusterforge@pve --roles ClusterForge
pveum user token add clusterforge@pve cf --privsep 0
```

Draai je Proxmox VE 9, vervang dan `VM.Monitor` door `VM.GuestAgent.Audit VM.GuestAgent.FileWrite`; zegt `pveum` dat een recht niet bestaat, dan heb je de lijst voor de andere versie. Kijk je versie na met `pveversion`.

Kopieer het secret uit de uitvoer van het laatste commando; Proxmox toont het maar één keer. Klik dan in ClusterForge bij Proxmox op "Proxmox koppelen":

1. API-adres: het adres van een Proxmox-host, bijvoorbeeld `https://10.0.10.11:8006`. In een Proxmox-cluster is één host genoeg.
2. Token-id: `clusterforge@pve!cf`, en het token-secret.
3. Klik naast de vingerafdruk op "Ophalen". Vergelijk de getoonde vingerafdruk met die op de host en klik op "Deze vingerafdruk gebruiken":

   ```sh
   openssl x509 -in /etc/pve/local/pve-ssl.pem -noout -fingerprint -sha256
   ```

   Heeft Proxmox een certificaat van een echte CA (bijvoorbeeld via ACME), dan kun je de vingerafdruk leeg laten.

Na het opslaan staan de hosts, VM's, containers en storage er binnen enkele seconden. Koppel daarna elke VM die een node is aan die node: in de lijst met VM's staat "Koppelen aan …" als de naam overeenkomt met een node, en bij de node zelf kun je een VM kiezen. De LXC moet poort 8006 van de Proxmox-host kunnen bereiken.

## 10. Een golden image maken en een cluster uitrollen

ClusterForge rolt nieuwe clusters uit door VM's te klonen uit een golden image: een VM-template met Debian 13, cloud-init, de QEMU guest agent en cf-agent. Maak die één keer, als root op een van je Proxmox-hosts (de host moet internet hebben):

```sh
curl -fsSL https://clusterforge.example.lan/install/golden-image.sh \
  | bash -s -- --server https://clusterforge.example.lan --storage local-lvm
```

Gebruik het adres waarop de LXC bereikbaar is. Het script installeert zo nodig `libguestfs-tools`, haalt het cloud-image van Debian (en controleert de checksum), zet er qemu-guest-agent en cf-agent in en maakt VM-template 9000 met de naam `debian-13-clusterforge`. Andere keuzes:

| Optie | Standaard | Wanneer |
| --- | --- | --- |
| `--storage` | `local-lvm` | Kies gedeelde storage (Ceph, NFS) om de VM's over meerdere hosts te verdelen; op lokale storage komen ze allemaal op deze host |
| `--vmid` | `9000` | Als 9000 al bezet is |
| `--bridge` | `vmbr0` | Een andere netwerkbridge |
| `--replace` | | Een eerdere golden image vervangen, bijvoorbeeld na een update van ClusterForge |

Daarna, in ClusterForge:

1. Proxmox → Synchroniseren (of wacht 20 seconden), zodat de template er staat.
2. Clusters → Cluster uitrollen. Kies Nginx met keepalived, geef een naam en een vrij VIP, kies de golden image en vul het eerste adres met prefix (bijvoorbeeld `10.0.20.11/24`), de gateway en DNS in. Plak je publieke SSH-sleutel; daarmee log je in als `debian`.
3. Onderaan staat welke nodes er komen. Klik op Uitrollen en volg de taak. Na een paar minuten staan de nodes op Actief en antwoordt `http://<VIP>/`.

De nieuwe VM's moeten het adres van ClusterForge (veld "Adres van ClusterForge" in het formulier) en poort 4222 van de LXC kunnen bereiken.

## Back-up en herstel van ClusterForge zelf

Alles wat ClusterForge weet, staat in PostgreSQL: clusters, nodes, taken, het logboek, de versleutelde tokens en ook de sleutel en het certificaat van NATS waarmee de agents verbinden. Daarnaast heb je `deploy/.env` nodig, met `CF_MASTER_KEY`. De metrics in VictoriaMetrics zijn handig maar niet nodig om verder te kunnen.

Een back-up maken (bijvoorbeeld elke nacht met cron):

```sh
cd /opt/clusterforge/deploy
mkdir -p /root/cf-backup
docker compose exec -T postgres pg_dump -U clusterforge -Fc clusterforge > /root/cf-backup/cf-$(date +%F).dump
cp .env /root/cf-backup/env-$(date +%F)
chmod 600 /root/cf-backup/*
```

Behandel de dump als een geheim: met de NATS-sleutel erin kan iemand zich tegenover je agents als ClusterForge voordoen. Bewaar `.env` ook buiten de LXC (wachtwoordmanager). Zonder dezelfde `CF_MASTER_KEY` zijn de Proxmox-, GitHub- en assistentsleutels in de dump niet meer te lezen en vul je ze opnieuw in.

Het eenvoudigst is daarnaast een back-up van de hele LXC in Proxmox (modus *stop* of *snapshot*); die bevat database, metrics en `.env` samen.

Terugzetten op een nieuwe LXC (stappen 1 tot 3 gedaan):

```sh
cd /opt/clusterforge/deploy
cp /root/cf-backup/env-JJJJ-MM-DD .env
docker compose up -d postgres
docker compose exec -T postgres pg_restore -U clusterforge -d clusterforge --clean --if-exists < /root/cf-backup/cf-JJJJ-MM-DD.dump
docker compose up -d --build
```

Geef de nieuwe LXC hetzelfde IP-adres (of zet `CF_NATS_ADVERTISE` en de DNS-naam goed): de agents verbinden met het adres en het certificaat dat ze bij hun aanmelding kregen, en melden zich dan vanzelf weer.

## Bijwerken

Maak eerst een back-up zoals hierboven: migraties lopen automatisch bij het starten en een oudere versie terugzetten kan alleen met die back-up.

### Zonder bouwen (aanbevolen op een kleine LXC)

CI bouwt bij elke commit een image en zet het op ghcr.io, getagd met de korte commit (`47d7ecc`), het PR-nummer (`pr-97`) of de branch (`main`), en bij een versie met de tag (`v0.5.0`). De LXC haalt het alleen op:

```sh
cd /opt/clusterforge
git fetch origin
git checkout -B main origin/main      # compose-bestand en docs van dezelfde versie
cd deploy
sed -i 's|^CF_IMAGE=.*|CF_IMAGE=ghcr.io/jonasz1996/clusterforge-server:main|' .env
docker compose pull server
docker compose up -d
docker compose ps                     # server moet na een minuut "healthy" zijn
```

Kies een tag die bij je checkout past: `:pr-97` bij een PR, `:v0.5.0` bij een versie. Het image bevat ook de agents voor amd64 en arm64 die de server aanbiedt; de server-image uit CI is alleen amd64 (een versie-tag ook arm64). De versie in de webinterface is de korte commit; `CF_VERSION` doet hier niets.

Geeft `pull` `denied` of `unauthorized`, dan staat het pakket nog op privé. Zet het eenmalig op openbaar op GitHub: profiel → Packages → clusterforge-server → Package settings → Change visibility → Public. Of log op de LXC in met een token met alleen `read:packages` (GitHub → Settings → Developer settings → Personal access tokens (classic)):

```sh
docker login ghcr.io -u Jonasz1996     # plak het token als wachtwoord
```

### Zelf bouwen

```sh
cd /opt/clusterforge
git fetch origin
git checkout -B main origin/main      # of een versie: git checkout v0.5.0
cd deploy
CF_VERSION=$(git describe --tags --always) docker compose up -d --build
docker compose ps
```

Laat `CF_IMAGE` daarvoor leeg in `.env`. Zet je liever `CF_VERSION` vast in `.env`, pas hem dan bij elke update aan. Draai alleen `main` of een versie-tag in productie, geen werkbranch: een branch die later anders gemerged wordt, kan je database een migratie geven die `main` niet kent.

Bouwen vraagt geheugen. Gemeten op de server met een lege build-cache (4 CPU's), het piekgeheugen van de Go-compiler alleen:

| In `.env` | Piek | Duur |
| --- | --- | --- |
| niets (standaard) | 2,2 GB | 1× |
| `CF_BUILD_GOFLAGS="-p=1 -gcflags=all=-c=1"` | 1,8 GB | 2,3× |
| dat en `CF_BUILD_GOGC=25` | 1,4 GB | 2,6× |

Het grootste pakket (de Anthropic-SDK voor de assistent) vraagt het meeste; `-p=1` alleen helpt daarom nauwelijks. Op een LXC met 2 GB RAM zet je beide regels in `.env`, en geef je hem swap of tijdelijk meer geheugen op de Proxmox-host (werkt meteen, zonder herstart):

```sh
pct set <ctid> --memory 4096 --swap 2048
```

Stopt de build toch met `signal: killed`, stop dan eerst de draaiende containers met `docker compose stop` en bouw opnieuw.

## Problemen

| Symptoom | Oorzaak |
| --- | --- |
| `docker run` geeft fouten over `permission denied` of `sysctl` | `nesting` en `keyctl` staan niet aan, of de container is niet herstart |
| Inloggen lijkt te lukken maar je komt terug op het loginscherm | `CF_SECURE_COOKIES=true` zonder TLS |
| Inloggen blijft hangen of de knop doet niets | De server krijgt geen databaseverbinding (pool vol, dode verbinding of een slot in PostgreSQL); zie [Inloggen blijft hangen](#inloggen-blijft-hangen) |
| Melding "de server antwoordt niet op tijd" | Zelfde oorzaak; de server geeft het na 10 à 20 s op in plaats van eindeloos te wachten, en zet de oorzaak in de log |
| Build stopt met `signal: killed` | Te weinig geheugen; haal het image uit CI of bouw zuiniger, zie [Bijwerken](#bijwerken) |
| `docker compose ps` toont de server als `unhealthy` | De server bereikt PostgreSQL niet of start niet; kijk met `docker compose logs server` |
| Bij een node staat "Grafieken staan uit" | `CF_VICTORIAMETRICS_URL` is niet gezet; de compose-stack zet die standaard |
| Node blijft Offline na het installeren van de agent | Poort 4222 niet bereikbaar vanaf de node, of `CF_NATS_ADVERTISE` wijst niet naar de LXC; kijk op de node met `journalctl -u cf-agent` |
| Bij Proxmox staat dat de server geen masterkey heeft | `CF_MASTER_KEY` is niet gezet in `.env`; zet hem en voer `docker compose up -d` uit |
| Koppelen geeft een fout over het certificaat | De vingerafdruk klopt niet (nieuw certificaat op de host?); haal hem opnieuw op via Bewerken |
| Koppelen geeft 401 of 403 | Token-ID of secret verkeerd, of de rol mist rechten; controleer met `pveum user token permissions clusterforge@pve cf` |
| "Het token-secret is niet te ontsleutelen" | `CF_MASTER_KEY` is veranderd; vul het secret opnieuw in via Proxmox → Bewerken |
| Uitrollen geeft 403 bij het klonen of instellen | De rol mist rechten om VM's te maken; voer `pveum role modify ClusterForge --privs "…"` uit met de lijst uit stap 9 |
| Bij Back-ups staat "Proxmox toont geen enkele back-up" | De rol mist `VM.Backup`; voer `pveum role modify ClusterForge --privs "…"` uit met de lijst uit stap 9, of er draait nog geen back-upjob in Proxmox |
| Uitrol blijft wachten op de QEMU guest agent | De VM start niet goed, of de golden image heeft geen qemu-guest-agent; maak hem opnieuw met het script en `--replace` |
| "cf-agent meldt zich niet aan" | De VM bereikt ClusterForge of poort 4222 niet; log in via de console van Proxmox en kijk met `journalctl -u cf-agent` |
| Controle "geeft nog geen 200" | Nginx draait niet of het VIP is niet bereikbaar vanaf de LXC; kijk op de node naar `systemctl status nginx keepalived` en klik daarna op Opnieuw proberen |
| Logs bekijken | `docker compose logs -f server` |

### Inloggen blijft hangen

Voer in `/opt/clusterforge/deploy` uit:

```bash
# Antwoordt de server, en bereikt hij de database? Hoort binnen 10 s {"status":"ok",...} te geven.
curl -s -m 10 http://127.0.0.1:8080/api/v1/health; echo
# Hoort binnen 20 s een 401 met JSON te geven (fout wachtwoord), geen stilte.
curl -s -i -m 25 -X POST http://127.0.0.1:8080/api/v1/auth/login \
  -H 'Content-Type: application/json' -d '{"username":"test","password":"test"}'
# Staat de server op unhealthy?
docker compose ps
# Foutregels van de server
docker compose logs --since 1h server | grep -iE 'error|interne fout|loopt nog|panic'
# Wat doen de verbindingen van ClusterForge in PostgreSQL? Veel "idle in transaction"
# of lang wachtende queries (wait_event Lock) wijzen op een vastzittende verbinding.
docker compose exec postgres psql -U clusterforge -d clusterforge -c \
  "select pid, state, wait_event_type, wait_event, now() - query_start as duur, left(query, 60) as query
   from pg_stat_activity where datname = 'clusterforge' order by query_start;"
```

Daarna lost `docker compose restart server` het meestal op. Stuur de uitvoer van
de commando's hierboven eerst mee als je de oorzaak wilt laten uitzoeken: na de
herstart is ze weg.

Op versies tot en met fase 5 (zonder de verbeterronde) had de server maar 4 databaseverbindingen op een
kleine LXC en wachtte hij zonder grens. Werk bij naar de laatste versie, of zet
tot dan `&pool_max_conns=25` achter `CF_DATABASE_URL` in `docker-compose.yml`
(na `sslmode=disable`) en voer `docker compose up -d` uit.
