"use client";

import Link from "next/link";
import { useQueries } from "@tanstack/react-query";
import { Suspense, useMemo, useState } from "react";
import { errText, inScope, ScopeBar, stateLabel, stateTone, useDockerAll, useDockerRun, useScope } from "@/components/docker/common";
import { formatBytes, tableClass, tdClass, thClass } from "@/components/inventory/bits";
import { Alert, Badge, Button, PageHeader, cx } from "@/components/ui";
import { api, unwrap } from "@/lib/api/client";
import {
  composeProject,
  containerName,
  cpath,
  envLabel,
  groupHosts,
  hostReady,
  portsText,
  useDockerHosts,
  type ContainerSummary,
  type DockerCall,
  type DockerHost,
  type DockerStat,
} from "@/lib/docker";
import { useIsAdmin } from "@/lib/inventory";

export default function Page() {
  return (
    <Suspense>
      <Containers />
    </Suspense>
  );
}

type Row = { host: DockerHost; c: ContainerSummary; name: string; project: string; managed?: { id: string; name: string } };

const filters = [
  { key: "", label: "Alle" },
  { key: "running", label: "Draaiend" },
  { key: "stopped", label: "Gestopt" },
  { key: "problem", label: "Aandacht" },
] as const;

function isProblem(c: ContainerSummary) {
  return (
    c.Status.includes("(unhealthy)") ||
    c.State === "restarting" ||
    c.State === "dead" ||
    (c.State === "exited" && !/^Exited \(0\)/.test(c.Status) && c.Labels?.["com.docker.compose.oneoff"] !== "True")
  );
}

