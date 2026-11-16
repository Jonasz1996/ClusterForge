"use client";

import { useQuery } from "@tanstack/react-query";
import { useRouter, useSearchParams } from "next/navigation";
import { useState, type ReactNode } from "react";
import { Modal } from "@/components/Modal";
import { toast } from "@/components/Toast";
import { Alert, Button, Input, Select } from "@/components/ui";
import { api, ApiError, unwrap } from "@/lib/api/client";
import { groupHosts, type DockerCall, type DockerHost } from "@/lib/docker";
import type { Job } from "@/lib/proxmox";
import { useQueryClient } from "@tanstack/react-query";

export function errText(e: unknown) {
  return e instanceof ApiError ? e.message : "Er ging iets mis.";
}

// Het antwoord van één node bij /docker/all.
export type NodeResult<T> = { node_id: string; hostname: string; data?: T; error?: string };

// useDockerAll leest een lijst op elke node met Docker tegelijk.
export function useDockerAll<T>(path: string, query?: Record<string, string>, refetchInterval = 15_000) {
  return useQuery({
    queryKey: ["docker", "all", path, query ?? {}],
    queryFn: async () =>
      (
        unwrap(
          await api.GET("/docker/all", { params: { query: { path, query: query ? JSON.stringify(query) : undefined } } }),
        ) as unknown as { results: NodeResult<T>[] }
      ).results,
    refetchInterval,
  });
}

// Scope: alle nodes, één cluster (c:<id>), de losse hosts (los) of één node
// (n:<id>). Staat in de URL, zodat een link of de terugknop hem houdt.
export function useScope(): [string, (s: string) => void] {
  const params = useSearchParams();
  const router = useRouter();
  const scope = params.get("scope") ?? "";
  const set = (s: string) => {
    const p = new URLSearchParams(params.toString());
    if (s) p.set("scope", s);
    else p.delete("scope");
    const qs = p.toString();
    router.replace(qs ? `?${qs}` : "?");
  };
  return [scope, set];
}

export function inScope(h: DockerHost, scope: string) {
  if (!scope) return true;
  if (scope === "los") return !h.cluster_id;
  if (scope.startsWith("c:")) return h.cluster_id === scope.slice(2);
  if (scope.startsWith("n:")) return h.node_id === scope.slice(2);
  return true;
}

export function ScopeBar({
  hosts,
  scope,
  setScope,
  search,
  setSearch,
  children,
}: {
  hosts: DockerHost[];
  scope: string;
  setScope: (s: string) => void;
  search: string;
  setSearch: (s: string) => void;
  children?: ReactNode;
}) {
  const groups = groupHosts(hosts);
  return (
    <div className="flex flex-wrap items-center gap-2">
      <Select aria-label="Welke nodes" value={scope} onChange={(e) => setScope(e.target.value)} className="w-auto min-w-48">
        <option value="">Alle nodes</option>
        {groups.map((g) => (
          <optgroup key={g.key || "los"} label={g.label}>
            <option value={g.key ? `c:${g.key}` : "los"}>{g.key ? `Heel ${g.label}` : "Alle losse hosts"}</option>
            {g.hosts.map((h) => (
              <option key={h.node_id} value={`n:${h.node_id}`}>
                {h.hostname}
              </option>
            ))}
          </optgroup>
        ))}
      </Select>
      <div className="w-full sm:w-64">
        <Input aria-label="Zoeken" placeholder="Zoeken…" value={search} onChange={(e) => setSearch(e.target.value)} />
      </div>
      {children}
    </div>
  );
}

