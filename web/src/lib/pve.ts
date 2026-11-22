"use client";

import { useMutation, useQueries, useQuery, useQueryClient } from "@tanstack/react-query";
import { useRouter, useSearchParams } from "next/navigation";
import { useEffect } from "react";
import { api, unwrap } from "./api/client";
import type { Job } from "./proxmox";
import type { PveHost } from "./pvehosts";

// Fase 26: Proxmox volledig via de agents op de hosts. Lezen gaat met
// pvesh get langs /pve/datacenters/{dc}/api; veranderen als taak met
// usePveCall. De types volgen internal/pvehost (overview.go) en de
// Proxmox-API zelf (https://pve.proxmox.com/pve-docs/api-viewer/).

export type Datacenter = {
  id: string;
  name: string;
  standalone: boolean;
  hosts: PveHost[];
  via?: string;
  why?: string;
};

export type OverviewNode = {
  name: string;
  online: boolean;
  ip?: string;
  cpu: number;
  cores: number;
  mem: number;
  maxmem: number;
  uptime: number;
  votes: number;
  links?: string[];
  lrm?: string;
  cpu_model?: string;
  kernel?: string;
  pve?: string;
  load?: string[];
  boot?: string;
  ksm?: number;
  running: number;
  guests: number;
  vcpu_running: number;
  vcpu_all: number;
  mem_assigned_running: number;
  mem_assigned_all: number;
  has_agent: boolean;
  agent_node_id?: string;
  reboot_required?: boolean;
  reboot_reasons?: string[];
};

export type OverviewStorage = { name: string; node?: string; shared: boolean; type: string; used: number; total: number; status: string; content?: string };
export type PveWarning = { level: "crit" | "warn"; text: string; area: string };

export type Overview = {
  datacenter: Datacenter;
  name: string;
  quorate: boolean;
  config_version?: number;
  qdevice: boolean;
  nodes: OverviewNode[];
  storages: OverviewStorage[];
  vms: number;
  cts: number;
  running: number;
  templates: number;
  ha: { enabled: boolean; quorum?: string; master?: string; resources: number; problems: string[] };
  capacity: {
    mem_total: number;
    mem_used: number;
    mem_assigned_running: number;
    mem_assigned_all: number;
    cores: number;
    vcpu_running: number;
    vcpu_all: number;
    failover: boolean;
    failover_host?: string;
    failover_short?: number;
  };
  warnings: PveWarning[];
  errors: string[];
};

// Een taak van Proxmox (/cluster/tasks, /nodes/{n}/tasks).
export type PveTask = {
  upid: string;
  node: string;
  type: string;
  id?: string;
  user: string;
  starttime: number;
  endtime?: number;
  status?: string;
  pid?: number;
};

export const API_PROTOCOL = 35;

const keys = {
  dcs: ["pve", "datacenters"] as const,
  overview: (dc: string) => ["pve", dc, "overview"] as const,
  get: (dc: string, path: string, params?: Record<string, string>) => ["pve", dc, "get", path, params ?? {}] as const,
  hardware: (id: string) => ["pve", "hardware", id] as const,
};

export function useDatacenters() {
  return useQuery({
    queryKey: keys.dcs,
    queryFn: async () => unwrap(await api.GET("/pve/datacenters")) as unknown as { items: Datacenter[]; protocol: number },
    refetchInterval: 30_000,
  });
}

// useDatacenterId is het gekozen datacenter: ?dc= in de URL, anders het
// vorige, anders het eerste dat ClusterForge kan lezen.
export function useDatacenterId(list: Datacenter[] | undefined): [string, (id: string) => void] {
  const params = useSearchParams();
  const router = useRouter();
  let stored = "";
  try {
    stored = typeof window !== "undefined" ? (window.localStorage.getItem("cf.pve.dc") ?? "") : "";
  } catch {
    stored = "";
  }
  const fromURL = params.get("dc") ?? "";
  const ids = (list ?? []).map((d) => d.id);
  const pick =
    (fromURL && ids.includes(fromURL) && fromURL) ||
    (stored && ids.includes(stored) && stored) ||
    (list ?? []).find((d) => d.via)?.id ||
    ids[0] ||
    "";
  useEffect(() => {
    try {
      if (pick) window.localStorage.setItem("cf.pve.dc", pick);
    } catch {
      /* geen opslag: niet erg */
    }
  }, [pick]);
  const set = (id: string) => {
    const q = new URLSearchParams(params.toString());
    q.set("dc", id);
    router.replace("?" + q.toString());
  };
  return [pick, set];
}

