"use client";

import Link from "next/link";
import { useSearchParams } from "next/navigation";
import { Suspense, useState } from "react";
import { RunBadge, reportHref } from "@/components/failover/Failover";
import { QueryState, tableClass, tdClass, thClass } from "@/components/inventory/bits";
import { PageHeader, Select } from "@/components/ui";
import type { components } from "@/lib/api/schema";
import { runResults, type TestRun } from "@/lib/failover";
import { useTestRuns } from "@/lib/verify";

type Kind = components["schemas"]["TestRunKind"];
type Result = components["schemas"]["TestRunResult"];

const kinds: Record<Kind, string> = {
  "failover.test": "Failovertest",
  "disaster.run": "Rampoefening",
  "backup.verify": "Back-upcontrole",
};

const fmt = new Intl.DateTimeFormat("nl-BE", { dateStyle: "medium", timeStyle: "short" });
const linkClass = "text-brand-600 hover:underline dark:text-brand-500";

export default function TestsPage() {
  return (
    <Suspense>
      <TestsInner />
    </Suspense>
  );
}

function TestsInner() {
  const params = useSearchParams();
  const [kind, setKind] = useState<Kind | "">(() => {
    const k = params.get("kind");
    return k && k in kinds ? (k as Kind) : "";
  });
  const [result, setResult] = useState<Result | "">("");
  const q = useTestRuns({ kind: kind || undefined, result: result || undefined, limit: 200 });

  return (
    <div className="space-y-6">
      <PageHeader
        title="Tests"
        description="Elke failovertest, rampoefening en back-upcontrole met zijn uitkomst. Nieuwste eerst; klik een run voor het rapport."
      />
      <div className="flex flex-wrap items-center gap-3">
        <Select aria-label="Soort" className="w-48" value={kind} onChange={(e) => setKind(e.target.value as Kind | "")}>
          <option value="">Alle soorten</option>
          {Object.entries(kinds).map(([k, label]) => (
            <option key={k} value={k}>
              {label}
            </option>
          ))}
        </Select>
        <Select aria-label="Uitkomst" className="w-44" value={result} onChange={(e) => setResult(e.target.value as Result | "")}>
          <option value="">Elke uitkomst</option>
          {Object.entries(runResults).map(([r, { label }]) => (
            <option key={r} value={r}>
              {label}
            </option>
          ))}
        </Select>
      </div>
      <QueryState q={q}>
        {q.data && (
          <>
            <RunTable runs={q.data} empty={kind || result ? "Geen runs met deze filters." : "Nog geen tests gedraaid."} />
            {q.data.length === 200 && (
              <p className="text-sm text-slate-500">Alleen de nieuwste 200 runs; kies een filter voor oudere.</p>
            )}
          </>
        )}
      </QueryState>
    </div>
  );
}

function RunTable({ runs, empty }: { runs: TestRun[]; empty: string }) {
  return (
    <div className="overflow-x-auto rounded-lg border border-slate-200 bg-white dark:border-slate-800 dark:bg-slate-900">
      <table className={tableClass}>
        <thead className="border-b border-slate-200 dark:border-slate-800">
          <tr>
            <th className={thClass}>Uitkomst</th>
            <th className={thClass}>Wat</th>
            <th className={thClass}>Cluster</th>
            <th className={thClass}>Gestart</th>
          </tr>
        </thead>
        <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
          {runs.map((r) => (
            <tr key={r.id} className="hover:bg-slate-50 dark:hover:bg-slate-800/50">
              <td className={`${tdClass} whitespace-nowrap`}>
                <RunBadge run={r} />
                {r.restored === false && <div className="mt-1 text-xs text-red-700 dark:text-red-300">niet hersteld</div>}
              </td>
              <td className={tdClass}>
                <div className="text-xs text-slate-500">{kinds[r.kind]}</div>
                <Link href={reportHref(r.id)} className={`font-medium ${linkClass}`}>
                  {what(r)}
                </Link>
                {r.summary && (
                  <div className="mt-0.5 max-w-xl text-xs break-words text-slate-600 dark:text-slate-400">{r.summary}</div>
                )}
              </td>
              <td className={tdClass}>
                <Clusters run={r} />
              </td>
              <td className={`${tdClass} whitespace-nowrap text-slate-600 tabular-nums dark:text-slate-400`}>
                {fmt.format(new Date(r.created_at))}
                {r.trigger === "schedule" && <div className="text-xs text-slate-400">gepland</div>}
              </td>
            </tr>
          ))}
          {runs.length === 0 && (
            <tr>
              <td colSpan={4} className="px-3 py-6 text-center text-sm text-slate-500">
                {empty}
              </td>
            </tr>
          )}
        </tbody>
      </table>
    </div>
  );
}

// what is de naam van de test, of de node waarvan de back-up gecontroleerd werd.
function what(r: TestRun) {
  if (r.disaster) return r.disaster.name;
  if (r.definition) return r.definition.name;
  return r.hostname || "Back-up";
}

// Clusters toont bij een rampoefening elk cluster dat ze mat.
function Clusters({ run }: { run: TestRun }) {
  const others = (run.disaster?.clusters ?? []).filter((c) => c.id !== run.cluster_id);
  return (
    <div className="space-y-0.5">
      {run.cluster_id ? (
        <Link href={`/clusters/detail?id=${run.cluster_id}`} className={linkClass}>
          {run.cluster_name}
        </Link>
      ) : (
        <span className="text-slate-500">{run.cluster_name || "–"}</span>
      )}
      {others.map((c) => (
        <div key={c.id} className="text-xs">
          <Link href={`/clusters/detail?id=${c.id}`} className={linkClass}>
            {c.name}
          </Link>
        </div>
      ))}
    </div>
  );
}
