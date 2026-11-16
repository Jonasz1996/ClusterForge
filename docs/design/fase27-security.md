# Fase 27: kwetsbaarheden en verharding van het eigen netwerk

Gevraagd door Jonas op 2026-11-16: eerst de eigen infrastructuur beter
beveiligen via de agents, als oefening voor latere pentests (nmap, "exploit
checks"). Alleen Debian 13, geen Ubuntu. Stappenplan in de Claude Doc "Fase 27
Security: analyse en stappenplan".

## Grenzen

- **Alleen eigen scopes.** Netwerken en adressen staan in `security_scopes`.
  Privé-ranges direct; een publiek adres alleen met het overgetikte
  `EIGEN NETWERK` en een notitie waarom het van jou is. Hoogstens /16 per
  netwerk. Scans (M3) gaan alleen naar adressen binnen een scope en buiten de
  uitsluitingen, en de scannende agent controleert dat opnieuw.
- **Vaststellen, niet aanvallen.** Een "exploit check" is: geldt een bekende
  kwetsbaarheid (CVE, en volgens CISA KEV actief misbruikt) voor de versie die
  hier draait. Geen exploitcode, geen payloads, geen brute force.

## Nummers

Afgesproken met fase 26 en 28: fase 27 gebruikt agentprotocol 40-44 en
migraties 00080-00089 (goose weigert migraties buiten volgorde).

## M1: fundament en CVE-check

| Onderdeel | Hoe |
| --- | --- |
| Agent (protocol 40) | Onderwerp `cf.node.<id>.security`, verzoek `inventory`: `dpkg-query` met naam, versie, bronpakket en bronversie, plus os-release en kernel. Alleen lezen. |
| Feeds | De server haalt elke dag (na een fout elk uur) de JSON van de Debian Security Tracker (met ETag) en de KEV-catalogus van CISA. Alleen de releases die op de nodes draaien, plus bookworm en trixie. Weggelaten: urgentie `unimportant` en "niet getroffen" (opgelost in versie 0). Een eigen spiegel kan met `CF_SECURITY_DEBIAN_URL` en `CF_SECURITY_KEV_URL`. |
| Matchen | Per bronpakket: `resolved` met een fixversie hoger dan de geïnstalleerde (dpkg-vergelijking) is een kwetsbaarheid met fix; `open`/`undetermined` een zonder fix. Bij meerdere versies (oude kernels) telt de nieuwste. |
| Ernst | KEV → kritiek; Debian-urgentie high → hoog, medium/end-of-life → middel, low → laag; nog niet ingeschat → middel als van op afstand en met fix, anders laag. |
| Bevindingen | `security_findings`, één per node en bronpakket (soort `cve`), uniek op soort+subject+sleutel. Wat niet meer gezien wordt, is vanzelf opgelost; komt het terug, dan gaat het weer open. Aanvaarden met reden en eventueel einddatum. Een nieuwe KEV-bevinding schrijft `security.kev_found` in het logboek. |
| Inventaris | Elke 6 uur per node (na een fout elk uur), of meteen met "Nu controleren". |
| API | `GET /security`, `POST /security/refresh`, `GET /security/findings`, `POST /security/findings/{id}/accept\|reopen`, scopes CRUD. |
| GUI | Beveiliging → Kwetsbaarheden (bevindingen, per pakket samengevoegd over de nodes; Nodes; Bronnen) en Eigen scopes. |

## Volgende mijlpalen

2. Verharding per node: `sshd -T`, sysctl, UID 0, lege wachtwoorden (alleen
   tellen), SUID, mountopties, unattended-upgrades, tijdsync, auditd;
   optioneel Lynis (CISOfy-repo).
3. Netwerkscans: scannerrol, optionele compose-service `scanner`
   (debian:13-slim, nmap, Trivy; network_mode host; profiel `scanner`),
   Kali-VM als agent; geplande scans binnen scopes; vaste lijst veilige
   NSE-scripts; verwacht (firewallcatalogus + luisterende poorten) tegenover
   werkelijk; TLS/SSH.
4. Panelen zonder login (één anonieme aanvraag), opt-in
   standaardwachtwoordcheck (één poging per dienst), Trivy voor images.
5. SOC-score, Aandacht nodig, rapport per scope.
6. Veilige fixes met één klik (sshd drop-in met terugdraaitimer, sysctl).
7. ClamAV, AIDE, rkhunter/chkrootkit, auditd-regels, unattended-upgrades;
   geen fail2ban (CrowdSec doet dat werk).
