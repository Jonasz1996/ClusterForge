"use client";

import Link from "next/link";
import { usePathname, useRouter, useSearchParams } from "next/navigation";
import { Suspense, useState } from "react";
import { DoneAlert, KubeActions, workloadActions, type ActionDone } from "@/components/kube/Actions";
import { applyModes, ApplyTab, type ApplyMode } from "@/components/kube/Apply";
import { LessonsTab } from "@/components/kube/Lessons";
import {
  EventList,
  KubeError,
  Learn,
  LearnToggle,
  ObjectLink,
  ReadyBadge,
  Stat,
  Term,
  Usage,
  UsageBar,
} from "@/components/kube/Kube";
import { AllTab, ConfigTab, EventsTab, NetworkTab, StorageTab } from "@/components/kube/Resources";
import { Empty, formatBytes, tableClass, tdClass, thClass } from "@/components/inventory/bits";
import { Alert, Badge, Button, Card, cx, PageHeader, Select } from "@/components/ui";
import { useCluster, useIsAdmin } from "@/lib/inventory";
import {
  age,
  formatCPU,
  kindHelp,
  kubectl,
  nsFlag,
  podStatusHelp,
  podTone,
  refOf,
  roleLabel,
  stateInfo,
  systemNamespaces,
  useKubeLearning,
  useKubeNodes,
  useKubeOverview,
  useKubePods,
  useKubeWorkloads,
  type KubeNode,
  type KubeOverview,
  type KubePod,
} from "@/lib/kube";

type Tab =
  | "overzicht"
  | "nodes"
  | "apps"
  | "toepassen"
  | "pods"
  | "netwerk"
  | "opslag"
  | "configuratie"
  | "gebeurtenissen"
  | "alles"
  | "lessen";

const tabs: { id: Tab; label: string }[] = [
  { id: "overzicht", label: "Overzicht" },
  { id: "nodes", label: "Nodes" },
  { id: "apps", label: "Apps" },
  { id: "toepassen", label: "Toepassen" },
  { id: "pods", label: "Pods" },
  { id: "netwerk", label: "Netwerk" },
  { id: "opslag", label: "Opslag" },
  { id: "configuratie", label: "Configuratie" },
  { id: "gebeurtenissen", label: "Gebeurtenissen" },
  { id: "alles", label: "Alles" },
  { id: "lessen", label: "Lessen" },
];

// Op deze tabbladen kies je een namespace en of het systeem meedoet.
const filtered: Tab[] = ["apps", "pods", "netwerk", "opslag", "configuratie", "gebeurtenissen", "alles"];

export default function KubeClusterPage() {
  return (
    <Suspense>
      <KubeClusterInner />
    </Suspense>
  );
}

// De tab en de filters staan in de URL, zodat een link naar bijvoorbeeld
// de pods van één node blijft werken.
function useParams() {
  const search = useSearchParams();
  const router = useRouter();
  const pathname = usePathname();
  const set = (patch: Record<string, string | undefined>) => {
    const next = new URLSearchParams(search.toString());
    for (const [k, v] of Object.entries(patch)) {
      if (v) next.set(k, v);
      else next.delete(k);
    }
    router.replace(`${pathname}?${next.toString()}`, { scroll: false });
  };
  const tab = (search.get("tab") as Tab | null) ?? "overzicht";
  return {
    id: search.get("id") ?? "",
    tab: tabs.some((t) => t.id === tab) ? tab : "overzicht",
    namespace: search.get("ns") ?? "",
    system: search.get("system") === "1",
    node: search.get("node") ?? "",
    warnings: search.get("warnings") === "1",
    type: search.get("type") ?? "",
    mode: applyModes.find((m) => m.id === search.get("mode"))?.id ?? ("app" as ApplyMode),
    lesson: search.get("les") ?? "",
    set,
  };
}

