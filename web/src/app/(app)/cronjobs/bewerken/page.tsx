"use client";

import { useRouter, useSearchParams } from "next/navigation";
import { Suspense, useState } from "react";
import { ProdConfirm, prodConfirmed, useProdTotp } from "@/components/ProdConfirm";
import { Alert, Button, Card, Field, Input, PageHeader, Select, Textarea } from "@/components/ui";
import { ApiError } from "@/lib/api/client";
import { jobInputOf, useCronSchedule, useManagedCronJob, useSaveCronJob, type ManagedCronJob, type ManagedCronJobInput } from "@/lib/cronjobs";
import { useClusters, useIsAdmin, useNodes } from "@/lib/inventory";

const presets: { label: string; schedule: string }[] = [
  { label: "Elk uur", schedule: "0 * * * *" },
  { label: "Elke 6 uur", schedule: "0 */6 * * *" },
  { label: "Elke dag om 03:00", schedule: "0 3 * * *" },
  { label: "Op werkdagen om 07:00", schedule: "0 7 * * 1-5" },
  { label: "Elke zondag om 04:00", schedule: "0 4 * * 0" },
  { label: "De 1e van de maand om 02:00", schedule: "0 2 1 * *" },
];

const timeFmt = new Intl.DateTimeFormat("nl-BE", { weekday: "short", day: "numeric", month: "short", hour: "2-digit", minute: "2-digit" });

const empty: ManagedCronJobInput = {
  name: "",
  schedule: "0 */6 * * *",
  run_on: "vip",
  node_id: null,
  user: "root",
  command: "",
  timeout_seconds: 3600,
  enabled: true,
  retries: 0,
  alert: true,
};

export default function EditCronJobPage() {
  return (
    <Suspense>
      <EditInner />
    </Suspense>
  );
}

function EditInner() {
  const params = useSearchParams();
  const id = params.get("id") ?? "";
  const existing = useManagedCronJob(id);
  const isAdmin = useIsAdmin();
  const job = existing.data?.job;
  if (!isAdmin) return <Alert>Alleen een beheerder kan cronjobs maken of wijzigen.</Alert>;
  if (id !== "" && !job) return <p className="text-sm text-slate-500">Laden…</p>;
  return <JobForm key={job?.id ?? "nieuw"} job={job} initialCluster={params.get("cluster") ?? ""} />;
}

