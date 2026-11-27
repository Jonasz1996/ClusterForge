"use client";

import Link from "next/link";
import { Term } from "@/components/help/Term";
import { helpFor } from "@/lib/help";
import { closeHelp, useHelpClosed, useHelpOn } from "@/lib/helpPrefs";

/** "Wat is dit?" bovenaan elke pagina (fase 41), uit lib/help. */
export function PageHelpBox({ pathname }: { pathname: string }) {
  const [on, setOn] = useHelpOn();
  const help = helpFor(pathname);
  // Per pagina gesloten blijft gesloten (fase 47); de ?-knop bovenaan
  // brengt ze terug.
  const closed = useHelpClosed();
  if (!on || !help || closed.includes(pathname)) return null;
  return (
    <aside
      aria-label="Uitleg bij deze pagina"
      className="mb-5 rounded-lg border border-brand-100 bg-brand-50/60 px-4 py-3 text-sm text-slate-700 print:hidden dark:border-slate-700 dark:bg-slate-900 dark:text-slate-300"
    >
      <div className="flex items-start gap-3">
        <span aria-hidden className="mt-0.5 flex size-5 shrink-0 items-center justify-center rounded-full bg-brand-600 text-xs font-bold text-white">
          ?
        </span>
        <div className="min-w-0 flex-1 space-y-1.5">
          <p>
            <strong>Wat is dit?</strong> {help.wat}
            {help.waarom && <> {help.waarom}</>}
          </p>
          {help.doen && help.doen.length > 0 && (
            <ul className="list-disc space-y-0.5 pl-5 text-slate-600 dark:text-slate-400">
              {help.doen.map((d) => (
                <li key={d}>{d}</li>
              ))}
            </ul>
          )}
          {help.termen && help.termen.length > 0 && (
            <p className="text-xs text-slate-500">
              Woorden:{" "}
              {help.termen.map((t, i) => (
                <span key={t}>
                  {i > 0 && ", "}
                  <Term id={t} />
                </span>
              ))}{" "}
              ·{" "}
              <Link href="/uitleg" className="underline">
                woordenlijst
              </Link>
            </p>
          )}
        </div>
        <div className="flex shrink-0 flex-col items-end gap-1 text-xs">
          <button type="button" onClick={() => closeHelp(pathname)} className="rounded px-1.5 py-0.5 text-slate-500 hover:bg-white/60 dark:hover:bg-slate-800" aria-label="Uitleg sluiten" title="Sluiten; de ?-knop bovenaan brengt ze terug">
            ×
          </button>
          <button type="button" onClick={() => setOn(false)} className="whitespace-nowrap text-slate-500 underline-offset-2 hover:underline">
            Overal uit
          </button>
        </div>
      </div>
    </aside>
  );
}