export function usePveOverview(dc: string, enabled = true) {
  return useQuery({
    queryKey: keys.overview(dc),
    queryFn: async () =>
      unwrap(await api.GET("/pve/datacenters/{dcId}/overview", { params: { path: { dcId: dc } } })) as unknown as Overview,
    enabled: enabled && !!dc,
    refetchInterval: 15_000,
    retry: false,
  });
}

// pveGet leest een pad van de Proxmox-API.
export async function pveGet<T>(dc: string, path: string, params?: Record<string, string>): Promise<T> {
  const res = unwrap(
    await api.GET("/pve/datacenters/{dcId}/api", {
      params: { path: { dcId: dc }, query: { path, params: params ? JSON.stringify(params) : undefined } },
    }),
  ) as unknown as { data: T };
  return res.data;
}

export function usePveGet<T>(
  dc: string,
  path: string,
  opts: { params?: Record<string, string>; enabled?: boolean; refetch?: number | false } = {},
) {
  return useQuery({
    queryKey: keys.get(dc, path, opts.params),
    queryFn: () => pveGet<T>(dc, path, opts.params),
    enabled: (opts.enabled ?? true) && !!dc && !!path,
    refetchInterval: opts.refetch ?? 15_000,
    retry: false,
  });
}

export type PveCall = {
  method: "create" | "set" | "delete";
  path: string;
  params?: Record<string, string>;
  confirm?: string;
};

// usePveCall verandert iets als taak; daarna leest de pagina opnieuw.
export function usePveCall(dc: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (c: PveCall) =>
      unwrap(
        await api.POST("/pve/datacenters/{dcId}/api", {
          params: { path: { dcId: dc } },
          body: c,
        }),
      ) as unknown as Job,
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["jobs"] });
      const again = () => qc.invalidateQueries({ queryKey: ["pve", dc] });
      again();
      setTimeout(again, 3_000);
      setTimeout(again, 10_000);
    },
  });
}

// pveHostAction start een actie op één host via zijn agent (onderhoud,
// passthrough, scrub, volume weg).
export async function pveHostAction(nodeId: string, body: { op: Record<string, unknown>; confirm?: string }): Promise<Job> {
  return unwrap(
    await api.POST("/pve-hosts/{nodeId}/actions", {
      params: { path: { nodeId } },
      body: body as never,
    }),
  ) as unknown as Job;
}

// Hardware van een host (fase 26).
export type PveHardware = {
  cpu_vendor: string;
  cpu_model?: string;
  cmdline: string;
  iommu: boolean;
  iommu_groups: number;
  modules: string[];
  boot: string;
  configured: { cmdline: string; vfio_ids: string[]; blacklist: string[]; managed: boolean };
  reboot_required: boolean;
  reboot_reasons?: string[];
  pci: {
    address: string;
    vendor: string;
    device: string;
    subsystem_vendor?: string;
    subsystem_device?: string;
    class: string;
    vendor_name?: string;
    device_name?: string;
    class_name?: string;
    driver?: string;
    iommu_group: number;
    group_size: number;
    mdev?: boolean;
    host_use?: string;
  }[];
  usb: { bus: number; port: string; vendor: string; product: string; manufacturer?: string; name?: string; class?: string; speed?: string }[];
  temps: { chip: string; label: string; c: number; high?: number; crit?: number }[];
  edac: { controller: string; corrected: number; uncorrected: number }[];
};

export function usePveHardware(nodeId: string, enabled = true) {
  return useQuery({
    queryKey: keys.hardware(nodeId),
    queryFn: async () =>
      unwrap(await api.GET("/pve-hosts/{nodeId}/hardware", { params: { path: { nodeId } } })) as unknown as PveHardware,
    enabled: enabled && !!nodeId,
    refetchInterval: 30_000,
    retry: false,
  });
}

// --- opmaak ---

export const pct = (a: number, b: number) => (b > 0 ? Math.round((a / b) * 100) : 0);

export function taskTypeLabel(t: string): string {
  return taskTypes[t] ?? t;
}

