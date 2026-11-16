"use client";

import Link from "next/link";
import { useRouter, useSearchParams } from "next/navigation";
import { Suspense, useState } from "react";
import { Modal } from "@/components/Modal";
import { Empty, QueryState, tableClass, tdClass, thClass } from "@/components/inventory/bits";
import { DatacenterGate, TypedConfirm, useCallRunner } from "@/components/pve/bits";
import { Check } from "@/components/pve/guest/common";
import { isGPU, passable, pciFit } from "@/components/pve/guest/Passthrough";
import { Alert, Badge, Button, Card, cx, Field, Input, PageHeader, Select } from "@/components/ui";
import { useIsAdmin } from "@/lib/inventory";
import { pveHostAction, usePveHardware, type Datacenter, type PveHardware } from "@/lib/pve";
import { useQueryClient } from "@tanstack/react-query";

// Hardware van één Proxmox-host (fase 26): PCI per IOMMU-groep met of het
// geschikt is voor passthrough, USB, temperaturen en ECC. Passthrough
// instellen zet IOMMU en vfio-pci op de host; daarna vraagt de pagina om een
// herstart.

export default function HardwarePage() {
  return (
    <div className="space-y-6">
      <PageHeader
        title="Hardware"
        description="Wat er in elke Proxmox-host zit, wat je kunt doorgeven aan een VM, en temperaturen en geheugenfouten. De agent op de host leest het uit."
      />
      <Suspense>
        <DatacenterGate>{(dc) => <HardwareView key={dc.id} dc={dc} />}</DatacenterGate>
      </Suspense>
    </div>
  );
}

function HardwareView({ dc }: { dc: Datacenter }) {
  const sp = useSearchParams();
  const router = useRouter();
  const hosts = dc.hosts.filter((h) => h.supported);
  const node = hosts.find((h) => h.pve_node === sp.get("node"))?.pve_node ?? hosts[0]?.pve_node ?? "";
  const host = hosts.find((h) => h.pve_node === node);
  const hw = usePveHardware(host?.node_id ?? "", !!host?.online);
  const setNode = (n: string) => router.replace(`/proxmox/hardware?dc=${encodeURIComponent(dc.id)}&node=${encodeURIComponent(n)}`);

  return (
    <div className="space-y-6">
      <div className="flex flex-wrap items-center gap-2 text-sm">
        <label htmlFor="hw-node" className="text-slate-500">
          Host
        </label>
        <Select id="hw-node" value={node} onChange={(e) => setNode(e.target.value)} className="w-auto">
          {hosts.map((h) => (
            <option key={h.pve_node} value={h.pve_node}>
              {h.pve_node}
              {!h.online ? " (offline)" : ""}
            </option>
          ))}
        </Select>
      </div>
      {!host ? (
        <Alert>Geen host met een agent.</Alert>
      ) : !host.online ? (
        <Alert>De agent op {host.pve_node} is offline.</Alert>
      ) : (
        <QueryState q={hw}>{hw.data && <HostHardware dc={dc} node={node} nodeId={host.node_id} hw={hw.data} />}</QueryState>
      )}
    </div>
  );
}

