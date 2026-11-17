"use client";

import Link from "next/link";
import { useMemo, useState, type ReactNode } from "react";
import { Alert, Button, Card, PageHeader, cx } from "@/components/ui";
import { AckButton } from "@/components/zabbix/Ack";
import {
  availLabel,
  countBySeverity,
  levelLabel,
  severities,
  severityLabel,
  severityStyle,
  since,
  useRefreshZabbix,
  useZabbixOverview,
  windowLabel,
  zabbixLinks,
  type ZabbixHost,
  type ZabbixLevel,
  type ZabbixOverview,
  type ZabbixProblem,
  type ZabbixWindow,
} from "@/lib/zabbixMonitoring";

export const nf = new Intl.NumberFormat("nl-BE", { maximumFractionDigits: 1 });
export const timeFmt = new Intl.DateTimeFormat("nl-BE", { hour: "2-digit", minute: "2-digit" });
const dayFmt = new Intl.DateTimeFormat("nl-BE", { weekday: "short", day: "numeric", hour: "2-digit", minute: "2-digit" });
const fullFmt = new Intl.DateTimeFormat("nl-BE", { dateStyle: "short", timeStyle: "short" });

// --- pagina-omhulsel -------------------------------------------------------------

// ZabbixPage is de kop en de lege of foutstanden die elke Zabbix-pagina deelt.
export function ZabbixPage({
  title,
  description,
  window,
  onWindow,
  children,
}: {
  title: string;
  description: string;
  window?: ZabbixWindow;
  onWindow?: (w: ZabbixWindow) => void;
  children: (o: ZabbixOverview) => ReactNode;
}) {
  const q = useZabbixOverview(window ?? "24h");
  const refresh = useRefreshZabbix();
  const o = q.data;
  const links = o ? zabbixLinks(o.frontend_url) : null;
  return (
    <div className="space-y-6">
      <PageHeader
        title={title}
        description={description}
        actions={
          <div className="flex flex-wrap items-center gap-2">
            {window && onWindow && (
              <div role="group" aria-label="Periode" className="inline-flex rounded-md border border-slate-300 p-0.5 dark:border-slate-700">
                {(["24h", "7d", "30d"] as const).map((w) => (
                  <button
                    key={w}
                    onClick={() => onWindow(w)}
                    aria-pressed={window === w}
                    className={cx(
                      "rounded px-3 py-1.5 text-sm font-medium",
                      window === w ? "bg-brand-600 text-white" : "text-slate-600 hover:bg-slate-100 dark:text-slate-300 dark:hover:bg-slate-800",
                    )}
                  >
                    {windowLabel[w]}
                  </button>
                ))}
              </div>
            )}
            {links && (
              <a
                href={links.problems}
                target="_blank"
                rel="noreferrer"
                className="inline-flex items-center rounded-md border border-slate-300 px-3 py-2 text-sm font-medium text-slate-700 hover:bg-slate-50 dark:border-slate-700 dark:text-slate-200 dark:hover:bg-slate-800"
              >
                Open Zabbix ↗
              </a>
            )}
            <Button
              variant="secondary"
              onClick={() => refresh.mutate()}
              disabled={refresh.isPending || !o?.configured}
              aria-label="Nu ophalen"
              title="Nu ophalen uit Zabbix"
            >
              {refresh.isPending ? "…" : "↻"}
            </Button>
          </div>
        }
      />
      {q.isLoading && <p className="text-sm text-slate-500">Laden…</p>}
      {q.isError && <Alert>Het Zabbix-overzicht laden mislukte. Het wordt zo opnieuw geprobeerd.</Alert>}
      {o && !o.configured && (
        <Alert kind="info">
          De Zabbix-API is nog niet ingesteld. Vul het adres van api_jsonrpc.php en een API-token in bij{" "}
          <Link href="/zabbix" className="font-medium underline">
            Zabbix → Agents
          </Link>
          ; daarna verschijnt hier binnen een minuut wat Zabbix ziet.
        </Alert>
      )}
      {o && o.configured && !o.updated_at && o.error && (
        <Alert>
          Zabbix is niet bereikbaar: {o.error}. Controleer het adres en het token bij{" "}
          <Link href="/zabbix" className="font-medium underline">
            Zabbix → Agents
          </Link>{" "}
          (knop API testen).
        </Alert>
      )}
      {o && o.configured && !o.updated_at && !o.error && <p className="text-sm text-slate-500">De eerste ronde loopt…</p>}
      {o && o.updated_at && children(o)}
    </div>
  );
}

