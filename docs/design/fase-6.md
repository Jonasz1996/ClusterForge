# ClusterForge – Technisch ontwerp fase 6: cronjobs en bestandssync

Stand: 2026-10-18

## Samenvatting

Jonas houdt zijn clusters op elkaar afgestemd met cronjobs: om de zoveel uur kopieert een script met rsync bestanden van de ene node naar de andere, of van het ene cluster naar het andere. Die cronjobs staan verspreid over crontabs en `/etc/cron.d` op elke node, niemand ziet of ze nog lopen, en een job die faalt merkt hij pas als er iets ontbreekt. lsyncd en GlusterFS heeft hij geprobeerd, maar die synchroniseren te veel.

Hij vroeg twee dingen: dat ClusterForge zijn cronjobs uitleest en duidelijk toont, en dat hij er ook taken kan bijzetten, aanpassen en opvolgen, zoals in Cronicle, maar dan passend bij clusters. Fase 6 doet dat in drie lagen:

| Module | Wat Jonas krijgt | Mijlpalen |
| --- | --- | --- |
| Zien | Een menu Cronjobs met elke cronjob en systemd-timer op elke node: wanneer hij draait in gewone taal, de volgende run, het commando, de gebruiker, de laatste run en of die lukte. Per cluster de verschillen tussen de nodes, en een waarschuwing bij jobs die naast een onderdeel van ClusterForge overbodig of gevaarlijk zijn. Een job die faalt, geeft een incident. | 1, 2 |
| Beheren | Eigen cronjobs in ClusterForge: aanmaken, aanpassen, aan- en uitzetten, nu uitvoeren en stoppen. Een job draait op de node met het VIP (en verhuist mee), op een vaste node, of op alle nodes tegelijk. Elke run met uitvoer, duur en exitcode, live te volgen. Een bestaande cronjob neemt ClusterForge met één knop over. | 3, 4, 5 |
| Bestandssync | Een cronjob van de soort Bestandssync: rsync van de node met het VIP naar de andere nodes, of naar een ander cluster, met mappen, uitsluitingen, een expliciete keuze voor verwijderen, een proefrun en per run het aantal bestanden en bytes. Zijn rsync-cronjobs worden zo beheerde syncs. | 6 |
| Bewaken | Status van het cluster, incidenten en What Broke voor cronjobs, het hoofdstuk Cronjobs in de documentatie en de assistent. | 2, 4, 7 |

Na mijlpaal 1 ziet Jonas al zijn cronjobs. Na mijlpaal 3 kan hij er zelf maken, na mijlpaal 5 zijn bestaande overnemen, en na mijlpaal 6 lopen zijn syncs als beheerde syncs met een overzicht per run.

**Uitgangspunten**

- **Eerst zien, dan veranderen.** Mijlpaal 1 en 2 lezen alleen. Een gevonden cronjob verandert pas als Jonas hem overneemt of uitzet, met een knop per job.
- **Gewone systemd op de node.** Een beheerde cronjob is een systemd-timer met een vaste service. Draait ClusterForge even niet, of verliest de node de verbinding, dan lopen de jobs gewoon door en meldt de agent de runs zodra hij weer verbonden is.
- **Clusters in de kern.** Een job hoort bij een cluster, niet bij een losse node, en weet waar hij moet draaien: op de node met het VIP, op een vaste node, of op alle nodes.
- **Een oude kopie overschrijft nooit de actieve node.** Een sync loopt standaard van de node met het VIP naar de andere nodes. De ontvangende node weigert als hij zelf het VIP heeft.
- **Niets verwijderen zonder dat Jonas het koos.** Een sync verwijdert geen bestanden op het doel, tenzij hij dat per sync aanzet.
- **Dezelfde regels als de rest van ClusterForge.** Alleen een beheerder maakt of wijzigt een job, elke wijziging staat met een diff in het logboek, en op prod vraagt het de slug en TOTP.
- **Geen wachtwoorden naar ClusterForge.** De agent maskeert wachtwoorden in de commando's van gevonden cronjobs voordat ze de node verlaten.

## Cron, systemd-timers of iets anders?

**Voor de jobs zelf: systemd-timers.** Een beheerde job wordt een timer `cf-cron-<id>.timer` die de service `cf-cron@<id>.service` start. Die service draait `cf-agent cron-run <id>`, dat het eigenlijke commando start en de run vastlegt.

