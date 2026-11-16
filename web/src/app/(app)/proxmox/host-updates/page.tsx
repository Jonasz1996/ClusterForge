"use client";

import Link from "next/link";
import { useRouter } from "next/navigation";
import { Suspense, useState } from "react";
import { Empty, QueryState, tableClass, tdClass, thClass } from "@/components/inventory/bits";
import { DatacenterGate, TypedConfirm, useCallRunner } from "@/components/pve/bits";
import { Check } from "@/components/pve/guest/common";
import { Alert, Badge, Button, Card, Field, PageHeader, Select } from "@/components/ui";
import { useIsAdmin } from "@/lib/inventory";
import { jobStatuses, useJobs } from "@/lib/proxmox";
import {
  guestLabel,
  hostUpdatePlan,
  startHostUpdate,
  usePerNode,
  usePveOverview,
  type AptPackage,
  type Datacenter,
  type GuestType,
  type HostUpdateInput,
  type HostUpdatePlan,
} from "@/lib/pve";

// Proxmox-hosts één voor één bijwerken (fase 26 M4): onderhoudsmodus aan,
// gasten weg, apt full-upgrade, herstarten als het moet, wachten, gasten
// terug. Het plan is vooraf te bekijken.

export default function HostUpdatesPage() {
  return (
    <div className="space-y-6">
      <PageHeader
        title="Host-updates"
        description="Werk Proxmox-hosts één voor één bij. ClusterForge maakt elke host eerst leeg door de gasten te verhuizen, werkt bij, herstart als het nodig is en zet de gasten terug."
      />
      <Suspense>
        <DatacenterGate>{(dc) => <HostUpdatesView key={dc.id} dc={dc} />}</DatacenterGate>
      </Suspense>
    </div>
  );
}

