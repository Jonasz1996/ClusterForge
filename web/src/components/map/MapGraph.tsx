"use client";

import dagre from "@dagrejs/dagre";
import {
  Background,
  Controls,
  Handle,
  MarkerType,
  Position,
  ReactFlow,
  ReactFlowProvider,
  useReactFlow,
  type Edge,
  type Node,
  type NodeProps,
} from "@xyflow/react";
import "@xyflow/react/dist/style.css";
import { useEffect, useMemo } from "react";
import { cx } from "@/components/ui";
import { markInfo, markOf, type MapEdge, type MapItem, type MapLayer, type Mark, type Overlay } from "@/lib/map";

// De kaart: Proxmox-hosts zijn vakken met hun VM's en lokale storage
// (fysiek) of hun bridges (netwerk). De fysieke laag staat in een vast
// raster, host naast host met de gedeelde storage eronder; de netwerklaag
// rekent dagre van links naar rechts: bridge en gateway, subnet, node. De
// VRRP-lijnen tellen niet mee voor de layout. Alle tekst gaat als
// React-tekst naar het scherm, nooit als HTML.

const CARD_W = 184;
const CARD_H = 50;
const HEADER = 34;
const PAD = 12;
const GAP = 12;
const HOST_GAP = 48;

const dots: Record<string, string> = {
  healthy: "bg-emerald-500",
  degraded: "bg-amber-500",
  down: "bg-red-600",
  split_brain: "bg-red-600",
  unknown: "bg-slate-400",
};

const kindStyle: Partial<Record<MapItem["kind"], string>> = {
  storage: "bg-amber-50/70 dark:bg-amber-950/30",
  subnet: "bg-sky-50 dark:bg-sky-950/40",
  gateway: "bg-violet-50 dark:bg-violet-950/40",
  vip: "bg-emerald-50 dark:bg-emerald-950/30",
  bridge: "bg-slate-50 dark:bg-slate-800",
};

type CardData = { it: MapItem; color?: string; mark?: Mark; dim: boolean; selected: boolean };
type CardNode = Node<CardData, "card">;
type HostNode = Node<CardData, "host">;

// Vier onzichtbare ingangen: links en rechts voor de netwerklaag, boven en
// onder voor schijven en VRRP.
function Handles() {
  const cls = "!size-1 !min-h-0 !min-w-0 !border-0 !bg-transparent";
  return (
    <>
      <Handle id="l" type="target" position={Position.Left} className={cls} />
      <Handle id="r" type="source" position={Position.Right} className={cls} />
      <Handle id="t" type="target" position={Position.Top} className={cls} />
      <Handle id="b" type="source" position={Position.Bottom} className={cls} />
      <Handle id="ts" type="source" position={Position.Top} className={cls} />
      <Handle id="bt" type="target" position={Position.Bottom} className={cls} />
    </>
  );
}

function title(it: MapItem, mark?: Mark) {
  return [it.name, it.detail, mark ? `wat-als: ${markInfo[mark].label}` : ""].filter(Boolean).join("\n");
}

function Card({ data }: NodeProps<CardNode>) {
  const { it, color, mark, dim, selected } = data;
  return (
    <div
      className={cx(
        "flex h-full w-full flex-col justify-center overflow-hidden rounded-md border border-slate-300 bg-white px-2.5 text-left shadow-xs transition-opacity dark:border-slate-700 dark:bg-slate-900",
        kindStyle[it.kind],
        it.kind === "subnet" && "rounded-full px-4",
        it.status === "down" && !mark && "border-red-300 dark:border-red-800",
        mark && markInfo[mark].ring,
        selected && "outline-2 outline-offset-2 outline-brand-500",
        dim && "opacity-25",
      )}
      style={color ? { borderLeftColor: color, borderLeftWidth: 4 } : undefined}
      title={title(it, mark)}
    >
      <Handles />
      <div className="flex min-w-0 items-center gap-1.5">
        <span className={cx("inline-block size-2 shrink-0 rounded-full", dots[it.status] ?? dots.unknown)} aria-hidden />
        <span className="truncate text-[13px] font-medium">{it.name}</span>
        {it.self && (
          <span
            className="ml-auto shrink-0 rounded bg-brand-600 px-1 text-[10px] font-semibold text-white"
            title="Hier draait ClusterForge"
          >
            CF
          </span>
        )}
      </div>
      <div className="truncate text-[11px] text-slate-500">{mark ? markInfo[mark].label : it.detail}</div>
    </div>
  );
}

