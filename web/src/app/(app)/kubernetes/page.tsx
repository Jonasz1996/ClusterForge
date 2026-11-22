"use client";

import Link from "next/link";
import { useRouter } from "next/navigation";
import { Learn, LearnToggle, ReadyBadge } from "@/components/kube/Kube";
import { ago, Empty, EnvBadge, QueryState, StatusBadge, tableClass, tdClass, thClass } from "@/components/inventory/bits";
import { Badge, Button, PageHeader } from "@/components/ui";
import { useIsAdmin } from "@/lib/inventory";
import { kubectl, useKubeClusters, type KubeClusterSummary } from "@/lib/kube";

export default function KubernetesPage() {
  const clusters = useKubeClusters();
  const isAdmin = useIsAdmin();
  const router = useRouter();

  return (
    <div className="space-y-6">
      <PageHeader
        title="Kubernetes"
        description="De Kubernetes-clusters (k3s), met hun nodes, apps en pods."
        actions={
          <>
            <span className="flex items-center px-2">
              <LearnToggle />
            </span>
            {isAdmin && <Button onClick={() => router.push("/clusters/uitrollen?template=kubernetes")}>Cluster uitrollen</Button>}
          </>
        }
      />

      <Learn title="Wat is Kubernetes?" commands={[kubectl("get nodes"), kubectl("get pods -A")]}>
        <p>
          Kubernetes draait je apps in containers op een groep machines, de nodes. Je schrijft op wat je wilt, zoals &quot;drie
          keer nginx&quot;, en Kubernetes zorgt dat het zo is: valt een node uit, dan start het de containers ergens anders
          opnieuw.
        </p>
        <p>
          ClusterForge gebruikt k3s, een kleine Kubernetes die als één programma installeert. De <em>servers</em> beheren het
          cluster en bewaren de toestand; de <em>agents</em> (workers) draaien de apps. Met drie servers blijft het cluster werken
          als er één uitvalt.
        </p>
        <p>Deze uitleg zie je zolang de leermodus aanstaat; zet hem rechtsboven uit als je hem niet meer nodig hebt.</p>
      </Learn>

      <QueryState q={clusters}>
        {clusters.data?.length === 0 ? (
          <Empty>
            Nog geen Kubernetes-cluster.{" "}
            {isAdmin
              ? "Rol er een uit met de template kubernetes, of maak een cluster van het type Kubernetes en zet cf-agent op een k3s-server."
              : "Een beheerder kan er een uitrollen."}
          </Empty>
        ) : (
          <div className="overflow-x-auto rounded-lg border border-slate-200 bg-white dark:border-slate-800 dark:bg-slate-900">
            <table className={tableClass}>
              <thead className="border-b border-slate-200 dark:border-slate-800">
                <tr>
                  <th className={thClass}>Naam</th>
                  <th className={thClass}>Status</th>
                  <th className={thClass}>Versie</th>
                  <th className={`${thClass} text-right`}>Nodes</th>
                  <th className={`${thClass} text-right`}>Apps</th>
                  <th className={`${thClass} text-right`}>Pods</th>
                  <th className={thClass}>Bijgewerkt</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
                {(clusters.data ?? []).map((c) => (
                  <ClusterRow key={c.id} c={c} />
                ))}
              </tbody>
            </table>
          </div>
        )}
      </QueryState>
    </div>
  );
}

function ClusterRow({ c }: { c: KubeClusterSummary }) {
  return (
    <tr className="hover:bg-slate-50 dark:hover:bg-slate-800/50">
      <td className={tdClass}>
        <Link href={`/kubernetes/cluster?id=${c.id}`} className="font-medium text-brand-700 hover:underline dark:text-sky-300">
          {c.name}
        </Link>
        <div className="flex items-center gap-1 text-xs text-slate-500">
          <EnvBadge env={c.environment} />
          {c.vip && <span>{c.vip}</span>}
        </div>
      </td>
      <td className={tdClass}>
        <span className="flex flex-wrap gap-1">
          <StatusBadge status={c.status} reason={c.status_reason} />
          {c.synced && !c.api_ok && (
            <span title={c.error}>
              <Badge tone="red">API niet bereikbaar</Badge>
            </span>
          )}
          {c.pods_problem > 0 && <Badge tone="red">{c.pods_problem} pods met problemen</Badge>}
        </span>
      </td>
      <td className={`${tdClass} whitespace-nowrap`}>{c.version || "–"}</td>
      <td className={`${tdClass} text-right`}>{c.synced ? <ReadyBadge ready={c.nodes_ready} total={c.nodes_total} /> : "–"}</td>
      <td className={`${tdClass} text-right`}>
        {c.synced ? <ReadyBadge ready={c.workloads_ready} total={c.workloads_total} /> : "–"}
      </td>
      <td className={`${tdClass} text-right tabular-nums`}>
        {c.synced ? (
          <span title={`${c.pods_running} draaien, ${c.pods_pending} wachten`}>
            {c.pods_running}/{c.pods_total}
          </span>
        ) : (
          "–"
        )}
      </td>
      <td className={`${tdClass} whitespace-nowrap text-slate-500`}>
        {c.synced ? (c.api_ok ? ago(c.synced_at) : `laatst gelukt ${ago(c.ok_at)}`) : "nog niet"}
      </td>
    </tr>
  );
}
