"use client";

import { SectionNav } from "@/components/shell/SectionNav";
import { Tile, TileGrid } from "@/components/Tiles";
import { Tabs } from "@/components/Tabs";
import { NodeProblems, nodeTabs, tabOf, type NodeTab } from "@/components/inventory/NodeProblems";
import { NodeZabbixTile } from "@/components/zabbix/NodeZabbix";
import { askConfirm } from "@/components/Confirm";
import Link from "next/link";
import { useRouter, useSearchParams } from "next/navigation";
import { Suspense, useState, type ReactNode } from "react";
import { HistoryCard } from "@/components/audit/AuditList";
import { NodeBackupsCard } from "@/components/backups/Backups";
import { NodeConnectionsCard } from "@/components/deps/NodeConnectionsCard";
import { NodeDriftCard } from "@/components/drift/Drift";
import { MetricsPanels } from "@/components/charts/MetricsPanels";
import { JobList } from "@/components/jobs/JobList";
import { ConfirmDelete } from "@/components/inventory/ConfirmDelete";
import { AgentUpgradeRow } from "@/components/inventory/AgentUpgrades";
import { InstallAgent } from "@/components/inventory/InstallAgent";
import { NodeForm } from "@/components/inventory/NodeForm";
import { NodeLifecycleCard } from "@/components/inventory/NodeLifecycleCard";
import { NodeProxmoxCard } from "@/components/proxmox/NodeProxmoxCard";
import { NodeChangesCard } from "@/components/journal/Changes";
import { NodeCronCard } from "@/components/cron/Cron";
import { CrowdSecNodeCard } from "@/components/crowdsec/CrowdSecNodeCard";
import { NodeDiskCard } from "@/components/disk/NodeDiskCard";
import { FirewallNodeCard } from "@/components/firewall/FirewallNodeView";
import { SeaweedCard } from "@/components/seaweed/SeaweedCard";
import {
  AgentBadge,
  ago,
  formatBytes,
  formatDuration,
  LifecycleBadge,
  GrafanaButton,
  QueryState,
  StatusBadge,
  StatusNote,
  Tags,
  tableClass,
  tdClass,
  thClass,
} from "@/components/inventory/bits";
import { AskButton } from "@/components/assistant/AskButton";
import { Alert, Badge, Button, Card, PageHeader } from "@/components/ui";
import {
  grafanaLink,
  useDeleteNode,
  useInfo,
  useIsAdmin,
  useNode,
  useNodeFacts,
  useRevokeAgent,
  useUpdateNode,
  type Facts,
  type Node,
  type NodeRuntime,
} from "@/lib/inventory";
import { useNodeAction } from "@/lib/lifecycle";
import { useJobs } from "@/lib/proxmox";

export default function NodeDetailPage() {
  return (
    <Suspense>
      <NodeDetailInner />
    </Suspense>
  );
}

const fmt = new Intl.DateTimeFormat("nl-BE", { dateStyle: "medium", timeStyle: "short" });
const none = <span className="text-slate-400">Geen</span>;

