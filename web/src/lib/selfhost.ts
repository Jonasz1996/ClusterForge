"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ApiError, getCsrfToken } from "./api/client";
import type { Job } from "./proxmox";

// Fase 45: ClusterForge zelf bijwerken vanuit de webinterface en verhuizen
// naar een eigen cluster (template clusterforge). Buiten de OpenAPI.

export type SelfNode = { id: string; hostname: string; address?: string; online: boolean; protocol: number };

export type SelfTarget = {
  id: string;
  name: string;
  slug: string;
  environment: string;
  status: string;
  vip?: string;
  nodes: SelfNode[];
};

export type SelfJob = {
  id: string;
  kind: "clusterforge.update" | "clusterforge.move";
  title: string;
  status: string;
  error?: string;
  created_at: string;
  finished_at?: string;
};

export type SelfInfo = {
  mode: "compose" | "cluster";
  version: string;
  image: string;
  repo: string;
  master_key: boolean;
  export: boolean;
  host_node_id?: string;
  hosts: SelfNode[];
  cluster?: SelfTarget;
  targets: SelfTarget[];
  jobs: SelfJob[];
  stragglers: SelfNode[];
  offline: SelfNode[];
  protocol: number;
};

async function call<T>(method: string, path: string, body?: unknown): Promise<T> {
  const headers: Record<string, string> = { Accept: "application/json" };
  if (body !== undefined) headers["Content-Type"] = "application/json";
  const csrf = getCsrfToken();
  if (csrf && method !== "GET") headers["X-CSRF-Token"] = csrf;
  const res = await fetch(`/api/v1${path}`, {
    method,
    headers,
    credentials: "same-origin",
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const data = await res.json().catch(() => ({}));
  if (!res.ok) throw new ApiError(res.status, data?.code ?? "unknown", data?.message ?? `Onverwachte fout (${res.status})`);
  return data as T;
}

export function useSelfInfo() {
  return useQuery({
    queryKey: ["self"],
    queryFn: () => call<SelfInfo>("GET", "/self"),
    // Tijdens bijwerken herstart de server: blijven proberen.
    refetchInterval: (q) => (q.state.data?.jobs.some((j) => j.status === "running" || j.status === "queued") ? 5_000 : 30_000),
    retry: true,
  });
}

export function useCheckImage() {
  return useMutation({
    mutationFn: (image: string) => call<{ image: string; exists: boolean; error?: string }>("POST", "/self/check-image", { image }),
  });
}

export type SelfUpdate = { image: string; host_node_id?: string; totp_code?: string; password?: string };

export function useSelfUpdate() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (b: SelfUpdate) => call<Job>("POST", "/self/update", b),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["self"] });
      qc.invalidateQueries({ queryKey: ["jobs"] });
    },
  });
}

export type SelfMove = {
  cluster_id: string;
  host_node_id: string;
  force?: boolean;
  metrics?: boolean;
  totp_code?: string;
  password?: string;
};

export function useSelfMove() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (b: SelfMove) => call<Job>("POST", "/self/move", b),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["self"] });
      qc.invalidateQueries({ queryKey: ["jobs"] });
    },
  });
}

// ImportNode volgt internal/selfhost/metricsimport.go (fase 48, 4.5).
export type ImportNode = {
  address: string;
  hostname?: string;
  state: "bezig" | "klaar" | "mislukt";
  started_at?: string;
  ended_at?: string;
  bytes?: number;
  reason?: string;
};

export function useMetricsImport(enabled: boolean) {
  return useQuery({
    queryKey: ["self", "metrics-import"],
    queryFn: () => call<{ nodes: ImportNode[] }>("GET", "/self/metrics-import"),
    enabled,
    refetchInterval: (q) => (q.state.data?.nodes.some((n) => n.state === "bezig") ? 10_000 : false),
  });
}
