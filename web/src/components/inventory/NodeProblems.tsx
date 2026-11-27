"use client";

import Link from "next/link";
import { useState } from "react";
import { RestartService } from "@/components/RestartService";
import { Card } from "@/components/ui";
import type { Node } from "@/lib/inventory";

// De vier tabbladen van de nodepagina (fase 47). Vroeger stonden er ±30
// kaarten onder elkaar.
export const nodeTabs = [
  { key: "status", label: "Status" },
  { key: "software", label: "Software" },
  { key: "netwerk", label: "Netwerk" },
  { key: "geschiedenis", label: "Geschiedenis" },
] as const;

export type NodeTab = (typeof nodeTabs)[number]["key"];

export function tabOf(v: string | null): NodeTab {
  return nodeTabs.find((t) => t.key === v)?.key ?? "status";
}

type Problem = { text: string; unit?: string };

// problemsOf splitst de reden van de status zoals de server ze schrijft
// (internal/status): "service ufw draait niet; schijf / is 92 % vol".
export function problemsOf(reason: string): Problem[] {
  if (!reason.trim()) return [];
  return reason.split("; ").map((part) => {
    const m = /^service (\S+) (is gefaald|crasht en start steeds opnieuw|draait niet)$/.exec(part.trim());
    return m ? { text: `${m[1]} ${m[2]}`, unit: m[1] } : { text: part.trim() };
  });
}

// NodeProblems staat bovenaan de nodepagina: wat er mis is, met bij elke
// dienst de logs en herstarten, en de drie acties die je het vaakst nodig
// hebt.
export function NodeProblems({ node, isAdmin }: { node: Node; isAdmin: boolean }) {
  const [restart, setRestart] = useState<string | null>(null);
  const problems = node.status === "healthy" || node.status === "unknown" ? [] : problemsOf(node.status_reason);
  const btn =
    "rounded-md border border-slate-300 px-2.5 py-1 text-xs font-medium text-slate-700 hover:border-brand-500 hover:text-brand-700 dark:border-slate-600 dark:text-slate-200 dark:hover:text-sky-300";
  const logs = (unit: string) => `/logs?node=${node.id}&kind=journal&name=${encodeURIComponent(unit)}`;
  return (
    <Card>
      <div className="space-y-3">
        {problems.length > 0 ? (
          <ul className="space-y-2">
            {problems.map((p) => (
              <li key={p.text} className="flex flex-wrap items-center gap-2 text-sm">
                <span className={`size-2 rounded-full ${node.status === "degraded" ? "bg-amber-400" : "bg-red-500"}`} aria-hidden />
                <span className="font-medium">{p.text}</span>
                {p.unit && isAdmin && (
                  <>
                    <Link href={logs(p.unit)} className={btn}>
                      Logs van {p.unit}
                    </Link>
                    {node.agent && (
                      <button type="button" className={btn} onClick={() => setRestart(p.unit!)}>
                        Herstarten
                      </button>
                    )}
                    {p.unit === "ufw" && (
                      <Link href="/firewall?tab=nodes" className={btn}>
                        Firewall
                      </Link>
                    )}
                  </>
                )}
              </li>
            ))}
          </ul>
        ) : (
          <p className="flex items-center gap-2 text-sm text-slate-600 dark:text-slate-400">
            <span className={`size-2 rounded-full ${node.status === "healthy" ? "bg-emerald-500" : "bg-slate-400"}`} aria-hidden />
            {node.status === "healthy" ? "Geen problemen." : node.agent ? "Status nog onbekend." : "Geen agent: ClusterForge ziet de status niet."}
          </p>
        )}
        {isAdmin && (
          <div className="flex flex-wrap gap-2 border-t border-slate-100 pt-3 dark:border-slate-800">
            <Link href={`/logs?node=${node.id}&kind=file&name=${encodeURIComponent("/var/log/syslog")}`} className={btn}>
              Logs
            </Link>
            <Link href="/updates" className={btn}>
              Bijwerken
            </Link>
            <Link href="/commandos" className={btn}>
              Commando uitvoeren
            </Link>
            {node.proxmox && (
              <Link href="/proxmox/gasten" className={btn}>
                In Proxmox
              </Link>
            )}
          </div>
        )}
      </div>
      {restart && <RestartService unit={restart} nodeIds={[node.id]} onClose={() => setRestart(null)} />}
    </Card>
  );
}
