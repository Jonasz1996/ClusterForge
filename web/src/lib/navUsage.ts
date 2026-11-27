"use client";

import { useQuery } from "@tanstack/react-query";
import { useSyncExternalStore } from "react";
import { api, unwrap } from "@/lib/api/client";
import type { NavUsage } from "@/lib/nav";

// Welke onderdelen in gebruik zijn (fase 47): het menu verbergt Kubernetes,
// Kaart, GitOps, Zabbix, AI-verslag en de platformen die niets beheren.
export function useNavUsage(enabled = true) {
  return useQuery({
    queryKey: ["nav-usage"],
    queryFn: async (): Promise<NavUsage> => unwrap(await api.GET("/nav/usage")),
    staleTime: 5 * 60_000,
    enabled,
  });
}

// "Alles tonen" zet ook wat niet in gebruik is in het menu, voor deze
// browser.
const KEY = "cf-nav-alles";
const listeners = new Set<() => void>();

function read(): boolean {
  try {
    return localStorage.getItem(KEY) === "1";
  } catch {
    return false;
  }
}

export function setShowAll(on: boolean) {
  try {
    if (on) localStorage.setItem(KEY, "1");
    else localStorage.removeItem(KEY);
  } catch {
    // alleen tot de pagina herlaadt
  }
  for (const l of listeners) l();
}

export function useShowAll(): [boolean, (on: boolean) => void] {
  const on = useSyncExternalStore(
    (l) => {
      listeners.add(l);
      return () => listeners.delete(l);
    },
    read,
    () => false,
  );
  return [on, setShowAll];
}
