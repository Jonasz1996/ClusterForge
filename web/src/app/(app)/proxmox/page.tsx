"use client";

import Link from "next/link";
import { useMemo, useState } from "react";
import { JobList } from "@/components/jobs/JobList";
import { ago, Empty, formatBytes, formatDuration, QueryState, Tags, tableClass, tdClass, thClass } from "@/components/inventory/bits";
import { ConnectionForm } from "@/components/proxmox/ConnectionForm";
import { GuestActions, type ActionResult } from "@/components/proxmox/GuestActions";
import { Alert, Badge, Button, Card, Input, PageHeader, Select, cx } from "@/components/ui";
import { useCreateNode, useIsAdmin, useNodes, useUpdateNode, type Node } from "@/lib/inventory";
import {
  percent,
  useCreateProxmox,
  useDeleteProxmox,
  useJobs,
  useProxmoxConnections,
  useProxmoxResources,
  useSyncProxmox,
  useUpdateProxmox,
  vmStatusInfo,
  type ProxmoxConnection,
  type ProxmoxGuest,
  type ProxmoxHost,
  type ProxmoxStorage,
} from "@/lib/proxmox";

export default function ProxmoxPage() {
  const conns = useProxmoxConnections();
  const isAdmin = useIsAdmin();
  const create = useCreateProxmox();
  const [adding, setAdding] = useState(false);
  const enabled = conns.data?.enabled ?? true;

  return (
    <div className="space-y-6">
      <PageHeader
        title="Proxmox"
        description="Hosts, VM's en containers uit je Proxmox-omgeving, met start, stop, snapshot en migratie."
        actions={
          isAdmin &&
          enabled &&
          !adding && <Button onClick={() => setAdding(true)}>Proxmox koppelen</Button>
        }
      />
      {!enabled && (
        <Alert kind="info">
          De server heeft nog geen masterkey om het API-token van Proxmox versleuteld te bewaren. Zet{" "}
          <code className="font-mono">CF_MASTER_KEY</code> in de omgeving van de server (maak er een met{" "}
          <code className="font-mono">openssl rand -hex 32</code>) en herstart hem.
        </Alert>
      )}
      {adding && (
        <Card title="Proxmox koppelen">
          <TokenHelp />
          <ConnectionForm
            submitLabel="Koppelen"
            onCancel={() => setAdding(false)}
            onSubmit={async (v) => {
              await create.mutateAsync(v);
              setAdding(false);
            }}
          />
        </Card>
      )}
      <QueryState q={conns}>
        {conns.data && conns.data.items.length === 0 && !adding && (
          <Empty>
            Nog geen Proxmox gekoppeld.{" "}
            {isAdmin && enabled && "Maak in Proxmox een API-token voor ClusterForge en koppel het hier."}
          </Empty>
        )}
        {conns.data?.items.map((c) => <ConnectionSection key={c.id} conn={c} isAdmin={isAdmin} />)}
      </QueryState>
    </div>
  );
}

// TokenHelp legt uit hoe je in Proxmox een token met de juiste rechten maakt.
function TokenHelp() {
  return (
    <details className="mb-4 rounded-md bg-slate-50 p-3 text-sm dark:bg-slate-800/50">
      <summary className="cursor-pointer font-medium">Zo maak je het API-token in Proxmox</summary>
      <p className="mt-2 text-slate-600 dark:text-slate-400">Voer dit uit op één Proxmox-host, als root:</p>
      <pre className="mt-2 rounded bg-slate-900 p-3 font-mono text-xs whitespace-pre-wrap text-slate-100">{`pveum role add ClusterForge --privs "VM.Audit VM.PowerMgmt VM.Snapshot VM.Migrate Sys.Audit Datastore.Audit Datastore.AllocateSpace"
pveum user add clusterforge@pve --comment "ClusterForge"
pveum acl modify / --users clusterforge@pve --roles ClusterForge
pveum user token add clusterforge@pve cf --privsep 0`}</pre>
      <p className="mt-2 text-slate-600 dark:text-slate-400">
        Het laatste commando toont het secret één keer. De vingerafdruk van het certificaat staat bij Host → System → Certificates,
        of haal hem hieronder op.
      </p>
    </details>
  );
}

