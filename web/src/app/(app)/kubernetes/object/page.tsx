"use client";

import Link from "next/link";
import { useSearchParams } from "next/navigation";
import { Suspense, useState, type ReactNode } from "react";
import { DoneAlert, KubeActions, type ActionDone, type ActionTarget } from "@/components/kube/Actions";
import { CodeBlock, EventList, KubeError, Learn, LearnToggle, ObjectLink, Term } from "@/components/kube/Kube";
import { KubeMetrics } from "@/components/kube/KubeMetrics";
import { Logs } from "@/components/kube/Logs";
import { ago, tableClass, tdClass, thClass } from "@/components/inventory/bits";
import { Alert, Badge, Card, cx, PageHeader } from "@/components/ui";
import { useCluster } from "@/lib/inventory";
import {
  age,
  kubectl,
  kubectlObject,
  objectHelp,
  podStatusHelp,
  useKubeLearning,
  useKubeObject,
  type KubeChainLink,
  type KubeContainer,
  type KubeMetricsScope,
  type KubeObject,
  type KubeRef,
} from "@/lib/kube";

export default function KubeObjectPage() {
  return (
    <Suspense>
      <KubeObjectInner />
    </Suspense>
  );
}

function KubeObjectInner() {
  const search = useSearchParams();
  const id = search.get("id") ?? "";
  const ref: KubeRef = {
    group: search.get("g") ?? "",
    version: search.get("v") ?? "",
    resource: search.get("r") ?? "",
    namespace: search.get("ns") ?? "",
    name: search.get("name") ?? "",
  };
  const cluster = useCluster(id);
  const [done, setDone] = useState<ActionDone | null>(null);
  const deleted = done?.deleted ?? false;
  const obj = useKubeObject(id, ref, !deleted);
  const o = obj.data;

  if (id === "" || ref.name === "" || ref.resource === "") return <Alert>Geen object opgegeven.</Alert>;

  return (
    <div className="space-y-6">
      <div className="text-sm">
        <Link href={`/kubernetes/cluster?id=${id}`} className="text-brand-700 hover:underline dark:text-sky-300">
          ← {cluster.data?.name ?? "Kubernetes-cluster"}
        </Link>
      </div>
      <PageHeader
        title={
          <span className="break-all">
            <span className="text-slate-500">{o?.kind ?? ref.resource}</span> {ref.name}
          </span>
        }
        description={
          o ? (
            <span>
              {ref.namespace ? <>namespace {ref.namespace} · </> : null}
              {o.api_version} · {age(o.created_at)} oud
            </span>
          ) : undefined
        }
        actions={
          <>
            <span className="flex items-center px-2">
              <LearnToggle />
            </span>
            {o && !deleted && <KubeActions clusterId={id} target={targetOf(o)} onDone={setDone} />}
          </>
        }
      />
      {deleted && done ? (
        <Alert kind="success">
          {done.message}{" "}
          <Link href={backHref(id, o?.kind ?? "")} className="font-medium underline">
            Terug naar {cluster.data?.name ?? "het cluster"}
          </Link>
        </Alert>
      ) : (
        <>
          <DoneAlert done={done} onClose={() => setDone(null)} />
          {obj.isLoading && <p className="text-sm text-slate-500">Laden…</p>}
          {obj.isError && <KubeError error={obj.error} />}
          {o && <ObjectView id={id} o={o} />}
        </>
      )}
    </div>
  );
}

// targetOf geeft de acties van het object, met de eigenaar van een pod.
function targetOf(o: KubeObject): ActionTarget {
  const owner = o.chain.find((l) => l.role === "owner");
  return {
    kind: o.kind,
    ref: o.ref,
    actions: o.actions,
    replicas: o.replicas,
    owner: owner ? { kind: owner.kind, name: owner.name } : null,
  };
}

// backHref is het tabblad waar het weggehaalde object stond.
function backHref(id: string, kind: string) {
  const tab =
    kind === "Pod"
      ? "pods"
      : ["Deployment", "StatefulSet", "DaemonSet", "Job", "CronJob"].includes(kind)
        ? "apps"
        : kind === "Node"
          ? "nodes"
          : "";
  return `/kubernetes/cluster?id=${id}${tab ? `&tab=${tab}` : ""}`;
}

