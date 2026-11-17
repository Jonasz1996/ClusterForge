"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, unwrap } from "./api/client";
import type { Job } from "./proxmox";

// Fase 35: diensten publiceren via Nginx Proxy Manager en beschermen met
// Authentik. Alles gaat langs de API van NPM (database én config samen),
// met een kopie van NPM vooraf, vijf controles erna en terugdraaien bij een
// fout. De types volgen internal/publish.

export const PUBLISH_PROTOCOL = 51;

export type PublishDefaults = {
  ssl_forced: boolean;
  http2: boolean;
  hsts: boolean;
  websockets: boolean;
  block_exploits: boolean;
  authentik: boolean;
  authentik_skip: string[];
  letsencrypt: boolean;
  letsencrypt_email?: string;
};

export type PublishSettings = {
  domain: string;
  defaults: PublishDefaults;
  npm_cluster_id?: string;
  npm_node_id?: string;
  npm_port: number;
  npm_identity: string;
  npm_has_secret: boolean;
  authentik_cluster_id?: string;
  authentik_node_id?: string;
  authentik_port: number;
  authentik_has_token: boolean;
  authentik_outpost: string;
  updated_at: string;
};

export type PublishSettingsInput = {
  domain: string;
  defaults?: PublishDefaults;
  npm_cluster_id?: string;
  npm_node_id?: string;
  npm_port?: number;
  npm_identity: string;
  npm_secret?: string;
  clear_npm?: boolean;
  authentik_cluster_id?: string;
  authentik_node_id?: string;
  authentik_port?: number;
  authentik_token?: string;
  authentik_outpost: string;
  clear_authentik?: boolean;
};

export type AKState = "beschermd" | "half" | "open" | "";

export type PublishProblem = {
  code: string;
  level: "crit" | "warn";
  text: string;
};

export type PublishHost = {
  id: number;
  domains: string[];
  forward_scheme: string;
  forward_host: string;
  forward_port: number;
  enabled: boolean;
  ssl_forced: boolean;
  websockets: boolean;
  certificate_id: number;
  certificate_name?: string;
  certificate_expires?: string;
  access_list?: string;
  has_advanced: boolean;
  managed: boolean;
  created_by_cf: boolean;
  authentik: AKState;
  authentik_detail?: string;
  authentik_by_cf: boolean;
  problems: PublishProblem[];
  drift?: string[];
  service_key?: string;
  service_name?: string;
  cluster_name?: string;
  suggest_vip?: string;
};

export type PublishService = {
  key: string;
  cluster_id?: string;
  cluster_name?: string;
  environment?: string;
  nodes: { id: string; hostname: string; ip?: string }[];
  on_all: boolean;
  proto: string;
  port: number;
  kind?: string;
  name: string;
  title?: string;
  container?: string;
  image?: string;
  unit?: string;
  status?: number;
  server?: string;
  target: string;
  target_vip: boolean;
  label: string;
  web: boolean;
  published: { host_id: number; domain: string; authentik: AKState }[];
  authentik_default: boolean;
};

export type PublishApp = {
  name: string;
  slug: string;
  provider?: string;
  mode?: string;
  external_host?: string;
  in_outpost: boolean;
  host_id?: number;
  snippet: boolean;
  state: AKState;
  detail?: string;
};

export type PublishReadState = {
  linked: boolean;
  hostname?: string;
  version?: string;
  read_at?: string;
  error?: string;
};

export type PublishOverview = {
  settings: PublishSettings;
  services: PublishService[];
  hosts: PublishHost[];
  apps: PublishApp[];
  nodes: {
    id: string;
    hostname: string;
    cluster_name?: string;
    ready?: string;
    error?: string;
    checked_at?: string;
    services: number;
  }[];
  npm: PublishReadState;
  authentik: PublishReadState;
  notices: {
    id: string;
    red: boolean;
    title: string;
    detail?: string;
    names?: string[];
    href: string;
    count: number;
  }[];
  counts: {
    services: number;
    web: number;
    unpublished: number;
    hosts: number;
    managed: number;
    protected: number;
    problems: number;
  };
  clusters: { id: string; name: string; template?: string; vips?: string[] }[];
  protocol: number;
};

export type PublishActionKind = "publish" | "update" | "retarget" | "protect" | "unprotect" | "delete" | "restore" | "rename";

