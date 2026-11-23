"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ApiError, getCsrfToken } from "./api/client";
import type { OpnAdviceItem } from "./opnsense";
import type { Job } from "./proxmox";

// Fase 48: een losse installatie vervangen door een cluster, de oude later
// opruimen, en de voorstellen naar het VIP. Buiten de OpenAPI (zoals /self);
// de types volgen internal/replace en internal/httpapi/replace.go.

export type Replacement = {
  id: string;
  cluster_id: string;
  cluster_name: string;
  old_node_id?: string;
  old_hostname?: string;
  old_address: string;
  vip?: string;
  reason: "overname" | "clusterforge" | "vervangen";
  keep_days: number;
  state: "actief" | "opruimen" | "opgeruimd" | "gehouden";
  job_id?: string;
  cleanup_job?: string;
  note?: string;
  created_at: string;
  due_at: string;
  due: boolean;
  guest: boolean;
  old_online: boolean;
};

export type VIPAdvice = {
  vips: string[];
  firewall: { firewall_id: string; cluster: string; confirm: string; prod: boolean; items: OpnAdviceItem[] }[];
  firewall_error?: string;
  hosts: { from: string; to: string; why: string; hosts: string[] }[];
  hosts_to_vip?: number;
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

export function useReplacements(cluster: string) {
  return useQuery({
    queryKey: ["replacements", cluster],
    queryFn: () => call<{ items: Replacement[]; default_keep_days: number }>("GET", `/replacements?cluster=${encodeURIComponent(cluster)}`),
    enabled: !!cluster,
    refetchInterval: 30_000,
  });
}

export function useVIPAdvice(cluster: string, enabled = true) {
  return useQuery({
    queryKey: ["vip-advice", cluster],
    queryFn: () => call<VIPAdvice>("GET", `/vip-advice?cluster=${encodeURIComponent(cluster)}`),
    enabled: !!cluster && enabled,
    staleTime: 60_000,
  });
}

function useDone() {
  const qc = useQueryClient();
  return () => {
    void qc.invalidateQueries({ queryKey: ["replacements"] });
    void qc.invalidateQueries({ queryKey: ["vip-advice"] });
    void qc.invalidateQueries({ queryKey: ["jobs"] });
    void qc.invalidateQueries({ queryKey: ["attention"] });
  };
}

export function useCreateReplacement() {
  const done = useDone();
  return useMutation({
    mutationFn: (b: { cluster_id: string; old_node_id?: string; old_address?: string; keep_days?: number; takeover: boolean }) =>
      call<{ job?: Job; replacement?: Replacement }>("POST", "/replacements", b),
    onSuccess: done,
  });
}

export function useKeepReplacement() {
  const done = useDone();
  return useMutation({
    mutationFn: (b: { id: string; keep_days: number; keep: boolean }) => call<Replacement>("PUT", `/replacements/${b.id}`, b),
    onSuccess: done,
  });
}

export function useCleanupReplacement() {
  const done = useDone();
  return useMutation({
    mutationFn: (b: { id: string; confirm: string; storage?: string }) => call<Job>("POST", `/replacements/${b.id}/cleanup`, b),
    onSuccess: done,
  });
}
