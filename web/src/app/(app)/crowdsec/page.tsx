"use client";

import Link from "next/link";
import { useRouter, useSearchParams } from "next/navigation";
import { Suspense, useState } from "react";
import { NodeDialog } from "@/components/crowdsec/CrowdSecNodeCard";
import { Modal } from "@/components/Modal";
import { ago, tableClass, tdClass, thClass } from "@/components/inventory/bits";
import { Alert, Badge, Button, Card, Field, Input, PageHeader, Select, Textarea, cx } from "@/components/ui";
import { ApiError } from "@/lib/api/client";
import {
  crowdSecState,
  flag,
  useApplyCrowdSecNode,
  useCrowdSec,
  useCrowdSecAlerts,
  useCrowdSecBans,
  useCrowdSecDecide,
  useCrowdSecSettings,
  useSetCrowdSecNodes,
  type CrowdSecNode,
  type CrowdSecOverview,
} from "@/lib/crowdsec";
import { useIsAdmin } from "@/lib/inventory";

const tabs = [
  { key: "overzicht", label: "Overzicht" },
  { key: "alerts", label: "Alerts" },
  { key: "bans", label: "Bans" },
  { key: "nodes", label: "Nodes" },
  { key: "profielen", label: "Profielen" },
  { key: "instellingen", label: "Instellingen" },
] as const;
type Tab = (typeof tabs)[number]["key"];

const timeFmt = new Intl.DateTimeFormat("nl-BE", { dateStyle: "short", timeStyle: "short" });
const hourFmt = new Intl.DateTimeFormat("nl-BE", { hour: "2-digit", minute: "2-digit" });

function errText(e: unknown) {
  return e instanceof ApiError ? e.message : "Er ging iets mis.";
}

export default function CrowdSecPage() {
  return (
    <Suspense>
      <CrowdSec />
    </Suspense>
  );
}

