"use client";

import { Suspense, useState, type ReactNode } from "react";
import { errText, inScope, NodeSections, rowsFrom, ScopeBar, useDockerAll, useDockerRun, useScope } from "@/components/docker/common";
import { formatBytes } from "@/components/inventory/bits";
import { Alert, Button, PageHeader } from "@/components/ui";
import { composeProject, containerName, cpath, shortId, useDockerHosts, type ContainerSummary, type DockerCall, type DockerHost, type SystemDF } from "@/lib/docker";
import { useIsAdmin } from "@/lib/inventory";

export default function Page() {
  return (
    <Suspense>
      <Cleanup />
    </Suspense>
  );
}

type Item = { label: string; size?: number };
type Part = { key: string; title: string; hint: string; items: Item[]; size: number; calls: DockerCall[]; what: string };
type Row = { host: DockerHost; parts: Part[] };

// parts zegt per soort wat weg kan, met de lijst vooraf. Gestopte containers
// van stacks die ClusterForge beheert, blijven staan: die horen bij Stacks.
function partsOf(host: DockerHost, df: SystemDF, containers: ContainerSummary[]): Part[] {
  const managed = new Set((host.stacks ?? []).map((s) => s.name));
  const sizeRw = new Map((df.Containers ?? []).map((c) => [c.Id, c.SizeRw ?? 0]));
  const stopped = containers.filter((c) => (c.State === "exited" || c.State === "created" || c.State === "dead") && !managed.has(composeProject(c)));
  const usedImages = new Set(containers.map((c) => c.ImageID));
  const images = (df.Images ?? []).filter((im) => !usedImages.has(im.Id));
  const volumes = (df.Volumes ?? []).filter((v) => v.UsageData && v.UsageData.RefCount === 0);
  const cache = (df.BuildCache ?? []).filter((b) => !b.InUse);
  const sum = (xs: (number | undefined)[]) => xs.reduce<number>((a, b) => a + Math.max(0, b ?? 0), 0);
  return [
    {
      key: "containers",
      title: "Gestopte containers",
      hint: "Niet van een beheerde stack. Hun logs en schrijflaag gaan mee weg.",
      items: stopped.map((c) => ({ label: `${containerName(c)} (${c.Status})`, size: sizeRw.get(c.Id) })),
      size: sum(stopped.map((c) => sizeRw.get(c.Id))),
      calls: stopped.map((c) => ({ method: "DELETE", path: cpath(c.Id) })),
      what: `${stopped.length} gestopte container(s) van ${host.hostname} verwijderen.`,
    },
    {
      key: "images",
      title: "Images zonder container",
      hint: "Ook images met een tag; ze kunnen later opnieuw gehaald worden.",
      items: images.map((im) => ({ label: im.RepoTags?.find((t) => t !== "<none>:<none>") ?? `<geen tag> ${shortId(im.Id)}`, size: im.Size })),
      size: sum(images.map((im) => im.Size)),
      calls: [{ method: "POST", path: "/images/prune", query: { filters: JSON.stringify({ dangling: ["false"] }) } }],
      what: `Alle images die geen container gebruikt van ${host.hostname} verwijderen.`,
    },
    {
      key: "volumes",
      title: "Ongebruikte volumes",
      hint: "De gegevens erin zijn daarna voorgoed weg.",
      items: volumes.map((v) => ({ label: v.Name, size: v.UsageData?.Size })),
      size: sum(volumes.map((v) => v.UsageData?.Size)),
      calls: [{ method: "POST", path: "/volumes/prune", query: { filters: JSON.stringify({ all: ["true"] }) } }],
      what: `Alle volumes die geen container gebruikt van ${host.hostname} verwijderen, met hun gegevens.`,
    },
    {
      key: "build",
      title: "Buildcache",
      hint: "Alleen wat nu niet in gebruik is.",
      items: cache.length ? [{ label: `${cache.length} stuk(ken) cache` }] : [],
      size: sum(cache.map((b) => b.Size)),
      calls: [{ method: "POST", path: "/build/prune" }],
      what: `De buildcache van ${host.hostname} leegmaken.`,
    },
  ];
}