| Kandidaat | Waarom wel of niet |
| --- | --- |
| **systemd-timer met cf-agent als starter** (gekozen) | Staat op elke Debian, overleeft een herstart van de agent, een gemiste run komt na een herstart alsnog (`Persistent=true`), en `systemctl list-timers` toont hem ook zonder ClusterForge. Omdat cf-agent het commando start, weet ClusterForge per run precies wanneer hij begon en stopte, met de exitcode en de uitvoer. |
| Een regel in `/etc/cron.d` | Werkt overal, maar cron legt geen exitcode of uitvoer vast. De run moet dan toch via een starter, en dan biedt cron niets wat een timer niet ook heeft. |
| Een planner in cf-agent zelf | Eén proces minder, maar een herstart of update van de agent laat runs vallen, en de jobs zijn onzichtbaar voor `systemctl`. |
| De server start de runs via NATS | Valt de verbinding weg, dan draait niets meer. Precies wat een cluster niet wil. |

Jonas schrijft het schema als een gewone cronregel (`0 */6 * * *`) of kiest het uit een lijst (elk uur, elke 6 uur, dagelijks om 03:00). ClusterForge zet het om naar `OnCalendar` en toont het in gewone taal met de volgende runs. Een cronregel met zowel een dag van de maand als een weekdag (cron leest dat als 'of', systemd als 'en') weigert het met uitleg.

**Waarom cf-agent de starter is.** De service draait als root. cf-agent leest de job uit `/etc/clusterforge/cron/<id>.json`, controleert of deze node aan de beurt is (bij een job op het VIP: heeft deze node het VIP), neemt een slot zodat dezelfde job nooit twee keer tegelijk loopt, en start het commando met `/bin/sh -c` als de gekozen gebruiker. Het schrijft de uitvoer naar een bestand, stopt het commando na de tijdslimiet, en legt begin en einde vast in een wachtrij op de node. De agent stuurt die wachtrij naar de server, met een bevestiging, zoals het wijzigingsjournaal. Zo gaat geen run verloren als de server even weg is.

**Waarom niet Cronicle zelf.** Cronicle is een aparte dienst met een eigen opslag, eigen gebruikers en eigen workers op elke node. ClusterForge heeft al een agent op elke node, de clusters, de VIP's, het logboek, de incidenten en de rechten. Een cronjob in ClusterForge weet dus welke node het VIP heeft, en een mislukte run komt in What Broke naast de rest van wat er op het cluster gebeurde.

## Bestandssync: waarom rsync op een timer

Jonas' bestanden veranderen de hele dag, maar een kopie om het uur of om de zes uur is genoeg. Dan past rsync op een timer:

| Kandidaat | Wat het doet | Waarom wel of niet |
| --- | --- | --- |
| **rsync op een timer** (gekozen) | Op een vast moment kopieert de bron alleen wat veranderd is. | Precies zo vaak als Jonas kiest, niet vaker. Geen dienst die altijd draait, geen extra laag onder de bestanden, en een mislukte run is een gewone exitcode. Wat er gebeurde, staat per run in de uitvoer. |
| lsyncd | Kijkt met inotify naar elke wijziging en start rsync binnen seconden. | Synchroniseert bij elke wijziging, ook een tijdelijk bestand of een half geschreven upload. Dat is wat Jonas 'te veel' vond. Watches op grote mappen kosten geheugen, en na een herstart begint het met een volledige vergelijking. |
| GlusterFS | Een gedeeld bestandssysteem over de nodes, via FUSE. | Elke schrijfactie gaat naar alle nodes tegelijk. Een extra laag onder de app, gevoelig voor split-brain, en traag bij veel kleine bestanden. In unprivileged CT's lastig door FUSE. |
| Syncthing | Synchroniseert in twee richtingen, continu. | Twee richtingen geeft conflicten zodra twee nodes hetzelfde bestand wijzigen, en het reageert ook op elke wijziging. |

**Wanneer gedeelde opslag beter is.** Als de app op meer dan één node tegelijk dezelfde bestanden schrijft (actief-actief), of als een bestand op de andere nodes binnen seconden aanwezig moet zijn, is een kopie om het uur niet genoeg. Dan hoort er gedeelde opslag onder: een NFS-share van een NAS of de Proxmox-host (eenvoudig, één plek), of CephFS als de Proxmox-hosts al Ceph draaien (geen enkel punt van uitval). De Nextcloud-data van Jonas staat bijvoorbeeld beter op NFS of CephFS zodra de app op alle drie de nodes tegelijk uploads aanneemt. Zolang er één actieve node is achter het VIP, en de andere nodes klaarstaan voor een failover, is rsync van de VIP-node genoeg. ClusterForge zegt dat ook in het plan van een sync: een interval korter dan vijf minuten raadt het af.

