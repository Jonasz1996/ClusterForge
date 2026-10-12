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

export type SpecHistory = components["schemas"]["SpecHistory"];
export type SpecRevision = components["schemas"]["SpecRevision"];

export const specSources: Record<SpecRevision["source"], string> = {
  ui: "webinterface",
  api: "API",
  git: "Git",
};

export function useSpecHistory(clusterId: string, enabled = true) {
  return useQuery({
    queryKey: ["clusters", clusterId, "spec-revisions"],
    queryFn: async () => unwrap(await api.GET("/clusters/{clusterId}/spec-revisions", { params: { path: { clusterId } } })),
    enabled,
  });
}

export type SpecEditForm = components["schemas"]["SpecEditForm"];
export type SpecEditInput = components["schemas"]["SpecEditInput"];
export type SpecEditPlan = components["schemas"]["SpecEditPlan"];
export type SpecApplyInput = components["schemas"]["SpecApplyInput"];

// useSpecEditForm geeft de parameters van een cluster om te wijzigen, met
// hun waarden van nu; version kiest een andere versie van de template.
export function useSpecEditForm(clusterId: string, version = "", enabled = true) {
  return useQuery({
    queryKey: ["clusters", clusterId, "spec-edit", version],
    queryFn: async () =>
      unwrap(
        await api.GET("/clusters/{clusterId}/spec/edit", {
          params: { path: { clusterId }, query: version ? { version } : {} },
        }),
      ),
    enabled: enabled && clusterId !== "",
    retry: false,
  });
}

// useSpecPlan plant een wijziging van de spec; er verandert nog niets.
export function useSpecPlan(clusterId: string, edit: SpecEditInput | null) {
  return useQuery({
    queryKey: ["spec-plan", clusterId, edit],
    queryFn: async () =>
      unwrap(await api.POST("/clusters/{clusterId}/spec/plan", { params: { path: { clusterId } }, body: edit! })),
    enabled: clusterId !== "" && edit !== null,
    retry: false,
    staleTime: Infinity,
  });
}

// useApplySpec past een geplande wijziging toe: een nieuwe revisie en, als
// er op de nodes iets verandert, een taak.
export function useApplySpec(clusterId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (body: SpecApplyInput) =>
      unwrap(await api.POST("/clusters/{clusterId}/spec", { params: { path: { clusterId } }, body })),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ["jobs"] });
      void qc.invalidateQueries({ queryKey: ["clusters"] });
      void qc.invalidateQueries({ queryKey: ["nodes"] });
      void qc.invalidateQueries({ queryKey: ["kube", clusterId] });
    },
  });
}
