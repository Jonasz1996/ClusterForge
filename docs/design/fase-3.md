# ClusterForge – Technisch ontwerp fase 3

Stand: 2026-10-11

## Samenvatting

Fase 1 laat zien wat er draait en rolt clusters uit. Fase 2 laat zien of het nog klopt en bewijst dat met tests. Fase 3 legt uit. Jonas krijgt antwoord op zes vragen: wat ging er kapot en waardoor, hoe gezond is alles en waar zit het risico, hoe hangt alles in het netwerk aan elkaar, waar staat de actuele documentatie, overleeft een cluster het uitvallen van een Proxmox-host of het verlies van al zijn VM's, en kan ik dat allemaal gewoon vragen.

Alles blijft in dezelfde Go-binary, met dezelfde agent, PostgreSQL, VictoriaMetrics en eventtabel. Er komt geen nieuw proces in de stack. De enige nieuwe uitgaande verbindingen zijn de Anthropic API voor de assistent en GitHub voor de documentatie, en die staan allebei uit tot Jonas ze aanzet. Fase 3 telt zes modules in vijftien mijlpalen.

| Module | Wat Jonas krijgt | Mijlpalen |
| --- | --- | --- |
| What Broke | Bij elke storing een incident met één tijdlijn: wat er eerst misging, wat er vlak ervoor veranderde en een gerangschikte lijst waarschijnlijke oorzaken met de reden erbij. Een oorzaak kan een taak of commit zijn, een pakketupdate, een herstart, een gewijzigd bestand, een SSH-login, een VM-actie in Proxmox of een piek in de metrics. Een regel luidt bijvoorbeeld 'nginx op web-01 viel 40 s na de upgrade van nginx 1.26.0 naar 1.26.3 uit'. | 1, 2, 3 |
| Risicoanalyse en health score | Een score van 0 tot 100 en een cijfer A tot F per cluster en voor alles samen. Die is opgebouwd uit zeven scorekaarten, met per aftrekpunt de oorzaak en een link naar de oplossing. Daarnaast een lijst single points of failure en een wat-als: kies een Proxmox-host, storage, bridge, node of dienst en zie wat er uitvalt. | 5, 6 |
| Infrastructure mapping | Een kaart op `/kaart` met Proxmox-hosts, storage, bridges en VLAN's, VM's, nodes, VIP's, diensten en de verbindingen ertussen. Verbindingen die de agent ziet, worden voorgestelde afhankelijkheden die Jonas bevestigt of negeert. | 1, 7, 8 |
| Auto-documentatie | Per cluster en voor het geheel actuele documentatie in Markdown: architectuur, netwerk, diensten, configuratie, back-ups, tests en runbooks met de gemeten hersteltijden. Te lezen in de webinterface, te downloaden en af te drukken, en desgewenst automatisch naar een eigen GitHub-repository. | 9, 10, 14 |
| Disaster simulations | In lab en test: een Proxmox-host laten uitvallen door alle VM's van het cluster op die host hard te stoppen, een dienst wegnemen waar andere clusters van afhangen, en netwerkstoringen met een dodemansknop in de agent. Voor elk cluster, ook prod, een DR-repetitie: het hele cluster uit de back-ups terugzetten in een afgesloten netwerk, controleren en weer opruimen, met de gemeten RTO en RPO. | 11, 12, 13, 14 |
| AI-assistent | Een chat op `/assistent` voor vragen als 'waarom was web-prod vannacht down?' of 'wat gebeurt er als pve2 uitvalt?', met links naar de schermen. Hij leest via vaste tools dezelfde gegevens als de webinterface, voert nooit iets uit en analyseert op verzoek een incident. Hij werkt met Jonas' eigen Anthropic API-sleutel en een maandbudget. | 4, 15 |

Na elke mijlpaal is er iets bruikbaars. Na mijlpaal 1 ziet Jonas per node wat er veranderde, ook buiten ClusterForge om. Na mijlpaal 3 krijgt elke storing een tijdlijn met een waarschijnlijke oorzaak, en na mijlpaal 4 kan hij daar vragen over stellen. Na mijlpaal 6 weet hij waar zijn single points of failure zitten en na mijlpaal 8 hoe alles aan elkaar hangt. Na mijlpaal 9 heeft hij documentatie die niet veroudert, en na mijlpaal 13 weet hij hoe lang het duurt om een heel cluster uit de back-ups terug te halen.

**Uitgangspunten**

- **Eerst vaste regels, dan AI.** What Broke, de health score, de SPOF-lijst, de kaart en de documentatie komen uit regels in Go die te testen zijn, met de redenen erbij. Alles werkt zonder API-sleutel. De assistent legt uit, zoekt en vat samen, maar zijn antwoord is nooit de enige bron: elke bewering linkt naar het scherm waar ze vandaan komt.
- **De assistent leest alleen.** Geen enkele tool verandert iets. Wil Jonas na een antwoord iets doen, dan linkt de assistent naar het bestaande scherm, waar de gewone bevestiging geldt, op prod met slug en TOTP.
- **Niets gaat naar buiten tot Jonas het aanzet.** De assistent staat uit tot er een sleutel is, en documentatie naar GitHub staat uit tot er een repository en een token zijn. Wat er naar de Anthropic API gaat, staat in de instellingen. Geheimen, bestandsinhoud en vingerafdrukken gaan nergens heen; dat geldt in fase 3 ook voor wat de assistent te zien krijgt.
- **Eerst lezen, dan storen.** Mijlpaal 1 tot en met 10 lezen alleen. Live storingen komen pas vanaf mijlpaal 11 en blijven beperkt tot lab en test. De DR-repetitie mag ook voor prod, omdat ze in een afgesloten netwerk draait en productie nooit raakt.
- **Hergebruik van fase 2.** Storingen gaan via het testslot, `test_runs`, het clusterslot, de gezondheidspoort, OnFinished en het sandbox-register. De wat-als rekent op de afhankelijkheidsgraaf, en ontdekte afhankelijkheden komen als voorstel in dezelfde tabellen `services` en `service_dependencies`.
- **Twee protocolverhogingen.** Protocol 5 (mijlpaal 1) brengt alles wat de agent extra leest: het wijzigingsjournaal, netwerkfacts en verbindingen. Protocol 6 (mijlpaal 12) brengt `fault.inject` met de dodemansknop. Oudere agents blijven werken. Ze missen alleen die gegevens, en de webinterface zegt dat.
- **Een mens beslist.** Er is geen automatisch herstel, geen automatisch toepassen en geen geplande storing op prod. Wat vanzelf gebeurt, is lezen, rekenen en documentatie schrijven, plus een AI-analyse per incident als Jonas dat aanzet.

## Wat fase 2 al klaarzet en wat ontbreekt

Fase 2 is gebouwd tot en met mijlpaal 17, en veel van wat fase 3 nodig heeft, bestaat al.

- **Events.** Een append-only tabel met 105 actions in een catalogus, met per action een Nederlandse zin (`internal/audit/describe.go`). De herkomst (IP, sessie, taak) staat in de payload, en `node_ref` en `job_ref` maken zoeken per node en taak goedkoop. Statusovergangen van nodes, clusters, VIP's en diensten staan erin met van, naar en reden. Elk agentcommando dat iets verandert, staat erin als `agent.command`.
- **Taken.** Alle taken met hun stappen en logs, het clusterslot, het testslot, OnFinished, de planner met het testvenster, en de gezondheidspoort (`internal/health/health.go`).
- **Tests.** `test_runs` met uitslag, tijdlijn in milliseconden en metingen. Verder de failovertest met vier lagen om altijd terug te zetten, de probes op het VIP, en de sandbox met register, allowlist, opruimer en `cf-agent verify`.
- **Afhankelijkheden.** Een graaf met harde en zachte afhankelijkheden, impact met oorzaak en pad, en `services.source` en `state` die al `discovered` en `suggested` kennen (`migrations/00015_dependencies.sql`).
- **Proxmox.** Elke 20 s een spiegel van hosts, gasten en storage met `pve_node`, `hastate` en `shared`, en events bij een VM die stopt, verhuist of verdwijnt.
- **Metrics.** VictoriaMetrics met 90 dagen node_exporter-reeksen per node, met labels voor node, cluster en omgeving.
- **Geheimen.** `secrets.Box` met HKDF-afgeleide sleutels, het patroon van het versleutelde GitHub-token, en gemaskeerd renderen met `[geheim]`.
- **Webinterface.** `@xyflow/react` en `@dagrejs/dagre` zitten er al in voor de afhankelijkheidsgraaf, en de testpagina toont een probe-tijdlijn.

De verkenning van de code vond ook gaten. De meeste zijn dicht voordat de module die ze nodig heeft, iets toont.

| Gat | Gevolg | Opgelost in |
| --- | --- | --- |
| Wijzigingen buiten ClusterForge zijn onzichtbaar. Er is geen pakketgeschiedenis, `node.facts_changed` bewaart alleen namen van sleutels, en `node_facts` alleen de laatste stand (`internal/agentbus/handlers.go:189-193`). | Een `apt upgrade` of een met de hand aangepast bestand vlak voor een storing is niet te vinden. | Mijlpaal 1: het journaal |
| Dienstovergangen die de status van de node niet veranderen, laten geen spoor na. `node_status` wordt elke 10 s overschreven (`internal/status/evaluator.go:210-225`). | 'Wanneer begon nginx te falen' is achteraf niet te beantwoorden. | Mijlpaal 1: dienstovergangen in het journaal |
| Proxmox-taken en het uitvallen van een host worden niet bewaard; alleen gekoppelde gasten geven events (`internal/proxmox/sync.go:175-212`). | Een VM die in de Proxmox-webinterface stopte of een host die uitviel, ontbreekt in elke uitleg. | Mijlpaal 1 |
| Het subject `cf.node.<id>.events` staat in het protocol maar wordt nergens gebruikt (`pkg/protocol/protocol.go:52`). | Geen gat, maar een kans: het journaal heeft geen nieuwe NATS-rechten nodig. | Mijlpaal 1 |
| De tijd in de envelope van de agent wordt genegeerd (`internal/agentbus/handlers.go:39-56`). | De klokafwijking van een node is onbekend, en tijden uit de node zijn niet te corrigeren. | Mijlpaal 1 |
| Facts hebben geen routes, gateway, DNS, soort interface, VLAN, master of MTU, en van keepalived alleen de VIP's (`pkg/protocol/protocol.go:138-143`, `internal/agent/collect.go:308-352`). Luisterende poorten worden alleen in de sandbox gelezen. | Een netwerkkaart van bestaande clusters is niet te tekenen, en VRRP-peers zijn onbekend. | Mijlpaal 1 (facts), mijlpaal 7 (kaart) |
| Er is geen `reboot_required`, geïnstalleerde kernel, leeftijd van de apt-lijsten of vervaldatum van certificaten. | Beveiliging en onderhoud zijn maar half te beoordelen. | Mijlpaal 1 (facts), mijlpaal 5 (score) |
| `vip.owner_changed` en `service.status_changed` hebben geen `node_id` in de payload, dus ze vallen buiten het filter op node. | Een tijdlijn per node mist VIP-wissels. | Mijlpaal 2: nieuwe payloads met `node_ids`, oude lezen op cluster |
| Metrics zijn alleen op te vragen als vast paneel dat op 'nu' eindigt (`internal/httpapi/monitoring.go:39-58`). | Een incident van gisteren heeft geen grafiek van dat moment. | Mijlpaal 3 |
| De detailweergaven worden in de handlers opgebouwd (`internal/httpapi/inventory.go:163`). | De assistent en de documentatie zouden de regels voor geheimen en rollen opnieuw moeten bouwen. | Mijlpaal 4: `internal/views` |
| Er is geen tabel voor instellingen van de server, en geen Markdown in de webinterface. | Een API-sleutel heeft geen plek, en antwoorden zijn niet leesbaar te tonen. | Mijlpaal 4 |
| Staplogs bevatten de uitvoer van agentcommando's zonder filter (`internal/deploy/render.go:315-317`). | Een staplog kan in een toolresultaat naar buiten gaan. | Mijlpaal 4: standaard weggelaten, anders ingekort en gefilterd |
| Er is geen score of scorekaart; `status/rules.go` noemt er een, maar er is er geen (`internal/status/rules.go:1-3`). | Groen zegt niets over hoe kwetsbaar een cluster is. | Mijlpaal 5 |
| De drempel van 30 dagen voor een oude test staat alleen in de webinterface (`web/src/lib/failover.ts:148`). | Score en webinterface kunnen het oneens zijn. | Mijlpaal 5: drempels in `internal/risk` |
| Op welke storage de schijven van een VM staan, wordt niet bewaard (`internal/proxmox/provision.go:52-71`). | 'Allebei op de lokale schijf van pve1' is niet te zien. | Mijlpaal 6 |
| ClusterForge weet niet op welke VM of host hij zelf draait. | Een rampsimulatie zou zijn eigen host kunnen stoppen. | Mijlpaal 6 |
| `deps.Impact` kent één doel en geen Proxmox-host (`internal/deps/impact.go:12-16`). | Een host-uitval is niet te voorspellen. | Mijlpaal 6 |
| Bridges en VLAN's zijn alleen bekend voor clusters uit een template, via de spec (`internal/deploy/deploy.go:131-146`). | De kaart en de DR-repetitie kennen het netwerk van bestaande clusters niet. | Mijlpaal 6 (gastconfiguratie), mijlpaal 7 (netwerk per host) |
| De Proxmox-sync leest geen netwerkkaarten van gasten en geen netwerk van hosts (`internal/proxmox/sync.go:129-133`). | Welke VM op welke bridge en in welk VLAN hangt, is onbekend. | Mijlpaal 6 en 7 |
| Een VM die niet door ClusterForge is uitgerold, wordt alleen met de hand aan een node gekoppeld. | Bestaande VM's ontbreken op de kaart en in de wat-als. | Mijlpaal 7: voorstel op MAC-adres |
| Afhankelijkheden met bron `discovered` zijn in het schema toegestaan, maar niemand maakt ze (`internal/deps/resolver.go:140-153`). | De graaf kent alleen wat in templates staat of met de hand is ingevoerd. | Mijlpaal 8 |
| Er is geen laag die documenten bouwt of bewaart, en geen schrijvende GitHub-client: `ghfake` weigert alles behalve GET (`internal/gitops/ghfake/ghfake.go:167`). De README belooft dat ClusterForge nooit naar Git schrijft (`README.md:289`). | Documentatie naar GitHub vraagt een nieuwe client en een eerlijke nieuwe belofte. | Mijlpaal 9 en 10 |
| Een failoverplan heeft één doel, en het clusterslot en het testslot kennen elk één cluster en twee soorten (`internal/failover/service.go:638-653`, `migrations/00013_failover.sql:53-54`). | Een host-uitval over meerdere clusters past er niet in. | Mijlpaal 11 |
| De uitslag van een failovertest gaat uit van een overname (`internal/failover/run.go:809-815`). | 'VIP valt weg' zou altijd FAIL geven. | Mijlpaal 11: uitslag met verwachte uitval |
| De sandbox heeft bewust geen netwerk, één sandbox per run, en een capaciteitscontrole per VM (`internal/backups/isolate.go:268-277`). | Een heel cluster samen terugzetten kan niet. | Mijlpaal 13 |