function ConnectionSection({ conn, isAdmin }: { conn: ProxmoxConnection; isAdmin: boolean }) {
  const res = useProxmoxResources(conn.id);
  const sync = useSyncProxmox();
  const update = useUpdateProxmox(conn.id);
  const remove = useDeleteProxmox();
  const jobs = useJobs({ proxmox_id: conn.id, limit: 8 });
  const [editing, setEditing] = useState(false);
  const [result, setResult] = useState<ActionResult | null>(null);
  const onlineHosts = (res.data?.hosts ?? []).filter((h) => h.status === "online").map((h) => h.name);

  return (
    <section className="space-y-4">
      <div className="flex flex-wrap items-start justify-between gap-3 border-b border-slate-200 pb-3 dark:border-slate-800">
        <div>
          <h2 className="text-lg font-semibold">{conn.name}</h2>
          <p className="text-sm text-slate-500">
            {conn.api_url}
            {conn.pve_version && ` · Proxmox VE ${conn.pve_version}`} · gesynchroniseerd {ago(conn.last_sync_at)}
          </p>
        </div>
        {isAdmin && !editing && (
          <div className="flex gap-2">
            <Button variant="secondary" disabled={sync.isPending} onClick={() => sync.mutate(conn.id)}>
              {sync.isPending ? "Syncen…" : "Nu syncen"}
            </Button>
            <Button variant="secondary" onClick={() => setEditing(true)}>
              Bewerken
            </Button>
            <Button
              variant="ghost"
              disabled={remove.isPending}
              onClick={() => {
                if (window.confirm(`Koppeling met ${conn.name} verwijderen? Nodes blijven bestaan, alleen hun koppeling met Proxmox verdwijnt.`))
                  remove.mutate(conn.id);
              }}
            >
              Verwijderen
            </Button>
          </div>
        )}
      </div>

      {conn.last_error && (
        <Alert>
          De laatste sync mislukte: {conn.last_error}
          {/[.!?]$/.test(conn.last_error) ? "" : "."}
          {conn.last_sync_at && <span> De gegevens hieronder zijn van {ago(conn.last_sync_at)}.</span>}
        </Alert>
      )}
      {editing && (
        <Card title={`${conn.name} bewerken`}>
          <ConnectionForm
            initial={conn}
            submitLabel="Opslaan"
            onCancel={() => setEditing(false)}
            onSubmit={async (v) => {
              await update.mutateAsync({ ...v, token_secret: v.token_secret || undefined });
              setEditing(false);
            }}
          />
        </Card>
      )}
      {result && (
        <Alert kind={result.kind === "success" ? "success" : "error"}>
          {result.kind === "success" ? (
            <>
              Taak gestart: {result.job.title}.{" "}
              <Link href={`/taken/detail?id=${result.job.id}`} className="font-medium underline">
                Volgen
              </Link>
            </>
          ) : (
            result.message
          )}
        </Alert>
      )}

      <QueryState q={res}>
        {res.data && (
          <>
            <Hosts hosts={res.data.hosts} />
            <Guests conn={conn} guests={res.data.guests} hosts={onlineHosts} isAdmin={isAdmin} onResult={setResult} />
            {res.data.storages.length > 0 && <Storages storages={res.data.storages} />}
          </>
        )}
      </QueryState>

      {(jobs.data?.length ?? 0) > 0 && (
        <Card title="Recente taken">
          <JobList jobs={jobs.data!} />
          <div className="mt-3 text-right text-sm">
            <Link href="/taken" className="text-brand-600 hover:underline dark:text-brand-500">
              Alle taken →
            </Link>
          </div>
        </Card>
      )}
    </section>
  );
}

function Bar({ pct }: { pct: number }) {
  return (
    <div className="h-1.5 rounded-full bg-slate-100 dark:bg-slate-800">
      <div
        className={cx("h-1.5 rounded-full", pct >= 90 ? "bg-red-500" : pct >= 75 ? "bg-amber-500" : "bg-brand-600")}
        style={{ width: `${Math.min(100, pct)}%` }}
      />
    </div>
  );
}

