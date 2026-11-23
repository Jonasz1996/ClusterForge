"use client";

import Link from "next/link";
import { useState } from "react";
import { askConfirm } from "@/components/Confirm";
import { toast } from "@/components/Toast";
import { Alert, Badge, Button, Card, Input, PageHeader } from "@/components/ui";
import { ApiError } from "@/lib/api/client";
import { useIsAdmin } from "@/lib/inventory";
import {
  useApplyResource,
  useResources,
  useResourceSettings,
  useSetResourceSettings,
  type ResourceProposal,
  type ResourceSettings,
  type ResourceThresholds,
} from "@/lib/resources";

// Testlijst 3, N27: ClusterForge ziet het probleem, stelt een oplossing voor
// en voert die alleen uit als jij klikt. Nooit krimpen of verwijderen.

function errText(e: unknown, fallback: string) {
  return e instanceof ApiError ? e.message : fallback;
}

const kindNames: Record<ResourceProposal["kind"], string> = {
  disk: "Schijf",
  swap: "Swap",
  memory: "Geheugen",
  cpu: "CPU",
  restarts: "Herstartende container",
  dependency: "Database-afhankelijkheid",
  tip: "Tip",
};

const levelTone = { crit: "red", warn: "amber", info: "blue" } as const;
const levelName = { crit: "kritiek", warn: "waarschuwing", info: "tip" } as const;

const fields: { key: keyof ResourceThresholds; label: string; step?: string }[] = [
  { key: "disk_pct", label: "Schijf vol vanaf (%)" },
  { key: "disk_days", label: "Of vol binnen (dagen)" },
  { key: "swap_pct", label: "Swap vanaf (%)" },
  { key: "mem_pct", label: "Geheugen p95 vanaf (%)" },
  { key: "load_ratio", label: "Load15 per kern vanaf", step: "0.1" },
];

export default function ResourcesPage() {
  const [fresh, setFresh] = useState(0);
  const data = useResources(fresh);
  const apply = useApplyResource();
  const admin = useIsAdmin();
  const [jobs, setJobs] = useState<Record<string, string>>({});

  async function run(p: ResourceProposal) {
    if (!p.fix) return;
    const ok = await askConfirm(p.title, { title: p.fix.label, confirmLabel: "Uitvoeren", undo: p.fix.undo });
    if (!ok) return;
    try {
      const r = await apply.mutateAsync(p.id);
      setJobs((j) => ({ ...j, [p.id]: r.job_id }));
      toast("Taak gestart.");
    } catch (e) {
      toast(errText(e, "Starten mislukte."), "error");
    }
  }

  const items = data.data?.items ?? [];
  const envs = [...new Set(items.map((p) => p.environment || "zonder omgeving"))].sort();
  return (
    <div className="space-y-6">
      <PageHeader
        title="Resources"
        description="Volle schijven, swap, geheugen, load en containers die blijven herstarten, met een oplossing die je met één klik als taak uitvoert."
      />
      <Alert kind="info">
        ClusterForge voert niets automatisch uit en verkleint of verwijdert nooit iets. Elke oplossing is een taak met een log, en bij elke knop
        staat hoe je ze terugdraait. Een schijf vergroten controleert eerst of de opslag op de Proxmox-host genoeg vrij heeft. Certificaten die
        verlopen staan bij <Link href="/risico" className="underline">Risico</Link>.
      </Alert>
      <div className="flex items-center gap-3">
        <Button variant="secondary" onClick={() => setFresh((f) => f + 1)} disabled={data.isFetching}>
          {data.isFetching ? "Bezig…" : "Opnieuw nakijken"}
        </Button>
        <span className="text-sm text-slate-500">{items.length === 0 ? "Geen voorstellen." : `${items.length} voorstel(len)`}</span>
      </div>
      {data.isError && <Alert>{errText(data.error, "De voorstellen laden mislukte.")}</Alert>}
      {envs.map((env) => (
        <Card key={env} title={env}>
          <ul className="divide-y divide-slate-200 dark:divide-slate-800">
            {items
              .filter((p) => (p.environment || "zonder omgeving") === env)
              .map((p) => (
                <li key={p.id} className="flex flex-col gap-2 py-3 sm:flex-row sm:items-start sm:justify-between">
                  <div className="min-w-0 space-y-1">
                    <div className="flex flex-wrap items-center gap-2">
                      <Badge tone={levelTone[p.level] ?? "slate"}>{levelName[p.level] ?? p.level}</Badge>
                      <Badge>{kindNames[p.kind] ?? p.kind}</Badge>
                      <span className="font-medium">{p.title}</span>
                    </div>
                    {p.detail && <p className="whitespace-pre-line text-sm text-slate-600 dark:text-slate-400">{p.detail}</p>}
                    {p.fix && <p className="text-xs text-slate-500">Terugdraaien: {p.fix.undo}</p>}
                    {!p.fix && p.no_fix && <p className="text-xs text-slate-500">{p.no_fix}</p>}
                    <div className="flex gap-3 text-sm">
                      {p.href && (
                        <Link href={p.href} className="text-brand-700 hover:underline dark:text-sky-300">
                          Bekijken
                        </Link>
                      )}
                      {jobs[p.id] && (
                        <Link href={`/taken/detail?id=${jobs[p.id]}`} className="text-brand-700 hover:underline dark:text-sky-300">
                          Log van de taak
                        </Link>
                      )}
                    </div>
                  </div>
                  {p.fix && admin && (
                    <Button className="shrink-0" onClick={() => run(p)} disabled={apply.isPending}>
                      {p.fix.label}
                    </Button>
                  )}
                </li>
              ))}
          </ul>
        </Card>
      ))}
      <ThresholdsCard admin={admin} />
    </div>
  );
}

function ThresholdsCard({ admin }: { admin: boolean }) {
  const st = useResourceSettings();
  const save = useSetResourceSettings();
  const [edit, setEdit] = useState<ResourceSettings | null>(null);
  if (!st.data) return null;
  const cur = edit ?? st.data;
  const envs = Object.keys(cur).sort();
  return (
    <Card title="Drempels per omgeving">
      <div className="overflow-x-auto">
        <table className="w-full text-sm">
          <thead>
            <tr className="text-left text-slate-500">
              <th className="py-1 pr-3">Omgeving</th>
              {fields.map((f) => (
                <th key={f.key} className="py-1 pr-3 font-normal">
                  {f.label}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {envs.map((env) => (
              <tr key={env}>
                <td className="py-1 pr-3 font-medium">{env}</td>
                {fields.map((f) => (
                  <td key={f.key} className="py-1 pr-3">
                    <Input
                      type="number"
                      step={f.step ?? "1"}
                      disabled={!admin}
                      value={cur[env][f.key]}
                      onChange={(e) =>
                        setEdit({ ...cur, [env]: { ...cur[env], [f.key]: Number(e.target.value) } })
                      }
                      className="w-24"
                    />
                  </td>
                ))}
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {admin && edit && (
        <div className="mt-3 flex gap-2">
          <Button
            onClick={async () => {
              try {
                await save.mutateAsync(edit);
                setEdit(null);
                toast("Drempels bewaard.");
              } catch (e) {
                toast(errText(e, "Bewaren mislukte."), "error");
              }
            }}
            disabled={save.isPending}
          >
            Bewaren
          </Button>
          <Button variant="secondary" onClick={() => setEdit(null)}>
            Annuleren
          </Button>
        </div>
      )}
    </Card>
  );
}