## Gedeelde bouwstenen

Tien mechanismen worden één keer gebouwd en door meerdere modules gebruikt. De modulehoofdstukken noemen ze bij naam. Ze staan hier in de volgorde waarin ze gebouwd worden.

### Agentprotocol 5: alles wat de agent extra leest

Fase 3 heeft van de agent vooral meer gegevens nodig. Die komen in één protocolverhoging, zodat Jonas in mijlpaal 1 één keer bijwerkt met `install.sh --upgrade`. Protocol 5 voegt toe:

- **Het wijzigingsjournaal**, als berichttype `changes` op het subject `events` (zie What Broke).
- **Netwerkfacts.** Per interface de soort (fysiek, vlan, bridge, bond, veth, wireguard), de ouder of master, het VLAN-id, de MTU en de adressen met prefix. Verder de routes met de standaardgateway, de DNS-resolvers en de luisterende poorten met protocol, adres, poort en unit. Ze komen uit `/sys`, `/proc` en `/etc`, zonder externe commando's. Nu kent een interface alleen naam, MAC, up en adressen (`pkg/protocol/protocol.go:138-143`).
- **Verbindingen**, als berichttype `conns`: elke minuut een steekproef van de gevestigde TCP-verbindingen uit `/proc/net/tcp`, elke vijf minuten samengevat (zie Infrastructure mapping).
- **Meer van keepalived:** per `vrrp_instance` ook interface, VRID en unicast-peers.
- **Facts voor de risicoanalyse:**
  - `reboot_required`;
  - de nieuwste geïnstalleerde kernel;
  - de leeftijd van de apt-lijsten;
  - de vervaldatum van publieke certificaten.
- **Een extra heartbeat bij een adreswissel.**

Alle nieuwe velden zijn optioneel. Een agent op protocol 4 werkt door, en elke module toont 'agent te oud' waar een gegeven ontbreekt, met het commando om bij te werken.

Komt in mijlpaal 1. Gebruikt door What Broke, de risicoanalyse, de kaart en de assistent.

### Wijzigingsjournaal

Eén tabel `node_changes` met alles wat er op een node veranderde, uit de agent, de server, Proxmox en ClusterForge zelf, elk met een Nederlandse zin. Het journaal is geen logboek. Het logboek zegt wat ClusterForge en zijn gebruikers deden, is append-only en wordt altijd bewaard. Het journaal zegt wat er op de machines gebeurde, en wordt na 180 dagen opgeruimd.

Komt in mijlpaal 1. Gebruikt door What Broke, de risicoanalyse ('al 9 dagen open'), de assistent en de documentatie (wijzigingen per maand).

### Proxmox-sync uitgebreid

De sync leest nu alleen `/version` en `/cluster/resources` (`internal/proxmox/sync.go:68-74`). Fase 3 voegt toe, elk met een eigen interval:

| Gegeven | Bron | Interval | Vanaf |
| --- | --- | --- | --- |
| taken (wie stopte, migreerde of maakte een back-up) | `/cluster/tasks` | 20 s | 1 |
| hosts online en offline als event | `/cluster/resources` (type node) | 20 s | 1 |
| gastconfiguratie: schijven, netwerkkaarten, HA | `/nodes/{node}/qemu/{vmid}/config` | 10 min | 6 |
| certificaat van Proxmox | TLS-handshake | 1 u | 6 |
| netwerk per host: bridges, bonds, VLAN's, adressen | `/nodes/{node}/network` | 10 min | 7 |

Dat kan allemaal met de rechten die de rol al heeft (`Sys.Audit`, `VM.Audit`). De client krijgt de methoden `Tasks`, `GuestConfigs` en `HostNetwork`, achter dezelfde smalle interface, en `pvefake` speelt ze na.

Gebruikt door What Broke, de risicoanalyse, de kaart, de rampsimulaties en de documentatie.

### Metricsvenster

`metrics.Client.QueryWindow(start, eind)` vraagt een vast PromQL-sjabloon op over een willekeurig venster. Er komen ook vaste risicoqueries bij: p95 van CPU en geheugen over 7 dagen, swapgebruik, netwerkfouten en een lineaire trend van het schijfgebruik. Net als nu stuurt de browser of de assistent nooit zelf PromQL (`internal/metrics/query.go:42-44`). Zonder VictoriaMetrics geven ze 'onbekend'.

Komt in mijlpaal 3, de risicoqueries in mijlpaal 5. Gebruikt door What Broke, de risicoanalyse en de assistent.

### Tijdlijn

`whatbroke.Timeline(scope, van, tot)` voegt events, het journaal, taken, specrevisies, Git-wijzigingen, drift, testruns, Proxmox-taken en metricsafwijkingen samen tot één lijst. Elke regel heeft een klasse (verandering, symptoom, info), een venster 'waarschijnlijk tussen' en een link. De eventcatalogus krijgt per action een klasse, en een test faalt als een action er geen heeft.

Komt in mijlpaal 3. Gebruikt door What Broke, de assistent en de documentatie.

### Leesmodellen buiten de handlers

De detailweergaven worden nu in de HTTP-handlers opgebouwd (`internal/httpapi/inventory.go:163`, `internal/httpapi/deps.go:141`). Fase 3 haalt ze eruit naar een pakket `internal/views`, met functies als `views.Cluster(ctx, id, rol)`. De API, de tools van de assistent en de documentatie gebruiken dan dezelfde weergave, met dezelfde regels voor geheimen en rollen. De handlers worden dunner, en hun antwoord verandert niet.

Komt in mijlpaal 4. Gebruikt door de assistent, de documentatie en de kaart.

### Topologie

`internal/topo` is één model van de hele infrastructuur, opgebouwd uit de database:

- Proxmox-hosts met hun storage en bridges;
- gasten met hun schijven en netwerkkaarten;
- nodes, clusters, VIP's en subnetten;
- diensten, afhankelijkheden en waargenomen verbindingen.

Gasten en nodes worden aan elkaar gekoppeld op VMID, en netwerkkaarten aan interfaces op MAC-adres. Het model rekent niet zelf; de wat-als, de kaart en de documentatie lezen het.

Komt in mijlpaal 6 (hosts, storage, gasten) en mijlpaal 7 (netwerk en verbindingen). Gebruikt door de risicoanalyse, de kaart, de rampsimulaties, de documentatie en de assistent.

### Markdown in de webinterface

Antwoorden van de assistent en de documentatie zijn Markdown. De webinterface krijgt één renderer met een beperkte set: koppen, alinea's, lijsten, tabellen, code en links. Links mogen alleen naar ClusterForge zelf; geen HTML, afbeeldingen of externe links. Mermaid-diagrammen in de documentatie worden als diagram getoond.

Komt in mijlpaal 4. Gebruikt door de assistent en de documentatie.

### Versleutelde instellingen

De assistent bewaart een API-sleutel, de documentatie een GitHub-token met schrijfrecht. Beide volgen het patroon van het GitOps-token (`internal/gitops/service.go:197-211`):

- één rij met een unieke index op `(true)`;
- `secrets.Box` met een AAD gebonden aan de rij;
- een leeg veld houdt de oude waarde;
- de API geeft het geheim nooit terug;
- het event zegt alleen 'gewijzigd';
- zonder `CF_MASTER_KEY` staat de module uit, met de reden.

Komt in mijlpaal 4. Gebruikt door de assistent en de documentatie naar GitHub.

### Testslot, clusterslot en sandboxes voor meer dan één

Een rampsimulatie raakt soms meerdere clusters, en een DR-repetitie maakt meerdere sandboxes. Daarom:

- `jobs.EnqueueForClusters` vergrendelt alle geraakte clusters in één transactie, gesorteerd op id (`internal/jobs/jobs.go:210-236`);
- het testslot kent de soorten `disaster.run` en `dr.rehearsal`;
- het sandbox-register staat meerdere sandboxes per run toe.

De vangnetten blijven dezelfde:

- het tijdstip van de storing vastleggen vóór het commando;
- Detach;
- de emergency-defer;
- OnFinished;
- de rode balk met Opnieuw herstellen.

Komt in mijlpaal 11 (slots) en mijlpaal 13 (sandboxes). Gebruikt door de rampsimulaties.

## What Broke

Bij een storing wil Jonas in één scherm zien wat er eerst misging, wat er vlak ervoor veranderde en wat daarvan de waarschijnlijke oorzaak is. Vandaag moet hij dat zelf bij elkaar zoeken in het logboek, de taken, drift, de metrics en Proxmox, en een deel ziet ClusterForge helemaal niet: een `apt upgrade` via SSH, een bestand dat met de hand is aangepast, of een VM die in de Proxmox-webinterface is gestopt.

What Broke bestaat uit drie lagen. Het wijzigingsjournaal legt vast wat er op elke node en in Proxmox verandert, ook buiten ClusterForge om (mijlpaal 1). Incidenten groeperen de statusovergangen tot één storing met een begin en een einde (mijlpaal 2). De analyse zet per incident de tijdlijn en de waarschijnlijke oorzaken op een rij (mijlpaal 3). Alles is vaste logica in Go. De assistent gebruikt dezelfde tijdlijn, maar What Broke heeft hem niet nodig.

### Hoe het werkt

**Het wijzigingsjournaal.** De tabel `node_changes` krijgt één regel per verandering, met een Nederlandse zin, het tijdstip op de node en de bron. Er zijn drie bronnen van buitenaf, en ClusterForge zelf.

- **De agent (protocol 5).** De agent stuurt zijn wijzigingen op het subject `cf.node.<id>.events`, dat al in het protocol staat en dat de NATS-rechten van de agent al toelaten (`pkg/protocol/protocol.go:52`). Hij leest:
  - pakketten uit `/var/log/dpkg.log`: installeren, bijwerken met oude en nieuwe versie, en verwijderen. Uit `/var/log/apt/history.log` komt welke gebruiker het commando gaf;
  - een herstart, aan een nieuwe `boot_id`, met de nieuwe kernel als die anders is;
  - per gevolgde dienst een herstart of een mislukking, uit `NRestarts`, `Result` en `ActiveEnterTimestamp` van `systemctl show`, dat hij bij elke heartbeat toch al aanroept;
  - bestanden: mtime, grootte, mode en eigenaar van een vaste lijst onder `/etc` (keepalived, nginx, haproxy, mysql, postgresql, redis, systemd-units, netwerk, `hosts`, `resolv.conf`, `fstab`, cron, sysctl en `sshd_config`), plus de paden uit de template of baseline van het cluster. Nooit de inhoud, en ook geen hash;
  - SSH-logins uit journald: gebruiker, bron-IP en methode, zonder commando's;
  - OOM-kills uit de kernellog: welk proces.

  De agent bewaart een cursor in `/var/lib/cf-agent/journal.json` en stuurt na een herverbinding wat er sindsdien gebeurde, met een maximum van zeven dagen en 500 regels. De server ontdubbelt op node, soort, sleutel en tijdstip.
- **De server zelf.** Dit werkt ook met agents op protocol 4. Uit de heartbeats komen diensten die van toestand wisselen, met 15 s debounce. Uit de facts komen oude en nieuwe waarden van kernel, OS-versie, boottijd en het aantal openstaande updates; nu bewaart `node.facts_changed` alleen de namen van de sleutels (`internal/agentbus/handlers.go:189-193`). Verder het verbinden en wegvallen van de agent en een nieuwe agentversie. De klokafwijking van de node komt uit het tijdstip in de envelope, dat de server nu negeert.
- **Proxmox.** De sync leest naast `/cluster/resources` ook `/cluster/tasks` in `proxmox_tasks`: UPID, host, soort, VMID, gebruiker, start, einde en exitstatus. Een VM die `root@pam` in de Proxmox-webinterface stopte, een migratie of een mislukte vzdump wordt zo een regel bij de node van die VM. Een Proxmox-host die offline of weer online gaat, geeft `proxmox.host_offline` en `proxmox.host_online`; nu geeft alleen een gekoppelde gast een event (`internal/proxmox/sync.go:175-212`).