// danger zegt wat overgetikt moet worden, net als de server
// (internal/dockerhost/call.go Danger).
export function danger(calls: DockerCall[]) {
  const names: string[] = [];
  let prune = false;
  const name = (p: string) => decodeURIComponent(p.replace(/^\/(containers|images|volumes|networks)\//, ""));
  for (const c of calls) {
    if (c.path.endsWith("/prune")) prune = true;
    else if (c.method === "DELETE") names.push(name(c.path));
    else if (c.path.endsWith("/kill")) names.push(name(c.path.slice(0, -5)));
  }
  if (prune) return "opruimen";
  if (names.length === 1) return names[0];
  if (names.length > 1) return "verwijderen";
  return "";
}

type Pending = { node: string; hostname: string; calls: DockerCall[]; title?: string; what: ReactNode; word: string };

// useDockerRun voert Docker-acties uit als taak; gevaarlijke acties vragen
// eerst het overtikken van de naam (of verwijderen/opruimen).
export function useDockerRun() {
  const qc = useQueryClient();
  const [pending, setPending] = useState<Pending | null>(null);
  const [typed, setTyped] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  async function send(p: Omit<Pending, "word" | "what">, confirm?: string) {
    setBusy(true);
    setError("");
    try {
      const j = unwrap(
        await api.POST("/docker/nodes/{nodeId}/calls", {
          params: { path: { nodeId: p.node } },
          body: { calls: p.calls, title: p.title, confirm } as never,
        }),
      ) as unknown as Job;
      toast(`${j.title}: taak gestart`);
      qc.invalidateQueries({ queryKey: ["jobs"] });
      for (const ms of [1_500, 5_000, 12_000]) setTimeout(() => qc.invalidateQueries({ queryKey: ["docker"] }), ms);
      setPending(null);
      return true;
    } catch (e) {
      if (pending) setError(errText(e));
      else toast(errText(e), "error");
      return false;
    } finally {
      setBusy(false);
    }
  }

  function run(node: string, hostname: string, calls: DockerCall[], opts?: { title?: string; what?: ReactNode }) {
    const word = danger(calls);
    if (!word) return send({ node, hostname, calls, title: opts?.title });
    setTyped("");
    setError("");
    setPending({ node, hostname, calls, title: opts?.title, what: opts?.what, word });
    return Promise.resolve(false);
  }

  const modal = pending && (
    <Modal title="Bevestigen" onClose={() => !busy && setPending(null)}>
      <form
        className="space-y-4"
        onSubmit={(e) => {
          e.preventDefault();
          if (typed.trim() === pending.word) void send(pending, typed.trim());
        }}
      >
        <div className="text-sm">{pending.what ?? `${pending.calls.length} actie(s) op ${pending.hostname}`}</div>
        <p className="text-sm text-slate-600 dark:text-slate-400">
          Dit kan niet ongedaan gemaakt worden. Tik <b className="font-mono break-all">{pending.word}</b> over om te bevestigen.
        </p>
        <Input autoFocus aria-label="Bevestiging" value={typed} onChange={(e) => setTyped(e.target.value)} />
        {error && <Alert>{error}</Alert>}
        <div className="flex justify-end gap-2">
          <Button type="button" variant="secondary" onClick={() => setPending(null)} disabled={busy}>
            Annuleren
          </Button>
          <Button type="submit" variant="danger" disabled={busy || typed.trim() !== pending.word}>
            {busy ? "Bezig…" : "Uitvoeren"}
          </Button>
        </div>
      </form>
    </Modal>
  );

  return { run, modal, busy };
}

export function stateTone(state: string, status = ""): "green" | "amber" | "red" | "slate" | "blue" {
  if (status.includes("(unhealthy)")) return "red";
  switch (state) {
    case "running":
      return "green";
    case "restarting":
    case "dead":
      return "red";
    case "paused":
      return "blue";
    case "exited":
      return /^Exited \(0\)/.test(status) ? "slate" : "amber";
  }
  return "slate";
}

export const stateLabel: Record<string, string> = {
  running: "draait",
  exited: "gestopt",
  paused: "gepauzeerd",
  restarting: "herstart",
  created: "aangemaakt",
  dead: "dood",
  removing: "wordt verwijderd",
};
