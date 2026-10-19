# ClusterForge – Ontwerp: verhuizen zonder commando's

Stand: 2026-10-19

## Samenvatting

Verhuizen (fase 5) kopieert een database naar een ander cluster en controleert de kopie, maar de app zelf raakt ClusterForge bewust niet aan. Bij Nextcloud moet Jonas daarom nog drie keer een commando op de nodes draaien: onderhoud aan, `dbhost` aanpassen op nc1-3, onderhoud uit. De GUI toont die commando's om te kopiëren (`web/src/components/databases/Move.tsx`, stap 1 van "Verhuizing starten" en stap 1 en 2 van "De kopie staat klaar").

Jonas wil op de nodes maar één commando ooit draaien: de eenmalige installatie van de agent met het token. Al de rest gaat via de GUI. Dit ontwerp laat de agent op de app-nodes die drie stappen zelf doen, als deel van de verhuizing: één bevestiging, daarna loopt alles door tot de app op de nieuwe database draait, of tot de eerste fout, waarna ClusterForge alles terugzet zoals het was.

| Mijlpaal | Wat Jonas krijgt |
| --- | --- |
| 1 | De agent herkent Nextcloud op een node en leest de instellingen die ertoe doen. Het plan van Verhuizen en de proef tonen per app-node wat er straks verandert, en wat in de weg zit. Nog niets wijzigt. |
| 2 | "Verhuizing starten" doet de app-stappen zelf: onderhoud aan, kopie, `dbhost` op elke node, controle, onderhoud uit, overschakelen. Bij een fout zet ClusterForge de app terug op de oude database. Op de clusterpagina een knop "Onderhoud aan/uit" voor Nextcloud. |
| 3 (later, op vraag) | Dezelfde aanpak voor de andere apps op het databasecluster: Gitea, Zabbix, Home Assistant, PhotoPrism. |

## Keuzes en waarom

**Eén bevestiging voor de hele keten, geen knop per stap.** De voorzet was drie knoppen, elk met een bevestiging. Gekozen is één bevestiging bij "Verhuizing starten" die alle stappen dekt, met de lijst van wat er per node gebeurt. Reden: tussen onderhoud aan en onderhoud uit staat Nextcloud stil voor iedereen. Met drie knoppen duurt dat zo lang als Jonas nodig heeft om terug te komen en te klikken, en de duur die de proef meet, klopt dan niet meer. Met één keten is het onderhoudsvenster de gemeten duur plus enkele seconden. De bevestiging draagt een hash van het plan (zoals bij Zelfherstel): wat Jonas zag, is wat er loopt. Op prod vraagt ze de slug en TOTP, zoals Verhuizen nu al doet.

**Stoppen bij de eerste fout, en terugzetten.** Elke stap meldt de foutregel van de node in de taak. Hoe ver de keten kwam, bepaalt het terugzetten:

| Fout tijdens | ClusterForge doet |
| --- | --- |
| Onderhoud aan, wachten op stilte, kopie, vergelijken | De kopie gaat weg (zoals nu). Onderhoud uit op de nodes waar ClusterForge het aanzette. De app draait verder op de oude database. |
| `dbhost` zetten of de controle daarna | Op elke node die al omgezet was, de oude waarde terug (letterlijk zoals ze in config.php stond, bewaard in de state van de taak). Daarna onderhoud uit. De kopie blijft staan, zodat een tweede poging niet opnieuw hoeft te kopiëren. |
| Onderhoud uit | Niets terug: de app staat al op de nieuwe database en die is gecontroleerd. De taak meldt welke node nog in onderhoud staat, met de knop "Onderhoud uit" erbij. |
| Lukt het terugzetten zelf niet | De taak zegt per node wat er nu in config.php staat en of onderhoud aan is, met de knoppen om het recht te zetten. Nooit een tweede poging zonder Jonas. |

Stond een node al in onderhoud voor de verhuizing, dan laat ClusterForge dat zo en zegt het.

