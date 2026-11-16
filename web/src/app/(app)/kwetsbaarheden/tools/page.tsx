"use client";

import Link from "next/link";
import { useRouter, useSearchParams } from "next/navigation";
import { Suspense, useState } from "react";
import { Tabs } from "@/components/Tabs";
import { ago, tableClass, tdClass, thClass } from "@/components/inventory/bits";
import { Alert, Badge, Button, Card, PageHeader, cx } from "@/components/ui";
import { useIsAdmin } from "@/lib/inventory";
import {
  useSecurityTools,
  useToolAction,
  useToolSchedule,
  type ToolAction,
  type ToolID,
  type ToolInfo,
  type ToolRow,
  type ToolsNode,
  type ToolsOverview,
} from "@/lib/security";

// Tools (fase 27 M7): beveiligingstools op de nodes installeren, draaien en
// plannen via de agents. De agent kent zelf de pakketten en commando's.

const days = ["zondag", "maandag", "dinsdag", "woensdag", "donderdag", "vrijdag", "zaterdag"];

export default function ToolsPage() {
  return (
    <Suspense>
      <Tools />
    </Suspense>
  );
}

function Tools() {
  const params = useSearchParams();
  const router = useRouter();
  const data = useSecurityTools();
  const isAdmin = useIsAdmin();
  const o = data.data;
  const tool = (o?.tools.find((t) => t.id === params.get("tool"))?.id ?? "clamav") as ToolID;
  return (
    <div className="space-y-6">
      <PageHeader
        title="Tools"
        description="Beveiligingstools op je nodes, zonder in te loggen: installeren, nu draaien of plannen. Alles uit de Debian-bronnen, Trivy uit de bron van Aqua Security. Wat ze vinden, komt als bevinding in het rapport en in Aandacht nodig."
      />
      {data.isError && <Alert>Tools laden mislukte.</Alert>}
      {o && (
        <>
          <Tabs
            tabs={o.tools.map((t) => ({ key: t.id, label: t.name }))}
            value={tool}
            onChange={(t) => router.replace(`/kwetsbaarheden/tools?tool=${t}`)}
            label="Tools"
          />
          <ToolSection o={o} info={o.tools.find((t) => t.id === tool)!} isAdmin={isAdmin} />
        </>
      )}
    </div>
  );
}

