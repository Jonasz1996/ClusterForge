"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, unwrap } from "./api/client";
import type { components } from "./api/schema";

// CrowdSec (fase 14): ClusterForge installeert en beheert CrowdSec op de nodes.

export type CrowdSecOverview = components["schemas"]["CrowdSecOverview"];
export type CrowdSecNode = components["schemas"]["CrowdSecNode"];
export type CrowdSecAlert = components["schemas"]["CrowdSecAlert"];
export type CrowdSecBan = components["schemas"]["CrowdSecBan"];
export type CrowdSecSettingsInput = components["schemas"]["CrowdSecSettingsInput"];
export type CrowdSecDecisionInput = components["schemas"]["CrowdSecDecisionInput"];
export type CrowdSecNodesInput = components["schemas"]["CrowdSecNodesInput"];
export type AlertQuery = { node_id?: string; cluster_id?: string; scenario?: string; value?: string; limit?: number };

const key = ["crowdsec"] as const;

export function useCrowdSec() {
  return useQuery({
    queryKey: key,
    queryFn: async () => unwrap(await api.GET("/crowdsec")),
    refetchInterval: (q) => (q.state.data?.nodes.some((n) => n.apply_running) ? 3_000 : 30_000),
  });
}

export function useCrowdSecAlerts(query: AlertQuery) {
  return useQuery({
    queryKey: [...key, "alerts", query],
    queryFn: async () => unwrap(await api.GET("/crowdsec/alerts", { params: { query } })).items,
    refetchInterval: 30_000,
  });
}

export function useCrowdSecBans() {
  return useQuery({
    queryKey: [...key, "bans"],
    queryFn: async () => unwrap(await api.GET("/crowdsec/decisions")).items,
    refetchInterval: 30_000,
  });
}

export function useCrowdSecNode(id: string) {
  return useQuery({
    queryKey: [...key, "node", id],
    queryFn: async () => unwrap(await api.GET("/crowdsec/nodes/{nodeId}", { params: { path: { nodeId: id } } })),
    refetchInterval: (q) => (q.state.data?.node.apply_running ? 3_000 : 30_000),
  });
}

function useInvalidate() {
  const qc = useQueryClient();
  return () => {
    void qc.invalidateQueries({ queryKey: key });
    void qc.invalidateQueries({ queryKey: ["jobs"] });
  };
}

export function useSetCrowdSecNodes() {
  const done = useInvalidate();
  return useMutation({
    mutationFn: async (body: CrowdSecNodesInput) => unwrap(await api.POST("/crowdsec/nodes", { body })),
    onSuccess: done,
  });
}

export function useConfigureCrowdSecNode() {
  const done = useInvalidate();
  return useMutation({
    mutationFn: async ({ id, body }: { id: string; body: components["schemas"]["CrowdSecNodeInput"] }) =>
      unwrap(await api.PUT("/crowdsec/nodes/{nodeId}", { params: { path: { nodeId: id } }, body })),
    onSuccess: done,
  });
}

export function useApplyCrowdSecNode() {
  const done = useInvalidate();
  return useMutation({
    mutationFn: async ({ id, remove }: { id: string; remove?: boolean }) =>
      unwrap(await api.POST("/crowdsec/nodes/{nodeId}/apply", { params: { path: { nodeId: id } }, body: { remove } })),
    onSuccess: done,
  });
}

export function useRefreshCrowdSecNode() {
  const done = useInvalidate();
  return useMutation({
    mutationFn: async (id: string) => unwrap(await api.POST("/crowdsec/nodes/{nodeId}/refresh", { params: { path: { nodeId: id } } })),
    onSuccess: done,
  });
}

export function useCrowdSecDecide() {
  const done = useInvalidate();
  return useMutation({
    mutationFn: async (body: CrowdSecDecisionInput) => unwrap(await api.POST("/crowdsec/decisions", { body })),
    onSuccess: done,
  });
}

export function useCrowdSecSettings() {
  const done = useInvalidate();
  return useMutation({
    mutationFn: async (body: CrowdSecSettingsInput) => unwrap(await api.PUT("/crowdsec/settings", { body })),
    onSuccess: done,
  });
}

export function useSetCrowdSecConsoleKey() {
  const done = useInvalidate();
  return useMutation({
    mutationFn: async (key: string) => unwrap(await api.PUT("/crowdsec/console", { body: { key } })),
    onSuccess: done,
  });
}

export function useEnrollCrowdSecConsole() {
  const done = useInvalidate();
  return useMutation({
    mutationFn: async (nodeId?: string) => unwrap(await api.POST("/crowdsec/console/enroll", { body: nodeId ? { node_id: nodeId } : {} })),
    onSuccess: done,
  });
}

/** De stand van CrowdSec op een node in een paar woorden. */
export function crowdSecState(n: CrowdSecNode): { label: string; tone: "slate" | "green" | "amber" | "red" | "blue" } {
  if (!n.supported) return { label: `agent te oud (protocol ${n.protocol})`, tone: "amber" };
  if (!n.enabled) return { label: "uit", tone: "slate" };
  if (!n.managed) return { label: "niet beheerd", tone: "slate" };
  if (n.apply_running) return { label: "wordt toegepast…", tone: "blue" };
  if (!n.online) return { label: "offline", tone: "red" };
  if (n.apply_error) return { label: "toepassen mislukt", tone: "red" };
  const st = n.status;
  if (!st) return { label: "stand ophalen…", tone: "slate" };
  if (!st.installed) return { label: "nog niet geïnstalleerd", tone: "amber" };
  if (st.active !== "active") return { label: `CrowdSec ${st.active ?? "?"}`, tone: "red" };
  if (n.bouncer && st.bouncer !== "active") return { label: "bouncer staat stil", tone: "red" };
  if (n.plan_hash && n.plan_hash !== n.applied_hash) return { label: "wijziging klaar", tone: "amber" };
  return { label: "beschermd", tone: "green" };
}