function NodeDetailInner() {
  const id = useSearchParams().get("id") ?? "";
  const node = useNode(id);
  const runtime = useNodeFacts(id);
  const info = useInfo();
  const isAdmin = useIsAdmin();
  const update = useUpdateNode();
  const remove = useDeleteNode();
  const router = useRouter();
  const [editing, setEditing] = useState(false);
  const [deleting, setDeleting] = useState(false);
  const jobs = useJobs({ node_id: id, limit: 10 }, id !== "");
  const params = useSearchParams();
  const tab = tabOf(params.get("tab"));
  const setTab = (t: NodeTab) => {
    const q = new URLSearchParams(params.toString());
    if (t === "status") q.delete("tab");
    else q.set("tab", t);
    router.replace(`/nodes/detail?${q.toString()}`, { scroll: false });
  };

  if (id === "") return <Alert>Geen node opgegeven.</Alert>;

  return (
    <QueryState q={node}>
      {node.data && (
        <div className="space-y-6">
          <div className="text-sm">
            <Link href="/nodes" className="text-slate-500 hover:underline">
              ← Nodes
            </Link>
          </div>
          <PageHeader
            title={node.data.hostname}
            description={
              <span className="inline-flex flex-wrap items-center gap-2">
                <StatusBadge status={node.data.status} reason={node.data.status_reason} />
                <LifecycleBadge lifecycle={node.data.lifecycle} />
                {node.data.cluster_id ? (
                  <Link href={`/clusters/detail?id=${node.data.cluster_id}`} className="hover:underline">
                    {node.data.cluster_name}
                  </Link>
                ) : (
                  <span>Zonder cluster</span>
                )}
              </span>
            }
            actions={
              !editing && (
                <>
                  <AskButton subject="node" id={id} />
                  {isAdmin && (
                    <>
                      <Link
                        href={`/logs?node=${id}&kind=file&name=${encodeURIComponent("/var/log/syslog")}`}
                        className="inline-flex items-center rounded-md border border-slate-300 px-3.5 py-2 text-sm font-medium hover:bg-slate-50 dark:border-slate-700 dark:hover:bg-slate-800"
                      >
                        Logs
                      </Link>
                      <Button variant="secondary" onClick={() => setEditing(true)}>
                        Bewerken
                      </Button>
                      <Button variant="danger" disabled={remove.isPending} onClick={() => setDeleting(true)}>
                        Verwijderen
                      </Button>
                    </>
                  )}
                </>
              )
            }
          />
          {/* Fase 47: bovenaan status, problemen en de acties die je het
              vaakst nodig hebt; de rest in vier tabbladen. */}
          <NodeProblems node={node.data} isAdmin={isAdmin} />
          <div className="flex flex-wrap items-end justify-between gap-3">
            <Tabs
              className="min-w-0 flex-1"
              value={tab}
              onChange={setTab}
              label="Onderdelen van de node"
              tabs={nodeTabs.map((t) => ({ key: t.key, label: t.label }))}
            />
            <SectionNav views />
          </div>

          {deleting && (
            <ConfirmDelete
              title={`Node ${node.data.hostname} verwijderen`}
              text={
                node.data.cluster_id
                  ? "Een agent op deze node wordt ook ingetrokken. De diensten van het cluster blijven bestaan."
                  : "Een agent op deze node wordt ook ingetrokken, en de diensten op deze node verdwijnen."
              }
              nodeId={node.data.cluster_id ? undefined : id}
              what="de node"
              pending={remove.isPending}
              error={remove.error}
              onClose={() => setDeleting(false)}
              onConfirm={() => remove.mutate(id, { onSuccess: () => router.push("/nodes") })}
            />
          )}

          <StatusNote status={node.data.status} reason={node.data.status_reason} since={node.data.status_since} />

          {editing && (
            <Card title="Node bewerken">
              <NodeForm
                initial={node.data}
                submitLabel="Opslaan"
                onCancel={() => setEditing(false)}
                onSubmit={async (v) => {
                  await update.mutateAsync({ id, body: v });
                  setEditing(false);
                }}
              />
            </Card>
          )}

          {tab === "status" && (
            <TileGrid>
            {!editing && (
              <Tile>
                <Card title="Inventory">
                  <dl className={dlClass}>
                    <dt className={dtClass}>Primair IP-adres</dt>
                    <dd className="font-mono text-xs">{node.data.primary_ip ?? none}</dd>
                    <dt className={dtClass}>Rol</dt>
                    <dd>{node.data.role || none}</dd>
                    <dt className={dtClass}>Beschrijving</dt>
                    <dd>{node.data.description || none}</dd>
                    <dt className={dtClass}>Tags</dt>
                    <dd>{node.data.tags.length ? <Tags tags={node.data.tags} /> : none}</dd>
                    <dt className={dtClass}>Laatst gewijzigd</dt>
                    <dd>{fmt.format(new Date(node.data.updated_at))}</dd>
                  </dl>
                </Card>
              </Tile>
            )}
            <Tile>
              <AgentCard node={node.data} runtime={runtime.data} isAdmin={isAdmin} />
            </Tile>
            {node.data.agent && (
              <Tile>
                <NodeDiskCard nodeId={id} isAdmin={isAdmin} />
              </Tile>
            )}
            {runtime.data?.facts && <FactsView facts={runtime.data.facts} runtime={runtime.data} part="opslag" />}
            <Tile>
              <NodeLifecycleCard node={node.data} runtime={runtime.data} jobs={jobs.data} isAdmin={isAdmin} />
            </Tile>
            {node.data.cluster_id && (
              <Tile>
                <NodeDriftCard nodeId={id} clusterId={node.data.cluster_id} isAdmin={isAdmin} />
              </Tile>
            )}
            {node.data.agent && (
              <Tile>
                <CrowdSecNodeCard nodeId={id} isAdmin={isAdmin} />
              </Tile>
            )}
            {node.data.agent && (
              <Tile>
                <FirewallNodeCard nodeId={id} isAdmin={isAdmin} />
              </Tile>
            )}
            <NodeZabbixTile nodeId={id} />
            {node.data.agent && (
              <Tile wide tall>
                <MetricsPanels
                  kind="node"
                  id={id}
                  actions={<GrafanaButton href={grafanaLink(info.data?.grafana_node_url ?? "", nodeLinkValues(node.data))} />}
                />
              </Tile>
            )}
            </TileGrid>
          )}

          {tab === "software" && (
            <TileGrid>
            {runtime.data?.facts && <FactsView facts={runtime.data.facts} runtime={runtime.data} part="software" />}
            <Tile>
              <NodeProxmoxCard node={node.data} isAdmin={isAdmin} />
            </Tile>
            {node.data.proxmox && (
              <Tile>
                <NodeBackupsCard nodeId={id} isAdmin={isAdmin} />
              </Tile>
            )}
            {node.data.agent && (
              <Tile>
                <NodeCronCard nodeId={id} />
              </Tile>
            )}
            {!runtime.data?.facts && (
              <Tile>
                <Card title="Software">
                  <p className="text-sm text-slate-500">Nog geen facts van de agent: het besturingssysteem, de updates en de services verschijnen hier zodra de agent ze stuurt.</p>
                </Card>
              </Tile>
            )}
            </TileGrid>
          )}

          {tab === "netwerk" && (
            <TileGrid>
            {runtime.data?.facts && <FactsView facts={runtime.data.facts} runtime={runtime.data} part="netwerk" />}
            {isAdmin && node.data.agent && (
              <Tile>
                <NodeConnectionsCard nodeId={id} />
              </Tile>
            )}
            {!runtime.data?.facts && !(isAdmin && node.data.agent) && (
              <Tile>
                <Card title="Netwerk">
                  <p className="text-sm text-slate-500">Nog geen netwerkgegevens van de agent.</p>
                </Card>
              </Tile>
            )}
            </TileGrid>
          )}

          {tab === "geschiedenis" && (
            <TileGrid>
            {(jobs.data?.length ?? 0) > 0 && (
              <Tile wide>
                <Card title="Taken">
                  <JobList jobs={jobs.data!} />
                </Card>
              </Tile>
            )}
            {node.data.agent && (
              <Tile wide>
                <NodeChangesCard nodeId={id} />
              </Tile>
            )}
            <Tile wide>
              <HistoryCard filter={{ node: id }} isAdmin={isAdmin} />
            </Tile>
            </TileGrid>
          )}
        </div>
      )}
    </QueryState>
  );
}

