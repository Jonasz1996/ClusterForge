"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, unwrap } from "./api/client";
import type { components } from "./api/schema";

export type CronGroup = components["schemas"]["CronGroup"];
export type CronJob = components["schemas"]["CronJob"];
export type CronJobNode = components["schemas"]["CronJobNode"];
export type CronNodeRead = components["schemas"]["CronNodeRead"];
export type CronWarning = components["schemas"]["CronWarning"];

type Tone = "slate" | "green" | "amber" | "red" | "blue";

export const resultInfo: Record<CronJobNode["last_result"], { label: string; tone: Tone }> = {
  "": { label: "Nog niet gedraaid", tone: "slate" },
  ok: { label: "Gelukt", tone: "green" },
  failed: { label: "Mislukt", tone: "red" },
  running: { label: "Loopt", tone: "blue" },
  started: { label: "Gestart", tone: "slate" },
};

export const sourceLabel: Record<CronJob["source"], string> = {
  crontab: "/etc/crontab",
  "cron.d": "cron.d",
  user: "crontab",
  periodic: "run-parts",
  timer: "systemd-timer",
};

// ClusterForge leest de cronjobs elke vijf minuten; het scherm kijkt elke
// minuut of er iets nieuws is.
export function useCronjobs() {
  return useQuery({
    queryKey: ["cronjobs"],
    queryFn: async () => unwrap(await api.GET("/cronjobs")).items,
    refetchInterval: 60_000,
  });
}

// useRefreshCronjobs laat de server alle nodes meteen opnieuw lezen en
// vraagt de lijst even later opnieuw op.
export function useRefreshCronjobs() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async () => {
      const res = await api.POST("/cronjobs/refresh");
      if (res.error) throw new Error("Opnieuw lezen mislukt");
    },
    onSuccess: () => {
      setTimeout(() => void qc.invalidateQueries({ queryKey: ["cronjobs"] }), 3_000);
    },
  });
}

// missingOn zegt op welke nodes van de groep een job ontbreekt.
export function missingOn(job: CronJob, group: CronGroup) {
  const on = new Set(job.nodes.map((n) => n.node_id));
  return group.nodes.filter((n) => !on.has(n.node_id)).map((n) => n.hostname);
}

// worstResult is de uitkomst die het meest aandacht vraagt over de nodes.
export function worstResult(job: CronJob): CronJobNode["last_result"] {
  const order: CronJobNode["last_result"][] = ["failed", "running", "ok", "started", ""];
  for (const r of order) if (job.nodes.some((n) => n.last_result === r)) return r;
  return "";
}

// useCronLogging laat cron op een node het einde en de exitcode van elke
// job loggen, of zet dat terug.
export function useCronLogging() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({ nodeId, enabled }: { nodeId: string; enabled: boolean }) =>
      unwrap(await api.POST("/nodes/{nodeId}/cron-logging", { params: { path: { nodeId } }, body: { enabled } })).output,
    onSuccess: () => void qc.invalidateQueries({ queryKey: ["cronjobs"] }),
  });
}

export const warningTone: Record<CronWarning["level"], "red" | "amber" | "blue"> = {
  danger: "red",
  warning: "amber",
  info: "blue",
};

export type ManagedCronJob = components["schemas"]["ManagedCronJob"];
export type ManagedCronJobInput = components["schemas"]["ManagedCronJobInput"];
export type CronRunSummary = components["schemas"]["CronRunSummary"];

export const runResultInfo: Record<CronRunSummary["result"], { label: string; tone: Tone }> = {
  running: { label: "Loopt", tone: "blue" },
  ok: { label: "Gelukt", tone: "green" },
  failed: { label: "Mislukt", tone: "red" },
  skipped: { label: "Overgeslagen", tone: "amber" },
  timeout: { label: "Tijdslimiet", tone: "red" },
  stopped: { label: "Gestopt", tone: "amber" },
};

export const runReasonLabel: Record<CronRunSummary["reason"], string> = {
  schedule: "volgens schema",
  manual: "met de hand",
  retry: "herhaling",
};

export const runOnLabel: Record<ManagedCronJob["run_on"], string> = {
  vip: "Op de node met het VIP",
  node: "Op één node",
  all: "Op alle nodes",
};

const managedKey = ["cron-jobs"] as const;

export function useManagedCronJobs() {
  return useQuery({
    queryKey: managedKey,
    queryFn: async () => unwrap(await api.GET("/cron-jobs")).items,
    refetchInterval: 15_000,
  });
}

// useManagedCronJob ververst vaker terwijl er een run loopt.
export function useManagedCronJob(id: string) {
  return useQuery({
    queryKey: [...managedKey, id],
    queryFn: async () => unwrap(await api.GET("/cron-jobs/{jobId}", { params: { path: { jobId: id } } })),
    enabled: id !== "",
    refetchInterval: (q) => (q.state.data?.runs.some((r) => r.result === "running") ? 2_000 : 15_000),
  });
}

