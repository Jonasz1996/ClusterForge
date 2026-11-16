"use client";

import Link from "next/link";
import { useRouter, useSearchParams } from "next/navigation";
import { useQuery } from "@tanstack/react-query";
import { Fragment, Suspense, useMemo, useState } from "react";
import { Modal } from "@/components/Modal";
import {
  ProdConfirm,
  prodConfirmed,
  useProdTotp,
} from "@/components/ProdConfirm";
import { ago, tableClass, tdClass, thClass } from "@/components/inventory/bits";
import {
  Alert,
  Badge,
  Button,
  Card,
  Field,
  Input,
  PageHeader,
  Select,
  Textarea,
  cx,
} from "@/components/ui";
import { ApiError } from "@/lib/api/client";
import { useClusters, useIsAdmin } from "@/lib/inventory";
import {
  actionLabel,
  containerTone,
  fetchStackLogs,
  placementLabel,
  stackHealth,
  useImportStacks,
  useReleaseStack,
  useSaveStack,
  useStack,
  useStackJob,
  useStacks,
  type Placement,
  type Stack,
  type StackAction,
  type StackContainer,
  type StackDetail,
  type StackImportResult,
  type StackOverview,
  type StackUnmanaged,
} from "@/lib/stacks";

function errText(e: unknown) {
  return e instanceof ApiError ? e.message : "Er ging iets mis.";
}

export default function ContainersPage() {
  return (
    <Suspense>
      <Containers />
    </Suspense>
  );
}

function Containers() {
  const params = useSearchParams();
  const router = useRouter();
  const clusters = useClusters();
  const isAdmin = useIsAdmin();
  const list = useMemo(() => {
    const all = clusters.data ?? [];
    // Docker-clusters eerst; de rest kan ook containers hebben.
    const docker = (c: (typeof all)[number]) =>
      c.type === "docker" || (c.template_name ?? "").startsWith("docker");
    return [...all].sort(
      (a, b) =>
        Number(docker(b)) - Number(docker(a)) || a.name.localeCompare(b.name),
    );
  }, [clusters.data]);
  const clusterId = params.get("cluster") ?? list[0]?.id ?? "";
  const data = useStacks(clusterId);

  return (
    <div className="space-y-6">
      <PageHeader
        title="Containers"
        description="De Compose-projecten van een cluster als gewenste staat: ClusterForge bewaart de bestanden (versleuteld), zet ze op elke node gelijk, start, stopt en werkt bij vanuit hier en toont de gezondheid per container. Een stack met plaatsing VIP draait alleen op de node met het VIP; zijn mappen gaan met een sync naar de andere nodes."
        actions={
          <Select
            aria-label="Cluster"
            value={clusterId}
            onChange={(e) =>
              router.replace(`/containers?cluster=${e.target.value}`)
            }
            className="min-w-56"
          >
            {list.map((c) => (
              <option key={c.id} value={c.id}>
                {c.name}
                {c.template_name ? ` (${c.template_name})` : ""}
              </option>
            ))}
          </Select>
        }
      />
      {clusters.data && list.length === 0 && (
        <Alert kind="info">
          Nog geen clusters. Maak er een bij{" "}
          <Link href="/clusters" className="underline">
            Clusters
          </Link>
          , bijvoorbeeld met de template docker-ha op je bestaande Docker-hosts.
        </Alert>
      )}
      {data.isError && <Alert>{errText(data.error)}</Alert>}
      {data.isLoading && clusterId && (
        <p className="text-sm text-slate-500">
          Laden… (de agents worden gevraagd wat er draait)
        </p>
      )}
      {data.data && <ClusterView o={data.data} isAdmin={isAdmin} />}
    </div>
  );
}

type Confirm = { action: StackAction; stacks: Stack[]; nodeId?: string };

