"use client";

import dynamic from "next/dynamic";
import Link from "next/link";
import { usePathname, useRouter, useSearchParams } from "next/navigation";
import { Suspense, useMemo, useState, useSyncExternalStore } from "react";
import { ServicePanel } from "@/components/deps/Panel";
import { EnvBadge, QueryState, StatusBadge } from "@/components/inventory/bits";
import { Alert, Badge, Button, cx, PageHeader, Select } from "@/components/ui";
import { indexGraph, useDepGraph } from "@/lib/deps";
import { useIncident } from "@/lib/incidents";
import { environments, useClusters, useIsAdmin, type Environment, type Status } from "@/lib/inventory";
import {
  clusterColors,
  edgeLabels,
  incidentOverlay,
  kindLabels,
  markInfo,
  markOf,
  useMap,
  useNodeLinks,
  whatIfOverlay,
  type MapItem,
  type MapItemKind,
  type MapLayer,
  type Mark,
  type Overlay,
} from "@/lib/map";
import { useWhatIf } from "@/lib/risk";

// React Flow en dagre laden alleen op deze pagina en alleen in de browser.
const loading = () => <div className="flex h-full items-center justify-center text-sm text-slate-500">Kaart laden…</div>;
const MapGraph = dynamic(() => import("@/components/map/MapGraph"), { ssr: false, loading });
const DepGraphView = dynamic(() => import("@/components/deps/Graph"), { ssr: false, loading });

type Laag = "fysiek" | "netwerk" | "diensten";

const lagen: { id: Laag; label: string; hint: string }[] = [
  {
    id: "fysiek",
    label: "Fysiek",
    hint: "Per Proxmox-host de VM's, containers en storage, met een lijn naar de storage van hun schijven.",
  },
  {
    id: "netwerk",
    label: "Netwerk",
    hint: "Bridges en VLAN's van de hosts, de subnetten met hun nodes, de gateways en de VIP's met hun huidige eigenaar.",
  },
  {
    id: "diensten",
    label: "Diensten",
    hint: "De afhankelijkheden tussen diensten, zoals op Afhankelijkheden. Het getal op een lijn is hoe vaak de agents de afgelopen 24 uur een verbinding zagen.",
  },
];

export default function MapPage() {
  return (
    <Suspense>
      <MapInner />
    </Suspense>
  );
}

// useWide volgt de breekpunt sm (640 px): daaronder een lijst.
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

