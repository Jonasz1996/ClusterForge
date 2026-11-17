"use client";

import Link from "next/link";
import { useState } from "react";
import { Alert, Card, cx } from "@/components/ui";
import { useAttention } from "@/lib/attention";

// AttentionCard toont bovenaan het overzicht wat nu een actie vraagt, met een
// link naar de pagina waar je het oplost. Alles in orde: één groene regel.
// Sinds fase 41 heeft elke regel "Wat kun je doen", in gewone taal.
export function AttentionCard() {
  const q = useAttention();
  const items = q.data?.items ?? [];
  const [open, setOpen] = useState<string | null>(null);
  return (
    <Card title="Aandacht nodig">
      {q.isLoading && <p className="text-sm text-slate-500">Laden…</p>}
      {q.isError && <Alert>Kon niet nagaan wat aandacht vraagt. Ververs de pagina; lukt het dan nog niet, kijk of de server draait.</Alert>}
      {q.data && items.length === 0 && (
        <p className="flex items-center gap-2 text-sm text-emerald-700 dark:text-emerald-300">
          <span className="size-2 rounded-full bg-emerald-500" aria-hidden />
          Niets dat nu een actie vraagt.
        </p>
      )}
      {items.length > 0 && (
        <>
          <p className="mb-2 text-xs text-slate-500">Rood is nu belangrijk, oranje mag even wachten. Klik op een regel om naar de juiste pagina te gaan.</p>
          <ul className="-mx-2 space-y-0.5">
            {items.map((it) => (
              <li key={it.id} className="rounded-md hover:bg-slate-50 dark:hover:bg-slate-800">
                <div className="flex items-start gap-1">
                  <Link href={it.href} className="flex min-w-0 flex-1 items-start gap-3 px-2 py-2">
                    <span
                      aria-label={it.tone === "red" ? "ernstig" : "waarschuwing"}
                      className={cx("mt-1.5 size-2.5 shrink-0 rounded-full", it.tone === "red" ? "bg-red-500" : "bg-amber-400")}
                    />
                    <span className="min-w-0 flex-1">
                      <span className="block text-sm font-medium">{it.title}</span>
                      {it.detail && <span className="block truncate text-xs text-slate-500">{it.detail}</span>}
                    </span>
                    <span aria-hidden className="text-slate-400">
                      ›
                    </span>
                  </Link>
                  {it.help && (
                    <button
                      type="button"
                      onClick={() => setOpen(open === it.id ? null : it.id)}
                      aria-expanded={open === it.id}
                      className="mt-1.5 mr-1 shrink-0 rounded px-1.5 py-0.5 text-xs text-brand-700 hover:bg-brand-50 dark:text-sky-300 dark:hover:bg-slate-700"
                    >
                      Wat doen?
                    </button>
                  )}
                </div>
                {it.help && open === it.id && (
                  <p className="mx-2 mb-2 ml-[2.1rem] rounded-md bg-brand-50 px-3 py-2 text-xs text-slate-700 dark:bg-slate-800 dark:text-slate-200">{it.help}</p>
                )}
              </li>
            ))}
          </ul>
        </>
      )}
    </Card>
  );
}
