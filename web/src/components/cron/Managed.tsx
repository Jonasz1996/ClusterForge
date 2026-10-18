"use client";

import Link from "next/link";
import { useState } from "react";
import { until } from "@/components/cron/Cron";
import { ago, Empty, EnvBadge, QueryState, tableClass, tdClass, thClass } from "@/components/inventory/bits";
import { ProdConfirm, prodConfirmed, useProdTotp } from "@/components/ProdConfirm";
import { Alert, Badge, Button, Card } from "@/components/ui";
import {
  runOnLabel,
  runResultInfo,
  useCronRun,
  useManagedCronJobs,
  type CronRunSummary,
  type ManagedCronJob,
} from "@/lib/cronjobs";
import { useIsAdmin } from "@/lib/inventory";

const timeFmt = new Intl.DateTimeFormat("nl-BE", { weekday: "short", day: "numeric", month: "short", hour: "2-digit", minute: "2-digit" });

export function RunBadge({ r }: { r: CronRunSummary }) {
  const info = runResultInfo[r.result];
  const exit = r.result === "failed" && r.exit_code !== null ? ` (${r.exit_code})` : "";
  return (
    <Badge tone={info.tone}>
      {info.label}
      {exit}
    </Badge>
  );
}

export function runDuration(r: CronRunSummary) {
  if (!r.ended_at) return null;
  const s = Math.max(0, Math.round((new Date(r.ended_at).getTime() - new Date(r.started_at).getTime()) / 1000));
  if (s < 60) return `${s} s`;
  if (s < 3600) return `${Math.floor(s / 60)} min ${s % 60} s`;
  return `${Math.floor(s / 3600)} u ${Math.floor((s % 3600) / 60)} min`;
}

// where zegt waar een job draait.
export function where(j: ManagedCronJob) {
  if (j.run_on === "node") return `Op ${j.node_hostname || "één node"}`;
  return runOnLabel[j.run_on];
}

function NodeStates({ j }: { j: ManagedCronJob }) {
  return (
    <ul className="space-y-1">
      {j.nodes.map((n) => (
        <li key={n.node_id} className="flex flex-wrap items-center gap-x-2 gap-y-1">
          <span className="font-medium">{n.hostname}</span>
          {n.has_vip && j.run_on === "vip" && <Badge tone="blue">VIP</Badge>}
          {n.last_run ? (
            <>
              <RunBadge r={n.last_run} />
              <span className="text-xs text-slate-500" title={timeFmt.format(new Date(n.last_run.started_at))}>
                {ago(n.last_run.started_at)}
              </span>
            </>
          ) : (
            j.run_on !== "vip" || n.has_vip ? <span className="text-xs text-slate-500">nog niet gedraaid</span> : null
          )}
          {n.error !== "" ? (
            <span className="text-xs text-amber-700 dark:text-amber-300">{n.error}</span>
          ) : (
            !n.applied && <span className="text-xs text-amber-700 dark:text-amber-300">wordt op de node gezet</span>
          )}
        </li>
      ))}
    </ul>
  );
}

