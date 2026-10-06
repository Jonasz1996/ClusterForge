"use client";

import dynamic from "next/dynamic";
import Link from "next/link";
import { useSearchParams } from "next/navigation";
import { Suspense, useMemo, useState, useSyncExternalStore } from "react";
import { ServiceDialog } from "@/components/deps/Dialogs";
import { ImpactList, ServicePanel, ServiceStatusText, StatusDot } from "@/components/deps/Panel";
import { Empty, EnvBadge, QueryState, StatusBadge, tableClass, tdClass, thClass } from "@/components/inventory/bits";
import { Alert, Button, cx, PageHeader, Select } from "@/components/ui";
import {
  indexGraph,
  instancesText,
  kindLabel,
  label,
  sources,
  strengthLabel,
  useDepGraph,
  useImpact,
  useUpdateService,
  visibleEdges,
  type DepGraph,
  type DepGroup,
  type DepService,
  type GraphFilter,
  type ImpactQuery,
  type Index,
} from "@/lib/deps";
import { environments, useIsAdmin, type Environment } from "@/lib/inventory";

// React Flow en dagre laden alleen op deze pagina en alleen in de browser,
// zodat de rest van de static export niet groeit.
const DepGraphView = dynamic(() => import("@/components/deps/Graph"), {
  ssr: false,
  loading: () => <div className="flex h-full items-center justify-center text-sm text-slate-500">Graaf laden…</div>,
});

export default function DependenciesPage() {
  return (
    <Suspense>
      <DependenciesInner />
    </Suspense>
  );
}

// useWide volgt de breekpunt sm (640 px): daaronder een lijst in plaats van
// de graaf.
function useWide() {
  return useSyncExternalStore(
    (cb) => {
      const m = window.matchMedia("(min-width: 640px)");
      m.addEventListener("change", cb);
      return () => m.removeEventListener("change", cb);
    },
    () => window.matchMedia("(min-width: 640px)").matches,
    () => true,
  );
}

