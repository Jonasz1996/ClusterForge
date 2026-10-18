"use client";

import { useQuery } from "@tanstack/react-query";
import { api, unwrap } from "./api/client";
import type { components } from "./api/schema";

export type HealReport = components["schemas"]["HealReport"];
export type HealProblem = components["schemas"]["HealProblem"];
export type HealPlan = components["schemas"]["HealPlan"];
export type HealPlanNode = components["schemas"]["HealPlanNode"];

// Zelfherstel kijkt elke minuut; het scherm volgt het ritme van Databases.
export function useHeal() {
  return useQuery({
    queryKey: ["heal"],
    queryFn: async () => unwrap(await api.GET("/heal")).items,
    refetchInterval: 30_000,
  });
}
