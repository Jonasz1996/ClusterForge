"use client";

import { useState } from "react";
import type { GuestCtx } from "@/app/(app)/proxmox/gast/page";
import { formatBytes } from "@/components/inventory/bits";
import { holds, useNodeBits } from "@/components/pve/CreateGuest";
import { Check, FormModal } from "@/components/pve/guest/common";
import { Alert, Field, Input, Select } from "@/components/ui";
import { usePveGet } from "@/lib/pve";

type MigratePre = {
  running?: number | boolean;
  allowed_nodes?: string[];
  not_allowed_nodes?: Record<string, { unavailable_storages?: string[]; blocking_ha_resources?: unknown[] }>;
  local_disks?: { volid: string; size?: number; cdrom?: number; is_unused?: number }[];
  local_resources?: string[];
  "mapped-resources"?: string[];
};

// MigrateDialog verhuist een gast naar een andere host van hetzelfde cluster.
export function MigrateDialog({ ctx, onClose }: { ctx: GuestCtx; onClose: () => void }) {
  const isVM = ctx.type === "qemu";
  const pre = usePveGet<MigratePre>(ctx.dc.id, `${ctx.path}/migrate`, { enabled: isVM, refetch: false });
  const others = ctx.dc.hosts.filter((h) => h.pve_node !== ctx.node);
  const allowed = isVM && pre.data?.allowed_nodes ? others.filter((h) => pre.data?.allowed_nodes?.includes(h.pve_node)) : others;
  const [target, setTarget] = useState("");
  const t = target || allowed.find((h) => h.online)?.pve_node || "";
  const bits = useNodeBits(ctx.dc.id, t);
  const [storage, setStorage] = useState("");
  const [restart, setRestart] = useState(true);
  const localDisks = (pre.data?.local_disks ?? []).filter((d) => !d.cdrom);
  const local = pre.data?.local_resources ?? [];
  const blocked = t ? pre.data?.not_allowed_nodes?.[t] : undefined;
  const store = bits.storages.filter((s) => holds(s, isVM ? "images" : "rootdir"));

  const error =
    local.length > 0
      ? `Deze VM gebruikt hardware van de host (${local.join(", ")}); verhuizen kan pas als die doorgegeven apparaten weg zijn (tabblad Passthrough).`
      : !t
        ? "Geen andere host in dit cluster om naartoe te verhuizen."
      : blocked?.unavailable_storages?.length && !storage
        ? `Op ${t} ontbreekt storage ${blocked.unavailable_storages.join(", ")}; kies een storage op de doelhost.`
        : !isVM && ctx.running && !restart
          ? "Een draaiende CT kan alleen verhuizen met een korte herstart."
          : null;

  const submit = () => {
    const params: Record<string, string> = { target: t };
    if (isVM) {
      if (ctx.running) params.online = "1";
      if (localDisks.length > 0) params["with-local-disks"] = "1";
      if (storage) params.targetstorage = storage;
    } else {
      if (ctx.running) {
        params.restart = "1";
        params.timeout = "120";
      }
      if (storage) params["target-storage"] = storage;
    }
    void ctx.run({ method: "create", path: `${ctx.path}/migrate`, params }).then((ok) => ok && onClose());
  };

  return (
    <FormModal title="Naar een andere host verhuizen" onClose={onClose} onSubmit={submit} submitLabel="Verhuizen" disabled={ctx.pending || pre.isLoading} error={error}>
      <Field label="Naar host" htmlFor="mg-target">
        <Select id="mg-target" value={t} onChange={(e) => setTarget(e.target.value)}>
          {others.map((h) => (
            <option key={h.pve_node} value={h.pve_node} disabled={!h.online || !allowed.includes(h)}>
              {h.pve_node}
              {!h.online ? " (offline)" : !allowed.includes(h) ? " (kan niet)" : ""}
            </option>
          ))}
        </Select>
      </Field>
      {(localDisks.length > 0 || !isVM) && (
        <Field label="Storage op de doelhost" htmlFor="mg-st" hint="Leeg: dezelfde storage-naam als nu">
          <Select id="mg-st" value={storage} onChange={(e) => setStorage(e.target.value)}>
            <option value="">zelfde als nu</option>
            {store.map((s) => (
              <option key={s.storage} value={s.storage}>
                {s.storage}
                {s.avail ? ` (${formatBytes(s.avail)} vrij)` : ""}
              </option>
            ))}
          </Select>
        </Field>
      )}
      {isVM && localDisks.length > 0 && (
        <Alert kind="info">
          {localDisks.length === 1 ? "Eén lokale schijf wordt" : `${localDisks.length} lokale schijven worden`} mee gekopieerd (
          {formatBytes(localDisks.reduce((a, d) => a + (d.size ?? 0), 0))}); dat kan even duren.
        </Alert>
      )}
      {isVM && ctx.running && local.length === 0 && <p className="text-sm text-slate-500">De VM blijft draaien tijdens het verhuizen (live).</p>}
      {!isVM && ctx.running && (
        <Check id="mg-restart" checked={restart} onChange={setRestart}>
          De CT kort stoppen en op de nieuwe host weer starten (een CT kan niet live verhuizen)
        </Check>
      )}
    </FormModal>
  );
}

