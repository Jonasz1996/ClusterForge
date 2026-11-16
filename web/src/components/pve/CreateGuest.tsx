"use client";

import { useMemo, useState, type ReactNode } from "react";
import { Modal } from "@/components/Modal";
import { formatBytes } from "@/components/inventory/bits";
import { useCallRunner } from "@/components/pve/bits";
import { Alert, Button, Field, Input, Select, Textarea } from "@/components/ui";
import { usePveGet, type Datacenter, type PveResource } from "@/lib/pve";

// Een nieuwe VM of CT, los van een ClusterForge-cluster (fase 26 M2).

export type NodeStorage = { storage: string; type: string; avail?: number; total?: number; content?: string; active?: number };
type Volume = { volid: string; size?: number; format?: string };

const tagRe = /^[a-z0-9_][a-z0-9_.+-]{0,63}$/;

// checkAddress controleert een vast adres: geldig, met prefix, en niet op de
// eerste drie adressen van het subnet (gateway en firewalls).
export function checkAddress(cidr: string, gw: string): string | null {
  const m = /^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})\/(\d{1,2})$/.exec(cidr.trim());
  if (!m || m.slice(1, 5).some((x) => Number(x) > 255)) return "Geef een adres met prefix, zoals 192.168.20.11/24.";
  const bits = Number(m[5]);
  if (bits < 8 || bits > 30) return "De prefix moet tussen /8 en /30 liggen.";
  const ip = m.slice(1, 5).reduce((a, x) => a * 256 + Number(x), 0);
  const mask = bits === 0 ? 0 : (0xffffffff << (32 - bits)) >>> 0;
  const host = ip - ((ip & mask) >>> 0);
  if (host <= 3) return "De eerste drie adressen van het subnet zijn voor de gateway en de twee firewalls; kies een hoger adres.";
  if (gw && !/^\d{1,3}(\.\d{1,3}){3}$/.test(gw.trim())) return "De gateway is geen IPv4-adres.";
  return null;
}

// firstHost geeft de gateway die bij een adres hoort: het eerste adres.
export function firstHost(cidr: string): string {
  const m = /^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})\/(\d{1,2})$/.exec(cidr.trim());
  if (!m) return "";
  const bits = Number(m[5]);
  const ip = m.slice(1, 5).reduce((a, x) => a * 256 + Number(x), 0);
  const mask = (0xffffffff << (32 - bits)) >>> 0;
  const g = ((ip & mask) >>> 0) + 1;
  return [g >>> 24, (g >>> 16) & 255, (g >>> 8) & 255, g & 255].join(".");
}

export function useNodeBits(dc: string, node: string) {
  const bridges = usePveGet<{ iface: string }[]>(dc, `/nodes/${node}/network`, { params: { type: "any_bridge" }, enabled: !!node, refetch: false });
  const storages = usePveGet<NodeStorage[]>(dc, `/nodes/${node}/storage`, { params: { enabled: "1" }, enabled: !!node, refetch: false });
  const nextid = usePveGet<string | number>(dc, "/cluster/nextid", { refetch: false });
  return { bridges: bridges.data ?? [], storages: storages.data ?? [], nextid: nextid.data };
}

export const holds = (s: NodeStorage, c: string) => (s.content ?? "").split(",").includes(c);

