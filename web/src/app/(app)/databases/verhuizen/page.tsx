"use client";

import Link from "next/link";
import { usePathname, useRouter, useSearchParams } from "next/navigation";
import { Suspense } from "react";
import { engineName, FindingItem, isPG, Item, MoveCard, nl, plural, seconds, sortFindings } from "@/components/databases/Move";
import { ago, Empty, formatBytes, QueryState, tableClass, tdClass, thClass } from "@/components/inventory/bits";
import { Alert, Badge, Button, Card, Field, PageHeader, Select } from "@/components/ui";
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
            Van {p ? p.source.name : "de oude server"} naar de {p ? engineName(p) : "database"} op de nodes van een app.
            ClusterForge toont eerst wat er mee moet en wat in de weg staat; een proefverhuizing importeert een kopie terwijl de
            app gewoon doorwerkt, en de echte verhuizing gebeurt met de app even in onderhoud. Terug naar{" "}
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
            <MoveCard p={p} loading={plan.isPlaceholderData} refresh={() => void plan.refetch()} />
            <Runs runs={p.runs} pg={isPG(p)} />
          </>
        )}
      </QueryState>
    </div>
  );
}

function SourceCard({ p }: { p: MigrationPlan }) {
  const s = p.source;
  const i = p.info;
  const pg = isPG(p);
  const objects = i
    ? [
        plural(i.views, "view", "views"),
        // Een routine heet in PostgreSQL een functie.
        pg ? plural(i.routines, "functie", "functies") : plural(i.routines, "routine", "routines"),
        plural(i.triggers, "trigger", "triggers"),
        // PostgreSQL kent geen events.
        ...(pg ? [] : [plural(i.events, "event", "events")]),
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
            <Item label="Grootte">
              {formatBytes(i.size_bytes)} {pg ? "voor de hele database" : "aan data en indexen"}
            </Item>
            <Item label="Tabellen">
              {plural(i.tables, "tabel", "tabellen")}, ongeveer {plural(i.rows, "rij", "rijen")}
            </Item>
            <Item label="Andere objecten">{objects}</Item>
            {pg ? (
              <>
                <Item label="Eigenaar">
                  <span className="font-mono">{i.owner || "–"}</span>
                </Item>
                <Item label="Encoding">{i.charset}</Item>
                <Item label="Extensies">
                  <span className="font-mono text-xs">{i.extensions.join(", ") || "geen"}</span>
                </Item>
              </>
            ) : (
              <Item label="Tekenset">
                {i.charset} <span className="text-slate-500">({i.collation})</span>
              </Item>
            )}
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
          Nog geen cluster met een {engineName(p)} van ClusterForge. Voeg er een toe met Database toevoegen bij het cluster van de
          app, of rol een nieuw cluster uit met Database erbij. Daarna kies je het hier.
        </Empty>
      </Card>
    );
  }
  const t = p.target;
  return (
    <Card title="Naar">
      <div className="max-w-md">
        <Field
          label="Cluster"
          htmlFor="target"
          hint={`De clusters met een ${engineName(p)} van ClusterForge op hun eigen nodes.`}
        >
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
  const pg = isPG(p);
  return (
    <dl className="mt-4 grid gap-x-6 gap-y-3 text-sm sm:grid-cols-2 lg:grid-cols-3">
      <Item label={pg ? "Importeert op de primary" : "Importeert op"}>
        <Link href={`/clusters/detail?id=${t.cluster_id}`} className="text-brand-700 hover:underline dark:text-sky-300">
          {t.cluster_name}
        </Link>
        , node {t.node}
      </Item>
      <Item label="Versie">{t.version || "–"}</Item>
      <Item label="Adres voor de app">
        <span className="font-mono">{t.address ? `${t.address}:${t.port}` : "–"}</span>
      </Item>
      {pg ? (
        <Item label="Al op het cluster">{formatBytes(t.data_bytes)} aan databases</Item>
      ) : (
        <Item label="Buffer pool">
          {formatBytes(t.buffer_pool_bytes)}
          <span className="text-slate-500">, al {formatBytes(t.data_bytes)} aan databases</span>
        </Item>
      )}
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
      {pg ? (
        <Item label="Tijdelijke gebruiker">
          lid van <span className="font-mono">{p.info?.owner || "de eigenaar"}</span>, alleen lezen
          {r?.seen && (
            <div className="text-xs text-slate-500">
              de oude server ziet {t.node} als <span className="font-mono">{r.seen}</span>
            </div>
          )}
        </Item>
      ) : (
        <Item label="Tijdelijke gebruiker mag van">
          <span className="font-mono text-xs">{t.hosts.join(", ") || "–"}</span>
        </Item>
      )}
    </dl>
  );
}

