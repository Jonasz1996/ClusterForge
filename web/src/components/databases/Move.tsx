"use client";

import Link from "next/link";
import { useState, type ReactNode } from "react";
import { CopyBlock } from "@/components/inventory/InstallAgent";
import { tableClass, tdClass, thClass } from "@/components/inventory/bits";
import { ProdConfirm, prodConfirmed, useProdTotp } from "@/components/ProdConfirm";
import { Alert, Badge, Button, Card, cx, Input } from "@/components/ui";
import { ApiError } from "@/lib/api/client";
import type { ComponentFinding } from "@/lib/components";
import {
  engineLabel,
  useCancelMove,
  useLockMove,
  useStartMove,
  useSwitchMove,
  type DatabaseGroup,
  type MigrationPlan,
  type MigrationRun,
  type MigrationTarget,
} from "@/lib/databases";
import { useIsAdmin } from "@/lib/inventory";

export const nl = (n: number) => n.toLocaleString("nl-BE");

export function plural(n: number, one: string, many: string) {
  return `${nl(n)} ${n === 1 ? one : many}`;
}

export function seconds(ms: number | null) {
  if (ms === null) return "–";
  const s = ms / 1000;
  if (s < 60) return `${s.toLocaleString("nl-BE", { maximumFractionDigits: 1 })} s`;
  const m = Math.floor(s / 60);
  return `${m} min ${Math.round(s - m * 60)} s`;
}

export function Item({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div>
      <dt className="text-xs font-medium uppercase tracking-wide text-slate-500">{label}</dt>
      <dd className="mt-0.5">{children}</dd>
    </div>
  );
}

const levelInfo: Record<string, { label: string; tone: "red" | "amber" | "slate"; box: string }> = {
  blocked: {
    label: "Blokkeert",
    tone: "red",
    box: "border-red-200 bg-red-50 text-red-900 dark:border-red-900 dark:bg-red-950 dark:text-red-200",
  },
  warning: {
    label: "Let op",
    tone: "amber",
    box: "border-amber-200 bg-amber-50 text-amber-900 dark:border-amber-900 dark:bg-amber-950 dark:text-amber-200",
  },
  info: {
    label: "Goed om te weten",
    tone: "slate",
    box: "border-slate-200 bg-slate-50 text-slate-800 dark:border-slate-800 dark:bg-slate-900 dark:text-slate-200",
  },
};

const levelOrder: Record<string, number> = { blocked: 0, warning: 1, info: 2 };

export function sortFindings(list: ComponentFinding[]) {
  return [...list].sort((a, b) => (levelOrder[a.level] ?? 3) - (levelOrder[b.level] ?? 3));
}

export function FindingItem({ f }: { f: ComponentFinding }) {
  const l = levelInfo[f.level] ?? levelInfo.info;
  return (
    <li className={cx("space-y-2 rounded border px-3 py-2", l.box)}>
      <p>
        <Badge tone={l.tone}>{l.label}</Badge> {f.message}
      </p>
      {f.command && <CopyBlock text={f.command} />}
    </li>
  );
}

// isPG zegt of het plan een database van PostgreSQL verhuist.
export function isPG(p: MigrationPlan) {
  return p.source.engine === "postgresql";
}

export function engineName(p: MigrationPlan) {
  return engineLabel[p.source.engine as DatabaseGroup["engine"]] ?? p.source.engine;
}

// De commando's van Nextcloud; voor een andere app zegt ClusterForge wat er
// moet, zonder commando.
function isNextcloud(p: MigrationPlan) {
  return /nextcloud/i.test(p.database) || /nextcloud/i.test(p.target?.cluster_name ?? "");
}

const occ = "sudo -u www-data php occ";

// prodSlug is de slug die Jonas intikt: die van het nieuwe cluster als dat
// prod is, anders die van het oude; leeg als geen van beide prod is.
function prodSlug(p: MigrationPlan, t: MigrationTarget | null) {
  if (t?.environment === "prod") return t.slug;
  if (p.source.environment === "prod") return p.source.slug;
  return "";
}

