"use client";

import { useRouter, useSearchParams } from "next/navigation";
import { Suspense, useMemo, useState } from "react";
import { Card, Input, Select, cx } from "@/components/ui";
import { ProblemRow, ZabbixPage } from "@/components/zabbix/Zabbix";
import { countBySeverity, severities, severityLabel, severityStyle, type ZabbixOverview } from "@/lib/zabbixMonitoring";

export default function ZabbixProblemsPage() {
  return (
    <Suspense>
      <ZabbixPage
        title="Zabbix-problemen"
        description="Alle open problemen in Zabbix, met de ClusterForge-node en het cluster erbij. Zwaarste eerst, daarna de oudste."
      >
        {(o) => <Problems o={o} />}
      </ZabbixPage>
    </Suspense>
  );
}

function Problems({ o }: { o: ZabbixOverview }) {
  const params = useSearchParams();
  const router = useRouter();
  const [now] = useState(() => Date.now());
  const min = Number(params.get("ernst") ?? "0");
  const cluster = params.get("cluster") ?? "";
  const scope = params.get("hosts") ?? "";
  const unacked = params.get("open") === "1";
  const suppressed = params.get("onderdrukt") === "1";
  const [search, setSearch] = useState(params.get("q") ?? "");

  const set = (k: string, v: string) => {
    const next = new URLSearchParams(params.toString());
    if (v) next.set(k, v);
    else next.delete(k);
    const qs = next.toString();
    router.replace(qs ? `/zabbix/problemen?${qs}` : "/zabbix/problemen", { scroll: false });
  };

  const clusters = useMemo(() => {
    const s = new Set<string>();
    for (const p of o.problems) for (const h of p.hosts) if (h.node?.cluster_name) s.add(h.node.cluster_name);
    return [...s].sort((a, b) => a.localeCompare(b, "nl"));
  }, [o.problems]);

  const q = search.trim().toLowerCase();
  const list = o.problems.filter((p) => {
    if (p.severity < min) return false;
    if (!suppressed && p.suppressed) return false;
    if (unacked && p.acknowledged) return false;
    if (cluster && !p.hosts.some((h) => h.node?.cluster_name === cluster)) return false;
    if (scope === "nodes" && !p.hosts.some((h) => h.node)) return false;
    if (scope === "other" && p.hosts.some((h) => h.node)) return false;
    if (q) {
      const hay = [p.name, p.opdata, ...p.hosts.flatMap((h) => [h.name, h.node?.hostname ?? "", h.node?.cluster_name ?? ""]), ...p.tags.map((t) => `${t.tag}:${t.value}`)]
        .join(" ")
        .toLowerCase();
      if (!hay.includes(q)) return false;
    }
    return true;
  });
  const bySev = countBySeverity(o);

  return (
    <>
      <div className="flex flex-wrap gap-2" role="group" aria-label="Vanaf ernst">
        {severities.map((s) => (
          <button
            key={s}
            onClick={() => set("ernst", min === s ? "" : String(s))}
            aria-pressed={min === s}
            className={cx(
              "inline-flex items-center gap-2 rounded-md border px-3 py-1.5 text-sm",
              min === s ? "border-brand-500 bg-brand-50 dark:bg-slate-800" : "border-slate-200 bg-white hover:border-brand-500 dark:border-slate-800 dark:bg-slate-900",
            )}
            title={`Alleen vanaf ${severityLabel[s].toLowerCase()}`}
          >
            <span className={cx("size-2.5 rounded-full", severityStyle[s].dot)} aria-hidden />
            {severityLabel[s]}
            <span className="font-semibold tabular-nums">{bySev[s]}</span>
          </button>
        ))}
      </div>
      <Card>
        <div className="mb-3 grid gap-2 sm:grid-cols-2 lg:grid-cols-4">
          <Input type="search" placeholder="Zoeken op naam, host, tag…" value={search} onChange={(e) => setSearch(e.target.value)} aria-label="Zoeken" />
          <Select value={cluster} onChange={(e) => set("cluster", e.target.value)} aria-label="Cluster">
            <option value="">Alle clusters</option>
            {clusters.map((c) => (
              <option key={c} value={c}>
                {c}
              </option>
            ))}
          </Select>
          <Select value={scope} onChange={(e) => set("hosts", e.target.value)} aria-label="Hosts">
            <option value="">Alle hosts</option>
            <option value="nodes">Alleen ClusterForge-nodes</option>
            <option value="other">Alleen andere hosts</option>
          </Select>
          <div className="flex flex-wrap items-center gap-x-4 gap-y-1 text-sm">
            <label className="inline-flex items-center gap-2">
              <input type="checkbox" checked={unacked} onChange={(e) => set("open", e.target.checked ? "1" : "")} className="size-4 rounded border-slate-300" />
              Niet bevestigd
            </label>
            <label className="inline-flex items-center gap-2">
              <input type="checkbox" checked={suppressed} onChange={(e) => set("onderdrukt", e.target.checked ? "1" : "")} className="size-4 rounded border-slate-300" />
              Onderdrukte
            </label>
          </div>
        </div>
        <p className="mb-2 text-xs text-slate-500">
          {list.length} van de {o.problems.length} problemen
          {o.problems.length >= 2000 && " (Zabbix gaf er meer; de oudste ontbreken)"}
        </p>
        {list.length === 0 ? (
          <p className="text-sm text-slate-500">{o.problems.length === 0 ? "Geen open problemen in Zabbix." : "Niets met deze filters."}</p>
        ) : (
          <ul className="-mx-2 space-y-1">
            {list.map((p) => (
              <ProblemRow key={p.eventid} p={p} base={o.frontend_url} now={now} />
            ))}
          </ul>
        )}
      </Card>
    </>
  );
}
