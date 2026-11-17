"use client";

import Link from "next/link";
import { useState, type ReactNode } from "react";
import { QueryState } from "@/components/inventory/bits";
import { Modal } from "@/components/Modal";
import { Alert, Button, Field, Input, Select, Textarea } from "@/components/ui";
import { usePbsCall, usePbsServerId, usePbsServers, type PbsCall, type PbsServer } from "@/lib/pbs";
import type { Job } from "@/lib/proxmox";

// PbsGate kiest de Proxmox Backup Server en toont de kinderen alleen als
// ClusterForge hem via de agent kan lezen.
export function PbsGate({ children }: { children: (s: PbsServer) => ReactNode }) {
  const q = usePbsServers();
  const list = q.data?.items ?? [];
  const [id, setId] = usePbsServerId(list);
  const srv = list.find((s) => s.node_id === id);
  return (
    <QueryState q={q}>
      {list.length === 0 ? (
        <Alert kind="info">
          Nog geen Proxmox Backup Server met een agent. Installeer de agent één keer op de PBS via{" "}
          <Link href="/nodes" className="underline">
            Nodes → Agent installeren
          </Link>
          ; ClusterForge herkent een PBS vanzelf. Staat de agent er al, werk hem dan bij via Nodes → Agents bijwerken (protocol 49 nodig).
        </Alert>
      ) : (
        <div className="space-y-4">
          {list.length > 1 && (
            <div className="flex flex-wrap items-center gap-2 text-sm">
              <label htmlFor="pbs-srv" className="text-slate-500">
                Backup Server
              </label>
              <Select id="pbs-srv" value={id} onChange={(e) => setId(e.target.value)} className="w-auto">
                {list.map((s) => (
                  <option key={s.node_id} value={s.node_id}>
                    {s.hostname}
                    {s.why ? " (niet bereikbaar)" : ""}
                  </option>
                ))}
              </Select>
            </div>
          )}
          {srv && !srv.why ? children(srv) : srv ? <Alert kind="error">{srv.why}</Alert> : null}
        </div>
      )}
    </QueryState>
  );
}

