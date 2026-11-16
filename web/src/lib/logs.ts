"use client";

import { useMutation, useQueries, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, unwrap } from "./api/client";
import type { components } from "./api/schema";

// Logs (fase 23): de agents lezen de logs op hun node (bestand onder
// /var/log, journaal, container); de server legt meerdere bronnen op één
// tijdlijn. De types volgen internal/logs.

type S = components["schemas"];
export type LogNode = S["LogNode"];
export type LogSources = S["LogSources"];
export type LogSource = S["LogSource"];
export type LogKind = LogSource["kind"];
export type LogQueryInput = S["LogQueryInput"];
export type LogQueryResult = S["LogQueryResult"];
export type LogEntry = S["LogEntry"];
export type LogSourceResult = S["LogSourceResult"];
export type LogView = S["LogView"];
export type LogViewInput = S["LogViewInput"];

export const levels = ["error", "warn", "info", "debug", ""] as const;
export type Level = (typeof levels)[number];

export const levelLabel: Record<string, string> = {
  error: "Fout",
  warn: "Waarschuwing",
  info: "Info",
  debug: "Debug",
  "": "Zonder niveau",
};

// Tijdvakken; "custom" gebruikt from/to.
export const ranges = [
  { id: "15m", label: "15 min", ms: 15 * 60_000 },
  { id: "1h", label: "1 uur", ms: 3_600_000 },
  { id: "6h", label: "6 uur", ms: 6 * 3_600_000 },
  { id: "24h", label: "24 uur", ms: 24 * 3_600_000 },
  { id: "7d", label: "7 dagen", ms: 7 * 24 * 3_600_000 },
  { id: "all", label: "Alles", ms: 0 },
  { id: "custom", label: "Eigen tijdvak", ms: 0 },
] as const;
export type RangeId = (typeof ranges)[number]["id"];

// Het filter zoals de pagina het bewaart in een weergave.
export type LogFilterState = {
  range: RangeId;
  from?: string;
  to?: string;
  pattern: string;
  regex: boolean;
  ignoreCase: boolean;
  exclude: string;
  levels: Level[];
  limit: number;
  // parse is een eigen reguliere expressie met benoemde groepen.
  parse: string;
};

export const defaultFilter: LogFilterState = {
  range: "1h",
  pattern: "",
  regex: false,
  ignoreCase: true,
  exclude: "",
  levels: [],
  limit: 500,
  parse: "",
};

// toQuery maakt van de bronnen en het filter een vraag aan de server. Een
// relatief tijdvak rekent vanaf nu, dus bij elke vernieuwing opnieuw.
export function toQuery(sources: LogSource[], f: LogFilterState, now = Date.now()): LogQueryInput {
  const q: LogQueryInput = { sources, limit: f.limit, ignore_case: f.ignoreCase, regex: f.regex };
  const r = ranges.find((x) => x.id === f.range);
  if (f.range === "custom") {
    if (f.from) q.since = new Date(f.from).toISOString();
    if (f.to) q.until = new Date(f.to).toISOString();
  } else if (r && r.ms > 0) {
    q.since = new Date(now - r.ms).toISOString();
  }
  if (f.pattern) q.pattern = f.pattern;
  if (f.exclude) q.exclude = f.exclude;
  if (f.levels.length > 0) q.levels = [...f.levels];
  return q;
}

export function sourceKey(s: LogSource) {
  return `${s.node_id}|${s.kind}|${s.name}`;
}

export function sourceLabel(kind: string, name: string) {
  if (kind === "journal") return name ? `journaal: ${name}` : "journaal (alles)";
  if (kind === "docker") return `container: ${name}`;
  return name;
}

export function useLogNodes() {
  return useQuery({
    queryKey: ["logs", "nodes"],
    queryFn: async () => unwrap(await api.GET("/logs/nodes")) as unknown as { nodes: LogNode[]; protocol: number },
    refetchInterval: 30_000,
  });
}

