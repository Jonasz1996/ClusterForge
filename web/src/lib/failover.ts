"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, unwrap } from "./api/client";
import type { components } from "./api/schema";

export type FailoverTest = components["schemas"]["FailoverTest"];
export type FailoverTestInput = components["schemas"]["FailoverTestInput"];
export type FailoverOptions = components["schemas"]["FailoverOptions"];
export type FailoverProbe = components["schemas"]["FailoverProbe"];
export type FailoverScenario = components["schemas"]["FailoverScenario"];
export type TestRun = components["schemas"]["TestRun"];
export type TestRunCheck = components["schemas"]["TestRunCheck"];
export type TestRunEvent = components["schemas"]["TestRunEvent"];
export type TestRunResult = components["schemas"]["TestRunResult"];

type Tone = "slate" | "green" | "amber" | "red" | "blue";

export const runResults: Record<TestRunResult, { label: string; tone: Tone }> = {
  pass: { label: "PASS", tone: "green" },
  warning: { label: "WAARSCHUWING", tone: "amber" },
  fail: { label: "FAIL", tone: "red" },
  error: { label: "ERROR", tone: "red" },
  skipped: { label: "Overgeslagen", tone: "slate" },
  canceled: { label: "Afgebroken", tone: "slate" },
};

// De tests horen bij het cluster, zodat de live stream ze met de rest van
// het cluster opnieuw laadt.
const keys = {
  tests: (clusterId: string) => ["clusters", clusterId, "failover"] as const,
  runs: (testId: string) => ["test-runs", "test", testId] as const,
  run: (id: string) => ["test-runs", id] as const,
};

export function useFailoverTests(clusterId: string) {
  return useQuery({
    queryKey: keys.tests(clusterId),
    queryFn: async () =>
      unwrap(await api.GET("/clusters/{clusterId}/failover-tests", { params: { path: { clusterId } } })),
    // Zolang een test loopt, verandert het laatste resultaat snel.
    refetchInterval: (q) => (q.state.data?.items.some((t) => t.last_run && !t.last_run.result) ? 2_000 : 60_000),
  });
}

export function useFailoverRuns(testId: string, enabled = true) {
  return useQuery({
    queryKey: keys.runs(testId),
    queryFn: async () =>
      unwrap(await api.GET("/failover-tests/{testId}/runs", { params: { path: { testId } } })).items,
    enabled,
  });
}

// useTestRun ververst elke 1,5 s zolang de run nog geen resultaat heeft.
export function useTestRun(id: string) {
  return useQuery({
    queryKey: keys.run(id),
    queryFn: async () => unwrap(await api.GET("/test-runs/{runId}", { params: { path: { runId: id } } })),
    enabled: id !== "",
    refetchInterval: (q) => (q.state.data && (!q.state.data.result || q.state.data.job_status === "running") ? 1_500 : false),
  });
}

function useRefresh() {
  const qc = useQueryClient();
  return () => {
    void qc.invalidateQueries({ queryKey: ["clusters"] });
    void qc.invalidateQueries({ queryKey: ["test-runs"] });
    void qc.invalidateQueries({ queryKey: ["jobs"] });
  };
}

export function useCreateFailoverTest(clusterId: string) {
  const refresh = useRefresh();
  return useMutation({
    mutationFn: async (body: FailoverTestInput) =>
      unwrap(await api.POST("/clusters/{clusterId}/failover-tests", { params: { path: { clusterId } }, body })),
    onSuccess: refresh,
  });
}

export function useUpdateFailoverTest() {
  const refresh = useRefresh();
  return useMutation({
    mutationFn: async ({ id, body }: { id: string; body: FailoverTestInput }) =>
      unwrap(await api.PATCH("/failover-tests/{testId}", { params: { path: { testId: id } }, body })),
    onSuccess: refresh,
  });
}

export function useDeleteFailoverTest() {
  const refresh = useRefresh();
  return useMutation({
    mutationFn: async (id: string) => unwrap(await api.DELETE("/failover-tests/{testId}", { params: { path: { testId: id } } })),
    onSuccess: refresh,
  });
}

// useStartFailoverTest geeft de nieuwe run terug; een mislukte voorcontrole
// komt als ApiError met code precheck_failed en de controles.
export function useStartFailoverTest() {
  const refresh = useRefresh();
  return useMutation({
    mutationFn: async (testId: string) =>
      unwrap(await api.POST("/failover-tests/{testId}/runs", { params: { path: { testId } } })),
    onSuccess: refresh,
  });
}

export function useRestoreTestRun() {
  const refresh = useRefresh();
  return useMutation({
    mutationFn: async (runId: string) =>
      unwrap(await api.POST("/test-runs/{runId}/restore", { params: { path: { runId } } })),
    onSuccess: refresh,
  });
}

// unitOf is wat de test stopt: keepalived of de gekozen dienst.
export function unitOf(t: Pick<FailoverTest, "scenario" | "service">) {
  return t.scenario === "keepalived_stop" ? "keepalived" : t.service;
}

// windowSeconds is hoe lang de meting hoogstens duurt; zo lang kan het VIP
// in het slechtste geval onbereikbaar zijn. Hetzelfde als de server.
export function windowSeconds(expect: number) {
  return Math.min(2 * expect + 10, 120);
}

export function probeText(p: FailoverProbe) {
  if (p.tcp) return `TCP-poort ${p.tcp.port}`;
  if (p.http) return `HTTP ${p.http.path} geeft ${p.http.expect}`;
  return "";
}

// list maakt van ["a", "b", "c"] "a, b en c".
export function list(items: string[]) {
  if (items.length <= 1) return items.join("");
  return `${items.slice(0, -1).join(", ")} en ${items[items.length - 1]}`;
}

// seconds toont milliseconden zoals de server: "3,4 s", "20 s" of "3 min".
export function seconds(ms: number) {
  const tenths = Math.round(ms / 100);
  if (tenths % 10 === 0 && tenths < 1200) return `${tenths / 10} s`;
  if (tenths < 600) return `${(tenths / 10).toFixed(1).replace(".", ",")} s`;
  if (tenths < 1200) return `${Math.round(tenths / 10)} s`;
  return `${Math.round(ms / 60_000)} min`;
}

// confirmText is de zin in het bevestigingsvenster, met de VIP's die de
// eigenaar nu heeft.
export function confirmText(t: FailoverTest, options: FailoverOptions) {
  const unit = unitOf(t);
  const owner = t.vip_owner;
  const owned = owner ? options.vips.filter((v) => v.owner_hostname === owner).map((v) => v.address) : [t.vip_address];
  const where = owner ? `op ${owner}, nu eigenaar van ${list(owned.length ? owned : [t.vip_address])}` : `op de eigenaar van ${t.vip_address}`;
  return (
    `${unit} wordt gestopt ${where}. ` +
    `Verwacht: een andere node neemt binnen ${t.max_takeover_seconds} s over` +
    (t.expect_failback ? ` en daarna gaat het VIP terug naar ${owner ?? "de oorspronkelijke node"}. ` : ". ") +
    `Lukt dat niet, dan is ${t.vip_address} hoogstens ${windowSeconds(t.max_takeover_seconds)} s onbereikbaar; ` +
    `daarna zet ClusterForge ${unit} weer aan.`
  );
}
