"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useRouter, useSearchParams } from "next/navigation";
import { useEffect } from "react";
import { api, getCsrfToken, unwrap } from "./api/client";
import type { Job } from "./proxmox";

// Fase 34: OPNsense-firewalls via de agent op elke firewall. Lezen gaat
// langs de lokale OPNsense-API (/opnsense/firewalls/{id}/api); veranderen
// altijd als taak opnsense.change: voorcontrole, herstelpunt en
// terugdraaitimer op de firewall, proeven, bevestigen of terugdraaien, en
// daarna de sync naar de backup. De types volgen internal/opnsense en
// pkg/protocol/opnsense.go.

export const OPNSENSE_PROTOCOL = 50;

export type OpnVIP = {
  interface: string;
  device?: string;
  address: string;
  vhid: number;
  state: string;
  advbase?: number;
  advskew?: number;
  mode: string;
  descr?: string;
};

export type OpnFacts = {
  version: string;
  series?: string;
  freebsd?: string;
  hostname?: string;
  carp: { demotion: number; allow: boolean; maintenance: boolean };
  vips?: OpnVIP[];
  ha: {
    pfsync_interface?: string;
    pfsync_peer?: string;
    pfsync_up?: boolean;
    sync_to?: string;
    username?: string;
    sync_items?: string[];
    disable_preempt?: boolean;
  };
  interfaces?: {
    name: string;
    device: string;
    descr?: string;
    enabled: boolean;
    up: boolean;
    addresses?: string[];
  }[];
  config: {
    time: number;
    description?: string;
    user?: string;
    backups?: number;
  };
  api: { ok: boolean; user?: string; pasted?: boolean; error?: string };
  gui?: string;
};

export type OpnFirewall = {
  node_id: string;
  hostname: string;
  address?: string;
  cluster_id?: string;
  cluster_name?: string;
  environment?: string;
  facts: OpnFacts;
  role: "master" | "backup" | "mixed" | "";
  protocol: number;
  agent_version?: string;
  online: boolean;
  heartbeat_at?: string;
  why?: string;
};

export type OpnProblem = { level: "crit" | "warn"; text: string };

export type OpnCluster = {
  key: string;
  name: string;
  cluster_id?: string;
  environment?: string;
  nodes: OpnFirewall[];
  master?: string;
  level: "ok" | "warn" | "crit";
  problems: OpnProblem[];
  vips: {
    address: string;
    interface: string;
    vhid: number;
    mode: string;
    descr?: string;
    states: Record<string, string>;
  }[];
  ha: boolean;
};

export type OpnRevision = {
  file: string;
  time: number;
  description: string;
  user: string;
  size: number;
};
export type OpnDiff = {
  from: string;
  to: string;
  diff: string;
  changed: number;
};

export type OpnCall = { path: string; body?: Record<string, unknown> };

export type OpnPlan = {
  each?: boolean;
  titles: string[];
  apply: string[] | null;
  level: "normal" | "access";
  ok: boolean;
  why?: string;
  cluster: string;
  master?: string;
  backups: string[];
  confirm?: string;
  prod: boolean;
  confirm_within: number;
};

const keys = {
  clusters: ["opnsense", "clusters"] as const,
  get: (id: string, path: string, params?: Record<string, string>) => ["opnsense", id, "api", path, params ?? {}] as const,
  ctl: (id: string, action: string) => ["opnsense", id, "ctl", action] as const,
  revisions: (id: string) => ["opnsense", id, "revisions"] as const,
};

export function useOpnClusters() {
  return useQuery({
    queryKey: keys.clusters,
    queryFn: async () =>
      unwrap(await api.GET("/opnsense/clusters")) as unknown as {
        items: OpnCluster[];
        protocol: number;
      },
    refetchInterval: 15_000,
  });
}

// useOpnClusterKey is het gekozen cluster: ?fw= in de URL, anders het
// vorige, anders het eerste.
export function useOpnClusterKey(list: OpnCluster[] | undefined): [string, (key: string) => void] {
  const params = useSearchParams();
  const router = useRouter();
  let stored = "";
  try {
    stored = typeof window !== "undefined" ? (window.localStorage.getItem("cf.opnsense") ?? "") : "";
  } catch {
    stored = "";
  }
  const fromURL = params.get("fw") ?? "";
  const ids = (list ?? []).map((c) => c.key);
  const pick = (fromURL && ids.includes(fromURL) && fromURL) || (stored && ids.includes(stored) && stored) || ids[0] || "";
  useEffect(() => {
    try {
      if (pick) window.localStorage.setItem("cf.opnsense", pick);
    } catch {
      /* geen opslag: niet erg */
    }
  }, [pick]);
  const set = (key: string) => {
    const q = new URLSearchParams(params.toString());
    q.set("fw", key);
    router.replace("?" + q.toString());
  };
  return [pick, set];
}

