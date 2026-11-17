"use client";

import Link from "next/link";
import { Tile, TileGrid } from "@/components/Tiles";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useSearchParams } from "next/navigation";
import { Suspense, useState, type ReactNode } from "react";
import { errText, stateLabel, stateTone, useDockerRun } from "@/components/docker/common";
import { ago, formatBytes, tableClass, tdClass, thClass } from "@/components/inventory/bits";
import { Alert, Badge, Button, Card, PageHeader, cx } from "@/components/ui";
import { api, unwrap } from "@/lib/api/client";
import { cpath, dockerConsoleHref, useDockerGet, useDockerHosts, type DockerMount, type DockerStat } from "@/lib/docker";
import { useIsAdmin } from "@/lib/inventory";

export default function Page() {
  return (
    <Suspense>
      <ContainerDetail />
    </Suspense>
  );
}

// Wat /containers/{id}/json teruggeeft (het deel dat de pagina toont).
type Inspect = {
  Id: string;
  Name: string;
  Created: string;
  Image: string;
  RestartCount: number;
  State: {
    Status: string;
    Running: boolean;
    Paused: boolean;
    Restarting: boolean;
    ExitCode: number;
    StartedAt: string;
    FinishedAt: string;
    OOMKilled?: boolean;
    Error?: string;
    Health?: { Status: string; FailingStreak: number; Log?: { Start: string; ExitCode: number; Output: string }[] } | null;
  };
  Config: { Image: string; Env?: string[] | null; Labels?: Record<string, string> | null; Cmd?: string[] | null; Entrypoint?: string[] | null; User?: string; WorkingDir?: string; Hostname?: string };
  HostConfig: {
    RestartPolicy?: { Name: string; MaximumRetryCount?: number };
    Privileged?: boolean;
    NetworkMode?: string;
    PortBindings?: Record<string, { HostIp: string; HostPort: string }[] | null> | null;
    Memory?: number;
    NanoCpus?: number;
    CapAdd?: string[] | null;
  };
  Mounts?: DockerMount[];
  NetworkSettings?: { Networks?: Record<string, { IPAddress?: string; Gateway?: string; MacAddress?: string; Aliases?: string[] | null }> };
};

type Top = { Titles: string[]; Processes: string[][] };

const restartLabel: Record<string, string> = { no: "nooit", always: "altijd", "unless-stopped": "tenzij gestopt", "on-failure": "bij een fout", "": "nooit" };

