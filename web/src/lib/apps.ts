"use client";

import { useQuery } from "@tanstack/react-query";
import { api, unwrap } from "./api/client";
import type { components } from "./api/schema";

export type ClusterApps = components["schemas"]["ClusterApps"];
export type ClusterApp = components["schemas"]["ClusterApp"];
export type NodeAppStatus = components["schemas"]["NodeAppStatus"];
export type AppStorage = components["schemas"]["AppStorage"];
export type AppIssue = components["schemas"]["AppIssue"];

/** De stand van de apps van een cluster (fase 29), elke 30 s opnieuw. */
export function useClusterApps(id: string) {
  return useQuery({
    queryKey: ["cluster-apps", id],
    queryFn: async () =>
      unwrap(
        await api.GET("/clusters/{clusterId}/apps", {
          params: { path: { clusterId: id } },
        }),
      ),
    refetchInterval: 30_000,
    enabled: id !== "",
  });
}
