"use client";

import Link from "next/link";
import { ago, formatBytes } from "@/components/inventory/bits";
import { Alert, Badge, Button, PageHeader } from "@/components/ui";
import { ApiError } from "@/lib/api/client";
import { envLabel, groupHosts, hostReady, useDockerHosts, useRefreshDocker, type DockerHost } from "@/lib/docker";

function errText(e: unknown) {
  return e instanceof ApiError ? e.message : "Er ging iets mis.";
}

// Docker → Overzicht: elke node met Docker, per cluster, met wat er draait,
// wat aandacht vraagt en hoeveel schijf Docker gebruikt.
export default function DockerOverview() {
  const q = useDockerHosts();
  const refresh = useRefreshDocker();
  const hosts = q.data?.hosts ?? [];
  const need = q.data?.protocol ?? 46;
  const groups = groupHosts(hosts);
  const t = hosts.reduce(
    (a, h) => {
      const s = h.summary;
      if (!s || hostReady(h, need)) return a;
      a.running += s.running;
      a.stopped += s.stopped + s.paused;
      a.problems += s.problems?.length ?? 0;
      a.images += s.images;
      a.disk += (s.disk?.images ?? 0) + (s.disk?.containers ?? 0) + (s.disk?.volumes ?? 0) + (s.disk?.build_cache ?? 0);
      a.reclaim += s.disk?.reclaimable ?? 0;
      return a;
    },
    { running: 0, stopped: 0, problems: 0, images: 0, disk: 0, reclaim: 0 },
  );
  const old = hosts.filter((h) => h.online && !h.supported).length;

  return (
    <div className="space-y-6">
      <PageHeader
        title="Docker"
        description="Docker op elke node met een agent, per cluster. De agent leest de lokale Docker-socket; die gaat nooit over het netwerk."
        actions={
          <Button variant="secondary" onClick={() => refresh.mutate()} disabled={refresh.isPending}>
            {refresh.isPending ? "Vernieuwen…" : "Vernieuwen"}
          </Button>
        }
      />
      {q.isError && <Alert>{errText(q.error)}</Alert>}
      {refresh.isError && <Alert>{errText(refresh.error)}</Alert>}
      {q.isLoading && <p className="text-sm text-slate-500">Laden…</p>}
      {q.data && hosts.length === 0 && (
        <Alert kind="info">
          Nog geen node met Docker. Installeer de agent op een Docker-host via{" "}
          <Link href="/nodes" className="underline">
            Nodes → Agent installeren
          </Link>
          ; ClusterForge herkent Docker vanzelf.
        </Alert>
      )}
      {old > 0 && (
        <Alert kind="info">
          {old === 1 ? "Eén agent is" : `${old} agents zijn`} te oud voor Docker (protocol {need} nodig). Werk ze bij via{" "}
          <Link href="/nodes" className="underline">
            Nodes → Agents bijwerken
          </Link>
          .
        </Alert>
      )}
      {hosts.length > 0 && (
        <div className="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-6">
          <Stat label="Nodes" value={hosts.length} />
          <Stat label="Draaiend" value={t.running} />
          <Stat label="Gestopt" value={t.stopped} />
          <Stat label="Aandacht" value={t.problems} tone={t.problems > 0 ? "red" : undefined} />
          <Stat label="Images" value={t.images} />
          <Stat label="Schijf" value={formatBytes(t.disk)} sub={t.reclaim > 0 ? `${formatBytes(t.reclaim)} op te ruimen` : undefined} />
        </div>
      )}
      {groups.map((g) => (
        <section key={g.key || "los"} className="space-y-3">
          <h2 className="flex flex-wrap items-center gap-2 text-sm font-semibold tracking-wide text-slate-500 uppercase">
            {g.key ? (
              <Link href={`/clusters/${g.key}`} className="hover:underline">
                {g.label}
              </Link>
            ) : (
              g.label
            )}
            {g.environment && <Badge tone={g.environment === "prod" ? "red" : "slate"}>{envLabel[g.environment] ?? g.environment}</Badge>}
            <span className="font-normal normal-case">
              · {g.hosts.length} {g.hosts.length === 1 ? "node" : "nodes"}
            </span>
          </h2>
          <div className="grid gap-3 md:grid-cols-2 xl:grid-cols-3">
            {g.hosts.map((h) => (
              <HostCard key={h.node_id} h={h} need={need} />
            ))}
          </div>
        </section>
      ))}
    </div>
  );
}

