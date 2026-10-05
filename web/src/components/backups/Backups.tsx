"use client";

import Link from "next/link";
import { useState } from "react";
import { Alert, Badge, Button, Card, Input, Label } from "@/components/ui";
import { formatBytes, tableClass, tdClass, thClass } from "@/components/inventory/bits";
import {
  backupAge,
  backupTimeFmt,
  freshnessInfo,
  useBackupPolicy,
  useBackups,
  useNodeBackups,
  useUpdateBackupPolicy,
  type BackupFreshness,
  type BackupItem,
  type BackupVolume,
} from "@/lib/backups";

const linkClass = "text-brand-600 hover:underline dark:text-brand-500";
const none = <span className="text-slate-400">–</span>;

export function FreshnessBadge({ freshness, title }: { freshness: BackupFreshness; title?: string }) {
  const f = freshnessInfo[freshness];
  return (
    <span title={title}>
      <Badge tone={f.tone}>{f.label}</Badge>
    </span>
  );
}

// BackupBadge is de kleine badge in de lijsten; zonder bewaakte VM niets.
export function BackupBadge({ freshness }: { freshness?: BackupFreshness }) {
  if (!freshness) return null;
  const f = freshnessInfo[freshness];
  return (
    <span title={`Back-up: ${f.label.toLowerCase()}`}>
      <Badge tone={f.tone}>{freshness === "ok" ? "Back-up vers" : `Back-up: ${f.label.toLowerCase()}`}</Badge>
    </span>
  );
}

// Verify toont de verificatie van Proxmox Backup Server.
export function Verify({ v }: { v: BackupVolume }) {
  if (v.verify === "ok") return <Badge tone="green">PBS geverifieerd</Badge>;
  if (v.verify === "failed") return <Badge tone="red">PBS-verificatie mislukt</Badge>;
  return null;
}

// Latest toont de nieuwste back-up: leeftijd, tijdstip, storage en grootte.
export function Latest({ v }: { v: BackupVolume | null }) {
  if (!v) return none;
  return (
    <span className="inline-flex flex-wrap items-center gap-x-2 gap-y-1">
      <span title={backupTimeFmt.format(new Date(v.time))}>{backupAge(v.time)} oud</span>
      <span className="text-xs text-slate-500">
        {v.storage} · {formatBytes(v.size)}
      </span>
      <Verify v={v} />
    </span>
  );
}

// NodeBackupsCard toont op de nodepagina de stand en de back-ups van de VM.
export function NodeBackupsCard({ nodeId }: { nodeId: string }) {
  const q = useNodeBackups(nodeId);
  const it = q.data?.item;
  if (!q.data || !it) return null;
  return (
    <Card
      title={
        <span className="flex flex-wrap items-center justify-between gap-2">
          <span className="flex items-center gap-2">
            Back-ups <FreshnessBadge freshness={it.freshness} />
          </span>
          <Link href="/back-ups" className={`text-sm font-normal ${linkClass}`}>
            Alle back-ups
          </Link>
        </span>
      }
    >
      {it.reason && <p className="mb-3 text-sm text-slate-600 dark:text-slate-400">{it.reason}</p>}
      {q.data.backups.length === 0 ? (
        <p className="text-sm text-slate-500">Proxmox heeft geen back-ups van VM {it.vmid}.</p>
      ) : (
        <div className="overflow-x-auto">
          <table className={tableClass}>
            <thead className="border-b border-slate-200 dark:border-slate-800">
              <tr>
                <th className={thClass}>Gemaakt</th>
                <th className={thClass}>Leeftijd</th>
                <th className={thClass}>Storage</th>
                <th className={thClass}>Grootte</th>
                <th className={thClass}>Notities</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
              {q.data.backups.slice(0, 20).map((b) => (
                <tr key={b.volid}>
                  <td className={`${tdClass} whitespace-nowrap tabular-nums`}>{backupTimeFmt.format(new Date(b.time))}</td>
                  <td className={`${tdClass} whitespace-nowrap`}>{backupAge(b.time)}</td>
                  <td className={tdClass}>
                    <span className="inline-flex flex-wrap items-center gap-2">
                      {b.storage}
                      <Verify v={b} />
                      {b.protected && <Badge>Beschermd</Badge>}
                    </span>
                  </td>
                  <td className={`${tdClass} whitespace-nowrap tabular-nums`}>{formatBytes(b.size)}</td>
                  <td className={`${tdClass} break-words text-slate-600 dark:text-slate-400`}>{b.notes || none}</td>
                </tr>
              ))}
            </tbody>
          </table>
          {q.data.backups.length > 20 && (
            <p className="mt-2 text-xs text-slate-500">De 20 nieuwste van {q.data.backups.length} back-ups.</p>
          )}
        </div>
      )}
      <p className="mt-3 text-xs text-slate-500">
        Een back-up mag hoogstens {it.max_age_hours} uur oud zijn. Back-ups maken en opruimen doen de back-upjobs van
        Proxmox.
      </p>
    </Card>
  );
}

