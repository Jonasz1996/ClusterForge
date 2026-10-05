# ClusterForge – Technisch ontwerp fase 2

Stand: 2026-10-05

## Samenvatting

Fase 1 laat zien wat er draait en rolt clusters uit. Fase 2 laat zien of het nog klopt, en bewijst dat met echte tests. Jonas krijgt antwoord op zes vragen: wie heeft wat veranderd, is er van elke machine een verse back-up die echt opstart, wijkt een node af van zijn gewenste staat, neemt de reserve-node het VIP op tijd over, welke clusters raakt het als een database uitvalt, en kan een cluster vanuit Git beheerd worden.

Alles blijft in dezelfde Go-binary, met dezelfde agent, PostgreSQL en eventtabel. Er komt geen nieuw proces in de stack, en geen enkele fase 2-module heeft Grafana, VictoriaMetrics of Loki nodig. Fase 2 telt zes modules in zeventien mijlpalen. Mijlpaal 13 maakt daarnaast het templatewerk van fase 1 af met Docker-cluster, Cron-cluster en Generic application.

| Module | Wat Jonas krijgt | Mijlpalen |
| --- | --- | --- |
| Audit logging | Een logboek op `/logboek`, alleen voor admins, met per regel een Nederlandse zin, filters op periode, gebruiker, cluster, node, taak en soort, vrij zoeken en export als NDJSON. Elk agentcommando dat iets verandert en de herkomst van elke actie (IP, taak) staan erin. | 1, 3 |
| Backup controle | Per node de leeftijd van de nieuwste Proxmox-back-up, en de VM's die in geen enkele back-upjob zitten. Daarna een back-up terugzetten als tijdelijke VM in een afgesloten sandbox, opstarten, controleren en altijd weer verwijderen, later ook met een controle van services en databases. | 2, 8, 11, 16 |
| Drift detection | Per node wat afwijkt van de gewenste staat, zoals een gewijzigd bestand, een gestopte dienst of een ontbrekend pakket. Dat werkt voor clusters uit een template en via een baseline voor bestaande clusters, met negeren op reden en voor templateclusters herstel node voor node. | 4, 5, 6, 10 |
| Failover testing | Keepalived of de dienst stoppen op de VIP-eigenaar, vanaf de server meten hoe lang het VIP onbereikbaar is en waar het terechtkomt, en alles terugzetten. Het rapport luidt bijvoorbeeld 'Test: keepalived stoppen op web-01 / Verwacht: VIP binnen 5 s op een andere node / Resultaat: PASS, na 3,2 s op web-02'. | 7, 11 |
| Dependency mapping | Diensten en wie van wie afhangt, per cluster en over clusters heen, uit templates, facts en handmatige invoer. De status gaat mee langs de graaf, en het venster van een riskante actie toont welke diensten geraakt worden. | 9, 12 |
| GitOps | Clusters uit een ingebouwde template beheren met `clusters/<slug>/cluster.yaml` in een GitHub-repository: valideren, een plan per node, goedkeuren, toepassen, terugdraaien met git revert, omhoog schalen en nieuwe clusters maken. | 14, 15, 17 |

Na elke mijlpaal is er iets bruikbaars. Na mijlpaal 2 weet Jonas of elke machine, ook ClusterForge zelf, een verse back-up heeft. Na mijlpaal 3 staat elke actie met herkomst in het logboek. Na mijlpaal 6 werkt drift op zijn met de hand gebouwde clusters, en na mijlpaal 7 en 8 weet hij of failover en terugzetten echt werken. Vanaf mijlpaal 11 controleert ClusterForge dat zelf in een nachtelijk venster, en vanaf mijlpaal 15 wijzigt hij een cluster met een commit.

**Uitgangspunten**

- **Eerst lezen, dan schrijven.** Elke module begint met een mijlpaal die alleen kijkt: het logboek, de versheid van back-ups, drift, de graaf en het GitOps-plan. Een ingreep op nodes komt pas als de leeskant zich bewezen heeft, en de eerste ingrepen blijven beperkt tot lab, test of een sandbox-VM.
- **Eén weg voor root-acties.** Elk commando dat iets verandert, vertrekt uit `Bus.Command` en staat daardoor in het logboek. Elke schrijvende taak gaat door het clusterslot, en elke taak die meer dan één node raakt, gebruikt de gezondheidspoort. Herstel en GitOps delen één uitroltaak, `cluster.apply`. Geen module bouwt hier een eigen variant van.
- **Opnieuw proberen is een nieuwe taak.** Taken die iets op nodes doen (`cluster.apply`, `failover.test` en `backup.verify`) zijn niet Retryable. Een nieuwe poging start vanuit de actuele toestand, zodat een oude beslissing nooit zonder nieuwe controle wordt uitgevoerd.
- **Niets geheims verlaat de server.** Gerenderde inhoud, en ook een ongezouten hash ervan, komt nooit in de database, de events, de API of Git. Vingerafdrukken zijn HMAC's met een sleutel die van `CF_MASTER_KEY` is afgeleid, en plannen tonen geheimen als plaatshouder.
- **Eén invasieve test tegelijk.** Over heel ClusterForge loopt hoogstens één failovertest of back-upcontrole. Geplande tests draaien alleen in het testvenster, en op prod start een failovertest alleen met de hand.
- **De meeste waarde zonder nieuwe agentversie.** Tot en met mijlpaal 15 komt er maar één protocolverhoging, voor `state.inspect`. Logboek, back-ups, sandbox, failovertests en de graaf werken met de agents die nu draaien, en bijwerken gaat met één commando per node.
- **Een mens beslist over elke wijziging.** Er is geen automatisch herstel en geen automatisch toepassen vanuit Git, ook niet in lab. Geplande tests zet Jonas zelf aan, en op prod vraagt elke riskante actie de getypte clusterslug van een admin met TOTP.

## Wat fase 1 al klaarzet en wat ontbreekt

Fase 1 is gebouwd tot en met fase 1-mijlpaal 7, en veel van wat fase 2 nodig heeft, bestaat al. De enige ingebouwde template is keepalived-nginx.

- **Events.** Een append-only tabel met een trigger tegen UPDATE en DELETE. Inventory schrijft al diffs per veld (`cluster.updated`, `node.updated`, `vip.updated`), en elk event gaat via `pg_notify` live naar de webinterface. Lezen kan alleen als lijst van de laatste 200, zonder filters.
- **Taken.** Stappen op volgnummer die na een herstart van de server verder gaan, `requested_by` per taak, en een unieke index die per node één node-actie tegelijk toelaat.
- **Agent.** Protocol 3, getypte commando's en `apply.steps` met zes staptypes. De heartbeat komt elke 10 s met de adressen en VIP's van de node, en de facts volgen een vaste lijst services.
- **Uitrol.** `renderContext`, `apply`, `check`, `createVM` en `enrollAll` bestaan al als stukken van de uitroltaak. Een uitrol schrijft `clusters.spec` met revisie 1 en bron ui, `cluster_spec_revisions` laat bron git al toe, en `clusters.git_repo_url` bestaat als tekstveld.
- **Proxmox.** Een client met een smalle interface, een sync elke 20 s die ook storages met hun content bewaart, en VM-acties als taak.
- **Lifecycle.** `checkTakeover`, een drain die met `context.WithoutCancel` terugdraait, wachten op een verse heartbeat die het VIP elders toont, en het bevestigingspatroon `needs_force`.
- **Tests.** `pvefake` met een call-log, `agenttest.Host` die apt en systemd naspeelt, `keepalivedSim` en een fleet-helper die een volledige uitrol met echte agents doet.

De ontwerpen vonden ook gaten. De meeste zijn dicht voordat fase 2 iets op een node verandert.

| Gat | Gevolg | Opgelost in |
| --- | --- | --- |
| De tabellen `services` en `node_fact_snapshots` uit het fase 1-ontwerp zijn niet gebouwd. | Dependency mapping heeft geen basis, en de diensten in templates worden geparst maar nergens gelezen. | Mijlpaal 9 maakt `services`. Snapshots komen er niet; de events zijn de geschiedenis. |
| De SSE-notificatie bevat `action`, `subject_id` en `cluster_id` van elk event, ook voor viewers. Een mislukte login bewaart een onbekende naam letterlijk (`internal/auth/service.go:146-152`). | Een wachtwoord dat in het naamveld getypt werd, staat in events en gaat naar elke open browser. | Mijlpaal 1 |
| Agentcommando's, specrevisies en de herkomst van een actie (IP, taak) worden niet gelogd. | Het logboek kan niet zeggen welke root-actie een taak deed, of vanaf welk IP. | Mijlpaal 3 |
| Er is geen clusterslot. Alleen node-acties sluiten elkaar per node uit, en Proxmox-acties kijken nergens naar. | Een herstel, een failovertest en een VM-stop kunnen tegelijk in hetzelfde cluster lopen. | Mijlpaal 3 |
| Adressen uit heartbeats tot 90 s oud tellen mee voor de VIP-eigenaar (`internal/status/rules.go:28-29`). | Elke VIP-wissel, ook bij onderhoud uit fase 1, geeft even een vals split_brain of down. | Mijlpaal 3 |
| `Runner.Finished` bestaat, maar wordt nergens gezet. | Een taak die in de wachtrij geannuleerd wordt, laat de gegevens van haar module onafgewerkt achter. | Mijlpaal 3 |
| `templates.Get` kent één versie per naam (`internal/templates/templates.go:154-161`). | Na een serverupdate vergelijkt drift met een nieuwere template dan er is uitgerold. | Mijlpaal 4 |
| `Validate` vult een ontbrekend geheim met een willekeurige waarde (`internal/templates/params.go:107-109`). | Opnieuw renderen zou elke node een ander VRRP-wachtwoord geven. | Mijlpaal 4 |
| De spec bewaart per node geen `node_id` en index, en renderen kan alleen binnen de uitroltaak. Handmatige clusters hebben spec `'{}'` en revisie 0. | Buiten een lopende uitrol is er geen gewenste staat om mee te vergelijken. | Mijlpaal 4, en voor handmatige clusters de baseline in mijlpaal 6 |
| De agent heeft geen leesmodus, en bijwerken kan alleen door opnieuw aan te melden met een nieuw token. | Een dry-run-vlag zou op een oude agent echt toepassen, en elke nieuwe agentfunctie kost per node een nieuwe aanmelding. | Mijlpaal 5 |
| Er is geen planner; elke lus is een losse ticker in `main`. | Er is geen plek voor periodieke controles en een testvenster. | Mijlpaal 5 en 11 |
| Een teruggezette kopie van een VM met dezelfde `agent.json` meldt zich als dezelfde node, en VM-acties accepteren elke VM. | Een sandbox kan commando's meekrijgen en de VIP-eigenaar verstoren. | Mijlpaal 8 en 16 |
| Een mislukte taak kan opnieuw vanaf de mislukte stap, en de runner slaat dan geslaagde stappen met hun controles over. | Een retry uren later voert oude beslissingen uit. | Vanaf mijlpaal 7 zijn taken op nodes niet Retryable |

## Gedeelde bouwstenen

Vijftien mechanismen worden één keer gebouwd en door meerdere modules gebruikt. De modulehoofdstukken noemen ze bij naam en leggen ze niet opnieuw uit. Ze staan hier in de volgorde waarin ze gebouwd worden.

### Eventcatalogus en agent.command

Elke regel in het logboek is een leesbare zin, en elke root-actie op een node staat erin, ook als een taak haar uitvoerde. `internal/events` krijgt daarvoor een catalogus, `Known`, met per action precies één soort en één Nederlandse zin. Het logboek maakt daar regels van als 'Node web03 toegevoegd', en het filter op soort zoekt op de exacte lijst actions van die soort. Een test scant de Go-bronnen en faalt als een action geen zin of soort heeft, dus elke fase 2-module registreert haar events in de catalogus.

`Bus.Command` is de enige plek waar commando's naar agents vertrekken. Voor elk commando dat iets verandert (`system.reboot`, `system.shutdown`, `node.maintenance.*` en `apply.steps`) schrijft het eerst een event `agent.command` met de `job_id` uit de context. Lukt dat niet, dan gaat het commando niet weg. Van stappen bewaart het alleen soort, pad, unit, pakketnamen en `creates`, nooit `run`, `unless` of inhoud, omdat die met geheimen gerenderd worden. Leescommando's zoals `facts.collect` en `state.inspect` geven geen regel, en geen module schrijft zelf een event per agentcommando.

Komt in mijlpaal 1 (catalogus) en mijlpaal 3 (`agent.command`). Gebruikt door alle zes modules.

### Clusterslot

Per cluster loopt hoogstens één schrijvende taak tegelijk. Een herstel valt dus nooit samen met een reboot, een failovertest of een VM-stop in hetzelfde cluster. Er komt één functie `jobs.EnqueueForCluster`. Die vergrendelt in één transactie de clusterrij met `SELECT ... FOR UPDATE`, zoekt met één query een wachtende of lopende taak met dit `cluster_id` of met een node van dit cluster, en zet de nieuwe taak alleen in de wachtrij als er geen is. Een weigering geeft 409 met de titel van de lopende taak.

Alle schrijvende aanvragen gaan erdoor: node-acties, Proxmox-acties op een VM die aan een node in een cluster hangt, een uitrol, `cluster.apply` en `failover.test`. Leesacties tellen niet mee: `refresh_facts`, de driftcontrole en `backup.verify`. De bestaande index per node blijft staan, en elke langlopende taak kijkt aan het begin van elke nodestap opnieuw of de node nog active is.

Komt in mijlpaal 3. Gebruikt door drift-herstel, GitOps, failovertests en de bestaande node- en Proxmox-acties.

### Statusdebounce

Een gewone VIP-wissel geeft geen vals split-brain of down meer. Nu tellen adressen uit heartbeats tot 90 s oud mee, terwijl een heartbeat elke 10 s komt, dus bij elke wissel staat het VIP even op twee nodes of op geen enkele. Met de debounce telt een conflict of een VIP zonder houder pas als het langer dan anderhalf heartbeatinterval duurt, 15 s; tot dan blijft de vorige eigenaar staan. Een node waarvan de VM volgens Proxmox stopped is, telt niet als houder.

Code die na de evaluator moet rekenen, zoals de impact van afhankelijkheden, draait in een eigen transactie na de commit en kan de evaluator nooit laten mislukken. De evaluator werkt `vips.owner_node_id` in dezelfde transactie bij als de status (`internal/status/evaluator.go:80-145`), en `checkTakeover` en de VIP-controles van lifecycle steunen daarop. Een fout in nieuwe code mag die transactie dus nooit tegenhouden.

Komt in mijlpaal 3. Gebruikt door failovertests, drift-herstel, GitOps, de impact uit dependency mapping en het bestaande onderhoud.

### OnFinished

Elke taak wordt door haar module netjes afgerond, ook als haar handler nooit draaide. `Runner.Finished` bestaat al, maar is één functie en wordt nergens gezet, terwijl sommige taken eindigen zonder dat hun handler draait: geannuleerd in de wachtrij, of te vaak onderbroken. Daarom komt er `Runner.OnFinished(kind, fn)`, met meerdere abonnees per soort, aangeroepen na `FinishJob`.

Elke module rondt daar haar eigen gegevens af. Een testrun zonder eindresultaat krijgt result `error` en `restored` false, een sandbox gaat naar de opruimer, een node wordt opnieuw op drift gecontroleerd en een Git-wijziging wordt applied of failed.

Komt in mijlpaal 3. Gebruikt door drift, back-upcontrole, failovertests en GitOps.

### Vingerafdrukken met afgeleide sleutels

ClusterForge ziet dat een bestand veranderd is zonder de inhoud of een te raden hash te bewaren. Gerenderde bestanden bevatten geheimen, zoals `auth_pass` in `keepalived.conf` (`internal/templates/builtin/keepalived-nginx/files/keepalived.conf.tmpl:32`), en een geheim heeft maar 6 tot 64 tekens. Omdat de rest van zo'n bestand bekend is, is een gewone sha256 offline te raden. Voor heel fase 2 geldt daarom dat niets wat uit gerenderde inhoud komt ongezouten naar de database, de events of de API gaat. `secrets.Box` krijgt `Derive(doel)`, dat met HKDF een sleutel van `CF_MASTER_KEY` afleidt, en een vingerafdruk is `HMAC(afgeleide sleutel, inhoud)`. De sleutel staat zo nooit naast de gegevens in de database.

Plannen en diffs renderen geheime parameters met een vaste plaatshouder in plaats van met de echte waarde. Er valt dan niets te maskeren, en de poll-lus van GitOps hoeft nooit een geheim te ontsleutelen. Zonder masterkey zijn er geen vingerafdrukken, en de module meldt dat in plaats van stil door te gaan.

Komt in mijlpaal 3. Gebruikt door drift, het logboek (`agent.command`) en het GitOps-plan.

### Gewenste staat met vaste templateversie

Uitrol, plan, driftcontrole en herstel zien voor elk cluster precies dezelfde stappen, gerenderd met de templateversie waarmee het cluster is uitgerold. Ingebouwde templates komen per versie in `builtin/<naam>/<versie>/` en worden opgezocht met `Get(naam, versie)`. `clusters.template_version` legt de versie al vast, en een test faalt als een versie verdwijnt die een cluster nog gebruikt. Een templateversie bijwerken is een expliciete wijziging van de spec, nooit iets wat stilzwijgend met een serverupdate meekomt.

De anonieme spec van de uitrol wordt een getypte `deploy.Spec`, met per node `node_id`, hostname, rol, index en adres, plus een kopie van naam, slug en omgeving van het cluster. `RenderNode(spec, geheimen, facts)` en `ApplySteps` worden uit de uitroltaak gehaald, zodat ze ook buiten een uitrol werken. Buiten een nieuwe uitrol is een ontbrekend geheim een fout, nooit een nieuwe waarde. Een oude spec zonder `node_id` wordt bij het lezen op hostname gekoppeld, en een lidmaatschapscontrole vergelijkt de actieve nodes met `spec.nodes`. `templates.Parse` weigert een template waarin `run`, `unless` of `creates` van een command-stap, of het pad van een file- of directory-stap, een parameter gebruikt zonder streng type (ipv4, int, cidr) of strikt pattern.

Komt in mijlpaal 4. Gebruikt door drift, GitOps, de diensten uit templates in dependency mapping, de verwachte services van de back-upcontrole en later de auto-documentatie van fase 3.

### state.inspect

ClusterForge leest de staat van een node met een commando dat gegarandeerd niets verandert, ook op een oude agent. Er komt bewust geen dry-run-vlag op `apply.steps`: een agent met protocol 3 leest commando's zonder `DisallowUnknownFields` (`internal/agent/commands.go:104`) en zou zo'n vlag dus negeren en de stappen echt toepassen. Een onbekende action weigert hij wel. Daarom is `state.inspect` een apart getypt commando, met een eigen requesttype zonder inhoudsveld.

Het gebruikt dezelfde leesfuncties als apply (`dpkg-query`, `systemctl show`, `id` en `stat`) en voert nooit `apt-get`, een ander systemctl-werkwoord of `sh` uit, dus ook geen `unless`. De server berekent de vingerafdrukken in het geheugen. Het resultaat is de waargenomen staat voor drift, de controle vlak voor een herstel, en in een GitOps-plan de lijst van bestanden die op een node al afwijken.

Komt in mijlpaal 5, als protocol 4. Gebruikt door drift, `cluster.apply` bij herstel en het GitOps-plan.

### Agentversie en bijwerken

Agents bijwerken is één commando per node, en de server stuurt een agent nooit een commando dat hij niet kent. De protocolversie blijft één oplopend getal, met per functie een eigen constante zoals `ApplySince`; `state.inspect` wordt 4. Een release die alleen meer verzamelt, zoals een langere lijst `WatchedServices`, of een subcommando toevoegt, zoals `cf-agent verify`, vraagt geen hoger protocol.

Bijwerken gaat niet via een agentcommando, maar met een nieuwe optie `install.sh --upgrade`. Die vervangt de binary na een checksumcontrole, houdt `/etc/clusterforge/agent.json` en herstart de service; nu kan bijwerken alleen door opnieuw aan te melden met een nieuw token. Node- en clusterdetail tonen per node 'agent te oud voor ...' met het commando om te kopiëren. `agent-state.json` wordt voortaan alleen geschreven via één functie met een mutex die alle velden behoudt.