// --- toestand ------------------------------------------------------------------------

export const levelStyle: Record<ZabbixLevel, { ring: string; dot: string; text: string; bg: string }> = {
  kritiek: { ring: "border-red-300 dark:border-red-900", dot: "bg-red-600", text: "text-red-700 dark:text-red-300", bg: "bg-red-50 dark:bg-red-950/40" },
  probleem: {
    ring: "border-orange-300 dark:border-orange-900",
    dot: "bg-orange-500",
    text: "text-orange-800 dark:text-orange-300",
    bg: "bg-orange-50 dark:bg-orange-950/40",
  },
  waarschuwing: {
    ring: "border-yellow-200 dark:border-yellow-900/60",
    dot: "bg-yellow-400",
    text: "text-yellow-800 dark:text-yellow-200",
    bg: "bg-yellow-50/60 dark:bg-yellow-950/20",
  },
  normaal: {
    ring: "border-emerald-200 dark:border-emerald-900",
    dot: "bg-emerald-500",
    text: "text-emerald-700 dark:text-emerald-300",
    bg: "bg-emerald-50/60 dark:bg-emerald-950/30",
  },
  onbekend: { ring: "border-slate-200 dark:border-slate-800", dot: "bg-slate-400", text: "text-slate-600 dark:text-slate-300", bg: "bg-slate-50 dark:bg-slate-900" },
};

export function StateBand({ o }: { o: ZabbixOverview }) {
  const l = (o.state.level as ZabbixLevel) in levelStyle ? (o.state.level as ZabbixLevel) : "onbekend";
  const st = levelStyle[l];
  const stale = o.error !== "";
  return (
    <section aria-label="Toestand van Zabbix" className={cx("flex flex-wrap items-center gap-x-4 gap-y-2 rounded-lg border px-4 py-3", st.ring, st.bg)}>
      <span className={cx("inline-flex items-center gap-2 text-sm font-bold uppercase tracking-widest", st.text)}>
        <span className="relative flex size-2.5" aria-hidden>
          {(l === "kritiek" || l === "probleem") && <span className={cx("absolute inline-flex size-full animate-ping rounded-full opacity-60", st.dot)} />}
          <span className={cx("relative inline-flex size-2.5 rounded-full", st.dot)} />
        </span>
        {levelLabel[l]}
      </span>
      <span className="order-last w-full min-w-0 text-sm text-slate-700 sm:order-none sm:w-auto sm:flex-1 dark:text-slate-300">{o.state.reasons.join(" · ")}</span>
      <span className={cx("ml-auto text-xs sm:ml-0", stale ? "text-red-600 dark:text-red-400" : "text-slate-500")} title={o.error || undefined}>
        Zabbix {o.version} · {stale ? "laatste ronde mislukt, stand van " : "bijgewerkt "}
        {o.updated_at && timeFmt.format(new Date(o.updated_at))}
      </span>
    </section>
  );
}

// --- kleine bouwstenen ------------------------------------------------------------------

export function SeverityBadge({ s }: { s: number }) {
  return (
    <span className={cx("inline-flex items-center rounded px-1.5 py-0.5 text-xs font-semibold whitespace-nowrap", severityStyle[s]?.badge)}>
      {severityLabel[s] ?? s}
    </span>
  );
}

export function AvailDot({ a, title }: { a: string; title?: string }) {
  const c = a === "ok" ? "bg-emerald-500" : a === "down" ? "bg-red-500" : a === "disabled" ? "bg-slate-300 dark:bg-slate-700" : "bg-slate-400";
  return <span title={title ?? availLabel[a] ?? a} aria-label={availLabel[a] ?? a} className={cx("inline-block size-2.5 shrink-0 rounded-full", c)} />;
}

export function Sparkline({ points, className = "text-brand-500" }: { points: number[]; className?: string }) {
  if (points.length < 2) return null;
  const w = 120;
  const h = 24;
  const min = Math.min(...points);
  const max = Math.max(...points);
  const span = max - min || 1;
  const xy = points.map((v, i) => [1 + (i * (w - 2)) / (points.length - 1), h - 2 - ((v - min) / span) * (h - 4)]);
  return (
    <svg viewBox={`0 0 ${w} ${h}`} width={w} height={h} className={cx("mt-2 max-w-full", className)} aria-hidden>
      <polyline fill="none" stroke="currentColor" strokeWidth={2} strokeLinejoin="round" strokeLinecap="round" points={xy.map((p) => p.join(",")).join(" ")} />
    </svg>
  );
}