Wat ClusterForge zelf op een node verandert, komt er ook in. `ApplySteps` schrijft per stap die iets aanpaste een regel met bron `clusterforge` en de `job_id`. Vandaag staat dat alleen als tekst in de staplog (`internal/deploy/render.go:228-270`). Zo staat in het journaal naast elkaar wat ClusterForge deed en wat er met de hand gebeurde.

**Incidenten.** Een detector draait als enkele lus met een advisory lock, zoals de evaluator, en wacht de opwarmtijd van 30 s na een herstart af. Hij reageert op de statusevents via de live-hub en opent een incident bij:

- een cluster dat van healthy naar degraded, down of split_brain gaat;
- een losse node, zonder cluster, die down gaat;
- een bevestigde dienst met afnemers die down gaat;
- een geplande test (failover, back-upcontrole, DR) die fail of error geeft.

Een nieuwe overgang komt bij een open incident als ze binnen 10 minuten na het begin valt en verwant is: hetzelfde cluster, een cluster dat via de afhankelijkheidsgraaf van het andere afhangt, of nodes op dezelfde Proxmox-host. Een incident sluit als alles 5 minuten gezond is, en gaat opnieuw open als binnen 15 minuten dezelfde storing terugkomt. Een node in onderhoud geeft geen incident. Een overgang tijdens een lopende test van ClusterForge in hetzelfde cluster geeft een incident met de markering 'door test', dat standaard verborgen is.

De ernst volgt uit omgeving en status: prod down of split_brain is kritiek, prod degraded is hoog, en de rest is laag. Een incident wordt nooit hoger dan zijn ergste overgang en nooit lager zolang het open is.

**De analyse.** `whatbroke.Timeline(scope, van, tot)` voegt alle bronnen samen tot één lijst: events op `cluster_id` en `node_ref`, het journaal, taken met hun stappen, specrevisies, Git-wijzigingen, driftbevindingen met hun mtime, `test_runs`, Proxmox-taken en afwijkingen in de metrics. Elke action in de eventcatalogus krijgt een klasse, verandering, symptoom of info, zodat de tijdlijn weet wat oorzaak kan zijn en wat gevolg is.

1. **Eerste symptoom.** Het vroegste symptoom, gecorrigeerd met de bekende vertraging van zijn bron. Een node gaat pas na 90 s zonder heartbeat op down (`internal/status/rules.go:28-29`), dus het echte begin is de laatste heartbeat of het laatste metrics-sample. Drift wordt pas na 15 min gezien, maar de mtime van het bestand zegt wanneer het veranderde. Elk tijdstip krijgt zo een venster 'waarschijnlijk tussen' in plaats van één schijnbaar exact moment.
2. **Kandidaten.** Alle veranderingen van twee uur voor het eerste symptoom tot een minuut erna.
3. **Rangschikking.** Elke kandidaat krijgt een score uit drie factoren:
   - *Afstand in de infrastructuur.* Dezelfde node telt het zwaarst. Daarna komen hetzelfde cluster, een leverancier via de afhankelijkheidsgraaf (`internal/deps/graph.go:225-254`) en dezelfde Proxmox-host of storage. Elders telt het minst.
   - *Afstand in tijd.* Een halveringstijd van 10 minuten.
   - *Soort.* Een taak, commit of uitrol, een pakketupdate en een VM-actie wegen het zwaarst. Daarna komen een gewijzigd configuratiebestand, een herstart en een SSH-login.

   Vaste verbanden geven een bonus en een zin in het rapport:
   - het bijgewerkte pakket hoort bij de dienst die faalt (`nginx` bij `nginx.service`);
   - het gewijzigde bestand hoort bij die dienst (`/etc/nginx/` bij nginx);
   - de VM stopte in Proxmox voordat de node down ging;
   - de Proxmox-host ging offline voordat meerdere VM's down gingen;
   - de schijf liep vol voordat de dienst faalde;
   - een OOM-kill trof het proces van de dienst;
   - de VIP-eigenaar herstartte vlak voor het VIP zonder houder.
4. **Uitkomst.** Hoogstens vijf oorzaken, elk als 'waarschijnlijk', 'mogelijk' of 'zwak verband', met de redenen en links. Is er niets veranderd in het venster, dan zegt het rapport dat ook: 'ClusterForge zag in de twee uur ervoor geen verandering. De afwijkingen in de metrics staan hieronder; de oorzaak ligt waarschijnlijk buiten de nodes.'
5. **Bereik.** Welke clusters en diensten geraakt waren en hoe lang, uit de overgangen en de impact van de afhankelijkheidsgraaf.

De metrics krijgen een venster: `metrics.Client.QueryWindow(start, eind)` met vaste PromQL-sjablonen voor CPU, geheugen, volle schijf, netwerkfouten, boottijd en kernelversie. Nu eindigt elke query op 'nu' en kan alleen 1 u, 6 u, 24 u of 7 d (`internal/metrics/query.go:26`). Een eenvoudige change-point-detectie vergelijkt het kwartier voor het symptoom met de twee uur daarvoor, en meldt alleen afwijkingen boven een vaste drempel.

De analyse is deterministisch en heeft een versienummer. Zolang het incident open is en tot een uur erna rekent ze opnieuw als er gegevens bijkomen, bijvoorbeeld een journaal dat na een herverbinding binnenkomt. Daarna ligt ze vast.

**Wat is er veranderd.** Los van incidenten heeft elk cluster en elke node een feed 'Wat is er veranderd' over een gekozen periode, uit dezelfde tijdlijn. Dat is vaak de eerste vraag als iets vreemd doet maar nog niet rood is.

### Datamodel

| Tabel | Inhoud |
| --- | --- |
| `node_changes` | `id`, `node_id`, `cluster_id` op het moment zelf, `ts` (tijd op de node, gecorrigeerd voor de klokafwijking), `observed_at`, `source` (agent, server, proxmox, clusterforge), `kind` (package, boot, service, file, login, oom, facts, network, agent, vm), `subject` (pakket, unit, pad, gebruiker), `summary`, `detail` jsonb, `job_id`. Uniek op node, soort, sleutel en tijdstip. Bewaard 180 dagen, opgeruimd door de planner. |
| `proxmox_tasks` | `upid`, `connection_id`, `pve_node`, `type`, `vmid`, `user`, `status`, `exitstatus`, `started_at`, `ended_at`. De laatste 30 dagen. |
| `incidents` | `id`, `number` (voor '#12'), `title`, `severity`, `status` (open, resolved), `started_at`, `detected_at`, `resolved_at`, `caused_by_test`, `test_run_id`, `analysis` jsonb, `analysis_version`, `analyzed_at`, `note`, en voor mijlpaal 15 `ai_analysis`, `ai_model`, `ai_at`. |
| `incident_subjects` | `incident_id`, `subject_type` (cluster, node, service, test_run), `subject_id`, `first_status`, `worst_status`, `since`, `until`. |

`events` blijft append-only. Incidenten en analyses staan in eigen tabellen, en het journaal staat apart omdat het een bewaartermijn heeft en het logboek alleen over ClusterForge zelf gaat. Nieuwe events: `incident.opened`, `incident.escalated`, `incident.resolved`, `incident.reopened`, `incident.note_updated`, `proxmox.host_offline` en `proxmox.host_online`, allemaal in de catalogus met een zin.

### Agent

Protocol 5 voegt een berichttype `changes` toe op het bestaande subject `events`. De agent leest alleen bestanden en roept alleen `systemctl show` en `journalctl` aan, zonder shell. Een server zonder fase 3 negeert het subject. Een agent op protocol 4 stuurt niets, en dan toont de node 'agent te oud voor het journaal' met het commando om bij te werken. De regels uit de server en uit Proxmox komen er dan wel.

Protocol 5 brengt ook de netwerkfacts en verbindingen voor de kaart (zie Infrastructure mapping) en een extra heartbeat bij een adreswissel. Fase 2 schoof die laatste door. De agent kijkt elke seconde naar zijn adressen en stuurt bij een wijziging meteen een heartbeat, hoogstens één per seconde. Een VIP-wissel staat dan binnen een seconde in de server in plaats van binnen tien.

### API

- `GET /api/v1/nodes/{id}/changes?from&to&kind` en `GET /api/v1/clusters/{id}/changes?from&to&kind`: het journaal.
- `GET /api/v1/clusters/{id}/timeline?from&to` en hetzelfde voor nodes: de samengevoegde tijdlijn.
- `GET /api/v1/incidents?status&cluster&from&to`, `GET /api/v1/incidents/{id}` met analyse, tijdlijn en bereik, en `PUT /api/v1/incidents/{id}/note` (admin).

Een viewer ziet incidenten, het journaal en de tijdlijn, maar zonder de beveiligingsevents en zonder IP-adressen en sessies uit de herkomst. Het logboek zelf blijft alleen voor admins.

### Schermen

- **Node, tab Wijzigingen.** Het journaal met filters op soort en periode, en bij elke regel een link naar de taak of het Proxmox-event.
- **Clusterdetail, kaart 'Wat is er veranderd'.** De laatste 24 uur, met een link naar de volledige tijdlijn.
- **`/incidenten`.** Open incidenten bovenaan, daarna de afgesloten, met duur, ernst en waarschijnlijke oorzaak in één regel. Het overzicht krijgt een kaart Open incidenten.
- **Incident.** Bovenaan status, duur, ernst en wat er geraakt is. Daaronder de waarschijnlijke oorzaken als kaarten met hun redenen, en de tijdlijn met een markering bij het eerste symptoom, filters op soort en de vertraging per bron als band. Kleine grafieken tonen het venster uit VictoriaMetrics. Verder een notitieveld en, vanaf mijlpaal 15, de knop Analyseer met de assistent.

### Veiligheid

- What Broke leest alleen. Het journaal bevat nooit bestandsinhoud of hashes, en een SSH-login alleen gebruiker, bron-IP en methode.
- Een `apt`-commandoregel kan een wachtwoord in een URL bevatten. De agent bewaart daarom alleen het eerste woord en de pakketnamen, en de server haalt elke `user:pass@` uit een URL.
- Regels uit het journaal zijn invoer van de node en dus niet te vertrouwen. De webinterface toont ze als tekst, en ze sturen nooit een actie aan.
- De detector en de analyse nemen nooit het clusterslot en sturen geen commando's.

### Mijlpalen

- **Mijlpaal 1:**
  - het journaal, met protocol 5 voor de agent;
  - waarnemingen van de server;
  - de Proxmox-taken en hosts;
  - de tab Wijzigingen.
- **Mijlpaal 2:** incidenten, `/incidenten` en de kaart op het overzicht.
- **Mijlpaal 3:**
  - de tijdlijn en de analyse;
  - het metricsvenster;
  - 'Wat is er veranderd' per cluster en node.

## AI-assistent

Jonas wil een vraag kunnen stellen in plaats van vijf schermen af te lopen: 'waarom was web-prod vannacht down?', 'welke nodes hebben al een week beveiligingsupdates open?', 'wat gebeurt er als pve2 uitvalt?'. De assistent beantwoordt die vragen in het Nederlands, met links naar de schermen waar het antwoord vandaan komt. Hij leest alleen, via vaste tools over dezelfde gegevens als de webinterface, en voert nooit iets uit.

### Hoe het werkt

**Model en verbinding.** De server praat met de Anthropic API via de officiële Go-SDK, `github.com/anthropics/anthropic-sdk-go`. De browser praat alleen met ClusterForge; de CSP met `connect-src 'self'` blijft zoals hij is (`internal/httpapi/middleware.go:116`).

- **Model.** Standaard `claude-opus-5-5`. In de instellingen kan Jonas `claude-sonnet-5-5` of `claude-haiku-5-5` kiezen als hij het goedkoper wil.
- **Denken en effort.** Opus 5.5 denkt altijd zelf na. ClusterForge zet de effort expliciet: medium voor een vraag in de chat, high voor een incidentanalyse.
- **Server-side fallback.** Die staat aan (`fallbacks: "default"` met de bijbehorende beta-header). Is het gekozen model overbelast, dan antwoordt de API met een ander model in plaats van een fout, en het antwoord toont welk model het werd.
- **Streaming.** Antwoorden worden gestreamd. De server zet de stream per vraag door naar de browser als `text/event-stream` op een POST, met de CSRF-header, en verlengt de write-deadline per stuk, zoals de export van het logboek (`internal/httpapi/audit.go:72-86`). De live-hub is daar niet voor geschikt, want die stuurt alles naar elke sessie.

**De lus.** Het model krijgt een systeemprompt, de tools en het gesprek. Vraagt het een tool, dan voert de server die uit met de rechten van wie de vraag stelt, en stuurt alle resultaten van één beurt samen terug.

- **Grenzen.** Hoogstens 12 rondes per vraag en een maximum aan uitvoertokens. Elke tool heeft een strikt JSON-schema (`strict: true`) en `tool_choice` blijft auto.
- **Afgebroken antwoorden.** Bij een `stop_reason` refusal of max_tokens stopt de lus en zegt het antwoord dat.
- **Caching.** De systeemprompt en de tooldefinities veranderen niet tussen vragen. Ze krijgen `cache_control` op het laatste systeemblok, zodat herhaalde vragen het grootste deel van de invoer uit de cache halen.

**Tools.** Elke tool is een Go-functie die alleen leest, met een minimale rol. Ze gebruiken de bestaande services en de mappers van de API (`toAPICluster`, `toAPINode`, `toAPIGitRepo`), zodat een tool nooit een ruwe databaserij met een versleuteld veld teruggeeft. De set groeit met de mijlpalen:

