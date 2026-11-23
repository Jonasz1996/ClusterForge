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
  authentik_auto_repair?: boolean;
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
  suggest?: PublishSuggestion;
  http2: boolean;
  hsts: boolean;
  block_exploits: boolean;
  advanced?: string;
  typo?: string;
  typo_domains?: string[];
  advanced_fix?: { old: string; new: string; why: string }[];
  themed?: boolean;
  css?: string;
  css_set?: string;
  // Testlijst 3: wat het doel zelf antwoordde, en "opruimen?" als het dood is.
  target_probe?: { status?: number; title?: string; error?: string; at: string };
  target_alive?: boolean;
  cleanup?: boolean;
};

export type PublishDeadNet = { prefix: string; hosts: number[]; names: string[] };

export type PublishSuggestion = {
  forward_scheme: string;
  forward_host: string;
  forward_port: number;
  service_key: string;
  service_name: string;
  cluster_name?: string;
  vip: boolean;
  why: string;
  sure: boolean;
};

export type PublishCert = { id: number; name: string; domains: string[]; provider: string; expires?: string };

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
  internal?: boolean;
  internal_why?: string;
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
    npm_standby?: boolean;
    npm_copied_at?: string;
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
    internal?: number;
    hosts: number;
    managed: number;
    protected: number;
    problems: number;
  };
  clusters: { id: string; name: string; template?: string; vips?: string[] }[];
  certs?: PublishCert[];
  outpost?: string;
  protocol: number;
  dead_nets?: PublishDeadNet[];
};

export type PublishActionKind =
  | "publish"
  | "update"
  | "retarget"
  | "retarget_many"
  | "protect"
  | "unprotect"
  | "delete"
  | "restore"
  | "rename"
  | "theme_many"
  | "ak_repair"
  | "move_address"
  | "delete_dead"
  | "revert";

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
  net?: string;
  old_domain?: string;
  new_domain?: string;
  keep_old?: boolean;
  advanced_config?: string;
  ssl_forced?: boolean;
  domains?: string[];
  theme?: boolean;
  // De host mag daarna een andere app tonen (anders draait de controle terug).
  allow_other_app?: boolean;
  change_id?: string;
};

// Een wijziging die ClusterForge aan een proxy host deed (testlijst 2, 1.1).
export type PublishChange = {
  id: string;
  job_id?: string;
  host_id: number;
  domain: string;
  action: string;
  created: boolean;
  deleted: boolean;
  title_before?: string;
  title_after?: string;
  changed_by?: string;
  changed_at: string;
  reverted_at?: string;
  reverted_by?: string;
  summary: string[];
};

export function usePublishChanges(hours = 24) {
  return useQuery({
    queryKey: ["publish", "changes", hours],
    queryFn: async () =>
      unwrap(await api.GET("/publish/changes", { params: { query: { hours } } })) as unknown as PublishChange[],
  });
}

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

// --- Fase 39: verkeer van NPM en logins van Authentik ---

export type TrafficWindow = "1h" | "24h" | "7d";

export type PublishBan = {
  scenario: string;
  origin: string;
  since: string;
  until: string;
  country?: string;
  as?: string;
  nodes: string[];
};

export type PublishFeed = { source: string; detail?: string; at: string; ok_at?: string; error?: string; lines: number };

export type TrafficView = {
  window: TrafficWindow;
  from: string;
  totals: {
    requests: number;
    visitors: number;
    s4xx: number;
    s5xx: number;
    denied: number;
    bytes: number;
    connections: number;
    connections_at?: string;
  };
  series: { t: string; requests: number; s4xx: number; s5xx: number }[];
  hosts: {
    host_id: number;
    domain: string;
    requests: number;
    visitors: number;
    s4xx: number;
    s5xx: number;
    denied: number;
    bytes: number;
    last?: string;
  }[];
  ips: {
    ip: string;
    requests: number;
    s4xx: number;
    s5xx: number;
    denied: number;
    bytes: number;
    hosts: string[];
    first: string;
    last: string;
    ban?: PublishBan;
    after_ban: number;
    failed_logins: number;
    suspicious: boolean;
    why?: string;
  }[];
  connections: { t: string; count: number }[];
  compare: { banned_seen: number; banned_passing: number; suspicious: number; active_bans: number };
  feeds: PublishFeed[];
};