// ManagedCard is de lijst met beheerde jobs bovenaan Cronjobs.
export function ManagedCard({ cluster }: { cluster: string }) {
  const q = useManagedCronJobs();
  const isAdmin = useIsAdmin();
  const jobs = (q.data ?? []).filter((j) => cluster === "" || j.cluster_id === cluster);
  return (
    <Card
      title={
        <span className="flex flex-wrap items-center justify-between gap-2">
          <span>Beheerde cronjobs</span>
          {isAdmin && (
            <Link href="/cronjobs/bewerken" className="text-sm font-normal text-brand-700 hover:underline dark:text-sky-300">
              Cronjob toevoegen
            </Link>
          )}
        </span>
      }
    >
      <QueryState q={q}>
        {jobs.length === 0 ? (
          <Empty>
            Nog geen beheerde cronjobs. Een beheerde job staat als systemd-timer op de nodes van een cluster, draait op de node met het
            VIP, op één node of op alle nodes, en elke run staat hier met zijn uitvoer.
          </Empty>
        ) : (
          <div className="overflow-x-auto">
            <table className={tableClass}>
              <thead className="border-b border-slate-200 dark:border-slate-800">
                <tr>
                  <th className={thClass}>Naam</th>
                  <th className={thClass}>Wanneer</th>
                  <th className={thClass}>Waar</th>
                  <th className={thClass}>Nodes en laatste run</th>
                  <th className={thClass}>Volgende</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
                {jobs.map((j) => (
                  <tr key={j.id}>
                    <td className={tdClass}>
                      <Link href={`/cronjobs/job?id=${j.id}`} className="font-medium text-brand-700 hover:underline dark:text-sky-300">
                        {j.name}
                      </Link>
                      <div className="mt-1 flex flex-wrap items-center gap-1 text-xs text-slate-500">
                        {j.cluster_name}
                        <EnvBadge env={j.cluster_environment} />
                        {!j.enabled && <Badge>Uit</Badge>}
                        {j.running > 0 && <Badge tone="blue">loopt nu</Badge>}
                        {j.failures_24h > 0 && <Badge tone="red">{j.failures_24h} mislukt in 24 u</Badge>}
                        {j.missed_at && (
                          <span title={`De run van ${timeFmt.format(new Date(j.missed_at))} is niet gestart`}>
                            <Badge tone="amber">run gemist</Badge>
                          </span>
                        )}
                        {j.retries > 0 && <span>herhaalt {j.retries}×</span>}
                      </div>
                    </td>
                    <td className={tdClass}>
                      <div>{j.describe}</div>
                      <div className="font-mono text-xs text-slate-500">{j.schedule}</div>
                    </td>
                    <td className={tdClass}>{where(j)}</td>
                    <td className={tdClass}>
                      <NodeStates j={j} />
                    </td>
                    <td className={`${tdClass} whitespace-nowrap`}>
                      {j.next_run ? (
                        <span title={timeFmt.format(new Date(j.next_run))}>{until(j.next_run)}</span>
                      ) : (
                        <span className="text-slate-400">–</span>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </QueryState>
    </Card>
  );
}

// RunOutput toont de uitvoer van één run.
export function RunOutput({ id }: { id: string }) {
  const q = useCronRun(id);
  const r = q.data;
  return (
    <QueryState q={q}>
      {r && (
        <div className="space-y-2">
          {r.output_hidden ? (
            <Alert>Deze job draait als root; de uitvoer is alleen voor beheerders.</Alert>
          ) : r.output === "" ? (
            <p className="text-sm text-slate-500">{r.result === "running" ? "Nog geen uitvoer." : "Geen uitvoer."}</p>
          ) : (
            <pre className="max-h-96 overflow-auto rounded-md bg-slate-950 p-3 font-mono text-xs whitespace-pre-wrap text-slate-100">
              {r.live && <span className="text-sky-300">{"Loopt nog; de uitvoer ververst vanzelf.\n"}</span>}
              {r.output_truncated && <span className="text-slate-400">{"… (alleen de laatste 64 KiB)\n"}</span>}
              {r.output}
            </pre>
          )}
        </div>
      )}
    </QueryState>
  );
}

// RunNow is de knop Nu uitvoeren, met de bevestiging op prod.
export function RunNow({ j, run }: { j: ManagedCronJob; run: { mutate: (v: { id: string; confirm?: string }) => void; isPending: boolean } }) {
  const prod = j.cluster_environment === "prod";
  const totp = useProdTotp();
  const [confirm, setConfirm] = useState("");
  const [asking, setAsking] = useState(false);
  if (prod && asking) {
    return (
      <div className="w-full space-y-2">
        <ProdConfirm slug={j.cluster_slug} value={confirm} onChange={setConfirm} />
        <div className="flex gap-2">
          <Button disabled={!prodConfirmed(j.cluster_slug, confirm, totp) || run.isPending} onClick={() => run.mutate({ id: j.id, confirm })}>
            Nu uitvoeren
          </Button>
          <Button variant="ghost" onClick={() => setAsking(false)}>
            Annuleren
          </Button>
        </div>
      </div>
    );
  }
  return (
    <Button disabled={run.isPending} onClick={() => (prod ? setAsking(true) : run.mutate({ id: j.id }))}>
      {run.isPending ? "Bezig…" : "Nu uitvoeren"}
    </Button>
  );
}
