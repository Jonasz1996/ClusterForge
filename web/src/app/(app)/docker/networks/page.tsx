"use client";

import { Suspense, useState } from "react";
import { Modal } from "@/components/Modal";
import { errText, FilterChips, inScope, NodeSections, rowsFrom, ScopeBar, useDockerAll, useDockerRun, useScope } from "@/components/docker/common";
import { tableClass, tdClass, thClass } from "@/components/inventory/bits";
import { Alert, Badge, Button, Field, Input, PageHeader, Select, cx } from "@/components/ui";
import { builtinNetworks, composeProject, containerName, shortId, useDockerHosts, type ContainerSummary, type DockerHost, type NetworkSummary } from "@/lib/docker";
import { useIsAdmin } from "@/lib/inventory";

export default function Page() {
  return (
    <Suspense>
      <Networks />
    </Suspense>
  );
}

type Member = { id: string; name: string; ip: string };
type Row = { host: DockerHost; n: NetworkSummary; members: Member[] };

const filters = [
  { key: "", label: "Alle" },
  { key: "own", label: "Eigen" },
  { key: "empty", label: "Leeg" },
] as const;
type FilterKey = (typeof filters)[number]["key"];

// /networks geeft de leden niet mee; die komen uit de containers.
function rowsOf(host: DockerHost, list: NetworkSummary[], containers: ContainerSummary[]): Row[] {
  return list.map((n) => ({
    host,
    n,
    members: containers.flatMap((c) =>
      Object.entries(c.NetworkSettings?.Networks ?? {})
        .filter(([name, x]) => (x.NetworkID ? x.NetworkID === n.Id : name === n.Name))
        .map(([, x]) => ({ id: c.Id, name: containerName(c), ip: x.IPAddress ?? "" })),
    ),
  }));
}

