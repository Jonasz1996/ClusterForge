"use client";

import Link from "next/link";
import { useState, type ReactNode } from "react";
import { QueryState } from "@/components/inventory/bits";
import { Alert, Select } from "@/components/ui";
import { useTrueNASCall, useTrueNASServers, type TrueNASCall, type TrueNASServer } from "@/lib/truenas";
import type { Job } from "@/lib/proxmox";

// TrueNASGate kiest de TrueNAS en toont de kinderen alleen als
// ClusterForge hem via de agent kan lezen.
export function TrueNASGate({ children }: { children: (s: TrueNASServer) => ReactNode }) {
  const q = useTrueNASServers();
  const list = q.data?.items ?? [];
  const [pick, setPick] = useState("");
  const id = pick || list.find((s) => !s.why)?.node_id || list[0]?.node_id || "";
  const srv = list.find((s) => s.node_id === id);
  return (
    <QueryState q={q}>
      {list.length === 0 ? (
        <Alert kind="info">
          Nog geen TrueNAS met een agent. Installeer de agent één keer op TrueNAS via{" "}
          <Link href="/nodes" className="underline">
            Nodes → Agent installeren
          </Link>{" "}
          (plak het commando in System → Shell van TrueNAS). ClusterForge herkent TrueNAS vanzelf en de agent overleeft een update van TrueNAS. Staat
          de agent er al, werk hem dan bij via Nodes → Agents bijwerken (protocol 53 nodig).
        </Alert>
      ) : (
        <div className="space-y-4">
          {list.length > 1 && (
            <div className="flex flex-wrap items-center gap-2 text-sm">
              <label htmlFor="tn-srv" className="text-slate-500">
                TrueNAS
              </label>
              <Select id="tn-srv" value={id} onChange={(e) => setPick(e.target.value)} className="w-auto">
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

// useTrueNASRunner start een aanroep en houdt de taak of fout bij.
export function useTrueNASRunner(id: string) {
  const call = useTrueNASCall(id);
  const [result, setResult] = useState<{ kind: "success"; job: Job } | { kind: "error"; message: string } | null>(null);
  const run = async (c: TrueNASCall) => {
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
  return { run, pending: call.isPending, banner };
}

export const tnTime = new Intl.DateTimeFormat("nl-BE", { dateStyle: "short", timeStyle: "short" });
