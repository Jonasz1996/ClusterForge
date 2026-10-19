# ClusterForge – Technisch ontwerp fase 8: versiebeheer van MariaDB

Stand: 2026-10-19

## Samenvatting

Op 19 oktober draaiden Jonas' databases vier verschillende MariaDB-versies naast elkaar: Node1 van het "database cluster" 12.3.3 uit de repo van MariaDB.org, Node2 en Node3 12.2.2, en nc1-3 11.8 uit Debian 13. Dat kwam pas aan het licht toen er iets misging: Zelfherstel faalde op het ontbrekende pakket `mariadb-server-galera` (sinds 12.3.2 apart), Node1 gaf fout 1558 omdat zijn systeemtabellen nog van 12.2 waren, en Verhuizen weigerde nextcloud naar nc1-3 omdat het doel ouder is dan de bron. Elke stap was handwerk per node: een repo toevoegen, de juiste pakketten installeren, `mariadb-upgrade` draaien, wachten tot de node Synced is. Voor tien clusters zijn dat dertig repo's en installs.

Jonas vroeg of de agents dat kunnen beheren: versies controleren, bijwerken, repo's vergelijken. Fase 8 doet dat als **Versies**: per cluster een gekozen bron (Debian, of MariaDB.org met een vaste reeks zoals 12.3), een overzicht dat alle nodes en clusters naast elkaar zet en afwijkingen noemt, en een rollende upgrade die één node tegelijk bijwerkt en pas verder gaat als die node weer Synced is.

| Module | Wat Jonas krijgt | Mijlpalen |
| --- | --- | --- |
| Zien | Een pagina Versies: per cluster en per node de draaiende versie, de geïnstalleerde pakketten, de bron (Debian of MariaDB.org, met het bestand), wat apt zou installeren, en de versie van de systeemtabellen. Afwijkingen staan bovenaan, met waarom ze ertoe doen. | 1 |
| Bron | Per cluster het versiebeleid kiezen: Debian, of MariaDB.org met een reeks. ClusterForge zet de repo op elke node gelijk en meldt een node die afwijkt. | 2 |
| Bijwerken | "Bijwerken naar 12.3.3": één node tegelijk, de node met het VIP als laatste, `mariadb-upgrade` op elke node, wachten op Synced, stoppen bij de eerste fout. Ook voor clusters die Jonas zelf bouwde. | 3 |
| Vanzelf en templates | Per cluster aan te zetten: kleine versies (12.3.3 naar 12.3.4) vanzelf in het testvenster, nooit op prod, nooit een nieuwe reeks. Het template MariaDB Galera kan MariaDB.org gebruiken, en Verhuizen biedt aan het doel eerst bij te werken. | 4 |
| Bewaken | Documentatie, What Broke en de assistent kennen de versies en de upgrades. | 5 |

**Uitgangspunten**

- **Eerst zien, dan kiezen, dan pas doen.** Mijlpaal 1 leest alleen. Elke upgrade is een plan met per node wat er verandert, met een hash, zoals bij Zelfherstel.
- **Nooit iets dat data kan kosten zonder bevestiging.** Een upgrade start alleen als alle nodes Synced zijn en het cluster compleet is; ClusterForge legt nooit een tweede node stil terwijl er al een weg is. Teruggaan naar een oudere versie op dezelfde node doet ClusterForge nooit; dat gaat via Verhuizen.
- **Alle nodes van een cluster krijgen exact dezelfde versie.** Het plan legt de versie vast (bijvoorbeeld `1:12.3.3+maria~deb13`) en installeert die met `=`, zodat een node die later aan de beurt komt geen nieuwere patch krijgt die intussen verscheen.
- **Werkt op elke MariaDB die de agent ziet,** ook als ClusterForge hem niet installeerde, zoals het "database cluster". Is de database een onderdeel van ClusterForge, dan wordt het bestaande "Bijwerken…" van fase 5 deze taak, zodat er nooit twee systemen hetzelfde doen.
- **Dezelfde regels als de rest.** Alleen een beheerder wijzigt; op prod vraagt het de slug en TOTP. Vanzelf staat standaard uit en gebeurt nooit op prod.

## Wat ClusterForge al heeft en wat ontbreekt

Bruikbaar zoals het is:

