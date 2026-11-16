"use client";

import Link from "next/link";
import { useRouter, useSearchParams } from "next/navigation";
import { Suspense, useMemo, useState } from "react";
import { Tabs } from "@/components/Tabs";
import { CVETable, FindingRow } from "@/components/security/FindingRow";
import { ago, tableClass, tdClass, thClass } from "@/components/inventory/bits";
import { Alert, Badge, Button, Card, Field, Input, PageHeader, Select, cx } from "@/components/ui";
import { useIsAdmin } from "@/lib/inventory";
import {
  isCVEDetail,
  severities,
  severityTone,
  useRefreshSecurity,
  useSecurity,
  useSecurityFindings,
  type FindingQuery,
  type SecurityFinding,
  type SecurityOverview,
  type Severity,
} from "@/lib/security";

const tabs = [
  { key: "bevindingen", label: "Bevindingen" },
  { key: "nodes", label: "Nodes" },
  { key: "feeds", label: "Bronnen" },
] as const;
type Tab = (typeof tabs)[number]["key"];

export default function VulnerabilitiesPage() {
  return (
    <Suspense>
      <Vulnerabilities />
    </Suspense>
  );
}

function Vulnerabilities() {
  const params = useSearchParams();
  const router = useRouter();
  const tab = (tabs.find((t) => t.key === params.get("tab"))?.key ?? "bevindingen") as Tab;
  const nodeParam = params.get("node") ?? "";
  const data = useSecurity();
  const isAdmin = useIsAdmin();
  const refresh = useRefreshSecurity();
  const setTab = (t: Tab) => router.replace(`/kwetsbaarheden?tab=${t}`);

  return (
    <div className="space-y-6">
      <PageHeader
        title="Kwetsbaarheden"
        description="Welke bekende kwetsbaarheden gelden voor de pakketten op je nodes, volgens de Debian Security Tracker, en welke daarvan volgens CISA actief misbruikt worden. De agents sturen alleen hun pakketlijst; er wordt niets gescand of aangevallen."
        actions={
          isAdmin && (
            <Button variant="secondary" onClick={() => refresh.mutate({ feeds: true, nodes: true })} disabled={refresh.isPending}>
              {refresh.isSuccess ? "Controle loopt…" : "Nu controleren"}
            </Button>
          )
        }
      />
      <Tabs tabs={tabs} value={tab} onChange={setTab} />
      {data.isError && <Alert>Kwetsbaarheden laden mislukte.</Alert>}
      {refresh.isError && <Alert>Controle starten mislukte.</Alert>}
      {data.data && <Notices o={data.data} goFeeds={() => setTab("feeds")} goNodes={() => setTab("nodes")} />}
      {data.data && tab === "bevindingen" && <FindingsTab o={data.data} isAdmin={isAdmin} initialNode={nodeParam} />}
      {data.data && tab === "nodes" && <NodesTab o={data.data} isAdmin={isAdmin} />}
      {data.data && tab === "feeds" && <FeedsTab o={data.data} />}
    </div>
  );
}

function Notices({ o, goFeeds, goNodes }: { o: SecurityOverview; goFeeds: () => void; goNodes: () => void }) {
  const broken = o.feeds.filter((f) => f.error && !f.ok_at);
  return (
    <>
      {broken.length > 0 && (
        <Alert>
          {broken.map((f) => f.label).join(" en ")} kon nog niet opgehaald worden.{" "}
          <button className="font-medium underline" onClick={goFeeds}>
            Bekijk de fout
          </button>
          . De server moet via HTTPS naar buiten kunnen.
        </Alert>
      )}
      {o.stats.nodes_old_agent > 0 && (
        <Alert kind="info">
          {o.stats.nodes_old_agent} {o.stats.nodes_old_agent === 1 ? "node heeft" : "nodes hebben"} een agent ouder dan protocol {o.protocol} en{" "}
          {o.stats.nodes_old_agent === 1 ? "wordt" : "worden"} nog niet gecontroleerd. Werk ze bij via{" "}
          <Link href="/nodes" className="font-medium underline">
            Nodes → Agents bijwerken
          </Link>
          .{" "}
          <button className="underline" onClick={goNodes}>
            Welke?
          </button>
        </Alert>
      )}
    </>
  );
}

function Tile({ label, value, tone, hint }: { label: string; value: string | number; tone?: "red" | "amber" | "green"; hint?: string }) {
  return (
    <div className="rounded-lg border border-slate-200 bg-white p-4 dark:border-slate-800 dark:bg-slate-900" title={hint}>
      <div className="text-xs font-medium uppercase tracking-wide text-slate-500">{label}</div>
      <div
        className={cx(
          "mt-1 text-2xl font-semibold tabular-nums",
          tone === "red" && "text-red-600 dark:text-red-400",
          tone === "amber" && "text-amber-600 dark:text-amber-400",
          tone === "green" && "text-emerald-600 dark:text-emerald-400",
        )}
      >
        {value}
      </div>
    </div>
  );
}