export function Tile({
  href,
  label,
  value,
  sub,
  tone,
  children,
}: {
  href: string;
  label: string;
  value: string;
  sub: ReactNode;
  tone?: "red" | "amber" | "green";
  children?: ReactNode;
}) {
  return (
    <Link
      href={href}
      className="group flex min-w-0 flex-col rounded-lg border border-slate-200 bg-white p-4 shadow-xs transition hover:border-brand-500 dark:border-slate-800 dark:bg-slate-900"
    >
      <span className="text-xs font-medium uppercase tracking-wide text-slate-500">{label}</span>
      <span
        className={cx(
          "mt-1 text-2xl font-semibold tabular-nums",
          tone === "red" && "text-red-600 dark:text-red-400",
          tone === "amber" && "text-amber-600 dark:text-amber-400",
          tone === "green" && "text-emerald-600 dark:text-emerald-400",
        )}
      >
        {value}
      </span>
      <span className="mt-0.5 text-xs text-slate-500">{sub}</span>
      {children}
    </Link>
  );
}

// NodeName toont de ClusterForge-node bij een host, of de hostnaam.
export function HostLabel({ name, node }: { name: string; node: ZabbixHost["node"] }) {
  if (!node) return <span className="break-words">{name}</span>;
  return (
    <span className="min-w-0">
      <Link href={`/nodes/detail?id=${node.node_id}`} className="font-medium text-brand-700 hover:underline dark:text-sky-300">
        {node.hostname}
      </Link>
      {node.cluster_name && (
        <>
          {" "}
          <span className="text-slate-500">in</span>{" "}
          {node.cluster_id ? (
            <Link href={`/clusters/detail?id=${node.cluster_id}`} className="text-slate-600 hover:underline dark:text-slate-400">
              {node.cluster_name}
            </Link>
          ) : (
            node.cluster_name
          )}
        </>
      )}
      {name.toLowerCase() !== node.hostname.toLowerCase() && <span className="text-slate-500"> · {name}</span>}
    </span>
  );
}

// --- tegels -------------------------------------------------------------------------

