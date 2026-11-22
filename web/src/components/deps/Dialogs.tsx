"use client";

import { useState, type FormEvent } from "react";
import { Modal } from "@/components/Modal";
import { Alert, Button, Field, Input, Select, Textarea } from "@/components/ui";
import { ApiError } from "@/lib/api/client";
import {
  kinds,
  useCreateDependency,
  useCreateService,
  useDepGraph,
  useUpdateService,
  type DepService,
  type ServiceKind,
} from "@/lib/deps";
import { useClusters, useNodes } from "@/lib/inventory";

// Scope is waar een nieuwe dienst komt: een cluster, een losse node of extern.
export type Scope = { cluster_id?: string; node_id?: string; external?: boolean };

function scopeKey(s: Scope) {
  if (s.cluster_id) return `cluster:${s.cluster_id}`;
  if (s.node_id) return `node:${s.node_id}`;
  return "external";
}

function parseScope(key: string): Scope {
  const [kind, id] = key.split(":");
  if (kind === "cluster") return { cluster_id: id };
  if (kind === "node") return { node_id: id };
  return { external: true };
}

function errorOf(e: unknown) {
  return e instanceof ApiError ? { message: e.message, field: e.field } : e ? { message: String(e), field: undefined } : null;
}

// ServiceDialog maakt een dienst aan of bewerkt hem. Zonder vaste scope kiest
// de beheerder een cluster, een losse node of extern.
export function ServiceDialog({
  service,
  scope,
  onClose,
  onSaved,
}: {
  service?: DepService;
  scope?: Scope;
  onClose: () => void;
  onSaved?: (s: DepService) => void;
}) {
  const create = useCreateService();
  const update = useUpdateService();
  const clusters = useClusters();
  const nodes = useNodes();
  const editing = !!service;
  const [where, setWhere] = useState(
    service
      ? scopeKey({ cluster_id: service.cluster_id ?? undefined, node_id: service.node_id ?? undefined })
      : scope
        ? scopeKey(scope)
        : "",
  );
  const [name, setName] = useState(service?.name ?? "");
  const [kind, setKind] = useState<ServiceKind>(service?.kind ?? "app");
  const [unit, setUnit] = useState(service?.unit ?? "");
  const [port, setPort] = useState(service?.port ? String(service.port) : "");
  const [address, setAddress] = useState(service?.address ?? "");
  const [description, setDescription] = useState(service?.description ?? "");
  const external = where === "external";
  const err = errorOf(create.error ?? update.error);
  const loose = (nodes.data ?? []).filter((n) => !n.cluster_id);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    const p = port.trim() === "" ? null : Number(port);
    try {
      let saved: DepService;
      if (service) {
        saved = await update.mutateAsync({
          id: service.id,
          body: { name, kind, description, port: p, ...(external ? { address } : { unit }) },
        });
      } else {
        const s = parseScope(where);
        saved = await create.mutateAsync({
          name,
          kind,
          description,
          ...(p !== null ? { port: p } : {}),
          ...(s.cluster_id ? { cluster_id: s.cluster_id } : {}),
          ...(s.node_id ? { node_id: s.node_id } : {}),
          ...(external ? { address } : { unit }),
        });
      }
      onSaved?.(saved);
      onClose();
    } catch {
      // De fout staat in het venster.
    }
  };

  return (
    <Modal title={editing ? `${service.name} bewerken` : "Dienst toevoegen"} onClose={onClose}>
      <form onSubmit={submit} className="space-y-4">
        {err && <Alert>{err.message}</Alert>}
        {!editing && !scope && (
          <Field label="Waar" htmlFor="svc-where" hint="Een losse node is een node zonder cluster.">
            <Select id="svc-where" value={where} onChange={(e) => setWhere(e.target.value)} required>
              <option value="" disabled>
                Kies…
              </option>
              <optgroup label="Clusters">
                {(clusters.data ?? []).map((c) => (
                  <option key={c.id} value={`cluster:${c.id}`}>
                    {c.name}
                  </option>
                ))}
              </optgroup>
              {loose.length > 0 && (
                <optgroup label="Losse nodes">
                  {loose.map((n) => (
                    <option key={n.id} value={`node:${n.id}`}>
                      {n.hostname}
                    </option>
                  ))}
                </optgroup>
              )}
              <option value="external">Extern, buiten ClusterForge</option>
            </Select>
          </Field>
        )}
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label="Naam" htmlFor="svc-name" hint="Letters, cijfers en @ . _ : / -">
            <Input id="svc-name" value={name} onChange={(e) => setName(e.target.value)} required maxLength={63} autoFocus />
          </Field>
          <Field label="Soort" htmlFor="svc-kind">
            <Select id="svc-kind" value={kind} onChange={(e) => setKind(e.target.value as ServiceKind)}>
              {kinds.map((k) => (
                <option key={k.value} value={k.value}>
                  {k.label}
                </option>
              ))}
            </Select>
          </Field>
        </div>
        {external ? (
          <div className="grid gap-4 sm:grid-cols-[1fr_8rem]">
            <Field label="Adres" htmlFor="svc-address" hint="IP-adres of hostname">
              <Input id="svc-address" value={address} onChange={(e) => setAddress(e.target.value)} required />
            </Field>
            <Field label="Poort" htmlFor="svc-port">
              <Input id="svc-port" type="number" min={1} max={65535} value={port} onChange={(e) => setPort(e.target.value)} required />
            </Field>
          </div>
        ) : (
          <div className="grid gap-4 sm:grid-cols-[1fr_8rem]">
            <Field
              label="Systemd-unit"
              htmlFor="svc-unit"
              hint="Zonder .service, zoals nginx. Met een unit volgt ClusterForge de status op elke node; leeg blijft de status onbekend."
            >
              <Input id="svc-unit" value={unit} onChange={(e) => setUnit(e.target.value)} />
            </Field>
            <Field label="Poort" htmlFor="svc-port">
              <Input id="svc-port" type="number" min={1} max={65535} value={port} onChange={(e) => setPort(e.target.value)} />
            </Field>
          </div>
        )}
        <Field label="Beschrijving" htmlFor="svc-desc">
          <Textarea id="svc-desc" rows={2} value={description} onChange={(e) => setDescription(e.target.value)} maxLength={500} />
        </Field>
        <div className="flex justify-end gap-2">
          <Button type="button" variant="secondary" onClick={onClose}>
            Annuleren
          </Button>
          <Button type="submit" disabled={create.isPending || update.isPending || (!editing && where === "")}>
            {editing ? "Opslaan" : "Toevoegen"}
          </Button>
        </div>
      </form>
    </Modal>
  );
}

