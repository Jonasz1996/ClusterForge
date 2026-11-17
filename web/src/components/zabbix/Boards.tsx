"use client";

import { useQueries } from "@tanstack/react-query";
import { useMemo, useState, type ReactNode } from "react";
import { askConfirm } from "@/components/Confirm";
import { Markdown } from "@/components/Markdown";
import { Modal } from "@/components/Modal";
import { toast } from "@/components/Toast";
import { TimeChart } from "@/components/charts/TimeChart";
import { Alert, Button, Field, Input, Select, Textarea, cx } from "@/components/ui";
import { ProblemRow, nf } from "@/components/zabbix/Zabbix";
import { ApiError } from "@/lib/api/client";
import {
  boardInput,
  formatZabbix,
  panelTypeLabel,
  rangeLabel,
  seriesQuery,
  severities,
  severityLabel,
  since,
  useDeleteZabbixBoard,
  useSaveZabbixBoard,
  useZabbixHostItems,
  useZabbixLatest,
  type PanelType,
  type ZabbixDashboard,
  type ZabbixDashboardInput,
  type ZabbixLatestItem,
  type ZabbixOverview,
  type ZabbixPanel,
  type ZabbixRange,
} from "@/lib/zabbixMonitoring";

// Eigen dashboards (fase 33 M4): panelen met Zabbix-items die je zelf kiest,
// in een raster van vier kolommen. Bewaren kan alleen een admin.

const MAX_PANELS = 48;
const MAX_GRAPH_ITEMS = 6;

const spanClass: Record<number, string> = {
  1: "",
  2: "md:col-span-2",
  3: "md:col-span-2 xl:col-span-3",
  4: "md:col-span-2 xl:col-span-4",
};
// Hoogte van de inhoud van een paneel, in pixels.
const bodyHeight: Record<number, number> = { 1: 140, 2: 260, 3: 400 };
const heightLabel: Record<number, string> = { 1: "Klein", 2: "Middel", 3: "Groot" };
const refreshChoices = [
  { v: 0, label: "niet" },
  { v: 30, label: "elke 30 s" },
  { v: 60, label: "elke minuut" },
  { v: 300, label: "elke 5 min" },
  { v: 900, label: "elke 15 min" },
];

function errText(e: unknown) {
  return e instanceof ApiError ? e.message : "Er ging iets mis.";
}

export function emptyPanel(type: PanelType = "graph"): ZabbixPanel {
  return { id: "", type, title: "", w: type === "graph" ? 2 : 1, h: type === "text" || type === "stat" || type === "gauge" ? 1 : 2, items: [], range: "24h", warn: null, crit: null, max: null, min_severity: 2, hostid: "", text: "" };
}

// numberOf leest de laatste waarde van een item als getal (of null).
function numberOf(it: ZabbixLatestItem | undefined) {
  if (!it || !it.numeric || it.error || it.last_value.trim() === "") return null;
  const v = Number(it.last_value);
  return Number.isFinite(v) ? v : null;
}

function tone(v: number | null, p: ZabbixPanel): "red" | "amber" | "green" | null {
  if (v == null || (p.warn == null && p.crit == null)) return null;
  if (p.crit != null && v >= p.crit) return "red";
  if (p.warn != null && v >= p.warn) return "amber";
  return "green";
}

const toneText = { red: "text-red-600 dark:text-red-400", amber: "text-amber-600 dark:text-amber-400", green: "text-emerald-600 dark:text-emerald-400" };
const toneColor = { red: "#ef4444", amber: "#f59e0b", green: "#10b981" };

function hostNames(o: ZabbixOverview) {
  return new Map(o.hosts.map((h) => [h.hostid, h.node?.hostname ?? h.name]));
}

// --- weergave ------------------------------------------------------------------------

