"use client";

import { Suspense } from "react";
import { Empty, QueryState, tableClass, tdClass, thClass } from "@/components/inventory/bits";
import { TrueNASGate, tnTime, useTrueNASRunner } from "@/components/truenas-bits";
import { Badge, Button, Card, PageHeader } from "@/components/ui";
import { useMe } from "@/lib/auth";
import { scheduleText, tnDate, useTrueNASGet, type TNOtherTask, type TNSnapshotTask, type TNTaskState, type TrueNASServer } from "@/lib/truenas";

export default function TrueNASProtectionPage() {
  return (
    <div className="space-y-6">
      <PageHeader
        title="Bescherming"
        description="De snapshot-taken, cloud-sync, rsync en replicatie van TrueNAS met hun laatste uitkomst. Zet ze aan of uit, of voer ze nu uit."
      />
      <Suspense>
        <TrueNASGate>{(s) => <ProtectionView key={s.node_id} srv={s} />}</TrueNASGate>
      </Suspense>
    </div>
  );
}

type Runner = ReturnType<typeof useTrueNASRunner>;

function stateOf(t: { state?: TNTaskState; job?: TNOtherTask["job"] }) {
  const st = t.state?.state ?? t.job?.state;
  const at = tnDate(t.state?.datetime ?? t.job?.time_finished ?? t.job?.time_started);
  const err = t.state?.error ?? t.job?.error;
  return { st, at, err };
}

function StateCell({ t }: { t: { state?: TNTaskState; job?: TNOtherTask["job"] } }) {
  const { st, at, err } = stateOf(t);
  if (!st) return <span className="text-slate-500">nog niet gelopen</span>;
  const tone = st === "FINISHED" || st === "SUCCESS" ? "green" : st === "RUNNING" ? "blue" : st === "PENDING" ? "slate" : "red";
  return (
    <div>
      <Badge tone={tone}>{st}</Badge> {at && <span className="text-xs text-slate-500">{tnTime.format(at)}</span>}
      {err && <div className="text-xs break-words text-red-700 dark:text-red-400">{err.split("\n")[0]}</div>}
    </div>
  );
}

function Actions({ ns, t, admin, runner }: { ns: string; t: { id: number; enabled: boolean }; admin: boolean; runner: Runner }) {
  if (!admin) return null;
  const runMethod = ns === "cloudsync" ? "cloudsync.sync" : `${ns}.run`;
  return (
    <div className="flex gap-1">
      <Button variant="ghost" disabled={runner.pending} onClick={() => runner.run({ method: runMethod, args: [t.id] })}>
        Nu uitvoeren
      </Button>
      <Button variant="ghost" disabled={runner.pending} onClick={() => runner.run({ method: `${ns}.update`, args: [t.id, { enabled: !t.enabled }] })}>
        {t.enabled ? "Uitzetten" : "Aanzetten"}
      </Button>
    </div>
  );
}

function ProtectionView({ srv }: { srv: TrueNASServer }) {
  const id = srv.node_id;
  const me = useMe();
  const admin = me.data?.user.role === "admin";
  const runner = useTrueNASRunner(id);
  const snaps = useTrueNASGet<TNSnapshotTask[]>(id, "pool.snapshottask.query");
  const cloud = useTrueNASGet<TNOtherTask[]>(id, "cloudsync.query");
  const rsync = useTrueNASGet<TNOtherTask[]>(id, "rsynctask.query");
  const repl = useTrueNASGet<TNOtherTask[]>(id, "replication.query");
  return (
    <div className="space-y-6">
      {runner.banner}
      <Card title="Snapshot-taken">
        <QueryState q={snaps}>
          {(snaps.data ?? []).length === 0 ? (
            <Empty>Geen snapshot-taken. Zonder kun je een verwijderd bestand niet terughalen; maak er een in TrueNAS of vraag het in ClusterForge.</Empty>
          ) : (
            <div className="overflow-x-auto">
              <table className={tableClass}>
                <thead>
                  <tr>
                    <th className={thClass}>Dataset</th>
                    <th className={thClass}>Schema</th>
                    <th className={thClass}>Bewaren</th>
                    <th className={thClass}>Laatste</th>
                    <th className={thClass} />
                  </tr>
                </thead>
                <tbody>
                  {(snaps.data ?? []).map((t) => (
                    <tr key={t.id} className={t.enabled ? "" : "opacity-60"}>
                      <td className={tdClass}>
                        {t.dataset}
                        {t.recursive && <span className="text-xs text-slate-500"> (recursief)</span>}
                        {!t.enabled && <Badge>uit</Badge>}
                      </td>
                      <td className={tdClass}>
                        <span className="font-mono text-xs">{scheduleText(t.schedule)}</span>
                      </td>
                      <td className={tdClass}>
                        {t.lifetime_value} {t.lifetime_unit?.toLowerCase()}
                      </td>
                      <td className={tdClass}>
                        <StateCell t={t} />
                      </td>
                      <td className={tdClass}>
                        <Actions ns="pool.snapshottask" t={t} admin={admin} runner={runner} />
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </QueryState>
      </Card>
      <OtherCard title="Cloud-sync" ns="cloudsync" q={cloud} admin={admin} runner={runner} />
      <OtherCard title="Rsync" ns="rsynctask" q={rsync} admin={admin} runner={runner} />
      <OtherCard title="Replicatie" ns="replication" q={repl} admin={admin} runner={runner} />
    </div>
  );
}

function OtherCard({
  title,
  ns,
  q,
  admin,
  runner,
}: {
  title: string;
  ns: string;
  q: ReturnType<typeof useTrueNASGet<TNOtherTask[]>>;
  admin: boolean;
  runner: Runner;
}) {
  const list = q.data ?? [];
  return (
    <Card title={title}>
      <QueryState q={q}>
        {list.length === 0 ? (
          <Empty>Geen taken.</Empty>
        ) : (
          <div className="overflow-x-auto">
            <table className={tableClass}>
              <thead>
                <tr>
                  <th className={thClass}>Taak</th>
                  <th className={thClass}>Schema</th>
                  <th className={thClass}>Laatste</th>
                  <th className={thClass} />
                </tr>
              </thead>
              <tbody>
                {list.map((t) => (
                  <tr key={t.id} className={t.enabled ? "" : "opacity-60"}>
                    <td className={tdClass}>
                      {t.description || t.name || t.path || `#${t.id}`}
                      {t.direction && <span className="text-xs text-slate-500"> · {t.direction.toLowerCase()}</span>}
                      {t.remotehost && <span className="text-xs text-slate-500"> · {t.remotehost}</span>}
                      {!t.enabled && <Badge>uit</Badge>}
                    </td>
                    <td className={tdClass}>
                      <span className="font-mono text-xs">{scheduleText(t.schedule)}</span>
                    </td>
                    <td className={tdClass}>
                      <StateCell t={t} />
                    </td>
                    <td className={tdClass}>
                      <Actions ns={ns} t={t} admin={admin} runner={runner} />
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
