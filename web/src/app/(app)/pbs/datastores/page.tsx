"use client";

import Link from "next/link";
import { Suspense, useState } from "react";
import { askConfirm } from "@/components/Confirm";
import { Empty, formatBytes, QueryState, tableClass, tdClass, thClass } from "@/components/inventory/bits";
import { CallForm, keepFields, PbsGate, scheduleHint, usePbsRunner, type FormField } from "@/components/pbs/bits";
import { ActionMenu, Meter, TypedConfirm } from "@/components/pve/bits";
import { Badge, Button, Card, cx, PageHeader } from "@/components/ui";
import { useIsAdmin } from "@/lib/inventory";
import { pct, usePbsGet, usePbsOverview, type PbsServer } from "@/lib/pbs";

export default function PbsDatastoresPage() {
  return (
    <div className="space-y-6">
      <PageHeader
        title="Datastores"
        description="De datastores van de Backup Server: gebruik, schema voor garbage collection, onderhoudsmodus. Start GC, verificatie of prune met één klik, maak een nieuwe datastore of verwijder er een."
      />
      <Suspense>
        <PbsGate>{(s) => <DatastoresView key={s.node_id} srv={s} />}</PbsGate>
      </Suspense>
    </div>
  );
}

export type DatastoreConfig = {
  name: string;
  path: string;
  comment?: string;
  "gc-schedule"?: string;
  "verify-new"?: boolean;
  "maintenance-mode"?: string;
  notify?: string;
  "notification-mode"?: string;
  "backing-device"?: string;
  tuning?: string;
};

const createFields: FormField[] = [
  { key: "name", label: "Naam", required: true, createOnly: true, placeholder: "store1" },
  { key: "path", label: "Pad", required: true, createOnly: true, placeholder: "/mnt/datastore/store1", hint: "Een lege map op een schijf van de PBS. PBS maakt de chunkstore aan." },
  { key: "comment", label: "Omschrijving" },
  { key: "gc-schedule", label: "Schema garbage collection", placeholder: "daily", hint: scheduleHint },
  { key: "verify-new", label: "Nieuwe back-ups meteen verifiëren", type: "checkbox", hint: "Kost lezen na elke back-up, maar een kapotte back-up valt meteen op." },
];

const editFields: FormField[] = [
  ...createFields,
  {
    key: "maintenance-mode",
    label: "Onderhoudsmodus",
    type: "select",
    options: [
      { value: "", label: "Uit" },
      { value: "type=read-only", label: "Alleen lezen (terugzetten kan, back-ups niet)" },
      { value: "type=offline", label: "Offline (niets)" },
    ],
  },
];

type Dialog =
  | { kind: "create" }
  | { kind: "edit"; cfg: DatastoreConfig }
  | { kind: "prune"; store: string }
  | { kind: "delete"; store: string }
  | null;

