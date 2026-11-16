"use client";

import Link from "next/link";
import { useSearchParams } from "next/navigation";
import { Suspense, useState } from "react";
import { askConfirm } from "@/components/Confirm";
import { formatBytes, QueryState } from "@/components/inventory/bits";
import { DatacenterGate, Meter, TypedConfirm, uptime, useCallRunner } from "@/components/pve/bits";
import { CloneDialog, MigrateDialog } from "@/components/pve/guest/Move";
import { CloudInitTab, HardwareTab, OptionsTab } from "@/components/pve/guest/Config";
import { PassthroughTab } from "@/components/pve/guest/Passthrough";
import { SnapshotsTab } from "@/components/pve/guest/Snapshots";
import { PveTaskTable } from "@/components/pve/Tasks";
import { BackupList } from "@/components/pve/Backups";
import { TagChips, useTagColors } from "@/components/pve/TagChips";
import { Tabs } from "@/components/Tabs";
import { Alert, Badge, Button, Card, PageHeader } from "@/components/ui";
import { useIsAdmin } from "@/lib/inventory";
import { consoleHref, guestLabel, guestPath, pct, pveHostAction, splitTags, usePveGet, type Datacenter, type GuestConfig, type GuestType, type PveResource, type PveTask } from "@/lib/pve";
import { vmStatusInfo } from "@/lib/proxmox";

export default function GuestPage() {
  return (
    <Suspense>
      <DatacenterGate>{(dc) => <GuestInner dc={dc} />}</DatacenterGate>
    </Suspense>
  );
}

export type GuestStatus = {
  status: string;
  qmpstatus?: string;
  name?: string;
  cpu?: number;
  cpus?: number;
  mem?: number;
  maxmem?: number;
  disk?: number;
  maxdisk?: number;
  netin?: number;
  netout?: number;
  diskread?: number;
  diskwrite?: number;
  uptime?: number;
  lock?: string;
  agent?: number;
  ha?: { managed?: number; state?: string; group?: string };
  template?: number | string;
};

export type GuestCtx = {
  dc: Datacenter;
  node: string;
  type: GuestType;
  vmid: number;
  path: string;
  config: GuestConfig;
  status?: GuestStatus;
  running: boolean;
  isAdmin: boolean;
  run: ReturnType<typeof useCallRunner>["run"];
  pending: boolean;
};

type Tab = "overview" | "hardware" | "cloudinit" | "options" | "snapshots" | "backups" | "passthrough" | "tasks";

