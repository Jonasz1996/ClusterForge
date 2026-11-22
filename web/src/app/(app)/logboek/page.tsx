"use client";

import { usePathname, useRouter, useSearchParams } from "next/navigation";
import { Suspense, useEffect, useMemo, useState } from "react";
import { AuditList } from "@/components/audit/AuditList";
import { Alert, Button, Card, Input, PageHeader, Select } from "@/components/ui";
import { exportUrl, useAudit, useAuditInfo, type AuditFilter } from "@/lib/audit";
import { useClusters, useIsAdmin, useNodes } from "@/lib/inventory";

export default function LogboekPage() {
  return (
    <Suspense>
      <LogboekInner />
    </Suspense>
  );
}

const periods = [
  { value: "", label: "Altijd" },
  { value: "24u", label: "Laatste 24 uur" },
  { value: "7d", label: "Laatste 7 dagen" },
  { value: "30d", label: "Laatste 30 dagen" },
  { value: "eigen", label: "Zelf kiezen" },
];

const periodMs: Record<string, number> = { "24u": 86_400_000, "7d": 7 * 86_400_000, "30d": 30 * 86_400_000 };

// Datums in de URL zijn ISO-tijdstippen; de datumvelden tonen de lokale dag.
const toDay = (iso?: string) => {
  if (!iso) return "";
  const d = new Date(iso);
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")}`;
};
const fromDay = (day: string, next = false) => {
  if (!day) return "";
  const [y, m, d] = day.split("-").map(Number);
  return new Date(y, m - 1, d + (next ? 1 : 0)).toISOString();
};

