"use client";

import Link from "next/link";
import { useRouter } from "next/navigation";
import { useState, type FormEvent } from "react";
import { Modal } from "@/components/Modal";
import { Checks, reportHref, RunBadge } from "@/components/failover/Failover";
import { Empty, tableClass, tdClass, thClass } from "@/components/inventory/bits";
import { Alert, Badge, Button, Card, Field, Input, Select, cx } from "@/components/ui";
import { ApiError } from "@/lib/api/client";
import {
  duration,
  expectText,
  matchLabels,
  roleLabels,
  statusLabels,
  targetNames,
  useCreateDisasterScenario,
  useDeleteDisasterScenario,
  useDisasterPreview,
  useDisasterScenarios,
  useRunDisaster,
  useUpdateDisasterScenario,
  type DisasterClusterPlan,
  type DisasterKind,
  type DisasterOptions,
  type DisasterPrediction,
  type DisasterReport,
  type DisasterScenario,
  type DisasterScenarioInput,
} from "@/lib/disaster";
import { list, probeText, seconds, type TestRun } from "@/lib/failover";

const linkClass = "text-brand-600 hover:underline dark:text-brand-500";
const fmt = new Intl.DateTimeFormat("nl-BE", { dateStyle: "medium", timeStyle: "short" });

// DisasterCard toont de rampoefeningen van een cluster met hun laatste
// resultaat; een admin maakt, wijzigt en start ze hier.
export function DisasterCard({ clusterId, isAdmin }: { clusterId: string; isAdmin: boolean }) {
  const q = useDisasterScenarios(clusterId);
  const [form, setForm] = useState<DisasterScenario | "new" | null>(null);
  const [starting, setStarting] = useState<DisasterScenario | null>(null);
  if (!q.data) return null;
  const { items, options } = q.data;
  const canCreate = options.allowed && options.kinds.some((k) => k.available);
  return (
    <Card
      title={
        <span className="flex flex-wrap items-center justify-between gap-2">
          <span>Rampoefeningen</span>
          {isAdmin && options.allowed && (
            <Button
              variant="secondary"
              disabled={!canCreate}
              title={canCreate ? undefined : options.kinds.map((k) => k.reason).find(Boolean)}
              onClick={() => setForm("new")}
            >
              Rampoefening toevoegen
            </Button>
          )}
        </span>
      }
    >
      {!options.allowed ? (
        <Empty>
          {options.reason}.{" "}
          <Link href="/risico" className={linkClass}>
            Naar Risico
          </Link>
        </Empty>
      ) : items.length === 0 ? (
        <Empty>
          Nog geen rampoefeningen. Een rampoefening laat in lab of test echt iets uitvallen: een hele Proxmox-host, een dienst op
          alle nodes of het VIP. ClusterForge meet elk geraakt cluster, vergelijkt dat met de voorspelling van Wat als en zet
          daarna alles weer aan.
        </Empty>
      ) : (
        <ul className="divide-y divide-slate-100 dark:divide-slate-800">
          {items.map((s) => (
            <ScenarioRow key={s.id} s={s} isAdmin={isAdmin} onEdit={() => setForm(s)} onStart={() => setStarting(s)} />
          ))}
        </ul>
      )}
      {form && (
        <Modal title={form === "new" ? "Rampoefening toevoegen" : "Rampoefening bewerken"} onClose={() => setForm(null)} wide>
          <DisasterForm
            clusterId={clusterId}
            initial={form === "new" ? undefined : form}
            options={options}
            onDone={() => setForm(null)}
          />
        </Modal>
      )}
      {starting && <StartDialog s={starting} onClose={() => setStarting(null)} />}
    </Card>
  );
}