// metricsScope kiest de grafieken van een pod of app; andere soorten hebben
// er geen.
function metricsScope(o: KubeObject): KubeMetricsScope | null {
  const ns = o.ref.namespace;
  if (o.kind === "Pod") return { namespace: ns, pod: o.ref.name };
  if (o.kind === "Deployment" || o.kind === "StatefulSet" || o.kind === "DaemonSet")
    return { namespace: ns, kind: o.kind, name: o.ref.name };
  return null;
}

function ObjectView({ id, o }: { id: string; o: KubeObject }) {
  const scope = metricsScope(o);
  const what = kubectlObject(o.ref, o.kind);
  const commands = [kubectl(`get ${what} -o yaml`), kubectl(`describe ${what}`)];
  if (o.kind === "Pod") commands.push(kubectl(`logs ${o.ref.name} -n ${o.ref.namespace}`));
  if (["Deployment", "StatefulSet", "DaemonSet"].includes(o.kind)) commands.push(kubectl(`rollout status ${what}`));
  return (
    <div className="space-y-6">
      <div
        className={cx(
          "rounded-md border px-4 py-3 text-sm",
          o.problem
            ? "border-red-200 bg-red-50 text-red-900 dark:border-red-900 dark:bg-red-950 dark:text-red-200"
            : "border-emerald-200 bg-emerald-50 text-emerald-900 dark:border-emerald-900 dark:bg-emerald-950 dark:text-emerald-200",
        )}
      >
        <span className="font-medium">{o.problem ? "Vraagt aandacht: " : "In orde: "}</span>
        {o.summary || "geen status"}
      </div>

      <Learn title={`Wat is een ${o.kind}?`} commands={commands}>
        <p>
          {objectHelp[o.kind] ??
            `${o.kind} is een soort uit ${o.api_version}, van Kubernetes zelf of van een app in het cluster.`}
        </p>
      </Learn>

      {o.chain.length > 0 && (
        <Card title="Keten">
          <Chain id={id} chain={o.chain} />
        </Card>
      )}

      {o.pod && (
        <>
          <Card title="Containers">
            <PodFacts pod={o.pod} />
            <Containers containers={o.pod.containers} />
          </Card>
          <Card title="Logs">
            <Logs clusterId={id} namespace={o.ref.namespace} pod={o.ref.name} containers={o.pod.containers} />
          </Card>
        </>
      )}

      {scope && <KubeMetrics clusterId={id} scope={scope} title="CPU en geheugen per pod" />}

      {o.conditions.length > 0 && (
        <Card title="Condities">
          <Conditions o={o} />
        </Card>
      )}

      <Card title="Gebeurtenissen">
        <EventsHelp />
        {o.events.length === 0 ? (
          <p className="text-sm text-slate-500">Geen events in het laatste uur.</p>
        ) : (
          <EventList clusterId={id} events={o.events} showObject={false} />
        )}
      </Card>

      {(Object.keys(o.labels).length > 0 || Object.keys(o.annotations).length > 0) && (
        <Card title="Labels">
          <Labels labels={o.labels} />
          {Object.keys(o.annotations).length > 0 && (
            <details className="mt-3 text-sm">
              <summary className="cursor-pointer text-slate-600 dark:text-slate-400">
                {Object.keys(o.annotations).length} annotaties
              </summary>
              <dl className="mt-2 space-y-1 text-xs">
                {Object.entries(o.annotations).map(([k, v]) => (
                  <div key={k} className="break-all">
                    <dt className="inline font-mono font-medium">{k}</dt>: <dd className="inline font-mono">{v}</dd>
                  </div>
                ))}
              </dl>
            </details>
          )}
        </Card>
      )}

      <Card title="YAML">
        <YAMLHelp />
        <CodeBlock text={o.yaml} className="max-h-[36rem]" />
      </Card>
    </div>
  );
}

function EventsHelp() {
  const learning = useKubeLearning();
  if (!learning.on) return null;
  return (
    <p className="mb-3 text-xs text-slate-500">
      Wat Kubernetes met dit object deed, het nieuwste eerst. Een waarschuwing zegt meestal waarom iets niet start.
    </p>
  );
}

function YAMLHelp() {
  const learning = useKubeLearning();
  if (!learning.on) return null;
  return (
    <p className="mb-3 text-xs text-slate-500">
      Zo bewaart Kubernetes het object. <code>spec</code> is wat jij vraagt, <code>status</code> wat Kubernetes ziet. De
      boekhouding (managedFields) is weggelaten, en van een Secret de waarden.
    </p>
  );
}

