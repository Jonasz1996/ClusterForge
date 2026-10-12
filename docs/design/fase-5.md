# ClusterForge – Technisch ontwerp fase 5: een database per app

Stand: 2026-10-12

## Samenvatting

Jonas heeft nu één gedeeld databasecluster: drie zware nodes met MariaDB en PostgreSQL achter VIP .180, waar al zijn belangrijke apps op draaien. Sinds hij een firewall met VLAN's heeft, loopt elke app over de firewall naar die database. Hij wil het omdraaien: elke belangrijke app krijgt een eigen databasecluster op zijn eigen nodes, en het gedeelde cluster verdwijnt. Voor Nextcloud ziet dat er zo uit:

| | Nu | Straks |
| --- | --- | --- |
| Database | db1 .181, db2 .182, db3 .183, VIP .180 | geen apart cluster meer |
| Nextcloud | nc1 .186, nc2 .187, nc3 .188, VIP .185 | nc1 + mariadb1 .186, nc2 + mariadb2 .187, nc3 + mariadb3 .188, VIP .185 voor app en database |

Fase 5 laat ClusterForge dat uitrollen, de data verhuizen en de databases daarna beheren. Het antwoord op zijn vraag of dit een goed idee is, staat in het volgende hoofdstuk: ja, met drie nodes per app.

| Module | Wat Jonas krijgt | Mijlpalen |
| --- | --- | --- |
| Zien | Een menu Databases met elke MariaDB en PostgreSQL die ClusterForge ziet, ook het huidige gedeelde cluster: versie, rol, toestand van het cluster, databases met hun grootte. Per database welke app ze gebruikt, van welke node. | 1, 2 |
| Uitrollen | Een onderdeel Database op een bestaand cluster, ook op zijn CT's, of bij een nieuw cluster: MariaDB Galera of PostgreSQL met Patroni op dezelfde nodes als de app, met de app en de database op één VIP. Het plan telt per node het geheugen van app en database op. | 3, 4, 5, 10 |
| Verhuizen | Per app een geleide verhuizing van het gedeelde cluster naar het nieuwe: eerst een proef die meet hoe lang het duurt, dan een paar minuten onderhoud, een controle per tabel, en de nieuwe instellingen voor de app. De app houdt zijn gebruiker en wachtwoord. Daarna het oude cluster netjes afbouwen. | 8, 9, 14 |
| Beheer | Databases en gebruikers aanmaken, elke nacht een dump, terugzetten, het cluster opnieuw starten na een volledige uitval, een node opnieuw laten synchroniseren, overschakelen naar een andere primary, en updates node voor node. | 6, 7, 11 |
| Bewaken | Status, incidenten en What Broke voor databases, grafieken, failovertests die echt schrijven via het VIP, een scorekaart Database, documentatie met runbooks, en de assistent. | 12, 13, 15 |

Na mijlpaal 2 weet Jonas welke app welke database gebruikt. Na mijlpaal 4 draait MariaDB met het VIP op nc1 tot nc3, na mijlpaal 7 met back-ups, en na mijlpaal 9 is Nextcloud verhuisd. PostgreSQL volgt in mijlpaal 10.

**Uitgangspunten**

- **Niets te kiezen dat er niet toe doet.** Eén manier per database: Galera voor MariaDB, Patroni voor PostgreSQL. De pakketten van Debian, de versie die bij het OS van de nodes hoort, en verstandige standaarden.
- **Bestaande nodes doen mee.** Een database heeft geen kernelmodules of speciale rechten nodig, dus ze kan op Jonas' CT's. Fase 5 maakt daarom van de database een onderdeel dat op nodes komt die er al zijn, en geen template die nieuwe VM's maakt.
- **De data gaat van node naar node.** Een dump gaat nooit door ClusterForge. De nieuwe node haalt hem rechtstreeks bij de oude database, over de weg die de app nu al gebruikt. Zo is er geen nieuwe firewallregel nodig.
- **Een mens beslist.** ClusterForge zet een app niet zelf in onderhoud, past de instellingen van een app niet aan en verwijdert geen oude database zonder getypte bevestiging. Het zegt bij elke stap wat Jonas moet doen, met het commando erbij.
- **Geen gegevens uit tabellen.** De agent stuurt namen, groottes, aantallen en toestanden, nooit de inhoud van een rij en nooit een wachtwoord in leesbare vorm.
- **Dezelfde regels als de rest van ClusterForge.** Elke actie in het logboek, het clusterslot, de gezondheidspoort na elke node, en op prod de slug en TOTP.
- **Eén agentupdate.** Agentprotocol 9 (mijlpaal 1) brengt alles wat de agent voor databases nodig heeft.

## Is een database per app een goed idee?

Ja. Voor Jonas' opstelling, met een firewall tussen de VLAN's en apps die elk drie nodes hebben, is het beter dan één gedeeld cluster.

**Wat het oplevert**

- **Geen firewallregels tussen app en database.** Het verkeer blijft binnen het VLAN van de app en gaat niet door de firewall. Dat is ook sneller, en de database is vanuit andere VLAN's niet meer bereikbaar.
- **Een storing raakt één app.** Nu valt bij een probleem op db1 tot db3 elke app tegelijk uit. Straks raakt een kapotte database alleen haar eigen app.
- **Onderhoud per app.** Een update, een herstart of een terugzetactie raakt één app, op het moment dat het die app uitkomt. Elke app kan zijn eigen versie hebben.
- **De drie zware nodes komen vrij.** Hun geheugen en CPU kunnen naar de app-nodes.

**Wat het kost**

- **Geheugen en CPU per node.** Elke app-node draait nu ook een database. Het plan telt per node het geheugen van de app en de database op en zegt hoeveel erbij moet (zie 'Uitrollen op een cluster').
- **Meer databaseclusters.** In plaats van één cluster zijn er straks een paar. Updates, back-ups, status en failovertests doet ClusterForge per cluster, zodat dat niet meer werk wordt voor Jonas.
- **Een node die uitvalt, neemt app en database tegelijk mee.** Met drie nodes blijven er twee over, en dat is genoeg voor beide.

**Waar het niet past**

- Een database die door meer dan één app gebruikt wordt. Die kan maar bij één app staan, en de andere app moet dan alsnog door de firewall. ClusterForge laat zulke databases zien (mijlpaal 2), zodat Jonas per geval kiest: meeverhuizen met één firewallregel voor de andere app, of op het oude cluster laten.
- Een app met één node. Een database met automatische failover heeft minstens drie stemmen nodig (zie hieronder).

## De keuzes

### MariaDB: Galera

