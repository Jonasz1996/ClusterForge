"use client";

import { useState } from "react";
import { Alert, Card, cx } from "@/components/ui";
import { ApiError } from "@/lib/api/client";
import { metricsRanges, useMetrics, type MetricsRange } from "@/lib/inventory";
import { TimeChart } from "./TimeChart";

// MetricsPanels toont de grafieken van een node of cluster, met een keuze van
// de periode.
export function MetricsPanels({ kind, id, actions }: { kind: "node" | "cluster"; id: string; actions?: React.ReactNode }) {
  const [range, setRange] = useState<MetricsRange>("1h");
  const q = useMetrics(kind, id, range);
  const disabled = q.error instanceof ApiError && q.error.code === "metrics_disabled";

  return (
    <section className="space-y-3">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <h2 className="text-lg font-semibold">Grafieken</h2>
        <div className="flex items-center gap-3">
          {actions}
          {!disabled && (
            <div className="inline-flex rounded-md border border-slate-200 p-0.5 dark:border-slate-700" role="group" aria-label="Periode">
              {metricsRanges.map((r) => (
                <button
                  key={r.value}
                  type="button"
                  onClick={() => setRange(r.value)}
                  aria-pressed={range === r.value}
                  className={cx(
                    "rounded px-2.5 py-1 text-xs font-medium",
                    range === r.value
                      ? "bg-slate-900 text-white dark:bg-slate-100 dark:text-slate-900"
                      : "text-slate-600 hover:bg-slate-100 dark:text-slate-300 dark:hover:bg-slate-800",
                  )}
                >
                  {r.label}
                </button>
              ))}
            </div>
          )}
        </div>
      </div>
      {disabled && (
        <Alert kind="info">
          Grafieken staan uit omdat er geen VictoriaMetrics is ingesteld. Zet <code>CF_VICTORIAMETRICS_URL</code> in de
          configuratie van de server; de Docker Compose-opzet doet dat standaard.
        </Alert>
      )}
      {q.isError && !disabled && <Alert>{q.error instanceof Error ? q.error.message : "Grafieken laden mislukt"}</Alert>}
      {q.isLoading && <p className="text-sm text-slate-500">Laden…</p>}
      {q.data && (
        <div className="grid gap-4 lg:grid-cols-2">
          {q.data.panels
            .filter((p) => p.series.length > 0 || p.id !== "temperature")
            .map((p) => (
              <Card key={p.id} title={p.title}>
                {p.series.some((s) => s.values.some((v) => v !== null)) ? (
                  <TimeChart timestamps={q.data.timestamps} series={p.series} unit={p.unit} />
                ) : (
                  <p className="py-8 text-center text-sm text-slate-500">Nog geen gegevens in deze periode.</p>
                )}
              </Card>
            ))}
        </div>
      )}
    </section>
  );
}