const statusOptions: {
  value: NonNullable<FindingQuery["status"]>;
  label: string;
}[] = [
  { value: "actueel", label: "Open en aanvaard" },
  { value: "open", label: "Open" },
  { value: "aanvaard", label: "Aanvaard" },
  { value: "opgelost", label: "Opgelost" },
  { value: "alle", label: "Alle" },
];

function FindingsTab({ o, isAdmin, initialNode }: { o: SecurityOverview; isAdmin: boolean; initialNode: string }) {
  const s = o.stats;
  const [status, setStatus] = useState<NonNullable<FindingQuery["status"]>>("actueel");
  const [node, setNode] = useState(initialNode);
  const [minSev, setMinSev] = useState<Severity>("laag");
  const [onlyFix, setOnlyFix] = useState(false);
  const [onlyKEV, setOnlyKEV] = useState(false);
  const [search, setSearch] = useState("");
  const [open, setOpen] = useState<string | null>(null);
  const [grouped, setGrouped] = useState(true);
  const findings = useSecurityFindings({ status, node_id: node || undefined, kind: "cve" });
  const rank = (x: Severity) => severities.length - severities.indexOf(x);

  const list = useMemo(() => {
    const q = search.trim().toLowerCase();
    return (findings.data ?? []).filter((f) => {
      if (rank(f.severity) < rank(minSev)) return false;
      const d = isCVEDetail(f.detail) ? f.detail : null;
      if (onlyFix && !(d && d.fixable > 0)) return false;
      if (onlyKEV && !(d && d.kev > 0)) return false;
      if (!q) return true;
      return (
        f.title.toLowerCase().includes(q) ||
        f.hostname.toLowerCase().includes(q) ||
        f.key.toLowerCase().includes(q) ||
        (d?.cves.some((c) => c.cve.toLowerCase().includes(q)) ?? false)
      );
    });
  }, [findings.data, minSev, onlyFix, onlyKEV, search]);

  const groups = useMemo(() => groupFindings(list), [list]);

  return (
    <div className="space-y-6">
      <div className="grid grid-cols-2 gap-3 md:grid-cols-3 xl:grid-cols-6">
        <Tile label="Open" value={s.open} tone={s.open > 0 ? "amber" : "green"} hint="Pakketten met minstens één bekende kwetsbaarheid, per node" />
        <Tile
          label="Kritiek"
          value={s.counts.kritiek ?? 0}
          tone={(s.counts.kritiek ?? 0) > 0 ? "red" : undefined}
          hint="Actief misbruikt volgens CISA"
        />
        <Tile label="Hoog" value={s.counts.hoog ?? 0} tone={(s.counts.hoog ?? 0) > 0 ? "red" : undefined} />
        <Tile label="Met een fix" value={s.fixable} tone={s.fixable > 0 ? "amber" : undefined} hint="Op te lossen met een update" />
        <Tile label="Opgelost 7 d" value={s.resolved_week} tone={s.resolved_week > 0 ? "green" : undefined} />
        <Tile label="Nodes gecontroleerd" value={`${s.nodes_checked}/${s.nodes_total}`} />
      </div>
      <Card>
        <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-5">
          <Field label="Zoeken" htmlFor="zoek">
            <Input id="zoek" value={search} onChange={(e) => setSearch(e.target.value)} placeholder="pakket, node of CVE" />
          </Field>
          <Field label="Node" htmlFor="node">
            <Select id="node" value={node} onChange={(e) => setNode(e.target.value)}>
              <option value="">Alle nodes</option>
              {o.nodes.map((n) => (
                <option key={n.node_id} value={n.node_id}>
                  {n.hostname}
                  {n.cluster_name ? ` (${n.cluster_name})` : ""}
                </option>
              ))}
            </Select>
          </Field>
          <Field label="Status" htmlFor="status">
            <Select id="status" value={status} onChange={(e) => setStatus(e.target.value as typeof status)}>
              {statusOptions.map((x) => (
                <option key={x.value} value={x.value}>
                  {x.label}
                </option>
              ))}
            </Select>
          </Field>
          <Field label="Minstens" htmlFor="ernst">
            <Select id="ernst" value={minSev} onChange={(e) => setMinSev(e.target.value as Severity)}>
              {severities.map((x) => (
                <option key={x} value={x}>
                  {x}
                </option>
              ))}
            </Select>
          </Field>
          <div className="flex flex-col justify-end gap-1 text-sm">
            <label className="flex items-center gap-2">
              <input type="checkbox" checked={onlyFix} onChange={(e) => setOnlyFix(e.target.checked)} /> alleen met een fix
            </label>
            <label className="flex items-center gap-2">
              <input type="checkbox" checked={onlyKEV} onChange={(e) => setOnlyKEV(e.target.checked)} /> alleen actief misbruikt
            </label>
          </div>
        </div>
      </Card>
      {findings.isError && <Alert>Bevindingen laden mislukte.</Alert>}
      {findings.data && list.length === 0 && (
        <Alert kind="success">
          {findings.data.length === 0 && s.nodes_checked === 0
            ? "Nog geen enkele node gecontroleerd. Zodra een agent met protocol 40 zijn pakketlijst stuurt en de feeds binnen zijn, verschijnen hier de bevindingen."
            : "Geen bevindingen met deze filters."}
        </Alert>
      )}
      {list.length > 0 && (
        <div className="space-y-2">
          <div className="flex flex-wrap items-center justify-between gap-2 text-sm text-slate-500">
            <span>
              {list.length} {list.length === 1 ? "bevinding" : "bevindingen"}
              {findings.data && list.length < findings.data.length ? ` van ${findings.data.length}` : ""}
              {grouped && ` in ${groups.length} ${groups.length === 1 ? "pakket" : "pakketten"}`}
            </span>
            <label className="flex items-center gap-2">
              <input type="checkbox" checked={grouped} onChange={(e) => setGrouped(e.target.checked)} /> per pakket samenvoegen
            </label>
          </div>
          {grouped
            ? groups.map((g) => (
                <GroupRow key={g.key} g={g} open={open === g.key} toggle={() => setOpen(open === g.key ? null : g.key)} isAdmin={isAdmin} />
              ))
            : list.map((f) => (
                <FindingRow key={f.id} f={f} open={open === f.id} toggle={() => setOpen(open === f.id ? null : f.id)} isAdmin={isAdmin} />
              ))}
        </div>
      )}
    </div>
  );
}

