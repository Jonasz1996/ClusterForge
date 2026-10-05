# ClusterForge installeren in een Debian-LXC op Proxmox

Deze stappen zetten ClusterForge op in een lege Debian-container met Docker Compose: de server, PostgreSQL en VictoriaMetrics draaien als containers in de LXC.

## 1. De container in Proxmox

Aanbevolen: Debian 13-template, 2 vCPU, 2 GB RAM, 16 GB disk, een vast IP-adres. Het eerste bouwen van de image (Go en Next.js) heeft het meeste geheugen nodig; daarna draait alles ruim binnen 1 GB.

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
nano .env
```

Kies in `.env` één van deze twee situaties:

| Situatie | `CF_BIND` | `CF_SECURE_COOKIES` | `CF_TRUST_PROXY_HEADERS` |
| --- | --- | --- | --- |
| Achter een reverse proxy met TLS op een andere machine (aanbevolen) | `0.0.0.0` | `true` | `true` |
| Eerst even testen via `http://<ip>:8080`, zonder TLS | `0.0.0.0` | `false` | `false` |

Met `CF_SECURE_COOKIES=true` en gewone http lukt inloggen niet: de browser weigert dan de sessiecookie. Zet het terug op `true` zodra er TLS voor staat. Zet `CF_TRUST_PROXY_HEADERS` alleen op `true` als poort 8080 niet rechtstreeks bereikbaar is voor andere machines, anders kan iemand een vals IP-adres meesturen.

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

Beperk poort 8080 op de LXC tot je proxy, bijvoorbeeld met de Proxmox-firewall op de container.

## Bijwerken

```sh
cd /opt/clusterforge
git pull
cd deploy && docker compose up -d --build
```

Databasemigraties lopen automatisch bij het starten van de server.

## Problemen

| Symptoom | Oorzaak |
| --- | --- |
| `docker run` geeft fouten over `permission denied` of `sysctl` | `nesting` en `keyctl` staan niet aan, of de container is niet herstart |
| Inloggen lijkt te lukken maar je komt terug op het loginscherm | `CF_SECURE_COOKIES=true` zonder TLS |
| Build stopt met `killed` | Te weinig geheugen; geef de LXC tijdelijk 3 GB |
| Logs bekijken | `docker compose logs -f server` |
