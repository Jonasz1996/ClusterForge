"use client";

import { useRouter } from "next/navigation";
import { useState, type FormEvent, type ReactNode } from "react";
import { PlanFields, PlanNewNodes, PlanOrder } from "@/components/gitops/Plan";
import { Modal } from "@/components/Modal";
import { ProdConfirm, prodConfirmed, useProdTotp } from "@/components/ProdConfirm";
import { fieldId, ParamInput, paramHint } from "@/components/inventory/ParamInput";
import { Alert, Button, Label, Select } from "@/components/ui";
import { ApiError } from "@/lib/api/client";
import { useApplySpec, useSpecEditForm, useSpecPlan, type SpecEditForm, type SpecEditInput } from "@/lib/deploy";

export type SpecCluster = { id: string; name: string; slug: string; environment: string };

// SpecEditDialog wijzigt de gewenste staat van een cluster uit een template:
// eerst het formulier, dan het plan zoals bij een wijziging uit Git, en pas
// bij Toepassen een nieuwe revisie en de taak. Met preset slaat het het
// formulier over, zoals bij Worker toevoegen.
export function SpecEditDialog({
  cluster,
  preset,
  title = "Specificatie wijzigen",
  intro,
  onClose,
}: {
  cluster: SpecCluster;
  preset?: Record<string, unknown>;
  title?: string;
  intro?: ReactNode;
  onClose: () => void;
}) {
  const [edit, setEdit] = useState<SpecEditInput | null>(preset ? { params: preset } : null);
  const [fieldError, setFieldError] = useState<ApiError | null>(null);
  return (
    <Modal title={title} onClose={onClose} wide>
      <div className="space-y-4 text-sm">
        {intro}
        {edit === null ? (
          <EditForm
            clusterId={cluster.id}
            error={fieldError}
            onPlan={(e) => {
              setFieldError(null);
              setEdit(e);
            }}
            onClose={onClose}
          />
        ) : (
          <PlanStep
            cluster={cluster}
            edit={edit}
            onBack={
              preset
                ? undefined
                : (err) => {
                    setFieldError(err);
                    setEdit(null);
                  }
            }
            onClose={onClose}
          />
        )}
      </div>
    </Modal>
  );
}

// text is een waarde zoals het formulier hem toont.
const text = (v: unknown) => (v === null || v === undefined ? "" : String(v));

function EditForm({
  clusterId,
  error,
  onPlan,
  onClose,
}: {
  clusterId: string;
  error: ApiError | null;
  onPlan: (e: SpecEditInput) => void;
  onClose: () => void;
}) {
  const [version, setVersion] = useState("");
  const q = useSpecEditForm(clusterId, version);
  const [values, setValues] = useState<Record<string, string>>({});
  const f = q.data;
  const [none, setNone] = useState(false);

  if (q.isLoading) return <p className="text-slate-500">Laden…</p>;
  if (q.isError) return <Alert>{q.error instanceof Error ? q.error.message : "Laden mislukt"}</Alert>;
  if (!f) return null;

  const current = version || f.version;
  const value = (name: string, now: unknown) => values[name] ?? text(now);
  const submit = (e: FormEvent) => {
    e.preventDefault();
    const params = changed(f, values);
    if (Object.keys(params).length === 0 && current === f.version && version === "") {
      setNone(true);
      return;
    }
    onPlan({ version: version || undefined, params });
  };

  return (
    <form className="space-y-4" onSubmit={submit}>
      {f.blocked ? (
        <Alert>{f.blocked}</Alert>
      ) : (
        <p className="text-slate-600 dark:text-slate-400">
          Pas aan wat moet veranderen. Je ziet eerst het plan: wat er per node verandert en welke nodes erbij komen. Er gebeurt
          niets tot je toepast.
        </p>
      )}
      {f.versions.length > 1 && (
        <div>
          <Label htmlFor="spec-version">Versie van {f.template}</Label>
          <Select id="spec-version" value={current} disabled={!!f.blocked} onChange={(e) => setVersion(e.target.value)}>
            {f.versions.map((v) => (
              <option key={v} value={v}>
                {v}
              </option>
            ))}
          </Select>
        </div>
      )}
      <div className="grid gap-4 sm:grid-cols-2">
        {f.params
          .filter((p) => p.param.type !== "secret")
          .map(({ param: p, value: now, locked }) => (
            <div key={p.name}>
              <Label htmlFor={fieldId(`params.${p.name}`)}>{p.label}</Label>
              <ParamInput
                p={p}
                value={value(p.name, now)}
                disabled={!!locked || !!f.blocked}
                onChange={(v) => {
                  setNone(false);
                  setValues((old) => ({ ...old, [p.name]: v }));
                }}
              />
              {error?.field === `params.${p.name}` ? (
                <p className="mt-1 text-xs text-red-700 dark:text-red-300">{error.message}</p>
              ) : (
                <p className="mt-1 text-xs text-slate-500">{locked || paramHint(p)}</p>
              )}
            </div>
          ))}
      </div>
      <p className="text-xs text-slate-500">
        Geheimen maakt ClusterForge zelf aan en bewaart het versleuteld; die staan hier niet.
      </p>
      {error && !error.field?.startsWith("params.") && <Alert>{error.message}</Alert>}
      {none && <Alert kind="info">Er verandert niets: de specificatie is al zo.</Alert>}
      <div className="flex justify-end gap-2">
        <Button type="button" variant="secondary" onClick={onClose}>
          Annuleren
        </Button>
        <Button type="submit" disabled={!!f.blocked}>
          Plan bekijken
        </Button>
      </div>
    </form>
  );
}

