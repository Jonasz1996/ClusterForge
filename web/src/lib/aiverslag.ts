"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ApiError, getCsrfToken } from "./api/client";
import type { Job } from "./proxmox";

// Fase 38: AI-verslag (Jonas' AI-analyse van zijn OPNsense-netwerk) via de
// agent op de machine van AI-verslag. De agent koppelt zichzelf daar als app
// "ClusterForge"; lezen gaat langs /aiverslag/{node}/api, schrijven langs
// /aiverslag/{node}/call (een taak, of meteen voor een vraag of "gezien").
// Start, App en de items zijn de antwoorden van AI-verslag zoals ze zijn.

export const AIV_PROTOCOL = 54;

export type AIVUnit = {
  name: string;
  active: string;
  sub?: string;
  result?: string;
  since?: number;
};

export type AIVInstance = {
  node_id: string;
  hostname: string;
  address?: string;
  facts: {
    version?: string;
    dir: string;
    url: string;
    reachable: boolean;
    paired: boolean;
    model?: string;
    data_used_pct?: number;
  };
  units: AIVUnit[];
  protocol: number;
  agent_version?: string;
  online: boolean;
  heartbeat_at?: string;
  why?: string;
};

export type AIVNiveau = "kritiek" | "verhoogd" | "waakzaam" | "normaal";

export type AIVNu = {
  icoon?: string;
  titel: string;
  tekst?: string;
  prio?: number;
  link?: string;
  soort?: string;
  claim?: string;
  ernst?: number;
  vertrouwen?: number;
};

type Count = Record<string, number> | null;

export type AIVStart = {
  ts?: number;
  versie?: string;
  niveau?: { niveau: AIVNiveau; redenen: string[] };
  score?: {
    score?: number | null;
    gisteren?: number | null;
    dag?: string;
    reeks?: number[];
  };
  incidenten?: {
    id: number;
    titel: string;
    prio?: number;
    ernst?: number;
    vertrouwen?: number;
    fasen?: unknown;
    laatst?: number;
  }[];
  incidenten_n?: number;
  blokkades?: {
    volledig?: number;
    mogelijk?: number;
    gedeeltelijk?: number;
    onterecht?: number;
  };
  risico?: { ip: string; naam?: string; score: number; niveau?: string }[];
  nu?: AIVNu[];
  ngfw?: {
    dns?: Count;
    firewall?: Count;
    crowdsec?: Count;
    maltrail?: Count;
    zenarmor?: Count;
    detecties?: number | null;
  };
  dreiging?: { detecties: number; per_claim?: Record<string, number> };
  feeds?: {
    aan?: number;
    ok?: number;
    fout?: number;
    nooit?: number;
    laatst?: number;
    lijst?: {
      naam: string;
      label: string;
      ok: boolean;
      oud?: boolean;
      fout?: string;
    }[];
  };
  bronnen?: Record<
    string,
    Record<
      string,
      { ok?: boolean; nooit?: boolean; fout?: string; ts?: number }
    >
  >;
  briefing?: {
    dag?: string;
    ts?: number;
    samenvatting?: string;
    punten?: unknown;
    ai?: unknown;
    fout?: string;
  } | null;
  firewall?: {
    versie?: string;
    updates?: unknown;
    herstart?: unknown;
    kwetsbaar?: number;
    audit_ts?: number;
  };
  run?: { ts?: number; taak?: number; taak_s?: number };
};

export type AIVAlarm = {
  id: number;
  ts: number;
  titel: string;
  tekst?: string;
  gezien: boolean;
};

export type AIVApp = {
  nu?: number;
  niveau?: AIVNiveau | null;
  score?: number | null;
  redenen?: string[];
  incidenten?: number | null;
  open_hoog?: number;
  meldingen?: AIVAlarm[];
  laatste_run?: string;
};

export type AIVItem = {
  cat: string;
  id: number;
  sev: "hoog" | "midden" | "laag";
  titel: string;
  uitleg: string;
  actie_nodig: boolean;
  actie: string;
  fix: boolean;
  status: string;
  ond?: string;
  ev: number;
  first?: unknown;
  last?: unknown;
  count: number;
  vervalt?: unknown;
};

export type AIVCat = {
  id: string;
  label: string;
  hoog: number;
  midden: number;
  laag: number;
  actie: number;
  err?: string;
  model?: string;
};

// AIVExt is de stand van ext56 (context uit ClusterForge) in AI-verslag.
export type AIVExt = {
  installed: boolean;
  version?: string;
  bundled: string;
  update: boolean;
  rules: number;
  stand?: {
    ts?: number;
    nodes?: number;
    vips?: number;
    regels?: number;
    weg?: number;
    onderhoud?: string[];
    wijzigingen?: number;
  };
  pushed_at?: string;
  push_error?: string;
};

