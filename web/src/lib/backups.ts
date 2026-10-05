"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, unwrap } from "./api/client";
import type { components } from "./api/schema";

export type BackupOverview = components["schemas"]["BackupOverview"];
export type BackupItem = components["schemas"]["BackupItem"];
export type BackupVolume = components["schemas"]["BackupVolume"];
export type BackupConnection = components["schemas"]["BackupConnection"];
export type BackupFreshness = components["schemas"]["BackupFreshness"];
export type BackupPolicy = components["schemas"]["BackupPolicy"];
export type BackupWatchEntry = components["schemas"]["BackupWatchEntry"];

type Tone = "slate" | "green" | "amber" | "red" | "blue";

export const freshnessInfo: Record<BackupFreshness, { label: string; tone: Tone }> = {
  ok: { label: "Vers", tone: "green" },
  stale: { label: "Te oud", tone: "amber" },
  missing: { label: "Geen back-up", tone: "red" },
  unknown: { label: "Onbekend", tone: "slate" },
};

const keys = {
  all: ["backups"] as const,
  node: (id: string) => ["backups", "node", id] as const,
  policy: (id: string) => ["backups", "policy", id] as const,
};

// De versheid wordt elke minuut berekend; de schermen volgen dat ritme.
const interval = 60_000;

export function useBackups(enabled = true) {
  return useQuery({
    queryKey: keys.all,
    queryFn: async () => unwrap(await api.GET("/backups")),
    refetchInterval: interval,
    enabled,
  });
}

export function useNodeBackups(id: string) {
  return useQuery({
    queryKey: keys.node(id),
    queryFn: async () => unwrap(await api.GET("/nodes/{nodeId}/backups", { params: { path: { nodeId: id } } })),
    refetchInterval: interval,
  });
}

export function useBackupPolicy(clusterId: string) {
  return useQuery({
    queryKey: keys.policy(clusterId),
    queryFn: async () =>
      unwrap(await api.GET("/clusters/{clusterId}/backup-policy", { params: { path: { clusterId } } })),
  });
}

export function useUpdateBackupPolicy(clusterId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (max_age_hours: number) =>
      unwrap(
        await api.PUT("/clusters/{clusterId}/backup-policy", {
          params: { path: { clusterId } },
          body: { max_age_hours },
        }),
      ),
    onSuccess: () => void qc.invalidateQueries({ queryKey: keys.all }),
  });
}

// useSetBackupWatch vervangt de lijst "ook bewaken" van een koppeling.
export function useSetBackupWatch(proxmoxId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (items: BackupWatchEntry[]) =>
      unwrap(await api.PUT("/proxmox/{proxmoxId}/backup-watch", { params: { path: { proxmoxId } }, body: { items } })),
    onSuccess: () => void qc.invalidateQueries({ queryKey: keys.all }),
  });
}

// useRefreshBackups leest de back-ups van een koppeling nu opnieuw.
export function useRefreshBackups() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (id: string) =>
      unwrap(await api.POST("/proxmox/{proxmoxId}/sync", { params: { path: { proxmoxId: id } } })),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: keys.all });
      void qc.invalidateQueries({ queryKey: ["proxmox"] });
    },
  });
}

// backupAge zegt hoe oud een back-up is, in uren of dagen.
export function backupAge(iso: string, now = Date.now()) {
  const h = Math.max(0, (now - new Date(iso).getTime()) / 3_600_000);
  if (h < 1) return "minder dan een uur";
  if (h < 48) return `${Math.floor(h)} uur`;
  return `${Math.floor(h / 24)} dagen`;
}

export const backupTimeFmt = new Intl.DateTimeFormat("nl-BE", {
  day: "2-digit",
  month: "2-digit",
  year: "numeric",
  hour: "2-digit",
  minute: "2-digit",
});