function HostUpdatesView({ dc }: { dc: Datacenter }) {
  const isAdmin = useIsAdmin();
  const router = useRouter();
  const nodes = dc.hosts.map((h) => h.pve_node);
  const overview = usePveOverview(dc.id);
  const apt = usePerNode<AptPackage[]>(dc.id, nodes, "apt/update", 300_000);
  const { run, pending, banner } = useCallRunner(dc.id);
  const [order, setOrder] = useState<string[]>([]);
  const [open, setOpen] = useState<string | null>(null);
  const [reboot, setReboot] = useState<HostUpdateInput["reboot"]>("needed");
  const [stuck, setStuck] = useState(false);
  const [plan, setPlan] = useState<HostUpdatePlan | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [confirm, setConfirm] = useState(false);

  const ver = new Map(dc.hosts.map((h) => [h.pve_node, h.version]));
  const info = new Map((overview.data?.nodes ?? []).map((n) => [n.name, n]));
  const input: HostUpdateInput = { hosts: order, reboot, shutdown_stuck: stuck };
  const toggle = (n: string) => {
    setPlan(null);
    setOrder((o) => (o.includes(n) ? o.filter((x) => x !== n) : [...o, n]));
  };
  const showPlan = async () => {
    setBusy(true);
    setError(null);
    try {
      setPlan(await hostUpdatePlan(dc.id, input));
    } catch (e) {
      setPlan(null);
      setError(e instanceof Error ? e.message : "Plan maken mislukt");
    } finally {
      setBusy(false);
    }
  };
  const start = async (typed: string) => {
    setBusy(true);
    setError(null);
    try {
      const job = await startHostUpdate(dc.id, { ...input, confirm: typed });
      router.push(`/taken/detail?id=${job.id}`);
    } catch (e) {
      setConfirm(false);
      setError(e instanceof Error ? e.message : "Starten mislukt");
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="space-y-6">
      {banner}
      {error && <Alert kind="error">{error}</Alert>}
      <Card title="Hosts">
        <QueryState q={overview}>
          <div className="relative -mx-4 overflow-x-auto sm:mx-0">
            <table className={tableClass}>
              <thead>
                <tr>
                  <th className={thClass}>
                    <span className="sr-only">Kiezen</span>
                  </th>
                  <th className={thClass}>Host</th>
                  <th className={thClass}>Updates</th>
                  <th className={thClass}>Herstart</th>
                  <th className={thClass}>
                    <span className="sr-only">Acties</span>
                  </th>
                </tr>
              </thead>
              <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
                {apt.map((a) => {
                  const n = info.get(a.node);
                  const pos = order.indexOf(a.node);
                  const pkgs = a.data ?? [];
                  const pve = pkgs.filter((p) => /^(pve-|proxmox-|qemu-server|lxc-pve|corosync|libpve)/.test(p.Package));
                  return (
                    <tr key={a.node} className="align-top">
                      <td className={tdClass}>
                        {isAdmin && <input type="checkbox" aria-label={`${a.node} bijwerken`} checked={pos >= 0} disabled={!n?.online} onChange={() => toggle(a.node)} />}
                      </td>
                      <td className={tdClass}>
                        <span className="font-medium">{a.node}</span>
                        {pos >= 0 && (
                          <span className="ml-2">
                            <Badge tone="blue">{pos + 1}e</Badge>
                          </span>
                        )}
                        {!n?.online && <span className="block text-xs text-red-700 dark:text-red-300">offline</span>}
                        {ver.get(a.node) && <span className="block text-xs text-slate-500">PVE {ver.get(a.node)}</span>}
                      </td>
                      <td className={tdClass}>
                        {a.loading ? (
                          <span className="text-slate-500">laden…</span>
                        ) : a.error ? (
                          <span className="text-xs text-red-700 dark:text-red-300">{a.error}</span>
                        ) : pkgs.length === 0 ? (
                          <Badge tone="green">bijgewerkt</Badge>
                        ) : (
                          <>
                            <button type="button" className="text-left hover:underline" onClick={() => setOpen(open === a.node ? null : a.node)}>
                              <Badge tone="amber">{pkgs.length} pakketten</Badge>
                              {pve.length > 0 && <span className="ml-2 text-xs text-slate-500">waarvan {pve.length} van Proxmox</span>}
                            </button>
                            {open === a.node && (
                              <ul className="mt-2 max-h-64 space-y-0.5 overflow-y-auto font-mono text-xs">
                                {pkgs.map((p) => (
                                  <li key={p.Package}>
                                    {p.Package} <span className="text-slate-500">{p.OldVersion ? `${p.OldVersion} → ` : ""}{p.Version}</span>
                                  </li>
                                ))}
                              </ul>
                            )}
                          </>
                        )}
                      </td>
                      <td className={tdClass}>
                        {n?.reboot_required ? (
                          <span title={(n.reboot_reasons ?? []).join(", ")}>
                            <Badge tone="amber">nodig</Badge>
                          </span>
                        ) : (
                          <span className="text-slate-500">–</span>
                        )}
                      </td>
                      <td className={tdClass + " text-right"}>
                        {isAdmin && n?.online && (
                          <Button variant="secondary" disabled={pending} onClick={() => void run({ method: "create", path: `/nodes/${a.node}/apt/update` })}>
                            Lijst vernieuwen
                          </Button>
                        )}
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        </QueryState>
        <p className="mt-3 text-xs text-slate-500">
          De volgorde is de volgorde waarin je de hosts aanvinkt. Proxmox raadt aan alle hosts van een cluster op dezelfde versie te houden, dus werk ze na elkaar allemaal bij.
        </p>
      </Card>

      {isAdmin && (
        <Card title="Ronde starten">
          <div className="space-y-4">
            <div className="grid gap-3 sm:grid-cols-2">
              <Field label="Herstarten" htmlFor="hu-reboot">
                <Select id="hu-reboot" value={reboot} onChange={(e) => (setPlan(null), setReboot(e.target.value as HostUpdateInput["reboot"]))}>
                  <option value="needed">Alleen als het nodig is (nieuwe kernel of bibliotheken)</option>
                  <option value="always">Altijd</option>
                  <option value="never">Nooit (ik herstart later zelf)</option>
                </Select>
              </Field>
            </div>
            <Check id="hu-stuck" checked={stuck} onChange={(v) => (setPlan(null), setStuck(v))}>
              Gasten die niet kunnen verhuizen (passthrough, lokale ISO, geen ruimte elders) stilleggen tijdens de update en daarna weer starten. Uit: de ronde stopt
              liever dan zo&apos;n gast uit te zetten.
            </Check>
            <div className="flex flex-wrap gap-2">
              <Button variant="secondary" disabled={busy || order.length === 0} onClick={() => void showPlan()}>
                Plan bekijken
              </Button>
              <Button disabled={busy || order.length === 0 || !plan} onClick={() => setConfirm(true)}>
                Bijwerken starten
              </Button>
            </div>
            {order.length === 0 && <p className="text-sm text-slate-500">Vink hierboven de hosts aan die je wilt bijwerken.</p>}
            {order.length > 0 && !plan && <p className="text-sm text-slate-500">Bekijk eerst het plan; daarna kun je starten.</p>}
            {plan && <PlanView plan={plan} />}
          </div>
        </Card>
      )}

      <RecentRounds />

      {confirm && (
        <TypedConfirm title="Proxmox-hosts bijwerken" expect={dc.name} confirmLabel="Starten" pending={busy} onConfirm={(t) => void start(t)} onClose={() => setConfirm(false)}>
          <p className="text-sm">
            ClusterForge werkt {order.join(", ")} na elkaar bij. Per host gaan de gasten eerst weg; {reboot === "never" ? "de host herstart niet" : reboot === "always" ? "daarna herstart de host" : "daarna herstart de host als dat nodig is"}.
          </p>
        </TypedConfirm>
      )}
    </div>
  );
}

function PlanView({ plan }: { plan: HostUpdatePlan }) {
  const ty = (t: string) => guestLabel(t as GuestType);
  return (
    <div className="space-y-3">
      {plan.plan.hosts.map((h, i) => (
        <div key={h.host} className="rounded-md border border-slate-200 p-3 text-sm dark:border-slate-700">
          <p className="font-medium">
            {i + 1}. {h.host}
          </p>
          {!h.moves?.length && !h.stops?.length && !h.ha?.length && <p className="text-slate-500">Geen gasten die draaien; meteen bijwerken.</p>}
          {!!h.moves?.length && (
            <ul className="mt-1 space-y-0.5">
              {h.moves.map((g) => (
                <li key={g.vmid}>
                  {ty(g.type)} {g.vmid} {g.name} verhuist naar {g.target} en komt daarna terug.
                </li>
              ))}
            </ul>
          )}
          {!!h.ha?.length && <p className="mt-1">Onder HA (Proxmox verhuist ze zelf bij onderhoudsmodus): {h.ha.join(", ")}.</p>}
          {!!h.stops?.length && (
            <ul className="mt-1 space-y-0.5 text-amber-800 dark:text-amber-300">
              {h.stops.map((g) => (
                <li key={g.vmid}>
                  {ty(g.type)} {g.vmid} {g.name} kan niet verhuizen en ligt stil tijdens de update.
                </li>
              ))}
            </ul>
          )}
        </div>
      ))}
      {plan.lines.length > 0 && (
        <details className="text-xs text-slate-500">
          <summary className="cursor-pointer">Details</summary>
          <ul className="mt-1 space-y-0.5">
            {plan.lines.map((l, i) => (
              <li key={i}>{l}</li>
            ))}
          </ul>
        </details>
      )}
    </div>
  );
}

function RecentRounds() {
  const jobs = useJobs({ limit: 200 });
  const rounds = (jobs.data ?? []).filter((j) => j.title.startsWith("Proxmox-hosts bijwerken")).slice(0, 10);
  return (
    <Card title="Laatste rondes">
      <QueryState q={jobs}>
        {rounds.length === 0 ? (
          <Empty>Nog geen rondes.</Empty>
        ) : (
          <ul className="divide-y divide-slate-100 text-sm dark:divide-slate-800">
            {rounds.map((j) => (
              <li key={j.id} className="flex items-center justify-between gap-2 py-2">
                <span>
                  <Link href={`/taken/detail?id=${j.id}`} className="hover:underline">
                    {j.title.replace(/^Proxmox-hosts bijwerken: /, "")}
                  </Link>
                  <span className="block text-xs text-slate-500">{new Date(j.created_at).toLocaleString("nl-BE", { dateStyle: "short", timeStyle: "short" })}</span>
                  {j.error && <span className="block text-xs text-red-700 dark:text-red-300">{j.error}</span>}
                </span>
                <Badge tone={jobStatuses[j.status].tone}>{jobStatuses[j.status].label}</Badge>
              </li>
            ))}
          </ul>
        )}
      </QueryState>
    </Card>
  );
}
