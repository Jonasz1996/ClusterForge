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
import { envInfo } from "@/lib/inventory";
import { kindLabel, serviceStatuses, topology, visibleEdges, type DepGraph, type DepGroup, type DepService } from "@/lib/deps";

// De graaf: clusters zijn vakken, diensten kaartjes, en een pijl loopt van
// afnemer naar leverancier, van links naar rechts. De layout wordt alleen
// opnieuw berekend als de topologie verandert, zodat een statuswissel de
// graaf niet laat verspringen. Alle tekst gaat als React-tekst naar het
// scherm, nooit als HTML.

const SERVICE_W = 176;
const SERVICE_H = 48;
const GROUP_HEADER = 30;
const GROUP_GAP = 18;
const GROUP_W = 220;
const GROUP_H = 64;

type ServiceData = { s: DepService; dim: boolean; selected: boolean };
type GroupData = { g: DepGroup; dim: boolean; collapsed: boolean };
type ServiceNode = Node<ServiceData, "service">;
// Niet "group": dat type krijgt in React Flow een eigen rand en opvulling.
type GroupNode = Node<GroupData, "cluster">;

function ringOf(impact: string, status: string) {
  if (impact === "down") return "border-red-500 border-2";
  if (impact === "degraded") return "border-amber-500 border-2";
  if (status === "down") return "border-red-300 dark:border-red-800";
  return "border-slate-300 dark:border-slate-700";
}

function ServiceCard({ data }: NodeProps<ServiceNode>) {
  const { s, dim, selected } = data;
  const st = serviceStatuses[s.status];
  return (
    <div
      className={cx(
        "flex h-full w-full flex-col justify-center rounded-md border bg-white px-2.5 text-left shadow-xs transition-opacity dark:bg-slate-900",
        ringOf(s.impact, s.status),
        s.state === "suggested" && "border-dashed",
        selected && "ring-2 ring-brand-500",
        dim && "opacity-25",
      )}
      title={[st.label + (s.status_reason ? `: ${s.status_reason}` : ""), s.impact_reason].filter(Boolean).join("\n")}
    >
      <Handle type="target" position={Position.Left} className="!size-1.5 !border-0 !bg-slate-400" />
      <div className="flex min-w-0 items-center gap-1.5">
        <span className={cx("inline-block size-2 shrink-0 rounded-full", st.dot)} aria-hidden />
        <span className="truncate text-[13px] font-medium">{s.name}</span>
      </div>
      <div className="truncate text-[11px] text-slate-500">
        {kindLabel(s.kind)}
        {s.state === "suggested" && " · voorstel"}
      </div>
      <Handle type="source" position={Position.Right} className="!size-1.5 !border-0 !bg-slate-400" />
    </div>
  );
}

function GroupBox({ data }: NodeProps<GroupNode>) {
  const { g, dim, collapsed } = data;
  const st = serviceStatuses[g.status === "split_brain" ? "down" : (g.status as DepService["status"])] ?? serviceStatuses.unknown;
  const env = g.environment ? envInfo(g.environment) : null;
  return (
    <div
      className={cx(
        "h-full w-full rounded-lg border bg-slate-50/80 transition-opacity dark:bg-slate-800/40",
        g.impact === "down" ? "border-red-500 border-2" : g.impact === "degraded" ? "border-amber-500 border-2" : "border-slate-300 dark:border-slate-700",
        g.kind === "external" && "border-dashed",
        dim && "opacity-30",
      )}
      title={[g.status_reason, g.impacted_by.length ? `geraakt door ${g.impacted_by.join(", ")}` : ""].filter(Boolean).join("\n") || undefined}
    >
      {collapsed && <Handle type="target" position={Position.Left} className="!size-1.5 !border-0 !bg-slate-400" />}
      <div className="flex items-center gap-1.5 px-2.5 py-1.5 text-xs font-semibold">
        <span className={cx("inline-block size-2 shrink-0 rounded-full", st.dot)} aria-hidden />
        <span className="truncate">{g.name}</span>
        {env && (
          <span
            className={cx(
              "rounded px-1 py-px text-[10px] font-medium",
              env.tone === "red" && "bg-red-50 text-red-700 dark:bg-red-950 dark:text-red-300",
              env.tone === "amber" && "bg-amber-50 text-amber-800 dark:bg-amber-950 dark:text-amber-300",
              env.tone === "green" && "bg-emerald-50 text-emerald-700 dark:bg-emerald-950 dark:text-emerald-300",
            )}
          >
            {env.label}
          </span>
        )}
      </div>
      {collapsed && g.impacted_by.length > 0 && (
        <div className="truncate px-2.5 text-[11px] text-red-700 dark:text-red-300">geraakt door {g.impacted_by.join(", ")}</div>
      )}
      {collapsed && <Handle type="source" position={Position.Right} className="!size-1.5 !border-0 !bg-slate-400" />}
    </div>
  );
}

const nodeTypes = { service: ServiceCard, cluster: GroupBox };

type Layout = Map<string, { x: number; y: number; w: number; h: number; parent?: string }>;

