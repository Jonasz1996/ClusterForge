# Fase 38: ClusterForge koppelen met AI-verslag

Gevraagd door Jonas op 2026-11-17: "Volgende fase is integratie met
aiverslag, een ander project op mijn GitHub" (github.com/Jonasz1996/aiverslag).

We bouwen verder op de hoogste stam (fase 37, branch
`claude/project-thread-jtwy2l`, bevat fase 32-37). Nummers: agentprotocol
**54**, migraties vanaf **00117** (alleen als een mijlpaal er een nodig heeft).

## Wat AI-verslag is (onderzoek van de repo)

- Een FastAPI-dienst (`aiverslag-web`, uvicorn op `127.0.0.1:8088`, nginx
  ervoor op 443) op **LokaleAI-1** (Proxmox-CT 151, 192.168.10.151, Debian 13)
  met Ollama (qwen3.5). Timers: analyse elk uur (`aiverslag-run`), Inzicht elke
  10 min, werker elke 5 min, momentopname naar GitHub elke nacht.
- Het leest OPNsense (API-sleutels), CrowdSec-LAPI van de firewall,
  Zenarmor-pushes, dreigingsfeeds, Shodan, Proxmox, en maakt daaruit
  bevindingen per categorie (`/api/state`), een dreigingsniveau en score
  (`/api/inzicht/start`), incidenten, alarmen (pushmeldingen) en voorstellen
  met een "fix" (IP blokkeren via alias, dienst herstarten).
- **Geen login**: de webdienst laat schrijfacties alleen toe van dezelfde site
  (Origin + `X-Requested-With: aiverslag`). Voor apps bestaat **koppelen met
  een eenmalige code** (`POST /api/app/tokens` → code, `POST /api/app/pair` →
  Bearer-sleutel). Met die sleutel werkt de hele API, ook van buitenaf, en elk
  verzoek komt in het schrijflog als "app-sleutel".
- `main` van die repo is een spiegel van de server (elke nacht overschreven);
  nieuw werk hoort op een eigen tak.

## Keuze: hoe ClusterForge met AI-verslag praat

| Optie | Voor | Tegen |
|---|---|---|
| A. Jonas maakt in AI-verslag een app-code en plakt die in ClusterForge; de server praat rechtstreeks met https://192.168.10.151 | Geen agent | Handwerk, eigen CA van aiverslag vertrouwen, server (.153) moet VLAN 10 poort 443 bereiken, sleutel vervalt na 90 dagen niet-gebruik |
| **B. cf-agent op LokaleAI-1 koppelt zichzelf lokaal** (code + pair op 127.0.0.1:8088) en geeft de API door | Geen handwerk (alleen de agent eenmalig installeren), sleutel verlaat de CT nooit, geen firewallregel nodig, de agent ziet ook de diensten, timers, Ollama en schijf | Agent nodig op de CT |

**Keuze B**, hetzelfde patroon als PBS en TrueNAS: de agent geeft de lokale
API door, de server beslist met een allowlist wat de GUI mag. De sleutel staat
in `/etc/clusterforge/aiverslag.token` (0600, root) en heet in AI-verslag
"ClusterForge" (zichtbaar en intrekbaar onder 📱 App). Verdwijnt de sleutel
(ingetrokken of verlopen), dan koppelt de agent opnieuw.

## Mijlpalen

| | Wat | Agent |
|---|---|---|
| **M0** | Dit plan (draft PR). | – |
| **M1** | Agent herkent AI-verslag (facts `aiverslag`: versie, diensten en timers, Ollama, schijf, laatste analyse, gekoppeld ja/nee), koppelt zichzelf en geeft de API door (lezen: `aiv_get`; schrijven: run `aiv_call`). Pagina **AI-verslag** → Overzicht: dreigingsniveau met redenen, score, NGFW-tellers (DNS, firewall, CrowdSec, 24 u), open "hoog", laatste alarmen, staat van de diensten en de zelftest. | protocol 54 |
| **M2** | Tabblad **Bevindingen**: per categorie met uitleg en onderzoek; acties analyse nu, onderzoeken, goedkeuren (normaal), verwijderen, fix uitvoeren (voorbeeld + bevestiging; geblokkeerd IP komt in het logboek). Tabblad **Vraag het AI-verslag** (chat met de AI-analist, antwoord stroomt binnen). Alarmen als gezien markeren. | – |
| **M3** | Samen met wat er al is: SOC-pagina (fase 25) neemt het dreigingsniveau van AI-verslag mee; aanvallerkaart en Ctrl K op een IP tonen het oordeel en de opzoeking van AI-verslag; "Ban op alle nodes" (CrowdSec, fase 14) vanuit een AI-verslag-item; regels in **Aandacht nodig** (niveau hoog, alarm niet gezien, dienst of timer stuk, AI-model uit, analyse te oud, schijf > 90 %); dashboard-API + homepage-widget. | – |
| **M4** | Andersom: AI-verslag krijgt context uit ClusterForge, zodat het geen vals alarm geeft en betere uitleg schrijft: namen van nodes, clusters en VIP's per IP, onderhoudsvensters (fase 30) en recente wijzigingen (logboek). Daarvoor een kleine extensie `ext56` in de aiverslag-repo (eigen tak), die ClusterForge via de GUI installeert (knop "Koppeling in AI-verslag installeren", via de agent). | – |

Elke mijlpaal: tests (allowlist, koppelen, nep-aiverslag), schermafdrukken
desktop en gsm, één gecombineerd image op de hoogste stam en het volledige
updatecommando.

## Vragen (zonder antwoord geldt het voorstel)

1. **Agent op LokaleAI-1.** Voorstel: je installeert de agent eenmalig via
   Nodes → Agent installeren op CT 151 (als losse node, omgeving productie).
   Daarna gaat alles via de GUI.
2. **Hoeveel mag ClusterForge doen in AI-verslag?** Voorstel: alles wat de
   eigen GUI van AI-verslag kan (analyse, onderzoek, goedkeuren, fixes), met
   voor fixes die de firewall raken een bevestiging (TOTP omdat het prod is).
3. **Context naar AI-verslag (M4).** Voorstel: ja, via een extensie `ext56`
   op een eigen tak van de aiverslag-repo, door ClusterForge geïnstalleerd.
   Daarvoor heb ik schrijfrechten op die repo nodig; zonder die rechten lever
   ik het bestand mee in ClusterForge zelf.
