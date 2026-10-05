import type { ReactNode } from "react";
import { Alert, Badge } from "@/components/ui";
import { connections, envInfo, lifecycleInfo, type AgentSummary } from "@/lib/inventory";

export function EnvBadge({ env }: { env: string }) {
  const e = envInfo(env);
  return <Badge tone={e.tone}>{e.label}</Badge>;
}

export function LifecycleBadge({ lifecycle }: { lifecycle: string }) {
  const l = lifecycleInfo(lifecycle);
  return <Badge tone={l.tone}>{l.label}</Badge>;
}

export function AgentBadge({ agent }: { agent: AgentSummary | null }) {
  if (!agent) return <Badge>Geen agent</Badge>;
  const c = connections[agent.connection];
  return (
    <Badge tone={c.tone}>
      <span className="mr-1 inline-block size-1.5 rounded-full bg-current" aria-hidden />
      {c.label}
    </Badge>
  );
}

export function formatBytes(n: number) {
  const units = ["B", "KB", "MB", "GB", "TB", "PB"];
  let i = 0;
  while (n >= 1024 && i < units.length - 1) {
    n /= 1024;
    i++;
  }
  return `${n.toLocaleString("nl-BE", { maximumFractionDigits: n < 10 && i > 0 ? 1 : 0 })} ${units[i]}`;
}

export function formatDuration(seconds: number) {
  const d = Math.floor(seconds / 86400);
  const h = Math.floor((seconds % 86400) / 3600);
  const m = Math.floor((seconds % 3600) / 60);
  if (d > 0) return `${d} d ${h} u`;
  if (h > 0) return `${h} u ${m} min`;
  return `${m} min`;
}

// ago zegt hoe lang geleden iets was, in gewone woorden.
export function ago(iso: string | null | undefined, now = Date.now()) {
  if (!iso) return "nooit";
  const s = Math.max(0, Math.round((now - new Date(iso).getTime()) / 1000));
  if (s < 60) return `${s} s geleden`;
  if (s < 3600) return `${Math.floor(s / 60)} min geleden`;
  if (s < 86400) return `${Math.floor(s / 3600)} u geleden`;
  return `${Math.floor(s / 86400)} d geleden`;
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