const dlClass = "grid grid-cols-[9rem_1fr] gap-x-4 gap-y-2.5 text-sm";
const dtClass = "text-slate-500";

function AgentCard({ node, runtime, isAdmin }: { node: Node; runtime?: NodeRuntime; isAdmin: boolean }) {
  const revoke = useRevokeAgent();
  const refresh = useNodeAction();
  const [installing, setInstalling] = useState(false);
  const [refreshed, setRefreshed] = useState<string | null>(null);
  const a = node.agent;

  if (!a) {
    return (
      <Card title="Agent">
        {installing ? (
          <InstallAgent nodeId={node.id} onClose={() => setInstalling(false)} />
        ) : (
          <div className="space-y-3 text-sm">
            <p className="text-slate-600 dark:text-slate-400">
              Op deze node draait nog geen agent. Met de agent zie je hier het besturingssysteem, de services, updates en wie de
              VIP heeft.
            </p>
            {isAdmin && <Button onClick={() => setInstalling(true)}>Agent installeren</Button>}
          </div>
        )}
      </Card>
    );
  }

  const hb = runtime?.heartbeat;
  return (
    <Card title="Agent">
      <dl className={dlClass}>
        <dt className={dtClass}>Verbinding</dt>
        <dd>
          <AgentBadge agent={a} /> <span className="text-slate-500">laatste heartbeat {ago(a.last_seen_at)}</span>
        </dd>
        <dt className={dtClass}>Versie</dt>
        <dd>{a.version || "onbekend"}</dd>
        <dt className={dtClass}>Aangemeld</dt>
        <dd>{fmt.format(new Date(a.enrolled_at))}</dd>
        {hb && (
          <>
            <dt className={dtClass}>Uptime</dt>
            <dd>{formatDuration(hb.uptime_seconds)}</dd>
            <dt className={dtClass}>Load</dt>
            <dd className="tabular-nums">{hb.load.map((l) => l.toFixed(2)).join(" · ")}</dd>
          </>
        )}
      </dl>
      <AgentUpgradeRow nodeId={node.id} isAdmin={isAdmin} />
      {isAdmin && (
        <div className="mt-4 flex flex-wrap items-center gap-2">
          {a.commands && a.connection !== "offline" && (
            <Button
              variant="secondary"
              disabled={refresh.isPending}
              onClick={() =>
                refresh.mutate(
                  { nodeId: node.id, body: { action: "refresh_facts" } },
                  {
                    onSuccess: () => setRefreshed("Gevraagd; de facts komen zo binnen."),
                    onError: (e) => setRefreshed(e.message),
                  },
                )
              }
            >
              Facts vernieuwen
            </Button>
          )}
          <Button
            variant="secondary"
            disabled={revoke.isPending}
            onClick={async () => {
              if (
                await askConfirm(
                  "Agent intrekken? De verbinding wordt meteen verbroken en deze agent kan niet meer inloggen. Daarna kun je opnieuw installeren.",
                )
              )
                void revoke.mutateAsync(a.id);
            }}
          >
            Agent intrekken
          </Button>
          {refreshed && <span className="text-xs text-slate-500">{refreshed}</span>}
        </div>
      )}
    </Card>
  );
}

