"use client";

import Link from "next/link";
import { Suspense } from "react";
import { Empty, formatBytes, QueryState, tableClass, tdClass, thClass } from "@/components/inventory/bits";
import { PbsGate, when } from "@/components/pbs/bits";
import { PbsTaskTable } from "@/components/pbs/Tasks";
import { Meter, uptime } from "@/components/pve/bits";
import { Tile as Win, TileGrid } from "@/components/Tiles";
import { Alert, Badge, Card, cx, PageHeader } from "@/components/ui";
import { jobKindLabel, pct, usePbsOverview, type PbsDatastoreUsage, type PbsOverview, type PbsServer } from "@/lib/pbs";

export default function PbsPage() {
  return (
    <div className="space-y-6">
      <PageHeader
        title="Backup Server"
        description="Je Proxmox Backup Server in één blik: datastores en hoe snel ze vollopen, de jobs met hun laatste uitkomst, mislukte taken en de machine zelf. Alles via de ClusterForge-agent op de PBS."
      />
      <Suspense>
        <PbsGate>{(s) => <OverviewView key={s.node_id} srv={s} />}</PbsGate>
      </Suspense>
    </div>
  );
}

function OverviewView({ srv }: { srv: PbsServer }) {
  const o = usePbsOverview(srv.node_id);
  return (
    <QueryState q={o}>
      {o.data && (
        <div className="space-y-6">
          <Strip o={o.data} />
          {o.data.warnings.length > 0 && (
            <Card title="Aandacht nodig">
              <ul className="space-y-1.5 text-sm">
                {o.data.warnings.map((w) => (
                  <li key={w.text} className="flex items-start gap-2">
                    <Badge tone={w.level === "crit" ? "red" : "amber"}>{w.level === "crit" ? "Ernstig" : "Let op"}</Badge>
                    <span>{w.text}</span>
                  </li>
                ))}
              </ul>
            </Card>
          )}
          <TileGrid>
            <Win wide>
              <DatastoresCard o={o.data} />
            </Win>
            <Win>
              <MachineCard o={o.data} />
            </Win>
            <Win wide>
              <JobsCard o={o.data} />
            </Win>
            <Win>
              <Card title={`Mislukt (24 u)${o.data.failed.length ? ` · ${o.data.failed.length}` : ""}`}>
                {o.data.failed.length === 0 ? (
                  <p className="text-sm text-slate-500">Geen mislukte taken in het laatste etmaal.</p>
                ) : (
                  <PbsTaskTable id={srv.node_id} tasks={o.data.failed} compact />
                )}
                <p className="mt-3 text-sm">
                  <Link href={`/pbs/taken?pbs=${srv.node_id}`} className="underline">
                    Alle taken
                  </Link>
                </p>
              </Card>
            </Win>
          </TileGrid>
          {o.data.errors.length > 0 && <p className="text-xs text-slate-500">Niet gelezen: {o.data.errors.join(" · ")}</p>}
        </div>
      )}
    </QueryState>
  );
}

function Stat({ label, value, sub, tone }: { label: string; value: string; sub?: string; tone?: "red" | "amber" | "green" }) {
  return (
    <div className="rounded-lg border border-slate-200 bg-white p-3 dark:border-slate-800 dark:bg-slate-900">
      <div className="text-xs font-medium tracking-wide text-slate-500 uppercase">{label}</div>
      <div
        className={cx(
          "mt-1 text-xl font-semibold tabular-nums",
          tone === "red" && "text-red-600 dark:text-red-400",
          tone === "amber" && "text-amber-600 dark:text-amber-400",
          tone === "green" && "text-emerald-600 dark:text-emerald-400",
        )}
      >
        {value}
      </div>
      {sub && <div className="mt-0.5 truncate text-xs text-slate-500">{sub}</div>}
    </div>
  );
}