| Kandidaat | Wat het is | Waarom wel of niet |
| --- | --- | --- |
| **Galera** (gekozen) | Elke node heeft alle data, en een schrijfactie is pas klaar als de meerderheid ze heeft. | Zit in MariaDB en Debian. Valt een node uit, dan werken de andere gewoon door, zonder overschakelen en zonder dataverlies. Een node die terugkomt, haalt zelf bij. |
| Primary en replica | Eén node schrijft, de andere volgen met een kleine vertraging. | Werkt met twee nodes, maar overschakelen is handwerk en een replica kan net achterlopen. |
| MaxScale of ProxySQL ervoor | Een proxy die schrijven en lezen verdeelt. | Nog een dienst om te beheren, en MaxScale heeft een andere licentie. Met één schrijvende node via het VIP is het niet nodig. |

De app schrijft altijd via het VIP, dus naar één node. Dat voorkomt de conflicten die Galera geeft als twee nodes tegelijk dezelfde rij wijzigen. Galera repliceert alleen InnoDB, en een tabel zonder primaire sleutel werkt slecht. De proef van een verhuizing meldt zulke tabellen.

### PostgreSQL: Patroni

| Kandidaat | Wat het is | Waarom wel of niet |
| --- | --- | --- |
| **Patroni met etcd** (gekozen) | Patroni start PostgreSQL, houdt één primary en zet de rest als replica. etcd houdt bij wie de primary is. | De standaard voor PostgreSQL met automatische failover, in Debian verpakt. Kiest nooit twee primaries. Overschakelen en een replica opnieuw opbouwen zijn één commando. |
| repmgr met keepalived | Replicatie met een eigen failoverdienst. | Minder strikt: zonder meerderheidsstem is een tweede primary mogelijk. |
| pg_auto_failover | Een monitornode beslist over failover. | De monitor is zelf een enkel punt van uitval. |
| Alleen replicatie | Streaming replication zonder automatische failover. | Overschakelen is handwerk, net als bij twee nodes zonder scheidsrechter. |

etcd draait op dezelfde drie nodes. Patroni komt uit Debian. Voor etcd gebruiken we het Debian-pakket als het er is voor de versie van de nodes, en anders de binary van de GitHub-release met een vaste SHA-256, zoals k3s in fase 4. Dat zoeken we uit in mijlpaal 10.

### Twee of drie nodes

Galera en etcd hebben een meerderheid nodig om door te werken. Met drie nodes blijft er na één uitval een meerderheid van twee over. Met twee nodes weet de overgebleven node niet of de andere weg is of alleen onbereikbaar, en dan stopt hij om te voorkomen dat beide kanten verder schrijven.

Twee nodes kan daarom alleen met een **scheidsrechter**: een kleine derde node in hetzelfde netwerk die alleen meestemt en geen data heeft. Voor MariaDB is dat `garbd` (pakket `galera-arbitrator-4`), voor PostgreSQL een derde etcd-lid. Eén vCPU en 512 MB is genoeg. Die node hoort bij het cluster met de rol scheidsrechter. Jonas maakt er een CT voor, of ClusterForge maakt een kleine VM uit de golden image. Zonder scheidsrechter weigert het plan twee nodes.

De standaard is drie nodes, zoals nc1 tot nc3.

### Eén VIP of twee

Jonas wil één VIP voor app en database, zoals .185 voor Nextcloud. Bij MariaDB kan dat. Elke Galera-node kan schrijven, dus de database volgt het VIP van de app. Keepalived kijkt dan op elke node naar de app en naar Galera. Is de database op de node met het VIP niet gezond, dan gaat het VIP naar een andere node.

Bij PostgreSQL kan dat niet. Er is maar één primary, en die kiest Patroni, niet keepalived. Viel de app op de primary uit, dan zou het VIP naar een node gaan waar alleen een replica draait, en daar kan de app niet schrijven. PostgreSQL krijgt daarom een eigen VIP in hetzelfde VLAN, dat altijd op de primary staat. Voor Nextcloud op PostgreSQL wordt dat bijvoorbeeld .189.

| Database | Standaard | Kan ook |
| --- | --- | --- |
| MariaDB Galera | Het VIP van de app (gedeeld) | Een eigen VIP |
| PostgreSQL met Patroni | Een eigen VIP dat de primary volgt | (niet anders) |

**Waarom geen HAProxy.** In Jonas' huidige opstelling verdeelt HAProxy het verkeer over de drie Galera-nodes, en zoekt een script welke PostgreSQL primary is. Dat is niet meer nodig. Bij Galera stuurt het VIP al het verkeer naar één node: schrijven op drie nodes tegelijk geeft conflicten op dezelfde rijen, dus één schrijver is veiliger, en de andere nodes staan klaar. Bij PostgreSQL volgt het eigen VIP de primary, en dat doet wat het script deed. Een HAProxy die Jonas voor andere dingen gebruikt, blijft gewoon staan.

### CT's en VM's

De database komt op de nodes die er al zijn, dus ook op Jonas' CT's. MariaDB, PostgreSQL, Patroni, etcd en keepalived draaien gewoon in een unprivileged LXC, en Jonas gebruikt keepalived al in zijn CT's. Dat is anders dan bij Kubernetes in fase 4, dat kernelmodules nodig heeft.

Een nieuw cluster met database rolt ClusterForge uit als VM's, zoals elk nieuw cluster. Een node erbij voor een CT-cluster maakt Jonas zelf als CT. Zodra de agent erop draait, voegt 'Node toevoegen aan de database' hem toe.

## Wat ClusterForge al heeft en wat ontbreekt

Bruikbaar zoals het is:

- **De stappen van een template** (`package`, `file`, `service`, `user`, `directory` en `command`) en `ApplySteps`, die ze op een node toepassen, ook buiten een uitrol (`internal/templates/templates.go:603`).
- **De uitrol node voor node** met de gezondheidspoort, het clusterslot en de VIP-eigenaar als laatste (`internal/rollout`).
- **Geheimen** met het type `secret`, versleuteld met `CF_MASTER_KEY` en gemaskeerd als `[geheim]`.
- **Keepalived** met VIP-volging, failovertests, de wat-als en de DR-repetitie.
- **Verbindingen** die de agent elke minuut ziet (fase 3), en voorgestelde afhankelijkheden in de graaf, met de soort `database` (`internal/deps/kinds.go:16`). ClusterForge ziet dus al dat nc1 met .180 op poort 3306 praat, maar niet met welke database.
- **De clustertypes** `mariadb_ha` en `postgresql_ha` (`internal/inventory/validate.go:16`), voor het huidige gedeelde cluster.
- **De diepe back-upcontrole** herkent al Galera en Patroni in een sandbox (`internal/agent/verify.go:353`).
- **Incidenten, What Broke, de health score, de documentatie en de assistent** met hun uitbreidingspunten.

