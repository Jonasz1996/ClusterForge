"use client";

import Link from "next/link";
import { ago, Empty, QueryState, StatusBadge } from "@/components/inventory/bits";
import { bySeverity, ClusterHealth } from "@/components/monitoring/ClusterHealth";
import { Alert, Card, PageHeader } from "@/components/ui";
import { statuses, useClusters, useInfo, useNodes, type Status } from "@/lib/inventory";

const order: Status[] = ["healthy", "degraded", "down", "split_brain", "unknown"];

type Problem = { key: string; href: string; name: string; kind: string; status: Status; reason: string; since: string | null };

export default function MonitoringPage() {
  const clusters = useClusters();
  const nodes = useNodes(true);
  const info = useInfo();

  const problems: Problem[] = [
    ...(clusters.data ?? [])
      .filter((c) => c.status !== "healthy" && c.status !== "unknown")
      .map((c) => ({
        key: c.id, href: `/clusters/detail?id=${c.id}`, name: c.name, kind: "Cluster",
        status: c.status, reason: c.status_reason, since: c.status_since,
      })),
    ...(nodes.data ?? [])
      .filter((n) => n.status !== "healthy" && n.status !== "unknown")
      .map((n) => ({
        key: n.id, href: `/nodes/detail?id=${n.id}`, name: n.hostname, kind: n.cluster_name ? `Node in ${n.cluster_name}` : "Node",
        status: n.status, reason: n.status_reason, since: n.status_since,
      })),
  ].sort(bySeverity);
  const withoutAgent = (nodes.data ?? []).filter((n) => !n.agent).length;

  return (
    <div className="space-y-6">
      <PageHeader title="Monitoring" description="Status van clusters en nodes, live bijgewerkt." />

      {info.data && !info.data.metrics_enabled && (
        <Alert kind="info">
          Er is geen VictoriaMetrics ingesteld, dus de grafieken bij nodes en clusters staan uit. De status hieronder werkt wel.
        </Alert>
      )}

      <div className="grid gap-4 sm:grid-cols-2">
        <Counts title="Clusters" items={clusters.data} />
        <Counts title="Nodes" items={nodes.data?.filter((n) => n.agent)} />
      </div>

      <Card title="Problemen">
        <QueryState q={nodes}>
          {problems.length === 0 ? (
            <p className="text-sm text-slate-500">Geen problemen. Alles met een agent is gezond.</p>
          ) : (
            <ul className="divide-y divide-slate-100 dark:divide-slate-800">
              {problems.map((p) => (
                <li key={p.key} className="flex flex-wrap items-start justify-between gap-x-4 gap-y-1 py-2.5 text-sm">
                  <div className="min-w-0">
                    <Link href={p.href} className="font-medium text-brand-700 hover:underline dark:text-sky-300">
                      {p.name}
                    </Link>
                    <span className="text-slate-500"> · {p.kind}</span>
                    <div className="text-slate-600 dark:text-slate-400">{p.reason}</div>
                  </div>
                  <div className="flex items-center gap-3">
                    {p.since && <span className="text-xs text-slate-500">{ago(p.since)}</span>}
                    <StatusBadge status={p.status} />
                  </div>
                </li>
              ))}
            </ul>
          )}
          {withoutAgent > 0 && (
            <p className="mt-3 text-xs text-slate-500">
              {withoutAgent} {withoutAgent === 1 ? "node heeft" : "nodes hebben"} nog geen agent en {withoutAgent === 1 ? "telt" : "tellen"}{" "}
              niet mee.{" "}
              <Link href="/nodes" className="underline">
                Naar Nodes
              </Link>
            </p>
          )}
        </QueryState>
      </Card>

      <section className="space-y-3">
        <h2 className="text-lg font-semibold">Clusters</h2>
        <QueryState q={clusters}>
          {clusters.data && clusters.data.length > 0 ? (
            <ClusterHealth clusters={clusters.data} />
          ) : (
            <Empty>Nog geen clusters.</Empty>
          )}
        </QueryState>
      </section>
    </div>
  );
}

function Counts({ title, items }: { title: string; items?: { status: Status }[] }) {
  return (
    <div className="rounded-lg border border-slate-200 bg-white px-4 py-3 dark:border-slate-800 dark:bg-slate-900">
      <div className="text-xs font-medium uppercase tracking-wide text-slate-500">{title}</div>
      {items ? (
        <div className="mt-2 flex flex-wrap gap-x-4 gap-y-1 text-sm">
          {order
            .filter((s) => items.some((i) => i.status === s))
            .map((s) => (
              <span key={s} className="inline-flex items-center gap-1.5">
                <span className="text-lg font-semibold tabular-nums">{items.filter((i) => i.status === s).length}</span>
                <span className="text-slate-600 dark:text-slate-400">{statuses[s].label.toLowerCase()}</span>
              </span>
            ))}
          {items.length === 0 && <span className="text-slate-500">Nog niets om te tonen</span>}
        </div>
      ) : (
        <div className="mt-2 text-sm text-slate-500">…</div>
      )}
    </div>
  );
}
