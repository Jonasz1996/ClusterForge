"use client";

import Link from "next/link";
import { useState } from "react";
import { TimeChart } from "@/components/charts/TimeChart";
import { Alert, Badge, Card, cx } from "@/components/ui";
import { ApiError } from "@/lib/api/client";
import { auditTimeFmt } from "@/lib/audit";
import {
  classes,
  classOrder,
  kindLabels,
  levels,
  shortSha,
  sourceLabels,
  useMetricsWindow,
  useTimeline,
  type IncidentAnalysis,
  type IncidentCause,
  type TimelineClass,
  type TimelineEntry,
} from "@/lib/timeline";

const linkClass = "text-brand-600 hover:underline dark:text-brand-500";

const secondsFmt = new Intl.DateTimeFormat("nl-BE", { hour: "2-digit", minute: "2-digit", second: "2-digit" });

// between zegt in welk venster iets waarschijnlijk gebeurde: 'tussen 14:01:05
// en 14:02:40'.
function between(from: string, to: string) {
  return `tussen ${secondsFmt.format(new Date(from))} en ${secondsFmt.format(new Date(to))}`;
}

// ClassFilter zet klassen aan en uit.
export function ClassFilter({ value, onChange }: { value: TimelineClass[]; onChange: (v: TimelineClass[]) => void }) {
  return (
    <div className="inline-flex flex-wrap gap-1.5" role="group" aria-label="Klassen">
      {classOrder.map((c) => {
        const on = value.includes(c);
        return (
          <button
            key={c}
            type="button"
            aria-pressed={on}
            onClick={() => onChange(on ? value.filter((x) => x !== c) : classOrder.filter((x) => x === c || value.includes(x)))}
            className={cx(
              "inline-flex items-center gap-1.5 rounded-full border px-2.5 py-1 text-xs font-medium",
              on
                ? "border-slate-300 bg-white text-slate-800 dark:border-slate-600 dark:bg-slate-800 dark:text-slate-100"
                : "border-transparent text-slate-400 line-through hover:text-slate-600 dark:hover:text-slate-300",
            )}
          >
            <span className={cx("size-2 rounded-full", classes[c].dot)} aria-hidden />
            {classes[c].plural}
          </button>
        );
      })}
    </div>
  );
}

// TimelineList toont regels van de tijdlijn met hun klasse, soort en, als
// het moment niet exact is, het venster waarin het waarschijnlijk gebeurde.
export function TimelineList({
  entries,
  firstSymptom,
  newestFirst = false,
  empty = "Niets in deze periode.",
}: {
  entries: TimelineEntry[];
  firstSymptom?: string;
  newestFirst?: boolean;
  empty?: string;
}) {
  if (entries.length === 0) return <p className="text-sm text-slate-500">{empty}</p>;
  const items = newestFirst ? [...entries].reverse() : entries;
  return (
    <ol className="relative space-y-0 text-sm">
      {items.map((e) => (
        <TimelineRow key={e.id} e={e} first={e.id === firstSymptom} />
      ))}
    </ol>
  );
}

function TimelineRow({ e, first }: { e: TimelineEntry; first: boolean }) {
  const [open, setOpen] = useState(false);
  const windowed = e.from !== e.to && e.delay !== "";
  return (
    <li
      className={cx(
        "border-l-2 py-1.5 pl-3",
        first ? "border-red-500 bg-red-50/60 dark:bg-red-950/30" : "border-slate-100 dark:border-slate-800",
      )}
    >
      <div className="flex items-start gap-2">
        <button
          type="button"
          onClick={() => setOpen(!open)}
          aria-expanded={open}
          aria-label={open ? "Details verbergen" : "Details tonen"}
          className="mt-0.5 shrink-0 rounded px-1 text-xs text-slate-400 hover:bg-slate-100 hover:text-slate-700 dark:hover:bg-slate-800"
        >
          {open ? "▾" : "▸"}
        </button>
        <div className="grid min-w-0 flex-1 gap-x-3 gap-y-0.5 sm:grid-cols-[9.5rem_7rem_1fr]">
          <time className="text-xs whitespace-nowrap text-slate-500 tabular-nums sm:text-sm" dateTime={e.ts}>
            {auditTimeFmt.format(new Date(e.ts))}
          </time>
          <span className="flex items-center gap-1.5">
            <span className={cx("size-2 shrink-0 rounded-full", classes[e.class].dot)} title={classes[e.class].label} />
            <Badge tone={classes[e.class].tone}>{kindLabels[e.kind] ?? e.kind}</Badge>
          </span>
          <span className="min-w-0 break-words">
            {first && <span className="mr-1.5 font-medium text-red-700 dark:text-red-300">Eerste symptoom ·</span>}
            {e.hostname && e.source !== "log" && !e.summary.includes(e.hostname) && (
              <span className="font-medium">{e.hostname}: </span>
            )}
            {e.link ? (
              <Link href={e.link} className="hover:underline">
                {e.summary}
              </Link>
            ) : (
              e.summary
            )}
            {windowed && (
              <span className="block text-xs text-slate-500" title={e.delay}>
                waarschijnlijk {between(e.from, e.to)}
              </span>
            )}
          </span>
        </div>
      </div>
      {open && <EntryDetails e={e} />}
    </li>
  );
}

