"use client";

import { useQuery } from "@tanstack/react-query";
import Link from "next/link";
import { useState, type ReactNode } from "react";
import { upgradeCommand } from "@/components/drift/Drift";
import { CopyBlock } from "@/components/inventory/InstallAgent";
import { tableClass, tdClass, thClass } from "@/components/inventory/bits";
import { Alert, Badge, Button, Card } from "@/components/ui";
import { api, unwrap } from "@/lib/api/client";
import type { components } from "@/lib/api/schema";
import { timesSeen } from "@/lib/deps";

type ConnOut = components["schemas"]["NodeConnOut"];
type ConnIn = components["schemas"]["NodeConnIn"];

const linkClass = "text-brand-600 hover:underline dark:text-brand-500";
const timeFmt = new Intl.DateTimeFormat("nl-BE", { hour: "2-digit", minute: "2-digit" });
// Zoveel regels per tabel tot je op Alles tonen klikt.
const shown = 12;

export function useNodeConnections(nodeId: string) {
  return useQuery({
    queryKey: ["nodes", nodeId, "connections"],
    queryFn: async () => unwrap(await api.GET("/nodes/{nodeId}/connections", { params: { path: { nodeId } } })),
    refetchInterval: 60_000,
  });
}

// addrPort schrijft 10.0.30.100:3306, en [2001:db8::1]:443 bij IPv6.
const addrPort = (addr: string, port: number) => `${addr.includes(":") ? `[${addr}]` : addr}:${port}`;

const depStates: Record<string, { label: string; tone: "green" | "blue" | "slate" }> = {
  confirmed: { label: "afhankelijkheid", tone: "green" },
  suggested: { label: "voorstel", tone: "blue" },
  ignored: { label: "genegeerd", tone: "slate" },
};

// Dest zegt waar een uitgaande verbinding heen gaat, met een link.
function Dest({ c }: { c: ConnOut }) {
  let what: ReactNode;
  switch (c.dest) {
    case "service":
      what = (
        <Link href={`/afhankelijkheden?dienst=${c.service_id}`} className={linkClass}>
          {c.dest_label}
        </Link>
      );
      break;
    case "node":
      what = (
        <>
          <Link href={`/nodes/detail?id=${c.node_id}`} className={linkClass}>
            {c.dest_label}
          </Link>
          <span className="text-slate-500">, geen dienst op poort {c.port}</span>
        </>
      );
      break;
    case "vip":
      what = (
        <>
          <Link href={`/clusters/detail?id=${c.cluster_id}`} className={linkClass}>
            {c.dest_label}
          </Link>
          <span className="text-slate-500">, geen dienst op poort {c.port}</span>
        </>
      );
      break;
    case "clusterforge":
      what = <span className="text-slate-500">ClusterForge zelf</span>;
      break;
    default:
      what = <span className="text-slate-500">onbekend adres</span>;
  }
  const st = c.dependency_state ? depStates[c.dependency_state] : undefined;
  return (
    <span className="inline-flex flex-wrap items-center gap-x-1.5 gap-y-1">
      <span>{what}</span>
      {st && <Badge tone={st.tone}>{st.label}</Badge>}
    </span>
  );
}

function Seen({ count, last }: { count: number; last: string }) {
  return (
    <span className="tabular-nums whitespace-nowrap">
      {timesSeen(count)}
      <span className="text-xs text-slate-500"> · laatst {timeFmt.format(new Date(last))}</span>
    </span>
  );
}

function More({ total, all, onAll }: { total: number; all: boolean; onAll: () => void }) {
  if (total <= shown || all) return null;
  return (
    <Button variant="ghost" className="mt-2 px-2 py-1 text-xs" onClick={onAll}>
      Alle {total} tonen
    </Button>
  );
}