function KubeClusterInner() {
  const p = useParams();
  const cluster = useCluster(p.id);
  const overview = useKubeOverview(p.id);
  const o = overview.data;
  const isAdmin = useIsAdmin();
  // Toepassen is alleen voor beheerders; de server controleert hetzelfde.
  const shown = tabs.filter((t) => t.id !== "toepassen" || isAdmin);

  if (p.id === "") return <Alert>Geen cluster opgegeven.</Alert>;

  return (
    <div className="space-y-6">
      <div className="text-sm">
        <Link href="/kubernetes" className="text-brand-700 hover:underline dark:text-sky-300">
          ← Kubernetes
        </Link>
      </div>
      <PageHeader
        title={cluster.data?.name ?? "Kubernetes-cluster"}
        description={
          o ? (
            <span>
              k3s {o.version} · via {o.via}
              {o.vip && <> · VIP {o.vip}</>}
            </span>
          ) : undefined
        }
        actions={
          <>
            <span className="flex items-center px-2">
              <LearnToggle />
            </span>
            <Link
              href={`/clusters/detail?id=${p.id}`}
              className="inline-flex items-center rounded-md border border-slate-300 bg-white px-3.5 py-2 text-sm font-medium text-slate-800 hover:bg-slate-50 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-100 dark:hover:bg-slate-800"
            >
              Cluster in ClusterForge
            </Link>
          </>
        }
      />

      <nav className="flex gap-1 overflow-x-auto border-b border-slate-200 dark:border-slate-800" aria-label="Onderdelen">
        {shown.map((t) => (
          <button
            key={t.id}
            type="button"
            onClick={() => p.set({ tab: t.id === "overzicht" ? undefined : t.id, mode: undefined })}
            className={cx(
              "-mb-px border-b-2 px-3 py-2 text-sm font-medium whitespace-nowrap",
              p.tab === t.id
                ? "border-brand-600 text-brand-700 dark:border-sky-400 dark:text-sky-300"
                : "border-transparent text-slate-600 hover:text-slate-900 dark:text-slate-400 dark:hover:text-slate-100",
            )}
          >
            {t.label}
          </button>
        ))}
      </nav>

      {overview.isLoading ? (
        <p className="text-sm text-slate-500">Laden…</p>
      ) : overview.isError && !o ? (
        <KubeError error={overview.error} />
      ) : (
        o && (
          <>
            {overview.isError && <KubeError error={overview.error} />}
            {filtered.includes(p.tab) && (
              <Filters o={o} namespace={p.namespace} system={p.system} set={p.set} node={p.tab === "pods" ? p.node : ""} />
            )}
            {p.tab === "overzicht" && <OverviewTab o={o} onTab={p.set} />}
            {p.tab === "nodes" && <NodesTab id={p.id} onPods={(node) => p.set({ tab: "pods", node })} />}
            {p.tab === "apps" && (
              <AppsTab
                id={p.id}
                namespace={p.namespace}
                system={p.system}
                onNew={() => p.set({ tab: "toepassen", mode: undefined })}
              />
            )}
            {p.tab === "toepassen" &&
              (isAdmin ? (
                <ApplyTab id={p.id} o={o} mode={p.mode} onMode={(m) => p.set({ mode: m === "app" ? undefined : m })} />
              ) : (
                <Alert>Alleen een beheerder kan iets uitrollen.</Alert>
              ))}
            {p.tab === "pods" && <PodsTab id={p.id} namespace={p.namespace} system={p.system} node={p.node} />}
            {p.tab === "netwerk" && <NetworkTab id={p.id} namespace={p.namespace} system={p.system} />}
            {p.tab === "opslag" && <StorageTab id={p.id} namespace={p.namespace} system={p.system} />}
            {p.tab === "configuratie" && <ConfigTab id={p.id} namespace={p.namespace} system={p.system} />}
            {p.tab === "gebeurtenissen" && (
              <EventsTab
                id={p.id}
                namespace={p.namespace}
                system={p.system}
                warnings={p.warnings}
                onWarnings={(on) => p.set({ warnings: on ? "1" : undefined })}
              />
            )}
            {p.tab === "alles" && (
              <AllTab id={p.id} namespace={p.namespace} system={p.system} type={p.type} onType={(type) => p.set({ type })} />
            )}
            {p.tab === "lessen" && (
              <LessonsTab id={p.id} lesson={p.lesson} namespaces={o.namespaces} onLesson={(les) => p.set({ les })} />
            )}
          </>
        )
      )}
    </div>
  );
}

