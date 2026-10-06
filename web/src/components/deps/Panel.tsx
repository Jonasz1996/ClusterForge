"use client";

import { useState } from "react";
import { EnvBadge } from "@/components/inventory/bits";
import { Modal } from "@/components/Modal";
import { Alert, Badge, Button, cx } from "@/components/ui";
import { ApiError } from "@/lib/api/client";
import {
  impacts,
  kindLabel,
  label,
  serviceStatuses,
  sources,
  strengthLabel,
  useDeleteDependency,
  useDeleteService,
  useImpact,
  useUpdateService,
  type DepEdge,
  type DepService,
  type Index,
} from "@/lib/deps";
import { DependencyDialog, ServiceDialog } from "./Dialogs";
import { ImpactList } from "./ImpactList";

export function StatusDot({ status, className }: { status: DepService["status"]; className?: string }) {
  return <span className={cx("inline-block size-2 shrink-0 rounded-full", serviceStatuses[status].dot, className)} aria-hidden />;
}

// ServiceStatusText is de eigen status met de doorgegeven uitval erbij.
export function ServiceStatusText({ s }: { s: DepService }) {
  const st = serviceStatuses[s.status];
  return (
    <span className="inline-flex flex-wrap items-center gap-1.5" title={s.status_reason || undefined}>
      <Badge tone={st.tone}>
        <StatusDot status={s.status} className="mr-1 size-1.5" />
        {st.label}
      </Badge>
      {s.impact !== "none" && (
        <span title={s.impact_reason}>
          <Badge tone={impacts[s.impact].tone}>{impacts[s.impact].label}</Badge>
        </span>
      )}
    </span>
  );
}

const instanceText = (state: string) => (state === "" ? "geen heartbeat" : state);

// ServicePanel is het zijpaneel bij een dienst: status, instanties, pijlen,
// de knop Wat raakt uitval? en voor een beheerder het beheer.
export function ServicePanel({
  s,
  idx,
  isAdmin,
  impactOn,
  onImpact,
  onClose,
}: {
  s: DepService;
  idx: Index;
  isAdmin: boolean;
  impactOn: boolean;
  onImpact: (on: boolean) => void;
  onClose: () => void;
}) {
  const group = idx.groups.get(s.group_id);
  const providers = idx.providers.get(s.id) ?? [];
  const consumers = idx.consumers.get(s.id) ?? [];
  const impact = useImpact(impactOn ? { service_id: s.id } : null);
  const [dialog, setDialog] = useState<"" | "edit" | "provider" | "consumer" | "delete">("");
  const update = useUpdateService();
  const err = update.error instanceof ApiError ? update.error.message : null;
  const setState = (state: "confirmed" | "ignored" | "suggested") => update.mutate({ id: s.id, body: { state } });

  return (
    <aside className="space-y-4 text-sm" aria-label={`Dienst ${s.name}`}>
      <div className="flex items-start justify-between gap-2">
        <div className="min-w-0">
          <h2 className="text-base font-semibold break-words">{s.name}</h2>
          <div className="mt-0.5 flex flex-wrap items-center gap-1.5 text-slate-500">
            <span>{kindLabel(s.kind)}</span>
            <span>·</span>
            <span>{group?.name}</span>
            {group?.environment && <EnvBadge env={group.environment} />}
          </div>
        </div>
        <Button variant="ghost" className="px-2 py-1" onClick={onClose} aria-label="Sluiten">
          ✕
        </Button>
      </div>
      {err && <Alert>{err}</Alert>}
      {s.state !== "confirmed" && (
        <Alert kind="info">
          {s.state === "suggested"
            ? `Voorstel: ClusterForge zag ${s.unit} ${s.instances.length ? "op " + s.instances.map((i) => i.hostname).join(", ") : ""}. Pas na bevestigen telt de dienst mee.`
            : "Genegeerd: deze dienst telt niet mee en wordt niet opnieuw voorgesteld."}
        </Alert>
      )}
      <div className="flex flex-wrap gap-2">
        {isAdmin && s.state === "suggested" && (
          <>
            <Button onClick={() => setState("confirmed")} disabled={update.isPending}>
              Bevestigen
            </Button>
            <Button variant="secondary" onClick={() => setState("ignored")} disabled={update.isPending}>
              Negeren
            </Button>
          </>
        )}
        {isAdmin && s.state === "ignored" && (
          <Button variant="secondary" onClick={() => setState(s.source === "discovered" ? "suggested" : "confirmed")} disabled={update.isPending}>
            Terugzetten
          </Button>
        )}
        <Button variant={impactOn ? "primary" : "secondary"} onClick={() => onImpact(!impactOn)} aria-pressed={impactOn}>
          {impactOn ? "Impact verbergen" : "Wat raakt uitval?"}
        </Button>
        {isAdmin && (
          <>
            <Button variant="secondary" onClick={() => setDialog("edit")}>
              Bewerken
            </Button>
            <Button variant="secondary-danger" onClick={() => setDialog("delete")}>
              Verwijderen
            </Button>
          </>
        )}
      </div>
      {impactOn && impact.data && <ImpactList impact={impact.data} />}

      <dl className="grid grid-cols-[7rem_1fr] gap-x-3 gap-y-1.5">
        <dt className="text-slate-500">Status</dt>
        <dd>
          <ServiceStatusText s={s} />
          {s.status_reason && <p className="mt-1 text-xs text-slate-500">{s.status_reason}</p>}
          {s.impact !== "none" && (
            <p className={cx("mt-1 text-xs", s.impact === "down" ? "text-red-700 dark:text-red-300" : "text-amber-700 dark:text-amber-300")}>
              {s.impact_reason}
            </p>
          )}
        </dd>
        {s.unit && (
          <>
            <dt className="text-slate-500">Unit</dt>
            <dd className="font-mono text-xs">{s.unit}</dd>
          </>
        )}
        {(s.address || s.port) && (
          <>
            <dt className="text-slate-500">{s.address ? "Adres" : "Poort"}</dt>
            <dd className="font-mono text-xs">{s.address ? `${s.address}:${s.port}` : s.port}</dd>
          </>
        )}
        <dt className="text-slate-500">Bron</dt>
        <dd>{sources[s.source]}</dd>
        {s.description && (
          <>
            <dt className="text-slate-500">Beschrijving</dt>
            <dd className="whitespace-pre-wrap">{s.description}</dd>
          </>
        )}
      </dl>

      {s.instances.length > 0 && (
        <section>
          <h3 className="mb-1 font-medium">Instanties</h3>
          <ul className="space-y-1">
            {s.instances.map((i) => (
              <li key={i.node_id} className="flex items-center gap-2">
                <StatusDot status={i.running ? "healthy" : i.state === "" ? "unknown" : "down"} />
                <span>{i.hostname}</span>
                <span className="text-xs text-slate-500">{instanceText(i.state)}</span>
              </li>
            ))}
          </ul>
        </section>
      )}

      <EdgeList
        title="Hangt af van"
        edges={providers}
        other={(e) => e.to}
        idx={idx}
        from={s}
        isAdmin={isAdmin}
        onAdd={isAdmin && s.state !== "ignored" ? () => setDialog("provider") : undefined}
      />
      <EdgeList
        title="Gebruikt door"
        edges={consumers}
        other={(e) => e.from}
        idx={idx}
        from={s}
        isAdmin={isAdmin}
        onAdd={isAdmin && s.state !== "ignored" ? () => setDialog("consumer") : undefined}
      />

      <p className="text-xs text-slate-500">
        Alleen bekende afhankelijkheden: wat niet in de graaf staat, telt niet mee. De graaf beslist nooit of een actie mag.
      </p>

      {dialog === "edit" && <ServiceDialog service={s} onClose={() => setDialog("")} />}
      {dialog === "provider" && <DependencyDialog from={s} onClose={() => setDialog("")} />}
      {dialog === "consumer" && <DependencyDialog to={s} onClose={() => setDialog("")} />}
      {dialog === "delete" && <DeleteService s={s} arrows={providers.length + consumers.length} onClose={() => setDialog("")} onDeleted={onClose} />}
    </aside>
  );
}

