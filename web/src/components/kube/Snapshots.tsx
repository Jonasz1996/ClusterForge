"use client";

import Link from "next/link";
import { useState, type FormEvent } from "react";
import { Modal } from "@/components/Modal";
import { ago, Empty, formatBytes, tableClass, tdClass, thClass } from "@/components/inventory/bits";
import { CodeBlock, KubeError, Learn, Term } from "@/components/kube/Kube";
import { Alert, Badge, Button, Card, Field, Input } from "@/components/ui";
import { auditTimeFmt } from "@/lib/audit";
import { useCluster, useIsAdmin } from "@/lib/inventory";
import {
  useKubeRestorePlan,
  useKubeSnapshotClusters,
  useKubeSnapshots,
  useRestoreKubeSnapshot,
  useSaveKubeSnapshot,
  type KubeSnapshot,
  type KubeSnapshotCluster,
  type KubeSnapshotSaved,
  type KubeSnapshots,
} from "@/lib/kube";

const kindLabel: Record<KubeSnapshot["kind"], string> = {
  scheduled: "Gepland",
  clusterforge: "ClusterForge",
  on_demand: "Met de hand",
};

const freshBadge: Record<KubeSnapshots["freshness"], { tone: "green" | "amber" | "red" | "slate"; label: string }> = {
  ok: { tone: "green", label: "Vers" },
  stale: { tone: "amber", label: "Ouder dan een dag" },
  none: { tone: "red", label: "Geen snapshot" },
  unknown: { tone: "slate", label: "Onbekend" },
};

const dir = "/var/lib/rancher/k3s/server/db/snapshots";

// SnapshotsTab toont de etcd-snapshots van een cluster per server, met
// Snapshot nu en terugzetten in lab en test. Op prod staat het runbook.
export function SnapshotsTab({ id }: { id: string }) {
  const isAdmin = useIsAdmin();
  const cluster = useCluster(id);
  const list = useKubeSnapshots(id);
  const save = useSaveKubeSnapshot(id);
  const [saved, setSaved] = useState<KubeSnapshotSaved | null>(null);
  const [restore, setRestore] = useState<KubeSnapshot | null>(null);
  const [job, setJob] = useState<string | null>(null);
  const prod = cluster.data?.environment === "prod";
  const v = list.data;
  const init = v?.servers.find((s) => s.init);
  return (
    <div className="space-y-6">
      <Learn
        title="Wat is een etcd-snapshot?"
        commands={["sudo k3s etcd-snapshot ls", "sudo k3s etcd-snapshot save --name met-de-hand", `ls -lh ${dir}`]}
      >
        <p>
          <Term help="De database van Kubernetes: elk object staat erin">etcd</Term> is de database van Kubernetes. Elke
          Deployment, Service, ConfigMap en Secret staat erin. k3s maakt elke twaalf uur een snapshot op elke server en houdt de
          laatste vijf, in <code className="break-all">{dir}</code>.
        </p>
        <p>
          Terugzetten zet het hele cluster terug naar dat moment: wat daarna gemaakt is, is weg, en wat daarna weggehaald is,
          staat er weer. De gegevens in volumes zitten niet in een snapshot; die blijven zoals ze zijn. Daarvoor zijn de back-ups
          van de VM&apos;s in Proxmox.
        </p>
      </Learn>
      <Card
        title={
          <span className="flex flex-wrap items-center justify-between gap-2">
            <span className="flex items-center gap-2">
              etcd-snapshots
              {v && <Badge tone={freshBadge[v.freshness].tone}>{freshBadge[v.freshness].label}</Badge>}
            </span>
            {isAdmin && (
              <Button
                type="button"
                className="text-sm font-normal"
                disabled={save.isPending}
                onClick={() => {
                  setSaved(null);
                  save.mutate(undefined, { onSuccess: setSaved });
                }}
              >
                {save.isPending ? "Snapshot maken…" : "Snapshot nu"}
              </Button>
            )}
          </span>
        }
      >
        <div className="space-y-3">
          {list.error ? <KubeError error={list.error} /> : null}
          {save.error ? <KubeError error={save.error} /> : null}
          {saved && <SavedAlert saved={saved} onClose={() => setSaved(null)} />}
          {job && (
            <Alert kind="success">
              Het terugzetten loopt.{" "}
              <Link href={`/taken/detail?id=${job}`} className="font-medium underline">
                Taak volgen
              </Link>
            </Alert>
          )}
          {v?.error && (
            <Alert>
              De lijst is nu niet te lezen: {v.error}
              {v.read_at ? ` Dit is de lijst van ${ago(v.read_at)}.` : ""}
            </Alert>
          )}
          {v?.freshness === "stale" && (
            <Alert>
              De nieuwste snapshot is ouder dan een dag, terwijl k3s er elke twaalf uur een maakt. Kijk met{" "}
              <code>sudo journalctl -u k3s | grep -i snapshot</code> op een server waarom het niet lukt.
            </Alert>
          )}
          {v && v.items.length === 0 ? (
            <Empty>
              Nog geen snapshots. k3s maakt de eerste om 0 of 12 uur{isAdmin ? "; met Snapshot nu heb je er meteen een" : ""}.
            </Empty>
          ) : (
            v && <SnapshotTable v={v} admin={isAdmin && !prod} onRestore={setRestore} />
          )}
          {v && init && (
            <p className="text-xs text-slate-500">
              Terugzetten kan alleen met een snapshot op {init.hostname}: die server begon het cluster, en de andere melden zich
              daarna bij hem opnieuw aan. k3s maakt op elke server op dezelfde momenten een snapshot.
            </p>
          )}
          {v && !init && v.items.length > 0 && isAdmin && !prod && (
            <p className="text-xs text-slate-500">
              Terugzetten kan nu niet: geen agent meldt welke server het cluster begon. Is de agent op de servers ouder dan deze
              versie, werk hem dan bij met het installatiescript en --upgrade.
            </p>
          )}
        </div>
      </Card>
      {v && <Runbook v={v} prod={prod} />}
      {restore && cluster.data && (
        <RestoreDialog
          id={id}
          slug={cluster.data.slug}
          snapshot={restore}
          onClose={() => setRestore(null)}
          onStarted={(jobId) => {
            setRestore(null);
            setJob(jobId);
          }}
        />
      )}
    </div>
  );
}

