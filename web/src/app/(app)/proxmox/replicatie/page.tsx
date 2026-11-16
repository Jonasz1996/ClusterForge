"use client";

import Link from "next/link";
import { Suspense, useState } from "react";
import { Empty, QueryState, tableClass, tdClass, thClass } from "@/components/inventory/bits";
import { ActionMenu, DatacenterGate, TypedConfirm, useCallRunner } from "@/components/pve/bits";
import { FormModal } from "@/components/pve/guest/common";
import { Alert, Badge, Button, Card, Field, Input, PageHeader, Select } from "@/components/ui";
import { useIsAdmin } from "@/lib/inventory";
import { guestHref, guestLabel, usePerNode, usePveGet, type Datacenter, type PveResource, type ReplicationJob, type ReplicationState } from "@/lib/pve";

// Replicatie van Proxmox (fase 26 M4): ZFS-schijven van een gast op een
// schema naar een andere host kopiëren, zodat HA of een verhuizing daar met
// bijna verse data kan starten.

export default function ReplicationPage() {
  return (
    <div className="space-y-6">
      <PageHeader
        title="Replicatie"
        description="Kopieer de schijven van een gast op een vast schema naar een andere host. Werkt alleen met lokale ZFS-opslag die op beide hosts dezelfde naam heeft."
      />
      <Suspense>
        <DatacenterGate>
          {(dc) => (dc.standalone ? <Alert kind="info">Replicatie kan alleen tussen hosts van één Proxmox-cluster.</Alert> : <ReplicationView key={dc.id} dc={dc} />)}
        </DatacenterGate>
      </Suspense>
    </div>
  );
}

const when = (t?: number) => (t ? new Date(t * 1000).toLocaleString("nl-BE", { dateStyle: "short", timeStyle: "short" }) : "–");

