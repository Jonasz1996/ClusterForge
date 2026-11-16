"use client";

import Link from "next/link";
import { useRouter, useSearchParams } from "next/navigation";
import { Suspense, useMemo, useState } from "react";
import { Tabs } from "@/components/Tabs";
import { ago, tableClass, tdClass, thClass } from "@/components/inventory/bits";
import { Alert, Badge, Button, Card, PageHeader, cx } from "@/components/ui";
import { useIsAdmin } from "@/lib/inventory";
import {
  scoreTone,
  severities,
  severityTone,
  useHardening,
  useRefreshSecurity,
  type HardeningNode,
  type HardeningOverview,
  type HardeningRule,
  type Severity,
} from "@/lib/security";

// Verharding (fase 27 M2): per node vaste regels op SSH, accounts, rechten,
// kernel, netwerk en diensten, met een score en wat je eraan doet.

const tabs = [
  { key: "nodes", label: "Per node" },
  { key: "regels", label: "Per regel" },
] as const;
type Tab = (typeof tabs)[number]["key"];

export default function HardeningPage() {
  return (
    <Suspense>
      <Hardening />
    </Suspense>
  );
}

function Hardening() {
  const params = useSearchParams();
  const router = useRouter();
  const tab = (tabs.find((t) => t.key === params.get("tab"))?.key ?? "nodes") as Tab;
  const data = useHardening();
  const isAdmin = useIsAdmin();
  const refresh = useRefreshSecurity();
  return (
    <div className="space-y-6">
      <PageHeader
        title="Verharding"
        description="Hoe goed elke node dichtgezet is: SSH, accounts, rechten op gevoelige bestanden, kernel- en netwerkinstellingen en overbodige diensten. De agent leest alleen; de regels staan in ClusterForge. In een LXC-container slaat ClusterForge over wat de Proxmox-host regelt."
        actions={
          isAdmin && (
            <Button variant="secondary" onClick={() => refresh.mutate({ nodes: true })} disabled={refresh.isPending}>
              {refresh.isSuccess ? "Controle loopt…" : "Nu controleren"}
            </Button>
          )
        }
      />
      <Tabs tabs={tabs} value={tab} onChange={(t) => router.replace(`/kwetsbaarheden/verharding?tab=${t}`)} />
      {data.isError && <Alert>Verharding laden mislukte.</Alert>}
      {data.data && <Summary o={data.data} />}
      {data.data && tab === "nodes" && <NodesTab o={data.data} isAdmin={isAdmin} />}
      {data.data && tab === "regels" && <RulesTab o={data.data} />}
    </div>
  );
}

function failsOf(o: HardeningOverview, n: HardeningNode) {
  const byId = new Map(o.rules.map((r) => [r.id, r]));
  return n.results.filter((x) => x.result === "fout").map((x) => ({ res: x, rule: byId.get(x.id)! }));
}

function Summary({ o }: { o: HardeningOverview }) {
  const checked = o.nodes.filter((n) => n.hardening_score !== null);
  const avg = checked.length ? Math.round(checked.reduce((s, n) => s + (n.hardening_score ?? 0), 0) / checked.length) : null;
  const count: Partial<Record<Severity, number>> = {};
  for (const n of checked) for (const f of failsOf(o, n)) count[f.rule.severity] = (count[f.rule.severity] ?? 0) + 1;
  const old = o.nodes.filter((n) => !n.audit_supported).length;
  const lynis = o.nodes.filter((n) => n.lynis).length;
  return (
    <>
      {old > 0 && (
        <Alert kind="info">
          {old} {old === 1 ? "node heeft" : "nodes hebben"} een agent ouder dan protocol {o.protocol}. Werk ze bij via{" "}
          <Link href="/nodes" className="font-medium underline">
            Nodes → Agents bijwerken
          </Link>
          .
        </Alert>
      )}
      <div className="grid grid-cols-2 gap-3 md:grid-cols-3 xl:grid-cols-6">
        <Tile label="Gemiddelde score" value={avg === null ? "–" : `${avg}%`} tone={avg === null ? undefined : toneClass(scoreTone(avg))} />
        <Tile label="Gecontroleerd" value={`${checked.length}/${o.nodes.length}`} />
        <Tile label="Kritiek" value={count.kritiek ?? 0} tone={(count.kritiek ?? 0) > 0 ? "red" : undefined} />
        <Tile label="Hoog" value={count.hoog ?? 0} tone={(count.hoog ?? 0) > 0 ? "red" : undefined} />
        <Tile label="Middel en laag" value={(count.middel ?? 0) + (count.laag ?? 0)} tone={(count.middel ?? 0) > 0 ? "amber" : undefined} />
        <Tile label="Lynis-rapporten" value={lynis} hint="Nodes met /var/log/lynis-report.dat" />
      </div>
    </>
  );
}

