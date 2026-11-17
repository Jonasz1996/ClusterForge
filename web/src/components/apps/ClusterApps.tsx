"use client";

import Link from "next/link";
import { formatBytes } from "@/components/inventory/bits";
import { Badge, Card } from "@/components/ui";
import {
  useClusterApps,
  type AppIssue,
  type AppStorage,
  type ClusterApp,
  type NodeAppStatus,
} from "@/lib/apps";

const timeFmt = new Intl.DateTimeFormat("nl-BE", {
  hour: "2-digit",
  minute: "2-digit",
  second: "2-digit",
});

const replicationHelp: Record<string, string> = {
  "000": "één kopie",
  "001": "2 kopieën (elk volume op 2 nodes)",
  "002": "3 kopieën (elk volume op elke node)",
};

function Issues({ issues }: { issues: AppIssue[] }) {
  if (issues.length === 0) return null;
  return (
    <ul className="mb-4 space-y-2">
      {issues.map((i, n) => (
        <li
          key={n}
          className={
            i.tone === "red"
              ? "rounded-md bg-red-50 px-3 py-2 text-sm text-red-800 dark:bg-red-950 dark:text-red-200"
              : "rounded-md bg-amber-50 px-3 py-2 text-sm text-amber-900 dark:bg-amber-950 dark:text-amber-200"
          }
        >
          <div className="font-medium">{i.title}</div>
          {i.detail && <div className="mt-0.5 opacity-90">{i.detail}</div>}
        </li>
      ))}
    </ul>
  );
}

function roleBadge(n: NodeAppStatus) {
  if (n.stale) return <Badge tone="slate">geen recente stand</Badge>;
  switch (n.role) {
    case "actief":
      return <Badge tone="green">actief (VIP)</Badge>;
    case "stand-by":
      return <Badge tone="blue">stand-by</Badge>;
    case "voor-overstap":
      return <Badge tone="amber">vóór de overstap</Badge>;
    default:
      return <Badge>{n.role || "onbekend"}</Badge>;
  }
}

function NodeColumn({ n }: { n: NodeAppStatus }) {
  const bad = n.checks.filter((c) => !c.ok);
  return (
    <div className="rounded-md border border-slate-200 p-3 dark:border-slate-800">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <Link
          href={`/nodes/detail?id=${n.node_id}`}
          className="font-medium text-brand-700 hover:underline dark:text-sky-300"
        >
          {n.hostname}
        </Link>
        {roleBadge(n)}
      </div>
      <div className="mt-1 text-xs text-slate-500 dark:text-slate-400">
        Versie{" "}
        <span className="font-mono">
          {n.version ? n.version.slice(0, 12) : "?"}
        </span>
        {n.ref && n.ref !== n.version && <> ({n.ref})</>} · stand van{" "}
        {timeFmt.format(new Date(n.updated_at))}
      </div>
      <ul className="mt-3 space-y-1 text-sm">
        {n.checks.map((c) => (
          <li key={c.id} className="flex items-start gap-2">
            <span
              aria-label={c.ok ? "in orde" : "probleem"}
              className={
                c.ok
                  ? "mt-1.5 h-2 w-2 shrink-0 rounded-full bg-emerald-500"
                  : c.severity === "red"
                    ? "mt-1.5 h-2 w-2 shrink-0 rounded-full bg-red-500"
                    : "mt-1.5 h-2 w-2 shrink-0 rounded-full bg-amber-500"
              }
            />
            <span>
              {c.label}
              {!c.ok && c.detail && (
                <span className="block text-xs text-slate-500 dark:text-slate-400">
                  {c.detail}
                </span>
              )}
            </span>
          </li>
        ))}
      </ul>
      {n.values.length > 0 && (
        <dl className="mt-3 space-y-1 border-t border-slate-100 pt-2 text-xs dark:border-slate-800">
          {n.values.map((v) => (
            <div key={v.id} className="flex justify-between gap-3">
              <dt className="text-slate-500 dark:text-slate-400">{v.label}</dt>
              <dd className="text-right font-medium">{v.value}</dd>
            </div>
          ))}
        </dl>
      )}
      {bad.length === 0 && !n.stale && (
        <div className="mt-2 text-xs text-emerald-700 dark:text-emerald-400">
          Alle controles in orde
        </div>
      )}
    </div>
  );
}

