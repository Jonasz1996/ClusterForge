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

// --- items en grafieken (M2) ---------------------------------------------------------------

export type ZabbixItem = components["schemas"]["ZabbixItem"];
export type ZabbixSeries = components["schemas"]["ZabbixSeries"];
export type ZabbixRange = "1h" | "6h" | "24h" | "7d" | "30d";
export const rangeLabel: Record<ZabbixRange, string> = { "1h": "1 u", "6h": "6 u", "24h": "24 u", "7d": "7 d", "30d": "30 d" };

export function useZabbixHostItems(hostid: string | undefined, enabled = true) {
  return useQuery({
    queryKey: ["zabbix-items", hostid],
    queryFn: async () => unwrap(await api.GET("/zabbix/hosts/{hostid}/items", { params: { path: { hostid: hostid! } } })),
    enabled: !!hostid && enabled,
    refetchInterval: 60_000,
  });
}

// seriesQuery is de vraag voor één grafieklijn; ook voor useQueries.
export function seriesQuery(itemid: string | undefined, range: ZabbixRange) {
  return {
    queryKey: ["zabbix-series", itemid, range],
    queryFn: async () => unwrap(await api.GET("/zabbix/items/{itemid}/series", { params: { path: { itemid: itemid! }, query: { range } } })),
    enabled: !!itemid,
    refetchInterval: range === "1h" || range === "6h" ? 60_000 : 300_000,
  };
}

export function useZabbixItemSeries(itemid: string | undefined, range: ZabbixRange) {
  return useQuery(seriesQuery(itemid, range));
}

const numFmt = (max: number) => new Intl.NumberFormat("nl-BE", { maximumFractionDigits: max });

function scaled(v: number, base: number, units: string[]) {
  let i = 0;
  let x = Math.abs(v);
  while (x >= base && i < units.length - 1) {
    x /= base;
    i++;
  }
  const n = Math.sign(v) * x;
  return `${numFmt(Math.abs(n) < 10 ? 2 : Math.abs(n) < 100 ? 1 : 0).format(n)} ${units[i]}`.trim();
}

function duration(s: number) {
  const neg = s < 0;
  let x = Math.abs(s);
  if (x < 1) return `${numFmt(0).format(x * 1000)} ms`;
  const parts: string[] = [];
  for (const [n, l] of [
    [86400, "d"],
    [3600, "u"],
    [60, "min"],
  ] as const) {
    if (x >= n) {
      parts.push(`${Math.floor(x / n)} ${l}`);
      x %= n;
    }
    if (parts.length === 2) break;
  }
  if (parts.length < 2 && x >= 1) parts.push(`${numFmt(parts.length ? 0 : 1).format(x)} s`);
  return (neg ? "-" : "") + parts.join(" ");
}

// formatZabbix toont een waarde met de eenheid zoals Zabbix het doet: B en
// Bps met 1024, bps en andere eenheden met 1000 (K, M, G), s en uptime als
// duur, unixtime als datum. Een ! voor de eenheid betekent: niet omrekenen.
export function formatZabbix(v: number, units: string): string {
  const u = units.trim();
  if (u.startsWith("!")) return `${numFmt(2).format(v)} ${u.slice(1)}`.trim();
  switch (u) {
    case "":
      return numFmt(Math.abs(v) < 10 ? 2 : 1).format(v);
    case "%":
      return `${numFmt(Math.abs(v) < 10 ? 1 : 0).format(v)} %`;
    case "B":
      return scaled(v, 1024, ["B", "KiB", "MiB", "GiB", "TiB", "PiB"]);
    case "Bps":
      return scaled(v, 1024, ["B/s", "KiB/s", "MiB/s", "GiB/s", "TiB/s"]);
    case "bps":
      return scaled(v, 1000, ["bps", "Kbps", "Mbps", "Gbps", "Tbps"]);
    case "s":
    case "uptime":
      return duration(v);
    case "unixtime":
      return v > 0 ? new Date(v * 1000).toLocaleString("nl-BE") : "—";
    case "rpm":
    case "RPM":
    case "°C":
    case "C":
      return `${numFmt(1).format(v)} ${u}`;
  }
  return scaled(v, 1000, ["", "K", "M", "G", "T"].map((p) => `${p}${u}`));
}