// De laag, de filters en de overlay staan in de URL, zodat de wat-als en een
// incident er rechtstreeks naartoe linken.
function MapInner() {
  const params = useSearchParams();
  const router = useRouter();
  const pathname = usePathname();
  const wide = useWide();
  const isAdmin = useIsAdmin();
  const raw = params.get("laag");
  const laag: Laag = lagen.some((l) => l.id === raw) ? (raw as Laag) : "fysiek";
  const environment = (params.get("omgeving") ?? "") as Environment | "";
  const clusterId = params.get("cluster_id") ?? "";
  const onderdelen = params.getAll("onderdeel");
  const incidentId = params.get("incident") ?? "";
  const [selected, setSelected] = useState<string | null>(null);

  const go = (patch: Record<string, string | string[] | null>) => {
    const next = new URLSearchParams(params.toString());
    for (const [k, v] of Object.entries(patch)) {
      next.delete(k);
      if (Array.isArray(v)) for (const x of v) next.append(k, x);
      else if (v) next.set(k, v);
    }
    if (next.get("laag") === "fysiek") next.delete("laag");
    const s = next.toString();
    router.replace(s ? `${pathname}?${s}` : pathname, { scroll: false });
  };

  const filter = { ...(environment ? { environment } : {}), ...(clusterId ? { cluster_id: clusterId } : {}) };
  const layer = laag === "netwerk" ? "network" : "physical";
  const map = useMap(layer, filter, laag !== "diensten");
  const deps = useDepGraph(
    { ...filter, level: "service", include_suggested: true, include_connections: true },
    laag === "diensten",
  );
  const clusters = useClusters();
  const links = useNodeLinks();
  const whatIf = useWhatIf(incidentId ? [] : onderdelen);
  const incident = useIncident(incidentId);

  const overlay: Overlay | null = useMemo(() => {
    if (incidentId && incident.data) return incidentOverlay(incident.data.incident.subjects);
    if (onderdelen.length > 0 && whatIf.data) return whatIfOverlay(whatIf.data);
    return null;
  }, [incidentId, incident.data, onderdelen.length, whatIf.data]);
  const colors = useMemo(() => clusterColors(map.data?.items ?? []), [map.data]);
  const idx = useMemo(() => indexGraph(deps.data), [deps.data]);
  const selItem = map.data?.items.find((i) => i.id === selected);
  const selService = laag === "diensten" && selected ? idx.services.get(selected) : undefined;
  const select = (id: string | null) => setSelected(id);
  const proposals = links.data?.length ?? 0;

  return (
    <div className="space-y-5">
      <PageHeader title="Kaart" description={lagen.find((l) => l.id === laag)!.hint} />

      {overlay !== null && (
        <OverlayBanner
          kind={incidentId ? "incident" : "wat-als"}
          title={
            incidentId
              ? `Incident #${incident.data?.incident.number}: ${incident.data?.incident.title}`
              : `Wat als ${whatIf.data!.components.map((c) => c.name).join(" en ")} uitvalt`
          }
          back={
            incidentId
              ? `/incidenten/detail?id=${incidentId}`
              : `/risico?${new URLSearchParams(onderdelen.map((o) => ["onderdeel", o]))}&tab=wat-als`
          }
          onClear={() => go({ onderdeel: null, incident: null })}
        />
      )}
      {whatIf.isError && (
        <Alert>De wat-als kon niet gerekend worden: {whatIf.error instanceof Error ? whatIf.error.message : ""}</Alert>
      )}

      <div className="flex flex-wrap items-center gap-x-4 gap-y-2 text-sm">
        <Select
          aria-label="Omgeving"
          className="w-auto"
          value={environment}
          onChange={(e) => go({ omgeving: e.target.value || null })}
        >
          <option value="">Alle omgevingen</option>
          {environments.map((e) => (
            <option key={e.value} value={e.value}>
              {e.label}
            </option>
          ))}
        </Select>
        <Select
          aria-label="Cluster"
          className="w-auto"
          value={clusterId}
          onChange={(e) => go({ cluster_id: e.target.value || null })}
        >
          <option value="">Alle clusters</option>
          {(clusters.data ?? [])
            .slice()
            .sort((a, b) => a.name.localeCompare(b.name))
            .map((c) => (
              <option key={c.id} value={c.id}>
                {c.name}
              </option>
            ))}
        </Select>
        {proposals > 0 && (
          <Link href="/proxmox#koppelvoorstellen" className="text-brand-700 hover:underline dark:text-sky-300">
            {proposals === 1 ? "1 VM lijkt een node te zijn" : `${proposals} VM's lijken een node te zijn`}
          </Link>
        )}
      </div>

      <div className="flex gap-1 border-b border-slate-200 dark:border-slate-800" role="tablist">
        {lagen.map((l) => (
          <button
            key={l.id}
            type="button"
            role="tab"
            aria-selected={laag === l.id}
            onClick={() => {
              select(null);
              go({ laag: l.id });
            }}
            className={cx(
              "-mb-px border-b-2 px-3 py-2 text-sm font-medium",
              laag === l.id
                ? "border-brand-600 text-brand-700 dark:text-sky-300"
                : "border-transparent text-slate-500 hover:text-slate-700 dark:hover:text-slate-300",
            )}
          >
            {l.label}
          </button>
        ))}
      </div>

      {laag === "diensten" ? (
        <QueryState q={deps}>
          {deps.data && deps.data.services.length === 0 ? (
            <Empty>
              Nog geen diensten. Ze komen uit een uitrol, uit voorstellen van de agents of met de hand op Afhankelijkheden.
            </Empty>
          ) : (
            deps.data && (
              <div className="flex gap-4">
                <div className="h-[70vh] min-h-[420px] min-w-0 flex-1 overflow-hidden rounded-lg border border-slate-200 bg-white dark:border-slate-800 dark:bg-slate-950">
                  <DepGraphView
                    graph={deps.data}
                    internal
                    selected={selected}
                    highlight={overlay ? overlay.services : null}
                    onSelect={select}
                    fitKey={selService ? "paneel" : ""}
                  />
                </div>
                {selService && wide && (
                  <div className="max-h-[70vh] w-80 shrink-0 overflow-y-auto rounded-lg border border-slate-200 bg-white p-4 dark:border-slate-800 dark:bg-slate-900">
                    <ServicePanel
                      s={selService}
                      idx={idx}
                      isAdmin={isAdmin}
                      impactOn={false}
                      onImpact={() => {}}
                      onClose={() => select(null)}
                    />
                  </div>
                )}
              </div>
            )
          )}
        </QueryState>
      ) : (
        <QueryState q={map}>
          {map.data && (
            <>
              {map.data.notes.map((n) => (
                <Alert key={n} kind="info">
                  {n}
                </Alert>
              ))}
              {map.data.items.length === 0 ? (
                <Empty layer={map.data.layer} filtered={!!environment || !!clusterId} />
              ) : wide ? (
                <div className="flex gap-4">
                  <div className="h-[70vh] min-h-[420px] min-w-0 flex-1 overflow-hidden rounded-lg border border-slate-200 bg-white dark:border-slate-800 dark:bg-slate-950">
                    <MapGraph
                      map={map.data}
                      colors={colors}
                      overlay={overlay}
                      selected={selected}
                      onSelect={select}
                      fitKey={selItem ? "paneel" : ""}
                    />
                  </div>
                  {selItem && (
                    <div className="max-h-[70vh] w-80 shrink-0 overflow-y-auto rounded-lg border border-slate-200 bg-white p-4 dark:border-slate-800 dark:bg-slate-900">
                      <ItemPanel
                        it={selItem}
                        map={map.data}
                        mark={overlay ? markOf(overlay, selItem) : undefined}
                        onSelect={select}
                      />
                    </div>
                  )}
                </div>
              ) : (
                <ListView map={map.data} overlay={overlay} selected={selected} onSelect={select} />
              )}
              {wide && map.data.items.length > 0 && <Legend layer={map.data.layer} colors={colors} overlay={overlay !== null} />}
            </>
          )}
        </QueryState>
      )}
    </div>
  );
}