export function useCronRun(id: string | null) {
  return useQuery({
    queryKey: ["cron-run", id],
    queryFn: async () => unwrap(await api.GET("/cron-runs/{runId}", { params: { path: { runId: id ?? "" } } })),
    enabled: !!id,
    refetchInterval: (q) => (q.state.data?.result === "running" ? 2_000 : false),
  });
}

export function useCronSchedule(schedule: string) {
  return useQuery({
    queryKey: ["cron-schedule", schedule],
    queryFn: async () => unwrap(await api.GET("/cron-jobs/schedule", { params: { query: { schedule } } })),
    enabled: schedule.trim() !== "",
    retry: false,
  });
}

export function useSaveCronJob() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({ id, clusterId, body }: { id?: string; clusterId: string; body: ManagedCronJobInput }) =>
      id
        ? unwrap(await api.PUT("/cron-jobs/{jobId}", { params: { path: { jobId: id } }, body }))
        : unwrap(await api.POST("/cron-jobs", { body: { ...body, cluster_id: clusterId } })),
    onSuccess: () => void qc.invalidateQueries({ queryKey: managedKey }),
  });
}

export function useDeleteCronJob() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({ id, confirm }: { id: string; confirm?: string }) =>
      unwrap(await api.DELETE("/cron-jobs/{jobId}", { params: { path: { jobId: id }, query: { confirm } } })),
    onSuccess: () => void qc.invalidateQueries({ queryKey: managedKey }),
  });
}

export function useRunCronJob() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({ id, confirm }: { id: string; confirm?: string }) =>
      unwrap(await api.POST("/cron-jobs/{jobId}/run", { params: { path: { jobId: id } }, body: { confirm } })).nodes,
    onSuccess: () => {
      setTimeout(() => void qc.invalidateQueries({ queryKey: managedKey }), 1_000);
    },
  });
}

// jobInputOf maakt van een job het formulier.
export function jobInputOf(j: ManagedCronJob): ManagedCronJobInput {
  return {
    name: j.name,
    schedule: j.schedule,
    run_on: j.run_on,
    node_id: j.node_id,
    user: j.user,
    command: j.command,
    timeout_seconds: j.timeout_seconds,
    enabled: j.enabled,
    retries: j.retries,
    alert: j.alert,
    kind: j.kind,
    sync: j.sync
      ? {
          source: j.sync.source,
          target: j.sync.target,
          to_cluster_id: j.sync.to_cluster_id,
          delete: j.sync.delete,
          exclude: j.sync.exclude,
          bwlimit_mb: j.sync.bwlimit_mb,
        }
      : undefined,
  };
}

// useStopCronRun stopt een lopende run op zijn node.
export function useStopCronRun() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({ id, confirm }: { id: string; confirm?: string }) =>
      unwrap(await api.POST("/cron-runs/{runId}/stop", { params: { path: { runId: id } }, body: { confirm } })),
    onSuccess: (_, v) => {
      setTimeout(() => {
        void qc.invalidateQueries({ queryKey: managedKey });
        void qc.invalidateQueries({ queryKey: ["cron-run", v.id] });
      }, 1_000);
    },
  });
}

// toggleable: een regel in een crontab, die ClusterForge in commentaar kan
// zetten en terug.
export function toggleable(j: CronJob) {
  return !j.clusterforge && (j.source === "crontab" || j.source === "cron.d" || j.source === "user");
}

// useToggleCron zet een gevonden job uit of weer aan op de gegeven nodes,
// node na node.
export function useToggleCron() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({ key, nodes, enabled, confirm }: { key: string; nodes: { node_id: string; hostname: string }[]; enabled: boolean; confirm?: string }) => {
      const out: string[] = [];
      for (const n of nodes) {
        const res = await api.POST("/nodes/{nodeId}/crons/{key}/toggle", {
          params: { path: { nodeId: n.node_id, key } },
          body: { enabled, confirm },
        });
        try {
          out.push(...unwrap(res).output.map((o) => `${n.hostname}: ${o}`));
        } catch (e) {
          if (e instanceof Error) e.message = `${n.hostname}: ${e.message}`;
          throw e;
        }
      }
      return out;
    },
    onSettled: () => void qc.invalidateQueries({ queryKey: ["cronjobs"] }),
  });
}

// adoptInput maakt van een gevonden job een beheerde: hetzelfde schema,
// dezelfde gebruiker en hetzelfde commando. Een job op alle nodes van het
// cluster draait voortaan op de node met het VIP, of zonder VIP op alle
// nodes; een job op één node op die node.
export function adoptInput(j: CronJob, g: CronGroup, hasVip: boolean): ManagedCronJobInput {
  const all = j.nodes.length > 1 && j.nodes.length >= g.nodes.length;
  const run_on: ManagedCronJobInput["run_on"] = j.nodes.length === 1 && g.nodes.length > 1 ? "node" : all && hasVip ? "vip" : "all";
  const sync = rsyncOf(j.command);
  if (sync) {
    // Een sync draait nooit op alle nodes tegelijk.
    const on = run_on === "all" ? (hasVip ? "vip" : "node") : run_on;
    return {
      name: adoptName(j.command),
      schedule: j.schedule,
      run_on: on,
      node_id: on === "node" ? j.nodes[0].node_id : null,
      user: "root",
      command: "",
      timeout_seconds: 3600,
      enabled: true,
      retries: 0,
      alert: true,
      kind: "sync",
      sync,
    };
  }
  return {
    name: adoptName(j.command),
    schedule: j.schedule,
    run_on,
    node_id: run_on === "node" ? j.nodes[0].node_id : null,
    user: j.user || "root",
    command: j.command,
    timeout_seconds: 3600,
    enabled: true,
    retries: 0,
    alert: true,
  };
}

