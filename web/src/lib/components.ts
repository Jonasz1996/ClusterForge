"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, unwrap } from "./api/client";
import type { components } from "./api/schema";

export type ClusterComponent = components["schemas"]["ClusterComponent"];
export type ComponentTemplate = components["schemas"]["ComponentTemplate"];
export type ComponentInput = components["schemas"]["ComponentInput"];
export type ComponentPlan = components["schemas"]["ComponentPlan"];
export type ComponentPlanNode = components["schemas"]["ComponentPlanNode"];
export type ComponentFinding = components["schemas"]["ComponentFinding"];
export type ComponentRecoverPlan = components["schemas"]["ComponentRecoverPlan"];
export type ComponentRecoverNode = components["schemas"]["ComponentRecoverNode"];
export type ComponentManage = components["schemas"]["ComponentManage"];
export type ComponentManageNode = components["schemas"]["ComponentManageNode"];
export type ComponentManageAction = components["schemas"]["ComponentManageAction"];
export type ComponentDatabases = components["schemas"]["ComponentDatabases"];
export type ComponentDatabase = components["schemas"]["ComponentDatabase"];
export type ComponentDatabaseUser = components["schemas"]["ComponentDatabaseUser"];
export type ComponentDatabaseCreated = components["schemas"]["ComponentDatabaseCreated"];
export type ComponentUserPassword = components["schemas"]["ComponentUserPassword"];
export type ComponentDumps = components["schemas"]["ComponentDumps"];
export type ComponentDump = components["schemas"]["ComponentDump"];
export type ComponentBackupSettings = components["schemas"]["ComponentBackupSettings"];
export type DatabaseBackup = components["schemas"]["DatabaseBackup"];

type Tone = "slate" | "green" | "amber" | "red" | "blue";

export const stateInfo: Record<string, { label: string; tone: Tone }> = {
  installing: { label: "Wordt geïnstalleerd", tone: "blue" },
  applying: { label: "Wordt toegepast", tone: "blue" },
  ready: { label: "Klaar", tone: "green" },
  failed: { label: "Mislukt", tone: "red" },
};

export const roleLabel: Record<string, string> = { data: "data", arbiter: "scheidsrechter" };

export function useComponentTemplates() {
  return useQuery({
    queryKey: ["component-templates"],
    queryFn: async () => unwrap(await api.GET("/component-templates")).items,
    staleTime: Infinity,
  });
}

// recovering is true zolang een taak de database opnieuw start.
export function recovering(c: ClusterComponent) {
  return c.recovery !== null && (c.recovery.status === "queued" || c.recovery.status === "running");
}

// Zolang een taak de database toepast of opnieuw start, of zolang ze weg
// is, volgt het scherm haar.
export function useClusterComponents(clusterId: string) {
  return useQuery({
    queryKey: ["clusters", clusterId, "components"],
    queryFn: async () => unwrap(await api.GET("/clusters/{clusterId}/components", { params: { path: { clusterId } } })).items,
    enabled: clusterId !== "",
    refetchInterval: (q) =>
      q.state.data?.some((c) => c.state === "installing" || c.state === "applying" || recovering(c) || c.down_since !== null)
        ? 3_000
        : 30_000,
  });
}

// Wat de taak met een node doet bij opnieuw starten.
export const recoverAction: Record<string, string> = {
  begin: "begint het cluster",
  join: "sluit aan",
  none: "doet al mee",
  skip: "doet niet mee",
  later: "na het lezen van de positie",
};

export const positionFrom: Record<string, string> = {
  running: "draait",
  grastate: "grastate.dat",
  recovery: "galera_recovery",
};

export const sinceFmt = new Intl.DateTimeFormat("nl-BE", {
  day: "numeric",
  month: "numeric",
  hour: "2-digit",
  minute: "2-digit",
});

// useRecoverPlan leest wat opnieuw starten zou doen; er verandert niets,
// dus het mag bij elke opening opnieuw.
export function useRecoverPlan(clusterId: string, componentId: string) {
  return useQuery({
    queryKey: ["clusters", clusterId, "components", componentId, "recover-plan"],
    queryFn: async () =>
      unwrap(
        await api.POST("/clusters/{clusterId}/components/{componentId}/recover/plan", {
          params: { path: { clusterId, componentId } },
        }),
      ),
    gcTime: 0,
    refetchOnWindowFocus: false,
  });
}