function DatastoresView({ srv }: { srv: PbsServer }) {
  const id = srv.node_id;
  const isAdmin = useIsAdmin();
  const o = usePbsOverview(id);
  const cfgs = usePbsGet<DatastoreConfig[]>(id, "/config/datastore");
  const { run, pending, banner } = usePbsRunner(id);
  const [dialog, setDialog] = useState<Dialog>(null);
  const [destroy, setDestroy] = useState(false);
  const usage = new Map((o.data?.datastores ?? []).map((d) => [d.store, d]));
  const gcJob = new Map((o.data?.jobs ?? []).filter((j) => j.kind === "gc").map((j) => [j.store, j]));
  return (
    <Card title="Datastores">
      <div className="space-y-4">
        {banner}
        {isAdmin && (
          <div className="flex justify-end">
            <Button onClick={() => setDialog({ kind: "create" })}>Nieuwe datastore</Button>
          </div>
        )}
        <QueryState q={cfgs}>
          {(cfgs.data ?? []).length === 0 ? (
            <Empty>
              Nog geen datastore. Maak er een op een lege map, of maak eerst een ZFS-pool of map op een schijf onder{" "}
              <Link href={`/pbs/systeem?pbs=${id}`} className="underline">
                Systeem → Schijven
              </Link>
              .
            </Empty>
          ) : (
            <div className="relative -mx-4 overflow-x-auto sm:mx-0">
              <table className={tableClass}>
                <thead>
                  <tr>
                    <th className={thClass}>Naam</th>
                    <th className={thClass}>Gebruik</th>
                    <th className={cx(thClass, "hidden md:table-cell")}>Pad</th>
                    <th className={cx(thClass, "hidden sm:table-cell")}>GC</th>
                    <th className={cx(thClass, "hidden lg:table-cell")}>Laatste GC</th>
                    {isAdmin && <th className={thClass} />}
                  </tr>
                </thead>
                <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
                  {(cfgs.data ?? [])
                    .slice()
                    .sort((a, b) => a.name.localeCompare(b.name))
                    .map((c) => {
                      const u = usage.get(c.name);
                      const gc = gcJob.get(c.name);
                      return (
                        <tr key={c.name}>
                          <td className={tdClass}>
                            <Link href={`/pbs/inhoud?pbs=${id}&store=${encodeURIComponent(c.name)}`} className="font-medium hover:underline">
                              {c.name}
                            </Link>
                            {c.comment && <span className="block text-xs text-slate-500">{c.comment}</span>}
                            {c["maintenance-mode"] && (
                              <span className="mt-1 block">
                                <Badge tone="amber">onderhoud: {c["maintenance-mode"].replace("type=", "")}</Badge>
                              </span>
                            )}
                            {c["verify-new"] && (
                              <span className="mt-1 mr-1 inline-block">
                                <Badge tone="blue">verifieert nieuwe</Badge>
                              </span>
                            )}
                          </td>
                          <td className={cx(tdClass, "min-w-44")}>
                            {u ? (
                              u.error ? (
                                <span className="text-red-600 dark:text-red-400">{u.error}</span>
                              ) : (
                                <>
                                  <Meter value={pct(u.used, u.total)} label={`Gebruik ${c.name}`} />
                                  <span className="text-xs text-slate-500">
                                    {formatBytes(u.used)} van {formatBytes(u.total)}
                                  </span>
                                </>
                              )
                            ) : (
                              "–"
                            )}
                          </td>
                          <td className={cx(tdClass, "hidden font-mono text-xs md:table-cell")}>{c.path}</td>
                          <td className={cx(tdClass, "hidden font-mono text-xs sm:table-cell")}>{c["gc-schedule"] || <span className="text-amber-600">geen</span>}</td>
                          <td className={cx(tdClass, "hidden whitespace-nowrap lg:table-cell")}>
                            {gc?.["last-run-state"] ? (
                              <Badge tone={gc["last-run-state"] === "OK" ? "green" : "red"}>{gc["last-run-state"].slice(0, 30)}</Badge>
                            ) : (
                              "–"
                            )}
                          </td>
                          {isAdmin && (
                            <td className={cx(tdClass, "text-right")}>
                              <ActionMenu
                                disabled={pending}
                                items={[
                                  { label: "Garbage collection nu", onClick: () => void run({ method: "create", path: `/admin/datastore/${c.name}/gc` }) },
                                  {
                                    label: "Alles verifiëren",
                                    onClick: async () => {
                                      if (await askConfirm(`Alle back-ups in ${c.name} verifiëren? Al geverifieerde slaat PBS over.`, { confirmLabel: "Verifiëren" }))
                                        void run({ method: "create", path: `/admin/datastore/${c.name}/verify`, params: { "ignore-verified": "1" } });
                                    },
                                  },
                                  { label: "Prune…", onClick: () => setDialog({ kind: "prune", store: c.name }) },
                                  { label: "Wijzigen…", onClick: () => setDialog({ kind: "edit", cfg: c }) },
                                  {
                                    label: c["maintenance-mode"] ? "Onderhoud uit" : "Alleen lezen (onderhoud)",
                                    onClick: () =>
                                      void run(
                                        c["maintenance-mode"]
                                          ? { method: "set", path: `/config/datastore/${c.name}`, params: { delete: "maintenance-mode" } }
                                          : { method: "set", path: `/config/datastore/${c.name}`, params: { "maintenance-mode": "type=read-only" } },
                                      ),
                                  },
                                  { label: "Verwijderen…", danger: true, onClick: () => setDialog({ kind: "delete", store: c.name }) },
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
      {dialog?.kind === "create" && (
        <CallForm id={id} title="Nieuwe datastore" fields={createFields} initial={{ "gc-schedule": "daily" }} method="create" path="/config/datastore" submitLabel="Aanmaken" onClose={() => setDialog(null)} />
      )}
      {dialog?.kind === "edit" && (
        <CallForm
          id={id}
          title={`Datastore ${dialog.cfg.name} wijzigen`}
          fields={editFields}
          initial={{
            comment: dialog.cfg.comment ?? "",
            "gc-schedule": dialog.cfg["gc-schedule"] ?? "",
            "verify-new": dialog.cfg["verify-new"] ? "1" : "0",
            "maintenance-mode": dialog.cfg["maintenance-mode"] ?? "",
          }}
          method="set"
          path={`/config/datastore/${dialog.cfg.name}`}
          submitLabel="Opslaan"
          onClose={() => setDialog(null)}
        />
      )}
      {dialog?.kind === "prune" && (
        <CallForm
          id={id}
          title={`Prune in ${dialog.store}`}
          intro="Prune haalt oude snapshots weg volgens de bewaarregels, in de hele datastore. Laat 'Op proef' aan om eerst in het taaklog te zien wat weg zou gaan. De ruimte komt pas vrij na de volgende garbage collection."
          fields={[...keepFields, { key: "dry-run", label: "Op proef (niets verwijderen)", type: "checkbox" }]}
          initial={{ "keep-last": "3", "keep-daily": "7", "keep-weekly": "4", "keep-monthly": "6", "dry-run": "1" }}
          method="create"
          path={`/admin/datastore/${dialog.store}/prune-datastore`}
          submitLabel="Prune starten"
          onClose={() => setDialog(null)}
        />
      )}
      {dialog?.kind === "delete" && (
        <TypedConfirm
          title={`Datastore ${dialog.store} verwijderen`}
          expect={dialog.store}
          confirmLabel="Verwijderen"
          pending={pending}
          onClose={() => setDialog(null)}
          onConfirm={(typed) => {
            void run({ method: "delete", path: `/config/datastore/${dialog.store}`, params: destroy ? { "destroy-data": "1" } : {}, confirm: typed });
            setDialog(null);
          }}
        >
          <p>De datastore verdwijnt uit PBS. Jobs die hem gebruiken worden ook verwijderd.</p>
          <label className="flex items-start gap-2">
            <input type="checkbox" className="mt-0.5" checked={destroy} onChange={(e) => setDestroy(e.target.checked)} />
            <span className={cx(destroy && "font-medium text-red-700 dark:text-red-300")}>Ook alle back-ups op de schijf wissen (kan niet terug)</span>
          </label>
        </TypedConfirm>
      )}
    </Card>
  );
}
