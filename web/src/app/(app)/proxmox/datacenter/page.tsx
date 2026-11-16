"use client";

import { DiskHealthSummary } from "@/components/pve/Disks";
import Link from "next/link";
import { Suspense, useState } from "react";
import { askConfirm } from "@/components/Confirm";
import { Empty, QueryState, tableClass, tdClass, thClass } from "@/components/inventory/bits";
import { ActionMenu, DatacenterGate, gb, Meter, TypedConfirm, uptime, useCallRunner } from "@/components/pve/bits";
import { PveTaskTable } from "@/components/pve/Tasks";
import { Alert, Badge, Card, cx, PageHeader } from "@/components/ui";
import { useIsAdmin } from "@/lib/inventory";
import { pct, pveHostAction, usePveGet, usePveOverview, type Datacenter, type Overview, type OverviewNode, type PveTask } from "@/lib/pve";
import { useQueryClient } from "@tanstack/react-query";
import { useRouter } from "next/navigation";

export default function DatacenterPage() {
  return (
    <div className="space-y-6">
      <PageHeader
        title="Datacenter"
        description="Het Proxmox-cluster in één blik: quorum en corosync, de hosts, wat er draait, storage, HA en of de capaciteit klopt. Alles via de ClusterForge-agent op de hosts."
      />
      <Suspense>
        <DatacenterGate>{(dc) => <DatacenterView key={dc.id} dc={dc} />}</DatacenterGate>
      </Suspense>
    </div>
  );
}

function DatacenterView({ dc }: { dc: Datacenter }) {
  const o = usePveOverview(dc.id);
  return (
    <QueryState q={o}>
      {o.data && (
        <div className="space-y-6">
          <Strip o={o.data} />
          {o.data.warnings.length > 0 && <WarningsCard o={o.data} />}
          <DiskHealthSummary dc={dc} />
          <HostsCard dc={dc} o={o.data} />
          <div className="grid gap-6 lg:grid-cols-2">
            <CapacityCard o={o.data} />
            <StorageCard o={o.data} />
          </div>
          <RecentTasks dc={dc} />
          {o.data.errors.length > 0 && (
            <p className="text-xs text-slate-500">Niet gelezen: {o.data.errors.join(" · ")}</p>
          )}
        </div>
      )}
    </QueryState>
  );
}

function Tile({ label, value, sub, tone }: { label: string; value: string; sub?: string; tone?: "red" | "amber" | "green" }) {
  return (
    <div className="rounded-lg border border-slate-200 bg-white p-3 dark:border-slate-800 dark:bg-slate-900">
      <div className="text-xs font-medium tracking-wide text-slate-500 uppercase">{label}</div>
      <div
        className={cx(
          "mt-1 text-xl font-semibold tabular-nums",
          tone === "red" && "text-red-600 dark:text-red-400",
          tone === "amber" && "text-amber-600 dark:text-amber-400",
          tone === "green" && "text-emerald-600 dark:text-emerald-400",
        )}
      >
        {value}
      </div>
      {sub && <div className="mt-0.5 text-xs text-slate-500">{sub}</div>}
    </div>
  );
}

function Strip({ o }: { o: Overview }) {
  const online = o.nodes.filter((n) => n.online).length;
  const crit = o.warnings.filter((w) => w.level === "crit").length;
  return (
    <div className="grid grid-cols-2 gap-3 md:grid-cols-3 xl:grid-cols-6">
      <Tile
        label="Quorum"
        value={o.datacenter.standalone ? "Losse host" : o.quorate ? "OK" : "Weg"}
        sub={o.datacenter.standalone ? undefined : `${o.name}${o.qdevice ? " · met QDevice" : ""}`}
        tone={o.datacenter.standalone ? undefined : o.quorate ? "green" : "red"}
      />
      <Tile label="Hosts" value={`${online} / ${o.nodes.length}`} sub="online" tone={online < o.nodes.length ? "red" : undefined} />
      <Tile label="Gasten" value={`${o.running} / ${o.vms + o.cts}`} sub={`draaien · ${o.vms} VM, ${o.cts} CT`} />
      <Tile
        label="Geheugen"
        value={`${pct(o.capacity.mem_used, o.capacity.mem_total)}%`}
        sub={`${gb(o.capacity.mem_used, 0)} van ${gb(o.capacity.mem_total, 0)} in gebruik`}
      />
      <Tile
        label="HA"
        value={o.ha.enabled ? `${o.ha.resources}` : "Uit"}
        sub={o.ha.enabled ? `resources${o.ha.master ? ` · master ${o.ha.master}` : ""}` : "geen resources"}
        tone={o.ha.problems.length > 0 ? "red" : undefined}
      />
      <Tile
        label="Aandacht"
        value={o.warnings.length === 0 ? "Niets" : String(o.warnings.length)}
        sub={crit > 0 ? `${crit} ernstig` : o.warnings.length > 0 ? "waarschuwingen" : "alles in orde"}
        tone={crit > 0 ? "red" : o.warnings.length > 0 ? "amber" : "green"}
      />
    </div>
  );
}

