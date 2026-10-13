"use client";

import { keepPreviousData, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, unwrap } from "./api/client";
import type { components } from "./api/schema";

export type RiskOverview = components["schemas"]["RiskOverview"];
export type ClusterRisk = components["schemas"]["ClusterRisk"];
export type RiskCard = components["schemas"]["RiskCard"];
export type RiskCardId = components["schemas"]["RiskCardId"];
export type RiskFinding = components["schemas"]["RiskFinding"];
export type RiskPlacedFinding = components["schemas"]["RiskPlacedFinding"];
export type RiskSeverity = components["schemas"]["RiskSeverity"];
export type RiskPoint = components["schemas"]["RiskPoint"];
export type RiskAcceptanceInput = components["schemas"]["RiskAcceptanceInput"];
export type RiskComponent = components["schemas"]["RiskComponent"];
export type RiskComponentKind = components["schemas"]["RiskComponentKind"];
export type RiskWhatIf = components["schemas"]["RiskWhatIf"];
export type RiskGuestOutcome = components["schemas"]["RiskGuestOutcome"];
export type RiskClusterOutcome = components["schemas"]["RiskClusterOutcome"];
export type RiskSPOF = components["schemas"]["RiskSPOF"];
export type SelfLocation = components["schemas"]["SelfLocation"];
export type SelfGuest = components["schemas"]["SelfGuest"];

type Tone = "slate" | "green" | "amber" | "red" | "blue";

export const cardLabels: Record<RiskCardId, string> = {
  availability: "Beschikbaarheid",
  backups: "Back-ups",
  recovery: "Herstelbaarheid",
  redundancy: "Redundantie",
  configuration: "Configuratie",
  maintenance: "Onderhoud en beveiliging",
  capacity: "Capaciteit",
  kubernetes: "Kubernetes",
  database: "Database",
  platform: "Platform",
};

export const severityInfo: Record<RiskSeverity, { label: string; tone: Tone }> = {
  low: { label: "Laag", tone: "slate" },
  medium: { label: "Middel", tone: "amber" },
  high: { label: "Hoog", tone: "red" },
  critical: { label: "Kritiek", tone: "red" },
};

// gradeTone kleurt een cijfer: A en B groen, C en D oranje, E en F rood.
export function gradeTone(grade: string): Tone {
  if (grade === "A" || grade === "B") return "green";
  if (grade === "C" || grade === "D") return "amber";
  if (grade === "E" || grade === "F") return "red";
  return "slate";
}

// gain toont wat oplossen oplevert, met één decimaal zoals 2,5.
export function formatGain(n: number) {
  return `+${n.toLocaleString("nl-BE", { maximumFractionDigits: 1 })}`;
}

// kindInfo is de naam van een soort onderdeel, enkelvoud en meervoud.
export const kindInfo: Record<RiskComponentKind, { label: string; plural: string }> = {
  host: { label: "Proxmox-host", plural: "Proxmox-hosts" },
  storage: { label: "Storage", plural: "Storage" },
  bridge: { label: "Bridge", plural: "Bridges" },
  cluster: { label: "Cluster", plural: "Clusters" },
  node: { label: "Node", plural: "Nodes" },
  service: { label: "Dienst", plural: "Diensten" },
};

export const kindOrder: RiskComponentKind[] = ["host", "storage", "bridge", "cluster", "node", "service"];

const keys = {
  all: ["risk"] as const,
  cluster: (id: string) => ["risk", "cluster", id] as const,
  history: (cluster?: string) => ["risk", "history", cluster ?? "fleet"] as const,
  components: ["risk", "components"] as const,
  spof: ["risk", "spof"] as const,
  whatIf: (components: string[]) => ["risk", "what-if", components] as const,
  self: ["self-location"] as const,
};

// De score rekent elke minuut; de schermen volgen dat ritme en de stroom.
const interval = 60_000;

export function useRisk() {
  return useQuery({
    queryKey: keys.all,
    queryFn: async () => unwrap(await api.GET("/risk")),
    refetchInterval: interval,
  });
}

export function useClusterRisk(id: string) {
  return useQuery({
    queryKey: keys.cluster(id),
    queryFn: async () => unwrap(await api.GET("/clusters/{clusterId}/risk", { params: { path: { clusterId: id } } })),
    refetchInterval: interval,
    enabled: id !== "",
  });
}

export function useRiskHistory(cluster?: string, days = 30) {
  return useQuery({
    queryKey: [...keys.history(cluster), days],
    queryFn: async () => unwrap(await api.GET("/risk/history", { params: { query: { cluster, days } } })),
    refetchInterval: interval,
  });
}

export function useRiskComponents() {
  return useQuery({
    queryKey: keys.components,
    queryFn: async () => unwrap(await api.GET("/risk/components")),
    refetchInterval: interval,
  });
}

export function useSPOFs() {
  return useQuery({
    queryKey: keys.spof,
    queryFn: async () => unwrap(await api.GET("/risk/spof")),
    refetchInterval: interval,
  });
}

// useWhatIf rekent uit wat er gebeurt als de gekozen onderdelen uitvallen.
// Het is een vraag, geen wijziging: ze volgt de score mee zoals de rest.
export function useWhatIf(components: string[]) {
  return useQuery({
    queryKey: keys.whatIf(components),
    queryFn: async () => unwrap(await api.POST("/risk/what-if", { body: { components } })),
    refetchInterval: interval,
    enabled: components.length > 0,
    placeholderData: keepPreviousData,
  });
}

export function useSelfLocation() {
  return useQuery({
    queryKey: keys.self,
    queryFn: async () => unwrap(await api.GET("/self-location")),
  });
}

// Waar ClusterForge draait, telt mee in de wat-als en de score.
function useSelfRefresh() {
  const qc = useQueryClient();
  return () => {
    void qc.invalidateQueries({ queryKey: keys.self });
    void qc.invalidateQueries({ queryKey: keys.all });
  };
}

export function useSetSelfLocation() {
  const refresh = useSelfRefresh();
  return useMutation({
    mutationFn: async (body: { connection_id: string; vmid: number }) => unwrap(await api.PUT("/self-location", { body })),
    onSuccess: refresh,
  });
}

export function useClearSelfLocation() {
  const refresh = useSelfRefresh();
  return useMutation({
    mutationFn: async () => unwrap(await api.DELETE("/self-location")),
    onSuccess: refresh,
  });
}

// Na accepteren of intrekken is de score al opnieuw berekend; de lijsten en
// de clusterlijst met de cijfers laden opnieuw.
function useRefresh() {
  const qc = useQueryClient();
  return () => {
    void qc.invalidateQueries({ queryKey: keys.all });
    void qc.invalidateQueries({ queryKey: ["clusters"] });
  };
}

export function useAcceptFinding() {
  const refresh = useRefresh();
  return useMutation({
    mutationFn: async (body: RiskAcceptanceInput) => unwrap(await api.POST("/risk/acceptances", { body })),
    onSuccess: refresh,
  });
}

export function useRemoveAcceptance() {
  const refresh = useRefresh();
  return useMutation({
    mutationFn: async (id: string) =>
      unwrap(await api.DELETE("/risk/acceptances/{acceptanceId}", { params: { path: { acceptanceId: id } } })),
    onSuccess: refresh,
  });
}