export function Tiles({ o }: { o: ZabbixOverview }) {
  const c = o.counts;
  const bySev = countBySeverity(o);
  const open = severities.reduce<number>((a, s) => a + bySev[s], 0);
  const serious = c.disaster + c.high;
  const server = o.health.find((h) => h.nvps != null) ?? o.health[0];
  const queue = o.health.reduce((a, h) => a + (h.queue ?? 0), 0);
  const queue10 = o.health.reduce((a, h) => a + (h.queue_10m ?? 0), 0);
  const ha = o.ha.find((n) => n.status === "active");
  const offlineProxies = o.proxies.filter((p) => p.state === "offline").length;
  return (
    <div className="grid grid-cols-2 gap-3 md:grid-cols-3 xl:grid-cols-6">
      <Tile
        href="/zabbix/problemen"
        label="Open problemen"
        value={nf.format(open)}
        tone={c.disaster > 0 ? "red" : serious > 0 ? "red" : c.average > 0 ? "amber" : "green"}
        sub={serious > 0 ? `${serious} ramp of hoog · ${c.unacked} niet bevestigd` : `${c.unacked} niet bevestigd${c.suppressed ? ` · ${c.suppressed} onderdrukt` : ""}`}
      >
        <Sparkline points={o.trend.map((p) => p.disaster + p.high + p.average + p.warning + p.info)} />
      </Tile>
      <Tile
        href="/zabbix/hosts?filter=down"
        label="Hosts bereikbaar"
        value={`${c.hosts_ok}/${c.hosts}`}
        tone={c.hosts_down > 0 ? "red" : c.hosts_unknown > 0 ? "amber" : "green"}
        sub={c.hosts_down > 0 ? `${c.hosts_down} onbereikbaar` : c.hosts_unknown > 0 ? `${c.hosts_unknown} onbekend` : "alles bereikbaar"}
      >
        <Sparkline points={o.trend.map((p) => p.hosts_down)} className="text-red-500" />
      </Tile>
      <Tile
        href="/zabbix/hosts?filter=nodes"
        label="Nodes bewaakt"
        value={`${c.watched}/${c.nodes}`}
        tone={c.nodes > 0 && c.watched < c.nodes ? "amber" : "green"}
        sub={c.watched < c.nodes ? `${c.nodes - c.watched} ClusterForge-node${c.nodes - c.watched === 1 ? "" : "s"} zonder host` : "elke node staat in Zabbix"}
      />
      <Tile
        href="#zabbix-zelf"
        label="Nieuwe waarden/s"
        value={server?.nvps != null ? nf.format(server.nvps) : "—"}
        sub={server?.nvps != null ? "nodig volgens Zabbix" : "template Zabbix server health ontbreekt"}
      >
        <Sparkline points={o.trend.filter((p) => p.nvps != null).map((p) => p.nvps ?? 0)} />
      </Tile>
      <Tile
        href="#zabbix-zelf"
        label="Wachtrij"
        value={o.health.some((h) => h.queue != null) ? nf.format(queue) : "—"}
        tone={queue10 > 0 ? "amber" : undefined}
        sub={queue10 > 0 ? `${nf.format(queue10)} langer dan 10 min` : "items die op een check wachten"}
      >
        <Sparkline points={o.trend.filter((p) => p.queue != null).map((p) => p.queue ?? 0)} />
      </Tile>
      <Tile
        href="#zabbix-zelf"
        label="Server en proxy"
        value={
          o.ha.length > 0
            ? `${o.ha.filter((n) => n.status === "active" || n.status === "standby").length}/${o.ha.length}`
            : o.proxies.length > 0
              ? `${o.proxies.length - offlineProxies}/${o.proxies.length}`
              : "—"
        }
        tone={(o.ha.length > 0 && !ha) || offlineProxies > 0 ? "red" : "green"}
        sub={
          o.ha.length > 0
            ? `${ha ? `actief: ${ha.name}` : "geen actieve HA-node"} · ${offlineProxies > 0 ? `${offlineProxies} proxy offline` : `proxy's online`}`
            : o.proxies.length > 0
              ? "proxy's online"
              : "geen HA-gegevens"
        }
      />
    </div>
  );
}

// --- problemen over tijd ------------------------------------------------------------------

export function ProblemTrend({ o }: { o: ZabbixOverview }) {
  const [hover, setHover] = useState<number | null>(null);
  const t = o.trend;
  const fmt = o.window === "24h" ? timeFmt : dayFmt;
  const stack = [5, 4, 3, 2, 1] as const;
  const val = (p: (typeof t)[number], s: number) => (s === 5 ? p.disaster : s === 4 ? p.high : s === 3 ? p.average : s === 2 ? p.warning : p.info);
  const max = Math.max(1, ...t.map((p) => stack.reduce((a, s) => a + val(p, s), 0)));
  const h = hover == null ? null : t[hover];
  return (
    <Card title={`Open problemen over tijd (${windowLabel[o.window as ZabbixWindow] ?? o.window})`}>
      {t.length < 2 ? (
        <p className="text-sm text-slate-500">
          ClusterForge bewaart elk kwartier een momentopname. Na een half uur verschijnt hier de grafiek.
        </p>
      ) : (
        <div className="relative" onMouseLeave={() => setHover(null)}>
          <div className="flex h-40 items-end gap-px" role="img" aria-label="Aantal open problemen per ernst over tijd">
            {t.map((p, i) => (
              <div key={p.at} className="flex h-full min-w-0 flex-1 flex-col justify-end" onMouseEnter={() => setHover(i)}>
                {stack.map((s) => {
                  const v = val(p, s);
                  if (v === 0) return null;
                  return (
                    <div
                      key={s}
                      className={cx("w-full first:rounded-t-sm", hover != null && hover !== i && "opacity-50")}
                      style={{ height: `${(v / max) * 100}%`, background: severityStyle[s].bar }}
                    />
                  );
                })}
              </div>
            ))}
          </div>
          <div className="mt-1 flex justify-between text-xs text-slate-500">
            <span>{fmt.format(new Date(t[0].at))}</span>
            <span>nu</span>
          </div>
          <div className="mt-2 flex flex-wrap gap-x-4 gap-y-1 text-xs text-slate-600 dark:text-slate-400">
            {stack.map((s) => (
              <span key={s} className="inline-flex items-center gap-1.5">
                <span className="size-2.5 rounded-sm" style={{ background: severityStyle[s].bar }} aria-hidden />
                {severityLabel[s]}
              </span>
            ))}
          </div>
          {h && hover != null && (
            <div
              className="pointer-events-none absolute top-0 z-10 w-max -translate-x-1/2 rounded-md border border-slate-200 bg-white px-2.5 py-1.5 text-xs shadow-lg dark:border-slate-700 dark:bg-slate-900"
              style={{ left: `${Math.min(85, Math.max(15, ((hover + 0.5) / t.length) * 100))}%` }}
            >
              <div className="font-medium">{fullFmt.format(new Date(h.at))}</div>
              {stack.map((s) => (
                <div key={s} className="flex justify-between gap-4 text-slate-600 dark:text-slate-400">
                  <span>{severityLabel[s]}</span>
                  <span className="tabular-nums">{val(h, s)}</span>
                </div>
              ))}
              <div className="flex justify-between gap-4 text-slate-600 dark:text-slate-400">
                <span>Hosts onbereikbaar</span>
                <span className="tabular-nums">{h.hosts_down}</span>
              </div>
            </div>
          )}
        </div>
      )}
    </Card>
  );
}