function Hosts({ hosts }: { hosts: ProxmoxHost[] }) {
  if (hosts.length === 0) return null;
  return (
    <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
      {hosts.map((h) => {
        const cpu = Math.round(h.cpu * 100);
        const mem = percent(h.mem, h.maxmem);
        return (
          <Card key={h.name}>
            <div className="flex items-center justify-between gap-2">
              <span className="font-semibold">{h.name}</span>
              <Badge tone={h.status === "online" ? "green" : "red"}>{h.status === "online" ? "Online" : "Offline"}</Badge>
            </div>
            {h.status === "online" ? (
              <div className="mt-3 space-y-2 text-xs text-slate-600 dark:text-slate-400">
                <div>
                  <div className="mb-1 flex justify-between">
                    <span>CPU · {h.maxcpu} cores</span>
                    <span className="tabular-nums">{cpu}%</span>
                  </div>
                  <Bar pct={cpu} />
                </div>
                <div>
                  <div className="mb-1 flex justify-between">
                    <span>Geheugen</span>
                    <span className="tabular-nums">
                      {formatBytes(h.mem)} van {formatBytes(h.maxmem)}
                    </span>
                  </div>
                  <Bar pct={mem} />
                </div>
                <div>Uptime {formatDuration(h.uptime)}</div>
              </div>
            ) : (
              <p className="mt-3 text-xs text-slate-500">Proxmox kan deze host niet bereiken.</p>
            )}
          </Card>
        );
      })}
    </div>
  );
}

