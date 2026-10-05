"use client";

import Link from "next/link";
import { useRouter, useSearchParams } from "next/navigation";
import { Suspense, useState, type ReactNode } from "react";
import { MetricsPanels } from "@/components/charts/MetricsPanels";
import { InstallAgent } from "@/components/inventory/InstallAgent";
import { NodeForm } from "@/components/inventory/NodeForm";
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
              isAdmin &&
              !editing && (
                <>
                  <Button variant="secondary" onClick={() => setEditing(true)}>
                    Bewerken
                  </Button>
                  <Button
                    variant="danger"
                    disabled={remove.isPending}
                    onClick={async () => {
                      if (!window.confirm(`Node ${node.data!.hostname} verwijderen? Een agent op deze node wordt ook ingetrokken.`))
                        return;
                      await remove.mutateAsync(node.data!.id);
                      router.push("/nodes");
                    }}
                  >
                    Verwijderen
                  </Button>
                </>
              )
            }
          />

          <StatusNote status={node.data.status} reason={node.data.status_reason} since={node.data.status_since} />

          {editing ? (
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
          ) : (
            <div className="grid gap-6 lg:grid-cols-2">
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
              <AgentCard node={node.data} runtime={runtime.data} isAdmin={isAdmin} />
            </div>
          )}

          {node.data.agent && (
            <MetricsPanels
              kind="node"
              id={id}
              actions={<GrafanaButton href={grafanaLink(info.data?.grafana_node_url ?? "", nodeLinkValues(node.data))} />}
            />
          )}

          {runtime.data?.facts && <FactsView facts={runtime.data.facts} runtime={runtime.data} />}
        </div>
      )}
    </QueryState>
  );
}

const dlClass = "grid grid-cols-[9rem_1fr] gap-x-4 gap-y-2.5 text-sm";
const dtClass = "text-slate-500";

function AgentCard({ node, runtime, isAdmin }: { node: Node; runtime?: NodeRuntime; isAdmin: boolean }) {
  const revoke = useRevokeAgent();
  const [installing, setInstalling] = useState(false);
  const a = node.agent;

  if (!a) {
    return (
      <Card title="Agent">
        {installing ? (
          <InstallAgent nodeId={node.id} onClose={() => setInstalling(false)} />
        ) : (
          <div className="space-y-3 text-sm">
            <p className="text-slate-600 dark:text-slate-400">
              Op deze node draait nog geen agent. Met de agent zie je hier het besturingssysteem, de services, updates
              en wie de VIP heeft.
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
      {isAdmin && (
        <div className="mt-4">
          <Button
            variant="secondary"
            disabled={revoke.isPending}
            onClick={() => {
              if (
                window.confirm(
                  "Agent intrekken? De verbinding wordt meteen verbroken en deze agent kan niet meer inloggen. Daarna kun je opnieuw installeren.",
                )
              )
                void revoke.mutateAsync(a.id);
            }}
          >
            Agent intrekken
          </Button>
        </div>
      )}
    </Card>
  );
}

function FactsView({ facts: f, runtime }: { facts: Facts; runtime: NodeRuntime }) {
  const hbServices = runtime.heartbeat?.services ?? {};
  return (
    <div className="space-y-6">
      <div className="flex items-baseline justify-between gap-4">
        <h2 className="text-lg font-semibold">Facts</h2>
        <span className="text-xs text-slate-500">
          verzameld {ago(runtime.collected_at)}
          {runtime.changed_at && ` · laatst gewijzigd ${fmt.format(new Date(runtime.changed_at))}`}
        </span>
      </div>

      <div className="grid gap-6 lg:grid-cols-2">
        <Card title="Systeem">
          <dl className={dlClass}>
            <dt className={dtClass}>Besturingssysteem</dt>
            <dd>{f.os.pretty_name || `${f.os.id} ${f.os.version_id}`}</dd>
            <dt className={dtClass}>Kernel</dt>
            <dd className="font-mono text-xs">{f.kernel}</dd>
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
              <p className="text-xs text-slate-500">Volgens de laatste apt update op de node.</p>
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
      </div>

      <Card title="Services">
        {(f.services?.length ?? 0) === 0 ? (
          <p className="text-sm text-slate-500">Geen van de gevolgde services is geïnstalleerd.</p>
        ) : (
          <div className="flex flex-wrap gap-2">
            {f.services!.map((s) => {
              const active = hbServices[s.name] ?? s.active;
              return (
                <span key={s.name} className="inline-flex items-center gap-1.5 rounded-md border border-slate-200 px-2 py-1 text-sm dark:border-slate-700">
                  {s.name}
                  <Badge tone={active === "active" ? "green" : active === "failed" ? "red" : "slate"}>{active}</Badge>
                </span>
              );
            })}
          </div>
        )}
      </Card>

      <div className="grid gap-6 lg:grid-cols-2">
        <Card title="Netwerk">
          <Table head={["Interface", "Adressen", ""]}>
            {(f.interfaces ?? []).map((i) => (
              <tr key={i.name}>
                <td className={tdClass}>
                  {i.name}
                  <div className="font-mono text-[11px] text-slate-400">{i.mac}</div>
                </td>
                <td className={`${tdClass} font-mono text-xs`}>{i.addresses?.length ? i.addresses.join(", ") : "–"}</td>
                <td className={`${tdClass} text-right`}>{!i.up && <Badge>down</Badge>}</td>
              </tr>
            ))}
          </Table>
        </Card>

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
      </div>
    </div>
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