function HostBox({ data }: NodeProps<HostNode>) {
  const { it, mark, dim, selected } = data;
  return (
    <div
      className={cx(
        "h-full w-full rounded-lg border border-slate-300 bg-slate-50/80 transition-opacity dark:border-slate-700 dark:bg-slate-800/40",
        it.status === "down" && "border-red-400 border-dashed",
        mark && markInfo[mark].ring,
        selected && "outline-2 outline-offset-2 outline-brand-500",
        dim && "opacity-40",
      )}
      title={title(it, mark)}
    >
      <div className="flex cursor-pointer items-center gap-1.5 px-3 py-2 text-xs font-semibold">
        <span className={cx("inline-block size-2 shrink-0 rounded-full", dots[it.status] ?? dots.unknown)} aria-hidden />
        <span className="shrink-0">{it.name}</span>
        <span className="truncate font-normal text-slate-500">{mark ? markInfo[mark].label : it.detail}</span>
      </div>
    </div>
  );
}

const nodeTypes = { card: Card, host: HostBox };

type Box = { x: number; y: number; w: number; h: number; parent?: string };
type Layout = Map<string, Box>;

const byName = (a: MapItem, b: MapItem) => a.name.localeCompare(b.name, "nl", { numeric: true });

// physicalLayout zet de hosts naast elkaar, met hun gasten in een raster en
// de lokale storage eronder; de gedeelde storage komt onder alle hosts.
function physicalLayout(items: MapItem[]): Layout {
  const out: Layout = new Map();
  const hosts = items.filter((i) => i.kind === "host").sort(byName);
  const known = new Set(hosts.map((h) => h.id));
  let x = 0;
  let maxH = 0;
  for (const h of hosts) {
    const kids = items.filter((i) => i.parent === h.id);
    const guests = kids.filter((i) => i.kind === "guest").sort(byName);
    const storage = kids.filter((i) => i.kind === "storage").sort(byName);
    const cols = Math.max(1, guests.length > 12 ? 3 : guests.length > 6 ? 2 : 1, Math.min(storage.length, 3));
    const place = (list: MapItem[], y0: number) => {
      list.forEach((it, i) => {
        const c = i % cols;
        const r = Math.floor(i / cols);
        out.set(it.id, { x: x + PAD + c * (CARD_W + GAP), y: y0 + r * (CARD_H + GAP), w: CARD_W, h: CARD_H, parent: h.id });
      });
      return list.length ? y0 + Math.ceil(list.length / cols) * (CARD_H + GAP) - GAP : y0 - GAP;
    };
    let y = place(guests, HEADER);
    if (storage.length) y = place(storage, y + GAP * 3);
    const w = PAD * 2 + cols * CARD_W + (cols - 1) * GAP;
    const hgt = Math.max(y + PAD, HEADER + CARD_H / 2);
    out.set(h.id, { x, y: 0, w, h: hgt });
    maxH = Math.max(maxH, hgt);
    x += w + HOST_GAP;
  }
  // Alles zonder host: de gedeelde storage, en gasten van een host die er
  // niet (meer) is, in een rij eronder.
  const loose = items.filter((i) => i.kind !== "host" && !known.has(i.parent)).sort(byName);
  const total = Math.max(x - HOST_GAP, 0);
  const rowW = loose.length * CARD_W + Math.max(loose.length - 1, 0) * GAP * 2;
  let lx = Math.max((total - rowW) / 2, 0);
  for (const it of loose) {
    out.set(it.id, { x: lx, y: maxH + 70, w: CARD_W, h: CARD_H });
    lx += CARD_W + GAP * 2;
  }
  return out;
}

