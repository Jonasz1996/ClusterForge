"use client";

import Link from "next/link";
import { usePathname, useRouter, useSearchParams } from "next/navigation";
import { Suspense, type ReactNode } from "react";
import { CopyBlock } from "@/components/inventory/InstallAgent";
import { ago, Empty, formatBytes, QueryState, tableClass, tdClass, thClass } from "@/components/inventory/bits";
import { Alert, Badge, Button, Card, cx, Field, PageHeader, Select } from "@/components/ui";
import type { ComponentFinding } from "@/lib/components";
import {
  engineLabel,
  kindLabel,
  useMigrationPlan,
  useStartTrial,
  type DatabaseGroup,
  type MigrationPlan,
  type MigrationRun,
  type MigrationTarget,
} from "@/lib/databases";
import { useIsAdmin } from "@/lib/inventory";

export default function MigratePage() {
  return (
    <Suspense>
      <MigrateInner />
    </Suspense>
  );
}

function MigrateInner() {
  const params = useSearchParams();
  const source = params.get("source") ?? "";
  const db = params.get("db") ?? "";
  const component = params.get("component") ?? "";
  const plan = useMigrationPlan(source, db, component);
  const p = plan.data;
  if (source === "" || db === "") return <Alert>Geen database opgegeven; kies er een bij Databases.</Alert>;
  return (
    <div className="space-y-6">
      <PageHeader
        title={`${db} verhuizen`}
        description={
          <>
            Van {p ? p.source.name : "de oude server"} naar de MariaDB op de nodes van een app. ClusterForge toont eerst wat er
            mee moet en wat in de weg staat; een proefverhuizing importeert een kopie terwijl de app gewoon doorwerkt. Terug naar{" "}
            <Link href="/databases" className="underline">
              Databases
            </Link>
            .
          </>
        }
      />
      <QueryState q={plan}>
        {p && (
          <>
            <SourceCard p={p} />
            <TargetCard p={p} />
            <FindingsCard p={p} />
            <TrialCard p={p} loading={plan.isPlaceholderData} />
            <Runs runs={p.runs} />
          </>
        )}
      </QueryState>
    </div>
  );
}

const nl = (n: number) => n.toLocaleString("nl-BE");

function plural(n: number, one: string, many: string) {
  return `${nl(n)} ${n === 1 ? one : many}`;
}

function SourceCard({ p }: { p: MigrationPlan }) {
  const s = p.source;
  const i = p.info;
  const objects = i
    ? [
        plural(i.views, "view", "views"),
        plural(i.routines, "routine", "routines"),
        plural(i.triggers, "trigger", "triggers"),
        plural(i.events, "event", "events"),
      ].join(", ")
    : "";
  return (
    <Card
      title={
        <span className="flex flex-wrap items-center gap-2">
          <span>
            Van{" "}
            {s.cluster_id ? (
              <Link href={`/clusters/detail?id=${s.cluster_id}`} className="text-brand-700 hover:underline dark:text-sky-300">
                {s.name}
              </Link>
            ) : (
              s.name
            )}
          </span>
          <Badge>{engineLabel[s.engine as DatabaseGroup["engine"]] ?? s.engine}</Badge>
          <Badge>{kindLabel[s.kind as DatabaseGroup["kind"]] ?? s.kind}</Badge>
        </span>
      }
    >
      <p className="mb-4 text-sm text-slate-700 dark:text-slate-300">{s.summary}</p>
      <dl className="grid gap-x-6 gap-y-3 text-sm sm:grid-cols-2 lg:grid-cols-3">
        <Item label="Database">
          <span className="font-mono">{p.database}</span>
        </Item>
        {i && (
          <>
            <Item label="Grootte">{formatBytes(i.size_bytes)} aan data en indexen</Item>
            <Item label="Tabellen">
              {plural(i.tables, "tabel", "tabellen")}, ongeveer {plural(i.rows, "rij", "rijen")}
            </Item>
            <Item label="Andere objecten">{objects}</Item>
            <Item label="Tekenset">
              {i.charset} <span className="text-slate-500">({i.collation})</span>
            </Item>
            <Item label="Versie">{i.version}</Item>
          </>
        )}
        <Item label="Servers">{s.servers.join(", ") || "–"}</Item>
        {s.node && (
          <Item label="Gelezen op">
            {s.node}
            {s.address && (
              <span className="text-slate-500">
                {" "}
                · bereikbaar op <span className="font-mono">{`${s.address}:${s.port}`}</span>
                {s.via === "vip" && " (het VIP)"}
              </span>
            )}
          </Item>
        )}
      </dl>
      <h3 className="mt-6 mb-2 text-sm font-semibold">Gebruikt door, de laatste 30 dagen</h3>
      {p.clients.length === 0 ? (
        <p className="text-sm text-slate-500">Niemand, volgens wat ClusterForge zag.</p>
      ) : (
        <ul className="space-y-1 text-sm">
          {p.clients.map((u) => (
            <li key={u.key}>
              {u.kind === "cluster" ? (
                <Link
                  href={`/clusters/detail?id=${u.cluster_id}`}
                  className="font-medium text-brand-700 hover:underline dark:text-sky-300"
                >
                  {u.name}
                </Link>
              ) : u.kind === "node" ? (
                <Link
                  href={`/nodes/detail?id=${u.node_id}`}
                  className="font-medium text-brand-700 hover:underline dark:text-sky-300"
                >
                  {u.name}
                </Link>
              ) : u.kind === "local" ? (
                <span>op de databasenode zelf</span>
              ) : (
                <span>
                  onbekend adres <span className="font-mono text-xs">{u.name}</span>
                </span>
              )}{" "}
              <span className="text-xs text-slate-500">
                {u.nodes.length > 0 && `${u.nodes.join(", ")} · `}als {u.db_users.join(", ")} ·{" "}
                {u.connections > 0 ? `${u.connections} verbonden` : `laatst ${ago(u.last_seen)}`}
              </span>
            </li>
          ))}
        </ul>
      )}
    </Card>
  );
}

