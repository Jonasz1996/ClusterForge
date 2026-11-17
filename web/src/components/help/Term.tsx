"use client";

import { useId, useState, type ReactNode } from "react";
import { glossary } from "@/lib/help";

/**
 * Term toont een vakterm met stippellijn; erover gaan of erop tikken toont de
 * uitleg uit de woordenlijst (fase 41). Zonder children de naam uit de lijst.
 */
export function Term({ id, children }: { id: string; children?: ReactNode }) {
  const t = glossary[id];
  const [open, setOpen] = useState(false);
  const tip = useId();
  if (!t) return <>{children ?? id}</>;
  return (
    <span className="relative inline-block">
      <button
        type="button"
        className="cursor-help underline decoration-dotted underline-offset-2"
        aria-describedby={open ? tip : undefined}
        onClick={() => setOpen((o) => !o)}
        onMouseEnter={() => setOpen(true)}
        onMouseLeave={() => setOpen(false)}
        onBlur={() => setOpen(false)}
      >
        {children ?? t.term}
      </button>
      {open && (
        <span
          id={tip}
          role="tooltip"
          className="absolute bottom-full left-0 z-50 mb-1 block w-72 max-w-[80vw] rounded-md bg-slate-900 px-3 py-2 text-left text-xs leading-relaxed font-normal text-white shadow-lg dark:bg-slate-700"
        >
          <strong className="block">{t.term}</strong>
          {t.uitleg}
        </span>
      )}
    </span>
  );
}