### Hoe een sync over de lijn gaat

rsync gaat over SSH, zoals Jonas' scripts nu waarschijnlijk ook doen, maar met een sleutel die alleen dat mag:

- Elke node in een sync krijgt een eigen sleutel `/etc/clusterforge/sync/id_ed25519`, die de agent zelf maakt. Alleen de publieke sleutel gaat naar ClusterForge.
- Op het doel zet de agent die sleutel in een blok tussen `# clusterforge-sync begin` en `# clusterforge-sync einde` in `/root/.ssh/authorized_keys`, met `restrict`, `from="<adressen van de bron-nodes>"` en `command="/usr/local/bin/cf-agent sync-receive --from <node>"`. De rest van het bestand blijft zoals het is.
- `cf-agent sync-receive` laat alleen `rsync --server` toe, met de opties die de sync zelf stuurt, naar precies de doelmap van een sync waarin die bron-node bron is. `--delete` alleen als die sync het mag. Heeft het doel zelf het VIP van zijn cluster, dan weigert het, zodat een oude kopie de actieve node niet overschrijft.
- De hostsleutels van de doelen gaan in `/etc/clusterforge/sync/known_hosts`, zodat de bron controleert met wie hij praat.

Zo kan de sleutel van de bron alleen naar de doelmappen schrijven, en niets anders op het doel doen. SSH moet aanstaan op de doelen, met `PermitRootLogin` op `prohibit-password` (de standaard van Debian) of `forced-commands-only`. Het plan van een sync controleert dat.

## Wat ClusterForge al heeft en wat ontbreekt

Bruikbaar zoals het is:

- **De agent op elke node** met een eigen NATS-subject per domein (`cmd`, `kube`, `db`) en het patroon van fase 5: een verzoek met deadline, een antwoord met fout, en de server die oude agents 'agent te oud' laat zien.
- **Het wijzigingsjournaal** (protocol 5): de agent bewaart regels tot de server ze bevestigt. Dezelfde aanpak brengt de runs van cronjobs over.
- **Deploystappen** (`apply.steps`) voor pakketten en bestanden, zoals rsync op een node zetten.
- **Het logboek, events, incidenten, What Broke, de clusterstatus, de documentatie en de assistent** met hun uitbreidingspunten.
- **De prod-bevestiging** met slug en TOTP (`ProdConfirm`), en de rollen admin en viewer.
- **VIP-eigenaar** per cluster uit de heartbeats, binnen een seconde na een verhuizing.

Wat ontbreekt:

| Gat | Gevolg | Opgelost in |
| --- | --- | --- |
| De agent leest geen crontabs of timers. | ClusterForge weet niet welke cronjobs er zijn. | Mijlpaal 1: agentprotocol 16 met het verzoek `cron list` |
| Cron legt standaard geen exitcode vast (Debian logt alleen de start). | Een gevonden job die faalt, is onzichtbaar. | Mijlpaal 2: met één knop `cron -L 15` via een drop-in, dan staan einde en exitcode in de journal |
| Er is geen manier om op een node iets op een schema te draaien en de uitkomst terug te krijgen. | Geen beheerde jobs. | Mijlpaal 3: timer, `cf-agent cron-run` en de wachtrij met runs |
| Een NATS-bericht is hoogstens 1 MiB. | De uitvoer van een run kan niet onbeperkt mee. | Bewust: per run de laatste 64 KiB in ClusterForge, de volledige uitvoer van de laatste tien runs op de node |
| Nodes praten niet met elkaar via ClusterForge. | Een sync kan niet via de agent. | Mijlpaal 6: rsync over SSH met een beperkte sleutel |

## Gedeelde bouwstenen

### Agentprotocol 16

Eén update van de agent voor de hele fase. Een eigen subject `cf.node.<id>.cron`, zodat een trage crontab de andere commando's niet ophoudt. De verzoeken:

