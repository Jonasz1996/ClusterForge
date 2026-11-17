"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ApiError, getCsrfToken } from "./api/client";
import type { Job } from "./proxmox";

// Fase 37: TrueNAS SCALE / Community Edition via de agent. Lezen gaat met
// midclt (alleen leesmethodes) langs /truenas/{node}/api, veranderen als
// taak met useTrueNASCall. De delen van het overzicht zijn de antwoorden
// van de middleware zoals ze zijn.

export const TRUENAS_PROTOCOL = 53;

export type TrueNASServer = {
  node_id: string;
  hostname: string;
  address?: string;
  version?: string;
  pools: { name: string; health: string }[];
  persistent: boolean;
  protocol: number;
  agent_version?: string;
  online: boolean;
  heartbeat_at?: string;
  why?: string;
};

export type TNDate = { $date: number } | number | string | null | undefined;

export function tnDate(d: TNDate): Date | null {
  if (d == null) return null;
  if (typeof d === "object") return new Date(d.$date);
  if (typeof d === "number") return new Date(d > 1e12 ? d : d * 1000);
  const t = Date.parse(d);
  return Number.isNaN(t) ? null : new Date(t);
}

export type TNSystem = {
  version?: string;
  hostname?: string;
  uptime_seconds?: number;
  physmem?: number;
  model?: string;
  cores?: number;
  physical_cores?: number;
  loadavg?: number[];
  system_product?: string;
  system_manufacturer?: string;
  ecc_memory?: boolean;
  timezone?: string;
};

export type TNScan = {
  function?: string;
  state?: string;
  start_time?: TNDate;
  end_time?: TNDate;
  percentage?: number;
  errors?: number;
};

export type TNVdev = { name?: string; type?: string; status?: string; disk?: string | null; children?: TNVdev[]; stats?: { read_errors?: number; write_errors?: number; checksum_errors?: number } };

export type TNPool = {
  id: number;
  name: string;
  status: string;
  healthy: boolean;
  warning?: boolean;
  status_detail?: string | null;
  size?: number;
  allocated?: number;
  free?: number;
  fragmentation?: string;
  autotrim?: { value?: string };
  scan?: TNScan | null;
  topology?: Record<string, TNVdev[]>;
};

export type TNDisk = {
  identifier: string;
  name: string;
  serial?: string;
  size?: number;
  model?: string;
  type?: string;
  rotationrate?: number | null;
  pool?: string | null;
  togglesmart?: boolean;
};

export type TNAlert = {
  uuid: string;
  level: string;
  klass?: string;
  formatted?: string;
  datetime?: TNDate;
  dismissed?: boolean;
};

export type TNJob = {
  id: number;
  method: string;
  state: string;
  progress?: { percent?: number | null; description?: string | null };
  time_started?: TNDate;
  time_finished?: TNDate;
  error?: string | null;
};

export type TNSmart = {
  disk?: string;
  identifier?: string;
  tests?: { num?: number; description?: string; status?: string; status_verbose?: string; lifetime?: number }[];
};

export type TrueNASOverview = {
  server: TrueNASServer;
  parts: {
    system?: TNSystem;
    pools?: TNPool[];
    disks?: TNDisk[];
    temps?: Record<string, number | { temperature?: number | null }>;
    alerts?: TNAlert[];
    running?: TNJob[];
    failed?: TNJob[];
    smart?: TNSmart[];
    boot?: { status?: string; healthy?: boolean; size?: number; allocated?: number };
  };
  warnings: { level: "critical" | "warning"; text: string }[];
  errors: Record<string, string>;
};

export function tempOf(v: number | { temperature?: number | null } | undefined): number | null {
  if (v == null) return null;
  if (typeof v === "number") return v;
  return v.temperature ?? null;
}

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

export function useTrueNASServers() {
  return useQuery({
    queryKey: ["truenas", "servers"],
    queryFn: () => call<{ items: TrueNASServer[]; protocol: number }>("GET", "/truenas"),
    refetchInterval: 30_000,
  });
}

export function useTrueNASOverview(id: string) {
  return useQuery({
    queryKey: ["truenas", id, "overview"],
    queryFn: () => call<TrueNASOverview>("GET", `/truenas/${id}/overview`),
    enabled: !!id,
    refetchInterval: 20_000,
    retry: false,
  });
}

export function useTrueNASGet<T>(id: string, method: string, args?: unknown[], opts: { enabled?: boolean; refetch?: number | false } = {}) {
  const a = args ? JSON.stringify(args) : "";
  return useQuery({
    queryKey: ["truenas", id, "get", method, a],
    queryFn: async () => {
      const q = new URLSearchParams({ method });
      if (a) q.set("args", a);
      return (await call<{ data: T }>("GET", `/truenas/${id}/api?${q.toString()}`)).data;
    },
    enabled: (opts.enabled ?? true) && !!id,
    refetchInterval: opts.refetch ?? 20_000,
    retry: false,
  });
}

export type TrueNASCall = { method: string; args: unknown[]; confirm?: string };

export function useTrueNASCall(id: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (c: TrueNASCall) => call<Job>("POST", `/truenas/${id}/call`, c),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["jobs"] });
      const again = () => qc.invalidateQueries({ queryKey: ["truenas", id] });
      again();
      setTimeout(again, 3_000);
      setTimeout(again, 10_000);
    },
  });
}