Wat ontbreekt:

| Gat | Gevolg | Opgelost in |
| --- | --- | --- |
| Er is geen template voor MariaDB HA of PostgreSQL HA. Fase 2 schoof ze door naar een eigen spoor. | ClusterForge kan geen databasecluster bouwen. | Mijlpaal 3 en 10 |
| Een template maakt altijd nieuwe VM's. Een cluster dat Jonas zelf bouwde, heeft alleen een baseline en geen spec (`internal/drift/report.go:63`, `internal/deploy/spec.go:15`). | Een database op nc1 tot nc3 kan niet. | Mijlpaal 3: het onderdeel, met een eigen spec naast de baseline of de template |
| De agent kent geen databasecommando's. Verify leest alleen `SHOW DATABASES` in een sandbox (`internal/agent/verify.go:762`). | Geen status, geen gebruikers, geen dump. | Mijlpaal 1 en verder: agentprotocol 9 |
| Verbindingen tonen poorten, geen databases. | Welke app welke database gebruikt, is niet te zien. | Mijlpaal 2 |
| Keepalived in een zelfgebouwd cluster is met de hand ingesteld. ClusterForge leest alleen de VIP's uit de heartbeats. | Het VIP kan niet op de gezondheid van de database letten. | Mijlpaal 4 |
| Een NATS-bericht is hoogstens 1 MiB (`internal/agentbus/bus.go:117`). | Een dump past niet door ClusterForge. | Bewust: de nieuwe node haalt de dump zelf bij de oude database |
| Failovertests weigeren een cluster met een database (`internal/failover/service.go:703`). | Een databasecluster wordt nooit getest. | Mijlpaal 13 |
| De clusterstatus kijkt naar nodes, VIP's en Kubernetes (`internal/status/rules.go`). | Een Galera-cluster zonder meerderheid of een PostgreSQL zonder primary is onzichtbaar. | Mijlpaal 12 |
| Back-ups zijn alleen de Proxmox-back-ups, en de sandbox werkt niet voor CT's. | Een losse database terugzetten kan niet. | Mijlpaal 7 |

## Gedeelde bouwstenen

### Het onderdeel Database

Een onderdeel is een stuk gewenste staat dat op de nodes van een bestaand cluster komt, naast wat er al draait. Het heeft een eigen spec met revisies in een nieuwe tabel `cluster_components`. Zo werkt het op een cluster uit een template en op een cluster met een baseline. Een cluster heeft hoogstens één MariaDB en één PostgreSQL.

```yaml
component: mariadb
version: 1.0.0          # versie van het onderdeel
mode: galera
nodes:
  - { node: nc1, role: data }
  - { node: nc2, role: data }
  - { node: nc3, role: data }
vip: shared             # of: { address: <vrij IP in het VLAN> }
settings:
  buffer_pool: 1G
  max_connections: 200
backup: { at: "02:00", keep_days: 7 }
```

De stappen staan in ingebouwde onderdelen in hetzelfde formaat als templates, `mariadb-galera` en `postgresql-patroni`, maar zonder VM's en rollen. De wachtwoorden voor de replicatie en de REST-API van Patroni zijn `secret`-parameters die ClusterForge zelf maakt. SST bij Galera heeft geen wachtwoord nodig: mariabackup logt in als de gebruiker `mysql` via de socket (`unix_socket`), die MariaDB zelf aanmaakt. Toepassen gaat met de bestaande uitroltaak, node voor node, met de gezondheidspoort na elke node. Die poort kijkt nu ook naar de database: Galera Synced met de verwachte clustergrootte, of Patroni running met een replica die bij is.

De drift kent het onderdeel. Een met de hand aangepaste `99-clusterforge-galera.cnf` valt dus op, ook op een cluster met een baseline. Een nieuw cluster uit een template kan het onderdeel meteen krijgen: 'Cluster uitrollen' heeft een stap 'Database erbij', en de uitrol voegt het toe nadat de VM's klaar zijn.

**MariaDB Galera.** De stappen installeren `mariadb-server`, `mariadb-backup` en `galera-4`, en schrijven `/etc/mysql/mariadb.conf.d/99-clusterforge-galera.cnf` (een eigen bestand, zodat het `60-galera.cnf` van het Debian-pakket ongemoeid blijft en een update van MariaDB niets vraagt) met onder meer:

- `wsrep_on`, de provider, `wsrep_cluster_name` (de slug) en `wsrep_cluster_address` met de adressen van de nodes;
- `wsrep_sst_method=mariabackup` met `wsrep_sst_auth=mysql:`, dus via de socket en zonder wachtwoord op de node;
- `binlog_format=ROW`, `innodb_autoinc_lock_mode=2` en de buffer pool uit het plan.

De eerste node start het cluster met `galera_new_cluster`, eenmalig met een merkteken als `creates`. De andere nodes starten gewoon en halen alle data bij de eerste. Staat er al een MariaDB met eigen databases op een node, dan weigert het plan.

**PostgreSQL met Patroni (mijlpaal 10).** De stappen installeren PostgreSQL, Patroni en etcd, en halen het lege cluster `main` weg dat Debian bij de installatie maakt. Patroni krijgt `/etc/patroni/clusterforge.yml` met etcd op de drie nodes, de replicatie, `pg_hba` voor de nodes van het cluster, en `synchronous_mode` aan zonder `strict`. Zo verliest een gewone failover geen bevestigde schrijfactie, en blijft een node alleen toch schrijven.

De versie is die van Debian op de nodes: MariaDB 10.11 en PostgreSQL 15 op Debian 12, MariaDB 11.8 en PostgreSQL 17 op Debian 13. Alle nodes van één database moeten dezelfde Debian-versie hebben. Het plan leest dat uit de facts.

### Agentprotocol 9: databasecommando's

De agent praat met de database via de socket: als root met `unix_socket` bij MariaDB, en als `postgres` via peer-authenticatie bij PostgreSQL. Dat is de standaard van Debian, dus de agent heeft geen wachtwoord nodig. Lukt het niet, dan zegt hij waarom.