| Tool | Leest | Vanaf |
| --- | --- | --- |
| `overzicht` | clusters en nodes met status en reden, open incidenten | 4 |
| `cluster`, `node` | detail, VIP's, facts, de invoer van de statusregels met hun uitleg (`status.ClusterWith`, `internal/status/rules.go:180`) | 4 |
| `tijdlijn`, `wijzigingen` | de tijdlijn en het journaal van What Broke over een venster | 4 |
| `incidenten`, `incident` | incidenten met analyse en bereik | 4 |
| `metrics` | de vaste panelen over een venster, nooit vrije PromQL | 4 |
| `drift`, `back_ups`, `testen`, `taken`, `git` | `drift.Report`, back-upoverzicht, `test_runs`, taken met ingekorte staplogs, Git-wijzigingen en spechistorie | 4 |
| `impact` | de afhankelijkheidsgraaf en de impact van een dienst | 4 |
| `gezondheid`, `wat_als` | score, scorekaarten, SPOF's en de wat-als | 5, 6 |
| `kaart` | hosts, netwerken, verbindingen | 7 |
| `documentatie` | de gegenereerde documentatie van een cluster | 9 |
| `rampoefeningen` | resultaten van rampsimulaties en DR-repetities | 11 |

Een tool geeft compacte JSON terug, hoogstens 20 KB, en zinnen uit `audit.describe` in plaats van ruwe payloads. Staplogs zijn standaard weggelaten. Op verzoek komen de laatste 50 regels mee, door hetzelfde filter als event-payloads (`internal/events/payload.go`).

**Antwoorden.** Het antwoord is Markdown met links naar schermen in ClusterForge, zoals `/clusters/<id>` of `/incidenten/<id>`. De webinterface rendert het met een kleine Markdown-renderer die alleen relatieve links naar ClusterForge toestaat, en geen afbeeldingen, HTML of externe links. Een antwoord dat iets aanraadt, linkt naar het scherm waar Jonas het zelf doet.

**Gesprekken en kosten.** Gesprekken worden per gebruiker bewaard, met per bericht het model, de tokens en de kosten volgens een prijstabel in de code. Voor de standaardmodellen is dat per miljoen tokens invoer/uitvoer:

- Opus 5.5: $4 / $20;
- Sonnet 5.5: $2 / $10;
- Haiku 5.5: $0,10 / $0,50.

Een gewone vraag met een paar tools kost met Opus 5.5 hoogstens ongeveer $0,25, en een incidentanalyse ongeveer $0,50; met caching wordt het minder. Er is een maandbudget, standaard $20. Is dat op, dan weigert de assistent nieuwe vragen tot de eerste van de volgende maand, met een event `assistant.budget_reached`. Daarnaast is er een limiet van 30 vragen per uur per gebruiker.

**Wie mag het gebruiken.** Standaard alleen admins, omdat de tijdlijn met herkomst en het logboek alleen voor admins zijn (`internal/httpapi/audit.go:21`). In de instellingen kan Jonas viewers toelaten. Hun tools laten dan de beveiligingsevents en de herkomst weg, en hun vragen tellen mee voor hetzelfde budget.

**Incidenten.** Mijlpaal 15 voegt de knop Analyseer toe aan een incident. Die geeft het model het incident, de analyse van What Broke, een uittreksel van de tijdlijn, de afwijkingen in de metrics en de impact, met dezelfde tools om verder te kijken. Het resultaat heeft een vaste vorm:

- in het kort;
- de waarschijnlijke oorzaak, met bewijs en links;
- wat nog te controleren is;
- wat Jonas kan doen, met links.

Het wordt bij het incident bewaard met model en datum. Automatisch analyseren bij elk nieuw incident staat standaard uit. Zet Jonas het aan, dan gebeurt het alleen voor prod-incidenten met ernst hoog of kritiek, en hoogstens tien keer per dag.

### Datamodel

| Tabel | Inhoud |
| --- | --- |
| `assistant_settings` | Eén rij, zoals `git_repos` (`migrations/00018_gitops.sql:28`): `enabled`, `api_key_enc`, `key_id`, `model`, `monthly_budget_usd`, `viewer_access`, `auto_analyze`, `updated_by`, `updated_at`. |
| `assistant_conversations` | `id`, `user_id`, `title`, `incident_id`, `created_at`, `updated_at`. Bewaard 365 dagen. |
| `assistant_messages` | `id`, `conversation_id`, `seq`, `role`, `content` jsonb (de blokken van de API, met toolresultaten ingekort tot 20 KB), `model`, `input_tokens`, `output_tokens`, `cache_read_tokens`, `cache_write_tokens`, `cost_usd`, `stop_reason`, `created_at`. |

De API-sleutel wordt versleuteld met `secrets.Box` en een AAD gebonden aan de rij, net als het GitHub-token (`internal/gitops/service.go:197-211`). Een leeg veld bij opslaan houdt de oude sleutel, de API geeft hem nooit terug, en het event zegt alleen 'sleutel gewijzigd'. Zonder `CF_MASTER_KEY` kan de assistent niet aan, en de instellingen zeggen waarom. Nieuwe events: `assistant.settings_updated`, `assistant.asked` (gesprek, gebruikte tools, tokens en kosten, maar niet de vraag zelf), `assistant.budget_reached` en `incident.analyzed`.

### API

- `GET` en `PUT /api/v1/assistant/settings` (admin; opslaan vraagt opnieuw wachtwoord of TOTP-code, zoals andere gevoelige instellingen). De sleutel gaat erin maar komt er nooit uit. `POST /api/v1/assistant/settings/test` doet één kleine aanroep en zegt of de sleutel werkt.
- `GET /api/v1/assistant/conversations`, `GET /api/v1/assistant/conversations/{id}` en `DELETE /api/v1/assistant/conversations/{id}`.
- `POST /api/v1/assistant/conversations/{id}/messages` streamt het antwoord. De events in de stream zijn `text`, `tool` (welke tool loopt, in het Nederlands), `done` (tokens en kosten) en `error`.
- `GET /api/v1/assistant/usage`: verbruik deze maand en per gebruiker.
- `POST /api/v1/incidents/{id}/analyze` (mijlpaal 15).

### Schermen

- **`/assistent`.** Links de gesprekken, rechts het gesprek. Tijdens het antwoord staat er welke tool loopt, bijvoorbeeld 'Tijdlijn van web-prod lezen'. Onder elk antwoord staan model, tokens en kosten. Bovenaan staat het verbruik van deze maand.
- **Overal een vraagknop.** Op een cluster, node of incident opent de knop Vraag de assistent een nieuw gesprek met dat onderwerp als context.
- **Instellingen, tab Assistent.** Aan of uit, de sleutel, model, budget, viewers en automatisch analyseren. Een vak legt uit wat er naar de Anthropic API gaat. Dat zijn je vragen en wat de tools teruggeven: namen, statussen, adressen, events, pakketnamen en metrics. Geheimen, bestandsinhoud, vingerafdrukken, tokens en wachtwoorden gaan er nooit heen.

### Veiligheid

- **Alleen lezen.** Geen tool roept een taak, goedkeuring, lifecycle-actie, driftcontrole of live Proxmox-aanroep aan. Een test loopt alle tools af en faalt als er een schrijvende query of `jobs.Enqueue*` in hun aanroepgraaf zit.
- **Geen geheimen.** Tools lezen nooit uit `users`, `sessions`, `server_secrets`, `secrets` of een kolom die op `_enc` eindigt. Gerenderde configuratie komt alleen via de gemaskeerde weg met `[geheim]` (`internal/templates/params.go:120`). Een test roept in de fleet-test elke tool aan en zoekt in alle resultaten naar de geheimen van de test.
- **Prompt-injectie.** Commitberichten, beschrijvingen, hostnamen, unitnamen en journaalregels komen van buiten. De systeemprompt zegt dat toolresultaten gegevens zijn en geen opdrachten. Omdat er geen schrijvende tools zijn en de renderer geen externe links of afbeeldingen toont, kan een injectie hoogstens een antwoord verkeerd maken, en geen gegevens wegsturen.
- **Testen zijn geen storingen.** Events van failovertests, rampsimulaties en sandboxes zijn als test gemarkeerd, zodat de assistent ze niet als incident uitlegt.
- **Herkomst.** Elke vraag staat in het logboek als `assistant.asked` met de gebruikte tools en de kosten. De vraag zelf staat alleen in het gesprek, dat alleen de vrager en admins zien.

### Mijlpalen

- **Mijlpaal 4:**
  - instellingen en de versleutelde sleutel;
  - de lus met streaming, budget en kosten;
  - de tools over What Broke, status, drift, back-ups, tests, taken, Git en afhankelijkheden;
  - `/assistent`.
- **Mijlpaal 15:**
  - Analyseer bij incidenten, met optioneel automatisch;
  - Leg uit bij de score en bij een SPOF;
  - een samenvatting 'In het kort' in de documentatie, op verzoek.

## Risicoanalyse en health score

De status zegt hoe het nu gaat: healthy, degraded, down of split_brain. Ze zegt niet hoe kwetsbaar een cluster is. Een cluster kan groen zijn terwijl beide nodes op dezelfde Proxmox-host draaien, de laatste back-up drie dagen oud is en de failovertest al twee maanden niet liep. De risicoanalyse maakt dat zichtbaar. Een score van 0 tot 100 met een cijfer zegt in één oogopslag hoe het staat. De scorekaarten zeggen wat de score drukt en wat het meeste oplevert om eerst aan te pakken. De wat-als en de SPOF-lijst zeggen wat er gebeurt als iets uitvalt.

### Hoe het werkt

**De score.** Een nieuw pakket `internal/risk` rekent zonder database, net als `status/rules.go` en `deps/graph.go`: `risk.Score(invoer)` geeft per cluster zeven scorekaarten, een totaal en een cijfer. Elke scorekaart begint op 100. Elke bevinding trekt er punten af naar haar ernst: kritiek 40, hoog 20, middel 10 en laag 5. Een kaart gaat nooit onder 0. Het totaal is het gewogen gemiddelde:

| Scorekaart | Gewicht | Wat telt |
| --- | --- | --- |
| Beschikbaarheid | 25 | status nu, beschikbaarheid over 30 dagen uit de statusevents, incidenten, VIP-wissels |
| Back-ups | 20 | versheid, dekking, laatste controle in de sandbox, PBS-verificatie |
| Herstelbaarheid | 15 | failovertest en DR-repetitie: uitslag, leeftijd, gehaalde doelen, niet herstelde runs |
| Redundantie | 15 | nodes en VIP-peers op één host, single points of failure, onderhoud, prod die van lab of test afhangt |
| Configuratie | 10 | open drift en hoe lang al, negeerregels met `*`, revisie niet toegepast, mislukte Git-wijziging |
| Onderhoud en beveiliging | 10 | beveiligingsupdates en hoe lang al, herstart nodig, OS end-of-life, certificaten, agentversie |
| Capaciteit | 5 | schijf boven 85 % of vol binnen 14 dagen volgens de trend, geheugen en CPU over 7 dagen |

Een kaart zonder gegevens telt niet mee en heet 'onbekend', bijvoorbeeld Capaciteit zonder VictoriaMetrics. Een onbekende waarde is nooit een slechte. Het cijfer is A vanaf 90, B vanaf 80, C vanaf 70, D vanaf 60, E vanaf 50 en F daaronder. Twee plafonds voorkomen een geruststellend cijfer:

- een cluster dat nu down of in split_brain is, krijgt hoogstens 40;
- een prod-cluster zonder enige back-up krijgt hoogstens 60.

**Het geheel.** Voor heel ClusterForge is er één score: het gemiddelde van de clusters, met prod drie keer zo zwaar als test en lab half zo zwaar, plus een kaart Platform. Die heeft eigen bevindingen:

- de VM van ClusterForge zelf heeft geen verse back-up;
- een admin zonder TOTP;
- het Proxmox-certificaat verloopt;
- een Proxmox-koppeling die niet lukt.

**Bevindingen.** Elke bevinding komt uit een vaste catalogus, net als de events, met een id, een kaart, een ernst, een Nederlandse zin en een link naar waar je het oplost. Voorbeelden:

- 'web-01 en web-02 draaien allebei op pve1: valt pve1 uit, dan is web-prod down' (Redundantie, hoog op prod, laag op lab);
- 'Laatste failovertest 41 dagen geleden' (Herstelbaarheid, middel), met een link naar Nu testen;
- '3 beveiligingsupdates open op db-01, al 9 dagen' (Onderhoud, hoog). Het 'al 9 dagen' komt uit het journaal;
- 'Schijf / op web-02 is vol over ongeveer 11 dagen' (Capaciteit, hoog), uit een lineaire trend over 7 dagen;
- 'Debian 11 is sinds augustus 2026 end-of-life' (Onderhoud, hoog), uit een tabel met EOL-datums in de code.

Een bevinding kan Jonas accepteren met een reden en eventueel een einddatum. Een lab-cluster op één host is bijvoorbeeld prima. Een geaccepteerde bevinding kost geen punten, maar blijft grijs zichtbaar. De score per bevinding staat erbij, zodat de lijst op `/risico` sorteert op 'wat levert het meeste op'.

**Herberekenen.** Elke minuut onder een eigen advisory lock, niet tijdens de opwarmtijd na een herstart, en direct na een statusovergang. De laatste uitkomst staat in `risk_scores`, en elke nacht schrijft een plannertaak een momentopname voor de trend. Een cijfer dat 10 minuten anders blijft, geeft één event `risk.grade_changed` met de drie zwaarste redenen.

