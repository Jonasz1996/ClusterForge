"use client";

import Link from "next/link";
import { useState } from "react";
import { ago, Empty, EnvBadge, QueryState, tableClass, tdClass, thClass } from "@/components/inventory/bits";
import { Badge, Card } from "@/components/ui";
import {
  missingOn,
  resultInfo,
  sourceLabel,
  useCronjobs,
  worstResult,
  type CronGroup,
  type CronJob,
  type CronJobNode,
} from "@/lib/cronjobs";

const timeFmt = new Intl.DateTimeFormat("nl-BE", { weekday: "short", day: "numeric", month: "short", hour: "2-digit", minute: "2-digit" });

// until zegt hoe lang het nog duurt tot iso, zoals "over 3 u".
export function until(iso: string | null | undefined, now = Date.now()) {
  if (!iso) return "";
  const s = Math.round((new Date(iso).getTime() - now) / 1000);
  if (s < 60) return "zo meteen";
  if (s < 3600) return `over ${Math.floor(s / 60)} min`;
  if (s < 86400) return `over ${Math.floor(s / 3600)} u`;
  return `over ${Math.floor(s / 86400)} d`;
}

function duration(n: CronJobNode) {
  if (!n.last_run || !n.last_end) return null;
  const s = Math.max(0, Math.round((new Date(n.last_end).getTime() - new Date(n.last_run).getTime()) / 1000));
  if (s < 60) return `${s} s`;
  if (s < 3600) return `${Math.floor(s / 60)} min ${s % 60} s`;
  return `${Math.floor(s / 3600)} u ${Math.floor((s % 3600) / 60)} min`;
}

export function ResultBadge({ n }: { n: CronJobNode }) {
  const r = resultInfo[n.last_result];
  const exit = n.last_exit !== null && n.last_result === "failed" ? ` (${n.last_exit})` : "";
  return (
    <Badge tone={r.tone}>
      {r.label}
      {exit}
    </Badge>
  );
}

function NodeRuns({ job, single }: { job: CronJob; single: boolean }) {
  return (
    <ul className="space-y-1">
      {job.nodes.map((n) => (
        <li key={n.node_id} className="flex flex-wrap items-center gap-x-2 gap-y-1">
          {!single && <span className="font-medium">{n.hostname}</span>}
          {!n.enabled ? (
            <Badge>Uit</Badge>
          ) : (
            <>
              <ResultBadge n={n} />
              {n.last_run && (
                <span className="text-xs text-slate-500" title={timeFmt.format(new Date(n.last_run))}>
                  {ago(n.last_run)}
                  {duration(n) && <>, duurde {duration(n)}</>}
                </span>
              )}
              {n.failures > 0 && (
                <span className="text-xs text-red-700 dark:text-red-300">
                  {n.failures} van {n.runs} mislukt in 24 u
                </span>
              )}
            </>
          )}
        </li>
      ))}
    </ul>
  );
}