// FactsView toont de facts in drie delen voor de tabbladen (fase 47):
// software (systeem, updates, services), netwerk (interfaces, routes,
// poorten) en opslag.
function FactsView({ facts: f, runtime, part }: { facts: Facts; runtime: NodeRuntime; part: "software" | "netwerk" | "opslag" }) {
  const hbServices = runtime.heartbeat?.services ?? {};
  return (
    <>
      {part === "software" && (
      <>

        <Tile>
        <Card title="Systeem">
          <p className="-mt-2 mb-3 text-xs text-slate-500">
            Facts verzameld {ago(runtime.collected_at)}
            {runtime.changed_at && ` · laatst gewijzigd ${fmt.format(new Date(runtime.changed_at))}`}
          </p>
          <dl className={dlClass}>
            <dt className={dtClass}>Besturingssysteem</dt>
            <dd>{f.os.pretty_name || `${f.os.id} ${f.os.version_id}`}</dd>
            <dt className={dtClass}>Kernel</dt>
            <dd>
              <span className="font-mono text-xs">{f.kernel}</span>
              {f.reboot_required && (
                <span className="ml-2">
                  <Badge tone="amber">herstart nodig</Badge>
                  {f.kernel_installed && f.kernel_installed !== f.kernel && (
                    <span className="ml-1 text-xs text-slate-500">
                      geïnstalleerd: <span className="font-mono">{f.kernel_installed}</span>
                    </span>
                  )}
                </span>
              )}
            </dd>
            <dt className={dtClass}>Architectuur</dt>
            <dd>{f.arch}</dd>
            <dt className={dtClass}>Virtualisatie</dt>
            <dd>{f.virtualization && f.virtualization !== "none" ? f.virtualization : "fysiek"}</dd>
            <dt className={dtClass}>CPU&apos;s</dt>
            <dd>{f.cpus}</dd>
            <dt className={dtClass}>Geheugen</dt>
            <dd>
              {formatBytes(f.memory_bytes)}
              {f.swap_bytes > 0 && <span className="text-slate-500"> + {formatBytes(f.swap_bytes)} swap</span>}
            </dd>
            <dt className={dtClass}>Opgestart</dt>
            <dd>{fmt.format(new Date(f.boot_time))}</dd>
            <dt className={dtClass}>Machine-id</dt>
            <dd className="font-mono text-xs break-all">{f.machine_id || none}</dd>
          </dl>
        </Card>
        </Tile>

        <Tile>
        <Card title="Updates">
          {f.upgrades ? (
            <div className="space-y-3 text-sm">
              <p>
                {f.upgrades.total === 0 ? (
                  "Alles is bijgewerkt."
                ) : (
                  <>
                    <span className="font-semibold">{f.upgrades.total}</span>{" "}
                    {f.upgrades.total === 1 ? "pakket kan" : "pakketten kunnen"} bijgewerkt worden.{" "}
                    {f.upgrades.security > 0 && (
                      <Badge tone="red">
                        {f.upgrades.security} {f.upgrades.security === 1 ? "beveiligingsupdate" : "beveiligingsupdates"}
                      </Badge>
                    )}
                  </>
                )}
              </p>
              {(f.upgrades.packages?.length ?? 0) > 0 && (
                <p className="text-xs leading-relaxed text-slate-500">{f.upgrades.packages!.join(", ")}</p>
              )}
              <p className="text-xs text-slate-500">
                Volgens de laatste apt update op de node
                {f.apt_updated_at ? `, ${fmt.format(new Date(f.apt_updated_at))}` : ""}.
              </p>
            </div>
          ) : (
            <p className="text-sm text-slate-500">Geen apt op deze node.</p>
          )}
          {f.docker && (
            <div className="mt-4 border-t border-slate-100 pt-4 text-sm dark:border-slate-800">
              Docker {f.docker.version}, {f.docker.containers} {f.docker.containers === 1 ? "container" : "containers"} actief
            </div>
          )}
          {f.keepalived && (
            <div className="mt-4 border-t border-slate-100 pt-4 text-sm dark:border-slate-800">
              Keepalived {f.keepalived.active || "onbekend"}
              {(f.keepalived.vips?.length ?? 0) > 0 && (
                <span className="text-slate-500"> · VIP&apos;s in de config: {f.keepalived.vips!.join(", ")}</span>
              )}
            </div>
          )}
        </Card>
        </Tile>

      <Tile wide>
      <Card title="Services">
        {(f.services?.length ?? 0) === 0 ? (
          <p className="text-sm text-slate-500">Geen van de gevolgde services is geïnstalleerd.</p>
        ) : (
          <div className="flex flex-wrap gap-2">
            {f.services!.map((s) => {
              const active = hbServices[s.name] ?? s.active;
              return (
                <span
                  key={s.name}
                  className="inline-flex items-center gap-1.5 rounded-md border border-slate-200 px-2 py-1 text-sm dark:border-slate-700"
                >
                  {s.name}
                  <Badge tone={active === "active" ? "green" : active === "failed" ? "red" : "slate"}>{active}</Badge>
                </span>
              );
            })}
          </div>
        )}
      </Card>
      </Tile>

      {f.seaweed && (
        <Tile>
          <SeaweedCard seaweed={f.seaweed} />
        </Tile>
      )}

      </>
      )}
      {part === "netwerk" && (
      <>
        <Tile>
        <Card title="Netwerk">
          <Table head={["Interface", "Adressen", ""]}>
            {(f.interfaces ?? []).map((i) => (
              <tr key={i.name}>
                <td className={tdClass}>
                  {i.name}
                  {i.kind && (
                    <span className="ml-1.5 text-xs text-slate-500">
                      {i.kind}
                      {i.vlan_id ? ` ${i.vlan_id}` : ""}
                      {i.master ? ` in ${i.master}` : ""}
                    </span>
                  )}
                  <div className="font-mono text-[11px] text-slate-400">
                    {i.mac}
                    {i.mtu && i.mtu !== 1500 ? ` · mtu ${i.mtu}` : ""}
                  </div>
                </td>
                <td className={`${tdClass} font-mono text-xs`}>{i.addresses?.length ? i.addresses.join(", ") : "–"}</td>
                <td className={`${tdClass} text-right`}>{!i.up && <Badge>down</Badge>}</td>
              </tr>
            ))}
          </Table>
          {(f.default_gateway || (f.dns?.length ?? 0) > 0) && (
            <p className="mt-3 text-xs text-slate-500">
              {f.default_gateway && (
                <>
                  Gateway <span className="font-mono">{f.default_gateway}</span>
                </>
              )}
              {f.default_gateway && (f.dns?.length ?? 0) > 0 && " · "}
              {(f.dns?.length ?? 0) > 0 && (
                <>
                  DNS <span className="font-mono">{f.dns!.join(", ")}</span>
                </>
              )}
            </p>
          )}
        </Card>
        </Tile>

      {((f.routes?.length ?? 0) > 0 || (f.listeners?.length ?? 0) > 0) && (
        <>
          {(f.routes?.length ?? 0) > 0 && (
            <Tile>
            <Card title="Routes">
              <Table head={["Bestemming", "Via", "Interface"]}>
                {f.routes!.map((r, i) => (
                  <tr key={i}>
                    <td className={tdClass}>
                      {r.dest === "0.0.0.0/0" ? "standaard" : <span className="font-mono text-xs">{r.dest}</span>}
                    </td>
                    <td className={tdClass}>
                      {r.gateway ? <span className="font-mono text-xs">{r.gateway}</span> : "rechtstreeks"}
                    </td>
                    <td className={tdClass}>
                      {r.interface}
                      {r.metric ? <span className="text-xs text-slate-500"> · metric {r.metric}</span> : null}
                    </td>
                  </tr>
                ))}
              </Table>
            </Card>
            </Tile>
          )}
          {(f.listeners?.length ?? 0) > 0 && (
            <Tile>
            <Card title="Luisterende poorten">
              <Table head={["Poort", "Adres", "Unit"]}>
                {f.listeners!.map((l, i) => (
                  <tr key={i}>
                    <td className={`${tdClass} tabular-nums`}>
                      {l.port}
                      <span className="text-xs text-slate-500"> {l.proto}</span>
                    </td>
                    <td className={tdClass}>
                      {l.addr === "0.0.0.0" || l.addr === "::" ? (
                        "alle adressen"
                      ) : (
                        <span className="font-mono text-xs">{l.addr}</span>
                      )}
                    </td>
                    <td className={tdClass}>{l.unit || <span className="text-slate-400">–</span>}</td>
                  </tr>
                ))}
              </Table>
            </Card>
            </Tile>
          )}
        </>
      )}
      </>
      )}
      {part === "opslag" && (
      <>
        <Tile>
        <Card title="Opslag">
          <Table head={["Mount", "Type", "Gebruik"]}>
            {(f.filesystems ?? []).map((fs) => {
              const pct = fs.size_bytes > 0 ? Math.round((fs.used_bytes / fs.size_bytes) * 100) : 0;
              return (
                <tr key={fs.mount + fs.device}>
                  <td className={tdClass}>
                    <span className="font-mono text-xs">{fs.mount}</span>
                    <div className="text-[11px] text-slate-400">{fs.device}</div>
                  </td>
                  <td className={tdClass}>{fs.type}</td>
                  <td className={`${tdClass} w-40`}>
                    <div className="h-1.5 rounded-full bg-slate-100 dark:bg-slate-800">
                      <div
                        className={`h-1.5 rounded-full ${pct >= 90 ? "bg-red-500" : pct >= 75 ? "bg-amber-500" : "bg-brand-600"}`}
                        style={{ width: `${pct}%` }}
                      />
                    </div>
                    <div className="mt-1 text-xs text-slate-500 tabular-nums">
                      {formatBytes(fs.used_bytes)} van {formatBytes(fs.size_bytes)} ({pct}%)
                    </div>
                  </td>
                </tr>
              );
            })}
          </Table>
        </Card>
        </Tile>

      </>
      )}
    </>
  );
}

function Table({ head, children }: { head: string[]; children: ReactNode }) {
  return (
    <div className="overflow-x-auto">
      <table className={tableClass}>
        <thead className="border-b border-slate-200 dark:border-slate-800">
          <tr>
            {head.map((h, i) => (
              <th key={i} className={thClass}>
                {h}
              </th>
            ))}
          </tr>
        </thead>
        <tbody className="divide-y divide-slate-100 dark:divide-slate-800">{children}</tbody>
      </table>
    </div>
  );
}

function nodeLinkValues(n: Node) {
  return { hostname: n.hostname, node_id: n.id, cluster: n.cluster_slug, cluster_id: n.cluster_id, env: null };
}
