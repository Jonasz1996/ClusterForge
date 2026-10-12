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
export type ComponentRecoverPlan = components["schemas"]["ComponentRecoverPlan"];
export type ComponentRecoverNode = components["schemas"]["ComponentRecoverNode"];

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

// recovering is true zolang een taak de database opnieuw start.
export function recovering(c: ClusterComponent) {
  return c.recovery !== null && (c.recovery.status === "queued" || c.recovery.status === "running");
}

// Zolang een taak de database toepast of opnieuw start, of zolang ze weg
// is, volgt het scherm haar.
export function useClusterComponents(clusterId: string) {
  return useQuery({
    queryKey: ["clusters", clusterId, "components"],
    queryFn: async () => unwrap(await api.GET("/clusters/{clusterId}/components", { params: { path: { clusterId } } })).items,
    enabled: clusterId !== "",
    refetchInterval: (q) =>
      q.state.data?.some((c) => c.state === "installing" || c.state === "applying" || recovering(c) || c.down_since !== null)
        ? 3_000
        : 30_000,
  });
}

// Wat de taak met een node doet bij opnieuw starten.
export const recoverAction: Record<string, string> = {
  begin: "begint het cluster",
  join: "sluit aan",
  none: "doet al mee",
  skip: "doet niet mee",
  later: "na het lezen van de positie",
};

export const positionFrom: Record<string, string> = {
  running: "draait",
  grastate: "grastate.dat",
  recovery: "galera_recovery",
};

export const sinceFmt = new Intl.DateTimeFormat("nl-BE", {
  day: "numeric",
  month: "numeric",
  hour: "2-digit",
  minute: "2-digit",
});

// useRecoverPlan leest wat opnieuw starten zou doen; er verandert niets,
// dus het mag bij elke opening opnieuw.
export function useRecoverPlan(clusterId: string, componentId: string) {
  return useQuery({
    queryKey: ["clusters", clusterId, "components", componentId, "recover-plan"],
    queryFn: async () =>
      unwrap(
        await api.POST("/clusters/{clusterId}/components/{componentId}/recover/plan", {
          params: { path: { clusterId, componentId } },
        }),
      ),
    gcTime: 0,
    refetchOnWindowFocus: false,
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

export function useRecoverComponent(clusterId: string) {
  const invalidate = useInvalidate(clusterId);
  return useMutation({
    mutationFn: async ({ id, hash, confirm }: { id: string; hash: string; confirm?: string }) =>
      unwrap(
        await api.POST("/clusters/{clusterId}/components/{componentId}/recover", {
          params: { path: { clusterId, componentId: id } },
          body: { hash, confirm },
        }),
      ),
    onSuccess: invalidate,
  });
}

export function useSetAutoRecover(clusterId: string) {
  const invalidate = useInvalidate(clusterId);
  return useMutation({
    mutationFn: async ({ id, on }: { id: string; on: boolean }) =>
      unwrap(
        await api.PATCH("/clusters/{clusterId}/components/{componentId}", {
          params: { path: { clusterId, componentId: id } },
          body: { auto_recover: on },
        }),
      ),
    onSuccess: invalidate,
  });
}