export type PublishAction = {
  action: PublishActionKind;
  host_id?: number;
  service_key?: string;
  label?: string;
  forward_scheme?: string;
  forward_host?: string;
  forward_port?: number;
  ssl?: string;
  websockets?: boolean;
  block_exploits?: boolean;
  http2?: boolean;
  hsts?: boolean;
  authentik?: boolean;
  authentik_group?: string;
  confirm?: string;
  hosts?: number[];
  old_domain?: string;
  new_domain?: string;
  keep_old?: boolean;
};

export type PublishPlan = {
  title: string;
  lines: string[] | null;
  warnings: string[] | null;
  hosts:
    | {
        host_id: number;
        domain: string;
        cert_new?: boolean;
        delete?: boolean;
        protect?: boolean;
        unprotect?: boolean;
      }[]
    | null;
  confirm?: string;
};

const keys = {
  overview: ["publish", "overview"] as const,
  settings: ["publish", "settings"] as const,
};

type Body = Record<string, unknown>;

export function usePublishOverview() {
  return useQuery({
    queryKey: keys.overview,
    queryFn: async () => unwrap(await api.GET("/publish/overview")) as unknown as PublishOverview,
    refetchInterval: 20_000,
  });
}

export function usePublishSettings() {
  return useQuery({
    queryKey: keys.settings,
    queryFn: async () => unwrap(await api.GET("/publish/settings")) as unknown as PublishSettings,
  });
}

function useInvalidate() {
  const qc = useQueryClient();
  return () => {
    void qc.invalidateQueries({ queryKey: ["publish"] });
    void qc.invalidateQueries({ queryKey: ["jobs"] });
    void qc.invalidateQueries({ queryKey: ["attention"] });
  };
}

export function useSavePublishSettings() {
  const done = useInvalidate();
  return useMutation({
    mutationFn: async (body: PublishSettingsInput) =>
      unwrap(await api.PUT("/publish/settings", { body: body as unknown as Body })) as unknown as PublishSettings,
    onSuccess: done,
  });
}

export function useTestPublish() {
  return useMutation({
    mutationFn: async (what: "npm" | "authentik") =>
      unwrap(await api.POST("/publish/test/{what}", { params: { path: { what } } })) as unknown as Record<string, string | number>,
  });
}

export function useRefreshPublish() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async () => unwrap(await api.POST("/publish/refresh")) as unknown as PublishOverview,
    onSuccess: (data) => qc.setQueryData(keys.overview, data),
  });
}

export async function planPublish(a: PublishAction): Promise<PublishPlan> {
  return unwrap(await api.POST("/publish/plan", { body: a as unknown as Body })) as unknown as PublishPlan;
}

export function useStartPublish() {
  const done = useInvalidate();
  return useMutation({
    mutationFn: async (a: PublishAction) =>
      unwrap(await api.POST("/publish/actions", { body: a as unknown as Body })) as unknown as { job: Job; plan: PublishPlan },
    onSuccess: done,
  });
}

export function useAdoptHosts() {
  const done = useInvalidate();
  return useMutation({
    mutationFn: async (ids: number[]) => unwrap(await api.POST("/publish/adopt", { body: { ids } as Body })) as unknown as { adopted: number },
    onSuccess: done,
  });
}

export function useAcceptDrift() {
  const done = useInvalidate();
  return useMutation({
    mutationFn: async (hostId: number) =>
      unwrap(
        await api.POST("/publish/hosts/{hostId}/accept", {
          params: { path: { hostId } },
        }),
      ),
    onSuccess: done,
  });
}

export function useReleaseHost() {
  const done = useInvalidate();
  return useMutation({
    mutationFn: async (hostId: number) =>
      unwrap(
        await api.POST("/publish/hosts/{hostId}/release", {
          params: { path: { hostId } },
        }),
      ),
    onSuccess: done,
  });
}

export const akLabel: Record<AKState, string> = {
  beschermd: "achter Authentik",
  half: "half ingesteld",
  open: "open",
  "": "onbekend",
};

export const akTone = (s: AKState) => (s === "beschermd" ? "green" : s === "half" ? "amber" : "slate");

export function fullName(label: string, domain: string) {
  const l = label.trim().toLowerCase().replace(/\.$/, "");
  return l.includes(".") ? l : `${l}.${domain}`;
}