const taskTypes: Record<string, string> = {
  vzdump: "Back-up",
  qmstart: "VM starten",
  qmstop: "VM uitzetten",
  qmshutdown: "VM afsluiten",
  qmreboot: "VM herstarten",
  qmsuspend: "VM pauzeren",
  qmresume: "VM hervatten",
  qmigrate: "VM verhuizen",
  qmclone: "VM klonen",
  qmcreate: "VM maken",
  qmdestroy: "VM verwijderen",
  qmrestore: "VM terugzetten",
  qmsnapshot: "VM-snapshot",
  qmrollback: "VM terugzetten naar snapshot",
  qmdelsnapshot: "VM-snapshot verwijderen",
  qmmove: "VM-schijf verplaatsen",
  qmtemplate: "VM naar template",
  vzstart: "CT starten",
  vzstop: "CT uitzetten",
  vzshutdown: "CT afsluiten",
  vzreboot: "CT herstarten",
  vzmigrate: "CT verhuizen",
  vzcreate: "CT maken",
  vzdestroy: "CT verwijderen",
  vzrestore: "CT terugzetten",
  vzsnapshot: "CT-snapshot",
  vzrollback: "CT terugzetten naar snapshot",
  vzdelsnapshot: "CT-snapshot verwijderen",
  vzclone: "CT klonen",
  vztemplate: "CT naar template",
  move_volume: "CT-volume verplaatsen",
  "ha-manager": "HA",
  hamigrate: "HA verhuizen",
  hastart: "HA starten",
  hastop: "HA stoppen",
  imgdel: "Volume verwijderen",
  download: "Downloaden",
  aptupdate: "Pakketlijst bijwerken",
  startall: "Alles starten",
  stopall: "Alles stoppen",
  migrateall: "Alles verhuizen",
  srvreload: "Dienst herladen",
  srvrestart: "Dienst herstarten",
  zfscreate: "ZFS-pool maken",
  dircreate: "Map maken",
  lvmcreate: "LVM maken",
  lvmthincreate: "LVM-thin maken",
  wipedisk: "Schijf wissen",
  acmenewcert: "Certificaat bestellen",
  acmerenew: "Certificaat vernieuwen",
  vncproxy: "Console",
  vncshell: "Shell",
  termproxy: "Terminal",
  spiceproxy: "SPICE-console",
  reboot: "Host herstarten",
  shutdown: "Host uitzetten",
  pull_file: "Bestand ophalen",
  reloadnetworkall: "Netwerk toepassen",
};

export function taskFailed(t: PveTask) {
  return !!t.status && t.status !== "OK" && !t.status.startsWith("WARNINGS") && t.endtime !== undefined;
}

// --- Gasten (fase 26 M2) ---

// Een regel van /cluster/resources.
export type PveResource = {
  id: string;
  type: "qemu" | "lxc" | "node" | "storage" | "pool" | "sdn";
  node: string;
  status: string;
  name?: string;
  vmid?: number;
  cpu?: number;
  maxcpu?: number;
  mem?: number;
  maxmem?: number;
  disk?: number;
  maxdisk?: number;
  uptime?: number;
  template?: number;
  hastate?: string;
  tags?: string;
  lock?: string;
  pool?: string;
  storage?: string;
  plugintype?: string;
  content?: string;
  shared?: number;
};

export type GuestType = "qemu" | "lxc";

export const guestPath = (node: string, type: GuestType, vmid: number | string) => `/nodes/${node}/${type}/${vmid}`;
export const guestLabel = (type: string) => (type === "lxc" ? "CT" : "VM");
export const guestHref = (dc: string, r: { node: string; type: string; vmid?: number }) =>
  `/proxmox/gast?dc=${encodeURIComponent(dc)}&node=${encodeURIComponent(r.node)}&type=${r.type}&vmid=${r.vmid}`;

// Proxmox scheidt tags met ; (ook , en spatie komen voor).
export const splitTags = (s?: string) => (s ?? "").split(/[;, ]+/).filter(Boolean);

export type GuestConfig = Record<string, string | number | undefined>;

export type PveSnapshotEntry = { name: string; description?: string; snaptime?: number; parent?: string; vmstate?: number; running?: number };

// parseProps leest "virtio=BC:24:..,bridge=vmbr0,tag=20" als sleutels.
export function parseProps(v: string | number | undefined): Record<string, string> {
  const out: Record<string, string> = {};
  for (const part of String(v ?? "").split(",")) {
    const i = part.indexOf("=");
    if (i < 0) {
      if (part) out[""] = part;
      continue;
    }
    out[part.slice(0, i)] = part.slice(i + 1);
  }
  return out;
}