function DependenciesInner() {
  const params = useSearchParams();
  const clusterId = params.get("cluster_id") ?? "";
  const isAdmin = useIsAdmin();
  const wide = useWide();
  const [tab, setTab] = useState<"graaf" | "voorstellen">("graaf");
  const [environment, setEnvironment] = useState<Environment | "">("");
  const [onlyClusters, setOnlyClusters] = useState(false);
  const [internal, setInternal] = useState(true);
  const [showSuggested, setShowSuggested] = useState(false);
  // Vanuit clusterdetail opent ?dienst= meteen het zijpaneel.
  const [selected, setSelected] = useState<string | null>(params.get("dienst"));
  const [impactOn, setImpactOn] = useState(false);
  const [adding, setAdding] = useState(false);

  const level = onlyClusters && wide ? "cluster" : "service";
  const filter: GraphFilter = {
    ...(clusterId ? { cluster_id: clusterId } : {}),
    ...(environment ? { environment } : {}),
    include_suggested: showSuggested,
    level,
  };
  const graph = useDepGraph(filter);
  const idx = useMemo(() => indexGraph(graph.data), [graph.data]);

  const selService = selected ? idx.services.get(selected) : undefined;
  const selGroup = selected && level === "cluster" ? idx.groups.get(selected) : undefined;
  const impactQuery: ImpactQuery | null = !impactOn
    ? null
    : selService
      ? { service_id: selService.id }
      : selGroup?.cluster_id
        ? { cluster_id: selGroup.cluster_id }
        : selGroup?.node_id
          ? { node_id: selGroup.node_id }
          : null;
  const impact = useImpact(impactQuery);
  const impactActive = impactQuery !== null;
  const highlight = useMemo(() => {
    if (!impactActive || !impact.data || !selected) return null;
    const ids = new Set<string>([selected]);
    if (level === "cluster") for (const g of impact.data.groups) ids.add(g.group_id);
    else for (const it of impact.data.items) ids.add(it.service_id);
    return ids;
  }, [impactActive, impact.data, selected, level]);

  const select = (id: string | null) => {
    if (id !== selected) setImpactOn(false);
    setSelected(id);
  };
  const clusterName = clusterId ? graph.data?.groups.find((g) => g.cluster_id === clusterId)?.name : undefined;

  return (
    <div className="space-y-5">
      <PageHeader
        title="Afhankelijkheden"
        description={
          clusterId ? (
            <span>
              {clusterName ?? "Dit cluster"} met zijn directe buren.{" "}
              <Link href="/afhankelijkheden" className="text-brand-700 hover:underline dark:text-sky-300">
                Alles tonen
              </Link>
            </span>
          ) : (
            "Welke dienst van welke andere afhangt, over alle clusters heen. Alleen bekende afhankelijkheden tellen mee."
          )
        }
        actions={isAdmin && <Button onClick={() => setAdding(true)}>Dienst toevoegen</Button>}
      />

      <div className="flex flex-wrap items-center gap-x-5 gap-y-2 text-sm">
        <Select
          aria-label="Omgeving"
          className="w-auto"
          value={environment}
          onChange={(e) => setEnvironment(e.target.value as Environment | "")}
        >
          <option value="">Alle omgevingen</option>
          {environments.map((e) => (
            <option key={e.value} value={e.value}>
              {e.label}
            </option>
          ))}
        </Select>
        <Toggle label="Alleen clusters" checked={onlyClusters} onChange={(v) => {
            setOnlyClusters(v);
            select(null);
          }} className="hidden sm:inline-flex" />
        <Toggle label="Interne afhankelijkheden" checked={internal} onChange={setInternal} className="hidden sm:inline-flex" />
        <Toggle label="Voorstellen tonen" checked={showSuggested} onChange={setShowSuggested} />
        {(graph.data?.suggestions ?? 0) > 0 && (
          <button type="button" className="text-brand-700 hover:underline dark:text-sky-300" onClick={() => setTab("voorstellen")}>
            {graph.data!.suggestions === 1 ? "1 voorstel" : `${graph.data!.suggestions} voorstellen`}
          </button>
        )}
      </div>

      <div className="flex gap-1 border-b border-slate-200 dark:border-slate-800" role="tablist">
        {(["graaf", "voorstellen"] as const).map((t) => (
          <button
            key={t}
            type="button"
            role="tab"
            aria-selected={tab === t}
            onClick={() => setTab(t)}
            className={cx(
              "-mb-px border-b-2 px-3 py-2 text-sm font-medium",
              tab === t ? "border-brand-600 text-brand-700 dark:text-sky-300" : "border-transparent text-slate-500 hover:text-slate-700 dark:hover:text-slate-300",
            )}
          >
            {t === "graaf" ? (wide ? "Graaf" : "Lijst") : "Voorstellen"}
          </button>
        ))}
      </div>

      {tab === "voorstellen" ? (
        <Suggestions environment={environment} clusterId={clusterId} isAdmin={isAdmin} />
      ) : (
        <QueryState q={graph}>
          {graph.data && graph.data.groups.length === 0 ? (
            <Empty>
              Nog geen diensten. Een uitrol uit een template maakt ze aan, de agents stellen bekende units voor, en een beheerder voegt ze met de
              hand toe.
            </Empty>
          ) : (
            graph.data &&
            (wide ? (
              <div className="flex gap-4">
                <div className="h-[70vh] min-h-[420px] min-w-0 flex-1 overflow-hidden rounded-lg border border-slate-200 bg-white dark:border-slate-800 dark:bg-slate-950">
                  <DepGraphView
                    graph={graph.data}
                    internal={internal}
                    selected={selected}
                    highlight={highlight}
                    onSelect={select}
                    fitKey={selService || selGroup ? "paneel" : ""}
                  />
                </div>
                {(selService || selGroup) && (
                  <div className="max-h-[70vh] w-80 shrink-0 overflow-y-auto rounded-lg border border-slate-200 bg-white p-4 dark:border-slate-800 dark:bg-slate-900">
                    {selService ? (
                      <ServicePanel
                        s={selService}
                        idx={idx}
                        isAdmin={isAdmin}
                        impactOn={impactOn}
                        onImpact={setImpactOn}
                        onClose={() => select(null)}
                      />
                    ) : (
                      <GroupPanel g={selGroup!} impactOn={impactOn} onImpact={setImpactOn} onClose={() => select(null)} impact={impact.data} />
                    )}
                  </div>
                )}
              </div>
            ) : (
              <ListView graph={graph.data} idx={idx} isAdmin={isAdmin} selected={selected} onSelect={select} impactOn={impactOn} onImpact={setImpactOn} />
            ))
          )}
          {graph.data && wide && <Legend />}
        </QueryState>
      )}

      {adding && <ServiceDialog scope={clusterId ? { cluster_id: clusterId } : undefined} onClose={() => setAdding(false)} onSaved={(s) => select(s.id)} />}
    </div>
  );
}

