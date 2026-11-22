"use client";

import { useSyncExternalStore } from "react";

// Of de uitleg "Wat is dit?" bovenaan elke pagina staat (fase 41). De keuze
// geldt voor deze browser. Sinds fase 47 onthoudt de browser ook welke
// uitleg je per pagina sloot, en staat de uitleg na de eerste week vanzelf
// uit tenzij je ze zelf weer aanzette: wie ClusterForge kent, heeft ze niet
// meer nodig, en de ?-knop bovenaan brengt ze terug.

const KEY = "cf-help";
const CLOSED = "cf-help-dicht";
const SINCE = "cf-help-sinds";
const WEEK = 7 * 24 * 3600 * 1000;
const listeners = new Set<() => void>();

function get(k: string): string | null {
  try {
    return localStorage.getItem(k);
  } catch {
    return null;
  }
}

function set(k: string, v: string | null) {
  try {
    if (v === null) localStorage.removeItem(k);
    else localStorage.setItem(k, v);
  } catch {
    // Zonder opslag geldt de keuze alleen tot de pagina herlaadt.
  }
}

// helpDefault is aan in de eerste week dat deze browser ClusterForge
// gebruikt, daarna uit.
function helpDefault(now = Date.now()): boolean {
  let since = Number(get(SINCE));
  if (!since) {
    since = now;
    set(SINCE, String(now));
  }
  return now - since < WEEK;
}

function read(): boolean {
  const v = get(KEY);
  if (v === "on") return true;
  if (v === "off") return false;
  return helpDefault();
}

function subscribe(l: () => void) {
  listeners.add(l);
  const onStorage = (e: StorageEvent) => {
    if (e.key === KEY || e.key === CLOSED) l();
  };
  window.addEventListener("storage", onStorage);
  return () => {
    listeners.delete(l);
    window.removeEventListener("storage", onStorage);
  };
}

function notify() {
  for (const l of listeners) l();
}

export function setHelpOn(on: boolean) {
  set(KEY, on ? "on" : "off");
  notify();
}

export function useHelpOn(): [boolean, (on: boolean) => void] {
  const on = useSyncExternalStore(subscribe, read, () => true);
  return [on, setHelpOn];
}

// De pagina's waar je de uitleg sloot.
let closedCache: { raw: string | null; value: string[] } = { raw: null, value: [] };

function readClosed(): string[] {
  const raw = get(CLOSED);
  if (raw === closedCache.raw) return closedCache.value;
  let value: string[] = [];
  try {
    const v = JSON.parse(raw ?? "[]");
    if (Array.isArray(v)) value = v.filter((x) => typeof x === "string");
  } catch {
    // kapotte waarde: niets gesloten
  }
  closedCache = { raw, value };
  return value;
}

export function closeHelp(path: string) {
  const c = readClosed();
  if (!c.includes(path)) set(CLOSED, JSON.stringify([...c, path]));
  notify();
}

export function reopenHelp(path: string) {
  set(
    CLOSED,
    JSON.stringify(readClosed().filter((p) => p !== path)),
  );
  notify();
}

const noneClosed: string[] = [];

export function useHelpClosed(): string[] {
  return useSyncExternalStore(subscribe, readClosed, () => noneClosed);
}
