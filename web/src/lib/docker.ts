"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, unwrap } from "./api/client";
import type { Job } from "./proxmox";

// Docker zoals Portainer (fase 28). De types volgen internal/dockerhost en
// pkg/protocol/docker.go; wat de Docker-API zelf geeft, volgt de Docker
// Engine API.

export type DockerProblem = { id: string; name: string; state: string; status: string; reason: string };

export type DockerDisk = { images: number; containers: number; volumes: number; build_cache: number; reclaimable: number; at: string };

export type DockerSummary = {
  version: string;
  api_version?: string;
  os?: string;
  kernel?: string;
  storage_driver?: string;
  root_dir?: string;
  cpus: number;
  mem_total: number;
  containers: number;
  running: number;
  paused: number;
  stopped: number;
  restarting: number;
  unhealthy: number;
  images: number;
  problems?: DockerProblem[];
  disk?: DockerDisk;
  warnings?: string[];
};

export type DockerHost = {
  node_id: string;
  hostname: string;
  cluster_id?: string;
  cluster?: string;
  environment?: string;
  vip_holder: boolean;
  docker_version?: string;
  protocol: number;
  agent_version?: string;
  online: boolean;
  heartbeat_at?: string;
  supported: boolean;
  summary?: DockerSummary;
  // stacks die ClusterForge in het cluster beheert (fase 21).
  stacks?: { id: string; name: string }[];
  check_error?: string;
  checked_at?: string;
};

export type DockerStat = {
  id: string;
  name: string;
  cpu: number;
  mem_usage: number;
  mem_limit: number;
  net_rx: number;
  net_tx: number;
  block_read: number;
  block_write: number;
  pids: number;
};

export type DockerCall = {
  method: "POST" | "DELETE";
  path: string;
  query?: Record<string, string>;
  body?: unknown;
  title?: string;
  ignore_missing?: boolean;
};

const keys = {
  hosts: ["docker", "hosts"] as const,
  get: (node: string, path: string, query?: Record<string, string>) => ["docker", "get", node, path, query ?? {}] as const,
  stats: (node: string) => ["docker", "stats", node] as const,
};

export function useDockerHosts() {
  return useQuery({
    queryKey: keys.hosts,
    queryFn: async () =>
      unwrap(await api.GET("/docker/hosts", {})) as unknown as { hosts: DockerHost[]; protocol: number },
    refetchInterval: 30_000,
  });
}

export function useRefreshDocker() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async () =>
      unwrap(await api.GET("/docker/hosts", { params: { query: { refresh: true } } })) as unknown as {
        hosts: DockerHost[];
        protocol: number;
      },
    onSuccess: (d) => {
      qc.setQueryData(keys.hosts, d);
      qc.invalidateQueries({ queryKey: ["docker", "get"] });
    },
  });
}

export async function dockerGet<T>(node: string, path: string, query?: Record<string, string>): Promise<T> {
  const res = unwrap(
    await api.GET("/docker/nodes/{nodeId}/api", {
      params: { path: { nodeId: node }, query: { path, query: query ? JSON.stringify(query) : undefined } },
    }),
  ) as unknown as { data: T };
  return res.data;
}

// useDockerGet leest een pad van de Docker-API op een node via de agent.
export function useDockerGet<T>(node: string | undefined, path: string, query?: Record<string, string>, opts?: { enabled?: boolean; refetchInterval?: number }) {
  return useQuery({
    queryKey: keys.get(node ?? "", path, query),
    queryFn: () => dockerGet<T>(node!, path, query),
    enabled: !!node && (opts?.enabled ?? true),
    refetchInterval: opts?.refetchInterval,
    retry: false,
  });
}

export function useDockerStats(node: string | undefined, opts?: { enabled?: boolean; refetchInterval?: number }) {
  return useQuery({
    queryKey: keys.stats(node ?? ""),
    queryFn: async () =>
      (
        unwrap(await api.GET("/docker/nodes/{nodeId}/stats", { params: { path: { nodeId: node! } } })) as unknown as {
          stats: DockerStat[];
        }
      ).stats,
    enabled: !!node && (opts?.enabled ?? true),
    refetchInterval: opts?.refetchInterval,
    retry: false,
  });
}

// useDockerCall verandert iets aan Docker op een node; het wordt een taak.
export function useDockerCall(node: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (v: { calls: DockerCall[]; title?: string; confirm?: string }) =>
      unwrap(
        await api.POST("/docker/nodes/{nodeId}/calls", {
          params: { path: { nodeId: node } },
          body: v as never,
        }),
      ) as unknown as Job,
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["jobs"] });
      // De taak duurt meestal een paar seconden; daarna opnieuw lezen.
      for (const ms of [2_000, 6_000, 15_000]) {
        setTimeout(() => qc.invalidateQueries({ queryKey: ["docker"] }), ms);
      }
    },
  });
}

// Groepen per cluster, in de volgorde van de server; nodes zonder cluster
// komen als laatste onder "Losse hosts".
export type DockerGroup = { key: string; label: string; environment?: string; hosts: DockerHost[] };

