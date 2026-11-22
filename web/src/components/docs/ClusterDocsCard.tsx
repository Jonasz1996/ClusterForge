"use client";

import Link from "next/link";
import { Card } from "@/components/ui";
import { docHref, formatTime, formatWhen, kindLabels, useDocIndex } from "@/lib/docs";

const chapters = ["architectuur", "netwerk", "configuratie", "cronjobs", "herstel", "runbooks"];

// ClusterDocsCard linkt naar de hoofdstukken van de documentatie van een
// cluster, met wanneer ze voor het laatst veranderden. De server zegt welke
// er zijn: Kubernetes en Database horen niet bij elk cluster.
export function ClusterDocsCard({ clusterId }: { clusterId: string }) {
  const index = useDocIndex();
  const c = index.data?.clusters.find((x) => x.id === clusterId);
  if (!c) return null;
  return (
    <Card title="Documentatie">
      <p className="mb-3 text-sm text-slate-600 dark:text-slate-400">
        {c.changed_at
          ? `Laatst bijgewerkt op ${formatWhen(c.changed_at)}.`
          : "Nog niet bewaard; de pagina toont hoe het er nu uitziet."}
        {c.queued_until && ` Een wijziging komt erin rond ${formatTime(c.queued_until)}.`}
      </p>
      <ul className="flex flex-wrap gap-2">
        {(c.docs.length > 0 ? c.docs.map((d) => d.kind) : chapters).map((k) => (
          <li key={k}>
            <Link
              href={docHref(c.slug, k)}
              className="inline-flex rounded-md border border-slate-200 px-3 py-1.5 text-sm font-medium text-slate-700 hover:bg-slate-50 dark:border-slate-700 dark:text-slate-200 dark:hover:bg-slate-800"
            >
              {kindLabels[k]}
            </Link>
          </li>
        ))}
      </ul>
    </Card>
  );
}