export type AIVOverview = {
  instance: AIVInstance;
  ext?: AIVExt;
  start?: AIVStart;
  app?: AIVApp;
  runinfo?: {
    tijden?: Record<string, unknown>;
    dagrapport?: Record<string, unknown>;
    notify?: boolean;
  };
  state?: {
    running: boolean;
    last?: string;
    summary?: string;
    busy: string[];
    cats: AIVCat[];
    top: AIVItem[];
  };
  warnings: { level: "critical" | "warning"; text: string }[];
  errors: Record<string, string>;
};

async function call<T>(
  method: string,
  path: string,
  body?: unknown,
): Promise<T> {
  const headers: Record<string, string> = { Accept: "application/json" };
  if (body !== undefined) headers["Content-Type"] = "application/json";
  const csrf = getCsrfToken();
  if (csrf && method !== "GET") headers["X-CSRF-Token"] = csrf;
  const res = await fetch(`/api/v1${path}`, {
    method,
    headers,
    credentials: "same-origin",
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const data = await res.json().catch(() => ({}));
  if (!res.ok)
    throw new ApiError(
      res.status,
      data?.code ?? "unknown",
      data?.message ?? `Onverwachte fout (${res.status})`,
    );
  return data as T;
}

export function useAIVInstances() {
  return useQuery({
    queryKey: ["aiverslag", "instances"],
    queryFn: () =>
      call<{ items: AIVInstance[]; protocol: number }>("GET", "/aiverslag"),
    refetchInterval: 30_000,
  });
}

// AIVSummary is de stand van één AI-verslag na de laatste ronde van de
// server (elke 5 minuten).
export type AIVSummary = {
  instance: AIVInstance;
  at: string;
  warnings: { level: "critical" | "warning"; text: string }[];
  niveau?: AIVNiveau;
  redenen?: string[];
  score?: number | null;
  open_hoog: number;
  incidenten: number;
  unseen: number;
  last_run?: number;
  error?: string;
};

export function useAIVSummary() {
  return useQuery({
    queryKey: ["aiverslag", "summary"],
    queryFn: () => call<{ items: AIVSummary[] }>("GET", "/aiverslag/summary"),
    refetchInterval: 60_000,
    retry: false,
  });
}

export function useAIVOverview(id: string) {
  return useQuery({
    queryKey: ["aiverslag", id, "overview"],
    queryFn: () => call<AIVOverview>("GET", `/aiverslag/${id}/overview`),
    enabled: !!id,
    refetchInterval: 30_000,
    retry: false,
  });
}

export function useAIVGet<T>(
  id: string,
  path: string,
  params?: Record<string, string>,
  opts: { enabled?: boolean; refetch?: number | false } = {},
) {
  const q = new URLSearchParams({ path, ...(params ?? {}) }).toString();
  return useQuery({
    queryKey: ["aiverslag", id, "get", q],
    queryFn: async () =>
      (await call<{ data: T }>("GET", `/aiverslag/${id}/api?${q}`)).data,
    enabled: (opts.enabled ?? true) && !!id,
    refetchInterval: opts.refetch ?? 30_000,
    retry: false,
  });
}

export type AIVCall = { path: string; body?: unknown; confirm?: string };

// useAIVCall start een schrijfactie als taak.
export function useAIVCall(id: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (c: AIVCall) => call<Job>("POST", `/aiverslag/${id}/call`, c),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["jobs"] });
      const again = () => qc.invalidateQueries({ queryKey: ["aiverslag", id] });
      again();
      setTimeout(again, 4_000);
      setTimeout(again, 15_000);
    },
  });
}

// useAIVInstall zet ext56 in AI-verslag (of werkt ze bij), als taak.
export function useAIVInstall(id: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: () => call<Job>("POST", `/aiverslag/${id}/install`, {}),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["jobs"] });
      const again = () => qc.invalidateQueries({ queryKey: ["aiverslag", id] });
      setTimeout(again, 20_000);
      setTimeout(again, 60_000);
      setTimeout(again, 120_000);
    },
  });
}

// useAIVNow voert een korte actie meteen uit en geeft het antwoord.
export function useAIVNow<T = unknown>(id: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (c: AIVCall) =>
      (await call<{ data: T }>("POST", `/aiverslag/${id}/call`, c)).data,
    onSuccess: () => qc.invalidateQueries({ queryKey: ["aiverslag", id] }),
  });
}

export const niveauTone: Record<
  string,
  "red" | "amber" | "blue" | "green" | "slate"
> = {
  kritiek: "red",
  verhoogd: "amber",
  waakzaam: "blue",
  normaal: "green",
};

export const sevTone: Record<string, "red" | "amber" | "slate"> = {
  hoog: "red",
  midden: "amber",
  laag: "slate",
};

export const aivTime = new Intl.DateTimeFormat("nl-BE", {
  dateStyle: "short",
  timeStyle: "short",
});

export function aivDate(ts?: number | null) {
  if (!ts) return "–";
  return aivTime.format(new Date(ts > 1e12 ? ts : ts * 1000));
}