| Verzoek | Wat het doet | Mijlpaal |
| --- | --- | --- |
| `list` | Alle cronjobs en timers op de node, met de laatste runs uit de journal. Wachtwoorden in commando's gemaskeerd. | 1 |
| `toggle` | Een gevonden cronregel uitzetten (in commentaar, met een merkteken) of weer aanzetten. | 5 |
| `apply` | De volledige set beheerde jobs van deze node: schrijft de jobbestanden en timers, zet ze aan, en haalt jobs weg die er niet meer bij horen. Geeft een hash terug. | 3 |
| `run` | Een job nu starten (`systemctl start --no-block`), of een lopende run stoppen. | 3, 4 |
| `output` | De uitvoer tot nu toe van een lopende run, of van een van de laatste tien runs. | 4 |
| `keys` | De publieke sleutel voor syncs en de hostsleutels van de node. | 6 |
| `dryrun` | Een sync als proef: rsync met `--dry-run --itemize-changes` naar één doel, met de lijst van wat zou veranderen. | 6 |

De runs komen op het subject `events` als bericht `cron_runs`, met een bevestiging. Een run heeft een id, de job, de node, begin en einde, exitcode, de reden (schema, handmatig, overgeslagen), de laatste 64 KiB uitvoer, en bij een sync de aantallen.

### Een beheerde job

```yaml
name: nextcloud-data-sync
cluster: nextcloud
kind: command            # of: sync
schedule: "0 */6 * * *"  # of een keuze uit de lijst
run_on: vip              # vip | node | all
node: nc1                # alleen bij run_on: node
user: root
command: |
  /usr/local/bin/opruimen.sh --ouder-dan 30
timeout: 1h
retries: 0               # 0 tot 3, met 1 minuut ertussen
enabled: true
alert: true              # een mislukte run geeft een incident
```

- **run_on: vip** draait op de node die het VIP van het cluster heeft. Op de andere nodes staat de timer ook, maar `cron-run` slaat daar over. Verhuist het VIP, dan draait de volgende run op de nieuwe node, zonder dat ClusterForge iets hoeft te doen. Tijdens een VIP-wissel kan een run vallen of, heel kort, op twee nodes starten; een job die dat niet mag, moet zelf idempotent zijn. Een cluster zonder VIP kan dit niet kiezen.
- **run_on: node** draait alleen op die node.
- **run_on: all** draait op elke node van het cluster, en de lijst toont de uitkomst per node.

Een sync is dezelfde job met `kind: sync` en in plaats van een commando:

```yaml
sync:
  source: /var/www/nextcloud/data/   # op de node met het VIP
  target: /var/www/nextcloud/data/   # standaard dezelfde map
  to: cluster                        # cluster: de andere nodes | <ander cluster>
  delete: false                      # bestanden op het doel weghalen die op de bron weg zijn
  exclude: ["*.part", "appdata_*/preview/"]
  bwlimit_mb: 0
```

### Tabellen

- `node_crons`: de gevonden cronjobs per node met hun laatste run, ververst elke vijf minuten.
- `cron_jobs`: de beheerde jobs met revisie; `cron_job_nodes`: per node welke hash de agent heeft toegepast.
- `cron_runs`: elke run, met de uitvoer. Runs ouder dan negentig dagen gaan weg, behalve de laatste per job en node.

## Mijlpalen in detail

**Mijlpaal 1: cronjobs lezen.** De agent leest `/etc/crontab`, `/etc/cron.d/*`, de crontabs in `/var/spool/cron/crontabs`, de mappen `cron.hourly`, `daily`, `weekly` en `monthly`, en elke systemd-timer met zijn service. Per job: bron, gebruiker, schema, commando (gemaskeerd), en de laatste run uit de journal (cron: de `CMD`-regels; timers: `LastTriggerUSec`, `Result` en `ExecMainStatus`). De server vraagt elke vijf minuten elke node met protocol 16, bewaart het en zet een event bij een job die bijkomt, verdwijnt of verandert, zodat What Broke 'cronjob gewijzigd' kan noemen. Het menu Cronjobs toont per cluster en per node elke job met het schema in gewone taal, de volgende run, het commando, de gebruiker en de laatste run. De pagina van een node krijgt een kaart Cronjobs.

