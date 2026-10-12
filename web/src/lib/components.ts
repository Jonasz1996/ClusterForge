"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, unwrap } from "./api/client";
import type { components } from "./api/schema";

export type ClusterComponent = components["schemas"]["ClusterComponent"];
export type ComponentTemplate = components["schemas"]["ComponentTemplate"];
export type ComponentInput = components["schemas"]["ComponentInput"];
export type ComponentPlan = components["schemas"]["ComponentPlan"];
export type ComponentPlanNode = components["schemas"]["ComponentPlanNode"];
export type ComponentFinding = components["schemas"]["ComponentFinding"];

type Tone = "slate" | "green" | "amber" | "red" | "blue";

export const stateInfo: Record<string, { label: string; tone: Tone }> = {
  installing: { label: "Wordt geïnstalleerd", tone: "blue" },
  applying: { label: "Wordt toegepast", tone: "blue" },
  ready: { label: "Klaar", tone: "green" },
  failed: { label: "Mislukt", tone: "red" },
};

export const roleLabel: Record<string, string> = { data: "data", arbiter: "scheidsrechter" };

export function useComponentTemplates() {
  return useQuery({
    queryKey: ["component-templates"],
    queryFn: async () => unwrap(await api.GET("/component-templates")).items,
    staleTime: Infinity,
  });
}

// Zolang een taak de database toepast, volgt het scherm hem.
export function useClusterComponents(clusterId: string) {
  return useQuery({
    queryKey: ["clusters", clusterId, "components"],
    queryFn: async () => unwrap(await api.GET("/clusters/{clusterId}/components", { params: { path: { clusterId } } })).items,
    enabled: clusterId !== "",
    refetchInterval: (q) => (q.state.data?.some((c) => c.state === "installing" || c.state === "applying") ? 3_000 : 30_000),
  });
}

export function usePlanComponent(clusterId: string) {
  return useMutation({
    mutationFn: async (body: ComponentInput) =>
      unwrap(await api.POST("/clusters/{clusterId}/components/plan", { params: { path: { clusterId } }, body })),
  });
}

function useInvalidate(clusterId: string) {
  const qc = useQueryClient();
  return () => {
    void qc.invalidateQueries({ queryKey: ["clusters", clusterId] });
    void qc.invalidateQueries({ queryKey: ["jobs"] });
  };
}

export function useAddComponent(clusterId: string) {
  const invalidate = useInvalidate(clusterId);
  return useMutation({
    mutationFn: async (body: ComponentInput & { hash: string; confirm?: string }) =>
      unwrap(await api.POST("/clusters/{clusterId}/components", { params: { path: { clusterId } }, body })),
    onSuccess: invalidate,
  });
}

export function useReapplyComponent(clusterId: string) {
  const invalidate = useInvalidate(clusterId);
  return useMutation({
    mutationFn: async ({ id, confirm }: { id: string; confirm?: string }) =>
      unwrap(
        await api.POST("/clusters/{clusterId}/components/{componentId}/apply", {
          params: { path: { clusterId, componentId: id } },
          body: { confirm },
        }),
      ),
    onSuccess: invalidate,
  });
}

export function useRemoveComponent(clusterId: string) {
  const invalidate = useInvalidate(clusterId);
  return useMutation({
    mutationFn: async (id: string) =>
      unwrap(
        await api.DELETE("/clusters/{clusterId}/components/{componentId}", { params: { path: { clusterId, componentId: id } } }),
      ),
    onSuccess: invalidate,
  });
}
