"use client";

import Link from "next/link";
import { useState } from "react";
import { BackupTable, FreshnessBadge, Latest } from "@/components/backups/Backups";
import { ago, Empty, QueryState } from "@/components/inventory/bits";
import { Alert, Button, Card, Input, Label, PageHeader, Select } from "@/components/ui";
import {
  useBackups,
  useRefreshBackups,
  useSetBackupWatch,
  type BackupConnection,
  type BackupItem,
} from "@/lib/backups";
import { useIsAdmin } from "@/lib/inventory";

export default function BackupsPage() {
  const q = useBackups();
  const isAdmin = useIsAdmin();
  const [only, setOnly] = useState("");
  const items = q.data?.items ?? [];
  const nodeItems = items.filter((it) => !it.watched);
  const shown = only ? nodeItems.filter((it) => it.freshness !== "ok") : nodeItems;
  const conns = q.data?.connections ?? [];

  return (
    <div className="space-y-6">
      <PageHeader
        title="Back-ups"
        description="Hoe oud de nieuwste back-up van elke VM in Proxmox is, en welke VM's in geen back-upjob zitten. Back-ups maken blijft de taak van Proxmox."
      />
      <QueryState q={q}>
        {conns.length === 0 ? (
          <Empty>
            Nog geen Proxmox gekoppeld.{" "}
            <Link href="/proxmox" className="text-brand-600 hover:underline dark:text-brand-500">
              Koppel Proxmox
            </Link>{" "}
            om de back-ups te zien.
          </Empty>
        ) : (
          <>
            <Tiles items={items} conns={conns} />
            <Card
              title={
                <span className="flex flex-wrap items-center justify-between gap-2">
                  <span>Per node</span>
                  <Select className="w-auto text-sm font-normal" aria-label="Tonen" value={only} onChange={(e) => setOnly(e.target.value)}>
                    <option value="">Alle VM&apos;s</option>
                    <option value="problemen">Alleen te oud, ontbrekend of onbekend</option>
                  </Select>
                </span>
              }
            >
              {nodeItems.length === 0 ? (
                <p className="text-sm text-slate-500">
                  Nog geen node gekoppeld aan zijn VM in Proxmox. Dat gebeurt vanzelf als de naam overeenkomt, of bij de
                  node onder Proxmox. Een VM zonder node zet je bij Ook bewaken hieronder.
                </p>
              ) : shown.length === 0 ? (
                <p className="text-sm text-slate-500">Elke node heeft een verse back-up.</p>
              ) : (
                <BackupTable items={shown} />
              )}
            </Card>
            {conns.map((c) => (
              <ConnectionCard key={c.id} c={c} items={items.filter((it) => it.connection_id === c.id)} isAdmin={isAdmin} />
            ))}
          </>
        )}
      </QueryState>
    </div>
  );
}

function Tiles({ items, conns }: { items: BackupItem[]; conns: BackupConnection[] }) {
  const fresh = items.filter((it) => it.freshness === "ok").length;
  const bad = items.filter((it) => it.freshness === "stale" || it.freshness === "missing").length;
  const unknownCoverage = conns.some((c) => c.not_backed_up === null);
  const uncovered = conns.reduce((n, c) => n + (c.not_backed_up?.length ?? 0), 0);
  return (
    <div className="grid gap-4 sm:grid-cols-3">
      <Tile label="Verse back-up" value={`${fresh} van ${items.length}`} tone={fresh === items.length ? "good" : "plain"} />
      <Tile label="Te oud of ontbrekend" value={String(bad)} tone={bad > 0 ? "bad" : "good"} />
      <Tile
        label="Niet in een back-upjob"
        value={unknownCoverage && uncovered === 0 ? "onbekend" : String(uncovered)}
        tone={uncovered > 0 ? "warn" : "plain"}
      />
    </div>
  );
}

function Tile({ label, value, tone }: { label: string; value: string; tone: "good" | "bad" | "warn" | "plain" }) {
  const color = {
    good: "text-emerald-700 dark:text-emerald-400",
    bad: "text-red-700 dark:text-red-400",
    warn: "text-amber-700 dark:text-amber-400",
    plain: "",
  }[tone];
  return (
    <div className="rounded-lg border border-slate-200 bg-white px-4 py-3 dark:border-slate-800 dark:bg-slate-900">
      <div className="text-xs text-slate-500">{label}</div>
      <div className={`mt-1 text-2xl font-semibold tabular-nums ${color}`}>{value}</div>
    </div>
  );
}

