"use client";

import { useSearchParams } from "next/navigation";
import { Suspense, useState } from "react";
import { askConfirm } from "@/components/Confirm";
import { Empty, QueryState, tableClass, tdClass, thClass } from "@/components/inventory/bits";
import { ActionMenu, DatacenterGate, TypedConfirm, useCallRunner } from "@/components/pve/bits";
import { Check, FormModal } from "@/components/pve/guest/common";
import { Tabs } from "@/components/Tabs";
import { Alert, Badge, Button, Card, Field, Input, PageHeader, Select } from "@/components/ui";
import { useIsAdmin } from "@/lib/inventory";
import { isGatewayAddress, usePveGet, type Datacenter, type NetIface, type SdnSubnet, type SdnVnet, type SdnZone } from "@/lib/pve";

// Netwerk van de Proxmox-hosts (fase 26 M6): bridges, bonds en VLAN's per
// host, en SDN (zones, VNets, subnets) voor het hele datacenter. Proxmox zet
// wijzigingen klaar; pas "Toepassen" maakt ze echt.

export default function NetworkPage() {
  return (
    <div className="space-y-6">
      <PageHeader
        title="Netwerk"
        description="Bridges, bonds en VLAN's van elke host, en SDN voor het datacenter. Wijzigingen staan klaar tot je ze toepast, zodat je ze eerst kunt nakijken."
      />
      <Suspense>
        <DatacenterGate>{(dc) => <NetworkView key={dc.id} dc={dc} />}</DatacenterGate>
      </Suspense>
    </div>
  );
}

type Tab = "ifaces" | "sdn";

function NetworkView({ dc }: { dc: Datacenter }) {
  const t = useSearchParams().get("tab");
  const [tab, setTab] = useState<Tab>(t === "sdn" ? "sdn" : "ifaces");
  const nodes = dc.hosts.map((h) => h.pve_node);
  const [node, setNode] = useState(nodes[0] ?? "");
  return (
    <div className="space-y-4">
      <Tabs
        value={tab}
        onChange={setTab}
        label="Netwerk"
        tabs={[
          { key: "ifaces", label: "Interfaces per host" },
          { key: "sdn", label: "SDN" },
        ]}
      />
      {tab === "ifaces" && (
        <>
          {nodes.length > 1 && (
            <Field label="Host" htmlFor="nw-node">
              <Select id="nw-node" value={node} onChange={(e) => setNode(e.target.value)} className="max-w-xs">
                {nodes.map((n) => (
                  <option key={n}>{n}</option>
                ))}
              </Select>
            </Field>
          )}
          {node && <Interfaces key={node} dc={dc} node={node} />}
        </>
      )}
      {tab === "sdn" && <Sdn dc={dc} />}
    </div>
  );
}

const typeWord: Record<string, string> = { bridge: "Bridge", bond: "Bond", eth: "Netwerkkaart", vlan: "VLAN", OVSBridge: "OVS-bridge", OVSBond: "OVS-bond", OVSPort: "OVS-poort", OVSIntPort: "OVS-intern", alias: "Alias", unknown: "Onbekend" };

