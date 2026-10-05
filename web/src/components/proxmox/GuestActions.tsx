"use client";

import { useState } from "react";
import { Modal } from "@/components/Modal";
import { Alert, Button, Field, Input, Select } from "@/components/ui";
import { useVmAction, type Job, type VmAction, type VmActionInput } from "@/lib/proxmox";

export type GuestRef = { vmid: number; type: string; name: string; status: string; host: string; template: boolean };

export type ActionResult = { kind: "success"; job: Job } | { kind: "error"; message: string };

const confirmText: Partial<Record<VmAction, (name: string) => string>> = {
  shutdown: (n) => `${n} netjes afsluiten? Proxmox vraagt het besturingssysteem om af te sluiten.`,
  reboot: (n) => `${n} herstarten?`,
  stop: (n) => `${n} hard uitzetten? Dat is de stekker eruit trekken: niet-opgeslagen werk gaat verloren.`,
};

// GuestActions toont de acties voor een VM of container. Elke actie wordt een
// taak; onResult meldt de taak of de fout.
export function GuestActions({
  proxmoxId,
  guest,
  hosts,
  onResult,
  layout = "menu",
}: {
  proxmoxId: string;
  guest: GuestRef;
  hosts: string[];
  onResult: (r: ActionResult) => void;
  layout?: "menu" | "buttons";
}) {
  const action = useVmAction();
  const [dialog, setDialog] = useState<"snapshot" | "migrate" | null>(null);

  if (guest.template) return <span className="text-xs text-slate-400">Template</span>;

  const run = async (body: VmActionInput) => {
    try {
      const job = await action.mutateAsync({ proxmoxId, vmid: guest.vmid, body });
      onResult({ kind: "success", job });
      return true;
    } catch (e) {
      onResult({ kind: "error", message: e instanceof Error ? e.message : "Actie mislukt" });
      return false;
    }
  };
  const quick = (a: VmAction) => {
    const ask = confirmText[a];
    if (ask && !window.confirm(ask(guest.name || `VM ${guest.vmid}`))) return;
    void run({ action: a });
  };

  const running = guest.status === "running";
  const items: { label: string; onClick: () => void; danger?: boolean }[] = running
    ? [
        { label: "Afsluiten", onClick: () => quick("shutdown") },
        { label: "Herstarten", onClick: () => quick("reboot") },
        { label: "Snapshot…", onClick: () => setDialog("snapshot") },
        { label: "Migreren…", onClick: () => setDialog("migrate") },
        { label: "Hard uitzetten", onClick: () => quick("stop"), danger: true },
      ]
    : [
        { label: "Starten", onClick: () => quick("start") },
        { label: "Snapshot…", onClick: () => setDialog("snapshot") },
        { label: "Migreren…", onClick: () => setDialog("migrate") },
      ];

  return (
    <>
      {layout === "buttons" ? (
        <div className="flex flex-wrap gap-2">
          {items.map((it) => (
            <Button
              key={it.label}
              variant={it.danger ? "secondary-danger" : "secondary"}
              disabled={action.isPending}
              onClick={it.onClick}
            >
              {it.label}
            </Button>
          ))}
        </div>
      ) : (
        <details className="relative inline-block text-left [&[open]>summary]:bg-slate-100 dark:[&[open]>summary]:bg-slate-800">
          <summary className="cursor-pointer list-none rounded-md px-2 py-1 text-sm font-medium text-slate-600 select-none hover:bg-slate-100 dark:text-slate-300 dark:hover:bg-slate-800">
            Acties ▾
          </summary>
          <div className="absolute right-0 z-20 mt-1 w-44 rounded-md border border-slate-200 bg-white py-1 shadow-lg dark:border-slate-700 dark:bg-slate-900">
            {items.map((it) => (
              <button
                key={it.label}
                type="button"
                disabled={action.isPending}
                onClick={(e) => {
                  (e.currentTarget.closest("details") as HTMLDetailsElement | null)?.removeAttribute("open");
                  it.onClick();
                }}
                className={`block w-full px-3 py-1.5 text-left text-sm hover:bg-slate-50 disabled:opacity-60 dark:hover:bg-slate-800 ${
                  it.danger ? "text-red-700 dark:text-red-300" : ""
                }`}
              >
                {it.label}
              </button>
            ))}
          </div>
        </details>
      )}
      {dialog === "snapshot" && (
        <SnapshotDialog guest={guest} pending={action.isPending} onClose={() => setDialog(null)} onSubmit={run} />
      )}
      {dialog === "migrate" && (
        <MigrateDialog
          guest={guest}
          hosts={hosts.filter((h) => h !== guest.host)}
          pending={action.isPending}
          onClose={() => setDialog(null)}
          onSubmit={run}
        />
      )}
    </>
  );
}