// readNode is de node om van te lezen: de master, anders de eerste die
// ClusterForge kan lezen.
export function readNode(c: OpnCluster): OpnFirewall | undefined {
  return c.nodes.find((n) => n.node_id === c.master && !n.why) ?? c.nodes.find((n) => !n.why);
}

export async function opnGet<T>(id: string, path: string, params?: Record<string, string>): Promise<T> {
  const res = unwrap(
    await api.GET("/opnsense/firewalls/{nodeId}/api", {
      params: {
        path: { nodeId: id },
        query: { path, params: params ? JSON.stringify(params) : undefined },
      },
    }),
  ) as unknown as { data: T };
  return res.data;
}

export function useOpnGet<T>(
  id: string | undefined,
  path: string,
  opts: {
    params?: Record<string, string>;
    enabled?: boolean;
    refetch?: number | false;
  } = {},
) {
  return useQuery({
    queryKey: keys.get(id ?? "", path, opts.params),
    queryFn: () => opnGet<T>(id!, path, opts.params),
    enabled: (opts.enabled ?? true) && !!id && !!path,
    refetchInterval: opts.refetch ?? 20_000,
    retry: false,
  });
}

// Een zoekopdracht van OPNsense geeft rows met total en rowCount.
export type OpnSearch<T> = {
  rows: T[];
  total: number;
  rowCount: number;
  current: number;
};

export function useOpnConfigctl<T>(id: string | undefined, action: string, refetch: number | false = 20_000) {
  return useQuery({
    queryKey: keys.ctl(id ?? "", action),
    queryFn: async () =>
      (
        unwrap(
          await api.GET("/opnsense/firewalls/{nodeId}/configctl", {
            params: { path: { nodeId: id! }, query: { action } },
          }),
        ) as unknown as { data: T }
      ).data,
    enabled: !!id,
    refetchInterval: refetch,
    retry: false,
  });
}

export function useOpnRevisions(id: string | undefined) {
  return useQuery({
    queryKey: keys.revisions(id ?? ""),
    queryFn: async () =>
      (
        unwrap(
          await api.GET("/opnsense/firewalls/{nodeId}/revisions", {
            params: { path: { nodeId: id! } },
          }),
        ) as unknown as { items: OpnRevision[] }
      ).items,
    enabled: !!id,
    refetchInterval: 30_000,
    retry: false,
  });
}

export async function opnDiff(id: string, from: string, to: string, other?: string): Promise<OpnDiff> {
  return unwrap(
    await api.GET("/opnsense/firewalls/{nodeId}/diff", {
      params: { path: { nodeId: id }, query: { from, to, other } },
    }),
  ) as unknown as OpnDiff;
}

export async function opnPlan(id: string, calls: OpnCall[], restore?: string): Promise<OpnPlan> {
  return unwrap(
    await api.POST("/opnsense/firewalls/{nodeId}/plan", {
      params: { path: { nodeId: id } },
      body: { calls, restore },
    }),
  ) as unknown as OpnPlan;
}

function useAfterJob() {
  const qc = useQueryClient();
  return () => {
    qc.invalidateQueries({ queryKey: ["jobs"] });
    const again = () => qc.invalidateQueries({ queryKey: ["opnsense"] });
    again();
    for (const ms of [5_000, 15_000, 40_000]) setTimeout(again, ms);
  };
}

export type OpnChangeInput = {
  calls: OpnCall[];
  // restore zet een revisie uit /conf/backup terug in plaats van calls.
  restore?: string;
  confirm_within?: number;
  confirm?: string;
  password?: string;
  totp_code?: string;
};

export function useOpnChange(id: string) {
  const after = useAfterJob();
  return useMutation({
    mutationFn: async (c: OpnChangeInput) =>
      unwrap(
        await api.POST("/opnsense/firewalls/{nodeId}/changes", {
          params: { path: { nodeId: id } },
          body: c,
        }),
      ) as unknown as Job,
    onSuccess: after,
  });
}

export function useOpnSync(id: string) {
  const after = useAfterJob();
  return useMutation({
    mutationFn: async () =>
      unwrap(
        await api.POST("/opnsense/firewalls/{nodeId}/sync", {
          params: { path: { nodeId: id } },
        }),
      ) as unknown as Job,
    onSuccess: after,
  });
}

export function useOpnKey(id: string) {
  const after = useAfterJob();
  return useMutation({
    mutationFn: async (k: { key: string; secret: string }) =>
      unwrap(
        await api.POST("/opnsense/firewalls/{nodeId}/key", {
          params: { path: { nodeId: id } },
          body: k,
        }),
      ) as unknown as Job,
    onSuccess: after,
  });
}

