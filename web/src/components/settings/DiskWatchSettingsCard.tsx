"use client";

import { useState, type FormEvent } from "react";
import { toast } from "@/components/Toast";
import { Alert, Button, Card, Field, Input } from "@/components/ui";
import { ApiError } from "@/lib/api/client";
import { useDiskSettings, useSetDiskSettings, type DiskWatchSettings } from "@/lib/disk";

/** Instellingen → Schijfbewaking (fase 41). */
export function DiskWatchSettingsCard({ isAdmin }: { isAdmin: boolean }) {
  const q = useDiskSettings();
  return (
    <Card title="Schijfbewaking">
      <p className="mb-3 text-sm text-slate-600 dark:text-slate-400">
        Elke agent meldt hoe vol zijn schijven zijn. Komt een schijf boven een drempel, dan staat ze bij Aandacht nodig, met uitleg en een knop
        om veilig ruimte vrij te maken.
      </p>
      {q.isError && <Alert>Kon de instellingen niet laden.</Alert>}
      {q.data && <Form key={JSON.stringify(q.data)} initial={q.data} isAdmin={isAdmin} />}
    </Card>
  );
}

function Form({ initial, isAdmin }: { initial: DiskWatchSettings; isAdmin: boolean }) {
  const save = useSetDiskSettings();
  const [warn, setWarn] = useState(String(initial.warn_pct));
  const [crit, setCrit] = useState(String(initial.crit_pct));
  const [ignore, setIgnore] = useState(initial.ignore_mounts.join(", "));
  const submit = (e: FormEvent) => {
    e.preventDefault();
    save.mutate(
      { warn_pct: Number(warn), crit_pct: Number(crit), ignore_mounts: ignore.split(/[\s,]+/).filter(Boolean) },
      { onSuccess: () => toast("Drempels opgeslagen.") },
    );
  };
  return (
    <form onSubmit={submit} className="space-y-3">
      <div className="grid grid-cols-2 gap-3">
        <Field label="Let op vanaf (%)" htmlFor="disk-warn" hint="Oranje melding: tijd om op te ruimen.">
          <Input id="disk-warn" type="number" min={50} max={99} value={warn} onChange={(e) => setWarn(e.target.value)} disabled={!isAdmin} />
        </Field>
        <Field label="Ernstig vanaf (%)" htmlFor="disk-crit" hint="Rode melding: nu ingrijpen.">
          <Input id="disk-crit" type="number" min={51} max={100} value={crit} onChange={(e) => setCrit(e.target.value)} disabled={!isAdmin} />
        </Field>
      </div>
      <Field label="Niet bewaken" htmlFor="disk-ignore" hint="Mounts die vol mogen staan, zoals een back-upschijf. Gescheiden door komma's, bv. /mnt/backup.">
        <Input id="disk-ignore" value={ignore} onChange={(e) => setIgnore(e.target.value)} disabled={!isAdmin} placeholder="/mnt/backup" />
      </Field>
      {save.isError && <Alert>{save.error instanceof ApiError ? save.error.message : "Opslaan mislukte."}</Alert>}
      {isAdmin && (
        <Button type="submit" disabled={save.isPending}>
          Opslaan
        </Button>
      )}
    </form>
  );
}