export type LoginsView = {
  window: TrafficWindow;
  from: string;
  totals: { logins: number; failed: number; users: number; failed_ips: number; failed_ips_not_banned: number; app_authorizations: number };
  series: { t: string; logins: number; failed: number }[];
  users: { username: string; logins: number; failed: number; last?: string; last_ip?: string }[];
  failed_ips: { ip: string; failed: number; usernames: string[]; last: string; country?: string; ban?: PublishBan; requests: number }[];
  apps: { app: string; count: number; users: number }[];
  recent: {
    created: string;
    action: string;
    username: string;
    ip: string;
    app?: string;
    country?: string;
    city?: string;
    reason?: string;
    banned: boolean;
  }[];
  feed?: PublishFeed;
};

export function usePublishTraffic(window: TrafficWindow) {
  return useQuery({
    queryKey: ["publish", "traffic", window],
    queryFn: async () => unwrap(await api.GET("/publish/traffic", { params: { query: { window } } })) as unknown as TrafficView,
    refetchInterval: 60_000,
  });
}

export function usePublishLogins(window: TrafficWindow) {
  return useQuery({
    queryKey: ["publish", "logins", window],
    queryFn: async () => unwrap(await api.GET("/publish/logins", { params: { query: { window } } })) as unknown as LoginsView,
    refetchInterval: 60_000,
  });
}

export const actionLabel: Record<string, string> = {
  login: "aangemeld",
  login_failed: "mislukt",
  logout: "afgemeld",
  authorize_application: "app geopend",
};

// Sjabloon voor Advanced (fase 42b): één algemeen sjabloon met en zonder
// Authentik, met per host een eigen css-bestand.
export type PublishTheme = {
  template_auth: string;
  template_plain: string;
  css_default: string;
  hosts: Record<string, string>;
  default_auth: string;
  default_plain: string;
  updated_at?: string;
};

export function usePublishTheme() {
  return useQuery({
    queryKey: ["publish", "theme"],
    queryFn: async () => unwrap(await api.GET("/publish/theme")) as unknown as PublishTheme,
  });
}

export function useSavePublishTheme() {
  const done = useInvalidate();
  return useMutation({
    mutationFn: async (body: { template_auth: string; template_plain: string; css_default: string }) =>
      unwrap(await api.PUT("/publish/theme", { body: body as unknown as Body })) as unknown as PublishTheme,
    onSuccess: done,
  });
}

export function useSetHostCss() {
  const done = useInvalidate();
  return useMutation({
    mutationFn: async ({ hostId, css }: { hostId: number; css: string }) =>
      unwrap(
        await api.PUT("/publish/theme/hosts/{hostId}", { params: { path: { hostId } }, body: { css } as unknown as Body }),
      ) as unknown as PublishTheme,
    onSuccess: done,
  });
}

// Fase 48: automatisch publiceren per cluster of dienst. Nooit een gok:
// alleen diensten met een exact doel (VIP of de enige node) en een vrije
// naam; de rest staat met de reden in de lijst.
export type AutoRule = {
  id: string;
  cluster_id?: string;
  cluster_name?: string;
  service_key?: string;
  enabled: boolean;
  cleanup: boolean;
  updated_at: string;
};

export type AutoItem = {
  service_key: string;
  domain: string;
  cluster_id?: string;
  cluster_name?: string;
  state: "publishing" | "published" | "failed" | "removing" | "removed";
  job_id?: string;
  npm_host_id?: number;
  cleanup: boolean;
  tries: number;
  error?: string;
  has_backup: boolean;
  tried_at?: string;
  updated_at: string;
};

export type AutoCandidate = {
  service_key: string;
  cluster_name?: string;
  name: string;
  domain?: string;
  target: string;
  authentik: boolean;
  skip?: string;
};

export type AutoView = {
  rules: AutoRule[];
  items: AutoItem[];
  candidates: AutoCandidate[];
  ready?: string;
};

export function usePublishAuto() {
  return useQuery({
    queryKey: ["publish", "auto"],
    queryFn: async () => unwrap(await api.GET("/publish/auto")) as unknown as AutoView,
    refetchInterval: 20_000,
  });
}

export function useSaveAutoRule() {
  const done = useInvalidate();
  return useMutation({
    mutationFn: async (body: { cluster_id?: string; service_key?: string; enabled: boolean; cleanup?: boolean }) =>
      unwrap(await api.PUT("/publish/auto/rules", { body: body as unknown as Body })) as unknown as AutoRule,
    onSuccess: done,
  });
}

export function useRetryAuto() {
  const done = useInvalidate();
  return useMutation({
    mutationFn: async (service_key: string) => unwrap(await api.POST("/publish/auto/retry", { body: { service_key } as Body })),
    onSuccess: done,
  });
}
