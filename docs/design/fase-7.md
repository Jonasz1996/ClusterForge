# ClusterForge – Technisch ontwerp fase 7: zelfherstel

Stand: 2026-10-18

## Samenvatting

Op 18 oktober lag Jonas' databasecluster plat. Na zijn VLAN-opdeling hadden de nodes nieuwe adressen, maar de Galera-config wees nog naar 192.168.0.191 en .192. Na een herstart bleef MariaDB op Node1 eindeloos aansluiten op die oude adressen; systemd zette de unit op failed terwijl het proces bleef draaien, en Node3 bleef alleen over zonder meerderheid (non-Primary). Nextcloud, Zabbix, Gitea en de rest zaten zonder database. ClusterForge zag het meteen ("Geen node zit in een Primary-component"), maar kon niets doen: het herstel uit fase 5 werkt alleen op databases die ClusterForge zelf heeft toegepast.

Jonas vroeg of de agents zulke problemen zelf kunnen oplossen, voor MariaDB en MySQL (en PostgreSQL op dezelfde nodes). Fase 7 doet dat als **Zelfherstel**: geen losse shell-scripts, maar herstelacties in agent en server die elk probleem eerst vaststellen, dan een plan tonen met waarom, en het plan als taak uitvoeren, met uitvoer, in het logboek en bij het incident.

| Module | Wat Jonas krijgt | Mijlpalen |
| --- | --- | --- |
| Vaststellen | Bij elke database op de pagina Databases een kaart Zelfherstel: wat er mis is, op welke node, waarom, en wat ClusterForge zou doen. Ook voor databases die hij zelf installeerde. | 1 |
| Herstellen | Per probleem de knop "Nu herstellen": de unit starten, een hangend proces opruimen, de adressen in de Galera-config bijwerken, of het cluster veilig opnieuw beginnen vanaf de node met de nieuwste data. | 2 |
| Vanzelf | Per cluster aan te zetten (standaard uit): ClusterForge voert veilige herstelacties zelf uit, met een rem na mislukte pogingen. Op prod nooit zonder bevestiging. | 3 |
| Breder | PostgreSQL en Patroni, MySQL zonder Galera, en de controle dat "Database toevoegen" geen stilstaande database overschrijft. | 4 |
| Bewaken | Het hoofdstuk Herstel in de documentatie, What Broke en de assistent kennen de herstelacties. | 5 |

**Uitgangspunten**

- **Eerst vaststellen, dan voorstellen, dan pas doen.** Mijlpaal 1 leest alleen. Elk probleem heeft een plan in gewone taal, met de reden waarom het wel of niet vanzelf mag.
- **Nooit iets dat data kan kosten zonder bevestiging.** Een Galera-cluster begint alleen op de node met de hoogste seqno, nooit twee keer, en nooit als een node niet antwoordt die nieuwere data kan hebben. Een node die een volledige kopie (SST) nodig heeft, krijgt die alleen van een donor die Synced is.
- **Werkt op elke database die de agent ziet,** ook als ClusterForge hem niet installeerde. Is de database een onderdeel van ClusterForge, dan gebruikt Zelfherstel de bestaande actie (Opnieuw starten, Toepassen), zodat er nooit twee systemen hetzelfde doen.
- **Dezelfde regels als de rest.** Alleen een beheerder herstelt; op prod vraagt het de slug en TOTP. Vanzelf herstellen staat standaard uit, en op prod gebeurt niets zonder bevestiging.
- **Klein en terug te draaien.** Een gewijzigd configbestand krijgt eerst een kopie `<bestand>.cf-zelfherstel-<tijd>`, en het plan noemt het bestand en de regel.

## Wat ClusterForge al heeft en wat ontbreekt

Bruikbaar zoals het is:

