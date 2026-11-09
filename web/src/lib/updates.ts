"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, unwrap } from "./api/client";
import type { components } from "./api/schema";

export type UpdateNode = components["schemas"]["UpdateNode"];
export type UpdateInput = components["schemas"]["UpdateInput"];
export type Command = components["schemas"]["Command"];
export type CommandDetail = components["schemas"]["CommandDetail"];
export type CommandInput = components["schemas"]["CommandInput"];
export type CommandRunInput = components["schemas"]["CommandRunInput"];

export function useUpdates() {
  return useQuery({
    queryKey: ["updates"],
    queryFn: async () => unwrap(await api.GET("/updates")),
    refetchInterval: 30_000,
  });
}

export function useStartUpdates() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (body: UpdateInput) => unwrap(await api.POST("/updates", { body })),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ["updates"] });
      void qc.invalidateQueries({ queryKey: ["jobs"] });
    },
  });
}

export function useCommands() {
  return useQuery({
    queryKey: ["commands"],
    queryFn: async () => unwrap(await api.GET("/commands")).items,
    refetchInterval: 15_000,
  });
}

export function useCommand(id: string | null) {
  return useQuery({
    queryKey: ["commands", id],
    enabled: !!id,
    queryFn: async () => unwrap(await api.GET("/commands/{commandId}", { params: { path: { commandId: id! } } })),
    refetchInterval: (q) => (q.state.data?.runs.some((r) => r.status === "queued" || r.status === "running") ? 2_000 : 15_000),
  });
}

export function useSaveCommand() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({ id, body }: { id: string | null; body: CommandInput }) =>
      id
        ? unwrap(await api.PUT("/commands/{commandId}", { params: { path: { commandId: id } }, body }))
        : unwrap(await api.POST("/commands", { body })),
    onSuccess: () => void qc.invalidateQueries({ queryKey: ["commands"] }),
  });
}

export function useDeleteCommand() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (id: string) => unwrap(await api.DELETE("/commands/{commandId}", { params: { path: { commandId: id } } })),
    onSuccess: () => void qc.invalidateQueries({ queryKey: ["commands"] }),
  });
}

export function useRunCommand() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({ id, body }: { id: string; body: CommandRunInput }) =>
      unwrap(await api.POST("/commands/{commandId}/run", { params: { path: { commandId: id } }, body })),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ["commands"] });
      void qc.invalidateQueries({ queryKey: ["jobs"] });
    },
  });
}

// Het Zabbix-voorbeeld van Jonas: de database omzetten na een grote versie,
// met een eigen configuratie zonder HA en zonder de socket van de dienst.
export const zabbixExample = `#!/bin/sh
set -e
grep -v -e '^HANodeName' -e '^NodeAddress' -e '^Include=' -e '^PidFile' -e '^SocketDir' \\
  /etc/zabbix/cf-zabbix_server.conf > /root/zbx-upgrade.conf
printf 'Include=/etc/zabbix/cf-zabbix_server-db.conf\\nPidFile=/tmp/zbx-upgrade.pid\\nSocketDir=/tmp\\n' >> /root/zbx-upgrade.conf
zabbix_server -f -c /root/zbx-upgrade.conf
`;
