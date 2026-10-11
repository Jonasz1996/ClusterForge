"use client";

import Link from "next/link";
import { useRouter } from "next/navigation";
import { BigResult } from "@/components/backups/Verify";
import { Checks, reportHref } from "@/components/failover/Failover";
import { CopyBlock } from "@/components/inventory/InstallAgent";
import { formatBytes, tableClass, tdClass, thClass } from "@/components/inventory/bits";
import { Modal } from "@/components/Modal";
import { Alert, Badge, Button, Card, cx } from "@/components/ui";
import { ApiError } from "@/lib/api/client";
import { backupTimeFmt } from "@/lib/backups";
import {
  minutesText,
  useDisasterPreview,
  useRunDisaster,
  type DisasterScenario,
  type DRPlan,
  type DRReport,
} from "@/lib/disaster";
import { list, probeText, type TestRun } from "@/lib/failover";
import { duration } from "@/lib/verify";

const linkClass = "text-brand-600 hover:underline dark:text-brand-500";
const dlClass = "grid gap-x-4 gap-y-2.5 text-sm sm:grid-cols-[9rem_1fr]";
const fmt = new Intl.DateTimeFormat("nl-BE", { dateStyle: "medium", timeStyle: "short" });

// FixCommands zijn de commando's die de bridge op de host afsluiten.
function FixCommands({ fix, host, bridge }: { fix: string[]; host: string; bridge: string }) {
  if (fix.length === 0) return null;
  return (
    <div className="space-y-2 text-sm">
      <p>
        Draai dit als root op {host || "de Proxmox-host"}. Het maakt {bridge} een bridge zonder poorten en zonder adres, zodat de
        sandboxes elkaar zien maar niets anders. Het laatste commando past het netwerk toe.
      </p>
      <CopyBlock text={fix.join("\n")} />
    </div>
  );
}

// Target toont een meting tegen haar doel.
function Target({ value, target, met, unit }: { value: string; target: number; met: boolean | null; unit: "min" | "u" }) {
  return (
    <span className="inline-flex flex-wrap items-center gap-2">
      <span className="tabular-nums">{value}</span>
      {target > 0 ? (
        <>
          <span className="text-slate-500">doel {unit === "min" ? minutesText(target) : `${target} u`}</span>
          {met !== null && <Badge tone={met ? "green" : "amber"}>{met ? "gehaald" : "niet gehaald"}</Badge>}
        </>
      ) : (
        <span className="text-slate-500">geen doel</span>
      )}
    </span>
  );
}