- **De pagina Databases** (fase 5, `internal/databases`): per node de draaiende versie (`DBServer.Version`), de Galera-toestand en wie het VIP heeft.
- **Bijwerken binnen Debian** (`component.update`, `internal/components/update.go`): node voor node met de schrijver als laatste, alleen de pakketten van de database, wachten tot de node meedoet. Alleen voor toegepaste onderdelen en alleen binnen de versie van Debian.
- **De volgorde van een rollende wijziging** (`internal/components/rolling.go`): datanodes op volgnummer, schrijver laatst, en vooraf de controle dat elke datanode meedoet.
- **Beschikbare upgrades** in de facts (`apt-get -s dist-upgrade`, `internal/agent/collect.go`), zonder versies of herkomst.
- **Pakkettoestand** via `dpkg-query` (`internal/agent/read.go`), en het logboek van apt in de journal (`journal_sources.go`).
- **Zelfherstel** (fase 7) met plannen, hashes, de prod-bevestiging en de rem op vanzelf; het **testvenster** `CF_TEST_WINDOW` (standaard zo 03:00-05:00).

Wat ontbreekt:

| Gat | Gevolg | Opgelost in |
| --- | --- | --- |
| De agent meldt geen bron en geen kandidaatversie. | Niemand ziet dat Node1 uit de 12.3-repo komt en Node2 uit 12.2, of dat apt bij de volgende update iets anders zou kiezen. | Mijlpaal 1: verzoek `db versions` |
| De versie van de systeemtabellen is onzichtbaar. | Fout 1558 komt pas als een app of Verhuizen erop stuit. | Mijlpaal 1 |
| Repo's zet Jonas met de hand. | Tien clusters, dertig keer hetzelfde, en geen garantie dat ze gelijk blijven. | Mijlpaal 2: versiebeleid en `set_source` |
| Bijwerken kent geen nieuwe reeks en geen zelfgebouwd cluster. | 12.2 naar 12.3 of 11.8 naar 12.3 blijft handwerk, inclusief `mariadb-upgrade` en het vinkje "Vanzelf opnieuw starten". | Mijlpaal 3: taak `db.upgrade` |
| Het template zit vast op Debian's MariaDB. | Een nieuw cluster als nc1-3 krijgt altijd 11.8, ook als de rest 12.3 draait. | Mijlpaal 4 |

## Gedeelde bouwstenen

### Agentprotocol 24

Het subject `cf.node.<id>.db` krijgt het verzoek `versions` met drie acties:

