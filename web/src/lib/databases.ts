"use client";

import { keepPreviousData, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, unwrap } from "./api/client";
import type { components } from "./api/schema";

export type DatabaseGroup = components["schemas"]["DatabaseGroup"];
export type DatabaseServer = components["schemas"]["DatabaseServer"];
export type DatabaseInfo = components["schemas"]["DatabaseInfo"];
export type DatabaseUser = components["schemas"]["DatabaseUser"];
export type MigrationPlan = components["schemas"]["DatabaseMigrationPlan"];
export type MigrationTarget = components["schemas"]["DatabaseMigrationTarget"];
export type MigrationRun = components["schemas"]["DatabaseMigrationRun"];
export type MigrationInput = components["schemas"]["DatabaseMigrationInput"];
export type MoveInput = components["schemas"]["DatabaseMoveInput"];

type Tone = "slate" | "green" | "amber" | "red" | "blue";

export const healthInfo: Record<DatabaseGroup["health"], { label: string; tone: Tone }> = {
  ok: { label: "Gezond", tone: "green" },
  warning: { label: "Aandacht", tone: "amber" },
  down: { label: "Plat", tone: "red" },
  unknown: { label: "Onbekend", tone: "slate" },
};

export const kindLabel: Record<DatabaseGroup["kind"], string> = {
  galera: "Galera-cluster",
  patroni: "Patroni",
  replication: "Primary met replica's",
  single: "Losse server",
};

export const engineLabel: Record<DatabaseGroup["engine"], string> = {
  mariadb: "MariaDB",
  postgresql: "PostgreSQL",
};

// ClusterForge leest de databases elke minuut; het scherm volgt dat ritme.
export function useDatabases() {
  return useQuery({
    queryKey: ["databases"],
    queryFn: async () => unwrap(await api.GET("/databases")).items,
    refetchInterval: 30_000,
  });
}

// useMigrationPlan leest het plan om een database te verhuizen. ClusterForge
// vraagt de oude en de nieuwe server elke keer opnieuw, dus het scherm
// vraagt het alleen vaker zolang er een proef loopt.
export function useMigrationPlan(source: string, database: string, component: string) {
  return useQuery({
    queryKey: ["databases", "migration", source, database, component],
    queryFn: async () =>
      unwrap(
        await api.GET("/databases/migration", {
          params: { query: { source, database, component: component === "" ? undefined : component } },
        }),
      ),
    enabled: source !== "" && database !== "",
    // Bij een ander doel blijft het vorige plan staan tot het nieuwe er is.
    placeholderData: keepPreviousData,
    refetchOnWindowFocus: false,
    refetchInterval: (q) => (q.state.data?.active ? 5_000 : false),
  });
}

export function useStartTrial() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (body: MigrationInput) => unwrap(await api.POST("/databases/migration/trial", { body })),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ["databases", "migration"] });
      void qc.invalidateQueries({ queryKey: ["jobs"] });
    },
  });
}

// De echte verhuizing: starten, overschakelen, annuleren en het slot op de
// oude database. Elk ververst het plan.
function useMigrationMutation<T, R>(fn: (body: T) => Promise<R>) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: fn,
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ["databases"] });
      void qc.invalidateQueries({ queryKey: ["jobs"] });
    },
  });
}

export function useStartMove() {
  return useMigrationMutation(async (body: MoveInput) => unwrap(await api.POST("/databases/migration/move", { body })));
}

export function useSwitchMove() {
  return useMigrationMutation(async ({ id, force }: { id: string; force: boolean }) =>
    unwrap(await api.POST("/databases/migrations/{id}/switch", { params: { path: { id } }, body: { force } })),
  );
}

export function useCancelMove() {
  return useMigrationMutation(async ({ id, confirm }: { id: string; confirm: string }) =>
    unwrap(await api.POST("/databases/migrations/{id}/cancel", { params: { path: { id } }, body: { confirm } })),
  );
}

export function useLockMove() {
  return useMigrationMutation(async ({ id, locked }: { id: string; locked: boolean }) =>
    unwrap(await api.POST("/databases/migrations/{id}/lock", { params: { path: { id } }, body: { locked } })),
  );
}
