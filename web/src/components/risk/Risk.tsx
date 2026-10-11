"use client";

import Link from "next/link";
import { useState, type FormEvent } from "react";
import { Modal } from "@/components/Modal";
import { Alert, Badge, Button, Card, Field, Input, Textarea, cx } from "@/components/ui";
import { ApiError } from "@/lib/api/client";
import {
  cardLabels,
  formatGain,
  gradeTone,
  severityInfo,
  useAcceptFinding,
  useClusterRisk,
  useRemoveAcceptance,
  useRisk,
  useRiskHistory,
  type RiskCard,
  type RiskFinding,
  type RiskPlacedFinding,
  type RiskPoint,
} from "@/lib/risk";

const dateFmt = new Intl.DateTimeFormat("nl-BE", { day: "numeric", month: "long", year: "numeric" });
const timeFmt = new Intl.DateTimeFormat("nl-BE", { hour: "2-digit", minute: "2-digit" });

// GradeBadge toont het cijfer met de score, zoals 'B · 84'.
export function GradeBadge({ score, grade }: { score: number | null; grade: string }) {
  if (score === null || grade === "") return null;
  return (
    <span title={`Health score ${score} van 100`}>
      <Badge tone={gradeTone(grade)}>
        {grade} · {score}
      </Badge>
    </span>
  );
}

// ClusterGrade is het cijfer bovenaan een cluster, met een link naar de kaart
// Gezondheid verderop.
export function ClusterGrade({ clusterId }: { clusterId: string }) {
  const q = useClusterRisk(clusterId);
  if (!q.data || q.data.score === null) return null;
  return (
    <a href="#gezondheid" className="hover:opacity-80">
      <GradeBadge score={q.data.score} grade={q.data.grade} />
    </a>
  );
}

// Sparkline is de trend van de score; een dag zonder score is een gat. De
// schaal volgt de scores, met minstens 20 punten hoogte, zodat een daling
// van een paar punten zichtbaar is.
export function Sparkline({ points, className }: { points: RiskPoint[]; className?: string }) {
  const w = 160;
  const h = 36;
  const scores = points.flatMap((p) => (p.score === null ? [] : [p.score]));
  if (scores.length < 2) return null;
  let lo = Math.max(0, Math.min(...scores) - 5);
  const hi = Math.min(100, Math.max(lo + 20, Math.max(...scores) + 5));
  lo = Math.max(0, Math.min(lo, hi - 20));
  const step = w / Math.max(points.length - 1, 1);
  const y = (s: number) => h - 2 - ((s - lo) / (hi - lo)) * (h - 4);
  const parts: string[][] = [[]];
  points.forEach((p, i) => {
    if (p.score === null) {
      if (parts[parts.length - 1].length > 0) parts.push([]);
      return;
    }
    parts[parts.length - 1].push(`${(i * step).toFixed(1)},${y(p.score).toFixed(1)}`);
  });
  const first = scores[0];
  const last = scores[scores.length - 1];
  return (
    <span className="inline-flex items-center gap-2 text-xs font-normal text-slate-500">
      <svg
        viewBox={`0 0 ${w} ${h}`}
        className={cx("h-9 w-40 text-brand-600 dark:text-sky-400", className)}
        role="img"
        aria-label={`Trend over ${points.length} dagen: van ${first} naar ${last}`}
      >
        {lo < 90 && hi > 90 && (
          <line x1="0" x2={w} y1={y(90)} y2={y(90)} className="stroke-slate-200 dark:stroke-slate-700" strokeDasharray="2 3" />
        )}
        {parts
          .filter((p) => p.length > 0)
          .map((p, i) => (
            <polyline key={i} points={p.join(" ")} fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinejoin="round" />
          ))}
      </svg>
      <span aria-hidden>
        {points.length} dagen: {first} → {last}
      </span>
    </span>
  );
}

function computedLine(at: string | null | undefined) {
  if (!at) return "Nog niet berekend; dat gebeurt enkele minuten na de start.";
  return `Berekend om ${timeFmt.format(new Date(at))}, elke minuut opnieuw.`;
}

