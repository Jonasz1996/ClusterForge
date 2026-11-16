"use client";

import { useCallback, useSyncExternalStore } from "react";
import { themeKey as key } from "./themeBoot";

// Thema: licht, donker of zoals het systeem. De keuze staat in de browser
// (localStorage) en komt als data-theme op <html>; globals.css kijkt daarnaar.
export type Theme = "system" | "light" | "dark";

const listeners = new Set<() => void>();

function read(): Theme {
  try {
    const v = localStorage.getItem(key);
    if (v === "light" || v === "dark") return v;
  } catch {
    // privévenster of geblokkeerde opslag: dan volgt het thema het systeem
  }
  return "system";
}

export function applyTheme(t: Theme) {
  const el = document.documentElement;
  if (t === "system") el.removeAttribute("data-theme");
  else el.setAttribute("data-theme", t);
}

export function useTheme(): [Theme, (t: Theme) => void] {
  const theme = useSyncExternalStore(
    (l) => {
      listeners.add(l);
      return () => listeners.delete(l);
    },
    read,
    () => "system" as Theme,
  );
  const set = useCallback((t: Theme) => {
    try {
      if (t === "system") localStorage.removeItem(key);
      else localStorage.setItem(key, t);
    } catch {
      // niet bewaard, maar wel toegepast voor deze pagina
    }
    applyTheme(t);
    for (const l of listeners) l();
  }, []);
  return [theme, set];
}

// isDarkNow zegt of de pagina nu donker is, voor code die zelf kleuren kiest
// zoals de grafieken.
export function isDarkNow() {
  if (typeof window === "undefined") return false;
  const t = document.documentElement.getAttribute("data-theme");
  if (t === "dark") return true;
  if (t === "light") return false;
  return window.matchMedia("(prefers-color-scheme: dark)").matches;
}
