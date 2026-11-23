"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ApiError, getCsrfToken } from "./api/client";

// Testlijst 3, N27: problemen met resources en een oplossing met één klik.

export type ResourceThresholds = {
  disk_pct: number;
  disk_days: number;
  swap_pct: number;
  mem_pct: number;
  load_ratio: number;
};

export type ResourceSettings = Record<string, ResourceThresholds>;

export type ResourceFix = { label: string; undo: string };

export type ResourceProposal = {
  id: string;
  kind: "disk" | "swap" | "memory" | "cpu" | "restarts" | "dependency" | "tip";
  level: "crit" | "warn" | "info";
  node_id: string;
  node: string;
  environment: string;
  title: string;
  detail?: string;
  href?: string;
  fix?: ResourceFix;
  no_fix?: string;
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

export function useResources(fresh: number) {
  return useQuery({
    queryKey: ["resources", fresh],
    queryFn: () => call<{ items: ResourceProposal[] }>("GET", `/resources${fresh ? "?fresh=1" : ""}`),
    refetchInterval: 60_000,
  });
}

export function useApplyResource() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => call<{ job_id: string }>("POST", "/resources/apply", { id }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["resources"] }),
  });
}

export function useResourceSettings() {
  return useQuery({ queryKey: ["resources-settings"], queryFn: () => call<ResourceSettings>("GET", "/resources/settings") });
}

export function useSetResourceSettings() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (s: ResourceSettings) => call<ResourceSettings>("PUT", "/resources/settings", s),
    onSuccess: (data) => {
      qc.setQueryData(["resources-settings"], data);
      qc.invalidateQueries({ queryKey: ["resources"] });
    },
  });
}
