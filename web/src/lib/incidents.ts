"use client";

import { keepPreviousData, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, unwrap } from "./api/client";
import type { components } from "./api/schema";
import { liveInterval, statuses, type Status } from "./inventory";
import { objectHref, refOf } from "./kube";

export type Incident = components["schemas"]["Incident"];
export type IncidentSubject = components["schemas"]["IncidentSubject"];
export type IncidentDetail = components["schemas"]["IncidentDetail"];

export const severities: Record<Incident["severity"], { label: string; tone: "slate" | "amber" | "red" }> = {
  low: { label: "Laag", tone: "slate" },
  high: { label: "Hoog", tone: "amber" },
  critical: { label: "Kritiek", tone: "red" },
};

export const incidentPeriods: { value: string; label: string; days: number }[] = [
  { value: "7d", label: "7 dagen", days: 7 },
  { value: "30d", label: "30 dagen", days: 30 },
  { value: "90d", label: "90 dagen", days: 90 },
  { value: "365d", label: "een jaar", days: 365 },
];

export const subjectTypes: Record<IncidentSubject["type"], string> = {
  cluster: "Cluster",
  node: "Node",
  service: "Dienst",
  test_run: "Test",
  app: "App",
};

// statusLabel is een status van een onderwerp in het Nederlands; een test
// heeft fail of error.
export function statusLabel(s: string) {
  if (s === "fail") return "mislukt";
  if (s === "error") return "fout";
  return (statuses[s as Status]?.label ?? s).toLowerCase();
}

export function statusTone(s: string): "slate" | "green" | "amber" | "red" {
  if (s === "fail" || s === "error") return "red";
  return statuses[s as Status]?.tone ?? "slate";
}

// subjectHref is de pagina van een geraakt onderwerp, of null als het niet
// meer bestaat.
export function subjectHref(s: IncidentSubject): string | null {
  if (!s.present) return null;
  switch (s.type) {
    case "cluster":
      return `/clusters/detail?id=${s.id}`;
    case "node":
      return `/nodes/detail?id=${s.id}`;
    case "service":
      return s.cluster_id ? `/afhankelijkheden?cluster_id=${s.cluster_id}&dienst=${s.id}` : "/afhankelijkheden";
    case "test_run":
      return `/tests/detail?id=${s.id}`;
    case "app":
      return s.cluster_id && s.app ? objectHref(s.cluster_id, refOf(s.app.kind, s.app.namespace, s.app.name)) : null;
  }
  return null;
}

// durationSeconds is hoe lang het incident duurde, of tot nu als het nog
// open is.
export function durationSeconds(inc: Incident, now = Date.now()) {
  const end = inc.resolved_at ? new Date(inc.resolved_at).getTime() : now;
  return Math.max(0, Math.round((end - new Date(inc.started_at).getTime()) / 1000));
}

function since(days: number) {
  // Op de minuut afgerond, zodat de vraag niet elke render verandert.
  const ms = Math.floor(Date.now() / 60000) * 60000 - days * 86400 * 1000;
  return new Date(ms).toISOString();
}

export function useIncidents(opts: {
  days?: number;
  status?: "open" | "resolved";
  cluster?: string;
  includeTests?: boolean;
  limit?: number;
}) {
  const { days = 30, status, cluster, includeTests = false, limit = 100 } = opts;
  return useQuery({
    queryKey: ["incidents", "list", days, status, cluster, includeTests, limit],
    queryFn: async () =>
      unwrap(
        await api.GET("/incidents", {
          params: {
            query: {
              from: since(days),
              status,
              cluster,
              include_tests: includeTests,
              limit,
            },
          },
        }),
      ),
    refetchInterval: liveInterval,
    placeholderData: keepPreviousData,
  });
}

export function useIncident(id: string) {
  return useQuery({
    queryKey: ["incidents", id],
    queryFn: async () =>
      unwrap(
        await api.GET("/incidents/{incidentId}", {
          params: { path: { incidentId: id } },
        }),
      ),
    enabled: id !== "",
    refetchInterval: liveInterval,
  });
}

export function useSetIncidentNote(id: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (note: string) =>
      unwrap(
        await api.PUT("/incidents/{incidentId}/note", {
          params: { path: { incidentId: id } },
          body: { note },
        }),
      ),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["incidents"] }),
  });
}
