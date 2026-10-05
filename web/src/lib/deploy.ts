"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, unwrap } from "./api/client";
import type { components } from "./api/schema";

export type Template = components["schemas"]["Template"];
export type TemplateParam = components["schemas"]["TemplateParam"];
export type DeployInput = components["schemas"]["DeployInput"];
export type DeployPlan = components["schemas"]["DeployPlan"];

export const paramTypes: Record<TemplateParam["type"], string> = {
  string: "tekst",
  int: "getal",
  bool: "ja/nee",
  ipv4: "IPv4-adres",
  cidr: "netwerk (CIDR)",
  size: "grootte",
  secret: "geheim",
};

export function useTemplates() {
  return useQuery({
    queryKey: ["templates"],
    queryFn: async () => unwrap(await api.GET("/templates")).items,
    staleTime: 5 * 60_000,
  });
}

export async function planDeployment(body: DeployInput) {
  return unwrap(await api.POST("/deployments/plan", { body }));
}

export function useDeploy() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (body: DeployInput) => unwrap(await api.POST("/deployments", { body })),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ["jobs"] });
      void qc.invalidateQueries({ queryKey: ["clusters"] });
      void qc.invalidateQueries({ queryKey: ["nodes"] });
    },
  });
}

export function useRetryJob() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (id: string) => unwrap(await api.POST("/jobs/{jobId}/retry", { params: { path: { jobId: id } } })),
    onSuccess: () => void qc.invalidateQueries({ queryKey: ["jobs"] }),
  });
}

// mib toont een geheugengrootte in MB als GB waar dat rond uitkomt.
export function mib(n: number) {
  return n % 1024 === 0 ? `${n / 1024} GB` : `${n} MB`;
}
