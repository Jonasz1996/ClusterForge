# Fase 44: RustDesk-server als HA-cluster

Gevraagd door Jonas op 22/11: zijn twee RustDesk-nodes (agents `rustdesk` en
`rustdesk-failover`, elk met hbbs/hbbr in Docker en keepalived, online via
NAT) als beheerd cluster, plus een HTTPS-API-server en wat RustDesk verder
biedt.

## Wat de open-source server kan, en wat Pro vraagt

Bronnen: <https://rustdesk.com/docs/en/self-host/>,
<https://rustdesk.com/docs/en/self-host/rustdesk-server-pro/>, de broncode van
rustdesk-server 1.1.17 en lejianwen/rustdesk-api v2.7.

| Functie | Open-source hbbs/hbbr | Met lejianwen/rustdesk-api (MIT) | Alleen Pro |
| --- | --- | --- | --- |
| Overnemen, bestanden, chat, TCP-tunnel, privacy | ja (client-functies) | | |
| Spraakoproep in een sessie, camera bekijken (client 1.4) | ja, de server relayt alleen | | |
| Inloggen in de client, adresboek, groepen | nee | ja | ja |
| OIDC (Authentik), LDAP | nee | ja | ja |
| Webbeheer, apparaten, verbindings- en bestandslogs | nee | ja | ja |
| Webclient | nee | ja (v1; webclient2 is weg na een DMCA) | ja |
| 2FA, toegangsbeleid, strategie-sync | nee | deels | ja |
| Meerdere relays met geo-keuze, eigen client-generator | nee | nee | ja |

Videobellen zoals Teams of Jitsi bestaat in RustDesk niet, ook niet in Pro.
Wel spraak tijdens een sessie en "camera bekijken" in de client.

## Ontwerp

Template `rustdesk` 1.0.0, zelfde model als `nginx-proxy-manager`:

- hbbs, hbbr en de API (`lejianwen/rustdesk-api`) draaien in Docker met
  `network_mode: host`, alleen op de node met het VIP (`cf-rustdesk.service`,
  gestart door keepalived, nooit enabled).
- De VIP-node maakt elke minuut een kopie: `id_ed25519` + `.pub`, `db_v2.sqlite3`
  en `rustdeskapi.db` (sqlite3 `.backup` + integrity_check + SHA256SUMS). De
  sync "RustDesk-sleutels en data" zet die op de andere nodes; een node die
  het VIP krijgt, zet de nieuwste geldige kopie op zijn plaats vóór de start.
  Zo blijft de sleutel na een failover dezelfde.
- Alleen node 1 mag zonder kopie starten. Zonder sleutel neemt die de sleutel
  en database over uit de map van de oude hbbs/hbbr-containers (mount op
  `/root` of `/data`); anders maakt hbbs een nieuwe. Een andere node zonder
  kopie neemt het VIP nooit (check faalt), zodat er nooit een tweede sleutel
  ontstaat.
- De uitrol stopt oude hbbs/hbbr-containers (`--restart=no`), hun mappen
  blijven staan. `rustdesk-ctl.sh overnemen [map]` blijft als handmatige weg.
- Wachtwoord van `admin` (API) is een geheim in de parameters; de check op de
  VIP-node zet het met `apimain reset-admin-pwd` als het in ClusterForge
  veranderde. De stempel reist mee met de kopie. JWT-sleutel is op elke node
  dezelfde, dus geen nieuwe login na een failover.
- Catalogi: firewall (21115-21117 TCP + 21116 UDP overal, 21114 en 21118/21119
  LAN), homepage-tegel, Publicatie: hbbs/hbbr zijn intern (niet via NPM), de
  API (21114) wel; `rustdesk` staat bij AuthentikSkip (eigen login, en de
  client praat rechtstreeks met de API).

Geen agentprotocol en geen migratie.

## Poorten (NAT van de router naar het VIP)

- TCP 21115 (NAT-test), TCP 21116 + UDP 21116 (ID, hole punching), TCP 21117 (relay): verplicht.
- TCP 21118/21119 (websocket): alleen voor de webclient, liever via NPM.
- TCP 21114 (API): niet via NAT, maar via NPM met https (proxy host naar `http://VIP:21114`).
