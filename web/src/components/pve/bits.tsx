"use client";

import Link from "next/link";
import { useState, type ReactNode } from "react";
import { Modal } from "@/components/Modal";
import { QueryState } from "@/components/inventory/bits";
import { Alert, Button, cx, Field, Input, Select } from "@/components/ui";
import { API_PROTOCOL, useDatacenterId, useDatacenters, usePveCall, type Datacenter, type PveCall } from "@/lib/pve";
import type { Job } from "@/lib/proxmox";

// DatacenterGate kiest het Proxmox-cluster en toont de kinderen alleen als
// ClusterForge het via een agent kan lezen.
export function DatacenterGate({ children }: { children: (dc: Datacenter) => ReactNode }) {
  const dcs = useDatacenters();
  const [id, setId] = useDatacenterId(dcs.data?.items);
  const list = dcs.data?.items ?? [];
  const dc = list.find((d) => d.id === id);
  return (
    <QueryState q={dcs}>
      {list.length === 0 ? (
        <Alert kind="info">
          Nog geen Proxmox-host met een agent. Installeer de agent één keer op elke host via{" "}
          <Link href="/nodes" className="underline">
            Nodes → Agent installeren
          </Link>
          ; ClusterForge herkent een Proxmox-host vanzelf.
        </Alert>
      ) : (
        <div className="space-y-4">
          {list.length > 1 && (
            <div className="flex flex-wrap items-center gap-2 text-sm">
              <label htmlFor="pve-dc" className="text-slate-500">
                Proxmox-cluster
              </label>
              <Select id="pve-dc" value={id} onChange={(e) => setId(e.target.value)} className="w-auto">
                {list.map((d) => (
                  <option key={d.id} value={d.id}>
                    {d.name}
                    {d.standalone ? " (losse host)" : ""}
                  </option>
                ))}
              </Select>
            </div>
          )}
          {dc && <OldAgents dc={dc} />}
          {dc?.via ? children(dc) : dc ? <Alert kind="error">{dc.why || "Geen host met een agent die dit kan."}</Alert> : null}
        </div>
      )}
    </QueryState>
  );
}

function OldAgents({ dc }: { dc: Datacenter }) {
  const old = dc.hosts.filter((h) => h.online && h.protocol < API_PROTOCOL);
  const off = dc.hosts.filter((h) => !h.online);
  if (old.length === 0 && off.length === 0) return null;
  return (
    <Alert kind="info">
      {old.length > 0 && (
        <>
          De agent op {old.map((h) => h.pve_node).join(", ")} is te oud voor volledig Proxmox-beheer (protocol {API_PROTOCOL} nodig). Werk{" "}
          {old.length === 1 ? "hem" : "ze"} bij via{" "}
          <Link href="/nodes" className="underline">
            Nodes → Agents bijwerken
          </Link>
          ; tot dan loopt alles via een andere host.{" "}
        </>
      )}
      {off.length > 0 && <>Offline: {off.map((h) => h.pve_node).join(", ")}.</>}
    </Alert>
  );
}

// Meter is een balk met een percentage, rood vanaf crit en oranje vanaf warn.
export function Meter({ value, warn = 85, crit = 95, label }: { value: number; warn?: number; crit?: number; label?: string }) {
  const v = Math.max(0, Math.min(100, value));
  const tone = value >= crit ? "bg-red-500" : value >= warn ? "bg-amber-500" : "bg-brand-500 dark:bg-sky-500";
  return (
    <div className="flex items-center gap-2" title={label}>
      <div className="h-1.5 w-full min-w-16 overflow-hidden rounded-full bg-slate-200 dark:bg-slate-800" role="meter" aria-valuenow={Math.round(value)} aria-valuemin={0} aria-valuemax={100} aria-label={label}>
        <div className={cx("h-full rounded-full", tone)} style={{ width: `${v}%` }} />
      </div>
      <span className="w-10 shrink-0 text-right text-xs text-slate-500 tabular-nums">{Math.round(value)}%</span>
    </div>
  );
}

