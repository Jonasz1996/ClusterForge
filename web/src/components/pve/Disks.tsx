"use client";

import Link from "next/link";
import { useState, type ReactNode } from "react";
import { Empty, formatBytes, QueryState, tableClass, tdClass, thClass } from "@/components/inventory/bits";
import { ActionMenu, Meter, TypedConfirm, useCallRunner } from "@/components/pve/bits";
import { Check, FormModal } from "@/components/pve/guest/common";
import { Modal } from "@/components/Modal";
import { Alert, Badge, Button, Card, Field, Input, Select } from "@/components/ui";
import { useIsAdmin } from "@/lib/inventory";
import {
  diskHealthy,
  pveHostAction,
  usePerNode,
  usePveGet,
  type Datacenter,
  type DiskInfo,
  type LvmThin,
  type SmartData,
  type ZfsDetail,
  type ZfsPool,
  type ZfsVdev,
} from "@/lib/pve";

// Schijven van de hosts (fase 26 M5): SMART-gezondheid en slijtage, ZFS-pools
// met hun toestand, en nieuwe opslag maken op een lege schijf.

export function DiskHealthSummary({ dc, link = true }: { dc: Datacenter; link?: boolean }) {
  const nodes = dc.hosts.map((h) => h.pve_node);
  const disks = usePerNode<DiskInfo[]>(dc.id, nodes, "disks/list", 300_000);
  const pools = usePerNode<ZfsPool[]>(dc.id, nodes, "disks/zfs", 120_000);
  const bad = [
    ...disks.flatMap((n) => (n.data ?? []).filter((d) => !diskHealthy(d.health)).map((d) => `${n.node}: ${d.devpath} ${d.model ?? ""} SMART ${d.health}`)),
    ...disks.flatMap((n) =>
      (n.data ?? []).filter((d) => typeof d.wearout === "number" && d.wearout <= 10).map((d) => `${n.node}: ${d.devpath} nog ${d.wearout}% levensduur`),
    ),
    ...pools.flatMap((n) => (n.data ?? []).filter((p) => p.health !== "ONLINE").map((p) => `${n.node}: ZFS-pool ${p.name} is ${p.health}`)),
  ];
  if (bad.length === 0) return null;
  return (
    <Alert kind="error">
      <p className="font-medium">
        Schijven vragen aandacht{" "}
        {link && (
          <Link href={`/proxmox/opslag?dc=${encodeURIComponent(dc.id)}&tab=disks`} className="font-normal underline">
            Bekijken
          </Link>
        )}
      </p>
      <ul className="mt-1 list-disc pl-5">
        {bad.map((b) => (
          <li key={b}>{b}</li>
        ))}
      </ul>
    </Alert>
  );
}

export function DiskView({ dc }: { dc: Datacenter }) {
  const nodes = dc.hosts.map((h) => h.pve_node);
  const [node, setNode] = useState(nodes[0] ?? "");
  return (
    <div className="space-y-4">
      <DiskHealthSummary dc={dc} link={false} />
      {nodes.length > 1 && (
        <Field label="Host" htmlFor="dk-node">
          <Select id="dk-node" value={node} onChange={(e) => setNode(e.target.value)} className="max-w-xs">
            {nodes.map((n) => (
              <option key={n}>{n}</option>
            ))}
          </Select>
        </Field>
      )}
      {node && <NodeDisks key={node} dc={dc} node={node} />}
    </div>
  );
}

const usedWord = (u?: string) =>
  !u || u === "unused"
    ? "leeg"
    : ((
        {
          ZFS: "ZFS",
          LVM: "LVM",
          partitions: "partities",
          mounted: "gemount",
          "BIOS boot": "opstart",
          EFI: "EFI",
          "Device Mapper": "device mapper",
          "LVM-thin": "LVM-thin",
        } as Record<string, string>
      )[u] ?? u);

