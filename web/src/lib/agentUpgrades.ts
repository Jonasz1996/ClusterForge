"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, unwrap } from "./api/client";
import type { components } from "./api/schema";

export type AgentUpgradeOverview = components["schemas"]["AgentUpgradeOverview"];
export type AgentUpgradeAgent = components["schemas"]["AgentUpgradeAgent"];
export type AgentUpgradeInput = components["schemas"]["AgentUpgradeInput"];

type Tone = "slate" | "green" | "amber" | "red" | "blue";

const key = ["agent-upgrades"] as const;

// Sneller verversen zolang er een taak loopt.
export function useAgentUpgrades() {
  return useQuery({
    queryKey: key,
    queryFn: async () => unwrap(await api.GET("/agent-upgrades")),
    refetchInterval: (q) => ((q.state.data?.running.length ?? 0) > 0 ? 3_000 : 30_000),
  });
}

export function useStartAgentUpgrades() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (body: AgentUpgradeInput) => unwrap(await api.POST("/agent-upgrades", { body })),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: key });
      void qc.invalidateQueries({ queryKey: ["jobs"] });
    },
  });
}

export function useSetAgentUpgradeAuto() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (enabled: boolean) => unwrap(await api.PUT("/agent-upgrades/auto", { body: { enabled } })),
    onSuccess: (data) => qc.setQueryData(key, data),
  });
}

export function useSetClusterAgentUpgradeAuto() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({ clusterId, enabled }: { clusterId: string; enabled: boolean }) =>
      unwrap(await api.PUT("/clusters/{clusterId}/agent-upgrade-auto", { params: { path: { clusterId } }, body: { enabled } })),
    onSuccess: (data) => qc.setQueryData(key, data),
  });
}

export const upgradeStates: Record<AgentUpgradeAgent["state"], { label: string; tone: Tone }> = {
  current: { label: "Bijgewerkt", tone: "green" },
  outdated: { label: "Verouderd", tone: "amber" },
  failed: { label: "Teruggezet", tone: "red" },
  manual: { label: "Met de hand bijwerken", tone: "amber" },
  newer: { label: "Nieuwer dan de server", tone: "slate" },
  unknown: { label: "Onbekend", tone: "slate" },
};

// upgradable: de knop werkt deze agent nu bij.
export function upgradable(a: AgentUpgradeAgent) {
  return a.online && (a.state === "outdated" || a.state === "failed");
}
