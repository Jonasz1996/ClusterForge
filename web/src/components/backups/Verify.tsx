"use client";

import Link from "next/link";
import { useRouter } from "next/navigation";
import { useState, type ReactNode } from "react";
import { Modal } from "@/components/Modal";
import { upgradeCommand } from "@/components/drift/Drift";
import { Checks, reportHref, RunBadge } from "@/components/failover/Failover";
import { CopyBlock } from "@/components/inventory/InstallAgent";
import { ago, formatBytes, tableClass, tdClass, thClass } from "@/components/inventory/bits";
import { Alert, Badge, Button, Card, Select, cx } from "@/components/ui";
import { backupTimeFmt, useNodeBackups, type BackupItem } from "@/lib/backups";
import { runResults, type TestRun } from "@/lib/failover";
import {
  duration,
  recovery,
  sandboxStates,
  useCleanupSandbox,
  useVerifyNode,
  type BackupSandbox,
  type BackupVerification,
} from "@/lib/verify";

const linkClass = "text-brand-600 hover:underline dark:text-brand-500";
const dateFmt = new Intl.DateTimeFormat("nl-BE", { day: "2-digit", month: "2-digit", year: "numeric" });
const timeFmt = new Intl.DateTimeFormat("nl-BE", { hour: "2-digit", minute: "2-digit" });
const dlClass = "grid gap-x-4 gap-y-2.5 text-sm sm:grid-cols-[9rem_1fr]";
const dtClass = "text-slate-500";
const none = <span className="text-slate-400">–</span>;

// deepProblem is de reden dat de diepe controle van een run niet liep,
// als ClusterForge daar stappen voor kan geven.
export function deepProblem(run: TestRun | undefined) {
  return run?.checks.find((c) => c.code === "agent_too_old" || c.code === "exec_forbidden")?.code as
    | "agent_too_old"
    | "exec_forbidden"
    | undefined;
}

function origin() {
  return typeof window === "undefined" ? "https://clusterforge.example" : window.location.origin;
}

// AgentSteps zijn de drie stappen als de back-up geen cf-agent met verify
// heeft: de agent bijwerken, de golden image opnieuw bouwen en een nieuwe
// back-up laten maken.
export function AgentSteps() {
  const o = origin();
  return (
    <ol className="list-decimal space-y-3 pl-5 text-sm">
      <li className="space-y-1.5">
        <p>Werk cf-agent bij op de node zelf. De aanmelding blijft staan.</p>
        <CopyBlock text={upgradeCommand()} />
      </li>
      <li className="space-y-1.5">
        <p>
          Bouw de golden image opnieuw, zodat nieuwe VM&apos;s de nieuwe agent ook hebben. Draai dit als root op de
          Proxmox-host met de image, met dezelfde <code>--vmid</code> en <code>--storage</code> als de eerste keer.
        </p>
        <CopyBlock text={`curl -fsSL ${o}/install/golden-image.sh | bash -s -- --server ${o} --storage local-lvm --replace`} />
      </li>
      <li>
        <p>
          Laat Proxmox een nieuwe back-up maken: wacht op de back-upjob, of kies in Proxmox bij de VM onder Backup voor
          Backup now. Klik daarna op de pagina Back-ups op Nu verversen en controleer opnieuw.
        </p>
      </li>
    </ol>
  );
}

// ExecRights zegt welk recht het token mist om cf-agent verify in de
// sandbox te starten.
export function ExecRights() {
  return (
    <div className="space-y-1.5 text-sm">
      <p>
        Op Proxmox VE 9 mag het token alleen iets starten via de guest agent met <code>VM.GuestAgent.Unrestricted</code>.
        Geef dat recht alleen op de sandbox-pool, als root op een van je Proxmox-hosts:
      </p>
      <CopyBlock
        text={`pveum role add ClusterForgeSandbox --privs "VM.GuestAgent.Unrestricted"\npveum acl modify /pool/cf-sandbox --users clusterforge@pve --roles ClusterForgeSandbox`}
      />
      <p className="text-slate-600 dark:text-slate-400">
        Op Proxmox VE 8 valt dit onder <code>VM.Monitor</code> in de rol ClusterForge, zoals in de README.
      </p>
    </div>
  );
}

