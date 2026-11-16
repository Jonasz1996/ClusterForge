"use client";

import Link from "next/link";
import { useState } from "react";
import { Empty, formatBytes, tableClass, tdClass, thClass } from "@/components/inventory/bits";
import { ActionMenu, TypedConfirm, useCallRunner } from "@/components/pve/bits";
import { holds, useNodeBits } from "@/components/pve/CreateGuest";
import { Check, FormModal } from "@/components/pve/guest/common";
import { Alert, Badge, Button, Card, cx, Field, Input, Select, Textarea } from "@/components/ui";
import { useIsAdmin } from "@/lib/inventory";
import {
  backupKind,
  backupStores,
  guestHref,
  guestLabel,
  useBackups,
  usePveGet,
  useRestoreTest,
  type Datacenter,
  type GuestType,
  type PveBackup,
  type PveResource,
} from "@/lib/pve";

// Back-ups lezen, terugzetten, op proef terugzetten en een back-up nu maken
// (fase 26 M3).

export function when(t?: number) {
  if (!t) return "–";
  return new Date(t * 1000).toLocaleString("nl-BE", { dateStyle: "short", timeStyle: "short" });
}

function age(t: number) {
  const d = (Date.now() / 1000 - t) / 86400;
  return d;
}

export function VerifyBadge({ b }: { b: PveBackup }) {
  const s = b.verification?.state;
  if (s === "ok") return <Badge tone="green">geverifieerd</Badge>;
  if (s === "failed") return <Badge tone="red">verificatie mislukt</Badge>;
  if (b.format === "pbs-vm" || b.format === "pbs-ct" || b.volid.includes(":backup/")) return <Badge tone="amber">niet geverifieerd</Badge>;
  return null;
}

