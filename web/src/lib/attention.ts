"use client";

import { useQuery } from "@tanstack/react-query";
import { api, unwrap } from "@/lib/api/client";
import type { components } from "@/lib/api/schema";

export type AttentionItem = components["schemas"]["AttentionItem"];

// useAttention is wat nu aandacht vraagt (offline agents, clusters in
// storing, open incidenten, …). Eén vraag voor het overzicht én de tellers in
// het menu; ze delen de cache.
export function useAttention(enabled = true) {
  return useQuery({
    queryKey: ["attention"],
    queryFn: async () => unwrap(await api.GET("/attention")),
    refetchInterval: 30_000,
    enabled,
  });
}

// useAttentionBadges geeft de tellers naast de menu-items.
export function useAttentionBadges(enabled: boolean) {
  return useAttention(enabled).data?.badges;
}
