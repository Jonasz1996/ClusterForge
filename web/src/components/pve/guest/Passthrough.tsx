"use client";

import Link from "next/link";
import { useState } from "react";
import type { GuestCtx } from "@/app/(app)/proxmox/gast/page";
import { askConfirm } from "@/components/Confirm";
import { Empty, QueryState, tableClass, tdClass, thClass } from "@/components/inventory/bits";
import { Check, FormModal, freeIndex } from "@/components/pve/guest/common";
import { Alert, Badge, Button, Card, Field, Input, Select } from "@/components/ui";
import { parseProps, usePveHardware, type PveHardware } from "@/lib/pve";

// Passthrough van PCI- en USB-apparaten naar een VM, of /dev-apparaten naar
// een CT (fase 26 M2). De lijst komt uit de hardwarescan van de agent op de
// host; wat op de host eerst ingesteld moet worden (IOMMU, vfio) staat op de
// pagina Hardware.

type PCI = PveHardware["pci"][number];

// pciFit zegt of een apparaat klaar is om door te geven.
export function pciFit(d: PCI, hw: PveHardware): { tone: "green" | "amber" | "red"; text: string } {
  if (!hw.iommu) return { tone: "red", text: "IOMMU staat uit op de host" };
  if (d.host_use) return { tone: "red", text: `de host gebruikt het (${d.host_use})` };
  if (d.iommu_group < 0) return { tone: "red", text: "geen IOMMU-groep" };
  if (d.driver === "vfio-pci") return d.group_size > 1 ? { tone: "amber", text: `klaar, deelt groep met ${d.group_size - 1} ander(e)` } : { tone: "green", text: "klaar (vfio-pci)" };
  if (d.group_size > 1) return { tone: "amber", text: `deelt IOMMU-groep met ${d.group_size - 1} ander(e); eerst vfio instellen` };
  return { tone: "amber", text: d.driver ? `driver ${d.driver}; eerst vfio instellen op de host` : "geen driver; kan meestal zo" };
}

// Bruggen, hostbridges en dergelijke zijn nooit nuttig om door te geven.
export const passable = (d: PCI) => !/^06/.test(d.class) && !/^0c05/.test(d.class);
export const isGPU = (d: PCI) => /^03/.test(d.class);
const baseAddr = (a: string) => a.replace(/\.\d$/, "");

const ctDevSuggestions = ["/dev/dri/renderD128", "/dev/dri/card0", "/dev/net/tun", "/dev/ttyUSB0", "/dev/ttyACM0", "/dev/kvm"];

