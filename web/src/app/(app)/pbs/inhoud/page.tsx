"use client";

import { useRouter, useSearchParams } from "next/navigation";
import { Suspense, useMemo, useState } from "react";
import { askConfirm } from "@/components/Confirm";
import { Empty, formatBytes, QueryState, tableClass, tdClass, thClass } from "@/components/inventory/bits";
import { Modal } from "@/components/Modal";
import { CallForm, keepFields, PbsGate, timeFmt, usePbsRunner } from "@/components/pbs/bits";
import { ActionMenu, TypedConfirm } from "@/components/pve/bits";
import { Alert, Badge, Button, Card, cx, Field, Input, PageHeader, Select } from "@/components/ui";
import { useIsAdmin } from "@/lib/inventory";
import { usePbsGet, type PbsServer } from "@/lib/pbs";

export default function PbsContentPage() {
  return (
    <div className="space-y-6">
      <PageHeader
        title="Inhoud"
        description="Alle back-ups per datastore en namespace: per VM, CT of host de snapshots met grootte, verificatie en bescherming. Verifieer, bescherm, zet een notitie, prune of verwijder."
      />
      <Suspense>
        <PbsGate>{(s) => <ContentView key={s.node_id} srv={s} />}</PbsGate>
      </Suspense>
    </div>
  );
}

type Group = {
  "backup-type": string;
  "backup-id": string;
  "last-backup": number;
  "backup-count": number;
  owner?: string;
  comment?: string;
  files?: string[];
};

type Snapshot = {
  "backup-type": string;
  "backup-id": string;
  "backup-time": number;
  comment?: string;
  size?: number;
  owner?: string;
  protected?: boolean;
  verification?: { state: string; upid: string };
  files?: { filename: string; size?: number; "crypt-mode"?: string }[];
  fingerprint?: string;
};

const typeLabel: Record<string, string> = { vm: "VM", ct: "CT", host: "Host" };

function snapParams(s: { "backup-type": string; "backup-id": string; "backup-time"?: number }, ns: string) {
  const p: Record<string, string> = { "backup-type": s["backup-type"], "backup-id": s["backup-id"] };
  if (s["backup-time"] !== undefined) p["backup-time"] = String(s["backup-time"]);
  if (ns) p.ns = ns;
  return p;
}

