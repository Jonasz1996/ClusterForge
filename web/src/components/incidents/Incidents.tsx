"use client";

import Link from "next/link";
import { formatDuration, tableClass, tdClass, thClass } from "@/components/inventory/bits";
import { Alert, Badge, Card } from "@/components/ui";
import { auditTimeFmt } from "@/lib/audit";
import { durationSeconds, severities, useIncidents, type Incident } from "@/lib/incidents";

export const incidentHref = (id: string) => `/incidenten/detail?id=${id}`;

export function SeverityBadge({ severity }: { severity: Incident["severity"] }) {
  const s = severities[severity];
  return <Badge tone={s.tone}>{s.label}</Badge>;
}

// IncidentState zegt of het incident nog loopt, en hoe lang al of hoe lang
// het duurde.
export function IncidentState({ inc }: { inc: Incident }) {
  const d = formatDuration(durationSeconds(inc));
  if (inc.status === "open") {
    return (
      <Badge tone="red">
        <span className="mr-1 inline-block size-1.5 rounded-full bg-current" aria-hidden />
        Open · {d}
      </Badge>
    );
  }
  return <Badge tone="green">Opgelost na {d}</Badge>;
}

// subjectsLine noemt wat er geraakt was, met het eerste onderwerp voorop.
function subjectsLine(inc: Incident) {
  const names = inc.subjects.map((s) => s.name);
  if (names.length <= 3) return names.join(", ");
  return `${names.slice(0, 3).join(", ")} en ${names.length - 3} meer`;
}

export function IncidentTable({ items, empty }: { items: Incident[]; empty: string }) {
  return (
    <div className="overflow-x-auto rounded-lg border border-slate-200 bg-white dark:border-slate-800 dark:bg-slate-900">
      <table className={tableClass}>
        <thead className="border-b border-slate-200 dark:border-slate-800">
          <tr>
            <th className={`${thClass} text-right`}>#</th>
            <th className={thClass}>Incident</th>
            <th className={thClass}>Ernst</th>
            <th className={thClass}>Status</th>
            <th className={thClass}>Begonnen</th>
          </tr>
        </thead>
        <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
          {items.map((inc) => (
            <tr key={inc.id} className="hover:bg-slate-50 dark:hover:bg-slate-800/50">
              <td className={`${tdClass} text-right text-slate-500 tabular-nums`}>{inc.number}</td>
              <td className={tdClass}>
                <Link href={incidentHref(inc.id)} className="font-medium text-brand-700 hover:underline dark:text-sky-300">
                  {inc.title}
                </Link>
                <div className="text-xs text-slate-500">
                  {subjectsLine(inc)}
                  {inc.caused_by_test && " · door een test"}
                </div>
                {inc.cause && (
                  <div className="mt-0.5 max-w-xl text-xs break-words text-slate-600 dark:text-slate-400">
                    <span className={inc.cause.level === "likely" ? "font-medium text-red-700 dark:text-red-300" : "font-medium"}>
                      {inc.cause.level === "likely" ? "Waarschijnlijk:" : "Mogelijk:"}
                    </span>{" "}
                    {inc.cause.summary}
                  </div>
                )}
              </td>
              <td className={tdClass}>
                <SeverityBadge severity={inc.severity} />
              </td>
              <td className={tdClass}>
                <IncidentState inc={inc} />
              </td>
              <td className={`${tdClass} whitespace-nowrap text-slate-600 tabular-nums dark:text-slate-400`}>
                {auditTimeFmt.format(new Date(inc.started_at))}
              </td>
            </tr>
          ))}
          {items.length === 0 && (
            <tr>
              <td colSpan={5} className="px-3 py-6 text-center text-sm text-slate-500">
                {empty}
              </td>
            </tr>
          )}
        </tbody>
      </table>
    </div>
  );
}

// OpenIncidentsCard staat op het overzicht: wat er nu misgaat.
export function OpenIncidentsCard() {
  const q = useIncidents({ status: "open", limit: 20 });
  return (
    <Card
      title={
        <span className="flex flex-wrap items-center justify-between gap-2">
          <span>Open incidenten</span>
          <Link href="/incidenten" className="text-sm font-normal text-brand-600 hover:underline dark:text-brand-500">
            Alle incidenten
          </Link>
        </span>
      }
    >
      {q.isLoading && <p className="text-sm text-slate-500">Laden…</p>}
      {q.isError && <Alert>Incidenten konden niet geladen worden.</Alert>}
      {q.data && q.data.items.length === 0 && <p className="text-sm text-slate-500">Geen open incidenten. Alles draait.</p>}
      {q.data && q.data.items.length > 0 && (
        <ul className="divide-y divide-slate-100 text-sm dark:divide-slate-800">
          {q.data.items.map((inc) => (
            <li key={inc.id} className="flex flex-wrap items-center justify-between gap-2 py-2">
              <Link href={incidentHref(inc.id)} className="min-w-0 font-medium text-brand-700 hover:underline dark:text-sky-300">
                <span className="text-slate-500 tabular-nums">#{inc.number}</span> {inc.title}
              </Link>
              <span className="flex shrink-0 gap-1">
                <SeverityBadge severity={inc.severity} />
                <IncidentState inc={inc} />
              </span>
            </li>
          ))}
        </ul>
      )}
    </Card>
  );
}
