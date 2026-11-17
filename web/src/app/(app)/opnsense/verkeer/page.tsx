"use client";

import { Suspense, useState } from "react";
import { tableClass, tdClass, thClass } from "@/components/inventory/bits";
import { ago, ClusterStrip, OpnGate } from "@/components/opnsense/bits";
import { Tile as Win, TileGrid } from "@/components/Tiles";
import { Alert, Badge, Card, Input, PageHeader, Select } from "@/components/ui";
import { readNode, useOpnFlows, type OpnCluster, type OpnFlowRow } from "@/lib/opnsense";

export default function OpnTrafficPage() {
  return (
    <div className="space-y-6">
      <PageHeader
        title="Verkeer"
        description="Wat de firewall doorlaat en blokkeert, uit het filterlog van elke firewall, per uur samengevat en 30 dagen bewaard. Adressen staan met de naam van de node, het VIP of het net. Toegelaten verkeer verschijnt alleen voor regels waarop loggen aanstaat."
      />
      <Suspense>
        <OpnGate>{(c) => <Traffic key={c.key} c={c} />}</OpnGate>
      </Suspense>
    </div>
  );
}

const nf = new Intl.NumberFormat("nl-BE");

function Who({ ip, name }: { ip: string; name?: string }) {
  return name ? (
    <span>
      {name}
      <span className="ml-1 font-mono text-xs text-slate-500">{ip}</span>
    </span>
  ) : (
    <span className="font-mono text-xs">{ip}</span>
  );
}

function Traffic({ c }: { c: OpnCluster }) {
  const node = readNode(c);
  const [win, setWin] = useState<"1h" | "24h" | "7d" | "30d">("24h");
  const [action, setAction] = useState<"" | "block" | "pass">("block");
  const [phrase, setPhrase] = useState("");
  const [q, setQ] = useState("");
  const f = useOpnFlows(node?.node_id, win, action, q);
  const reads = f.data?.reads ?? [];
  return (
    <div className="space-y-4">
      <ClusterStrip c={c} />
      {reads
        .filter((r) => r.why || r.error)
        .map((r) => (
          <Alert key={r.node_id} kind={r.why ? "info" : "error"}>
            {r.hostname}: {r.why || `filterlog lezen mislukt (${r.error})`}
          </Alert>
        ))}
      <form
        className="flex flex-wrap items-center gap-2 text-sm"
        onSubmit={(e) => {
          e.preventDefault();
          setQ(phrase);
        }}
      >
        <Select aria-label="Venster" value={win} onChange={(e) => setWin(e.target.value as typeof win)} className="w-auto">
          <option value="1h">Laatste uur</option>
          <option value="24h">Laatste 24 uur</option>
          <option value="7d">Laatste 7 dagen</option>
          <option value="30d">Laatste 30 dagen</option>
        </Select>
        <Select aria-label="Actie" value={action} onChange={(e) => setAction(e.target.value as typeof action)} className="w-auto">
          <option value="block">Geblokkeerd</option>
          <option value="pass">Toegelaten</option>
          <option value="">Alles</option>
        </Select>
        <Input
          placeholder="Zoek op node, adres, poort of regel"
          value={phrase}
          onChange={(e) => setPhrase(e.target.value)}
          className="max-w-xs"
        />
        <button type="submit" className="text-slate-500 underline">
          zoeken
        </button>
        {f.data && (
          <span className="text-slate-500">
            {nf.format(f.data.blocked)} geblokkeerd · {nf.format(f.data.passed)} toegelaten
            {reads.some((r) => r.read_at) &&
              ` · gelezen ${ago(Math.floor(new Date(reads.find((r) => r.read_at)!.read_at!).getTime() / 1000))}`}
          </span>
        )}
      </form>
      {f.isError && <Alert>{(f.error as Error).message}</Alert>}
      <TileGrid>
        <Win wide>
          <Card title={`Stromen${f.data ? ` · ${f.data.rows.length}` : ""}`}>
            {!f.data ? (
              <p className="text-sm text-slate-500">Laden…</p>
            ) : f.data.rows.length === 0 ? (
              <p className="text-sm text-slate-500">Niets in dit venster.</p>
            ) : (
              <div className="max-h-[36rem] overflow-auto">
                <table className={tableClass}>
                  <thead>
                    <tr>
                      <th className={thClass}>Van</th>
                      <th className={thClass}>Naar</th>
                      <th className={thClass}>Poort</th>
                      <th className={thClass}>Interface</th>
                      <th className={thClass}>Regel</th>
                      <th className={`${thClass} text-right`}>Aantal</th>
                      <th className={thClass}>Laatst</th>
                    </tr>
                  </thead>
                  <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
                    {f.data.rows.map((r, i) => (
                      <Row key={i} r={r} />
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </Card>
        </Win>
        <Win>
          <Card title="Regels die raakten">
            {(f.data?.rules.length ?? 0) === 0 ? (
              <p className="text-sm text-slate-500">Geen.</p>
            ) : (
              <ul className="divide-y divide-slate-100 text-sm dark:divide-slate-800">
                {f.data!.rules.slice(0, 30).map((r) => (
                  <li key={`${r.rule}-${r.action}`} className="flex items-center justify-between gap-2 py-1.5">
                    <span className="min-w-0">
                      <Badge tone={r.action === "pass" ? "green" : "red"}>{r.action === "pass" ? "door" : "blok"}</Badge>{" "}
                      {r.rule_name || <span className="font-mono text-xs">{r.rule}</span>}
                    </span>
                    <span className="text-slate-500">{nf.format(r.count)}</span>
                  </li>
                ))}
              </ul>
            )}
          </Card>
        </Win>
      </TileGrid>
    </div>
  );
}

function Row({ r }: { r: OpnFlowRow }) {
  return (
    <tr>
      <td className={tdClass}>
        <Who ip={r.src} name={r.src_name} />
      </td>
      <td className={tdClass}>
        <Who ip={r.dst} name={r.dst_name} />
      </td>
      <td className={`${tdClass} whitespace-nowrap`}>{r.port ? `${r.port}/${r.proto}` : r.proto}</td>
      <td className={tdClass}>
        {r.iface_name || r.interface} <span className="text-xs text-slate-500">{r.dir === "in" ? "in" : "uit"}</span>
      </td>
      <td className={tdClass}>
        <Badge tone={r.action === "pass" ? "green" : "red"}>{r.action === "pass" ? "door" : "blok"}</Badge>{" "}
        <span className="text-xs">{r.rule_name || r.rule}</span>
      </td>
      <td className={`${tdClass} text-right`}>{nf.format(r.count)}</td>
      <td className={`${tdClass} whitespace-nowrap text-xs text-slate-500`}>{ago(Math.floor(new Date(r.last).getTime() / 1000))}</td>
    </tr>
  );
}
