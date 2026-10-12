"use client";

import { tableClass, tdClass, thClass } from "@/components/inventory/bits";
import { Badge, Card } from "@/components/ui";
import { mib } from "@/lib/deploy";
import type { GitPlan } from "@/lib/gitops";
import { Diff } from "./bits";

// Het plan van een wijziging van de spec, uit Git of uit de webinterface:
// de velden, de nieuwe nodes en wat er per node verandert, in volgorde.

// PlanFields toont de velden die veranderen.
export function PlanFields({ plan: p }: { plan: GitPlan }) {
  if (p.metadata.length === 0 && p.params.length === 0) return null;
  return (
    <Card title="Velden">
      <div className="overflow-x-auto">
        <table className={tableClass}>
          <thead className="border-b border-slate-200 dark:border-slate-800">
            <tr>
              <th className={thClass}>Veld</th>
              <th className={thClass}>Oud</th>
              <th className={thClass}>Nieuw</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
            {[...p.metadata, ...p.params].map((f) => (
              <tr key={f.field}>
                <td className={tdClass}>
                  {f.label}
                  <div className="text-xs text-slate-500">
                    <code>{f.field}</code>
                  </div>
                </td>
                <td className={`${tdClass} text-red-700 dark:text-red-300`}>
                  {f.from || <span className="text-slate-400">leeg</span>}
                </td>
                <td className={`${tdClass} text-emerald-700 dark:text-emerald-300`}>
                  {f.to || <span className="text-slate-400">leeg</span>}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </Card>
  );
}

// PlanNewNodes toont de nodes die erbij komen: bij een nieuw cluster alle.
export function PlanNewNodes({ plan: p, kind }: { plan: GitPlan; kind: "create" | "update" }) {
  if (p.new_nodes.length === 0) return null;
  return (
    <Card title={kind === "create" ? "Nodes" : "Nieuwe nodes"}>
      <div className="overflow-x-auto">
        <table className={tableClass}>
          <thead className="border-b border-slate-200 dark:border-slate-800">
            <tr>
              <th className={thClass}>Hostname</th>
              <th className={thClass}>Rol</th>
              <th className={thClass}>Adres</th>
              <th className={thClass}>Proxmox-host</th>
              <th className={thClass}>VM</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
            {p.new_nodes.map((n) => (
              <tr key={n.hostname}>
                <td className={`${tdClass} font-medium`}>{n.hostname}</td>
                <td className={tdClass}>{n.role}</td>
                <td className={`${tdClass} font-mono text-xs`}>{n.address ? `${n.address}/${n.prefix}` : "DHCP"}</td>
                <td className={`${tdClass} whitespace-nowrap`}>
                  {n.host || <span className="text-slate-400">{kind === "update" ? "kiest de taak" : "–"}</span>}
                </td>
                <td className={`${tdClass} whitespace-nowrap`}>
                  {n.vm.cpu} vCPU · {mib(n.vm.memory_mib)} · {n.vm.disk_gib} GB
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {kind === "create" && (p.vip || p.proxmox) && (
        <p className="mt-3 text-sm text-slate-600 dark:text-slate-400">
          {p.proxmox && `Uit Proxmox-koppeling ${p.proxmox}`}
          {p.vip && ` · VIP ${p.vip}${p.vrid ? `, VRRP-id ${p.vrid}` : ""}`}
        </p>
      )}
    </Card>
  );
}

// PlanOrder toont per node wat er verandert, in de volgorde van toepassen.
// local is wat op de nodes al afwijkt en overschreven wordt.
export function PlanOrder({ plan: p, local = [] }: { plan: GitPlan; local?: string[] }) {
  return (
    <Card title="Volgorde op de nodes">
      {p.nodes.length === 0 ? (
        <p className="text-sm text-slate-600 dark:text-slate-400">
          {p.no_steps
            ? "Op geen enkele node verandert een stap; alleen de gegevens in ClusterForge veranderen."
            : "Geen nodes met een wijziging."}
        </p>
      ) : (
        <ol className="space-y-4">
          {p.new_nodes.length > 0 && (
            <li className="text-sm text-slate-600 dark:text-slate-400">
              Eerst: de VM van {p.new_nodes.map((n) => n.hostname).join(" en ")} maken en de agent aanmelden.{" "}
              {p.nodes.some((n) => !n.new)
                ? "Daarna de bestaande nodes, zodat ze de nieuwe peer kennen voor die start."
                : "Op de bestaande nodes verandert niets."}
            </li>
          )}
          {p.nodes.map((n, i) => (
            <li key={n.hostname} className="space-y-2">
              <div className="flex flex-wrap items-center gap-2 text-sm">
                <span className="font-medium">
                  {i + 1}. {n.hostname}
                </span>
                {n.new && <Badge tone="blue">Nieuwe node</Badge>}
                {n.vips.length > 0 && <Badge tone="amber">VIP-eigenaar</Badge>}
              </div>
              <ul className="space-y-1 pl-5">
                {n.steps.map((s) => (
                  <li key={s.step} className="text-sm">
                    {s.diff ? (
                      <details>
                        <summary className="cursor-pointer">
                          {s.title}, {s.summary}
                        </summary>
                        <div className="mt-2">
                          <Diff text={s.diff} />
                        </div>
                      </details>
                    ) : (
                      <span>
                        {s.title}, {s.summary}
                      </span>
                    )}
                  </li>
                ))}
              </ul>
            </li>
          ))}
        </ol>
      )}
      {p.unchanged.length > 0 && <p className="mt-4 text-sm text-slate-500">Ongewijzigd: {p.unchanged.join(", ")}.</p>}
      {local.length > 0 && (
        <div className="mt-4 rounded-md bg-amber-50 p-3 text-sm text-amber-900 dark:bg-amber-950 dark:text-amber-200">
          <p className="font-medium">Let op</p>
          <ul className="mt-1 list-disc space-y-1 pl-5">
            {local.map((l, i) => (
              <li key={i}>{l}</li>
            ))}
          </ul>
        </div>
      )}
    </Card>
  );
}
