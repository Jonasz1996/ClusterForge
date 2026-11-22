"use client";

import { useState } from "react";
import { IncidentTable } from "@/components/incidents/Incidents";
import { QueryState } from "@/components/inventory/bits";
import { PageHeader, Select } from "@/components/ui";
import { incidentPeriods, useIncidents } from "@/lib/incidents";

export default function IncidentsPage() {
  const [period, setPeriod] = useState("30d");
  const [status, setStatus] = useState<"" | "open" | "resolved">("");
  const [tests, setTests] = useState(false);
  const days = incidentPeriods.find((p) => p.value === period)?.days ?? 30;
  const q = useIncidents({
    days,
    status: status || undefined,
    includeTests: tests,
    limit: 200,
  });

  return (
    <div className="space-y-6">
      <PageHeader
        title="Incidenten"
        description="Storingen die ClusterForge zag, elk met een begin, een einde en wat er geraakt was. Overgangen die bij elkaar horen, staan in één incident. Open incidenten staan bovenaan."
      />
      <div className="flex flex-wrap items-center gap-3">
        <Select aria-label="Periode" className="w-36" value={period} onChange={(e) => setPeriod(e.target.value)}>
          {incidentPeriods.map((p) => (
            <option key={p.value} value={p.value}>
              {p.label}
            </option>
          ))}
        </Select>
        <Select
          aria-label="Status"
          className="w-44"
          value={status}
          onChange={(e) => setStatus(e.target.value as "" | "open" | "resolved")}
        >
          <option value="">Open en opgelost</option>
          <option value="open">Alleen open</option>
          <option value="resolved">Alleen opgelost</option>
        </Select>
        <label className="inline-flex cursor-pointer items-center gap-2 text-sm">
          <input
            type="checkbox"
            className="size-4 rounded border-slate-300"
            checked={tests}
            onChange={(e) => setTests(e.target.checked)}
          />
          Ook incidenten door een failovertest
        </label>
      </div>
      <QueryState q={q}>
        {q.data && (
          <>
            <IncidentTable
              items={q.data.items}
              empty={status === "open" ? "Geen open incidenten. Alles draait." : "Geen incidenten in deze periode."}
            />
            {q.data.has_more && <p className="text-sm text-slate-500">Er zijn meer incidenten; kies een kortere periode.</p>}
          </>
        )}
      </QueryState>
    </div>
  );
}
