# ClusterForge

Eén webinterface om traditionele Linux HA-clusters (Proxmox, Debian/Ubuntu, Keepalived, gedeelde databases, Docker, cronclusters) te beheren, monitoren, deployen en onderhouden. Geen Kubernetes-kloon.

Het technisch ontwerp staat in [docs/design/mvp-fase-1.md](docs/design/mvp-fase-1.md).

## Stand van zaken

| Mijlpaal | Inhoud | Status |
| --- | --- | --- |
| 1. Fundament | Server, database, login met TOTP, webinterface, CI | klaar |
| 2. Inventory | Clusters, nodes, VIP's | klaar |
| 3. Agent | Enrollment, heartbeat, facts | klaar |
| 4. Monitoring | Metrics, status, dashboards | klaar |
| 5. Proxmox | Sync en VM-acties | klaar |
| 6. Node lifecycle | Reboot, maintenance, drain | klaar |
| 7. Templates en deployment | Clusters uit templates | klaar |

| Fase 2 | Inhoud | Status |
| --- | --- | --- |
| 1. Logboek | Wie deed wat, wanneer en vanaf waar; filters en export | klaar |
| 2. Back-ups | Versheid van de Proxmox-back-ups per VM, VM's zonder back-upjob | klaar |
| 3. Herkomst en clusterslot | IP, sessie en taak bij elke regel, commando's aan agents in het logboek, één schrijvende taak per cluster | klaar |
| 4. Gewenste staat | Specificatie per cluster met revisies, vaste templateversies, controle op onveilige waarden in templates | klaar |
| 5. Drift zien | Per node zien wat afwijkt van de gewenste staat van een cluster uit een template, zonder iets op de node te veranderen | klaar |
| 6. Baseline en negeren | Een baseline als gewenste staat voor clusters zonder template, en afwijkingen bewust negeren met een reden | klaar |
| 7. Failovertest | Met de hand keepalived of nginx stoppen op de VIP-eigenaar in lab en test, meten hoe snel een andere node overneemt, en alles weer herstellen | klaar |
| 8. Back-upcontrole | Een back-up terugzetten als tijdelijke VM met afgesloten netwerk, opstarten, controleren via de guest agent en altijd weer verwijderen | klaar |
| 9. Diensten en afhankelijkheden | Welke dienst van welke andere afhangt, over clusters heen; voorstellen uit de agents en "Wat raakt uitval?" | klaar |
| 10. Drift herstellen | Gekozen afwijkingen opnieuw toepassen, node voor node met de VIP-eigenaar als laatste en een gezondheidscontrole na elke node; op prod met de slug en tweestapsverificatie | klaar |
| 11. Planning en prod | Failovertests en back-upcontroles gepland in een testvenster, de VM hard uitzetten in lab en test, en failovertests op prod met de hand met de slug en tweestapsverificatie | klaar |
| 12. Impact bij acties | Per dienst de opgeslagen status met een regel in het logboek bij elke verandering, "geraakt door" in de clusterlijst, en in de bevestigingsvensters welke bekende diensten down of verminderd raken | klaar |
| 13. Meer templates | Docker-hosts, Cron-cluster en Eigen applicatie (een container achter een VIP), met hun diensten meteen in de graaf | klaar |
| 14. GitOps: lezen en plannen | Clusters uit een template beschrijven in een GitHub-repository; elk bestand gecontroleerd met veld en regel, en per wijziging een plan met de diff per node, zonder iets op de nodes te veranderen | klaar |
| 15. GitOps: goedkeuren en toepassen | Een plan goedkeuren of afwijzen; goedkeuren schrijft een revisie met de commit en past de wijziging node voor node toe, met de VIP-eigenaar als laatste; terugdraaien met git revert, en Opnieuw toepassen na een mislukte toepassing | klaar |
| 16. Diepe back-upcontrole | In de teruggezette sandbox controleert cf-agent verify of de services van de node draaien, hun poorten luisteren, de HTTP-controles van de template slagen en PostgreSQL of MariaDB antwoordt | klaar |
| 17. Omhoog schalen en nieuwe clusters uit Git | Een hogere node_count maakt na goedkeuring de nieuwe VM's, meldt hun agents aan en past eerst de bestaande nodes toe, zodat geen node met een onbekende peer start; een nieuw bestand wordt na goedkeuring een uitrol met spec-revisie 1 en bron Git | klaar |

