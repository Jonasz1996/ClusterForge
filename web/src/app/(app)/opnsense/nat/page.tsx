"use client";

import { Suspense, useState } from "react";
import { tableClass, tdClass, thClass } from "@/components/inventory/bits";
import { ChangeDialog, ClusterStrip, JobBanner, OpnGate } from "@/components/opnsense/bits";
import { ChangedList, EditForm, toBody, type FieldSpec, type Pending } from "@/components/opnsense/edit";
import { Tile as Win, TileGrid } from "@/components/Tiles";
import { Button, Card, PageHeader } from "@/components/ui";
import { useMe } from "@/lib/auth";
import { readNode, useOpnGet, type OpnCluster, type OpnFirewall, type OpnSearch } from "@/lib/opnsense";
import type { Job } from "@/lib/proxmox";

export default function OpnNatPage() {
  return (
    <div className="space-y-6">
      <PageHeader
        title="NAT"
        description="Port forwards, uitgaande NAT, 1:1 en NPT van OPNsense, gelezen op de master. Een wijziging raakt mogelijk de toegang, dus ze gaat altijd met herstelpunt, terugdraaitimer en proeven, en je tikt de naam van het cluster over."
      />
      <Suspense>
        <OpnGate>{(c) => <Nat key={c.key} c={c} />}</OpnGate>
      </Suspense>
    </div>
  );
}

type Row = Record<string, string> & { uuid: string };
type Result = { kind: "success"; job: Job } | { kind: "error"; message: string } | null;

type Section = {
  title: string;
  ctrl: string;
  what: string;
  cols: [string, string][];
  // fields kun je wijzigen of bij toevoegen invullen; zonder: alleen aan/uit en verwijderen.
  fields?: (n: OpnFirewall, r?: Row) => FieldSpec[];
  defaults?: Record<string, string>;
  descr: string;
};

function ifaces(n: OpnFirewall, current?: string): [string, string][] {
  const opts: [string, string][] = (n.facts.interfaces ?? []).map((i) => [i.name, i.descr ? `${i.descr} (${i.name})` : i.name]);
  if (current && !opts.some(([o]) => o === current)) opts.unshift([current, current]);
  return opts;
}

const protocols: [string, string][] = [
  ["tcp", "TCP"],
  ["udp", "UDP"],
  ["tcp/udp", "TCP/UDP"],
];

const sections: Section[] = [
  {
    title: "Port forwards",
    ctrl: "d_nat",
    what: "port forward",
    descr: "descr",
    cols: [
      ["interface", "Interface"],
      ["protocol", "Protocol"],
      ["destination.network", "Doel"],
      ["destination.port", "Poort"],
      ["target", "Naar"],
      ["local-port", "Lokale poort"],
      ["descr", "Omschrijving"],
    ],
    fields: (n, r) => [
      { key: "interface", label: "Interface", kind: "select", options: ifaces(n, r?.interface) },
      { key: "protocol", label: "Protocol", kind: "select", options: protocols },
      { key: "destination.network", label: "Doel", hint: "Meestal wanip of een CARP-VIP" },
      { key: "destination.port", label: "Poort", hint: "Bijvoorbeeld 443 of 8000-8010" },
      { key: "target", label: "Naar adres", hint: "Intern adres of alias" },
      { key: "local-port", label: "Lokale poort" },
      { key: "descr", label: "Omschrijving", wide: true },
    ],
    defaults: { protocol: "tcp", "destination.network": "wanip" },
  },
  {
    title: "Uitgaande NAT",
    ctrl: "source_nat",
    what: "uitgaande NAT-regel",
    descr: "description",
    cols: [
      ["interface", "Interface"],
      ["source_net", "Bron"],
      ["destination_net", "Doel"],
      ["target", "Vertaling"],
      ["description", "Omschrijving"],
    ],
    fields: (n, r) => [
      { key: "interface", label: "Interface", kind: "select", options: ifaces(n, r?.interface) },
      { key: "source_net", label: "Bron" },
      { key: "destination_net", label: "Doel" },
      { key: "target", label: "Vertaling", hint: "Adres of CARP-VIP waarachter het verkeer naar buiten gaat" },
      { key: "description", label: "Omschrijving", wide: true },
    ],
    defaults: { source_net: "any", destination_net: "any" },
  },
  {
    title: "1:1",
    ctrl: "one_to_one",
    what: "1:1-regel",
    descr: "description",
    cols: [
      ["interface", "Interface"],
      ["external", "Extern"],
      ["source_net", "Intern"],
      ["description", "Omschrijving"],
    ],
  },
  {
    title: "NPT",
    ctrl: "npt",
    what: "NPT-regel",
    descr: "description",
    cols: [
      ["interface", "Interface"],
      ["source_net", "Intern"],
      ["destination_net", "Extern"],
      ["description", "Omschrijving"],
    ],
  },
];

