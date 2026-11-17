"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ApiError, getCsrfToken } from "./api/client";

// Homepage-apps (fase 36): apps die ClusterForge zelf herkent en als tegel
// aan de homepage geeft. Deze endpoints staan buiten de OpenAPI.

export type HomeAppOverride = {
  hidden?: boolean;
  name?: string;
  group?: string;
  icon?: string;
  url?: string;
  sort?: number;
  fields?: string[];
};

export type HomeAppInstance = {
  node_id: string;
  node: string;
  ip?: string;
  online: boolean;
  port?: number;
  kind?: string;
  http_status?: number;
  container?: string;
  image?: string;
  unit?: string;
};

export type HomeApp = {
  key: string;
  slug: string;
  name: string;
  group: string;
  icon: string;
  catalog?: string;
  found_by: string;
  status: "ok" | "warn" | "down" | "unknown";
  status_label: string;
  reason?: string;
  gone: boolean;
  first_seen?: string;
  last_seen?: string;
  cluster?: { id: string; slug: string; name: string };
  vip?: string;
  active_node?: string;
  url?: string;
  internal_url?: string;
  version?: string;
  domains: string[];
  authentik?: string;
  ports: number[];
  instances: HomeAppInstance[];
  problems: string[];
  override: HomeAppOverride;
  hidden: boolean;
  sort: number;
};

export type HomeAppDoubt = {
  key: string;
  name: string;
  node?: string;
  cluster?: string;
  port: number;
  kind: string;
  title?: string;
  image?: string;
  unit?: string;
};

export type HomeAppsAdmin = {
  apps: HomeApp[];
  doubts: HomeAppDoubt[];
  values: Record<string, Record<string, unknown>>;
  fields: Record<string, { label: string; format?: string }>;
  default_fields: string[];
  groups: string[];
  base_path: string;
  generated_at: string;
};

async function call<T>(
  method: string,
  path: string,
  body?: unknown,
): Promise<T> {
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
  if (!res.ok)
    throw new ApiError(
      res.status,
      data?.code ?? "unknown",
      data?.message ?? `Onverwachte fout (${res.status})`,
    );
  return data as T;
}

const key = ["home-apps"] as const;

export function useHomeApps() {
  return useQuery({
    queryKey: key,
    queryFn: () => call<HomeAppsAdmin>("GET", "/home-apps"),
    refetchInterval: 30_000,
  });
}

export function useSaveOverride() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (b: { key: string; override: HomeAppOverride }) =>
      call<HomeAppsAdmin>("POST", "/home-apps/override", b),
    onSuccess: (d) => qc.setQueryData(key, d),
  });
}

export function useDecideDoubt() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (b: {
      key: string;
      choice: "" | "ignore" | "app";
      name?: string;
    }) => call<HomeAppsAdmin>("POST", "/home-apps/doubt", b),
    onSuccess: (d) => qc.setQueryData(key, d),
  });
}