export function groupHosts(hosts: DockerHost[]): DockerGroup[] {
  const groups: DockerGroup[] = [];
  const loose: DockerHost[] = [];
  for (const h of hosts) {
    if (!h.cluster_id) {
      loose.push(h);
      continue;
    }
    let g = groups.find((x) => x.key === h.cluster_id);
    if (!g) {
      g = { key: h.cluster_id, label: h.cluster ?? "", environment: h.environment, hosts: [] };
      groups.push(g);
    }
    g.hosts.push(h);
  }
  if (loose.length > 0) groups.push({ key: "", label: "Losse hosts", hosts: loose });
  return groups;
}

// hostReady zegt waarom een node geen Docker-verzoek kan doen; leeg als het kan.
export function hostReady(h: DockerHost, need: number) {
  if (!h.online) return "agent offline";
  if (!h.supported || h.protocol < need) return `agent te oud (protocol ${h.protocol}, ${need} nodig)`;
  return "";
}

export const envLabel: Record<string, string> = { prod: "Productie", test: "Test", lab: "Lab" };

// Een rij uit /containers/json (Docker Engine API).
export type DockerPort = { IP?: string; PrivatePort: number; PublicPort?: number; Type: string };
export type DockerMount = { Type: string; Name?: string; Source: string; Destination: string; RW?: boolean; Mode?: string; Driver?: string };
export type ContainerSummary = {
  Id: string;
  Names: string[];
  Image: string;
  ImageID: string;
  Command?: string;
  Created: number;
  State: string;
  Status: string;
  Ports: DockerPort[];
  Labels: Record<string, string>;
  Mounts?: DockerMount[];
  NetworkSettings?: { Networks?: Record<string, { IPAddress?: string; NetworkID?: string }> };
};

export const containerName = (c: { Names?: string[]; Id: string }) => (c.Names?.[0] ?? c.Id.slice(0, 12)).replace(/^\//, "");
export const composeProject = (c: { Labels?: Record<string, string> | null }) => c.Labels?.["com.docker.compose.project"] ?? "";

// ports maakt van de poorten een korte tekst: 8080→80/tcp.
export function portsText(ports: DockerPort[] | undefined) {
  const seen = new Set<string>();
  const out: string[] = [];
  for (const p of ports ?? []) {
    const s = p.PublicPort ? `${p.PublicPort}→${p.PrivatePort}${p.Type === "tcp" ? "" : "/" + p.Type}` : `${p.PrivatePort}/${p.Type}`;
    if (!seen.has(s)) {
      seen.add(s);
      out.push(s);
    }
  }
  return out.join(", ");
}

// containerPath maakt een pad naar een container (id is altijd veilig).
export const cpath = (id: string, rest = "") => `/containers/${id}${rest}`;

// Een rij uit /images/json.
export type ImageSummary = {
  Id: string;
  RepoTags: string[] | null;
  RepoDigests: string[] | null;
  Size: number;
  Created: number;
  Containers: number;
  Labels?: Record<string, string> | null;
};

// Een volume uit /volumes en de grootte uit /system/df.
export type VolumeSummary = {
  Name: string;
  Driver: string;
  Mountpoint: string;
  CreatedAt?: string;
  Labels?: Record<string, string> | null;
  Scope?: string;
  UsageData?: { Size: number; RefCount: number } | null;
};

export type NetworkSummary = {
  Name: string;
  Id: string;
  Driver: string;
  Scope: string;
  Internal?: boolean;
  IPAM?: { Config?: { Subnet?: string; Gateway?: string }[] | null };
  Containers?: Record<string, { Name: string; IPv4Address?: string }> | null;
  Labels?: Record<string, string> | null;
};

export type SystemDF = {
  LayersSize: number;
  Images: ImageSummary[] | null;
  Containers: { Id: string; SizeRw?: number; State: string }[] | null;
  Volumes: VolumeSummary[] | null;
  BuildCache: { Size: number; InUse: boolean; Shared: boolean }[] | null;
};

export const shortId = (id: string) => id.replace(/^sha256:/, "").slice(0, 12);
export const builtinNetworks = new Set(["bridge", "host", "none"]);

// Console in een container (M5). De sessie loopt over dezelfde websocket als
// de Proxmox-console (consoleSocketURL in lib/pve).
export type DockerShell = "auto" | "sh" | "bash";
export type DockerConsoleOpened = { session: string; kind: string; title: string };

export async function openDockerConsole(
  node: string,
  body: { container: string; shell: DockerShell; user?: string; cols?: number; rows?: number; password?: string; totp_code?: string },
): Promise<DockerConsoleOpened> {
  return unwrap(await api.POST("/docker/nodes/{nodeId}/console", { params: { path: { nodeId: node } }, body })) as DockerConsoleOpened;
}

/** De link naar de console van een container; die opent in een eigen tabblad. */
export function dockerConsoleHref(node: string, id: string) {
  return `/docker/console?${new URLSearchParams({ node, id })}`;
}
