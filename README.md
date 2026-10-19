# ClusterForge

Eén webinterface om traditionele Linux HA-clusters (Proxmox, Debian/Ubuntu, Keepalived, gedeelde databases, Docker, cronclusters) en Kubernetes-clusters met k3s te beheren, monitoren, deployen en onderhouden. Geen Kubernetes-kloon: voor Kubernetes rolt ClusterForge k3s uit en beheert het.

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
| 8. Verbindingen en ontdekte afhankelijkheden | De TCP-verbindingen die de agents zien, per node en als voorgestelde afhankelijkheden met het aantal verbindingen als bewijs; een onbekende bestemming wordt een voorgestelde externe dienst, en op de kaart staat bij elke pijl hoe vaak de agents hem zagen | klaar |
| 9. Auto-documentatie | Per cluster vijf hoofdstukken (architectuur, netwerk met diagram, configuratie, herstel en runbooks) en een overzicht, als Markdown uit wat er draait; tien minuten na een wijziging en elke nacht bijgewerkt, met de geschiedenis en wat er veranderde, downloaden als zip en afdrukken; zonder geheimen | klaar |
| 10. Documentatie naar GitHub | De documentatie in een eigen GitHub-repository met een eigen token: na elke nieuwe versie één commit met alleen de gewijzigde bestanden, zonder commit als er niets veranderde; nooit in de branch die GitOps leest | klaar |
| 11. Rampoefeningen | In lab en test met opzet een Proxmox-host, een dienst op alle nodes of een VIP laten uitvallen; vooraf de voorspelling van de wat-als, tijdens de storing meten op de VIP's en de status van elk geraakt en afhankelijk cluster, en een rapport met voorspeld tegen gemeten; daarna altijd alles terugzetten. Pagina Tests met alle runs | klaar |
| 12. Storingen met dodemansknop | Een rampoefening als reeks van tot vijf stappen: VRRP blokkeren, een interface down, een TCP-poort dicht, vertraging of pakketverlies, een schijf vullen of een dienst stoppen, op de VIP-houder, de andere nodes, alle nodes of één node. De agent zet elke storing met een lease die de server verlengt en heft hem zelf op als ClusterForge wegvalt, na de bovengrens en na een herstart | klaar |
| 13. DR-repetitie | Een heel cluster terugzetten uit de back-ups, ook op prod: alle nodes tegelijk als sandbox op één host aan een afgesloten bridge, controleren dat het VIP één houder heeft en antwoordt, RTO en RPO meten tegen de doelen per cluster, en daarna alles weer verwijderen; met de hand of elke week of maand in het testvenster | klaar |
| 14. Herstelbaarheid in documentatie en rapport | De gemeten RTO en RPO met datum en doel in het hoofdstuk Herstel, de runbooks met de gemeten hersteltijden, de DR-repetitie in de kaart Herstelbaarheid, en het rapport Herstelbaarheid per maand op `/rapporten`, af te drukken als PDF en elke eerste van de maand bewaard | klaar |
| 15. Assistent bij incidenten, risico en documentatie | Analyseer op een incident geeft in het kort wat er gebeurde, de waarschijnlijke oorzaak met bewijs, wat je nog moet controleren en wat je kunt doen, bewaard bij het incident met model en datum; Leg uit bij een bevinding van de health score en bij een SPOF; een samenvatting 'In het kort' bovenaan de documentatie van een cluster; en automatisch analyseren van nieuwe prod-incidenten, standaard uit | klaar |

| Fase 4: Kubernetes | Inhoud | Status |
| --- | --- | --- |
| 1. Template Kubernetes (k3s) | Een Kubernetes-cluster uitrollen met k3s op Debian-VM's: één, drie of vijf servers met etcd, nul tot tien workers, en één VIP met keepalived voor kubectl en de apps; Traefik, ServiceLB, local-path en metrics-server zitten erin | klaar |
| 2. Kube-brug en overzicht | cf-agent op een k3s-server leest de Kubernetes-API voor ClusterForge (agentprotocol 8), alleen lezen en zonder de waarden van Secrets; een pagina Kubernetes met per cluster een overzicht, de nodes gekoppeld aan de ClusterForge-nodes, de apps en de pods, met een leermodus die uitlegt wat je ziet en het kubectl-commando ernaast zet; na een uitrol wacht ClusterForge tot elke node Ready is | klaar |
| 3. Details, logs en gebeurtenissen | Een pod, app, node of ander object openen: de toestand in gewone woorden, de keten van Deployment tot de VM op de Proxmox-host en van Service en Ingress, de containers, condities, events en YAML; de logs van een container, ook van de vorige na een herstart; tabbladen Netwerk, Opslag, Configuratie, Gebeurtenissen en Alles, met de soorten van geïnstalleerde apps zoals IngressRoute van Traefik | klaar |
| 4. Acties | Een app schalen, herstarten of terugdraaien naar een vorige versie, een object weghalen, een node op cordon zetten, weer pods geven of leegmaken; alles in het logboek, op prod met de slug en tweestapsverificatie; een herstart of onderhoud van een node via ClusterForge maakt hem in Kubernetes eerst leeg en geeft hem daarna weer pods | klaar |
| 5. Apps uitrollen | Een tabblad Toepassen met drie manieren: een formulier Nieuwe app dat een Deployment, Service en Ingress maakt met de YAML ernaast, een Helm-chart via de Helm-controller van k3s, of eigen YAML met voorbeelden; Controleren is een dry-run met het verschil per object en de fouten bij het veld, pas Toepassen verandert iets; een webadres onder nip.io wijst zonder eigen DNS naar het VIP | klaar |
| 6. Lessen | Een tabblad Lessen met acht lessen, van je cluster verkennen tot een node in onderhoud; elke opdracht kan in ClusterForge of met kubectl, en ClusterForge kijkt op het cluster of ze gelukt is; de voortgang blijft per gebruiker bewaard, en Opruimen haalt de namespace leren weg | klaar |
| 7. Nodes toevoegen en weghalen | Op het tabblad Nodes een worker toevoegen of naar drie of vijf servers gaan, met een nieuwe revisie van de spec; een worker weghalen als taak die hem leegmaakt, uit Kubernetes haalt en de VM verwijdert; de spec van een cluster wijzigen in de webinterface; les 9 en 10 | klaar |
| 8. Status, incidenten en metrics | Een Kubernetes-cluster is down als de API niet antwoordt en verminderd bij een node die NotReady is of een app die niet doet wat hij moet; zo'n app opent een incident, What Broke leest de waarschuwingen van Kubernetes en ziet een nieuwe image als uitrol; CPU en geheugen per pod en node elke minuut in VictoriaMetrics, met grafieken | klaar |
| 9. kubectl op je eigen pc | Een kubeconfig per gebruiker voor kubectl, k9s, Headlamp of Lens, met een eigen ServiceAccount en een token dat verloopt; beheerder of alleen lezen, intrekken werkt meteen, en verlopen kubeconfigs ruimt ClusterForge op | klaar |
| 10. etcd-snapshots | Een tabblad Snapshots met de snapshots die k3s van zijn database maakt, per server, en hun stand bij Back-ups; Snapshot nu; in lab en test het hele cluster terugzetten naar een snapshot als taak die stap voor stap doet wat k3s beschrijft; op prod een runbook met de commando's | klaar |
| 11. Bijwerken | Template 1.1.0 met k3s 1.36; op het overzicht welke k3s elke node draait en Bijwerken naar de volgende versie, één minorversie per keer en nooit terug; de uitrol gaat node voor node, eerst de servers, maakt elke node eerst leeg, wacht tot hij Ready is met de nieuwe versie en stopt bij de eerste node die niet terugkomt | klaar |
| 12. Score en documentatie | Een scorekaart Kubernetes bij de health score: een node die NotReady is, één server op prod, geen of een te oude etcd-snapshot, een k3s zonder updates, een app die blijft herstarten, en apps op prod met één pod, met een image zonder vaste versie of zonder geheugenlimiet; een hoofdstuk Kubernetes in de documentatie met de versie, nodes, namespaces, apps, webadressen, opslag en snapshots, en de runbooks voor een node vervangen, bijwerken en etcd terugzetten | klaar |
| 13. Assistent voor Kubernetes | Drie tools voor de assistent, alleen lezend: een cluster live, een object met zijn events, keten en YAML, en de laatste 200 logregels van een pod; Leg uit bij elk object en elke waarschuwing, zoals 'Waarom start deze pod niet?'; Ik zit vast bij elke opdracht van een les, met een hint zonder het antwoord; wachtwoorden en tokens in logs en YAML worden gemaskeerd, en de waarden van Secrets komen nooit bij de assistent | klaar |

