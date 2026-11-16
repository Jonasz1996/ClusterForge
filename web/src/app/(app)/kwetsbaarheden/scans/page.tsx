"use client";

import Link from "next/link";
import { useRouter, useSearchParams } from "next/navigation";
import { Suspense, useState } from "react";
import { askConfirm } from "@/components/Confirm";
import { Modal } from "@/components/Modal";
import { Tabs } from "@/components/Tabs";
import { ago, tableClass, tdClass, thClass } from "@/components/inventory/bits";
import { FindingRow } from "@/components/security/FindingRow";
import { Alert, Badge, Button, Card, Field, Input, PageHeader, Select, cx } from "@/components/ui";
import { useIsAdmin } from "@/lib/inventory";
import {
  knownLabel,
  profileLabel,
  useDeleteScan,
  useLinkScanner,
  useRunScan,
  useSaveScan,
  useScanRun,
  useScans,
  useSecurityFindings,
  useSecurityScopes,
  weekdays,
  type RunHost,
  type ScanRun,
  type ScansOverview,
  type SecurityScan,
  type SecurityScope,
} from "@/lib/security";

// Netwerkscans (fase 27 M3): een scanner (de scannercontainer of een node
// met nmap) scant een eigen scope. ClusterForge legt de open poorten naast
// wat de agents en de firewall weten.

const tabs = [
  { key: "scans", label: "Scans" },
  { key: "bevindingen", label: "Bevindingen" },
  { key: "uitvoeringen", label: "Uitvoeringen" },
] as const;
type Tab = (typeof tabs)[number]["key"];

export default function ScansPage() {
  return (
    <Suspense>
      <Scans />
    </Suspense>
  );
}

function Scans() {
  const params = useSearchParams();
  const router = useRouter();
  const tab = (tabs.find((t) => t.key === params.get("tab"))?.key ?? "scans") as Tab;
  const runParam = params.get("run");
  const data = useScans();
  const scopes = useSecurityScopes();
  const isAdmin = useIsAdmin();
  const [edit, setEdit] = useState<SecurityScan | "new" | null>(null);
  const go = (t: Tab, run?: string) => router.replace(`/kwetsbaarheden/scans?tab=${t}${run ? `&run=${run}` : ""}`);
  const noScopes = scopes.data && scopes.data.items.length === 0;
  return (
    <div className="space-y-6">
      <PageHeader
        title="Netwerkscans"
        description="Een scanner zoekt met nmap welke poorten open staan binnen je eigen scopes, met de versies en de TLS- en SSH-instellingen van wat er luistert. Alleen veilige scripts: geen brute force, geen exploits. ClusterForge vergelijkt het resultaat met wat de agents en de firewall weten."
        actions={
          isAdmin && (
            <Button onClick={() => setEdit("new")} disabled={!scopes.data || noScopes}>
              Scan toevoegen
            </Button>
          )
        }
      />
      <Tabs tabs={tabs} value={tab} onChange={(t) => go(t)} />
      {data.isError && <Alert>Scans laden mislukte.</Alert>}
      {noScopes && (
        <Alert kind="info">
          Maak eerst een{" "}
          <Link href="/kwetsbaarheden/scopes" className="font-medium underline">
            eigen scope
          </Link>
          : een scan gaat nooit buiten je eigen netwerken.
        </Alert>
      )}
      {data.data && tab === "scans" && (
        <>
          <Scanners o={data.data} isAdmin={isAdmin} />
          <ScanList o={data.data} isAdmin={isAdmin} onEdit={setEdit} onRun={(id) => go("uitvoeringen", id)} />
        </>
      )}
      {tab === "bevindingen" && <NetworkFindings isAdmin={isAdmin} />}
      {data.data && tab === "uitvoeringen" && <Runs o={data.data} selected={runParam} onSelect={(id) => go("uitvoeringen", id)} />}
      {edit && scopes.data && data.data && (
        <ScanForm scan={edit === "new" ? null : edit} scopes={scopes.data.items} o={data.data} onClose={() => setEdit(null)} />
      )}
    </div>
  );
}

