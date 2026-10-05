"use client";

import Link from "next/link";
import { ago, Empty, tableClass, tdClass, thClass } from "@/components/inventory/bits";
import { Badge } from "@/components/ui";
import { jobStatuses, type Job, type JobStatus } from "@/lib/proxmox";

export function JobStatusBadge({ status }: { status: JobStatus }) {
  const s = jobStatuses[status];
  return (
    <Badge tone={s.tone}>
      {status === "running" && <span className="mr-1 inline-block size-1.5 animate-pulse rounded-full bg-current" aria-hidden />}
      {s.label}
    </Badge>
  );
}

// duration geeft hoe lang een taak liep, of loopt.
export function jobDuration(j: Pick<Job, "started_at" | "finished_at">, now = Date.now()) {
  if (!j.started_at) return "–";
  const end = j.finished_at ? new Date(j.finished_at).getTime() : now;
  const s = Math.max(0, Math.round((end - new Date(j.started_at).getTime()) / 1000));
  if (s < 60) return `${s} s`;
  if (s < 3600) return `${Math.floor(s / 60)} min ${s % 60} s`;
  return `${Math.floor(s / 3600)} u ${Math.floor((s % 3600) / 60)} min`;
}

export function JobList({ jobs, empty = "Nog geen taken." }: { jobs: Job[]; empty?: string }) {
  if (jobs.length === 0) return <Empty>{empty}</Empty>;
  return (
    <div className="overflow-x-auto">
      <table className={tableClass}>
        <thead className="border-b border-slate-200 dark:border-slate-800">
          <tr>
            <th className={thClass}>Taak</th>
            <th className={thClass}>Status</th>
            <th className={thClass}>Door</th>
            <th className={thClass}>Wanneer</th>
            <th className={`${thClass} text-right`}>Duur</th>
          </tr>
        </thead>
        <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
          {jobs.map((j) => (
            <tr key={j.id}>
              <td className={tdClass}>
                <Link href={`/taken/detail?id=${j.id}`} className="font-medium hover:underline">
                  {j.title}
                </Link>
                {j.status === "failed" && j.error && <div className="mt-0.5 text-xs text-red-700 dark:text-red-300">{j.error}</div>}
              </td>
              <td className={tdClass}>
                <JobStatusBadge status={j.status} />
              </td>
              <td className={`${tdClass} text-slate-600 dark:text-slate-400`}>{j.requested_by ?? "systeem"}</td>
              <td className={`${tdClass} whitespace-nowrap text-slate-600 dark:text-slate-400`}>{ago(j.created_at)}</td>
              <td className={`${tdClass} text-right whitespace-nowrap tabular-nums text-slate-600 dark:text-slate-400`}>
                {jobDuration(j)}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
