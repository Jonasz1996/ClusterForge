# Agents werken zichzelf bij

Tot nu toe werkte je na elke serverupdate elke node met de hand bij
(`agent.sh --upgrade`) en herbouwde je het golden image. Vanaf agentprotocol 23
doet de server dat zelf: als taak met een resultaat per node, of vanzelf na
een serverupdate.

## Wat de agent doet (protocol 23, commando `agent.upgrade`)

1. Het commando komt over de vastgepinde NATS-verbinding en draagt de sha256
   van de nieuwe binary per architectuur. Daarom mag de download zelf over
   http: een gewijzigde download valt door de sha256.
2. De agent haalt `/downloads/cf-agent-linux-<arch>` van zijn eigen server
   (`server_url` uit `agent.json`) in de map van zijn binary en controleert de
   sha256.
3. Hij start de nieuwe binary met `cf-agent self-check`: die moet hier draaien
   en een protocol melden dat niet lager is dan het zijne. Nooit terug naar een
   oudere versie.
4. Hij bewaart zichzelf als `/usr/local/bin/cf-agent.prev`, schrijft
   `/var/lib/clusterforge/upgrade.json` en zet met `systemd-run` een tijdelijke
   timer `cf-agent-rollback` die over 3 minuten `cf-agent.prev upgrade-rollback`
   start. Lukt dat niet, dan stopt hij: er is nog niets vervangen.
5. Hij vervangt zichzelf in één rename en herstart met
   `systemctl --no-block restart cf-agent`.
6. De nieuwe agent bevestigt zodra de server iets van hem aanneemt (facts met
   Ack): de timer gaat weg en `upgrade.json` ook. Pas dan meldt zijn heartbeat
   de nieuwe sha256, zodat de server een agent die crasht of niet aangenomen
   wordt niet als bijgewerkt telt.
7. Bevestigt hij niet op tijd (crasht, verbindt niet), dan zet de timer de oude
   binary terug, herstart cf-agent en schrijft `rolled_back` in
   `upgrade.json`. De oude agent meldt in zijn heartbeat welke sha256 mislukte;
   de server probeert die dan niet nog eens vanzelf.

Na een reboot binnen die 3 minuten is de tijdelijke timer weg; de nieuwe agent
zet hem bij zijn start terug met de tijd die er nog over was. Een agent die al
bij het starten crasht, kan dat niet: zet de oude dan terug met
`sudo /usr/local/bin/cf-agent.prev upgrade-rollback`. `agent.sh --upgrade` helpt
hier niet: dat haalt dezelfde binary opnieuw.

De heartbeat meldt vanaf 23 ook `arch` en `binary_sha256`, zodat de server
ziet of een agent dezelfde is als die hij aanbiedt, ook bij hetzelfde protocol.

## Wat de server doet

- Een agent is verouderd als zijn protocol lager is dan dat van de server, of
  hetzelfde met een andere sha256 dan de binary die de server aanbiedt. Een
  agent met een hoger protocol dan de server wordt nooit aangeraakt.
- Agents onder protocol 23 kennen het commando niet: die werk je één keer met
  de hand bij. De server toont het commando erbij.
- Taak `agents.upgrade`, één per cluster: één node tegelijk, wachten tot hij
  met de nieuwe sha256 terug is, dan de volgende. Komt een node niet terug of
  wordt hij teruggezet, dan stopt de taak voor dat cluster, met een
  logboekregel. Een node die niet terugkomt, staat offline en opent zo een
  incident zoals elke node die wegvalt.
- Knop "Agents bijwerken" bij Nodes (alle verouderde agents buiten prod, per
  prodcluster een eigen knop met bevestiging) en per node op de nodepagina.
  Prod vraagt tweestapsverificatie en de slug, zoals andere prod-acties.
- Instelling "Agents vanzelf bijwerken" (standaard aan) bij Instellingen, per
  cluster uit te zetten. Nooit vanzelf op prod. De server kijkt elke minuut
  en start een taak per cluster met verouderde agents die verbonden zijn.

## Gevolg

Het golden image hoeft niet meer na elke update opnieuw: een VM uit een ouder
image meldt zich aan en wordt daarna vanzelf bijgewerkt (behalve op prod, daar
met de knop). Alleen een image met een agent onder protocol 23 maak je nog één
keer opnieuw.
