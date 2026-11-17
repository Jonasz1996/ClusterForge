"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, unwrap } from "./api/client";
import type { components } from "./api/schema";

// Zabbix-monitoring (fase 33): de stand uit de Zabbix-API, elke minuut
// verzameld door de server.

export type ZabbixOverview = components["schemas"]["ZabbixOverview"];
export type ZabbixProblem = components["schemas"]["ZabbixProblem"];
export type ZabbixHost = components["schemas"]["ZabbixHost"];
export type ZabbixProxy = components["schemas"]["ZabbixProxy"];
export type ZabbixHANode = components["schemas"]["ZabbixHANode"];
export type ZabbixHealth = components["schemas"]["ZabbixHealth"];
export type ZabbixNodeLink = components["schemas"]["ZabbixNodeLink"];
export type ZabbixTrendPoint = components["schemas"]["ZabbixTrendPoint"];
export type ZabbixWindow = "24h" | "7d" | "30d";
export type ZabbixLevel = "kritiek" | "probleem" | "waarschuwing" | "normaal" | "onbekend";

const key = ["zabbix-overview"] as const;

export function useZabbixOverview(window: ZabbixWindow = "24h") {
  return useQuery({
    queryKey: [...key, window],
    queryFn: async () => unwrap(await api.GET("/zabbix/overview", { params: { query: { window } } })),
    // De server verzamelt elke minuut; vaker vragen heeft geen zin.
    refetchInterval: 30_000,
  });
}

export function useRefreshZabbix() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async () => unwrap(await api.POST("/zabbix/refresh")),
    onSuccess: () => void qc.invalidateQueries({ queryKey: key }),
  });
}

export function useZabbixHostLink() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (v: { hostid: string; node_id: string | null } | { hostid: string; reset: true }) => {
      if ("reset" in v) {
        return unwrap(await api.DELETE("/zabbix/hosts/{hostid}/link", { params: { path: { hostid: v.hostid } } }));
      }
      return unwrap(
        await api.PUT("/zabbix/hosts/{hostid}/link", { params: { path: { hostid: v.hostid } }, body: { node_id: v.node_id } }),
      );
    },
    // De server draait meteen een ronde; even later is de koppeling zichtbaar.
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: key });
      setTimeout(() => void qc.invalidateQueries({ queryKey: key }), 2_000);
    },
  });
}

export const windowLabel: Record<ZabbixWindow, string> = { "24h": "24 uur", "7d": "7 dagen", "30d": "30 dagen" };

// Ernst zoals Zabbix hem noemt, met de kleuren van ClusterForge.
export const severities = [5, 4, 3, 2, 1, 0] as const;
export const severityLabel: Record<number, string> = {
  5: "Ramp",
  4: "Hoog",
  3: "Gemiddeld",
  2: "Waarschuwing",
  1: "Info",
  0: "Niet ingedeeld",
};
export const severityStyle: Record<number, { dot: string; badge: string; bar: string; text: string }> = {
  5: { dot: "bg-red-700", badge: "bg-red-700 text-white", bar: "#b91c1c", text: "text-red-700 dark:text-red-400" },
  4: { dot: "bg-red-500", badge: "bg-red-500 text-white", bar: "#ef4444", text: "text-red-600 dark:text-red-400" },
  3: { dot: "bg-orange-500", badge: "bg-orange-500 text-white", bar: "#f97316", text: "text-orange-600 dark:text-orange-400" },
  2: { dot: "bg-amber-400", badge: "bg-amber-300 text-amber-950", bar: "#fbbf24", text: "text-amber-700 dark:text-amber-300" },
  1: { dot: "bg-sky-400", badge: "bg-sky-100 text-sky-800 dark:bg-sky-950 dark:text-sky-200", bar: "#38bdf8", text: "text-sky-700 dark:text-sky-300" },
  0: { dot: "bg-slate-400", badge: "bg-slate-200 text-slate-700 dark:bg-slate-800 dark:text-slate-300", bar: "#94a3b8", text: "text-slate-500" },
};

export const levelLabel: Record<ZabbixLevel, string> = {
  kritiek: "Kritiek",
  probleem: "Probleem",
  waarschuwing: "Waarschuwing",
  normaal: "Normaal",
  onbekend: "Onbekend",
};

export const availLabel: Record<string, string> = {
  ok: "bereikbaar",
  down: "onbereikbaar",
  unknown: "onbekend",
  none: "geen interface",
  disabled: "uitgeschakeld",
};

export const sourceLabel: Record<string, string> = {
  manual: "met de hand gekoppeld",
  agent: "via Zabbix-agents van ClusterForge",
  name: "op naam",
  ip: "op IP-adres",
};

// countsOf telt de problemen per ernst (zonder onderdrukte).
export function countBySeverity(o: ZabbixOverview): Record<number, number> {
  const c = o.counts;
  return { 5: c.disaster, 4: c.high, 3: c.average, 2: c.warning, 1: c.info, 0: c.not_classified };
}

// Links naar de Zabbix-webinterface.
export function zabbixLinks(base: string) {
  if (!base) return null;
  return {
    problem: (p: ZabbixProblem) => `${base}tr_events.php?triggerid=${encodeURIComponent(p.triggerid)}&eventid=${encodeURIComponent(p.eventid)}`,
    host: (hostid: string) => `${base}zabbix.php?action=latest.view&filter_set=1&hostids%5B%5D=${encodeURIComponent(hostid)}`,
    hostProblems: (hostid: string) => `${base}zabbix.php?action=problem.view&filter_set=1&hostids%5B%5D=${encodeURIComponent(hostid)}`,
    problems: `${base}zabbix.php?action=problem.view`,
  };
}

// duration: hoe lang al, kort ("3 d 4 u", "12 min").
export function since(iso: string, now = Date.now()) {
  const s = Math.max(0, Math.floor((now - new Date(iso).getTime()) / 1000));
  const d = Math.floor(s / 86400);
  const h = Math.floor((s % 86400) / 3600);
  const m = Math.floor((s % 3600) / 60);
  if (d > 0) return h > 0 ? `${d} d ${h} u` : `${d} d`;
  if (h > 0) return m > 0 ? `${h} u ${m} min` : `${h} u`;
  if (m > 0) return `${m} min`;
  return `${s} s`;
}
