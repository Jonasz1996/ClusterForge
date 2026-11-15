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