// usePbsRunner start een aanroep en houdt de taak of fout bij voor een
// melding boven de kaart.
export function usePbsRunner(id: string) {
  const call = usePbsCall(id);
  const [result, setResult] = useState<{ kind: "success"; job: Job } | { kind: "error"; message: string } | null>(null);
  const run = async (c: PbsCall) => {
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

export const timeFmt = new Intl.DateTimeFormat("nl-BE", { dateStyle: "short", timeStyle: "short" });

export function when(unix?: number) {
  return unix ? timeFmt.format(new Date(unix * 1000)) : "–";
}

// --- Formulieren (fase 32 M2-M4) ------------------------------------------

export type FormField = {
  key: string;
  label: string;
  type?: "text" | "number" | "password" | "textarea" | "select" | "checkbox";
  options?: { value: string; label: string }[];
  hint?: string;
  placeholder?: string;
  required?: boolean;
  // Alleen bij aanmaken (sleutels die PBS later niet meer wijzigt).
  createOnly?: boolean;
};

// CallForm is een venster met velden dat één aanroep van de PBS-API doet.
// Bij wijzigen gaan alleen de velden mee die veranderd zijn.
export function CallForm({
  id,
  title,
  fields,
  initial = {},
  method,
  path,
  fixed = {},
  submitLabel,
  intro,
  confirm,
  onClose,
}: {
  id: string;
  title: string;
  fields: FormField[];
  initial?: Record<string, string>;
  method: "create" | "set";
  path: string;
  fixed?: Record<string, string>;
  submitLabel: string;
  intro?: ReactNode;
  confirm?: string;
  onClose: () => void;
}) {
  const [vals, setVals] = useState<Record<string, string>>(initial);
  const [typed, setTyped] = useState("");
  const { run, pending, banner } = usePbsRunner(id);
  const [done, setDone] = useState(false);
  const shown = fields.filter((f) => method === "create" || !f.createOnly);
  const submit = async () => {
    const params: Record<string, string> = { ...fixed };
    for (const f of shown) {
      const v = (vals[f.key] ?? "").trim();
      if (method === "set" && v === (initial[f.key] ?? "")) continue;
      if (v === "" && f.type !== "checkbox") continue;
      if (f.type === "checkbox") params[f.key] = v === "1" ? "1" : "0";
      else params[f.key] = v;
    }
    if (await run({ method, path, params, confirm: confirm ? typed : undefined })) setDone(true);
  };
  return (
    <Modal title={title} onClose={onClose}>
      <form
        className="space-y-3"
        onSubmit={(e) => {
          e.preventDefault();
          void submit();
        }}
      >
        {intro && <div className="text-sm text-slate-600 dark:text-slate-300">{intro}</div>}
        {banner}
        {!done &&
          shown.map((f) => {
            const fid = `pbsf-${f.key}`;
            const v = vals[f.key] ?? "";
            const set = (x: string) => setVals((cur) => ({ ...cur, [f.key]: x }));
            if (f.type === "checkbox")
              return (
                <label key={f.key} className="flex items-start gap-2 text-sm">
                  <input type="checkbox" className="mt-0.5" checked={v === "1"} onChange={(e) => set(e.target.checked ? "1" : "0")} />
                  <span>
                    {f.label}
                    {f.hint && <span className="block text-xs text-slate-500">{f.hint}</span>}
                  </span>
                </label>
              );
            return (
              <Field key={f.key} label={f.label + (f.required ? " *" : "")} htmlFor={fid} hint={f.hint}>
                {f.type === "select" ? (
                  <Select id={fid} value={v} onChange={(e) => set(e.target.value)}>
                    {(f.options ?? []).map((o) => (
                      <option key={o.value} value={o.value}>
                        {o.label}
                      </option>
                    ))}
                  </Select>
                ) : f.type === "textarea" ? (
                  <Textarea id={fid} rows={4} value={v} placeholder={f.placeholder} onChange={(e) => set(e.target.value)} />
                ) : (
                  <Input
                    id={fid}
                    type={f.type === "password" ? "password" : f.type === "number" ? "number" : "text"}
                    value={v}
                    required={f.required}
                    placeholder={f.placeholder}
                    autoComplete={f.type === "password" ? "new-password" : "off"}
                    onChange={(e) => set(e.target.value)}
                  />
                )}
              </Field>
            );
          })}
        {!done && confirm && (
          <Field label={`Tik ${confirm} over om te bevestigen`} htmlFor="pbsf-confirm">
            <Input id="pbsf-confirm" value={typed} onChange={(e) => setTyped(e.target.value)} autoComplete="off" spellCheck={false} />
          </Field>
        )}
        <div className="flex justify-end gap-2">
          <Button type="button" variant="secondary" onClick={onClose}>
            {done ? "Sluiten" : "Annuleren"}
          </Button>
          {!done && (
            <Button type="submit" disabled={pending || (!!confirm && typed.trim() !== confirm)}>
              {submitLabel}
            </Button>
          )}
        </div>
      </form>
    </Modal>
  );
}

// keepFields zijn de bewaarregels van prune.
export const keepFields: FormField[] = [
  { key: "keep-last", label: "Laatste", type: "number", hint: "Zoveel nieuwste back-ups altijd houden" },
  { key: "keep-hourly", label: "Per uur", type: "number" },
  { key: "keep-daily", label: "Per dag", type: "number" },
  { key: "keep-weekly", label: "Per week", type: "number" },
  { key: "keep-monthly", label: "Per maand", type: "number" },
  { key: "keep-yearly", label: "Per jaar", type: "number" },
];

// scheduleHint legt de kalendernotatie van PBS uit.
export const scheduleHint = "Kalendernotatie van systemd, zoals daily, 02:30, sat 03:00, mon..fri 21:00 of *:0/30. Leeg is geen schema.";