function Filters({
  o,
  namespace,
  system,
  node,
  set,
}: {
  o: KubeOverview;
  namespace: string;
  system: boolean;
  node?: string;
  set: (patch: Record<string, string | undefined>) => void;
}) {
  const shown = o.namespaces.filter((n) => system || n === namespace || !systemNamespaces.includes(n));
  return (
    <div className="flex flex-wrap items-center gap-3">
      <Select
        className="w-auto"
        aria-label="Namespace"
        value={namespace}
        onChange={(e) => set({ ns: e.target.value || undefined })}
      >
        <option value="">Alle namespaces</option>
        {shown.map((n) => (
          <option key={n} value={n}>
            {n}
          </option>
        ))}
      </Select>
      <label className="inline-flex cursor-pointer items-center gap-2 text-sm text-slate-700 dark:text-slate-300">
        <input
          type="checkbox"
          className="size-4"
          checked={system}
          onChange={(e) => set({ system: e.target.checked ? "1" : undefined })}
        />
        <Term help="kube-system, kube-public en kube-node-lease: de onderdelen van Kubernetes zelf, zoals DNS en Traefik.">
          Systeem tonen
        </Term>
      </label>
      {node && (
        <span className="inline-flex items-center gap-1">
          <Badge tone="blue">node {node}</Badge>
          <button type="button" className="text-xs text-slate-500 hover:underline" onClick={() => set({ node: undefined })}>
            filter weg
          </button>
        </span>
      )}
    </div>
  );
}

function OverviewTab({ o, onTab }: { o: KubeOverview; onTab: (patch: Record<string, string | undefined>) => void }) {
  const learning = useKubeLearning();
  return (
    <div className="space-y-6">
      <Learn commands={[kubectl("get nodes"), kubectl("get deployments -A"), kubectl("get pods -A")]}>
        <p>
          Dit is de stand van het cluster zoals de Kubernetes-API hem nu ziet. <strong>Nodes</strong> zijn de machines.{" "}
          <strong>Apps</strong> zijn wat je laat draaien, zoals een Deployment; Kubernetes maakt er <strong>pods</strong> voor:
          één of meer containers die samen op een node draaien. Een <strong>namespace</strong> is een map om apps te groeperen.
        </p>
      </Learn>

      <div className="grid grid-cols-2 gap-3 md:grid-cols-4">
        <button type="button" className="text-left" onClick={() => onTab({ tab: "nodes" })}>
          <Stat
            label="Nodes Ready"
            value={`${o.nodes_ready}/${o.nodes_total}`}
            tone={o.nodes_ready < o.nodes_total ? "red" : undefined}
            note={o.nodes_ready < o.nodes_total ? `${o.nodes_total - o.nodes_ready} niet Ready` : "alle nodes werken"}
          />
        </button>
        <button type="button" className="text-left" onClick={() => onTab({ tab: "apps", system: "1" })}>
          <Stat
            label="Apps in orde"
            value={`${o.workloads_ready}/${o.workloads_total}`}
            tone={o.workloads_ready < o.workloads_total ? "amber" : undefined}
            note="met die van het systeem"
          />
        </button>
        <button type="button" className="text-left" onClick={() => onTab({ tab: "pods", system: "1" })}>
          <Stat
            label="Pods draaien"
            value={`${o.pods.running}/${o.pods.total}`}
            tone={o.pods.problem > 0 ? "red" : undefined}
            note={
              o.pods.problem > 0
                ? `${o.pods.problem} met problemen`
                : o.pods.pending > 0
                  ? `${o.pods.pending} wachten`
                  : o.pods.succeeded > 0
                    ? `${o.pods.succeeded} klaar`
                    : "geen problemen"
            }
          />
        </button>
        <Stat
          label="Namespaces"
          value={o.namespaces.length}
          note={`${o.namespaces.filter((n) => !systemNamespaces.includes(n)).length} van jou`}
        />
      </div>

      <Card title="Capaciteit">
        <Usage allocatable={o.allocatable} used={o.used} />
        {!o.used && (
          <p className="mt-3 text-xs text-slate-500">metrics-server antwoordt niet; daarom staat het gebruik er niet bij.</p>
        )}
        {learning.on && (
          <p className="mt-3 text-xs text-slate-500">
            Beschikbaar is wat Kubernetes aan pods mag geven (allocatable): het geheugen en de CPU van de nodes min wat het
            systeem nodig heeft. Gebruik komt van metrics-server, dat k3s meelevert; <code>{kubectl("top nodes")}</code> toont
            hetzelfde.
          </p>
        )}
      </Card>

      <Card title="Pods met problemen">
        {o.problems.length === 0 ? (
          <p className="text-sm text-slate-500">Geen pods met problemen.</p>
        ) : (
          <PodTable id={o.cluster_id} pods={o.problems} />
        )}
      </Card>

      <Card title="Waarschuwingen">
        {learning.on && (
          <p className="mb-3 text-xs text-slate-500">
            Kubernetes schrijft events bij alles wat het doet en bewaart ze een uur; dit zijn de laatste vijf waarschuwingen,
            zoals een image dat niet te downloaden is. Zelf: <code>{kubectl("get events -A --field-selector type=Warning")}</code>
          </p>
        )}
        {o.warnings.length === 0 ? (
          <p className="text-sm text-slate-500">Geen waarschuwingen.</p>
        ) : (
          <EventList clusterId={o.cluster_id} events={o.warnings} />
        )}
      </Card>
    </div>
  );
}

