"use client";

import Link from "next/link";
import { Suspense, useState } from "react";
import { errText, FilterChips, inScope, NodeSections, rowsFrom, ScopeBar, stateLabel, stateTone, useDockerAll, useDockerRun, useScope } from "@/components/docker/common";
import { tableClass, tdClass, thClass } from "@/components/inventory/bits";
import { Alert, Badge, PageHeader, cx } from "@/components/ui";
import { composeProject, containerName, cpath, useDockerHosts, type ContainerSummary, type DockerCall, type DockerHost } from "@/lib/docker";
import { useIsAdmin } from "@/lib/inventory";

export default function Page() {
  return (
    <Suspense>
      <Stacks />
    </Suspense>
  );
}

type Row = {
  host: DockerHost;
  project: string;
  containers: ContainerSummary[];
  running: number;
  dir: string;
  managed?: { id: string; name: string };
};

const filters = [
  { key: "", label: "Alle" },
  { key: "managed", label: "Beheerd" },
  { key: "free", label: "Niet beheerd" },
  { key: "down", label: "Niet alles draait" },
] as const;
type FilterKey = (typeof filters)[number]["key"];

// Een Compose-project is wat Docker zelf met labels bijhoudt; ook stacks die
// iemand met de hand startte, tellen mee.
function rowsOf(host: DockerHost, list: ContainerSummary[]): Row[] {
  const by = new Map<string, ContainerSummary[]>();
  for (const c of list) {
    const p = composeProject(c);
    if (!p) continue;
    by.set(p, [...(by.get(p) ?? []), c]);
  }
  return [...by.entries()].map(([project, cs]) => ({
    host,
    project,
    containers: cs.sort((a, b) => containerName(a).localeCompare(containerName(b))),
    running: cs.filter((c) => c.State === "running").length,
    dir: cs[0].Labels?.["com.docker.compose.project.working_dir"] ?? "",
    managed: host.stacks?.find((s) => s.name === project),
  }));
}