// MoveCard is de echte verhuizing: starten, overschakelen of annuleren, en
// daarna het slot op de oude database. loading: het plan voor een ander
// doel komt nog.
export function MoveCard({ p, loading, refresh }: { p: MigrationPlan; loading: boolean; refresh: () => void }) {
  const open = p.runs.find((r) => r.kind === "migrate" && r.stage === "copied" && r.status === "running");
  const done = p.runs.find((r) => r.kind === "migrate" && r.stage === "done");
  const moving = p.active?.kind === "component.migrate" ? p.active : null;
  // Is de laatste verhuizing klaar, dan is er niets meer te starten.
  const finished = done !== undefined && p.runs.find((r) => r.kind === "migrate") === done;
  return (
    <Card title="Verhuizen">
      <div className="space-y-5">
        {open ? (
          <Copied p={p} r={open} />
        ) : moving ? (
          <Alert kind="info">
            De verhuizing loopt: {moving.title}.{" "}
            <Link href={`/taken/detail?id=${moving.job_id}`} className="underline">
              Volg de taak
            </Link>
            . Laat de app in onderhoud tot de kopie klaar is.
          </Alert>
        ) : finished ? null : (
          <Start p={p} loading={loading} refresh={refresh} />
        )}
        {done && !open && <Moved p={p} r={done} />}
      </div>
    </Card>
  );
}

function lastTrial(p: MigrationPlan) {
  return p.runs.find((r) => r.kind === "trial" && r.status === "ok" && r.import_ms !== null);
}