// FindingItem is één bevinding met de ernst, de link om ze op te lossen, wat
// oplossen oplevert en Accepteren of Intrekken.
export function FindingItem({
  f,
  gain,
  cluster,
  isAdmin,
  onAccept,
}: {
  f: RiskFinding;
  gain: number;
  cluster?: string;
  isAdmin: boolean;
  onAccept: (f: RiskFinding) => void;
}) {
  const remove = useRemoveAcceptance();
  const sev = severityInfo[f.severity];
  const a = f.accepted;
  return (
    <li
      className={cx(
        "flex flex-col gap-1 py-2 text-sm sm:flex-row sm:items-start sm:justify-between sm:gap-3",
        a && "text-slate-500",
      )}
    >
      <div className="min-w-0 flex-1">
        <p className="break-words">
          <span className="mr-1.5">{a ? <Badge>Geaccepteerd</Badge> : <Badge tone={sev.tone}>{sev.label}</Badge>}</span>
          {cluster && <span className="font-medium">{cluster}: </span>}
          {f.text}
        </p>
        {a && (
          <p className="mt-0.5 text-xs break-words">
            {a.reason}
            {a.expires_at ? ` · tot ${dateFmt.format(new Date(a.expires_at))}` : " · zonder einddatum"}
            {a.by && ` · ${a.by}`}
          </p>
        )}
        {remove.isError && <p className="mt-1 text-xs text-red-600">Intrekken mislukt: {remove.error.message}</p>}
      </div>
      <div className="flex shrink-0 items-center gap-2">
        {!a && gain >= 0.05 && (
          <span
            className="text-xs text-emerald-700 tabular-nums dark:text-emerald-400"
            title="Zoveel stijgt de score als je dit oplost"
          >
            {formatGain(gain)}
          </span>
        )}
        {f.link && (
          <Link href={f.link} className="text-xs text-brand-600 hover:underline dark:text-sky-300">
            Oplossen
          </Link>
        )}
        {isAdmin && !a && (
          <button type="button" className="text-xs text-slate-500 hover:underline" onClick={() => onAccept(f)}>
            Accepteren
          </button>
        )}
        {isAdmin && a && (
          <button
            type="button"
            className="text-xs text-slate-500 hover:underline disabled:opacity-60"
            disabled={remove.isPending}
            onClick={() => remove.mutate(a.id)}
          >
            Intrekken
          </button>
        )}
      </div>
    </li>
  );
}

// AcceptDialog legt vast dat een bevinding bewust blijft staan. Ze kost dan
// geen punten meer, maar blijft grijs zichtbaar.
export function AcceptDialog({ f, onClose }: { f: RiskFinding; onClose: () => void }) {
  const accept = useAcceptFinding();
  const [reason, setReason] = useState("");
  const [until, setUntil] = useState("");
  const submit = (e: FormEvent) => {
    e.preventDefault();
    accept.mutate(
      {
        check: f.check,
        subject_type: f.subject.type,
        subject_id: f.subject.id,
        reason,
        expires_at: until ? new Date(`${until}T23:59:59`).toISOString() : null,
      },
      { onSuccess: onClose },
    );
  };
  return (
    <Modal title="Bevinding accepteren" onClose={onClose}>
      <form onSubmit={submit} className="space-y-4 text-sm">
        <p>{f.text}</p>
        <p className="text-slate-600 dark:text-slate-400">
          Een geaccepteerde bevinding kost geen punten meer, maar blijft grijs in de lijst staan. Zo zie je dat het een bewuste
          keuze is, zoals een labcluster op één host. Accepteren komt in het logboek.
        </p>
        <Field label="Reden" htmlFor="accept-reason">
          <Textarea
            id="accept-reason"
            rows={2}
            required
            minLength={3}
            maxLength={500}
            value={reason}
            onChange={(e) => setReason(e.target.value)}
          />
        </Field>
        <Field label="Tot en met" htmlFor="accept-until" hint="Leeg: tot je ze intrekt. Hoogstens twee jaar.">
          <Input id="accept-until" type="date" value={until} onChange={(e) => setUntil(e.target.value)} />
        </Field>
        {accept.isError && <Alert>{accept.error instanceof ApiError ? accept.error.message : "Accepteren mislukt"}</Alert>}
        <div className="flex justify-end gap-2">
          <Button type="button" variant="ghost" onClick={onClose}>
            Annuleren
          </Button>
          <Button type="submit" disabled={accept.isPending}>
            {accept.isPending ? "Accepteren…" : "Accepteren"}
          </Button>
        </div>
      </form>
    </Modal>
  );
}

