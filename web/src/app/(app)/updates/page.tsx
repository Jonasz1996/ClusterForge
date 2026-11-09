"use client";

import Link from "next/link";
import { useState } from "react";
import { Modal } from "@/components/Modal";
import { ProdConfirm, prodConfirmed, useProdTotp } from "@/components/ProdConfirm";
import { Alert, Badge, Button, Card, PageHeader } from "@/components/ui";
import { ApiError } from "@/lib/api/client";
import { useIsAdmin } from "@/lib/inventory";
import { useStartUpdates, useUpdates, type UpdateNode } from "@/lib/updates";

type Target = { title: string; nodes: UpdateNode[]; clusterId?: string; prodSlug?: string };

export default function UpdatesPage() {
  const updates = useUpdates();
  const isAdmin = useIsAdmin();
  const [target, setTarget] = useState<Target | null>(null);

  const groups: { key: string; name: string; env: string; slug: string; clusterId?: string; nodes: UpdateNode[] }[] = [];
  for (const n of updates.data?.nodes ?? []) {
    const key = n.cluster_id ?? "";
    let g = groups.find((x) => x.key === key);
    if (!g) {
      g = { key, name: n.cluster_name || "Zonder cluster", env: n.cluster_environment, slug: n.cluster_slug, clusterId: n.cluster_id ?? undefined, nodes: [] };
      groups.push(g);
    }
    g.nodes.push(n);
  }

  return (
    <div className="space-y-6">
      <PageHeader
        title="Updates"
        description="Pakketten (apt) en Docker Compose-projecten bijwerken via de agents. Per cluster één node tegelijk, de VIP-houder als laatste; na elke node wacht ClusterForge tot de diensten weer draaien. De uitvoer volg je live in de taak."
      />
      {updates.isError && <Alert>Updates laden mislukte.</Alert>}
      {updates.data && groups.length === 0 && <p className="text-sm text-slate-500">Nog geen nodes met een agent.</p>}
      {groups.map((g) => {
        const prod = g.env === "prod";
        const usable = g.nodes.filter((n) => n.supported && n.online);
        return (
          <Card
            key={g.key}
            title={
              <span className="flex flex-wrap items-center justify-between gap-2">
                <span className="flex items-center gap-2">
                  {g.name}
                  {g.env && <Badge tone={prod ? "red" : "slate"}>{g.env}</Badge>}
                  {g.nodes.some((n) => n.has_database) && <Badge tone="blue">database</Badge>}
                </span>
                {isAdmin && g.clusterId && (
                  <Button
                    disabled={usable.length !== g.nodes.length}
                    title={usable.length !== g.nodes.length ? "Niet elke node is verbonden met een agent vanaf protocol " + updates.data?.run_protocol : undefined}
                    onClick={() => setTarget({ title: `${g.name} bijwerken`, nodes: g.nodes, clusterId: g.clusterId, prodSlug: prod ? g.slug : undefined })}
                  >
                    Cluster bijwerken
                  </Button>
                )}
              </span>
            }
          >
            {g.nodes.some((n) => n.has_database) && (
              <p className="mb-3 text-xs text-slate-500">
                De databasepakketten blijven bij een update staan; die werk je bij met Bijwerken op de databasekaart van het cluster.
              </p>
            )}
            <div className="overflow-x-auto">
              <table className="w-full text-sm">
                <thead className="text-left text-xs text-slate-500">
                  <tr>
                    <th className="py-1 pr-3 font-medium">Node</th>
                    <th className="py-1 pr-3 font-medium">Pakketten</th>
                    <th className="py-1 pr-3 font-medium">Docker</th>
                    <th className="py-1 pr-3 font-medium">Herstart</th>
                    <th className="py-1" />
                  </tr>
                </thead>
                <tbody>
                  {g.nodes.map((n) => (
                    <NodeRow
                      key={n.node_id}
                      n={n}
                      runProtocol={updates.data?.run_protocol ?? 0}
                      onUpdate={
                        isAdmin
                          ? () => setTarget({ title: `${n.hostname} bijwerken`, nodes: [n], prodSlug: prod ? g.slug : undefined })
                          : undefined
                      }
                    />
                  ))}
                </tbody>
              </table>
            </div>
          </Card>
        );
      })}
      {target && <UpdateDialog target={target} onClose={() => setTarget(null)} />}
    </div>
  );
}

