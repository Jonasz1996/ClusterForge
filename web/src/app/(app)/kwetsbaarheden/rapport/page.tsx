"use client";

import Link from "next/link";
import { useRouter, useSearchParams } from "next/navigation";
import { Suspense } from "react";
import { tableClass, tdClass, thClass } from "@/components/inventory/bits";
import { Alert, Badge, Button, Card, Field, PageHeader, Select, cx } from "@/components/ui";
import { scoreTone, severities, severityTone, useSecurityReport, useSecurityScopes, type SecurityFinding, type SecurityReport } from "@/lib/security";

// Rapport per scope (fase 27 M5): wat een pentest-rapport over je eigen
// netwerk zou zeggen, uit wat ClusterForge al weet. Af te drukken of als
// PDF te bewaren.

const kindLabel: Record<string, string> = { cve: "Pakketten", hardening: "Verharding", netwerk: "Netwerk" };

export default function ReportPage() {
  return (
    <Suspense>
      <Report />
    </Suspense>
  );
}

function Report() {
  const params = useSearchParams();
  const router = useRouter();
  const scopes = useSecurityScopes();
  const scopeId = params.get("scope") ?? scopes.data?.items[0]?.id ?? "";
  const rep = useSecurityReport(scopeId);
  return (
    <div className="space-y-6">
      <div className="print:hidden">
        <PageHeader
          title="Rapport"
          description="Per eigen scope: de laatste scan, de nodes met hun kwetsbaarheden en verharding, en alle open bevindingen met de oplossing. Druk af of bewaar als PDF."
          actions={rep.data && <Button onClick={() => window.print()}>Afdrukken of PDF</Button>}
        />
      </div>
      {scopes.data && scopes.data.items.length === 0 && (
        <Alert kind="info">
          Nog geen eigen scopes. Maak er een bij{" "}
          <Link href="/kwetsbaarheden/scopes" className="font-medium underline">
            Eigen scopes
          </Link>
          .
        </Alert>
      )}
      {scopes.data && scopes.data.items.length > 1 && (
        <div className="max-w-sm print:hidden">
          <Field label="Scope" htmlFor="rapport-scope">
            <Select id="rapport-scope" value={scopeId} onChange={(e) => router.replace(`/kwetsbaarheden/rapport?scope=${e.target.value}`)}>
              {scopes.data.items.map((s) => (
                <option key={s.id} value={s.id}>
                  {s.name}
                </option>
              ))}
            </Select>
          </Field>
        </div>
      )}
      {rep.isError && <Alert>Rapport maken mislukte.</Alert>}
      {rep.data && <ReportBody r={rep.data} />}
    </div>
  );
}