function Toggle({ label, checked, onChange, className }: { label: string; checked: boolean; onChange: (v: boolean) => void; className?: string }) {
  return (
    <label className={cx("cursor-pointer items-center gap-2", className ?? "inline-flex")}>
      <input type="checkbox" className="size-4 rounded border-slate-300" checked={checked} onChange={(e) => onChange(e.target.checked)} />
      {label}
    </label>
  );
}

function Legend() {
  const line = (style: React.CSSProperties) => <span className="inline-block w-6 border-t-2 align-middle" style={style} aria-hidden />;
  return (
    <div className="mt-3 flex flex-wrap items-center gap-x-5 gap-y-1.5 text-xs text-slate-500">
      <span className="inline-flex items-center gap-1.5">
        <StatusDot status="healthy" /> gezond
      </span>
      <span className="inline-flex items-center gap-1.5">
        <StatusDot status="degraded" /> verminderd
      </span>
      <span className="inline-flex items-center gap-1.5">
        <StatusDot status="down" /> down
      </span>
      <span className="inline-flex items-center gap-1.5">
        <StatusDot status="unknown" /> onbekend
      </span>
      <span className="inline-flex items-center gap-1.5">
        <span className="inline-block h-3 w-5 rounded-sm border-2 border-red-500" aria-hidden /> down door afhankelijkheid
      </span>
      <span className="inline-flex items-center gap-1.5">
        <span className="inline-block h-3 w-5 rounded-sm border-2 border-amber-500" aria-hidden /> verminderd door afhankelijkheid
      </span>
      <span className="inline-flex items-center gap-1.5">{line({ borderColor: "#475569" })} hard</span>
      <span className="inline-flex items-center gap-1.5">{line({ borderColor: "#94a3b8", borderTopWidth: 1 })} zacht</span>
      <span className="inline-flex items-center gap-1.5">{line({ borderColor: "#475569", borderTopStyle: "dashed" })} voorstel</span>
      <span>Een pijl loopt van afnemer naar leverancier.</span>
    </div>
  );
}

// GroupPanel is het zijpaneel in de ingeklapte weergave.
function GroupPanel({
  g,
  impactOn,
  onImpact,
  onClose,
  impact,
}: {
  g: DepGroup;
  impactOn: boolean;
  onImpact: (v: boolean) => void;
  onClose: () => void;
  impact: Parameters<typeof ImpactList>[0]["impact"] | undefined;
}) {
  return (
    <aside className="space-y-4 text-sm" aria-label={g.name}>
      <div className="flex items-start justify-between gap-2">
        <div className="min-w-0">
          <h2 className="text-base font-semibold break-words">{g.name}</h2>
          <div className="mt-1 flex flex-wrap items-center gap-1.5">
            {g.kind !== "external" && <StatusBadge status={g.status} reason={g.status_reason} />}
            {g.environment && <EnvBadge env={g.environment} />}
            <span className="text-slate-500">{g.kind === "cluster" ? "cluster" : g.kind === "node" ? "losse node" : "extern"}</span>
          </div>
        </div>
        <Button variant="ghost" className="px-2 py-1" onClick={onClose} aria-label="Sluiten">
          ✕
        </Button>
      </div>
      {g.impacted_by.length > 0 && <Alert>Geraakt door {g.impacted_by.join(", ")}.</Alert>}
      {g.cluster_id && (
        <div className="flex flex-wrap gap-2">
          <Link
            href={`/afhankelijkheden?cluster_id=${g.cluster_id}`}
            className="text-brand-700 hover:underline dark:text-sky-300"
            onClick={onClose}
          >
            Diensten tonen
          </Link>
          <Link href={`/clusters/detail?id=${g.cluster_id}`} className="text-brand-700 hover:underline dark:text-sky-300">
            Naar het cluster
          </Link>
        </div>
      )}
      {g.kind !== "external" && (
        <div>
          <Button variant={impactOn ? "primary" : "secondary"} onClick={() => onImpact(!impactOn)} aria-pressed={impactOn}>
            {impactOn ? "Impact verbergen" : "Wat raakt uitval?"}
          </Button>
        </div>
      )}
      {impactOn && impact && <ImpactList impact={impact} />}
    </aside>
  );
}

