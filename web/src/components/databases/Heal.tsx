"use client";

import Link from "next/link";
import { useRouter } from "next/navigation";
import { useState } from "react";
import { Modal } from "@/components/Modal";
import { ProdConfirm, prodConfirmed, useProdTotp } from "@/components/ProdConfirm";
import { Alert, Badge, Button, cx } from "@/components/ui";
import { tableClass, tdClass, thClass } from "@/components/inventory/bits";
import { sinceFmt } from "@/lib/components";
import { healRunning, useRunHeal, useSetHealAuto, type HealAuto, type HealPlan, type HealReport, type HealRun } from "@/lib/heal";
import { useIsAdmin } from "@/lib/inventory";

// HealPanel toont bij een database wat Zelfherstel vaststelde, waarom, en
// wat het zou doen, met de knop Nu herstellen en Vanzelf herstellen van het
// cluster. Zonder problemen toont het alleen een herstel van het afgelopen
// etmaal en de instelling.
export function HealPanel({ r }: { r?: HealReport }) {
  const admin = useIsAdmin();
  const [open, setOpen] = useState(false);
  if (!r) return null;
  const auto = r.auto && r.cluster_id && (admin || r.auto.enabled) ? <AutoToggle clusterId={r.cluster_id} a={r.auto} admin={admin} /> : null;
  const quiet = r.problems.length === 0 && !r.plan;
  if (quiet && !r.last_run && !auto) return null;
  if (quiet)
    return (
      <div className="mb-4 space-y-2">
        {r.last_run && <LastRun run={r.last_run} />}
        {auto}
      </div>
    );
  const canRun = admin && r.plan && !r.plan.blocked && !r.plan.managed_component_id && !healRunning(r.last_run);
  return (
    <section className="mb-4 rounded border border-slate-200 p-3 dark:border-slate-800">
      <h3 className="mb-2 text-sm font-semibold">Zelfherstel</h3>
      <ul className="space-y-2">
        {r.problems.map((p, i) => (
          <li
            key={i}
            className={cx(
              "rounded border px-3 py-2 text-sm",
              p.level === "down"
                ? "border-red-200 bg-red-50 text-red-900 dark:border-red-900 dark:bg-red-950 dark:text-red-200"
                : "border-amber-200 bg-amber-50 text-amber-900 dark:border-amber-900 dark:bg-amber-950 dark:text-amber-200",
            )}
          >
            <p className="font-medium">
              <Badge tone={p.level === "down" ? "red" : "amber"}>{p.level === "down" ? "Probleem" : "Let op"}</Badge> {p.title}
            </p>
            <p className="mt-1">{p.why}</p>
          </li>
        ))}
      </ul>
      {r.plan && <Plan p={r.plan} />}
      {r.last_run && (
        <div className="mt-3">
          <LastRun run={r.last_run} />
        </div>
      )}
      {r.plan?.managed_component_id && r.cluster_id && (
        <p className="mt-3 text-sm">
          <Link href={`/clusters/detail?id=${r.cluster_id}`} className="font-medium underline">
            Naar de database van {r.cluster_name}
          </Link>
        </p>
      )}
      {auto && <div className="mt-3">{auto}</div>}
      {canRun && (
        <div className="mt-3 flex justify-end">
          <Button onClick={() => setOpen(true)}>Nu herstellen</Button>
        </div>
      )}
      {open && r.plan && <RunDialog r={r} p={r.plan} onClose={() => setOpen(false)} />}
    </section>
  );
}

// AutoToggle is Vanzelf herstellen van het cluster, met waarom het plan nu
// wel of niet vanzelf gebeurt.
function AutoToggle({ clusterId, a, admin }: { clusterId: string; a: HealAuto; admin: boolean }) {
  const set = useSetHealAuto(clusterId);
  const paused = a.enabled && a.paused_at !== null;
  return (
    <div className="space-y-2 text-sm">
      <label className="flex items-start gap-2">
        <input
          type="checkbox"
          className="mt-0.5"
          checked={a.enabled && !paused}
          disabled={!admin || set.isPending || (!a.allowed && !a.enabled)}
          onChange={(e) => set.mutate(e.target.checked)}
        />
        <span>
          Vanzelf herstellen op dit cluster
          <span className="block text-slate-500">
            {a.allowed
              ? "ClusterForge voert een plan dat vanzelf mag zelf uit, als het probleem minstens twee minuten bestaat. Hoogstens drie pogingen per uur, na een mislukte poging een kwartier wachten, het Galera-cluster opnieuw beginnen hoogstens eens per uur, en na drie mislukte pogingen op rij pauze. Adressen wijzigen gebeurt nooit vanzelf."
              : "Op prod herstelt ClusterForge nooit vanzelf; elk herstel vraagt je bevestiging."}
          </span>
        </span>
      </label>
      {paused && (
        <Alert>
          Gepauzeerd sinds {sinceFmt.format(new Date(a.paused_at!))}: {a.paused_reason}. Zet het opnieuw aan om het weer te laten
          proberen.
        </Alert>
      )}
      {a.note && !paused && a.enabled && <p className="text-slate-600 dark:text-slate-400">{a.note}</p>}
      {set.isError && <Alert>{set.error.message}</Alert>}
    </div>
  );
}