function NodeDisks({ dc, node }: { dc: Datacenter; node: string }) {
  const isAdmin = useIsAdmin();
  const disks = usePveGet<DiskInfo[]>(dc.id, `/nodes/${node}/disks/list`, {
    refetch: 120_000,
  });
  const zfs = usePveGet<ZfsPool[]>(dc.id, `/nodes/${node}/disks/zfs`, {
    refetch: 60_000,
  });
  const thin = usePveGet<LvmThin[]>(dc.id, `/nodes/${node}/disks/lvmthin`, {
    refetch: 60_000,
  });
  const { run, pending, banner } = useCallRunner(dc.id);
  const agent = dc.hosts.find((h) => h.pve_node === node);
  const [scrubMsg, setScrubMsg] = useState<{ ok: boolean; text: string; id?: string } | null>(null);
  const scrub = async (pool: string, stop: boolean) => {
    if (!agent) return;
    try {
      const j = await pveHostAction(agent.node_id, { op: { action: "zpool_scrub", scrub: { pool, stop } } });
      setScrubMsg({ ok: true, text: `Scrub van ${pool} ${stop ? "gestopt" : "gestart"}.`, id: j.id });
    } catch (e) {
      setScrubMsg({ ok: false, text: e instanceof Error ? e.message : "Mislukt" });
    }
  };
  const [dialog, setDialog] = useState<
    { kind: "smart"; d: DiskInfo } | { kind: "wipe"; d: DiskInfo } | { kind: "gpt"; d: DiskInfo } | { kind: "create" } | { kind: "pool"; name: string } | null
  >(null);
  const close = () => setDialog(null);
  const free = (disks.data ?? []).filter((d) => !d.used || d.used === "unused");

  return (
    <div className="space-y-4">
      {banner}
      {scrubMsg && (
        <Alert kind={scrubMsg.ok ? "success" : "error"}>
          {scrubMsg.text}{" "}
          {scrubMsg.id && (
            <Link href={`/taken/detail?id=${scrubMsg.id}`} className="underline">
              Volgen
            </Link>
          )}
        </Alert>
      )}
      <Card
        title={
          <span className="flex items-center justify-between gap-2">
            <span>Schijven op {node}</span>
            {isAdmin && (
              <Button variant="secondary" disabled={pending || free.length === 0} onClick={() => setDialog({ kind: "create" })}>
                Opslag maken op lege schijf
              </Button>
            )}
          </span>
        }
      >
        <QueryState q={disks}>
          {(disks.data ?? []).length === 0 ? (
            <Empty>Geen schijven gevonden.</Empty>
          ) : (
            <div className="relative -mx-4 overflow-x-auto sm:mx-0">
              <table className={tableClass}>
                <thead>
                  <tr>
                    <th className={thClass}>Schijf</th>
                    <th className={thClass}>Soort</th>
                    <th className={thClass}>Grootte</th>
                    <th className={thClass}>In gebruik</th>
                    <th className={thClass}>SMART</th>
                    <th className={thClass}>Slijtage</th>
                    <th className={thClass}>
                      <span className="sr-only">Acties</span>
                    </th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
                  {disks.data!.map((d) => {
                    const unused = !d.used || d.used === "unused";
                    return (
                      <tr key={d.devpath}>
                        <td className={tdClass}>
                          <span className="font-mono">{d.devpath}</span>
                          <span className="block text-xs text-slate-500">
                            {[d.vendor, d.model].filter(Boolean).join(" ")}
                            {d.serial ? ` · ${d.serial}` : ""}
                          </span>
                        </td>
                        <td className={tdClass}>
                          {d.type === "nvme" ? "NVMe" : d.type === "ssd" ? "SSD" : d.type === "hdd" ? `HDD${d.rpm ? ` ${d.rpm} rpm` : ""}` : (d.type ?? "–")}
                        </td>
                        <td className={tdClass + " tabular-nums"}>{formatBytes(d.size)}</td>
                        <td className={tdClass}>{unused ? <Badge tone="blue">leeg</Badge> : usedWord(d.used)}</td>
                        <td className={tdClass}>
                          <button type="button" className="hover:underline" onClick={() => setDialog({ kind: "smart", d })}>
                            <Badge tone={!d.health || d.health === "UNKNOWN" ? "slate" : diskHealthy(d.health) ? "green" : "red"}>
                              {!d.health || d.health === "UNKNOWN" ? "onbekend" : diskHealthy(d.health) ? "in orde" : d.health}
                            </Badge>
                          </button>
                        </td>
                        <td className={tdClass + " min-w-32"}>
                          {typeof d.wearout === "number" ? (
                            <Meter value={100 - d.wearout} warn={80} crit={90} label={`${d.devpath} slijtage`} />
                          ) : (
                            <span className="text-slate-500">–</span>
                          )}
                        </td>
                        <td className={tdClass + " text-right"}>
                          <ActionMenu
                            disabled={pending}
                            items={[
                              {
                                label: "SMART-gegevens",
                                onClick: () => setDialog({ kind: "smart", d }),
                              },
                              ...(isAdmin && unused && !d.gpt
                                ? [
                                    {
                                      label: "GPT geven…",
                                      onClick: () => setDialog({ kind: "gpt", d }),
                                    },
                                  ]
                                : []),
                              ...(isAdmin
                                ? [
                                    {
                                      label: "Wissen…",
                                      onClick: () => setDialog({ kind: "wipe", d }),
                                      danger: true,
                                    },
                                  ]
                                : []),
                            ]}
                          />
                        </td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </div>
          )}
        </QueryState>
        <p className="mt-3 text-xs text-slate-500">
          Slijtage is het verbruikte deel van de levensduur van een SSD volgens SMART. Een HDD heeft geen slijtagewaarde.
        </p>
      </Card>

      <Card title="ZFS-pools">
        <QueryState q={zfs}>
          {(zfs.data ?? []).length === 0 ? (
            <Empty>Geen ZFS op deze host.</Empty>
          ) : (
            <ul className="divide-y divide-slate-100 dark:divide-slate-800">
              {zfs.data!.map((p) => (
                <li key={p.name} className="flex flex-wrap items-center justify-between gap-3 py-2 text-sm">
                  <button type="button" className="font-medium hover:underline" onClick={() => setDialog({ kind: "pool", name: p.name })}>
                    {p.name}
                  </button>
                  <Badge tone={p.health === "ONLINE" ? "green" : p.health === "DEGRADED" ? "amber" : "red"}>{p.health}</Badge>
                  <div className="min-w-48 flex-1">
                    <Meter value={(p.alloc / p.size) * 100} warn={80} crit={90} label={`${p.name} vol`} />
                  </div>
                  <span className="text-xs text-slate-500 tabular-nums">
                    {formatBytes(p.alloc)} van {formatBytes(p.size)}
                    {p.frag !== undefined ? ` · fragmentatie ${p.frag}%` : ""}
                  </span>
                  <ActionMenu
                    disabled={pending}
                    items={[
                      { label: "Details", onClick: () => setDialog({ kind: "pool", name: p.name }) },
                      ...(isAdmin && agent
                        ? [
                            { label: "Scrub starten", onClick: () => void scrub(p.name, false) },
                            { label: "Scrub stoppen", onClick: () => void scrub(p.name, true) },
                          ]
                        : []),
                    ]}
                  />
                </li>
              ))}
            </ul>
          )}
        </QueryState>
      </Card>

      {(thin.data ?? []).length > 0 && (
        <Card title="LVM-thinpools">
          <ul className="divide-y divide-slate-100 text-sm dark:divide-slate-800">
            {thin.data!.map((t) => (
              <li key={`${t.vg}/${t.lv}`} className="flex flex-wrap items-center justify-between gap-3 py-2">
                <span className="font-medium">
                  {t.vg}/{t.lv}
                </span>
                <div className="min-w-48 flex-1">
                  <Meter value={(t.used / t.lv_size) * 100} label={`${t.lv} data`} />
                </div>
                {t.metadata_size ? (
                  <span className="text-xs text-slate-500">metadata {Math.round(((t.metadata_used ?? 0) / t.metadata_size) * 100)}%</span>
                ) : null}
              </li>
            ))}
          </ul>
          <p className="mt-2 text-xs text-slate-500">Loopt de metadata vol, dan gaan alle volumes op deze pool alleen-lezen. Hou ze onder 80%.</p>
        </Card>
      )}

      {dialog?.kind === "smart" && <SmartDialog dc={dc} node={node} d={dialog.d} onClose={close} />}
      {dialog?.kind === "pool" && <PoolDialog dc={dc} node={node} name={dialog.name} onClose={close} />}
      {dialog?.kind === "create" && <CreateDialog node={node} free={free} run={run} pending={pending} onClose={close} />}
      {(dialog?.kind === "wipe" || dialog?.kind === "gpt") && (
        <TypedConfirm
          title={dialog.kind === "wipe" ? `${dialog.d.devpath} wissen` : `${dialog.d.devpath} een GPT geven`}
          expect={dialog.d.devpath}
          confirmLabel={dialog.kind === "wipe" ? "Wissen" : "GPT geven"}
          pending={pending}
          onClose={close}
          onConfirm={(typed) =>
            void run(
              dialog.kind === "wipe"
                ? {
                    method: "set",
                    path: `/nodes/${node}/disks/wipedisk`,
                    params: { disk: dialog.d.devpath },
                    confirm: typed,
                  }
                : {
                    method: "create",
                    path: `/nodes/${node}/disks/initgpt`,
                    params: { disk: dialog.d.devpath },
                    confirm: typed,
                  },
            ).then((ok) => ok && close())
          }
        >
          <p className="text-sm">
            {dialog.kind === "wipe"
              ? `Alles op ${dialog.d.devpath} (${formatBytes(dialog.d.size)}, ${usedWord(dialog.d.used)}) wordt gewist: partities, bestandssystemen en ZFS/LVM-labels. Proxmox weigert als de schijf nog in gebruik is.`
              : `${dialog.d.devpath} krijgt een lege GPT-partitietabel.`}
          </p>
        </TypedConfirm>
      )}
    </div>
  );
}

function SmartDialog({ dc, node, d, onClose }: { dc: Datacenter; node: string; d: DiskInfo; onClose: () => void }) {
  const q = usePveGet<SmartData>(dc.id, `/nodes/${node}/disks/smart`, {
    params: { disk: d.devpath },
    refetch: false,
  });
  const important = /reallocated|pending|uncorrect|crc|wear|percent|spare|media|error|temperature|power_on/i;
  return (
    <Modal title={`SMART van ${d.devpath}`} onClose={onClose} wide>
      <QueryState q={q}>
        <div className="space-y-3">
          <p className="text-sm">
            Uitslag: <Badge tone={diskHealthy(q.data?.health) ? "green" : "red"}>{q.data?.health ?? "onbekend"}</Badge>
          </p>
          {q.data?.attributes?.length ? (
            <div className="relative -mx-4 max-h-96 overflow-auto sm:mx-0">
              <table className={tableClass}>
                <thead>
                  <tr>
                    <th className={thClass}>Kenmerk</th>
                    <th className={thClass}>Waarde</th>
                    <th className={thClass}>Slechtste</th>
                    <th className={thClass}>Drempel</th>
                    <th className={thClass}>Ruw</th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
                  {q.data.attributes.map((a) => (
                    <tr key={`${a.id}-${a.name}`} className={important.test(a.name) ? "font-medium" : ""}>
                      <td className={tdClass}>
                        {a.name}
                        {a.fail && a.fail !== "-" ? <span className="ml-2 text-xs text-red-700 dark:text-red-300">{a.fail}</span> : null}
                      </td>
                      <td className={tdClass + " tabular-nums"}>{a.value}</td>
                      <td className={tdClass + " tabular-nums"}>{a.worst}</td>
                      <td className={tdClass + " tabular-nums"}>{a.threshold}</td>
                      <td className={tdClass + " font-mono text-xs"}>{a.raw}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          ) : q.data?.text ? (
            <pre className="max-h-96 overflow-auto rounded bg-slate-100 p-3 text-xs dark:bg-slate-900">{q.data.text}</pre>
          ) : (
            <p className="text-sm text-slate-500">Deze schijf geeft geen SMART-gegevens (bijvoorbeeld een virtuele schijf of een RAID-controller).</p>
          )}
        </div>
      </QueryState>
    </Modal>
  );
}

function PoolDialog({ dc, node, name, onClose }: { dc: Datacenter; node: string; name: string; onClose: () => void }) {
  const q = usePveGet<ZfsDetail>(dc.id, `/nodes/${node}/disks/zfs/${name}`, {
    refetch: 30_000,
  });
  return (
    <Modal title={`ZFS-pool ${name}`} onClose={onClose} wide>
      <QueryState q={q}>
        <div className="space-y-3 text-sm">
          <p>
            Toestand: <Badge tone={q.data?.state === "ONLINE" ? "green" : q.data?.state === "DEGRADED" ? "amber" : "red"}>{q.data?.state}</Badge>
          </p>
          {q.data?.status && <p>{q.data.status}</p>}
          {q.data?.action && <p className="text-amber-800 dark:text-amber-300">{q.data.action}</p>}
          {q.data?.scan && (
            <p>
              <span className="text-slate-500">Laatste scrub/resilver: </span>
              {q.data.scan}
            </p>
          )}
          {q.data?.errors && (
            <p>
              <span className="text-slate-500">Fouten: </span>
              {q.data.errors}
            </p>
          )}
          <div className="relative -mx-4 overflow-x-auto sm:mx-0">
            <table className={tableClass}>
              <thead>
                <tr>
                  <th className={thClass}>Onderdeel</th>
                  <th className={thClass}>Toestand</th>
                  <th className={thClass}>Lezen</th>
                  <th className={thClass}>Schrijven</th>
                  <th className={thClass}>Checksum</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-slate-100 dark:divide-slate-800">{(q.data?.children ?? []).flatMap((c) => vdevRows(c, 0))}</tbody>
            </table>
          </div>
        </div>
      </QueryState>
    </Modal>
  );
}

function vdevRows(v: ZfsVdev, depth: number): ReactNode[] {
  const errs = (v.read ?? 0) + (v.write ?? 0) + (v.cksum ?? 0);
  return [
    <tr key={`${depth}-${v.name}`}>
      <td className={tdClass + " font-mono text-xs"} style={{ paddingLeft: `${depth * 1.25 + 0.75}rem` }}>
        {v.name}
        {v.msg && <span className="block font-sans text-slate-500">{v.msg}</span>}
      </td>
      <td className={tdClass}>{v.state && <Badge tone={v.state === "ONLINE" ? "green" : v.state === "DEGRADED" ? "amber" : "red"}>{v.state}</Badge>}</td>
      <td className={tdClass + (errs ? " text-red-700 dark:text-red-300" : "")}>{v.read ?? 0}</td>
      <td className={tdClass + (errs ? " text-red-700 dark:text-red-300" : "")}>{v.write ?? 0}</td>
      <td className={tdClass + (errs ? " text-red-700 dark:text-red-300" : "")}>{v.cksum ?? 0}</td>
    </tr>,
    ...(v.children ?? []).flatMap((c) => vdevRows(c, depth + 1)),
  ];
}

type Runner = ReturnType<typeof useCallRunner>["run"];

const minDisks: Record<string, number> = {
  single: 1,
  mirror: 2,
  raid10: 4,
  raidz: 3,
  raidz2: 4,
  raidz3: 5,
};

function CreateDialog({ node, free, run, pending, onClose }: { node: string; free: DiskInfo[]; run: Runner; pending: boolean; onClose: () => void }) {
  const [kind, setKind] = useState<"zfs" | "lvmthin" | "lvm" | "directory">("zfs");
  const [name, setName] = useState("");
  const [picked, setPicked] = useState<string[]>([]);
  const [raid, setRaid] = useState("mirror");
  const [compression, setCompression] = useState("lz4");
  const [ashift, setAshift] = useState("12");
  const [fs, setFs] = useState("ext4");
  const [addStorage, setAddStorage] = useState(true);
  const [confirm, setConfirm] = useState(false);
  const many = kind === "zfs";
  const devices = many ? picked : picked.slice(0, 1);
  const error = !/^[A-Za-z][A-Za-z0-9_.-]{1,60}$/.test(name)
    ? "Geef een naam (letters, cijfers, - _ .)."
    : devices.length === 0
      ? "Kies een lege schijf."
      : many && devices.length < minDisks[raid]
        ? `${raid} heeft minstens ${minDisks[raid]} schijven nodig.`
        : many && raid === "raid10" && devices.length % 2
          ? "raid10 heeft een even aantal schijven nodig."
          : null;
  const params = (): Record<string, string> => {
    const p: Record<string, string> = {
      name,
      add_storage: addStorage ? "1" : "0",
    };
    if (kind === "zfs")
      Object.assign(p, {
        devices: devices.join(","),
        raidlevel: raid,
        compression,
        ashift,
      });
    else p.device = devices[0];
    if (kind === "directory") p.filesystem = fs;
    return p;
  };
  if (confirm)
    return (
      <TypedConfirm
        title={`Opslag ${name} maken`}
        expect={name}
        confirmLabel="Maken"
        pending={pending}
        onClose={onClose}
        onConfirm={(typed) =>
          void run({
            method: "create",
            path: `/nodes/${node}/disks/${kind}`,
            params: params(),
            confirm: typed,
          }).then((ok) => ok && onClose())
        }
      >
        <p className="text-sm">Alles op {devices.join(", ")} wordt gewist om er de nieuwe opslag op te maken.</p>
      </TypedConfirm>
    );
  return (
    <FormModal title={`Opslag maken op ${node}`} onClose={onClose} onSubmit={() => setConfirm(true)} submitLabel="Verder" disabled={pending} error={error} wide>
      <div className="grid gap-3 sm:grid-cols-2">
        <Field label="Soort" htmlFor="dk-kind">
          <Select id="dk-kind" value={kind} onChange={(e) => setKind(e.target.value as typeof kind)}>
            <option value="zfs">ZFS (aanbevolen; replicatie en snapshots)</option>
            <option value="lvmthin">LVM-thin</option>
            <option value="lvm">LVM</option>
            <option value="directory">Map (ext4/xfs)</option>
          </Select>
        </Field>
        <Field label="Naam" htmlFor="dk-name" hint="Wordt ook de naam van de opslag in Proxmox">
          <Input id="dk-name" value={name} onChange={(e) => setName(e.target.value)} />
        </Field>
        {kind === "zfs" && (
          <>
            <Field label="Opbouw" htmlFor="dk-raid">
              <Select id="dk-raid" value={raid} onChange={(e) => setRaid(e.target.value)}>
                <option value="single">Eén schijf (geen bescherming)</option>
                <option value="mirror">Spiegel (mirror)</option>
                <option value="raid10">RAID10 (gespiegelde paren)</option>
                <option value="raidz">RAIDZ1</option>
                <option value="raidz2">RAIDZ2</option>
                <option value="raidz3">RAIDZ3</option>
              </Select>
            </Field>
            <Field label="Compressie" htmlFor="dk-comp">
              <Select id="dk-comp" value={compression} onChange={(e) => setCompression(e.target.value)}>
                <option value="lz4">lz4</option>
                <option value="zstd">zstd</option>
                <option value="off">uit</option>
              </Select>
            </Field>
            <Field label="ashift" htmlFor="dk-ashift" hint="12 past bij bijna elke moderne schijf (4K-sectoren)">
              <Select id="dk-ashift" value={ashift} onChange={(e) => setAshift(e.target.value)}>
                <option>9</option>
                <option>12</option>
                <option>13</option>
              </Select>
            </Field>
          </>
        )}
        {kind === "directory" && (
          <Field label="Bestandssysteem" htmlFor="dk-fs">
            <Select id="dk-fs" value={fs} onChange={(e) => setFs(e.target.value)}>
              <option>ext4</option>
              <option>xfs</option>
            </Select>
          </Field>
        )}
      </div>
      <fieldset className="space-y-1">
        <legend className="text-sm font-medium">{many ? "Schijven" : "Schijf"}</legend>
        {free.map((d) => (
          <Check
            key={d.devpath}
            id={`dk-${d.devpath}`}
            checked={many ? picked.includes(d.devpath) : picked[0] === d.devpath}
            onChange={(v) => setPicked(many ? (v ? [...picked, d.devpath] : picked.filter((x) => x !== d.devpath)) : v ? [d.devpath] : [])}
          >
            <span className="font-mono">{d.devpath}</span> {formatBytes(d.size)} {d.model}
          </Check>
        ))}
      </fieldset>
      <Check id="dk-add" checked={addStorage} onChange={setAddStorage}>
        Meteen als opslag aan Proxmox toevoegen
      </Check>
    </FormModal>
  );
}