// networkLayout rekent met dagre; hosts zijn samengestelde knopen.
function networkLayout(items: MapItem[], edges: MapEdge[]): Layout {
  const d = new dagre.graphlib.Graph({ compound: true });
  d.setGraph({ rankdir: "LR", nodesep: 22, ranksep: 90, marginx: 10, marginy: 10 });
  d.setDefaultEdgeLabel(() => ({}));
  const ids = new Set(items.map((i) => i.id));
  const parents = new Set(items.filter((i) => i.parent && ids.has(i.parent)).map((i) => i.parent));
  for (const it of items) {
    if (it.kind === "host") {
      if (parents.has(it.id)) d.setNode(it.id, {});
      continue;
    }
    d.setNode(it.id, { width: CARD_W, height: CARD_H });
    if (it.parent && parents.has(it.parent)) d.setParent(it.id, it.parent);
  }
  for (const e of edges) if (e.kind !== "vrrp" && d.hasNode(e.from) && d.hasNode(e.to)) d.setEdge(e.from, e.to);
  dagre.layout(d);
  const out: Layout = new Map();
  for (const id of d.nodes()) {
    const n = d.node(id);
    if (!n) continue;
    out.set(id, {
      x: n.x - n.width / 2,
      y: n.y - n.height / 2,
      w: n.width,
      h: n.height,
      parent: d.parent(id) as string | undefined,
    });
  }
  // Een host omsluit zijn bridges, met ruimte voor zijn kop; komt die over
  // een host erboven, dan schuift de host met zijn bridges omlaag.
  const groups = [...out].filter(([id]) => parents.has(id)).sort((a, b) => a[1].y - b[1].y);
  const placed: Box[] = [];
  for (const [id] of groups) {
    const kids = [...out.values()].filter((c) => c.parent === id);
    const x0 = Math.min(...kids.map((c) => c.x)) - PAD;
    const y0 = Math.min(...kids.map((c) => c.y)) - HEADER;
    const x1 = Math.max(...kids.map((c) => c.x + c.w)) + PAD;
    const y1 = Math.max(...kids.map((c) => c.y + c.h)) + PAD;
    const b = { x: x0, y: y0, w: x1 - x0, h: y1 - y0 };
    let shift = 0;
    for (const p of placed) {
      const overlapX = b.x < p.x + p.w && p.x < b.x + b.w;
      const below = p.y + p.h + GAP;
      if (overlapX && b.y + shift < below && b.y + shift + b.h > p.y) shift = below - b.y;
    }
    b.y += shift;
    out.set(id, b);
    placed.push(b);
    if (shift === 0) continue;
    for (const [cid, c] of out) if (c.parent === id) out.set(cid, { ...c, y: c.y + shift });
  }
  return out;
}

// topology is wat de layout bepaalt; een statuswissel laat de kaart staan.
function topology(m: MapLayer) {
  return m.layer + "|" + m.items.map((i) => i.id + ">" + i.parent).join(",") + "|" + m.edges.map((e) => e.id).join(",");
}

const edgeStyle: Record<MapEdge["kind"], { stroke: string; width: number; dash?: string; arrow?: boolean }> = {
  disk: { stroke: "#cbd5e1", width: 1 },
  member: { stroke: "#94a3b8", width: 1.25 },
  uplink: { stroke: "#475569", width: 1.75 },
  gateway: { stroke: "#7c3aed", width: 1.5 },
  owner: { stroke: "#059669", width: 2, arrow: true },
  vrrp: { stroke: "#64748b", width: 1.25, dash: "5 4" },
};

export type MapGraphProps = {
  map: MapLayer;
  colors: Map<string, { name: string; color: string }>;
  overlay: Overlay | null;
  selected: string | null;
  onSelect: (id: string | null) => void;
  fitKey?: string;
};

export default function MapGraph(props: MapGraphProps) {
  return (
    <ReactFlowProvider>
      <Inner {...props} />
    </ReactFlowProvider>
  );
}