function Strip({ o }: { o: PbsOverview }) {
  const used = o.datastores.reduce((a, d) => a + (d.used || 0), 0);
  const total = o.datastores.reduce((a, d) => a + (d.total || 0), 0);
  const crit = o.warnings.filter((w) => w.level === "crit").length;
  const badJobs = o.jobs.filter((j) => !j.disable && j["last-run-state"] && j["last-run-state"] !== "OK" && !j["last-run-state"].startsWith("WARNINGS")).length;
  return (
    <div className="grid grid-cols-2 gap-3 md:grid-cols-3 xl:grid-cols-6">
      <Stat label="Server" value={o.server.hostname} sub={`PBS ${o.version ?? o.server.version ?? "?"}`} />
      <Stat label="Datastores" value={String(o.datastores.length)} sub={total ? `${formatBytes(used)} van ${formatBytes(total)}` : undefined} />
      <Stat label="Gebruik" value={`${Math.round(pct(used, total))}%`} tone={pct(used, total) >= 95 ? "red" : pct(used, total) >= 85 ? "amber" : undefined} sub="alle datastores samen" />
      <Stat label="Jobs" value={String(o.jobs.length)} sub={badJobs ? `${badJobs} mislukt` : "alles OK"} tone={badJobs ? "red" : undefined} />
      <Stat label="Taken" value={String(o.running)} sub={`lopen nu · ${o.failed.length} mislukt (24 u)`} tone={o.failed.length ? "red" : undefined} />
      <Stat
        label="Aandacht"
        value={o.warnings.length === 0 ? "Niets" : String(o.warnings.length)}
        sub={crit > 0 ? `${crit} ernstig` : o.warnings.length > 0 ? "waarschuwingen" : "alles in orde"}
        tone={crit > 0 ? "red" : o.warnings.length > 0 ? "amber" : "green"}
      />
    </div>
  );
}

// Spark is het gebruik van een datastore over de laatste maand.
function Spark({ d }: { d: PbsDatastoreUsage }) {
  const pts = (d.history ?? []).map((v, i) => [i, v] as const).filter((p): p is readonly [number, number] => p[1] !== null && p[1] !== undefined);
  if (pts.length < 2) return null;
  const n = (d.history ?? []).length - 1 || 1;
  const path = pts.map(([i, v], k) => `${k ? "L" : "M"}${((i / n) * 100).toFixed(1)},${(30 - v * 30).toFixed(1)}`).join(" ");
  return (
    <svg viewBox="0 0 100 30" preserveAspectRatio="none" className="h-6 w-24 text-sky-600 dark:text-sky-400" aria-label="Verloop van het gebruik">
      <path d={path} fill="none" stroke="currentColor" strokeWidth="1.5" vectorEffect="non-scaling-stroke" />
    </svg>
  );
}

function fullIn(d: PbsDatastoreUsage) {
  const t = d["estimated-full-date"];
  if (!t) return "–";
  if (t < 0) return "nooit";
  const days = Math.round((t * 1000 - Date.now()) / 86_400_000);
  if (days <= 0) return "nu";
  if (days > 730) return "> 2 jaar";
  return `${days} dagen`;
}

