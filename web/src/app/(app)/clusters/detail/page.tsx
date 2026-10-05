"use client";

import Link from "next/link";
import { useRouter, useSearchParams } from "next/navigation";
import { Suspense, useState, type FormEvent } from "react";
import { ClusterForm } from "@/components/inventory/ClusterForm";
import { NodeForm } from "@/components/inventory/NodeForm";
import { Empty, EnvBadge, LifecycleBadge, QueryState, Tags, tableClass, tdClass, thClass } from "@/components/inventory/bits";
import { Alert, Button, Card, Input, PageHeader, Select } from "@/components/ui";
import {
  typeLabel,
  useCluster,
  useCreateNode,
  useCreateVip,
  useDeleteCluster,
  useDeleteVip,
  useIsAdmin,
  useNodes,
  useUpdateCluster,
  useUpdateNode,
  useUpdateVip,
  type ClusterDetail,
  type Vip,
} from "@/lib/inventory";

export default function ClusterDetailPage() {
  return (
    <Suspense>
      <ClusterDetailInner />
    </Suspense>
  );
}

function ClusterDetailInner() {
  const id = useSearchParams().get("id") ?? "";
  const cluster = useCluster(id);
  const isAdmin = useIsAdmin();
  const update = useUpdateCluster(id);
  const remove = useDeleteCluster();
  const router = useRouter();
  const [editing, setEditing] = useState(false);

  if (id === "") return <Alert>Geen cluster opgegeven.</Alert>;

  return (
    <QueryState q={cluster}>
      {cluster.data && (
        <div className="space-y-6">
          <div className="text-sm">
            <Link href="/clusters" className="text-slate-500 hover:underline">
              ← Clusters
            </Link>
          </div>
          <PageHeader
            title={cluster.data.name}
            description={
              <span className="inline-flex flex-wrap items-center gap-2">
                <EnvBadge env={cluster.data.environment} />
                <span>{typeLabel(cluster.data.type)}</span>
                <span className="text-slate-400">·</span>
                <code className="text-xs">{cluster.data.slug}</code>
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
                      const c = cluster.data!;
                      if (!window.confirm(`Cluster ${c.name} verwijderen? De nodes blijven bestaan zonder cluster; de VIP's verdwijnen.`)) return;
                      await remove.mutateAsync(c.id);
                      router.push("/clusters");
                    }}
                  >
                    Verwijderen
                  </Button>
                </>
              )
            }
          />

          {editing ? (
            <Card title="Cluster bewerken">
              <ClusterForm
                initial={cluster.data}
                submitLabel="Opslaan"
                onCancel={() => setEditing(false)}
                onSubmit={async (v) => {
                  await update.mutateAsync(v);
                  setEditing(false);
                }}
              />
            </Card>
          ) : (
            <Info c={cluster.data} />
          )}

          <NodesCard c={cluster.data} isAdmin={isAdmin} />
          <VipsCard c={cluster.data} isAdmin={isAdmin} />
        </div>
      )}
    </QueryState>
  );
}

function Info({ c }: { c: ClusterDetail }) {
  const fmt = new Intl.DateTimeFormat("nl-BE", { dateStyle: "medium", timeStyle: "short" });
  const rows: [string, React.ReactNode][] = [
    ["Beschrijving", c.description || <span className="text-slate-400">Geen</span>],
    ["Owners", c.owners.length ? c.owners.map((o) => o.username).join(", ") : <span className="text-slate-400">Geen</span>],
    ["Tags", c.tags.length ? <Tags tags={c.tags} /> : <span className="text-slate-400">Geen</span>],
    ["Git-repository", c.git_repo_url ? <code className="text-xs break-all">{c.git_repo_url}</code> : <span className="text-slate-400">Geen</span>],
    ["Status", c.status === "unknown" ? <span className="text-slate-400">Onbekend tot de agent er is</span> : c.status],
    ["Laatst gewijzigd", fmt.format(new Date(c.updated_at))],
  ];
  return (
    <Card>
      <dl className="grid gap-x-6 gap-y-3 text-sm sm:grid-cols-[10rem_1fr]">
        {rows.map(([k, v]) => (
          <div key={k} className="contents">
            <dt className="text-slate-500">{k}</dt>
            <dd>{v}</dd>
          </div>
        ))}
      </dl>
    </Card>
  );
}

