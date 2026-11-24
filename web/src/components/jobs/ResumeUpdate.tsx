"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import Link from "next/link";
import { useState } from "react";
import { ProdConfirm, prodConfirmed, useProdTotp } from "@/components/ProdConfirm";
import { Alert, Button, Card } from "@/components/ui";
import { api, unwrap } from "@/lib/api/client";
import type { Job } from "@/lib/proxmox";

// ResumeUpdate staat bij een gestopte clusterupdate (fase 47): verder gaan
// bij de node waar ze stopte, met dezelfde keuzes, in plaats van opnieuw
// vanaf het begin. Mislukte één Compose-project, dan kan het deze keer
// overgeslagen worden.
export function ResumeUpdate({ job }: { job: Job }) {
  const qc = useQueryClient();
  const totp = useProdTotp();
  const [confirm, setConfirm] = useState("");
  const hint = useQuery({
    queryKey: ["update-resume", job.id],
    queryFn: async () => unwrap(await api.GET("/updates/resume/{jobId}", { params: { path: { jobId: job.id } } })),
    retry: false,
  });
  const resume = useMutation({
    mutationFn: async (skip: string[]) =>
      unwrap(
        await api.POST("/updates/resume/{jobId}", {
          params: { path: { jobId: job.id } },
          body: { skip_projects: skip, ...(hint.data?.prod ? { confirm } : {}) },
        }),
      ),
    onSuccess: () => void qc.invalidateQueries({ queryKey: ["jobs"] }),
  });
  const h = hint.data;
  if (!h) return null;
  const blocked = resume.isPending || (h.prod && !prodConfirmed(h.cluster_slug, confirm, totp));
  return (
    <Card title="Verder gaan">
      {resume.data ? (
        <div className="space-y-2 text-sm">
          <Alert kind="success">De update gaat verder.</Alert>
          {resume.data.jobs.map((j) => (
            <p key={j.id}>
              <Link href={`/taken/detail?id=${j.id}`} className="font-medium text-brand-700 underline dark:text-sky-300">
                {j.title}
              </Link>
            </p>
          ))}
        </div>
      ) : (
        <div className="space-y-3 text-sm">
          <p>
            De update stopte bij <strong>{h.hostname}</strong>.
            {h.done.length > 0 && <> Al bijgewerkt: {h.done.join(", ")}; die worden niet opnieuw gedaan.</>} Verder gaan gebruikt dezelfde
            keuzes als deze update.
          </p>
          {h.prod && <ProdConfirm slug={h.cluster_slug} value={confirm} onChange={setConfirm} />}
          <div className="flex flex-wrap gap-2">
            <Button disabled={blocked} onClick={() => resume.mutate([])}>
              Hervat vanaf {h.hostname}
            </Button>
            {h.projects.map((p) => (
              <Button key={p} variant="secondary" disabled={blocked} onClick={() => resume.mutate([p])}>
                Hervat en sla Docker over voor {p}
              </Button>
            ))}
          </div>
          {h.projects.length > 0 && (
            <p className="text-xs text-slate-500">
              Overslaan geldt alleen voor deze keer: {h.projects.join(", ")} blijft op de oude versie staan; de andere projecten en de pakketten
              worden wel bijgewerkt.
            </p>
          )}
          {resume.error && <Alert>{resume.error.message}</Alert>}
        </div>
      )}
    </Card>
  );
}
