"use client";

import { useState } from "react";
import { Empty, formatBytes, QueryState, tableClass, tdClass, thClass } from "@/components/inventory/bits";
import { ActionMenu, Meter, TypedConfirm, useCallRunner } from "@/components/pve/bits";
import { Check, FormModal } from "@/components/pve/guest/common";
import { Badge, Button, Card, Field, Input, Select } from "@/components/ui";
import { useIsAdmin } from "@/lib/inventory";
import { usePveGet, type Datacenter, type PveResource, type StorageCfg } from "@/lib/pve";

// Opslag van het datacenter (fase 26 M5): wat Proxmox in /etc/pve/storage.cfg
// heeft, met het gebruik per host, en toevoegen/wijzigen/weghalen.

const typeWord: Record<string, string> = {
  dir: "Map",
  nfs: "NFS",
  cifs: "SMB/CIFS",
  pbs: "Proxmox Backup Server",
  zfspool: "ZFS",
  lvmthin: "LVM-thin",
  lvm: "LVM",
  rbd: "Ceph RBD",
  cephfs: "CephFS",
  iscsi: "iSCSI",
  esxi: "ESXi-import",
};

const contentWord: Record<string, string> = {
  images: "VM-schijven",
  rootdir: "CT-volumes",
  iso: "ISO's",
  vztmpl: "CT-templates",
  backup: "Back-ups",
  snippets: "Snippets",
  import: "Import",
};

const contentFor: Record<string, string[]> = {
  dir: ["images", "rootdir", "iso", "vztmpl", "backup", "snippets", "import"],
  nfs: ["images", "rootdir", "iso", "vztmpl", "backup", "snippets", "import"],
  cifs: ["images", "rootdir", "iso", "vztmpl", "backup", "snippets", "import"],
  zfspool: ["images", "rootdir"],
  lvmthin: ["images", "rootdir"],
  lvm: ["images", "rootdir"],
  pbs: ["backup"],
};

