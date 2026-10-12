"use client";

import { useEffect, type ReactNode } from "react";

// Modal is een eenvoudig dialoogvenster; Escape of een klik ernaast sluit het.
// Het neemt geen kleur of opmaak over van de plek waar het staat, zoals een
// rode melding.
export function Modal({
  title,
  onClose,
  children,
  wide = false,
}: {
  title: string;
  onClose: () => void;
  children: ReactNode;
  wide?: boolean;
}) {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);
  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-slate-900/40 p-4"
      onMouseDown={(e) => {
        if (e.target === e.currentTarget) onClose();
      }}
    >
      <div
        role="dialog"
        aria-modal="true"
        aria-label={title}
        className={`max-h-full w-full ${wide ? "max-w-3xl" : "max-w-md"} overflow-y-auto rounded-lg bg-white p-5 text-left font-normal whitespace-normal text-slate-900 shadow-xl dark:bg-slate-900 dark:text-slate-100`}
      >
        <h2 className="mb-4 text-base font-semibold">{title}</h2>
        {children}
      </div>
    </div>
  );
}
