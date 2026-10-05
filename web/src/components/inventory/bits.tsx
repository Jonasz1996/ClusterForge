import type { ReactNode } from "react";
import { Alert, Badge } from "@/components/ui";
import { envInfo, lifecycleInfo } from "@/lib/inventory";

export function EnvBadge({ env }: { env: string }) {
  const e = envInfo(env);
  return <Badge tone={e.tone}>{e.label}</Badge>;
}

export function LifecycleBadge({ lifecycle }: { lifecycle: string }) {
  const l = lifecycleInfo(lifecycle);
  return <Badge tone={l.tone}>{l.label}</Badge>;
}

export function Tags({ tags }: { tags: string[] }) {
  if (tags.length === 0) return null;
  return (
    <span className="inline-flex flex-wrap gap-1">
      {tags.map((t) => (
        <Badge key={t}>{t}</Badge>
      ))}
    </span>
  );
}

// QueryState toont laden of een fout zolang er geen data is.
export function QueryState({ q, children }: { q: { isLoading: boolean; isError: boolean; error: unknown }; children: ReactNode }) {
  if (q.isLoading) return <p className="text-sm text-slate-500">Laden…</p>;
  if (q.isError) return <Alert>{q.error instanceof Error ? q.error.message : "Laden mislukt"}</Alert>;
  return <>{children}</>;
}

export function Empty({ children }: { children: ReactNode }) {
  return (
    <div className="rounded-lg border border-dashed border-slate-300 px-6 py-10 text-center text-sm text-slate-500 dark:border-slate-700">
      {children}
    </div>
  );
}

export const tableClass = "w-full text-left text-sm";
export const thClass = "px-3 py-2 text-xs font-medium uppercase tracking-wide text-slate-500";
export const tdClass = "px-3 py-2 align-top";
