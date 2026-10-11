"use client";

import Link from "next/link";
import { useSearchParams } from "next/navigation";
import { Suspense, useState } from "react";
import { AskButton } from "@/components/assistant/AskButton";
import { IncidentAnalysisCard } from "@/components/assistant/IncidentAnalysis";
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
import type { IncidentAnalysis, TimelineClass, TimelineEntry } from "@/lib/timeline";
import { AnalysisCard, ClassFilter, TimelineList, WindowCharts } from "@/components/whatbroke/WhatBroke";

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
            actions={<AskButton subject="incident" id={q.data.incident.id} />}
          />
          <Facts inc={q.data.incident} />
          <AnalysisCard analysis={q.data.analysis} />
          <IncidentAnalysisCard key={q.data.incident.id} id={q.data.incident.id} ai={q.data.ai} />
          <Subjects items={q.data.incident.subjects} />
          {q.data.analysis && <Charts inc={q.data.incident} analysis={q.data.analysis} timeline={q.data.timeline} />}
          <TimelineCard
            entries={q.data.timeline}
            more={q.data.timeline_more}
            firstSymptom={q.data.analysis?.first_symptom.entry_id}
          />
          <NoteCard key={q.data.incident.id} inc={q.data.incident} isAdmin={isAdmin} />
        </div>
      )}
    </QueryState>
  );
}

// TimelineCard is de tijdlijn van het incident: wat er veranderde en wat er
// misging, van twee uur voor het eerste symptoom tot het einde.
function TimelineCard({ entries, more, firstSymptom }: { entries: TimelineEntry[]; more: boolean; firstSymptom?: string }) {
  const [shown, setShown] = useState<TimelineClass[]>(["change", "symptom"]);
  const items = entries.filter((e) => shown.includes(e.class) || e.id === firstSymptom);
  return (
    <Card title="Tijdlijn">
      <div className="space-y-3">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <p className="text-sm text-slate-500">
            Het logboek, het journaal van de nodes en de afwijkingen in de metrics, oudste eerst.
          </p>
          <ClassFilter value={shown} onChange={setShown} />
        </div>
        <TimelineList entries={items} firstSymptom={firstSymptom} empty="Niets in de tijdlijn met deze filters." />
        {more && <p className="text-sm text-slate-500">De tijdlijn had meer dan 1000 regels; de oudste ontbreken.</p>}
      </div>
    </Card>
  );
}

// Charts toont de grafieken rond het eerste symptoom: van de node waar het
// begon, anders van het geraakte cluster.
function Charts({ inc, analysis, timeline }: { inc: Incident; analysis: IncidentAnalysis; timeline: TimelineEntry[] }) {
  const first = analysis.first_symptom;
  const entry = timeline.find((e) => e.id === first.entry_id);
  const cluster = inc.subjects.find((s) => s.type === "cluster" && s.present);
  const node = inc.subjects.find((s) => s.type === "node" && s.present);
  let target: { kind: "node" | "cluster"; id: string; name: string } | null = null;
  if (entry?.node_id) target = { kind: "node", id: entry.node_id, name: entry.hostname };
  else if (node) target = { kind: "node", id: node.id, name: node.name };
  else if (cluster) target = { kind: "cluster", id: cluster.id, name: cluster.name };
  if (!target) return null;
  // Van drie kwartier voor tot een kwartier na het eerste symptoom, op de
  // minuut. Wat nog in de toekomst ligt, blijft leeg.
  const minute = 60_000;
  const from = Math.floor((new Date(first.from).getTime() - 45 * minute) / minute) * minute;
  const to = Math.ceil((new Date(first.to).getTime() + 15 * minute) / minute) * minute;
  if (to <= from) return null;
  return (
    <WindowCharts
      kind={target.kind}
      id={target.id}
      name={target.name}
      from={new Date(from).toISOString()}
      to={new Date(to).toISOString()}
      marker={first.from}
    />
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
        <dt className={dtClass}>Kaart</dt>
        <dd>
          <Link href={`/kaart?incident=${inc.id}`} className={linkClass}>
            Wat het raakte, op de kaart
          </Link>
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