| Actie | Wat het doet |
| --- | --- |
| `inspect` | Leest alleen. De geïnstalleerde pakketten van MariaDB en Galera met versie (`dpkg-query` op `mariadb-*`, `libmariadb*`, `galera-*`, `mysql-common`), per pakket wat `apt-cache policy` zegt (geïnstalleerd, kandidaat, en per versie de bron met URI, suite en prioriteit), de apt-bronnen die MariaDB leveren (bestand, regel, URI, suite, aan of uit, `.list` en `.sources`), de pin-bestanden in `/etc/apt/preferences.d` die MariaDB raken, de inhoud van `/var/lib/mysql/mysql_upgrade_info`, en of `apt` of `dpkg` nog bezig is of half klaar staat (`dpkg --audit`). De agent draait geen `apt-get update`: wat apt zegt, is van de laatste keer dat iemand dat deed, en het antwoord zegt wanneer dat was. |
| `set_source` | Zet de bron van MariaDB op de node: schrijft `/etc/apt/keyrings/cf-mariadb.pgp`, `/etc/apt/sources.list.d/cf-mariadb.sources` en `/etc/apt/preferences.d/cf-mariadb` (pin op de bron van het beleid, zodat Debian's 11.8 niet wint), en zet andere bronnen die MariaDB leveren uit door ze te hernoemen naar `<bestand>.cf-uit-<tijd>`. Daarna `apt-get update` en de controle dat de kandidaat uit de nieuwe bron komt; anders zet de agent alles terug. Bij "Debian" verdwijnen de eigen bestanden weer. |
| `install` | Installeert een vaste versie van de pakketten uit het plan met `apt-get install -y --no-install-recommends pakket=versie …`, zonder `--allow-downgrades`. Weigert als `dpkg --audit` iets meldt of apt bezig is. De uitvoer gaat naar de taak. |

De sleutel van MariaDB.org zit in de server, met zijn vingerafdruk; de agent controleert de vingerafdruk met `gpg --show-keys` voordat hij het bestand schrijft. Zo hangt een node niet af van een download van de sleutel. Starten, stoppen, `mariadb-upgrade` en wachten op Synced gaan met de bestaande deploystappen en databaseverzoeken, zodat hun uitvoer in de taak staat.

### Het versiebeleid van een cluster

Per cluster één beleid (tabel `db_version_policy`, in de eerstvolgende vrije migratie):

```
cluster:   database cluster
engine:    mariadb
source:    debian | mariadb.org
series:    "12.3"          (alleen bij mariadb.org)
mirror:    https://dlm.mariadb.com/repo/mariadb-server   (aan te passen, voor een eigen mirror)
auto:      false           (mijlpaal 4)
```

Zonder beleid kijkt ClusterForge alleen en meldt het afwijkingen tussen de nodes, maar wijzigt het niets. Bij een beleid meldt het ook elke node die er niet aan voldoet.

### Afwijkingen

`internal/versions` bekijkt elke databasegroep van de pagina Databases met de antwoorden van `inspect`, als zuivere functie, zoals `internal/heal`:

| Code | Wanneer | Niveau |
| --- | --- | --- |
| `mixed_versions` | De nodes van één cluster draaien verschillende versies (Node1 12.3.3, Node2 en Node3 12.2.2). | Waarschuwing |
| `system_tables_old` | `mysql_upgrade_info` is ouder dan de draaiende versie, of ontbreekt. Dat geeft fout 1558. | Waarschuwing, met het commando |
| `galera_package_missing` | MariaDB 12.3.2 of nieuwer zonder `mariadb-server-galera`: geen `galera_new_cluster`, geen SST. | Down |
| `source_mixed` | De nodes halen MariaDB uit verschillende bronnen of reeksen, of één node heeft twee bronnen die MariaDB leveren. | Waarschuwing |
| `policy_drift` | Een node voldoet niet aan het beleid van zijn cluster (andere bron, andere reeks, of een kandidaat die buiten de reeks valt). | Waarschuwing |
| `update_available` | De bron heeft een nieuwere versie in dezelfde reeks. | Info |
| `dpkg_broken` | `dpkg --audit` meldt half geïnstalleerde pakketten (zoals op nc2 na de upgrade van 19 oktober). | Waarschuwing; blokkeert elke upgrade op die node |
| `apt_stale` | De laatste `apt-get update` is ouder dan 7 dagen. | Info |

Over clusters heen zet de pagina de versies naast elkaar, zodat ook zichtbaar is dat een verhuizing van "database cluster" (12.3) naar "nextcloud" (11.8) een downgrade zou zijn.

### Een upgrade

Elke upgrade is een taak `db.upgrade` met een plan:

```
cluster:   database cluster
van:       Node1 12.3.3, Node2 12.2.2, Node3 12.2.2
naar:      12.3.3 (1:12.3.3+maria~deb13), bron MariaDB.org 12.3
volgorde:  Node2, Node3, Node1 (VIP-houder laatst; Node1 heeft al 12.3.3, alleen controleren)
pakketten: mariadb-server, mariadb-server-galera, mariadb-client, mariadb-backup, galera-4 …
hash:      over plan en toestand
```

Per node: bron zetten (`set_source`) → `install` met de vaste versie → MariaDB starten als dpkg dat niet deed → wachten tot de node Synced is en `wsrep_cluster_size` weer compleet → `mariadb-upgrade --skip-write-binlog` (zodat de wijziging van de systeemtabellen niet via Galera naar nodes met de oude versie gaat; elke node doet zijn eigen tabellen) → controleren dat versie en `mysql_upgrade_info` kloppen → de volgende node. Tijdens de taak pauzeert ClusterForge zijn eigen herstel voor dat cluster (het automatisch opnieuw starten van fase 5 en Zelfherstel van fase 7), zodat Jonas het vinkje "Vanzelf opnieuw starten na een volledige uitval" niet meer met de hand uit en aan hoeft te zetten. Het eind van de taak, ook een mislukt eind, zet het terug.

Events `cluster.version_upgrade_started` / `_done` / `_failed` en per node `node.version_upgraded`, met job_id; een open incident van het cluster krijgt de taak in zijn tijdlijn.

## Mijlpalen in detail

**Mijlpaal 1: zien.** Agentprotocol 24 met `versions inspect`. `internal/versions` met de regels uit de tabel, getest op vaste voorbeelden, waaronder de stand van 19 oktober (Node1 12.3.3 met tabellen van 12.2 en zonder `mariadb-server-galera`, Node2 en Node3 12.2.2, nc1-3 11.8 uit Debian, nc2 met half geïnstalleerde pakketten). API `GET /api/v1/versions` en een pagina Versies (in het menu bij Databases) met de afwijkingen bovenaan en per cluster een tabel per node. De kaart van een databasecluster krijgt een regel "MariaDB 12.3.3 op 1 van 3 nodes". Ook PostgreSQL komt in de tabel (versie per node, PGDG of Debian), alleen om te lezen. Agents ouder dan 24 tonen "agent te oud".

**Mijlpaal 2: bron.** Het versiebeleid per cluster met `PUT /clusters/{id}/version-policy` (beheerder; op prod slug en TOTP), en het formulier op de pagina Versies. Bij het opslaan een plan per node: welke bestanden er komen, welke bronnen uit gaan, welke kandidaat apt daarna kiest. "Bron toepassen" voert `set_source` node voor node uit; er verandert nog geen MariaDB. Afwijkingen `policy_drift` komen erbij.

**Mijlpaal 3: bijwerken.** De taak `db.upgrade` voor MariaDB en Galera, voor gedetecteerde groepen en voor onderdelen van ClusterForge (die krijgen de nieuwe versie en bron in hun spec, en "Bijwerken…" van fase 5 opent dit plan). Voorwaarden, elk met een eigen blokkade in het plan:

- Alle datanodes Synced, het cluster compleet, geen andere taak op het cluster.
- Geen `dpkg_broken` op een node, genoeg vrije ruimte in de datamap en op `/var/cache/apt`.
- Hoogstens één stap in het hoofdnummer (11.x naar 12.x, 12.2 naar 12.3); een grotere sprong, of naar een oudere versie, gaat via Verhuizen.
- Bij een nieuwe reeks een geslaagde back-up van het cluster van de laatste 24 uur: op prod verplicht, elders een waarschuwing.

"Nu bijwerken" met het plan, de hash en op prod slug en TOTP. Faalt een node (installeren mislukt, niet Synced binnen 30 minuten, `mariadb-upgrade` faalt), dan stopt de taak meteen. De andere nodes draaien dan nog en het cluster blijft werken; de taak zegt welke node, wat de uitvoer was en wat Jonas kan doen. ClusterForge zet een bijgewerkte node nooit vanzelf terug naar de oude versie: een downgrade op dezelfde datamap is niet veilig.

**Mijlpaal 4: vanzelf, template en Verhuizen.** Een instelling per cluster "Kleine versies vanzelf bijwerken", standaard uit, niet aan te zetten op prod. Vanzelf gaat alleen binnen de reeks van het beleid (12.3.3 naar 12.3.4), alleen in het testvenster, alleen als het plan geen blokkade en geen waarschuwing heeft, hoogstens één keer per week per cluster, en na een mislukte poging niet meer tot Jonas het opnieuw aanzet. Het template MariaDB Galera krijgt de parameter "Bron" (Debian of MariaDB.org met een reeks), installeert bij 12.3.2 en nieuwer ook `mariadb-server-galera`, en zet het beleid van het cluster bij het uitrollen. Verhuizen noemt bij een doel dat ouder is dan de bron de knop "Doel eerst bijwerken", die het plan van mijlpaal 3 opent voor het doelcluster.

**Mijlpaal 5: documentatie, What Broke en de assistent.** Het hoofdstuk Versies in de documentatie per cluster (beleid, versies per node, de upgrades van de laatste dertig dagen); What Broke toont een upgrade als wijziging en noemt hem als waarschijnlijke oorzaak als er kort daarna fouten komen; de assistent krijgt de tool `versions` die alleen leest; README en docs krijgen het hoofdstuk Versies.

## Veiligheid

- **Een upgrade legt een node stil.** Daarom alleen met alle nodes Synced, één tegelijk, de VIP-houder laatst, en wachten op Synced voor de volgende. Een cluster met twee nodes en een scheidsrechter mag; een cluster dat zonder de node geen meerderheid heeft, niet.
- **Een nieuwe reeks kan niet terug.** Daarom een back-up vooraf (op prod verplicht), nooit vanzelf, en het plan zegt dat terugdraaien alleen kan via een herstel of Verhuizen.
- **Gemengde versies tijdens de upgrade.** `mariadb-upgrade` draait met `--skip-write-binlog`, zodat de nieuwe systeemtabellen niet naar nodes met de oude versie repliceren. De taak blijft zo kort mogelijk in de gemengde toestand en de pagina waarschuwt als een cluster langer dan 7 dagen gemengd blijft.
- **Een bron wijzigen raakt alles wat apt doet.** De agent maakt alleen eigen bestanden met een `cf-`-naam, zet andere MariaDB-bronnen uit door ze te hernoemen (nooit weg te gooien), en zet alles terug als de controle na `apt-get update` faalt. Bronnen die geen MariaDB leveren, raakt hij niet.
- **De sleutel van de bron** komt uit de server met een vaste vingerafdruk, niet van internet.
- **Vanzelf** staat standaard uit, nooit op prod, nooit een nieuwe reeks, en heeft een rem.

## Testen

- De regels van `internal/versions` op vaste groepen: de stand van 19 oktober, een cluster dat helemaal gelijk is, twee bronnen op één node, een kandidaat buiten de reeks, `dpkg --audit` met uitvoer.
- `versions inspect` en `set_source` op voorbeeldmappen in `/etc/apt` met `apt-cache policy` en `dpkg-query` als nep in `agenttest`, ook het terugzetten als de kandidaat niet uit de nieuwe bron komt.
- Het plan van `db.upgrade`: de volgorde met de VIP-houder laatst, de blokkades (node niet Synced, sprong van twee hoofdnummers, downgrade, geen back-up op prod), en dat de hash verandert als een node van toestand verandert.
- Een echte Galera van drie nodes in Docker (zoals `internal/galera/docker_test.go`) met 12.2 naar 12.3 uit de repo van MariaDB.org: na de taak draaien alle nodes 12.3, Synced, met `mysql_upgrade_info` van 12.3.

## Bouwvolgorde

| # | Mijlpaal | Klaar als | Grootte |
| --- | --- | --- | --- |
| 1 | Zien | De pagina Versies toont bij Jonas "12.3.3 op Node1, 12.2.2 op Node2 en Node3", "systeemtabellen van Node1 zijn van 12.2" en "nc1-3 draaien 11.8 uit Debian", elk met waarom. | M |
| 2 | Bron | Het beleid MariaDB.org 12.3 op "database cluster" zet op alle drie de nodes dezelfde bron, en apt kiest daarna 12.3. | M |
| 3 | Bijwerken | "Nu bijwerken" brengt Node2 en Node3 naar 12.3.3, één tegelijk, met het cluster de hele tijd Primary. | L |
| 4 | Vanzelf, template en Verhuizen | Een patch komt vanzelf in het testvenster op een lab-cluster; een nieuw Galera-cluster kan 12.3 krijgen; Verhuizen biedt "Doel eerst bijwerken". | M |
| 5 | Documentatie en assistent | Hoofdstuk, What Broke, tool en README. | S |

## Keuzes in dit ontwerp

| Keuze | Gekozen | Waarom |
| --- | --- | --- |
| Wat het beleid vastlegt | Een bron en een reeks (12.3), geen exacte patch | Een reeks is wat Jonas kiest; de patch legt elk plan apart vast, zodat binnen één upgrade alle nodes gelijk zijn. |
| Bron van MariaDB.org | `dlm.mariadb.com` met een eigen `.sources`-bestand, een pin en de sleutel uit de server | Dezelfde bron als het script van MariaDB zelf, maar zonder het script en zonder download van de sleutel; de pin voorkomt dat Debian's 11.8 terugkomt. Een eigen mirror kan. |
| Andere MariaDB-bronnen op een node | Uitzetten door te hernoemen, in het plan te zien | Twee bronnen voor dezelfde pakketten geven precies de gemengde versies van 19 oktober. Hernoemen is terug te draaien. |
| Volgorde | Node voor node, VIP-houder laatst, wachten op Synced | Hetzelfde als de rollende wijzigingen van fase 5; de apps merken pas iets bij de laatste node. |
| Sprong in versie | Hoogstens één hoofdnummer per upgrade, nooit terug | Een rollende Galera-upgrade is alleen tussen opeenvolgende hoofdversies te vertrouwen; terug op dezelfde datamap is niet veilig. Verhuizen (met #92) blijft de weg naar een oudere versie. |
| Eigen herstel tijdens de upgrade | Gepauzeerd voor dat cluster, tot de taak eindigt | Anders start ClusterForge een node die de taak juist stillegde, en moet Jonas weer het vinkje uit en aan zetten. |
| Vanzelf | Per cluster, standaard uit, alleen patches, alleen in het testvenster, nooit op prod | Een patch herstart MariaDB op elke node; Jonas kiest per cluster of dat 's nachts mag. Een nieuwe reeks is nooit vanzelf. |
| Onderdelen van ClusterForge | "Bijwerken…" van fase 5 wordt deze taak | Nooit twee systemen voor hetzelfde. |
| PostgreSQL | Alleen lezen in deze fase | Een nieuwe hoofdversie van PostgreSQL vraagt `pg_upgrade` of een dump, een ander soort werk; kleine versies doet fase 5 al. |

## Buiten fase 8

- Hoofdversies van PostgreSQL en Patroni bijwerken.
- MySQL van Oracle (alleen lezen; bijwerken kan later met dezelfde bouwstenen).
- Andere pakketten dan de database (nginx, keepalived, het OS); de facts tonen beschikbare upgrades al.
- De upgrade van nc1-3: die wacht op Jonas' keuze in de projectchat en gebeurt tot dan met de hand of niet.
