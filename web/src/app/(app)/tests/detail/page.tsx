"use client";

import Link from "next/link";
import { useSearchParams } from "next/navigation";
import { Suspense } from "react";
import { HistoryCard } from "@/components/audit/AuditList";
import { BackupReport } from "@/components/backups/Verify";
import { Checks, expectation, RunBadge } from "@/components/failover/Failover";
import { QueryState } from "@/components/inventory/bits";
import { Steps } from "@/components/jobs/Steps";
import { Alert, Button, Card, PageHeader, cx } from "@/components/ui";
import {
  probeText,
  seconds,
  stillDown,
  useRestoreTestRun,
  useTestRun,
  type FailoverDefinition,
  type FailoverMeasurements,
  type TestRun,
  type TestRunEvent,
} from "@/lib/failover";
import { useIsAdmin } from "@/lib/inventory";
import { jobActive, useCancelJob, useJob } from "@/lib/proxmox";

export default function TestRunPage() {
  return (
    <Suspense>
      <TestRunInner />
    </Suspense>
  );
}

const fmt = new Intl.DateTimeFormat("nl-BE", { dateStyle: "medium", timeStyle: "medium" });
const dlClass = "grid gap-x-4 gap-y-2.5 text-sm sm:grid-cols-[9rem_1fr]";
const dtClass = "text-slate-500";
const linkClass = "text-brand-600 hover:underline dark:text-brand-500";

function TestRunInner() {
  const id = useSearchParams().get("id") ?? "";
  const run = useTestRun(id);
  const isAdmin = useIsAdmin();
  const job = useJob(run.data?.job_id ?? "");
  const cancel = useCancelJob();
  const restore = useRestoreTestRun();
  if (id === "") return <Alert>Geen testrun opgegeven.</Alert>;
  const isBackup = run.data?.kind === "backup.verify";
  const skipped = run.data?.result === "skipped";
  const vm = run.data?.definition?.scenario === "vm_hard_stop";

  return (
    <QueryState q={run}>
      {run.data && (
        <div className="space-y-6">
          <div className="text-sm">
            {isBackup ? (
              <Link href="/back-ups" className="text-slate-500 hover:underline">
                ← Back-ups
              </Link>
            ) : run.data.cluster_id ? (
              <Link href={`/clusters/detail?id=${run.data.cluster_id}`} className="text-slate-500 hover:underline">
                ← {run.data.cluster_name}
              </Link>
            ) : (
              <span className="text-slate-500">{run.data.cluster_name}</span>
            )}
          </div>
          <PageHeader
            title={
              isBackup
                ? `Back-upcontrole: ${run.data.hostname || run.data.cluster_name}`
                : `Failovertest: ${run.data.definition?.name ?? ""}`
            }
            description={
              <span className="inline-flex flex-wrap items-center gap-2">
                <RunBadge run={run.data} />
                {job.data?.cancel_requested && jobActive(job.data.status) && (
                  <span>{isBackup ? "wordt afgebroken en opgeruimd…" : "wordt afgebroken en hersteld…"}</span>
                )}
                <span>{fmt.format(new Date(run.data.created_at))}</span>
                {run.data.trigger === "schedule" && <span className="text-slate-400">· gepland in het testvenster</span>}
                {run.data.requested_by && <span className="text-slate-400">· door {run.data.requested_by}</span>}
              </span>
            }
            actions={
              isAdmin && (
                <>
                  {job.data && jobActive(job.data.status) && !job.data.cancel_requested && (
                    <Button
                      variant="danger"
                      disabled={cancel.isPending}
                      onClick={() => {
                        const question = isBackup
                          ? "Controle afbreken? ClusterForge zet de sandbox-VM uit en verwijdert hem."
                          : vm
                            ? "Test afbreken? ClusterForge start de VM meteen weer."
                            : `Test afbreken? ClusterForge zet ${run.data?.definition?.unit || "de dienst"} meteen weer aan.`;
                        if (window.confirm(question)) cancel.mutate(job.data!.id);
                      }}
                    >
                      Afbreken
                    </Button>
                  )}
                  {!isBackup && run.data.restored === false && !restore.data && (
                    <Button variant="danger" disabled={restore.isPending} onClick={() => restore.mutate(run.data!.id)}>
                      Opnieuw herstellen
                    </Button>
                  )}
                </>
              )
            }
          />
          {!isBackup && run.data.restored === false && (
            <Alert>
              Niet volledig hersteld: {stillDown(run.data.definition, run.data.hostname)}.
            </Alert>
          )}
          {restore.error && <Alert>{restore.error.message}</Alert>}
          {restore.data && (
            <Alert kind="info">
              Herstel gestart.{" "}
              <Link href={`/taken/detail?id=${restore.data.id}`} className="underline">
                Taak volgen
              </Link>
            </Alert>
          )}
          {skipped ? (
            <>
              <Alert kind="info">
                {run.data.summary}
                <span className="block">Er is niets veranderd; er was geen taak.</span>
              </Alert>
              {run.data.checks.length > 0 && <Precheck checks={run.data.checks} />}
            </>
          ) : isBackup ? (
            <BackupReport run={run.data} isAdmin={isAdmin} />
          ) : (
            run.data.definition &&
            run.data.measurements && (
              <>
                <Summary run={run.data} d={run.data.definition} m={run.data.measurements} />
                <Timeline run={run.data} m={run.data.measurements} />
                {run.data.checks.length > 0 && <Precheck checks={run.data.checks} />}
              </>
            )
          )}
          {job.data && (
            <div className="space-y-4">
              <h2 className="flex flex-wrap items-baseline justify-between gap-2 text-base font-semibold">
                <span>Stappen</span>
                <Link href={`/taken/detail?id=${job.data.id}`} className={`text-sm font-normal ${linkClass}`}>
                  Taak openen
                </Link>
              </h2>
              <Steps job={job.data} />
            </div>
          )}
          {run.data.job_id && <HistoryCard filter={{ job: run.data.job_id }} isAdmin={isAdmin} />}
        </div>
      )}
    </QueryState>
  );
}

