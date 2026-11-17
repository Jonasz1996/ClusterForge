"use client";

import { Suspense, useState } from "react";
import { tableClass, tdClass, thClass } from "@/components/inventory/bits";
import { OpnGate } from "@/components/opnsense/bits";
import type { Pending } from "@/components/opnsense/edit";
import { ItemTiles } from "@/components/opnsense/ItemTable";
import { Tile as Win } from "@/components/Tiles";
import { Badge, Card, Input, PageHeader } from "@/components/ui";
import { useOpnGet, type OpnFirewall, type OpnSearch } from "@/lib/opnsense";

export default function OpnServicesPage() {
  return (
    <div className="space-y-6">
      <PageHeader
        title="Diensten en plugins"
        description="De diensten op de OPNsense-master starten, stoppen of herstarten, en plugins installeren of verwijderen (op elke firewall, want plugins gaan niet mee met de sync). Ook dat gaat als veilige wijziging: valt de toegang weg, dan draait de firewall terug."
      />
      <Suspense>
        <OpnGate>
          {(c) => (
            <ItemTiles
              key={c.key}
              c={c}
              specs={[]}
              extra={(n, admin, onChange) => (
                <>
                  <Win>
                    <Services n={n} admin={admin} onChange={onChange} />
                  </Win>
                  <Win wide>
                    <Plugins n={n} admin={admin} onChange={onChange} />
                  </Win>
                </>
              )}
            />
          )}
        </OpnGate>
      </Suspense>
    </div>
  );
}

type Service = { id: string; name: string; description: string; running: number; locked?: number };

const say = (text: string) => <p className="mt-2 text-slate-600 dark:text-slate-300">{text}</p>;

function Services({ n, admin, onChange }: { n: OpnFirewall; admin: boolean; onChange: (p: Pending) => void }) {
  const q = useOpnGet<OpnSearch<Service>>(n.node_id, "core/service/search");
  const rows = q.data?.rows ?? [];
  const act = (s: Service, cmd: "start" | "stop" | "restart") => {
    const path = `core/service/${cmd}/${s.name}${s.id && s.id !== s.name ? `/${s.id}` : ""}`;
    const word = { start: "starten", stop: "stoppen", restart: "herstarten" }[cmd];
    onChange({ calls: [{ path }], before: say(`${s.description || s.name} ${word}.`) });
  };
  return (
    <Card title={`Diensten${q.data ? ` · ${rows.filter((s) => !s.running).length} gestopt` : ""}`}>
      {q.isError ? (
        <p className="text-sm text-red-700 dark:text-red-400">{(q.error as Error).message}</p>
      ) : (
        <ul className="divide-y divide-slate-100 text-sm dark:divide-slate-800">
          {rows.map((s) => (
            <li key={`${s.name}-${s.id}`} className="flex flex-wrap items-center justify-between gap-2 py-1.5">
              <span>
                {s.description || s.name}
                <span className="ml-2 text-xs text-slate-500">{s.name}</span>
              </span>
              <span className="flex items-center gap-2 text-xs">
                <Badge tone={s.running ? "green" : "red"}>{s.running ? "actief" : "gestopt"}</Badge>
                {admin && !s.locked && (
                  <>
                    {s.running ? (
                      <>
                        <button type="button" className="text-slate-500 underline" onClick={() => act(s, "restart")}>
                          herstarten
                        </button>
                        <button type="button" className="text-red-700 underline dark:text-red-400" onClick={() => act(s, "stop")}>
                          stoppen
                        </button>
                      </>
                    ) : (
                      <button type="button" className="text-slate-500 underline" onClick={() => act(s, "start")}>
                        starten
                      </button>
                    )}
                  </>
                )}
              </span>
            </li>
          ))}
        </ul>
      )}
    </Card>
  );
}

type Plugin = { name: string; version: string; comment?: string; installed: string; locked?: string; provided?: string; tier?: string };

function Plugins({ n, admin, onChange }: { n: OpnFirewall; admin: boolean; onChange: (p: Pending) => void }) {
  const q = useOpnGet<{ plugin?: Plugin[] }>(n.node_id, "core/firmware/info", { refetch: 120_000 });
  const [filter, setFilter] = useState("");
  const [all, setAll] = useState(false);
  const list = (q.data?.plugin ?? [])
    .filter((p) => all || p.installed === "1")
    .filter((p) => !filter || `${p.name} ${p.comment}`.toLowerCase().includes(filter.toLowerCase()))
    .sort((a, b) => Number(b.installed) - Number(a.installed) || a.name.localeCompare(b.name));
  const installed = (q.data?.plugin ?? []).filter((p) => p.installed === "1").length;
  return (
    <Card title={`Plugins${q.data ? ` · ${installed} geïnstalleerd` : ""}`}>
      {q.isError ? (
        <p className="text-sm text-red-700 dark:text-red-400">{(q.error as Error).message}</p>
      ) : (
        <>
          <div className="mb-3 flex flex-wrap items-center gap-3 text-sm">
            <Input placeholder="Zoeken" value={filter} onChange={(e) => setFilter(e.target.value)} className="max-w-xs" />
            <label className="flex items-center gap-2">
              <input type="checkbox" checked={all} onChange={(e) => setAll(e.target.checked)} />
              Ook niet geïnstalleerd
            </label>
          </div>
          <div className="overflow-x-auto">
            <table className={tableClass}>
              <thead>
                <tr>
                  <th className={thClass}>Plugin</th>
                  <th className={thClass}>Versie</th>
                  <th className={thClass}>Omschrijving</th>
                  {admin && <th className={thClass} />}
                </tr>
              </thead>
              <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
                {list.map((p) => (
                  <tr key={p.name}>
                    <td className={`${tdClass} font-medium`}>
                      {p.name}
                      {p.installed === "1" && (
                        <span className="ml-2">
                          <Badge tone="green">geïnstalleerd</Badge>
                        </span>
                      )}
                    </td>
                    <td className={tdClass}>{p.version}</td>
                    <td className={tdClass}>{p.comment}</td>
                    {admin && (
                      <td className={`${tdClass} text-xs whitespace-nowrap`}>
                        {p.installed === "1" ? (
                          <button
                            type="button"
                            className="text-red-700 underline dark:text-red-400"
                            onClick={() =>
                              onChange({
                                calls: [{ path: `core/firmware/remove/${p.name}` }],
                                before: say(`Plugin ${p.name} verwijderen.`),
                              })
                            }
                          >
                            verwijderen
                          </button>
                        ) : (
                          <button
                            type="button"
                            className="text-slate-500 underline"
                            onClick={() =>
                              onChange({
                                calls: [{ path: `core/firmware/install/${p.name}` }],
                                before: say(`Plugin ${p.name} installeren.`),
                              })
                            }
                          >
                            installeren
                          </button>
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
    </Card>
  );
}
