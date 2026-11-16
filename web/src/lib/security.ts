"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, unwrap } from "./api/client";
import type { components } from "./api/schema";

// Beveiliging (fase 27): bekende kwetsbaarheden per node (Debian Security
// Tracker, CISA KEV) en de eigen scopes. De types volgen internal/security.

export type Severity = "kritiek" | "hoog" | "middel" | "laag" | "info";
export const severities: Severity[] = ["kritiek", "hoog", "middel", "laag", "info"];

export type SecurityFeed = {
  name: string;
  label: string;
  url: string;
  fetched_at: string | null;
  ok_at: string | null;
  error: string;
  entries: number;
};

export type SecurityNode = {
  node_id: string;
  hostname: string;
  address: string;
  cluster_id: string | null;
  cluster_name: string;
  cluster_slug: string;
  online: boolean;
  protocol: number;
  supported: boolean;
  os: string;
  codename: string;
  kernel: string;
  dpkg: boolean;
  packages: number;
  collected_at: string | null;
  error: string;
  error_at: string | null;
  checked: boolean;
  note: string;
  counts: Partial<Record<Severity, number>>;
  kev: number;
  fixable: number;
};

export type SecurityOverview = {
  protocol: number;
  feeds: SecurityFeed[];
  stats: {
    open: number;
    accepted: number;
    counts: Partial<Record<Severity, number>>;
    kev: number;
    fixable: number;
    resolved_week: number;
    nodes_checked: number;
    nodes_total: number;
    nodes_old_agent: number;
  };
  nodes: SecurityNode[];
  scopes: number;
};

export type CVEHit = {
  cve: string;
  urgency: string;
  status: string;
  fixed_version: string;
  fixable: boolean;
  kev: boolean;
  ransomware: boolean;
  description: string;
  scope: string;
  severity: Severity;
};

export type CVEDetail = {
  package: string;
  version: string;
  release: string;
  binaries: string[];
  fix_version: string;
  fixable: number;
  no_fix: number;
  kev: number;
  cves: CVEHit[];
};

export type FindingStatus = "open" | "aanvaard" | "opgelost";

export type SecurityFinding = {
  id: string;
  kind: string;
  subject: string;
  key: string;
  node_id: string | null;
  hostname: string;
  cluster_name: string;
  cluster_slug: string;
  severity: Severity;
  title: string;
  summary: string;
  fix: string;
  detail: CVEDetail | Record<string, unknown>;
  status: FindingStatus;
  first_seen: string;
  last_seen: string;
  resolved_at: string | null;
  accepted_at: string | null;
  accepted_until: string | null;
  accepted_note: string;
  accepted_by: string;
};

export type SecurityScope = {
  id: string;
  name: string;
  description: string;
  cidrs: string[];
  exclude: string[];
  public: boolean;
  public_note: string;
  addresses: number;
  created_at: string;
  updated_at: string;
};

export type FindingQuery = {
  status?: "open" | "aanvaard" | "opgelost" | "alle" | "actueel";
  node_id?: string;
  kind?: string;
};

const key = ["security"] as const;

export function useSecurity() {
  return useQuery({
    queryKey: key,
    queryFn: async () => unwrap(await api.GET("/security")) as unknown as SecurityOverview,
    refetchInterval: 30_000,
  });
}

export function useSecurityFindings(query: FindingQuery) {
  return useQuery({
    queryKey: [...key, "findings", query],
    queryFn: async () => (unwrap(await api.GET("/security/findings", { params: { query } })) as unknown as { items: SecurityFinding[] }).items,
    refetchInterval: 30_000,
  });
}

export function useSecurityScopes() {
  return useQuery({
    queryKey: [...key, "scopes"],
    queryFn: async () =>
      unwrap(await api.GET("/security/scopes")) as unknown as {
        items: SecurityScope[];
        confirm_text: string;
      },
  });
}

function useInvalidate() {
  const qc = useQueryClient();
  return () => void qc.invalidateQueries({ queryKey: key });
}

export function useRefreshSecurity() {
  const done = useInvalidate();
  return useMutation({
    mutationFn: async (body: components["schemas"]["SecurityRefreshInput"]) => unwrap(await api.POST("/security/refresh", { body })),
    onSuccess: () => {
      // De ronde loopt op de achtergrond; even later opnieuw kijken.
      setTimeout(done, 5_000);
      setTimeout(done, 20_000);
    },
  });
}

export function useAcceptFinding() {
  const done = useInvalidate();
  return useMutation({
    mutationFn: async ({ id, note, days }: { id: string; note: string; days?: number }) =>
      unwrap(
        await api.POST("/security/findings/{findingId}/accept", {
          params: { path: { findingId: id } },
          body: { note, days },
        }),
      ) as unknown as SecurityFinding,
    onSuccess: done,
  });
}

export function useReopenFinding() {
  const done = useInvalidate();
  return useMutation({
    mutationFn: async (id: string) =>
      unwrap(
        await api.POST("/security/findings/{findingId}/reopen", {
          params: { path: { findingId: id } },
        }),
      ) as unknown as SecurityFinding,
    onSuccess: done,
  });
}

export function useSaveScope() {
  const done = useInvalidate();
  return useMutation({
    mutationFn: async ({ id, body }: { id?: string; body: components["schemas"]["SecurityScopeInput"] }) =>
      unwrap(
        id
          ? await api.PUT("/security/scopes/{scopeId}", {
              params: { path: { scopeId: id } },
              body,
            })
          : await api.POST("/security/scopes", { body }),
      ) as unknown as SecurityScope,
    onSuccess: done,
  });
}

export function useDeleteScope() {
  const done = useInvalidate();
  return useMutation({
    mutationFn: async (id: string) =>
      unwrap(
        await api.DELETE("/security/scopes/{scopeId}", {
          params: { path: { scopeId: id } },
        }),
      ),
    onSuccess: done,
  });
}

export type Tone = "slate" | "green" | "amber" | "red" | "blue";

export const severityTone: Record<Severity, Tone> = {
  kritiek: "red",
  hoog: "red",
  middel: "amber",
  laag: "blue",
  info: "slate",
};

export const urgencyLabel: Record<string, string> = {
  high: "hoog",
  medium: "middel",
  low: "laag",
  "not yet assigned": "nog niet ingeschat",
  "end-of-life": "geen ondersteuning meer",
  unimportant: "onbelangrijk",
};

export function isCVEDetail(d: SecurityFinding["detail"]): d is CVEDetail {
  return typeof d === "object" && d !== null && Array.isArray((d as CVEDetail).cves);
}