export const diskKeys = /^(scsi|virtio|sata|ide|efidisk|tpmstate|rootfs|mp|unused)\d*$/;

// Kleuren van tags uit de datacenter-opties (tag-style color-map).
export function tagColors(tagStyle?: string): Record<string, string> {
  const out: Record<string, string> = {};
  const m = /color-map=([^,]*)/.exec(tagStyle ?? "");
  if (!m) return out;
  for (const item of m[1].split(";")) {
    const [tag, bg] = item.split(":");
    if (tag && bg && /^[0-9a-fA-F]{6}$/.test(bg)) out[tag] = "#" + bg;
  }
  return out;
}

// --- Back-ups (fase 26 M3) ---

// Een back-up op een storage (/nodes/<n>/storage/<s>/content?content=backup).
export type PveBackup = {
  volid: string;
  ctime: number;
  size: number;
  vmid?: number;
  format?: string;
  subtype?: string;
  notes?: string;
  protected?: number | boolean;
  encrypted?: string;
  verification?: { state?: string; upid?: string };
  // Waar ClusterForge hem las.
  node: string;
  storage: string;
};

// Een back-upjob (/cluster/backup).
export type PveBackupJob = {
  id: string;
  schedule?: string;
  storage?: string;
  all?: number;
  vmid?: string;
  pool?: string;
  exclude?: string;
  node?: string;
  mode?: string;
  compress?: string;
  enabled?: number;
  comment?: string;
  "notes-template"?: string;
  "prune-backups"?: string | Record<string, string | number>;
  "next-run"?: number;
};

export const backupKind = (b: Pick<PveBackup, "volid" | "subtype">) =>
  b.subtype === "lxc" || /\/ct\/|vzdump-lxc-/.test(b.volid) ? ("lxc" as const) : ("qemu" as const);

// Back-upopslag: elke storage met content backup, een gedeelde maar één keer.
export function backupStores(res: PveResource[] | undefined): { node: string; storage: string; shared: boolean; type?: string }[] {
  const out: { node: string; storage: string; shared: boolean; type?: string }[] = [];
  const seen = new Set<string>();
  for (const r of res ?? []) {
    if (r.type !== "storage" || r.status !== "available" || !(r.content ?? "").split(",").includes("backup")) continue;
    const key = r.shared ? r.storage! : `${r.node}/${r.storage}`;
    if (seen.has(key)) continue;
    seen.add(key);
    out.push({ node: r.node, storage: r.storage!, shared: !!r.shared, type: r.plugintype });
  }
  return out.sort((a, b) => a.storage.localeCompare(b.storage) || a.node.localeCompare(b.node));
}

// useBackups leest de back-ups van alle back-upopslag, eventueel van één gast.
export function useBackups(dc: string, stores: { node: string; storage: string }[], vmid?: number) {
  const qs = useQueries({
    queries: stores.map((s) => {
      const path = `/nodes/${s.node}/storage/${s.storage}/content`;
      const params: Record<string, string> = { content: "backup", ...(vmid ? { vmid: String(vmid) } : {}) };
      return {
        queryKey: keys.get(dc, path, params),
        queryFn: async () => (await pveGet<Omit<PveBackup, "node" | "storage">[]>(dc, path, params)).map((b) => ({ ...b, node: s.node, storage: s.storage })),
        refetchInterval: 30_000,
        retry: false,
      };
    }),
  });
  const items = qs.flatMap((q) => q.data ?? []).sort((a, b) => b.ctime - a.ctime);
  const errors = qs.map((q, i) => (q.error ? `${stores[i].storage}: ${q.error instanceof Error ? q.error.message : "fout"}` : "")).filter(Boolean);
  return { items, loading: qs.some((q) => q.isLoading), errors };
}

export type RestoreTestInput = { node: string; archive: string; type: GuestType; storage: string };

export function useRestoreTest(dc: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (body: RestoreTestInput) =>
      unwrap(await api.POST("/pve/datacenters/{dcId}/restore-test", { params: { path: { dcId: dc } }, body })) as unknown as Job,
    onSuccess: () => void qc.invalidateQueries({ queryKey: ["jobs"] }),
  });
}