| Commando | Wat |
| --- | --- |
| `db.status` | Engine, versie, rol, de toestand van Galera of Patroni, de databases met grootte en aantal tabellen, en de verbindingen per database, gebruiker en clientadres. |
| `db.users` | Gebruikers met hun hosts en rechten per database, zonder wachtwoord. Alleen binnen een verhuizing ook de hash, zie 'Verhuizen'. |
| `db.ensure` | Een database, gebruiker of recht aanmaken of bijwerken. Een wachtwoord of hash gaat één keer mee en gaat via stdin naar de database, nooit als argument. |
| `db.drop` | Een database of gebruiker weghalen, alleen met de naam die de server bevestigd meestuurt. |
| `db.migrate` | Op de doelnode: een dump bij de bron halen en meteen importeren, met voortgang in bytes. Ook als proef in een tijdelijke database. |
| `db.compare` | Het aantal rijen per tabel, om bron en doel te vergelijken. |
| `db.dump` | Een dump maken, de dumps tonen, oude opruimen, en een dump terugzetten. |
| `db.galera` | De positie (seqno) en `safe_to_bootstrap` van een node, het cluster starten op deze node, en een node opnieuw laten synchroniseren. |
| `db.patroni` | De leden met rol en achterstand, overschakelen, herstarten en een replica opnieuw opbouwen. |
| `db.probe` | Een schrijftest via het VIP, voor de failovertests. |
| `keepalived.track` | Een controle van de database toevoegen aan de keepalived-instance met een gegeven VIP (zie hieronder). |

Namen van databases en gebruikers controleert de agent tegen `^[a-z0-9_]{1,63}$`. Vrije SQL bestaat in de brug niet. De heartbeat krijgt een klein blok met de toestand van de database, zodat de status binnen tien seconden klopt.

Een agent op protocol 8 werkt door, maar het menu Databases toont dan 'agent te oud', met het commando om bij te werken. Dat geldt ook voor de oude nodes db1 tot db3: zonder agent op protocol 9 daar ziet ClusterForge niet wie welke database gebruikt.

### Keepalived en `cf-agent check db`

De agent krijgt een subcommando `cf-agent check db`, dat keepalived elke twee seconden aanroept. Het slaagt als de database op deze node mag schrijven: bij Galera `wsrep_local_state` 4 (Synced) in een Primary-component, bij Patroni een 200 op `/primary` van de lokale REST-API.

- **Gedeeld VIP.** Het VIP van de app krijgt `track_script { cf_db }` erbij, naast de controle van de app. Bij een cluster uit de template Nginx met keepalived komt dat in een nieuwe templateversie. Bij een zelfgebouwd cluster past `keepalived.track` de `keepalived.conf` van Jonas aan met een kleine, vaste wijziging: een `include` van `/etc/keepalived/conf.d/*.conf` en het `track_script` in de instance met dat VIP. Het plan toont de diff, met `auth_pass` gemaskeerd, en herlaadt keepalived node voor node met de VIP-eigenaar als laatste.
- **Eigen VIP.** Een eigen `vrrp_instance` in `/etc/keepalived/conf.d/clusterforge-db.conf`, met een vrij VRRP-id en een `secret` als wachtwoord. Heeft het cluster nog geen keepalived, dan installeert het onderdeel het.

Het plan controleert dat het VIP vrij is en in hetzelfde netwerk ligt als de nodes, zoals bij een uitrol.

### Datamodel

| Tabel | Inhoud | Bewaard |
| --- | --- | --- |
| `cluster_components` | Het onderdeel per cluster: soort, spec en revisie | met revisies |
| `db_servers` | Per node: engine, versie, rol, toestand, laatste status en fout | actueel |
| `db_databases` | Per server of cluster: naam, grootte, aantal tabellen | actueel |
| `db_clients` | Database, gebruiker, clientadres, de gekoppelde node en het cluster, eerst en laatst gezien | tot 30 dagen na laatst gezien |
| `db_migrations` | Verhuizing: bron, doel, databases, stap, metingen, controles en resultaat | altijd |
| `db_dumps` | Per dump: database, node, pad, grootte, duur en controle | zolang de dump bestaat |

Verbindingen, query's per seconde, de buffer pool, de achterstand van replica's, flow control van Galera en de grootte per database gaan elke minuut naar VictoriaMetrics, met de labels van de node plus `database`.

## Uitrollen op een cluster

Op de pagina van een cluster staat 'Database toevoegen'. Jonas kiest MariaDB of PostgreSQL, de nodes, het VIP en zo nodig een scheidsrechter. Het plan toont per node wat er gebeurt en of het past.

- **Geheugen.** Het geheugen van de node (uit de facts, en voor een CT of VM ook uit Proxmox), wat de app de afgelopen zeven dagen hoogstens gebruikte (uit VictoriaMetrics), en wat de database nodig heeft: de buffer pool plus ongeveer 300 MB. De buffer pool is standaard de grootte van de data plus een kwart, met een minimum van 256 MB, en hoogstens de helft van wat vrij is. Blijft er minder dan 15% over, dan zegt het plan hoeveel erbij moet, met het commando, bijvoorbeeld `pct set 186 --memory 6144`.
- **Schijf.** De data twee keer (de database en een dump), plus ruimte voor de logs van de replicatie.
- **Proxmox-hosts.** Staan twee nodes van de database op dezelfde Proxmox-host, dan waarschuwt het plan: valt die host uit, dan is de meerderheid weg.
- **Bestaande diensten.** Een MariaDB of PostgreSQL die al op een node draait met eigen databases, een poort die bezet is, of een andere Debian-versie op één node.

De grootte van de data komt uit het gedeelde cluster als Jonas een database kiest die hij gaat verhuizen. Na bevestiging volgt de uitrol node voor node. Na de laatste node schrijft ClusterForge een testrij via het VIP en leest ze op elke node terug.

## Wie gebruikt welke database

Mijlpaal 2 leert ClusterForge welke app welke database gebruikt, eerst op het gedeelde cluster. De agent op db1 tot db3 kijkt elke minuut in `information_schema.PROCESSLIST` of `pg_stat_activity` welke gebruiker vanaf welk adres met welke database verbonden is. ClusterForge koppelt die adressen aan nodes en clusters. Een adres dat het niet kent, staat er als 'onbekend adres'. Omdat cronjobs niet de hele tijd verbonden zijn, telt alles van de laatste dertig dagen mee.

Het menu Databases toont dan per database:

- de grootte en het aantal tabellen;
- welke clusters en nodes ze gebruiken, en wanneer voor het laatst;
- een waarschuwing als meer dan één cluster ze gebruikt;
- een voorstel voor de afhankelijkheidsgraaf, zoals 'nextcloud hangt af van de database nextcloud op db-gedeeld', dat Jonas bevestigt of negeert;
- een knop 'Verhuizen naar …' met het cluster dat de database gebruikt.

## Verhuizen

Per app verhuist ClusterForge de databases van het gedeelde cluster naar het nieuwe onderdeel, met een dump en een import. De app staat daarvoor een paar minuten in onderhoud. Zonder onderhoud verhuizen, met replicatie, staat onder 'Buiten fase 5'.