function Item({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div>
      <dt className="text-xs font-medium uppercase tracking-wide text-slate-500">{label}</dt>
      <dd className="mt-0.5">{children}</dd>
    </div>
  );
}

function TargetCard({ p }: { p: MigrationPlan }) {
  const router = useRouter();
  const pathname = usePathname();
  const params = useSearchParams();
  const choose = (id: string) => {
    const next = new URLSearchParams(params.toString());
    if (id === "") next.delete("component");
    else next.set("component", id);
    router.replace(`${pathname}?${next.toString()}`);
  };
  if (p.targets.length === 0) {
    return (
      <Card title="Naar">
        <Empty>
          Nog geen cluster met een MariaDB van ClusterForge. Voeg er een toe met Database toevoegen bij het cluster van de app, of
          rol een nieuw cluster uit met Database erbij. Daarna kies je het hier.
        </Empty>
      </Card>
    );
  }
  const t = p.target;
  return (
    <Card title="Naar">
      <div className="max-w-md">
        <Field label="Cluster" htmlFor="target" hint="De clusters met een MariaDB van ClusterForge op hun eigen nodes.">
          <Select id="target" value={t?.component_id ?? ""} onChange={(e) => choose(e.target.value)}>
            {!t && <option value="">Kies het cluster van de app</option>}
            {p.targets.map((o) => (
              <option key={o.component_id} value={o.component_id}>
                {o.cluster_name}
                {o.suggested ? " (gebruikt de database nu)" : ""}
              </option>
            ))}
          </Select>
        </Field>
      </div>
      {t && <TargetDetails t={t} p={p} />}
    </Card>
  );
}

