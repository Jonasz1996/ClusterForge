"use client";

import { keepPreviousData, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, unwrap } from "./api/client";
import type { components, operations } from "./api/schema";
import { liveInterval } from "./inventory";

export type DepGraph = components["schemas"]["DependencyGraph"];
export type DepGroup = components["schemas"]["DepGroup"];
export type DepService = components["schemas"]["DepService"];
export type DepEdge = components["schemas"]["DepEdge"];
export type ServiceKind = components["schemas"]["ServiceKind"];
export type ServiceStatus = components["schemas"]["ServiceStatus"];
export type ServiceImpact = components["schemas"]["ServiceImpact"];
export type ServiceSource = components["schemas"]["ServiceSource"];
export type ServiceInput = components["schemas"]["ServiceInput"];
export type ServicePatch = components["schemas"]["ServicePatch"];
export type DependencyInput = components["schemas"]["DependencyInput"];
export type Impact = components["schemas"]["Impact"];
export type ImpactItem = components["schemas"]["ImpactItem"];
export type GraphFilter = NonNullable<operations["getDependencyGraph"]["parameters"]["query"]>;

type Tone = "slate" | "green" | "amber" | "red" | "blue";

export const kinds: { value: ServiceKind; label: string }[] = [
  { value: "web", label: "Web" },
  { value: "lb", label: "Loadbalancer" },
  { value: "vip", label: "VIP" },
  { value: "database", label: "Database" },
  { value: "cache", label: "Cache" },
  { value: "queue", label: "Wachtrij" },
  { value: "storage", label: "Opslag" },
  { value: "dns", label: "DNS" },
  { value: "cron", label: "Cron" },
  { value: "container", label: "Containers" },
  { value: "app", label: "Applicatie" },
  { value: "external", label: "Extern" },
  { value: "other", label: "Overig" },
];

export const kindLabel = (k: string) => kinds.find((x) => x.value === k)?.label ?? k;

export const serviceStatuses: Record<ServiceStatus, { label: string; tone: Tone; dot: string }> = {
  healthy: { label: "Gezond", tone: "green", dot: "bg-emerald-500" },
  degraded: { label: "Verminderd", tone: "amber", dot: "bg-amber-500" },
  down: { label: "Down", tone: "red", dot: "bg-red-600" },
  unknown: { label: "Onbekend", tone: "slate", dot: "bg-slate-400" },
};

export const impacts: Record<ServiceImpact, { label: string; tone: Tone }> = {
  none: { label: "Geen", tone: "slate" },
  degraded: { label: "Verminderd door afhankelijkheid", tone: "amber" },
  down: { label: "Down door afhankelijkheid", tone: "red" },
};

export const sources: Record<ServiceSource, string> = {
  template: "template",
  manual: "handmatig",
  discovered: "ontdekt",
};

export const strengthLabel = (s: string) => (s === "soft" ? "zacht" : "hard");

const keys = {
  graph: (f: GraphFilter) => ["deps", "graph", f] as const,
  impact: (q: ImpactQuery) => ["deps", "impact", q] as const,
};

// useDepGraph leest de graaf; naast de live stream ververst hij elke 15 s,
// want een gestopte unit geeft alleen een event als de nodestatus omslaat.
export function useDepGraph(filter: GraphFilter, enabled = true) {
  return useQuery({
    queryKey: keys.graph(filter),
    queryFn: async () => unwrap(await api.GET("/dependency-graph", { params: { query: filter } })),
    refetchInterval: liveInterval,
    placeholderData: keepPreviousData,
    enabled,
  });
}

export type ImpactQuery = { service_id?: string; cluster_id?: string; node_id?: string };

export function useImpact(q: ImpactQuery | null) {
  return useQuery({
    queryKey: keys.impact(q ?? {}),
    queryFn: async () => unwrap(await api.GET("/impact", { params: { query: q ?? {} } })),
    enabled: !!q,
    refetchInterval: liveInterval,
  });
}

function useRefresh() {
  const qc = useQueryClient();
  return () => {
    void qc.invalidateQueries({ queryKey: ["deps"] });
    void qc.invalidateQueries({ queryKey: ["audit"] });
  };
}

export function useCreateService() {
  const refresh = useRefresh();
  return useMutation({
    mutationFn: async (body: ServiceInput) => unwrap(await api.POST("/services", { body })),
    onSuccess: refresh,
  });
}

