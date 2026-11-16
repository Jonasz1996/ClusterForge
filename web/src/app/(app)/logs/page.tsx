"use client";

import Link from "next/link";
import { useSearchParams } from "next/navigation";
import { useQuery } from "@tanstack/react-query";
import { Fragment, Suspense, useMemo, useState, type ReactNode } from "react";
import { Modal } from "@/components/Modal";
import { tableClass, tdClass, thClass } from "@/components/inventory/bits";
import { Alert, Badge, Button, Card, Input, Label, PageHeader, Select, cx } from "@/components/ui";
import { ApiError } from "@/lib/api/client";
import { useIsAdmin } from "@/lib/inventory";
import {
  colorOf,
  compileParse,
  defaultFilter,
  fetchLogs,
  fieldsOf,
  levelLabel,
  levels,
  patternOf,
  ranges,
  sourceKey,
  sourceLabel,
  toQuery,
  useDeleteLogView,
  useLogNodes,
  useLogSources,
  useLogViews,
  useSaveLogView,
  type Level,
  type LogEntry,
  type LogFilterState,
  type LogKind,
  type LogNode,
  type LogQueryResult,
  type LogSource,
  type LogView,
  type RangeId,
} from "@/lib/logs";

function errText(e: unknown) {
  return e instanceof ApiError ? e.message : "Er ging iets mis.";
}

export default function LogsPage() {
  return (
    <Suspense>
      <Logs />
    </Suspense>
  );
}

type Mode = "tijdlijn" | "naast" | "patronen" | "velden";

const modes: { id: Mode; label: string; hint: string }[] = [
  { id: "tijdlijn", label: "Tijdlijn", hint: "Alle bronnen samen, op tijd" },
  { id: "naast", label: "Naast elkaar", hint: "Een kolom per bron, rijen op tijd" },
  { id: "patronen", label: "Patronen", hint: "Gelijke regels samen, geteld per bron" },
  { id: "velden", label: "Velden", hint: "Ontlede velden en hun waarden" },
];

function levelTone(l: string) {
  switch (l) {
    case "error":
      return "red" as const;
    case "warn":
      return "amber" as const;
    case "info":
      return "blue" as const;
    default:
      return "slate" as const;
  }
}

const levelRank: Record<string, number> = { error: 4, warn: 3, info: 2, debug: 1, "": 0 };

function fmtTime(iso: string | undefined, withDate: boolean) {
  if (!iso) return "—";
  const d = new Date(iso);
  const t = d.toLocaleTimeString("nl-BE", { hour: "2-digit", minute: "2-digit", second: "2-digit" });
  const ms = String(d.getMilliseconds()).padStart(3, "0");
  if (!withDate) return `${t}.${ms}`;
  return `${d.toLocaleDateString("nl-BE", { day: "2-digit", month: "2-digit" })} ${t}.${ms}`;
}