| Fase 3 | Inhoud | Status |
| --- | --- | --- |
| 1. Wijzigingsjournaal | Per node wat er veranderde, ook buiten ClusterForge om: pakketten, diensten, bestanden, herstarts, SSH-logins, geheugentekort, netwerk en Proxmox-taken, elk met een Nederlandse zin | klaar |
| 2. Incidenten | Statusovergangen die bij elkaar horen, als één storing met een begin, een einde, een ernst en wat er geraakt was; pagina Incidenten en een kaart op het overzicht | klaar |
| 3. What Broke: tijdlijn en oorzaak | Per incident het eerste symptoom en een gerangschikte lijst waarschijnlijke oorzaken met de redenen, uit één tijdlijn van logboek, journaal en metrics; 'Wat is er veranderd' per cluster en een tijdlijn per cluster en node | klaar |
| 4. AI-assistent | Vragen over de fleet in het Nederlands met de Anthropic API, met tools die alleen lezen, de kosten per antwoord, een maandbudget en een vraagknop op clusters, nodes en incidenten | klaar |
| 5. Health score en scorekaarten | Per cluster en voor het geheel een score van 0 tot 100 met een cijfer A tot F, uit zeven scorekaarten en de kaart Platform; per bevinding de reden, een link om ze op te lossen en wat het oplevert, accepteren met een reden, en een trend over 30 dagen | klaar |
| 6. Wat-als en SPOF's | Kies wat er uitvalt (een Proxmox-host, storage, bridge, node, dienst of cluster) en zie per cluster de voorspelde status, welke VM's Proxmox HA herstart en welke diensten geraakt worden; de onderdelen die alleen al productie down leggen, als bevinding in de score; en of ClusterForge zelf meegaat | klaar |
| 7. Infrastructuurkaart | Een kaart op `/kaart` met drie lagen: fysiek (hosts, VM's en storage), netwerk (bridges, VLAN's, subnetten, gateways, VIP's en VRRP-peers) en diensten; met filters, een detailpaneel en de wat-als of een incident als overlay. Het netwerk van de Proxmox-hosts wordt gelezen, en een VM met het MAC-adres van een node wordt een koppelvoorstel | klaar |

## Draaien met Docker Compose

Stap voor stap in een Debian-container op Proxmox: [docs/install-proxmox-lxc.md](docs/install-proxmox-lxc.md).

```sh
cd deploy
cp .env.example .env          # zet minstens POSTGRES_PASSWORD en CF_MASTER_KEY
docker compose up -d --build
docker compose exec -it server clusterforge-server admin create -username jonas
```

De server luistert op `127.0.0.1:8080`. Zet hem achter je reverse proxy met TLS: de sessiecookie heeft de `Secure`-vlag en werkt dus niet over gewone http (tenzij `CF_SECURE_COOKIES=false`).

Agents verbinden zelf naar de server, op poort 4222 (NATS met TLS). Die poort moet dus bereikbaar zijn vanaf je nodes; ClusterForge maakt nooit zelf een verbinding naar een node.

### Configuratie

| Variabele | Standaard | Betekenis |
| --- | --- | --- |
| `CF_DATABASE_URL` | (verplicht) | PostgreSQL-connectiestring |
| `CF_LISTEN` | `:8080` | Adres van de HTTP-server |
| `CF_SECURE_COOKIES` | `true` | `Secure`-vlag op de sessiecookie |
| `CF_TRUST_PROXY_HEADERS` | `false` | Client-IP uit `X-Forwarded-For` halen; alleen achter je eigen proxy |
| `CF_SESSION_TTL` | `12h` | Sessie verloopt na zoveel tijd zonder activiteit |
| `CF_LOG_LEVEL` | `info` | `debug`, `info`, `warn` of `error` |
| `CF_NATS_LISTEN` | `:4222` | Adres waarop agents met NATS verbinden |
| `CF_NATS_ADVERTISE` | (leeg) | `host[:poort]` waarmee agents NATS bereiken; leeg betekent de hostnaam waarmee de agent zich aanmeldt |
| `CF_AGENT_DIR` | `/usr/share/clusterforge/agents` | Map met de `cf-agent`-binaries die de server aanbiedt om te downloaden |
| `CF_VICTORIAMETRICS_URL` | (leeg) | VictoriaMetrics voor de metrics, bijvoorbeeld `http://victoriametrics:8428`; leeg zet de grafieken uit |
| `CF_GRAFANA_NODE_URL` | (leeg) | Link naar Grafana bij elke node, met `{hostname}`, `{node_id}`, `{cluster}`, `{cluster_id}` en `{env}` |
| `CF_GRAFANA_CLUSTER_URL` | (leeg) | Link naar Grafana bij elk cluster, met `{cluster}`, `{cluster_id}` en `{env}` |
| `CF_MASTER_KEY` | (leeg) | Sleutel van 32 bytes (base64 of hex) waarmee de server geheimen zoals het Proxmox-token versleutelt; maak er een met `openssl rand -hex 32`. Zonder sleutel kan Proxmox niet gekoppeld worden |
| `CF_MASTER_KEY_FILE` | (leeg) | Bestand met de masterkey, in plaats van `CF_MASTER_KEY` |
| `CF_DRIFT_INTERVAL` | `15m` | Hoe vaak de server elke node op drift controleert; `0` zet dat uit (Nu controleren blijft werken), anders minstens `1m` |
| `CF_SANDBOX_STORAGE` | (leeg) | Proxmox-storage voor de tijdelijke VM van een back-upcontrole; leeg kiest per host de storage voor VM-schijven met de meeste vrije ruimte |
| `CF_SANDBOX_BOOT_TIMEOUT` | `10m` | Hoe lang een teruggezette VM mag doen over opstarten tot de guest agent antwoordt; tussen `30s` en `1h` |
| `CF_TEST_WINDOW` | `zo 03:00-05:00` | Het testvenster voor geplande failovertests en back-upcontroles: dagen (`ma` tot `zo`, met komma's, of `dagelijks`) en de tijden, op de klok van `TZ` |
| `CF_JOURNAL_SSH_LOGINS` | `true` | Bewaar SSH-logins (gebruiker, bron-IP en methode) in het journaal van de nodes; `false` laat ze weg |
| `TZ` | `Europe/Brussels` in Docker Compose | Tijdzone van de server; het testvenster volgt zomer- en wintertijd |

### Commando's

```text
clusterforge-server serve                        start de server (voert eerst de migraties uit)
clusterforge-server migrate                      voert alleen de migraties uit
clusterforge-server admin create -username X     maakt een gebruiker aan (-role admin|viewer)
clusterforge-server version                      toont de versie
```

`admin create` vraagt het wachtwoord in de terminal, of leest één regel van stdin als die geen terminal is.

## Agent installeren op een node

Maak in de webinterface bij Nodes → Agent installeren een token aan en voer het getoonde commando als root uit op de node:

```sh
curl -fsSL https://clusterforge.example/install/agent.sh | sudo sh -s -- --server https://clusterforge.example --token cfe_...
```

Het script downloadt `cf-agent` van je eigen server, controleert de checksum, meldt de node aan en start de systemd-service `cf-agent`. Bestaat er nog geen node met die hostname, dan maakt de aanmelding er een aan. De agent stuurt elke 10 seconden een heartbeat, elke 15 seconden metrics en elk kwartier (en bij elke nieuwe verbinding) zijn facts: OS, kernel, CPU, geheugen, schijven, netwerk, services, updates, Docker en Keepalived.

```text
cf-agent enroll -server URL -token cfe_...      meldt deze machine aan (doet het installatiescript)
cf-agent run                                    verbindt met ClusterForge (zo start systemd hem)
cf-agent facts                                  toont de facts van deze machine
cf-agent version                                toont de versie
```

De sleutel van de agent staat in `/etc/clusterforge/agent.json`. Intrekken kan bij de node in de webinterface; de verbinding valt dan meteen weg.

Een nieuwere agent zet je erop zonder opnieuw aan te melden:

```sh
curl -fsSL https://clusterforge.example/install/agent.sh | sudo sh -s -- --server https://clusterforge.example --upgrade
```

Het script controleert de checksum, vervangt alleen de binary en herstart `cf-agent`; `agent.json` blijft staan. Mislukt de download of de checksum, dan blijft de oude agent draaien.

## Monitoring

De agent stuurt elke 15 seconden metrics mee over dezelfde verbinding: CPU, load, geheugen, schijven, schijf-I/O, netwerk en temperatuur. De namen volgen node_exporter, en de server voegt de labels `job="clusterforge"`, `instance` en `node` (hostname), `node_id`, `cluster`, `cluster_id` en `env` toe. Bestaande Grafana-dashboards voor node_exporter, zoals Node Exporter Full, werken daardoor met VictoriaMetrics als Prometheus-databron.

De status van nodes en clusters wordt elke 5 seconden opnieuw berekend:

| Status | Node | Cluster |
| --- | --- | --- |
| Gezond | Heartbeat binnen 30 s, geen probleem | Alle actieve nodes gezond, elk VIP precies één eigenaar |
| Verminderd | Heartbeat 30 tot 90 s oud, een schijf voor 90 % vol, een gefaalde service, of een service die enabled is maar niet draait | Minstens één actieve node niet gezond, maar de VIP's zijn in orde |
| Down | Geen heartbeat in 90 s | Een VIP zonder eigenaar, of alle nodes down |
| Split-brain | | Een VIP op twee nodes tegelijk |
| Onbekend | Geen agent | Geen agent op de actieve nodes |

Een VIP mag 15 seconden op twee nodes of op geen enkele staan voordat het telt, zodat een gewone wissel geen vals split-brain geeft. Nodes in onderhoud, draining, opbouw of uit dienst tellen niet mee voor het cluster. Elke statuswissel en elke VIP-verhuis komt in het [logboek](#logboek), en de webinterface werkt live bij.

Een node zonder agent of heartbeat die aan een Proxmox-VM gekoppeld is, krijgt zijn status van Proxmox: staat de VM uit, dan is de node Down met als reden "VM staat uit in Proxmox".

## Proxmox

ClusterForge praat met de REST-API van Proxmox VE (8 of nieuwer) via een API-token. Maak dat token één keer aan op een van je Proxmox-hosts, als root:

```sh
pveum role add ClusterForge --privs "VM.Audit VM.PowerMgmt VM.Snapshot VM.Migrate VM.Allocate VM.Clone VM.Config.CPU VM.Config.Memory VM.Config.Disk VM.Config.Network VM.Config.Cloudinit VM.Config.Options VM.Config.HWType VM.Config.CDROM VM.Monitor VM.Backup Sys.Audit Datastore.Audit Datastore.AllocateSpace SDN.Use"
pveum user add clusterforge@pve --comment "ClusterForge"
pveum acl modify / --users clusterforge@pve --roles ClusterForge
pveum user token add clusterforge@pve cf --privsep 0
```

Dat is de rol voor Proxmox VE 8. Op Proxmox VE 9 bestaat `VM.Monitor` niet meer; zet daar `VM.GuestAgent.Audit VM.GuestAgent.FileWrite` in de plaats, en geef voor de [diepe back-upcontrole](#diepe-controle-met-cf-agent-verify) `VM.GuestAgent.Unrestricted` alleen op de sandbox-pool. Bestaat de rol al uit een eerdere versie, vervang dan `role add` door `role modify` met dezelfde lijst. De rechten om VM's te maken en de guest agent te gebruiken zijn alleen nodig om [clusters uit te rollen](#clusters-uitrollen). `VM.Backup` en `Datastore.AllocateSpace` zijn nodig om de [back-ups](#back-ups) te lezen: zonder die rechten laat Proxmox ze stilzwijgend weg. Een rol uit een eerdere versie mist `VM.Backup`. `VM.Config.HWType` en `VM.Config.CDROM` zijn nodig om een teruggezette back-up af te sluiten bij de [back-upcontrole](#back-ups-controleren); een rol uit een eerdere versie mist ze.

Het laatste commando toont het secret één keer. Klik in de webinterface bij Proxmox op "Proxmox koppelen" en vul het API-adres (`https://pve1.example.lan:8006`), de token-id (`clusterforge@pve!cf`) en het secret in. Heeft Proxmox een zelfondertekend certificaat, klik dan naast de vingerafdruk op "Ophalen" en vergelijk de vingerafdruk met die op de host (`openssl x509 -in /etc/pve/local/pve-ssl.pem -noout -fingerprint -sha256`); ClusterForge vertrouwt daarna alleen dat certificaat. Het secret wordt versleuteld met `CF_MASTER_KEY` opgeslagen. Verlies je die sleutel, dan vul je het secret opnieuw in via Bewerken.

Is je Proxmox een cluster, dan is één koppeling genoeg: ClusterForge ziet via elke host alle hosts, VM's, containers en storage. Losse hosts koppel je elk apart.

Elke 20 seconden haalt ClusterForge de stand op. Bij Proxmox zie je de hosts met hun belasting, alle VM's en containers en de storage. Een VM koppel je aan een node met "Koppelen" (of maak er meteen een node van), waarna de node zijn VM-status, host en acties toont. Start, afsluiten, hard uitzetten, herstarten, snapshot maken en live migreren naar een andere host doe je vanaf de node of vanuit het overzicht; viewers kunnen alleen kijken. Verandert een gekoppelde VM buiten ClusterForge om (gestart, gestopt, verhuisd of verdwenen), dan komt dat in het logboek.

Wil je ook de CPU, het geheugen en de schijven van de Proxmox-hosts zelf in grafieken, zet dan ook daar de agent op.

Elke 10 minuten leest ClusterForge ook het netwerk van elke online host: bridges met hun poorten, bonds, VLAN-interfaces, adressen en de gateway. Je ziet het bij Proxmox onder Netwerk, per host, en op de [kaart](#kaart). Daar is geen extra recht voor nodig (`Sys.Audit`); SDN-zones leest ClusterForge niet.

**Koppelvoorstellen.** Heeft een VM of container hetzelfde MAC-adres als een node met een agent die nog aan geen VM hangt, dan stelt ClusterForge voor om ze te koppelen. Het voorstel staat bovenaan bij Proxmox en bij de node; een beheerder bevestigt of negeert het. Bevestigen koppelt de node aan de VM, net als Koppelen; een genegeerd voorstel komt niet terug. Templates en de sandbox-pool tellen niet mee.

## Back-ups

ClusterForge maakt zelf geen back-ups; dat blijven de back-upjobs van Proxmox (vzdump naar een storage of naar Proxmox Backup Server). Wel leest het elk kwartier welke back-ups er zijn en toont bij Back-ups per VM hoe oud de nieuwste is, op welke storage, hoe groot, en of Proxmox Backup Server hem geverifieerd heeft. Een VM is bewaakt als hij aan een node gekoppeld is, of als je hem op de pagina Back-ups bij "Ook bewaken" zet met zijn VMID en een label: zo houd je ook de container van ClusterForge zelf in het oog.

Is de nieuwste back-up ouder dan de maximale leeftijd, dan staat de VM op "Te oud"; zonder back-up op "Geen back-up". De maximale leeftijd is standaard 30 uur, wat past bij een dagelijkse back-upjob. Een admin stelt hem per cluster in, onderaan de kaart Back-ups op de clusterpagina; voor VM's op de lijst "Ook bewaken" geldt de standaard. Elke wissel komt één keer in het logboek, en de clusters en nodes tonen een back-upbadge. Daaronder staan de VM's en containers die in geen enkele back-upjob van Proxmox zitten. "Nu verversen" leest de back-ups meteen opnieuw, bijvoorbeeld na een back-upjob.

Toont Proxmox geen enkele back-up terwijl er VM's bewaakt worden, dan meldt ClusterForge dat één keer als leesfout in plaats van elke VM op "Geen back-up" te zetten: meestal mist het API-token dan `VM.Backup`. Staat een host uit, dan blijven de back-ups op zijn lokale storage staan zoals ze laatst gelezen zijn.

### Back-ups controleren

Een back-up die er is, is nog geen back-up die terug te zetten is. Nu controleren (op de pagina Back-ups) of Back-up controleren (op de node, met een andere back-up te kiezen) zet een back-up terug als tijdelijke VM, start hem, controleert hem en verwijdert hem altijd weer. De VM van de node zelf raakt ClusterForge daarbij niet aan. Maak eerst één keer de pool aan waarin die tijdelijke VM's komen, op een van je Proxmox-hosts als root:

```sh
pveum pool add cf-sandbox --comment "ClusterForge back-upcontrole"
pveum acl modify /pool/cf-sandbox --users clusterforge@pve --roles PVEPoolAdmin
```

De controle is een taak met zes stappen, en het rapport toont ze allemaal:

1. **Kiezen.** De nieuwste back-up, of de gekozen. Staat de back-up op Proxmox Backup Server of andere gedeelde storage, dan gaat de VM naar de host met het meeste vrije geheugen; anders naar de host met de back-up. De schijven komen op `CF_SANDBOX_STORAGE`, of op de storage voor VM-schijven met de meeste vrije ruimte. Zou die storage daarna voor meer dan 85 % vol zijn, of heeft de host niet het geheugen van de VM plus 1 GiB vrij, dan wordt de controle overgeslagen in plaats van je productie-VM's te laten vastlopen.
2. **Terugzetten** onder een nieuw VMID in pool `cf-sandbox`, nooit over een bestaande VM en zonder te starten.
3. **Isoleren.** Elke netwerkkaart krijgt `link_down=1`, opstarten bij boot en de bescherming gaan uit, en de VM krijgt de tag en het SMBIOS-serienummer `cf-sandbox`. Heeft de VM iets dat niet zeker af te sluiten is, zoals virtiofs, PCI- of USB-doorgifte of een schijf van de host, dan wordt hij niet gestart en meteen opgeruimd. ClusterForge leest de config terug en start alleen als elke regel klopt.
4. **Starten** en wachten tot de guest agent antwoordt, hoogstens `CF_SANDBOX_BOOT_TIMEOUT`. Zonder guest agent telt alleen dat de VM blijft draaien, met een waarschuwing.
5. **Controleren** via de guest agent: hostname, besturingssysteem en bestandssystemen, dan de [diepe controle](#diepe-controle-met-cf-agent-verify) met cf-agent in de sandbox, en dat de agent van de node niet ineens twee keer verbonden is.
6. **Opruimen**: hard uitzetten en verwijderen.

De uitslag leest bijvoorbeeld als "geslaagd. Terugzetten 3 min 12 s, opstarten 41 s. Hostname web01, Debian 13, 2 bestandssystemen. Sandbox-VM 131 verwijderd om 04:07." De hersteltijd (terugzetten plus opstarten) staat ook bij de node op de pagina Back-ups, met daaronder de geschiedenis van de controles. Er loopt hoogstens één back-upcontrole of failovertest tegelijk.

Een kopie van een node draagt dezelfde agentsleutel. Zolang een sandbox bestaat, weigert ClusterForge daarom een tweede verbinding met de sleutel van de bronnode, en komt die er toch, dan zet het de sandbox meteen hard uit en keurt de controle af. Op de pagina Proxmox heeft een sandbox geen actieknoppen en kan hij niet aan een node gekoppeld worden; starten en stoppen doet de controle zelf.

De tijdelijke VM verdwijnt altijd: na de controle, bij Afbreken, en na een herstart van de server midden in de taak (de run eindigt dan met "onderbroken door herstart van de server"). Lukt verwijderen niet, bijvoorbeeld omdat Proxmox de VM vergrendeld heeft, dan staat hij bij Sandboxes op de pagina Back-ups, komt er één regel in het logboek en probeert de opruimer het elke 5 minuten opnieuw; Opruimen doet het meteen. De opruimer raakt alleen VM's aan die ClusterForge zelf teruggezet heeft en die nog in pool `cf-sandbox` zitten.

Wil je de isolatie één keer met eigen ogen zien: open tijdens een controle in Proxmox de VM in pool `cf-sandbox`. Bij Hardware staat elke netwerkkaart op `link_down=1`, en in de console toont `ip link` de kaarten als `NO-CARRIER`. Containers (LXC) krijgen alleen versheid en dekking, want ze hebben geen guest agent.

### Diepe controle met cf-agent verify

Dat een VM opstart, zegt nog niet dat de website of de database erop werkt. Daarom start ClusterForge in de sandbox via de guest agent `cf-agent verify` met een aanvraag die zegt wat er moet draaien. Die aanvraag komt van ClusterForge zelf: de services die de template van het cluster start (met de vaste templateversie uit de gewenste staat), aangevuld met de services die op de productienode actief waren volgens de laatste facts. Er staat geen geheim in, en geen vrije opdracht of SQL: cf-agent voert alleen zijn eigen vaste controles uit en alleen op 127.0.0.1. Die controles zijn:

- **Services.** Elke verwachte service moet `active` zijn. Een service die niet bestaat of in de sandbox niet kan werken, zoals keepalived dat zijn VIP niet kan zetten ("Cannot assign requested address") of een Patroni- of Galera-node zonder de rest van het cluster, geeft een waarschuwing in plaats van een afkeuring.
- **Gefaalde units.** Units die na het opstarten gefaald zijn. De wait-online-units en `networking.service` falen in een sandbox zonder netwerk vaak en tellen niet mee; andere geven een waarschuwing.
- **Poorten.** De poort van elke service uit de template moet op 127.0.0.1 te bereiken zijn.
- **HTTP.** De HTTP-controles van de template, omgezet naar 127.0.0.1 met hetzelfde pad, moeten de verwachte status geven.
- **PostgreSQL en MariaDB.** Staat de database bij de services, dan moet hij antwoorden en zijn lijst met databases geven; bij PostgreSQL moet ook elke database open te maken zijn. Het rapport noemt alleen namen en aantallen, zoals "PostgreSQL met 3 databases", nooit inhoud.

Een afkeuring hier keurt de hele back-up af: "back-up afgekeurd: 1 controle mislukt. HTTP http://127.0.0.1/: gaf 503 in plaats van 200." Een controle duurt hoogstens een minuut, alles samen hoogstens vijf.

Op Proxmox VE 9 heeft het token daarvoor `VM.GuestAgent.Unrestricted` nodig. Geef dat alleen op de sandbox-pool, als root op een van je Proxmox-hosts:

```sh
pveum role add ClusterForgeSandbox --privs "VM.GuestAgent.Unrestricted"
pveum acl modify /pool/cf-sandbox --users clusterforge@pve --roles ClusterForgeSandbox
```

Op Proxmox VE 8 valt het onder `VM.Monitor`, dat al in de rol hierboven staat. Mist het recht, dan slaagt de controle met de waarschuwing "het API-token mag in de sandbox niets starten via de guest agent" en staan deze commando's in het rapport.

De diepe controle werkt alleen met een back-up waarin al een cf-agent met `verify` staat. In een oudere back-up slaagt de controle met de waarschuwing "cf-agent in deze back-up kent verify nog niet", en het rapport en de kaart Back-ups op de node tonen de drie stappen om dat te regelen:

1. Werk cf-agent bij op de node met het commando van de kaart Drift (`agent.sh --upgrade`); de aanmelding blijft staan.
2. Bouw de golden image opnieuw met `golden-image.sh --replace` en dezelfde `--vmid` en `--storage` als de eerste keer, zodat nieuwe VM's de nieuwe agent ook hebben.
3. Laat Proxmox een nieuwe back-up maken, klik op Back-ups op Nu verversen en controleer opnieuw.

De nieuwe agent herkent een sandbox aan het SMBIOS-serienummer `cf-sandbox`. Start hij daar, dan verbindt hij niet met ClusterForge en wacht hij tot de VM verwijderd wordt; zo praat een kopie van een node nooit mee als die node.

## Clusters uitrollen

Een template beschrijft een volledig cluster: hoeveel VM's, hoe groot, welke software erop komt en hoe ClusterForge achteraf controleert dat het werkt. De templates zitten in de server ingebouwd; bij Templates zie je ze met hun parameters en de diensten die ze in de graaf zetten. Er zijn er vier, beschreven onder [De templates](#de-templates).

Wat je nodig hebt:

1. Een gekoppelde Proxmox, met een token dat VM's mag maken (zie de rol hierboven).
2. Een golden image: een VM-template met Debian 13, cloud-init, de QEMU guest agent en cf-agent. Maak hem als root op een Proxmox-host met het script dat ClusterForge zelf serveert:

   ```sh
   curl -fsSL https://clusterforge.example/install/golden-image.sh | bash -s -- --server https://clusterforge.example --storage local-lvm
   ```

   Het script haalt het cloud-image van Debian, zet er qemu-guest-agent en cf-agent in met `virt-customize` (uit `libguestfs-tools`, dat het zo nodig installeert) en maakt VM-template 9000. Met `--vmid`, `--storage`, `--bridge` en `--name` kies je iets anders, en `--replace` vervangt een eerdere template. Op gedeelde storage (Ceph, NFS) verdeelt ClusterForge de VM's over de hosts; op lokale storage komen ze allemaal op de host van de template.
3. De nieuwe VM's moeten ClusterForge kunnen bereiken: het webadres om zich aan te melden, en poort 4222 voor NATS.

Klik bij Clusters op "Cluster uitrollen", kies de template en vul de naam, de parameters (zoals het VIP), de golden image en het netwerk in. Onderaan zie je meteen welke nodes er komen, met hun adres en Proxmox-host; een fout staat bij het veld. Na "Uitrollen" loopt de taak:

1. Per node de VM klonen, cores, geheugen, netwerk en cloud-init instellen, de schijf vergroten en starten.
2. De agents aanmelden: wachten op de guest agent, via de guest agent een eenmalig aanmeldbestand in de VM zetten (`/etc/clusterforge/enroll.json`) en wachten tot cf-agent zich meldt.
3. Per node de stappen van de template: pakketten, bestanden en services. Elke stap meldt of hij iets veranderde.
4. Controleren: een node heeft het VIP, en bij Nginx met keepalived en Eigen applicatie antwoordt het VIP over HTTP.
5. De nodes worden actief en tellen mee voor de status van het cluster.

Loopt een stap mis, dan staat bij de taak waarom. Los het op en klik op "Opnieuw proberen"; de taak gaat verder bij de stap die misliep. Wil je het cluster niet meer, verwijder dan het cluster en de nodes, en de VM's in Proxmox; ClusterForge ruimt in deze versie niets vanzelf op. Geheimen van een template, zoals het VRRP-wachtwoord, staan versleuteld met `CF_MASTER_KEY` in de database en nooit in de taak of het logboek.

Een eigen golden image kan ook: installeer cf-agent erin met `agent.sh --server https://clusterforge.example --no-enroll`. De agent meldt zich dan aan zodra ClusterForge het aanmeldbestand in de nieuwe VM zet. Zorg ook voor cloud-init en qemu-guest-agent, en maak `/etc/machine-id` leeg voor je er een template van maakt.

### De templates

Alle templates installeren hun software uit Debian 13, beheren alleen wat in hun stappen staat, en gebruiken geen commando's: elke stap is daardoor ook in de driftcontrole te zien. De drie met een VIP geven het met keepalived aan de node met de hoogste prioriteit waarop de bewaakte dienst draait, en na herstel keert het terug naar de eerste node.

- **Nginx met keepalived** (`keepalived-nginx`). Twee tot vijf webservers met Nginx achter één VIP. Je eigen site zet je in `/var/www/html`.
- **Docker-hosts** (`docker`). Een tot tien servers met `docker.io` en `docker-compose` (Compose v2, als `docker compose`) en een eigen `/etc/docker/daemon.json` met logrotatie (standaard 3 bestanden van 50 MB per container) en `live-restore`, zodat containers blijven draaien als Docker herstart. Je stacks zet je in `/opt/stacks`; welke containers er draaien, beheer je zelf, en die tellen niet mee voor drift. Er is geen VIP.
- **Cron-cluster** (`cron`). Twee tot vijf servers met cron en keepalived; de node met het VIP draait de taken. Zet op elke node dezelfde taken, met `cf-leader` ervoor, bijvoorbeeld in `/etc/cron.d/taken`:

  ```
  */5 * * * * root /usr/local/bin/cf-leader /usr/local/bin/opruimen.sh
  ```

  `cf-leader` voert het commando alleen uit als deze node het VIP heeft, en doet anders niets. Valt de node of cron uit, dan draaien de taken binnen enkele seconden op de volgende. Bij een split-brain hebben twee nodes het VIP en kan een taak dubbel draaien; de status van het cluster toont dat als split-brain. Je taken zelf beheert ClusterForge niet.
- **Eigen applicatie** (`generic`). Een container-image, zoals `ghcr.io/jonas/app:1.4.2`, op twee tot vijf servers achter één VIP. Elke node draait hem als systemd-dienst `cf-app` met `docker run`, met de poort op het VIP en de node naar de poort in de container. Instellingen zet je per node als `NAAM=waarde` in `/etc/cf-app/app.env` (alleen voor root leesbaar); daarna `systemctl restart cf-app`. Na de uitrol moet `http://<VIP>:<poort><pad>` 200 geven. Een image uit een registry met wachtwoord kan nog niet. Kan een node de image niet ophalen, dan start de container met de image die er al staat. Deze template heeft een agent van deze versie nodig, die `cf-app` volgt en systemd de nieuwe unit laat lezen; werk daarom eerst je golden image bij.

Een uitrol zet de diensten van de template meteen in de [afhankelijkheidsgraaf](#afhankelijkheden): docker als container, cron met keepalived, en app met docker en keepalived, met de poort die je koos.

### Gewenste staat

Elk cluster uit een template heeft een specificatie, de gewenste staat: de templateversie, de parameters, het netwerk en de nodes. Daarmee berekent ClusterForge op elk moment voor elke node opnieuw wat er hoort te staan, precies zoals bij de uitrol. Geheimen staan er niet in, alleen dat ze versleuteld opgeslagen zijn. Elke wijziging is een nieuwe revisie. Bij een cluster zie je onder Specificatie de huidige parameters en de historie, met per revisie wanneer ze gemaakt is, door wie, waarvandaan (webinterface, API of Git) en wat er veranderde. Wat niet klopt staat erboven: een node die niet meer bij het cluster hoort, een actieve node die niet in de specificatie staat, of een templateversie die deze server niet kent.

Templates hebben een versie, en een cluster blijft op de versie waarmee het uitgerold is. Een nieuwere ClusterForge brengt nieuwe versies mee naast de oude: een nieuw cluster krijgt de nieuwste, een bestaand cluster verandert pas als iemand zijn specificatie bewust wijzigt. Mist een versie die een cluster gebruikt, bijvoorbeeld na een downgrade, dan meldt de server dat bij het starten en bij het cluster.

In de commando's en paden van een template mag alleen iets komen waarvan de vorm vastligt: getallen, IP-adressen, groottes, ja of nee, strings met een pattern van alleen letters, cijfers en `. _ - : @ , + =`, de slug en omgeving van het cluster, en hostname, rol, index, adres en prefix van een node. Een template die daar iets anders gebruikt, laadt niet. Zo kan een parameter, ook een die later uit Git komt, geen eigen shellcommando op de nodes uitvoeren.

### Drift

Elk cluster heeft de kaart Drift. Daar zie je per node of hij nog klopt met de gewenste staat: in orde, drift, fout, geen verwachte staat (een node die niet in de specificatie staat) of overgeslagen. Klap een node open voor de afwijkingen per stap, zoals een met de hand aangepast configuratiebestand, andere rechten, een uitgeschakelde service of een ontbrekend pakket, met wat de template verwacht en wat er werkelijk staat. Bij een bestand staat hoe groot het is tegenover de template en wanneer het op de node veranderde, nooit de inhoud of een hash. In de clusterlijst en het overzicht staat een gele badge "Drift · 2 nodes", of een grijze "drift onbekend" als de laatste controle ouder is dan een uur.

De server controleert elke actieve node met een verbonden agent elk kwartier (`CF_DRIFT_INTERVAL`), opnieuw na een uitrol of een actie op een node, en meteen met de knop Nu controleren. Een nieuwe afwijking telt pas als ze er een halve minuut later nog is, zodat een korte herstart geen melding geeft; de knop slaat die tweede blik over. Nodes in onderhoud of met een lopende taak worden overgeslagen, en hun laatste uitkomst blijft staan. In het logboek komt alleen een overgang: drift gevonden, veranderd, verdwenen of een controle die mislukt. Een controle stuurt de agent alleen leesopdrachten (`dpkg-query`, `systemctl show`, `id` en het lezen van bestanden) en verandert nooit iets; herstellen doe je zelf, met Herstellen…. Een stap met `unless` wordt niet gecontroleerd, want die test is vrije shell.

Een cluster dat je met de hand aanmaakte, heeft geen template en dus geen gewenste staat. Daar legt de knop Baseline vastleggen er een vast, in drie stappen: kies de nodes (standaard alle actieve), kies welke pakketten, services en bestanden of mappen tellen (voorgevuld naar het soort cluster, zoals nginx met `/etc/nginx/nginx.conf`, plus de services die de agent op de nodes ziet), en bekijk per node wat er vastgelegd wordt voor je opslaat. ClusterForge onthoudt van een pakket dat het geïnstalleerd is (een nieuwere versie is geen drift), van een service of ze ingeschakeld is en draait, en van een bestand de rechten, eigenaar, groep, grootte en een HMAC van de inhoud met `CF_MASTER_KEY`. De inhoud zelf en een gewone hash ervan komen nergens: niet in de database, niet in het logboek en niet in de API. Zonder `CF_MASTER_KEY` legt een baseline geen bestanden vast. Een baseline is een revisie van de gewenste staat, net als een specificatie; na een bewuste wijziging leg je met Opnieuw vastleggen één node opnieuw vast en houden de andere hun baseline. Een actieve node zonder baseline staat bovenaan de kaart.

Een afwijking die zo hoort, negeer je met Negeren… bij de stap: met een reden, voor die node of het hele cluster, en eventueel tot een datum. Een regel geldt voor een hele stap, zoals `file:/etc/nginx/nginx.conf`, of met een `*` aan het eind voor alles met dat begin, zoals `file:/etc/nginx/*`. Alles negeren (`*`) kan alleen tijdelijk. De afwijking blijft grijs zichtbaar maar telt niet mee voor de status en de badges, en de kaart is meteen bijgewerkt. Onder Genegeerd staan alle regels met wie ze maakte; Opheffen laat de afwijking weer tellen. Een verlopen regel blijft in de lijst staan maar telt niet meer. Een regel maken of opheffen en een baseline vastleggen komen in het logboek.

Bij een cluster uit een template zet Herstellen… afwijkingen terug. Je kiest per node welke stappen, standaard alle afwijkende die niet genegeerd zijn, en ziet dan in gewone zinnen wat er gebeurt, in de volgorde van de taak: eerst de nodes zonder VIP, de VIP-eigenaar als laatste. Bijvoorbeeld "web-02: bestand /etc/keepalived/keepalived.conf overschrijven, daarna keepalived herladen". Een bestand wordt helemaal overschreven en een service ingeschakeld en gestart, dus wat er met de hand veranderd is, gaat verloren; genegeerde stappen blijven altijd staan. Op een prodcluster tik je ter bevestiging de slug in, en dat kan alleen met tweestapsverificatie aan. Daarna opent de taak met de live voortgang.

De taak (`cluster.apply`) bekijkt elke node vlak voor ze iets toepast opnieuw. Is er sinds je het venster opende weer iets veranderd, ook als iemand hetzelfde bestand nog eens aanpaste, dan stopt ze op die node zonder iets toe te passen. Heeft de node een VIP, dan gaat ze alleen door als een andere node met keepalived het kan overnemen. Na elke node wacht ze op twee gezonde heartbeats, één houder per VIP en de controles van de template (het VIP en de HTTP-controle). Lukt dat niet, dan stopt de taak en blijven de nodes daarna, met de VIP-eigenaar, ongemoeid. Een mislukt herstel start je opnieuw vanuit de drift van dat moment, nooit met Opnieuw proberen. Herstellen kan niet bij een baseline (van een bestand kent ClusterForge dan alleen een vingerafdruk) of als het lidmaatschap afwijkt van de specificatie. Het logboek toont wie het herstel vroeg, elk commando aan de agents onder de taak, en de drift die daarna verdween met de taak erbij.

Drift kan alleen met een agent van deze versie of nieuwer. Een oudere agent staat als "agent te oud voor driftcontrole", met het upgradecommando om te kopiëren. Werk ook je golden image bij (opnieuw `agent.sh --no-enroll` en de VM weer als template), anders krijgen nieuwe VM's de oude agent.

## GitOps

Een cluster uit een ingebouwde template kun je beschrijven in een GitHub-repository: per cluster een bestand `clusters/<slug>/cluster.yaml`. ClusterForge leest de repository elke minuut, controleert elk bestand en toont voor een wijziging wat ze op elke node zou doen. Pas als een beheerder het plan goedkeurt, verandert er iets op de nodes. ClusterForge schrijft zelf nooit naar Git.

Zo begin je:

1. Maak een repository, bijvoorbeeld `clusterforge-config`, en op GitHub een fine-grained token (Settings → Developer settings → Fine-grained tokens) met alleen die repository en alleen Contents: Read-only.
2. Koppel bij GitOps de repository (eigenaar/naam of de link), de branch (standaard `main`), de map (standaard `clusters`) en het token. Testen leest de laatste commit en de clusterbestanden zonder iets op te slaan. Het token wordt versleuteld met `CF_MASTER_KEY` bewaard en komt nooit terug uit de API; zonder masterkey staat GitOps uit. De server moet `api.github.com` op poort 443 kunnen bereiken. Vul ook het adres van ClusterForge in, zoals bij Uitrollen: daarmee melden nieuwe nodes uit Git zich aan, bij omhoog schalen en bij een nieuw cluster. Het formulier stelt het adres van de pagina voor.
3. Exporteer een cluster (bij GitOps onder "Clusters nog niet in Git", of op de kaart Git van het cluster) en commit het bestand ongewijzigd als `clusters/<slug>/cluster.yaml`.
4. Klik op Koppelen. Dat lukt alleen als het bestand gelijk is aan de export; anders zie je het verschil. Vanaf dan is Git de bron van waarheid: naam, beschrijving, omgeving, tags, VIP's, welke nodes erin zitten en hun IP-adres wijzig je in het bestand, en de API weigert ze met 409. Verwijderen kan pas na Ontkoppelen. Node-acties zoals onderhoud en herstarten blijven gewoon in ClusterForge.

Een bestand ziet er zo uit:

```yaml
clusterforge: 1
cluster:
  name: Webcluster
  slug: web                 # gelijk aan de mapnaam
  environment: prod
  tags: [web]
template:
  name: keepalived-nginx
  version: 1.1.0            # verplicht
params:                     # alle parameters behalve geheimen, zoals auth_pass
  vip: 10.0.20.100          # ligt vast
  node_count: 2
  vrid: 51                  # ligt vast
  cpu: 2                    # cpu, memory en disk liggen vast na de uitrol
  memory: 2G
  disk: 20G
target:                     # alleen gelezen bij een nieuw cluster
  proxmox: Thuislab         # de naam van de Proxmox-koppeling
  image_vmid: 9000
  first_ip: 10.0.20.11/24
```

Bij elk bestand staat bij GitOps een toestand: in sync, wijziging wacht, wordt toegepast, niet toegepast, afgewezen, ongeldig, nieuw, niet gekoppeld of ontbreekt. Een ongeldig bestand toont elke fout met veld en regel, zoals "regel 5, cluster.environment: kies lab, test of prod". De controle is streng: geen onbekende velden, de slug gelijk aan de map, een template en versie die de server kent, alle parameters ingevuld, en niets wijzigen wat vastligt (slug, templatenaam, VIP, VRRP-id en de VM-vorm). Een geheim in het bestand is een fout; ClusterForge bewaart zo'n bestand dan niet, en je haalt het geheim best ook uit de geschiedenis van de repository. Minder nodes dan nu kan niet via Git, daarvoor zijn de lifecycle-acties.

Een bestand voor een cluster dat nog niet bestaat, staat op "nieuw". Het plan toont dan de nodes met hun adres en VM-vorm, het VIP en de Proxmox-koppeling, met dezelfde controles als bij Uitrollen: vrije adressen, een vrij VIP en genoeg geheugen op de hosts. Ontbreekt `vrid`, dan kiest ClusterForge een vrije VRRP-id en zegt het plan welke; zet die daarna in het bestand, want na de uitrol ligt hij vast.

Een geldig bestand dat afwijkt van het cluster wordt een plan. Op de pagina van de wijziging zie je de commit (met of GitHub hem geverifieerd vindt), de velden oud en nieuw, en per node in de volgorde van toepassen welke stappen veranderen, met een uitklapbare diff; de VIP-eigenaar komt als laatste. Geheimen staan er als `[geheim]` in. Uit de laatste driftcontrole staat erbij wat op een node al afwijkt en overschreven zou worden. Per cluster wacht er hoogstens één plan; een nieuwere commit vervangt het.

### Goedkeuren en toepassen

Alleen een beheerder keurt een plan goed, ook in lab. Op prod moet hij tweestapsverificatie aan hebben en de slug van het cluster intikken; dat geldt ook als het cluster naar prod gaat. Goedkeuren leest eerst de laatste commit, controleert dat het plan nog klopt met het cluster en de nodes, en doet dan alles in één keer: het schrijft een nieuwe revisie van de gewenste staat met bron Git en de commit (te zien in de historie van het cluster, met een link naar GitHub), zet naam, beschrijving, omgeving en tags uit het bestand, en start een taak die de wijziging toepast. Die taak raakt alleen de stappen uit het plan, node voor node in de volgorde van het plan: eerst de nodes zonder VIP, de VIP-eigenaar als laatste. Na elke node wacht ze tot die gezond is, elk VIP één houder heeft en de controles van de template slagen, zoals de HTTP-controle van nginx. Lukt dat niet, dan stopt ze en blijven de nodes daarna ongemoeid. Verandert er op geen enkele node iets, zoals bij alleen andere tags, dan is de wijziging meteen toegepast, zonder taak. Een geheim dat een nieuwere templateversie erbij heeft, maakt ClusterForge zelf aan.

### Omhoog schalen en nieuwe clusters

Een hogere `node_count` geeft een plan met de nieuwe nodes en wat er op de bestaande verandert, zoals de extra peer in `keepalived.conf`. Een nieuwe node krijgt de laagste vrije index, want de keepalived-prioriteit volgt uit de index, en bij vaste adressen het adres dat bij die index hoort. Goedkeuren zet de nieuwe node meteen in de inventory, in provisioning, en start een taak die:

1. de VM kloont uit de golden image, op de host met het meeste vrije geheugen, en hem start;
2. via de guest agent het aanmeldbestand schrijft en wacht tot de agent zich meldt met zijn facts;
3. de bestaande nodes bijwerkt, eerst die zonder VIP en de VIP-eigenaar als laatste, met na elke node de gezondheidscontrole;
4. pas dan alle stappen van de template op de nieuwe node toepast. Hij telt mee voor de status zodra hij gezond is en elk VIP precies één houder heeft.

Die volgorde voorkomt split-brain: keepalived met `unicast_peer` hoort alleen peers die elkaar kennen. Startte de nieuwe node eerst, dan zou hij de VIP-eigenaar niet horen en het VIP ook nemen. Ook een nieuwe node die nog niet meetelt, mag geen tweede houder zijn; anders stopt de taak. Mislukt de taak, dan blijft de nieuwe node in provisioning staan, en Opnieuw toepassen op de kaart Git maakt hem af: een VM die al bestaat, wordt niet opnieuw gemaakt. Omhoog schalen uit Git kan alleen met vaste adressen. Met DHCP is het adres van de nieuwe node pas bekend als zijn VM draait, en daarmee wat er op de andere nodes verandert.

Een nieuw bestand goedkeuren maakt het cluster met spec-revisie 1, bron Git en de commit, koppelt het meteen aan het bestand en rolt het uit zoals Uitrollen: VM's maken, agents aanmelden, software installeren en controleren. Een geheim zoals `auth_pass` maakt ClusterForge zelf aan. Mislukt de uitrol, dan open je de taak en kies je Opnieuw proberen; lukt dat, dan staat de wijziging alsnog op toegepast.

Zonder het adres van ClusterForge bij de koppeling kan geen van beide; het plan zegt dat dan.

Afwijzen vraagt een reden. Het bestand blijft dan afgewezen tot een nieuwe commit het wijzigt, en er komt geen nieuw plan voor dezelfde inhoud; draai de commit in Git dus ook terug.

Terugdraaien doe je met `git revert`: de oude inhoud wordt een nieuw plan en volgt dezelfde weg. Mislukte een toepassing, dan staat het cluster op "niet toegepast": de kaart Git toont de gewenste en de toegepaste revisie, bijvoorbeeld "gewenste revisie 4, toegepast revisie 3", met de knop Opnieuw toepassen. Die past op elke node alle stappen van de template opnieuw toe, behalve wat genegeerd wordt. Een volgende goedgekeurde wijziging, zoals de revert, doet dat ook vanzelf zolang een eerdere revisie niet overal staat. De mislukte wijziging zelf blijft mislukt in de lijst.

Elke beslissing staat in het logboek met de commit en de auteur: goedgekeurd, afgewezen, toegepast of mislukt, en de commando's aan de agents staan onder de taak.

### Lezen

De server vraagt GitHub elke minuut alleen of de branch veranderde (met een ETag, dus zonder kosten als er niets nieuws is) en leest daarna alleen de bestanden die veranderden. Nu synchroniseren leest meteen opnieuw. Lukt het lezen niet, bijvoorbeeld door een verlopen token, dan staat de fout bij de koppeling en in het logboek, en blijft alles zoals het was.

## Failovertests

Een failovertest bewijst dat een VIP echt verhuist als de node die het heeft uitvalt. Op de pagina van een cluster staat de kaart Failovertests. Een beheerder voegt er een test toe: het scenario (keepalived stoppen op de eigenaar, nginx of haproxy stoppen op de eigenaar, of de VM van de eigenaar hard uitzetten via Proxmox), het VIP, binnen hoeveel seconden een andere node moet overnemen (standaard 5 s voor keepalived en 10 s voor een dienst of een VM), of het VIP daarna terug moet naar de oorspronkelijke node (standaard aan bij de templates met keepalived, die preempt gebruiken) en de probe: een HTTP-pad met de verwachte status of een TCP-poort op het VIP. Scenario's die op dit cluster niet kunnen, staan grijs met de reden.

Nu testen toont eerst wat er gaat gebeuren, bijvoorbeeld: "keepalived wordt gestopt op web01, nu eigenaar van 10.0.30.100. Verwacht: een andere node neemt binnen 5 s over. Lukt dat niet, dan is 10.0.30.100 hoogstens 20 s onbereikbaar; daarna zet ClusterForge keepalived weer aan." Daarna doet de server een strenge voorcontrole zonder uitweg: het cluster gezond, geen database op de nodes (een VIP dat naar een replica verhuist, kan schrijfacties laten mislukken), verse heartbeats, precies één eigenaar per VIP, een agent die deploystappen kan uitvoeren, de dienst draait, een reservenode met keepalived, geen andere taak in het cluster, geen andere test in heel ClusterForge en een probe die drie keer slaagt. Faalt er één, dan start de test niet en staan de controles in het venster.

De test zelf is een taak met vijf stappen. ClusterForge stopt de dienst op de eigenaar (een stop, nooit een disable), vraagt het VIP elke kwart seconde op en wacht tot een verse heartbeat een andere eigenaar meldt en de probe weer drie keer op rij slaagt. Daarna start hij de dienst weer, wacht tot de node gezond is en, als dat verwacht wordt, tot het VIP terug is. De uitslag is bijvoorbeeld "PASS: 10.0.30.100 3,4 s onbereikbaar, overgenomen door web02, daarna terug op web01, alles hersteld" of "FAIL: … meer dan de verwachte 5 s". Het rapport toont de test, de verwachting en het resultaat, een tijdlijn met de probe in groen en rood en markeringen voor de storing, de overname, het herstel en de terugkeer, en de stappen met hun log. Afbreken herstelt meteen.

De dienst komt altijd terug: na de meting, bij een fout, bij Afbreken, bij een nette stop van de server en na een crash, als de taak verder gaat. Lukt het herstel toch niet, dan staat bovenaan het cluster een rode balk "Failovertest niet volledig hersteld" met de knop Opnieuw herstellen. Elke test en run komt in het logboek, met het run-id bij elke regel en het stop- en startcommando aan de agent.

**De VM hard uitzetten** speelt een stroomonderbreking na: ClusterForge zet de VM van de eigenaar via Proxmox uit zonder hem af te sluiten, meet de overname en start de VM daarna weer; de terugkeer mag dan zo lang duren als het opstarten. Dat kan alleen in lab en test, als elke node met keepalived aan zijn VM gekoppeld is. De voorcontrole eist daarnaast dat de VM draait, niet onder Proxmox HA staat (de HA-manager zou hem zelf weer starten of verplaatsen) en dat er geen docker op draait, omdat containers met volumes data kunnen bevatten.

**Op prod** start een failovertest alleen met de hand: keepalived of een dienst stoppen, niet de VM. Net als bij Drift herstellen moet de beheerder tweestapsverificatie aan hebben en de slug van het cluster intikken. Het venster zegt ook wat er gebeurt als ClusterForge midden in de test wegvalt: het VIP blijft bereikbaar op de andere node, alleen is er dan geen reservenode tot ClusterForge terug is en herstelt.

### Planning

Een failovertest kan gepland draaien in het testvenster (`CF_TEST_WINDOW`, standaard zondag van 03:00 tot 05:00 op de klok van de server); zet het vinkje Gepland in het testvenster bij de test. Voor de back-upcontrole staat het vinkje per cluster op de kaart Back-ups, en elke run controleert de node die het langst niet gecontroleerd is. Planning staat standaard uit, en een geplande failovertest kan niet op prod. Gaat een cluster naar prod, dan wordt de volgende geplande run overgeslagen en gaat de planning uit.

Het testvenster volgt de klok, ook bij zomer- en wintertijd: "elke 7 dagen om 04:00" blijft om 04:00 op de klok, op 25 oktober 2026 dus een uur later in UTC. De planner kijkt elke minuut en start nooit twee tests tegelijk: loopt er al een failovertest of back-upcontrole, of een andere taak in het cluster, dan wacht de volgende. De voorcontrole loopt voordat er een taak is; lukt ze niet, dan komt er een overgeslagen run met de reden en de controles, zonder taak, en schuift de test naar het volgende venster. Hetzelfde geldt voor een run die meer dan 30 minuten te laat is (wachten op een andere test telt niet mee) of die buiten het venster zou vallen, bijvoorbeeld omdat ClusterForge niet draaide: dan geen inhaalrun midden op de dag, maar één overgeslagen run en de volgende in het eerstvolgende venster. De kaarten op de clusterpagina tonen "Niet getest sinds" en "Niet gecontroleerd sinds", in oranje na dertig dagen of als het nog nooit gebeurde.

## Afhankelijkheden

Bij Afhankelijkheden zie je welke diensten je draait en welke dienst van welke andere afhangt, over alle clusters heen. Een dienst hoort bij een cluster, bij een losse node zonder cluster, of is extern met een adres en een poort, zoals een NFS-server op 10.0.0.60:2049. Een pijl loopt van de afnemer naar de leverancier: php8.2-fpm in web-prod hangt af van mariadb in db-prod. Bij een harde afhankelijkheid is de afnemer down als de leverancier uitvalt, bij een zachte werkt hij verminderd.

Diensten komen op drie manieren in de graaf:

- **Uit een template.** Een cluster dat je uitrolt met Nginx met keepalived 1.1.0 krijgt nginx en keepalived, met nginx die hard afhangt van keepalived. Bestaande clusters uit die template kregen ze één keer bij de update. De andere templates zetten hun diensten er ook meteen in, zie [De templates](#de-templates).
- **Met de hand.** Een beheerder voegt bij Afhankelijkheden of op de pagina van een cluster diensten, externe diensten en afhankelijkheden toe. Zo komen ook met de hand gebouwde clusters en de verbanden tussen clusters erin.
- **Als voorstel.** Ziet een agent een bekende unit draaien op een actieve node van een cluster (nginx, apache2, haproxy, keepalived, mariadb, mysql, postgresql, redis-server, memcached, rabbitmq-server, php-fpm of docker), dan verschijnt die dienst binnen vijf minuten bij Voorstellen. Bevestig hem of negeer hem; een genegeerde dienst komt niet terug.

Elke dienst heeft een eigen status uit de heartbeats: gezond als zijn unit op alle actieve nodes van het cluster draait, verminderd als hij op een deel draait, down als hij nergens draait, en onbekend zonder unit, zonder agent of als extern. Valt een dienst uit, dan krijgen de diensten die er hard van afhangen een rode rand en die er zacht van afhangen een oranje, en kleurt de pijl naar de oorzaak rood. De graaf ververst zich vanzelf; een uitval staat er binnen een halve minuut in.

Klik op een dienst voor zijn instanties per node, wat hij gebruikt en wie hem gebruikt, en de knop Wat raakt uitval?. Die dimt alles wat niet geraakt wordt en toont per cluster welke diensten down of verminderd zouden raken, met prod bovenaan. Met Alleen clusters zie je de clusters met samengevoegde pijlen, en op een smal scherm een lijst per cluster. Het gaat altijd om bekende afhankelijkheden: wat niet in de graaf staat, telt niet mee, en de graaf houdt nooit een actie tegen. ClusterForge voert voor afhankelijkheden niets uit op de nodes.

Redis, memcached, rabbitmq, apache2, php-fpm en PostgreSQL-instanties ziet alleen een agent van deze versie of nieuwer. Zet hem erop met `--upgrade` (zie [Agent installeren](#agent-installeren-op-een-node)); met een oudere agent blijven die diensten onbekend.

### Impact bij acties

ClusterForge rekent na elke statusronde de status van elke dienst en de doorgegeven uitval opnieuw uit en bewaart ze. Verandert de status of de impact van een bevestigde dienst, dan komt er één regel in het logboek onder Afhankelijkheden, zoals "nginx in web-prod down door een afhankelijkheid: mariadb in db-prod is down, via php8.2-fpm", en bij herstel nog één. Een nieuwe dienst krijgt zijn eerste status zonder regel.

- **Geraakt door.** In het overzicht en de clusterlijst staat naast de status van een cluster "geraakt door db-prod" als een dienst van buiten uitval doorgeeft. De status van het cluster zelf verandert daar niet door.
- **Bevestigingsvensters.** Onderhoud, herstarten en afsluiten van een node, Nu testen bij een failovertest en het herstelplan van drift tonen welke bekende diensten down of verminderd raken als die node wegvalt. Een tweede node waarop de dienst draait of die het VIP kan overnemen, telt mee.
- **Verwijderen.** Het venster voor het verwijderen van een cluster of een losse node noemt de diensten van buiten die ervan afhangen. Die afhankelijkheden verdwijnen mee en staan daarna in de regel van het verwijderen in het logboek.

Ook hier gaat het om bekende afhankelijkheden: de vensters houden niets tegen, en een lege lijst betekent alleen dat ClusterForge geen afhankelijkheid kent.

## Onderhoud, herstarten en afsluiten

Bij elke node staat de kaart Beheer:

| Actie | Wat er gebeurt |
| --- | --- |
| Onderhoud | De agent zet keepalived uit (stop en disable), ClusterForge wacht tot de VIP's bij een andere node staan en zet de node dan in onderhoud. Hij telt niet mee voor de status van het cluster en keepalived blijft uit, ook na een herstart. |
| Onderhoud beëindigen | Keepalived komt terug zoals het voor het onderhoud stond, en de node is weer actief. |
| Herstarten | Eerst de VIP's weg en de node in onderhoud, dan herstarten, wachten tot de node terug is, keepalived weer aan en de node weer actief. |
| Afsluiten | Zoals herstarten, maar de node blijft uit en in onderhoud. Start hem in Proxmox of op de machine zelf en beëindig daarna het onderhoud. |

Kan geen andere node een VIP overnemen (geen actieve, online node waarop keepalived draait), dan vraagt ClusterForge eerst of je toch door wilt. Staan de VIP's na twee minuten nog niet ergens anders, dan zet de taak keepalived weer aan en blijft de node actief. Elke stap staat met het antwoord van de agent bij Taken, en elke wissel van lifecycle komt in het logboek, met de reden die je opgaf.

Een node zonder agent kun je alleen in en uit onderhoud zetten; de VIP's haal je dan zelf weg.

De agent voert alleen vaste soorten commando's uit: facts verzamelen, de toestand lezen voor de driftcontrole, keepalived uit- en aanzetten voor onderhoud, keepalived, nginx of haproxy stoppen en starten voor een failovertest, herstarten en afsluiten met `systemctl`, en de stappen van een template (pakketten met apt, bestanden, services, gebruikers, mappen en commando's). Na een gewijzigde unit onder `/etc/systemd/system` doet hij `systemctl daemon-reload`. Omdat hij als root bestanden schrijft, kan wie de server beheert alles op de nodes; bescherm de server en `CF_MASTER_KEY` daarom als een beheerwachtwoord. Hij onthoudt het onderhoud en het laatste herstartcommando in `/var/lib/clusterforge/agent-state.json`, zodat hij na een herstart niet nog eens herstart. Een agent van voor deze versie kan geen commando's uitvoeren; trek hem in en installeer hem opnieuw.

## Taken

Alles wat even duurt, zoals een VM migreren, een node herstarten of een cluster uitrollen, loopt als taak op de achtergrond. Bij Taken zie je wat er loopt en wat er gebeurd is, met per stap het logboek uit Proxmox. Een lopende taak kun je annuleren; ClusterForge stopt dan ook de taak in Proxmox. Valt de server weg tijdens een taak, dan gaat hij na de herstart verder waar hij was, zonder de actie in Proxmox nog eens te starten. Per cluster loopt hoogstens één taak die iets verandert, zoals een uitrol, een herstart of een VM-actie; een tweede krijgt de melding dat er al een taak loopt. Facts verversen telt niet mee.

## Logboek

Bij Logboek (alleen voor beheerders) staat alles wat er in ClusterForge veranderde, nieuwste eerst: wie het deed (een gebruiker, de agent op een node, of het systeem namens wie de taak aanvroeg), wanneer, vanaf welk IP-adres en wat er veranderde. Klik een regel open voor de oude en nieuwe waarde per veld, de taak en de ruwe gegevens. Je filtert op periode, persoon, soort, cluster, node (ook verwijderde) en tekst. De filters staan in de adresbalk, zodat je een gefilterde weergave kunt doorsturen. Exporteren geeft dezelfde selectie als NDJSON-bestand (één regel per gebeurtenis, tot 100.000 regels), en elke export komt zelf in het logboek. De pagina van een cluster, node of taak toont onderaan de laatste tien regels.

Elke regel draagt zijn herkomst: het IP-adres, de sessie (regels met dezelfde sessiecode komen uit dezelfde login) en de taak waar hij bij hoort. Elk commando dat ClusterForge naar een agent stuurt, zoals herstarten, keepalived uitzetten of een bestand schrijven, komt in het logboek voordat het vertrekt; lukt dat niet, dan gaat het commando niet weg. Van een bestand staan alleen het pad en een vingerafdruk, nooit de inhoud. Velden als wachtwoorden en geheimen staan er als "[verborgen]", en heel lange waarden worden ingekort. Mislukte aanmeldingen van agents en de loginlimiet komen per minuut hoogstens drie keer per IP-adres en tien keer in totaal in het logboek; de rest telt één samenvattende regel.

Regels kunnen niet gewijzigd of verwijderd worden, ook niet met `TRUNCATE` in de database. Een mislukte login bewaart de gebruiker en de reden, maar nooit wat er in het naamveld getypt werd: dat is soms een wachtwoord.

## Wijzigingen per node

De pagina van een node heeft een kaart Wijzigingen: wat er op die machine veranderde, nieuwste eerst, met een filter op soort en periode. Elke regel zegt hoe ClusterForge het weet: gezien door de agent, afgeleid door de server, uit Proxmox, of gedaan door ClusterForge zelf (met een link naar de taak).

- **De agent** (vanaf cf-agent met protocol 5) leest elke 30 seconden:
  - `/var/log/apt/history.log` en `/var/log/dpkg.log`: welk pakket van welke naar welke versie ging, en wie apt startte;
  - de systemd-units uit de heartbeat: een dienst die faalt, stopt, start of door systemd herstart wordt;
  - mtime, grootte, rechten en eigenaar van een vaste lijst onder `/etc` (keepalived, nginx, haproxy, MySQL, PostgreSQL, Redis, Apache, PHP, systemd-units, netwerk, `hosts`, `resolv.conf`, `fstab`, cron, sysctl, `sshd_config`, Docker) en `/opt/stacks`. Nooit de inhoud en geen hash;
  - een herstart aan een nieuwe boot-id, met de oude en nieuwe kernel;
  - SSH-logins (gebruiker, bron-IP en methode, nooit commando's) en de OOM-killer uit journald.
- **De server** ziet zelf een nieuwe agentversie, een onderbreking van de verbinding, en uit de facts de OS-versie, het aantal updates, interfaces, adressen (zonder de VIP's van keepalived), gateway, DNS, routes, luisterende poorten en de instellingen van keepalived. Voor een agent van voor het journaal leidt hij ook diensten en herstarts uit de heartbeats en facts af.
- **Proxmox**: de sync leest `/cluster/tasks`. Een VM die iemand in de Proxmox-webinterface stopt, migreert of terugzet, komt bij de node van die VM. Een Proxmox-host die offline of weer online gaat, komt in het logboek.

De agent bewaart wat hij nog moet sturen in `/var/lib/clusterforge/journal.json`, tot zeven dagen en 2000 regels, en stuurt het na een onderbreking alsnog. De tijden komen van de node en worden gecorrigeerd voor het verschil met de klok van de server. Een viewer ziet het IP-adres van een login niet, en met `CF_JOURNAL_SSH_LOGINS=false` bewaart de server geen logins. Het journaal wordt na 180 dagen opgeruimd, de Proxmox-taken en verbindingen na 30 dagen. Staat er een amberkleurige melding "Agent te oud voor het journaal", werk cf-agent dan bij met het commando eronder; de aanmelding blijft staan.

De facts tonen nu ook of een herstart nodig is (met de geïnstalleerde kernel), wanneer apt de lijsten voor het laatst bijwerkte, de soort, VLAN en MTU van elke interface, en de gateway en DNS-servers.

## Incidenten

Gaat er iets mis, dan maakt ClusterForge er een incident van: één storing met een begin, een einde, een ernst en wat er geraakt was. Een incident gaat open bij:

- een cluster dat verminderd, down of in split-brain gaat;
- een node zonder cluster die down gaat;
- een bevestigde dienst met afnemers (zie Afhankelijkheden) die down gaat;
- een geplande failovertest of back-upcontrole die mislukt of een fout geeft.

Wat bij elkaar hoort, komt in hetzelfde incident: een node bij het incident van zijn cluster, een dienst of mislukte test bij dat van zijn cluster zolang het cluster nog misgaat, en een andere overgang die binnen tien minuten na het begin valt en verwant is (hetzelfde cluster, een cluster dat via de afhankelijkheden van het andere afhangt, of een node op dezelfde Proxmox-host). Een incident sluit als alles vijf minuten gezond is, en gaat weer open als dezelfde storing binnen een kwartier terugkomt. Een node in onderhoud geeft geen incident.

De ernst volgt uit de omgeving: op prod is down of split-brain kritiek en verminderd hoog, al het andere is laag. Een incident wordt alleen erger, nooit minder erg. Wat gebeurt terwijl een failovertest van ClusterForge in hetzelfde cluster loopt (of tot twee minuten erna), krijgt de markering 'door een failovertest' en is standaard verborgen.

Bij Incidenten staan de open incidenten bovenaan, daarna de opgeloste, met filters op periode en status en de waarschijnlijke oorzaak in één regel. Een incident toont wat er misging (zie What Broke hieronder), wat er geraakt was (met de eerste, ergste en laatste status en van wanneer tot wanneer), de grafieken rond het eerste symptoom, de tijdlijn en een notitie. Het overzicht heeft een kaart Open incidenten. Iedereen ziet incidenten; alleen een beheerder schrijft de notitie, en een viewer ziet in de tijdlijn geen IP-adres. Openen, erger worden, sluiten, heropenen en de notitie staan ook in het logboek, onder de soort Incidenten.

Een VIP-wissel en de statuswissel van een dienst staan nu ook bij de nodes zelf in het logboek, niet alleen bij het cluster.

## What Broke

Bij elk incident zet ClusterForge op een rij wat er eerst misging, wat er vlak ervoor veranderde en wat daarvan de waarschijnlijke oorzaak is. Dat zijn vaste regels zonder AI: dezelfde gegevens geven altijd hetzelfde rapport, met de redenen erbij.

**De tijdlijn** voegt het logboek en het journaal van de nodes samen, met de afwijkingen in de metrics erbij. Elke regel is een verandering (die oorzaak kan zijn), een symptoom (wat er misging) of informatie. Een moment dat ClusterForge pas later ziet, krijgt een venster 'waarschijnlijk tussen': een node gaat pas na 90 s zonder heartbeat op down, Proxmox wordt elke 20 s gelezen, en drift elk kwartier.

**De analyse:**

1. **Eerste symptoom.** Het vroegste symptoom in wat geraakt was, rond het begin van het incident: een dienst die faalde in het journaal, een node of cluster dat slechter werd, een VIP zonder eigenaar.
2. **Kandidaten.** Elke verandering van twee uur voor het eerste symptoom tot een minuut erna, plus een OOM-kill en een afwijking in de metrics van het kwartier ervoor.
3. **Score.** Elke kandidaat krijgt een score uit:
   - de plaats: de node die misging telt het zwaarst, dan hetzelfde cluster, een cluster waar het van afhangt en dezelfde Proxmox-host, en elders het minst;
   - de tijd: elke tien minuten ervoor telt half zo zwaar;
   - de soort: een Git-wijziging, uitrol, pakketupdate of VM-actie het zwaarst, dan een taak, een gewijzigd bestand, een herstart en een SSH-login.
4. **Verbanden.** Vaste verbanden geven een bonus en een zin in het rapport:
   - het bijgewerkte pakket hoort bij de dienst die faalt (nginx bij nginx.service, mariadb-server bij mysql);
   - het gewijzigde bestand hoort bij die dienst (/etc/nginx/ bij nginx);
   - de OOM-killer trof het proces van de dienst;
   - de VM stopte in Proxmox voordat de node down ging;
   - de Proxmox-host ging offline voordat de VM's erop down gingen;
   - de schijf liep vol voordat de dienst faalde;
   - de VIP-eigenaar herstartte vlak voor het VIP geen eigenaar meer had;
   - de wijziging mislukte.
5. **Groeperen.** Regels van dezelfde taak zijn één oorzaak, met de Git-commit als die er is. Pakketten of bestanden op dezelfde node binnen twee minuten zijn één update.
6. **Uitkomst.** Hoogstens vijf oorzaken, elk waarschijnlijk, mogelijk of zwak verband, met de redenen. Veranderde er niets, dan zegt het rapport dat: 'ClusterForge zag in de twee uur ervoor geen verandering. De afwijkingen in de metrics staan hieronder; de oorzaak ligt waarschijnlijk buiten de nodes.'

Bijvoorbeeld: 'Waarschijnlijke oorzaak: nginx bijgewerkt van 1.26.0 naar 1.26.3 op web-01, 40 s voor het eerste symptoom', met als reden 'nginx op web-01 faalde 40 s na deze update van nginx'.

De afwijkingen in de metrics vergelijken het kwartier voor het eerste symptoom met de twee uur daarvoor, met vaste drempels: CPU boven 90 % en 30 punten hoger dan gemiddeld, geheugen boven 95 %, een schijf die boven 95 % komt, en netwerkfouten. Zonder VictoriaMetrics staat in het rapport dat de metrics niet te lezen waren.

De analyse rekent opnieuw zolang het incident open is en tot een uur na het einde, bijvoorbeeld als een journaal na een herverbinding binnenkomt. Daarna ligt ze vast. Ze leest alleen, neemt nooit het clusterslot en stuurt geen commando's.

**Wat is er veranderd.** Een cluster heeft een kaart met de veranderingen van de laatste 24 uur. De volledige tijdlijn van een cluster of node, met filters op periode en klasse, staat bij Tijdlijn; de kaart Wijzigingen van een node linkt ernaar. Een viewer ziet de tijdlijn ook, maar nooit iets uit de beveiliging en bij een SSH-login geen IP-adres.

## Gezondheid

De status zegt hoe het nu gaat. De health score zegt hoe kwetsbaar een cluster is: een cluster kan gezond zijn terwijl beide nodes op dezelfde Proxmox-host draaien, de laatste back-up twee dagen oud is en de failovertest al maanden niet liep. Elk cluster krijgt een score van 0 tot 100 en een cijfer: A vanaf 90, B vanaf 80, C vanaf 70, D vanaf 60, E vanaf 50, anders F. Het zijn vaste regels zonder AI, met de reden bij elke bevinding.

**Waar.** Het overzicht toont de score van het geheel met de trend over 30 dagen, het cijfer per cluster en de drie bevindingen die het meeste opleveren; de rest klapt open. De clusterlijst heeft een cijferbadge naast de status. Een cluster heeft een kaart Gezondheid met de zeven scorekaarten. Elke bevinding heeft een link Oplossen naar het scherm waar je het doet, en wat de score stijgt als je het oplost.

**De zeven kaarten.** Elke kaart begint op 100 en verliest punten per bevinding: laag 5, middel 10, hoog 20, kritiek 40, nooit onder 0. Op prod is een bevinding vaak een stap ernstiger, op lab een stap milder.

| Kaart | Weegt | Wat telt |
| --- | --- | --- |
| Beschikbaarheid | 25 % | nu down, split-brain of verminderd; de beschikbaarheid over 30 dagen (onder 99,9 %); veel incidenten; een VIP dat vaak van eigenaar wisselt (tests niet meegerekend) |
| Back-ups | 20 % | een node zonder back-up of met een te oude, volgens het back-upbeleid; een mislukte verificatie in Proxmox Backup Server; een back-upcontrole in de sandbox die mislukte, waarschuwde, nooit gebeurde of langer dan 30 dagen geleden is |
| Herstelbaarheid | 15 % | een VIP zonder failovertest; een test die nooit liep, mislukte, zijn doel niet haalde of langer dan 30 dagen geleden is; een test die niet hersteld is |
| Redundantie | 15 % | alle nodes op dezelfde Proxmox-host, of de nodes van een VIP; maar één node (niet op lab); een node die langer dan een dag in onderhoud staat; een prod-dienst die hard afhangt van een dienst op lab of test; een [single point of failure](#wat-als): een host, storage, bridge of node waarvan de uitval alleen al het prod-cluster of een bevestigde prod-dienst down legt |
| Configuratie | 10 % | geen gewenste staat; drift, zwaarder naarmate ze langer duurt; een regel die alle drift negeert; een revisie die al een uur niet toegepast is; een mislukte Git-wijziging |
| Onderhoud en beveiliging | 10 % | open beveiligingsupdates, zwaarder na 3 en 14 dagen; pakketlijsten die 14 dagen niet ververst zijn; een herstart nodig; een besturingssysteem dat end-of-life is of binnen 90 dagen wordt; een certificaat op de node dat binnen 30 dagen verloopt; een verouderde agent |
| Capaciteit | 5 % | een schijf boven 85 % of 95 %; een schijf die volgens de trend van 7 dagen binnen 14 dagen vol is; CPU of geheugen met een p95 boven 90 % over 7 dagen; veel swap; meer dan 1000 netwerkfouten in 7 dagen |

Een kaart zonder gegevens is onbekend en telt niet mee; de kaart zegt waarom, bijvoorbeeld 'Zonder VIP is er geen failovertest'. De trend, CPU en geheugen komen uit VictoriaMetrics; zonder VictoriaMetrics telt bij Capaciteit alleen de schijfbezetting.

**Plafonds.** Een cluster dat nu down is of een split-brain heeft, krijgt hoogstens 40. Een prod-cluster zonder enige back-up krijgt hoogstens 60.

**Het geheel.** De score van het geheel is voor 80 % het gewogen gemiddelde van de clusters (prod telt 3 keer, test 1 keer, lab half) en voor 20 % de kaart Platform. Die kaart gaat over ClusterForge zelf: een beheerder zonder tweestapsverificatie, een Proxmox-koppeling die niet te lezen is, het certificaat van Proxmox dat binnen 30 dagen verloopt, niet weten op welke VM ClusterForge zelf draait, en een VM van ClusterForge zonder back-up of met een back-up ouder dan 30 uur. Een viewer ziet dat een beheerder geen tweestapsverificatie heeft, maar niet welke.

**Accepteren.** Een bevinding die je bewust laat staan, zoals een labcluster op één host, accepteer je met een reden en eventueel een einddatum van hoogstens twee jaar. Ze kost dan geen punten meer, maar blijft grijs in de lijst staan met de reden, tot wanneer en door wie. Alleen beheerders accepteren of trekken in; beide komen in het logboek.

**Wanneer.** De score rekent elke minuut opnieuw, en meteen als een incident opent, verandert of sluit, en na een acceptatie. De eerste twee minuten na een herstart wacht ze, zodat de agents eerst opnieuw verbinden. Elke dag vlak na middernacht legt de planner een momentopname vast voor de trend. Verandert het cijfer van een cluster of van het geheel en blijft dat tien minuten zo, dan komt er een regel in het logboek met de belangrijkste redenen. De score leest alleen: ze start geen tests en blokkeert geen acties.

## Wat als

Bij Risico staan drie tabbladen: alle bevindingen van de health score, de single points of failure en de wat-als. De link naast de score op het overzicht brengt je er.

**Wat als.** Kies wat er uitvalt: een of meer Proxmox-hosts, storages, bridges, nodes, diensten of clusters, tot 20 tegelijk. ClusterForge rekent dan uit, zonder iets te doen:

- welke VM's en containers uitvallen: alles op een uitgevallen host, alles met een schijf op een uitgevallen storage, en alles waarvan de eerste of alle netwerkkaarten aan een uitgevallen bridge hangen (hangt alleen een andere kaart eraan, dan draait de VM door met minder netwerk);
- welke daarvan Proxmox HA op een andere host herstart: alleen als de host uitvalt, de VM in HA staat, al zijn schijven op gedeelde storage staan en een andere host genoeg vrij geheugen heeft (de grootste VM's eerst, elk naar de host met het meeste vrije geheugen);
- per cluster de status nu, bij de uitval en na de herstart door HA, met de reden, en naar welke node een VIP verhuist;
- welke diensten down of verminderd raken, ook via bekende afhankelijkheden, en welke terugkomen zodra HA de VM's herstart heeft;
- of ClusterForge zelf uitvalt.

De voorspelling gaat uit van de laatste Proxmox-sync, de status van de nodes en de bekende afhankelijkheden. Wat ze onzeker maakt, staat erbij: een VM waarvan de configuratie nog niet gelezen is, een koppeling die bij de laatste sync niet te lezen was, en dat HA-groepen en voorkeurshosts niet bekend zijn. Bridges komen uit het netwerk van de hosts en uit de netwerkkaarten van de VM's. Op de [kaart](#kaart) zie je de uitkomst ook: Op de kaart tonen kleurt wat uitvalt.

**SPOF's.** Een single point of failure is een host, storage, bridge of node waarvan de uitval alleen al een prod-cluster of een bevestigde prod-dienst down legt, ook nadat HA herstart wat het kan. ClusterForge rekent elk onderdeel apart, alsof de rest gezond is, en zet de onderdelen die het meeste down leggen bovenaan. Elk staat ook als bevinding Hoog op de kaart Redundantie van het cluster, met een link naar de wat-als; staan de nodes al allemaal op dezelfde host, dan komt er niet nog eens een bevinding voor die host bij.

**Configuratie van de VM's.** Daarvoor leest ClusterForge elke 10 minuten de configuratie van de VM's en containers: de schijven met hun storage, de netwerkkaarten met hun bridge en VLAN, en de vaste IP-adressen. Daar zijn geen extra Proxmox-rechten voor nodig (`VM.Audit`). Templates en de sandbox-pool slaat hij over.

**Waar ClusterForge zelf draait.** ClusterForge zoekt zijn eigen VM of container aan zijn MAC-adres en, als dat niet uniek is, aan zijn IP-adres. In Docker ziet hij zijn eigen MAC niet; dan zoekt hij aan het adres uit `CF_NATS_ADVERTISE` en het adres bij de Git-koppeling, tegen de vaste IP's van de VM's en de adressen van draaiende containers. Vindt hij het niet, kies de VM dan bij Instellingen onder "Waar ClusterForge draait".

## Kaart

Bij Kaart zie je hoe alles aan elkaar hangt, in drie lagen:

- **Fysiek.** Per Proxmox-host de VM's en containers met hun status, en de lokale storage. Gedeelde storage staat onder de hosts, met een lijn van elke VM naar de storage van zijn schijven. Een VM die een node is, krijgt de kleur van zijn cluster; de VM van ClusterForge zelf heeft het label CF.
- **Netwerk.** De subnetten met hun VLAN en de nodes erin, de bridges van de hosts met hun poorten (zoals `bond0 (eno1, eno2)`), de gateways, de VIP's met een lijn naar hun huidige eigenaar, en een stippellijn tussen VRRP-peers. Subnetten komen uit de adressen die de agents melden; een bridge hoort bij een subnet als de host er een adres in heeft, of als een node met dat netwerk via zijn netwerkkaart aan die bridge hangt.
- **Diensten.** De graaf van [Afhankelijkheden](#afhankelijkheden), met de voorstellen als stippellijn.

Filter op omgeving of cluster; dan blijft alleen staan wat bij die nodes hoort. Een klik op een element opent een paneel met de details, waar het mee verbonden is, een link naar zijn pagina en "Wat als dit uitvalt?". Op een smal scherm wordt de kaart een lijst.

Vanuit de wat-als (Op de kaart tonen) en vanuit een incident (Wat het raakte, op de kaart) opent de kaart met een overlay: wat uitvalt, down of verminderd is of door HA herstart wordt, krijgt een rand in die kleur, en de rest wordt gedimd. Op de kaart staan alleen nodes die aan een VM gekoppeld zijn (fysiek) of die een agent hebben (netwerk); wat ontbreekt, staat erboven.

## Assistent

Bij Assistent stel je vragen over de fleet in gewone taal, zoals 'Waarom lag web-prod gisteravond plat?' of 'Wat veranderde er deze week op db-01?'. De assistent zoekt het antwoord op met dezelfde gegevens als de webinterface en antwoordt in het Nederlands, met links naar de schermen. Op een cluster, node of incident start de knop "Vraag de assistent" een gesprek over dat onderwerp.

**Aanzetten.** De assistent gebruikt de Anthropic API met je eigen API-sleutel; zonder sleutel staat hij uit.

1. Zorg dat `CF_MASTER_KEY` gezet is: de API-sleutel wordt daarmee versleuteld en komt nooit terug uit de API.
2. Maak een API-sleutel op [console.anthropic.com](https://console.anthropic.com) (Settings, API Keys).
3. Ga naar Instellingen, Assistent. Plak de sleutel, kies een model en een maandbudget, zet de assistent aan en bevestig met je wachtwoord of je TOTP-code.
4. Klik op Testen: ClusterForge stelt een korte vraag zonder gegevens van de fleet en toont het model dat antwoordde.

De server moet `api.anthropic.com` op poort 443 kunnen bereiken. Elke wijziging komt in het logboek, zonder de sleutel.

**Modellen en kosten.** Je kiest Claude Opus 5.5 (standaard), Claude Sonnet 5.5 of Claude Haiku 5.5. Bij elk antwoord staan het model, de tokens en de kosten in dollar; Instellingen toont wat deze maand gebruikt is. De prijzen per miljoen tokens:

| Model | Invoer | Uitvoer |
| --- | --- | --- |
| Claude Opus 5.5 | $4 | $20 |
| Claude Sonnet 5.5 | $2 | $10 |
| Claude Haiku 5.5 | $0,10 | $0,50 |

Een vraag kost meestal enkele centen; een vraag die veel tools nodig heeft meer. Weigert Opus of Sonnet een vraag, dan probeert de API een ander model en staat dat model bij het antwoord.

**Grenzen.**

- Het maandbudget is standaard $20. Is het op, dan stelt niemand meer een vraag tot de volgende maand of tot een beheerder het verhoogt; een antwoord dat al loopt, gaat nog door.
- Iemand stelt hoogstens 30 vragen per uur, met elk hoogstens 4000 tekens.
- Per vraag mag het model 12 keer tools gebruiken; daarna stopt het en vraagt de assistent om een gerichtere vraag.
- Eén gesprek beantwoordt één vraag tegelijk.

**Wie.** Standaard alleen beheerders. Zet je 'Ook viewers mogen vragen stellen' aan, dan kunnen viewers het ook, met hun eigen rechten: ze zien via de assistent niets wat ze in de webinterface niet zien, zoals een IP-adres bij een SSH-login. Iedereen ziet zijn eigen gesprekken; een beheerder ziet die van iedereen. Een gesprek blijft 365 dagen na de laatste vraag staan, of tot je het verwijdert.

**Wat de assistent kan.** Alleen lezen. Hij heeft achttien tools: het overzicht van de fleet, een cluster, een node, de tijdlijn, de wijzigingen, incidenten zoeken, een incident met de analyse van What Broke, metrics, drift, back-ups, failovertests, taken, Git-wijzigingen, de impact van een storing, de health score, de wat-als met de single points of failure en de kaart. Hij kan niets starten, stoppen, herstarten, toepassen of wijzigen; een tool roept dezelfde leesopdrachten aan als de webinterface, met de rechten van wie de vraag stelt. Raadt hij iets aan, dan linkt hij naar het scherm waar jij het doet.

**Wat naar Anthropic gaat.** Je vraag, de gebruikersnaam en rol, de tijd, en wat de tools lezen: namen, statussen, adressen, tijdlijnen, journaalregels, pakketnamen, metrics, Git-commits en taken van de fleet. Geheimen gaan nooit mee: geen wachtwoorden, tokens, sleutels, vingerafdrukken, sessies en geen inhoud van bestanden. Commitberichten en journaalregels zijn tekst van buiten ClusterForge; de assistent behandelt ze als gegevens en volgt geen instructies die erin staan.

## Ontwikkelen

Nodig: Go 1.26, Node 22 met pnpm, Docker.

```sh
make dev-up            # PostgreSQL en VictoriaMetrics in Docker
make dev-admin USER=jonas
make dev-server        # Go-server op :8080
make dev-web           # Next.js op :3000, stuurt /api door naar :8080
make dev-github        # optioneel: een nep-GitHub op :8098 voor GitOps
```

Open http://localhost:3000.

| Taak | Commando |
| --- | --- |
| Tests (met integratietests tegen PostgreSQL) | `make test` |
| Lint en typecheck | `make lint` |
| Code opnieuw genereren na een wijziging in `api/openapi.yaml` of `internal/store/queries` | `make generate` |
| Binary met ingebedde webinterface | `make build` |
| `cf-agent` voor amd64 en arm64 (in `bin/agents`, voor `make dev-server`) | `make agent` |
| Container-image | `make docker` |

Voor GitOps zonder echte GitHub start `make dev-github DIR=/tmp/cf-config`: een nep-GitHub die de inhoud van die map als repository toont en opnieuw commit zodra er iets verandert. Koppel dan met API-adres `http://127.0.0.1:8098`, repository `jonas/cf-config` en token `dev-token`.

De ingebouwde templates staan in `internal/templates/builtin/<naam>/<versie>/`. Wat een uitgebrachte versie op de nodes zet, verander je niet meer, want clusters kunnen ze gebruiken: kopieer de map naar een nieuwe versie, pas die aan en zet hem in de lijst `released` in `internal/templates/registry_test.go`. De test faalt als een uitgebrachte versie verdwijnt of een nieuwe er niet in staat.

### Indeling

```text
cmd/clusterforge-server/   main: serve, migrate, admin create
cmd/cf-agent/              de agent op de nodes: enroll, run, facts
internal/auth/             wachtwoorden (argon2id), TOTP, sessies
internal/httpapi/          HTTP-handlers; gen/ is gegenereerd uit api/openapi.yaml
internal/store/            sqlc-queries; queries/ is de bron
internal/events/           append-only eventlog
internal/inventory/        clusters, nodes en VIP's: validatie en wijzigingen met events
internal/agents/           enrollmenttokens, aanmelden en intrekken van agents
internal/agentbus/         ingebedde NATS-server: authenticatie per node, heartbeats en facts
internal/agentdist/        installatiescript en downloads van cf-agent
internal/agent/            code van cf-agent zelf: aanmelden, verbinden, facts, metrics en het journaal verzamelen
internal/journal/          het wijzigingsjournaal: zinnen, verschillen in de facts en opruimen
internal/incidents/        incidenten: overgangen groeperen, ernst, openen en sluiten na elke statusronde
internal/whatbroke/        What Broke: de tijdlijn uit logboek en journaal, en de analyse van elk incident
internal/assistant/        de AI-assistent: instellingen, gesprekken, de lus met de Anthropic API, kosten en budget
internal/risk/             de health score: de zeven scorekaarten, de kaart Platform, acceptaties en de trend; de wat-als en de SPOF's
internal/topo/             één model van hosts, storage, bridges en VM's met hun schijven en netwerkkaarten, waar ClusterForge zelf draait, en de lagen van de kaart
internal/metrics/          metrics naar VictoriaMetrics schrijven en de grafieken opvragen
internal/status/           statusregels voor nodes en clusters, VIP-eigenaars
internal/live/             live updates naar de webinterface (Server-Sent Events)
internal/proxmox/          Proxmox-API: koppelingen, sync elke 20 s, VM-acties, het netwerk van de hosts en koppelvoorstellen; pvefake/ is een nep-Proxmox voor tests
internal/jobs/             taken op de achtergrond met stappen, logboek en annuleren
internal/lifecycle/        acties op nodes via de agent: onderhoud, herstarten, afsluiten
internal/secrets/          versleutelen van geheimen met de masterkey
internal/templates/        ingebouwde templates per versie, parameters en de controle op onveilige waarden
internal/deploy/           clusters uitrollen, de gewenste staat en haar revisies
internal/audit/            het logboek: lezen, filteren, beschrijven en exporteren
internal/backups/          versheid van de Proxmox-back-ups per VM, de back-upcontrole in een sandbox en de opruimer
internal/drift/            driftcontrole: vergelijken met de gewenste staat, de scanner en het rapport
internal/health/           wachten op verse heartbeats: een node klaar, de VIP's op hun plaats
internal/failover/         failovertests: voorcontrole, storing, meting, herstel en het rapport
internal/gitops/           GitOps: de repository lezen, clusterbestanden controleren, plannen en koppelen; ghfake/ is een nep-GitHub
internal/deps/             diensten en afhankelijkheden: graaf, status, doorgeven, impact en voorstellen
internal/rollout/          cluster.apply: wijzigingen node voor node toepassen, zoals drift herstellen
internal/planner/          het testvenster, zomer- en wintertijd, en wanneer een geplande run start of overgeslagen wordt
pkg/protocol/              berichten tussen server en agent
internal/webui/            ingebedde webinterface
migrations/                goose SQL-migraties
api/openapi.yaml           API-specificatie, bron van waarheid
web/                       Next.js-app (static export)
deploy/                    Dockerfile en compose-bestanden
```

Een release maak je met een tag (`git tag v0.1.0 && git push --tags`); GitHub Actions bouwt dan de image `ghcr.io/jonasz1996/clusterforge-server` voor amd64 en arm64.