Komt in mijlpaal 5. Gebruikt door drift (`state.inspect`), de back-upcontrole (`cf-agent verify` en het sandbox-merkteken), dependency mapping (meer gevolgde units) en later de dodemansknop van fase 3.

### Planner

Periodieke taken draaien allemaal op dezelfde manier en houden rekening met zomer- en wintertijd, en een gemiste run wordt overgeslagen in plaats van midden op de dag ingehaald. Nu is elke lus een losse ticker in `main`. `internal/planner` levert twee kleine dingen. `Loop(ctx, interval, kick, fn)` neemt per lus een advisory lock en logt fouten, naar het model van `Evaluator.Run`. `Next(regel, tijdzone, nu)` rekent 'elke N dagen om HH:MM' en het testvenster uit.

Een run die meer dan 30 minuten te laat is, wordt overgeslagen. Elke module bewaart haar eigen `next_run_at`; er komt geen algemene planningstabel.

`Loop` komt in mijlpaal 5 en `Next` in mijlpaal 11. Gebruikt door de driftscanner, de back-upinventaris en -controles, de failoverplanning en de nachtelijke export van het logboek.

### Gezondheidspoort

Een taak gaat pas naar de volgende node als verse waarnemingen bewijzen dat de vorige gezond is. Een helper in een nieuw pakket `internal/health` wacht op bewijs dat nieuwer is dan een gegeven tijdstip. Eerst moeten er minstens twee heartbeats van de node na het commando zijn, waarin alle services uit de stappen van die node active zijn en de node healthy is. Daarna moet het cluster precies één houder per VIP hebben en mag het niet split_brain of down zijn. Voordat de VIP-eigenaar aan de beurt is, draait `checkTakeover`, en na de laatste node draaien de controles van de template.

Opgeslagen status telt niet mee, want na een herstart van de server schrijft de evaluator 30 s niets. Lifecycle heeft dit patroon nu los in `waitMoved` en `undrain`; die worden de eerste gebruikers van de helper.

Komt in mijlpaal 7. Gebruikt door drift-herstel, `cluster.apply` voor GitOps, de terugkeer na een failovertest en node lifecycle.

### test_runs

Failovertests en back-upcontroles hebben één rapportvorm, één rapportpagina en één geschiedenis. De tabel `test_runs` heeft de kolommen `id`, `kind` (`failover.test` of `backup.verify`), `trigger` (`manual` of `schedule`), `cluster_id`, `node_id` met een kopie van de hostname, `job_id`, `definition` (de invoer bij de start, jsonb) en `result`. `result` is null zolang de taak loopt en daarna `pass`, `warning`, `fail`, `error`, `skipped` of `canceled`. Verder zijn er `restored`, `summary`, `checks`, `timeline` en `measurements` (jsonb, bijvoorbeeld onderbreking in ms of hersteltijd in s) en de tijden. Of een run wacht of loopt, lees je via `job_id` uit jobs, zodat er één bron van waarheid is.

Eén component 'laatste resultaat' toont beide soorten. Wat per soort verschilt, staat in eigen tabellen: `failover_tests` voor de definities en `backup_sandboxes` als sandbox-register. Drift houdt zijn eigen `drift_checks` als laatste stand per node.

Komt in mijlpaal 7. Gebruikt door failovertests, de back-upcontrole en in fase 3 de health score, What Broke en de auto-documentatie.

### Testslot en testvenster

Er loopt nooit meer dan één invasieve test tegelijk, en geplande tests draaien alleen in een vast nachtelijk venster. Het slot is een partiële unieke index op jobs, `((true)) WHERE kind IN ('backup.verify','failover.test') AND status IN ('queued','running')`, naar het patroon van de bestaande index voor node-acties. Geplande tests draaien in één globaal venster, `CF_TEST_WINDOW`, standaard zondag 03:00 tot 05:00 in de tijdzone van de server. Een eigen tijdstip is er niet: de planning staat alleen aan of uit, per failovertest en voor de back-upcontrole per cluster.

De planner doet de voorcontrole voordat hij een taak maakt. Een overgeslagen run staat dan als skipped in `test_runs`, zonder een mislukte taak die iemand per ongeluk opnieuw start.

Komt in mijlpaal 7 (slot) en mijlpaal 11 (venster). Gebruikt door de back-upcontrole, failovertests en later de disaster simulations van fase 3.

### Sandbox-register

ClusterForge kan alleen VM's verwijderen of starten die het zelf als sandbox gemaakt heeft. De tabel `backup_sandboxes` (`connection_id`, `vmid`, `run_id`, `state`, `host`, `storage` en de tijden) is het enige register van tijdelijke VM's. Destroy, start en later de guest-exec van `cf-agent verify` bestaan alleen als bewaakte functies met vier voorwaarden: er is een rij in het register, de VM zit volgens de laatste sync in pool `cf-sandbox`, geen node is aan dat VMID gekoppeld, en het is niet het bron-VMID.

Verwijderen gebeurt alleen met `purge=1` en nooit met `destroy-unreferenced-disks`. Losse hosts delen elk hun eigen VMID's uit, en die optie kan op gedeelde storage een productieschijf met hetzelfde VMID wissen. `proxmox.RequestAction` en het koppelen van een VM aan een node weigeren een VM uit het register. Isoleren gebeurt met een allowlist van configsleutels, niet met een denylist.

Komt in mijlpaal 8. Gebruikt door de back-upcontrole en later de disaster simulations van fase 3.

### cluster.apply

Elke wijziging die meer dan één node raakt, gaat door één taak die node voor node toepast, met de VIP-eigenaar als laatste, en die stopt zodra iets niet gezond is. Bij het in de wachtrij zetten liggen in de parameters vast: de spec-revisie, de nodes in hun volgorde en per node welke stappen. Eerst komen de nodes zonder VIP en dan de eigenaar. Bij omhoog schalen komen eerst de bestaande nodes en dan de nieuwe, zodat geen node met een onbekende unicast-peer start. De stappen zijn alles wat verandert, of bij herstel alleen de gekozen afwijkende stappen met hun notify-handlers.

Bovenaan de handler en aan het begin van elke nodestap controleert de taak opnieuw de revisie, of de node active is en bij herstel de vingerafdruk van de afwijking. Daardoor is hervatten na een herstart veilig, ook al herkent de runner stappen alleen aan hun volgnummer en slaat hij geslaagde stappen over (`internal/jobs/jobs.go:432-439`). Na elke node volgt de gezondheidspoort. De taak is niet Retryable, gaat door het clusterslot en vraagt op prod de bevestiging bij prod.

Komt in mijlpaal 10; de stappen voor nieuwe VM's en het aanmelden van agents komen in mijlpaal 17. Gebruikt door drift-herstel, GitOps (toepassen en schalen) en later het bijwerken van een templateversie.

### Bevestiging bij prod

Op prod gebeurt niets riskants per ongeluk. De API geeft 409 `needs_confirmation` zolang het veld `confirm` niet gelijk is aan de clusterslug, naar het patroon van `needs_force` bij node-acties. Daarnaast moet de gebruiker een admin zijn met TOTP aan, want TOTP is bij het inloggen nu optioneel. De webinterface heeft één bevestigingsvenster dat in gewone zinnen zegt wat er gaat gebeuren, met de maximale gevolgen erbij, en dat de slug laat intikken.

Komt in mijlpaal 10. Gebruikt door drift-herstel, de GitOps-goedkeuring en failovertests op prod.

## Audit logging

Audit logging maakt van de append-only eventtabel uit fase 1 een logboek dat Jonas kan doorzoeken. Een vraag als "wie heeft vorige week iets aan webcluster-prod veranderd, en vanaf welk IP?" beantwoordt hij in één scherm: hij kiest het cluster en de periode en klapt de regel open die hem interesseert. Elke regel is een Nederlandse zin met een link naar het onderwerp, en een taak die hij startte, blijft op zijn naam staan, ook als de server ze afrondt.

```text
05-10-2026 14:08 · systeem namens Jonas · Taak geslaagd: Herstarten: web01 · webcluster-prod
05-10-2026 14:07 · systeem · VIP 10.0.20.100 verhuisd van web01 naar web02 · webcluster-prod
05-10-2026 14:06 · systeem namens Jonas · Commando system.reboot gestuurd naar web01 (reden: kernelupdate) · webcluster-prod
05-10-2026 14:06 · Jonas · Taak gestart: Herstarten: web01 · webcluster-prod
05-10-2026 14:02 · Jonas · Node web03 toegevoegd · webcluster-prod
```

Klapt hij "Taak gestart" open, dan ziet hij het IP, de sessie, een link "alles van deze taak" en de ruwe payload. Bij een wijziging als "Cluster webcluster-prod gewijzigd: omgeving van test naar prod" staat er een tabel met veld, oude en nieuwe waarde.

Daarnaast legt de module vast wat nu ontbreekt: mislukte herauthenticatie, mislukte enrollments, elk agentcommando dat iets verandert, specrevisies en de herkomst (IP, sessie, taak) van elke actie. Ze dicht ook twee lekken die vandaag al bestaan: een getypte gebruikersnaam, soms een wachtwoord, staat letterlijk in events, en de details van elk event gaan naar de SSE-stream van elke ingelogde browser, ook die van viewers.

### Hoe het werkt

**Zinnen en soorten uit de eventcatalogus.** internal/audit maakt van elke rij een regel met wie (Jonas, agent op web01, systeem of systeem namens Jonas), de zin uit de eventcatalogus, de wijzigingen en de links. De diffs die inventory nu al als {from, to} per veld schrijft, worden een tabel met Nederlandse veldnamen, en owner-id's worden gebruikersnamen. De soorten zijn Beveiliging, Inventory, Taken, Agents, Proxmox en Status, en de fase 2-modules voegen er hun eigen aan toe, zoals Drift, GitOps, Back-ups en Failovertests.

**Eén query voor alle filters.** ListAudit leest events met joins naar gebruikers, agents, nodes, clusters en taken, zodat elke regel namen heeft en een verwijderd onderwerp herkenbaar blijft. Alle filters zijn optioneel: periode, gebruiker, cluster, node, taak, soort, actortype, action-prefix en zoektekst. Het filter op gebruiker kijkt naar de actor en naar jobs.requested_by, zodat "systeem namens Jonas" meekomt. Het filter op cluster vindt ook cluster.deleted, dat geen cluster_id heeft. Zoeken is ILIKE op action, onderwerp, payload, gebruikersnaam, clusternaam en hostname, met % en _ geëscapet. Een full-text index is niet nodig, want het volume blijft onder 200.000 regels per jaar. Pagineren gaat met een cursor op id, nieuwste eerst, zodat nieuwe regels de volgende pagina niet verschuiven.

**Herkomst zonder extra code per handler.** events.Writer neemt de herkomst uit de context en zet ze onder payload.origin: ip, de eerste 8 hex-tekens van de sessiehash en job_id. requireSession en Login zetten IP en sessie, en de takenrunner zet job_id in de context van elke handler. Zo hoort alles wat tijdens een taak gebeurt bij die taak, agent.command inbegrepen. De herkomst staat genest omdat de payload van een *.updated-event per veldnaam een diff bevat (internal/inventory/inventory.go:516-527), en een veld met dezelfde naam zou botsen.

**Agentcommando's.** Voor agent.command, beschreven bij de eventcatalogus, bewaart Bus.Command van een file-stap een vingerafdruk in plaats van de inhoud. Een retry met hetzelfde commando-id geeft bewust een tweede regel, zodat de retry zichtbaar is. De agent en het protocol veranderen voor deze module niet.

**Nieuwe en gewijzigde events.**

| Event | Wanneer en wat |
| --- | --- |
| `auth.login_failed` (gewijzigd) | Bij een onbekende naam alleen subject "onbekend", het IP en de reden. Een uitgeschakelde gebruiker krijgt reden `disabled` in plaats van `bad_password`. |
| `auth.login` en `agent.enrolled` (gewijzigd) | De login krijgt de user-agent erbij (ingekort), de enrollment het IP. |
| `auth.reauth_failed` | Een fout huidig wachtwoord bij wachtwoord wijzigen, of een foute code bij TOTP aan- of uitzetten (`what` zegt welke). |
| `auth.totp_setup_started` | Iemand begint TOTP in te stellen. |
| `auth.rate_limited` | De loginlimiet slaat aan. |
| `agent.enroll_failed` | Een ongeldig of verlopen enrollmenttoken, met IP, hostname en reden. |
| `agent.command` | Elk wijzigend commando, geschreven voordat het vertrekt. |
| `cluster.spec_changed` | Een nieuwe specrevisie, in dezelfde transactie: revisie, vorige revisie, bron, template en versie. GitOps schrijft later hetzelfde event met bron git en de commit. |
| `secret.created` | Per clustergeheim alleen de naam, nooit de waarde. |
| `proxmox.sync_requested` | Iemand start een Proxmox-sync met de hand. |
| `audit.exported` | Elke export, met de filters en het aantal regels. |

auth.rate_limited en agent.enroll_failed worden per remote IP gedrosseld, met daarnaast per action een grens van 10 per minuut. Daarboven volgt één samenvattend event met het aantal weggelaten pogingen. Geweigerde toegang (403 en CSRF) en onbekende NATS-sleutels gaan naar slog en niet naar het logboek: de webinterface toont viewers geen admin-acties, en een aanvaller maakt gratis nieuwe sleutels, dus zulke events zouden vooral het logboek vullen. De namen user.disabled, user.enabled, user.role_changed, user.totp_reset en user.sessions_revoked staan al in de catalogus, voor het gebruikersbeheer dat later komt.

**Export.** Er is één formaat, NDJSON: één event per regel, met dezelfde velden als de API en tijden in UTC (RFC 3339). Dat werkt met jq en kan later zonder omzetting naar Loki. De export streamt in pagina's van 1000 via dezelfde query, tot 100.000 regels, en verlengt de write deadline per pagina, want de server heeft een WriteTimeout van 60 s. Optioneel schrijft een nachtelijke lus van de planner de events van de vorige dag naar CF_EVENTS_EXPORT_DIR, bedoeld als share op de NAS met snapshots.

**Live.** De notificatie bij een nieuw event bevat alleen nog het event-id. live.ts ververst daarop ook de query van het logboek, maar alleen op de eerste pagina, zodat een lijst waarin Jonas verder gescrold heeft niet verspringt.

### Datamodel

Alles komt in één nieuwe migratie. Er komen geen nieuwe tabellen: de herkomst staat in payload.origin.

| Tabel | Kolommen | Waarom |
| --- | --- | --- |
| events | `node_ref text`, gegenereerd en opgeslagen: subject_id bij subject node, anders payload.node_id | Filteren op node vindt ook taken en agentevents over die node. job.queued, de taakafronding, agent.enrolled en enrollment_token.created hebben al node_id. |
| events | `job_ref text`, gegenereerd: subject_id bij subject job, anders payload.job_id of payload.origin.job_id | "Alles van deze taak", en de koppeling naar jobs.requested_by voor "namens Jonas". |
| events | Indexen op (node_ref, id DESC) en (job_ref, id DESC), partieel op NOT NULL, en op (actor_type, actor_id, id DESC) | Snelle filters op node, taak en gebruiker, samen met de cursor op id. |
| events | Trigger events_no_truncate, BEFORE TRUNCATE | De bestaande trigger werkt per rij, en TRUNCATE ging er langs. |
| events | notify_event() stuurt alleen {"id"} | Nu gaan action, subject_type, subject_id en cluster_id naar elke SSE-stream (migrations/00004_monitoring.sql:21-23). |

De gegenereerde kolommen zijn text, dus een cast kan nooit mislukken. Het toevoegen herschrijft de tabel zonder een UPDATE-trigger af te vuren, zodat ook oude regels een waarde krijgen zonder de append-only regel te breken. Nieuwe sqlc-queries zijn ListAudit, ListAuditUsers (ook uitgeschakelde gebruikers, die ListUsers weglaat) en AuditStats (aantal regels en oudste tijdstip).

### API

| Methode | Pad | Doel |
| --- | --- | --- |
| GET | `/api/v1/audit` | Logboek, nieuwste eerst. Query: `before` (id), `limit` (1 tot 200, standaard 50), `from`, `to`, `user`, `cluster`, `node`, `job`, `category`, `actor_type`, `action` (prefix) en `q`. Antwoord: `{items, next_before}`. |
| GET | `/api/v1/audit/export` | Dezelfde filters, als gestreamde NDJSON-bijlage van hoogstens 100.000 regels. Schrijft audit.exported. |
| GET | `/api/v1/audit/info` | Voor de filterbalk en de instellingen: de soorten met hun label, alle gebruikers (ook uitgeschakelde), het aantal regels en het oudste tijdstip. |
| GET | `/api/v1/events` | Blijft voor compatibiliteit. Het overzicht stapt over op `/audit?limit=20`, zodat de labels in TypeScript weg kunnen. |

Alle vier zijn alleen voor admins. Een AuditEntry bevat id, ts, category, action, actor, on_behalf_of, summary (de zin), subject (met deleted), cluster, node, job, ip, changes (field, label, from, to) en payload. De export is een GET met de SameSite=Strict-cookie, dus een gewone link in de webinterface volstaat. De beschrijving van `/stream` in api/openapi.yaml zegt voortaan dat een notificatie alleen het event-id bevat.

### Schermen

- **Logboek (/logboek).** Een nieuw menu-item, alleen zichtbaar voor admins; een viewer die de pagina opent, ziet "Alleen voor beheerders". Bovenaan staat een filterbalk met periode (vandaag, 7 dagen, 30 dagen of zelf van en tot), gebruiker (ook Systeem en Agents), cluster, node (beperkt tot het gekozen cluster), soort en een zoekveld. De filters staan in de URL, zodat Jonas een gefilterde weergave kan bewaren als bladwijzer.
- **De lijst.** Kolommen Tijd, Wie, Wat en Cluster, in lokale tijd (nl-BE). Wat linkt naar het cluster, de node, de taak of Proxmox; bij een verwijderd onderwerp staat "(verwijderd)" zonder link. Een regel klapt open met de wijzigingen als tabel, IP en sessie, een link "alles van deze taak", het event-id en de ruwe payload als ingeklapte JSON. Onderaan staat "Meer laden"; paginanummers zijn er niet. Rechtsboven exporteert een knop de huidige filters naar NDJSON.
- **Geschiedenis.** Cluster-, node- en taakdetail krijgen één gedeelde kaart met de laatste 10 regels en een link "Alles in het logboek".
- **Overzicht en instellingen.** De kaart "Recente activiteit" gebruikt de zinnen van de server. Instellingen krijgt een alinea Logboek met het aantal regels en de oudste regel.

### Veiligheid

- **Geen geheimen in events.** De Writer vervangt de waarde van password, *_password, *_secret, totp_secret, private_key en value_enc door "[verborgen]"; de markering "gewijzigd" van het Proxmox-token blijft staan. Gewone namen als code, token, content en value blijven zichtbaar, en een module met geheime inhoud laat die zelf weg, met een eigen test. Strings boven 2 KB en payloads boven 16 KB worden ingekort. git_repo_url weigert voortaan een gebruikersnaam of wachtwoord in de URL.
- **Geen getypte tekst.** Een mislukte login met een onbekende naam bewaart alleen "onbekend", het IP en de reden; herhaalde pogingen zijn aan het IP te herkennen. Bestaande rijen met een letterlijke naam (internal/auth/service.go:146-152) blijven staan, want de tabel is append-only, maar het logboek toont ze als "onbekend" en zoeken en export slaan dat veld over.
- **Lektest.** TestDeploy rolt uit met auth_pass "geheim12" en token_secret "geheim-secret", plus een testtemplate met het geheim in unless. Die strings mogen nergens staan: niet in events.payload, jobs.params, jobs.error, de log, state en error van job_steps, cluster_spec_revisions.spec of de export.
- **Geen root-actie zonder spoor.** agent.command is fail-closed via de eventcatalogus. Inventory, agents, Proxmox en de nieuwe deploy-events schrijven in dezelfde transactie als de wijziging. In auth en bij opnieuw proberen of annuleren van een taak blijft het event best effort: een fout wordt gelogd, maar een databaseprobleem blokkeert het inloggen niet.
- **Onveranderbaarheid, eerlijk ingeschat.** De rijtrigger en de TRUNCATE-trigger beschermen tegen bugs en vergissingen, en ClusterForge verwijdert zelf nooit regels; een bewaartermijn is bij dit volume niet nodig. Tegen iemand met de databasecredential helpt een trigger niet, en de standaardopzet draait als PostgreSQL-superuser (deploy/docker-compose.yml:35). docs/verharding.md beschrijft hoe migraties als eigenaar draaien met `clusterforge-server migrate` en serve als een rol die op events alleen mag lezen en toevoegen, en dus de trigger niet kan uitzetten. Wat daar echt tegen helpt, is een kopie buiten het systeem: de optionele nachtelijke export naar de NAS. Een hash-keten zonder anker buiten de server voegt daar weinig aan toe, want wie de credential heeft, rekent de keten zelf opnieuw uit.
- **Rechten en volume.** Het logboek blijft alleen voor admins, omdat het IP's, beveiligingsevents en een kaart van de infrastructuur bevat. Lees-requests worden niet gelogd, een export wel. De drossels houden een aanvaller of een kapotte agent tegen die het logboek wil laten vollopen.
- **Geen productieschade.** De module stuurt zelf niets naar nodes. Het enige nieuwe faalpad is het fail-closed gedrag van agent.command, en dat faalt naar de veilige kant: geen actie. De migratie herschrijft de eventtabel één keer; bij minder dan een miljoen regels duurt dat seconden, onder een korte exclusieve lock bij het opstarten.