// formatItem toont de laatste waarde van een item.
export function formatItem(it: ZabbixItem) {
  if (!it.numeric) return it.last_value;
  const v = Number(it.last_value);
  return Number.isFinite(v) && it.last_value !== "" ? formatZabbix(v, it.units) : it.last_value;
}

// Belangrijkste grafieken van een Linux-host (template Linux by Zabbix agent).
const keyItems: { label: string; match: (k: string) => boolean }[] = [
  { label: "CPU", match: (k) => k === "system.cpu.util" || k === "system.cpu.util[,,avg1]" },
  { label: "Geheugen", match: (k) => k === "vm.memory.utilization" },
  { label: "Load", match: (k) => k === "system.cpu.load[all,avg1]" },
  { label: "Schijf /", match: (k) => k === "vfs.fs.dependent.size[/,pused]" || k === "vfs.fs.size[/,pused]" },
  { label: "Netwerk in", match: (k) => /^net\.if\.in\[("?)(eth0|ens\d+|enp\S+|vmbr0)\1(,|\])/.test(k) },
  { label: "Netwerk uit", match: (k) => /^net\.if\.out\[("?)(eth0|ens\d+|enp\S+|vmbr0)\1(,|\])/.test(k) },
];

export function pickKeyItems(items: ZabbixItem[]) {
  const out: { label: string; item: ZabbixItem }[] = [];
  for (const k of keyItems) {
    const it = items.find((i) => i.numeric && !i.error && k.match(i.key));
    if (it) out.push({ label: k.label, item: it });
  }
  return out;
}

// --- dashboards (M3) -------------------------------------------------------------------

export type ZabbixMetrics = components["schemas"]["ZabbixMetrics"];
export type ZabbixHostMetrics = components["schemas"]["ZabbixHostMetrics"];
export type ZabbixEvents = components["schemas"]["ZabbixEvents"];
export type ZabbixTopCount = components["schemas"]["ZabbixTopCount"];

export function useZabbixMetrics(enabled = true) {
  return useQuery({
    queryKey: ["zabbix-metrics"],
    queryFn: async () => unwrap(await api.GET("/zabbix/metrics")),
    enabled,
    refetchInterval: 60_000,
  });
}

export function useZabbixEvents(window: ZabbixWindow, enabled = true) {
  return useQuery({
    queryKey: ["zabbix-events", window],
    queryFn: async () => unwrap(await api.GET("/zabbix/events", { params: { query: { window } } })),
    enabled,
    refetchInterval: 120_000,
  });
}

// --- eigen dashboards (M4) ---

export type ZabbixDashboard = components["schemas"]["ZabbixDashboard"];
export type ZabbixDashboardInput = components["schemas"]["ZabbixDashboardInput"];
export type ZabbixPanel = components["schemas"]["ZabbixPanel"];
export type ZabbixLatestItem = components["schemas"]["ZabbixLatestItem"];
export type PanelType = "graph" | "stat" | "gauge" | "problems" | "text";

export const panelTypeLabel: Record<PanelType, string> = {
  graph: "Grafiek",
  stat: "Getal",
  gauge: "Meter",
  problems: "Problemen",
  text: "Tekst",
};

const boardsKey = ["zabbix-dashboards"] as const;

export function useZabbixBoards() {
  return useQuery({
    queryKey: boardsKey,
    queryFn: async () => unwrap(await api.GET("/zabbix/dashboards")),
  });
}

export function useSaveZabbixBoard() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({ id, body }: { id?: string; body: ZabbixDashboardInput }) =>
      id
        ? unwrap(await api.PUT("/zabbix/dashboards/{id}", { params: { path: { id } }, body }))
        : unwrap(await api.POST("/zabbix/dashboards", { body })),
    onSuccess: () => void qc.invalidateQueries({ queryKey: boardsKey }),
  });
}

export function useDeleteZabbixBoard() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (id: string) => unwrap(await api.DELETE("/zabbix/dashboards/{id}", { params: { path: { id } } })),
    onSuccess: () => void qc.invalidateQueries({ queryKey: boardsKey }),
  });
}

