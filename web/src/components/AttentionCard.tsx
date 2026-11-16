"use client";

import Link from "next/link";
import { Alert, Card, cx } from "@/components/ui";
import { useAttention } from "@/lib/attention";

// AttentionCard toont bovenaan het overzicht wat nu een actie vraagt, met een
// link naar de pagina waar je het oplost. Alles in orde: één groene regel.
export function AttentionCard() {
  const q = useAttention();
  const items = q.data?.items ?? [];
  return (
    <Card title="Aandacht nodig">
      {q.isLoading && <p className="text-sm text-slate-500">Laden…</p>}
      {q.isError && <Alert>Kon niet nagaan wat aandacht vraagt.</Alert>}
      {q.data && items.length === 0 && (
        <p className="flex items-center gap-2 text-sm text-emerald-700 dark:text-emerald-300">
          <span className="size-2 rounded-full bg-emerald-500" aria-hidden />
          Niets dat nu een actie vraagt.
        </p>
      )}
      {items.length > 0 && (
        <ul className="-mx-2 space-y-0.5">
          {items.map((it) => (
            <li key={it.id}>
              <Link href={it.href} className="flex items-start gap-3 rounded-md px-2 py-2 hover:bg-slate-50 dark:hover:bg-slate-800">
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
            </li>
          ))}
        </ul>
      )}
    </Card>
  );
}
