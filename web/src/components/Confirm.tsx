"use client";

import { useEffect, useRef, useSyncExternalStore, type ReactNode } from "react";
import { Modal } from "@/components/Modal";
import { Button } from "@/components/ui";

// askConfirm vervangt window.confirm: een eigen venster in de stijl van de
// rest, dat ook op mobiel goed leest en geen browsermelding geeft. Het geeft
// true als de gebruiker bevestigt.
//
//   if (await askConfirm("VIP verwijderen?", { danger: true })) remove();

export type ConfirmOptions = {
  title?: string;
  confirmLabel?: string;
  cancelLabel?: string;
  danger?: boolean;
  // Fase 41: wat er gebeurt en of je het kunt terugdraaien, in gewone taal.
  // Zonder undo zegt een gevaarlijke vraag zelf dat terugdraaien misschien
  // niet kan.
  what?: ReactNode;
  undo?: ReactNode;
};

type Pending = { message: ReactNode; opts: ConfirmOptions; resolve: (ok: boolean) => void };

let pending: Pending | null = null;
const listeners = new Set<() => void>();

function emit() {
  for (const l of listeners) l();
}

export function askConfirm(message: ReactNode, opts: ConfirmOptions = {}): Promise<boolean> {
  // Een tweede vraag terwijl er nog één open staat, sluit de eerste af als nee.
  pending?.resolve(false);
  return new Promise<boolean>((resolve) => {
    pending = { message, opts, resolve };
    emit();
  });
}

function settle(ok: boolean) {
  const p = pending;
  pending = null;
  emit();
  p?.resolve(ok);
}

function subscribe(l: () => void) {
  listeners.add(l);
  return () => {
    listeners.delete(l);
  };
}

// guessDanger herkent een vraag over iets dat weg of kapot gaat, zodat de knop
// rood wordt zonder dat elke plek het zelf moet zeggen.
function guessDanger(message: ReactNode) {
  return typeof message === "string" && /verwijder|intrek|weghal|beëindig|loslat|ontkoppel|terugzet|weggooi|uit dit cluster|stop|annuleer|annuleren|wissen|afbreken|vergeet/i.test(message);
}

// ConfirmHost staat één keer in de layout en toont de open vraag.
export function ConfirmHost() {
  const p = useSyncExternalStore(
    subscribe,
    () => pending,
    () => null,
  );
  const okRef = useRef<HTMLButtonElement>(null);
  useEffect(() => {
    if (p) okRef.current?.focus();
  }, [p]);
  if (!p) return null;
  const danger = p.opts.danger ?? guessDanger(p.message);
  return (
    <Modal title={p.opts.title ?? "Bevestigen"} onClose={() => settle(false)}>
      <div className="text-sm text-slate-700 dark:text-slate-300">{p.message}</div>
      {p.opts.what && (
        <p className="mt-3 text-sm text-slate-700 dark:text-slate-300">
          <strong>Wat gebeurt er?</strong> {p.opts.what}
        </p>
      )}
      {(p.opts.undo || danger) && (
        <p className="mt-3 rounded-md bg-amber-50 px-3 py-2 text-xs text-amber-900 dark:bg-amber-950 dark:text-amber-200">
          <strong>Kun je het terugdraaien?</strong>{" "}
          {p.opts.undo ?? "Misschien niet. Twijfel je, annuleer dan en lees eerst de uitleg bovenaan de pagina, of vraag het aan de Assistent."}
        </p>
      )}
      <div className="mt-5 flex flex-wrap justify-end gap-2">
        <Button variant="secondary" onClick={() => settle(false)}>
          {p.opts.cancelLabel ?? (typeof p.message === "string" && /annul/i.test(p.message) ? "Nee, terug" : "Annuleren")}
        </Button>
        <Button ref={okRef} variant={danger ? "danger" : "primary"} onClick={() => settle(true)}>
          {p.opts.confirmLabel ?? (danger ? "Ja, doorgaan" : "Bevestigen")}
        </Button>
      </div>
    </Modal>
  );
}
