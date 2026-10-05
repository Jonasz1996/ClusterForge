"use client";

import Link from "next/link";
import type { ReactNode } from "react";
import { CopyBlock } from "@/components/inventory/InstallAgent";
import { ago, QueryState } from "@/components/inventory/bits";
import { Alert, Badge, Button, Card } from "@/components/ui";
import {
  driftStatuses,
  useCheckDrift,
  useClusterDrift,
  useNodeDrift,
  type ClusterDriftSummary,
  type DriftFinding,
  type DriftNode,
  type DriftReport,
} from "@/lib/drift";

const timeFmt = new Intl.DateTimeFormat("nl-BE", { dateStyle: "medium", timeStyle: "short" });

// upgradeCommand werkt cf-agent bij zonder opnieuw aan te melden.
export function upgradeCommand() {
  const origin = typeof window === "undefined" ? "https://clusterforge.example" : window.location.origin;
  return `curl -fsSL ${origin}/install/agent.sh | sudo sh -s -- --server ${origin} --upgrade`;
}

// DriftBadge staat in de lijsten naast de status: geel bij drift, grijs als
// de controle te oud of nog niet gedaan is. In orde geeft geen badge.
export function DriftBadge({ drift, href }: { drift: ClusterDriftSummary; href?: string }) {
  let badge;
  if (drift.status === "drift") {
    badge = (
      <Badge tone="amber">
        Drift · {drift.nodes_with_drift} {drift.nodes_with_drift === 1 ? "node" : "nodes"}
      </Badge>
    );
  } else if (drift.status === "unknown") {
    badge = <Badge>drift onbekend</Badge>;
  } else {
    return null;
  }
  const title =
    drift.status === "unknown"
      ? drift.checked_at
        ? `Laatste driftcontrole ${ago(drift.checked_at)}`
        : "Nog niet op drift gecontroleerd"
      : "Wijkt af van de gewenste staat";
  return href ? (
    <Link href={href} title={title}>
      {badge}
    </Link>
  ) : (
    <span title={title}>{badge}</span>
  );
}

// ClusterDriftCard toont de drift van alle nodes van een cluster.
export function ClusterDriftCard({ clusterId, isAdmin }: { clusterId: string; isAdmin: boolean }) {
  const q = useClusterDrift(clusterId);
  const check = useCheckDrift("cluster", clusterId);
  return (
    <DriftCard q={q} check={check} isAdmin={isAdmin} scope="cluster">
      {(r) =>
        r.nodes.length === 0 ? (
          <p className="text-sm text-slate-500">Dit cluster heeft nog geen nodes om te controleren.</p>
        ) : (
          <ul className="divide-y divide-slate-100 rounded-md border border-slate-200 dark:divide-slate-800 dark:border-slate-800">
            {r.nodes.map((n) => (
              <li key={n.node_id}>
                <details className="group" open={r.nodes.length === 1 || undefined}>
                  <summary className="flex cursor-pointer list-none flex-wrap items-center gap-x-3 gap-y-1 px-3 py-2.5 hover:bg-slate-50 dark:hover:bg-slate-800/50 [&::-webkit-details-marker]:hidden">
                    <span aria-hidden className="text-slate-400 transition group-open:rotate-90">
                      ›
                    </span>
                    <Link
                      href={`/nodes/detail?id=${n.node_id}`}
                      className="font-medium text-brand-700 hover:underline dark:text-sky-300"
                      onClick={(e) => e.stopPropagation()}
                    >
                      {n.hostname}
                    </Link>
                    <NodeSummary n={n} />
                  </summary>
                  <div className="px-3 pt-1 pb-4 sm:pl-8">
                    <NodeDetail n={n} />
                  </div>
                </details>
              </li>
            ))}
          </ul>
        )
      }
    </DriftCard>
  );
}

// NodeDriftCard is dezelfde kaart voor één node. Zonder gewenste staat
// toont hij niets.
export function NodeDriftCard({ nodeId, isAdmin }: { nodeId: string; isAdmin: boolean }) {
  const q = useNodeDrift(nodeId);
  const check = useCheckDrift("node", nodeId);
  if (!q.data?.source) return null;
  return (
    <DriftCard q={q} check={check} isAdmin={isAdmin} scope="node">
      {(r) =>
        r.nodes.map((n) => (
          <div key={n.node_id} className="space-y-3">
            <p className="flex flex-wrap items-center gap-x-3 gap-y-1">
              <NodeSummary n={n} />
            </p>
            <NodeDetail n={n} />
          </div>
        ))
      }
    </DriftCard>
  );
}

type Q = { data?: DriftReport; isLoading: boolean; isError: boolean; error: unknown };
type Check = { mutate: () => void; isPending: boolean; isError: boolean; error: unknown };