// BoardGrid toont de panelen; frame geeft een paneel eventueel knoppen (bewerken).
export function BoardGrid({
  panels,
  o,
  controls,
}: {
  panels: ZabbixPanel[];
  o: ZabbixOverview;
  controls?: (i: number) => ReactNode;
}) {
  const ids = panels.filter((p) => p.type === "stat" || p.type === "gauge").flatMap((p) => p.items.map((i) => i.itemid));
  const latest = useZabbixLatest(ids);
  const byId = useMemo(() => new Map((latest.data ?? []).map((it) => [it.itemid, it])), [latest.data]);
  const names = useMemo(() => hostNames(o), [o]);
  if (panels.length === 0 && !controls) {
    return <p className="rounded-lg border border-dashed border-slate-300 p-8 text-center text-sm text-slate-500 dark:border-slate-700">Dit dashboard heeft nog geen panelen.</p>;
  }
  return (
    <div className="grid grid-cols-1 gap-4 md:grid-cols-2 xl:grid-cols-4">
      {panels.map((p, i) => (
        <section
          key={p.id || i}
          className={cx("flex min-w-0 flex-col rounded-lg border border-slate-200 bg-white p-4 shadow-xs dark:border-slate-800 dark:bg-slate-900", spanClass[p.w])}
        >
          <div className="mb-2 flex flex-wrap items-start justify-between gap-x-2 gap-y-1">
            <h2 className="min-w-0 flex-1 basis-40 truncate text-sm font-semibold" title={panelTitle(p, byId, names)}>
              {panelTitle(p, byId, names)}
            </h2>
            {controls?.(i)}
          </div>
          <div className="min-w-0 flex-1" style={{ minHeight: bodyHeight[p.h] }}>
            <PanelBody p={p} o={o} latest={byId} latestError={latest.error} names={names} />
          </div>
        </section>
      ))}
    </div>
  );
}

function panelTitle(p: ZabbixPanel, latest: Map<string, ZabbixLatestItem>, names: Map<string, string>) {
  if (p.title) return p.title;
  if (p.type === "problems") return p.hostid ? `Problemen van ${names.get(p.hostid) ?? "host"}` : "Open problemen";
  if (p.type === "text") return "";
  const it = latest.get(p.items[0]?.itemid ?? "");
  if (it && p.items.length === 1) return `${names.get(it.hostid) ?? it.host_name} · ${p.items[0].label || it.name}`;
  return panelTypeLabel[p.type as PanelType] ?? "Paneel";
}

function PanelBody({
  p,
  o,
  latest,
  latestError,
  names,
}: {
  p: ZabbixPanel;
  o: ZabbixOverview;
  latest: Map<string, ZabbixLatestItem>;
  latestError: unknown;
  names: Map<string, string>;
}) {
  switch (p.type) {
    case "graph":
      return <GraphBody p={p} names={names} />;
    case "stat":
    case "gauge":
      return <ValueBody p={p} it={latest.get(p.items[0]?.itemid ?? "")} error={latestError} names={names} />;
    case "problems":
      return <ProblemsBody p={p} o={o} />;
    case "text":
      return p.text ? <Markdown text={p.text} className="text-sm" /> : <p className="text-sm text-slate-500">Geen tekst.</p>;
    default:
      return <p className="text-sm text-slate-500">Onbekende soort paneel.</p>;
  }
}

