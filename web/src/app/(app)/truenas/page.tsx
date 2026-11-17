"use client";

import { Suspense } from "react";
import { Empty, formatBytes, QueryState, tableClass, tdClass, thClass } from "@/components/inventory/bits";
import { Meter, uptime } from "@/components/pve/bits";
import { Tile as Win, TileGrid } from "@/components/Tiles";
import { TrueNASGate, tnTime, useTrueNASRunner } from "@/components/truenas-bits";
import { Alert, Badge, Button, Card, PageHeader } from "@/components/ui";
import { useMe } from "@/lib/auth";
import { tempOf, tnDate, useTrueNASOverview, type TNAlert, type TNPool, type TrueNASOverview, type TrueNASServer } from "@/lib/truenas";

export default function TrueNASPage() {
  return (
    <div className="space-y-6">
      <PageHeader
        title="TrueNAS"
        description="Je TrueNAS in één blik: pools met gezondheid, gebruik en scrub, schijven met temperatuur en SMART, meldingen en taken. Alles via de ClusterForge-agent op TrueNAS."
      />
      <Suspense>
        <TrueNASGate>{(s) => <OverviewView key={s.node_id} srv={s} />}</TrueNASGate>
      </Suspense>
    </div>
  );
}

function OverviewView({ srv }: { srv: TrueNASServer }) {
  const o = useTrueNASOverview(srv.node_id);
  const me = useMe();
  const admin = me.data?.user.role === "admin";
  const runner = useTrueNASRunner(srv.node_id);
  return (
    <QueryState q={o}>
      {o.data && (
        <div className="space-y-6">
          {!srv.persistent && (
            <Alert kind="info">
              De agent staat nog niet in /data/clusterforge en verdwijnt bij een update van TrueNAS. Werk hem één keer bij via Nodes → Agents bijwerken;
              daarna start hij vanzelf na elke update.
            </Alert>
          )}
          {runner.banner}
          {o.data.warnings.length > 0 && (
            <Card title="Aandacht nodig">
              <ul className="space-y-1.5 text-sm">
                {o.data.warnings.map((w) => (
                  <li key={w.text} className="flex items-start gap-2">
                    <Badge tone={w.level === "critical" ? "red" : "amber"}>{w.level === "critical" ? "Ernstig" : "Let op"}</Badge>
                    <span>{w.text}</span>
                  </li>
                ))}
              </ul>
            </Card>
          )}
          <TileGrid>
            <Win wide>
              <PoolsCard o={o.data} admin={admin} runner={runner} />
            </Win>
            <Win>
              <SystemCard o={o.data} />
            </Win>
            <Win wide>
              <DisksCard o={o.data} admin={admin} runner={runner} />
            </Win>
            <Win>
              <AlertsCard alerts={o.data.parts.alerts ?? []} admin={admin} runner={runner} />
            </Win>
            <Win wide>
              <JobsCard o={o.data} admin={admin} runner={runner} />
            </Win>
          </TileGrid>
          {Object.keys(o.data.errors).length > 0 && (
            <p className="text-xs text-slate-500">
              Niet gelezen:{" "}
              {Object.entries(o.data.errors)
                .map(([k, v]) => `${k} (${v})`)
                .join(" · ")}
            </p>
          )}
        </div>
      )}
    </QueryState>
  );
}

type Runner = ReturnType<typeof useTrueNASRunner>;

function SystemCard({ o }: { o: TrueNASOverview }) {
  const s = o.parts.system;
  const boot = o.parts.boot;
  return (
    <Card title="Machine">
      {!s ? (
        <Empty>Niet gelezen.</Empty>
      ) : (
        <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1 text-sm">
          <dt className="text-slate-500">Versie</dt>
          <dd>{s.version ?? o.server.version}</dd>
          <dt className="text-slate-500">Naam</dt>
          <dd>{s.hostname}</dd>
          <dt className="text-slate-500">Hardware</dt>
          <dd>{[s.system_manufacturer, s.system_product].filter(Boolean).join(" ") || "–"}</dd>
          <dt className="text-slate-500">CPU</dt>
          <dd>
            {s.model} {s.cores ? `(${s.cores} threads)` : ""}
          </dd>
          <dt className="text-slate-500">Geheugen</dt>
          <dd>
            {s.physmem ? formatBytes(s.physmem) : "–"}
            {s.ecc_memory ? " ECC" : ""}
          </dd>
          <dt className="text-slate-500">Load</dt>
          <dd>{s.loadavg?.map((l) => l.toFixed(2)).join(" / ") ?? "–"}</dd>
          <dt className="text-slate-500">Uptime</dt>
          <dd>{uptime(Math.round(s.uptime_seconds ?? 0))}</dd>
          <dt className="text-slate-500">Boot-pool</dt>
          <dd>{boot?.status ?? "–"}</dd>
          <dt className="text-slate-500">Agent</dt>
          <dd>
            {o.server.agent_version} {o.server.persistent ? "· blijft bij updates" : "· nog niet blijvend"}
          </dd>
        </dl>
      )}
    </Card>
  );
}

