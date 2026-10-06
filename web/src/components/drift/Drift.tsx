"use client";

import Link from "next/link";
import { useState, type ReactNode } from "react";
import { BaselineWizard, IgnoredList, IgnoreForm } from "@/components/drift/Baseline";
import { RemediateDialog } from "@/components/drift/Remediate";
import { actualText, expectedText } from "@/components/drift/text";
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
  type DriftSource,
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
    <DriftCard q={q} check={check} isAdmin={isAdmin} clusterId={clusterId} scope="cluster">
      {(r, recapture) =>
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
                    <NodeDetail n={n} source={r.source} clusterId={clusterId} isAdmin={isAdmin} recapture={recapture} />
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
export function NodeDriftCard({ nodeId, clusterId, isAdmin }: { nodeId: string; clusterId: string; isAdmin: boolean }) {
  const q = useNodeDrift(nodeId);
  const check = useCheckDrift("node", nodeId);
  if (!q.data?.source) return null;
  return (
    <DriftCard q={q} check={check} isAdmin={isAdmin} clusterId={clusterId} nodeId={nodeId} scope="node">
      {(r, recapture) =>
        r.nodes.map((n) => (
          <div key={n.node_id} className="space-y-3">
            <p className="flex flex-wrap items-center gap-x-3 gap-y-1">
              <NodeSummary n={n} />
            </p>
            <NodeDetail n={n} source={r.source} clusterId={clusterId} isAdmin={isAdmin} recapture={recapture} />
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
  clusterId,
  nodeId,
  scope,
  children,
}: {
  q: Q;
  check: Check;
  isAdmin: boolean;
  clusterId: string;
  nodeId?: string;
  scope: "cluster" | "node";
  children: (r: DriftReport, recapture: (nodeId: string) => void) => ReactNode;
}) {
  // De wizard: null is dicht, "" voor het hele cluster, anders één node.
  const [wizard, setWizard] = useState<string | null>(null);
  const r = q.data;
  const src = r?.source ?? null;
  const [remediate, setRemediate] = useState(false);
  // Herstellen kan alleen bij een template: van een baseline kent
  // ClusterForge bij bestanden alleen een vingerafdruk.
  const canRemediate = isAdmin && src?.kind === "template" && (r?.nodes.some((n) => n.findings.some((f) => !f.ignored)) ?? false);
  const last = r?.nodes.reduce<string | null>((acc, n) => (n.checked_at && (!acc || n.checked_at > acc) ? n.checked_at : acc), null);
  const lastText = ` · laatste controle ${last ? timeFmt.format(new Date(last)) : "nog niet gedaan"}`;
  return (
    <Card
      title={
        <span className="flex flex-wrap items-start justify-between gap-2">
          <span>
            Drift
            {src && (
              <span className="mt-0.5 block text-sm font-normal text-slate-500">
                {src.kind === "baseline"
                  ? `Baseline van ${src.baseline_at ? timeFmt.format(new Date(src.baseline_at)) : "onbekende datum"}, revisie ${src.spec_revision}`
                  : `Template ${src.template} ${src.template_version}, spec-revisie ${src.spec_revision}`}
                {lastText}
              </span>
            )}
          </span>
          {isAdmin && src && (
            <span className="flex flex-wrap gap-2">
              {src.kind === "baseline" && scope === "cluster" && (
                <Button variant="secondary" onClick={() => setWizard("")}>
                  Baseline vastleggen
                </Button>
              )}
              {canRemediate && <Button onClick={() => setRemediate(true)}>Herstellen…</Button>}
              <Button variant="secondary" disabled={check.isPending} onClick={() => check.mutate()}>
                {check.isPending ? "Controleren…" : "Nu controleren"}
              </Button>
            </span>
          )}
        </span>
      }
    >
      <QueryState q={q}>
        {r && !src && (
          <div className="space-y-3 text-sm">
            <p className="text-slate-600 dark:text-slate-400">
              Dit cluster komt niet uit een template en heeft dus nog geen gewenste staat. Leg een baseline vast: ClusterForge
              onthoudt dan hoe de gekozen pakketten, services en bestanden er nu bij staan, en meldt elke afwijking.
            </p>
            {isAdmin && <Button onClick={() => setWizard("")}>Baseline vastleggen</Button>}
          </div>
        )}
        {r && src && (
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
                {src.kind === "baseline" && src.items && <>Vastgelegd: {itemsText(src.items)}. </>}
                Een controle kijkt alleen; ze verandert nooit iets op de nodes.
              </p>
            )}
            {children(r, (id) => setWizard(id))}
            <IgnoredList clusterId={clusterId} nodeId={nodeId} isAdmin={isAdmin} />
          </div>
        )}
      </QueryState>
      {wizard !== null && (
        <BaselineWizard clusterId={clusterId} source={src} node={wizard || undefined} onClose={() => setWizard(null)} />
      )}
      {remediate && r && <RemediateDialog clusterId={clusterId} report={r} onClose={() => setRemediate(false)} />}
    </Card>
  );
}

function itemsText(it: NonNullable<DriftSource["items"]>) {
  const parts = [
    it.packages.length > 0 && `${it.packages.length} ${it.packages.length === 1 ? "pakket" : "pakketten"}`,
    it.services.length > 0 && `${it.services.length} ${it.services.length === 1 ? "service" : "services"}`,
    it.files.length > 0 && `${it.files.length} ${it.files.length === 1 ? "bestand of map" : "bestanden en mappen"}`,
  ].filter(Boolean);
  return parts.join(", ");
}

// NodeSummary is de regel per node: badge, aantal afwijkingen en wanneer.
function NodeSummary({ n }: { n: DriftNode }) {
  const skippedOnly = n.status === "unknown" && n.skipped !== "";
  const st = driftStatuses[n.status];
  const active = n.findings.filter((f) => !f.ignored).length;
  const ignored = n.findings.length - active;
  return (
    <>
      {skippedOnly ? <Badge>Overgeslagen</Badge> : <Badge tone={st.tone}>{st.label}</Badge>}
      {active > 0 && (
        <span className="text-sm">
          {active} {active === 1 ? "afwijking" : "afwijkingen"}
          {n.drift_since && <span className="text-slate-500"> · sinds {timeFmt.format(new Date(n.drift_since))}</span>}
        </span>
      )}
      {ignored > 0 && <span className="text-xs text-slate-500">{ignored} genegeerd</span>}
      <span className="text-xs text-slate-500">
        {n.checked_at ? `gecontroleerd ${ago(n.checked_at)}` : skippedOnly ? n.skipped : "nog niet gecontroleerd"}
      </span>
    </>
  );
}

// NodeDetail toont de afwijkingen per stap, wat niet te controleren was en
// waarom een node nu overgeslagen wordt.
function NodeDetail({
  n,
  source,
  clusterId,
  isAdmin,
  recapture,
}: {
  n: DriftNode;
  source: DriftSource | null;
  clusterId: string;
  isAdmin: boolean;
  recapture: (nodeId: string) => void;
}) {
  const [ignoring, setIgnoring] = useState<string | null>(null);
  const groups = groupBy(n.findings);
  const baseline = source?.kind === "baseline";
  const anyIgnored = n.findings.some((f) => f.ignored);
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
      {n.status === "none" &&
        (baseline ? (
          <p className="text-slate-500">Deze node heeft nog geen baseline, dus er is niets om mee te vergelijken.</p>
        ) : (
          <p className="text-slate-500">Deze node staat niet in de specificatie van het cluster, dus er is niets om mee te vergelijken.</p>
        ))}
      {n.status === "in_sync" && (
        <p className="text-slate-500">
          Alles op deze node is {baseline ? "zoals vastgelegd in de baseline" : "zoals de template het wil"}
          {anyIgnored && ", op de genegeerde afwijkingen na"}.
        </p>
      )}
      {baseline && isAdmin && (
        <div>
          <Button variant="secondary" className="px-2.5 py-1 text-xs" onClick={() => recapture(n.node_id)}>
            {n.status === "none" ? "Baseline vastleggen" : "Opnieuw vastleggen"}
          </Button>
        </div>
      )}

      {groups.length > 0 && (
        <ul className="space-y-3">
          {groups.map((g) => {
            const ignored = g.findings.every((f) => f.ignored);
            return (
              <li key={g.step}>
                <div className={ignored ? "opacity-60" : undefined}>
                  <div className="flex flex-wrap items-center gap-2">
                    <span className="font-medium break-all">{g.title}</span>
                    {ignored && <Badge>genegeerd</Badge>}
                    {isAdmin && !ignored && ignoring !== g.step && (
                      <button
                        type="button"
                        className="text-xs text-brand-700 hover:underline dark:text-sky-300"
                        onClick={() => setIgnoring(g.step)}
                      >
                        Negeren…
                      </button>
                    )}
                  </div>
                  <dl className="mt-1 grid grid-cols-[5.5rem_1fr] gap-x-3 gap-y-0.5">
                    <dt className="text-slate-500">Verwacht</dt>
                    <dd className="break-words">{g.findings.map(expectedText).join(", ")}</dd>
                    <dt className="text-slate-500">Werkelijk</dt>
                    <dd className="break-words">{g.findings.map(actualText).join(", ")}</dd>
                  </dl>
                </div>
                {ignoring === g.step && (
                  <IgnoreForm
                    clusterId={clusterId}
                    nodeId={n.node_id}
                    hostname={n.hostname}
                    step={g.step}
                    onDone={() => setIgnoring(null)}
                  />
                )}
              </li>
            );
          })}
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

function groupBy(findings: DriftFinding[]) {
  const out: { step: string; title: string; findings: DriftFinding[] }[] = [];
  for (const f of findings) {
    const g = out.find((x) => x.step === f.step);
    if (g) g.findings.push(f);
    else out.push({ step: f.step, title: f.title, findings: [f] });
  }
  return out;
}