**Mijlpaal 2: verschillen, waarschuwingen en fouten.** Per cluster legt ClusterForge de jobs van de nodes naast elkaar: 'staat alleen op nc1', 'wijkt af op nc3' (ander schema of commando). Waarschuwingen bij jobs die naast een onderdeel overbodig of gevaarlijk zijn: een Galera-bootstrap (`galera_new_cluster`, `--wsrep-new-cluster`) of een herstart van MariaDB of PostgreSQL op een cluster met het onderdeel Database, dat dat zelf doet; een script dat de primary van PostgreSQL zoekt naast Patroni; een rsync naar dezelfde map als een beheerde sync. Een timer die faalt, of een cronjob met een exitcode die niet nul is, geeft een event en een incident. De knop 'Fouten van cron vastleggen' zet een drop-in voor `cron.service` met `-L 15`, zodat cron einde en exitcode logt.

**Mijlpaal 3: beheerde cronjobs.** Aanmaken, aanpassen, aan- en uitzetten en weghalen, met een diff en prod-bevestiging. `cron apply` zet de jobs op de nodes; een lus om de vijf minuten herstelt een node die achterloopt, ook na een herinstallatie. `cf-agent cron-run` met slot, gebruiker, tijdslimiet en de wachtrij met runs. 'Nu uitvoeren' start de job op de juiste node. Een historie per job met elke run, per node, met duur en exitcode.

**Mijlpaal 4: opvolgen.** Een lopende run live volgen, de uitvoer van een run lezen, een run stoppen. Herhalen bij een fout. Een mislukte run geeft een incident (per job uit te zetten) en komt in What Broke. Een job die niet draaide toen hij moest (de timer stond uit, de node was weg) geeft een waarschuwing. De clusterstatus kijkt naar de laatste run van elke job. Op het overzicht: welke jobs nu draaien en welke de laatste dag mislukten.

**Mijlpaal 5: overnemen.** Bij elke gevonden cronjob 'Overnemen in ClusterForge': een formulier met schema, gebruiker en commando ingevuld, op die node of, als de job op alle nodes staat, op alle nodes of de VIP-node. Na het opslaan zet ClusterForge de oude regel in commentaar (met `# uitgezet door ClusterForge`), op elke node waar hij stond. 'Uitzetten' en 'Weer aanzetten' werken ook zonder overnemen. Een rsync-cronjob wordt herkend en vult in mijlpaal 6 het formulier van een sync in.

**Mijlpaal 6: bestandssync.** De soort Bestandssync met het plan (rsync op de nodes, SSH open, `PermitRootLogin`, ruimte op het doel), de sleutels, `sync-receive`, de runs met aantallen bestanden en bytes per doel, de proefrun met de lijst van wat zou veranderen, en van cluster naar cluster. Een rsync-cronjob van Jonas wordt een beheerde sync met dezelfde mappen en opties.