const roleLabel: Record<KubeChainLink["role"], string> = {
  owner: "Gemaakt door",
  node: "Draait op node",
  clusterforge: "Node in ClusterForge",
  vm: "Machine",
  service: "Bereikbaar via",
  ingress: "Van buiten via",
  pods: "Kiest pods",
  volume: "Volume",
  claim: "Claim",
  backend: "Stuurt naar",
};

// Chain toont de keten van boven naar onder: wie het object maakte, waar
// het draait tot de VM op de Proxmox-host, en hoe je het bereikt.
function Chain({ id, chain }: { id: string; chain: KubeChainLink[] }) {
  const learning = useKubeLearning();
  return (
    <div className="space-y-3">
      {learning.on && (
        <p className="text-xs text-slate-500">
          Een Deployment maakt een ReplicaSet, die de pods maakt. De pod draait op een node, die in ClusterForge een VM op een
          Proxmox-host is. Een Service en een Ingress brengen er verkeer naartoe.
        </p>
      )}
      <ol className="space-y-2 text-sm">
        {chain.map((l, i) => (
          <li key={`${l.role}/${l.kind}/${l.name}/${i}`} className="flex flex-col gap-0.5 sm:flex-row sm:items-baseline sm:gap-3">
            <span className="shrink-0 text-xs font-medium tracking-wide text-slate-500 uppercase sm:w-40">
              {roleLabel[l.role]}
            </span>
            <span className="min-w-0 break-all">
              <ChainTarget id={id} l={l} />
              {l.text && l.role !== "vm" && <span className="text-slate-500"> · {l.text}</span>}
              {l.url && (
                <>
                  {" · "}
                  <a href={l.url} target="_blank" rel="noreferrer" className="text-brand-700 hover:underline dark:text-sky-300">
                    {l.url}
                  </a>
                </>
              )}
            </span>
          </li>
        ))}
      </ol>
    </div>
  );
}

function ChainTarget({ id, l }: { id: string; l: KubeChainLink }): ReactNode {
  const label = l.role === "vm" ? l.text : l.kind && l.role !== "pods" ? `${l.kind} ${l.name}` : l.name;
  if (l.ref) {
    return (
      <ObjectLink clusterId={id} objRef={l.ref}>
        {label}
      </ObjectLink>
    );
  }
  if (l.node_id) {
    return (
      <Link href={`/nodes/detail?id=${l.node_id}`} className="text-brand-700 hover:underline dark:text-sky-300">
        {label}
      </Link>
    );
  }
  return <span>{label}</span>;
}

function PodFacts({ pod }: { pod: NonNullable<KubeObject["pod"]> }) {
  return (
    <dl className="mb-4 grid grid-cols-2 gap-x-6 gap-y-2 text-sm md:grid-cols-4">
      <Fact label="Node">{pod.node || "nog geen"}</Fact>
      <Fact label="IP van de pod">{pod.ip || "–"}</Fact>
      <Fact label="IP van de node">{pod.host_ip || "–"}</Fact>
      <Fact label="QoS">
        <Term help="Hoe Kubernetes de pod behandelt als een node te weinig geheugen heeft. BestEffort (zonder requests en limits) gaat eerst weg, dan Burstable, Guaranteed als laatste.">
          {pod.qos || "–"}
        </Term>
      </Fact>
    </dl>
  );
}

function Fact({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div>
      <dt className="text-xs text-slate-500">{label}</dt>
      <dd className="font-mono text-xs break-all">{children}</dd>
    </div>
  );
}

function containerTone(c: KubeContainer): "green" | "amber" | "red" | "slate" {
  if (c.state === "running") return c.ready ? "green" : "amber";
  if (c.state === "terminated") return c.reason === "Completed" ? "slate" : "red";
  if (c.state === "waiting") return ["ContainerCreating", "PodInitializing"].includes(c.reason) ? "amber" : "red";
  return "slate";
}

function containerLabel(c: KubeContainer) {
  if (c.state === "running") return c.ready ? "Draait" : "Draait, niet klaar";
  if (c.state === "unknown") return "Nog niet gestart";
  return c.reason || (c.state === "waiting" ? "Wacht" : "Gestopt");
}