// --- problemenlijst ----------------------------------------------------------------------

export function ProblemRow({ p, base, now }: { p: ZabbixProblem; base: string; now: number }) {
  const links = zabbixLinks(base);
  return (
    <li className={cx("flex items-start gap-3 rounded-md px-2 py-2 hover:bg-slate-50 dark:hover:bg-slate-800/60", p.suppressed && "opacity-60")}>
      <span className={cx("mt-1.5 size-2.5 shrink-0 rounded-full", severityStyle[p.severity]?.dot)} aria-hidden />
      <span className="min-w-0 flex-1">
        <span className="flex flex-wrap items-center gap-x-2 gap-y-1">
          <SeverityBadge s={p.severity} />
          <span className="text-sm font-medium break-words">{p.name}</span>
          {p.opdata && <span className="text-xs text-slate-500 tabular-nums">{p.opdata}</span>}
        </span>
        <span className="mt-0.5 block text-xs text-slate-600 dark:text-slate-400">
          {p.hosts.length === 0
            ? "geen host"
            : p.hosts.map((h, i) => (
                <span key={h.hostid}>
                  {i > 0 && ", "}
                  <HostLabel name={h.name} node={h.node} />
                </span>
              ))}
        </span>
        <span className="mt-0.5 flex flex-wrap gap-x-3 text-xs text-slate-500">
          <span title={fullFmt.format(new Date(p.since))}>sinds {since(p.since, now)}</span>
          {p.acknowledged ? <span className="text-emerald-700 dark:text-emerald-400">bevestigd</span> : <span>niet bevestigd</span>}
          {p.suppressed && <span>onderdrukt</span>}
          {p.tags.slice(0, 4).map((t) => (
            <span key={t.tag + t.value} className="rounded bg-slate-100 px-1 text-slate-600 dark:bg-slate-800 dark:text-slate-300">
              {t.tag}
              {t.value && `: ${t.value}`}
            </span>
          ))}
        </span>
      </span>
      <AckButton p={p} />
      {links && (
        <a
          href={links.problem(p)}
          target="_blank"
          rel="noreferrer"
          className="shrink-0 rounded-md border border-slate-300 px-2.5 py-1 text-xs font-medium text-slate-700 hover:bg-slate-50 dark:border-slate-700 dark:text-slate-200 dark:hover:bg-slate-800"
        >
          Zabbix ↗
        </a>
      )}
    </li>
  );
}