// CloneDialog maakt een kopie, volledig of gekoppeld (alleen van een template).
export function CloneDialog({ ctx, name, onClose }: { ctx: GuestCtx; name: string; onClose: () => void }) {
  const template = ctx.config.template === 1 || ctx.config.template === "1";
  const nextid = usePveGet<string | number>(ctx.dc.id, "/cluster/nextid", { refetch: false });
  const [typedId, setNewid] = useState<string | null>(null);
  const newid = typedId ?? (nextid.data !== undefined ? String(nextid.data) : "");
  const [newName, setNewName] = useState(name ? `${name}-kopie` : "");
  const [target, setTarget] = useState(ctx.node);
  const [full, setFull] = useState(!template);
  const [storage, setStorage] = useState("");
  const bits = useNodeBits(ctx.dc.id, target);
  const store = bits.storages.filter((s) => holds(s, ctx.type === "qemu" ? "images" : "rootdir"));
  const error = !/^\d+$/.test(newid) || Number(newid) < 100
    ? "Het nieuwe ID is een getal vanaf 100."
    : !/^[A-Za-z0-9]([A-Za-z0-9.-]{0,62})$/.test(newName.trim())
      ? "Geef een naam (letters, cijfers, punten en streepjes)."
      : !full && target !== ctx.node
        ? "Een gekoppelde kloon blijft op dezelfde host; kies een volledige kopie om naar een andere host te klonen."
        : ctx.running && ctx.type === "lxc"
          ? "Een draaiende CT kan Proxmox niet klonen; zet de CT eerst uit."
          : null;
  const submit = () => {
    const params: Record<string, string> = { newid, [ctx.type === "qemu" ? "name" : "hostname"]: newName.trim(), full: full ? "1" : "0" };
    if (target !== ctx.node) params.target = target;
    if (full && storage) params.storage = storage;
    void ctx.run({ method: "create", path: `${ctx.path}/clone`, params }).then((ok) => ok && onClose());
  };
  return (
    <FormModal title="Klonen" onClose={onClose} onSubmit={submit} submitLabel="Klonen" disabled={ctx.pending} error={error}>
      <div className="grid gap-3 sm:grid-cols-2">
        <Field label="Nieuw ID" htmlFor="cl-id">
          <Input id="cl-id" inputMode="numeric" value={newid} onChange={(e) => setNewid(e.target.value)} />
        </Field>
        <Field label="Naam" htmlFor="cl-name">
          <Input id="cl-name" value={newName} onChange={(e) => setNewName(e.target.value)} />
        </Field>
        <Field label="Host" htmlFor="cl-node">
          <Select id="cl-node" value={target} onChange={(e) => setTarget(e.target.value)}>
            {ctx.dc.hosts.map((h) => (
              <option key={h.pve_node} value={h.pve_node} disabled={!h.online}>
                {h.pve_node}
                {!h.online ? " (offline)" : ""}
              </option>
            ))}
          </Select>
        </Field>
        {full && (
          <Field label="Storage" htmlFor="cl-st" hint="Leeg: dezelfde als de bron">
            <Select id="cl-st" value={storage} onChange={(e) => setStorage(e.target.value)}>
              <option value="">zelfde als de bron</option>
              {store.map((s) => (
                <option key={s.storage} value={s.storage}>
                  {s.storage}
                  {s.avail ? ` (${formatBytes(s.avail)} vrij)` : ""}
                </option>
              ))}
            </Select>
          </Field>
        )}
      </div>
      {template && (
        <Check id="cl-full" checked={full} onChange={setFull}>
          Volledige kopie (los van de template). Uit: een gekoppelde kloon, snel en klein, maar de template kan dan niet meer weg.
        </Check>
      )}
    </FormModal>
  );
}