function EntryDetails({ e }: { e: TimelineEntry }) {
  const rows = Object.entries(e.detail).sort(([a], [b]) => a.localeCompare(b));
  return (
    <dl className="mt-2 ml-7 grid grid-cols-[8rem_1fr] gap-x-3 gap-y-1 rounded bg-slate-50 p-3 text-xs dark:bg-slate-800/50">
      <dt className="text-slate-500">Klasse</dt>
      <dd>{classes[e.class].label}</dd>
      <dt className="text-slate-500">Bron</dt>
      <dd>{sourceLabels[e.source]}</dd>
      {e.delay && (
        <>
          <dt className="text-slate-500">Venster</dt>
          <dd>
            {between(e.from, e.to)}, omdat {e.delay}
          </dd>
        </>
      )}
      {e.cluster && (
        <>
          <dt className="text-slate-500">Cluster</dt>
          <dd>
            {e.cluster_id ? (
              <Link href={`/clusters/detail?id=${e.cluster_id}`} className={linkClass}>
                {e.cluster}
              </Link>
            ) : (
              e.cluster
            )}
          </dd>
        </>
      )}
      {e.job_id && (
        <>
          <dt className="text-slate-500">Taak</dt>
          <dd>
            <Link href={`/taken/detail?id=${e.job_id}`} className={linkClass}>
              {e.job || "bekijken"}
            </Link>
          </dd>
        </>
      )}
      {e.subject && (
        <>
          <dt className="text-slate-500">Onderwerp</dt>
          <dd className="font-mono break-all">{e.subject}</dd>
        </>
      )}
      {rows.map(([k, v]) => (
        <div key={k} className="contents">
          <dt className="text-slate-500">{k}</dt>
          <dd className="font-mono break-all">{v}</dd>
        </div>
      ))}
    </dl>
  );
}

// AnalysisCard is de uitkomst van What Broke: de conclusie, het eerste
// symptoom, de oorzaken met hun redenen en de afwijkingen in de metrics.
export function AnalysisCard({ analysis }: { analysis: IncidentAnalysis | null }) {
  if (!analysis) {
    return (
      <Card title="Wat ging er mis">
        <p className="text-sm text-slate-500">De analyse wordt gerekend; dit scherm werkt vanzelf bij.</p>
      </Card>
    );
  }
  const first = analysis.first_symptom;
  return (
    <Card title="Wat ging er mis">
      <div className="space-y-4 text-sm">
        <p className="text-base font-medium">{analysis.conclusion}</p>
        <div className="rounded border-l-2 border-red-500 bg-red-50/60 px-3 py-2 dark:bg-red-950/30">
          <div className="text-xs font-medium text-red-700 uppercase dark:text-red-300">Eerste symptoom</div>
          <div>{first.summary}</div>
          <div className="text-xs text-slate-500">
            {first.from !== first.to ? (
              <>
                Waarschijnlijk {between(first.from, first.to)}
                {first.why && <>, omdat {first.why}</>}
              </>
            ) : (
              auditTimeFmt.format(new Date(first.ts))
            )}
          </div>
        </div>
        {analysis.causes.length > 0 && (
          <div className="space-y-2">
            <h3 className="font-medium">Mogelijke oorzaken</h3>
            <ol className="space-y-2">
              {analysis.causes.map((c) => (
                <CauseItem key={c.entry_id} c={c} />
              ))}
            </ol>
          </div>
        )}
        {analysis.anomalies.length > 0 && (
          <div className="space-y-1">
            <h3 className="font-medium">Afwijkingen in de metrics</h3>
            <ul className="list-disc space-y-0.5 pl-5">
              {analysis.anomalies.map((a) => (
                <li key={`${a.node_id}-${a.metric}-${a.label}`}>
                  {a.summary} <span className="text-xs text-slate-500">vanaf {secondsFmt.format(new Date(a.at))}</span>
                </li>
              ))}
            </ul>
          </div>
        )}
        {analysis.consumers.length > 0 && (
          <p>
            <span className="text-slate-500">Hangen af van wat geraakt was: </span>
            {analysis.consumers.map((c, i) => (
              <span key={c.id}>
                {i > 0 && ", "}
                <Link href={`/clusters/detail?id=${c.id}`} className={linkClass}>
                  {c.name}
                </Link>
              </span>
            ))}
          </p>
        )}
        <p className="text-xs text-slate-500">
          Gezocht van {auditTimeFmt.format(new Date(analysis.window_from))} tot{" "}
          {auditTimeFmt.format(new Date(analysis.window_to))}: {analysis.changes}{" "}
          {analysis.changes === 1 ? "verandering" : "veranderingen"}.
          {analysis.metrics_unknown && " De metrics waren niet te lezen."} Vaste regels zonder AI; bijgewerkt{" "}
          {auditTimeFmt.format(new Date(analysis.analyzed_at))}. Tot een uur na het einde rekent ClusterForge opnieuw als er
          gegevens bijkomen.
        </p>
      </div>
    </Card>
  );
}

