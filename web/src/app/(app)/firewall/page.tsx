"use client";

import { Tabs } from "@/components/Tabs";
import Link from "next/link";
import { useRouter, useSearchParams } from "next/navigation";
import { Suspense, useState } from "react";
import { NodeActions, PlanView, errText, hourFmt, timeFmt } from "@/components/firewall/FirewallNodeView";
import { Modal } from "@/components/Modal";
import { ago, tableClass, tdClass, thClass } from "@/components/inventory/bits";
import { Alert, Badge, Button, Card, Field, Input, PageHeader, Select, Textarea, cx } from "@/components/ui";
import {
  classLabel,
  firewallState,
  useAddFirewallRule,
  useDeleteFirewallRule,
  useFirewall,
  useFirewallSettings,
  useFirewallTraffic,
  useSetFirewallNodes,
  type FirewallCell,
  type FirewallCluster,
  type FirewallNode,
  type FirewallOverview,
  type HourCount,
} from "@/lib/firewall";
import { useIsAdmin } from "@/lib/inventory";

const tabs = [
  { key: "overzicht", label: "Overzicht" },
  { key: "verkeer", label: "Verkeer naar dichte poorten" },
  { key: "nodes", label: "Nodes" },
  { key: "regels", label: "Eigen regels" },
  { key: "catalogus", label: "Catalogus" },
  { key: "instellingen", label: "Instellingen" },
] as const;
type Tab = (typeof tabs)[number]["key"];

export default function FirewallPage() {
  return (
    <Suspense>
      <Firewall />
    </Suspense>
  );
}