function scanText(p: TNPool) {
  const sc = p.scan;
  if (!sc || !sc.function) return "nog geen scrub";
  const what = sc.function === "SCRUB" ? "Scrub" : sc.function === "RESILVER" ? "Resilver" : sc.function;
  if (sc.state === "SCANNING") return `${what} bezig: ${Math.round(sc.percentage ?? 0)}%`;
  const end = tnDate(sc.end_time);
  return `${what} ${end ? tnTime.format(end) : ""} · ${sc.errors ?? 0} fouten`;
}

function PoolsCard({ o, admin, runner }: { o: TrueNASOverview; admin: boolean; runner: Runner }) {
  const pools = o.parts.pools ?? [];
  return (
    <Card title="Pools">
      {pools.length === 0 ? (
        <Empty>Geen pools.</Empty>
      ) : (
        <div className="overflow-x-auto">
          <table className={tableClass}>
            <thead>
              <tr>
                <th className={thClass}>Pool</th>
                <th className={thClass}>Toestand</th>
                <th className={thClass}>Gebruik</th>
                <th className={thClass}>Scrub</th>
                {admin && <th className={thClass} />}
              </tr>
            </thead>
            <tbody>
              {pools.map((p) => {
                const used = p.size ? ((p.allocated ?? 0) * 100) / p.size : 0;
                const scanning = p.scan?.state === "SCANNING" && p.scan.function === "SCRUB";
                return (
                  <tr key={p.name}>
                    <td className={tdClass}>
                      <span className="font-medium">{p.name}</span>
                    </td>
                    <td className={tdClass}>
                      <Badge tone={p.status === "ONLINE" && p.healthy ? "green" : "red"}>{p.status}</Badge>
                      {p.status_detail && <div className="mt-1 text-xs text-slate-500">{p.status_detail}</div>}
                    </td>
                    <td className={tdClass}>
                      <Meter value={used} warn={80} crit={90} label={`gebruik ${p.name}`} />
                      <div className="text-xs text-slate-500">
                        {formatBytes(p.allocated ?? 0)} van {formatBytes(p.size ?? 0)}
                        {p.fragmentation ? ` · fragmentatie ${p.fragmentation}%` : ""}
                      </div>
                    </td>
                    <td className={tdClass}>
                      <span className="text-sm">{scanText(p)}</span>
                    </td>
                    {admin && (
                      <td className={tdClass}>
                        <Button
                          variant="secondary"
                          disabled={runner.pending}
                          onClick={() => runner.run({ method: "pool.scrub.scrub", args: [p.name, scanning ? "STOP" : "START"] })}
                        >
                          {scanning ? "Scrub stoppen" : "Scrub starten"}
                        </Button>
                      </td>
                    )}
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
    </Card>
  );
}

function DisksCard({ o, admin, runner }: { o: TrueNASOverview; admin: boolean; runner: Runner }) {
  const disks = [...(o.parts.disks ?? [])].sort((a, b) => a.name.localeCompare(b.name, "nl", { numeric: true }));
  const temps = o.parts.temps ?? {};
  const smart = new Map((o.parts.smart ?? []).map((s) => [s.disk ?? s.identifier ?? "", s.tests?.[0]]));
  return (
    <Card title={`Schijven${disks.length ? ` · ${disks.length}` : ""}`}>
      {disks.length === 0 ? (
        <Empty>Geen schijven gelezen.</Empty>
      ) : (
        <div className="overflow-x-auto">
          <table className={tableClass}>
            <thead>
              <tr>
                <th className={thClass}>Schijf</th>
                <th className={thClass}>Model</th>
                <th className={thClass}>Grootte</th>
                <th className={thClass}>Pool</th>
                <th className={thClass}>Temp.</th>
                <th className={thClass}>Laatste SMART-test</th>
                {admin && <th className={thClass} />}
              </tr>
            </thead>
            <tbody>
              {disks.map((d) => {
                const t = tempOf(temps[d.name]);
                const last = smart.get(d.name) ?? smart.get(d.identifier);
                const bad = last?.status && !["SUCCESS", "RUNNING"].includes(last.status);
                return (
                  <tr key={d.identifier}>
                    <td className={tdClass}>
                      <span className="font-medium">{d.name}</span>
                      <div className="text-xs text-slate-500">{d.serial}</div>
                    </td>
                    <td className={tdClass}>
                      {d.model} <span className="text-xs text-slate-500">{d.type}</span>
                    </td>
                    <td className={tdClass}>{formatBytes(d.size ?? 0)}</td>
                    <td className={tdClass}>{d.pool ?? "–"}</td>
                    <td className={tdClass}>
                      {t == null ? "–" : <Badge tone={t >= 55 ? "red" : t >= 48 ? "amber" : "slate"}>{Math.round(t)} °C</Badge>}
                    </td>
                    <td className={tdClass}>
                      {last ? (
                        <span>
                          <Badge tone={bad ? "red" : "green"}>{last.status}</Badge> <span className="text-xs text-slate-500">{last.description}</span>
                        </span>
                      ) : (
                        "–"
                      )}
                    </td>
                    {admin && (
                      <td className={tdClass}>
                        <Button
                          variant="ghost"
                          disabled={runner.pending}
                          onClick={() => runner.run({ method: "smart.test.manual_test", args: [[{ identifier: d.identifier, type: "SHORT" }]] })}
                        >
                          Korte test
                        </Button>
                      </td>
                    )}
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
    </Card>
  );
}

const levelTone = (l: string) => (["CRITICAL", "ALERT", "EMERGENCY", "ERROR"].includes(l) ? "red" : l === "WARNING" ? "amber" : "slate");

function AlertsCard({ alerts, admin, runner }: { alerts: TNAlert[]; admin: boolean; runner: Runner }) {
  const open = alerts.filter((a) => !a.dismissed);
  const dismissed = alerts.length - open.length;
  return (
    <Card title={`Meldingen${open.length ? ` · ${open.length}` : ""}`}>
      {open.length === 0 ? (
        <p className="text-sm text-slate-500">Geen open meldingen{dismissed ? ` (${dismissed} weggeklikt)` : ""}.</p>
      ) : (
        <ul className="space-y-2 text-sm">
          {open.map((a) => {
            const d = tnDate(a.datetime);
            return (
              <li key={a.uuid} className="flex items-start gap-2">
                <Badge tone={levelTone(a.level)}>{a.level}</Badge>
                <div className="min-w-0 flex-1">
                  <div className="break-words">{a.formatted}</div>
                  {d && <div className="text-xs text-slate-500">{tnTime.format(d)}</div>}
                </div>
                {admin && (
                  <Button variant="ghost" disabled={runner.pending} onClick={() => runner.run({ method: "alert.dismiss", args: [a.uuid] })}>
                    Wegklikken
                  </Button>
                )}
              </li>
            );
          })}
        </ul>
      )}
    </Card>
  );
}

function JobsCard({ o, admin, runner }: { o: TrueNASOverview; admin: boolean; runner: Runner }) {
  const running = o.parts.running ?? [];
  const failed = o.parts.failed ?? [];
  return (
    <Card title="Taken op TrueNAS">
      <h3 className="mb-2 text-sm font-medium">Bezig{running.length ? ` · ${running.length}` : ""}</h3>
      {running.length === 0 ? (
        <p className="mb-4 text-sm text-slate-500">Niets bezig.</p>
      ) : (
        <ul className="mb-4 space-y-2 text-sm">
          {running.map((j) => (
            <li key={j.id} className="flex items-center gap-3">
              <span className="font-mono text-xs">{j.method}</span>
              <span className="flex-1 text-slate-500">{j.progress?.description}</span>
              <span className="w-32">
                <Meter value={j.progress?.percent ?? 0} warn={101} crit={101} />
              </span>
              {admin && (
                <Button variant="ghost" disabled={runner.pending} onClick={() => runner.run({ method: "core.job_abort", args: [j.id] })}>
                  Afbreken
                </Button>
              )}
            </li>
          ))}
        </ul>
      )}
      <h3 className="mb-2 text-sm font-medium">Laatst mislukt</h3>
      {failed.length === 0 ? (
        <p className="text-sm text-slate-500">Geen mislukte taken.</p>
      ) : (
        <ul className="space-y-1.5 text-sm">
          {failed.map((j) => {
            const d = tnDate(j.time_finished ?? j.time_started);
            return (
              <li key={j.id}>
                <span className="font-mono text-xs">{j.method}</span>
                {d && <span className="ml-2 text-xs text-slate-500">{tnTime.format(d)}</span>}
                {j.error && <div className="text-xs break-words text-red-700 dark:text-red-400">{j.error.split("\n")[0]}</div>}
              </li>
            );
          })}
        </ul>
      )}
    </Card>
  );
}