function OverlayBanner({
  kind,
  title,
  back,
  onClear,
}: {
  kind: "wat-als" | "incident";
  title: string;
  back: string;
  onClear: () => void;
}) {
  return (
    <div className="flex flex-wrap items-center justify-between gap-3 rounded-lg border border-amber-300 bg-amber-50 px-4 py-3 text-sm dark:border-amber-800 dark:bg-amber-950/40">
      <div>
        <span className="font-semibold">{title}.</span>{" "}
        <span className="text-slate-600 dark:text-slate-400">
          {kind === "wat-als"
            ? "Gekleurd is wat uitvalt of door HA herstart; de rest is gedimd. Er is niets aangeraakt."
            : "Gekleurd is wat het incident raakte; de rest is gedimd."}
        </span>
      </div>
      <div className="flex shrink-0 gap-2">
        <Link href={back} className="rounded-md px-2.5 py-1 font-medium text-brand-700 hover:underline dark:text-sky-300">
          {kind === "wat-als" ? "Terug naar de wat-als" : "Naar het incident"}
        </Link>
        <Button variant="secondary" className="px-2.5 py-1" onClick={onClear}>
          Overlay weghalen
        </Button>
      </div>
    </div>
  );
}

function Empty({ layer, filtered, children }: { layer?: MapLayer["layer"]; filtered?: boolean; children?: React.ReactNode }) {
  return (
    <div className="rounded-lg border border-dashed border-slate-300 p-8 text-center text-sm text-slate-500 dark:border-slate-700">
      {children ??
        (filtered
          ? "Niets op de kaart met deze filters. Op de kaart staan alleen nodes die aan een VM gekoppeld zijn (fysiek) of die een agent hebben (netwerk)."
          : layer === "network"
            ? "Nog geen netwerk bekend. Het komt uit de agents op de nodes en uit Proxmox."
            : "Nog geen Proxmox-hosts. Koppel Proxmox om de fysieke laag te zien.")}
    </div>
  );
}

// neighbours zijn de elementen met een lijn naar of van it.
function neighbours(map: MapLayer, it: MapItem) {
  const byId = new Map(map.items.map((i) => [i.id, i]));
  const out: { it: MapItem; label: string }[] = [];
  for (const e of map.edges) {
    const other = e.from === it.id ? e.to : e.to === it.id ? e.from : null;
    const o = other ? byId.get(other) : undefined;
    if (!o) continue;
    let label = edgeLabels[e.kind];
    if (e.kind === "owner") label = e.from === it.id ? "eigenaar" : "VIP";
    if (e.kind === "disk") label = e.from === it.id ? "schijf op" : "schijf van";
    if (e.kind === "member") label = e.from === it.id ? "node in subnet" : "in subnet";
    if (e.label && e.kind !== "owner") label += ` (${e.label})`;
    out.push({ it: o, label });
  }
  return out.sort((a, b) => a.it.name.localeCompare(b.it.name, "nl", { numeric: true }));
}