function Scanners({ o, isAdmin }: { o: ScansOverview; isAdmin: boolean }) {
  const link = useLinkScanner();
  const container = o.scanners.find((s) => s.container);
  return (
    <Card title="Scanners">
      {o.scanners.length === 0 && (
        <p className="text-sm text-slate-600 dark:text-slate-400">
          Nog geen scanner. De scannercontainer draait naast de server en ziet je VLAN&apos;s zoals de server ze ziet. Een eigen VM (bijvoorbeeld
          Kali) met de agent en nmap verschijnt hier ook vanzelf.
        </p>
      )}
      {o.scanners.length > 0 && (
        <ul className="divide-y divide-slate-100 dark:divide-slate-800">
          {o.scanners.map((s) => (
            <li key={s.node_id} className="flex flex-wrap items-center gap-x-3 gap-y-1 py-2">
              <span className="font-medium">{s.hostname}</span>
              {s.container && <Badge tone="blue">container</Badge>}
              <span className="text-sm text-slate-500">{s.address}</span>
              <span className="ml-auto">
                <Badge tone={s.ready ? "green" : "amber"}>{s.ready ? "klaar" : s.note}</Badge>
              </span>
            </li>
          ))}
        </ul>
      )}
      {isAdmin && !container && (
        <div className="mt-3 space-y-2 rounded-md bg-slate-50 p-3 text-sm dark:bg-slate-800/50">
          <div className="font-medium">Scannercontainer koppelen</div>
          <p className="text-slate-600 dark:text-slate-400">
            Zet eenmalig <span className="font-mono">COMPOSE_PROFILES=scanner</span> in de .env van de server en start de stack opnieuw. Klik dan
            hier: ClusterForge maakt de node cf-scanner en de container meldt zich vanzelf aan.
          </p>
          <Button variant="secondary" onClick={() => link.mutate()} disabled={link.isPending}>
            Scannercontainer koppelen
          </Button>
          {link.isSuccess && <Alert kind="success">Gekoppeld. De container meldt zich binnen een minuut aan.</Alert>}
          {link.isError && <Alert>{link.error instanceof Error ? link.error.message : String(link.error)}</Alert>}
        </div>
      )}
      {isAdmin && container && !container.ready && container.note === "offline" && (
        <div className="mt-3 flex flex-wrap items-center gap-2 text-sm text-slate-600 dark:text-slate-400">
          De container is nog niet aangemeld of staat uit.
          <Button variant="secondary" onClick={() => link.mutate()} disabled={link.isPending}>
            Opnieuw koppelen
          </Button>
        </div>
      )}
    </Card>
  );
}

function schedText(s: SecurityScan) {
  const h = `${String(s.hour).padStart(2, "0")}:00`;
  if (s.schedule === "dagelijks") return `elke dag om ${h}`;
  if (s.schedule === "wekelijks") return `elke ${weekdays[s.weekday]} om ${h}`;
  return "alleen met de hand";
}

function runTone(r: ScanRun) {
  return r.status === "klaar" ? "green" : r.status === "mislukt" ? "red" : "blue";
}

function ScanList({
  o,
  isAdmin,
  onEdit,
  onRun,
}: {
  o: ScansOverview;
  isAdmin: boolean;
  onEdit: (s: SecurityScan) => void;
  onRun: (runId: string) => void;
}) {
  const run = useRunScan();
  const remove = useDeleteScan();
  if (o.scans.length === 0) return null;
  return (
    <div className="space-y-3">
      {o.scans.map((s) => (
        <Card key={s.id}>
          <div className="flex flex-wrap items-start gap-3">
            <div className="min-w-0 basis-full sm:basis-0 sm:flex-1">
              <div className="font-medium">{s.name}</div>
              <div className="text-sm text-slate-500">
                {s.scope_name} · {s.profile} · {schedText(s)} · scanner {s.scanner_name || "automatisch"}
              </div>
              {s.last_run && (
                <button className="mt-1 text-sm text-blue-600 hover:underline dark:text-blue-400" onClick={() => onRun(s.last_run!.id)}>
                  Laatst {ago(s.last_run.started_at)}:{" "}
                  {s.last_run.status === "klaar"
                    ? `${s.last_run.hosts_up} hosts, ${s.last_run.open_ports} open poorten, ${s.last_run.findings} bevindingen`
                    : s.last_run.status === "mislukt"
                      ? `mislukt: ${s.last_run.error}`
                      : "bezig…"}
                </button>
              )}
              {s.next_run && s.schedule !== "uit" && (
                <div className="text-xs text-slate-500">Volgende: {new Date(s.next_run).toLocaleString("nl-BE")}</div>
              )}
            </div>
            {s.running && <Badge tone="blue">scant…</Badge>}
            {isAdmin && (
              <div className="flex flex-wrap gap-2">
                <Button
                  onClick={() => run.mutate(s.id, { onSuccess: (r) => onRun(r.run_id) })}
                  disabled={s.running || run.isPending || !o.scanners.some((x) => x.ready)}
                >
                  Nu scannen
                </Button>
                <Button variant="secondary" onClick={() => onEdit(s)}>
                  Wijzigen
                </Button>
                <Button
                  variant="secondary-danger"
                  onClick={async () => {
                    if (
                      await askConfirm("De uitvoeringen gaan mee weg; de bevindingen blijven tot een volgende scan.", {
                        title: `Scan ${s.name} verwijderen?`,
                        danger: true,
                      })
                    )
                      remove.mutate(s.id);
                  }}
                >
                  Verwijderen
                </Button>
              </div>
            )}
          </div>
        </Card>
      ))}
      {run.isError && <Alert>{run.error instanceof Error ? run.error.message : String(run.error)}</Alert>}
    </div>
  );
}