function ClusterView({ o, isAdmin }: { o: StackOverview; isAdmin: boolean }) {
  const [confirm, setConfirm] = useState<Confirm | null>(null);
  const [edit, setEdit] = useState<string | null>(null);
  const [logs, setLogs] = useState<{
    nodeId: string;
    hostname: string;
    container: string;
  } | null>(null);
  const [open, setOpen] = useState<Record<string, boolean>>({});
  const [msg, setMsg] = useState<{
    ok: boolean;
    text: string;
    job?: string;
  } | null>(null);
  const release = useReleaseStack(o.cluster.id);
  const nodeName = (id: string) =>
    o.nodes.find((n) => n.id === id)?.hostname ?? id.slice(0, 8);
  const outdated = o.stacks.some((s) =>
    s.nodes.some((n) => n.revision < s.revision),
  );

  return (
    <div className="space-y-6">
      {!o.available && (
        <Alert>
          Er is geen masterkey (CF_MASTER_KEY): ClusterForge kan geen stacks
          bewaren.
        </Alert>
      )}
      {msg && (
        <Alert kind={msg.ok ? "success" : "error"}>
          {msg.text}{" "}
          {msg.job && (
            <Link
              href={`/taken/detail?id=${msg.job}`}
              className="font-medium underline"
            >
              Taak volgen
            </Link>
          )}
        </Alert>
      )}
      {o.job_id && (
        <Alert kind="info">
          Er loopt een taak voor de containers van dit cluster.{" "}
          <Link
            href={`/taken/detail?id=${o.job_id}`}
            className="font-medium underline"
          >
            Bekijken
          </Link>
        </Alert>
      )}

      <Card title="Nodes">
        <div className="flex flex-wrap gap-2">
          {o.nodes.map((n) => (
            <div
              key={n.id}
              className="rounded-md border border-slate-200 px-3 py-2 text-sm dark:border-slate-800"
            >
              <div className="flex items-center gap-2 font-medium">
                <Link
                  href={`/nodes/detail?id=${n.id}`}
                  className="hover:underline"
                >
                  {n.hostname}
                </Link>
                {n.holds_vip && <Badge tone="blue">VIP</Badge>}
              </div>
              <div className="mt-1 flex flex-wrap gap-1">
                {!n.online ? (
                  <Badge tone="red">agent offline</Badge>
                ) : !n.supported ? (
                  <Badge tone="amber">agent te oud ({n.protocol})</Badge>
                ) : n.docker ? (
                  <Badge tone="green">Docker</Badge>
                ) : (
                  <Badge tone="red">geen Docker</Badge>
                )}
              </div>
              {n.error && (
                <p className="mt-1 max-w-64 text-xs text-red-600 dark:text-red-400">
                  {n.error}
                </p>
              )}
            </div>
          ))}
        </div>
        <p className="mt-3 text-sm text-slate-500">
          {o.cluster.vip ? (
            <>
              VIP <span className="font-mono">{o.cluster.vip}</span>
              {o.cluster.vip_owner
                ? ` op ${nodeName(o.cluster.vip_owner)}`
                : " (niemand heeft het nu)"}
              .
            </>
          ) : (
            "Dit cluster heeft geen VIP: elke stack draait op zijn nodes, zonder keepalived."
          )}{" "}
          {o.nodes.some((n) => n.online && !n.supported) &&
            "Een agent die te oud is, werk je bij via Nodes → Agents bijwerken."}
        </p>
      </Card>

      <Card
        title={
          <div className="flex flex-wrap items-center justify-between gap-2">
            <span>Beheerde stacks ({o.stacks.length})</span>
            {isAdmin && o.stacks.length > 0 && (
              <div className="flex gap-2">
                <Button
                  variant="secondary"
                  onClick={() => setConfirm({ action: "lists", stacks: [] })}
                >
                  Lijsten en syncs vernieuwen
                </Button>
                <Button
                  variant={outdated ? "primary" : "secondary"}
                  onClick={() =>
                    setConfirm({ action: "apply", stacks: o.stacks })
                  }
                >
                  Alles toepassen
                </Button>
              </div>
            )}
          </div>
        }
      >
        {o.stacks.length === 0 ? (
          <p className="text-sm text-slate-500">
            Nog geen beheerde stacks. Neem hieronder de projecten over die al op
            de nodes draaien; ze blijven draaien zoals ze zijn tot je ze
            toepast.
          </p>
        ) : (
          <div className="overflow-x-auto">
            <table className={tableClass}>
              <thead>
                <tr>
                  <th className={thClass}>Stack</th>
                  {o.nodes.map((n) => (
                    <th key={n.id} className={thClass}>
                      {n.hostname}
                      {n.holds_vip && " (VIP)"}
                    </th>
                  ))}
                  {isAdmin && <th className={thClass}>Acties</th>}
                </tr>
              </thead>
              <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
                {o.stacks.map((s) => (
                  <Fragment key={s.id}>
                    <tr>
                      <td className={tdClass}>
                        <button
                          className="text-left font-medium hover:underline"
                          onClick={() =>
                            setOpen({ ...open, [s.id]: !open[s.id] })
                          }
                        >
                          {open[s.id] ? "▾" : "▸"} {s.name}
                        </button>
                        <div className="font-mono text-xs text-slate-500">
                          {s.dir}
                        </div>
                        <div className="mt-1 flex flex-wrap gap-1">
                          <Badge
                            tone={s.placement === "vip" ? "blue" : "slate"}
                          >
                            {placementLabel[s.placement]}
                          </Badge>
                          {s.critical && <Badge tone="amber">kritiek</Badge>}
                          {s.desired === "stopped" && <Badge>gestopt</Badge>}
                          {s.sync_paths.length > 0 && s.placement === "vip" && (
                            <Badge>sync {s.sync_schedule}</Badge>
                          )}
                          <Badge>rev {s.revision}</Badge>
                        </div>
                      </td>
                      {o.nodes.map((n) => {
                        const sn = s.nodes.find((x) => x.node_id === n.id);
                        if (!sn)
                          return (
                            <td
                              key={n.id}
                              className={cx(tdClass, "text-slate-400")}
                            >
                              –
                            </td>
                          );
                        const h = stackHealth(sn, s.desired);
                        return (
                          <td key={n.id} className={tdClass}>
                            <Badge tone={h.tone}>{h.text}</Badge>
                            {sn.revision < s.revision && (
                              <div className="mt-1 text-xs text-amber-700 dark:text-amber-300">
                                {sn.revision === 0
                                  ? "nog niet toegepast"
                                  : `rev ${sn.revision}, nieuw: ${s.revision}`}
                              </div>
                            )}
                            {sn.error && (
                              <div
                                className="mt-1 max-w-56 text-xs text-red-600 dark:text-red-400"
                                title={sn.error}
                              >
                                {sn.error.length > 120
                                  ? sn.error.slice(0, 120) + "…"
                                  : sn.error}
                              </div>
                            )}
                            {sn.applied_at && (
                              <div className="mt-1 text-xs text-slate-500">
                                toegepast {ago(sn.applied_at)}
                              </div>
                            )}
                          </td>
                        );
                      })}
                      {isAdmin && (
                        <td className={tdClass}>
                          <div className="flex flex-wrap gap-1">
                            <Button
                              variant="secondary"
                              className="px-2 py-1"
                              onClick={() =>
                                setConfirm({ action: "apply", stacks: [s] })
                              }
                            >
                              Toepassen
                            </Button>
                            {s.desired === "running" ? (
                              <Button
                                variant="secondary"
                                className="px-2 py-1"
                                onClick={() =>
                                  setConfirm({ action: "stop", stacks: [s] })
                                }
                              >
                                Stoppen
                              </Button>
                            ) : (
                              <Button
                                variant="secondary"
                                className="px-2 py-1"
                                onClick={() =>
                                  setConfirm({ action: "start", stacks: [s] })
                                }
                              >
                                Starten
                              </Button>
                            )}
                            <Button
                              variant="secondary"
                              className="px-2 py-1"
                              onClick={() =>
                                setConfirm({ action: "restart", stacks: [s] })
                              }
                            >
                              Herstarten
                            </Button>
                            <Button
                              variant="secondary"
                              className="px-2 py-1"
                              onClick={() =>
                                setConfirm({ action: "update", stacks: [s] })
                              }
                            >
                              Bijwerken
                            </Button>
                            <Button
                              variant="ghost"
                              className="px-2 py-1"
                              onClick={() => setEdit(s.id)}
                            >
                              Bewerken
                            </Button>
                          </div>
                        </td>
                      )}
                    </tr>
                    {open[s.id] && (
                      <tr>
                        <td
                          colSpan={o.nodes.length + (isAdmin ? 2 : 1)}
                          className="bg-slate-50/60 px-3 py-3 dark:bg-slate-900/40"
                        >
                          <StackContainers
                            s={s}
                            nodeName={nodeName}
                            onLogs={
                              isAdmin
                                ? (nodeId, container) =>
                                    setLogs({
                                      nodeId,
                                      hostname: nodeName(nodeId),
                                      container,
                                    })
                                : undefined
                            }
                          />
                          {isAdmin && (
                            <div className="mt-3 flex flex-wrap gap-2">
                              <Button
                                variant="secondary-danger"
                                onClick={() =>
                                  setConfirm({ action: "down", stacks: [s] })
                                }
                              >
                                Containers verwijderen (down)
                              </Button>
                              <Button
                                variant="ghost"
                                disabled={release.isPending}
                                onClick={() => {
                                  if (
                                    !window.confirm(
                                      `${s.name} loslaten? De containers en bestanden op de nodes blijven, ClusterForge beheert hem niet meer.`,
                                    )
                                  )
                                    return;
                                  release.mutate(s.id, {
                                    onSuccess: (r) =>
                                      setMsg({
                                        ok: true,
                                        text: `${s.name} losgelaten; de containers blijven draaien.`,
                                        job: r.job_id ?? undefined,
                                      }),
                                    onError: (e) =>
                                      setMsg({ ok: false, text: errText(e) }),
                                  });
                                }}
                              >
                                Loslaten
                              </Button>
                            </div>
                          )}
                          {s.notes && (
                            <p className="mt-2 text-sm whitespace-pre-wrap text-slate-600 dark:text-slate-300">
                              {s.notes}
                            </p>
                          )}
                        </td>
                      </tr>
                    )}
                  </Fragment>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Card>

      {isAdmin && <ImportCard o={o} onDone={setMsg} />}
      {o.others.length > 0 && <OthersCard o={o} />}

      {confirm && (
        <ConfirmJob
          o={o}
          c={confirm}
          onClose={() => setConfirm(null)}
          onDone={setMsg}
        />
      )}
      {edit && (
        <EditStack
          o={o}
          id={edit}
          onClose={() => setEdit(null)}
          onApply={(s) => setConfirm({ action: "apply", stacks: [s] })}
        />
      )}
      {logs && (
        <LogsModal
          clusterId={o.cluster.id}
          {...logs}
          onClose={() => setLogs(null)}
        />
      )}
    </div>
  );
}

function ContainerRow({
  c,
  onLogs,
}: {
  c: StackContainer;
  onLogs?: () => void;
}) {
  return (
    <tr>
      <td className={tdClass}>
        <span className="font-medium">{c.service || c.name}</span>
        <div className="font-mono text-xs text-slate-500">{c.image}</div>
      </td>
      <td className={tdClass}>
        <Badge tone={containerTone(c)}>
          {c.state}
          {c.health ? ` · ${c.health}` : ""}
        </Badge>
        <div className="text-xs text-slate-500">{c.status}</div>
        {(c.restarts ?? 0) > 0 && (
          <div className="text-xs text-amber-700 dark:text-amber-300">
            {c.restarts}× herstart
          </div>
        )}
      </td>
      <td className={cx(tdClass, "font-mono text-xs")}>
        {(c.ports ?? []).join(", ") || "–"}
      </td>
      <td className={tdClass}>
        {onLogs && (
          <Button variant="ghost" className="px-2 py-1" onClick={onLogs}>
            Logs
          </Button>
        )}
      </td>
    </tr>
  );
}

function StackContainers({
  s,
  nodeName,
  onLogs,
}: {
  s: Stack;
  nodeName: (id: string) => string;
  onLogs?: (nodeId: string, c: string) => void;
}) {
  return (
    <div className="space-y-3">
      {s.nodes.map((n) => (
        <div key={n.node_id}>
          <div className="text-sm font-medium">
            {nodeName(n.node_id)}{" "}
            {n.should ? (
              ""
            ) : (
              <span className="text-slate-500">
                (hoort hier niet te draaien)
              </span>
            )}
          </div>
          {n.containers.length === 0 ? (
            <p className="text-sm text-slate-500">Geen containers.</p>
          ) : (
            <table className={tableClass}>
              <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
                {n.containers.map((c) => (
                  <ContainerRow
                    key={c.id}
                    c={c}
                    onLogs={
                      onLogs ? () => onLogs(n.node_id, c.name) : undefined
                    }
                  />
                ))}
              </tbody>
            </table>
          )}
        </div>
      ))}
    </div>
  );
}

function ConfirmJob({
  o,
  c,
  onClose,
  onDone,
}: {
  o: StackOverview;
  c: Confirm;
  onClose: () => void;
  onDone: (m: { ok: boolean; text: string; job?: string }) => void;
}) {
  const job = useStackJob(o.cluster.id);
  const [typed, setTyped] = useState("");
  const totp = useProdTotp();
  const prod = o.cluster.environment === "prod" && c.action !== "lists";
  const names = c.stacks.map((s) => s.name).join(", ") || "alle stacks";
  const explain: Record<StackAction, string> = {
    apply:
      "Zet de bestanden op de nodes en brengt elke stack in zijn gewenste stand: draaien waar hij hoort, gestopt op de andere nodes. Een stack die na de wijziging niet gezond wordt, krijgt zijn vorige bestanden terug.",
    start: "Zet de gewenste stand op draaien en start de stack waar hij hoort.",
    stop: "Zet de gewenste stand op gestopt en stopt de containers op alle nodes. Keepalived start hem dan ook niet meer.",
    restart: "Herstart de containers waar de stack draait.",
    update:
      "Haalt de nieuwste images (docker compose pull) op alle nodes en herstart de stack waar hij draait, met de gezondheidscontrole en terugdraaien.",
    down: "Verwijdert de containers en netwerken (docker compose down) op alle nodes. Volumes en mappen blijven. Toepassen of Starten zet ze terug.",
    lists:
      "Schrijft de lijst voor keepalived op elke node en zet de syncs van de stacks met plaatsing VIP goed. Er start of stopt niets.",
  };
  const ok = !prod || prodConfirmed(o.cluster.slug, typed, totp);
  return (
    <Modal title={`${actionLabel[c.action]}: ${names}`} onClose={onClose} wide>
      <div className="space-y-4 text-sm">
        <p>{explain[c.action]}</p>
        {prod && (
          <ProdConfirm
            slug={o.cluster.slug}
            value={typed}
            onChange={setTyped}
          />
        )}
        {job.isError && <Alert>{errText(job.error)}</Alert>}
        <div className="flex justify-end gap-2">
          <Button variant="secondary" onClick={onClose}>
            Annuleren
          </Button>
          <Button
            variant={c.action === "down" ? "danger" : "primary"}
            disabled={!ok || job.isPending}
            onClick={() =>
              job.mutate(
                {
                  action: c.action,
                  stack_ids: c.stacks.length
                    ? c.stacks.map((s) => s.id)
                    : undefined,
                  node_id: c.nodeId,
                  confirm: prod ? typed : undefined,
                },
                {
                  onSuccess: (r) => {
                    onDone({
                      ok: true,
                      text: `${actionLabel[c.action]} gestart voor ${names}.`,
                      job: r.job_id,
                    });
                    onClose();
                  },
                },
              )
            }
          >
            {actionLabel[c.action]}
          </Button>
        </div>
      </div>
    </Modal>
  );
}

type ImportChoice = {
  on: boolean;
  source: string;
  placement: Placement;
  critical: boolean;
  sync: string;
};

function ImportCard({
  o,
  onDone,
}: {
  o: StackOverview;
  onDone: (m: { ok: boolean; text: string }) => void;
}) {
  const imp = useImportStacks(o.cluster.id);
  const multi = o.cluster.vip !== "" && o.nodes.length > 1;
  // Per projectnaam: op welke nodes het staat.
  const groups = useMemo(() => {
    const m = new Map<string, StackUnmanaged[]>();
    for (const u of o.unmanaged)
      m.set(u.project.name, [...(m.get(u.project.name) ?? []), u]);
    return [...m.entries()].sort((a, b) => a[0].localeCompare(b[0]));
  }, [o.unmanaged]);
  const [overrides, setOverrides] = useState<Record<string, ImportChoice>>({});
  const [results, setResults] = useState<StackImportResult[]>([]);
  const choice = useMemo(() => {
    const out: Record<string, ImportChoice> = {};
    for (const [name, us] of groups) {
      // De bron is de node met het VIP, anders de eerste waar het draait.
      const src =
        us.find((u) => u.node_id === o.cluster.vip_owner) ??
        us.find((u) => u.containers.some((c) => c.state === "running")) ??
        us[0];
      out[name] = overrides[name] ?? {
        on: false,
        source: src.node_id,
        placement:
          us.length === 1 && o.nodes.length > 1 && !multi
            ? "node"
            : multi
              ? "vip"
              : "alle",
        critical: false,
        sync: src.sync_paths.join("\n"),
      };
    }
    return out;
  }, [groups, overrides, o.cluster.vip_owner, o.nodes.length, multi]);
  const setChoice = setOverrides;

  if (groups.length === 0) {
    return (
      <Card title="Overnemen">
        <p className="text-sm text-slate-500">
          Er draaien geen Compose-projecten op de nodes die ClusterForge nog
          niet beheert.
        </p>
        {results.length > 0 && <ImportResults results={results} />}
      </Card>
    );
  }
  const set = (name: string, p: Partial<ImportChoice>) =>
    setChoice({ ...choice, [name]: { ...choice[name], ...p } });
  const chosen = Object.entries(choice).filter(([, c]) => c.on);
  const run = async () => {
    const bySource = new Map<string, [string, ImportChoice][]>();
    for (const e of chosen)
      bySource.set(e[1].source, [...(bySource.get(e[1].source) ?? []), e]);
    const all: StackImportResult[] = [];
    try {
      for (const [source, items] of bySource) {
        const r = await imp.mutateAsync({
          node_id: source,
          items: items.map(([name, c]) => ({
            name,
            placement: c.placement,
            node_ids: c.placement === "node" ? [c.source] : undefined,
            critical: c.critical,
            sync_paths:
              c.placement === "vip"
                ? c.sync
                    .split("\n")
                    .map((x) => x.trim())
                    .filter(Boolean)
                : [],
          })),
        });
        all.push(...r);
      }
      setResults(all);
      const bad = all.filter((r) => r.error).length;
      onDone({
        ok: bad === 0,
        text: bad
          ? `${all.length - bad} overgenomen, ${bad} mislukt (zie Overnemen).`
          : `${all.length} overgenomen. Er veranderde nog niets op de nodes: kijk de bestanden na met Bewerken en kies dan Toepassen.`,
      });
    } catch (e) {
      onDone({ ok: false, text: errText(e) });
    }
  };

  return (
    <Card title={`Overnemen (${groups.length} projecten)`}>
      <div className="space-y-3 text-sm">
        <p className="text-slate-600 dark:text-slate-300">
          Deze Compose-projecten draaien al op de nodes. Overnemen leest hun
          bestanden (compose, .env en kleine configuratie) van de bronnode en
          bewaart ze versleuteld; data, volumes en grote bestanden blijven waar
          ze zijn. Er verandert niets op de nodes tot je Toepassen kiest.
          {multi &&
            " Met plaatsing VIP draait de stack alleen op de node met het VIP en gaan de sync-mappen elk kwartier naar de andere nodes (containers kort gepauzeerd); Toepassen stopt hem dan op de andere nodes."}
        </p>
        <div className="overflow-x-auto">
          <table className={tableClass}>
            <thead>
              <tr>
                <th className={thClass}></th>
                <th className={thClass}>Project</th>
                <th className={thClass}>Bron</th>
                <th className={thClass}>Plaatsing</th>
                <th className={thClass}>Kritiek</th>
                <th className={thClass}>Sync-mappen (bij VIP)</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
              {groups.map(([name, us]) => {
                const c = choice[name];
                if (!c) return null;
                const src = us.find((u) => u.node_id === c.source) ?? us[0];
                const bad = src.containers.filter(
                  (x) => containerTone(x) === "red",
                ).length;
                return (
                  <tr key={name}>
                    <td className={tdClass}>
                      <input
                        type="checkbox"
                        aria-label={`${name} overnemen`}
                        checked={c.on}
                        onChange={(e) => set(name, { on: e.target.checked })}
                      />
                    </td>
                    <td className={tdClass}>
                      <div className="font-medium">{name}</div>
                      <div className="font-mono text-xs text-slate-500">
                        {src.project.working_dir}
                      </div>
                      <div className="text-xs text-slate-500">
                        {src.containers.length} containers op{" "}
                        {us.map((u) => u.hostname).join(", ")}
                        {bad > 0 && (
                          <span className="text-red-600 dark:text-red-400">
                            {" "}
                            · {bad} kapot
                          </span>
                        )}
                      </div>
                      {(src.project.missing ?? []).length > 0 && (
                        <div className="text-xs text-red-600 dark:text-red-400">
                          ontbreekt: {(src.project.missing ?? []).join(", ")}
                        </div>
                      )}
                    </td>
                    <td className={tdClass}>
                      <Select
                        aria-label="Bron"
                        value={c.source}
                        onChange={(e) =>
                          set(name, {
                            source: e.target.value,
                            sync: (
                              us.find((u) => u.node_id === e.target.value)
                                ?.sync_paths ?? []
                            ).join("\n"),
                          })
                        }
                      >
                        {us.map((u) => (
                          <option key={u.node_id} value={u.node_id}>
                            {u.hostname}
                            {u.node_id === o.cluster.vip_owner ? " (VIP)" : ""}
                          </option>
                        ))}
                      </Select>
                    </td>
                    <td className={tdClass}>
                      <Select
                        aria-label="Plaatsing"
                        value={c.placement}
                        onChange={(e) =>
                          set(name, { placement: e.target.value as Placement })
                        }
                      >
                        {(["vip", "alle", "node"] as Placement[]).map((p) => (
                          <option key={p} value={p}>
                            {p === "node"
                              ? "Alleen de bronnode"
                              : placementLabel[p]}
                          </option>
                        ))}
                      </Select>
                    </td>
                    <td className={tdClass}>
                      <input
                        type="checkbox"
                        aria-label={`${name} kritiek`}
                        checked={c.critical}
                        onChange={(e) =>
                          set(name, { critical: e.target.checked })
                        }
                      />
                    </td>
                    <td className={tdClass}>
                      {c.placement === "vip" ? (
                        <Textarea
                          rows={Math.max(2, c.sync.split("\n").length)}
                          className="min-w-64 font-mono text-xs"
                          value={c.sync}
                          onChange={(e) => set(name, { sync: e.target.value })}
                        />
                      ) : (
                        <span className="text-xs text-slate-500">
                          geen sync
                        </span>
                      )}
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
        <div className="flex items-center gap-2">
          <Button
            disabled={chosen.length === 0 || imp.isPending || !o.available}
            onClick={run}
          >
            {imp.isPending ? "Overnemen…" : `${chosen.length} overnemen`}
          </Button>
          <Button
            variant="ghost"
            onClick={() =>
              setChoice(
                Object.fromEntries(
                  Object.entries(choice).map(([k, v]) => [
                    k,
                    { ...v, on: true },
                  ]),
                ),
              )
            }
          >
            Alles aanvinken
          </Button>
        </div>
        {results.length > 0 && <ImportResults results={results} />}
      </div>
    </Card>
  );
}

function ImportResults({ results }: { results: StackImportResult[] }) {
  return (
    <ul className="space-y-1 text-sm">
      {results.map((r) => (
        <li key={r.name}>
          <span className="font-medium">{r.name}</span>:{" "}
          {r.error ? (
            <span className="text-red-600 dark:text-red-400">{r.error}</span>
          ) : (
            <>
              {r.files.length} bestanden ({r.files.join(", ")})
              {r.skipped.length > 0 && (
                <span className="text-slate-500">
                  {" "}
                  · niet meegenomen: {r.skipped.join(", ")}
                </span>
              )}
            </>
          )}
        </li>
      ))}
    </ul>
  );
}

function OthersCard({ o }: { o: StackOverview }) {
  return (
    <Card title="Containers zonder Compose-project">
      <p className="mb-2 text-sm text-slate-500">
        Deze containers zijn met docker run gemaakt. ClusterForge toont ze, maar
        kan ze pas beheren als ze in een compose-bestand staan.
      </p>
      <table className={tableClass}>
        <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
          {o.others.flatMap((u) =>
            u.containers.map((c) => (
              <tr key={u.node_id + c.id}>
                <td className={tdClass}>{u.hostname}</td>
                <td className={tdClass}>{c.name}</td>
                <td className={cx(tdClass, "font-mono text-xs")}>{c.image}</td>
                <td className={tdClass}>
                  <Badge tone={containerTone(c)}>{c.state}</Badge>
                </td>
              </tr>
            )),
          )}
        </tbody>
      </table>
    </Card>
  );
}

function EditStack({
  o,
  id,
  onClose,
  onApply,
}: {
  o: StackOverview;
  id: string;
  onClose: () => void;
  onApply: (s: Stack) => void;
}) {
  const d = useStack(id);
  return (
    <Modal
      title={d.data ? `${d.data.stack.name} bewerken` : "Stack"}
      onClose={onClose}
      wide
    >
      {d.isError && <Alert>{errText(d.error)}</Alert>}
      {d.isLoading && <p className="text-sm text-slate-500">Laden…</p>}
      {d.data && (
        <EditForm
          key={d.data.stack.revision}
          o={o}
          d={d.data}
          onClose={onClose}
          onApply={onApply}
        />
      )}
    </Modal>
  );
}

function EditForm({
  o,
  d,
  onClose,
  onApply,
}: {
  o: StackOverview;
  d: StackDetail;
  onClose: () => void;
  onApply: (s: Stack) => void;
}) {
  const save = useSaveStack(o.cluster.id);
  const s = d.stack;
  const id = s.id;
  const [files, setFiles] = useState(() =>
    d.files.map((f) => ({ path: f.path, content: f.content, mode: f.mode })),
  );
  const [tab, setTab] = useState(0);
  const [form, setForm] = useState(() => ({
    placement: s.placement,
    node_ids: s.node_ids,
    critical: s.critical,
    desired: s.desired,
    compose: s.compose_files.join(", "),
    sync: s.sync_paths.join("\n"),
    schedule: s.sync_schedule,
    notes: s.notes,
    note: "",
  }));
  const [saved, setSaved] = useState<Stack | null>(null);
  const [newPath, setNewPath] = useState("");

  return (
    <div className="space-y-4 text-sm">
      {saved ? (
        <div className="space-y-3">
          <Alert kind="success">
            Bewaard als revisie {saved.revision}. Er veranderde nog niets op de
            nodes; Toepassen zet de wijziging erop (met terugdraaien als de
            stack niet gezond wordt).
          </Alert>
          <div className="flex justify-end gap-2">
            <Button variant="secondary" onClick={onClose}>
              Later
            </Button>
            <Button
              onClick={() => {
                onClose();
                onApply(saved);
              }}
            >
              Nu toepassen
            </Button>
          </div>
        </div>
      ) : (
        <>
          <p className="text-slate-500">
            Wachtwoorden staan als «geheim-N»: laat die staan om ze te houden.
            Een nieuwe waarde typ je gewoon in de plaats.
          </p>
          <div className="flex flex-wrap gap-1 border-b border-slate-200 dark:border-slate-800">
            {files.map((f, i) => (
              <button
                key={f.path + i}
                onClick={() => setTab(i)}
                className={cx(
                  "-mb-px border-b-2 px-2 py-1 font-mono text-xs",
                  tab === i
                    ? "border-brand-600"
                    : "border-transparent text-slate-500",
                )}
              >
                {f.path}
              </button>
            ))}
          </div>
          {files[tab] && (
            <div className="space-y-2">
              <Textarea
                rows={18}
                spellCheck={false}
                className="w-full font-mono text-xs"
                value={files[tab].content}
                onChange={(e) =>
                  setFiles(
                    files.map((f, i) =>
                      i === tab ? { ...f, content: e.target.value } : f,
                    ),
                  )
                }
              />
              <Button
                variant="ghost"
                className="px-2 py-1"
                onClick={() => {
                  setFiles(files.filter((_, i) => i !== tab));
                  setTab(0);
                }}
              >
                {files[tab].path} uit de stack halen
              </Button>
            </div>
          )}
          <div className="flex gap-2">
            <Input
              placeholder="nieuw bestand, bv. config/app.ini"
              className="font-mono text-xs"
              value={newPath}
              onChange={(e) => setNewPath(e.target.value.trim())}
            />
            <Button
              variant="secondary"
              disabled={!newPath || files.some((f) => f.path === newPath)}
              onClick={() => {
                setFiles([
                  ...files,
                  { path: newPath, content: "", mode: 0o644 },
                ]);
                setTab(files.length);
                setNewPath("");
              }}
            >
              Toevoegen
            </Button>
          </div>

          <div className="grid gap-3 md:grid-cols-2">
            <Field label="Plaatsing" htmlFor="st-placement">
              <Select
                id="st-placement"
                value={form.placement}
                onChange={(e) =>
                  setForm({ ...form, placement: e.target.value as Placement })
                }
              >
                {(["vip", "alle", "node"] as Placement[]).map((p) => (
                  <option key={p} value={p}>
                    {placementLabel[p]}
                  </option>
                ))}
              </Select>
            </Field>
            <Field
              label="Compose-bestanden"
              htmlFor="st-compose"
              hint="In volgorde, met komma's"
            >
              <Input
                id="st-compose"
                className="font-mono text-xs"
                value={form.compose}
                onChange={(e) => setForm({ ...form, compose: e.target.value })}
              />
            </Field>
          </div>
          {form.placement === "node" && (
            <div className="flex flex-wrap gap-3">
              {o.nodes.map((n) => (
                <label key={n.id} className="flex items-center gap-1">
                  <input
                    type="checkbox"
                    checked={form.node_ids.includes(n.id)}
                    onChange={(e) =>
                      setForm({
                        ...form,
                        node_ids: e.target.checked
                          ? [...form.node_ids, n.id]
                          : form.node_ids.filter((x) => x !== n.id),
                      })
                    }
                  />
                  {n.hostname}
                </label>
              ))}
            </div>
          )}
          <div className="flex flex-wrap gap-4">
            <label className="flex items-center gap-1">
              <input
                type="checkbox"
                checked={form.critical}
                onChange={(e) =>
                  setForm({ ...form, critical: e.target.checked })
                }
              />
              Kritiek: het VIP gaat weg als deze stack op de VIP-node niet
              gezond draait
            </label>
            <label className="flex items-center gap-1">
              <input
                type="checkbox"
                checked={form.desired === "running"}
                onChange={(e) =>
                  setForm({
                    ...form,
                    desired: e.target.checked ? "running" : "stopped",
                  })
                }
              />
              Hoort te draaien
            </label>
          </div>
          {form.placement === "vip" && (
            <div className="grid gap-3 md:grid-cols-2">
              <Field
                label="Sync-mappen"
                htmlFor="st-sync"
                hint="Eén per regel; van de VIP-node naar de andere nodes"
              >
                <Textarea
                  id="st-sync"
                  rows={4}
                  className="font-mono text-xs"
                  value={form.sync}
                  onChange={(e) => setForm({ ...form, sync: e.target.value })}
                />
              </Field>
              <Field label="Sync-schema (cron)" htmlFor="st-sched">
                <Input
                  id="st-sched"
                  className="font-mono text-xs"
                  value={form.schedule}
                  onChange={(e) =>
                    setForm({ ...form, schedule: e.target.value })
                  }
                />
              </Field>
            </div>
          )}
          <Field label="Notities" htmlFor="st-notes">
            <Textarea
              id="st-notes"
              rows={2}
              value={form.notes}
              onChange={(e) => setForm({ ...form, notes: e.target.value })}
            />
          </Field>
          <Field label="Wat veranderde er? (bij de revisie)" htmlFor="st-note">
            <Input
              id="st-note"
              value={form.note}
              onChange={(e) => setForm({ ...form, note: e.target.value })}
            />
          </Field>
          {save.isError && <Alert>{errText(save.error)}</Alert>}
          <div className="flex justify-end gap-2">
            <Button variant="secondary" onClick={onClose}>
              Annuleren
            </Button>
            <Button
              disabled={save.isPending}
              onClick={() =>
                save.mutate(
                  {
                    id,
                    body: {
                      files,
                      compose_files: form.compose
                        .split(",")
                        .map((x) => x.trim())
                        .filter(Boolean),
                      placement: form.placement,
                      node_ids: form.placement === "node" ? form.node_ids : [],
                      critical: form.critical,
                      desired: form.desired,
                      sync_paths: form.sync
                        .split("\n")
                        .map((x) => x.trim())
                        .filter(Boolean),
                      sync_schedule: form.schedule,
                      notes: form.notes,
                      note: form.note,
                    },
                  },
                  { onSuccess: setSaved },
                )
              }
            >
              Bewaren
            </Button>
          </div>
          {d.revisions.length > 0 && (
            <details>
              <summary className="cursor-pointer text-slate-500">
                Revisies ({d.revisions.length})
              </summary>
              <ul className="mt-2 space-y-1">
                {d.revisions.map((r) => (
                  <li key={r.revision}>
                    rev {r.revision} · {ago(r.created_at)} ·{" "}
                    {r.username || "systeem"}
                    {r.note && ` · ${r.note}`}
                  </li>
                ))}
              </ul>
            </details>
          )}
        </>
      )}
    </div>
  );
}

function LogsModal({
  clusterId,
  nodeId,
  hostname,
  container,
  onClose,
}: {
  clusterId: string;
  nodeId: string;
  hostname: string;
  container: string;
  onClose: () => void;
}) {
  const [tail, setTail] = useState(300);
  const q = useQuery({
    queryKey: ["stack-logs", clusterId, nodeId, container, tail],
    queryFn: () => fetchStackLogs(clusterId, nodeId, container, tail),
    retry: false,
  });
  const text = q.data ?? null;
  const err = q.isError ? errText(q.error) : "";
  return (
    <Modal
      title={`Logs van ${container} op ${hostname}`}
      onClose={onClose}
      wide
    >
      <div className="space-y-2">
        <div className="flex items-center gap-2 text-sm">
          <span>Laatste</span>
          <Select
            aria-label="Aantal regels"
            value={tail}
            onChange={(e) => setTail(Number(e.target.value))}
          >
            {[100, 300, 1000, 3000].map((n) => (
              <option key={n} value={n}>
                {n} regels
              </option>
            ))}
          </Select>
          <Link
            className="ml-auto text-brand-600 hover:underline"
            href={`/logs?node=${nodeId}&kind=docker&name=${encodeURIComponent(container)}&cluster=1`}
          >
            Filteren en vergelijken met de andere nodes →
          </Link>
        </div>
        {err && <Alert>{err}</Alert>}
        {text === null && !err ? (
          <p className="text-sm text-slate-500">Laden…</p>
        ) : (
          <pre className="max-h-[60vh] overflow-auto rounded bg-slate-950 p-3 text-xs whitespace-pre-wrap text-slate-100">
            {text || "(leeg)"}
          </pre>
        )}
      </div>
    </Modal>
  );
}