export function useUpdateService() {
  const refresh = useRefresh();
  return useMutation({
    mutationFn: async ({ id, body }: { id: string; body: ServicePatch }) =>
      unwrap(await api.PATCH("/services/{serviceId}", { params: { path: { serviceId: id } }, body })),
    onSuccess: refresh,
  });
}

export function useDeleteService() {
  const refresh = useRefresh();
  return useMutation({
    mutationFn: async (id: string) => unwrap(await api.DELETE("/services/{serviceId}", { params: { path: { serviceId: id } } })),
    onSuccess: refresh,
  });
}

export function useCreateDependency() {
  const refresh = useRefresh();
  return useMutation({
    mutationFn: async (body: DependencyInput) => unwrap(await api.POST("/dependencies", { body })),
    onSuccess: refresh,
  });
}

export function useUpdateDependency() {
  const refresh = useRefresh();
  return useMutation({
    mutationFn: async ({ id, strength, note }: { id: string; strength?: "hard" | "soft"; note?: string }) =>
      unwrap(await api.PATCH("/dependencies/{dependencyId}", { params: { path: { dependencyId: id } }, body: { strength, note } })),
    onSuccess: refresh,
  });
}

export function useDeleteDependency() {
  const refresh = useRefresh();
  return useMutation({
    mutationFn: async (id: string) =>
      unwrap(await api.DELETE("/dependencies/{dependencyId}", { params: { path: { dependencyId: id } } })),
    onSuccess: refresh,
  });
}

// --- helpers voor de graaf ---

// Index maakt opzoeken in een graaf goedkoop.
export type Index = {
  groups: Map<string, DepGroup>;
  services: Map<string, DepService>;
  // providers: pijlen waarmee een dienst van andere afhangt; consumers: pijlen
  // van de diensten die van hem afhangen.
  providers: Map<string, DepEdge[]>;
  consumers: Map<string, DepEdge[]>;
};

export function indexGraph(g: DepGraph | undefined): Index {
  const idx: Index = { groups: new Map(), services: new Map(), providers: new Map(), consumers: new Map() };
  if (!g) return idx;
  for (const gr of g.groups) idx.groups.set(gr.id, gr);
  for (const s of g.services) idx.services.set(s.id, s);
  for (const e of g.edges) {
    idx.providers.set(e.from, [...(idx.providers.get(e.from) ?? []), e]);
    idx.consumers.set(e.to, [...(idx.consumers.get(e.to) ?? []), e]);
  }
  return idx;
}

// label noemt een dienst met zijn groep als die anders is dan from.
export function label(idx: Index, id: string, fromGroup?: string) {
  const s = idx.services.get(id);
  if (!s) return "onbekende dienst";
  if (s.group_id === fromGroup) return s.name;
  return `${s.name} in ${idx.groups.get(s.group_id)?.name ?? "?"}`;
}

// instancesText zegt waar een dienst draait: "op web01 en web02".
export function instancesText(s: DepService) {
  const names = s.instances.map((i) => i.hostname);
  if (names.length === 0) return "";
  if (names.length === 1) return `op ${names[0]}`;
  return `op ${names.slice(0, -1).join(", ")} en ${names[names.length - 1]}`;
}

// worst is de ergste kleur van een dienst: eigen status of doorgegeven uitval.
export function worst(s: DepService): ServiceStatus {
  if (s.status === "down" || s.impact === "down") return "down";
  if (s.status === "degraded" || s.impact === "degraded") return "degraded";
  return s.status;
}

// topology is een sleutel die alleen verandert als diensten of pijlen
// verschijnen of verdwijnen, niet bij een statuswissel: de layout wordt dan
// niet opnieuw berekend en de graaf verspringt niet.
export function topology(g: DepGraph | undefined) {
  if (!g) return "";
  return [
    g.level,
    g.groups.map((x) => x.id).sort().join(","),
    g.services.map((x) => x.id).sort().join(","),
    g.edges.map((x) => `${x.from}>${x.to}`).sort().join(","),
  ].join("|");
}

// visibleEdges laat zonder Interne afhankelijkheden alleen de pijlen tussen
// groepen zien; de ingeklapte weergave heeft alleen zulke pijlen.
export function visibleEdges(g: DepGraph, internal: boolean) {
  if (internal || g.level === "cluster") return g.edges;
  const group = new Map(g.services.map((s) => [s.id, s.group_id]));
  return g.edges.filter((e) => group.get(e.from) !== group.get(e.to));
}
