"use client";

import Link from "next/link";
import { cx } from "@/components/ui";
import { levelLabel, useSoc, type SocLevel } from "@/lib/soc";

export const levelStyle: Record<SocLevel, { ring: string; dot: string; text: string; bg: string }> = {
  kritiek: { ring: "border-red-300 dark:border-red-900", dot: "bg-red-500", text: "text-red-700 dark:text-red-300", bg: "bg-red-50 dark:bg-red-950/40" },
  verhoogd: {
    ring: "border-amber-300 dark:border-amber-900",
    dot: "bg-amber-500",
    text: "text-amber-800 dark:text-amber-300",
    bg: "bg-amber-50 dark:bg-amber-950/40",
  },
  waakzaam: {
    ring: "border-yellow-200 dark:border-yellow-900/60",
    dot: "bg-yellow-400",
    text: "text-yellow-800 dark:text-yellow-200",
    bg: "bg-yellow-50/60 dark:bg-yellow-950/20",
  },
  normaal: {
    ring: "border-emerald-200 dark:border-emerald-900",
    dot: "bg-emerald-500",
    text: "text-emerald-700 dark:text-emerald-300",
    bg: "bg-emerald-50/60 dark:bg-emerald-950/30",
  },
};

const nf = new Intl.NumberFormat("nl-BE");

// SocStrip is de beveiliging in één regel op het overzicht: niveau, de
// reden, de score en de kerncijfers van de laatste 24 uur, met een link naar
// het SOC. Geen gegevens (of geen rechten): niets tonen.
export function SocStrip() {
  const q = useSoc("24h");
  const o = q.data;
  if (!o) return null;
  const l = o.level.level;
  const st = levelStyle[l];
  const s = o.stats;
  const facts: [string, string][] = [
    ["Score", `${o.score.score}/100`],
    ["Aanvallen 24 u", nf.format(s.attacks)],
    ["Bans", nf.format(s.active_bans)],
    ["Beschermd", `${s.protected}/${s.nodes}`],
  ];
  return (
    <Link
      href="/soc"
      aria-label={`Beveiliging: dreigingsniveau ${levelLabel[l]}, naar het SOC`}
      className={cx("group relative flex flex-wrap items-center gap-x-5 gap-y-2 rounded-lg border px-4 py-3 transition hover:shadow-sm", st.ring, st.bg)}
    >
      <span className={cx("inline-flex items-center gap-2 text-sm font-semibold", st.text)}>
        <span className={cx("size-2.5 rounded-full", st.dot)} aria-hidden />
        Beveiliging: {levelLabel[l].toLowerCase()}
      </span>
      <span className="w-full min-w-0 truncate text-sm text-slate-600 sm:w-auto sm:flex-1 dark:text-slate-400">
        {o.level.reasons.join(" · ")}
      </span>
      <dl className="flex flex-wrap gap-x-4 gap-y-1 text-sm">
        {facts.map(([k, v]) => (
          <div key={k} className="flex items-baseline gap-1.5">
            <dt className="text-xs text-slate-500">{k}</dt>
            <dd className="font-semibold tabular-nums text-slate-800 dark:text-slate-100">{v}</dd>
          </div>
        ))}
      </dl>
      <span aria-hidden className="absolute top-3 right-4 text-slate-400 sm:static group-hover:text-slate-600 dark:group-hover:text-slate-200">
        ›
      </span>
    </Link>
  );
}