### De weg van de data

De nieuwe primary, bijvoorbeeld nc1, haalt de dump met `mariadb-dump` of `pg_dump` rechtstreeks bij het VIP van het gedeelde cluster, en importeert hem meteen in de nieuwe database. Dat is dezelfde weg die Nextcloud nu gebruikt, van nc1 naar .180 op poort 3306, dus de firewall laat het al toe. De dump gaat niet door ClusterForge en staat niet op schijf.

Daarvoor maakt de agent op een node van het gedeelde cluster een tijdelijke gebruiker `cf_verhuizing_…`. Die mag alleen lezen, alleen in die ene database, alleen vanaf het adres van de doelnode, en verdwijnt na de verhuizing, ook als die mislukt. Laat `pg_hba` van het oude PostgreSQL-cluster zo'n gebruiker niet toe, dan vraagt ClusterForge één keer het wachtwoord van de app, zoals het in de configuratie van de app staat. Het gebruikt dat alleen voor deze verhuizing.

**De app houdt zijn gebruiker en wachtwoord.** ClusterForge leest op het oude cluster de gebruikers met rechten op de database, met de hash van hun wachtwoord, en maakt ze op het nieuwe cluster met dezelfde hash. ClusterForge kent het wachtwoord dus niet, en de app hoeft alleen het adres van de database te veranderen. De hash gaat via ClusterForge van de oude naar de nieuwe agent, staat alleen versleuteld in de taak, en komt nooit in het logboek, de events of de API. De hosts van zo'n gebruiker worden de adressen van de nodes van het nieuwe cluster en `localhost`.

### Plan en proef

Het plan van een verhuizing controleert:

- of de doelnode de oude database bereikt en de tijdelijke gebruiker kan inloggen;
- de versies: van oud naar nieuwer kan, terug naar een oudere versie niet;
- de grootte, de vrije schijf en het geheugen op de nieuwe nodes;
- bij MariaDB: tabellen zonder primaire sleutel en tabellen die geen InnoDB zijn;
- bij PostgreSQL: of de extensies van de database op de nieuwe nodes bestaan;
- welke clients de database de laatste dertig dagen gebruikten, en of er een buiten het doelcluster bij zit.

De **proefverhuizing** doet alles behalve het onderhoud. Ze importeert in een tijdelijke database `<naam>_proef` terwijl de app gewoon doorwerkt, meet hoe lang het duurt, vergelijkt de tabellen en gooit de kopie daarna weg. Zo weet Jonas vooraf hoe lang het onderhoud duurt. Omdat de app doorschrijft, wijken de aantallen bij de proef een beetje af. Het verslag zegt dat erbij.

### Uitvoeren

De verhuizing is een taak met vaste stappen die na een herstart van de server verder gaat. Ze is niet Retryable en vraagt op prod de slug en TOTP.

1. **Controle.** Het plan opnieuw, met de toestand van nu.
2. **Onderhoud.** De taak wacht tot Jonas de app in onderhoud zet. Voor bekende apps staat het commando erbij, voor Nextcloud `sudo -u www-data php occ maintenance:mode --on`. ClusterForge toont daarna de verbindingen die nog openstaan naar de oude database. Zijn die weg, of kiest Jonas om toch door te gaan, dan gaat de taak verder.
3. **Gebruikers en database** op het nieuwe cluster, met de hashes van het oude.
4. **Dump en import**, met de voortgang in megabytes en de verwachte resterende tijd.
5. **Controle per tabel.** Het aantal rijen op het oude en het nieuwe cluster moet gelijk zijn. Wijkt er een af, dan stopt de taak. Er is dan niets verloren: de app staat nog op de oude database, en Jonas zet het onderhoud weer uit.
6. **Overschakelen.** ClusterForge toont de nieuwe instellingen: het adres is het VIP van het nieuwe cluster, en gebruiker en wachtwoord blijven hetzelfde. Voor Nextcloud: `sudo -u www-data php occ config:system:set dbhost --value=<VIP van Nextcloud>`. Jonas past het aan, zet het onderhoud uit en klikt 'De app draait op de nieuwe database'. ClusterForge controleert dat de app-nodes nu met het nieuwe cluster verbonden zijn en niet meer met het oude.
7. **De oude database op slot.** De gebruikers van de app verliezen hun schrijfrechten op de oude database, zodat een vergeten cronjob niet stilletjes daar verder schrijft. Eén knop zet ze terug. De tijdelijke gebruiker verdwijnt.

**Terug.** Tot en met stap 5 is stoppen veilig, want de app gebruikt nog de oude database. Na stap 6 kan terug zolang de oude database bestaat: dezelfde verhuizing in de andere richting.

### Het oude cluster afbouwen

Een verhuisde database blijft op het oude cluster staan, op slot. Is er zeven dagen geen enkele verbinding meer mee geweest, dan biedt ClusterForge 'Afbouwen' aan (mijlpaal 14):

1. een laatste dump op een node van het oude cluster, die blijft staan tot Jonas hem weghaalt;
2. de database en haar gebruikers weghalen, na het intikken van de naam van de database, en op prod met de slug en TOTP.

Staat er geen database van een app meer op het oude cluster, dan zegt ClusterForge dat db1 tot db3 leeg zijn. De CT's weghalen doet Jonas in Proxmox. ClusterForge verwijdert geen CT's.

## Databases en gebruikers

Mijlpaal 6 geeft elk onderdeel een tabblad Databases met de databases (grootte, tabellen, verbindingen) en de gebruikers (naam, hosts, rechten per database). 'Database aanmaken' maakt een database met een eigen gebruiker en een sterk wachtwoord, met alle rechten op die database, vanaf de nodes van het cluster. Het wachtwoord staat één keer op het scherm, samen met de instellingen voor de app. ClusterForge bewaart het niet. Kwijt betekent 'Nieuw wachtwoord'.

Elke actie staat in het logboek, met de namen en zonder wachtwoord. Een database weghalen vraagt de naam, en op prod ook de slug en TOTP.

## Back-ups