// DeepHelp legt uit waarom de diepe controle niet liep en wat je eraan doet.
export function DeepHelp({ run, compact = false }: { run: TestRun | undefined; compact?: boolean }) {
  const problem = deepProblem(run);
  if (!problem) return null;
  return (
    <div className="space-y-3 rounded border border-amber-200 bg-amber-50 px-3 py-3 text-amber-950 dark:border-amber-900 dark:bg-amber-950 dark:text-amber-100">
      <p className="text-sm">
        {problem === "agent_too_old" ? (
          <>
            <span className="font-medium">Geen diepe controle{compact ? " bij de laatste controle" : ""}.</span> De cf-agent in
            deze back-up kent <code>verify</code> nog niet, dus alleen terugzetten, opstarten en de guest agent zijn
            gecontroleerd. Services, poorten en databases controleren lukt vanaf een back-up met de nieuwe agent:
          </>
        ) : (
          <>
            <span className="font-medium">Geen diepe controle{compact ? " bij de laatste controle" : ""}.</span> Het API-token
            mag in de sandbox niets starten via de guest agent.
          </>
        )}
      </p>
      {problem === "agent_too_old" ? <AgentSteps /> : <ExecRights />}
    </div>
  );
}

// verifiable zegt of de back-up van een regel te controleren is: een VM met
// een node, geen container.
export function verifiable(it: BackupItem) {
  return !!it.node && it.guest_type === "qemu";
}

// runningFor is de lopende controle van een node, als die er is.
export function runningFor(runs: TestRun[] | undefined, nodeId: string | undefined) {
  return nodeId ? runs?.find((r) => !r.result && r.node_id === nodeId) : undefined;
}

// LastVerification is de cel "Laatste controle": uitkomst, datum met een
// link naar het rapport en de hersteltijd.
export function LastVerification({
  it,
  running,
  isAdmin,
}: {
  it: BackupItem;
  running?: TestRun;
  isAdmin: boolean;
}) {
  const [open, setOpen] = useState(false);
  const v = it.last_verification;
  let body: ReactNode;
  if (running) {
    body = (
      <span className="inline-flex flex-wrap items-center gap-2">
        <RunBadge run={running} />
        <Link href={reportHref(running.id)} className={linkClass}>
          Rapport volgen
        </Link>
      </span>
    );
  } else if (v) {
    body = <VerificationLine v={v} />;
  } else if (it.guest_type === "lxc") {
    body = <span className="text-xs text-slate-500">Container: alleen versheid</span>;
  } else {
    body = <span className="text-slate-500">Nog niet gecontroleerd</span>;
  }
  return (
    <div className="space-y-1">
      {body}
      {isAdmin && verifiable(it) && it.latest && !running && (
        <div>
          <Button variant="ghost" className="-ml-2 px-2 py-1 text-xs" onClick={() => setOpen(true)}>
            Nu controleren
          </Button>
        </div>
      )}
      {open && it.node && <VerifyDialog nodeId={it.node.id} name={it.node.name} onClose={() => setOpen(false)} />}
    </div>
  );
}

function VerificationLine({ v }: { v: BackupVerification }) {
  const r = runResults[v.result];
  return (
    <span className="inline-flex flex-wrap items-center gap-x-2 gap-y-1">
      <span title={v.summary}>
        <Badge tone={r.tone}>{r.label}</Badge>
      </span>
      <Link href={reportHref(v.run_id)} className={cx("tabular-nums", linkClass)}>
        {v.finished_at ? dateFmt.format(new Date(v.finished_at)) : "rapport"}
      </Link>
      {v.recovery_seconds !== null && (
        <span className="text-xs text-slate-500">hersteltijd {duration(v.recovery_seconds)}</span>
      )}
    </span>
  );
}

