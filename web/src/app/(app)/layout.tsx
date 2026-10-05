"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";
import type { ReactNode } from "react";
import { Button } from "@/components/ui";
import { useLogout, useRequireMe } from "@/lib/auth";
import { useLiveUpdates } from "@/lib/live";

type NavItem = { href: string; label: string; soon?: string; adminOnly?: boolean };

// Onderdelen die later komen staan al in de navigatie, met de mijlpaal erbij.
const nav: NavItem[] = [
  { href: "/", label: "Overzicht" },
  { href: "/clusters", label: "Clusters" },
  { href: "/nodes", label: "Nodes" },
  { href: "/monitoring", label: "Monitoring" },
  { href: "/proxmox", label: "Proxmox" },
  { href: "/back-ups", label: "Back-ups" },
  { href: "/taken", label: "Taken" },
  { href: "/templates", label: "Templates" },
  { href: "/logboek", label: "Logboek", adminOnly: true },
  { href: "/instellingen", label: "Instellingen" },
];

export default function AppLayout({ children }: { children: ReactNode }) {
  const me = useRequireMe();
  const logout = useLogout();
  const pathname = usePathname();
  useLiveUpdates(!!me.data);
  const isAdmin = me.data?.user.role === "admin";

  if (!me.data) {
    return (
      <div className="flex min-h-screen items-center justify-center text-sm text-slate-500">
        {me.isError ? "De server is niet bereikbaar." : "Laden…"}
      </div>
    );
  }

  return (
    <div className="flex min-h-screen flex-col md:flex-row">
      <aside className="border-b border-slate-200 bg-white md:w-60 md:shrink-0 md:border-r md:border-b-0 dark:border-slate-800 dark:bg-slate-900">
        <div className="px-5 py-4 text-lg font-semibold tracking-tight">ClusterForge</div>
        <nav className="flex gap-1 overflow-x-auto px-3 pb-3 md:flex-col md:overflow-visible">
          {nav.filter((item) => !item.adminOnly || isAdmin).map((item) =>
            item.soon ? (
              <span
                key={item.href}
                title={`Komt in ${item.soon}`}
                className="flex shrink-0 items-center justify-between gap-2 rounded-md px-3 py-2 text-sm text-slate-400 dark:text-slate-500"
              >
                {item.label}
                <span className="hidden rounded bg-slate-100 px-1.5 py-0.5 text-[10px] uppercase tracking-wide md:inline dark:bg-slate-800">
                  binnenkort
                </span>
              </span>
            ) : (
              <Link
                key={item.href}
                href={item.href}
                className={`shrink-0 rounded-md px-3 py-2 text-sm font-medium ${
                  (item.href === "/" ? pathname === "/" : pathname.startsWith(item.href))
                    ? "bg-brand-50 text-brand-700 dark:bg-slate-800 dark:text-white"
                    : "text-slate-700 hover:bg-slate-100 dark:text-slate-300 dark:hover:bg-slate-800"
                }`}
              >
                {item.label}
              </Link>
            ),
          )}
        </nav>
      </aside>
      <div className="flex min-w-0 flex-1 flex-col">
        <header className="flex items-center justify-end gap-3 border-b border-slate-200 bg-white px-6 py-3 dark:border-slate-800 dark:bg-slate-900">
          <span className="text-sm text-slate-600 dark:text-slate-400">
            {me.data.user.username} <span className="text-slate-400">· {me.data.user.role}</span>
          </span>
          <Button variant="secondary" onClick={() => void logout()}>
            Uitloggen
          </Button>
        </header>
        <main className="mx-auto w-full max-w-5xl flex-1 px-6 py-8">{children}</main>
      </div>
    </div>
  );
}