// adoptName is een naam uit het commando: het eerste programma dat geen
// cd, sudo of shell is, zoals backup uit /usr/local/bin/backup.sh.
export function adoptName(command: string) {
  const skip = new Set(["cd", "sudo", "nice", "ionice", "flock", "timeout", "env", "sh", "bash", "php", "python3", "perl", "&&", ";"]);
  const words = command.split(/\s+/).filter((w) => w !== "" && !w.includes("="));
  let prev = "";
  for (const w of words) {
    const prog = w.split("/").pop() ?? "";
    if (w.startsWith("-") || skip.has(prog) || prev === "cd" || prev === "-u") {
      prev = w;
      continue;
    }
    const base = w.replace(/^["']|["']$/g, "").split("/").pop() ?? "";
    const name = base.replace(/\.(sh|py|php|pl)$/, "").toLowerCase().replace(/[^a-z0-9-]+/g, "-").replace(/^-+|-+$/g, "");
    if (name !== "") return name.slice(0, 50);
    prev = w;
  }
  return "overgenomen";
}

export type CronSyncPlan = components["schemas"]["CronSyncPlan"];
export type CronSyncResult = components["schemas"]["CronSyncResult"];
export type ManagedCronSyncInput = components["schemas"]["ManagedCronSyncInput"];

export const syncResultInfo: Record<CronSyncResult["result"], { label: string; tone: Tone }> = {
  ok: { label: "Gelukt", tone: "green" },
  failed: { label: "Mislukt", tone: "red" },
  refused: { label: "Geweigerd", tone: "amber" },
};

// useCronSyncPlan vraagt bij elke node van een sync na of hij klaar is.
export function useCronSyncPlan(id: string, enabled: boolean) {
  return useQuery({
    queryKey: [...managedKey, id, "plan"],
    queryFn: async () => unwrap(await api.GET("/cron-jobs/{jobId}/plan", { params: { path: { jobId: id } } })),
    enabled: enabled && id !== "",
    refetchInterval: 60_000,
  });
}

// useDryRunCronSync draait rsync --dry-run naar één doel.
export function useDryRunCronSync() {
  return useMutation({
    mutationFn: async ({ id, target }: { id: string; target: string }) =>
      unwrap(await api.POST("/cron-jobs/{jobId}/dry-run", { params: { path: { jobId: id } }, body: { target } })),
  });
}

// rsyncOf herkent een rsync van een map naar een map op een andere node,
// zoals rsync -a --delete /srv/data/ root@nc2:/srv/data/. Welke nodes het
// doel zijn, zegt het cluster; dat kiest Jonas in het formulier.
export function rsyncOf(command: string): ManagedCronSyncInput | null {
  const words = command.trim().split(/\s+/);
  const i = words.findIndex((w) => (w.split("/").pop() ?? "") === "rsync");
  if (i < 0 || /[;&|`$]/.test(command)) return null;
  const args = words.slice(i + 1);
  const paths: string[] = [];
  const exclude: string[] = [];
  let bw = 0;
  const unquote = (v: string) => v.replace(/^["']|["']$/g, "");
  for (let k = 0; k < args.length; k++) {
    const a = args[k];
    if (a.startsWith("--exclude=")) exclude.push(unquote(a.slice(10)));
    else if (a === "--exclude" && k + 1 < args.length) exclude.push(unquote(args[++k]));
    else if (a.startsWith("--bwlimit=")) bw = Math.max(1, Math.round(parseInt(a.slice(10), 10) / 1024) || 0);
    else if (a === "-e" || a === "--rsh") k++;
    else if (!a.startsWith("-")) paths.push(a);
  }
  if (paths.length !== 2) return null;
  const [src, dst] = paths;
  const remote = dst.match(/^(?:[^@\s]+@)?[^:\s/]+:(\/.*)$/);
  if (!src.startsWith("/") || !remote) return null;
  const slash = (p: string) => (p.endsWith("/") ? p : p + "/");
  // Zonder / achter de bron zet rsync de map zelf in het doel.
  const base = src.endsWith("/") ? "" : (src.split("/").pop() ?? "");
  return {
    source: slash(src),
    target: slash(slash(remote[1]) + base),
    to_cluster_id: null,
    delete: args.some((a) => a.startsWith("--delete")),
    exclude,
    bwlimit_mb: bw,
  };
}
