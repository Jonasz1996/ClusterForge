"use client";

import { useState } from "react";
import { QueryState } from "@/components/inventory/bits";
import { Alert, Badge, Button, Card, Select } from "@/components/ui";
import { syncResultInfo, useCronSyncPlan, useDryRunCronSync, type CronSyncResult, type ManagedCronJob } from "@/lib/cronjobs";

function bytes(b: number) {
  const units = ["B", "KB", "MB", "GB", "TB"];
  let v = b;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${i === 0 ? v : v.toFixed(1)} ${units[i]}`;
}

// SyncResults zegt per doel wat een run kopieerde, of waarom niet.
export function SyncResults({ results }: { results: CronSyncResult[] }) {
  return (
    <ul className="mt-1 space-y-0.5 text-xs text-slate-600 dark:text-slate-400">
      {results.map((s) => (
        <li key={s.node_id}>
          <span className="font-medium">{s.hostname}</span>:{" "}
          {s.result === "ok" ? (
            <>
              {s.files} {s.files === 1 ? "bestand" : "bestanden"}, {bytes(s.bytes)}
              {s.deleted > 0 && `, ${s.deleted} weg`}
            </>
          ) : (
            <span className={s.result === "refused" ? "text-amber-700 dark:text-amber-300" : "text-red-700 dark:text-red-300"}>
              {syncResultInfo[s.result].label.toLowerCase()}
              {s.error && `: ${s.error}`}
            </span>
          )}
        </li>
      ))}
    </ul>
  );
}

// SyncPlan toont per node of hij klaar is voor de sync, en laat een
// proefrun naar één doel draaien.
export function SyncPlan({ j }: { j: ManagedCronJob }) {
  const plan = useCronSyncPlan(j.id, true);
  const dry = useDryRunCronSync();
  const targets = (plan.data?.nodes ?? []).filter((n) => n.role !== "bron");
  const [target, setTarget] = useState("");
  const chosen = target || targets[0]?.node_id || "";
  return (
    <Card title="Klaar voor de sync?">
      <QueryState q={plan}>
        {plan.data && (
          <div className="space-y-4">
            {plan.data.notes.map((n) => (
              <Alert key={n} kind="info">
                {n}
              </Alert>
            ))}
            <ul className="space-y-3">
              {plan.data.nodes.map((n) => (
                <li key={n.node_id} className="text-sm">
                  <div className="flex flex-wrap items-center gap-2">
                    <span className="font-medium">{n.hostname}</span>
                    {n.address && <span className="font-mono text-xs text-slate-500">{n.address}</span>}
                    <Badge tone="blue">{n.role}</Badge>
                    <Badge tone={n.ok ? "green" : "red"}>{n.ok ? "Klaar" : "Niet klaar"}</Badge>
                  </div>
                  <ul className="mt-1 space-y-0.5 text-xs">
                    {n.checks.map((c) => (
                      <li key={c.name} className={c.ok ? "text-slate-500" : "text-red-700 dark:text-red-300"}>
                        {c.ok ? "✓" : "✗"} {c.text}
                      </li>
                    ))}
                  </ul>
                </li>
              ))}
            </ul>
            {targets.length > 0 && (
              <div className="space-y-2 border-t border-slate-100 pt-4 dark:border-slate-800">
                <p className="text-sm text-slate-600 dark:text-slate-400">
                  Een proefrun draait rsync --dry-run vanaf de bron: hij zegt wat er zou veranderen, zonder iets te kopiëren of weg te halen.
                </p>
                <div className="flex flex-wrap items-center gap-2">
                  {targets.length > 1 && (
                    <Select className="w-auto" value={chosen} onChange={(e) => setTarget(e.target.value)} aria-label="Doel van de proefrun">
                      {targets.map((n) => (
                        <option key={n.node_id} value={n.node_id}>
                          {n.hostname}
                        </option>
                      ))}
                    </Select>
                  )}
                  <Button variant="secondary" disabled={dry.isPending || chosen === ""} onClick={() => dry.mutate({ id: j.id, target: chosen })}>
                    {dry.isPending ? "Bezig…" : `Proefrun naar ${targets.find((n) => n.node_id === chosen)?.hostname ?? ""}`}
                  </Button>
                </div>
                {dry.isError && <Alert>{dry.error.message}</Alert>}
                {dry.isSuccess && (
                  <div className="space-y-1">
                    <p className="text-sm">
                      Vanaf {dry.data.source}:{" "}
                      {dry.data.lines.length === 0 ? "niets te doen, alles is al gelijk." : `${dry.data.lines.length} wijzigingen.`}
                    </p>
                    {dry.data.lines.length > 0 && (
                      <pre className="max-h-96 overflow-auto rounded-md bg-slate-950 p-3 font-mono text-xs whitespace-pre-wrap text-slate-100">
                        {dry.data.lines.join("\n")}
                      </pre>
                    )}
                  </div>
                )}
              </div>
            )}
          </div>
        )}
      </QueryState>
    </Card>
  );
}
