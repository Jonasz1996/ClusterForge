"use client";

import { Suspense, useState } from "react";
import { tableClass, tdClass, thClass } from "@/components/inventory/bits";
import { ChangeDialog, ClusterStrip, JobBanner, OpnGate } from "@/components/opnsense/bits";
import { Alert, Button, Card, PageHeader, Select } from "@/components/ui";
import { useMe } from "@/lib/auth";
import { opnDiff, readNode, revisionTime, useOpnRevisions, type OpnCall, type OpnCluster, type OpnDiff } from "@/lib/opnsense";
import type { Job } from "@/lib/proxmox";

const noCalls: OpnCall[] = [];

export default function OpnRevisionsPage() {
  return (
    <div className="space-y-6">
      <PageHeader
        title="Revisies"
        description="Elke wijziging aan een OPNsense-firewall schrijft een revisie van config.xml. Vergelijk twee revisies, of de configuratie van de master met die van de backup. Wachtwoorden, sleutels en certificaten haalt de agent eruit voor ze de firewall verlaten."
      />
      <Suspense>
        <OpnGate>{(c) => <Revisions key={c.key} c={c} />}</OpnGate>
      </Suspense>
    </div>
  );
}

function Revisions({ c }: { c: OpnCluster }) {
  const me = useMe();
  const admin = me.data?.user.role === "admin";
  const first = readNode(c);
  const [nodeId, setNodeId] = useState(first?.node_id ?? "");
  const node = c.nodes.find((n) => n.node_id === nodeId);
  const q = useOpnRevisions(node && !node.why ? nodeId : undefined);
  const [pick, setPick] = useState<string[]>([]);
  const [diff, setDiff] = useState<OpnDiff | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const others = c.nodes.filter((n) => n.node_id !== nodeId && !n.why);
  // Terugzetten gaat altijd op de master (en daarna de sync), dus alleen
  // met de revisies van de master.
  const canRestore = admin && (c.nodes.length === 1 || c.master === nodeId);
  const [restore, setRestore] = useState<string | null>(null);
  const [result, setResult] = useState<{ kind: "success"; job: Job } | null>(null);

  const run = async (from: string, to: string, other?: string) => {
    setBusy(true);
    setError(null);
    try {
      setDiff(await opnDiff(nodeId, from, to, other));
    } catch (e) {
      setError(e instanceof Error ? e.message : "Vergelijken mislukt");
      setDiff(null);
    } finally {
      setBusy(false);
    }
  };
  const toggle = (f: string) => setPick((p) => (p.includes(f) ? p.filter((x) => x !== f) : [...p.slice(-1), f]));

  return (
    <div className="space-y-6">
      <ClusterStrip c={c} />
      <JobBanner result={result} />
      {restore && (
        <ChangeDialog
          nodeId={nodeId}
          calls={noCalls}
          restore={restore}
          before={
            <p className="mt-2 text-slate-600 dark:text-slate-300">
              De hele configuratie gaat terug naar die revisie, daarna herlaadt de firewall alle diensten.
              {c.ha ? " De backup krijgt ze via de sync." : ""}
            </p>
          }
          onClose={() => setRestore(null)}
          onDone={(job) => {
            setRestore(null);
            setResult({ kind: "success", job });
          }}
        />
      )}
      <div className="flex flex-wrap items-center gap-2 text-sm">
        {c.nodes.length > 1 && (
          <>
            <label htmlFor="opn-rev-node" className="text-slate-500">
              Firewall
            </label>
            <Select
              id="opn-rev-node"
              value={nodeId}
              onChange={(e) => {
                setNodeId(e.target.value);
                setPick([]);
                setDiff(null);
              }}
              className="w-auto"
            >
              {c.nodes.map((n) => (
                <option key={n.node_id} value={n.node_id}>
                  {n.hostname}
                </option>
              ))}
            </Select>
          </>
        )}
        {admin && (
          <>
            <Button variant="secondary" disabled={pick.length !== 2 || busy} onClick={() => run(older(pick, q.data), newer(pick, q.data))}>
              Vergelijk de twee gekozen
            </Button>
            {others.map((o) => (
              <Button key={o.node_id} variant="secondary" disabled={busy} onClick={() => run("current", "current", o.node_id)}>
                Vergelijk met {o.hostname}
              </Button>
            ))}
          </>
        )}
      </div>
      {error && <Alert>{error}</Alert>}
      {diff && (
        <Card
          title={diff.changed === 0 ? "Geen verschil" : diff.changed < 0 ? "Te groot om te vergelijken" : `${diff.changed} regels anders`}
        >
          {diff.changed > 0 && (
            <pre className="max-h-[32rem] overflow-auto rounded-md bg-slate-900 p-3 text-xs leading-relaxed text-slate-100 dark:bg-black">
              {diff.diff.split("\n").map((l, i) => (
                <div
                  key={i}
                  className={
                    l.startsWith("+") ? "text-green-300" : l.startsWith("-") ? "text-red-300" : l.startsWith("@@") ? "text-sky-300" : ""
                  }
                >
                  {l || " "}
                </div>
              ))}
            </pre>
          )}
          {diff.from === "current" && diff.to === "current" && (
            <p className="mt-2 text-xs text-slate-500">
              Het revisieblok en de hostname tellen niet mee. Onderdelen die OPNsense niet synchroniseert (interfaces, CARP-skew,
              pfsync-instellingen) horen wel te verschillen.
            </p>
          )}
        </Card>
      )}
      <Card title={`Revisies van ${node?.hostname ?? ""}`}>
        {node?.why ? (
          <Alert>{node.why}</Alert>
        ) : q.isError ? (
          <p className="text-sm text-red-700 dark:text-red-400">{(q.error as Error).message}</p>
        ) : (
          <div className="overflow-x-auto">
            <table className={tableClass}>
              <thead>
                <tr>
                  {admin && <th className={thClass} />}
                  <th className={thClass}>Tijd</th>
                  <th className={thClass}>Door</th>
                  <th className={thClass}>Omschrijving</th>
                  {canRestore && <th className={thClass} />}
                </tr>
              </thead>
              <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
                {(q.data ?? []).map((r) => (
                  <tr key={r.file}>
                    {admin && (
                      <td className={tdClass}>
                        <input
                          type="checkbox"
                          aria-label={`Kies ${r.file}`}
                          checked={pick.includes(r.file)}
                          onChange={() => toggle(r.file)}
                        />
                      </td>
                    )}
                    <td className={`${tdClass} whitespace-nowrap`}>
                      {r.file === "current" ? <span className="font-medium">Nu</span> : revisionTime(r.time)}
                    </td>
                    <td className={tdClass}>{r.user || "–"}</td>
                    <td className={tdClass}>{r.description || "–"}</td>
                    {canRestore && (
                      <td className={`${tdClass} text-xs whitespace-nowrap`}>
                        {r.file !== "current" && (
                          <button type="button" className="text-slate-500 underline" onClick={() => setRestore(r.file)}>
                            terugzetten
                          </button>
                        )}
                      </td>
                    )}
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Card>
    </div>
  );
}

type Rev = { file: string; time: number };

function older(pick: string[], list: Rev[] | undefined) {
  const t = (f: string) => (f === "current" ? Infinity : (list?.find((r) => r.file === f)?.time ?? 0));
  return t(pick[0]) <= t(pick[1]) ? pick[0] : pick[1];
}

function newer(pick: string[], list: Rev[] | undefined) {
  const o = older(pick, list);
  return pick[0] === o ? pick[1] : pick[0];
}
