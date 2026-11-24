"use client";

import Link from "next/link";
import { useState } from "react";
import { RestartService } from "@/components/RestartService";
import { RunCommandAction } from "@/components/RunCommandAction";
import { Alert, Badge, Card, cx } from "@/components/ui";
import { useAttention, type AttentionItem } from "@/lib/attention";
import { useMe } from "@/lib/auth";

type Action = NonNullable<AttentionItem["actions"]>[number];

// AttentionCard is "Wat moet ik doen" (fase 47): één lijst met wat nu een
// actie vraagt, per oorzaak. Dezelfde oorzaak op 25 nodes is één regel. Elke
// regel zegt of gebruikers er iets van merken (dienst werkt of plat), heeft
// de acties bij zich (logs, herstarten, de juiste pagina) en een uitleg in
// gewone taal. Alles in orde: één groene regel.
export function AttentionCard() {
  const q = useAttention();
  const isAdmin = useMe().data?.user.role === "admin";
  const items = q.data?.items ?? [];
  const [open, setOpen] = useState<string | null>(null);
  const [restart, setRestart] = useState<{ unit: string; nodeIds: string[] } | null>(null);
  const [command, setCommand] = useState<{ title: string; script: string; nodeIds: string[] } | null>(null);
  const now = items.filter((i) => i.tone === "red");
  const later = items.filter((i) => i.tone !== "red");

  const action = (a: Action, it: AttentionItem) => {
    const cls =
      "rounded-md border border-slate-300 px-2 py-0.5 text-xs font-medium text-slate-700 hover:border-brand-500 hover:text-brand-700 dark:border-slate-600 dark:text-slate-200 dark:hover:text-sky-300";
    if (a.kind === "restart_service") {
      if (!isAdmin || !a.unit || !a.node_ids?.length) return null;
      return (
        <button key={a.label} type="button" className={cls} onClick={() => setRestart({ unit: a.unit!, nodeIds: a.node_ids! })}>
          {a.label}
        </button>
      );
    }
    if (a.kind === "run_command") {
      if (!isAdmin || !a.script || !a.node_ids?.length) return null;
      return (
        <button
          key={a.label}
          type="button"
          className={cls}
          onClick={() => setCommand({ title: a.title ?? a.label, script: a.script!, nodeIds: a.node_ids! })}
        >
          {a.label}
        </button>
      );
    }
    if (!a.href) return null;
    // Logs zijn alleen voor beheerders.
    if (!isAdmin && a.href.startsWith("/logs")) return null;
    return (
      <Link key={a.label + it.id} href={a.href} className={cls}>
        {a.label}
      </Link>
    );
  };

  const row = (it: AttentionItem) => (
    <li key={it.id} className="rounded-md px-2 py-2 hover:bg-slate-50 dark:hover:bg-slate-800/60">
      <div className="flex items-start gap-3">
        <span
          aria-label={it.tone === "red" ? "ernstig" : "waarschuwing"}
          className={cx("mt-1.5 size-2.5 shrink-0 rounded-full", it.tone === "red" ? "bg-red-500" : "bg-amber-400")}
        />
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
            <Link href={it.href} className="text-sm font-medium hover:underline">
              {it.title}
            </Link>
            {it.impact === "service_ok" && <Badge tone="green">dienst werkt</Badge>}
            {it.impact === "service_down" && <Badge tone="red">dienst plat</Badge>}
          </div>
          {it.detail && <p className="truncate text-xs text-slate-500">{it.detail}</p>}
          <div className="mt-1.5 flex flex-wrap items-center gap-1.5">
            {(it.actions ?? []).map((a) => action(a, it))}
            <Link
              href={it.href}
              className="rounded-md px-1.5 py-0.5 text-xs text-brand-700 hover:bg-brand-50 dark:text-sky-300 dark:hover:bg-slate-700"
            >
              Openen ›
            </Link>
            {it.help && (
              <button
                type="button"
                onClick={() => setOpen(open === it.id ? null : it.id)}
                aria-expanded={open === it.id}
                className="rounded-md px-1.5 py-0.5 text-xs text-brand-700 hover:bg-brand-50 dark:text-sky-300 dark:hover:bg-slate-700"
              >
                Uitleg
              </button>
            )}
          </div>
          {it.help && open === it.id && (
            <p className="mt-2 rounded-md bg-brand-50 px-3 py-2 text-xs text-slate-700 dark:bg-slate-800 dark:text-slate-200">{it.help}</p>
          )}
        </div>
      </div>
    </li>
  );

  return (
    <Card title="Wat moet ik doen">
      {q.isLoading && <p className="text-sm text-slate-500">Laden…</p>}
      {q.isError && <Alert>Kon niet nagaan wat aandacht vraagt. Ververs de pagina; lukt het dan nog niet, kijk of de server draait.</Alert>}
      {q.data && items.length === 0 && (
        <p className="flex items-center gap-2 text-sm text-emerald-700 dark:text-emerald-300">
          <span className="size-2 rounded-full bg-emerald-500" aria-hidden />
          Niets dat nu een actie vraagt.
        </p>
      )}
      {now.length > 0 && (
        <section>
          <h3 className="mb-1 text-xs font-semibold tracking-wide text-red-700 uppercase dark:text-red-300">Nu doen</h3>
          <ul className="-mx-2 space-y-0.5">{now.map(row)}</ul>
        </section>
      )}
      {later.length > 0 && (
        <section className={now.length > 0 ? "mt-4" : ""}>
          <h3 className="mb-1 text-xs font-semibold tracking-wide text-amber-700 uppercase dark:text-amber-300">Mag even wachten</h3>
          <ul className="-mx-2 space-y-0.5">{later.map(row)}</ul>
        </section>
      )}
      {restart && <RestartService unit={restart.unit} nodeIds={restart.nodeIds} onClose={() => setRestart(null)} />}
      {command && <RunCommandAction title={command.title} script={command.script} nodeIds={command.nodeIds} onClose={() => setCommand(null)} />}
    </Card>
  );
}