// FleetRiskCard staat op het overzicht: de score van het geheel met de trend,
// de drie bevindingen die het meeste opleveren en de rest uitklapbaar.
export function FleetRiskCard({ isAdmin }: { isAdmin: boolean }) {
  const q = useRisk();
  const hist = useRiskHistory();
  const [accepting, setAccepting] = useState<RiskFinding | null>(null);
  const d = q.data;
  const open = (d?.findings ?? []).filter((f) => !f.accepted);
  const top = open.slice(0, 3);
  const rest = (d?.findings ?? []).filter((f) => !top.includes(f));
  const item = (f: RiskPlacedFinding) => (
    <FindingItem
      key={`${f.check}-${f.subject.type}-${f.subject.id}`}
      f={f}
      gain={f.fleet_gain}
      cluster={f.cluster?.name ?? "ClusterForge"}
      isAdmin={isAdmin}
      onAccept={setAccepting}
    />
  );
  return (
    <Card title="Gezondheid">
      {q.isLoading && <p className="text-sm text-slate-500">Laden…</p>}
      {q.isError && <Alert>De health score kon niet geladen worden.</Alert>}
      {d && (
        <div className="space-y-4">
          <div className="flex flex-wrap items-center gap-x-6 gap-y-3">
            <div className="flex items-baseline gap-2">
              <span className={cx("text-3xl font-semibold tabular-nums", d.score === null && "text-slate-400")}>
                {d.score ?? "–"}
              </span>
              <span className="text-sm text-slate-500">/ 100</span>
              {d.grade && <Badge tone={gradeTone(d.grade)}>Cijfer {d.grade}</Badge>}
            </div>
            <Sparkline points={hist.data?.points ?? []} />
            <div className="flex flex-wrap gap-1.5">
              {d.clusters.map((c) => (
                <Link
                  key={c.id}
                  href={`/clusters/detail?id=${c.id}#gezondheid`}
                  className="inline-flex items-center gap-1 text-xs hover:underline"
                >
                  <span>{c.name}</span>
                  {c.grade ? <Badge tone={gradeTone(c.grade)}>{c.grade}</Badge> : <Badge>?</Badge>}
                </Link>
              ))}
            </div>
          </div>
          <p className="text-xs text-slate-500">
            {computedLine(d.computed_at)}
            {d.platform?.score !== undefined && d.platform?.score !== null && <> Platform: {d.platform.score} van 100.</>}{" "}
            <Link href="/risico" className="text-brand-600 hover:underline dark:text-sky-300">
              Alle bevindingen, SPOF&apos;s en wat-als
            </Link>
          </p>
          {d.computed_at && open.length === 0 && (
            <p className="text-sm text-slate-500">Geen open bevindingen. Alles staat zoals het hoort.</p>
          )}
          {top.length > 0 && (
            <div>
              <h3 className="text-sm font-medium">Levert het meeste op</h3>
              <ul className="divide-y divide-slate-100 dark:divide-slate-800">{top.map(item)}</ul>
            </div>
          )}
          {rest.length > 0 && (
            <details className="group rounded-md border border-slate-200 dark:border-slate-800">
              <summary className="flex cursor-pointer list-none items-center gap-2 px-3 py-2 text-sm hover:bg-slate-50 dark:hover:bg-slate-800/50 [&::-webkit-details-marker]:hidden">
                <span aria-hidden className="text-slate-400 transition group-open:rotate-90">
                  ›
                </span>
                Alle bevindingen ({d.findings.length})
              </summary>
              <ul className="divide-y divide-slate-100 border-t border-slate-100 px-3 dark:divide-slate-800 dark:border-slate-800">
                {rest.map(item)}
              </ul>
            </details>
          )}
        </div>
      )}
      {accepting && <AcceptDialog f={accepting} onClose={() => setAccepting(null)} />}
    </Card>
  );
}