function Networks() {
  const isAdmin = useIsAdmin();
  const hostsQ = useDockerHosts();
  const netsQ = useDockerAll<NetworkSummary[]>("/networks", undefined, 30_000);
  const containersQ = useDockerAll<ContainerSummary[]>("/containers/json", { all: "1" }, 30_000);
  const [scope, setScope] = useScope();
  const [search, setSearch] = useState("");
  const [filter, setFilter] = useState<FilterKey>("");
  const [createFor, setCreateFor] = useState<DockerHost | null>(null);
  const [connect, setConnect] = useState<Row | null>(null);
  const { run, modal } = useDockerRun();
  const need = hostsQ.data?.protocol ?? 46;
  const hosts = (hostsQ.data?.hosts ?? []).filter((h) => inScope(h, scope));

  const containersBy = new Map((containersQ.data ?? []).map((r) => [r.node_id, r.data ?? []]));
  const { rows, errors } = rowsFrom(netsQ.data, hosts, (h, list) => rowsOf(h, list, containersBy.get(h.node_id) ?? []));
  const q = search.trim().toLowerCase();
  const test = (r: Row, f: FilterKey) => f === "" || (f === "own" && !builtinNetworks.has(r.n.Name)) || (f === "empty" && r.members.length === 0 && !builtinNetworks.has(r.n.Name));
  const shown = rows.filter(
    (r) => test(r, filter) && (!q || `${r.n.Name} ${r.n.Driver} ${subnets(r.n)} ${r.host.hostname} ${r.members.map((m) => m.name).join(" ")}`.toLowerCase().includes(q)),
  );

  return (
    <div className="space-y-5">
      <PageHeader
        title="Netwerken"
        description="De Docker-netwerken op elke node, met hun subnet en welke containers erop hangen. bridge, host en none zijn van Docker zelf en blijven staan."
      />
      <ScopeBar hosts={hostsQ.data?.hosts ?? []} scope={scope} setScope={setScope} search={search} setSearch={setSearch}>
        <FilterChips options={filters.map((f) => ({ ...f, count: rows.filter((r) => test(r, f.key)).length }))} value={filter} onChange={setFilter} />
      </ScopeBar>
      {(netsQ.isError || containersQ.isError) && <Alert>{errText(netsQ.error ?? containersQ.error)}</Alert>}
      {netsQ.isLoading && <p className="text-sm text-slate-500">Laden…</p>}
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
                Netwerk maken
              </Button>
              {rows.some((r) => r.host.node_id === h.node_id && r.members.length === 0 && !builtinNetworks.has(r.n.Name)) && (
                <Button
                  variant="secondary-danger"
                  onClick={() =>
                    void run(h.node_id, h.hostname, [{ method: "POST", path: "/networks/prune" }], {
                      what: `Alle eigen netwerken zonder containers van ${h.hostname} verwijderen.`,
                    })
                  }
                >
                  Lege weg
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
                <th className={thClass}>Netwerk</th>
                <th className={cx(thClass, "hidden sm:table-cell")}>Driver</th>
                <th className={thClass}>Subnet</th>
                <th className={thClass}>Containers</th>
                {isAdmin && <th className={thClass} />}
              </tr>
            </thead>
            <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
              {rs
                .sort((a, b) => Number(builtinNetworks.has(a.n.Name)) - Number(builtinNetworks.has(b.n.Name)) || a.n.Name.localeCompare(b.n.Name))
                .map((r) => {
                  const builtin = builtinNetworks.has(r.n.Name);
                  return (
                    <tr key={r.n.Id}>
                      <td className={cx(tdClass, "font-mono text-xs break-all")}>
                        {r.n.Name}
                        {builtin && (
                          <span className="ml-2">
                            <Badge>Docker</Badge>
                          </span>
                        )}
                        {r.n.Internal && (
                          <span className="ml-2">
                            <Badge tone="blue">intern</Badge>
                          </span>
                        )}
                        <div className="mt-0.5 text-slate-500">
                          {shortId(r.n.Id)}
                          {composeProject(r.n) && <> · stack {composeProject(r.n)}</>}
                        </div>
                      </td>
                      <td className={cx(tdClass, "hidden text-xs sm:table-cell")}>{r.n.Driver}</td>
                      <td className={cx(tdClass, "font-mono text-xs")}>{subnets(r.n) || "–"}</td>
                      <td className={cx(tdClass, "text-xs")}>
                        {r.members.length === 0 && <span className="text-slate-500">geen</span>}
                        <ul className="space-y-0.5">
                          {r.members.map((m) => (
                            <li key={m.id} className="flex flex-wrap items-center gap-x-2">
                              <span>{m.name}</span>
                              {m.ip && <span className="font-mono text-slate-500">{m.ip}</span>}
                              {isAdmin && !builtin && (
                                <button
                                  type="button"
                                  className="rounded px-1 text-slate-500 hover:bg-slate-100 hover:text-red-600 dark:hover:bg-slate-800"
                                  title={`${m.name} loskoppelen`}
                                  onClick={() =>
                                    void run(h.node_id, h.hostname, [{ method: "POST", path: `/networks/${r.n.Id}/disconnect`, body: { Container: m.id } }])
                                  }
                                >
                                  loskoppelen
                                </button>
                              )}
                            </li>
                          ))}
                        </ul>
                      </td>
                      {isAdmin && (
                        <td className={cx(tdClass, "text-right whitespace-nowrap")}>
                          {!builtin && (
                            <button
                              type="button"
                              className="rounded px-1.5 py-0.5 text-xs text-slate-600 hover:bg-slate-100 dark:text-slate-300 dark:hover:bg-slate-800"
                              onClick={() => setConnect(r)}
                            >
                              Container koppelen
                            </button>
                          )}
                          {!builtin && r.members.length === 0 && (
                            <button
                              type="button"
                              className="rounded px-1.5 py-0.5 text-xs text-red-600 hover:bg-red-50 dark:text-red-400 dark:hover:bg-slate-800"
                              onClick={() =>
                                void run(h.node_id, h.hostname, [{ method: "DELETE", path: `/networks/${encodeURIComponent(r.n.Name)}` }], {
                                  what: `Netwerk ${r.n.Name} verwijderen van ${h.hostname}.`,
                                })
                              }
                            >
                              Verwijderen
                            </button>
                          )}
                        </td>
                      )}
                    </tr>
                  );
                })}
            </tbody>
          </table>
        )}
      </NodeSections>
      {createFor && <CreateNetwork host={createFor} onClose={() => setCreateFor(null)} run={run} />}
      {connect && (
        <ConnectModal row={connect} containers={containersBy.get(connect.host.node_id) ?? []} onClose={() => setConnect(null)} run={run} />
      )}
      {modal}
    </div>
  );
}

function subnets(n: NetworkSummary) {
  return (n.IPAM?.Config ?? [])
    .map((c) => c.Subnet)
    .filter(Boolean)
    .join(", ");
}

type Run = ReturnType<typeof useDockerRun>["run"];

