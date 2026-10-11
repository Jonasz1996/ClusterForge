"use client";

import Link from "next/link";
import { useSearchParams } from "next/navigation";
import { Suspense, useState } from "react";
import { AuditList } from "@/components/audit/AuditList";
import { IncidentState, SeverityBadge } from "@/components/incidents/Incidents";
import { formatDuration, QueryState, tableClass, tdClass, thClass } from "@/components/inventory/bits";
import { Alert, Badge, Button, Card, PageHeader, Textarea } from "@/components/ui";
import { auditTimeFmt } from "@/lib/audit";
import {
  statusLabel,
  statusTone,
  subjectHref,
  subjectTypes,
  useIncident,
  useSetIncidentNote,
  type Incident,
  type IncidentSubject,
} from "@/lib/incidents";
import { useIsAdmin } from "@/lib/inventory";

export default function IncidentPage() {
  return (
    <Suspense>
      <IncidentInner />
    </Suspense>
  );
}

const dlClass = "grid gap-x-4 gap-y-2.5 text-sm sm:grid-cols-[9rem_1fr]";
const dtClass = "text-slate-500";
const linkClass = "text-brand-600 hover:underline dark:text-brand-500";

function IncidentInner() {
  const id = useSearchParams().get("id") ?? "";
  const q = useIncident(id);
  const isAdmin = useIsAdmin();
  if (id === "") return <Alert>Geen incident opgegeven.</Alert>;

  return (
    <QueryState q={q}>
      {q.data && (
        <div className="space-y-6">
          <div className="text-sm">
            <Link href="/incidenten" className="text-slate-500 hover:underline">
              ← Incidenten
            </Link>
          </div>
          <PageHeader
            title={
              <>
                <span className="text-slate-400 tabular-nums">#{q.data.incident.number}</span> {q.data.incident.title}
              </>
            }
            description={
              <span className="inline-flex flex-wrap items-center gap-2">
                <SeverityBadge severity={q.data.incident.severity} />
                <IncidentState inc={q.data.incident} />
                {q.data.incident.caused_by_test && <Badge tone="blue">door een failovertest</Badge>}
              </span>
            }
          />
          <Facts inc={q.data.incident} />
          <Subjects items={q.data.incident.subjects} />
          <NoteCard key={q.data.incident.id} inc={q.data.incident} isAdmin={isAdmin} />
          <Card title="Geschiedenis">
            <p className="mb-3 text-sm text-slate-500">
              Statusovergangen, VIP-wissels en tests over wat geraakt was, van vijf minuten voor het begin tot het einde.
            </p>
            <AuditList items={q.data.history} compact />
            {q.data.history_more && (
              <p className="mt-2 text-sm text-slate-500">Er waren meer regels dan hier staan; de eerste 200 zijn getoond.</p>
            )}
          </Card>
        </div>
      )}
    </QueryState>
  );
}

function Facts({ inc }: { inc: Incident }) {
  const late = (new Date(inc.detected_at).getTime() - new Date(inc.started_at).getTime()) / 1000;
  return (
    <Card>
      <dl className={dlClass}>
        <dt className={dtClass}>Begonnen</dt>
        <dd className="tabular-nums">
          {auditTimeFmt.format(new Date(inc.started_at))}
          {late >= 60 && <span className="text-slate-500"> · gezien {formatDuration(late)} later</span>}
        </dd>
        <dt className={dtClass}>{inc.resolved_at ? "Opgelost" : "Einde"}</dt>
        <dd className="tabular-nums">
          {inc.resolved_at ? (
            auditTimeFmt.format(new Date(inc.resolved_at))
          ) : (
            <span>Loopt nog. Het sluit als alles vijf minuten gezond is.</span>
          )}
        </dd>
        {inc.test_run_id && (
          <>
            <dt className={dtClass}>Test</dt>
            <dd>
              <Link href={`/tests/detail?id=${inc.test_run_id}`} className={linkClass}>
                De failovertest die liep
              </Link>
            </dd>
          </>
        )}
      </dl>
    </Card>
  );
}

function StatusChip({ status }: { status: string }) {
  return <Badge tone={statusTone(status)}>{statusLabel(status)}</Badge>;
}

