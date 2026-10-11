"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, unwrap } from "./api/client";
import type { components } from "./api/schema";
import { list } from "./failover";

export type DisasterScenario = components["schemas"]["DisasterScenario"];
export type DisasterScenarioInput = components["schemas"]["DisasterScenarioInput"];
export type DisasterOptions = components["schemas"]["DisasterOptions"];
export type DisasterKind = components["schemas"]["DisasterKind"];
export type DisasterPreview = components["schemas"]["DisasterPreview"];
export type DisasterPrediction = components["schemas"]["DisasterPrediction"];
export type DisasterClusterPlan = components["schemas"]["DisasterClusterPlan"];
export type DisasterClusterResult = components["schemas"]["DisasterClusterResult"];
export type DisasterReport = components["schemas"]["DisasterReport"];
export type DisasterTarget = components["schemas"]["DisasterTarget"];
export type DisasterStep = components["schemas"]["DisasterStep"];
export type DisasterStepAction = components["schemas"]["DisasterStepAction"];
export type DisasterStepOn = components["schemas"]["DisasterStepOn"];
export type DisasterPlannedStep = components["schemas"]["DisasterPlannedStep"];
export type DisasterNodeOption = components["schemas"]["DisasterNodeOption"];

type Tone = "slate" | "green" | "amber" | "red" | "blue";

export const statusLabels: Record<DisasterPrediction["status"], { label: string; tone: Tone }> = {
  healthy: { label: "gezond", tone: "green" },
  degraded: { label: "verminderd", tone: "amber" },
  down: { label: "down", tone: "red" },
  split_brain: { label: "split-brain", tone: "red" },
  unknown: { label: "onbekend", tone: "slate" },
};

export const matchLabels: Record<DisasterClusterResult["match"], { label: string; tone: Tone }> = {
  as_predicted: { label: "Zoals voorspeld", tone: "green" },
  milder: { label: "Milder", tone: "amber" },
  stayed_healthy: { label: "Bleef gezond", tone: "amber" },
  unexpected: { label: "Erger dan voorspeld", tone: "red" },
  not_recovered: { label: "Niet hersteld", tone: "red" },
  unknown: { label: "Nog niet gemeten", tone: "slate" },
};

export const roleLabels: Record<DisasterClusterPlan["role"], string> = {
  target: "valt uit",
  dependent: "hangt ervan af",
};

// onLabels zeggen op welke nodes een stap van een reeks werkt.
export const onLabels: Record<DisasterStepOn, string> = {
  owner: "de VIP-houder",
  others: "de andere nodes",
  all: "alle nodes",
  node: "één node",
};

// faultMaxSeconds is de harde bovengrens van een storing in de agent;
// cutOffMaxSeconds die van link_down op de interface naar ClusterForge.
export const faultMaxSeconds = 600;
export const cutOffMaxSeconds = 120;

const keys = {
  scenarios: (clusterId: string) => ["clusters", clusterId, "disaster"] as const,
  preview: (id: string) => ["disaster-scenarios", id, "preview"] as const,
};

export function useDisasterScenarios(clusterId: string) {
  return useQuery({
    queryKey: keys.scenarios(clusterId),
    queryFn: async () => unwrap(await api.GET("/clusters/{clusterId}/disaster-scenarios", { params: { path: { clusterId } } })),
    // Zolang een rampoefening loopt, verandert het laatste resultaat snel.
    refetchInterval: (q) => (q.state.data?.items.some((s) => s.last_run && !s.last_run.result) ? 2_000 : 60_000),
  });
}

// useDisasterPreview doet de voorcontrole en de voorspelling, zonder iets
// te veranderen. Ze is altijd vers: de toestand verandert snel.
export function useDisasterPreview(id: string, enabled: boolean) {
  return useQuery({
    queryKey: keys.preview(id),
    queryFn: async () =>
      unwrap(await api.GET("/disaster-scenarios/{scenarioId}/preview", { params: { path: { scenarioId: id } } })),
    enabled,
    staleTime: 0,
    gcTime: 0,
  });
}

function useRefresh() {
  const qc = useQueryClient();
  return () => {
    void qc.invalidateQueries({ queryKey: ["clusters"] });
    void qc.invalidateQueries({ queryKey: ["test-runs"] });
    void qc.invalidateQueries({ queryKey: ["jobs"] });
  };
}

export function useCreateDisasterScenario(clusterId: string) {
  const refresh = useRefresh();
  return useMutation({
    mutationFn: async (body: DisasterScenarioInput) =>
      unwrap(await api.POST("/clusters/{clusterId}/disaster-scenarios", { params: { path: { clusterId } }, body })),
    onSuccess: refresh,
  });
}

export function useUpdateDisasterScenario() {
  const refresh = useRefresh();
  return useMutation({
    mutationFn: async ({ id, body }: { id: string; body: DisasterScenarioInput }) =>
      unwrap(await api.PUT("/disaster-scenarios/{scenarioId}", { params: { path: { scenarioId: id } }, body })),
    onSuccess: refresh,
  });
}

export function useDeleteDisasterScenario() {
  const refresh = useRefresh();
  return useMutation({
    mutationFn: async (id: string) =>
      unwrap(await api.DELETE("/disaster-scenarios/{scenarioId}", { params: { path: { scenarioId: id } } })),
    onSuccess: refresh,
  });
}

// useRunDisaster geeft de nieuwe run terug; een mislukte voorcontrole komt
// als ApiError met code precheck_failed en de controles.
export function useRunDisaster() {
  const refresh = useRefresh();
  return useMutation({
    mutationFn: async ({ id, confirm }: { id: string; confirm: string[] }) =>
      unwrap(await api.POST("/disaster-scenarios/{scenarioId}/run", { params: { path: { scenarioId: id } }, body: { confirm } })),
    onSuccess: refresh,
  });
}

// targetNames zijn de nodes die de storing krijgen, met de VM erbij.
export function targetNames(targets: DisasterTarget[]) {
  return targets.map((t) => (t.vmid ? `${t.hostname} (VM ${t.vmid})` : t.hostname));
}

// stillDown zegt wat er mogelijk nog uit staat als het herstel niet lukte;
// hetzelfde als de server.
export function stillDown(d: Pick<DisasterReport, "kind" | "unit" | "targets" | "steps"> | null | undefined) {
  const names = list((d?.targets ?? []).map((t) => t.hostname));
  if (d?.kind === "host_down") return `de VM's van ${names} staan mogelijk nog uit`;
  if (d?.kind === "sequence") {
    const units = d.steps
      .filter((s) => s.action === "service_stop")
      .flatMap((s) => s.nodes.map((n) => `${s.unit} op ${n.hostname}`));
    const msg = `storingen van de reeks staan mogelijk nog aan op ${names} (de agent heft ze uiterlijk na ${faultMaxSeconds} s zelf op)`;
    return units.length ? `${msg} en ${list([...new Set(units)])} staat mogelijk nog stil` : msg;
  }
  return `${d?.unit || "de dienst"} staat mogelijk nog uit op ${names}`;
}

// expectText beschrijft wat een rampoefening verwacht.
export function expectText(s: { fault_seconds: number; recovery_seconds: number }) {
  return `Storing ${duration(s.fault_seconds)}, daarna binnen ${duration(s.recovery_seconds)} weer gezond`;
}

// duration toont seconden als "90 s" of "10 min".
export function duration(secs: number) {
  if (secs < 120 || secs % 60 !== 0) return `${secs} s`;
  return `${secs / 60} min`;
}