export function PassthroughTab({ ctx }: { ctx: GuestCtx }) {
  const host = ctx.dc.hosts.find((h) => h.pve_node === ctx.node);
  const hw = usePveHardware(host?.node_id ?? "", !!host && ctx.type === "qemu");
  const [dialog, setDialog] = useState<"pci" | "usb" | "dev" | null>(null);
  const [changed, setChanged] = useState(false);
  const cfg = ctx.config;
  const keys = Object.keys(cfg)
    .filter((k) => (ctx.type === "qemu" ? /^(hostpci|usb)\d+$/ : /^dev\d+$/).test(k))
    .sort((a, b) => a.localeCompare(b, undefined, { numeric: true }));
  const done = (ok: boolean) => {
    if (ok) {
      setChanged(true);
      setDialog(null);
    }
  };

  const remove = async (key: string) => {
    if (!(await askConfirm(`${key} (${String(cfg[key])}) weghalen uit deze gast?`))) return;
    done(await ctx.run({ method: "set", path: `${ctx.path}/config`, params: { delete: key } }));
  };

  const describe = (key: string) => {
    const v = String(cfg[key]);
    if (!key.startsWith("hostpci") || !hw.data) return v;
    const addr = parseProps(v)[""] ?? "";
    const full = addr.includes(":") && addr.split(":").length === 2 ? `0000:${addr}` : addr;
    const d = hw.data.pci.find((p) => p.address === full || baseAddr(p.address) === full);
    return d ? `${v} — ${d.device_name ?? d.class_name ?? ""}` : v;
  };

  return (
    <div className="space-y-4">
      {ctx.running && changed && (
        <Alert kind="info">
          <span className="flex flex-wrap items-center gap-2">
            De gast draait; doorgegeven apparaten werken pas na een herstart.
            <Button variant="secondary" disabled={ctx.pending} onClick={() => void ctx.run({ method: "create", path: `${ctx.path}/status/reboot` })}>
              Nu herstarten
            </Button>
          </span>
        </Alert>
      )}
      {hw.data?.reboot_required && (
        <Alert kind="info">
          Host {ctx.node} moet herstarten voor de passthrough-instellingen werken ({(hw.data.reboot_reasons ?? []).join("; ")}).{" "}
          <Link href={`/proxmox/hardware?dc=${encodeURIComponent(ctx.dc.id)}&node=${encodeURIComponent(ctx.node)}`} className="underline">
            Naar Hardware
          </Link>
        </Alert>
      )}
      <Card
        title={
          <span className="flex flex-wrap items-center justify-between gap-2">
            <span>Doorgegeven apparaten</span>
            {ctx.isAdmin && (
              <span className="flex flex-wrap gap-2">
                {ctx.type === "qemu" ? (
                  <>
                    <Button variant="secondary" disabled={ctx.pending || !hw.data} onClick={() => setDialog("pci")}>
                      PCI-apparaat
                    </Button>
                    <Button variant="secondary" disabled={ctx.pending || !hw.data} onClick={() => setDialog("usb")}>
                      USB-apparaat
                    </Button>
                  </>
                ) : (
                  <Button variant="secondary" disabled={ctx.pending} onClick={() => setDialog("dev")}>
                    Apparaat toevoegen
                  </Button>
                )}
              </span>
            )}
          </span>
        }
      >
        {!host && <Alert>Geen agent op host {ctx.node}; de hardware kan niet gelezen worden.</Alert>}
        {keys.length === 0 ? (
          <Empty>Nog niets doorgegeven.</Empty>
        ) : (
          <ul className="divide-y divide-slate-100 dark:divide-slate-800">
            {keys.map((k) => (
              <li key={k} className="flex items-center justify-between gap-3 py-2 text-sm">
                <span className="min-w-0">
                  <span className="font-mono text-xs">{k}</span> <span className="break-all">{describe(k)}</span>
                </span>
                {ctx.isAdmin && (
                  <Button variant="ghost" disabled={ctx.pending} onClick={() => void remove(k)}>
                    Weghalen
                  </Button>
                )}
              </li>
            ))}
          </ul>
        )}
        {ctx.type === "qemu" && keys.some((k) => k.startsWith("hostpci")) && (
          <p className="mt-3 text-xs text-slate-500">Een VM met een doorgegeven PCI-apparaat kan niet live verhuizen en houdt al zijn geheugen vast (ballon werkt niet).</p>
        )}
      </Card>

      {ctx.type === "qemu" && host && (
        <QueryState q={hw}>{hw.data && <HostDevices hw={hw.data} cfg={cfg} />}</QueryState>
      )}

      {dialog === "pci" && hw.data && <AddPCI ctx={ctx} hw={hw.data} onClose={() => setDialog(null)} onDone={done} />}
      {dialog === "usb" && hw.data && <AddUSB ctx={ctx} hw={hw.data} onClose={() => setDialog(null)} onDone={done} />}
      {dialog === "dev" && <AddDev ctx={ctx} onClose={() => setDialog(null)} onDone={done} />}
    </div>
  );
}

