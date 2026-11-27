"use client";

import Link from "next/link";
import { useState } from "react";
import { Alert, Badge, Button, Card, Select } from "@/components/ui";
import { usePublishAuto, useRetryAuto, useSaveAutoRule, type AutoItem, type PublishOverview } from "@/lib/publish";

const stateText: Record<AutoItem["state"], [string, "slate" | "green" | "amber" | "red" | "blue"]> = {
  publishing: ["bezig", "blue"],
  published: ["gepubliceerd", "green"],
  failed: ["mislukt", "red"],
  removing: ["opruimen", "amber"],
  removed: ["opgeruimd", "slate"],
};

// AutoPublish is het blok "Automatisch publiceren" (fase 48): per cluster
// aanzetten; elke 5 minuten publiceert ClusterForge één nieuwe webdienst met
// de gewone taak (kopie, controles, terugdraaien) en ruimt hij op bij decomm.
export function AutoPublish({ ov, admin }: { ov: PublishOverview; admin: boolean }) {
  const q = usePublishAuto();
  const save = useSaveAutoRule();
  const retry = useRetryAuto();
  const [pick, setPick] = useState("");
  const v = q.data;
  if (!v) return null;
  const clusterRules = v.rules.filter((r) => r.cluster_id);
  const free = ov.clusters.filter((c) => !clusterRules.some((r) => r.cluster_id === c.id));
  const todo = v.candidates.filter((c) => !c.skip);
  const skipped = v.candidates.filter((c) => c.skip);
  return (
    <Card title="Automatisch publiceren">
      <p className="mb-3 text-sm text-slate-600 dark:text-slate-400">
        Zet het aan per cluster. Elke 5 minuten krijgt een nieuwe webdienst van dat cluster een proxy host <code>naam.{ov.settings.domain}</code>{" "}
        met het sjabloon en een certificaat, en een provider en app in Authentik (behalve apps met een eigen login). Dat gebeurt met de gewone taak:
        kopie van NPM vooraf, controle erna, terugdraaien bij een fout. Alles staat in <Link href="/taken?who=all" className="underline">Taken</Link>.
        ClusterForge gokt nooit: een dienst zonder exact doel (VIP of één node) of met een naam die al bestaat, slaat hij over met de reden erbij. Bij
        decomm ruimt hij op wat hij zelf maakte, na een kopie van de host.
      </p>
      {v.ready && <Alert kind="info">{v.ready}</Alert>}
      {save.isError && <Alert>{(save.error as Error).message}</Alert>}
      <ul className="divide-y divide-slate-100 text-sm dark:divide-slate-800">
        {clusterRules.map((r) => (
          <li key={r.id} className="flex flex-wrap items-center justify-between gap-2 py-1.5">
            <span>
              {r.cluster_name} {r.enabled ? <Badge tone="green">aan</Badge> : <Badge>uit</Badge>}
              {r.enabled && !r.cleanup && <span className="ml-2 text-xs text-slate-500">niet opruimen bij decomm</span>}
            </span>
            {admin && (
              <span className="flex gap-3 text-xs">
                <label className="flex items-center gap-1">
                  <input
                    type="checkbox"
                    checked={r.cleanup}
                    onChange={(e) => save.mutate({ cluster_id: r.cluster_id, enabled: r.enabled, cleanup: e.target.checked })}
                  />
                  opruimen bij decomm
                </label>
                <button
                  type="button"
                  className="text-brand-700 underline dark:text-sky-300"
                  onClick={() => save.mutate({ cluster_id: r.cluster_id, enabled: !r.enabled, cleanup: r.cleanup })}
                >
                  {r.enabled ? "uitzetten" : "aanzetten"}
                </button>
              </span>
            )}
          </li>
        ))}
      </ul>
      {admin && free.length > 0 && (
        <div className="mt-3 flex flex-wrap items-center gap-2 text-sm">
          <Select value={pick} onChange={(e) => setPick(e.target.value)} className="max-w-xs" aria-label="Cluster">
            <option value="">Kies een cluster</option>
            {free.map((c) => (
              <option key={c.id} value={c.id}>
                {c.name}
              </option>
            ))}
          </Select>
          <Button
            variant="secondary"
            disabled={!pick || save.isPending}
            onClick={() => save.mutate({ cluster_id: pick, enabled: true }, { onSuccess: () => setPick("") })}
          >
            Automatisch publiceren aanzetten
          </Button>
        </div>
      )}
      {(todo.length > 0 || skipped.length > 0) && (
        <div className="mt-4 text-sm">
          <h3 className="font-medium">Volgende beurten</h3>
          <ul className="mt-1 space-y-1">
            {todo.map((c) => (
              <li key={c.service_key}>
                <Badge tone="blue">wordt gepubliceerd</Badge> {c.domain} → {c.target}
                {c.authentik && <span className="ml-1 text-xs text-slate-500">achter Authentik</span>}
              </li>
            ))}
            {skipped.map((c) => (
              <li key={c.service_key} className="text-slate-500">
                <Badge>overgeslagen</Badge> {c.cluster_name} · {c.name} ({c.target}): {c.skip}
              </li>
            ))}
          </ul>
        </div>
      )}
      {v.items.length > 0 && (
        <div className="mt-4 text-sm">
          <h3 className="font-medium">Wat ClusterForge vanzelf deed</h3>
          <ul className="mt-1 divide-y divide-slate-100 dark:divide-slate-800">
            {v.items.map((it) => {
              const [txt, tone] = stateText[it.state];
              return (
                <li key={it.service_key} className="flex flex-wrap items-center justify-between gap-2 py-1.5">
                  <span>
                    <Badge tone={tone}>{txt}</Badge> {it.domain}
                    {it.cluster_name && <span className="ml-2 text-xs text-slate-500">{it.cluster_name}</span>}
                    {it.error && <div className="text-xs text-red-600 dark:text-red-400">{it.error}</div>}
                  </span>
                  <span className="flex gap-3 text-xs">
                    {it.job_id && (
                      <Link href={`/taken/detail?id=${it.job_id}`} className="underline">
                        taak
                      </Link>
                    )}
                    {it.has_backup && (
                      <a href={`/api/v1/publish/auto/backup?service_key=${encodeURIComponent(it.service_key)}`} className="underline" download>
                        kopie van de host
                      </a>
                    )}
                    {admin && it.state === "failed" && (
                      <button type="button" className="text-brand-700 underline dark:text-sky-300" onClick={() => retry.mutate(it.service_key)}>
                        opnieuw
                      </button>
                    )}
                  </span>
                </li>
              );
            })}
          </ul>
        </div>
      )}
    </Card>
  );
}