function Interfaces({ dc, node }: { dc: Datacenter; node: string }) {
  const isAdmin = useIsAdmin();
  const q = usePveGet<NetIface[]>(dc.id, `/nodes/${node}/network`, { refetch: 30_000 });
  const { run, pending, banner } = useCallRunner(dc.id);
  const [dialog, setDialog] = useState<{ kind: "add"; type: string } | { kind: "edit"; i: NetIface } | { kind: "apply" } | null>(null);
  const close = () => setDialog(null);
  const list = [...(q.data ?? [])].sort((a, b) => order(a) - order(b) || a.iface.localeCompare(b.iface, undefined, { numeric: true }));

  const remove = async (i: NetIface) => {
    if (!(await askConfirm(`${i.iface} weghalen? Dat staat klaar tot je de wijzigingen toepast.`))) return;
    void run({ method: "delete", path: `/nodes/${node}/network/${i.iface}` });
  };

  return (
    <div className="space-y-4">
      {banner}
      <Card
        title={
          <span className="flex flex-wrap items-center justify-between gap-2">
            <span>Interfaces van {node}</span>
            {isAdmin && (
              <span className="flex flex-wrap gap-2">
                <Button variant="secondary" disabled={pending} onClick={() => setDialog({ kind: "add", type: "bridge" })}>
                  Bridge
                </Button>
                <Button variant="secondary" disabled={pending} onClick={() => setDialog({ kind: "add", type: "vlan" })}>
                  VLAN
                </Button>
                <Button variant="secondary" disabled={pending} onClick={() => setDialog({ kind: "add", type: "bond" })}>
                  Bond
                </Button>
              </span>
            )}
          </span>
        }
      >
        <QueryState q={q}>
          {list.length === 0 ? (
            <Empty>Geen interfaces gevonden.</Empty>
          ) : (
            <div className="relative -mx-4 overflow-x-auto sm:mx-0">
              <table className={tableClass}>
                <thead>
                  <tr>
                    <th className={thClass}>Naam</th>
                    <th className={thClass}>Soort</th>
                    <th className={thClass}>Adres</th>
                    <th className={thClass}>Gateway</th>
                    <th className={thClass}>Poorten</th>
                    <th className={thClass}>Status</th>
                    <th className={thClass}>
                      <span className="sr-only">Acties</span>
                    </th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
                  {list.map((i) => (
                    <tr key={i.iface}>
                      <td className={tdClass}>
                        <span className="font-mono font-medium">{i.iface}</span>
                        {i.comments && <span className="block text-xs text-slate-500">{i.comments}</span>}
                      </td>
                      <td className={tdClass}>
                        {typeWord[i.type] ?? i.type}
                        {i.bridge_vlan_aware ? <span className="block text-xs text-slate-500">VLAN-bewust</span> : null}
                        {i.type === "bond" && i.bond_mode ? <span className="block text-xs text-slate-500">{i.bond_mode}</span> : null}
                      </td>
                      <td className={tdClass + " font-mono text-xs"}>
                        {i.cidr ?? (i.method === "dhcp" ? "DHCP" : "–")}
                        {i.cidr6 && <span className="block">{i.cidr6}</span>}
                      </td>
                      <td className={tdClass + " font-mono text-xs"}>{i.gateway ?? "–"}</td>
                      <td className={tdClass + " font-mono text-xs"}>{i.bridge_ports || i.slaves || i["vlan-raw-device"] || "–"}</td>
                      <td className={tdClass}>
                        {i.active ? <Badge tone="green">actief</Badge> : <Badge tone="slate">niet actief</Badge>}
                        {!i.autostart && i.type !== "eth" ? <span className="block text-xs text-slate-500">start niet vanzelf</span> : null}
                      </td>
                      <td className={tdClass + " text-right"}>
                        {isAdmin && i.type !== "eth" && i.type !== "unknown" && (
                          <ActionMenu
                            disabled={pending}
                            items={[
                              { label: "Wijzigen", onClick: () => setDialog({ kind: "edit", i }) },
                              { label: "Weghalen", onClick: () => void remove(i), danger: true },
                            ]}
                          />
                        )}
                        {isAdmin && i.type === "eth" && (
                          <ActionMenu disabled={pending} items={[{ label: "Wijzigen", onClick: () => setDialog({ kind: "edit", i }) }]} />
                        )}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </QueryState>
        {isAdmin && (
          <div className="mt-4 flex flex-wrap items-center gap-2 border-t border-slate-100 pt-4 dark:border-slate-800">
            <Button disabled={pending} onClick={() => setDialog({ kind: "apply" })}>
              Wijzigingen toepassen
            </Button>
            <Button
              variant="secondary"
              disabled={pending}
              onClick={async () => {
                if (await askConfirm(`Alle klaargezette netwerkwijzigingen op ${node} vergeten?`)) void run({ method: "delete", path: `/nodes/${node}/network` });
              }}
            >
              Klaargezette wijzigingen vergeten
            </Button>
            <span className="text-xs text-slate-500">Wijzigingen worden pas actief na toepassen (ifreload, zonder herstart).</span>
          </div>
        )}
      </Card>
      {dialog?.kind === "add" && <IfaceDialog node={node} type={dialog.type} existing={q.data ?? []} run={run} pending={pending} onClose={close} />}
      {dialog?.kind === "edit" && <IfaceDialog node={node} type={dialog.i.type} edit={dialog.i} existing={q.data ?? []} run={run} pending={pending} onClose={close} />}
      {dialog?.kind === "apply" && (
        <TypedConfirm
          title={`Netwerk van ${node} toepassen`}
          expect={node}
          confirmLabel="Toepassen"
          pending={pending}
          onClose={close}
          onConfirm={(typed) => void run({ method: "set", path: `/nodes/${node}/network`, confirm: typed }).then((ok) => ok && close())}
        >
          <p className="text-sm">
            Proxmox herlaadt het netwerk van {node}. Klopt een adres of gateway niet, dan is de host (en de agent) niet meer bereikbaar en moet je het op de console
            rechtzetten. Kijk de lijst dus eerst na.
          </p>
        </TypedConfirm>
      )}
    </div>
  );
}

const order = (i: NetIface) => ({ eth: 0, bond: 1, bridge: 2, vlan: 3 })[i.type] ?? 4;

type Runner = ReturnType<typeof useCallRunner>["run"];

function IfaceDialog({ node, type, edit, existing, run, pending, onClose }: { node: string; type: string; edit?: NetIface; existing: NetIface[]; run: Runner; pending: boolean; onClose: () => void }) {
  const nextName = (prefix: string) => {
    for (let n = 0; n < 4095; n++) if (!existing.some((e) => e.iface === `${prefix}${n}`)) return `${prefix}${n}`;
    return prefix;
  };
  const [name, setName] = useState(edit?.iface ?? (type === "bridge" ? nextName("vmbr") : type === "bond" ? nextName("bond") : ""));
  const [cidr, setCidr] = useState(edit?.cidr ?? "");
  const [gateway, setGateway] = useState(edit?.gateway ?? "");
  const [ports, setPorts] = useState(edit?.bridge_ports ?? edit?.slaves ?? "");
  const [vlanAware, setVlanAware] = useState(!!edit?.bridge_vlan_aware);
  const [bondMode, setBondMode] = useState(edit?.bond_mode ?? "active-backup");
  const [raw, setRaw] = useState(edit?.["vlan-raw-device"] ?? existing.find((e) => e.type === "bridge")?.iface ?? "");
  const [tag, setTag] = useState(edit?.["vlan-id"] ? String(edit["vlan-id"]) : "");
  const [mtu, setMtu] = useState(edit?.mtu ? String(edit.mtu) : "");
  const [autostart, setAutostart] = useState(edit ? !!edit.autostart : true);
  const [comments, setComments] = useState(edit?.comments ?? "");
  const vlanName = type === "vlan" && !edit ? (raw && tag ? `${raw}.${tag}` : "") : name;
  const iface = type === "vlan" ? vlanName : name;
  const otherGw = existing.find((e) => e.gateway && e.iface !== edit?.iface);

  const error =
    !edit && !/^[A-Za-z][A-Za-z0-9_.]{1,20}$/.test(iface)
      ? type === "vlan"
        ? "Kies de onderliggende interface en een VLAN-tag."
        : "Geef een naam (bijvoorbeeld vmbr1)."
      : !edit && existing.some((e) => e.iface === iface)
        ? `${iface} bestaat al.`
        : type === "bridge" && !/^vmbr\d{1,4}$/.test(iface)
          ? "Een bridge heet vmbrN (bijvoorbeeld vmbr1)."
          : type === "vlan" && !edit && !(Number(tag) >= 1 && Number(tag) <= 4094)
            ? "Een VLAN-tag ligt tussen 1 en 4094."
            : cidr && !/^\d{1,3}(\.\d{1,3}){3}\/\d{1,2}$/.test(cidr)
              ? "Geef het adres als CIDR, bijvoorbeeld 192.168.10.11/24."
              : isGatewayAddress(cidr)
                ? "Adressen .1 tot .3 zijn voor de gateway en de firewalls (CARP). Kies een ander adres."
                : gateway && !cidr
                  ? "Een gateway heeft een adres op dezelfde interface nodig."
                  : gateway && otherGw
                    ? `Er is al een standaardgateway op ${otherGw.iface}; Proxmox laat er maar één toe.`
                    : mtu && !(Number(mtu) >= 576 && Number(mtu) <= 65520)
                      ? "MTU ligt tussen 576 en 65520."
                      : type === "bond" && !ports.trim()
                        ? "Een bond heeft minstens één netwerkkaart nodig."
                        : null;

  const submit = () => {
    const p: Record<string, string> = { type, autostart: autostart ? "1" : "0" };
    const del: string[] = [];
    const opt = (k: string, v: string) => {
      if (v) p[k] = v;
      else if (edit) del.push(k);
    };
    opt("cidr", cidr);
    opt("gateway", gateway);
    opt("mtu", mtu);
    opt("comments", comments);
    if (type === "bridge") {
      opt("bridge_ports", ports.trim());
      p.bridge_vlan_aware = vlanAware ? "1" : "0";
    }
    if (type === "bond") {
      p.slaves = ports.trim();
      p.bond_mode = bondMode;
    }
    if (type === "vlan" && !edit) {
      p["vlan-raw-device"] = raw;
      p["vlan-id"] = tag;
    }
    if (del.length) p.delete = del.join(",");
    const call = edit
      ? { method: "set" as const, path: `/nodes/${node}/network/${edit.iface}`, params: p }
      : { method: "create" as const, path: `/nodes/${node}/network`, params: { ...p, iface } };
    void run(call).then((ok) => ok && onClose());
  };

  const eths = existing.filter((e) => e.type === "eth" || e.type === "bond").map((e) => e.iface);
  return (
    <FormModal
      title={edit ? `${edit.iface} wijzigen` : type === "bridge" ? "Bridge toevoegen" : type === "bond" ? "Bond toevoegen" : "VLAN toevoegen"}
      onClose={onClose}
      onSubmit={submit}
      submitLabel="Klaarzetten"
      disabled={pending}
      error={error}
      wide
    >
      <div className="grid gap-3 sm:grid-cols-2">
        {type !== "vlan" || edit ? (
          <Field label="Naam" htmlFor="if-n">
            <Input id="if-n" value={name} disabled={!!edit} onChange={(e) => setName(e.target.value)} className="font-mono" />
          </Field>
        ) : (
          <>
            <Field label="Op interface" htmlFor="if-raw">
              <Select id="if-raw" value={raw} onChange={(e) => setRaw(e.target.value)}>
                {existing
                  .filter((e) => ["bridge", "bond", "eth"].includes(e.type))
                  .map((e) => (
                    <option key={e.iface}>{e.iface}</option>
                  ))}
              </Select>
            </Field>
            <Field label="VLAN-tag" htmlFor="if-tag" hint={vlanName ? `Wordt ${vlanName}` : undefined}>
              <Input id="if-tag" inputMode="numeric" value={tag} onChange={(e) => setTag(e.target.value.replace(/\D/g, ""))} />
            </Field>
          </>
        )}
        {type === "bridge" && (
          <Field label="Poorten" htmlFor="if-p" hint={`Netwerkkaarten of bonds, met spaties. Beschikbaar: ${eths.join(", ") || "geen"}`}>
            <Input id="if-p" value={ports} onChange={(e) => setPorts(e.target.value)} className="font-mono" />
          </Field>
        )}
        {type === "bond" && (
          <>
            <Field label="Netwerkkaarten" htmlFor="if-s" hint={`Met spaties. Beschikbaar: ${existing.filter((e) => e.type === "eth").map((e) => e.iface).join(", ")}`}>
              <Input id="if-s" value={ports} onChange={(e) => setPorts(e.target.value)} className="font-mono" />
            </Field>
            <Field label="Modus" htmlFor="if-bm">
              <Select id="if-bm" value={bondMode} onChange={(e) => setBondMode(e.target.value)}>
                <option value="active-backup">active-backup (werkt op elke switch)</option>
                <option value="802.3ad">LACP (802.3ad, switch moet meedoen)</option>
                <option value="balance-alb">balance-alb</option>
                <option value="balance-tlb">balance-tlb</option>
                <option value="balance-rr">balance-rr</option>
                <option value="balance-xor">balance-xor</option>
                <option value="broadcast">broadcast</option>
              </Select>
            </Field>
          </>
        )}
        <Field label="Adres (CIDR)" htmlFor="if-c" hint="Leeg voor een bridge alleen voor gasten">
          <Input id="if-c" value={cidr} onChange={(e) => setCidr(e.target.value.trim())} className="font-mono" placeholder="192.168.10.11/24" />
        </Field>
        <Field label="Gateway" htmlFor="if-g" hint="Alleen op de interface naar buiten">
          <Input id="if-g" value={gateway} onChange={(e) => setGateway(e.target.value.trim())} className="font-mono" placeholder="192.168.10.1" />
        </Field>
        <Field label="MTU" htmlFor="if-m" hint="Leeg is 1500">
          <Input id="if-m" inputMode="numeric" value={mtu} onChange={(e) => setMtu(e.target.value.replace(/\D/g, ""))} />
        </Field>
        <Field label="Opmerking" htmlFor="if-cm">
          <Input id="if-cm" value={comments} onChange={(e) => setComments(e.target.value)} />
        </Field>
      </div>
      {type === "bridge" && (
        <Check id="if-va" checked={vlanAware} onChange={setVlanAware}>
          VLAN-bewust: gasten kiezen zelf hun VLAN-tag op deze bridge
        </Check>
      )}
      <Check id="if-as" checked={autostart} onChange={setAutostart}>
        Vanzelf starten bij opstarten
      </Check>
    </FormModal>
  );
}

function Sdn({ dc }: { dc: Datacenter }) {
  const isAdmin = useIsAdmin();
  const zones = usePveGet<SdnZone[]>(dc.id, "/cluster/sdn/zones", { refetch: 30_000 });
  const vnets = usePveGet<SdnVnet[]>(dc.id, "/cluster/sdn/vnets", { refetch: 30_000 });
  const { run, pending, banner } = useCallRunner(dc.id);
  const [dialog, setDialog] = useState<{ kind: "zone" } | { kind: "vnet" } | { kind: "subnet"; v: SdnVnet } | { kind: "apply" } | null>(null);
  const close = () => setDialog(null);
  const pendingChanges = [...(zones.data ?? []), ...(vnets.data ?? [])].some((x) => x.state && x.state !== "available");

  const remove = async (what: string, path: string) => {
    if (await askConfirm(`${what} weghalen? Gasten die dit gebruiken verliezen hun netwerk zodra je toepast.`)) void run({ method: "delete", path });
  };

  return (
    <div className="space-y-4">
      {banner}
      {pendingChanges && <Alert kind="info">Er staan SDN-wijzigingen klaar. Ze werken pas na &quot;SDN toepassen&quot;.</Alert>}
      <Card
        title={
          <span className="flex items-center justify-between gap-2">
            <span>Zones</span>
            {isAdmin && (
              <Button variant="secondary" disabled={pending} onClick={() => setDialog({ kind: "zone" })}>
                Zone toevoegen
              </Button>
            )}
          </span>
        }
      >
        <QueryState q={zones}>
          {(zones.data ?? []).length === 0 ? (
            <Empty>Nog geen SDN-zones. Een VLAN-zone op vmbr0 is de eenvoudigste start.</Empty>
          ) : (
            <ul className="divide-y divide-slate-100 text-sm dark:divide-slate-800">
              {zones.data!.map((z) => (
                <li key={z.zone} className="flex flex-wrap items-center justify-between gap-2 py-2">
                  <span>
                    <span className="font-medium">{z.zone}</span> <span className="text-slate-500">{z.type}</span>
                    {z.bridge && <span className="text-slate-500"> op {z.bridge}</span>}
                    {z.state && z.state !== "available" && (
                      <span className="ml-2">
                        <Badge tone="amber">{z.state}</Badge>
                      </span>
                    )}
                  </span>
                  {isAdmin && (
                    <Button variant="secondary" disabled={pending} onClick={() => void remove(`Zone ${z.zone}`, `/cluster/sdn/zones/${z.zone}`)}>
                      Weghalen
                    </Button>
                  )}
                </li>
              ))}
            </ul>
          )}
        </QueryState>
      </Card>

      <Card
        title={
          <span className="flex items-center justify-between gap-2">
            <span>VNets</span>
            {isAdmin && (
              <Button variant="secondary" disabled={pending || (zones.data ?? []).length === 0} onClick={() => setDialog({ kind: "vnet" })}>
                VNet toevoegen
              </Button>
            )}
          </span>
        }
      >
        <QueryState q={vnets}>
          {(vnets.data ?? []).length === 0 ? (
            <Empty>Nog geen VNets.</Empty>
          ) : (
            <div className="relative -mx-4 overflow-x-auto sm:mx-0">
              <table className={tableClass}>
                <thead>
                  <tr>
                    <th className={thClass}>VNet</th>
                    <th className={thClass}>Zone</th>
                    <th className={thClass}>Tag</th>
                    <th className={thClass}>Subnets</th>
                    <th className={thClass}>
                      <span className="sr-only">Acties</span>
                    </th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
                  {vnets.data!.map((v) => (
                    <tr key={v.vnet}>
                      <td className={tdClass}>
                        <span className="font-medium">{v.vnet}</span>
                        {v.alias && <span className="block text-xs text-slate-500">{v.alias}</span>}
                      </td>
                      <td className={tdClass}>{v.zone}</td>
                      <td className={tdClass}>{v.tag ?? "–"}</td>
                      <td className={tdClass}>
                        <Subnets dc={dc} vnet={v.vnet} />
                      </td>
                      <td className={tdClass + " text-right"}>
                        {isAdmin && (
                          <ActionMenu
                            disabled={pending}
                            items={[
                              { label: "Subnet toevoegen", onClick: () => setDialog({ kind: "subnet", v }) },
                              { label: "Weghalen", onClick: () => void remove(`VNet ${v.vnet}`, `/cluster/sdn/vnets/${v.vnet}`), danger: true },
                            ]}
                          />
                        )}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </QueryState>
      </Card>
      {isAdmin && (
        <Button disabled={pending} onClick={() => setDialog({ kind: "apply" })}>
          SDN toepassen
        </Button>
      )}
      {dialog?.kind === "zone" && <ZoneDialog taken={(zones.data ?? []).map((z) => z.zone)} nodes={dc.hosts.map((h) => h.pve_node)} run={run} pending={pending} onClose={close} />}
      {dialog?.kind === "vnet" && <VnetDialog zones={zones.data ?? []} taken={(vnets.data ?? []).map((v) => v.vnet)} run={run} pending={pending} onClose={close} />}
      {dialog?.kind === "subnet" && <SubnetDialog vnet={dialog.v} run={run} pending={pending} onClose={close} />}
      {dialog?.kind === "apply" && (
        <TypedConfirm
          title="SDN toepassen"
          expect="sdn"
          confirmLabel="Toepassen"
          pending={pending}
          onClose={close}
          onConfirm={(typed) => void run({ method: "set", path: "/cluster/sdn", confirm: typed }).then((ok) => ok && close())}
        >
          <p className="text-sm">Elke host herlaadt zijn netwerk met de nieuwe zones en VNets. Gasten op een weggehaald VNet verliezen hun netwerk.</p>
        </TypedConfirm>
      )}
    </div>
  );
}

function Subnets({ dc, vnet }: { dc: Datacenter; vnet: string }) {
  const q = usePveGet<SdnSubnet[]>(dc.id, `/cluster/sdn/vnets/${vnet}/subnets`, { refetch: 60_000 });
  if (!q.data?.length) return <span className="text-slate-500">–</span>;
  return (
    <ul className="font-mono text-xs">
      {q.data.map((s) => (
        <li key={s.subnet}>
          {s.cidr ?? s.subnet}
          {s.gateway ? ` via ${s.gateway}` : ""}
          {s.snat ? " (SNAT)" : ""}
        </li>
      ))}
    </ul>
  );
}

function ZoneDialog({ taken, nodes, run, pending, onClose }: { taken: string[]; nodes: string[]; run: Runner; pending: boolean; onClose: () => void }) {
  const [zone, setZone] = useState("");
  const [type, setType] = useState("vlan");
  const [bridge, setBridge] = useState("vmbr0");
  const [tag, setTag] = useState("");
  const error = !/^[a-z][a-z0-9]{0,7}$/.test(zone)
    ? "Een zonenaam is 1 tot 8 kleine letters of cijfers en begint met een letter."
    : taken.includes(zone)
      ? "Die zone bestaat al."
      : type !== "simple" && !bridge
        ? "Geef de bridge."
        : type === "qinq" && !(Number(tag) >= 1 && Number(tag) <= 4094)
          ? "Geef de buitenste VLAN-tag."
          : null;
  const submit = () => {
    const p: Record<string, string> = { zone, type, nodes: nodes.join(",") };
    if (type !== "simple") p.bridge = bridge;
    if (type === "qinq") p.tag = tag;
    void run({ method: "create", path: "/cluster/sdn/zones", params: p }).then((ok) => ok && onClose());
  };
  return (
    <FormModal title="Zone toevoegen" onClose={onClose} onSubmit={submit} submitLabel="Klaarzetten" disabled={pending} error={error}>
      <div className="grid gap-3 sm:grid-cols-2">
        <Field label="Naam" htmlFor="z-n">
          <Input id="z-n" value={zone} onChange={(e) => setZone(e.target.value)} />
        </Field>
        <Field label="Soort" htmlFor="z-t">
          <Select id="z-t" value={type} onChange={(e) => setType(e.target.value)}>
            <option value="vlan">VLAN (tags op een bestaande bridge)</option>
            <option value="simple">Simple (los netwerk per host, met NAT)</option>
            <option value="qinq">QinQ</option>
          </Select>
        </Field>
        {type !== "simple" && (
          <Field label="Bridge" htmlFor="z-b">
            <Input id="z-b" value={bridge} onChange={(e) => setBridge(e.target.value)} className="font-mono" />
          </Field>
        )}
        {type === "qinq" && (
          <Field label="Buitenste tag" htmlFor="z-tag">
            <Input id="z-tag" inputMode="numeric" value={tag} onChange={(e) => setTag(e.target.value.replace(/\D/g, ""))} />
          </Field>
        )}
      </div>
    </FormModal>
  );
}

function VnetDialog({ zones, taken, run, pending, onClose }: { zones: SdnZone[]; taken: string[]; run: Runner; pending: boolean; onClose: () => void }) {
  const [vnet, setVnet] = useState("");
  const [zone, setZone] = useState(zones[0]?.zone ?? "");
  const [tag, setTag] = useState("");
  const [alias, setAlias] = useState("");
  const z = zones.find((x) => x.zone === zone);
  const needsTag = z?.type === "vlan" || z?.type === "qinq" || z?.type === "vxlan";
  const error = !/^[a-z][a-z0-9]{0,7}$/.test(vnet)
    ? "Een VNet-naam is 1 tot 8 kleine letters of cijfers en begint met een letter."
    : taken.includes(vnet)
      ? "Dat VNet bestaat al."
      : needsTag && !(Number(tag) >= 1 && Number(tag) <= 16777215)
        ? "Geef de VLAN-tag van dit VNet."
        : null;
  const submit = () => {
    const p: Record<string, string> = { vnet, zone };
    if (needsTag) p.tag = tag;
    if (alias) p.alias = alias;
    void run({ method: "create", path: "/cluster/sdn/vnets", params: p }).then((ok) => ok && onClose());
  };
  return (
    <FormModal title="VNet toevoegen" onClose={onClose} onSubmit={submit} submitLabel="Klaarzetten" disabled={pending} error={error}>
      <div className="grid gap-3 sm:grid-cols-2">
        <Field label="Naam" htmlFor="v-n" hint="Gasten kiezen dit als bridge">
          <Input id="v-n" value={vnet} onChange={(e) => setVnet(e.target.value)} />
        </Field>
        <Field label="Zone" htmlFor="v-z">
          <Select id="v-z" value={zone} onChange={(e) => setZone(e.target.value)}>
            {zones.map((x) => (
              <option key={x.zone}>{x.zone}</option>
            ))}
          </Select>
        </Field>
        {needsTag && (
          <Field label="VLAN-tag" htmlFor="v-t">
            <Input id="v-t" inputMode="numeric" value={tag} onChange={(e) => setTag(e.target.value.replace(/\D/g, ""))} />
          </Field>
        )}
        <Field label="Beschrijving" htmlFor="v-a" hint="Bijvoorbeeld Zabbix-VLAN">
          <Input id="v-a" value={alias} onChange={(e) => setAlias(e.target.value)} />
        </Field>
      </div>
    </FormModal>
  );
}

function SubnetDialog({ vnet, run, pending, onClose }: { vnet: SdnVnet; run: Runner; pending: boolean; onClose: () => void }) {
  const [cidr, setCidr] = useState("");
  const [gateway, setGateway] = useState("");
  const [snat, setSnat] = useState(false);
  const error = !/^\d{1,3}(\.\d{1,3}){3}\/\d{1,2}$/.test(cidr) ? "Geef het subnet als CIDR, bijvoorbeeld 192.168.199.0/24." : gateway && !/^\d{1,3}(\.\d{1,3}){3}$/.test(gateway) ? "De gateway is een IPv4-adres." : null;
  const submit = () => {
    const p: Record<string, string> = { subnet: cidr, type: "subnet" };
    if (gateway) p.gateway = gateway;
    if (snat) p.snat = "1";
    void run({ method: "create", path: `/cluster/sdn/vnets/${vnet.vnet}/subnets`, params: p }).then((ok) => ok && onClose());
  };
  return (
    <FormModal title={`Subnet op ${vnet.vnet}`} onClose={onClose} onSubmit={submit} submitLabel="Klaarzetten" disabled={pending} error={error}>
      <div className="grid gap-3 sm:grid-cols-2">
        <Field label="Subnet" htmlFor="s-c">
          <Input id="s-c" value={cidr} onChange={(e) => setCidr(e.target.value.trim())} className="font-mono" />
        </Field>
        <Field label="Gateway" htmlFor="s-g" hint="Bij een VLAN-zone is dat de firewall (.1)">
          <Input id="s-g" value={gateway} onChange={(e) => setGateway(e.target.value.trim())} className="font-mono" />
        </Field>
      </div>
      <Check id="s-snat" checked={snat} onChange={setSnat}>
        SNAT: gasten gaan via het adres van de host naar buiten (alleen bij een simple-zone)
      </Check>
    </FormModal>
  );
}
