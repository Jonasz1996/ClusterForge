"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, unwrap } from "./api/client";
import type { components, operations } from "./api/schema";

export type BackupSandbox = components["schemas"]["BackupSandbox"];
export type BackupVerification = components["schemas"]["BackupVerification"];
export type BackupVerifyReport = components["schemas"]["BackupVerifyReport"];
export type TestRunFilter = NonNullable<operations["listTestRuns"]["parameters"]["query"]>;

type Tone = "slate" | "green" | "amber" | "red" | "blue";

export const sandboxStates: Record<BackupSandbox["state"], { label: string; tone: Tone }> = {
  reserved: { label: "Gereserveerd", tone: "blue" },
  present: { label: "Bestaat", tone: "blue" },
  destroyed: { label: "Verwijderd", tone: "green" },
  destroy_failed: { label: "Verwijderen mislukt", tone: "red" },
  none: { label: "Niet in Proxmox", tone: "slate" },
};

// useTestRuns leest runs van failovertests en back-upcontroles. Zolang er
// één loopt, ververst de lijst elke 2 s.
export function useTestRuns(filter: TestRunFilter, enabled = true) {
  return useQuery({
    queryKey: ["test-runs", "list", filter] as const,
    queryFn: async () => unwrap(await api.GET("/test-runs", { params: { query: filter } })).items,
    enabled,
    refetchInterval: (q) => (q.state.data?.some((r) => !r.result) ? 2_000 : 60_000),
  });
}

// useVerifyRuns is de geschiedenis van de back-upcontroles, gedeeld door
// de pagina Back-ups en de kaarten op cluster- en nodedetail.
export function useVerifyRuns(nodeId?: string) {
  return useTestRuns(nodeId ? { kind: "backup.verify", node_id: nodeId, limit: 10 } : { kind: "backup.verify", limit: 50 });
}

function useRefresh() {
  const qc = useQueryClient();
  return () => {
    void qc.invalidateQueries({ queryKey: ["backups"] });
    void qc.invalidateQueries({ queryKey: ["test-runs"] });
    void qc.invalidateQueries({ queryKey: ["jobs"] });
  };
}

// useVerifyNode start een back-upcontrole; zonder volid de nieuwste back-up.
export function useVerifyNode() {
  const refresh = useRefresh();
  return useMutation({
    mutationFn: async ({ nodeId, volid }: { nodeId: string; volid?: string }) =>
      unwrap(
        await api.POST("/nodes/{nodeId}/backups/verify", {
          params: { path: { nodeId } },
          body: volid ? { volid } : {},
        }),
      ),
    onSuccess: refresh,
  });
}

// useCleanupSandbox verwijdert een sandbox nu, langs dezelfde weg als de
// opruimer.
export function useCleanupSandbox() {
  const refresh = useRefresh();
  return useMutation({
    mutationFn: async (id: string) =>
      unwrap(await api.POST("/backup-sandboxes/{sandboxId}/cleanup", { params: { path: { sandboxId: id } } })),
    onSuccess: refresh,
  });
}

// duration schrijft een duur zoals de server: "41 s", "3 min 12 s", "2 u 5 min".
export function duration(sec: number | null | undefined) {
  if (sec === null || sec === undefined) return "";
  const s = Math.max(0, Math.round(sec));
  if (s < 60) return `${s} s`;
  if (s < 3600) return s % 60 ? `${Math.floor(s / 60)} min ${s % 60} s` : `${s / 60} min`;
  const m = Math.floor((s % 3600) / 60);
  return m ? `${Math.floor(s / 3600)} u ${m} min` : `${Math.floor(s / 3600)} u`;
}

// recovery is de hersteltijd van een run: terugzetten plus opstarten.
export function recovery(r: BackupVerifyReport | null | undefined) {
  const m = r?.measurements;
  if (!m || m.restore_seconds === null || m.boot_seconds === null) return null;
  return m.restore_seconds + m.boot_seconds;
}

// recent zegt of een tijdstip in de laatste 30 dagen valt.
export function recent(iso: string | null | undefined, now = Date.now()) {
  return !!iso && now - new Date(iso).getTime() < 30 * 86_400_000;
}
