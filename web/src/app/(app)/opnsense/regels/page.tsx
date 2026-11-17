"use client";

import { Suspense, useMemo, useState } from "react";
import { tableClass, tdClass, thClass } from "@/components/inventory/bits";
import { ChangeDialog, ClusterStrip, JobBanner, OpnGate } from "@/components/opnsense/bits";
import { Badge, Button, Card, Input, PageHeader } from "@/components/ui";
import { useMe } from "@/lib/auth";
import { readNode, useOpnGet, type OpnCall, type OpnCluster, type OpnSearch } from "@/lib/opnsense";
import type { Job } from "@/lib/proxmox";

export default function OpnRulesPage() {
  return (
    <div className="space-y-6">
      <PageHeader
        title="Regels en aliassen"
        description="De firewallregels en aliassen zoals OPNsense ze via zijn API toont, gelezen op de master. Aliassen kun je hier al veilig wijzigen; regels beheren komt in de volgende stap."
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
  quick?: string;
};

type Alias = {
  uuid: string;
  enabled: string;
  name: string;
  type: string;
  description?: string;
  content?: string;
  current_items?: string;
  last_updated?: string;
};

type Result = { kind: "success"; job: Job } | { kind: "error"; message: string } | null;

function Rules({ c }: { c: OpnCluster }) {
  const n = readNode(c);
  const [result, setResult] = useState<Result>(null);
  if (!n) return <ClusterStrip c={c} />;
  return (
    <div className="space-y-6">
      <ClusterStrip c={c} />
      <JobBanner result={result} />
      <RulesCard id={n.node_id} />
      <AliasesCard id={n.node_id} onJob={setResult} />
    </div>
  );
}

const actionTone = (a: string) => (a === "pass" ? "green" : a === "block" || a === "reject" ? "red" : "slate");

function RulesCard({ id }: { id: string }) {
  const q = useOpnGet<OpnSearch<Rule>>(id, "firewall/filter/searchRule");
  const [filter, setFilter] = useState("");
  const rows = useMemo(() => {
    const f = filter.toLowerCase();
    return (q.data?.rows ?? []).filter((r) => !f || JSON.stringify(r).toLowerCase().includes(f));
  }, [q.data, filter]);
  return (
    <Card title={`Firewallregels${q.data ? ` · ${q.data.rows.length}` : ""}`}>
      {q.isError ? (
        <p className="text-sm text-red-700 dark:text-red-400">{(q.error as Error).message}</p>
      ) : q.data && q.data.rows.length === 0 ? (
        <p className="text-sm text-slate-500">
          Geen regels via de API. Op oudere versies staan de regels nog in het klassieke scherm (Firewall → Rules); die leest ClusterForge
          via de revisies.
        </p>
      ) : (
        <>
          <Input placeholder="Zoeken" value={filter} onChange={(e) => setFilter(e.target.value)} className="mb-3 max-w-xs" />
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
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </>
      )}
    </Card>
  );
}

function AliasesCard({ id, onJob }: { id: string; onJob: (r: Result) => void }) {
  const me = useMe();
  const admin = me.data?.user.role === "admin";
  const q = useOpnGet<OpnSearch<Alias>>(id, "firewall/alias/searchItem");
  const [filter, setFilter] = useState("");
  const [edit, setEdit] = useState<{ alias: Alias; descr: string } | null>(null);
  const [calls, setCalls] = useState<OpnCall[] | null>(null);
  const rows = useMemo(() => {
    const f = filter.toLowerCase();
    return (q.data?.rows ?? []).filter((r) => !f || `${r.name} ${r.description} ${r.content}`.toLowerCase().includes(f));
  }, [q.data, filter]);
  return (
    <Card title={`Aliassen${q.data ? ` · ${q.data.rows.length}` : ""}`}>
      {q.isError ? (
        <p className="text-sm text-red-700 dark:text-red-400">{(q.error as Error).message}</p>
      ) : (
        <>
          <Input placeholder="Zoeken" value={filter} onChange={(e) => setFilter(e.target.value)} className="mb-3 max-w-xs" />
          <div className="overflow-x-auto">
            <table className={tableClass}>
              <thead>
                <tr>
                  <th className={thClass}>Naam</th>
                  <th className={thClass}>Type</th>
                  <th className={thClass}>Inhoud</th>
                  <th className={thClass}>Omschrijving</th>
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
                    <td className={tdClass}>
                      {edit?.alias.uuid === a.uuid ? (
                        <form
                          className="flex gap-2"
                          onSubmit={(e) => {
                            e.preventDefault();
                            setCalls([
                              {
                                path: `firewall/alias/setItem/${a.uuid}`,
                                body: { alias: { name: a.name, description: edit.descr } },
                              },
                            ]);
                          }}
                        >
                          <Input
                            value={edit.descr}
                            onChange={(e) => setEdit({ ...edit, descr: e.target.value })}
                            maxLength={255}
                            autoFocus
                          />
                          <Button type="submit" disabled={edit.descr === (a.description ?? "")}>
                            Verder
                          </Button>
                          <Button type="button" variant="secondary" onClick={() => setEdit(null)}>
                            Annuleren
                          </Button>
                        </form>
                      ) : (
                        <span className="flex items-center gap-2">
                          <span>{a.description || <span className="text-slate-400">–</span>}</span>
                          {admin && !a.uuid.startsWith("__") && (
                            <button
                              type="button"
                              className="text-xs text-slate-500 underline"
                              onClick={() =>
                                setEdit({
                                  alias: a,
                                  descr: a.description ?? "",
                                })
                              }
                            >
                              wijzigen
                            </button>
                          )}
                        </span>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </>
      )}
      {calls && edit && (
        <ChangeDialog
          nodeId={id}
          calls={calls}
          before={
            <p className="mt-2 text-slate-600 dark:text-slate-300">
              Omschrijving van {edit.alias.name}: <span className="line-through">{edit.alias.description || "(leeg)"}</span> →{" "}
              <span className="font-medium">{edit.descr || "(leeg)"}</span>
            </p>
          }
          onClose={() => setCalls(null)}
          onDone={(job) => {
            onJob({ kind: "success", job });
            setCalls(null);
            setEdit(null);
          }}
        />
      )}
    </Card>
  );
}
