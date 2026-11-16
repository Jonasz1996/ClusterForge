"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
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