function WarningsCard({ o }: { o: Overview }) {
  return (
    <Card title="Aandacht nodig">
      <ul className="space-y-1.5 text-sm">
        {o.warnings.map((w) => (
          <li key={w.text} className="flex items-start gap-2">
            <Badge tone={w.level === "crit" ? "red" : "amber"}>{w.level === "crit" ? "Ernstig" : "Let op"}</Badge>
            <span>{w.text}</span>
          </li>
        ))}
      </ul>
    </Card>
  );
}

type HostDialog = { node: OverviewNode; kind: "reboot" | "shutdown" | "stopall" } | null;

function HostsCard({ dc, o }: { dc: Datacenter; o: Overview }) {
  const isAdmin = useIsAdmin();
  const { run, pending, banner } = useCallRunner(dc.id);
  const [dialog, setDialog] = useState<HostDialog>(null);
  const qc = useQueryClient();
  const router = useRouter();
  const [maintResult, setMaintResult] = useState<{ ok: boolean; text: string } | null>(null);
  const maintenance = async (n: OverviewNode, on: boolean) => {
    const ok = await askConfirm(
      on
        ? `${n.name} in onderhoudsmodus zetten? HA verhuist de gasten onder HA naar andere hosts en zet er niets meer op tot de onderhoudsmodus uit is.`
        : `${n.name} uit onderhoudsmodus halen? HA mag er dan weer gasten op zetten.`,
      { confirmLabel: on ? "Onderhoud aan" : "Onderhoud uit" },
    );
    if (!ok || !n.agent_node_id) return;
    try {
      const j = await pveHostAction(n.agent_node_id, { op: { action: "maintenance", enable: on } });
      setMaintResult({ ok: true, text: `Taak gestart: ${j.title}.` });
      qc.invalidateQueries({ queryKey: ["jobs"] });
      setTimeout(() => qc.invalidateQueries({ queryKey: ["pve"] }), 5_000);
    } catch (e) {
      setMaintResult({ ok: false, text: e instanceof Error ? e.message : "Mislukt" });
    }
  };

  return (
    <Card title="Hosts">
      <div className="space-y-3">
        {banner}
        {maintResult && <Alert kind={maintResult.ok ? "success" : "error"}>{maintResult.text}</Alert>}
        <div className="relative -mx-4 overflow-x-auto sm:mx-0">
          <table className={tableClass}>
            <thead>
              <tr>
                <th className={thClass}>Host</th>
                <th className={thClass}>CPU</th>
                <th className={thClass}>Geheugen</th>
                <th className={thClass}>Gasten</th>
                <th className={cx(thClass, "hidden md:table-cell")}>Versie</th>
                <th className={cx(thClass, "hidden lg:table-cell")}>Corosync</th>
                <th className={thClass}>
                  <span className="sr-only">Acties</span>
                </th>
              </tr>
            </thead>
            <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
              {o.nodes.map((n) => (
                <tr key={n.name}>
                  <td className={tdClass}>
                    <div className="flex flex-wrap items-center gap-2">
                      <span className="font-medium">{n.name}</span>
                      {!n.online ? (
                        <Badge tone="red">Offline</Badge>
                      ) : n.lrm?.includes("maintenance") ? (
                        <Badge tone="amber">Onderhoud</Badge>
                      ) : (
                        <Badge tone="green">Online</Badge>
                      )}
                      {!n.has_agent && <Badge tone="slate">Geen agent</Badge>}
                      {n.reboot_required && (
                        <span title={(n.reboot_reasons ?? []).join("; ")}>
                          <Badge tone="amber">Herstart nodig</Badge>
                        </span>
                      )}
                    </div>
                    <div className="mt-0.5 text-xs text-slate-500">
                      {[n.ip, n.online && `up ${uptime(n.uptime)}`, n.load?.length ? `load ${n.load.join(" ")}` : ""].filter(Boolean).join(" · ")}
                    </div>
                  </td>
                  <td className={cx(tdClass, "min-w-32")}>
                    <Meter value={n.cpu * 100} warn={80} crit={95} label={`CPU van ${n.name}`} />
                    <div className="mt-0.5 text-xs text-slate-500" title="vCPU's van draaiende gasten op het aantal threads">
                      {n.vcpu_running} vCPU op {n.cores} threads
                    </div>
                  </td>
                  <td className={cx(tdClass, "min-w-40")}>
                    <Meter value={pct(n.mem, n.maxmem)} label={`Geheugen van ${n.name}`} />
                    <div className="mt-0.5 text-xs text-slate-500">
                      {gb(n.mem, 0)} / {gb(n.maxmem, 0)} · toegewezen {pct(n.mem_assigned_running, n.maxmem)}%
                    </div>
                  </td>
                  <td className={cx(tdClass, "tabular-nums")}>
                    {n.running} / {n.guests}
                  </td>
                  <td className={cx(tdClass, "hidden text-xs text-slate-500 md:table-cell")}>
                    {n.pve && <div>PVE {n.pve}</div>}
                    {n.kernel && <div className="font-mono">{n.kernel}</div>}
                    {n.cpu_model && <div className="max-w-56 truncate" title={n.cpu_model}>{n.cpu_model}</div>}
                  </td>
                  <td className={cx(tdClass, "hidden text-xs text-slate-500 lg:table-cell")}>
                    {o.datacenter.standalone ? "–" : `${n.votes || 0} stem${n.votes === 1 ? "" : "men"}`}
                    {n.links?.map((l) => (
                      <div key={l} className="font-mono">
                        {l}
                      </div>
                    ))}
                  </td>
                  <td className={cx(tdClass, "text-right")}>
                    {isAdmin && n.online && (
                      <ActionMenu
                        disabled={pending}
                        items={[
                          { label: "Hardware en passthrough", onClick: () => router.push(`/proxmox/hardware?dc=${encodeURIComponent(dc.id)}&node=${encodeURIComponent(n.name)}`) },
                          ...(n.agent_node_id
                            ? [
                                {
                                  label: n.lrm?.includes("maintenance") ? "Onderhoudsmodus uit" : "Onderhoudsmodus aan",
                                  onClick: () => void maintenance(n, !n.lrm?.includes("maintenance")),
                                },
                              ]
                            : []),
                          {
                            label: "Alle gasten starten",
                            onClick: async () => {
                              if (await askConfirm(`Alle gasten met "starten bij opstarten" op ${n.name} starten?`))
                                void run({ method: "create", path: `/nodes/${n.name}/startall` });
                            },
                          },
                          { label: "Alle gasten stoppen…", onClick: () => setDialog({ node: n, kind: "stopall" }), danger: true },
                          { label: "Herstarten…", onClick: () => setDialog({ node: n, kind: "reboot" }), danger: true },
                          { label: "Uitzetten…", onClick: () => setDialog({ node: n, kind: "shutdown" }), danger: true },
                        ]}
                      />
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
        <p className="text-xs text-slate-500">
          Alle VM&apos;s en CT&apos;s beheren, aanmaken en taggen:{" "}
          <Link href="/proxmox/gasten" className="underline">
            Gasten
          </Link>
          .
        </p>
      </div>
      {dialog && (
        <TypedConfirm
          title={dialog.kind === "reboot" ? `${dialog.node.name} herstarten` : dialog.kind === "shutdown" ? `${dialog.node.name} uitzetten` : `Alle gasten op ${dialog.node.name} stoppen`}
          expect={dialog.node.name}
          confirmLabel={dialog.kind === "reboot" ? "Herstarten" : dialog.kind === "shutdown" ? "Uitzetten" : "Alles stoppen"}
          pending={pending}
          onClose={() => setDialog(null)}
          onConfirm={async (typed) => {
            const ok =
              dialog.kind === "stopall"
                ? await run({ method: "create", path: `/nodes/${dialog.node.name}/stopall`, confirm: typed })
                : await run({ method: "create", path: `/nodes/${dialog.node.name}/status`, params: { command: dialog.kind }, confirm: typed });
            if (ok) setDialog(null);
          }}
        >
          {dialog.kind === "stopall" ? (
            <p>Alle {dialog.node.running} draaiende gasten op deze host worden afgesloten.</p>
          ) : (
            <>
              <p>
                {dialog.node.running > 0
                  ? `Er draaien ${dialog.node.running} gasten op ${dialog.node.name}. Gasten onder HA verhuizen eerst (of stoppen, afhankelijk van de HA-instelling); de rest wordt afgesloten.`
                  : `Er draaien geen gasten op ${dialog.node.name}.`}
              </p>
              {!o.datacenter.standalone && o.nodes.filter((x) => x.online).length <= 2 && (
                <p className="text-red-700 dark:text-red-300">Met deze host weg kan het cluster zijn quorum verliezen.</p>
              )}
              <p className="text-slate-500">Zet de host eerst in onderhoudsmodus als je HA-gasten vooraf wil laten verhuizen.</p>
            </>
          )}
        </TypedConfirm>
      )}
    </Card>
  );
}

function CapacityCard({ o }: { o: Overview }) {
  const c = o.capacity;
  return (
    <Card title="Capaciteit">
      <div className="space-y-4 text-sm">
        <div>
          <div className="mb-1 flex justify-between text-xs text-slate-500">
            <span>Geheugen in gebruik</span>
            <span>
              {gb(c.mem_used, 0)} / {gb(c.mem_total, 0)}
            </span>
          </div>
          <Meter value={pct(c.mem_used, c.mem_total)} label="Geheugen in gebruik" />
        </div>
        <div>
          <div className="mb-1 flex justify-between text-xs text-slate-500">
            <span>Toegewezen aan draaiende gasten</span>
            <span>{gb(c.mem_assigned_running, 0)}</span>
          </div>
          <Meter value={pct(c.mem_assigned_running, c.mem_total)} warn={90} crit={100} label="Toegewezen geheugen" />
          <div className="mt-1 text-xs text-slate-500">Met alle gasten aan: {gb(c.mem_assigned_all, 0)} ({pct(c.mem_assigned_all, c.mem_total)}%)</div>
        </div>
        <div className="flex justify-between">
          <span className="text-slate-500">vCPU&apos;s</span>
          <span className="tabular-nums">
            {c.vcpu_running} draaiend · {c.vcpu_all} totaal op {c.cores} threads ({c.cores ? (c.vcpu_running / c.cores).toFixed(1) : "–"} : 1)
          </span>
        </div>
        {!o.datacenter.standalone && (
          <div
            className={cx(
              "rounded-md p-3",
              c.failover ? "bg-emerald-50 text-emerald-800 dark:bg-emerald-950/40 dark:text-emerald-200" : "bg-amber-50 text-amber-900 dark:bg-amber-950/40 dark:text-amber-200",
            )}
          >
            {c.failover
              ? "Valt de grootste host uit, dan past alles wat nu draait nog op de andere hosts."
              : `Valt ${c.failover_host} uit, dan is er ${gb(c.failover_short ?? 0)} geheugen tekort om alles wat draait elders te starten.`}
          </div>
        )}
      </div>
    </Card>
  );
}

function StorageCard({ o }: { o: Overview }) {
  return (
    <Card title="Storage">
      {o.storages.length === 0 ? (
        <Empty>Geen storage.</Empty>
      ) : (
        <ul className="space-y-3 text-sm">
          {o.storages.map((s) => (
            <li key={s.name + (s.node ?? "")}>
              <div className="flex flex-wrap items-baseline justify-between gap-2">
                <span className="font-medium">
                  {s.name}
                  <span className="ml-1 text-xs font-normal text-slate-500">
                    {s.type}
                    {s.shared ? " · gedeeld" : s.node ? ` · ${s.node}` : ""}
                  </span>
                </span>
                <span className="text-xs text-slate-500 tabular-nums">
                  {s.total > 0 ? `${gb(s.used, 0)} / ${gb(s.total, 0)}` : s.status}
                </span>
              </div>
              {s.total > 0 && <Meter value={pct(s.used, s.total)} label={`Storage ${s.name}`} />}
            </li>
          ))}
        </ul>
      )}
    </Card>
  );
}

function RecentTasks({ dc }: { dc: Datacenter }) {
  const tasks = usePveGet<PveTask[]>(dc.id, "/cluster/tasks", { refetch: 10_000 });
  return (
    <Card
      title={
        <div className="flex items-center justify-between gap-2">
          <span>Recente Proxmox-taken</span>
          <Link href={`/proxmox/taken?dc=${encodeURIComponent(dc.id)}`} className="text-sm font-normal text-brand-700 hover:underline dark:text-sky-300">
            Alle taken →
          </Link>
        </div>
      }
    >
      <QueryState q={tasks}>{tasks.data && <PveTaskTable dc={dc.id} tasks={[...tasks.data].sort((a, b) => b.starttime - a.starttime).slice(0, 8)} compact />}</QueryState>
    </Card>
  );
}