// ListView is de weergave onder 640 px: per groep de diensten met Hangt af
// van en Gebruikt door.
function ListView({
  graph,
  idx,
  isAdmin,
  selected,
  onSelect,
  impactOn,
  onImpact,
}: {
  graph: DepGraph;
  idx: Index;
  isAdmin: boolean;
  selected: string | null;
  onSelect: (id: string | null) => void;
  impactOn: boolean;
  onImpact: (v: boolean) => void;
}) {
  const edges = visibleEdges(graph, true);
  const sel = selected ? idx.services.get(selected) : undefined;
  return (
    <div className="space-y-4">
      {graph.groups.map((g) => {
        const items = graph.services.filter((s) => s.group_id === g.id);
        if (items.length === 0) return null;
        return (
          <section key={g.id} className="rounded-lg border border-slate-200 bg-white dark:border-slate-800 dark:bg-slate-900">
            <div className="flex flex-wrap items-center gap-2 border-b border-slate-100 px-4 py-2.5 dark:border-slate-800">
              <h2 className="font-semibold">{g.name}</h2>
              {g.environment && <EnvBadge env={g.environment} />}
              {g.kind !== "external" && <StatusBadge status={g.status} reason={g.status_reason} />}
            </div>
            <ul className="divide-y divide-slate-100 dark:divide-slate-800">
              {items.map((s) => {
                const uses = edges.filter((e) => e.from === s.id);
                const usedBy = edges.filter((e) => e.to === s.id);
                return (
                  <li key={s.id} className="px-4 py-3 text-sm">
                    <button type="button" className="w-full text-left" onClick={() => onSelect(selected === s.id ? null : s.id)}>
                      <div className="flex flex-wrap items-center justify-between gap-2">
                        <span className={cx("font-medium", s.state === "suggested" && "italic")}>
                          {s.name}
                          <span className="font-normal text-slate-500"> · {kindLabel(s.kind)}</span>
                        </span>
                        <ServiceStatusText s={s} />
                      </div>
                      {uses.length > 0 && (
                        <p className="mt-1 text-xs text-slate-500">
                          Hangt af van {uses.map((e) => `${label(idx, e.to, g.id)} (${strengthLabel(e.strength)})`).join(", ")}
                        </p>
                      )}
                      {usedBy.length > 0 && (
                        <p className="mt-0.5 text-xs text-slate-500">Gebruikt door {usedBy.map((e) => label(idx, e.from, g.id)).join(", ")}</p>
                      )}
                    </button>
                    {sel?.id === s.id && (
                      <div className="mt-3 rounded-md border border-slate-200 p-3 dark:border-slate-800">
                        <ServicePanel s={sel} idx={idx} isAdmin={isAdmin} impactOn={impactOn} onImpact={onImpact} onClose={() => onSelect(null)} />
                      </div>
                    )}
                  </li>
                );
              })}
            </ul>
          </section>
        );
      })}
    </div>
  );
}

