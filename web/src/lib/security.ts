"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, unwrap } from "./api/client";
import type { components } from "./api/schema";

// Beveiliging (fase 27): bekende kwetsbaarheden per node (Debian Security
// Tracker, CISA KEV) en de eigen scopes. De types volgen internal/security.

export type Severity = "kritiek" | "hoog" | "middel" | "laag" | "info";
export const severities: Severity[] = ["kritiek", "hoog", "middel", "laag", "info"];

export type SecurityFeed = {
  name: string;
  label: string;
  url: string;
  fetched_at: string | null;
  ok_at: string | null;
  error: string;
  entries: number;
};

export type SecurityNode = {
  node_id: string;
  hostname: string;
  address: string;
  cluster_id: string | null;
  cluster_name: string;
  cluster_slug: string;
  online: boolean;
  protocol: number;
  supported: boolean;
  os: string;
  codename: string;
  kernel: string;
  dpkg: boolean;
  packages: number;
  collected_at: string | null;
  error: string;
  error_at: string | null;
  checked: boolean;
  note: string;
  counts: Partial<Record<Severity, number>>;
  kev: number;
  fixable: number;
};

export type SecurityOverview = {
  protocol: number;
  feeds: SecurityFeed[];
  stats: {
    open: number;
    accepted: number;
    counts: Partial<Record<Severity, number>>;
    kev: number;
    fixable: number;
    resolved_week: number;
    nodes_checked: number;
    nodes_total: number;
    nodes_old_agent: number;
  };
  nodes: SecurityNode[];
  scopes: number;
};

export type CVEHit = {
  cve: string;
  urgency: string;
  status: string;
  fixed_version: string;
  fixable: boolean;
  kev: boolean;
  ransomware: boolean;
  description: string;
  scope: string;
  severity: Severity;
};

export type CVEDetail = {
  package: string;
  version: string;
  release: string;
  binaries: string[];
  fix_version: string;
  fixable: number;
  no_fix: number;
  kev: number;
  cves: CVEHit[];
};

export type FindingStatus = "open" | "aanvaard" | "opgelost";

export type SecurityFinding = {
  id: string;
  kind: string;
  subject: string;
  key: string;
  node_id: string | null;
  hostname: string;
  cluster_name: string;
  cluster_slug: string;
  severity: Severity;
  title: string;
  summary: string;
  fix: string;
  detail: CVEDetail | Record<string, unknown>;
  status: FindingStatus;
  first_seen: string;
  last_seen: string;
  resolved_at: string | null;
  accepted_at: string | null;
  accepted_until: string | null;
  accepted_note: string;
  accepted_by: string;
};

export type SecurityScope = {
  id: string;
  name: string;
  description: string;
  cidrs: string[];
  exclude: string[];
  public: boolean;
  public_note: string;
  addresses: number;
  created_at: string;
  updated_at: string;
};

export type FindingQuery = {
  status?: "open" | "aanvaard" | "opgelost" | "alle" | "actueel";
  node_id?: string;
  kind?: string;
};

const key = ["security"] as const;

export function useSecurity() {
  return useQuery({
    queryKey: key,
    queryFn: async () => unwrap(await api.GET("/security")) as unknown as SecurityOverview,
    refetchInterval: 30_000,
  });
}

export function useSecurityFindings(query: FindingQuery) {
  return useQuery({
    queryKey: [...key, "findings", query],
    queryFn: async () => (unwrap(await api.GET("/security/findings", { params: { query } })) as unknown as { items: SecurityFinding[] }).items,
    refetchInterval: 30_000,
  });
}

export function useSecurityScopes() {
  return useQuery({
    queryKey: [...key, "scopes"],
    queryFn: async () =>
      unwrap(await api.GET("/security/scopes")) as unknown as {
        items: SecurityScope[];
        confirm_text: string;
      },
  });
}

function useInvalidate() {
  const qc = useQueryClient();
  return () => void qc.invalidateQueries({ queryKey: key });
}

export function useRefreshSecurity() {
  const done = useInvalidate();
  return useMutation({
    mutationFn: async (body: components["schemas"]["SecurityRefreshInput"]) => unwrap(await api.POST("/security/refresh", { body })),
    onSuccess: () => {
      // De ronde loopt op de achtergrond; even later opnieuw kijken.
      setTimeout(done, 5_000);
      setTimeout(done, 20_000);
    },
  });
}

export function useAcceptFinding() {
  const done = useInvalidate();
  return useMutation({
    mutationFn: async ({ id, note, days }: { id: string; note: string; days?: number }) =>
      unwrap(
        await api.POST("/security/findings/{findingId}/accept", {
          params: { path: { findingId: id } },
          body: { note, days },
        }),
      ) as unknown as SecurityFinding,
    onSuccess: done,
  });
}

