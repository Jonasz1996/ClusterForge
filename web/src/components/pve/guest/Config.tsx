"use client";

import { useState } from "react";
import type { GuestCtx } from "@/app/(app)/proxmox/gast/page";
import { askConfirm } from "@/components/Confirm";
import { Empty, formatBytes, tableClass, tdClass, thClass } from "@/components/inventory/bits";
import { ActionMenu, TypedConfirm } from "@/components/pve/bits";
import { checkAddress, firstHost, holds, useNodeBits, VolumePicker } from "@/components/pve/CreateGuest";
import { Check, FormModal, freeIndex, joinProps } from "@/components/pve/guest/common";
import { Alert, Badge, Button, Card, Field, Input, Select, Textarea } from "@/components/ui";
import { diskKeys, parseProps, splitTags, type GuestConfig, type PveCall } from "@/lib/pve";

// Hardware, cloud-init en opties van één gast (fase 26 M2). Alles gaat als
// pvesh set op .../config; wat pas na een herstart werkt, toont de pagina als
// "wacht op herstart".

const n = (v: unknown) => Number(v ?? 0) || 0;

// sizeOf leest "size=32G" als bytes.
function sizeOf(v: string | number | undefined): number {
  const m = /(?:^|,)size=(\d+(?:\.\d+)?)([KMGT]?)/.exec(String(v ?? ""));
  if (!m) return 0;
  const mult = { "": 1, K: 2 ** 10, M: 2 ** 20, G: 2 ** 30, T: 2 ** 40 }[m[2] as "" | "K" | "M" | "G" | "T"];
  return Number(m[1]) * mult;
}

const isCdrom = (v: string | number | undefined) => /(?:^|,)media=cdrom/.test(String(v ?? ""));
const volumeOf = (v: string | number | undefined) => String(v ?? "").split(",")[0];
const onStorage = (vol: string) => /^[a-zA-Z][\w.-]*:/.test(vol) && !vol.startsWith("/");

function sortKeys(cfg: GuestConfig, re: RegExp) {
  return Object.keys(cfg)
    .filter((k) => re.test(k))
    .sort((a, b) => a.localeCompare(b, undefined, { numeric: true }));
}

// --- Hardware ---

