# Fase 39: verkeer van NPM, logins van Authentik, naast CrowdSec

Vraag van Jonas: diepere integratie met NPM en Authentik. Logs uitlezen, open
verbindingen bijhouden, vergelijken met de bans van CrowdSec, logins en
mislukte logins van Authentik, en dat alles ook in de API voor homepage.

## Stappen

- **M1 agent (protocol 55)**: nieuwe web-op `npm_traffic`. De agent op de
  NPM-node leest `data/logs/proxy-host-*_access.log` stukje bij stuk (inode en
  offset per bestand, rotatie en afkappen opgevangen, eerste keer alleen de
  laatste 4 MB) en telt per host per minuut en per host per IP. Daarnaast het
  aantal open verbindingen op 80/443 uit `/proc/<pid>/net/tcp(6)` van de
  NPM-container.
- **M2 server (migratie 00120)**: elke minuut ophalen bij NPM en bij de
  events-API van Authentik (login, login_failed, logout,
  authorize_application). Opslag in buckets van 5 minuten en per uur per IP;
  oude data wordt opgeruimd. Elk IP wordt naast de actieve bans van CrowdSec
  gelegd (IP en bereik).
- **M3 API**: `GET /publish/traffic` en `GET /publish/logins` met venster 1h,
  24h of 7d. Homepage-API v2: per app `requests_24h`, `visitors_24h`,
  `errors_24h`, `denied_24h` en `opened_24h`, en de bronnen
  `/api/dashboard/v2/sources/traffic` en `/logins`.
- **M4 GUI**: Publicatie → Verkeer en Publicatie → Logins.
- **Aandacht nodig**: ≥10 mislukte logins in een uur van een IP zonder ban;
  geblokkeerd IP dat meer dan 10 minuten na de ban nog binnenkomt bij NPM
  (bouncer ontbreekt); ≥20 % 5xx op een host; een bron die niet meer leest.

## Getest

Unit-tests voor het lezen van de logs en het tellen van verbindingen, en de
test met een echte NPM in Docker: verkeer door NPM, mislukte logins in een
nagebootste Authentik, een ban voor een gezien IP, en de meldingen.
