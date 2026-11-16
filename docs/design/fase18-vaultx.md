# Fase 18: VaultX als HA-cluster

Gevraagd op 2026-11-15. VaultX (github.com/Jonasz1996/vaultx) is de eigen
wachtwoordkluis: wachtwoorden, MFA-tokens, notities en bijlagen, met de
Bitwarden-apps en een webkluis. Het draait nu op één Debian 13-CT
(`vaultx`, 192.168.115.157) zonder Docker: `/opt/vaultx`, `vaultx.service`,
nginx op 80/443 en een lokale PostgreSQL 17. Doel: een cluster dat een node
kan verliezen, met een overname van de bestaande kluis **zonder verlies**.

## Wat VaultX bewaart

- **PostgreSQL**: gebruikers, de end-to-end versleutelde kluis, notities,
  bijlagen (bytea), sessies, apparaten, auditlog, mislukte logins.
- **`/etc/vaultx/vaultx.env`**: `VAULTX_SECRET_KEY` (versleutelt
  NPM-wachtwoorden, client secrets en de kluistokens), OIDC-client, publieke
  URL, Authentik-token.
- Niets op schijf buiten die twee. De live sync naar de Bitwarden-apps loopt
  via PostgreSQL `LISTEN/NOTIFY` en werkt al over meerdere instanties; de
  NPM-planner neemt een advisory lock per koppeling; lockouts staan in de
  database. VaultX kan dus op elke node tegelijk draaien.

## Opbouw

- Template `vaultx` 1.0.0, rol `node`, 3 of 5 **nieuwe** CT's (Debian 13).
  De oude node wordt geen clusternode: Patroni op die node zou zijn
  PostgreSQL moeten verplaatsen, en de oude node moet onaangeroerd blijven
  tot de overname geslaagd is.
- **PostgreSQL met Patroni** op alle nodes (`database: engine: postgresql`,
  initdb UTF8/C.UTF-8 zoals VaultX vraagt), eigen VIP `db_vip` op de
  primary. ClusterForge maakt gebruiker en database `vaultx` (eigenaar).
- **VaultX** native, zoals op de oude node (er zijn geen gepubliceerde
  images): `git clone` naar `/opt/vaultx` op de gekozen versie (commit, tag
  of branch), venv, frontend gebouwd met Node 22, nginx-site en
  zelfondertekend certificaat uit `deploy/debian` van VaultX zelf.
  `cf-vaultx.service` (eigen unit, dezelfde hardening als `vaultx.service`)
  draait op elke node, `Restart=always`. Nooit `update.sh` van VaultX op een
  clusternode draaien: bijwerken gaat via ClusterForge (Wijzigen → Versie).
- **Migraties**: `ExecStartPre` draait `alembic upgrade head` onder een
  PostgreSQL-advisory lock (`vaultx-migreer.py`), zodat drie nodes die
  tegelijk starten niet tegelijk migreren.
- **Configuratie**: `vaultx-ctl.sh voorstart` (als root) schrijft
  `/etc/vaultx/vaultx.env` (0640 root:vaultx) uit de basis van de template
  (`/etc/cf-vaultx/base.env`, 0600: database via `db_vip`, en voor een nieuw
  cluster publieke URL, sleutel en OIDC) en, na een overname, de
  overgenomen `VAULTX_*`-instellingen van de oude node
  (`/opt/cf-vaultx/files/.cf/vaultx.env`, 0600). De overgenomen waarden
  winnen, behalve `VAULTX_DATABASE_URL`.
- **Sync** `VaultX-sleutel`: elke minuut `/opt/cf-vaultx/files/` van de
  VIP-node naar de andere nodes (alleen `.cf/vaultx.env` en
  `.cf/nginx-realip.conf`), zonder `--delete`.
- **Keepalived** zonder `nopreempt`, `vaultx-ctl.sh check` (backend
  `/health/ready` en nginx) met `weight 50`.
- **ufw**: als ufw actief is, regels voor etcd, Patroni, PostgreSQL en VRRP
  tussen de nodes (zoals Authentik). De rest beheert Jonas zelf.
- **Back-up**: `cf-vaultx-backup.timer` elke nacht op elke node; alleen de
  node met het database-VIP maakt een `pg_dump -Fc` plus een kopie van de
  instellingen in `/var/backups/cf-vaultx` (0700), laatste 14.

## Markering en veiligheid

`COMMENT ON DATABASE vaultx` = `cf-vaultx key=<sha256[:16] van de sleutel>
ready=<tijd> …`. `cf-vaultx` start alleen als de markering er is en de
sleutel van de node dezelfde vingerafdruk heeft. Zonder markering:

- `modus: nieuw`: node 1 neemt een volledig lege database in gebruik.
- `modus: overnemen` (standaard): geen enkele node start; VaultX wacht op
  de overname. Zo begint nooit een lege kluis naast de echte.

Geheimen staan nooit in de uitvoer van taken, in `vaultx-ctl.sh` (0755) of
in tests: `VAULTX_SECRET_KEY` en wachtwoorden staan alleen in bestanden met
0600/0640, scripts tonen enkel de vingerafdruk.

## Overname