function EdgeList({
  title,
  edges,
  other,
  idx,
  from,
  isAdmin,
  onAdd,
}: {
  title: string;
  edges: DepEdge[];
  other: (e: DepEdge) => string;
  idx: Index;
  from: DepService;
  isAdmin: boolean;
  onAdd?: () => void;
}) {
  const remove = useDeleteDependency();
  return (
    <section>
      <div className="mb-1 flex items-center justify-between gap-2">
        <h3 className="font-medium">{title}</h3>
        {onAdd && (
          <Button variant="ghost" className="px-2 py-1 text-xs" onClick={onAdd}>
            Toevoegen
          </Button>
        )}
      </div>
      {edges.length === 0 ? (
        <p className="text-slate-500">Niets bekend.</p>
      ) : (
        <ul className="space-y-1">
          {edges.map((e) => {
            const o = idx.services.get(other(e));
            return (
              <li key={e.id} className="flex items-start justify-between gap-2">
                <span className={cx(e.affected && "text-red-700 dark:text-red-300")}>
                  {o && <StatusDot status={o.status} className="mr-1.5 align-middle" />}
                  {label(idx, other(e), from.group_id)}
                  <span className="text-xs text-slate-500">
                    {" "}
                    · {strengthLabel(e.strength)} · {sources[e.source]}
                    {e.note && ` · ${e.note}`}
                  </span>
                </span>
                {isAdmin && (
                  <button
                    type="button"
                    className="text-xs text-slate-400 hover:text-red-600"
                    title="Afhankelijkheid verwijderen"
                    aria-label={`Afhankelijkheid met ${o?.name ?? "dienst"} verwijderen`}
                    onClick={() => remove.mutate(e.id)}
                  >
                    ✕
                  </button>
                )}
              </li>
            );
          })}
        </ul>
      )}
    </section>
  );
}

function DeleteService({ s, arrows, onClose, onDeleted }: { s: DepService; arrows: number; onClose: () => void; onDeleted: () => void }) {
  const remove = useDeleteService();
  const err = remove.error instanceof ApiError ? remove.error.message : null;
  const kept = s.unit !== "" && s.state !== "ignored" && !s.address;
  return (
    <Modal title={`${s.name} verwijderen?`} onClose={onClose}>
      <div className="space-y-3 text-sm">
        {err && <Alert>{err}</Alert>}
        <p>
          {arrows > 0
            ? `De ${arrows === 1 ? "afhankelijkheid verdwijnt" : `${arrows} afhankelijkheden verdwijnen`} mee.`
            : "Er hangt niets van deze dienst af."}
          {kept && " De dienst heeft een unit en blijft als genegeerd staan, zodat ClusterForge hem niet opnieuw voorstelt."}
        </p>
        <div className="flex justify-end gap-2">
          <Button variant="secondary" onClick={onClose}>
            Annuleren
          </Button>
          <Button
            variant="danger"
            disabled={remove.isPending}
            onClick={() =>
              remove.mutate(s.id, {
                onSuccess: () => {
                  onClose();
                  onDeleted();
                },
              })
            }
          >
            Verwijderen
          </Button>
        </div>
      </div>
    </Modal>
  );
}
