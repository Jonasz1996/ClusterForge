"use client";

import { useRef, type KeyboardEvent, type ReactNode } from "react";
import { cx } from "@/components/ui";

export type TabItem<K extends string> = { key: K; label: ReactNode; title?: string };

// Tabs is de tabbladbalk van ClusterForge: overal dezelfde kleuren (ook in
// het donker), met de toetsen ← → Home End te bedienen, en op een smal
// scherm één rij die horizontaal schuift in plaats van te breken; het
// gekozen tabblad schuift dan vanzelf in beeld.
export function Tabs<K extends string>({
  tabs,
  value,
  onChange,
  label = "Tabbladen",
  className,
}: {
  tabs: readonly TabItem<K>[];
  value: K;
  onChange: (key: K) => void;
  label?: string;
  className?: string;
}) {
  const refs = useRef(new Map<K, HTMLButtonElement>());
  const move = (e: KeyboardEvent<HTMLButtonElement>, i: number) => {
    let next = -1;
    if (e.key === "ArrowRight") next = (i + 1) % tabs.length;
    else if (e.key === "ArrowLeft") next = (i - 1 + tabs.length) % tabs.length;
    else if (e.key === "Home") next = 0;
    else if (e.key === "End") next = tabs.length - 1;
    if (next < 0) return;
    e.preventDefault();
    const k = tabs[next].key;
    onChange(k);
    refs.current.get(k)?.focus();
  };
  return (
    <div
      role="tablist"
      aria-label={label}
      className={cx("-mx-4 flex gap-1 overflow-x-auto border-b border-slate-200 px-4 [scrollbar-width:none] sm:mx-0 sm:px-0 dark:border-slate-800", className)}
    >
      {tabs.map((t, i) => {
        const on = t.key === value;
        return (
          <button
            key={t.key}
            ref={(el) => {
              if (el) refs.current.set(t.key, el);
              else refs.current.delete(t.key);
              if (el && on) el.scrollIntoView({ block: "nearest", inline: "nearest" });
            }}
            type="button"
            role="tab"
            aria-selected={on}
            tabIndex={on ? 0 : -1}
            title={t.title}
            onClick={() => onChange(t.key)}
            onKeyDown={(e) => move(e, i)}
            className={cx(
              "-mb-px shrink-0 border-b-2 px-3 py-2 text-sm font-medium whitespace-nowrap focus-visible:outline-2 focus-visible:outline-offset-[-2px] focus-visible:outline-brand-500",
              on
                ? "border-brand-600 text-brand-700 dark:border-sky-400 dark:text-sky-300"
                : "border-transparent text-slate-500 hover:border-slate-300 hover:text-slate-800 dark:text-slate-400 dark:hover:border-slate-600 dark:hover:text-slate-200",
            )}
          >
            {t.label}
          </button>
        );
      })}
    </div>
  );
}