// --- HA, replicatie en host-updates (fase 26 M4) ---

export type HaStatusEntry = {
  id: string;
  type: "quorum" | "master" | "lrm" | "service" | string;
  status?: string;
  node?: string;
  sid?: string;
  state?: string;
  crm_state?: string;
  request_state?: string;
  quorate?: number;
  timestamp?: number;
};

export type HaResource = { sid: string; state?: string; group?: string; max_restart?: number; max_relocate?: number; comment?: string; failback?: number; type?: string };
export type HaRule = { rule: string; type: string; resources: string; nodes?: string; strict?: number; affinity?: string; disable?: number; comment?: string };
export type HaGroup = { group: string; nodes: string; restricted?: number; nofailback?: number; comment?: string };

export type ReplicationJob = { id: string; guest: number; target: string; schedule?: string; rate?: number; comment?: string; disable?: number; source?: string; type?: string };
export type ReplicationState = ReplicationJob & { last_sync?: number; next_sync?: number; duration?: number; fail_count?: number; error?: string; state?: string };

export type HostUpdateInput = { hosts: string[]; reboot: "needed" | "always" | "never"; shutdown_stuck: boolean; dry_run?: boolean; confirm?: string };

export type PlanGuest = { type: string; vmid: number; name: string; target?: string };
export type HostUpdatePlan = { plan: { hosts: { host: string; moves: PlanGuest[] | null; stops: PlanGuest[] | null; ha: string[] | null }[] }; lines: string[] };

export async function hostUpdatePlan(dc: string, body: HostUpdateInput): Promise<HostUpdatePlan> {
  return unwrap(await api.POST("/pve/datacenters/{dcId}/host-update", { params: { path: { dcId: dc } }, body: { ...body, dry_run: true } })) as unknown as HostUpdatePlan;
}

export type AptPackage = { Package: string; Version: string; OldVersion?: string; Priority?: string; Section?: string; Title?: string; Origin?: string };

// usePerNode leest hetzelfde pad op elke host (bijvoorbeeld apt/update of
// replication); een host die niet antwoordt geeft een fout per host.
export function usePerNode<T>(dc: string, nodes: string[], sub: string, refetch: number | false = 60_000) {
  const qs = useQueries({
    queries: nodes.map((n) => {
      const path = `/nodes/${n}/${sub}`;
      return { queryKey: keys.get(dc, path, undefined), queryFn: () => pveGet<T>(dc, path), refetchInterval: refetch, retry: false };
    }),
  });
  return nodes.map((n, i) => ({ node: n, data: qs[i]?.data, error: qs[i]?.error instanceof Error ? qs[i].error.message : qs[i]?.error ? "fout" : "", loading: !!qs[i]?.isLoading }));
}

export async function startHostUpdate(dc: string, body: HostUpdateInput): Promise<Job> {
  return unwrap(await api.POST("/pve/datacenters/{dcId}/host-update", { params: { path: { dcId: dc } }, body: { ...body, dry_run: false } })) as unknown as Job;
}

// --- Opslag, schijven, ISO's en opruimen (fase 26 M5) ---

export type StorageCfg = {
  storage: string;
  type: string;
  content?: string;
  path?: string;
  server?: string;
  export?: string;
  share?: string;
  pool?: string;
  vgname?: string;
  thinpool?: string;
  datastore?: string;
  username?: string;
  fingerprint?: string;
  namespace?: string;
  nodes?: string;
  shared?: number;
  disable?: number;
  sparse?: number;
};

export type DiskInfo = {
  devpath: string;
  type?: string;
  size: number;
  model?: string;
  serial?: string;
  vendor?: string;
  health?: string;
  wearout?: number | string;
  used?: string;
  gpt?: number;
  rpm?: number;
};

export type SmartData = {
  health?: string;
  type?: "ata" | "text" | string;
  text?: string;
  attributes?: { id?: string | number; name: string; value?: string | number; worst?: string | number; threshold?: string | number; raw?: string; fail?: string; flags?: string }[];
};

export type ZfsPool = { name: string; size: number; alloc: number; free: number; frag?: number; dedup?: number; health: string };
export type ZfsVdev = { name: string; state?: string; read?: number; write?: number; cksum?: number; msg?: string; leaf?: number; children?: ZfsVdev[] };
export type ZfsDetail = { name: string; state: string; status?: string; action?: string; scan?: string; errors?: string; children?: ZfsVdev[] };
export type LvmThin = { lv: string; vg: string; lv_size: number; used: number; metadata_size?: number; metadata_used?: number };