// useComponentManage leest Beheer van een database: per datanode hoe het
// ermee staat en wat er kan. Er verandert niets; zolang het venster open is,
// ververst het elke vijf seconden.
export function useComponentManage(clusterId: string, componentId: string) {
  return useQuery({
    queryKey: ["clusters", clusterId, "components", componentId, "manage"],
    queryFn: async () =>
      unwrap(
        await api.GET("/clusters/{clusterId}/components/{componentId}/manage", {
          params: { path: { clusterId, componentId } },
        }),
      ),
    gcTime: 0,
    refetchInterval: 5000,
    refetchOnWindowFocus: false,
  });
}

export function usePlanComponent(clusterId: string) {
  return useMutation({
    mutationFn: async (body: ComponentInput) =>
      unwrap(await api.POST("/clusters/{clusterId}/components/plan", { params: { path: { clusterId } }, body })),
  });
}

function useInvalidate(clusterId: string) {
  const qc = useQueryClient();
  return () => {
    void qc.invalidateQueries({ queryKey: ["clusters", clusterId] });
    void qc.invalidateQueries({ queryKey: ["jobs"] });
  };
}

export function useAddComponent(clusterId: string) {
  const invalidate = useInvalidate(clusterId);
  return useMutation({
    mutationFn: async (body: ComponentInput & { hash: string; confirm?: string }) =>
      unwrap(await api.POST("/clusters/{clusterId}/components", { params: { path: { clusterId } }, body })),
    onSuccess: invalidate,
  });
}

export function useReapplyComponent(clusterId: string) {
  const invalidate = useInvalidate(clusterId);
  return useMutation({
    mutationFn: async ({ id, confirm }: { id: string; confirm?: string }) =>
      unwrap(
        await api.POST("/clusters/{clusterId}/components/{componentId}/apply", {
          params: { path: { clusterId, componentId: id } },
          body: { confirm },
        }),
      ),
    onSuccess: invalidate,
  });
}

export function useRemoveComponent(clusterId: string) {
  const invalidate = useInvalidate(clusterId);
  return useMutation({
    mutationFn: async (id: string) =>
      unwrap(
        await api.DELETE("/clusters/{clusterId}/components/{componentId}", { params: { path: { clusterId, componentId: id } } }),
      ),
    onSuccess: invalidate,
  });
}

export function useRecoverComponent(clusterId: string) {
  const invalidate = useInvalidate(clusterId);
  return useMutation({
    mutationFn: async ({ id, hash, confirm }: { id: string; hash: string; confirm?: string }) =>
      unwrap(
        await api.POST("/clusters/{clusterId}/components/{componentId}/recover", {
          params: { path: { clusterId, componentId: id } },
          body: { hash, confirm },
        }),
      ),
    onSuccess: invalidate,
  });
}

export function useManageComponent(clusterId: string) {
  const invalidate = useInvalidate(clusterId);
  return useMutation({
    mutationFn: async ({
      id,
      ...body
    }: {
      id: string;
      action: ComponentManageAction["action"];
      node_id: string;
      hash: string;
      confirm?: string;
    }) =>
      unwrap(
        await api.POST("/clusters/{clusterId}/components/{componentId}/manage", {
          params: { path: { clusterId, componentId: id } },
          body,
        }),
      ),
    onSuccess: invalidate,
  });
}

export function useSetAutoRecover(clusterId: string) {
  const invalidate = useInvalidate(clusterId);
  return useMutation({
    mutationFn: async ({ id, on }: { id: string; on: boolean }) =>
      unwrap(
        await api.PATCH("/clusters/{clusterId}/components/{componentId}", {
          params: { path: { clusterId, componentId: id } },
          body: { auto_recover: on },
        }),
      ),
    onSuccess: invalidate,
  });
}

// useComponentDatabases leest de databases en gebruikers live op een
// datanode.
export function useComponentDatabases(clusterId: string, componentId: string) {
  return useQuery({
    queryKey: ["clusters", clusterId, "components", componentId, "databases"],
    queryFn: async () =>
      unwrap(
        await api.GET("/clusters/{clusterId}/components/{componentId}/databases", {
          params: { path: { clusterId, componentId } },
        }),
      ),
    enabled: clusterId !== "" && componentId !== "",
    refetchInterval: 30_000,
  });
}

// De mutaties hieronder geven een wachtwoord één keer terug; react-query
// houdt het alleen zolang het venster open is.
export function useCreateDatabase(clusterId: string, componentId: string) {
  const invalidate = useInvalidate(clusterId);
  return useMutation({
    mutationFn: async (body: { name: string; user?: string }) =>
      unwrap(
        await api.POST("/clusters/{clusterId}/components/{componentId}/databases", {
          params: { path: { clusterId, componentId } },
          body,
        }),
      ),
    onSuccess: invalidate,
  });
}