function Start({ p, loading, refresh }: { p: MigrationPlan; loading: boolean; refresh: () => void }) {
  const isAdmin = useIsAdmin();
  const totp = useProdTotp();
  const start = useStartMove();
  const [force, setForce] = useState(false);
  const [confirm, setConfirm] = useState("");
  const t = p.target;
  const slug = prodSlug(p, t);
  const trial = lastTrial(p);
  const conns = p.connections;
  const total = conns.reduce((n, c) => n + c.count, 0);
  const pg = isPG(p);
  const users = movingUsers(p);
  const roles = movingRoles(p);
  const blocked = p.move_blocked;
  const ready =
    isAdmin &&
    t !== null &&
    !blocked &&
    p.active === null &&
    !loading &&
    !start.isPending &&
    (total === 0 || force) &&
    (slug === "" || prodConfirmed(slug, confirm, totp));
  return (
    <>
      <div className="space-y-2 text-sm text-slate-700 dark:text-slate-300">
        <p>
          De verhuizing zet {p.database} onder dezelfde naam in de {engineName(p)} van {t ? t.cluster_name : "het nieuwe cluster"}
          , met dezelfde {pg ? "rollen, eigenaar" : "gebruikers"} en wachtwoorden. Daarna telt ClusterForge de rijen van elke
          tabel aan beide kanten: alles moet gelijk zijn. Wijkt er iets af, dan stopt de taak, gaat de kopie weer weg en blijft de
          app op de oude database.
        </p>
        <p>
          De app staat zolang in onderhoud
          {trial ? (
            <>
              : volgens de proef van {when.format(new Date(trial.started_at))} ongeveer{" "}
              <strong>{seconds((trial.import_ms ?? 0) + (trial.count_ms ?? 0))}</strong>.
            </>
          ) : (
            <>. Doe eerst een proef, dan weet je hoe lang.</>
          )}
        </p>
      </div>

      <Step n={1} title="Zet de app in onderhoud">
        {isNextcloud(p) ? (
          <>
            <p>Op een node van Nextcloud:</p>
            <CopyBlock text={`${occ} maintenance:mode --on`} />
          </>
        ) : (
          <p>Zet de app in onderhoud of stop ze, zodat niemand nog in {p.database} schrijft, ook geen cronjob.</p>
        )}
      </Step>

      <Step n={2} title={`Wie er nu met ${p.database} op ${p.source.name} verbonden is`}>
        {total === 0 ? (
          <p className="text-green-800 dark:text-green-300">Niemand. De app is stil.</p>
        ) : (
          <>
            <p>
              Nog {plural(total, "verbinding", "verbindingen")}. Na het onderhoud zijn die meestal binnen een paar seconden weg.
            </p>
            <ul className="space-y-0.5 font-mono text-xs">
              {conns.map((c, i) => (
                <li key={i}>
                  {c.user} vanaf {c.hostname || c.address || "de server zelf"}
                  {c.hostname && c.address ? ` (${c.address})` : ""} op {c.server}: {c.count}
                </li>
              ))}
            </ul>
          </>
        )}
        <Button variant="secondary" onClick={refresh} disabled={loading}>
          Opnieuw kijken
        </Button>
      </Step>

      <Step n={3} title="Wie er meegaat">
        {pg ? (
          roles.length === 0 ? (
            <p>Geen rol met iets in {p.database}; maak de gebruiker van de app op het nieuwe cluster zelf.</p>
          ) : (
            <RolesTable rows={roles} />
          )
        ) : users.length === 0 ? (
          <p>Geen gebruiker met een wachtwoord en rechten op {p.database}; maak die op het nieuwe cluster zelf.</p>
        ) : (
          <AccountsTable
            rows={users.map((u) => ({ user: u.user, oldHosts: u.hosts, hosts: p.new_hosts, privileges: u.privileges }))}
            target={t?.cluster_name ?? "het nieuwe cluster"}
          />
        )}
        <p className="text-xs text-slate-500">
          {pg
            ? "ClusterForge neemt de SCRAM-verifier van elk wachtwoord mee van de oude server naar de nieuwe, zonder hem te bewaren. Het wachtwoord zelf kent het niet. Een rol die er al is, krijgt het wachtwoord van de oude server."
            : "ClusterForge neemt de hash van elk wachtwoord mee van de oude server naar de nieuwe, zonder hem te bewaren. Het wachtwoord zelf kent het niet."}
        </p>
      </Step>

      {p.move_findings.length > 0 && (
        <ul className="space-y-2 text-sm">
          {sortFindings(p.move_findings).map((f, i) => (
            <FindingItem key={i} f={f} />
          ))}
        </ul>
      )}

      {total > 0 && isAdmin && (
        <label className="flex items-start gap-2 text-sm">
          <input type="checkbox" className="mt-1" checked={force} onChange={(e) => setForce(e.target.checked)} />
          <span>
            Toch verhuizen, met de verbindingen erbij. Schrijft er nog iemand, dan kloppen de tellingen niet en stopt de taak
            zonder verlies.
          </span>
        </label>
      )}
      {slug !== "" && isAdmin && <ProdConfirm slug={slug} value={confirm} onChange={setConfirm} />}
      {start.isError && <Alert>{start.error.message}</Alert>}
      <div className="flex flex-wrap items-center gap-3">
        <Button
          disabled={!ready}
          onClick={() => {
            if (!t) return;
            start.mutate({
              source: p.source.key,
              database: p.database,
              component_id: t.component_id,
              force: total > 0 && force,
              confirm: slug !== "" ? confirm : undefined,
            });
          }}
        >
          {start.isPending ? "Bezig…" : "Verhuizing starten"}
        </Button>
        {!isAdmin ? (
          <span className="text-sm text-slate-500">Alleen een beheerder kan verhuizen.</span>
        ) : !t ? (
          <span className="text-sm text-slate-500">Kies eerst het cluster waar de database naartoe gaat.</span>
        ) : blocked ? (
          <span className="text-sm text-slate-500">Los eerst op wat de verhuizing blokkeert.</span>
        ) : p.active !== null ? (
          <span className="text-sm text-slate-500">Wacht tot de proef klaar is.</span>
        ) : total > 0 && !force ? (
          <span className="text-sm text-slate-500">Er is nog iemand verbonden.</span>
        ) : null}
      </div>
    </>
  );
}