// ClusterBackupsCard toont op de clusterpagina de stand per node en het
// back-upbeleid van het cluster.
export function ClusterBackupsCard({ clusterId, isAdmin }: { clusterId: string; isAdmin: boolean }) {
  const q = useBackups();
  const policy = useBackupPolicy(clusterId);
  const [editing, setEditing] = useState(false);
  const items = (q.data?.items ?? []).filter((it) => it.cluster?.id === clusterId);
  if (items.length === 0) return null;
  return (
    <Card
      title={
        <span className="flex flex-wrap items-center justify-between gap-2">
          <span>Back-ups</span>
          <Link href="/back-ups" className={`text-sm font-normal ${linkClass}`}>
            Alle back-ups
          </Link>
        </span>
      }
    >
      <BackupTable items={items} showCluster={false} />
      {policy.data &&
        (editing ? (
          <PolicyForm clusterId={clusterId} maxAge={policy.data.max_age_hours} onDone={() => setEditing(false)} />
        ) : (
          <p className="mt-3 flex flex-wrap items-center gap-2 text-xs text-slate-500">
            <span>
              De nieuwste back-up van elke node mag hoogstens {policy.data.max_age_hours} uur oud zijn
              {policy.data.default && " (standaard)"}.
            </span>
            {isAdmin && (
              <Button variant="ghost" className="px-2 py-1 text-xs" onClick={() => setEditing(true)}>
                Wijzigen
              </Button>
            )}
          </p>
        ))}
    </Card>
  );
}

function PolicyForm({ clusterId, maxAge, onDone }: { clusterId: string; maxAge: number; onDone: () => void }) {
  const update = useUpdateBackupPolicy(clusterId);
  const [value, setValue] = useState(String(maxAge));
  const [error, setError] = useState("");
  return (
    <form
      className="mt-3 space-y-3 rounded-md bg-slate-50 p-3 dark:bg-slate-800/50"
      onSubmit={async (e) => {
        e.preventDefault();
        setError("");
        const n = Number(value);
        if (!Number.isInteger(n) || n < 1 || n > 720) {
          setError("Geef een aantal uren van 1 tot 720.");
          return;
        }
        try {
          await update.mutateAsync(n);
          onDone();
        } catch (err) {
          setError(err instanceof Error ? err.message : "Opslaan mislukt");
        }
      }}
    >
      <div className="max-w-xs">
        <Label htmlFor={`maxage-${clusterId}`}>Maximale leeftijd in uren</Label>
        <Input id={`maxage-${clusterId}`} type="number" min={1} max={720} value={value} onChange={(e) => setValue(e.target.value)} />
        <p className="mt-1 text-xs text-slate-500">30 uur past bij een dagelijkse back-upjob.</p>
      </div>
      {error && <Alert>{error}</Alert>}
      <div className="flex gap-2">
        <Button type="submit" disabled={update.isPending}>
          Opslaan
        </Button>
        <Button type="button" variant="ghost" onClick={onDone}>
          Annuleren
        </Button>
      </div>
    </form>
  );
}

// BackupTable is één regel per bewaakte VM.
export function BackupTable({ items, showCluster = true }: { items: BackupItem[]; showCluster?: boolean }) {
  return (
    <div className="overflow-x-auto">
      <table className={tableClass}>
        <thead className="border-b border-slate-200 dark:border-slate-800">
          <tr>
            <th className={thClass}>Node</th>
            {showCluster && <th className={thClass}>Cluster</th>}
            <th className={thClass}>Stand</th>
            <th className={thClass}>Nieuwste back-up</th>
            <th className={`${thClass} text-right`}>Aantal</th>
          </tr>
        </thead>
        <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
          {items.map((it) => (
            <tr key={`${it.connection_id}/${it.vmid}`}>
              <td className={tdClass}>
                {it.node ? (
                  <Link href={`/nodes/detail?id=${it.node.id}`} className={linkClass}>
                    {it.node.name}
                  </Link>
                ) : (
                  <span>{it.label || it.name || "naamloos"}</span>
                )}
                <div className="text-xs text-slate-500">
                  VM {it.vmid}
                  {it.watched && " · ook bewaakt"}
                </div>
              </td>
              {showCluster && (
                <td className={tdClass}>
                  {it.cluster ? (
                    <Link href={`/clusters/detail?id=${it.cluster.id}`} className="hover:underline">
                      {it.cluster.name}
                    </Link>
                  ) : (
                    none
                  )}
                </td>
              )}
              <td className={tdClass}>
                <FreshnessBadge freshness={it.freshness} title={it.reason || undefined} />
                {it.freshness !== "ok" && it.reason && (
                  <div className="mt-1 max-w-xs text-xs text-slate-500">{it.reason}</div>
                )}
              </td>
              <td className={tdClass}>
                <Latest v={it.latest} />
              </td>
              <td className={`${tdClass} text-right tabular-nums`}>{it.count}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