export function useReopenFinding() {
  const done = useInvalidate();
  return useMutation({
    mutationFn: async (id: string) =>
      unwrap(
        await api.POST("/security/findings/{findingId}/reopen", {
          params: { path: { findingId: id } },
        }),
      ) as unknown as SecurityFinding,
    onSuccess: done,
  });
}

export function useSaveScope() {
  const done = useInvalidate();
  return useMutation({
    mutationFn: async ({ id, body }: { id?: string; body: components["schemas"]["SecurityScopeInput"] }) =>
      unwrap(
        id
          ? await api.PUT("/security/scopes/{scopeId}", {
              params: { path: { scopeId: id } },
              body,
            })
          : await api.POST("/security/scopes", { body }),
      ) as unknown as SecurityScope,
    onSuccess: done,
  });
}

export function useDeleteScope() {
  const done = useInvalidate();
  return useMutation({
    mutationFn: async (id: string) =>
      unwrap(
        await api.DELETE("/security/scopes/{scopeId}", {
          params: { path: { scopeId: id } },
        }),
      ),
    onSuccess: done,
  });
}

export type Tone = "slate" | "green" | "amber" | "red" | "blue";

export const severityTone: Record<Severity, Tone> = {
  kritiek: "red",
  hoog: "red",
  middel: "amber",
  laag: "blue",
  info: "slate",
};

export const urgencyLabel: Record<string, string> = {
  high: "hoog",
  medium: "middel",
  low: "laag",
  "not yet assigned": "nog niet ingeschat",
  "end-of-life": "geen ondersteuning meer",
  unimportant: "onbelangrijk",
};

export function isCVEDetail(d: SecurityFinding["detail"]): d is CVEDetail {
  return typeof d === "object" && d !== null && Array.isArray((d as CVEDetail).cves);
}

// Verharding (M2).

export type HardeningRule = { id: string; category: string; title: string; severity: Severity; fix: string; auto: boolean };
export type RuleResult = { id: string; result: "ok" | "fout" | "nvt"; value: string; expected: string };
export type LynisReport = {
  date: string;
  hardening_index: number;
  warnings: string[];
  suggestions: string[];
  tests_done: number;
  lynis_version: string;
};

export type HardeningNode = SecurityNode & {
  audit_supported: boolean;
  audit_at: string | null;
  audit_error: string;
  hardening_score: number | null;
  virtualization: string;
  results: RuleResult[];
  lynis: LynisReport | null;
};

export type HardeningOverview = { protocol: number; fix_protocol: number; rules: HardeningRule[]; nodes: HardeningNode[] };

export function useHardening() {
  return useQuery({
    queryKey: [...key, "hardening"],
    queryFn: async () => unwrap(await api.GET("/security/hardening")) as unknown as HardeningOverview,
    refetchInterval: 30_000,
  });
}

// Oplossen met één klik (M6).

export type FixOutcome = { rule: string; ok: boolean; changed?: string[]; error?: string };
export type FixStatus = "klaar" | "mislukt" | "wacht op bevestiging" | "bevestigd" | "teruggedraaid" | "verlopen";
export type FixRun = {
  id: string;
  node_id: string;
  hostname: string;
  rules: string[];
  outcomes: FixOutcome[];
  error: string;
  ssh_pending: boolean;
  rollback_at: string | null;
  confirmed_at: string | null;
  rolled_back_at: string | null;
  created_by: string;
  created_at: string;
  status: FixStatus;
};

export const fixStatusTone: Record<FixStatus, Tone> = {
  klaar: "green",
  mislukt: "red",
  "wacht op bevestiging": "amber",
  bevestigd: "green",
  teruggedraaid: "slate",
  verlopen: "slate",
};

export function useFixRuns() {
  return useQuery({
    queryKey: [...key, "fixes"],
    queryFn: async () => unwrap(await api.GET("/security/fixes")) as unknown as FixRun[],
    refetchInterval: 15_000,
  });
}

export function useApplyFixes() {
  const done = useInvalidate();
  return useMutation({
    mutationFn: async (body: components["schemas"]["SecurityFixInput"]) => unwrap(await api.POST("/security/fixes", { body })) as unknown as FixRun,
    onSuccess: done,
  });
}

export function useFinishFix() {
  const done = useInvalidate();
  return useMutation({
    mutationFn: async ({ id, keep }: { id: string; keep: boolean }) =>
      (keep
        ? unwrap(await api.POST("/security/fixes/{fixId}/confirm", { params: { path: { fixId: id } } }))
        : unwrap(await api.POST("/security/fixes/{fixId}/rollback", { params: { path: { fixId: id } } }))) as unknown as FixRun,
    onSuccess: done,
  });
}