function linkText(href: string) {
  if (href.startsWith("/nodes/")) return "Naar de node";
  if (href.startsWith("/clusters/")) return "Naar het cluster";
  if (href.startsWith("/proxmox")) return "Naar Proxmox";
  return "Openen";
}

function ItemPanel({
  it,
  map,
  mark,
  onSelect,
}: {
  it: MapItem;
  map: MapLayer;
  mark?: Mark;
  onSelect: (id: string | null) => void;
}) {
  const near = neighbours(map, it);
  const parent = it.parent ? map.items.find((i) => i.id === it.parent) : undefined;
  return (
    <aside className="space-y-4 text-sm" aria-label={it.name}>
      <div className="flex items-start justify-between gap-2">
        <div className="min-w-0">
          <h2 className="text-base font-semibold break-words">{it.name}</h2>
          <div className="mt-1 flex flex-wrap items-center gap-1.5">
            {hasStatus(it) && <StatusBadge status={it.status as Status} />}
            {it.environment && <EnvBadge env={it.environment} />}
            <span className="text-slate-500">
              {it.kind === "guest" ? (it.facts.find((f) => f.label === "Soort")?.value ?? "VM") : kindLabels[it.kind]}
              {parent && ` op ${parent.name}`}
            </span>
          </div>
        </div>
        <Button variant="ghost" className="px-2 py-1" onClick={() => onSelect(null)} aria-label="Sluiten">
          ✕
        </Button>
      </div>
      {mark && <Badge tone={markInfo[mark].tone}>{markInfo[mark].label}</Badge>}
      {it.self && <Alert kind="info">Hier draait ClusterForge zelf.</Alert>}
      {it.detail && <p className="text-slate-600 dark:text-slate-400">{it.detail}</p>}
      {it.facts.length > 0 && (
        <dl className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1.5">
          {it.facts.map((f, i) => (
            <div key={i} className="contents">
              <dt className="text-slate-500">{f.label}</dt>
              <dd className="min-w-0 break-words">{f.value}</dd>
            </div>
          ))}
        </dl>
      )}
      {near.length > 0 && (
        <div>
          <h3 className="mb-1.5 text-xs font-semibold tracking-wide text-slate-500 uppercase">Verbonden met</h3>
          <ul className="space-y-1">
            {near.map((n, i) => (
              <li key={i}>
                <button
                  type="button"
                  className="text-left text-brand-700 hover:underline dark:text-sky-300"
                  onClick={() => onSelect(n.it.id)}
                >
                  {n.it.name}
                </button>{" "}
                <span className="text-slate-500">· {n.label}</span>
              </li>
            ))}
          </ul>
        </div>
      )}
      <div className="flex flex-wrap gap-x-4 gap-y-1">
        {it.link && (
          <Link href={it.link} className="text-brand-700 hover:underline dark:text-sky-300">
            {linkText(it.link)}
          </Link>
        )}
        {whatIfKey(it) && (
          <Link
            href={`/risico?tab=wat-als&onderdeel=${encodeURIComponent(whatIfKey(it)!)}`}
            className="text-brand-700 hover:underline dark:text-sky-300"
          >
            Wat als dit uitvalt?
          </Link>
        )}
      </div>
    </aside>
  );
}

// Een subnet of gateway heeft geen eigen status.
function hasStatus(it: MapItem) {
  return it.kind !== "subnet" && it.kind !== "gateway";
}

// whatIfKey is de sleutel van het onderdeel in de wat-als, als die het kent.
function whatIfKey(it: MapItem): string | null {
  if (it.kind === "host" || it.kind === "storage" || it.kind === "bridge") return it.id;
  if (it.node_id) return `node:${it.node_id}`;
  if (it.kind === "vip" && it.cluster_id) return `cluster:${it.cluster_id}`;
  return null;
}

const listOrder: Record<MapLayer["layer"], MapItemKind[]> = {
  physical: ["host", "storage", "guest"],
  network: ["subnet", "vip", "gateway", "host", "bridge", "node"],
};

const listTitles: Partial<Record<MapItemKind, string>> = {
  subnet: "Subnetten",
  vip: "VIP's",
  gateway: "Gateways",
  bridge: "Bridges",
  node: "Nodes",
};

