"use client";

import Link from "next/link";
import { useSyncExternalStore } from "react";
import { GroupIcon } from "@/components/shell/icons";
import { cx } from "@/components/ui";
import { activeHref, groupOf, inUse, itemHrefs, navGroups, navPlatforms, type NavItem } from "@/lib/nav";
import { useNavUsage, useShowAll } from "@/lib/navUsage";

// Welke hoofditems je zelf openklapte; de groep van de pagina waar je bent
// staat altijd open, de rest is standaard dicht (fase 47).
const openKey = "cf-nav-open";

const listeners = new Set<() => void>();
let cache: { raw: string | null; value: string[] } = { raw: null, value: [] };
const none: string[] = [];

function readOpen(): string[] {
  let raw: string | null = null;
  try {
    raw = localStorage.getItem(openKey);
  } catch {
    return none;
  }
  if (raw === cache.raw) return cache.value;
  let value: string[] = [];
  try {
    const v = JSON.parse(raw ?? "[]");
    if (Array.isArray(v)) value = v.filter((x) => typeof x === "string");
  } catch {
    // kapotte waarde: alles dicht
  }
  cache = { raw, value };
  return value;
}

function writeOpen(next: string[]) {
  try {
    localStorage.setItem(openKey, JSON.stringify(next));
  } catch {
    // niet bewaard
  }
  for (const l of listeners) l();
}

type BadgeMap = Record<string, { count: number; tone: "red" | "amber" }>;

// Sidebar is het menu: zes hoofditems (fase 47). Een hoofditem opent zijn
// eerste pagina en klapt zijn onderdelen open; de pijl klapt alleen open of
// dicht. Onderdelen die niet in gebruik zijn (Kubernetes, GitOps, …) staan
// er pas als ze iets beheren, of met "Alles tonen". badges zijn tellers,
// zoals open incidenten.
export function Sidebar({
  pathname,
  isAdmin,
  badges,
  onNavigate,
}: {
  pathname: string;
  isAdmin: boolean;
  badges?: BadgeMap;
  onNavigate?: () => void;
}) {
  const active = activeHref(pathname);
  const activeGroup = groupOf(active);
  const usage = useNavUsage().data;
  const [showAll, setShowAll] = useShowAll();
  const opened = useSyncExternalStore(
    (l) => {
      listeners.add(l);
      return () => listeners.delete(l);
    },
    readOpen,
    () => none,
  );

  function toggle(id: string) {
    writeOpen(opened.includes(id) ? opened.filter((x) => x !== id) : [...opened, id]);
  }

  // Een item met tabbladen telt de meldingen van al zijn tabbladen op.
  const badgeOf = (i: NavItem): { count: number; tone: "red" | "amber" } | undefined => {
    let out: { count: number; tone: "red" | "amber" } | undefined;
    for (const h of itemHrefs(i)) {
      const b = badges?.[h];
      if (!b || b.count <= 0) continue;
      out = { count: (out?.count ?? 0) + b.count, tone: out?.tone === "red" || b.tone === "red" ? "red" : "amber" };
    }
    return out;
  };

  const badge = (b?: { count: number; tone: "red" | "amber" }) =>
    b &&
    b.count > 0 && (
      <span
        className={cx(
          "min-w-5 rounded-full px-1.5 text-center text-[11px] leading-5 font-semibold",
          b.tone === "red" ? "bg-red-600 text-white" : "bg-amber-400 text-amber-950",
        )}
      >
        {b.count > 99 ? "99+" : b.count}
      </span>
    );

  // Platformen staat er alleen als er minstens één platform in gebruik is.
  const platformsUsed = navPlatforms.some((p) => inUse(p, usage));
  let hidden = 0;
  const visible = (i: NavItem) => {
    if (i.adminOnly && !isAdmin) return false;
    const used = i.href === "/platformen" ? platformsUsed : inUse(i, usage);
    // Wat je nu open hebt, staat er altijd.
    if (!used && !showAll && active !== i.href) {
      hidden++;
      return false;
    }
    return true;
  };

  const item = (i: NavItem) => {
    const on = active === i.href;
    return (
      <Link
        key={i.href}
        href={i.href}
        onClick={onNavigate}
        aria-current={on ? "page" : undefined}
        className={cx(
          "flex items-center justify-between gap-2 rounded-md py-1.5 pr-3 pl-9 text-sm",
          on
            ? "bg-brand-50 font-medium text-brand-700 dark:bg-slate-800 dark:text-white"
            : "text-slate-600 hover:bg-slate-100 dark:text-slate-400 dark:hover:bg-slate-800",
        )}
      >
        <span className="truncate">{i.label}</span>
        {badge(badgeOf(i))}
      </Link>
    );
  };

  return (
    <nav aria-label="Hoofdmenu" className="space-y-0.5 px-3 pb-4">
      {navGroups.map((g) => {
        const items = g.items.filter(visible);
        if (items.length === 0) return null;
        const here = g.id === activeGroup;
        const open = here || opened.includes(g.id);
        let count = 0;
        let tone: "red" | "amber" = "amber";
        for (const i of g.items) {
          const b = badgeOf(i);
          if (!b) continue;
          count += b.count;
          if (b.tone === "red") tone = "red";
        }
        return (
          <div key={g.id}>
            <div
              className={cx(
                "flex items-center rounded-md",
                here ? "text-brand-700 dark:text-white" : "text-slate-800 hover:bg-slate-100 dark:text-slate-200 dark:hover:bg-slate-800",
              )}
            >
              <Link
                href={items[0].href}
                onClick={() => {
                  if (!opened.includes(g.id)) writeOpen([...opened, g.id]);
                  onNavigate?.();
                }}
                className="flex min-w-0 flex-1 items-center gap-2.5 px-3 py-2 text-sm font-semibold"
              >
                <GroupIcon id={g.id} />
                <span className="truncate">{g.label}</span>
                {!open && <span className="ml-auto">{badge(count > 0 ? { count, tone } : undefined)}</span>}
              </Link>
              <button
                type="button"
                onClick={() => toggle(g.id)}
                aria-expanded={open}
                aria-label={open ? `${g.label} dichtklappen` : `${g.label} openklappen`}
                disabled={here}
                className="rounded px-2 py-2 text-slate-400 hover:text-slate-700 disabled:opacity-40 dark:hover:text-slate-200"
              >
                <span aria-hidden className={cx("inline-block transition-transform", open ? "rotate-90" : "")}>
                  ›
                </span>
              </button>
            </div>
            {open && <div className="mt-0.5 mb-1.5 space-y-0.5">{items.map(item)}</div>}
          </div>
        );
      })}
      {(hidden > 0 || showAll) && (
        <button
          type="button"
          onClick={() => setShowAll(!showAll)}
          className="mt-3 w-full rounded px-3 py-1 text-left text-xs text-slate-500 hover:text-slate-800 dark:hover:text-slate-200"
        >
          {showAll ? "Alleen wat in gebruik is" : `Alles tonen (${hidden} niet in gebruik)`}
        </button>
      )}
    </nav>
  );
}
