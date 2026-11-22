"use client";

import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { api, unwrap } from "./api/client";
import type { components } from "./api/schema";
import { liveInterval } from "./inventory";
import { kindLabels as journalKinds } from "./journal";

export type TimelineEntry = components["schemas"]["TimelineEntry"];
export type IncidentAnalysis = components["schemas"]["IncidentAnalysis"];
export type IncidentCause = components["schemas"]["IncidentCause"];
export type IncidentFinding = components["schemas"]["IncidentFinding"];
export type TimelineClass = TimelineEntry["class"];

type Tone = "slate" | "green" | "amber" | "red" | "blue";

export const classes: Record<TimelineClass, { label: string; plural: string; tone: Tone; dot: string }> = {
  change: { label: "verandering", plural: "Veranderingen", tone: "blue", dot: "bg-brand-500" },
  symptom: { label: "symptoom", plural: "Symptomen", tone: "red", dot: "bg-red-500" },
  info: { label: "info", plural: "Info", tone: "slate", dot: "bg-slate-300 dark:bg-slate-600" },
};

export const classOrder: TimelineClass[] = ["change", "symptom", "info"];

export const levels: Record<IncidentCause["level"], { label: string; tone: Tone }> = {
  likely: { label: "Waarschijnlijk", tone: "red" },
  possible: { label: "Mogelijk", tone: "amber" },
  weak: { label: "Zwak verband", tone: "slate" },
};

// Soorten uit het logboek; die uit het journaal staan in journal.ts.
export const kindLabels: Record<string, string> = {
  ...journalKinds,
  job: "taak",
  heal: "zelfherstel",
  gitops: "Git",
  drift: "drift",
  proxmox: "Proxmox",
  test: "test",
  backup: "back-up",
  status: "status",
  incident: "incident",
  lifecycle: "nodebeheer",
  config: "configuratie",
  deps: "afhankelijkheid",
  other: "overig",
  metrics: "metrics",
  kube: "Kubernetes",
};

export const sourceLabels: Record<TimelineEntry["source"], string> = {
  log: "logboek",
  agent: "agent",
  server: "afgeleid",
  proxmox: "Proxmox",
  clusterforge: "ClusterForge",
  metrics: "metrics",
  kube: "Kubernetes",
};

export const timelinePeriods: { value: string; label: string; hours: number }[] = [
  { value: "1h", label: "1 uur", hours: 1 },
  { value: "6h", label: "6 uur", hours: 6 },
  { value: "24h", label: "24 uur", hours: 24 },
  { value: "7d", label: "7 dagen", hours: 24 * 7 },
  { value: "31d", label: "31 dagen", hours: 24 * 31 },
];

function since(hours: number) {
  // Op de minuut afgerond, zodat de vraag niet elke render verandert.
  const ms = Math.floor(Date.now() / 60000) * 60000 - hours * 3600 * 1000;
  return new Date(ms).toISOString();
}

export function useTimeline(
  kind: "node" | "cluster",
  id: string,
  opts: { hours: number; classes?: TimelineClass[]; limit?: number },
) {
  const { hours, classes: only, limit = 500 } = opts;
  const cls = only && only.length < classOrder.length ? only.join(",") : undefined;
  return useQuery({
    queryKey: [kind === "node" ? "nodes" : "clusters", id, "timeline", hours, cls, limit],
    queryFn: async () => {
      const query = { from: since(hours), class: cls, limit };
      return unwrap(
        kind === "node"
          ? await api.GET("/nodes/{nodeId}/timeline", { params: { path: { nodeId: id }, query } })
          : await api.GET("/clusters/{clusterId}/timeline", { params: { path: { clusterId: id }, query } }),
      );
    },
    enabled: id !== "" && (only === undefined || only.length > 0),
    refetchInterval: liveInterval,
    placeholderData: keepPreviousData,
  });
}

// useMetricsWindow haalt de grafieken over een vast venster, zoals dat van
// een incident.
export function useMetricsWindow(kind: "node" | "cluster", id: string, from: string, to: string) {
  return useQuery({
    queryKey: [kind === "node" ? "nodes" : "clusters", id, "metrics-window", from, to],
    queryFn: async () => {
      const query = { from, to };
      return unwrap(
        kind === "node"
          ? await api.GET("/nodes/{nodeId}/metrics", { params: { path: { nodeId: id }, query } })
          : await api.GET("/clusters/{clusterId}/metrics", { params: { path: { clusterId: id }, query } }),
      );
    },
    enabled: id !== "",
    retry: false,
  });
}

// shortSha is het begin van een commit, zoals Git het toont.
export const shortSha = (sha: string) => sha.slice(0, 7);
