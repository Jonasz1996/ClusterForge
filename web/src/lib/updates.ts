"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, unwrap } from "./api/client";
import type { components } from "./api/schema";

export type UpdateNode = components["schemas"]["UpdateNode"];
export type UpdateInput = components["schemas"]["UpdateInput"];
export type DockerReport = components["schemas"]["DockerReport"];
export type ComposeProject = UpdateNode["compose_projects"][number];
export type UpdatePolicy = components["schemas"]["UpdatePolicy"];
export type UpdatePolicies = components["schemas"]["UpdatePolicies"];
export type ClusterPolicy = UpdatePolicies["clusters"][number];
export type ComposeFile = components["schemas"]["ComposeFiles"]["files"][number];
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

export function useUpdatePolicies() {
  return useQuery({
    queryKey: ["updates", "policies"],
    queryFn: async () => unwrap(await api.GET("/updates/policies")),
    refetchInterval: 60_000,
  });
}

export function useSetEnvPolicy() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({ env, body }: { env: "lab" | "test" | "prod"; body: UpdatePolicy & { confirm?: string } }) =>
      unwrap(await api.PUT("/updates/policies/environments/{environment}", { params: { path: { environment: env } }, body })),
    onSuccess: (data) => qc.setQueryData(["updates", "policies"], data),
  });
}

export function useSetClusterPolicy() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({ id, body }: { id: string; body: components["schemas"]["ClusterUpdatePolicyInput"] }) =>
      unwrap(await api.PUT("/updates/policies/clusters/{clusterId}", { params: { path: { clusterId: id } }, body })),
    onSuccess: (data) => qc.setQueryData(["updates", "policies"], data),
  });
}

export function useReadCompose() {
  return useMutation({
    mutationFn: async (body: { node_id: string; project: string }) => unwrap(await api.POST("/updates/compose", { body })),
  });
}

export function useWriteCompose() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (body: components["schemas"]["ComposeWriteInput"]) => unwrap(await api.PUT("/updates/compose", { body })),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ["updates"] });
      void qc.invalidateQueries({ queryKey: ["jobs"] });
    },
  });
}

// proposalFile haalt het aangepaste bestand uit het voorstel van de
// assistent: het laatste codeblok, met het pad in de eerste regel.
export function proposalFile(text: string): { path: string; content: string } | null {
  const blocks = [...text.matchAll(/```(?:ya?ml|sh|env|dotenv)?\n([\s\S]*?)```/g)];
  if (blocks.length === 0) return null;
  let content = blocks[blocks.length - 1][1];
  const first = content.split("\n", 1)[0];
  const m = first.match(/^#\s*(\/\S+)\s*$/);
  if (!m) return { path: "", content };
  content = content.slice(first.length + 1);
  return { path: m[1], content };
}

export const resultLabels: Record<DockerReport["result"], { text: string; tone: "green" | "slate" | "amber" | "red" }> = {
  up_to_date: { text: "al bij", tone: "slate" },
  updated: { text: "bijgewerkt", tone: "green" },
  rolled_back: { text: "teruggezet", tone: "amber" },
  failed: { text: "mislukt", tone: "red" },
};

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