function toLocalInput(ms: number) {
  const d = new Date(ms);
  const p = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}T${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`;
}

// highlighter maakt van de zoekterm een expressie om treffers te markeren.
function highlighter(f: LogFilterState): RegExp | null {
  if (!f.pattern) return null;
  try {
    return new RegExp(f.regex ? f.pattern : f.pattern.replace(/[.*+?^${}()|[\]\\]/g, "\\$&"), f.ignoreCase ? "gi" : "g");
  } catch {
    return null;
  }
}

function Highlight({ text, re }: { text: string; re: RegExp | null }) {
  if (!re) return <>{text}</>;
  const parts: ReactNode[] = [];
  let last = 0;
  // matchAll werkt op een kopie; lastIndex van re blijft zoals hij is.
  for (const m of text.matchAll(re)) {
    if (m[0] === "" || m.index === undefined) break;
    parts.push(text.slice(last, m.index));
    parts.push(
      <mark key={m.index} className="rounded-sm bg-yellow-200 px-0.5 text-slate-900 dark:bg-yellow-600/60 dark:text-white">
        {m[0]}
      </mark>,
    );
    last = m.index + m[0].length;
    if (parts.length > 200) break;
  }
  parts.push(text.slice(last));
  return <>{parts}</>;
}

function Logs() {
  const params = useSearchParams();
  const isAdmin = useIsAdmin();
  const nodesQ = useLogNodes();
  const viewsQ = useLogViews();
  const [picked, setPicked] = useState<string[]>([]);
  const [sources, setSources] = useState<LogSource[]>([]);
  const [draft, setDraft] = useState<LogFilterState>(defaultFilter);
  const [applied, setApplied] = useState<LogFilterState>(defaultFilter);
  const [mode, setMode] = useState<Mode>("tijdlijn");
  const [live, setLive] = useState(false);
  const [viewId, setViewId] = useState("");
  const [saving, setSaving] = useState(false);
  const [focus, setFocus] = useState("");
  const [inited, setInited] = useState(false);

  const nodes = useMemo(() => nodesQ.data?.nodes ?? [], [nodesQ.data]);
  const nodeById = useMemo(() => new Map(nodes.map((n) => [n.id, n])), [nodes]);

  // Vanuit een node of container: ?node=<id>&kind=docker&name=web-1, of
  // ?view=<id> voor een bewaarde weergave. Eenmalig, zodra de lijsten er
  // zijn (state aanpassen tijdens het renderen, zonder effect).
  if (!inited && nodesQ.data && viewsQ.data) {
    setInited(true);
    const v = params.get("view");
    const view = v ? viewsQ.data.find((x) => x.id === v) : undefined;
    const node = params.get("node");
    if (view) {
      loadView(view);
    } else if (node && nodeById.has(node)) {
      const n = nodeById.get(node)!;
      const kind = (params.get("kind") as LogKind | null) ?? undefined;
      const name = params.get("name") ?? "";
      const cluster =
        params.get("cluster") === "1" && n.cluster_id ? nodes.filter((x) => x.cluster_id === n.cluster_id && x.supported) : [n];
      setPicked(cluster.map((x) => x.id));
      if (kind) setSources(cluster.map((x) => ({ node_id: x.id, kind, name })));
    }
  }

  function loadView(v: LogView) {
    setViewId(v.id);
    setSources(v.sources);
    setPicked([...new Set(v.sources.map((s) => s.node_id))]);
    const f = { ...defaultFilter, ...(v.filter as Partial<LogFilterState>) };
    setDraft(f);
    setApplied(f);
    if (typeof (v.filter as { mode?: string }).mode === "string") setMode((v.filter as { mode: Mode }).mode);
  }

  const parse = useMemo(() => compileParse(applied.parse), [applied.parse]);
  const queryKey = ["logs", "query", JSON.stringify(sources), JSON.stringify({ ...applied, parse: "" })];
  const result = useQuery({
    queryKey,
    queryFn: () => fetchLogs(toQuery(sources, applied)),
    enabled: sources.length > 0,
    refetchInterval: live ? 10_000 : false,
    retry: false,
    placeholderData: (prev) => prev,
  });

  if (!isAdmin) {
    return (
      <div className="space-y-6">
        <PageHeader title="Logs" />
        <Alert kind="info">Logs bekijken kan alleen een beheerder: logs bevatten vaak gevoelige gegevens.</Alert>
      </div>
    );
  }

  const apply = (f: LogFilterState = draft) => {
    setApplied(f);
    setDraft(f);
  };

  const zoom = (fromMs: number, toMs: number) => {
    apply({ ...draft, range: "custom", from: toLocalInput(fromMs), to: toLocalInput(toMs) });
    setLive(false);
  };

  const currentView = viewsQ.data?.find((v) => v.id === viewId);

  return (
    <div className="space-y-6">
      <PageHeader
        title="Logs"
        description={
          <>
            De agents lezen de logs op hun node: bestanden onder /var/log, het systemd-journaal en de logs van containers. Kies nodes en
            bronnen, filter, en vergelijk de nodes van een cluster op één tijdlijn. ClusterForge bewaart de logs niet; wachtwoorden en
            tokens maskeert de agent.
          </>
        }
        actions={
          <ViewBar
            views={viewsQ.data ?? []}
            viewId={viewId}
            onLoad={(id) => {
              const v = viewsQ.data?.find((x) => x.id === id);
              if (v) loadView(v);
              else setViewId("");
            }}
            onSave={() => setSaving(true)}
            canSave={sources.length > 0}
            onDeleted={() => setViewId("")}
          />
        }
      />

      {nodesQ.isError && <Alert>{errText(nodesQ.error)}</Alert>}

      <SourcePicker
        nodes={nodes}
        picked={picked}
        setPicked={setPicked}
        sources={sources}
        setSources={setSources}
        nodeById={nodeById}
        minProtocol={nodesQ.data?.protocol ?? 34}
      />

      <FilterBar draft={draft} setDraft={setDraft} onApply={() => apply()} live={live} setLive={setLive} parseError={compileParse(draft.parse).error} />

      {sources.length === 0 ? (
        <Card>
          <p className="text-sm text-slate-500">
            Kies hierboven een of meer nodes en voeg een bron toe, bijvoorbeeld <code>/var/log/syslog</code> of het journaal van{" "}
            <code>keepalived.service</code>. Kies je alle nodes van een cluster, dan zie je hun logs naast elkaar.
          </p>
        </Card>
      ) : (
        <Results
          result={result.data}
          loading={result.isFetching}
          error={result.isError ? errText(result.error) : ""}
          sources={sources}
          filter={applied}
          parse={parse.re}
          mode={mode}
          setMode={setMode}
          focus={focus}
          setFocus={setFocus}
          onZoom={zoom}
          onRefresh={() => void result.refetch()}
          onPattern={(p) => apply({ ...draft, pattern: p, regex: false })}
        />
      )}

      {saving && (
        <SaveView
          current={currentView}
          sources={sources}
          filter={{ ...applied, mode }}
          nodeById={nodeById}
          onClose={() => setSaving(false)}
          onSaved={(v) => {
            setViewId(v.id);
            setSaving(false);
          }}
        />
      )}
    </div>
  );
}

// --- weergaven ---------------------------------------------------------------

function ViewBar({
  views,
  viewId,
  onLoad,
  onSave,
  canSave,
  onDeleted,
}: {
  views: LogView[];
  viewId: string;
  onLoad: (id: string) => void;
  onSave: () => void;
  canSave: boolean;
  onDeleted: () => void;
}) {
  const del = useDeleteLogView();
  return (
    <div className="flex flex-wrap items-center gap-2">
      <Select aria-label="Bewaarde weergave" className="w-56" value={viewId} onChange={(e) => onLoad(e.target.value)}>
        <option value="">Bewaarde weergaven…</option>
        {views.map((v) => (
          <option key={v.id} value={v.id}>
            {v.name}
          </option>
        ))}
      </Select>
      <Button variant="secondary" onClick={onSave} disabled={!canSave}>
        Weergave bewaren
      </Button>
      {viewId && (
        <Button
          variant="secondary-danger"
          disabled={del.isPending}
          onClick={() =>
            del.mutate(viewId, {
              onSuccess: onDeleted,
            })
          }
        >
          Verwijderen
        </Button>
      )}
    </div>
  );
}

function SaveView({
  current,
  sources,
  filter,
  nodeById,
  onClose,
  onSaved,
}: {
  current?: LogView;
  sources: LogSource[];
  filter: LogFilterState & { mode: Mode };
  nodeById: Map<string, LogNode>;
  onClose: () => void;
  onSaved: (v: LogView) => void;
}) {
  const save = useSaveLogView();
  const [name, setName] = useState(current?.name ?? "");
  const [asNew, setAsNew] = useState(!current);
  const clusters = new Set(sources.map((s) => nodeById.get(s.node_id)?.cluster_id ?? null));
  const clusterId = clusters.size === 1 ? [...clusters][0] : null;
  return (
    <Modal title="Weergave bewaren" onClose={onClose}>
      <form
        className="space-y-4"
        onSubmit={(e) => {
          e.preventDefault();
          save.mutate(
            { id: asNew ? undefined : current?.id, body: { name, cluster_id: clusterId, sources, filter: filter as unknown as Record<string, unknown> } },
            { onSuccess: onSaved },
          );
        }}
      >
        <div>
          <Label htmlFor="view-name">Naam</Label>
          <Input id="view-name" value={name} onChange={(e) => setName(e.target.value)} maxLength={100} autoFocus />
        </div>
        <p className="text-sm text-slate-500">
          {sources.length} bronnen met het huidige filter. Een relatief tijdvak (zoals &quot;1 uur&quot;) telt bij het openen vanaf dan.
        </p>
        {current && (
          <label className="flex items-center gap-2 text-sm">
            <input type="checkbox" checked={asNew} onChange={(e) => setAsNew(e.target.checked)} /> Als nieuwe weergave (anders wordt
            &quot;{current.name}&quot; aangepast)
          </label>
        )}
        {save.isError && <Alert>{errText(save.error)}</Alert>}
        <div className="flex justify-end gap-2">
          <Button type="button" variant="secondary" onClick={onClose}>
            Annuleren
          </Button>
          <Button type="submit" disabled={save.isPending || !name.trim()}>
            Bewaren
          </Button>
        </div>
      </form>
    </Modal>
  );
}

// --- bronnen kiezen -----------------------------------------------------------

function SourcePicker({
  nodes,
  picked,
  setPicked,
  sources,
  setSources,
  nodeById,
  minProtocol,
}: {
  nodes: LogNode[];
  picked: string[];
  setPicked: (ids: string[]) => void;
  sources: LogSource[];
  setSources: (s: LogSource[]) => void;
  nodeById: Map<string, LogNode>;
  minProtocol: number;
}) {
  const [kind, setKind] = useState<LogKind>("file");
  const [name, setName] = useState("/var/log/syslog");
  const [filterText, setFilterText] = useState("");
  const srcQs = useLogSources(picked);

  const groups = useMemo(() => {
    const m = new Map<string, { name: string; id: string | null; nodes: LogNode[] }>();
    for (const n of nodes) {
      const k = n.cluster_id ?? "";
      if (!m.has(k)) m.set(k, { name: n.cluster_name || "Zonder cluster", id: n.cluster_id ?? null, nodes: [] });
      m.get(k)!.nodes.push(n);
    }
    return [...m.values()];
  }, [nodes]);

  // De bronnen van de gekozen nodes samen, met op hoeveel nodes ze staan.
  const options = useMemo(() => {
    const count = new Map<string, number>();
    const add = (k: string) => count.set(k, (count.get(k) ?? 0) + 1);
    for (const q of srcQs) {
      const d = q.data;
      if (!d) continue;
      if (kind === "file") d.files.forEach((f) => add(f.path));
      if (kind === "journal") {
        if (d.journal) add("");
        d.units.forEach(add);
      }
      if (kind === "docker") d.containers.forEach((c) => add(c.name));
    }
    return [...count.entries()].sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]));
  }, [srcQs, kind]);

  const errors = srcQs
    .map((q, i) => (q.isError ? `${nodeById.get(picked[i])?.hostname ?? picked[i]}: ${errText(q.error)}` : ""))
    .filter(Boolean);

  const toggle = (id: string) => setPicked(picked.includes(id) ? picked.filter((x) => x !== id) : [...picked, id]);

  const addSource = (k: LogKind, n: string) => {
    const have = new Set(sources.map(sourceKey));
    const next = [...sources];
    for (const id of picked) {
      const s = { node_id: id, kind: k, name: n };
      if (!have.has(sourceKey(s))) next.push(s);
    }
    setSources(next.slice(0, 12));
  };

  const shown = filterText
    ? options.filter(([o]) => o.toLowerCase().includes(filterText.toLowerCase()))
    : options;

  return (
    <Card title="Bronnen">
      <div className="space-y-4">
        <div className="space-y-3">
          {groups.map((g) => {
            const usable = g.nodes.filter((n) => n.supported);
            const all = usable.length > 0 && usable.every((n) => picked.includes(n.id));
            return (
              <div key={g.id ?? "geen"} className="flex flex-wrap items-center gap-2">
                <span className="w-40 shrink-0 truncate text-sm font-medium" title={g.name}>
                  {g.name}
                </span>
                {g.nodes.map((n) => (
                  <label
                    key={n.id}
                    title={
                      !n.supported
                        ? `Agent te oud (protocol ${n.protocol}, ${minProtocol} nodig): Nodes → Agents bijwerken`
                        : !n.online
                          ? "Agent offline"
                          : n.primary_ip
                    }
                    className={cx(
                      "flex cursor-pointer items-center gap-1.5 rounded-md border px-2 py-1 text-sm",
                      picked.includes(n.id)
                        ? "border-brand-500 bg-brand-50 dark:bg-slate-800"
                        : "border-slate-300 dark:border-slate-700",
                      !n.supported && "cursor-not-allowed opacity-50",
                    )}
                  >
                    <input type="checkbox" disabled={!n.supported} checked={picked.includes(n.id)} onChange={() => toggle(n.id)} />
                    {n.hostname}
                    {n.holds_vip && <Badge tone="blue">VIP</Badge>}
                    {!n.online && <Badge tone="red">offline</Badge>}
                  </label>
                ))}
                {usable.length > 1 && (
                  <Button
                    variant="ghost"
                    className="px-2 py-1"
                    onClick={() =>
                      setPicked(
                        all
                          ? picked.filter((id) => !usable.some((n) => n.id === id))
                          : [...new Set([...picked, ...usable.map((n) => n.id)])],
                      )
                    }
                  >
                    {all ? "Niets van dit cluster" : "Heel het cluster"}
                  </Button>
                )}
              </div>
            );
          })}
          {nodes.length === 0 && <p className="text-sm text-slate-500">Nog geen nodes met een agent.</p>}
          {nodes.some((n) => !n.supported) && (
            <p className="text-xs text-slate-500">
              Grijze nodes hebben een oudere agent. Werk ze bij via{" "}
              <Link className="underline" href="/nodes">
                Nodes → Agents bijwerken
              </Link>
              .
            </p>
          )}
        </div>

        {picked.length > 0 && (
          <div className="space-y-2 border-t border-slate-200 pt-4 dark:border-slate-800">
            <div className="flex flex-wrap items-end gap-2">
              <div>
                <Label htmlFor="src-kind">Soort</Label>
                <Select
                  id="src-kind"
                  className="w-40"
                  value={kind}
                  onChange={(e) => {
                    const k = e.target.value as LogKind;
                    setKind(k);
                    setName(k === "file" ? "/var/log/syslog" : "");
                  }}
                >
                  <option value="file">Bestand</option>
                  <option value="journal">Journaal</option>
                  <option value="docker">Container</option>
                </Select>
              </div>
              <div className="min-w-64 flex-1">
                <Label htmlFor="src-name">{kind === "file" ? "Pad" : kind === "journal" ? "Unit (leeg = alles)" : "Container"}</Label>
                <Input
                  id="src-name"
                  list="src-options"
                  value={name}
                  placeholder={kind === "journal" ? "bv. keepalived.service" : kind === "docker" ? "bv. nginx" : "/var/log/…"}
                  onChange={(e) => {
                    setName(e.target.value);
                    setFilterText(e.target.value);
                  }}
                />
                <datalist id="src-options">
                  {options.map(([o]) => (
                    <option key={o} value={o} />
                  ))}
                </datalist>
              </div>
              <Button
                onClick={() => addSource(kind, name.trim())}
                disabled={(kind !== "journal" && !name.trim()) || sources.length >= 12}
              >
                Toevoegen voor {picked.length} {picked.length === 1 ? "node" : "nodes"}
              </Button>
            </div>
            {srcQs.some((q) => q.isLoading) && <p className="text-xs text-slate-500">Bronnen opvragen bij de agents…</p>}
            {errors.map((e) => (
              <Alert key={e}>{e}</Alert>
            ))}
            {shown.length > 0 && (
              <div className="flex max-h-40 flex-wrap gap-1.5 overflow-y-auto">
                {shown.slice(0, 80).map(([o, c]) => (
                  <button
                    key={o}
                    type="button"
                    onClick={() => addSource(kind, o)}
                    className="rounded border border-slate-200 px-2 py-0.5 font-mono text-xs hover:bg-slate-50 dark:border-slate-700 dark:hover:bg-slate-800"
                    title={`Toevoegen; staat op ${c} van ${picked.length} nodes`}
                  >
                    {kind === "journal" && o === "" ? "(heel het journaal)" : o}
                    {picked.length > 1 && <span className="ml-1 text-slate-400">{c}/{picked.length}</span>}
                  </button>
                ))}
              </div>
            )}
          </div>
        )}

        {sources.length > 0 && (
          <div className="flex flex-wrap items-center gap-2 border-t border-slate-200 pt-4 dark:border-slate-800">
            {sources.map((s, i) => (
              <span
                key={sourceKey(s)}
                className="inline-flex items-center gap-1.5 rounded-md border border-slate-200 py-0.5 pr-1 pl-2 text-xs dark:border-slate-700"
                style={{ borderLeft: `4px solid ${colorOf(i)}` }}
              >
                <strong>{nodeById.get(s.node_id)?.hostname ?? "?"}</strong>
                <span className="font-mono">{sourceLabel(s.kind, s.name)}</span>
                <button
                  type="button"
                  aria-label="Bron weghalen"
                  className="rounded px-1 text-slate-400 hover:bg-slate-100 hover:text-slate-700 dark:hover:bg-slate-800"
                  onClick={() => setSources(sources.filter((_, j) => j !== i))}
                >
                  ×
                </button>
              </span>
            ))}
            <Button variant="ghost" className="px-2 py-1 text-xs" onClick={() => setSources([])}>
              Alles weghalen
            </Button>
            <span className="text-xs text-slate-500">hoogstens 12 bronnen</span>
          </div>
        )}
      </div>
    </Card>
  );
}

// --- filter ---------------------------------------------------------------------

function FilterBar({
  draft,
  setDraft,
  onApply,
  live,
  setLive,
  parseError,
}: {
  draft: LogFilterState;
  setDraft: (f: LogFilterState) => void;
  onApply: () => void;
  live: boolean;
  setLive: (b: boolean) => void;
  parseError: string;
}) {
  const set = (p: Partial<LogFilterState>) => setDraft({ ...draft, ...p });
  const toggleLevel = (l: Level) =>
    set({ levels: draft.levels.includes(l) ? draft.levels.filter((x) => x !== l) : [...draft.levels, l] });
  return (
    <Card title="Filter">
      <form
        className="space-y-3"
        onSubmit={(e) => {
          e.preventDefault();
          onApply();
        }}
      >
        <div className="flex flex-wrap items-end gap-3">
          <div>
            <Label htmlFor="f-range">Tijdvak</Label>
            <Select
              id="f-range"
              className="w-40"
              value={draft.range}
              onChange={(e) => {
                const r = e.target.value as RangeId;
                if (r === "custom" && !draft.from) {
                  set({ range: r, from: toLocalInput(Date.now() - 3_600_000), to: toLocalInput(Date.now()) });
                } else set({ range: r });
              }}
            >
              {ranges.map((r) => (
                <option key={r.id} value={r.id}>
                  {r.id === "all" ? "Alles (nieuwste regels)" : r.id === "custom" ? r.label : `Laatste ${r.label}`}
                </option>
              ))}
            </Select>
          </div>
          {draft.range === "custom" && (
            <>
              <div>
                <Label htmlFor="f-from">Van</Label>
                <Input id="f-from" type="datetime-local" step={1} value={draft.from ?? ""} onChange={(e) => set({ from: e.target.value })} />
              </div>
              <div>
                <Label htmlFor="f-to">Tot</Label>
                <Input id="f-to" type="datetime-local" step={1} value={draft.to ?? ""} onChange={(e) => set({ to: e.target.value })} />
              </div>
            </>
          )}
          <div className="min-w-56 flex-1">
            <Label htmlFor="f-pattern">Zoeken</Label>
            <Input
              id="f-pattern"
              value={draft.pattern}
              placeholder={draft.regex ? "bv. WSREP|wsrep_.*fail" : "bv. Failed password"}
              onChange={(e) => set({ pattern: e.target.value })}
            />
          </div>
          <div className="min-w-48 flex-1">
            <Label htmlFor="f-exclude">Weglaten</Label>
            <Input id="f-exclude" value={draft.exclude} placeholder="bv. healthcheck" onChange={(e) => set({ exclude: e.target.value })} />
          </div>
          <div>
            <Label htmlFor="f-limit">Regels per bron</Label>
            <Select id="f-limit" className="w-28" value={draft.limit} onChange={(e) => set({ limit: Number(e.target.value) })}>
              {[100, 200, 500, 1000, 2000, 5000].map((n) => (
                <option key={n} value={n}>
                  {n}
                </option>
              ))}
            </Select>
          </div>
        </div>
        <div className="flex flex-wrap items-center gap-x-4 gap-y-2 text-sm">
          <label className="flex items-center gap-1.5">
            <input type="checkbox" checked={draft.regex} onChange={(e) => set({ regex: e.target.checked })} /> Reguliere expressie
          </label>
          <label className="flex items-center gap-1.5">
            <input type="checkbox" checked={!draft.ignoreCase} onChange={(e) => set({ ignoreCase: !e.target.checked })} /> Hoofdlettergevoelig
          </label>
          <span className="text-slate-400">|</span>
          <span className="text-slate-600 dark:text-slate-400">Niveau:</span>
          {levels.map((l) => (
            <label key={l || "none"} className="flex items-center gap-1.5">
              <input type="checkbox" checked={draft.levels.includes(l)} onChange={() => toggleLevel(l)} />
              <Badge tone={levelTone(l)}>{levelLabel[l]}</Badge>
            </label>
          ))}
          {draft.levels.length === 0 && <span className="text-xs text-slate-500">(geen vinkje = alle)</span>}
        </div>
        <div className="flex flex-wrap items-end gap-3">
          <div className="min-w-64 flex-1">
            <Label htmlFor="f-parse">Eigen ontleding (optioneel)</Label>
            <Input
              id="f-parse"
              className="font-mono"
              value={draft.parse}
              placeholder={'bv. "(?<methode>GET|POST) (?<pad>\\S+)[^"]*" (?<status>\\d{3})'}
              onChange={(e) => set({ parse: e.target.value })}
            />
            <p className="mt-1 text-xs text-slate-500">
              Reguliere expressie met benoemde groepen; elke groep wordt een kolom en een veld onder Velden. key=value en JSON ontleedt
              ClusterForge vanzelf.
            </p>
            {parseError && <p className="mt-1 text-xs text-red-600">{parseError}</p>}
          </div>
          <div className="flex gap-2">
            <Button type="submit">Toepassen</Button>
            <Button type="button" variant={live ? "primary" : "secondary"} onClick={() => setLive(!live)} title="Elke 10 seconden vernieuwen">
              {live ? "Live: aan" : "Live volgen"}
            </Button>
          </div>
        </div>
      </form>
    </Card>
  );
}

// --- resultaten ------------------------------------------------------------------

function Results({
  result,
  loading,
  error,
  sources,
  filter,
  parse,
  mode,
  setMode,
  focus,
  setFocus,
  onZoom,
  onRefresh,
  onPattern,
}: {
  result?: LogQueryResult;
  loading: boolean;
  error: string;
  sources: LogSource[];
  filter: LogFilterState;
  parse: RegExp | null;
  mode: Mode;
  setMode: (m: Mode) => void;
  focus: string;
  setFocus: (p: string) => void;
  onZoom: (from: number, to: number) => void;
  onRefresh: () => void;
  onPattern: (p: string) => void;
}) {
  // De kleur volgt de bron in de vraag; het antwoord kan van een vorige
  // vraag zijn (placeholder), dus via de sleutel.
  const colorIdx = useMemo(() => new Map(sources.map((s, i) => [sourceKey(s), i])), [sources]);
  const srcs = result?.sources ?? [];
  const color = (i: number) => {
    const s = srcs[i];
    return colorOf(s ? (colorIdx.get(`${s.node_id}|${s.kind}|${s.name}`) ?? i) : i);
  };
  const entries = useMemo(() => {
    const all = result?.entries ?? [];
    return focus ? all.filter((e) => patternOf(e.msg) === focus) : all;
  }, [result, focus]);

  const download = () => {
    const lines = entries.map((e) => {
      const s = srcs[e.source];
      return [e.ts ?? "", s?.hostname ?? "", s?.label ?? "", e.level || "-", e.proc || "-", e.msg.replace(/\n/g, "\\n")].join("\t");
    });
    const blob = new Blob([lines.join("\n") + "\n"], { type: "text/plain" });
    const a = document.createElement("a");
    a.href = URL.createObjectURL(blob);
    a.download = `logs-${new Date().toISOString().slice(0, 19).replace(/[:T]/g, "-")}.tsv`;
    a.click();
    URL.revokeObjectURL(a.href);
  };

  return (
    <div className="space-y-4">
      {error && <Alert>{error}</Alert>}
      {result && (
        <Card>
          <div className="space-y-3">
            <div className="flex flex-wrap items-center justify-between gap-3">
              <div className="flex flex-wrap items-center gap-2 text-sm">
                <strong>{result.entries.length} regels</strong>
                {(["error", "warn", "info", "debug", "none"] as const).map((l) =>
                  result.levels[l] ? (
                    <Badge key={l} tone={levelTone(l === "none" ? "" : l)}>
                      {levelLabel[l === "none" ? "" : l]}: {result.levels[l]}
                    </Badge>
                  ) : null,
                )}
                {result.truncated && (
                  <span className="text-xs text-amber-700 dark:text-amber-300">
                    Er zijn meer regels dan getoond: verklein het tijdvak of verhoog het aantal per bron.
                  </span>
                )}
                {loading && <span className="text-xs text-slate-500">vernieuwen…</span>}
              </div>
              <div className="flex gap-2">
                <Button variant="secondary" onClick={onRefresh} disabled={loading}>
                  Vernieuwen
                </Button>
                <Button variant="secondary" onClick={download} disabled={entries.length === 0}>
                  Downloaden
                </Button>
              </div>
            </div>
            <div className="overflow-x-auto">
              <table className={tableClass}>
                <thead>
                  <tr>
                    <th className={thClass}>Node</th>
                    <th className={thClass}>Bron</th>
                    <th className={thClass}>Regels</th>
                    <th className={thClass}>Fouten</th>
                    <th className={thClass}>Van – tot</th>
                    <th className={thClass}></th>
                  </tr>
                </thead>
                <tbody>
                  {srcs.map((s, i) => {
                    const errs = result.entries.filter((e) => e.source === i && e.level === "error").length;
                    return (
                      <tr key={i} className="border-t border-slate-100 dark:border-slate-800">
                        <td className={tdClass}>
                          <span className="inline-flex items-center gap-2">
                            <span className="inline-block h-3 w-3 rounded-sm" style={{ background: color(i) }} />
                            <strong>{s.hostname}</strong>
                            {s.cluster_name && <span className="text-xs text-slate-500">{s.cluster_name}</span>}
                          </span>
                        </td>
                        <td className={cx(tdClass, "font-mono text-xs")}>{s.label}</td>
                        <td className={tdClass}>
                          {s.error ? (
                            <span className="text-red-700 dark:text-red-300">{s.error}</span>
                          ) : (
                            <>
                              {s.count}
                              {s.truncated && <span className="text-xs text-amber-700 dark:text-amber-300"> (meer)</span>}
                            </>
                          )}
                        </td>
                        <td className={tdClass}>{errs > 0 ? <Badge tone="red">{errs}</Badge> : "—"}</td>
                        <td className={cx(tdClass, "text-xs whitespace-nowrap text-slate-500")}>
                          {s.first ? `${fmtTime(s.first, true)} – ${fmtTime(s.last, true)}` : "—"}
                        </td>
                        <td className={tdClass}></td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </div>
            <Histogram entries={result.entries} sources={srcs.length} color={color} onZoom={onZoom} />
          </div>
        </Card>
      )}

      {result && (
        <div className="flex flex-wrap items-center gap-2">
          {modes.map((m) => (
            <Button key={m.id} variant={mode === m.id ? "primary" : "secondary"} title={m.hint} onClick={() => setMode(m.id)}>
              {m.label}
            </Button>
          ))}
          {focus && (
            <span className="inline-flex items-center gap-1 rounded-md bg-brand-50 px-2 py-1 font-mono text-xs dark:bg-slate-800">
              patroon: {focus.slice(0, 80)}
              <button type="button" className="px-1" aria-label="Patroon weghalen" onClick={() => setFocus("")}>
                ×
              </button>
            </span>
          )}
        </div>
      )}

      {result && mode === "tijdlijn" && (
        <Timeline entries={entries} srcs={srcs} color={color} filter={filter} parse={parse} onZoom={onZoom} />
      )}
      {result && mode === "naast" && <SideBySide entries={entries} srcs={srcs} color={color} filter={filter} />}
      {result && mode === "patronen" && (
        <Patterns
          entries={result.entries}
          srcs={srcs}
          color={color}
          onFocus={(p) => {
            setFocus(p);
            setMode("tijdlijn");
          }}
          onSearch={onPattern}
        />
      )}
      {result && mode === "velden" && <Fields entries={entries} srcs={srcs} color={color} parse={parse} />}
    </div>
  );
}

// Histogram: regels per tijdvak per bron; een klik zoomt in.
function Histogram({
  entries,
  sources,
  color,
  onZoom,
}: {
  entries: LogEntry[];
  sources: number;
  color: (i: number) => string;
  onZoom: (from: number, to: number) => void;
}) {
  const data = useMemo(() => {
    const ts = entries.filter((e) => e.ts).map((e) => new Date(e.ts!).getTime());
    if (ts.length < 2) return null;
    const min = Math.min(...ts);
    const max = Math.max(...ts);
    if (max <= min) return null;
    const n = 60;
    const step = (max - min) / n;
    const buckets = Array.from({ length: n }, () => ({ per: new Array<number>(sources).fill(0), errors: 0, total: 0 }));
    for (const e of entries) {
      if (!e.ts) continue;
      const b = Math.min(n - 1, Math.floor((new Date(e.ts).getTime() - min) / step));
      buckets[b].per[e.source] = (buckets[b].per[e.source] ?? 0) + 1;
      buckets[b].total++;
      if (e.level === "error") buckets[b].errors++;
    }
    const top = Math.max(...buckets.map((b) => b.total));
    return { min, max, step, buckets, top };
  }, [entries, sources]);
  if (!data) return null;
  return (
    <div>
      <div className="flex h-24 items-end gap-px" role="img" aria-label="Regels per tijdvak per bron">
        {data.buckets.map((b, i) => {
          const from = data.min + i * data.step;
          return (
            <button
              key={i}
              type="button"
              className="group relative flex h-full flex-1 flex-col-reverse"
              title={`${fmtTime(new Date(from).toISOString(), true)}: ${b.total} regels${b.errors ? `, ${b.errors} fouten` : ""}. Klik om in te zoomen.`}
              onClick={() => onZoom(from, from + data.step)}
            >
              {b.per.map((c, s) =>
                c > 0 ? (
                  <span key={s} style={{ height: `${(c / data.top) * 100}%`, background: color(s) }} className="block w-full opacity-80 group-hover:opacity-100" />
                ) : null,
              )}
              {b.errors > 0 && <span className="absolute top-0 left-1/2 h-1.5 w-1.5 -translate-x-1/2 rounded-full bg-red-600" />}
            </button>
          );
        })}
      </div>
      <div className="mt-1 flex justify-between text-xs text-slate-500">
        <span>{fmtTime(new Date(data.min).toISOString(), true)}</span>
        <span>rode stip = fouten · klik op een staaf om in te zoomen</span>
        <span>{fmtTime(new Date(data.max).toISOString(), true)}</span>
      </div>
    </div>
  );
}

const PAGE = 1000;

function useMultiDay(entries: LogEntry[]) {
  return useMemo(() => {
    const ts = entries.filter((e) => e.ts).map((e) => e.ts!.slice(0, 10));
    return ts.length > 0 && ts[0] !== ts[ts.length - 1];
  }, [entries]);
}

function Timeline({
  entries,
  srcs,
  color,
  filter,
  parse,
  onZoom,
}: {
  entries: LogEntry[];
  srcs: LogQueryResult["sources"];
  color: (i: number) => string;
  filter: LogFilterState;
  parse: RegExp | null;
  onZoom: (from: number, to: number) => void;
}) {
  const [newestFirst, setNewestFirst] = useState(true);
  const [count, setCount] = useState(PAGE);
  const [open, setOpen] = useState<number | null>(null);
  const multiDay = useMultiDay(entries);
  const hl = useMemo(() => highlighter(filter), [filter]);
  const groups = useMemo(() => {
    if (!parse) return [] as string[];
    const m = parse.source.matchAll(/\(\?<([A-Za-z][\w]*)>/g);
    return [...m].map((x) => x[1]);
  }, [parse]);
  const ordered = newestFirst ? [...entries].reverse() : entries;
  const shown = ordered.slice(0, count);
  return (
    <Card>
      <div className="mb-3 flex flex-wrap items-center gap-3 text-sm">
        <label className="flex items-center gap-1.5">
          <input type="checkbox" checked={newestFirst} onChange={(e) => setNewestFirst(e.target.checked)} /> Nieuwste bovenaan
        </label>
        <span className="text-xs text-slate-500">Klik op een regel voor alles en voor &quot;wat gebeurde er toen op de andere nodes&quot;.</span>
      </div>
      {entries.length === 0 ? (
        <p className="text-sm text-slate-500">Geen regels die passen.</p>
      ) : (
        <div className="overflow-x-auto">
          <table className="w-full text-left font-mono text-xs">
            <thead>
              <tr className="text-slate-500">
                <th className="px-2 py-1 font-medium">Tijd</th>
                <th className="px-2 py-1 font-medium">Node</th>
                <th className="px-2 py-1 font-medium">Niveau</th>
                <th className="px-2 py-1 font-medium">Programma</th>
                {groups.map((g) => (
                  <th key={g} className="px-2 py-1 font-medium">
                    {g}
                  </th>
                ))}
                <th className="px-2 py-1 font-medium">Bericht</th>
              </tr>
            </thead>
            <tbody>
              {shown.map((e, i) => {
                const s = srcs[e.source];
                const isOpen = open === i;
                const firstLine = e.msg.split("\n", 1)[0];
                const more = e.msg.length > firstLine.length;
                const f = groups.length > 0 ? fieldsOf(e.msg, parse) : {};
                return (
                  <Fragment key={i}>
                    <tr
                      className={cx(
                        "cursor-pointer border-t border-slate-100 align-top hover:bg-slate-50 dark:border-slate-800 dark:hover:bg-slate-800/60",
                        e.level === "error" && "bg-red-50/60 dark:bg-red-950/30",
                        e.level === "warn" && "bg-amber-50/50 dark:bg-amber-950/20",
                      )}
                      style={{ boxShadow: `inset 3px 0 0 ${color(e.source)}` }}
                      onClick={() => setOpen(isOpen ? null : i)}
                    >
                      <td className={cx("px-2 py-1 whitespace-nowrap", e.inherited && "text-slate-400")}>{fmtTime(e.ts, multiDay)}</td>
                      <td className="px-2 py-1 whitespace-nowrap" style={{ color: color(e.source) }}>
                        {s?.hostname}
                      </td>
                      <td className="px-2 py-1">{e.level && <Badge tone={levelTone(e.level)}>{e.level}</Badge>}</td>
                      <td className="max-w-32 truncate px-2 py-1 text-slate-500">{e.proc}</td>
                      {groups.map((g) => (
                        <td key={g} className="px-2 py-1 whitespace-nowrap">
                          {f[g] ?? ""}
                        </td>
                      ))}
                      <td className="px-2 py-1 break-all whitespace-pre-wrap">
                        <Highlight text={isOpen ? e.msg : firstLine} re={hl} />
                        {more && !isOpen && <span className="ml-1 text-slate-400">[+{e.msg.split("\n").length - 1} regels]</span>}
                      </td>
                    </tr>
                    {isOpen && (
                      <tr className="border-t border-slate-100 dark:border-slate-800">
                        <td colSpan={5 + groups.length} className="bg-slate-50 px-2 py-2 font-sans dark:bg-slate-800/60">
                          <div className="flex flex-wrap items-center gap-2 text-xs">
                            <span>
                              {s?.hostname} · {s?.label} · {e.ts ? new Date(e.ts).toLocaleString("nl-BE") : "zonder tijd"}
                            </span>
                            {e.ts && (
                              <>
                                <Button
                                  variant="secondary"
                                  className="px-2 py-1 text-xs"
                                  onClick={() => {
                                    const t = new Date(e.ts!).getTime();
                                    onZoom(t - 60_000, t + 60_000);
                                  }}
                                >
                                  ± 1 minuut op alle bronnen
                                </Button>
                                <Button
                                  variant="secondary"
                                  className="px-2 py-1 text-xs"
                                  onClick={() => {
                                    const t = new Date(e.ts!).getTime();
                                    onZoom(t - 10 * 60_000, t + 10 * 60_000);
                                  }}
                                >
                                  ± 10 minuten
                                </Button>
                              </>
                            )}
                            <Button variant="ghost" className="px-2 py-1 text-xs" onClick={() => void navigator.clipboard?.writeText(e.msg)}>
                              Kopiëren
                            </Button>
                          </div>
                          {Object.keys(fieldsOf(e.msg, parse)).length > 0 && (
                            <div className="mt-2 flex flex-wrap gap-1">
                              {Object.entries(fieldsOf(e.msg, parse)).map(([k, v]) => (
                                <span key={k} className="rounded bg-white px-1.5 py-0.5 font-mono text-xs dark:bg-slate-900">
                                  <span className="text-slate-500">{k}=</span>
                                  {v}
                                </span>
                              ))}
                            </div>
                          )}
                        </td>
                      </tr>
                    )}
                  </Fragment>
                );
              })}
            </tbody>
          </table>
          {ordered.length > count && (
            <div className="mt-3 text-center">
              <Button variant="secondary" onClick={() => setCount(count + PAGE)}>
                Meer tonen ({ordered.length - count} over)
              </Button>
            </div>
          )}
        </div>
      )}
    </Card>
  );
}

// SideBySide: een kolom per bron; elke regel staat in de kolom van zijn bron
// op de rij van zijn tijd, zodat je ziet wat waar tegelijk gebeurde.
function SideBySide({
  entries,
  srcs,
  color,
  filter,
}: {
  entries: LogEntry[];
  srcs: LogQueryResult["sources"];
  color: (i: number) => string;
  filter: LogFilterState;
}) {
  const [count, setCount] = useState(PAGE);
  const multiDay = useMultiDay(entries);
  const hl = useMemo(() => highlighter(filter), [filter]);
  const rows = [...entries].reverse().slice(0, count);
  return (
    <Card>
      <p className="mb-3 text-xs text-slate-500">Nieuwste bovenaan. Een lege cel: die bron schreef op dat moment niets.</p>
      <div className="max-h-[70vh] overflow-auto">
        <table className="w-full table-fixed font-mono text-xs" style={{ minWidth: `${8 + srcs.length * 16}rem` }}>
          <thead className="sticky top-0 bg-white dark:bg-slate-900">
            <tr>
              <th className="w-28 px-2 py-1 text-left font-medium text-slate-500">Tijd</th>
              {srcs.map((s, i) => (
                <th key={i} className="px-2 py-1 text-left font-medium" style={{ borderBottom: `3px solid ${color(i)}` }}>
                  {s.hostname} <span className="font-normal text-slate-500">{s.label}</span>
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {rows.map((e, i) => (
              <tr key={i} className="border-t border-slate-100 align-top dark:border-slate-800">
                <td className="px-2 py-1 whitespace-nowrap text-slate-500">{fmtTime(e.ts, multiDay)}</td>
                {srcs.map((_, si) => (
                  <td
                    key={si}
                    className={cx(
                      "px-2 py-1 break-all whitespace-pre-wrap",
                      si === e.source && e.level === "error" && "bg-red-50 dark:bg-red-950/40",
                      si === e.source && e.level === "warn" && "bg-amber-50 dark:bg-amber-950/30",
                    )}
                  >
                    {si === e.source && (
                      <>
                        {e.proc && <span className="text-slate-500">{e.proc}: </span>}
                        <Highlight text={e.msg.split("\n", 1)[0]} re={hl} />
                      </>
                    )}
                  </td>
                ))}
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {entries.length > count && (
        <div className="mt-3 text-center">
          <Button variant="secondary" onClick={() => setCount(count + PAGE)}>
            Meer tonen ({entries.length - count} over)
          </Button>
        </div>
      )}
    </Card>
  );
}

// Patterns: dezelfde soort regel (getallen, adressen en id's weggelaten)
// samen, geteld per bron. Een patroon dat maar op één node staat is vaak
// precies het verschil dat je zoekt.
function Patterns({
  entries,
  srcs,
  color,
  onFocus,
  onSearch,
}: {
  entries: LogEntry[];
  srcs: LogQueryResult["sources"];
  color: (i: number) => string;
  onFocus: (p: string) => void;
  onSearch: (p: string) => void;
}) {
  const [onlyDiff, setOnlyDiff] = useState(false);
  const live = srcs.map((s, i) => (!s.error ? i : -1)).filter((i) => i >= 0);
  const rows = useMemo(() => {
    const m = new Map<string, { pattern: string; per: number[]; total: number; level: string; example: string; last?: string; proc: string }>();
    for (const e of entries) {
      const p = patternOf(e.msg);
      let r = m.get(p);
      if (!r) {
        r = { pattern: p, per: new Array<number>(srcs.length).fill(0), total: 0, level: "", example: e.msg.split("\n", 1)[0], proc: e.proc };
        m.set(p, r);
      }
      r.per[e.source]++;
      r.total++;
      if ((levelRank[e.level] ?? 0) > (levelRank[r.level] ?? 0)) r.level = e.level;
      if (e.ts) r.last = e.ts;
    }
    return [...m.values()].sort((a, b) => (levelRank[b.level] ?? 0) - (levelRank[a.level] ?? 0) || b.total - a.total);
  }, [entries, srcs.length]);
  const shown = onlyDiff && live.length > 1 ? rows.filter((r) => live.some((i) => r.per[i] === 0)) : rows;
  return (
    <Card>
      <div className="mb-3 flex flex-wrap items-center gap-3 text-sm">
        <span>
          {rows.length} patronen in {entries.length} regels
        </span>
        {live.length > 1 && (
          <label className="flex items-center gap-1.5">
            <input type="checkbox" checked={onlyDiff} onChange={(e) => setOnlyDiff(e.target.checked)} /> Alleen wat niet op elke bron staat
          </label>
        )}
      </div>
      <div className="overflow-x-auto">
        <table className={tableClass}>
          <thead>
            <tr>
              <th className={thClass}>Niveau</th>
              <th className={thClass}>Patroon</th>
              {srcs.map((s, i) => (
                <th key={i} className={cx(thClass, "text-right")} style={{ color: color(i) }} title={s.label}>
                  {s.hostname}
                </th>
              ))}
              <th className={thClass}>Laatst</th>
              <th className={thClass}></th>
            </tr>
          </thead>
          <tbody>
            {shown.slice(0, 500).map((r) => {
              const on = live.filter((i) => r.per[i] > 0);
              return (
                <tr key={r.pattern} className="border-t border-slate-100 align-top dark:border-slate-800">
                  <td className={tdClass}>{r.level && <Badge tone={levelTone(r.level)}>{r.level}</Badge>}</td>
                  <td className={cx(tdClass, "max-w-xl")}>
                    <div className="font-mono text-xs break-all">
                      {r.proc && <span className="text-slate-500">{r.proc}: </span>}
                      {r.pattern}
                    </div>
                    <div className="mt-0.5 truncate font-mono text-xs text-slate-400" title={r.example}>
                      bv. {r.example}
                    </div>
                    {live.length > 1 && on.length === 1 && (
                      <Badge tone="amber">alleen op {srcs[on[0]].hostname}</Badge>
                    )}
                  </td>
                  {srcs.map((_, i) => (
                    <td key={i} className={cx(tdClass, "text-right tabular-nums", r.per[i] === 0 && "text-slate-300 dark:text-slate-600")}>
                      {r.per[i]}
                    </td>
                  ))}
                  <td className={cx(tdClass, "text-xs whitespace-nowrap text-slate-500")}>{fmtTime(r.last, true)}</td>
                  <td className={cx(tdClass, "whitespace-nowrap")}>
                    <Button variant="ghost" className="px-2 py-1 text-xs" onClick={() => onFocus(r.pattern)}>
                      Regels
                    </Button>
                    <Button
                      variant="ghost"
                      className="px-2 py-1 text-xs"
                      title="Zoek de vaste tekst van dit patroon opnieuw op de nodes (ook buiten de getoonde regels)"
                      onClick={() => {
                        const fixed = r.pattern.split(/<[a-z*]+>|"<\*>"|'<\*>'/).sort((a, b) => b.length - a.length)[0]?.trim();
                        if (fixed) onSearch(fixed);
                      }}
                    >
                      Zoeken
                    </Button>
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
        {shown.length > 500 && <p className="mt-2 text-xs text-slate-500">De eerste 500 patronen.</p>}
      </div>
    </Card>
  );
}

// Fields: de velden die ClusterForge uit de regels haalt (key=value, JSON,
// eigen ontleding) met hun meest voorkomende waarden per bron.
function Fields({
  entries,
  srcs,
  color,
  parse,
}: {
  entries: LogEntry[];
  srcs: LogQueryResult["sources"];
  color: (i: number) => string;
  parse: RegExp | null;
}) {
  const stats = useMemo(() => {
    const m = new Map<string, Map<string, number[]>>();
    for (const e of entries) {
      for (const [k, v] of Object.entries(fieldsOf(e.msg, parse))) {
        if (!m.has(k)) m.set(k, new Map());
        const vals = m.get(k)!;
        if (!vals.has(v)) vals.set(v, new Array<number>(srcs.length).fill(0));
        vals.get(v)![e.source]++;
      }
    }
    return [...m.entries()]
      .map(([k, vals]) => ({ key: k, vals, total: [...vals.values()].reduce((a, per) => a + per.reduce((x, y) => x + y, 0), 0) }))
      .sort((a, b) => b.total - a.total);
  }, [entries, srcs.length, parse]);
  const [sel, setSel] = useState("");
  const cur = stats.find((s) => s.key === sel) ?? stats[0];
  if (stats.length === 0) {
    return (
      <Card>
        <p className="text-sm text-slate-500">
          Geen velden gevonden. ClusterForge herkent key=value en JSON vanzelf; voor andere regels geef je onder Filter een eigen ontleding
          met benoemde groepen, zoals <code>(?&lt;status&gt;\d{3})</code>.
        </p>
      </Card>
    );
  }
  const values = cur
    ? [...cur.vals.entries()].map(([v, per]) => ({ v, per, total: per.reduce((a, b) => a + b, 0) })).sort((a, b) => b.total - a.total)
    : [];
  return (
    <Card>
      <div className="flex flex-col gap-4 md:flex-row">
        <div className="md:w-56 md:shrink-0">
          <p className="mb-2 text-xs text-slate-500">Velden</p>
          <div className="flex flex-wrap gap-1 md:flex-col">
            {stats.slice(0, 60).map((s) => (
              <button
                key={s.key}
                type="button"
                onClick={() => setSel(s.key)}
                className={cx(
                  "flex items-center justify-between gap-2 rounded px-2 py-1 text-left font-mono text-xs",
                  cur?.key === s.key ? "bg-brand-50 dark:bg-slate-800" : "hover:bg-slate-50 dark:hover:bg-slate-800",
                )}
              >
                <span className="truncate">{s.key}</span>
                <span className="text-slate-400">{s.total}</span>
              </button>
            ))}
          </div>
        </div>
        <div className="min-w-0 flex-1 overflow-x-auto">
          <p className="mb-2 text-sm">
            <code>{cur?.key}</code>: {values.length} verschillende waarden
          </p>
          <table className={tableClass}>
            <thead>
              <tr>
                <th className={thClass}>Waarde</th>
                {srcs.map((s, i) => (
                  <th key={i} className={cx(thClass, "text-right")} style={{ color: color(i) }}>
                    {s.hostname}
                  </th>
                ))}
                <th className={cx(thClass, "text-right")}>Totaal</th>
              </tr>
            </thead>
            <tbody>
              {values.slice(0, 50).map((r) => (
                <tr key={r.v} className="border-t border-slate-100 dark:border-slate-800">
                  <td className={cx(tdClass, "max-w-md font-mono text-xs break-all")}>{r.v || <em>(leeg)</em>}</td>
                  {r.per.map((c, i) => (
                    <td key={i} className={cx(tdClass, "text-right tabular-nums", c === 0 && "text-slate-300 dark:text-slate-600")}>
                      {c}
                    </td>
                  ))}
                  <td className={cx(tdClass, "text-right font-medium tabular-nums")}>{r.total}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </div>
    </Card>
  );
}