### Mijlpalen

- **Mijlpaal 1, Logboek en lekfixes.** Levert de nieuwe migratie (notificatie met alleen het id, TRUNCATE-trigger, node_ref en job_ref), de fix voor auth.login_failed, de eventcatalogus met zijn test, de drie audit-endpoints met NDJSON-export, het scherm /logboek, de kaart Geschiedenis en de zinnen op het overzicht.
- **Mijlpaal 3, Herkomst, agentcommando's in het logboek en het clusterslot.** Levert payload.origin, de filter op geheime sleutels, agent.command uit Bus.Command, de nieuwe auth-, agent-, deploy- en Proxmox-events met drossels en de controle op git_repo_url, zodat audit logging daarna af is; dezelfde mijlpaal bouwt ook het clusterslot, de statusdebounce en OnFinished.

De optionele nachtelijke export staat in geen van beide mijlpalen. Ze gebruikt de planner-lus, die er vanaf mijlpaal 5 is, en is daarna een klein los stuk. Elke latere mijlpaal registreert haar events in de catalogus, en de catalogustest bewaakt dat.

## Back-upcontrole

Back-upcontrole vertelt Jonas niet alleen dát er back-ups zijn, maar ook dat ze echt terug te zetten zijn en hoe lang dat duurt. ClusterForge leest de bestaande Proxmox-back-ups (vzdump op storage en Proxmox Backup Server), toont per node hoe oud de nieuwste is en meldt welke VM's in geen enkele back-upjob zitten. Op afroep, en later gepland in het testvenster, zet het een back-up terug als tijdelijke VM in een afgesloten sandbox, kijkt of die opstart en verwijdert hem daarna altijd. Op de pagina Back-ups ziet dat er zo uit:

```text
webcluster-prod                                                     back-up: te oud
  web01   6 u oud · pbs-main · 4,2 GB · PBS ok   vers     geslaagd 27-09-2026 · hersteltijd 3 min 40 s
  web02   31 u oud · pbs-main · 4,1 GB           te oud   nog niet gecontroleerd
Ook bewaken
  clusterforge (VMID 105)   5 u oud · pbs-main · 1,3 GB   vers
Niet in een back-upjob
  db-test01 (VMID 214)
```

Het rapport van een controle leest als "web01, back-up van 04-10-2026 01:00: geslaagd. Terugzetten 3 min 12 s, opstarten 41 s. Hostname web01, Debian 13, 2 bestandssystemen. Sandbox-VM 131 verwijderd om 04:07." Vanaf mijlpaal 16 komen daar de services bij en, waar die draaien, PostgreSQL of MariaDB.

ClusterForge plant zelf geen back-ups en beheert geen bewaartermijnen; dat blijven de Proxmox-back-upjobs en PBS-prune, en er komt geen knop om een back-up te maken. LXC-containers krijgen alleen versheid en dekking, want ze hebben geen guest agent. Terugzetten over de originele VM, naar een andere Proxmox-koppeling of van een heel cluster tegelijk valt buiten fase 2.

### Hoe het werkt

**Inventaris en versheid.** De Proxmox-sync leest elke 15 minuten de back-upvolumes van elke storage met content backup, met tijd, grootte, formaat, notes, protected en de verificatiestatus van PBS, en daarnaast `/cluster/backup-info/not-backed-up`. Het bestaande `POST /proxmox/{id}/sync` ververst de back-ups meteen mee. Per node, en per VMID op de lijst "ook bewaken", geldt een versheidsregel: de nieuwste back-up is hoogstens 30 uur oud, instelbaar per cluster. Een overgang naar vers, te oud of ontbrekend geeft precies één event. De eerste berekening na de uitrol schrijft de stand weg zonder event, zodat er geen stroom meldingen komt. Een lege lijst is verdacht: Proxmox laat back-upvolumes zonder melding weg als het token geen VM.Backup en Datastore.AllocateSpace heeft, en de rol uit de README van fase 1 had VM.Backup niet. Geeft een storage met content backup nul volumes terwijl er gekoppelde VM's zijn, dan schrijft de sync één backup.inventory_failed met de hint "heeft het token VM.Backup?" in plaats van missing per node. De README noemt VM.Backup voortaan als vereist om back-ups te lezen.

**Los van de clusterstatus.** De statusregels van node en cluster blijven ongewijzigd, want een back-up zegt niets over de beschikbaarheid van nu. De back-upstand is een apart veld in de cluster- en nodelijst; het cluster krijgt de slechtste stand van zijn nodes. De health score kan die later meewegen.

**De controle als taak.** Nu controleren maakt een run in test_runs en een taak backup.verify in het testslot, zodat er nooit twee sandboxes of een sandbox naast een failovertest draaien. Omdat de controle de productie-VM niet aanraakt, telt ze niet mee in het clusterslot. De taak heeft zes vaste stappen.

1. **Kiezen.** De nieuwste back-up of de gevraagde, met ExtractConfig voor schijven, geheugen en netwerkkaarten. Terugzetten gebeurt binnen dezelfde Proxmox-koppeling. Staat de back-up op storage van één host, dan op die host; bij PBS of NFS op de host met het meeste vrije geheugen. De sandbox-storage is de storage met content images en de meeste vrije ruimte, of een globaal ingestelde. Zou die storage daarna boven 85 % komen, of heeft de host niet het geheugen van de VM plus 1 GiB vrij, dan wordt de run skipped.
2. **Terugzetten.** NextID, dan eerst een rij met state reserved in het sandbox-register, en pas daarna `POST /nodes/{n}/qemu` met archive, storage en pool=cf-sandbox. Nooit force en nooit start.
3. **Isoleren.** link_down=1 op elke netwerkkaart, onboot=0 en protection=0. Daarna geldt een allowlist van configsleutels: schijven op storage, netX, cores, memory, cpu, machine, bios, boot, agent, smbios1, vmgenid, ostype, scsihw, vga, serial, de cloud-init-sleutels, tags en description. Een onbekende sleutel, zoals virtiofs0, geeft skipped "niet te isoleren" en meteen opruimen. Naam en cloud-init-sleutels blijven staan, omdat cloud-init de hostname bij elke start uit de VM-naam zet (internal/deploy/run.go:218 en 299-320). De sandbox herken je aan pool cf-sandbox, de tag cf-sandbox, de description en het SMBIOS-serienummer cf-sandbox; de bestaande uuid in smbios1 blijft. De taak leest de config terug en start alleen als elke regel klopt.
4. **Starten.** Power start, en de guest agent pollen tot de opstarttijdslimiet (globaal, standaard 600 s).
5. **Controleren.** get-host-name, get-osinfo en get-fsinfo via de guest agent, en vanaf mijlpaal 16 ook cf-agent verify. Heeft de VM geen guest agent in zijn config, dan wordt de uitkomst warning: alleen terugzetten en starten zijn dan gecontroleerd.
6. **Opruimen.** Altijd, ook na falende controles: protection eraf, hard stoppen en verwijderen via de bewaakte functies, alleen met purge=1. Faalden er controles, dan eindigt de run daarna met fail en "back-up afgekeurd: N controles mislukt".

backup.verify is niet Retryable en wordt ook niet hervat. Start de taak na een herstart van de server opnieuw (Attempts groter dan 1), dan gaat ze meteen naar opruimen en eindigt de run met error "onderbroken door herstart van de server". VMID, host en tijden staan daarom in backup_sandboxes en test_runs, niet in de stapstate. De hele taak heeft een limiet van 2 uur. Na elke taak via OnFinished, bij het starten van de server en elke 5 minuten verwijdert een opruimer sandboxes waarvan de run klaar is, de taak niet meer loopt of de rij ouder is dan 3 uur. destroy_failed probeert hij elke ronde opnieuw. Bestaat het VMID niet in Proxmox of zit het niet in cf-sandbox, bijvoorbeeld na een crash tussen NextID en het terugzetten, dan sluit hij de rij met state none en één logregel, zonder alarm.

**Isolatie eerst met de hand bewijzen.** link_down haalt de carrier weg. Op de golden image (Debian 13 genericcloud met DHCP) krijgt de VM dan geen adres en faalt de wait-online-unit meestal na een time-out. Vóór de bouw van mijlpaal 8 zet Jonas daarom één back-up met de hand terug met link_down, om te zien wat er opstart. Een afgesloten bridge (bijvoorbeeld vmbr99 zonder poorten en zonder hostadres) komt pas als die proef laat zien dat link_down niet volstaat.

**Events.** In de eventcatalogus registreert de module onder de soort Back-ups: backup.fresh, backup.stale, backup.missing, backup.inventory_failed en backup.inventory_recovered, backup.policy_updated, backup.watch_updated, backup.sandbox_destroy_failed en een event voor een tweede agentverbinding tijdens een controle. De uitkomst zelf staat in test_runs.

### Datamodel

Alles komt in nieuwe migraties; bestaande migraties blijven ongewijzigd. Sandbox-storage en opstarttijdslimiet zijn globale instellingen, geen kolommen per cluster.

| Tabel | Kolommen | Waarom |
| --- | --- | --- |
| proxmox_backups | connection_id en volid (samen PK), storage, pve_node, vmid, guest_type, ctime, size_bytes, format, notes, protected, verify_state, synced_at; index (connection_id, vmid, ctime DESC) | Kopie van de back-upvolumes, zoals proxmox_resources dat is voor /cluster/resources. Pagina's bevragen Proxmox niet, en de taak kiest hieruit het volid. |
| proxmox_connections (bestaand) | backup_checked_at, backup_error, not_backed_up (jsonb, NULL als Proxmox dat niet liet lezen) | De laatste poging om de back-ups te lezen. Een fout maakt de stand van die koppeling onbekend in plaats van ontbrekend. |
| backup_status | connection_id en vmid (samen PK), freshness (ok, stale, missing), latest_backup_at, changed_at; mijlpaal 8 voegt last_verified_at, last_result en last_run_id toe | De laatst berekende stand per bewaakte VM, of die nu bij een node hoort of op de lijst "ook bewaken" staat: precies één event per overgang. Onbekend wordt bij het lezen berekend en staat hier niet. |
| backup_watch | connection_id en vmid (samen PK), label, created_at | VM's zonder node, met ClusterForge zelf als eerste, krijgen dezelfde regel en badge. |
| backup_policies | cluster_id PK, max_age_hours (standaard 30, 1 tot 720), updated_at, updated_by; mijlpaal 11 voegt verify_enabled (standaard uit) en next_run_at toe | Eén rij per cluster; zonder rij gelden de standaarden, met de planning uit. |
| backup_sandboxes | id, connection_id, vmid, source_vmid, run_id, volid, state (reserved, present, destroyed, destroy_failed, none), host, storage, created_at, destroyed_at; partieel uniek op (connection_id, vmid) zolang de state reserved, present of destroy_failed is | Het sandbox-register: de enige bron voor de bewaakte functies en de opruimer. |
| test_runs (bestaand) | kind backup.verify; definition met volid, back-uptijd en grootte; checks; timeline; measurements met terugzet-, opstart- en controletijd in seconden | Het rapport, op dezelfde pagina als een failovertest. |

### Agent

De agent verandert pas in mijlpaal 16, met een nieuwe release zonder hoger protocol. `cf-agent verify -` leest een getypte VerifyRequest van stdin en schrijft een VerifyResult als JSON naar stdout. Exitcode 0 geldt ook als controles falen, 3 betekent een ongeldige aanvraag, en er komt altijd JSON met protocol_version. Een oude agent geeft exit 2 zonder JSON, en de server maakt daar warning "agent te oud" van. De limiet is 60 s per controle en 5 minuten in totaal.

| Controle | Wat de agent uitvoert | Uitkomst |
| --- | --- | --- |
| Service | `systemctl show`, moet active zijn | Niet geïnstalleerd geeft warning, net als een bekende clusterdienst die zonder peers niet start (Galera, Patroni), met uitleg. |
| Gefaalde units | `systemctl --failed --plain --no-legend` | Negeert standaard systemd-networkd-wait-online, NetworkManager-wait-online en networking.service. |
| TCP en HTTP | Alleen naar 127.0.0.1 en ::1 | Een dienst die faalt omdat hij aan zijn eigen adres bindt, geeft warning. |
| PostgreSQL | `runuser -u postgres -- psql -XAtq` met vaste queries: bereikbaar, databases, `SELECT 1` per database, herstelmodus | Peer-authenticatie via de socket, zonder wachtwoord. |
| MariaDB | `mariadb-admin ping` en `mariadb -N -e 'SHOW DATABASES'` | Idem. |

Databasenamen moeten voldoen aan `^[A-Za-z_][A-Za-z0-9_]{0,62}$`, en er komt nooit vrije SQL of shell uit de aanvraag. De verwachte services komen uit de service-stappen van de template, via de gewenste staat met vaste templateversie, plus de laatste facts van de productienode. Er komt geen verify-sectie in templates.

**Sandbox-merkteken.** `cf-agent run` leest bij het starten /sys/class/dmi/id/product_serial. Staat daar cf-sandbox, dan logt hij dat, verbindt hij niet met NATS en wacht hij tot hij gestopt wordt. Diepe controles en het merkteken werken alleen als de back-up deze agent al bevat. Daarom noemt de webinterface de stappen: agent bijwerken met `install.sh --upgrade`, golden image opnieuw bouwen, en een nieuwe back-up laten maken. verify.run als NATS-commando op live nodes schuift naar fase 3.

### API

| Methode | Pad | Doel |
| --- | --- | --- |
| GET | `/api/v1/backups` | Overzicht per cluster en node, plus "ook bewaken": versheid, nieuwste back-up, laatste controle met hersteltijd, VM's zonder back-upjob en bestaande sandboxes (viewer). |
| GET | `/api/v1/nodes/{nodeId}/backups` | De back-ups van de VM van deze node en de laatste controles (viewer). |
| POST | `/api/v1/nodes/{nodeId}/backups/verify` | Een controle starten, optioneel met een gekozen volid. Geeft 202 met run en taak, of 409 als het testslot bezet is (admin). |
| GET | `/api/v1/test-runs?kind=backup.verify` | Controles met filters op cluster, node en uitkomst, nieuwste eerst; gedeeld met de failovertests (viewer). |
| GET | `/api/v1/test-runs/{runId}` | Het rapport: uitkomst, controles, tijden, back-up, sandbox en taak (viewer). |
| POST | `/api/v1/backup-sandboxes/{sandboxId}/cleanup` | De sandbox nu verwijderen langs dezelfde bewaakte weg, bijvoorbeeld na destroy_failed (admin). |
| GET, PUT | `/api/v1/clusters/{clusterId}/backup-policy` | Maximale leeftijd en planning aan of uit, met de volgende run (lezen viewer, wijzigen admin). |
| PUT | `/api/v1/proxmox/{proxmoxId}/backup-watch` | De lijst "ook bewaken": VMID's met een label (admin). |

Annuleren gaat via het bestaande `POST /api/v1/jobs/{jobId}/cancel`, waarna OnFinished de sandbox aan de opruimer geeft. Het bestaande `POST /api/v1/proxmox/{proxmoxId}/sync` ververst ook de back-ups, en `POST /api/v1/proxmox/{proxmoxId}/vms/{vmid}/actions` geeft 409 voor een sandbox-VM.

### Schermen

- **Back-ups (/back-ups).** Een nieuw menu-item. Bovenaan staan drie tegels: nodes met een verse back-up (x van y), nodes die de laatste 30 dagen met succes gecontroleerd zijn, en de laatste afgekeurde controle. Daaronder staat per cluster en node de nieuwste back-up (leeftijd, storage, grootte, PBS-verificatie), de versheidsbadge, de laatste controle met een link naar het rapport, de gemeten hersteltijd en voor admins de knop Nu controleren. Dan volgen de blokken Ook bewaken (met toevoegen voor admins) en Niet in een back-upjob, en een blok Sandboxes dat alleen verschijnt als er een sandbox bestaat of verwijderen mislukte, met VMID, host, leeftijd en Opruimen. Onderaan staat de geschiedenis van de controles.
- **Rapport.** De gedeelde rapportpagina van test_runs toont node, back-up (volid, datum, grootte) en de uitkomst als grote badge, een tijdlijn met terugzetten, opstarten, controles en totaal, de tabel met controles, de sandboxgegevens (VMID, host, storage, isolatie, verwijderd om) en een link naar de taak. Markdown-download en printopmaak komen met de auto-documentatie van fase 3.
- **Clusterdetail.** Een kaart Back-ups met per node de versheid en de laatste controle, de maximale leeftijd en vanaf mijlpaal 11 de planning aan of uit met de volgende run.
- **Nodedetail.** Een kaart Back-ups met de back-ups van de VM, de laatste controles en de knop Back-up controleren, standaard op de nieuwste en met een andere te kiezen. Is de agent te oud voor de diepe controle, dan staan de drie stappen erbij met het commando om te kopiëren.
- **Elders.** De cluster- en nodelijst krijgen een kleine back-upbadge, en live.ts ververst ook de query-key backups. Lopende controles staan gewoon bij Taken, en de Proxmox-pagina toont bij een sandbox-VM geen actieknoppen.

### Veiligheid

Dit is de module met het grootste risico op productieschade in fase 2: ze start een exacte kopie van een productieserver, met hetzelfde IP- en MAC-adres, dezelfde keepalived-configuratie en VIP en dezelfde agent-nkey, en ze maakt en verwijdert VM's met een token op /.

- **Isolatie vóór de eerste start.** De taak start pas na het teruglezen van de config, als elke netwerkkaart link_down heeft, alle sleutels op de allowlist staan en het VMID volgens Proxmox in pool cf-sandbox zit. pvefake krijgt delete= in setConfig en een logregel voor GET config, zodat een test de volgorde POST config, GET config, POST status/start echt bewijst.
- **Geen tweede agent met dezelfde identiteit.** Een kopie die toch het netwerk bereikt, zou als de bronnode binnenkomen en diens commando's meekrijgen. Zolang de sandbox bestaat, weigert Bus.Check een tweede gelijktijdige verbinding met de nkey van de bronnode; herstart de productienode intussen, dan is hij hooguit 90 s later terug. Stijgt het aantal verbindingen toch, dan zet de taak de sandbox hard uit en eindigt de run met "controle afgebroken: tweede verbinding". Vanaf mijlpaal 16 weigert een nieuwe agent in de sandbox ook zelf te verbinden.
- **Nooit een productie-VM raken.** Het VMID komt uit NextID, Restore heeft geen force-parameter, en starten, verwijderen en exec lopen alleen via het sandbox-register en de bewaakte Proxmox-functies. De bestaande VM-acties en het koppelen aan een node weigeren een sandbox-VM, en één go/ast-test controleert wie Destroy en AgentRunVerify aanroept.
- **Geen vrije shell.** De enige exec is AgentRunVerify, met de vaste argv `/usr/local/bin/cf-agent verify -` in de client, en AgentInfo kent alleen get-host-name, get-osinfo en get-fsinfo. De aanvraag is getypte data, en poort- en HTTP-controles gaan alleen naar loopback. Zo blijven getypte acties de regel, ook al loopt het transport via de guest agent in plaats van NATS.
- **Belasting en ruimte.** Het testslot houdt het bij één sandbox tegelijk, en geplande controles draaien 's nachts in het testvenster. De regel van 85 % voorkomt dat een volle LVM-thin- of ZFS-pool ook de productie-VM's op die pool laat vastlopen. Overlapt het testvenster met een back-upjob uit /cluster/backup, dan waarschuwt de pagina, want een sandbox kan anders mee geback-upt en vergrendeld worden.
- **Rechten in Proxmox, eerlijk.** Wie het token heeft, is via VM.GuestAgent.FileWrite op / al root op elke VM met guest agent (README.md:112), dus de code is de echte grens. Mijlpaal 16 geeft VM.GuestAgent.Unrestricted alleen op /pool/cf-sandbox (PVE 9). Op PVE 8 valt exec onder VM.Monitor op /, en de README zegt dat. Poollidmaatschap komt uit het veld pool van /cluster/resources, dus Pool.Audit is niet nodig.
- **Gegevens en rollen.** De kopie van productiedata blijft op Proxmox en wordt verwijderd; het rapport bevat alleen namen, aantallen en statussen. Alleen admins starten, annuleren en ruimen op, viewers lezen. Een controle vraagt geen bevestiging bij prod, want ze verandert niets aan de productie-VM.