export function useSetUserPassword(clusterId: string, componentId: string) {
  const invalidate = useInvalidate(clusterId);
  return useMutation({
    mutationFn: async ({ user, confirm }: { user: string; confirm?: string }) =>
      unwrap(
        await api.POST("/clusters/{clusterId}/components/{componentId}/users/{user}/password", {
          params: { path: { clusterId, componentId, user } },
          body: { confirm },
        }),
      ),
    onSuccess: invalidate,
  });
}

export function useDropDatabase(clusterId: string, componentId: string) {
  const invalidate = useInvalidate(clusterId);
  return useMutation({
    mutationFn: async ({ name, typed, confirm }: { name: string; typed: string; confirm?: string }) =>
      unwrap(
        await api.POST("/clusters/{clusterId}/components/{componentId}/databases/{name}/drop", {
          params: { path: { clusterId, componentId, name } },
          body: { name: typed, confirm },
        }),
      ),
    onSuccess: invalidate,
  });
}

export function useDropUser(clusterId: string, componentId: string) {
  const invalidate = useInvalidate(clusterId);
  return useMutation({
    mutationFn: async ({ user, typed, confirm }: { user: string; typed: string; confirm?: string }) =>
      unwrap(
        await api.POST("/clusters/{clusterId}/components/{componentId}/users/{user}/drop", {
          params: { path: { clusterId, componentId, user } },
          body: { name: typed, confirm },
        }),
      ),
    onSuccess: invalidate,
  });
}

// connectionText is wat een app nodig heeft om te verbinden.
export function connectionText(c: { address: string; port: number; database?: string; user: string; password: string }) {
  return [
    `Host: ${c.address}`,
    `Poort: ${c.port}`,
    ...(c.database ? [`Database: ${c.database}`] : []),
    `Gebruiker: ${c.user}`,
    `Wachtwoord: ${c.password}`,
  ].join("\n");
}

// useComponentDumps leest de dumps zoals ClusterForge ze kent; zolang er
// een dump of terugzetten loopt, vaker.
export function useComponentDumps(clusterId: string, componentId: string) {
  return useQuery({
    queryKey: ["clusters", clusterId, "components", componentId, "dumps"],
    queryFn: async () =>
      unwrap(
        await api.GET("/clusters/{clusterId}/components/{componentId}/dumps", {
          params: { path: { clusterId, componentId } },
        }),
      ),
    enabled: clusterId !== "" && componentId !== "",
    refetchInterval: (q) => (q.state.data?.active ? 3_000 : 60_000),
  });
}

export function useDumpNow(clusterId: string, componentId: string) {
  const invalidate = useInvalidate(clusterId);
  return useMutation({
    mutationFn: async (database: string) =>
      unwrap(
        await api.POST("/clusters/{clusterId}/components/{componentId}/dumps", {
          params: { path: { clusterId, componentId } },
          body: { database },
        }),
      ),
    onSuccess: invalidate,
  });
}

export function useRestoreDump(clusterId: string, componentId: string) {
  const invalidate = useInvalidate(clusterId);
  return useMutation({
    mutationFn: async ({ dumpId, target }: { dumpId: string; target?: string }) =>
      unwrap(
        await api.POST("/clusters/{clusterId}/components/{componentId}/dumps/{dumpId}/restore", {
          params: { path: { clusterId, componentId, dumpId } },
          body: { target: target || undefined },
        }),
      ),
    onSuccess: invalidate,
  });
}

export function useSetBackup(clusterId: string, componentId: string) {
  const invalidate = useInvalidate(clusterId);
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (body: { on: boolean; at: string; keep_days: number }) =>
      unwrap(
        await api.PUT("/clusters/{clusterId}/components/{componentId}/backup", {
          params: { path: { clusterId, componentId } },
          body,
        }),
      ),
    onSuccess: () => {
      invalidate();
      void qc.invalidateQueries({ queryKey: ["backups", "databases"] });
    },
  });
}

// useDatabaseBackups is per database van een cluster de nieuwste dump,
// voor Back-ups.
export function useDatabaseBackups() {
  return useQuery({
    queryKey: ["backups", "databases"],
    queryFn: async () => unwrap(await api.GET("/backups/databases")),
    refetchInterval: 60_000,
  });
}

export const dumpStatus: Record<string, { label: string; tone: Tone }> = {
  running: { label: "bezig", tone: "blue" },
  ok: { label: "goed", tone: "green" },
  suspect: { label: "verdacht", tone: "amber" },
  failed: { label: "mislukt", tone: "red" },
};

export const dumpFreshness: Record<string, { label: string; tone: Tone }> = {
  ok: { label: "vers", tone: "green" },
  stale: { label: "te oud", tone: "amber" },
  missing: { label: "geen dump", tone: "red" },
  off: { label: "uit", tone: "slate" },
};
