"use client";

import Link from "next/link";
import { useSearchParams } from "next/navigation";
import { Suspense, useState } from "react";
import { ChangeStatusBadge, CommitLine, Diff } from "@/components/gitops/bits";
import { ApproveDialog, RejectDialog } from "@/components/gitops/Decide";
import { EnvBadge, QueryState, tableClass, tdClass, thClass } from "@/components/inventory/bits";
import { Alert, Badge, Button, Card, PageHeader } from "@/components/ui";
import { mib } from "@/lib/deploy";
import { useGitChange, type GitChangeDetail, type GitPlan } from "@/lib/gitops";
import { useIsAdmin } from "@/lib/inventory";

export default function GitChangePage() {
  return (
    <Suspense>
      <GitChangeInner />
    </Suspense>
  );
}

function GitChangeInner() {
  const id = useSearchParams().get("id") ?? "";
  const change = useGitChange(id);
  if (id === "") return <Alert>Geen wijziging opgegeven.</Alert>;
  return <QueryState q={change}>{change.data && <ChangeView c={change.data} />}</QueryState>;
}

const timeFmt = new Intl.DateTimeFormat("nl-BE", { dateStyle: "medium", timeStyle: "short" });

function ChangeView({ c }: { c: GitChangeDetail }) {
  const p = c.plan;
  const name = c.cluster_name || c.slug;
  const isAdmin = useIsAdmin();
  const [dialog, setDialog] = useState<"approve" | "reject" | null>(null);
  return (
    <div className="space-y-6">
      <div className="text-sm">
        <Link href="/gitops" className="text-slate-500 hover:underline">
          ← GitOps
        </Link>
      </div>
      <PageHeader
        title={c.kind === "create" ? `Nieuw cluster ${name}` : `Wijziging voor ${name}`}
        description={
          <span className="inline-flex flex-wrap items-center gap-2">
            <ChangeStatusBadge status={c.status} />
            <EnvBadge env={c.cluster_environment} />
            {c.cluster_id && (
              <Link href={`/clusters/detail?id=${c.cluster_id}`} className="text-brand-700 hover:underline dark:text-sky-300">
                naar het cluster
              </Link>
            )}
            {c.file_url && (
              <a href={c.file_url} target="_blank" rel="noreferrer" className="font-mono text-xs text-brand-700 hover:underline dark:text-sky-300">
                {c.path}
              </a>
            )}
          </span>
        }
      />
      {c.status === "pending" && (
        <Card>
          <div className="space-y-3 text-sm">
            <p>
              Dit is een plan: er is nog niets op de nodes veranderd.{" "}
              {isAdmin ? "Lees het na en keur het goed of wijs het af." : "Een beheerder keurt het goed of wijst het af."}
            </p>
            {c.blocked && <Alert>{c.blocked}</Alert>}
            {c.full_apply && !c.blocked && (
              <Alert kind="info">
                Een eerdere revisie staat nog niet op alle nodes. Goedkeuren past daarom op elke node alle stappen van de template opnieuw toe.
              </Alert>
            )}
            {isAdmin && (
              <div className="flex flex-wrap gap-2">
                <Button type="button" disabled={!!c.blocked} onClick={() => setDialog("approve")}>
                  Goedkeuren en toepassen…
                </Button>
                <Button type="button" variant="secondary-danger" onClick={() => setDialog("reject")}>
                  Afwijzen…
                </Button>
              </div>
            )}
          </div>
        </Card>
      )}
      {c.status !== "pending" && c.status !== "superseded" && c.decided_at && (
        <Alert kind={c.status === "failed" ? "error" : c.status === "applied" ? "success" : "info"}>
          {decision(c)}
          {c.job_id && (
            <>
              {" "}
              <Link href={`/taken/detail?id=${c.job_id}`} className="font-medium underline">
                Naar de taak
              </Link>
              .
            </>
          )}
          {c.status === "failed" && (
            <>
              {" "}
              Breng de nodes bij met Opnieuw toepassen op{" "}
              <Link href={`/clusters/detail?id=${c.cluster_id}`} className="font-medium underline">
                de clusterpagina
              </Link>
              , of draai de commit terug met git revert.
            </>
          )}
        </Alert>
      )}
      {c.status === "superseded" && c.reason && <Alert kind="info">Dit plan vervalt: {c.reason}.</Alert>}
      {dialog === "approve" && <ApproveDialog c={c} onClose={() => setDialog(null)} />}
      {dialog === "reject" && <RejectDialog c={c} onClose={() => setDialog(null)} />}

      <Card>
        <dl className="grid gap-x-6 gap-y-3 text-sm sm:grid-cols-[8rem_1fr]">
          <dt className="text-slate-500">Commit</dt>
          <dd>
            <CommitLine c={c.commit} />
          </dd>
          <dt className="text-slate-500">Basis</dt>
          <dd>{basis(p)}</dd>
          {p.warnings.length > 0 && (
            <>
              <dt className="text-slate-500">Waarschuwingen</dt>
              <dd>
                <ul className="list-disc space-y-1 pl-5 text-amber-800 dark:text-amber-300">
                  {p.warnings.map((w, i) => (
                    <li key={i}>{w}</li>
                  ))}
                </ul>
              </dd>
            </>
          )}
        </dl>
      </Card>

      {(p.metadata.length > 0 || p.params.length > 0) && (
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
                    <td className={`${tdClass} text-red-700 dark:text-red-300`}>{f.from || <span className="text-slate-400">leeg</span>}</td>
                    <td className={`${tdClass} text-emerald-700 dark:text-emerald-300`}>{f.to || <span className="text-slate-400">leeg</span>}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </Card>
      )}

      {p.new_nodes.length > 0 && (
        <Card title={c.kind === "create" ? "Nodes" : "Nieuwe nodes"}>
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
                    <td className={tdClass}>{n.host || <span className="text-slate-400">–</span>}</td>
                    <td className={tdClass}>
                      {n.vm.cpu} vCPU · {mib(n.vm.memory_mib)} · {n.vm.disk_gib} GB
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          {c.kind === "create" && (p.vip || p.proxmox) && (
            <p className="mt-3 text-sm text-slate-600 dark:text-slate-400">
              {p.proxmox && `Uit Proxmox-koppeling ${p.proxmox}`}
              {p.vip && ` · VIP ${p.vip}${p.vrid ? `, VRRP-id ${p.vrid}` : ""}`}
            </p>
          )}
        </Card>
      )}

      {c.kind === "update" && (
        <Card title="Volgorde op de nodes">
          {p.nodes.length === 0 ? (
            <p className="text-sm text-slate-600 dark:text-slate-400">
              {p.no_steps
                ? "Op geen enkele node verandert een stap; alleen de gegevens in ClusterForge veranderen."
                : "Geen nodes met een wijziging."}
            </p>
          ) : (
            <ol className="space-y-4">
              {p.nodes.map((n, i) => (
                <li key={n.node_id} className="space-y-2">
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
          {c.local.length > 0 && (
            <div className="mt-4 rounded-md bg-amber-50 p-3 text-sm text-amber-900 dark:bg-amber-950 dark:text-amber-200">
              <p className="font-medium">Let op</p>
              <ul className="mt-1 list-disc space-y-1 pl-5">
                {c.local.map((l, i) => (
                  <li key={i}>{l}</li>
                ))}
              </ul>
            </div>
          )}
        </Card>
      )}
    </div>
  );
}

// decision zegt wie besliste en wat er daarna gebeurde.
function decision(c: GitChangeDetail) {
  const by = `${c.decided_by ?? "een verwijderde gebruiker"} op ${timeFmt.format(new Date(c.decided_at!))}`;
  switch (c.status) {
    case "rejected":
      return `Afgewezen door ${by}: ${c.reason}.`;
    case "applying":
      return `Goedgekeurd door ${by} als revisie ${c.revision}; de taak past de wijziging nu toe.`;
    case "applied":
      return `Goedgekeurd door ${by} en toegepast als revisie ${c.revision}${c.job_id ? "" : "; op de nodes veranderde niets"}.`;
    case "failed":
      return `Goedgekeurd door ${by} als revisie ${c.revision}, maar het toepassen mislukte: ${c.reason}.`;
  }
  return "";
}

// basis zegt waartegen het plan rekent: de revisie, de template en het
// aantal nodes erbij.
function basis(p: GitPlan) {
  const parts: string[] = [];
  if (p.kind === "update") parts.push(`spec-revisie ${p.base_revision}`);
  const t = p.template;
  parts.push(
    !t.from
      ? `template ${t.name} ${t.to}`
      : t.from === t.to
        ? `template ${t.name} ${t.to}, ongewijzigd`
        : `template ${t.name} van ${t.from} naar ${t.to}`,
  );
  if (p.kind === "update") {
    parts.push(p.new_nodes.length === 0 ? "geen nodes erbij" : `${p.new_nodes.length} ${p.new_nodes.length === 1 ? "node" : "nodes"} erbij`);
  }
  return parts.join(" · ");
}
