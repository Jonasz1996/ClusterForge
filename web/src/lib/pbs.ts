"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useRouter, useSearchParams } from "next/navigation";
import { useEffect } from "react";
import { api, unwrap } from "./api/client";
import type { Job } from "./proxmox";

// Fase 32: Proxmox Backup Server via de agent op de server. Lezen gaat met
// proxmox-backup-debug api get langs /pbs/servers/{id}/api; veranderen als
// taak met usePbsCall. De types volgen internal/pbs en de PBS-API zelf
// (https://pbs.proxmox.com/docs/api-viewer/).

export const PBS_PROTOCOL = 49;

export type PbsServer = {
  node_id: string;
  hostname: string;
  node: string;
  version?: string;
  datastores: string[];
  protocol: number;
  agent_version?: string;
  online: boolean;
  heartbeat_at?: string;
  why?: string;
};

export type PbsUsage = { total: number; used: number; free?: number; avail?: number };

export type PbsDatastoreUsage = {
  store: string;
  total: number;
  used: number;
  avail: number;
  history?: (number | null)[];
  "history-start"?: number;
  "history-delta"?: number;
  "estimated-full-date"?: number;
  "gc-status"?: PbsGCStatus;
  "mount-status"?: string;
  error?: string;
};

export type PbsGCStatus = {
  upid?: string;
  "index-file-count"?: number;
  "index-data-bytes"?: number;
  "disk-bytes"?: number;
  "disk-chunks"?: number;
  "removed-bytes"?: number;
  "removed-chunks"?: number;
  "pending-bytes"?: number;
  "pending-chunks"?: number;
  "removed-bad"?: number;
  "still-bad"?: number;
};

export type PbsJob = {
  kind: "gc" | "prune" | "verify" | "sync";
  id: string;
  store: string;
  schedule?: string;
  disable?: boolean;
  "last-run-state"?: string;
  "last-run-endtime"?: number;
  "last-run-upid"?: string;
  "next-run"?: number;
  remote?: string;
  comment?: string;
};

export type PbsTask = {
  upid: string;
  worker_type: string;
  worker_id?: string;
  user: string;
  starttime: number;
  endtime?: number;
  status?: string;
};

export type PbsWarning = { level: "crit" | "warn"; text: string; area: string };

export type PbsOverview = {
  server: PbsServer;
  status?: {
    cpu: number;
    wait: number;
    loadavg: number[];
    memory: PbsUsage;
    swap: PbsUsage;
    root: PbsUsage;
    uptime: number;
    kversion: string;
    cpuinfo: { model: string; cpus: number; sockets: number };
    info: { fingerprint: string };
  };
  version?: string;
  datastores: PbsDatastoreUsage[];
  jobs: PbsJob[];
  failed: PbsTask[];
  running: number;
  subscription?: string;
  warnings: PbsWarning[];
  errors: string[];
};

const keys = {
  servers: ["pbs", "servers"] as const,
  overview: (id: string) => ["pbs", id, "overview"] as const,
  get: (id: string, path: string, params?: Record<string, string>) => ["pbs", id, "get", path, params ?? {}] as const,
};

export function usePbsServers() {
  return useQuery({
    queryKey: keys.servers,
    queryFn: async () => unwrap(await api.GET("/pbs/servers")) as unknown as { items: PbsServer[]; protocol: number },
    refetchInterval: 30_000,
  });
}

// usePbsServerId is de gekozen PBS: ?pbs= in de URL, anders de vorige,
// anders de eerste die ClusterForge kan lezen.
export function usePbsServerId(list: PbsServer[] | undefined): [string, (id: string) => void] {
  const params = useSearchParams();
  const router = useRouter();
  let stored = "";
  try {
    stored = typeof window !== "undefined" ? (window.localStorage.getItem("cf.pbs") ?? "") : "";
  } catch {
    stored = "";
  }
  const fromURL = params.get("pbs") ?? "";
  const ids = (list ?? []).map((s) => s.node_id);
  const pick =
    (fromURL && ids.includes(fromURL) && fromURL) ||
    (stored && ids.includes(stored) && stored) ||
    (list ?? []).find((s) => !s.why)?.node_id ||
    ids[0] ||
    "";
  useEffect(() => {
    try {
      if (pick) window.localStorage.setItem("cf.pbs", pick);
    } catch {
      /* geen opslag: niet erg */
    }
  }, [pick]);
  const set = (id: string) => {
    const q = new URLSearchParams(params.toString());
    q.set("pbs", id);
    router.replace("?" + q.toString());
  };
  return [pick, set];
}