function SnapshotDialog({
  guest,
  pending,
  onClose,
  onSubmit,
}: {
  guest: GuestRef;
  pending: boolean;
  onClose: () => void;
  onSubmit: (b: VmActionInput) => Promise<boolean>;
}) {
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [vmstate, setVmstate] = useState(false);
  const canState = guest.type === "qemu" && guest.status === "running";
  return (
    <Modal title={`Snapshot van ${guest.name || guest.vmid}`} onClose={onClose}>
      <form
        className="space-y-4"
        onSubmit={async (e) => {
          e.preventDefault();
          if (await onSubmit({ action: "snapshot", snapshot_name: name.trim(), description, vmstate: canState && vmstate }))
            onClose();
        }}
      >
        <Field label="Naam" htmlFor="snap-name" hint="Leeg geeft cf-datum-tijd. Begint met een letter; letters, cijfers, - en _.">
          <Input id="snap-name" value={name} onChange={(e) => setName(e.target.value)} placeholder="voor-update" maxLength={40} />
        </Field>
        <Field label="Beschrijving" htmlFor="snap-desc">
          <Input id="snap-desc" value={description} onChange={(e) => setDescription(e.target.value)} maxLength={500} />
        </Field>
        {canState && (
          <label className="flex items-start gap-2 text-sm">
            <input type="checkbox" className="mt-0.5" checked={vmstate} onChange={(e) => setVmstate(e.target.checked)} />
            <span>
              Geheugen meenemen
              <span className="block text-xs text-slate-500">Terugzetten hervat de VM dan precies waar hij was; de snapshot wordt groter.</span>
            </span>
          </label>
        )}
        <div className="flex justify-end gap-2">
          <Button type="button" variant="secondary" onClick={onClose}>
            Annuleren
          </Button>
          <Button type="submit" disabled={pending}>
            Snapshot maken
          </Button>
        </div>
      </form>
    </Modal>
  );
}

function MigrateDialog({
  guest,
  hosts,
  pending,
  onClose,
  onSubmit,
}: {
  guest: GuestRef;
  hosts: string[];
  pending: boolean;
  onClose: () => void;
  onSubmit: (b: VmActionInput) => Promise<boolean>;
}) {
  const [target, setTarget] = useState(hosts[0] ?? "");
  const running = guest.status === "running";
  return (
    <Modal title={`${guest.name || guest.vmid} migreren`} onClose={onClose}>
      {hosts.length === 0 ? (
        <div className="space-y-4">
          <Alert kind="info">Er is geen andere host online om naartoe te migreren.</Alert>
          <div className="flex justify-end">
            <Button variant="secondary" onClick={onClose}>
              Sluiten
            </Button>
          </div>
        </div>
      ) : (
        <form
          className="space-y-4"
          onSubmit={async (e) => {
            e.preventDefault();
            if (await onSubmit({ action: "migrate", target })) onClose();
          }}
        >
          <p className="text-sm text-slate-600 dark:text-slate-400">
            Staat nu op <span className="font-medium">{guest.host}</span>.{" "}
            {running
              ? guest.type === "qemu"
                ? "De VM draait en gaat live over; lokale schijven worden meegekopieerd, dat kan even duren."
                : "Een container kan niet live over: hij stopt, verhuist en start op de nieuwe host."
              : "Hij staat uit en verhuist zonder te starten."}
          </p>
          <Field label="Naar host" htmlFor="mig-target">
            <Select id="mig-target" value={target} onChange={(e) => setTarget(e.target.value)}>
              {hosts.map((h) => (
                <option key={h} value={h}>
                  {h}
                </option>
              ))}
            </Select>
          </Field>
          <div className="flex justify-end gap-2">
            <Button type="button" variant="secondary" onClick={onClose}>
              Annuleren
            </Button>
            <Button type="submit" disabled={pending || target === ""}>
              Migreren
            </Button>
          </div>
        </form>
      )}
    </Modal>
  );
}