function ConnectionCard({ c, items, isAdmin }: { c: BackupConnection; items: BackupItem[]; isAdmin: boolean }) {
  const refresh = useRefreshBackups();
  return (
    <Card
      title={
        <span className="flex flex-wrap items-center justify-between gap-2">
          <span>Proxmox: {c.name}</span>
          {isAdmin && (
            <Button variant="secondary" disabled={refresh.isPending} onClick={() => refresh.mutate(c.id)}>
              {refresh.isPending ? "Lezen…" : "Nu verversen"}
            </Button>
          )}
        </span>
      }
    >
      <div className="space-y-5 text-sm">
        <p className="text-slate-600 dark:text-slate-400">
          {c.checked_at ? `Back-ups ${ago(c.checked_at)} gelezen` : "Back-ups nog niet gelezen"}; {c.backup_count}{" "}
          {c.backup_count === 1 ? "back-up" : "back-ups"} bekend.
        </p>
        {c.error && <Alert>{c.error}</Alert>}
        <WatchSection c={c} items={items} isAdmin={isAdmin} />
        <div>
          <h3 className="mb-1 font-medium">Niet in een back-upjob</h3>
          {c.not_backed_up === null ? (
            <p className="text-slate-500">
              Onbekend: Proxmox liet dit niet lezen. Het API-token heeft daarvoor het recht Sys.Audit nodig.
            </p>
          ) : c.not_backed_up.length === 0 ? (
            <p className="text-slate-500">Elke VM en container zit in een back-upjob.</p>
          ) : (
            <ul className="flex flex-wrap gap-2">
              {c.not_backed_up.map((g) => (
                <li key={g.vmid} className="rounded border border-amber-200 bg-amber-50 px-2 py-1 dark:border-amber-900 dark:bg-amber-950">
                  {g.node ? (
                    <Link href={`/nodes/detail?id=${g.node.id}`} className="hover:underline">
                      {g.node.name}
                    </Link>
                  ) : (
                    g.name || "naamloos"
                  )}{" "}
                  <span className="text-xs text-slate-500">
                    {g.type === "lxc" ? "CT" : "VM"} {g.vmid}
                  </span>
                </li>
              ))}
            </ul>
          )}
        </div>
      </div>
    </Card>
  );
}

// WatchSection is de lijst "ook bewaken": VM's zonder node waarvan de
// back-up toch bewaakt wordt, zoals de container van ClusterForge zelf.
function WatchSection({ c, items, isAdmin }: { c: BackupConnection; items: BackupItem[]; isAdmin: boolean }) {
  const save = useSetBackupWatch(c.id);
  const [vmid, setVmid] = useState("");
  const [label, setLabel] = useState("");
  const [error, setError] = useState("");
  const watched = items.filter((it) => it.watched);

  async function put(next: BackupConnection["watch"]) {
    setError("");
    try {
      await save.mutateAsync(next);
      return true;
    } catch (err) {
      setError(err instanceof Error ? err.message : "Opslaan mislukt");
      return false;
    }
  }

  return (
    <div>
      <h3 className="mb-1 font-medium">Ook bewaken</h3>
      <p className="mb-2 text-slate-500">
        VM&apos;s zonder node waarvan de back-up toch bewaakt wordt, zoals de container van ClusterForge zelf. Ze vallen
        onder de standaard van 30 uur.
      </p>
      {c.watch.length === 0 ? (
        <p className="text-slate-500">Geen.</p>
      ) : (
        <ul className="divide-y divide-slate-100 rounded-md border border-slate-200 dark:divide-slate-800 dark:border-slate-800">
          {c.watch.map((w) => {
            const it = watched.find((x) => x.vmid === w.vmid);
            const byNode = !it && items.find((x) => x.vmid === w.vmid && x.node);
            return (
              <li key={w.vmid} className="flex flex-wrap items-center gap-x-3 gap-y-1 px-3 py-2">
                <span className="font-medium">{w.label || it?.name || "naamloos"}</span>
                <span className="text-xs text-slate-500">VM {w.vmid}</span>
                {it ? (
                  <>
                    <FreshnessBadge freshness={it.freshness} title={it.reason || undefined} />
                    <span className="text-slate-600 dark:text-slate-400">
                      {it.freshness === "ok" || it.freshness === "stale" ? <Latest v={it.latest} /> : it.reason}
                    </span>
                  </>
                ) : byNode && byNode.node ? (
                  <span className="text-xs text-slate-500">
                    Hoort bij node{" "}
                    <Link href={`/nodes/detail?id=${byNode.node.id}`} className="hover:underline">
                      {byNode.node.name}
                    </Link>{" "}
                    en wordt daar al bewaakt.
                  </span>
                ) : null}
                {isAdmin && (
                  <Button
                    variant="ghost"
                    className="ml-auto px-2 py-1 text-xs"
                    disabled={save.isPending}
                    aria-label={`VM ${w.vmid} niet meer bewaken`}
                    onClick={() => void put(c.watch.filter((x) => x.vmid !== w.vmid))}
                  >
                    Verwijderen
                  </Button>
                )}
              </li>
            );
          })}
        </ul>
      )}
      {isAdmin && (
        <form
          className="mt-3 flex flex-wrap items-end gap-2"
          onSubmit={async (e) => {
            e.preventDefault();
            const n = Number(vmid);
            if (!Number.isInteger(n) || n < 100) {
              setError("Een VMID is een getal vanaf 100.");
              return;
            }
            if (c.watch.some((w) => w.vmid === n)) {
              setError(`VM ${n} staat al in de lijst.`);
              return;
            }
            if (await put([...c.watch, { vmid: n, label: label.trim() }])) {
              setVmid("");
              setLabel("");
            }
          }}
        >
          <div className="w-28">
            <Label htmlFor={`watch-vmid-${c.id}`}>VMID</Label>
            <Input id={`watch-vmid-${c.id}`} inputMode="numeric" placeholder="105" value={vmid} onChange={(e) => setVmid(e.target.value)} />
          </div>
          <div className="min-w-48 flex-1">
            <Label htmlFor={`watch-label-${c.id}`}>Label</Label>
            <Input
              id={`watch-label-${c.id}`}
              placeholder="bijvoorbeeld clusterforge"
              maxLength={100}
              value={label}
              onChange={(e) => setLabel(e.target.value)}
            />
          </div>
          <Button type="submit" variant="secondary" disabled={save.isPending || !vmid}>
            Toevoegen
          </Button>
        </form>
      )}
      {error && (
        <div className="mt-2">
          <Alert>{error}</Alert>
        </div>
      )}
    </div>
  );
}
