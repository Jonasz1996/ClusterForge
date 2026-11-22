"use client";

import Link from "next/link";
import type { ReactNode } from "react";
import { EnvBadge } from "@/components/inventory/bits";
import { Badge } from "@/components/ui";
import { useImpact, type Impact, type ImpactItem, type ServiceImpact } from "@/lib/deps";

// ImpactList is het antwoord op Wat raakt uitval?, per groep met prod
// bovenaan.
export function ImpactList({ impact, title }: { impact: Impact; title?: ReactNode }) {
  const byGroup = impact.groups.map((g) => ({ g, items: impact.items.filter((i) => i.group_id === g.group_id) }));
  return (
    <section className="rounded-md border border-slate-200 p-3 text-sm dark:border-slate-800" aria-label="Wat raakt uitval">
      <div className="mb-2 flex flex-wrap items-baseline justify-between gap-2">
        <h3 className="font-medium">{title ?? `Wat raakt uitval van ${impact.target.name}?`}</h3>
        <span className="text-xs text-slate-500">bekende afhankelijkheden</span>
      </div>
      {impact.items.length === 0 ? (
        <p className="text-slate-500">Er hangt geen bekende dienst van af.</p>
      ) : (
        <div className="space-y-3">
          {byGroup.map(({ g, items }) => (
            <div key={g.group_id}>
              <div className="mb-1 flex items-center gap-1.5 text-xs font-medium tracking-wide text-slate-500 uppercase">
                {g.environment && <EnvBadge env={g.environment} />}
                <span>{g.name}</span>
              </div>
              <ul className="space-y-1">
                {items.map((it) => (
                  <ImpactRow key={it.service_id} it={it} />
                ))}
              </ul>
            </div>
          ))}
        </div>
      )}
    </section>
  );
}

function ImpactRow({ it }: { it: ImpactItem }) {
  return (
    <li className="grid grid-cols-[auto_1fr] items-baseline gap-x-2">
      <Badge tone={it.impact === "down" ? "red" : "amber"}>{it.impact === "down" ? "down" : "verminderd"}</Badge>
      <span>
        <span className="font-medium">{it.name}</span>
        <span className="text-slate-500"> · {it.reason}</span>
      </span>
    </li>
  );
}

// NodeImpact zegt in een bevestigingsvenster welke bekende diensten down of
// verminderd raken als de node wegvalt. Het is alleen informatie: het
// venster houdt niets tegen, en een lege lijst betekent alleen dat
// ClusterForge geen afhankelijkheid kent.
export function NodeImpact({ nodeId, title }: { nodeId: string; title: string }) {
  const impact = useImpact({ node_id: nodeId });
  if (impact.isLoading) return <p className="text-xs text-slate-500">Bekende afhankelijkheden laden…</p>;
  if (!impact.data) return <p className="text-xs text-slate-500">De afhankelijkheden konden niet geladen worden.</p>;
  if (impact.data.items.length === 0) {
    return (
      <p className="text-xs text-slate-500">
        ClusterForge kent geen dienst die hierdoor down of verminderd raakt.{" "}
        <Link href="/afhankelijkheden" className="underline">
          Bekende afhankelijkheden
        </Link>
      </p>
    );
  }
  return <ImpactList impact={impact.data} title={title} />;
}

// ImpactLine is dezelfde lijst op één regel, voor een stap in een plan:
// "nginx in web-prod verminderd, php8.2-fpm in web-prod down".
export function ImpactLine({ nodeId, prefix }: { nodeId: string; prefix: string }) {
  const impact = useImpact({ node_id: nodeId });
  const items = impact.data?.items ?? [];
  if (items.length === 0) return null;
  return (
    <p className="text-xs text-slate-600 dark:text-slate-400">
      {prefix}{" "}
      {items.map((it, i) => (
        <span key={it.service_id}>
          {i > 0 && ", "}
          <span className={it.impact === "down" ? "font-medium text-red-700 dark:text-red-300" : "text-amber-700 dark:text-amber-300"}>
            {it.name} in {it.group_name} {it.impact === "down" ? "down" : "verminderd"}
          </span>
        </span>
      ))}{" "}
      <span className="text-slate-500">(bekende afhankelijkheden)</span>
    </p>
  );
}

// UsedBy waarschuwt bij verwijderen: de diensten van buiten die van dit
// cluster of deze losse node afhangen. Hun afhankelijkheden verdwijnen mee.
export function UsedBy({ clusterId, nodeId, what }: { clusterId?: string; nodeId?: string; what: string }) {
  const impact = useImpact(clusterId ? { cluster_id: clusterId } : nodeId ? { node_id: nodeId } : null);
  const outside = (impact.data?.items ?? []).filter((it) => !it.direct);
  if (outside.length === 0) return null;
  const groups = [...new Set(outside.map((it) => it.group_name))];
  return (
    <div className="space-y-1.5 rounded border border-amber-200 bg-amber-50 px-3 py-2 text-sm text-amber-900 dark:border-amber-900 dark:bg-amber-950 dark:text-amber-200">
      <p>
        Gebruikt door {groups.length === 1 ? groups[0] : `${groups.slice(0, -1).join(", ")} en ${groups[groups.length - 1]}`}. Die
        afhankelijkheden verdwijnen met {what}:
      </p>
      <ul className="list-disc space-y-0.5 pl-5">
        {outside.map((it) => (
          <li key={it.service_id}>
            {it.name} in {it.group_name}: {it.reason}
          </li>
        ))}
      </ul>
    </div>
  );
}

// ImpactBadge staat naast de status van een cluster als bekende
// afhankelijkheden uitval van buiten doorgeven: "geraakt door db-prod". De
// status van het cluster zelf verandert daar niet door.
export function ImpactBadge({ impact, by }: { impact: ServiceImpact; by: string[] }) {
  if (impact === "none" || by.length === 0) return null;
  const who = by.length === 1 ? by[0] : `${by.slice(0, -1).join(", ")} en ${by[by.length - 1]}`;
  const how = impact === "down" ? "Down" : "Verminderd";
  return (
    <span title={`${how} door een afhankelijkheid van ${who} (bekende afhankelijkheden)`}>
      <Badge tone={impact === "down" ? "red" : "amber"}>geraakt door {who}</Badge>
    </span>
  );
}
