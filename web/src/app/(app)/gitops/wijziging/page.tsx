"use client";

import Link from "next/link";
import { useSearchParams } from "next/navigation";
import { Suspense, useState } from "react";
import { ChangeStatusBadge, CommitLine } from "@/components/gitops/bits";
import { PlanFields, PlanNewNodes, PlanOrder } from "@/components/gitops/Plan";
import { ApproveDialog, RejectDialog } from "@/components/gitops/Decide";
import { EnvBadge, QueryState } from "@/components/inventory/bits";
import { Alert, Button, Card, PageHeader } from "@/components/ui";
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
            {c.blocked && (
              <Alert>
                {c.blocked}
                {c.blocked_code === "no_server_url" && (
                  <>
                    {" "}
                    <Link href="/gitops" className="font-medium underline">
                      Naar de koppeling
                    </Link>
                    .
                  </>
                )}
              </Alert>
            )}
            {c.kind === "create" && !c.blocked && (
              <Alert kind="info">
                Goedkeuren maakt het cluster met spec-revisie 1 uit deze commit en rolt het uit, zoals bij Uitrollen: VM&apos;s maken,
                agents aanmelden, software installeren en controleren.
              </Alert>
            )}
            {c.full_apply && !c.blocked && (
              <Alert kind="info">
                Een eerdere revisie staat nog niet op alle nodes. Goedkeuren past daarom op elke node alle stappen van de template opnieuw toe.
              </Alert>
            )}
            {isAdmin && (
              <div className="flex flex-wrap gap-2">
                <Button type="button" disabled={!!c.blocked} onClick={() => setDialog("approve")}>
                  {c.kind === "create" ? "Goedkeuren en uitrollen…" : "Goedkeuren en toepassen…"}
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
          {c.status === "failed" && c.kind === "create" && (
            <> Open de taak en kies Opnieuw proberen; stappen die al lukten, slaat ze over.</>
          )}
          {c.status === "failed" && c.kind !== "create" && (
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

      <PlanFields plan={p} />
      <PlanNewNodes plan={p} kind={c.kind} />
      {c.kind === "update" && <PlanOrder plan={p} local={c.local} />}
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
      if (c.kind === "create") return `Goedgekeurd door ${by}; de uitrol loopt.`;
      return `Goedgekeurd door ${by} als revisie ${c.revision}; de taak past de wijziging nu toe.`;
    case "applied":
      if (c.kind === "create") return `Goedgekeurd door ${by} en uitgerold als revisie ${c.revision}.`;
      return `Goedgekeurd door ${by} en toegepast als revisie ${c.revision}${c.job_id ? "" : "; op de nodes veranderde niets"}.`;
    case "failed":
      if (c.kind === "create") return `Goedgekeurd door ${by}, maar de uitrol mislukte: ${c.reason}.`;
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
