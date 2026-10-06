"use client";

import { keepPreviousData, useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { api, unwrap } from "@/lib/api/client";
import type { components } from "@/lib/api/schema";

export type AuditEntry = components["schemas"]["AuditEntry"];
export type AuditInfo = components["schemas"]["AuditInfo"];

// AuditFilter zijn de filters van het logboek, zoals ze ook in de URL staan.
export type AuditFilter = {
  from?: string;
  to?: string;
  user?: string;
  actor_type?: "user" | "agent" | "system";
  cluster?: string;
  node?: string;
  job?: string;
  category?: string;
  q?: string;
};

const clean = (f: AuditFilter) =>
  Object.fromEntries(Object.entries(f).filter(([, v]) => v !== undefined && v !== "")) as AuditFilter;

export function useAudit(filter: AuditFilter, enabled = true) {
  const f = clean(filter);
  return useInfiniteQuery({
    queryKey: ["audit", f],
    queryFn: async ({ pageParam }) =>
      unwrap(await api.GET("/audit", { params: { query: { ...f, before: pageParam ?? undefined, limit: 50 } } })),
    initialPageParam: null as number | null,
    getNextPageParam: (last) => last.next_before,
    placeholderData: keepPreviousData,
    enabled,
  });
}

// useAuditRecent geeft de laatste regels voor een kaart, zonder paginering.
export function useAuditRecent(filter: AuditFilter, limit: number, enabled = true) {
  const f = clean(filter);
  return useQuery({
    queryKey: ["audit", "recent", f, limit],
    queryFn: async () => unwrap(await api.GET("/audit", { params: { query: { ...f, limit } } })).items,
    enabled,
  });
}

export function useAuditInfo(enabled = true) {
  return useQuery({
    queryKey: ["audit", "info"],
    queryFn: async () => unwrap(await api.GET("/audit/info")),
    enabled,
  });
}

// auditQuery zet filters om naar een querystring voor een link of de export.
export function auditQuery(filter: AuditFilter) {
  return new URLSearchParams(clean(filter) as Record<string, string>).toString();
}

export const exportUrl = (filter: AuditFilter) => {
  const q = auditQuery(filter);
  return `/api/v1/audit/export${q ? `?${q}` : ""}`;
};

// who zegt wie iets deed, ook als het systeem het namens iemand deed.
export function who(e: AuditEntry) {
  if (e.actor.type === "system" && e.on_behalf_of) return `systeem namens ${e.on_behalf_of.name}`;
  return e.actor.name;
}

// subjectHref geeft de pagina van het onderwerp, of null als die er niet (meer) is.
export function subjectHref(e: AuditEntry): string | null {
  if (e.subject.deleted || !e.subject.id) return null;
  switch (e.subject.type) {
    case "cluster":
      return `/clusters/detail?id=${e.subject.id}`;
    case "node":
      return `/nodes/detail?id=${e.subject.id}`;
    case "job":
      return `/taken/detail?id=${e.subject.id}`;
    case "proxmox":
      return "/proxmox";
    case "vip":
      return e.cluster && !e.cluster.deleted ? `/clusters/detail?id=${e.cluster.id}` : null;
    case "agent":
      return e.node && !e.node.deleted ? `/nodes/detail?id=${e.node.id}` : null;
    case "failover_test":
      return e.cluster && !e.cluster.deleted ? `/clusters/detail?id=${e.cluster.id}` : null;
    case "test_run":
      return `/tests/detail?id=${e.subject.id}`;
  }
  return null;
}

export const auditTimeFmt = new Intl.DateTimeFormat("nl-BE", {
  day: "2-digit",
  month: "2-digit",
  year: "numeric",
  hour: "2-digit",
  minute: "2-digit",
});
