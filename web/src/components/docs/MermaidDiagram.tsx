"use client";

import dagre from "@dagrejs/dagre";
import { useId, useMemo } from "react";

// MermaidDiagram tekent de Mermaid-flowcharts die de documentatie schrijft,
// als SVG met dagre. Het kent alleen dat deel van Mermaid: flowchart LR of
// TB, subgraphs, vier vormen en vier soorten lijnen. Wat het niet kent,
// toont het als code.

type Shape = "box" | "round" | "stadium" | "db";
type FlowNode = { id: string; lines: string[]; shape: Shape; group?: string };
type FlowGroup = { id: string; lines: string[] };
type FlowEdge = { from: string; to: string; arrow: boolean; dashed: boolean; label?: string };
export type Flow = { dir: "LR" | "TB"; nodes: FlowNode[]; groups: FlowGroup[]; edges: FlowEdge[] };

const shapeOf: Record<string, Shape> = { "[": "box", "(": "round", "([": "stadium", "[(": "db" };
const closeOf: Record<string, string> = { "[": "]", "(": ")", "([": "])", "[(": ")]" };
const nodeRe = /^(\w+)(\(\[|\[\(|\[|\()"([^"]*)"(\]\)|\)\]|\]|\))$/;
const edgeRe = /^(\w+)\s+(-->|-\.->|---|-\.-)(?:\|"([^"]*)"\|)?\s+(\w+)$/;
const groupRe = /^subgraph\s+(\w+)\["([^"]*)"\]$/;

// label leest een label zoals de documentatie het schrijft: regels gescheiden
// door <br/> en tekens als entiteit, zoals #quot; en #35;.
function label(s: string): string[] {
  const named: Record<string, string> = { quot: '"', amp: "&", lt: "<", gt: ">" };
  return s
    .split("<br/>")
    .map((l) => l.replace(/#(\w+);/g, (m, e: string) => (/^\d+$/.test(e) ? String.fromCharCode(Number(e)) : (named[e] ?? m))));
}

export function parseFlow(src: string): Flow | null {
  const lines = src
    .split("\n")
    .map((l) => l.trim())
    .filter((l) => l !== "" && !l.startsWith("%%"));
  const head = /^flowchart\s+(LR|RL|TB|TD|BT)$/.exec(lines[0] ?? "");
  if (!head) return null;
  const flow: Flow = { dir: head[1] === "LR" || head[1] === "RL" ? "LR" : "TB", nodes: [], groups: [], edges: [] };
  const known = new Set<string>();
  let group: string | undefined;
  for (const line of lines.slice(1)) {
    let m: RegExpExecArray | null;
    if ((m = groupRe.exec(line))) {
      if (group) return null;
      group = m[1];
      flow.groups.push({ id: m[1], lines: label(m[2]) });
    } else if (line === "end") {
      if (!group) return null;
      group = undefined;
    } else if ((m = nodeRe.exec(line))) {
      if (closeOf[m[2]] !== m[4] || known.has(m[1])) return null;
      known.add(m[1]);
      flow.nodes.push({ id: m[1], lines: label(m[3]), shape: shapeOf[m[2]], group });
    } else if ((m = edgeRe.exec(line))) {
      flow.edges.push({
        from: m[1],
        to: m[4],
        arrow: m[2].endsWith(">"),
        dashed: m[2].includes("."),
        label: m[3] !== undefined ? label(m[3]).join(" ") : undefined,
      });
    } else {
      return null;
    }
  }
  if (group || flow.edges.some((e) => !known.has(e.from) || !known.has(e.to))) return null;
  return flow;
}

type Box = { x: number; y: number; w: number; h: number };

const LINE = 16;
const PAD = 12;
const HEADER = 18;

// textWidth schat de breedte van tekst in px; genoeg om een vak te maken.
function textWidth(s: string, size: number) {
  return s.length * size * 0.58;
}

function layout(f: Flow) {
  const d = new dagre.graphlib.Graph({ compound: true, multigraph: true });
  d.setGraph({ rankdir: f.dir, nodesep: 28, ranksep: f.dir === "LR" ? 70 : 50, marginx: 8, marginy: 8 });
  d.setDefaultEdgeLabel(() => ({}));
  const used = new Set(f.nodes.map((n) => n.group).filter(Boolean));
  for (const g of f.groups) if (used.has(g.id)) d.setNode(g.id, {});
  for (const n of f.nodes) {
    const w = Math.max(...n.lines.map((l, i) => textWidth(l, i === 0 ? 13 : 12))) + (n.shape === "stadium" ? 36 : 24);
    d.setNode(n.id, { width: Math.min(Math.max(w, 70), 320), height: n.lines.length * LINE + (n.shape === "db" ? 24 : 14) });
    if (n.group && used.has(n.group)) d.setParent(n.id, n.group);
  }
  f.edges.forEach((e, i) => {
    if (e.from !== e.to) d.setEdge(e.from, e.to, {}, "e" + i);
  });
  dagre.layout(d);
  const boxes = new Map<string, Box>();
  for (const n of f.nodes) {
    const b = d.node(n.id);
    boxes.set(n.id, { x: b.x - b.width / 2, y: b.y - b.height / 2, w: b.width, h: b.height });
  }
  // Een vak omsluit zijn knopen met ruimte voor zijn titel; komt het over
  // een vak erboven, dan schuift het met zijn knopen omlaag.
  const groups = new Map<string, Box>();
  const placed: Box[] = [];
  const order = f.groups
    .filter((g) => used.has(g.id))
    .map((g) => {
      const kids = f.nodes.filter((n) => n.group === g.id).map((n) => boxes.get(n.id)!);
      const head = HEADER + (g.lines.length - 1) * 14;
      const x0 = Math.min(...kids.map((k) => k.x)) - PAD;
      const y0 = Math.min(...kids.map((k) => k.y)) - PAD - head;
      const x1 = Math.max(...kids.map((k) => k.x + k.w)) + PAD;
      const y1 = Math.max(...kids.map((k) => k.y + k.h)) + PAD;
      return { g, b: { x: x0, y: y0, w: Math.max(x1 - x0, ...g.lines.map((l) => textWidth(l, 12) + 2 * PAD)), h: y1 - y0 } };
    })
    .sort((a, b) => a.b.y - b.b.y);
  for (const { g, b } of order) {
    let shift = 0;
    for (const p of placed) {
      const overlapX = b.x < p.x + p.w && p.x < b.x + b.w;
      if (overlapX && b.y + shift < p.y + p.h + 10 && b.y + shift + b.h > p.y) shift = p.y + p.h + 10 - b.y;
    }
    b.y += shift;
    groups.set(g.id, b);
    placed.push(b);
    if (shift === 0) continue;
    for (const n of f.nodes) if (n.group === g.id) boxes.get(n.id)!.y += shift;
  }
  // Alles naar de oorsprong.
  const all = [...boxes.values(), ...groups.values()];
  const minX = Math.min(...all.map((b) => b.x)) - 4;
  const minY = Math.min(...all.map((b) => b.y)) - 4;
  for (const b of all) {
    b.x -= minX;
    b.y -= minY;
  }
  const width = Math.max(...all.map((b) => b.x + b.w)) + 4;
  const height = Math.max(...all.map((b) => b.y + b.h)) + 4;
  return { boxes, groups, width, height };
}

// clip geeft het punt waar de lijn van het midden van b naar (tx, ty) de rand
// van b raakt.
function clip(b: Box, tx: number, ty: number): [number, number] {
  const cx = b.x + b.w / 2;
  const cy = b.y + b.h / 2;
  const dx = tx - cx;
  const dy = ty - cy;
  if (dx === 0 && dy === 0) return [cx, cy];
  const s = Math.min(dx === 0 ? Infinity : b.w / 2 / Math.abs(dx), dy === 0 ? Infinity : b.h / 2 / Math.abs(dy));
  return [cx + dx * s, cy + dy * s];
}

function NodeShape({ n, b }: { n: FlowNode; b: Box }) {
  const cls = "fill-white stroke-slate-400 dark:fill-slate-900 dark:stroke-slate-500";
  const top = n.shape === "db" ? 10 : 0;
  const y0 = b.y + top + (b.h - top - n.lines.length * LINE) / 2 + 12;
  return (
    <g>
      {n.shape === "db" ? (
        <>
          <path
            d={`M${b.x},${b.y + 6} v${b.h - 12} a${b.w / 2},6 0 0 0 ${b.w},0 v${-(b.h - 12)}`}
            className={cls}
            strokeWidth={1.25}
          />
          <ellipse cx={b.x + b.w / 2} cy={b.y + 6} rx={b.w / 2} ry={6} className={cls} strokeWidth={1.25} />
        </>
      ) : (
        <rect
          x={b.x}
          y={b.y}
          width={b.w}
          height={b.h}
          rx={n.shape === "stadium" ? b.h / 2 : n.shape === "round" ? 12 : 4}
          className={cls}
          strokeWidth={1.25}
        />
      )}
      {n.lines.map((l, i) => (
        <text
          key={i}
          x={b.x + b.w / 2}
          y={y0 + i * LINE}
          textAnchor="middle"
          className={i === 0 ? "fill-slate-900 dark:fill-slate-100" : "fill-slate-500 dark:fill-slate-400"}
          fontSize={i === 0 ? 13 : 12}
          fontWeight={i === 0 ? 600 : 400}
        >
          {l}
        </text>
      ))}
    </g>
  );
}

export function MermaidDiagram({ source }: { source: string }) {
  const arrow = "cf-arrow-" + useId().replace(/:/g, "");
  const flow = useMemo(() => parseFlow(source), [source]);
  const lay = useMemo(() => (flow && flow.nodes.length > 0 ? layout(flow) : null), [flow]);
  if (!flow || !lay) {
    return (
      <pre className="overflow-x-auto rounded-md bg-slate-100 px-3 py-2 font-mono text-xs dark:bg-slate-800">
        <code>{source}</code>
      </pre>
    );
  }
  const title = flow.groups.length > 0 ? flow.groups[0].lines[0] : flow.nodes[0].lines[0];
  return (
    <div className="overflow-x-auto rounded-md border border-slate-200 bg-slate-50/60 p-2 dark:border-slate-800 dark:bg-slate-950/40">
      <svg
        role="img"
        aria-label={"Diagram: " + title}
        width={lay.width}
        height={lay.height}
        viewBox={`0 0 ${lay.width} ${lay.height}`}
        className="mx-auto block h-auto max-w-full"
        fontFamily="ui-sans-serif, system-ui, sans-serif"
      >
        <defs>
          <marker id={arrow} viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse">
            <path d="M0,0 L10,5 L0,10 z" className="fill-slate-500 dark:fill-slate-400" />
          </marker>
        </defs>
        {flow.groups.map((g) => {
          const b = lay.groups.get(g.id);
          return (
            b && (
              <rect
                key={g.id}
                x={b.x}
                y={b.y}
                width={b.w}
                height={b.h}
                rx={8}
                className="fill-slate-100/70 stroke-slate-300 dark:fill-slate-800/50 dark:stroke-slate-700"
              />
            )
          );
        })}
        {flow.edges.map((e, i) => {
          const a = lay.boxes.get(e.from)!;
          const b = lay.boxes.get(e.to)!;
          if (e.from === e.to) return null;
          const [x1, y1] = clip(a, b.x + b.w / 2, b.y + b.h / 2);
          const [x2, y2] = clip(b, a.x + a.w / 2, a.y + a.h / 2);
          const mx = (x1 + x2) / 2;
          const my = (y1 + y2) / 2;
          const lw = e.label ? textWidth(e.label, 11) + 8 : 0;
          return (
            <g key={i}>
              <line
                x1={x1}
                y1={y1}
                x2={x2}
                y2={y2}
                className="stroke-slate-500 dark:stroke-slate-400"
                strokeWidth={1.25}
                strokeDasharray={e.dashed ? "5 4" : undefined}
                markerEnd={e.arrow ? `url(#${arrow})` : undefined}
              />
              {e.label && (
                <>
                  <rect x={mx - lw / 2} y={my - 8} width={lw} height={16} rx={3} className="fill-white dark:fill-slate-900" />
                  <text x={mx} y={my + 4} textAnchor="middle" fontSize={11} className="fill-slate-600 dark:fill-slate-300">
                    {e.label}
                  </text>
                </>
              )}
            </g>
          );
        })}
        {/* De titels van de vakken over de lijnen, met een rand in de kleur van het vak. */}
        {flow.groups.map((g) => {
          const b = lay.groups.get(g.id);
          if (!b) return null;
          return g.lines.map((l, i) => (
            <text
              key={g.id + i}
              x={b.x + PAD}
              y={b.y + 15 + i * 14}
              fontSize={i === 0 ? 12 : 11}
              fontWeight={i === 0 ? 600 : 400}
              paintOrder="stroke"
              strokeWidth={4}
              strokeLinejoin="round"
              className={
                (i === 0 ? "fill-slate-700 dark:fill-slate-200" : "fill-slate-500 dark:fill-slate-400") +
                " stroke-slate-100 dark:stroke-slate-800"
              }
            >
              {l}
            </text>
          ));
        })}
        {flow.nodes.map((n) => (
          <NodeShape key={n.id} n={n} b={lay.boxes.get(n.id)!} />
        ))}
      </svg>
    </div>
  );
}

export default MermaidDiagram;
