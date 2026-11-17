# Fase 41: een leek beheert een hele cluster vanuit de GUI

Gevraagd door Jonas op 2026-11-17 19:05 UTC: "een leek moet een hele cluster
kunnen beheren vanuit clusterforge gui", met overal duidelijke uitleg, en:
"schijf is bijna vol zie ik, kan agent zulke checks doen en melding maken?"
(LokaleAI-1 stond op 79 %).

## Grondregels

- **Elke pagina zegt wat ze is.** Wat zie je hier, waarom is het nuttig, wat
  kun je hier doen en wat gebeurt er als je klikt.
- **Geen jargon zonder uitleg.** Een woord als VIP, failover of drift krijgt een
  korte uitleg in gewone taal, op de plek zelf en in één woordenlijst.
- **Een fout zegt wat je moet doen.** Niet alleen "timeout", maar wat er
  misging en de volgende stap.
- **Veilige standaarden.** Wat iets weggooit of herstart vraagt een bevestiging
  die zegt wat er gebeurt en of het terug te draaien is. Wat standaard
  aangevinkt staat, is altijd het veilige.
- **Op de nodes alleen de agent.** Al de rest via de GUI, ook opruimen.
- **Geen nieuwe agentversie nodig.** Alles in deze fase gebruikt wat de
  agent al kan (een script draaien als taak).

## Mijlpalen

### M1: schijfbewaking en ruimte vrijmaken
- Elke agent meldt al de bezetting van zijn bestandssystemen. De server neemt
  per node de volste schijf.
- **Drempels** in Instellingen → Schijfbewaking: standaard 80 % (let op) en
  90 % (ernstig), plus mounts die je wilt negeren.
- **Aandacht nodig** krijgt twee regels: "schijf bijna vol" (oranje) en "schijf
  vol" (rood), met de nodes en het percentage, en een link naar de node.
- **Elke regel in Aandacht nodig krijgt "Wat kun je doen"**: een korte uitleg in
  gewone taal, ook voor de bestaande regels.
- **Node → Schijfruimte**:
  - **Kijk wat ruimte inneemt**: een taak die op de node meet wat er opruimbaar
    is (systeemlogboek, apt-cache, oude logbestanden, Docker, oude tijdelijke
    bestanden) en de grootste mappen.
  - **Ruimte vrijmaken**: een wizard met per onderdeel wat het is, hoeveel het
    vrijmaakt en of het veilig is. De veilige onderdelen staan aangevinkt.
    Daarna meet hij opnieuw en toont voor en na.
  - Op prod vraagt opruimen de gewone bevestiging met TOTP.

### M2: uitleg op elke pagina en een woordenlijst
- Bovenaan elke pagina een blok **"Wat is dit?"**: wat je ziet, waarom, wat je
  kunt doen. Inklapbaar. Een knop **?** in de bovenbalk zet de uitleg overal
  aan of uit (standaard aan).
- **Hulp → Woordenlijst**: de vaktermen in gewone taal (cluster, node, VIP,
  Keepalived, failover, split-brain, drift, quorum, Galera, snapshot, agent,
  TOTP, ...).
- Een **woord met stippellijn** toont de uitleg als je erover gaat of erop tikt.

### M3: foutmeldingen die zeggen wat je moet doen
- Eén vertaler in de webinterface voor gekende fouten: agent offline of te oud,
  TOTP nodig, geen rechten, er loopt al een taak, time-out, schijf vol, apt
  bezet, DNS, Proxmox-token. Elke foutmelding krijgt eronder "Wat kun je doen".
- Ook bij een mislukte taak (Taken) staat de uitleg bij de fout.

### M4: Aan de slag en wizards
- Nieuwe pagina **Aan de slag** met begeleide taken in stappen, met per stap of
  hij al gedaan is (live nagekeken):
  - eerste keer instellen (tweestap aan, Proxmox koppelen, eerste agent);
  - een server toevoegen;
  - een nieuw cluster maken;
  - een server veilig herstarten of onderhouden;
  - updates installeren;
  - de schijf is vol;
  - er staat iets in het rood: wat nu;
  - back-ups controleren.
- Het overzicht toont een kaart "Nieuw hier?" met een link.

### M5: veilige bevestigingen
- Het bevestigingsvenster krijgt "Wat gebeurt er" en "Kun je het terugdraaien"
  voor de gevaarlijke acties (node verwijderen, herstarten, afsluiten, cluster
  verwijderen, opruimen).

## Nummers
- **Geen nieuw protocol**: alles via scripttaken die de agent al kent.
- **Migratie 00125** (`00125_disk_watch`): de drempels en de laatste meting per
  node. 00122-00124 blijven vrij voor fase 40.

## Open punten (voorstel wordt gevolgd tenzij Jonas anders zegt)
1. **Drempels**: 80 % en 90 %, voor alle nodes dezelfde.
2. **Docker opruimen**: niet standaard aangevinkt. Alleen ongebruikte images en
   de bouwcache, nooit volumes.
3. **Uitleg standaard aan**, voor iedereen; wie hem kent, klapt hem dicht.