export const roleLabel: Record<string, string> = {
  master: "MASTER",
  backup: "BACKUP",
  mixed: "gemengd",
  "": "geen CARP",
};

export function revisionTime(t: number) {
  return new Intl.DateTimeFormat("nl-BE", {
    dateStyle: "short",
    timeStyle: "medium",
  }).format(new Date(t * 1000));
}

// --- HA en firmware (M5) -----------------------------------------------------

export type OpnConfirm = { confirm?: string; password?: string; totp_code?: string };

// authBody maakt van wat je intikte een code (6 cijfers) of een wachtwoord.
export function authBody(code: string): { password?: string; totp_code?: string } {
  if (!code) return {};
  return /^\d{6}$/.test(code) ? { totp_code: code, password: code } : { password: code };
}

// useOpnCarp: id is de firewall voor de onderhoudsmodus; bij de
// failovertest een node van het cluster (de server kiest de master).
export function useOpnCarp() {
  const after = useAfterJob();
  return useMutation({
    mutationFn: async ({ id, ...b }: { id: string; action: "maintenance_on" | "maintenance_off" | "failover_test" } & OpnConfirm) =>
      unwrap(
        await api.POST("/opnsense/firewalls/{nodeId}/carp", {
          params: { path: { nodeId: id } },
          body: b,
        }),
      ) as unknown as Job,
    onSuccess: after,
  });
}

export function useOpnFirmware(id: string) {
  const after = useAfterJob();
  return useMutation({
    mutationFn: async (b: { mode: "check" | "update" | "upgrade" } & OpnConfirm) =>
      unwrap(
        await api.POST("/opnsense/firewalls/{nodeId}/firmware", {
          params: { path: { nodeId: id } },
          body: b,
        }),
      ) as unknown as Job,
    onSuccess: after,
  });
}

export type OpnDrift = {
  master: string;
  rows: { node_id: string; hostname: string; sections: string[]; error?: string }[];
  sync_items: string[];
  own: string[];
};

export function useOpnDrift(id: string | undefined) {
  return useQuery({
    queryKey: ["opnsense", "drift", id],
    queryFn: async () =>
      unwrap(
        await api.GET("/opnsense/firewalls/{nodeId}/drift", {
          params: { path: { nodeId: id! } },
        }),
      ) as unknown as OpnDrift,
    enabled: !!id,
    refetchInterval: 120_000,
    retry: false,
  });
}

export type OpnBackup = {
  id: string;
  node_id: string;
  taken_at: string;
  reason: string;
  sha256: string;
  size: number;
  version?: string;
};

export function useOpnBackups(id: string | undefined) {
  return useQuery({
    queryKey: ["opnsense", "backups", id],
    queryFn: async () =>
      unwrap(
        await api.GET("/opnsense/firewalls/{nodeId}/backups", {
          params: { path: { nodeId: id! } },
        }),
      ) as unknown as { items: OpnBackup[]; keep: number },
    enabled: !!id,
    refetchInterval: 60_000,
  });
}

export function useOpnBackupNow(id: string) {
  const after = useAfterJob();
  return useMutation({
    mutationFn: async () =>
      unwrap(
        await api.POST("/opnsense/firewalls/{nodeId}/backups", {
          params: { path: { nodeId: id } },
        }),
      ) as unknown as Job,
    onSuccess: after,
  });
}

// downloadOpnBackup haalt de config.xml (met geheimen) en laat de browser
// hem bewaren; altijd met code of wachtwoord.
export async function downloadOpnBackup(id: string, code: string) {
  const headers: Record<string, string> = { "Content-Type": "application/json" };
  const csrf = getCsrfToken();
  if (csrf) headers["X-CSRF-Token"] = csrf;
  const res = await fetch(`/api/v1/opnsense/backups/${id}/download`, {
    method: "POST",
    credentials: "same-origin",
    headers,
    body: JSON.stringify(authBody(code)),
  });
  if (!res.ok) {
    const e = (await res.json().catch(() => null)) as { message?: string } | null;
    throw new Error(e?.message ?? `Downloaden mislukt (${res.status})`);
  }
  const name = /filename="([^"]+)"/.exec(res.headers.get("Content-Disposition") ?? "")?.[1] ?? "config.xml";
  const url = URL.createObjectURL(await res.blob());
  const a = document.createElement("a");
  a.href = url;
  a.download = name;
  a.click();
  setTimeout(() => URL.revokeObjectURL(url), 5_000);
}

// --- Verkeer uit het filterlog (fase 40) ----------------------------------------

export type OpnFlowRow = {
  action: string;
  interface: string;
  iface_name?: string;
  dir: string;
  proto: string;
  src: string;
  src_name?: string;
  dst: string;
  dst_name?: string;
  port?: number;
  rule?: string;
  rule_name?: string;
  count: number;
  first: string;
  last: string;
};

