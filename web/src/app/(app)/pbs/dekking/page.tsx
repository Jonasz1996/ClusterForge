"use client";

import Link from "next/link";
import { useMemo, useState } from "react";
import { Empty, QueryState, tableClass, tdClass, thClass } from "@/components/inventory/bits";
import { timeFmt } from "@/components/pbs/bits";
import { Badge, Button, Card, cx, PageHeader } from "@/components/ui";
import { usePbsCoverage, type NodeCoverage } from "@/lib/pbs";

export default function PbsCoveragePage() {
  const [fresh, setFresh] = useState(false);
  const q = usePbsCoverage(fresh);
  const [onlyBad, setOnlyBad] = useState(false);
  const groups = useMemo(() => {
    const by = new Map<string, NodeCoverage[]>();
    for (const n of q.data?.nodes ?? []) {
      if (onlyBad && n.state === "ok") continue;
      const k = n.cluster_name || "Zonder cluster";
      by.set(k, [...(by.get(k) ?? []), n]);
    }
    return [...by.entries()];
  }, [q.data, onlyBad]);
  const bad = (q.data?.nodes ?? []).filter((n) => n.state !== "ok").length;
  return (
    <div className="space-y-6">
      <PageHeader
        title="Dekking"
        description="Heeft elke node die een Proxmox-gast is een recente, geverifieerde back-up op een Backup Server? Per cluster, met de nieuwste back-up over alle PBS'en, datastores en namespaces heen."
      />
      <Card>
        <div className="space-y-4">
          <div className="flex flex-wrap items-center gap-3 text-sm">
            <label className="flex items-center gap-2">
              <input type="checkbox" checked={onlyBad} onChange={(e) => setOnlyBad(e.target.checked)} />
              Alleen wat aandacht vraagt{bad ? ` (${bad})` : ""}
            </label>
            <span className="ml-auto text-xs text-slate-500">{q.data ? `Gelezen ${timeFmt.format(new Date(q.data.at))}` : ""}</span>
            <Button variant="secondary" disabled={q.isFetching} onClick={() => (fresh ? void q.refetch() : setFresh(true))}>
              {q.isFetching ? "Lezen…" : "Nu opnieuw lezen"}
            </Button>
          </div>
          <QueryState q={q}>
            {(q.data?.nodes ?? []).length === 0 ? (
              <Empty>
                Geen nodes die ClusterForge aan een Proxmox-gast gekoppeld heeft. Koppel ze via{" "}
                <Link href="/proxmox" className="underline">
                  Proxmox
                </Link>
                ; daarna zie je hier per node zijn back-ups.
              </Empty>
            ) : groups.length === 0 ? (
              <Empty>Alles in orde: elke node heeft een recente back-up.</Empty>
            ) : (
              groups.map(([cluster, nodes]) => (
                <section key={cluster} className="space-y-2">
                  <h3 className="text-sm font-semibold">{cluster}</h3>
                  <div className="relative -mx-4 overflow-x-auto sm:mx-0">
                    <table className={tableClass}>
                      <thead>
                        <tr>
                          <th className={thClass}>Node</th>
                          <th className={thClass}>Laatste back-up</th>
                          <th className={cx(thClass, "hidden md:table-cell")}>Waar</th>
                          <th className={thClass}>Stand</th>
                        </tr>
                      </thead>
                      <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
                        {nodes.map((n) => (
                          <tr key={n.node_id} className={cx(n.state === "crit" && "bg-red-50/40 dark:bg-red-950/20")}>
                            <td className={tdClass}>
                              <Link href={`/nodes/detail?id=${n.node_id}`} className="font-medium hover:underline">
                                {n.hostname}
                              </Link>
                              <span className="ml-1 text-xs text-slate-500">
                                {n.guest_type === "lxc" ? "CT" : n.guest_type === "qemu" ? "VM" : ""} {n.vmid}
                              </span>
                            </td>
                            <td className={cx(tdClass, "whitespace-nowrap")}>
                              {n.last_backup ? timeFmt.format(new Date(n.last_backup * 1000)) : "–"}
                              {n.count ? <span className="ml-1 text-xs text-slate-500">({n.count})</span> : null}
                            </td>
                            <td className={cx(tdClass, "hidden text-xs md:table-cell")}>
                              {n.server_id ? (
                                <Link
                                  href={`/pbs/inhoud?pbs=${n.server_id}&store=${encodeURIComponent(n.store ?? "")}${n.ns ? "&ns=" + encodeURIComponent(n.ns) : ""}`}
                                  className="hover:underline"
                                >
                                  {n.server} / {n.store}
                                  {n.ns ? " / " + n.ns : ""}
                                </Link>
                              ) : (
                                "–"
                              )}
                            </td>
                            <td className={tdClass}>
                              <Badge tone={n.state === "ok" ? "green" : n.state === "warn" ? "amber" : "red"}>
                                {n.state === "ok" ? (n.verify === "ok" ? "OK, geverifieerd" : "OK") : n.state === "warn" ? "Let op" : "Probleem"}
                              </Badge>
                              {n.why && <span className="block text-xs text-slate-500">{n.why}</span>}
                            </td>
                          </tr>
                        ))}
                      </tbody>
                    </table>
                  </div>
                </section>
              ))
            )}
          </QueryState>
          {(q.data?.errors ?? []).length > 0 && <p className="text-xs text-slate-500">Niet gelezen: {q.data!.errors.join(" · ")}</p>}
        </div>
      </Card>
    </div>
  );
}