function toneClass(t: string): "red" | "amber" | "green" | undefined {
  return t === "red" ? "red" : t === "amber" ? "amber" : t === "green" ? "green" : undefined;
}

function Tile({ label, value, tone, hint }: { label: string; value: string | number; tone?: "red" | "amber" | "green"; hint?: string }) {
  return (
    <div className="rounded-lg border border-slate-200 bg-white p-4 dark:border-slate-800 dark:bg-slate-900" title={hint}>
      <div className="text-xs font-medium uppercase tracking-wide text-slate-500">{label}</div>
      <div
        className={cx(
          "mt-1 text-2xl font-semibold tabular-nums",
          tone === "red" && "text-red-600 dark:text-red-400",
          tone === "amber" && "text-amber-600 dark:text-amber-400",
          tone === "green" && "text-emerald-600 dark:text-emerald-400",
        )}
      >
        {value}
      </div>
    </div>
  );
}

function ScoreBar({ score }: { score: number | null }) {
  if (score === null) return <span className="text-slate-400">–</span>;
  const t = scoreTone(score);
  return (
    <div className="flex items-center gap-2">
      <div className="h-2 w-24 overflow-hidden rounded-full bg-slate-200 dark:bg-slate-700">
        <div
          className={cx("h-full", t === "green" && "bg-emerald-500", t === "amber" && "bg-amber-500", t === "red" && "bg-red-500")}
          style={{ width: `${score}%` }}
        />
      </div>
      <span className="tabular-nums">{score}%</span>
    </div>
  );
}

function NodesTab({ o, isAdmin }: { o: HardeningOverview; isAdmin: boolean }) {
  const [open, setOpen] = useState<string | null>(null);
  const refresh = useRefreshSecurity();
  const nodes = useMemo(() => [...o.nodes].sort((a, b) => (a.hardening_score ?? 101) - (b.hardening_score ?? 101)), [o.nodes]);
  return (
    <div className="space-y-2">
      {nodes.map((n) => {
        const fails = failsOf(o, n);
        const isOpen = open === n.node_id;
        return (
          <div key={n.node_id} className="rounded-lg border border-slate-200 bg-white dark:border-slate-800 dark:bg-slate-900">
            <button
              className="flex w-full flex-wrap items-center gap-x-4 gap-y-2 p-3 text-left"
              onClick={() => setOpen(isOpen ? null : n.node_id)}
              aria-expanded={isOpen}
            >
              <span className="min-w-40 flex-1">
                <span className="font-medium">{n.hostname}</span>
                <span className="block text-xs text-slate-500">
                  {n.cluster_name || "geen cluster"}
                  {n.virtualization ? ` · ${n.virtualization}` : ""}
                  {n.audit_at && ` · ${ago(n.audit_at)}`}
                </span>
              </span>
              <ScoreBar score={n.hardening_score} />
              <span className="flex min-w-48 flex-wrap gap-1">
                {severities.map((s) => {
                  const c = fails.filter((f) => f.rule.severity === s).length;
                  return c > 0 ? (
                    <Badge key={s} tone={severityTone[s]}>
                      {c} {s}
                    </Badge>
                  ) : null;
                })}
                {n.hardening_score !== null && fails.length === 0 && <Badge tone="green">alles in orde</Badge>}
                {n.note && <span className="text-xs text-amber-700 dark:text-amber-400">{n.note}</span>}
              </span>
              <span className="text-xs text-slate-500">{isOpen ? "▲" : "▼"}</span>
            </button>
            {isOpen && (
              <div className="space-y-4 border-t border-slate-200 p-3 text-sm dark:border-slate-800">
                {n.results.length === 0 ? <p className="text-slate-500">Nog geen controle.</p> : <Checks o={o} n={n} />}
                {n.lynis && <Lynis n={n} />}
                {isAdmin && n.audit_supported && n.online && (
                  <Button variant="secondary" onClick={() => refresh.mutate({ nodes: true, node_id: n.node_id })} disabled={refresh.isPending}>
                    Deze node opnieuw controleren
                  </Button>
                )}
              </div>
            )}
          </div>
        );
      })}
    </div>
  );
}