function NetworkFindings({ isAdmin }: { isAdmin: boolean }) {
  const list = useSecurityFindings({ kind: "netwerk" });
  const [open, setOpen] = useState<string | null>(null);
  if (list.isError) return <Alert>Bevindingen laden mislukte.</Alert>;
  if (!list.data) return null;
  if (list.data.length === 0) return <Alert kind="info">Geen open bevindingen uit netwerkscans.</Alert>;
  return (
    <div className="space-y-2">
      {list.data.map((f) => (
        <FindingRow key={f.id} f={f} open={open === f.id} toggle={() => setOpen(open === f.id ? null : f.id)} isAdmin={isAdmin} />
      ))}
    </div>
  );
}

function Runs({ o, selected, onSelect }: { o: ScansOverview; selected: string | null; onSelect: (id: string) => void }) {
  if (o.runs.length === 0) return <Alert kind="info">Nog geen scans uitgevoerd.</Alert>;
  const current = selected ?? o.runs[0].id;
  return (
    <div className="grid items-start gap-4 lg:grid-cols-[18rem_1fr]">
      <Card>
        <ul className="-my-1 divide-y divide-slate-100 dark:divide-slate-800">
          {o.runs.map((r) => (
            <li key={r.id}>
              <button
                className={cx("w-full rounded px-2 py-2 text-left text-sm", r.id === current && "bg-slate-100 dark:bg-slate-800")}
                onClick={() => onSelect(r.id)}
              >
                <div className="flex items-center gap-2">
                  <span className="font-medium">{r.scan_name}</span>
                  <Badge tone={runTone(r)}>{r.status}</Badge>
                </div>
                <div className="text-xs text-slate-500">
                  {new Date(r.started_at).toLocaleString("nl-BE")} · {r.scanner_name}
                </div>
              </button>
            </li>
          ))}
        </ul>
      </Card>
      <RunDetail id={current} />
    </div>
  );
}

function RunDetail({ id }: { id: string }) {
  const d = useScanRun(id);
  if (d.isError) return <Alert>Uitvoering laden mislukte.</Alert>;
  if (!d.data) return null;
  const r = d.data;
  const dur = r.finished_at ? Math.round((new Date(r.finished_at).getTime() - new Date(r.started_at).getTime()) / 1000) : null;
  return (
    <div className="min-w-0 space-y-4">
      <Card>
        <div className="flex flex-wrap items-center gap-2">
          <span className="font-medium">{r.scan_name}</span>
          <Badge tone={runTone(r)}>{r.status}</Badge>
          <span className="text-sm text-slate-500">
            {r.trigger === "schema" ? "volgens schema" : "met de hand"} · {r.scanner_name}
            {dur !== null && ` · ${dur < 120 ? `${dur} s` : `${Math.round(dur / 60)} min`}`}
          </span>
        </div>
        <div className="mt-1 font-mono text-xs text-slate-500">{r.targets.join(", ")}</div>
        {r.status === "klaar" && (
          <div className="mt-2 text-sm">
            {r.hosts_up} hosts bereikbaar, {r.open_ports} open poorten, {r.findings} bevindingen
          </div>
        )}
        {r.error && <Alert>{r.error}</Alert>}
        {r.result?.notes.map((n) => (
          <p key={n} className="mt-2 text-sm text-amber-700 dark:text-amber-400">
            {n}
          </p>
        ))}
        {r.result && (
          <details className="mt-2 text-xs text-slate-500">
            <summary className="cursor-pointer">nmap {r.result.nmap}</summary>
            <code className="block break-all">nmap {r.result.args.join(" ")}</code>
          </details>
        )}
      </Card>
      {r.status === "bezig" && <Alert kind="info">De scan loopt; deze pagina ververst vanzelf.</Alert>}
      {r.result?.hosts.map((h) => (
        <HostCard key={h.addr} h={h} />
      ))}
    </div>
  );
}