function CardPanel({ c, isAdmin, onAccept }: { c: RiskCard; isAdmin: boolean; onAccept: (f: RiskFinding) => void }) {
  const known = c.score !== null;
  return (
    <div className="rounded-md border border-slate-200 p-3 dark:border-slate-800">
      <div className="flex items-baseline justify-between gap-2">
        <h3 className="text-sm font-medium">{cardLabels[c.id]}</h3>
        <span className="text-xs text-slate-500 tabular-nums">
          {known ? `${c.score} / 100` : "onbekend"} · weegt {c.weight}%
        </span>
      </div>
      {known && (
        <div className="mt-1.5 h-1.5 overflow-hidden rounded bg-slate-100 dark:bg-slate-800" aria-hidden>
          <div
            className={cx(
              "h-full rounded",
              (c.score ?? 0) >= 80 ? "bg-emerald-500" : (c.score ?? 0) >= 60 ? "bg-amber-500" : "bg-red-500",
            )}
            style={{ width: `${c.score}%` }}
          />
        </div>
      )}
      {c.note && <p className="mt-1.5 text-xs text-slate-500">{c.note}</p>}
      {c.findings.length > 0 ? (
        <ul className="mt-1 divide-y divide-slate-100 dark:divide-slate-800">
          {c.findings.map((f) => (
            <FindingItem
              key={`${f.check}-${f.subject.type}-${f.subject.id}`}
              f={f}
              gain={f.gain}
              isAdmin={isAdmin}
              onAccept={onAccept}
            />
          ))}
        </ul>
      ) : (
        known && <p className="mt-1.5 text-xs text-slate-500">Niets gevonden.</p>
      )}
    </div>
  );
}

// ClusterRiskCard is de health score van een cluster met de zeven kaarten.
export function ClusterRiskCard({ clusterId, isAdmin }: { clusterId: string; isAdmin: boolean }) {
  const q = useClusterRisk(clusterId);
  const hist = useRiskHistory(clusterId);
  const [accepting, setAccepting] = useState<RiskFinding | null>(null);
  const d = q.data;
  return (
    <section id="gezondheid" className="scroll-mt-4">
      <Card
        title={
          <span className="flex flex-wrap items-center justify-between gap-2">
            <span className="flex items-center gap-2">
              Gezondheid
              {d && <GradeBadge score={d.score} grade={d.grade} />}
            </span>
            <Sparkline points={hist.data?.points ?? []} className="h-7 w-32" />
          </span>
        }
      >
        {q.isLoading && <p className="text-sm text-slate-500">Laden…</p>}
        {q.isError && <Alert>De health score kon niet geladen worden.</Alert>}
        {d && (
          <div className="space-y-3">
            <p className="text-xs text-slate-500">{computedLine(d.computed_at)}</p>
            {d.cap && (
              <Alert kind="info">
                De score is hoogstens {d.cap.max}: {d.cap.reason}.
              </Alert>
            )}
            {d.cards.length > 0 && (
              <div className="grid gap-3 lg:grid-cols-2">
                {d.cards.map((c) => (
                  <CardPanel key={c.id} c={c} isAdmin={isAdmin} onAccept={setAccepting} />
                ))}
              </div>
            )}
          </div>
        )}
        {accepting && <AcceptDialog f={accepting} onClose={() => setAccepting(null)} />}
      </Card>
    </section>
  );
}