function NodesTab({ id, onPods }: { id: string; onPods: (node: string) => void }) {
  const nodes = useKubeNodes(id);
  const first = nodes.data?.[0]?.name ?? "<node>";
  return (
    <div className="space-y-4">
      <Learn
        commands={[
          kubectl("get nodes -o wide"),
          kubectl(`describe node ${first}`),
          kubectl(`drain ${first} --ignore-daemonsets --delete-emptydir-data`),
          kubectl(`uncordon ${first}`),
        ]}
      >
        <p>
          Een <strong>server</strong> draait het brein van Kubernetes: de API, de planner die pods aan nodes geeft, en etcd met de
          toestand van het cluster. Een <strong>agent</strong> draait alleen pods. In k3s draaien servers ook gewone pods.
        </p>
        <p>
          <strong>Ready</strong> betekent dat de kubelet, het programma op de node dat de containers start, zich op tijd meldt.
          Een node in <strong>cordon</strong> krijgt geen nieuwe pods, bijvoorbeeld voor onderhoud. <strong>Leegmaken</strong> zet
          hem op cordon en verhuist ook de pods die er al draaien naar andere nodes.
        </p>
      </Learn>
      {nodes.isError && <KubeError error={nodes.error} />}
      {nodes.data &&
        (nodes.data.length === 0 ? <Empty>Geen nodes.</Empty> : <NodeTable id={id} nodes={nodes.data} onPods={onPods} />)}
    </div>
  );
}

