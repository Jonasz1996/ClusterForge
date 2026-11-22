"use client";

import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { api, unwrap } from "./api/client";
import type { components } from "./api/schema";
import { liveInterval } from "./inventory";

export type NodeChange = components["schemas"]["NodeChange"];
export type NodeChanges = components["schemas"]["NodeChanges"];

// De filters op soort, in de volgorde van het menu. Een filter kan meer
// soorten samen nemen.
export const changeKinds: { value: string; label: string; kinds: string }[] = [
  { value: "", label: "Alles", kinds: "" },
  { value: "package", label: "Pakketten", kinds: "package" },
  { value: "service", label: "Diensten", kinds: "service" },
  { value: "file", label: "Bestanden", kinds: "file" },
  { value: "boot", label: "Herstarts", kinds: "boot" },
  { value: "login", label: "Logins", kinds: "login" },
  { value: "network", label: "Netwerk", kinds: "network" },
  { value: "vm", label: "Proxmox", kinds: "vm" },
  { value: "other", label: "Overig", kinds: "oom,facts,agent,user,command" },
];

export const changePeriods: { value: string; label: string; hours: number }[] = [
  { value: "24h", label: "24 uur", hours: 24 },
  { value: "7d", label: "7 dagen", hours: 24 * 7 },
  { value: "30d", label: "30 dagen", hours: 24 * 30 },
  { value: "180d", label: "180 dagen", hours: 24 * 180 },
];

export const kindLabels: Record<string, string> = {
  package: "pakket",
  service: "dienst",
  file: "bestand",
  boot: "herstart",
  login: "login",
  oom: "geheugen",
  network: "netwerk",
  facts: "systeem",
  vm: "Proxmox",
  agent: "agent",
  user: "gebruiker",
  command: "commando",
};

export const sourceLabels: Record<NodeChange["source"], string> = {
  agent: "gezien door de agent",
  server: "afgeleid door ClusterForge",
  proxmox: "uit Proxmox",
  clusterforge: "gedaan door ClusterForge",
};

function since(hours: number) {
  // Op de minuut afgerond, zodat de vraag niet elke render verandert.
  const ms = Math.floor(Date.now() / 60000) * 60000 - hours * 3600 * 1000;
  return new Date(ms).toISOString();
}

export function useNodeChanges(nodeId: string, kind: string, hours: number) {
  const kinds = changeKinds.find((k) => k.value === kind)?.kinds ?? "";
  return useQuery({
    queryKey: ["nodes", nodeId, "changes", kinds, hours],
    queryFn: async () =>
      unwrap(
        await api.GET("/nodes/{nodeId}/changes", {
          params: { path: { nodeId }, query: { from: since(hours), kind: kinds || undefined, limit: 200 } },
        }),
      ),
    enabled: nodeId !== "",
    refetchInterval: liveInterval,
    placeholderData: keepPreviousData,
  });
}