function LastRun({ run }: { run: HealRun }) {
  const job = (
    <Link href={`/taken/detail?id=${run.job_id}`} className="underline">
      bekijk de taak
    </Link>
  );
  const at = sinceFmt.format(new Date(run.finished_at ?? run.created_at));
  if (healRunning(run)) return <Alert kind="info">Zelfherstel is bezig ({job}).</Alert>;
  if (run.status === "succeeded") return <Alert kind="success">Zelfherstel herstelde deze database op {at} ({job}).</Alert>;
  return (
    <Alert>
      Zelfherstel {run.status === "canceled" ? "werd geannuleerd" : "mislukte"} op {at}
      {run.error && `: ${run.error}`} ({job}).
    </Alert>
  );
}

// RunDialog vraagt de bevestiging voor het plan, met de slug op prod.
function RunDialog({ r, p, onClose }: { r: HealReport; p: HealPlan; onClose: () => void }) {
  const run = useRunHeal();
  const router = useRouter();
  const totp = useProdTotp();
  const [confirm, setConfirm] = useState("");
  const prod = r.cluster_environment === "prod";
  const begins = p.action === "galera" && !p.nodes.some((n) => n.primary);
  return (
    <Modal title={`${r.cluster_name || r.engine} herstellen`} onClose={onClose} wide>
      <div className="space-y-4 text-sm">
        <p>ClusterForge doet dit, stap voor stap, als een taak die je kunt volgen:</p>
        <ol className="list-decimal space-y-1 pl-5">
          {p.steps.map((s, i) => (
            <li key={i}>{s}</li>
          ))}
        </ol>
        {p.nodes.some((n) => n.set_address) && (
          <p className="text-slate-600 dark:text-slate-400">
            Van elk configuratiebestand dat het wijzigt, bewaart de agent eerst een kopie ernaast (.cf-zelfherstel-&lt;tijd&gt;).
          </p>
        )}
        {begins && (
          <p className="text-slate-600 dark:text-slate-400">
            Het cluster begint alleen opnieuw op de node met de nieuwste data. Komt er intussen een node in een Primary component,
            dan sluiten de andere daar aan en begint ClusterForge niets.
          </p>
        )}
        {prod && <ProdConfirm slug={r.cluster_slug} value={confirm} onChange={setConfirm} />}
        {run.isError && <Alert>{run.error.message}</Alert>}
        <div className="flex justify-end gap-2">
          <Button variant="secondary" onClick={onClose}>
            Annuleren
          </Button>
          <Button
            disabled={run.isPending || (prod && !prodConfirmed(r.cluster_slug, confirm, totp))}
            onClick={async () => {
              const j = await run.mutateAsync({ key: r.key, hash: p.hash, confirm: prod ? confirm : undefined }).catch(() => null);
              if (j) router.push(`/taken/detail?id=${j.id}`);
            }}
          >
            {run.isPending ? "Bezig…" : "Nu herstellen"}
          </Button>
        </div>
      </div>
    </Modal>
  );
}

function Plan({ p }: { p: HealPlan }) {
  const addresses = p.nodes.filter((n) => n.set_address);
  return (
    <div className="mt-4 space-y-3 text-sm">
      <h4 className="font-semibold">Herstelplan</h4>
      <ol className="list-decimal space-y-1 pl-5 text-slate-700 dark:text-slate-300">
        {p.steps.map((s, i) => (
          <li key={i}>{s}</li>
        ))}
      </ol>
      {addresses.length > 0 && (
        <div className="relative overflow-x-auto">
          <table className={tableClass}>
            <thead className="border-b border-slate-200 dark:border-slate-800">
              <tr>
                <th className={thClass}>Node</th>
                <th className={thClass}>Nu</th>
                <th className={thClass}>Wordt</th>
                <th className={thClass}>Bestand</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
              {addresses.map((n) => (
                <tr key={n.node_id}>
                  <td className={tdClass}>{n.hostname}</td>
                  <td className={`${tdClass} font-mono text-xs`}>
                    <div>{n.old_cluster_address}</div>
                    {n.old_node_address && <div>node: {n.old_node_address}</div>}
                  </td>
                  <td className={`${tdClass} font-mono text-xs`}>
                    <div>{n.cluster_address}</div>
                    {n.node_address && <div>node: {n.node_address}</div>}
                  </td>
                  <td className={`${tdClass} font-mono text-xs`}>{n.file}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {p.blocked ? (
        <p className="text-red-700 dark:text-red-300">Kan nu niet: {p.blocked}.</p>
      ) : (
        <p className="text-slate-600 dark:text-slate-400">
          {p.auto ? <Badge tone="green">mag vanzelf</Badge> : <Badge>alleen met bevestiging</Badge>} {p.auto_why}
        </p>
      )}
    </div>
  );
}