// VerifyDialog vraagt welke back-up, standaard de nieuwste, en start de
// controle. Daarna gaat het scherm naar het rapport.
export function VerifyDialog({ nodeId, name, onClose }: { nodeId: string; name: string; onClose: () => void }) {
  const backups = useNodeBackups(nodeId);
  const verify = useVerifyNode();
  const router = useRouter();
  const list = backups.data?.backups ?? [];
  const [volid, setVolid] = useState("");
  const chosen = list.find((b) => b.volid === volid) ?? list[0];
  return (
    <Modal title={`Back-up controleren: ${name}`} onClose={onClose}>
      <div className="space-y-4 text-sm">
        <p>
          ClusterForge zet de back-up terug als tijdelijke VM in pool cf-sandbox, met elke netwerkkaart losgekoppeld. Het
          start de VM, controleert via de guest agent en met cf-agent verify of de services, poorten en databases
          werken, en verwijdert hem daarna altijd. De VM van {name} zelf blijft onaangeroerd.
        </p>
        {list.length === 0 ? (
          <p className="text-slate-500">{backups.isLoading ? "Back-ups laden…" : "Er is geen back-up om te controleren."}</p>
        ) : (
          <div>
            <label htmlFor="verify-volid" className="mb-1 block font-medium text-slate-700 dark:text-slate-300">
              Back-up
            </label>
            <Select id="verify-volid" value={chosen?.volid ?? ""} onChange={(e) => setVolid(e.target.value)}>
              {list.map((b, i) => (
                <option key={b.volid} value={b.volid}>
                  {backupTimeFmt.format(new Date(b.time))} · {b.storage} · {formatBytes(b.size)}
                  {i === 0 ? " (nieuwste)" : ""}
                </option>
              ))}
            </Select>
            {chosen && <p className="mt-1 font-mono text-xs break-all text-slate-500">{chosen.volid}</p>}
          </div>
        )}
        {verify.error && <Alert>{verify.error.message}</Alert>}
        <div className="flex justify-end gap-2">
          <Button variant="secondary" onClick={onClose}>
            Annuleren
          </Button>
          <Button
            disabled={verify.isPending || !chosen}
            onClick={async () => {
              const run = await verify
                .mutateAsync({ nodeId, volid: chosen === list[0] ? undefined : chosen?.volid })
                .catch(() => null);
              if (run) router.push(reportHref(run.id));
            }}
          >
            {verify.isPending ? "Starten…" : "Terugzetten en controleren"}
          </Button>
        </div>
      </div>
    </Modal>
  );
}

// SandboxesCard toont de tijdelijke VM's die nog kunnen bestaan; alleen
// zichtbaar als er een is.
export function SandboxesCard({ sandboxes, isAdmin }: { sandboxes: BackupSandbox[]; isAdmin: boolean }) {
  if (sandboxes.length === 0) return null;
  return (
    <Card title="Sandboxes">
      <p className="mb-3 text-sm text-slate-600 dark:text-slate-400">
        Tijdelijke VM&apos;s van back-upcontroles in pool cf-sandbox. Na elke controle verwijdert ClusterForge de sandbox; een
        mislukte verwijdering probeert de opruimer elke 5 minuten opnieuw.
      </p>
      <div className="overflow-x-auto">
        <table className={tableClass}>
          <thead className="border-b border-slate-200 dark:border-slate-800">
            <tr>
              <th className={thClass}>VMID</th>
              <th className={thClass}>Proxmox</th>
              <th className={thClass}>Back-up van</th>
              <th className={thClass}>Stand</th>
              <th className={thClass}>Leeftijd</th>
              <th className={thClass} />
            </tr>
          </thead>
          <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
            {sandboxes.map((s) => (
              <SandboxRow key={s.id} s={s} isAdmin={isAdmin} />
            ))}
          </tbody>
        </table>
      </div>
    </Card>
  );
}