export const originLabel: Record<string, string> = { auto: "herkend", extra: "zelf aangezet", off: "uitgezet" };

const flagBase = 0x1f1e6;
/** Vlag-emoji van een landcode, zoals NL → 🇳🇱. */
export function flag(cc: string) {
  if (!/^[A-Z]{2}$/.test(cc)) return "";
  return String.fromCodePoint(flagBase + cc.charCodeAt(0) - 65, flagBase + cc.charCodeAt(1) - 65);
}

// Centrale blocklists, automatisch blokkeren en meldingen (fase 47).

export type CrowdSecList = components["schemas"]["CrowdSecList"];
export type CrowdSecListDiff = components["schemas"]["CrowdSecListDiff"];
export type CrowdSecPublicSource = components["schemas"]["CrowdSecPublicSource"];
export type CrowdSecListInput = components["schemas"]["CrowdSecListInput"];
export type CrowdSecListEntriesInput = components["schemas"]["CrowdSecListEntriesInput"];
export type CrowdSecAutoRules = components["schemas"]["CrowdSecAutoRules"];
export type CrowdSecNotifyInput = components["schemas"]["CrowdSecNotifyInput"];

export function useCrowdSecLists() {
  return useQuery({
    queryKey: [...key, "lists"],
    queryFn: async () => unwrap(await api.GET("/crowdsec/lists")),
    refetchInterval: 15_000,
  });
}

export function useCrowdSecListEntries(id: string, q: string) {
  return useQuery({
    queryKey: [...key, "lists", id, "entries", q],
    queryFn: async () => unwrap(await api.GET("/crowdsec/lists/{listId}/entries", { params: { path: { listId: id }, query: { q, limit: 500 } } })),
  });
}

export function useCrowdSecListVersions(id: string) {
  return useQuery({
    queryKey: [...key, "lists", id, "versions"],
    queryFn: async () => unwrap(await api.GET("/crowdsec/lists/{listId}/versions", { params: { path: { listId: id } } })).items,
  });
}

export function useAddCrowdSecList() {
  const done = useInvalidate();
  return useMutation({
    mutationFn: async (body: CrowdSecListInput) => unwrap(await api.POST("/crowdsec/lists", { body })),
    onSuccess: done,
  });
}

export function usePreviewCrowdSecList() {
  return useMutation({
    mutationFn: async (body: CrowdSecListInput) => unwrap(await api.POST("/crowdsec/lists/preview", { body })),
  });
}

export function useUpdateCrowdSecList() {
  const done = useInvalidate();
  return useMutation({
    mutationFn: async ({ id, body }: { id: string; body: CrowdSecListInput }) =>
      unwrap(await api.PUT("/crowdsec/lists/{listId}", { params: { path: { listId: id } }, body })),
    onSuccess: done,
  });
}

export function useDeleteCrowdSecList() {
  const done = useInvalidate();
  return useMutation({
    mutationFn: async (id: string) => unwrap(await api.DELETE("/crowdsec/lists/{listId}", { params: { path: { listId: id } } })),
    onSuccess: done,
  });
}

export function useChangeCrowdSecListEntries() {
  const done = useInvalidate();
  return useMutation({
    mutationFn: async ({ id, body }: { id: string; body: CrowdSecListEntriesInput }) =>
      unwrap(await api.POST("/crowdsec/lists/{listId}/entries", { params: { path: { listId: id } }, body })),
    onSuccess: (d) => {
      if (d.applied) done();
    },
  });
}

export function useRefreshCrowdSecList() {
  const done = useInvalidate();
  return useMutation({
    mutationFn: async (id: string) => unwrap(await api.POST("/crowdsec/lists/{listId}/refresh", { params: { path: { listId: id } } })),
    onSuccess: done,
  });
}

export function useRollbackCrowdSecList() {
  const done = useInvalidate();
  return useMutation({
    mutationFn: async ({ id, version, dryRun }: { id: string; version: number; dryRun: boolean }) =>
      unwrap(await api.POST("/crowdsec/lists/{listId}/rollback", { params: { path: { listId: id } }, body: { version_id: version, dry_run: dryRun } })),
    onSuccess: (d) => {
      if (d.applied) done();
    },
  });
}

export function useCrowdSecAutoRules() {
  const done = useInvalidate();
  return useMutation({
    mutationFn: async (body: CrowdSecAutoRules) => unwrap(await api.PUT("/crowdsec/auto-rules", { body })),
    onSuccess: done,
  });
}

export function useCrowdSecNotify() {
  const done = useInvalidate();
  return useMutation({
    mutationFn: async (body: CrowdSecNotifyInput) => unwrap(await api.PUT("/crowdsec/notify", { body })),
    onSuccess: done,
  });
}

export function useTestCrowdSecNotify() {
  return useMutation({
    mutationFn: async () => unwrap(await api.POST("/crowdsec/notify/test")).results,
  });
}

export function useCrowdSecConsoleOff() {
  const done = useInvalidate();
  return useMutation({
    mutationFn: async (on: boolean) => unwrap(await api.PUT("/crowdsec/console/off", { body: { on } })),
    onSuccess: done,
  });
}

/** Minuten leesbaar: 60 → 1 uur, 1440 → 1 dag. */
export function everyLabel(m: number) {
  if (m % 1440 === 0) return m === 1440 ? "elke dag" : `elke ${m / 1440} dagen`;
  if (m % 60 === 0) return m === 60 ? "elk uur" : `elke ${m / 60} uur`;
  return `elke ${m} minuten`;
}