export function BackupList({ dc, res, vmid }: { dc: Datacenter; res: PveResource[]; vmid?: number }) {
  const isAdmin = useIsAdmin();
  const stores = backupStores(res);
  const [store, setStore] = useState("");
  const [query, setQuery] = useState("");
  const shown = store ? stores.filter((s) => `${s.node}/${s.storage}` === store) : stores;
  const b = useBackups(dc.id, shown, vmid);
  const { run, pending, banner } = useCallRunner(dc.id);
  const [dialog, setDialog] = useState<{ kind: "restore" | "test" | "notes" | "delete"; b: PveBackup } | { kind: "now" } | null>(null);
  const close = () => setDialog(null);
  const guests = new Map(res.filter((r) => r.type === "qemu" || r.type === "lxc").map((r) => [r.vmid, r]));
  const q = query.trim().toLowerCase();
  const items = b.items.filter((x) => !q || String(x.vmid).includes(q) || (guests.get(x.vmid)?.name ?? "").toLowerCase().includes(q) || (x.notes ?? "").toLowerCase().includes(q));

  return (
    <Card
      title={
        <span className="flex flex-wrap items-center justify-between gap-2">
          <span>Back-ups</span>
          {isAdmin && (
            <Button variant="secondary" disabled={pending} onClick={() => setDialog({ kind: "now" })}>
              Nu een back-up maken
            </Button>
          )}
        </span>
      }
    >
      <div className="space-y-3">
        {banner}
        {stores.length === 0 ? (
          <Empty>Geen storage voor back-ups. Voeg er een toe (bijvoorbeeld Proxmox Backup Server) in de Proxmox-instellingen van de storage.</Empty>
        ) : (
          <>
            <div className="flex flex-wrap items-center gap-2">
              <Select aria-label="Storage" value={store} onChange={(e) => setStore(e.target.value)} className="w-auto">
                <option value="">Alle back-upopslag ({stores.length})</option>
                {stores.map((s) => (
                  <option key={`${s.node}/${s.storage}`} value={`${s.node}/${s.storage}`}>
                    {s.storage}
                    {s.shared ? "" : ` op ${s.node}`}
                    {s.type ? ` (${s.type})` : ""}
                  </option>
                ))}
              </Select>
              {!vmid && <Input aria-label="Zoeken" placeholder="Zoek op ID, naam of notitie" value={query} onChange={(e) => setQuery(e.target.value)} className="w-full sm:w-72" />}
              <span className="text-xs text-slate-500">{b.loading ? "Laden…" : `${items.length} back-up${items.length === 1 ? "" : "s"}`}</span>
            </div>
            {b.errors.length > 0 && <Alert>{b.errors.join(" · ")}</Alert>}
            {!b.loading && items.length === 0 ? (
              <Empty>{vmid ? "Nog geen back-ups van deze gast." : "Geen back-ups gevonden."}</Empty>
            ) : (
              <div className="relative -mx-4 overflow-x-auto sm:mx-0">
                <table className={tableClass}>
                  <thead>
                    <tr>
                      {!vmid && <th className={thClass}>Gast</th>}
                      <th className={thClass}>Gemaakt</th>
                      <th className={thClass}>Grootte</th>
                      <th className={thClass}>Storage</th>
                      <th className={thClass}>Notitie</th>
                      <th className={thClass}>Staat</th>
                      <th className={thClass}>
                        <span className="sr-only">Acties</span>
                      </th>
                    </tr>
                  </thead>
                  <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
                    {items.map((x) => {
                      const g = guests.get(x.vmid);
                      const kind = backupKind(x);
                      const old = age(x.ctime) > 8;
                      return (
                        <tr key={`${x.storage}/${x.volid}`}>
                          {!vmid && (
                            <td className={tdClass}>
                              {g ? (
                                <Link href={guestHref(dc.id, { node: g.node, type: g.type, vmid: g.vmid })} className="font-medium hover:underline">
                                  {guestLabel(kind)} {x.vmid} {g.name}
                                </Link>
                              ) : (
                                <span>
                                  {guestLabel(kind)} {x.vmid} <span className="text-xs text-slate-500">(bestaat niet meer)</span>
                                </span>
                              )}
                            </td>
                          )}
                          <td className={cx(tdClass, "whitespace-nowrap", old && "text-amber-700 dark:text-amber-300")} title={old ? "Ouder dan een week" : undefined}>
                            {when(x.ctime)}
                          </td>
                          <td className={tdClass + " whitespace-nowrap tabular-nums"}>{formatBytes(x.size)}</td>
                          <td className={tdClass + " text-xs"}>{x.storage}</td>
                          <td className={tdClass + " max-w-64 text-xs break-words text-slate-600 dark:text-slate-300"}>{x.notes || "–"}</td>
                          <td className={tdClass}>
                            <span className="flex flex-wrap gap-1">
                              <VerifyBadge b={x} />
                              {x.protected ? <Badge tone="blue">beschermd</Badge> : null}
                              {x.encrypted ? <Badge>versleuteld</Badge> : null}
                            </span>
                          </td>
                          <td className={tdClass + " text-right"}>
                            {isAdmin && (
                              <ActionMenu
                                disabled={pending}
                                items={[
                                  { label: "Terugzetten…", onClick: () => setDialog({ kind: "restore", b: x }) },
                                  { label: "Terugzettest…", onClick: () => setDialog({ kind: "test", b: x }) },
                                  { label: "Notitie en bescherming", onClick: () => setDialog({ kind: "notes", b: x }) },
                                  ...(x.protected ? [] : [{ label: "Verwijderen…", onClick: () => setDialog({ kind: "delete", b: x }), danger: true }]),
                                ]}
                              />
                            )}
                          </td>
                        </tr>
                      );
                    })}
                  </tbody>
                </table>
              </div>
            )}
          </>
        )}
      </div>
      {dialog?.kind === "restore" && <RestoreDialog dc={dc} res={res} b={dialog.b} run={run} pending={pending} onClose={close} />}
      {dialog?.kind === "test" && <RestoreTestDialog dc={dc} b={dialog.b} onClose={close} />}
      {dialog?.kind === "notes" && <NotesDialog b={dialog.b} run={run} pending={pending} onClose={close} />}
      {dialog?.kind === "now" && <BackupNowDialog res={res} vmid={vmid} run={run} pending={pending} onClose={close} />}
      {dialog?.kind === "delete" && (
        <TypedConfirm
          title="Back-up verwijderen"
          expect={dialog.b.volid}
          confirmLabel="Verwijderen"
          pending={pending}
          onClose={close}
          onConfirm={(typed) =>
            void run({ method: "delete", path: `/nodes/${dialog.b.node}/storage/${dialog.b.storage}/content/${dialog.b.volid}`, confirm: typed }).then(
              (ok) => ok && close(),
            )
          }
        >
          <p>
            De back-up van {when(dialog.b.ctime)} ({formatBytes(dialog.b.size)}) wordt gewist. Dat kan niet ongedaan gemaakt worden.
          </p>
        </TypedConfirm>
      )}
    </Card>
  );
}