**De wat-als.** Kies een of meer onderdelen: een Proxmox-host, een storage, een bridge, een node, een dienst of een cluster. De wat-als rekent uit wat er gebeurt:

1. Welke VM's uitvallen, en welke Proxmox HA elders herstart. Dat laatste gebeurt alleen als de VM onder HA staat, op gedeelde storage, en er een host met genoeg vrij geheugen is.
2. Welke nodes daardoor down gaan, en wat de echte statusregels (`status.ClusterWith`, `internal/status/rules.go:180`) dan voor elk cluster zeggen: blijft er een VIP-houder over, en hoeveel nodes zijn er nog?
3. Welke diensten via de afhankelijkheidsgraaf geraakt worden. `deps.Impact` krijgt daarvoor een doel met een verzameling nodes (`internal/deps/impact.go:12-16`).
4. Welke VM's op die host niet in de inventory staan. Die staan er als 'onbekend' bij.

De wat-als rekent alleen en raakt niets aan, dus ook viewers mogen hem gebruiken.

**Single points of failure.** De SPOF-lijst draait de wat-als voor elk onderdeel apart. Een onderdeel is een SPOF als zijn uitval een prod-cluster of een bevestigde prod-dienst down maakt. De lijst sorteert op hoeveel er down gaat, en elke SPOF wordt een bevinding op de kaart Redundantie van de clusters die het raakt.

**Wat er nieuw gelezen wordt.** Voor de score en de wat-als zijn een paar gegevens nodig die er nog niet zijn:

- **Gastconfiguratie uit Proxmox.** Per gekoppelde VM leest de sync elke 10 minuten de configuratie: schijven met hun storage, netwerkkaarten met bridge, VLAN-tag en MAC-adres, `onboot` en de HA-status. Nu leest alleen het maken van een golden image dat (`internal/proxmox/provision.go:40-103`).
- **Het certificaat van Proxmox.** De vervaldatum, bij elke sync. Nu alleen bij het koppelen (`internal/proxmox/client.go:368-414`).
- **Facts in protocol 5:**
  - `reboot_required`;
  - de nieuwste geïnstalleerde kernel;
  - de leeftijd van de apt-lijsten, want zonder `apt update` zijn de tellingen te laag;
  - de vervaldatum van publieke certificaten onder `/etc/nginx`, `/etc/haproxy` en `/etc/letsencrypt/live`.
- **Waar ClusterForge zelf draait.** De VM of container, gevonden aan de MAC-adressen van de server in de gastconfiguratie, met de VMID als handmatige terugval.

### Datamodel

| Tabel | Inhoud |
| --- | --- |
| `risk_scores` | `subject_type` (cluster, platform), `subject_id`, `score`, `grade`, `cards` jsonb, `findings` jsonb, `computed_at`. Eén rij per onderwerp. |
| `risk_snapshots` | `day`, `subject_type`, `subject_id`, `score`, `grade`, `cards` jsonb. Elke nacht één rij; klein genoeg om altijd te bewaren. |
| `risk_acceptances` | `id`, `check`, `subject_type`, `subject_id`, `reason`, `expires_at`, `created_by`, `created_at`. |
| `proxmox_guest_config` | `connection_id`, `vmid`, `disks` jsonb (storage, gedeeld of lokaal, grootte), `nets` jsonb (bridge, tag, mac), `onboot`, `ha`, `read_at`. |

Nieuwe events: `risk.grade_changed`, `risk.accepted` en `risk.acceptance_removed`.

### API

- `GET /api/v1/risk`: de score van het geheel, de kaart Platform en alle bevindingen gesorteerd op opbrengst.
- `GET /api/v1/clusters/{id}/risk`: de scorekaarten van een cluster. De clusterlijst krijgt `score` en `grade`.
- `GET /api/v1/risk/history?subject&days`: de trend.
- `GET /api/v1/risk/spof`: de SPOF-lijst.
- `POST /api/v1/risk/what-if` met de onderdelen. Het rekent alleen, en daarom is het een POST zonder bijwerkingen.
- `POST` en `DELETE /api/v1/risk/acceptances` (admin).

Viewers lezen alles behalve de acceptaties wijzigen. De antwoorden bevatten alleen berekende redenen en aantallen, nooit payloads van events.

### Schermen

- **Overzicht.** Bovenaan de score en het cijfer van het geheel, met een trendlijn over 30 dagen en de drie bevindingen die het meeste opleveren.
- **Clusterlijst en clusterdetail.** Een cijferbadge naast de status. Een tab Gezondheid met de zeven kaarten, elk met zijn bevindingen, de link om het op te lossen en Accepteren.
- **`/risico`.** Drie tabs:
  - Bevindingen: alles, gesorteerd op opbrengst en te filteren op kaart en omgeving;
  - SPOF's;
  - Wat als: kies onderdelen en zie per cluster de voorspelde status met de reden, de diensten die geraakt worden en welke VM's HA herstart. Vanaf mijlpaal 7 staat de uitkomst ook op de kaart.

### Veiligheid

- De risicoanalyse leest en rekent alleen. Ze start geen tests en blokkeert geen acties; een score is advies, net als de graaf.
- De EOL-tabel en de drempels staan in de code met tests. Wie ze wil aanpassen, doet dat in een nieuwe versie, niet in de database.

### Mijlpalen

- **Mijlpaal 5:**
  - de score met de zeven kaarten;
  - de kaart Platform;
  - acceptaties en de trend;
  - de badges en de tab Gezondheid.
- **Mijlpaal 6:**
  - de gastconfiguratie uit Proxmox;
  - de wat-als;
  - de SPOF-lijst en `/risico`;
  - weten waar ClusterForge zelf draait.

## Infrastructure mapping

Jonas wil zien hoe alles aan elkaar hangt: op welke host een VM draait, op welke bridge en in welk VLAN, welke nodes een subnet delen, waar een VIP nu zit, en welke diensten met elkaar praten. De afhankelijkheidsgraaf uit fase 2 toont diensten. De kaart voegt daar de fysieke laag en het netwerk aan toe, en de verbindingen die de agent ziet.

### Hoe het werkt

**De gegevens.** Drie bronnen, die het topologiemodel samenvoegt:

- **Proxmox.** Hosts en storage uit de sync. Per gast de netwerkkaarten (bridge, VLAN-tag, MAC) uit de gastconfiguratie van mijlpaal 6. Per host het netwerk uit `GET /nodes/{node}/network`: bridges met hun poorten, bonds, VLAN-interfaces, adressen en de gateway. Dat kan met `Sys.Audit`, dat al in de rol zit. SDN-zones lezen vraagt `SDN.Audit`, en dat doen we niet. De parsers voor `net0=virtio=…,bridge=vmbr0,tag=20` verhuizen van `internal/backups/isolate.go` naar `internal/proxmox`, zodat de sandbox en de kaart dezelfde gebruiken.
- **De agent (protocol 5).** Hij leest alles uit `/sys`, `/proc` en bestanden, zonder externe commando's:
  - per interface de soort, ouder, master, VLAN-id en MTU (`/sys/class/net`, `/proc/net/vlan/config`);
  - routes en standaardgateway (`/proc/net/route`) en DNS (`/etc/resolv.conf`);
  - luisterende poorten met de unit die ze opent, uit `/proc/net/tcp` met de parser die `cf-agent verify` al heeft (`internal/agent/verify.go:465-510`) en `/proc/<pid>/cgroup`;
  - van keepalived per `vrrp_instance` ook interface, VRID en unicast-peers, niet alleen de VIP's (`internal/agent/collect.go:308-352`).

  Deze velden veranderen zelden en tellen mee in de hash van de facts. Een wijziging wordt een regel 'netwerk' in het journaal, met wat er veranderde.
- **De inventory.** Nodes, clusters, VIP's met hun huidige eigenaar, en diensten met afhankelijkheden.

**Koppelen.** Een gast hoort bij een node via de VMID-koppeling die er al is. Een netwerkkaart van een gast hoort bij een interface van de node via het MAC-adres. Staat een VM in Proxmox niet als node gekoppeld, maar heeft hij hetzelfde MAC-adres als een node met een agent, dan stelt de kaart die koppeling voor. Een admin bevestigt haar. Subnetten volgen uit de adressen met prefix, en een VIP hoort bij het subnet waarin het valt.

**De kaart.** `/kaart` heeft drie lagen, met dezelfde React Flow en dagre als de afhankelijkheidsgraaf (`web/src/components/deps/Graph.tsx`):

- **Fysiek.** Per Proxmox-host de storage en de VM's, gekleurd per cluster, met de status van host en VM. Gedeelde storage hangt tussen de hosts.
- **Netwerk.** Bridges en VLAN's met hun subnetten, de nodes erin, de VIP's met een lijn naar hun huidige eigenaar, en de gateway als router. VRRP-peers zijn een stippellijn tussen de nodes.
- **Diensten.** De afhankelijkheidsgraaf, met ontdekte voorstellen als stippellijn en het aantal verbindingen erbij.

Filters op omgeving en cluster, live status via de bestaande stream, en een klik op een element opent een paneel met details en een link naar zijn pagina. De wat-als van mijlpaal 6 en het bereik van een incident kleuren de kaart als overlay. Op een smal scherm wordt de kaart een lijst, zoals bij de afhankelijkheden.

**Verbindingen (mijlpaal 8).** De agent bekijkt elke minuut de gevestigde TCP-verbindingen in `/proc/net/tcp` en `/proc/net/tcp6`. Elke vijf minuten stuurt hij een samenvatting als bericht `conns` op het subject `events`:

- **Uitgaand:** per unit, doeladres en doelpoort het aantal keer gezien.
- **Inkomend:** alleen per eigen poort het aantal, behalve als de afzender een bekende node is.

Hoogstens 500 regels per bericht, ruim onder de 1 MiB van NATS. Er gaan geen PID's, commandoregels of inhoud mee, en tijdelijke poorten als bestemming tellen niet.

De server houdt per verbinding bij wanneer hij voor het eerst en het laatst gezien is. Daarna zet de resolver ze om naar voorstellen, onder dezelfde lock als nu (`internal/deps/resolver.go:20`):

- **Bekende dienst.** Een uitgaande verbinding naar het adres en de poort van een bekende dienst wordt een voorgestelde afhankelijkheid met bron `discovered`. Het adres kan van een node zijn, of een VIP van een cluster met een dienst op die poort. Een voorbeeld: 'php8.2-fpm op web-01 → mariadb in db-prod: 1.240 verbindingen naar 10.0.30.100:3306 in de afgelopen 24 uur'.
- **Onbekende bestemming.** Een bestemming die minstens een uur lang terugkomt, wordt een voorgestelde externe dienst.
- **Nooit voorgesteld.** NATS en de webinterface van ClusterForge zelf.

Bevestigen maakt het voorstel echt, en negeren laat de grafsteen achter die er al is (`migrations/00015_dependencies.sql`). Het schema hoeft daarvoor niet te veranderen.

### Datamodel

| Tabel | Inhoud |
| --- | --- |
| `proxmox_host_networks` | `connection_id`, `pve_node`, `iface`, `type` (bridge, bond, vlan, eth), `ports`, `vlan_id`, `cidr`, `gateway`, `vlan_aware`, `read_at`. |
| `observed_connections` | `node_id`, `unit`, `direction`, `peer_addr`, `peer_port`, `local_port`, `first_seen`, `last_seen`, `samples`. Uniek op node, unit, richting, peer en poort. Rijen die 30 dagen niet gezien zijn, worden opgeruimd. |
| `node_link_proposals` | `connection_id`, `vmid`, `node_id`, `mac`, `state` (suggested, confirmed, ignored). |

De netwerkfacts staan in `node_facts`, en de gastconfiguratie in `proxmox_guest_config` uit mijlpaal 6. Nieuwe events: `node.link_proposed` en `node.link_confirmed`. Voor ontdekte afhankelijkheden zijn de events er al.

### API

- `GET /api/v1/map?layer=physical|network|services&environment&cluster`: knopen en lijnen met status. Viewers zien de kaart, maar verbindingsdetails met externe adressen alleen als admin.
- `GET /api/v1/nodes/{id}/connections`: de verbindingen van een node (admin).
- `POST /api/v1/node-links/{id}/confirm` en `/ignore` (admin).
- Ontdekte afhankelijkheden lopen via de bestaande endpoints voor diensten en afhankelijkheden.

### Schermen

- **`/kaart`** met de drie lagen, filters, het detailpaneel en de overlay voor wat-als en incidenten.
- **Node, kaart Netwerk.** Uitgebreid met soort, VLAN, master, MTU, routes, DNS en luisterende poorten (`web/src/app/(app)/nodes/detail/page.tsx:377-390`), en vanaf mijlpaal 8 de verbindingen.
- **Afhankelijkheden.** Een filter Voorstellen met het bewijs per voorstel, en Bevestigen en Negeren.
- **Proxmox.** Per host een tab Netwerk.

### Veiligheid

- De kaart leest alleen. De server scant of probeert geen netwerken; alles komt uit de agent en de Proxmox-API.
- De agent leest bestanden onder `/proc`, `/sys` en `/etc` en neemt geen argumenten van de server aan voor wat hij leest.
- Inkomende verbindingen van onbekende adressen worden samengeteld per poort. Bij een publieke webserver zijn dat persoonsgegevens, dus de afzonderlijke adressen komen niet in de database, de events of de API.
- Voorstellen veranderen niets tot een admin ze bevestigt, en de graaf blokkeert nooit een actie.

### Mijlpalen