### Mijlpalen

- **Mijlpaal 2, Back-ups zien en vers houden.** Levert een nieuwe migratie met proxmox_backups, backup_status, backup_watch en backup_policies met de maximale leeftijd per cluster, de inventaris in de Proxmox-sync met inventory_failed bij een lege lijst, de pagina Back-ups, de badges en de kaarten op node- en clusterdetail.
- **Mijlpaal 8, Back-up terugzetten en controleren in een sandbox.** Levert backup_sandboxes, de Proxmox-uitbreidingen (ExtractConfig, Restore, Destroy, AgentInfo, het veld pool), de taak backup.verify met controles aan de kant van Proxmox, de opruimer, de weigeringen in Bus.Check en de VM-acties en het rapport in test_runs, voor elke back-up die er nu al is.
- **Mijlpaal 11, Planning in het testvenster en failover op prod.** Zet de controle per cluster gepland aan of uit, steeds voor de node die het langst niet gecontroleerd is, met de voorcontrole in de planner en "niet gecontroleerd sinds" op de clusterkaart.
- **Mijlpaal 16, Diepe back-upcontrole met cf-agent verify.** Levert cf-agent verify met de service-, unit-, poort- en databasecontroles, AgentRunVerify, het sandbox-merkteken, de rechten voor PVE 9 in de README en de stappen om agent en golden image bij te werken.

De module steunt op de eventcatalogus (mijlpaal 1), OnFinished (mijlpaal 3), de planner-lus en install.sh --upgrade (mijlpaal 5) en test_runs met het testslot (mijlpaal 7).

## Drift detection

Drift detection laat per node zien wat afwijkt van de gewenste staat, zoals een met de hand aangepast configuratiebestand, een uitgeschakelde dienst of een ontbrekend pakket. Voor een cluster uit een template is de gewenste staat precies wat de uitrol zou neerzetten, met de templateversie waarmee het cluster is uitgerold. Voor zijn bestaande, met de hand gebouwde clusters legt Jonas één keer een baseline vast. In de clusterlijst staat dan een gele badge naast de status, en op clusterdetail ziet hij bijvoorbeeld:

```text
Drift · template keepalived-nginx 1.0.0, spec-revisie 1 · laatste controle 14:15
web01   in orde
web02   drift · 2 afwijkingen · sinds 5 okt 14:12
  Bestand /etc/keepalived/keepalived.conf
    Verwacht: 0640, inhoud volgens template
    Werkelijk: inhoud wijkt af · 1.231 bytes in plaats van 1.204 · gewijzigd op de node om 14:10
  Service nginx
    Verwacht: enabled, active
    Werkelijk: disabled, active
Genegeerd (1): file:/var/www/html/index.html · "tijdelijke onderhoudspagina" · Jonas · tot 12 okt
```

Een afwijking kan hij negeren met een verplichte reden en een optionele einddatum. Bij een templatecluster kan hij ook herstellen: ClusterForge past dan alleen de afwijkende stappen opnieuw toe, node voor node. Een controle verandert nooit iets op de node, en herstellen gebeurt nooit vanzelf, zodat een hotfix tijdens een incident blijft staan tot Jonas beslist.

### Hoe het werkt

**Verwachte staat.** Voor een templatecluster rendert `Expected` de stappen van de node met `RenderNode` uit de gewenste staat met vaste templateversie, dus met dezelfde code, versie en geheimen als de uitrol. Ontbreekt die versie in de binary, een geheim of de masterkey, dan krijgt de node status `error` en is herstel niet mogelijk; er wordt nooit een nieuw geheim verzonnen. Heeft de server een nieuwere versie van de template, dan is dat alleen een melding, want bijwerken is een aparte wijziging van de spec. Voor een baselinecluster leest `Expected` de lijst `expect` van de node uit de baseline. Een node die niet in `spec.nodes` staat, krijgt `none`, en wijkt de set actieve nodes af van `spec.nodes`, dan krijgt het cluster de melding "lidmaatschap wijkt af van de spec".

**Waarnemen.** De server stuurt die stappen zonder inhoud als `state.inspect` naar de agent. Die antwoordt per stap met pakketten en hun versie, services (loaded, enabled, active), bestanden en mappen (bestaat, grootte, mode, eigenaar, mtime en sha256), gebruikers en het `creates`-pad van command-stappen. Een stap met `unless` krijgt "niet gecontroleerd", want die test is vrije shell en draait nooit tijdens een controle. Op een node zonder dpkg geldt hetzelfde voor pakketten.

**Vergelijken.** `Compare` is een pure functie met de betekenis van apply. Een bestand heeft standaard mode 0644 en een map 0755, de eigenaar telt alleen als de stap hem zet en `enabled` alleen als het gezet is, en state started, restarted of reloaded verwacht active. Een ontbrekend pakket is drift, net als een pakket met state absent dat toch geïnstalleerd is. Een pakketversie die achterloopt op een rolgenoot is geen drift, want herstel kan dat niet oplossen: de package-stap installeert alleen wat ontbreekt (`internal/agent/apply.go:90-98`). Bestanden vergelijkt de server in het geheugen, met de gerenderde inhoud bij een template en met de vingerafdruk bij een baseline. Elke afwijking krijgt een sleutel als `file:/etc/keepalived/keepalived.conf:content` of `service:nginx:enabled`, plus een eigen vingerafdruk over de waargenomen waarde (inhoud, mode, eigenaar, enabled, active).

**Scanner.** De scanner is een lus op de planner. Hij controleert elke actieve node met een agent op protocol 4 en een recente heartbeat elke 15 minuten (`CF_DRIFT_INTERVAL`, 0 zet hem uit), opnieuw na een uitrol, herstel of node-actie via OnFinished, en meteen bij de knop Nu controleren. Er lopen hoogstens 4 controles tegelijk, elk met 30 s tijd. Nodes in onderhoud, draining of provisioning slaat hij over zonder iets te schrijven, omdat onderhoud keepalived bewust stopt (`internal/agent/commands.go:180-216`); de laatste uitkomst blijft staan met haar leeftijd erbij. Een node met een wachtende of lopende taak krijgt "overgeslagen: taak bezig", want de agent doet één commando tegelijk en een apply mag 14 minuten duren. Een nieuwe of andere set afwijkingen wordt na 30 s opnieuw bekeken, en alleen wat beide keren afwijkt telt. De knop slaat die bevestiging over, zodat het antwoord binnen de WriteTimeout van 60 s blijft.

**Events alleen bij een overgang.** Scanner, knop en herstel schrijven `drift_checks` en de events in één transactie, onder een advisory lock per node zoals de evaluator (`internal/status/evaluator.go:83`), zodat er nooit een dubbel event komt. In de eventcatalogus komen onder de soort Drift: `drift.detected` (van in_sync of none naar drift, met hoogstens 20 sleutels), `drift.changed` (een andere set sleutels, met wat erbij kwam en wegviel), `drift.resolved` (weer in_sync, met de duur en de `job_id` van een herstel), `drift.check_failed` (naar error, niet bij een onbereikbare agent), en voor de acties van een admin `drift.ignore_added`, `drift.ignore_removed` en `drift.baseline_set`.

**Drift is geen storing.** De statusregels veranderen niet. Drift is een aparte waarschuwing en maakt een cluster niet degraded, anders staat een cluster met blijvende drift altijd op degraded en valt een echte storing niet meer op. Een gestopte dienst staat in beide: de status zegt dat hij niet werkt, drift dat hij afwijkt van de spec.

**Baseline.** Jonas kiest nodes, pakketten, services en bestanden, en ClusterForge legt met `state.inspect` vast wat er nu staat, als spec-revisie van soort baseline met vingerafdrukken in plaats van hashes. Heeft hij een node bewust veranderd, dan legt hij de baseline voor die node opnieuw vast, wat een nieuwe revisie geeft. Zonder masterkey legt een baseline geen bestanden vast. Een baselinecluster krijgt in fase 2 geen herstel, want van bestanden kent de baseline alleen een vingerafdruk.

**Negeren.** Een negeerregel geldt voor een hele stap (`file:/pad`, `service:naam` of `package:naam`), op één node of op het hele cluster. Een sleutel die op `*` eindigt, matcht als voorvoegsel, en `*` alleen met een einddatum pauzeert het cluster. Aspecten zoals `:mode` zijn alleen weergave, want herstel past hele stappen toe: een file-stap schrijft altijd de volledige inhoud en een service-stap doet enable en start samen (`apply.go:204-222` en `247-255`). Een bewust aangepaste inhoud zou anders bij het herstel van de mode alsnog overschreven worden.

**Herstel.** Herstel is `cluster.apply` in de modus herstel. De taak krijgt per node de gekozen afwijkende stappen zonder genegeerde stappen, met hun notify-handlers en de vingerafdrukken die Jonas zag. Aan het begin van elke nodestap inspecteert de taak de node opnieuw en stopt ze zonder iets toe te passen als een vingerafdruk veranderd is, ook als iemand hetzelfde bestand een tweede keer wijzigde. Na elke node volgt de gezondheidspoort. Herstel kan alleen bij een templatecluster waarvan het lidmaatschap klopt; anders zou het `unicast_peer` herschrijven en een met de hand toegevoegde node buitensluiten.

### Datamodel

`drift_checks` komt in mijlpaal 5 en `drift_ignores` in mijlpaal 6, elk in een nieuwe migratie. Clusters en nodes krijgen geen nieuwe kolommen: ListClusters, GetCluster en ListNodes rekenen de samenvatting uit met een subquery, zoals `node_count`.

| Tabel | Kolommen | Waarom |
| --- | --- | --- |
| drift_checks | `node_id` (sleutel, verwijderd met de node), `status` (in_sync, drift, error, none), `source` (template, baseline), `spec_revision`, `template_version`, `findings` (jsonb), `fingerprint`, `error`, `checked_at`, `drift_since` | Laatste uitkomst per node, zoals node_facts; de events zijn de geschiedenis. `fingerprint` hasht de actieve sleutels en bepaalt of `drift.changed` nodig is. Er is geen `cluster_id`: de samenvatting joint via `nodes.cluster_id`, en een rij vervalt als de node van cluster wisselt. |
| drift_ignores | `id`, `cluster_id`, `node_id` (leeg is het hele cluster), `key`, `reason` (verplicht, 3 tot 500 tekens), `expires_at` (optioneel), `created_by`, `created_at` | Bewuste uitzonderingen met reden, wie en tot wanneer. Een verlopen regel blijft zichtbaar, maar telt niet meer. |
| cluster_spec_revisions | Een baseline is een spec `{"kind":"baseline","nodes":[{"node_id","hostname","role","expect":[...]}]}`, met per bestand mode, eigenaar en `content_hmac` | Historie en wie-wijzigde-wat zijn er meteen, `cluster.spec_changed` komt vanzelf, en GitOps kan hetzelfde formaat later lezen. |

Een element van `findings` heeft `key`, `kind`, `title`, `expected`, `actual`, `detail`, `since`, `ignored`, `ignore_id` en de vingerafdruk van de waargenomen waarde. `expected` en `actual` zijn korte teksten zoals "enabled, active". Bij een bestand staan grootte, mtime, mode en eigenaar erbij, nooit inhoud of een ruwe hash.

### Agent

De agent krijgt protocol 4 voor `state.inspect`. Bestaande agents komen erop met `install.sh --upgrade`, en voor nieuwe uitrollen wordt de golden image opnieuw gebouwd.

- **Protocol.** `pkg/protocol` krijgt de action `state.inspect` met een eigen constante naast `ApplySince`, en een eigen requesttype per stap zonder `Content`. `Result` krijgt `Observations`, één per stap in dezelfde volgorde, met de velden uit Waarnemen plus `Skipped` en `Error`. `ApplySteps` accepteert alleen `templates.Step`, zodat een gestripte stap nooit naar apply kan.
- **Gedeelde leesfuncties.** `apply.go` wordt gesplitst in leesfuncties die apply en het nieuwe `inspect.go` allebei gebruiken: `packageStates` (één `dpkg-query` met `${Version}` voor alle namen), `unit` (`systemctl show`), `fileState`, `dirState` en `userExists` (`id -u`). `fileState` volgt symlinks zoals apply, hasht alleen gewone bestanden tot 16 MiB en meldt een symlink alleen als informatie. Paden gaan door de bestaande controle `a.path`.
- **Grenzen.** `state.inspect` heeft 30 s tijd in plaats van 2 minuten en stuurt na afloop geen facts. De NATS-rechten veranderen niet, want het commando gebruikt hetzelfde cmd-subject.
- **Tests.** `agenttest.Host` krijgt pakketversies (`SetVersion`). De test voor inspect vergelijkt naast `Calls()` een momentopname van `h.Root` (inhoud, mode, eigenaar, mtime). Een consistentietest past elke stap van de ingebouwde templates toe, inspecteert en vergelijkt, en eist nul afwijkingen, zodat apply en controle niet uit elkaar kunnen lopen.

### API

| Methode | Pad | Doel |
| --- | --- | --- |
| GET | `/api/v1/clusters/{clusterId}/drift` | Bron (template met uitgerolde versie, of baseline met revisie), meldingen over een nieuwere templateversie of afwijkend lidmaatschap, en per node status, laatste controle, `drift_since`, afwijkingen en genegeerde afwijkingen. Ook voor viewers. |
| GET | `/api/v1/nodes/{nodeId}/drift` | Hetzelfde voor één node. Ook voor viewers. |
| POST | `/api/v1/clusters/{clusterId}/drift/check` | Nu controleren (admin). Synchroon, alle actieve nodes parallel met elk 20 s, zonder bevestiging na 30 s. Het antwoord heeft de vorm van de GET. |
| POST | `/api/v1/nodes/{nodeId}/drift/check` | Eén node nu controleren (admin). |
| GET | `/api/v1/clusters/{clusterId}/drift/ignores` | Negeerregels met reden, wie, wanneer en tot wanneer (mijlpaal 6). |
| POST | `/api/v1/clusters/{clusterId}/drift/ignores` | Negeerregel maken (admin, mijlpaal 6) met `node_id` (optioneel), `key`, `reason` en `expires_at` (optioneel). Hij geldt meteen. |
| DELETE | `/api/v1/drift-ignores/{ignoreId}` | Negeerregel opheffen (admin, mijlpaal 6). |
| POST | `/api/v1/clusters/{clusterId}/drift/baseline` | Baseline vastleggen, of met `node_ids` opnieuw vastleggen voor die nodes (admin, mijlpaal 6). Geeft een nieuwe spec-revisie en weigert bij een templatecluster. |
| POST | `/api/v1/clusters/{clusterId}/drift/remediate` | Herstel aanvragen (admin, mijlpaal 10) met `node_ids`, per node de stapsleutels met de getoonde vingerafdrukken, en `confirm`. Geeft 202 met de taak, of 409 bij een bezet clusterslot, een node die niet active is, afwijkend lidmaatschap, een baselinecluster of een ontbrekende bevestiging bij prod. |
| GET | `/api/v1/clusters`, `/api/v1/clusters/{id}`, `/api/v1/nodes` | Bestaand. Een cluster krijgt `drift` met `status` (in_sync, drift, unknown), `nodes_with_drift` en `checked_at`, een node krijgt `drift_status`. |

### Schermen

- **Clusterlijst en overzicht.** Naast de statusbadge staat een gele badge "Drift · 2 nodes", of een grijze "drift onbekend" als de laatste controle ouder is dan een uur. De badge linkt naar clusterdetail.
- **Kaart Drift op clusterdetail.** De kop toont de bron ("Template keepalived-nginx 1.0.0, spec-revisie 1" of "Baseline van 4 oktober, revisie 3") en de laatste controle. Voor een admin staan er de knoppen Herstellen… en Nu controleren. Een nieuwere templateversie geeft de melding "Deze server kent ook keepalived-nginx 1.1.0; dit cluster wordt vergeleken met 1.0.0, de versie waarmee het is uitgerold." Daaronder staat per node de hostname, een badge (in orde, drift, fout, geen verwachte staat, overgeslagen), het aantal afwijkingen en de laatste controle. Klapt Jonas een node open, dan ziet hij de afwijkingen zoals in het voorbeeld hierboven, en bij elke afwijking de actie Negeren… met een verplichte reden en een optionele einddatum. Een ingeklapte sectie "Genegeerd (n)" toont reden, wie en tot wanneer, met een knop om de regel op te heffen.
- **Zonder verwachte staat.** Een handmatig cluster zonder baseline krijgt een korte uitleg en de knop Baseline vastleggen.
- **Kaart Drift op nodedetail.** Dezelfde component voor één node, naast AgentCard en NodeLifecycleCard. Is de agent te oud, dan staat er "agent te oud voor driftcontrole" met het upgradecommando om te kopiëren.
- **Baselinewizard (mijlpaal 6).** Stap 1 kiest nodes, standaard alle actieve. Stap 2 kiest pakketten, services en bestanden, vooraf ingevuld per clustertype: bij keepalived het pakket keepalived en `/etc/keepalived/keepalived.conf`, bij nginx het pakket nginx en `/etc/nginx/nginx.conf`, bij docker `/etc/docker/daemon.json` en bij cron `/etc/crontab`, met de services uit de facts. Stap 3 toont per node versies, mode en eigenaar, zonder hashes, en slaat op. Voor één node doorloopt Jonas dezelfde wizard om opnieuw vast te leggen.
- **Herstelvenster (mijlpaal 10).** Per node staat in gewone zinnen wat er gebeurt, zoals "bestand /etc/keepalived/keepalived.conf overschrijven, daarna keepalived herladen" of "service nginx enablen en starten", in de volgorde van de taak met de VIP-eigenaar als laatste, plus de waarschuwing dat genegeerde punten blijven staan. Vanaf mijlpaal 12 noemt het venster ook de diensten die geraakt kunnen worden. Op prod volgt de bevestiging bij prod, en daarna opent het bestaande taakdetail met de live voortgang.
- **Data.** `web/src/lib/drift.ts` gebruikt de query keys `["clusters", id, "drift"]` en `["nodes", id, "drift"]`, zodat de bestaande SSE-hook ze vanzelf ververst.

### Veiligheid

