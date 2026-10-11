"use client";

import Link from "next/link";
import { usePathname, useRouter, useSearchParams } from "next/navigation";
import { Suspense } from "react";
import { EnvBadge, QueryState, tableClass, tdClass, thClass } from "@/components/inventory/bits";
import { GradeBadge } from "@/components/risk/Risk";
import { Badge, Button, Card, PageHeader, Select } from "@/components/ui";
import { freshnessInfo } from "@/lib/backups";
import { formatWhen } from "@/lib/docs";
import { runResults } from "@/lib/failover";
import {
  goalText,
  measuredText,
  monthLabel,
  recentMonths,
  runKinds,
  tally,
  useRecoveryReport,
  type RecoveryReport,
  type RecoveryReportCluster,
} from "@/lib/reports";
import { severityInfo } from "@/lib/risk";
import { duration } from "@/lib/verify";

const linkClass = "text-brand-600 hover:underline dark:text-sky-300 print:text-inherit print:no-underline";

export default function ReportsPage() {
  return (
    <Suspense>
      <ReportsInner />
    </Suspense>
  );
}

// De maand staat in de URL, zodat een link naar een rapport blijft werken.
function ReportsInner() {
  const params = useSearchParams();
  const router = useRouter();
  const pathname = usePathname();
  const month = params.get("maand") ?? "";
  const q = useRecoveryReport(month);
  const r = q.data;
  const shown = r?.month ?? month;
  const saved = r?.saved_months ?? [];

  return (
    <div className="space-y-6">
      <div className="print:hidden">
        <PageHeader
          title="Rapporten"
          description="Herstelbaarheid per maand: back-ups, controles, failovertests, rampoefeningen en DR-repetities, met RTO en RPO tegen de doelen. Afdrukken als PDF gaat via de browser."
          actions={
            <>
              <Select
                aria-label="Maand"
                value={shown}
                onChange={(e) => router.replace(`${pathname}?maand=${e.target.value}`)}
                className="w-60"
              >
                {recentMonths(saved).map((m) => (
                  <option key={m} value={m}>
                    {monthLabel(m)}
                    {saved.includes(m) ? " (bewaard)" : ""}
                  </option>
                ))}
              </Select>
              <Button variant="secondary" onClick={() => window.print()} disabled={!r}>
                Afdrukken
              </Button>
            </>
          }
        />
      </div>
      <QueryState q={q}>{r && <Recovery r={r} />}</QueryState>
    </div>
  );
}

function statusLine(r: RecoveryReport) {
  if (r.saved) return `Bewaard op ${formatWhen(r.generated_at)}: zo stond het aan het einde van de maand.`;
  if (r.running) return `De maand loopt nog. Berekend op ${formatWhen(r.generated_at)}, met de tests tot nu.`;
  return `Voor deze maand is geen rapport bewaard, dus ClusterForge berekende het nu. De tests komen uit de geschiedenis; de back-ups, de doelen en de risico's zijn die van ${formatWhen(r.generated_at)}.`;
}

function Recovery({ r }: { r: RecoveryReport }) {
  return (
    <article className="space-y-6">
      <header>
        <h2 className="text-xl font-semibold tracking-tight">Herstelbaarheid, {monthLabel(r.month)}</h2>
        <p className="mt-1 text-sm text-slate-600 dark:text-slate-400">{statusLine(r)}</p>
      </header>
      {r.clusters.length === 0 ? (
        <p className="text-sm text-slate-500">In deze maand was er nog geen cluster.</p>
      ) : (
        <>
          <Summary r={r} />
          {r.clusters.map((c) => (
            <ClusterReport key={c.id} c={c} month={r.month} />
          ))}
        </>
      )}
    </article>
  );
}

function Count({ total, passed }: { total: number; passed: number }) {
  if (total === 0) return <span className="text-slate-400">geen</span>;
  return (
    <Badge tone={passed === total ? "green" : "amber"}>
      {passed} van {total}
    </Badge>
  );
}

function Met({ met }: { met: boolean | null }) {
  if (met === null) return null;
  return <Badge tone={met ? "green" : "red"}>{met ? "gehaald" : "niet gehaald"}</Badge>;
}