type Runner = ReturnType<typeof useCallRunner>["run"];

function RestoreDialog({ dc, res, b, run, pending, onClose }: { dc: Datacenter; res: PveResource[]; b: PveBackup; run: Runner; pending: boolean; onClose: () => void }) {
  const kind = backupKind(b);
  const existing = res.find((r) => (r.type === "qemu" || r.type === "lxc") && r.vmid === b.vmid);
  const nextid = usePveGet<string | number>(dc.id, "/cluster/nextid", { refetch: false });
  const [mode, setMode] = useState<"new" | "over">(existing ? "new" : "over");
  const online = dc.hosts.filter((h) => h.online).map((h) => h.pve_node);
  const [node, setNode] = useState(b.node && online.includes(b.node) ? b.node : (online[0] ?? ""));
  const target = mode === "over" && existing ? existing.node : node;
  const bits = useNodeBits(dc.id, target);
  const stores = bits.storages.filter((s) => holds(s, kind === "qemu" ? "images" : "rootdir"));
  const [storage, setStorage] = useState("");
  const [typedId, setTypedId] = useState<string | null>(null);
  const newid = typedId ?? (nextid.data !== undefined ? String(nextid.data) : "");
  const [start, setStart] = useState(false);
  const [typed, setTyped] = useState("");
  const vmid = mode === "over" ? String(b.vmid ?? "") : newid;
  const taken = mode === "new" && res.some((r) => String(r.vmid) === newid);
  const error = !/^\d+$/.test(vmid) || Number(vmid) < 100
    ? "Geef een ID vanaf 100."
    : taken
      ? `ID ${newid} bestaat al.`
      : mode === "over" && existing && existing.status === "running"
        ? `${guestLabel(kind)} ${b.vmid} draait; zet hem eerst uit om eroverheen terug te zetten.`
        : null;
  const submit = () => {
    const p: Record<string, string> = { vmid };
    if (kind === "qemu") p.archive = b.volid;
    else Object.assign(p, { ostemplate: b.volid, restore: "1" });
    if (storage) p.storage = storage;
    if (mode === "new") p.unique = "1";
    if (mode === "over" && existing) p.force = "1";
    if (start) p.start = "1";
    void run({ method: "create", path: `/nodes/${target}/${kind}`, params: p, confirm: mode === "over" && existing ? typed.trim() : undefined }).then((ok) => ok && onClose());
  };
  const needTyped = mode === "over" && !!existing;
  return (
    <FormModal
      title={`Terugzetten: ${guestLabel(kind)} ${b.vmid ?? ""} van ${when(b.ctime)}`}
      onClose={onClose}
      onSubmit={submit}
      submitLabel="Terugzetten"
      disabled={pending || (needTyped && typed.trim() !== vmid)}
      error={error}
    >
      {existing && (
        <fieldset className="space-y-2 text-sm">
          <label className="flex items-start gap-2">
            <input type="radio" name="rs-mode" checked={mode === "new"} onChange={() => setMode("new")} className="mt-0.5" />
            <span>Als nieuwe gast naast {existing.name ?? b.vmid} (nieuwe MAC-adressen; zet het netwerk zelf goed voor je hem start)</span>
          </label>
          <label className="flex items-start gap-2">
            <input type="radio" name="rs-mode" checked={mode === "over"} onChange={() => setMode("over")} className="mt-0.5" />
            <span>
              Over {guestLabel(kind)} {b.vmid} {existing.name} heen: <strong>alles van nu gaat verloren</strong>
            </span>
          </label>
        </fieldset>
      )}
      <div className="grid gap-3 sm:grid-cols-2">
        {mode === "new" ? (
          <>
            <Field label="Nieuw ID" htmlFor="rs-id">
              <Input id="rs-id" inputMode="numeric" value={newid} onChange={(e) => setTypedId(e.target.value)} />
            </Field>
            <Field label="Host" htmlFor="rs-node">
              <Select id="rs-node" value={node} onChange={(e) => setNode(e.target.value)}>
                {online.map((n) => (
                  <option key={n}>{n}</option>
                ))}
              </Select>
            </Field>
          </>
        ) : (
          <p className="text-sm sm:col-span-2">
            Op {target}, met ID {vmid}.
          </p>
        )}
        <Field label="Storage" htmlFor="rs-st" hint="Leeg: dezelfde als in de back-up">
          <Select id="rs-st" value={storage} onChange={(e) => setStorage(e.target.value)}>
            <option value="">zoals in de back-up</option>
            {stores.map((s) => (
              <option key={s.storage} value={s.storage}>
                {s.storage}
                {s.avail ? ` (${formatBytes(s.avail)} vrij)` : ""}
              </option>
            ))}
          </Select>
        </Field>
      </div>
      <Check id="rs-start" checked={start} onChange={setStart}>
        Daarna starten
      </Check>
      {needTyped && (
        <Field label={`Tik ${vmid} over om te bevestigen`} htmlFor="rs-typed">
          <Input id="rs-typed" value={typed} onChange={(e) => setTyped(e.target.value)} autoComplete="off" />
        </Field>
      )}
    </FormModal>
  );
}

