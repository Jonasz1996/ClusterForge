"use client";

import Link from "next/link";
import { useRouter } from "next/navigation";
import { useState } from "react";
import { Alert, Button, Card, Input } from "@/components/ui";
import { useCleanupFailedDeploy, useFailedDeploy } from "@/lib/deployFailed";
import type { Job } from "@/lib/proxmox";

// FailedDeploy staat bij een mislukte uitrol (fase 48, 4.2): opruimen wat
// de uitrol maakte, zodat slug en adressen weer vrij zijn, en dan opnieuw
// met dezelfde of andere instellingen.
export function FailedDeploy({ job }: { job: Job }) {
  const router = useRouter();
  const f = useFailedDeploy(job.id);
  const clean = useCleanupFailedDeploy();
  const [confirm, setConfirm] = useState("");
  const [guests, setGuests] = useState(true);
  const d = f.data;
  if (!d) return null;
  const wizard = `/clusters/uitrollen?van=${d.job_id}`;
  const guestCount = d.nodes.filter((n) => !n.gone && !n.existing && n.vmid).length;
  const existing = d.nodes.some((n) => n.existing);

  async function run(next: boolean) {
    const j = await clean.mutateAsync({ jobId: d!.job_id, confirm: confirm.trim(), guests });
    router.push(next ? `${wizard}&opruimen=${j.id}` : `/taken/detail?id=${j.id}`);
  }

  if (!d.can_clean) {
    if (d.cluster_exists) return d.why ? <Alert kind="info">Opruimen kan nu niet: {d.why}.</Alert> : null;
    return (
      <Card title="Opnieuw uitrollen">
        <div className="space-y-2 text-sm">
          <p>
            Deze mislukte uitrol is opgeruimd
            {d.cleanup_job_id && (
              <>
                {" "}
                (
                <Link href={`/taken/detail?id=${d.cleanup_job_id}`} className="underline">
                  taak
                </Link>
                )
              </>
            )}
            . Rol opnieuw uit met dezelfde instellingen; pas aan wat nodig is.
          </p>
          <Link href={wizard} className="font-medium text-brand-700 underline dark:text-sky-300">
            Naar de wizard met deze instellingen
          </Link>
        </div>
      </Card>
    );
  }

  return (
    <Card title="Mislukte uitrol">
      <div className="space-y-3 text-sm">
        <p>
          Opnieuw proberen gaat verder met dezelfde instellingen. Wil je iets anders (een ander adres, een andere host of template), ruim
          dan eerst op: ClusterForge haalt {guestCount > 0 ? "de CT's en VM's, " : ""}de nodes, het VIP en het cluster <strong>{d.cluster_name}</strong>{" "}
          weg, zodat de slug <code>{d.slug}</code> en de adressen weer vrij zijn. De wizard staat daarna klaar met dezelfde instellingen.
        </p>
        <ul className="list-disc pl-5 text-slate-600 dark:text-slate-400">
          {d.nodes
            .filter((n) => !n.gone)
            .map((n) => (
              <li key={n.id}>
                {n.hostname}
                {n.address && <> ({n.address})</>}
                {n.existing
                  ? ": bestond al, gaat alleen uit het cluster"
                  : n.vmid
                    ? `: ${n.vmid} op ${n.host ?? "?"}`
                    : ": nog geen CT of VM"}
              </li>
            ))}
        </ul>
        {guestCount > 0 && (
          <label className="flex items-start gap-2">
            <input type="checkbox" className="mt-1" checked={guests} onChange={(e) => setGuests(e.target.checked)} />
            <span>
              Ook de {guestCount} CT&apos;s en VM&apos;s weghalen (uitzetten en verwijderen via de agent op hun host). Alleen een gast met
              de naam van de node; een andere laat ClusterForge staan.
            </span>
          </label>
        )}
        {existing && (
          <p className="text-slate-600 dark:text-slate-400">
            Bestaande nodes blijven bestaan; wat de uitrol erop installeerde, blijft staan.
          </p>
        )}
        <label className="block max-w-sm">
          <span className="text-slate-600 dark:text-slate-400">
            Tik <code>{d.slug}</code> over om op te ruimen
          </span>
          <Input value={confirm} onChange={(e) => setConfirm(e.target.value)} placeholder={d.slug} />
        </label>
        <div className="flex flex-wrap gap-2">
          <Button disabled={clean.isPending || confirm.trim() !== d.slug} onClick={() => void run(true).catch(() => {})}>
            Opruimen en opnieuw met andere instellingen
          </Button>
          <Button variant="secondary" disabled={clean.isPending || confirm.trim() !== d.slug} onClick={() => void run(false).catch(() => {})}>
            Alleen opruimen
          </Button>
        </div>
        {clean.error && <Alert>{clean.error.message}</Alert>}
      </div>
    </Card>
  );
}