export function usePbsOverview(id: string, enabled = true) {
  return useQuery({
    queryKey: keys.overview(id),
    queryFn: async () => unwrap(await api.GET("/pbs/servers/{nodeId}/overview", { params: { path: { nodeId: id } } })) as unknown as PbsOverview,
    enabled: enabled && !!id,
    refetchInterval: 15_000,
    retry: false,
  });
}

export async function pbsGet<T>(id: string, path: string, params?: Record<string, string>): Promise<T> {
  const res = unwrap(
    await api.GET("/pbs/servers/{nodeId}/api", {
      params: { path: { nodeId: id }, query: { path, params: params ? JSON.stringify(params) : undefined } },
    }),
  ) as unknown as { data: T };
  return res.data;
}

export function usePbsGet<T>(id: string, path: string, opts: { params?: Record<string, string>; enabled?: boolean; refetch?: number | false } = {}) {
  return useQuery({
    queryKey: keys.get(id, path, opts.params),
    queryFn: () => pbsGet<T>(id, path, opts.params),
    enabled: (opts.enabled ?? true) && !!id && !!path,
    refetchInterval: opts.refetch ?? 15_000,
    retry: false,
  });
}

export type PbsCall = {
  method: "create" | "set" | "delete";
  path: string;
  params?: Record<string, string>;
  confirm?: string;
};

export function usePbsCall(id: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (c: PbsCall) =>
      unwrap(await api.POST("/pbs/servers/{nodeId}/api", { params: { path: { nodeId: id } }, body: c })) as unknown as Job,
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["jobs"] });
      const again = () => qc.invalidateQueries({ queryKey: ["pbs", id] });
      again();
      setTimeout(again, 3_000);
      setTimeout(again, 10_000);
    },
  });
}

export function pbsTaskFailed(t: PbsTask) {
  return t.endtime !== undefined && t.endtime !== null && !!t.status && t.status !== "OK" && !t.status.startsWith("WARNINGS");
}

const taskTypes: Record<string, string> = {
  backup: "Back-up",
  reader: "Terugzetten / lezen",
  garbage_collection: "Garbage collection",
  verify: "Verificatie",
  verificationjob: "Verify-job",
  verify_snapshot: "Verificatie snapshot",
  verify_group: "Verificatie groep",
  prune: "Prune",
  prunejob: "Prune-job",
  syncjob: "Sync-job",
  sync: "Sync",
  aptupdate: "Pakketlijsten verversen",
  "create-datastore": "Datastore maken",
  "delete-datastore": "Datastore verwijderen",
  zfscreate: "ZFS-pool maken",
  dircreate: "Map maken",
  srvstart: "Dienst starten",
  srvstop: "Dienst stoppen",
  srvrestart: "Dienst herstarten",
  srvreload: "Dienst herladen",
  "acme-new-cert": "ACME-certificaat",
  "acme-renew-cert": "ACME vernieuwen",
  logrotate: "Logrotatie",
  "mount-device": "Aankoppelen",
  "unmount-device": "Loskoppelen",
  tape_backup: "Tape-back-up",
  tape_restore: "Tape terugzetten",
  "load-media": "Tape laden",
  diskinit: "Schijf initialiseren",
  wipedisk: "Schijf wissen",
};

export function pbsTaskLabel(t: string) {
  return taskTypes[t] ?? t;
}

// worker_id is met \x2d en \x3a gecodeerd (store:vm/101 → store\x3avm\x2f101).
export function pbsWorkerID(id?: string) {
  if (!id) return "";
  return id.replace(/\\x([0-9a-fA-F]{2})/g, (_, h: string) => String.fromCharCode(parseInt(h, 16)));
}

export function pct(used: number, total: number) {
  return total > 0 ? (used * 100) / total : 0;
}

export const jobKindLabel: Record<PbsJob["kind"], string> = {
  gc: "Garbage collection",
  prune: "Prune",
  verify: "Verify",
  sync: "Sync",
};

// pbsCreateToken maakt of vernieuwt een API-token; de waarde komt één keer terug.
export async function pbsCreateToken(
  id: string,
  t: { userid: string; tokenid: string; comment?: string; expire?: number; regenerate?: boolean },
): Promise<{ full_tokenid: string; value: string }> {
  return unwrap(await api.POST("/pbs/servers/{nodeId}/tokens", { params: { path: { nodeId: id } }, body: t })) as unknown as {
    full_tokenid: string;
    value: string;
  };
}
