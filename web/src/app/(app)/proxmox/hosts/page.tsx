"use client";

import { askConfirm } from "@/components/Confirm";
import Link from "next/link";
import { useMemo, useState } from "react";
import { JobList } from "@/components/jobs/JobList";
import { Modal } from "@/components/Modal";
import { ago, Empty, formatBytes, formatDuration, QueryState, Tags, tableClass, tdClass, thClass } from "@/components/inventory/bits";
import { Alert, Badge, Button, Card, cx, Field, Input, PageHeader, Select, Textarea } from "@/components/ui";
import { useIsAdmin } from "@/lib/inventory";
import { percent, useJobs, vmStatusInfo, type Job } from "@/lib/proxmox";
import {
  defaultServerURL,
  pveTypeLabel,
  usePveAction,
  usePveAvailable,
  usePveHosts,
  usePveInventory,
  type PveCreate,
  type PveGuest,
  type PveHost,
  type PveInventory,
  type PveRun,
} from "@/lib/pvehosts";

type Result = { kind: "success"; job: Job } | { kind: "error"; message: string } | null;
type Submit = (v: { op: PveRun; confirm?: string; server_url?: string }) => Promise<boolean>;

export default function PveHostsPage() {
  const hosts = usePveHosts();
  const isAdmin = useIsAdmin();
  const [picked, setPicked] = useState("");
  const list = hosts.data?.hosts ?? [];
  const current = list.find((h) => h.node_id === picked) ?? list.find((h) => h.online && h.supported) ?? list[0];

  return (
    <div className="space-y-6">
      <PageHeader
        title="Proxmox-hosts"
        description="Hosts met een ClusterForge-agent: containers en VM's starten, stoppen, aanpassen, snapshotten en verhuizen met pct en qm, en nieuwe CT's maken met de agent er meteen in."
        actions={
          <Link href="/proxmox" className="text-sm text-brand-700 hover:underline dark:text-brand-300">
            ← Proxmox-koppelingen
          </Link>
        }
      />
      <QueryState q={hosts}>
        {hosts.data && (
          <>
            {hosts.data.missing.length > 0 && (
              <Alert kind="info">
                Nog zonder agent: {hosts.data.missing.map((m) => `${m.pve_node} (${m.connection_name})`).join(", ")}. Installeer de agent
                één keer op die host via{" "}
                <Link href="/nodes" className="underline">
                  Nodes → Agent installeren
                </Link>{" "}
                (als root op de host); daarna staat hij hier.
              </Alert>
            )}
            {list.length === 0 ? (
              <Empty>
                Nog geen Proxmox-host met een agent. Installeer de agent op een host via Nodes → Agent installeren; ClusterForge
                herkent een Proxmox-host vanzelf.
              </Empty>
            ) : (
              <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
                {list.map((h) => (
                  <button
                    key={h.node_id}
                    type="button"
                    onClick={() => setPicked(h.node_id)}
                    className={cx(
                      "rounded-lg border p-3 text-left transition",
                      current?.node_id === h.node_id
                        ? "border-brand-500 bg-brand-50 dark:bg-brand-950/30"
                        : "border-slate-200 hover:bg-slate-50 dark:border-slate-800 dark:hover:bg-slate-900",
                    )}
                  >
                    <div className="flex items-center justify-between gap-2">
                      <span className="font-medium">{h.pve_node}</span>
                      {!h.online ? <Badge tone="red">Offline</Badge> : !h.supported ? <Badge tone="amber">Agent te oud</Badge> : <Badge tone="green">Klaar</Badge>}
                    </div>
                    <div className="mt-1 text-xs text-slate-500">
                      {[h.cluster && `cluster ${h.cluster}`, h.version && `PVE ${h.version}`, h.agent_version && `agent ${h.agent_version}`]
                        .filter(Boolean)
                        .join(" · ")}
                    </div>
                  </button>
                ))}
              </div>
            )}
            {current && <HostDetail key={current.node_id} host={current} hosts={list} isAdmin={isAdmin} protocol={hosts.data.protocol} />}
          </>
        )}
      </QueryState>
    </div>
  );
}