function DriftCard({
  q,
  check,
  isAdmin,
  scope,
  children,
}: {
  q: Q;
  check: Check;
  isAdmin: boolean;
  scope: "cluster" | "node";
  children: (r: DriftReport) => ReactNode;
}) {
  const r = q.data;
  const last = r?.nodes.reduce<string | null>((acc, n) => (n.checked_at && (!acc || n.checked_at > acc) ? n.checked_at : acc), null);
  return (
    <Card
      title={
        <span className="flex flex-wrap items-start justify-between gap-2">
          <span>
            Drift
            {r?.source && (
              <span className="mt-0.5 block text-sm font-normal text-slate-500">
                Template {r.source.template} {r.source.template_version}, spec-revisie {r.source.spec_revision} · laatste controle{" "}
                {last ? timeFmt.format(new Date(last)) : "nog niet gedaan"}
              </span>
            )}
          </span>
          {isAdmin && r?.source && (
            <Button variant="secondary" disabled={check.isPending} onClick={() => check.mutate()}>
              {check.isPending ? "Controleren…" : "Nu controleren"}
            </Button>
          )}
        </span>
      }
    >
      <QueryState q={q}>
        {r && !r.source && (
          <p className="text-sm text-slate-500">
            Dit cluster komt niet uit een template en heeft dus geen gewenste staat om mee te vergelijken.
          </p>
        )}
        {r && r.source && (
          <div className="space-y-4">
            {check.isError && (
              <Alert>{check.error instanceof Error ? check.error.message : "Controleren mislukt"}</Alert>
            )}
            {r.notes.length > 0 && (
              <ul className="space-y-1 rounded border border-amber-200 bg-amber-50 px-3 py-2 text-sm text-amber-900 dark:border-amber-900 dark:bg-amber-950 dark:text-amber-200">
                {r.notes.map((n) => (
                  <li key={n}>{n}</li>
                ))}
              </ul>
            )}
            {scope === "cluster" && (
              <p className="text-sm text-slate-600 dark:text-slate-400">
                Een controle kijkt alleen; ze verandert nooit iets op de nodes.
              </p>
            )}
            {children(r)}
          </div>
        )}
      </QueryState>
    </Card>
  );
}

// NodeSummary is de regel per node: badge, aantal afwijkingen en wanneer.
function NodeSummary({ n }: { n: DriftNode }) {
  const skippedOnly = n.status === "unknown" && n.skipped !== "";
  const st = driftStatuses[n.status];
  return (
    <>
      {skippedOnly ? <Badge>Overgeslagen</Badge> : <Badge tone={st.tone}>{st.label}</Badge>}
      {n.findings.length > 0 && (
        <span className="text-sm">
          {n.findings.length} {n.findings.length === 1 ? "afwijking" : "afwijkingen"}
          {n.drift_since && <span className="text-slate-500"> · sinds {timeFmt.format(new Date(n.drift_since))}</span>}
        </span>
      )}
      <span className="text-xs text-slate-500">
        {n.checked_at ? `gecontroleerd ${ago(n.checked_at)}` : skippedOnly ? n.skipped : "nog niet gecontroleerd"}
      </span>
    </>
  );
}

// NodeDetail toont de afwijkingen per stap, wat niet te controleren was en
// waarom een node nu overgeslagen wordt.
function NodeDetail({ n }: { n: DriftNode }) {
  const groups = groupBy(n.findings);
  return (
    <div className="space-y-3 text-sm">
      {n.agent_too_old && (
        <div className="space-y-2 rounded border border-amber-200 bg-amber-50 px-3 py-2 text-amber-900 dark:border-amber-900 dark:bg-amber-950 dark:text-amber-200">
          <p>
            <span className="font-medium">Agent te oud voor driftcontrole.</span> Werk cf-agent bij met dit commando op de
            node; de aanmelding blijft staan.
          </p>
          <CopyBlock text={upgradeCommand()} />
        </div>
      )}
      {n.skipped && !n.agent_too_old && n.checked_at && (
        <p className="text-slate-500">Nu overgeslagen: {n.skipped}. De laatste uitkomst blijft staan.</p>
      )}
      {n.status === "error" && n.error && (
        <Alert>
          Controle mislukt: {n.error}
          {n.findings.length > 0 && " Hieronder staan de afwijkingen van de laatste geslaagde controle."}
        </Alert>
      )}
      {n.status === "none" && (
        <p className="text-slate-500">Deze node staat niet in de specificatie van het cluster, dus er is niets om mee te vergelijken.</p>
      )}
      {n.status === "in_sync" && <p className="text-slate-500">Alles op deze node is zoals de template het wil.</p>}

      {groups.length > 0 && (
        <ul className="space-y-3">
          {groups.map((g) => (
            <li key={g.step}>
              <div className="font-medium break-all">{g.title}</div>
              <dl className="mt-1 grid grid-cols-[5.5rem_1fr] gap-x-3 gap-y-0.5">
                <dt className="text-slate-500">Verwacht</dt>
                <dd className="break-words">{g.findings.map(expectedText).join(", ")}</dd>
                <dt className="text-slate-500">Werkelijk</dt>
                <dd className="break-words">{g.findings.map(actualText).join(", ")}</dd>
              </dl>
            </li>
          ))}
        </ul>
      )}

      {n.unchecked.length > 0 && (
        <div className="text-xs text-slate-500">
          <div className="font-medium">Niet gecontroleerd</div>
          <ul className="mt-1 space-y-0.5">
            {n.unchecked.map((u) => (
              <li key={u.step + u.reason} className="break-words">
                {u.title}: {u.reason}
              </li>
            ))}
          </ul>
        </div>
      )}
    </div>
  );
}

const aspectPrefix: Partial<Record<DriftFinding["aspect"], string>> = {
  mode: "rechten ",
  owner: "eigenaar ",
  group: "groep ",
};

function expectedText(f: DriftFinding) {
  return (aspectPrefix[f.aspect] ?? "") + f.expected;
}

function actualText(f: DriftFinding) {
  const extra = [f.detail, f.mtime && `gewijzigd op de node om ${timeFmt.format(new Date(f.mtime))}`].filter(Boolean);
  return (aspectPrefix[f.aspect] ?? "") + f.actual + (extra.length ? ` (${extra.join(", ")})` : "");
}

function groupBy(findings: DriftFinding[]) {
  const out: { step: string; title: string; findings: DriftFinding[] }[] = [];
  for (const f of findings) {
    const g = out.find((x) => x.step === f.step);
    if (g) g.findings.push(f);
    else out.push({ step: f.step, title: f.title, findings: [f] });
  }
  return out;
}
