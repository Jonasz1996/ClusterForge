"use client";

import { Suspense, useState } from "react";
import { Empty, formatBytes, QueryState, tableClass, tdClass, thClass } from "@/components/inventory/bits";
import { Modal } from "@/components/Modal";
import { TrueNASGate, tnTime, useTrueNASRunner } from "@/components/truenas-bits";
import { Alert, Badge, Button, Card, Field, Input, PageHeader } from "@/components/ui";
import { useMe } from "@/lib/auth";
import { flatDatasets, tnNum, tnVal, useTrueNASGet, useTrueNASSnapshots, type TNDataset, type TNSnapshot, type TrueNASServer } from "@/lib/truenas";

export default function TrueNASDatasetsPage() {
  return (
    <div className="space-y-6">
      <PageHeader
        title="Datasets en snapshots"
        description="Alle datasets met gebruik, quota en snapshots. Maak een snapshot vóór je iets wijzigt, verwijder oude of rol een dataset terug."
      />
      <Suspense>
        <TrueNASGate>{(s) => <DatasetsView key={s.node_id} srv={s} />}</TrueNASGate>
      </Suspense>
    </div>
  );
}

function DatasetsView({ srv }: { srv: TrueNASServer }) {
  const q = useTrueNASGet<TNDataset[]>(srv.node_id, "pool.dataset.details", undefined, { refetch: 60_000 });
  const [open, setOpen] = useState("");
  const [filter, setFilter] = useState("");
  const me = useMe();
  const admin = me.data?.user.role === "admin";
  const runner = useTrueNASRunner(srv.node_id);
  const list = flatDatasets(q.data).filter((d) => !filter || d.name.toLowerCase().includes(filter.toLowerCase()));
  return (
    <div className="space-y-4">
      {runner.banner}
      <QueryState q={q}>
        <Card title={`Datasets · ${list.length}`}>
          <div className="mb-3 max-w-xs">
            <Input placeholder="Zoeken" value={filter} onChange={(e) => setFilter(e.target.value)} aria-label="Datasets zoeken" />
          </div>
          {list.length === 0 ? (
            <Empty>Geen datasets.</Empty>
          ) : (
            <div className="overflow-x-auto">
              <table className={tableClass}>
                <thead>
                  <tr>
                    <th className={thClass}>Dataset</th>
                    <th className={thClass}>Gebruikt</th>
                    <th className={thClass}>Vrij</th>
                    <th className={thClass}>Snapshots</th>
                    <th className={thClass}>Quota</th>
                    <th className={thClass}>Shares</th>
                    <th className={thClass} />
                  </tr>
                </thead>
                <tbody>
                  {list.map((d) => {
                    const depth = d.name.split("/").length - 1;
                    const quota = tnNum(d.quota) || tnNum(d.refquota);
                    return (
                      <tr key={d.id}>
                        <td className={tdClass}>
                          <span style={{ paddingLeft: `${depth * 14}px` }} className={depth === 0 ? "font-medium" : ""}>
                            {depth === 0 ? d.name : d.name.split("/").pop()}
                          </span>
                          {d.type === "VOLUME" && <Badge>zvol</Badge>} {d.encrypted && <Badge tone={d.locked ? "red" : "blue"}>{d.locked ? "vergrendeld" : "versleuteld"}</Badge>}
                        </td>
                        <td className={tdClass}>
                          {formatBytes(tnNum(d.used))}
                          {tnNum(d.usedbysnapshots) > 0 && <div className="text-xs text-slate-500">snapshots {formatBytes(tnNum(d.usedbysnapshots))}</div>}
                        </td>
                        <td className={tdClass}>{formatBytes(tnNum(d.available))}</td>
                        <td className={tdClass}>{d.snapshot_count ?? "–"}</td>
                        <td className={tdClass}>{quota ? formatBytes(quota) : "–"}</td>
                        <td className={tdClass}>
                          {(d.smb_shares?.length ?? 0) > 0 && <Badge tone="blue">SMB</Badge>} {(d.nfs_shares?.length ?? 0) > 0 && <Badge tone="blue">NFS</Badge>}
                        </td>
                        <td className={tdClass}>
                          <Button variant="ghost" onClick={() => setOpen(d.name)}>
                            Snapshots
                          </Button>
                        </td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </div>
          )}
        </Card>
      </QueryState>
      {open && (
        <Modal title={`Snapshots van ${open}`} onClose={() => setOpen("")} wide>
          <Snapshots id={srv.node_id} dataset={open} admin={admin} runner={runner} />
        </Modal>
      )}
    </div>
  );
}

type Runner = ReturnType<typeof useTrueNASRunner>;

function stamp() {
  const d = new Date();
  const p = (n: number) => String(n).padStart(2, "0");
  return `cf-${d.getFullYear()}${p(d.getMonth() + 1)}${p(d.getDate())}-${p(d.getHours())}${p(d.getMinutes())}`;
}

function Snapshots({ id, dataset, admin, runner }: { id: string; dataset: string; admin: boolean; runner: Runner }) {
  const q = useTrueNASSnapshots(id, dataset);
  const [name, setName] = useState(stamp);
  const [rollback, setRollback] = useState<TNSnapshot | null>(null);
  const [typed, setTyped] = useState("");
  const ns = q.data?.method.startsWith("zfs.") ? "zfs.snapshot" : "pool.snapshot";
  const items = [...(q.data?.items ?? [])].sort((a, b) => Number(b.createtxg ?? 0) - Number(a.createtxg ?? 0));
  return (
    <div className="space-y-4">
      {runner.banner}
      {admin && (
        <form
          className="flex flex-wrap items-end gap-2"
          onSubmit={(e) => {
            e.preventDefault();
            void runner.run({ method: `${ns}.create`, args: [{ dataset, name }] }).then((ok) => ok && setName(stamp()));
          }}
        >
          <Field label="Nieuwe snapshot" htmlFor="tn-snap">
            <Input id="tn-snap" value={name} onChange={(e) => setName(e.target.value)} />
          </Field>
          <Button type="submit" disabled={runner.pending || !name}>
            Snapshot maken
          </Button>
        </form>
      )}
      <QueryState q={q}>
        {items.length === 0 ? (
          <Empty>Geen snapshots.</Empty>
        ) : (
          <div className="max-h-[60vh] overflow-auto">
            <table className={tableClass}>
              <thead>
                <tr>
                  <th className={thClass}>Naam</th>
                  <th className={thClass}>Gemaakt</th>
                  <th className={thClass}>Gebruikt</th>
                  {admin && <th className={thClass} />}
                </tr>
              </thead>
              <tbody>
                {items.map((s) => {
                  const created = tnVal(s.properties?.creation);
                  return (
                    <tr key={s.id}>
                      <td className={tdClass}>{s.snapshot_name}</td>
                      <td className={tdClass}>{typeof created === "number" ? tnTime.format(new Date(created > 1e12 ? created : created * 1000)) : (created ?? "–")}</td>
                      <td className={tdClass}>{s.properties?.used ? formatBytes(tnNum(s.properties.used)) : "–"}</td>
                      {admin && (
                        <td className={tdClass}>
                          <div className="flex gap-1">
                            <Button variant="ghost" disabled={runner.pending} onClick={() => runner.run({ method: `${ns}.delete`, args: [s.id] })}>
                              Verwijderen
                            </Button>
                            <Button variant="ghost" disabled={runner.pending} onClick={() => (setRollback(s), setTyped(""))}>
                              Terugrollen
                            </Button>
                          </div>
                        </td>
                      )}
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        )}
      </QueryState>
      {rollback && (
        <div className="space-y-3 rounded-md border border-red-300 p-3 dark:border-red-800">
          <Alert kind="error">
            Terugrollen naar {rollback.snapshot_name} gooit alles weg wat na die snapshot op {dataset} veranderde, ook nieuwere snapshots. Tik{" "}
            <strong>{dataset}</strong> over om te bevestigen.
          </Alert>
          <Input value={typed} onChange={(e) => setTyped(e.target.value)} aria-label="Naam van de dataset" />
          <div className="flex gap-2">
            <Button
              variant="danger"
              disabled={typed !== dataset || runner.pending}
              onClick={() => void runner.run({ method: `${ns}.rollback`, args: [rollback.id, { force: true }], confirm: typed }).then((ok) => ok && setRollback(null))}
            >
              Terugrollen
            </Button>
            <Button variant="secondary" onClick={() => setRollback(null)}>
              Annuleren
            </Button>
          </div>
        </div>
      )}
    </div>
  );
}