- **Elke nacht een dump** van elke database van het onderdeel, standaard om 02:00, dus buiten het testvenster van zondag 03:00 tot 05:00. Bij Galera op een node zonder het VIP, zodat de schrijvende node geen extra last krijgt. Bij PostgreSQL op een replica.
- **Op de node zelf**, in `/var/backups/clusterforge-db/<database>/` met modus 0700, gecomprimeerd met zstd, zeven dagen bewaard. De Proxmox-back-up van de CT of VM neemt die map mee, dus er is ook een kopie buiten de node.
- **Gecontroleerd.** Een dump moet compleet zijn en mag niet de helft kleiner zijn dan de vorige. Bij Back-ups en in de score staat per database de leeftijd van de nieuwste dump.
- **'Dump nu'** voor een database, bijvoorbeeld voor een update van de app.
- **Terugzetten.** Standaard naast de bestaande database, als `<naam>_herstel_<datum>`, zodat Jonas eerst kan kijken. Over de bestaande database heen kan ook, met de app in onderhoud, en op prod met de slug en TOTP.

## Opkomen na een herstart

Jonas' huidige opstelling kwam na een reboot alleen goed op dankzij cronjobs, en PostgreSQL vroeg na een volledige uitval handwerk. Mijlpaal 5 maakt dat de taak van ClusterForge, vóór er een database verhuist.

- **Eén node herstart.** MariaDB staat aan bij het opstarten en sluit aan bij de nodes die nog draaien; wat hij miste, haalt hij bij een andere node (IST, of alles opnieuw met SST). Bij Patroni wordt de node weer replica. ClusterForge hoeft niets te doen, maar meldt het als een node niet binnen een paar minuten Synced is.
- **Alles tegelijk down**, door een stroomstoring of een herstart van de Proxmox-host. Galera start dan niet vanzelf: geen node weet of hij de nieuwste data heeft. ClusterForge ziet dat alle datanodes terug zijn maar geen enkele meedoet. Het vraagt elke node naar zijn positie (`grastate.dat` met `seqno` en `safe_to_bootstrap`, en zo nodig `mariadbd --wsrep-recover`), start het cluster op de node met de hoogste, en daarna de andere, met de gezondheidspoort na elke node. Dat gebeurt automatisch, met een regel in het logboek, en kan per cluster uit.
- **Een node blijft weg.** Dan start ClusterForge niets: net die node kan de nieuwste data hebben. Het cluster wordt plat gemeld, met de posities van de nodes die er wel zijn en een knop om toch te starten op de beste daarvan, na bevestiging.
- **PostgreSQL met Patroni** komt na een volledige uitval vanzelf terug: zodra de meerderheid van etcd er is, kiest Patroni de primary, en het VIP volgt. ClusterForge controleert dat alleen.

De tests: in CI herstarten drie echte MariaDB-containers één voor één, daarna allemaal tegelijk, en hard (kill) en netjes (stop); elke keer komt het cluster terug zonder verloren rij. Op Jonas' cluster komt een failovertest 'alles herstarten' in mijlpaal 13.

## Beheer

Mijlpaal 11 maakt de handelingen die bij een databasecluster horen tot geleide taken, met een plan, een bevestiging en een regel in het logboek.

- **Cluster opnieuw starten.** Met de hand, voor als het automatisch starten uit mijlpaal 5 niet mag of wacht op een node die wegblijft. Het plan toont de positie van elke node, de keuze en waarom.
- **Node opnieuw synchroniseren.** Een node die niet meer bij komt, gooit zijn data weg en haalt alles opnieuw bij een andere. Bij Patroni heet dat een replica opnieuw opbouwen.
- **Overschakelen (PostgreSQL).** Een andere node primary maken, bijvoorbeeld voor onderhoud. Het VIP volgt vanzelf.
- **Onderhoud en reboot.** Een node van een databasecluster in onderhoud zetten werkt zoals nu, met één aanvulling. Bij PostgreSQL schakelt ClusterForge eerst over als de node primary is. Na de reboot wacht ClusterForge tot de node weer Synced is of als replica bij is, voordat het 'klaar' meldt.
- **Updates.** Een kleinere versie, zoals 11.8.3 naar 11.8.4, node voor node: de schrijvende node als laatste, met de gezondheidspoort na elke node. Een grote versie wordt een nieuwe verhuizing, zie 'Buiten fase 5'.
- **Node toevoegen aan de database.** Een nieuwe node van het cluster, ook een CT die Jonas zelf maakte, krijgt het onderdeel en sluit aan.

## Status, incidenten, failovertests en score

- **Clusterstatus (mijlpaal 12).** Down als Galera geen Primary-component heeft of Patroni geen primary, of als het VIP geen database achter zich heeft. Verminderd als een node ontbreekt of niet Synced is, een replica meer dan 30 seconden achterloopt, of de scheidsrechter weg is. Een overgang wordt een incident, en What Broke krijgt de gebeurtenissen van de database erbij: een node die het cluster verlaat, een volledige synchronisatie, een failover of een herstart.
- **Grafieken (mijlpaal 12).** Verbindingen, query's per seconde, de buffer pool, achterstand en flow control, per node en per cluster.
- **Failovertests (mijlpaal 13).** Een cluster met een onderdeel Database krijgt wel een failovertest. Tijdens de test schrijft de agent op een andere node elke halve seconde een rij via het VIP, in een eigen kleine database `clusterforge_probe`. Het verslag zegt hoe lang schrijven niet lukte, en of er een bevestigde rij verloren ging. Bijvoorbeeld: 'Schrijven via .185 lukte weer na 9 s, er ging niets verloren.' De wat-als van fase 3 kent dan ook de meerderheid: 'Vallen nc2 en nc3 weg, dan stopt MariaDB.'
- **Scorekaart Database (mijlpaal 15).** Bevindingen zoals:
  - geen HA (één node, of twee zonder scheidsrechter);
  - een dump ouder dan een dag;
  - een node die niet Synced is of een replica die achterloopt;
  - twee databasenodes op dezelfde Proxmox-host;
  - minder dan 15% vrij geheugen op een node;
  - een database die door meer dan één cluster gebruikt wordt;
  - een verhuisde database die na dertig dagen nog niet afgebouwd is;
  - Galera-tabellen zonder primaire sleutel;
  - geen geslaagde failovertest in negentig dagen.
- **Documentatie (mijlpaal 15).** Een hoofdstuk Database per cluster: engine en versie, nodes en rollen, het VIP, de databases met grootte en gebruikers, de back-ups, en runbooks voor het cluster opnieuw starten, een node opnieuw synchroniseren, een dump terugzetten, overschakelen en verhuizen.
- **Assistent (mijlpaal 15).** Twee tools die alleen lezen: `database` met de toestand van een onderdeel, en `database_clients` met wie welke database gebruikt. Leg uit bij een incident van een database. Geen wachtwoorden, hashes of rijen.

## Firewall en netwerk