// DependencyDialog legt een pijl vast. Met from kiest de beheerder waar die
// dienst van afhangt, met to wie hem gebruikt, en met choices eerst de
// afnemer uit die lijst. Een nieuwe externe dienst kan meteen mee.
export function DependencyDialog({
  from,
  to,
  choices,
  onClose,
}: {
  from?: DepService;
  to?: DepService;
  choices?: DepService[];
  onClose: () => void;
}) {
  const graph = useDepGraph({ include_suggested: true });
  const createDep = useCreateDependency();
  const createSvc = useCreateService();
  const [consumer, setConsumer] = useState(from?.id ?? "");
  const [other, setOther] = useState("");
  const [strength, setStrength] = useState<"hard" | "soft">("hard");
  const [note, setNote] = useState("");
  const [ext, setExt] = useState({ name: "", kind: "storage" as ServiceKind, address: "", port: "" });
  const fixedId = from?.id ?? to?.id ?? consumer;
  const newExternal = other === "new-external";
  const err = errorOf(createSvc.error ?? createDep.error);
  const groups = graph.data?.groups ?? [];
  const services = (graph.data?.services ?? []).filter((s) => s.id !== fixedId && s.state !== "ignored");
  const picking = !from && !to;

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    try {
      let otherId = other;
      if (newExternal) {
        const s = await createSvc.mutateAsync({ name: ext.name, kind: ext.kind, address: ext.address, port: Number(ext.port) });
        otherId = s.id;
      }
      await createDep.mutateAsync({
        from_service_id: to ? otherId : consumer,
        to_service_id: to ? to.id : otherId,
        strength,
        note,
      });
      onClose();
    } catch {
      // De fout staat in het venster.
    }
  };

  const title = to ? `Wie gebruikt ${to.name}?` : from ? `Waar hangt ${from.name} van af?` : "Afhankelijkheid toevoegen";
  return (
    <Modal title={title} onClose={onClose}>
      <form onSubmit={submit} className="space-y-4">
        {err && <Alert>{err.message}</Alert>}
        {picking && (
          <Field label="Afnemer" htmlFor="dep-from" hint="De dienst die van een andere afhangt.">
            <Select id="dep-from" value={consumer} onChange={(e) => setConsumer(e.target.value)} required>
              <option value="" disabled>
                Kies…
              </option>
              {(choices ?? []).map((s) => (
                <option key={s.id} value={s.id}>
                  {s.name}
                </option>
              ))}
            </Select>
          </Field>
        )}
        <Field label={to ? "Afnemer" : "Leverancier"} htmlFor="dep-other" hint="Per cluster gegroepeerd. Voorstellen staan erbij.">
          <Select id="dep-other" value={other} onChange={(e) => setOther(e.target.value)} required>
            <option value="" disabled>
              Kies een dienst…
            </option>
            {groups.map((g) => {
              const items = services.filter((s) => s.group_id === g.id);
              if (items.length === 0) return null;
              return (
                <optgroup key={g.id} label={g.name}>
                  {items.map((s) => (
                    <option key={s.id} value={s.id}>
                      {s.name}
                      {s.state === "suggested" ? " (voorstel)" : ""}
                    </option>
                  ))}
                </optgroup>
              );
            })}
            {!to && <option value="new-external">Nieuwe externe dienst…</option>}
          </Select>
        </Field>
        {newExternal && (
          <div className="grid gap-3 rounded-md border border-slate-200 p-3 sm:grid-cols-2 dark:border-slate-800">
            <Field label="Naam" htmlFor="ext-name">
              <Input id="ext-name" value={ext.name} onChange={(e) => setExt({ ...ext, name: e.target.value })} required maxLength={63} />
            </Field>
            <Field label="Soort" htmlFor="ext-kind">
              <Select id="ext-kind" value={ext.kind} onChange={(e) => setExt({ ...ext, kind: e.target.value as ServiceKind })}>
                {kinds.map((k) => (
                  <option key={k.value} value={k.value}>
                    {k.label}
                  </option>
                ))}
              </Select>
            </Field>
            <Field label="Adres" htmlFor="ext-address">
              <Input id="ext-address" value={ext.address} onChange={(e) => setExt({ ...ext, address: e.target.value })} required />
            </Field>
            <Field label="Poort" htmlFor="ext-port">
              <Input id="ext-port" type="number" min={1} max={65535} value={ext.port} onChange={(e) => setExt({ ...ext, port: e.target.value })} required />
            </Field>
          </div>
        )}
        <fieldset>
          <legend className="mb-1 block text-sm font-medium text-slate-700 dark:text-slate-300">Sterkte</legend>
          <div className="space-y-1.5 text-sm">
            <label className="flex items-start gap-2">
              <input type="radio" name="strength" checked={strength === "hard"} onChange={() => setStrength("hard")} className="mt-1" />
              <span>
                <span className="font-medium">Hard</span>: valt de leverancier uit, dan werkt de afnemer niet.
              </span>
            </label>
            <label className="flex items-start gap-2">
              <input type="radio" name="strength" checked={strength === "soft"} onChange={() => setStrength("soft")} className="mt-1" />
              <span>
                <span className="font-medium">Zacht</span>: de afnemer werkt verminderd, bijvoorbeeld zonder cache.
              </span>
            </label>
          </div>
        </fieldset>
        <Field label="Notitie" htmlFor="dep-note">
          <Input id="dep-note" value={note} onChange={(e) => setNote(e.target.value)} maxLength={500} placeholder="Waarom, bijvoorbeeld de database van de webshop" />
        </Field>
        <div className="flex justify-end gap-2">
          <Button type="button" variant="secondary" onClick={onClose}>
            Annuleren
          </Button>
          <Button type="submit" disabled={createDep.isPending || createSvc.isPending}>
            Toevoegen
          </Button>
        </div>
      </form>
    </Modal>
  );
}
