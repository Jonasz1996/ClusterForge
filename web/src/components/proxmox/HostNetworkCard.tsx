"use client";

import Link from "next/link";
import { useState } from "react";
import { ago, tableClass, tdClass, thClass } from "@/components/inventory/bits";
import { Alert, Badge, Card, cx } from "@/components/ui";
import { ifaceType, useHostNetwork, type HostInterface } from "@/lib/map";

const typeOrder = ["bridge", "OVSBridge", "bond", "OVSBond", "vlan", "eth"];

function rank(i: HostInterface) {
  const r = typeOrder.indexOf(i.type);
  return r < 0 ? typeOrder.length : r;
}

// HostNetworkCard toont per host van een koppeling het netwerk zoals
// Proxmox het kent: bridges met hun poorten, bonds, VLAN's en adressen.
export function HostNetworkCard({ connectionId }: { connectionId: string }) {
  const q = useHostNetwork(connectionId);
  const [chosen, setChosen] = useState<string | null>(null);
  const hosts = q.data?.hosts ?? [];
  const cur = hosts.find((h) => h.host === chosen) ?? hosts[0];
  const ifaces = [...(cur?.interfaces ?? [])].sort(
    (a, b) => rank(a) - rank(b) || a.iface.localeCompare(b.iface, "nl", { numeric: true }),
  );

  return (
    <Card
      title={
        <span className="flex items-center justify-between gap-2">
          Netwerk
          <Link href="/kaart?laag=netwerk" className="text-sm font-normal text-brand-600 hover:underline dark:text-brand-500">
            Op de kaart →
          </Link>
        </span>
      }
    >
      {q.isLoading ? (
        <p className="text-sm text-slate-500">Laden…</p>
      ) : hosts.length === 0 ? (
        <p className="text-sm text-slate-500">
          Het netwerk van de hosts is nog niet gelezen. ClusterForge leest het na een geslaagde sync, en daarna elke tien minuten.
        </p>
      ) : (
        <div className="space-y-3">
          {hosts.length > 1 && (
            <div className="flex gap-1 border-b border-slate-200 dark:border-slate-800" role="tablist">
              {hosts.map((h) => (
                <button
                  key={h.host}
                  type="button"
                  role="tab"
                  aria-selected={cur?.host === h.host}
                  onClick={() => setChosen(h.host)}
                  className={cx(
                    "-mb-px border-b-2 px-3 py-1.5 text-sm font-medium",
                    cur?.host === h.host
                      ? "border-brand-600 text-brand-700 dark:text-sky-300"
                      : "border-transparent text-slate-500 hover:text-slate-700 dark:hover:text-slate-300",
                  )}
                >
                  {h.host}
                  {h.error && <span className="ml-1 text-red-600">!</span>}
                </button>
              ))}
            </div>
          )}
          {cur?.error && (
            <Alert>
              Het netwerk van {cur.host} lezen mislukte: {cur.error}. Wat hieronder staat, is van daarvoor.
            </Alert>
          )}
          {ifaces.length === 0 ? (
            <p className="text-sm text-slate-500">Geen interfaces.</p>
          ) : (
            <div className="overflow-x-auto">
              <table className={tableClass}>
                <thead className="border-b border-slate-200 dark:border-slate-800">
                  <tr>
                    <th className={thClass}>Interface</th>
                    <th className={thClass}>Soort</th>
                    <th className={thClass}>Poorten of onderlaag</th>
                    <th className={thClass}>Adres</th>
                    <th className={thClass} />
                  </tr>
                </thead>
                <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
                  {ifaces.map((i) => (
                    <tr key={i.iface}>
                      <td className={tdClass}>
                        <span className="font-mono text-xs">{i.iface}</span>
                        {i.comments && <div className="text-[11px] text-slate-500">{i.comments}</div>}
                      </td>
                      <td className={tdClass}>
                        {ifaceType(i.type)}
                        {i.vlan_id ? ` ${i.vlan_id}` : ""}
                      </td>
                      <td className={cx(tdClass, "font-mono text-xs")}>
                        {i.ports.length ? i.ports.join(", ") : i.vlan_raw_device || "–"}
                      </td>
                      <td className={cx(tdClass, "font-mono text-xs")}>
                        {[i.cidr, i.cidr6].filter(Boolean).join(", ") || "–"}
                        {i.gateway && <div className="text-[11px] text-slate-500">gateway {i.gateway}</div>}
                      </td>
                      <td className={cx(tdClass, "text-right whitespace-nowrap")}>
                        {i.vlan_aware && <Badge tone="blue">VLAN-aware</Badge>}{" "}
                        {i.mtu && i.mtu !== 1500 ? <Badge>mtu {i.mtu}</Badge> : null} {!i.active && <Badge>niet actief</Badge>}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
          {cur && <p className="text-xs text-slate-500">Gelezen {ago(cur.read_at)}.</p>}
        </div>
      )}
    </Card>
  );
}