export function NowCard({ o, className }: { o: ZabbixOverview; className?: string }) {
  const [now] = useState(() => Date.now());
  const list = useMemo(() => o.problems.filter((p) => !p.suppressed && p.severity >= 2).slice(0, 8), [o.problems]);
  const more = o.problems.filter((p) => !p.suppressed).length - list.length;
  return (
    <Card title="Nu belangrijk" className={className}>
      {list.length === 0 ? (
        <p className="flex items-center gap-2 text-sm text-emerald-700 dark:text-emerald-300">
          <span className="size-2 rounded-full bg-emerald-500" aria-hidden />
          Geen open problemen vanaf ernst waarschuwing.
        </p>
      ) : (
        <ul className="-mx-2 space-y-1">
          {list.map((p) => (
            <ProblemRow key={p.eventid} p={p} base={o.frontend_url} now={now} />
          ))}
        </ul>
      )}
      {more > 0 && (
        <Link href="/zabbix/problemen" className="mt-2 inline-block text-sm font-medium text-brand-700 hover:underline dark:text-sky-300">
          Alle {o.problems.filter((p) => !p.suppressed).length} problemen →
        </Link>
      )}
    </Card>
  );
}

// --- hosts in één oogopslag ---------------------------------------------------------------

// hostColor: de zwaarste open probleem bepaalt de kleur; onbereikbaar telt als hoog.
function hostColor(h: ZabbixHost) {
  if (!h.enabled) return "#cbd5e1";
  const sev = Math.max(h.worst, h.available === "down" ? 4 : -1);
  if (sev >= 0) return severityStyle[sev].bar;
  if (h.maintenance) return "#a78bfa";
  if (h.available === "ok") return "#10b981";
  return "#94a3b8";
}

export function HostMap({ o, onlyNodes }: { o: ZabbixOverview; onlyNodes?: boolean }) {
  const hosts = o.hosts.filter((h) => h.enabled && (!onlyNodes || h.node));
  const groups = useMemo(() => {
    const m = new Map<string, ZabbixHost[]>();
    for (const h of hosts) {
      const k = h.node ? h.node.cluster_name || "Losse nodes" : "Overige hosts in Zabbix";
      m.set(k, [...(m.get(k) ?? []), h]);
    }
    return [...m.entries()].sort((a, b) => {
      const rank = (k: string) => (k === "Overige hosts in Zabbix" ? 2 : k === "Losse nodes" ? 1 : 0);
      return rank(a[0]) - rank(b[0]) || a[0].localeCompare(b[0], "nl");
    });
  }, [hosts]);
  const links = zabbixLinks(o.frontend_url);
  return (
    <Card title="Hosts in één oogopslag">
      {hosts.length === 0 ? (
        <p className="text-sm text-slate-500">Geen ingeschakelde hosts.</p>
      ) : (
        <div className="space-y-4">
          {groups.map(([g, list]) => (
            <div key={g}>
              <div className="mb-1.5 text-xs font-medium uppercase tracking-wide text-slate-500">
                {g} <span className="font-normal normal-case">({list.length})</span>
              </div>
              <div className="flex flex-wrap gap-1.5">
                {list.map((h) => {
                  const label = h.node?.hostname ?? h.name;
                  const title = `${label}: ${availLabel[h.available] ?? h.available}${h.problems > 0 ? `, ${h.problems} probleem${h.problems === 1 ? "" : "en"} (zwaarste: ${severityLabel[h.worst]})` : ""}${h.maintenance ? ", in onderhoud" : ""}`;
                  const inner = (
                    <span
                      className="flex h-9 min-w-9 max-w-44 items-center justify-center truncate rounded px-2 text-xs font-medium text-white shadow-xs"
                      style={{ background: hostColor(h) }}
                    >
                      {label}
                    </span>
                  );
                  return h.node ? (
                    <Link key={h.hostid} href={`/nodes/detail?id=${h.node.node_id}`} title={title} aria-label={title}>
                      {inner}
                    </Link>
                  ) : links ? (
                    <a key={h.hostid} href={links.host(h.hostid)} target="_blank" rel="noreferrer" title={title} aria-label={title}>
                      {inner}
                    </a>
                  ) : (
                    <span key={h.hostid} title={title}>
                      {inner}
                    </span>
                  );
                })}
              </div>
            </div>
          ))}
          <div className="flex flex-wrap gap-x-4 gap-y-1 text-xs text-slate-600 dark:text-slate-400">
            {[
              ["#10b981", "in orde"],
              [severityStyle[2].bar, "waarschuwing"],
              [severityStyle[3].bar, "gemiddeld"],
              [severityStyle[4].bar, "hoog of onbereikbaar"],
              [severityStyle[5].bar, "ramp"],
              ["#a78bfa", "onderhoud"],
              ["#94a3b8", "onbekend"],
            ].map(([c, l]) => (
              <span key={l} className="inline-flex items-center gap-1.5">
                <span className="size-2.5 rounded-sm" style={{ background: c }} aria-hidden />
                {l}
              </span>
            ))}
          </div>
        </div>
      )}
    </Card>
  );
}