function NodesCard({ c, isAdmin }: { c: ClusterDetail; isAdmin: boolean }) {
  const [mode, setMode] = useState<"none" | "new" | "existing">("none");
  const createNode = useCreateNode();
  const updateNode = useUpdateNode();
  const allNodes = useNodes();
  const [pick, setPick] = useState("");
  const [error, setError] = useState<string | null>(null);
  const loose = (allNodes.data ?? []).filter((n) => n.cluster_id === null);

  async function attach(e: FormEvent) {
    e.preventDefault();
    setError(null);
    try {
      await updateNode.mutateAsync({ id: pick, body: { cluster_id: c.id } });
      setPick("");
      setMode("none");
    } catch (err) {
      setError(err instanceof Error ? err.message : "Toevoegen mislukt");
    }
  }

  return (
    <Card
      title={
        <span className="flex items-center justify-between gap-2">
          <span>Nodes</span>
          {isAdmin && mode === "none" && (
            <span className="flex gap-2">
              <Button variant="secondary" onClick={() => setMode("existing")}>
                Bestaande toevoegen
              </Button>
              <Button variant="secondary" onClick={() => setMode("new")}>
                Nieuwe node
              </Button>
            </span>
          )}
        </span>
      }
    >
      <div className="space-y-4">
        {mode === "new" && (
          <div className="rounded-md border border-slate-200 p-4 dark:border-slate-800">
            <NodeForm
              clusterId={c.id}
              submitLabel="Node aanmaken"
              onCancel={() => setMode("none")}
              onSubmit={async (v) => {
                await createNode.mutateAsync(v);
                setMode("none");
              }}
            />
          </div>
        )}
        {mode === "existing" && (
          <form onSubmit={attach} className="flex flex-wrap items-end gap-2 rounded-md border border-slate-200 p-4 dark:border-slate-800">
            {error && <Alert>{error}</Alert>}
            {loose.length === 0 ? (
              <p className="text-sm text-slate-500">Er zijn geen nodes zonder cluster.</p>
            ) : (
              <Select className="w-auto" aria-label="Node" required value={pick} onChange={(e) => setPick(e.target.value)}>
                <option value="">Kies een node zonder cluster</option>
                {loose.map((n) => (
                  <option key={n.id} value={n.id}>
                    {n.hostname}
                  </option>
                ))}
              </Select>
            )}
            {loose.length > 0 && (
              <Button type="submit" disabled={pick === "" || updateNode.isPending}>
                Toevoegen
              </Button>
            )}
            <Button type="button" variant="secondary" onClick={() => setMode("none")}>
              Annuleren
            </Button>
          </form>
        )}
        {c.nodes.length === 0 ? (
          <Empty>Dit cluster heeft nog geen nodes.</Empty>
        ) : (
          <div className="overflow-x-auto">
            <table className={tableClass}>
              <thead className="border-b border-slate-200 dark:border-slate-800">
                <tr>
                  <th className={thClass}>Hostname</th>
                  <th className={thClass}>Rol</th>
                  <th className={thClass}>IP-adres</th>
                  <th className={thClass}>Lifecycle</th>
                  {isAdmin && <th className={thClass} />}
                </tr>
              </thead>
              <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
                {c.nodes.map((n) => (
                  <tr key={n.id}>
                    <td className={tdClass}>
                      <Link href={`/nodes/detail?id=${n.id}`} className="font-medium text-brand-700 hover:underline dark:text-sky-300">
                        {n.hostname}
                      </Link>
                    </td>
                    <td className={tdClass}>{n.role || <span className="text-slate-400">–</span>}</td>
                    <td className={`${tdClass} font-mono text-xs`}>{n.primary_ip ?? <span className="text-slate-400">–</span>}</td>
                    <td className={tdClass}>
                      <LifecycleBadge lifecycle={n.lifecycle} />
                    </td>
                    {isAdmin && (
                      <td className={`${tdClass} text-right`}>
                        <Button
                          variant="ghost"
                          className="px-2 py-1 text-xs"
                          onClick={() => {
                            if (window.confirm(`${n.hostname} uit dit cluster halen? De node zelf blijft bestaan.`))
                              void updateNode.mutateAsync({ id: n.id, body: { cluster_id: null } });
                          }}
                        >
                          Uit cluster
                        </Button>
                      </td>
                    )}
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>
    </Card>
  );
}

function VipsCard({ c, isAdmin }: { c: ClusterDetail; isAdmin: boolean }) {
  const [adding, setAdding] = useState(false);
  const create = useCreateVip(c.id);
  return (
    <Card
      title={
        <span className="flex items-center justify-between gap-2">
          <span>VIP&apos;s</span>
          {isAdmin && !adding && (
            <Button variant="secondary" onClick={() => setAdding(true)}>
              VIP toevoegen
            </Button>
          )}
        </span>
      }
    >
      <div className="space-y-4">
        {adding && (
          <VipForm
            submitLabel="Toevoegen"
            onCancel={() => setAdding(false)}
            onSubmit={async (v) => {
              await create.mutateAsync(v);
              setAdding(false);
            }}
          />
        )}
        {c.vips.length === 0 ? (
          <Empty>Geen VIP&apos;s. Een VIP is een virtueel IP-adres dat tussen de nodes verhuist.</Empty>
        ) : (
          <div className="overflow-x-auto">
            <table className={tableClass}>
              <thead className="border-b border-slate-200 dark:border-slate-800">
                <tr>
                  <th className={thClass}>Adres</th>
                  <th className={thClass}>Interface</th>
                  <th className={thClass}>VRID</th>
                  <th className={thClass}>Huidige eigenaar</th>
                  <th className={thClass}>Beschrijving</th>
                  {isAdmin && <th className={thClass} />}
                </tr>
              </thead>
              <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
                {c.vips.map((v) => (
                  <VipRow key={v.id} v={v} isAdmin={isAdmin} />
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>
    </Card>
  );
}

function VipRow({ v, isAdmin }: { v: Vip; isAdmin: boolean }) {
  const [editing, setEditing] = useState(false);
  const update = useUpdateVip();
  const remove = useDeleteVip();
  if (editing) {
    return (
      <tr>
        <td colSpan={6} className="py-3">
          <VipForm
            initial={v}
            submitLabel="Opslaan"
            onCancel={() => setEditing(false)}
            onSubmit={async (body) => {
              await update.mutateAsync({ id: v.id, body });
              setEditing(false);
            }}
          />
        </td>
      </tr>
    );
  }
  return (
    <tr>
      <td className={`${tdClass} font-mono text-xs`}>{v.address}</td>
      <td className={tdClass}>{v.interface || <span className="text-slate-400">–</span>}</td>
      <td className={`${tdClass} tabular-nums`}>{v.vrid ?? <span className="text-slate-400">–</span>}</td>
      <td className={tdClass}>{v.owner_hostname ?? <span className="text-slate-400">Onbekend</span>}</td>
      <td className={tdClass}>{v.description}</td>
      {isAdmin && (
        <td className={`${tdClass} whitespace-nowrap text-right`}>
          <Button variant="ghost" className="px-2 py-1 text-xs" onClick={() => setEditing(true)}>
            Bewerken
          </Button>
          <Button
            variant="ghost"
            className="px-2 py-1 text-xs text-red-600!"
            onClick={() => {
              if (window.confirm(`VIP ${v.address} verwijderen?`)) void remove.mutateAsync(v.id);
            }}
          >
            Verwijderen
          </Button>
        </td>
      )}
    </tr>
  );
}

function VipForm({
  initial,
  submitLabel,
  onSubmit,
  onCancel,
}: {
  initial?: Vip;
  submitLabel: string;
  onSubmit: (v: { address: string; interface: string; vrid: number | null; description: string }) => Promise<unknown>;
  onCancel: () => void;
}) {
  const [address, setAddress] = useState(initial?.address ?? "");
  const [iface, setIface] = useState(initial?.interface ?? "");
  const [vrid, setVrid] = useState(initial?.vrid?.toString() ?? "");
  const [description, setDescription] = useState(initial?.description ?? "");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  async function submit(e: FormEvent) {
    e.preventDefault();
    setError(null);
    setBusy(true);
    try {
      await onSubmit({ address, interface: iface, vrid: vrid === "" ? null : Number(vrid), description });
    } catch (err) {
      setError(err instanceof Error ? err.message : "Opslaan mislukt");
    } finally {
      setBusy(false);
    }
  }

  return (
    <form onSubmit={submit} className="space-y-3">
      {error && <Alert>{error}</Alert>}
      <div className="grid gap-2 sm:grid-cols-[1fr_8rem_6rem_1fr]">
        <Input aria-label="Adres" required placeholder="10.0.10.100" value={address} onChange={(e) => setAddress(e.target.value)} />
        <Input aria-label="Interface" placeholder="eth0" maxLength={15} value={iface} onChange={(e) => setIface(e.target.value)} />
        <Input aria-label="VRID" type="number" min={1} max={255} placeholder="VRID" value={vrid} onChange={(e) => setVrid(e.target.value)} />
        <Input aria-label="Beschrijving" placeholder="Beschrijving" value={description} onChange={(e) => setDescription(e.target.value)} />
      </div>
      <div className="flex gap-2">
        <Button type="submit" disabled={busy}>
          {busy ? "Opslaan…" : submitLabel}
        </Button>
        <Button type="button" variant="secondary" onClick={onCancel}>
          Annuleren
        </Button>
      </div>
    </form>
  );
}
