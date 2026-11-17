"use client";

import Link from "next/link";
import { Alert, Button, Card } from "@/components/ui";
import { ApiError } from "@/lib/api/client";
import { useIsAdmin } from "@/lib/inventory";
import { useSetZabbixMaintenance, useZabbixMaintenance } from "@/lib/zabbixMonitoring";

const fmt = new Intl.DateTimeFormat("nl-BE", { day: "numeric", month: "short", hour: "2-digit", minute: "2-digit" });

// MaintenanceCard: onderhoud van ClusterForge doorzetten naar Zabbix (M6).
export function MaintenanceCard() {
  const q = useZabbixMaintenance();
  const set = useSetZabbixMaintenance();
  const isAdmin = useIsAdmin();
  const m = q.data;
  return (
    <Card title="Onderhoud naar Zabbix">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <p className="max-w-3xl text-sm text-slate-600 dark:text-slate-400">
          Zet je een node in ClusterForge in onderhoud, dan zet ClusterForge in Zabbix een onderhoud op zijn host. Zabbix blijft meten, maar
          meldt geen problemen. Komt de node uit onderhoud, dan haalt ClusterForge het weer weg. Alleen onderhouden die ClusterForge zelf
          zette, komen ooit weg; ze lopen 7 dagen en ClusterForge verlengt ze zolang de node in onderhoud blijft.
        </p>
        {m && (
          <span
            className={`inline-flex items-center gap-2 rounded-full px-2.5 py-0.5 text-xs font-medium ${
              m.sync ? "bg-emerald-50 text-emerald-700 dark:bg-emerald-950 dark:text-emerald-300" : "bg-slate-100 text-slate-600 dark:bg-slate-800 dark:text-slate-300"
            }`}
          >
            <span className={`size-1.5 rounded-full ${m.sync ? "bg-emerald-500" : "bg-slate-400"}`} aria-hidden />
            {m.sync ? "Aan" : "Uit"}
          </span>
        )}
      </div>
      {q.isError && <Alert>{q.error instanceof ApiError ? q.error.message : "Er ging iets mis."}</Alert>}
      {m && (
        <div className="mt-3 space-y-3">
          {m.error && <Alert>{m.error}</Alert>}
          {set.isError && <Alert>{set.error instanceof ApiError ? set.error.message : "Er ging iets mis."}</Alert>}
          {m.active.length > 0 ? (
            <ul className="divide-y divide-slate-100 text-sm dark:divide-slate-800">
              {m.active.map((a) => (
                <li key={a.node_id} className="flex flex-wrap items-center justify-between gap-x-3 py-1.5">
                  <Link href={`/nodes/detail?id=${a.node_id}`} className="font-medium text-brand-700 hover:underline dark:text-sky-300">
                    {a.hostname}
                  </Link>
                  <span className="text-xs text-slate-500">
                    in onderhoud sinds {fmt.format(new Date(a.created_at))}, loopt tot {fmt.format(new Date(a.active_till))}
                  </span>
                </li>
              ))}
            </ul>
          ) : (
            m.sync && <p className="text-sm text-slate-500">Nu staat geen enkele node met een Zabbix-host in onderhoud.</p>
          )}
          {isAdmin && (
            <div className="flex flex-wrap items-center gap-3">
              <Button variant={m.sync ? "secondary" : "primary"} disabled={set.isPending} onClick={() => set.mutate(!m.sync)}>
                {set.isPending ? "Bezig…" : m.sync ? "Uitzetten" : "Aanzetten"}
              </Button>
              <span className="text-xs text-slate-500">
                {m.sync ? "Uitzetten haalt de onderhouden van ClusterForge in Zabbix meteen weg." : "Het API-token heeft daarvoor schrijfrechten op de hosts nodig."}
                {m.updated_by && <> Laatst gewijzigd door {m.updated_by}.</>}
              </span>
            </div>
          )}
        </div>
      )}
    </Card>
  );
}