function Firewall() {
  const params = useSearchParams();
  const router = useRouter();
  const tab = (tabs.find((t) => t.key === params.get("tab"))?.key ?? "overzicht") as Tab;
  const data = useFirewall();
  const isAdmin = useIsAdmin();
  const setTab = (t: Tab) => router.replace(`/firewall?tab=${t}`);

  return (
    <div className="space-y-6">
      <PageHeader
        title="Firewall"
        description="ClusterForge beheert ufw op de nodes die je aanzet: open wat de software gebruikt, voor wie het nodig is, de rest dicht. Clusterpoorten gaan alleen open voor de andere clusterleden, SSH blijft altijd open en een toepassing die de node onbereikbaar maakt, draait zichzelf terug."
      />
      <Tabs tabs={tabs} value={tab} onChange={setTab} />
      {data.isPending && <p className="text-sm text-slate-500">Firewall van de nodes laden…</p>}
      {data.isError && <Alert>Firewall laden mislukte.</Alert>}
      {data.data && tab === "overzicht" && <OverviewTab o={data.data} isAdmin={isAdmin} goNodes={() => setTab("nodes")} />}
      {data.data && tab === "verkeer" && <TrafficTab o={data.data} isAdmin={isAdmin} />}
      {data.data && tab === "nodes" && <NodesTab o={data.data} isAdmin={isAdmin} />}
      {data.data && tab === "regels" && <RulesTab o={data.data} isAdmin={isAdmin} />}
      {data.data && tab === "catalogus" && <CatalogTab o={data.data} />}
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

function Bars({ hourly, label }: { hourly: HourCount[]; label: string }) {
  const max = Math.max(1, ...hourly.map((h) => h.count));
  return (
    <>
      <div className="flex h-32 gap-1" role="img" aria-label={label}>
        {hourly.map((h) => (
          <div key={h.hour} className="flex h-full flex-1 flex-col justify-end" title={`${hourFmt.format(new Date(h.hour))}: ${h.count}`}>
            <div className="w-full rounded-t bg-red-500/70" style={{ height: `${(h.count / max) * 100}%`, minHeight: h.count > 0 ? 2 : 0 }} />
          </div>
        ))}
      </div>
      <div className="mt-1 flex justify-between text-xs text-slate-500">
        <span>{hourly[0] && hourFmt.format(new Date(hourly[0].hour))}</span>
        <span>nu</span>
      </div>
    </>
  );
}

function OverviewTab({ o, isAdmin, goNodes }: { o: FirewallOverview; isAdmin: boolean; goNodes: () => void }) {
  const s = o.stats;
  const enabled = s.managed + s.preview;
  return (
    <div className="space-y-6">
      {enabled === 0 && (
        <Alert kind="info">
          De firewall wordt nog op geen enkele node beheerd.{" "}
          {isAdmin ? (
            <button className="font-medium underline" onClick={goNodes}>
              Zet nodes op voorbeeld bij Nodes
            </button>
          ) : (
            "Een beheerder zet nodes aan bij Nodes"
          )}
          : je ziet dan eerst wat er open en dicht zou gaan, zonder dat er iets verandert.
        </Alert>
      )}
      <div className="grid grid-cols-2 gap-3 md:grid-cols-3 xl:grid-cols-6">
        <Tile label="Beheerd" value={s.managed} hint="Nodes waarop ClusterForge de regels toepast" />
        <Tile label="Voorbeeld" value={s.preview} hint="Nodes waarvan je het plan ziet zonder toe te passen" />
        <Tile label="ufw aan" value={`${s.active}/${enabled}`} tone={s.managed > 0 && s.active < s.managed ? "red" : undefined} />
        <Tile label="Open poorten" value={s.open_ports} hint="Over alle nodes samen" />
        <Tile label="Pogingen 24 u" value={s.blocked_24h.toLocaleString("nl-BE")} tone={s.blocked_24h > 0 ? "amber" : undefined} hint="Nieuwe verbindingen naar dichte poorten" />
        <Tile label="Bronnen 24 u" value={s.sources_24h} hint="Verschillende afzenders" />
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
      <Card title="Pogingen naar dichte poorten per uur (24 u)">
        <Bars hourly={o.hourly} label="Pogingen per uur" />
      </Card>
      {o.clusters.map((c) => (
        <ClusterMatrix key={c.id ?? "none"} c={c} nodes={o.nodes} />
      ))}
    </div>
  );
}

function Cell({ c, n }: { c: FirewallCell | undefined; n: FirewallNode | undefined }) {
  if (!c) return <span className="text-slate-400">—</span>;
  const managed = n?.mode === "managed";
  return (
    <span className="flex flex-col items-start gap-0.5">
      {c.state === "open" ? (
        <span title={c.from.join(", ")}>
          <Badge tone={c.class === "any" ? "amber" : "green"}>
            {classLabel[c.class] ?? c.class}
            {!c.applied && (managed ? " · nog niet" : " · plan")}
          </Badge>
        </span>
      ) : c.state === "docker" ? (
        <Badge tone="blue">Docker</Badge>
      ) : (
        <Badge>dicht</Badge>
      )}
      {c.blocked_24h > 0 && (
        <Link href={`/firewall?tab=verkeer&node_id=${n?.node_id ?? ""}`} className="text-xs font-medium text-red-600 hover:underline dark:text-red-400">
          {c.blocked_24h.toLocaleString("nl-BE")} geblokkeerd
        </Link>
      )}
    </span>
  );
}

function ClusterMatrix({ c, nodes }: { c: FirewallCluster; nodes: FirewallNode[] }) {
  const byId = new Map(nodes.map((n) => [n.node_id, n]));
  return (
    <Card
      title={
        <span className="flex flex-wrap items-center justify-between gap-2">
          <span>
            {c.id ? (
              <Link href={`/clusters/detail?id=${c.id}`} className="hover:underline">
                {c.name}
              </Link>
            ) : (
              c.name
            )}{" "}
            <span className="text-xs font-normal text-slate-500">wat er open en dicht staat</span>
          </span>
          {c.id && (
            <Link href={`/firewall?tab=verkeer&cluster_id=${c.id}`} className="text-sm font-normal text-brand-700 hover:underline dark:text-sky-300">
              Verkeer van dit cluster
            </Link>
          )}
        </span>
      }
    >
      <div className="overflow-x-auto">
        <table className={tableClass}>
          <thead>
            <tr>
              <th className={thClass}>Poort</th>
              <th className={thClass}>Software</th>
              {c.nodes.map((n) => {
                const fn = byId.get(n.id);
                const st = fn ? firewallState(fn) : null;
                return (
                  <th key={n.id} className={thClass}>
                    <Link href={`/nodes/detail?id=${n.id}`} className="hover:underline">
                      {n.hostname}
                    </Link>
                    {st && (
                      <div className="mt-0.5 font-normal normal-case">
                        <Badge tone={st.tone}>{st.label}</Badge>
                      </div>
                    )}
                  </th>
                );
              })}
            </tr>
          </thead>
          <tbody>
            {c.ports.map((p) => (
              <tr key={`${p.proto}/${p.port}`}>
                <td className={cx(tdClass, "font-mono text-xs")}>{p.port ? `${p.port}/${p.proto}` : p.proto}</td>
                <td className={tdClass}>
                  {p.software || <span className="text-slate-500">onbekend</span>}
                  {p.label && <span className="text-slate-500"> · {p.label}</span>}
                </td>
                {c.nodes.map((n) => (
                  <td key={n.id} className={tdClass}>
                    <Cell c={p.cells[n.id]} n={byId.get(n.id)} />
                  </td>
                ))}
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      <p className="mt-2 text-xs text-slate-500">
        Alle andere poorten zijn dicht. “plan”: zo zou het worden (stand voorbeeld); “nog niet”: beheerd maar nog niet toegepast. Een poort via Docker zet Docker
        zelf open, buiten ufw om.
      </p>
    </Card>
  );
}

function TrafficTab({ o, isAdmin }: { o: FirewallOverview; isAdmin: boolean }) {
  const params = useSearchParams();
  const [clusterId, setClusterId] = useState(params.get("cluster_id") ?? "");
  const [nodeId, setNodeId] = useState(params.get("node_id") ?? "");
  const [hours, setHours] = useState(24);
  const q = useFirewallTraffic({ cluster_id: clusterId || undefined, node_id: nodeId || undefined, hours, limit: 500 });
  const add = useAddFirewallRule();
  const [msg, setMsg] = useState<string | null>(null);
  const clusters = o.clusters.filter((c) => c.id);
  const items = q.data?.items ?? [];
  return (
    <div className="space-y-4">
      <p className="text-sm text-slate-600 dark:text-slate-400">
        Nieuwe verbindingen die ufw weggooide omdat geen regel ze toeliet. Een <Badge tone="amber">clusterlid</Badge> dat op een dichte poort klopt, is bijna altijd een
        vergeten regel.
      </p>
      <div className="flex flex-wrap gap-2">
        <Select aria-label="Cluster" value={clusterId} onChange={(e) => setClusterId(e.target.value)} className="w-auto">
          <option value="">Alle clusters</option>
          {clusters.map((c) => (
            <option key={c.id} value={c.id ?? ""}>
              {c.name}
            </option>
          ))}
        </Select>
        <Select aria-label="Node" value={nodeId} onChange={(e) => setNodeId(e.target.value)} className="w-auto">
          <option value="">Alle nodes</option>
          {o.nodes
            .filter((n) => n.mode !== "off")
            .map((n) => (
              <option key={n.node_id} value={n.node_id}>
                {n.hostname}
              </option>
            ))}
        </Select>
        <Select aria-label="Periode" value={hours} onChange={(e) => setHours(Number(e.target.value))} className="w-auto">
          <option value={24}>24 uur</option>
          <option value={168}>7 dagen</option>
          <option value={720}>30 dagen</option>
        </Select>
      </div>
      {msg && <Alert kind="info">{msg}</Alert>}
      {q.data && (
        <Card title="Per uur (24 u)">
          <Bars hourly={q.data.hourly} label="Pogingen per uur" />
        </Card>
      )}
      <div className="overflow-x-auto">
        <table className={tableClass}>
          <thead>
            <tr>
              <th className={thClass}>Laatst</th>
              <th className={thClass}>Naar</th>
              <th className={thClass}>Poort</th>
              <th className={thClass}>Van</th>
              <th className={cx(thClass, "text-right")}>Pakketten</th>
              {isAdmin && <th className={thClass} />}
            </tr>
          </thead>
          <tbody>
            {items.length === 0 && (
              <tr>
                <td className={tdClass} colSpan={6}>
                  <span className="text-slate-500">{q.isLoading ? "Laden…" : "Geen verkeer naar dichte poorten in deze periode."}</span>
                </td>
              </tr>
            )}
            {items.map((t) => (
              <tr key={`${t.node_id}/${t.src}/${t.proto}/${t.port}`}>
                <td className={cx(tdClass, "whitespace-nowrap text-xs")} title={timeFmt.format(new Date(t.last))}>
                  {ago(t.last)}
                </td>
                <td className={tdClass}>
                  <Link href={`/nodes/detail?id=${t.node_id}`} className="hover:underline">
                    {t.hostname}
                  </Link>
                  {t.cluster_name && <span className="text-xs text-slate-500"> · {t.cluster_name}</span>}
                </td>
                <td className={tdClass}>
                  <span className="font-mono text-xs">{t.port ? `${t.port}/${t.proto}` : t.proto}</span>
                  {t.software && <span className="text-xs text-slate-500"> · {t.software}</span>}
                </td>
                <td className={tdClass}>
                  <span className="font-mono text-xs">{t.src}</span>
                  {t.src_node && (
                    <span className="ml-1">
                      <Badge tone={t.same_cluster ? "amber" : "slate"}>{t.same_cluster ? `clusterlid ${t.src_node}` : t.src_node}</Badge>
                    </span>
                  )}
                </td>
                <td className={cx(tdClass, "text-right tabular-nums")}>{t.count.toLocaleString("nl-BE")}</td>
                {isAdmin && (
                  <td className={tdClass}>
                    {t.same_cluster && t.cluster_id && t.port > 0 && (t.proto === "tcp" || t.proto === "udp") && (
                      <Button
                        variant="secondary"
                        disabled={add.isPending}
                        onClick={() =>
                          add.mutate(
                            { action: "open", proto: t.proto as "tcp" | "udp", port: String(t.port), source: "cluster", cluster_id: t.cluster_id ?? undefined, comment: "na geblokkeerd verkeer" },
                            { onSuccess: () => setMsg(`${t.port}/${t.proto} staat in het plan open voor de leden van ${t.cluster_name}; beheerde nodes passen het zo toe.`), onError: (e) => setMsg(errText(e)) },
                          )
                        }
                      >
                        Openzetten voor het cluster
                      </Button>
                    )}
                  </td>
                )}
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}

function NodesTab({ o, isAdmin }: { o: FirewallOverview; isAdmin: boolean }) {
  const set = useSetFirewallNodes();
  const [msg, setMsg] = useState<string | null>(null);
  const [show, setShow] = useState<FirewallNode | null>(null);
  const shown = show ? (o.nodes.find((n) => n.node_id === show.node_id) ?? show) : null;
  return (
    <div className="space-y-4">
      {isAdmin && (
        <div className="flex flex-wrap items-center gap-2">
          <Button
            variant="secondary"
            disabled={set.isPending}
            onClick={() =>
              set.mutate(
                { all: true, mode: "preview" },
                { onSuccess: (r) => setMsg((r as { skipped: string[] }).skipped.join("; ") || "Alle nodes staan op voorbeeld; de plannen volgen meteen."), onError: (e) => setMsg(errText(e)) },
              )
            }
          >
            Voorbeeld op alle nodes
          </Button>
          <span className="text-sm text-slate-500">Voorbeeld verandert niets op de nodes. Zet een node op beheerd om het plan toe te passen.</span>
        </div>
      )}
      {msg && <Alert kind="info">{msg}</Alert>}
      <div className="overflow-x-auto">
        <table className={tableClass}>
          <thead>
            <tr>
              <th className={thClass}>Node</th>
              <th className={thClass}>Cluster</th>
              <th className={thClass}>Stand</th>
              <th className={thClass}>Plan</th>
              <th className={thClass}>Verschil</th>
              <th className={thClass}>Geblokkeerd 24 u</th>
              <th className={thClass}>Toegepast</th>
              {isAdmin && <th className={thClass}>Acties</th>}
            </tr>
          </thead>
          <tbody>
            {o.nodes.map((n) => {
              const st = firewallState(n);
              const open = n.plan?.entries.filter((e) => e.state === "open").length ?? 0;
              const d = n.diff;
              return (
                <tr key={n.node_id}>
                  <td className={tdClass}>
                    <Link href={`/nodes/detail?id=${n.node_id}`} className="font-medium hover:underline">
                      {n.hostname}
                    </Link>
                  </td>
                  <td className={tdClass}>{n.cluster_name || <span className="text-slate-500">—</span>}</td>
                  <td className={tdClass}>
                    <Badge tone={st.tone}>{st.label}</Badge>
                    {n.apply_error && <div className="mt-1 max-w-xs text-xs text-red-600 dark:text-red-400">{n.apply_error}</div>}
                  </td>
                  <td className={tdClass}>
                    {n.plan ? (
                      <button className="text-brand-700 hover:underline dark:text-sky-300" onClick={() => setShow(n)}>
                        {open} open
                      </button>
                    ) : (
                      <span className="text-slate-500">—</span>
                    )}
                  </td>
                  <td className={cx(tdClass, "whitespace-nowrap font-mono text-xs")}>
                    {d ? (
                      d.add.length + d.adopt.length + d.remove.length === 0 ? (
                        <span className="text-slate-500">gelijk</span>
                      ) : (
                        <button onClick={() => setShow(n)} className="hover:underline">
                          <span className="text-emerald-700 dark:text-emerald-400">+{d.add.length}</span>{" "}
                          {d.adopt.length > 0 && <span className="text-sky-700 dark:text-sky-300">~{d.adopt.length} </span>}
                          <span className="text-red-700 dark:text-red-400">−{d.remove.length}</span>
                        </button>
                      )
                    ) : (
                      "—"
                    )}
                  </td>
                  <td className={cx(tdClass, "tabular-nums")}>{n.mode === "off" ? "—" : n.blocked_24h.toLocaleString("nl-BE")}</td>
                  <td className={cx(tdClass, "text-xs")}>{n.applied_at ? ago(n.applied_at) : "—"}</td>
                  {isAdmin && (
                    <td className={tdClass}>
                      <NodeActions n={n} onMsg={setMsg} compact />
                    </td>
                  )}
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
      {shown && (
        <Modal title={`Firewall van ${shown.hostname}`} onClose={() => setShow(null)} wide>
          <div className="space-y-4">
            {isAdmin && <NodeActions n={shown} onMsg={setMsg} />}
            <PlanView n={shown} />
          </div>
        </Modal>
      )}
    </div>
  );
}

function RulesTab({ o, isAdmin }: { o: FirewallOverview; isAdmin: boolean }) {
  const add = useAddFirewallRule();
  const del = useDeleteFirewallRule();
  const [form, setForm] = useState({ action: "open", proto: "tcp", port: "", source: "lan", scope: "", comment: "" });
  const [msg, setMsg] = useState<string | null>(null);
  const scopes = [
    ...o.clusters.filter((c) => c.id).map((c) => ({ value: `c:${c.id}`, label: `cluster ${c.name}` })),
    ...o.nodes.filter((n) => n.mode !== "off").map((n) => ({ value: `n:${n.node_id}`, label: `node ${n.hostname}` })),
  ];
  const submit = () => {
    const [kind, id] = form.scope.split(":");
    add.mutate(
      {
        action: form.action as "open" | "close",
        proto: form.proto as "tcp" | "udp",
        port: form.port.trim(),
        source: form.action === "open" ? form.source.trim() : undefined,
        comment: form.comment.trim() || undefined,
        cluster_id: kind === "c" ? id : undefined,
        node_id: kind === "n" ? id : undefined,
      },
      {
        onSuccess: () => {
          setMsg("Regel bewaard; de plannen worden meteen opnieuw uitgerekend.");
          setForm({ ...form, port: "", comment: "" });
        },
        onError: (e) => setMsg(errText(e)),
      },
    );
  };
  return (
    <div className="space-y-4">
      <p className="text-sm text-slate-600 dark:text-slate-400">
        Wat de herkenning niet weet. <strong>Open</strong> zet een poort open voor een bron; <strong>dicht</strong> houdt een poort dicht, ook als er iets op luistert (behalve
        SSH).
      </p>
      <div className="overflow-x-auto">
        <table className={tableClass}>
          <thead>
            <tr>
              <th className={thClass}>Geldt voor</th>
              <th className={thClass}>Actie</th>
              <th className={thClass}>Poort</th>
              <th className={thClass}>Bron</th>
              <th className={thClass}>Commentaar</th>
              {isAdmin && <th className={thClass} />}
            </tr>
          </thead>
          <tbody>
            {o.rules.length === 0 && (
              <tr>
                <td className={tdClass} colSpan={6}>
                  <span className="text-slate-500">Nog geen eigen regels.</span>
                </td>
              </tr>
            )}
            {o.rules.map((r) => (
              <tr key={r.id}>
                <td className={tdClass}>{r.hostname ? `node ${r.hostname}` : r.cluster_name ? `cluster ${r.cluster_name}` : "alle nodes"}</td>
                <td className={tdClass}>{r.action === "open" ? <Badge tone="green">open</Badge> : <Badge>dicht</Badge>}</td>
                <td className={cx(tdClass, "font-mono text-xs")}>
                  {r.port}/{r.proto}
                </td>
                <td className={tdClass}>{r.action === "open" ? (classLabel[r.source] ?? r.source) : "—"}</td>
                <td className={tdClass}>{r.comment}</td>
                {isAdmin && (
                  <td className={tdClass}>
                    <Button variant="secondary-danger" disabled={del.isPending} onClick={() => del.mutate(r.id, { onError: (e) => setMsg(errText(e)) })}>
                      Verwijderen
                    </Button>
                  </td>
                )}
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {isAdmin && (
        <Card title="Regel toevoegen">
          <div className="grid gap-3 md:grid-cols-3">
            <Field label="Geldt voor" htmlFor="fw-scope">
              <Select id="fw-scope" value={form.scope} onChange={(e) => setForm({ ...form, scope: e.target.value })}>
                <option value="">alle nodes</option>
                {scopes.map((s) => (
                  <option key={s.value} value={s.value}>
                    {s.label}
                  </option>
                ))}
              </Select>
            </Field>
            <Field label="Actie" htmlFor="fw-action">
              <Select id="fw-action" value={form.action} onChange={(e) => setForm({ ...form, action: e.target.value })}>
                <option value="open">open</option>
                <option value="close">dicht</option>
              </Select>
            </Field>
            <Field label="Poort" htmlFor="fw-port" hint="Zoals 8443 of 2379:2380">
              <div className="flex gap-2">
                <Input id="fw-port" value={form.port} onChange={(e) => setForm({ ...form, port: e.target.value })} placeholder="8443" />
                <Select aria-label="Protocol" value={form.proto} onChange={(e) => setForm({ ...form, proto: e.target.value })} className="w-24">
                  <option value="tcp">tcp</option>
                  <option value="udp">udp</option>
                </Select>
              </div>
            </Field>
            {form.action === "open" && (
              <Field label="Bron" htmlFor="fw-source" hint="lan, cluster, admin, any, containers, of een adres/netwerk">
                <Input id="fw-source" list="fw-classes" value={form.source} onChange={(e) => setForm({ ...form, source: e.target.value })} />
                <datalist id="fw-classes">
                  {o.classes.map((c) => (
                    <option key={c} value={c}>
                      {classLabel[c]}
                    </option>
                  ))}
                </datalist>
              </Field>
            )}
            <Field label="Commentaar" htmlFor="fw-comment">
              <Input id="fw-comment" maxLength={60} value={form.comment} onChange={(e) => setForm({ ...form, comment: e.target.value })} />
            </Field>
          </div>
          {msg && (
            <div className="mt-3">
              <Alert kind="info">{msg}</Alert>
            </div>
          )}
          <div className="mt-3">
            <Button disabled={add.isPending || !form.port.trim()} onClick={submit}>
              Toevoegen
            </Button>
          </div>
        </Card>
      )}
    </div>
  );
}

function CatalogTab({ o }: { o: FirewallOverview }) {
  return (
    <div className="space-y-3">
      <p className="text-sm text-slate-600 dark:text-slate-400">
        Software die ClusterForge herkent aan het proces, de systemd-unit of het Docker-image, met de poorten en voor wie ze opengaan. Poorten van onbekende software volgen
        de instelling “Onbekende poorten”.
      </p>
      <div className="overflow-x-auto">
        <table className={tableClass}>
          <thead>
            <tr>
              <th className={thClass}>Software</th>
              <th className={thClass}>Herkend aan</th>
              <th className={thClass}>Poorten</th>
              <th className={thClass}>Andere poorten</th>
            </tr>
          </thead>
          <tbody>
            {o.catalog.map((s) => (
              <tr key={s.key}>
                <td className={tdClass}>
                  {s.name}
                  {s.fixed && (
                    <div className="text-xs text-slate-500" title="De poorten gaan open zodra de unit enabled is, ook als er nog niets luistert">
                      ook als de dienst stilstaat
                    </div>
                  )}
                </td>
                <td className={cx(tdClass, "font-mono text-xs")}>{[...(s.procs ?? []), ...(s.units ?? []).map((u) => u + ".service"), ...(s.images ?? [])].join(", ")}</td>
                <td className={tdClass}>
                  {s.vrrp && <div className="text-xs">VRRP · clusterleden</div>}
                  {s.ports.map((p) => (
                    <div key={`${p.proto}/${p.port}`} className="text-xs">
                      <span className="font-mono">
                        {p.port}/{p.proto}
                      </span>{" "}
                      · {classLabel[p.class] ?? p.class}
                      {p.label && <span className="text-slate-500"> ({p.label})</span>}
                    </div>
                  ))}
                </td>
                <td className={cx(tdClass, "text-xs")}>{s.default === "ignore" ? "ufw laat toe" : s.default ? classLabel[s.default] : "instelling"}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}

function SettingsTab({ o, isAdmin }: { o: FirewallOverview; isAdmin: boolean }) {
  const save = useFirewallSettings();
  const s = o.settings;
  const [form, setForm] = useState({ ...s, lanText: s.lan.join("\n"), adminText: s.admin.join("\n") });
  const [msg, setMsg] = useState<{ ok: boolean; text: string } | null>(null);
  const lines = (t: string) =>
    t
      .split(/[\n,]/)
      .map((x) => x.trim())
      .filter(Boolean);
  const submit = () =>
    save.mutate(
      {
        auto_apply: form.auto_apply,
        lan: lines(form.lanText),
        admin: lines(form.adminText),
        ssh_from: form.ssh_from,
        unknown_ports: form.unknown_ports,
        strict: form.strict,
        confirm_seconds: Number(form.confirm_seconds),
        grace_hours: Number(form.grace_hours),
        monitor: form.monitor,
      },
      { onSuccess: () => setMsg({ ok: true, text: "Bewaard; de plannen worden opnieuw uitgerekend." }), onError: (e) => setMsg({ ok: false, text: errText(e) }) },
    );
  const check = (k: "auto_apply" | "strict" | "monitor", label: string, hint: string) => (
    <label className="flex items-start gap-2 text-sm">
      <input type="checkbox" className="mt-1" disabled={!isAdmin} checked={form[k]} onChange={(e) => setForm({ ...form, [k]: e.target.checked })} />
      <span>
        <span className="font-medium">{label}</span>
        <span className="block text-slate-500">{hint}</span>
      </span>
    </label>
  );
  return (
    <Card title="Instellingen">
      <div className="grid gap-4 md:grid-cols-2">
        <Field label="Eigen netwerken (lan)" htmlFor="fw-lan" hint="Eén netwerk per regel. Bron “eigen netwerk” in het plan.">
          <Textarea id="fw-lan" rows={4} disabled={!isAdmin} value={form.lanText} onChange={(e) => setForm({ ...form, lanText: e.target.value })} />
        </Field>
        <Field label="Beheernetwerken (admin)" htmlFor="fw-admin" hint="Bv. 192.168.90.0/24 voor je pc. Leeg = de eigen netwerken.">
          <Textarea id="fw-admin" rows={4} disabled={!isAdmin} value={form.adminText} onChange={(e) => setForm({ ...form, adminText: e.target.value })} />
        </Field>
        <Field label="SSH vanaf" htmlFor="fw-ssh" hint="SSH gaat nooit dicht; dit kiest alleen voor wie.">
          <Select id="fw-ssh" disabled={!isAdmin} value={form.ssh_from} onChange={(e) => setForm({ ...form, ssh_from: e.target.value as "lan" | "admin" | "any" })}>
            <option value="lan">eigen netwerken</option>
            <option value="admin">beheernetwerken</option>
            <option value="any">iedereen</option>
          </Select>
        </Field>
        <Field label="Onbekende poorten" htmlFor="fw-unknown" hint="Software die de catalogus niet kent en op een poort luistert.">
          <Select id="fw-unknown" disabled={!isAdmin} value={form.unknown_ports} onChange={(e) => setForm({ ...form, unknown_ports: e.target.value as "lan" | "closed" })}>
            <option value="lan">open voor de eigen netwerken</option>
            <option value="closed">dicht</option>
          </Select>
        </Field>
        <Field label="Wachttijd bevestigen (seconden)" htmlFor="fw-confirm" hint="Zonder bevestiging zet de node de vorige regels vanzelf terug.">
          <Input id="fw-confirm" type="number" min={30} max={900} disabled={!isAdmin} value={form.confirm_seconds} onChange={(e) => setForm({ ...form, confirm_seconds: Number(e.target.value) })} />
        </Field>
        <Field label="Poorten onthouden (uren)" htmlFor="fw-grace" hint="Zo lang blijft een poort open nadat er niets meer op luistert.">
          <Input id="fw-grace" type="number" min={0} max={720} disabled={!isAdmin} value={form.grace_hours} onChange={(e) => setForm({ ...form, grace_hours: Number(e.target.value) })} />
        </Field>
      </div>
      <div className="mt-4 space-y-3">
        {check("auto_apply", "Automatisch toepassen", "Op beheerde nodes een gewijzigd plan (nieuwe software, andere instellingen) vanzelf toepassen, met controle en terugdraaitimer.")}
        {check("strict", "Strikt", "Regels die niet van ClusterForge zijn, weghalen. Het voorbeeld toont ze altijd eerst.")}
        {check("monitor", "Verkeer naar dichte poorten tellen", "Een NFLOG-regel in ufw; de agent telt per bron en poort.")}
      </div>
      {msg && (
        <div className="mt-3">
          <Alert kind={msg.ok ? "success" : "error"}>{msg.text}</Alert>
        </div>
      )}
      {isAdmin && (
        <div className="mt-4">
          <Button disabled={save.isPending} onClick={submit}>
            Bewaren
          </Button>
        </div>
      )}
      {!isAdmin && <p className="mt-3 text-sm text-slate-500">Alleen een beheerder kan dit wijzigen.</p>}
    </Card>
  );
}