function HostHardware({ dc, node, nodeId, hw }: { dc: Datacenter; node: string; nodeId: string; hw: PveHardware }) {
  const isAdmin = useIsAdmin();
  const { run, pending, banner } = useCallRunner(dc.id);
  const [dialog, setDialog] = useState<"setup" | "reboot" | null>(null);
  const [selected, setSelected] = useState<Set<string>>(() => new Set((hw.configured.vfio_ids ?? [])));
  const groups = new Map<number, PveHardware["pci"]>();
  for (const d of hw.pci.filter(passable)) {
    const g = groups.get(d.iommu_group) ?? [];
    g.push(d);
    groups.set(d.iommu_group, g);
  }
  const ordered = [...groups.entries()].sort((a, b) => a[0] - b[0]);
  const toggle = (id: string) =>
    setSelected((s) => {
      const n = new Set(s);
      if (n.has(id)) n.delete(id);
      else n.add(id);
      return n;
    });
  const hot = hw.temps.filter((t) => t.c >= (t.high ?? t.crit ?? 90) * 0.95);
  const ecc = hw.edac.reduce((a, e) => ({ ce: a.ce + e.corrected, ue: a.ue + e.uncorrected }), { ce: 0, ue: 0 });

  return (
    <div className="space-y-6">
      {banner}
      {hw.reboot_required && (
        <Alert kind="info">
          <span className="flex flex-wrap items-center justify-between gap-2">
            <span>
              <strong>{node} moet herstarten</strong> voor de nieuwe instellingen werken: {(hw.reboot_reasons ?? []).join("; ")}.
            </span>
            {isAdmin && (
              <Button variant="secondary" disabled={pending} onClick={() => setDialog("reboot")}>
                Host herstarten…
              </Button>
            )}
          </span>
        </Alert>
      )}

      <div className="grid gap-6 lg:grid-cols-2">
        <Card title="Passthrough op deze host">
          <dl className="grid grid-cols-2 gap-x-6 gap-y-2 text-sm">
            <Item label="Processor" value={hw.cpu_model || hw.cpu_vendor} />
            <Item label="IOMMU" value={hw.iommu ? `aan (${hw.iommu_groups} groepen)` : "uit"} tone={hw.iommu ? "green" : "amber"} />
            <Item label="Opstartlader" value={hw.boot === "systemd-boot" ? "systemd-boot (/etc/kernel/cmdline)" : "GRUB (/etc/default/grub)"} />
            <Item label="vfio-modules" value={hw.modules.filter((m) => m.startsWith("vfio")).join(", ") || "niet geladen"} />
            <Item label="vfio-pci voor" value={(hw.configured.vfio_ids ?? []).join(", ") || "niets"} />
            <Item label="Uitgeschakelde drivers" value={(hw.configured.blacklist ?? []).join(", ") || "geen"} />
          </dl>
          <p className="mt-3 font-mono text-xs break-all text-slate-500">{hw.cmdline}</p>
          {hw.configured.cmdline && hw.configured.cmdline !== hw.cmdline && (
            <p className="mt-1 font-mono text-xs break-all text-amber-700 dark:text-amber-300">na herstart: {hw.configured.cmdline}</p>
          )}
          {isAdmin && (
            <div className="mt-4 flex justify-end">
              <Button disabled={pending} onClick={() => setDialog("setup")}>
                Passthrough instellen…
              </Button>
            </div>
          )}
        </Card>

        <Card title="Temperaturen en geheugen">
          {hot.length > 0 && (
            <div className="mb-3">
              <Alert>Heet: {hot.map((t) => `${t.label} ${Math.round(t.c)} °C`).join(", ")}.</Alert>
            </div>
          )}
          {hw.edac.length > 0 ? (
            <p className="mb-3 text-sm">
              ECC:{" "}
              {ecc.ue > 0 ? (
                <Badge tone="red">{ecc.ue} niet te herstellen fouten</Badge>
              ) : ecc.ce > 0 ? (
                <Badge tone="amber">{ecc.ce} herstelde fouten</Badge>
              ) : (
                <Badge tone="green">geen fouten</Badge>
              )}
            </p>
          ) : (
            <p className="mb-3 text-sm text-slate-500">Geen ECC-gegevens (geen ECC-geheugen, of geen EDAC-driver).</p>
          )}
          {hw.temps.length === 0 ? (
            <Empty>Geen temperatuursensoren gevonden.</Empty>
          ) : (
            <ul className="grid gap-x-6 gap-y-1 text-sm sm:grid-cols-2">
              {hw.temps.map((t, i) => (
                <li key={`${t.chip}-${t.label}-${i}`} className="flex justify-between gap-2">
                  <span className="truncate text-slate-600 dark:text-slate-300" title={t.chip}>
                    {t.label}
                  </span>
                  <span className={cx("tabular-nums", hot.includes(t) && "font-semibold text-red-700 dark:text-red-300")}>{Math.round(t.c)} °C</span>
                </li>
              ))}
            </ul>
          )}
        </Card>
      </div>

      <Card title="PCI-apparaten per IOMMU-groep">
        <p className="mb-3 text-sm text-slate-500">
          Een VM krijgt altijd een hele IOMMU-groep. Vink aan wat bij het opstarten naar vfio-pci moet (zodat de host het niet meer gebruikt) en kies dan
          &quot;Passthrough instellen&quot;. Doorgeven aan een VM doe je op de pagina van de VM, tabblad Passthrough.
        </p>
        <div className="relative -mx-4 overflow-x-auto sm:mx-0">
          <table className={tableClass}>
            <thead>
              <tr>
                <th className={thClass}>
                  <span className="sr-only">vfio</span>
                </th>
                <th className={thClass}>Groep</th>
                <th className={thClass}>Adres</th>
                <th className={thClass}>Apparaat</th>
                <th className={thClass}>ID</th>
                <th className={thClass}>Driver</th>
                <th className={thClass}>Geschikt</th>
              </tr>
            </thead>
            <tbody>
              {ordered.map(([g, devs]) =>
                devs.map((d, i) => {
                  const id = `${d.vendor}:${d.device}`;
                  const fit = pciFit(d, hw);
                  return (
                    <tr key={d.address} className={cx(i === 0 && "border-t border-slate-200 dark:border-slate-700")}>
                      <td className={tdClass}>
                        {isAdmin && !d.host_use && (
                          <input type="checkbox" aria-label={`vfio-pci voor ${id}`} checked={selected.has(id)} onChange={() => toggle(id)} />
                        )}
                      </td>
                      <td className={tdClass + " tabular-nums"}>{i === 0 ? (g >= 0 ? g : "–") : ""}</td>
                      <td className={tdClass + " font-mono text-xs whitespace-nowrap"}>{d.address}</td>
                      <td className={tdClass}>
                        {d.device_name ?? d.class_name ?? d.class}
                        <span className="block text-xs text-slate-500">
                          {[d.vendor_name, d.class_name, d.mdev && "vGPU/SR-IOV"].filter(Boolean).join(" · ")}
                        </span>
                      </td>
                      <td className={tdClass + " font-mono text-xs whitespace-nowrap"}>{id}</td>
                      <td className={tdClass + " font-mono text-xs"}>{d.driver ?? "–"}</td>
                      <td className={tdClass}>
                        <Badge tone={fit.tone}>{fit.text}</Badge>
                      </td>
                    </tr>
                  );
                }),
              )}
            </tbody>
          </table>
        </div>
      </Card>

      <Card title="USB-apparaten">
        {hw.usb.length === 0 ? (
          <Empty>Geen USB-apparaten.</Empty>
        ) : (
          <div className="relative -mx-4 overflow-x-auto sm:mx-0">
            <table className={tableClass}>
              <thead>
                <tr>
                  <th className={thClass}>Poort</th>
                  <th className={thClass}>Apparaat</th>
                  <th className={thClass}>ID</th>
                  <th className={thClass}>Snelheid</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
                {hw.usb.map((u) => (
                  <tr key={`${u.bus}-${u.port}`}>
                    <td className={tdClass + " font-mono text-xs"}>
                      {u.bus}-{u.port}
                    </td>
                    <td className={tdClass}>
                      {u.name ?? "onbekend"}
                      {u.manufacturer && <span className="block text-xs text-slate-500">{u.manufacturer}</span>}
                    </td>
                    <td className={tdClass + " font-mono text-xs"}>
                      {u.vendor}:{u.product}
                    </td>
                    <td className={tdClass + " text-xs"}>{u.speed ? `${u.speed} Mbit/s` : "–"}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Card>

      {dialog === "setup" && <SetupDialog node={node} nodeId={nodeId} hw={hw} selected={[...selected]} onClose={() => setDialog(null)} />}
      {dialog === "reboot" && (
        <TypedConfirm
          title={`${node} herstarten`}
          expect={node}
          confirmLabel="Herstarten"
          pending={pending}
          onClose={() => setDialog(null)}
          onConfirm={(typed) =>
            void run({ method: "create", path: `/nodes/${node}/status`, params: { command: "reboot" }, confirm: typed }).then((ok) => ok && setDialog(null))
          }
        >
          <p>Alle gasten op {node} stoppen tijdens de herstart, tenzij je ze eerst verhuist of de host in onderhoudsmodus zet (Datacenter → Hosts).</p>
        </TypedConfirm>
      )}
      <p className="text-xs text-slate-500">
        Onderhoud en herstarten van hosts: <Link href={`/proxmox/datacenter?dc=${encodeURIComponent(dc.id)}`} className="underline">Datacenter</Link>.
      </p>
    </div>
  );
}

function Item({ label, value, tone }: { label: string; value: string; tone?: "green" | "amber" }) {
  return (
    <div className="min-w-0">
      <dt className="text-xs text-slate-500">{label}</dt>
      <dd className={cx("font-medium break-words", tone === "amber" && "text-amber-700 dark:text-amber-300", tone === "green" && "text-emerald-700 dark:text-emerald-300")}>{value}</dd>
    </div>
  );
}

// Drivers die de host loslaat als een kaart naar vfio gaat. Alleen
// voorgesteld als geen ander apparaat van de host ze nog nodig heeft.
const gpuDrivers = ["nouveau", "nvidia", "nvidiafb", "radeon", "amdgpu", "i915", "xe", "snd_hda_intel"];

function SetupDialog({ node, nodeId, hw, selected, onClose }: { node: string; nodeId: string; hw: PveHardware; selected: string[]; onClose: () => void }) {
  const qc = useQueryClient();
  const chosen = hw.pci.filter((d) => selected.includes(`${d.vendor}:${d.device}`));
  const others = hw.pci.filter((d) => !selected.includes(`${d.vendor}:${d.device}`));
  const suggest = [...new Set(chosen.map((d) => d.driver).filter((x): x is string => !!x && x !== "vfio-pci" && gpuDrivers.includes(x)))].filter(
    (drv) => !others.some((o) => o.driver === drv),
  );
  const [iommu, setIommu] = useState(true);
  const [blacklist, setBlacklist] = useState([...new Set([...(hw.configured.blacklist ?? []), ...suggest])].join(", "));
  const [typed, setTyped] = useState("");
  const [state, setState] = useState<{ ok: boolean; text: string } | null>(null);
  const [busy, setBusy] = useState(false);
  const bl = blacklist.split(/[\s,]+/).filter(Boolean);
  const shared = chosen.filter((d) => d.group_size > 1);
  const hostUse = chosen.filter((d) => d.host_use);
  const error = bl.some((b) => !/^[a-z0-9_]{2,32}$/.test(b))
    ? "Drivers zijn namen zoals nouveau of snd_hda_intel."
    : !iommu && selected.length === 0
      ? "Kies IOMMU of minstens één apparaat."
      : hostUse.length > 0
        ? `De host gebruikt ${hostUse.map((d) => d.address).join(", ")} zelf (${hostUse.map((d) => d.host_use).join(", ")}); dat kan niet naar vfio.`
        : null;
  const submit = async () => {
    setBusy(true);
    try {
      const j = await pveHostAction(nodeId, { op: { action: "passthrough", passthrough: { iommu, vfio_ids: selected, blacklist: bl } }, confirm: typed.trim() });
      setState({ ok: true, text: `Taak gestart: ${j.title}. Daarna vraagt deze pagina om een herstart.` });
      qc.invalidateQueries({ queryKey: ["jobs"] });
      setTimeout(() => qc.invalidateQueries({ queryKey: ["pve", "hardware", nodeId] }), 15_000);
    } catch (e) {
      setState({ ok: false, text: e instanceof Error ? e.message : "Mislukt" });
    } finally {
      setBusy(false);
    }
  };
  return (
    <Modal title={`Passthrough instellen op ${node}`} onClose={onClose}>
      {state?.ok ? (
        <div className="space-y-4">
          <Alert kind="success">{state.text}</Alert>
          <div className="flex justify-end">
            <Button onClick={onClose}>Sluiten</Button>
          </div>
        </div>
      ) : (
        <form
          className="space-y-4"
          onSubmit={(e) => {
            e.preventDefault();
            if (!error && typed.trim() === node) void submit();
          }}
        >
          <Check id="su-iommu" checked={iommu} onChange={setIommu}>
            IOMMU aanzetten op de kernelregel ({hw.cpu_vendor.includes("AMD") ? "iommu=pt" : "intel_iommu=on iommu=pt"}) en de vfio-modules laden
          </Check>
          <div className="text-sm">
            <p className="font-medium">vfio-pci neemt bij het opstarten:</p>
            {chosen.length === 0 ? (
              <p className="text-slate-500">niets (vink apparaten aan in de lijst)</p>
            ) : (
              <ul className="mt-1 list-inside list-disc">
                {chosen.map((d) => (
                  <li key={d.address}>
                    <span className="font-mono text-xs">
                      {d.vendor}:{d.device}
                    </span>{" "}
                    {d.device_name ?? d.class_name} {isGPU(d) && <Badge>videokaart</Badge>}
                  </li>
                ))}
              </ul>
            )}
          </div>
          {shared.length > 0 && (
            <Alert kind="info">
              {shared.map((d) => d.address).join(", ")} {shared.length === 1 ? "deelt" : "delen"} een IOMMU-groep met andere apparaten. Een VM krijgt de hele groep;
              vink dan alles uit die groep aan (behalve PCI-bruggen).
            </Alert>
          )}
          <Field label="Drivers uitschakelen" htmlFor="su-bl" hint="Met komma's. Alleen drivers die de host zelf niet meer nodig heeft.">
            <Input id="su-bl" value={blacklist} onChange={(e) => setBlacklist(e.target.value)} className="font-mono" />
          </Field>
          <p className="text-xs text-slate-500">
            De agent past de kernelregel aan, schrijft /etc/modprobe.d/clusterforge-vfio.conf, bouwt de initramfs opnieuw en werkt de opstartlader bij. Vorige
            bestanden bewaart hij bij de taak. Het werkt pas na een herstart van de host.
          </p>
          <Field label={`Tik ${node} over om te bevestigen`} htmlFor="su-typed">
            <Input id="su-typed" value={typed} onChange={(e) => setTyped(e.target.value)} autoComplete="off" spellCheck={false} />
          </Field>
          {(error || state) && <Alert>{error ?? state?.text}</Alert>}
          <div className="flex justify-end gap-2">
            <Button type="button" variant="secondary" onClick={onClose}>
              Annuleren
            </Button>
            <Button type="submit" variant="danger" disabled={!!error || busy || typed.trim() !== node}>
              Instellen
            </Button>
          </div>
        </form>
      )}
    </Modal>
  );
}
