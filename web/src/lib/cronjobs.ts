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