function SubjectName({ s }: { s: IncidentSubject }) {
  const href = subjectHref(s);
  return (
    <>
      <span className="text-slate-500">{subjectTypes[s.type]} </span>
      {href ? (
        <Link href={href} className={linkClass}>
          {s.name}
        </Link>
      ) : (
        <span>
          {s.name} <span className="text-slate-500">(verwijderd)</span>
        </span>
      )}
      {s.reason && <div className="max-w-md text-xs break-words text-slate-500">{s.reason}</div>}
    </>
  );
}

function Subjects({ items }: { items: IncidentSubject[] }) {
  return (
    <Card title="Wat er geraakt was">
      {/* Op een smal scherm een lijst, anders een tabel. */}
      <ul className="divide-y divide-slate-100 text-sm sm:hidden dark:divide-slate-800">
        {items.map((s) => (
          <li key={`${s.type}-${s.id}`} className="space-y-1.5 py-2">
            <div>
              <SubjectName s={s} />
            </div>
            <div className="flex flex-wrap items-center gap-1">
              <StatusChip status={s.first_status} />
              {s.worst_status !== s.first_status && (
                <>
                  <span className="text-slate-400">→</span>
                  <StatusChip status={s.worst_status} />
                </>
              )}
              <span className="text-slate-400">· laatst</span>
              <StatusChip status={s.last_status} />
            </div>
            <div className="text-xs text-slate-500 tabular-nums">
              {auditTimeFmt.format(new Date(s.since))} tot {s.until ? auditTimeFmt.format(new Date(s.until)) : "nu"}
            </div>
          </li>
        ))}
      </ul>
      <div className="-mx-3 hidden overflow-x-auto sm:block">
        <table className={tableClass}>
          <thead className="border-b border-slate-200 dark:border-slate-800">
            <tr>
              <th className={thClass}>Onderwerp</th>
              <th className={thClass}>Eerst</th>
              <th className={thClass}>Ergst</th>
              <th className={thClass}>Laatst</th>
              <th className={thClass}>Van</th>
              <th className={thClass}>Tot</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
            {items.map((s) => {
              return (
                <tr key={`${s.type}-${s.id}`}>
                  <td className={tdClass}>
                    <SubjectName s={s} />
                  </td>
                  <td className={tdClass}>
                    <StatusChip status={s.first_status} />
                  </td>
                  <td className={tdClass}>
                    <StatusChip status={s.worst_status} />
                  </td>
                  <td className={tdClass}>
                    <StatusChip status={s.last_status} />
                  </td>
                  <td className={`${tdClass} whitespace-nowrap tabular-nums`}>{auditTimeFmt.format(new Date(s.since))}</td>
                  <td className={`${tdClass} whitespace-nowrap tabular-nums`}>
                    {s.until ? auditTimeFmt.format(new Date(s.until)) : <span className="text-slate-500">nog bezig</span>}
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
    </Card>
  );
}

// NoteCard is de notitie bij een incident: wat er gedaan of afgesproken is.
// Alleen een beheerder schrijft ze.
function NoteCard({ inc, isAdmin }: { inc: Incident; isAdmin: boolean }) {
  const save = useSetIncidentNote(inc.id);
  // null zolang er niets getypt is; dan volgt het veld de server.
  const [text, setText] = useState<string | null>(null);
  const value = text ?? inc.note;
  if (!isAdmin) {
    return (
      <Card title="Notitie">
        {inc.note ? (
          <p className="text-sm whitespace-pre-wrap">{inc.note}</p>
        ) : (
          <p className="text-sm text-slate-500">Geen notitie.</p>
        )}
      </Card>
    );
  }
  return (
    <Card title="Notitie">
      <form
        className="space-y-3"
        onSubmit={(e) => {
          e.preventDefault();
          save.mutate(value, { onSuccess: () => setText(null) });
        }}
      >
        <Textarea
          aria-label="Notitie"
          rows={4}
          maxLength={10000}
          placeholder="Wat je zag, wat je deed, wat er nog moet gebeuren."
          value={value}
          onChange={(e) => setText(e.target.value)}
        />
        {save.isError && <Alert>{save.error instanceof Error ? save.error.message : "Opslaan mislukt"}</Alert>}
        <div className="flex items-center gap-3">
          <Button type="submit" disabled={save.isPending || text === null || text === inc.note}>
            {save.isPending ? "Opslaan…" : "Opslaan"}
          </Button>
          {save.isSuccess && text === null && <span className="text-sm text-slate-500">Opgeslagen.</span>}
        </div>
      </form>
    </Card>
  );
}