// --- Zabbix zelf ---------------------------------------------------------------------------

function Meter({ label, v, warn = 70, bad = 90 }: { label: string; v: number | null | undefined; warn?: number; bad?: number }) {
  if (v == null) return null;
  const c = v >= bad ? "bg-red-500" : v >= warn ? "bg-amber-400" : "bg-emerald-500";
  return (
    <div>
      <div className="flex justify-between gap-2 text-xs text-slate-600 dark:text-slate-400">
        <span className="min-w-0 truncate">{label}</span>
        <span className="shrink-0 tabular-nums">{nf.format(v)} %</span>
      </div>
      <div className="mt-0.5 h-1.5 overflow-hidden rounded-full bg-slate-100 dark:bg-slate-800">
        <div className={cx("h-full rounded-full", c)} style={{ width: `${Math.min(100, Math.max(0, v))}%` }} />
      </div>
    </div>
  );
}

const haLabel: Record<string, { text: string; tone: string }> = {
  active: { text: "actief", tone: "text-emerald-700 dark:text-emerald-400" },
  standby: { text: "stand-by", tone: "text-slate-600 dark:text-slate-300" },
  stopped: { text: "gestopt", tone: "text-amber-700 dark:text-amber-300" },
  unavailable: { text: "onbereikbaar", tone: "text-red-600 dark:text-red-400" },
};

export function ZabbixSelf({ o, className }: { o: ZabbixOverview; className?: string }) {
  const [now] = useState(() => Date.now());
  return (
    <section id="zabbix-zelf" className={cx("scroll-mt-20", className)}>
      <Card title="Zabbix zelf">
        <div className="space-y-5">
          <div>
            <h3 className="mb-1.5 text-xs font-medium uppercase tracking-wide text-slate-500">HA-cluster van de server</h3>
            {o.ha_error ? (
              <p className="text-sm text-slate-500">Niet te zien: {o.ha_error}. Een token van een Super admin toont dit wel.</p>
            ) : o.ha.length === 0 ? (
              <p className="text-sm text-slate-500">Geen HA-cluster (één server).</p>
            ) : (
              <ul className="space-y-1 text-sm">
                {o.ha.map((n) => (
                  <li key={n.name} className="flex flex-wrap items-center justify-between gap-x-3">
                    <span className="min-w-0">
                      <span className="font-medium">{n.name}</span> <span className="font-mono text-xs text-slate-500">{n.address}</span>
                    </span>
                    <span className={cx("text-xs font-medium", haLabel[n.status]?.tone)}>
                      {haLabel[n.status]?.text ?? n.status}
                      {n.last_seen && n.status !== "active" && <span className="font-normal text-slate-500"> · gezien {since(n.last_seen, now)} geleden</span>}
                    </span>
                  </li>
                ))}
              </ul>
            )}
          </div>
          <div>
            <h3 className="mb-1.5 text-xs font-medium uppercase tracking-wide text-slate-500">Proxy&apos;s</h3>
            {o.proxies_error ? (
              <p className="text-sm text-slate-500">Niet te zien: {o.proxies_error}</p>
            ) : o.proxies.length === 0 ? (
              <p className="text-sm text-slate-500">Geen proxy&apos;s.</p>
            ) : (
              <ul className="space-y-1 text-sm">
                {o.proxies.map((p) => (
                  <li key={p.proxyid} className="flex flex-wrap items-center justify-between gap-x-3">
                    <span className="inline-flex min-w-0 items-center gap-2">
                      <AvailDot a={p.state === "online" ? "ok" : p.state === "offline" ? "down" : "unknown"} title={p.state} />
                      <span className="font-medium">{p.name}</span>
                      <span className="text-xs text-slate-500">
                        {p.mode === "passive" ? "passief" : "actief"} · {p.hosts} host{p.hosts === 1 ? "" : "s"}
                      </span>
                    </span>
                    <span className="text-xs text-slate-500">
                      {p.version && (
                        <span className={p.compatibility === "outdated" || p.compatibility === "unsupported" ? "text-amber-700 dark:text-amber-300" : undefined}>
                          {p.version}
                          {p.compatibility === "outdated" && " (verouderd)"}
                          {p.compatibility === "unsupported" && " (niet ondersteund)"}
                        </span>
                      )}
                      {p.last_seen && <> · {since(p.last_seen, now)} geleden</>}
                    </span>
                  </li>
                ))}
              </ul>
            )}
          </div>
          <div>
            <h3 className="mb-1.5 text-xs font-medium uppercase tracking-wide text-slate-500">Gezondheid</h3>
            {o.health_error ? (
              <p className="text-sm text-slate-500">Niet te zien: {o.health_error}</p>
            ) : o.health.length === 0 ? (
              <p className="text-sm text-slate-500">
                Geen gezondheidsitems. Koppel de templates <em>Zabbix server health</em> en <em>Zabbix proxy health</em> aan de server en de proxy om
                wachtrij, caches en drukke processen te zien.
              </p>
            ) : (
              <div className="grid gap-4 sm:grid-cols-2">
                {o.health.map((h) => (
                  <div key={h.hostid} className="min-w-0 space-y-2 rounded-md border border-slate-200 p-3 dark:border-slate-800">
                    <div className="flex flex-wrap justify-between gap-x-2 text-sm">
                      <span className="font-medium">{h.name}</span>
                      <span className="text-xs text-slate-500 tabular-nums">
                        {h.nvps != null && <>{nf.format(h.nvps)} nvps · </>}
                        {h.queue != null && <>wachtrij {nf.format(h.queue)}</>}
                        {h.queue_10m != null && h.queue_10m > 0 && <span className="text-amber-700 dark:text-amber-300"> ({nf.format(h.queue_10m)} &gt; 10 min)</span>}
                      </span>
                    </div>
                    <Meter label="Config-cache" v={h.cache_config} />
                    <Meter label="History-cache" v={h.cache_history} />
                    <Meter label="Value-cache" v={h.cache_value} />
                    {h.busiest.slice(0, 3).map((p) => (
                      <Meter key={p.name} label={p.name} v={p.busy} warn={60} bad={85} />
                    ))}
                  </div>
                ))}
              </div>
            )}
          </div>
        </div>
      </Card>
    </section>
  );
}