function SavedAlert({ saved, onClose }: { saved: KubeSnapshotSaved; onClose: () => void }) {
  const s = saved.snapshot;
  return (
    <Alert kind="success">
      <span className="flex flex-wrap items-start justify-between gap-2">
        <span>
          {s ? (
            <>
              Snapshot <code>{s.file}</code> gemaakt op {saved.hostname}
              {s.size_bytes !== null ? `, ${formatBytes(s.size_bytes)}` : ""}.
            </>
          ) : (
            <>
              Snapshot gemaakt op {saved.hostname}; k3s meldt hem zo in de lijst.
              {saved.output.length > 0 && <span className="block font-mono text-xs">{saved.output.at(-1)}</span>}
            </>
          )}
        </span>
        <button type="button" className="text-xs underline" onClick={onClose}>
          Sluiten
        </button>
      </span>
    </Alert>
  );
}

function SnapshotTable({ v, admin, onRestore }: { v: KubeSnapshots; admin: boolean; onRestore: (s: KubeSnapshot) => void }) {
  const init = v.servers.find((s) => s.init)?.node;
  return (
    <div className="overflow-x-auto">
      <table className={tableClass}>
        <thead>
          <tr>
            <th className={thClass}>Gemaakt</th>
            <th className={thClass}>Server</th>
            <th className={thClass}>Soort</th>
            <th className={thClass}>Grootte</th>
            <th className={thClass}>Status</th>
            {admin && <th className={thClass} />}
          </tr>
        </thead>
        <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
          {v.items.map((s) => (
            <tr key={s.name}>
              <td className={tdClass}>
                <div className="whitespace-nowrap">{s.taken_at ? auditTimeFmt.format(new Date(s.taken_at)) : "onbekend"}</div>
                <div className="font-mono text-xs break-all text-slate-500">{s.file}</div>
              </td>
              <td className={tdClass}>
                <div className="whitespace-nowrap">{s.hostname ?? s.node}</div>
                {s.node === init && <div className="text-xs text-slate-500">begon het cluster</div>}
              </td>
              <td className={tdClass}>
                {kindLabel[s.kind]}
                {s.s3 && <span className="block text-xs text-slate-500">in S3</span>}
              </td>
              <td className={`${tdClass} whitespace-nowrap`}>{s.size_bytes !== null ? formatBytes(s.size_bytes) : "?"}</td>
              <td className={tdClass}>
                {s.ready ? <Badge tone="green">Klaar</Badge> : <Badge tone="red">Mislukt</Badge>}
                {s.error && <div className="text-xs text-red-700 dark:text-red-300">{s.error}</div>}
              </td>
              {admin && (
                <td className={`${tdClass} text-right`}>
                  {s.restorable ? (
                    <Button type="button" variant="secondary" className="px-2 py-1 text-xs" onClick={() => onRestore(s)}>
                      Terugzetten
                    </Button>
                  ) : (
                    // Op een andere server zegt de regel onder de tabel waarom
                    // niet; hier alleen wat bij deze snapshot hoort.
                    s.ready && s.node === init && <span className="text-xs text-slate-500">{s.restore_note}</span>
                  )}
                </td>
              )}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

// Runbook is terugzetten met de hand, zoals k3s het beschrijft: op prod de
// enige weg, en in lab en test om te zien wat ClusterForge doet.
function Runbook({ v, prod }: { v: KubeSnapshots; prod: boolean }) {
  const init = v.servers.find((s) => s.init);
  const others = v.servers.filter((s) => !s.init);
  const first = init?.hostname ?? "de eerste server (met cluster-init)";
  const file = v.items.find((s) => s.ready && !s.s3 && (!init || s.node === init.node))?.file ?? "<bestand>";
  const steps: { title: string; where: string; cmds: string[] }[] = [
    { title: "Maak eerst een snapshot van nu", where: first, cmds: ["sudo k3s etcd-snapshot save --name voor-terugzetten"] },
    {
      title: "Stop k3s op elke server",
      where: v.servers.map((s) => s.hostname).join(", ") || "elke server",
      cmds: ["sudo systemctl stop k3s"],
    },
    {
      title: "Zet de snapshot terug",
      where: first,
      cmds: [`ls -lh ${dir}`, `sudo k3s server --cluster-reset \\\n  --cluster-reset-restore-path=${dir}/${file}`],
    },
    { title: "Start k3s", where: first, cmds: ["sudo systemctl start k3s", "sudo kubectl get nodes"] },
  ];
  if (others.length > 0 || !init) {
    steps.push({
      title: "Zet de database opzij en start k3s, zodat ze zich opnieuw aanmelden",
      where: others.map((s) => s.hostname).join(", ") || "de andere servers",
      cmds: [
        "sudo mv /var/lib/rancher/k3s/server/db /var/lib/rancher/k3s/server/db-voor-terugzetten",
        "sudo mkdir -m 700 /var/lib/rancher/k3s/server/db",
        "sudo mv /var/lib/rancher/k3s/server/db-voor-terugzetten/snapshots /var/lib/rancher/k3s/server/db/",
        "sudo systemctl start k3s",
      ],
    });
  }
  return (
    <Card title={prod ? "Terugzetten op prod" : "Met de hand terugzetten"}>
      <div className="space-y-4 text-sm text-slate-700 dark:text-slate-300">
        <p>
          {prod
            ? "Op prod zet ClusterForge geen snapshot terug: het cluster ligt dan een paar minuten stil, en alles wat na de snapshot veranderde is weg. Moet het toch, doe het dan zo, als root op de servers."
            : "Dit doet Terugzetten ook, stap voor stap. Zo kun je het zelf volgen, of het doen als ClusterForge er niet bij kan."}
        </p>
        <ol className="list-decimal space-y-3 pl-5">
          {steps.map((s) => (
            <li key={s.title} className="space-y-1">
              <p>
                {s.title} <span className="text-slate-500">(op {s.where})</span>
              </p>
              <CodeBlock text={s.cmds.join("\n")} />
            </li>
          ))}
        </ol>
        <p>
          De workers melden zich vanzelf weer. Kijk tot slot met <code>sudo kubectl get nodes</code> of elke node Ready is. Gaat
          het mis, dan staat de oude database van {first} in{" "}
          <code className="break-all">/var/lib/rancher/k3s/server/db/etcd-old-…</code>.
        </p>
      </div>
    </Card>
  );
}

function RestoreDialog({
  id,
  slug,
  snapshot,
  onClose,
  onStarted,
}: {
  id: string;
  slug: string;
  snapshot: KubeSnapshot;
  onClose: () => void;
  onStarted: (jobId: string) => void;
}) {
  const plan = useKubeRestorePlan(id, snapshot.name);
  const restore = useRestoreKubeSnapshot(id);
  const [typed, setTyped] = useState("");
  const p = plan.data;
  const when = snapshot.taken_at ? auditTimeFmt.format(new Date(snapshot.taken_at)) : "het moment van de snapshot";
  const ready = p !== undefined && typed === slug && !restore.isPending;
  const send = (e: FormEvent) => {
    e.preventDefault();
    if (ready) restore.mutate({ name: snapshot.name, confirm: typed }, { onSuccess: (j) => onStarted(j.id) });
  };
  return (
    <Modal title="Snapshot terugzetten" onClose={onClose} wide>
      <form className="space-y-4 text-sm" onSubmit={send}>
        {plan.isLoading && <p className="text-slate-500">Elke server controleren…</p>}
        {plan.error ? <KubeError error={plan.error} /> : null}
        {p && (
          <>
            <p>
              Het cluster gaat terug naar <span className="font-medium">{when}</span> (<code>{snapshot.file}</code>). In deze
              volgorde:
            </p>
            <ol className="list-decimal space-y-1 pl-5">
              <li>Een snapshot van nu op {p.init}, zodat je terug kunt naar hoe het net was.</li>
              <li>k3s stoppen op {[...p.others, p.init].join(", ")}. Vanaf hier antwoordt de API niet; de pods draaien door.</li>
              <li>De snapshot terugzetten op {p.init}.</li>
              <li>k3s starten op {p.init}, en wachten tot de API antwoordt.</li>
              {p.others.length > 0 && <li>Op {p.others.join(" en ")} de database opzij zetten en k3s starten.</li>}
              <li>
                Wachten tot elke node Ready is
                {p.workers > 0 ? `; ${p.workers === 1 ? "de worker meldt" : `de ${p.workers} workers melden`} zich vanzelf` : ""}.
              </li>
            </ol>
            <Alert>
              Alles wat na {when} in Kubernetes veranderde, is daarna weg: nieuwe apps, andere images en aantallen, Secrets. De
              gegevens in volumes blijven zoals ze nu zijn.
            </Alert>
            <Field label={`Tik ${slug} om te bevestigen`} htmlFor="kube-restore-confirm">
              <Input
                id="kube-restore-confirm"
                autoComplete="off"
                spellCheck={false}
                className="font-mono"
                value={typed}
                onChange={(e) => setTyped(e.target.value.trim())}
              />
            </Field>
          </>
        )}
        {restore.error ? <KubeError error={restore.error} /> : null}
        <div className="flex justify-end gap-2">
          <Button type="button" variant="secondary" onClick={onClose}>
            Annuleren
          </Button>
          <Button type="submit" variant="danger" disabled={!ready}>
            {restore.isPending ? "Bezig…" : "Terugzetten"}
          </Button>
        </div>
      </form>
    </Modal>
  );
}

// SnapshotsCard staat bij Back-ups: per Kubernetes-cluster hoe oud de
// nieuwste etcd-snapshot is, per server.
export function SnapshotsCard() {
  const q = useKubeSnapshotClusters();
  const items = q.data ?? [];
  if (items.length === 0) return null;
  return (
    <Card title="Kubernetes: etcd-snapshots">
      <div className="space-y-3">
        <p className="text-sm text-slate-500">
          k3s maakt elke twaalf uur een snapshot van de database van Kubernetes op elke server. Een snapshot ouder dan een dag
          betekent dat het niet lukt.
        </p>
        <div className="overflow-x-auto">
          <table className={tableClass}>
            <thead>
              <tr>
                <th className={thClass}>Cluster</th>
                <th className={thClass}>Nieuwste</th>
                <th className={thClass}>Per server</th>
                <th className={thClass}>Status</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
              {items.map((c) => (
                <SnapshotClusterRow key={c.cluster_id} c={c} />
              ))}
            </tbody>
          </table>
        </div>
      </div>
    </Card>
  );
}

function SnapshotClusterRow({ c }: { c: KubeSnapshotCluster }) {
  return (
    <tr>
      <td className={tdClass}>
        <Link
          href={`/kubernetes/cluster?id=${c.cluster_id}&tab=snapshots`}
          className="font-medium text-brand-700 hover:underline dark:text-sky-300"
        >
          {c.name}
        </Link>
        <div className="text-xs text-slate-500">{c.environment}</div>
      </td>
      <td className={`${tdClass} whitespace-nowrap`}>{c.newest_at ? ago(c.newest_at) : "geen"}</td>
      <td className={tdClass}>
        {c.servers.length === 0 ? (
          <span className="text-slate-500">{c.error || "nog niet gelezen"}</span>
        ) : (
          <ul className="space-y-0.5">
            {c.servers.map((s) => (
              <li key={s.node} className="whitespace-nowrap">
                {s.hostname ?? s.node}: {s.newest_at ? ago(s.newest_at) : "geen bruikbare"}{" "}
                <span className="text-xs text-slate-500">({s.count})</span>
              </li>
            ))}
          </ul>
        )}
      </td>
      <td className={tdClass}>
        <Badge tone={freshBadge[c.freshness].tone}>{freshBadge[c.freshness].label}</Badge>
      </td>
    </tr>
  );
}