| Fase 5: databaseclusters per app | Inhoud | Status |
| --- | --- | --- |
| 1. Databases lezen | cf-agent leest MariaDB en PostgreSQL op de node zelf (agentprotocol 9), alleen met vaste query's en zonder de inhoud van tabellen; een pagina Databases met per cluster een Galera-cluster, een primary met replica's of een losse server, de toestand van elke node in gewone woorden en de databases met hun grootte | klaar |
| 2. Wie gebruikt welke database | De agent ziet elke minuut welke gebruiker vanaf welk adres met welke database verbonden is; ClusterForge koppelt de adressen aan nodes en clusters en toont per database wie haar de laatste dertig dagen gebruikte, met een waarschuwing als meer dan één cluster dat doet en of de afhankelijkheid in de graaf staat | klaar |
| 3. Galera als onderdeel | Database toevoegen op een cluster dat er al is, ook met CT's: een plan per node met systeem, geheugen (met het `pct set`-commando als het krap wordt), schijf en Proxmox-host, en blokkades zoals twee nodes zonder scheidsrechter; daarna zet ClusterForge MariaDB Galera node voor node op en wacht na elke node tot hij Synced is; MariaDB komt in de graaf en de drift ziet een met de hand aangepaste configuratie | klaar |
| 4. Het VIP van de app voor de database | Kies bij Database toevoegen het VIP van de app; keepalived zet het dan alleen op een node waarvan MariaDB mag schrijven (`cf-agent check db`), zodat de app altijd naar één node schrijft. Het plan toont per node de diff van keepalived.conf; ClusterForge past het bestand aan na `keepalived -t`, herlaadt node voor node met de eigenaar van het VIP laatst, en blokkeert een node met het VIP maar zonder database | klaar |
| 5. Opkomen na een herstart | ClusterForge kijkt om de 20 seconden of de database draait. Na een volledige uitval, zoals een reboot van alle nodes, begint het het Galera-cluster vanzelf opnieuw op de node met de nieuwste data (uit `grastate.dat`, of na een crash met `galera_recovery`), maar alleen als alle nodes er zijn en MariaDB nergens meer start; ontbreekt er een, dan wacht het en kies je zelf Opnieuw starten, met een plan per node. Een node die niet startte terwijl het cluster draait, start het daar opnieuw. Getest met drie echte MariaDB-containers | klaar |
| 6. Databases en gebruikers | Bij **Databases en gebruikers** op de kaart Database staan de databases met grootte, tabellen en verbindingen, en de gebruikers met hun hosts en rechten, zonder wachtwoord. **Database aanmaken** maakt een database met een eigen gebruiker en een sterk wachtwoord dat één keer op het scherm staat, samen met wat de app nodig heeft; ClusterForge bewaart het niet. **Nieuw wachtwoord** en **Weghalen** (met de naam ter bevestiging, op prod ook de slug en tweestapsverificatie). Getest tegen een echte MariaDB 11.8 | klaar |
| 7. Back-ups van de databases | Elke nacht om 02:00 een dump van elke database, op een node zonder het VIP, met zstd in `/var/backups/clusterforge-db` (alleen voor root) en zeven dagen bewaard. ClusterForge controleert elke dump: compleet, alle tabellen, en niet meer dan de helft kleiner dan de vorige. Bij Back-ups en op de pagina Databases en gebruikers staat per database de leeftijd van de nieuwste goede dump. **Dump nu** voor één database, en **Terugzetten** in een nieuwe database naast de bestaande, als `<naam>_herstel_<datum>`. Getest tegen een echte MariaDB 11.8 | klaar |
| 8. Verhuizen: plan en proef | Bij Databases **Verhuizen…** naast elke MariaDB-database: een plan met de grootte, tabellen en objecten, wie de database gebruikt, het cluster waar ze naartoe gaat en wat in de weg staat, zoals tabellen zonder primaire sleutel of MyISAM (met de commando's), een firewall tussen de nodes, te weinig schijf of een te kleine buffer pool. De **proefverhuizing** importeert een kopie als `<naam>_proef` terwijl de app doorwerkt, telt de rijen en objecten aan beide kanten, meet hoe lang het duurde en haalt de kopie weer weg. **Database erbij** bij Cluster uitrollen zet na de uitrol meteen MariaDB Galera op de nodes. Getest met een echte MariaDB 10.11 als oude server en 11.8 als nieuwe | klaar |
| 9. Verhuizen: uitvoeren | Met de app in onderhoud zet **Verhuizing starten** de database onder dezelfde naam op het nieuwe cluster, met dezelfde gebruikers en dezelfde hash van hun wachtwoord, dus de app houdt haar wachtwoord. Elke tabel moet aan beide kanten evenveel rijen hebben; wijkt er iets af, dan gaan de kopie en de nieuwe gebruikers weg en staat de app nog op de oude database. Na **De app draait op de nieuwe database** mogen de gebruikers van de app de oude database alleen nog lezen; één knop haalt het slot eraf. Getest met een echte MariaDB 10.11 als oude server en 11.8 als nieuwe | klaar |
| 10. PostgreSQL met Patroni | Bij Database toevoegen ook **PostgreSQL met Patroni**: PostgreSQL uit Debian (15 op Debian 12, 17 op Debian 13) met Patroni, en etcd 3.5 (uit de GitHub-release met een vaste SHA-256) op dezelfde nodes, dat bijhoudt wie de primary is; met twee nodes stemt een scheidsrechter alleen in etcd mee. ClusterForge zet eerst etcd op alle nodes en dan Patroni node voor node, en wacht tot elke replica de primary bijhoudt; één replica is synchroon, zodat een failover geen bevestigde schrijfactie verliest. PostgreSQL krijgt een **eigen VIP** dat altijd op de primary staat (`cf-agent check db postgresql`), in een eigen bestand in `/etc/keepalived/conf.d`; keepalived.conf krijgt alleen een include. MariaDB kan nu ook een eigen VIP krijgen in plaats van dat van de app. Een leeg cluster main van Debian haalt ClusterForge weg; een PostgreSQL met databases blokkeert. De wachtwoorden van Patroni staan alleen voor root leesbaar. Getest met echte Patroni, etcd en PostgreSQL 16: een harde uitval van de primary geeft binnen ongeveer 20 seconden een nieuwe zonder verlies, de oude primary komt terug als replica, en na een uitval van alle nodes komt het cluster vanzelf terug. **Databases en gebruikers** en **Dumps** werken ook voor PostgreSQL: een database met een eigen gebruiker als eigenaar, de nachtelijke dump met `pg_dump` op de primary, en terugzetten naast de bestaande database, ook na een failover. **Verhuizen…** werkt ook van een andere PostgreSQL naar Patroni: de rollen gaan mee met hun SCRAM-verifier, dus de app houdt haar wachtwoord, ook de groepsrollen en hun leden; het plan geeft de regel voor `pg_hba.conf` van de oude server en zegt welke extensies op de nieuwe nodes ontbreken; het slot zet de oude database op alleen lezen. Getest tegen een echte PostgreSQL 16 | klaar |
| 11. Beheer | **Beheer…** op de kaart Database toont per node de toestand en wat er kan, met de reden als het niet kan en wat er gebeurt als je het doet. Bij Galera **Opnieuw synchroniseren**: de node gooit zijn positie weg en haalt alle data opnieuw bij een node die Synced is; niet op de enige node die Synced is. Bij Patroni **Primary maken**, alleen voor de synchrone replica zodat er niets verloren gaat, met het VIP mee, en **Opnieuw opbouwen** van een replica met `pg_basebackup`. **Onderhoud en herstarten** van een node met een database: is hij de primary van Patroni, dan schakelt ClusterForge eerst over; na de herstart of het onderhoud wacht de taak tot de node weer meedoet; zou de database stoppen, haar meerderheid in etcd verliezen of het VIP geen node hebben, dan vraagt ClusterForge eerst of je toch door wilt (agentprotocol 12). Getest met echte Patroni en etcd. **Instellingen…** (zoals de buffer pool, shared buffers en max_connections) wordt een nieuwe revisie die node voor node toegepast wordt, de schrijver als laatste; bij Patroni komt max_connections in etcd en herstart PostgreSQL node voor node, de primary als laatste. **Bijwerken…** werkt MariaDB of PostgreSQL node voor node bij binnen de versie van Debian, zoals 11.8.3 naar 11.8.4: apt-get update, alleen de pakketten van de database, en een herstart alleen waar iets veranderde; voor de primary schakelt ClusterForge eerst over. **Node toevoegen…** brengt de database naar een nieuwe node van het cluster, ook een CT die je zelf maakte: Galera haalt de data met SST, Patroni maakt de node lid van etcd en een replica; de nodes die er al zijn, herstarten niet. Getest met een echte vierde node van Galera, Patroni en etcd | klaar |
| 12. Status, incidenten en grafieken | ClusterForge bewaakt nu ook PostgreSQL met Patroni om de 20 seconden. De database telt mee in de status van het cluster: down zonder Primary component of primary, of als het VIP op een node staat waar de database niet kan schrijven; verminderd als een node niet meedoet of niet Synced is, een replica meer dan 30 seconden achterloopt of de scheidsrechter weg is. Zo'n overgang geeft een incident. In het logboek en in What Broke komen een herstart van MariaDB of PostgreSQL, een failover van Patroni en een state transfer van Galera met de donor. **Grafieken** op de kaart Database: verbindingen, query's of transacties per seconde, de buffer pool of cache-hits, de wachtrij en flow control van Galera, de achterstand van de replica's en de grootte per database, met een lijn per node (agentprotocol 13) | klaar |
| 13. Failovertests met database | Een nieuw scenario van de failovertest, **de database stoppen**, op het VIP van een onderdeel Database: ClusterForge stopt MariaDB of Patroni op de node met het VIP en meet hoe snel een andere node overneemt. Bij elke test die een database raakt (haar VIP verplaatsen, of een datanode uitzetten) schrijft een andere datanode elke halve seconde een rij via het VIP in een eigen kleine database `clusterforge_probe` (agentprotocol 14). Het rapport zegt hoe lang schrijven niet lukte en of er een bevestigde rij verloren ging, zoals 'schrijven via 10.0.20.185 lukte weer na 9 s, er ging niets verloren'; een verloren rij is altijd FAIL. Daarna wacht de test tot de database weer gezond is. Een database die ClusterForge niet beheert, houdt een cluster nog steeds buiten de failovertest. De test **[alles herstarten](#alles-herstarten)** herstart alle nodes van de database tegelijk, zoals na een stroomonderbreking, en meet hoe lang het duurt tot ze weer gezond is; bij Galera begint ClusterForge het cluster opnieuw zoals de bewaking, en daarna kijkt de agent na of elke rij die de database voor de herstart bevestigde er nog is (agentprotocol 15). Mag ook op prod, met de hand. De [wat-als](#wat-als) kent de meerderheid van Galera en etcd: 'zonder nc2 en nc3 is er geen meerderheid (1 van de 3 leden), dus MariaDB stopt', en dan zijn ook Nextcloud en het cluster down | klaar |
| 14. Het oude cluster afbouwen | Een verhuisde database blijft op slot op het oude cluster staan. Zeven dagen nadat niemand van buiten de databasenode er nog mee verbond, kan **[Oude database afbouwen](#het-oude-cluster-afbouwen)**: een laatste dump op een node van het oude cluster, die blijft staan tot je hem weghaalt, en daarna de database en de gebruikers die niets anders hebben weghalen, met de naam ter bevestiging (op prod ook de slug en tweestapsverificatie). Bovenaan de pagina Databases staat per oude server wat er nog op staat, met de melding als db1 tot db3 leeg zijn, en welke firewallregel van een app naar het oude cluster weg kan. ClusterForge verwijdert geen nodes en beheert de firewall niet. Getest met een echte MariaDB 10.11 en PostgreSQL 16 | klaar |
| 15. Score, documentatie en assistent | Een scorekaart **Database** bij de health score, voor elk cluster met een databaseserver op zijn nodes: geen HA, een node die niet meedoet of een replica die achterloopt, twee databasenodes op dezelfde Proxmox-host, te weinig vrij geheugen, dumps die uit staan of ouder zijn dan een dag, Galera-tabellen zonder primaire sleutel of buiten InnoDB (de nachtelijke dump legt ze vast), geen geslaagde failovertest in 90 dagen, een database die meer dan één cluster gebruikt, en een verhuisde database die na 30 dagen nog niet afgebouwd is. Een hoofdstuk **Database** in de documentatie per cluster met de engine en versie, de nodes, het VIP, de databases met grootte, gebruikers en dumps, en runbooks voor opnieuw starten, een node opnieuw synchroniseren, een dump terugzetten, overschakelen en verhuizen. Twee tools voor de [assistent](#assistent-bij-databases) die alleen lezen: **database** met de toestand van een onderdeel, en **database_clients** met wie welke database gebruikt; de analyse van een incident op een cluster met een database kijkt daar ook naar | klaar |

| Fase 6: cronjobs en bestandssync | Inhoud | Status |
| --- | --- | --- |
| 1. Cronjobs lezen | cf-agent leest elke cronjob en systemd-timer op de node (agentprotocol 16): `/etc/crontab`, `/etc/cron.d`, de crontabs van gebruikers, `cron.hourly` tot `cron.monthly` en de timers, met de laatste run uit de journal; wachtwoorden in commando's worden op de node al gemaskeerd. Een pagina **[Cronjobs](#cronjobs)** met per cluster elke job één keer, het schema in gewone taal, de volgende run, op welke nodes hij staat (en waar niet) en hoe de laatste run ging; een kaart Cronjobs op de pagina van een node. Een job die bijkomt, verdwijnt of verandert, staat in het logboek en in What Broke | klaar |
| 2. Verschillen, waarschuwingen en fouten | Een job die op een node anders staat, toont het schema of commando van die node. Waarschuwingen bij gevaarlijke jobs naast een onderdeel van ClusterForge, zoals een Galera-bootstrap-cron naast het onderdeel Database (rood), een cron die de database herstart, of een script dat zelf een PostgreSQL-primary kiest; een rsync-cron krijgt de tip dat hij straks beheerd kan. Een job die mislukt, geeft een event en een incident tot hij weer lukt. Met de knop **Fouten van cron vastleggen** zet ClusterForge `-L 15` aan in een drop-in van cron.service (agentprotocol 17) | klaar |
| 3. Beheerde cronjobs | Eigen cronjobs in ClusterForge: toevoegen, wijzigen, aan- en uitzetten, weghalen en **Nu uitvoeren**, op de node met het VIP (verhuist mee), een vaste node of alle nodes. Elke job is een systemd-timer met `cf-agent cron-run` (agentprotocol 18), met slot, tijdslimiet en per run de exitcode en de laatste 64 KiB uitvoer | klaar |
| 4. Opvolgen | De uitvoer van een lopende run live volgen, een run stoppen, tot drie keer opnieuw proberen na een fout, een incident per job tot hij weer lukt (per job uit te zetten) en een melding als een geplande run niet startte (agentprotocol 19) | klaar |
| 5. Overnemen | Bij een gevonden cronjob **Overnemen in ClusterForge** (het formulier staat ingevuld) en **Uitzetten** / **Weer aanzetten**: de regel gaat in commentaar met `# uitgezet door ClusterForge`, op elke node waar hij stond (agentprotocol 20) | klaar |
| 6. Bestandssync | Een beheerde job van de soort **Bestandssync**: rsync over SSH van de node met het VIP (of één vaste node) naar de andere nodes van het cluster of naar alle nodes van een ander cluster, met per doel het aantal bestanden en bytes, een kaart **Klaar voor de sync?** per node en een **proefrun** (`rsync --dry-run`). ClusterForge zet de sleutels zelf: elke bron een eigen sleutel, op elk doel een regel in `authorized_keys` die alleen `cf-agent sync-receive` naar die ene map toelaat (agentprotocol 21) | klaar |
| 7. Documentatie en assistent | Een hoofdstuk **Cronjobs** in de documentatie van elk cluster, met de beheerde jobs en syncs, wat er verder op de nodes staat, en runbooks om een run na te kijken, [een sync terug te draaien](#bestandssync), een sync weg te halen en een uitgezette job weer aan te zetten. Twee tools voor de assistent die alleen lezen: **cronjobs** met de beheerde en gevonden jobs van een cluster, en **cron_runs** met de runs van één job en het einde van de uitvoer van de laatste mislukte run (gemaskeerd; een kijker ziet de uitvoer van een job als root niet) | klaar |

| Fase 7: zelfherstel | Inhoud | Status |
| --- | --- | --- |
| 1. Vaststellen | cf-agent bekijkt elke minuut MariaDB, MySQL en PostgreSQL op de node (agentprotocol 22): de unit volgens systemd, de processen die echt draaien, de vrije ruimte in de datamap, de Galera-adressen uit de config met het bestand en de regel, en de config van Patroni. Bij elke database op de pagina **Databases** een kaart **[Zelfherstel](#zelfherstel)** met wat er mis is, op welke node en waarom, zoals "Galera zoekt de andere nodes op verkeerde adressen", "MariaDB op db1 hangt" en "Geen node zit in een Primary component", met het herstelplan en of het vanzelf mag; ook voor databases die ClusterForge niet toepaste | klaar |
| 2. Nu herstellen | **Nu herstellen** voert het plan uit als taak met een stap per node: de Galera-adressen bijwerken (met een kopie `<bestand>.cf-zelfherstel-<tijd>`), een hangend proces stoppen (TERM, na een minuut KILL, alleen in de cgroup van de unit), de server starten, of bij Galera zonder Primary component het cluster opnieuw beginnen op de node met de nieuwste data en de andere laten aansluiten tot ze Synced zijn. Alleen het plan dat je zag, op prod met de slug en tweestapsverificatie; een onderdeel van ClusterForge herstel je met zijn eigen acties | klaar |
| 3. Vanzelf herstellen | Per cluster **Vanzelf herstellen op dit cluster**, standaard uit en nooit op prod: een plan dat vanzelf mag, gebeurt als het probleem twee minuten bestaat, hoogstens drie keer per uur, een kwartier na een mislukte poging, het Galera-cluster opnieuw beginnen hoogstens eens per uur, en na drie mislukte pogingen op rij pauze tot je het opnieuw aanzet. Adressen wijzigen gebeurt nooit vanzelf | klaar |
| 4. Breder | PostgreSQL en Patroni: een gestopte of hangende unit, en een Patroni die etcd niet bereikt of een adres opgeeft dat de node niet heeft, met het bestand en de regel (die config wijzigt ClusterForge niet). MySQL zonder Galera starten en opvolgen. **Database toevoegen** weigert een node waarop MariaDB of PostgreSQL staat maar stilstaat, met de verwijzing naar Zelfherstel | klaar |
| 5. Documentatie, What Broke en de assistent | Het hoofdstuk **Herstel** in de documentatie van elk cluster met een database heeft een sectie **Zelfherstel**: of Vanzelf herstellen aan staat, en wat Zelfherstel de laatste 30 dagen deed, met wat er mis was, met de hand of vanzelf, en hoe het afliep. Een herstel staat in de tijdlijn van What Broke als **zelfherstel** met een link naar de taak; begon het na de storing, dan is het een antwoord en geen oorzaak. Bij het incident van het cluster staan de herstelacties ook als ze na het halfuur van de analyse komen. De assistent leest Zelfherstel met de tool **heal**, die alleen leest | klaar |

## Draaien met Docker Compose

Stap voor stap in een Debian-container op Proxmox: [docs/install-proxmox-lxc.md](docs/install-proxmox-lxc.md). Daar staan ook [back-up en herstel van ClusterForge zelf](docs/install-proxmox-lxc.md#back-up-en-herstel-van-clusterforge-zelf) en [bijwerken](docs/install-proxmox-lxc.md#bijwerken). `docker compose ps` toont de server als `healthy` zodra hij antwoordt (`clusterforge-server healthcheck`).

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
| `CF_TEST_WINDOW` | `zo 03:00-05:00` | Het testvenster voor geplande failovertests, back-upcontroles en DR-repetities: dagen (`ma` tot `zo`, met komma's, of `dagelijks`) en de tijden, op de klok van `TZ` |
| `CF_JOURNAL_SSH_LOGINS` | `true` | Bewaar SSH-logins (gebruiker, bron-IP en methode) in het journaal van de nodes; `false` laat ze weg |
| `CF_DOCS_USERNAMES` | `true` | Zet in de documentatie bij elke revisie wie hem maakte; `false` laat de gebruikersnamen weg |
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

De server moet `https://` zijn: over http kan wie het verkeer onderschept zich als ClusterForge voordoen en commando's als root naar de node sturen. Gebruik je in een vertrouwd netwerk toch http, voeg dan `--allow-http` toe (de webinterface doet dat zelf als je hem over http opent; een uitrol vanuit een golden image volgt het adres dat je bij Uitrollen instelde).

Het script downloadt `cf-agent` van je eigen server, controleert de checksum, meldt de node aan en start de systemd-service `cf-agent`. Bestaat er nog geen node met die hostname, dan maakt de aanmelding er een aan. De agent stuurt elke 10 seconden een heartbeat, elke 15 seconden metrics en elk kwartier (en bij elke nieuwe verbinding) zijn facts: OS, kernel, CPU, geheugen, schijven, netwerk, services, updates, Docker, Keepalived en k3s.

```text
cf-agent enroll -server URL -token cfe_...      meldt deze machine aan (doet het installatiescript; -allow-http voor http)
cf-agent run                                    verbindt met ClusterForge (zo start systemd hem)
cf-agent facts                                  toont de facts van deze machine
cf-agent fault-clear                            heft achtergebleven storingen van een rampoefening op
cf-agent self-check                             versie, protocol en architectuur als JSON
cf-agent upgrade-rollback                       zet de oude agent terug (doet de timer cf-agent-rollback)
cf-agent version                                toont de versie
```

De sleutel van de agent staat in `/etc/clusterforge/agent.json`. Intrekken kan bij de node in de webinterface; de verbinding valt dan meteen weg.

Een nieuwere agent zet je erop zonder opnieuw aan te melden:

```sh
curl -fsSL https://clusterforge.example/install/agent.sh | sudo sh -s -- --server https://clusterforge.example --upgrade
```

Het script controleert de checksum, vervangt alleen de binary en herstart `cf-agent`; `agent.json` blijft staan. Mislukt de download of de checksum, dan blijft de oude agent draaien. Vanaf **agentprotocol 23** hoeft dit maar één keer: daarna werken de agents zichzelf bij (zie hieronder). Sinds de storingen van [rampoefeningen](#reeksen-van-storingen) zet het script ook `cf-agent-fault-clear.service` neer, die bij elke start van de machine achtergebleven storingen opheft voordat het netwerk opkomt. Voor een reeks heeft de node `nft` (pakket `nftables`) en `tc` (pakket `iproute2`) nodig.

### Agents werken zichzelf bij

Vanaf agentprotocol 23 werkt ClusterForge de agents zelf bij naar de agent die de server meebrengt. Bij Nodes staat de kaart "Agents bijwerken" zodra er een agent ouder is, of een andere build met hetzelfde protocol. "Agents bijwerken" doet alles buiten prod, per prodcluster is er een eigen knop die tweestapsverificatie en de slug vraagt; bij een node staat "Agent bijwerken" in de kaart Agent. Het wordt een taak per cluster, één node tegelijk: de volgende pas als de vorige met de nieuwe agent terug is. Komt een node niet terug, of zet hij de oude terug, dan stopt de taak van dat cluster, met een regel in het logboek. Een node die wegblijft, staat offline en opent een incident zoals elke node.

Wat de agent zelf doet:

1. hij haalt `/downloads/cf-agent-linux-<arch>` van zijn eigen server en controleert de sha256 die over de vastgepinde NATS-verbinding meekwam (de download zelf mag dus over http);
2. hij start de nieuwe met `cf-agent self-check`, die hier moet draaien en geen lager protocol mag melden: nooit terug naar een oudere versie;
3. hij bewaart zichzelf als `/usr/local/bin/cf-agent.prev` en zet met `systemd-run` een tijdelijke timer `cf-agent-rollback`;
4. hij vervangt zichzelf in één keer en herstart `cf-agent`;
5. de nieuwe agent zet de timer uit zodra de server iets van hem aanneemt; pas dan telt de server de node als bijgewerkt. Gebeurt dat niet binnen 3 minuten, dan zet de timer de oude agent terug en meldt die dat aan de server.

Een update die mislukt (teruggezet, of de download, de self-check of de herstart lukt niet) probeert ClusterForge niet nog eens vanzelf naar dezelfde agent; de knop wel.

Vanzelf bijwerken staat standaard aan: elke minuut start de server een taak voor de clusters met verouderde, verbonden agents. Nooit op prod. Uitzetten, voor alles of per cluster, kan bij Instellingen → Agents bijwerken. Agents onder protocol 23 kennen dit nog niet: die werk je één keer bij met `--upgrade` hierboven; de kaart toont het commando. Het golden image hoeft daarna niet meer na elke update opnieuw: een nieuwe VM meldt zich aan en wordt bijgewerkt (op prod met de knop). Na een reboot binnen die 3 minuten zet de nieuwe agent de timer terug; een agent die al bij het starten crasht, kan dat niet: zet de oude dan terug met `sudo /usr/local/bin/cf-agent.prev upgrade-rollback`. `agent.sh --upgrade` helpt daar niet, dat haalt dezelfde agent opnieuw. Het ontwerp staat in [docs/design/agent-zelfupdate.md](docs/design/agent-zelfupdate.md).

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

Voor een Kubernetes-cluster gelden ook de regels van de API, de nodes in Kubernetes en de apps; zie [Status, incidenten en metrics](#status-incidenten-en-metrics).

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

Van de databases die ClusterForge zelf op een cluster zette, maakt het wel elke nacht een dump; bij Back-ups staat per database hoe oud de nieuwste goede is. Zie [Dumps](#dumps).

Van een Kubernetes-cluster staan bij Back-ups ook de etcd-snapshots die k3s zelf maakt: per cluster en per server hoe oud de nieuwste is. Zie [etcd-snapshots](#etcd-snapshots).

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

Een template beschrijft een volledig cluster: hoeveel VM's, hoe groot, welke software erop komt en hoe ClusterForge achteraf controleert dat het werkt. De templates zitten in de server ingebouwd; bij Templates zie je ze met hun parameters en de diensten die ze in de graaf zetten. Er zijn er vijf, beschreven onder [De templates](#de-templates).

Wat je nodig hebt:

1. Een gekoppelde Proxmox, met een token dat VM's mag maken (zie de rol hierboven).
2. Een golden image: een VM-template met Debian 13, cloud-init, de QEMU guest agent en cf-agent. Maak hem als root op een Proxmox-host met het script dat ClusterForge zelf serveert:

   ```sh
   curl -fsSL https://clusterforge.example/install/golden-image.sh | bash -s -- --server https://clusterforge.example --storage local-lvm
   ```

   Het script haalt het cloud-image van Debian, zet er qemu-guest-agent en cf-agent in met `virt-customize` (uit `libguestfs-tools`, dat het zo nodig installeert) en maakt VM-template 9000. Met `--vmid`, `--storage`, `--bridge` en `--name` kies je iets anders, en `--replace` vervangt een eerdere template. Op gedeelde storage (Ceph, NFS) verdeelt ClusterForge de VM's over de hosts; op lokale storage komen ze allemaal op de host van de template.
3. De nieuwe VM's moeten ClusterForge kunnen bereiken: het webadres om zich aan te melden, en poort 4222 voor NATS. Voor Kubernetes hebben ze ook internet nodig, voor k3s van GitHub en de images van je apps.

Klik bij Clusters op "Cluster uitrollen", kies de template en vul de naam, de parameters (zoals het VIP), de golden image en het netwerk in. Onderaan zie je meteen welke nodes er komen, met hun adres en Proxmox-host; een fout staat bij het veld. Na "Uitrollen" loopt de taak:

1. Per node de VM klonen, cores, geheugen, netwerk en cloud-init instellen, de schijf vergroten en starten.
2. De agents aanmelden: wachten op de guest agent, via de guest agent een eenmalig aanmeldbestand in de VM zetten (`/etc/clusterforge/enroll.json`) en wachten tot cf-agent zich meldt.
3. Per node de stappen van de template: pakketten, bestanden en services. Elke stap meldt of hij iets veranderde.
4. Controleren: een node heeft het VIP, bij Nginx met keepalived en Eigen applicatie antwoordt het VIP over HTTP, en bij Kubernetes geeft Traefik 404 op het VIP.
5. De nodes worden actief en tellen mee voor de status van het cluster.

Vink je **Database erbij** aan (niet bij Kubernetes), dan zet ClusterForge na een gelukte uitrol MariaDB Galera op alle nodes van het nieuwe cluster, zoals [Database toevoegen](#een-database-op-de-nodes-van-een-cluster) dat doet, om er daarna een database naartoe te [verhuizen](#een-database-verhuizen). Galera heeft minstens drie nodes nodig; met minder, of als het plan iets anders blokkeert, komt de database er niet en staat de reden in het logboek.

Loopt een stap mis, dan staat bij de taak waarom. Los het op en klik op "Opnieuw proberen"; de taak gaat verder bij de stap die misliep. Wil je het cluster niet meer, verwijder dan het cluster en de nodes, en de VM's in Proxmox; ClusterForge ruimt in deze versie niets vanzelf op. Geheimen van een template, zoals het VRRP-wachtwoord, staan versleuteld met `CF_MASTER_KEY` in de database en nooit in de taak of het logboek.

Een eigen golden image kan ook: installeer cf-agent erin met `agent.sh --server https://clusterforge.example --no-enroll`. De agent meldt zich dan aan zodra ClusterForge het aanmeldbestand in de nieuwe VM zet. Zorg ook voor cloud-init en qemu-guest-agent, en maak `/etc/machine-id` leeg voor je er een template van maakt.

### De templates

Alle templates behalve Kubernetes installeren hun software uit Debian 13, beheren alleen wat in hun stappen staat, en gebruiken geen commando's: elke stap is daardoor ook in de driftcontrole te zien. De vier met een VIP geven het met keepalived aan de node met de hoogste prioriteit waarop de bewaakte dienst draait, en na herstel keert het terug naar de eerste node.

- **Nginx met keepalived** (`keepalived-nginx`). Twee tot vijf webservers met Nginx achter één VIP. Je eigen site zet je in `/var/www/html`.
- **Docker-hosts** (`docker`). Een tot tien servers met `docker.io` en `docker-compose` (Compose v2, als `docker compose`) en een eigen `/etc/docker/daemon.json` met logrotatie (standaard 3 bestanden van 50 MB per container) en `live-restore`, zodat containers blijven draaien als Docker herstart. Je stacks zet je in `/opt/stacks`; welke containers er draaien, beheer je zelf, en die tellen niet mee voor drift. Er is geen VIP.
- **Cron-cluster** (`cron`). Twee tot vijf servers met cron en keepalived; de node met het VIP draait de taken. Zet op elke node dezelfde taken, met `cf-leader` ervoor, bijvoorbeeld in `/etc/cron.d/taken`:

  ```
  */5 * * * * root /usr/local/bin/cf-leader /usr/local/bin/opruimen.sh
  ```

  `cf-leader` voert het commando alleen uit als deze node het VIP heeft, en doet anders niets. Valt de node of cron uit, dan draaien de taken binnen enkele seconden op de volgende. Bij een split-brain hebben twee nodes het VIP en kan een taak dubbel draaien; de status van het cluster toont dat als split-brain. Je taken zelf beheert ClusterForge niet.
- **Eigen applicatie** (`generic`). Een container-image, zoals `ghcr.io/jonas/app:1.4.2`, op twee tot vijf servers achter één VIP. Elke node draait hem als systemd-dienst `cf-app` met `docker run`, met de poort op het VIP en de node naar de poort in de container. Instellingen zet je per node als `NAAM=waarde` in `/etc/cf-app/app.env` (alleen voor root leesbaar); daarna `systemctl restart cf-app`. Na de uitrol moet `http://<VIP>:<poort><pad>` 200 geven. Een image uit een registry met wachtwoord kan nog niet. Kan een node de image niet ophalen, dan start de container met de image die er al staat. Deze template heeft een agent van deze versie nodig, die `cf-app` volgt en systemd de nieuwe unit laat lezen; werk daarom eerst je golden image bij.

- **Kubernetes (k3s)** (`kubernetes`). Een Kubernetes-cluster met k3s; zie [Kubernetes](#kubernetes).

Een uitrol zet de diensten van de template meteen in de [afhankelijkheidsgraaf](#afhankelijkheden): docker als container, cron met keepalived, en app met docker en keepalived, met de poort die je koos.

### Gewenste staat

Elk cluster uit een template heeft een specificatie, de gewenste staat: de templateversie, de parameters, het netwerk en de nodes. Daarmee berekent ClusterForge op elk moment voor elke node opnieuw wat er hoort te staan, precies zoals bij de uitrol. Geheimen staan er niet in, alleen dat ze versleuteld opgeslagen zijn. Elke wijziging is een nieuwe revisie. Bij een cluster zie je onder Specificatie de huidige parameters en de historie, met per revisie wanneer ze gemaakt is, door wie, waarvandaan (webinterface, API of Git) en wat er veranderde. Wat niet klopt staat erboven: een node die niet meer bij het cluster hoort, een actieve node die niet in de specificatie staat, of een templateversie die deze server niet kent.

Een beheerder wijzigt de specificatie van een cluster dat geen Git volgt met **Wijzigen…** op de kaart Specificatie. Je past de parameters aan, of kiest een andere templateversie; geheimen en wat na de uitrol vastligt, zoals het VIP, staan grijs. Daarna zie je hetzelfde plan als bij een wijziging uit Git (zie [GitOps](#gitops)): de velden oud en nieuw, de nodes die erbij komen met hun adres, en per node in de volgorde van toepassen welke stappen veranderen. Pas bij Toepassen komt er een revisie met bron webinterface, en een taak die het plan uitvoert, met nieuwe nodes zoals bij [omhoog schalen](#omhoog-schalen-en-nieuwe-clusters). Verandert er op geen node iets, dan geldt de revisie meteen als toegepast. Op prod vraagt Toepassen de slug en tweestapsverificatie. Minder nodes kan hier niet: daarvoor zijn de lifecycle-acties, en bij Kubernetes Weghalen op het tabblad Nodes. Volgt een cluster Git, dan is `cluster.yaml` de bron en staat Wijzigen dicht.

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

Een cluster uit een ingebouwde template kun je beschrijven in een GitHub-repository: per cluster een bestand `clusters/<slug>/cluster.yaml`. ClusterForge leest de repository elke minuut, controleert elk bestand en toont voor een wijziging wat ze op elke node zou doen. Pas als een beheerder het plan goedkeurt, verandert er iets op de nodes. GitOps schrijft nooit naar Git. De documentatie-export schrijft alleen in de map en branch die je zelf instelt, met een eigen token (zie [Naar GitHub](#naar-github)).

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

## Kubernetes

ClusterForge rolt Kubernetes-clusters uit met [k3s](https://docs.k3s.io): echt Kubernetes in één programma, met alles wat een cluster nodig heeft erin. Er valt niets te kiezen. Het ontwerp van deze fase staat in [docs/design/kubernetes.md](docs/design/kubernetes.md).

Kies bij "Cluster uitrollen" de template Kubernetes (k3s) en vul een vrij VIP in. Standaard krijg je de vorm om te leren: één server en twee workers (samen 6 vCPU, 12 GB geheugen en 110 GB schijf). Met drie servers overleeft het cluster het uitvallen van één; een even aantal kan niet, omdat etcd een meerderheid nodig heeft.

- **Servers** (`<slug>-server-01`, …) draaien de API, de scheduler en etcd, de database van het cluster. etcd staat ook met één server aan, zodat er meteen snapshots zijn: elke twaalf uur, de laatste vijf in `/var/lib/rancher/k3s/server/db/snapshots`.
- **Workers** (`<slug>-worker-01`, …) draaien je apps. Zonder workers draaien ze op de servers.
- **Het VIP** staat met keepalived op een server waarvan de API antwoordt. `kubectl` praat ermee op poort 6443, en Traefik beantwoordt er je apps op poort 80 en 443.
- **In het cluster** zitten containerd, Flannel voor het netwerk tussen de nodes, CoreDNS, Traefik als ingress, ServiceLB voor Services van type LoadBalancer, local-path voor volumes op de schijf van een node, metrics-server voor `kubectl top`, en de Helm-controller.

De template installeert een vaste k3s-versie (template 1.0.0: v1.35.9+k3s1, 1.1.0: v1.36.5+k3s1; een nieuw cluster krijgt de nieuwste) van de GitHub-release en controleert de SHA-256 met het controlebestand van die release. De instellingen staan in `/etc/rancher/k3s/config.yaml` (alleen voor root leesbaar, want het jointoken staat erin), en de driftcontrole kijkt mee. Het jointoken maakt ClusterForge zelf en bewaart het versleuteld, zoals elk geheim van een template.

Kubernetes draait op VM's, niet in LXC-containers: daar vraagt het een privileged container met extra instellingen, en zo'n opstelling breekt geregeld.

Na de uitrol werkt `kubectl` op elke server als root:

```sh
sudo kubectl get nodes -o wide
sudo kubectl get pods -A
```

Op je eigen pc gebruik je een kubeconfig uit ClusterForge; zie [kubectl op je eigen pc](#kubectl-op-je-eigen-pc).

Na het starten van k3s wacht de uitrol tot elke node van het cluster zich als Ready meldt in Kubernetes (hoogstens vijf minuten), en pas daarna kijkt ze of Traefik op het VIP antwoordt.

### De pagina Kubernetes

Onder Kubernetes staan je Kubernetes-clusters met hun versie, hoeveel nodes Ready zijn, hoeveel apps in orde zijn en hoeveel pods draaien. Klik op een cluster voor het dashboard, dat elke vijf seconden ververst:

- **Overzicht**: de tellers, de CPU en het geheugen tegenover wat de nodes kunnen geven (van metrics-server), een grafiek van CPU en geheugen per namespace, de pods met problemen, zoals CrashLoopBackOff of ImagePullBackOff, en de laatste vijf waarschuwingen (Kubernetes bewaart events een uur).
- **Nodes**: elke Kubernetes-node met Ready of NotReady en waarom, server of agent, cordon, gebruik, het aantal pods en een link naar de node in ClusterForge. ClusterForge koppelt ze op de naam die k3s kreeg, het adres of de hostname. Een beheerder zet hier nodes bij en haalt workers weg; zie [Nodes toevoegen en weghalen](#nodes-toevoegen-en-weghalen).
- **Apps**: de Deployments, StatefulSets, DaemonSets, CronJobs en Jobs, met hoeveel er klaar zijn en wat er mis is.
- **Pods**: de pods zoals `kubectl get pods` ze toont, te filteren op namespace en node.
- **Netwerk**: de Services met hun intern adres, poorten en hoeveel pods ze kiezen (0 is rood: dan klopt een label niet), en de Ingresses met hun webadres om te openen.
- **Opslag**: de claims (PVC's) met wie ze gebruikt, de volumes met de node en de map waar local-path ze maakte, en de StorageClasses.
- **Configuratie**: de ConfigMaps met hun sleutels, en de Secrets met alleen hun sleutels en hoe lang elke waarde is.
- **Gebeurtenissen**: de events van het laatste uur, waarschuwingen eerst.
- **Alles**: elke soort die het cluster kent, ook die van apps zoals Traefik (IngressRoute) en k3s (HelmChart), met hun objecten.
- **Snapshots**: de etcd-snapshots die k3s maakt, per server, met Snapshot nu en terugzetten; zie [etcd-snapshots](#etcd-snapshots).
- **kubectl**: een kubeconfig maken voor kubectl, k9s, Headlamp of Lens, en de kubeconfigs die er al zijn; zie [kubectl op je eigen pc](#kubectl-op-je-eigen-pc).

Klik op een pod, app, node, Service of ander object om het te openen. Je ziet:

- de toestand in gewone woorden, zoals "CrashLoopBackOff op leren-worker-02: 0 van 1 containers klaar, 7 herstarts";
- de keten: welke ReplicaSet en Deployment de pod maakten, op welke node hij draait, welke ClusterForge-node en welke VM op welke Proxmox-host dat is, en via welke Service en Ingress hij bereikbaar is;
- bij een pod of app een grafiek van CPU en geheugen per pod;
- bij een pod de containers met hun toestand en waarom de vorige stopte, en de logs: de laatste 100 tot 5000 regels, met tijdstippen als je wilt, die meeverversen. Herstartte een container, dan toont Vorige container de log van voor de herstart, waar de reden meestal staat;
- de condities, de events van dat object, de labels en het object als YAML, zoals `kubectl get -o yaml` het toont maar zonder managedFields en zonder de waarden van Secrets.

Ook een gebruiker met de rol viewer kan logs lezen.

### Acties

Een beheerder ziet bij een object en in de tabellen de acties die erbij passen. Elke actie opent eerst een venster dat zegt wat er gaat gebeuren, met in de leermodus het `kubectl`-commando dat hetzelfde doet:

- **Schalen** (Deployment, StatefulSet): het aantal pods, van 0 tot 100. Met 0 draait de app niet, maar hij blijft bestaan.
- **Herstarten** (Deployment, StatefulSet, DaemonSet): de pods worden een voor een vervangen, zoals `kubectl rollout restart`. Handig als een app een gewijzigde ConfigMap of Secret opnieuw moet lezen.
- **Terugdraaien** (Deployment): kies een vorige versie met haar images; Kubernetes maakt daar een nieuwe versie van en vervangt de pods een voor een, zoals `kubectl rollout undo`.
- **Weghalen**: op de pagina van het object, en bij een pod ook in de tabel. Het venster zegt wat er gebeurt, zoals "ReplicaSet web-7c9 maakt meteen een nieuwe pod" of dat de volumes van een StatefulSet blijven. Bij een namespace of een volume tik je de naam in. De namespace `default` en die van Kubernetes zelf kun je niet weghalen, en een node ook niet met deze actie: een worker haal je weg met Weghalen op het tabblad Nodes, zodat ook zijn VM verdwijnt.
- **Cordon** en **Uncordon** (node): er komen geen nieuwe pods meer op de node, of weer wel.
- **Leegmaken** (node): eerst toont ClusterForge welke pods weggaan, welke blijven (zoals die van een DaemonSet) en welke het tegenhouden: een pod zonder eigenaar komt nergens terug, en de gegevens in emptyDir gaan verloren. Die gaan alleen weg als je Toch doorgaan aanvinkt. Daarna is het een taak: de node gaat op cordon en elke pod wordt netjes gevraagd te vertrekken. Houdt een PodDisruptionBudget dat tegen, dan probeert ClusterForge het om de vijf seconden opnieuw, tot vijf minuten lang.

Elke actie staat in het logboek, met wie ze deed. Op een prodcluster vraagt ze de slug van het cluster en moet je tweestapsverificatie aan hebben. Stond iets al zo, dan verandert er niets en schrijft ClusterForge ook niets in het logboek.

Herstart je een node van een Kubernetes-cluster via ClusterForge, of zet je hem in onderhoud of uit, dan maakt ClusterForge hem in Kubernetes eerst leeg. Na een herstart wacht ClusterForge tot de node weer Ready is (hoogstens vijf minuten) en geeft hem dan weer pods; na onderhoud gebeurt dat bij Onderhoud beëindigen. Zette je de node zelf op cordon, dan blijft hij daarop staan. Antwoordt de Kubernetes-API niet, dan gaat de actie door zonder leegmaken, en staat dat bij de taak.

De namespaces van Kubernetes zelf (`kube-system`, `kube-public`, `kube-node-lease`) staan standaard verborgen; zet Systeem tonen aan om ze te zien.

De **leermodus** staat standaard aan. Hij legt op elk tabblad uit wat je ziet en geeft het `kubectl`-commando dat hetzelfde toont, om te kopiëren. Wijs een soort app of een status van een pod aan voor uitleg. Zet hem rechtsboven uit; dat blijft bewaard voor je gebruiker.

Een cluster dat je zelf bouwde, werkt ook: maak een cluster van het type Kubernetes, voeg de nodes toe en zet cf-agent op minstens één server.

### Nodes toevoegen en weghalen

Op het tabblad Nodes ziet een beheerder hoeveel servers en workers de specificatie vraagt, met twee knoppen:

- **Worker toevoegen…** zet het aantal workers één hoger, tot tien.
- **Servers naar 3…** (en daarna **naar 5…**) zet er servers bij. Met drie servers mag er één uitvallen, met vijf twee. Terug naar minder servers kan niet.

Beide tonen eerst het plan, zoals [Wijzigen…](#gewenste-staat) op de pagina van het cluster: welke nodes erbij komen, met naam, adres en Proxmox-host, en wat er op de bestaande nodes verandert. Een worker erbij verandert niets op de andere nodes. Bij extra servers krijgt keepalived op de bestaande server de nieuwe als peers, en die stap staat in het plan. Na Toepassen loopt de taak: de VM's maken uit de golden image, de agents aanmelden, de bestaande nodes bijwerken en k3s op de nieuwe nodes installeren. Een nieuwe server sluit aan bij etcd van de eerste. De taak eindigt pas als alle nodes Ready zijn in Kubernetes. Mislukt ze, dan maakt Opnieuw toepassen op de kaart Git de nieuwe nodes af; een VM die al bestaat, wordt niet opnieuw gemaakt. Nieuwe nodes melden zich aan op het adres van de pagina waarop je toepast. Met DHCP kan dit niet, net als bij omhoog schalen uit Git.

Bij een worker staat in het menu Acties **Weghalen…**. Het venster toont welke pods verhuizen en welke het tegenhouden, zoals bij Leegmaken, en welke VM op welke Proxmox-host verdwijnt. Je tikt de naam van de node in. Daarna is het een taak met vijf stappen, die bij Opnieuw proberen overslaat wat al gelukt is:

1. Leegmaken. Is de node niet Ready, dan gaat hij alleen op cordon, want zijn pods stoppen daar nooit netjes.
2. De VM uitzetten: eerst netjes, en na twee minuten hard.
3. De node uit Kubernetes halen.
4. De VM verwijderen, met zijn schijven.
5. De node uit ClusterForge halen en het aantal workers in de specificatie één lager zetten, als nieuwe revisie.

ClusterForge verwijdert alleen een VM die het zelf voor deze worker maakte. Voor het uitzetten en vlak voor het verwijderen kijkt het opnieuw: de node hoort nog bij dit cluster, is een worker in de specificatie, en de VM heeft dezelfde VMID en dezelfde naam in Proxmox. Het mag geen golden image, template of sandbox van een back-upcontrole zijn. Heeft de VM in Proxmox protection aan, dan weigert ClusterForge: zet het daar uit als hij weg mag, en probeer het opnieuw. Een server haal je niet weg, want etcd telt op zijn servers.

Volgt het cluster Git, dan zet je het aantal nodes in `cluster.yaml`; de knoppen staan dan dicht, en Weghalen ook. Een cluster dat niet uit een template komt, beheer je zelf.

### Apps uitrollen

Op het tabblad **Toepassen** zet een beheerder een app op, op drie manieren:

- **Nieuwe app**: een formulier met naam, namespace, image, poort, aantal pods, omgevingsvariabelen, een volume en een webadres. Ernaast staat de YAML die het maakt: een Deployment, een Service en een Ingress, met een volume ook een PersistentVolumeClaim, en een Namespace als die nog niet bestaat. In de leermodus staat bij elk object wat het doet. Voorbeeld: whoami vult het in met een kleine webserver die toont welke pod antwoordt.
- **Helm-chart**: repository, chart, versie en values. ClusterForge maakt er een HelmChart van in `kube-system`, en de Helm-controller van k3s installeert de chart in de namespace die je kiest, met een Job `helm-install-<naam>`. Voorbeeld: podinfo.
- **Eigen YAML**: één of meer objecten, gescheiden door `---`, zoals voor `kubectl apply -f`. Of begin met een voorbeeld: whoami, nginx, Uptime Kuma met een volume, of PostgreSQL als StatefulSet met een Secret.

Alle drie gaan langs dezelfde weg:

1. **Controleren** stuurt elk object als dry-run naar de API. Kubernetes keurt alles zoals bij echt toepassen, maar bewaart niets. Je ziet per object of het nieuw, anders of hetzelfde is, met het verschil tegenover wat er nu draait.
   - Een fout staat bij het veld, zoals `spec.template.spec.containers[0].ports[0].containerPort`, en in het formulier bij het invulvak.
   - Een fout in de YAML zelf geeft de regel.
   - Ook de values van een Helm-chart moeten geldige YAML zijn.
2. **Toepassen** kan pas na een controle zonder fouten, en alleen als er iets verandert.
   - Namespaces gaan eerst.
   - Faalt een object, dan stopt ClusterForge en zegt het wat wel en niet is toegepast.
   - Nog eens toepassen geeft "Geen verschil".

Beheert een ander programma een veld dat je YAML anders zet, dan toont Controleren dat conflict en welk programma het veld heeft. Dat gebeurt bijvoorbeeld als je het object eerder met een gewone `kubectl apply` maakte. Met **Velden overnemen** wordt ClusterForge de eigenaar; Controleren toont dan wat er verandert, en de knop wordt Toepassen en overnemen.

Het webadres is standaard `<app>.<VIP>.nip.io`. nip.io is een publieke DNS-dienst die elke naam met een IP-adres erin naar dat adres laat wijzen. Zo werkt het zonder eigen DNS, als je pc internet heeft. Wil je eigen namen, zet dan in je DNS een wildcard zoals `*.leren.lab` naar het VIP en vul `app.leren.lab` in.

Toepassen staat in het logboek met de soort en naam van wat nieuw of anders is, nooit met de inhoud. Op een prodcluster vraagt het de slug en tweestapsverificatie; controleren kan zonder. ClusterForge toont nooit de waarden van een Secret, ook niet in het verschil.

### Lessen

Op het tabblad **Lessen** leer je Kubernetes op je eigen cluster. Elke les heeft een paar opdrachten, op volgorde:

1. **Je cluster verkennen**: hoeveel nodes, welke een server is, waar CoreDNS en Traefik draaien.
2. **Je eerste app**: de namespace `leren`, een Deployment met nginx, en de pod die ontstaat.
3. **Schalen en zelfherstel**: naar drie pods, op hoeveel nodes ze staan, en een pod weghalen die terugkomt.
4. **Bereikbaar maken**: een Service, een Ingress onder `web.<VIP>.nip.io`, en de app in je browser.
5. **Een nieuwe versie**: nginx 1.29 uitrollen, het versienummer, en terugdraaien.
6. **Configuratie en geheimen**: een ConfigMap en een Secret als omgevingsvariabelen van web.
7. **Opslag die blijft**: een volume, een app die erin schrijft, herstarten, en de node waaraan het volume vastzit.
8. **Een node in onderhoud**: cordon, leegmaken en terugzetten. Daarvoor heeft het cluster minstens twee nodes nodig.
9. **Een node erbij**: een worker toevoegen, pods op de nieuwe node zetten, en hoe weghalen gaat.
10. **Een server valt uit**: de meerderheid van etcd, een server hard uitzetten, zien dat de API blijft antwoorden, en hem weer aanzetten. Deze les kan alleen op een cluster met omgeving lab en minstens drie servers.

Bij elke opdracht staat wat ze je leert, hoe het in ClusterForge kan en welk `kubectl`-commando hetzelfde doet. Waar YAML bij hoort, controleer en pas je die meteen toe, zoals op het tabblad Toepassen. Zolang een opdracht aan de beurt is, kijkt ClusterForge elke vijf seconden op het cluster of ze gelukt is, en zegt wat het ziet, zoals "Er draait nog 1 pod van web op leren-worker-02". Het kijkt alleen; het verandert zelf niets. Een vraag beantwoord je in het vak eronder; een fout antwoord geeft een hint. Staat de [assistent](#assistent) aan, dan heeft elke opdracht een knop **Ik zit vast**: de assistent kijkt wat er op het cluster staat en geeft een hint, zonder het antwoord voor te zeggen.

ClusterForge kijkt naar het resultaat, niet naar hoe je het deed: `kubectl create deployment` telt net zo goed als de YAML. Lessen bouwen op elkaar voort, maar je kunt ze in elke volgorde openen; mist er iets uit een eerdere les, dan zegt de opdracht dat.

De voortgang blijft per gebruiker en per cluster bewaard. Ook een viewer kan de lessen volgen, met kubectl of met een beheerder die toepast. Met **Opnieuw beginnen** wis je je voortgang in een les; wat er op het cluster staat, blijft. Ben je klaar, dan haalt **Opruimen** onder de lijst de namespace `leren` weg, met alles wat de lessen maakten, ook de volumes. Je voortgang blijft dan staan.

### Status, incidenten en metrics

Een Kubernetes-cluster krijgt de gewone [status](#monitoring) van zijn nodes en VIP, met de regels van Kubernetes erbij:

- **Down** als de API via geen enkele server antwoordt, 90 seconden lang (drie rondes van de sync). Is de agent te oud of draait er nergens een agent met k3s, dan weet ClusterForge het niet, en blijft de status van de nodes staan.
- **Verminderd** als een node NotReady is (bij een server staat erbij dat etcd een lid mist), of als een app verminderd of down is. De reden noemt hoogstens drie problemen, zoals "app web in demo: de uitrol hangt (ImagePullBackOff)".

**Apps.** Elke Deployment, StatefulSet en DaemonSet buiten kube-system, kube-public en kube-node-lease krijgt een eigen status. Een app doet niet wat hij moet als hij minder pods klaar heeft dan gewenst, of als een uitrol niet opschiet omdat de nieuwe pods een probleem hebben: de oude pods draaien dan nog, maar de nieuwe versie komt er nooit. Duurt dat twee minuten, dan is de app down als geen enkele pod klaar is, en anders verminderd. Een gewone uitrol of een pod die herstart, is binnen die tijd klaar. Elke wissel staat in het logboek, zoals "App web in demo van gezond naar verminderd: de uitrol hangt (ImagePullBackOff)".

**Incidenten.** Een app die verminderd of down gaat, opent een [incident](#incidenten), net als een dienst. Het cluster dat erdoor verminderd raakt, komt in hetzelfde incident, en de app linkt naar zijn pagina in het dashboard.

**What Broke.** ClusterForge bewaart de waarschuwingen van Kubernetes 30 dagen (Kubernetes zelf bewaart ze een uur), en ook elke container die stopte door gebrek aan geheugen (OOMKilled). Ze staan in de tijdlijn met de bron Kubernetes; BackOff, Failed, OOMKilled, FailedScheduling, Unhealthy, Evicted en FailedMount zijn symptomen. Krijgt een app een andere image, dan is dat een uitrol: een verandering in het logboek, zoals "Uitrol van web in demo: image nginx:1.29 → nginx:1.299", op het moment dat de eerste pod met de nieuwe image ontstond. Gaat de app daarna mis, dan noemt [What Broke](#what-broke) de uitrol als oorzaak, met het verband in één zin: "web in demo kan zijn image niet ophalen sinds de uitrol van image nginx:1.299 om 14:02."

**Metrics.** Elke minuut leest ClusterForge CPU en geheugen uit metrics-server, dat k3s meelevert, en schrijft ze naar VictoriaMetrics:

| Metric | Wat | Extra labels |
| --- | --- | --- |
| `kube_pod_cpu_usage_cores` | CPU van een pod, in cores | `namespace`, `pod`, `workload`, `workload_kind` |
| `kube_pod_memory_working_set_bytes` | Geheugen van een pod (working set), in bytes | `namespace`, `pod`, `workload`, `workload_kind` |
| `kube_node_cpu_usage_cores` | CPU van een node, in cores | |
| `kube_node_memory_working_set_bytes` | Geheugen van een node, in bytes | |

Ze hebben dezelfde labels als de metrics van een node (`job`, `instance`, `node`, `node_id`, `cluster`, `cluster_id`, `env`), met als node die waarop de pod draait. `workload` is de app van de pod, zoals de Deployment boven zijn ReplicaSet. Het overzicht toont CPU en geheugen per namespace, de pagina van een app of pod per pod. Een nieuwe agent is hiervoor niet nodig: de agent leest metrics-server zoals de rest van de API.

### kubectl op je eigen pc

Op het tabblad **kubectl** maak je een kubeconfig. Daarmee praten kubectl, [k9s](https://k9scli.io), [Headlamp](https://headlamp.dev) en [Lens](https://k8slens.dev) op je eigen pc rechtstreeks met de API van het cluster, op het VIP en poort 6443. Je pc moet dat adres kunnen bereiken.

1. Geef de kubeconfig een naam (zoals laptop), kies de rechten en hoe lang hij geldt (1, 7 of 30 dagen), en bevestig met je wachtwoord, of met je code als je tweestapsverificatie hebt.
2. Download het bestand, zoals `leren.kubeconfig`. Dat kan alleen nu: ClusterForge bewaart het token niet.
3. Gebruik het:

```sh
mkdir -p ~/.kube
mv ~/Downloads/leren.kubeconfig ~/.kube/
chmod 600 ~/.kube/leren.kubeconfig
export KUBECONFIG=~/.kube/leren.kubeconfig
kubectl get nodes
```

In PowerShell op Windows is het `$env:KUBECONFIG = "$HOME\Downloads\leren.kubeconfig"`. k9s leest dezelfde `KUBECONFIG`; in Headlamp en Lens voeg je het bestand toe als cluster.

Zo werkt het:

- Elke kubeconfig krijgt een eigen ServiceAccount in de namespace `clusterforge`, zoals `cf-jonas-1a2b3c`, met een ClusterRoleBinding `clusterforge:cf-jonas-1a2b3c`. Het token erbij vraagt ClusterForge met de TokenRequest-API; het verloopt vanzelf.
- **Beheerder** is de ClusterRole `cluster-admin`: alles, ook Secrets lezen. **Alleen lezen** is `view`: alles lezen behalve Secrets, en niets wijzigen. Een viewer krijgt alleen view. Een beheerder-kubeconfig voor een prodcluster vraagt tweestapsverificatie en de slug.
- **Intrekken** haalt het ServiceAccount weg, en dan weigert de API het token meteen. Antwoordt de API op dat moment niet, dan staat de kubeconfig op "Wordt ingetrokken" en probeert ClusterForge het elke 30 seconden opnieuw.
- Een verlopen kubeconfig ruimt ClusterForge op dezelfde manier op. Is de gebruiker uitgeschakeld, of geen admin meer bij een beheerder-kubeconfig, dan trekt ClusterForge hem binnen een halve minuut in.
- Uitgeven en intrekken staan in het logboek, zonder het token. Een beheerder ziet alle kubeconfigs van het cluster, een viewer de zijne. Verlopen en ingetrokken kubeconfigs blijven 30 dagen in de lijst.

Zelf kijken in het cluster:

```sh
kubectl get serviceaccounts -n clusterforge
kubectl get clusterrolebindings -l app.kubernetes.io/managed-by=clusterforge
```

Dit vraagt een agent van deze versie op een server; met een oudere toont het tabblad "Agent te oud" met het commando om hem bij te werken.

### etcd-snapshots

Alles wat je in Kubernetes maakt, staat in etcd, de database van het cluster, op elke server. k3s maakt daar zelf om 0 en 12 uur een snapshot van, op elke server, en houdt de laatste vijf in `/var/lib/rancher/k3s/server/db/snapshots`. Op het tabblad **Snapshots** zie je ze per server: wanneer, hoe groot, en of k3s hem klaar meldt. ClusterForge leest de lijst elke vijf minuten. Is de nieuwste bruikbare snapshot ouder dan een dag, dan staat er "Ouder dan een dag": dan lukt het k3s niet, en `sudo journalctl -u k3s | grep -i snapshot` op een server zegt waarom. Bij **Back-ups** staat dezelfde stand voor elk Kubernetes-cluster.

- **Snapshot nu** (beheerder) maakt meteen een snapshot op de server die het cluster begon, met `k3s etcd-snapshot save --name clusterforge`. Hij heet dan `clusterforge-<server>-<tijd>` en staat in het logboek.
- **Terugzetten** (beheerder, in lab en test) zet het hele cluster terug naar het moment van een snapshot. Je ziet eerst wat er gaat gebeuren en tikt de slug van het cluster ter bevestiging.

Terugzetten is een taak die doet wat k3s beschrijft, stap voor stap:

1. Elke server controleren: de agent is verbonden en kent snapshots, en de snapshot staat op de server die het cluster begon.
2. Een snapshot van nu maken (`clusterforge-voor-terugzetten`), zodat je terug kunt naar hoe het net was.
3. k3s stoppen op elke server. Vanaf hier antwoordt de API niet; de pods op de nodes draaien door.
4. `k3s server --cluster-reset --cluster-reset-restore-path=…` op de server die het cluster begon. De oude database zet k3s daar in `db/etcd-old-…`.
5. k3s starten op die server, en wachten tot de API antwoordt.
6. Op de andere servers de database opzij zetten (`db-voor-terugzetten-<tijd>`, de snapshots blijven) en k3s starten, zodat ze zich opnieuw aanmelden.
7. Wachten tot elke node Ready is. De workers melden zich vanzelf weer.

Mislukt een stap, dan zegt de taak waarom en staat het cluster stil waar het was; **Opnieuw proberen** gaat verder bij die stap. Na het terugzetten staat elk object weer zoals op dat moment: wat daarna gemaakt is, is weg, en wat daarna weggehaald is, staat er weer, ook Secrets. De gegevens in volumes zitten niet in een snapshot en blijven zoals ze zijn; daarvoor zijn de back-ups van de VM's in Proxmox. Terwijl het loopt, is het cluster een paar minuten down. Dat kan een incident openen, dat sluit als de API terug is; het begin en het einde van het terugzetten staan in het logboek, en What Broke noemt het terugzetten als oorzaak.

Alleen een snapshot op de server die het cluster begon (de eerste, met `cluster-init` in zijn config) kan terug: de andere servers melden zich daarna bij hem opnieuw aan. Omdat k3s op elke server op dezelfde momenten een snapshot maakt, is die er altijd. Een snapshot in S3 zet ClusterForge niet terug.

**Op prod** zet ClusterForge niets terug: het cluster ligt dan een paar minuten stil, en alles wat na de snapshot veranderde, is weg. Het tabblad toont daar het runbook, met de namen van je servers en de nieuwste snapshot ingevuld. In lab en test staat hetzelfde onder "Met de hand terugzetten". Als root op de servers:

```sh
# 1. Op de eerste server: een snapshot van nu
sudo k3s etcd-snapshot save --name voor-terugzetten
# 2. Op elke server
sudo systemctl stop k3s
# 3. Op de eerste server: terugzetten en starten
ls -lh /var/lib/rancher/k3s/server/db/snapshots
sudo k3s server --cluster-reset --cluster-reset-restore-path=/var/lib/rancher/k3s/server/db/snapshots/<bestand>
sudo systemctl start k3s
# 4. Op elke andere server: de database opzij zetten en starten
sudo mv /var/lib/rancher/k3s/server/db /var/lib/rancher/k3s/server/db-voor-terugzetten
sudo mkdir -m 700 /var/lib/rancher/k3s/server/db
sudo mv /var/lib/rancher/k3s/server/db-voor-terugzetten/snapshots /var/lib/rancher/k3s/server/db/
sudo systemctl start k3s
# 5. Kijken of elke node Ready is
sudo kubectl get nodes
```

De lijst werkt met elke agent die Kubernetes leest. Snapshot nu en Terugzetten vragen een agent van deze versie op de servers; met een oudere zegt ClusterForge welke server je moet bijwerken met het installatiescript en `--upgrade`.

### Kubernetes bijwerken

Elke versie van de template kubernetes installeert één vaste k3s. Bijwerken is overstappen naar een nieuwere versie van de template, met dezelfde wijziging van de specificatie als bij nodes toevoegen. Op het **Overzicht** van een cluster staat de kaart **Kubernetes-versie**: welke k3s het cluster hoort te draaien, welke versie elke node meldt (de kolom VERSION van `kubectl get nodes`), en of er een nieuwere is. Kubernetes gaat één minorversie per keer omhoog en nooit terug, dus ClusterForge biedt alleen de volgende aan; een versie die een minor overslaat of ouder is, weigert het plan.

1. Maak eerst een snapshot op het tabblad Snapshots.
2. Kies **Bijwerken naar k3s v1.36.5…** (beheerder). Je ziet het plan: per node de stappen in de volgorde van de uitrol, met de nieuwe k3s erbij. Er gebeurt nog niets.
3. Kies Toepassen. De taak gaat node voor node, eerst de servers en dan de workers, want een kubelet mag niet nieuwer zijn dan de API-server. Per node:
   - Leegmaken: de node gaat op cordon met de markering van ClusterForge, en zijn pods gaan via de Eviction-API naar de andere nodes, zodat een PodDisruptionBudget geldt. Lukt dat niet, dan krijgt hij weer pods en stopt de taak voor er iets veranderd is. Is er geen andere node voor de pods, dan blijven ze staan: een herstart van k3s stopt ze niet.
   - De nieuwe binary installeren (`install-k3s.sh`, met de controle van de SHA-256) en k3s of k3s-agent herstarten.
   - Wachten tot de node gezond is, het VIP een houder heeft, elke node Ready is en Traefik op het VIP antwoordt, en daarna tot de node Ready is met de nieuwe versie.
   - De cordon weghalen: de node krijgt weer pods, en de volgende is aan de beurt.

Komt een node niet terug, dan stopt de taak daar en blijft die node op cordon; de nodes daarna blijven ongemoeid op de oude versie. Kijk op de node met `sudo journalctl -u k3s` (een server) of `sudo journalctl -u k3s-agent` (een worker). Is het opgelost, kies dan **Opnieuw toepassen** op de pagina van het cluster: nodes die de nieuwe versie al draaien, gaan niet opnieuw leeg, en de rest volgt. Wil je de node toch eerst weer gebruiken, haal de cordon dan weg met Uncordon op het tabblad Nodes.

Met één server antwoordt de API van het cluster even niet terwijl k3s daar herstart, en is het VIP even weg; apps die al draaien, blijven draaien. Met drie servers neemt een andere server het VIP over. Volgt het cluster Git, dan zet je in `cluster.yaml` de versie van de template op de nieuwe en keur je de wijziging goed; de uitrol is dezelfde.

### Score en documentatie

Een Kubernetes-cluster krijgt bij de [health score](#gezondheid) een achtste kaart, **Kubernetes**, met deze bevindingen:

| Bevinding | Ernst | Wanneer |
| --- | --- | --- |
| Node NotReady | middel, op prod hoog | een node die Kubernetes niet Ready noemt; na vijf minuten zet Kubernetes zijn pods elders |
| Eén server | middel | een prod-cluster met één server: valt die uit, dan antwoordt de API niet en start niets opnieuw |
| Geen etcd-snapshot | hoog, op prod kritiek | ClusterForge las de snapshots en er is er geen die klaar is |
| Snapshot te oud | middel, op prod hoog | de nieuwste snapshot is ouder dan een dag; k3s maakt er normaal elke twaalf uur een |
| Versie zonder updates | hoog, of laag binnen 90 dagen | de Kubernetes-minor van k3s krijgt geen updates meer |
| Blijft herstarten | middel, op prod hoog | een app met een pod in CrashLoopBackOff, of met minstens drie herstarts van zijn huidige pods en de laatste in het afgelopen etmaal |
| Eén pod | laag | apps op prod met `replicas: 1` (geen DaemonSet) |
| Image zonder vaste versie | laag | apps met een image op `:latest` of zonder tag; een digest (`@sha256:…`) is vast |
| Geen geheugenlimiet | laag | apps met een container zonder `resources.limits.memory` |

De laatste drie tellen alleen de eigen apps, niet die van k3s in `kube-system`, en zijn één bevinding per cluster met de namen erin, zodat tien apps zonder limiet de score niet laten kelderen. Oplossen opent het overzicht van het cluster, het tabblad Nodes, Snapshots of Apps, of de app zelf. Een bevinding over een app of node accepteer je zoals elke andere; een node die niet in ClusterForge staat, krijgt een vast id uit het cluster en zijn naam. Zolang ClusterForge het cluster niet kon lezen, is de kaart onbekend.

In de [documentatie](#documentatie) krijgt het cluster een hoofdstuk **Kubernetes**, na Architectuur: de k3s-versie en tot wanneer ze updates krijgt, de nieuwere template als die er is, de API via het VIP, de nodes met rol, ClusterForge-node, adres, k3s-versie, CPU en geheugen, de eigen namespaces, de apps met soort, aantal pods en images, de webadressen (elke regel van een Ingress, en Services met een adres buiten het cluster), de opslag met klasse, grootte, node, map en de pods die hem gebruiken, en de etcd-snapshots. Daarna de runbooks: een worker of server vervangen, bijwerken met de volgorde van de nodes, en etcd terugzetten met de commando's stap voor stap. Het hoofdstuk Runbooks zegt dat onderhoud, herstarten en afsluiten een node eerst leegmaken, en verwijst voor een node vervangen naar dit hoofdstuk.

Wat elke minuut verandert, zoals Ready en het aantal herstarts, staat er niet in: dat staat in ClusterForge, en zo krijgt het hoofdstuk alleen een nieuwe versie als er echt iets veranderde. De namespaces, webadressen en opslag leest ClusterForge elke vijf minuten; na een uitrol, Toepassen of een andere wijziging aan het cluster schrijft het de documentatie opnieuw, zoals bij elk cluster. Secrets staan er niet in.

### Assistent bij Kubernetes

Staat de [assistent](#assistent) aan, dan kan hij ook in een Kubernetes-cluster kijken, net als de rest alleen lezend en met de rechten van wie de vraag stelt:

- **kubernetes**: een cluster nu, met de versie, de nodes, de apps, de pods met een probleem en de waarschuwingen; met een namespace alle apps en pods daarin;
- **kube_object**: één object, zoals een pod, app, node of Ingress, met zijn toestand, condities, events, de keten tot de VM en de YAML (hoogstens 8 kB);
- **kube_logs**: de laatste 200 regels log van een container, ook van de vorige na een herstart.

Bij elk object staat **Leg uit**, en bij een pod die niet start **Waarom start deze pod niet?**. De assistent leest dan de events, de containers en de log van voor de laatste herstart, en antwoordt met de oorzaak, de regel die het bewijst, en wat je kunt doen, in ClusterForge of met kubectl. Ook bij elke waarschuwing van Kubernetes, op het overzicht en het tabblad Gebeurtenissen, staat Leg uit. Bij een les is er **Ik zit vast** (zie [Lessen](#lessen)); bij een vraag krijgt de assistent de hint van de les mee, maar nooit het goede antwoord.

Van een Secret ziet de assistent alleen de namen van de sleutels, zoals overal: de agent stuurt de waarden niet mee. In logs, YAML en berichten vervangt ClusterForge bovendien wat op een wachtwoord of token lijkt door `[geheim]` voordat het naar Anthropic gaat: een waarde achter een naam als `password`, `secret`, `token` of `api_key`, een env-variabele met zo'n naam, het wachtwoord in een URL als `postgres://app:…@db`, Bearer-tokens, JWT's en private keys. Annotaties gaan niet mee, behalve in de YAML, waar ze ook gemaskeerd zijn. Dat filter is ruim: soms valt er ook iets weg dat geen geheim is.

### Hoe ClusterForge Kubernetes leest

De server praat nooit zelf met de Kubernetes-API. cf-agent op een k3s-server doet het, met de beheerdersconfig `/etc/rancher/k3s/k3s.yaml` die daar al staat, en stuurt het antwoord via de bestaande verbinding (NATS) terug. Daardoor hoeft poort 6443 niet open naar ClusterForge, en verlaat geen certificaat van het cluster de server. ClusterForge kiest een server met een verse heartbeat waarvan de API antwoordt; valt die weg, dan neemt een andere het over.

De agent laat alleen lezen toe, met een vaste lijst: geen exec, attach, port-forward of proxy, en geen watch. Schrijven kan alleen voor de acties hierboven: een patch op Deployments, StatefulSets, ReplicaSets, DaemonSets en nodes, een eviction van een pod, en het weghalen van één object, nooit van `default` of de namespaces van Kubernetes zelf. Voor Toepassen doet de agent een server-side apply van één object tegelijk, met een dry-run bij Controleren; dat kan elk object zijn, zoals bij `kubectl apply`, en daarom kan alleen een beheerder het. Een patch of apply staat in Kubernetes op naam van `clusterforge` (de fieldManager). Voor een kubeconfig maakt de agent zelf het ServiceAccount en de ClusterRoleBinding, alleen in de namespace `clusterforge`, alleen met een naam die met `cf-` begint en alleen met `cluster-admin` of `view`, en vraagt hij er een token voor; het token gaat één keer naar ClusterForge en zo naar je browser. Van een log haalt hij alleen de laatste regels, hoogstens 5000 en 2 MB, zonder te blijven volgen; het dashboard vraagt hem elke vijf seconden opnieuw. Van een Secret stuurt hij de namen van de sleutels, nooit de waarden: die vervangt hij door "(verborgen, N bytes)". Hij doet hoogstens vier verzoeken tegelijk, elk hoogstens 30 seconden.

Voor snapshots doet de agent op een server alleen dit: `k3s etcd-snapshot save` met een naam van ClusterForge, `systemctl stop` en `start` van k3s, `k3s server --cluster-reset` met een bestand uit de map met snapshots en alleen als k3s stilstaat op de server die het cluster begon, en op een andere server de map `db` opzij zetten als k3s stilstaat. Zo'n verzoek mag tien minuten duren.

Dit kan vanaf **agentprotocol 8**. Met een oudere agent toont de pagina "Agent te oud voor Kubernetes", met het commando om hem bij te werken; de aanmelding blijft staan. Werk ook je golden image bij, anders krijgen nieuwe VM's de oude agent. De andere onderdelen van ClusterForge blijven werken met een oudere agent.

Elke 30 seconden vat ClusterForge elk Kubernetes-cluster samen voor de lijst, de status, de apps, de waarschuwingen en de score, en elke vijf minuten leest het de etcd-snapshots en de namespaces, webadressen en opslag voor de documentatie. Antwoordt de API niet, dan blijft de laatste stand staan met "API niet bereikbaar" en de reden.

## Databases

De pagina **Databases** toont de MariaDB- en PostgreSQL-servers op je nodes, gegroepeerd per cluster en engine. ClusterForge herkent:

- een **Galera-cluster** (MariaDB): per node of hij Synced is, en of het cluster een Primary-component heeft;
- **PostgreSQL met Patroni**: de rol van elke node volgens Patroni en de timeline;
- een **primary met replica's**: de bron van elke replica en hoeveel seconden hij achterloopt;
- een **losse server**.

Bovenaan elk cluster staat in één zin hoe het ervoor staat, zoals "3 van 3 nodes Synced." of "Primary db1 met 2 replica's.". Daaronder de nodes met hun toestand, het aantal verbindingen en wanneer ClusterForge ze het laatst gelezen heeft, en de databases met hun grootte en het aantal tabellen. Een kijker ziet hetzelfde als een beheerder.

ClusterForge leest elke node met een databaseserver elke minuut. Kan het een node niet vragen, dan blijft de laatste stand staan met de reden: agent offline, geen antwoord of agent te oud. Wat er mis is met een database en hoe ClusterForge het herstelt, staat bij elke database onder [Zelfherstel](#zelfherstel).

### Wie gebruikt welke database

Bij elke database staat wie haar gebruikt: per cluster de nodes die verbonden waren, met welke databasegebruiker, hoeveel verbindingen er nu zijn of wanneer de laatste was. De agent kijkt daarvoor elke minuut in `information_schema.PROCESSLIST` of `pg_stat_activity`; ClusterForge koppelt het adres van de client aan een node en zijn cluster. Wat het niet kent, staat er als onbekend adres, en een verbinding via de socket of 127.0.0.1 als "op de databasenode zelf". Omdat een cronjob niet altijd verbonden is, telt alles van de laatste dertig dagen mee.

- **gedeeld**: meer dan één cluster, losse node of onbekend adres gebruikt de database. Zo'n database moet je eerst splitsen voor je ze naar de nodes van één app verhuist.
- **niet in de afhankelijkheden**: het cluster gebruikt de database, maar de graaf bij [Afhankelijkheden](#afhankelijkheden) weet dat nog niet. ClusterForge stelt de pijl daar zelf voor uit de verbindingen die de agents zien (zie [Verbindingen](#verbindingen)); bevestig hem bij Afhankelijkheden → Voorstellen.
- **niemand in de laatste 30 dagen**: een kandidaat om op te ruimen.

### Een database op de nodes van een cluster

Op de pagina van een cluster staat de kaart **Database**. Database toevoegen zet MariaDB Galera op nodes van dat cluster zelf, zodat de app haar database naast zich heeft en er geen firewallregel tussen zit. Het werkt met VM's en CT's, op Debian 12 (MariaDB 10.11) en Debian 13 (MariaDB 11.8); alle nodes hebben dezelfde Debian.

Kies de nodes, standaard alle actieve nodes van het cluster, en eventueel de buffer pool en het maximum aantal verbindingen. ClusterForge toont meteen een plan, per node:

- het systeem, het geheugen met hoeveel er nu gebruikt wordt (het 95e percentiel van de laatste week) en hoeveel er na MariaDB vrij blijft;
- de vrije schijf onder `/var/lib/mysql`;
- de Proxmox-host en de VM of CT, als de node gekoppeld is;
- de stappen die ClusterForge op de node zet.

Wat niet kan, blokkeert, met de reden: minder dan twee nodes, **twee nodes zonder scheidsrechter** (valt er één weg, dan heeft de andere geen meerderheid en stopt de database), een agent die offline of te oud is, een andere Debian dan 12 of 13, een poort (3306, 4444, 4567 of 4568) die al bezet is, MySQL, een Galera-cluster of een MariaDB met eigen databases die al op de node staat (verhuis die eerst), een MariaDB die er staat maar niet draait, of draait maar waar de agent niet in kan (ClusterForge ziet dan niet welke databases erin staan; start een stilstaande MariaDB eerst, of kijk bij [Zelfherstel](#zelfherstel) als ze niet start, en zorg bij een draaiende dat root via de socket mag inloggen, met `mariadb -u root` zonder wachtwoord), of minder dan 2 GB vrije schijf. Een waarschuwing laat je wel door: een even aantal stemmen, twee van de drie stemmen op dezelfde Proxmox-host, een lege MariaDB die al draait (ClusterForge neemt haar over), krap geheugen of minder dan 10 GB vrije schijf. Bij krap geheugen staat het commando erbij, zoals `pct set 186 --memory 5632`.

Met twee nodes kies je een derde node van het cluster als **scheidsrechter**: hij krijgt alleen `garbd`, stemt mee en heeft geen data.

Database toevoegen doet het plan dat je zag; is er intussen iets veranderd, dan toont ClusterForge het nieuwe plan. Op prod tik je de slug in. Daarna is het een taak met een stap per node:

1. De eerste node installeert `mariadb-server`, `mariadb-backup` en `galera-4`, schrijft `/etc/mysql/mariadb.conf.d/99-clusterforge-galera.cnf` en begint het cluster met `galera_new_cluster`. ClusterForge doet dat alleen als geen andere gekozen node al in een cluster met deze naam zit, zodat er nooit twee clusters met dezelfde naam ontstaan.
2. Elke volgende node sluit aan: MariaDB herstart met de nieuwe configuratie en haalt alle data bij een node die al meedoet (SST met `mariabackup`). ClusterForge wacht tot hij Synced is in een cluster met het verwachte aantal leden, en gaat pas dan naar de volgende.
3. Controleren: alle nodes Synced, in hetzelfde cluster, met evenveel leden als stemmen. Dan komt MariaDB in de graaf bij [Afhankelijkheden](#afhankelijkheden).

De naam van het Galera-cluster is de slug van het cluster. SST gebruikt de gebruiker `mysql` via de socket, die MariaDB zelf aanmaakt, dus er staat geen wachtwoord op de nodes. Elke node heeft een merkteken `/var/lib/clusterforge/galera-<slug>`; zonder doet hij nog niet mee.

Daarna kent de drift de database: een met de hand aangepaste `99-clusterforge-galera.cnf` valt op, ook op een cluster zonder template of baseline. **Opnieuw toepassen** zet alles terug, node voor node; alleen op een node waar iets veranderde, herstart MariaDB, en ClusterForge wacht tot hij weer Synced is. Mislukt de taak, dan blijft staan wat al klaar was; los het op en kies Opnieuw toepassen. **Weghalen** kan zolang de database nooit helemaal gelukt is, en haalt haar alleen uit ClusterForge, niet van de nodes.

De app verbindt nog met haar oude database. Hoe je die verhuist, staat onder [Een database verhuizen](#een-database-verhuizen).

#### Het VIP van de app

Kies bij Database toevoegen het VIP van de app, zoals `10.0.20.185` bij Nextcloud; heeft het cluster één VIP, dan staat dat al gekozen. De app verbindt dan met het VIP voor haar database, en keepalived zet het VIP alleen op een node waarvan MariaDB mag schrijven: Synced, in de Primary component van Galera en met `wsrep_ready` aan. Valt MariaDB op die node uit, loopt hij achter of is hij donor voor een nieuwe node, dan gaat het VIP binnen enkele seconden naar een andere node. Zo schrijft de app altijd naar één node, wat bij Galera conflicten op dezelfde rijen voorkomt, en er is geen HAProxy nodig.

ClusterForge doet dat in de keepalived.conf die je zelf schreef, zonder de rest te veranderen:

- in `/etc/keepalived/conf.d/clusterforge-db.conf` staat `vrrp_script cf_db`, die elke twee seconden `cf-agent check db` draait (exit 0 als MariaDB mag schrijven);
- bovenaan keepalived.conf komt `include /etc/keepalived/conf.d/*.conf`, want keepalived moet het script kennen voor de `vrrp_instance` het noemt;
- de `vrrp_instance` met het VIP krijgt `cf_db` in zijn `track_script`, naast wat er al stond, zoals `chk_apache`.

Het plan toont per node de diff van keepalived.conf, met het wachtwoord van VRRP (`auth_pass`) verborgen. Het blokkeert als een andere node van het cluster het VIP ook in keepalived heeft maar geen database krijgt (dan zou het VIP daar kunnen belanden), als minder dan twee datanodes het VIP hebben, als keepalived ergens niet draait, of als het cluster uit een template komt: die zet keepalived.conf bij de volgende uitrol terug, dus dat komt later. Een scheidsrechter met het VIP krijgt de controle ook, zodat het VIP er nooit staat.

Na de nodes krijgt elke node met het VIP een eigen stap: het bestand, de aanpassing van keepalived.conf (alleen als `keepalived -t` de nieuwe versie goedkeurt; anders verandert er niets) en `systemctl reload keepalived`, met de node die het VIP nu heeft als laatste. Na elke stap wacht ClusterForge tot het VIP weer op een node staat, en Controleren eist dat het op precies één datanode staat. Daarna ziet de drift een aangepaste `clusterforge-db.conf`, en Opnieuw toepassen zet hem terug en herlaadt keepalived alleen op die node.

Zelf controleren op een node:

```sh
cf-agent check db; echo $?          # 0 = deze node mag het VIP hebben
journalctl -u keepalived | grep cf_db
```

#### Na een herstart

Valt één node uit, dan draait de database verder op de andere; komt hij terug, dan sluit MariaDB vanzelf weer aan en haalt hij wat hij miste. Vallen alle nodes tegelijk weg, zoals bij een stroomuitval, dan vindt Galera zijn cluster zelf terug zodra ze allemaal weer starten. Maar na een gewone stop van alle nodes, zoals een herstart van de Proxmox-host, of als de nodes ver na elkaar opstarten, begint Galera niet vanzelf: elke node wacht op een cluster dat er niet is, geeft het op, en MariaDB staat op `failed`. Dan moet de node met de nieuwste data het cluster beginnen.

ClusterForge doet dat. Het vraagt om de 20 seconden elke datanode waar hij staat en begint het cluster vanzelf opnieuw als:

- alle datanodes antwoorden, want een node die ontbreekt kan de nieuwste data hebben;
- MariaDB op geen enkele node nog draait of start, want dan kan Galera het nog zelf; hangt een node langer dan 15 minuten in starten, dan wacht ClusterForge niet meer;
- MariaDB ergens niet startte (`failed`): is hij overal gewoon gestopt, dan deed iemand dat met opzet en blijft hij uit;
- de database al anderhalve minuut weg is, en ClusterForge het niet al het laatste halfuur probeerde.

De taak **Database vanzelf opnieuw starten** leest dan per node tot welke transactie hij de data heeft: na een gewone stop staat dat in `/var/lib/mysql/grastate.dat`, na een crash alleen in InnoDB, en dan haalt `galera_recovery` het eruit. De node met de hoogste seqno begint het cluster (bij gelijke stand die als laatste stopte, dan de eerste); hij krijgt `safe_to_bootstrap: 1` en `galera_new_cluster`. Daarna start MariaDB op de andere nodes, een voor een, en ClusterForge wacht telkens tot de node Synced is; ze halen wat ze missen bij de eerste. Zo gaat er geen transactie verloren. Heeft de database het VIP van de app, dan controleert de taak ook dat het weer op één datanode staat. Een node die niet startte terwijl het cluster draait, start ClusterForge na anderhalve minuut op dezelfde manier opnieuw; geen enkele node begint dan een cluster.

Ontbreekt er een node, dan wacht ClusterForge, en zegt de kaart **Database** waarom. Met **Opnieuw starten** zie je per node hoe het ermee staat, zijn seqno en wat er gaat gebeuren, met een waarschuwing dat een node die niet meedoet zijn nieuwere data verliest als hij later aansluit. Bevestig je, dan begint ClusterForge zonder die node; op prod tik je de slug in. Opnieuw starten werkt ook als het cluster nog draait: de nodes die er niet bij zijn, sluiten dan aan.

Op de kaart staat ook welke node niet meedoet en sinds wanneer, en de schakelaar **Vanzelf opnieuw starten na een volledige uitval**, standaard aan. In het logboek komen de uitval, een node die langer dan vijf minuten niet meedoet, het opnieuw starten (door wie, of vanzelf en waarom) en het resultaat.

Of dit op jouw cluster echt werkt, test je met de failovertest [alles herstarten](#alles-herstarten). Een Galera-cluster dat ClusterForge niet zelf toepaste, begint [Zelfherstel](#zelfherstel) op dezelfde manier opnieuw, met Nu herstellen, of vanzelf als je dat aanzet.

Zelf kijken op een node:

```sh
cat /var/lib/mysql/grastate.dat     # seqno -1 na een crash
galera_recovery                     # alleen als MariaDB stilstaat
systemctl status mariadb
```

Wat ClusterForge aanneemt over Galera, staat in een test met drie echte MariaDB 11.8-containers (`internal/galera`, draait in de CI): na een gewone stop, een crash van eerst één en dan alle nodes, en een stroomuitval wint de juiste node en zijn daarna alle rijen er.

#### Databases en gebruikers

Op de kaart **Database** van een cluster opent **Databases en gebruikers** wat er in MariaDB staat. ClusterForge leest het live op de node met het VIP, of anders de eerste node die Synced is: de databases met hun grootte, tabellen en verbindingen, en de gebruikers met de adressen waarvandaan ze mogen inloggen en hun rechten per database. Wachtwoorden en hashes leest het nooit. Root, mysql en mariadb.sys zijn van MariaDB zelf; daar verandert ClusterForge niets aan.

**Database aanmaken** vraagt een naam (kleine letters, cijfers en `_`) en eventueel een andere gebruiker; standaard heet de gebruiker zoals de database. ClusterForge maakt de database met utf8mb4 en een gebruiker met alle rechten op die database, en geeft hem een sterk wachtwoord. Hij mag inloggen vanaf elke node van het cluster en vanaf elk VIP: een app op de node met het VIP die naar het VIP verbindt, komt bij MariaDB binnen met het VIP als adres. Galera zet de database en de gebruiker zelf op de andere nodes.

Het wachtwoord staat één keer op het scherm, met het adres, de poort, de database en de gebruiker, om in de instellingen van de app te zetten. ClusterForge bewaart het niet, ook niet in het logboek. Kwijt? Kies **Nieuw wachtwoord** bij de gebruiker; het oude werkt dan meteen niet meer, dus de app moet het nieuwe krijgen.

**Weghalen** van een database of gebruiker vraagt de naam ter bevestiging, op prod ook de slug en tweestapsverificatie, net als een nieuw wachtwoord. Een database weghalen haalt alle data weg en kan niet ongedaan worden; de gebruikers houden hun rechten op die naam tot je ze ook weghaalt. Alles komt in het logboek, met de namen en zonder wachtwoord.

Met de hand op een node:

```sh
mariadb -e "SELECT User, Host FROM mysql.user"
mariadb -e "SHOW GRANTS FOR 'nextcloud'@'10.0.20.185'"
```

**PostgreSQL met Patroni** werkt hetzelfde, met een paar verschillen. Heeft een cluster zowel MariaDB als PostgreSQL, dan kies je bovenaan welke. ClusterForge leest en schrijft op de primary volgens Patroni, want een replica is alleen-lezen; de replica's krijgen alles via de replicatie. De gebruiker van een nieuwe database wordt haar **eigenaar**: hij mag alles in die database, ook tabellen en extensies maken, en verder niets. Een gebruiker heeft in PostgreSQL geen adressen; vanwaar hij binnenkomt bepaalt `pg_hba.conf`, dat ClusterForge bij het uitrollen zet op de nodes van het cluster en het VIP. In de app zet je het VIP van PostgreSQL met poort 5432, zodat de app na een failover vanzelf bij de nieuwe primary uitkomt. Postgres (alleen via de socket, zonder wachtwoord), cf_replicatie en cf_rewind zijn van PostgreSQL en Patroni; daar verandert ClusterForge niets aan.

```sh
runuser -u postgres -- psql -c '\l'
runuser -u postgres -- psql -c '\du'
```

#### Dumps

Onder Databases en gebruikers staat de kaart **Dumps**. Elke nacht om 02:00, in de tijdzone van ClusterForge en dus buiten het testvenster van zondag 03:00 tot 05:00, maakt ClusterForge een dump van elke database. Dat gebeurt op een datanode die Synced is en het VIP niet heeft, zodat de node waar de app naar schrijft geen extra werk krijgt; is er zo een niet, dan op de node met het VIP, en de taak zegt waarom. ClusterForge kiest bij voorkeur de node van de vorige dump, zodat de dumps van een database bij elkaar staan. Stond ClusterForge om 02:00 uit, dan komt de dump zodra het weer draait. Met **Instellen** kies je het uur, hoeveel dagen een dump blijft staan (1 tot 90, standaard 7) of zet je de nachtelijke dump uit.

Een dump is `mariadb-dump --single-transaction` van één database, met routines, events en triggers, zonder de database te blokkeren. cf-agent comprimeert hem met zstd en schrijft hem naar `/var/backups/clusterforge-db/<database>/<database>-<datum>-<tijd>.sql.zst`; die mappen zijn 0700 en de bestanden 0600, alleen voor root. Ze gaan mee in de Proxmox-back-up van de node. Een dump gaat nooit door ClusterForge; het kent alleen de naam, de grootte en de controle.

Elke dump wordt gecontroleerd:

- **goed**: `mariadb-dump` eindigde met `-- Dump completed` en alle tabellen staan erin;
- **verdacht**: compleet, maar er mist een tabel, of hij is meer dan de helft kleiner dan de vorige dump van die database; de reden staat erbij, en het logboek meldt het;
- **mislukt**: `mariadb-dump` gaf een fout, en dan gaat het halve bestand weg, of de dump is niet af; terugzetten kan dan niet.

Per database toont de kaart de leeftijd, grootte en node van de nieuwste goede dump, en een mislukte of verdachte poging daarna. Ouder dan 26 uur is **te oud**. Dezelfde lijst, voor alle clusters, staat bij [Back-ups](#back-ups). **Dump nu** maakt meteen een dump van één database, bijvoorbeeld voor een update van de app. Na elke dump haalt ClusterForge de dumps weg die ouder zijn dan de bewaartijd, maar de nieuwste goede dump van een database blijft altijd staan.

**Terugzetten** bij een goede of verdachte dump zet hem in een nieuwe database naast de bestaande, standaard `<naam>_herstel_<datum van de dump>`, of een naam die je kiest. De bestaande database en de apps merken er niets van. Daarna staat de nieuwe database bij Databases, met haar grootte en tabellen, zodat je kunt vergelijken; mislukt het terugzetten, dan haalt de agent de halve database weer weg. Gebruikers zitten niet in een dump. Een kopie met de standaardnaam krijgt geen nachtelijke dump. Over de bestaande database heen terugzetten, met de app in onderhoud, komt later.

Bij **PostgreSQL** is een dump `pg_dump` van één database, als `<database>-<datum>-<tijd>.pgsql.zst` in dezelfde map. Hij is goed als hij eindigt met `-- PostgreSQL database dump complete` en alle tabellen erin staan. De dump loopt op de **primary** en niet op een replica: op een replica breekt PostgreSQL een lange `pg_dump` na 30 seconden af zodra de primary oude rijen opruimt, en die wachttijd langer maken zou een failover ophouden. `pg_dump` leest een vaste momentopname zonder schrijvers te blokkeren, dus de app merkt alleen wat extra leeswerk. Na een failover kan de nieuwste dump op een node staan die nu replica is. Terugzetten gaat dan over het netwerk: ClusterForge maakt op de primary een tijdelijke beheerder `cf_herstel_<nummer>` met een willekeurig wachtwoord die hoogstens 12 uur geldig is, de agent op de node met de dump zet hem daarmee terug in een nieuwe database met dezelfde eigenaar, en daarna haalt ClusterForge de tijdelijke beheerder weer weg, ook als het mislukt. Wat de dump zelf maakt, zoals een extensie, gaat aan het eind naar de eigenaar van de database.

Met de hand op een node:

```sh
ls -l /var/backups/clusterforge-db/nextcloud/
zstd -dc /var/backups/clusterforge-db/nextcloud/nextcloud-20261012-020000.sql.zst | tail -n 1
mariadb -e "CREATE DATABASE nextcloud_test"
zstd -dc /var/backups/clusterforge-db/nextcloud/nextcloud-20261012-020000.sql.zst | mariadb nextcloud_test
```

En bij PostgreSQL, op de primary:

```sh
runuser -u postgres -- createdb -O nextcloud nextcloud_test
zstd -dc /var/backups/clusterforge-db/nextcloud/nextcloud-20261012-020000.pgsql.zst | runuser -u postgres -- psql -X -v ON_ERROR_STOP=1 nextcloud_test
```

#### Beheer

Op de kaart **Database** van een cluster opent **Beheer…** een overzicht per node: zijn toestand, of hij het VIP heeft, en bij PostgreSQL wie de primary en wie de synchrone replica is en hoeveel WAL elke replica nog niet heeft. Bij elke node staan de acties die er kunnen; kan een actie nu niet, dan staat de reden eronder. Kies je een actie, dan zie je eerst wat er gebeurt, en op prod tik je de slug in. Daarna is het een taak met een stap per node. Is de toestand intussen veranderd, dan weigert ClusterForge en toont het het nieuwe overzicht. Het overzicht ververst elke vijf seconden.

Bij **MariaDB Galera**:

- **Opnieuw synchroniseren** is voor een node die niet meer bijkomt of waarvan de data niet klopt. MariaDB stopt op die node, `grastate.dat` gaat weg en MariaDB start weer; de node haalt dan alle data opnieuw bij een node die Synced is (SST met `mariabackup`). De andere nodes werken door, en heeft de node het VIP, dan gaat dat naar een andere. ClusterForge wacht tot de node weer Synced is en controleert daarna het cluster en het VIP. Op de enige node die Synced is kan het niet: zonder hem stopt de database en is er niemand om de data van te halen. Is geen enkele node Synced, gebruik dan Opnieuw starten.

Bij **PostgreSQL met Patroni**:

- **Primary maken** schakelt over, bijvoorbeeld voor onderhoud aan de primary. Patroni stopt de schrijvers op de primary, wacht tot de replica alles heeft en maakt haar de primary; de oude primary wordt een replica. De app kan een paar seconden niet schrijven, en het VIP van PostgreSQL gaat mee. Het kan alleen naar de **synchrone replica**: die heeft elke bevestigde schrijfactie, en Patroni weigert een andere. ClusterForge wacht tot de nieuwe primary schrijft, de oude als replica bijhoudt en het VIP op de nieuwe staat.
- **Opnieuw opbouwen** is voor een replica die niet meer bijkomt, zoals na een volle schijf of als de WAL die ze nodig heeft al weg is op de primary. PostgreSQL stopt op die node, gooit zijn data weg en kopieert alles opnieuw van de primary (`pg_basebackup`). ClusterForge wacht tot de replica weer bijhoudt. Op de primary kan het niet.

De agent vraagt het aan de REST-API van Patroni op de node zelf, met het wachtwoord uit `/etc/patroni/clusterforge.env`; dat wachtwoord gaat nooit naar ClusterForge. Met de hand op een node van het cluster, met de slug van het cluster als naam:

```sh
sudo sh -c 'set -a; . /etc/patroni/clusterforge.env; patronictl -c /etc/patroni/clusterforge.yml list'
sudo sh -c 'set -a; . /etc/patroni/clusterforge.env; patronictl -c /etc/patroni/clusterforge.yml switchover nextcloud --leader nc1 --candidate nc2 --force'
sudo sh -c 'set -a; . /etc/patroni/clusterforge.env; patronictl -c /etc/patroni/clusterforge.yml reinit nextcloud nc3 --force'
```

Een node met een database in **onderhoud** zetten, **herstarten** of **afsluiten** doe je bij de node zelf (zie [Onderhoud, herstarten en afsluiten](#onderhoud-herstarten-en-afsluiten)); ClusterForge houdt dan rekening met de database:

- Is de node de primary van Patroni, dan schakelt ClusterForge eerst over naar de synchrone replica en wacht tot het VIP van PostgreSQL daar staat. Pas daarna gaat keepalived op de node uit. Zonder die stap zou het VIP nergens staan, want het mag alleen op de primary.
- Na een herstart, of als het onderhoud stopt, wacht de taak tot de node weer meedoet: Synced bij Galera, de primary of een replica die bijhoudt bij Patroni. Pas dan komt keepalived terug en is de node weer actief.
- Zou de actie de database stilleggen, vraagt ClusterForge eerst of je toch door wilt, met de reden: de enige node die Synced is, een primary zonder synchrone replica om naar over te schakelen, een node die nu de data aan een andere geeft (SST), of een node zonder wie etcd geen meerderheid meer heeft. In het laatste geval zet Patroni PostgreSQL op alleen lezen tot de node terug is.

#### Instellingen wijzigen

**Instellingen…** op de kaart Database toont de instellingen die na de uitrol nog kunnen veranderen, met hun waarde nu: bij MariaDB Galera de buffer pool en het maximaal aantal verbindingen, bij PostgreSQL de shared buffers en `max_connections`. Het VIP en de wachtwoorden liggen vast. Zodra je een waarde verandert, zegt het plan wat er verandert, in welke volgorde de nodes gaan, hoeveel geheugen elke node heeft en wat er met een grotere buffer pool of grotere shared buffers vrij blijft. Blijft er minder dan 15% over, dan staat er hoeveel geheugen de node nodig heeft, met het commando `pct set` of `qm set` voor de Proxmox-host. Doet een node nu niet mee, dan kan het niet: anders ligt er tijdens de herstart van een andere node een tweede stil.

Wijzigen maakt een nieuwe revisie, zoals bij Opnieuw toepassen, en is dan een taak:

- Bij **MariaDB Galera** schrijft ClusterForge de configuratie op elke node en herstart het MariaDB node voor node, de node met het VIP als laatste. Na elke node wacht het tot hij weer Synced is. De andere nodes werken door; bij de herstart van de node met het VIP gaat dat naar een andere node.
- Bij **PostgreSQL met Patroni** gaan de shared buffers in de configuratie van Patroni op elke node. `max_connections` bewaart Patroni voor het hele cluster in etcd, dus ClusterForge zet het daar (de REST-API van Patroni, agentprotocol 12). Daarna herstart PostgreSQL node voor node via Patroni: de replica's eerst, de primary als laatste, zonder failover. ClusterForge wacht na elke node tot hij weer bijhoudt en geen instelling meer op een herstart wacht. Bij de herstart van de primary kan de app een paar seconden niet schrijven.

Met de hand gaat `max_connections` zo, waarna elke node een herstart nodig heeft (`patronictl restart nextcloud nc2 --force`, de primary als laatste):

```sh
sudo sh -c 'set -a; . /etc/patroni/clusterforge.env; patronictl -c /etc/patroni/clusterforge.yml edit-config nextcloud -p max_connections=200 --force'
```

Opnieuw toepassen herstart PostgreSQL ook op de nodes waar een instelling nog op een herstart wacht, in dezelfde volgorde.

#### Bijwerken

**Bijwerken…** werkt de database bij naar een kleinere versie binnen die van Debian, zoals MariaDB 11.8.3 naar 11.8.4 of PostgreSQL 17.6 naar 17.7. Het plan toont per node de versie die draait en de pakketten van de database met een nieuwere versie volgens de laatste `apt update` op de node (Debian doet dat elke dag). De volgorde is dezelfde als bij de instellingen: de nodes zonder het VIP of de replica's eerst, de schrijver als laatste, bij Galera daarna de scheidsrechter. De scheidsrechter van PostgreSQL heeft alleen etcd en blijft zoals hij is.

Per node doet de taak:

1. `apt-get update`, en kijken of er voor de pakketten van de database (MariaDB, Galera en hun bibliotheken, of PostgreSQL, Patroni, `python3-etcd` en `libpq`) een nieuwere versie is. Zo niet, dan verandert er op die node niets.
2. Is de node de primary van Patroni, dan schakelt ClusterForge eerst over naar de synchrone replica en wacht tot het VIP meegaat.
3. `apt-get install --only-upgrade` van alleen die pakketten, met de configuratiebestanden zoals ze zijn en zonder iets weg te halen. Andere pakketten en een nieuwe hoofdversie blijven zoals ze zijn.
4. Veranderde er een versie, dan herstart MariaDB, Patroni of garbd, tenzij het pakket dat zelf al deed. ClusterForge wacht tot de node weer Synced is of als replica bijhoudt, en gaat dan pas naar de volgende.

Elke stap op de node heeft een merkteken van de taak in `/var/lib/clusterforge`, zodat een taak die na een herstart van ClusterForge verder gaat niets twee keer doet. Het logboek toont welke pakketten nieuw zijn en per node de versie ervoor en erna. Met de hand doe je hetzelfde zo, op één node tegelijk:

```sh
sudo apt-get update && sudo apt-get install --only-upgrade mariadb-server galera-4 && sudo systemctl restart mariadb
```

#### Een node toevoegen

**Node toevoegen…** op de kaart Database brengt de database naar een node van het cluster die haar nog niet heeft, bijvoorbeeld een vierde node of een node die een kapotte vervangt. Bij een cluster van CT's maak je de CT zelf in Proxmox, met dezelfde Debian als de andere nodes, en installeer je de agent erop; zodra de agent meldt, staat de node in de lijst. Bij VM's kan het een node zijn die ClusterForge uitrolde.

Het plan toont de nieuwe node (Debian, CT of VM, geheugen en vrije schijf, en de stappen die erop komen), per node die er al is wat er verandert, en wat er gebeurt. Het blokkeert als een node van de database nu niet meedoet, als de nieuwe node een andere Debian draait, als zijn agent te oud is, als er niet genoeg schijf of geheugen is, of als er al 9 datanodes zijn. Met een even aantal stemmen erna waarschuwt het: valt het netwerk in twee gelijke helften, dan heeft geen van beide een meerderheid.

- Bij **MariaDB Galera** krijgt de nieuwe node MariaDB met een configuratie waarin alle nodes staan, en haalt hij alle data bij een node die meedoet (SST met mariabackup). Die node werkt door, maar is tijdens het kopiëren trager. De andere nodes krijgen de nieuwe in hun configuratie voor de volgende start; MariaDB herstart daar niet, want een node die al meedoet, leert de nieuwe via Galera kennen.
- Bij **PostgreSQL met Patroni** maakt ClusterForge de nieuwe node eerst lid van etcd, via de eerste node die meedoet, en start etcd erop met `initial-cluster-state: existing`. Tot etcd daar draait, telt etcd een lid meer, dus moeten de andere leden er allemaal zijn; ClusterForge wacht tot drie minuten. Daarna krijgen de andere nodes de nieuwe in `pg_hba` en herlaadt Patroni daar, zonder herstart. Patroni op de nieuwe node maakt een replica met `pg_basebackup` van de primary.
- Het **VIP**: heeft de database een eigen VIP, dan krijgt keepalived op de nieuwe node het en herladen de andere nodes keepalived met de nieuwe als peer, de node met het VIP als laatste. Deelt de database het VIP met de app, dan krijgt de nieuwe node de controle `cf_db` als keepalived daar het VIP al kent; anders zegt het plan dat het VIP er niet komt tot de app er ook draait.

Toevoegen maakt een nieuwe revisie en is een taak, met daarna de controle van het cluster en het VIP. Met de hand gaat het lid worden van etcd zo, op een node die meedoet, waarna je etcd op de nieuwe node start met `initial-cluster-state: existing`:

```sh
etcdctl --endpoints=http://127.0.0.1:2379 member add nc4 --peer-urls=http://10.0.20.189:2380
```

#### Status, incidenten en grafieken

ClusterForge kijkt om de 20 seconden naar elke database die het op een cluster zette: bij Galera naar de positie van elke node, bij Patroni naar de rol en de achterstand. Het resultaat staat als badge naast de naam op de kaart Database en telt mee in de status van het cluster:

- **Down**: Galera heeft geen Primary component, Patroni heeft geen primary, of het VIP staat op een node waar de database niet kan schrijven (bij Galera een node die niet Synced is, bij Patroni een andere node dan de primary). Net na een failover telt het VIP nog niet mee, want keepalived heeft een paar seconden nodig.
- **Verminderd**: een node doet niet mee of is niet Synced (zoals een donor of een node die bijhaalt), een replica loopt meer dan 30 seconden achter, of de scheidsrechter is weg.

Zo'n overgang maakt het cluster verminderd of down en opent een [incident](#incidenten). Loopt er een taak in het cluster, zoals Beheer of Bijwerken, dan kijkt de bewaking niet. Na twee minuten zonder kijken telt de database niet meer mee, zodat een geplande herstart geen incident geeft.

In het logboek, en daarmee in de tijdlijn van [What Broke](#what-broke), komen:

- een **herstart** van MariaDB of PostgreSQL op een node: de server draait, maar zijn uptime begon opnieuw. What Broke zet hem op het moment zelf (nu min de uptime) en telt hem als verandering;
- een **failover** van Patroni, van welke node naar welke;
- een **state transfer** van Galera: een node die opnieuw aansluit en de data die hij mist bij een donor haalt;
- zoals eerder: de database down en weer up, en een node die vijf minuten niet meedoet.

**Grafieken** op de kaart Database toont een lijn per node:

- **MariaDB Galera**: verbindingen en het maximum, query's per seconde (per node en voor het hele cluster), hoeveel van de buffer pool met data gevuld is, de wachtrij van Galera (writesets die een node nog moet verwerken), flow control en de grootte per database. Flow control is het deel van de tijd dat Galera het schrijven pauzeert omdat een node niet bijhoudt; blijft het boven een paar procent, dan schrijft de app trager en is die node te zwaar belast.
- **PostgreSQL met Patroni**: verbindingen en het maximum, transacties per seconde (per node en voor het hele cluster), hoeveel er uit het geheugen gelezen wordt (cache-hits), de achterstand van de replica's en de grootte per database.

De agent meet ze elke 15 seconden met één vaste query, de grootte per database eens per minuut, en stuurt ze met de andere metrics naar VictoriaMetrics als `node_db_*` met het label `engine`. Dat kan vanaf agentprotocol 13; werk een oudere agent bij met `--upgrade`. In Grafana vind je ze bijvoorbeeld zo:

```promql
rate(node_db_queries_total{cluster="nextcloud"}[5m])
```

### Een database verhuizen

Bij **Databases** staat naast elke database van MariaDB of PostgreSQL **Verhuizen…**. Dat opent het plan om haar van de oude server, zoals een gedeeld databasecluster, naar de database op de nodes van de app te brengen: van MariaDB naar MariaDB Galera, van PostgreSQL naar PostgreSQL met Patroni. Het plan verandert niets en een kijker ziet het ook. Hieronder staat hoe het bij MariaDB gaat; wat bij PostgreSQL anders is, staat onder [Van PostgreSQL](#van-postgresql).

- **Van**: de grootte van de data en indexen, het aantal tabellen en rijen, de views, routines, triggers en events, de tekenset en de versie. ClusterForge leest de database op een node van de oude server die het VIP niet heeft, zodat de app er niets van merkt, en toont wie de database de laatste dertig dagen gebruikte.
- **Naar**: de clusters met een MariaDB van ClusterForge. Gebruikt de app van een cluster de database nu al, dan staat dat cluster gekozen. Je ziet de node die importeert, zijn versie, het adres waar de app de database straks vindt, de buffer pool, de vrije schijf op elke node en of de nieuwe node de oude server echt bereikt.
- **Controle**: wat blokkeert, wat aandacht vraagt en wat goed is om te weten.

Wat blokkeert: de database bestaat niet, de oude server draait niet goed genoeg om van te lezen, een agent is te oud, het is hetzelfde cluster, de nieuwe node bereikt poort 3306 van de oude server niet (een firewall of `bind-address`; er staat bij welke poort van welke adressen open moet), MariaDB weigert de node, bijvoorbeeld na te veel mislukte verbindingen (dan staat `mariadb-admin flush-hosts` erbij), de nieuwe MariaDB is ouder dan de oude, een andere database heet al `<naam>_proef`, er loopt al een proef, een verhuizing wacht nog op overschakelen, of er is niet genoeg schijf voor de data plus 1 GB.

Wat aandacht vraagt, laat de proef wel door:

- **tabellen zonder primaire sleutel**: Galera repliceert die slecht of niet. Voor Nextcloud lost `sudo -u www-data php occ db:add-missing-primary-keys` het op; anders staat er welke tabellen het zijn.
- **MyISAM of een andere engine dan InnoDB**: Galera repliceert alleen InnoDB. Het plan geeft per tabel de `ALTER TABLE … ENGINE=InnoDB`.
- **ook andere apps gebruiken de database**: die moeten mee verhuizen of eerst een eigen database krijgen.
- **de database staat er al** op het nieuwe cluster, of de oude server draait MySQL in plaats van MariaDB.
- **krappe schijf** (minder dan twee keer de data) of **een te kleine buffer pool** voor de data die er al staat plus deze database; dan wordt de app trager tot je `innodb_buffer_pool_size` groter maakt.

Goed om te weten: het verkeer gaat via het VIP van de oude server, zonder TLS, de versie gaat omhoog, of een vorige proef liet een kopie achter die de volgende proef eerst weghaalt.

#### Proefverhuizing

**Proefverhuizing starten** (alleen een beheerder) doet de verhuizing zonder dat de app iets merkt, om te zien of het lukt en hoe lang het duurt:

1. Controle: het plan opnieuw, met dezelfde blokkades.
2. Een kopie van een vorige proef weghalen, als die er nog is.
3. Op de oude server maakt de agent een tijdelijke gebruiker `cf_verhuizing_<nummer>` met een willekeurig wachtwoord, die alleen mag lezen (`SELECT`, `SHOW VIEW`, en zo nodig `TRIGGER`, `EVENT` en het lezen van de routines) en alleen vanaf de adressen van de nieuwe node. De nieuwe node haalt de database met `mariadb-dump --single-transaction` en zet haar meteen in `<naam>_proef`; er komt geen bestand op schijf. Daarna haalt de agent de tijdelijke gebruiker weer weg, ook als de proef mislukt of je ze annuleert.
4. Rijen en objecten vergelijken: ClusterForge telt de rijen van elke tabel aan beide kanten, en de views, routines, triggers en events. Een paar rijen verschil is normaal, want de app schrijft intussen verder; ontbreekt er een tabel of object, dan is de proef mislukt.
5. `<naam>_proef` weghalen.

Het verslag staat onder **Proeven en verhuizingen**: hoe lang ophalen en importeren duurde, hoe lang het tellen duurde, hoeveel SQL over het netwerk kwam, de rijen oud en nieuw, de tabellen met een ander aantal en een mislukte stap met de reden. Zo weet je hoe lang de app straks in onderhoud moet. Het wachtwoord van de tijdelijke gebruiker bewaart ClusterForge nergens: niet in de taak, het logboek of het verslag.

#### Verhuizen

De echte verhuizing zet de database onder dezelfde naam op het nieuwe cluster, met dezelfde gebruikers en wachtwoorden. De app staat daarvoor in onderhoud, ongeveer zo lang als de proef zei. Alles staat onder **Verhuizen** op dezelfde pagina.

1. **Zet de app in onderhoud.** Voor Nextcloud, op een node van de app:

   ```sh
   sudo -u www-data php occ maintenance:mode --on
   ```

   Je ziet wie er nu met de oude database verbonden is, op elke server van het oude cluster; **Opnieuw kijken** leest het opnieuw. Is er nog iemand, dan start de verhuizing niet, tenzij je **Toch verhuizen** aanvinkt.

2. **Verhuizing starten** (alleen een beheerder; raakt de verhuizing een prodcluster, dan met de slug en tweestapsverificatie). De taak gaat na een herstart van ClusterForge verder:
   1. Controle: het plan opnieuw. Ook een database met dezelfde naam op het nieuwe cluster blokkeert, want de verhuizing schrijft niet over een bestaande database.
   2. Onderhoud: ClusterForge wacht even tot niemand meer met de oude database verbonden is. Lukt dat niet, dan stopt de taak.
   3. Gebruikers maken: elke gebruiker met rechten op de database en een gewoon wachtwoord (`mysql_native_password`) komt op het nieuwe cluster met dezelfde hash van zijn wachtwoord, vanaf de adressen van de nodes en VIP's van het cluster en `localhost`, met dezelfde rechten op die database. De app logt dus in met haar oude wachtwoord. Een gebruiker die via de socket of een andere plugin inlogt, gaat niet mee; dat staat in het plan.
   4. Ophalen en importeren, zoals bij de proef, maar in de echte naam.
   5. Vergelijken: elke tabel moet aan beide kanten evenveel rijen hebben, en alle views, routines, triggers en events moeten er zijn. Wijkt er iets af, dan stopt de taak: de nieuwe database en de gebruikers die ClusterForge maakte, gaan weg, en de app staat nog op de oude database. Zet dan het onderhoud weer uit.

3. **Wijs de app naar de nieuwe database.** Het adres staat bij Verhuizen; gebruiker en wachtwoord blijven hetzelfde. Voor Nextcloud, op elke node met een eigen `config.php`:

   ```sh
   sudo -u www-data php occ config:system:set dbhost --value=10.0.20.185
   sudo -u www-data php occ maintenance:mode --off
   ```

4. **De app draait op de nieuwe database.** ClusterForge kijkt of niemand nog met de oude database verbonden is en de app wel met de nieuwe; anders zegt het wie, en kun je toch overschakelen. Daarna zet het de oude database op slot: de gebruikers van de app houden alleen `SELECT` en `SHOW VIEW` (voor zover ze die al hadden), zodat een vergeten cronjob niet stilletjes in de oude database verder schrijft.

Zolang je niet overgeschakeld hebt, kan **Annuleren**: tik de naam van de database, en ClusterForge haalt de kopie en de nieuwe gebruikers weg. Dat kan niet meer als iemand de nieuwe database al gebruikt; schakel dan over.

Na de verhuizing haalt **Slot eraf halen** het slot er weer af: de gebruikers krijgen hun rechten van vóór de verhuizing terug. Terug naar de oude database gaat zo: slot eraf en de app weer naar het oude adres wijzen. Wat de app sinds het overschakelen schreef, staat dan alleen in de nieuwe database. De oude database blijft staan tot je ze [afbouwt](#het-oude-cluster-afbouwen).

In het logboek: `cluster.database_move_copied`, `cluster.database_move_failed`, `cluster.database_moved`, `cluster.database_move_cancelled`, `cluster.database_locked` en `cluster.database_unlocked`.

Met de hand op de oude server, om te zien of er nog een tijdelijke gebruiker staat:

```sh
mariadb -e "SELECT User, Host FROM mysql.user WHERE User LIKE 'cf\_verhuizing\_%'"
```

#### Van PostgreSQL

Een database van PostgreSQL verhuist naar een cluster met PostgreSQL en Patroni, op dezelfde manier en met dezelfde knoppen. De nieuwe node is de primary volgens Patroni. Wat anders is:

- **De tijdelijke gebruiker.** `cf_verhuizing_<nummer>` komt op de primary van de oude server, met een willekeurig wachtwoord als SCRAM-verifier, hoogstens 12 uur geldig en alleen om te lezen (`default_transaction_read_only`). Hij leest als **lid van de eigenaar** van de database, want in PostgreSQL bestaat geen recht om een hele database te lezen. Is de eigenaar een superuser, dan blokkeert het plan: ClusterForge maakt nooit een gebruiker met de rechten van een superuser. Mag de eigenaar niet alles in de database lezen, dan staan de `GRANT`'s erbij die het oplossen.
- **pg_hba.conf.** De oude server moet de nieuwe node binnenlaten. Het plan probeert dat en geeft, als het niet mag, de regel met het adres dat de oude server ziet, zo voor een node die als 10.0.20.186 binnenkomt:

  ```sh
  echo "host nextcloud all 10.0.20.186/32 scram-sha-256" | sudo tee -a "$(sudo -u postgres psql -XAtc 'SHOW hba_file')"
  sudo -u postgres psql -Xc 'SELECT pg_reload_conf()'
  ```

  Doe dat op elke server van het oude cluster. Beheert Patroni daar `pg_hba.conf`, zet de regel dan met `patronictl edit-config` bij `postgresql.pg_hba`. Ziet de oude server een ander adres dan dat van de node, dan staat er NAT of een proxy tussen; dat zegt het plan ook. Haal de regel na de verhuizing weer weg.
- **Extensies.** Gebruikt de database een extensie die op de nieuwe node niet te installeren is, zoals PostGIS, dan blokkeert het plan. Installeer het pakket op elke node van het nieuwe cluster; de dump maakt de extensie dan zelf.
- **Versies.** Van een oudere naar een nieuwere PostgreSQL gaat goed, zoals van 15 naar 17; terug naar een oudere niet, want `pg_dump` leest geen nieuwere server.
- **De rollen.** Mee gaan de eigenaar, elke rol die iets in de database bezit of er rechten op heeft, ook een groepsrol zonder inloggen, en wie daar lid van is. Ze komen met dezelfde SCRAM-verifier, dus de app logt in met haar oude wachtwoord, en met dezelfde lidmaatschappen. Logt een rol al in op het nieuwe cluster, dan krijgt hij het wachtwoord van de oude server; het plan waarschuwt daarvoor, want wie hem daar met een ander wachtwoord gebruikt, moet dat aanpassen. Wat de verhuizing blokkeert (de proef neemt geen rollen mee en gaat wel): een wachtwoord als **md5-hash**, want het nieuwe cluster laat alleen `scram-sha-256` toe. Het plan geeft het commando om het wachtwoord op de oude server opnieuw te zetten, met hetzelfde wachtwoord als in de app:

  ```sh
  sudo -u postgres psql -X -c "SET password_encryption = 'scram-sha-256'" -c "\password rapport"
  ```

  Ook een superuser, een rol van PostgreSQL zelf (postgres, cf_replicatie en cf_rewind) of een rol met een wachtwoord in een andere vorm gaat niet mee; bezit zo'n rol iets in de database, dan blokkeert het plan. Een rol zonder wachtwoord, zoals een die via `peer`, LDAP of een certificaat inlogt, gaat mee, maar het nieuwe cluster vraagt er altijd een; geef hem er een bij Databases en gebruikers.
- **De proef** haalt de database zonder eigenaars en rechten (`pg_dump --no-owner --no-privileges`) in `<naam>_proef`. De echte verhuizing maakt de database met dezelfde eigenaar, neemt de eigenaars en rechten van de dump mee en geeft de rollen weer `CONNECT`, `CREATE` en `TEMPORARY` op de database, zoals op de oude server. Rijen en objecten vergelijken telt de tabellen, views, functies en triggers; PostgreSQL kent geen events.
- **Het slot** zet `default_transaction_read_only` op de oude database, voor iedereen die daarna verbindt: wie nu verbindt, kan alleen nog lezen. Een verbinding die al open was, merkt het pas als ze opnieuw verbindt. **Slot eraf halen** zet het terug. Wie echt nog moet schrijven, kan dat met `SET default_transaction_read_only = off` of `BEGIN READ WRITE`; het slot houdt een vergeten cronjob tegen, geen beheerder.
- **Annuleren** haalt alleen de rollen weg die ClusterForge op het nieuwe cluster maakte; een rol die er al stond, blijft.

Wat niet meegaat: de rechten op de database voor PUBLIC, instellingen met `ALTER DATABASE … SET` of `ALTER ROLE … IN DATABASE` (het plan zegt hoeveel het er zijn), en de locale: de database krijgt dezelfde encoding, maar de sortering van het nieuwe cluster (bij een andere encoding dan UTF-8 de sortering `C`). In de app zet je het VIP van PostgreSQL met poort 5432; gebruiker en wachtwoord blijven hetzelfde.

Met de hand op de oude server, om te zien of er nog een tijdelijke gebruiker staat:

```sh
sudo -u postgres psql -XAtc "SELECT rolname, rolvaliduntil FROM pg_roles WHERE rolname LIKE 'cf\_verhuizing\_%'"
```

#### Het oude cluster afbouwen

Een verhuisde database blijft op de oude server staan, op slot, zodat je terug kunt. Op de verhuispagina staat onder de verhuizing **Oude database afbouwen**, met de dag vanaf wanneer het kan.

- **Zeven dagen stil.** Afbouwen kan zeven dagen na het overschakelen, of zeven dagen na de laatste verbinding met de oude database als die later kwam. Alleen verbindingen van buiten de databasenode tellen: een back-up of cronjob op db1 zelf houdt het niet tegen. Die toont de pagina wel; zet zo'n back-up stop, want na het afbouwen mislukt hij. ClusterForge leest de verbindingen elke minuut, dus een verbinding van een paar seconden kan het missen. Daarom blijft het slot die week op de oude database: een vergeten cronjob die er schrijft, krijgt een fout.
- **Niemand verbonden.** Bij het starten en nog eens vlak voor het weghalen kijkt ClusterForge of er nu iemand met de oude database verbonden is, ook op de node zelf. Is dat zo, dan haalt het niets weg en zegt het wie.
- **Een laatste dump.** Op een node van het oude cluster, in `/var/backups/clusterforge-db/<database>/`, gecontroleerd zoals de nachtelijke dump: compleet en met alle tabellen. Lukt de dump niet, dan stopt het afbouwen en blijft de database staan. De dump blijft staan tot je hem weghaalt, en is weg als je de node weghaalt. De pagina geeft het commando om hem te kopiëren:

  ```sh
  scp root@db1:/var/backups/clusterforge-db/nextcloud/nextcloud-20261020-140512.sql.zst .
  ```

- **Weghalen.** Tik de naam van de database, op prod ook de slug, met tweestapsverificatie. ClusterForge haalt de database weg en daarna de gebruikers die alleen die database hadden. Een gebruiker blijft staan als hij rechten heeft op alle databases of op een andere, of de laatste 30 dagen een andere database gebruikte; de taak zegt waarom. Bij PostgreSQL blijft ook een rol met SUPERUSER, CREATEROLE of REPLICATION staan. MariaDB laat bij `DROP DATABASE` de rechten op die database staan, dus wie blijft, houdt ze; de taak geeft de `REVOKE` om ze weg te halen:

  ```sql
  REVOKE ALL PRIVILEGES ON `nextcloud`.* FROM 'rapport'@'10.0.20.%';
  ```

Bovenaan de pagina **Databases** staat per oude server wat er nog op staat: wat nog niet verhuisd is, met een link om het te verhuizen, en wat op afbouwen wacht. Staat er geen database van een app meer op, dan zegt ClusterForge dat db1, db2 en db3 leeg zijn. De CT's of VM's weghalen doe je in Proxmox; ClusterForge verwijdert geen nodes. Gaf een server geen lijst met databases, dan zegt ClusterForge niet dat hij leeg is.

Daar staan ook de **firewallregels** van elke app naar de oude servers, zoals Nextcloud naar 10.0.20.180:3306, afgeleid uit de verbindingen die ClusterForge zag:

- **kan weg**: alles van die app is afgebouwd en ze verbond na het overschakelen niet meer;
- **nodig tot het afbouwen**: met de regel kan de app nog terug naar de oude database;
- **nog in gebruik**: een node van de app verbond na het overschakelen nog met de oude servers. Dan wijst er nog iets naar het oude adres, zoals een tweede `config.php` of een cronjob.

De firewall zelf beheert ClusterForge niet; een regel die weg kan, haal je daar weg.

In het logboek: `cluster.database_retired`, met de dump, de gebruikers die weggingen en of de oude server nu leeg is.

### In de documentatie

Een cluster met een databaseserver op zijn nodes krijgt in de [documentatie](#documentatie) een hoofdstuk **Database**, na Architectuur (en na Kubernetes). Per onderdeel Database staat erin:

- de engine en versie, de datanodes en de scheidsrechter, en het adres voor de app: het VIP van de app of het eigen VIP, met de poort;
- de naam van het Galera-cluster of de scope van Patroni, de nachtelijke dumps (tijd, hoe lang bewaard, waar en op welke node) en, bij Galera, of ClusterForge het cluster na een volledige uitval vanzelf opnieuw begint;
- de databases met hun grootte (in twee cijfers, zodat een database die wat groeit niet elke nacht een nieuwe versie geeft), het aantal tabellen, wie ze de laatste 30 dagen gebruikte en met welke gebruiker, en of er een goede dump is. De database van de failovertest staat er niet bij.

Daarna de runbooks, met de namen van dit cluster: eerst wat je in ClusterForge kiest, en daaronder de commando's voor als ClusterForge er niet bij kan.

- **Opnieuw starten na een volledige uitval.** Bij Galera de seqno per node uit `grastate.dat` of `galera_recovery`, het cluster beginnen op de node met de nieuwste data met `galera_new_cluster`, de andere nodes starten en `wsrep_cluster_size` nakijken. Bij Patroni wat etcd en Patroni zelf doen en hoe je het nakijkt met `patronictl list`.
- **Een node opnieuw synchroniseren** (Galera) of **een replica opnieuw opbouwen** (Patroni, `patronictl reinit`).
- **Een dump terugzetten**: naast de bestaande database met **Terugzetten**, of met de hand over de bestaande heen met `zstd -dc … | mariadb`, of bij PostgreSQL in een nieuwe database op de primary.
- **Overschakelen**: het VIP vrijmaken met Onderhoud of Herstarten, of `systemctl stop keepalived`; bij Patroni **Primary maken** of `patronictl switchover`.
- **Een database verhuizen**: de vijf stappen van plan tot afbouwen.

Een databaseserver die ClusterForge niet uitrolde, zoals een gedeeld databasecluster op nodes van het cluster, staat erin met zijn databases en wie ze gebruikt, zonder runbooks. Wachtwoorden, hashes en de inhoud van tabellen staan er nooit in. De hoofdstukken Herstel en Runbooks verwijzen naar dit hoofdstuk. Komt er een database bij of gaat ze weg, dan schrijft ClusterForge de documentatie opnieuw zoals bij elke wijziging, en de export naar Git haalt `database.md` weg als het hoofdstuk er niet meer is.

### Assistent bij databases

Staat de [assistent](#assistent) aan, dan kent hij twee tools voor databases, net als de rest alleen lezend en met de rechten van wie de vraag stelt:

- **database**: de databases van een cluster. Per onderdeel Database de gezondheid met de reden, de nodes met hun rol en toestand (Synced, Donor, primary, replica en hoe ver die achterloopt), het VIP en wie het heeft, de laatste herstart, de instellingen, de databases met hun grootte, en per database de nieuwste dump en of die vers is. Ook de databaseservers op de nodes die ClusterForge alleen leest.
- **database_clients**: wie welke database gebruikt. Per database de clusters, nodes en onbekende adressen die er de laatste 30 dagen mee verbonden, met de gebruiker in de database, het aantal verbindingen en of de afhankelijkheid in de graaf staat; bij een onderdeel Database ook de gebruikers met hun rechten. Met de naam van een app-cluster vind je zo ook de databases elders die het nog gebruikt, zoals op het gedeelde databasecluster.

Zo kun je vragen 'Hoe staat de database van Nextcloud ervoor?' of 'Wie gebruikt de database wiki nog?'. **Analyseer** op een incident dat een cluster met een database raakte, kijkt ook met deze tools, legt in gewone woorden uit wat Galera of Patroni deed (een node die het cluster verliet en met een volledige kopie terugkwam, een failover, een cluster zonder meerderheid), en noemt bij Wat je kunt doen het runbook uit het hoofdstuk Database en Beheer.

Wachtwoorden, hashes en rijen gaan niet mee: de tools lezen dezelfde weergaven als de webinterface, die nooit een wachtwoord of hash van een databasegebruiker toont, en ClusterForge leest geen inhoud van tabellen, alleen de catalogus en de status.

### Hoe ClusterForge de databases leest

cf-agent doet het op de node zelf, als root via de socket: `mariadb` voor MariaDB en `psql` als de gebruiker postgres voor PostgreSQL, en de REST-API van Patroni op `127.0.0.1:8008`; lezen kan daar zonder wachtwoord, en voor overschakelen of opnieuw opbouwen leest de agent het wachtwoord van de REST-API uit `/etc/patroni/clusterforge.env`, dat alleen root leest. Er is dus geen databasegebruiker of wachtwoord voor ClusterForge nodig en poort 3306 of 5432 hoeft niet open. De agent stelt alleen vaste query's op de systeemtabellen, zoals `SHOW GLOBAL STATUS` en `pg_stat_replication`; hij leest nooit de inhoud van een tabel en neemt geen SQL van de server aan. Een database of gebruiker aanmaken of weghalen gaat met vaste opdrachten die de agent zelf maakt, alleen met namen van kleine letters, cijfers en `_`; het wachtwoord gaat via stdin naar `mariadb`, zodat het nooit in `ps` staat, en een foutmelding komt terug zonder wachtwoord. Hij doet hoogstens twee verzoeken tegelijk, elk hoogstens 30 seconden, en telt de tabellen van hoogstens 100 databases.

Een dump en terugzetten lopen op de agent op de achtergrond, één tegelijk per node, en ClusterForge vraagt elke paar seconden hoe ver ze zijn. De agent start `mariadb-dump` (of `mysqldump`) met vaste opties en schrijft eerst naar een `.partial`-bestand dat pas na de controle zijn naam krijgt; naast elke dump staat een klein `.json`-bestand met de grootte en de controle. Terugzetten doet `CREATE DATABASE` voor een naam die nog niet bestaat en stuurt de dump door `mariadb`; de agent schrijft nooit in een database die er al was. Bij PostgreSQL rekent de agent de SCRAM-verifier van een nieuw wachtwoord zelf uit, zoals `\password` in psql, en stuurt alleen die via stdin naar `psql`, zodat het wachtwoord ook niet in het log van PostgreSQL kan komen. Het wachtwoord van de tijdelijke beheerder om terug te zetten staat alleen in een passfile in een eigen map van de agent (modus 0700), die hij na afloop weghaalt; ClusterForge bewaart het nergens, ook niet in de taak.

Bij een proefverhuizing praat de nieuwe node over het netwerk met de oude server, als de tijdelijke gebruiker. Het wachtwoord staat alleen in een optiebestand in een eigen map van de agent (modus 0700), dat hij na afloop weghaalt, en dus nooit in `ps`. Kan de oude server TLS, dan gebruikt de agent het, zonder het certificaat te controleren, want dat is meestal zelfgemaakt. Een MariaDB ouder dan 11.3 kent het recht `SHOW CREATE ROUTINE` niet; dan krijgt de tijdelijke gebruiker `SELECT` op `mysql.proc` om de routines te lezen. De agent importeert één database tegelijk, en ClusterForge vraagt elke paar seconden hoe ver hij is.

Bij een verhuizing leest de agent op de oude server de gebruikers met rechten op de database uit `information_schema.SCHEMA_PRIVILEGES` en `mysql.user`, met de hash van hun wachtwoord. Die gaat via ClusterForge, alleen in het geheugen, naar de agent op de nieuwe node, die de gebruiker maakt met `IDENTIFIED VIA mysql_native_password USING '<hash>'` via stdin; een foutmelding komt terug zonder hash. ClusterForge bewaart de hash nergens: niet in de taak, het logboek, de verhuizing of de API. Het slot is een `REVOKE ALL PRIVILEGES` op de database en een `GRANT` van wat de gebruiker mag blijven lezen. De rechten van vóór de verhuizing, zonder hash, bewaart ClusterForge bij de verhuizing, zodat het slot eraf kan.

Bij PostgreSQL leest de agent de rollen uit `pg_roles`, `pg_authid`, `pg_shdepend`, `pg_auth_members` en de rechten op de database, en de SCRAM-verifier alleen bij de echte verhuizing. De agent op de nieuwe node zet hem met `ALTER ROLE … PASSWORD '<verifier>'` via stdin naar `psql`; PostgreSQL slaat een verifier op zoals hij is, dus het wachtwoord zelf komt nergens. Ook die verifier bewaart ClusterForge nergens. Het wachtwoord van de tijdelijke gebruiker staat alleen in een passfile in een eigen map van de agent (modus 0700). Voordat hij iets doet, kijkt de agent of de tijdelijke gebruiker over het netwerk binnen mag: met een gebruiker die niet bestaat, zodat `pg_hba.conf` zegt of de node binnen mag en vanaf welk adres, zonder dat er een wachtwoord over het netwerk gaat.

Dit kan vanaf **agentprotocol 9**, voor PostgreSQL vanaf **agentprotocol 10**, een database van PostgreSQL verhuizen vanaf **agentprotocol 11**, op de oude en de nieuwe node, Patroni beheren (Primary maken, Opnieuw opbouwen en overschakelen voor onderhoud) vanaf **agentprotocol 12**, de metingen voor de grafieken vanaf **agentprotocol 13**, de schrijftest van de [failovertest](#met-een-database) vanaf **agentprotocol 14**, het nakijken van de rijen na [alles herstarten](#alles-herstarten) vanaf **agentprotocol 15**, en [Zelfherstel](#zelfherstel) vanaf **agentprotocol 22**, op de nodes van de database: een oudere agent zou een opdracht voor PostgreSQL op MariaDB uitvoeren, dus ClusterForge stuurt hem er geen. Met een oudere agent toont de pagina het commando om hem bij te werken:

```sh
curl -fsSL https://clusterforge.example/install/agent.sh | sudo sh -s -- --server https://clusterforge.example --upgrade
```

Bouw ook je golden image opnieuw met `golden-image.sh --replace`, anders krijgen nieuwe VM's de oude agent. De rest van ClusterForge blijft werken met een oudere agent. Het plan voor de hele fase staat in [docs/design/fase-5.md](docs/design/fase-5.md) (PR #57).

## Cronjobs

Het menu **Cronjobs** toont elke cronjob en systemd-timer op je nodes, per cluster. ClusterForge leest ze elke vijf minuten via de agent, of meteen met **Nu opnieuw lezen**:

- `/etc/crontab` en de bestanden in `/etc/cron.d` (met de gebruiker uit de regel);
- de crontabs van gebruikers, zoals `crontab -e` ze maakt (`/var/spool/cron/crontabs`);
- de scripts in `/etc/cron.hourly`, `daily`, `weekly` en `monthly`;
- elke systemd-timer met de service die hij start.

Dezelfde job op meer nodes van een cluster staat er één keer, met per node de laatste run; staat hij niet op elke node, dan zegt de lijst waar hij ontbreekt. Het schema staat in gewone taal ("elke 6 uur, op minuut 0", "op werkdagen, om 04:00") met de volgende run. Jobs van Debian-pakketten, zoals `logrotate` of `apt-daily`, zijn standaard verborgen. De pagina van een node heeft een kaart Cronjobs met alleen de jobs van die node.

**De laatste run.** Bij een timer zegt systemd wanneer hij liep, hoe lang en met welke exitcode. Bij cron leest de agent de journal: cron logt standaard alleen dat een job start, dus staat er **Gestart**. Start je cron met `-L 15`, dan logt hij ook het einde en de exitcode, en zegt ClusterForge **Gelukt** of **Mislukt**, met hoe vaak een job de laatste 24 uur mislukte.

**Wachtwoorden.** De agent maskeert wachtwoorden in commando's voordat ze de node verlaten: `-pGeheim` van mysql en mariadb-dump, `--password=`, `PASSWORD=` en `PGPASSWORD=`, een gebruiker met wachtwoord in een URL, en een `Authorization`-header. Beter is natuurlijk een bestand met het wachtwoord op de node, zoals `~/.my.cnf`.

Staat dezelfde job (zelfde bestand en gebruiker) op een node anders, dan staat bij de job **Anders op** met het schema of commando van die node. Het cluster krijgt dan het label "verschilt per node".

**Fouten van cron vastleggen.** Logt cron op een node niet of een job lukte, dan staat boven de lijst een knop **Fouten van cron vastleggen** (alleen voor een beheerder). Die schrijft `/etc/systemd/system/cron.service.d/50-clusterforge-log.conf` met dezelfde `ExecStart` plus `-L 15`, doet `systemctl daemon-reload` en herstart cron; jobs die al lopen, draaien door. Weghalen kan met `rm` van dat bestand, `systemctl daemon-reload` en `systemctl restart cron`. Dit kan vanaf agentprotocol 17, en alleen bij cron van Debian; cronie logt geen exitcode.

**Waarschuwingen.** ClusterForge legt elke eigen cronjob naast wat het zelf beheert:

- **Gevaarlijk:** een cron die Galera opnieuw start (`galera_new_cluster`, `safe_to_bootstrap`, een script met galera en bootstrap of recover in de naam) op een cluster met het [onderdeel Database](#met-een-database) MariaDB. ClusterForge start Galera zelf opnieuw, alleen als alle nodes antwoorden en op de node met de nieuwste data; zo'n cron kan bij een netwerkstoring een tweede cluster starten. Haal hem weg. Zonder het onderdeel is het een waarschuwing.
- **Gevaarlijk:** een script dat zelf een PostgreSQL-primary zoekt of promote (`pg_ctl promote`, `repmgr`) naast het onderdeel Database PostgreSQL: Patroni kiest de primary.
- **Let op:** een cron die MariaDB of PostgreSQL herstart naast het onderdeel van die database; dat kan een failovertest of onderhoud doorkruisen.
- **Tip:** een `rsync` naar een andere host is een bestandssync die ClusterForge straks beheerd kan draaien, alleen vanaf de node met het VIP.

**Fouten.** Mislukt een job (een exitcode die niet 0 is), dan staat er een event "Cronjob op db1 mislukt" in het logboek en opent een [incident](#incidenten) "Cronjob … op db1 mislukt" met ernst laag; een mislukte back-up maakt een cluster niet down. Lukt de volgende run, dan volgt "Cronjob op db1 lukt weer" en sluit het incident. Jobs van Debian-pakketten tellen niet mee.

**Wijzigingen.** Komt er een job bij, verdwijnt er een of verandert het schema of commando, dan staat dat in het logboek en in [What Broke](#what-broke), zoals "Cronjob op nc1 gewijzigd in /etc/cron.d/nextcloud-sync: schema 0 */4 * * *, was 0 */6 * * *".

Lezen kan vanaf **agentprotocol 16**, de knop Fouten van cron vastleggen vanaf **17**, beheerde jobs vanaf **18**. Met een oudere agent toont de pagina het commando om hem bij te werken; de andere onderdelen blijven werken.

### Beheerde cronjobs

Bovenaan **Cronjobs** staan de jobs die ClusterForge zelf beheert. Een beheerder voegt er een toe met **Cronjob toevoegen**: het cluster, een naam, het schema (een cronregel zoals `0 */6 * * *`, of een keuze uit de lijst; het formulier toont het in gewone taal met de volgende runs), waar hij draait, de gebruiker, het commando en de tijdslimiet (standaard een uur).

- **Op de node met het VIP** (standaard): de timer staat op elke node, maar alleen de node die op dat moment het VIP heeft, voert de job uit. Verhuist het VIP, dan draait de volgende run op de nieuwe node. Tijdens een wissel kan een run vallen of heel kort op twee nodes starten; een job die dat niet mag, moet zelf tegen een dubbele run kunnen.
- **Op één vaste node** of **op alle nodes** van het cluster.

Op de node is een job een systemd-timer `cf-cron-<id>.timer` die `cf-cron@<id>.service` start; die draait `cf-agent cron-run <id>`. `systemctl list-timers` toont hem dus ook zonder ClusterForge, en een gemiste run komt na een herstart alsnog (`Persistent=true`). cf-agent neemt een slot (loopt de vorige run nog, dan slaat deze over), start het commando met `/bin/sh -c` als de gekozen gebruiker, stopt het na de tijdslimiet, en stuurt de run met de exitcode en de laatste 64 KiB uitvoer naar ClusterForge; de volledige uitvoer van de laatste tien runs blijft op de node in `/var/lib/clusterforge/cron/output`. Is ClusterForge even weg, dan lopen de jobs door en komen de runs later binnen.

De pagina van een job heeft **Nu uitvoeren**, **Bewerken**, **Uitzetten** en **Weghalen**, en de runs met per run de node, de uitkomst, de duur en de uitvoer. Een run die mislukt, staat in het logboek en in What Broke. Een kijker ziet de jobs en runs, maar niet de uitvoer van een job die als root draait. Op prod vraagt elke wijziging tweestapsverificatie en de slug. Runs ouder dan negentig dagen gaan weg, behalve de laatste per job en node. Dit vraagt **agentprotocol 18**; een node met een oudere agent zegt dat bij de job.

#### Opvolgen

Een run die loopt, staat als **Loopt** bij de job; met **Volgen** zie je zijn uitvoer tot nu toe, die elke twee seconden ververst, en met **Stoppen** stopt cf-agent het commando met alles wat het startte (de run krijgt de uitkomst **Gestopt**). Bij **Opnieuw proberen** kies je tot drie herhalingen: mislukt een run of haalt hij de tijdslimiet niet, dan probeert cf-agent het een minuut later nog eens; die run staat als **herhaling** in de lijst. Staat **Een incident openen als de job mislukt** aan (standaard), dan opent een mislukte laatste run op een node een incident op de job, dat sluit zodra de job daar weer lukt. Ging een gepland moment meer dan tien minuten voorbij zonder dat de job startte, dan staat er **run gemist** bij, bijvoorbeeld omdat de node uit stond of cf-agent niet draaide. Een mislukte cronjob verandert de status van het cluster bewust niet: de app draait gewoon, alleen de job heeft aandacht nodig. Stoppen en live uitvoer vragen **agentprotocol 19**; herhalen werkt pas als de agent op de node is bijgewerkt.

#### Overnemen en uitzetten

Bij een gevonden cronjob in `/etc/crontab`, `/etc/cron.d` of de crontab van een gebruiker staan voor een beheerder drie knoppen. **Overnemen in ClusterForge** opent het formulier van een beheerde job met het schema, de gebruiker en het commando ingevuld: op die ene node als hij op één node stond, anders op de node met het VIP (of op alle nodes als het cluster geen VIP heeft). Na **Overnemen** zet ClusterForge de oude regel in commentaar op elke node waar hij aanstond, zodat de job niet twee keer draait. Een wachtwoord dat in het commando stond, ziet ClusterForge niet (`***`); zet het dan in een bestand op de node, zoals `~/.my.cnf`, en pas het commando aan. **Uitzetten** zet de regel in commentaar zonder over te nemen, **Weer aanzetten** haalt dat weg. De regel blijft in het bestand staan als `# uitgezet door ClusterForge: <de regel>`, de rest van het bestand blijft zoals het was, en een uitgezette job geeft geen waarschuwingen of incidenten meer. Een timer of een script in `cron.daily` zet ClusterForge niet uit. Dit vraagt **agentprotocol 20**.

#### Bestandssync

Een cronjob die bestanden van de ene node naar de andere kopieert, kan ClusterForge zelf: kies bij **Cronjob toevoegen** de soort **Bestandssync (rsync over SSH)**. Je kiest de map op de bron, zoals `/var/www/nextcloud/data/`, en of hij naar de andere nodes van hetzelfde cluster gaat of naar alle nodes van een ander cluster, met eventueel een andere map op het doel, patronen die niet mee moeten (zoals `*.part`), een bandbreedte en of wat op de bron weg is ook op het doel weg moet. Anders dan lsyncd of GlusterFS kopieert een sync alleen op het schema dat je kiest, bijvoorbeeld om de vier uur, en alleen wat veranderde.

De bron is de node met het VIP (die verhuist mee) of één vaste node. Binnen een cluster weigert een doel dat zelf het VIP van zijn cluster heeft, zodat de actieve node nooit overschreven wordt: verhuist het VIP, dan wordt de nieuwe eigenaar de bron en de oude een doel. Naar een ander cluster gaat de sync naar elke node van dat cluster, behalve een node met het VIP daar.

ClusterForge regelt de sleutels. Elke bron krijgt een eigen sleutel in `/etc/clusterforge/sync/`, met de hostsleutels van de doelen in een eigen `known_hosts`. Op elk doel zet ClusterForge die sleutel in `/root/.ssh/authorized_keys`, tussen `# clusterforge-sync begin` en `# clusterforge-sync einde`, met `restrict`, alleen vanaf de adressen van de bron, en met als enige toegelaten commando `cf-agent sync-receive`. Dat laat alleen `rsync --server` naar die ene map toe, nooit lezen, en `--delete` alleen als de sync dat mag; de rest van het bestand blijft zoals het was. Een sync draait als root, en het doel heeft `PermitRootLogin prohibit-password` (de standaard van Debian) en rsync nodig.

De pagina van een sync toont de kaart **Klaar voor de sync?**: per node of hij bron of doel is, en of rsync, ssh en sshd er staan, of root met een sleutel mag inloggen, of er een hostsleutel is en hoeveel ruimte er vrij is bij de doelmap. Met **Proefrun** draait de bron `rsync --dry-run` naar één doel en zie je wat er zou veranderen, zonder iets te kopiëren. Bij elke run staat per doel hoeveel bestanden en bytes er meegingen en hoeveel er weg gingen, of waarom het doel weigerde; een run mislukt als één doel mislukte of als elk doel weigerde. Bij **Overnemen** van een gevonden `rsync … root@node:/map/` vult ClusterForge het formulier als sync in. Dit vraagt **agentprotocol 21** op de bron en op de doelen.

#### Documentatie en assistent

De documentatie van elk cluster heeft een hoofdstuk **Cronjobs** met de beheerde jobs en syncs, de jobs die ClusterForge verder op de nodes vond (eerst wat je zelf maakte, dan wat met een pakket meekwam) en runbooks: een run nakijken, een sync terugdraaien (rsync heeft geen prullenbak: zet de sync uit, kopieer terug van een node die de goede versie nog heeft of zet de map terug uit een back-up), een sync weghalen en een uitgezette job weer aanzetten. De assistent leest cronjobs met de tool **cronjobs** en de runs van één job met **cron_runs**; vraag bijvoorbeeld "waarom mislukte de sync naar backup vannacht?".

## Zelfherstel

Bij elke database op de pagina **Databases** staat de kaart **Zelfherstel**: wat er mis is, op welke node, waarom, en wat ClusterForge eraan zou doen. Dat werkt voor elke MariaDB, MySQL en PostgreSQL die de agent ziet, ook als ClusterForge haar niet zelf toepaste, zoals een Galera-cluster dat je met de hand opzette. ClusterForge herstelt met vaste acties in de agent en de server, niet met scripts: eerst vaststellen, dan een plan in gewone taal, en pas met **Nu herstellen**, of vanzelf als je dat per cluster aanzet, een taak met een stap per node, in het logboek en in de tijdlijn van het incident. Een kijker ziet de kaart ook; alleen een beheerder herstelt.

Een voorbeeld. Na een nieuwe indeling van de VLAN's hebben de nodes andere adressen, maar de config van Galera wijst nog naar de oude. Na een herstart blijft MariaDB op db1 aansluiten op die oude adressen; systemd zet de unit op failed terwijl `mariadbd` doorloopt, en db3 draait alleen, zonder meerderheid. De kaart zegt dan "Galera zoekt de andere nodes op verkeerde adressen", "MariaDB op db1 hangt" en "Geen node zit in een Primary component", met één plan: de adressen bijwerken, het hangende proces stoppen en het cluster opnieuw beginnen op de node met de nieuwste data.

Is de database een [onderdeel van ClusterForge](#een-database-op-de-nodes-van-een-cluster), dan herstelt Zelfherstel haar niet zelf. De kaart zegt wat er mis is en linkt naar de database van het cluster, met Opnieuw starten, Opnieuw synchroniseren en Toepassen; zo doen nooit twee systemen hetzelfde.

### Wat Zelfherstel vaststelt

Elke minuut vraagt ClusterForge elke node met een databaseserver wat er speelt: wat systemd over de unit zegt, welke processen van de database echt draaien en bij welke unit ze horen, de vrije ruimte in de datamap, bij Galera de adressen die volgens `my_print_defaults` gelden (met het bestand en de regel waar ze staan) en de adressen waarop de journal van het laatste kwartier "timed out" meldt, en bij Patroni de config en of de etcd-adressen erin antwoorden. Samen met de toestand op de pagina Databases geeft dat deze problemen, rood als **Probleem** of amber als **Let op**:

- **De server staat stil** (`unit_stopped`): de unit van MariaDB, MySQL, PostgreSQL of Patroni is gestopt of failed, en er draait geen proces meer.
- **De server hangt** (`process_hung`): systemd zette de unit op failed of gestopt, maar het proces draait nog en antwoordt niet; of MariaDB start al tien minuten en bereikt geen enkele andere node.
- **Buiten systemd** (`process_outside_unit`): een proces van de database hoort niet bij de unit, zoals een `--wsrep-recover` of een server die met de hand gestart is. ClusterForge laat het met rust en herstelt niets op die node tot het weg is.
- **Start niet mee bij een boot** (`unit_disabled`): de server staat stil of hangt en de unit staat op disabled, dus na een herstart komt de database niet vanzelf op; `systemctl enable` lost dat op.
- **Verkeerde Galera-adressen** (`galera_wrong_address`): `wsrep_cluster_address` noemt een adres dat geen node van het cluster heeft, mist een node, of `wsrep_node_address` is geen adres van de node zelf. Het adres van een andere node met een agent, die (nog) niet bij de database hoort, blijft staan.
- **Geen Primary component** (`galera_no_primary`): geen enkele node van Galera heeft een meerderheid, dus elke app op deze database ligt stil.
- **Niet alle nodes te bekijken** (`no_quorum`): bij Galera zonder Primary component antwoordt een node niet. Die kan de nieuwste data hebben, dus ClusterForge begint het cluster niet; zorg eerst dat de agent daar weer antwoordt.
- **Schijf bijna vol** (`disk_full`): de datamap heeft minder dan 1 GiB of 5 % vrij. Een node die opnieuw aansluit, heeft ruimte nodig voor een volledige kopie. Alleen melden: ClusterForge gooit niets weg.
- **Patroni bereikt etcd niet** (`patroni_dcs`): de agent bereikt geen enkel etcd-adres uit de config van Patroni (rood: zonder etcd weet Patroni niet wie de primary is, en herstarten helpt niet), of niet elk (amber). Met het bestand en de regel van elk adres, en of etcd op de node zelf draait.
- **Patroni geeft een verkeerd adres op** (`patroni_wrong_address`): `restapi.connect_address` of `postgresql.connect_address` is geen adres van de node, dus replica's en apps zoeken hem daar.
- **Niet na te kijken** (`no_inspect`, `patroni_config`): de agent antwoordde niet, is te oud, of vond de config van Patroni niet; de reden staat erbij.

### Nu herstellen

Onder de problemen staat het **Herstelplan**: de stappen in gewone taal, bij nieuwe adressen een tabel met per node het adres nu, wat het wordt en het bestand, en **mag vanzelf** of **alleen met bevestiging** met de reden. Kan het plan nu niet, dan staat er waarom en is er geen knop: een proces buiten systemd, een Patroni die etcd niet bereikt, een node die niet te bekijken is, of Galera met MySQL in plaats van MariaDB. Dat laatste begin je zelf op de node met de hoogste seqno, bij Percona XtraDB Cluster met `systemctl start mysql@bootstrap`; daarna sluit Zelfherstel de andere nodes aan.

**Nu herstellen** toont het plan nog eens; op prod tik je de slug in, en het kan alleen met tweestapsverificatie. ClusterForge voert alleen het plan uit dat je zag: veranderde de toestand intussen, dan weigert het en zie je het nieuwe plan. Het herstel is een taak **Zelfherstel: MariaDB van <cluster>** met een stap per node en de uitvoer van elke stap. Per database loopt hoogstens één herstel, en zoals elke taak die iets verandert niet terwijl er een andere taak op het cluster loopt. De taak doet:

1. **Galera-adressen bijwerken** op elke node waar ze niet kloppen. De agent zet `wsrep_cluster_address` en `wsrep_node_address` in het bestand waar ze nu staan, na een kopie ernaast als `<bestand>.cf-zelfherstel-<tijd>`, en kijkt met `my_print_defaults` of de nieuwe waarde echt geldt; zo niet, dan zet hij de kopie terug. Een MariaDB die draait, leest het bestand pas bij haar volgende start; ClusterForge herstart haar daarvoor niet.
2. **Een hangend proces stoppen**: TERM aan de processen in de cgroup van de unit, na een minuut KILL, en nakijken dat ze weg zijn. Een pid dat intussen niet meer bij de unit hoort, krijgt niets.
3. **Starten**: een losse server, een replica, MySQL, PostgreSQL of Patroni start (`systemctl reset-failed` en `systemctl start`), en de taak wacht tot hij antwoordt, hoogstens vijf minuten. Bij Galera met een node die al Primary is, sluiten de gestopte nodes daar een voor een aan, en de taak wacht telkens tot de node Synced is, ook als hij een volledige kopie haalt.
4. **Het cluster opnieuw beginnen**, bij Galera zonder Primary component: MariaDB stoppen op elke node, zodat Galera zijn positie vastlegt; de seqno lezen uit `grastate.dat`, of na een crash met `galera_recovery`; op de node met de hoogste seqno (bij gelijke stand die als laatste stopte) `safe_to_bootstrap: 1` zetten in `grastate.dat` en het cluster beginnen met `galera_new_cluster`; en de andere nodes een voor een laten aansluiten. Is een positie onbekend of hebben de nodes een andere geschiedenis, dan stopt de taak zonder te beginnen. Vlak voor het beginnen kijkt ClusterForge nog eens of intussen een node Primary werd; dan sluiten de andere daar aan, zodat er nooit twee clusters ontstaan. **Controleren** eist daarna alle nodes Synced in één cluster.

Daarna staat op de kaart wanneer Zelfherstel de database het laatst herstelde, of waarom het mislukte, met een link naar de taak.

### Vanzelf herstellen

Op de kaart zet een beheerder per cluster **Vanzelf herstellen op dit cluster** aan. Standaard staat het uit, en op prod kan het niet aan: daar vraagt elk herstel je bevestiging. Staat het aan, dan voert ClusterForge een plan dat **mag vanzelf** zelf uit, met een rem:

- het probleem bestaat minstens twee minuten, zodat een herstart door systemd eerst klaar is;
- hoogstens drie pogingen per uur per cluster, en niet terwijl er een andere taak op het cluster loopt;
- na een mislukte poging wacht het een kwartier;
- het Galera-cluster opnieuw beginnen gebeurt hoogstens eens per uur; nog een keer kan alleen met Nu herstellen;
- na drie mislukte pogingen op rij gaat het op pauze, tot een beheerder het opnieuw aanzet.

Vanzelf mag alleen wat geen data kost: starten wat stilstaat, een hangend proces stoppen, en het Galera-cluster opnieuw beginnen als elke node te bekijken is en geen node te weinig schijf heeft. Adressen wijzigen gebeurt nooit vanzelf, want een fout adres splitst het cluster. Een database op een losse node en een onderdeel van ClusterForge (met zijn eigen [Vanzelf opnieuw starten](#na-een-herstart)) herstelt Zelfherstel ook niet vanzelf. De kaart zegt bij elk plan waarom het nu wel of niet vanzelf gebeurt. Een vanzelf gestarte taak heet **Zelfherstel (vanzelf): …**, en het logboek zegt "ClusterForge herstelt MariaDB van <cluster> vanzelf" met de problemen als reden.

### Wat Zelfherstel nooit doet

- Een Galera-cluster beginnen als niet elke node antwoordt, op een andere node dan die met de nieuwste data, of als de nodes een andere geschiedenis hebben.
- Een proces stoppen dat niet in de cgroup van de unit zit.
- Een bestand wijzigen zonder kopie, of een ander bestand dan dat met de Galera-adressen. De enige uitzondering is `safe_to_bootstrap: 1` in `/var/lib/mysql/grastate.dat` van de node die het cluster opnieuw begint, zoals `galera_new_cluster` het vraagt. De config van Patroni en etcd verandert het niet: zet een adres daar zelf goed en herstart patroni, één node tegelijk, of kies bij een onderdeel van ClusterForge Toepassen.
- Ruimte vrijmaken bij een volle schijf.
- Apps zoals Nextcloud of keepalived herstellen.
- Vanzelf herstellen op prod.

### Documentatie, What Broke en de assistent

In het logboek staan "Zelfherstel gestart voor MariaDB van <cluster>: db1, db2, db3" (vanzelf: "ClusterForge herstelt MariaDB van <cluster> vanzelf", met de problemen erachter), "Zelfherstel van MariaDB van <cluster> gelukt" of "… mislukt: <de fout>", en "Zelfherstel vanzelf aangezet op cluster <cluster>" of "… gepauzeerd op cluster <cluster>: 3 pogingen op rij mislukten".

In de tijdlijn van [What Broke](#what-broke) staat een herstel als **zelfherstel**, met een link naar de taak. Begon het herstel voor het eerste symptoom, dan kan What Broke het als oorzaak noemen, zoals "Zelfherstel van MariaDB van <cluster> gelukt": een herstart of een opnieuw begonnen Galera-cluster kan iets anders raken. Begon het erna, dan is het een antwoord op de storing; dan telt het niet als verandering en ook niet als oorzaak. Bij het [incident](#incidenten) staan de herstelacties in de tijdlijn bij de veranderingen, ook een mislukte, en ook als ze later komen dan het halfuur dat de analyse leest, tot een minuut na het einde van het incident. Bij een cluster hoort een herstel bij het incident van dat cluster en van de clusters die ervan afhangen; bij een losse node bij het incident van die node.

Het hoofdstuk **Herstel** van de [documentatie](#documentatie) heeft bij elk cluster met een database een sectie **Zelfherstel**: of Vanzelf herstellen aan staat, gepauzeerd is of op prod niet kan, een tabel met wat Zelfherstel de laatste 30 dagen deed, nieuwste eerst (wanneer, de database, wat er mis was, met de hand of vanzelf met de stappen, en of het gelukt is), en wanneer Vanzelf herstellen aan- of uitging of pauzeerde. Wie het vroeg staat erbij, tenzij `CF_DOCS_USERNAMES=false`. Wat Zelfherstel nu ziet, staat niet in de documentatie, want dat verandert elke minuut; dat zie je bij Databases.

De [assistent](#assistent) leest Zelfherstel met de tool **heal**, die alleen leest: per database de problemen met hun reden, het plan met de stappen, of het vanzelf mag en waarom niet, Vanzelf herstellen van het cluster en de laatste poging, en voor één cluster de herstelpogingen van de laatste 30 dagen. Een plan herstellen kan hij niet; hij linkt naar Databases. Bij de analyse van een incident met een database kijkt hij ook naar Zelfherstel. Vraag bijvoorbeeld "Waarom start de database van Nextcloud niet?" of "Wat deed Zelfherstel vannacht?".

### Wat de agent leest en schrijft

Vaststellen leest alleen: `systemctl show` van de units van de databases, `/proc` voor de processen (`mariadbd`, `mysqld`, `postgres`, `patroni`) en hun cgroup, de vrije ruimte in de datamap, `my_print_defaults` en de bestanden die `/etc/mysql/my.cnf` met `!include` en `!includedir` leest, de journal van de unit, en de config van Patroni uit `ExecStart` van patroni.service. Naar elk etcd-adres in die config maakt de agent hoogstens 2 s een TCP-verbinding, zonder iets te sturen. Alleen het bijwerken van de Galera-adressen schrijft iets; starten, stoppen, een proces beëindigen, `safe_to_bootstrap: 1` in `grastate.dat` en `galera_new_cluster` gaan als gewone stappen naar de agent, en staan zoals elk commando in het logboek voordat ze vertrekken.

Zelf kijken op een node:

```sh
systemctl status mariadb                                        # failed, maar staat er nog een Main PID of proces in de CGroup?
my_print_defaults --mysqld server galera mariadb mariadbd | grep wsrep_
journalctl -u mariadb --since -15min | grep 'timed out'
ls /etc/mysql/mariadb.conf.d/*.cf-zelfherstel-*                 # de kopieën die Zelfherstel maakte
```

Dit kan vanaf **agentprotocol 22**, op elke node van de database. Met een oudere agent zegt de kaart "Zelfherstel kan db1 niet bekijken", met de vraag hem bij te werken met `--upgrade` (zie [Agent installeren op een node](#agent-installeren-op-een-node)); bij Galera zonder Primary component kan het cluster dan niet opnieuw beginnen. Bouw ook je golden image opnieuw met `golden-image.sh --replace`.

Via de API: `GET /api/v1/heal` geeft per database de problemen, het plan met zijn `hash`, de laatste herstelpoging van het afgelopen etmaal en Vanzelf herstellen van het cluster, ook voor een kijker. `POST /api/v1/heal/run` met de `key` en `hash` daaruit (op prod ook `confirm` met de slug) geeft de taak; een plan dat intussen veranderde geeft 409 `plan_stale`. `PUT /api/v1/clusters/{id}/heal-auto` met `{"enabled": true}` zet Vanzelf herstellen aan, en geeft 409 op prod. In het logboek: `cluster.heal_started`, `cluster.heal_done`, `cluster.heal_failed` (bij een losse node `node.heal_…`), `cluster.heal_auto_changed` en `cluster.heal_auto_paused`.

## Failovertests

Een failovertest bewijst dat een VIP echt verhuist als de node die het heeft uitvalt. Op de pagina van een cluster staat de kaart Failovertests. Een beheerder voegt er een test toe: het scenario (keepalived stoppen op de eigenaar, nginx of haproxy stoppen op de eigenaar, [de database stoppen](#met-een-database) op de eigenaar, [alle nodes van de database herstarten](#alles-herstarten), of de VM van de eigenaar hard uitzetten via Proxmox), het VIP, binnen hoeveel seconden een andere node moet overnemen (standaard 5 s voor keepalived, 10 s voor een dienst of een VM en 20 s voor een database; bij alles herstarten binnen hoeveel seconden de database weer gezond is, standaard 300 s), of het VIP daarna terug moet naar de oorspronkelijke node (standaard aan bij de templates met keepalived, die preempt gebruiken) en de probe: een HTTP-pad met de verwachte status of een TCP-poort op het VIP. Scenario's die op dit cluster niet kunnen, staan grijs met de reden.

Nu testen toont eerst wat er gaat gebeuren, bijvoorbeeld: "keepalived wordt gestopt op web01, nu eigenaar van 10.0.30.100. Verwacht: een andere node neemt binnen 5 s over. Lukt dat niet, dan is 10.0.30.100 hoogstens 20 s onbereikbaar; daarna zet ClusterForge keepalived weer aan." Daarna doet de server een strenge voorcontrole zonder uitweg: het cluster gezond, geen database op de nodes die ClusterForge niet beheert (een VIP dat naar een replica verhuist, kan schrijfacties laten mislukken; een [onderdeel Database](#met-een-database) mag wel), verse heartbeats, precies één eigenaar per VIP, een agent die deploystappen kan uitvoeren, de dienst draait, een reservenode met keepalived, geen andere taak in het cluster, geen andere test in heel ClusterForge en een probe die drie keer slaagt. Faalt er één, dan start de test niet en staan de controles in het venster.

De test zelf is een taak met vijf stappen. ClusterForge stopt de dienst op de eigenaar (een stop, nooit een disable), vraagt het VIP elke kwart seconde op en wacht tot een verse heartbeat een andere eigenaar meldt en de probe weer drie keer op rij slaagt. Daarna start hij de dienst weer, wacht tot de node gezond is en, als dat verwacht wordt, tot het VIP terug is. De uitslag is bijvoorbeeld "PASS: 10.0.30.100 3,4 s onbereikbaar, overgenomen door web02, daarna terug op web01, alles hersteld" of "FAIL: … meer dan de verwachte 5 s". Het rapport toont de test, de verwachting en het resultaat, een tijdlijn met de probe in groen en rood en markeringen voor de storing, de overname, het herstel en de terugkeer, en de stappen met hun log. Afbreken herstelt meteen.

De dienst komt altijd terug: na de meting, bij een fout, bij Afbreken, bij een nette stop van de server en na een crash, als de taak verder gaat. Lukt het herstel toch niet, dan staat bovenaan het cluster een rode balk "Failovertest niet volledig hersteld" met de knop Opnieuw herstellen. Elke test en run komt in het logboek, met het run-id bij elke regel en het stop- en startcommando aan de agent.

**De VM hard uitzetten** speelt een stroomonderbreking na: ClusterForge zet de VM van de eigenaar via Proxmox uit zonder hem af te sluiten, meet de overname en start de VM daarna weer; de terugkeer mag dan zo lang duren als het opstarten. Dat kan alleen in lab en test, als elke node met keepalived aan zijn VM gekoppeld is. De voorcontrole eist daarnaast dat de VM draait, niet onder Proxmox HA staat (de HA-manager zou hem zelf weer starten of verplaatsen) en dat er geen docker op draait, omdat containers met volumes data kunnen bevatten.

**Op prod** start een failovertest alleen met de hand: keepalived of een dienst stoppen, de database stoppen of [alles herstarten](#alles-herstarten), niet de VM. Net als bij Drift herstellen moet de beheerder tweestapsverificatie aan hebben en de slug van het cluster intikken. Het venster zegt ook wat er gebeurt als ClusterForge midden in de test wegvalt: het VIP blijft bereikbaar op de andere node, alleen is er dan geen reservenode tot ClusterForge terug is en herstelt.

### Met een database

Een cluster met een [onderdeel Database](#een-database-op-de-nodes-van-een-cluster) krijgt ook failovertests. Er is dan een scenario bij: **de database stoppen** op de node met haar VIP, met `systemctl stop mariadb` bij Galera of `systemctl stop patroni` bij PostgreSQL. Het kan alleen op het VIP van een database, en ClusterForge kiest de dienst zelf. Bij Galera neemt een andere node die Synced is het VIP over; bij Patroni wordt de synchrone replica de nieuwe primary en volgt het VIP. Op een eigen VIP van de database is de probe standaard de poort van de database, 3306 of 5432; op het VIP van de app blijft het de probe van de app.

Een test raakt een database als hij haar VIP verplaatst, of als hij de VM van een datanode hard uitzet. Dan komen er drie dingen bij:

- **Voorcontrole.** De database is gezond: alle datanodes doen mee, het VIP staat op een node die kan schrijven, en de scheidsrechter is er. Zo kost een node minder nooit de meerderheid.
- **Schrijftest.** Een andere datanode schrijft tijdens de hele test elke halve seconde een rij via het VIP, met een nieuwe verbinding zoals een app na een storing, in de database `clusterforge_probe` als de gebruiker `cf_probe`. Elke poging mag 2 seconden duren. De agent onthoudt welke rijen de database bevestigde. De test begint pas als hij drie keer na elkaar kon schrijven, en de meting stopt pas als het VIP overgenomen is, de probe slaagt en het schrijven weer lukt.
- **Terugkeer.** Na het herstel wacht ClusterForge tot de database weer helemaal gezond is, hoogstens 10 minuten. Een gestopte node van Galera haalt eerst bij wat hij miste.

Na de test leest de agent via het VIP welke rijen er nog zijn: een bevestigde rij die ontbreekt, ging **verloren**. Dat is altijd FAIL, ook als de test afgebroken werd. Een rij die er wel is maar waarvan de bevestiging nooit aankwam, is geen verlies; het rapport telt die apart. De uitslag is bijvoorbeeld:

- "PASS: schrijven via 10.0.20.185 lukte weer na 9 s, er ging niets verloren; overgenomen door nc2, daarna terug op nc1, alles hersteld"
- "FAIL: schrijven via 10.0.20.185 lukte 31 s niet, meer dan de verwachte 20 s; …"
- "FAIL: 1 bevestigde rij verloren bij schrijven via 10.0.20.185: de database bevestigde ze, maar na de test waren ze weg; …"

Het rapport toont onder de probe een tweede balk voor het schrijven, in groen en rood, en een markering wanneer de database weer gezond was. Bij **Schrijven** staat hoe lang het niet lukte, ook bij de terugkeer, en hoeveel pogingen mislukten; bij **Rijen** of alle bevestigde rijen er nog zijn, of hoeveel er verloren gingen met hun volgnummers.

Een paar regels volgen uit hoe de database het VIP gebruikt:

- Het VIP van PostgreSQL volgt de primary. Keepalived stoppen zou het VIP alleen naar een replica brengen die niet kan schrijven, dus dat scenario kan op dat VIP niet; test het met de database stoppen.
- Een eigen VIP van een database komt niet terug naar de eerste node, want het blijft waar de database schrijft. Daarom kan "terug naar de oorspronkelijke node" daar niet aan.
- De VM van een datanode hard uitzetten, terwijl het VIP van de app elders staat, test het schrijven via het VIP van de database: dat hoort gewoon te blijven lukken.

De database `clusterforge_probe` en de gebruiker `cf_probe` maakt ClusterForge bij de eerste test, op dezelfde manier als bij [Databases en gebruikers](#databases-en-gebruikers): de gebruiker mag alleen in die database, vanaf de nodes en VIP's van het cluster. Elke test krijgt een nieuw wachtwoord, dat ClusterForge niet bewaart; de agent houdt het in een bestand dat alleen root leest, tot de test klaar is. Na de test haalt de agent de rijen weer weg, en ook rijen van meer dan een dag oud die achterbleven. De database staat niet in de lijst bij Databases en gebruikers en komt niet in de nachtelijke dumps; de gebruiker staat er als systeemgebruiker. De agent stopt de schrijftest zelf na hoogstens 30 minuten, ook als ClusterForge wegvalt.

De schrijftest vraagt **agentprotocol 14** op de node die schrijft. Met een oudere agent faalt de voorcontrole en zegt ze welke agent je met `--upgrade` moet bijwerken.

### Alles herstarten

De test **alle nodes van de database herstarten** bewijst dat een database na een stroomonderbreking of een herstart van de Proxmox-host weer gezond wordt. ClusterForge stuurt alle datanodes en de scheidsrechter tegelijk een herstart, zoals **Herstarten** bij een node, vraagt het VIP elke kwart seconde op en wacht tot elke node terug is en de database weer gezond is: bij Galera alle datanodes Synced, bij Patroni een primary met zijn replica's en het VIP erbij. Alles wat op die nodes draait, ook de app, is zo lang weg.

Galera begint na een herstart van alle nodes vaak niet vanzelf (zie [Na een herstart](#na-een-herstart)). De test doet dan wat de bewaking zou doen, met dezelfde regels, maar zonder op haar volgende ronde te wachten: zodra alle nodes terug zijn, MariaDB nergens meer probeert te starten en de herstart anderhalve minuut geleden is, begint ClusterForge het cluster opnieuw op de node met de nieuwste data en start het MariaDB op de andere. Daarom moet **Vanzelf opnieuw starten na een volledige uitval** aan staan; anders start de test niet. Patroni kiest zelf weer een primary; daar doet ClusterForge niets.

- **Verwachting.** Binnen hoeveel seconden de database weer gezond is, gerekend vanaf het versturen van de herstart: standaard 300 s, hoogstens 1800 s. De meting duurt hoogstens vijf minuten langer. "Terug naar de oorspronkelijke node" kan niet; na de herstart staat het VIP waar keepalived het zet.
- **Voorcontrole.** Zoals bij de database stoppen, en daarnaast heeft elke node van de database een verse heartbeat en een agent die commando's kent.
- **Schrijftest.** Een datanode schrijft elke halve seconde een rij via het VIP tot hij zelf herstart; ClusterForge onthoudt welke rijen de database bevestigde. Na de herstart en het herstel krijgt de schrijftest een nieuw wachtwoord, en kijkt de agent via het VIP na of elke bevestigde rij er nog is (stap Rijen nakijken, agentprotocol 15). Een rij die ontbreekt, is FAIL.

De uitslag is bijvoorbeeld:

- "PASS: na het herstarten van nc1, nc2 en nc3 was MariaDB Galera na 2 min 34 s weer gezond (ClusterForge begon het cluster opnieuw op nc2), 10.0.20.185 antwoordde weer na 98 s; alle 23 bevestigde rijen waren er nog; daarna op nc1, alles hersteld"
- "FAIL: MariaDB Galera was pas na 5 min 20 s weer gezond (…), meer dan de verwachte 5 min; …"
- "FAIL: nc3 was 10 min na het herstarten nog niet terug; …"

Het rapport toont bij **Nodes terug** wanneer elke node terug was, bij **Database gezond** na hoeveel tijd, en bij **Opnieuw begonnen** wat ClusterForge deed, of dat het niet nodig was. De tijdlijn heeft een markering voor elke node die terugkomt en voor het opnieuw beginnen.

Is de database aan het eind van de meting nog niet gezond, dan wacht het herstel tot alle nodes terug zijn en begint ClusterForge haar opnieuw, hoogstens drie keer. Lukt dat niet, dan staat de rode balk bovenaan het cluster, met Opnieuw herstellen. Afbreken kan, maar de herstart is dan al gebeurd: ClusterForge wacht dan tot alles terug is en zet de database recht.

**Op prod** kan alles herstarten ook, alleen met de hand, met tweestapsverificatie en de slug: het is wat er na een stroomonderbreking gebeurt, en dat wil je juist van je echte clusters weten. Kies er een rustig moment voor. Valt ClusterForge midden in de test weg, dan komen de nodes toch terug; Galera begint dan pas opnieuw als ClusterForge terug is.

### Planning

Een failovertest kan gepland draaien in het testvenster (`CF_TEST_WINDOW`, standaard zondag van 03:00 tot 05:00 op de klok van de server); zet het vinkje Gepland in het testvenster bij de test. Voor de back-upcontrole staat het vinkje per cluster op de kaart Back-ups, en elke run controleert de node die het langst niet gecontroleerd is. Planning staat standaard uit, en een geplande failovertest kan niet op prod. Gaat een cluster naar prod, dan wordt de volgende geplande run overgeslagen en gaat de planning uit.

Het testvenster volgt de klok, ook bij zomer- en wintertijd: "elke 7 dagen om 04:00" blijft om 04:00 op de klok, op 25 oktober 2026 dus een uur later in UTC. De planner kijkt elke minuut en start nooit twee tests tegelijk: loopt er al een failovertest of back-upcontrole, of een andere taak in het cluster, dan wacht de volgende. De voorcontrole loopt voordat er een taak is; lukt ze niet, dan komt er een overgeslagen run met de reden en de controles, zonder taak, en schuift de test naar het volgende venster. Hetzelfde geldt voor een run die meer dan 30 minuten te laat is (wachten op een andere test telt niet mee) of die buiten het venster zou vallen, bijvoorbeeld omdat ClusterForge niet draaide: dan geen inhaalrun midden op de dag, maar één overgeslagen run en de volgende in het eerstvolgende venster. De kaarten op de clusterpagina tonen "Niet getest sinds" en "Niet gecontroleerd sinds", in oranje na dertig dagen of als het nog nooit gebeurde.

### Rampoefeningen

Een failovertest laat één node uitvallen; een rampoefening laat iets groters wegvallen en kijkt wat de rest doet. Op de pagina van een cluster staat daarvoor de kaart Rampoefeningen. Een beheerder kiest wat er uitvalt:

- **Proxmox-host valt uit**: alle gekoppelde VM's op die host gaan tegelijk hard uit. De host zelf blijft aan. De storing duurt minstens 100 s, omdat een node pas na 90 s zonder heartbeat down is; standaard 2 minuten, met 10 minuten om weer gezond te worden, want de VM's moeten opstarten.
- **Dienst valt weg op alle nodes**: een dienst van de failovertest, of hier ook PostgreSQL of MariaDB, stopt op elke node van het cluster. Standaard 1 minuut, met 2 minuten voor het herstel.
- **VIP valt weg**: keepalived stopt op alle nodes, zodat het VIP geen houder heeft.
- **Reeks van storingen**: tot vijf stappen na elkaar, zie [hieronder](#reeksen-van-storingen).
- **DR-repetitie**: het hele cluster terugzetten uit de back-ups, in sandboxes, zie [hieronder](#dr-repetitie). Dat raakt het echte cluster niet en kan als enige ook op prod.

Nu oefenen toont eerst de voorspelling van de [wat-als](#wat-als): per cluster dat geraakt wordt of er volgens de bekende afhankelijkheden van afhangt, de status nu en de voorspelde status met de reden, en wat de voorspelling onzeker maakt. Daarna de voorcontrole: alleen nodes in lab- en testclusters vallen uit, geen prodcluster hangt ervan af, elk geraakt cluster is gezond met verse heartbeats, in geen van die clusters loopt een taak, er loopt geen andere test, de probes op elk VIP slagen drie keer, en ClusterForge draait zelf niet op wat uitvalt. Bij een host komen daar bij: de host is online, geen VM erop staat onder Proxmox HA en op geen ervan draait docker; staan er VM's op die ClusterForge niet kent terwijl hij niet weet waar hij zelf draait, dan start de oefening niet. Ter bevestiging tik je de slug in van elk cluster dat uitvalt.

De oefening is een taak met vijf stappen: Voorcontrole, Storing en meten, Herstellen, Herstel controleren en Rapport. Ze neemt het clusterslot van elk cluster dat uitvalt, zodat daar intussen niets anders loopt, en het testslot. Tijdens de storing vraagt de server elk VIP elke kwart seconde op en leest hij de status van elk cluster en van zijn bevestigde diensten. Een onderbreking korter dan 10 s telt als overname, niet als uitval. Het rapport zet per cluster voorspeld en gemeten naast elkaar (zoals voorspeld, milder, bleef gezond, erger dan voorspeld of niet hersteld), met hoe lang elk VIP onbereikbaar was, wanneer elk cluster weer gezond was en een tijdlijn per VIP. De uitslag is bijvoorbeeld "PASS: 2 clusters gedroegen zich zoals voorspeld, alles hersteld na 6 s", "WARNING: App bleef gezond, maar zou down worden" of "FAIL: App ging onverwacht down". Verminderd is bij een cluster dat alleen afhangt niet te zien en telt dan als zoals voorspeld.

Alles komt terug, zoals bij de failovertest: na de meting, bij een fout, bij Afbreken, bij een nette stop van de server en na een crash. Lukt het herstel niet, dan staat er een rode balk bij elk gemeten cluster met Opnieuw herstellen, en een bevinding bij Herstelbaarheid in de health score. Een incident tijdens de oefening staat als veroorzaakt door een test. Op prod kan alleen de DR-repetitie; de andere rampoefeningen niet, ook niet met de hand.

Alle runs van failovertests, rampoefeningen en back-upcontroles staan op de pagina Tests, met filters op soort en uitkomst.

#### Reeksen van storingen

Een reeks bootst na wat echt misgaat: geen nette uitval, maar een netwerk dat hapert, een poort die dichtvalt of twee nodes die elkaar niet meer horen. Elke stap heeft een tijdstip (seconden na de start van de storing), wat er gebeurt en op welke nodes: de VIP-houder (de node die bij de start het VIP heeft), de andere nodes, alle nodes of één gekozen node. Wat er kan:

| Stap | Wat de agent doet |
| --- | --- |
| VRRP blokkeren | nftables laat geen VRRP meer door, in en uit. Op alle nodes geeft dat split-brain: elke node neemt het VIP |
| Interface down | `ip link set dev … down` op de gekozen interface |
| TCP-poort dicht | nftables gooit binnenkomend TCP-verkeer op die poort weg, behalve van de node zelf |
| Vertraging of pakketverlies | `tc qdisc … netem` op de interface, tot 2000 ms en 50 % |
| Schijf vullen | een bestand dat de schijf tot 50 à 95 % vult |
| Dienst stoppen | `systemctl stop` van keepalived of een dienst van de failovertest |

Een stap blijft aan tot het einde van de storing; daarna heft ClusterForge alles op en start hij de gestopte diensten. De voorspelling rekent de reeks stap voor stap na: wie het VIP krijgt, of de probe het nog haalt en welke status het cluster dan heeft. De voorcontrole kijkt daarnaast of elke agent storingen kent (protocol 6), of `nft` en `tc` er zijn, en of er nog een storing van een eerdere oefening aanstaat.

De dodemansknop: de agent zet elke storing met een lease van 30 s die de server elke 10 s verlengt. Valt de server weg, crasht hij of verliest de node het contact, dan heft de agent de storing zelf op zodra de lease afloopt. Elke storing heeft daarnaast een harde bovengrens van 10 minuten. De agent bewaart zijn storingen in `faults.json` naast zijn state, heft ze op bij elke start, en `cf-agent-fault-clear.service` doet dat bij het opstarten van de machine al voor het netwerk opkomt, ook als de agent zelf niet start. Met de hand: `sudo cf-agent fault-clear`.

Interface down op de interface waarover de agent met ClusterForge praat, snijdt de node af: dan kan ClusterForge niet verlengen en niet opheffen. Zo'n stap heeft geen lease maar alleen een bovengrens van 2 minuten, telt vanaf de stap tot het einde van de storing, en mag alleen in de laatste 2 minuten van de storing vallen; daarna volgt er op die node geen stap meer. De agent zet de interface na die tijd zelf weer aan, ook na een herstart.

#### DR-repetitie

Een back-upcontrole bewijst dat één VM terugkomt; een DR-repetitie bewijst dat het hele cluster terugkomt, zoals na een echte ramp. ClusterForge zet van elke node de nieuwste back-up tegelijk terug als sandbox, allemaal op één Proxmox-host en alleen aan een afgesloten bridge: daar zien de nodes elkaar, maar niets anders. Daarna controleert het of het cluster werkt en meet het hoe lang dat duurde. Het echte cluster blijft draaien en ClusterForge raakt het niet aan; daarom kan de DR-repetitie ook op prod. Kies op de kaart Rampoefeningen "DR-repetitie", met de bridge (standaard `vmbr99`) en eventueel een planning.

**De afgesloten bridge** maak je één keer, als root op de host waar de repetitie draait. De voorcontrole noemt die host en geeft deze commando's met de goede namen:

```sh
pvesh create /nodes/pve1/network --iface vmbr99 --type bridge --autostart 1 --comments 'ClusterForge DR-repetitie: afgesloten'
pvesh set /nodes/pve1/network
```

Afgesloten betekent: geen poorten en geen adres. Hangt de bridge aan een netwerkkaart of heeft ze een adres, dan start de repetitie niet, en de voorcontrole en het rapport geven het commando dat dat weghaalt. Hangt er een andere VM aan, ook een achtergebleven sandbox van een eerdere repetitie (die heeft dezelfde adressen), dan start ze ook niet. Het netwerk van de host leest ClusterForge met `Sys.Audit`, dat al in de rol staat. Verder zijn de pool `cf-sandbox` van de [back-upcontrole](#back-ups-controleren) nodig en, op Proxmox VE 9, de rol ClusterForgeSandbox van de [diepe controle](#diepe-controle-met-cf-agent-verify).

De voorcontrole eist verder: alle nodes in dezelfde Proxmox-koppeling, elk gekoppeld aan een VM (geen container) met een back-up; een host die alle back-ups kan lezen, het geheugen van alle VM's samen plus 1 GB vrij heeft en een storage die daarna onder 85 % blijft; geen andere test; en geen split-brain in het echte cluster. Liggen de back-ups van de nodes meer dan een uur uit elkaar, dan komt er een waarschuwing, want de gegevens passen dan misschien niet bij elkaar.

De repetitie is een taak met zes stappen:

1. **Voorcontrole**, opnieuw, want er kan intussen een nieuwere back-up zijn.
2. **Terugzetten**: alle back-ups tegelijk, elk onder een nieuw VMID in pool `cf-sandbox`. Hier begint de RTO.
3. **Afsluiten**: elke netwerkkaart komt, nog losgekoppeld, aan de afgesloten bridge met het MAC-adres van de bron, zodat het besturingssysteem zijn eigen netwerkconfig vindt. VLAN-tags en trunks gaan eraf, want de bridge heeft geen poorten. Opstarten bij boot en de bescherming gaan uit, de VM krijgt de tag en het serienummer `cf-sandbox`. ClusterForge leest de config terug, kijkt nog een keer of de bridge afgesloten is en zet dan pas de kaarten aan.
4. **Starten**: alle VM's, en wachten tot elke guest agent antwoordt.
5. **Controleren**: in elke sandbox de [diepe controle](#diepe-controle-met-cf-agent-verify) met cf-agent, die ook zijn adressen meldt. Daaruit volgt wie elk VIP heeft; keepalived krijgt daar anderhalve minuut voor. Precies één houder is goed, twee is split-brain in de sandbox. Vanaf een node zonder VIP vraagt cf-agent dan het VIP op met de probe van de failovertest (HTTP op poort 80, of de TCP-poort) en kijkt hij of elke andere node antwoordt (poort 22; ook een geweigerde verbinding telt als antwoord). Slaagt alles, dan stopt de RTO.
6. **Opruimen**: alle sandboxes hard uit en verwijderd, altijd, ook na een fout of Afbreken.

Zolang de sandboxes kunnen draaien, bewaakt ClusterForge het echte cluster elke 2 seconden. Komt er een tweede agentverbinding met de sleutel van een node, verhuist een echt VIP naar een andere node of krijgt het cluster split-brain, dan zet het alle sandboxes meteen hard uit, breekt het de repetitie af met ERROR en schrijft het dat in het logboek.

**RTO en RPO.** De RTO is de tijd van de start van het terugzetten tot alle controles geslaagd zijn; de RPO is hoe oud de oudste back-up was bij die start. Prod heeft standaard als doel een RTO van 60 minuten en een RPO van 26 uur (een dagelijkse back-up met wat speling); lab en test hebben geen doel. Een admin past de doelen per cluster aan in het formulier van de DR-repetitie; 0 is geen doel. De uitslag is bijvoorbeeld "PASS: Web op vmbr99: het cluster kwam werkend terug. RTO 7 min 12 s (doel 1 u). RPO 6 u (doel 26 u). VIP 10.0.20.100 bij web-01, HTTP antwoordt. 2 sandbox-VM's verwijderd om 04:21." Haalt de repetitie een doel niet, dan wordt het WARNING; komt het cluster niet werkend terug, dan FAIL. Het rapport toont per node de back-up, het sandbox-VMID, de tijden en de adressen, per VIP de houder en de probe, en de stappen met hun log.

**Planning.** Elke week in het testvenster, of in het eerste testvenster van elke maand. Loopt er een andere test, dan wacht de repetitie, zoals bij de [failovertest](#planning). Faalt de voorcontrole van een geplande repetitie, dan komt er een run met ERROR en de commando's in het rapport, zonder taak, en schuift de planning door naar de volgende keer.

**Na de repetitie.** De documentatie van het cluster zegt in het hoofdstuk Herstel waar de getallen vandaan komen, bijvoorbeeld "Gemeten in de sandbox op 3 november 2026, met de DR-repetitie 'DR Web'", met een tabel van RTO en RPO tegen de doelen, per node hoe lang terugzetten en opstarten duurden, en wie het VIP had. Een mislukte repetitie daarna staat eronder, met haar uitslag. Het runbook "Terugzetten uit back-up" noemt dezelfde hersteltijden bij de `qmrestore`-regels. De uitslag telt mee in de kaart Herstelbaarheid van de [health score](#gezondheid) en staat in het [rapport Herstelbaarheid](#rapporten).

De adressen en de probes kent cf-agent vanaf protocol 7. Met een oudere agent in de back-up slaagt de repetitie hoogstens met een waarschuwing, omdat het VIP en de verbindingen niet gecontroleerd zijn; de drie stappen om dat te regelen staan bij de [diepe controle](#diepe-controle-met-cf-agent-verify).

## Afhankelijkheden

Bij Afhankelijkheden zie je welke diensten je draait en welke dienst van welke andere afhangt, over alle clusters heen. Een dienst hoort bij een cluster, bij een losse node zonder cluster, of is extern met een adres en een poort, zoals een NFS-server op 10.0.0.60:2049. Een pijl loopt van de afnemer naar de leverancier: php8.2-fpm in web-prod hangt af van mariadb in db-prod. Bij een harde afhankelijkheid is de afnemer down als de leverancier uitvalt, bij een zachte werkt hij verminderd.

Diensten komen op drie manieren in de graaf:

- **Uit een template.** Een cluster dat je uitrolt met Nginx met keepalived 1.1.0 krijgt nginx en keepalived, met nginx die hard afhangt van keepalived. Bestaande clusters uit die template kregen ze één keer bij de update. De andere templates zetten hun diensten er ook meteen in, zie [De templates](#de-templates).
- **Met de hand.** Een beheerder voegt bij Afhankelijkheden of op de pagina van een cluster diensten, externe diensten en afhankelijkheden toe. Zo komen ook met de hand gebouwde clusters en de verbanden tussen clusters erin.
- **Als voorstel.** Ziet een agent een bekende unit draaien op een actieve node van een cluster (nginx, apache2, haproxy, keepalived, mariadb, mysql, postgresql, redis-server, memcached, rabbitmq-server, php-fpm of docker), dan verschijnt die dienst binnen vijf minuten bij Voorstellen. Bevestig hem of negeer hem; een genegeerde dienst komt niet terug.

Ook afhankelijkheden komen als voorstel, uit de verbindingen die de agents zien (zie [Verbindingen](#verbindingen)).

Elke dienst heeft een eigen status uit de heartbeats: gezond als zijn unit op alle actieve nodes van het cluster draait, verminderd als hij op een deel draait, down als hij nergens draait, en onbekend zonder unit, zonder agent of als extern. Valt een dienst uit, dan krijgen de diensten die er hard van afhangen een rode rand en die er zacht van afhangen een oranje, en kleurt de pijl naar de oorzaak rood. De graaf ververst zich vanzelf; een uitval staat er binnen een halve minuut in.

Klik op een dienst voor zijn instanties per node, wat hij gebruikt en wie hem gebruikt, en de knop Wat raakt uitval?. Die dimt alles wat niet geraakt wordt en toont per cluster welke diensten down of verminderd zouden raken, met prod bovenaan. Met Alleen clusters zie je de clusters met samengevoegde pijlen, en op een smal scherm een lijst per cluster. Het gaat altijd om bekende afhankelijkheden: wat niet in de graaf staat, telt niet mee, en de graaf houdt nooit een actie tegen. ClusterForge voert voor afhankelijkheden niets uit op de nodes.

Redis, memcached, rabbitmq, apache2, php-fpm en PostgreSQL-instanties ziet alleen een agent van deze versie of nieuwer. Zet hem erop met `--upgrade` (zie [Agent installeren](#agent-installeren-op-een-node)); met een oudere agent blijven die diensten onbekend.

### Verbindingen

Een agent met protocol 5 kijkt elke minuut naar de gevestigde TCP-verbindingen in `/proc/net/tcp` en `tcp6`, met de systemd-unit van het proces. Elke vijf minuten stuurt hij een samenvatting: uitgaand per unit, adres en poort, en inkomend per eigen poort. Geen PID's, commandoregels of inhoud. Van een inkomende verbinding blijft de afzender alleen bewaard als het een node is; alle andere adressen worden per poort samengeteld, want bij een publieke webserver zijn dat persoonsgegevens.

Elke vijf minuten zet ClusterForge de verbindingen van de afgelopen 24 uur om naar voorstellen:

- **Naar een bekende dienst.** Maakt de unit van een dienst verbindingen naar het adres van een node of een VIP, en draait daar een dienst op die poort, dan wordt dat een voorgestelde afhankelijkheid. De dienst op die poort is een dienst met die poort, of de dienst van de unit die er op die poort luistert. Het bewijs staat erbij, zoals "php8.2-fpm op web-01 → mariadb in db-prod: 1.240 verbindingen gezien naar 10.0.30.100:3306 in de afgelopen 24 uur". De agent kijkt elke minuut, dus een verbinding die een uur openstaat, telt 60 keer. Een verbinding naar een externe dienst met dat adres en die poort telt ook.
- **Naar een onbekend adres.** Komt een bestemming minstens een uur lang terug, dan wordt ze eerst een voorgestelde externe dienst, zoals 203.0.113.7:443, met een voorgestelde afhankelijkheid ernaartoe. Hoogstens twintig nieuwe per ronde.
- **Nooit.** Verbindingen van cf-agent, naar NATS en de webinterface van ClusterForge zelf, van een unit die geen dienst is (zoals apt), en naar een node of VIP zonder dienst op die poort.

Bij Afhankelijkheden → Voorstellen staan de voorgestelde afhankelijkheden onder de diensten, elk met het bewijs per node en adres. Bevestigen maakt de pijl echt; was de dienst aan een van beide kanten nog een voorstel, dan wordt die ook bevestigd. Negeren laat de pijl als genegeerd staan, zodat hij niet terugkomt, en Terugzetten maakt er weer een voorstel van. Een pijl die al bestaat, met de hand of uit een template, wordt nooit nog eens voorgesteld.

Op de pagina van een node staat voor een beheerder de kaart Verbindingen: uitgaand met waar het heen gaat (een dienst, een node, een VIP, ClusterForge zelf of een onbekend adres) en of er al een afhankelijkheid is, en inkomend per poort met de afzender als het een node is. Op de kaart staat in de laag Diensten bij elke pijl hoe vaak de agents een verbinding zagen. De verbindingen worden na 30 dagen opgeruimd, de tellingen per uur na twee dagen.

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

Heeft de node een database van ClusterForge, dan schakelt ClusterForge eerst over als hij de primary van Patroni is, en wacht het na een herstart of het onderhoud tot de node weer meedoet in de database; zou de database stoppen, dan vraagt het eerst of je toch door wilt. Zie [Beheer](#beheer).

De agent voert alleen vaste soorten commando's uit: facts verzamelen, de toestand lezen voor de driftcontrole, keepalived uit- en aanzetten voor onderhoud, keepalived, nginx, haproxy, MariaDB of Patroni stoppen en starten voor een failovertest, de Galera-adressen in de config van MariaDB bijwerken voor [Zelfherstel](#zelfherstel), na een kopie, herstarten en afsluiten met `systemctl`, en de stappen van een template (pakketten met apt, bestanden, services, gebruikers, mappen en commando's). Na een gewijzigde unit onder `/etc/systemd/system` doet hij `systemctl daemon-reload`. Omdat hij als root bestanden schrijft, kan wie de server beheert alles op de nodes; bescherm de server en `CF_MASTER_KEY` daarom als een beheerwachtwoord. Hij onthoudt het onderhoud en het laatste herstartcommando in `/var/lib/clusterforge/agent-state.json`, zodat hij na een herstart niet nog eens herstart. Een agent van voor deze versie kan geen commando's uitvoeren; trek hem in en installeer hem opnieuw.

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
- een app in een Kubernetes-cluster die verminderd of down gaat (zie [Status, incidenten en metrics](#status-incidenten-en-metrics));
- een geplande failovertest of back-upcontrole die mislukt of een fout geeft.

Wat bij elkaar hoort, komt in hetzelfde incident: een node bij het incident van zijn cluster, een dienst, app of mislukte test bij dat van zijn cluster zolang het cluster nog misgaat, een cluster bij het incident van een app erin, en een andere overgang die binnen tien minuten na het begin valt en verwant is (hetzelfde cluster, een cluster dat via de afhankelijkheden van het andere afhangt, of een node op dezelfde Proxmox-host). Een incident sluit als alles vijf minuten gezond is, en gaat weer open als dezelfde storing binnen een kwartier terugkomt. Een node in onderhoud geeft geen incident.

De ernst volgt uit de omgeving: op prod is down of split-brain kritiek en verminderd hoog, al het andere is laag. Een incident wordt alleen erger, nooit minder erg. Wat gebeurt terwijl een failovertest van ClusterForge in hetzelfde cluster loopt (of tot twee minuten erna), krijgt de markering 'door een failovertest' en is standaard verborgen.

Bij Incidenten staan de open incidenten bovenaan, daarna de opgeloste, met filters op periode en status en de waarschijnlijke oorzaak in één regel. Een incident toont wat er misging (zie What Broke hieronder), de analyse van de [assistent](#analyseren-uitleggen-en-samenvatten) als die er is, wat er geraakt was (met de eerste, ergste en laatste status en van wanneer tot wanneer), de grafieken rond het eerste symptoom, de tijdlijn en een notitie. Het overzicht heeft een kaart Open incidenten. Iedereen ziet incidenten; alleen een beheerder schrijft de notitie, en een viewer ziet in de tijdlijn geen IP-adres. Openen, erger worden, sluiten, heropenen en de notitie staan ook in het logboek, onder de soort Incidenten.

Een VIP-wissel en de statuswissel van een dienst staan nu ook bij de nodes zelf in het logboek, niet alleen bij het cluster. Een herstel door [Zelfherstel](#zelfherstel) staat in de tijdlijn van het incident van zijn cluster, ook als het later komt dan het halfuur dat What Broke leest.

## What Broke

Bij elk incident zet ClusterForge op een rij wat er eerst misging, wat er vlak ervoor veranderde en wat daarvan de waarschijnlijke oorzaak is. Dat zijn vaste regels zonder AI: dezelfde gegevens geven altijd hetzelfde rapport, met de redenen erbij.

**De tijdlijn** voegt het logboek, het journaal van de nodes en de waarschuwingen van Kubernetes samen, met de afwijkingen in de metrics erbij. Elke regel is een verandering (die oorzaak kan zijn), een symptoom (wat er misging) of informatie. Een moment dat ClusterForge pas later ziet, krijgt een venster 'waarschijnlijk tussen': een node gaat pas na 90 s zonder heartbeat op down, Proxmox wordt elke 20 s gelezen, en drift elk kwartier.

**De analyse:**

1. **Eerste symptoom.** Het vroegste symptoom in wat geraakt was, rond het begin van het incident: een dienst die faalde in het journaal, een node, cluster of app dat slechter werd, een VIP zonder eigenaar, een waarschuwing van Kubernetes.
2. **Kandidaten.** Elke verandering van twee uur voor het eerste symptoom tot een minuut erna, plus een OOM-kill en een afwijking in de metrics van het kwartier ervoor.
3. **Score.** Elke kandidaat krijgt een score uit:
   - de plaats: de node die misging telt het zwaarst, dan hetzelfde cluster, een cluster waar het van afhangt en dezelfde Proxmox-host, en elders het minst;
   - de tijd: elke tien minuten ervoor telt half zo zwaar;
   - de soort: een Git-wijziging, uitrol (ook van een image in Kubernetes), pakketupdate of VM-actie het zwaarst, dan een taak, een gewijzigd bestand, een herstart en een SSH-login.
4. **Verbanden.** Vaste verbanden geven een bonus en een zin in het rapport:
   - het bijgewerkte pakket hoort bij de dienst die faalt (nginx bij nginx.service, mariadb-server bij mysql);
   - het gewijzigde bestand hoort bij die dienst (/etc/nginx/ bij nginx);
   - de OOM-killer trof het proces van de dienst;
   - de VM stopte in Proxmox voordat de node down ging;
   - de Proxmox-host ging offline voordat de VM's erop down gingen;
   - de schijf liep vol voordat de dienst faalde;
   - de VIP-eigenaar herstartte vlak voor het VIP geen eigenaar meer had;
   - de wijziging mislukte;
   - de app in Kubernetes ging mis na de uitrol van een nieuwe image.
5. **Groeperen.** Regels van dezelfde taak zijn één oorzaak, met de Git-commit als die er is. Pakketten of bestanden op dezelfde node binnen twee minuten zijn één update.
6. **Uitkomst.** Hoogstens vijf oorzaken, elk waarschijnlijk, mogelijk of zwak verband, met de redenen. Veranderde er niets, dan zegt het rapport dat: 'ClusterForge zag in de twee uur ervoor geen verandering. De afwijkingen in de metrics staan hieronder; de oorzaak ligt waarschijnlijk buiten de nodes.'

Bijvoorbeeld: 'Waarschijnlijke oorzaak: nginx bijgewerkt van 1.26.0 naar 1.26.3 op web-01, 40 s voor het eerste symptoom', met als reden 'nginx op web-01 faalde 40 s na deze update van nginx'.

De afwijkingen in de metrics vergelijken het kwartier voor het eerste symptoom met de twee uur daarvoor, met vaste drempels: CPU boven 90 % en 30 punten hoger dan gemiddeld, geheugen boven 95 %, een schijf die boven 95 % komt, en netwerkfouten. Zonder VictoriaMetrics staat in het rapport dat de metrics niet te lezen waren.

De analyse rekent opnieuw zolang het incident open is en tot een uur na het einde, bijvoorbeeld als een journaal na een herverbinding binnenkomt. Daarna ligt ze vast. Ze leest alleen, neemt nooit het clusterslot en stuurt geen commando's.

Een herstel door [Zelfherstel](#zelfherstel) staat in de tijdlijn als **zelfherstel**, met de taak. Begon het voor het eerste symptoom, dan kan het een oorzaak zijn; begon het erna, dan is het een antwoord op de storing en geen oorzaak.

**Wat is er veranderd.** Een cluster heeft een kaart met de veranderingen van de laatste 24 uur. De volledige tijdlijn van een cluster of node, met filters op periode en klasse, staat bij Tijdlijn; de kaart Wijzigingen van een node linkt ernaar. Een viewer ziet de tijdlijn ook, maar nooit iets uit de beveiliging en bij een SSH-login geen IP-adres.

## Gezondheid

De status zegt hoe het nu gaat. De health score zegt hoe kwetsbaar een cluster is: een cluster kan gezond zijn terwijl beide nodes op dezelfde Proxmox-host draaien, de laatste back-up twee dagen oud is en de failovertest al maanden niet liep. Elk cluster krijgt een score van 0 tot 100 en een cijfer: A vanaf 90, B vanaf 80, C vanaf 70, D vanaf 60, E vanaf 50, anders F. Het zijn vaste regels zonder AI, met de reden bij elke bevinding.

**Waar.** Het overzicht toont de score van het geheel met de trend over 30 dagen, het cijfer per cluster en de drie bevindingen die het meeste opleveren; de rest klapt open. De clusterlijst heeft een cijferbadge naast de status. Een cluster heeft een kaart Gezondheid met de zeven scorekaarten; een Kubernetes-cluster heeft er de kaart Kubernetes bij, en een cluster met een databaseserver op zijn nodes de kaart Database. Elke bevinding heeft een link Oplossen naar het scherm waar je het doet, en wat de score stijgt als je het oplost.

**De kaarten.** Elke kaart begint op 100 en verliest punten per bevinding: laag 5, middel 10, hoog 20, kritiek 40, nooit onder 0. Op prod is een bevinding vaak een stap ernstiger, op lab een stap milder.

| Kaart | Weegt | Wat telt |
| --- | --- | --- |
| Beschikbaarheid | 25 % | nu down, split-brain of verminderd; de beschikbaarheid over 30 dagen (onder 99,9 %); veel incidenten; een VIP dat vaak van eigenaar wisselt (tests niet meegerekend) |
| Back-ups | 20 % | een node zonder back-up of met een te oude, volgens het back-upbeleid; een mislukte verificatie in Proxmox Backup Server; een back-upcontrole in de sandbox die mislukte, waarschuwde, nooit gebeurde of langer dan 30 dagen geleden is |
| Herstelbaarheid | 15 % | een VIP zonder failovertest; een test die nooit liep, mislukte, zijn doel niet haalde of langer dan 30 dagen geleden is; een test die niet hersteld is; een DR-repetitie die nooit liep, mislukte, haar doel niet haalde of langer dan 45 dagen geleden is |
| Redundantie | 15 % | alle nodes op dezelfde Proxmox-host, of de nodes van een VIP; maar één node (niet op lab); een node die langer dan een dag in onderhoud staat; een prod-dienst die hard afhangt van een dienst op lab of test; een [single point of failure](#wat-als): een host, storage, bridge of node waarvan de uitval alleen al het prod-cluster of een bevestigde prod-dienst down legt |
| Configuratie | 10 % | geen gewenste staat; drift, zwaarder naarmate ze langer duurt; een regel die alle drift negeert; een revisie die al een uur niet toegepast is; een mislukte Git-wijziging |
| Onderhoud en beveiliging | 10 % | open beveiligingsupdates, zwaarder na 3 en 14 dagen; pakketlijsten die 14 dagen niet ververst zijn; een herstart nodig; een besturingssysteem dat end-of-life is of binnen 90 dagen wordt; een certificaat op de node dat binnen 30 dagen verloopt; een verouderde agent |
| Capaciteit | 5 % | een schijf boven 85 % of 95 %; een schijf die volgens de trend van 7 dagen binnen 14 dagen vol is; CPU of geheugen met een p95 boven 90 % over 7 dagen; veel swap; meer dan 1000 netwerkfouten in 7 dagen |
| Kubernetes | 15, alleen bij een Kubernetes-cluster | een node die NotReady is; één server op prod; geen etcd-snapshot of een die ouder is dan een dag; een k3s-versie zonder updates of die er binnen 90 dagen geen meer krijgt; een app die blijft herstarten; apps op prod met één pod, apps met een image zonder vaste versie (`:latest` of zonder tag) en apps met pods zonder geheugenlimiet, elk samen als één lage bevinding. Zie [Score en documentatie](#score-en-documentatie) |
| Database | 15, alleen bij een cluster met een databaseserver op zijn nodes | een database op één node, of op twee zonder scheidsrechter; een node die niet meedoet of een replica die achterloopt; twee databasenodes op dezelfde Proxmox-host (zwaarder als die host de meerderheid kost); minder dan 15 % vrij geheugen op een databasenode (p95 over 7 dagen); nachtelijke dumps die uit staan, of een database zonder goede dump van de laatste dag; Galera-tabellen zonder primaire sleutel of buiten InnoDB, volgens de laatste dump; een database met een VIP zonder geslaagde failovertest in 90 dagen; een database die meer dan één cluster gebruikt; een verhuisde database die na 30 dagen nog niet afgebouwd is |

De score is het gewogen gemiddelde van de kaarten die iets weten; bij een Kubernetes-cluster weegt de kaart Kubernetes 15 naast de 100 van de andere, net als de kaart Database bij een cluster met een databaseserver, en toont elke kaart haar deel daarvan (Beschikbaarheid dan 22 %, met beide kaarten 19 %). Een kaart zonder gegevens is onbekend en telt niet mee; de kaart zegt waarom, bijvoorbeeld 'Zonder VIP is er geen failovertest'. De trend, CPU en geheugen komen uit VictoriaMetrics; zonder VictoriaMetrics telt bij Capaciteit alleen de schijfbezetting.

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

**Databases.** Heeft een cluster een [database op zijn nodes](#een-database-op-de-nodes-van-een-cluster), dan kent de wat-als de meerderheid. Galera, en etcd onder Patroni, werken alleen met meer dan de helft van hun leden; de scheidsrechter telt mee. Vallen nc2 en nc3 tegelijk weg, dan draait MariaDB op nc1 nog, maar zegt de wat-als: "MariaDB Galera: zonder nc2 en nc3 is er geen meerderheid (1 van de 3 leden), dus MariaDB stopt". Dan zijn het cluster, de dienst `mariadb` en wat er hard van afhangt, zoals Nextcloud, down. Bij Patroni stopt PostgreSQL met schrijven. Valt één node weg, dan is het cluster verminderd, met "nog 2 van de 3 leden, genoeg voor een meerderheid".

- **Galera** telt de leden die nu meedoen. Is MariaDB op nc3 al gestopt, dan gaat Galera met nc1 en nc2 verder, en stopt het als daarna nog een van die twee wegvalt.
- **etcd** telt altijd al zijn leden, ook die nu weg zijn.
- Is de database nu al down, dan blijft ze dat in de voorspelling.
- Herstart HA de nodes, dan rekent de wat-als met de nodes die terug zijn: Galera vindt zijn cluster terug zodra de leden elkaar weer zien, en Patroni kiest weer een primary.

Zo wordt een Proxmox-host met twee van de drie nodes van een database ook een SPOF.

**SPOF's.** Een single point of failure is een host, storage, bridge of node waarvan de uitval alleen al een prod-cluster of een bevestigde prod-dienst down legt, ook nadat HA herstart wat het kan. ClusterForge rekent elk onderdeel apart, alsof de rest gezond is, en zet de onderdelen die het meeste down leggen bovenaan. Elk staat ook als bevinding Hoog op de kaart Redundantie van het cluster, met een link naar de wat-als; staan de nodes al allemaal op dezelfde host, dan komt er niet nog eens een bevinding voor die host bij.

**Configuratie van de VM's.** Daarvoor leest ClusterForge elke 10 minuten de configuratie van de VM's en containers: de schijven met hun storage, de netwerkkaarten met hun bridge en VLAN, en de vaste IP-adressen. Daar zijn geen extra Proxmox-rechten voor nodig (`VM.Audit`). Templates en de sandbox-pool slaat hij over.

**Waar ClusterForge zelf draait.** ClusterForge zoekt zijn eigen VM of container aan zijn MAC-adres en, als dat niet uniek is, aan zijn IP-adres. In Docker ziet hij zijn eigen MAC niet; dan zoekt hij aan het adres uit `CF_NATS_ADVERTISE` en het adres bij de Git-koppeling, tegen de vaste IP's van de VM's en de adressen van draaiende containers. Vindt hij het niet, kies de VM dan bij Instellingen onder "Waar ClusterForge draait".

## Kaart

Bij Kaart zie je hoe alles aan elkaar hangt, in drie lagen:

- **Fysiek.** Per Proxmox-host de VM's en containers met hun status, en de lokale storage. Gedeelde storage staat onder de hosts, met een lijn van elke VM naar de storage van zijn schijven. Een VM die een node is, krijgt de kleur van zijn cluster; de VM van ClusterForge zelf heeft het label CF.
- **Netwerk.** De subnetten met hun VLAN en de nodes erin, de bridges van de hosts met hun poorten (zoals `bond0 (eno1, eno2)`), de gateways, de VIP's met een lijn naar hun huidige eigenaar, en een stippellijn tussen VRRP-peers. Subnetten komen uit de adressen die de agents melden; een bridge hoort bij een subnet als de host er een adres in heeft, of als een node met dat netwerk via zijn netwerkkaart aan die bridge hangt.
- **Diensten.** De graaf van [Afhankelijkheden](#afhankelijkheden), met de voorstellen als stippellijn en bij elke pijl hoe vaak de agents de afgelopen 24 uur een verbinding zagen (zie [Verbindingen](#verbindingen)).

Filter op omgeving of cluster; dan blijft alleen staan wat bij die nodes hoort. Een klik op een element opent een paneel met de details, waar het mee verbonden is, een link naar zijn pagina en "Wat als dit uitvalt?". Op een smal scherm wordt de kaart een lijst.

Vanuit de wat-als (Op de kaart tonen) en vanuit een incident (Wat het raakte, op de kaart) opent de kaart met een overlay: wat uitvalt, down of verminderd is of door HA herstart wordt, krijgt een rand in die kleur, en de rest wordt gedimd. Op de kaart staan alleen nodes die aan een VM gekoppeld zijn (fysiek) of die een agent hebben (netwerk); wat ontbreekt, staat erboven.

## Documentatie

Bij Documentatie staat wat ClusterForge zelf over de clusters schrijft, uit wat het al weet: de specificatie, de inventory, de facts van de agents, Proxmox, de afhankelijkheden, de back-ups, de tests en de wat-als. Er is een overzicht met alle clusters, de afhankelijkheden tussen clusters als diagram, de Proxmox-hosts met hun storage en bridges, de single points of failure en de open bevindingen. Elk cluster heeft zes hoofdstukken; een Kubernetes-cluster heeft er Kubernetes bij, en een cluster met een databaseserver op zijn nodes Database:

- **Architectuur.** Wat het cluster doet, de nodes met hun VM en host, de VIP's met de volgorde waarin keepalived ze geeft, en de diensten met hun afhankelijkheden als diagram.
- **Kubernetes.** Alleen bij een Kubernetes-cluster: de k3s-versie en tot wanneer ze updates krijgt, de nodes met hun rol, de namespaces, de apps met hun images en aantal pods, de webadressen, de opslag en de etcd-snapshots, met de runbooks voor een node vervangen, bijwerken en etcd terugzetten. Zie [Score en documentatie](#score-en-documentatie).
- **Database.** Alleen bij een cluster met een databaseserver op zijn nodes: per database de engine, versie, nodes, het adres voor de app, de dumps en de databases met grootte en gebruikers, met de runbooks voor opnieuw starten, een node opnieuw synchroniseren, een dump terugzetten, overschakelen en verhuizen. Zie [In de documentatie](#in-de-documentatie).
- **Netwerk.** Subnet, gateway, DNS, bridge en VLAN, een diagram van de nodes per host met de VIP's, de adressen per interface met MAC-adres, bridge en VLAN uit Proxmox, en de poorten.
- **Configuratie.** De template met zijn parameters, de bestanden die ClusterForge op elke node zet, `cluster.yaml` zoals het in Git zou staan, en de revisies met wat er veranderde.
- **Cronjobs.** De beheerde jobs en syncs, de jobs die ClusterForge verder op de nodes vond, en runbooks om een run na te kijken en een sync terug te draaien (zie [Cronjobs](#documentatie-en-assistent)).
- **Herstel.** De back-ups per node met hun versheid, de back-upcontroles met de hersteltijd, de failovertests met wat ze maten, de DR-repetitie met de gemeten RTO en RPO tegen de doelen, bij een cluster met een database wat [Zelfherstel](#zelfherstel) de laatste 30 dagen deed, en per node en per host wat er uitvalt volgens de wat-als.
- **Runbooks.** Onderhoud, herstarten en afsluiten zoals ClusterForge het doet, het VIP met de hand verplaatsen, een VM of het hele cluster terugzetten uit de nieuwste back-ups met de echte `qmrestore`-regels en de hersteltijden uit de DR-repetitie, een node vervangen en wat te doen als een host uitvalt.

Tien minuten na een wijziging die een cluster raakt, zoals een uitrol, een gewijzigde specificatie of een node in onderhoud, schrijft ClusterForge de documenten van dat cluster opnieuw; een reeks wijzigingen geeft zo één nieuwe versie. Elke nacht om 06:00 schrijft het alles opnieuw, bijvoorbeeld voor nieuwe back-ups, en met Nu bijwerken hoeft een beheerder niet te wachten. Een document krijgt alleen een nieuwe versie als de tekst veranderde; de laatste 50 versies blijven bewaard. Bij Geschiedenis zie je wanneer en waarom elke versie kwam, open je een oudere en zie je wat er veranderde.

Een beheerder kan bovenaan het hoofdstuk Architectuur een samenvatting 'In het kort' laten schrijven door de [assistent](#analyseren-uitleggen-en-samenvatten).

Downloaden geeft een zip met Markdown: `README.md` en `clusters/<slug>/<hoofdstuk>.md`, met links tussen de bestanden, zodat het in een Git-repository of wiki goed staat. De diagrammen zijn Mermaid; GitHub tekent ze. Afdrukken drukt het open document af zonder menu en knoppen. Ook een viewer leest en downloadt de documentatie.

Geheimen staan er nooit in: een geheime parameter staat er als `[geheim]`, en ook in de bestanden die ClusterForge op een node zet, staat elk geheim als `[geheim]`. Wat er echt op een node staat, en een hash daarvan, komt er niet in; dat vergelijkt de driftcontrole. Met `CF_DOCS_USERNAMES=false` staat bij de revisies niet wie ze maakte.

### Naar GitHub

ClusterForge kan de documentatie ook in een eigen GitHub-repository zetten, met dezelfde bestanden als in de zip. Zo begin je:

1. Maak op GitHub een private repository, bijvoorbeeld `clusterforge-docs`, en vink "Add a README file" aan: in een lege repository kan ClusterForge niet schrijven.
2. Maak een fine-grained token (Settings → Developer settings → Fine-grained tokens) met alleen die repository en alleen Contents: Read and write.
3. Vul bij Instellingen → Documentatie naar GitHub de repository, de branch (standaard `main`), de map (standaard `docs`, leeg is de hoofdmap) en het token in. Testen leest de branch en maakt een losse blob die bij geen enkele commit hoort; zo blijkt zonder iets te veranderen of het token mag schrijven. Het token wordt versleuteld met `CF_MASTER_KEY` bewaard en komt nooit terug uit de API; zonder masterkey staat de export uit.

Na het opslaan publiceert ClusterForge meteen, en daarna telkens als een document een nieuwe versie krijgt: tien minuten na een wijziging, en elke nacht. Elke ronde is één commit met alleen de bestanden die veranderden, met als auteur ClusterForge en als bericht bijvoorbeeld "Documentatie bijgewerkt: overzicht, web-prod" met elk bestand eronder. Is er niets veranderd, dan komt er geen commit. De branch wordt bijgewerkt zonder force; pushte iemand intussen, dan bouwt ClusterForge de commit opnieuw op de nieuwe kop. Bestaat de branch nog niet, dan maakt ClusterForge hem met alleen de documentatie.

ClusterForge schrijft alleen in die branch en die map. Een bestand dat je zelf in de map zet, blijft staan; de documenten van een cluster dat verwijderd is, gaan weg. De repository en branch die GitOps leest, worden geweigerd (een andere branch in dezelfde repository mag), zodat een commit van de documentatie nooit een GitOps-plan start en het GitOps-token alleen-lezen blijft. Mislukt een ronde, bijvoorbeeld omdat het token verlopen is, dan staat de fout bij de koppeling en in het logboek, en probeert ClusterForge het om de vijf minuten opnieuw. Met Pauzeren publiceert het alleen nog met Nu publiceren; Verwijderen laat staan wat al in de repository staat.

## Rapporten

Bij Rapporten staat het rapport Herstelbaarheid, per maand. Het zet per cluster bij elkaar wat er over herstel bekend is, prod eerst:

- een samenvatting van alle clusters: hoeveel back-ups vers zijn, hoeveel back-upcontroles, failovertests, rampoefeningen en DR-repetities er die maand waren en hoeveel daarvan slaagden, en RTO en RPO tegen de doelen;
- per cluster de RTO en RPO van de laatste DR-repetitie tot het einde van de maand die het cluster werkend terugkreeg, met datum en doel;
- de nieuwste back-up van elke VM met haar stand en de laatste back-upcontrole met de hersteltijd;
- alle tests van de maand met hun uitslag en wat ze maten;
- de open bevindingen op de kaarten Back-ups, Herstelbaarheid en Redundantie van de [health score](#gezondheid); een geaccepteerde bevinding staat er niet in.

Afdrukken drukt het rapport af zonder menu en knoppen, met elk cluster op een eigen pagina; kies in de browser "Opslaan als PDF" voor een PDF. Ook in donkere modus is de afdruk licht.

Elke eerste van de maand bewaart de planner het rapport van de maand ervoor. Stond de server toen uit, dan doet hij het in de eerste zeven dagen alsnog; een maand waarin nog geen cluster bestond, slaat hij over. Een bewaard rapport verandert niet meer en zegt dus hoe de back-ups, de doelen en de risico's er toen voor stonden; in de keuzelijst staat het als "(bewaard)". Voor de lopende maand en voor een oudere maand zonder bewaard rapport rekent ClusterForge het rapport bij het openen: de tests komen dan uit de geschiedenis, maar de back-ups, de doelen en de risico's zijn die van nu, en dat staat erboven. Ook een viewer leest de rapporten; via de API is het `GET /api/v1/reports/recovery?month=2026-11`.

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

**Wat de assistent kan.** Alleen lezen. Hij heeft zevenentwintig tools: het overzicht van de fleet, een cluster, een node, de tijdlijn, de wijzigingen, incidenten zoeken, een incident met de analyse van What Broke, metrics, drift, back-ups, failovertests, taken, Git-wijzigingen, de impact van een storing, de health score, de wat-als met de single points of failure, de kaart, ook met de verbindingen tussen diensten, de documentatie, zonder de inhoud van bestanden en `cluster.yaml`, drie voor [Kubernetes](#assistent-bij-kubernetes): een cluster live, een object en de log van een pod, twee voor [databases](#assistent-bij-databases): de toestand van de databases van een cluster en wie welke database gebruikt, twee voor [cronjobs](#documentatie-en-assistent): de jobs van een cluster en de runs van één job, en **heal** voor [Zelfherstel](#zelfherstel): wat er mis is met de databases, het herstelplan en de herstelpogingen. Hij kan niets starten, stoppen, herstarten, toepassen of wijzigen; een tool roept dezelfde leesopdrachten aan als de webinterface, met de rechten van wie de vraag stelt. Raadt hij iets aan, dan linkt hij naar het scherm waar jij het doet.

**Wat naar Anthropic gaat.** Je vraag, de gebruikersnaam en rol, de tijd, en wat de tools lezen: namen, statussen, adressen, tijdlijnen, journaalregels, pakketnamen, metrics, Git-commits en taken van de fleet, bij Kubernetes objecten met hun YAML en de log van pods, en bij databases de toestand per node, de namen en groottes van de databases, de namen van de gebruikers met hun rechten, en wie ermee verbindt, en bij Zelfherstel de problemen met hun reden, de plannen met de adressen en bestanden uit de config, en de herstelpogingen met hun fout. Geheimen gaan nooit mee: geen wachtwoorden of hashes, geen rijen uit een database, geen tokens, sleutels, vingerafdrukken, sessies, geen waarden van Secrets en geen inhoud van bestanden; in logs en YAML staat `[geheim]` op de plek van wat op een wachtwoord of token lijkt. Commitberichten, journaalregels en logs zijn tekst van buiten ClusterForge; de assistent behandelt ze als gegevens en volgt geen instructies die erin staan.

### Analyseren, uitleggen en samenvatten

Naast vragen beantwoorden doet de assistent drie vaste opdrachten. Elk antwoord komt live binnen en is ook een gesprek bij Assistent, met het label Analyse, Uitleg of Samenvatting. Het telt mee voor het budget en de grenzen hierboven, en wie het vroeg, kan erin doorvragen.

- **Analyseer, op een incident.** De assistent leest het incident, de analyse van What Broke, de tijdlijn en de geschiedenis, kijkt met de tools verder waar dat helpt (metrics, wijzigingen op de nodes, impact, eerdere incidenten, en bij een cluster met een database [de database](#assistent-bij-databases) en [Zelfherstel](#zelfherstel)) en schrijft vier delen: In het kort, Waarschijnlijke oorzaak met het bewijs en hoe zeker dat is, Nog te controleren, en Wat je kunt doen met links naar de schermen. Het resultaat staat daarna bij het incident, met het model, de datum, wie het vroeg en de kosten, en in het logboek. Opnieuw analyseren vervangt het; weigert het model of loopt er iets mis, dan blijft de vorige analyse staan. De analyse leest met de rechten van wie ze vraagt: die van een beheerder ziet een viewer niet, en een viewer kan ze ook niet vervangen.
- **Leg uit, bij een bevinding of een SPOF.** Bij elke bevinding van de [health score](#gezondheid) en bij elke single point of failure op Risico legt de assistent in een venster uit wat het betekent, waarom het in jouw fleet telt en hoe je het oplost. Een viewer krijgt de uitleg zonder wat hij in de webinterface niet ziet. Bij Kubernetes staat Leg uit bij elk object en elke waarschuwing, en Ik zit vast bij de lessen; zie [Assistent bij Kubernetes](#assistent-bij-kubernetes).
- **In het kort, in de documentatie.** Een beheerder laat in het hoofdstuk Architectuur van een cluster een samenvatting van een paar alinea's schrijven uit de hoofdstukken. Ze staat bovenaan het hoofdstuk met het model en de datum, gaat mee in de zip en naar GitHub, en blijft staan tot je een nieuwe vraagt of haar weghaalt. Links, afbeeldingen, HTML en tabellen haalt ClusterForge eruit; bij twijfel gelden de hoofdstukken zelf.

**Automatisch analyseren.** Standaard uit. Zet een beheerder bij Instellingen, Assistent 'Nieuwe prod-incidenten met ernst hoog of kritiek automatisch analyseren' aan, dan analyseert ClusterForge zo'n incident twee minuten nadat het ontdekt is, zodat de tijdlijn en de metrics van het begin er zijn. Alleen incidenten die een prod-cluster raken en niet door een test komen, elk één keer, hoogstens tien per dag en binnen het maandbudget. Zo'n analyse leest met de rechten van een beheerder, staat bij het incident als automatisch, en het gesprek staat bij de gesprekken van iedereen zonder gebruiker.

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
internal/assistant/        de AI-assistent: instellingen, gesprekken, de lus met de Anthropic API, kosten en budget, analyses van incidenten, uitleg en samenvattingen
internal/risk/             de health score: de zeven scorekaarten, Kubernetes, Database, de kaart Platform, acceptaties en de trend; de wat-als en de SPOF's
internal/docs/             de documentatie: Markdown en Mermaid uit wat ClusterForge weet, de versies, bijwerken na een wijziging, de zip en de export naar GitHub
internal/reports/          het rapport Herstelbaarheid per maand en de maandelijkse momentopname
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
internal/gitops/           GitOps: de repository lezen, clusterbestanden controleren, plannen en koppelen; ghfake/ is een nep-GitHub die ook schrijft
internal/deps/             diensten en afhankelijkheden: graaf, status, doorgeven, impact, voorstellen en verbindingen
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