- **Alleen lezen.** Een controle is altijd `state.inspect`, en de server stuurt dat alleen naar een agent met protocol 4. De agent voert daarbij alleen `dpkg-query`, `systemctl show`, `id` en `stat` uit en leest bestanden; de tests met `Calls()` en de momentopname van `h.Root` bewaken dat.
- **Geen inhoud en geen te raden hash.** De agent krijgt geen bestandsinhoud en geeft er geen terug. Database, events en API bevatten alleen vingerafdrukken, grootte, mode, eigenaar en mtime, en een test zoekt het `auth_pass`-geheim en de sha256 van `keepalived.conf` in elk antwoord, event en databaserij. Verschil tonen komt er in fase 2 niet: Jonas heeft SSH, en maskeren zou geheimen missen die ClusterForge niet kent, zoals een met de hand gewijzigd `auth_pass`.
- **Geen vals alarm.** De vaste templateversie en de clusterkopie in de spec zorgen dat een serverupdate of een nieuwe clusternaam geen drift op elke node geeft. Onderhoud, lopende taken en de bevestiging na 30 s houden tijdelijke toestanden buiten de telling, en een node buiten de spec krijgt `none`.
- **Een mens beslist over herstel.** Er is geen automatisch herstel. Herstellen kan alleen een admin, met de bevestiging bij prod en via het clusterslot, zodat het nooit samenvalt met een uitrol, node-actie of Proxmox-actie in hetzelfde cluster. De taak is niet Retryable; opnieuw proberen is een nieuw herstel vanuit de actuele weergave.
- **Herstel raakt alleen wat Jonas zag.** Een veranderde vingerafdruk stopt de nodestap, een genegeerde stap wordt nooit toegepast, een ontbrekend geheim is een fout in plaats van een nieuw VRRP-wachtwoord, en de gezondheidspoort stopt de taak vóór de VIP-eigenaar als een eerdere node niet gezond terugkomt.
- **Belasting.** Een controle is één `dpkg-query`, een paar keer `systemctl show` en een paar bestanden, met hoogstens 4 nodes tegelijk en 30 s per node. De scanner wacht nooit achter een lopende apply.
- **Rechten.** Viewers zien drift, want er staan geen geheimen in. Controleren, negeren, een baseline vastleggen en herstellen geven voor hen 403. Elke schrijvende actie geeft een event met de gebruiker als actor, en de root-acties van een herstel staan als `agent.command` onder de taak in het logboek.

### Mijlpalen

- **Mijlpaal 4, Gewenste staat met vaste templateversie.** Levert de templates in versiemappen, de getypte spec met `node_id`, index en clusterkopie, `RenderNode` buiten de uitroltaak, de strikte modus voor geheimen en de lidmaatschapscontrole, de basis waartegen drift vergelijkt.
- **Mijlpaal 5, Drift zien voor clusters uit een template.** Levert `state.inspect` met protocol 4 en `install.sh --upgrade`, `drift_checks`, `Expected` en `Compare`, de scanner, de gele badge, de kaarten Drift en de meldingen over lidmaatschap en een te oude agent, allemaal zonder iets op een node te veranderen.
- **Mijlpaal 6, Baseline en negeren: drift voor bestaande clusters.** Levert de baselinewizard met opnieuw vastleggen per node en `drift_ignores` met reden, voorvoegsels en einddatum, zodat drift ook op Jonas' met de hand gebouwde clusters werkt.
- **Mijlpaal 10, Veilige uitrol en drift herstellen.** Levert herstel als `cluster.apply` in de modus herstel, met selectie op stapniveau, een vingerafdruk per afwijking, het herstelvenster en de bevestiging bij prod, alleen voor templateclusters.

Mijlpaal 3 legt daarvoor het clusterslot, OnFinished en de afgeleide sleutel voor vingerafdrukken klaar. Later gebruikt mijlpaal 13 de consistentietest voor de nieuwe templates, en toont mijlpaal 14 met dezelfde inspectie in een GitOps-plan welke bestanden op de nodes al afwijken.

## Failovertests

Met failovertests weet Jonas of de reserve-node het VIP echt op tijd overneemt, voordat een echte storing het hem vertelt. ClusterForge stopt met opzet keepalived of de dienst op de node die nu het VIP heeft, meet vanaf de server hoe lang het VIP onbereikbaar is en welke node het overneemt, en zet daarna altijd alles terug. Op clusterdetail ziet hij per test het laatste resultaat:

```text
Test                               Verwacht                        Laatste resultaat         Planning
keepalived stoppen op de eigenaar  10.0.20.10 binnen 5 s elders    PASS 3,4 s · 04-10-2026   testvenster
nginx stoppen op de eigenaar       10.0.20.10 binnen 10 s elders   FAIL 12,8 s · 27-09-2026  handmatig
```

Een klik op het resultaat opent het rapport, in de vorm die Jonas zelf gebruikt:

```text
Test:      keepalived stoppen op web-01
Verwacht:  VIP 10.0.20.10 binnen 5 s op een andere node
Resultaat: PASS, 3,4 s onbereikbaar, overgenomen door web-02, daarna terug op web-01, alles hersteld
```

Fase 2 veroorzaakt één storing op één node. Vanaf mijlpaal 7 kan dat keepalived of een dienst stoppen zijn, in lab en test. Vanaf mijlpaal 11 komen daar de VM hard uitzetten in lab en test, geplande tests in het testvenster en handmatige tests op prod bij. Databaseclusters krijgen geen failovertests, omdat een VIP dat naar een replica verhuist schrijfacties kan laten mislukken. Een node herstarten is geen apart scenario. Voor VRRP is dat hetzelfde als keepalived stoppen, want keepalived stopt netjes bij het afsluiten. Netwerk loskoppelen, meerdere storingen na elkaar en een agent die een storing zelf terugdraait, horen bij de disaster simulations van fase 3.

### Hoe het werkt

**Scenario's.** De standaardverwachtingen staan in de code, en Jonas past ze per test aan.

| Scenario | Storing | Herstel | Verwachting | Waar |
| --- | --- | --- | --- | --- |
| `keepalived_stop` | keepalived stoppen op de VIP-eigenaar | keepalived starten | 5 s | lab en test vanaf mijlpaal 7, prod vanaf mijlpaal 11 |
| `service_stop` | nginx of haproxy stoppen, zodat de vrrp_script-controle het VIP laat verhuizen | de dienst starten | 10 s (chk_nginx doet er al ongeveer 4 s over) | idem |
| `vm_hard_stop` | de VM van de VIP-eigenaar hard uitzetten via Proxmox | de VM starten met `proxmox.PowerAndWait` | 10 s | alleen lab en test, vanaf mijlpaal 11 |

**Eén storingspad, geen nieuwe agent.** Stoppen gebeurt met het bestaande `apply.steps` met één service-stap `stopped`, en herstellen met dezelfde stap met `started`. Dat is `systemctl stop` en `start` zonder enable te wijzigen (`internal/agent/apply.go:250-259`), dus na een reboot van de node draait de dienst altijd weer. De server vult alleen de units keepalived, nginx en haproxy in. De agents die nu draaien (protocol 3) werken meteen, en de agent verandert voor deze module niet. De code zit in een nieuw pakket `internal/failover`. Elk scenario heeft daar een kleine interface (Check, Inject, Clear), zodat fase 3 er scenario's bij kan zetten.

**Meten met een verplichte probe.** De server vraagt het VIP elke 250 ms op met een time-out van 400 ms. Dat gebeurt met een HTTP-pad en een verwachte status, of met de TCP-poort van de dienst. De onderbreking loopt van de eerste mislukte probe tot drie goede op rij, en dat is wat een client merkt. Een heartbeat komt maar elke 10 s (`pkg/protocol/protocol.go:82`) en bepaalt dus alleen welke node het VIP overnam. De standaardprobe komt uit de http-check van de template, gerenderd met `clusters.spec.params`. Kan de server het VIP niet rechtstreeks bereiken, dan start de test niet.

**PASS.** Drie dingen moeten kloppen. Een verse heartbeat toont het VIP op een andere node en niet meer op het doel, of het doel stuurde sinds de storing geen heartbeat meer. De onderbreking blijft onder de verwachting. De terugkeer lukt. Een te trage overname met geslaagd herstel is FAIL. Lukt het herstel niet, dan wordt het result error met restored false. Keepalived stoppen raakt alle VIP's op het doel, dus de taak controleert de overname voor elk van die VIP's.

**Voorcontrole.** Deze voorwaarden gelden voor elke run, zonder force-uitweg:
- het cluster is healthy en het VIP heeft precies één houder;
- een actieve peer met een verse heartbeat heeft dit VIP in `node_facts.keepalived.vips`;
- geen node van het cluster meldt postgresql, mariadb of mysql als actief, welk type het cluster ook heeft;
- alle actieve nodes hebben een heartbeat jonger dan 30 s, en het clusterslot en het testslot zijn vrij;
- de probe slaagt drie keer na elkaar;
- bij vm_hard_stop is de node aan een VM gekoppeld, staat die VM niet onder Proxmox HA en draait er geen docker.

`POST .../runs` doet de voorcontrole synchroon, zodat de API meteen 409 met de reden geeft. In één transactie maakt het de rij in test_runs en de taak via het clusterslot. De taak herhaalt de voorcontrole als eerste stap. Faalt ze dan alsnog, dan eindigt de run als skipped zonder storing.

**De taak `failover.test`** heeft vijf vaste stappen:
1. **Voorcontrole**, met een baseline in de stapstate: eigenaar, peers en alle VIP's van het doel.
2. **Storing en meten.** De server bewaart eerst het tijdstip en stuurt dan de storing met command-id `<run>-inject`. Een hervatte stap stuurt de storing niet opnieuw. De meting stopt zodra de probe drie keer na elkaar slaagt en een verse heartbeat de overname bevestigt. Ze stopt uiterlijk na 2 × de verwachting + 10 s, en nooit later dan na 120 s. Daarna volgt altijd herstel, ook als het VIP nooit verhuisde.
3. **Herstellen**: de service-stap `started`, of bij vm_hard_stop de VM starten en wachten op de Proxmox-taak.
4. **Terugkeer controleren** met de gezondheidspoort, dus alleen met waarnemingen die nieuwer zijn dan het herstelcommando. Dat moet binnen 3 minuten lukken, of bij vm_hard_stop binnen de BootTimeout van lifecycle. Bij `expect_failback` moet het VIP terug zijn bij de oorspronkelijke eigenaar. Dat staat standaard aan voor clusters uit keepalived-nginx, dat preempt gebruikt met prioriteit 150 en 140, en uit voor clusters zonder template. De probe meet ook de korte onderbreking bij die terugkeer.
5. **Rapport**: result, metingen, summary, timeline en restored in test_runs, en het event failover.finished.

**Altijd herstellen.** Annuleren na de storing kort alleen de meting in. Cancel breekt de context van een taak meteen af (`internal/jobs/jobs.go:209-214`). Daarom sturen herstel en terugkeer hun commando's met `context.WithoutCancel` en stoppen ze alleen bij `jobs.Interrupted`. De run wordt pas aan het eind canceled. Bij een fout of een nette stop van de server herstelt een defer met `context.WithoutCancel` en een limiet van 10 s. Dat kan omdat NATS open blijft tot serve() terugkeert (`cmd/clusterforge-server/main.go:182-195`). Na een harde crash hervat de taak. De meting is dan ongeldig, de run krijgt error 'meting onderbroken' en het herstel volgt. Via OnFinished krijgt een run zonder eindresultaat error, en restored false als de herstelstap niet geslaagd is. `failover.test` is niet Retryable. Bij restored false start de rode balk een kleine taak 'Opnieuw herstellen', die alleen herstelt en de terugkeer controleert.

**Planning (mijlpaal 11).** De planning staat per test aan of uit, standaard uit, en geplande tests draaien alleen in het testvenster. De voorcontrole vóór het maken van een taak, de overgeslagen runs en het testslot werken zoals bij de gedeelde bouwstenen beschreven. Een prod-test kan niet gepland worden.

### Datamodel

Mijlpaal 7 maakt in een nieuwe migratie failover_tests, test_runs en de index voor het testslot. Clusters, nodes en vips krijgen geen nieuwe kolommen. Scenario en service zijn tekst die de applicatie controleert, zoals clusters.type, zodat een nieuw scenario geen migratie vraagt.

| Tabel | Kolommen | Waarom |
| --- | --- | --- |
| failover_tests | id, cluster_id en vip_id (ON DELETE CASCADE), name, scenario, service (leeg, nginx of haproxy), max_takeover_seconds (1 tot 120), expect_failback, probe (jsonb), scheduled en next_run_at (vanaf mijlpaal 11), created_by, created_at, updated_at | De definitie per cluster. probe is `{"http":{"path":"/","expect":200}}` of `{"tcp":{"port":80}}` en bevat nooit een host, want de host is altijd het VIP. Een partiële index op next_run_at WHERE scheduled voor de planner. |
| test_runs (gedeeld) | kind `failover.test`; result pass, fail, error, skipped of canceled; node_id en hostname van het doel; definition met test_id, scenario, service, VIP-adres, verwachting en probe bij de start; checks met de voorcontrole; measurements met downtime_ms, failback_ms en de overnamenode; timeline als lijst van {t_ms, kind, text}; restored en summary | Eén rij is het rapport van één run. Een latere wijziging van de test verandert oude rapporten niet, en stappen en logs staan in de taak via job_id. |

### API

Lezen mag elke ingelogde gebruiker. Maken, wijzigen en uitvoeren mag alleen een admin.

| Methode | Pad | Doel |
| --- | --- | --- |
| GET | `/api/v1/clusters/{clusterId}/failover-tests` | De tests met hun laatste en volgende run, plus wat het formulier nodig heeft: welke scenario's nu kunnen (met reden, zoals 'geen peer met dit VIP' of 'geen Proxmox-koppeling'), de VIP's, de standaardverwachting en de standaardprobe. |
| POST | `/api/v1/clusters/{clusterId}/failover-tests` | Nieuwe test. Valideert scenario, VIP van dit cluster, unit uit de lijst, de grenzen en de probe. |
| GET, PATCH, DELETE | `/api/v1/failover-tests/{testId}` | Eén test lezen, wijzigen (ook planning aan of uit) of verwijderen. De runs blijven als geschiedenis. |
| POST | `/api/v1/failover-tests/{testId}/runs` | Nu uitvoeren: 202 met run en job_id. 409 met de reden bij een mislukte voorcontrole of een bezet slot. Op prod 409 prod_locked tot mijlpaal 11, daarna 409 needs_confirmation zonder confirm. |
| GET | `/api/v1/failover-tests/{testId}/runs` | Geschiedenis van één test, nieuwste eerst. |
| GET | `/api/v1/test-runs/{runId}` | Het rapport, gedeeld met de back-upcontrole: definitie bij de start, doel- en overnamenode, metingen, summary, timeline, restored en job_id. |
| POST | `/api/v1/test-runs/{runId}/restore` | 'Opnieuw herstellen' voor een failoverrun met restored false: een nieuwe taak door het clusterslot. |
| POST | `/api/v1/jobs/{jobId}/cancel` | Bestaand. Breekt een lopende test af; het herstel loopt altijd. |

### Schermen

- **Clusterdetail.** Een kaart Failovertests tussen VIP's en Taken. Per test toont ze het scenario in gewone taal, de verwachting, het laatste resultaat als PASS- of FAIL-badge met tijd en datum, en de planning. Een admin krijgt de knoppen 'Nu testen' en 'Bewerken'. Vanaf mijlpaal 11 tonen de clusterkaarten 'niet getest sinds'.
- **Rode balk.** Bij restored false staat bovenaan het cluster bijvoorbeeld 'Failovertest niet volledig hersteld: keepalived staat nog uit op web-01', met de knop 'Opnieuw herstellen'.
- **Formulier.** Scenario's die nu niet kunnen, staan grijs met hun reden. Verder vraagt het formulier het VIP, de verwachting in seconden (vooraf ingevuld), 'VIP moet daarna terug naar de oorspronkelijke node' en de probe (HTTP-pad en status, of TCP-poort). Vanaf mijlpaal 11 komt daar de planning bij, behalve op prod.
- **Bevestiging.** Het venster zegt bijvoorbeeld: 'keepalived wordt gestopt op web-01, nu eigenaar van 10.0.20.10 en 10.0.20.11. Verwacht: een andere node neemt binnen 5 s over. Lukt dat niet, dan is 10.0.20.10 hoogstens 20 s onbereikbaar; daarna zet ClusterForge keepalived weer aan.' Op prod is dit de bevestiging bij prod. Vanaf mijlpaal 12 noemt het venster ook de geraakte diensten.
- **Rapport.** Bovenaan de rapportpagina van test_runs staan Test, Verwacht en Resultaat. Daaronder staat de tijdlijn als horizontale balk: de probe in groen of rood, met markeringen voor storing, VIP-wissels, herstel en terugkeer. Onderaan staan de stappen en logs van de taak, live via SSE, met de knop 'Afbreken'.

Een aparte overzichtspagina met alle failovertests en een trend van de overnametijd komt niet in fase 2.

### Veiligheid

ClusterForge veroorzaakt hier met opzet uitval met root-rechten. De schade is daarom klein, vooraf begrensd en altijd terug te draaien.

- **Wie en waar.** Alleen een admin start een test. Lab en test kunnen vanaf mijlpaal 7. Op prod antwoordt de API tot mijlpaal 11 met 409 prod_locked. Daarna kan prod alleen met de hand, met de bevestiging bij prod, en alleen voor keepalived_stop en service_stop. Valt de server midden in zo'n test weg, dan blijft het VIP bereikbaar op de andere node. Alleen de reserve-node ontbreekt dan tot ClusterForge terug is, en het bevestigingsvenster zegt dat. vm_hard_stop komt nooit op prod: een hard gestopte VM heeft geen agent, en alleen de server zet hem weer aan.
- **Alleen geschikte clusters.** De voorcontrole heeft geen force-uitweg. Databases worden herkend aan de diensten die de agents melden, niet aan het clustertype, dat een vrij label is. vm_hard_stop weigert VM's onder Proxmox HA, omdat de HA-manager zou meespelen. Het weigert ook nodes waarop docker actief is, omdat containers met volumes data kunnen bevatten.
- **Begrensde schade.** Er is één storing op één node. Tijdens de test weigert het clusterslot node-acties, Proxmox-acties op VM's van het cluster, drift-herstel en GitOps. Zo kan niemand de node die het VIP net overnam stoppen of migreren. Het testslot laat over heel ClusterForge één invasieve test tegelijk toe. Het meetvenster duurt hoogstens 120 s, en de bevestiging noemt de maximale onbereikbaarheid vooraf.
- **Herstel in lagen.** Er zijn vier lagen: de herstelstap, de defer bij een fout, bij annuleren of bij een nette stop, het hervatten na een crash, en als laatste vangnet OnFinished met de rode balk. De storing is een stop en nooit een disable, dus na een reboot van de node draait de dienst altijd weer. Bij vm_hard_stop hangt het herstel af van de server en de Proxmox-API. Daarom blijft dat scenario bij lab en test.
- **Geen vrije shell en geen vrije URL.** Naar de agent gaat alleen één service-stap met een unit uit een vaste lijst. De probe gebruikt een eigen client in plaats van httpGet uit deploy, want die volgt redirects (`internal/deploy/deploy.go:96-109`). De probe bouwt de URL als `url.URL{Host: vip, Path: pad}`, eist dat het pad met / begint, volgt geen redirects en gebruikt geen keep-alive. Zo kan de server geen willekeurig adres opvragen.
- **Herkenbaar.** De eventcatalogus krijgt failover_test.created, failover_test.updated, failover_test.deleted, failover.fault_injected, failover.fault_cleared en failover.finished met result in de payload. Alle failover-events dragen run_id. Daarmee kunnen drift en later What Broke waarnemingen tijdens een test als bewust veroorzaakt markeren. De commando's zelf staan als agent.command in het logboek. Dankzij de statusdebounce geeft een geslaagde test geen vals split_brain of down, en echte statuswissels blijven gewone events.
- **Noodrem.** 'Afbreken' herstelt meteen, en de planning staat per test uit tot Jonas ze aanzet.

### Mijlpalen

- **Mijlpaal 7, Failovertest met de hand in lab en test.** Levert failover_tests, test_runs, de gezondheidspoort, het testslot, keepalived_stop en service_stop via apply.steps met een verplichte probe, de strenge voorcontrole, herstel ook bij annuleren, een nette stop of een crash, de kaart op clusterdetail, het bevestigingsvenster en de rapportpagina, met prod nog op prod_locked.
- **Mijlpaal 11, Planning in het testvenster en failover op prod.** Levert de planning per test in het testvenster met overgeslagen runs zonder taak, vm_hard_stop in lab en test via proxmox.PowerAndWait, handmatige tests op prod met slug en TOTP, en 'niet getest sinds' op de clusterkaarten.