function StoragePanel({ s }: { s: AppStorage }) {
  const max = Math.max(
    1,
    ...s.nodes.map((n) => Math.max(n.disk_total, n.bytes)),
  );
  const reps = Object.entries(s.replications ?? {});
  return (
    <div className="mt-5">
      <h3 className="mb-2 text-sm font-semibold">Media per node (SeaweedFS)</h3>
      <Issues issues={s.issues} />
      {s.volume_count > 0 && (
        <p className="mb-3 text-sm text-slate-600 dark:text-slate-300">
          {s.volume_count} volumes, samen {formatBytes(s.logical_bytes)} aan
          media (elk bestand één keer geteld)
          {reps.length > 0 && (
            <>
              ; replicatie{" "}
              {reps
                .map(
                  ([r, n]) =>
                    `${r} (${replicationHelp[r] ?? r}${reps.length > 1 ? `, ${n} volumes` : ""})`,
                )
                .join(", ")}
            </>
          )}
          .{" "}
          {s.under_replicated === 0
            ? "Elk volume heeft al zijn kopieën."
            : `${s.under_replicated} volumes hebben te weinig kopieën.`}
          {s.leader && <> Leider: {s.leader}.</>}
        </p>
      )}
      <ul className="space-y-2">
        {s.nodes.map((n) => {
          const used = n.disk_total > 0 ? n.disk_total - n.disk_free : 0;
          return (
            <li key={n.hostname}>
              <div className="flex flex-wrap justify-between gap-2 text-sm">
                <span className="font-medium">{n.hostname}</span>
                <span className="text-slate-600 dark:text-slate-300">
                  {formatBytes(n.bytes)} media
                  {n.disk_total > 0 && (
                    <>
                      {" "}
                      · {formatBytes(n.disk_free)} vrij van{" "}
                      {formatBytes(n.disk_total)}
                    </>
                  )}
                </span>
              </div>
              <div
                className="relative mt-1 h-2.5 overflow-hidden rounded bg-slate-100 dark:bg-slate-800"
                role="img"
                aria-label={`${n.hostname}: ${formatBytes(n.bytes)} media`}
              >
                {n.disk_total > 0 && (
                  <div
                    className="absolute inset-y-0 left-0 bg-slate-300 dark:bg-slate-600"
                    style={{ width: `${(used / max) * 100}%` }}
                  />
                )}
                <div
                  className="absolute inset-y-0 left-0 bg-sky-600 dark:bg-sky-400"
                  style={{ width: `${(n.bytes / max) * 100}%` }}
                />
              </div>
            </li>
          );
        })}
      </ul>
      {reps.length === 1 && reps[0][0] === "001" && s.nodes.length >= 3 && (
        <p className="mt-2 text-xs text-slate-500 dark:text-slate-400">
          Met 001 staat elk volume op twee nodes, dus niet alles op elke node:
          per node ongeveer twee derde van twee keer de media. Wil je alles op
          elke node, kies dan Wijzigen → Replicatie 002; ClusterForge zet de
          bestaande volumes om.
        </p>
      )}
    </div>
  );
}

function AppPanel({ a }: { a: ClusterApp }) {
  return (
    <div>
      <Issues issues={a.issues} />
      <div className="grid gap-3 md:grid-cols-2 xl:grid-cols-3">
        {a.nodes.map((n) => (
          <NodeColumn key={n.node_id} n={n} />
        ))}
      </div>
    </div>
  );
}

/** Kaart Applicatie (fase 29): de stand die de template per node meldt. */
export function ClusterAppsCard({ clusterId }: { clusterId: string }) {
  const q = useClusterApps(clusterId);
  const data = q.data;
  if (!data || (data.apps.length === 0 && !data.storage)) return null;
  const title =
    data.apps.length === 1 ? `Applicatie: ${data.apps[0].title}` : "Applicatie";
  return (
    <div id="applicatie" className="scroll-mt-20">
      <Card title={title}>
        {data.apps.map((a) => (
          <AppPanel key={a.app} a={a} />
        ))}
        {data.storage && <StoragePanel s={data.storage} />}
        <p className="mt-4 text-xs text-slate-500 dark:text-slate-400">
          Elke node meldt elke minuut zijn eigen deel; de controles van de app
          zelf doet alleen de node met het VIP. Een andere versie kies je met
          Wijzigen → Versie.
        </p>
      </Card>
    </div>
  );
}