function CrowdSec() {
  const params = useSearchParams();
  const router = useRouter();
  const tab = (tabs.find((t) => t.key === params.get("tab"))?.key ?? "overzicht") as Tab;
  const data = useCrowdSec();
  const isAdmin = useIsAdmin();
  const setTab = (t: Tab) => router.replace(`/crowdsec?tab=${t}`);

  return (
    <div className="space-y-6">
      <PageHeader
        title="CrowdSec"
        description="ClusterForge installeert CrowdSec en de nftables-bouncer op de nodes die je aanzet, herkent de software die er draait en zet daar de juiste collections en logbronnen voor. Een ban op één node geldt meteen op de andere."
      />
      <nav className="flex flex-wrap gap-1 border-b border-slate-200 dark:border-slate-800" aria-label="Tabbladen">
        {tabs.map((t) => (
          <button
            key={t.key}
            onClick={() => setTab(t.key)}
            className={cx(
              "-mb-px border-b-2 px-3 py-2 text-sm font-medium",
              tab === t.key ? "border-brand-600 text-brand-700 dark:text-sky-300" : "border-transparent text-slate-500 hover:text-slate-800 dark:hover:text-slate-200",
            )}
          >
            {t.label}
          </button>
        ))}
      </nav>
      {data.isError && <Alert>CrowdSec laden mislukte.</Alert>}
      {data.data && tab === "overzicht" && <OverviewTab o={data.data} isAdmin={isAdmin} goNodes={() => setTab("nodes")} />}
      {data.data && tab === "alerts" && <AlertsTab o={data.data} isAdmin={isAdmin} />}
      {data.data && tab === "bans" && <BansTab o={data.data} isAdmin={isAdmin} />}
      {data.data && tab === "nodes" && <NodesTab o={data.data} isAdmin={isAdmin} />}
      {data.data && tab === "profielen" && <ProfilesTab o={data.data} />}
      {data.data && tab === "instellingen" && <SettingsTab o={data.data} isAdmin={isAdmin} />}
    </div>
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

function OverviewTab({ o, isAdmin, goNodes }: { o: CrowdSecOverview; isAdmin: boolean; goNodes: () => void }) {
  const s = o.stats;
  const max = Math.max(1, ...o.hourly.map((h) => h.count));
  const enabled = o.nodes.filter((n) => n.enabled).length;
  return (
    <div className="space-y-6">
      {enabled === 0 && (
        <Alert kind="info">
          CrowdSec staat nog op geen enkele node aan.{" "}
          {isAdmin ? (
            <button className="font-medium underline" onClick={goNodes}>
              Zet het aan bij Nodes
            </button>
          ) : (
            "Een beheerder zet het aan bij Nodes."
          )}
          .
        </Alert>
      )}
      <div className="grid grid-cols-2 gap-3 md:grid-cols-3 xl:grid-cols-6">
        <Tile label="Beheerde nodes" value={s.managed} />
        <Tile label="CrowdSec draait" value={`${s.running}/${s.managed}`} tone={s.running < s.managed ? "amber" : "green"} />
        <Tile label="Alerts 24 u" value={s.alerts_24h} />
        <Tile label="Actieve bans" value={s.active_bans} hint="Eigen bans, zonder de community-blocklist" />
        <Tile label="Community-blocklist" value={s.blocklist > 0 ? s.blocklist.toLocaleString("nl-BE") : "—"} hint="IP's uit de Central API en lijsten" />
        <Tile label="Bouncers" value={`${s.bouncers - s.bouncers_down}/${s.bouncers}`} tone={s.bouncers_down > 0 ? "red" : undefined} />
      </div>
      {o.warnings.length > 0 && (
        <Card title="Waarschuwingen">
          <ul className="space-y-1 text-sm">
            {o.warnings.map((w, i) => (
              <li key={i}>
                <Link href={`/nodes/detail?id=${w.node_id}`} className="font-medium hover:underline">
                  {w.hostname}
                </Link>
                : {w.text}
              </li>
            ))}
          </ul>
        </Card>
      )}
      <Card title="Alerts per uur (24 u)">
        <div className="flex h-32 gap-1" role="img" aria-label="Alerts per uur">
          {o.hourly.map((h) => (
            <div key={h.hour} className="flex h-full flex-1 flex-col justify-end" title={`${hourFmt.format(new Date(h.hour))}: ${h.count}`}>
              <div className="w-full rounded-t bg-brand-500/80" style={{ height: `${(h.count / max) * 100}%`, minHeight: h.count > 0 ? 2 : 0 }} />
            </div>
          ))}
        </div>
        <div className="mt-1 flex justify-between text-xs text-slate-500">
          <span>{o.hourly[0] && hourFmt.format(new Date(o.hourly[0].hour))}</span>
          <span>nu</span>
        </div>
      </Card>
      <div className="grid gap-6 lg:grid-cols-2">
        <Card title="Top-scenario's (7 dagen)">
          <TopList items={o.top_scenarios} link={(k) => `/crowdsec?tab=alerts&scenario=${encodeURIComponent(k)}`} />
        </Card>
        <Card title="Top-IP's (7 dagen)">
          <TopList items={o.top_ips} link={(k) => `/crowdsec?tab=alerts&value=${encodeURIComponent(k)}`} showOrigin />
        </Card>
      </div>
    </div>
  );
}

function TopList({ items, link, showOrigin }: { items: CrowdSecOverview["top_ips"]; link: (k: string) => string; showOrigin?: boolean }) {
  if (items.length === 0) return <p className="text-sm text-slate-500">Geen alerts.</p>;
  const max = Math.max(...items.map((i) => i.count));
  return (
    <ul className="space-y-1.5 text-sm">
      {items.map((i) => (
        <li key={i.key} className="relative">
          <div className="absolute inset-y-0 left-0 rounded bg-brand-500/10" style={{ width: `${(i.count / max) * 100}%` }} />
          <Link href={link(i.key)} className="relative flex justify-between gap-2 px-2 py-0.5 hover:underline">
            <span className="truncate font-mono text-xs leading-5">
              {i.key}
              {showOrigin && (i.country || i.as) && (
                <span className="ml-2 font-sans text-slate-500">
                  {flag(i.country)} {i.country} {i.as}
                </span>
              )}
            </span>
            <span className="tabular-nums">{i.count}</span>
          </Link>
        </li>
      ))}
    </ul>
  );
}

function AlertsTab({ o, isAdmin }: { o: CrowdSecOverview; isAdmin: boolean }) {
  const params = useSearchParams();
  const [nodeId, setNodeId] = useState(params.get("node_id") ?? "");
  const [clusterId, setClusterId] = useState(params.get("cluster_id") ?? "");
  const [scenario, setScenario] = useState(params.get("scenario") ?? "");
  const [value, setValue] = useState(params.get("value") ?? "");
  const [ban, setBan] = useState<string | null>(null);
  const alerts = useCrowdSecAlerts({
    node_id: nodeId || undefined,
    cluster_id: clusterId || undefined,
    scenario: scenario || undefined,
    value: value || undefined,
    limit: 500,
  });
  const clusters = [...new Map(o.nodes.filter((n) => n.cluster_id).map((n) => [n.cluster_id!, n.cluster_name])).entries()];
  const scenarios = o.top_scenarios.map((s) => s.key);
  return (
    <Card>
      <div className="mb-4 grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
        <Select value={nodeId} onChange={(e) => setNodeId(e.target.value)} aria-label="Node">
          <option value="">Alle nodes</option>
          {o.nodes
            .filter((n) => n.enabled)
            .map((n) => (
              <option key={n.node_id} value={n.node_id}>
                {n.hostname}
              </option>
            ))}
        </Select>
        <Select value={clusterId} onChange={(e) => setClusterId(e.target.value)} aria-label="Cluster">
          <option value="">Alle clusters</option>
          {clusters.map(([id, name]) => (
            <option key={id} value={id}>
              {name}
            </option>
          ))}
        </Select>
        <Select value={scenario} onChange={(e) => setScenario(e.target.value)} aria-label="Scenario">
          <option value="">Alle scenario&apos;s</option>
          {[...new Set([scenario, ...scenarios].filter(Boolean))].map((s) => (
            <option key={s} value={s}>
              {s}
            </option>
          ))}
        </Select>
        <Input placeholder="IP (of begin ervan)" value={value} onChange={(e) => setValue(e.target.value.trim())} aria-label="IP" />
      </div>
      {alerts.isError && <Alert>Alerts laden mislukte.</Alert>}
      {alerts.data && alerts.data.length === 0 && <p className="text-sm text-slate-500">Geen alerts.</p>}
      {alerts.data && alerts.data.length > 0 && (
        <div className="overflow-x-auto">
          <table className={tableClass}>
            <thead>
              <tr>
                <th className={thClass}>Tijd</th>
                <th className={thClass}>Node</th>
                <th className={thClass}>Scenario</th>
                <th className={thClass}>Bron</th>
                <th className={thClass}>Events</th>
                <th className={thClass} />
              </tr>
            </thead>
            <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
              {alerts.data.map((a) => (
                <tr key={a.id}>
                  <td className={cx(tdClass, "whitespace-nowrap")} title={a.message}>
                    {timeFmt.format(new Date(a.started_at))}
                  </td>
                  <td className={tdClass}>
                    <Link href={`/nodes/detail?id=${a.node_id}`} className="hover:underline">
                      {a.hostname}
                    </Link>
                    {a.cluster_name && <div className="text-xs text-slate-500">{a.cluster_name}</div>}
                  </td>
                  <td className={cx(tdClass, "font-mono text-xs")}>
                    {a.scenario}
                    {a.simulated && (
                      <span className="ml-1">
                        <Badge>simulatie</Badge>
                      </span>
                    )}
                  </td>
                  <td className={tdClass}>
                    <span className="font-mono text-xs">{a.value}</span>
                    <div className="text-xs text-slate-500">
                      {flag(a.country)} {a.country} {a.as}
                    </div>
                  </td>
                  <td className={cx(tdClass, "tabular-nums")}>
                    {a.events}
                    {a.decisions > 0 && (
                      <span className="ml-2">
                        <Badge tone="red">ban</Badge>
                      </span>
                    )}
                  </td>
                  <td className={cx(tdClass, "text-right")}>
                    {isAdmin && a.decisions === 0 && (
                      <Button variant="ghost" onClick={() => setBan(a.value)}>
                        Blokkeren
                      </Button>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {ban !== null && <BanDialog o={o} initial={ban} onClose={() => setBan(null)} />}
    </Card>
  );
}

function BansTab({ o, isAdmin }: { o: CrowdSecOverview; isAdmin: boolean }) {
  const bans = useCrowdSecBans();
  const decide = useCrowdSecDecide();
  const [adding, setAdding] = useState(false);
  const [msg, setMsg] = useState<string | null>(null);
  const unban = (scope: string, value: string) =>
    decide.mutate(
      { delete: true, scope: scope === "range" ? "range" : "ip", value },
      {
        onSuccess: (r) => setMsg(`${value} gedeblokkeerd op ${r.nodes.join(", ") || "geen node"}${r.errors.length ? `; niet op: ${r.errors.join("; ")}` : ""}.`),
        onError: (e) => setMsg(errText(e)),
      },
    );
  return (
    <Card
      title={
        <span className="flex flex-wrap items-center justify-between gap-2">
          <span>Actieve bans</span>
          {isAdmin && <Button onClick={() => setAdding(true)}>IP blokkeren</Button>}
        </span>
      }
    >
      <p className="mb-3 text-xs text-slate-500">
        Eigen bans van de nodes, samengevoegd per adres. De community-blocklist ({o.stats.blocklist > 0 ? o.stats.blocklist.toLocaleString("nl-BE") : "?"} adressen) staat
        er niet bij. Gedeeld: ClusterForge zette de ban ook op de andere nodes ({shareLabel[o.settings.share_bans]}).
      </p>
      {msg && <Alert kind="info">{msg}</Alert>}
      {bans.isError && <Alert>Bans laden mislukte.</Alert>}
      {bans.data && bans.data.length === 0 && <p className="text-sm text-slate-500">Geen actieve bans.</p>}
      {bans.data && bans.data.length > 0 && (
        <div className="overflow-x-auto">
          <table className={tableClass}>
            <thead>
              <tr>
                <th className={thClass}>Adres</th>
                <th className={thClass}>Reden</th>
                <th className={thClass}>Nodes</th>
                <th className={thClass}>Tot</th>
                <th className={thClass} />
              </tr>
            </thead>
            <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
              {bans.data.map((b) => (
                <tr key={b.scope + b.value}>
                  <td className={tdClass}>
                    <span className="font-mono text-xs">{b.value}</span>
                    <div className="text-xs text-slate-500">
                      {flag(b.country)} {b.country} {b.as}
                    </div>
                  </td>
                  <td className={cx(tdClass, "text-xs")}>
                    <span className="font-mono">{b.scenario}</span>
                    <div className="mt-0.5 flex gap-1">
                      <Badge>{b.origin}</Badge>
                      {b.shared && <Badge tone="blue">gedeeld</Badge>}
                      {b.type !== "ban" && <Badge tone="amber">{b.type}</Badge>}
                    </div>
                  </td>
                  <td className={cx(tdClass, "text-xs")}>{b.nodes.join(", ")}</td>
                  <td className={cx(tdClass, "whitespace-nowrap text-xs")}>{timeFmt.format(new Date(b.until))}</td>
                  <td className={cx(tdClass, "text-right")}>
                    {isAdmin && (
                      <Button variant="secondary" disabled={decide.isPending} onClick={() => unban(b.scope, b.value)}>
                        Deblokkeren
                      </Button>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {adding && <BanDialog o={o} initial="" onClose={() => setAdding(false)} />}
    </Card>
  );
}

const shareLabel: Record<string, string> = { off: "delen staat uit", cluster: "binnen het cluster", all: "op alle beheerde nodes" };

function BanDialog({ o, initial, onClose }: { o: CrowdSecOverview; initial: string; onClose: () => void }) {
  const decide = useCrowdSecDecide();
  const [value, setValue] = useState(initial);
  const [duration, setDuration] = useState("24h");
  const [reason, setReason] = useState("");
  const [where, setWhere] = useState("all");
  const [result, setResult] = useState<string | null>(null);
  const clusters = [...new Map(o.nodes.filter((n) => n.enabled && n.cluster_id).map((n) => [n.cluster_id!, n.cluster_name])).entries()];
  const submit = () => {
    const scope = value.includes("/") ? "range" : "ip";
    decide.mutate(
      {
        scope,
        value: value.trim(),
        duration,
        reason: reason.trim() || undefined,
        cluster_id: where.startsWith("c:") ? where.slice(2) : undefined,
        node_ids: where.startsWith("n:") ? [where.slice(2)] : undefined,
      },
      {
        onSuccess: (r) => setResult(`Geblokkeerd op ${r.nodes.join(", ") || "geen node"}${r.errors.length ? `; niet op: ${r.errors.join("; ")}` : ""}.`),
      },
    );
  };
  return (
    <Modal title="IP blokkeren" onClose={onClose}>
      <div className="space-y-3">
        <Field label="IP of range" htmlFor="ban-value" hint="Zoals 203.0.113.7 of 203.0.113.0/24">
          <Input id="ban-value" value={value} onChange={(e) => setValue(e.target.value)} autoFocus />
        </Field>
        <Field label="Duur" htmlFor="ban-duration">
          <Select id="ban-duration" value={duration} onChange={(e) => setDuration(e.target.value)}>
            <option value="1h">1 uur</option>
            <option value="4h">4 uur</option>
            <option value="24h">24 uur</option>
            <option value="168h">7 dagen</option>
            <option value="720h">30 dagen</option>
            <option value="8760h">1 jaar</option>
          </Select>
        </Field>
        <Field label="Reden" htmlFor="ban-reason" hint="Leeg: handmatig via ClusterForge">
          <Input id="ban-reason" value={reason} onChange={(e) => setReason(e.target.value)} />
        </Field>
        <Field label="Waar" htmlFor="ban-where">
          <Select id="ban-where" value={where} onChange={(e) => setWhere(e.target.value)}>
            <option value="all">Alle nodes met CrowdSec</option>
            {clusters.map(([id, name]) => (
              <option key={id} value={`c:${id}`}>
                Cluster {name}
              </option>
            ))}
            {o.nodes
              .filter((n) => n.enabled)
              .map((n) => (
                <option key={n.node_id} value={`n:${n.node_id}`}>
                  Node {n.hostname}
                </option>
              ))}
          </Select>
        </Field>
        {decide.isError && <Alert>{errText(decide.error)}</Alert>}
        {result && <Alert kind="success">{result}</Alert>}
        <div className="flex justify-end gap-2">
          <Button variant="secondary" onClick={onClose}>
            {result ? "Sluiten" : "Annuleren"}
          </Button>
          {!result && (
            <Button variant="danger" disabled={!value.trim() || decide.isPending} onClick={submit}>
              Blokkeren
            </Button>
          )}
        </div>
      </div>
    </Modal>
  );
}

function NodesTab({ o, isAdmin }: { o: CrowdSecOverview; isAdmin: boolean }) {
  const set = useSetCrowdSecNodes();
  const apply = useApplyCrowdSecNode();
  const [edit, setEdit] = useState<CrowdSecNode | null>(null);
  const [remove, setRemove] = useState<CrowdSecNode | null>(null);
  const [msg, setMsg] = useState<string | null>(null);
  const groups: { key: string; name: string; env: string; clusterId?: string; nodes: CrowdSecNode[] }[] = [];
  for (const n of o.nodes) {
    const key = n.cluster_id ?? "";
    let g = groups.find((x) => x.key === key);
    if (!g) {
      g = { key, name: n.cluster_name || "Zonder cluster", env: n.cluster_environment, clusterId: n.cluster_id ?? undefined, nodes: [] };
      groups.push(g);
    }
    g.nodes.push(n);
  }
  const done = (r: { skipped: string[] }) => setMsg(r.skipped.length ? `Overgeslagen: ${r.skipped.join("; ")}` : "Aangezet; ClusterForge haalt de stand op en past het plan toe.");
  const onErr = (e: unknown) => setMsg(errText(e));
  return (
    <div className="space-y-6">
      {isAdmin && (
        <div className="flex flex-wrap items-center gap-2">
          <Button disabled={set.isPending} onClick={() => set.mutate({ all: true, managed: true }, { onSuccess: done, onError: onErr })}>
            CrowdSec op alle nodes
          </Button>
          <span className="text-xs text-slate-500">
            Bouncer standaard {o.settings.bouncer_default ? "aan" : "uit"}; automatisch toepassen {o.settings.auto_apply ? "aan" : "uit"} (Instellingen).
          </span>
        </div>
      )}
      {msg && <Alert kind="info">{msg}</Alert>}
      {groups.map((g) => (
        <Card
          key={g.key}
          title={
            <span className="flex flex-wrap items-center justify-between gap-2">
              <span className="flex items-center gap-2">
                {g.name}
                {g.env && <Badge tone={g.env === "prod" ? "red" : "slate"}>{g.env}</Badge>}
              </span>
              {isAdmin && g.clusterId && (
                <span className="flex gap-2">
                  <Button variant="secondary" disabled={set.isPending} onClick={() => set.mutate({ cluster_id: g.clusterId, managed: true }, { onSuccess: done, onError: onErr })}>
                    Cluster aanzetten
                  </Button>
                  {g.nodes.some((n) => n.enabled && n.managed) && (
                    <Button
                      variant="ghost"
                      disabled={set.isPending}
                      onClick={() => set.mutate({ cluster_id: g.clusterId, managed: false }, { onSuccess: () => setMsg("Beheer uitgezet; CrowdSec blijft op de nodes staan."), onError: onErr })}
                    >
                      Beheer uit
                    </Button>
                  )}
                </span>
              )}
            </span>
          }
        >
          <div className="overflow-x-auto">
            <table className={tableClass}>
              <thead>
                <tr>
                  <th className={thClass}>Node</th>
                  <th className={thClass}>Stand</th>
                  <th className={thClass}>Profielen</th>
                  <th className={thClass}>Toegepast</th>
                  <th className={thClass} />
                </tr>
              </thead>
              <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
                {g.nodes.map((n) => {
                  const st = crowdSecState(n);
                  return (
                    <tr key={n.node_id}>
                      <td className={tdClass}>
                        <Link href={`/nodes/detail?id=${n.node_id}`} className="font-medium hover:underline">
                          {n.hostname}
                        </Link>
                        {n.status?.version && <div className="text-xs text-slate-500">CrowdSec {n.status.version}</div>}
                      </td>
                      <td className={tdClass}>
                        <Badge tone={st.tone}>{st.label}</Badge>
                        {n.apply_error && <div className="mt-1 max-w-xs text-xs text-red-600 dark:text-red-400">{n.apply_error}</div>}
                        {n.enabled && n.bouncer && <div className="mt-1 text-xs text-slate-500">met bouncer</div>}
                      </td>
                      <td className={cx(tdClass, "text-xs")}>
                        <span className="flex flex-wrap gap-1">
                          {(n.plan?.profiles ?? []).map((p) => (
                            <span key={p.key} title={[p.reason, p.note].filter(Boolean).join(" — ")}>
                              <Badge tone={p.origin === "off" ? "slate" : p.note ? "amber" : "blue"}>
                                {p.origin === "off" ? <s>{p.name}</s> : p.name}
                              </Badge>
                            </span>
                          ))}
                        </span>
                      </td>
                      <td className={cx(tdClass, "whitespace-nowrap text-xs")}>
                        {n.applied_at ? ago(n.applied_at) : "—"}
                        {n.apply_job_id && (
                          <div>
                            <Link href={`/taken/detail?id=${n.apply_job_id}`} className="text-brand-700 hover:underline dark:text-sky-300">
                              laatste taak
                            </Link>
                          </div>
                        )}
                      </td>
                      <td className={cx(tdClass, "text-right whitespace-nowrap")}>
                        {isAdmin && n.supported && !n.enabled && (
                          <Button variant="secondary" disabled={set.isPending} onClick={() => set.mutate({ node_ids: [n.node_id], managed: true }, { onSuccess: done, onError: onErr })}>
                            Aanzetten
                          </Button>
                        )}
                        {isAdmin && n.enabled && (
                          <span className="inline-flex gap-1">
                            {!n.managed ? (
                              <Button variant="secondary" onClick={() => set.mutate({ node_ids: [n.node_id], managed: true }, { onSuccess: done, onError: onErr })}>
                                Beheer aan
                              </Button>
                            ) : (
                              <>
                                <Button
                                  variant="secondary"
                                  disabled={apply.isPending || n.apply_running || !n.online}
                                  onClick={() => apply.mutate({ id: n.node_id }, { onSuccess: () => setMsg(`Toepassen op ${n.hostname} gestart.`), onError: onErr })}
                                >
                                  Toepassen
                                </Button>
                                <Button variant="ghost" onClick={() => setEdit(n)}>
                                  Wijzigen
                                </Button>
                              </>
                            )}
                            <Button variant="ghost" onClick={() => setRemove(n)}>
                              …
                            </Button>
                          </span>
                        )}
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        </Card>
      ))}
      {edit && <NodeDialog o={o} n={edit} onClose={() => setEdit(null)} />}
      {remove && (
        <Modal title={`CrowdSec op ${remove.hostname}`} onClose={() => setRemove(null)}>
          <div className="space-y-3 text-sm">
            <p>
              <strong>Beheer uit</strong>: ClusterForge past niets meer toe en haalt geen alerts meer op; CrowdSec en de bouncer blijven werken zoals ze nu staan.
            </p>
            <p>
              <strong>Verwijderen</strong>: <code>apt-get purge</code> van CrowdSec en de bouncer; hun bans op deze node vallen weg. De alerts in ClusterForge blijven.
            </p>
            <div className="flex flex-wrap justify-end gap-2">
              <Button variant="secondary" onClick={() => setRemove(null)}>
                Annuleren
              </Button>
              {remove.managed && (
                <Button
                  variant="secondary"
                  onClick={() => set.mutate({ node_ids: [remove.node_id], managed: false }, { onSuccess: () => { setRemove(null); setMsg("Beheer uitgezet."); }, onError: onErr })}
                >
                  Beheer uit
                </Button>
              )}
              <Button
                variant="danger"
                disabled={!remove.online || apply.isPending}
                onClick={() =>
                  apply.mutate({ id: remove.node_id, remove: true }, { onSuccess: () => { setRemove(null); setMsg(`Verwijderen van ${remove.hostname} gestart.`); }, onError: onErr })
                }
              >
                Verwijderen
              </Button>
            </div>
          </div>
        </Modal>
      )}
    </div>
  );
}

function ProfilesTab({ o }: { o: CrowdSecOverview }) {
  return (
    <Card>
      <p className="mb-3 text-sm text-slate-600 dark:text-slate-400">
        Wat ClusterForge herkent en wat het dan op de node zet. Collections komen uit de CrowdSec Hub; &quot;community&quot; zijn collections van gebruikers, niet van CrowdSec zelf.
        Zabbix valt onder Apache of nginx (de web-frontend).
      </p>
      <div className="overflow-x-auto">
        <table className={tableClass}>
          <thead>
            <tr>
              <th className={thClass}>Profiel</th>
              <th className={thClass}>Collections</th>
              <th className={thClass}>Op</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
            {o.catalog.map((p) => (
              <tr key={p.key}>
                <td className={tdClass}>
                  <span className="font-medium">{p.name}</span> {p.community && <Badge>community</Badge>}
                  <div className="text-xs text-slate-500">{p.description}</div>
                </td>
                <td className={cx(tdClass, "font-mono text-xs")}>{p.collections.join(", ")}</td>
                <td className={cx(tdClass, "text-xs")}>{p.nodes.length ? p.nodes.join(", ") : <span className="text-slate-400">—</span>}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </Card>
  );
}

function SettingsTab({ o, isAdmin }: { o: CrowdSecOverview; isAdmin: boolean }) {
  const save = useCrowdSecSettings();
  const [auto, setAuto] = useState(o.settings.auto_apply);
  const [bouncer, setBouncer] = useState(o.settings.bouncer_default);
  const [share, setShare] = useState(o.settings.share_bans);
  const [whitelist, setWhitelist] = useState(o.settings.whitelist.join("\n"));
  return (
    <Card>
      <div className="max-w-2xl space-y-4 text-sm">
        <label className="flex items-start gap-2">
          <input type="checkbox" className="mt-1" checked={auto} disabled={!isAdmin} onChange={(e) => setAuto(e.target.checked)} />
          <span>
            <span className="font-medium">Automatisch toepassen</span>
            <span className="block text-xs text-slate-500">
              Komt er software bij of verandert een instelling, dan past ClusterForge de node vanzelf aan (ook op prod). Het script test de configuratie eerst en zet bij een fout de vorige terug.
            </span>
          </span>
        </label>
        <label className="flex items-start gap-2">
          <input type="checkbox" className="mt-1" checked={bouncer} disabled={!isAdmin} onChange={(e) => setBouncer(e.target.checked)} />
          <span>
            <span className="font-medium">Bouncer standaard aan</span>
            <span className="block text-xs text-slate-500">Voor nodes die je vanaf nu aanzet; per node te wijzigen.</span>
          </span>
        </label>
        <Field label="Bans delen" htmlFor="cs-share" hint="Een ban van een scenario op één node komt ook op de andere nodes, met de resterende duur.">
          <Select id="cs-share" value={share} disabled={!isAdmin} onChange={(e) => setShare(e.target.value as typeof share)}>
            <option value="all">Op alle beheerde nodes</option>
            <option value="cluster">Binnen het cluster</option>
            <option value="off">Uit</option>
          </Select>
        </Field>
        <Field label="Whitelist" htmlFor="cs-whitelist" hint="Eén adres of netwerk per regel. CrowdSec bant deze nooit: zo blijven je eigen netwerk, de proxy voor een backend en de clusternodes vrij.">
          <Textarea id="cs-whitelist" rows={7} className="font-mono" value={whitelist} disabled={!isAdmin} onChange={(e) => setWhitelist(e.target.value)} />
        </Field>
        {save.isError && <Alert>{errText(save.error)}</Alert>}
        {save.isSuccess && <Alert kind="success">Bewaard. Gewijzigde plannen worden {auto ? "vanzelf toegepast" : "niet vanzelf toegepast: gebruik Toepassen bij Nodes"}.</Alert>}
        {isAdmin && (
          <Button
            disabled={save.isPending}
            onClick={() =>
              save.mutate({
                auto_apply: auto,
                bouncer_default: bouncer,
                share_bans: share,
                whitelist: whitelist
                  .split(/[\n,]/)
                  .map((s) => s.trim())
                  .filter(Boolean),
              })
            }
          >
            Bewaren
          </Button>
        )}
      </div>
    </Card>
  );
}