// changed zijn de parameters die anders zijn dan nu, zoals de server ze
// verwacht: een getal als getal, aan of uit als boolean.
function changed(f: SpecEditForm, values: Record<string, string>) {
  const out: Record<string, unknown> = {};
  for (const { param: p, value: now, locked } of f.params) {
    const v = values[p.name];
    if (v === undefined || locked || v.trim() === text(now)) continue;
    const t = v.trim();
    if (p.type === "bool") out[p.name] = t === "true";
    else if (p.type === "int" && /^-?\d+$/.test(t)) out[p.name] = Number(t);
    else out[p.name] = t;
  }
  return out;
}

function PlanStep({
  cluster,
  edit,
  onBack,
  onClose,
}: {
  cluster: SpecCluster;
  edit: SpecEditInput;
  onBack?: (err: ApiError | null) => void;
  onClose: () => void;
}) {
  const router = useRouter();
  const plan = useSpecPlan(cluster.id, edit);
  const apply = useApplySpec(cluster.id);
  const totp = useProdTotp();
  const [confirm, setConfirm] = useState("");
  const [done, setDone] = useState<number | null>(null);
  const p = plan.data;
  const prod = p?.needs_confirmation ?? cluster.environment === "prod";
  const ok = !prod || prodConfirmed(cluster.slug, confirm, totp);

  if (plan.isLoading) return <p className="text-slate-500">Plan maken…</p>;
  if (plan.isError) {
    const err = plan.error instanceof ApiError ? plan.error : null;
    return (
      <div className="space-y-4">
        <Alert>{err ? sentence(err.message) : "Het plan maken mislukte."}</Alert>
        <div className="flex justify-end gap-2">
          <Button type="button" variant="secondary" onClick={onClose}>
            Sluiten
          </Button>
          {onBack && (
            <Button type="button" onClick={() => onBack(err)}>
              Terug naar het formulier
            </Button>
          )}
        </div>
      </div>
    );
  }
  if (!p) return null;
  if (done !== null) {
    return (
      <div className="space-y-4">
        <Alert kind="success">
          Revisie {done} is opgeslagen. Op geen enkele node verandert een stap, dus ze geldt meteen als toegepast.
        </Alert>
        <div className="flex justify-end">
          <Button type="button" onClick={onClose}>
            Sluiten
          </Button>
        </div>
      </div>
    );
  }

  const pl = p.plan;
  const err = apply.error instanceof ApiError ? apply.error : null;
  return (
    <div className="space-y-4">
      <p>
        <span className="font-medium">{pl.summary}.</span> Dit is een plan: er is nog niets veranderd.
      </p>
      {p.blocked && <Alert>{p.blocked}</Alert>}
      {p.full_apply && !p.blocked && (
        <Alert kind="info">
          Een eerdere revisie staat nog niet op alle nodes. Toepassen past daarom op elke node alle stappen van de template
          opnieuw toe.
        </Alert>
      )}
      {pl.warnings.length > 0 && (
        <ul className="list-disc space-y-1 pl-5 text-amber-800 dark:text-amber-300">
          {pl.warnings.map((w, i) => (
            <li key={i}>{w}</li>
          ))}
        </ul>
      )}
      <PlanFields plan={pl} />
      <PlanNewNodes plan={pl} kind="update" />
      <PlanOrder plan={pl} />
      {prod && <ProdConfirm slug={cluster.slug} value={confirm} onChange={setConfirm} />}
      {err && (
        <Alert>
          {sentence(err.message)}
          {err.code === "plan_changed" && (
            <>
              {" "}
              <button
                type="button"
                className="font-medium underline"
                onClick={() => void plan.refetch().then(() => apply.reset())}
              >
                Nieuw plan maken
              </button>
            </>
          )}
        </Alert>
      )}
      <div className="flex justify-end gap-2">
        {onBack ? (
          <Button type="button" variant="secondary" onClick={() => onBack(null)}>
            Terug
          </Button>
        ) : (
          <Button type="button" variant="secondary" onClick={onClose}>
            Annuleren
          </Button>
        )}
        <Button
          type="button"
          variant={prod ? "danger" : "primary"}
          disabled={!!p.blocked || !ok || apply.isPending}
          onClick={() =>
            apply.mutate(
              {
                ...edit,
                hash: p.hash,
                // Nieuwe nodes melden zich aan op het adres van deze pagina.
                server_url: pl.new_nodes.length > 0 ? window.location.origin : undefined,
                confirm: prod ? confirm : undefined,
              },
              {
                onSuccess: (r) => {
                  if (r.job) router.push(`/taken/detail?id=${r.job.id}`);
                  else setDone(r.revision);
                },
              },
            )
          }
        >
          {apply.isPending ? "Toepassen…" : pl.new_nodes.length > 0 ? "Toepassen en nodes maken" : "Toepassen"}
        </Button>
      </div>
    </div>
  );
}

// sentence maakt van een foutmelding van de server een zin.
function sentence(msg: string) {
  const s = msg.charAt(0).toUpperCase() + msg.slice(1);
  return /[.!?]$/.test(s) ? s : `${s}.`;
}
