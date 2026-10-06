"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, unwrap } from "./api/client";
import type { components } from "./api/schema";

export type GitRepoStatus = components["schemas"]["GitRepoStatus"];
export type GitRepo = components["schemas"]["GitRepo"];
export type GitRepoInput = components["schemas"]["GitRepoInput"];
export type GitCommit = components["schemas"]["GitCommit"];
export type GitFile = components["schemas"]["GitFile"];
export type GitFileState = components["schemas"]["GitFileState"];
export type GitChange = components["schemas"]["GitChange"];
export type GitChangeDetail = components["schemas"]["GitChangeDetail"];
export type GitChangeStatus = components["schemas"]["GitChangeStatus"];
export type GitPlan = components["schemas"]["GitPlan"];
export type GitDecision = components["schemas"]["GitDecision"];

type Tone = "slate" | "green" | "amber" | "red" | "blue";

export const fileStates: Record<GitFileState, { label: string; tone: Tone }> = {
  in_sync: { label: "In sync", tone: "green" },
  pending: { label: "Wijziging wacht", tone: "blue" },
  applying: { label: "Wordt toegepast", tone: "blue" },
  not_applied: { label: "Niet toegepast", tone: "red" },
  rejected: { label: "Afgewezen", tone: "slate" },
  invalid: { label: "Ongeldig", tone: "red" },
  new: { label: "Nieuw", tone: "blue" },
  unlinked: { label: "Niet gekoppeld", tone: "slate" },
  missing: { label: "Ontbreekt", tone: "amber" },
};

export const changeStatuses: Record<GitChangeStatus, { label: string; tone: Tone }> = {
  pending: { label: "Wacht op goedkeuring", tone: "blue" },
  applying: { label: "Wordt toegepast", tone: "blue" },
  applied: { label: "Toegepast", tone: "green" },
  failed: { label: "Mislukt", tone: "red" },
  rejected: { label: "Afgewezen", tone: "slate" },
  superseded: { label: "Vervangen", tone: "slate" },
};

// shortSha is de korte vorm van een commit, zoals GitHub hem toont.
export const shortSha = (sha: string) => sha.slice(0, 7);

// commitTitle is de eerste regel van het commitbericht.
export const commitTitle = (c: GitCommit) => c.message.split("\n")[0];

// De server leest elke minuut; de schermen volgen sneller zodat een sync
// via de knop meteen zichtbaar is. De live-updates verversen ook.
const refetch = 15_000;

export function useGitRepo() {
  return useQuery({
    queryKey: ["gitops", "repo"],
    queryFn: async () => unwrap(await api.GET("/gitops/repo")),
    refetchInterval: refetch,
  });
}

export function useGitFiles(enabled = true) {
  return useQuery({
    queryKey: ["gitops", "files"],
    queryFn: async () => unwrap(await api.GET("/gitops/files")).items,
    refetchInterval: refetch,
    enabled,
  });
}

export function useGitChanges(filter: { status?: GitChangeStatus; cluster_id?: string } = {}, enabled = true) {
  return useQuery({
    queryKey: ["gitops", "changes", filter],
    queryFn: async () => unwrap(await api.GET("/gitops/changes", { params: { query: filter } })).items,
    refetchInterval: refetch,
    enabled,
  });
}

export function useGitChange(id: string) {
  return useQuery({
    queryKey: ["gitops", "change", id],
    queryFn: async () => unwrap(await api.GET("/gitops/changes/{changeId}", { params: { path: { changeId: id } } })),
    enabled: id !== "",
  });
}

function useInvalidate() {
  const qc = useQueryClient();
  return () => {
    void qc.invalidateQueries({ queryKey: ["gitops"] });
    void qc.invalidateQueries({ queryKey: ["clusters"] });
    void qc.invalidateQueries({ queryKey: ["audit"] });
  };
}

export function useSaveGitRepo() {
  const invalidate = useInvalidate();
  return useMutation({
    mutationFn: async (body: GitRepoInput) => unwrap(await api.PUT("/gitops/repo", { body })),
    onSuccess: invalidate,
  });
}

export function useProbeGitRepo() {
  return useMutation({
    mutationFn: async (body: GitRepoInput) => unwrap(await api.POST("/gitops/repo/probe", { body })),
  });
}

export function useDeleteGitRepo() {
  const invalidate = useInvalidate();
  return useMutation({
    mutationFn: async () => unwrap(await api.DELETE("/gitops/repo")),
    onSuccess: invalidate,
  });
}

export function useSyncGit() {
  const invalidate = useInvalidate();
  return useMutation({
    mutationFn: async () => unwrap(await api.POST("/gitops/sync")),
    // De lus leest op de achtergrond; even later staat de uitkomst er.
    onSuccess: () => {
      invalidate();
      setTimeout(invalidate, 1500);
    },
  });
}

export function useLinkCluster() {
  const invalidate = useInvalidate();
  return useMutation({
    mutationFn: async (clusterId: string) => unwrap(await api.POST("/clusters/{clusterId}/git/link", { params: { path: { clusterId } } })),
    onSuccess: invalidate,
  });
}

export function useUnlinkCluster() {
  const invalidate = useInvalidate();
  return useMutation({
    mutationFn: async (clusterId: string) => unwrap(await api.POST("/clusters/{clusterId}/git/unlink", { params: { path: { clusterId } } })),
    onSuccess: invalidate,
  });
}

// useApproveGitChange keurt een wachtende wijziging goed; op prod met de slug.
export function useApproveGitChange(changeId: string) {
  const invalidate = useInvalidate();
  return useMutation({
    mutationFn: async (confirm?: string) =>
      unwrap(await api.POST("/gitops/changes/{changeId}/approve", { params: { path: { changeId } }, body: { confirm } })),
    onSuccess: invalidate,
  });
}

export function useRejectGitChange(changeId: string) {
  const invalidate = useInvalidate();
  return useMutation({
    mutationFn: async (reason: string) => unwrap(await api.POST("/gitops/changes/{changeId}/reject", { params: { path: { changeId } }, body: { reason } })),
    onSuccess: invalidate,
  });
}

// useReapplyCluster past de huidige revisie opnieuw toe na een mislukte toepassing.
export function useReapplyCluster(clusterId: string) {
  const invalidate = useInvalidate();
  return useMutation({
    mutationFn: async (confirm?: string) =>
      unwrap(await api.POST("/clusters/{clusterId}/git/reapply", { params: { path: { clusterId } }, body: { confirm } })),
    onSuccess: invalidate,
  });
}

// exportUrl is de download van cluster.yaml; de browser stuurt de cookie mee.
export const exportUrl = (clusterId: string) => `/api/v1/clusters/${clusterId}/git/export`;