export function StorageList({ dc, res }: { dc: Datacenter; res: PveResource[] }) {
  const isAdmin = useIsAdmin();
  const cfg = usePveGet<StorageCfg[]>(dc.id, "/storage", { refetch: 60_000 });
  const { run, pending, banner } = useCallRunner(dc.id);
  const [dialog, setDialog] = useState<{ kind: "add" } | { kind: "edit"; s: StorageCfg } | { kind: "remove"; s: StorageCfg } | null>(null);
  const close = () => setDialog(null);
  const nodes = dc.hosts.map((h) => h.pve_node);
  const usage = (name: string) => res.filter((r) => r.type === "storage" && r.storage === name);

  return (
    <Card
      title={
        <span className="flex items-center justify-between gap-2">
          <span>Opslag</span>
          {isAdmin && (
            <Button variant="secondary" disabled={pending} onClick={() => setDialog({ kind: "add" })}>
              Opslag toevoegen
            </Button>
          )}
        </span>
      }
    >
      <div className="space-y-3">
        {banner}
        <QueryState q={cfg}>
          {(cfg.data ?? []).length === 0 ? (
            <Empty>Geen opslag gevonden.</Empty>
          ) : (
            <div className="relative -mx-4 overflow-x-auto sm:mx-0">
              <table className={tableClass}>
                <thead>
                  <tr>
                    <th className={thClass}>Naam</th>
                    <th className={thClass}>Type</th>
                    <th className={thClass}>Inhoud</th>
                    <th className={thClass}>Gebruik per host</th>
                    <th className={thClass}>
                      <span className="sr-only">Acties</span>
                    </th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
                  {[...cfg.data!]
                    .sort((a, b) => a.storage.localeCompare(b.storage))
                    .map((s) => {
                      const u = usage(s.storage);
                      const shown = s.shared || ["nfs", "cifs", "pbs", "rbd", "cephfs"].includes(s.type) ? u.slice(0, 1) : u;
                      return (
                        <tr key={s.storage} className="align-top">
                          <td className={tdClass}>
                            <span className="font-medium">{s.storage}</span>
                            {s.disable ? (
                              <span className="ml-2">
                                <Badge tone="slate">uit</Badge>
                              </span>
                            ) : null}
                            <span className="block text-xs text-slate-500">{where(s)}</span>
                          </td>
                          <td className={tdClass}>
                            {typeWord[s.type] ?? s.type}
                            {s.shared || ["nfs", "cifs", "pbs"].includes(s.type) ? <span className="block text-xs text-slate-500">gedeeld</span> : null}
                          </td>
                          <td className={tdClass + " text-xs"}>
                            {(s.content ?? "")
                              .split(",")
                              .filter(Boolean)
                              .map((c) => contentWord[c] ?? c)
                              .join(", ")}
                            <span className="block text-slate-500">{s.nodes ? `alleen ${s.nodes.split(",").join(", ")}` : "alle hosts"}</span>
                          </td>
                          <td className={tdClass + " min-w-56"}>
                            {shown.length === 0 ? (
                              <span className="text-slate-500">–</span>
                            ) : (
                              <ul className="space-y-1">
                                {shown.map((r) => (
                                  <li key={r.id} className="text-xs">
                                    {shown.length > 1 || !s.shared ? <span className="text-slate-500">{r.node} </span> : null}
                                    {r.status === "available" && r.maxdisk ? (
                                      <>
                                        <Meter value={((r.disk ?? 0) / r.maxdisk) * 100} label={`${s.storage} op ${r.node}`} />
                                        <span className="text-slate-500">
                                          {formatBytes(r.disk ?? 0)} van {formatBytes(r.maxdisk)}
                                        </span>
                                      </>
                                    ) : (
                                      <Badge tone={r.status === "available" ? "green" : "red"}>
                                        {r.status === "available" ? "beschikbaar" : "niet bereikbaar"}
                                      </Badge>
                                    )}
                                  </li>
                                ))}
                              </ul>
                            )}
                          </td>
                          <td className={tdClass + " text-right"}>
                            {isAdmin && (
                              <ActionMenu
                                disabled={pending}
                                items={[
                                  {
                                    label: "Wijzigen",
                                    onClick: () => setDialog({ kind: "edit", s }),
                                  },
                                  {
                                    label: s.disable ? "Aanzetten" : "Uitzetten",
                                    onClick: () =>
                                      void run({
                                        method: "set",
                                        path: `/storage/${s.storage}`,
                                        params: s.disable ? { delete: "disable" } : { disable: "1" },
                                      }),
                                  },
                                  {
                                    label: "Weghalen…",
                                    onClick: () => setDialog({ kind: "remove", s }),
                                    danger: true,
                                  },
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
        </QueryState>
      </div>
      {dialog?.kind === "add" && <StorageDialog nodes={nodes} taken={(cfg.data ?? []).map((s) => s.storage)} run={run} pending={pending} onClose={close} />}
      {dialog?.kind === "edit" && <StorageDialog edit={dialog.s} nodes={nodes} taken={[]} run={run} pending={pending} onClose={close} />}
      {dialog?.kind === "remove" && (
        <TypedConfirm
          title={`Opslag ${dialog.s.storage} weghalen`}
          expect={dialog.s.storage}
          confirmLabel="Weghalen"
          pending={pending}
          onClose={close}
          onConfirm={(typed) =>
            void run({
              method: "delete",
              path: `/storage/${dialog.s.storage}`,
              confirm: typed,
            }).then((ok) => ok && close())
          }
        >
          <p className="text-sm">
            Proxmox vergeet deze opslag. De gegevens zelf (schijven, back-ups) blijven staan, maar gasten die er een schijf op hebben starten niet meer tot je
            de opslag opnieuw toevoegt.
          </p>
        </TypedConfirm>
      )}
    </Card>
  );
}

function where(s: StorageCfg) {
  switch (s.type) {
    case "dir":
      return s.path ?? "";
    case "nfs":
      return `${s.server}:${s.export}`;
    case "cifs":
      return `//${s.server}/${s.share}`;
    case "pbs":
      return `${s.server} / ${s.datastore}${s.namespace ? ` / ${s.namespace}` : ""}`;
    case "zfspool":
      return s.pool ?? "";
    case "lvmthin":
      return `${s.vgname}/${s.thinpool}`;
    case "lvm":
      return s.vgname ?? "";
    default:
      return "";
  }
}

type Runner = ReturnType<typeof useCallRunner>["run"];

function StorageDialog({
  edit,
  nodes,
  taken,
  run,
  pending,
  onClose,
}: {
  edit?: StorageCfg;
  nodes: string[];
  taken: string[];
  run: Runner;
  pending: boolean;
  onClose: () => void;
}) {
  const [type, setType] = useState(edit?.type ?? "nfs");
  const [name, setName] = useState(edit?.storage ?? "");
  const [f, setF] = useState<Record<string, string>>({
    path: edit?.path ?? "",
    server: edit?.server ?? "",
    export: edit?.export ?? "",
    share: edit?.share ?? "",
    username: edit?.username ?? "",
    password: "",
    domain: "",
    datastore: edit?.datastore ?? "",
    namespace: edit?.namespace ?? "",
    fingerprint: edit?.fingerprint ?? "",
    pool: edit?.pool ?? "",
    vgname: edit?.vgname ?? "",
    thinpool: edit?.thinpool ?? "",
  });
  const set = (k: string) => (e: { target: { value: string } }) => setF({ ...f, [k]: e.target.value });
  const allowed = contentFor[type] ?? [];
  const [content, setContent] = useState<string[]>((edit?.content ?? (type === "pbs" ? "backup" : "images,rootdir")).split(",").filter(Boolean));
  const [only, setOnly] = useState<string[]>((edit?.nodes ?? "").split(",").filter(Boolean));
  const [shared, setShared] = useState(!!edit?.shared);
  const supported = !!contentFor[type];

  const need: Record<string, string[]> = {
    dir: ["path"],
    nfs: ["server", "export"],
    cifs: ["server", "share"],
    pbs: ["server", "datastore", "username", ...(edit ? [] : ["password"])],
    zfspool: ["pool"],
    lvmthin: ["vgname", "thinpool"],
    lvm: ["vgname"],
  };
  const chosen = content.filter((c) => allowed.includes(c));
  const error = !supported
    ? `ClusterForge kan ${typeWord[type] ?? type} nog niet wijzigen; doe dat in Proxmox zelf.`
    : !edit && !/^[A-Za-z][A-Za-z0-9_.-]{0,99}$/.test(name)
      ? "De naam begint met een letter en bevat alleen letters, cijfers, - _ en ."
      : !edit && taken.includes(name)
        ? "Die naam bestaat al."
        : (need[type] ?? []).some((k) => !f[k].trim())
          ? "Vul alle verplichte velden in."
          : chosen.length === 0
            ? "Kies minstens één soort inhoud."
            : null;

  const submit = () => {
    const p: Record<string, string> = { content: chosen.join(",") };
    if (only.length > 0 && only.length < nodes.length) p.nodes = only.join(",");
    if (type === "dir" || type === "lvm") p.shared = shared ? "1" : "0";
    if (!edit) {
      p.storage = name;
      p.type = type;
      for (const k of need[type] ?? []) p[k] = f[k].trim();
      if (type === "cifs" && f.username) p.username = f.username;
      if (type === "cifs" && f.password) p.password = f.password;
      if (type === "cifs" && f.domain) p.domain = f.domain;
      if (type === "pbs" && f.fingerprint) p.fingerprint = f.fingerprint.trim();
      if (type === "pbs" && f.namespace) p.namespace = f.namespace.trim();
      if (type === "dir") p.mkdir = "0";
    } else {
      const del: string[] = [];
      if (!p.nodes && edit.nodes) del.push("nodes");
      if (type === "pbs" && f.fingerprint !== (edit.fingerprint ?? "")) {
        if (f.fingerprint) p.fingerprint = f.fingerprint.trim();
        else del.push("fingerprint");
      }
      if (type === "pbs" && f.password) p.password = f.password;
      if (type === "cifs" && f.password) p.password = f.password;
      if (del.length) p.delete = del.join(",");
    }
    const call = edit ? { method: "set" as const, path: `/storage/${edit.storage}`, params: p } : { method: "create" as const, path: "/storage", params: p };
    void run(call).then((ok) => ok && onClose());
  };

  const text = (k: string, label: string, hint?: string, extra?: { type?: string; placeholder?: string }) => (
    <Field label={label} htmlFor={`st-${k}`} hint={hint}>
      <Input id={`st-${k}`} value={f[k]} onChange={set(k)} disabled={!!edit && k !== "password" && k !== "fingerprint"} {...extra} />
    </Field>
  );

  return (
    <FormModal
      title={edit ? `Opslag ${edit.storage} wijzigen` : "Opslag toevoegen"}
      onClose={onClose}
      onSubmit={submit}
      submitLabel="Opslaan"
      disabled={pending}
      error={error}
      wide
    >
      <div className="grid gap-3 sm:grid-cols-2">
        <Field label="Naam" htmlFor="st-name">
          <Input id="st-name" value={name} onChange={(e) => setName(e.target.value)} disabled={!!edit} />
        </Field>
        <Field label="Soort" htmlFor="st-type">
          <Select
            id="st-type"
            value={type}
            disabled={!!edit}
            onChange={(e) => (setType(e.target.value), setContent(e.target.value === "pbs" ? ["backup"] : ["images", "rootdir"]))}
          >
            {Object.keys(contentFor).map((t) => (
              <option key={t} value={t}>
                {typeWord[t]}
              </option>
            ))}
          </Select>
        </Field>
        {type === "dir" && text("path", "Map op de host", "Bestaat al, bijvoorbeeld /mnt/data")}
        {(type === "nfs" || type === "cifs" || type === "pbs") && text("server", "Server", "IP-adres of naam")}
        {type === "nfs" && text("export", "Export", "Bijvoorbeeld /volume1/proxmox")}
        {type === "cifs" && text("share", "Share")}
        {type === "pbs" && text("datastore", "Datastore")}
        {type === "pbs" && text("namespace", "Namespace", "Leeg is de hoofdnamespace")}
        {(type === "cifs" || type === "pbs") &&
          text("username", "Gebruiker", type === "pbs" ? "Bijvoorbeeld backup@pbs of een token user@pbs!naam" : undefined)}
        {(type === "cifs" || type === "pbs") &&
          text("password", type === "pbs" ? "Wachtwoord of token" : "Wachtwoord", edit ? "Leeg laat het huidige staan" : undefined, { type: "password" })}
        {type === "cifs" && !edit && text("domain", "Domein", "Optioneel")}
        {type === "pbs" && text("fingerprint", "Vingerafdruk", "Nodig bij een zelfondertekend certificaat; staat op het PBS-dashboard")}
        {type === "zfspool" && text("pool", "ZFS-pool of dataset", "Bijvoorbeeld rpool/data")}
        {(type === "lvmthin" || type === "lvm") && text("vgname", "Volumegroep")}
        {type === "lvmthin" && text("thinpool", "Thinpool")}
      </div>
      <fieldset className="space-y-2">
        <legend className="text-sm font-medium">Inhoud</legend>
        <div className="grid gap-1 sm:grid-cols-3">
          {allowed.map((c) => (
            <Check key={c} id={`st-c-${c}`} checked={content.includes(c)} onChange={(v) => setContent(v ? [...content, c] : content.filter((x) => x !== c))}>
              {contentWord[c]}
            </Check>
          ))}
        </div>
      </fieldset>
      <fieldset className="space-y-2">
        <legend className="text-sm font-medium">Hosts</legend>
        <p className="text-xs text-slate-500">Niets aangevinkt is alle hosts.</p>
        <div className="grid gap-1 sm:grid-cols-3">
          {nodes.map((n) => (
            <Check key={n} id={`st-n-${n}`} checked={only.includes(n)} onChange={(v) => setOnly(v ? [...only, n] : only.filter((x) => x !== n))}>
              {n}
            </Check>
          ))}
        </div>
      </fieldset>
      {(type === "dir" || type === "lvm") && (
        <Check id="st-shared" checked={shared} onChange={setShared}>
          Gedeeld: elke host ziet dezelfde inhoud (bijvoorbeeld een gemounte netwerkschijf). Laat uit voor een lokale schijf.
        </Check>
      )}
    </FormModal>
  );
}