function Inner({ map, colors, overlay, selected, onSelect, fitKey }: MapGraphProps) {
  const topo = topology(map);
  // De layout hangt alleen af van de topologie.
  const boxes = useMemo(
    () => (map.layer === "physical" ? physicalLayout(map.items) : networkLayout(map.items, map.edges)),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [topo],
  );
  const { fitView } = useReactFlow();
  useEffect(() => {
    const t = setTimeout(() => void fitView({ padding: 0.08, duration: 200 }), 30);
    return () => clearTimeout(t);
  }, [topo, fitKey, fitView]);

  const marks = useMemo(() => {
    const m = new Map<string, Mark | undefined>();
    if (overlay) for (const it of map.items) m.set(it.id, markOf(overlay, it));
    return m;
  }, [map.items, overlay]);

  const nodes = useMemo(() => {
    const out: (CardNode | HostNode)[] = [];
    // React Flow wil een host voor zijn kinderen.
    const ordered = [...map.items].sort((a, b) => Number(b.kind === "host") - Number(a.kind === "host"));
    for (const it of ordered) {
      const b = boxes.get(it.id);
      if (!b) continue;
      const mark = marks.get(it.id);
      const data: CardData = {
        it,
        mark,
        color: it.cluster_id ? colors.get(it.cluster_id)?.color : undefined,
        dim: !!overlay && !mark && it.kind !== "host",
        selected: it.id === selected,
      };
      const p = b.parent ? boxes.get(b.parent) : undefined;
      if (it.kind === "host") {
        out.push({
          id: it.id,
          type: "host",
          position: { x: b.x, y: b.y },
          style: { width: b.w, height: b.h },
          data,
          draggable: false,
          zIndex: 0,
        });
        continue;
      }
      out.push({
        id: it.id,
        type: "card",
        ...(p ? { parentId: b.parent } : {}),
        position: p ? { x: b.x - p.x, y: b.y - p.y } : { x: b.x, y: b.y },
        style: { width: b.w, height: b.h },
        data,
        draggable: false,
        zIndex: 1,
      });
    }
    return out;
  }, [map.items, boxes, marks, colors, overlay, selected]);

  const edges = useMemo<Edge[]>(() => {
    const physical = map.layer === "physical";
    return map.edges
      .filter((e) => boxes.has(e.from) && boxes.has(e.to))
      .map((e) => {
        const st = edgeStyle[e.kind];
        const touches = selected !== null && (e.from === selected || e.to === selected);
        const marked = !!overlay && !!marks.get(e.from) && !!marks.get(e.to);
        let handles = physical ? { sourceHandle: "b", targetHandle: "t" } : { sourceHandle: "r", targetHandle: "l" };
        if (e.kind === "vrrp") {
          const a = boxes.get(e.from)!;
          const b = boxes.get(e.to)!;
          handles = a.y <= b.y ? { sourceHandle: "b", targetHandle: "t" } : { sourceHandle: "ts", targetHandle: "bt" };
        }
        const stroke = touches ? "#0369a1" : marked ? "#dc2626" : st.stroke;
        return {
          id: e.id,
          source: e.from,
          target: e.to,
          ...handles,
          zIndex: physical ? 0 : 2,
          label: e.kind === "uplink" || e.kind === "vrrp" ? e.label || undefined : undefined,
          labelStyle: { fontSize: 10, fill: "#64748b" },
          labelBgStyle: { fillOpacity: 0.85 },
          markerEnd: st.arrow ? { type: MarkerType.ArrowClosed, color: stroke, width: 14, height: 14 } : undefined,
          style: {
            stroke,
            strokeWidth: touches ? st.width + 1 : st.width,
            strokeDasharray: st.dash,
            opacity: overlay && !marked && !touches ? 0.25 : 1,
          },
        };
      });
  }, [map.layer, map.edges, boxes, selected, overlay, marks]);

  return (
    <ReactFlow
      nodes={nodes}
      edges={edges}
      nodeTypes={nodeTypes}
      nodesDraggable={false}
      nodesConnectable={false}
      elementsSelectable
      onNodeClick={(_, n) => onSelect(n.id === selected ? null : n.id)}
      onPaneClick={() => onSelect(null)}
      minZoom={0.15}
      maxZoom={1.75}
      proOptions={{ hideAttribution: true }}
      colorMode="system"
      fitView
    >
      <Background gap={20} size={1} />
      <Controls showInteractive={false} />
    </ReactFlow>
  );
}