function GuestInner({ dc }: { dc: Datacenter }) {
  const sp = useSearchParams();
  const node = sp.get("node") ?? "";
  const type = (sp.get("type") === "lxc" ? "lxc" : "qemu") as GuestType;
  const vmid = Number(sp.get("vmid") ?? 0);
  const path = guestPath(node, type, vmid);
  const config = usePveGet<GuestConfig>(dc.id, path + "/config", { refetch: 10_000, enabled: vmid > 0 });
  const status = usePveGet<GuestStatus>(dc.id, path + "/status/current", { refetch: 5_000, enabled: vmid > 0 });
  const isAdmin = useIsAdmin();
  const { run, pending, banner } = useCallRunner(dc.id);
  const tabKeys: Tab[] = ["overview", "hardware", "cloudinit", "options", "snapshots", "backups", "passthrough", "tasks"];
  const [tab, setTab] = useState<Tab>(tabKeys.find((t) => t === sp.get("tab")) ?? "overview");
  const [dialog, setDialog] = useState<"migrate" | "clone" | "template" | "destroy" | "stop" | null>(null);
  const [destroyMsg, setDestroyMsg] = useState<string | null>(null);
  const colors = useTagColors(dc.id);

  if (!node || !vmid) return <Alert>Geen gast opgegeven.</Alert>;
  const cfg = config.data ?? {};
  const st = status.data;
  const running = st?.status === "running";
  const template = cfg.template === 1 || cfg.template === "1";
  const name = String(cfg.name ?? cfg.hostname ?? st?.name ?? "");
  const hasCloudInit = type === "qemu" && Object.values(cfg).some((v) => String(v).includes("cloudinit"));
  const ctx: GuestCtx = { dc, node, type, vmid, path, config: cfg, status: st, running, isAdmin, run, pending };
  const hostAgent = dc.hosts.find((h) => h.pve_node === node);

  const power = async (cmd: string, ask?: string) => {
    if (ask && !(await askConfirm(ask))) return;
    void run({ method: "create", path: `${path}/status/${cmd}` });
  };

  const tabs = [
    { key: "overview" as const, label: "Overzicht" },
    { key: "hardware" as const, label: "Hardware" },
    ...(hasCloudInit ? [{ key: "cloudinit" as const, label: "Cloud-init" }] : []),
    { key: "options" as const, label: "Opties" },
    { key: "snapshots" as const, label: "Snapshots" },
    { key: "backups" as const, label: "Back-ups" },
    { key: "passthrough" as const, label: "Passthrough" },
    { key: "tasks" as const, label: "Taken" },
  ];

  return (
    <div className="space-y-6">
      <div className="text-sm">
        <Link href={`/proxmox/gasten?dc=${encodeURIComponent(dc.id)}`} className="text-slate-500 hover:underline">
          ← Gasten
        </Link>
      </div>
      <PageHeader
        title={
          <span className="flex flex-wrap items-center gap-2">
            {guestLabel(type)} {vmid} {name && <span>{name}</span>}
            {template ? <Badge tone="blue">Template</Badge> : st && <Badge tone={vmStatusInfo(st.qmpstatus === "paused" ? "paused" : st.status).tone}>{vmStatusInfo(st.qmpstatus === "paused" ? "paused" : st.status).label}</Badge>}
            {st?.lock && <Badge tone="amber">Slot: {st.lock}</Badge>}
          </span>
        }
        description={`Op ${node}${st?.ha?.managed ? ` · onder HA (${st.ha.state})` : ""}`}
        actions={
          isAdmin && !template ? (
            <div className="flex flex-wrap gap-2">
              {!running ? (
                <Button disabled={pending} onClick={() => void power("start")}>
                  Starten
                </Button>
              ) : (
                <>
                  <Button onClick={() => window.open(consoleHref(dc.id, { node, kind: type === "lxc" ? "ct" : "vnc", vmid }), "_blank")}>Console</Button>
                  {type === "qemu" && "serial0" in cfg && (
                    <Button variant="secondary" onClick={() => window.open(consoleHref(dc.id, { node, kind: "serial", vmid }), "_blank")}>
                      Seriële console
                    </Button>
                  )}
                  <Button variant="secondary" disabled={pending} onClick={() => void power("shutdown", `${name || vmid} netjes afsluiten?`)}>
                    Afsluiten
                  </Button>
                  <Button variant="secondary" disabled={pending} onClick={() => void power("reboot", `${name || vmid} herstarten?`)}>
                    Herstarten
                  </Button>
                  {type === "qemu" &&
                    (st?.qmpstatus === "paused" ? (
                      <Button variant="secondary" disabled={pending} onClick={() => void power("resume")}>
                        Hervatten
                      </Button>
                    ) : (
                      <Button variant="secondary" disabled={pending} onClick={() => void power("suspend", `${name || vmid} pauzeren?`)}>
                        Pauzeren
                      </Button>
                    ))}
                  <Button variant="secondary-danger" disabled={pending} onClick={() => setDialog("stop")}>
                    Hard uitzetten
                  </Button>
                </>
              )}
              <Button variant="secondary" disabled={pending} onClick={() => setDialog("migrate")}>
                Verhuizen
              </Button>
              <Button variant="secondary" disabled={pending} onClick={() => setDialog("clone")}>
                Klonen
              </Button>
              {!running && (
                <Button variant="secondary" disabled={pending} onClick={() => setDialog("template")}>
                  Naar template
                </Button>
              )}
              {!running && (
                <Button variant="secondary-danger" disabled={pending || !hostAgent} title={hostAgent ? undefined : "Geen agent op deze host"} onClick={() => setDialog("destroy")}>
                  Verwijderen
                </Button>
              )}
            </div>
          ) : isAdmin && template ? (
            <div className="flex gap-2">
              <Button disabled={pending} onClick={() => setDialog("clone")}>
                Klonen
              </Button>
              <Button variant="secondary-danger" disabled={pending || !hostAgent} onClick={() => setDialog("destroy")}>
                Verwijderen
              </Button>
            </div>
          ) : undefined
        }
      />
      {banner}
      {destroyMsg && <Alert kind={destroyMsg.startsWith("Taak") ? "success" : "error"}>{destroyMsg}</Alert>}
      <QueryState q={config}>
        <Tabs tabs={tabs} value={tab} onChange={setTab} label="Onderdelen van de gast" />
        {tab === "overview" && <Overview ctx={ctx} colors={colors} />}
        {tab === "hardware" && <HardwareTab ctx={ctx} />}
        {tab === "cloudinit" && <CloudInitTab ctx={ctx} />}
        {tab === "options" && <OptionsTab ctx={ctx} />}
        {tab === "snapshots" && <SnapshotsTab ctx={ctx} />}
        {tab === "backups" && <GuestBackups dc={dc} vmid={vmid} />}
        {tab === "passthrough" && <PassthroughTab ctx={ctx} />}
        {tab === "tasks" && <GuestTasks ctx={ctx} />}
      </QueryState>
      {dialog === "migrate" && <MigrateDialog ctx={ctx} onClose={() => setDialog(null)} />}
      {dialog === "clone" && <CloneDialog ctx={ctx} name={name} onClose={() => setDialog(null)} />}
      {dialog === "stop" && (
        <TypedConfirm
          title={`${guestLabel(type)} ${vmid} hard uitzetten`}
          expect={String(vmid)}
          confirmLabel="Hard uitzetten"
          pending={pending}
          onClose={() => setDialog(null)}
          onConfirm={async () => {
            if (await run({ method: "create", path: `${path}/status/stop` })) setDialog(null);
          }}
        >
          <p>Dit is de stekker eruit: niet-opgeslagen gegevens in de gast gaan verloren. Probeer eerst Afsluiten.</p>
        </TypedConfirm>
      )}
      {dialog === "template" && (
        <TypedConfirm
          title={`${guestLabel(type)} ${vmid} omzetten naar template`}
          expect={String(vmid)}
          confirmLabel="Naar template"
          pending={pending}
          onClose={() => setDialog(null)}
          onConfirm={async (typed) => {
            if (await run({ method: "create", path: `${path}/template`, confirm: typed })) setDialog(null);
          }}
        >
          <p>Een template kun je niet meer starten, alleen klonen. Dit draai je niet terug.</p>
        </TypedConfirm>
      )}
      {dialog === "destroy" && hostAgent && (
        <TypedConfirm
          title={`${guestLabel(type)} ${vmid} verwijderen`}
          expect={name || String(vmid)}
          confirmLabel="Definitief verwijderen"
          onClose={() => setDialog(null)}
          onConfirm={async (typed) => {
            try {
              const j = await pveHostAction(hostAgent.node_id, { op: { action: "destroy", type, vmid }, confirm: typed });
              setDestroyMsg(`Taak gestart: ${j.title}.`);
              setDialog(null);
            } catch (e) {
              setDestroyMsg(e instanceof Error ? e.message : "Mislukt");
              setDialog(null);
            }
          }}
        >
          <p>De gast en al zijn schijven worden gewist, ook uit back-upjobs, replicatie en HA. Back-ups op PBS blijven bestaan.</p>
        </TypedConfirm>
      )}
    </div>
  );
}

