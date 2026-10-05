"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, unwrap } from "./api/client";
import type { components } from "./api/schema";
import { useMe } from "./auth";

export type Cluster = components["schemas"]["ClusterListItem"];
export type ClusterDetail = components["schemas"]["ClusterDetail"];
export type ClusterInput = components["schemas"]["ClusterInput"];
export type ClusterPatch = components["schemas"]["ClusterPatch"];
export type ClusterType = components["schemas"]["ClusterType"];
export type Environment = components["schemas"]["Environment"];
export type Node = components["schemas"]["Node"];
export type NodeInput = components["schemas"]["NodeInput"];
export type NodePatch = components["schemas"]["NodePatch"];
export type NodeLifecycle = components["schemas"]["NodeLifecycle"];
export type Vip = components["schemas"]["Vip"];
export type VipInput = components["schemas"]["VipInput"];
export type VipPatch = components["schemas"]["VipPatch"];
export type UserRef = components["schemas"]["UserRef"];

export const clusterTypes: { value: ClusterType; label: string }[] = [
  { value: "keepalived", label: "Keepalived" },
  { value: "nginx", label: "Nginx" },
  { value: "docker", label: "Docker" },
  { value: "cron", label: "Cron" },
  { value: "postgresql_ha", label: "PostgreSQL HA" },
  { value: "mariadb_ha", label: "MariaDB HA" },
  { value: "generic", label: "Algemeen" },
];

export const environments: { value: Environment; label: string; tone: "green" | "amber" | "red" }[] = [
  { value: "lab", label: "Lab", tone: "green" },
  { value: "test", label: "Test", tone: "amber" },
  { value: "prod", label: "Productie", tone: "red" },
];

export const lifecycles: { value: NodeLifecycle; label: string; tone: "slate" | "green" | "amber" | "red" | "blue" }[] = [
  { value: "provisioning", label: "Wordt opgezet", tone: "blue" },
  { value: "active", label: "Actief", tone: "green" },
  { value: "maintenance", label: "Onderhoud", tone: "amber" },
  { value: "draining", label: "Draining", tone: "amber" },
  { value: "decommissioned", label: "Uit dienst", tone: "slate" },
];

export const typeLabel = (t: string) => clusterTypes.find((x) => x.value === t)?.label ?? t;
export const envInfo = (e: string) => environments.find((x) => x.value === e) ?? { value: e, label: e, tone: "amber" as const };
export const lifecycleInfo = (l: string) => lifecycles.find((x) => x.value === l) ?? { value: l, label: l, tone: "slate" as const };

// parseTags maakt van "web, dc1 prod" een lijst; de server normaliseert verder.
export function parseTags(s: string): string[] {
  return s
    .split(/[\s,]+/)
    .map((t) => t.trim())
    .filter(Boolean);
}

const keys = {
  clusters: ["clusters"] as const,
  cluster: (id: string) => ["clusters", id] as const,
  nodes: ["nodes"] as const,
  node: (id: string) => ["nodes", id] as const,
  users: ["users"] as const,
};

export function useIsAdmin() {
  return useMe().data?.user.role === "admin";
}

export function useClusters() {
  return useQuery({ queryKey: keys.clusters, queryFn: async () => unwrap(await api.GET("/clusters")).items });
}

export function useCluster(id: string) {
  return useQuery({
    queryKey: keys.cluster(id),
    queryFn: async () => unwrap(await api.GET("/clusters/{clusterId}", { params: { path: { clusterId: id } } })),
    enabled: id !== "",
  });
}

export function useNodes() {
  return useQuery({ queryKey: keys.nodes, queryFn: async () => unwrap(await api.GET("/nodes")).items });
}

export function useNode(id: string) {
  return useQuery({
    queryKey: keys.node(id),
    queryFn: async () => unwrap(await api.GET("/nodes/{nodeId}", { params: { path: { nodeId: id } } })),
    enabled: id !== "",
  });
}

export function useUsers() {
  return useQuery({ queryKey: keys.users, queryFn: async () => unwrap(await api.GET("/users")).items });
}

// Elke wijziging raakt lijsten, details en de eventlog; alles opnieuw laden
// is eenvoudig en goedkoop bij deze aantallen.
function useInvalidate() {
  const qc = useQueryClient();
  return () => {
    void qc.invalidateQueries({ queryKey: keys.clusters });
    void qc.invalidateQueries({ queryKey: keys.nodes });
    void qc.invalidateQueries({ queryKey: ["events"] });
  };
}

export function useCreateCluster() {
  const invalidate = useInvalidate();
  return useMutation({
    mutationFn: async (body: ClusterInput) => unwrap(await api.POST("/clusters", { body })),
    onSuccess: invalidate,
  });
}

export function useUpdateCluster(id: string) {
  const invalidate = useInvalidate();
  return useMutation({
    mutationFn: async (body: ClusterPatch) =>
      unwrap(await api.PATCH("/clusters/{clusterId}", { params: { path: { clusterId: id } }, body })),
    onSuccess: invalidate,
  });
}

export function useDeleteCluster() {
  const invalidate = useInvalidate();
  return useMutation({
    mutationFn: async (id: string) =>
      unwrap(await api.DELETE("/clusters/{clusterId}", { params: { path: { clusterId: id } } })),
    onSuccess: invalidate,
  });
}

export function useCreateNode() {
  const invalidate = useInvalidate();
  return useMutation({
    mutationFn: async (body: NodeInput) => unwrap(await api.POST("/nodes", { body })),
    onSuccess: invalidate,
  });
}

export function useUpdateNode() {
  const invalidate = useInvalidate();
  return useMutation({
    mutationFn: async ({ id, body }: { id: string; body: NodePatch }) =>
      unwrap(await api.PATCH("/nodes/{nodeId}", { params: { path: { nodeId: id } }, body })),
    onSuccess: invalidate,
  });
}

export function useDeleteNode() {
  const invalidate = useInvalidate();
  return useMutation({
    mutationFn: async (id: string) => unwrap(await api.DELETE("/nodes/{nodeId}", { params: { path: { nodeId: id } } })),
    onSuccess: invalidate,
  });
}

export function useCreateVip(clusterId: string) {
  const invalidate = useInvalidate();
  return useMutation({
    mutationFn: async (body: VipInput) =>
      unwrap(await api.POST("/clusters/{clusterId}/vips", { params: { path: { clusterId } }, body })),
    onSuccess: invalidate,
  });
}

export function useUpdateVip() {
  const invalidate = useInvalidate();
  return useMutation({
    mutationFn: async ({ id, body }: { id: string; body: VipPatch }) =>
      unwrap(await api.PATCH("/vips/{vipId}", { params: { path: { vipId: id } }, body })),
    onSuccess: invalidate,
  });
}

export function useDeleteVip() {
  const invalidate = useInvalidate();
  return useMutation({
    mutationFn: async (id: string) => unwrap(await api.DELETE("/vips/{vipId}", { params: { path: { vipId: id } } })),
    onSuccess: invalidate,
  });
}
