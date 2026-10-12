"use client";

import { useQuery } from "@tanstack/react-query";
import { api, unwrap } from "./api/client";
import type { components } from "./api/schema";

export type DatabaseGroup = components["schemas"]["DatabaseGroup"];
export type DatabaseServer = components["schemas"]["DatabaseServer"];
export type DatabaseInfo = components["schemas"]["DatabaseInfo"];

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
