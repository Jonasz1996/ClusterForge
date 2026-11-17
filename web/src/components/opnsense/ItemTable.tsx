"use client";

import { useState } from "react";
import { tableClass, tdClass, thClass } from "@/components/inventory/bits";
import { ChangedList, EditForm, toBody, type FieldSpec, type Pending } from "@/components/opnsense/edit";
import { ChangeDialog, ClusterStrip, JobBanner } from "@/components/opnsense/bits";
import { Tile as Win, TileGrid } from "@/components/Tiles";
import { Button, Card, Input } from "@/components/ui";
import { useMe } from "@/lib/auth";
import { readNode, useOpnGet, type OpnCluster, type OpnFirewall, type OpnSearch } from "@/lib/opnsense";
import type { Job } from "@/lib/proxmox";

// ItemTable toont een lijst uit een MVC-model van OPNsense (VIP's, routes,
// DNS-overrides, reservaties …) met toevoegen, wijzigen, aan/uit en
// verwijderen. Elke wijziging gaat als Pending naar de ChangeDialog.

export type Row = Record<string, string> & { uuid: string };

export type ItemSpec = {
  title: string;
  // search is het zoekpad, zoals unbound/settings/searchHostOverride.
  search: string;
  // base en x maken add<x>, set<x>/<uuid>, del<x>/<uuid> en toggle<x>/<uuid>.
  base: string;
  x: string;
  root: string;
  what: string;
  cols: [string, string][];
  fields?: (n: OpnFirewall, r?: Row) => FieldSpec[];
  defaults?: Record<string, string>;
  // name is het veld dat het item een naam geeft in de dialoog.
  name: string;
  toggle?: boolean;
  // readonly: alleen lezen (leases).
  readonly?: boolean;
  hint?: string;
};