// useLogSources vraagt de bronnenlijst van elke gekozen node.
export function useLogSources(nodeIds: string[]) {
  return useQueries({
    queries: nodeIds.map((id) => ({
      queryKey: ["logs", "sources", id],
      queryFn: async () =>
        unwrap(await api.GET("/nodes/{nodeId}/log-sources", { params: { path: { nodeId: id } } })) as unknown as LogSources,
      staleTime: 60_000,
      retry: false,
    })),
  });
}

export async function fetchLogs(q: LogQueryInput) {
  return unwrap(await api.POST("/logs/query", { body: q })) as unknown as LogQueryResult;
}

export function useLogViews() {
  return useQuery({
    queryKey: ["logs", "views"],
    queryFn: async () => (unwrap(await api.GET("/logs/views")) as unknown as { views: LogView[] }).views,
  });
}

export function useSaveLogView() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({ id, body }: { id?: string; body: LogViewInput }) =>
      (id
        ? unwrap(await api.PUT("/logs/views/{viewId}", { params: { path: { viewId: id } }, body }))
        : unwrap(await api.POST("/logs/views", { body }))) as unknown as LogView,
    onSuccess: () => void qc.invalidateQueries({ queryKey: ["logs", "views"] }),
  });
}

export function useDeleteLogView() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (id: string) => {
      const res = await api.DELETE("/logs/views/{viewId}", { params: { path: { viewId: id } } });
      if (!res.response.ok) unwrap(res);
    },
    onSuccess: () => void qc.invalidateQueries({ queryKey: ["logs", "views"] }),
  });
}

// --- ontleden en vergelijken in de browser ----------------------------------

// fieldsOf haalt key=value-paren uit een bericht (ook key="met spaties").
const kvRe = /(?:^|\s)([A-Za-z_][\w.-]{0,40})=("(?:[^"\\]|\\.)*"|[^\s,;]+)/g;

export function fieldsOf(msg: string, parse: RegExp | null): Record<string, string> {
  const out: Record<string, string> = {};
  const firstLine = msg.split("\n", 1)[0];
  for (const m of firstLine.matchAll(kvRe)) {
    let v = m[2];
    if (v.startsWith('"')) v = v.slice(1, -1);
    out[m[1]] = v;
  }
  if (parse) {
    const m = parse.exec(firstLine);
    if (m?.groups) {
      for (const [k, v] of Object.entries(m.groups)) if (v !== undefined) out[k] = v;
    }
  }
  return out;
}

// compileParse maakt de eigen ontleding; null bij een lege of foute.
export function compileParse(src: string): { re: RegExp | null; error: string } {
  if (!src.trim()) return { re: null, error: "" };
  try {
    const re = new RegExp(src);
    if (!/\(\?<[A-Za-z]/.test(src)) return { re, error: "Geef benoemde groepen, zoals (?<status>\\d{3})." };
    return { re, error: "" };
  } catch (e) {
    return { re: null, error: e instanceof Error ? e.message : "ongeldige expressie" };
  }
}

// patternOf maakt van een bericht een patroon: getallen, adressen, id's en
// tekst tussen aanhalingstekens worden <*>, zodat dezelfde soort regel van
// verschillende nodes samenvalt.
export function patternOf(msg: string): string {
  return msg
    .split("\n", 1)[0]
    .slice(0, 300)
    .replace(/\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b/gi, "<id>")
    .replace(/\b(?:\d{1,3}\.){3}\d{1,3}(?::\d+)?\b/g, "<ip>")
    .replace(/\b[0-9a-f]{12,}\b/gi, "<hex>")
    .replace(/"[^"]{0,200}"/g, '"<*>"')
    .replace(/'[^']{0,200}'/g, "'<*>'")
    .replace(/\d+(?:[.,:]\d+)*/g, "<n>")
    .replace(/\s+/g, " ")
    .trim();
}

// Kleuren per bron, goed te onderscheiden in licht en donker.
export const sourceColors = [
  "#2563eb",
  "#d97706",
  "#059669",
  "#db2777",
  "#7c3aed",
  "#0891b2",
  "#65a30d",
  "#dc2626",
  "#4f46e5",
  "#ca8a04",
  "#0d9488",
  "#9333ea",
];

export function colorOf(i: number) {
  return sourceColors[i % sourceColors.length];
}