function CauseItem({ c }: { c: IncidentCause }) {
  const level = levels[c.level];
  return (
    <li className="rounded-md border border-slate-200 p-3 dark:border-slate-700">
      <div className="flex flex-wrap items-center gap-1.5">
        <Badge tone={level.tone}>{level.label}</Badge>
        <Badge>{kindLabels[c.kind] ?? c.kind}</Badge>
        {c.hostname && <span className="text-xs text-slate-500">{c.hostname}</span>}
        {c.cluster && <span className="text-xs text-slate-500">· {c.cluster}</span>}
        <span className="ml-auto text-xs text-slate-400 tabular-nums">{auditTimeFmt.format(new Date(c.ts))}</span>
      </div>
      <div className="mt-1.5 font-medium break-words">
        {c.link ? (
          <Link href={c.link} className="hover:underline">
            {c.summary}
          </Link>
        ) : (
          c.summary
        )}
        {c.commit && (
          <span className="ml-2 rounded bg-slate-100 px-1 font-mono text-xs dark:bg-slate-800" title={c.commit}>
            {shortSha(c.commit)}
          </span>
        )}
      </div>
      <ul className="mt-1 list-disc space-y-0.5 pl-5 text-slate-600 dark:text-slate-400">
        {c.reasons.map((r) => (
          <li key={r}>{r}</li>
        ))}
      </ul>
    </li>
  );
}

// WindowCharts toont de grafieken van een node of cluster rond het eerste
// symptoom, met een rode lijn op dat moment.
export function WindowCharts({
  kind,
  id,
  name,
  from,
  to,
  marker,
}: {
  kind: "node" | "cluster";
  id: string;
  name: string;
  from: string;
  to: string;
  marker: string;
}) {
  const q = useMetricsWindow(kind, id, from, to);
  const disabled = q.error instanceof ApiError && q.error.code === "metrics_disabled";
  const wanted = kind === "node" ? ["cpu", "memory", "disk", "network"] : ["cpu", "memory", "load", "network"];
  return (
    <Card title={`Grafieken van ${name} rond het eerste symptoom`}>
      {disabled && <p className="text-sm text-slate-500">Grafieken staan uit omdat er geen VictoriaMetrics is ingesteld.</p>}
      {q.isError && !disabled && <Alert>{q.error instanceof Error ? q.error.message : "Grafieken laden mislukt"}</Alert>}
      {q.isLoading && <p className="text-sm text-slate-500">Laden…</p>}
      {q.data && (
        <div className="grid gap-4 md:grid-cols-2">
          {q.data.panels
            .filter((p) => wanted.includes(p.id))
            .map((p) => (
              <div key={p.id}>
                <div className="mb-1 text-xs font-medium text-slate-500">{p.title}</div>
                {p.series.some((s) => s.values.some((v) => v !== null)) ? (
                  <TimeChart
                    timestamps={q.data.timestamps}
                    series={p.series}
                    unit={p.unit}
                    height={140}
                    marker={new Date(marker).getTime() / 1000}
                  />
                ) : (
                  <p className="py-6 text-center text-sm text-slate-500">Geen gegevens in dit venster.</p>
                )}
              </div>
            ))}
        </div>
      )}
    </Card>
  );
}

// RecentChangesCard is 'Wat is er veranderd' op een cluster: de
// veranderingen van de laatste 24 uur, nieuwste eerst.
export function RecentChangesCard({ kind, id }: { kind: "node" | "cluster"; id: string }) {
  const q = useTimeline(kind, id, { hours: 24, classes: ["change"], limit: 10 });
  return (
    <Card
      title={
        <span className="flex flex-wrap items-center justify-between gap-2">
          <span>Wat is er veranderd</span>
          <Link
            href={`/tijdlijn?${kind}=${id}`}
            className="text-sm font-normal text-brand-600 hover:underline dark:text-brand-500"
          >
            Volledige tijdlijn
          </Link>
        </span>
      }
    >
      {q.isLoading && <p className="text-sm text-slate-500">Laden…</p>}
      {q.isError && <Alert>{q.error instanceof Error ? q.error.message : "Tijdlijn laden mislukt"}</Alert>}
      {q.data && (
        <>
          <TimelineList entries={q.data.entries} newestFirst empty="Niets veranderd in de laatste 24 uur." />
          {q.data.has_more && <p className="mt-2 text-sm text-slate-500">De laatste tien; de rest staat in de tijdlijn.</p>}
        </>
      )}
    </Card>
  );
}
