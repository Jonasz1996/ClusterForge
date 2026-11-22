"use client";

import { useSyncExternalStore } from "react";

// Of de uitleg "Wat is dit?" bovenaan elke pagina staat (fase 41). Standaard
// aan; de keuze geldt voor deze browser. Per pagina kun je de uitleg ook
// dichtklappen.

const KEY = "cf-help";
const listeners = new Set<() => void>();

function read(): boolean {
  try {
    return localStorage.getItem(KEY) !== "off";
  } catch {
    return true;
  }
}

function subscribe(l: () => void) {
  listeners.add(l);
  const onStorage = (e: StorageEvent) => {
    if (e.key === KEY) l();
  };
  window.addEventListener("storage", onStorage);
  return () => {
    listeners.delete(l);
    window.removeEventListener("storage", onStorage);
  };
}

export function setHelpOn(on: boolean) {
  try {
    localStorage.setItem(KEY, on ? "on" : "off");
  } catch {
    // Zonder opslag geldt de keuze alleen tot de pagina herlaadt.
  }
  for (const l of listeners) l();
}

export function useHelpOn(): [boolean, (on: boolean) => void] {
  const on = useSyncExternalStore(subscribe, read, () => true);
  return [on, setHelpOn];
}