export type StorageItem = { volid: string; format?: string; size: number; content: string; ctime?: number; vmid?: number; notes?: string; used?: number };
export type AplTemplate = { template: string; package?: string; version?: string; os?: string; section?: string; headline?: string; description?: string; type?: string };

// diskHealthy zegt of een SMART-uitslag in orde is ("PASSED" bij ATA, "OK"
// bij SAS/NVMe); onbekend telt niet als fout.
export const diskHealthy = (h?: string) => !h || /^(PASSED|OK|UNKNOWN)$/i.test(h);

// usePerStorage leest dezelfde inhoud van meerdere opslagen (gedeelde één
// keer, lokale per host).
export function usePerStorage<T, S extends { node: string; storage: string } = { node: string; storage: string }>(dc: string, stores: S[], params: Record<string, string>, enabled = true) {
  const qs = useQueries({
    queries: stores.map((s) => {
      const path = `/nodes/${s.node}/storage/${s.storage}/content`;
      return { queryKey: keys.get(dc, path, params), queryFn: () => pveGet<T[]>(dc, path, params), refetchInterval: 60_000, retry: false, enabled };
    }),
  });
  return stores.map((s, i) => ({ ...s, data: qs[i]?.data, error: qs[i]?.error instanceof Error ? qs[i].error.message : qs[i]?.error ? "fout" : "", loading: !!qs[i]?.isLoading }));
}

// storesWith zoekt de beschikbare opslag met een bepaald soort inhoud.
export function storesWith(res: PveResource[] | undefined, content: string[]): { node: string; storage: string; shared: boolean }[] {
  const out: { node: string; storage: string; shared: boolean }[] = [];
  const seen = new Set<string>();
  for (const r of res ?? []) {
    if (r.type !== "storage" || r.status !== "available") continue;
    const have = (r.content ?? "").split(",");
    if (!content.some((c) => have.includes(c))) continue;
    const key = r.shared ? r.storage! : `${r.node}/${r.storage}`;
    if (seen.has(key)) continue;
    seen.add(key);
    out.push({ node: r.node, storage: r.storage!, shared: !!r.shared });
  }
  return out.sort((a, b) => a.storage.localeCompare(b.storage) || a.node.localeCompare(b.node));
}

// useGuestConfigs leest de configuratie van veel gasten tegelijk (voor
// opruimen: ongebruikte schijven en ISO's in cd-stations).
export function useGuestConfigs(dc: string, guests: PveResource[], enabled = true) {
  const qs = useQueries({
    queries: guests.map((g) => {
      const path = `/nodes/${g.node}/${g.type}/${g.vmid}/config`;
      return { queryKey: keys.get(dc, path, undefined), queryFn: () => pveGet<Record<string, unknown>>(dc, path), refetchInterval: 120_000, retry: false, enabled };
    }),
  });
  return { configs: guests.map((g, i) => ({ guest: g, config: qs[i]?.data })), loading: qs.some((q) => q.isLoading) };
}

// --- Netwerk, SDN, firewall, certificaten, toegang (fase 26 M6) ---

export type NetIface = {
  iface: string;
  type: string;
  method?: string;
  method6?: string;
  cidr?: string;
  address?: string;
  gateway?: string;
  cidr6?: string;
  gateway6?: string;
  bridge_ports?: string;
  bridge_vlan_aware?: number;
  slaves?: string;
  bond_mode?: string;
  "vlan-raw-device"?: string;
  "vlan-id"?: number;
  mtu?: number;
  autostart?: number;
  active?: number;
  exists?: number;
  comments?: string;
};

export type SdnZone = { zone: string; type: string; bridge?: string; tag?: number; mtu?: number; nodes?: string; ipam?: string; pending?: unknown; state?: string };
export type SdnVnet = { vnet: string; zone: string; tag?: number; alias?: string; vlanaware?: number; pending?: unknown; state?: string };
export type SdnSubnet = { subnet: string; cidr?: string; gateway?: string; snat?: number; vnet?: string; state?: string };