// --- dekking ----------------------------------------------------------------------------------

export function CoverageCard({ o }: { o: ZabbixOverview }) {
  const others = o.hosts.filter((h) => h.enabled && !h.node).length;
  return (
    <Card title="Dekking">
      <p className="mb-3 text-sm text-slate-600 dark:text-slate-400">
        {o.counts.watched} van de {o.counts.nodes} ClusterForge-nodes staan in Zabbix. Daarnaast bewaakt Zabbix {others} andere host{others === 1 ? "" : "s"}{" "}
        (bv. firewalls, Proxmox-hosts of switches).
      </p>
      {o.unwatched.length === 0 ? (
        <p className="flex items-center gap-2 text-sm text-emerald-700 dark:text-emerald-300">
          <span className="size-2 rounded-full bg-emerald-500" aria-hidden />
          Elke ClusterForge-node heeft een host in Zabbix.
        </p>
      ) : (
        <>
          <ul className="divide-y divide-slate-100 text-sm dark:divide-slate-800">
            {o.unwatched.map((n) => (
              <li key={n.node_id} className="flex flex-wrap items-center justify-between gap-x-3 py-1.5">
                <span className="min-w-0">
                  <Link href={`/nodes/detail?id=${n.node_id}`} className="font-medium text-brand-700 hover:underline dark:text-sky-300">
                    {n.hostname}
                  </Link>
                  {n.cluster_name && <span className="text-slate-500"> in {n.cluster_name}</span>}
                </span>
                <span className="font-mono text-xs text-slate-500">{n.ip}</span>
              </li>
            ))}
          </ul>
          <p className="mt-3 text-xs text-slate-500">
            Zet zabbix-agent2 op deze nodes via{" "}
            <Link href="/zabbix" className="font-medium text-brand-700 hover:underline dark:text-sky-300">
              Zabbix → Agents
            </Link>
            , of koppel een bestaande host met de hand bij{" "}
            <Link href="/zabbix/hosts?filter=other" className="font-medium text-brand-700 hover:underline dark:text-sky-300">
              Hosts
            </Link>
            .
          </p>
        </>
      )}
    </Card>
  );
}
