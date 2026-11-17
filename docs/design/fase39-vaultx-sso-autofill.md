# Fase 39: VaultX met Authentik, één keer inloggen en overal ingevuld

Gevraagd door Jonas op 2026-11-17: "verbetering vaultx in samenwerking met
npm/authentik: als ik ingelogd ben op authentik ben ik ook ingelogd op vaultx
en vult deze overal automatisch de opgeslagen gegevens in, gebruikersnaam,
wachtwoord en eventueel MFA-code, allemaal geregeld door de agents."

We bouwen op de hoogste stam bij de start (fase 38, branch
`claude/project-thread-4bl51a`, bevat fase 32-38). Nummers: agentprotocol
**57**, migraties vanaf **00130**. Wat fase 35 nog aan NPM/Authentik bouwt,
neem ik mee zodra het op die branch staat.

## Wat er al is (onderzoek)

- **VaultX** (repo Jonasz1996/vaultx, cluster uit fase 18 op .157/.158/.159,
  VIP .160) logt al in via Authentik (OIDC, PKCE, back-channel logout). Ben je
  bij Authentik aangemeld, dan is "Inloggen met Authentik" één klik zonder
  wachtwoord. Het **account** is dus al SSO.
- De **kluis** zelf is end-to-end versleuteld zoals Bitwarden: de browser of
  app leidt uit je master password de sleutel af, de server ziet alleen
  versleutelde tekst. VaultX geeft de Bitwarden-apps "premium", dus de
  officiële Bitwarden-extensie kan al automatisch invullen, ook de TOTP-code.
- **Fase 35** zet Authentik forward auth op NPM-hosts (blok tussen
  `# clusterforge-authentik`-markeringen, outpost op het Authentik-VIP) en
  praat met NPM en Authentik via de agent met een allowlist.
- VaultX' eigen ontwerp (`00-kernbeslissingen.md`, KB-01/02/03) beschrijft
  precies deze wens en de valkuil: "Authentik-sessie = kluis open" kan niet
  zonder dat iets anders de sleutel bewaart.

## De eerlijke veiligheidsafweging

Er zijn twee aparte dingen: **inloggen** (wie ben je) en **ontgrendelen** (heb
je de sleutel). Authentik bewijst het eerste, niet het tweede.

| Hoe ontgrendelen | Wat je merkt | Wat het kost |
|---|---|---|
| Master password (nu) | Eén keer per apparaat/browserstart typen | Niets: een gestolen database of een gekraakte Authentik geeft geen enkel wachtwoord |
| PIN of vingerafdruk in de extensie | Na de eerste keer alleen PIN of niets tot de browser sluit | Wie je ontgrendelde pc heeft, heeft je kluis |
| **Sleutelbewaarder** ("SSO = kluis open", zoals Bitwardens Key Connector) | Aangemeld bij Authentik = kluis open, nergens nog een master password | Wie Authentik overneemt (of je Authentik-sessie steelt) én de sleutelbewaarder bereikt, heeft **alles**. Authentik wordt dan even belangrijk als de kluis: verplicht MFA (passkey) voor VaultX in Authentik, korte sessie, sleutelbewaarder op aparte, beperkte plek. |

**Automatisch invullen** kan op drie manieren, van veilig naar handig:

1. **Echte SSO in de app** (OIDC of header-login via Authentik): geen
   wachtwoord meer nodig. Altijd de eerste keuze; fase 35 en VaultX fase 5
   doen dit al voor apps die het kennen.
2. **Browserextensie** (Bitwarden-extensie met VaultX als server): vult in op
   jouw apparaat, het wachtwoord bestaat alleen ontsleuteld in jouw browser.
   Werkt overal, ook buiten NPM. Nadeel: alleen in een browser met de
   extensie (niet op een vreemde pc).
3. **Inlog-poort door de agents** (forward auth + invullen op de NPM-nodes):
   NPM vraagt bij elke aanvraag eerst Authentik, daarna de agent op de
   NPM-node; die zet de gebruikersnaam en het wachtwoord (Basic-auth) of
   meldt zelf aan op het loginformulier (met TOTP-code) en geeft de
   sessiecookie terug. Werkt op elk apparaat zonder extensie. **Maar**: het
   wachtwoord moet dan ontsleuteld bestaan op de NPM-nodes. Wie root krijgt
   op een NPM-node, kan die wachtwoorden lezen. Daarom alleen voor apps die
   je per app aanzet, nooit voor je hele kluis, alleen achter Authentik, en
   elke invulling in het logboek.

**Voorstel**: alle drie, elk waar het past. Standaard ladder per app:
SSO als de app het kan, anders extensie, inlog-poort alleen als je het per
app aanzet. En de sleutelbewaarder als bewuste keuze (vraag 1).

## Hoe de inlog-poort werkt (agents)

