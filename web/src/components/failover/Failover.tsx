"use client";

import Link from "next/link";
import { useRouter } from "next/navigation";
import { useState, type FormEvent } from "react";
import { Modal } from "@/components/Modal";
import { Empty } from "@/components/inventory/bits";
import { Alert, Badge, Button, Card, Field, Input, Label, Select, cx } from "@/components/ui";
import { ApiError } from "@/lib/api/client";
import {
  confirmText,
  probeText,
  runResults,
  unitOf,
  useCreateFailoverTest,
  useDeleteFailoverTest,
  useFailoverRuns,
  useFailoverTests,
  useRestoreTestRun,
  useStartFailoverTest,
  useUpdateFailoverTest,
  type FailoverOptions,
  type FailoverProbe,
  type FailoverScenario,
  type FailoverTest,
  type FailoverTestInput,
  type TestRun,
  type TestRunCheck,
} from "@/lib/failover";

const linkClass = "text-brand-600 hover:underline dark:text-brand-500";
const fmt = new Intl.DateTimeFormat("nl-BE", { dateStyle: "medium", timeStyle: "short" });

export function reportHref(runId: string) {
  return `/tests/detail?id=${runId}`;
}

// RunBadge toont het resultaat van een run, of dat hij nog loopt.
export function RunBadge({ run }: { run: Pick<TestRun, "result" | "job_status"> }) {
  if (!run.result) return <Badge tone="blue">{run.job_status === "queued" ? "Wacht" : "Loopt"}</Badge>;
  const r = runResults[run.result];
  return <Badge tone={r.tone}>{r.label}</Badge>;
}

// expectation beschrijft wat de test verwacht, in gewone taal.
export function expectation(t: { max_takeover_seconds: number; expect_failback: boolean }) {
  return `Overname binnen ${t.max_takeover_seconds} s${t.expect_failback ? ", daarna terug naar de oorspronkelijke node" : ""}`;
}

// FailoverBanner staat bovenaan het cluster zolang een failovertest niet
// volledig hersteld is.
export function FailoverBanner({ clusterId, isAdmin }: { clusterId: string; isAdmin: boolean }) {
  const q = useFailoverTests(clusterId);
  const runs = q.data?.unrestored ?? [];
  if (runs.length === 0) return null;
  return (
    <div className="space-y-2">
      {runs.map((r) => (
        <UnrestoredRow key={r.id} run={r} isAdmin={isAdmin} />
      ))}
    </div>
  );
}

function UnrestoredRow({ run, isAdmin }: { run: TestRun; isAdmin: boolean }) {
  const restore = useRestoreTestRun();
  const unit = run.definition.unit || "de dienst";
  return (
    <div
      role="alert"
      className="flex flex-wrap items-center justify-between gap-3 rounded-md border border-red-300 bg-red-50 px-4 py-3 text-sm text-red-800 dark:border-red-900 dark:bg-red-950 dark:text-red-200"
    >
      <div>
        <p className="font-medium">
          Failovertest niet volledig hersteld: {unit} staat mogelijk nog uit op {run.hostname}.
        </p>
        <p className="mt-0.5">
          {run.definition.name}, {fmt.format(new Date(run.created_at))}.{" "}
          <Link href={reportHref(run.id)} className="underline">
            Rapport bekijken
          </Link>
        </p>
        {restore.error && <p className="mt-1">{restore.error.message}</p>}
        {restore.data && (
          <p className="mt-1">
            Herstel gestart.{" "}
            <Link href={`/taken/detail?id=${restore.data.id}`} className="underline">
              Taak volgen
            </Link>
          </p>
        )}
      </div>
      {isAdmin && !restore.data && (
        <Button variant="danger" disabled={restore.isPending} onClick={() => restore.mutate(run.id)}>
          {restore.isPending ? "Bezig…" : "Opnieuw herstellen"}
        </Button>
      )}
    </div>
  );
}

