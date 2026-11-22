"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, unwrap } from "./api/client";
import type { components } from "./api/schema";

// Firewall (fase 16): ClusterForge beheert ufw op de nodes en telt het
// verkeer naar dichte poorten. De types volgen internal/firewall.

export type FirewallRule = {
  action: string;
  proto: string;
  port?: string;
  from: string;
  to?: string;
  direction?: string;
  iface?: string;
  app?: string;
  comment?: string;
};

export type FirewallListener = { proto: string; addr: string; port: number; process?: string; unit?: string; container?: string; image?: string };

export type FirewallStatus = {
  installed: boolean;
  version?: string;
  active: boolean;
  incoming?: string;
  outgoing?: string;
  routed?: string;
  rules: FirewallRule[];
  listeners: FirewallListener[];
  monitor: string;
  monitor_error?: string;
  monitor_rule: boolean;
  pending?: { apply_id: string; deadline: string } | null;
  applied?: { hash: string; applied_at: string } | null;
  rolled_back?: { apply_id: string; at: string; reason?: string } | null;
  errors?: string[];
};

export type FirewallEntry = {
  proto: string;
  port: string;
  software: string;
  label?: string;
  process?: string;
  class: string;
  origin: string;
  state: "open" | "closed" | "docker" | "ignored";
  from: string[];
  unknown?: boolean;
  note?: string;
};

export type FirewallPlan = { hash: string; strict: boolean; rules: FirewallRule[]; entries: FirewallEntry[]; warnings: string[] };

export type FirewallDiff = { add: FirewallRule[]; adopt: FirewallRule[]; remove: FirewallRule[]; keep: FirewallRule[]; foreign: FirewallRule[] };

export type FirewallNode = {
  node_id: string;
  hostname: string;
  address: string;
  cluster_id: string | null;
  cluster_name: string;
  cluster_slug: string;
  cluster_environment: string;
  cluster_template: string;
  online: boolean;
  protocol: number;
  supported: boolean;
  mode: "off" | "preview" | "managed";
  status: FirewallStatus | null;
  status_at: string | null;
  status_error: string;
  plan: FirewallPlan | null;
  plan_hash: string;
  applied_hash: string;
  applied_at: string | null;
  apply_job_id: string | null;
  apply_running: boolean;
  apply_error: string;
  apply_failed_at: string | null;
  blocked_at: string | null;
  blocked_source: string;
  diff: FirewallDiff | null;
  blocked_24h: number;
};

export type FirewallSettings = {
  auto_apply: boolean;
  lan: string[];
  admin: string[];
  ssh_from: "lan" | "admin" | "any";
  unknown_ports: "lan" | "closed";
  strict: boolean;
  confirm_seconds: number;
  grace_hours: number;
  monitor: boolean;
  updated_at?: string;
};

export type FirewallCell = { state: string; class: string; from: string[]; applied: boolean; blocked_24h: number };
export type FirewallPortRow = { proto: string; port: string; software: string; label: string; cells: Record<string, FirewallCell> };
export type FirewallCluster = {
  id: string | null;
  name: string;
  slug: string;
  nodes: { id: string; hostname: string; mode: string; active: boolean }[];
  ports: FirewallPortRow[];
};

export type FirewallCustomRule = {
  id: string;
  cluster_id: string | null;
  cluster_name: string;
  node_id: string | null;
  hostname: string;
  action: "open" | "close";
  proto: string;
  port: string;
  source: string;
  comment: string;
  created_at: string;
};

export type FirewallSoftware = {
  key: string;
  name: string;
  procs?: string[];
  units?: string[];
  images?: string[];
  ports: { proto: string; port: string; class: string; label?: string }[];
  fixed?: boolean;
  default?: string;
  vrrp?: boolean;
};

export type HourCount = { hour: string; count: number };

export type FirewallOverview = {
  protocol: number;
  settings: FirewallSettings;
  stats: {
    managed: number;
    preview: number;
    active: number;
    pending: number;
    failed: number;
    open_ports: number;
    blocked_24h: number;
    sources_24h: number;
  };
  hourly: HourCount[];
  clusters: FirewallCluster[];
  warnings: { node_id: string; hostname: string; text: string }[];
  nodes: FirewallNode[];
  rules: FirewallCustomRule[];
  catalog: FirewallSoftware[];
  classes: string[];
};

export type TrafficRow = {
  node_id: string;
  hostname: string;
  cluster_id: string | null;
  cluster_name: string;
  src: string;
  proto: string;
  port: number;
  count: number;
  first: string;
  last: string;
  src_node: string;
  same_cluster: boolean;
  software: string;
};

export type TrafficQuery = { node_id?: string; cluster_id?: string; port?: number; hours?: number; limit?: number };

const key = ["firewall"] as const;

export function useFirewall() {
  return useQuery({
    queryKey: key,
    queryFn: async () => unwrap(await api.GET("/firewall")) as unknown as FirewallOverview,
    refetchInterval: (q) => (q.state.data?.nodes.some((n) => n.apply_running) ? 3_000 : 30_000),
  });
}

export function useFirewallNode(id: string) {
  return useQuery({
    queryKey: [...key, "node", id],
    queryFn: async () =>
      unwrap(await api.GET("/firewall/nodes/{nodeId}", { params: { path: { nodeId: id } } })) as unknown as {
        node: FirewallNode;
        hourly: HourCount[];
        traffic: TrafficRow[];
      },
    refetchInterval: (q) => (q.state.data?.node.apply_running ? 3_000 : 30_000),
  });
}