function ScenarioRow({
  s,
  isAdmin,
  onEdit,
  onStart,
}: {
  s: DisasterScenario;
  isAdmin: boolean;
  onEdit: () => void;
  onStart: () => void;
}) {
  const running = s.last_run && !s.last_run.result;
  return (
    <li className="py-3 first:pt-0 last:pb-0">
      <div className="grid gap-3 sm:grid-cols-[1fr_auto]">
        <div className="min-w-0 space-y-1 text-sm">
          <p className="font-medium">{s.name}</p>
          <p className="text-slate-600 dark:text-slate-400">
            {s.description}
            {s.host && <span className="text-slate-500"> ({s.host.connection})</span>}
          </p>
          <p className="text-xs text-slate-500">{expectText(s)}</p>
          <p className="flex flex-wrap items-center gap-2 text-xs">
            {s.last_run ? (
              <>
                <RunBadge run={s.last_run} />
                <Link href={reportHref(s.last_run.id)} className={linkClass}>
                  {running ? "Rapport volgen" : fmt.format(new Date(s.last_run.finished_at ?? s.last_run.created_at))}
                </Link>
                {s.last_run.result && <span className="text-slate-500">{s.last_run.summary}</span>}
              </>
            ) : (
              <span className="text-slate-500">Nog nooit geoefend.</span>
            )}
          </p>
        </div>
        {isAdmin && (
          <div className="flex flex-wrap items-start gap-2">
            <Button variant="secondary" onClick={onEdit}>
              Bewerken
            </Button>
            <Button disabled={!!running} onClick={onStart}>
              {running ? "Loopt…" : "Voorspellen en starten"}
            </Button>
          </div>
        )}
      </div>
    </li>
  );
}

// Prediction toont een status als badge met de reden erachter.
function Prediction({ p }: { p: DisasterPrediction }) {
  const s = statusLabels[p.status];
  return (
    <span>
      <Badge tone={s.tone}>{s.label}</Badge>
      {p.reason && <span className="ml-1.5 text-slate-600 dark:text-slate-400">{p.reason}</span>}
    </span>
  );
}

function ClusterLink({ c }: { c: Pick<DisasterClusterPlan, "id" | "name" | "environment"> }) {
  return (
    <>
      <Link href={`/clusters/detail?id=${c.id}`} className={linkClass}>
        {c.name}
      </Link>
      <span className="block text-xs text-slate-500">{c.environment}</span>
    </>
  );
}