// GraphBody legt de lijnen van meerdere items op één tijdas. De server legt
// de stukjes op vaste tijdstippen, zodat ze samenvallen.
function GraphBody({ p, names }: { p: ZabbixPanel; names: Map<string, string> }) {
  const range = (p.range || "24h") as ZabbixRange;
  const qs = useQueries({ queries: p.items.map((it) => seriesQuery(it.itemid, range)) });
  const data = qs.map((q) => q.data);
  const key = qs.map((q) => q.dataUpdatedAt).join(",");
  const merged = useMemo(() => {
    const ok = data.map((s, i) => ({ s, it: p.items[i] })).filter((x) => x.s);
    if (ok.length === 0) return null;
    const hosts = new Set(ok.map((x) => x.s!.hostid));
    const ts = [...new Set(ok.flatMap((x) => x.s!.timestamps))].sort((a, b) => a - b);
    const series = ok.map(({ s, it }) => {
      const at = new Map(s!.timestamps.map((t, i) => [t, s!.avg[i]]));
      const name = it.label || s!.name;
      return { label: hosts.size > 1 && !it.label ? `${names.get(s!.hostid) ?? s!.hostid}: ${name}` : name, values: ts.map((t) => at.get(t) ?? null) };
    });
    return { ts, series, units: ok[0].s!.units };
    // key verandert als een van de lijnen nieuwe data heeft.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key, p.items, names]);
  const units = merged?.units ?? "";
  const format = useMemo(() => (v: number) => formatZabbix(v, units), [units]);
  const errors = qs.map((q, i) => (q.error ? `${p.items[i].label || `item ${p.items[i].itemid}`}: ${errText(q.error)}` : null)).filter(Boolean);
  const height = bodyHeight[p.h] - 70;
  return (
    <div className="min-w-0">
      {!merged && qs.some((q) => q.isLoading) && <div className="flex items-center justify-center text-xs text-slate-500" style={{ height }}>Laden…</div>}
      {merged && <TimeChart timestamps={merged.ts} series={merged.series} unit={units === "%" ? "percent" : "count"} height={Math.max(height, 90)} format={format} formatKey={units} />}
      {errors.length > 0 && (
        <ul className="mt-1 space-y-0.5 text-xs text-red-600 dark:text-red-400">
          {errors.map((e) => (
            <li key={e}>{e}</li>
          ))}
        </ul>
      )}
      <p className="mt-1 text-right text-[11px] text-slate-500">{rangeLabel[range]}</p>
    </div>
  );
}

function ValueBody({ p, it, error, names }: { p: ZabbixPanel; it: ZabbixLatestItem | undefined; error: unknown; names: Map<string, string> }) {
  if (!it) {
    return <p className="text-sm text-slate-500">{error ? errText(error) : "Item niet gevonden (nog laden, of staat het niet meer in Zabbix?)."}</p>;
  }
  const v = numberOf(it);
  const t = tone(v, p);
  const sub = (
    <span className="text-xs text-slate-500">
      {names.get(it.hostid) ?? it.host_name}
      {it.last_clock && ` · ${since(it.last_clock)}`}
    </span>
  );
  if (it.error) return <p className="text-sm text-red-600 dark:text-red-400">{it.error}</p>;
  if (p.type === "gauge" && v != null) {
    return (
      <div className="flex flex-col items-center justify-center gap-1">
        <Meter value={v} max={p.max ?? 100} color={t ? toneColor[t] : "#3b82f6"} label={formatZabbix(v, it.units)} />
        {sub}
      </div>
    );
  }
  return (
    <div className="flex h-full flex-col justify-center">
      <span className={cx("text-4xl font-semibold tabular-nums break-words", t && toneText[t])}>
        {v != null ? formatZabbix(v, it.units) : it.last_value || "—"}
      </span>
      {sub}
    </div>
  );
}

// Meter is een halve boog van 0 tot max.
function Meter({ value, max, color, label }: { value: number; max: number; color: string; label: string }) {
  const c = Math.PI * 34;
  const f = Math.min(1, Math.max(0, value / (max || 1)));
  return (
    <svg viewBox="0 0 80 46" className="w-full max-w-[200px]" role="img" aria-label={label}>
      <path d="M6 40 A34 34 0 0 1 74 40" fill="none" stroke="currentColor" strokeWidth="8" className="text-slate-100 dark:text-slate-800" strokeLinecap="round" />
      <path d="M6 40 A34 34 0 0 1 74 40" fill="none" stroke={color} strokeWidth="8" strokeLinecap="round" strokeDasharray={`${f * c} ${c}`} />
      <text x="40" y="38" textAnchor="middle" className="fill-current text-[10px] font-semibold tabular-nums">
        {label}
      </text>
    </svg>
  );
}

function ProblemsBody({ p, o }: { p: ZabbixPanel; o: ZabbixOverview }) {
  const [now] = useState(() => Date.now());
  const list = o.problems
    .filter((x) => !x.suppressed && x.severity >= p.min_severity && (!p.hostid || x.hosts.some((h) => h.hostid === p.hostid)))
    .sort((a, b) => b.severity - a.severity || b.since.localeCompare(a.since));
  if (list.length === 0) return <p className="text-sm text-emerald-700 dark:text-emerald-400">Geen open problemen vanaf {severityLabel[p.min_severity]?.toLowerCase()}.</p>;
  return (
    <div className="overflow-y-auto" style={{ maxHeight: bodyHeight[p.h] }}>
      <ul className="-mx-2">
        {list.map((x) => (
          <ProblemRow key={x.eventid} p={x} base={o.frontend_url} now={now} />
        ))}
      </ul>
    </div>
  );
}

// --- bewerken ------------------------------------------------------------------------

function IconButton({ label, onClick, children, disabled }: { label: string; onClick: () => void; children: ReactNode; disabled?: boolean }) {
  return (
    <button
      type="button"
      title={label}
      aria-label={label}
      disabled={disabled}
      onClick={onClick}
      className="rounded px-1.5 py-0.5 text-sm text-slate-500 hover:bg-slate-100 hover:text-slate-900 disabled:opacity-30 dark:hover:bg-slate-800 dark:hover:text-slate-100"
    >
      {children}
    </button>
  );
}

// BoardEditor bewerkt een dashboard: naam, verversen en de panelen. Bewaren
// gaat pas naar de server met de knop.
export function BoardEditor({
  board,
  o,
  onDone,
}: {
  board: ZabbixDashboard | null;
  o: ZabbixOverview;
  onDone: (saved: ZabbixDashboard | null) => void;
}) {
  const [draft, setDraft] = useState<ZabbixDashboardInput>(() => (board ? boardInput(board) : { name: "", description: "", refresh: 60, panels: [] }));
  const [editing, setEditing] = useState<{ i: number | null; p: ZabbixPanel } | null>(null);
  const save = useSaveZabbixBoard();
  const set = (patch: Partial<ZabbixDashboardInput>) => setDraft((d) => ({ ...d, ...patch }));
  const setPanels = (f: (ps: ZabbixPanel[]) => ZabbixPanel[]) => setDraft((d) => ({ ...d, panels: f(d.panels) }));
  const move = (i: number, by: number) =>
    setPanels((ps) => {
      const j = i + by;
      if (j < 0 || j >= ps.length) return ps;
      const n = [...ps];
      [n[i], n[j]] = [n[j], n[i]];
      return n;
    });

  const submit = () =>
    save.mutate(
      { id: board?.id, body: draft },
      {
        onSuccess: (b) => {
          toast(board ? "Dashboard bewaard." : "Dashboard gemaakt.");
          onDone(b);
        },
      },
    );

  return (
    <div className="space-y-4">
      <div className="grid gap-3 rounded-lg border border-slate-200 bg-white p-4 md:grid-cols-[2fr_3fr_1fr] dark:border-slate-800 dark:bg-slate-900">
        <Field label="Naam" htmlFor="board-name">
          <Input id="board-name" value={draft.name} maxLength={80} onChange={(e) => set({ name: e.target.value })} placeholder="bv. Databanken" />
        </Field>
        <Field label="Beschrijving" htmlFor="board-desc">
          <Input id="board-desc" value={draft.description} maxLength={300} onChange={(e) => set({ description: e.target.value })} placeholder="Waarvoor dient dit dashboard?" />
        </Field>
        <Field label="Verversen" htmlFor="board-refresh">
          <Select id="board-refresh" value={draft.refresh} onChange={(e) => set({ refresh: Number(e.target.value) })}>
            {refreshChoices.map((r) => (
              <option key={r.v} value={r.v}>
                {r.label}
              </option>
            ))}
          </Select>
        </Field>
      </div>
      {save.error && <Alert>{errText(save.error)}</Alert>}
      <div className="flex flex-wrap items-center gap-2">
        <Button onClick={submit} disabled={save.isPending || draft.name.trim() === ""}>
          {save.isPending ? "Bewaren…" : "Bewaren"}
        </Button>
        <Button variant="secondary" onClick={() => onDone(null)} disabled={save.isPending}>
          Annuleren
        </Button>
        <Button variant="secondary" onClick={() => setEditing({ i: null, p: emptyPanel() })} disabled={draft.panels.length >= MAX_PANELS}>
          + Paneel
        </Button>
        <span className="text-xs text-slate-500">
          {draft.panels.length} van hoogstens {MAX_PANELS} panelen
        </span>
      </div>
      {draft.panels.length === 0 ? (
        <button
          type="button"
          onClick={() => setEditing({ i: null, p: emptyPanel() })}
          className="block w-full rounded-lg border border-dashed border-slate-300 p-10 text-center text-sm text-slate-500 hover:border-brand-500 hover:text-brand-700 dark:border-slate-700 dark:hover:text-sky-300"
        >
          Nog leeg. Voeg een eerste paneel toe: een grafiek, een getal, een meter, de open problemen of wat uitleg.
        </button>
      ) : (
        <BoardGrid
          panels={draft.panels}
          o={o}
          controls={(i) => (
            <div className="flex shrink-0 items-center">
              <IconButton label="Naar voren" onClick={() => move(i, -1)} disabled={i === 0}>
                ←
              </IconButton>
              <IconButton label="Naar achter" onClick={() => move(i, 1)} disabled={i === draft.panels.length - 1}>
                →
              </IconButton>
              <IconButton label="Paneel bewerken" onClick={() => setEditing({ i, p: draft.panels[i] })}>
                ✎
              </IconButton>
              <IconButton label="Paneel kopiëren" onClick={() => setPanels((ps) => (ps.length >= MAX_PANELS ? ps : [...ps.slice(0, i + 1), { ...ps[i], id: "" }, ...ps.slice(i + 1)]))}>
                ⧉
              </IconButton>
              <IconButton label="Paneel weghalen" onClick={() => setPanels((ps) => ps.filter((_, j) => j !== i))}>
                ✕
              </IconButton>
            </div>
          )}
        />
      )}
      {editing && (
        <PanelDialog
          initial={editing.p}
          o={o}
          onClose={() => setEditing(null)}
          onSave={(p) => {
            setPanels((ps) => (editing.i == null ? [...ps, p] : ps.map((x, j) => (j === editing.i ? p : x))));
            setEditing(null);
          }}
        />
      )}
    </div>
  );
}

function PanelDialog({ initial, o, onClose, onSave }: { initial: ZabbixPanel; o: ZabbixOverview; onClose: () => void; onSave: (p: ZabbixPanel) => void }) {
  const [p, setP] = useState<ZabbixPanel>(initial);
  const set = (patch: Partial<ZabbixPanel>) => setP((x) => ({ ...x, ...patch }));
  const type = p.type as PanelType;
  const usesItems = type === "graph" || type === "stat" || type === "gauge";
  const maxItems = type === "graph" ? MAX_GRAPH_ITEMS : 1;
  const hosts = useMemo(() => [...o.hosts].sort((a, b) => (a.node?.hostname ?? a.name).localeCompare(b.node?.hostname ?? b.name)), [o.hosts]);
  const problem =
    usesItems && p.items.length === 0
      ? "Kies minstens één item."
      : usesItems && p.items.length > maxItems
        ? `Hoogstens ${maxItems} item${maxItems > 1 ? "s" : ""} voor deze soort.`
        : type === "gauge" && p.max != null && p.max <= 0
          ? "Het maximum moet boven 0 liggen."
          : null;
  const num = (s: string) => (s.trim() === "" ? null : Number(s.replace(",", ".")));

  return (
    <Modal title={initial.id || initial.items.length || initial.text ? "Paneel bewerken" : "Paneel toevoegen"} onClose={onClose} wide>
      <div className="space-y-4">
        <div className="grid gap-3 sm:grid-cols-2">
          <Field label="Soort" htmlFor="panel-type">
            <Select
              id="panel-type"
              value={p.type}
              onChange={(e) => {
                const t = e.target.value as PanelType;
                set({ type: t, items: t === "graph" ? p.items : p.items.slice(0, 1) });
              }}
            >
              {(Object.keys(panelTypeLabel) as PanelType[]).map((t) => (
                <option key={t} value={t}>
                  {panelTypeLabel[t]}
                </option>
              ))}
            </Select>
          </Field>
          <Field label="Titel" htmlFor="panel-title" hint="Leeg: de naam van het item.">
            <Input id="panel-title" value={p.title} maxLength={80} onChange={(e) => set({ title: e.target.value })} />
          </Field>
          <Field label="Breedte" htmlFor="panel-w" hint="Op een gsm is elk paneel even breed.">
            <Select id="panel-w" value={p.w} onChange={(e) => set({ w: Number(e.target.value) })}>
              {[1, 2, 3, 4].map((w) => (
                <option key={w} value={w}>
                  {w === 4 ? "Volle breedte" : `${w} van 4 kolommen`}
                </option>
              ))}
            </Select>
          </Field>
          <Field label="Hoogte" htmlFor="panel-h">
            <Select id="panel-h" value={p.h} onChange={(e) => set({ h: Number(e.target.value) })}>
              {[1, 2, 3].map((h) => (
                <option key={h} value={h}>
                  {heightLabel[h]}
                </option>
              ))}
            </Select>
          </Field>
        </div>

        {usesItems && <ItemPicker hosts={hosts} items={p.items} max={maxItems} onChange={(items) => set({ items })} />}

        {type === "graph" && (
          <Field label="Venster" htmlFor="panel-range">
            <Select id="panel-range" value={p.range || "24h"} onChange={(e) => set({ range: e.target.value })}>
              {(Object.keys(rangeLabel) as ZabbixRange[]).map((r) => (
                <option key={r} value={r}>
                  laatste {rangeLabel[r]}
                </option>
              ))}
            </Select>
          </Field>
        )}

        {(type === "stat" || type === "gauge") && (
          <div className="grid gap-3 sm:grid-cols-3">
            <Field label="Oranje vanaf" htmlFor="panel-warn" hint="Leeg: geen kleur.">
              <Input id="panel-warn" inputMode="decimal" defaultValue={p.warn ?? ""} onChange={(e) => set({ warn: num(e.target.value) })} />
            </Field>
            <Field label="Rood vanaf" htmlFor="panel-crit">
              <Input id="panel-crit" inputMode="decimal" defaultValue={p.crit ?? ""} onChange={(e) => set({ crit: num(e.target.value) })} />
            </Field>
            {type === "gauge" && (
              <Field label="Volle meter" htmlFor="panel-max" hint="Leeg: 100 (voor procenten).">
                <Input id="panel-max" inputMode="decimal" defaultValue={p.max ?? ""} onChange={(e) => set({ max: num(e.target.value) })} />
              </Field>
            )}
          </div>
        )}

        {type === "problems" && (
          <div className="grid gap-3 sm:grid-cols-2">
            <Field label="Vanaf ernst" htmlFor="panel-sev">
              <Select id="panel-sev" value={p.min_severity} onChange={(e) => set({ min_severity: Number(e.target.value) })}>
                {[...severities].reverse().map((s) => (
                  <option key={s} value={s}>
                    {severityLabel[s]}
                  </option>
                ))}
              </Select>
            </Field>
            <Field label="Host" htmlFor="panel-host">
              <Select id="panel-host" value={p.hostid} onChange={(e) => set({ hostid: e.target.value })}>
                <option value="">Alle hosts</option>
                {hosts.map((h) => (
                  <option key={h.hostid} value={h.hostid}>
                    {h.node?.hostname ?? h.name}
                  </option>
                ))}
              </Select>
            </Field>
          </div>
        )}

        {type === "text" && (
          <Field label="Tekst" htmlFor="panel-text" hint="Markdown: **vet**, lijstjes, links.">
            <Textarea id="panel-text" rows={6} maxLength={2000} value={p.text} onChange={(e) => set({ text: e.target.value })} />
          </Field>
        )}

        {problem && <p className="text-sm text-amber-700 dark:text-amber-400">{problem}</p>}
        <div className="flex justify-end gap-2">
          <Button variant="secondary" onClick={onClose}>
            Annuleren
          </Button>
          <Button onClick={() => onSave(p)} disabled={!!problem}>
            Klaar
          </Button>
        </div>
      </div>
    </Modal>
  );
}

// ItemPicker kiest numerieke items: eerst de host, dan zoeken in zijn items.
function ItemPicker({
  hosts,
  items,
  max,
  onChange,
}: {
  hosts: ZabbixOverview["hosts"];
  items: ZabbixPanel["items"];
  max: number;
  onChange: (items: ZabbixPanel["items"]) => void;
}) {
  const [hostid, setHostid] = useState("");
  const [q, setQ] = useState("");
  const hi = useZabbixHostItems(hostid || undefined);
  const chosen = useZabbixLatest(items.map((i) => i.itemid));
  const info = new Map((chosen.data ?? []).map((it) => [it.itemid, it]));
  const names = new Map(hosts.map((h) => [h.hostid, h.node?.hostname ?? h.name]));
  const list = (hi.data?.items ?? []).filter((it) => it.numeric && !it.error && (q === "" || `${it.name} ${it.key}`.toLowerCase().includes(q.toLowerCase())));
  const has = (id: string) => items.some((i) => i.itemid === id);
  const toggle = (id: string) => {
    if (has(id)) onChange(items.filter((i) => i.itemid !== id));
    else if (max === 1) onChange([{ itemid: id, label: "" }]);
    else if (items.length < max) onChange([...items, { itemid: id, label: "" }]);
  };

  return (
    <div className="space-y-3">
      <div>
        <span className="mb-1 block text-sm font-medium text-slate-700 dark:text-slate-300">{max === 1 ? "Item" : `Items (hoogstens ${max})`}</span>
        {items.length === 0 ? (
          <p className="text-sm text-slate-500">Nog niets gekozen.</p>
        ) : (
          <ul className="space-y-1.5">
            {items.map((i, n) => {
              const it = info.get(i.itemid);
              return (
                <li key={i.itemid} className="flex flex-wrap items-center gap-2 rounded-md bg-slate-50 px-2 py-1.5 text-sm dark:bg-slate-800/60">
                  <span className="min-w-0 flex-1 truncate">
                    {it ? (
                      <>
                        <span className="text-slate-500">{names.get(it.hostid) ?? it.host_name} · </span>
                        {it.name}
                      </>
                    ) : (
                      <span className="text-slate-500">item {i.itemid}</span>
                    )}
                  </span>
                  {max > 1 && (
                    <Input
                      aria-label="Eigen naam in de legende"
                      placeholder="eigen naam (mag leeg)"
                      value={i.label}
                      maxLength={80}
                      onChange={(e) => onChange(items.map((x, j) => (j === n ? { ...x, label: e.target.value } : x)))}
                      className="!w-48 !py-1"
                    />
                  )}
                  <IconButton label="Weghalen" onClick={() => onChange(items.filter((_, j) => j !== n))}>
                    ✕
                  </IconButton>
                </li>
              );
            })}
          </ul>
        )}
      </div>
      <div className="grid gap-2 sm:grid-cols-2">
        <Select aria-label="Host" value={hostid} onChange={(e) => setHostid(e.target.value)}>
          <option value="">Kies een host…</option>
          {hosts.map((h) => (
            <option key={h.hostid} value={h.hostid}>
              {h.node?.hostname ?? h.name}
            </option>
          ))}
        </Select>
        <Input aria-label="Zoek een item" placeholder="zoek op naam of key" value={q} onChange={(e) => setQ(e.target.value)} disabled={!hostid} />
      </div>
      {hostid && (
        <div className="max-h-60 overflow-y-auto rounded-md border border-slate-200 dark:border-slate-800">
          {hi.isLoading && <p className="p-3 text-sm text-slate-500">Items laden…</p>}
          {hi.error && <p className="p-3 text-sm text-red-600 dark:text-red-400">{errText(hi.error)}</p>}
          {hi.data && list.length === 0 && <p className="p-3 text-sm text-slate-500">Geen numerieke items{q && " met die naam"}.</p>}
          <ul>
            {list.slice(0, 200).map((it) => (
              <li key={it.itemid}>
                <label className="flex cursor-pointer items-center gap-2 px-3 py-1.5 text-sm hover:bg-slate-50 dark:hover:bg-slate-800/60">
                  <input
                    type={max === 1 ? "radio" : "checkbox"}
                    name="panel-item"
                    checked={has(it.itemid)}
                    disabled={!has(it.itemid) && max > 1 && items.length >= max}
                    onChange={() => toggle(it.itemid)}
                  />
                  <span className="min-w-0 flex-1 truncate">{it.name}</span>
                  <span className="shrink-0 tabular-nums text-xs text-slate-500">{it.last_value !== "" && !Number.isNaN(Number(it.last_value)) ? formatZabbix(Number(it.last_value), it.units) : ""}</span>
                </label>
              </li>
            ))}
          </ul>
          {list.length > 200 && <p className="p-2 text-xs text-slate-500">Eerste 200 van {nf.format(list.length)}; zoek om te verfijnen.</p>}
        </div>
      )}
    </div>
  );
}

// --- beheer: nieuw, import, export, kopie, weg -------------------------------------------------

function slugify(s: string) {
  return (
    s
      .toLowerCase()
      .normalize("NFD")
      .replace(/[̀-ͯ]/g, "")
      .replace(/[^a-z0-9]+/g, "-")
      .replace(/^-|-$/g, "") || "dashboard"
  );
}

export function exportBoard(b: ZabbixDashboard) {
  const body = { clusterforge: "zabbix-dashboard", version: 1, ...boardInput(b) };
  const url = URL.createObjectURL(new Blob([JSON.stringify(body, null, 2)], { type: "application/json" }));
  const a = document.createElement("a");
  a.href = url;
  a.download = `zabbix-dashboard-${slugify(b.name)}.json`;
  a.click();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
}

// useBoardActions geeft kopiëren, importeren en weggooien, met meldingen.
export function useBoardActions(onOpen: (id: string | null) => void) {
  const save = useSaveZabbixBoard();
  const del = useDeleteZabbixBoard();
  const create = (body: ZabbixDashboardInput, done: string) =>
    save.mutate({ body }, { onSuccess: (b) => (toast(done), onOpen(b.id)), onError: (e) => toast(errText(e), "error") });
  return {
    busy: save.isPending || del.isPending,
    copy: (b: ZabbixDashboard) => create({ ...boardInput(b), name: `${b.name} (kopie)`.slice(0, 80) }, "Kopie gemaakt."),
    importFile: async (f: File) => {
      let v: unknown;
      try {
        v = JSON.parse(await f.text());
      } catch {
        toast("Dat is geen JSON-bestand.", "error");
        return;
      }
      create(boardInput(v), "Dashboard geïmporteerd.");
    },
    remove: async (b: ZabbixDashboard) => {
      if (!(await askConfirm(`Dashboard "${b.name}" weggooien? Dat kan niet ongedaan.`, { danger: true, confirmLabel: "Weggooien" }))) return;
      del.mutate(b.id, { onSuccess: () => (toast("Dashboard weggegooid."), onOpen(null)), onError: (e) => toast(errText(e), "error") });
    },
  };
}
