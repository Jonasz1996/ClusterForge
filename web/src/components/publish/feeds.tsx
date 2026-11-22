"use client";

import type { ReactNode } from "react";
import { ago } from "@/components/publish/bits";
import { Alert } from "@/components/ui";
import type { PublishFeed, TrafficWindow } from "@/lib/publish";

export const windows: { v: TrafficWindow; label: string }[] = [
  { v: "1h", label: "1 uur" },
  { v: "24h", label: "24 uur" },
  { v: "7d", label: "7 dagen" },
];

export function WindowPicker({ value, onChange }: { value: TrafficWindow; onChange: (w: TrafficWindow) => void }) {
  return (
    <div role="group" aria-label="Venster" className="inline-flex overflow-hidden rounded-md border border-slate-300 text-sm dark:border-slate-700">
      {windows.map((w) => (
        <button
          key={w.v}
          type="button"
          aria-pressed={value === w.v}
          onClick={() => onChange(w.v)}
          className={
            value === w.v
              ? "bg-brand-600 px-3 py-1.5 text-white"
              : "bg-white px-3 py-1.5 text-slate-700 hover:bg-slate-50 dark:bg-slate-900 dark:text-slate-200 dark:hover:bg-slate-800"
          }
        >
          {w.label}
        </button>
      ))}
    </div>
  );
}

export function Stat({ label, value, tone, hint }: { label: string; value: ReactNode; tone?: "red" | "amber" | "green"; hint?: string }) {
  const color =
    tone === "red"
      ? "text-red-700 dark:text-red-400"
      : tone === "amber"
        ? "text-amber-700 dark:text-amber-300"
        : tone === "green"
          ? "text-green-700 dark:text-green-400"
          : "";
  return (
    <div className="rounded-lg border border-slate-200 bg-white p-4 dark:border-slate-800 dark:bg-slate-900">
      <div className="text-xs text-slate-500">{label}</div>
      <div className={`mt-1 text-2xl font-semibold ${color}`}>{value}</div>
      {hint && <div className="mt-1 text-xs text-slate-500">{hint}</div>}
    </div>
  );
}

export function FeedLine({ feed, name }: { feed?: PublishFeed; name: string }) {
  if (!feed) return <p className="text-xs text-slate-500">{name}: nog niet gelezen. ClusterForge leest elke minuut.</p>;
  return (
    <div className="space-y-2">
      <p className="text-xs text-slate-500">
        {name}
        {feed.detail ? ` via ${feed.detail}` : ""}: laatst gelukt {ago(feed.ok_at)}, {feed.lines.toLocaleString("nl-BE")} regels sinds de start.
      </p>
      {feed.error && (
        <Alert>
          {name}: {feed.error}
        </Alert>
      )}
    </div>
  );
}

export const n = (v: number) => v.toLocaleString("nl-BE");

export const when = new Intl.DateTimeFormat("nl-BE", { dateStyle: "short", timeStyle: "short" });
