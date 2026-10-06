"use client";

import { useRouter } from "next/navigation";
import { useState } from "react";
import { ImpactLine } from "@/components/deps/ImpactList";
import { Modal } from "@/components/Modal";
import { ProdConfirm, prodConfirmed, useProdTotp } from "@/components/ProdConfirm";
import { expectedText, shortActual } from "@/components/drift/text";
import { Alert, Badge, Button, cx } from "@/components/ui";
import { ApiError } from "@/lib/api/client";
import { useRemediate, type DriftFinding, type DriftNode, type DriftReport, type RemediationInput, type RemediationPlan } from "@/lib/drift";

// Pick is één afwijkende stap op één node, met alle afwijkingen ervan.
type Pick = { node: DriftNode; step: string; title: string; findings: DriftFinding[] };

const pickKey = (p: { node: DriftNode; step: string }) => p.node.node_id + " " + p.step;

// candidates zijn de stappen die te herstellen zijn: afwijkend en niet
// genegeerd, in de volgorde van het rapport.
function candidates(r: DriftReport): Pick[] {
  const out: Pick[] = [];
  for (const n of r.nodes) {
    for (const f of n.findings) {
      if (f.ignored) continue;
      const p = out.find((x) => x.node.node_id === n.node_id && x.step === f.step);
      if (p) p.findings.push(f);
      else out.push({ node: n, step: f.step, title: f.title, findings: [f] });
    }
  }
  return out;
}

// blocked zegt waarom op een node nu niet hersteld kan worden, of null.
function blocked(n: DriftNode): string | null {
  if (n.agent_too_old) return "agent te oud";
  if (n.skipped) return n.skipped;
  if (n.status === "error") return "de laatste controle mislukte";
  return null;
}