function SandboxRow({ s, isAdmin }: { s: BackupSandbox; isAdmin: boolean }) {
  const cleanup = useCleanupSandbox();
  const st = sandboxStates[s.state];
  return (
    <tr>
      <td className={`${tdClass} tabular-nums`}>{s.vmid}</td>
      <td className={tdClass}>
        {s.connection_name}
        <div className="text-xs text-slate-500">
          {s.host || "host onbekend"}
          {s.storage && ` · ${s.storage}`}
        </div>
      </td>
      <td className={tdClass}>
        {s.source ? (
          <Link href={`/nodes/detail?id=${s.source.id}`} className={linkClass}>
            {s.source.name}
          </Link>
        ) : (
          `VM ${s.source_vmid}`
        )}
      </td>
      <td className={tdClass}>
        <span className="inline-flex flex-wrap items-center gap-2">
          <Badge tone={st.tone}>{st.label}</Badge>
          {s.running && <span className="text-xs text-slate-500">controle loopt</span>}
        </span>
        {s.error && <div className="mt-1 max-w-xs text-xs break-words text-red-700 dark:text-red-300">{s.error}</div>}
        {cleanup.error && <div className="mt-1 max-w-xs text-xs text-red-700 dark:text-red-300">{cleanup.error.message}</div>}
      </td>
      <td className={`${tdClass} whitespace-nowrap`}>{ago(s.created_at).replace(" geleden", "")}</td>
      <td className={`${tdClass} text-right whitespace-nowrap`}>
        <span className="inline-flex items-center gap-2">
          {s.run_id && (
            <Link href={reportHref(s.run_id)} className={`text-xs ${linkClass}`}>
              Rapport
            </Link>
          )}
          {isAdmin && !s.running && (
            <Button variant="secondary" className="px-2 py-1 text-xs" disabled={cleanup.isPending} onClick={() => cleanup.mutate(s.id)}>
              {cleanup.isPending ? "Opruimen…" : "Opruimen"}
            </Button>
          )}
        </span>
      </td>
    </tr>
  );
}

