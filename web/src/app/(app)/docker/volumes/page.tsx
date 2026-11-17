"use client";

import { Suspense, useState } from "react";
import { Modal } from "@/components/Modal";
import { errText, FilterChips, inScope, NodeSections, rowsFrom, ScopeBar, useDockerAll, useDockerRun, useScope } from "@/components/docker/common";
import { ago, formatBytes, tableClass, tdClass, thClass } from "@/components/inventory/bits";
import { Alert, Badge, Button, Field, Input, PageHeader, Select, cx } from "@/components/ui";
import { composeProject, containerName, useDockerHosts, type ContainerSummary, type DockerHost, type SystemDF, type VolumeSummary } from "@/lib/docker";
import { useIsAdmin } from "@/lib/inventory";

export default function Page() {
  return (
    <Suspense>
      <Volumes />
    </Suspense>
  );
}

type Row = { host: DockerHost; v: VolumeSummary; size?: number; used: string[] };

const filters = [
  { key: "", label: "Alle" },
  { key: "used", label: "In gebruik" },
  { key: "unused", label: "Ongebruikt" },
] as const;
type FilterKey = (typeof filters)[number]["key"];

// De grootte komt uit /system/df: /volumes geeft die niet.
function rowsOf(host: DockerHost, df: SystemDF, containers: ContainerSummary[]): Row[] {
  return (df.Volumes ?? []).map((v) => ({
    host,
    v,
    size: v.UsageData && v.UsageData.Size >= 0 ? v.UsageData.Size : undefined,
    used: containers.filter((c) => (c.Mounts ?? []).some((m) => m.Type === "volume" && m.Name === v.Name)).map(containerName),
  }));
}