// NodeConnectionsCard toont voor een beheerder de TCP-verbindingen die de
// agent de afgelopen 24 uur zag, en waar ze heen gaan. Inkomend van een
// adres dat geen node is, staat alleen samengeteld per poort.
export function NodeConnectionsCard({ nodeId }: { nodeId: string }) {
  const q = useNodeConnections(nodeId);
  const [allOut, setAllOut] = useState(false);
  const [allIn, setAllIn] = useState(false);
  const d = q.data;
  const outRows = d ? (allOut ? d.out : d.out.slice(0, shown)) : [];
  const inRows = d ? (allIn ? d.in : d.in.slice(0, shown)) : [];
  return (
    <Card title="Verbindingen">
      <div className="space-y-4 text-sm">
        <p className="text-slate-600 dark:text-slate-400">
          De TCP-verbindingen van de afgelopen 24 uur. De agent kijkt elke minuut; een verbinding die een uur openstaat, telt 60
          keer. Een verbinding van een dienst naar een andere wordt een voorstel op{" "}
          <Link href="/afhankelijkheden" className={linkClass}>
            Afhankelijkheden
          </Link>
          .
        </p>
        {q.isError && <Alert>{q.error instanceof Error ? q.error.message : "Verbindingen laden mislukt"}</Alert>}
        {d && !d.supported && (
          <div className="space-y-2 rounded border border-amber-200 bg-amber-50 px-3 py-2 text-amber-900 dark:border-amber-900 dark:bg-amber-950 dark:text-amber-200">
            <p>
              <span className="font-medium">Agent te oud voor verbindingen.</span> Werk cf-agent bij met dit commando op de node;
              de aanmelding blijft staan.
            </p>
            <CopyBlock text={upgradeCommand()} />
          </div>
        )}
        {d && d.supported && d.out.length === 0 && d.in.length === 0 && (
          <p className="text-slate-500">De agent zag de afgelopen 24 uur geen gevestigde verbindingen.</p>
        )}
        {d && d.out.length > 0 && (
          <section>
            <h3 className="mb-1 font-medium">Uitgaand</h3>
            <div className="overflow-x-auto">
              <table className={tableClass}>
                <thead className="border-b border-slate-200 dark:border-slate-800">
                  <tr>
                    <th className={thClass}>Unit</th>
                    <th className={thClass}>Naar</th>
                    <th className={thClass}>Wat</th>
                    <th className={thClass}>Gezien</th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
                  {outRows.map((c) => (
                    <tr key={`${c.unit}-${c.addr}-${c.port}`}>
                      <td className={tdClass}>{c.unit || <span className="text-slate-400">–</span>}</td>
                      <td className={tdClass}>
                        <span className="font-mono text-xs">{addrPort(c.addr, c.port)}</span>
                      </td>
                      <td className={tdClass}>
                        <Dest c={c} />
                      </td>
                      <td className={tdClass}>
                        <Seen count={c.count} last={c.last_seen} />
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
            <More total={d.out.length} all={allOut} onAll={() => setAllOut(true)} />
          </section>
        )}
        {d && d.in.length > 0 && (
          <section>
            <h3 className="mb-1 font-medium">Inkomend</h3>
            <div className="overflow-x-auto">
              <table className={tableClass}>
                <thead className="border-b border-slate-200 dark:border-slate-800">
                  <tr>
                    <th className={thClass}>Poort</th>
                    <th className={thClass}>Unit</th>
                    <th className={thClass}>Van</th>
                    <th className={thClass}>Gezien</th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
                  {inRows.map((c: ConnIn) => (
                    <tr key={`${c.port}-${c.unit}-${c.peer_node_id ?? ""}`}>
                      <td className={`${tdClass} tabular-nums`}>{c.port}</td>
                      <td className={tdClass}>{c.unit || <span className="text-slate-400">–</span>}</td>
                      <td className={tdClass}>
                        {c.peer_node_id ? (
                          <Link href={`/nodes/detail?id=${c.peer_node_id}`} className={linkClass}>
                            {c.peer_hostname}
                          </Link>
                        ) : (
                          <span className="text-slate-500">andere adressen, samengeteld</span>
                        )}
                      </td>
                      <td className={tdClass}>
                        <Seen count={c.count} last={c.last_seen} />
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
            <More total={d.in.length} all={allIn} onAll={() => setAllIn(true)} />
          </section>
        )}
      </div>
    </Card>
  );
}