function HostDetail({ host, hosts, isAdmin, protocol }: { host: PveHost; hosts: PveHost[]; isAdmin: boolean; protocol: number }) {
  const ready = host.online && host.supported;
  const inv = usePveInventory(host.node_id, ready);
  const jobs = useJobs({ node_id: host.node_id, limit: 10 });
  const action = usePveAction(host.node_id);
  const [result, setResult] = useState<Result>(null);
  const [creating, setCreating] = useState(false);

  const submit: Submit = async (v) => {
    try {
      const job = await action.mutateAsync(v);
      setResult({ kind: "success", job });
      return true;
    } catch (e) {
      setResult({ kind: "error", message: e instanceof Error ? e.message : "Actie mislukt" });
      return false;
    }
  };

  if (!host.online) return <Alert kind="error">De agent op {host.pve_node} is offline sinds {ago(host.heartbeat_at)}.</Alert>;
  if (!host.supported)
    return (
      <Alert kind="info">
        De agent op {host.pve_node} spreekt protocol {host.protocol}; Proxmox-beheer vraagt {protocol}. Werk hem bij via{" "}
        <Link href="/nodes" className="underline">
          Nodes → Agents bijwerken
        </Link>
        .
      </Alert>
    );

  // Verhuizen kan alleen binnen hetzelfde Proxmox-cluster.
  const targets = hosts.filter((h) => h.node_id !== host.node_id && h.cluster && h.cluster === host.cluster).map((h) => h.pve_node);

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h2 className="text-lg font-semibold">{host.pve_node}</h2>
        {isAdmin && inv.data && <Button onClick={() => setCreating(true)}>CT maken</Button>}
      </div>
      {result?.kind === "success" && (
        <Alert kind="success">
          Taak gestart: {result.job.title}. Volg hem hieronder bij Recente taken.
        </Alert>
      )}
      {result?.kind === "error" && <Alert kind="error">{result.message}</Alert>}
      <QueryState q={inv}>
        {inv.data && (
          <>
            <GuestTable inv={inv.data} isAdmin={isAdmin} targets={targets} pending={action.isPending} submit={submit} />
            <div className="grid gap-4 lg:grid-cols-2">
              <StorageCard inv={inv.data} />
              <TemplatesCard host={host} inv={inv.data} isAdmin={isAdmin} pending={action.isPending} submit={submit} />
            </div>
            {creating && <CreateCTDialog inv={inv.data} pending={action.isPending} submit={submit} onClose={() => setCreating(false)} />}
          </>
        )}
      </QueryState>
      <Card title="Recente taken">
        <JobList jobs={jobs.data ?? []} />
      </Card>
    </div>
  );
}

type Dialog = "settings" | "resize" | "snapshots" | "migrate" | "bootstrap" | "destroy";