// isGatewayAddress zegt of een IPv4-adres op .1-.3 van een /24 valt: daar
// staan bij Jonas de CARP-VIP en de twee firewalls.
export function isGatewayAddress(cidr?: string) {
  const m = /^(\d+)\.(\d+)\.(\d+)\.(\d+)\/(\d+)$/.exec(cidr ?? "");
  return !!m && Number(m[5]) >= 24 && ["1", "2", "3"].includes(m[4]);
}

export type FwRule = {
  pos: number;
  type: "in" | "out" | "group" | string;
  action: string;
  enable?: number;
  macro?: string;
  source?: string;
  dest?: string;
  proto?: string;
  dport?: string;
  sport?: string;
  iface?: string;
  log?: string;
  comment?: string;
};
export type FwOptions = { enable?: number; policy_in?: string; policy_out?: string; policy_forward?: string; log_level_in?: string; ebtables?: number; dhcp?: number; macfilter?: number; ipfilter?: number; nosmurfs?: number; tcpflags?: number; ndp?: number; radv?: number };
export type FwAlias = { name: string; cidr: string; comment?: string };
export type FwIpset = { name: string; comment?: string };
export type FwIpsetEntry = { cidr: string; comment?: string; nomatch?: number };
export type FwGroup = { group: string; comment?: string };

export type CertInfo = { filename: string; subject?: string; issuer?: string; notbefore?: number; notafter?: number; san?: string[]; fingerprint?: string };
export type NodeConfig = { acme?: string; [k: `acmedomain${number}`]: string | undefined; description?: string; wakeonlan?: string };
export type AcmeAccount = { name: string };
export type AcmePlugin = { plugin: string; type: string; api?: string; data?: string; disable?: number };
export type AcmeDirectory = { name: string; url: string };
export type AcmeChallenge = { id: string; name?: string; type: string };

export type PveUser = {
  userid: string;
  enable?: number;
  expire?: number;
  firstname?: string;
  lastname?: string;
  email?: string;
  comment?: string;
  groups?: string | string[];
  tokens?: { tokenid: string; expire?: number; privsep?: number; comment?: string }[];
  "realm-type"?: string;
};
export type PveGroup = { groupid: string; comment?: string; users?: string };
export type PveAcl = { path: string; type: "user" | "group" | "token"; ugid: string; roleid: string; propagate?: number };
export type PveRole = { roleid: string; special?: number; privs?: string };
export type PveRealm = { realm: string; type: string; comment?: string };

// propKV leest "a=1,b=2" uit Proxmox in een object.
export function propKV(s?: string): Record<string, string> {
  const out: Record<string, string> = {};
  for (const part of (s ?? "").split(",")) {
    const i = part.indexOf("=");
    if (i > 0) out[part.slice(0, i)] = part.slice(i + 1);
    else if (part) out[""] = part;
  }
  return out;
}

export type PveTokenCreated = { full_tokenid: string; value: string };

export async function createPveToken(dc: string, body: { userid: string; tokenid: string; comment?: string; expire?: number; privsep?: boolean }): Promise<PveTokenCreated> {
  return unwrap(await api.POST("/pve/datacenters/{dcId}/tokens", { params: { path: { dcId: dc } }, body })) as PveTokenCreated;
}

// Console (M7): openen geeft een sessie; daarna opent de browser binnen 30
// seconden de websocket. Het VNC-wachtwoord is eenmalig.
export type ConsoleKind = "vnc" | "serial" | "ct" | "host";
export type PveConsoleOpened = { session: string; kind: ConsoleKind; title: string; password?: string };

export async function openPveConsole(dc: string, body: { node: string; kind: ConsoleKind; vmid?: number; cols?: number; rows?: number }): Promise<PveConsoleOpened> {
  return unwrap(await api.POST("/pve/datacenters/{dcId}/console", { params: { path: { dcId: dc } }, body })) as PveConsoleOpened;
}

export function consoleSocketURL(session: string): string {
  const proto = window.location.protocol === "https:" ? "wss:" : "ws:";
  return `${proto}//${window.location.host}/api/v1/pve/console/${encodeURIComponent(session)}`;
}

/** De link naar de consolepagina; die opent in een eigen tabblad. */
export function consoleHref(dc: string, c: { node: string; kind: ConsoleKind; vmid?: number }): string {
  const q = new URLSearchParams({ dc, node: c.node, kind: c.kind });
  if (c.vmid) q.set("vmid", String(c.vmid));
  return `/proxmox/console?${q}`;
}
