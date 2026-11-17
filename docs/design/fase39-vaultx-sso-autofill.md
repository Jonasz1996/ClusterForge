# Fase 39: VaultX met Authentik, één keer inloggen en overal ingevuld

Gevraagd door Jonas op 2026-11-17: "verbetering vaultx in samenwerking met
npm/authentik: als ik ingelogd ben op authentik ben ik ook ingelogd op vaultx
en vult deze overal automatisch de opgeslagen gegevens in, gebruikersnaam,
wachtwoord en eventueel MFA-code, allemaal geregeld door de agents."

Antwoord van Jonas op de eerste versie van dit plan (15:55 UTC):

> Alles moet standalone blijven werken als een certificaat vervallen is of
> nodes down zijn, anders ligt misschien alles plat. Alles blijft werken
> zoals het is, behalve: als ik bij Authentik aangemeld ben, heb ik echte
> SSO overal: de wachtwoordkluis en alles wat erin zit, niet via SAML/OAuth
> maar gewoon automatisch invullen van de gegevens en Tab, Enter.

Daarom is de eerste versie (een inlog-poort in NPM, in het pad van elke
aanvraag) **geschrapt**: als die poort, de agent of Authentik weg is, zou de
app onbereikbaar worden. Het nieuwe plan raakt NPM en de apps niet aan.

We bouwen op de hoogste stam bij de start (fase 38, branch
`claude/project-thread-4bl51a`). Nummers: agentprotocol **57**, migraties
vanaf **00130**.

## Uitgangspunt: alles faalt naar "zoals nu"

| Wat valt weg | Gevolg |
|---|---|
| Authentik, sleutelbewaarder of ClusterForge | De extensie vraagt je master password (of PIN); invullen werkt daarna gewoon |
| VaultX | De extensie werkt met de laatst gesynchroniseerde kluis (versleuteld in de browser), zoals de Bitwarden-apps |
| De extensie zelf | Je typt zelf, zoals nu |
| Certificaat vervallen | Niets verandert aan de apps; de extensie praat met VaultX en de sleutelbewaarder, valt anders terug op het master password |

Geen enkele app, geen NPM-host en geen Authentik-provider wordt gewijzigd.

## Hoe het werkt

1. **Browserextensie "VaultX Invuller"** (Chrome, Edge, Firefox; Manifest V3).
   Ze praat met VaultX zoals de Bitwarden-apps (dezelfde API, dezelfde
   end-to-end versleuteling), herkent op elke pagina het loginformulier,
   vult gebruikersnaam en wachtwoord in, en op een volgende stap of in een
   TOTP-veld de MFA-code, en drukt Enter. Per item instelbaar: alleen
   invullen, of invullen en versturen (standaard versturen). Nooit op een
   pagina waarvan het adres niet bij het item past, nooit in een iframe van
   een ander domein, nooit op http behalve op adressen die je zelf toelaat.
2. **Ontgrendelen met Authentik.** Eén keer per browser ontgrendel je met je
   master password; de extensie geeft dan een versleutelde kopie van je
   kluissleutel aan de **sleutelbewaarder**. Daarna: ben je bij Authentik
   aangemeld (met MFA), dan vraagt de extensie de sleutel terug en is de kluis
   open zonder master password. Afmelden bij Authentik of 12 uur zonder
   Authentik-sessie vergrendelt de kluis weer.
3. **De sleutelbewaarder draait in de agents op de VaultX-nodes** (.157,
   .158, .159, achter het VaultX-VIP .160 op een eigen pad). Elke node
   antwoordt zelf, dus één node down is geen probleem. Hij geeft een
   sleutel alleen terug met een geldig Authentik-token (handtekening,
   vervaldatum, MFA gebruikt) én de apparaatsleutel van die browser, zodat
   een gestolen Authentik-sessie alleen niet volstaat. Wat in de database
   ligt, is versleuteld met een sleutel die alleen op de VaultX-nodes staat.
4. **ClusterForge** zet het op via de GUI: sleutelbewaarder aan op het
   VaultX-cluster, Authentik-applicatie voor de extensie (met MFA-eis),
   extensie downloaden, lijst van browsers/apparaten met intrekken, logboek
   van ontgrendelingen, en "Aandacht nodig" als de sleutelbewaarder op een
   node niet antwoordt.

## De eerlijke veiligheidsafweging

- De kluis blijft end-to-end versleuteld; de VaultX-server ziet nog steeds
  nooit een wachtwoord.
- **Nieuw risico**: wie tegelijk je Authentik-account (met MFA) én een
  ontgrendelde browser van jou heeft, heeft je kluis. Dat is ongeveer
  hetzelfde als vandaag met een extensie die ontgrendeld blijft. Wie alleen
  Authentik overneemt, heeft niets, want de apparaatsleutel ontbreekt.
- Wie root krijgt op een VaultX-node én op je pc, kan alles. Dat was al zo.
- Automatisch Enter op een nagemaakte loginpagina is het grootste gevaar
  van invullen; daarom alleen op exact het adres van het item.

## Mijlpalen

| | Wat | Agent |
|---|---|---|
| **M0** | Dit plan (draft PR). | – |
| **M1** | Extensie: aanmelden bij VaultX met master password, kluis synchroniseren en ontsleutelen (Bitwarden-formaat), loginformulieren herkennen, invullen, TOTP, Enter. Test in CI met echte Chromium tegen een nagespeelde VaultX en testpagina's (gewone login, twee stappen, TOTP-veld, nagemaakt domein). | – |
| **M2** | Sleutelbewaarder in de agent op de VaultX-nodes, apparaatsleutel per browser, Authentik-token controleren. | 57 |
| **M3** | ClusterForge-GUI: aanzetten op het VaultX-cluster, Authentik-applicatie met MFA-eis, extensie downloaden, apparaten en intrekken, logboek (migratie 00130). | – |
| **M4** | Aandacht nodig, dashboard-API, schermafdrukken en het gecombineerde image. | – |

## Vragen (zonder antwoord geldt het voorstel)

1. **Enter automatisch?** Voorstel: ja voor alle items, per item uit te
   zetten.
2. **Welke browsers?** Voorstel: Chrome/Edge en Firefox. De extensie zelf
   installeren in je browser is de enige stap die niet via de GUI kan.

## Stand

- M0: plan herschreven na het antwoord van Jonas (geen inlog-poort in NPM).