- **De pagina Databases** (fase 5, `internal/databases`): per cluster de MariaDB-, MySQL- en PostgreSQL-servers, als Galera, Patroni, replicatie of losse server, met de toestand per node. Ook voor databases die ClusterForge niet toepaste.
- **Positie lezen** (`db position`, `internal/agent/dbposition.go`): seqno uit Galera, `grastate.dat` of `galera_recovery`, en de keuze van de beste node (`galera.Choose`: hoogste seqno, zelfde cluster-UUID).
- **Opnieuw starten** (`component.recover`): stoppen, posities lezen, beginnen op de beste node, de rest een voor een laten aansluiten en wachten tot ze Synced zijn. Alleen voor toegepaste onderdelen.
- **Deploystappen** (service, command, file) via `deploy.ApplySteps`, met de uitvoer in de taak.
- **Incidenten, events, What Broke, het logboek en de prod-bevestiging.**

Wat ontbreekt:

| Gat | Gevolg | Opgelost in |
| --- | --- | --- |
| De agent leest de Galera-config niet. | Verkeerde adressen in `wsrep_cluster_address` zijn onzichtbaar. | Mijlpaal 1: verzoek `heal inspect` |
| De agent ziet niet dat een proces blijft draaien terwijl systemd de unit op failed zette. | ClusterForge denkt dat MariaDB stilstaat; `galera_recovery` zou naast het oude proces starten. | Mijlpaal 1 |
| Herstel kent alleen toegepaste onderdelen. | Een zelf geïnstalleerd cluster blijft plat. | Mijlpaal 2: taak `heal.run` op basis van de databasegroep |
| Geen automatisch herstel buiten onderdelen. | Een herstart van alle CT's 's nachts wacht tot Jonas wakker is. | Mijlpaal 3 |

## Gedeelde bouwstenen

### Agentprotocol 22

Het bestaande subject `cf.node.<id>.db` krijgt het verzoek `heal` met twee acties:

| Actie | Wat het doet |
| --- | --- |
| `inspect` | Per engine: de unit (active, sub, result, MainPID, enabled), processen van de engine buiten een draaiende unit (`mariadbd`, `mysqld`, `postgres`, `patroni`), de vrije ruimte in de datamap, en voor Galera de geldende waarden van `wsrep_cluster_address`, `wsrep_node_address`, `wsrep_sst_receive_address` en `wsrep_cluster_name` (via `my_print_defaults`, met het bestand en de regel waar ze staan), en de adressen waarop de laatste journalregels "connection to peer … timed out" melden. De eigen IPv4-adressen van de node. Leest alleen. |
| `set_galera_address` | Zet `wsrep_cluster_address` en `wsrep_node_address` in het bestand waar ze nu staan, na een kopie, en controleert met `my_print_defaults` dat de nieuwe waarde geldt; anders zet de agent de kopie terug. Een MariaDB die draait, leest het bestand pas bij zijn volgende start, dus een node die de apps bedient merkt er nu niets van. |

Starten, stoppen, een hangend proces beëindigen en `galera_new_cluster` gaan met de bestaande deploystappen (service en command), zodat hun uitvoer in de taak staat.

### Een probleem

De server (`internal/heal`) bekijkt elke databasegroep van de pagina Databases met de antwoorden van `inspect` en geeft een lijst problemen:

```
code:      galera_wrong_address
engine:    mariadb
cluster:   database cluster
nodes:     [DatabaseClusterNode1, DatabaseClusterNode2]
level:     down | warning
title:     "Galera zoekt de andere nodes op oude adressen"
why:       "wsrep_cluster_address noemt 192.168.0.191 en 192.168.0.192; die adressen heeft geen node van het cluster. De nodes hebben nu 192.168.10.181, .182 en .183."
plan:      [stappen in gewone taal]
auto:      true | false, met reden
hash:      over het probleem en de toestand, zodat een plan dat intussen veranderde niet meer uitgevoerd wordt
```

### De eerste herstelacties