function Volumes() {
  const isAdmin = useIsAdmin();
  const hostsQ = useDockerHosts();
  const dfQ = useDockerAll<SystemDF>("/system/df", undefined, 60_000);
  const containersQ = useDockerAll<ContainerSummary[]>("/containers/json", { all: "1" }, 30_000);
  const [scope, setScope] = useScope();
  const [search, setSearch] = useState("");
  const [filter, setFilter] = useState<FilterKey>("");
  const [createFor, setCreateFor] = useState<DockerHost | null>(null);
  const { run, modal } = useDockerRun();
  const need = hostsQ.data?.protocol ?? 46;
  const hosts = (hostsQ.data?.hosts ?? []).filter((h) => inScope(h, scope));

  const containersBy = new Map((containersQ.data ?? []).map((r) => [r.node_id, r.data ?? []]));
  const { rows, errors } = rowsFrom(dfQ.data, hosts, (h, df) => rowsOf(h, df, containersBy.get(h.node_id) ?? []));
  const q = search.trim().toLowerCase();
  const test = (r: Row, f: FilterKey) => f === "" || (f === "used" && r.used.length > 0) || (f === "unused" && r.used.length === 0);
  const shown = rows.filter((r) => test(r, filter) && (!q || `${r.v.Name} ${r.host.hostname} ${composeProject(r.v)} ${r.used.join(" ")}`.toLowerCase().includes(q)));

  return (
    <div className="space-y-5">
      <PageHeader
        title="Volumes"
        description="De volumes op elke node, met hun grootte en welke containers ze gebruiken. Een volume in gebruik kan niet weg; ongebruikte volumes opruimen wist hun gegevens voorgoed."
      />
      <ScopeBar hosts={hostsQ.data?.hosts ?? []} scope={scope} setScope={setScope} search={search} setSearch={setSearch}>
        <FilterChips options={filters.map((f) => ({ ...f, count: rows.filter((r) => test(r, f.key)).length }))} value={filter} onChange={setFilter} />
      </ScopeBar>
      {(dfQ.isError || containersQ.isError) && <Alert>{errText(dfQ.error ?? containersQ.error)}</Alert>}
      {dfQ.isLoading && <p className="text-sm text-slate-500">Laden…</p>}
      <NodeSections
        hosts={hosts}
        rows={shown}
        errors={errors}
        need={need}
        filtered={!!q || !!filter}
        header={(h) =>
          isAdmin && (
            <>
              <Button variant="secondary" onClick={() => setCreateFor(h)}>
                Volume maken
              </Button>
              {rows.some((r) => r.host.node_id === h.node_id && r.used.length === 0) && (
                <Button
                  variant="secondary-danger"
                  onClick={() =>
                    void run(h.node_id, h.hostname, [{ method: "POST", path: "/volumes/prune", query: { filters: JSON.stringify({ all: ["true"] }) } }], {
                      what: `Alle volumes die geen container gebruikt van ${h.hostname} verwijderen, met hun gegevens.`,
                    })
                  }
                >
                  Ongebruikte weg
                </Button>
              )}
            </>
          )
        }
      >
        {(h, rs) => (
          <table className={tableClass}>
            <thead>
              <tr>
                <th className={thClass}>Volume</th>
                <th className={cx(thClass, "hidden md:table-cell")}>Stack</th>
                <th className={cx(thClass, "text-right")}>Grootte</th>
                <th className={cx(thClass, "hidden md:table-cell")}>Gemaakt</th>
                <th className={thClass}>Gebruikt door</th>
                {isAdmin && <th className={thClass} />}
              </tr>
            </thead>
            <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
              {rs
                .sort((a, b) => a.v.Name.localeCompare(b.v.Name))
                .map((r) => (
                  <tr key={r.v.Name}>
                    <td className={cx(tdClass, "font-mono text-xs break-all")}>
                      {r.v.Name}
                      {r.v.Driver !== "local" && (
                        <span className="ml-2">
                          <Badge>{r.v.Driver}</Badge>
                        </span>
                      )}
                      <div className="mt-0.5 hidden text-slate-500 lg:block">{r.v.Mountpoint}</div>
                    </td>
                    <td className={cx(tdClass, "hidden text-xs md:table-cell")}>{composeProject(r.v) || <span className="text-slate-400">–</span>}</td>
                    <td className={cx(tdClass, "text-right text-xs whitespace-nowrap tabular-nums")}>{r.size !== undefined ? formatBytes(r.size) : "–"}</td>
                    <td className={cx(tdClass, "hidden text-xs md:table-cell")}>{r.v.CreatedAt ? ago(r.v.CreatedAt) : "–"}</td>
                    <td className={cx(tdClass, "text-xs")}>{r.used.length ? r.used.join(", ") : <Badge>ongebruikt</Badge>}</td>
                    {isAdmin && (
                      <td className={cx(tdClass, "text-right whitespace-nowrap")}>
                        {r.used.length === 0 && (
                          <button
                            type="button"
                            className="rounded px-1.5 py-0.5 text-xs text-red-600 hover:bg-red-50 dark:text-red-400 dark:hover:bg-slate-800"
                            onClick={() =>
                              void run(h.node_id, h.hostname, [{ method: "DELETE", path: `/volumes/${encodeURIComponent(r.v.Name)}` }], {
                                what: `Volume ${r.v.Name} met al zijn gegevens verwijderen van ${h.hostname}.`,
                              })
                            }
                          >
                            Verwijderen
                          </button>
                        )}
                      </td>
                    )}
                  </tr>
                ))}
            </tbody>
          </table>
        )}
      </NodeSections>
      {createFor && <CreateVolume host={createFor} onClose={() => setCreateFor(null)} run={run} />}
      {modal}
    </div>
  );
}

function CreateVolume({ host, onClose, run }: { host: DockerHost; onClose: () => void; run: ReturnType<typeof useDockerRun>["run"] }) {
  const [name, setName] = useState("");
  const [driver, setDriver] = useState("local");
  const valid = /^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$/.test(name.trim());
  return (
    <Modal title={`Volume maken op ${host.hostname}`} onClose={onClose}>
      <form
        className="space-y-4"
        onSubmit={async (e) => {
          e.preventDefault();
          if (valid && (await run(host.node_id, host.hostname, [{ method: "POST", path: "/volumes/create", body: { Name: name.trim(), Driver: driver } }]))) onClose();
        }}
      >
        <Field label="Naam" htmlFor="vname" hint="Letters, cijfers, punt, streep en underscore.">
          <Input id="vname" autoFocus value={name} onChange={(e) => setName(e.target.value)} placeholder="app_data" />
        </Field>
        <Field label="Driver" htmlFor="vdriver">
          <Select id="vdriver" value={driver} onChange={(e) => setDriver(e.target.value)}>
            <option value="local">local</option>
          </Select>
        </Field>
        <div className="flex justify-end gap-2">
          <Button type="button" variant="secondary" onClick={onClose}>
            Annuleren
          </Button>
          <Button type="submit" disabled={!valid}>
            Maken
          </Button>
        </div>
      </form>
    </Modal>
  );
}
