"use client";

import Link from "next/link";
import { useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { Button, cx } from "@/components/ui";
import { ItemChart } from "@/components/zabbix/NodeZabbix";
import { AvailDot, HostMap, SeverityBadge, StateBand, ZabbixSelf, nf } from "@/components/zabbix/Zabbix";
import {
  countBySeverity,
  formatZabbix,
  severities,
  severityLabel,
  severityStyle,
  useZabbixEvents,
  useZabbixMetrics,
  type ZabbixEvents,
  type ZabbixHost,
  type ZabbixHostMetrics,
  type ZabbixOverview,
  type ZabbixRange,
  type ZabbixTopCount,
  type ZabbixWindow,
} from "@/lib/zabbixMonitoring";

export type DashKey = "infra" | "nodes" | "zabbix" | "problemen";
export const dashboards: { key: DashKey; label: string; description: string }[] = [
  { key: "infra", label: "Infrastructuur", description: "Alle hosts: toestand, zwaarste verbruikers en problemen over tijd." },
  { key: "nodes", label: "ClusterForge-nodes", description: "Per cluster elke node met CPU, geheugen, schijf en open problemen." },
  { key: "zabbix", label: "Zabbix zelf", description: "HA-cluster, proxy, nieuwe waarden per seconde, wachtrij en caches." },
  { key: "problemen", label: "Problemen", description: "Hoeveel problemen, wanneer, hoe snel opgelost en waar." },
];

const dayFmt = new Intl.DateTimeFormat("nl-BE", { day: "numeric", month: "short" });
const hourFmt = new Intl.DateTimeFormat("nl-BE", { hour: "2-digit", minute: "2-digit" });
const fullFmt = new Intl.DateTimeFormat("nl-BE", { weekday: "short", day: "numeric", month: "short", hour: "2-digit", minute: "2-digit" });

export function windowRange(w: ZabbixWindow): ZabbixRange {
  return w;
}

// --- bouwstenen -------------------------------------------------------------------------

export function Panel({ title, sub, className, children, action }: { title: string; sub?: ReactNode; className?: string; children: ReactNode; action?: ReactNode }) {
  return (
    <section className={cx("flex min-w-0 flex-col rounded-lg border border-slate-200 bg-white p-4 shadow-xs dark:border-slate-800 dark:bg-slate-900", className)}>
      <div className="mb-3 flex items-start justify-between gap-2">
        <div className="min-w-0">
          <h2 className="text-sm font-semibold">{title}</h2>
          {sub && <p className="text-xs text-slate-500">{sub}</p>}
        </div>
        {action}
      </div>
      <div className="min-w-0 flex-1">{children}</div>
    </section>
  );
}

export function Stat({ label, value, tone, sub }: { label: string; value: string; tone?: "red" | "amber" | "green"; sub?: ReactNode }) {
  return (
    <section className="flex min-w-0 flex-col rounded-lg border border-slate-200 bg-white p-4 shadow-xs dark:border-slate-800 dark:bg-slate-900">
      <span className="text-xs font-medium uppercase tracking-wide text-slate-500">{label}</span>
      <span
        className={cx(
          "mt-1 text-3xl font-semibold tabular-nums",
          tone === "red" && "text-red-600 dark:text-red-400",
          tone === "amber" && "text-amber-600 dark:text-amber-400",
          tone === "green" && "text-emerald-600 dark:text-emerald-400",
        )}
      >
        {value}
      </span>
      {sub && <span className="mt-0.5 text-xs text-slate-500">{sub}</span>}
    </section>
  );
}

function pctColor(v: number) {
  return v >= 90 ? "#ef4444" : v >= 75 ? "#f59e0b" : "#10b981";
}

// Gauge is een halve meter van 0 tot 100 %.
export function Gauge({ value, label, size = 88 }: { value: number | null | undefined; label: string; size?: number }) {
  const r = 34;
  const c = Math.PI * r;
  const v = value == null ? 0 : Math.min(100, Math.max(0, value));
  return (
    <div className="flex flex-col items-center" style={{ width: size }} title={`${label}: ${value == null ? "onbekend" : `${nf.format(value)} %`}`}>
      <svg viewBox="0 0 80 46" width={size} height={size * 0.575} aria-hidden>
        <path d="M6 40 A34 34 0 0 1 74 40" fill="none" stroke="currentColor" strokeWidth="8" className="text-slate-100 dark:text-slate-800" strokeLinecap="round" />
        {value != null && (
          <path d="M6 40 A34 34 0 0 1 74 40" fill="none" stroke={pctColor(v)} strokeWidth="8" strokeLinecap="round" strokeDasharray={`${(v / 100) * c} ${c}`} />
        )}
        <text x="40" y="38" textAnchor="middle" className="fill-current text-[13px] font-semibold tabular-nums">
          {value == null ? "—" : `${Math.round(v)}%`}
        </text>
      </svg>
      <span className="text-[11px] text-slate-500">{label}</span>
    </div>
  );
}

// BarList is een top-N met een balk per rij.
export function BarList({ rows, unit = "%", empty }: { rows: { key: string; label: ReactNode; value: number; href?: string }[]; unit?: string; empty: string }) {
  if (rows.length === 0) return <p className="text-sm text-slate-500">{empty}</p>;
  const max = unit === "%" ? 100 : Math.max(...rows.map((r) => r.value), 1);
  return (
    <ul className="space-y-1.5">
      {rows.map((r) => (
        <li key={r.key} className="text-sm">
          <div className="flex justify-between gap-2">
            <span className="min-w-0 truncate">{r.href ? <Link href={r.href} className="hover:underline">{r.label}</Link> : r.label}</span>
            <span className="shrink-0 tabular-nums text-slate-600 dark:text-slate-300">{unit === "%" ? `${nf.format(r.value)} %` : formatZabbix(r.value, unit)}</span>
          </div>
          <div className="mt-0.5 h-1.5 overflow-hidden rounded-full bg-slate-100 dark:bg-slate-800">
            <div className="h-full rounded-full" style={{ width: `${Math.min(100, (r.value / max) * 100)}%`, background: unit === "%" ? pctColor(r.value) : "#3b82f6" }} />
          </div>
        </li>
      ))}
    </ul>
  );
}

// StackedBars toont nieuwe problemen per stukje tijd, gestapeld per ernst.
export function StackedBars({ e, height = 160 }: { e: ZabbixEvents; height?: number }) {
  const [hover, setHover] = useState<number | null>(null);
  const b = e.buckets;
  const stack = [5, 4, 3, 2, 1, 0];
  const total = (i: number) => b[i].severity.reduce((a, v) => a + v, 0);
  const max = Math.max(1, ...b.map((_, i) => total(i)));
  const perDay = e.window !== "24h";
  const h = hover == null ? null : b[hover];
  if (b.length === 0) return <p className="text-sm text-slate-500">Geen gegevens.</p>;
  return (
    <div className="relative" onMouseLeave={() => setHover(null)}>
      <div className="flex items-end gap-px" style={{ height }} role="img" aria-label="Nieuwe problemen per ernst over tijd">
        {b.map((x, i) => (
          <div key={x.at} className="flex h-full min-w-0 flex-1 flex-col justify-end" onMouseEnter={() => setHover(i)}>
            {stack.map((s) =>
              x.severity[s] ? (
                <div
                  key={s}
                  className={cx("w-full first:rounded-t-sm", hover != null && hover !== i && "opacity-50")}
                  style={{ height: `${(x.severity[s] / max) * 100}%`, background: severityStyle[s].bar }}
                />
              ) : null,
            )}
          </div>
        ))}
      </div>
      <div className="mt-1 flex justify-between text-xs text-slate-500">
        <span>{perDay ? dayFmt.format(new Date(b[0].at)) : hourFmt.format(new Date(b[0].at))}</span>
        <span>nu</span>
      </div>
      {h && hover != null && (
        <div
          className="pointer-events-none absolute top-0 z-10 w-max -translate-x-1/2 rounded-md border border-slate-200 bg-white px-2.5 py-1.5 text-xs shadow-lg dark:border-slate-700 dark:bg-slate-900"
          style={{ left: `${Math.min(85, Math.max(15, ((hover + 0.5) / b.length) * 100))}%` }}
        >
          <div className="font-medium">{perDay ? dayFmt.format(new Date(h.at)) : fullFmt.format(new Date(h.at))}</div>
          {stack
            .filter((s) => h.severity[s] > 0)
            .map((s) => (
              <div key={s} className="flex justify-between gap-4 text-slate-600 dark:text-slate-400">
                <span>{severityLabel[s]}</span>
                <span className="tabular-nums">{h.severity[s]}</span>
              </div>
            ))}
          {total(hover) === 0 && <div className="text-slate-500">geen nieuwe problemen</div>}
        </div>
      )}
    </div>
  );
}

const days = ["ma", "di", "wo", "do", "vr", "za", "zo"];

// Heatmap: nieuwe problemen per weekdag en uur.
export function Heatmap({ e }: { e: ZabbixEvents }) {
  const max = Math.max(1, ...e.heatmap.flat());
  return (
    <div className="overflow-x-auto">
      <div className="inline-grid min-w-full gap-0.5" style={{ gridTemplateColumns: `2rem repeat(24, minmax(0.9rem, 1fr))` }}>
        <span />
        {Array.from({ length: 24 }, (_, h) => (
          <span key={h} className="text-center text-[10px] text-slate-500">
            {h % 3 === 0 ? h : ""}
          </span>
        ))}
        {e.heatmap.map((row, d) => (
          <div key={d} className="contents">
            <span className="text-[11px] leading-5 text-slate-500">{days[d]}</span>
            {row.map((v, h) => (
              <span
                key={h}
                className="h-5 rounded-sm bg-slate-100 dark:bg-slate-800"
                style={v > 0 ? { background: `rgba(239, 68, 68, ${0.15 + 0.85 * (v / max)})` } : undefined}
                title={`${days[d]} ${h}:00: ${v} probleem${v === 1 ? "" : "en"}`}
              />
            ))}
          </div>
        ))}
      </div>
    </div>
  );
}

function TopList({ rows, empty, host }: { rows: ZabbixTopCount[]; empty: string; host?: boolean }) {
  if (rows.length === 0) return <p className="text-sm text-slate-500">{empty}</p>;
  const max = Math.max(...rows.map((r) => r.count), 1);
  return (
    <ul className="space-y-1.5 text-sm">
      {rows.map((r) => (
        <li key={r.id}>
          <div className="flex items-center justify-between gap-2">
            <span className="flex min-w-0 items-center gap-1.5">
              <span className={cx("size-2 shrink-0 rounded-full", severityStyle[r.severity]?.dot)} aria-hidden />
              {host && r.node ? (
                <Link href={`/nodes/detail?id=${r.node.node_id}`} className="truncate text-brand-700 hover:underline dark:text-sky-300">
                  {r.node.hostname}
                </Link>
              ) : (
                <span className="truncate">{r.name}</span>
              )}
            </span>
            <span className="shrink-0 tabular-nums text-slate-600 dark:text-slate-300">{r.count}×</span>
          </div>
          <div className="mt-0.5 h-1 overflow-hidden rounded-full bg-slate-100 dark:bg-slate-800">
            <div className="h-full rounded-full" style={{ width: `${(r.count / max) * 100}%`, background: severityStyle[r.severity]?.bar }} />
          </div>
        </li>
      ))}
    </ul>
  );
}

function duration(s: number) {
  if (s < 60) return `${Math.round(s)} s`;
  if (s < 3600) return `${Math.round(s / 60)} min`;
  if (s < 86400) return `${nf.format(s / 3600)} u`;
  return `${nf.format(s / 86400)} d`;
}

// --- dashboards ----------------------------------------------------------------------------

function metricsByHost(m: ZabbixHostMetrics[] | undefined) {
  return new Map((m ?? []).map((x) => [x.hostid, x]));
}

function hostName(h: ZabbixHost) {
  return h.node?.hostname ?? h.name;
}

function topBy(o: ZabbixOverview, m: Map<string, ZabbixHostMetrics>, f: (x: ZabbixHostMetrics) => number | null | undefined) {
  return o.hosts
    .filter((h) => h.enabled)
    .map((h) => ({ h, v: m.get(h.hostid) ? f(m.get(h.hostid)!) : null }))
    .filter((x): x is { h: ZabbixHost; v: number } => x.v != null)
    .sort((a, b) => b.v - a.v)
    .slice(0, 10)
    .map(({ h, v }) => ({ key: h.hostid, label: hostName(h), value: v, href: h.node ? `/nodes/detail?id=${h.node.node_id}` : undefined }));
}

export function InfraDashboard({ o, win }: { o: ZabbixOverview; win: ZabbixWindow }) {
  const metrics = useZabbixMetrics();
  const events = useZabbixEvents(win);
  const m = useMemo(() => metricsByHost(metrics.data?.hosts), [metrics.data]);
  const bySev = countBySeverity(o);
  const open = severities.reduce<number>((a, s) => a + bySev[s], 0);
  const e = events.data;
  return (
    <div className="space-y-4">
      <StateBand o={o} />
      <div className="grid grid-cols-2 gap-4 lg:grid-cols-4">
        <Stat label="Open problemen" value={nf.format(open)} tone={o.counts.disaster + o.counts.high > 0 ? "red" : open > 0 ? "amber" : "green"} sub={`${o.counts.unacked} niet bevestigd`} />
        <Stat label="Hosts bereikbaar" value={`${o.counts.hosts_ok}/${o.counts.hosts}`} tone={o.counts.hosts_down > 0 ? "red" : "green"} sub={`${o.counts.hosts_down} onbereikbaar`} />
        <Stat label="Nieuwe problemen" value={e ? nf.format(e.total) : "…"} sub={`in ${win === "24h" ? "24 uur" : win === "7d" ? "7 dagen" : "30 dagen"}`} />
        <Stat label="Gemiddelde hersteltijd" value={e?.mttr != null ? duration(e.mttr) : "—"} sub={e ? `${e.resolved} opgelost` : undefined} />
      </div>
      <HostMap o={o} />
      <div className="grid gap-4 lg:grid-cols-3">
        <Panel title="Top CPU" sub="laatste waarde">
          <BarList rows={topBy(o, m, (x) => x.cpu)} empty={metrics.isLoading ? "Laden…" : "Geen CPU-items (template Linux by Zabbix agent)."} />
        </Panel>
        <Panel title="Top geheugen" sub="laatste waarde">
          <BarList rows={topBy(o, m, (x) => x.memory)} empty={metrics.isLoading ? "Laden…" : "Geen geheugenitems."} />
        </Panel>
        <Panel title="Top schijf /" sub="gebruikt">
          <BarList rows={topBy(o, m, (x) => x.disk)} empty={metrics.isLoading ? "Laden…" : "Geen schijfitems."} />
        </Panel>
      </div>
      <Panel title="Nieuwe problemen over tijd" sub={win === "24h" ? "per uur" : "per dag"}>
        {e ? <StackedBars e={e} /> : <p className="text-sm text-slate-500">{events.isError ? "Laden mislukte." : "Laden…"}</p>}
      </Panel>
    </div>
  );
}

export function NodesDashboard({ o }: { o: ZabbixOverview }) {
  const metrics = useZabbixMetrics();
  const m = useMemo(() => metricsByHost(metrics.data?.hosts), [metrics.data]);
  const groups = useMemo(() => {
    const g = new Map<string, { id: string | null; hosts: ZabbixHost[] }>();
    for (const h of o.hosts) {
      if (!h.node) continue;
      const k = h.node.cluster_name || "Losse nodes";
      const e = g.get(k) ?? { id: h.node.cluster_id, hosts: [] };
      e.hosts.push(h);
      g.set(k, e);
    }
    return [...g.entries()].sort((a, b) => (a[0] === "Losse nodes" ? 1 : b[0] === "Losse nodes" ? -1 : a[0].localeCompare(b[0], "nl")));
  }, [o.hosts]);
  if (groups.length === 0) return <p className="text-sm text-slate-500">Nog geen ClusterForge-node gekoppeld aan een Zabbix-host.</p>;
  return (
    <div className="space-y-4">
      {groups.map(([name, g]) => (
        <Panel
          key={name}
          title={name}
          sub={`${g.hosts.filter((h) => h.available === "ok").length}/${g.hosts.length} bereikbaar`}
          action={
            g.id ? (
              <Link href={`/clusters/detail?id=${g.id}`} className="text-xs font-medium text-brand-700 hover:underline dark:text-sky-300">
                Cluster →
              </Link>
            ) : undefined
          }
        >
          <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-3">
            {g.hosts.map((h) => {
              const x = m.get(h.hostid);
              return (
                <Link
                  key={h.hostid}
                  href={`/nodes/detail?id=${h.node!.node_id}`}
                  className="rounded-md border border-slate-200 p-3 transition hover:border-brand-500 dark:border-slate-800"
                >
                  <div className="mb-2 flex items-center justify-between gap-2">
                    <span className="flex min-w-0 items-center gap-1.5 text-sm font-medium">
                      <AvailDot a={h.available} />
                      <span className="truncate">{h.node!.hostname}</span>
                    </span>
                    {h.problems > 0 ? <SeverityBadge s={h.worst} /> : h.maintenance ? <span className="text-xs text-violet-600">onderhoud</span> : null}
                  </div>
                  <div className="flex justify-between">
                    <Gauge value={x?.cpu} label="CPU" />
                    <Gauge value={x?.memory} label="Geheugen" />
                    <Gauge value={x?.disk} label="Schijf /" />
                  </div>
                  <div className="mt-1 flex justify-between text-[11px] text-slate-500">
                    <span>load {x?.load != null ? nf.format(x.load) : "—"}</span>
                    <span>{x?.uptime != null ? `up ${formatZabbix(x.uptime, "uptime")}` : ""}</span>
                  </div>
                </Link>
              );
            })}
          </div>
        </Panel>
      ))}
    </div>
  );
}

export function ZabbixDashboard({ o, win }: { o: ZabbixOverview; win: ZabbixWindow }) {
  const range = windowRange(win);
  const charts = o.health.flatMap((h) =>
    (
      [
        ["nvps", "Nieuwe waarden per seconde"],
        ["values", "Verwerkte waarden per seconde"],
        ["queue", "Wachtrij"],
        ["queue_10m", "Wachtrij langer dan 10 min"],
        ["cache_history", "History-cache gebruikt"],
        ["cache_value", "Value-cache gebruikt"],
      ] as const
    )
      .filter(([k]) => h.items[k])
      .map(([k, label]) => ({ key: `${h.hostid}-${k}`, itemid: h.items[k], title: `${h.name} · ${label}` })),
  );
  return (
    <div className="space-y-4">
      <ZabbixSelf o={o} />
      {charts.length > 0 && (
        <div className="grid gap-4 lg:grid-cols-2">
          {charts.map((c) => (
            <Panel key={c.key} title={c.title}>
              <ItemChart itemid={c.itemid} range={range} height={150} />
            </Panel>
          ))}
        </div>
      )}
    </div>
  );
}

export function ProblemsDashboard({ win }: { win: ZabbixWindow }) {
  const events = useZabbixEvents(win);
  const e = events.data;
  if (!e) return <p className="text-sm text-slate-500">{events.isError ? "Laden mislukte." : "Laden…"}</p>;
  const bySev = [0, 0, 0, 0, 0, 0];
  for (const b of e.buckets) b.severity.forEach((v, i) => (bySev[i] += v));
  return (
    <div className="space-y-4">
      <div className="grid grid-cols-2 gap-4 lg:grid-cols-4">
        <Stat label="Nieuwe problemen" value={nf.format(e.total)} sub={e.truncated ? "meer dan 10 000; de oudste ontbreken" : undefined} />
        <Stat label="Ramp en hoog" value={nf.format(bySev[5] + bySev[4])} tone={bySev[5] + bySev[4] > 0 ? "red" : "green"} />
        <Stat label="Opgelost" value={nf.format(e.resolved)} sub={e.total > 0 ? `${Math.round((e.resolved / e.total) * 100)} %` : undefined} />
        <Stat label="Gemiddelde hersteltijd" value={e.mttr != null ? duration(e.mttr) : "—"} />
      </div>
      <Panel title="Nieuwe problemen" sub={win === "24h" ? "per uur, gestapeld per ernst" : "per dag, gestapeld per ernst"}>
        <StackedBars e={e} height={200} />
        <div className="mt-2 flex flex-wrap gap-x-4 gap-y-1 text-xs text-slate-600 dark:text-slate-400">
          {[5, 4, 3, 2, 1].map((s) => (
            <span key={s} className="inline-flex items-center gap-1.5">
              <span className="size-2.5 rounded-sm" style={{ background: severityStyle[s].bar }} aria-hidden />
              {severityLabel[s]} {bySev[s]}
            </span>
          ))}
        </div>
      </Panel>
      <div className="grid gap-4 lg:grid-cols-2">
        <Panel title="Wanneer" sub="nieuwe problemen per weekdag en uur">
          <Heatmap e={e} />
        </Panel>
        <Panel title="Vaakst" sub="triggers met de meeste problemen">
          <TopList rows={e.top_triggers} empty="Geen problemen in dit venster." />
        </Panel>
      </div>
      <Panel title="Hosts met de meeste problemen">
        <TopList rows={e.top_hosts} empty="Geen problemen in dit venster." host />
      </Panel>
    </div>
  );
}

// --- verversen en kiosk ------------------------------------------------------------------------

const refreshOptions = [
  { v: 0, label: "uit" },
  { v: 30, label: "30 s" },
  { v: 60, label: "1 min" },
  { v: 300, label: "5 min" },
  { v: 900, label: "15 min" },
];

export function useAutoRefresh(seconds: number) {
  const qc = useQueryClient();
  useEffect(() => {
    if (!seconds) return;
    const t = setInterval(() => {
      if (document.visibilityState !== "visible") return;
      for (const k of ["zabbix-overview", "zabbix-metrics", "zabbix-events", "zabbix-series", "zabbix-latest"]) void qc.invalidateQueries({ queryKey: [k] });
    }, seconds * 1000);
    return () => clearInterval(t);
  }, [seconds, qc]);
}

export function RefreshPicker({ value, onChange }: { value: number; onChange: (v: number) => void }) {
  return (
    <label className="inline-flex items-center gap-1.5 text-sm text-slate-600 dark:text-slate-300">
      <span aria-hidden>↻</span>
      <select
        value={value}
        onChange={(e) => onChange(Number(e.target.value))}
        className="rounded-md border border-slate-300 bg-white px-2 py-1.5 text-sm dark:border-slate-700 dark:bg-slate-900"
        aria-label="Automatisch verversen"
      >
        {refreshOptions.map((o) => (
          <option key={o.v} value={o.v}>
            {o.v ? `elke ${o.label}` : "niet verversen"}
          </option>
        ))}
      </select>
    </label>
  );
}

// useKiosk zet een element op volledig scherm (tv-modus).
export function useKiosk() {
  const ref = useRef<HTMLDivElement>(null);
  const [on, setOn] = useState(false);
  useEffect(() => {
    const f = () => setOn(document.fullscreenElement === ref.current && ref.current !== null);
    document.addEventListener("fullscreenchange", f);
    return () => document.removeEventListener("fullscreenchange", f);
  }, []);
  const toggle = () => {
    if (document.fullscreenElement) void document.exitFullscreen();
    else void ref.current?.requestFullscreen?.();
  };
  return { ref, on, toggle };
}

export function KioskButton({ on, toggle }: { on: boolean; toggle: () => void }) {
  return (
    <Button variant="secondary" onClick={toggle} title="Volledig scherm, voor een tv of tweede scherm">
      {on ? "Kiosk uit" : "Kiosk ⛶"}
    </Button>
  );
}
