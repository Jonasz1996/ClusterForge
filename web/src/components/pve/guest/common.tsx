"use client";

import type { ReactNode } from "react";
import { Modal } from "@/components/Modal";
import { Alert, Button } from "@/components/ui";

// FormModal is het vaste venster voor een wijziging aan een gast.
export function FormModal({
  title,
  onClose,
  onSubmit,
  submitLabel,
  disabled,
  error,
  children,
  wide,
}: {
  title: string;
  onClose: () => void;
  onSubmit: () => void;
  submitLabel: string;
  disabled?: boolean;
  error?: string | null;
  children: ReactNode;
  wide?: boolean;
}) {
  return (
    <Modal title={title} onClose={onClose} wide={wide}>
      <form
        className="space-y-4"
        onSubmit={(e) => {
          e.preventDefault();
          if (!disabled && !error) onSubmit();
        }}
      >
        {children}
        {error && <Alert>{error}</Alert>}
        <div className="flex justify-end gap-2">
          <Button type="button" variant="secondary" onClick={onClose}>
            Annuleren
          </Button>
          <Button type="submit" disabled={disabled || !!error}>
            {submitLabel}
          </Button>
        </div>
      </form>
    </Modal>
  );
}

// Check is een vinkje met tekst.
export function Check({ id, checked, onChange, children }: { id: string; checked: boolean; onChange: (v: boolean) => void; children: ReactNode }) {
  return (
    <label htmlFor={id} className="flex items-start gap-2 text-sm">
      <input id={id} type="checkbox" className="mt-0.5" checked={checked} onChange={(e) => onChange(e.target.checked)} />
      <span>{children}</span>
    </label>
  );
}

// joinProps zet sleutels terug in de Proxmox-vorm "a=1,b=2"; een lege sleutel
// komt vooraan zonder naam (bijvoorbeeld het volume van een schijf).
export function joinProps(p: Record<string, string>): string {
  const head = p[""] ? [p[""]] : [];
  return [...head, ...Object.entries(p).filter(([k, v]) => k !== "" && v !== "").map(([k, v]) => `${k}=${v}`)].join(",");
}

// freeIndex geeft het eerste vrije nummer voor een sleutel zoals net of scsi.
export function freeIndex(cfg: Record<string, unknown>, prefix: string, max = 30): number {
  for (let i = 0; i <= max; i++) if (cfg[prefix + i] === undefined) return i;
  return -1;
}