function GuestTable({
  inv,
  isAdmin,
  targets,
  pending,
  submit,
}: {
  inv: PveInventory;
  isAdmin: boolean;
  targets: string[];
  pending: boolean;
  submit: Submit;
}) {
  const [filter, setFilter] = useState("");
  const [open, setOpen] = useState<{ guest: PveGuest; dialog: Dialog } | null>(null);
  const guests = useMemo(() => {
    const f = filter.trim().toLowerCase();
    return [...inv.guests]
      .filter((g) => !f || g.name.toLowerCase().includes(f) || String(g.vmid).includes(f) || (g.tags ?? []).some((t) => t.includes(f)))
      .sort((a, b) => a.vmid - b.vmid);
  }, [inv.guests, filter]);

  const quick = async (g: PveGuest, a: PveRun["action"], ask?: string) => {
    if (ask && !await askConfirm(ask)) return;
    void submit({ op: { action: a, type: g.type, vmid: g.vmid } });
  };

  const items = (g: PveGuest) => {
    const name = g.name || String(g.vmid);
    const running = g.status === "running";
    const out: { label: string; onClick: () => void; danger?: boolean }[] = [];
    if (g.template) {
      out.push({ label: "Instellingen…", onClick: () => setOpen({ guest: g, dialog: "settings" }) });
      out.push({ label: "Verwijderen…", onClick: () => setOpen({ guest: g, dialog: "destroy" }), danger: true });
      return out;
    }
    if (running) {
      out.push({ label: "Afsluiten", onClick: () => quick(g, "shutdown", `${name} netjes afsluiten?`) });
      out.push({ label: "Herstarten", onClick: () => quick(g, "reboot", `${name} herstarten?`) });
      if (g.type === "qemu") out.push({ label: "Pauzeren", onClick: () => quick(g, "suspend") });
    } else if (g.status === "paused" || g.status === "suspended") {
      out.push({ label: "Hervatten", onClick: () => quick(g, "resume") });
    } else {
      out.push({ label: "Starten", onClick: () => quick(g, "start") });
    }
    out.push({ label: "Instellingen…", onClick: () => setOpen({ guest: g, dialog: "settings" }) });
    out.push({ label: "Schijf vergroten…", onClick: () => setOpen({ guest: g, dialog: "resize" }) });
    out.push({ label: "Snapshots…", onClick: () => setOpen({ guest: g, dialog: "snapshots" }) });
    if (targets.length > 0) out.push({ label: "Verhuizen…", onClick: () => setOpen({ guest: g, dialog: "migrate" }) });
    if (g.type === "lxc") out.push({ label: "Agent erin zetten…", onClick: () => setOpen({ guest: g, dialog: "bootstrap" }) });
    if (running || g.status === "paused" || g.status === "suspended")
      out.push({
        label: "Hard uitzetten",
        onClick: () => quick(g, "stop", `${name} hard uitzetten? Dat is de stekker eruit trekken.`),
        danger: true,
      });
    else out.push({ label: "Verwijderen…", onClick: () => setOpen({ guest: g, dialog: "destroy" }), danger: true });
    return out;
  };

  return (
    <Card title={`Containers en VM's (${inv.guests.filter((g) => !g.template).length})`}>
      <div className="mb-3 max-w-xs">
        <Input placeholder="Zoeken op naam, ID of tag" value={filter} onChange={(e) => setFilter(e.target.value)} />
      </div>
      {guests.length === 0 ? (
        <Empty>Niets gevonden.</Empty>
      ) : (
        <div className="overflow-x-auto">
          <table className={tableClass}>
            <thead className="border-b border-slate-200 dark:border-slate-800">
              <tr>
                <th className={thClass}>ID</th>
                <th className={thClass}>Naam</th>
                <th className={thClass}>Status</th>
                <th className={thClass}>CPU</th>
                <th className={thClass}>RAM</th>
                <th className={thClass}>Schijven</th>
                <th className={thClass}>Netwerk</th>
                <th className={thClass}>Tags</th>
                {isAdmin && <th className={thClass} />}
              </tr>
            </thead>
            <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
              {guests.map((g) => {
                const st = vmStatusInfo(g.status);
                return (
                  <tr key={`${g.type}-${g.vmid}`}>
                    <td className={cx(tdClass, "font-mono")}>{g.vmid}</td>
                    <td className={tdClass}>
                      <div className="font-medium">{g.name || "—"}</div>
                      <div className="text-xs text-slate-500">
                        {pveTypeLabel(g.type)}
                        {g.template && " · template"}
                        {g.type === "lxc" && !g.unprivileged && " · privileged"}
                        {g.lock && ` · vergrendeld (${g.lock})`}
                      </div>
                    </td>
                    <td className={tdClass}>
                      <Badge tone={st.tone}>{st.label}</Badge>
                      {g.uptime ? <div className="text-xs text-slate-500">{formatDuration(g.uptime)}</div> : null}
                    </td>
                    <td className={tdClass}>{g.cores ?? "—"}</td>
                    <td className={tdClass}>{g.memory_mib ? `${g.memory_mib} MB` : "—"}</td>
                    <td className={cx(tdClass, "text-xs")}>
                      {(g.disks ?? []).map((d) => (
                        <div key={d.name}>
                          {d.name}: {d.storage} {d.size_gib} GB
                        </div>
                      ))}
                    </td>
                    <td className={cx(tdClass, "max-w-56 text-xs break-all text-slate-500")}>
                      {(g.nets ?? []).map((n) => (
                        <div key={n}>{n}</div>
                      ))}
                    </td>
                    <td className={tdClass}>
                      <Tags tags={g.tags ?? []} />
                    </td>
                    {isAdmin && (
                      <td className={cx(tdClass, "text-right")}>
                        <Menu items={items(g)} disabled={pending} />
                      </td>
                    )}
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
      {open?.dialog === "settings" && <SettingsDialog guest={open.guest} pending={pending} submit={submit} onClose={() => setOpen(null)} />}
      {open?.dialog === "resize" && <ResizeDialog guest={open.guest} pending={pending} submit={submit} onClose={() => setOpen(null)} />}
      {open?.dialog === "snapshots" && <SnapshotsDialog guest={open.guest} pending={pending} submit={submit} onClose={() => setOpen(null)} />}
      {open?.dialog === "migrate" && (
        <MigrateDialog guest={open.guest} targets={targets} inv={inv} pending={pending} submit={submit} onClose={() => setOpen(null)} />
      )}
      {open?.dialog === "bootstrap" && <BootstrapDialog guest={open.guest} pending={pending} submit={submit} onClose={() => setOpen(null)} />}
      {open?.dialog === "destroy" && <DestroyDialog guest={open.guest} pending={pending} submit={submit} onClose={() => setOpen(null)} />}
    </Card>
  );
}

function Menu({ items, disabled }: { items: { label: string; onClick: () => void; danger?: boolean }[]; disabled: boolean }) {
  return (
    <details className="relative inline-block text-left [&[open]>summary]:bg-slate-100 dark:[&[open]>summary]:bg-slate-800">
      <summary className="cursor-pointer list-none rounded-md px-2 py-1 text-sm font-medium text-slate-600 select-none hover:bg-slate-100 dark:text-slate-300 dark:hover:bg-slate-800">
        Acties ▾
      </summary>
      <div className="absolute right-0 z-20 mt-1 w-48 rounded-md border border-slate-200 bg-white py-1 shadow-lg dark:border-slate-700 dark:bg-slate-900">
        {items.map((it) => (
          <button
            key={it.label}
            type="button"
            disabled={disabled}
            onClick={(e) => {
              (e.currentTarget.closest("details") as HTMLDetailsElement | null)?.removeAttribute("open");
              it.onClick();
            }}
            className={cx(
              "block w-full px-3 py-1.5 text-left text-sm hover:bg-slate-50 disabled:opacity-60 dark:hover:bg-slate-800",
              it.danger && "text-red-700 dark:text-red-300",
            )}
          >
            {it.label}
          </button>
        ))}
      </div>
    </details>
  );
}

function Actions({ onClose, pending, label, danger }: { onClose: () => void; pending: boolean; label: string; danger?: boolean }) {
  return (
    <div className="flex justify-end gap-2">
      <Button type="button" variant="secondary" onClick={onClose}>
        Annuleren
      </Button>
      <Button type="submit" variant={danger ? "danger" : "primary"} disabled={pending}>
        {label}
      </Button>
    </div>
  );
}

const title = (g: PveGuest) => `${pveTypeLabel(g.type)} ${g.vmid}${g.name ? ` (${g.name})` : ""}`;
const ref = (g: PveGuest) => ({ type: g.type, vmid: g.vmid });

function SettingsDialog({ guest, pending, submit, onClose }: { guest: PveGuest; pending: boolean; submit: Submit; onClose: () => void }) {
  const ct = guest.type === "lxc";
  const [cores, setCores] = useState(String(guest.cores ?? ""));
  const [mem, setMem] = useState(String(guest.memory_mib ?? ""));
  const [swap, setSwap] = useState(String(guest.swap_mib ?? 0));
  const [onboot, setOnboot] = useState(!!guest.onboot);
  const [tags, setTags] = useState((guest.tags ?? []).join(" "));
  const [desc, setDesc] = useState(guest.description ?? "");
  const [ns, setNs] = useState(guest.nameserver ?? "");
  const [features, setFeatures] = useState(guest.features ?? "");
  return (
    <Modal title={`Instellingen van ${title(guest)}`} onClose={onClose}>
      <form
        className="space-y-4"
        onSubmit={async (e) => {
          e.preventDefault();
          const set: PveRun["set"] = {};
          if (cores && Number(cores) !== guest.cores) set.cores = Number(cores);
          if (mem && Number(mem) !== guest.memory_mib) set.memory_mib = Number(mem);
          if (ct && Number(swap) !== (guest.swap_mib ?? 0)) set.swap_mib = Number(swap);
          if (onboot !== !!guest.onboot) set.onboot = onboot;
          const t = tags.split(/[\s,;]+/).filter(Boolean);
          if (t.join(";") !== (guest.tags ?? []).join(";")) set.tags = t;
          if (desc !== (guest.description ?? "")) set.description = desc;
          if (ct && ns !== (guest.nameserver ?? "")) set.nameserver = ns.trim();
          if (ct && features !== (guest.features ?? "")) set.features = features.trim();
          if (Object.keys(set).length === 0) return onClose();
          if (await submit({ op: { action: "set", ...ref(guest), set } })) onClose();
        }}
      >
        <div className="grid grid-cols-3 gap-3">
          <Field label="CPU-kernen" htmlFor="s-cores">
            <Input id="s-cores" type="number" min={1} max={512} value={cores} onChange={(e) => setCores(e.target.value)} />
          </Field>
          <Field label="RAM (MB)" htmlFor="s-mem">
            <Input id="s-mem" type="number" min={16} step={256} value={mem} onChange={(e) => setMem(e.target.value)} />
          </Field>
          {ct && (
            <Field label="Swap (MB)" htmlFor="s-swap">
              <Input id="s-swap" type="number" min={0} step={256} value={swap} onChange={(e) => setSwap(e.target.value)} />
            </Field>
          )}
        </div>
        <Field label="Tags" htmlFor="s-tags" hint="Gescheiden door spaties.">
          <Input id="s-tags" value={tags} onChange={(e) => setTags(e.target.value)} />
        </Field>
        {ct && (
          <div className="grid grid-cols-2 gap-3">
            <Field label="DNS-servers" htmlFor="s-ns" hint="Gescheiden door spaties; leeg is die van de host.">
              <Input id="s-ns" value={ns} onChange={(e) => setNs(e.target.value)} />
            </Field>
            <Field label="Features" htmlFor="s-feat" hint="Zoals nesting=1,keyctl=1 (nodig voor Docker).">
              <Input id="s-feat" value={features} onChange={(e) => setFeatures(e.target.value)} />
            </Field>
          </div>
        )}
        <Field label="Beschrijving" htmlFor="s-desc">
          <Textarea id="s-desc" rows={3} value={desc} onChange={(e) => setDesc(e.target.value)} />
        </Field>
        <label className="flex items-center gap-2 text-sm">
          <input type="checkbox" checked={onboot} onChange={(e) => setOnboot(e.target.checked)} />
          Starten als de host opstart
        </label>
        <p className="text-xs text-slate-500">CPU en RAM gelden bij een CT meteen; bij een VM soms pas na een herstart.</p>
        <Actions onClose={onClose} pending={pending} label="Opslaan" />
      </form>
    </Modal>
  );
}

function ResizeDialog({ guest, pending, submit, onClose }: { guest: PveGuest; pending: boolean; submit: Submit; onClose: () => void }) {
  const disks = guest.disks ?? [];
  const [disk, setDisk] = useState(disks[0]?.name ?? "");
  const [add, setAdd] = useState("4");
  return (
    <Modal title={`Schijf vergroten van ${title(guest)}`} onClose={onClose}>
      <form
        className="space-y-4"
        onSubmit={async (e) => {
          e.preventDefault();
          if (await submit({ op: { action: "resize", ...ref(guest), disk, size: `+${Number(add)}G` } })) onClose();
        }}
      >
        <Field label="Schijf" htmlFor="r-disk">
          <Select id="r-disk" value={disk} onChange={(e) => setDisk(e.target.value)}>
            {disks.map((d) => (
              <option key={d.name} value={d.name}>
                {d.name} ({d.storage}, {d.size_gib} GB)
              </option>
            ))}
          </Select>
        </Field>
        <Field label="Erbij (GB)" htmlFor="r-add" hint="Verkleinen kan Proxmox niet.">
          <Input id="r-add" type="number" min={1} value={add} onChange={(e) => setAdd(e.target.value)} />
        </Field>
        <Actions onClose={onClose} pending={pending || !disk || Number(add) < 1} label="Vergroten" />
      </form>
    </Modal>
  );
}

function SnapshotsDialog({ guest, pending, submit, onClose }: { guest: PveGuest; pending: boolean; submit: Submit; onClose: () => void }) {
  const [name, setName] = useState(() => `cf-${new Date().toISOString().slice(0, 16).replace(/[-:T]/g, "")}`);
  const [desc, setDesc] = useState("");
  const [vmstate, setVmstate] = useState(false);
  const canState = guest.type === "qemu" && guest.status === "running";
  const snaps = (guest.snapshots ?? []).filter((s) => s.name !== "current");
  return (
    <Modal title={`Snapshots van ${title(guest)}`} onClose={onClose} wide>
      <div className="space-y-5">
        {snaps.length === 0 ? (
          <Empty>Nog geen snapshots.</Empty>
        ) : (
          <table className={tableClass}>
            <thead>
              <tr>
                <th className={thClass}>Naam</th>
                <th className={thClass}>Wanneer</th>
                <th className={thClass}>Beschrijving</th>
                <th className={thClass} />
              </tr>
            </thead>
            <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
              {snaps.map((s) => (
                <tr key={s.name}>
                  <td className={cx(tdClass, "font-mono")}>{s.name}</td>
                  <td className={tdClass}>{s.time ? ago(s.time) : "—"}</td>
                  <td className={cx(tdClass, "text-xs")}>{s.description}</td>
                  <td className={cx(tdClass, "text-right whitespace-nowrap")}>
                    <Button
                      variant="secondary"
                      disabled={pending}
                      onClick={async () => {
                        if (!await askConfirm(`${title(guest)} terugzetten naar ${s.name}? Wat sindsdien veranderde, gaat verloren.`)) return;
                        if (await submit({ op: { action: "rollback", ...ref(guest), snapshot: s.name } })) onClose();
                      }}
                    >
                      Terugzetten
                    </Button>{" "}
                    <Button
                      variant="secondary-danger"
                      disabled={pending}
                      onClick={async () => {
                        if (!await askConfirm(`Snapshot ${s.name} weggooien?`)) return;
                        if (await submit({ op: { action: "delsnapshot", ...ref(guest), snapshot: s.name } })) onClose();
                      }}
                    >
                      Weg
                    </Button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
        <form
          className="space-y-3 border-t border-slate-200 pt-4 dark:border-slate-800"
          onSubmit={async (e) => {
            e.preventDefault();
            if (await submit({ op: { action: "snapshot", ...ref(guest), snapshot: name.trim(), description: desc, vmstate: canState && vmstate } }))
              onClose();
          }}
        >
          <div className="grid grid-cols-2 gap-3">
            <Field label="Nieuwe snapshot" htmlFor="sn-name" hint="Begint met een letter; letters, cijfers, - en _.">
              <Input id="sn-name" value={name} onChange={(e) => setName(e.target.value)} maxLength={40} />
            </Field>
            <Field label="Beschrijving" htmlFor="sn-desc">
              <Input id="sn-desc" value={desc} onChange={(e) => setDesc(e.target.value)} maxLength={500} />
            </Field>
          </div>
          {canState && (
            <label className="flex items-center gap-2 text-sm">
              <input type="checkbox" checked={vmstate} onChange={(e) => setVmstate(e.target.checked)} />
              Geheugen meenemen
            </label>
          )}
          <Actions onClose={onClose} pending={pending || !name.trim()} label="Snapshot maken" />
        </form>
      </div>
    </Modal>
  );
}

function MigrateDialog({
  guest,
  targets,
  inv,
  pending,
  submit,
  onClose,
}: {
  guest: PveGuest;
  targets: string[];
  inv: PveInventory;
  pending: boolean;
  submit: Submit;
  onClose: () => void;
}) {
  const [target, setTarget] = useState(targets[0] ?? "");
  const [storage, setStorage] = useState("");
  const running = guest.status === "running";
  return (
    <Modal title={`${title(guest)} verhuizen`} onClose={onClose}>
      <form
        className="space-y-4"
        onSubmit={async (e) => {
          e.preventDefault();
          if (await submit({ op: { action: "migrate", ...ref(guest), target, target_storage: storage || undefined } })) onClose();
        }}
      >
        <Field label="Naar host" htmlFor="m-target">
          <Select id="m-target" value={target} onChange={(e) => setTarget(e.target.value)}>
            {targets.map((t) => (
              <option key={t}>{t}</option>
            ))}
          </Select>
        </Field>
        <Field label="Doelstorage" htmlFor="m-storage" hint="Leeg houdt dezelfde naam; die moet dan op de doelhost bestaan.">
          <Select id="m-storage" value={storage} onChange={(e) => setStorage(e.target.value)}>
            <option value="">Zelfde storage</option>
            {inv.storages
              .filter((s) => s.content.includes(guest.type === "lxc" ? "rootdir" : "images"))
              .map((s) => (
                <option key={s.name}>{s.name}</option>
              ))}
          </Select>
        </Field>
        {running && guest.type === "lxc" && (
          <p className="text-xs text-slate-500">Een draaiende CT gaat even uit: Proxmox stopt hem, verhuist hem en start hem op de nieuwe host.</p>
        )}
        <Actions onClose={onClose} pending={pending || !target} label="Verhuizen" />
      </form>
    </Modal>
  );
}

function BootstrapDialog({ guest, pending, submit, onClose }: { guest: PveGuest; pending: boolean; submit: Submit; onClose: () => void }) {
  const [server, setServer] = useState(defaultServerURL);
  return (
    <Modal title={`Agent in ${title(guest)} zetten`} onClose={onClose}>
      <form
        className="space-y-4"
        onSubmit={async (e) => {
          e.preventDefault();
          if (await submit({ op: { action: "bootstrap", ...ref(guest) }, server_url: server.trim() })) onClose();
        }}
      >
        <p className="text-sm text-slate-600 dark:text-slate-300">
          De host zet cf-agent in de CT met pct push en meldt hem aan met een eenmalig token. De CT moet draaien en ClusterForge kunnen
          bereiken. Daarna staat hij bij Nodes.
        </p>
        <Field label="Adres van ClusterForge" htmlFor="b-server" hint="Zoals de CT het bereikt, bijvoorbeeld http://192.168.113.153:8080.">
          <Input id="b-server" value={server} onChange={(e) => setServer(e.target.value)} placeholder="http://192.168.113.153:8080" />
        </Field>
        <Actions onClose={onClose} pending={pending || !server.trim()} label="Agent installeren" />
      </form>
    </Modal>
  );
}

function DestroyDialog({ guest, pending, submit, onClose }: { guest: PveGuest; pending: boolean; submit: Submit; onClose: () => void }) {
  const [confirm, setConfirm] = useState("");
  const want = guest.name || String(guest.vmid);
  const ok = confirm.trim() === want || confirm.trim() === String(guest.vmid);
  return (
    <Modal title={`${title(guest)} verwijderen`} onClose={onClose}>
      <form
        className="space-y-4"
        onSubmit={async (e) => {
          e.preventDefault();
          if (await submit({ op: { action: "destroy", ...ref(guest) }, confirm: confirm.trim() })) onClose();
        }}
      >
        <Alert kind="error">
          Dit gooit de {pveTypeLabel(guest.type)} en al zijn schijven weg. Dat kan niet terug. Een node die nog in een cluster van
          ClusterForge zit, weigert ClusterForge.
        </Alert>
        <Field label={`Tik ${want} over`} htmlFor="d-confirm">
          <Input id="d-confirm" value={confirm} onChange={(e) => setConfirm(e.target.value)} autoComplete="off" />
        </Field>
        <Actions onClose={onClose} pending={pending || !ok} label="Definitief verwijderen" danger />
      </form>
    </Modal>
  );
}

function StorageCard({ inv }: { inv: PveInventory }) {
  return (
    <Card title="Storage">
      <table className={tableClass}>
        <thead>
          <tr>
            <th className={thClass}>Naam</th>
            <th className={thClass}>Inhoud</th>
            <th className={thClass}>Vrij</th>
          </tr>
        </thead>
        <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
          {inv.storages.map((s) => (
            <tr key={s.name}>
              <td className={tdClass}>
                <div className="font-medium">{s.name}</div>
                <div className="text-xs text-slate-500">
                  {s.type}
                  {s.shared && " · gedeeld"}
                  {!s.active && " · niet actief"}
                </div>
              </td>
              <td className={cx(tdClass, "text-xs")}>{s.content}</td>
              <td className={tdClass}>
                {s.total > 0 ? (
                  <>
                    {formatBytes(s.avail)} van {formatBytes(s.total)}
                    <div className="text-xs text-slate-500">{percent(s.total - s.avail, s.total)}% gebruikt</div>
                  </>
                ) : (
                  "—"
                )}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </Card>
  );
}

function TemplatesCard({
  host,
  inv,
  isAdmin,
  pending,
  submit,
}: {
  host: PveHost;
  inv: PveInventory;
  isAdmin: boolean;
  pending: boolean;
  submit: Submit;
}) {
  const [browsing, setBrowsing] = useState(false);
  const avail = usePveAvailable(host.node_id, browsing);
  const stores = inv.storages.filter((s) => s.content.split(",").includes("vztmpl"));
  const [storage, setStorage] = useState(stores[0]?.name ?? "");
  const [filter, setFilter] = useState("debian");
  const have = new Set(inv.templates.map((t) => t.volid.split("/").pop()));
  return (
    <Card title="Containertemplates">
      {inv.templates.length === 0 ? (
        <Empty>Nog geen templates op deze host.</Empty>
      ) : (
        <ul className="space-y-1 text-sm">
          {inv.templates.map((t) => (
            <li key={t.volid} className="flex justify-between gap-2">
              <span className="font-mono text-xs break-all">{t.volid}</span>
              <span className="text-xs whitespace-nowrap text-slate-500">{formatBytes(t.size)}</span>
            </li>
          ))}
        </ul>
      )}
      {isAdmin && (
        <div className="mt-4 space-y-3 border-t border-slate-200 pt-3 dark:border-slate-800">
          {!browsing ? (
            <div className="flex flex-wrap gap-2">
              <Button
                variant="secondary"
                disabled={pending || !storage}
                onClick={() => void submit({ op: { action: "template", template: { storage, match: "debian-13-standard_" } } })}
              >
                Nieuwste Debian 13 ophalen
              </Button>
              <Button variant="ghost" onClick={() => setBrowsing(true)}>
                Andere template kiezen…
              </Button>
            </div>
          ) : (
            <>
              <div className="flex flex-wrap gap-2">
                <Select value={storage} onChange={(e) => setStorage(e.target.value)} className="w-40">
                  {stores.map((s) => (
                    <option key={s.name}>{s.name}</option>
                  ))}
                </Select>
                <Input value={filter} onChange={(e) => setFilter(e.target.value)} placeholder="Zoeken" className="w-48" />
              </div>
              <QueryState q={avail}>
                <ul className="max-h-64 space-y-1 overflow-y-auto text-sm">
                  {(avail.data ?? [])
                    .filter((a) => a.section === "system" && a.file.includes(filter.trim()))
                    .map((a) => (
                      <li key={a.file} className="flex items-center justify-between gap-2">
                        <span className="font-mono text-xs break-all">{a.file}</span>
                        {have.has(a.file) ? (
                          <span className="text-xs text-slate-500">Staat er</span>
                        ) : (
                          <Button
                            variant="secondary"
                            disabled={pending || !storage}
                            onClick={() => void submit({ op: { action: "template", template: { storage, file: a.file } } })}
                          >
                            Ophalen
                          </Button>
                        )}
                      </li>
                    ))}
                </ul>
              </QueryState>
            </>
          )}
          {stores.length === 0 && <p className="text-xs text-amber-700">Geen storage met inhoud vztmpl op deze host.</p>}
        </div>
      )}
    </Card>
  );
}

// subnetInfo geeft de gateway (eerste adres van het subnet) en het
// volgnummer van het adres in het subnet.
function subnetInfo(cidr: string): { gw: string; host: number } | null {
  const m = /^(\d+)\.(\d+)\.(\d+)\.(\d+)\/(\d+)$/.exec(cidr.trim());
  if (!m) return null;
  const ip = m.slice(1, 5).map(Number);
  const bits = Number(m[5]);
  if (ip.some((p) => p > 255) || bits < 8 || bits > 30) return null;
  const n = ((ip[0] << 24) | (ip[1] << 16) | (ip[2] << 8) | ip[3]) >>> 0;
  const net = (n & ((~0 << (32 - bits)) >>> 0)) >>> 0;
  const gw = net + 1;
  return { gw: [gw >>> 24, (gw >>> 16) & 255, (gw >>> 8) & 255, gw & 255].join("."), host: n - net };
}

function CreateCTDialog({ inv, pending, submit, onClose }: { inv: PveInventory; pending: boolean; submit: Submit; onClose: () => void }) {
  const used = new Set(inv.used_ids);
  const freeID = useMemo(() => {
    let id = 100;
    while (used.has(id)) id++;
    return id;
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [inv.used_ids]);
  const disks = inv.storages.filter((s) => s.active && s.content.split(",").includes("rootdir"));
  const [vmid, setVmid] = useState(String(freeID));
  const [c, setC] = useState<PveCreate>({
    hostname: "",
    ostemplate: inv.templates.find((t) => t.volid.includes("debian-13"))?.volid ?? inv.templates[0]?.volid ?? "",
    cores: 2,
    memory_mib: 2048,
    swap_mib: 512,
    storage: disks[0]?.name ?? "",
    disk_gib: 16,
    bridge: inv.bridges.includes("vmbr0") ? "vmbr0" : (inv.bridges[0] ?? ""),
    address: "",
    gateway: "",
    host_keys: true,
    nesting: true,
    onboot: true,
    start: true,
  });
  const [vlan, setVlan] = useState("");
  const [dns, setDns] = useState("");
  const [keys, setKeys] = useState("");
  const set = <K extends keyof PveCreate>(k: K, v: PveCreate[K]) => setC((o) => ({ ...o, [k]: v }));

  const parsed = c.address ? subnetInfo(c.address) : null;
  const reserved = parsed !== null && parsed.host >= 1 && parsed.host <= 3;
  const idTaken = used.has(Number(vmid));

  return (
    <Modal title={`CT maken op ${inv.node}`} onClose={onClose} wide>
      <form
        className="space-y-4"
        onSubmit={async (e) => {
          e.preventDefault();
          const create: PveCreate = {
            ...c,
            hostname: c.hostname.trim(),
            vlan: vlan ? Number(vlan) : undefined,
            address: c.address?.trim() || undefined,
            gateway: c.address?.trim() ? c.gateway?.trim() || parsed?.gw : undefined,
            dns: dns.split(/[\s,]+/).filter(Boolean),
            ssh_keys: keys
              .split("\n")
              .map((k) => k.trim())
              .filter(Boolean),
          };
          if (await submit({ op: { action: "create_ct", type: "lxc", vmid: Number(vmid), create } })) onClose();
        }}
      >
        <div className="grid gap-3 sm:grid-cols-3">
          <Field label="CTID" htmlFor="c-id" hint={idTaken ? "Dit nummer is al in gebruik." : undefined}>
            <Input id="c-id" type="number" min={100} value={vmid} onChange={(e) => setVmid(e.target.value)} />
          </Field>
          <Field label="Hostname" htmlFor="c-name">
            <Input id="c-name" value={c.hostname} onChange={(e) => set("hostname", e.target.value)} placeholder="web-01" required />
          </Field>
          <Field label="Template" htmlFor="c-tmpl" hint={inv.templates.length === 0 ? "Haal eerst een template op (hieronder)." : undefined}>
            <Select id="c-tmpl" value={c.ostemplate} onChange={(e) => set("ostemplate", e.target.value)}>
              {inv.templates.map((t) => (
                <option key={t.volid} value={t.volid}>
                  {t.volid.split("/").pop()}
                </option>
              ))}
            </Select>
          </Field>
          <Field label="CPU-kernen" htmlFor="c-cores">
            <Input id="c-cores" type="number" min={1} value={c.cores} onChange={(e) => set("cores", Number(e.target.value))} />
          </Field>
          <Field label="RAM (MB)" htmlFor="c-mem">
            <Input id="c-mem" type="number" min={128} step={256} value={c.memory_mib} onChange={(e) => set("memory_mib", Number(e.target.value))} />
          </Field>
          <Field label="Swap (MB)" htmlFor="c-swap">
            <Input id="c-swap" type="number" min={0} step={256} value={c.swap_mib} onChange={(e) => set("swap_mib", Number(e.target.value))} />
          </Field>
          <Field label="Storage" htmlFor="c-storage">
            <Select id="c-storage" value={c.storage} onChange={(e) => set("storage", e.target.value)}>
              {disks.map((s) => (
                <option key={s.name} value={s.name}>
                  {s.name} ({formatBytes(s.avail)} vrij)
                </option>
              ))}
            </Select>
          </Field>
          <Field label="Schijf (GB)" htmlFor="c-disk">
            <Input id="c-disk" type="number" min={1} value={c.disk_gib} onChange={(e) => set("disk_gib", Number(e.target.value))} />
          </Field>
          <Field label="Bridge" htmlFor="c-bridge">
            <Select id="c-bridge" value={c.bridge} onChange={(e) => set("bridge", e.target.value)}>
              {inv.bridges.map((b) => (
                <option key={b}>{b}</option>
              ))}
            </Select>
          </Field>
          <Field label="VLAN" htmlFor="c-vlan" hint="Leeg zonder tag.">
            <Input id="c-vlan" type="number" min={1} max={4094} value={vlan} onChange={(e) => setVlan(e.target.value)} />
          </Field>
          <Field label="IP-adres" htmlFor="c-ip" hint={reserved ? "De eerste drie adressen zijn voor de gateway en de firewalls." : "Zoals 192.168.10.150/24; leeg is DHCP."}>
            <Input id="c-ip" value={c.address} onChange={(e) => set("address", e.target.value)} placeholder="192.168.10.150/24" />
          </Field>
          <Field label="Gateway" htmlFor="c-gw" hint={parsed ? `Leeg is ${parsed.gw}.` : undefined}>
            <Input id="c-gw" value={c.gateway} onChange={(e) => set("gateway", e.target.value)} placeholder={parsed?.gw ?? ""} />
          </Field>
          <Field label="DNS-servers" htmlFor="c-dns" hint="Leeg is die van de host.">
            <Input id="c-dns" value={dns} onChange={(e) => setDns(e.target.value)} />
          </Field>
        </div>
        <Field label="Extra SSH-sleutels" htmlFor="c-keys" hint="Eén per regel. De sleutels van root op de host gaan er standaard al in.">
          <Textarea id="c-keys" rows={2} value={keys} onChange={(e) => setKeys(e.target.value)} />
        </Field>
        <div className="flex flex-wrap gap-4 text-sm">
          <label className="flex items-center gap-2">
            <input type="checkbox" checked={!!c.host_keys} onChange={(e) => set("host_keys", e.target.checked)} />
            SSH-sleutels van de host erin
          </label>
          <label className="flex items-center gap-2">
            <input type="checkbox" checked={!!c.nesting} onChange={(e) => set("nesting", e.target.checked)} />
            Nesting (nodig voor Docker)
          </label>
          <label className="flex items-center gap-2">
            <input type="checkbox" checked={!!c.privileged} onChange={(e) => set("privileged", e.target.checked)} />
            Privileged
          </label>
          <label className="flex items-center gap-2">
            <input type="checkbox" checked={!!c.onboot} onChange={(e) => set("onboot", e.target.checked)} />
            Starten met de host
          </label>
          <label className="flex items-center gap-2">
            <input type="checkbox" checked={!!c.start} onChange={(e) => set("start", e.target.checked)} />
            Meteen starten
          </label>
        </div>
        <p className="text-xs text-slate-500">
          Wil je er meteen een cluster op? Gebruik dan{" "}
          <Link href="/clusters/uitrollen" className="underline">
            Cluster uitrollen
          </Link>{" "}
          met Nieuwe CT&apos;s: die maakt de CT&apos;s, zet de agent erin en rolt het cluster uit.
        </p>
        <Actions onClose={onClose} pending={pending || reserved || idTaken || !c.hostname.trim() || !c.ostemplate || !c.storage} label="CT maken" />
      </form>
    </Modal>
  );
}
