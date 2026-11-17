"use client";

import { Suspense, useMemo, useState } from "react";
import { tableClass, tdClass, thClass } from "@/components/inventory/bits";
import { ChangeDialog, ClusterStrip, JobBanner, OpnGate } from "@/components/opnsense/bits";
import { ChangedList, EditForm, toBody, type Changed, type FieldSpec, type Pending } from "@/components/opnsense/edit";
import { Badge, Button, Card, Input, PageHeader } from "@/components/ui";
import { useMe } from "@/lib/auth";
import { readNode, useOpnGet, type OpnCluster, type OpnFirewall, type OpnSearch } from "@/lib/opnsense";
import type { Job } from "@/lib/proxmox";

export default function OpnRulesPage() {
  return (
    <div className="space-y-6">
      <PageHeader
        title="Regels en aliassen"
        description="De firewallregels en aliassen van OPNsense, gelezen op de master. Elke wijziging gaat met een herstelpunt en een terugdraaitimer op de firewall: valt de webinterface, een CARP-VIP of een gateway weg, dan staat de oude configuratie meteen terug. Daarna gaat ze naar de backup."
      />
      <Suspense>
        <OpnGate>{(c) => <Rules key={c.key} c={c} />}</OpnGate>
      </Suspense>
    </div>
  );
}

type Rule = {
  uuid: string;
  enabled: string;
  sequence?: string;
  action: string;
  quick?: string;
  interface: string;
  direction?: string;
  ipprotocol?: string;
  protocol?: string;
  source_net?: string;
  source_port?: string;
  destination_net?: string;
  destination_port?: string;
  description?: string;
  log?: string;
};

type Alias = {
  uuid: string;
  enabled: string;
  name: string;
  type: string;
  description?: string;
  content?: string;
};

type Result = { kind: "success"; job: Job } | { kind: "error"; message: string } | null;