// Tools op de nodes (M7).

export type ToolID = "clamav" | "aide" | "rkhunter" | "chkrootkit" | "lynis" | "auditd" | "trivy";
export type ToolAction = "install" | "run" | "baseline" | "remove";
export type ToolInfo = { id: ToolID; name: string; description: string; run: boolean; baseline: boolean; no_lxc: boolean; docker: boolean };
export type TrivyVuln = { id: string; pkg: string; version: string; fixed?: string; severity: string; title?: string };
export type TrivyImage = { image: string; counts: Record<string, number>; vulns: TrivyVuln[]; error?: string };
export type ToolRunState = { started?: string; ended?: string; exit?: number; items?: string[]; tail?: string; images?: TrivyImage[] };
export type ToolRow = {
  tool: ToolID;
  installed: boolean;
  version: string;
  running: string;
  state: { active?: boolean; rules?: number; last?: Partial<Record<ToolAction, ToolRunState>> };
  schedule: "uit" | "dagelijks" | "wekelijks";
  weekday: number;
  hour: number;
  next_run_at: string | null;
  updated_at: string;
};
export type ToolsNode = {
  node_id: string;
  hostname: string;
  cluster_name: string;
  online: boolean;
  protocol: number;
  virtualization: string;
  docker: boolean;
  fetched_at: string | null;
  error: string;
  tools: ToolRow[];
};
export type ToolsOverview = { protocol: number; tools: ToolInfo[]; nodes: ToolsNode[] };

export function useSecurityTools() {
  return useQuery({
    queryKey: [...key, "tools"],
    queryFn: async () => unwrap(await api.GET("/security/tools")) as unknown as ToolsOverview,
    refetchInterval: (q) => ((q.state.data as ToolsOverview | undefined)?.nodes.some((n) => n.tools.some((t) => t.running)) ? 10_000 : 60_000),
  });
}

export function useToolAction() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({ node, tool, action }: { node: string; tool: ToolID; action: ToolAction }) =>
      unwrap(await api.POST("/security/tools/{nodeId}/{tool}/{action}", { params: { path: { nodeId: node, tool, action } } })),
    onSuccess: () => void qc.invalidateQueries({ queryKey: [...key, "tools"] }),
  });
}

export function useToolSchedule() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({ node, tool, body }: { node: string; tool: ToolID; body: components["schemas"]["SecurityToolSchedule"] }) =>
      unwrap(await api.PUT("/security/tools/{nodeId}/{tool}/schedule", { params: { path: { nodeId: node, tool } }, body })),
    onSuccess: () => void qc.invalidateQueries({ queryKey: [...key, "tools"] }),
  });
}

export function scoreTone(score: number | null): Tone {
  if (score === null) return "slate";
  if (score >= 90) return "green";
  if (score >= 70) return "amber";
  return "red";
}

// Netwerkscans (M3).

export type ScanRun = {
  id: string;
  scan_id: string | null;
  scan_name: string;
  scanner_id: string | null;
  scanner_name: string;
  trigger: "schema" | "hand" | "import";
  status: "bezig" | "klaar" | "mislukt";
  error: string;
  started_at: string;
  finished_at: string | null;
  targets: string[];
  hosts_up: number;
  open_ports: number;
  findings: number;
};

export type SecurityScan = {
  id: string;
  name: string;
  scope_id: string;
  scope_name: string;
  scanner_id: string | null;
  scanner_name: string;
  profile: "snel" | "standaard" | "volledig";
  max_rate: number;
  default_passwords: boolean;
  schedule: "uit" | "dagelijks" | "wekelijks";
  weekday: number;
  hour: number;
  next_run: string | null;
  running: boolean;
  last_run: ScanRun | null;
};

export type Scanner = {
  node_id: string;
  hostname: string;
  address: string;
  online: boolean;
  protocol: number;
  ready: boolean;
  nmap: boolean;
  container: boolean;
  note: string;
};

export type ScansOverview = { protocol: number; profiles: string[]; scripts: string[]; scans: SecurityScan[]; scanners: Scanner[]; runs: ScanRun[] };

export type ScanPort = {
  proto: string;
  port: number;
  state: string;
  service?: string;
  product?: string;
  version?: string;
  extra?: string;
  tunnel?: string;
  cpe?: string[];
  scripts?: Record<string, string>;
};

export type RunPort = {
  proto: string;
  port: number;
  known: "" | "luistert" | "firewall" | "dicht" | "onbekend";
  process?: string;
  title?: string;
  hits?: { id: string; detail?: string }[];
};

