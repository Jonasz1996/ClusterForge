"use client";

import Link from "next/link";
import { useQueries } from "@tanstack/react-query";
import { Suspense, useState } from "react";
import { errText, FilterChips, inScope, ScopeBar, useScope } from "@/components/docker/common";
import { tableClass, tdClass, thClass } from "@/components/inventory/bits";
import { Alert, Badge, PageHeader, Select, cx } from "@/components/ui";
import { dockerGet, hostReady, useDockerHosts, type DockerHost } from "@/lib/docker";

export default function Page() {
  return (
    <Suspense>
      <Events />
    </Suspense>
  );
}

// Een gebeurtenis van /events (Docker Engine API).
type DockerEvent = {
  Type: string;
  Action: string;
  Actor?: { ID: string; Attributes?: Record<string, string> };
  time: number;
  timeNano?: number;
};

type Row = { host: DockerHost; e: DockerEvent };

// Wat telt; exec_* van healthchecks zou de lijst overspoelen.
const wanted = {
  type: ["container", "image", "volume", "network"],
  event: [
    "create", "start", "restart", "stop", "die", "kill", "oom", "pause", "unpause", "destroy", "rename", "update", "health_status",
    "pull", "delete", "tag", "untag", "import", "connect", "disconnect", "mount", "unmount", "remove",
  ],
};

const periods = [
  { key: "3600", label: "Laatste uur" },
  { key: "21600", label: "Laatste 6 uur" },
  { key: "86400", label: "Laatste 24 uur" },
] as const;

const filters = [
  { key: "", label: "Alles" },
  { key: "bad", label: "Fouten" },
  { key: "container", label: "Containers" },
  { key: "image", label: "Images" },
  { key: "other", label: "Volumes en netwerken" },
] as const;
type FilterKey = (typeof filters)[number]["key"];

function isBad(e: DockerEvent) {
  const a = e.Action;
  if (a === "oom" || a === "kill") return true;
  if (a === "die") return (e.Actor?.Attributes?.exitCode ?? "0") !== "0";
  return a.startsWith("health_status") && a.includes("unhealthy");
}

