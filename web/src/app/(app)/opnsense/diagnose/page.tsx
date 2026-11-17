"use client";

import Link from "next/link";
import { Suspense, useState } from "react";
import { tableClass, tdClass, thClass } from "@/components/inventory/bits";
import { OpnGate } from "@/components/opnsense/bits";
import type { Pending } from "@/components/opnsense/edit";
import { ItemTiles } from "@/components/opnsense/ItemTable";
import { Tile as Win } from "@/components/Tiles";
import { Badge, Card, Input, PageHeader } from "@/components/ui";
import { useOpnGet, type OpnFirewall, type OpnSearch } from "@/lib/opnsense";

export default function OpnDiagnosePage() {
  return (
    <div className="space-y-6">
      <PageHeader
        title="Diagnose"
        description="Wat de OPNsense-master nu ziet: het firewalllog van de laatste minuten, de open verbindingen (pf-staten) en de ARP-tabel. Een verbinding wissen gaat als veilige wijziging."
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
                  <Win wide>
                    <FirewallLog n={n} />
                  </Win>
                  <Win wide>
                    <States n={n} admin={admin} onChange={onChange} />
                  </Win>
                  <Win>
                    <Arp n={n} />
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

type LogRow = {
  __timestamp__?: string;
  action?: string;
  interface?: string;
  dir?: string;
  protoname?: string;
  src?: string;
  srcport?: string;
  dst?: string;
  dstport?: string;
  label?: string;
};

function FirewallLog({ n }: { n: OpnFirewall }) {
  const q = useOpnGet<LogRow[]>(n.node_id, "diagnostics/firewall/log", { refetch: 10_000 });
  const [filter, setFilter] = useState("");
  const [blocked, setBlocked] = useState(false);
  const rows = (Array.isArray(q.data) ? q.data : [])
    .filter((r) => !blocked || r.action === "block")
    .filter((r) => !filter || JSON.stringify(r).toLowerCase().includes(filter.toLowerCase()))
    .slice(0, 200);
  return (
    <Card title="Firewalllog (live)">
      {q.isError ? (
        <p className="text-sm text-red-700 dark:text-red-400">{(q.error as Error).message}</p>
      ) : (
        <>
          <div className="mb-3 flex flex-wrap items-center gap-3 text-sm">
            <Input
              placeholder="Filter (adres, poort, regel)"
              value={filter}
              onChange={(e) => setFilter(e.target.value)}
              className="max-w-xs"
            />
            <label className="flex items-center gap-2">
              <input type="checkbox" checked={blocked} onChange={(e) => setBlocked(e.target.checked)} />
              Alleen geblokkeerd
            </label>
            <Link
              href={`/logs?node=${n.node_id}&cluster=1&kind=file&name=${encodeURIComponent("/var/log/filter/latest.log")}`}
              className="text-slate-500 underline"
            >
              Ouder en van beide firewalls: bij Logs
            </Link>
          </div>
          <div className="max-h-[28rem] overflow-auto">
            <table className={tableClass}>
              <thead>
                <tr>
                  <th className={thClass}>Tijd</th>
                  <th className={thClass}>Actie</th>
                  <th className={thClass}>Interface</th>
                  <th className={thClass}>Van</th>
                  <th className={thClass}>Naar</th>
                  <th className={thClass}>Regel</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-slate-100 font-mono text-xs dark:divide-slate-800">
                {rows.map((r, i) => (
                  <tr key={i}>
                    <td className={`${tdClass} whitespace-nowrap`}>{(r.__timestamp__ ?? "").replace("T", " ").slice(5, 19)}</td>
                    <td className={tdClass}>
                      <Badge tone={r.action === "pass" ? "green" : r.action === "block" ? "red" : "slate"}>{r.action}</Badge>
                    </td>
                    <td className={tdClass}>
                      {r.interface} {r.dir}
                    </td>
                    <td className={tdClass}>
                      {r.src}
                      {r.srcport ? `:${r.srcport}` : ""}
                    </td>
                    <td className={tdClass}>
                      {r.dst}
                      {r.dstport ? `:${r.dstport}` : ""} {r.protoname}
                    </td>
                    <td className={`${tdClass} font-sans`}>{r.label}</td>
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

type State = {
  id: string;
  creatorid: string;
  iface?: string;
  proto?: string;
  direction?: string;
  src_addr?: string;
  src_port?: string;
  dst_addr?: string;
  dst_port?: string;
  state?: string;
  descr?: string;
};

function States({ n, admin, onChange }: { n: OpnFirewall; admin: boolean; onChange: (p: Pending) => void }) {
  const [phrase, setPhrase] = useState("");
  const [search, setSearch] = useState("");
  const q = useOpnGet<OpnSearch<State>>(n.node_id, "diagnostics/firewall/query_states", {
    params: { searchPhrase: search, rowCount: "100" },
    refetch: false,
  });
  return (
    <Card title={`Verbindingen (pf-staten)${q.data ? ` · ${q.data.total}` : ""}`}>
      <form
        className="mb-3 flex gap-2"
        onSubmit={(e) => {
          e.preventDefault();
          setSearch(phrase);
        }}
      >
        <Input placeholder="Zoek op adres of poort" value={phrase} onChange={(e) => setPhrase(e.target.value)} className="max-w-xs" />
        <button type="submit" className="text-sm text-slate-500 underline">
          zoeken
        </button>
      </form>
      {q.isError ? (
        <p className="text-sm text-red-700 dark:text-red-400">{(q.error as Error).message}</p>
      ) : (
        <div className="max-h-[28rem] overflow-auto">
          <table className={tableClass}>
            <thead>
              <tr>
                <th className={thClass}>Interface</th>
                <th className={thClass}>Proto</th>
                <th className={thClass}>Van</th>
                <th className={thClass}>Naar</th>
                <th className={thClass}>Toestand</th>
                {admin && <th className={thClass} />}
              </tr>
            </thead>
            <tbody className="divide-y divide-slate-100 font-mono text-xs dark:divide-slate-800">
              {(q.data?.rows ?? []).map((r) => (
                <tr key={`${r.id}-${r.creatorid}`}>
                  <td className={tdClass}>
                    {r.iface} {r.direction}
                  </td>
                  <td className={tdClass}>{r.proto}</td>
                  <td className={tdClass}>
                    {r.src_addr}
                    {r.src_port ? `:${r.src_port}` : ""}
                  </td>
                  <td className={tdClass}>
                    {r.dst_addr}
                    {r.dst_port ? `:${r.dst_port}` : ""}
                  </td>
                  <td className={tdClass}>{r.state}</td>
                  {admin && (
                    <td className={`${tdClass} font-sans`}>
                      <button
                        type="button"
                        className="text-red-700 underline dark:text-red-400"
                        onClick={() =>
                          onChange({
                            calls: [{ path: `diagnostics/firewall/del_state/${r.id}/${r.creatorid}` }],
                            before: (
                              <p className="mt-2 font-mono text-xs">
                                {r.src_addr}:{r.src_port} → {r.dst_addr}:{r.dst_port} ({r.proto})
                              </p>
                            ),
                          })
                        }
                      >
                        wissen
                      </button>
                    </td>
                  )}
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </Card>
  );
}

type ArpRow = { ip: string; mac: string; intf?: string; intf_description?: string; manufacturer?: string; hostname?: string };

function Arp({ n }: { n: OpnFirewall }) {
  const q = useOpnGet<ArpRow[]>(n.node_id, "diagnostics/interface/getArp", { refetch: 60_000 });
  const rows = Array.isArray(q.data) ? q.data : [];
  return (
    <Card title={`ARP-tabel${q.data ? ` · ${rows.length}` : ""}`}>
      {q.isError ? (
        <p className="text-sm text-red-700 dark:text-red-400">{(q.error as Error).message}</p>
      ) : (
        <ul className="max-h-[28rem] divide-y divide-slate-100 overflow-auto text-sm dark:divide-slate-800">
          {rows.map((r) => (
            <li key={`${r.ip}-${r.intf}`} className="py-1">
              <span className="font-mono text-xs">{r.ip}</span>
              <span className="ml-2 font-mono text-xs text-slate-500">{r.mac}</span>
              <span className="block text-xs text-slate-500">
                {[r.hostname, r.manufacturer, r.intf_description || r.intf].filter(Boolean).join(" · ")}
              </span>
            </li>
          ))}
        </ul>
      )}
    </Card>
  );
}