// PredictionTable toont per cluster wat de wat-als voorspelt.
function PredictionTable({ clusters }: { clusters: DisasterClusterPlan[] }) {
  return (
    <div className="overflow-x-auto">
      <table className={tableClass}>
        <thead className="border-b border-slate-200 dark:border-slate-800">
          <tr>
            <th className={thClass}>Cluster</th>
            <th className={thClass}>Rol</th>
            <th className={thClass}>Voorspeld</th>
            <th className={thClass}>Gemeten via</th>
          </tr>
        </thead>
        <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
          {clusters.map((c) => (
            <tr key={c.id}>
              <td className={tdClass}>
                <ClusterLink c={c} />
              </td>
              <td className={`${tdClass} whitespace-nowrap`}>{roleLabels[c.role]}</td>
              <td className={tdClass}>
                <Prediction p={c.predicted} />
              </td>
              <td className={`${tdClass} text-xs text-slate-600 dark:text-slate-400`}>
                {c.vips.length > 0
                  ? c.vips.map((v) => (
                      <span key={v.address} className="block">
                        <span className="font-mono">{v.address}</span> · {probeText(v.probe)}
                      </span>
                    ))
                  : "de status van het cluster en zijn diensten"}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

// StartDialog doet de voorcontrole en de voorspelling, en start pas als de
// admin de slug van elk cluster dat uitvalt intikt.
function StartDialog({ s, onClose }: { s: DisasterScenario; onClose: () => void }) {
  const preview = useDisasterPreview(s.id, true);
  const run = useRunDisaster();
  const router = useRouter();
  const [typed, setTyped] = useState<Record<string, string>>({});
  const p = preview.data;
  const err = run.error instanceof ApiError ? run.error : null;
  const confirmed = !!p && p.confirm.every((slug) => (typed[slug] ?? "").trim() === slug);
  const failed = p?.checks.filter((c) => !c.ok).length ?? 0;
  return (
    <Modal title={`Rampoefening: ${s.name}`} onClose={onClose} wide>
      <div className="space-y-4 text-sm">
        {preview.isLoading && <p className="text-slate-500">Voorcontrole en voorspelling…</p>}
        {preview.error && <Alert>{preview.error.message}</Alert>}
        {p && (
          <>
            <p>
              {p.description}: {list(targetNames(p.targets)) || "niets"}. De storing duurt {duration(s.fault_seconds)}; daarna zet
              ClusterForge alles weer aan en verwacht het elk cluster binnen {duration(s.recovery_seconds)} weer gezond.
            </p>
            <div>
              <h3 className="mb-2 font-medium">Wat de wat-als voorspelt</h3>
              <PredictionTable clusters={p.clusters} />
            </div>
            {p.notes.length > 0 && (
              <Alert kind="info">
                <ul className="list-disc space-y-1 pl-4">
                  {p.notes.map((n) => (
                    <li key={n}>{n}</li>
                  ))}
                </ul>
              </Alert>
            )}
            {p.skipped.length > 0 && (
              <p className="text-slate-600 dark:text-slate-400">
                Blijven aan, want ze staan niet in ClusterForge: {list(p.skipped)}.
              </p>
            )}
            <details open={failed > 0} className="rounded-md border border-slate-200 p-3 dark:border-slate-800">
              <summary className="cursor-pointer font-medium">
                Voorcontrole{" "}
                <span className="font-normal text-slate-500">
                  {failed === 0
                    ? `alle ${p.checks.length} controles in orde`
                    : `${failed} van ${p.checks.length} controles mislukt`}
                </span>
              </summary>
              <div className="mt-3">
                <Checks checks={p.checks} />
              </div>
            </details>
            {p.ok && p.confirm.length > 0 && (
              <div className="space-y-2">
                <p>
                  Tik ter bevestiging de slug in van elk cluster dat uitvalt. Afbreken kan op de rapportpagina; ClusterForge zet
                  dan meteen alles weer aan.
                </p>
                <div className="grid gap-3 sm:grid-cols-2">
                  {p.confirm.map((slug) => (
                    <Field key={slug} label={`Tik ${slug} in`} htmlFor={`dr-confirm-${slug}`}>
                      <Input
                        id={`dr-confirm-${slug}`}
                        autoComplete="off"
                        value={typed[slug] ?? ""}
                        onChange={(e) => setTyped((t) => ({ ...t, [slug]: e.target.value }))}
                      />
                    </Field>
                  ))}
                </div>
              </div>
            )}
          </>
        )}
        {err?.code === "precheck_failed" && err.checks ? (
          <div className="space-y-2">
            <Alert>De rampoefening start niet, omdat de voorcontrole faalt.</Alert>
            <Checks checks={err.checks} />
          </div>
        ) : (
          run.error && <Alert>{run.error.message}</Alert>
        )}
        <div className="flex justify-end gap-2">
          <Button variant="secondary" onClick={onClose}>
            Annuleren
          </Button>
          <Button
            variant="danger"
            disabled={!p?.ok || !confirmed || run.isPending}
            onClick={async () => {
              const r = await run.mutateAsync({ id: s.id, confirm: p?.confirm ?? [] }).catch(() => null);
              if (r) router.push(reportHref(r.id));
            }}
          >
            {run.isPending ? "Voorcontrole…" : "Storing starten en meten"}
          </Button>
        </div>
      </div>
    </Modal>
  );
}

function DisasterForm({
  clusterId,
  initial,
  options,
  onDone,
}: {
  clusterId: string;
  initial?: DisasterScenario;
  options: DisasterOptions;
  onDone: () => void;
}) {
  const create = useCreateDisasterScenario(clusterId);
  const update = useUpdateDisasterScenario();
  const remove = useDeleteDisasterScenario();
  const firstAvailable = options.kinds.find((k) => k.available) ?? options.kinds[0];
  const [kind, setKind] = useState<DisasterKind>(initial?.kind ?? firstAvailable?.key ?? "service_down");
  const kindOpt = options.kinds.find((k) => k.key === kind);
  const hostKey = (connId: string, host: string) => `${connId}/${host}`;
  const firstHost = options.hosts[0];
  const [host, setHost] = useState(
    initial?.host
      ? hostKey(initial.host.connection_id, initial.host.host)
      : firstHost
        ? hostKey(firstHost.connection_id, firstHost.host)
        : "",
  );
  const [unit, setUnit] = useState(initial?.unit || options.units[0] || "nginx");
  const [fault, setFault] = useState(String(initial?.fault_seconds ?? kindOpt?.fault_seconds ?? 60));
  const [recovery, setRecovery] = useState(String(initial?.recovery_seconds ?? kindOpt?.recovery_seconds ?? 120));
  const [touched, setTouched] = useState(!!initial);
  const [name, setName] = useState(initial?.name ?? "");
  const [nameTouched, setNameTouched] = useState(!!initial);
  const [error, setError] = useState<{ message: string; field?: string } | null>(null);
  const busy = create.isPending || update.isPending || remove.isPending;
  const hostOpt = options.hosts.find((h) => hostKey(h.connection_id, h.host) === host);

  const defaultName = () => {
    if (kind === "host_down") return `${hostOpt?.host ?? "Proxmox-host"} valt uit`;
    if (kind === "service_down") return `${unit} valt weg`;
    return "VIP valt weg";
  };
  const shownName = nameTouched ? name : defaultName();

  function pickKind(key: DisasterKind) {
    setKind(key);
    const opt = options.kinds.find((k) => k.key === key);
    if (!touched && opt) {
      setFault(String(opt.fault_seconds));
      setRecovery(String(opt.recovery_seconds));
    }
  }

  async function submit(e: FormEvent) {
    e.preventDefault();
    setError(null);
    const body: DisasterScenarioInput = {
      name: shownName.trim(),
      kind,
      fault_seconds: Number(fault),
      recovery_seconds: Number(recovery),
      ...(kind === "host_down" && hostOpt ? { connection_id: hostOpt.connection_id, host: hostOpt.host } : {}),
      ...(kind === "service_down" ? { unit } : {}),
    };
    try {
      if (initial) await update.mutateAsync({ id: initial.id, body });
      else await create.mutateAsync(body);
      onDone();
    } catch (err) {
      setError({
        message: err instanceof Error ? err.message : "Opslaan mislukt",
        field: err instanceof ApiError ? err.field : undefined,
      });
    }
  }

  const fieldError = (f: string) => (error?.field === f ? <p className="mt-1 text-xs text-red-600">{error.message}</p> : null);

  return (
    <form onSubmit={submit} className="space-y-4">
      {error && !error.field && <Alert>{error.message}</Alert>}
      <fieldset>
        <legend className="mb-1 text-sm font-medium text-slate-700 dark:text-slate-300">Wat valt uit</legend>
        <div className="space-y-2">
          {options.kinds.map((k) => {
            const disabled = !k.available && k.key !== initial?.kind;
            return (
              <label
                key={k.key}
                className={cx(
                  "flex gap-3 rounded-md border p-3 text-sm",
                  kind === k.key ? "border-brand-500 bg-brand-50/50 dark:bg-slate-800" : "border-slate-200 dark:border-slate-800",
                  disabled ? "cursor-not-allowed opacity-60" : "cursor-pointer",
                )}
              >
                <input
                  type="radio"
                  name="kind"
                  className="mt-0.5"
                  value={k.key}
                  checked={kind === k.key}
                  disabled={disabled}
                  onChange={() => pickKind(k.key)}
                />
                <span>
                  <span className="font-medium">{k.label}</span>
                  <span className="block text-xs text-slate-500">
                    {k.available ? kindHint[k.key] : `Kan nu niet: ${k.reason}`}
                  </span>
                </span>
              </label>
            );
          })}
        </div>
        {fieldError("kind")}
      </fieldset>

      <div className="grid gap-4 sm:grid-cols-2">
        {kind === "host_down" && (
          <Field label="Proxmox-host" htmlFor="dr-host">
            <Select id="dr-host" required value={host} onChange={(e) => setHost(e.target.value)}>
              {options.hosts.length === 0 && <option value="">Geen host met een node van dit cluster</option>}
              {options.hosts.map((h) => (
                <option key={hostKey(h.connection_id, h.host)} value={hostKey(h.connection_id, h.host)}>
                  {h.host} ({h.connection})
                </option>
              ))}
            </Select>
            {hostOpt && (
              <p className="mt-1 text-xs text-slate-500">
                Gaat hard uit: {list([...hostOpt.nodes, ...hostOpt.others])}.
                {hostOpt.others.length > 0 && " Die van andere clusters vallen dus mee uit."}
              </p>
            )}
            {fieldError("host")}
          </Field>
        )}
        {kind === "service_down" && (
          <Field label="Dienst" htmlFor="dr-unit">
            <Select id="dr-unit" value={unit} onChange={(e) => setUnit(e.target.value)}>
              {(options.units.length ? options.units : [unit]).map((u) => (
                <option key={u} value={u}>
                  {u}
                </option>
              ))}
            </Select>
            {fieldError("unit")}
          </Field>
        )}
        <Field
          label="Duur van de storing in seconden"
          htmlFor="dr-fault"
          hint={
            kind === "host_down"
              ? `Minstens ${kindOpt?.min_fault_seconds ?? 100} s: na een harde stop is een node pas na 90 s zonder heartbeat down.`
              : `Tussen ${kindOpt?.min_fault_seconds ?? 20} en 600 s.`
          }
        >
          <Input
            id="dr-fault"
            type="number"
            min={kindOpt?.min_fault_seconds ?? 20}
            max={600}
            required
            value={fault}
            onChange={(e) => {
              setFault(e.target.value);
              setTouched(true);
            }}
          />
          {fieldError("fault_seconds")}
        </Field>
        <Field
          label="Weer gezond binnen, in seconden"
          htmlFor="dr-recovery"
          hint="Na het terugzetten. Duurt het langer, dan is de uitslag een waarschuwing."
        >
          <Input
            id="dr-recovery"
            type="number"
            min={10}
            max={1800}
            required
            value={recovery}
            onChange={(e) => {
              setRecovery(e.target.value);
              setTouched(true);
            }}
          />
          {fieldError("recovery_seconds")}
        </Field>
        <Field label="Naam" htmlFor="dr-name">
          <Input
            id="dr-name"
            required
            maxLength={200}
            value={shownName}
            onChange={(e) => {
              setName(e.target.value);
              setNameTouched(true);
            }}
          />
          {fieldError("name")}
        </Field>
      </div>

      <div className="flex flex-wrap justify-between gap-2">
        <div className="flex gap-2">
          <Button type="submit" disabled={busy}>
            {busy ? "Opslaan…" : initial ? "Opslaan" : "Toevoegen"}
          </Button>
          <Button type="button" variant="secondary" onClick={onDone}>
            Annuleren
          </Button>
        </div>
        {initial && (
          <Button
            type="button"
            variant="secondary-danger"
            disabled={busy}
            onClick={async () => {
              if (!window.confirm(`Rampoefening ${initial.name} verwijderen? De rapporten van eerdere runs blijven bewaard.`))
                return;
              try {
                await remove.mutateAsync(initial.id);
                onDone();
              } catch (err) {
                setError({ message: err instanceof Error ? err.message : "Verwijderen mislukt" });
              }
            }}
          >
            Verwijderen
          </Button>
        )}
      </div>
    </form>
  );
}

const kindHint: Record<DisasterKind, string> = {
  host_down:
    "Alle gekoppelde VM's op één Proxmox-host gaan tegelijk hard uit, zoals bij een stroomonderbreking. De host zelf blijft aan.",
  service_down: "Eén dienst stopt op alle nodes van dit cluster tegelijk.",
  vip_loss: "keepalived stopt op alle nodes, zodat geen node het VIP heeft.",
};

// DisasterReportView is het rapport van een rampoefening: voorspeld tegen
// gemeten per cluster, de probe per VIP en de tijdlijn.
export function DisasterReportView({ run, d }: { run: TestRun; d: DisasterReport }) {
  return (
    <>
      <Card>
        <dl className="grid gap-x-4 gap-y-2.5 text-sm sm:grid-cols-[9rem_1fr]">
          <dt className="text-slate-500">Storing</dt>
          <dd>
            {d.description}: {list(targetNames(d.targets)) || "niets"}
            {d.skipped.length > 0 && <span className="block text-xs text-slate-500">Bleven aan: {list(d.skipped)}</span>}
          </dd>
          <dt className="text-slate-500">Verwacht</dt>
          <dd>
            {expectText({
              fault_seconds: Math.round(d.fault_ms / 1000),
              recovery_seconds: Math.round(d.expect_recovery_ms / 1000),
            })}
          </dd>
          <dt className="text-slate-500">Resultaat</dt>
          <dd className={cx(run.result === "fail" || run.result === "error" ? "text-red-700 dark:text-red-300" : "")}>
            {run.summary || (run.job_status === "queued" ? "De rampoefening wacht op haar beurt." : "De rampoefening loopt…")}
          </dd>
          {d.recovery_ms !== null && (
            <>
              <dt className="text-slate-500">Weer gezond na</dt>
              <dd className="tabular-nums">
                {offset(d.recovery_ms)} na het terugzetten
                {d.total_ms !== null && <span className="text-slate-500"> ({offset(d.total_ms)} na de storing)</span>}
              </dd>
            </>
          )}
          {d.target_results.some((t) => t.error) && (
            <>
              <dt className="text-slate-500">Niet gelukt</dt>
              <dd className="text-red-700 dark:text-red-300">
                {d.target_results
                  .filter((t) => t.error)
                  .map((t) => `${t.hostname}: ${t.error}`)
                  .join("; ")}
              </dd>
            </>
          )}
          {run.finished_at && (
            <>
              <dt className="text-slate-500">Klaar</dt>
              <dd>{fmt.format(new Date(run.finished_at))}</dd>
            </>
          )}
        </dl>
      </Card>
      <Card title="Voorspeld tegen gemeten">
        <ComparisonTable d={d} />
        {d.notes.length > 0 && (
          <ul className="mt-4 list-disc space-y-1 pl-5 text-xs text-slate-500">
            {d.notes.map((n) => (
              <li key={n}>{n}</li>
            ))}
          </ul>
        )}
      </Card>
      <DisasterTimeline run={run} d={d} />
    </>
  );
}

// recoveredText zegt wanneer een cluster weer gezond was.
function recoveredText(c: DisasterReport["clusters"][number]) {
  if (c.recovered_ms !== null) return `na ${offset(c.recovered_ms)}`;
  return c.match === "not_recovered" ? "niet" : "—";
}

// ComparisonList is de vergelijking op een smal scherm, een blok per cluster.
function ComparisonList({ d }: { d: DisasterReport }) {
  return (
    <ul className="divide-y divide-slate-100 text-sm sm:hidden dark:divide-slate-800">
      {d.clusters.map((c) => {
        const m = matchLabels[c.match];
        return (
          <li key={c.id} className="space-y-1.5 py-3 first:pt-0 last:pb-0">
            <div className="flex flex-wrap items-baseline justify-between gap-2">
              <span>
                <Link href={`/clusters/detail?id=${c.id}`} className={linkClass}>
                  {c.name}
                </Link>
                <span className="text-xs text-slate-500">
                  {" "}
                  · {c.environment} · {roleLabels[c.role]}
                </span>
              </span>
              <Badge tone={m.tone}>{m.label}</Badge>
            </div>
            <p>
              <span className="text-slate-500">Voorspeld </span>
              <Prediction p={c.predicted} />
            </p>
            <p>
              <span className="text-slate-500">Gemeten </span>
              {c.match === "unknown" && c.measured.status === "unknown" ? (
                <span className="text-slate-400">nog niet gemeten</span>
              ) : (
                <Prediction p={c.measured} />
              )}
            </p>
            <p className={cx("tabular-nums", c.slow && "text-amber-700 dark:text-amber-400")}>
              <span className="text-slate-500">Weer gezond </span>
              {recoveredText(c)}
              {c.slow && " (trager dan verwacht)"}
            </p>
          </li>
        );
      })}
    </ul>
  );
}

function ComparisonTable({ d }: { d: DisasterReport }) {
  return (
    <>
      <ComparisonList d={d} />
      <div className="hidden overflow-x-auto sm:block">
        <table className={tableClass}>
          <thead className="border-b border-slate-200 dark:border-slate-800">
            <tr>
              <th className={thClass}>Cluster</th>
              <th className={thClass}>Rol</th>
              <th className={thClass}>Voorspeld</th>
              <th className={thClass}>Gemeten</th>
              <th className={thClass}>Uitkomst</th>
              <th className={thClass}>Weer gezond</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
            {d.clusters.map((c) => {
              const m = matchLabels[c.match];
              return (
                <tr key={c.id}>
                  <td className={tdClass}>
                    <ClusterLink c={c} />
                  </td>
                  <td className={`${tdClass} whitespace-nowrap`}>{roleLabels[c.role]}</td>
                  <td className={tdClass}>
                    <Prediction p={c.predicted} />
                  </td>
                  <td className={tdClass}>
                    {c.match === "unknown" && c.measured.status === "unknown" ? (
                      <span className="text-slate-400">nog niet gemeten</span>
                    ) : (
                      <Prediction p={c.measured} />
                    )}
                  </td>
                  <td className={tdClass}>
                    <Badge tone={m.tone}>{m.label}</Badge>
                  </td>
                  <td className={cx(tdClass, "whitespace-nowrap tabular-nums", c.slow && "text-amber-700 dark:text-amber-400")}>
                    {recoveredText(c)}
                    {c.slow && <span className="block text-xs">trager dan verwacht</span>}
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
    </>
  );
}

// offset toont een tijd in de rampoefening tot op de seconde, ook boven de
// twee minuten: "2 min 31 s" in plaats van "3 min".
function offset(ms: number) {
  if (ms < 120_000) return seconds(ms);
  const s = Math.round(ms / 1000);
  return s % 60 ? `${Math.floor(s / 60)} min ${s % 60} s` : `${s / 60} min`;
}

const markers: Partial<Record<TestRun["timeline"][number]["kind"], { color: string; label: string }>> = {
  fault: { color: "bg-red-600", label: "Storing" },
  clear: { color: "bg-slate-500", label: "Terugzetten" },
  ready: { color: "bg-slate-400", label: "Nodes klaar" },
  return: { color: "bg-violet-600", label: "Cluster weer gezond" },
};

// DisasterTimeline toont per VIP een balk, groen of rood, met de storing,
// het terugzetten en het herstel erop, en daaronder elke gebeurtenis.
function DisasterTimeline({ run, d }: { run: TestRun; d: DisasterReport }) {
  const events = run.timeline;
  if (d.vips.length === 0 && events.length === 0) return null;
  const end = Math.max(d.end_ms, ...d.vips.flatMap((v) => v.segments.map((s) => s.to_ms)), ...events.map((e) => e.t_ms), 1);
  const pct = (ms: number) => `${Math.min(100, Math.max(0, (ms / end) * 100))}%`;
  const names = Object.fromEntries(d.clusters.map((c) => [c.id, c.name]));
  const lines = events.filter((e) => markers[e.kind]);
  return (
    <Card title="Tijdlijn">
      <div className="space-y-4">
        {d.vips.map((v) => (
          <div key={`${v.cluster_id}-${v.address}`}>
            <p className="mb-1 text-xs text-slate-600 dark:text-slate-400">
              {names[v.cluster_id]} · <span className="font-mono">{v.address}</span> · {probeText(v.probe)}
            </p>
            <div className="relative">
              <div
                className="relative h-5 overflow-hidden rounded bg-slate-100 dark:bg-slate-800"
                aria-label={`Probe op ${v.address}`}
              >
                {v.segments.map((s, i) => (
                  <div
                    key={i}
                    className={cx("absolute inset-y-0", s.ok ? "bg-emerald-500" : "bg-red-500")}
                    style={{ left: pct(s.from_ms), width: `calc(${pct(s.to_ms - s.from_ms)} + 1px)` }}
                    title={`${s.ok ? "Bereikbaar" : "Onbereikbaar"} van ${seconds(s.from_ms)} tot ${seconds(s.to_ms)}`}
                  />
                ))}
              </div>
              {lines.map((e, i) => (
                <div
                  key={i}
                  className={cx("absolute -top-1 -bottom-1 w-0.5", markers[e.kind]!.color)}
                  style={{ left: pct(e.t_ms) }}
                  title={`${seconds(e.t_ms)}: ${e.text}`}
                />
              ))}
            </div>
          </div>
        ))}
        {d.vips.length > 0 && (
          <div className="flex justify-between text-xs text-slate-500 tabular-nums">
            <span>0 s</span>
            <span>{seconds(end)}</span>
          </div>
        )}
        <div className="flex flex-wrap gap-x-4 gap-y-1 text-xs text-slate-600 dark:text-slate-400">
          {d.vips.length > 0 && (
            <>
              <Legend color="bg-emerald-500" label="VIP bereikbaar" />
              <Legend color="bg-red-500" label="VIP onbereikbaar" />
            </>
          )}
          {Object.values(markers)
            .filter((x) => x && lines.some((e) => markers[e.kind] === x))
            .map((x) => (
              <Legend key={x!.label} color={x!.color} label={x!.label} thin />
            ))}
        </div>
        <ol className="space-y-1 text-sm">
          {events.map((e, i) => (
            <li key={i} className="grid grid-cols-[5.5rem_1fr] gap-2">
              <span className="text-right text-slate-500 tabular-nums">+{offset(e.t_ms)}</span>
              <span>{e.text}</span>
            </li>
          ))}
        </ol>
      </div>
    </Card>
  );
}

function Legend({ color, label, thin = false }: { color: string; label: string; thin?: boolean }) {
  return (
    <span className="inline-flex items-center gap-1.5">
      <span aria-hidden className={cx("inline-block h-3", thin ? "w-0.5" : "w-3 rounded-sm", color)} />
      {label}
    </span>
  );
}