function Stat({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div>
      <div className="text-xs font-medium tracking-wide text-slate-500 uppercase">{label}</div>
      <div className="mt-1 text-sm">{children}</div>
    </div>
  );
}

type AgentIfaces = { result?: { name: string; "ip-addresses"?: { "ip-address": string; prefix: number; "ip-address-type": string }[] }[] };
type CTIfaces = { name: string; inet?: string; inet6?: string }[];

function Overview({ ctx, colors }: { ctx: GuestCtx; colors: Record<string, string> }) {
  const { dc, path, type, config, status: st, running } = ctx;
  const vmAgent = usePveGet<AgentIfaces>(dc.id, path + "/agent/network-get-interfaces", {
    enabled: type === "qemu" && running && String(config.agent ?? "").startsWith("1"),
    refetch: 30_000,
  });
  const ctIfaces = usePveGet<CTIfaces>(dc.id, path + "/interfaces", { enabled: type === "lxc" && running, refetch: 30_000 });
  const pending = usePveGet<{ key: string; value?: unknown; pending?: unknown; delete?: number }[]>(dc.id, path + "/pending", { refetch: 15_000 });
  const ips =
    type === "qemu"
      ? (vmAgent.data?.result ?? [])
          .filter((i) => i.name !== "lo")
          .flatMap((i) => (i["ip-addresses"] ?? []).filter((a) => a["ip-address-type"] === "ipv4").map((a) => `${a["ip-address"]}/${a.prefix} (${i.name})`))
      : (ctIfaces.data ?? []).filter((i) => i.name !== "lo" && i.inet).map((i) => `${i.inet} (${i.name})`);
  const changes = (pending.data ?? []).filter((p) => p.pending !== undefined || p.delete);
  return (
    <div className="space-y-4">
      {changes.length > 0 && running && (
        <Alert kind="info">
          Wacht op een herstart van de gast: {changes.map((c) => (c.delete ? `${c.key} weg` : c.key)).join(", ")}.{" "}
          {ctx.isAdmin && (
            <button type="button" className="underline" onClick={() => void ctx.run({ method: "create", path: `${path}/status/reboot` })}>
              Nu herstarten
            </button>
          )}
        </Alert>
      )}
      <Card>
        <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
          <Stat label="CPU">
            {running ? <Meter value={(st?.cpu ?? 0) * 100} warn={80} crit={95} /> : null}
            <span className="text-xs text-slate-500">{st?.cpus ?? config.cores} vCPU</span>
          </Stat>
          <Stat label="Geheugen">
            {running ? <Meter value={pct(st?.mem ?? 0, st?.maxmem ?? 0)} /> : null}
            <span className="text-xs text-slate-500">
              {running ? `${formatBytes(st?.mem ?? 0)} van ` : ""}
              {formatBytes(st?.maxmem ?? 0)}
            </span>
          </Stat>
          <Stat label="Schijf">
            {type === "lxc" && running && (st?.maxdisk ?? 0) > 0 ? <Meter value={pct(st?.disk ?? 0, st?.maxdisk ?? 0)} /> : null}
            <span className="text-xs text-slate-500">{formatBytes(st?.maxdisk ?? 0)}</span>
          </Stat>
          <Stat label="Draait">{running ? uptime(st?.uptime ?? 0) : "–"}</Stat>
          <Stat label="Netwerk">
            {running ? `↓ ${formatBytes(st?.netin ?? 0)} · ↑ ${formatBytes(st?.netout ?? 0)}` : "–"}
          </Stat>
          <Stat label="Adressen">
            {ips.length > 0 ? (
              ips.map((ip) => (
                <div key={ip} className="font-mono text-xs">
                  {ip}
                </div>
              ))
            ) : (
              <span className="text-xs text-slate-500">{type === "qemu" && running ? "Zonder QEMU guest agent onbekend" : "–"}</span>
            )}
          </Stat>
          <Stat label="Tags">
            <TagChips tags={splitTags(String(config.tags ?? ""))} colors={colors} />
            {!config.tags && <span className="text-xs text-slate-500">geen (aanpassen bij Opties)</span>}
          </Stat>
          <Stat label="HA">{st?.ha?.managed ? `${st.ha.state}${st.ha.group ? ` · ${st.ha.group}` : ""}` : "niet onder HA"}</Stat>
        </div>
      </Card>
      {config.description && (
        <Card title="Beschrijving">
          <p className="text-sm whitespace-pre-wrap">{String(config.description)}</p>
        </Card>
      )}
    </div>
  );
}

function GuestTasks({ ctx }: { ctx: GuestCtx }) {
  const tasks = usePveGet<PveTask[]>(ctx.dc.id, `/nodes/${ctx.node}/tasks`, { params: { vmid: String(ctx.vmid), limit: "200" }, refetch: 10_000 });
  return (
    <Card>
      <QueryState q={tasks}>{tasks.data && <PveTaskTable dc={ctx.dc.id} tasks={tasks.data} />}</QueryState>
    </Card>
  );
}

function GuestBackups({ dc, vmid }: { dc: Datacenter; vmid: number }) {
  const res = usePveGet<PveResource[]>(dc.id, "/cluster/resources", { refetch: 30_000 });
  return (
    <QueryState q={res}>
      <BackupList dc={dc} res={res.data ?? []} vmid={vmid} />
    </QueryState>
  );
}