function NodeRow({ n, runProtocol, onUpdate }: { n: UpdateNode; runProtocol: number; onUpdate?: () => void }) {
  const total = n.upgrades_total;
  return (
    <tr className="border-t border-slate-100 align-top dark:border-slate-800">
      <td className="py-2 pr-3">
        <Link href={`/nodes/detail?id=${n.node_id}`} className="font-medium hover:underline">
          {n.hostname}
        </Link>
        <span className="ml-2 inline-flex gap-1">
          {n.vip_holder && <Badge tone="blue">VIP</Badge>}
          {!n.online && <Badge tone="red">offline</Badge>}
          {n.online && !n.supported && <Badge tone="amber">agent te oud (protocol {n.protocol}, nodig {runProtocol})</Badge>}
        </span>
      </td>
      <td className="py-2 pr-3">
        {total === null ? (
          <span className="text-slate-500">onbekend</span>
        ) : total === 0 ? (
          <span className="text-green-700 dark:text-green-400">bij</span>
        ) : (
          <details>
            <summary className="cursor-pointer">
              {total} {total === 1 ? "update" : "updates"}
              {n.upgrades_security > 0 && (
                <span className="ml-2">
                  <Badge tone="red">{n.upgrades_security} beveiliging</Badge>
                </span>
              )}
            </summary>
            <p className="mt-1 max-w-md font-mono text-xs break-words text-slate-600 dark:text-slate-400">{n.packages.join(" ")}</p>
          </details>
        )}
        {n.apt_updated_at && (
          <p className="text-xs text-slate-500">lijsten van {new Date(n.apt_updated_at).toLocaleString("nl-BE")}</p>
        )}
      </td>
      <td className="py-2 pr-3">
        {!n.docker_version ? (
          <span className="text-slate-500">geen</span>
        ) : (
          <>
            <span className="text-xs text-slate-500">Docker {n.docker_version}</span>
            {n.compose_projects.length === 0 ? (
              <p className="text-xs text-slate-500">geen Compose-projecten</p>
            ) : (
              <ul className="text-xs">
                {n.compose_projects.map((p) => (
                  <li key={p.name}>
                    <span className="font-mono">{p.name}</span> <span className="text-slate-500">{p.status}</span>
                  </li>
                ))}
              </ul>
            )}
          </>
        )}
      </td>
      <td className="py-2 pr-3">{n.reboot_required ? <Badge tone="amber">nodig</Badge> : <span className="text-slate-500">nee</span>}</td>
      <td className="py-2 text-right">
        {onUpdate && (
          <Button variant="secondary" disabled={!n.online || !n.supported} onClick={onUpdate}>
            Bijwerken
          </Button>
        )}
      </td>
    </tr>
  );
}

function UpdateDialog({ target, onClose }: { target: Target; onClose: () => void }) {
  const start = useStartUpdates();
  const totp = useProdTotp();
  const hasDocker = target.nodes.some((n) => n.compose_projects.length > 0);
  const [apt, setApt] = useState(true);
  const [full, setFull] = useState(false);
  const [autoremove, setAutoremove] = useState(false);
  const [docker, setDocker] = useState(hasDocker);
  const [prune, setPrune] = useState(false);
  const [reboot, setReboot] = useState(false);
  const [confirm, setConfirm] = useState("");
  const blocked = (!apt && !docker) || (!!target.prodSlug && !prodConfirmed(target.prodSlug, confirm, totp));

  const submit = () =>
    start.mutate({
      ...(target.clusterId ? { cluster_id: target.clusterId } : { node_ids: target.nodes.map((n) => n.node_id) }),
      ...(apt ? { apt: { full, autoremove } } : {}),
      ...(docker ? { docker: { prune } } : {}),
      reboot,
      ...(target.prodSlug ? { confirm } : {}),
    });

  const order = [...target.nodes.filter((n) => !n.vip_holder), ...target.nodes.filter((n) => n.vip_holder)];
  return (
    <Modal title={target.title} onClose={onClose}>
      {start.data ? (
        <div className="space-y-3 text-sm">
          <Alert kind="success">De update is gestart.</Alert>
          {start.data.jobs.map((j) => (
            <p key={j.id}>
              <Link href={`/taken/detail?id=${j.id}`} className="font-medium text-brand-700 underline dark:text-sky-300">
                {j.title}
              </Link>{" "}
              (uitvoer live in de taak)
            </p>
          ))}
          {start.data.skipped.map((s) => (
            <Alert key={s}>{s}</Alert>
          ))}
          <div className="flex justify-end">
            <Button onClick={onClose}>Sluiten</Button>
          </div>
        </div>
      ) : (
        <div className="space-y-4 text-sm">
          <p className="text-slate-600 dark:text-slate-400">Volgorde: {order.map((n) => n.hostname + (n.vip_holder ? " (VIP)" : "")).join(" → ")}</p>
          <fieldset className="space-y-1">
            <label className="flex items-center gap-2">
              <input type="checkbox" checked={apt} onChange={(e) => setApt(e.target.checked)} /> Pakketten (apt-get upgrade)
            </label>
            {apt && (
              <div className="ml-6 space-y-1">
                <label className="flex items-center gap-2">
                  <input type="checkbox" checked={full} onChange={(e) => setFull(e.target.checked)} /> full-upgrade (pakketten mogen erbij komen of weggaan)
                </label>
                <label className="flex items-center gap-2">
                  <input type="checkbox" checked={autoremove} onChange={(e) => setAutoremove(e.target.checked)} /> daarna autoremove
                </label>
              </div>
            )}
            <label className="flex items-center gap-2">
              <input type="checkbox" checked={docker} onChange={(e) => setDocker(e.target.checked)} /> Docker Compose (pull en up -d, elk project)
            </label>
            {docker && (
              <label className="ml-6 flex items-center gap-2">
                <input type="checkbox" checked={prune} onChange={(e) => setPrune(e.target.checked)} /> daarna ongebruikte images opruimen
              </label>
            )}
            <label className="flex items-center gap-2">
              <input type="checkbox" checked={reboot} onChange={(e) => setReboot(e.target.checked)} /> Herstarten als de node erom vraagt
            </label>
          </fieldset>
          {target.prodSlug && <ProdConfirm slug={target.prodSlug} value={confirm} onChange={setConfirm} />}
          {start.error && <Alert>{start.error instanceof ApiError ? start.error.message : "Starten mislukte"}</Alert>}
          <div className="flex justify-end gap-2">
            <Button variant="secondary" onClick={onClose}>
              Annuleren
            </Button>
            <Button disabled={blocked || start.isPending} onClick={submit}>
              Bijwerken
            </Button>
          </div>
        </div>
      )}
    </Modal>
  );
}