Na de verhuizing hoeft er geen regel meer van een app naar een database. Galera (3306, 4567, 4568 en 4444) en Patroni (5432, 8008, 2379 en 2380) praten alleen binnen het VLAN van de app. Draait er op de nodes zelf een firewall, zoals nftables, dan moeten die poorten tussen de nodes open. De gezondheidspoort ziet het meteen als een node de andere niet bereikt.

Wat blijft:

- de nodes naar ClusterForge op poort 4222 (NATS), zoals nu;
- tijdens de verhuizing de bestaande regel van de app naar het oude VIP .180, want daarlangs komt de dump;
- de regel voor een andere app die een verhuisde database deelt, als Jonas daarvoor kiest.

Na het afbouwen kan de regel van de app naar .180 weg. ClusterForge zegt welke regels niet meer gebruikt worden, uit de verbindingen die het ziet. De firewall zelf beheert het niet.

## Veiligheid

- **Geen vrije SQL.** De agent kent alleen de commando's hierboven, met gecontroleerde namen. Een fout in de server kan de agent dus geen willekeurige query laten uitvoeren.
- **Geen gegevens uit tabellen.** Wat de agent terugstuurt, zijn namen, groottes, aantallen en toestanden. Een dump gaat van node naar node en komt nooit bij ClusterForge.
- **Wachtwoorden.** Een nieuw wachtwoord staat één keer op het scherm en wordt niet bewaard. Een hash bij een verhuizing staat alleen versleuteld in de taak en verdwijnt als die klaar is. De wachtwoorden voor replicatie zijn `secret`-parameters, versleuteld bewaard, gemaskeerd in plannen en drift, en op de node alleen in bestanden met modus 0600. Een wachtwoord gaat via stdin naar de database, nooit als argument dat `ps` laat zien.
- **De tijdelijke gebruiker** mag alleen lezen, in één database, vanaf één adres. Een opruimtaak haalt een achtergebleven `cf_verhuizing_…` na een dag weg.
- **Weghalen** van een database vraagt altijd de naam, en een database op het oude cluster pas na een laatste dump.
- **Verkeer tussen de nodes** is niet versleuteld en blijft binnen het VLAN. TLS voor Galera en de replicatie staat onder 'Latere uitbreidingen'.
- **De dumps** staan in een map met modus 0700 van root.

## Testen zonder echt cluster

- **Echte databases in CI.** De workflow krijgt naast PostgreSQL ook MariaDB als service. De agentcommando's draaien daartegen: status, gebruikers, aanmaken, dump en terugzetten, en een verhuizing van de ene server naar de andere met de controle per tabel en het kopiëren van een hash.
- **Een echt Galera-cluster in CI.** Drie MariaDB-containers met de gerenderde configuratie: het cluster starten, aansluiten, een node stoppen en terug, en alles stoppen, netjes en hard, waarna ClusterForge het cluster zelf opnieuw start op de hoogste positie.
- **dbfake** voor Patroni en voor toestanden die in CI moeilijk na te bootsen zijn, zoals een node die als Donor blijft hangen of een replica die achterloopt. Net als `kubefake` in fase 4.
- **De onderdelen** worden getest op de gerenderde bestanden, en `hack/` krijgt nagespeelde databaseclusters voor de schermafbeeldingen.

De eerste echte uitrol en verhuizing gebeuren op Jonas' Proxmox. Daarom begint hij het best met de proefverhuizing.

## Bouwvolgorde

| # | Mijlpaal | Klaar als | Grootte |
| --- | --- | --- | --- |
| 1 | Databases lezen | Agentprotocol 9. Het menu Databases toont elke MariaDB en PostgreSQL met versie, rol, toestand van Galera of Patroni, en de databases met grootte. Het gedeelde cluster db1 tot db3 staat erin. Een test bevestigt dat geen wachtwoord of rij de agent verlaat. | M |
| 2 | Wie gebruikt welke database | Per database de clusters en nodes die haar gebruiken, ook cronjobs van de laatste dertig dagen. Een database van meer dan één cluster krijgt een waarschuwing. De graaf krijgt voorstellen. | M |
| 3 | Onderdeel MariaDB Galera | 'Database toevoegen' op een cluster met drie nodes, ook CT's, geeft drie nodes Synced. Het plan toont geheugen, schijf en hosts per node. Twee nodes kan alleen met een scheidsrechter. Een nieuw cluster kan de database meteen krijgen. Drift ziet een gewijzigde `99-clusterforge-galera.cnf`. | L |
| 4 | VIP voor de database | Een gedeeld VIP volgt de app en Galera: stopt MariaDB op de VIP-eigenaar, dan gaat het VIP binnen enkele seconden naar een andere node. Een eigen VIP werkt ook. Het plan toont de diff van een zelfgebouwde `keepalived.conf`. | M |
| 5 | Opkomen na een herstart | Een node die herstart, sluit vanzelf weer aan. Na een volledige uitval start ClusterForge het cluster zelf zodra alle datanodes terug zijn, op de node met de nieuwste data; ontbreekt er een, dan wacht het en vraagt het Jonas. In CI herstarten drie echte MariaDB-containers één voor één en allemaal tegelijk, en niets gaat verloren. | M |
| 6 | Databases en gebruikers | Een database met gebruiker aanmaken toont één keer het wachtwoord en de instellingen. Gebruikers en rechten staan in de lijst. Weghalen vraagt de naam. | M |
| 7 | Back-ups van databases | Elke nacht een dump per database, zeven dagen bewaard, met de leeftijd bij Back-ups. 'Dump nu' werkt, en terugzetten naast de bestaande database werkt. | M |
| 8 | Verhuizen: plan en proef | Het plan voor Nextcloud toont grootte, versies, clients en waarschuwingen. De proefverhuizing importeert een kopie terwijl de app doorwerkt, meet de duur en ruimt op. | M |
| 9 | Verhuizen: uitvoeren | Een database verhuist met onderhoud, dump en import, gelijke aantallen per tabel, dezelfde gebruiker en hetzelfde wachtwoord, en de oude database op slot. Een afwijking stopt de taak zonder verlies. | L |
| 10 | PostgreSQL met Patroni | Drie nodes met één primary en twee replica's, een eigen VIP dat de primary volgt, en verhuizen naar PostgreSQL werkt. Stopt de primary, dan staat het VIP binnen een halve minuut op de nieuwe. | L |
| 11 | Beheer | Het cluster met de hand opnieuw starten na een volledige uitval kiest de juiste node. Een node opnieuw synchroniseren, overschakelen, een kleinere update node voor node en onderhoud met wachten op Synced werken. | L |
| 12 | Status, incidenten en metrics | Een Galera-node die wegvalt, maakt het cluster verminderd en geeft een incident. Grafieken per node en cluster. | M |
| 13 | Failovertests met database | Een failovertest op het Nextcloud-cluster meet hoe lang schrijven via het VIP niet lukte, en dat er niets verloren ging. Een test 'alles herstarten' meet hoe lang het duurt tot alles weer Synced is. De wat-als kent de meerderheid. | M |
| 14 | Oude cluster afbouwen | Na zeven dagen zonder verbindingen een laatste dump en weghalen met de naam. ClusterForge meldt wanneer db1 tot db3 leeg zijn. | S |
| 15 | Score, documentatie en assistent | De scorekaart Database, het hoofdstuk Database met runbooks, en twee tools voor de assistent die alleen lezen. | M |