function ToolSection({ o, info, isAdmin }: { o: ToolsOverview; info: ToolInfo; isAdmin: boolean }) {
  const action = useToolAction();
  const [bulk, setBulk] = useState<string | null>(null);
  const old = o.nodes.filter((n) => n.protocol < o.protocol).length;
  const fits = (n: ToolsNode) => !(info.no_lxc && n.virtualization === "lxc") && (!info.docker || n.docker);
  const nodes = o.nodes.filter(fits);
  const ready = nodes.filter((n) => n.online && n.protocol >= o.protocol);
  const row = (n: ToolsNode) => n.tools.find((t) => t.tool === info.id)!;
  const notInstalled = ready.filter((n) => !row(n).installed && !row(n).running);
  const installed = ready.filter((n) => row(n).installed && !row(n).running);
  const runAll = async (list: ToolsNode[], act: ToolAction) => {
    setBulk(null);
    let ok = 0;
    for (const n of list) {
      try {
        await action.mutateAsync({ node: n.node_id, tool: info.id, action: act });
        ok++;
      } catch {
        /* de rij toont de fout bij de volgende stand */
      }
    }
    setBulk(`${ok} van ${list.length} gestart.`);
  };
  return (
    <div className="space-y-4">
      <p className="text-sm text-slate-600 dark:text-slate-400">{info.description}</p>
      {old > 0 && (
        <Alert kind="info">
          {old} {old === 1 ? "node heeft" : "nodes hebben"} een agent ouder dan protocol {o.protocol}. Werk ze bij via{" "}
          <Link href="/nodes" className="font-medium underline">
            Nodes → Agents bijwerken
          </Link>
          .
        </Alert>
      )}
      {(info.no_lxc || info.docker) && (
        <p className="text-xs text-slate-500">
          {info.no_lxc && "Niet in LXC-containers (de kernel is van de Proxmox-host); zet het op de VM's en de Proxmox-hosts. "}
          {info.docker && "Alleen op nodes met Docker. "}
          {o.nodes.length - nodes.length > 0 && `${o.nodes.length - nodes.length} nodes vallen daarom weg.`}
        </p>
      )}
      {isAdmin && (
        <div className="flex flex-wrap items-center gap-2">
          <Button variant="secondary" disabled={notInstalled.length === 0 || action.isPending} onClick={() => runAll(notInstalled, "install")}>
            Installeren op {notInstalled.length} {notInstalled.length === 1 ? "node" : "nodes"}
          </Button>
          {info.run && (
            <Button variant="secondary" disabled={installed.length === 0 || action.isPending} onClick={() => runAll(installed, "run")}>
              Nu draaien op {installed.length} {installed.length === 1 ? "node" : "nodes"}
            </Button>
          )}
          {bulk && <span className="text-sm text-slate-500">{bulk}</span>}
        </div>
      )}
      {action.isError && <Alert>{(action.error as Error).message}</Alert>}
      <Card>
        {nodes.length === 0 ? (
          <p className="text-sm text-slate-500">Geen nodes waar {info.name} op past.</p>
        ) : (
          <div className="overflow-x-auto">
            <table className={tableClass}>
              <thead>
                <tr>
                  <th className={thClass}>Node</th>
                  <th className={thClass}>Stand</th>
                  <th className={thClass}>{info.run ? "Laatste run" : "Regels"}</th>
                  {info.run && <th className={thClass}>Planning</th>}
                  {isAdmin && <th className={thClass}>Acties</th>}
                </tr>
              </thead>
              <tbody>
                {nodes.map((n) => (
                  <NodeRow key={n.node_id} o={o} n={n} r={row(n)} info={info} isAdmin={isAdmin} />
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Card>
    </div>
  );
}

function NodeRow({ o, n, r, info, isAdmin }: { o: ToolsOverview; n: ToolsNode; r: ToolRow; info: ToolInfo; isAdmin: boolean }) {
  const action = useToolAction();
  const ready = n.online && n.protocol >= o.protocol;
  const busy = !!r.running || action.isPending;
  const install = r.state.last?.install;
  const installFailed = !r.installed && install?.exit !== undefined && install.exit !== 0;
  const act = (a: ToolAction) => action.mutate({ node: n.node_id, tool: info.id, action: a });
  return (
    <tr className="border-t border-slate-100 align-top dark:border-slate-800">
      <td className={tdClass}>
        <div className="font-medium">{n.hostname}</div>
        <div className="text-xs text-slate-500">
          {n.cluster_name || "geen cluster"}
          {n.virtualization ? ` · ${n.virtualization}` : ""}
        </div>
      </td>
      <td className={tdClass}>
        {r.running ? (
          <Badge tone="blue">bezig: {actionLabel[r.running as ToolAction] ?? r.running}</Badge>
        ) : r.installed ? (
          <span>
            <Badge tone="green">geïnstalleerd</Badge> <span className="text-xs text-slate-500">{r.version}</span>
          </span>
        ) : (
          <span className="text-slate-500">niet geïnstalleerd</span>
        )}
        {installFailed && (
          <details className="mt-1 text-xs text-red-600">
            <summary className="cursor-pointer">installeren mislukte (exit {install.exit})</summary>
            <pre className="mt-1 max-w-md overflow-x-auto whitespace-pre-wrap text-slate-600 dark:text-slate-400">{install.tail}</pre>
          </details>
        )}
        {!ready && (
          <div className="text-xs text-amber-700 dark:text-amber-400">{n.online ? `agent ouder dan protocol ${o.protocol}` : "offline"}</div>
        )}
        {n.error && <div className="text-xs text-red-600">{n.error}</div>}
      </td>
      <td className={tdClass}>{info.run ? <LastRun r={r} tool={info.id} /> : <AuditdState r={r} />}</td>
      {info.run && <td className={tdClass}>{isAdmin && r.installed ? <Schedule n={n} r={r} /> : <ScheduleText r={r} />}</td>}
      {isAdmin && (
        <td className={tdClass}>
          <div className="flex flex-wrap gap-1">
            {!r.installed && (
              <Button variant="secondary" disabled={!ready || busy} onClick={() => act("install")}>
                Installeren
              </Button>
            )}
            {r.installed && info.run && (
              <Button variant="secondary" disabled={!ready || busy} onClick={() => act("run")}>
                Nu draaien
              </Button>
            )}
            {r.installed && info.baseline && (
              <Button
                variant="secondary"
                disabled={!ready || busy}
                onClick={() => act("baseline")}
                title="Na een verwachte wijziging, zoals een update"
              >
                Basislijn bijwerken
              </Button>
            )}
            {r.installed && (
              <Button variant="secondary" disabled={!ready || busy} onClick={() => act("remove")}>
                Verwijderen
              </Button>
            )}
          </div>
          {action.isError && <div className="mt-1 text-xs text-red-600">{(action.error as Error).message}</div>}
        </td>
      )}
    </tr>
  );
}

const actionLabel: Record<ToolAction, string> = { install: "installeren", run: "draait", baseline: "basislijn", remove: "verwijderen" };

function LastRun({ r, tool }: { r: ToolRow; tool: ToolID }) {
  const last = r.state.last?.run;
  const base = r.state.last?.baseline;
  if (!last?.started) return <span className="text-slate-500">nog niet</span>;
  if (!last.ended) return <span className="text-slate-500">gestart {ago(last.started)}</span>;
  const items = last.items ?? [];
  const images = (last.images ?? []).filter((i) => (i.counts.critical ?? 0) + (i.counts.high ?? 0) > 0 || i.error);
  const failed = tool === "aide" ? (last.exit ?? 0) >= 14 : tool === "lynis" || tool === "trivy" ? last.exit !== 0 : (last.exit ?? 0) > 1;
  return (
    <div className="space-y-1">
      <div className="text-xs text-slate-500">{ago(last.ended)}</div>
      {failed ? (
        <details className="text-xs text-red-600">
          <summary className="cursor-pointer">mislukt (exit {last.exit})</summary>
          <pre className="mt-1 max-w-md overflow-x-auto whitespace-pre-wrap text-slate-600 dark:text-slate-400">{last.tail}</pre>
        </details>
      ) : tool === "trivy" ? (
        images.length === 0 ? (
          <Badge tone="green">{(last.images ?? []).length} images, niets hoog of kritiek</Badge>
        ) : (
          <details>
            <summary className="cursor-pointer text-sm">
              {images.length} van {(last.images ?? []).length} images met hoge of kritieke kwetsbaarheden
            </summary>
            <ul className="mt-1 space-y-1 text-xs">
              {images.map((i) => (
                <li key={i.image}>
                  <span className="font-mono">{i.image}</span>:{" "}
                  {i.error ? (
                    <span className="text-red-600">{i.error}</span>
                  ) : (
                    <>
                      {i.counts.critical ?? 0} kritiek, {i.counts.high ?? 0} hoog
                      <span className="block text-slate-500">
                        {i.vulns
                          .slice(0, 5)
                          .map((v) => `${v.id} (${v.pkg}${v.fixed ? ` → ${v.fixed}` : ""})`)
                          .join(" · ")}
                      </span>
                    </>
                  )}
                </li>
              ))}
            </ul>
          </details>
        )
      ) : tool === "lynis" ? (
        <span className="text-xs">
          Rapport bij de node onder{" "}
          <Link href="/kwetsbaarheden/verharding" className="underline">
            Verharding
          </Link>
        </span>
      ) : items.length === 0 ? (
        <Badge tone="green">niets gevonden</Badge>
      ) : (
        <details>
          <summary className="cursor-pointer text-sm">
            <Badge tone={tool === "aide" ? "amber" : tool === "chkrootkit" && !items.some((i) => i.includes("INFECTED")) ? "slate" : "red"}>
              {items.length} {items.length === 1 ? "vondst" : "vondsten"}
            </Badge>
          </summary>
          <ul className="mt-1 max-w-lg space-y-0.5 font-mono text-xs">
            {items.map((i) => (
              <li key={i} className="break-all">
                {i}
              </li>
            ))}
          </ul>
        </details>
      )}
      {base?.ended && <div className="text-xs text-slate-500">basislijn {ago(base.ended)}</div>}
    </div>
  );
}

function AuditdState({ r }: { r: ToolRow }) {
  if (!r.installed) return <span className="text-slate-500">–</span>;
  return (
    <span className="text-sm">
      <Badge tone={r.state.active ? "green" : "red"}>{r.state.active ? "actief" : "staat stil"}</Badge>{" "}
      <span className="text-xs text-slate-500">{r.state.rules ?? 0} regels geladen</span>
    </span>
  );
}

function ScheduleText({ r }: { r: ToolRow }) {
  if (r.schedule === "uit") return <span className="text-slate-500">niet gepland</span>;
  return (
    <span className="text-sm">
      {r.schedule === "dagelijks" ? "dagelijks" : `elke ${days[r.weekday]}`} om {r.hour}:00
    </span>
  );
}

function Schedule({ n, r }: { n: ToolsNode; r: ToolRow }) {
  const save = useToolSchedule();
  const set = (patch: Partial<Pick<ToolRow, "schedule" | "weekday" | "hour">>) =>
    save.mutate({ node: n.node_id, tool: r.tool, body: { schedule: r.schedule, weekday: r.weekday, hour: r.hour, ...patch } });
  const sel = "rounded border border-slate-300 bg-white px-1 py-0.5 text-sm dark:border-slate-700 dark:bg-slate-900";
  return (
    <div className="flex flex-wrap items-center gap-1">
      <select className={sel} value={r.schedule} onChange={(e) => set({ schedule: e.target.value as ToolRow["schedule"] })} aria-label="Planning">
        <option value="uit">niet gepland</option>
        <option value="dagelijks">dagelijks</option>
        <option value="wekelijks">wekelijks</option>
      </select>
      {r.schedule === "wekelijks" && (
        <select className={sel} value={r.weekday} onChange={(e) => set({ weekday: Number(e.target.value) })} aria-label="Dag">
          {days.map((d, i) => (
            <option key={d} value={i}>
              {d}
            </option>
          ))}
        </select>
      )}
      {r.schedule !== "uit" && (
        <select className={sel} value={r.hour} onChange={(e) => set({ hour: Number(e.target.value) })} aria-label="Uur">
          {Array.from({ length: 24 }, (_, h) => (
            <option key={h} value={h}>
              {h}:00
            </option>
          ))}
        </select>
      )}
      {save.isError && <span className="text-xs text-red-600">{(save.error as Error).message}</span>}
      {r.next_run_at && (
        <span className={cx("text-xs text-slate-500")}>
          volgende {new Date(r.next_run_at).toLocaleString("nl-BE", { weekday: "short", hour: "2-digit", minute: "2-digit" })}
        </span>
      )}
    </div>
  );
}