function ContainerDetail() {
  const params = useSearchParams();
  const node = params.get("node") ?? "";
  const id = params.get("id") ?? "";
  const isAdmin = useIsAdmin();
  const hosts = useDockerHosts();
  const host = hosts.data?.hosts.find((h) => h.node_id === node);
  const c = useDockerGet<Inspect>(node, cpath(id, "/json"), undefined, { enabled: !!id, refetchInterval: 10_000 });
  const running = !!c.data?.State.Running && !c.data.State.Paused;
  const top = useDockerGet<Top>(node, cpath(id, "/top"), undefined, { enabled: running, refetchInterval: 15_000 });
  const { run, modal } = useDockerRun();

  if (!node || !id) return <Alert>Geen container gekozen.</Alert>;
  if (c.isError) return <Alert>{errText(c.error)}</Alert>;
  if (!c.data) return <p className="text-sm text-slate-500">Laden…</p>;
  const d = c.data;
  const name = d.Name.replace(/^\//, "");
  const labels = d.Config.Labels ?? {};
  const project = labels["com.docker.compose.project"] ?? "";
  const managed = host?.stacks?.find((s) => s.name === project);
  const status = d.State.Health?.Status === "unhealthy" ? "(unhealthy)" : "";
  const hostname = host?.hostname ?? "";
  const act = (verb: string) => void run(node, hostname, [{ method: "POST", path: cpath(d.Id, "/" + verb) }]);
  const ports = Object.entries(d.HostConfig.PortBindings ?? {}).flatMap(([p, bs]) => (bs ?? []).map((b) => `${b.HostIp && b.HostIp !== "0.0.0.0" ? b.HostIp + ":" : ""}${b.HostPort} → ${p}`));

  return (
    <div className="space-y-6">
      <div className="text-sm">
        <Link href="/docker/containers" className="text-brand-700 hover:underline dark:text-brand-300">
          ← Containers
        </Link>
      </div>
      <PageHeader
        title={<span className="break-all">{name}</span>}
        description={
          <span className="flex flex-wrap items-center gap-2">
            <Badge tone={stateTone(d.State.Status, status)}>{status ? "unhealthy" : (stateLabel[d.State.Status] ?? d.State.Status)}</Badge>
            <span>
              op <Link href={`/nodes/${node}`} className="underline">{hostname || "node"}</Link>
              {host?.cluster && <> · {host.cluster}</>}
            </span>
            {project &&
              (managed ? (
                <Link href={`/containers?cluster=${host?.cluster_id}`}>
                  <Badge tone="blue">stack {project} (ClusterForge)</Badge>
                </Link>
              ) : (
                <span className="text-xs">compose {project}</span>
              ))}
          </span>
        }
        actions={
          isAdmin && (
            <>
              {running || d.State.Restarting ? (
                <>
                  <Button variant="secondary" onClick={() => act("restart")}>
                    Herstarten
                  </Button>
                  {!managed && (
                    <>
                      <Button variant="secondary" onClick={() => act("stop")}>
                        Stoppen
                      </Button>
                      <Button variant="secondary" onClick={() => act("pause")}>
                        Pauzeren
                      </Button>
                      <Button variant="secondary-danger" onClick={() => void run(node, hostname, [{ method: "POST", path: cpath(d.Id, "/kill") }], { what: `${name} hard stoppen (SIGKILL) op ${hostname}` })}>
                        Kill
                      </Button>
                    </>
                  )}
                </>
              ) : d.State.Paused ? (
                <Button variant="secondary" onClick={() => act("unpause")}>
                  Hervatten
                </Button>
              ) : (
                <Button onClick={() => act("start")}>Starten</Button>
              )}
              {!managed && (
                <Button
                  variant="secondary-danger"
                  onClick={() =>
                    void run(node, hostname, [{ method: "DELETE", path: cpath(d.Id), query: { force: "1" } }], {
                      what: `Container ${name} verwijderen op ${hostname}${running ? " (hij draait; hij wordt eerst gestopt)" : ""}. Volumes blijven staan.`,
                    })
                  }
                >
                  Verwijderen
                </Button>
              )}
            </>
          )
        }
      />
      {managed && (
        <Alert kind="info">
          Deze container hoort bij stack {project}, die ClusterForge beheert. Stoppen, pauzeren en verwijderen gaan via{" "}
          <Link href={`/containers?cluster=${host?.cluster_id}`} className="underline">
            Docker → Stacks
          </Link>
          ; anders zet de stack hem terug. Herstarten kan hier.
        </Alert>
      )}
      <div className="flex flex-wrap gap-2 text-sm">
        <Link href={`/logs?node=${node}&kind=docker&name=${encodeURIComponent(name)}`} className="rounded border border-slate-300 px-3 py-1 hover:border-slate-400 dark:border-slate-700">
          Logs
        </Link>
        {isAdmin && running && (
          <Link
            href={dockerConsoleHref(node, d.Id)}
            target="_blank"
            className="rounded border border-slate-300 px-3 py-1 hover:border-slate-400 dark:border-slate-700"
          >
            Console
          </Link>
        )}
      </div>

      {running && <LiveStats node={node} id={d.Id} />}

      <TileGrid>
        <Tile>
        <Card title="Overzicht">
          <Dl
            rows={[
              ["Image", <span key="i" className="font-mono text-xs break-all">{d.Config.Image}</span>],
              ["Gemaakt", ago(d.Created)],
              [running ? "Gestart" : "Gestopt", running ? ago(d.State.StartedAt) : `${ago(d.State.FinishedAt)} (exitcode ${d.State.ExitCode})`],
              ["Herstartbeleid", restartLabel[d.HostConfig.RestartPolicy?.Name ?? ""] ?? d.HostConfig.RestartPolicy?.Name],
              ["Herstarts", d.RestartCount],
              ["Commando", <span key="c" className="font-mono text-xs break-all">{[...(d.Config.Entrypoint ?? []), ...(d.Config.Cmd ?? [])].join(" ")}</span>],
              ["Gebruiker", d.Config.User || "zoals het image (meestal root)"],
              ["Limieten", `${d.HostConfig.NanoCpus ? `${d.HostConfig.NanoCpus / 1e9} CPU` : "geen CPU-limiet"}, ${d.HostConfig.Memory ? formatBytes(d.HostConfig.Memory) : "geen RAM-limiet"}`],
              ...(d.HostConfig.Privileged ? ([["Privileged", <Badge key="p" tone="red">ja: volledige toegang tot de host</Badge>]] as [string, ReactNode][]) : []),
              ...(d.State.OOMKilled ? ([["Geheugen", <Badge key="o" tone="red">gestopt door te weinig geheugen (OOM)</Badge>]] as [string, ReactNode][]) : []),
              ...(d.State.Error ? ([["Fout", d.State.Error]] as [string, ReactNode][]) : []),
            ]}
          />
        </Card>
        </Tile>
        {d.State.Health && (
          <Tile>
          <Card title={`Health: ${d.State.Health.Status}`}>
            <ul className="space-y-2 text-xs">
              {(d.State.Health.Log ?? []).slice(-5).reverse().map((l) => (
                <li key={l.Start} className="space-y-0.5">
                  <div className="flex gap-2">
                    <Badge tone={l.ExitCode === 0 ? "green" : "red"}>exit {l.ExitCode}</Badge>
                    <span className="text-slate-500">{ago(l.Start)}</span>
                  </div>
                  <pre className="overflow-x-auto whitespace-pre-wrap text-slate-600 dark:text-slate-400">{l.Output.trim()}</pre>
                </li>
              ))}
            </ul>
          </Card>
          </Tile>
        )}
        <Tile>
        <Card title="Netwerk">
          <Dl
            rows={[
              ["Poorten", ports.length ? ports.join(", ") : "geen gepubliceerde poorten"],
              ["Modus", d.HostConfig.NetworkMode ?? ""],
              ...Object.entries(d.NetworkSettings?.Networks ?? {}).map(([n, v]) => [n, v.IPAddress || "geen IP"] as [string, ReactNode]),
            ]}
          />
        </Card>
        </Tile>
        <Tile>
        <Card title="Opslag">
          {(d.Mounts ?? []).length === 0 ? (
            <p className="text-sm text-slate-500">Geen volumes of bind mounts.</p>
          ) : (
            <ul className="space-y-1 text-xs">
              {d.Mounts!.map((m) => (
                <li key={m.Destination} className="break-all">
                  <Badge tone={m.Type === "volume" ? "blue" : "slate"}>{m.Type}</Badge> <span className="font-mono">{m.Type === "volume" ? m.Name : m.Source}</span> →{" "}
                  <span className="font-mono">{m.Destination}</span>
                  {m.RW === false && " (alleen lezen)"}
                  {m.Source === "/var/run/docker.sock" && (
                    <>
                      {" "}
                      <Badge tone="red">Docker-socket: volledige macht over de host</Badge>
                    </>
                  )}
                </li>
              ))}
            </ul>
          )}
        </Card>
        </Tile>
        <Tile>
        <Card title="Omgevingsvariabelen">
          {(d.Config.Env ?? []).length === 0 ? (
            <p className="text-sm text-slate-500">Geen.</p>
          ) : (
            <Secrets env={d.Config.Env!} />
          )}
        </Card>
        </Tile>
        <Tile>
        <Card title="Labels">
          {Object.keys(labels).length === 0 ? (
            <p className="text-sm text-slate-500">Geen.</p>
          ) : (
            <ul className="space-y-0.5 font-mono text-xs break-all">
              {Object.entries(labels)
                .sort()
                .map(([k, v]) => (
                  <li key={k}>
                    <span className="text-slate-500">{k}=</span>
                    {v}
                  </li>
                ))}
            </ul>
          )}
        </Card>
        </Tile>
      {running && top.data && (
        <Tile wide>
        <Card title="Processen">
          <div className="overflow-x-auto">
            <table className={tableClass}>
              <thead>
                <tr>
                  {top.data.Titles.map((t) => (
                    <th key={t} className={thClass}>
                      {t}
                    </th>
                  ))}
                </tr>
              </thead>
              <tbody className="divide-y divide-slate-100 font-mono text-xs dark:divide-slate-800">
                {top.data.Processes.map((p, i) => (
                  <tr key={i}>
                    {p.map((v, j) => (
                      <td key={j} className={cx(tdClass, "break-all")}>
                        {v}
                      </td>
                    ))}
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </Card>
        </Tile>
      )}
      </TileGrid>
      {modal}
    </div>
  );
}

function Dl({ rows }: { rows: [string, ReactNode][] }) {
  return (
    <dl className="grid grid-cols-[minmax(7rem,auto)_1fr] gap-x-4 gap-y-1.5 text-sm">
      {rows.map(([k, v]) => (
        <div key={k} className="contents">
          <dt className="text-slate-500">{k}</dt>
          <dd className="min-w-0">{v}</dd>
        </div>
      ))}
    </dl>
  );
}

// Secrets toont de omgeving; waarden van sleutels die op een geheim lijken
// staan verborgen tot je erop klikt.
function Secrets({ env }: { env: string[] }) {
  const [show, setShow] = useState(false);
  const secret = /pass|secret|token|key|pwd|auth|credential/i;
  return (
    <div className="space-y-2">
      <ul className="space-y-0.5 font-mono text-xs break-all">
        {env.map((e) => {
          const i = e.indexOf("=");
          const k = i < 0 ? e : e.slice(0, i);
          const v = i < 0 ? "" : e.slice(i + 1);
          return (
            <li key={e}>
              <span className="text-slate-500">{k}=</span>
              {secret.test(k) && !show && v !== "•••" ? "••••••" : v}
            </li>
          );
        })}
      </ul>
      {env.some((e) => secret.test(e.split("=")[0]) && !e.endsWith("=•••")) && (
        <button type="button" className="text-xs underline" onClick={() => setShow(!show)}>
          {show ? "Geheimen verbergen" : "Geheimen tonen"}
        </button>
      )}
    </div>
  );
}

// LiveStats meet elke 3 s en tekent de laatste twee minuten.
function LiveStats({ node, id }: { node: string; id: string }) {
  const qc = useQueryClient();
  const key = ["docker", "stats", node, id];
  // De geschiedenis zit in de query zelf: elke meting komt achteraan.
  const q = useQuery({
    queryKey: key,
    queryFn: async () => {
      const s = (
        unwrap(await api.GET("/docker/nodes/{nodeId}/stats", { params: { path: { nodeId: node }, query: { containers: id } } })) as unknown as {
          stats: DockerStat[];
        }
      ).stats[0];
      const prev = qc.getQueryData<{ t: number; s: DockerStat }[]>(key) ?? [];
      return s ? [...prev.slice(-39), { t: Date.now(), s }] : prev;
    },
    refetchInterval: 3_000,
    retry: false,
  });
  const hist = q.data ?? [];
  const s = hist[hist.length - 1]?.s;
  if (!s) return null;
  // Netwerk en schijf zijn tellers; de snelheid is het verschil.
  const rate = (f: (x: DockerStat) => number) => {
    if (hist.length < 2) return null;
    const a = hist[hist.length - 2];
    const b = hist[hist.length - 1];
    const dt = (b.t - a.t) / 1000;
    return dt > 0 ? Math.max(0, (f(b.s) - f(a.s)) / dt) : null;
  };
  const rx = rate((x) => x.net_rx);
  const tx = rate((x) => x.net_tx);
  return (
    <div className="grid grid-cols-2 gap-3 lg:grid-cols-4">
      <Meter label="CPU" value={`${s.cpu.toFixed(1)}%`} points={hist.map((h) => h.s.cpu)} />
      <Meter
        label="RAM"
        value={formatBytes(s.mem_usage)}
        sub={s.mem_limit ? `van ${formatBytes(s.mem_limit)}` : undefined}
        points={hist.map((h) => h.s.mem_usage)}
      />
      <Meter label="Netwerk" value={rx === null ? "…" : `↓ ${formatBytes(rx)}/s`} sub={tx === null ? undefined : `↑ ${formatBytes(tx)}/s`} />
      <Meter label="Schijf" value={`${formatBytes(s.block_read)} gelezen`} sub={`${formatBytes(s.block_write)} geschreven · ${s.pids} processen`} />
    </div>
  );
}

function Meter({ label, value, sub, points }: { label: string; value: string; sub?: string; points?: number[] }) {
  const max = Math.max(1e-9, ...(points ?? [0]));
  const path =
    points && points.length > 1
      ? points.map((p, i) => `${(i / (points.length - 1)) * 100},${30 - (p / max) * 28}`).join(" ")
      : "";
  return (
    <div className="rounded-lg border border-slate-200 bg-white px-4 py-3 dark:border-slate-800 dark:bg-slate-900">
      <div className="text-xs text-slate-500">{label}</div>
      <div className="text-lg font-semibold tabular-nums">{value}</div>
      {sub && <div className="text-xs text-slate-500">{sub}</div>}
      {path && (
        <svg viewBox="0 0 100 30" preserveAspectRatio="none" className="mt-1 h-8 w-full text-brand-600" aria-hidden>
          <polyline points={path} fill="none" stroke="currentColor" strokeWidth="1.5" vectorEffect="non-scaling-stroke" />
        </svg>
      )}
    </div>
  );
}