function RestoreTestDialog({ dc, b, onClose }: { dc: Datacenter; b: PveBackup; onClose: () => void }) {
  const kind = backupKind(b);
  const ready = dc.hosts.filter((h) => h.online && h.protocol >= 35).map((h) => h.pve_node);
  const [node, setNode] = useState(ready.includes(b.node) ? b.node : (ready[0] ?? ""));
  const bits = useNodeBits(dc.id, node);
  const stores = bits.storages.filter((s) => holds(s, kind === "qemu" ? "images" : "rootdir"));
  const [storage, setStorage] = useState("");
  const st = storage || stores[0]?.storage || "";
  const test = useRestoreTest(dc.id);
  const [done, setDone] = useState<{ id: string; title: string } | null>(null);
  if (done)
    return (
      <FormModal title="Terugzettest gestart" onClose={onClose} onSubmit={onClose} submitLabel="Sluiten">
        <Alert kind="success">
          {done.title}.{" "}
          <Link href={`/taken/detail?id=${done.id}`} className="underline">
            Volgen
          </Link>
        </Alert>
      </FormModal>
    );
  return (
    <FormModal
      title="Terugzettest"
      onClose={onClose}
      submitLabel="Test starten"
      disabled={test.isPending || !st || !node}
      error={test.error ? (test.error instanceof Error ? test.error.message : "Mislukt") : !node ? "Geen host met een bijgewerkte agent." : null}
      onSubmit={() =>
        test.mutate({ node, archive: b.volid, type: kind as GuestType, storage: st }, { onSuccess: (j) => setDone({ id: j.id, title: j.title }) })
      }
    >
      <p className="text-sm">
        ClusterForge zet de back-up van {guestLabel(kind)} {b.vmid} ({when(b.ctime)}) terug als een nieuwe gast met een vrij ID, met de netwerkkaarten los en zonder
        doorgegeven hardware. Hij start hem, wacht tot hij draait{kind === "qemu" ? " (en tot de guest agent antwoordt, als die aan staat)" : ""} en verwijdert hem
        daarna weer. Het origineel merkt er niets van.
      </p>
      <div className="grid gap-3 sm:grid-cols-2">
        <Field label="Host" htmlFor="rt-node">
          <Select id="rt-node" value={node} onChange={(e) => setNode(e.target.value)}>
            {ready.map((n) => (
              <option key={n}>{n}</option>
            ))}
          </Select>
        </Field>
        <Field label="Storage voor de test" htmlFor="rt-st" hint="Er moet plaats zijn voor de hele gast">
          <Select id="rt-st" value={st} onChange={(e) => setStorage(e.target.value)}>
            {stores.map((s) => (
              <option key={s.storage} value={s.storage}>
                {s.storage}
                {s.avail ? ` (${formatBytes(s.avail)} vrij)` : ""}
              </option>
            ))}
          </Select>
        </Field>
      </div>
      {b.size > 0 && <p className="text-xs text-slate-500">De back-up is {formatBytes(b.size)}; teruggezet neemt de gast meestal meer plaats in.</p>}
    </FormModal>
  );
}