Op de **VIP-node** van het cluster, via Commando's:

1. `vaultx-ctl.sh bron-koppelen 192.168.115.157` maakt een eigen
   ed25519-sleutel en toont één regel voor de oude node. Die regel (op de
   oude node uitvoeren, ook via Commando's) installeert
   `/usr/local/sbin/cf-vaultx-bron` en zet de sleutel in
   `/root/.ssh/authorized_keys` met `restrict,from="<IP van de VIP-node>",command="…"`:
   de sleutel kan alleen de vaste opdrachten van dat script aanroepen.
2. `vaultx-ctl.sh overnemen 192.168.115.157`:
   1. Controles: modus overnemen, VIP hier, database leeg en zonder
      markering, `info` van de oude node: commit, alembic-versie,
      PostgreSQL-versie. De migratie van de oude database moet in de code
      van het cluster zitten; een andere commit vraagt `--andere-versie`.
   2. **Oude VaultX stoppen** (`systemctl disable --now vaultx`; nginx en
      PostgreSQL blijven draaien, er wordt niets gewist). Vanaf hier
      draait elke fout de overname terug en start de oude VaultX weer.
   3. Telling op de oude database: per tabel het aantal rijen en een md5
      over alle rijen (tabellen tot 64 MB; grotere alleen het aantal).
   4. **Back-up op de oude node**: `pg_dump -Fc` naar
      `/var/backups/vaultx/cf-overname-<tijd>.dump` plus de env, gecontroleerd
      met `pg_restore --list`; daarna gaat dezelfde dump naar de VIP-node
      en moet de sha256 gelijk zijn.
   5. `pg_restore --single-transaction --exit-on-error --no-owner` in de
      database van het cluster.
   6. Telling op het cluster: elk aantal en elke md5 moet gelijk zijn.
   7. Instellingen (`VAULTX_*` uit de env) en `nginx-realip.conf`
      overnemen, markering zetten, `cf-vaultx` hier starten en wachten op
      `/health/ready`.
   8. Klaar: de andere nodes starten zodra de sync de instellingen bracht.
   Bij een fout in 3-7: database van het cluster leeg, overgenomen
   instellingen weg, oude VaultX weer `enable --now`.
3. Jonas zet in NPM de proxy host `vaultx.jbogaert.be` naar het VIP:80.
4. `vaultx-ctl.sh afronden 192.168.115.157` haalt de sleutel van de oude
   node weg. De oude node blijft staan (gestopt) als terugweg.

## De bestaande node als clusternode

Jonas wil de bestaande node houden en uitbreiden naar drie (2026-11-16).
Dat kan ook: rol uit op bestaande nodes, met de oude VaultX-node erbij.

- De uitrolstap `vaultx-ctl.sh voorbereiden` (alleen op een node met
  `/etc/systemd/system/vaultx.service`): eerst een gecontroleerde
  `pg_dump` naar `/var/backups/vaultx/cf-voorbereiden-*.dump`, dan
  `pg_renamecluster <v> main vaultx-oud` en poort 5433 (de data blijft
  staan), `VAULTX_DATABASE_URL` naar 5433, een drop-in `PGPORT=5433` voor de
  oude back-uptimer, VaultX weer starten en wachten op `/health/ready`.
  Mislukt iets, dan gaat alles terug zoals het was. Zonder die stap zou
  `drop-main.sh` van Patroni weigeren (main heeft eigen databases).
- Tot de overname bedient de oude VaultX op die node het VIP (hij is de
  enige gezonde). NPM kan dus al vóór de overname naar het VIP wijzen.
- `vaultx-ctl.sh overnemen` zonder IP op die node: de bron is lokaal
  (`cf-vaultx-bron` met `PGPORT=5433`, zonder SSH). Na de overname staan
  `vaultx.service` en de oude back-uptimer uit; het cluster `vaultx-oud`
  blijft met zijn data staan als terugweg.
- `bouwen` weigert op die node andere code dan de commit van de oude
  VaultX zolang die nog draait.
- `terug` zonder IP op die node: `cf-vaultx` uit, de instellingen van de oude
  VaultX terug, `vaultx.service` en de back-uptimer weer aan.

## Terugweg

Zolang de oude node bestaat: `vaultx-ctl.sh terug 192.168.115.157` op de
VIP-node haalt de markering weg (geen node start nog), stopt VaultX op
deze node en start de oude VaultX weer; daarna `systemctl disable --now
cf-vaultx` op de andere nodes (Commando's) en NPM terug naar .157.
Wijzigingen in de kluis sinds de overname staan dan alleen in het cluster;
`terug` maakt er eerst een dump van in `/var/lib/cf-vaultx/overname/`.

## Mijlpalen

- M1: ontwerp + template (installatie, Patroni, keepalived, ufw, back-up),
  overname, terugweg, tests (render, shellcheck, overname en terugdraaien
  tegen een echte PostgreSQL).
- M2: na het eerste gebruik bij Jonas: verbeteringen uit de praktijk.

Geen agent- of migratiewijziging nodig (geen protocol- of migratienummer).
`cf-vaultx` komt bij de bewaakte units en in de firewallcatalogus.