| Code | Wanneer | Plan | Vanzelf? |
| --- | --- | --- | --- |
| `unit_stopped` | De unit van MariaDB, MySQL of Patroni staat uit of op failed, de node draait, er hangt geen proces, en (bij Galera) een andere node zit in een Primary component of het is een losse server. | Starten en opvolgen tot hij Synced is of antwoordt. | Ja |
| `process_hung` | systemd zegt failed of inactive, maar het proces draait nog; bij Galera zonder Primary component. | Het proces stoppen (TERM, na 60 s KILL, alleen de cgroup van de unit), dan verder als `unit_stopped` of `galera_no_primary`. | Ja, tenzij het de laatste node met de nieuwste data is en dat niet vast te stellen is |
| `galera_wrong_address` | `wsrep_cluster_address` noemt adressen die geen node van het cluster heeft, of mist adressen die het cluster wel heeft, of `wsrep_node_address` is geen adres van de node. | Op elke node de adressen bijwerken naar de huidige adressen van de nodes, dan herstarten in de goede volgorde (zie `galera_no_primary` als niemand Primary is). | Nee: een adres raadt ClusterForge uit de agent, en een fout adres splitst het cluster. Altijd bevestigen. |
| `galera_no_primary` | Geen enkele node zit in een Primary component, en alle nodes antwoorden. | Elke node stoppen, de posities lezen, beginnen op de node met de hoogste seqno, de andere een voor een laten aansluiten. | Alleen als alle nodes een bekende positie hebben, met één duidelijke winnaar, en er geen ander probleem op het cluster is |
| `disk_full` | De datamap heeft minder dan 5 % of 1 GiB vrij. | Alleen melden: welke map, hoe groot, de grootste databases (bij Jonas Zabbix), en wat te doen. | Nee |
| `no_quorum` | Een node antwoordt niet, of een positie is onbekend, of twee nodes hebben dezelfde seqno met een andere cluster-UUID. | Alleen melden: welke node eerst terug moet komen, en waarom beginnen nu data kan kosten. | Nee |

Elke uitvoering is een taak `heal.run` met stappen, de uitvoer van elke stap, en een event `cluster.heal_started` / `cluster.heal_done` / `cluster.heal_failed`. Een open incident van het cluster krijgt de taak in zijn tijdlijn.

## Mijlpalen in detail

**Mijlpaal 1: vaststellen.** Agentprotocol 22 met `heal inspect`. `internal/heal` met de regels uit de tabel, als zuivere functie over de groep en de antwoorden, getest op vaste voorbeelden, waaronder Jonas' storing van 18 oktober. API `GET /api/v1/heal` en een kaart Zelfherstel per database op de pagina Databases met het probleem, waarom, het plan, en of het vanzelf mag. Agents ouder dan 22 tonen "agent te oud".

**Mijlpaal 2: herstellen.** De taak `heal.run` voor `unit_stopped`, `process_hung`, `galera_wrong_address` en `galera_no_primary`, met `set_galera_address` op de agent. "Nu herstellen" met het plan, de hash en op prod slug en TOTP. Is de groep een onderdeel van ClusterForge, dan wijst de kaart naar Opnieuw starten of Toepassen.

**Mijlpaal 3: vanzelf.** Een instelling per cluster "Zelfherstel: vanzelf uitvoeren", standaard uit, niet aan te zetten op prod. Een lus om de minuut voert een probleem met `auto: true` uit als het minstens twee minuten bestaat (een herstart is dan al klaar), hoogstens drie keer per uur per cluster, en nooit als er al een taak op het cluster loopt. Na een mislukte poging: de volgende pas na 15 minuten, en na drie mislukte pogingen niet meer tot Jonas het opnieuw aanzet. Een `galera_no_primary` begint vanzelf hoogstens één keer per uur.

**Mijlpaal 4: breder.** PostgreSQL en Patroni: unit gestopt of failed, een hangend proces, en een Patroni die etcd niet bereikt of naar adressen wijst die geen node heeft (melden, met het bestand en de regel). MySQL (`mysql.service`, `mysqld`) zonder Galera: starten en opvolgen. "Database toevoegen" weigert een node waarop MariaDB of MySQL bestaat maar stilstaat, met de verwijzing naar Zelfherstel.