// FailoverCard toont de failovertests van een cluster met hun laatste
// resultaat; een admin maakt, wijzigt en start ze hier.
export function FailoverCard({ clusterId, isAdmin }: { clusterId: string; isAdmin: boolean }) {
  const q = useFailoverTests(clusterId);
  const [form, setForm] = useState<FailoverTest | "new" | null>(null);
  const [starting, setStarting] = useState<FailoverTest | null>(null);
  if (!q.data) return null;
  const { items, options } = q.data;
  const canCreate = options.scenarios.some((s) => s.available);
  return (
    <Card
      title={
        <span className="flex flex-wrap items-center justify-between gap-2">
          <span>Failovertests</span>
          {isAdmin && (
            <Button
              variant="secondary"
              disabled={!canCreate}
              title={canCreate ? undefined : options.scenarios.map((s) => s.reason).find(Boolean)}
              onClick={() => setForm("new")}
            >
              Test toevoegen
            </Button>
          )}
        </span>
      }
    >
      {items.length === 0 ? (
        <Empty>
          {canCreate
            ? "Nog geen failovertests. Een test stopt keepalived of nginx op de node met het VIP en meet hoe snel een andere node overneemt."
            : `Hier kan nog geen failovertest: ${options.scenarios[0]?.reason ?? "geen scenario beschikbaar"}.`}
        </Empty>
      ) : (
        <ul className="divide-y divide-slate-100 dark:divide-slate-800">
          {items.map((t) => (
            <TestRow
              key={t.id}
              t={t}
              options={options}
              isAdmin={isAdmin}
              onEdit={() => setForm(t)}
              onStart={() => setStarting(t)}
            />
          ))}
        </ul>
      )}
      {isAdmin && options.run_blocked && items.length > 0 && (
        <p className="mt-3 text-xs text-slate-500">Testen kan hier nu niet: {options.run_blocked}.</p>
      )}
      {form && (
        <Modal title={form === "new" ? "Failovertest toevoegen" : "Failovertest bewerken"} onClose={() => setForm(null)} wide>
          <FailoverForm clusterId={clusterId} initial={form === "new" ? undefined : form} options={options} onDone={() => setForm(null)} />
        </Modal>
      )}
      {starting && <StartDialog t={starting} options={options} onClose={() => setStarting(null)} />}
    </Card>
  );
}

function TestRow({
  t,
  options,
  isAdmin,
  onEdit,
  onStart,
}: {
  t: FailoverTest;
  options: FailoverOptions;
  isAdmin: boolean;
  onEdit: () => void;
  onStart: () => void;
}) {
  const [history, setHistory] = useState(false);
  const running = t.last_run && !t.last_run.result;
  return (
    <li className="py-3 first:pt-0 last:pb-0">
      <div className="grid gap-3 sm:grid-cols-[1fr_auto]">
        <div className="min-w-0 space-y-1 text-sm">
          <p className="font-medium">{t.name}</p>
          <p className="text-slate-600 dark:text-slate-400">
            {t.description} <span className="font-mono text-xs">{t.vip_address}</span>
            {t.vip_owner && <span className="text-slate-500"> (nu op {t.vip_owner})</span>}
          </p>
          <p className="text-xs text-slate-500">
            {expectation(t)} · probe {probeText(t.probe)}
          </p>
          <p className="flex flex-wrap items-center gap-2 text-xs">
            {t.last_run ? (
              <>
                <RunBadge run={t.last_run} />
                <Link href={reportHref(t.last_run.id)} className={linkClass}>
                  {running ? "Rapport volgen" : fmt.format(new Date(t.last_run.finished_at ?? t.last_run.created_at))}
                </Link>
                {t.last_run.result && <span className="text-slate-500">{t.last_run.summary}</span>}
              </>
            ) : (
              <span className="text-slate-500">Nog nooit getest.</span>
            )}
          </p>
        </div>
        <div className="flex flex-wrap items-start gap-2">
          {t.last_run && (
            <Button variant="ghost" className="px-2 py-1 text-xs" onClick={() => setHistory((h) => !h)}>
              {history ? "Verberg geschiedenis" : "Geschiedenis"}
            </Button>
          )}
          {isAdmin && (
            <>
              <Button variant="secondary" onClick={onEdit}>
                Bewerken
              </Button>
              <Button disabled={!!options.run_blocked || !!running} title={options.run_blocked || undefined} onClick={onStart}>
                {running ? "Loopt…" : "Nu testen"}
              </Button>
            </>
          )}
        </div>
      </div>
      {history && <RunHistory testId={t.id} />}
    </li>
  );
}

