"use client";

import { Badge, cx } from "@/components/ui";
import { tableClass, tdClass, thClass } from "@/components/inventory/bits";
import type { HealPlan, HealReport } from "@/lib/heal";

// HealPanel toont bij een database wat Zelfherstel vaststelde, waarom, en
// wat het zou doen. Zonder problemen toont het niets.
export function HealPanel({ r }: { r?: HealReport }) {
  if (!r || (r.problems.length === 0 && !r.plan)) return null;
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
    </section>
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