function Events() {
  const hostsQ = useDockerHosts();
  const [scope, setScope] = useScope();
  const [search, setSearch] = useState("");
  const [filter, setFilter] = useState<FilterKey>("");
  const [period, setPeriod] = useState<(typeof periods)[number]["key"]>("3600");
  const need = hostsQ.data?.protocol ?? 46;
  const hosts = (hostsQ.data?.hosts ?? []).filter((h) => inScope(h, scope) && !hostReady(h, need));

  const results = useQueries({
    queries: hosts.map((h) => ({
      queryKey: ["docker", "events", h.node_id, period],
      queryFn: () => {
        const now = Math.floor(Date.now() / 1000);
        return dockerGet<DockerEvent[]>(h.node_id, "/events", { since: String(now - Number(period)), until: String(now), filters: JSON.stringify(wanted) });
      },
      refetchInterval: 30_000,
      retry: false,
    })),
  });

  const rows: Row[] = [];
  const errors: { host: DockerHost; error: string }[] = [];
  results.forEach((r, i) => {
    if (r.isError) errors.push({ host: hosts[i], error: errText(r.error) });
    for (const e of r.data ?? []) rows.push({ host: hosts[i], e });
  });
  const q = search.trim().toLowerCase();
  const test = (r: Row, f: FilterKey) =>
    f === "" ||
    (f === "bad" && isBad(r.e)) ||
    (f === "container" && r.e.Type === "container") ||
    (f === "image" && r.e.Type === "image") ||
    (f === "other" && (r.e.Type === "volume" || r.e.Type === "network"));
  const shown = rows
    .filter((r) => test(r, filter) && (!q || `${r.host.hostname} ${r.e.Type} ${r.e.Action} ${name(r.e)} ${r.e.Actor?.Attributes?.image ?? ""}`.toLowerCase().includes(q)))
    .sort((a, b) => (b.e.timeNano ?? b.e.time * 1e9) - (a.e.timeNano ?? a.e.time * 1e9))
    .slice(0, 1000);
  const loading = results.some((r) => r.isLoading);

  return (
    <div className="space-y-5">
      <PageHeader
        title="Gebeurtenissen"
        description="Wat Docker op elke node deed: containers die starten, stoppen of crashen, images die gehaald worden, volumes en netwerken. Rechtstreeks uit Docker, niet bewaard in ClusterForge; Docker zelf houdt ze een beperkte tijd bij."
      />
      <ScopeBar hosts={hostsQ.data?.hosts ?? []} scope={scope} setScope={setScope} search={search} setSearch={setSearch}>
        <Select aria-label="Periode" value={period} onChange={(e) => setPeriod(e.target.value as typeof period)} className="w-auto">
          {periods.map((p) => (
            <option key={p.key} value={p.key}>
              {p.label}
            </option>
          ))}
        </Select>
        <FilterChips options={filters.map((f) => ({ ...f, count: rows.filter((r) => test(r, f.key)).length }))} value={filter} onChange={setFilter} />
      </ScopeBar>
      {errors.map((e) => (
        <Alert key={e.host.node_id}>
          {e.host.hostname}: {e.error}
        </Alert>
      ))}
      {loading && <p className="text-sm text-slate-500">Laden…</p>}
      {!loading && shown.length === 0 && <p className="text-sm text-slate-500">Geen gebeurtenissen in deze periode.</p>}
      {shown.length > 0 && (
        <div className="overflow-x-auto rounded-lg border border-slate-200 bg-white dark:border-slate-800 dark:bg-slate-900">
          <table className={tableClass}>
            <thead>
              <tr>
                <th className={thClass}>Tijd</th>
                <th className={thClass}>Node</th>
                <th className={thClass}>Wat</th>
                <th className={thClass}>Object</th>
                <th className={cx(thClass, "hidden md:table-cell")}>Details</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
              {shown.map((r, i) => (
                <tr key={`${r.host.node_id}-${r.e.timeNano ?? r.e.time}-${i}`}>
                  <td className={cx(tdClass, "text-xs whitespace-nowrap tabular-nums")}>{new Date(r.e.time * 1000).toLocaleString("nl-BE")}</td>
                  <td className={cx(tdClass, "text-xs")}>{r.host.hostname}</td>
                  <td className={cx(tdClass, "text-xs whitespace-nowrap")}>
                    <Badge tone={isBad(r.e) ? "red" : r.e.Action === "start" ? "green" : "slate"}>
                      {r.e.Type} {actionText(r.e.Action)}
                    </Badge>
                  </td>
                  <td className={cx(tdClass, "font-mono text-xs break-all")}>
                    {r.e.Type === "container" && r.e.Actor?.ID && r.e.Action !== "destroy" ? (
                      <Link className="text-brand-600 hover:underline" href={`/docker/container?node=${r.host.node_id}&id=${r.e.Actor.ID}`}>
                        {name(r.e)}
                      </Link>
                    ) : (
                      name(r.e)
                    )}
                  </td>
                  <td className={cx(tdClass, "hidden text-xs text-slate-500 md:table-cell")}>{details(r.e)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}

function name(e: DockerEvent) {
  const a = e.Actor?.Attributes ?? {};
  return a.name ?? a.container ?? e.Actor?.ID?.slice(0, 12) ?? "";
}

function actionText(a: string) {
  if (a.startsWith("health_status")) return a.replace("health_status: ", "gezondheid: ");
  return a;
}

function details(e: DockerEvent) {
  const a = e.Actor?.Attributes ?? {};
  const out: string[] = [];
  if (e.Type === "container" && a.image) out.push(a.image);
  if (a.exitCode) out.push(`exitcode ${a.exitCode}`);
  if (a.signal) out.push(`signaal ${a.signal}`);
  if (e.Type === "network" && a.container) out.push(`container ${a.container.slice(0, 12)}`);
  if (a["com.docker.compose.project"]) out.push(`stack ${a["com.docker.compose.project"]}`);
  return out.join(" · ");
}
