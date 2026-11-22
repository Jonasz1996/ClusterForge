"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, unwrap } from "./api/client";
import type { components } from "./api/schema";

export type ApiToken = components["schemas"]["ApiToken"];
export type ApiTokenInput = components["schemas"]["ApiTokenInput"];
export type DashboardCluster = components["schemas"]["DashboardCluster"];

const keys = {
  list: ["api-tokens"] as const,
  preview: ["api-tokens", "preview"] as const,
};

export function useApiTokens(enabled: boolean) {
  return useQuery({ queryKey: keys.list, enabled, queryFn: async () => unwrap(await api.GET("/api-tokens")) });
}

export function useDashboardPreview(enabled: boolean) {
  return useQuery({
    queryKey: keys.preview,
    enabled,
    queryFn: async () => unwrap(await api.GET("/api-tokens/preview")),
  });
}

export function useCreateApiToken() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (body: ApiTokenInput) => unwrap(await api.POST("/api-tokens", { body })),
    onSuccess: () => qc.invalidateQueries({ queryKey: keys.list }),
  });
}

export function useDeleteApiToken() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (id: string) =>
      unwrap(await api.DELETE("/api-tokens/{tokenId}", { params: { path: { tokenId: id } } })),
    onSuccess: () => qc.invalidateQueries({ queryKey: keys.list }),
  });
}

// homepageYaml is een services.yaml-blok voor homepage (customapi), één tegel
// per cluster. In de eigen homepage komt "token" versleuteld in de geheimen;
// homepage.dev zelf gebruikt de header eronder.
export function homepageYaml(
  origin: string,
  base: string,
  token: string,
  clusters: { name: string; slug: string; id: string }[],
  soc = false,
) {
  const lines = ["- ClusterForge:"];
  // Het beveiligingsoverzicht (fase 25) kan alleen met een token voor alle
  // clusters.
  if (soc) {
    lines.push(
      `    - Beveiliging:`,
      `        icon: mdi-shield-check`,
      `        href: ${origin}/soc`,
      `        description: ClusterForge SOC`,
      `        widget:`,
      `          type: customapi`,
      `          url: ${origin}${base}/soc`,
      `          token: ${token}`,
      `          refreshInterval: 60000`,
      `          mappings:`,
      `            - field: level_label`,
      `              label: Niveau`,
      `            - field: score`,
      `              label: Score`,
      `            - field: attacks_24h`,
      `              label: Aanvallen 24 u`,
      `            - field: active_bans`,
      `              label: Bans`,
      `            - field: protected`,
      `              label: Beschermd`,
    );
    // Zabbix (fase 33): problemen en bereikbaarheid uit Zabbix.
    lines.push(
      `    - Zabbix:`,
      `        icon: mdi-chart-line`,
      `        href: ${origin}/zabbix/overzicht`,
      `        description: Zabbix via ClusterForge`,
      `        widget:`,
      `          type: customapi`,
      `          url: ${origin}${base}/zabbix`,
      `          token: ${token}`,
      `          refreshInterval: 60000`,
      `          mappings:`,
      `            - field: level_label`,
      `              label: Toestand`,
      `            - field: problems`,
      `              label: Problemen`,
      `            - field: high`,
      `              label: Hoog`,
      `            - field: available`,
      `              label: Bereikbaar`,
    );
    // AI-verslag (fase 38): dreigingsniveau en netwerkscore van de AI.
    lines.push(
      `    - AI-verslag:`,
      `        icon: mdi-robot`,
      `        href: ${origin}/aiverslag`,
      `        description: AI-verslag via ClusterForge`,
      `        widget:`,
      `          type: customapi`,
      `          url: ${origin}${base}/aiverslag`,
      `          token: ${token}`,
      `          refreshInterval: 60000`,
      `          mappings:`,
      `            - field: level_label`,
      `              label: Niveau`,
      `            - field: score`,
      `              label: Score`,
      `            - field: incidents`,
      `              label: Incidenten`,
      `            - field: unseen_alarms`,
      `              label: Alarmen`,
    );
  }
  for (const c of clusters) {
    lines.push(
      `    - ${yamlKey(c.name)}:`,
      `        icon: mdi-server-network`,
      `        href: ${origin}/clusters/detail?id=${c.id}`,
      `        description: ClusterForge`,
      `        widget:`,
      `          type: customapi`,
      `          url: ${origin}${base}/clusters/${c.slug}`,
      `          token: ${token}`,
      `          # homepage.dev zelf: in plaats van token`,
      `          # headers:`,
      `          #   Authorization: Bearer ${token}`,
      `          refreshInterval: 30000`,
      `          mappings:`,
      `            - field: status_label`,
      `              label: Status`,
      `            - field: nodes`,
      `              label: Nodes`,
      `            - field: vip_owner`,
      `              label: VIP`,
      `            - field: db_label`,
      `              label: Database`,
      `            - field: incidents_open`,
      `              label: Incidenten`,
      `            - field: updates`,
      `              label: Updates`,
      `            - field: score`,
      `              label: Score`,
      `            - field: crowdsec_bans`,
      `              label: Bans`,
    );
  }
  return lines.join("\n");
}

function yamlKey(s: string) {
  return /^[\w .()-]+$/.test(s) && !/^\s|\s$/.test(s) ? s : JSON.stringify(s);
}
