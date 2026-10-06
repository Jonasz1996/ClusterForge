"use client";

import { useEffect, useRef } from "react";
import { jobDuration, JobStatusBadge } from "@/components/jobs/JobList";
import { Alert, Card } from "@/components/ui";
import type { JobDetail } from "@/lib/proxmox";

// Steps toont de stappen van een taak met hun uitvoer.
export function Steps({ job }: { job: JobDetail }) {
  if (job.steps.length === 0) {
    return <p className="text-sm text-slate-500">{job.status === "queued" ? "De taak wacht op zijn beurt." : "Geen stappen."}</p>;
  }
  return (
    <div className="space-y-4">
      {job.steps.map((s) => (
        <Card key={s.seq}>
          <div className="mb-3 flex flex-wrap items-center justify-between gap-2">
            <h2 className="font-semibold">
              {s.seq + 1}. {s.name}
            </h2>
            <span className="inline-flex items-center gap-2 text-xs text-slate-500">
              <JobStatusBadge status={s.status} />
              {jobDuration({ started_at: s.started_at, finished_at: s.finished_at })}
            </span>
          </div>
          {s.error && (
            <div className="mb-3">
              <Alert>{s.error}</Alert>
            </div>
          )}
          <Log lines={s.log} follow={s.status === "running"} />
        </Card>
      ))}
    </div>
  );
}

// Log toont de uitvoer en scrolt mee zolang de stap loopt.
function Log({ lines, follow }: { lines: string[]; follow: boolean }) {
  const ref = useRef<HTMLPreElement>(null);
  useEffect(() => {
    if (follow && ref.current) ref.current.scrollTop = ref.current.scrollHeight;
  }, [lines, follow]);
  if (lines.length === 0) return <p className="text-sm text-slate-500">Nog geen uitvoer.</p>;
  return (
    <pre ref={ref} className="max-h-96 overflow-auto rounded bg-slate-900 p-3 font-mono text-xs leading-relaxed text-slate-100">
      {lines.join("\n")}
    </pre>
  );
}