function Rules({ c }: { c: OpnCluster }) {
  const n = readNode(c);
  const me = useMe();
  const admin = me.data?.user.role === "admin";
  const [result, setResult] = useState<Result>(null);
  const [pending, setPending] = useState<Pending | null>(null);
  if (!n) return <ClusterStrip c={c} />;
  return (
    <div className="space-y-6">
      <ClusterStrip c={c} />
      <JobBanner result={result} />
      <RulesCard n={n} admin={admin} onChange={setPending} />
      <AliasesCard id={n.node_id} admin={admin} onChange={setPending} />
      {pending && (
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

const actionTone = (a: string) => (a === "pass" ? "green" : a === "block" || a === "reject" ? "red" : "slate");

function ifaceOptions(n: OpnFirewall, current?: string): [string, string][] {
  const opts: [string, string][] = (n.facts.interfaces ?? []).map((i) => [i.name, i.descr ? `${i.descr} (${i.name})` : i.name]);
  if (current && !opts.some(([o]) => o === current)) opts.unshift([current, current]);
  return opts;
}

function ruleSpecs(n: OpnFirewall, r?: Rule): FieldSpec[] {
  return [
    { key: "enabled", label: "Aan", kind: "check" },
    {
      key: "action",
      label: "Actie",
      kind: "select",
      options: [
        ["pass", "Doorlaten (pass)"],
        ["block", "Blokkeren (block)"],
        ["reject", "Weigeren (reject)"],
      ],
    },
    { key: "interface", label: "Interface", kind: "select", options: ifaceOptions(n, r?.interface) },
    {
      key: "direction",
      label: "Richting",
      kind: "select",
      options: [
        ["in", "Binnenkomend"],
        ["out", "Uitgaand"],
      ],
    },
    {
      key: "ipprotocol",
      label: "IP-versie",
      kind: "select",
      options: [
        ["inet", "IPv4"],
        ["inet6", "IPv6"],
        ["inet46", "IPv4 en IPv6"],
      ],
    },
    {
      key: "protocol",
      label: "Protocol",
      kind: "select",
      options: [
        ["any", "Alle"],
        ["TCP", "TCP"],
        ["UDP", "UDP"],
        ["TCP/UDP", "TCP/UDP"],
        ["ICMP", "ICMP"],
      ],
    },
    { key: "source_net", label: "Bron", hint: "any, een net (192.168.90.0/24), een alias of lan, opt1 …" },
    { key: "destination_net", label: "Doel", hint: "any, een adres, een alias of wanip" },
    { key: "destination_port", label: "Doelpoort", hint: "Leeg of een poort, bereik of alias" },
    { key: "sequence", label: "Volgorde", hint: "Lager eerst" },
    { key: "log", label: "Loggen", kind: "check" },
    { key: "description", label: "Omschrijving", wide: true },
  ];
}

function ruleValues(r?: Rule): Record<string, string> {
  if (!r)
    return {
      enabled: "1",
      action: "pass",
      direction: "in",
      ipprotocol: "inet",
      protocol: "any",
      source_net: "any",
      destination_net: "any",
    };
  return {
    enabled: r.enabled,
    action: r.action,
    interface: r.interface,
    direction: r.direction ?? "in",
    ipprotocol: r.ipprotocol ?? "inet",
    protocol: r.protocol ?? "any",
    source_net: r.source_net ?? "",
    destination_net: r.destination_net ?? "",
    destination_port: r.destination_port ?? "",
    sequence: r.sequence ?? "",
    log: r.log ?? "0",
    description: r.description ?? "",
  };
}

function Before({ what, changed }: { what: string; changed: Changed }) {
  return (
    <div className="mt-2">
      <p className="text-slate-600 dark:text-slate-300">{what}</p>
      <ChangedList changed={changed} />
    </div>
  );
}

function RulesCard({ n, admin, onChange }: { n: OpnFirewall; admin: boolean; onChange: (p: Pending) => void }) {
  const q = useOpnGet<OpnSearch<Rule>>(n.node_id, "firewall/filter/searchRule");
  const [filter, setFilter] = useState("");
  const [edit, setEdit] = useState<Rule | "new" | null>(null);
  const rows = useMemo(() => {
    const f = filter.toLowerCase();
    return (q.data?.rows ?? []).filter((r) => !f || JSON.stringify(r).toLowerCase().includes(f));
  }, [q.data, filter]);
  const name = (r: Rule) => r.description || `regel ${r.sequence ?? ""}`.trim();
  return (
    <Card title={`Firewallregels${q.data ? ` · ${q.data.rows.length}` : ""}`}>
      {q.isError ? (
        <p className="text-sm text-red-700 dark:text-red-400">{(q.error as Error).message}</p>
      ) : (
        <>
          <div className="mb-3 flex flex-wrap items-center gap-2">
            <Input placeholder="Zoeken" value={filter} onChange={(e) => setFilter(e.target.value)} className="max-w-xs" />
            {admin && (
              <Button variant="secondary" onClick={() => setEdit("new")}>
                Regel toevoegen
              </Button>
            )}
          </div>
          {q.data && q.data.rows.length === 0 && (
            <p className="mb-2 text-sm text-slate-500">
              Geen regels via de API. Regels uit het klassieke scherm (Firewall → Rules) staan in de revisies; nieuwe regels die je hier
              maakt, komen in de nieuwe regelset van OPNsense.
            </p>
          )}
          <div className="overflow-x-auto">
            <table className={tableClass}>
              <thead>
                <tr>
                  <th className={thClass}>#</th>
                  <th className={thClass}>Actie</th>
                  <th className={thClass}>Interface</th>
                  <th className={thClass}>Bron</th>
                  <th className={thClass}>Doel</th>
                  <th className={thClass}>Poort</th>
                  <th className={thClass}>Omschrijving</th>
                  {admin && <th className={thClass} />}
                </tr>
              </thead>
              <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
                {rows.map((r) => (
                  <tr key={r.uuid} className={r.enabled === "0" ? "opacity-50" : ""}>
                    <td className={tdClass}>{r.sequence}</td>
                    <td className={tdClass}>
                      <Badge tone={actionTone(r.action)}>{r.action}</Badge>
                    </td>
                    <td className={tdClass}>{r.interface || "zwevend"}</td>
                    <td className={`${tdClass} font-mono text-xs`}>{r.source_net || "any"}</td>
                    <td className={`${tdClass} font-mono text-xs`}>{r.destination_net || "any"}</td>
                    <td className={`${tdClass} font-mono text-xs`}>
                      {r.protocol && r.protocol !== "any" ? `${r.protocol} ` : ""}
                      {r.destination_port || ""}
                    </td>
                    <td className={tdClass}>{r.description}</td>
                    {admin && (
                      <td className={`${tdClass} whitespace-nowrap text-xs`}>
                        <RowActions
                          on={r.enabled !== "0"}
                          onEdit={() => setEdit(r)}
                          onToggle={() =>
                            onChange({
                              calls: [{ path: `firewall/filter/toggleRule/${r.uuid}/${r.enabled === "0" ? 1 : 0}` }],
                              before: <Before what={`Regel ${name(r)} ${r.enabled === "0" ? "aanzetten" : "uitzetten"}.`} changed={[]} />,
                            })
                          }
                          onDelete={() =>
                            onChange({
                              calls: [{ path: `firewall/filter/delRule/${r.uuid}` }],
                              before: <Before what={`Regel ${name(r)} verwijderen.`} changed={[]} />,
                            })
                          }
                        />
                      </td>
                    )}
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </>
      )}
      {edit && (
        <EditForm
          title={edit === "new" ? "Firewallregel toevoegen" : `Firewallregel ${name(edit)} wijzigen`}
          specs={ruleSpecs(n, edit === "new" ? undefined : edit)}
          initial={ruleValues(edit === "new" ? undefined : edit)}
          isNew={edit === "new"}
          onClose={() => setEdit(null)}
          onNext={(changed) => {
            const isNew = edit === "new";
            const all = isNew ? ruleValues() : {};
            onChange({
              calls: [
                isNew
                  ? { path: "firewall/filter/addRule", body: toBody("rule", changed, all) }
                  : { path: `firewall/filter/setRule/${edit.uuid}`, body: toBody("rule", changed) },
              ],
              before: <Before what={isNew ? "Nieuwe regel:" : `Regel ${name(edit)}:`} changed={changed} />,
            });
            setEdit(null);
          }}
        />
      )}
    </Card>
  );
}

function RowActions({ on, onEdit, onToggle, onDelete }: { on: boolean; onEdit: () => void; onToggle: () => void; onDelete: () => void }) {
  return (
    <span className="flex gap-2 text-slate-500">
      <button type="button" className="underline" onClick={onEdit}>
        wijzigen
      </button>
      <button type="button" className="underline" onClick={onToggle}>
        {on ? "uitzetten" : "aanzetten"}
      </button>
      <button type="button" className="text-red-700 underline dark:text-red-400" onClick={onDelete}>
        verwijderen
      </button>
    </span>
  );
}

const aliasTypes: [string, string][] = [
  ["host", "Host(s)"],
  ["network", "Netwerk(en)"],
  ["port", "Poort(en)"],
  ["url", "URL (IP's)"],
  ["urltable", "URL-tabel"],
  ["geoip", "GeoIP"],
  ["networkgroup", "Netwerkgroep"],
  ["mac", "MAC-adres"],
  ["asn", "BGP ASN"],
  ["dynipv6host", "Dynamische IPv6-host"],
  ["internal", "Intern"],
  ["external", "Extern (geavanceerd)"],
];

const aliasSpecs: FieldSpec[] = [
  { key: "enabled", label: "Aan", kind: "check" },
  { key: "name", label: "Naam", hint: "Letters, cijfers en _; zo heet hij in de regels" },
  { key: "type", label: "Type", kind: "select", options: aliasTypes },
  { key: "content", label: "Inhoud", kind: "textarea", hint: "Eén waarde per regel" },
  { key: "description", label: "Omschrijving", wide: true },
];

function AliasesCard({ id, admin, onChange }: { id: string; admin: boolean; onChange: (p: Pending) => void }) {
  const q = useOpnGet<OpnSearch<Alias>>(id, "firewall/alias/searchItem");
  const [filter, setFilter] = useState("");
  const [edit, setEdit] = useState<Alias | "new" | null>(null);
  const rows = useMemo(() => {
    const f = filter.toLowerCase();
    return (q.data?.rows ?? []).filter((r) => !f || `${r.name} ${r.description} ${r.content}`.toLowerCase().includes(f));
  }, [q.data, filter]);
  // Aliassen van OPNsense zelf (bogons, sshlockout …) hebben geen echte uuid.
  const own = (a: Alias) => /^[0-9a-f]{8}-/.test(a.uuid);
  const values = (a?: Alias): Record<string, string> =>
    a
      ? {
          enabled: a.enabled,
          name: a.name,
          type: a.type,
          content: (a.content ?? "").split(",").join("\n"),
          description: a.description ?? "",
        }
      : { enabled: "1", type: "host" };
  return (
    <Card title={`Aliassen${q.data ? ` · ${q.data.rows.length}` : ""}`}>
      {q.isError ? (
        <p className="text-sm text-red-700 dark:text-red-400">{(q.error as Error).message}</p>
      ) : (
        <>
          <div className="mb-3 flex flex-wrap items-center gap-2">
            <Input placeholder="Zoeken" value={filter} onChange={(e) => setFilter(e.target.value)} className="max-w-xs" />
            {admin && (
              <Button variant="secondary" onClick={() => setEdit("new")}>
                Alias toevoegen
              </Button>
            )}
          </div>
          <div className="overflow-x-auto">
            <table className={tableClass}>
              <thead>
                <tr>
                  <th className={thClass}>Naam</th>
                  <th className={thClass}>Type</th>
                  <th className={thClass}>Inhoud</th>
                  <th className={thClass}>Omschrijving</th>
                  {admin && <th className={thClass} />}
                </tr>
              </thead>
              <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
                {rows.map((a) => (
                  <tr key={a.uuid} className={a.enabled === "0" ? "opacity-50" : ""}>
                    <td className={`${tdClass} font-medium`}>{a.name}</td>
                    <td className={tdClass}>{a.type}</td>
                    <td className={`${tdClass} max-w-xs truncate font-mono text-xs`} title={a.content}>
                      {(a.content ?? "").split("\n").join(", ") || "–"}
                    </td>
                    <td className={tdClass}>{a.description || <span className="text-slate-400">–</span>}</td>
                    {admin && (
                      <td className={`${tdClass} whitespace-nowrap text-xs`}>
                        {own(a) && (
                          <RowActions
                            on={a.enabled !== "0"}
                            onEdit={() => setEdit(a)}
                            onToggle={() =>
                              onChange({
                                calls: [{ path: `firewall/alias/toggleItem/${a.uuid}/${a.enabled === "0" ? 1 : 0}` }],
                                before: <Before what={`Alias ${a.name} ${a.enabled === "0" ? "aanzetten" : "uitzetten"}.`} changed={[]} />,
                              })
                            }
                            onDelete={() =>
                              onChange({
                                calls: [{ path: `firewall/alias/delItem/${a.uuid}` }],
                                before: (
                                  <Before
                                    what={`Alias ${a.name} verwijderen. OPNsense weigert dat als een regel hem nog gebruikt.`}
                                    changed={[]}
                                  />
                                ),
                              })
                            }
                          />
                        )}
                      </td>
                    )}
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </>
      )}
      {edit && (
        <EditForm
          title={edit === "new" ? "Alias toevoegen" : `Alias ${edit.name} wijzigen`}
          specs={aliasSpecs}
          initial={values(edit === "new" ? undefined : edit)}
          isNew={edit === "new"}
          onClose={() => setEdit(null)}
          onNext={(changed) => {
            const isNew = edit === "new";
            // De naam gaat altijd mee, zodat het logboek zegt welke alias.
            const keep = isNew ? values() : { name: edit.name };
            onChange({
              calls: [
                isNew
                  ? { path: "firewall/alias/addItem", body: toBody("alias", changed, keep) }
                  : { path: `firewall/alias/setItem/${edit.uuid}`, body: toBody("alias", changed, keep) },
              ],
              before: <Before what={isNew ? "Nieuwe alias:" : `Alias ${edit.name}:`} changed={changed} />,
            });
            setEdit(null);
          }}
        />
      )}
    </Card>
  );
}