function RunHistory({ testId }: { testId: string }) {
  const runs = useFailoverRuns(testId);
  if (!runs.data) return <p className="mt-2 text-xs text-slate-500">Laden…</p>;
  return (
    <ul className="mt-3 space-y-1.5 rounded-md bg-slate-50 p-3 text-xs dark:bg-slate-800/50">
      {runs.data.slice(0, 10).map((r) => (
        <li key={r.id} className="flex flex-wrap items-baseline gap-2">
          <RunBadge run={r} />
          <Link href={reportHref(r.id)} className={cx("tabular-nums", linkClass)}>
            {fmt.format(new Date(r.created_at))}
          </Link>
          <span className="text-slate-600 dark:text-slate-400">{r.summary}</span>
        </li>
      ))}
    </ul>
  );
}

// StartDialog is de bevestiging: wat er gestopt wordt, wat de test verwacht
// en hoe lang het VIP in het slechtste geval onbereikbaar is.
function StartDialog({ t, options, onClose }: { t: FailoverTest; options: FailoverOptions; onClose: () => void }) {
  const start = useStartFailoverTest();
  const router = useRouter();
  const err = start.error instanceof ApiError ? start.error : null;
  return (
    <Modal title={`Failovertest starten: ${t.name}`} onClose={onClose}>
      <div className="space-y-4 text-sm">
        <p>{confirmText(t, options)}</p>
        <p className="text-slate-500">
          Afbreken kan op de rapportpagina; ClusterForge zet {unitOf(t)} dan meteen weer aan.
        </p>
        {err?.code === "precheck_failed" && err.checks ? (
          <div className="space-y-2">
            <Alert>De test start niet, omdat de voorcontrole faalt.</Alert>
            <Checks checks={err.checks} />
          </div>
        ) : (
          start.error && <Alert>{start.error.message}</Alert>
        )}
        <div className="flex justify-end gap-2">
          <Button variant="secondary" onClick={onClose}>
            Annuleren
          </Button>
          <Button
            variant="danger"
            disabled={start.isPending}
            onClick={async () => {
              const run = await start.mutateAsync(t.id).catch(() => null);
              if (run) router.push(reportHref(run.id));
            }}
          >
            {start.isPending ? "Voorcontrole…" : `${unitOf(t)} stoppen en meten`}
          </Button>
        </div>
      </div>
    </Modal>
  );
}

// Checks toont de controles van de voorcontrole.
export function Checks({ checks }: { checks: TestRunCheck[] }) {
  return (
    <ul className="space-y-1 text-sm">
      {checks.map((c) => (
        <li key={c.name} className="flex gap-2">
          <span aria-hidden className={c.ok ? "text-emerald-600" : "text-red-600"}>
            {c.ok ? "✓" : "✗"}
          </span>
          <span>
            <span className="font-medium">{c.name}</span>
            {c.detail && <span className="text-slate-600 dark:text-slate-400">: {c.detail}</span>}
          </span>
        </li>
      ))}
    </ul>
  );
}

type ProbeKind = "http" | "tcp";

