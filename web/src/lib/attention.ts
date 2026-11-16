"use client";

// useAttentionBadges geeft de tellers naast de menu-items (open incidenten,
// offline agents, …). Komt uit /attention; zie mijlpaal 3.
export function useAttentionBadges(enabled: boolean): Record<string, { count: number; tone: "red" | "amber" }> | undefined {
  void enabled;
  return undefined;
}
