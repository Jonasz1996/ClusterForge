"use client";

import Link from "next/link";
import { Suspense, useState } from "react";
import { QueryState } from "@/components/inventory/bits";
import { DatacenterGate } from "@/components/pve/bits";
import { Button, Card, cx, Field, Input, PageHeader, Select } from "@/components/ui";
import { usePveGet, type Datacenter } from "@/lib/pve";

// Het systeemlogboek van een Proxmox-host (fase 26 M6), zoals in de
// Proxmox-GUI onder host → System → Syslog. Voor zoeken over meerdere
// nodes en diensten is er de pagina Logs (fase 23).

export default function SyslogPage() {
  return (
    <div className="space-y-6">
      <PageHeader
        title="Syslog"
        description={
          <>
            Het journal van een Proxmox-host, nieuwste onderaan. Zoeken en vergelijken over nodes heen kan op{" "}
            <Link href="/logs" className="underline">
              Logs
            </Link>
            .
          </>
        }
      />
      <Suspense>
        <DatacenterGate>{(dc) => <SyslogView key={dc.id} dc={dc} />}</DatacenterGate>
      </Suspense>
    </div>
  );
}

const levels: [string, RegExp][] = [
  ["fouten", /\b(error|err|fail(ed|ure)?|fatal|crit(ical)?|panic|segfault|oom|i\/o error)\b/i],
  ["waarschuwingen", /\b(warn(ing)?|timeout|timed out|retry|degraded|unreachable)\b/i],
];

function SyslogView({ dc }: { dc: Datacenter }) {
  const nodes = dc.hosts.map((h) => h.pve_node);
  const [node, setNode] = useState(nodes[0] ?? "");
  const [count, setCount] = useState("500");
  const [filter, setFilter] = useState("");
  const [only, setOnly] = useState("");
  const [live, setLive] = useState(true);
  const q = usePveGet<string[]>(dc.id, `/nodes/${node}/journal`, { params: { lastentries: count }, refetch: live ? 10_000 : false, enabled: !!node });
  const re = levels.find(([k]) => k === only)?.[1];
  const lines = (q.data ?? []).filter((l) => (!filter || l.toLowerCase().includes(filter.toLowerCase())) && (!re || re.test(l)) && !/^-- (No entries|cursor:)/.test(l));

  return (
    <Card>
      <div className="mb-3 grid gap-3 sm:grid-cols-[12rem_8rem_10rem_1fr_auto] sm:items-end">
        <Field label="Host" htmlFor="sl-n">
          <Select id="sl-n" value={node} onChange={(e) => setNode(e.target.value)}>
            {nodes.map((n) => (
              <option key={n}>{n}</option>
            ))}
          </Select>
        </Field>
        <Field label="Regels" htmlFor="sl-c">
          <Select id="sl-c" value={count} onChange={(e) => setCount(e.target.value)}>
            <option>200</option>
            <option>500</option>
            <option>2000</option>
            <option>5000</option>
          </Select>
        </Field>
        <Field label="Alleen" htmlFor="sl-o">
          <Select id="sl-o" value={only} onChange={(e) => setOnly(e.target.value)}>
            <option value="">alles</option>
            {levels.map(([k]) => (
              <option key={k}>{k}</option>
            ))}
          </Select>
        </Field>
        <Field label="Zoeken" htmlFor="sl-f">
          <Input id="sl-f" value={filter} onChange={(e) => setFilter(e.target.value)} placeholder="corosync, pveproxy, zfs…" />
        </Field>
        <Button variant="secondary" onClick={() => setLive(!live)}>
          {live ? "Pauzeren" : "Volgen"}
        </Button>
      </div>
      <QueryState q={q}>
        <pre className="max-h-[70vh] overflow-auto rounded bg-slate-950 p-3 text-xs leading-5 text-slate-200">
          {lines.length === 0 ? (
            <span className="text-slate-500">Geen regels{filter || only ? " die passen" : ""}.</span>
          ) : (
            lines.map((l, i) => (
              <div key={i} className={cx(levels[0][1].test(l) ? "text-red-300" : levels[1][1].test(l) ? "text-amber-200" : "")}>
                {l}
              </div>
            ))
          )}
        </pre>
        <p className="mt-2 text-xs text-slate-500">
          {lines.length} van {(q.data ?? []).length} regels{live ? ", vernieuwt elke 10 seconden" : ", gepauzeerd"}.
        </p>
      </QueryState>
    </Card>
  );
}