function Summary({ run, d, m }: { run: TestRun; d: FailoverDefinition; m: FailoverMeasurements }) {
  return (
    <Card>
      <dl className={dlClass}>
        <dt className={dtClass}>Test</dt>
        <dd>
          {d.description} <span className="font-mono text-xs">{d.vip}</span>
          {run.hostname && <span className="text-slate-500"> (eigenaar was {run.hostname})</span>}
          <div className="text-xs text-slate-500">probe {probeText(d.probe)}</div>
        </dd>
        <dt className={dtClass}>Verwacht</dt>
        <dd>{expectation(d)}</dd>
        <dt className={dtClass}>Resultaat</dt>
        <dd className={cx(run.result === "fail" || run.result === "error" ? "text-red-700 dark:text-red-300" : "")}>
          {run.summary || (run.job_status === "queued" ? "De test wacht op zijn beurt." : "De test loopt…")}
        </dd>
        {m.downtime_ms !== null && m.downtime_ms !== undefined && (
          <>
            <dt className={dtClass}>Onbereikbaar</dt>
            <dd className="tabular-nums">{seconds(m.downtime_ms)}</dd>
          </>
        )}
        {m.takeover_node && (
          <>
            <dt className={dtClass}>Overgenomen door</dt>
            <dd>
              {m.takeover_node_id ? (
                <Link href={`/nodes/detail?id=${m.takeover_node_id}`} className={linkClass}>
                  {m.takeover_node}
                </Link>
              ) : (
                m.takeover_node
              )}
              {m.takeover_ms !== null && <span className="text-slate-500"> (bevestigd na {seconds(m.takeover_ms)})</span>}
            </dd>
          </>
        )}
        {m.returned_to && (
          <>
            <dt className={dtClass}>Daarna op</dt>
            <dd>
              {m.returned_to}
              {m.failback_ms !== null && m.failback_ms > 0 && (
                <span className="text-slate-500"> (onderbreking bij de terugkeer {seconds(m.failback_ms)})</span>
              )}
            </dd>
          </>
        )}
        {run.finished_at && (
          <>
            <dt className={dtClass}>Klaar</dt>
            <dd>{fmt.format(new Date(run.finished_at))}</dd>
          </>
        )}
      </dl>
    </Card>
  );
}

// Precheck is dichtgeklapt als alles in orde was.
function Precheck({ checks }: { checks: TestRun["checks"] }) {
  const failed = checks.filter((c) => !c.ok).length;
  return (
    <Card>
      <details open={failed > 0}>
        <summary className="cursor-pointer text-base font-semibold">
          Voorcontrole{" "}
          <span className="text-sm font-normal text-slate-500">
            {failed === 0 ? `alle ${checks.length} controles in orde` : `${failed} van ${checks.length} controles mislukt`}
          </span>
        </summary>
        <div className="mt-4">
          <Checks checks={checks} />
        </div>
      </details>
    </Card>
  );
}

