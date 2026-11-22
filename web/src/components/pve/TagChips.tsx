"use client";

import { usePveGet, tagColors } from "@/lib/pve";

// useTagColors leest de kleuren van tags uit de datacenter-opties.
export function useTagColors(dc: string) {
  const opts = usePveGet<{ "tag-style"?: string }>(dc, "/cluster/options", { refetch: 60_000 });
  return tagColors(opts.data?.["tag-style"]);
}

// Leesbare tekstkleur op een achtergrond.
function fg(hex: string) {
  const n = parseInt(hex.slice(1), 16);
  const l = (0.299 * ((n >> 16) & 255) + 0.587 * ((n >> 8) & 255) + 0.114 * (n & 255)) / 255;
  return l > 0.6 ? "#0f172a" : "#ffffff";
}

export function TagChips({ tags, colors }: { tags: string[]; colors: Record<string, string> }) {
  if (tags.length === 0) return null;
  return (
    <span className="inline-flex flex-wrap gap-1">
      {tags.map((t) => {
        const bg = colors[t];
        return (
          <span
            key={t}
            className={
              bg
                ? "rounded px-1.5 py-0.5 text-xs font-medium"
                : "rounded bg-slate-100 px-1.5 py-0.5 text-xs font-medium text-slate-700 dark:bg-slate-800 dark:text-slate-200"
            }
            style={bg ? { background: bg, color: fg(bg) } : undefined}
          >
            {t}
          </span>
        );
      })}
    </span>
  );
}