Mijlpaal 3 legt daarvoor het clusterslot, de statusdebounce en OnFinished. Mijlpaal 12 voegt de geraakte diensten toe aan het bevestigingsvenster.

## Afhankelijkheden

Dependency mapping laat zien welke diensten Jonas draait en welke dienst van welke andere afhangt, per cluster en over alle clusters heen. Een dienst is bijvoorbeeld nginx in web-prod, mariadb in db-prod of een externe NFS-server op 10.0.0.60:2049. Diensten en afhankelijkheden komen uit de templates en uit zijn eigen invoer, en de facts van de agents leveren voorstellen voor diensten. De status van de nodes gaat mee langs de graaf, zodat een uitgevallen database meteen toont welke clusters geraakt zijn. Op `/afhankelijkheden` klikt hij op mariadb en daarna op "Wat raakt uitval?":

```text
Wat raakt uitval van mariadb in db-prod?                   bekende afhankelijkheden
prod   web-prod   php8.2-fpm   down         hangt hard af van mariadb · handmatig
prod   web-prod   nginx        down         via php8.2-fpm
test   app-test   app          verminderd   hangt zacht af van mariadb · handmatig
```

De graaf dimt intussen alles wat niet geraakt wordt. Valt mariadb echt uit, dan krijgt die dienst een rode stip en krijgen php8.2-fpm en nginx een rode rand, zonder dat Jonas de pagina herlaadt. Vanaf mijlpaal 12 toont het bevestigingsvenster voor onderhoud van db01 dezelfde soort lijst, en staat "geraakt door db-prod" naast web-prod in de clusterlijst. De module voert niets uit op de nodes: er komen geen agentcommando's en geen taaksoorten bij, en de graaf blokkeert of start nooit een actie.

### Hoe het werkt

**Diensten en pijlen.** Een dienst hoort bij een cluster of bij een losse node zonder cluster, of hij is extern, met een adres en een poort. Hij heeft een soort uit een vaste lijst in `internal/deps` (web, lb, vip, database, cache, queue, storage, dns, cron, container, app, external, other) en optioneel een systemd-unit en een hoofdpoort. Een afhankelijkheid is een pijl van afnemer naar leverancier, dus "web-prod hangt af van mariadb", en de layout loopt van links naar rechts. Bij een harde afhankelijkheid is de afnemer down als de leverancier uitvalt, bij een zachte werkt hij verminderd. Cycli mogen. Instanties worden niet apart bewaard: ze volgen uit de actieve nodes van het cluster en de unitstatus in de heartbeat, zodat er geen tweede bron is om bij te houden.

**Uit templates.** keepalived-nginx 1.1.0 krijgt in zijn services-sectie `unit`, `port`, `depends_on` en `strength`, met dezelfde stappen als 1.0.0: nginx (web, unit nginx, poort 80) hangt hard af van keepalived (vip, unit keepalived). `templates.Parse` controleert dat `depends_on` naar bestaande namen wijst. Een uitrol maakt de diensten en afhankelijkheden in dezelfde transactie als het cluster, met bron template. Bestaande keepalived-nginx-clusters blijven op 1.0.0 en krijgen dezelfde twee diensten één keer uit de migratie, zodat de resolver niets hoeft aan te vullen.

**Met de hand.** Een admin maakt diensten, externe diensten en afhankelijkheden aan in de webinterface of via de API. Zo komen Jonas' met de hand gebouwde clusters in de graaf, en zo legt hij ook de verbanden tussen clusters vast, zoals web-prod naar mariadb in db-prod.

**Voorstellen uit de facts.** Een resolver draait elke 5 minuten als lus op de planner, met dezelfde advisory lock als de evaluator, zodat hij en de impactberekening van mijlpaal 12 nooit tegelijk dezelfde rijen bijwerken. Hij maakt van een bekende unit op een actieve node van een cluster een voorgestelde dienst: nginx en apache2 worden web, haproxy lb, keepalived vip, mariadb, mysql en postgresql database, redis-server en memcached cache, rabbitmq-server queue, docker container, en cron alleen in een cluster van type cron. Infrastructuurunits zoals ssh, cf-agent, qemu-guest-agent, containerd, corosync, pve\*, ceph\*, chrony en systemd-\* slaat hij over. Hoort een unit in dezelfde scope al bij een dienst, in welke staat ook, dan maakt hij geen voorstel, dus een handmatige "webserver" met unit nginx krijgt geen tweede nginx ernaast. Een bestaande rij behoudt altijd haar staat; alleen `last_seen_at` schuift op.

**Eigen status.** In mijlpaal 9 rekent de API de status uit bij het lezen, met pure functies in `internal/deps/graph.go` naar het model van `internal/status/rules.go`. Een dienst met een unit telt alleen de actieve nodes van zijn cluster waarop die unit in de heartbeat voorkomt; de agent laat een unit die niet geïnstalleerd is daar al weg (`internal/agent/collect.go:276`). Draait de unit op al die nodes, dan is de dienst healthy, op een deel degraded en op geen enkele down. Zijn er nul zulke nodes, heeft de dienst geen unit of volgt de agent de unit niet, dan is hij unknown (grijs). Zo staat een dienst die maar op één rol draait niet altijd op verminderd, en lijkt een cluster in uitrol niet down. Een vip-dienst is down bij een VIP zonder eigenaar of bij split-brain, met de reden uit de clusterstatus. Een externe dienst is unknown, want ClusterForge bewaakt hem niet actief.

**Doorgeven en impact.** Een harde afhankelijkheid op een dienst die down is, maakt de afnemer "down door afhankelijkheid", een zachte maakt hem "verminderd door afhankelijkheid". Alleen down gaat verder door de graaf. Unknown, voorstellen en genegeerde rijen geven niets door, en een dienst die zelf al down is, krijgt geen tweede markering. Valt nginx overal uit, dan geeft keepalived het VIP af, maar blijft nginx in de graaf de oorzaak. Impact is dezelfde berekening voor een denkbeeldige uitval van een dienst, een cluster of een node: omgekeerd zoeken langs de pijlen, met het pad erbij en elke dienst één keer, zodat cycli geen probleem zijn. Bij een node telt een tweede gezonde instantie mee, net als de overname van het VIP. "Geraakt door" verandert de clusterstatus niet: de statusregels en hun events blijven alleen over het cluster zelf gaan.

**Live.** In mijlpaal 9 ververst de graafquery ook elke 15 s (`liveInterval`), naast de bestaande SSE, want een gestopte unit geeft alleen een event als de nodestatus omslaat. Een uitval staat daardoor binnen 30 s in de graaf. In mijlpaal 12 rekent `deps.Evaluate` na elke ronde van de evaluator, na diens commit en in een eigen transactie, zoals de statusdebounce voorschrijft; een fout wordt alleen gelogd. Hij bewaart status en impact per dienst en schrijft alleen bij een verandering één `service.status_changed` met eigen status, impact, oorzaak en pad. `live.ts` ververst daarop de query `['deps']`.

**Events.** In de eventcatalogus komen onder de soort Afhankelijkheden `service.created`, `service.updated`, `service.deleted`, `dependency.created`, `dependency.updated` en `dependency.deleted`, elk in dezelfde transactie als de wijziging. Een voorstel is `service.created` met bron discovered en het systeem als actor. Mijlpaal 12 voegt `service.status_changed` toe. DeleteCluster en DeleteNode zetten vanaf dan de inkomende afhankelijkheden van buiten het cluster of de node in de payload van hun event; nu schrijft DeleteCluster alleen `cluster.deleted` (`internal/inventory/inventory.go:221-236`), en verdwijnt zo'n verband door de cascade zonder spoor.

### Datamodel

Mijlpaal 9 maakt `services` en `service_dependencies` in een nieuwe migratie, en mijlpaal 12 voegt de statuskolommen toe in een andere nieuwe migratie.

| Tabel | Kolommen | Waarom |
| --- | --- | --- |
| services | `id`, `cluster_id` of `node_id` (hoogstens één, ON DELETE CASCADE), `name`, `kind`, `unit`, `port`, `address` (alleen extern), `description`, `source` (template, manual, discovered), `state` (confirmed, suggested, ignored), `last_seen_at`, `created_at`, `updated_at` | Een dienst van een cluster, een losse node of extern. `kind` is een vaste lijst in Go, net als de clustertypes, zodat een nieuwe soort geen migratie vraagt. Een genegeerde rij blijft als grafsteen staan, zodat het voorstel niet terugkomt. |
| service_dependencies | `id`, `from_service_id` (afnemer), `to_service_id` (leverancier), `strength` (hard, soft), `source`, `state`, `note`, `created_at`, `updated_at` | Een eigen rij per pijl met foreign keys, in plaats van `depends_on uuid[]` uit het fase 1-ontwerp: een array laat bij verwijderen wezen achter en kan geen bron, sterkte of staat dragen. Wie de pijl maakte, staat in het event. |
| services (mijlpaal 12) | `status`, `impact` (none, degraded, down), `impact_reason`, `impact_since` | Zo schrijft `deps.Evaluate` alleen bij een verandering een event, zoals de evaluator bij nodes. |

Een naam heeft hoogstens 63 tekens uit letters, cijfers en `@ . _ : / -`, en is uniek per scope via een unieke index op (`cluster_id`, `node_id`, `lower(name)`) met NULLS NOT DISTINCT; een externe dienst is uniek op adres en poort. Een pijl naar zichzelf mag niet, een pijl bestaat hoogstens één keer, en een index op `to_service_id` maakt het omgekeerd zoeken voor impact snel. In fase 2 komt een afhankelijkheid alleen uit een template of van Jonas; voorstellen gaan over diensten. De migratie van mijlpaal 9 vult elk keepalived-nginx-cluster met nginx, keepalived en hun afhankelijkheid, met bron template en staat confirmed.

### Agent

Er komen geen commando's, geen subjects en geen hoger protocol bij. Een nieuwe agentrelease maakt de vaste lijst `WatchedServices` (`internal/agent/collect.go:22-25`) langer met apache2, redis-server, memcached, rabbitmq-server, php8.1-fpm tot en met php8.4-fpm en, via een patroon, de PostgreSQL-instantie `postgresql@<versie>-main`. Die instantie is nodig omdat `postgresql.service` op Debian en Ubuntu een overkoepelende unit is die altijd active staat. Jonas zet de release op zijn nodes met `install.sh --upgrade`, zoals beschreven bij agentversie en bijwerken. Een node met een oude agent blijft werken, maar zijn redis- of php-fpm-diensten blijven unknown. De nieuwe units tellen daarna ook mee in de bestaande noderegels, net als nginx nu: een unit die enabled is maar niet draait, maakt de node verminderd.

### API

De pickers in de webinterface gebruiken dezelfde graafaanroep, dus een aparte lijst van diensten is niet nodig.

| Methode | Pad | Doel |
| --- | --- | --- |
| GET | `/api/v1/dependency-graph` | De graaf in één aanroep: groepen (clusters met status en omgeving), diensten (eigen status, impact, instanties per node) en pijlen (sterkte, bron, staat, geraakt). Filters `cluster_id` (dat cluster plus directe buren), `environment` en `include_suggested`; `level=cluster` geeft de ingeklapte weergave met samengevoegde pijlen. Voor elke ingelogde gebruiker. |
| POST | `/api/v1/services` | Een dienst aanmaken in een cluster, op een losse node, of extern met `address` en `port` (admin). |
| PATCH | `/api/v1/services/{serviceId}` | Naam, soort, unit, poort of beschrijving wijzigen, of via `state` een voorstel bevestigen, negeren of terugzetten (admin). |
| DELETE | `/api/v1/services/{serviceId}` | Een dienst met zijn pijlen verwijderen (admin). Een dienst met een unit blijft als genegeerde rij staan, zodat de resolver hem niet opnieuw voorstelt. |
| POST | `/api/v1/dependencies` | Een afhankelijkheid aanmaken met `from_service_id`, `to_service_id`, `strength` en `note` (admin). 400 bij een pijl naar zichzelf, 409 als hij al bestaat. |
| PATCH | `/api/v1/dependencies/{dependencyId}` | Sterkte of notitie wijzigen (admin). |
| DELETE | `/api/v1/dependencies/{dependencyId}` | Een afhankelijkheid verwijderen (admin). |
| GET | `/api/v1/impact` | Wat raakt uitval, met precies één van `service_id`, `cluster_id` of `node_id`: diensten en clusters met ernst (down of verminderd), omgeving en pad, prod bovenaan. Voor elke ingelogde gebruiker. |

### Schermen

- **Navigatie.** Een nieuw menu-item Afhankelijkheden (`/afhankelijkheden`), tussen Monitoring en Proxmox.
- **De graaf.** Clusters zijn groepen met naam, omgevingsbadge en status, diensten zijn kaartjes met naam, soort en een stip voor de eigen status. Een rode rand betekent "down door afhankelijkheid", een oranje "verminderd door afhankelijkheid", en de pijl naar de oorzaak kleurt rood. Bevestigde pijlen zijn doorgetrokken, zachte dun en grijs, en voorstellen gestippeld, alleen met de schakelaar Voorstellen tonen. Bovenaan staan een filter op omgeving, de schakelaars Alleen clusters en Interne afhankelijkheden, en een teller "N voorstellen".
- **Zijpaneel.** Een klik op een dienst toont de eigen status met reden, de instanties per node met hun unitstatus, Hangt af van en Gebruikt door, de bron, en de knop Wat raakt uitval?, die alles dimt wat niet geraakt wordt en rechts de lijst per cluster toont. Een admin kan er bevestigen, negeren, bewerken, verwijderen en een afhankelijkheid toevoegen, ook naar een bestaande of nieuwe externe dienst.
- **Tab Voorstellen.** Een tabel met de voorgestelde diensten, zoals "redis-server · cache · web-prod · op web01 en web02", met per rij Bevestigen en Negeren.
- **Clusterdetail.** Een kaart Diensten en afhankelijkheden met een tabel van de diensten (naam, soort, unit, poort, status, bron), de lijsten Hangt af van en Gebruikt door, en een link naar `/afhankelijkheden?cluster_id=`. Een admin voegt hier diensten en afhankelijkheden toe, met een picker die per cluster groepeert, en behandelt de voorstellen van dit cluster.
- **Smalle schermen.** Onder 640 px toont `/afhankelijkheden` een lijst in plaats van de graaf: per cluster de diensten met Hangt af van en Gebruikt door.
- **Mijlpaal 12.** Overzicht en clusterlijst tonen naast de status een badge "geraakt door db-prod". De bevestigingsvensters van herstarten, onderhoud, afsluiten, failovertest en herstel tonen via `/impact?node_id=` welke diensten down of verminderd raken. Het venster voor verwijderen van een cluster of node toont "gebruikt door web-prod".
- **Bibliotheek.** React Flow 12 (`@xyflow/react`, MIT) met `@dagrejs/dagre` voor de layout, via `next/dynamic` zonder SSR en alleen op de graafpagina geladen, zodat de rest van de static export niet groeit. De layout wordt alleen opnieuw berekend als de topologie verandert, niet bij een statuswissel, zodat de graaf niet verspringt. Valt de bundel te groot uit, dan wordt het een eigen SVG met dagre. Inklappen en samenvoegen gebeurt in Go, zodat `graph_test.go` het test; de webinterface heeft geen testrunner.

### Veiligheid

- **Niets op de node.** Er komen geen agentcommando's en geen taaksoorten bij. De agentrelease volgt alleen meer units, en voorstellen komen uit facts die de server al ontvangt. Het ergste wat een gecompromitteerde node kan doen, is voorstellen laten verschijnen.
- **De graaf beslist niets.** De impact in een bevestigingsvenster is alleen informatie, en de bestaande VIP-controle in lifecycle (`internal/lifecycle/lifecycle.go:280-297`) blijft de enige harde blokkade. Een onvolledige graaf mag een gevaarlijke actie nooit veilig laten lijken, en ook geen noodzakelijke actie tegenhouden. De webinterface spreekt daarom altijd van "bekende afhankelijkheden" en toont bij elke afhankelijkheid de bron.
- **Voorstellen blijven voorstellen.** De resolver maakt alleen rijen met staat suggested en verandert nooit de staat van een bestaande rij. Alleen een admin bevestigt, en een genegeerde rij blijft staan zodat ze niet terugkomt.
- **De evaluator blijft vrij.** `deps.Evaluate` draait na de commit van de evaluator, dus een fout in de nieuwe code kan de node- en clusterstatus en de VIP-eigenaar nooit bevriezen. Een test laat een deps-hook die altijd faalt `vip.owner_changed` niet tegenhouden. De gedeelde advisory lock voorkomt deadlocks tussen resolver en evaluator op dezelfde rijen.
- **Namen uit de nodes.** Unitnamen komen van de nodes. De resolver schoont een onbekend teken per rij op en schrijft elke rij apart, zodat één vreemde naam de ronde niet tegenhoudt. React escapet alle tekst, en de graaf gebruikt nooit `dangerouslySetInnerHTML` of HTML-labels.
- **Verwijderen laat een spoor na.** Een verwijderd cluster of een verwijderde node neemt zijn diensten en afhankelijkheden mee, maar vanaf mijlpaal 12 staan de inkomende afhankelijkheden in het event en waarschuwt het venster ervoor.
- **Rechten.** Viewers zien de graaf en de impact, want er staan geen geheimen in, en krijgen 403 op elke schrijfactie. Elke schrijfactie geeft een event met de gebruiker als actor.

### Mijlpalen

- **Mijlpaal 9, Diensten en afhankelijkheden.** Levert `services` en `service_dependencies` met het aanvullen van bestaande keepalived-nginx-clusters, keepalived-nginx 1.1.0 met diensten, handmatig beheer met events, voorstellen uit bekende units, eigen status en doorgeven bij het lezen, `GET /api/v1/impact`, de pagina `/afhankelijkheden` en de kaart op clusterdetail, plus een agentrelease met een langere `WatchedServices`.
- **Mijlpaal 12, Impact bij acties.** Levert de opgeslagen status en impact met `deps.Evaluate` na de commit van de evaluator, het event `service.status_changed`, de badge "geraakt door", de impact in de bevestigingsvensters van lifecycle, failovertest en herstel, en de inkomende afhankelijkheden in het event bij verwijderen.

Mijlpaal 3 legt daarvoor de statusdebounce met het rekenen na de commit klaar, mijlpaal 4 de templateversies waardoor keepalived-nginx 1.1.0 naast 1.0.0 kan staan, en mijlpaal 5 `install.sh --upgrade`. Mijlpaal 13 geeft de nieuwe templates meteen diensten met unit en poort, zodat een uitgerold Docker-cluster vanzelf in de graaf staat. Afhankelijkheden ontdekken uit luisterende poorten en TCP-verbindingen hoort bij infrastructure mapping in fase 3.

## GitOps

Met GitOps beheert Jonas zijn clusters uit een ingebouwde template vanuit één GitHub-repository. Per cluster staat daar een bestand `clusters/<slug>/cluster.yaml` met naam, omgeving, template, parameters en Proxmox-doel, zonder geheimen. Binnen een minuut na een commit toont ClusterForge of het bestand geldig is en wat de wijziging op elke node zou doen. Na zijn goedkeuring past `cluster.apply` de wijziging node voor node toe, en terugdraaien is een `git revert` die hetzelfde pad volgt. ClusterForge schrijft zelf nooit naar Git: het token heeft alleen leesrecht en export is een download. Clusters verwijderen, omlaag schalen en bestaande VM's groter maken gaan niet via Git. Een nieuwe clusternaam ziet er op de wijzigingspagina zo uit:

```text
Wijziging voor web · wacht op goedkeuring · prod
Commit 3f9c2a1 "Webcluster heet voortaan Website" · Jonas · geverifieerd
Basis: spec-revisie 1 · template keepalived-nginx 1.0.0, ongewijzigd · geen nodes erbij
Metadata    veld   oud          nieuw
            name   Webcluster   Website
Volgorde    1. web-02   bestand /var/www/html/index.html, 2 regels anders   [diff]
            2. web-01   bestand /var/www/html/index.html, 2 regels anders   [diff]   VIP-eigenaar
Let op      web-01: index.html wijkt op de node al af van de spec en wordt overschreven
[Goedkeuren en toepassen…]   [Afwijzen…]
```

### Hoe het werkt