// Suggestions is de tab Voorstellen: wat de agents zagen en nog niet
// bevestigd is, en wat genegeerd werd.
function Suggestions({ environment, clusterId, isAdmin }: { environment: Environment | ""; clusterId: string; isAdmin: boolean }) {
  const graph = useDepGraph({
    ...(clusterId ? { cluster_id: clusterId } : {}),
    ...(environment ? { environment } : {}),
    include_suggested: true,
    include_ignored: true,
  });
  const idx = useMemo(() => indexGraph(graph.data), [graph.data]);
  const update = useUpdateService();
  const err = update.error instanceof Error ? update.error.message : null;
  const all = graph.data?.services ?? [];
  const suggested = all.filter((s) => s.state === "suggested" && (!clusterId || s.cluster_id === clusterId));
  const ignored = all.filter((s) => s.state === "ignored" && (!clusterId || s.cluster_id === clusterId));
  const groupName = (s: DepService) => idx.groups.get(s.group_id)?.name ?? "";

  return (
    <QueryState q={graph}>
      <div className="space-y-6">
        {err && <Alert>{err}</Alert>}
        <section>
          <p className="mb-3 text-sm text-slate-600 dark:text-slate-400">
            ClusterForge stelt een dienst voor als een agent een bekende unit ziet draaien. Een voorstel telt pas mee na bevestigen.
          </p>
          {suggested.length === 0 ? (
            <Empty>Geen voorstellen.</Empty>
          ) : (
            <div className="overflow-x-auto rounded-lg border border-slate-200 bg-white dark:border-slate-800 dark:bg-slate-900">
              <table className={tableClass}>
                <thead>
                  <tr className="border-b border-slate-200 dark:border-slate-800">
                    <th className={thClass}>Dienst</th>
                    <th className={thClass}>Soort</th>
                    <th className={thClass}>Cluster</th>
                    <th className={thClass}>Gezien</th>
                    {isAdmin && <th className={thClass} />}
                  </tr>
                </thead>
                <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
                  {suggested.map((s) => (
                    <tr key={s.id}>
                      <td className={cx(tdClass, "font-medium")}>{s.name}</td>
                      <td className={tdClass}>{kindLabel(s.kind)}</td>
                      <td className={tdClass}>{groupName(s)}</td>
                      <td className={cx(tdClass, "text-slate-500")}>{instancesText(s) || "nu niet"}</td>
                      {isAdmin && (
                        <td className={cx(tdClass, "text-right whitespace-nowrap")}>
                          <Button
                            className="px-2.5 py-1"
                            disabled={update.isPending}
                            onClick={() => update.mutate({ id: s.id, body: { state: "confirmed" } })}
                          >
                            Bevestigen
                          </Button>{" "}
                          <Button
                            variant="secondary"
                            className="px-2.5 py-1"
                            disabled={update.isPending}
                            onClick={() => update.mutate({ id: s.id, body: { state: "ignored" } })}
                          >
                            Negeren
                          </Button>
                        </td>
                      )}
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </section>
        {ignored.length > 0 && (
          <section>
            <h2 className="mb-2 font-semibold">Genegeerd</h2>
            <p className="mb-3 text-sm text-slate-600 dark:text-slate-400">Deze diensten tellen niet mee en worden niet opnieuw voorgesteld.</p>
            <ul className="divide-y divide-slate-100 rounded-lg border border-slate-200 bg-white text-sm dark:divide-slate-800 dark:border-slate-800 dark:bg-slate-900">
              {ignored.map((s) => (
                <li key={s.id} className="flex flex-wrap items-center justify-between gap-2 px-3 py-2">
                  <span>
                    <span className="font-medium">{s.name}</span>
                    <span className="text-slate-500">
                      {" "}
                      · {kindLabel(s.kind)} · {groupName(s)} · {sources[s.source]}
                    </span>
                  </span>
                  {isAdmin && (
                    <Button
                      variant="secondary"
                      className="px-2.5 py-1"
                      disabled={update.isPending}
                      onClick={() => update.mutate({ id: s.id, body: { state: s.source === "discovered" ? "suggested" : "confirmed" } })}
                    >
                      Terugzetten
                    </Button>
                  )}
                </li>
              ))}
            </ul>
          </section>
        )}
      </div>
    </QueryState>
  );
}
