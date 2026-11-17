"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useRouter, useSearchParams } from "next/navigation";
import { useEffect } from "react";
import { api, unwrap } from "./api/client";
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

export async function opnPlan(id: string, calls: OpnCall[]): Promise<OpnPlan> {
  return unwrap(
    await api.POST("/opnsense/firewalls/{nodeId}/plan", {
      params: { path: { nodeId: id } },
      body: { calls },
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
