"use client";

import { useState } from "react";
import type { GuestCtx } from "@/app/(app)/proxmox/gast/page";
import { askConfirm } from "@/components/Confirm";
import { Empty, QueryState, tableClass, tdClass, thClass } from "@/components/inventory/bits";
import { ActionMenu, TypedConfirm } from "@/components/pve/bits";
import { Check, FormModal } from "@/components/pve/guest/common";
import { Badge, Button, Card, Field, Input, Textarea } from "@/components/ui";
import { usePveGet, type PveSnapshotEntry } from "@/lib/pve";

const nameRe = /^[A-Za-z][A-Za-z0-9_-]{1,39}$/;

function when(t?: number) {
  if (!t) return "–";
  return new Date(t * 1000).toLocaleString("nl-BE", { dateStyle: "short", timeStyle: "short" });
}

export function SnapshotsTab({ ctx }: { ctx: GuestCtx }) {
  const q = usePveGet<PveSnapshotEntry[]>(ctx.dc.id, `${ctx.path}/snapshot`, { refetch: 15_000 });
  const [dialog, setDialog] = useState<{ kind: "create" } | { kind: "rollback"; name: string } | { kind: "describe"; snap: PveSnapshotEntry } | null>(null);
  const close = () => setDialog(null);
  const list = (q.data ?? []).filter((s) => s.name !== "current").sort((a, b) => (b.snaptime ?? 0) - (a.snaptime ?? 0));
  const current = (q.data ?? []).find((s) => s.name === "current");

  const remove = async (name: string) => {
    if (!(await askConfirm(`Snapshot ${name} verwijderen? De gast zelf blijft zoals hij nu is.`))) return;
    void ctx.run({ method: "delete", path: `${ctx.path}/snapshot/${name}` });
  };

  return (
    <Card
      title={
        <span className="flex items-center justify-between gap-2">
          <span>Snapshots</span>
          {ctx.isAdmin && (
            <Button variant="secondary" disabled={ctx.pending} onClick={() => setDialog({ kind: "create" })}>
              Snapshot maken
            </Button>
          )}
        </span>
      }
    >
      <QueryState q={q}>
        {list.length === 0 ? (
          <Empty>Nog geen snapshots. Een snapshot is geen back-up: hij staat op dezelfde storage als de gast.</Empty>
        ) : (
          <div className="relative -mx-4 overflow-x-auto sm:mx-0">
            <table className={tableClass}>
              <thead>
                <tr>
                  <th className={thClass}>Naam</th>
                  <th className={thClass}>Gemaakt</th>
                  <th className={thClass}>Beschrijving</th>
                  <th className={thClass}>
                    <span className="sr-only">Acties</span>
                  </th>
                </tr>
              </thead>
              <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
                {list.map((s) => (
                  <tr key={s.name}>
                    <td className={tdClass + " font-medium"}>
                      {s.name}{" "}
                      {s.vmstate ? <Badge tone="blue">met geheugen</Badge> : null}{" "}
                      {current?.parent === s.name && <Badge tone="green">nu hierop</Badge>}
                    </td>
                    <td className={tdClass + " whitespace-nowrap"}>{when(s.snaptime)}</td>
                    <td className={tdClass + " text-slate-600 dark:text-slate-300"}>{s.description?.trim() || "–"}</td>
                    <td className={tdClass + " text-right"}>
                      {ctx.isAdmin && (
                        <ActionMenu
                          disabled={ctx.pending}
                          items={[
                            { label: "Terugzetten", onClick: () => setDialog({ kind: "rollback", name: s.name }), danger: true },
                            { label: "Beschrijving", onClick: () => setDialog({ kind: "describe", snap: s }) },
                            { label: "Verwijderen", onClick: () => void remove(s.name), danger: true },
                          ]}
                        />
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </QueryState>
      {dialog?.kind === "create" && <CreateSnapshot ctx={ctx} taken={list.map((s) => s.name)} onClose={close} />}
      {dialog?.kind === "describe" && <DescribeSnapshot ctx={ctx} snap={dialog.snap} onClose={close} />}
      {dialog?.kind === "rollback" && <Rollback ctx={ctx} name={dialog.name} onClose={close} />}
    </Card>
  );
}

function CreateSnapshot({ ctx, taken, onClose }: { ctx: GuestCtx; taken: string[]; onClose: () => void }) {
  const [name, setName] = useState(() => {
    const d = new Date();
    const p = (x: number) => String(x).padStart(2, "0");
    return `cf-${d.getFullYear()}${p(d.getMonth() + 1)}${p(d.getDate())}-${p(d.getHours())}${p(d.getMinutes())}`;
  });
  const [description, setDescription] = useState("");
  const [vmstate, setVmstate] = useState(false);
  const error = !nameRe.test(name) ? "De naam begint met een letter en bevat letters, cijfers, _ of - (2 tot 40 tekens)." : taken.includes(name) ? "Die naam bestaat al." : null;
  const submit = () => {
    const params: Record<string, string> = { snapname: name };
    if (description.trim()) params.description = description.trim();
    if (vmstate) params.vmstate = "1";
    void ctx.run({ method: "create", path: `${ctx.path}/snapshot`, params }).then((ok) => ok && onClose());
  };
  return (
    <FormModal title="Snapshot maken" onClose={onClose} onSubmit={submit} submitLabel="Maken" disabled={ctx.pending} error={error}>
      <Field label="Naam" htmlFor="sn-name">
        <Input id="sn-name" value={name} onChange={(e) => setName(e.target.value)} autoFocus />
      </Field>
      <Field label="Beschrijving" htmlFor="sn-desc">
        <Textarea id="sn-desc" rows={2} value={description} onChange={(e) => setDescription(e.target.value)} />
      </Field>
      {ctx.type === "qemu" && ctx.running && (
        <Check id="sn-vmstate" checked={vmstate} onChange={setVmstate}>
          Ook het geheugen bewaren (terugzetten gaat dan verder waar de VM was; duurt langer en neemt evenveel ruimte als het RAM)
        </Check>
      )}
    </FormModal>
  );
}

function DescribeSnapshot({ ctx, snap, onClose }: { ctx: GuestCtx; snap: PveSnapshotEntry; onClose: () => void }) {
  const [description, setDescription] = useState(snap.description ?? "");
  return (
    <FormModal
      title={`Beschrijving van ${snap.name}`}
      onClose={onClose}
      submitLabel="Opslaan"
      disabled={ctx.pending}
      onSubmit={() =>
        void ctx.run({ method: "set", path: `${ctx.path}/snapshot/${snap.name}/config`, params: { description } }).then((ok) => ok && onClose())
      }
    >
      <Field label="Beschrijving" htmlFor="sd-desc">
        <Textarea id="sd-desc" rows={3} value={description} onChange={(e) => setDescription(e.target.value)} autoFocus />
      </Field>
    </FormModal>
  );
}

function Rollback({ ctx, name, onClose }: { ctx: GuestCtx; name: string; onClose: () => void }) {
  const [start, setStart] = useState(ctx.running);
  return (
    <TypedConfirm
      title={`Terugzetten naar ${name}`}
      expect={name}
      confirmLabel="Terugzetten"
      pending={ctx.pending}
      onClose={onClose}
      onConfirm={(typed) =>
        void ctx
          .run({ method: "create", path: `${ctx.path}/snapshot/${name}/rollback`, params: start ? { start: "1" } : {}, confirm: typed })
          .then((ok) => ok && onClose())
      }
    >
      <p>Alles wat na deze snapshot veranderde, gaat verloren. De gast wordt eerst gestopt.</p>
      <Check id="rb-start" checked={start} onChange={setStart}>
        Daarna weer starten
      </Check>
    </TypedConfirm>
  );
}