export function ItemTable({ n, s, admin, onChange }: { n: OpnFirewall; s: ItemSpec; admin: boolean; onChange: (p: Pending) => void }) {
  const q = useOpnGet<OpnSearch<Row>>(n.node_id, s.search);
  const [filter, setFilter] = useState("");
  const [edit, setEdit] = useState<Row | "new" | null>(null);
  const all = q.data?.rows ?? [];
  const f = filter.toLowerCase();
  const rows = all.filter((r) => !f || JSON.stringify(r).toLowerCase().includes(f));
  const on = (r: Row) => r.enabled !== "0" && r.disabled !== "1";
  const name = (r: Row) => `${s.what} ${r[s.name] || ""}`.trim();
  const values = (r?: Row): Record<string, string> => {
    if (!r) return { ...(s.defaults ?? {}) };
    const out: Record<string, string> = {};
    for (const fs of s.fields?.(n, r) ?? []) out[fs.key] = r[fs.key] ?? "";
    return out;
  };
  const say = (text: string) => <p className="mt-2 text-slate-600 dark:text-slate-300">{text}</p>;
  const canEdit = admin && !s.readonly && !!s.fields;
  return (
    <Card title={`${s.title}${q.data ? ` · ${all.length}` : ""}`}>
      {q.isError ? (
        <p className="text-sm text-slate-500">Niet beschikbaar op deze firewall ({(q.error as Error).message}).</p>
      ) : (
        <>
          {s.hint && <p className="mb-2 text-xs text-slate-500">{s.hint}</p>}
          {(all.length > 8 || canEdit) && (
            <div className="mb-3 flex flex-wrap items-center gap-2">
              {all.length > 8 && (
                <Input placeholder="Zoeken" value={filter} onChange={(e) => setFilter(e.target.value)} className="max-w-xs" />
              )}
              {canEdit && (
                <Button variant="secondary" onClick={() => setEdit("new")}>
                  Toevoegen
                </Button>
              )}
            </div>
          )}
          {rows.length === 0 ? (
            <p className="text-sm text-slate-500">Leeg.</p>
          ) : (
            <div className="overflow-x-auto">
              <table className={tableClass}>
                <thead>
                  <tr>
                    {s.cols.map(([, label]) => (
                      <th key={label} className={thClass}>
                        {label}
                      </th>
                    ))}
                    {admin && !s.readonly && <th className={thClass} />}
                  </tr>
                </thead>
                <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
                  {rows.map((r, i) => (
                    <tr key={r.uuid ?? i} className={on(r) ? "" : "opacity-50"}>
                      {s.cols.map(([k]) => (
                        <td key={k} className={tdClass}>
                          {r[k] || "–"}
                        </td>
                      ))}
                      {admin && !s.readonly && (
                        <td className={`${tdClass} text-xs whitespace-nowrap`}>
                          <span className="flex gap-2 text-slate-500">
                            {s.fields && (
                              <button type="button" className="underline" onClick={() => setEdit(r)}>
                                wijzigen
                              </button>
                            )}
                            {s.toggle !== false && (
                              <button
                                type="button"
                                className="underline"
                                onClick={() =>
                                  onChange({
                                    calls: [{ path: `${s.base}/toggle${s.x}/${r.uuid}/${on(r) ? 0 : 1}` }],
                                    before: say(`${name(r)} ${on(r) ? "uitzetten" : "aanzetten"}.`),
                                  })
                                }
                              >
                                {on(r) ? "uitzetten" : "aanzetten"}
                              </button>
                            )}
                            <button
                              type="button"
                              className="text-red-700 underline dark:text-red-400"
                              onClick={() =>
                                onChange({ calls: [{ path: `${s.base}/del${s.x}/${r.uuid}` }], before: say(`${name(r)} verwijderen.`) })
                              }
                            >
                              verwijderen
                            </button>
                          </span>
                        </td>
                      )}
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </>
      )}
      {edit && s.fields && (
        <EditForm
          title={edit === "new" ? `${s.what} toevoegen` : `${name(edit)} wijzigen`}
          specs={s.fields(n, edit === "new" ? undefined : edit)}
          initial={values(edit === "new" ? undefined : edit)}
          isNew={edit === "new"}
          onClose={() => setEdit(null)}
          onNext={(changed) => {
            const isNew = edit === "new";
            onChange({
              calls: [
                isNew
                  ? { path: `${s.base}/add${s.x}`, body: toBody(s.root, changed, values()) }
                  : { path: `${s.base}/set${s.x}/${edit.uuid}`, body: toBody(s.root, changed, { [s.name]: edit[s.name] ?? "" }) },
              ],
              before: (
                <div className="mt-2">
                  <ChangedList changed={changed} />
                </div>
              ),
            });
            setEdit(null);
          }}
        />
      )}
    </Card>
  );
}

export function ifaceOptions(n: OpnFirewall, current?: string): [string, string][] {
  const opts: [string, string][] = (n.facts.interfaces ?? []).map((i) => [i.name, i.descr ? `${i.descr} (${i.name})` : i.name]);
  if (current && !opts.some(([o]) => o === current)) opts.unshift([current, current]);
  return opts;
}

// ItemTiles toont een reeks ItemTables voor een cluster, met de melding van
// de taak en de wijzigingsdialoog.
export function ItemTiles({ c, specs }: { c: OpnCluster; specs: ItemSpec[] }) {
  const n = readNode(c);
  const me = useMe();
  const admin = me.data?.user.role === "admin";
  const [result, setResult] = useState<{ kind: "success"; job: Job } | { kind: "error"; message: string } | null>(null);
  const [pending, setPending] = useState<Pending | null>(null);
  return (
    <div className="space-y-6">
      <ClusterStrip c={c} />
      <JobBanner result={result} />
      {n && (
        <TileGrid>
          {specs.map((s) => (
            <Win key={s.search} wide={s.cols.length > 4}>
              <ItemTable n={n} s={s} admin={admin} onChange={setPending} />
            </Win>
          ))}
        </TileGrid>
      )}
      {n && pending && (
        <ChangeDialog
          nodeId={n.node_id}
          calls={pending.calls}
          before={pending.before}
          onClose={() => setPending(null)}
          onDone={(job) => {
            setResult({ kind: "success", job });
            setPending(null);
          }}
        />
      )}
    </div>
  );
}
