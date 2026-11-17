"use client";

import { Suspense } from "react";
import { tableClass, tdClass, thClass } from "@/components/inventory/bits";
import { ClusterStrip, OpnGate } from "@/components/opnsense/bits";
import { Tile as Win, TileGrid } from "@/components/Tiles";
import { Card, PageHeader } from "@/components/ui";
import { readNode, useOpnGet, type OpnCluster, type OpnSearch } from "@/lib/opnsense";

export default function OpnNatPage() {
  return (
    <div className="space-y-6">
      <PageHeader
        title="NAT"
        description="Port forwards, uitgaande NAT, 1:1 en NPT zoals OPNsense ze via zijn API toont, gelezen op de master. Beheren komt in de volgende stap, met de gekoppelde regel erbij."
      />
      <Suspense>
        <OpnGate>{(c) => <Nat key={c.key} c={c} />}</OpnGate>
      </Suspense>
    </div>
  );
}

type Row = Record<string, string> & { uuid: string };

const sections: { title: string; path: string; cols: [string, string][] }[] = [
  {
    title: "Port forwards",
    path: "firewall/d_nat/searchRule",
    cols: [
      ["interface", "Interface"],
      ["protocol", "Protocol"],
      ["destination.network", "Doel"],
      ["destination.port", "Poort"],
      ["target", "Naar"],
      ["local-port", "Lokale poort"],
      ["descr", "Omschrijving"],
    ],
  },
  {
    title: "Uitgaande NAT",
    path: "firewall/source_nat/searchRule",
    cols: [
      ["interface", "Interface"],
      ["source_net", "Bron"],
      ["destination_net", "Doel"],
      ["target", "Vertaling"],
      ["description", "Omschrijving"],
    ],
  },
  {
    title: "1:1",
    path: "firewall/one_to_one/searchRule",
    cols: [
      ["interface", "Interface"],
      ["external", "Extern"],
      ["source_net", "Intern"],
      ["description", "Omschrijving"],
    ],
  },
  {
    title: "NPT",
    path: "firewall/npt/searchRule",
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
  return (
    <div className="space-y-6">
      <ClusterStrip c={c} />
      {n && (
        <TileGrid>
          {sections.map((s) => (
            <Win key={s.path} wide={s.title === "Port forwards"}>
              <Section id={n.node_id} {...s} />
            </Win>
          ))}
        </TileGrid>
      )}
    </div>
  );
}

function Section({ id, title, path, cols }: { id: string; title: string; path: string; cols: [string, string][] }) {
  const q = useOpnGet<OpnSearch<Row>>(id, path);
  const rows = q.data?.rows ?? [];
  return (
    <Card title={`${title}${q.data ? ` · ${rows.length}` : ""}`}>
      {q.isError ? (
        <p className="text-sm text-slate-500">Niet beschikbaar via de API op deze OPNsense-versie ({(q.error as Error).message}).</p>
      ) : rows.length === 0 ? (
        <p className="text-sm text-slate-500">Geen regels.</p>
      ) : (
        <div className="overflow-x-auto">
          <table className={tableClass}>
            <thead>
              <tr>
                {cols.map(([, label]) => (
                  <th key={label} className={thClass}>
                    {label}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
              {rows.map((r) => (
                <tr key={r.uuid} className={r.enabled === "0" || r.disabled === "1" ? "opacity-50" : ""}>
                  {cols.map(([k]) => (
                    <td key={k} className={`${tdClass} ${k === "description" || k === "descr" ? "" : "font-mono text-xs"}`}>
                      {r[k] || "–"}
                    </td>
                  ))}
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </Card>
  );
}
