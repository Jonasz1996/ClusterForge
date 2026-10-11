"use client";

import Link from "next/link";
import { useState } from "react";
import { upgradeCommand } from "@/components/drift/Drift";
import { CopyBlock } from "@/components/inventory/InstallAgent";
import { Alert, Badge, Card, Select } from "@/components/ui";
import { auditTimeFmt } from "@/lib/audit";
import { changeKinds, changePeriods, kindLabels, sourceLabels, useNodeChanges, type NodeChange } from "@/lib/journal";

// toneOf kleurt wat aandacht vraagt: een dienst die faalde of een proces dat
// het geheugen opat in rood, een herstart in oranje.
function toneOf(c: NodeChange) {
  if (c.kind === "oom" || (c.kind === "service" && c.action === "failed")) return "red";
  if (c.kind === "boot" || c.kind === "agent") return "amber";
  if (c.source === "clusterforge") return "blue";
  return "slate";
}

// NodeChangesCard toont het journaal van een node: wat er op de machine
// veranderde, door wie, en hoe ClusterForge het weet.
export function NodeChangesCard({ nodeId }: { nodeId: string }) {
  const [kind, setKind] = useState("");
  const [period, setPeriod] = useState("7d");
  const hours = changePeriods.find((p) => p.value === period)?.hours ?? 24 * 7;
  const q = useNodeChanges(nodeId, kind, hours);
  const filters = (
    <div className="flex flex-wrap gap-2">
      <Select aria-label="Soort" className="w-36" value={kind} onChange={(e) => setKind(e.target.value)}>
        {changeKinds.map((k) => (
          <option key={k.value} value={k.value}>
            {k.label}
          </option>
        ))}
      </Select>
      <Select aria-label="Periode" className="w-32" value={period} onChange={(e) => setPeriod(e.target.value)}>
        {changePeriods.map((p) => (
          <option key={p.value} value={p.value}>
            {p.label}
          </option>
        ))}
      </Select>
    </div>
  );
  return (
    <Card title="Wijzigingen">
      <div className="space-y-3 text-sm">
        {filters}
        {(q.data?.agents_too_old.length ?? 0) > 0 && (
          <div className="space-y-2 rounded border border-amber-200 bg-amber-50 px-3 py-2 text-amber-900 dark:border-amber-900 dark:bg-amber-950 dark:text-amber-200">
            <p>
              <span className="font-medium">Agent te oud voor het journaal.</span> Pakketten, bestanden, logins en
              herstarts ziet ClusterForge pas na een update van cf-agent. Werk hem bij met dit commando op de node; de
              aanmelding blijft staan.
            </p>
            <CopyBlock text={upgradeCommand()} />
          </div>
        )}
        {q.isError && <Alert>{q.error instanceof Error ? q.error.message : "Wijzigingen laden mislukt"}</Alert>}
        {q.data && <ChangeList items={q.data.changes} />}
        {q.data?.has_more && (
          <p className="text-slate-500">Er zijn meer wijzigingen; kies een kortere periode of een soort.</p>
        )}
      </div>
    </Card>
  );
}

export function ChangeList({ items, showNode = false }: { items: NodeChange[]; showNode?: boolean }) {
  if (items.length === 0) return <p className="text-slate-500">Geen wijzigingen in deze periode.</p>;
  return (
    <ul className="divide-y divide-slate-100 dark:divide-slate-800">
      {items.map((c) => (
        <ChangeRow key={c.id} c={c} showNode={showNode} />
      ))}
    </ul>
  );
}

function ChangeRow({ c, showNode }: { c: NodeChange; showNode: boolean }) {
  const [open, setOpen] = useState(false);
  return (
    <li className="py-2">
      <div className="flex items-start gap-3">
        <button
          type="button"
          onClick={() => setOpen(!open)}
          aria-expanded={open}
          aria-label={open ? "Details verbergen" : "Details tonen"}
          className="mt-0.5 shrink-0 rounded px-1 text-xs text-slate-400 hover:bg-slate-100 hover:text-slate-700 dark:hover:bg-slate-800"
        >
          {open ? "▾" : "▸"}
        </button>
        <div className="grid min-w-0 flex-1 gap-x-3 gap-y-0.5 sm:grid-cols-[9.5rem_5.5rem_1fr]">
          <time className="text-xs whitespace-nowrap text-slate-500 tabular-nums sm:text-sm" dateTime={c.ts}>
            {auditTimeFmt.format(new Date(c.ts))}
          </time>
          <span>
            <Badge tone={toneOf(c)}>{kindLabels[c.kind] ?? c.kind}</Badge>
          </span>
          <span className="min-w-0 break-words">
            {showNode && (
              <Link href={`/nodes/detail?id=${c.node_id}`} className="font-medium hover:underline">
                {c.hostname}
              </Link>
            )}
            {showNode && ": "}
            {c.summary}
            {c.job_id && (
              <>
                {" · "}
                <Link href={`/taken/detail?id=${c.job_id}`} className="text-brand-600 hover:underline dark:text-brand-500">
                  taak
                </Link>
              </>
            )}
          </span>
        </div>
      </div>
      {open && <ChangeDetails c={c} />}
    </li>
  );
}

function ChangeDetails({ c }: { c: NodeChange }) {
  const rows = Object.entries(c.detail).sort(([a], [b]) => a.localeCompare(b));
  // Het tijdstip van de wijziging komt van de node; ClusterForge zag ze
  // pas later als de agent offline was.
  const late = new Date(c.observed_at).getTime() - new Date(c.ts).getTime() > 5 * 60 * 1000;
  return (
    <dl className="mt-2 ml-8 grid grid-cols-[8rem_1fr] gap-x-3 gap-y-1 rounded bg-slate-50 p-3 text-xs dark:bg-slate-800/50">
      <dt className="text-slate-500">Bron</dt>
      <dd>{sourceLabels[c.source]}</dd>
      <dt className="text-slate-500">Onderwerp</dt>
      <dd className="font-mono break-all">{c.subject}</dd>
      {late && (
        <>
          <dt className="text-slate-500">Gezien</dt>
          <dd>{auditTimeFmt.format(new Date(c.observed_at))}</dd>
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