function NodesTab({ o, isAdmin }: { o: SecurityOverview; isAdmin: boolean }) {
  const refresh = useRefreshSecurity();
  return (
    <Card title="Nodes">
      <div className="overflow-x-auto">
        <table className={tableClass}>
          <thead>
            <tr>
              <th className={thClass}>Node</th>
              <th className={thClass}>Systeem</th>
              <th className={thClass}>Pakketten</th>
              <th className={thClass}>Gecontroleerd</th>
              <th className={thClass}>Bevindingen</th>
              <th className={thClass} />
            </tr>
          </thead>
          <tbody>
            {o.nodes.map((n) => (
              <tr key={n.node_id} className="border-t border-slate-100 dark:border-slate-800">
                <td className={tdClass}>
                  <Link href={`/nodes/detail?id=${n.node_id}`} className="font-medium hover:underline">
                    {n.hostname}
                  </Link>
                  <div className="text-xs text-slate-500">
                    {n.cluster_name || "geen cluster"}
                    {!n.online && " · offline"}
                  </div>
                </td>
                <td className={tdClass}>
                  {n.os ? `${n.os} ${n.codename}` : "?"}
                  {n.kernel && <div className="text-xs text-slate-500">kernel {n.kernel}</div>}
                </td>
                <td className={cx(tdClass, "tabular-nums")}>{n.packages || "–"}</td>
                <td className={tdClass}>
                  {n.collected_at ? ago(n.collected_at) : "nog niet"}
                  {n.note && <div className="text-xs text-amber-700 dark:text-amber-400">{n.note}</div>}
                </td>
                <td className={tdClass}>
                  {n.checked ? (
                    <Link href={`/kwetsbaarheden?node=${n.node_id}`} className="flex flex-wrap gap-1">
                      {severities
                        .filter((s) => (n.counts[s] ?? 0) > 0)
                        .map((s) => (
                          <Badge key={s} tone={severityTone[s]}>
                            {n.counts[s]} {s}
                          </Badge>
                        ))}
                      {severities.every((s) => !n.counts[s]) && <Badge tone="green">niets bekend</Badge>}
                    </Link>
                  ) : (
                    <span className="text-slate-400">–</span>
                  )}
                </td>
                <td className={cx(tdClass, "text-right")}>
                  {isAdmin && n.supported && n.online && (
                    <Button variant="secondary" onClick={() => refresh.mutate({ nodes: true, node_id: n.node_id })} disabled={refresh.isPending}>
                      Opnieuw
                    </Button>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </Card>
  );
}

function FeedsTab({ o }: { o: SecurityOverview }) {
  return (
    <div className="grid gap-4 md:grid-cols-2">
      {o.feeds.map((f) => (
        <Card key={f.name} title={f.label}>
          <dl className="space-y-1 text-sm">
            <div className="flex justify-between gap-4">
              <dt className="text-slate-500">Laatst gelukt</dt>
              <dd>{f.ok_at ? ago(f.ok_at) : "nog nooit"}</dd>
            </div>
            <div className="flex justify-between gap-4">
              <dt className="text-slate-500">Laatst geprobeerd</dt>
              <dd>{f.fetched_at ? ago(f.fetched_at) : "nog nooit"}</dd>
            </div>
            <div className="flex justify-between gap-4">
              <dt className="text-slate-500">Regels</dt>
              <dd className="tabular-nums">{f.entries.toLocaleString("nl-BE")}</dd>
            </div>
            <div className="break-all text-xs text-slate-500">{f.url}</div>
          </dl>
          {f.error && (
            <div className="mt-3">
              <Alert>{f.error}</Alert>
            </div>
          )}
        </Card>
      ))}
      <p className="text-sm text-slate-500 md:col-span-2">
        De server haalt beide bronnen elke dag op (na een fout elk uur opnieuw). De Security Tracker zegt per Debian-release in welke versie een
        kwetsbaarheid opgelost is; CISA houdt bij welke kwetsbaarheden in het wild misbruikt worden. Alleen Debian wordt gecontroleerd.
      </p>
    </div>
  );
}

type Group = { key: string; worst: SecurityFinding; items: SecurityFinding[] };

/** groupFindings voegt dezelfde bevinding op meerdere nodes samen (zelfde soort, pakket en versie). */
function groupFindings(list: SecurityFinding[]): Group[] {
  const out: Group[] = [];
  const byKey = new Map<string, Group>();
  for (const f of list) {
    const d = isCVEDetail(f.detail) ? f.detail : null;
    const k = `${f.kind}|${f.key}|${d?.version ?? ""}|${f.title}`;
    let g = byKey.get(k);
    if (!g) {
      g = { key: k, worst: f, items: [] };
      byKey.set(k, g);
      out.push(g);
    }
    g.items.push(f);
  }
  return out;
}

function GroupRow({ g, open, toggle, isAdmin }: { g: Group; open: boolean; toggle: () => void; isAdmin: boolean }) {
  if (g.items.length === 1) return <FindingRow f={g.worst} open={open} toggle={toggle} isAdmin={isAdmin} />;
  const f = g.worst;
  const d = isCVEDetail(f.detail) ? f.detail : null;
  const hosts = g.items.map((x) => x.hostname || x.subject);
  const accepted = g.items.filter((x) => x.status === "aanvaard").length;
  return (
    <div className="rounded-lg border border-slate-200 bg-white dark:border-slate-800 dark:bg-slate-900">
      <button className="flex w-full flex-wrap items-start gap-x-3 gap-y-1 p-3 text-left" onClick={toggle} aria-expanded={open}>
        <Badge tone={severityTone[f.severity]}>{f.severity}</Badge>
        {d && d.kev > 0 && <Badge tone="red">actief misbruikt</Badge>}
        <Badge tone="slate">{g.items.length} nodes</Badge>
        <span className="min-w-0 basis-full sm:basis-0 sm:flex-1">
          <span className="font-medium">{f.title}</span>
          <span className="block text-sm text-slate-500">
            {hosts.slice(0, 6).join(", ")}
            {hosts.length > 6 && ` en ${hosts.length - 6} meer`} · {f.summary}
            {accepted > 0 && ` · ${accepted} aanvaard`}
          </span>
        </span>
        <span className="text-xs text-slate-500">{open ? "▲" : "▼"}</span>
      </button>
      {open && (
        <div className="space-y-4 border-t border-slate-200 p-3 text-sm dark:border-slate-800">
          <div>
            <div className="font-medium">Oplossing</div>
            <p className="text-slate-700 dark:text-slate-300">{f.fix}</p>
            {d && d.fixable > 0 && (
              <Link href="/updates" className="text-sm font-medium text-blue-600 hover:underline dark:text-blue-400">
                Naar Updates →
              </Link>
            )}
          </div>
          {d && <CVETable d={d} />}
          <div>
            <div className="mb-1 font-medium">Per node</div>
            <div className="space-y-2">
              {g.items.map((x) => (
                <FindingRow key={x.id} f={x} open={false} toggle={() => {}} isAdmin={isAdmin} compact />
              ))}
            </div>
          </div>
        </div>
      )}
    </div>
  );
}