// TypedConfirm vraagt iets over te tikken voor een gevaarlijke actie.
export function TypedConfirm({
  title,
  expect,
  children,
  confirmLabel,
  pending,
  onConfirm,
  onClose,
}: {
  title: string;
  expect: string;
  children: ReactNode;
  confirmLabel: string;
  pending?: boolean;
  onConfirm: (typed: string) => void;
  onClose: () => void;
}) {
  const [typed, setTyped] = useState("");
  return (
    <Modal title={title} onClose={onClose}>
      <form
        className="space-y-4"
        onSubmit={(e) => {
          e.preventDefault();
          if (typed.trim() === expect) onConfirm(typed.trim());
        }}
      >
        <div className="space-y-2 text-sm">{children}</div>
        <Field label={`Tik ${expect} over om te bevestigen`} htmlFor="typed-confirm">
          <Input id="typed-confirm" value={typed} onChange={(e) => setTyped(e.target.value)} autoFocus autoComplete="off" spellCheck={false} />
        </Field>
        <div className="flex justify-end gap-2">
          <Button type="button" variant="secondary" onClick={onClose}>
            Annuleren
          </Button>
          <Button type="submit" variant="danger" disabled={typed.trim() !== expect || pending}>
            {confirmLabel}
          </Button>
        </div>
      </form>
    </Modal>
  );
}

// useCallResult houdt de laatste taak of fout van een aanroep bij, voor een
// melding boven de pagina.
export function useCallRunner(dc: string) {
  const call = usePveCall(dc);
  const [result, setResult] = useState<{ kind: "success"; job: Job } | { kind: "error"; message: string } | null>(null);
  const run = async (c: PveCall) => {
    try {
      const job = await call.mutateAsync(c);
      setResult({ kind: "success", job });
      return true;
    } catch (e) {
      setResult({ kind: "error", message: e instanceof Error ? e.message : "Mislukt" });
      return false;
    }
  };
  const banner =
    result?.kind === "success" ? (
      <Alert kind="success">
        Taak gestart: {result.job.title}.{" "}
        <Link href={`/taken/detail?id=${result.job.id}`} className="underline">
          Volgen
        </Link>
      </Alert>
    ) : result?.kind === "error" ? (
      <Alert kind="error">{result.message}</Alert>
    ) : null;
  return { run, pending: call.isPending, banner, clear: () => setResult(null) };
}

export function uptime(s: number) {
  if (!s) return "–";
  const d = Math.floor(s / 86400);
  const h = Math.floor((s % 86400) / 3600);
  if (d > 0) return `${d} d ${h} u`;
  const m = Math.floor((s % 3600) / 60);
  return `${h} u ${m} min`;
}

export function gb(bytes: number, digits = 1) {
  return `${(bytes / 2 ** 30).toFixed(digits)} GB`;
}

// ActionMenu is het menu "Acties ▾" van een rij.
export function ActionMenu({ items, disabled }: { items: { label: string; onClick: () => void; danger?: boolean }[]; disabled: boolean }) {
  return (
    <details className="relative inline-block text-left [&[open]>summary]:bg-slate-100 dark:[&[open]>summary]:bg-slate-800">
      <summary className="cursor-pointer list-none rounded-md whitespace-nowrap px-2 py-1 text-sm font-medium text-slate-600 select-none hover:bg-slate-100 dark:text-slate-300 dark:hover:bg-slate-800">
        Acties ▾
      </summary>
      <div className="absolute right-0 z-20 mt-1 w-48 rounded-md border border-slate-200 bg-white py-1 shadow-lg dark:border-slate-700 dark:bg-slate-900">
        {items.map((it) => (
          <button
            key={it.label}
            type="button"
            disabled={disabled}
            onClick={(e) => {
              (e.currentTarget.closest("details") as HTMLDetailsElement | null)?.removeAttribute("open");
              it.onClick();
            }}
            className={cx(
              "block w-full px-3 py-1.5 text-left text-sm hover:bg-slate-50 disabled:opacity-60 dark:hover:bg-slate-800",
              it.danger && "text-red-700 dark:text-red-300",
            )}
          >
            {it.label}
          </button>
        ))}
      </div>
    </details>
  );
}
