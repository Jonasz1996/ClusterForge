"use client";

import Link from "next/link";
import { useState } from "react";
import { Alert, Card, cx } from "@/components/ui";
import {
  auditQuery,
  auditTimeFmt,
  subjectHref,
  useAuditRecent,
  who,
  type AuditEntry,
  type AuditFilter,
} from "@/lib/audit";

const linkClass = "text-brand-600 hover:underline dark:text-brand-500";

// AuditList toont regels van het logboek: tijd, wie, wat en het cluster. Een
// regel klapt open voor de wijzigingen, herkomst en de ruwe gegevens.
export function AuditList({ items, compact = false }: { items: AuditEntry[]; compact?: boolean }) {
  if (items.length === 0) return <p className="text-sm text-slate-500">Geen regels gevonden.</p>;
  return (
    <ul className="divide-y divide-slate-100 dark:divide-slate-800">
      {items.map((e) => (
        <AuditRow key={e.id} e={e} compact={compact} />
      ))}
    </ul>
  );
}

function AuditRow({ e, compact }: { e: AuditEntry; compact: boolean }) {
  const [open, setOpen] = useState(false);
  const href = subjectHref(e);
  return (
    <li className="py-2 text-sm">
      <div className="flex items-start gap-3">
        <button
          type="button"
          onClick={() => setOpen(!open)}
          aria-expanded={open}
          aria-label={open ? "Details verbergen" : "Details tonen"}
          className="mt-0.5 shrink-0 rounded px-1 text-xs text-slate-400 hover:bg-slate-100 hover:text-slate-700 dark:hover:bg-slate-800"
        >
          {open ? "▾" : "▸"}
        </button>
        <div className="grid min-w-0 flex-1 gap-x-3 gap-y-0.5 sm:grid-cols-[9.5rem_11.5rem_1fr]">
          <time className="text-xs whitespace-nowrap text-slate-500 tabular-nums sm:text-sm" dateTime={e.ts}>
            {auditTimeFmt.format(new Date(e.ts))}
          </time>
          <span className={cx("truncate", e.actor.type !== "user" && "text-slate-500")} title={who(e)}>
            {who(e)}
          </span>
          <span className="min-w-0 break-words">
            {href ? (
              <Link href={href} className={linkClass}>
                {e.summary}
              </Link>
            ) : (
              e.summary
            )}
            {e.subject.deleted && <span className="text-slate-500"> (verwijderd)</span>}
            {!compact && e.cluster && e.subject.type !== "cluster" && (
              <span className="text-slate-500">
                {" · "}
                {e.cluster.deleted ? (
                  `${e.cluster.name} (verwijderd)`
                ) : (
                  <Link href={`/clusters/detail?id=${e.cluster.id}`} className="hover:underline">
                    {e.cluster.name}
                  </Link>
                )}
              </span>
            )}
          </span>
        </div>
      </div>
      {open && <AuditDetails e={e} />}
    </li>
  );
}

function AuditDetails({ e }: { e: AuditEntry }) {
  return (
    <div className="mt-2 ml-7 space-y-3 rounded-md bg-slate-50 p-3 text-sm dark:bg-slate-800/50">
      {e.changes.length > 0 && (
        <table className="w-full text-left text-sm">
          <thead>
            <tr className="text-xs text-slate-500">
              <th className="py-1 pr-3 font-medium">Veld</th>
              <th className="py-1 pr-3 font-medium">Was</th>
              <th className="py-1 font-medium">Wordt</th>
            </tr>
          </thead>
          <tbody>
            {e.changes.map((c) => (
              <tr key={c.field} className="align-top">
                <td className="py-1 pr-3 text-slate-600 dark:text-slate-400">{c.label}</td>
                <td className="py-1 pr-3 break-all">{c.from || <span className="text-slate-400">leeg</span>}</td>
                <td className="py-1 break-all">{c.to || <span className="text-slate-400">leeg</span>}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      <dl className="grid grid-cols-[7rem_1fr] gap-x-3 gap-y-1">
        <dt className="text-slate-500">Soort</dt>
        <dd>
          <code className="text-xs">{e.action}</code>
        </dd>
        {e.ip && (
          <>
            <dt className="text-slate-500">IP-adres</dt>
            <dd className="tabular-nums">{e.ip}</dd>
          </>
        )}
        {e.session && (
          <>
            <dt className="text-slate-500">Sessie</dt>
            <dd>
              <code className="text-xs" title="Regels met dezelfde code komen uit dezelfde login">
                {e.session}
              </code>
            </dd>
          </>
        )}
        {e.node && e.subject.type !== "node" && (
          <>
            <dt className="text-slate-500">Node</dt>
            <dd>
              {e.node.deleted ? (
                `${e.node.name} (verwijderd)`
              ) : (
                <Link href={`/nodes/detail?id=${e.node.id}`} className={linkClass}>
                  {e.node.name}
                </Link>
              )}
            </dd>
          </>
        )}
        {e.job && (
          <>
            <dt className="text-slate-500">Taak</dt>
            <dd className="space-x-3">
              <Link href={`/taken/detail?id=${e.job.id}`} className={linkClass}>
                {e.job.name || "taak"}
              </Link>
              <Link href={`/logboek?${auditQuery({ job: e.job.id })}`} className={linkClass}>
                alles van deze taak
              </Link>
            </dd>
          </>
        )}
        <dt className="text-slate-500">Regel</dt>
        <dd className="tabular-nums">#{e.id}</dd>
      </dl>
      <details>
        <summary className="cursor-pointer text-xs text-slate-500">Ruwe gegevens</summary>
        <pre className="mt-2 max-h-64 overflow-auto rounded bg-slate-900 p-2 font-mono text-xs text-slate-100">
          {JSON.stringify(e.payload, null, 2)}
        </pre>
      </details>
    </div>
  );
}

// HistoryCard toont de laatste regels over een cluster, node of taak, met
// een link naar het volledige logboek. Alleen voor beheerders.
export function HistoryCard({ filter, isAdmin }: { filter: AuditFilter; isAdmin: boolean }) {
  const recent = useAuditRecent(filter, 10, isAdmin);
  if (!isAdmin) return null;
  return (
    <Card
      title={
        <span className="flex flex-wrap items-center justify-between gap-2">
          <span>Geschiedenis</span>
          <Link href={`/logboek?${auditQuery(filter)}`} className={cx("text-sm font-normal", linkClass)}>
            Alles in het logboek
          </Link>
        </span>
      }
    >
      {recent.isLoading && <p className="text-sm text-slate-500">Laden…</p>}
      {recent.isError && <Alert>Geschiedenis kon niet geladen worden.</Alert>}
      {recent.data && <AuditList items={recent.data} compact />}
    </Card>
  );
}
