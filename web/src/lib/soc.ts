"use client";

import { useQuery } from "@tanstack/react-query";
import { api, unwrap } from "./api/client";
import type { components } from "./api/schema";

// SOC (fase 25): het beveiligingsoverzicht over CrowdSec, de firewall en de
// logins van ClusterForge heen.

export type SocOverview = components["schemas"]["SocOverview"];
export type SocItem = components["schemas"]["SocItem"];
export type SocAttacker = components["schemas"]["SocAttacker"];
export type SocCoverage = components["schemas"]["SocCoverage"];
export type SocCount = components["schemas"]["SocCount"];
export type SocWindow = "24h" | "7d";
export type SocLevel = SocOverview["level"]["level"];

export function useSoc(window: SocWindow, enabled = true) {
  return useQuery({
    queryKey: ["soc", window],
    queryFn: async () => unwrap(await api.GET("/soc", { params: { query: { window } } })),
    // Niet verversen in een verborgen tabblad; dat doet react-query al.
    refetchInterval: 30_000,
    enabled,
  });
}

export const levelLabel: Record<SocLevel, string> = {
  kritiek: "Kritiek",
  verhoogd: "Verhoogd",
  waakzaam: "Waakzaam",
  normaal: "Normaal",
};

export const windowLabel: Record<SocWindow, string> = { "24h": "24 uur", "7d": "7 dagen" };
