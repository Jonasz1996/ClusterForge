"use client";

import Link from "next/link";
import { tableClass, tdClass, thClass } from "@/components/inventory/bits";
import { Alert, Button, Card, cx } from "@/components/ui";
import { useDecideNodeLink, useNodeLinks } from "@/lib/map";

// NodeLinkProposals toont de VM's met het MAC-adres van een node die nog
// niet aan een VM hangt. Bevestigen koppelt ze, zoals Koppelen bij de node.
export function NodeLinkProposals({ isAdmin }: { isAdmin: boolean }) {
  const links = useNodeLinks();
  const decide = useDecideNodeLink();
  const items = links.data ?? [];
  if (items.length === 0) return null;
  const err = decide.error instanceof Error ? decide.error.message : null;

  return (
    <div id="koppelvoorstellen" className="scroll-mt-4">
      <Card title={items.length === 1 ? "1 VM lijkt een node te zijn" : `${items.length} VM's lijken een node te zijn`}>
        <p className="mb-3 text-sm text-slate-600 dark:text-slate-400">
          Deze VM&apos;s en containers hebben hetzelfde MAC-adres als een node met een agent, maar zijn nog niet aan die node
          gekoppeld. Gekoppeld staan ze op de kaart en tellen ze mee in de wat-als en de health score. Een genegeerd voorstel komt
          niet terug.
        </p>
        {err && (
          <div className="mb-3">
            <Alert>{err}</Alert>
          </div>
        )}
        <div className="overflow-x-auto">
          <table className={tableClass}>
            <thead className="border-b border-slate-200 dark:border-slate-800">
              <tr>
                <th className={thClass}>Node</th>
                <th className={thClass}>Is waarschijnlijk</th>
                <th className={thClass}>Zelfde MAC-adres</th>
                {isAdmin && <th className={thClass} />}
              </tr>
            </thead>
            <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
              {items.map((l) => (
                <tr key={l.id}>
                  <td className={tdClass}>
                    <Link href={`/nodes/detail?id=${l.node_id}`} className="font-medium hover:underline">
                      {l.hostname}
                    </Link>
                    {l.cluster && <span className="text-slate-500"> · {l.cluster}</span>}
                  </td>
                  <td className={tdClass}>
                    {l.guest_type === "lxc" ? "Container" : "VM"} {l.vmid}
                    {l.guest && ` (${l.guest})`}{" "}
                    <span className="text-slate-500">
                      op {l.host} in {l.connection}
                    </span>
                  </td>
                  <td className={cx(tdClass, "font-mono text-xs")}>{l.mac}</td>
                  {isAdmin && (
                    <td className={cx(tdClass, "text-right whitespace-nowrap")}>
                      <Button
                        className="px-2.5 py-1"
                        disabled={decide.isPending}
                        onClick={() => decide.mutate({ id: l.id, confirm: true })}
                      >
                        Bevestigen
                      </Button>{" "}
                      <Button
                        variant="secondary"
                        className="px-2.5 py-1"
                        disabled={decide.isPending}
                        onClick={() => decide.mutate({ id: l.id, confirm: false })}
                      >
                        Negeren
                      </Button>
                    </td>
                  )}
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </Card>
    </div>
  );
}
