"use client";

import Link from "next/link";
import { useAssistantSettings, type Subject } from "@/lib/assistant";

// AskButton begint een gesprek met de assistent over een cluster, node of
// incident. Hij staat er alleen als de assistent voor jou aan staat.
export function AskButton({ subject, id }: { subject: Subject; id: string }) {
  const s = useAssistantSettings();
  if (!s.data?.can_use) return null;
  return (
    <Link
      href={`/assistent?${subject}=${encodeURIComponent(id)}`}
      className="inline-flex items-center justify-center rounded-md border border-slate-300 bg-white px-3.5 py-2 text-sm font-medium text-slate-800 transition hover:bg-slate-50 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-100 dark:hover:bg-slate-800"
    >
      Vraag de assistent
    </Link>
  );
}