function Guests({
  conn,
  guests,
  hosts,
  isAdmin,
  onResult,
}: {
  conn: ProxmoxConnection;
  guests: ProxmoxGuest[];
  hosts: string[];
  isAdmin: boolean;
  onResult: (r: ActionResult) => void;
}) {
  const nodes = useNodes();
  const [search, setSearch] = useState("");
  const [filter, setFilter] = useState("");
  const shown = useMemo(() => {
    const s = search.trim().toLowerCase();
    // Templates onderaan, de rest op VMID.
    const sorted = [...guests].sort((a, b) => Number(a.template) - Number(b.template) || a.vmid - b.vmid);
    return sorted.filter(
      (g) =>
        (filter === "" || (filter === "template" ? g.template : !g.template && g.status === filter)) &&
        (s === "" ||
          g.name.toLowerCase().includes(s) ||
          String(g.vmid).includes(s) ||
          g.host.includes(s) ||
          g.tags.some((t) => t.includes(s)) ||
          (g.node_hostname ?? "").toLowerCase().includes(s)),
    );
  }, [guests, search, filter]);

  return (
    <Card title={`VM's en containers (${guests.filter((g) => !g.template).length})`}>
      <div className="mb-3 flex flex-wrap gap-2">
        <Input
          className="max-w-xs"
          placeholder="Zoek op naam, VMID, host of tag"
          value={search}
          onChange={(e) => setSearch(e.target.value)}
          aria-label="Zoeken"
        />
        <Select className="w-auto" value={filter} onChange={(e) => setFilter(e.target.value)} aria-label="Filter">
          <option value="">Alles</option>
          <option value="running">Draait</option>
          <option value="stopped">Uit</option>
          <option value="template">Templates</option>
        </Select>
      </div>
      {shown.length === 0 ? (
        <Empty>Geen VM&apos;s gevonden.</Empty>
      ) : (
        <div className="overflow-x-auto">
          <table className={tableClass}>
            <thead className="border-b border-slate-200 dark:border-slate-800">
              <tr>
                <th className={thClass}>VMID</th>
                <th className={thClass}>Naam</th>
                <th className={thClass}>Host</th>
                <th className={thClass}>Status</th>
                <th className={thClass}>Gebruik</th>
                <th className={thClass}>Node</th>
                {isAdmin && <th className={thClass} />}
              </tr>
            </thead>
            <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
              {shown.map((g) => {
                const st = vmStatusInfo(g.status);
                return (
                  <tr key={g.vmid}>
                    <td className={`${tdClass} tabular-nums text-slate-500`}>{g.vmid}</td>
                    <td className={tdClass}>
                      <span className="font-medium">{g.name || "–"}</span>{" "}
                      <span className="text-xs text-slate-400">{g.type === "lxc" ? "container" : "VM"}</span>
                      {g.sandbox && (
                        <span className="ml-1" title="Tijdelijke VM van een back-upcontrole; ClusterForge start en verwijdert hem zelf">
                          <Badge tone="blue">Sandbox</Badge>
                        </span>
                      )}
                      {g.tags.length > 0 && (
                        <div className="mt-1">
                          <Tags tags={g.tags} />
                        </div>
                      )}
                    </td>
                    <td className={tdClass}>{g.host}</td>
                    <td className={tdClass}>
                      {g.template ? (
                        <Badge>Template</Badge>
                      ) : (
                        <Badge tone={st.tone}>
                          {st.label}
                          {g.lock && ` · ${g.lock}`}
                        </Badge>
                      )}
                    </td>
                    <td className={`${tdClass} text-xs whitespace-nowrap text-slate-600 tabular-nums dark:text-slate-400`}>
                      {g.status === "running" ? (
                        <>
                          {Math.round(g.cpu * 100)}% van {g.maxcpu} CPU
                          <div>
                            {formatBytes(g.mem)} / {formatBytes(g.maxmem)}
                          </div>
                        </>
                      ) : (
                        <>
                          {g.maxcpu > 0 && `${g.maxcpu} CPU · `}
                          {formatBytes(g.maxmem)}
                        </>
                      )}
                    </td>
                    <td className={tdClass}>
                      <NodeCell conn={conn} guest={g} nodes={nodes.data ?? []} isAdmin={isAdmin} />
                    </td>
                    {isAdmin && (
                      <td className={`${tdClass} text-right`}>
                        {g.sandbox ? (
                          <span className="text-xs text-slate-400">–</span>
                        ) : (
                          <GuestActions proxmoxId={conn.id} guest={g} hosts={hosts} onResult={onResult} />
                        )}
                      </td>
                    )}
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
    </Card>
  );
}

// NodeCell toont de gekoppelde node, of laat een beheerder de VM opnemen of
// koppelen aan een node met dezelfde naam.
function NodeCell({ conn, guest, nodes, isAdmin }: { conn: ProxmoxConnection; guest: ProxmoxGuest; nodes: Node[]; isAdmin: boolean }) {
  const create = useCreateNode();
  const update = useUpdateNode();
  const [error, setError] = useState<string | null>(null);
  if (guest.node_id) {
    return (
      <Link href={`/nodes/detail?id=${guest.node_id}`} className="font-medium text-brand-600 hover:underline dark:text-brand-500">
        {guest.node_hostname}
      </Link>
    );
  }
  if (!isAdmin || guest.template || guest.sandbox) return <span className="text-slate-400">–</span>;
  const link = { connection_id: conn.id, vmid: guest.vmid };
  const short = guest.name.toLowerCase();
  const match = nodes.find((n) => !n.proxmox && (n.hostname.toLowerCase() === short || n.hostname.toLowerCase().split(".")[0] === short));
  const busy = create.isPending || update.isPending;
  const run = async (fn: () => Promise<unknown>) => {
    setError(null);
    try {
      await fn();
    } catch (e) {
      setError(e instanceof Error ? e.message : "Mislukt");
    }
  };
  return (
    <div>
      {match ? (
        <Button
          variant="ghost"
          className="px-2 py-1 text-xs"
          disabled={busy}
          onClick={() => void run(() => update.mutateAsync({ id: match.id, body: { proxmox: link } }))}
        >
          Koppelen aan {match.hostname}
        </Button>
      ) : (
        <Button
          variant="ghost"
          className="px-2 py-1 text-xs"
          disabled={busy || guest.name === ""}
          onClick={() => void run(() => create.mutateAsync({ hostname: guest.name, proxmox: link }))}
        >
          Opnemen als node
        </Button>
      )}
      {error && <div className="mt-1 text-xs text-red-700 dark:text-red-300">{error}</div>}
    </div>
  );
}

function Storages({ storages }: { storages: ProxmoxStorage[] }) {
  // Gedeelde storage staat bij elke host; één keer tonen is genoeg.
  const seen = new Set<string>();
  const rows = storages.filter((s) => {
    const key = s.shared ? s.name : `${s.host}/${s.name}`;
    if (seen.has(key)) return false;
    seen.add(key);
    return true;
  });
  return (
    <Card title="Storage">
      <div className="overflow-x-auto">
        <table className={tableClass}>
          <thead className="border-b border-slate-200 dark:border-slate-800">
            <tr>
              <th className={thClass}>Naam</th>
              <th className={thClass}>Host</th>
              <th className={thClass}>Type</th>
              <th className={thClass}>Gebruik</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
            {rows.map((s) => {
              const pct = percent(s.disk, s.maxdisk);
              return (
                <tr key={`${s.host}/${s.name}`}>
                  <td className={`${tdClass} font-medium`}>{s.name}</td>
                  <td className={tdClass}>{s.shared ? <Badge tone="blue">Gedeeld</Badge> : s.host}</td>
                  <td className={tdClass}>{s.type}</td>
                  <td className={`${tdClass} w-48`}>
                    <Bar pct={pct} />
                    <div className="mt-1 text-xs text-slate-500 tabular-nums">
                      {formatBytes(s.disk)} van {formatBytes(s.maxdisk)} ({pct}%)
                    </div>
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
    </Card>
  );
}