// movingUsers zijn de gebruikers die meegaan, met hun hosts op de oude
// server; zoals de server ze kiest: een gewoon wachtwoord en rechten op de
// database.
function movingUsers(p: MigrationPlan) {
  const out: { user: string; hosts: string[]; privileges: string[] }[] = [];
  for (const a of p.accounts) {
    if (!a.moves) continue;
    let u = out.find((x) => x.user === a.user);
    if (!u) {
      u = { user: a.user, hosts: [], privileges: [] };
      out.push(u);
    }
    u.hosts.push(a.host);
    for (const r of a.privileges) if (!u.privileges.includes(r)) u.privileges.push(r);
  }
  return out;
}

// movingRoles zijn de rollen van PostgreSQL die meegaan.
function movingRoles(p: MigrationPlan): RoleRow[] {
  return p.accounts
    .filter((a) => a.moves)
    .map((a) => ({ user: a.user, plugin: a.plugin, memberOf: a.member_of, privileges: a.privileges, isNew: null }));
}

type RoleRow = { user: string; plugin: string; memberOf: string[]; privileges: string[]; isNew: boolean | null };

const loginText: Record<string, string> = {
  "scram-sha-256": "met wachtwoord",
  nologin: "zonder inloggen",
  none: "zonder wachtwoord",
};

const pgRightText: Record<string, string> = {
  OWNER: "eigenaar",
  OBJECTS: "objecten",
  CONNECT: "verbinden",
  CREATE: "schema's maken",
  TEMPORARY: "tijdelijke tabellen",
};