**Mijlpaal 5: documentatie, What Broke en de assistent.** Het hoofdstuk Herstel noemt per cluster de problemen van de laatste dertig dagen en wat Zelfherstel deed; What Broke toont een herstelactie als gebeurtenis; de assistent krijgt de tool `heal` die alleen leest; README en docs krijgen het hoofdstuk Zelfherstel.

## Veiligheid

- **Beginnen kan data kosten.** Daarom alleen vanaf de hoogste seqno, met alle nodes gezien, en vlak voor het beginnen nog eens nagaan of intussen een andere node Primary werd (zoals `recoverBegin` al doet). Twee nodes met dezelfde seqno maar een andere cluster-UUID: nooit vanzelf.
- **Een adres wijzigen kan een cluster splitsen.** Daarom nooit vanzelf, en het plan toont oud en nieuw per node met het bestand. Op een node die draait, verandert alleen het bestand; ClusterForge herstart die node niet.
- **Een proces beëindigen** alleen in de cgroup van de unit en alleen als de node niet in een Primary component zit.
- **Vanzelf** staat standaard uit, nooit op prod, en heeft een rem.
- **De agent schrijft alleen** het bestand dat de Galera-adressen al bevat, na een kopie.

## Testen

- De regels van `internal/heal` op vaste groepen: Jonas' storing (oude adressen, hangend proces met unit failed, Node3 non-Primary), een gewone herstart van alle nodes, een node die niet antwoordt, twee UUID's, schijf vol.
- `heal inspect` en `set_galera_address` op voorbeeldbestanden in `/etc/mysql` met `my_print_defaults` als nep.
- Een echte Galera van drie nodes in Docker (zoals `internal/galera/docker_test.go`): alle nodes killen, `heal.run` laat ze weer Synced worden.

## Bouwvolgorde

| # | Mijlpaal | Klaar als | Grootte |
| --- | --- | --- | --- |
| 1 | Vaststellen | De pagina Databases toont bij Jonas' storing "Galera zoekt de andere nodes op oude adressen", "MariaDB hangt op Node1" en "Geen Primary component", elk met het plan. | M |
| 2 | Herstellen | "Nu herstellen" maakt zijn cluster weer 3 van 3 Synced, vanaf de node met de nieuwste data. | L |
| 3 | Vanzelf | Na een herstart van alle nodes in lab begint ClusterForge het cluster zelf; na drie mislukte pogingen stopt het. | M |
| 4 | Breder | Patroni, MySQL en de controle bij Database toevoegen. | M |
| 5 | Documentatie en assistent | Hoofdstuk, What Broke, tool en README. | S |

## Keuzes die al gemaakt zijn

| Keuze | Gekozen | Waarom |
| --- | --- | --- |
| Scripts of acties? | Acties in agent en server | Elke stap is te volgen, gelogd en terug te vinden bij het incident; een script ziet niet wat de andere nodes doen. |
| Vanzelf uitvoeren | Per cluster, standaard uit, nooit op prod | Jonas kiest per cluster hoeveel hij ClusterForge toevertrouwt. |
| Galera beginnen | Alleen vanaf de hoogste seqno met alle nodes gezien | Anders kan de nieuwste data weg zijn. |
| Adressen wijzigen | Altijd bevestigen | Een fout adres splitst het cluster. |
| Onderdelen van ClusterForge | De bestaande acties | Nooit twee systemen voor hetzelfde. |

## Buiten fase 7

- Herstel van apps (Nextcloud, Zabbix) en van keepalived. Kan later met dezelfde bouwstenen.
- Schijfruimte zelf vrijmaken. ClusterForge zegt waar de ruimte heen gaat; weggooien blijft bij Jonas.
- Herstel van Kubernetes; dat doet Kubernetes zelf.