function DatastoresCard({ o }: { o: PbsOverview }) {
  return (
    <Card title="Datastores">
      {o.datastores.length === 0 ? (
        <Empty>Nog geen datastore.</Empty>
      ) : (
        <div className="relative -mx-4 overflow-x-auto sm:mx-0">
          <table className={tableClass}>
            <thead>
              <tr>
                <th className={thClass}>Naam</th>
                <th className={thClass}>Gebruik</th>
                <th className={cx(thClass, "hidden sm:table-cell")}>Vrij</th>
                <th className={cx(thClass, "hidden md:table-cell")}>Verloop</th>
                <th className={thClass}>Vol over</th>
                <th className={cx(thClass, "hidden lg:table-cell")}>Ontdubbeling</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
              {o.datastores.map((d) => {
                const gc = d["gc-status"];
                const dedup = gc && gc["disk-bytes"] ? (gc["index-data-bytes"] ?? 0) / gc["disk-bytes"] : 0;
                return (
                  <tr key={d.store}>
                    <td className={cx(tdClass, "font-medium")}>
                      {d.store}
                      {d["mount-status"] === "notmounted" && (
                        <span className="ml-2">
                          <Badge tone="amber">niet aangekoppeld</Badge>
                        </span>
                      )}
                    </td>
                    <td className={cx(tdClass, "min-w-40")}>
                      {d.error ? <span className="text-red-600 dark:text-red-400">{d.error}</span> : <Meter value={pct(d.used, d.total)} label={`Gebruik ${d.store}`} />}
                    </td>
                    <td className={cx(tdClass, "hidden whitespace-nowrap tabular-nums sm:table-cell")}>{formatBytes(d.avail)}</td>
                    <td className={cx(tdClass, "hidden md:table-cell")}>
                      <Spark d={d} />
                    </td>
                    <td className={cx(tdClass, "whitespace-nowrap")}>{fullIn(d)}</td>
                    <td className={cx(tdClass, "hidden whitespace-nowrap tabular-nums lg:table-cell")}>{dedup ? `${dedup.toFixed(1)}×` : "–"}</td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
    </Card>
  );
}

function MachineCard({ o }: { o: PbsOverview }) {
  const s = o.status;
  if (!s) return <Card title="Machine"><Alert kind="info">De status van de machine kon niet gelezen worden.</Alert></Card>;
  return (
    <Card title="Machine">
      <dl className="space-y-2 text-sm">
        <Row k="CPU">
          <Meter value={s.cpu * 100} label="CPU" />
        </Row>
        <Row k="Geheugen">
          <Meter value={pct(s.memory.used, s.memory.total)} label="Geheugen" />
        </Row>
        <Row k="Systeemschijf">
          <Meter value={pct(s.root.used, s.root.total)} warn={80} crit={90} label="Systeemschijf" />
        </Row>
        <Row k="IO-wachten">{(s.wait * 100).toFixed(1)}%</Row>
        <Row k="Load">{(s.loadavg ?? []).map((l) => l.toFixed(2)).join(" · ")}</Row>
        <Row k="Uptime">{uptime(s.uptime)}</Row>
        <Row k="CPU-model">
          {s.cpuinfo.model} ({s.cpuinfo.cpus} threads)
        </Row>
        <Row k="Kernel">{s.kversion}</Row>
        <Row k="Abonnement">{o.subscription === "active" ? "Actief" : o.subscription ? "Geen (no-subscription)" : "–"}</Row>
        <Row k="Fingerprint">
          <span className="font-mono text-xs break-all">{s.info.fingerprint}</span>
        </Row>
      </dl>
    </Card>
  );
}

function Row({ k, children }: { k: string; children: React.ReactNode }) {
  return (
    <div className="grid grid-cols-[7.5rem_1fr] items-center gap-2">
      <dt className="text-slate-500">{k}</dt>
      <dd className="min-w-0">{children}</dd>
    </div>
  );
}

function JobsCard({ o }: { o: PbsOverview }) {
  return (
    <Card title="Jobs">
      {o.jobs.length === 0 ? (
        <Empty>Nog geen jobs. Garbage collection, prune, verify en sync plan je vanaf M3 onder Jobs.</Empty>
      ) : (
        <div className="relative -mx-4 overflow-x-auto sm:mx-0">
          <table className={tableClass}>
            <thead>
              <tr>
                <th className={thClass}>Soort</th>
                <th className={thClass}>Job</th>
                <th className={cx(thClass, "hidden sm:table-cell")}>Schema</th>
                <th className={thClass}>Laatste keer</th>
                <th className={cx(thClass, "hidden md:table-cell")}>Volgende</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
              {o.jobs.map((j) => {
                const st = j["last-run-state"];
                const bad = !!st && st !== "OK" && !st.startsWith("WARNINGS");
                return (
                  <tr key={j.kind + j.id} className={cx(bad && "bg-red-50/40 dark:bg-red-950/20")}>
                    <td className={tdClass}>{jobKindLabel[j.kind]}</td>
                    <td className={tdClass}>
                      <span className="font-medium">{j.kind === "gc" ? j.store : j.id}</span>
                      {j.kind !== "gc" && <span className="ml-1 text-xs text-slate-500">{j.store}</span>}
                      {j.disable && (
                        <span className="ml-2">
                          <Badge>uit</Badge>
                        </span>
                      )}
                    </td>
                    <td className={cx(tdClass, "hidden font-mono text-xs sm:table-cell")}>{j.schedule || "–"}</td>
                    <td className={cx(tdClass, "whitespace-nowrap")}>
                      {st ? <Badge tone={bad ? "red" : st === "OK" ? "green" : "amber"}>{st.length > 40 ? st.slice(0, 40) + "…" : st}</Badge> : "–"}
                      <span className="ml-2 text-xs text-slate-500">{when(j["last-run-endtime"])}</span>
                    </td>
                    <td className={cx(tdClass, "hidden whitespace-nowrap text-slate-500 md:table-cell")}>{when(j["next-run"])}</td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
    </Card>
  );
}