// RemediateDialog is het herstelvenster: kiezen welke afwijkingen, dan in
// gewone zinnen zien wat er gebeurt in de volgorde van de taak, op prod de
// slug intikken, en daarna naar de taak.
export function RemediateDialog({ clusterId, report, onClose }: { clusterId: string; report: DriftReport; onClose: () => void }) {
  const router = useRouter();
  const all = candidates(report);
  const usable = all.filter((p) => blocked(p.node) === null);
  const [chosen, setChosen] = useState<string[]>(() => usable.map(pickKey));
  const [plan, setPlan] = useState<RemediationPlan | null>(null);
  // De keuze met de vingerafdrukken van het moment van Bekijken; herstellen
  // stuurt precies die, ook als het rapport intussen ververst.
  const [shown, setShown] = useState<RemediationInput | null>(null);
  const [confirm, setConfirm] = useState("");
  const remediate = useRemediate(clusterId);
  const totp = useProdTotp();
  const ignored = report.nodes.reduce((sum, n) => sum + n.findings.filter((f) => f.ignored).length, 0);

  const input = (): RemediationInput => {
    const nodes: RemediationInput["nodes"] = [];
    for (const p of usable) {
      if (!chosen.includes(pickKey(p))) continue;
      let n = nodes.find((x) => x.node_id === p.node.node_id);
      if (!n) {
        n = { node_id: p.node.node_id, steps: [] };
        nodes.push(n);
      }
      n.steps.push({ step: p.step, fingerprints: p.findings.map((f) => f.fingerprint) });
    }
    return { nodes };
  };
  const preview = () => {
    const body = input();
    remediate.reset();
    remediate.mutate(
      { ...body, preview: true },
      {
        onSuccess: (r) => {
          setShown(body);
          setPlan(r.plan);
        },
      },
    );
  };
  const start = () =>
    shown &&
    remediate.mutate(
      { ...shown, confirm: plan?.needs_confirmation ? confirm : undefined },
      {
        onSuccess: (r) => {
          if (r.job) router.push(`/taken/detail?id=${r.job.id}`);
        },
      },
    );
  const err = remediate.error;
  const errText = err instanceof ApiError ? err.message : err ? "Herstellen mislukt" : null;
  const stale = err instanceof ApiError && err.code === "drift_changed";

  return (
    <Modal title="Drift herstellen" onClose={onClose} wide>
      <div className="space-y-4 text-sm">
        <ol className="flex flex-wrap gap-x-4 gap-y-1 text-xs text-slate-500">
          {["Kiezen", "Bekijken en herstellen"].map((label, i) => (
            <li key={label} className={(plan ? 1 : 0) === i ? "font-semibold text-slate-900 dark:text-slate-100" : undefined}>
              {i + 1}. {label}
            </li>
          ))}
        </ol>

        {!plan && (
          <>
            <p className="text-slate-600 dark:text-slate-400">
              ClusterForge past alleen de gekozen stappen van de template opnieuw toe, node voor node. Een bestand wordt
              helemaal overschreven en een service wordt ingeschakeld en gestart; wat je met de hand veranderde, gaat dus
              verloren.
            </p>
            {all.length === 0 && <p className="text-slate-500">Er is niets om te herstellen.</p>}
            <div className="space-y-3">
              {report.nodes
                .filter((n) => all.some((p) => p.node.node_id === n.node_id))
                .map((n) => {
                  const why = blocked(n);
                  const picks = all.filter((p) => p.node.node_id === n.node_id);
                  return (
                    <div key={n.node_id} className="rounded-md border border-slate-200 dark:border-slate-800">
                      <div className="flex flex-wrap items-center gap-2 border-b border-slate-100 px-3 py-2 dark:border-slate-800">
                        <span className="font-medium">{n.hostname}</span>
                        {why && <span className="text-xs text-slate-500">kan nu niet: {why}</span>}
                      </div>
                      <ul className="divide-y divide-slate-100 dark:divide-slate-800">
                        {picks.map((p) => (
                          <li key={p.step}>
                            <label className={cx("flex gap-3 px-3 py-2", why ? "opacity-60" : "cursor-pointer")}>
                              <input
                                type="checkbox"
                                className="mt-0.5"
                                disabled={why !== null}
                                checked={chosen.includes(pickKey(p))}
                                onChange={(e) =>
                                  setChosen(e.target.checked ? [...chosen, pickKey(p)] : chosen.filter((k) => k !== pickKey(p)))
                                }
                              />
                              <span className="min-w-0">
                                <span className="block font-medium break-all">{p.title}</span>
                                <span className="block text-xs text-slate-500">
                                  Verwacht {p.findings.map(expectedText).join(", ")} · werkelijk {p.findings.map(shortActual).join(", ")}
                                </span>
                              </span>
                            </label>
                          </li>
                        ))}
                      </ul>
                    </div>
                  );
                })}
            </div>
            {ignored > 0 && (
              <p className="text-slate-500">
                {ignored === 1 ? "Eén genegeerde afwijking blijft" : `${ignored} genegeerde afwijkingen blijven`} zoals ze zijn.
              </p>
            )}
            {errText && (
              <Alert>
                {errText}
                {stale && " Sluit dit venster en kies Nu controleren."}
              </Alert>
            )}
            <div className="flex justify-end gap-2">
              <Button type="button" variant="secondary" onClick={onClose}>
                Annuleren
              </Button>
              <Button type="button" disabled={chosen.length === 0 || remediate.isPending} onClick={preview}>
                {remediate.isPending ? "Bekijken…" : "Bekijken"}
              </Button>
            </div>
          </>
        )}

        {plan && (
          <>
            <p className="text-slate-600 dark:text-slate-400">
              Dit gebeurt in deze volgorde, op {plan.cluster} ({plan.template} {plan.version}):
            </p>
            <ol className="space-y-3">
              {plan.nodes.map((n, i) => (
                <li key={n.node_id} className="rounded-md border border-slate-200 px-3 py-2 dark:border-slate-800">
                  <div className="flex flex-wrap items-center gap-2">
                    <span className="text-slate-500">{i + 1}.</span>
                    <span className="font-medium">{n.hostname}</span>
                    {n.vips.length > 0 && <Badge tone="blue">VIP-eigenaar · {n.vips.join(", ")}</Badge>}
                  </div>
                  <ul className="mt-1 list-disc space-y-0.5 pl-9">
                    {n.steps.map((st) => (
                      <li key={st.step} className="break-words">
                        {st.action}
                      </li>
                    ))}
                  </ul>
                  <div className="mt-1 pl-9">
                    <ImpactLine nodeId={n.node_id} prefix={`Valt ${n.hostname} hierbij weg, dan raakt dat:`} />
                  </div>
                </li>
              ))}
            </ol>
            {plan.notes.length > 0 && (
              <ul className="space-y-1.5 rounded border border-amber-200 bg-amber-50 px-3 py-2 text-amber-900 dark:border-amber-900 dark:bg-amber-950 dark:text-amber-200">
                {plan.notes.map((n) => (
                  <li key={n}>{n}</li>
                ))}
              </ul>
            )}
            {plan.needs_confirmation && <ProdConfirm slug={plan.slug} value={confirm} onChange={setConfirm} />}
            {errText && <Alert>{errText}</Alert>}
            <div className="flex justify-between gap-2">
              <Button
                type="button"
                variant="secondary"
                onClick={() => {
                  remediate.reset();
                  setPlan(null);
                }}
              >
                Terug
              </Button>
              <Button
                type="button"
                variant={plan.needs_confirmation ? "danger" : "primary"}
                disabled={remediate.isPending || (plan.needs_confirmation && !prodConfirmed(plan.slug, confirm, totp))}
                onClick={start}
              >
                {remediate.isPending ? "Starten…" : "Herstellen"}
              </Button>
            </div>
          </>
        )}
      </div>
    </Modal>
  );
}
