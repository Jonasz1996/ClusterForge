"use client";

import Link from "next/link";
import { useSyncExternalStore } from "react";
import { cx } from "@/components/ui";
import { activeHref, groupOf, navGroups, navHome, type NavItem } from "@/lib/nav";

const collapsedKey = "cf-nav-dicht";

const listeners = new Set<() => void>();
let cache: { raw: string | null; value: string[] } = { raw: null, value: [] };
const none: string[] = [];

function readCollapsed(): string[] {
  let raw: string | null = null;
  try {
    raw = localStorage.getItem(collapsedKey);
  } catch {
    return none;
  }
  if (raw === cache.raw) return cache.value;
  let value: string[] = [];
  try {
    const v = JSON.parse(raw ?? "[]");
    if (Array.isArray(v)) value = v.filter((x) => typeof x === "string");
  } catch {
    // kapotte waarde: alles open
  }
  cache = { raw, value };
  return value;
}

function writeCollapsed(next: string[]) {
  try {
    localStorage.setItem(collapsedKey, JSON.stringify(next));
  } catch {
    // niet bewaard
  }
  for (const l of listeners) l();
}

// Sidebar is het menu in groepen. Een groep klapt dicht met een klik op de
// kop; dat onthoudt de browser. De groep van de huidige pagina staat altijd
// open. badges zijn tellers naast een menu-item, zoals open incidenten.
export function Sidebar({
  pathname,
  isAdmin,
  badges,
  onNavigate,
}: {
  pathname: string;
  isAdmin: boolean;
  badges?: Record<string, { count: number; tone: "red" | "amber" }>;
  onNavigate?: () => void;
}) {
  const active = activeHref(pathname);
  const activeGroup = groupOf(active);
  const collapsed = useSyncExternalStore(
    (l) => {
      listeners.add(l);
      return () => listeners.delete(l);
    },
    readCollapsed,
    () => none,
  );

  function toggle(id: string) {
    writeCollapsed(collapsed.includes(id) ? collapsed.filter((x) => x !== id) : [...collapsed, id]);
  }

  const item = (i: NavItem) => {
    const on = active === i.href;
    const b = badges?.[i.href];
    return (
      <Link
        key={i.href}
        href={i.href}
        onClick={onNavigate}
        aria-current={on ? "page" : undefined}
        className={cx(
          "flex items-center justify-between gap-2 rounded-md px-3 py-1.5 text-sm font-medium",
          on
            ? "bg-brand-50 text-brand-700 dark:bg-slate-800 dark:text-white"
            : "text-slate-700 hover:bg-slate-100 dark:text-slate-300 dark:hover:bg-slate-800",
        )}
      >
        <span className="truncate">{i.label}</span>
        {b && b.count > 0 && (
          <span
            className={cx(
              "min-w-5 rounded-full px-1.5 text-center text-[11px] leading-5 font-semibold",
              b.tone === "red" ? "bg-red-600 text-white" : "bg-amber-400 text-amber-950",
            )}
          >
            {b.count > 99 ? "99+" : b.count}
          </span>
        )}
      </Link>
    );
  };

  return (
    <nav aria-label="Hoofdmenu" className="space-y-1 px-3 pb-4">
      {item(navHome)}
      {navGroups.map((g) => {
        const items = g.items.filter((i) => !i.adminOnly || isAdmin);
        if (items.length === 0) return null;
        const open = g.id === activeGroup || !collapsed.includes(g.id);
        const groupCount = items.reduce((n, i) => n + (badges?.[i.href]?.count ?? 0), 0);
        return (
          <div key={g.id} className="pt-2">
            <button
              type="button"
              onClick={() => toggle(g.id)}
              aria-expanded={open}
              className="flex w-full items-center justify-between rounded px-3 py-1 text-[11px] font-semibold tracking-wider text-slate-500 uppercase hover:text-slate-800 dark:hover:text-slate-200"
            >
              <span>{g.label}</span>
              <span className="flex items-center gap-1.5">
                {!open && groupCount > 0 && <span className="size-2 rounded-full bg-red-500" aria-label="meldingen" />}
                <span aria-hidden className={cx("transition-transform", open ? "rotate-90" : "")}>
                  ›
                </span>
              </span>
            </button>
            {open && <div className="mt-0.5 space-y-0.5">{items.map(item)}</div>}
          </div>
        );
      })}
    </nav>
  );
}