function Cleanup() {
  const isAdmin = useIsAdmin();
  const hostsQ = useDockerHosts();
  const dfQ = useDockerAll<SystemDF>("/system/df", undefined, 60_000);
  const containersQ = useDockerAll<ContainerSummary[]>("/containers/json", { all: "1" }, 30_000);
  const [scope, setScope] = useScope();
  const [search, setSearch] = useState("");
  const { run, modal } = useDockerRun();
  const need = hostsQ.data?.protocol ?? 46;
  const hosts = (hostsQ.data?.hosts ?? []).filter((h) => inScope(h, scope) && (!search.trim() || h.hostname.toLowerCase().includes(search.trim().toLowerCase())));
  const containersBy = new Map((containersQ.data ?? []).map((r) => [r.node_id, r.data ?? []]));
  const { rows, errors } = rowsFrom<SystemDF, Row>(dfQ.data, hosts, (h, df) => [{ host: h, parts: partsOf(h, df, containersBy.get(h.node_id) ?? []) }]);
  const total = rows.reduce((a, r) => a + r.parts.reduce((b, p) => b + p.size, 0), 0);

  return (
    <div className="space-y-5">
      <PageHeader
        title="Opruimen"
        description="Wat er op elke node weg kan, met de lijst vooraf. Elke stap vraagt een bevestiging en draait als taak; wat een beheerde stack nodig heeft, blijft staan."
      />
      <ScopeBar hosts={hostsQ.data?.hosts ?? []} scope={scope} setScope={setScope} search={search} setSearch={setSearch}>
        {rows.length > 0 && <span className="text-sm text-slate-600 dark:text-slate-400">Samen vrij te maken: ongeveer {formatBytes(total)}</span>}
      </ScopeBar>
      {(dfQ.isError || containersQ.isError) && <Alert>{errText(dfQ.error ?? containersQ.error)}</Alert>}
      {dfQ.isLoading && <p className="text-sm text-slate-500">Laden…</p>}
      <NodeSections hosts={hosts} rows={rows} errors={errors} need={need} filtered={false}>
        {(h, rs) => (
          <div className="grid gap-3 p-3 md:grid-cols-2">
            {(rs[0]?.parts ?? []).map((p) => (
              <PartCard key={p.key} part={p}>
                {isAdmin && p.items.length > 0 && (
                  <Button variant="secondary-danger" onClick={() => void run(h.node_id, h.hostname, p.calls, { title: `${p.title} opruimen`, what: p.what })}>
                    Opruimen
                  </Button>
                )}
              </PartCard>
            ))}
          </div>
        )}
      </NodeSections>
      {modal}
    </div>
  );
}

function PartCard({ part, children }: { part: Part; children: ReactNode }) {
  const [open, setOpen] = useState(false);
  const shown = open ? part.items : part.items.slice(0, 5);
  return (
    <div className="rounded-md border border-slate-200 p-3 dark:border-slate-800">
      <div className="flex items-start gap-2">
        <div className="min-w-0 flex-1">
          <div className="text-sm font-medium">
            {part.title}{" "}
            <span className="font-normal text-slate-500 tabular-nums">
              {part.items.length ? `${part.key === "build" ? "" : part.items.length + " · "}${formatBytes(part.size)}` : "niets"}
            </span>
          </div>
          <p className="text-xs text-slate-500">{part.hint}</p>
        </div>
        {children}
      </div>
      {part.items.length > 0 && part.key !== "build" && (
        <ul className="mt-2 space-y-0.5 font-mono text-xs break-all">
          {shown.map((it) => (
            <li key={it.label} className="flex gap-2">
              <span className="min-w-0 flex-1">{it.label}</span>
              {it.size !== undefined && <span className="text-slate-500 tabular-nums">{formatBytes(it.size)}</span>}
            </li>
          ))}
          {part.items.length > 5 && (
            <li>
              <button type="button" className="font-sans text-brand-600 hover:underline" onClick={() => setOpen(!open)}>
                {open ? "minder" : `nog ${part.items.length - 5}`}
              </button>
            </li>
          )}
        </ul>
      )}
    </div>
  );
}
