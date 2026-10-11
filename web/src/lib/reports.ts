"use client";

import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { api, unwrap } from "./api/client";
import type { components } from "./api/schema";
import { duration } from "./verify";

export type RecoveryReport = components["schemas"]["RecoveryReport"];
export type RecoveryReportCluster = components["schemas"]["RecoveryReportCluster"];
export type RecoveryReportRun = components["schemas"]["RecoveryReportRun"];
export type RecoveryReportBackup = components["schemas"]["RecoveryReportBackup"];
export type RecoveryReportMeasured = components["schemas"]["RecoveryReportMeasured"];

// useRecoveryReport leest het rapport van een maand (JJJJ-MM); leeg is deze
// maand.
export function useRecoveryReport(month: string) {
  return useQuery({
    queryKey: ["reports", "recovery", month],
    placeholderData: keepPreviousData,
    queryFn: async () => unwrap(await api.GET("/reports/recovery", { params: { query: month ? { month } : {} } })),
  });
}

export const runKinds: Record<RecoveryReportRun["kind"], string> = {
  "backup.verify": "Back-upcontrole",
  "failover.test": "Failovertest",
  "disaster.run": "Rampoefening",
  "dr.rehearsal": "DR-repetitie",
};

// monthKey is JJJJ-MM van een datum, in de tijdzone van de browser.
export function monthKey(d: Date) {
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}`;
}

// monthLabel schrijft 2026-11 als november 2026.
export function monthLabel(key: string) {
  const [y, m] = key.split("-").map(Number);
  return new Date(y, m - 1, 1).toLocaleDateString("nl-BE", { month: "long", year: "numeric" });
}

// recentMonths zijn deze maand en de elf ervoor, nieuwste eerst, plus
// oudere maanden met een bewaard rapport.
export function recentMonths(saved: string[], now = new Date()) {
  const out: string[] = [];
  for (let i = 0; i < 12; i++) out.push(monthKey(new Date(now.getFullYear(), now.getMonth() - i, 1)));
  for (const m of saved) if (!out.includes(m)) out.push(m);
  return out.sort().reverse();
}

// measuredText is de meting van een run in woorden.
export function measuredText(r: RecoveryReportRun) {
  const parts: string[] = [];
  if (r.recovery_seconds !== null) parts.push(`hersteltijd ${duration(r.recovery_seconds)}`);
  if (r.takeover_ms !== null) parts.push(`overname ${ms(r.takeover_ms)}`);
  if (r.downtime_ms !== null) parts.push(`onbereikbaar ${ms(r.downtime_ms)}`);
  if (r.rto_seconds !== null) parts.push(`RTO ${duration(r.rto_seconds)}`);
  if (r.rpo_seconds !== null) parts.push(`RPO ${duration(r.rpo_seconds)}`);
  return parts.join(", ");
}

function ms(v: number) {
  if (v < 10_000) return `${(v / 1000).toLocaleString("nl-BE", { maximumFractionDigits: 1 })} s`;
  return duration(v / 1000);
}

// goalText schrijft een doel; 0 is geen doel.
export function goalText(minutes: number) {
  return minutes ? duration(minutes * 60) : "geen doel";
}

// tally telt de runs van een soort: hoeveel er waren en hoeveel slaagden.
export function tally(runs: RecoveryReportRun[], kind: RecoveryReportRun["kind"]) {
  const of = runs.filter((r) => r.kind === kind);
  return { total: of.length, passed: of.filter((r) => r.result === "pass").length };
}