**Het bestand.** Onder de gekozen map (standaard `clusters/`) staat één map per cluster, met de slug als naam. Andere bestanden worden genegeerd. Alleen clusters uit een ingebouwde template kunnen in Git, want templates, stappen en bestanden komen altijd uit de binary.

```yaml
clusterforge: 1
cluster:
  name: Webcluster
  slug: web                 # gelijk aan de mapnaam
  environment: prod
  tags: [web]
template:
  name: keepalived-nginx
  version: 1.0.0            # verplicht; export schrijft hem uit
params:                     # alle niet-geheime parameters, nooit auth_pass
  vip: 10.0.20.100          # immutable
  vrid: 51                  # immutable; mag alleen bij een nieuw cluster ontbreken
  node_count: 2
  cpu: 2                    # cpu, memory en disk liggen vast na de uitrol
  memory: 2G
  disk: 20G
target:                     # alleen gelezen bij een nieuw cluster
  proxmox: pve-thuis        # naam van de Proxmox-koppeling
  bridge: vmbr0
  first_ip: 10.0.20.11/24   # verder image_vmid, vlan, gateway en dns
```

**Lezen.** De server pollt elke 60 seconden de head van de branch met een ETag. Zonder nieuwe commit antwoordt GitHub dan met 304, en kost een poll niets. De knop Nu synchroniseren en een herstart van de server stoten de lus meteen aan, dus een webhook is niet nodig. Bij een nieuwe commit haalt de lus de tree op en daarna alleen de clusterbestanden met een andere blob-sha. Daarbij gelden grenzen van 200 bestanden en 64 KB per bestand. De client zit achter een kleine interface `Source` (Head, Tree, Blob, Commit), zodat Gitea of Forgejo later kunnen. Tests en `make dev` gebruiken een nep-GitHub naar het voorbeeld van pvefake. Het token is een fine-grained token op alleen deze repository met Contents read-only. Het wordt versleuteld met de masterkey, net als het Proxmox-token, en zonder `CF_MASTER_KEY` staat GitOps uit. Er komen geen omgevingsvariabelen bij, maar de server moet `api.github.com:443` uitgaand kunnen bereiken. Is GitHub onbereikbaar of is het token verlopen, dan komt de fout in `last_error` en blijft alles zoals het is.

**Valideren.** De eerste laag controleert het bestand zelf. De YAML moet strikt zijn, zonder onbekende velden, en de slug moet gelijk zijn aan de mapnaam. De server moet de template en de versie kennen, de parameters moeten het templateschema volgen, en er mogen geen geheimen in staan en geen wijzigingen aan wat vastligt. Alle niet-geheime parameters zijn verplicht, zodat een weggelaten `node_count` nooit ongemerkt terugvalt op de standaardwaarde. Vast liggen de slug, de templatenaam, vip en vrid (via een nieuwe vlag `immutable` op een templateparameter), en voor een bestaand cluster ook cpu, memory en disk. `target` wordt bij een bestaand cluster genegeerd. Een lagere `node_count` dan het aantal actieve nodes is een fout, en de melding verwijst naar de lifecycle-acties. De tweede laag gebruikt de controles van de uitrol: de Proxmox-koppeling op naam, vrije adressen en VIP, en genoeg capaciteit. Die controles sluiten het eigen cluster uit. Elke fout noemt veld en regelnummer. Een geheime parameter zoals `auth_pass` maakt ClusterForge bij een nieuw cluster zelf aan; bij een bestaand cluster blijft hij in de tabel secrets. Verdwijnt het bestand van een gekoppeld cluster, dan geeft dat alleen een waarschuwing.

**Plannen.** Elk geldig bestand dat afwijkt van de spec van zijn cluster, wordt een plan. Het plan rendert de oude revisie met haar eigen templateversie en de nieuwe revisie met de hare, volgens de gewenste staat met vaste templateversie. Zo toont ook een versiewijziging haar diff, en werkt een revert daarvan. Het plan toont metadata en parameters oud en nieuw, de nodes die erbij komen, per node de gewijzigde stappen met een unified diff, en de waarschuwingen. Geheimen staan er als plaatshouder in, zoals bij de vingerafdrukken beschreven, dus de poll-lus ontsleutelt nooit een geheim. Uit de laatste driftcontrole toont het plan welke bestanden op de nodes al afwijken en bij het toepassen overschreven worden. Voor een toekomstige node rekent het plan met het geplande adres en de netwerkkaart van de bestaande nodes, en zet het er een waarschuwing bij. Een plan voor een nieuw cluster toont alleen nodes, adressen en VM-vorm. Per cluster is er hoogstens één wachtend plan, en een nieuwere commit zet het oudere plan op superseded. Plannen raakt geen enkele node.

**Goedkeuren.** Alleen een admin keurt goed, ook in lab, en op prod geldt de bevestiging bij prod. Bij goedkeuring doet de server alles in één transactie. Hij vergrendelt het cluster, controleert `base_revision` en of dit het nieuwste plan is, en schrijft een spec-revisie met bron git en de commit-sha. Daarna werkt hij de metadata bij en zet hij via het clusterslot `cluster.apply` in de wachtrij, of `cluster.deploy` voor een nieuw cluster. Verandert er geen gerenderde stap, zoals bij alleen andere tags, en was `applied_revision` al gelijk aan `spec_revision`, dan schuift de revisie op zonder taak. Afwijzen kan met een reden.

**Toepassen.** Toepassen is `cluster.apply` in de modus wijziging, in de volgorde die daar beschreven staat. Bij omhoog schalen maakt de taak eerst de nieuwe VM's en meldt ze aan. Een nieuwe node krijgt de laagste vrije index, omdat de keepalived-prioriteit uit de index volgt (`internal/templates/builtin/keepalived-nginx/files/keepalived.conf.tmpl:22`). Anders zakt die prioriteit na herhaald schalen weg. Via OnFinished wordt de wijziging applied of failed. Een mislukte wijziging blijft failed; Opnieuw toepassen is een nieuwe taak voor de huidige revisie. De agent verandert niet: alles gaat via het bestaande `apply.steps`, en de lokale afwijkingen in het plan komen van `state.inspect`.

**Koppelen en exporteren.** Jonas exporteert een bestaand templatecluster als `cluster.yaml`, met de naam van de Proxmox-koppeling en de templateversie en zonder geheimen. Hij commit het bestand en koppelt het cluster. Dat lukt alleen als het bestand precies gelijk is aan de export; anders krijgt hij een fout met de diff. Daarna is Git de bron van waarheid. `git_repo_url` wordt de link naar het bestand op GitHub, de API weigert wijzigingen aan wat uit Git komt met 409, en verwijderen kan pas na ontkoppelen. Node-acties zoals herstarten en onderhoud blijven mogelijk, via het clusterslot. Na ontkoppelen negeert ClusterForge het bestand.

**Events.** In de eventcatalogus komen onder de soort GitOps:

- `gitops.repo_connected`, `gitops.repo_updated` (alleen de gewijzigde velden, nooit het token) en `gitops.repo_disconnected`;
- `gitops.commit_seen`, en `gitops.sync_failed` en `gitops.sync_recovered` (alleen bij een overgang);
- `gitops.file_invalid` en `gitops.file_missing`;
- `gitops.change_planned`, `gitops.change_superseded`, `gitops.change_approved`, `gitops.change_rejected`, `gitops.change_applied` en `gitops.change_failed`;
- `cluster.git_linked` en `cluster.git_unlinked`.

Elke payload heeft de commit-sha en de auteur. `cluster.spec_changed` krijgt bron, commit-sha en `change_id`, en de root-acties staan als `agent.command` onder de taak.

### Datamodel

Mijlpaal 14 brengt een nieuwe migratie, en mijlpaal 17 voegt in een nieuwe migratie `server_url` toe. `clusters.applied_revision` bestaat dan al uit mijlpaal 10.

| Tabel | Kolommen | Waarom |
| --- | --- | --- |
| git_repos | `id`, `api_url` (standaard api.github.com, alleen https behalve in tests en `make dev`), `owner`, `name`, `branch` (main), `path` (clusters), `token_enc`, `key_id`, `head_sha`, `head_etag`, `synced_sha`, `scan` (jsonb), `last_sync_at`, `last_error`, tijden; vanaf mijlpaal 17 `server_url` | De koppeling, met precies één rij (unieke index op `(true)`). Het token is versleuteld met AAD `git:<id>`. `scan` is de laatste stand per bestand: pad, slug, blob-sha, cluster, toestand en fouten met veld en regel. Een bestand met een geheimfout wordt niet bewaard. |
| git_changes | `id`, `repo_id`, `cluster_id` (leeg bij een nieuw cluster, SET NULL), `slug`, `kind` (create, update), `commit_sha`, `commit_message`, `commit_author`, `commit_verified`, `base_revision`, `spec` en `metadata` (jsonb, zonder geheimen), `plan` (jsonb), `status` (pending, applying, applied, failed, rejected, superseded), `job_id`, `decided_by`, `decided_at`, `reason`, tijden | Eén wijziging van één bestand in één commit, met plan, beslissing en herkomst. Twee partiële unieke indexen op `slug`, een voor pending en een voor applying, laten per cluster één wachtend plan en één toepassing toe. |
| clusters | `git_repo_id` (SET NULL) | Is dit veld gezet, dan is Git de bron van waarheid. De koppeling verwijderen ontkoppelt alle clusters. |
| cluster_spec_revisions | `commit_sha`, met CHECK (source <> 'git' OR commit_sha IS NOT NULL) | Elke revisie uit Git wijst naar haar commit, en `GET /api/v1/clusters/{clusterId}/spec-revisions` uit mijlpaal 4 toont die commit. |

### API

| Methode | Pad | Doel |
| --- | --- | --- |
| GET | `/api/v1/gitops/repo` | Koppeling en syncstatus: repository, branch, map, laatste commit en laatste fout. Ook voor viewers; het token komt nooit terug. |
| PUT, DELETE | `/api/v1/gitops/repo` | Koppeling maken, wijzigen of verwijderen (admin). Het token kan alleen geschreven worden, en verwijderen ontkoppelt alle clusters. |
| POST | `/api/v1/gitops/repo/probe` | Token, branch en map testen zonder op te slaan (admin), met de head-commit en de gevonden clusterbestanden. |
| POST | `/api/v1/gitops/sync` | Nu synchroniseren (admin); antwoordt met 202. |
| GET | `/api/v1/gitops/files` | Bestanden uit de laatste scan met toestand en fouten. Ook voor viewers. |
| GET | `/api/v1/gitops/changes` en `/api/v1/gitops/changes/{changeId}` | Wijzigingen, te filteren op status en `cluster_id`, en één wijziging met commit, plan en diffs. Ook voor viewers. |
| POST | `/api/v1/gitops/changes/{changeId}/approve` | Goedkeuren (admin), met `confirm` op prod. Geeft 202 met de taak, of 409 bij een verouderd plan of een bezet clusterslot. |
| POST | `/api/v1/gitops/changes/{changeId}/reject` | Afwijzen met een reden (admin). |
| POST | `/api/v1/clusters/{clusterId}/git/link` en `/api/v1/clusters/{clusterId}/git/unlink` | Koppelen en ontkoppelen (admin). Koppelen lukt alleen als het bestand gelijk is aan de export, anders volgt 409 met de diff. |
| POST | `/api/v1/clusters/{clusterId}/git/reapply` | Opnieuw toepassen (admin): een nieuwe `cluster.apply` voor de huidige revisie als `applied_revision` achterloopt, met `confirm` op prod. |
| GET | `/api/v1/clusters/{clusterId}/git/export` | `cluster.yaml` als download, zonder geheimen, alleen voor templateclusters. Ook voor viewers. |
| PATCH, POST, DELETE | Bestaande cluster-, VIP- en nodepaden | Geven voor een gekoppeld cluster 409 op naam, beschrijving, omgeving, tags, slug, type, `git_repo_url`, `primary_ip`, VIP's, nodes in of uit het cluster en verwijderen. |

### Schermen

- **/gitops**, een nieuw item tussen Templates en Instellingen. Bovenaan staat de kaart Koppeling, naar het voorbeeld van de Proxmox-koppeling: repository, branch, map en token (alleen schrijven), met de knoppen Testen, Opslaan en Nu synchroniseren. Daaronder staat de laatste commit (korte sha, bericht, auteur, tijd en een link naar GitHub), of een rode melding als de laatste sync mislukte. Dan volgt de tabel Bestanden met pad, cluster, commit en een badge (in sync, wijziging wacht, ongeldig, nieuw, niet gekoppeld, ontbreekt). Bij een ongeldig bestand staan de fouten per veld en regel, en bij een niet-gekoppeld bestand de knop Koppelen. De lijst Wijzigingen toont eerst wat wacht of loopt. Elke rij heeft cluster, commit, status, een link naar de taak en een samenvatting zoals "node_count van 2 naar 3; keepalived.conf op 2 nodes, 1 nieuwe node". Onderaan staat "Clusters nog niet in Git" met per cluster de knop Exporteren.
- **/gitops/wijziging?id=…** Dit is de pagina uit het voorbeeld bovenaan. Ze werkt met een query-parameter, zoals clusterdetail, omdat de static export geen dynamische routes heeft. Elke diff is uitklapbaar. Goedkeuren opent het gedeelde bevestigingsvenster, en Afwijzen vraagt een reden. Na goedkeuring linkt de pagina naar het taakdetail met de live voortgang.
- **Clusterdetail.** Een kaart Git toont het pad, de commit van de huidige revisie en de Git-toestand, bijvoorbeeld "gewenste revisie 5, toegepast 4" met de knop Opnieuw toepassen. Verder staan er de knoppen Exporteren, Koppelen of Ontkoppelen. Bij een gekoppeld cluster zijn de velden in het clusterformulier uitgeschakeld, met de uitleg "wijzig dit in Git". De tab Historie toont per revisie de bron en de commit.
- **Overzicht.** Een teller "2 wijzigingen wachten op goedkeuring" linkt naar `/gitops`. Alle schermen verversen bij de `gitops.*`-events via de bestaande SSE-hook.

### Veiligheid

Het grootste risico is dat GitOps een nieuwe weg naar root op productieservers opent voor wie naar de branch kan pushen of het GitHub-account steelt. Het ontwerp beperkt dat zo:

- **Git levert waarden, nooit gedrag.** Templates, stappen, bestanden en command-stappen komen alleen uit de binary, en waarden uit Git worden nooit zelf als sjabloon uitgevoerd. De agent draait command-stappen met `sh -c` (`internal/agent/apply.go:375-381`). Daarom weigert `templates.Parse` sinds mijlpaal 4 een parameter zonder streng type of pattern in een command-stap of een pad. Zo bereikt een waarde uit Git ook in latere templates geen shell.
- **Een mens beslist, en de schade blijft klein.** Elke wijziging vraagt de goedkeuring van een admin, ook in lab. Er is geen automatisch of periodiek toepassen, en plannen doet niets op de nodes. `cluster.apply` stopt bij de eerste node die niet gezond terugkomt, vóór de VIP-eigenaar aan de beurt is. Het clusterslot sluit node-, Proxmox- en uitroltaken in hetzelfde cluster uit, een verouderde `base_revision` blokkeert goedkeuren, en de taak is niet Retryable. Terugdraaien is `git revert` langs dezelfde controles.
- **Wat vastligt, blijft vast.** Slug, templatenaam, VIP, vrid, Proxmox-doel en VM-vorm wijzigen niet via Git, en ook niet via de UI zodra een cluster gekoppeld is. Een VIP-wijziging in de UI zou anders het echte adres uit de lijst van bezette VIP's en vrid's halen (`internal/store/queries/deploy.sql:21-28`). Een ander cluster kan dan hetzelfde VIP krijgen.
- **Niets onomkeerbaars.** ClusterForge verwijdert vanuit Git geen clusters, VM's of schijven. Omlaag schalen valt buiten fase 2: een vertrekkende node die zijn unicast-peers kwijtraakt, kan even een tweede VIP-houder worden.
- **Geheimen blijven buiten Git.** Een geheim in een bestand is een validatiefout, en zo'n bestand wordt niet bewaard of getoond. Plannen gebruiken plaatshouders, en export schrijft geen geheimen. Het token komt nooit terug uit de API en staat in geen log. Het gaat alleen over https naar de host van `api_url` en heeft alleen leesrecht. De test van mijlpaal 14 zoekt het VRRP-wachtwoord in `git_changes`, `git_repos`, de events en elk API-antwoord.
- **Alles staat in het logboek.** Elke beslissing is een event met commit-sha en auteur, en elke revisie bewaart `commit_sha` en `created_by`. Ondertekende commits zijn niet verplicht, maar de UI toont of GitHub een commit als geverifieerd ziet.

### Mijlpalen

- **Mijlpaal 14, GitOps: lezen, valideren en plannen.** Levert de koppeling met testknop, de poll-lus met ETag, de nep-GitHub, het bestandsformaat met fouten op veld en regel, plannen met diffs per node en lokale afwijkingen, export en koppelen, en alleen-lezen velden voor gekoppelde clusters, zonder dat er op een node iets verandert.
- **Mijlpaal 15, GitOps: goedkeuren en toepassen.** Levert goedkeuren en afwijzen, spec-revisies met bron git en commit-sha, toepassen met `cluster.apply`, de afronding via OnFinished, Opnieuw toepassen en de kaart Git met gewenste en toegepaste revisie.
- **Mijlpaal 17, Omhoog schalen en nieuwe clusters uit Git.** Levert een hogere `node_count` met nieuwe VM's, het aanmelden van hun agents en de veilige volgorde, en maakt van een nieuw bestand een uitrol met bron git, met `server_url` op de koppeling.

GitOps steunt daarbij op de vaste templateversies uit mijlpaal 4, de driftcontrole uit mijlpaal 5, de uitroltaak uit mijlpaal 10 en de templates uit mijlpaal 13. Daardoor dekt het meer dan het ene keepalived-nginx-cluster.

## Bouwvolgorde

Fase 2 telt zeventien mijlpalen. Elke module kijkt eerst voordat ze iets verandert, de veiligheidsfundering staat er vóór de eerste root-actie, en Jonas heeft na elke mijlpaal iets bruikbaars. De grootte is een relatieve inschatting.

