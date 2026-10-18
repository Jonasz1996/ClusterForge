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