function ReportBody({ r }: { r: SecurityReport }) {
  const open = r.findings.filter((f) => f.status === "open");
  const accepted = r.findings.filter((f) => f.status === "aanvaard");
  const s = r.summary;
  return (
    <article className="space-y-6">
      <header>
        <h2 className="text-2xl font-semibold">Beveiligingsrapport {r.scope.name}</h2>
        <p className="text-sm text-slate-500">
          {r.scope.cidrs.join(", ")}
          {r.scope.exclude.length > 0 && ` · zonder ${r.scope.exclude.join(", ")}`} · gemaakt {new Date(r.generated_at).toLocaleString("nl-BE")}
        </p>
      </header>

      <Card title="Samenvatting">
        <dl className="grid grid-cols-2 gap-4 sm:grid-cols-4">
          <Stat label="Open bevindingen" value={s.open} tone={s.counts.kritiek || s.counts.hoog ? "red" : s.open ? "amber" : "green"} />
          <Stat label="Actief misbruikt (KEV)" value={s.kev} tone={s.kev ? "red" : "green"} />
          <Stat label="Gemiddelde verharding" value={s.hardening_avg === null ? "–" : `${s.hardening_avg}%`} tone={scoreTone(s.hardening_avg)} />
          <Stat label="Bereikbare hosts" value={r.last_run ? `${s.hosts_up} (${s.unknown_hosts} onbekend)` : "nog niet gescand"} />
        </dl>
        <div className="mt-4 flex flex-wrap gap-2">
          {severities
            .filter((sev) => s.counts[sev])
            .map((sev) => (
              <Badge key={sev} tone={severityTone[sev]}>
                {s.counts[sev]} {sev}
              </Badge>
            ))}
          {s.accepted > 0 && <Badge tone="slate">{s.accepted} aanvaard</Badge>}
        </div>
        <p className="mt-3 text-sm text-slate-600 dark:text-slate-400">
          {r.last_run
            ? `Laatste netwerkscan ${new Date(r.last_run.started_at).toLocaleString("nl-BE")} door ${r.last_run.scanner_name}: ${s.open_ports} open poorten.`
            : "Er is nog geen netwerkscan van deze scope; het rapport steunt alleen op de agents."}{" "}
          Alleen vaststellen, niet aanvallen: kwetsbaarheden volgens de Debian Security Tracker en CISA KEV, verharding volgens de controles van
          ClusterForge, en diensten volgens nmap met veilige scripts.
        </p>
      </Card>

      <Card title={`Nodes in deze scope (${r.nodes.length})`}>
        {r.nodes.length === 0 ? (
          <p className="text-sm text-slate-500">Geen ClusterForge-nodes met een adres in deze scope.</p>
        ) : (
          <div className="overflow-x-auto">
            <table className={tableClass}>
              <thead>
                <tr>
                  <th className={thClass}>Node</th>
                  <th className={thClass}>Adres</th>
                  <th className={thClass}>Systeem</th>
                  <th className={thClass}>Verharding</th>
                  <th className={thClass}>Open</th>
                </tr>
              </thead>
              <tbody>
                {r.nodes.map((n) => (
                  <tr key={n.node_id} className="border-t border-slate-100 dark:border-slate-800">
                    <td className={tdClass}>
                      <div className="font-medium">{n.hostname}</div>
                      {n.cluster_name && <div className="text-xs text-slate-500">{n.cluster_name}</div>}
                    </td>
                    <td className={cx(tdClass, "font-mono text-xs")}>{n.address}</td>
                    <td className={tdClass}>{n.os || "–"}</td>
                    <td className={tdClass}>
                      {n.hardening_score === null ? "–" : <Badge tone={scoreTone(n.hardening_score)}>{n.hardening_score}%</Badge>}
                    </td>
                    <td className={tdClass}>
                      <div className="flex flex-wrap gap-1">
                        {n.kev > 0 && <Badge tone="red">{n.kev} KEV</Badge>}
                        {severities
                          .filter((sev) => n.counts[sev])
                          .map((sev) => (
                            <Badge key={sev} tone={severityTone[sev]}>
                              {n.counts[sev]} {sev}
                            </Badge>
                          ))}
                        {Object.keys(n.counts).length === 0 && <span className="text-slate-500">geen</span>}
                      </div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Card>

      {r.last_run?.result && r.last_run.result.hosts.some((h) => !h.node_id) && (
        <Card title="Toestellen die ClusterForge niet kent">
          <ul className="space-y-1 text-sm">
            {r.last_run.result.hosts
              .filter((h) => !h.node_id)
              .map((h) => (
                <li key={h.addr}>
                  <span className="font-mono">{h.addr}</span>
                  {(h.vendor || h.hostname) && <span className="text-slate-500"> · {[h.hostname, h.vendor].filter(Boolean).join(" · ")}</span>}
                  {h.ports.length > 0 && <span className="text-slate-500"> · poorten {h.ports.map((p) => p.port).join(", ")}</span>}
                </li>
              ))}
          </ul>
        </Card>
      )}

      <Card title={`Open bevindingen (${open.length})`}>
        {open.length === 0 ? <p className="text-sm text-slate-500">Geen open bevindingen.</p> : <FindingList list={open} />}
      </Card>
      {accepted.length > 0 && (
        <Card title={`Aanvaard risico (${accepted.length})`}>
          <FindingList list={accepted} />
        </Card>
      )}
    </article>
  );
}

function Stat({ label, value, tone }: { label: string; value: string | number; tone?: string }) {
  return (
    <div>
      <dt className="text-xs uppercase tracking-wide text-slate-500">{label}</dt>
      <dd
        className={cx(
          "mt-1 text-xl font-semibold tabular-nums",
          tone === "red" && "text-red-600",
          tone === "amber" && "text-amber-600",
          tone === "green" && "text-emerald-600",
        )}
      >
        {value}
      </dd>
    </div>
  );
}

function FindingList({ list }: { list: SecurityFinding[] }) {
  return (
    <ol className="space-y-3">
      {list.map((f) => (
        <li key={f.id} className="break-inside-avoid border-t border-slate-100 pt-3 first:border-0 first:pt-0 dark:border-slate-800">
          <div className="flex flex-wrap items-center gap-2">
            <Badge tone={severityTone[f.severity]}>{f.severity}</Badge>
            <span className="text-xs uppercase tracking-wide text-slate-500">{kindLabel[f.kind] ?? f.kind}</span>
            <span className="font-medium">{f.title}</span>
          </div>
          <div className="mt-1 text-sm text-slate-600 dark:text-slate-400">
            {f.hostname || f.subject.replace(/^ip:/, "")}
            {f.cluster_name && ` · ${f.cluster_name}`} · {f.summary}
          </div>
          <div className="mt-1 text-sm">
            <span className="font-medium">Oplossing:</span> {f.fix}
          </div>
          {f.status === "aanvaard" && (
            <div className="mt-1 text-xs text-slate-500">
              Aanvaard door {f.accepted_by || "?"}: {f.accepted_note}
            </div>
          )}
        </li>
      ))}
    </ol>
  );
}