function Stacks() {
  const isAdmin = useIsAdmin();
  const hostsQ = useDockerHosts();
  const containersQ = useDockerAll<ContainerSummary[]>("/containers/json", { all: "1" }, 15_000);
  const [scope, setScope] = useScope();
  const [search, setSearch] = useState("");
  const [filter, setFilter] = useState<FilterKey>("");
  const { run, modal } = useDockerRun();
  const need = hostsQ.data?.protocol ?? 46;
  const hosts = (hostsQ.data?.hosts ?? []).filter((h) => inScope(h, scope));

  const { rows, errors } = rowsFrom(containersQ.data, hosts, rowsOf);
  const q = search.trim().toLowerCase();
  const test = (r: Row, f: FilterKey) =>
    f === "" || (f === "managed" && !!r.managed) || (f === "free" && !r.managed) || (f === "down" && r.running < r.containers.length);
  const shown = rows.filter(
    (r) => test(r, filter) && (!q || `${r.project} ${r.host.hostname} ${r.dir} ${r.containers.map(containerName).join(" ")}`.toLowerCase().includes(q)),
  );

  const act = (r: Row, what: "start" | "stop" | "restart") => {
    const pick = r.containers.filter((c) => (what === "start" ? c.State !== "running" : c.State === "running"));
    const calls: DockerCall[] = pick.map((c) => ({ method: "POST", path: cpath(c.Id, "/" + what) }));
    if (calls.length) void run(r.host.node_id, r.host.hostname, calls, { title: `Stack ${r.project}: ${{ start: "starten", stop: "stoppen", restart: "herstarten" }[what]}` });
  };

  return (
    <div className="space-y-5">
      <PageHeader
        title="Stacks"
        description="Alle Compose-projecten op elke node, ook wat iemand met de hand startte. Stacks die ClusterForge beheert, wijzig je in hun cluster; een project dat nog niet beheerd wordt, kun je daar overnemen."
      />
      <ScopeBar hosts={hostsQ.data?.hosts ?? []} scope={scope} setScope={setScope} search={search} setSearch={setSearch}>
        <FilterChips options={filters.map((f) => ({ ...f, count: rows.filter((r) => test(r, f.key)).length }))} value={filter} onChange={setFilter} />
      </ScopeBar>
      {containersQ.isError && <Alert>{errText(containersQ.error)}</Alert>}
      {containersQ.isLoading && <p className="text-sm text-slate-500">Laden…</p>}
      <NodeSections
        hosts={hosts}
        rows={shown}
        errors={errors}
        need={need}
        filtered={!!q || !!filter}
        header={(h) =>
          h.cluster_id && (
            <Link className="text-sm text-brand-600 hover:underline" href={`/containers?cluster=${h.cluster_id}`}>
              Stacks van {h.cluster} beheren →
            </Link>
          )
        }
      >
        {(h, rs) =>
          rs.length === 0 ? (
            <p className="px-3 py-3 text-sm text-slate-500">Geen Compose-projecten op deze node.</p>
          ) : (
            <table className={tableClass}>
              <thead>
                <tr>
                  <th className={thClass}>Stack</th>
                  <th className={thClass}>Containers</th>
                  <th className={cx(thClass, "hidden lg:table-cell")}>Map</th>
                  {isAdmin && <th className={thClass} />}
                </tr>
              </thead>
              <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
                {rs
                  .sort((a, b) => a.project.localeCompare(b.project))
                  .map((r) => (
                    <tr key={r.project} className="align-top">
                      <td className={cx(tdClass, "text-sm")}>
                        <div className="font-medium">{r.project}</div>
                        <div className="mt-1 flex flex-wrap items-center gap-1">
                          {r.managed ? <Badge tone="blue">beheerd</Badge> : <Badge>niet beheerd</Badge>}
                          <Badge tone={r.running === r.containers.length ? "green" : r.running === 0 ? "slate" : "amber"}>
                            {r.running}/{r.containers.length} draait
                          </Badge>
                        </div>
                      </td>
                      <td className={cx(tdClass, "text-xs")}>
                        <ul className="space-y-0.5">
                          {r.containers.map((c) => (
                            <li key={c.Id} className="flex flex-wrap items-center gap-2">
                              <Link className="text-brand-600 hover:underline" href={`/docker/container?node=${h.node_id}&id=${c.Id}`}>
                                {containerName(c)}
                              </Link>
                              <Badge tone={stateTone(c.State, c.Status)}>{stateLabel[c.State] ?? c.State}</Badge>
                              <span className="font-mono break-all text-slate-500">{c.Image}</span>
                            </li>
                          ))}
                        </ul>
                      </td>
                      <td className={cx(tdClass, "hidden font-mono text-xs break-all text-slate-500 lg:table-cell")}>{r.dir || "–"}</td>
                      {isAdmin && (
                        <td className={cx(tdClass, "text-right text-xs whitespace-nowrap")}>
                          {r.managed ? (
                            <Link className="rounded px-1.5 py-0.5 text-brand-600 hover:bg-slate-100 dark:hover:bg-slate-800" href={`/containers?cluster=${h.cluster_id}`}>
                              Beheren
                            </Link>
                          ) : (
                            <>
                              {r.running < r.containers.length && <ActBtn onClick={() => act(r, "start")}>Starten</ActBtn>}
                              {r.running > 0 && <ActBtn onClick={() => act(r, "restart")}>Herstarten</ActBtn>}
                              {r.running > 0 && <ActBtn onClick={() => act(r, "stop")}>Stoppen</ActBtn>}
                              {h.cluster_id && (
                                <Link className="rounded px-1.5 py-0.5 text-brand-600 hover:bg-slate-100 dark:hover:bg-slate-800" href={`/containers?cluster=${h.cluster_id}`}>
                                  Overnemen
                                </Link>
                              )}
                            </>
                          )}
                        </td>
                      )}
                    </tr>
                  ))}
              </tbody>
            </table>
          )
        }
      </NodeSections>
      {modal}
    </div>
  );
}

function ActBtn({ onClick, children }: { onClick: () => void; children: React.ReactNode }) {
  return (
    <button type="button" className="rounded px-1.5 py-0.5 text-slate-600 hover:bg-slate-100 dark:text-slate-300 dark:hover:bg-slate-800" onClick={onClick}>
      {children}
    </button>
  );
}