function CreateNetwork({ host, onClose, run }: { host: DockerHost; onClose: () => void; run: Run }) {
  const [name, setName] = useState("");
  const [driver, setDriver] = useState("bridge");
  const [subnet, setSubnet] = useState("");
  const [gateway, setGateway] = useState("");
  const [internal, setInternal] = useState(false);
  const cidr = /^\d{1,3}(\.\d{1,3}){3}\/\d{1,2}$/;
  const ip = /^\d{1,3}(\.\d{1,3}){3}$/;
  const valid =
    /^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$/.test(name.trim()) &&
    !builtinNetworks.has(name.trim()) &&
    (!subnet.trim() || cidr.test(subnet.trim())) &&
    (!gateway.trim() || (ip.test(gateway.trim()) && !!subnet.trim()));
  return (
    <Modal title={`Netwerk maken op ${host.hostname}`} onClose={onClose}>
      <form
        className="space-y-4"
        onSubmit={async (e) => {
          e.preventDefault();
          if (!valid) return;
          const body: Record<string, unknown> = { Name: name.trim(), Driver: driver, Internal: internal, CheckDuplicate: true };
          if (subnet.trim()) body.IPAM = { Driver: "default", Config: [{ Subnet: subnet.trim(), ...(gateway.trim() ? { Gateway: gateway.trim() } : {}) }] };
          if (await run(host.node_id, host.hostname, [{ method: "POST", path: "/networks/create", body }])) onClose();
        }}
      >
        <Field label="Naam" htmlFor="nname">
          <Input id="nname" autoFocus value={name} onChange={(e) => setName(e.target.value)} placeholder="app_net" />
        </Field>
        <Field label="Driver" htmlFor="ndriver">
          <Select id="ndriver" value={driver} onChange={(e) => setDriver(e.target.value)}>
            <option value="bridge">bridge</option>
            <option value="macvlan">macvlan</option>
            <option value="ipvlan">ipvlan</option>
          </Select>
        </Field>
        <Field label="Subnet (optioneel)" htmlFor="nsubnet" hint="Leeg: Docker kiest er zelf een. Kies geen subnet dat al in je VLAN's zit.">
          <Input id="nsubnet" value={subnet} onChange={(e) => setSubnet(e.target.value)} placeholder="172.30.0.0/24" />
        </Field>
        <Field label="Gateway (optioneel)" htmlFor="ngw">
          <Input id="ngw" value={gateway} onChange={(e) => setGateway(e.target.value)} placeholder="172.30.0.1" disabled={!subnet.trim()} />
        </Field>
        <label className="flex items-center gap-2 text-sm">
          <input type="checkbox" checked={internal} onChange={(e) => setInternal(e.target.checked)} />
          Intern: containers op dit netwerk kunnen niet naar buiten
        </label>
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

function ConnectModal({ row, containers, onClose, run }: { row: Row; containers: ContainerSummary[]; onClose: () => void; run: Run }) {
  const free = containers.filter((c) => !row.members.some((m) => m.id === c.Id)).sort((a, b) => containerName(a).localeCompare(containerName(b)));
  const [id, setId] = useState(free[0]?.Id ?? "");
  return (
    <Modal title={`Container koppelen aan ${row.n.Name}`} onClose={onClose}>
      <form
        className="space-y-4"
        onSubmit={async (e) => {
          e.preventDefault();
          if (id && (await run(row.host.node_id, row.host.hostname, [{ method: "POST", path: `/networks/${row.n.Id}/connect`, body: { Container: id } }]))) onClose();
        }}
      >
        {free.length === 0 ? (
          <p className="text-sm text-slate-500">Alle containers op {row.host.hostname} hangen al aan dit netwerk.</p>
        ) : (
          <Field label="Container" htmlFor="ccont" hint="Een koppeling van hier verdwijnt als de stack opnieuw wordt uitgerold; zet hem in de compose om hem te houden.">
            <Select id="ccont" value={id} onChange={(e) => setId(e.target.value)}>
              {free.map((c) => (
                <option key={c.Id} value={c.Id}>
                  {containerName(c)} ({c.State})
                </option>
              ))}
            </Select>
          </Field>
        )}
        <div className="flex justify-end gap-2">
          <Button type="button" variant="secondary" onClick={onClose}>
            Annuleren
          </Button>
          <Button type="submit" disabled={!id}>
            Koppelen
          </Button>
        </div>
      </form>
    </Modal>
  );
}