function HostCard({ h }: { h: RunHost }) {
  const [open, setOpen] = useState<number | null>(null);
  return (
    <Card>
      <div className="flex flex-wrap items-baseline gap-x-3 gap-y-1">
        <span className="font-mono font-medium">{h.addr}</span>
        {h.node ? (
          <span className="text-sm">
            {h.node}
            {h.cluster && <span className="text-slate-500"> · {h.cluster}</span>}
          </span>
        ) : (
          <Badge tone="slate">geen node in ClusterForge</Badge>
        )}
        {(h.hostname || h.vendor) && <span className="text-xs text-slate-500">{[h.hostname, h.vendor].filter(Boolean).join(" · ")}</span>}
      </div>
      {h.ports.length === 0 ? (
        <p className="mt-2 text-sm text-slate-500">Bereikbaar, geen open poorten gevonden.</p>
      ) : (
        <div className="mt-2 overflow-x-auto">
          <table className={tableClass}>
            <thead>
              <tr>
                <th className={thClass}>Poort</th>
                <th className={thClass}>Dienst</th>
                <th className={thClass}>Verwacht</th>
              </tr>
            </thead>
            <tbody>
              {h.ports.map((p, i) => {
                const e = h.expected[i];
                const k = knownLabel[e?.known ?? ""];
                const scripts = Object.entries(p.scripts ?? {});
                return (
                  <tr key={`${p.proto}/${p.port}`} className="border-t border-slate-100 align-top dark:border-slate-800">
                    <td className={cx(tdClass, "whitespace-nowrap font-mono")}>
                      {p.port}/{p.proto}
                    </td>
                    <td className={tdClass}>
                      <div>
                        {p.tunnel === "ssl" && "TLS · "}
                        {[p.service, p.product, p.version].filter(Boolean).join(" ") || "–"}
                      </div>
                      {p.extra && <div className="text-xs text-slate-500">{p.extra}</div>}
                      {e?.title && <div className="text-xs text-slate-500">“{e.title}”</div>}
                      {e?.hits?.map((x) => (
                        <div key={x.id} className="mt-1">
                          <Badge tone="red">{x.detail || x.id}</Badge>
                        </div>
                      ))}
                      {scripts.length > 0 && (
                        <button className="text-xs text-blue-600 hover:underline dark:text-blue-400" onClick={() => setOpen(open === i ? null : i)}>
                          {open === i ? "verberg details" : `${scripts.length} details`}
                        </button>
                      )}
                      {open === i &&
                        scripts.map(([id, out]) => (
                          <div key={id} className="mt-2">
                            <div className="text-xs font-medium">{id}</div>
                            <pre className="max-h-64 overflow-auto rounded bg-slate-50 p-2 text-xs dark:bg-slate-800">{out}</pre>
                          </div>
                        ))}
                    </td>
                    <td className={cx(tdClass, "whitespace-nowrap")}>
                      <Badge tone={k.tone}>{k.label}</Badge>
                      {e?.process && <div className="text-xs text-slate-500">{e.process}</div>}
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
    </Card>
  );
}

function ScanForm({ scan, scopes, o, onClose }: { scan: SecurityScan | null; scopes: SecurityScope[]; o: ScansOverview; onClose: () => void }) {
  const save = useSaveScan();
  const [name, setName] = useState(scan?.name ?? "");
  const [scope, setScope] = useState(scan?.scope_id ?? scopes[0]?.id ?? "");
  const [scanner, setScanner] = useState(scan?.scanner_id ?? "");
  const [profile, setProfile] = useState<string>(scan?.profile ?? "standaard");
  const [schedule, setSchedule] = useState<string>(scan?.schedule ?? "wekelijks");
  const [weekday, setWeekday] = useState(String(scan?.weekday ?? 0));
  const [hour, setHour] = useState(String(scan?.hour ?? 2));
  const [rate, setRate] = useState(String(scan?.max_rate ?? 300));
  const [defaults, setDefaults] = useState(scan?.default_passwords ?? false);
  const submit = () =>
    save.mutate(
      {
        id: scan?.id,
        body: {
          name,
          scope_id: scope,
          scanner_id: scanner || null,
          profile: profile as "snel" | "standaard" | "volledig",
          schedule: schedule as "uit" | "dagelijks" | "wekelijks",
          weekday: Number(weekday),
          hour: Number(hour),
          max_rate: Number(rate),
          default_passwords: defaults,
        },
      },
      { onSuccess: onClose },
    );
  return (
    <Modal title={scan ? `Scan ${scan.name}` : "Nieuwe scan"} onClose={onClose} wide>
      <div className="space-y-3">
        <div className="grid gap-3 sm:grid-cols-2">
          <Field label="Naam" htmlFor="scan-naam">
            <Input id="scan-naam" value={name} onChange={(e) => setName(e.target.value)} maxLength={60} placeholder="Servers wekelijks" />
          </Field>
          <Field label="Eigen scope" htmlFor="scan-scope">
            <Select id="scan-scope" value={scope} onChange={(e) => setScope(e.target.value)}>
              {scopes.map((s) => (
                <option key={s.id} value={s.id}>
                  {s.name} ({s.cidrs.join(", ")})
                </option>
              ))}
            </Select>
          </Field>
          <Field label="Profiel" htmlFor="scan-profiel">
            <Select id="scan-profiel" value={profile} onChange={(e) => setProfile(e.target.value)}>
              {o.profiles.map((p) => (
                <option key={p} value={p}>
                  {profileLabel[p] ?? p}
                </option>
              ))}
            </Select>
          </Field>
          <Field label="Scanner" htmlFor="scan-scanner">
            <Select id="scan-scanner" value={scanner} onChange={(e) => setScanner(e.target.value)}>
              <option value="">Automatisch (de container eerst)</option>
              {o.scanners.map((s) => (
                <option key={s.node_id} value={s.node_id}>
                  {s.hostname}
                  {s.ready ? "" : ` (${s.note})`}
                </option>
              ))}
            </Select>
          </Field>
          <Field label="Schema" htmlFor="scan-schema">
            <Select id="scan-schema" value={schedule} onChange={(e) => setSchedule(e.target.value)}>
              <option value="wekelijks">Wekelijks</option>
              <option value="dagelijks">Dagelijks</option>
              <option value="uit">Alleen met de hand</option>
            </Select>
          </Field>
          {schedule !== "uit" && (
            <div className="grid grid-cols-2 gap-2">
              {schedule === "wekelijks" && (
                <Field label="Dag" htmlFor="scan-dag">
                  <Select id="scan-dag" value={weekday} onChange={(e) => setWeekday(e.target.value)}>
                    {weekdays.map((d, i) => (
                      <option key={d} value={i}>
                        {d}
                      </option>
                    ))}
                  </Select>
                </Field>
              )}
              <Field label="Uur" htmlFor="scan-uur">
                <Select id="scan-uur" value={hour} onChange={(e) => setHour(e.target.value)}>
                  {Array.from({ length: 24 }, (_, i) => (
                    <option key={i} value={i}>
                      {String(i).padStart(2, "0")}:00
                    </option>
                  ))}
                </Select>
              </Field>
            </div>
          )}
          <Field label="Pakketten per seconde" htmlFor="scan-rate" hint="Lager is rustiger voor je netwerk; 300 is een veilige standaard.">
            <Input id="scan-rate" type="number" min={10} max={5000} value={rate} onChange={(e) => setRate(e.target.value)} />
          </Field>
        </div>
        <label className="flex items-start gap-2 rounded-md border border-slate-200 p-3 text-sm dark:border-slate-700">
          <input type="checkbox" className="mt-1" checked={defaults} onChange={(e) => setDefaults(e.target.checked)} />
          <span>
            <span className="font-medium">Standaardwachtwoorden controleren</span>
            <span className="block text-slate-500">
              Per dienst één aanmeldpoging met het bekende standaardwachtwoord (admin/admin bij Grafana en bij een login van de browser). Nooit meer
              dan één poging, dus geen risico op een vergrendeld account.
            </span>
          </span>
        </label>
        <p className="text-xs text-slate-500">
          Scripts die meelopen: {o.scripts.join(", ")}. Daarna spreekt de scanner elke webdienst, Redis en Memcached één keer anoniem aan om panelen
          en API&apos;s zonder login te vinden.
        </p>
        {save.isError && <Alert>{save.error instanceof Error ? save.error.message : String(save.error)}</Alert>}
        <div className="flex justify-end gap-2">
          <Button variant="secondary" onClick={onClose}>
            Annuleren
          </Button>
          <Button onClick={submit} disabled={save.isPending || !name.trim() || !scope}>
            Bewaren
          </Button>
        </div>
      </div>
    </Modal>
  );
}
