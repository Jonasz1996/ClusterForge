"use client";

import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api, unwrap } from "./api/client";
import type { components } from "./api/schema";

export type NodeAction = components["schemas"]["NodeAction"];
export type NodeActionInput = components["schemas"]["NodeActionInput"];

export function useNodeAction() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({ nodeId, body }: { nodeId: string; body: NodeActionInput }) =>
      unwrap(await api.POST("/nodes/{nodeId}/actions", { params: { path: { nodeId } }, body })),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ["jobs"] });
      void qc.invalidateQueries({ queryKey: ["nodes"] });
    },
  });
}