| # | Mijlpaal | Klaar als | Grootte |
| --- | --- | --- | --- |
| 1 | Logboek en lekfixes | `/logboek` toont regels als '05-10-2026 14:02 · Jonas · Node web03 toegevoegd · webcluster-prod', met filters en een diff-tabel bij `cluster.updated`, en een viewer krijgt 403. De SSE-notificatie draagt alleen het id, en een als gebruikersnaam getypt wachtwoord staat nergens in events of stream. | L |
| 2 | Back-ups zien en vers houden | De pagina Back-ups toont per gekoppelde node de nieuwste back-up met leeftijd, storage, grootte en PBS-verificatie, en een te oude of ontbrekende back-up geeft precies één event. VM's zonder back-upjob staan apart, en de VM van ClusterForge zelf krijgt dezelfde badge. | M |
| 3 | Herkomst, agentcommando's in het logboek en het clusterslot | Een herstart van web01 staat onder het filter `node=web01` als `job.queued` met IP, `agent.command` met `job_id`, de lifecyclewijziging en `job.succeeded`. Een tweede schrijvende actie in hetzelfde cluster geeft 409, en een VIP-wissel geeft geen split_brain- of down-event meer. | L |
| 4 | Gewenste staat met vaste templateversie | `RenderNode` uit `clusters.spec` geeft dezelfde stappen als de uitroltaak, ook voor een oude spec, en een cluster op 1.0.0 rendert nog met 1.0.0 als 1.1.0 bestaat. Een ontbrekend geheim is een fout, en clusterdetail heeft een tab Historie. | M |
| 5 | Drift zien voor clusters uit een template | Na een uitrol van keepalived-nginx geven drie wijzigingen op web02 daar vier afwijkingen en precies één `drift.detected`, zonder dat de controle iets verandert of een geheim of hash lekt. Een agent met protocol 3 toont 'agent te oud', en `install.sh --upgrade` brengt hem op protocol 4. | L |
| 6 | Baseline en negeren: drift voor bestaande clusters | Een handmatig aangemaakt cluster krijgt via de wizard een baseline, en een gewijzigd `nginx.conf` op één node geeft drift. Opnieuw vastleggen of een negeerregel haalt de afwijking weg, en geen API-antwoord bevat een hash. | M |
| 7 | Failovertest met de hand in lab en test | In de integratietest geeft een snelle overname PASS en een trage FAIL, en daarna draait keepalived weer met het VIP terug, ook na annuleren of een herstart van de runner. Op Jonas' lab-cluster geeft de test 'PASS: na x s op web-02, alles hersteld'. | L |
| 8 | Back-up terugzetten en controleren in een sandbox | Op Jonas' Proxmox geeft een back-up van een keepalived-nginx-node passed, of warning met uitleg, met hersteltijd en opstarttijd, en daarna is de VM weg. Een server die midden in de taak stopt, laat na de herstart geen sandbox achter. | L |
| 9 | Diensten en afhankelijkheden | Jonas koppelt web-prod aan mariadb in db-prod, en impact op mariadb noemt web-prod met het pad. Stopt mariadb op alle db-nodes, dan wordt de dienst binnen 30 s rood en krijgt web-prod een rode rand. | L |
| 10 | Veilige uitrol en drift herstellen | Herstel zet `keepalived.conf` op web02 terug, herlaadt keepalived en laat web01 ongemoeid, en een tweede wijziging tussen tonen en uitvoeren laat de nodestap mislukken zonder iets toe te passen. Faalt de HTTP-controle na web02, dan stopt de taak vóór de VIP-eigenaar, en op prod weigert de API zonder slug. | L |
| 11 | Planning in het testvenster en failover op prod | De planner rekent 'elke 7 dagen om 04:00' goed uit over de overgang naar wintertijd op 25 oktober 2026, start nooit twee tests tegelijk en zet een gemiste run op skipped zonder mislukte taak. Een prod-test loopt alleen met de slug, en een geplande failovertest op prod is niet in te stellen. | M |
| 12 | Impact bij acties | Valt db-prod uit, dan komt er per geraakte dienst precies één `service.status_changed` met oorzaak en pad, en bij herstel nog één. Het venster voor onderhoud van db01 toont welke diensten down raken. | M |
| 13 | Meer templates: Docker, Cron en Generic | Elke template rolt uit in de fleet-test, en voor elke stap geeft apply gevolgd door inspect en `Compare` nul afwijkingen. De templatelijst toont vier templates. | L |
| 14 | GitOps: lezen, valideren en plannen | Binnen een minuut na een commit staat elk bestand onder `clusters/` op geldig, of ongeldig met veld en regel, en een andere `cluster.name` geeft een plan met de diff van `index.html` zonder dat er op de nodes iets verandert. Het VRRP-wachtwoord staat nergens in de GitOps-tabellen, de events of de API. | L |
| 15 | GitOps: goedkeuren en toepassen | Goedkeuren van een naamwijziging maakt spec-revisie 2 met bron git en commit-sha, en `cluster.apply` past `index.html` eerst toe op de node zonder VIP en daarna op de eigenaar. Een commit die nginx laat falen, stopt vóór de VIP-eigenaar, en git revert brengt de oude naam langs hetzelfde pad terug. | M |
| 16 | Diepe back-upcontrole met `cf-agent verify` | Op Jonas' Proxmox geeft een nieuwe back-up van een bijgewerkte node een rapport met services en, waar die draait, de databasecontrole. Een oude agent geeft warning 'agent te oud', en een agent die in de sandbox start, verbindt niet met NATS. | M |
| 17 | Omhoog schalen en nieuwe clusters uit Git | `node_count` van 2 naar 3 maakt na goedkeuring een derde VM en zet `keepalived.conf` met drie peers op alle nodes, waarna het cluster healthy is zonder split_brain-event. Een nieuw bestand `clusters/api/cluster.yaml` wordt na goedkeuring een uitrol met spec-revisie 1 en bron git. | L |

**Waarom deze volgorde.** Mijlpaal 1 en 2 lezen alleen en vragen geen agentwijziging, en mijlpaal 1 dicht meteen twee lekken die vandaag bestaan. Mijlpaal 3 legt de veiligheidsfundering waarop alles met root-acties steunt, en helpt het onderhoud uit fase 1 al door het valse split-brain weg te nemen. Daarna komen de gewenste staat en drift, eerst alleen lezend (mijlpaal 4 tot en met 6). De eerste ingrepen, de failovertest en de sandbox, blijven beperkt tot lab, test of een tijdelijke VM, en leveren `test_runs`, het testslot en de gezondheidspoort, die herstel in mijlpaal 10 hergebruikt. GitOps komt pas als `cluster.apply` zich met herstel bewezen heeft en er meer templates zijn dan keepalived-nginx. Mijlpaal 16 wacht tot `install.sh --upgrade` een tijd in gebruik is en de sandbox zich bewezen heeft. Omhoog schalen is door de unicast-peers van keepalived het riskantste deel en komt als laatste; valt het buiten de tijd, dan blijft alle eerdere waarde staan.

**Fase 1-mijlpaal 8.** Die mijlpaal wordt gesplitst. Docker-cluster, Cron-cluster en Generic application komen als mijlpaal 13, na de vaste templateversies en de controle op parameters in command-stappen (mijlpaal 4), de inspect-consistentietest (mijlpaal 5) en de veilige uitroltaak (mijlpaal 10). Op die plek krijgen ze meteen een eigen versiemap, gaan hun stappen door de test dat apply, inspect en `Compare` nul afwijkingen geven, kan een waarde uit Git niet ongecontroleerd in een command-stap belanden, en zijn ze later veilig bij te werken. Ze komen vóór GitOps, zodat GitOps meer dekt dan het ene keepalived-nginx-cluster. Wie ze nu eerst bouwt, moet ze na mijlpaal 4 alsnog verhuizen en door de nieuwe controles halen. PostgreSQL HA en MariaDB HA gaan naar een eigen spoor na fase 2, omdat ze het zwaarst zijn (replicatie, bootstrap van de eerste primary, failover van data) en de failovertest databaseclusters in fase 2 bewust weigert. Jonas' bestaande databaseservers krijgen toch al waarde zonder template: versheid van back-ups (mijlpaal 2), terugzetten in een sandbox (mijlpaal 8), databasecontroles in de sandbox (mijlpaal 16), drift via een baseline (mijlpaal 6) en een plek in de afhankelijkheidsgraaf (mijlpaal 9). Niets in fase 2 wacht op fase 1-mijlpaal 8.

## Open keuzes

Veertien keuzes zijn ingevuld met een aanbeveling, en het ontwerp volgt overal de aanbevolen kolom.

| Keuze | Aanbevolen | Alternatief | Waarom |
| --- | --- | --- | --- |
| Met welke templateversie vergelijken en herstellen? | Versiemappen in de binary; altijd renderen met `clusters.template_version`. Bijwerken is een expliciete specwijziging. | Gerenderde stappen per node bewaren (`applied_renders`) en daartegen vergelijken. | Elke revisie is opnieuw te renderen, een versiewijziging toont haar diff en git revert werkt. Er is geen tweede kopie met gerenderde geheimen. |
| Delen drift-herstel en GitOps één uitroltaak? | Ja: `cluster.apply` met de modus wijziging of herstel. | Twee taken, elk met een eigen gezondheidspoort. | Volgorde, overgeslagen controles en oude heartbeats worden één keer opgelost en met twee soorten gebruik getest. |
| Hoe sluiten schrijvende taken elkaar uit? | Het clusterslot in Go, over cluster en nodes heen. | Per soort een partiële unieke index plus losse controles. | Een index kijkt niet over soorten heen, en niet tegelijk naar `node_id` en `cluster_id`. Eén functie is één plek om te testen. |
| Opnieuw proberen vanaf de mislukte stap? | Nee: een nieuwe taak vanuit de actuele weergave. Hervatten na een herstart blijft, behalve bij `backup.verify`, dat dan meteen opruimt. | Retryable, met controles per stap en een overgang van failed terug naar applying. | De runner slaat geslaagde stappen over, dus een retry uren later voert oude beslissingen uit. |
| Hoe veroorzaakt de failovertest een storing? | Alleen via `apply.steps` (stoppen en weer starten), met herstel door de server. Prod met de hand. | `fault.inject` met een dodemansknop in de agent (protocol 5) vanaf het begin. | Eén storingspad. Valt de server uit, dan ontbreekt hooguit de reserve-node; de dodemansknop hoort bij fase 3. |
| Hoe meten we de overnametijd? | Een verplichte probe vanaf de server op het VIP. | Een extra heartbeat bij elke adreswissel, met de probe optioneel. | De heartbeat komt elke 10 s, te grof voor 'binnen 5 s'. De probe meet wat een client ziet. |
| Afhankelijkheden ontdekken via de agent? | Niet in fase 2: templates, facts en handmatige invoer. | Een optionele mijlpaal met uitgaande verbindingen. | Groot en riskant, met een nieuw berichttype, en een steekproef mist kortlevende verbindingen. |
| Hoeveel GitOps in fase 2? | Lezen, plannen, goedkeuren, toepassen, omhoog schalen en nieuwe clusters. | Ook `auto_apply` in lab, webhook, commitstatus en omlaag schalen. | Pollen met ETag kost niets, het token blijft alleen-lezen, en omlaag schalen kan met lifecycle-acties. |
| Gedeelde tabel voor test- en controleruns? | Eén `test_runs`, met eigen tabellen voor definities en sandboxes. | Per module een eigen runs-tabel. | Eén rapportpagina, één afronding en één bron voor fase 3. |
| Hoe isoleren we een sandbox? | `link_down` op elke netwerkkaart en een allowlist van configsleutels. | Vanaf het begin een afgesloten bridge, zoals `vmbr99`. | Geen voorbereiding in Proxmox, en nooit per ongeluk aan productie. De bridge komt als de handmatige proef erom vraagt. |
| Wat bewaren we van gerenderde inhoud? | Alleen HMAC's met een HKDF-sleutel uit `CF_MASTER_KEY`, plus grootte, mode, eigenaar en mtime. Geen Verschil tonen. | Een HMAC-sleutel in `server_secrets`, en Verschil tonen met maskering. | Een sleutel in dezelfde database helpt niet tegen een dump, en maskeren mist onbekende geheimen. |
| Bewaartermijn en export van het logboek? | Onbeperkt bewaren met een TRUNCATE-trigger, alleen NDJSON, optioneel elke nacht naar de NAS. | Een termijn van minstens 365 dagen, plus CSV. | Minder dan 200.000 regels per jaar. Een termijn voegt een verwijderpad toe aan een append-only tabel. |
| Extra drempel voor root-acties op prod? | De slug plus een admin met TOTP aan. | Alleen de slug, of een verse TOTP-code per actie. | TOTP is nu optioneel. De controle is één regel; een verse code vraagt een nieuw API-veld en meer UI. |
| Herstel voor clusters met een baseline? | Niet in fase 2: drift toont alleen. | Herstel van pakketten, services en mappen. | Een baseline kent van bestanden alleen een vingerafdruk. Wie herstel wil, zet het cluster in een template. |

## Vragen aan Jonas

De vragen staan in de volgorde waarin de mijlpalen ze nodig hebben. Zolang een antwoord ontbreekt, bouwen we met de standaard eronder.

- [ ] Is je Proxmox één cluster of zijn het losse hosts, welke versie draait er (8 of 9), en is er gedeelde storage of een Proxmox Backup Server? (vanaf mijlpaal 2, en vooral vanaf mijlpaal 8)

  Standaard zolang je niet antwoordt: één Proxmox VE 9-cluster met gedeelde storage en PBS. De code blijft veilig bij losse hosts: terugzetten gebeurt binnen dezelfde koppeling, verwijderen nooit met `destroy-unreferenced-disks`, en de README geeft de rechten voor PVE 8 en 9.

- [ ] Waar draait ClusterForge zelf (VM of LXC, welke VMID), en wordt die machine geback-upt? (vanaf mijlpaal 2)

  Standaard zolang je niet antwoordt: een VM of LXC op dezelfde Proxmox. Vanaf mijlpaal 2 zet je die VMID op de pagina Back-ups bij 'Ook bewaken', zodat je ziet of de database met de geheimen een verse back-up heeft.

- [ ] Hoe vaak maakt Proxmox back-ups van je VM's? (vanaf mijlpaal 2)

  Standaard zolang je niet antwoordt: dagelijks, dus een back-up geldt als te oud na 30 uur.

- [ ] Welke OS-versies draaien de nodes? (vanaf mijlpaal 5)

  Standaard zolang je niet antwoordt: Debian 12 en 13 en Ubuntu 22.04 en 24.04, met systemd, apt, dpkg en cgroup v2, zoals de apply-stappen al aannemen. Op een node zonder dpkg krijgen pakketstappen 'niet gecontroleerd' en geen drift.

- [ ] Mogen we de agents bijwerken met `install.sh --upgrade` en de golden image opnieuw bouwen? (vanaf mijlpaal 5 voor het bijwerken, vanaf mijlpaal 16 voor de golden image)

  Standaard zolang je niet antwoordt: ja, per node met het commando dat de UI toont. Nodes met een oude agent blijven werken, maar zonder drift en zonder diepe back-upcontrole.

- [ ] Wil je een nachtelijke kopie van het logboek op je NAS? (vanaf mijlpaal 5)

  Standaard zolang je niet antwoordt: geen kopie. Wijst `CF_EVENTS_EXPORT_DIR` naar een share met snapshots, dan schrijft ClusterForge daar elke nacht de events van de vorige dag als NDJSON.

- [ ] Hoe zijn je bestaande clusters opgebouwd: welke configuratiebestanden en diensten horen in een baseline? (vanaf mijlpaal 6)

  Standaard zolang je niet antwoordt: de presets per clustertype. Bij keepalived het pakket keepalived en `/etc/keepalived/keepalived.conf`, bij nginx nginx en `/etc/nginx/nginx.conf`, bij docker `/etc/docker/daemon.json` en bij cron `/etc/crontab`. De services komen uit de facts, en je past alles aan in de wizard.

- [ ] Welke clusters zijn prod, en mogen failovertests daar draaien? (vanaf mijlpaal 7, voor prod vanaf mijlpaal 11)

  Standaard zolang je niet antwoordt: tot mijlpaal 11 alleen lab en test. Daarna kan prod met de hand, met slug en TOTP, voor keepalived stoppen en de dienst stoppen, maar nooit gepland. VM hard uitzetten blijft voor lab en test.

- [ ] Kan de ClusterForge-server de VIP's rechtstreeks bereiken op HTTP of TCP, zonder proxy? (vanaf mijlpaal 7)

  Standaard zolang je niet antwoordt: ja, net als de HTTP-check na een uitrol. Kan dat voor een cluster niet, dan start een failovertest daar niet.

- [ ] Draaien er diensten die de agent nu niet volgt, zoals redis, php-fpm, memcached, rabbitmq of een PostgreSQL-instantie? (vanaf mijlpaal 9)

  Standaard zolang je niet antwoordt: de lijst `WatchedServices` wordt langer met `apache2`, `redis-server`, `memcached`, `rabbitmq-server`, `php8.1-fpm` tot en met `php8.4-fpm` en `postgresql@<versie>-main`.

- [ ] Heb je TOTP aangezet op je adminaccount? (vanaf mijlpaal 10)

  Standaard zolang je niet antwoordt: ja. Root-acties op prod vragen vanaf mijlpaal 10 een admin met TOTP aan.

- [ ] In welke tijdzone en welk venster mogen geplande tests (back-up en failover) draaien? (vanaf mijlpaal 11)

  Standaard zolang je niet antwoordt: `TZ=Europe/Brussels` en `CF_TEST_WINDOW` zondag 03:00 tot 05:00. De planning staat per cluster standaard uit.

- [ ] Welke GitHub-repository gebruik je voor GitOps, en mag ClusterForge uitgaand naar `api.github.com`? (vanaf mijlpaal 14)

  Standaard zolang je niet antwoordt: een private repository `clusterforge-config` met branch `main` en per cluster `clusters/<slug>/cluster.yaml`, en een fine-grained token met alleen leesrecht op die repository. ClusterForge pollt elke 60 s, zonder webhook.

- [ ] Draaien Grafana, VictoriaMetrics of Loki al ergens? (doet er in fase 2 voor geen enkele mijlpaal toe)

  Standaard zolang je niet antwoordt: geen enkele fase 2-module heeft ze nodig, want alles staat in PostgreSQL en in de events. Metrics zoals hersteltijd of overnametijd kunnen later via de bestaande ingest.

## Buiten fase 2

Fase 2 laat een aantal dingen bewust liggen. Een deel hoort bij fase 3 en bouwt daar verder op wat fase 2 neerlegt, een deel krijgt een eigen spoor of komt later als uitbreiding, en een deel blijft bij Proxmox of bij Jonas zelf.

**Fase 3: disaster simulations.** Storingen waarbij de agent zijn verbinding verliest, zoals een netwerkinterface down zetten of VRRP-verkeer blokkeren, horen hier, net als meerdere storingen tegelijk of na elkaar, een hele Proxmox-host uit, en een heel cluster samen terugzetten in een geïsoleerd netwerk. Daarvoor komen dan `fault.inject`, een dodemansknop in de agent en een extra heartbeat bij een adreswissel. Ze hergebruiken het testslot, `test_runs` en het sandbox-register.

**Fase 3: infrastructure mapping.** Afhankelijkheden ontdekken via de agent, uit luisterende poorten en TCP-verbindingen, en Proxmox-hosts, storage en netwerklagen in de graaf.

**Fase 3: What Broke, health score, risicoanalyse, AI-assistent en auto-documentatie.** Die lezen de events, `test_runs` en de `/audit`-API die fase 2 vult. Een SPOF- of risicoscore, automatisch reageren op een mislukte failovertest, wijzigingen als pull request voorstellen en een afdrukbaar back-uprapport horen daar ook.

**Eigen spoor na fase 2: databaseclusters.** De templates PostgreSQL HA en MariaDB HA, met replicatie, bootstrap van de eerste primary, failover van data en clusterspecifieke controles zoals replicatiestatus of Galera-quorum. Tot dan weigert de failovertest elk cluster waarin een databasedienst actief is.

**Latere uitbreidingen.** Deze punten passen in het ontwerp, maar hebben nog geen mijlpaal:

- geheimen via SOPS of Vault, en geheimen beheren in de webinterface;
- GitLab, Gitea of Forgejo als bron (de client zit al achter een interface), meerdere repositories of branches, en een lint-commando voor CI met dezelfde parser;
- omlaag schalen via Git, wat tot dan met de bestaande lifecycle-acties gaat;
- de vorm van VM's (cores, geheugen, schijf) wijzigen via Git en als drift tonen, waarvoor `proxmox_resources` de gegevens al heeft;
- drift van Docker-containers en compose-bestanden, en pakketten upgraden als aparte module;
- gebruikersbeheer in de webinterface (de eventnamen staan al in de catalogus), een auditor-rol, en events doorsturen naar Loki of een SIEM in het NDJSON-formaat dat er al is;
- meldingen per mail of ntfy bij een mislukte geplande test, en testresultaten als metrics in VictoriaMetrics;
- een templateparameter van het type dienst voor afhankelijkheden tussen clusters bij een uitrol, met de eerste template die hem nodig heeft.

**Blijft bij Proxmox of bij Jonas.**

- Automatisch herstellen en automatisch toepassen vanuit Git. Een mens beslist, ook in lab, en een hotfix tijdens een incident mag ClusterForge niet terugdraaien.
- Geplande failovertests op prod, en de VM hard uitzetten op prod.
- Back-ups maken, plannen en opruimen, en PBS beheren. Dat blijft bij Proxmox. Er komen ook geen back-ups op applicatieniveau (pg_dump, restic), geen terugzetten over de originele VM heen, en voor LXC alleen versheid en dekking.
- Naar Git schrijven. ClusterForge commit niet en zet geen commitstatus, zodat het token alleen leesrecht nodig heeft. Geheimen en templates komen nooit uit Git.
- Inhoud van bestanden tonen of bewaren. Grootte, mode, eigenaar en mtime volstaan, en Jonas heeft SSH.
- Een bewaartermijn of hash-keten voor het logboek. De nachtelijke kopie op de NAS is de kopie buiten de server.
- Realtime bewaking met inotify, onbeheerde extra's (pakketten, services of cronjobs die niet in de verwachte staat staan), drift voor losse nodes zonder cluster, en herstel voor clusters met een baseline.