export type OpnFlows = {
  window: string;
  rows: OpnFlowRow[];
  rules: { rule: string; rule_name?: string; action: string; count: number; last: string }[];
  reads: { node_id: string; hostname: string; read_at?: string; error?: string; why?: string }[];
  blocked: number;
  passed: number;
};

export function useOpnFlows(id: string | undefined, window: "1h" | "24h" | "7d" | "30d", action: "" | "block" | "pass", q: string) {
  return useQuery({
    queryKey: ["opnsense", "flows", id, window, action, q],
    queryFn: async () =>
      unwrap(
        await api.GET("/opnsense/firewalls/{nodeId}/flows", {
          params: { path: { nodeId: id! }, query: { window, action: action || undefined, q: q || undefined } },
        }),
      ) as unknown as OpnFlows,
    enabled: !!id,
    refetchInterval: 60_000,
  });
}

// --- Automatisch beheer (fase 40 M2) ---------------------------------------

export type OpnAutoMode = "uit" | "voorbeeld" | "beheerd";

export type OpnAutoSettings = {
  aliases: OpnAutoMode;
  dns: OpnAutoMode;
  dhcp: OpnAutoMode;
  domain: string;
  ignored: string[];
};

export type OpnAutoItem = {
  key: string;
  kind: "alias" | "dns" | "dhcp";
  op: "add" | "set" | "del" | "conflict";
  title: string;
  detail?: string;
  ignored: boolean;
  manual?: boolean;
};

export type OpnAuto = {
  cluster: string;
  firewall: string;
  settings: OpnAutoSettings;
  domain: string;
  items: OpnAutoItem[];
  in_order: Record<string, number>;
  notes: string[];
  pending: number;
  last_run?: string;
  last_error?: string;
  last_job?: string;
};

export function useOpnAuto(id: string | undefined) {
  return useQuery({
    queryKey: ["opnsense", "auto", id],
    queryFn: async () =>
      unwrap(await api.GET("/opnsense/firewalls/{nodeId}/auto", { params: { path: { nodeId: id! } } })) as unknown as OpnAuto,
    enabled: !!id,
    refetchInterval: 60_000,
  });
}

export function useOpnAutoSave(id: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (b: OpnAutoSettings & OpnConfirm) =>
      unwrap(
        await api.PUT("/opnsense/firewalls/{nodeId}/auto", {
          params: { path: { nodeId: id } },
          body: b,
        }),
      ) as unknown as OpnAutoSettings,
    onSuccess: () => qc.invalidateQueries({ queryKey: ["opnsense", "auto"] }),
  });
}

export function useOpnAutoApply(id: string) {
  const after = useAfterJob();
  return useMutation({
    mutationFn: async (b: { keys: string[] } & OpnConfirm) =>
      unwrap(
        await api.POST("/opnsense/firewalls/{nodeId}/auto/apply", {
          params: { path: { nodeId: id } },
          body: b,
        }),
      ) as unknown as Job,
    onSuccess: after,
  });
}

// --- Regeladvies (fase 40 M3) ------------------------------------------------

export type OpnAdviceItem = {
  key: string;
  kind: "open" | "dup" | "shadow" | "unused" | "wan";
  op: "add" | "set" | "del" | "conflict";
  title: string;
  detail?: string;
  ignored: boolean;
  manual?: boolean;
};

export type OpnAdvice = {
  cluster: string;
  firewall: string;
  items: OpnAdviceItem[];
  notes: string[];
  since?: string;
  confirm: string;
};

export function useOpnAdvice(id: string | undefined) {
  return useQuery({
    queryKey: ["opnsense", "advice", id],
    queryFn: async () =>
      unwrap(await api.GET("/opnsense/firewalls/{nodeId}/advice", { params: { path: { nodeId: id! } } })) as unknown as OpnAdvice,
    enabled: !!id,
    refetchInterval: 5 * 60_000,
  });
}

export function useOpnAdviceApply(id: string) {
  const after = useAfterJob();
  return useMutation({
    mutationFn: async (b: { keys: string[] } & OpnConfirm) =>
      unwrap(
        await api.POST("/opnsense/firewalls/{nodeId}/advice/apply", {
          params: { path: { nodeId: id } },
          body: b,
        }),
      ) as unknown as Job,
    onSuccess: after,
  });
}

export function useOpnAdviceIgnore(id: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (b: { key: string; ignore: boolean }) =>
      unwrap(
        await api.POST("/opnsense/firewalls/{nodeId}/advice/ignore", {
          params: { path: { nodeId: id } },
          body: b,
        }),
      ),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["opnsense", "advice"] }),
  });
}
