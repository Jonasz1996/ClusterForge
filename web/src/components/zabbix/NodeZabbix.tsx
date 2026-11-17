"use client";

import Link from "next/link";
import { useMemo, useState } from "react";
import { TimeChart } from "@/components/charts/TimeChart";
import { Tile } from "@/components/Tiles";
import { Card, Input, Select, cx } from "@/components/ui";
import { AvailDot, ProblemRow, SeverityBadge } from "@/components/zabbix/Zabbix";
import { ApiError } from "@/lib/api/client";
import {
  availLabel,
  formatItem,
  formatZabbix,
  pickKeyItems,
  rangeLabel,
  severityLabel,
  sourceLabel,
  useZabbixHostItems,
  useZabbixItemSeries,
  useZabbixOverview,
  zabbixLinks,
  type ZabbixHost,
  type ZabbixItem,
  type ZabbixRange,
} from "@/lib/zabbixMonitoring";

const ranges: ZabbixRange[] = ["1h", "6h", "24h", "7d", "30d"];

export function RangePicker({ value, onChange }: { value: ZabbixRange; onChange: (r: ZabbixRange) => void }) {
  return (
    <div role="group" aria-label="Periode" className="inline-flex rounded-md border border-slate-300 p-0.5 dark:border-slate-700">
      {ranges.map((r) => (
        <button
          key={r}
          onClick={() => onChange(r)}
          aria-pressed={value === r}
          className={cx(
            "rounded px-2 py-0.5 text-xs font-medium",
            value === r ? "bg-brand-600 text-white" : "text-slate-600 hover:bg-slate-100 dark:text-slate-300 dark:hover:bg-slate-800",
          )}
        >
          {rangeLabel[r]}
        </button>
      ))}
    </div>
  );
}

// ItemChart is de grafiek van één Zabbix-item. Bij trends (langer dan een
// dag) komen minimum en maximum erbij.
export function ItemChart({ itemid, range, height = 160, title }: { itemid: string; range: ZabbixRange; height?: number; title?: string }) {
  const q = useZabbixItemSeries(itemid, range);
  const s = q.data;
  const series = useMemo(() => {
    if (!s) return [];
    const out = [{ label: s.source === "trends" ? "gemiddeld" : "waarde", values: s.avg }];
    // Trends hebben per uur een minimum en maximum; die tonen we erbij.
    if (s.source === "trends") out.push({ label: "max", values: s.max }, { label: "min", values: s.min });
    return out;
  }, [s]);
  const units = s?.units ?? "";
  const format = useMemo(() => (v: number) => formatZabbix(v, units), [units]);
  return (
    <div className="min-w-0">
      {title && <div className="mb-1 text-xs font-medium text-slate-600 dark:text-slate-400">{title}</div>}
      {q.isLoading && <div className="flex items-center justify-center text-xs text-slate-500" style={{ height }}>Laden…</div>}
      {q.isError && (
        <div className="flex items-center justify-center text-xs text-red-600 dark:text-red-400" style={{ height }}>
          {q.error instanceof ApiError ? q.error.message : "Grafiek laden mislukte."}
        </div>
      )}
      {s && (
        <TimeChart
          timestamps={s.timestamps}
          series={series}
          unit={units === "%" ? "percent" : "count"}
          height={height}
          format={format}
          formatKey={units}
        />
      )}
    </div>
  );
}

// hostOfNode zoekt de Zabbix-host bij een node in de laatste stand.
function useNodeHost(nodeId: string) {
  const q = useZabbixOverview("24h");
  const o = q.data;
  const host = o?.hosts.find((h) => h.node?.node_id === nodeId) ?? null;
  return { o, host };
}

export function NodeZabbixTile({ nodeId }: { nodeId: string }) {
  const { o, host } = useNodeHost(nodeId);
  if (!o || !o.configured || !o.updated_at) return null;
  return (
    <Tile wide tall>
      <Card title="Zabbix" summary={<NodeZabbixSummary host={host} />}>
        {host ? (
          <NodeZabbix host={host} base={o.frontend_url} problems={o.problems.filter((p) => p.hosts.some((h) => h.hostid === host.hostid))} />
        ) : (
          <p className="text-sm text-slate-600 dark:text-slate-400">
            Deze node staat niet in Zabbix (of ClusterForge vindt de host niet). Zet zabbix-agent2 erop via{" "}
            <Link href="/zabbix" className="font-medium text-brand-700 hover:underline dark:text-sky-300">
              Zabbix → Agents
            </Link>
            , of koppel een bestaande host bij{" "}
            <Link href="/zabbix/hosts?filter=other" className="font-medium text-brand-700 hover:underline dark:text-sky-300">
              Zabbix → Hosts
            </Link>
            .
          </p>
        )}
      </Card>
    </Tile>
  );
}