function Checks({ o, n }: { o: HardeningOverview; n: HardeningNode }) {
  const res = new Map(n.results.map((r) => [r.id, r]));
  const cats = [...new Set(o.rules.map((r) => r.category))];
  return (
    <div className="grid gap-4 lg:grid-cols-2">
      {cats.map((cat) => (
        <div key={cat}>
          <div className="mb-1 font-medium">{cat}</div>
          <ul className="space-y-1">
            {o.rules
              .filter((r) => r.category === cat)
              .map((r) => {
                const x = res.get(r.id);
                if (!x) return null;
                return (
                  <li key={r.id} className={cx("rounded-md p-2", x.result === "fout" && "bg-red-50 dark:bg-red-950/30")}>
                    <div className="flex items-start gap-2">
                      <span
                        className={cx(
                          "w-4 shrink-0 text-center font-bold",
                          x.result === "ok" && "text-emerald-600",
                          x.result === "fout" && "text-red-600",
                          x.result === "nvt" && "text-slate-400",
                        )}
                        aria-label={x.result}
                      >
                        {x.result === "ok" ? "✓" : x.result === "fout" ? "✗" : "–"}
                      </span>
                      <span className="flex-1">
                        <span className={cx(x.result === "nvt" && "text-slate-500")}>{x.result === "ok" ? okTitle(r) : r.title}</span>
                        {x.result !== "ok" && (
                          <span className="block text-xs text-slate-500">
                            {x.result === "nvt" ? x.value : `Nu: ${x.value} · verwacht: ${x.expected}`}
                          </span>
                        )}
                        {x.result === "fout" && <span className="mt-1 block text-xs text-slate-700 dark:text-slate-300">{r.fix}</span>}
                      </span>
                      {x.result === "fout" && <Badge tone={severityTone[r.severity]}>{r.severity}</Badge>}
                    </div>
                  </li>
                );
              })}
          </ul>
        </div>
      ))}
    </div>
  );
}

/** okTitle: de regel zonder het woord van de fout, zoals "root mag inloggen…" → toon de titel gedimd. */
function okTitle(r: HardeningRule) {
  return <span className="text-slate-600 dark:text-slate-400">{r.title.replace(/^./, (c) => c.toUpperCase())} — in orde</span>;
}

function Lynis({ n }: { n: HardeningNode }) {
  const l = n.lynis!;
  return (
    <Card title={`Lynis ${l.lynis_version}: hardening index ${l.hardening_index}`}>
      <p className="text-xs text-slate-500">
        Rapport van {l.date || "?"}, {l.tests_done} tests. ClusterForge leest /var/log/lynis-report.dat; het draaien van Lynis zelf komt in M7.
      </p>
      {l.warnings.length > 0 && (
        <>
          <div className="mt-2 font-medium">Waarschuwingen</div>
          <ul className="list-disc pl-5">
            {l.warnings.map((w) => (
              <li key={w}>{w}</li>
            ))}
          </ul>
        </>
      )}
      {l.suggestions.length > 0 && (
        <details className="mt-2">
          <summary className="cursor-pointer font-medium">{l.suggestions.length} suggesties</summary>
          <ul className="list-disc pl-5">
            {l.suggestions.map((w) => (
              <li key={w}>{w}</li>
            ))}
          </ul>
        </details>
      )}
    </Card>
  );
}

function RulesTab({ o }: { o: HardeningOverview }) {
  const rows = o.rules
    .map((r) => {
      const failing = o.nodes.filter((n) => n.results.some((x) => x.id === r.id && x.result === "fout"));
      const checked = o.nodes.filter((n) => n.results.some((x) => x.id === r.id && x.result !== "nvt")).length;
      return { r, failing, checked };
    })
    .sort((a, b) => b.failing.length - a.failing.length || severities.indexOf(a.r.severity) - severities.indexOf(b.r.severity));
  return (
    <Card>
      <div className="overflow-x-auto">
        <table className={tableClass}>
          <thead>
            <tr>
              <th className={thClass}>Regel</th>
              <th className={thClass}>Ernst</th>
              <th className={thClass}>Faalt op</th>
              <th className={thClass}>Oplossing</th>
            </tr>
          </thead>
          <tbody>
            {rows.map(({ r, failing, checked }) => (
              <tr key={r.id} className="border-t border-slate-100 dark:border-slate-800">
                <td className={tdClass}>
                  <div className="font-medium">{r.title}</div>
                  <div className="text-xs text-slate-500">{r.category}</div>
                </td>
                <td className={tdClass}>
                  <Badge tone={severityTone[r.severity]}>{r.severity}</Badge>
                </td>
                <td className={tdClass}>
                  {failing.length === 0 ? (
                    <span className="text-emerald-600">{checked > 0 ? "nergens" : "–"}</span>
                  ) : (
                    <span>
                      {failing.length}/{checked}: {failing.map((n) => n.hostname).join(", ")}
                    </span>
                  )}
                </td>
                <td className={cx(tdClass, "text-xs text-slate-600 dark:text-slate-400")}>{r.fix}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </Card>
  );
}