// VerifyHistory is de geschiedenis van de back-upcontroles.
export function VerifyHistory({ runs, showNode = true }: { runs: TestRun[]; showNode?: boolean }) {
  if (runs.length === 0) return <p className="text-sm text-slate-500">Nog geen back-up gecontroleerd.</p>;
  return (
    <div className="overflow-x-auto">
      <table className={tableClass}>
        <thead className="border-b border-slate-200 dark:border-slate-800">
          <tr>
            <th className={thClass}>Gestart</th>
            {showNode && <th className={thClass}>Node</th>}
            <th className={thClass}>Uitkomst</th>
            <th className={thClass}>Hersteltijd</th>
            <th className={thClass}>Samenvatting</th>
          </tr>
        </thead>
        <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
          {runs.map((r) => {
            const rec = recovery(r.backup);
            return (
              <tr key={r.id}>
                <td className={`${tdClass} whitespace-nowrap tabular-nums`}>
                  <Link href={reportHref(r.id)} className={linkClass}>
                    {backupTimeFmt.format(new Date(r.created_at))}
                  </Link>
                </td>
                {showNode && (
                  <td className={tdClass}>
                    {r.node_id ? (
                      <Link href={`/nodes/detail?id=${r.node_id}`} className="hover:underline">
                        {r.hostname}
                      </Link>
                    ) : (
                      r.hostname
                    )}
                  </td>
                )}
                <td className={tdClass}>
                  <RunBadge run={r} />
                </td>
                <td className={`${tdClass} whitespace-nowrap tabular-nums`}>{rec !== null ? duration(rec) : none}</td>
                <td className={`${tdClass} max-w-md text-slate-600 dark:text-slate-400`}>
                  {r.summary || (r.job_status === "queued" ? "Wacht op zijn beurt." : "Loopt…")}
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}

const bigTones: Record<string, string> = {
  green: "bg-emerald-100 text-emerald-800 dark:bg-emerald-950 dark:text-emerald-300",
  amber: "bg-amber-100 text-amber-800 dark:bg-amber-950 dark:text-amber-300",
  red: "bg-red-100 text-red-800 dark:bg-red-950 dark:text-red-300",
  slate: "bg-slate-100 text-slate-700 dark:bg-slate-800 dark:text-slate-300",
  blue: "bg-brand-50 text-brand-700 dark:bg-slate-800 dark:text-brand-500",
};

function BigResult({ run }: { run: TestRun }) {
  const r = run.result ? runResults[run.result] : { label: run.job_status === "queued" ? "WACHT" : "LOOPT", tone: "blue" };
  return (
    <span className={cx("inline-flex rounded-md px-4 py-2 text-lg font-semibold tracking-wide", bigTones[r.tone])}>{r.label}</span>
  );
}

const phases = [
  { key: "restore_seconds", label: "Terugzetten", color: "bg-brand-600" },
  { key: "boot_seconds", label: "Opstarten", color: "bg-violet-600" },
  { key: "check_seconds", label: "Controleren", color: "bg-emerald-500" },
  { key: "cleanup_seconds", label: "Opruimen", color: "bg-slate-400" },
] as const;

// BackupReport is het rapport van een back-upcontrole op de gedeelde
// rapportpagina.
export function BackupReport({ run, isAdmin }: { run: TestRun; isAdmin: boolean }) {
  const b = run.backup;
  if (!b) return null;
  const d = b.definition;
  const m = b.measurements;
  const rec = recovery(b);
  const known = phases.filter((p) => m[p.key] !== null);
  const cleanupFailed = run.checks.some((c) => c.name === "Opruimen" && !c.ok);
  const label = (p: (typeof phases)[number]) => (p.key === "cleanup_seconds" && cleanupFailed ? "Opruimen, niet gelukt" : p.label);
  const total = m.total_seconds ?? known.reduce((n, p) => n + (m[p.key] ?? 0), 0);
  return (
    <>
      <Card>
        <div className="flex flex-wrap items-start gap-4">
          <BigResult run={run} />
          <p
            className={cx(
              "min-w-56 flex-1 text-sm leading-6",
              run.result === "fail" || run.result === "error" ? "text-red-700 dark:text-red-300" : "",
            )}
          >
            {run.summary || (run.job_status === "queued" ? "De controle wacht op zijn beurt." : "De controle loopt…")}
          </p>
        </div>
        <dl className={cx(dlClass, "mt-5")}>
          <dt className={dtClass}>Node</dt>
          <dd>
            {run.node_id ? (
              <Link href={`/nodes/detail?id=${run.node_id}`} className={linkClass}>
                {run.hostname}
              </Link>
            ) : (
              run.hostname
            )}
            {run.cluster_name && <span className="text-slate-500"> in {run.cluster_name}</span>}
          </dd>
          <dt className={dtClass}>Back-up</dt>
          <dd>
            {d.volid ? (
              <>
                {backupTimeFmt.format(new Date(d.backup_time))} · {d.backup_storage} · {formatBytes(d.size)}
                <span className="text-slate-500">{d.chosen ? " (gekozen)" : " (de nieuwste)"}</span>
                <div className="font-mono text-xs break-all text-slate-500">{d.volid}</div>
              </>
            ) : (
              <span className="text-slate-500">nog niet gekozen</span>
            )}
          </dd>
          <dt className={dtClass}>Proxmox</dt>
          <dd>
            {d.connection_name || "–"}
            {d.source_vmid > 0 && (
              <span className="text-slate-500">
                {" "}
                · bron VM {d.source_vmid}
                {d.guest_name && ` (${d.guest_name})`}
              </span>
            )}
          </dd>
          {rec !== null && (
            <>
              <dt className={dtClass}>Hersteltijd</dt>
              <dd className="tabular-nums">
                {duration(rec)}{" "}
                <span className="text-slate-500">
                  (terugzetten {duration(m.restore_seconds)}, opstarten {duration(m.boot_seconds)})
                </span>
              </dd>
            </>
          )}
          {m.services_expected > 0 && (
            <>
              <dt className={dtClass}>Services</dt>
              <dd>
                {m.services_active} van {m.services_expected} actief
              </dd>
            </>
          )}
          {m.databases.length > 0 && (
            <>
              <dt className={dtClass}>Databases</dt>
              <dd>{m.databases.join(", ")}</dd>
            </>
          )}
          {m.agent_version && (
            <>
              <dt className={dtClass}>cf-agent</dt>
              <dd>
                {m.agent_version} <span className="text-slate-500">in de back-up</span>
              </dd>
            </>
          )}
          {run.finished_at && (
            <>
              <dt className={dtClass}>Klaar</dt>
              <dd>{backupTimeFmt.format(new Date(run.finished_at))}</dd>
            </>
          )}
        </dl>
      </Card>
      {(known.length > 0 || run.timeline.length > 0) && (
        <Card title="Tijdlijn">
          <div className="space-y-4">
            {known.length > 0 && total > 0 && (
              <>
                <div className="flex h-6 overflow-hidden rounded bg-slate-100 dark:bg-slate-800" aria-label="Duur per stap">
                  {known.map((p) => (
                    <div
                      key={p.key}
                      className={cx("h-full", p.color)}
                      style={{ width: `${((m[p.key] ?? 0) / total) * 100}%`, minWidth: "2px" }}
                      title={`${label(p)}: ${duration(m[p.key])}`}
                    />
                  ))}
                </div>
                <div className="flex flex-wrap gap-x-4 gap-y-1 text-xs text-slate-600 tabular-nums dark:text-slate-400">
                  {known.map((p) => (
                    <span key={p.key} className="inline-flex items-center gap-1.5">
                      <span aria-hidden className={cx("inline-block h-3 w-3 rounded-sm", p.color)} />
                      {label(p)} {duration(m[p.key])}
                    </span>
                  ))}
                  {m.total_seconds !== null && <span className="font-medium">Totaal {duration(m.total_seconds)}</span>}
                </div>
              </>
            )}
            {run.timeline.length > 0 && (
              <ol className="space-y-1 text-sm">
                {run.timeline.map((e, i) => (
                  <li key={i} className="grid grid-cols-[5.5rem_1fr] gap-2">
                    <span className="text-right text-slate-500 tabular-nums">+{duration(e.t_ms / 1000)}</span>
                    <span>{e.text}</span>
                  </li>
                ))}
              </ol>
            )}
          </div>
        </Card>
      )}
      {run.checks.length > 0 && (
        <Card title="Controles">
          <Checks checks={run.checks} />
          {deepProblem(run) && (
            <div className="mt-4">
              <DeepHelp run={run} />
            </div>
          )}
        </Card>
      )}
      <SandboxDetails run={run} isAdmin={isAdmin} />
    </>
  );
}

function SandboxDetails({ run, isAdmin }: { run: TestRun; isAdmin: boolean }) {
  const cleanup = useCleanupSandbox();
  const b = run.backup!;
  const m = b.measurements;
  const s = b.sandbox;
  const isolation = run.checks.find((c) => c.name === "Isolatie");
  if (!s && m.sandbox_vmid === 0) {
    if (!run.result) return null;
    return (
      <Card title="Sandbox">
        <p className="text-sm text-slate-500">Er is geen sandbox-VM gemaakt.</p>
      </Card>
    );
  }
  const st = s ? sandboxStates[s.state] : null;
  const destroyedAt = s?.destroyed_at ?? m.destroyed_at;
  const canClean = isAdmin && s && !s.running && (s.state === "present" || s.state === "destroy_failed" || s.state === "reserved");
  return (
    <Card
      title={
        <span className="flex flex-wrap items-center justify-between gap-2">
          <span>Sandbox</span>
          {canClean && (
            <Button variant="secondary" disabled={cleanup.isPending} onClick={() => cleanup.mutate(s.id)}>
              {cleanup.isPending ? "Opruimen…" : "Nu opruimen"}
            </Button>
          )}
        </span>
      }
    >
      {cleanup.error && (
        <div className="mb-3">
          <Alert>{cleanup.error.message}</Alert>
        </div>
      )}
      <dl className={dlClass}>
        <dt className={dtClass}>VMID</dt>
        <dd className="tabular-nums">{s?.vmid ?? m.sandbox_vmid}</dd>
        <dt className={dtClass}>Host</dt>
        <dd>{s?.host || m.host || "–"}</dd>
        <dt className={dtClass}>Storage</dt>
        <dd>{s?.storage || m.storage || "–"}</dd>
        <dt className={dtClass}>Isolatie</dt>
        <dd>{isolation ? isolation.detail : <span className="text-slate-500">nog niet geïsoleerd</span>}</dd>
        {st && (
          <>
            <dt className={dtClass}>Stand</dt>
            <dd className="flex flex-wrap items-center gap-2">
              <Badge tone={st.tone}>{st.label}</Badge>
              {s?.error && <span className="text-red-700 dark:text-red-300">{s.error}</span>}
            </dd>
          </>
        )}
        <dt className={dtClass}>Verwijderd om</dt>
        <dd>{destroyedAt ? timeFmt.format(new Date(destroyedAt)) : <span className="text-slate-500">nog niet</span>}</dd>
      </dl>
    </Card>
  );
}
