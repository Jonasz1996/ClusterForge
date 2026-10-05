"use client";

import Link from "next/link";
import { DriftBadge } from "@/components/drift/Drift";
import { EnvBadge, StatusBadge } from "@/components/inventory/bits";
import type { Cluster, Status } from "@/lib/inventory";

// Ernstigste eerst, zodat problemen bovenaan staan.
export const severity: Record<Status, number> = { split_brain: 0, down: 1, degraded: 2, unknown: 3, healthy: 4 };

export function bySeverity<T extends { status: Status }>(a: T, b: T) {
  return severity[a.status] - severity[b.status];
}

// ClusterHealth toont per cluster de status en wie elk VIP heeft.
export function ClusterHealth({ clusters }: { clusters: Cluster[] }) {
  const sorted = [...clusters].sort((a, b) => bySeverity(a, b) || a.name.localeCompare(b.name, "nl"));
  return (
    <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
      {sorted.map((c) => (
        <Link
          key={c.id}
          href={`/clusters/detail?id=${c.id}`}
          className="block rounded-lg border border-slate-200 bg-white p-4 hover:border-slate-300 dark:border-slate-800 dark:bg-slate-900 dark:hover:border-slate-700"
        >
          <div className="flex items-start justify-between gap-2">
            <div className="min-w-0">
              <div className="truncate font-medium">{c.name}</div>
              <div className="mt-1">
                <EnvBadge env={c.environment} />
              </div>
            </div>
            <span className="flex flex-col items-end gap-1">
              <StatusBadge status={c.status} reason={c.status_reason} />
              <DriftBadge drift={c.drift} />
            </span>
          </div>
          {c.status_reason && <p className="mt-2 text-xs text-slate-500">{c.status_reason}</p>}
          {c.vips.length > 0 && (
            <ul className="mt-3 space-y-1 border-t border-slate-100 pt-3 text-xs dark:border-slate-800">
              {c.vips.map((v) => (
                <li key={v.address} className="flex justify-between gap-2">
                  <span className="font-mono">{v.address}</span>
                  {v.owner_hostname ? (
                    <span className="text-slate-700 dark:text-slate-300">{v.owner_hostname}</span>
                  ) : (
                    <span className="text-red-600 dark:text-red-400">geen eigenaar</span>
                  )}
                </li>
              ))}
            </ul>
          )}
        </Link>
      ))}
    </div>
  );
}