export function useFirewallTraffic(query: TrafficQuery) {
  return useQuery({
    queryKey: [...key, "traffic", query],
    queryFn: async () =>
      unwrap(await api.GET("/firewall/traffic", { params: { query } })) as unknown as { items: TrafficRow[]; hourly: HourCount[] },
    refetchInterval: 60_000,
  });
}

function useInvalidate() {
  const qc = useQueryClient();
  return () => {
    void qc.invalidateQueries({ queryKey: key });
    void qc.invalidateQueries({ queryKey: ["jobs"] });
  };
}

export function useSetFirewallNodes() {
  const done = useInvalidate();
  return useMutation({
    mutationFn: async (body: components["schemas"]["FirewallNodesInput"]) => unwrap(await api.POST("/firewall/nodes", { body })),
    onSuccess: done,
  });
}

export function useRefreshFirewallNode() {
  const done = useInvalidate();
  return useMutation({
    mutationFn: async (id: string) =>
      unwrap(await api.POST("/firewall/nodes/{nodeId}/refresh", { params: { path: { nodeId: id } } })) as unknown as FirewallNode,
    onSuccess: done,
  });
}

export function useApplyFirewallNode() {
  const done = useInvalidate();
  return useMutation({
    mutationFn: async (id: string) => unwrap(await api.POST("/firewall/nodes/{nodeId}/apply", { params: { path: { nodeId: id } } })),
    onSuccess: done,
  });
}

export function useRollbackFirewallNode() {
  const done = useInvalidate();
  return useMutation({
    mutationFn: async (id: string) => unwrap(await api.POST("/firewall/nodes/{nodeId}/rollback", { params: { path: { nodeId: id } } })),
    onSuccess: done,
  });
}

export function useDisableFirewallNode() {
  const done = useInvalidate();
  return useMutation({
    mutationFn: async (id: string) => unwrap(await api.POST("/firewall/nodes/{nodeId}/disable", { params: { path: { nodeId: id } } })),
    onSuccess: done,
  });
}

export function useFirewallSettings() {
  const done = useInvalidate();
  return useMutation({
    mutationFn: async (body: components["schemas"]["FirewallSettingsInput"]) => unwrap(await api.PUT("/firewall/settings", { body })),
    onSuccess: done,
  });
}

export function useAddFirewallRule() {
  const done = useInvalidate();
  return useMutation({
    mutationFn: async (body: components["schemas"]["FirewallRuleInput"]) => unwrap(await api.POST("/firewall/rules", { body })),
    onSuccess: done,
  });
}

export function useDeleteFirewallRule() {
  const done = useInvalidate();
  return useMutation({
    mutationFn: async (id: string) => unwrap(await api.DELETE("/firewall/rules/{ruleId}", { params: { path: { ruleId: id } } })),
    onSuccess: done,
  });
}

export type Tone = "slate" | "green" | "amber" | "red" | "blue";

/** De stand van de firewall op een node in een paar woorden. */
export function firewallState(n: FirewallNode): { label: string; tone: Tone } {
  if (!n.supported) return { label: `agent te oud (protocol ${n.protocol})`, tone: "amber" };
  if (n.mode === "off") return { label: "niet beheerd", tone: "slate" };
  if (n.apply_running) return { label: "wordt toegepast…", tone: "blue" };
  if (!n.online) return { label: "offline", tone: "red" };
  if (n.status?.pending) return { label: "wacht op bevestiging", tone: "blue" };
  if (n.apply_error) return { label: "toepassen mislukt", tone: "red" };
  const st = n.status;
  if (!st) return { label: "stand ophalen…", tone: "slate" };
  if (n.mode === "preview") {
    if (!st.installed) return { label: "voorbeeld · ufw ontbreekt", tone: "slate" };
    return { label: st.active ? "voorbeeld · ufw aan" : "voorbeeld · ufw uit", tone: "slate" };
  }
  if (!st.active) return { label: "ufw staat uit", tone: "red" };
  if (n.plan_hash && n.plan_hash !== n.applied_hash) return { label: "wijziging klaar", tone: "amber" };
  return { label: "beschermd", tone: "green" };
}

export const modeLabel: Record<string, string> = { off: "uit", preview: "voorbeeld", managed: "beheerd" };

export const classLabel: Record<string, string> = {
  any: "iedereen",
  lan: "eigen netwerk",
  cluster: "clusterleden",
  admin: "beheer",
  containers: "containers",
  ssh: "SSH",
  docker: "via Docker",
  closed: "dicht",
};

export const originLabel: Record<string, string> = {
  ssh: "SSH (altijd)",
  listening: "luistert",
  remembered: "onthouden",
  unit: "systemd-unit",
  template: "clustertemplate",
  vrrp: "keepalived",
  custom: "eigen regel",
  docker: "Docker",
};

/** Een regel als korte tekst: 2379/tcp van 192.168.103.243. */
export function ruleText(r: FirewallRule) {
  const port = r.port ? `${r.port}/${r.proto}` : r.proto;
  const from = r.from === "any" ? "iedereen" : r.from;
  return `${r.action === "allow" ? "" : r.action + " "}${port} van ${from}${r.app ? ` (${r.app})` : ""}`;
}
