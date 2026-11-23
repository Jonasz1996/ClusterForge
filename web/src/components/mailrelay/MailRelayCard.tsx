"use client";

import Link from "next/link";
import { useState } from "react";
import { useMutation, useQuery } from "@tanstack/react-query";
import { toast } from "@/components/Toast";
import { Alert, Badge, Button, Card, Input } from "@/components/ui";
import { ApiError, getCsrfToken } from "@/lib/api/client";

// Testlijst 3, N30: wachtrij, open-relaytest en een testmail per relay.

type RelayNode = {
  node_id: string;
  hostname: string;
  online: boolean;
  queue: number;
  deferred: number;
  active: boolean;
  vip: boolean;
  relay: "closed" | "open" | "error" | "";
  queue_warn: number;
  growing: boolean;
  at: string;
  error?: string;
};

async function call<T>(method: string, path: string, body?: unknown): Promise<T> {
  const headers: Record<string, string> = { Accept: "application/json" };
  if (body !== undefined) headers["Content-Type"] = "application/json";
  const csrf = getCsrfToken();
  if (csrf && method !== "GET") headers["X-CSRF-Token"] = csrf;
  const res = await fetch(`/api/v1${path}`, {
    method,
    headers,
    credentials: "same-origin",
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const data = await res.json().catch(() => ({}));
  if (!res.ok) throw new ApiError(res.status, data?.code ?? "unknown", data?.message ?? `Onverwachte fout (${res.status})`);
  return data as T;
}

const relayText = { closed: "geen open relay", open: "OPEN RELAY", error: "test mislukt", "": "nog niet getest" } as const;

export function MailRelayCard({ clusterId, isAdmin }: { clusterId: string; isAdmin: boolean }) {
  const [fresh, setFresh] = useState(0);
  const [to, setTo] = useState("");
  const [job, setJob] = useState<string | null>(null);
  const q = useQuery({
    queryKey: ["mailrelay", clusterId, fresh],
    queryFn: () => call<{ nodes: RelayNode[] }>("GET", `/clusters/${clusterId}/mailrelay${fresh ? "?fresh=1" : ""}`),
    refetchInterval: 60_000,
  });
  const test = useMutation({
    mutationFn: () => call<{ job_id: string }>("POST", `/clusters/${clusterId}/mailrelay/testmail`, { to }),
    onSuccess: (r) => {
      setJob(r.job_id);
      toast("Testmail gestart.");
    },
    onError: (e) => toast(e instanceof ApiError ? e.message : "Starten mislukte.", "error"),
  });
  return (
    <Card title="Postfix-relay">
      {q.isError && <Alert>{q.error instanceof ApiError ? q.error.message : "Laden mislukte."}</Alert>}
      <ul className="space-y-2 text-sm">
        {(q.data?.nodes ?? []).map((n) => (
          <li key={n.node_id} className="flex flex-wrap items-center gap-2">
            <span className="font-medium">{n.hostname}</span>
            {n.vip && <Badge tone="blue">VIP</Badge>}
            {n.error ? (
              <span className="text-slate-500">{n.error}</span>
            ) : (
              <>
                <Badge tone={n.active ? "green" : "red"}>{n.active ? "Postfix draait" : "Postfix staat stil"}</Badge>
                <Badge tone={n.relay === "closed" ? "green" : n.relay === "open" ? "red" : "amber"}>{relayText[n.relay] ?? n.relay}</Badge>
                <Badge tone={n.queue >= n.queue_warn || n.growing ? "amber" : "slate"}>
                  wachtrij {n.queue}
                  {n.deferred > 0 ? ` (${n.deferred} uitgesteld)` : ""}
                  {n.growing ? ", groeit" : ""}
                </Badge>
              </>
            )}
          </li>
        ))}
      </ul>
      <div className="mt-3 flex flex-wrap items-center gap-2">
        <Button variant="secondary" onClick={() => setFresh((f) => f + 1)} disabled={q.isFetching}>
          {q.isFetching ? "Bezig…" : "Opnieuw meten"}
        </Button>
        {isAdmin && (
          <>
            <Input
              type="email"
              value={to}
              onChange={(e) => setTo(e.target.value)}
              placeholder="ontvanger (leeg = uit de parameters)"
              className="w-64"
            />
            <Button onClick={() => test.mutate()} disabled={test.isPending}>
              Testmail sturen
            </Button>
          </>
        )}
        {job && (
          <Link href={`/taken/detail?id=${job}`} className="text-sm text-brand-700 hover:underline dark:text-sky-300">
            Log van de testmail
          </Link>
        )}
      </div>
      <p className="mt-2 text-xs text-slate-500">
        De testmail gaat via het VIP naar de smarthost; de log toont het antwoord van de provider. ClusterForge meet elke 5 minuten de wachtrij en of
        een adres van buiten kan relayen.
      </p>
    </Card>
  );
}
