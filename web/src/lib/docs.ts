"use client";

import { keepPreviousData, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, unwrap } from "./api/client";
import type { components } from "./api/schema";

export type DocIndex = components["schemas"]["DocIndex"];
export type DocMeta = components["schemas"]["DocMeta"];
export type DocClusterIndex = components["schemas"]["DocClusterIndex"];
export type DocSummaryMeta = components["schemas"]["DocSummaryMeta"];
export type Doc = components["schemas"]["Doc"];
export type DocVersion = components["schemas"]["DocVersion"];
export type DocsTarget = components["schemas"]["DocsTarget"];
export type DocsTargetInput = components["schemas"]["DocsTargetInput"];
export type DocsPublishResult = components["schemas"]["DocsPublishResult"];

export type DocScope = "overview" | "cluster";

// DocRef wijst een document aan; cluster is leeg bij het overzicht.
export type DocRef = { scope: DocScope; kind: string; cluster?: string };

export const downloadUrl = "/api/v1/docs/download";

// kindLabels zijn de hoofdstukken in hun volgorde.
export const kindLabels: Record<string, string> = {
  overzicht: "Overzicht",
  architectuur: "Architectuur",
  kubernetes: "Kubernetes",
  database: "Database",
  netwerk: "Netwerk",
  configuratie: "Configuratie",
  herstel: "Herstel",
  runbooks: "Runbooks",
};

const keys = {
  index: ["docs", "index"] as const,
  doc: (r: DocRef, version?: number) => ["docs", "doc", r.scope, r.kind, r.cluster ?? "", version ?? "latest"] as const,
  history: (r: DocRef) => ["docs", "history", r.scope, r.kind, r.cluster ?? ""] as const,
  diff: (r: DocRef, from: number, to: number) => ["docs", "diff", r.scope, r.kind, r.cluster ?? "", from, to] as const,
};

const path = (r: DocRef) => ({ scope: r.scope, kind: r.kind });

export function useDocIndex() {
  return useQuery({ queryKey: keys.index, queryFn: async () => unwrap(await api.GET("/docs")) });
}

export function useDoc(r: DocRef | null, version?: number) {
  return useQuery({
    queryKey: r ? keys.doc(r, version) : ["docs", "doc", "none"],
    enabled: !!r,
    placeholderData: keepPreviousData,
    queryFn: async () =>
      unwrap(await api.GET("/docs/{scope}/{kind}", { params: { path: path(r!), query: { cluster: r!.cluster, version } } })),
  });
}

export function useDocHistory(r: DocRef | null, enabled: boolean) {
  return useQuery({
    queryKey: r ? keys.history(r) : ["docs", "history", "none"],
    enabled: !!r && enabled,
    queryFn: async () =>
      unwrap(await api.GET("/docs/{scope}/{kind}/history", { params: { path: path(r!), query: { cluster: r!.cluster } } })),
  });
}

export function useDocDiff(r: DocRef | null, from?: number, to?: number) {
  return useQuery({
    queryKey: r && from && to ? keys.diff(r, from, to) : ["docs", "diff", "none"],
    enabled: !!r && !!from && !!to,
    queryFn: async () =>
      unwrap(
        await api.GET("/docs/{scope}/{kind}/diff", {
          params: { path: path(r!), query: { cluster: r!.cluster, from: from!, to: to! } },
        }),
      ),
  });
}

export function useGenerateDocs() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async () => unwrap(await api.POST("/docs/generate")),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["docs"] }),
  });
}

export function useDocsTarget() {
  return useQuery({
    queryKey: ["docs", "target"],
    queryFn: async () => unwrap(await api.GET("/docs/target")),
    // Een publicatie loopt op de achtergrond; zo verschijnt de commit vanzelf.
    refetchInterval: 15_000,
  });
}

export function useSaveDocsTarget() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (body: DocsTargetInput) => unwrap(await api.PUT("/docs/target", { body })),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["docs", "target"] }),
  });
}

export function useTestDocsTarget() {
  return useMutation({
    mutationFn: async (body: DocsTargetInput) => unwrap(await api.POST("/docs/target/test", { body })),
  });
}

export function useDeleteDocsTarget() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async () => unwrap(await api.DELETE("/docs/target")),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["docs", "target"] }),
  });
}

export function usePublishDocs() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async () => unwrap(await api.POST("/docs/publish")),
    // Ook na een fout: de koppeling toont dan de melding.
    onSettled: () => qc.invalidateQueries({ queryKey: ["docs", "target"] }),
  });
}

// docHref is de pagina van een document; een cluster heet bij zijn slug.
export function docHref(slug: string | null, kind: string, anchor?: string) {
  const q = slug ? `?cluster=${encodeURIComponent(slug)}&hoofdstuk=${kind}` : "";
  return "/documentatie" + q + (anchor ? "#" + anchor : "");
}

// resolveDocLink maakt van een relatieve link in een document, zoals
// netwerk.md of ../../README.md, een pad naar die pagina; slug is het
// cluster van het document waarin de link staat.
export function resolveDocLink(href: string, slug: string | null): string | null {
  const [p, anchor] = href.split("#", 2);
  const parts: string[] = slug ? ["clusters", slug] : [];
  for (const seg of p.split("/")) {
    if (seg === "" || seg === ".") continue;
    if (seg === "..") {
      if (parts.length === 0) return null;
      parts.pop();
    } else parts.push(seg);
  }
  if (parts.length === 1 && parts[0] === "README.md") return docHref(null, "overzicht", anchor);
  const m = parts.length === 3 && parts[0] === "clusters" ? /^([a-z]+)\.md$/.exec(parts[2]) : null;
  if (m && kindLabels[m[1]] && m[1] !== "overzicht") return docHref(parts[1], m[1], anchor);
  return null;
}

// formatTime schrijft alleen het uur, als 14:05.
export function formatTime(iso: string) {
  return new Date(iso).toLocaleTimeString("nl-BE", { hour: "2-digit", minute: "2-digit" });
}

// formatWhen schrijft een moment als 3 nov 2026 14:05.
export function formatWhen(iso: string) {
  return new Date(iso).toLocaleString("nl-BE", {
    day: "numeric",
    month: "short",
    year: "numeric",
    hour: "2-digit",
    minute: "2-digit",
  });
}
