"use client";

import Link from "next/link";
import { useState } from "react";
import { tableClass, tdClass, thClass } from "@/components/inventory/bits";
import { ReapplyDialog } from "@/components/gitops/Decide";
import { SpecEditDialog } from "@/components/inventory/SpecEdit";
import { Learn, Term } from "@/components/kube/Kube";
import { Badge, Button, Card } from "@/components/ui";
import { ApiError } from "@/lib/api/client";
import { useCluster, useIsAdmin } from "@/lib/inventory";
import { kubectl, useKubeUpgrade, type KubeUpgrade } from "@/lib/kube";

// shortK3s maakt van v1.36.5+k3s1 v1.36.5.
function shortK3s(v: string) {
  return v.replace(/\+k3s\d+$/, "");
}

// UpgradeCard toont welke k3s het cluster draait, wat elke node meldt, en
// biedt de volgende versie aan. Bijwerken is overstappen naar een nieuwere
// versie van de template, met het plan zoals bij elke wijziging van de spec.
export function UpgradeCard({ id }: { id: string }) {
  const q = useKubeUpgrade(id);
  const cluster = useCluster(id);
  const isAdmin = useIsAdmin();
  const [open, setOpen] = useState(false);
  if (q.error instanceof ApiError && q.error.code === "not_template") {
    return (
      <Card title="Kubernetes-versie">
        <p className="text-sm text-slate-500">
          Dit cluster komt niet uit een template van ClusterForge. k3s bijwerken doe je daarom zelf, met het installatiescript van
          k3s.
        </p>
      </Card>
    );
  }
  const u = q.data;
  const c = cluster.data;
  if (!u) return null;
  const next = u.next;
  const versions = new Set(u.nodes.map((n) => n.kubelet));
  return (
    <Card title="Kubernetes-versie">
      <div className="space-y-4 text-sm">
        <Learn commands={[kubectl("get nodes"), "sudo k3s --version"]}>
          <p>
            Elke node draait k3s, met de <Term help="De agent van Kubernetes op elke node; hij start de pods.">kubelet</Term>.
            Bijwerken gaat node voor node: eerst de servers, want een kubelet mag niet nieuwer zijn dan de API-server. Elke node
            gaat eerst leeg (<em>drain</em>), krijgt de nieuwe k3s, herstart, en krijgt pas weer pods als hij Ready is met de
            nieuwe versie. Kubernetes gaat één minorversie per keer omhoog, van 1.35 naar 1.36, en nooit terug. In de kolom
            VERSION van <code>kubectl get nodes</code> zie je per node welke versie hij draait.
          </p>
        </Learn>
        <div className="flex flex-wrap items-start justify-between gap-3">
          <div className="space-y-1">
            <p>
              {u.k3s ? (
                <>
                  Dit cluster hoort k3s <code>{shortK3s(u.k3s)}</code> te draaien, uit {u.template} {u.version}.
                </>
              ) : (
                <>
                  Versie {u.version} van {u.template} zit niet in deze ClusterForge; welke k3s erbij hoort, is daarom onbekend.
                </>
              )}
            </p>
            {next ? (
              <p>
                Er is een nieuwere versie: {u.template} {next.version} met k3s <code>{shortK3s(next.k3s)}</code>.
              </p>
            ) : (
              u.k3s && <p className="text-slate-500">Dit is de nieuwste k3s die deze ClusterForge kent.</p>
            )}
          </div>
          {next && isAdmin && (
            <Button disabled={!!u.blocked || !c} title={u.blocked || undefined} onClick={() => setOpen(true)}>
              Bijwerken naar k3s {shortK3s(next.k3s)}…
            </Button>
          )}
        </div>
        <Blocked u={u} id={id} />
        {u.nodes_error ? (
          <p className="text-xs text-slate-500">De nodes zijn nu niet te lezen: {u.nodes_error}</p>
        ) : (
          <>
            {versions.size > 1 && (
              <p className="text-amber-800 dark:text-amber-300">
                Niet elke node draait dezelfde versie. Dat mag even, tijdens het bijwerken, maar niet lang: pas als elke node is
                bijgewerkt, is het cluster weer gelijk.
              </p>
            )}
            <div className="overflow-x-auto">
              <table className={tableClass}>
                <thead>
                  <tr>
                    <th className={thClass}>Node</th>
                    <th className={thClass}>Rol</th>
                    <th className={thClass}>Versie</th>
                    <th className={thClass}>Status</th>
                  </tr>
                </thead>
                <tbody>
                  {u.nodes.map((n) => (
                    <tr key={n.name}>
                      <td className={`${tdClass} whitespace-nowrap`}>{n.name}</td>
                      <td className={tdClass}>{n.server ? "server" : "worker"}</td>
                      <td className={tdClass}>
                        <code>{shortK3s(n.kubelet)}</code>
                        {u.k3s && n.kubelet !== u.k3s && (
                          <span className="ml-2">
                            <Badge tone="amber">niet {shortK3s(u.k3s)}</Badge>
                          </span>
                        )}
                      </td>
                      <td className={tdClass}>
                        {!n.ready ? (
                          <Badge tone="red">Niet Ready</Badge>
                        ) : n.unschedulable ? (
                          <Badge tone="amber">Ready, op cordon</Badge>
                        ) : (
                          <Badge tone="green">Ready</Badge>
                        )}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </>
        )}
      </div>
      {open && c && next && (
        <SpecEditDialog
          cluster={c}
          version={next.version}
          title={`Bijwerken naar k3s ${shortK3s(next.k3s)}`}
          intro={
            <p className="text-slate-600 dark:text-slate-400">
              Het cluster gaat naar {u.template} {next.version}. ClusterForge werkt de nodes één voor één bij: eerst de servers,
              dan de workers. Elke node gaat eerst leeg, krijgt k3s {shortK3s(next.k3s)} en herstart; pas als hij Ready is met de
              nieuwe versie, krijgt hij weer pods en komt de volgende aan de beurt. Komt een node niet terug, dan stopt de taak en
              blijft die node op cordon, zodat de rest blijft draaien. Maak eerst een snapshot op het tabblad Snapshots.
            </p>
          }
          onClose={() => setOpen(false)}
        />
      )}
    </Card>
  );
}

// Blocked zegt waarom bijwerken hier nu niet kan, en wat je dan doet.
function Blocked({ u, id }: { u: KubeUpgrade; id: string }) {
  const cluster = useCluster(id);
  const isAdmin = useIsAdmin();
  const [reapply, setReapply] = useState(false);
  const c = cluster.data;
  if (u.behind) {
    return (
      <div className="flex flex-wrap items-center justify-between gap-3">
        <p className="text-amber-800 dark:text-amber-300">
          Een eerdere toepassing op dit cluster is niet afgemaakt, zoals een bijwerking die stopte bij een node die niet
          terugkwam. Los het probleem op die node op en pas de revisie opnieuw toe; nodes die al klaar zijn, blijven staan.
        </p>
        {isAdmin && c && (
          <Button variant="secondary" onClick={() => setReapply(true)}>
            Opnieuw toepassen…
          </Button>
        )}
        {reapply && c && (
          <ReapplyDialog
            clusterId={c.id}
            name={c.name}
            slug={c.slug}
            prod={c.environment === "prod"}
            revision={c.spec_revision}
            gitManaged={c.git_managed}
            kubernetes
            onClose={() => setReapply(false)}
          />
        )}
      </div>
    );
  }
  if (u.git_managed && u.next) {
    return (
      <p className="text-xs text-slate-500">
        Dit cluster volgt Git. Zet in <code>cluster.yaml</code> de versie van de template op {u.next.version} en keur de wijziging
        goed op de pagina{" "}
        <Link href="/gitops" className="font-medium underline">
          GitOps
        </Link>
        ; de uitrol gaat dan op dezelfde manier.
      </p>
    );
  }
  return null;
}
