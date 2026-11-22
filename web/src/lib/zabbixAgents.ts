"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, unwrap } from "./api/client";
import type { components } from "./api/schema";

export type ZabbixAgentsOverview =
  components["schemas"]["ZabbixAgentsOverview"];
export type ZabbixAgentNode = components["schemas"]["ZabbixAgentNode"];
export type ZabbixAgentSettings = components["schemas"]["ZabbixAgentSettings"];
export type ZabbixAgentSettingsInput =
  components["schemas"]["ZabbixAgentSettingsInput"];
export type ZabbixAgentNodesInput =
  components["schemas"]["ZabbixAgentNodesInput"];

const key = ["zabbix-agents"] as const;

export function useZabbixAgents() {
  return useQuery({
    queryKey: key,
    queryFn: async () => unwrap(await api.GET("/zabbix-agents")),
    refetchInterval: (q) =>
      q.state.data?.nodes.some((n) => n.apply_running) ? 3_000 : 30_000,
  });
}

export function useSaveZabbixAgentSettings() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (body: ZabbixAgentSettingsInput) =>
      unwrap(await api.PUT("/zabbix-agents/settings", { body })),
    onSuccess: () => void qc.invalidateQueries({ queryKey: key }),
  });
}

export function useTestZabbixAgentAPI() {
  return useMutation({
    mutationFn: async () => unwrap(await api.POST("/zabbix-agents/test")),
  });
}

export function useZabbixAgentNodes() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (body: ZabbixAgentNodesInput) =>
      unwrap(await api.POST("/zabbix-agents/nodes", { body })),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: key });
      void qc.invalidateQueries({ queryKey: ["jobs"] });
    },
  });
}

type Tone = "slate" | "green" | "amber" | "red" | "blue";

// zabbixAgentState is de toestand van een node in één woord.
export function zabbixAgentState(n: ZabbixAgentNode): {
  label: string;
  tone: Tone;
} {
  if (n.apply_running) return { label: "Bezig", tone: "blue" };
  if (!n.enabled)
    return n.agent_service
      ? { label: "Niet beheerd", tone: "slate" }
      : { label: "Geen agent", tone: "slate" };
  if (!n.managed) return { label: "Niet meer beheerd", tone: "slate" };
  if (n.apply_failed_at && n.pending) return { label: "Mislukt", tone: "red" };
  if (n.pending)
    return {
      label: n.applied_at ? "Nog toe te passen" : "Nog te installeren",
      tone: "amber",
    };
  return { label: "In orde", tone: "green" };
}