function FindingsCard({ p }: { p: MigrationPlan }) {
  const list = sortFindings(p.findings);
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
        {isPG(p) && (
          <p>
            De kopie van de proef heeft geen eigenaars en rechten: de rollen van de app staan nog niet op het nieuwe cluster. De
            echte verhuizing neemt ze mee.
          </p>
        )}
      </div>
      {p.active?.kind === "component.migrate_trial" && (
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

const statusInfo: Record<MigrationRun["status"], { label: string; tone: "blue" | "green" | "red" }> = {
  running: { label: "Loopt", tone: "blue" },
  ok: { label: "Gelukt", tone: "green" },
  failed: { label: "Mislukt", tone: "red" },
};

// Een verhuizing toont waar ze is; een proef alleen haar status.
const stageInfo: Record<string, { label: string; tone: "blue" | "green" | "red" | "amber" | "slate" }> = {
  copy: { label: "Kopieert", tone: "blue" },
  copied: { label: "Wacht op overschakelen", tone: "amber" },
  done: { label: "Verhuisd", tone: "green" },
  cancelled: { label: "Geannuleerd", tone: "slate" },
};

function runState(r: MigrationRun) {
  if (r.kind === "migrate" && r.status !== "failed") return stageInfo[r.stage] ?? statusInfo[r.status];
  if (r.kind === "migrate" && r.stage === "cancelled") return stageInfo.cancelled;
  return statusInfo[r.status];
}

const objectLabel: Record<string, string> = { views: "views", routines: "routines", triggers: "triggers", events: "events" };
const objectName = (kind: string, pg: boolean) => (pg && kind === "routines" ? "functies" : (objectLabel[kind] ?? kind));

const when = new Intl.DateTimeFormat("nl-BE", { day: "numeric", month: "numeric", hour: "2-digit", minute: "2-digit" });

function Runs({ runs, pg }: { runs: MigrationRun[]; pg: boolean }) {
  return (
    <Card title="Proeven en verhuizingen">
      {runs.length === 0 ? (
        <p className="text-sm text-slate-500">Nog geen proef of verhuizing van deze database.</p>
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
                <RunRow key={r.id} r={r} pg={pg} />
              ))}
            </tbody>
          </table>
        </div>
      )}
    </Card>
  );
}

function RunRow({ r, pg }: { r: MigrationRun; pg: boolean }) {
  const st = runState(r);
  const trial = r.kind === "trial";
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
        <span className="flex flex-wrap gap-1">
          <Badge>{trial ? "Proef" : "Verhuizing"}</Badge>
          <Badge tone={st.tone}>{st.label}</Badge>
        </span>
        {/* Bij een gelukte proef of kopie staat in het bericht wat de kolommen ernaast al tonen. */}
        {r.status === "failed" && r.message && <p className="mt-1 text-sm whitespace-pre-line">{r.message}</p>}
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
                  {plural(r.differences.length, "tabel", "tabellen")} met een ander aantal
                  {trial ? ", want de app werkte door" : ""}
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
                  .map((o) => `${objectName(o.kind, pg)} ${o.source === o.target ? o.source : `${o.source} → ${o.target}`}`)
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
