"use client";

import { useState, type ReactNode } from "react";
import { Modal } from "@/components/Modal";
import { Button, Field, Input, Select, Textarea } from "@/components/ui";
import type { OpnCall } from "@/lib/opnsense";

// Een formulier voor een OPNsense-item (regel, alias, port forward). Het
// stuurt alleen de velden die veranderden, zodat de rest blijft zoals
// OPNsense hem had; de wijziging gaat daarna door ChangeDialog.

export type FieldSpec = {
  // key is het veld, met een punt voor genest (destination.port).
  key: string;
  label: string;
  kind?: "text" | "select" | "check" | "textarea";
  options?: [string, string][];
  hint?: string;
  wide?: boolean;
};

export type Changed = { key: string; label: string; from: string; to: string }[];

export function EditForm({
  title,
  specs,
  initial,
  isNew,
  onClose,
  onNext,
}: {
  title: string;
  specs: FieldSpec[];
  initial: Record<string, string>;
  isNew: boolean;
  onClose: () => void;
  onNext: (changed: Changed) => void;
}) {
  const [v, setV] = useState<Record<string, string>>(initial);
  const changed: Changed = specs
    .filter((s) => (v[s.key] ?? "") !== (initial[s.key] ?? "") || (isNew && (v[s.key] ?? "") !== ""))
    .map((s) => ({ key: s.key, label: s.label, from: initial[s.key] ?? "", to: v[s.key] ?? "" }));
  return (
    <Modal title={title} onClose={onClose} wide>
      <form
        className="space-y-4"
        onSubmit={(e) => {
          e.preventDefault();
          onNext(changed);
        }}
      >
        <div className="grid gap-4 sm:grid-cols-2">
          {specs.map((s) => {
            const id = "opn-f-" + s.key.replace(/[^a-z0-9]/gi, "-");
            const val = v[s.key] ?? "";
            const set = (x: string) => setV({ ...v, [s.key]: x });
            let input: ReactNode;
            switch (s.kind) {
              case "select":
                input = (
                  <Select id={id} value={val} onChange={(e) => set(e.target.value)}>
                    {(s.options ?? []).map(([o, l]) => (
                      <option key={o} value={o}>
                        {l}
                      </option>
                    ))}
                  </Select>
                );
                break;
              case "check":
                return (
                  <label key={s.key} className="flex items-center gap-2 text-sm sm:col-span-2">
                    <input id={id} type="checkbox" checked={val === "1"} onChange={(e) => set(e.target.checked ? "1" : "0")} />
                    {s.label}
                  </label>
                );
              case "textarea":
                input = <Textarea id={id} rows={4} value={val} onChange={(e) => set(e.target.value)} className="font-mono text-xs" />;
                break;
              default:
                input = <Input id={id} value={val} onChange={(e) => set(e.target.value)} autoComplete="off" />;
            }
            return (
              <div key={s.key} className={s.wide || s.kind === "textarea" ? "sm:col-span-2" : undefined}>
                <Field label={s.label} htmlFor={id} hint={s.hint}>
                  {input}
                </Field>
              </div>
            );
          })}
        </div>
        <div className="flex gap-2">
          <Button type="submit" disabled={changed.length === 0}>
            Verder
          </Button>
          <Button type="button" variant="secondary" onClick={onClose}>
            Annuleren
          </Button>
        </div>
      </form>
    </Modal>
  );
}

// toBody bouwt het lichaam van setItem/setRule uit de gewijzigde velden.
export function toBody(root: string, changed: Changed, extra: Record<string, string> = {}): Record<string, unknown> {
  const inner: Record<string, unknown> = {};
  const put = (key: string, val: string) => {
    const [a, b] = key.split(".");
    if (b) {
      const sub = (inner[a] as Record<string, string>) ?? {};
      sub[b] = val;
      inner[a] = sub;
    } else {
      inner[a] = val;
    }
  };
  for (const [k, val] of Object.entries(extra)) put(k, val);
  for (const c of changed) put(c.key, c.to);
  return { [root]: inner };
}

// ChangedList toont oud → nieuw in de ChangeDialog.
export function ChangedList({ changed }: { changed: Changed }) {
  if (changed.length === 0) return null;
  return (
    <table className="mt-2 w-full text-left text-xs">
      <tbody>
        {changed.map((c) => (
          <tr key={c.key}>
            <td className="py-0.5 pr-3 text-slate-500">{c.label}</td>
            <td className="py-0.5 pr-2 font-mono break-all text-red-700 line-through dark:text-red-400">{c.from || "(leeg)"}</td>
            <td className="py-0.5 font-mono break-all text-green-700 dark:text-green-400">{c.to || "(leeg)"}</td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

// Pending is een wijziging die klaarstaat voor de ChangeDialog.
export type Pending = { calls: OpnCall[]; before?: ReactNode };