function Summary({ r }: { r: RecoveryReport }) {
  return (
    <Card title="Samenvatting">
      <div className="overflow-x-auto">
        <table className={tableClass}>
          <thead className="border-b border-slate-200 dark:border-slate-800">
            <tr>
              <th className={thClass}>Cluster</th>
              <th className={thClass}>Back-ups vers</th>
              <th className={thClass}>Controles</th>
              <th className={thClass}>Failovertests</th>
              <th className={thClass}>Rampoefeningen</th>
              <th className={thClass}>DR-repetities</th>
              <th className={thClass}>RTO en RPO</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
            {r.clusters.map((c) => {
              const fresh = c.backups.filter((b) => b.freshness === "ok").length;
              return (
                <tr key={c.id}>
                  <td className={tdClass}>
                    <div className="font-medium">{c.name}</div>
                    <div className="mt-1 flex flex-wrap gap-1">
                      <EnvBadge env={c.environment} />
                      <GradeBadge score={c.score} grade={c.grade} />
                    </div>
                  </td>
                  <td className={tdClass}>
                    {c.backups.length === 0 ? (
                      <span className="text-slate-400">geen</span>
                    ) : (
                      <Badge tone={fresh === c.backups.length ? "green" : "amber"}>
                        {fresh} van {c.backups.length}
                      </Badge>
                    )}
                  </td>
                  <td className={tdClass}>
                    <Count {...tally(c.runs, "backup.verify")} />
                  </td>
                  <td className={tdClass}>
                    <Count {...tally(c.runs, "failover.test")} />
                  </td>
                  <td className={tdClass}>
                    <Count {...tally(c.runs, "disaster.run")} />
                  </td>
                  <td className={tdClass}>
                    <Count {...tally(c.runs, "dr.rehearsal")} />
                  </td>
                  <td className={`${tdClass} whitespace-nowrap`}>
                    {c.dr ? (
                      <div className="space-y-1">
                        <Measure label="RTO" value={c.dr.rto_seconds} goal={c.rto_target_minutes} met={c.dr.rto_met} />
                        <Measure label="RPO" value={c.dr.rpo_seconds} goal={c.rpo_target_hours * 60} met={c.dr.rpo_met} />
                      </div>
                    ) : (
                      <span className="text-slate-400">niet gemeten</span>
                    )}
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
      <p className="mt-3 text-xs text-slate-500">
        Bij de tests staat hoeveel er in {monthLabel(r.month)} slaagden, van het aantal. RTO en RPO komen van de laatste
        DR-repetitie tot het einde van de maand die het cluster werkend terugkreeg.
      </p>
    </Card>
  );
}

function Measure({ label, value, goal, met }: { label: string; value: number | null; goal: number; met: boolean | null }) {
  return (
    <div className="flex items-center gap-2">
      <span className="w-8 text-xs font-medium text-slate-500">{label}</span>
      {value === null ? <span className="text-slate-400">niet gemeten</span> : duration(value)}
      <span className="text-xs text-slate-500">doel {goalText(goal)}</span>
      <Met met={met} />
    </div>
  );
}

function RecoveryLine({ c }: { c: RecoveryReportCluster }) {
  const goals =
    c.rto_target_minutes || c.rpo_target_hours
      ? `De doelen zijn een RTO van ${goalText(c.rto_target_minutes)} en een RPO van ${goalText(c.rpo_target_hours * 60)}.`
      : "Dit cluster heeft geen doel voor RTO en RPO.";
  const dr = c.dr;
  if (!dr) {
    return (
      <p className="text-sm text-slate-700 dark:text-slate-300">
        Nog geen DR-repetitie heeft dit cluster werkend teruggezet, dus RTO en RPO zijn niet gemeten. {goals}
      </p>
    );
  }
  return (
    <div className="space-y-2 text-sm text-slate-700 dark:text-slate-300">
      <div className="flex flex-wrap items-center gap-x-4 gap-y-2">
        <span>
          <span className="font-medium">RTO</span> {dr.rto_seconds !== null ? duration(dr.rto_seconds) : "niet gemeten"} (doel{" "}
          {goalText(c.rto_target_minutes)}) <Met met={dr.rto_met} />
        </span>
        <span>
          <span className="font-medium">RPO</span> {dr.rpo_seconds !== null ? duration(dr.rpo_seconds) : "niet gemeten"} (doel{" "}
          {goalText(c.rpo_target_hours * 60)}) <Met met={dr.rpo_met} />
        </span>
      </div>
      <p className="text-slate-600 dark:text-slate-400">
        Gemeten in de sandbox op {formatWhen(dr.finished_at)} met de DR-repetitie{" "}
        <Link href={`/tests/detail?id=${dr.run_id}`} className={linkClass}>
          {dr.name || "zonder naam"}
        </Link>
        .
      </p>
    </div>
  );
}

function ClusterReport({ c, month }: { c: RecoveryReportCluster; month: string }) {
  return (
    <section className="space-y-4 print:break-before-page">
      <h3 className="flex flex-wrap items-center gap-2 text-lg font-semibold">
        <Link href={`/clusters/detail?id=${c.id}`} className={linkClass}>
          {c.name}
        </Link>
        <EnvBadge env={c.environment} />
        <GradeBadge score={c.score} grade={c.grade} />
      </h3>
      <RecoveryLine c={c} />

      <Card title="Back-ups">
        {c.backups.length === 0 ? (
          <p className="text-sm text-slate-500">
            Geen VM van dit cluster is aan Proxmox gekoppeld, dus ClusterForge kent geen back-up.
          </p>
        ) : (
          <div className="overflow-x-auto">
            <table className={tableClass}>
              <thead className="border-b border-slate-200 dark:border-slate-800">
                <tr>
                  <th className={thClass}>Node</th>
                  <th className={thClass}>Nieuwste back-up</th>
                  <th className={thClass}>Stand</th>
                  <th className={thClass}>Laatste controle</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
                {c.backups.map((b) => {
                  const f = freshnessInfo[b.freshness];
                  const v = b.last_verification;
                  return (
                    <tr key={`${b.connection}-${b.vmid}`}>
                      <td className={tdClass}>
                        <div className="font-medium">{b.node || `VM ${b.vmid}`}</div>
                        <div className="text-xs text-slate-500">
                          VM {b.vmid} · {b.connection}
                        </div>
                      </td>
                      <td className={tdClass}>
                        {b.time ? (
                          <>
                            <div>{formatWhen(b.time)}</div>
                            <div className="text-xs text-slate-500">
                              {b.storage} · {b.count} {b.count === 1 ? "back-up" : "back-ups"}
                            </div>
                          </>
                        ) : (
                          <span className="text-slate-400">geen</span>
                        )}
                      </td>
                      <td className={tdClass}>
                        <Badge tone={f.tone}>{f.label}</Badge>
                      </td>
                      <td className={tdClass}>
                        {v ? (
                          <div className="flex flex-wrap items-center gap-2">
                            <Badge tone={runResults[v.result as keyof typeof runResults]?.tone ?? "slate"}>
                              {runResults[v.result as keyof typeof runResults]?.label ?? v.result}
                            </Badge>
                            <span className="text-xs text-slate-500">
                              {v.finished_at ? formatWhen(v.finished_at) : ""}
                              {v.recovery_seconds !== null ? ` · hersteltijd ${duration(v.recovery_seconds)}` : ""}
                            </span>
                          </div>
                        ) : (
                          <span className="text-slate-400">nooit</span>
                        )}
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        )}
      </Card>

      <Card title={`Tests in ${monthLabel(month)}`}>
        {c.runs.length === 0 ? (
          <p className="text-sm text-slate-500">In deze maand liep er geen test op dit cluster.</p>
        ) : (
          <div className="overflow-x-auto">
            <table className={tableClass}>
              <thead className="border-b border-slate-200 dark:border-slate-800">
                <tr>
                  <th className={thClass}>Wanneer</th>
                  <th className={thClass}>Test</th>
                  <th className={thClass}>Uitslag</th>
                  <th className={thClass}>Gemeten</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
                {c.runs.map((run) => {
                  const res = runResults[run.result];
                  return (
                    <tr key={run.id}>
                      <td className={`${tdClass} whitespace-nowrap`}>{formatWhen(run.finished_at)}</td>
                      <td className={tdClass}>
                        <div className="text-xs text-slate-500">
                          {runKinds[run.kind]}
                          {run.trigger === "schedule" ? ", gepland" : ""}
                        </div>
                        <Link href={`/tests/detail?id=${run.id}`} className={linkClass}>
                          {run.name || runKinds[run.kind]}
                        </Link>
                      </td>
                      <td className={tdClass}>
                        <Badge tone={res.tone}>{res.label}</Badge>
                      </td>
                      <td className={`${tdClass} text-slate-600 dark:text-slate-400`}>{measuredText(run)}</td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        )}
      </Card>

      <Card title="Open risico's">
        {c.risks.length === 0 ? (
          <p className="text-sm text-slate-500">
            Geen open risico&apos;s op de kaarten Back-ups, Herstelbaarheid en Redundantie.
          </p>
        ) : (
          <ul className="space-y-2 text-sm">
            {c.risks.map((f, i) => {
              const sev = severityInfo[f.severity];
              return (
                <li key={`${f.check}-${i}`} className="flex items-start gap-2">
                  <Badge tone={sev.tone}>{sev.label}</Badge>
                  <span>{f.text}</span>
                </li>
              );
            })}
          </ul>
        )}
      </Card>
    </section>
  );
}