export function useZabbixLatest(itemids: string[]) {
  const ids = [...new Set(itemids)].sort().join(",");
  return useQuery({
    queryKey: ["zabbix-latest", ids],
    queryFn: async () => unwrap(await api.GET("/zabbix/items/latest", { params: { query: { itemids: ids } } })),
    enabled: ids !== "",
    refetchInterval: 60_000,
  });
}

// boardInput maakt van een bewaard of geïmporteerd dashboard wat de server
// verwacht: alleen de bekende velden, met standaardwaarden waar iets ontbreekt.
export function boardInput(v: unknown): ZabbixDashboardInput {
  const o = (v && typeof v === "object" ? v : {}) as Record<string, unknown>;
  const str = (x: unknown) => (typeof x === "string" ? x : "");
  const num = (x: unknown, d: number) => (typeof x === "number" && Number.isFinite(x) ? x : d);
  const optNum = (x: unknown) => (typeof x === "number" && Number.isFinite(x) ? x : null);
  const panels = Array.isArray(o.panels) ? o.panels : [];
  return {
    name: str(o.name),
    description: str(o.description),
    refresh: num(o.refresh, 60),
    panels: panels.map((pv) => {
      const p = (pv && typeof pv === "object" ? pv : {}) as Record<string, unknown>;
      const items = Array.isArray(p.items) ? p.items : [];
      return {
        id: str(p.id),
        type: str(p.type),
        title: str(p.title),
        w: num(p.w, 2),
        h: num(p.h, 2),
        items: items.map((iv) => {
          const i = (iv && typeof iv === "object" ? iv : {}) as Record<string, unknown>;
          return { itemid: typeof i.itemid === "number" ? String(i.itemid) : str(i.itemid), label: str(i.label) };
        }),
        range: str(p.range),
        warn: optNum(p.warn),
        crit: optNum(p.crit),
        max: optNum(p.max),
        min_severity: num(p.min_severity, 0),
        hostid: str(p.hostid),
        text: str(p.text),
      };
    }),
  };
}

// --- schrijven naar Zabbix (M6) ---

export type ZabbixAckInput = components["schemas"]["ZabbixAckInput"];

export function useAckZabbixProblem() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({ eventid, body }: { eventid: string; body: ZabbixAckInput }) =>
      unwrap(await api.POST("/zabbix/problems/{eventid}/acknowledge", { params: { path: { eventid } }, body })),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: key });
      // De server leest Zabbix meteen opnieuw; even later klopt alles.
      setTimeout(() => void qc.invalidateQueries({ queryKey: key }), 2_500);
    },
  });
}

// Onderhoud naar Zabbix (M6): staat een node in onderhoud, dan zet
// ClusterForge in Zabbix een onderhoud op zijn host.
export type ZabbixMaintenance = components["schemas"]["ZabbixMaintenance"];

const maintKey = ["zabbix-maintenance"] as const;

export function useZabbixMaintenance() {
  return useQuery({
    queryKey: maintKey,
    queryFn: async () => unwrap(await api.GET("/zabbix/maintenance")),
    refetchInterval: 60_000,
  });
}

export function useSetZabbixMaintenance() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (sync: boolean) => unwrap(await api.PUT("/zabbix/maintenance", { body: { sync } })),
    onSuccess: (m) => {
      qc.setQueryData(maintKey, m);
      void qc.invalidateQueries({ queryKey: key });
    },
  });
}