function Containers() {
  const isAdmin = useIsAdmin();
  const hostsQ = useDockerHosts();
  const all = useDockerAll<ContainerSummary[]>("/containers/json", { all: "1" }, 10_000);
  const [scope, setScope] = useScope();
  const [search, setSearch] = useState("");
  const [filter, setFilter] = useState<(typeof filters)[number]["key"]>("");
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const { run, modal } = useDockerRun();
  const need = hostsQ.data?.protocol ?? 46;

  const hosts = useMemo(() => (hostsQ.data?.hosts ?? []).filter((h) => inScope(h, scope)), [hostsQ.data, scope]);
  const ready = hosts.filter((h) => !hostReady(h, need));

  // Live CPU en RAM per node, elke 10 s.
  const stats = useQueries({
    queries: ready.map((h) => ({
      queryKey: ["docker", "stats", h.node_id],
      queryFn: async () =>
        (unwrap(await api.GET("/docker/nodes/{nodeId}/stats", { params: { path: { nodeId: h.node_id } } })) as unknown as { stats: DockerStat[] })
          .stats,
      refetchInterval: 10_000,
      retry: false,
    })),
  });
  const statOf = new Map<string, DockerStat>();
  stats.forEach((q) => q.data?.forEach((s) => statOf.set(s.id, s)));

  const rows: Row[] = [];
  const errors: { host: DockerHost; error: string }[] = [];
  for (const r of all.data ?? []) {
    const host = hosts.find((h) => h.node_id === r.node_id);
    if (!host) continue;
    if (r.error) {
      errors.push({ host, error: r.error });
      continue;
    }
    for (const c of r.data ?? []) {
      const name = containerName(c);
      const project = composeProject(c);
      rows.push({ host, c, name, project, managed: host.stacks?.find((s) => s.name === project) });
    }
  }
  const q = search.trim().toLowerCase();
  const shown = rows.filter((r) => {
    if (filter === "running" && r.c.State !== "running") return false;
    if (filter === "stopped" && r.c.State === "running") return false;
    if (filter === "problem" && !isProblem(r.c)) return false;
    if (!q) return true;
    return `${r.name} ${r.c.Image} ${r.project} ${r.host.hostname} ${portsText(r.c.Ports)}`.toLowerCase().includes(q);
  });
  const key = (r: Row) => `${r.host.node_id}/${r.c.Id}`;
  const sel = shown.filter((r) => selected.has(key(r)));

  function toggle(r: Row) {
    const s = new Set(selected);
    if (s.has(key(r))) s.delete(key(r));
    else s.add(key(r));
    setSelected(s);
  }

  // bulk zet per node één taak met de aanroepen voor de gekozen containers.
  async function bulk(verb: "start" | "stop" | "restart" | "remove") {
    const byNode = new Map<string, Row[]>();
    for (const r of sel) byNode.set(r.host.node_id, [...(byNode.get(r.host.node_id) ?? []), r]);
    for (const [node, rs] of byNode) {
      const calls: DockerCall[] = rs.map((r) =>
        verb === "remove"
          ? { method: "DELETE", path: cpath(r.c.Id), query: { force: "1" } }
          : { method: "POST", path: cpath(r.c.Id, "/" + verb) },
      );
      const names = rs.map((r) => r.name).join(", ");
      await run(node, rs[0].host.hostname, calls, {
        what: verb === "remove" ? `Verwijderen op ${rs[0].host.hostname} (ook als ze draaien): ${names}` : undefined,
      });
    }
    setSelected(new Set());
  }

  const groups = groupHosts(hosts);
  const counts = {
    "": rows.length,
    running: rows.filter((r) => r.c.State === "running").length,
    stopped: rows.filter((r) => r.c.State !== "running").length,
    problem: rows.filter((r) => isProblem(r.c)).length,
  };

  return (
    <div className="space-y-5">
      <PageHeader title="Containers" description="Alle containers van alle nodes met Docker, per cluster. CPU en RAM zijn live (elke 10 s)." />
      <ScopeBar hosts={hostsQ.data?.hosts ?? []} scope={scope} setScope={setScope} search={search} setSearch={setSearch}>
        <div className="flex flex-wrap gap-1">
          {filters.map((f) => (
            <button
              key={f.key}
              type="button"
              onClick={() => setFilter(f.key)}
              className={cx(
                "rounded-full border px-3 py-1 text-xs",
                filter === f.key
                  ? "border-brand-600 bg-brand-50 text-brand-700 dark:bg-slate-800 dark:text-white"
                  : "border-slate-300 text-slate-600 hover:border-slate-400 dark:border-slate-700 dark:text-slate-400",
              )}
            >
              {f.label} <span className="tabular-nums">{counts[f.key]}</span>
            </button>
          ))}
        </div>
      </ScopeBar>
      {hostsQ.isError && <Alert>{errText(hostsQ.error)}</Alert>}
      {all.isError && <Alert>{errText(all.error)}</Alert>}
      {all.isLoading && <p className="text-sm text-slate-500">Laden… (de agents worden gevraagd wat er draait)</p>}
      {isAdmin && sel.length > 0 && (
        <div className="sticky top-14 z-10 flex flex-wrap items-center gap-2 rounded-lg border border-brand-200 bg-brand-50 px-3 py-2 text-sm dark:border-slate-700 dark:bg-slate-800">
          <span className="mr-2">{sel.length} gekozen</span>
          <Button variant="secondary" onClick={() => void bulk("start")}>
            Starten
          </Button>
          <Button variant="secondary" onClick={() => void bulk("stop")}>
            Stoppen
          </Button>
          <Button variant="secondary" onClick={() => void bulk("restart")}>
            Herstarten
          </Button>
          <Button variant="secondary-danger" onClick={() => void bulk("remove")}>
            Verwijderen
          </Button>
          <button type="button" className="ml-auto text-xs underline" onClick={() => setSelected(new Set())}>
            Niets kiezen
          </button>
        </div>
      )}
      {groups.map((g) => {
        const groupRows = shown.filter((r) => g.hosts.some((h) => h.node_id === r.host.node_id));
        const groupErrors = errors.filter((e) => g.hosts.some((h) => h.node_id === e.host.node_id));
        const notReady = g.hosts.filter((h) => hostReady(h, need));
        if (groupRows.length === 0 && groupErrors.length === 0 && notReady.length === 0 && (q || filter)) return null;
        return (
          <section key={g.key || "los"} className="space-y-2">
            <h2 className="flex flex-wrap items-center gap-2 text-sm font-semibold tracking-wide text-slate-500 uppercase">
              {g.label}
              {g.environment && <Badge tone={g.environment === "prod" ? "red" : "slate"}>{envLabel[g.environment] ?? g.environment}</Badge>}
            </h2>
            {notReady.map((h) => (
              <p key={h.node_id} className="text-sm text-slate-500">
                {h.hostname}: {hostReady(h, need)}
              </p>
            ))}
            {groupErrors.map((e) => (
              <Alert key={e.host.node_id}>
                {e.host.hostname}: {e.error}
              </Alert>
            ))}
            {g.hosts
              .filter((h) => groupRows.some((r) => r.host.node_id === h.node_id))
              .map((h) => (
                <div key={h.node_id} className="overflow-x-auto rounded-lg border border-slate-200 bg-white dark:border-slate-800 dark:bg-slate-900">
                  <div className="flex items-center gap-2 border-b border-slate-100 px-3 py-2 text-sm font-medium dark:border-slate-800">
                    {h.hostname}
                    {h.vip_holder && <Badge tone="blue">VIP</Badge>}
                  </div>
                  <table className={tableClass}>
                    <thead>
                      <tr>
                        {isAdmin && <th className={cx(thClass, "w-8")} />}
                        <th className={thClass}>Naam</th>
                        <th className={thClass}>Status</th>
                        <th className={cx(thClass, "hidden lg:table-cell")}>Image</th>
                        <th className={cx(thClass, "hidden md:table-cell")}>Poorten</th>
                        <th className={cx(thClass, "text-right")}>CPU</th>
                        <th className={cx(thClass, "text-right")}>RAM</th>
                        {isAdmin && <th className={thClass} />}
                      </tr>
                    </thead>
                    <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
                      {groupRows
                        .filter((r) => r.host.node_id === h.node_id)
                        .sort((a, b) => a.project.localeCompare(b.project) || a.name.localeCompare(b.name))
                        .map((r) => {
                          const st = statOf.get(r.c.Id);
                          return (
                            <tr key={r.c.Id}>
                              {isAdmin && (
                                <td className={tdClass}>
                                  <input type="checkbox" aria-label={`${r.name} kiezen`} checked={selected.has(key(r))} onChange={() => toggle(r)} />
                                </td>
                              )}
                              <td className={tdClass}>
                                <Link
                                  href={`/docker/container?node=${r.host.node_id}&id=${r.c.Id}`}
                                  className="font-medium break-all hover:underline"
                                >
                                  {r.name}
                                </Link>
                                {r.project && (
                                  <div className="mt-0.5">
                                    {r.managed ? (
                                      <Link href={`/containers?cluster=${r.host.cluster_id}`} title="Beheerd door ClusterForge (Stacks)">
                                        <Badge tone="blue">stack {r.project}</Badge>
                                      </Link>
                                    ) : (
                                      <span className="text-xs text-slate-500">compose {r.project}</span>
                                    )}
                                  </div>
                                )}
                              </td>
                              <td className={tdClass}>
                                <Badge tone={stateTone(r.c.State, r.c.Status)}>
                                  {r.c.Status.includes("(unhealthy)") ? "unhealthy" : (stateLabel[r.c.State] ?? r.c.State)}
                                </Badge>
                                <div className="mt-0.5 text-xs text-slate-500">{r.c.Status}</div>
                              </td>
                              <td className={cx(tdClass, "hidden max-w-64 font-mono text-xs break-all lg:table-cell")}>{r.c.Image}</td>
                              <td className={cx(tdClass, "hidden text-xs md:table-cell")}>{portsText(r.c.Ports)}</td>
                              <td className={cx(tdClass, "text-right text-xs tabular-nums")}>{st ? `${st.cpu.toFixed(1)}%` : ""}</td>
                              <td className={cx(tdClass, "text-right text-xs whitespace-nowrap tabular-nums")}>{st ? formatBytes(st.mem_usage) : ""}</td>
                              {isAdmin && (
                                <td className={cx(tdClass, "text-right whitespace-nowrap")}>
                                  <RowActions r={r} run={run} />
                                </td>
                              )}
                            </tr>
                          );
                        })}
                    </tbody>
                  </table>
                </div>
              ))}
          </section>
        );
      })}
      {modal}
    </div>
  );
}

function RowActions({ r, run }: { r: Row; run: ReturnType<typeof useDockerRun>["run"] }) {
  const go = (verb: string) => void run(r.host.node_id, r.host.hostname, [{ method: "POST", path: cpath(r.c.Id, "/" + verb) }]);
  const btn = "rounded px-1.5 py-0.5 text-xs text-slate-600 hover:bg-slate-100 dark:text-slate-300 dark:hover:bg-slate-800";
  return (
    <span className="inline-flex gap-1">
      {r.c.State === "running" || r.c.State === "restarting" ? (
        <>
          <button type="button" className={btn} onClick={() => go("restart")}>
            Herstarten
          </button>
          {!r.managed && (
            <button type="button" className={btn} onClick={() => go("stop")}>
              Stoppen
            </button>
          )}
        </>
      ) : r.c.State === "paused" ? (
        <button type="button" className={btn} onClick={() => go("unpause")}>
          Hervatten
        </button>
      ) : (
        <button type="button" className={btn} onClick={() => go("start")}>
          Starten
        </button>
      )}
    </span>
  );
}
