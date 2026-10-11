"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, unwrap } from "./api/client";
import type { components } from "./api/schema";
import type { Environment } from "./inventory";

export type MapLayer = components["schemas"]["MapLayer"];
export type MapLayerName = MapLayer["layer"];
export type MapItem = components["schemas"]["MapItem"];
export type MapItemKind = MapItem["kind"];
export type MapEdge = components["schemas"]["MapEdge"];
export type HostNetwork = components["schemas"]["HostNetwork"];
export type HostInterface = components["schemas"]["HostInterface"];
export type NodeLink = components["schemas"]["NodeLink"];

export type MapFilter = { environment?: Environment; cluster_id?: string };

export const kindLabels: Record<MapItemKind, string> = {
  host: "Proxmox-host",
  storage: "Storage",
  guest: "VM of container",
  subnet: "Subnet",
  gateway: "Gateway",
  node: "Node",
  vip: "VIP",
  bridge: "Bridge",
};

export const edgeLabels: Record<MapEdge["kind"], string> = {
  disk: "schijf op",
  member: "in subnet",
  uplink: "uplink",
  gateway: "gateway",
  owner: "eigenaar",
  vrrp: "VRRP-peer",
};

// De kaart volgt de stroom; de timer vangt wat geen event geeft, zoals een
// VIP die van eigenaar wisselt.
const interval = 30_000;

export function useMap(layer: MapLayerName, filter: MapFilter, enabled = true) {
  return useQuery({
    queryKey: ["map", layer, filter],
    queryFn: async () => unwrap(await api.GET("/map", { params: { query: { layer, ...filter } } })),
    refetchInterval: interval,
    enabled,
  });
}

export function useHostNetwork(connectionId: string) {
  return useQuery({
    queryKey: ["proxmox", connectionId, "network"],
    queryFn: async () => unwrap(await api.GET("/proxmox/{proxmoxId}/network", { params: { path: { proxmoxId: connectionId } } })),
    enabled: connectionId !== "",
  });
}

export function useNodeLinks() {
  return useQuery({
    queryKey: ["proxmox", "node-links"],
    queryFn: async () => unwrap(await api.GET("/node-links")).items,
    refetchInterval: 60_000,
  });
}

// useDecideNodeLink bevestigt of negeert een voorstel. Bevestigen koppelt
// de node aan de VM; de node, de kaart en de score laden opnieuw.
export function useDecideNodeLink() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({ id, confirm }: { id: string; confirm: boolean }) => {
      const params = { params: { path: { linkId: id } } };
      return unwrap(
        await (confirm ? api.POST("/node-links/{linkId}/confirm", params) : api.POST("/node-links/{linkId}/ignore", params)),
      );
    },
    onSuccess: () => {
      for (const key of ["proxmox", "nodes", "map", "risk"]) void qc.invalidateQueries({ queryKey: [key] });
    },
  });
}

// ifaceType is de soort van een interface van een host in gewone woorden.
export function ifaceType(t: string) {
  switch (t) {
    case "bridge":
    case "OVSBridge":
      return "bridge";
    case "bond":
    case "OVSBond":
      return "bond";
    case "eth":
      return "netwerkkaart";
    case "vlan":
      return "VLAN";
    case "alias":
      return "alias";
    default:
      return t;
  }
}

// Een overlay kleurt de kaart met de uitkomst van de wat-als of met wat een
// incident raakte. Een element zonder markering wordt gedimd.
export type Mark = "failed" | "down" | "degraded" | "partial" | "restarted";

export const markInfo: Record<Mark, { label: string; ring: string; tone: "red" | "amber" | "blue" }> = {
  failed: { label: "valt uit", ring: "ring-[3px] ring-red-600", tone: "red" },
  down: { label: "down", ring: "ring-2 ring-red-500", tone: "red" },
  degraded: { label: "verminderd", ring: "ring-2 ring-amber-500", tone: "amber" },
  partial: { label: "netwerkkaart weg", ring: "ring-2 ring-amber-500", tone: "amber" },
  restarted: { label: "herstart door HA", ring: "ring-2 ring-sky-500", tone: "blue" },
};

const markOrder: Mark[] = ["restarted", "partial", "degraded", "down", "failed"];

function worst(a: Mark | undefined, b: Mark | undefined) {
  if (!a) return b;
  if (!b) return a;
  return markOrder.indexOf(a) >= markOrder.indexOf(b) ? a : b;
}

export type Overlay = {
  // marks per sleutel van de kaart, en per node als node:<id>.
  marks: Map<string, Mark>;
  // clusters kleurt de VIP's van een cluster, en met clusterWide alles wat
  // bij het cluster hoort.
  clusters: Map<string, Mark>;
  clusterWide: boolean;
  // services zijn de geraakte diensten, voor de laag Diensten.
  services: Set<string>;
};

export function markOf(o: Overlay, it: MapItem): Mark | undefined {
  let m = o.marks.get(it.id);
  if (it.node_id) m = worst(m, o.marks.get(`node:${it.node_id}`));
  if (it.cluster_id && (o.clusterWide || it.kind === "vip")) m = worst(m, o.clusters.get(it.cluster_id));
  return m;
}

function statusMark(status: string): Mark | undefined {
  if (status === "down" || status === "split_brain") return "down";
  if (status === "degraded") return "degraded";
  return undefined;
}

type WhatIf = components["schemas"]["RiskWhatIf"];
type IncidentSubject = components["schemas"]["IncidentSubject"];

export function whatIfOverlay(r: WhatIf): Overlay {
  const marks = new Map<string, Mark>();
  const put = (k: string, m: Mark | undefined) => {
    const w = worst(marks.get(k), m);
    if (w) marks.set(k, w);
  };
  for (const c of r.components) put(c.key, "failed");
  for (const g of r.guests) put(g.key, g.outcome);
  for (const n of r.nodes) put(`node:${n.id}`, n.outcome);
  const clusters = new Map<string, Mark>();
  for (const c of r.clusters) {
    const m = statusMark(c.during.status);
    if (m) clusters.set(c.id, m);
  }
  const services = new Set(r.services.filter((s) => s.impact !== "none").map((s) => s.id));
  for (const c of r.components) if (c.kind === "service") services.add(c.key.replace(/^service:/, ""));
  return { marks, clusters, clusterWide: false, services };
}

export function incidentOverlay(subjects: IncidentSubject[]): Overlay {
  const marks = new Map<string, Mark>();
  const clusters = new Map<string, Mark>();
  const services = new Set<string>();
  for (const s of subjects) {
    const m = statusMark(s.worst_status) ?? "degraded";
    if (s.type === "node") marks.set(`node:${s.id}`, m);
    if (s.type === "cluster") clusters.set(s.id, m);
    if (s.type === "service") services.add(s.id);
  }
  return { marks, clusters, clusterWide: true, services };
}

// clusterColors geeft elk cluster een vaste kleur, op naam gesorteerd.
const palette = ["#0284c7", "#7c3aed", "#059669", "#d97706", "#db2777", "#0d9488", "#4f46e5", "#65a30d", "#dc2626", "#475569"];

export function clusterColors(items: MapItem[]) {
  const names = new Map<string, string>();
  for (const it of items) if (it.cluster_id && it.cluster) names.set(it.cluster_id, it.cluster);
  const sorted = [...names].sort((a, b) => a[1].localeCompare(b[1]));
  return new Map(sorted.map(([id, name], i) => [id, { name, color: palette[i % palette.length] }]));
}
