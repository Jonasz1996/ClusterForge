"use client";

import Link from "next/link";
import { useSearchParams } from "next/navigation";
import { Suspense } from "react";
import { HistoryCard } from "@/components/audit/AuditList";
import { jobDuration, JobStatusBadge } from "@/components/jobs/JobList";
import { Steps } from "@/components/jobs/Steps";
import { QueryState } from "@/components/inventory/bits";
import { Alert, Button, Card, PageHeader } from "@/components/ui";
import { useIsAdmin, useNode } from "@/lib/inventory";
import { useRetryJob } from "@/lib/deploy";
import { jobActive, useCancelJob, useJob } from "@/lib/proxmox";

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
          <HistoryCard filter={{ job: job.data.id }} isAdmin={isAdmin} />
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
