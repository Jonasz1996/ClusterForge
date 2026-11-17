"use client";

import Link from "next/link";
import { useState, type ReactNode } from "react";
import { QueryState } from "@/components/inventory/bits";
import { Alert, Select } from "@/components/ui";
import { usePbsCall, usePbsServerId, usePbsServers, type PbsCall, type PbsServer } from "@/lib/pbs";
import type { Job } from "@/lib/proxmox";

// PbsGate kiest de Proxmox Backup Server en toont de kinderen alleen als
// ClusterForge hem via de agent kan lezen.
export function PbsGate({ children }: { children: (s: PbsServer) => ReactNode }) {
  const q = usePbsServers();
  const list = q.data?.items ?? [];
  const [id, setId] = usePbsServerId(list);
  const srv = list.find((s) => s.node_id === id);
  return (
    <QueryState q={q}>
      {list.length === 0 ? (
        <Alert kind="info">
          Nog geen Proxmox Backup Server met een agent. Installeer de agent één keer op de PBS via{" "}
          <Link href="/nodes" className="underline">
            Nodes → Agent installeren
          </Link>
          ; ClusterForge herkent een PBS vanzelf. Staat de agent er al, werk hem dan bij via Nodes → Agents bijwerken (protocol 49 nodig).
        </Alert>
      ) : (
        <div className="space-y-4">
          {list.length > 1 && (
            <div className="flex flex-wrap items-center gap-2 text-sm">
              <label htmlFor="pbs-srv" className="text-slate-500">
                Backup Server
              </label>
              <Select id="pbs-srv" value={id} onChange={(e) => setId(e.target.value)} className="w-auto">
                {list.map((s) => (
                  <option key={s.node_id} value={s.node_id}>
                    {s.hostname}
                    {s.why ? " (niet bereikbaar)" : ""}
                  </option>
                ))}
              </Select>
            </div>
          )}
          {srv && !srv.why ? children(srv) : srv ? <Alert kind="error">{srv.why}</Alert> : null}
        </div>
      )}
    </QueryState>
  );
}

// usePbsRunner start een aanroep en houdt de taak of fout bij voor een
// melding boven de kaart.
export function usePbsRunner(id: string) {
  const call = usePbsCall(id);
  const [result, setResult] = useState<{ kind: "success"; job: Job } | { kind: "error"; message: string } | null>(null);
  const run = async (c: PbsCall) => {
    try {
      const job = await call.mutateAsync(c);
      setResult({ kind: "success", job });
      return true;
    } catch (e) {
      setResult({ kind: "error", message: e instanceof Error ? e.message : "Mislukt" });
      return false;
    }
  };
  const banner =
    result?.kind === "success" ? (
      <Alert kind="success">
        Taak gestart: {result.job.title}.{" "}
        <Link href={`/taken/detail?id=${result.job.id}`} className="underline">
          Volgen
        </Link>
      </Alert>
    ) : result?.kind === "error" ? (
      <Alert kind="error">{result.message}</Alert>
    ) : null;
  return { run, pending: call.isPending, banner, clear: () => setResult(null) };
}

export const timeFmt = new Intl.DateTimeFormat("nl-BE", { dateStyle: "short", timeStyle: "short" });

export function when(unix?: number) {
  return unix ? timeFmt.format(new Date(unix * 1000)) : "–";
}