function Nat({ c }: { c: OpnCluster }) {
  const n = readNode(c);
  const me = useMe();
  const admin = me.data?.user.role === "admin";
  const [result, setResult] = useState<Result>(null);
  const [pending, setPending] = useState<Pending | null>(null);
  return (
    <div className="space-y-6">
      <ClusterStrip c={c} />
      <JobBanner result={result} />
      {n && (
        <TileGrid>
          {sections.map((s) => (
            <Win key={s.ctrl} wide={s.ctrl === "d_nat"}>
              <SectionCard n={n} s={s} admin={admin} onChange={setPending} />
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

function SectionCard({ n, s, admin, onChange }: { n: OpnFirewall; s: Section; admin: boolean; onChange: (p: Pending) => void }) {
  const q = useOpnGet<OpnSearch<Row>>(n.node_id, `firewall/${s.ctrl}/searchRule`);
  const rows = q.data?.rows ?? [];
  const [edit, setEdit] = useState<Row | "new" | null>(null);
  const on = (r: Row) => r.enabled !== "0" && r.disabled !== "1";
  const name = (r: Row) => r[s.descr] || s.what;
  const values = (r?: Row): Record<string, string> => {
    if (!r) return { ...(s.defaults ?? {}) };
    const out: Record<string, string> = {};
    for (const f of s.fields?.(n, r) ?? []) out[f.key] = r[f.key] ?? "";
    return out;
  };
  const say = (text: string) => <p className="mt-2 text-slate-600 dark:text-slate-300">{text}</p>;
  return (
    <Card title={`${s.title}${q.data ? ` · ${rows.length}` : ""}`}>
      {q.isError ? (
        <p className="text-sm text-slate-500">Niet beschikbaar via de API op deze OPNsense-versie ({(q.error as Error).message}).</p>
      ) : (
        <>
          {admin && s.fields && (
            <div className="mb-3">
              <Button variant="secondary" onClick={() => setEdit("new")}>
                {s.ctrl === "d_nat" ? "Port forward toevoegen" : "Regel toevoegen"}
              </Button>
            </div>
          )}
          {rows.length === 0 ? (
            <p className="text-sm text-slate-500">Geen regels.</p>
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
                    {admin && <th className={thClass} />}
                  </tr>
                </thead>
                <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
                  {rows.map((r) => (
                    <tr key={r.uuid} className={on(r) ? "" : "opacity-50"}>
                      {s.cols.map(([k]) => (
                        <td key={k} className={`${tdClass} ${k === s.descr ? "" : "font-mono text-xs"}`}>
                          {r[k] || "–"}
                        </td>
                      ))}
                      {admin && (
                        <td className={`${tdClass} text-xs whitespace-nowrap`}>
                          <span className="flex gap-2 text-slate-500">
                            {s.fields && (
                              <button type="button" className="underline" onClick={() => setEdit(r)}>
                                wijzigen
                              </button>
                            )}
                            <button
                              type="button"
                              className="underline"
                              onClick={() =>
                                onChange({
                                  calls: [{ path: `firewall/${s.ctrl}/toggleRule/${r.uuid}/${on(r) ? 0 : 1}` }],
                                  before: say(`${name(r)} ${on(r) ? "uitzetten" : "aanzetten"}.`),
                                })
                              }
                            >
                              {on(r) ? "uitzetten" : "aanzetten"}
                            </button>
                            <button
                              type="button"
                              className="text-red-700 underline dark:text-red-400"
                              onClick={() =>
                                onChange({
                                  calls: [{ path: `firewall/${s.ctrl}/delRule/${r.uuid}` }],
                                  before: say(`${name(r)} verwijderen.`),
                                })
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
                  ? { path: `firewall/${s.ctrl}/addRule`, body: toBody("rule", changed, values()) }
                  : { path: `firewall/${s.ctrl}/setRule/${edit.uuid}`, body: toBody("rule", changed) },
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