function NotesDialog({ b, run, pending, onClose }: { b: PveBackup; run: Runner; pending: boolean; onClose: () => void }) {
  const [notes, setNotes] = useState(b.notes ?? "");
  const [prot, setProt] = useState(!!b.protected);
  return (
    <FormModal
      title="Notitie en bescherming"
      onClose={onClose}
      submitLabel="Opslaan"
      disabled={pending}
      onSubmit={() =>
        void run({
          method: "set",
          path: `/nodes/${b.node}/storage/${b.storage}/content/${b.volid}`,
          params: { notes, protected: prot ? "1" : "0" },
        }).then((ok) => ok && onClose())
      }
    >
      <Field label="Notitie" htmlFor="bn-notes">
        <Textarea id="bn-notes" rows={3} value={notes} onChange={(e) => setNotes(e.target.value)} />
      </Field>
      <Check id="bn-prot" checked={prot} onChange={setProt}>
        Beschermd: opruimen (retentie) en verwijderen slaan deze back-up over
      </Check>
    </FormModal>
  );
}

export function BackupNowDialog({ res, vmid, run, pending, onClose }: { res: PveResource[]; vmid?: number; run: Runner; pending: boolean; onClose: () => void }) {
  const guests = res.filter((r) => (r.type === "qemu" || r.type === "lxc") && !r.template).sort((a, b) => (a.vmid ?? 0) - (b.vmid ?? 0));
  const [pick, setPick] = useState(String(vmid ?? guests[0]?.vmid ?? ""));
  const g = guests.find((x) => String(x.vmid) === pick);
  const stores = backupStores(res).filter((s) => s.shared || s.node === g?.node);
  const [storage, setStorage] = useState("");
  const st = storage || stores[0]?.storage || "";
  const [mode, setMode] = useState("snapshot");
  const [notes, setNotes] = useState("{{guestname}}");
  const [prot, setProt] = useState(false);
  const submit = () => {
    if (!g) return;
    const p: Record<string, string> = { vmid: pick, storage: st, mode, "notes-template": notes };
    if (!/pbs/.test(stores.find((s) => s.storage === st)?.type ?? "")) p.compress = "zstd";
    if (prot) p.protected = "1";
    void run({ method: "create", path: `/nodes/${g.node}/vzdump`, params: p }).then((ok) => ok && onClose());
  };
  return (
    <FormModal title="Nu een back-up maken" onClose={onClose} onSubmit={submit} submitLabel="Back-up maken" disabled={pending || !g || !st} error={!st && g ? `Geen back-upopslag die ${g.node} kan bereiken.` : null}>
      <div className="grid gap-3 sm:grid-cols-2">
        <Field label="Gast" htmlFor="bk-g">
          <Select id="bk-g" value={pick} onChange={(e) => setPick(e.target.value)} disabled={!!vmid}>
            {guests.map((x) => (
              <option key={x.vmid} value={String(x.vmid)}>
                {guestLabel(x.type)} {x.vmid} {x.name}
              </option>
            ))}
          </Select>
        </Field>
        <Field label="Naar" htmlFor="bk-st">
          <Select id="bk-st" value={st} onChange={(e) => setStorage(e.target.value)}>
            {stores.map((s) => (
              <option key={`${s.node}/${s.storage}`} value={s.storage}>
                {s.storage}
                {s.type ? ` (${s.type})` : ""}
              </option>
            ))}
          </Select>
        </Field>
        <Field label="Modus" htmlFor="bk-mode" hint="Snapshot: de gast blijft draaien">
          <Select id="bk-mode" value={mode} onChange={(e) => setMode(e.target.value)}>
            <option value="snapshot">Snapshot</option>
            <option value="suspend">Pauzeren</option>
            <option value="stop">Stoppen</option>
          </Select>
        </Field>
        <Field label="Notitie" htmlFor="bk-notes" hint="{{guestname}}, {{vmid}}, {{node}}, {{cluster}}">
          <Input id="bk-notes" value={notes} onChange={(e) => setNotes(e.target.value)} />
        </Field>
      </div>
      <Check id="bk-prot" checked={prot} onChange={setProt}>
        Beschermen tegen opruimen
      </Check>
    </FormModal>
  );
}