function NodeTable({ id, nodes, onPods }: { id: string; nodes: KubeNode[]; onPods: (node: string) => void }) {
  const isAdmin = useIsAdmin();
  const [done, setDone] = useState<ActionDone | null>(null);
  return (
    <div className="space-y-3">
      <DoneAlert done={done} onClose={() => setDone(null)} />
      <div className="overflow-x-auto rounded-lg border border-slate-200 bg-white dark:border-slate-800 dark:bg-slate-900">
        <table className={tableClass}>
          <thead className="border-b border-slate-200 dark:border-slate-800">
            <tr>
              <th className={thClass}>Node</th>
              <th className={thClass}>Status</th>
              <th className={thClass}>Rol</th>
              <th className={thClass}>Adres</th>
              <th className={`${thClass} min-w-48`}>Gebruik</th>
              <th className={`${thClass} text-right`}>Pods</th>
              <th className={thClass}>Leeftijd</th>
              {isAdmin && <th className={thClass} aria-label="Acties" />}
            </tr>
          </thead>
          <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
            {nodes.map((n) => (
              <tr key={n.name}>
                <td className={tdClass}>
                  <div className="font-medium">
                    <ObjectLink clusterId={id} objRef={refOf("Node", "", n.name)}>
                      {n.name}
                    </ObjectLink>
                  </div>
                  <div className="text-xs text-slate-500">
                    {n.node ? (
                      <Link href={`/nodes/detail?id=${n.node.id}`} className="text-brand-700 hover:underline dark:text-sky-300">
                        node in ClusterForge
                      </Link>
                    ) : (
                      <span title="Geen ClusterForge-node met dit adres of deze naam">niet in ClusterForge</span>
                    )}
                    {n.node && !n.node.agent && " · geen agent"}
                  </div>
                </td>
                <td className={tdClass}>
                  <span className="flex flex-wrap gap-1">
                    <span title={n.ready ? undefined : n.ready_reason}>
                      <Badge tone={n.ready ? "green" : "red"}>{n.ready ? "Ready" : "NotReady"}</Badge>
                    </span>
                    {n.unschedulable && (
                      <Term help="Cordon: er komen geen nieuwe pods op deze node.">
                        <Badge tone="amber">cordon</Badge>
                      </Term>
                    )}
                    {n.pressure.map((c) => (
                      <Badge key={c} tone="amber">
                        {c}
                      </Badge>
                    ))}
                  </span>
                  {!n.ready && n.ready_reason && (
                    <div className="mt-1 text-xs text-red-700 dark:text-red-300">{n.ready_reason}</div>
                  )}
                </td>
                <td className={tdClass}>
                  <Term help={n.roles.length ? `Rollen: ${n.roles.join(", ")}` : "Geen rollen: een gewone worker"}>
                    {roleLabel(n.roles)}
                  </Term>
                  <div className="text-xs text-slate-500">{n.kubelet_version}</div>
                </td>
                <td className={`${tdClass} whitespace-nowrap`}>{n.internal_ip}</td>
                <td className={tdClass}>
                  <div className="space-y-2">
                    <UsageBar label="CPU" used={n.used?.cpu_millis ?? null} total={n.allocatable.cpu_millis} format={formatCPU} />
                    <UsageBar
                      label="Geheugen"
                      used={n.used?.memory_bytes ?? null}
                      total={n.allocatable.memory_bytes}
                      format={formatBytes}
                    />
                  </div>
                </td>
                <td className={`${tdClass} text-right`}>
                  <button
                    type="button"
                    className="text-brand-700 tabular-nums hover:underline dark:text-sky-300"
                    onClick={() => onPods(n.name)}
                  >
                    {n.pods}
                  </button>
                </td>
                <td className={`${tdClass} text-slate-500`} title={n.created_at}>
                  {age(n.created_at)}
                </td>
                {isAdmin && (
                  <td className={`${tdClass} text-right`}>
                    <KubeActions
                      clusterId={id}
                      layout="menu"
                      target={{
                        kind: "Node",
                        ref: refOf("Node", "", n.name),
                        actions: n.unschedulable ? ["uncordon", "drain"] : ["cordon", "drain"],
                      }}
                      onDone={setDone}
                    />
                  </td>
                )}
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}

function AppsTab({ id, namespace, system, onNew }: { id: string; namespace: string; system: boolean; onNew: () => void }) {
  const workloads = useKubeWorkloads(id, namespace, system);
  const isAdmin = useIsAdmin();
  const [done, setDone] = useState<ActionDone | null>(null);
  return (
    <div className="space-y-4">
      <Learn commands={[kubectl(`get deployments,statefulsets,daemonsets,cronjobs ${nsFlag(namespace)}`)]}>
        <p>
          Je laat een app draaien door Kubernetes te vertellen hoe hij eruitziet: welk image, hoeveel kopieën (replica&apos;s).
          Kubernetes maakt de pods en houdt het aantal op peil. Wijs een soort aan voor meer uitleg.
        </p>
        {isAdmin && (
          <p>
            Met <strong>Acties</strong> schaal je een app, herstart je zijn pods of ga je terug naar een vorige versie. Weghalen
            doe je op de pagina van de app zelf. Een nieuwe app zet je op met <strong>Nieuwe app</strong>.
          </p>
        )}
      </Learn>
      {isAdmin && (
        <div className="flex justify-end">
          <Button onClick={onNew}>Nieuwe app</Button>
        </div>
      )}
      <DoneAlert done={done} onClose={() => setDone(null)} />
      {workloads.isError && <KubeError error={workloads.error} />}
      {workloads.data &&
        (workloads.data.length === 0 ? (
          <Empty>
            Geen apps{namespace ? ` in ${namespace}` : ""}.
            {!system && !namespace && " Zet Systeem tonen aan om die van Kubernetes zelf te zien."}
          </Empty>
        ) : (
          <div className="overflow-x-auto rounded-lg border border-slate-200 bg-white dark:border-slate-800 dark:bg-slate-900">
            <table className={tableClass}>
              <thead className="border-b border-slate-200 dark:border-slate-800">
                <tr>
                  <th className={thClass}>App</th>
                  <th className={thClass}>Soort</th>
                  <th className={thClass}>Toestand</th>
                  <th className={`${thClass} text-right`}>Klaar</th>
                  <th className={thClass}>Image</th>
                  <th className={thClass}>Leeftijd</th>
                  {isAdmin && <th className={thClass} aria-label="Acties" />}
                </tr>
              </thead>
              <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
                {workloads.data.map((w) => {
                  const st = stateInfo[w.state];
                  return (
                    <tr key={`${w.kind}/${w.namespace}/${w.name}`}>
                      <td className={tdClass}>
                        <div className="font-medium">
                          <ObjectLink clusterId={id} objRef={refOf(w.kind, w.namespace, w.name)}>
                            {w.name}
                          </ObjectLink>
                        </div>
                        <div className="text-xs text-slate-500">{w.namespace}</div>
                      </td>
                      <td className={tdClass}>
                        <Term help={kindHelp[w.kind]}>{w.kind}</Term>
                        {w.schedule && <div className="font-mono text-xs text-slate-500">{w.schedule}</div>}
                      </td>
                      <td className={tdClass}>
                        <Badge tone={st.tone}>{st.label}</Badge>
                        {w.state_text && <div className="mt-1 text-xs text-slate-500">{w.state_text}</div>}
                      </td>
                      <td className={`${tdClass} text-right`}>
                        {w.kind === "CronJob" ? "–" : <ReadyBadge ready={w.ready} total={w.desired} />}
                      </td>
                      <td className={`${tdClass} max-w-64 break-all font-mono text-xs`}>{w.images.join(", ")}</td>
                      <td className={`${tdClass} text-slate-500`} title={w.created_at}>
                        {age(w.created_at)}
                      </td>
                      {isAdmin && (
                        <td className={`${tdClass} text-right`}>
                          <KubeActions
                            clusterId={id}
                            layout="menu"
                            target={{
                              kind: w.kind,
                              ref: refOf(w.kind, w.namespace, w.name),
                              actions: workloadActions(w.kind),
                              replicas: w.desired,
                            }}
                            onDone={setDone}
                          />
                        </td>
                      )}
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        ))}
    </div>
  );
}

function PodsTab({ id, namespace, system, node }: { id: string; namespace: string; system: boolean; node: string }) {
  const pods = useKubePods(id, namespace, system, node);
  const first = pods.data?.[0];
  const example = first ? `${first.name} -n ${first.namespace}` : "<pod> -n <namespace>";
  return (
    <div className="space-y-4">
      <Learn
        commands={[
          kubectl(`get pods ${nsFlag(namespace)} -o wide${node ? ` --field-selector spec.nodeName=${node}` : ""}`),
          kubectl(`describe pod ${example}`),
          kubectl(`logs ${example}`),
          kubectl(`delete pod ${example}`),
        ]}
      >
        <p>
          Een <strong>pod</strong> is de kleinste eenheid in Kubernetes: één of meer containers die samen op een node draaien en
          een eigen IP-adres hebben. Pods zijn wegwerp: bij een nieuwe versie of een kapotte node maakt Kubernetes nieuwe.
        </p>
        <p>
          <strong>Klaar</strong> zegt hoeveel containers klaar zijn voor verkeer. <strong>Herstarts</strong> die blijven oplopen,
          wijzen op een container die crasht. Wijs een status aan voor uitleg.
        </p>
        <p>
          Een pod <strong>weghalen</strong> is de gewone manier om hem opnieuw te laten starten: zijn eigenaar, zoals een
          ReplicaSet, maakt meteen een nieuwe.
        </p>
      </Learn>
      {pods.isError && <KubeError error={pods.error} />}
      {pods.data &&
        (pods.data.length === 0 ? (
          <Empty>
            Geen pods{namespace ? ` in ${namespace}` : ""}
            {node ? ` op ${node}` : ""}.{!system && !namespace && " Zet Systeem tonen aan om die van Kubernetes zelf te zien."}
          </Empty>
        ) : (
          <PodTable id={id} pods={pods.data} />
        ))}
    </div>
  );
}

function PodTable({ id, pods }: { id: string; pods: KubePod[] }) {
  const isAdmin = useIsAdmin();
  const [done, setDone] = useState<ActionDone | null>(null);
  return (
    <div className="space-y-3">
      <DoneAlert done={done} onClose={() => setDone(null)} />
      <div className="overflow-x-auto rounded-lg border border-slate-200 bg-white dark:border-slate-800 dark:bg-slate-900">
        <table className={tableClass}>
          <thead className="border-b border-slate-200 dark:border-slate-800">
            <tr>
              <th className={thClass}>Pod</th>
              <th className={thClass}>Status</th>
              <th className={`${thClass} text-right`}>Klaar</th>
              <th className={`${thClass} text-right`}>Herstarts</th>
              <th className={thClass}>Node</th>
              <th className={thClass}>IP</th>
              <th className={thClass}>Leeftijd</th>
              {isAdmin && <th className={thClass} aria-label="Acties" />}
            </tr>
          </thead>
          <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
            {pods.map((p) => (
              <tr key={`${p.namespace}/${p.name}`}>
                <td className={tdClass}>
                  <div className="min-w-56 font-medium break-all">
                    <ObjectLink clusterId={id} objRef={refOf("Pod", p.namespace, p.name)}>
                      {p.name}
                    </ObjectLink>
                  </div>
                  <div className="text-xs text-slate-500">
                    {p.namespace}
                    {p.owner_kind && ` · van ${p.owner_kind} ${p.owner_name}`}
                  </div>
                </td>
                <td className={tdClass}>
                  <Term help={podStatusHelp[p.status] ?? p.status}>
                    <Badge tone={podTone(p)}>{p.status}</Badge>
                  </Term>
                </td>
                <td className={`${tdClass} text-right tabular-nums`}>
                  {p.ready}/{p.containers}
                </td>
                <td className={cx(tdClass, "text-right tabular-nums", p.restarts > 0 && "text-amber-700 dark:text-amber-300")}>
                  {p.restarts}
                </td>
                <td className={`${tdClass} whitespace-nowrap`}>
                  {p.node ? (
                    <ObjectLink clusterId={id} objRef={refOf("Node", "", p.node)}>
                      {p.node}
                    </ObjectLink>
                  ) : (
                    "–"
                  )}
                </td>
                <td className={`${tdClass} whitespace-nowrap font-mono text-xs`}>{p.ip || "–"}</td>
                <td className={`${tdClass} text-slate-500`} title={p.created_at}>
                  {age(p.created_at)}
                </td>
                {isAdmin && (
                  <td className={`${tdClass} text-right`}>
                    <KubeActions
                      clusterId={id}
                      layout="menu"
                      target={{
                        kind: "Pod",
                        ref: refOf("Pod", p.namespace, p.name),
                        actions: ["delete"],
                        owner: p.owner_kind ? { kind: p.owner_kind, name: p.owner_name } : null,
                      }}
                      onDone={setDone}
                    />
                  </td>
                )}
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}
