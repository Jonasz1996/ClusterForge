"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, unwrap } from "./api/client";
import type { components } from "./api/schema";

export type AgentEndpointsOverview = components["schemas"]["AgentEndpointsOverview"];
export type AgentEndpointAgent = components["schemas"]["AgentEndpointAgent"];

const key = ["agent-endpoints"] as const;

// known is het adres voor agents zodra de server het gaf; zo nemen ook
// commando's die buiten een hook opgebouwd worden het interne adres.
let known = "";

// agentServerURL is het adres waarmee nodes ClusterForge bereiken: wat bij
// Instellingen staat of het interne adres (CF_NATS_ADVERTISE), anders het
// adres van deze pagina.
export function agentServerURL() {
  if (known) return known;
  return typeof window === "undefined" ? "https://clusterforge.example" : window.location.origin;
}

export function useAgentEndpoints(enabled = true) {
  return useQuery({
    queryKey: key,
    enabled,
    queryFn: async () => {
      const ov = unwrap(await api.GET("/agent-endpoints"));
      known = ov.server_url;
      return ov;
    },
    refetchInterval: (q) => ((q.state.data?.running.length ?? 0) > 0 ? 3_000 : 60_000),
  });
}

export function useSetAgentEndpoints() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (body: { server_url: string; nats_url: string }) => unwrap(await api.PUT("/agent-endpoints", { body })),
    onSuccess: (data) => {
      known = data.server_url;
      qc.setQueryData(key, data);
    },
  });
}

export function useApplyAgentEndpoints() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (body: { node_ids?: string[]; all?: boolean; include_prod?: boolean; totp_code?: string }) =>
      unwrap(await api.POST("/agent-endpoints/apply", { body })),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: key });
      void qc.invalidateQueries({ queryKey: ["jobs"] });
    },
  });
}

type Tone = "slate" | "green" | "amber" | "red" | "blue";

export const endpointStates: Record<AgentEndpointAgent["state"], { label: string; tone: Tone }> = {
  current: { label: "Juist adres", tone: "green" },
  differs: { label: "Ander adres", tone: "amber" },
  failed: { label: "Teruggezet", tone: "red" },
  manual: { label: "Eerst bijwerken", tone: "slate" },
  unknown: { label: "Onbekend", tone: "slate" },
};

export const applicable = (a: AgentEndpointAgent) => a.online && (a.state === "differs" || a.state === "failed");