export function VolumePicker({ dc, node, storages, content, value, onChange, id, empty }: {
  dc: string;
  node: string;
  storages: NodeStorage[];
  content: string;
  value: string;
  onChange: (v: string) => void;
  id: string;
  empty: string;
}) {
  const [st, setSt] = useState("");
  const options = storages.filter((s) => holds(s, content));
  const store = st || options[0]?.storage || "";
  const vols = usePveGet<Volume[]>(dc, `/nodes/${node}/storage/${store}/content`, { params: { content }, enabled: !!node && !!store, refetch: false });
  return (
    <div className="grid gap-2 sm:grid-cols-[10rem_1fr]">
      <Select aria-label="Storage" value={store} onChange={(e) => setSt(e.target.value)}>
        {options.length === 0 && <option value="">geen storage</option>}
        {options.map((s) => (
          <option key={s.storage}>{s.storage}</option>
        ))}
      </Select>
      <Select id={id} value={value} onChange={(e) => onChange(e.target.value)}>
        <option value="">{(vols.data ?? []).length === 0 ? empty : "Kies…"}</option>
        {(vols.data ?? []).map((v) => (
          <option key={v.volid} value={v.volid}>
            {v.volid.replace(/^[^:]+:(iso|vztmpl|import)\//, "")}
            {v.size ? ` (${formatBytes(v.size)})` : ""}
          </option>
        ))}
      </Select>
    </div>
  );
}

function Section({ title, children }: { title: string; children: ReactNode }) {
  return (
    <fieldset className="space-y-3 border-t border-slate-200 pt-3 dark:border-slate-800">
      <legend className="pr-2 text-sm font-semibold">{title}</legend>
      {children}
    </fieldset>
  );
}

function NetFields({ bridges, bridge, setBridge, vlan, setVlan, ip, setIP, gw, setGW, dhcpLabel }: {
  bridges: { iface: string }[];
  bridge: string;
  setBridge: (v: string) => void;
  vlan: string;
  setVlan: (v: string) => void;
  ip: string;
  setIP: (v: string) => void;
  gw: string;
  setGW: (v: string) => void;
  dhcpLabel: string;
}) {
  return (
    <div className="grid gap-3 sm:grid-cols-2">
      <Field label="Bridge" htmlFor="cg-br">
        <Select id="cg-br" value={bridge} onChange={(e) => setBridge(e.target.value)}>
          {bridges.map((b) => (
            <option key={b.iface}>{b.iface}</option>
          ))}
        </Select>
      </Field>
      <Field label="VLAN" htmlFor="cg-vlan" hint="Leeg: geen tag">
        <Input id="cg-vlan" inputMode="numeric" value={vlan} onChange={(e) => setVlan(e.target.value.replace(/\D/g, ""))} />
      </Field>
      <Field label="Adres" htmlFor="cg-ip" hint={dhcpLabel}>
        <Input id="cg-ip" value={ip} onChange={(e) => setIP(e.target.value)} placeholder="192.168.20.11/24" />
      </Field>
      <Field label="Gateway" htmlFor="cg-gw" hint="Leeg: het eerste adres van het subnet">
        <Input id="cg-gw" value={gw} onChange={(e) => setGW(e.target.value)} placeholder={ip ? firstHost(ip) : ""} />
      </Field>
    </div>
  );
}

function useCommon(dc: Datacenter, nodes: string[]) {
  const [node, setNode] = useState(nodes[0] ?? "");
  const bits = useNodeBits(dc.id, node);
  const [typedVmid, setVmid] = useState<string | null>(null);
  const vmid = typedVmid ?? (bits.nextid !== undefined ? String(bits.nextid) : "");
  const [bridge, setBridge] = useState("");
  const br = bridge || bits.bridges[0]?.iface || "vmbr0";
  return { node, setNode, bits, vmid, setVmid, bridge: br, setBridge };
}

export function CreateVMDialog({ dc, nodes, onClose, used }: { dc: Datacenter; nodes: string[]; storages: PveResource[]; used: PveResource[]; onClose: () => void }) {
  const c = useCommon(dc, nodes);
  const { run, pending, banner } = useCallRunner(dc.id);
  const [name, setName] = useState("");
  const [mode, setMode] = useState<"iso" | "cloud" | "empty">("iso");
  const [iso, setIso] = useState("");
  const [image, setImage] = useState("");
  const [ostype, setOstype] = useState("l26");
  const [cores, setCores] = useState("2");
  const [cpu, setCpu] = useState("host");
  const [mem, setMem] = useState("2048");
  const [balloon, setBalloon] = useState("");
  const [uefi, setUefi] = useState(false);
  const [disk, setDisk] = useState("");
  const [size, setSize] = useState("32");
  const [vlan, setVlan] = useState("");
  const [ip, setIP] = useState("");
  const [gw, setGW] = useState("");
  const [ciuser, setCiuser] = useState("debian");
  const [cipass, setCipass] = useState("");
  const [keys, setKeys] = useState("");
  const [dns, setDns] = useState("");
  const [tags, setTags] = useState("");
  const [onboot, setOnboot] = useState(true);
  const [start, setStart] = useState(true);
  const diskStores = c.bits.storages.filter((s) => holds(s, "images"));
  const store = disk || diskStores[0]?.storage || "";
  const tagList = tags.split(/[\s,;]+/).filter(Boolean);
  const idTaken = used.some((g) => String(g.vmid) === c.vmid.trim());
  const errs: string[] = [];
  if (!/^[1-9]\d{2,8}$/.test(c.vmid.trim())) errs.push("Het VMID is een getal vanaf 100.");
  else if (idTaken) errs.push(`VMID ${c.vmid} is al in gebruik.`);
  if (!/^[a-zA-Z0-9]([a-zA-Z0-9.-]{0,61}[a-zA-Z0-9])?$/.test(name)) errs.push("Geef een naam (letters, cijfers, - en .).");
  if (mode === "iso" && !iso) errs.push("Kies een ISO.");
  if (mode === "cloud" && !image) errs.push("Kies een cloud-image (storage met inhoud 'import').");
  if (!store) errs.push("Geen storage voor schijven op deze host.");
  if (ip) {
    const e = checkAddress(ip, gw);
    if (e) errs.push(e);
  }
  if (tagList.some((t) => !tagRe.test(t))) errs.push("Een tag heeft kleine letters, cijfers en - _ . +.");

  const submit = async () => {
    const p: Record<string, string> = {
      vmid: c.vmid.trim(),
      name,
      ostype,
      cores,
      sockets: "1",
      cpu,
      memory: mem,
      scsihw: "virtio-scsi-single",
      agent: "1",
      onboot: onboot ? "1" : "0",
      net0: `virtio,bridge=${c.bridge}${vlan ? `,tag=${vlan}` : ""}`,
    };
    if (balloon) p.balloon = balloon;
    if (uefi) {
      p.bios = "ovmf";
      p.machine = "q35";
      p.efidisk0 = `${store}:1,efitype=4m,pre-enrolled-keys=${ostype.startsWith("win") ? "1" : "0"}`;
    }
    if (mode === "cloud") {
      p.scsi0 = `${store}:0,import-from=${image},iothread=1,discard=on`;
      p.ide2 = `${store}:cloudinit`;
      p.boot = "order=scsi0";
      p.serial0 = "socket";
      p.vga = "serial0";
      if (ciuser) p.ciuser = ciuser;
      if (cipass) p.cipassword = cipass;
      if (keys.trim()) p.sshkeys = encodeURIComponent(keys.trim() + "\n");
      p.ipconfig0 = ip ? `ip=${ip.trim()},gw=${gw.trim() || firstHost(ip)}` : "ip=dhcp";
      if (dns.trim()) p.nameserver = dns.trim();
    } else {
      p.scsi0 = `${store}:${size},iothread=1,discard=on`;
      if (mode === "iso") {
        p.ide2 = `${iso},media=cdrom`;
        p.boot = "order=scsi0;ide2;net0";
      }
    }
    if (tagList.length) p.tags = tagList.join(";");
    if (start) p.start = "1";
    if (await run({ method: "create", path: `/nodes/${c.node}/qemu`, params: p })) onClose();
  };

  return (
    <Modal title="VM maken" onClose={onClose} wide>
      <form
        className="space-y-4"
        onSubmit={(e) => {
          e.preventDefault();
          if (errs.length === 0) void submit();
        }}
      >
        {banner}
        <div className="grid gap-3 sm:grid-cols-3">
          <Field label="Host" htmlFor="vm-node">
            <Select id="vm-node" value={c.node} onChange={(e) => c.setNode(e.target.value)}>
              {nodes.map((n) => (
                <option key={n}>{n}</option>
              ))}
            </Select>
          </Field>
          <Field label="VMID" htmlFor="vm-id">
            <Input id="vm-id" inputMode="numeric" value={c.vmid} onChange={(e) => c.setVmid(e.target.value)} />
          </Field>
          <Field label="Naam" htmlFor="vm-name">
            <Input id="vm-name" value={name} onChange={(e) => setName(e.target.value)} placeholder="docker-01" />
          </Field>
        </div>
        <Section title="Besturingssysteem">
          <div className="flex flex-wrap gap-4 text-sm">
            {(
              [
                ["iso", "Installeren van een ISO"],
                ["cloud", "Cloud-image met cloud-init"],
                ["empty", "Lege schijf"],
              ] as const
            ).map(([k, l]) => (
              <label key={k} className="inline-flex items-center gap-1.5">
                <input type="radio" name="vm-mode" checked={mode === k} onChange={() => setMode(k)} />
                {l}
              </label>
            ))}
          </div>
          {mode === "iso" && (
            <Field label="ISO" htmlFor="vm-iso">
              <VolumePicker dc={dc.id} node={c.node} storages={c.bits.storages} content="iso" value={iso} onChange={setIso} id="vm-iso" empty="Geen ISO's; download er een bij Storage" />
            </Field>
          )}
          {mode === "cloud" && (
            <Field label="Cloud-image" htmlFor="vm-img" hint="Een .qcow2 of .img op een storage met inhoud 'import', zoals debian-13-genericcloud-amd64.qcow2">
              <VolumePicker dc={dc.id} node={c.node} storages={c.bits.storages} content="import" value={image} onChange={setImage} id="vm-img" empty="Geen images; download er een bij Storage" />
            </Field>
          )}
          <div className="grid gap-3 sm:grid-cols-2">
            <Field label="Soort" htmlFor="vm-os">
              <Select id="vm-os" value={ostype} onChange={(e) => setOstype(e.target.value)}>
                <option value="l26">Linux</option>
                <option value="win11">Windows 11 / 2022 / 2025</option>
                <option value="win10">Windows 10 / 2016 / 2019</option>
                <option value="other">Iets anders</option>
              </Select>
            </Field>
            <label className="flex items-end gap-2 pb-2 text-sm">
              <input type="checkbox" checked={uefi} onChange={(e) => setUefi(e.target.checked)} />
              UEFI (OVMF, q35) — nodig voor Windows 11 en GPU-passthrough
            </label>
          </div>
        </Section>
        <Section title="Hardware">
          <div className="grid gap-3 sm:grid-cols-4">
            <Field label="Cores" htmlFor="vm-cores">
              <Input id="vm-cores" inputMode="numeric" value={cores} onChange={(e) => setCores(e.target.value.replace(/\D/g, ""))} />
            </Field>
            <Field label="CPU-type" htmlFor="vm-cpu" hint="host: snelst; x86-64-v2-AES: verhuist tussen verschillende CPU's">
              <Select id="vm-cpu" value={cpu} onChange={(e) => setCpu(e.target.value)}>
                <option>host</option>
                <option>x86-64-v2-AES</option>
                <option>x86-64-v3</option>
              </Select>
            </Field>
            <Field label="Geheugen (MB)" htmlFor="vm-mem">
              <Input id="vm-mem" inputMode="numeric" value={mem} onChange={(e) => setMem(e.target.value.replace(/\D/g, ""))} />
            </Field>
            <Field label="Minimum (ballooning)" htmlFor="vm-bal" hint="Leeg: zelfde als geheugen">
              <Input id="vm-bal" inputMode="numeric" value={balloon} onChange={(e) => setBalloon(e.target.value.replace(/\D/g, ""))} />
            </Field>
          </div>
          <div className="grid gap-3 sm:grid-cols-2">
            <Field label="Schijf op" htmlFor="vm-st">
              <Select id="vm-st" value={store} onChange={(e) => setDisk(e.target.value)}>
                {diskStores.map((s) => (
                  <option key={s.storage} value={s.storage}>
                    {s.storage} ({s.type}{s.avail ? `, ${formatBytes(s.avail)} vrij` : ""})
                  </option>
                ))}
              </Select>
            </Field>
            {mode !== "cloud" ? (
              <Field label="Grootte (GB)" htmlFor="vm-size">
                <Input id="vm-size" inputMode="numeric" value={size} onChange={(e) => setSize(e.target.value.replace(/\D/g, ""))} />
              </Field>
            ) : (
              <p className="self-end pb-2 text-xs text-slate-500">De schijf krijgt de grootte van het image; maak ze daarna groter bij Hardware.</p>
            )}
          </div>
        </Section>
        <Section title="Netwerk">
          <NetFields
            bridges={c.bits.bridges}
            bridge={c.bridge}
            setBridge={c.setBridge}
            vlan={vlan}
            setVlan={setVlan}
            ip={ip}
            setIP={setIP}
            gw={gw}
            setGW={setGW}
            dhcpLabel={mode === "cloud" ? "Leeg: DHCP (via cloud-init)" : "Alleen voor cloud-init; anders stel je het adres in de VM in"}
          />
        </Section>
        {mode === "cloud" && (
          <Section title="Cloud-init">
            <div className="grid gap-3 sm:grid-cols-3">
              <Field label="Gebruiker" htmlFor="vm-ciu">
                <Input id="vm-ciu" value={ciuser} onChange={(e) => setCiuser(e.target.value)} />
              </Field>
              <Field label="Wachtwoord" htmlFor="vm-cip" hint="Gaat versleuteld naar de host">
                <Input id="vm-cip" type="password" autoComplete="new-password" value={cipass} onChange={(e) => setCipass(e.target.value)} />
              </Field>
              <Field label="DNS" htmlFor="vm-dns" hint="Leeg: die van de host">
                <Input id="vm-dns" value={dns} onChange={(e) => setDns(e.target.value)} placeholder="192.168.10.1" />
              </Field>
            </div>
            <Field label="SSH-sleutels" htmlFor="vm-keys" hint="Eén publieke sleutel per regel">
              <Textarea id="vm-keys" rows={3} value={keys} onChange={(e) => setKeys(e.target.value)} className="font-mono text-xs" />
            </Field>
          </Section>
        )}
        <Section title="Opties">
          <Field label="Tags" htmlFor="vm-tags">
            <Input id="vm-tags" value={tags} onChange={(e) => setTags(e.target.value)} placeholder="prod docker" />
          </Field>
          <div className="flex flex-wrap gap-4 text-sm">
            <label className="inline-flex items-center gap-1.5">
              <input type="checkbox" checked={onboot} onChange={(e) => setOnboot(e.target.checked)} />
              Starten bij het opstarten van de host
            </label>
            <label className="inline-flex items-center gap-1.5">
              <input type="checkbox" checked={start} onChange={(e) => setStart(e.target.checked)} />
              Meteen starten
            </label>
          </div>
        </Section>
        {errs.length > 0 && (
          <Alert kind="info">
            <ul className="list-inside list-disc">
              {errs.map((e) => (
                <li key={e}>{e}</li>
              ))}
            </ul>
          </Alert>
        )}
        <div className="flex justify-end gap-2">
          <Button type="button" variant="secondary" onClick={onClose}>
            Annuleren
          </Button>
          <Button type="submit" disabled={pending || errs.length > 0}>
            VM maken
          </Button>
        </div>
      </form>
    </Modal>
  );
}

export function CreateCTDialog({ dc, nodes, onClose, used }: { dc: Datacenter; nodes: string[]; storages: PveResource[]; used: PveResource[]; onClose: () => void }) {
  const c = useCommon(dc, nodes);
  const { run, pending, banner } = useCallRunner(dc.id);
  const [hostname, setHostname] = useState("");
  const [tmpl, setTmpl] = useState("");
  const [password, setPassword] = useState("");
  const [keys, setKeys] = useState("");
  const [cores, setCores] = useState("1");
  const [mem, setMem] = useState("1024");
  const [swap, setSwap] = useState("512");
  const [disk, setDisk] = useState("");
  const [size, setSize] = useState("8");
  const [vlan, setVlan] = useState("");
  const [ip, setIP] = useState("");
  const [gw, setGW] = useState("");
  const [dns, setDns] = useState("");
  const [nesting, setNesting] = useState(true);
  const [docker, setDocker] = useState(false);
  const [tags, setTags] = useState("");
  const [onboot, setOnboot] = useState(true);
  const [start, setStart] = useState(true);
  const rootStores = useMemo(() => c.bits.storages.filter((s) => holds(s, "rootdir")), [c.bits.storages]);
  const store = disk || rootStores[0]?.storage || "";
  const tagList = tags.split(/[\s,;]+/).filter(Boolean);
  const errs: string[] = [];
  if (!/^[1-9]\d{2,8}$/.test(c.vmid.trim())) errs.push("Het CTID is een getal vanaf 100.");
  else if (used.some((g) => String(g.vmid) === c.vmid.trim())) errs.push(`CTID ${c.vmid} is al in gebruik.`);
  if (!/^[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?$/.test(hostname)) errs.push("Geef een hostname (letters, cijfers en -).");
  if (!tmpl) errs.push("Kies een containertemplate (download er een bij Hosts en gasten → Containertemplates).");
  if (!password && !keys.trim()) errs.push("Geef een wachtwoord of een SSH-sleutel voor root.");
  if (password && password.length < 5) errs.push("Het wachtwoord heeft minstens 5 tekens.");
  if (!store) errs.push("Geen storage voor containers op deze host.");
  if (ip) {
    const e = checkAddress(ip, gw);
    if (e) errs.push(e);
  }
  if (tagList.some((t) => !tagRe.test(t))) errs.push("Een tag heeft kleine letters, cijfers en - _ . +.");

  const submit = async () => {
    const net = [`name=eth0`, `bridge=${c.bridge}`];
    if (vlan) net.push(`tag=${vlan}`);
    net.push(ip ? `ip=${ip.trim()}` : "ip=dhcp");
    if (ip) net.push(`gw=${gw.trim() || firstHost(ip)}`);
    const p: Record<string, string> = {
      vmid: c.vmid.trim(),
      hostname,
      ostemplate: tmpl,
      cores,
      memory: mem,
      swap,
      rootfs: `${store}:${size}`,
      net0: net.join(","),
      unprivileged: "1",
      onboot: onboot ? "1" : "0",
    };
    const features = [nesting || docker ? "nesting=1" : "", docker ? "keyctl=1" : ""].filter(Boolean);
    if (features.length) p.features = features.join(",");
    if (password) p.password = password;
    if (keys.trim()) p["ssh-public-keys"] = keys.trim() + "\n";
    if (dns.trim()) p.nameserver = dns.trim();
    if (tagList.length) p.tags = tagList.join(";");
    if (start) p.start = "1";
    if (await run({ method: "create", path: `/nodes/${c.node}/lxc`, params: p })) onClose();
  };

  return (
    <Modal title="CT maken" onClose={onClose} wide>
      <form
        className="space-y-4"
        onSubmit={(e) => {
          e.preventDefault();
          if (errs.length === 0) void submit();
        }}
      >
        {banner}
        <p className="text-sm text-slate-500">
          Een losse CT. Voor een CT met de ClusterForge-agent erin, die meteen in een cluster komt, gebruik je Cluster uitrollen of Hosts en gasten → CT maken.
        </p>
        <div className="grid gap-3 sm:grid-cols-3">
          <Field label="Host" htmlFor="ct-node">
            <Select id="ct-node" value={c.node} onChange={(e) => c.setNode(e.target.value)}>
              {nodes.map((n) => (
                <option key={n}>{n}</option>
              ))}
            </Select>
          </Field>
          <Field label="CTID" htmlFor="ct-id">
            <Input id="ct-id" inputMode="numeric" value={c.vmid} onChange={(e) => c.setVmid(e.target.value)} />
          </Field>
          <Field label="Hostname" htmlFor="ct-name">
            <Input id="ct-name" value={hostname} onChange={(e) => setHostname(e.target.value)} placeholder="web-01" />
          </Field>
        </div>
        <Field label="Containertemplate" htmlFor="ct-tmpl">
          <VolumePicker dc={dc.id} node={c.node} storages={c.bits.storages} content="vztmpl" value={tmpl} onChange={setTmpl} id="ct-tmpl" empty="Geen templates op deze storage" />
        </Field>
        <Section title="Toegang">
          <div className="grid gap-3 sm:grid-cols-2">
            <Field label="Wachtwoord van root" htmlFor="ct-pw" hint="Gaat versleuteld naar de host">
              <Input id="ct-pw" type="password" autoComplete="new-password" value={password} onChange={(e) => setPassword(e.target.value)} />
            </Field>
            <Field label="SSH-sleutels" htmlFor="ct-keys" hint="Eén per regel">
              <Textarea id="ct-keys" rows={2} value={keys} onChange={(e) => setKeys(e.target.value)} className="font-mono text-xs" />
            </Field>
          </div>
        </Section>
        <Section title="Hardware">
          <div className="grid gap-3 sm:grid-cols-3">
            <Field label="Cores" htmlFor="ct-cores">
              <Input id="ct-cores" inputMode="numeric" value={cores} onChange={(e) => setCores(e.target.value.replace(/\D/g, ""))} />
            </Field>
            <Field label="Geheugen (MB)" htmlFor="ct-mem">
              <Input id="ct-mem" inputMode="numeric" value={mem} onChange={(e) => setMem(e.target.value.replace(/\D/g, ""))} />
            </Field>
            <Field label="Swap (MB)" htmlFor="ct-swap">
              <Input id="ct-swap" inputMode="numeric" value={swap} onChange={(e) => setSwap(e.target.value.replace(/\D/g, ""))} />
            </Field>
          </div>
          <div className="grid gap-3 sm:grid-cols-2">
            <Field label="Schijf op" htmlFor="ct-st">
              <Select id="ct-st" value={store} onChange={(e) => setDisk(e.target.value)}>
                {rootStores.map((s) => (
                  <option key={s.storage} value={s.storage}>
                    {s.storage} ({s.type}{s.avail ? `, ${formatBytes(s.avail)} vrij` : ""})
                  </option>
                ))}
              </Select>
            </Field>
            <Field label="Grootte (GB)" htmlFor="ct-size">
              <Input id="ct-size" inputMode="numeric" value={size} onChange={(e) => setSize(e.target.value.replace(/\D/g, ""))} />
            </Field>
          </div>
        </Section>
        <Section title="Netwerk">
          <NetFields
            bridges={c.bits.bridges}
            bridge={c.bridge}
            setBridge={c.setBridge}
            vlan={vlan}
            setVlan={setVlan}
            ip={ip}
            setIP={setIP}
            gw={gw}
            setGW={setGW}
            dhcpLabel="Leeg: DHCP"
          />
          <Field label="DNS" htmlFor="ct-dns" hint="Leeg: die van de host">
            <Input id="ct-dns" value={dns} onChange={(e) => setDns(e.target.value)} placeholder="192.168.10.1" />
          </Field>
        </Section>
        <Section title="Opties">
          <Field label="Tags" htmlFor="ct-tags">
            <Input id="ct-tags" value={tags} onChange={(e) => setTags(e.target.value)} />
          </Field>
          <div className="flex flex-wrap gap-4 text-sm">
            <label className="inline-flex items-center gap-1.5">
              <input type="checkbox" checked={nesting} onChange={(e) => setNesting(e.target.checked)} />
              Nesting (systemd in nieuwe Debian)
            </label>
            <label className="inline-flex items-center gap-1.5">
              <input type="checkbox" checked={docker} onChange={(e) => setDocker(e.target.checked)} />
              Docker erin (keyctl)
            </label>
            <label className="inline-flex items-center gap-1.5">
              <input type="checkbox" checked={onboot} onChange={(e) => setOnboot(e.target.checked)} />
              Starten bij het opstarten van de host
            </label>
            <label className="inline-flex items-center gap-1.5">
              <input type="checkbox" checked={start} onChange={(e) => setStart(e.target.checked)} />
              Meteen starten
            </label>
          </div>
        </Section>
        {errs.length > 0 && (
          <Alert kind="info">
            <ul className="list-inside list-disc">
              {errs.map((e) => (
                <li key={e}>{e}</li>
              ))}
            </ul>
          </Alert>
        )}
        <div className="flex justify-end gap-2">
          <Button type="button" variant="secondary" onClick={onClose}>
            Annuleren
          </Button>
          <Button type="submit" disabled={pending || errs.length > 0}>
            CT maken
          </Button>
        </div>
      </form>
    </Modal>
  );
}