- **Mijlpaal 7:**
  - netwerk per host uit Proxmox;
  - netwerkfacts van de agent (al verzameld vanaf mijlpaal 1);
  - het koppelen op MAC-adres;
  - `/kaart` met de drie lagen en de overlay.
- **Mijlpaal 8:**
  - de verbindingen van de agent (al verzameld vanaf mijlpaal 1);
  - `observed_connections`;
  - voorstellen in de afhankelijkheidsgraaf.

## Auto-documentatie

Documentatie die met de hand bijgehouden wordt, veroudert. ClusterForge weet al bijna alles wat erin hoort: de architectuur uit de template en de spec, het netwerk uit de facts en Proxmox, de diensten uit de graaf, de back-ups en tests met hun metingen. Fase 3 schrijft daar Markdown van die altijd klopt met wat er draait. Jonas leest het in de webinterface, downloadt of drukt het af, en kan het automatisch naar een eigen GitHub-repository laten schrijven.

### Hoe het werkt

**De documenten.** Een nieuw pakket `internal/docs` bouwt deterministische Markdown met een vaste volgorde en zonder tijdstempel in de tekst, zodat een diff alleen echte wijzigingen toont. Per cluster komen er vijf hoofdstukken, en daarnaast een overzicht:

| Document | Inhoud | Bron |
| --- | --- | --- |
| Architectuur | wat het cluster doet, de template met versie en beschrijving, de nodes met rol, VM-grootte en Proxmox-host, het VIP en de failback, een diagram van de diensten | `deploy.Spec`, templatemetadata, `ListNodesByCluster`, de graaf |
| Netwerk | subnet, gateway, VLAN, bridge, adressen, VIP's met VRID en interface, poorten per dienst, een netwerkdiagram | spec, gastconfiguratie, netwerkfacts, de kaart |
| Configuratie | de parameters met labels (geheimen als `[geheim]`), de beheerde bestanden per node, gemaskeerd gerenderd, het huidige `cluster.yaml`, de wijzigingen per revisie met bron en commit | `deploy.History`, `deploy.RenderMasked`, `gitops.Export` |
| Herstel | back-upbeleid, nieuwste back-up per node, laatste sandboxcontrole met hersteltijd, failovertests met overnametijd, DR-repetitie met RTO en RPO, wat er uitvalt bij uitval van elke node en host | back-ups, `test_runs`, de wat-als |
| Runbooks | stappen voor onderhoud, een node vervangen, het VIP handmatig verplaatsen, terugzetten uit back-up met de echte VMID's en storage, en wat te doen bij uitval van elke host | lifecycle, failover, back-ups, de wat-als |

Het overzicht bevat:

- alle clusters met omgeving, status en cijfer;
- de Proxmox-hosts met hun storage en bridges;
- de SPOF-lijst;
- een diagram van de afhankelijkheden tussen clusters;
- de open bevindingen van de risicoanalyse.

Diagrammen zijn Mermaid-blokken, die GitHub en de webinterface als diagram tonen.

**Runbooks uit de code.** Een runbook beschrijft wat ClusterForge echt doet, niet wat iemand ooit opschreef. Onderhoud op web-01 staat er bijvoorbeeld zo in: 'Kies Onderhoud. ClusterForge zet keepalived uit, wacht tot het VIP 10.0.20.100 op web-02 staat, en pas dan mag je herstarten.' Dat volgt de stappen van lifecycle (`internal/lifecycle/lifecycle.go:146-524`). Terugzetten uit back-up geeft het `qmrestore`-commando met de VMID, het volume en de storage van de nieuwste back-up. Een runbook waarvoor gegevens ontbreken, zegt dat in plaats van te gokken: 'VLAN onbekend: deze node is niet uit een template uitgerold en de gastconfiguratie is nog niet gelezen.'

**Actueel houden.** Een lus met een advisory lock luistert naar de events. Ze kijkt bij elk event naar de soort en het cluster, en zet de documenten die het raakt opnieuw in de rij, met 10 minuten debounce per cluster. Na een uitrol, toepassen, failovertest, back-upcontrole of DR-repetitie gebeurt dat ook via OnFinished. Elke nacht genereert een plannertaak alles opnieuw als vangnet.

Elk resultaat gaat met een sha256 naar `doc_snapshots`, maar alleen als de inhoud veranderde. Zo is er een geschiedenis met een diff tussen versies (`internal/gitops/diff.go:19`), en weet de export wat er gewijzigd is. 'Gegenereerd op' staat in een kopregel buiten de hash.

**Naar GitHub (mijlpaal 10).** Een eigen koppeling, los van GitOps:

- **Bestemming.** Een repository, een branch (standaard `main`), een map (standaard `docs/`) en een eigen fine-grained token met Contents lezen en schrijven.
- **Schrijven.** Een `Publisher` maakt via de Git Data API één commit per ronde met alleen de gewijzigde bestanden: blobs, een tree op de vorige tree, een commit, en dan de branch bijwerken zonder force. Is de branch intussen veranderd, dan leest hij opnieuw en bouwt de commit opnieuw. Geen wijziging betekent geen commit.
- **Commitbericht.** Bijvoorbeeld 'Documentatie bijgewerkt: web-prod, db-prod'. De auteur heet ClusterForge.
- **Niet de GitOps-branch.** De bestemming mag niet de repository en branch van GitOps zijn. Een eigen repository is de standaard; een andere branch in dezelfde repository mag. Een documentatiecommit kan zo nooit een GitOps-plan starten, en het GitOps-token blijft alleen-lezen.
- **De belofte in de README.** Daar staat nu dat ClusterForge nooit naar Git schrijft (`README.md:289`). Dat wordt: 'GitOps schrijft nooit naar Git. De documentatie-export schrijft alleen in de map en branch die je zelf instelt, met een eigen token.' Hetzelfde komt in het GitOps-formulier (`web/src/components/gitops/RepoForm.tsx:146`).

**Herstelbaarheid (mijlpaal 14).** Na de DR-repetitie komen de gemeten RTO en RPO in het hoofdstuk Herstel, met datum en doel, en alle getallen zeggen waar ze vandaan komen: 'gemeten in de sandbox op 3 november 2026'. Daarbij komt een rapport Herstelbaarheid op `/rapporten`, af te drukken als PDF via de browser. Per cluster staan erin: de nieuwste back-ups, de laatste controles, failovertests en DR-repetities, RTO en RPO tegen de doelen, en de open risico's. Elke eerste van de maand bewaart een plannertaak het rapport van de maand ervoor als snapshot. Dit is het afdrukbare back-uprapport dat fase 2 doorschoof.

### Datamodel

| Tabel | Inhoud |
| --- | --- |
| `doc_snapshots` | `id`, `scope` (cluster, overview, report), `cluster_id`, `kind`, `content`, `sha256`, `generated_at`, `cause` (event-id of 'nacht'), `published_sha`. Alleen een nieuwe rij als de hash verandert. De laatste 50 versies per document blijven bewaard; Git heeft de rest. |
| `docs_targets` | Eén rij: `api_url`, `owner`, `repo`, `branch`, `path`, `token_enc`, `key_id`, `enabled`, `last_commit_sha`, `last_published_at`, `last_error`. |

Nieuwe events: `docs.target_connected`, `docs.target_updated`, `docs.target_disconnected`, `docs.published`, `docs.publish_failed` en `docs.publish_recovered`. Een nieuwe snapshot geeft geen event, want dat zou het logboek vullen.

### API

- `GET /api/v1/docs`: de lijst met documenten en hun laatste versie.
- `GET /api/v1/docs/{scope}/{kind}?cluster=` en `GET .../history`, plus een diff tussen twee versies.
- `GET /api/v1/docs/download`: een zip met alle Markdown, in dezelfde mappen als in Git.
- `GET`, `PUT` en `DELETE /api/v1/docs/target`, `POST /api/v1/docs/target/test` en `POST /api/v1/docs/publish` (admin).
- `GET /api/v1/reports/recovery?month=` (mijlpaal 14).

Viewers lezen en downloaden. De documenten bevatten nooit IP-adressen of sessies uit de herkomst; gebruikersnamen bij revisies zijn een instelling, standaard aan.

### Schermen

- **`/documentatie`.** Links de boom (Overzicht, per cluster de vijf hoofdstukken), rechts het gerenderde document met diagrammen. Bovenaan staan Download als zip, Afdrukken en Geschiedenis met de diff.
- **Clusterdetail.** Een kaart Documentatie met de laatste wijziging en een link.
- **Instellingen, tab Documentatie.** De GitHub-koppeling, met de laatste commit en link, de laatste fout, en Nu publiceren.
- **`/rapporten`.** Het rapport Herstelbaarheid per maand, met afdrukstijl.

### Veiligheid

- **Geen geheimen.** Geheime parameters worden overgeslagen of als `[geheim]` gerenderd, zoals `exportYAML` doet (`internal/gitops/link.go:95`). Bestandsinhoud of hashes van nodes komen er nooit in. Een test genereert alle documenten voor de fleet-test en faalt als een geheim van de test erin staat.
- **Het token.** Versleuteld met een eigen AAD, nooit terug uit de API, en alleen naar de API-host, zonder redirects naar een andere host. Het volgt dezelfde regels als de GitHub-client van GitOps (`internal/gitops/github.go:109-135`).
- **Testen.** `ghfake` krijgt de schrijvende endpoints en meerdere branches. Een test bevestigt dat de GitOps-lus nog steeds alleen GET doet, en dat de export alleen in zijn eigen map en branch schrijft.
- **Alleen lezen.** De documentatie leest alleen opgeslagen gegevens. Ze stuurt nooit een agentcommando om iets te kunnen beschrijven.

### Mijlpalen

- **Mijlpaal 9:**
  - de vijf hoofdstukken en het overzicht;
  - snapshots met geschiedenis;
  - `/documentatie`, downloaden en afdrukken.
- **Mijlpaal 10:** de GitHub-export met eigen token, en de nieuwe tekst in README en formulier.
- **Mijlpaal 14:** herstel met de gemeten RTO en RPO, en het rapport Herstelbaarheid.

## Disaster simulations

Fase 2 test één storing op één node: keepalived of een dienst stoppen, of de VM hard uitzetten. Fase 3 test wat er gebeurt als er meer tegelijk misgaat:

- een hele Proxmox-host weg;
- een dienst waar andere clusters van afhangen;
- een VIP zonder houder;
- een netwerk dat hapert;
- het hele cluster kwijt, terug te halen uit de back-ups.

Er zijn drie niveaus, van veilig naar invasief. De wat-als van de risicoanalyse voorspelt zonder iets aan te raken, en mag voor elk cluster en elke rol (mijlpaal 6). Live simulaties veroorzaken de storing echt, alleen in lab en test, met dezelfde vangnetten als de failovertest (mijlpaal 11 en 12). De DR-repetitie zet een heel cluster terug in een afgesloten netwerk en raakt productie dus nooit. Daarom mag die ook voor prod (mijlpaal 13).

### Hoe het werkt

**Voorspellen en meten.** Elke live simulatie begint met de voorspelling van de wat-als: welke clusters en diensten zouden down of degraded gaan, en welke blijven draaien. Tijdens de storing meet de server met dezelfde probes als de failovertest (`internal/failover/probe.go`), op de VIP's van het doelcluster en van elk cluster dat er volgens de graaf van afhangt. Het rapport zet beide naast elkaar:

| Cluster | Voorspeld | Gemeten |
| --- | --- | --- |
| db-test | down | down, 0 s tot herstel na het terugzetten |
| web-test | degraded | down: php8.2-fpm gaf 502, onverwacht |
| cron-test | geraakt | bleef gezond |

Een simulatie bewijst zo niet alleen dat herstel werkt, maar ook of de afhankelijkheidsgraaf klopt. Ze heeft drie uitslagen:

- **PASS:** alles gedroeg zich zoals voorspeld en is hersteld binnen de verwachting.
- **WARNING:** iets bleef gezond dat geraakt zou worden. De graaf heeft dan waarschijnlijk een afhankelijkheid te veel, of een harde die zacht is.
- **FAIL:** er ging iets onverwacht down, of iets herstelde niet. Dan ontbreekt er een afhankelijkheid of werkt de failover niet.

**Scenario's in mijlpaal 11.** Die werken met de agents die er zijn:

- **Proxmox-host valt uit** (`host_down`). Alle gekoppelde VM's op één host worden tegelijk hard gestopt via Proxmox, zoals bij VM hard uitzetten (`internal/failover/run.go:690-734`). De host zelf blijft aan; ClusterForge zet nooit een host uit. Mag alleen als alle gekoppelde VM's op die host bij lab- of testclusters horen. Geweigerd als de host een prod-node, de VM van ClusterForge zelf of een VM onder Proxmox HA draait. VM's die niet in de inventory staan, worden niet gestopt en staan in het rapport als 'niet meegenomen'.
- **Dienst valt weg** (`service_down`). Een unit stoppen op alle nodes van een cluster, bijvoorbeeld mariadb op db-test, en meten wat de afnemers doen. Hiervoor komen de database-units op de lijst van te stoppen units, alleen voor lab en test (`internal/failover/service.go:58`).
- **VIP valt weg** (`vip_loss`). Keepalived stoppen op alle nodes, zodat het VIP geen houder heeft. Een failovertest verwacht een overname; deze uitslag verwacht juist uitval en herstel na het terugzetten (`internal/failover/run.go:809-815`).

**Storingsinjectie in mijlpaal 12.** Met protocol 6 krijgt de agent `fault.inject` en `fault.clear`. Het zijn vaste storingen, geen vrije commando's:

| Storing | Wat de agent doet |
| --- | --- |
| `link_down` | een interface down zetten |
| `vrrp_block` | VRRP-verkeer (IP-protocol 112) laten vallen met een eigen nft-tabel `cf_fault` |
| `port_block` | inkomend verkeer op één TCP-poort laten vallen |
| `netem` | vertraging (ms) of pakketverlies (%) op een interface met tc |
| `disk_fill` | een bestand onder `/var/lib/cf-agent` aanmaken tot een schijf op een gekozen percentage staat, hoogstens 95 % |

**De dodemansknop.** Een storing kan de agent van de server afsnijden. Daarom ruimt de agent haar zelf op:

- Hij schrijft de storing naar `/var/lib/cf-agent/faults.json` voordat hij haar toepast.
- Elke storing heeft een lease van 30 s die de server verlengt zolang hij meet, en een harde bovengrens van hoogstens 10 minuten. Stopt het verlengen, omdat de server uitvalt of de verbinding weg is, dan draait de agent de storing zelf terug.
- Bij `link_down` op de interface waarover de agent praat, kan de server niet verlengen. Daar geldt alleen de bovengrens, en die is hoogstens 2 minuten.
- Bij elke start draait de agent achtergebleven storingen terug. Een oneshot-unit `cf-agent-fault-clear.service`, die `install.sh` plaatst, doet dat bij het opstarten, ook als de agent zelf niet start.

Een scenario wordt in mijlpaal 12 een reeks van hoogstens vijf stappen met een tijdstip, bijvoorbeeld:

- 0 s: `vrrp_block` op de VIP-eigenaar;
- 30 s: keepalived stoppen op de tweede node;
- 90 s: alles opheffen.

Zo zijn ook split-brain te testen (`vrrp_block` op alle nodes: verwacht is dat de statusevaluator split_brain meldt) en trage netwerken (`netem` 200 ms op de VIP-eigenaar). Of nft en tc er zijn, staat vanaf protocol 6 in de facts. Ontbreken ze, dan is de storing grijs met de reden.

**DR-repetitie in mijlpaal 13.** Voor één cluster, ook prod, zet ClusterForge van elke node de nieuwste back-up terug als sandbox-VM, op een afgesloten bridge in plaats van met `link_down` (`internal/backups/isolate.go:268-277`). De nodes kunnen elkaar dan wel zien, maar het netwerk niet. Daarna:

1. **Bridge controleren.** Op elke host die gebruikt wordt, leest de server via de Proxmox-API de bridge (`GET /nodes/{node}/network/{bridge}`, standaard `vmbr99`). Ze moet bestaan, het type bridge hebben, geen `bridge_ports` en geen adres. Geen enkele VM buiten het sandbox-register mag erop hangen. Anders start de repetitie niet, met de reden en het commando om het te herstellen.
2. **Capaciteit.** Samen voor alle VM's: per storage de schijfruimte met 10 % marge en onder 85 %, en per host het geheugen plus 1 GiB. Dat is dezelfde regel als de back-upcontrole (`internal/backups/verify.go:440-481`), maar opgeteld.
3. **Terugzetten.** Via het sandbox-register en `guard()` (`internal/backups/sandbox.go:71-183`). Elke VM krijgt de allowlist van configsleutels. Elke netwerkkaart gaat naar de afgesloten bridge zonder VLAN-tag, met hetzelfde MAC-adres. Eerst staat `link_down=1`. Pas als de server de configuratie heeft teruggelezen, gaat de link aan.
4. **Starten en controleren.** De VM's starten tegelijk. De agent in de sandbox verbindt nooit met de server (`cmd/cf-agent/main.go:127`). De controle loopt via de guest agent met `cf-agent verify` (`internal/backups/deep.go:43`), uitgebreid met:
   - de adressen van de node, waaruit de server afleidt of elk VIP precies één houder heeft;
   - een HTTP- of TCP-probe van de ene sandbox-node naar het VIP en naar de andere nodes;
   - de databasecontrole uit fase 2.

   Die uitbreiding zit in de agent van protocol 6. Een back-up met een oudere agent geeft voor die punten 'niet gecontroleerd: agent in de back-up te oud'.
5. **Meten.** De RTO loopt van de start van het eerste terugzetten tot alle controles slagen. De RPO is de leeftijd van de oudste gebruikte back-up. Liggen de back-ups van de nodes meer dan een uur uit elkaar, dan waarschuwt het rapport dat de gegevens van de nodes niet bij elkaar passen, bijvoorbeeld bij een database met replicatie.
6. **Opruimen.** Altijd, ook na annuleren of een herstart van de server, via OnFinished en de opruimer van het sandbox-register.

**Vangnetten bij de DR-repetitie.** Een kopie van prod met dezelfde adressen is veilig zolang de bridge echt afgesloten is. Daarom zijn er drie lagen:

- de controle van de bridge vooraf;
- `link_down=1` tot de configuratie is teruggelezen;
- tijdens de repetitie houdt de server het echte cluster in de gaten. Verhuist een VIP van het prod-cluster, of komt er een split_brain, dan stopt hij meteen alle sandbox-VM's hard en is de uitslag error.

**Doelen.** Per cluster kan Jonas een RTO- en een RPO-doel zetten. Standaard is dat voor prod 60 minuten en 26 uur, en voor lab en test geen doel. De uitslag is PASS als alle controles slagen en de doelen gehaald zijn, WARNING als een doel niet gehaald is, en FAIL als het cluster niet werkend terugkomt.

**Planning.** Een DR-repetitie kan met de hand of gepland in het testvenster, bijvoorbeeld de eerste zondag van de maand. Dat gebeurt via het testslot en de planner, net als de back-upcontrole (`internal/failover/schedule.go:144-320`). Live simulaties zijn nooit gepland: ze starten alleen met de hand.

### Gedeelde regels

Alle drie de soorten volgen de regels van de failovertest:

- **Precheck zonder force.** Het cluster is gezond, de heartbeats zijn vers en de slots zijn vrij (`internal/failover/service.go:669-818`).
- **Testslot.** Over heel ClusterForge loopt hoogstens één invasieve test.
- **Clusterslot.** Een simulatie die meerdere clusters raakt, vergrendelt ze allemaal in één transactie, gesorteerd op id.
- **Altijd terugzetten.** De vier lagen blijven: tijdstip van de storing vastleggen vóór het commando, Detach, de emergency-defer en de rode balk met Opnieuw herstellen (`internal/failover/run.go:199-239,920-977`).
- **Nooit Retryable.** Een nieuwe poging is een nieuwe taak.
- **Bevestiging.** Een admin start; een live simulatie vraagt de getypte slug van elk geraakt cluster.

ClusterForge weet vanaf mijlpaal 6 op welke VM of container hij zelf draait. Hij zoekt die op aan de MAC-adressen van zijn eigen interfaces in de VM-configuraties, en anders vult Jonas de VMID in. Een simulatie die die VM of zijn host raakt, start niet.

### Datamodel

| Tabel | Inhoud |
| --- | --- |
| `disaster_scenarios` | `id`, `cluster_id`, `name`, `kind` (host_down, service_down, vip_loss, sequence, dr_rehearsal), `params` jsonb (host, unit, stappen), `expect` jsonb (maximale uitval, verwachte status per cluster), `schedule` (alleen voor DR), `created_by`, `created_at`, `updated_at`. |
| `clusters` | Nieuwe kolommen `rto_target_minutes` en `rpo_target_hours`. |
| `test_runs` | Nieuwe soorten `disaster.run` en `dr.rehearsal`. `measurements` krijgt `recovery_ms` en `total_ms`, en voor DR `rto_seconds`, `rpo_seconds` en per VM terugzetten, opstarten en controleren. `definition` bevat ook de voorspelling. |
| `backup_sandboxes` | Mag meer dan één sandbox per run hebben (`internal/store/queries/sandboxes.sql:10-12`). |

De unieke index `jobs_test_slot`, de slotqueries en de enums in de API krijgen de nieuwe soorten (`migrations/00013_failover.sql:53-54`). Nieuwe events: `disaster.started`, `disaster.fault_injected`, `disaster.fault_cleared`, `disaster.finished`, `dr.sandbox_isolation_failed` en `dr.aborted`.

### Agent

Protocol 6 brengt:

- `fault.inject`, `fault.renew` en `fault.clear`;
- de lease en het opruimen bij start en boot;
- `tools` in de facts (nft, tc);
- de uitgebreide `cf-agent verify` met adressen en probes naar peers.

`install.sh --upgrade` plaatst de oneshot-unit. Een agent onder protocol 6 kan meedoen aan de scenario's van mijlpaal 11, maar niet aan storingsinjectie.

### API

- `GET` en `POST /api/v1/clusters/{id}/disaster-scenarios`, en `PUT` en `DELETE /api/v1/disaster-scenarios/{id}`.
- `GET /api/v1/disaster-scenarios/{id}/preview`: precheck en voorspelling, zonder iets te doen.
- `POST /api/v1/disaster-scenarios/{id}/run`: admin, met de slugs bij live simulaties.
- De rapporten komen uit de bestaande endpoints voor `test_runs`, en Opnieuw herstellen uit het bestaande pad (`internal/httpapi/failover.go:40-49`).

### Schermen

- **Clusterdetail, tab Rampoefeningen.** De scenario's met hun laatste uitslag, een knop Voorspellen en een knop Starten. Starten toont eerst de voorspelling, de precheck en wat er gestopt wordt.
- **Rapport.** De bestaande testpagina (`web/src/app/(app)/tests/detail/page.tsx`), met de probe-tijdlijn per cluster, de tabel voorspeld tegen gemeten en voor DR de RTO en RPO tegen de doelen.
- **`/tests`.** Laat ook rampoefeningen zien, met een filter op soort.

### Veiligheid

- Live storingen alleen in lab en test, nooit gepland, en nooit op een host met een prod-node, de VM van ClusterForge of een HA-VM. Prod krijgt alleen de voorspelling en de DR-repetitie.
- De agent voert alleen de vijf vaste storingen uit, met grenzen. Er is geen vrije interface, regel of commando.
  - `link_down` mag niet op `lo`.
  - `disk_fill` gaat hoogstens tot 95 % en schrijft alleen onder `/var/lib/cf-agent`.
  - `netem` gaat hoogstens tot 2000 ms en 50 %.
- De DR-repetitie raakt alleen VM's in de pool `cf-sandbox` en het register. Alleen het pakket `backups` mag Restore en Destroy aanroepen (`internal/backups/callers_test.go:17-20`), en dat blijft zo.

### Mijlpalen

- **Mijlpaal 11:** host_down, service_down en vip_loss in lab en test, met voorspelling, het rapport voorspeld tegen gemeten, en meerdere clusters in het clusterslot.
- **Mijlpaal 12:** protocol 6 met `fault.inject` en de dodemansknop, en scenario's als reeks.
- **Mijlpaal 13:**
  - de DR-repetitie met afgesloten bridge;
  - de RTO- en RPO-doelen;
  - de planning in het testvenster.
- **Mijlpaal 14:** de uitslagen in de documentatie en het rapport Herstelbaarheid (zie Auto-documentatie).

## Bouwvolgorde

Fase 3 telt vijftien mijlpalen. Eerst komen de gegevens die er nog niet zijn, daarna wat erop rekent, dan de assistent die erover praat, en pas daarna de storingen. De grootte is een relatieve inschatting.