// ListView is de kaart onder 640 px: fysiek per host, het netwerk per soort.
function ListView({
  map,
  overlay,
  selected,
  onSelect,
}: {
  map: MapLayer;
  overlay: Overlay | null;
  selected: string | null;
  onSelect: (id: string | null) => void;
}) {
  const sections: { key: string; title: string; status?: string; items: MapItem[] }[] = [];
  if (map.layer === "physical") {
    for (const h of map.items.filter((i) => i.kind === "host")) {
      sections.push({ key: h.id, title: h.name, status: h.status, items: map.items.filter((i) => i.parent === h.id) });
    }
    const shared = map.items.filter((i) => i.kind === "storage" && !i.parent);
    if (shared.length) sections.push({ key: "shared", title: "Gedeelde storage", items: shared });
  } else {
    for (const k of listOrder.network) {
      if (k === "host") continue;
      const items = map.items.filter((i) => i.kind === k);
      if (items.length) sections.push({ key: k, title: listTitles[k] ?? k, items });
    }
  }
  return (
    <div className="space-y-4">
      {sections.map((s) => (
        <section key={s.key} className="rounded-lg border border-slate-200 bg-white dark:border-slate-800 dark:bg-slate-900">
          <div className="flex items-center gap-2 border-b border-slate-100 px-4 py-2.5 dark:border-slate-800">
            <h2 className="font-semibold">{s.title}</h2>
            {s.status && <StatusBadge status={s.status as Status} />}
          </div>
          <ul className="divide-y divide-slate-100 dark:divide-slate-800">
            {s.items.map((it) => {
              const mark = overlay ? markOf(overlay, it) : undefined;
              const parent = it.kind === "bridge" ? map.items.find((h) => h.id === it.parent)?.name : undefined;
              return (
                <li key={it.id} className={cx("px-4 py-3 text-sm", overlay && !mark && "opacity-50")}>
                  <button type="button" className="w-full text-left" onClick={() => onSelect(selected === it.id ? null : it.id)}>
                    <div className="flex flex-wrap items-center justify-between gap-2">
                      <span className="font-medium">
                        {it.name}
                        {parent && <span className="font-normal text-slate-500"> op {parent}</span>}
                      </span>
                      {mark ? (
                        <Badge tone={markInfo[mark].tone}>{markInfo[mark].label}</Badge>
                      ) : (
                        hasStatus(it) && <StatusBadge status={it.status as Status} />
                      )}
                    </div>
                    {it.detail && <p className="mt-0.5 text-xs text-slate-500">{it.detail}</p>}
                  </button>
                  {selected === it.id && (
                    <div className="mt-3 rounded-md border border-slate-200 p-3 dark:border-slate-800">
                      <ItemPanel it={it} map={map} mark={mark} onSelect={onSelect} />
                    </div>
                  )}
                </li>
              );
            })}
          </ul>
        </section>
      ))}
    </div>
  );
}

function Legend({
  layer,
  colors,
  overlay,
}: {
  layer: MapLayer["layer"];
  colors: Map<string, { name: string; color: string }>;
  overlay: boolean;
}) {
  const line = (style: React.CSSProperties) => (
    <span className="inline-block w-6 border-t-2 align-middle" style={style} aria-hidden />
  );
  return (
    <div className="mt-3 flex flex-wrap items-center gap-x-5 gap-y-1.5 text-xs text-slate-500">
      {[...colors.values()].map((c) => (
        <span key={c.name} className="inline-flex items-center gap-1.5">
          <span className="inline-block h-3 w-1 rounded-sm" style={{ background: c.color }} aria-hidden /> {c.name}
        </span>
      ))}
      {layer === "physical" ? (
        <span className="inline-flex items-center gap-1.5">
          {line({ borderColor: "#cbd5e1", borderTopWidth: 1 })} schijf op storage
        </span>
      ) : (
        <>
          <span className="inline-flex items-center gap-1.5">{line({ borderColor: "#475569" })} bridge naar subnet</span>
          <span className="inline-flex items-center gap-1.5">
            {line({ borderColor: "#94a3b8", borderTopWidth: 1 })} node in subnet
          </span>
          <span className="inline-flex items-center gap-1.5">{line({ borderColor: "#7c3aed" })} gateway</span>
          <span className="inline-flex items-center gap-1.5">{line({ borderColor: "#059669" })} VIP bij eigenaar</span>
          <span className="inline-flex items-center gap-1.5">
            {line({ borderColor: "#64748b", borderTopStyle: "dashed" })} VRRP-peers
          </span>
        </>
      )}
      {overlay &&
        (["failed", "down", "degraded", "restarted"] as Mark[]).map((m) => (
          <span key={m} className="inline-flex items-center gap-1.5">
            <span className={cx("inline-block h-3 w-5 rounded-sm", markInfo[m].ring)} aria-hidden /> {markInfo[m].label}
          </span>
        ))}
    </div>
  );
}
