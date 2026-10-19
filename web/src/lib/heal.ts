"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, unwrap } from "./api/client";
import type { components } from "./api/schema";

export type HealReport = components["schemas"]["HealReport"];
export type HealProblem = components["schemas"]["HealProblem"];
export type HealPlan = components["schemas"]["HealPlan"];
export type HealPlanNode = components["schemas"]["HealPlanNode"];
export type HealRun = components["schemas"]["HealRun"];
export type HealAuto = components["schemas"]["HealAuto"];

// Zelfherstel kijkt elke minuut; het scherm volgt het ritme van Databases,
// en sneller zolang een herstel loopt.
export function useHeal() {
  return useQuery({
    queryKey: ["heal"],
    queryFn: async () => unwrap(await api.GET("/heal")).items,
    refetchInterval: (q) => (q.state.data?.some((r) => healRunning(r.last_run)) ? 5_000 : 30_000),
  });
}

export function healRunning(r?: HealRun | null) {
  return r?.status === "queued" || r?.status === "running";
}

export function useRunHeal() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (body: { key: string; hash: string; confirm?: string }) => unwrap(await api.POST("/heal/run", { body })),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ["heal"] });
      void qc.invalidateQueries({ queryKey: ["databases"] });
      void qc.invalidateQueries({ queryKey: ["jobs"] });
    },
  });
}

// useSetHealAuto zet Vanzelf herstellen van een cluster aan of uit.
export function useSetHealAuto(clusterId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (enabled: boolean) =>
      unwrap(await api.PUT("/clusters/{clusterId}/heal-auto", { params: { path: { clusterId } }, body: { enabled } })),
    onSuccess: () => void qc.invalidateQueries({ queryKey: ["heal"] }),
  });
}