function ReplicationView({ dc }: { dc: Datacenter }) {
  const isAdmin = useIsAdmin();
  const nodes = dc.hosts.map((h) => h.pve_node);
  const jobs = usePveGet<ReplicationJob[]>(dc.id, "/cluster/replication", { refetch: 30_000 });
  const guests = usePveGet<PveResource[]>(dc.id, "/cluster/resources", { params: { type: "vm" }, refetch: 30_000 });
  const states = usePerNode<ReplicationState[]>(dc.id, nodes, "replication", 30_000);
  const { run, pending, banner } = useCallRunner(dc.id);
  const [dialog, setDialog] = useState<{ kind: "add" } | { kind: "edit"; j: ReplicationJob } | { kind: "delete"; j: ReplicationJob } | null>(null);
  const close = () => setDialog(null);

  const byVMID = new Map((guests.data ?? []).map((g) => [g.vmid, g]));
  const stateOf = new Map(states.flatMap((s) => (s.data ?? []).map((x) => [x.id, x] as const)));
  const nodeErrors = states.filter((s) => s.error);
  const list = [...(jobs.data ?? [])].sort((a, b) => a.guest - b.guest || a.id.localeCompare(b.id));

  const toggle = (j: ReplicationJob) => void run({ method: "set", path: `/cluster/replication/${j.id}`, params: j.disable ? { delete: "disable" } : { disable: "1" } });
  const now = (j: ReplicationJob) => {
    const src = stateOf.get(j.id)?.source ?? j.source ?? byVMID.get(j.guest)?.node;
    if (src) void run({ method: "create", path: `/nodes/${src}/replication/${j.id}/schedule_now` });
  };

  return (
    <div className="space-y-6">
      {banner}
      {nodeErrors.length > 0 && <Alert kind="info">Status niet te lezen van {nodeErrors.map((n) => `${n.node} (${n.error})`).join(", ")}.</Alert>}
      <Card
        title={
          <span className="flex items-center justify-between gap-2">
            <span>Replicatiejobs</span>
            {isAdmin && (
              <Button variant="secondary" disabled={pending} onClick={() => setDialog({ kind: "add" })}>
                Job toevoegen
              </Button>
            )}
          </span>
        }
      >
        <QueryState q={jobs}>
          {list.length === 0 ? (
            <Empty>Nog geen replicatie. Voeg een job toe voor een gast op ZFS.</Empty>
          ) : (
            <div className="relative -mx-4 overflow-x-auto sm:mx-0">
              <table className={tableClass}>
                <thead>
                  <tr>
                    <th className={thClass}>Gast</th>
                    <th className={thClass}>Naar</th>
                    <th className={thClass}>Schema</th>
                    <th className={thClass}>Laatste</th>
                    <th className={thClass}>Volgende</th>
                    <th className={thClass}>Status</th>
                    <th className={thClass}>
                      <span className="sr-only">Acties</span>
                    </th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
                  {list.map((j) => {
                    const g = byVMID.get(j.guest);
                    const s = stateOf.get(j.id);
                    const failed = (s?.fail_count ?? 0) > 0 || !!s?.error;
                    return (
                      <tr key={j.id}>
                        <td className={tdClass}>
                          {g ? (
                            <Link href={guestHref(dc.id, { node: g.node, type: g.type, vmid: g.vmid })} className="font-medium hover:underline">
                              {guestLabel(g.type)} {g.vmid} {g.name}
                            </Link>
                          ) : (
                            <span>{j.guest}</span>
                          )}
                          <span className="block font-mono text-xs text-slate-500">{j.id}</span>
                          {j.comment && <span className="block text-xs text-slate-500">{j.comment}</span>}
                        </td>
                        <td className={tdClass}>{j.target}</td>
                        <td className={tdClass + " font-mono text-xs"}>
                          {j.schedule ?? "*/15"}
                          {j.rate ? <span className="block font-sans text-slate-500">max {j.rate} MB/s</span> : null}
                        </td>
                        <td className={tdClass + " whitespace-nowrap"}>
                          {when(s?.last_sync)}
                          {s?.duration ? <span className="block text-xs text-slate-500">{Math.round(s.duration)} s</span> : null}
                        </td>
                        <td className={tdClass + " whitespace-nowrap"}>{j.disable ? "–" : when(s?.next_sync)}</td>
                        <td className={tdClass}>
                          {j.disable ? (
                            <Badge tone="slate">uit</Badge>
                          ) : failed ? (
                            <>
                              <Badge tone="red">{s?.fail_count ? `${s.fail_count}× mislukt` : "fout"}</Badge>
                              {s?.error && <span className="block max-w-xs text-xs text-red-700 dark:text-red-300">{s.error}</span>}
                            </>
                          ) : s?.last_sync ? (
                            <Badge tone="green">in orde</Badge>
                          ) : (
                            <Badge tone="amber">nog niet gelopen</Badge>
                          )}
                        </td>
                        <td className={tdClass + " text-right"}>
                          {isAdmin && (
                            <ActionMenu
                              disabled={pending}
                              items={[
                                ...(j.disable ? [] : [{ label: "Nu synchroniseren", onClick: () => now(j) }]),
                                { label: "Wijzigen", onClick: () => setDialog({ kind: "edit", j }) },
                                { label: j.disable ? "Aanzetten" : "Uitzetten", onClick: () => toggle(j) },
                                { label: "Verwijderen…", onClick: () => setDialog({ kind: "delete", j }), danger: true },
                              ]}
                            />
                          )}
                        </td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </div>
          )}
        </QueryState>
      </Card>

      {dialog?.kind === "add" && <JobDialog guests={(guests.data ?? []).filter((g) => !g.template)} jobs={jobs.data ?? []} nodes={nodes} run={run} pending={pending} onClose={close} />}
      {dialog?.kind === "edit" && <JobDialog edit={dialog.j} guests={[]} jobs={[]} nodes={nodes} run={run} pending={pending} onClose={close} />}
      {dialog?.kind === "delete" && (
        <TypedConfirm
          title={`Replicatie ${dialog.j.id} verwijderen`}
          expect={dialog.j.id}
          confirmLabel="Verwijderen"
          pending={pending}
          onClose={close}
          onConfirm={() => void run({ method: "delete", path: `/cluster/replication/${dialog.j.id}` }).then((ok) => ok && close())}
        >
          <p className="text-sm">
            Proxmox stopt met kopiëren naar {dialog.j.target} en wist daar de gerepliceerde schijven. De gast zelf blijft onaangeroerd.
          </p>
        </TypedConfirm>
      )}
    </div>
  );
}

type Runner = ReturnType<typeof useCallRunner>["run"];

// nextJobID kiest het eerste vrije volgnummer per gast: <vmid>-<n>.
function nextJobID(vmid: number, jobs: ReplicationJob[]) {
  for (let n = 0; n < 1000; n++) if (!jobs.some((j) => j.id === `${vmid}-${n}`)) return `${vmid}-${n}`;
  return `${vmid}-0`;
}

function JobDialog({
  edit,
  guests,
  jobs,
  nodes,
  run,
  pending,
  onClose,
}: {
  edit?: ReplicationJob;
  guests: PveResource[];
  jobs: ReplicationJob[];
  nodes: string[];
  run: Runner;
  pending: boolean;
  onClose: () => void;
}) {
  const [vmid, setVmid] = useState(String(guests[0]?.vmid ?? ""));
  const guest = guests.find((g) => String(g.vmid) === vmid);
  const targets = nodes.filter((n) => n !== guest?.node && !jobs.some((j) => String(j.guest) === vmid && j.target === n));
  const [target, setTarget] = useState("");
  const [schedule, setSchedule] = useState(edit?.schedule ?? "*/15");
  const [rate, setRate] = useState(edit?.rate ? String(edit.rate) : "");
  const [comment, setComment] = useState(edit?.comment ?? "");
  const tgt = target && targets.includes(target) ? target : (targets[0] ?? "");
  const error = !edit && !guest ? "Kies een gast." : !edit && !tgt ? "Deze gast repliceert al naar alle andere hosts." : rate && !/^\d+(\.\d+)?$/.test(rate) ? "Snelheid is een getal in MB/s." : !schedule.trim() ? "Geef een schema." : null;
  const submit = () => {
    const p: Record<string, string> = { schedule: schedule.trim() };
    if (rate) p.rate = rate;
    if (comment) p.comment = comment;
    const call = edit
      ? { method: "set" as const, path: `/cluster/replication/${edit.id}`, params: { ...p, ...(!rate && edit.rate ? { delete: "rate" } : {}) } }
      : { method: "create" as const, path: "/cluster/replication", params: { ...p, id: nextJobID(Number(vmid), jobs), type: "local", target: tgt } };
    void run(call).then((ok) => ok && onClose());
  };
  return (
    <FormModal title={edit ? `Replicatie ${edit.id}` : "Replicatie toevoegen"} onClose={onClose} onSubmit={submit} submitLabel="Opslaan" disabled={pending} error={error}>
      {!edit && (
        <div className="grid gap-3 sm:grid-cols-2">
          <Field label="Gast" htmlFor="rep-g">
            <Select id="rep-g" value={vmid} onChange={(e) => setVmid(e.target.value)}>
              {guests.map((g) => (
                <option key={g.id} value={g.vmid}>
                  {guestLabel(g.type)} {g.vmid} {g.name} (op {g.node})
                </option>
              ))}
            </Select>
          </Field>
          <Field label="Naar host" htmlFor="rep-t">
            <Select id="rep-t" value={tgt} onChange={(e) => setTarget(e.target.value)}>
              {targets.map((n) => (
                <option key={n}>{n}</option>
              ))}
            </Select>
          </Field>
        </div>
      )}
      <div className="grid gap-3 sm:grid-cols-2">
        <Field label="Schema" htmlFor="rep-s" hint="Proxmox-kalender: */15 is elk kwartier, 22:30 elke avond, mon..fri 8-18/1 elk uur op werkdagen">
          <Input id="rep-s" value={schedule} onChange={(e) => setSchedule(e.target.value)} className="font-mono" />
        </Field>
        <Field label="Maximale snelheid (MB/s)" htmlFor="rep-r" hint="Leeg is onbeperkt">
          <Input id="rep-r" inputMode="decimal" value={rate} onChange={(e) => setRate(e.target.value)} />
        </Field>
      </div>
      <Field label="Opmerking" htmlFor="rep-c">
        <Input id="rep-c" value={comment} onChange={(e) => setComment(e.target.value)} />
      </Field>
      {!edit && <p className="text-xs text-slate-500">Alle schijven van de gast moeten op lokale ZFS staan; anders meldt de eerste run een fout. Wil je een schijf overslaan, zet dan replicate=0 op die schijf.</p>}
    </FormModal>
  );
}