export function HardwareTab({ ctx }: { ctx: GuestCtx }) {
  const { config: cfg, type } = ctx;
  const [dialog, setDialog] = useState<
    | { kind: "resources" }
    | { kind: "resize"; key: string }
    | { kind: "move"; key: string }
    | { kind: "unused"; key: string }
    | { kind: "iso"; key: string }
    | { kind: "disk" }
    | { kind: "net"; key: string; isNew: boolean }
    | null
  >(null);
  const close = () => setDialog(null);
  const disks = sortKeys(cfg, diskKeys);
  const nets = sortKeys(cfg, /^net\d+$/);
  const edit = ctx.isAdmin;

  const detach = async (key: string) => {
    const cd = isCdrom(cfg[key]);
    const msg = cd
      ? `Het cd-station ${key} verwijderen?`
      : `${key} loskoppelen? De schijf blijft bestaan als "ongebruikt" en kan daarna weer gekoppeld of verwijderd worden.`;
    if (!(await askConfirm(msg))) return;
    void ctx.run({ method: "set", path: `${ctx.path}/config`, params: { delete: key } });
  };

  const reattach = (key: string) => {
    const vol = volumeOf(cfg[key]);
    const i = freeIndex(cfg, "scsi");
    if (i < 0) return;
    void ctx.run({ method: "set", path: `${ctx.path}/config`, params: { [`scsi${i}`]: vol } });
  };

  const removeNet = async (key: string) => {
    if (!(await askConfirm(`Netwerkkaart ${key} verwijderen?`))) return;
    void ctx.run({ method: "set", path: `${ctx.path}/config`, params: { delete: key } });
  };

  return (
    <div className="space-y-4">
      <Card
        title={
          <span className="flex items-center justify-between gap-2">
            <span>Processor en geheugen</span>
            {edit && (
              <Button variant="secondary" disabled={ctx.pending} onClick={() => setDialog({ kind: "resources" })}>
                Wijzigen
              </Button>
            )}
          </span>
        }
      >
        <dl className="grid gap-x-6 gap-y-2 text-sm sm:grid-cols-2 lg:grid-cols-4">
          <Item label="Geheugen" value={`${n(cfg.memory) || 512} MiB`} />
          {type === "qemu" ? (
            <>
              <Item label="Ballon (minimum)" value={cfg.balloon === undefined ? "standaard" : n(cfg.balloon) === 0 ? "uit" : `${n(cfg.balloon)} MiB`} />
              <Item label="vCPU's" value={`${(n(cfg.sockets) || 1) * (n(cfg.cores) || 1)} (${n(cfg.sockets) || 1} × ${n(cfg.cores) || 1})`} />
              <Item label="CPU-type" value={String(cfg.cpu ?? "kvm64 (standaard)")} />
              <Item label="BIOS" value={cfg.bios === "ovmf" ? "UEFI (OVMF)" : "SeaBIOS"} />
              <Item label="Machine" value={String(cfg.machine ?? "i440fx")} />
              <Item label="SCSI-controller" value={String(cfg.scsihw ?? "lsi")} />
              <Item label="Besturingssysteem" value={String(cfg.ostype ?? "–")} />
            </>
          ) : (
            <>
              <Item label="Swap" value={`${n(cfg.swap)} MiB`} />
              <Item label="Kernen" value={cfg.cores === undefined ? "alle" : String(cfg.cores)} />
              <Item label="CPU-limiet" value={cfg.cpulimit ? String(cfg.cpulimit) : "geen"} />
              <Item label="Soort" value={n(cfg.unprivileged) ? "zonder rechten (unprivileged)" : "met rechten (privileged)"} />
            </>
          )}
        </dl>
      </Card>

      <Card
        title={
          <span className="flex items-center justify-between gap-2">
            <span>Schijven</span>
            {edit && (
              <Button variant="secondary" disabled={ctx.pending} onClick={() => setDialog({ kind: "disk" })}>
                Schijf toevoegen
              </Button>
            )}
          </span>
        }
      >
        {disks.length === 0 ? (
          <Empty>Geen schijven.</Empty>
        ) : (
          <div className="relative -mx-4 overflow-x-auto sm:mx-0">
            <table className={tableClass}>
              <thead>
                <tr>
                  <th className={thClass}>Sleutel</th>
                  <th className={thClass}>Volume</th>
                  <th className={thClass}>Grootte</th>
                  <th className={thClass}>Opties</th>
                  <th className={thClass}>
                    <span className="sr-only">Acties</span>
                  </th>
                </tr>
              </thead>
              <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
                {disks.map((k) => {
                  const v = cfg[k];
                  const props = parseProps(v);
                  const vol = props[""] ?? "";
                  const cd = isCdrom(v);
                  const unused = k.startsWith("unused");
                  const stored = onStorage(vol);
                  const extra = Object.entries(props)
                    .filter(([key]) => key !== "" && key !== "size" && key !== "media")
                    .map(([key, val]) => `${key}=${val}`)
                    .join(", ");
                  const items: { label: string; onClick: () => void; danger?: boolean }[] = [];
                  if (cd) {
                    items.push({ label: "ISO wisselen", onClick: () => setDialog({ kind: "iso", key: k }) });
                    items.push({ label: "Cd-station verwijderen", onClick: () => void detach(k), danger: true });
                  } else if (unused) {
                    if (type === "qemu") items.push({ label: "Weer koppelen", onClick: () => reattach(k) });
                    items.push({ label: "Definitief verwijderen", onClick: () => setDialog({ kind: "unused", key: k }), danger: true });
                  } else {
                    if (stored && !k.startsWith("efidisk") && !k.startsWith("tpmstate")) items.push({ label: "Groter maken", onClick: () => setDialog({ kind: "resize", key: k }) });
                    if (stored) items.push({ label: "Naar andere storage", onClick: () => setDialog({ kind: "move", key: k }) });
                    if (k !== "rootfs") items.push({ label: "Loskoppelen", onClick: () => void detach(k), danger: true });
                  }
                  return (
                    <tr key={k}>
                      <td className={tdClass + " font-mono text-xs"}>{k}</td>
                      <td className={tdClass + " font-mono text-xs break-all"}>
                        {vol}
                        {cd && (
                          <span className="ml-1">
                            <Badge>cd/dvd</Badge>
                          </span>
                        )}
                        {unused && (
                          <span className="ml-1">
                            <Badge tone="amber">ongebruikt</Badge>
                          </span>
                        )}
                      </td>
                      <td className={tdClass + " whitespace-nowrap tabular-nums"}>{sizeOf(v) ? formatBytes(sizeOf(v)) : "–"}</td>
                      <td className={tdClass + " text-xs text-slate-500"}>{extra || "–"}</td>
                      <td className={tdClass + " text-right"}>{edit && items.length > 0 && <ActionMenu items={items} disabled={ctx.pending} />}</td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        )}
      </Card>

      <Card
        title={
          <span className="flex items-center justify-between gap-2">
            <span>Netwerk</span>
            {edit && (
              <Button
                variant="secondary"
                disabled={ctx.pending}
                onClick={() => setDialog({ kind: "net", key: `net${freeIndex(cfg, "net")}`, isNew: true })}
              >
                Netwerkkaart toevoegen
              </Button>
            )}
          </span>
        }
      >
        {nets.length === 0 ? (
          <Empty>Geen netwerkkaarten.</Empty>
        ) : (
          <div className="relative -mx-4 overflow-x-auto sm:mx-0">
            <table className={tableClass}>
              <thead>
                <tr>
                  <th className={thClass}>Sleutel</th>
                  <th className={thClass}>Bridge</th>
                  <th className={thClass}>VLAN</th>
                  <th className={thClass}>MAC</th>
                  <th className={thClass}>{type === "lxc" ? "Adres" : "Model"}</th>
                  <th className={thClass}>Firewall</th>
                  <th className={thClass}>
                    <span className="sr-only">Acties</span>
                  </th>
                </tr>
              </thead>
              <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
                {nets.map((k) => {
                  const p = parseProps(cfg[k]);
                  const model = Object.keys(p).find((x) => nicModels.includes(x));
                  return (
                    <tr key={k}>
                      <td className={tdClass + " font-mono text-xs"}>
                        {k}
                        {p.name && <span className="text-slate-500"> ({p.name})</span>}
                      </td>
                      <td className={tdClass}>{p.bridge ?? "–"}</td>
                      <td className={tdClass}>{p.tag ?? "–"}</td>
                      <td className={tdClass + " font-mono text-xs"}>{type === "lxc" ? (p.hwaddr ?? "–") : model ? p[model] : "–"}</td>
                      <td className={tdClass + " text-xs"}>{type === "lxc" ? [p.ip, p.gw && `gw ${p.gw}`].filter(Boolean).join(", ") || "–" : (model ?? "–")}</td>
                      <td className={tdClass}>{p.firewall === "1" ? "aan" : "uit"}</td>
                      <td className={tdClass + " text-right"}>
                        {edit && (
                          <ActionMenu
                            disabled={ctx.pending}
                            items={[
                              { label: "Wijzigen", onClick: () => setDialog({ kind: "net", key: k, isNew: false }) },
                              { label: "Verwijderen", onClick: () => void removeNet(k), danger: true },
                            ]}
                          />
                        )}
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        )}
      </Card>

      {dialog?.kind === "resources" && <ResourcesDialog ctx={ctx} onClose={close} />}
      {dialog?.kind === "resize" && <ResizeDialog ctx={ctx} disk={dialog.key} onClose={close} />}
      {dialog?.kind === "move" && <MoveDiskDialog ctx={ctx} disk={dialog.key} onClose={close} />}
      {dialog?.kind === "iso" && <IsoDialog ctx={ctx} disk={dialog.key} onClose={close} />}
      {dialog?.kind === "disk" && <AddDiskDialog ctx={ctx} onClose={close} />}
      {dialog?.kind === "net" && <NetDialog ctx={ctx} netKey={dialog.key} isNew={dialog.isNew} onClose={close} />}
      {dialog?.kind === "unused" && (
        <TypedConfirm
          title={`${dialog.key} definitief verwijderen`}
          expect={String(ctx.vmid)}
          confirmLabel="Volume verwijderen"
          pending={ctx.pending}
          onClose={close}
          onConfirm={(typed) => {
            const call: PveCall =
              type === "qemu"
                ? { method: "set", path: `${ctx.path}/unlink`, params: { idlist: dialog.key, force: "1" }, confirm: typed }
                : { method: "set", path: `${ctx.path}/config`, params: { delete: dialog.key }, confirm: typed };
            void ctx.run(call).then((ok) => ok && close());
          }}
        >
          <p>
            Het volume <span className="font-mono">{volumeOf(cfg[dialog.key])}</span> wordt van de storage gewist. Dat kan niet ongedaan gemaakt worden.
          </p>
        </TypedConfirm>
      )}
    </div>
  );
}

const nicModels = ["virtio", "e1000", "e1000e", "vmxnet3", "rtl8139"];

function Item({ label, value }: { label: string; value: string }) {
  return (
    <div>
      <dt className="text-xs text-slate-500">{label}</dt>
      <dd className="font-medium break-words">{value}</dd>
    </div>
  );
}

const cpuTypes = ["host", "x86-64-v2-AES", "x86-64-v3", "x86-64-v4", "kvm64", "qemu64"];

function ResourcesDialog({ ctx, onClose }: { ctx: GuestCtx; onClose: () => void }) {
  const cfg = ctx.config;
  const [memory, setMemory] = useState(String(cfg.memory ?? 512));
  const [balloon, setBalloon] = useState(cfg.balloon === undefined ? "" : String(cfg.balloon));
  const [cores, setCores] = useState(String(cfg.cores ?? (ctx.type === "qemu" ? 1 : "")));
  const [sockets, setSockets] = useState(String(cfg.sockets ?? 1));
  const [cpu, setCpu] = useState(String(cfg.cpu ?? ""));
  const [swap, setSwap] = useState(String(cfg.swap ?? 512));
  const [cpulimit, setCpulimit] = useState(String(cfg.cpulimit ?? ""));
  const int = (s: string) => /^\d+$/.test(s.trim());
  const error =
    !int(memory) || Number(memory) < 16
      ? "Geef het geheugen in MiB (minstens 16)."
      : balloon && (!int(balloon) || Number(balloon) > Number(memory))
        ? "De ballon is een getal in MiB, niet groter dan het geheugen."
        : cores && (!int(cores) || Number(cores) < 1)
          ? "Het aantal kernen is een getal vanaf 1."
          : ctx.type === "qemu" && (!int(sockets) || Number(sockets) < 1)
            ? "Het aantal sockets is een getal vanaf 1."
            : ctx.type === "lxc" && !int(swap)
              ? "Geef de swap in MiB."
              : cpulimit && !/^\d+(\.\d+)?$/.test(cpulimit)
                ? "De CPU-limiet is een getal, zoals 2 of 1.5."
                : null;
  const submit = () => {
    const params: Record<string, string> = { memory: memory.trim() };
    const del: string[] = [];
    if (ctx.type === "qemu") {
      if (balloon.trim()) params.balloon = balloon.trim();
      else if (cfg.balloon !== undefined) del.push("balloon");
      params.cores = cores.trim() || "1";
      params.sockets = sockets.trim();
      if (cpu) params.cpu = cpu;
      else if (cfg.cpu !== undefined) del.push("cpu");
    } else {
      params.swap = swap.trim();
      if (cores.trim()) params.cores = cores.trim();
      else if (cfg.cores !== undefined) del.push("cores");
      if (cpulimit.trim() && cpulimit.trim() !== "0") params.cpulimit = cpulimit.trim();
      else if (cfg.cpulimit !== undefined) del.push("cpulimit");
    }
    if (del.length) params.delete = del.join(",");
    void ctx.run({ method: "set", path: `${ctx.path}/config`, params }).then((ok) => ok && onClose());
  };
  return (
    <FormModal title="Processor en geheugen" onClose={onClose} onSubmit={submit} submitLabel="Opslaan" disabled={ctx.pending} error={error}>
      <div className="grid gap-3 sm:grid-cols-2">
        <Field label="Geheugen (MiB)" htmlFor="r-mem">
          <Input id="r-mem" inputMode="numeric" value={memory} onChange={(e) => setMemory(e.target.value)} />
        </Field>
        {ctx.type === "qemu" ? (
          <>
            <Field label="Ballon, minimum (MiB)" htmlFor="r-bal" hint="Leeg: standaard; 0: ballon uit (nodig voor passthrough)">
              <Input id="r-bal" inputMode="numeric" value={balloon} onChange={(e) => setBalloon(e.target.value)} />
            </Field>
            <Field label="Sockets" htmlFor="r-sock">
              <Input id="r-sock" inputMode="numeric" value={sockets} onChange={(e) => setSockets(e.target.value)} />
            </Field>
            <Field label="Kernen per socket" htmlFor="r-cores">
              <Input id="r-cores" inputMode="numeric" value={cores} onChange={(e) => setCores(e.target.value)} />
            </Field>
            <Field label="CPU-type" htmlFor="r-cpu" hint="host is het snelst, maar verhuizen kan dan alleen naar dezelfde CPU">
              <Select id="r-cpu" value={cpu} onChange={(e) => setCpu(e.target.value)}>
                <option value="">standaard (kvm64)</option>
                {[...new Set([...cpuTypes, ...(cpu && !cpuTypes.includes(cpu) ? [cpu] : [])])].map((c) => (
                  <option key={c}>{c}</option>
                ))}
              </Select>
            </Field>
          </>
        ) : (
          <>
            <Field label="Swap (MiB)" htmlFor="r-swap">
              <Input id="r-swap" inputMode="numeric" value={swap} onChange={(e) => setSwap(e.target.value)} />
            </Field>
            <Field label="Kernen" htmlFor="r-cores" hint="Leeg: alle kernen van de host">
              <Input id="r-cores" inputMode="numeric" value={cores} onChange={(e) => setCores(e.target.value)} />
            </Field>
            <Field label="CPU-limiet" htmlFor="r-lim" hint="Leeg of 0: geen limiet">
              <Input id="r-lim" inputMode="decimal" value={cpulimit} onChange={(e) => setCpulimit(e.target.value)} />
            </Field>
          </>
        )}
      </div>
      {ctx.running && <p className="text-xs text-slate-500">Draait de gast, dan werkt een wijziging die niet live kan pas na een herstart; de pagina meldt dat.</p>}
    </FormModal>
  );
}

function ResizeDialog({ ctx, disk, onClose }: { ctx: GuestCtx; disk: string; onClose: () => void }) {
  const [add, setAdd] = useState("10");
  const now = sizeOf(ctx.config[disk]);
  const error = !/^\d+$/.test(add.trim()) || Number(add) < 1 ? "Geef het aantal GB dat erbij komt." : null;
  return (
    <FormModal
      title={`${disk} groter maken`}
      onClose={onClose}
      submitLabel="Groter maken"
      disabled={ctx.pending}
      error={error}
      onSubmit={() => void ctx.run({ method: "set", path: `${ctx.path}/resize`, params: { disk, size: `+${add.trim()}G` } }).then((ok) => ok && onClose())}
    >
      <p className="text-sm">
        Nu {now ? formatBytes(now) : "onbekend"}. Kleiner maken kan niet. Het bestandssysteem in de gast groeit niet altijd vanzelf mee
        {ctx.type === "qemu" ? " (in Linux: growpart en resize2fs of xfs_growfs)" : ""}.
      </p>
      <Field label="Erbij (GB)" htmlFor="rs-add">
        <Input id="rs-add" inputMode="numeric" value={add} onChange={(e) => setAdd(e.target.value)} autoFocus />
      </Field>
    </FormModal>
  );
}

function MoveDiskDialog({ ctx, disk, onClose }: { ctx: GuestCtx; disk: string; onClose: () => void }) {
  const bits = useNodeBits(ctx.dc.id, ctx.node);
  const cur = volumeOf(ctx.config[disk]).split(":")[0];
  const options = bits.storages.filter((s) => holds(s, ctx.type === "qemu" ? "images" : "rootdir") && s.storage !== cur);
  const [storage, setStorage] = useState("");
  const [del, setDel] = useState(true);
  const target = storage || options[0]?.storage || "";
  const ctRunning = ctx.type === "lxc" && ctx.running;
  const submit = () => {
    const call =
      ctx.type === "qemu"
        ? { path: `${ctx.path}/move_disk`, params: { disk, storage: target, ...(del ? { delete: "1" } : {}) } }
        : { path: `${ctx.path}/move_volume`, params: { volume: disk, storage: target, ...(del ? { delete: "1" } : {}) } };
    void ctx.run({ method: "create", ...call }).then((ok) => ok && onClose());
  };
  return (
    <FormModal
      title={`${disk} naar andere storage`}
      onClose={onClose}
      onSubmit={submit}
      submitLabel="Verplaatsen"
      disabled={ctx.pending || !target}
      error={ctRunning ? "Een CT moet uit staan om een volume te verplaatsen." : options.length === 0 ? "Geen andere storage op deze host die dit kan bewaren." : null}
    >
      <p className="text-sm">
        Nu op <span className="font-mono">{cur}</span>.{ctx.type === "qemu" && ctx.running ? " Dat kan terwijl de VM draait." : ""}
      </p>
      <Field label="Naar" htmlFor="mv-st">
        <Select id="mv-st" value={target} onChange={(e) => setStorage(e.target.value)}>
          {options.map((s) => (
            <option key={s.storage} value={s.storage}>
              {s.storage} ({s.type}
              {s.avail ? `, ${formatBytes(s.avail)} vrij` : ""})
            </option>
          ))}
        </Select>
      </Field>
      <Check id="mv-del" checked={del} onChange={setDel}>
        Het oude volume daarna weghalen (anders blijft het als ongebruikte schijf staan)
      </Check>
    </FormModal>
  );
}

function IsoDialog({ ctx, disk, onClose }: { ctx: GuestCtx; disk: string; onClose: () => void }) {
  const bits = useNodeBits(ctx.dc.id, ctx.node);
  const [iso, setIso] = useState("");
  const set = (vol: string) => void ctx.run({ method: "set", path: `${ctx.path}/config`, params: { [disk]: `${vol},media=cdrom` } }).then((ok) => ok && onClose());
  return (
    <FormModal title={`ISO in ${disk}`} onClose={onClose} onSubmit={() => set(iso)} submitLabel="Plaatsen" disabled={ctx.pending || !iso}>
      <Field label="ISO" htmlFor="iso-pick">
        <VolumePicker dc={ctx.dc.id} node={ctx.node} storages={bits.storages} content="iso" value={iso} onChange={setIso} id="iso-pick" empty="Geen ISO's op deze storage" />
      </Field>
      <Button type="button" variant="ghost" disabled={ctx.pending} onClick={() => set("none")}>
        Station leegmaken
      </Button>
    </FormModal>
  );
}

function AddDiskDialog({ ctx, onClose }: { ctx: GuestCtx; onClose: () => void }) {
  const bits = useNodeBits(ctx.dc.id, ctx.node);
  const options = bits.storages.filter((s) => holds(s, ctx.type === "qemu" ? "images" : "rootdir"));
  const [storage, setStorage] = useState("");
  const [size, setSize] = useState("32");
  const [bus, setBus] = useState("scsi");
  const [mp, setMp] = useState("/data");
  const [discard, setDiscard] = useState(true);
  const target = storage || options[0]?.storage || "";
  const key = ctx.type === "qemu" ? `${bus}${freeIndex(ctx.config, bus)}` : `mp${freeIndex(ctx.config, "mp", 255)}`;
  const error =
    !/^\d+$/.test(size.trim()) || Number(size) < 1
      ? "Geef de grootte in GB."
      : ctx.type === "lxc" && !/^\/[\w./-]+$/.test(mp.trim())
        ? "Geef een absoluut pad in de CT, zoals /data."
        : options.length === 0
          ? "Geen storage op deze host die schijven kan bewaren."
          : null;
  const submit = () => {
    let value = `${target}:${size.trim()}`;
    if (ctx.type === "qemu") {
      if (discard) value += ",discard=on";
      if (bus === "scsi" && String(ctx.config.scsihw ?? "").includes("single")) value += ",iothread=1";
      if (bus === "scsi" || bus === "sata") value += ",ssd=1";
    } else value += `,mp=${mp.trim()},backup=1`;
    void ctx.run({ method: "set", path: `${ctx.path}/config`, params: { [key]: value } }).then((ok) => ok && onClose());
  };
  return (
    <FormModal title="Schijf toevoegen" onClose={onClose} onSubmit={submit} submitLabel={`${key} toevoegen`} disabled={ctx.pending || !target} error={error}>
      <div className="grid gap-3 sm:grid-cols-2">
        <Field label="Storage" htmlFor="ad-st">
          <Select id="ad-st" value={target} onChange={(e) => setStorage(e.target.value)}>
            {options.map((s) => (
              <option key={s.storage} value={s.storage}>
                {s.storage}
                {s.avail ? ` (${formatBytes(s.avail)} vrij)` : ""}
              </option>
            ))}
          </Select>
        </Field>
        <Field label="Grootte (GB)" htmlFor="ad-size">
          <Input id="ad-size" inputMode="numeric" value={size} onChange={(e) => setSize(e.target.value)} />
        </Field>
        {ctx.type === "qemu" ? (
          <Field label="Bus" htmlFor="ad-bus">
            <Select id="ad-bus" value={bus} onChange={(e) => setBus(e.target.value)}>
              <option value="scsi">SCSI (aanbevolen)</option>
              <option value="virtio">VirtIO Block</option>
              <option value="sata">SATA</option>
            </Select>
          </Field>
        ) : (
          <Field label="Pad in de CT" htmlFor="ad-mp">
            <Input id="ad-mp" value={mp} onChange={(e) => setMp(e.target.value)} />
          </Field>
        )}
      </div>
      {ctx.type === "qemu" && (
        <Check id="ad-discard" checked={discard} onChange={setDiscard}>
          Discard aan (vrije ruimte teruggeven aan thin storage)
        </Check>
      )}
    </FormModal>
  );
}

function NetDialog({ ctx, netKey, isNew, onClose }: { ctx: GuestCtx; netKey: string; isNew: boolean; onClose: () => void }) {
  const bits = useNodeBits(ctx.dc.id, ctx.node);
  const orig = parseProps(isNew ? "" : ctx.config[netKey]);
  const origModel = Object.keys(orig).find((x) => nicModels.includes(x));
  const [model, setModel] = useState(origModel ?? "virtio");
  const [bridge, setBridge] = useState(orig.bridge ?? "");
  const [tag, setTag] = useState(orig.tag ?? "");
  const [fw, setFw] = useState(isNew ? true : orig.firewall === "1");
  const [name, setName] = useState(orig.name ?? `eth${netKey.slice(3)}`);
  const [ip, setIP] = useState(orig.ip && orig.ip !== "dhcp" ? orig.ip : "");
  const [gw, setGW] = useState(orig.gw ?? "");
  const br = bridge || bits.bridges[0]?.iface || "vmbr0";
  const error =
    tag && (!/^\d+$/.test(tag) || Number(tag) < 1 || Number(tag) > 4094)
      ? "Een VLAN ligt tussen 1 en 4094."
      : ctx.type === "lxc" && !/^[a-z][a-z0-9]{0,14}$/.test(name)
        ? "Geef een naam voor de kaart in de CT, zoals eth0."
        : ctx.type === "lxc" && ip
          ? checkAddress(ip, gw)
          : null;
  const submit = () => {
    const p: Record<string, string> = { ...orig };
    if (ctx.type === "qemu") {
      const mac = origModel ? orig[origModel] : "";
      if (origModel) delete p[origModel];
      const rest = Object.fromEntries(Object.entries(p).filter(([k]) => k !== ""));
      const head = mac ? `${model}=${mac}` : model;
      Object.assign(rest, { bridge: br, tag, firewall: fw ? "1" : "" });
      const value = [head, ...Object.entries(rest).filter(([, v]) => v !== "").map(([k, v]) => `${k}=${v}`)].join(",");
      void ctx.run({ method: "set", path: `${ctx.path}/config`, params: { [netKey]: value } }).then((ok) => ok && onClose());
      return;
    }
    Object.assign(p, { name, bridge: br, tag, firewall: fw ? "1" : "", ip: ip ? ip.trim() : "dhcp", gw: ip ? gw.trim() || firstHost(ip) : "" });
    if (!p.type) p.type = "veth";
    void ctx.run({ method: "set", path: `${ctx.path}/config`, params: { [netKey]: joinProps(p) } }).then((ok) => ok && onClose());
  };
  return (
    <FormModal title={isNew ? `Netwerkkaart ${netKey} toevoegen` : `${netKey} wijzigen`} onClose={onClose} onSubmit={submit} submitLabel="Opslaan" disabled={ctx.pending} error={error}>
      <div className="grid gap-3 sm:grid-cols-2">
        {ctx.type === "qemu" ? (
          <Field label="Model" htmlFor="nd-model">
            <Select id="nd-model" value={model} onChange={(e) => setModel(e.target.value)}>
              {nicModels.map((m) => (
                <option key={m}>{m}</option>
              ))}
            </Select>
          </Field>
        ) : (
          <Field label="Naam in de CT" htmlFor="nd-name">
            <Input id="nd-name" value={name} onChange={(e) => setName(e.target.value)} />
          </Field>
        )}
        <Field label="Bridge" htmlFor="nd-br">
          <Select id="nd-br" value={br} onChange={(e) => setBridge(e.target.value)}>
            {[...new Set([br, ...bits.bridges.map((b) => b.iface)])].map((b) => (
              <option key={b}>{b}</option>
            ))}
          </Select>
        </Field>
        <Field label="VLAN" htmlFor="nd-tag" hint="Leeg: geen tag">
          <Input id="nd-tag" inputMode="numeric" value={tag} onChange={(e) => setTag(e.target.value.replace(/\D/g, ""))} />
        </Field>
        {ctx.type === "lxc" && (
          <>
            <Field label="Adres" htmlFor="nd-ip" hint="Leeg: DHCP">
              <Input id="nd-ip" value={ip} onChange={(e) => setIP(e.target.value)} placeholder="192.168.20.11/24" />
            </Field>
            <Field label="Gateway" htmlFor="nd-gw" hint="Leeg: het eerste adres van het subnet">
              <Input id="nd-gw" value={gw} onChange={(e) => setGW(e.target.value)} placeholder={ip ? firstHost(ip) : ""} />
            </Field>
          </>
        )}
      </div>
      <Check id="nd-fw" checked={fw} onChange={setFw}>
        Proxmox-firewall op deze kaart
      </Check>
    </FormModal>
  );
}

// --- Cloud-init ---

export function CloudInitTab({ ctx }: { ctx: GuestCtx }) {
  const cfg = ctx.config;
  const ip0 = parseProps(cfg.ipconfig0);
  const [user, setUser] = useState(String(cfg.ciuser ?? ""));
  const [password, setPassword] = useState("");
  const [keys, setKeys] = useState(() => {
    try {
      return decodeURIComponent(String(cfg.sshkeys ?? "")).trim();
    } catch {
      return "";
    }
  });
  const [ip, setIP] = useState(ip0.ip && ip0.ip !== "dhcp" ? ip0.ip : "");
  const [gw, setGW] = useState(ip0.gw ?? "");
  const [dns, setDns] = useState(String(cfg.nameserver ?? ""));
  const [search, setSearch] = useState(String(cfg.searchdomain ?? ""));
  const [saved, setSaved] = useState(false);
  const error = ip ? checkAddress(ip, gw) : dns && !/^[\d.:a-fA-F ]+$/.test(dns) ? "De DNS-servers zijn IP-adressen, met een spatie ertussen." : null;
  const save = async () => {
    const params: Record<string, string> = {};
    const del: string[] = [];
    const put = (k: string, v: string) => {
      if (v) params[k] = v;
      else if (cfg[k] !== undefined) del.push(k);
    };
    put("ciuser", user.trim());
    if (password) params.cipassword = password;
    put("sshkeys", keys.trim() ? encodeURIComponent(keys.trim() + "\n") : "");
    params.ipconfig0 = ip ? `ip=${ip.trim()},gw=${gw.trim() || firstHost(ip)}` : "ip=dhcp";
    put("nameserver", dns.trim());
    put("searchdomain", search.trim());
    if (del.length) params.delete = del.join(",");
    const ok = await ctx.run({ method: "set", path: `${ctx.path}/config`, params });
    if (ok) {
      setPassword("");
      setSaved(true);
    }
  };
  return (
    <Card title="Cloud-init">
      <form
        className="space-y-4"
        onSubmit={(e) => {
          e.preventDefault();
          if (!error) void save();
        }}
      >
        <div className="grid gap-3 sm:grid-cols-2">
          <Field label="Gebruiker" htmlFor="ci-user">
            <Input id="ci-user" value={user} onChange={(e) => setUser(e.target.value)} autoComplete="off" disabled={!ctx.isAdmin} />
          </Field>
          <Field label="Wachtwoord" htmlFor="ci-pw" hint={cfg.cipassword ? "Er is een wachtwoord ingesteld; leeg laten houdt het" : "Leeg: geen wachtwoord, alleen sleutels"}>
            <Input id="ci-pw" type="password" value={password} onChange={(e) => setPassword(e.target.value)} autoComplete="new-password" disabled={!ctx.isAdmin} />
          </Field>
          <Field label="Adres" htmlFor="ci-ip" hint="Leeg: DHCP">
            <Input id="ci-ip" value={ip} onChange={(e) => setIP(e.target.value)} placeholder="192.168.20.11/24" disabled={!ctx.isAdmin} />
          </Field>
          <Field label="Gateway" htmlFor="ci-gw" hint="Leeg: het eerste adres van het subnet">
            <Input id="ci-gw" value={gw} onChange={(e) => setGW(e.target.value)} placeholder={ip ? firstHost(ip) : ""} disabled={!ctx.isAdmin} />
          </Field>
          <Field label="DNS-servers" htmlFor="ci-dns" hint="Leeg: zoals de host">
            <Input id="ci-dns" value={dns} onChange={(e) => setDns(e.target.value)} disabled={!ctx.isAdmin} />
          </Field>
          <Field label="Zoekdomein" htmlFor="ci-search">
            <Input id="ci-search" value={search} onChange={(e) => setSearch(e.target.value)} disabled={!ctx.isAdmin} />
          </Field>
        </div>
        <Field label="SSH-sleutels" htmlFor="ci-keys" hint="Eén publieke sleutel per regel">
          <Textarea id="ci-keys" rows={3} value={keys} onChange={(e) => setKeys(e.target.value)} className="font-mono text-xs" disabled={!ctx.isAdmin} />
        </Field>
        {error && <Alert>{error}</Alert>}
        {saved && (
          <Alert kind="info">
            Opgeslagen. Cloud-init past het toe bij de volgende start; een gast die al eens gestart is, neemt sommige zaken (zoals de gebruiker) niet opnieuw over.
          </Alert>
        )}
        {ctx.isAdmin && (
          <div className="flex flex-wrap justify-end gap-2">
            <Button
              type="button"
              variant="secondary"
              disabled={ctx.pending}
              onClick={() => void ctx.run({ method: "set", path: `${ctx.path}/cloudinit` })}
              title="Maakt de cloud-init-schijf opnieuw met de huidige instellingen"
            >
              Cloud-init-schijf opnieuw maken
            </Button>
            <Button type="submit" disabled={ctx.pending || !!error}>
              Opslaan
            </Button>
          </div>
        )}
      </form>
    </Card>
  );
}

// --- Opties ---

export function OptionsTab({ ctx }: { ctx: GuestCtx }) {
  const cfg = ctx.config;
  const nameKey = ctx.type === "qemu" ? "name" : "hostname";
  const startup = parseProps(cfg.startup);
  const [name, setName] = useState(String(cfg[nameKey] ?? ""));
  const [description, setDescription] = useState(String(cfg.description ?? ""));
  const [tags, setTags] = useState(splitTags(String(cfg.tags ?? "")).join(", "));
  const [onboot, setOnboot] = useState(cfg.onboot === 1 || cfg.onboot === "1");
  const [protection, setProtection] = useState(cfg.protection === 1 || cfg.protection === "1");
  const [order, setOrder] = useState(startup.order ?? "");
  const [up, setUp] = useState(startup.up ?? "");
  const [down, setDown] = useState(startup.down ?? "");
  const [agent, setAgent] = useState(parseProps(cfg.agent)[""] === "1" || parseProps(cfg.agent).enabled === "1");
  const tagList = splitTags(tags);
  const error = !/^[A-Za-z0-9]([A-Za-z0-9.-]{0,62})$/.test(name.trim())
    ? "De naam mag letters, cijfers, punten en streepjes bevatten (DNS-naam)."
    : tagList.some((t) => !/^[a-z0-9_][a-z0-9_.+-]{0,63}$/i.test(t))
      ? "Tags zijn woorden met letters, cijfers, _ . + of -."
      : [order, up, down].some((x) => x && !/^\d+$/.test(x))
        ? "Volgorde en wachttijden zijn getallen."
        : null;
  const save = () => {
    const params: Record<string, string> = { [nameKey]: name.trim(), onboot: onboot ? "1" : "0", protection: protection ? "1" : "0" };
    const del: string[] = [];
    if (description.trim()) params.description = description;
    else if (cfg.description !== undefined) del.push("description");
    if (tagList.length) params.tags = tagList.join(";");
    else if (cfg.tags !== undefined) del.push("tags");
    const st = joinProps({ order, up, down });
    if (st) params.startup = st;
    else if (cfg.startup !== undefined) del.push("startup");
    if (ctx.type === "qemu") {
      const a = parseProps(cfg.agent);
      delete a[""];
      a.enabled = agent ? "1" : "0";
      params.agent = joinProps(a);
    }
    if (del.length) params.delete = del.join(",");
    void ctx.run({ method: "set", path: `${ctx.path}/config`, params });
  };
  const off = !ctx.isAdmin;
  return (
    <Card title="Opties">
      <form
        className="space-y-4"
        onSubmit={(e) => {
          e.preventDefault();
          if (!error) save();
        }}
      >
        <div className="grid gap-3 sm:grid-cols-2">
          <Field label={ctx.type === "qemu" ? "Naam" : "Hostnaam"} htmlFor="op-name">
            <Input id="op-name" value={name} onChange={(e) => setName(e.target.value)} disabled={off} />
          </Field>
          <Field label="Tags" htmlFor="op-tags" hint="Met komma's ertussen">
            <Input id="op-tags" value={tags} onChange={(e) => setTags(e.target.value)} disabled={off} />
          </Field>
          <Field label="Startvolgorde" htmlFor="op-order" hint="Lager start eerder; leeg: na de genummerde">
            <Input id="op-order" inputMode="numeric" value={order} onChange={(e) => setOrder(e.target.value)} disabled={off} />
          </Field>
          <div className="grid grid-cols-2 gap-3">
            <Field label="Wacht na start (s)" htmlFor="op-up">
              <Input id="op-up" inputMode="numeric" value={up} onChange={(e) => setUp(e.target.value)} disabled={off} />
            </Field>
            <Field label="Wacht bij stoppen (s)" htmlFor="op-down">
              <Input id="op-down" inputMode="numeric" value={down} onChange={(e) => setDown(e.target.value)} disabled={off} />
            </Field>
          </div>
        </div>
        <Field label="Notities" htmlFor="op-desc" hint="Verschijnt ook in de Proxmox-GUI">
          <Textarea id="op-desc" rows={4} value={description} onChange={(e) => setDescription(e.target.value)} disabled={off} />
        </Field>
        <div className="space-y-2">
          <Check id="op-onboot" checked={onboot} onChange={(v) => !off && setOnboot(v)}>
            Starten als de host opstart
          </Check>
          <Check id="op-prot" checked={protection} onChange={(v) => !off && setProtection(v)}>
            Beveiligd: niet te verwijderen en schijven niet te wissen tot dit uit staat
          </Check>
          {ctx.type === "qemu" && (
            <Check id="op-agent" checked={agent} onChange={(v) => !off && setAgent(v)}>
              QEMU guest agent (voor IP-adressen, netjes afsluiten en consistente back-ups)
            </Check>
          )}
        </div>
        {error && <Alert>{error}</Alert>}
        {ctx.isAdmin && (
          <div className="flex justify-end">
            <Button type="submit" disabled={ctx.pending || !!error}>
              Opslaan
            </Button>
          </div>
        )}
      </form>
    </Card>
  );
}