const markers: Record<TestRunEvent["kind"], { color: string; label: string } | null> = {
  fault: { color: "bg-red-600", label: "Storing" },
  takeover: { color: "bg-brand-600", label: "Overname" },
  clear: { color: "bg-slate-500", label: "Herstel" },
  ready: { color: "bg-slate-400", label: "Node klaar" },
  return: { color: "bg-violet-600", label: "Terugkeer" },
  down: null,
  up: null,
  step: null,
};

// Timeline toont de probe als horizontale balk, groen of rood, met
// markeringen voor de storing, de overname, het herstel en de terugkeer.
function Timeline({ run, m }: { run: TestRun; m: FailoverMeasurements }) {
  const events = run.timeline;
  if (m.probe.length === 0 && events.length === 0) return null;
  const end = Math.max(m.end_ms, ...m.probe.map((s) => s.to_ms), ...events.map((e) => e.t_ms), 1);
  const pct = (ms: number) => `${Math.min(100, Math.max(0, (ms / end) * 100))}%`;
  const expectAt = m.expect_ms > 0 && m.expect_ms < end ? m.expect_ms : null;
  return (
    <Card title="Tijdlijn">
      <div className="space-y-4">
        <div className="relative pt-5">
          <div className="relative h-6 overflow-hidden rounded bg-slate-100 dark:bg-slate-800" aria-label="Probe tijdens de test">
            {m.probe.map((s, i) => (
              <div
                key={i}
                className={cx("absolute inset-y-0", s.ok ? "bg-emerald-500" : "bg-red-500")}
                style={{ left: pct(s.from_ms), width: `calc(${pct(s.to_ms - s.from_ms)} + 1px)` }}
                title={`${s.ok ? "Bereikbaar" : "Onbereikbaar"} van ${seconds(s.from_ms)} tot ${seconds(s.to_ms)}`}
              />
            ))}
          </div>
          {expectAt !== null && (
            <div className="absolute top-3 bottom-0 border-l-2 border-dashed border-amber-500" style={{ left: pct(expectAt) }}>
              <span
                className={cx(
                  "absolute -top-3 text-[10px] whitespace-nowrap text-amber-700 dark:text-amber-300",
                  expectAt / end > 0.8 ? "right-1" : "left-1",
                )}
              >
                verwacht {seconds(expectAt)}
              </span>
            </div>
          )}
          {events.map((e, i) => {
            const mk = markers[e.kind];
            if (!mk) return null;
            return (
              <div
                key={i}
                className={cx("absolute top-4 bottom-0 w-0.5", mk.color)}
                style={{ left: pct(e.t_ms) }}
                title={`${seconds(e.t_ms)}: ${e.text}`}
              />
            );
          })}
        </div>
        <div className="flex justify-between text-xs text-slate-500 tabular-nums">
          <span>0 s</span>
          <span>{seconds(end)}</span>
        </div>
        <div className="flex flex-wrap gap-x-4 gap-y-1 text-xs text-slate-600 dark:text-slate-400">
          <Legend color="bg-emerald-500" label="VIP bereikbaar" />
          <Legend color="bg-red-500" label="VIP onbereikbaar" />
          {Object.values(markers)
            .filter((x) => x && events.some((e) => markers[e.kind] === x))
            .map((x) => (
              <Legend key={x!.label} color={x!.color} label={x!.label} thin />
            ))}
        </div>
        <ol className="space-y-1 text-sm">
          {events.map((e, i) => (
            <li key={i} className="grid grid-cols-[4.5rem_1fr] gap-2">
              <span className="text-right text-slate-500 tabular-nums">+{seconds(e.t_ms)}</span>
              <span>{e.text}</span>
            </li>
          ))}
        </ol>
      </div>
    </Card>
  );
}

function Legend({ color, label, thin = false }: { color: string; label: string; thin?: boolean }) {
  return (
    <span className="inline-flex items-center gap-1.5">
      <span aria-hidden className={cx("inline-block h-3", thin ? "w-0.5" : "w-3 rounded-sm", color)} />
      {label}
    </span>
  );
}