**Overschakelen gebeurt vanzelf als het mag.** Na onderhoud uit wacht de taak tot 2 minuten op de voorwaarden die de knop "De app draait op de nieuwe database" nu controleert (`SwitchMove`, `internal/components/move.go`): niemand meer op de oude database, de app wel op de nieuwe. Dan zet ClusterForge de oude op slot, zoals nu. Komt er in die tijd geen verbinding (een stille avond), dan blijft de verhuizing op "De kopie staat klaar" met de bestaande knop; het slot is dan het enige dat nog ontbreekt, niet een commando.

**Vaste acties, geen vrije commando's.** De agent krijgt geen "voer dit uit". Hij kent een handvol acties met vaste argumenten; waarden zoals het nieuwe adres worden op beide kanten gecontroleerd (een IP-adres of hostnaam, eventueel met poort). Zo kan een bericht over NATS nooit een willekeurig commando op een node starten.

**Het wachtwoord van Nextcloud blijft op de node.** De agent leest `config.php` zelf en stuurt alleen `dbtype`, `dbhost`, `dbname`, `dbuser`, `maintenance`, `version` en `instanceid` naar de server. De controle van de nieuwe database met de gegevens van de app doet de agent op de node.

**De firewall blijft van Jonas.** De kopie loopt van de doelnode naar de oude server, en de app-nodes moeten de nieuwe database bereiken. ClusterForge controleert beide al in de proef en noemt de regel die ontbreekt, maar wijzigt de firewall niet.

## Mijlpaal 1: Nextcloud herkennen en lezen

### Agentprotocol

Een nieuw verzoek op `cf.node.<id>.app`, in het eerstvolgende vrije protocolnummer (24, of 25 als fase 8 eerder gebouwd wordt):

| Actie | Wat het doet |
| --- | --- |
| `inspect` | Zoekt Nextcloud: `config/config.php` naast `occ` onder `/var/www/*`, `/srv/*`, `/usr/share/nginx/*`, en onder de webroots uit de actieve configuratie van nginx en Apache. Per installatie: pad, eigenaar van `config.php`, de velden hierboven, of `occ` start (`occ status --output=json`), de versie van PHP, en een hash van `config.php` zonder de geheime velden. |
| `nextcloud.maintenance` | `occ maintenance:mode --on` of `--off`, daarna opnieuw lezen en controleren. |
| `nextcloud.dbhost` | `occ config:system:set dbhost --value=<adres>`, daarna opnieuw lezen. Antwoordt met de oude waarde. |
| `nextcloud.dbcheck` | Logt met `dbuser` en `dbpassword` uit `config.php` in op een gegeven adres en leest één rij uit `<dbtableprefix>appconfig`. Wijzigt niets. |
| `nextcloud.add_primary_keys` | `occ db:add-missing-primary-keys`, de oplossing die het plan van Verhuizen nu als commando toont (`migrate.go`). |

`occ` loopt als de eigenaar van `config.php` (`runuser -u <eigenaar> -- php <pad>/occ … --no-interaction`), zonder shell, met een time-out van 2 minuten; de uitvoer gaat naar de taak.

Een te oude agent geeft het bekende "agent te oud". Het plan zegt dan welke nodes, met de knop "Agents bijwerken" uit #100 ernaast. Geen commando: wie "vanzelf bijwerken" aan heeft, heeft de nieuwe agent al.

### Welke nodes de app-nodes zijn

De nodes met een agent waarvan het adres in "Gebruikt door" van de database staat (`db_clients`), plus de nodes van het doelcluster. Op elk ervan vraagt ClusterForge `inspect` en houdt de installaties waarvan `dbname` de verhuisde database is en `dbhost` naar de oude server of zijn VIP wijst. Een adres in "Gebruikt door" zonder agent of zonder herkende Nextcloud wordt een vondst: daar kan ClusterForge de app niet omzetten.

### In het plan en de proef

Het plan van Verhuizen krijgt een blok "De app" met per node: pad, huidige `dbhost` → nieuw adres, onderhoud nu aan of uit. Nieuwe vondsten:

| Vondst | Niveau |
| --- | --- |
| Een node uit "Gebruikt door" zonder agent of zonder herkende Nextcloud | Blokkeert de automatische app-stappen; Verhuizen kan nog met de hand zoals nu. |
| `config.php` verschilt tussen de nodes (hash) | Waarschuwing: na de verhuizing staat overal hetzelfde `dbhost`; de rest blijft zoals het is. |
| Een sync-taak van ClusterForge (fase 6) of een regel in een crontab raakt de map `config/` | Waarschuwing: een sync kan het nieuwe `dbhost` overschrijven met een oude kopie. ClusterForge zet alle nodes in dezelfde stap om, maar een sync van een node buiten de verhuizing zou het terugdraaien. |
| `occ status` faalt | Blokkeert, met de foutregel. |
| Tabellen zonder primaire sleutel | Zoals nu, maar met de knop "Primaire sleutels toevoegen" in plaats van het commando. |

De proef doet `nextcloud.dbcheck` vanaf elke app-node naar het adres van het doelcluster tegen de proefkopie, zodat een ontbrekende firewallregel of een gebruiker zonder recht vanaf die host uitkomt voor de echte verhuizing.

## Mijlpaal 2: de verhuizing doet de app-stappen

`moveSteps` (`internal/components/move.go`) krijgt stappen voor en na de kopie wanneer het plan een Nextcloud vond:

1. Controle (bestaand), nu met de app-nodes en hun `inspect` in de state.
2. **Onderhoud aan** op elke app-node.
3. Wachten tot niemand nog verbonden is (bestaand; de wachttijd van 30 s blijft).
4. Gebruikers, kopie en vergelijken (bestaand).
5. **`dbhost` zetten** op elke app-node, oude waarde in de state.
6. **Controle** met `nextcloud.dbcheck` vanaf elke app-node naar het nieuwe adres.
7. **Onderhoud uit** op elke app-node waar ClusterForge het aanzette.
8. **Overschakelen**: wachten op de voorwaarden van `SwitchMove`, daarna het slot op de oude database.

Elke stap is hervatbaar zoals de andere stappen van de taak: na een herstart van de server leest hij zijn state en gaat verder, of zet terug.

In de GUI verdwijnen de `CopyBlock`'s met `occ`. Het venster "Verhuizing starten" toont in de plaats daarvan wat ClusterForge per node zal doen, met één vinkje "ClusterForge zet Nextcloud zelf in onderhoud en om" (standaard aan als alles herkend is). Uit betekent: de verhuizing zoals nu, met de commando's.

De clusterpagina krijgt voor een cluster met Nextcloud een kaart "Nextcloud" met per node de versie, `dbhost` en onderhoud, en de knoppen "Onderhoud aan" en "Onderhoud uit" (alle nodes tegelijk, met bevestiging; prod met TOTP). Elke actie komt in het logboek.

## Mijlpaal 3 (later): andere apps

Dezelfde vorm, met per app de plek van het adres: Gitea (`app.ini`, `[database] HOST`, herstart van de unit), Zabbix (`zabbix_server.conf` `DBHost` en `zabbix.conf.php`), Home Assistant (`recorder: db_url`), PhotoPrism (`PHOTOPRISM_DATABASE_SERVER`). Die hebben geen onderhoudsmodus; daar stopt de agent de dienst en start hem na de controle. Pas bouwen als Jonas een van die databases wil verhuizen.

## Wat niet verandert

- De kopie, de vergelijking, het slot, "Slot eraf halen" en "Oude database afbouwen" blijven zoals in fase 5.
- Zonder herkende app loopt Verhuizen zoals nu en toont het de stappen in tekst.
- ClusterForge wijzigt geen firewall, geen HAProxy en geen andere instelling van Nextcloud dan `dbhost` en `maintenance`.

## Tests

- Agent: `agenttest` krijgt een nagespeelde Nextcloud (map met `config.php` en een `occ` dat de aanroepen registreert), ook met een `occ` dat faalt en een `config.php` die na het schrijven niet klopt.
- Verhuizen: de hele keten groen; een fout in elke stap met het terugzetten uit de tabel hierboven; een herstart van de server midden in stap 5; een node die al in onderhoud stond.
- Web: de schermen van het plan, de bevestiging (lab en prod) en de taak, op desktop en mobiel, in `/mnt/project-files/clusterforge/schermen/`.