// PlanTable toont per node welke back-up teruggaat.
function PlanTable({ p }: { p: DRPlan }) {
  return (
    <div className="overflow-x-auto">
      <table className={tableClass}>
        <thead className="border-b border-slate-200 dark:border-slate-800">
          <tr>
            <th className={thClass}>Node</th>
            <th className={thClass}>Back-up</th>
            <th className={thClass}>Vraagt</th>
          </tr>
        </thead>
        <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
          {p.nodes.map((n) => (
            <tr key={n.node_id}>
              <td className={tdClass}>
                {n.hostname}
                <span className="block text-xs text-slate-500">
                  VM {n.source_vmid}
                  {n.address && ` · ${n.address}`}
                </span>
              </td>
              <td className={tdClass}>
                {backupTimeFmt.format(new Date(n.backup_time))}
                <span className="block text-xs text-slate-500">
                  {n.backup_storage} · {formatBytes(n.size_bytes)}
                </span>
              </td>
              <td className={`${tdClass} text-xs whitespace-nowrap text-slate-600 dark:text-slate-400`}>
                {n.memory_bytes > 0 ? `${formatBytes(n.memory_bytes)} geheugen` : "–"}
                {n.disk_bytes > 0 && <span className="block">{formatBytes(n.disk_bytes)} schijf</span>}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

// RehearsalStart doet de voorcontrole van een DR-repetitie en start haar.
// Ze raakt het echte cluster niet, dus er is geen slug om in te tikken.
export function RehearsalStart({ s, onClose }: { s: DisasterScenario; onClose: () => void }) {
  const preview = useDisasterPreview(s.id, true);
  const run = useRunDisaster();
  const router = useRouter();
  const p = preview.data;
  const plan = p?.rehearsal ?? null;
  const err = run.error instanceof ApiError ? run.error : null;
  const failed = p?.checks.filter((c) => !c.ok && !c.warning).length ?? 0;
  return (
    <Modal title={`DR-repetitie: ${s.name}`} onClose={onClose} wide>
      <div className="space-y-4 text-sm">
        {preview.isLoading && <p className="text-slate-500">Voorcontrole…</p>}
        {preview.error && <Alert>{preview.error.message}</Alert>}
        {p && plan && (
          <>
            <p>
              ClusterForge zet elke node van {plan.cluster_name} terug uit zijn nieuwste back-up, als sandbox in pool cf-sandbox
              {plan.host && ` op ${plan.host}`}, met de netwerkkaarten aan de afgesloten bridge {plan.bridge}. Het echte cluster
              blijft draaien. Verhuist een VIP of komt er split-brain, dan gaan alle sandboxes meteen hard uit. Na de controles
              verwijdert ClusterForge alles.
            </p>
            {plan.nodes.length > 0 && <PlanTable p={plan} />}
            <dl className={dlClass}>
              {plan.host && (
                <>
                  <dt className="text-slate-500">Waar</dt>
                  <dd>
                    {plan.host}, storage {plan.storage}
                    <span className="text-slate-500"> ({plan.connection})</span>
                  </dd>
                </>
              )}
              {plan.vips.length > 0 && (
                <>
                  <dt className="text-slate-500">Controle</dt>
                  <dd>
                    {plan.vips.map((v) => (
                      <span key={v.address} className="block">
                        VIP <span className="font-mono">{v.address}</span> heeft één houder en {probeText(v.probe)}
                      </span>
                    ))}
                  </dd>
                </>
              )}
              <dt className="text-slate-500">RPO nu</dt>
              <dd>
                <Target
                  value={plan.nodes.length ? duration(plan.rpo_seconds) : "–"}
                  target={plan.rpo_target_hours}
                  met={plan.nodes.length && plan.rpo_target_hours ? plan.rpo_seconds <= plan.rpo_target_hours * 3600 : null}
                  unit="u"
                />
              </dd>
              <dt className="text-slate-500">RTO-doel</dt>
              <dd>{plan.rto_target_minutes > 0 ? minutesText(plan.rto_target_minutes) : "geen doel"}</dd>
            </dl>
            <details open={failed > 0} className="rounded-md border border-slate-200 p-3 dark:border-slate-800">
              <summary className="cursor-pointer font-medium">
                Voorcontrole{" "}
                <span className="font-normal text-slate-500">
                  {failed === 0
                    ? `alle ${p.checks.length} controles in orde`
                    : `${failed} van ${p.checks.length} controles mislukt`}
                </span>
              </summary>
              <div className="mt-3">
                <Checks checks={p.checks} />
              </div>
            </details>
            <FixCommands fix={plan.fix} host={plan.host} bridge={plan.bridge} />
          </>
        )}
        {err?.code === "precheck_failed" && err.checks ? (
          <div className="space-y-2">
            <Alert>De DR-repetitie start niet, omdat de voorcontrole faalt.</Alert>
            <Checks checks={err.checks} />
          </div>
        ) : (
          run.error && <Alert>{run.error.message}</Alert>
        )}
        <div className="flex justify-end gap-2">
          <Button variant="secondary" onClick={onClose}>
            Annuleren
          </Button>
          <Button
            disabled={!p?.ok || run.isPending}
            onClick={async () => {
              const r = await run.mutateAsync({ id: s.id, confirm: [] }).catch(() => null);
              if (r) router.push(reportHref(r.id));
            }}
          >
            {run.isPending ? "Voorcontrole…" : "Repetitie starten"}
          </Button>
        </div>
      </div>
    </Modal>
  );
}

// RehearsalReportView is het rapport van een DR-repetitie: RTO en RPO
// tegen de doelen, per node wat er terugkwam, en het VIP in de sandbox.
export function RehearsalReportView({ run, d }: { run: TestRun; d: DRReport }) {
  const active = !run.result;
  return (
    <>
      <Card>
        <div className="flex flex-wrap items-start gap-4">
          <BigResult run={run} />
          <p
            className={cx(
              "min-w-56 flex-1 text-sm leading-6",
              run.result === "fail" || run.result === "error" ? "text-red-700 dark:text-red-300" : "",
            )}
          >
            {run.summary || (run.job_status === "queued" ? "De repetitie wacht op haar beurt." : "De repetitie loopt…")}
          </p>
        </div>
        <dl className={cx(dlClass, "mt-5")}>
          <dt className="text-slate-500">Cluster</dt>
          <dd>
            {run.cluster_id ? (
              <Link href={`/clusters/detail?id=${run.cluster_id}`} className={linkClass}>
                {d.cluster_name}
              </Link>
            ) : (
              d.cluster_name
            )}
            {d.environment && <span className="text-slate-500"> · {d.environment}</span>}
          </dd>
          <dt className="text-slate-500">Waar</dt>
          <dd>
            {d.host ? (
              <>
                {d.host}, storage {d.storage}, bridge {d.bridge}
                {d.connection && <span className="text-slate-500"> ({d.connection})</span>}
              </>
            ) : (
              <>bridge {d.bridge}</>
            )}
          </dd>
          <dt className="text-slate-500">RTO</dt>
          <dd>
            <Target
              value={
                d.rto_seconds !== null
                  ? duration(d.rto_seconds)
                  : active
                    ? "wordt gemeten"
                    : "niet gehaald, het cluster kwam niet werkend terug"
              }
              target={d.rto_target_minutes}
              met={d.rto_met}
              unit="min"
            />
            <span className="block text-xs text-slate-500">Van de start van het terugzetten tot alle controles slaagden.</span>
          </dd>
          <dt className="text-slate-500">RPO</dt>
          <dd>
            <Target
              value={d.rpo_seconds !== null ? duration(d.rpo_seconds) : "–"}
              target={d.rpo_target_hours}
              met={d.rpo_met}
              unit="u"
            />
            <span className="block text-xs text-slate-500">De leeftijd van de oudste back-up bij de start.</span>
          </dd>
          {(d.restore_seconds !== null || d.boot_seconds !== null) && (
            <>
              <dt className="text-slate-500">Fasen</dt>
              <dd className="tabular-nums">
                {[
                  d.restore_seconds !== null && `terugzetten ${duration(d.restore_seconds)}`,
                  d.boot_seconds !== null && `opstarten ${duration(d.boot_seconds)}`,
                  d.check_seconds !== null && `controleren ${duration(d.check_seconds)}`,
                  d.cleanup_seconds !== null && `opruimen ${duration(d.cleanup_seconds)}`,
                ]
                  .filter(Boolean)
                  .join(", ")}
              </dd>
            </>
          )}
          {d.aborted && (
            <>
              <dt className="text-slate-500">Afgebroken</dt>
              <dd className="text-red-700 dark:text-red-300">{d.aborted}; alle sandboxes zijn meteen hard uitgezet.</dd>
            </>
          )}
          {run.finished_at && (
            <>
              <dt className="text-slate-500">Klaar</dt>
              <dd>
                {fmt.format(new Date(run.finished_at))}
                {d.destroyed_at && (
                  <span className="text-slate-500"> · sandboxes verwijderd om {fmt.format(new Date(d.destroyed_at))}</span>
                )}
              </dd>
            </>
          )}
        </dl>
      </Card>
      {d.fix.length > 0 && (
        <Card title="Bridge afsluiten">
          <FixCommands fix={d.fix} host={d.host} bridge={d.bridge} />
        </Card>
      )}
      {d.nodes.length > 0 && (
        <Card title="Nodes">
          <div className="overflow-x-auto">
            <table className={tableClass}>
              <thead className="border-b border-slate-200 dark:border-slate-800">
                <tr>
                  <th className={thClass}>Node</th>
                  <th className={thClass}>Back-up</th>
                  <th className={thClass}>Sandbox</th>
                  <th className={thClass}>Terug na</th>
                  <th className={thClass}>Adressen</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
                {d.nodes.map((n) => (
                  <tr key={n.node_id}>
                    <td className={tdClass}>
                      <Link href={`/nodes/detail?id=${n.node_id}`} className={linkClass}>
                        {n.hostname}
                      </Link>
                      <span className="block text-xs text-slate-500">bron VM {n.source_vmid}</span>
                    </td>
                    <td className={`${tdClass} whitespace-nowrap`}>{backupTimeFmt.format(new Date(n.backup_time))}</td>
                    <td className={`${tdClass} whitespace-nowrap`}>
                      {n.sandbox_vmid !== null ? `VM ${n.sandbox_vmid}` : "–"}
                      {n.sandbox_vmid !== null && (
                        <span className="block text-xs text-slate-500">
                          {n.destroyed ? "verwijderd" : active ? "bestaat" : "nog niet verwijderd"}
                        </span>
                      )}
                    </td>
                    <td className={`${tdClass} text-xs whitespace-nowrap tabular-nums`}>
                      {n.restore_seconds !== null ? `teruggezet ${duration(n.restore_seconds)}` : "–"}
                      {n.boot_seconds !== null && <span className="block">opgestart {duration(n.boot_seconds)}</span>}
                    </td>
                    <td className={`${tdClass} font-mono text-xs`}>
                      {n.addresses.length ? (
                        n.addresses.join(", ")
                      ) : (
                        <span className="font-sans text-slate-400">niet gemeld</span>
                      )}
                      {n.agent_version && <span className="block font-sans text-slate-500">cf-agent {n.agent_version}</span>}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          {d.vips.length > 0 && (
            <ul className="mt-4 space-y-1 text-sm">
              {d.vips.map((v) => (
                <li key={v.address}>
                  VIP <span className="font-mono">{v.address}</span>:{" "}
                  {!v.checked
                    ? "niet gecontroleerd"
                    : v.holders.length === 1
                      ? `houder ${v.holders[0]}`
                      : v.holders.length === 0
                        ? active
                          ? "nog geen houder"
                          : "geen houder"
                        : `${list(v.holders)}, split-brain in de sandbox`}
                  {v.probe_ok !== null && (
                    <span className={v.probe_ok ? "text-emerald-700 dark:text-emerald-400" : "text-red-700 dark:text-red-300"}>
                      {" "}
                      · {v.probe} {v.probe_ok ? "in orde" : "mislukt"}
                    </span>
                  )}
                </li>
              ))}
            </ul>
          )}
        </Card>
      )}
      {run.checks.length > 0 && (
        <Card title="Controles">
          <Checks checks={run.checks} />
        </Card>
      )}
      {run.timeline.length > 0 && (
        <Card title="Tijdlijn">
          <ol className="space-y-1 text-sm">
            {run.timeline.map((e, i) => (
              <li key={i} className="grid grid-cols-[5.5rem_1fr] gap-2">
                <span className="text-right text-slate-500 tabular-nums">+{duration(e.t_ms / 1000)}</span>
                <span>{e.text}</span>
              </li>
            ))}
          </ol>
        </Card>
      )}
    </>
  );
}
