"use client";

import { useSyncExternalStore } from "react";
import { cx } from "@/components/ui";

// toast toont kort een melding rechtsonder, zoals "Gekopieerd" of
// "Opgeslagen". Fouten die de gebruiker moet lezen horen in een Alert bij het
// formulier, niet in een toast die vanzelf verdwijnt.
type Toast = { id: number; text: string; kind: "success" | "error" | "info" };

let toasts: Toast[] = [];
let nextId = 1;
const listeners = new Set<() => void>();

function emit() {
  for (const l of listeners) l();
}

export function toast(text: string, kind: Toast["kind"] = "success") {
  const id = nextId++;
  toasts = [...toasts.slice(-3), { id, text, kind }];
  emit();
  setTimeout(
    () => {
      toasts = toasts.filter((t) => t.id !== id);
      emit();
    },
    kind === "error" ? 8000 : 3500,
  );
}

const empty: Toast[] = [];

export function ToastHost() {
  const list = useSyncExternalStore(
    (l) => {
      listeners.add(l);
      return () => listeners.delete(l);
    },
    () => toasts,
    () => empty,
  );
  if (list.length === 0) return null;
  return (
    <div className="pointer-events-none fixed inset-x-4 bottom-4 z-[60] flex flex-col items-end gap-2 sm:inset-x-auto sm:right-6" aria-live="polite">
      {list.map((t) => (
        <div
          key={t.id}
          role={t.kind === "error" ? "alert" : "status"}
          className={cx(
            "pointer-events-auto max-w-sm rounded-md px-4 py-2.5 text-sm shadow-lg",
            t.kind === "success" && "bg-emerald-700 text-white",
            t.kind === "error" && "bg-red-700 text-white",
            t.kind === "info" && "bg-slate-800 text-white dark:bg-slate-700",
          )}
        >
          {t.text}
        </div>
      ))}
    </div>
  );
}