function FailoverForm({
  clusterId,
  initial,
  options,
  onDone,
}: {
  clusterId: string;
  initial?: FailoverTest;
  options: FailoverOptions;
  onDone: () => void;
}) {
  const create = useCreateFailoverTest(clusterId);
  const update = useUpdateFailoverTest();
  const remove = useDeleteFailoverTest();
  const firstAvailable = options.scenarios.find((s) => s.available) ?? options.scenarios[0];
  const firstVip = options.vips[0];

  const [scenario, setScenario] = useState<FailoverScenario>(initial?.scenario ?? firstAvailable?.key ?? "keepalived_stop");
  const scenarioOpt = options.scenarios.find((s) => s.key === scenario);
  const [service, setService] = useState(initial?.service || scenarioOpt?.units[0] || "nginx");
  const [vipId, setVipId] = useState(initial?.vip_id ?? firstVip?.id ?? "");
  const [secs, setSecs] = useState(String(initial?.max_takeover_seconds ?? scenarioOpt?.default_seconds ?? 5));
  const [secsTouched, setSecsTouched] = useState(!!initial);
  const [failback, setFailback] = useState(initial?.expect_failback ?? options.default_failback);
  const startProbe = initial?.probe ?? firstVip?.probe ?? { http: { path: "/", expect: 200 } };
  const [probeKind, setProbeKind] = useState<ProbeKind>(startProbe.tcp ? "tcp" : "http");
  const [path, setPath] = useState(startProbe.http?.path ?? "/");
  const [expect, setExpect] = useState(String(startProbe.http?.expect ?? 200));
  const [port, setPort] = useState(String(startProbe.tcp?.port ?? 80));
  const [probeTouched, setProbeTouched] = useState(!!initial);
  const [name, setName] = useState(initial?.name ?? "");
  const [nameTouched, setNameTouched] = useState(!!initial);
  const [error, setError] = useState<{ message: string; field?: string } | null>(null);
  const busy = create.isPending || update.isPending || remove.isPending;

  const defaultName = (sc: FailoverScenario, svc: string) => (sc === "keepalived_stop" ? "keepalived stoppen" : `${svc} stoppen`);
  const shownName = nameTouched ? name : defaultName(scenario, service);

  function pickScenario(key: FailoverScenario) {
    setScenario(key);
    const opt = options.scenarios.find((s) => s.key === key);
    if (!secsTouched && opt) setSecs(String(opt.default_seconds));
    if (key === "service_stop" && opt && !opt.units.includes(service)) setService(opt.units[0] ?? "nginx");
  }

  function pickVip(id: string) {
    setVipId(id);
    const v = options.vips.find((x) => x.id === id);
    if (!probeTouched && v) applyProbe(v.probe);
  }

  function applyProbe(p: FailoverProbe) {
    setProbeKind(p.tcp ? "tcp" : "http");
    if (p.http) {
      setPath(p.http.path);
      setExpect(String(p.http.expect));
    }
    if (p.tcp) setPort(String(p.tcp.port));
  }

  async function submit(e: FormEvent) {
    e.preventDefault();
    setError(null);
    const body: FailoverTestInput = {
      name: shownName.trim(),
      vip_id: vipId,
      scenario,
      service: scenario === "service_stop" ? service : "",
      max_takeover_seconds: Number(secs),
      expect_failback: failback,
      probe: probeKind === "tcp" ? { tcp: { port: Number(port) } } : { http: { path, expect: Number(expect) } },
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
        <legend className="mb-1 text-sm font-medium text-slate-700 dark:text-slate-300">Scenario</legend>
        <div className="space-y-2">
          {options.scenarios.map((s) => {
            const disabled = !s.available && s.key !== initial?.scenario;
            return (
              <label
                key={s.key}
                className={cx(
                  "flex gap-3 rounded-md border p-3 text-sm",
                  scenario === s.key ? "border-brand-500 bg-brand-50/50 dark:bg-slate-800" : "border-slate-200 dark:border-slate-800",
                  disabled ? "cursor-not-allowed opacity-60" : "cursor-pointer",
                )}
              >
                <input
                  type="radio"
                  name="scenario"
                  className="mt-0.5"
                  value={s.key}
                  checked={scenario === s.key}
                  disabled={disabled}
                  onChange={() => pickScenario(s.key)}
                />
                <span>
                  <span className="font-medium">{s.label}</span>
                  <span className="block text-xs text-slate-500">
                    {s.available ? `Standaard verwacht: overname binnen ${s.default_seconds} s` : `Kan nu niet: ${s.reason}`}
                  </span>
                </span>
              </label>
            );
          })}
        </div>
        {fieldError("scenario")}
      </fieldset>

      <div className="grid gap-4 sm:grid-cols-2">
        {scenario === "service_stop" && (
          <Field label="Dienst" htmlFor="fo-service">
            <Select id="fo-service" value={service} onChange={(e) => setService(e.target.value)}>
              {(scenarioOpt?.units.length ? scenarioOpt.units : [service]).map((u) => (
                <option key={u} value={u}>
                  {u}
                </option>
              ))}
            </Select>
            {fieldError("service")}
          </Field>
        )}
        <Field label="VIP" htmlFor="fo-vip">
          <Select id="fo-vip" required value={vipId} onChange={(e) => pickVip(e.target.value)}>
            {options.vips.length === 0 && <option value="">Geen VIP&apos;s</option>}
            {options.vips.map((v) => (
              <option key={v.id} value={v.id}>
                {v.address}
                {v.owner_hostname ? ` (nu op ${v.owner_hostname})` : ""}
              </option>
            ))}
          </Select>
          {fieldError("vip_id")}
        </Field>
        <Field label="Verwachte overname in seconden" htmlFor="fo-secs" hint="Hoogstens 120 s. Duurt het langer, dan is de uitslag FAIL.">
          <Input
            id="fo-secs"
            type="number"
            min={1}
            max={120}
            required
            value={secs}
            onChange={(e) => {
              setSecs(e.target.value);
              setSecsTouched(true);
            }}
          />
          {fieldError("max_takeover_seconds")}
        </Field>
        <Field label="Naam" htmlFor="fo-name">
          <Input
            id="fo-name"
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

      <label className="flex items-start gap-2 text-sm">
        <input type="checkbox" className="mt-0.5" checked={failback} onChange={(e) => setFailback(e.target.checked)} />
        <span>
          VIP moet daarna terug naar de oorspronkelijke node
          <span className="block text-xs text-slate-500">
            Aan bij keepalived met preempt, zoals de template keepalived-nginx. Uit als het VIP na een overname blijft waar het is.
          </span>
        </span>
      </label>

      <fieldset>
        <legend className="mb-1 text-sm font-medium text-slate-700 dark:text-slate-300">Probe</legend>
        <p className="mb-2 text-xs text-slate-500">
          ClusterForge vraagt het VIP tijdens de test elke kwart seconde op. De host is altijd het VIP.
        </p>
        <div className="mb-3 flex gap-4 text-sm">
          {(["http", "tcp"] as const).map((k) => (
            <label key={k} className="flex items-center gap-2">
              <input
                type="radio"
                name="probe"
                checked={probeKind === k}
                onChange={() => {
                  setProbeKind(k);
                  setProbeTouched(true);
                }}
              />
              {k === "http" ? "HTTP" : "TCP"}
            </label>
          ))}
        </div>
        {probeKind === "http" ? (
          <div className="grid gap-4 sm:grid-cols-[1fr_8rem]">
            <div>
              <Label htmlFor="fo-path">Pad</Label>
              <Input
                id="fo-path"
                required
                placeholder="/health"
                value={path}
                onChange={(e) => {
                  setPath(e.target.value);
                  setProbeTouched(true);
                }}
              />
            </div>
            <div>
              <Label htmlFor="fo-expect">Status</Label>
              <Input
                id="fo-expect"
                type="number"
                min={100}
                max={599}
                required
                value={expect}
                onChange={(e) => {
                  setExpect(e.target.value);
                  setProbeTouched(true);
                }}
              />
            </div>
          </div>
        ) : (
          <div className="max-w-[10rem]">
            <Label htmlFor="fo-port">Poort</Label>
            <Input
              id="fo-port"
              type="number"
              min={1}
              max={65535}
              required
              value={port}
              onChange={(e) => {
                setPort(e.target.value);
                setProbeTouched(true);
              }}
            />
          </div>
        )}
        {fieldError("probe")}
      </fieldset>

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
              if (!window.confirm(`Failovertest ${initial.name} verwijderen? De rapporten van eerdere runs blijven bewaard.`)) return;
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
