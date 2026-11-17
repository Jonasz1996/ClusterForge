"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, unwrap } from "./api/client";
import type { components } from "./api/schema";

// Schijfbewaking (fase 41): drempels, de stand per node, meten en opruimen.

export type DiskWatchSettings = components["schemas"]["DiskWatchSettings"];
export type NodeDisk = components["schemas"]["NodeDisk"];
export type DiskScan = components["schemas"]["DiskScan"];
export type DiskScanItem = components["schemas"]["DiskScanItem"];

const key = ["disk"] as const;

export function useDiskSettings() {
  return useQuery({ queryKey: [...key, "settings"], queryFn: async () => unwrap(await api.GET("/disk/settings")) });
}

export function useSetDiskSettings() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (body: DiskWatchSettings) => unwrap(await api.PUT("/disk/settings", { body })),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: key });
      void qc.invalidateQueries({ queryKey: ["attention"] });
    },
  });
}

export function useNodeDisk(id: string, enabled = true) {
  return useQuery({
    queryKey: [...key, "node", id],
    enabled,
    queryFn: async () => unwrap(await api.GET("/nodes/{nodeId}/disk", { params: { path: { nodeId: id } } })),
    refetchInterval: (q) => (q.state.data?.running_job_id ? 3_000 : 60_000),
  });
}

function useInvalidate() {
  const qc = useQueryClient();
  return () => {
    void qc.invalidateQueries({ queryKey: key });
    void qc.invalidateQueries({ queryKey: ["jobs"] });
  };
}

export function useScanNodeDisk() {
  const inv = useInvalidate();
  return useMutation({
    mutationFn: async (id: string) => unwrap(await api.POST("/nodes/{nodeId}/disk/scan", { params: { path: { nodeId: id } } })),
    onSuccess: inv,
  });
}

export function useCleanupNodeDisk() {
  const inv = useInvalidate();
  return useMutation({
    mutationFn: async ({ id, items }: { id: string; items: string[] }) =>
      unwrap(await api.POST("/nodes/{nodeId}/disk/cleanup", { params: { path: { nodeId: id } }, body: { items } })),
    onSuccess: inv,
  });
}

// bytes leesbaar, zoals 1,4 GB.
export function human(b: number) {
  if (b < 1000) return `${b} B`;
  const units = ["kB", "MB", "GB", "TB", "PB"];
  let v = b;
  let i = -1;
  while (v >= 1000 && i < units.length - 1) {
    v /= 1000;
    i++;
  }
  return `${v.toLocaleString("nl-BE", { maximumFractionDigits: 1 })} ${units[i]}`;
}
