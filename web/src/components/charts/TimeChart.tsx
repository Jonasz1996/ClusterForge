"use client";

import { useEffect, useMemo, useRef } from "react";
import uPlot from "uplot";
import "uplot/dist/uPlot.min.css";
import { formatBytes } from "@/components/inventory/bits";
import type { MetricPanel } from "@/lib/inventory";

type Unit = MetricPanel["unit"];
type Series = MetricPanel["series"][number];

const palette = ["#2563eb", "#16a34a", "#d97706", "#dc2626", "#7c3aed", "#0891b2", "#db2777", "#65a30d", "#475569", "#ea580c"];

const timeFmt = new Intl.DateTimeFormat("nl-BE", { hour: "2-digit", minute: "2-digit" });
const dayFmt = new Intl.DateTimeFormat("nl-BE", { day: "numeric", month: "short" });
const fullFmt = new Intl.DateTimeFormat("nl-BE", { dateStyle: "short", timeStyle: "short" });

// timeTicks maakt de labels van de tijdas: uren binnen een dag, anders de
// datum, met de datum erbij waar een nieuwe dag begint.
function timeTicks(_u: uPlot, splits: number[]) {
  if (splits.length === 0) return [];
  const span = splits[splits.length - 1] - splits[0];
  let prevDay = "";
  return splits.map((ts) => {
    const d = new Date(ts * 1000);
    const day = dayFmt.format(d);
    if (span > 2 * 86400) return day;
    const label = day !== prevDay && prevDay !== "" ? `${timeFmt.format(d)}\n${day}` : timeFmt.format(d);
    prevDay = day;
    return label;
  });
}

export function formatValue(v: number, unit: Unit) {
  switch (unit) {
    case "percent":
      return `${v.toLocaleString("nl-BE", { maximumFractionDigits: v < 10 ? 1 : 0 })} %`;
    case "bytes_per_second":
      return `${formatBytes(v)}/s`;
    case "celsius":
      return `${v.toLocaleString("nl-BE", { maximumFractionDigits: 0 })} °C`;
    default:
      return v.toLocaleString("nl-BE", { maximumFractionDigits: 2 });
  }
}

function isDark() {
  return typeof window !== "undefined" && window.matchMedia("(prefers-color-scheme: dark)").matches;
}

// TimeChart tekent lijnen op een gedeelde tijdas met uPlot. De grafiek wordt
// alleen opnieuw opgebouwd als de lijnen veranderen; de data komt erin met
// setData, zodat verversen de muispositie niet kwijtraakt.
export function TimeChart({
  timestamps,
  series,
  unit,
  height = 180,
}: {
  timestamps: number[];
  series: Series[];
  unit: Unit;
  height?: number;
}) {
  const ref = useRef<HTMLDivElement>(null);
  const plot = useRef<uPlot | null>(null);
  const shape = `${unit}|${series.map((s) => s.label).join("|")}`;
  const data = useMemo<uPlot.AlignedData>(() => [timestamps, ...series.map((s) => s.values)], [timestamps, series]);

  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    const dark = isDark();
    const axis = {
      stroke: dark ? "#94a3b8" : "#64748b",
      grid: { stroke: dark ? "#1e293b" : "#e2e8f0", width: 1 },
      ticks: { stroke: dark ? "#1e293b" : "#e2e8f0", width: 1 },
    };
    const labels = shape.split("|").slice(1);
    const opts: uPlot.Options = {
      width: el.clientWidth,
      height,
      cursor: { drag: { x: false, y: false } },
      legend: { live: true },
      scales: {
        y: unit === "percent" ? { range: [0, 100] } : { range: (_u, _min, max) => [0, max > 0 ? max * 1.1 : 1] },
      },
      series: [
        { label: "Tijd", value: (_u, v) => (v == null ? "" : fullFmt.format(new Date(v * 1000))) },
        ...labels.map((label, i) => ({
          label,
          stroke: palette[i % palette.length],
          width: 1.5,
          value: (_u: uPlot, v: number | null) => (v == null ? "–" : formatValue(v, unit)),
        })),
      ],
      axes: [
        { ...axis, values: timeTicks },
        { ...axis, size: 70, values: (_u, vals) => vals.map((v) => formatValue(v, unit)) },
      ],
    };
    const u = new uPlot(opts, [[], ...labels.map(() => [])], el);
    plot.current = u;
    const ro = new ResizeObserver(() => u.setSize({ width: el.clientWidth, height }));
    ro.observe(el);
    return () => {
      ro.disconnect();
      u.destroy();
      plot.current = null;
    };
  }, [shape, unit, height]);

  useEffect(() => {
    plot.current?.setData(data);
  }, [data, shape]);

  return <div ref={ref} className="cf-chart w-full" />;
}