function LogboekInner() {
  const isAdmin = useIsAdmin();
  const params = useSearchParams();
  const router = useRouter();
  const pathname = usePathname();
  const get = (k: string) => params.get(k) ?? "";
  const filter: AuditFilter = {
    from: get("from"),
    to: get("to"),
    user: get("user"),
    actor_type: (get("actor_type") || undefined) as AuditFilter["actor_type"],
    cluster: get("cluster"),
    node: get("node"),
    job: get("job"),
    category: get("category"),
    q: get("q"),
  };
  const period = get("periode") || (filter.from || filter.to ? "eigen" : "");

  const set = (changes: Record<string, string>) => {
    const next = new URLSearchParams(params.toString());
    for (const [k, v] of Object.entries(changes)) {
      if (v) next.set(k, v);
      else next.delete(k);
    }
    const s = next.toString();
    router.replace(s ? `${pathname}?${s}` : pathname, { scroll: false });
  };

  const audit = useAudit(filter, isAdmin);
  const info = useAuditInfo(isAdmin);
  const clusters = useClusters();
  const nodes = useNodes();

  // De zoektekst gaat pas na een korte pauze in de URL. Verandert de URL op
  // een andere manier (een link, Filters wissen), dan volgt het veld.
  const urlQ = filter.q ?? "";
  const [search, setSearch] = useState(urlQ);
  const [seenQ, setSeenQ] = useState(urlQ);
  const [pushed, setPushed] = useState<string | null>(null);
  if (urlQ !== seenQ) {
    setSeenQ(urlQ);
    if (urlQ !== pushed) setSearch(urlQ);
  }
  useEffect(() => {
    if (search.trim() === urlQ) return;
    const t = setTimeout(() => {
      setPushed(search.trim());
      set({ q: search.trim() });
    }, 400);
    return () => clearTimeout(t);
  });

  const clusterOptions = useMemo(
    () => [
      ...(clusters.data ?? []).map((c) => ({ id: c.id, name: c.name })),
      ...(info.data?.deleted_clusters ?? []).map((c) => ({ id: c.id, name: `${c.name || "naamloos"} (verwijderd)` })),
    ],
    [clusters.data, info.data],
  );
  const nodeOptions = useMemo(
    () => [
      ...(nodes.data ?? [])
        .filter((n) => !filter.cluster || n.cluster_id === filter.cluster)
        .map((n) => ({ id: n.id, name: n.hostname })),
      ...(filter.cluster ? [] : (info.data?.deleted_nodes ?? [])).map((n) => ({ id: n.id, name: `${n.name || "naamloos"} (verwijderd)` })),
    ],
    [nodes.data, info.data, filter.cluster],
  );

  if (!isAdmin) {
    return (
      <div className="space-y-6">
        <PageHeader title="Logboek" />
        <Alert kind="info">Het logboek is alleen voor beheerders.</Alert>
      </div>
    );
  }

  const whoValue = filter.user ? `u:${filter.user}` : filter.actor_type ? `t:${filter.actor_type}` : "";
  const items = audit.data?.pages.flatMap((p) => p.items) ?? [];
  const filtered = Object.entries(filter).some(([, v]) => v);

  return (
    <div className="space-y-6">
      <PageHeader
        title="Logboek"
        description="Alles wat er in ClusterForge veranderde: wie, wat, wanneer en vanaf waar. Nieuwste eerst."
        actions={
          <a
            href={exportUrl(filter)}
            className="inline-flex items-center rounded-md border border-slate-300 bg-white px-3.5 py-2 text-sm font-medium text-slate-800 hover:bg-slate-50 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-100 dark:hover:bg-slate-800"
          >
            Exporteren
          </a>
        }
      />

      <Card>
        <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
          <Select
            aria-label="Periode"
            value={period}
            onChange={(e) => {
              const v = e.target.value;
              if (v === "eigen") set({ periode: "eigen" });
              else if (v === "") set({ periode: "", from: "", to: "" });
              else set({ periode: v, from: new Date(Date.now() - periodMs[v]).toISOString(), to: "" });
            }}
          >
            {periods.map((p) => (
              <option key={p.value} value={p.value}>
                {p.label}
              </option>
            ))}
          </Select>
          <Select
            aria-label="Wie"
            value={whoValue}
            onChange={(e) => {
              const [kind, v] = [e.target.value.slice(0, 2), e.target.value.slice(2)];
              set({ user: kind === "u:" ? v : "", actor_type: kind === "t:" ? v : "" });
            }}
          >
            <option value="">Iedereen</option>
            {(info.data?.users ?? []).map((u) => (
              <option key={u.id} value={`u:${u.id}`}>
                {u.username}
                {u.disabled ? " (uitgeschakeld)" : ""}
              </option>
            ))}
            <option value="t:system">Systeem</option>
            <option value="t:agent">Agents</option>
          </Select>
          <Select aria-label="Soort" value={filter.category} onChange={(e) => set({ category: e.target.value })}>
            <option value="">Alle soorten</option>
            {(info.data?.categories ?? []).map((c) => (
              <option key={c.key} value={c.key}>
                {c.label}
              </option>
            ))}
          </Select>
          <Select aria-label="Cluster" value={filter.cluster} onChange={(e) => set({ cluster: e.target.value, node: "" })}>
            <option value="">Alle clusters</option>
            {clusterOptions.map((c) => (
              <option key={c.id} value={c.id}>
                {c.name}
              </option>
            ))}
          </Select>
          <Select aria-label="Node" value={filter.node} onChange={(e) => set({ node: e.target.value })}>
            <option value="">Alle nodes</option>
            {nodeOptions.map((n) => (
              <option key={n.id} value={n.id}>
                {n.name}
              </option>
            ))}
          </Select>
          <Input
            type="search"
            aria-label="Zoeken"
            placeholder="Zoeken op naam, adres of fout"
            value={search}
            onChange={(e) => setSearch(e.target.value)}
          />
          {period === "eigen" && (
            <div className="flex items-center gap-2 sm:col-span-2">
              <Input
                type="date"
                aria-label="Van"
                value={toDay(filter.from)}
                onChange={(e) => set({ from: fromDay(e.target.value) })}
              />
              <span className="text-sm text-slate-500">tot en met</span>
              <Input
                type="date"
                aria-label="Tot en met"
                value={filter.to ? toDay(new Date(new Date(filter.to).getTime() - 1).toISOString()) : ""}
                onChange={(e) => set({ to: fromDay(e.target.value, true) })}
              />
            </div>
          )}
        </div>
        {filtered && (
          <div className="mt-3 flex flex-wrap items-center gap-3 text-sm text-slate-600 dark:text-slate-400">
            {filter.job && <span>Alleen regels van één taak.</span>}
            <Button
              variant="ghost"
              onClick={() => {
                setSearch("");
                router.replace(pathname, { scroll: false });
              }}
            >
              Filters wissen
            </Button>
          </div>
        )}
      </Card>

      <Card>
        {audit.isLoading && <p className="text-sm text-slate-500">Laden…</p>}
        {audit.isError && <Alert>{audit.error instanceof Error ? audit.error.message : "Laden mislukt"}</Alert>}
        {audit.data && <AuditList items={items} />}
        {audit.hasNextPage && (
          <div className="mt-4 flex justify-center">
            <Button variant="secondary" disabled={audit.isFetchingNextPage} onClick={() => void audit.fetchNextPage()}>
              {audit.isFetchingNextPage ? "Laden…" : "Meer laden"}
            </Button>
          </div>
        )}
        {info.data && (
          <p className="mt-4 text-xs text-slate-500">
            {info.data.total.toLocaleString("nl-BE")} regels in totaal
            {info.data.oldest && `, de oudste van ${new Date(info.data.oldest).toLocaleDateString("nl-BE")}`}. Regels kunnen
            niet gewijzigd of verwijderd worden.
          </p>
        )}
      </Card>
    </div>
  );
}