export type RunHost = {
  addr: string;
  mac?: string;
  vendor?: string;
  hostname?: string;
  ports: ScanPort[];
  node_id: string | null;
  node: string;
  cluster: string;
  expected: RunPort[];
};

export type ScanRunDetail = ScanRun & {
  result: { nmap: string; args: string[]; truncated: boolean; notes: string[]; hosts: RunHost[]; probed: boolean } | null;
};

export function useScans() {
  return useQuery({
    queryKey: [...key, "scans"],
    queryFn: async () => unwrap(await api.GET("/security/scans")) as unknown as ScansOverview,
    refetchInterval: (q) => (q.state.data?.scans.some((s) => s.running) ? 5_000 : 30_000),
  });
}

export function useScanRun(id: string | null) {
  return useQuery({
    queryKey: [...key, "scan-run", id],
    enabled: !!id,
    queryFn: async () => unwrap(await api.GET("/security/scan-runs/{runId}", { params: { path: { runId: id! } } })) as unknown as ScanRunDetail,
    refetchInterval: (q) => (q.state.data?.status === "bezig" ? 5_000 : false),
  });
}

export function useSaveScan() {
  const done = useInvalidate();
  return useMutation({
    mutationFn: async ({ id, body }: { id?: string; body: components["schemas"]["SecurityScanInput"] }) =>
      unwrap(
        id ? await api.PUT("/security/scans/{scanId}", { params: { path: { scanId: id } }, body }) : await api.POST("/security/scans", { body }),
      ) as unknown as SecurityScan,
    onSuccess: done,
  });
}

export function useDeleteScan() {
  const done = useInvalidate();
  return useMutation({
    mutationFn: async (id: string) => unwrap(await api.DELETE("/security/scans/{scanId}", { params: { path: { scanId: id } } })),
    onSuccess: done,
  });
}

export function useRunScan() {
  const done = useInvalidate();
  return useMutation({
    mutationFn: async (id: string) =>
      unwrap(await api.POST("/security/scans/{scanId}/run", { params: { path: { scanId: id } } })) as unknown as { run_id: string },
    onSuccess: done,
  });
}

// Fase 43: een nmap-rapport van een eigen pentest (bv. Kali) inlezen.
export function useImportScan() {
  const done = useInvalidate();
  return useMutation({
    mutationFn: async (body: components["schemas"]["SecurityImportInput"]) =>
      unwrap(await api.POST("/security/scans/import", { body })) as unknown as ScanRun,
    onSuccess: done,
  });
}

export function useLinkScanner() {
  const done = useInvalidate();
  return useMutation({
    mutationFn: async () => unwrap(await api.POST("/security/scanner")) as unknown as { node_id: string; hostname: string; linked: boolean },
    onSuccess: () => {
      done();
      setTimeout(done, 10_000);
    },
  });
}

export const weekdays = ["zondag", "maandag", "dinsdag", "woensdag", "donderdag", "vrijdag", "zaterdag"];

export const profileLabel: Record<string, string> = {
  snel: "Snel: 100 meest gebruikte poorten",
  standaard: "Standaard: 1000 poorten, versies en TLS/SSH",
  volledig: "Volledig: alle 65 535 poorten, versies en TLS/SSH",
};

export const knownLabel: Record<string, { label: string; tone: Tone }> = {
  luistert: { label: "verwacht", tone: "green" },
  firewall: { label: "open volgens firewall", tone: "green" },
  dicht: { label: "hoort dicht", tone: "red" },
  onbekend: { label: "onbekend", tone: "amber" },
  "": { label: "geen node", tone: "slate" },
};

// Rapport per scope (M5).

export type ReportNode = {
  node_id: string;
  hostname: string;
  address: string;
  cluster_name: string;
  os: string;
  counts: Record<string, number>;
  kev: number;
  hardening_score: number | null;
};

export type SecurityReport = {
  scope: SecurityScope;
  generated_at: string;
  summary: {
    counts: Record<string, number>;
    open: number;
    accepted: number;
    kev: number;
    hardening_avg: number | null;
    hosts_up: number;
    open_ports: number;
    unknown_hosts: number;
  };
  last_run: ScanRunDetail | null;
  nodes: ReportNode[];
  findings: SecurityFinding[];
};

export function useSecurityReport(scopeId: string) {
  return useQuery({
    queryKey: [...key, "report", scopeId],
    enabled: !!scopeId,
    queryFn: async () => unwrap(await api.GET("/security/report", { params: { query: { scope_id: scopeId } } })) as unknown as SecurityReport,
  });
}
