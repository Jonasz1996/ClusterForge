"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ApiError, getCsrfToken } from "./api/client";

// Fase 46: automatisch onderhoud door de agents (schijf en databases).
// Buiten de OpenAPI, zoals ClusterForge zelf.

export type UpkeepPolicy = {
  enabled: boolean;
  window: string;
  journal: boolean;
  journal_mb: number;
  apt: boolean;
  logrotate: boolean;
  old_logs: boolean;
  old_logs_days: number;
  coredumps: boolean;
  tmp: boolean;
  tmp_days: number;
  docker_images: boolean;
  docker_image_days: number;
  docker_build_cache: boolean;
  docker_logs_mb: number;
  db_analyze: boolean;
  db_optimize: boolean;
  optimize_min_mb: number;
  optimize_min_pct: number;
  urgent: boolean;
  pause_seconds: number;
};

export type UpkeepItem = { id: string; title: string; status: "ok" | "fail" | "skip" | "plan"; note?: string; bytes: number };
export type UpkeepDB = { engine: string; role: string; action: string; count: number; note: string };
export type UpkeepTable = { engine: string; name: string; free: number; size: number };

export type UpkeepResult = {
  skipped?: string;
  used_before: number;
  used_after: number;
  freed: number;
  items: UpkeepItem[];
  db: UpkeepDB[];
  tables: UpkeepTable[];
  order?: number;
  why?: string;
};

export type UpkeepRow = {
  id: number;
  job_id: string;
  node_id: string;
  cluster_id?: string;
  hostname: string;
  mode: "run" | "preview" | "urgent";
  at: string;
  ok: boolean;
  freed: number;
  result: UpkeepResult;
};

export type UpkeepNode = {
  node_id: string;
  hostname: string;
  online: boolean;
  lifecycle: string;
  vip_holder: boolean;
  db_engine?: string;
  db_role?: string;
  galera_expect?: number;
  order: number;
  why: string;
  last?: UpkeepRow;
  preview?: UpkeepRow;
};

export type UpkeepScope = {
  scope: string;
  kind: "env" | "standalone" | "cluster";
  name: string;
  slug?: string;
  environment?: string;
  cluster_id?: string;
  mode: "inherit" | "own" | "off";
  own?: UpkeepPolicy;
  effective: UpkeepPolicy;
  updated_by?: string;
  updated_at?: string;
  next_start?: string;
  last_run?: { started_at: string; job_id?: string; note?: string };
  running_job?: string;
  nodes: UpkeepNode[];
};

export type UpkeepOverview = { timezone: string; defaults: UpkeepPolicy; scopes: UpkeepScope[] };

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

export function useUpkeep() {
  return useQuery({
    queryKey: ["upkeep"],
    queryFn: () => call<UpkeepOverview>("GET", "/upkeep"),
    refetchInterval: (q) => (q.state.data?.scopes.some((s) => s.running_job) ? 5_000 : 30_000),
  });
}

export function useUpkeepHistory(filter: { node?: string; cluster?: string } = {}) {
  const qs = new URLSearchParams();
  if (filter.node) qs.set("node", filter.node);
  if (filter.cluster) qs.set("cluster", filter.cluster);
  const q = qs.toString();
  return useQuery({
    queryKey: ["upkeep-history", filter.node ?? "", filter.cluster ?? ""],
    queryFn: () => call<{ results: UpkeepRow[] }>("GET", `/upkeep/history${q ? `?${q}` : ""}`),
    refetchInterval: 30_000,
  });
}

export function useSetUpkeepPolicy() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (b: { scope: string; mode: string; policy: UpkeepPolicy }) =>
      call<UpkeepOverview>("PUT", `/upkeep/scopes/${encodeURIComponent(b.scope)}`, { mode: b.mode, policy: b.policy }),
    onSuccess: (data) => qc.setQueryData(["upkeep"], data),
  });
}

export function useStartUpkeep() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (b: { scope: string; preview: boolean }) =>
      call<{ job_id: string }>("POST", `/upkeep/scopes/${encodeURIComponent(b.scope)}/start`, { preview: b.preview }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["upkeep"] });
      qc.invalidateQueries({ queryKey: ["upkeep-history"] });
    },
  });
}

// bytes leesbaar, zoals 1,4 GB.
export function human(b: number) {
  if (!b || b < 1000) return `${b || 0} B`;
  const u = ["kB", "MB", "GB", "TB", "PB"];
  let v = b / 1000;
  let i = 0;
  while (v >= 1000 && i < u.length - 1) {
    v /= 1000;
    i++;
  }
  return `${v.toFixed(1).replace(".", ",")} ${u[i]}`;
}

export const engineNames: Record<string, string> = { mariadb: "MariaDB", postgresql: "PostgreSQL" };

export function policyText(p: UpkeepPolicy) {
  if (!p.enabled) return "uit";
  const disk = [p.journal, p.apt, p.logrotate, p.old_logs, p.coredumps, p.tmp].filter(Boolean).length;
  const parts: string[] = [];
  if (disk) parts.push("logs en caches");
  if (p.docker_images || p.docker_build_cache || p.docker_logs_mb > 0) parts.push("Docker");
  if (p.db_analyze || p.db_optimize) parts.push(p.db_optimize ? "databases (ook OPTIMIZE/REINDEX)" : "databases (ANALYZE/VACUUM)");
  return `${parts.join(", ") || "niets"} · ${p.window}${p.urgent ? " · meteen bij volle schijf" : ""}`;
}