function NodeZabbixSummary({ host }: { host: ZabbixHost | null }) {
  if (!host) return <p className="text-sm text-slate-500">Niet in Zabbix.</p>;
  return (
    <div className="flex flex-wrap items-center gap-2 text-sm">
      <AvailDot a={host.available} />
      <span>{availLabel[host.available]}</span>
      {host.problems > 0 ? (
        <>
          <span className="text-slate-400">·</span>
          <SeverityBadge s={host.worst} />
          <span>
            {host.problems} probleem{host.problems === 1 ? "" : "en"}
          </span>
        </>
      ) : (
        <span className="text-slate-500">· geen problemen</span>
      )}
    </div>
  );
}

function NodeZabbix({ host, base, problems }: { host: ZabbixHost; base: string; problems: import("@/lib/zabbixMonitoring").ZabbixProblem[] }) {
  const [now] = useState(() => Date.now());
  const [range, setRange] = useState<ZabbixRange>("24h");
  const items = useZabbixHostItems(host.hostid);
  const key = useMemo(() => pickKeyItems(items.data?.items ?? []), [items.data]);
  const links = zabbixLinks(base);
  const open = problems.filter((p) => !p.suppressed);
  return (
    <div className="space-y-5">
      <div className="flex flex-wrap items-center justify-between gap-2 text-sm">
        <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
          <span className="inline-flex items-center gap-1.5">
            <AvailDot a={host.available} />
            {availLabel[host.available]}
          </span>
          <span className="text-slate-500">
            host <span className="font-mono">{host.host}</span>
            {host.proxy ? ` via ${host.proxy}` : " via de server"}
            {host.node && ` · ${sourceLabel[host.node.source] ?? host.node.source}`}
          </span>
          {host.maintenance && <span className="text-violet-600 dark:text-violet-300">in onderhoud</span>}
        </div>
        {links && (
          <a href={links.host(host.hostid)} target="_blank" rel="noreferrer" className="text-sm font-medium text-brand-700 hover:underline dark:text-sky-300">
            Open in Zabbix ↗
          </a>
        )}
      </div>
      {host.interfaces.some((i) => i.error) && (
        <p className="text-xs break-words text-red-600 dark:text-red-400">{host.interfaces.find((i) => i.error)?.error}</p>
      )}

      <div>
        <h3 className="mb-1.5 text-xs font-medium uppercase tracking-wide text-slate-500">Open problemen</h3>
        {open.length === 0 ? (
          <p className="text-sm text-emerald-700 dark:text-emerald-300">Geen open problemen.</p>
        ) : (
          <ul className="-mx-2 space-y-1">
            {open.map((p) => (
              <ProblemRow key={p.eventid} p={p} base={base} now={now} />
            ))}
          </ul>
        )}
      </div>

      {key.length > 0 && (
        <div>
          <div className="mb-2 flex flex-wrap items-center justify-between gap-2">
            <h3 className="text-xs font-medium uppercase tracking-wide text-slate-500">Grafieken uit Zabbix</h3>
            <RangePicker value={range} onChange={setRange} />
          </div>
          <div className="grid gap-4 md:grid-cols-2">
            {key.map((k) => (
              <ItemChart key={k.item.itemid} itemid={k.item.itemid} range={range} title={`${k.label} · ${k.item.name}`} height={140} />
            ))}
          </div>
        </div>
      )}

      <LatestData hostid={host.hostid} items={items.data?.items} loading={items.isLoading} error={items.error} truncated={items.data?.truncated} />
    </div>
  );
}

