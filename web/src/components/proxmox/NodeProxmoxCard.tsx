"use client";

import Link from "next/link";
import { useState } from "react";
import { formatDuration } from "@/components/inventory/bits";
import { GuestActions, type ActionResult } from "@/components/proxmox/GuestActions";
import { Alert, Badge, Button, Card, Select } from "@/components/ui";
import { useUpdateNode, type Node } from "@/lib/inventory";
import { useProxmoxConnections, useProxmoxResources, useVmSnapshots, vmStatusInfo } from "@/lib/proxmox";

const dlClass = "grid grid-cols-[9rem_1fr] gap-x-4 gap-y-2.5 text-sm";
const dtClass = "text-slate-500";
const snapFmt = new Intl.DateTimeFormat("nl-BE", { dateStyle: "short", timeStyle: "short" });

// NodeProxmoxCard toont de VM achter een node met zijn acties, of laat een
// beheerder de node aan een VM koppelen.
export function NodeProxmoxCard({ node, isAdmin }: { node: Node; isAdmin: boolean }) {
  const p = node.proxmox;
  const res = useProxmoxResources(p?.connection_id ?? "");
  const update = useUpdateNode();
  const [result, setResult] = useState<ActionResult | null>(null);
  const [showSnaps, setShowSnaps] = useState(false);
  const snaps = useVmSnapshots(p?.connection_id ?? "", p?.vmid ?? 0, showSnaps && !!p?.found);

  if (!p) return isAdmin ? <LinkCard node={node} /> : null;

  const guest = res.data?.guests.find((g) => g.vmid === p.vmid);
  const hosts = (res.data?.hosts ?? []).filter((h) => h.status === "online").map((h) => h.name);
  const st = vmStatusInfo(p.status);
  return (
    <Card title="Proxmox">
      <dl className={dlClass}>
        <dt className={dtClass}>Omgeving</dt>
        <dd>
          <Link href="/proxmox" className="hover:underline">
            {p.connection_name}
          </Link>
        </dd>
        <dt className={dtClass}>{p.type === "lxc" ? "Container" : "VM"}</dt>
        <dd>
          {p.vmid}
          {p.name && p.name !== node.hostname && <span className="text-slate-500"> · {p.name}</span>}
        </dd>
        {p.found ? (
          <>
            <dt className={dtClass}>Host</dt>
            <dd>{p.host}</dd>
            <dt className={dtClass}>Status</dt>
            <dd>
              <Badge tone={st.tone}>{st.label}</Badge>
              {p.status === "running" && p.uptime > 0 && <span className="text-slate-500"> · {formatDuration(p.uptime)}</span>}
            </dd>
          </>
        ) : (
          <dd className="col-span-2">
            <Alert kind="info">Deze VM zat niet in de laatste sync. Is hij verwijderd of heeft het token er geen rechten op?</Alert>
          </dd>
        )}
      </dl>
      {result && (
        <div className="mt-4">
          <Alert kind={result.kind === "success" ? "success" : "error"}>
            {result.kind === "success" ? (
              <>
                Taak gestart.{" "}
                <Link href={`/taken/detail?id=${result.job.id}`} className="font-medium underline">
                  Volgen
                </Link>
              </>
            ) : (
              result.message
            )}
          </Alert>
        </div>
      )}
      {isAdmin && guest && (
        <div className="mt-4">
          <GuestActions proxmoxId={p.connection_id} guest={guest} hosts={hosts} onResult={setResult} layout="buttons" />
        </div>
      )}
      {p.found && (
        <div className="mt-4 border-t border-slate-100 pt-3 text-sm dark:border-slate-800">
          {!showSnaps ? (
            <button type="button" className="text-brand-600 hover:underline dark:text-brand-500" onClick={() => setShowSnaps(true)}>
              Snapshots tonen
            </button>
          ) : snaps.isLoading ? (
            <span className="text-slate-500">Snapshots laden…</span>
          ) : snaps.isError ? (
            <span className="text-red-700 dark:text-red-300">{snaps.error.message}</span>
          ) : (snaps.data?.length ?? 0) === 0 ? (
            <span className="text-slate-500">Geen snapshots.</span>
          ) : (
            <ul className="space-y-1">
              {snaps.data!.map((s) => (
                <li key={s.name} className="flex justify-between gap-2">
                  <span>
                    <span className="font-mono text-xs">{s.name}</span>
                    {s.vmstate && <span className="text-xs text-slate-500"> · met geheugen</span>}
                    {s.description && <span className="block text-xs text-slate-500">{s.description}</span>}
                  </span>
                  <span className="text-xs whitespace-nowrap text-slate-500">{s.time ? snapFmt.format(new Date(s.time)) : ""}</span>
                </li>
              ))}
            </ul>
          )}
        </div>
      )}
      {isAdmin && (
        <div className="mt-4">
          <Button
            variant="ghost"
            className="px-2 py-1 text-xs"
            disabled={update.isPending}
            onClick={() => {
              if (window.confirm("Koppeling met deze VM verwijderen? De VM zelf blijft ongemoeid."))
                update.mutate({ id: node.id, body: { proxmox: null } });
            }}
          >
            Ontkoppelen
          </Button>
        </div>
      )}
    </Card>
  );
}

// LinkCard laat een beheerder een node aan een VM koppelen.
function LinkCard({ node }: { node: Node }) {
  const conns = useProxmoxConnections();
  const [connId, setConnId] = useState("");
  const first = conns.data?.items[0]?.id ?? "";
  const active = connId || first;
  const res = useProxmoxResources(active);
  const update = useUpdateNode();
  const [vmid, setVmid] = useState("");
  const [error, setError] = useState<string | null>(null);

  if (!conns.data || conns.data.items.length === 0) return null;
  const free = (res.data?.guests ?? []).filter((g) => !g.node_id && !g.template);
  const suggestion = free.find((g) => g.name.toLowerCase() === node.hostname.toLowerCase().split(".")[0]);
  const chosen = vmid || (suggestion ? String(suggestion.vmid) : "");

  return (
    <Card title="Proxmox">
      <p className="mb-3 text-sm text-slate-600 dark:text-slate-400">
        Is deze node een VM of container in Proxmox? Koppel hem, dan zie je de VM hier en kun je hem starten, stoppen of migreren.
      </p>
      {error && (
        <div className="mb-3">
          <Alert>{error}</Alert>
        </div>
      )}
      <div className="flex flex-wrap gap-2">
        {conns.data.items.length > 1 && (
          <Select className="w-auto" value={active} onChange={(e) => setConnId(e.target.value)} aria-label="Omgeving">
            {conns.data.items.map((c) => (
              <option key={c.id} value={c.id}>
                {c.name}
              </option>
            ))}
          </Select>
        )}
        <Select className="w-auto min-w-48" value={chosen} onChange={(e) => setVmid(e.target.value)} aria-label="VM">
          <option value="">Kies een VM…</option>
          {free.map((g) => (
            <option key={g.vmid} value={g.vmid}>
              {g.vmid} · {g.name}
            </option>
          ))}
        </Select>
        <Button
          variant="secondary"
          disabled={chosen === "" || update.isPending}
          onClick={async () => {
            setError(null);
            try {
              await update.mutateAsync({ id: node.id, body: { proxmox: { connection_id: active, vmid: Number(chosen) } } });
            } catch (e) {
              setError(e instanceof Error ? e.message : "Koppelen mislukt");
            }
          }}
        >
          Koppelen
        </Button>
      </div>
    </Card>
  );
}
