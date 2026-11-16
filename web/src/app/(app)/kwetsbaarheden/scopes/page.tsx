"use client";

import { useState } from "react";
import { askConfirm } from "@/components/Confirm";
import { Modal } from "@/components/Modal";
import { ago, tableClass, tdClass, thClass } from "@/components/inventory/bits";
import { Alert, Badge, Button, Card, Field, Input, PageHeader, Textarea, cx } from "@/components/ui";
import { useIsAdmin } from "@/lib/inventory";
import { useDeleteScope, useSaveScope, useSecurityScopes, type SecurityScope } from "@/lib/security";

// Eigen scopes (fase 27): de netwerken die van jou zijn. Scans gaan later
// alleen naar adressen hierbinnen.

export default function ScopesPage() {
  const scopes = useSecurityScopes();
  const isAdmin = useIsAdmin();
  const [edit, setEdit] = useState<SecurityScope | "new" | null>(null);
  const remove = useDeleteScope();
  return (
    <div className="space-y-6">
      <PageHeader
        title="Eigen scopes"
        description="De netwerken en adressen die van jou zijn. Netwerkscans vanuit ClusterForge gaan alleen naar adressen binnen een scope en buiten de uitsluitingen; de agent die scant, controleert dat zelf ook nog eens. Een publiek adres vraagt een bevestiging."
        actions={isAdmin && <Button onClick={() => setEdit("new")}>Scope toevoegen</Button>}
      />
      {scopes.isError && <Alert>Scopes laden mislukte.</Alert>}
      {scopes.data && scopes.data.items.length === 0 && (
        <Alert kind="info">
          Nog geen scopes. Voeg je VLAN&apos;s toe, bijvoorbeeld 192.168.10.0/24, en sluit de gateways (.1 tot .3) uit als je die niet wilt laten
          scannen.
        </Alert>
      )}
      {scopes.data && scopes.data.items.length > 0 && (
        <Card>
          <div className="overflow-x-auto">
            <table className={tableClass}>
              <thead>
                <tr>
                  <th className={thClass}>Naam</th>
                  <th className={thClass}>Netwerken</th>
                  <th className={thClass}>Uitgesloten</th>
                  <th className={thClass}>Adressen</th>
                  <th className={thClass}>Gewijzigd</th>
                  <th className={thClass} />
                </tr>
              </thead>
              <tbody>
                {scopes.data.items.map((s) => (
                  <tr key={s.id} className="border-t border-slate-100 dark:border-slate-800">
                    <td className={tdClass}>
                      <div className="font-medium">{s.name}</div>
                      {s.description && <div className="text-xs text-slate-500">{s.description}</div>}
                      {s.public && (
                        <div className="mt-1" title={s.public_note}>
                          <Badge tone="amber">publiek adres</Badge>
                        </div>
                      )}
                    </td>
                    <td className={cx(tdClass, "font-mono text-xs")}>{s.cidrs.join(", ")}</td>
                    <td className={cx(tdClass, "font-mono text-xs")}>{s.exclude.join(", ") || "–"}</td>
                    <td className={cx(tdClass, "tabular-nums")}>{s.addresses.toLocaleString("nl-BE")}</td>
                    <td className={tdClass}>{ago(s.updated_at)}</td>
                    <td className={cx(tdClass, "whitespace-nowrap text-right")}>
                      {isAdmin && (
                        <div className="flex justify-end gap-2">
                          <Button variant="secondary" onClick={() => setEdit(s)}>
                            Wijzigen
                          </Button>
                          <Button
                            variant="secondary-danger"
                            onClick={async () => {
                              if (
                                await askConfirm(`Geplande scans op deze netwerken stoppen. Bevindingen blijven bewaard.`, {
                                  title: `Scope ${s.name} verwijderen?`,
                                  confirmLabel: "Verwijderen",
                                  danger: true,
                                })
                              )
                                remove.mutate(s.id);
                            }}
                          >
                            Verwijderen
                          </Button>
                        </div>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </Card>
      )}
      {edit && scopes.data && <ScopeForm scope={edit === "new" ? null : edit} confirmText={scopes.data.confirm_text} onClose={() => setEdit(null)} />}
      {remove.isError && <Alert>Verwijderen mislukte.</Alert>}
    </div>
  );
}

const lines = (s: string) =>
  s
    .split(/[\s,;]+/)
    .map((x) => x.trim())
    .filter(Boolean);

function ScopeForm({ scope, confirmText, onClose }: { scope: SecurityScope | null; confirmText: string; onClose: () => void }) {
  const save = useSaveScope();
  const [name, setName] = useState(scope?.name ?? "");
  const [description, setDescription] = useState(scope?.description ?? "");
  const [cidrs, setCidrs] = useState(scope?.cidrs.join("\n") ?? "");
  const [exclude, setExclude] = useState(scope?.exclude.join("\n") ?? "");
  const [confirm, setConfirm] = useState(scope?.public ? confirmText : "");
  const [note, setNote] = useState(scope?.public_note ?? "");
  const submit = () =>
    save.mutate(
      {
        id: scope?.id,
        body: {
          name,
          description,
          cidrs: lines(cidrs),
          exclude: lines(exclude),
          public_confirm: confirm,
          public_note: note,
        },
      },
      { onSuccess: onClose },
    );
  return (
    <Modal title={scope ? `Scope ${scope.name}` : "Nieuwe scope"} onClose={onClose} wide>
      <div className="space-y-3">
        <Field label="Naam" htmlFor="scope-naam">
          <Input id="scope-naam" value={name} onChange={(e) => setName(e.target.value)} maxLength={60} placeholder="Servers VLAN 10" />
        </Field>
        <Field label="Omschrijving" htmlFor="scope-omschrijving">
          <Input id="scope-omschrijving" value={description} onChange={(e) => setDescription(e.target.value)} maxLength={300} />
        </Field>
        <div className="grid gap-3 sm:grid-cols-2">
          <Field label="Netwerken en adressen" htmlFor="scope-cidrs" hint="Eén per regel, zoals 192.168.10.0/24 of 192.168.113.153. Hoogstens /16.">
            <Textarea id="scope-cidrs" rows={5} value={cidrs} onChange={(e) => setCidrs(e.target.value)} className="font-mono" />
          </Field>
          <Field label="Uitsluiten" htmlFor="scope-exclude" hint="Adressen die nooit gescand worden, zoals de gateways .1 tot .3.">
            <Textarea id="scope-exclude" rows={5} value={exclude} onChange={(e) => setExclude(e.target.value)} className="font-mono" />
          </Field>
        </div>
        <details className="rounded-md border border-slate-200 p-3 text-sm dark:border-slate-700" open={scope?.public}>
          <summary className="cursor-pointer font-medium">Publiek adres (alleen als het van jou is)</summary>
          <p className="mt-2 text-slate-500">
            Een adres buiten de privé-ranges scan je alleen als het van jou is of als je er schriftelijke toestemming voor hebt. Tik dan{" "}
            <span className="font-mono font-semibold">{confirmText}</span> over en schrijf waarom.
          </p>
          <div className="mt-2 grid gap-2 sm:grid-cols-2">
            <Field label="Bevestiging" htmlFor="scope-confirm">
              <Input id="scope-confirm" value={confirm} onChange={(e) => setConfirm(e.target.value)} />
            </Field>
            <Field label="Waarom is dit van jou?" htmlFor="scope-note">
              <Input id="scope-note" value={note} onChange={(e) => setNote(e.target.value)} maxLength={300} />
            </Field>
          </div>
        </details>
        {save.isError && <Alert>{save.error instanceof Error ? save.error.message : String(save.error)}</Alert>}
        <div className="flex justify-end gap-2">
          <Button variant="secondary" onClick={onClose}>
            Annuleren
          </Button>
          <Button onClick={submit} disabled={save.isPending || !name.trim() || lines(cidrs).length === 0}>
            Bewaren
          </Button>
        </div>
      </div>
    </Modal>
  );
}
