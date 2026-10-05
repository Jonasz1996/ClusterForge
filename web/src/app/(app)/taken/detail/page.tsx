"use client";

import Link from "next/link";
import { useSearchParams } from "next/navigation";
import { Suspense, useEffect, useRef } from "react";
import { jobDuration, JobStatusBadge } from "@/components/jobs/JobList";
import { QueryState } from "@/components/inventory/bits";
import { Alert, Button, Card, PageHeader } from "@/components/ui";
import { useIsAdmin, useNode } from "@/lib/inventory";
import { useRetryJob } from "@/lib/deploy";
import { jobActive, useCancelJob, useJob, type JobDetail } from "@/lib/proxmox";

export default function JobDetailPage() {
  return (
    <Suspense>
      <JobDetailInner />
    </Suspense>
  );
}

const fmt = new Intl.DateTimeFormat("nl-BE", { dateStyle: "medium", timeStyle: "medium" });
const dlClass = "grid grid-cols-[9rem_1fr] gap-x-4 gap-y-2.5 text-sm";
const dtClass = "text-slate-500";

function JobDetailInner() {
  const id = useSearchParams().get("id") ?? "";
  const job = useJob(id);
  const isAdmin = useIsAdmin();
  const cancel = useCancelJob();
  const retry = useRetryJob();
  if (id === "") return <Alert>Geen taak opgegeven.</Alert>;

  return (
    <QueryState q={job}>
      {job.data && (
        <div className="space-y-6">
          <div className="text-sm">
            <Link href="/taken" className="text-slate-500 hover:underline">
              ← Taken
            </Link>
          </div>
          <PageHeader
            title={job.data.title}
            description={
              <span className="inline-flex items-center gap-2">
                <JobStatusBadge status={job.data.status} />
                {job.data.cancel_requested && jobActive(job.data.status) && <span>wordt geannuleerd…</span>}
              </span>
            }
            actions={
              isAdmin &&
              (jobActive(job.data.status)
                ? !job.data.cancel_requested && (
                    <Button
                      variant="secondary"
                      disabled={cancel.isPending}
                      onClick={() => {
                        if (window.confirm("Taak annuleren? Een lopende Proxmox-taak wordt gestopt.")) cancel.mutate(job.data!.id);
                      }}
                    >
                      Annuleren
                    </Button>
                  )
                : job.data.retryable && (
                    <Button disabled={retry.isPending} onClick={() => retry.mutate(job.data!.id)}>
                      Opnieuw proberen
                    </Button>
                  ))
            }
          />
          {job.data.status === "failed" && job.data.error && <Alert>{job.data.error}</Alert>}
          {job.data.retryable && (
            <p className="text-sm text-slate-600 dark:text-slate-400">
              Opnieuw proberen gaat verder bij de stap die misliep; wat al gelukt is, wordt overgeslagen.
            </p>
          )}
          {retry.error && <Alert>{retry.error.message}</Alert>}
          <Card>
            <dl className={dlClass}>
              <dt className={dtClass}>Gevraagd door</dt>
              <dd>{job.data.requested_by ?? "systeem"}</dd>
              <dt className={dtClass}>Aangemaakt</dt>
              <dd>{fmt.format(new Date(job.data.created_at))}</dd>
              <dt className={dtClass}>Gestart</dt>
              <dd>{job.data.started_at ? fmt.format(new Date(job.data.started_at)) : "nog niet"}</dd>
              <dt className={dtClass}>Klaar</dt>
              <dd>{job.data.finished_at ? fmt.format(new Date(job.data.finished_at)) : "nog niet"}</dd>
              <dt className={dtClass}>Duur</dt>
              <dd className="tabular-nums">{jobDuration(job.data)}</dd>
              {job.data.node_id && <NodeLink id={job.data.node_id} />}
              {job.data.attempts > 1 && (
                <>
                  <dt className={dtClass}>Pogingen</dt>
                  <dd>{job.data.attempts} (hervat na een herstart van de server)</dd>
                </>
              )}
            </dl>
          </Card>
          <Steps job={job.data} />
        </div>
      )}
    </QueryState>
  );
}

function NodeLink({ id }: { id: string }) {
  const node = useNode(id);
  return (
    <>
      <dt className={dtClass}>Node</dt>
      <dd>
        <Link href={`/nodes/detail?id=${id}`} className="text-brand-600 hover:underline dark:text-brand-500">
          {node.data?.hostname ?? "…"}
        </Link>
      </dd>
    </>
  );
}

function Steps({ job }: { job: JobDetail }) {
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