function HostDevices({ hw, cfg }: { hw: PveHardware; cfg: GuestCtx["config"] }) {
  const used = new Set(
    Object.entries(cfg)
      .filter(([k]) => k.startsWith("hostpci"))
      .map(([, v]) => (parseProps(v)[""] ?? "").replace(/^(?!0000:)/, "0000:")),
  );
  const list = hw.pci.filter(passable);
  return (
    <Card title="PCI-apparaten op deze host">
      <div className="relative -mx-4 overflow-x-auto sm:mx-0">
        <table className={tableClass}>
          <thead>
            <tr>
              <th className={thClass}>Adres</th>
              <th className={thClass}>Apparaat</th>
              <th className={thClass}>ID</th>
              <th className={thClass}>Groep</th>
              <th className={thClass}>Geschikt</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
            {list.map((d) => {
              const fit = pciFit(d, hw);
              return (
                <tr key={d.address}>
                  <td className={tdClass + " font-mono text-xs whitespace-nowrap"}>{d.address}</td>
                  <td className={tdClass}>
                    {d.device_name ?? d.class_name}
                    <span className="block text-xs text-slate-500">{d.vendor_name}</span>
                  </td>
                  <td className={tdClass + " font-mono text-xs whitespace-nowrap"}>
                    {d.vendor.replace(/^0x/, "")}:{d.device.replace(/^0x/, "")}
                  </td>
                  <td className={tdClass}>{d.iommu_group >= 0 ? d.iommu_group : "–"}</td>
                  <td className={tdClass}>
                    {used.has(d.address) || used.has(baseAddr(d.address)) ? <Badge tone="blue">in deze VM</Badge> : <Badge tone={fit.tone}>{fit.text}</Badge>}
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

function AddPCI({ ctx, hw, onClose, onDone }: { ctx: GuestCtx; hw: PveHardware; onClose: () => void; onDone: (ok: boolean) => void }) {
  const list = hw.pci.filter(passable).filter((d) => !d.host_use);
  const inUse = Object.entries(ctx.config)
    .filter(([k]) => k.startsWith("hostpci"))
    .map(([, v]) => (parseProps(v)[""] ?? "").replace(/^(?!0000:)/, "0000:"));
  const free = list.filter((x) => !inUse.includes(x.address) && !inUse.includes(baseAddr(x.address)));
  const [addr, setAddr] = useState(free.find((d) => d.driver === "vfio-pci")?.address ?? free[0]?.address ?? "");
  const d = list.find((x) => x.address === addr);
  const q35 = String(ctx.config.machine ?? "").includes("q35");
  const functions = d ? hw.pci.filter((x) => baseAddr(x.address) === baseAddr(d.address)).length : 0;
  const [all, setAll] = useState(true);
  const [pcie, setPcie] = useState(q35);
  const [primary, setPrimary] = useState(false);
  const [rombar, setRombar] = useState(true);
  const fit = d ? pciFit(d, hw) : null;
  const key = `hostpci${freeIndex(ctx.config, "hostpci", 15)}`;
  const balloon = ctx.config.balloon === undefined || Number(ctx.config.balloon) !== 0;
  const error = !d ? "Kies een apparaat." : fit?.tone === "red" ? `Dit apparaat kan niet: ${fit.text}.` : key === "hostpci-1" ? "Alle 16 plaatsen zijn bezet." : null;
  const submit = () => {
    if (!d) return;
    const parts = [all && functions > 1 ? baseAddr(d.address) : d.address];
    if (pcie && q35) parts.push("pcie=1");
    if (primary && isGPU(d)) parts.push("x-vga=1");
    if (!rombar) parts.push("rombar=0");
    const params: Record<string, string> = { [key]: parts.join(",") };
    if (balloon) params.balloon = "0";
    void ctx.run({ method: "set", path: `${ctx.path}/config`, params }).then(onDone);
  };
  return (
    <FormModal title="PCI-apparaat doorgeven" onClose={onClose} onSubmit={submit} submitLabel={`Als ${key} toevoegen`} disabled={ctx.pending} error={error} wide>
      <Field label="Apparaat" htmlFor="pp-dev">
        <Select id="pp-dev" value={addr} onChange={(e) => setAddr(e.target.value)}>
          {free.map((x) => (
            <option key={x.address} value={x.address}>
              {x.address} {x.device_name ?? x.class_name} [{x.vendor.replace(/^0x/, "")}:{x.device.replace(/^0x/, "")}]
              {x.driver === "vfio-pci" ? " ✓" : ""}
            </option>
          ))}
        </Select>
      </Field>
      {d && fit && (
        <p className="text-sm">
          IOMMU-groep {d.iommu_group}, driver {d.driver ?? "geen"}: <Badge tone={fit.tone}>{fit.text}</Badge>
        </p>
      )}
      {d && d.driver !== "vfio-pci" && fit?.tone !== "red" && (
        <Alert kind="info">
          Tip: zet dit apparaat eerst op vfio-pci via{" "}
          <Link href={`/proxmox/hardware?dc=${encodeURIComponent(ctx.dc.id)}&node=${encodeURIComponent(ctx.node)}`} className="underline">
            Hardware → Passthrough instellen
          </Link>
          , dan neemt de host het niet meer in gebruik bij het opstarten.
        </Alert>
      )}
      <div className="space-y-2">
        {functions > 1 && (
          <Check id="pp-all" checked={all} onChange={setAll}>
            Alle {functions} functies van dit apparaat (zoals de geluidschip van een videokaart)
          </Check>
        )}
        <Check id="pp-pcie" checked={pcie && q35} onChange={setPcie}>
          Als PCI Express {q35 ? "" : "(alleen met machinetype q35; deze VM is i440fx)"}
        </Check>
        {d && isGPU(d) && (
          <Check id="pp-vga" checked={primary} onChange={setPrimary}>
            Hoofdvideokaart van de VM (x-vga; het Proxmox-scherm blijft dan zwart)
          </Check>
        )}
        <Check id="pp-rom" checked={rombar} onChange={setRombar}>
          ROM-BAR (laat aan, tenzij de VM niet opstart)
        </Check>
      </div>
      {balloon && <p className="text-xs text-slate-500">De geheugenballon gaat uit (balloon=0): met passthrough ligt al het geheugen vast.</p>}
    </FormModal>
  );
}

function AddUSB({ ctx, hw, onClose, onDone }: { ctx: GuestCtx; hw: PveHardware; onClose: () => void; onDone: (ok: boolean) => void }) {
  const list = hw.usb.filter((u) => u.class !== "09");
  const [pick, setPick] = useState(list[0] ? `${list[0].vendor}:${list[0].product}` : "");
  const [byPort, setByPort] = useState(false);
  const u = list.find((x) => `${x.vendor}:${x.product}` === pick);
  const key = `usb${freeIndex(ctx.config, "usb", 13)}`;
  const fast = Number(u?.speed ?? 0) >= 5000;
  const submit = () => {
    if (!u) return;
    let v = byPort ? `host=${u.bus}-${u.port}` : `host=${u.vendor}:${u.product}`;
    if (fast) v += ",usb3=1";
    void ctx.run({ method: "set", path: `${ctx.path}/config`, params: { [key]: v } }).then(onDone);
  };
  return (
    <FormModal title="USB-apparaat doorgeven" onClose={onClose} onSubmit={submit} submitLabel={`Als ${key} toevoegen`} disabled={ctx.pending} error={!u ? "Geen USB-apparaat gevonden op de host." : key === "usb-1" ? "Alle plaatsen zijn bezet." : null}>
      <Field label="Apparaat" htmlFor="pu-dev">
        <Select id="pu-dev" value={pick} onChange={(e) => setPick(e.target.value)}>
          {list.map((x) => (
            <option key={`${x.bus}-${x.port}`} value={`${x.vendor}:${x.product}`}>
              {x.manufacturer ?? ""} {x.name ?? "onbekend"} [{x.vendor}:{x.product}] poort {x.bus}-{x.port}
            </option>
          ))}
        </Select>
      </Field>
      <Check id="pu-port" checked={byPort} onChange={setByPort}>
        Op poort in plaats van op ID (voor twee dezelfde sticks; het apparaat moet dan in die poort blijven)
      </Check>
    </FormModal>
  );
}

function AddDev({ ctx, onClose, onDone }: { ctx: GuestCtx; onClose: () => void; onDone: (ok: boolean) => void }) {
  const [path, setPath] = useState(ctx.config.dev0 === undefined ? "/dev/dri/renderD128" : "");
  const [gid, setGid] = useState("");
  const [readOnly, setReadOnly] = useState(false);
  const key = `dev${freeIndex(ctx.config, "dev", 255)}`;
  const error = !/^\/dev\/[\w./-]+$/.test(path.trim()) || path.includes("..")
    ? "Geef een pad onder /dev, zoals /dev/dri/renderD128."
    : gid && !/^\d+$/.test(gid)
      ? "De groep is een nummer (gid in de CT), zoals 104 voor render."
      : null;
  const submit = () => {
    let v = `path=${path.trim()}`;
    if (gid) v += `,gid=${gid}`;
    if (readOnly) v += ",deny-write=1";
    void ctx.run({ method: "set", path: `${ctx.path}/config`, params: { [key]: v } }).then(onDone);
  };
  return (
    <FormModal title="Apparaat doorgeven aan de CT" onClose={onClose} onSubmit={submit} submitLabel={`Als ${key} toevoegen`} disabled={ctx.pending} error={error}>
      <Field label="Apparaat op de host" htmlFor="pd-path" hint="Bijvoorbeeld de GPU voor transcoding: /dev/dri/renderD128">
        <Input id="pd-path" list="pd-sugg" value={path} onChange={(e) => setPath(e.target.value)} className="font-mono" />
        <datalist id="pd-sugg">
          {ctDevSuggestions.map((s) => (
            <option key={s} value={s} />
          ))}
        </datalist>
      </Field>
      <Field label="Groep in de CT (gid)" htmlFor="pd-gid" hint="Leeg: root. Debian: render = 104 (of 993), video = 44">
        <Input id="pd-gid" inputMode="numeric" value={gid} onChange={(e) => setGid(e.target.value)} />
      </Field>
      <Check id="pd-ro" checked={readOnly} onChange={setReadOnly}>
        Alleen lezen
      </Check>
    </FormModal>
  );
}