- Elke NPM-node (template `nginx-proxy-manager`) krijgt in de agent een
  **inlog-poort** (protocol 57). De agent maakt zelf een sleutelpaar
  (X25519); de geheime sleutel verlaat de node nooit
  (`/etc/clusterforge/inlogpoort.key`, 0600). De publieke sleutel komt in de
  facts.
- Je legt een inlog vast in ClusterForge (**Publicatie → Automatisch
  inloggen**): host, gebruikersnaam, wachtwoord, eventueel TOTP-geheim,
  soort (Basic of formulier) en wie (Authentik-gebruikers of -groep). De
  **browser** versleutelt dat naar de publieke sleutel van elke NPM-node.
  De ClusterForge-server ziet en bewaart alleen versleutelde tekst (migratie
  00130) en geeft die door aan de agents.
- Op de host zet ClusterForge naast het Authentik-blok een tweede
  `auth_request` naar de inlog-poort (markeringen `# clusterforge-inlog`),
  via de bestaande fase-35-weg (NPM-API, snapshot vooraf, HTTPS-controle,
  terugdraaien bij fout). De poort luistert alleen op het adres dat de
  NPM-container bereikt, met een geheim per host in de aanvraag, en vertrouwt
  alleen de Authentik-gebruiker die de outpost net bevestigde.
- **Basic-auth**: de agent geeft een `Authorization`-kop terug die NPM naar de
  app stuurt (een kop van de browser zelf wordt eerst gewist).
- **Formulier**: bij het eerste bezoek zonder app-sessie meldt de agent zelf
  aan (recept per app: loginpad, velden, TOTP-veld, succescontrole), en stuurt
  je browser door met de sessiecookie van de app. Recepten voor wat je draait
  (o.a. Zabbix, Portainer, Proxmox, PBS, OPNsense, TrueNAS, NPM, Home
  Assistant); een eigen recept kan in de GUI.
- **TOTP**: de agent rekent de code zelf uit het geheim, op het moment van
  aanmelden.

## Mijlpalen

| | Wat | Agent |
|---|---|---|
| **M0** | Dit plan (draft PR). | – |
| **M1** | **VaultX-SSO nakijken en rechtzetten** via de GUI: pagina VaultX toont per node of OIDC naar het juiste Authentik wijst, sessieduur, back-channel logout, NPM-host `vaultx.jbogaert.be` naar VIP .160 (stond nog naar het oude IP) en of Authentik MFA eist voor de VaultX-applicatie. Knoppen "Rechtzetten" (vaultx-ctl.sh `sso`, node per node) en "MFA verplichten" (Authentik-flow). | – |
| **M2** | **Inlog-poort** in de agent (Basic-auth), sleutelpaar, versleutelen in de browser, migratie 00130, NPM-blok zetten/weghalen met controle. | 57 |
| **M3** | Formulier-login + TOTP, recepten, eigen recept, testen tegen echte apps in CI (een testapp met formulier en TOTP achter echte NPM + nagespeelde Authentik, zoals fase 35). | – |
| **M4** | **Kluis met Authentik ontgrendelen** (sleutelbewaarder) en "Delen met inlog-poort" vanuit de VaultX-webkluis, zodat je wachtwoorden maar op één plek bijhoudt. Dit is werk in de VaultX-repo zelf (zie vraag 2). | – |
| **M5** | Logboek per invulling, **Aandacht nodig** (inlog faalt, wachtwoord veranderd in de app, TOTP fout, node zonder sleutel), drift (iemand haalde het NPM-blok weg), dashboard-API. | – |

Elke mijlpaal: tests, schermafdrukken desktop en gsm, één gecombineerd image
op de hoogste stam en het volledige updatecommando. Op de nodes niets
handmatig: de agents bijwerken gaat via Nodes → Agents bijwerken.

## Vragen (zonder antwoord geldt het voorstel)

1. **Kluis openen met alleen Authentik (sleutelbewaarder)?** Voorstel: ja,
   maar alleen samen met verplichte MFA (passkey of TOTP) in Authentik voor
   VaultX en een sessie van hooguit 12 uur; je master password blijft werken
   als reserve. Zonder dit blijft het: Authentik voor het account, master
   password of PIN voor de kluis.
2. **Schrijfrechten op de VaultX-repo.** M4 is werk in VaultX zelf. Voorstel:
   voeg `Jonasz1996/vaultx` toe in Project settings → Repositories; ik werk op
   een eigen tak met een PR. Zonder die rechten bouw ik M1-M3 en M5 en houdt
   ClusterForge de inloggegevens voor de poort zelf bij (versleuteld).
3. **Inlog-poort per app aanzetten of overal?** Voorstel: per app, alleen
   voor apps zonder SSO, en alleen voor jouw Authentik-account. Overal aan
   zou alle wachtwoorden op de NPM-nodes zetten.

## Stand

- M0: dit plan.