function ContentView({ srv }: { srv: PbsServer }) {
  const id = srv.node_id;
  const params = useSearchParams();
  const router = useRouter();
  const isAdmin = useIsAdmin();
  const store = params.get("store") && srv.datastores.includes(params.get("store")!) ? params.get("store")! : (srv.datastores[0] ?? "");
  const ns = params.get("ns") ?? "";
  const setParam = (k: string, v: string) => {
    const q = new URLSearchParams(params.toString());
    if (v) q.set(k, v);
    else q.delete(k);
    if (k === "store") q.delete("ns");
    router.replace("?" + q.toString());
  };
  const nss = usePbsGet<{ ns: string; comment?: string }[]>(id, `/admin/datastore/${store}/namespace`, { params: { "max-depth": "7" }, enabled: !!store, refetch: 60_000 });
  const groups = usePbsGet<Group[]>(id, `/admin/datastore/${store}/groups`, { params: ns ? { ns } : undefined, enabled: !!store, refetch: 30_000 });
  const [search, setSearch] = useState("");
  const [open, setOpen] = useState<Group | null>(null);
  const [nsDialog, setNsDialog] = useState<"create" | "delete" | null>(null);
  const { run, pending, banner } = usePbsRunner(id);
  const [now] = useState(() => Date.now() / 1000);
  const list = useMemo(() => {
    const f = search.trim().toLowerCase();
    return (groups.data ?? [])
      .filter((g) => !f || g["backup-id"].toLowerCase().includes(f) || (g.comment ?? "").toLowerCase().includes(f))
      .sort((a, b) => a["backup-type"].localeCompare(b["backup-type"]) || a["backup-id"].localeCompare(b["backup-id"], undefined, { numeric: true }));
  }, [groups.data, search]);
  if (!store) return <Alert kind="info">Deze PBS heeft nog geen datastore.</Alert>;
  return (
    <Card>
      <div className="space-y-4">
        {banner}
        <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
          <Field label="Datastore" htmlFor="c-store">
            <Select id="c-store" value={store} onChange={(e) => setParam("store", e.target.value)}>
              {srv.datastores.map((d) => (
                <option key={d}>{d}</option>
              ))}
            </Select>
          </Field>
          <Field label="Namespace" htmlFor="c-ns">
            <Select id="c-ns" value={ns} onChange={(e) => setParam("ns", e.target.value)}>
              {(nss.data ?? [{ ns: "" }]).map((n) => (
                <option key={n.ns} value={n.ns}>
                  {n.ns || "(hoofd)"}
                </option>
              ))}
            </Select>
          </Field>
          <Field label="Zoeken" htmlFor="c-search">
            <Input id="c-search" value={search} onChange={(e) => setSearch(e.target.value)} placeholder="VMID, naam, notitie" />
          </Field>
          {isAdmin && (
            <div className="flex items-end gap-2">
              <Button variant="secondary" onClick={() => setNsDialog("create")}>
                Namespace…
              </Button>
              {ns && (
                <Button variant="secondary-danger" onClick={() => setNsDialog("delete")}>
                  Weg
                </Button>
              )}
            </div>
          )}
        </div>
        <QueryState q={groups}>
          {list.length === 0 ? (
            <Empty>Geen back-ups in {ns ? `namespace ${ns}` : "deze datastore"}.</Empty>
          ) : (
            <div className="relative -mx-4 overflow-x-auto sm:mx-0">
              <table className={tableClass}>
                <thead>
                  <tr>
                    <th className={thClass}>Groep</th>
                    <th className={thClass}>Laatste back-up</th>
                    <th className={cx(thClass, "hidden sm:table-cell")}>Aantal</th>
                    <th className={cx(thClass, "hidden md:table-cell")}>Eigenaar</th>
                    {isAdmin && <th className={thClass} />}
                  </tr>
                </thead>
                <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
                  {list.map((g) => {
                    const age = (now - g["last-backup"]) / 3600;
                    return (
                      <tr key={g["backup-type"] + g["backup-id"]} className="cursor-pointer hover:bg-slate-50 dark:hover:bg-slate-900" onClick={() => setOpen(g)}>
                        <td className={tdClass}>
                          <span className="font-medium">
                            {typeLabel[g["backup-type"]] ?? g["backup-type"]} {g["backup-id"]}
                          </span>
                          {g.comment && <span className="block text-xs text-slate-500">{g.comment}</span>}
                        </td>
                        <td className={cx(tdClass, "whitespace-nowrap")}>
                          {timeFmt.format(new Date(g["last-backup"] * 1000))}
                          {age > 48 && (
                            <span className="ml-2">
                              <Badge tone={age > 24 * 7 ? "red" : "amber"}>{Math.round(age / 24)} d oud</Badge>
                            </span>
                          )}
                        </td>
                        <td className={cx(tdClass, "hidden tabular-nums sm:table-cell")}>{g["backup-count"]}</td>
                        <td className={cx(tdClass, "hidden text-slate-500 md:table-cell")}>{g.owner}</td>
                        {isAdmin && (
                          <td className={cx(tdClass, "text-right")} onClick={(e) => e.stopPropagation()}>
                            <ActionMenu
                              disabled={pending}
                              items={[
                                { label: "Snapshots…", onClick: () => setOpen(g) },
                                { label: "Groep verifiëren", onClick: () => void run({ method: "create", path: `/admin/datastore/${store}/verify`, params: snapParams(g, ns) }) },
                              ]}
                            />
                          </td>
                        )}
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </div>
          )}
        </QueryState>
      </div>
      {open && <GroupDialog id={id} store={store} ns={ns} group={open} onClose={() => setOpen(null)} />}
      {nsDialog === "create" && (
        <CallForm
          id={id}
          title={`Namespace in ${store}`}
          intro="Een namespace scheidt back-ups binnen één datastore, bijvoorbeeld per Proxmox-cluster of per omgeving. Ontdubbeling werkt over namespaces heen."
          fields={[
            { key: "name", label: "Naam", required: true, placeholder: "prod" },
            { key: "parent", label: "Onder", type: "select", options: (nss.data ?? [{ ns: "" }]).map((n) => ({ value: n.ns, label: n.ns || "(hoofd)" })) },
          ]}
          initial={{ parent: ns }}
          method="create"
          path={`/admin/datastore/${store}/namespace`}
          submitLabel="Aanmaken"
          onClose={() => setNsDialog(null)}
        />
      )}
      {nsDialog === "delete" && (
        <TypedConfirm
          title={`Namespace ${ns} verwijderen`}
          expect={ns}
          confirmLabel="Verwijderen"
          pending={pending}
          onClose={() => setNsDialog(null)}
          onConfirm={(typed) => {
            void run({ method: "delete", path: `/admin/datastore/${store}/namespace`, params: { ns, "delete-groups": "1" }, confirm: typed });
            setNsDialog(null);
            setParam("ns", "");
          }}
        >
          <p>De namespace en alle back-ups erin (ook in onderliggende namespaces) gaan weg. Beschermde snapshots blijven staan.</p>
        </TypedConfirm>
      )}
    </Card>
  );
}

function verifyBadge(s: Snapshot) {
  const v = s.verification?.state;
  if (v === "ok") return <Badge tone="green">geverifieerd</Badge>;
  if (v === "failed") return <Badge tone="red">kapot</Badge>;
  return <Badge>niet geverifieerd</Badge>;
}

function GroupDialog({ id, store, ns, group, onClose }: { id: string; store: string; ns: string; group: Group; onClose: () => void }) {
  const isAdmin = useIsAdmin();
  const snaps = usePbsGet<Snapshot[]>(id, `/admin/datastore/${store}/snapshots`, { params: snapParams(group, ns), refetch: 20_000 });
  const { run, pending, banner } = usePbsRunner(id);
  const [notes, setNotes] = useState<Snapshot | null>(null);
  const [prune, setPrune] = useState(false);
  const [del, setDel] = useState(false);
  const name = `${typeLabel[group["backup-type"]] ?? group["backup-type"]} ${group["backup-id"]}`;
  const list = (snaps.data ?? []).slice().sort((a, b) => b["backup-time"] - a["backup-time"]);
  return (
    <Modal title={`${name} in ${store}${ns ? " / " + ns : ""}`} onClose={onClose} wide>
      <div className="space-y-3">
        {banner}
        {isAdmin && (
          <div className="flex flex-wrap gap-2">
            <Button variant="secondary" disabled={pending} onClick={() => void run({ method: "create", path: `/admin/datastore/${store}/verify`, params: snapParams(group, ns) })}>
              Alles verifiëren
            </Button>
            <Button variant="secondary" onClick={() => setPrune(true)}>
              Prune…
            </Button>
            <Button variant="secondary-danger" onClick={() => setDel(true)}>
              Groep verwijderen…
            </Button>
          </div>
        )}
        <QueryState q={snaps}>
          {list.length === 0 ? (
            <Empty>Geen snapshots.</Empty>
          ) : (
            <div className="relative -mx-4 max-h-[60vh] overflow-auto sm:mx-0">
              <table className={tableClass}>
                <thead>
                  <tr>
                    <th className={thClass}>Moment</th>
                    <th className={thClass}>Grootte</th>
                    <th className={thClass}>Staat</th>
                    <th className={cx(thClass, "hidden md:table-cell")}>Notitie</th>
                    {isAdmin && <th className={thClass} />}
                  </tr>
                </thead>
                <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
                  {list.map((s) => {
                    const enc = (s.files ?? []).some((f) => f["crypt-mode"] === "encrypt");
                    return (
                      <tr key={s["backup-time"]} className={cx(s.verification?.state === "failed" && "bg-red-50/40 dark:bg-red-950/20")}>
                        <td className={cx(tdClass, "whitespace-nowrap tabular-nums")}>{timeFmt.format(new Date(s["backup-time"] * 1000))}</td>
                        <td className={cx(tdClass, "whitespace-nowrap tabular-nums")}>{s.size ? formatBytes(s.size) : "–"}</td>
                        <td className={cx(tdClass, "space-x-1 whitespace-nowrap")}>
                          {verifyBadge(s)}
                          {s.protected && <Badge tone="blue">beschermd</Badge>}
                          {enc && <Badge tone="slate">versleuteld</Badge>}
                        </td>
                        <td className={cx(tdClass, "hidden max-w-60 truncate text-slate-500 md:table-cell")} title={s.comment}>
                          {s.comment}
                        </td>
                        {isAdmin && (
                          <td className={cx(tdClass, "text-right")}>
                            <ActionMenu
                              disabled={pending}
                              items={[
                                { label: "Verifiëren", onClick: () => void run({ method: "create", path: `/admin/datastore/${store}/verify`, params: snapParams(s, ns) }) },
                                {
                                  label: s.protected ? "Bescherming weg" : "Beschermen",
                                  onClick: () =>
                                    void run({ method: "set", path: `/admin/datastore/${store}/protected`, params: { ...snapParams(s, ns), protected: s.protected ? "0" : "1" } }),
                                },
                                { label: "Notitie…", onClick: () => setNotes(s) },
                                {
                                  label: "Verwijderen",
                                  danger: true,
                                  onClick: async () => {
                                    if (s.protected) return;
                                    if (await askConfirm(`Snapshot van ${timeFmt.format(new Date(s["backup-time"] * 1000))} verwijderen? De ruimte komt vrij na de volgende garbage collection.`, { danger: true, confirmLabel: "Verwijderen" }))
                                      void run({ method: "delete", path: `/admin/datastore/${store}/snapshots`, params: snapParams(s, ns) });
                                  },
                                },
                              ]}
                            />
                          </td>
                        )}
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </div>
          )}
        </QueryState>
        <div className="flex justify-end">
          <Button variant="secondary" onClick={onClose}>
            Sluiten
          </Button>
        </div>
      </div>
      {notes && (
        <CallForm
          id={id}
          title="Notitie bij snapshot"
          fields={[{ key: "notes", label: "Notitie", type: "textarea" }]}
          initial={{ notes: notes.comment ?? "" }}
          method="set"
          path={`/admin/datastore/${store}/notes`}
          fixed={snapParams(notes, ns)}
          submitLabel="Opslaan"
          onClose={() => setNotes(null)}
        />
      )}
      {prune && (
        <CallForm
          id={id}
          title={`Prune ${name}`}
          intro="Houdt de snapshots die aan een bewaarregel voldoen en verwijdert de rest. Op proef toont het taaklog wat weg zou gaan."
          fields={[...keepFields, { key: "dry-run", label: "Op proef (niets verwijderen)", type: "checkbox" }]}
          initial={{ "keep-last": "3", "keep-daily": "7", "keep-weekly": "4", "dry-run": "1" }}
          method="create"
          path={`/admin/datastore/${store}/prune`}
          fixed={snapParams(group, ns)}
          submitLabel="Prune"
          onClose={() => setPrune(false)}
        />
      )}
      {del && (
        <TypedConfirm
          title={`${name} verwijderen`}
          expect={group["backup-id"]}
          confirmLabel="Verwijderen"
          pending={pending}
          onClose={() => setDel(false)}
          onConfirm={(typed) => {
            void run({ method: "delete", path: `/admin/datastore/${store}/groups`, params: snapParams(group, ns), confirm: typed });
            setDel(false);
          }}
        >
          <p>Alle {group["backup-count"]} snapshots van {name} gaan weg, behalve beschermde.</p>
        </TypedConfirm>
      )}
    </Modal>
  );
}