// layout rekent met dagre posities uit; clusters zijn samengestelde knopen.
function layout(g: DepGraph, internal: boolean): Layout {
  const d = new dagre.graphlib.Graph({ compound: true });
  d.setGraph({ rankdir: "LR", nodesep: 30, ranksep: 72, marginx: 10, marginy: 10 });
  d.setDefaultEdgeLabel(() => ({}));
  const out: Layout = new Map();
  if (g.level === "cluster") {
    for (const gr of g.groups) d.setNode(gr.id, { width: GROUP_W, height: GROUP_H });
  } else {
    const used = new Set(g.services.map((s) => s.group_id));
    for (const gr of g.groups) if (used.has(gr.id)) d.setNode(gr.id, {});
    for (const s of g.services) {
      d.setNode(s.id, { width: SERVICE_W, height: SERVICE_H });
      d.setParent(s.id, s.group_id);
    }
  }
  for (const e of visibleEdges(g, internal)) d.setEdge(e.from, e.to);
  dagre.layout(d);
  for (const id of d.nodes()) {
    const n = d.node(id);
    if (!n) continue;
    out.set(id, { x: n.x - n.width / 2, y: n.y - n.height / 2, w: n.width, h: n.height, parent: d.parent(id) as string | undefined });
  }
  if (g.level === "cluster") return out;
  // Ruimte voor de kop van elk cluster. Komt een kop daardoor over een
  // cluster erboven, dan schuift het cluster met zijn diensten omlaag.
  const groups = [...out].filter(([, b]) => !b.parent).sort((a, b) => a[1].y - b[1].y);
  const placed: { x: number; y: number; w: number; h: number }[] = [];
  for (const [id, box] of groups) {
    const b = { ...box, y: box.y - GROUP_HEADER, h: box.h + GROUP_HEADER };
    let shift = 0;
    for (const p of placed) {
      const overlapX = b.x < p.x + p.w && p.x < b.x + b.w;
      const below = p.y + p.h + GROUP_GAP;
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

export type GraphProps = {
  graph: DepGraph;
  internal: boolean;
  selected: string | null;
  // highlight zijn de diensten (of groepen) die geraakt worden; null dimt niets.
  highlight: Set<string> | null;
  onSelect: (id: string | null) => void;
  // fitKey: bij een andere waarde past de graaf zich opnieuw in, zoals
  // wanneer het zijpaneel opengaat.
  fitKey?: string;
};

export default function DepGraphView(props: GraphProps) {
  return (
    <ReactFlowProvider>
      <Inner {...props} />
    </ReactFlowProvider>
  );
}

function Inner({ graph, internal, selected, highlight, onSelect, fitKey }: GraphProps) {
  const topo = topology(graph) + (internal ? "|i" : "");
  // De layout hangt alleen af van de topologie.
  // eslint-disable-next-line react-hooks/exhaustive-deps
  const boxes = useMemo(() => layout(graph, internal), [topo]);
  const { fitView } = useReactFlow();
  useEffect(() => {
    const t = setTimeout(() => void fitView({ padding: 0.1, duration: 200 }), 30);
    return () => clearTimeout(t);
  }, [topo, fitKey, fitView]);

  const nodes = useMemo(() => {
    const out: (ServiceNode | GroupNode)[] = [];
    const collapsed = graph.level === "cluster";
    for (const g of graph.groups) {
      const b = boxes.get(g.id);
      if (!b) continue;
      out.push({
        id: g.id,
        type: "cluster",
        position: { x: b.x, y: b.y },
        style: { width: b.w, height: b.h },
        data: { g, collapsed, dim: !!highlight && (collapsed ? !highlight.has(g.id) : false) },
        selectable: collapsed,
        draggable: false,
        zIndex: 0,
      });
    }
    if (!collapsed) {
      for (const s of graph.services) {
        const b = boxes.get(s.id);
        const p = boxes.get(s.group_id);
        if (!b || !p) continue;
        out.push({
          id: s.id,
          type: "service",
          parentId: s.group_id,
          position: { x: b.x - p.x, y: b.y - p.y },
          style: { width: SERVICE_W, height: SERVICE_H },
          data: { s, selected: s.id === selected, dim: !!highlight && !highlight.has(s.id) },
          draggable: false,
          zIndex: 1,
        });
      }
    }
    return out;
  }, [graph, boxes, selected, highlight]);

  const edges = useMemo<Edge[]>(
    () =>
      visibleEdges(graph, internal).map((e) => {
        const color = e.affected ? "#dc2626" : e.strength === "soft" ? "#94a3b8" : "#475569";
        const dim = !!highlight && !(highlight.has(e.from) && highlight.has(e.to));
        return {
          id: e.id,
          source: e.from,
          target: e.to,
          zIndex: 2,
          animated: false,
          label: graph.level === "cluster" && e.count > 1 ? String(e.count) : undefined,
          markerEnd: { type: MarkerType.ArrowClosed, color, width: 16, height: 16 },
          style: {
            stroke: color,
            strokeWidth: e.affected ? 2.25 : e.strength === "soft" ? 1 : 1.75,
            strokeDasharray: e.state === "suggested" ? "4 4" : undefined,
            opacity: dim ? 0.15 : 1,
          },
        };
      }),
    [graph, internal, highlight],
  );

  return (
    <ReactFlow
      nodes={nodes}
      edges={edges}
      nodeTypes={nodeTypes}
      nodesDraggable={false}
      nodesConnectable={false}
      elementsSelectable
      onNodeClick={(_, n) => {
        if (n.type === "service" || graph.level === "cluster") onSelect(n.id);
      }}
      onPaneClick={() => onSelect(null)}
      minZoom={0.2}
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
