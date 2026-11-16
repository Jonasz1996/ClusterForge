"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";
import { useEffect, useState, type ReactNode } from "react";
import { ConfirmHost } from "@/components/Confirm";
import { CommandPalette } from "@/components/shell/CommandPalette";
import { MenuIcon, MoonIcon, SearchIcon, SunIcon, SystemIcon } from "@/components/shell/icons";
import { Sidebar } from "@/components/shell/Sidebar";
import { ToastHost } from "@/components/Toast";
import { Button } from "@/components/ui";
import { useAttentionBadges } from "@/lib/attention";
import { useLogout, useRequireMe } from "@/lib/auth";
import { useLiveUpdates } from "@/lib/live";
import { titleFor } from "@/lib/nav";
import { useTheme, type Theme } from "@/lib/theme";
import { useReloadOnStaleChunks, useServerUpdated } from "@/lib/version";

const themeNext: Record<Theme, Theme> = { system: "light", light: "dark", dark: "system" };
const themeLabel: Record<Theme, string> = { system: "Thema: zoals het systeem", light: "Thema: licht", dark: "Thema: donker" };
const themeIcon: Record<Theme, ReactNode> = { system: <SystemIcon />, light: <SunIcon />, dark: <MoonIcon /> };

export default function AppLayout({ children }: { children: ReactNode }) {
  const me = useRequireMe();
  const logout = useLogout();
  const pathname = usePathname();
  useLiveUpdates(!!me.data);
  const isAdmin = me.data?.user.role === "admin";
  // Het menu op mobiel hoort bij de pagina waar het openging: na een
  // navigatie is het vanzelf dicht.
  const [menuAt, setMenuAt] = useState<string | null>(null);
  const menuOpen = menuAt === pathname;
  const setMenuOpen = (o: boolean) => setMenuAt(o ? pathname : null);
  const [searchOpen, setSearchOpen] = useState(false);
  const [theme, setTheme] = useTheme();
  const badges = useAttentionBadges(!!me.data);
  const updated = useServerUpdated(!!me.data);
  useReloadOnStaleChunks();

  // De titel van het tabblad volgt de pagina (React zet <title> in <head>).
  const pageTitle = titleFor(pathname);
  const title = <title>{pageTitle ? `${pageTitle} · ClusterForge` : "ClusterForge"}</title>;

  // Ctrl K of ⌘ K opent zoeken overal; / ook, behalve in een invoerveld.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const typing = e.target instanceof HTMLElement && (e.target.isContentEditable || /^(INPUT|TEXTAREA|SELECT)$/.test(e.target.tagName));
      if ((e.key === "k" || e.key === "K") && (e.ctrlKey || e.metaKey)) {
        e.preventDefault();
        setSearchOpen((o) => !o);
      } else if (e.key === "/" && !typing && !e.ctrlKey && !e.metaKey && !e.altKey) {
        e.preventDefault();
        setSearchOpen(true);
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);

  // Achter het open menu scrolt de pagina niet mee.
  useEffect(() => {
    document.body.style.overflow = menuOpen ? "hidden" : "";
    return () => {
      document.body.style.overflow = "";
    };
  }, [menuOpen]);

  if (!me.data) {
    return (
      <div className="flex min-h-screen flex-col items-center justify-center gap-3 text-sm text-slate-500">
        {title}
        {me.isError ? (
          <>
            <p>De server is niet bereikbaar.</p>
            <Button variant="secondary" onClick={() => void me.refetch()}>
              Opnieuw proberen
            </Button>
          </>
        ) : (
          "Laden…"
        )}
      </div>
    );
  }

  const searchButton = (
    <button
      type="button"
      onClick={() => setSearchOpen(true)}
      aria-label="Zoeken"
      className="flex min-w-0 items-center gap-2 rounded-md p-1.5 text-sm text-slate-500 hover:bg-slate-100 sm:border sm:border-slate-300 sm:bg-white sm:px-3 sm:hover:border-slate-400 sm:hover:bg-white dark:hover:bg-slate-800 sm:dark:border-slate-700 sm:dark:bg-slate-900 sm:dark:hover:border-slate-500"
    >
      <SearchIcon />
      <span className="hidden truncate sm:inline">Zoeken…</span>
      <kbd className="ml-4 hidden rounded border border-slate-200 px-1 text-[10px] text-slate-400 sm:inline dark:border-slate-700">Ctrl K</kbd>
    </button>
  );

  return (
    <div className="flex min-h-screen">
      {title}
      {/* Zijbalk op een groot scherm. */}
      <aside className="hidden w-60 shrink-0 border-r border-slate-200 bg-white md:block print:hidden dark:border-slate-800 dark:bg-slate-900">
        <div className="sticky top-0 flex max-h-screen flex-col">
          <Link href="/" className="px-5 py-4 text-lg font-semibold tracking-tight">
            ClusterForge
          </Link>
          <div className="min-h-0 flex-1 overflow-y-auto">
            <Sidebar pathname={pathname} isAdmin={isAdmin} badges={badges} />
          </div>
        </div>
      </aside>

      {/* Menu als lade op mobiel. */}
      {menuOpen && (
        <div className="fixed inset-0 z-40 md:hidden print:hidden" role="dialog" aria-modal="true" aria-label="Menu">
          <div className="absolute inset-0 bg-slate-900/40" onClick={() => setMenuOpen(false)} />
          <div className="absolute inset-y-0 left-0 flex w-72 max-w-[85vw] flex-col bg-white shadow-xl dark:bg-slate-900">
            <div className="flex items-center justify-between px-5 py-4">
              <span className="text-lg font-semibold tracking-tight">ClusterForge</span>
              <button type="button" onClick={() => setMenuOpen(false)} aria-label="Menu sluiten" className="rounded p-1 text-xl leading-none text-slate-500">
                ×
              </button>
            </div>
            <div className="min-h-0 flex-1 overflow-y-auto">
              <Sidebar pathname={pathname} isAdmin={isAdmin} badges={badges} onNavigate={() => setMenuOpen(false)} />
            </div>
            <div className="flex items-center justify-between gap-2 border-t border-slate-200 px-5 py-3 dark:border-slate-800">
              <span className="truncate text-sm text-slate-600 dark:text-slate-400">
                {me.data.user.username} <span className="text-slate-400">· {me.data.user.role}</span>
              </span>
              <Button variant="secondary" onClick={() => void logout()}>
                Uitloggen
              </Button>
            </div>
          </div>
        </div>
      )}

      <div className="flex min-w-0 flex-1 flex-col">
        <header className="sticky top-0 z-30 flex items-center gap-2 border-b border-slate-200 bg-white/95 px-3 py-2.5 backdrop-blur sm:px-6 print:hidden dark:border-slate-800 dark:bg-slate-900/95">
          <button
            type="button"
            onClick={() => setMenuOpen(true)}
            aria-label="Menu openen"
            className="relative rounded-md p-1.5 text-slate-600 hover:bg-slate-100 md:hidden dark:text-slate-300 dark:hover:bg-slate-800"
          >
            <MenuIcon />
            {badges && Object.values(badges).some((b) => b.count > 0) && (
              <span className="absolute top-1 right-1 size-2 rounded-full bg-red-500" aria-label="meldingen" />
            )}
          </button>
          <Link href="/" className="font-semibold tracking-tight md:hidden">
            ClusterForge
          </Link>
          <div className="ml-auto flex min-w-0 items-center gap-2 sm:gap-3">
            {searchButton}
            <button
              type="button"
              onClick={() => setTheme(themeNext[theme])}
              title={themeLabel[theme]}
              aria-label={themeLabel[theme]}
              className="rounded-md p-1.5 text-slate-600 hover:bg-slate-100 dark:text-slate-300 dark:hover:bg-slate-800"
            >
              {themeIcon[theme]}
            </button>
            <Link href="/instellingen" className="hidden text-sm text-slate-600 hover:underline sm:inline dark:text-slate-400">
              {me.data.user.username} <span className="text-slate-400">· {me.data.user.role}</span>
            </Link>
            <span className="hidden sm:block">
              <Button variant="secondary" onClick={() => void logout()}>
                Uitloggen
              </Button>
            </span>
          </div>
        </header>
        {updated && (
          <div role="status" className="flex flex-wrap items-center justify-center gap-3 bg-brand-600 px-4 py-2 text-sm text-white print:hidden">
            ClusterForge is bijgewerkt. Vernieuw de pagina om de nieuwe versie te gebruiken.
            <button type="button" onClick={() => window.location.reload()} className="rounded bg-white/15 px-2.5 py-1 font-medium hover:bg-white/25">
              Vernieuwen
            </button>
          </div>
        )}
        {/* De graaf van Afhankelijkheden krijgt de volle breedte. */}
        <main
          className={`mx-auto w-full flex-1 px-4 py-6 sm:px-6 sm:py-8 print:max-w-none print:p-0 ${
            pathname.startsWith("/afhankelijkheden")
              ? "max-w-[100rem]"
              : pathname.startsWith("/documentatie") || pathname.startsWith("/rapporten") || pathname.startsWith("/logs")
                ? "max-w-6xl"
                : "max-w-5xl"
          }`}
        >
          {children}
        </main>
      </div>
      {searchOpen && <CommandPalette onClose={() => setSearchOpen(false)} isAdmin={isAdmin} />}
      <ConfirmHost />
      <ToastHost />
    </div>
  );
}