| # | Mijlpaal | Klaar als | Grootte |
| --- | --- | --- | --- |
| 1 | Wijzigingsjournaal en agentprotocol 5 | Na `apt-get install --only-upgrade nginx` op web-02 staat binnen een minuut 'nginx bijgewerkt van 1.26.0 naar 1.26.3 (root)' op de tab Wijzigingen. Na een reboot staat er 'Herstart, kernel 6.12.48'. Een VM die in Proxmox gestopt wordt, geeft een regel met de Proxmox-gebruiker. Een agent op protocol 4 toont 'agent te oud voor het journaal', maar dienstovergangen uit de heartbeats verschijnen wel. Geen regel bevat bestandsinhoud of een hash. | L |
| 2 | Incidenten | nginx stoppen op beide nodes van web-test geeft precies één incident, met ernst laag en status open. Weer starten sluit het na 5 minuten. Een failovertest geeft een incident 'door test' dat standaard verborgen is. Een mislukte geplande back-upcontrole geeft een incident. `/incidenten` en de kaart op het overzicht tonen het. | M |
| 3 | What Broke: tijdlijn en oorzaak | In de integratietest geeft 'pakket nginx bijgewerkt, daarna nginx failed' die upgrade als waarschijnlijke oorzaak, met de reden. Een host die offline gaat met drie VM's erop geeft de host. Een mislukte GitOps-wijziging geeft de commit met sha. Zonder verandering zegt het rapport dat. De incidentpagina toont de tijdlijn met grafieken van het venster. | L |
| 4 | AI-assistent | Met een sleutel beantwoordt `/assistent` 'waarom was web-test om 14:00 down?' met de oorzaak uit mijlpaal 3 en links, en toont welke tools liepen, het model, de tokens en de kosten. Zonder sleutel of masterkey staat er waarom het uit is. Een test bevestigt dat geen tool schrijft of een geheim teruggeeft. Bij een leeg budget weigert hij met een duidelijke zin. | L |
| 5 | Health score en scorekaarten | Een fleet met beide web-nodes op pve1, een back-up van 40 uur en een failovertest van 41 dagen geleden geeft de drie bevindingen met hun punten en een cijfer, en accepteren haalt de punten weg. Het overzicht toont de score met trend, en een nacht later staat er een momentopname. | M |
| 6 | Wat-als en single points of failure | 'Wat als pve1 uitvalt' voorspelt voor elk cluster de status met de echte statusregels, en noemt welke VM's HA herstart. pve1 staat in de SPOF-lijst als web-prod daar helemaal op draait. ClusterForge weet op welke VM hij zelf draait. | M |
| 7 | Infrastructuurkaart | `/kaart` toont voor de fleet de hosts met storage en bridges, de VM's per host, de VLAN's en subnetten met de nodes en VIP's, en de diensten met hun afhankelijkheden, met live status en een wat-als als overlay. | L |
| 8 | Verbindingen en ontdekte afhankelijkheden | Een node van web-test die verbindingen maakt naar het VIP van db-test op 3306, geeft een voorgestelde afhankelijkheid php8.2-fpm → mariadb met het aantal verbindingen als bewijs. Bevestigen maakt haar echt, negeren laat een grafsteen, en NATS naar ClusterForge wordt nooit voorgesteld. | M |
| 9 | Auto-documentatie | `/documentatie` toont per cluster architectuur, netwerk met diagram, diensten, configuratie, back-ups, tests en runbooks, en een overzicht. Downloaden geeft een zip met Markdown. Een test vindt geen enkel geheim uit de fleet in de uitvoer. | M |
| 10 | Documentatie naar GitHub | Met een eigen repository en token staat binnen 15 minuten na een wijziging één commit met alleen de gewijzigde bestanden. Een tweede run zonder wijziging maakt geen commit, en dezelfde repository als GitOps wordt geweigerd. | S |
| 11 | Rampsimulaties in lab en test | `host_down` op pve2 met web-test en db-test stopt alle vier de VM's tegelijk, meet de VIP's, zet alles terug, en het rapport zet voorspeld en gemeten naast elkaar. `service_down` op mariadb in db-test toont wat web-test deed. Op een host met een prod-node start het niet. | L |
| 12 | Storingsinjectie met dodemansknop | `vrrp_block` op alle nodes geeft split_brain en ruimt zichzelf op als de server midden in de test stopt. `link_down` op de beheerinterface komt na de bovengrens vanzelf terug, ook na een reboot. Een reeks van drie stappen loopt in volgorde. | L |
| 13 | DR-repetitie | Op Jonas' Proxmox zet de repetitie web-prod helemaal terug op `vmbr99`. In de sandbox heeft het VIP één houder en antwoordt HTTP, het rapport geeft RTO en RPO tegen de doelen, en daarna is alles weg. Zonder afgesloten bridge start ze niet, en het rapport geeft de commando's. | L |
| 14 | Herstelbaarheid in documentatie en rapport | De documentatie van web-prod noemt de gemeten RTO en RPO met datum, en het runbook 'Terugzetten uit back-up' gebruikt de echte VMID's en storage. `/rapporten` drukt per cluster back-ups, controles, failovertests en DR af, voor de maand. | M |
| 15 | Assistent bij incidenten, risico en documentatie | Analyseer op een incident geeft samenvatting, oorzaak met bewijs, wat te controleren en wat te doen, en bewaart het met model en datum. Leg uit werkt op een bevinding en een SPOF. Automatisch analyseren staat uit tot Jonas het aanzet, en dan alleen voor prod. | M |

**Waarom deze volgorde.** Alle modules hebben gegevens nodig die er nog niet zijn: het journaal, de Proxmox-taken, de facts en het netwerk. Daarom komt mijlpaal 1 eerst, met de enige agentupdate tot mijlpaal 12. What Broke komt daarna, omdat Jonas het dagelijks gebruikt en omdat het de tijdlijn levert waar de assistent op steunt.

De assistent komt vroeg, als mijlpaal 4. Dan heeft hij al veel te vertellen, en elke volgende mijlpaal voegt er een tool aan toe in plaats van hem achteraf aan te sluiten. De score en de wat-als rekenen op wat er dan is. De kaart en de ontdekte afhankelijkheden volgen, zodat de wat-als, de documentatie en de rampsimulaties een vollere graaf hebben.

Storingen komen als laatste: eerst zonder nieuwe agent (mijlpaal 11), dan met de dodemansknop (mijlpaal 12), en de DR-repetitie pas als de kaart weet welke bridges er zijn. Valt het einde buiten de tijd, dan blijft alle eerdere waarde staan.

## Open keuzes

Veertien keuzes zijn ingevuld met een aanbeveling, en het ontwerp volgt overal de aanbevolen kolom.

| Keuze | Aanbevolen | Alternatief | Waarom |
| --- | --- | --- | --- |
| Waar staan wijzigingen van de nodes? | Een eigen tabel `node_changes`, 180 dagen bewaard. | In de eventtabel, als events van de agent. | Het logboek gaat over ClusterForge en zijn gebruikers en wordt nooit opgeruimd. Het journaal is groter en heeft een termijn nodig. |
| Duwt de agent het journaal, of vraagt de server erom bij een incident? | Duwen, met een cursor en ontdubbelen. | Een leescommando `journal.read` per venster. | Een node die down is, kan niets meer vertellen, en de gegevens moeten er al zijn als het incident begint. |
| Logs verzamelen? | Nee: alleen gerichte regels (SSH-logins, OOM-kills) in het journaal. | Loki of VictoriaLogs, of een leescommando `journal.query`. | Een logstack is een nieuw proces en veel opslag. Gerichte regels dekken de meeste oorzaken; `journal.query` kan later. |
| Hoe vinden we de oorzaak? | Vaste regels met gewichten en verbanden, met de redenen erbij. | Het taalmodel laten kiezen uit de tijdlijn. | Regels zijn te testen, kosten niets en werken zonder sleutel. De assistent kan ze daarna uitleggen. |
| Welk model voor de assistent? | `claude-opus-5-5`, met server-side fallback, in te stellen. | Standaard Sonnet 5.5 of Haiku 5.5. | Opus 5.5 is het sterkst in het combineren van veel tools. Het maandbudget begrenst de kosten, en de goedkopere modellen zijn één keuze in de instellingen. |
| Wie mag de assistent gebruiken? | Standaard alleen admins; viewers als instelling. | Iedereen. | De tijdlijn met herkomst is alleen voor admins, en elke vraag kost geld. |
| Automatisch een AI-analyse per incident? | Uit; een knop Analyseer, en optioneel aan voor prod. | Altijd aan. | Kosten en ruis. De vaste analyse van What Broke is er altijd. |
| Hoe berekenen we de score? | Vaste catalogus met gewichten, plafonds en acceptaties, onbekend telt niet mee. | Een model dat de score schat. | Uitlegbaar, stabiel en te testen; elke punt aftrek heeft een oorzaak en een oplossing. |
| Welke bibliotheek voor de kaart? | `@xyflow/react` met `dagre`, die er al zijn. | Cytoscape of D3. | Geen nieuwe afhankelijkheid, en de afhankelijkheidsgraaf gebruikt ze al. |
| Hoe ontdekken we verbindingen? | Een steekproef uit `/proc/net/tcp` elke minuut, samengevat per 5 minuten. | eBPF of conntrack. | Werkt overal zonder kernelmodules of externe commando's. Kortlevende verbindingen missen we, maar een afhankelijkheid gebruikt een dienst zelden maar één keer. |
| Waar komt de documentatie in Git? | Een eigen repository met een eigen token met schrijfrecht. | Een map `docs/` in de GitOps-repository. | Het GitOps-token blijft alleen-lezen, en een documentatiecommit start nooit een GitOps-plan. |
| Hoe simuleren we een host-uitval? | Alle gekoppelde VM's op die host tegelijk hard stoppen, alleen in lab en test. | De host echt uitzetten via IPMI of de Proxmox-API. | ClusterForge zet nooit een host uit. Proxmox HA test je hiermee niet, en dat zegt het rapport. |
| Hoe isoleren we de DR-repetitie? | Een bridge zonder poorten en adres (`vmbr99`), vooraf gecontroleerd via de API, met `link_down` tot de config gecontroleerd is en een noodstop als prod verandert. | Een SDN-zone, of `link_down` zoals bij de enkele sandbox. | Met `link_down` zien de nodes elkaar niet, en dan is een cluster niet te testen. Een bridge is in twee minuten gemaakt en te controleren. |
| Hoe veroorzaken we netwerkstoringen? | `fault.inject` in de agent met een lease en een harde bovengrens. | `link_down` op de netwerkkaart via de Proxmox-API. | Proxmox kan alleen de hele kaart afsluiten, en valt de server uit, dan blijft die dicht. De agent kan VRRP, één poort of vertraging, en ruimt zelf op. |

## Vragen aan Jonas

De vragen staan in de volgorde waarin de mijlpalen ze nodig hebben. Zolang een antwoord ontbreekt, bouwen we met de standaard eronder.

- [ ] Mogen we de agents bijwerken naar protocol 5, en later naar 6? (vanaf mijlpaal 1, en mijlpaal 12)

  Standaard zolang je niet antwoordt: ja, per node met `install.sh --upgrade` zoals de webinterface toont, en de golden image opnieuw bouwen. Nodes met een oudere agent blijven werken, zonder journaal van de agent en zonder storingsinjectie.

- [ ] Mag het journaal SSH-logins bewaren (gebruiker, bron-IP, methode)? (vanaf mijlpaal 1)

  Standaard zolang je niet antwoordt: ja, 180 dagen, zonder commando's. Een instelling zet het uit.

- [ ] Mag ClusterForge uitgaand naar `api.anthropic.com`, en wil je de assistent met je eigen API-sleutel? Welk maandbudget? (vanaf mijlpaal 4)

  Standaard zolang je niet antwoordt: de assistent staat uit tot je een sleutel invult bij Instellingen → Assistent. Daarna gebruikt hij `claude-opus-5-5` met server-side fallback aan, een budget van $20 per maand, en alleen voor admins. Een gewone vraag kost met Opus 5.5 hoogstens ongeveer $0,25.

- [ ] Gebruik je Proxmox HA, en zijn er gastnetwerken met VLAN-tags? (vanaf mijlpaal 6)

  Standaard zolang je niet antwoordt: geen HA-groepen, dus de wat-als rekent op geen herstart elders, behalve voor VM's met een `hastate`. VLAN-tags komen uit de gastconfiguratie.

- [ ] Welke clusters horen samen, en welke afhankelijkheden ken je al? (vanaf mijlpaal 8)

  Standaard zolang je niet antwoordt: de voorstellen uit de verbindingen. Je bevestigt of negeert ze op de pagina Afhankelijkheden.

- [ ] Welke GitHub-repository voor de documentatie? (vanaf mijlpaal 10)

  Standaard zolang je niet antwoordt: geen. Maak je een private repository, bijvoorbeeld `clusterforge-docs`, met een fine-grained token met alleen Contents lezen en schrijven op die repository, dan schrijft ClusterForge daar elke nacht om 06:00 en na wijzigingen.

- [ ] Op welke clusters mogen live rampsimulaties draaien? (vanaf mijlpaal 11)

  Standaard zolang je niet antwoordt: alleen lab en test, en alleen met de hand. Op prod alleen de voorspelling en de DR-repetitie.

- [ ] Wil je op elke Proxmox-host een afgesloten bridge voor de DR-repetitie? (vanaf mijlpaal 13)

  Standaard zolang je niet antwoordt: `vmbr99`, zonder poorten en zonder adres. Tot die er is, staat de DR-repetitie uit met deze commando's per host:

  ```
  cat >> /etc/network/interfaces <<'EOF'

  auto vmbr99
  iface vmbr99 inet manual
      bridge-ports none
      bridge-stp off
      bridge-fd 0
  EOF
  ifreload -a
  ```

- [ ] Wat zijn je doelen voor herstel per cluster (RTO en RPO)? (vanaf mijlpaal 13)

  Standaard zolang je niet antwoordt: voor prod een RTO van 60 minuten en een RPO van 26 uur (dagelijkse back-ups met marge), voor lab en test geen doel.

- [ ] Mag een DR-repetitie gepland in het testvenster draaien? (vanaf mijlpaal 13)

  Standaard zolang je niet antwoordt: de planning staat per cluster uit. Aan betekent de eerste zondag van de maand in `CF_TEST_WINDOW`. Het terugzetten belast de storage van je hosts, ook als het cluster prod is.

## Buiten fase 3

Fase 3 laat een aantal dingen bewust liggen.

**Blijft bij Jonas.**

- Automatisch herstellen, door regels of door de assistent, en automatisch toepassen vanuit Git. De assistent heeft geen enkele schrijvende tool.
- Live storingen op prod, en een geplande live storing waar dan ook. Prod krijgt de voorspelling en de DR-repetitie.
- Een Proxmox-host echt uitzetten, en storingen in Proxmox zelf (storage, corosync, het netwerk van de host).

**Latere uitbreidingen.** Deze punten passen in het ontwerp, maar hebben nog geen mijlpaal:

- meldingen bij een incident of een gedaald cijfer, per mail, ntfy of webhook;
- een leescommando `journal.query` voor journald per unit en venster, of VictoriaLogs als logopslag;
- wijzigingen als pull request op de GitOps-repository voorstellen. Dat vraagt schrijfrecht op die repository, en fase 3 houdt dat token bewust alleen-lezen. De assistent kan de wijziging in `cluster.yaml` wel als tekst geven, die Jonas zelf commit;
- rechten per cluster en gebruikersbeheer in de webinterface;
- uitgebreidere beveiligingsfacts (SSH-configuratie, firewall, open poorten naar buiten) als opt-in;
- dnf en zypper naast apt voor updates en het journaal;
- rampsimulaties en DR-repetities voor de databasetemplates, zodra het databasespoor ze heeft.