function LatestData({
  items,
  loading,
  error,
  truncated,
}: {
  hostid: string;
  items?: ZabbixItem[];
  loading: boolean;
  error: unknown;
  truncated?: boolean;
}) {
  const [search, setSearch] = useState("");
  const [comp, setComp] = useState("");
  const [open, setOpen] = useState<string | null>(null);
  const [range, setRange] = useState<ZabbixRange>("24h");
  const comps = useMemo(() => [...new Set((items ?? []).map((i) => i.component).filter(Boolean))].sort(), [items]);
  const q = search.trim().toLowerCase();
  const list = (items ?? []).filter((i) => (!comp || i.component === comp) && (!q || `${i.name} ${i.key}`.toLowerCase().includes(q)));
  const shown = list.slice(0, 200);
  return (
    <div>
      <h3 className="mb-1.5 text-xs font-medium uppercase tracking-wide text-slate-500">Laatste waarden</h3>
      {loading && <p className="text-sm text-slate-500">Laden…</p>}
      {error != null && <p className="text-sm text-red-600 dark:text-red-400">{error instanceof ApiError ? error.message : "Laden mislukte."}</p>}
      {items && (
        <>
          <div className="mb-2 grid gap-2 sm:grid-cols-2">
            <Input type="search" placeholder="Zoeken op naam of sleutel…" value={search} onChange={(e) => setSearch(e.target.value)} aria-label="Items zoeken" />
            <Select value={comp} onChange={(e) => setComp(e.target.value)} aria-label="Onderdeel">
              <option value="">Alle onderdelen</option>
              {comps.map((c) => (
                <option key={c} value={c}>
                  {c}
                </option>
              ))}
            </Select>
          </div>
          <p className="mb-1 text-xs text-slate-500">
            {list.length} van de {items.length} items{list.length > shown.length && `, de eerste ${shown.length} getoond`}
            {truncated && " (de host heeft er meer)"}. Klik een getal voor de grafiek.
          </p>
          <ul className="divide-y divide-slate-100 text-sm dark:divide-slate-800">
            {shown.map((i) => (
              <li key={i.itemid} className="py-1.5">
                <button
                  className={cx("flex w-full items-baseline justify-between gap-3 text-left", i.numeric && !i.error ? "hover:text-brand-700 dark:hover:text-sky-300" : "cursor-default")}
                  onClick={() => i.numeric && !i.error && setOpen(open === i.itemid ? null : i.itemid)}
                  aria-expanded={i.numeric ? open === i.itemid : undefined}
                >
                  <span className="min-w-0">
                    <span className="block break-words">{i.name}</span>
                    <span className="block truncate font-mono text-xs text-slate-500">{i.key}</span>
                  </span>
                  <span className="max-w-[45%] shrink-0 text-right">
                    {i.error ? (
                      <span className="text-xs text-red-600 dark:text-red-400">{i.error}</span>
                    ) : (
                      <span className={cx("break-words", i.numeric && "tabular-nums")}>{formatItem(i) || <span className="text-slate-400">—</span>}</span>
                    )}
                  </span>
                </button>
                {open === i.itemid && (
                  <div className="mt-2">
                    <div className="mb-1 flex justify-end">
                      <RangePicker value={range} onChange={setRange} />
                    </div>
                    <ItemChart itemid={i.itemid} range={range} />
                  </div>
                )}
              </li>
            ))}
          </ul>
        </>
      )}
    </div>
  );
}

// ClusterZabbixTile: per node van het cluster de toestand in Zabbix en de
// open problemen samen.
export function ClusterZabbixTile({ clusterId }: { clusterId: string }) {
  const q = useZabbixOverview("24h");
  const o = q.data;
  const [now] = useState(() => Date.now());
  if (!o || !o.configured || !o.updated_at) return null;
  const hosts = o.hosts.filter((h) => h.node?.cluster_id === clusterId);
  const missing = o.unwatched.filter((n) => n.cluster_id === clusterId);
  if (hosts.length === 0 && missing.length === 0) return null;
  const ids = new Set(hosts.map((h) => h.hostid));
  const problems = o.problems.filter((p) => !p.suppressed && p.hosts.some((h) => ids.has(h.hostid)));
  const worst = Math.max(-1, ...hosts.map((h) => h.worst));
  return (
    <Tile wide>
      <Card
        title="Zabbix"
        summary={
          <p className="text-sm">
            {hosts.filter((h) => h.available === "ok").length}/{hosts.length + missing.length} nodes bereikbaar in Zabbix ·{" "}
            {problems.length === 0 ? "geen problemen" : `${problems.length} probleem${problems.length === 1 ? "" : "en"}, zwaarste ${severityLabel[worst]?.toLowerCase()}`}
          </p>
        }
      >
        <ul className="mb-4 flex flex-wrap gap-2">
          {hosts.map((h) => (
            <li key={h.hostid}>
              <Link
                href={`/nodes/detail?id=${h.node!.node_id}`}
                className="inline-flex items-center gap-2 rounded-md border border-slate-200 px-2.5 py-1.5 text-sm hover:border-brand-500 dark:border-slate-800"
                title={`${availLabel[h.available]}${h.problems ? `, ${h.problems} probleem${h.problems === 1 ? "" : "en"}` : ""}`}
              >
                <AvailDot a={h.available} />
                {h.node!.hostname}
                {h.problems > 0 && <SeverityBadge s={h.worst} />}
              </Link>
            </li>
          ))}
          {missing.map((n) => (
            <li key={n.node_id}>
              <Link
                href={`/nodes/detail?id=${n.node_id}`}
                className="inline-flex items-center gap-2 rounded-md border border-dashed border-slate-300 px-2.5 py-1.5 text-sm text-slate-500 dark:border-slate-700"
                title="Niet in Zabbix"
              >
                {n.hostname} · niet in Zabbix
              </Link>
            </li>
          ))}
        </ul>
        {problems.length === 0 ? (
          <p className="text-sm text-emerald-700 dark:text-emerald-300">Geen open problemen op de nodes van dit cluster.</p>
        ) : (
          <ul className="-mx-2 space-y-1">
            {problems.map((p) => (
              <ProblemRow key={p.eventid} p={p} base={o.frontend_url} now={now} />
            ))}
          </ul>
        )}
      </Card>
    </Tile>
  );
}