function Stat({ label, value, sub, tone }: { label: string; value: number | string; sub?: string; tone?: "red" }) {
  return (
    <div className="rounded-lg border border-slate-200 bg-white px-4 py-3 dark:border-slate-800 dark:bg-slate-900">
      <div className="text-xs text-slate-500">{label}</div>
      <div className={`text-xl font-semibold tabular-nums ${tone === "red" ? "text-red-600 dark:text-red-400" : ""}`}>{value}</div>
      {sub && <div className="text-xs text-slate-500">{sub}</div>}
    </div>
  );
}

function HostCard({ h, need }: { h: DockerHost; need: number }) {
  const why = hostReady(h, need);
  const s = h.summary;
  const disk = s?.disk;
  return (
    <div className="space-y-3 rounded-lg border border-slate-200 bg-white p-4 dark:border-slate-800 dark:bg-slate-900">
      <div className="flex flex-wrap items-center gap-2">
        <Link href={`/nodes/${h.node_id}`} className="font-medium hover:underline">
          {h.hostname}
        </Link>
        {h.vip_holder && <Badge tone="blue">VIP</Badge>}
        {why ? <Badge tone="amber">{why}</Badge> : h.check_error ? <Badge tone="red">fout</Badge> : null}
        <span className="ml-auto text-xs text-slate-500">Docker {s?.version || h.docker_version || "?"}</span>
      </div>
      {h.check_error && !why && <p className="text-xs text-red-600 dark:text-red-400">{h.check_error}</p>}
      {s ? (
        <>
          <div className="flex flex-wrap gap-x-4 gap-y-1 text-sm">
            <span>
              <b className="tabular-nums">{s.running}</b> draaiend
            </span>
            <span className="text-slate-500">
              <b className="tabular-nums">{s.stopped}</b> gestopt
            </span>
            {s.paused > 0 && (
              <span className="text-slate-500">
                <b className="tabular-nums">{s.paused}</b> gepauzeerd
              </span>
            )}
            <span className="text-slate-500">
              <b className="tabular-nums">{s.images}</b> images
            </span>
          </div>
          {disk && (
            <div className="text-xs text-slate-500">
              Schijf: images {formatBytes(disk.images)}, volumes {formatBytes(disk.volumes)}, containers {formatBytes(disk.containers)}
              {disk.build_cache > 0 && `, buildcache ${formatBytes(disk.build_cache)}`}
              {disk.reclaimable > 0 && <> · {formatBytes(disk.reclaimable)} op te ruimen</>}
            </div>
          )}
          {(s.problems?.length ?? 0) > 0 && (
            <ul className="space-y-1 text-sm">
              {s.problems!.map((p) => (
                <li key={p.id} className="flex flex-wrap items-baseline gap-2">
                  <Badge tone={p.reason === "unhealthy" || p.reason === "herstartlus" ? "red" : "amber"}>{p.reason}</Badge>
                  <span className="font-mono text-xs break-all">{p.name}</span>
                  <span className="text-xs text-slate-500">{p.status}</span>
                </li>
              ))}
            </ul>
          )}
          {(s.warnings?.length ?? 0) > 0 && (
            <details className="text-xs text-amber-700 dark:text-amber-400">
              <summary className="cursor-pointer">{s.warnings!.length} waarschuwing(en) van Docker</summary>
              <ul className="mt-1 list-disc pl-4">
                {s.warnings!.map((w) => (
                  <li key={w}>{w}</li>
                ))}
              </ul>
            </details>
          )}
          <div className="text-xs text-slate-400">
            {s.os}
            {s.cpus > 0 && ` · ${s.cpus} CPU · ${formatBytes(s.mem_total)} RAM`}
            {h.checked_at && ` · gelezen ${ago(h.checked_at)}`}
          </div>
        </>
      ) : (
        !why && <p className="text-sm text-slate-500">Nog niet gelezen; dat gebeurt elke minuut.</p>
      )}
    </div>
  );
}