function TargetDetails({ t, p }: { t: MigrationTarget; p: MigrationPlan }) {
  const r = p.reach;
  return (
    <dl className="mt-4 grid gap-x-6 gap-y-3 text-sm sm:grid-cols-2 lg:grid-cols-3">
      <Item label="Importeert op">
        <Link href={`/clusters/detail?id=${t.cluster_id}`} className="text-brand-700 hover:underline dark:text-sky-300">
          {t.cluster_name}
        </Link>
        , node {t.node}
      </Item>
      <Item label="Versie">{t.version || "–"}</Item>
      <Item label="Adres voor de app">
        <span className="font-mono">{t.address ? `${t.address}:${t.port}` : "–"}</span>
      </Item>
      <Item label="Buffer pool">
        {formatBytes(t.buffer_pool_bytes)}
        <span className="text-slate-500">, al {formatBytes(t.data_bytes)} aan databases</span>
      </Item>
      <Item label="Vrije schijf">
        {t.disks.length === 0
          ? "–"
          : t.disks.map((d) => (
              <div key={d.hostname}>
                {d.hostname}: {formatBytes(d.free_bytes)} <span className="text-slate-500">op {d.mount}</span>
              </div>
            ))}
      </Item>
      <Item label="Oude server bereikt">
        {!r ? (
          "–"
        ) : r.reached ? (
          <span className="flex flex-wrap items-center gap-1">
            <Badge tone="green">ja</Badge>
            {r.tls ? <Badge tone="green">TLS</Badge> : <Badge>zonder TLS</Badge>}
          </span>
        ) : (
          <Badge tone="red">nee</Badge>
        )}
      </Item>
      <Item label="Tijdelijke gebruiker mag van">
        <span className="font-mono text-xs">{t.hosts.join(", ") || "–"}</span>
      </Item>
    </dl>
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

function FindingsCard({ p }: { p: MigrationPlan }) {
  const list = [...p.findings].sort((a, b) => (levelOrder[a.level] ?? 3) - (levelOrder[b.level] ?? 3));
  return (
    <Card title="Controle">
      {list.length === 0 ? (
        <p className="text-sm text-slate-600 dark:text-slate-400">Niets dat in de weg staat.</p>
      ) : (
        <ul className="space-y-2 text-sm">
          {list.map((f, i) => (
            <FindingItem key={i} f={f} />
          ))}
        </ul>
      )}
    </Card>
  );
}

function FindingItem({ f }: { f: ComponentFinding }) {
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

// loading: het plan voor een ander doel komt nog; tot dan start er niets.
function TrialCard({ p, loading }: { p: MigrationPlan; loading: boolean }) {
  const isAdmin = useIsAdmin();
  const start = useStartTrial();
  const t = p.target;
  const proef = proefName(p.database);
  return (
    <Card title="Proefverhuizing">
      <div className="space-y-3 text-sm text-slate-700 dark:text-slate-300">
        <p>
          De proef haalt {p.database} op en importeert hem als <span className="font-mono">{proef}</span>
          {t ? ` op ${t.node}` : ""}, terwijl de app op de oude server doorwerkt. Daarna telt ClusterForge de rijen en objecten
          aan beide kanten, meet hoe lang het duurde en haalt de kopie weer weg.
        </p>
        <p>
          Op de oude server maakt de proef een tijdelijke gebruiker die alleen mag lezen, met een wachtwoord dat nergens bewaard
          wordt; na de proef is die weer weg. Kleine verschillen in rijen zijn normaal, want de app schrijft intussen verder.
        </p>
      </div>
      {p.active && (
        <div className="mt-4">
          <Alert kind="info">
            Er loopt nu een proef: {p.active.title}.{" "}
            <Link href={`/taken/detail?id=${p.active.job_id}`} className="underline">
              Volg de taak
            </Link>
            ; het verslag verschijnt hieronder als ze klaar is.
          </Alert>
        </div>
      )}
      {start.isError && (
        <div className="mt-4">
          <Alert>{start.error.message}</Alert>
        </div>
      )}
      <div className="mt-4 flex flex-wrap items-center gap-3">
        <Button
          disabled={!isAdmin || !t || p.blocked || p.active !== null || start.isPending || loading}
          onClick={() => {
            if (t) start.mutate({ source: p.source.key, database: p.database, component_id: t.component_id });
          }}
        >
          {start.isPending ? "Bezig…" : "Proefverhuizing starten"}
        </Button>
        {!isAdmin ? (
          <span className="text-sm text-slate-500">Alleen een beheerder kan een proef starten.</span>
        ) : !t ? (
          <span className="text-sm text-slate-500">Kies eerst het cluster waar de database naartoe gaat.</span>
        ) : p.blocked ? (
          <span className="text-sm text-slate-500">Los eerst op wat hierboven blokkeert.</span>
        ) : null}
      </div>
    </Card>
  );
}

// proefName volgt TrialName op de server: de naam met _proef, hoogstens
// 63 tekens.
function proefName(db: string) {
  const suffix = "_proef";
  return db.slice(0, 63 - suffix.length) + suffix;
}

function seconds(ms: number | null) {
  if (ms === null) return "–";
  const s = ms / 1000;
  if (s < 60) return `${s.toLocaleString("nl-BE", { maximumFractionDigits: 1 })} s`;
  const m = Math.floor(s / 60);
  return `${m} min ${Math.round(s - m * 60)} s`;
}

const statusInfo: Record<MigrationRun["status"], { label: string; tone: "blue" | "green" | "red" }> = {
  running: { label: "Loopt", tone: "blue" },
  ok: { label: "Gelukt", tone: "green" },
  failed: { label: "Mislukt", tone: "red" },
};

const objectLabel: Record<string, string> = { views: "views", routines: "routines", triggers: "triggers", events: "events" };

const when = new Intl.DateTimeFormat("nl-BE", { day: "numeric", month: "numeric", hour: "2-digit", minute: "2-digit" });

function Runs({ runs }: { runs: MigrationRun[] }) {
  return (
    <Card title="Proeven">
      {runs.length === 0 ? (
        <p className="text-sm text-slate-500">Nog geen proef van deze database.</p>
      ) : (
        <div className="overflow-x-auto">
          <table className={tableClass}>
            <thead className="border-b border-slate-200 dark:border-slate-800">
              <tr>
                <th className={thClass}>Gestart</th>
                <th className={thClass}>Naar</th>
                <th className={thClass}>Uitkomst</th>
                <th className={`${thClass} text-right`}>Importeren</th>
                <th className={`${thClass} text-right`}>Tellen</th>
                <th className={`${thClass} text-right`}>Over het netwerk</th>
                <th className={thClass}>Rijen en objecten</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
              {runs.map((r) => (
                <RunRow key={r.id} r={r} />
              ))}
            </tbody>
          </table>
        </div>
      )}
    </Card>
  );
}

function RunRow({ r }: { r: MigrationRun }) {
  const st = statusInfo[r.status];
  const objects = r.objects.filter((o) => o.source > 0 || o.target > 0);
  return (
    <tr>
      <td className={`${tdClass} whitespace-nowrap`}>
        {when.format(new Date(r.started_at))}
        {r.job_id && (
          <div>
            <Link href={`/taken/detail?id=${r.job_id}`} className="text-xs text-brand-700 hover:underline dark:text-sky-300">
              taak
            </Link>
          </div>
        )}
      </td>
      <td className={tdClass}>
        <Link href={`/clusters/detail?id=${r.cluster_id}`} className="text-brand-700 hover:underline dark:text-sky-300">
          {r.cluster_name}
        </Link>
        {r.target_node && <div className="text-xs text-slate-500">op {r.target_node}</div>}
        {r.source_node && (
          <div className="text-xs text-slate-500">
            van {r.source_node}
            {r.source_address && ` (${r.source_address})`}
          </div>
        )}
      </td>
      <td className={`${tdClass} min-w-48`}>
        <Badge tone={st.tone}>{st.label}</Badge>
        {/* Bij een gelukte proef staat in het bericht wat de kolommen ernaast al tonen. */}
        {r.status !== "ok" && r.message && <p className="mt-1 text-sm whitespace-pre-line">{r.message}</p>}
      </td>
      <td className={`${tdClass} text-right tabular-nums`}>{seconds(r.import_ms)}</td>
      <td className={`${tdClass} text-right tabular-nums`}>{seconds(r.count_ms)}</td>
      <td className={`${tdClass} text-right tabular-nums`}>{r.dump_bytes > 0 ? formatBytes(r.dump_bytes) : "–"}</td>
      <td className={`${tdClass} min-w-56`}>
        {r.tables > 0 ? (
          <>
            {plural(r.tables, "tabel", "tabellen")}, {nl(r.rows_source)} rijen oud en {nl(r.rows_target)} nieuw
            {r.differences.length > 0 && (
              <details className="mt-1">
                <summary className="cursor-pointer text-xs text-slate-500">
                  {plural(r.differences.length, "tabel", "tabellen")} met een ander aantal, want de app werkte door
                </summary>
                <ul className="mt-1 font-mono text-xs">
                  {r.differences.map((d) => (
                    <li key={d.name}>
                      {d.name}: {nl(d.source)} → {nl(d.target)}
                    </li>
                  ))}
                </ul>
              </details>
            )}
            {objects.length > 0 && (
              <div className="mt-1 text-xs text-slate-500">
                {objects
                  .map(
                    (o) => `${objectLabel[o.kind] ?? o.kind} ${o.source === o.target ? o.source : `${o.source} → ${o.target}`}`,
                  )
                  .join(", ")}
              </div>
            )}
          </>
        ) : (
          "–"
        )}
      </td>
    </tr>
  );
}