// JobRows toont de jobs als tabel. Met group erbij zegt de kolom Nodes ook
// waar de job ontbreekt.
export function JobTable({ jobs, group, single }: { jobs: CronJob[]; group?: CronGroup; single?: boolean }) {
  return (
    <div className="overflow-x-auto">
      <table className={tableClass}>
        <thead className="border-b border-slate-200 dark:border-slate-800">
          <tr>
            <th className={thClass}>Wanneer</th>
            <th className={thClass}>Commando</th>
            <th className={thClass}>Gebruiker</th>
            <th className={thClass}>{single ? "Laatste run" : "Nodes en laatste run"}</th>
            <th className={thClass}>Volgende</th>
          </tr>
        </thead>
        <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
          {jobs.map((j) => {
            const missing = group ? missingOn(j, group) : [];
            return (
              <tr key={j.key}>
                <td className={tdClass}>
                  <div>{j.describe || <span className="font-mono text-xs">{j.schedule}</span>}</div>
                  {j.describe && <div className="font-mono text-xs text-slate-500">{j.schedule}</div>}
                </td>
                <td className={`${tdClass} max-w-xl`}>
                  <div className="font-mono text-xs break-all">{j.command}</div>
                  <div className="mt-1 flex flex-wrap gap-1 text-xs text-slate-500">
                    <span title={j.line ? `${j.file}, regel ${j.line}` : j.file}>
                      {j.source === "timer" ? j.file : `${sourceLabel[j.source]} · ${j.file}`}
                    </span>
                    {j.description && <span>· {j.description}</span>}
                    {j.package && <Badge>pakket {j.package}</Badge>}
                    {j.clusterforge && <Badge tone="blue">ClusterForge</Badge>}
                  </div>
                </td>
                <td className={tdClass}>{j.user}</td>
                <td className={tdClass}>
                  <NodeRuns job={j} single={!!single} />
                  {missing.length > 0 && (
                    <div className="mt-1 text-xs text-amber-700 dark:text-amber-300">Niet op {missing.join(", ")}</div>
                  )}
                </td>
                <td className={`${tdClass} whitespace-nowrap`}>
                  {j.next_run ? (
                    <span title={timeFmt.format(new Date(j.next_run))}>{until(j.next_run)}</span>
                  ) : (
                    <span className="text-slate-400">–</span>
                  )}
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}

export function GroupCard({ g, showSystem }: { g: CronGroup; showSystem: boolean }) {
  const jobs = g.jobs.filter((j) => showSystem || j.package === "");
  const hidden = g.jobs.length - jobs.length;
  const failing = jobs.filter((j) => worstResult(j) === "failed").length;
  const errors = g.nodes.filter((n) => n.error !== "");
  const title = (
    <span className="flex flex-wrap items-center gap-2">
      {g.cluster_id ? (
        <Link href={`/clusters/detail?id=${g.cluster_id}`} className="text-brand-700 hover:underline dark:text-sky-300">
          {g.cluster_name}
        </Link>
      ) : (
        <span>Nodes zonder cluster</span>
      )}
      {g.cluster_environment && <EnvBadge env={g.cluster_environment} />}
      <span className="text-sm font-normal text-slate-500">
        {jobs.length} {jobs.length === 1 ? "job" : "jobs"} op {g.nodes.map((n) => n.hostname).join(", ")}
      </span>
      {failing > 0 && <Badge tone="red">{failing} mislukt</Badge>}
    </span>
  );
  return (
    <Card title={title}>
      {errors.map((n) => (
        <p key={n.node_id} className="mb-2 text-sm text-amber-700 dark:text-amber-300">
          {n.hostname}: {n.error}
          {n.ok_at && <span className="text-slate-500"> (laatst gelezen {ago(n.ok_at)})</span>}
        </p>
      ))}
      {g.nodes.some((n) => n.error === "" && n.daemon === "cron" && !n.logging) && (
        <p className="mb-3 text-sm text-slate-600 dark:text-slate-400">
          cron logt op {g.nodes.filter((n) => n.daemon === "cron" && !n.logging).map((n) => n.hostname).join(", ")} alleen de start
          van een job, niet of hij lukte. Daarom staat er &lsquo;Gestart&rsquo; in plaats van &lsquo;Gelukt&rsquo; of &lsquo;Mislukt&rsquo;.
        </p>
      )}
      {jobs.length === 0 ? (
        <p className="text-sm text-slate-500">Geen eigen cronjobs op deze nodes.</p>
      ) : (
        <JobTable jobs={jobs} group={g} />
      )}
      {hidden > 0 && (
        <p className="mt-2 text-xs text-slate-500">
          {hidden} {hidden === 1 ? "job" : "jobs"} van Debian-pakketten verborgen.
        </p>
      )}
    </Card>
  );
}

// NodeCronCard toont de cronjobs van één node, op de pagina van de node.
export function NodeCronCard({ nodeId }: { nodeId: string }) {
  const q = useCronjobs();
  const [showSystem, setShowSystem] = useState(false);
  const group = q.data?.find((g) => g.nodes.some((n) => n.node_id === nodeId));
  const read = group?.nodes.find((n) => n.node_id === nodeId);
  const all = (group?.jobs ?? [])
    .filter((j) => j.nodes.some((n) => n.node_id === nodeId))
    .map((j) => ({ ...j, nodes: j.nodes.filter((n) => n.node_id === nodeId) }));
  const jobs = all.filter((j) => showSystem || j.package === "");
  return (
    <Card
      title={
        <span className="flex flex-wrap items-center gap-2">
          Cronjobs
          <Link href="/cronjobs" className="text-sm font-normal text-brand-700 hover:underline dark:text-sky-300">
            alle cronjobs
          </Link>
        </span>
      }
    >
      <QueryState q={q}>
        {!read ? (
          <p className="text-sm text-slate-500">ClusterForge heeft de cronjobs van deze node nog niet gelezen.</p>
        ) : (
          <>
            {read.error && <p className="mb-2 text-sm text-amber-700 dark:text-amber-300">{read.error}</p>}
            {jobs.length === 0 ? <Empty>Geen eigen cronjobs of timers op deze node.</Empty> : <JobTable jobs={jobs} single />}
            {all.length > jobs.length && (
              <button type="button" className="mt-2 text-xs text-brand-700 hover:underline dark:text-sky-300" onClick={() => setShowSystem(true)}>
                Ook de {all.length - jobs.length} jobs van Debian-pakketten tonen
              </button>
            )}
          </>
        )}
      </QueryState>
    </Card>
  );
}