function Containers({ containers }: { containers: KubeContainer[] }) {
  return (
    <div className="overflow-x-auto">
      <table className={tableClass}>
        <thead className="border-b border-slate-200 dark:border-slate-800">
          <tr>
            <th className={thClass}>Container</th>
            <th className={thClass}>Toestand</th>
            <th className={`${thClass} text-right`}>Herstarts</th>
            <th className={thClass}>Vorige keer</th>
            <th className={thClass}>Image</th>
          </tr>
        </thead>
        <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
          {containers.map((c) => (
            <tr key={`${c.init}/${c.name}`}>
              <td className={tdClass}>
                <span className="font-medium">{c.name}</span>{" "}
                {c.init && (
                  <Term help="Een init-container draait voor de andere en moet eerst klaar zijn, zoals wachten op een databank.">
                    <Badge>init</Badge>
                  </Term>
                )}
              </td>
              <td className={tdClass}>
                <Term help={podStatusHelp[c.reason] ?? (c.state === "running" ? podStatusHelp.Running : c.reason)}>
                  <Badge tone={containerTone(c)}>{containerLabel(c)}</Badge>
                </Term>
                {c.since && c.state === "running" && <div className="mt-1 text-xs text-slate-500">sinds {ago(c.since)}</div>}
                {c.message && <div className="mt-1 max-w-md text-xs break-words text-slate-500">{c.message}</div>}
              </td>
              <td className={cx(tdClass, "text-right tabular-nums", c.restarts > 0 && "text-amber-700 dark:text-amber-300")}>
                {c.restarts}
              </td>
              <td className={`${tdClass} text-xs`}>
                {c.last_reason ? (
                  <>
                    <Term help={podStatusHelp[c.last_reason] ?? c.last_reason}>{c.last_reason}</Term>
                    {c.last_exit_code !== null && (
                      <span className="whitespace-nowrap text-slate-500"> · exitcode {c.last_exit_code}</span>
                    )}
                    {c.last_finished && <div className="text-slate-500">{ago(c.last_finished)}</div>}
                  </>
                ) : (
                  "–"
                )}
              </td>
              <td className={`${tdClass} max-w-72 min-w-48 font-mono text-xs break-all`}>{c.image}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

// Bij een Pressure-conditie is True slecht, bij de andere goed.
function conditionTone(type: string, status: string): "green" | "amber" | "red" {
  const badWhenTrue = type.endsWith("Pressure") || type === "NetworkUnavailable";
  if (status === "Unknown") return "amber";
  return (status === "True") !== badWhenTrue ? "green" : "red";
}

function Conditions({ o }: { o: KubeObject }) {
  const learning = useKubeLearning();
  return (
    <>
      {learning.on && (
        <p className="mb-3 text-xs text-slate-500">
          Condities zijn de controles die Kubernetes bijhoudt, zoals Ready of Available, met de reden als iets niet klopt.
        </p>
      )}
      <div className="overflow-x-auto">
        <table className={tableClass}>
          <thead className="border-b border-slate-200 dark:border-slate-800">
            <tr>
              <th className={thClass}>Conditie</th>
              <th className={thClass}>Status</th>
              <th className={thClass}>Reden</th>
              <th className={thClass}>Sinds</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
            {o.conditions.map((c) => (
              <tr key={c.type}>
                <td className={`${tdClass} font-medium`}>{c.type}</td>
                <td className={tdClass}>
                  <Badge tone={conditionTone(c.type, c.status)}>{c.status}</Badge>
                </td>
                <td className={`${tdClass} text-xs`}>
                  {c.reason && <div className="font-medium">{c.reason}</div>}
                  {c.message && <div className="max-w-xl break-words text-slate-500">{c.message}</div>}
                  {!c.reason && !c.message && "–"}
                </td>
                <td className={`${tdClass} text-slate-500`}>{c.last_transition ? ago(c.last_transition) : "–"}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </>
  );
}

function Labels({ labels }: { labels: Record<string, string> }) {
  const entries = Object.entries(labels);
  if (entries.length === 0) return <p className="text-sm text-slate-500">Geen labels.</p>;
  return (
    <div className="flex flex-wrap gap-1.5">
      {entries.map(([k, v]) => (
        <span key={k} className="rounded bg-slate-100 px-1.5 py-0.5 font-mono text-xs break-all dark:bg-slate-800">
          {k}={v}
        </span>
      ))}
    </div>
  );
}
