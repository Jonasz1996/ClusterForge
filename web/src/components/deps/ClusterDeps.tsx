"use client";

import Link from "next/link";
import { useMemo, useState } from "react";
import { Empty, QueryState, tableClass, tdClass, thClass } from "@/components/inventory/bits";
import { Alert, Badge, Button, Card, cx } from "@/components/ui";
import { indexGraph, kindLabel, label, sources, strengthLabel, useDepGraph, useUpdateService, type DepEdge, type Index } from "@/lib/deps";
import { DependencyDialog, ServiceDialog } from "./Dialogs";
import { ServiceStatusText, StatusDot } from "./Panel";

// ClusterDepsCard is de kaart Diensten en afhankelijkheden op clusterdetail:
// de diensten van het cluster, de pijlen naar en van andere clusters, en de
// voorstellen van dit cluster.
export function ClusterDepsCard({ clusterId, isAdmin }: { clusterId: string; isAdmin: boolean }) {
  const graph = useDepGraph({ cluster_id: clusterId, include_suggested: true });
  const idx = useMemo(() => indexGraph(graph.data), [graph.data]);
  const update = useUpdateService();
  const [dialog, setDialog] = useState<"" | "service" | "dependency">("");
  const err = update.error instanceof Error ? update.error.message : null;

  const own = (graph.data?.services ?? []).filter((s) => s.cluster_id === clusterId);
  const ownIds = new Set(own.map((s) => s.id));
  const edges = graph.data?.edges ?? [];
  const uses = edges.filter((e) => ownIds.has(e.from) && !ownIds.has(e.to));
  const usedBy = edges.filter((e) => ownIds.has(e.to) && !ownIds.has(e.from));
  const confirmed = own.filter((s) => s.state === "confirmed");
  const groupId = own[0]?.group_id ?? graph.data?.groups.find((g) => g.cluster_id === clusterId)?.id;

  return (
    <Card
      title={
        <span className="flex flex-wrap items-center justify-between gap-2">
          <span>Diensten en afhankelijkheden</span>
          <span className="flex flex-wrap gap-2 text-sm font-normal">
            {isAdmin && (
              <>
                <Button variant="secondary" className="px-2.5 py-1" onClick={() => setDialog("service")}>
                  Dienst toevoegen
                </Button>
                {confirmed.length > 0 && (
                  <Button variant="secondary" className="px-2.5 py-1" onClick={() => setDialog("dependency")}>
                    Afhankelijkheid toevoegen
                  </Button>
                )}
              </>
            )}
            <Link
              href={`/afhankelijkheden?cluster_id=${clusterId}`}
              className="inline-flex items-center px-1 text-brand-700 hover:underline dark:text-sky-300"
            >
              Graaf tonen
            </Link>
          </span>
        </span>
      }
    >
      <QueryState q={graph}>
        <div className="space-y-5 text-sm">
          {err && <Alert>{err}</Alert>}
          {own.length === 0 ? (
            <Empty>
              Dit cluster heeft nog geen diensten. Zodra een agent een bekende unit zoals nginx of mariadb meldt, verschijnt hier een voorstel.
            </Empty>
          ) : (
            <div className="-mx-5 overflow-x-auto">
              <table className={tableClass}>
                <thead>
                  <tr className="border-b border-slate-200 dark:border-slate-800">
                    <th className={cx(thClass, "pl-5")}>Dienst</th>
                    <th className={thClass}>Soort</th>
                    <th className={thClass}>Unit</th>
                    <th className={thClass}>Poort</th>
                    <th className={thClass}>Status</th>
                    <th className={cx(thClass, "pr-5")}>Bron</th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
                  {own.map((s) => (
                    <tr key={s.id} className={cx(s.state === "suggested" && "bg-slate-50/70 dark:bg-slate-800/30")}>
                      <td className={cx(tdClass, "pl-5 font-medium")}>
                        <Link
                          href={`/afhankelijkheden?cluster_id=${clusterId}&dienst=${s.id}`}
                          className="text-brand-700 hover:underline dark:text-sky-300"
                        >
                          {s.name}
                        </Link>
                      </td>
                      <td className={tdClass}>{kindLabel(s.kind)}</td>
                      <td className={cx(tdClass, "font-mono text-xs")}>{s.unit || <span className="text-slate-400">geen</span>}</td>
                      <td className={tdClass}>{s.port ?? <span className="text-slate-400">geen</span>}</td>
                      <td className={tdClass}>
                        {s.state === "suggested" ? <Badge tone="blue">voorstel</Badge> : <ServiceStatusText s={s} />}
                      </td>
                      <td className={cx(tdClass, "pr-5 whitespace-nowrap")}>
                        {s.state === "suggested" && isAdmin ? (
                          <span className="flex gap-1.5">
                            <Button
                              className="px-2 py-0.5 text-xs"
                              disabled={update.isPending}
                              onClick={() => update.mutate({ id: s.id, body: { state: "confirmed" } })}
                            >
                              Bevestigen
                            </Button>
                            <Button
                              variant="secondary"
                              className="px-2 py-0.5 text-xs"
                              disabled={update.isPending}
                              onClick={() => update.mutate({ id: s.id, body: { state: "ignored" } })}
                            >
                              Negeren
                            </Button>
                          </span>
                        ) : (
                          sources[s.source]
                        )}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}

          <div className="grid gap-5 sm:grid-cols-2">
            <Arrows title="Hangt af van" empty="Geen bekende afhankelijkheden van andere clusters." edges={uses} idx={idx} groupId={groupId} side="to" />
            <Arrows title="Gebruikt door" empty="Geen bekend ander cluster gebruikt dit cluster." edges={usedBy} idx={idx} groupId={groupId} side="from" />
          </div>
          <p className="text-xs text-slate-500">Alleen bekende afhankelijkheden. De graaf beslist nooit of een actie mag.</p>
        </div>
      </QueryState>

      {dialog === "service" && <ServiceDialog scope={{ cluster_id: clusterId }} onClose={() => setDialog("")} />}
      {dialog === "dependency" && <DependencyDialog choices={confirmed} onClose={() => setDialog("")} />}
    </Card>
  );
}

function Arrows({
  title,
  empty,
  edges,
  idx,
  groupId,
  side,
}: {
  title: string;
  empty: string;
  edges: DepEdge[];
  idx: Index;
  groupId?: string;
  side: "from" | "to";
}) {
  return (
    <section>
      <h3 className="mb-1.5 font-medium">{title}</h3>
      {edges.length === 0 ? (
        <p className="text-slate-500">{empty}</p>
      ) : (
        <ul className="space-y-1">
          {edges.map((e) => {
            const mine = side === "to" ? e.from : e.to;
            const other = side === "to" ? e.to : e.from;
            const o = idx.services.get(other);
            return (
              <li key={e.id} className={cx(e.affected && "text-red-700 dark:text-red-300")}>
                {o && <StatusDot status={o.status} className="mr-1.5 align-middle" />}
                {side === "to" ? (
                  <>
                    {label(idx, mine, groupId)} → {label(idx, other, groupId)}
                  </>
                ) : (
                  <>
                    {label(idx, other, groupId)} → {label(idx, mine, groupId)}
                  </>
                )}
                <span className="text-xs text-slate-500">
                  {" "}
                  · {strengthLabel(e.strength)} · {sources[e.source]}
                </span>
              </li>
            );
          })}
        </ul>
      )}
    </section>
  );
}