function RolesTable({ rows }: { rows: RoleRow[] }) {
  const showNew = rows.some((r) => r.isNew !== null);
  return (
    <div className="overflow-x-auto">
      <table className={tableClass}>
        <thead className="border-b border-slate-200 dark:border-slate-800">
          <tr>
            <th className={thClass}>Rol</th>
            <th className={thClass}>Inloggen</th>
            <th className={thClass}>Lid van</th>
            <th className={thClass}>In de database</th>
            {showNew && <th className={thClass}>Op het nieuwe cluster</th>}
          </tr>
        </thead>
        <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
          {rows.map((r) => (
            <tr key={r.user}>
              <td className={`${tdClass} font-mono`}>{r.user}</td>
              <td className={tdClass}>{loginText[r.plugin] ?? r.plugin}</td>
              <td className={`${tdClass} font-mono text-xs`}>{r.memberOf.join(", ") || "–"}</td>
              <td className={tdClass}>
                {r.privileges.length === 0 ? "via een rol" : r.privileges.map((x) => pgRightText[x] ?? x).join(", ")}
              </td>
              {showNew && <td className={tdClass}>{r.isNew ? "nieuw" : "bestond al"}</td>}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

const allRights = ["SELECT", "INSERT", "UPDATE", "DELETE", "CREATE", "DROP", "INDEX", "ALTER"];

function rightsText(rights: string[]) {
  return allRights.every((r) => rights.includes(r)) ? "alle rechten" : rights.join(", ");
}

function AccountsTable({
  rows,
  target,
}: {
  rows: { user: string; oldHosts: string[]; hosts: string[]; privileges: string[] }[];
  target: string;
}) {
  return (
    <div className="overflow-x-auto">
      <table className={tableClass}>
        <thead className="border-b border-slate-200 dark:border-slate-800">
          <tr>
            <th className={thClass}>Gebruiker</th>
            <th className={thClass}>Op de oude server vanaf</th>
            <th className={thClass}>Op {target} vanaf</th>
            <th className={thClass}>Rechten</th>
          </tr>
        </thead>
        <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
          {rows.map((r) => (
            <tr key={r.user}>
              <td className={`${tdClass} font-mono`}>{r.user}</td>
              <td className={`${tdClass} font-mono text-xs`}>{r.oldHosts.join(", ")}</td>
              <td className={`${tdClass} min-w-48 font-mono text-xs`}>{r.hosts.join(", ")}</td>
              <td className={tdClass}>{rightsText(r.privileges)}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function Step({ n, title, children }: { n: number; title: string; children: ReactNode }) {
  return (
    <section className="flex gap-3">
      <span className="mt-0.5 flex size-6 shrink-0 items-center justify-center rounded-full bg-brand-600 text-xs font-semibold text-white">
        {n}
      </span>
      <div className="min-w-0 flex-1 space-y-2 text-sm text-slate-700 dark:text-slate-300">
        <h3 className="font-semibold text-slate-900 dark:text-slate-100">{title}</h3>
        {children}
      </div>
    </section>
  );
}

const when = new Intl.DateTimeFormat("nl-BE", { day: "numeric", month: "numeric", hour: "2-digit", minute: "2-digit" });

// Copied: de kopie staat klaar; Jonas wijst de app naar de nieuwe database
// en schakelt over, of annuleert.
function Copied({ p, r }: { p: MigrationPlan; r: MigrationRun }) {
  const isAdmin = useIsAdmin();
  const sw = useSwitchMove();
  const cancel = useCancelMove();
  const [typed, setTyped] = useState("");
  const addr = r.target_address;
  const swErr = sw.error instanceof ApiError && sw.error.status === 409 ? sw.error : null;
  const users = r.accounts.map((a) => a.user);
  const pg = isPG(p);
  return (
    <>
      <Alert kind="success">
        De kopie staat klaar op {r.cluster_name}
        {r.target_node ? ` (${r.target_node})` : ""}: {plural(r.tables, "tabel", "tabellen")} en {nl(r.rows_target)} rijen, aan
        beide kanten gelijk, in {seconds(r.import_ms)}. De app gebruikt nog de oude database.
      </Alert>
      <Step n={1} title="Wijs de app naar de nieuwe database">
        <p>
          Het adres wordt <span className="font-mono">{addr ? `${addr}:${pg ? 5432 : 3306}` : r.cluster_name}</span>. De gebruiker
          en het wachtwoord blijven hetzelfde.
        </p>
        {isNextcloud(p) && addr && (
          <>
            <p>Op een node van Nextcloud, en op elke node als ze elk een eigen config.php hebben:</p>
            <CopyBlock text={`${occ} config:system:set dbhost --value=${addr}`} />
          </>
        )}
      </Step>
      <Step n={2} title="Zet het onderhoud uit">
        {isNextcloud(p) ? <CopyBlock text={`${occ} maintenance:mode --off`} /> : <p>Start de app weer.</p>}
      </Step>
      <Step n={3} title="Schakel over">
        <p>
          ClusterForge kijkt of niemand nog met {r.database} op {r.source_name} verbonden is en de app wel met de nieuwe. Daarna
          {pg
            ? " kan wie met de oude database verbindt, daar alleen nog lezen, zodat een vergeten cronjob niet stilletjes verder schrijft."
            : users.length > 0
              ? ` ${users.length === 1 ? "mag" : "mogen"} ${users.join(" en ")} de oude database alleen nog lezen, zodat een vergeten cronjob daar niet stilletjes verder schrijft.`
              : " is de verhuizing klaar."}
        </p>
        {r.accounts.length > 0 &&
          (pg ? (
            <RolesTable
              rows={r.accounts.map((a) => {
                const old = p.accounts.find((x) => x.user === a.user);
                return {
                  user: a.user,
                  plugin: old?.plugin ?? "",
                  memberOf: old?.member_of ?? [],
                  privileges: a.privileges,
                  isNew: a.new,
                };
              })}
            />
          ) : (
            <AccountsTable
              rows={r.accounts.map((a) => ({ user: a.user, oldHosts: a.old_hosts, hosts: a.hosts, privileges: a.privileges }))}
              target={r.cluster_name}
            />
          ))}
        {sw.isError && (
          <Alert>
            {sw.error.message}
            {swErr && isAdmin && (
              <div className="mt-2">
                <Button variant="secondary-danger" onClick={() => sw.mutate({ id: r.id, force: true })} disabled={sw.isPending}>
                  Toch overschakelen
                </Button>
              </div>
            )}
          </Alert>
        )}
        <div className="flex flex-wrap items-center gap-3">
          <Button disabled={!isAdmin || sw.isPending} onClick={() => sw.mutate({ id: r.id, force: false })}>
            {sw.isPending ? "Bezig…" : "De app draait op de nieuwe database"}
          </Button>
          {!isAdmin && <span className="text-sm text-slate-500">Alleen een beheerder kan overschakelen.</span>}
        </div>
      </Step>
      <details className="rounded border border-slate-200 px-3 py-2 text-sm dark:border-slate-800">
        <summary className="cursor-pointer font-medium">Annuleren</summary>
        <div className="mt-2 space-y-2 text-slate-700 dark:text-slate-300">
          <p>
            Haalt {r.database} en de {pg ? "rollen die ClusterForge nieuw maakte" : "nieuwe gebruikers"} weg van {r.cluster_name};
            de app blijft op de oude database. Dat kan alleen zolang niemand de nieuwe database gebruikt. Tik de naam van de
            database om te bevestigen.
          </p>
          <div className="flex flex-wrap items-center gap-2">
            <Input
              aria-label="Naam van de database"
              className="max-w-56 font-mono"
              autoComplete="off"
              spellCheck={false}
              value={typed}
              onChange={(e) => setTyped(e.target.value.trim())}
              disabled={!isAdmin}
            />
            <Button
              variant="danger"
              disabled={!isAdmin || typed !== r.database || cancel.isPending}
              onClick={() => cancel.mutate({ id: r.id, confirm: typed })}
            >
              {cancel.isPending ? "Bezig…" : "Kopie weghalen"}
            </Button>
          </div>
          {cancel.isError && <Alert>{cancel.error.message}</Alert>}
        </div>
      </details>
    </>
  );
}

// Moved: na de verhuizing, met het slot op de oude database.
function Moved({ p, r }: { p: MigrationPlan; r: MigrationRun }) {
  const isAdmin = useIsAdmin();
  const lock = useLockMove();
  const users = r.accounts.map((a) => a.user);
  const pg = isPG(p);
  // Bij PostgreSQL staat het slot op de database zelf, ook zonder rollen.
  const lockable = pg || users.length > 0;
  return (
    <section className="space-y-2 rounded border border-slate-200 p-3 text-sm text-slate-700 dark:border-slate-800 dark:text-slate-300">
      <h3 className="flex flex-wrap items-center gap-2 font-semibold text-slate-900 dark:text-slate-100">
        Verhuisd naar {r.cluster_name}
        {r.switched_at && <span className="font-normal text-slate-500">op {when.format(new Date(r.switched_at))}</span>}
        {lockable &&
          (r.locked ? <Badge tone="green">oude database op slot</Badge> : <Badge tone="amber">oude database open</Badge>)}
      </h3>
      <p>
        De app gebruikt {r.database} op <span className="font-mono">{r.target_address || r.cluster_name}</span>.{" "}
        {pg
          ? r.locked
            ? `Op ${r.source_name} kan wie met ${r.database} verbindt, alleen nog lezen.`
            : `Op ${r.source_name} kan iedereen weer in ${r.database} schrijven.`
          : users.length === 0
            ? "Er ging geen gebruiker mee, dus er is geen slot."
            : r.locked
              ? `Op ${r.source_name} ${users.length === 1 ? "mag" : "mogen"} ${users.join(" en ")} ${r.database} alleen nog lezen.`
              : `Op ${r.source_name} ${users.length === 1 ? "kan" : "kunnen"} ${users.join(" en ")} weer in ${r.database} schrijven.`}
      </p>
      <p className="text-slate-500">
        Terug kan zolang de oude database bestaat: haal het slot eraf en wijs de app weer naar {p.source.address || p.source.name}
        . Wat de app sinds het overschakelen schreef, staat dan alleen in de nieuwe database.
      </p>
      {lockable && (
        <Button
          variant="secondary"
          disabled={!isAdmin || lock.isPending}
          onClick={() => lock.mutate({ id: r.id, locked: !r.locked })}
        >
          {lock.isPending ? "Bezig…" : r.locked ? "Slot eraf halen" : "Weer op slot zetten"}
        </Button>
      )}
      {lock.isError && <Alert>{lock.error.message}</Alert>}
    </section>
  );
}
