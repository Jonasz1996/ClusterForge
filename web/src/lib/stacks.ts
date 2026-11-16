"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, unwrap } from "./api/client";
import type { components } from "./api/schema";

// Docker-stacks (fase 21): de Compose-projecten van een cluster als gewenste
// staat. De types volgen internal/stacks.

type S = components["schemas"];
export type StackOverview = S["StackOverview"];
export type Stack = S["Stack"];
export type StackNode = S["StackNode"];
export type StackContainer = S["StackContainer"];
export type StackClusterNode = S["StackClusterNode"];
export type StackUnmanaged = S["StackUnmanaged"];
export type StackDetail = S["StackDetail"];
export type StackFile = S["StackFile"];
export type StackImportInput = S["StackImportInput"];
export type StackImportResult = S["StackImportResult"];
export type StackJobInput = S["StackJobInput"];
export type StackUpdateInput = S["StackUpdateInput"];
export type StackAction = StackJobInput["action"];
export type Placement = Stack["placement"];

const key = (clusterId: string) => ["stacks", clusterId] as const;

export function useStacks(clusterId: string) {
  return useQuery({
    queryKey: key(clusterId),
    queryFn: async () =>
      unwrap(
        await api.GET("/clusters/{clusterId}/stacks", {
          params: { path: { clusterId } },
        }),
      ) as unknown as StackOverview,
    enabled: clusterId !== "",
    refetchInterval: (q) => (q.state.data?.job_id ? 4_000 : 20_000),
  });
}

export function useStack(id: string) {
  return useQuery({
    queryKey: ["stack", id],
    queryFn: async () =>
      unwrap(
        await api.GET("/stacks/{stackId}", {
          params: { path: { stackId: id } },
        }),
      ) as unknown as StackDetail,
    enabled: id !== "",
  });
}

function useDone(clusterId: string) {
  const qc = useQueryClient();
  return () => {
    void qc.invalidateQueries({ queryKey: key(clusterId) });
    void qc.invalidateQueries({ queryKey: ["stack"] });
    void qc.invalidateQueries({ queryKey: ["jobs"] });
  };
}

export function useImportStacks(clusterId: string) {
  const done = useDone(clusterId);
  return useMutation({
    mutationFn: async (body: StackImportInput) =>
      (
        unwrap(
          await api.POST("/clusters/{clusterId}/stacks/import", {
            params: { path: { clusterId } },
            body,
          }),
        ) as unknown as {
          results: StackImportResult[];
        }
      ).results,
    onSuccess: done,
  });
}

export function useStackJob(clusterId: string) {
  const done = useDone(clusterId);
  return useMutation({
    mutationFn: async (body: StackJobInput) =>
      unwrap(
        await api.POST("/clusters/{clusterId}/stacks/jobs", {
          params: { path: { clusterId } },
          body,
        }),
      ) as unknown as { job_id: string },
    onSuccess: done,
  });
}

export function useSaveStack(clusterId: string) {
  const done = useDone(clusterId);
  return useMutation({
    mutationFn: async ({ id, body }: { id: string; body: StackUpdateInput }) =>
      unwrap(
        await api.PUT("/stacks/{stackId}", {
          params: { path: { stackId: id } },
          body,
        }),
      ) as unknown as Stack,
    onSuccess: done,
  });
}

export function useReleaseStack(clusterId: string) {
  const done = useDone(clusterId);
  return useMutation({
    mutationFn: async (id: string) =>
      unwrap(
        await api.DELETE("/stacks/{stackId}", {
          params: { path: { stackId: id } },
        }),
      ) as unknown as { job_id: string | null },
    onSuccess: done,
  });
}

export async function fetchStackLogs(
  clusterId: string,
  nodeId: string,
  container: string,
  tail = 300,
) {
  const r = unwrap(
    await api.GET("/clusters/{clusterId}/stacks/logs", {
      params: {
        path: { clusterId },
        query: { node_id: nodeId, container, tail },
      },
    }),
  ) as unknown as { logs: string };
  return r.logs;
}

export const placementLabel: Record<Placement, string> = {
  alle: "Alle nodes",
  vip: "Alleen VIP-node",
  node: "Gekozen nodes",
};

export const actionLabel: Record<StackAction, string> = {
  apply: "Toepassen",
  start: "Starten",
  stop: "Stoppen",
  restart: "Herstarten",
  update: "Bijwerken (images)",
  down: "Verwijderen (down)",
  lists: "Lijsten",
};

// containerTone kleurt de staat van een container.
export function containerTone(
  c: StackContainer,
): "green" | "amber" | "red" | "slate" {
  if (
    c.health === "unhealthy" ||
    c.state === "restarting" ||
    c.state === "dead"
  )
    return "red";
  if (c.state === "exited")
    return c.status?.startsWith("Exited (0)") ? "slate" : "red";
  if (c.state === "paused" || c.health === "starting" || c.state === "created")
    return "amber";
  if (c.state === "running") return "green";
  return "slate";
}

// stackHealth vat de containers van een stack op een node samen.
export function stackHealth(
  n: StackNode,
  desired: Stack["desired"],
): { tone: "green" | "amber" | "red" | "slate"; text: string } {
  const cs = n.containers ?? [];
  if (n.error) return { tone: "red", text: "fout" };
  if (!n.should || desired === "stopped") {
    const running = cs.filter((c) => c.state === "running").length;
    return running > 0
      ? { tone: "amber", text: `${running} draait (hoort niet)` }
      : { tone: "slate", text: "gestopt" };
  }
  if (cs.length === 0) return { tone: "red", text: "geen containers" };
  const tones = cs.map(containerTone);
  if (tones.includes("red"))
    return {
      tone: "red",
      text: `${tones.filter((t) => t === "red").length}/${cs.length} kapot`,
    };
  if (tones.includes("amber")) return { tone: "amber", text: "start op" };
  return {
    tone: "green",
    text: `${cs.filter((c) => c.state === "running").length}/${cs.length} gezond`,
  };
}
