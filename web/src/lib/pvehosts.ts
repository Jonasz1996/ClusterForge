"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, unwrap } from "./api/client";
import type { Job } from "./proxmox";
import { agentServerURL } from "./agentEndpoints";

// Proxmox-hosts met een agent (fase 17). De types volgen internal/pvehost en
// pkg/protocol/pve.go.

export type PveHost = {
  node_id: string;
  hostname: string;
  pve_node: string;
  cluster?: string;
  version?: string;
  protocol: number;
  agent_version?: string;
  online: boolean;
  heartbeat_at?: string;
  supported: boolean;
  connection_id?: string;
  connection_name?: string;
};

export type PveMissing = { connection_id: string; connection_name: string; pve_node: string; online: boolean };

export type PveDisk = { name: string; storage: string; size_gib: number };
export type PveSnapshot = { name: string; description?: string; time?: string; parent?: string; vmstate?: boolean };

export type PveGuest = {
  type: "lxc" | "qemu";
  vmid: number;
  name: string;
  status: string;
  template?: boolean;
  lock?: string;
  cores?: number;
  memory_mib?: number;
  swap_mib?: number;
  onboot?: boolean;
  tags?: string[];
  description?: string;
  disks?: PveDisk[];
  nets?: string[];
  features?: string;
  unprivileged?: boolean;
  nameserver?: string;
  snapshots?: PveSnapshot[];
  uptime?: number;
};

export type PveStorage = { name: string; type: string; content: string; active: boolean; shared?: boolean; total: number; avail: number };
export type PveOSTemplate = { volid: string; size: number };
export type PveAppliance = { section: string; file: string };

export type PveInventory = {
  node: string;
  version: string;
  cluster?: string;
  arch: string;
  guests: PveGuest[];
  storages: PveStorage[];
  bridges: string[];
  templates: PveOSTemplate[];
  ssh_keys: string[];
  used_ids: number[];
};

export type PveSetting = {
  cores?: number;
  memory_mib?: number;
  swap_mib?: number;
  onboot?: boolean;
  tags?: string[];
  description?: string;
  nameserver?: string;
  features?: string;
};

export type PveCreate = {
  hostname: string;
  ostemplate: string;
  cores: number;
  memory_mib: number;
  swap_mib: number;
  storage: string;
  disk_gib: number;
  bridge: string;
  vlan?: number;
  address?: string;
  gateway?: string;
  dns?: string[];
  ssh_keys?: string[];
  host_keys?: boolean;
  privileged?: boolean;
  nesting?: boolean;
  keyctl?: boolean;
  onboot?: boolean;
  tags?: string[];
  description?: string;
  start?: boolean;
};

export type PveAction =
  | "start"
  | "stop"
  | "shutdown"
  | "reboot"
  | "suspend"
  | "resume"
  | "set"
  | "resize"
  | "snapshot"
  | "rollback"
  | "delsnapshot"
  | "migrate"
  | "destroy"
  | "create_ct"
  | "template"
  | "bootstrap";

export type PveRun = {
  action: PveAction;
  type?: "lxc" | "qemu";
  vmid?: number;
  set?: PveSetting;
  disk?: string;
  size?: string;
  snapshot?: string;
  description?: string;
  vmstate?: boolean;
  target?: string;
  target_storage?: string;
  create?: PveCreate;
  template?: { storage: string; file?: string; match?: string };
};

const keys = {
  hosts: ["pve-hosts"] as const,
  inventory: (id: string) => ["pve-hosts", id, "inventory"] as const,
  available: (id: string) => ["pve-hosts", id, "available"] as const,
};

export function usePveHosts() {
  return useQuery({
    queryKey: keys.hosts,
    queryFn: async () =>
      unwrap(await api.GET("/pve-hosts")) as unknown as { hosts: PveHost[]; missing: PveMissing[]; protocol: number },
    refetchInterval: 30_000,
  });
}

export function usePveInventory(id: string, enabled = true) {
  return useQuery({
    queryKey: keys.inventory(id),
    queryFn: async () =>
      unwrap(await api.GET("/pve-hosts/{nodeId}/inventory", { params: { path: { nodeId: id } } })) as unknown as PveInventory,
    enabled: enabled && !!id,
    refetchInterval: 15_000,
    retry: false,
  });
}

export function usePveAvailable(id: string, enabled: boolean) {
  return useQuery({
    queryKey: keys.available(id),
    queryFn: async () =>
      (unwrap(await api.GET("/pve-hosts/{nodeId}/available", { params: { path: { nodeId: id } } })) as unknown as { items: PveAppliance[] })
        .items,
    enabled: enabled && !!id,
    staleTime: 10 * 60_000,
    retry: false,
  });
}

export function usePveAction(id: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (v: { op: PveRun; confirm?: string; server_url?: string }) =>
      unwrap(
        await api.POST("/pve-hosts/{nodeId}/actions", {
          params: { path: { nodeId: id } },
          body: v as never,
        }),
      ) as unknown as Job,
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["jobs"] });
      setTimeout(() => qc.invalidateQueries({ queryKey: keys.inventory(id) }), 3_000);
    },
  });
}

export const pveTypeLabel = (t: string) => (t === "lxc" ? "CT" : "VM");

// De standaard voor het adres waarmee een CT ClusterForge bereikt: de
// pagina zelf, tenzij die via localhost loopt.
export function defaultServerURL() {
  if (typeof window === "undefined") return "";
  const o = agentServerURL();
  return /\/\/(localhost|127\.)/.test(o) ? "" : o;
}
