"use client";

import { keepPreviousData, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, unwrap } from "./api/client";
import type { components } from "./api/schema";

export type ProxmoxConnection = components["schemas"]["ProxmoxConnection"];
export type ProxmoxInput = components["schemas"]["ProxmoxInput"];
export type ProxmoxPatch = components["schemas"]["ProxmoxPatch"];
export type ProxmoxProbe = components["schemas"]["ProxmoxProbe"];
export type ProxmoxResources = components["schemas"]["ProxmoxResources"];
export type ProxmoxHost = components["schemas"]["ProxmoxHost"];
export type ProxmoxGuest = components["schemas"]["ProxmoxGuest"];
export type ProxmoxStorage = components["schemas"]["ProxmoxStorage"];
export type ProxmoxSnapshot = components["schemas"]["ProxmoxSnapshot"];
export type NodeProxmox = components["schemas"]["NodeProxmox"];
export type VmAction = components["schemas"]["VmAction"];
export type VmActionInput = components["schemas"]["VmActionInput"];
export type Job = components["schemas"]["Job"];
export type JobDetail = components["schemas"]["JobDetail"];
export type JobStatus = components["schemas"]["JobStatus"];

type Tone = "slate" | "green" | "amber" | "red" | "blue";

export const vmStatuses: Record<string, { label: string; tone: Tone }> = {
  running: { label: "Draait", tone: "green" },
  stopped: { label: "Uit", tone: "slate" },
  paused: { label: "Gepauzeerd", tone: "amber" },
  suspended: { label: "Gepauzeerd", tone: "amber" },
};
export const vmStatusInfo = (s: string) => vmStatuses[s] ?? { label: s || "onbekend", tone: "slate" as const };

export const jobStatuses: Record<JobStatus, { label: string; tone: Tone }> = {
  queued: { label: "Wacht", tone: "slate" },
  running: { label: "Bezig", tone: "blue" },
  succeeded: { label: "Gelukt", tone: "green" },
  failed: { label: "Mislukt", tone: "red" },
  canceled: { label: "Geannuleerd", tone: "amber" },
};

export const jobActive = (s: JobStatus) => s === "queued" || s === "running";

const keys = {
  all: ["proxmox"] as const,
  resources: (id: string) => ["proxmox", id, "resources"] as const,
  snapshots: (id: string, vmid: number) => ["proxmox", id, "snapshots", vmid] as const,
  jobs: (filter: JobFilter) => ["jobs", filter] as const,
  job: (id: string) => ["jobs", "detail", id] as const,
};

// Proxmox wordt elke 20 s gesynchroniseerd; de schermen volgen dat ritme.
const syncInterval = 20_000;

export function useProxmoxConnections() {
  return useQuery({
    queryKey: keys.all,
    queryFn: async () => unwrap(await api.GET("/proxmox")),
    refetchInterval: syncInterval,
  });
}

export function useProxmoxResources(id: string) {
  return useQuery({
    queryKey: keys.resources(id),
    queryFn: async () => unwrap(await api.GET("/proxmox/{proxmoxId}/resources", { params: { path: { proxmoxId: id } } })),
    enabled: id !== "",
    refetchInterval: syncInterval,
  });
}

export function useVmSnapshots(id: string, vmid: number, enabled: boolean) {
  return useQuery({
    queryKey: keys.snapshots(id, vmid),
    queryFn: async () =>
      unwrap(await api.GET("/proxmox/{proxmoxId}/vms/{vmid}/snapshots", { params: { path: { proxmoxId: id, vmid } } }))
        .items,
    enabled: enabled && id !== "",
    retry: false,
  });
}

function useInvalidateProxmox() {
  const qc = useQueryClient();
  return () => {
    void qc.invalidateQueries({ queryKey: keys.all });
    void qc.invalidateQueries({ queryKey: ["nodes"] });
    void qc.invalidateQueries({ queryKey: ["events"] });
  };
}

export function useCreateProxmox() {
  const invalidate = useInvalidateProxmox();
  return useMutation({
    mutationFn: async (body: ProxmoxInput) => unwrap(await api.POST("/proxmox", { body })),
    onSuccess: invalidate,
  });
}

export function useUpdateProxmox(id: string) {
  const invalidate = useInvalidateProxmox();
  return useMutation({
    mutationFn: async (body: ProxmoxPatch) =>
      unwrap(await api.PATCH("/proxmox/{proxmoxId}", { params: { path: { proxmoxId: id } }, body })),
    onSuccess: invalidate,
  });
}

export function useDeleteProxmox() {
  const invalidate = useInvalidateProxmox();
  return useMutation({
    mutationFn: async (id: string) =>
      unwrap(await api.DELETE("/proxmox/{proxmoxId}", { params: { path: { proxmoxId: id } } })),
    onSuccess: invalidate,
  });
}

export function useSyncProxmox() {
  const invalidate = useInvalidateProxmox();
  return useMutation({
    mutationFn: async (id: string) =>
      unwrap(await api.POST("/proxmox/{proxmoxId}/sync", { params: { path: { proxmoxId: id } } })),
    onSuccess: invalidate,
  });
}

export function useProbeProxmox() {
  return useMutation({
    mutationFn: async (apiUrl: string) => unwrap(await api.POST("/proxmox/probe", { body: { api_url: apiUrl } })),
  });
}

export function useVmAction() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({ proxmoxId, vmid, body }: { proxmoxId: string; vmid: number; body: VmActionInput }) =>
      unwrap(await api.POST("/proxmox/{proxmoxId}/vms/{vmid}/actions", { params: { path: { proxmoxId, vmid } }, body })),
    onSuccess: () => void qc.invalidateQueries({ queryKey: ["jobs"] }),
  });
}

export type JobFilter = { node_id?: string; proxmox_id?: string; cluster_id?: string; limit?: number };

// Taken verversen snel zolang er een loopt.
export function useJobs(filter: JobFilter = {}, enabled = true) {
  return useQuery({
    queryKey: keys.jobs(filter),
    queryFn: async () => unwrap(await api.GET("/jobs", { params: { query: filter } })).items,
    enabled,
    placeholderData: keepPreviousData,
    refetchInterval: (q) => (q.state.data?.some((j) => jobActive(j.status)) ? 2_000 : 15_000),
  });
}

export function useJob(id: string) {
  return useQuery({
    queryKey: keys.job(id),
    queryFn: async () => unwrap(await api.GET("/jobs/{jobId}", { params: { path: { jobId: id } } })),
    enabled: id !== "",
    refetchInterval: (q) => (q.state.data && jobActive(q.state.data.status) ? 1_500 : false),
  });
}

export function useCancelJob() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (id: string) => unwrap(await api.POST("/jobs/{jobId}/cancel", { params: { path: { jobId: id } } })),
    onSuccess: () => void qc.invalidateQueries({ queryKey: ["jobs"] }),
  });
}

// percent zet een verhouding om naar een geheel percentage.
export function percent(used: number, total: number) {
  return total > 0 ? Math.round((used / total) * 100) : 0;
}