function JobForm({ job, initialCluster }: { job?: ManagedCronJob; initialCluster: string }) {
  const id = job?.id ?? "";
  const clusters = useClusters();
  const nodes = useNodes();
  const totp = useProdTotp();
  const save = useSaveCronJob();
  const router = useRouter();
  const [form, setForm] = useState<ManagedCronJobInput>(job ? jobInputOf(job) : empty);
  const [clusterId, setClusterId] = useState(job?.cluster_id ?? initialCluster);
  const [confirm, setConfirm] = useState("");

  const schedule = useCronSchedule(form.schedule);
  const cluster = clusters.data?.find((c) => c.id === clusterId);
  const clusterNodes = (nodes.data ?? []).filter((n) => n.cluster_id === clusterId);
  const prod = cluster?.environment === "prod";
  const fieldError = save.error instanceof ApiError ? save.error.field : undefined;
  const set = (patch: Partial<ManagedCronJobInput>) => setForm((f) => ({ ...f, ...patch }));
  const err = (field: string) => (fieldError === field && save.error ? <p className="mt-1 text-sm text-red-700 dark:text-red-300">{save.error.message}</p> : null);

  const submit = () =>
    save.mutate(
      { id: id || undefined, clusterId, body: { ...form, confirm: prod ? confirm : undefined } },
      { onSuccess: (j) => router.push(`/cronjobs/job?id=${j.id}`) },
    );

  return (
    <div className="space-y-6">
      <PageHeader
        title={id ? `${job?.name} bewerken` : "Cronjob toevoegen"}
        description="Een beheerde cronjob staat als systemd-timer op de nodes van het cluster. cf-agent start het commando, houdt bij of het lukte en stuurt de uitvoer naar ClusterForge."
      />
      <Card>
        <div className="grid max-w-2xl gap-4">
          <Field label="Cluster" htmlFor="cluster">
            <Select id="cluster" value={clusterId} disabled={id !== ""} onChange={(e) => setClusterId(e.target.value)}>
              <option value="">Kies een cluster</option>
              {(clusters.data ?? []).map((c) => (
                <option key={c.id} value={c.id}>
                  {c.name}
                </option>
              ))}
            </Select>
          </Field>
          <Field label="Naam" htmlFor="name" hint="Zoals nextcloud-opruimen; uniek in het cluster.">
            <Input id="name" value={form.name} onChange={(e) => set({ name: e.target.value })} />
            {err("name")}
          </Field>
          <Field label="Wanneer" htmlFor="schedule" hint="Een cronregel: minuut uur dag maand weekdag.">
            <div className="flex flex-wrap gap-2">
              <Input id="schedule" className="w-48 font-mono" value={form.schedule} onChange={(e) => set({ schedule: e.target.value })} />
              <Select
                value=""
                className="w-auto"
                onChange={(e) => e.target.value !== "" && set({ schedule: e.target.value })}
                aria-label="Kies een schema"
              >
                <option value="">Kies uit de lijst…</option>
                {presets.map((p) => (
                  <option key={p.schedule} value={p.schedule}>
                    {p.label}
                  </option>
                ))}
              </Select>
            </div>
            {schedule.data && (
              <p className="mt-1 text-sm text-slate-600 dark:text-slate-400">
                {schedule.data.describe}. Volgende runs: {schedule.data.next.slice(0, 3).map((t) => timeFmt.format(new Date(t))).join(", ")}.
              </p>
            )}
            {schedule.isError && <p className="mt-1 text-sm text-red-700 dark:text-red-300">{schedule.error.message}</p>}
            {err("schedule")}
          </Field>
          <Field label="Waar" htmlFor="run_on">
            <Select id="run_on" value={form.run_on} onChange={(e) => set({ run_on: e.target.value as ManagedCronJobInput["run_on"] })}>
              <option value="vip">Op de node met het VIP (verhuist mee)</option>
              <option value="node">Op één vaste node</option>
              <option value="all">Op alle nodes van het cluster</option>
            </Select>
            {err("run_on")}
          </Field>
          {form.run_on === "node" && (
            <Field label="Node" htmlFor="node">
              <Select id="node" value={form.node_id ?? ""} onChange={(e) => set({ node_id: e.target.value || null })}>
                <option value="">Kies een node</option>
                {clusterNodes.map((n) => (
                  <option key={n.id} value={n.id}>
                    {n.hostname}
                  </option>
                ))}
              </Select>
              {err("node_id")}
            </Field>
          )}
          <Field label="Gebruiker" htmlFor="user">
            <Input id="user" className="w-48" value={form.user} onChange={(e) => set({ user: e.target.value })} />
            {err("user")}
          </Field>
          <Field
            label="Commando"
            htmlFor="command"
            hint="Draait met /bin/sh -c. Zet een wachtwoord liever in een bestand op de node (zoals ~/.my.cnf) dan in het commando."
          >
            <Textarea id="command" rows={5} className="font-mono" value={form.command} onChange={(e) => set({ command: e.target.value })} />
            {err("command")}
          </Field>
          <Field label="Tijdslimiet in minuten" htmlFor="timeout" hint="Daarna stopt cf-agent de job. Een run die nog loopt als de volgende moet starten, slaat die over.">
            <Input
              id="timeout"
              type="number"
              min={1}
              max={1440}
              className="w-32"
              value={Math.round(form.timeout_seconds / 60)}
              onChange={(e) => set({ timeout_seconds: Number(e.target.value) * 60 })}
            />
            {err("timeout_seconds")}
          </Field>
          <Field label="Opnieuw proberen" htmlFor="retries" hint="Mislukt een run, dan probeert cf-agent het een minuut later nog eens.">
            <Select id="retries" className="w-auto" value={form.retries ?? 0} onChange={(e) => set({ retries: Number(e.target.value) })}>
              <option value={0}>Niet</option>
              <option value={1}>1 keer</option>
              <option value={2}>2 keer</option>
              <option value={3}>3 keer</option>
            </Select>
            {err("retries")}
          </Field>
          <label className="flex items-center gap-2 text-sm">
            <input type="checkbox" checked={form.alert ?? true} onChange={(e) => set({ alert: e.target.checked })} />
            Een incident openen als de job mislukt, tot hij weer lukt
          </label>
          <label className="flex items-center gap-2 text-sm">
            <input type="checkbox" checked={form.enabled} onChange={(e) => set({ enabled: e.target.checked })} />
            Aan
          </label>
          {prod && cluster && <ProdConfirm slug={cluster.slug} value={confirm} onChange={setConfirm} />}
          {save.isError && !fieldError && <Alert>{save.error.message}</Alert>}
          <div>
            <Button disabled={clusterId === "" || save.isPending || (prod && !prodConfirmed(cluster?.slug ?? "", confirm, totp))} onClick={submit}>
              {save.isPending ? "Bezig…" : id ? "Opslaan" : "Toevoegen"}
            </Button>
          </div>
        </div>
      </Card>
    </div>
  );
}
