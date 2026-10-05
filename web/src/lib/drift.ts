"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, unwrap } from "./api/client";
import type { components } from "./api/schema";

export type DriftReport = components["schemas"]["DriftReport"];
export type DriftNode = components["schemas"]["DriftNode"];
export type DriftFinding = components["schemas"]["DriftFinding"];
export type ClusterDriftSummary = components["schemas"]["ClusterDriftSummary"];
export type DriftSource = components["schemas"]["DriftSource"];
export type BaselineItems = components["schemas"]["BaselineItems"];
export type BaselineInput = components["schemas"]["BaselineInput"];
export type BaselineResult = components["schemas"]["BaselineResult"];
export type BaselineItem = components["schemas"]["BaselineItem"];
export type DriftIgnore = components["schemas"]["DriftIgnore"];
export type DriftIgnoreInput = components["schemas"]["DriftIgnoreInput"];

type Tone = "slate" | "green" | "amber" | "red" | "blue";

export const driftStatuses: Record<DriftNode["status"], { label: string; tone: Tone }> = {
  in_sync: { label: "In orde", tone: "green" },
  drift: { label: "Drift", tone: "amber" },
  error: { label: "Fout", tone: "red" },
  none: { label: "Geen verwachte staat", tone: "slate" },
  unknown: { label: "Nog niet gecontroleerd", tone: "slate" },
};

const keys = {
  cluster: (id: string) => ["clusters", id, "drift"] as const,
  node: (id: string) => ["nodes", id, "drift"] as const,
  ignores: (id: string) => ["clusters", id, "drift", "ignores"] as const,
};

// De scanner loopt elke 15 minuten; een overgang komt via de live stream.
const interval = 60_000;

export function useClusterDrift(id: string) {
  return useQuery({
    queryKey: keys.cluster(id),
    queryFn: async () => unwrap(await api.GET("/clusters/{clusterId}/drift", { params: { path: { clusterId: id } } })),
    refetchInterval: interval,
  });
}

export function useNodeDrift(id: string) {
  return useQuery({
    queryKey: keys.node(id),
    queryFn: async () => unwrap(await api.GET("/nodes/{nodeId}/drift", { params: { path: { nodeId: id } } })),
    refetchInterval: interval,
  });
}

// useCheckDrift controleert een cluster of één node meteen. Het antwoord is
// het nieuwe rapport; de lijsten laden daarna opnieuw voor de badges.
export function useCheckDrift(scope: "cluster" | "node", id: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async () =>
      scope === "cluster"
        ? unwrap(await api.POST("/clusters/{clusterId}/drift/check", { params: { path: { clusterId: id } } }))
        : unwrap(await api.POST("/nodes/{nodeId}/drift/check", { params: { path: { nodeId: id } } })),
    onSuccess: (data) => {
      qc.setQueryData(scope === "cluster" ? keys.cluster(id) : keys.node(id), data);
      void qc.invalidateQueries({ queryKey: ["clusters"] });
      void qc.invalidateQueries({ queryKey: ["nodes"] });
    },
  });
}

// Na een baseline of een negeerregel kloppen het rapport, de badges in de
// lijsten en de regels niet meer; alles onder clusters en nodes laadt opnieuw.
function useRefresh() {
  const qc = useQueryClient();
  return () => {
    void qc.invalidateQueries({ queryKey: ["clusters"] });
    void qc.invalidateQueries({ queryKey: ["nodes"] });
  };
}

// useCaptureBaseline legt een baseline vast, of met preview alleen wat er
// vastgelegd zou worden.
export function useCaptureBaseline(clusterId: string) {
  const refresh = useRefresh();
  return useMutation({
    mutationFn: async (body: BaselineInput) =>
      unwrap(await api.POST("/clusters/{clusterId}/drift/baseline", { params: { path: { clusterId } }, body })),
    onSuccess: (_, body) => {
      if (!body.preview) refresh();
    },
  });
}

export function useDriftIgnores(clusterId: string, enabled = true) {
  return useQuery({
    queryKey: keys.ignores(clusterId),
    queryFn: async () =>
      unwrap(await api.GET("/clusters/{clusterId}/drift/ignores", { params: { path: { clusterId } } })).items,
    enabled,
  });
}

export function useCreateDriftIgnore(clusterId: string) {
  const refresh = useRefresh();
  return useMutation({
    mutationFn: async (body: DriftIgnoreInput) =>
      unwrap(await api.POST("/clusters/{clusterId}/drift/ignores", { params: { path: { clusterId } }, body })),
    onSuccess: refresh,
  });
}

export function useDeleteDriftIgnore() {
  const refresh = useRefresh();
  return useMutation({
    mutationFn: async (id: string) => unwrap(await api.DELETE("/drift-ignores/{ignoreId}", { params: { path: { ignoreId: id } } })),
    onSuccess: refresh,
  });
}
