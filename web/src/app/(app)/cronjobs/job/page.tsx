"use client";

import Link from "next/link";
import { useRouter, useSearchParams } from "next/navigation";
import { Suspense, useState } from "react";
import { RunBadge, runDuration, RunNow, RunOutput, where } from "@/components/cron/Managed";
import { ago, EnvBadge, QueryState, tableClass, tdClass, thClass } from "@/components/inventory/bits";
import { ProdConfirm, prodConfirmed, useProdTotp } from "@/components/ProdConfirm";
import { Alert, Badge, Button, Card, PageHeader } from "@/components/ui";
import { useDeleteCronJob, useManagedCronJob, useRunCronJob, useSaveCronJob, jobInputOf } from "@/lib/cronjobs";
import { useIsAdmin } from "@/lib/inventory";

const timeFmt = new Intl.DateTimeFormat("nl-BE", { weekday: "short", day: "numeric", month: "short", hour: "2-digit", minute: "2-digit" });

export default function CronJobPage() {
  return (
    <Suspense>
      <CronJobInner />
    </Suspense>
  );
}

function CronJobInner() {
  const id = useSearchParams().get("id") ?? "";
  const router = useRouter();
  const q = useManagedCronJob(id);
  const isAdmin = useIsAdmin();
  const totp = useProdTotp();
  const run = useRunCronJob();
  const save = useSaveCronJob();
  const del = useDeleteCronJob();
  const [open, setOpen] = useState<string | null>(null);
  const [confirm, setConfirm] = useState("");
  const [deleting, setDeleting] = useState(false);
  const j = q.data?.job;
  const prod = j?.cluster_environment === "prod";
  const confirmed = !prod || (j ? prodConfirmed(j.cluster_slug, confirm, totp) : false);

  return (
    <QueryState q={q}>
      {j && (
        <div className="space-y-6">
          <PageHeader
            title={j.name}
            description={
              <span className="flex flex-wrap items-center gap-2">
                Beheerde cronjob in{" "}
                <Link href={`/clusters/detail?id=${j.cluster_id}`} className="text-brand-700 hover:underline dark:text-sky-300">
                  {j.cluster_name}
                </Link>
                <EnvBadge env={j.cluster_environment} />
                {!j.enabled && <Badge>Uit</Badge>}
              </span>
            }
            actions={
              isAdmin && (
                <div className="flex flex-wrap gap-2">
                  <RunNow j={j} run={run} />
                  <Link
                    href={`/cronjobs/bewerken?id=${j.id}`}
                    className="inline-flex items-center rounded-md border border-slate-300 px-3.5 py-2 text-sm font-medium dark:border-slate-700"
                  >
                    Bewerken
                  </Link>
                </div>
              )
            }
          />
          {run.isError && <Alert>{run.error.message}</Alert>}
          {run.isSuccess && <p className="text-sm text-slate-600 dark:text-slate-400">Gestart op {run.data.join(", ")}.</p>}

          <Card title="De job">
            <dl className="grid gap-x-6 gap-y-3 text-sm sm:grid-cols-[auto_1fr]">
              <dt className="text-slate-500">Wanneer</dt>
              <dd>
                {j.describe} <span className="font-mono text-xs text-slate-500">({j.schedule})</span>
                {j.next_run && (
                  <span className="text-slate-500">; volgende run {timeFmt.format(new Date(j.next_run))}</span>
                )}
              </dd>
              <dt className="text-slate-500">Waar</dt>
              <dd>{where(j)}</dd>
              <dt className="text-slate-500">Gebruiker</dt>
              <dd>{j.user}</dd>
              <dt className="text-slate-500">Tijdslimiet</dt>
              <dd>{j.timeout_seconds >= 3600 ? `${j.timeout_seconds / 3600} u` : `${Math.round(j.timeout_seconds / 60)} min`}</dd>
              <dt className="text-slate-500">Commando</dt>
              <dd>
                <pre className="overflow-x-auto rounded-md bg-slate-100 p-2 font-mono text-xs whitespace-pre-wrap dark:bg-slate-800">{j.command}</pre>
              </dd>
              <dt className="text-slate-500">Op de nodes</dt>
              <dd>
                <ul className="space-y-1">
                  {j.nodes.map((n) => (
                    <li key={n.node_id} className="flex flex-wrap items-center gap-2">
                      <span className="font-medium">{n.hostname}</span>
                      {n.has_vip && j.run_on === "vip" && <Badge tone="blue">VIP</Badge>}
                      {n.error ? (
                        <span className="text-amber-700 dark:text-amber-300">{n.error}</span>
                      ) : n.applied ? (
                        <span className="text-slate-500">timer cf-cron-{j.id.slice(0, 8)}… staat erop</span>
                      ) : (
                        <span className="text-amber-700 dark:text-amber-300">wordt op de node gezet</span>
                      )}
                    </li>
                  ))}
                </ul>
              </dd>
            </dl>
            {isAdmin && (
              <div className="mt-4 flex flex-wrap items-center gap-2 border-t border-slate-100 pt-4 dark:border-slate-800">
                {prod && <ProdConfirm slug={j.cluster_slug} value={confirm} onChange={setConfirm} />}
                <Button
                  variant="secondary"
                  disabled={!confirmed || save.isPending}
                  onClick={() =>
                    save.mutate({ id: j.id, clusterId: j.cluster_id, body: { ...jobInputOf(j), enabled: !j.enabled, confirm: prod ? confirm : undefined } })
                  }
                >
                  {j.enabled ? "Uitzetten" : "Aanzetten"}
                </Button>
                {deleting ? (
                  <>
                    <span className="text-sm">De timer gaat weg van elke node, met de historie.</span>
                    <Button
                      variant="danger"
                      disabled={!confirmed || del.isPending}
                      onClick={() => del.mutate({ id: j.id, confirm: prod ? confirm : undefined }, { onSuccess: () => router.push("/cronjobs") })}
                    >
                      Echt weghalen
                    </Button>
                    <Button variant="ghost" onClick={() => setDeleting(false)}>
                      Annuleren
                    </Button>
                  </>
                ) : (
                  <Button variant="secondary-danger" onClick={() => setDeleting(true)}>
                    Weghalen
                  </Button>
                )}
                {save.isError && <Alert>{save.error.message}</Alert>}
                {del.isError && <Alert>{del.error.message}</Alert>}
              </div>
            )}
          </Card>

          <Card title="Runs">
            {(q.data?.runs ?? []).length === 0 ? (
              <p className="text-sm text-slate-500">Nog geen runs.</p>
            ) : (
              <div className="overflow-x-auto">
                <table className={tableClass}>
                  <thead className="border-b border-slate-200 dark:border-slate-800">
                    <tr>
                      <th className={thClass}>Gestart</th>
                      <th className={thClass}>Node</th>
                      <th className={thClass}>Uitkomst</th>
                      <th className={thClass}>Duur</th>
                      <th className={thClass}>Hoe</th>
                      <th className={thClass} />
                    </tr>
                  </thead>
                  <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
                    {(q.data?.runs ?? []).map((r) => (
                      <RunRow key={r.id} open={open === r.id} onToggle={() => setOpen(open === r.id ? null : r.id)} r={r} />
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </Card>
        </div>
      )}
    </QueryState>
  );
}

function RunRow({ r, open, onToggle }: { r: Parameters<typeof RunBadge>[0]["r"]; open: boolean; onToggle: () => void }) {
  return (
    <>
      <tr>
        <td className={tdClass} title={timeFmt.format(new Date(r.started_at))}>
          {ago(r.started_at)}
        </td>
        <td className={tdClass}>{r.hostname}</td>
        <td className={tdClass}>
          <RunBadge r={r} />
        </td>
        <td className={tdClass}>{runDuration(r) ?? "–"}</td>
        <td className={tdClass}>{r.reason === "manual" ? "met de hand" : "volgens schema"}</td>
        <td className={tdClass}>
          <button type="button" className="text-sm text-brand-700 hover:underline dark:text-sky-300" onClick={onToggle}>
            {open ? "Verbergen" : "Uitvoer"}
          </button>
        </td>
      </tr>
      {open && (
        <tr>
          <td colSpan={6} className={tdClass}>
            <RunOutput id={r.id} />
          </td>
        </tr>
      )}
    </>
  );
}