**Waarom deze volgorde.** Eerst zien, zodat Jonas weet welke app welke database gebruikt voordat er iets verandert. Dan MariaDB met het VIP, want Nextcloud is de eerste app, en meteen daarna het opkomen na een herstart, zodat een stroomstoring na de verhuizing geen handwerk vraagt. Back-ups komen vóór de verhuizing, zodat een verhuisde database vanaf de eerste nacht een dump heeft. Na mijlpaal 9 kan Nextcloud verhuizen. PostgreSQL volgt met hetzelfde verhuizen. Beheer, bewaken, testen en afbouwen volgen als de eerste apps verhuisd zijn. Valt het einde buiten de tijd, dan blijft alle eerdere waarde staan.

## Open keuzes

Elke keuze is ingevuld met een aanbeveling, en het ontwerp volgt overal de aanbevolen kolom.

| Keuze | Aanbevolen | Alternatief | Waarom |
| --- | --- | --- | --- |
| Een database per app of gedeeld? | Per app | Het gedeelde cluster houden | Zie 'Is een database per app een goed idee?'. |
| HA voor MariaDB? | Galera | Primary met replica, of een proxy ervoor | Geen overschakelen, geen dataverlies, een node haalt zelf bij. |
| HA voor PostgreSQL? | Patroni met etcd | repmgr, pg_auto_failover, alleen replicatie | Nooit twee primaries, en overschakelen is één commando. |
| Hoeveel nodes? | Drie | Twee met een scheidsrechter | Een meerderheid na één uitval zonder extra node. |
| Eén VIP voor app en database? | Ja bij Galera, nee bij PostgreSQL | Altijd een eigen VIP | Galera kan op elke node schrijven, PostgreSQL alleen op de primary. |
| Template of onderdeel? | Een onderdeel op bestaande nodes | Een template met nieuwe VM's | Jonas' apps draaien al op CT's, en de database hoort bij die nodes. |
| Welke versie? | Die van Debian op de nodes | De repositories van MariaDB of PostgreSQL | Beveiligingsupdates via Debian, niets extra te beheren. |
| Hoe verhuizen? | Dump en import met kort onderhoud | Replicatie zonder onderhoud | Eenvoudig, te controleren per tabel, en een paar minuten onderhoud per app. Replicatie kan later. |
| Hoe komt de dump bij de nieuwe node? | De nieuwe node haalt hem bij de oude database | Via ClusterForge, of van node naar node over een nieuwe poort | Geen nieuwe firewallregel, en de data komt niet bij ClusterForge. |
| Wachtwoord van de app? | Hetzelfde, met de hash van het oude cluster | Een nieuw wachtwoord | De app hoeft alleen het adres te veranderen. |
| Nieuwe wachtwoorden bewaren? | Nee, één keer tonen | Versleuteld bewaren | Wat niet bewaard wordt, kan niet uitlekken. |
| Waar staan de dumps? | Op de node, mee in de Proxmox-back-up | NFS of een andere plek | Werkt meteen. Een kopie naar NFS kan later. |
| Synchrone replicatie bij PostgreSQL? | Aan, niet strikt | Asynchroon | Geen verlies bij een gewone failover, en schrijven gaat door met één node. |

## Vragen aan Jonas

Zolang een antwoord ontbreekt, bouwen we met de standaard eronder.

- [ ] Drie nodes per app, of twee met een scheidsrechter? (mijlpaal 3)

  Standaard zolang je niet antwoordt: drie, zoals nc1 tot nc3. Twee kan met een kleine derde CT of VM als scheidsrechter.

- [ ] Eén VIP voor app en database? (mijlpaal 4)

  Standaard zolang je niet antwoordt: ja bij MariaDB, zoals je voorstelt (.185 voor Nextcloud). PostgreSQL krijgt een eigen vrij IP in hetzelfde VLAN, omdat de primary niet met de app meegaat.

- [ ] Draait cf-agent op db1 tot db3 en op nc1 tot nc3? (mijlpaal 1)

  Standaard zolang je niet antwoordt: ja, en je werkt ze bij naar protocol 9. Zonder agent op db1 tot db3 ziet ClusterForge niet wie welke database gebruikt, en vraagt een verhuizing het wachtwoord van de app.

- [ ] Hoeveel onderhoud mag een verhuizing kosten? (mijlpaal 9)

  Standaard zolang je niet antwoordt: een paar minuten per app, buiten de drukke uren. De proefverhuizing zegt vooraf hoe lang precies.

- [ ] Met welke app begin je? (mijlpaal 8)

  Standaard zolang je niet antwoordt: Nextcloud op MariaDB, eerst met een proefverhuizing.

- [ ] Zijn dumps op de node genoeg, of wil je een kopie elders? (mijlpaal 7)

  Standaard zolang je niet antwoordt: elke nacht een dump op de node, zeven dagen, en die gaat mee in de Proxmox-back-up van de CT.

## Buiten fase 5

**Bewust niet.**

- Verhuizen zonder onderhoud, met replicatie van het oude naar het nieuwe cluster. Dump en import zijn eenvoudiger en goed te controleren. Voor grote databases kan het later.
- Een upgrade naar een nieuwe grote versie op dezelfde nodes. Dat wordt een verhuizing naar een nieuw onderdeel.
- De firewall beheren. ClusterForge zegt welke regels nodig zijn en welke niet meer gebruikt worden.
- De instellingen van een app aanpassen of een app in onderhoud zetten. ClusterForge toont het commando.
- CT's maken of weghalen. Nieuwe nodes van ClusterForge blijven VM's.

**Latere uitbreidingen.** Deze punten passen in het ontwerp, maar hebben nog geen mijlpaal:

- TLS voor Galera, de replicatie en de verbinding van de app;
- een kopie van de dumps naar NFS of een andere host;
- het onderdeel in `cluster.yaml` voor clusters die Git volgen;
- het geheugen van een CT of VM verhogen met een knop, met het recht `VM.Config.Memory`;
- Redis en andere diensten van een app als onderdeel.
