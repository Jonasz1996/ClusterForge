"use client";

import Link from "next/link";
import { useState, type ReactNode } from "react";
import { QueryState } from "@/components/inventory/bits";
import { Alert, Badge, Select } from "@/components/ui";
import {
  niveauTone,
  useAIVCall,
  useAIVInstances,
  type AIVCall,
  type AIVInstance,
} from "@/lib/aiverslag";
import type { Job } from "@/lib/proxmox";

// AIVGate kiest de machine met AI-verslag en toont de kinderen alleen als
// ClusterForge AI-verslag via de agent kan lezen.
export function AIVGate({
  children,
}: {
  children: (v: AIVInstance) => ReactNode;
}) {
  const q = useAIVInstances();
  const list = q.data?.items ?? [];
  const [pick, setPick] = useState("");
  const id =
    pick || list.find((s) => !s.why)?.node_id || list[0]?.node_id || "";
  const v = list.find((s) => s.node_id === id);
  return (
    <QueryState q={q}>
      {list.length === 0 ? (
        <Alert kind="info">
          ClusterForge ziet nog geen AI-verslag. Installeer de agent één keer op
          de machine van AI-verslag (LokaleAI-1) via{" "}
          <Link href="/nodes" className="underline">
            Nodes → Agent installeren
          </Link>
          . De agent herkent AI-verslag vanzelf en koppelt zich daar als app
          &quot;ClusterForge&quot; (zichtbaar onder 📱 App in AI-verslag); je
          hoeft niets te plakken. Staat de agent er al, werk hem dan bij via
          Nodes → Agents bijwerken (protocol 54 nodig).
        </Alert>
      ) : (
        <div className="space-y-4">
          {list.length > 1 && (
            <div className="flex flex-wrap items-center gap-2 text-sm">
              <label htmlFor="aiv-pick" className="text-slate-500">
                Machine
              </label>
              <Select
                id="aiv-pick"
                value={id}
                onChange={(e) => setPick(e.target.value)}
                className="w-auto"
              >
                {list.map((s) => (
                  <option key={s.node_id} value={s.node_id}>
                    {s.hostname}
                    {s.why ? " (niet bereikbaar)" : ""}
                  </option>
                ))}
              </Select>
            </div>
          )}
          {v && !v.why ? (
            children(v)
          ) : v ? (
            <Alert kind="error">{v.why}</Alert>
          ) : null}
        </div>
      )}
    </QueryState>
  );
}

// useAIVRunner start een schrijfactie als taak en houdt de taak of fout bij.
export function useAIVRunner(id: string) {
  const call = useAIVCall(id);
  const [result, setResult] = useState<
    { kind: "success"; job: Job } | { kind: "error"; message: string } | null
  >(null);
  const run = async (c: AIVCall) => {
    try {
      const job = await call.mutateAsync(c);
      setResult({ kind: "success", job });
      return true;
    } catch (e) {
      setResult({
        kind: "error",
        message: e instanceof Error ? e.message : "Mislukt",
      });
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

export type AIVRunner = ReturnType<typeof useAIVRunner>;

export function NiveauBadge({ niveau }: { niveau?: string | null }) {
  if (!niveau) return <Badge>onbekend</Badge>;
  return <Badge tone={niveauTone[niveau] ?? "slate"}>{niveau}</Badge>;
}

export function UnitDot({ active }: { active: string }) {
  const c =
    active === "active"
      ? "bg-emerald-500"
      : active === "failed"
        ? "bg-red-500"
        : active === "activating"
          ? "bg-amber-400"
          : "bg-slate-400";
  return (
    <span
      aria-hidden
      className={`inline-block size-2.5 shrink-0 rounded-full ${c}`}
    />
  );
}

// webURL is het adres van de eigen GUI van AI-verslag (nginx op 443).
export function webURL(v: AIVInstance, link = "") {
  if (!v.address) return "";
  const l = link.startsWith("#") ? "/" + link : link;
  return `https://${v.address}${l}`;
}