**Mijlpaal 7: documentatie en assistent.** Het hoofdstuk Cronjobs per cluster (jobs, schema's, waar ze draaien, syncs met hun mappen, en een runbook 'een sync terugdraaien'), de assistent-tools `cronjobs` en `cron_runs` die alleen lezen, en de README.

## Veiligheid

- **Een cronjob is vrije shell, als root.** Dat is het doel, maar dus alleen voor beheerders, met de volledige tekst in het logboek, een diff bij elke wijziging, en op prod de slug en TOTP. Een viewer ziet de jobs en runs, niet de uitvoer van een job die als root draait.
- **Wachtwoorden in commando's.** Gevonden commando's gaan gemaskeerd naar ClusterForge (`-p…`, `--password=…`, `PASSWORD=…`, `user:wachtwoord@` in een URL). Voor beheerde jobs raadt de UI een bestand op de node aan in plaats van een wachtwoord in het commando, en de uitvoer van een run wordt op dezelfde manier gemaskeerd.
- **De sleutel van een sync** kan alleen rsync naar de eigen doelmappen doen, vanaf de adressen van de bron-nodes, en verlaat de node nooit.
- **De agent** schrijft alleen onder `/etc/clusterforge/cron`, `/etc/systemd/system/cf-cron-*`, zijn eigen blok in `authorized_keys`, en zet alleen regels in commentaar die Jonas aanwees.

## Testen

- De lezer van crontabs en timers op vaste voorbeeldbestanden, met Jonas' soort regels (rsync, Galera-bootstrap, `@reboot`, `MAILTO`).
- De omzetting van cron naar `OnCalendar` tegen een eigen berekening van de volgende runs, en in CI tegen `systemd-analyze calendar`.
- `cron-run` met slot, gebruiker, tijdslimiet en de wachtrij, en de agent die de wachtrij na een onderbreking alsnog stuurt.
- Een sync tussen twee mappen op de runner met echte rsync en sshd op loopback, met `sync-receive` dat een andere map, `--delete` zonder toestemming en een doel met het VIP weigert.

## Bouwvolgorde

| # | Mijlpaal | Klaar als | Grootte |
| --- | --- | --- | --- |
| 1 | Cronjobs lezen | Agentprotocol 16. Het menu Cronjobs toont elke cronjob en timer van nc1 tot nc3 en db1 tot db3, met schema in gewone taal, volgende run en laatste run. Geen wachtwoord verlaat de node. | M |
| 2 | Verschillen, waarschuwingen en fouten | 'Staat alleen op nc1' en 'wijkt af', de Galera-cron op db1 tot db3 als gevaarlijk naast het onderdeel Database, een falende job als incident. | M |
| 3 | Beheerde cronjobs | Een job op het VIP van Nextcloud draait op de node met het VIP en verhuist mee; 'Nu uitvoeren' en de historie werken. | L |
| 4 | Opvolgen | Live uitvoer, stoppen, herhalen, incidenten, de clusterstatus en What Broke. | M |
| 5 | Overnemen | Een bestaande cronjob wordt met één knop een beheerde job, en de oude regel staat in commentaar. | S |
| 6 | Bestandssync | Een sync van de VIP-node naar de andere nodes en naar een ander cluster, met proefrun, aantallen per run en een doel dat weigert als het het VIP heeft. | L |
| 7 | Documentatie en assistent | Het hoofdstuk Cronjobs, twee tools voor de assistent en de README. | S |

**Waarom deze volgorde.** Eerst zien, zodat Jonas weet wat er nu draait voor er iets verandert. Dan de beheerde jobs met hun runs, omdat de sync er een soort van is. Overnemen komt vóór de sync, zodat zijn gewone cronjobs al beheerd kunnen worden terwijl de sync gebouwd wordt.

## Keuzes die al gemaakt zijn

Jonas wilde geen vragen vooraf. Dit zijn de standaarden; elk is later per job te veranderen.

| Keuze | Gekozen | Waarom |
| --- | --- | --- |
| Hoe draait een job op de node? | systemd-timer met `cf-agent cron-run` | Loopt door zonder ClusterForge, en elke run is te volgen. |
| Schema | Cronregel of een keuze uit de lijst, met `Persistent=true` | Jonas kent cron; een gemiste run komt na een herstart. |
| Waar draait een job standaard? | Op de node met het VIP | Past bij zijn actief-passieve clusters. |
| Twee runs van dezelfde job tegelijk? | Nooit; de tweede slaat over | Voorkomt dat een trage sync zichzelf inhaalt. |
| Tijdslimiet | Eén uur, per job aan te passen | Een hangende job blokkeert niet voor altijd. |
| Herhalen bij een fout | Niet, tenzij aangezet (tot drie keer) | Een herhaling kan meer kwaad doen dan goed. |
| Incident bij een mislukte run | Aan | Daarvoor wilde Jonas het overzicht. |
| Uitvoer bewaren | De laatste 64 KiB per run, negentig dagen | Genoeg voor een fout, zonder de database te vullen. |
| Bestandssync | rsync over SSH van de VIP-node, interval standaard elke 6 uur | Zie 'Bestandssync: waarom rsync op een timer'. |
| Verwijderen op het doel | Uit, per sync aan te zetten | Een fout op de bron mag geen data op het doel weghalen. |
| Gevonden cronjobs veranderen | Alleen in commentaar zetten, per job, na een klik | Niets verdwijnt, en terugzetten is één klik. |

## Buiten fase 6

**Bewust niet.**

- Synchroniseren bij elke wijziging (lsyncd) of in twee richtingen. Zie hierboven.
- Gedeelde opslag beheren (NFS, CephFS). ClusterForge zegt wanneer het beter past, maar zet het niet op.
- Taken die op elkaar wachten (job B na job A). Kan later, als Jonas het nodig heeft.
- Cronjobs in Kubernetes. Daar zijn CronJobs van Kubernetes zelf voor.

**Latere uitbreidingen.**

- Een melding per e-mail of webhook bij een mislukte run, naast het incident;
- jobs in `cluster.yaml` voor clusters die Git volgen;
- een sync met een snapshot op het doel (`--link-dest`), zodat een vorige versie terug te halen is.
