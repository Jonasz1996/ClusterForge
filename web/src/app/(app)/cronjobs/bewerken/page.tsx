"use client";

import Link from "next/link";
import { useRouter, useSearchParams } from "next/navigation";
import { Suspense, useState } from "react";
import { ProdConfirm, prodConfirmed, useProdTotp } from "@/components/ProdConfirm";
import { Alert, Button, Card, Field, Input, PageHeader, Select, Textarea } from "@/components/ui";
import { ApiError } from "@/lib/api/client";
import {
  adoptInput,
  jobInputOf,
  useCronjobs,
  useCronSchedule,
  useManagedCronJob,
  useSaveCronJob,
  useToggleCron,
  type CronJob,
  type ManagedCronJob,
  type ManagedCronJobInput,
} from "@/lib/cronjobs";
import { useCluster, useClusters, useIsAdmin, useNodes } from "@/lib/inventory";

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
  const adoptKey = params.get("overnemen") ?? "";
  const initialCluster = params.get("cluster") ?? "";
  const found = useCronjobs();
  const cluster = useCluster(adoptKey !== "" ? initialCluster : "");
  if (!isAdmin) return <Alert>Alleen een beheerder kan cronjobs maken of wijzigen.</Alert>;
  if (id !== "" && !job) return <p className="text-sm text-slate-500">Laden…</p>;
  if (adoptKey !== "") {
    if (!found.data || !cluster.data) return <p className="text-sm text-slate-500">Laden…</p>;
    const g = found.data.find((x) => x.cluster_id === initialCluster);
    const origin = g?.jobs.find((j) => j.key === adoptKey);
    if (!g || !origin) return <Alert>Die cronjob staat niet (meer) op de nodes van dit cluster.</Alert>;
    return <JobForm key={`overnemen-${adoptKey}`} initialCluster={initialCluster} origin={origin} initial={adoptInput(origin, g, cluster.data.vips.length > 0)} />;
  }
  return <JobForm key={job?.id ?? "nieuw"} job={job} initialCluster={initialCluster} />;
}

function JobForm({
  job,
  initialCluster,
  origin,
  initial,
}: {
  job?: ManagedCronJob;
  initialCluster: string;
  origin?: CronJob;
  initial?: ManagedCronJobInput;
}) {
  const id = job?.id ?? "";
  const clusters = useClusters();
  const nodes = useNodes();
  const totp = useProdTotp();
  const save = useSaveCronJob();
  const router = useRouter();
  const [form, setForm] = useState<ManagedCronJobInput>(job ? jobInputOf(job) : (initial ?? empty));
  const toggle = useToggleCron();
  const [created, setCreated] = useState<string | null>(null);
  const originOn = origin?.nodes.filter((n) => n.enabled) ?? [];
  const [clusterId, setClusterId] = useState(job?.cluster_id ?? initialCluster);
  const [confirm, setConfirm] = useState("");

  const schedule = useCronSchedule(form.schedule);
  const cluster = clusters.data?.find((c) => c.id === clusterId);
  const clusterNodes = (nodes.data ?? []).filter((n) => n.cluster_id === clusterId);
  const prod = cluster?.environment === "prod";
  const fieldError = save.error instanceof ApiError ? save.error.field : undefined;
  const set = (patch: Partial<ManagedCronJobInput>) => setForm((f) => ({ ...f, ...patch }));
  const isSync = form.kind === "sync";
  const sync = form.sync ?? { source: "", target: "", to_cluster_id: null, delete: false, exclude: [], bwlimit_mb: 0 };
  const setSync = (patch: Partial<typeof sync>) => setForm((f) => ({ ...f, sync: { ...sync, ...f.sync, ...patch } }));
  const otherClusters = (clusters.data ?? []).filter((c) => c.id !== clusterId);
  const [excludeText, setExcludeText] = useState((form.sync?.exclude ?? []).join("\n"));
  const err = (field: string) => (fieldError === field && save.error ? <p className="mt-1 text-sm text-red-700 dark:text-red-300">{save.error.message}</p> : null);

  // Bij overnemen gaat de oude regel daarna in commentaar, op elke node
  // waar hij aanstond.
  const submit = () =>
    save.mutate(
      { id: id || undefined, clusterId, body: { ...form, confirm: prod ? confirm : undefined } },
      {
        onSuccess: (j) => {
          if (!origin || originOn.length === 0) {
            router.push(`/cronjobs/job?id=${j.id}`);
            return;
          }
          setCreated(j.id);
          toggle.mutate(
            { key: origin.key, nodes: originOn, enabled: false, confirm: prod ? confirm : undefined },
            { onSuccess: () => router.push(`/cronjobs/job?id=${j.id}`) },
          );
        },
      },
    );

  if (created) {
    return (
      <div className="space-y-4">
        <PageHeader title={`${form.name} overgenomen`} />
        {toggle.isPending && <p className="text-sm text-slate-500">De oude regel gaat in commentaar op {originOn.map((n) => n.hostname).join(", ")}…</p>}
        {toggle.isError && (
          <Alert>
            De beheerde job staat er, maar de oude regel kon niet overal uit: {toggle.error.message}. Zet hem uit bij Gevonden op de nodes, anders draait
            de job twee keer.
          </Alert>
        )}
        <Link href={`/cronjobs/job?id=${created}`} className="text-sm text-brand-700 hover:underline dark:text-sky-300">
          Naar de job
        </Link>
      </div>
    );
  }

  return (
    <div className="space-y-6">
      <PageHeader
        title={id ? `${job?.name} bewerken` : "Cronjob toevoegen"}
        description="Een beheerde cronjob staat als systemd-timer op de nodes van het cluster. cf-agent start het commando, houdt bij of het lukte en stuurt de uitvoer naar ClusterForge."
      />
      {origin && (
        <Alert kind="info">
          Overnemen van {origin.file}
          {origin.line ? `, regel ${origin.line}` : ""} op {origin.nodes.map((n) => n.hostname).join(", ")}. Na het opslaan zet ClusterForge die regel in
          commentaar, met &quot;# uitgezet door ClusterForge&quot; ervoor; terugzetten kan bij Gevonden op de nodes.
          {form.command.includes("***") &&
            " Het commando bevat een wachtwoord dat ClusterForge niet ziet (***). Zet het in een bestand op de node, zoals ~/.my.cnf, en pas het commando aan."}
        </Alert>
      )}
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
          {id === "" && (
            <Field label="Soort" htmlFor="kind">
              <Select
                id="kind"
                value={form.kind ?? "command"}
                onChange={(e) => {
                  const kind = e.target.value as "command" | "sync";
                  set(kind === "sync" ? { kind, run_on: form.run_on === "all" ? "vip" : form.run_on, user: "root", sync } : { kind, sync: undefined });
                }}
              >
                <option value="command">Commando</option>
                <option value="sync">Bestandssync (rsync over SSH)</option>
              </Select>
              {err("kind")}
            </Field>
          )}
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
          <Field label={isSync ? "Bron" : "Waar"} htmlFor="run_on">
            <Select id="run_on" value={form.run_on} onChange={(e) => set({ run_on: e.target.value as ManagedCronJobInput["run_on"] })}>
              <option value="vip">{isSync ? "De node met het VIP (verhuist mee)" : "Op de node met het VIP (verhuist mee)"}</option>
              <option value="node">{isSync ? "Eén vaste node" : "Op één vaste node"}</option>
              {!isSync && <option value="all">Op alle nodes van het cluster</option>}
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
          {isSync ? (
            <>
              <Field label="Map op de bron" htmlFor="source" hint="Zoals /var/www/nextcloud/data/. De inhoud van deze map gaat mee.">
                <Input id="source" className="font-mono" value={sync.source} onChange={(e) => setSync({ source: e.target.value })} />
                {err("sync.source")}
              </Field>
              <Field label="Naar" htmlFor="to_cluster">
                <Select id="to_cluster" value={sync.to_cluster_id ?? ""} onChange={(e) => setSync({ to_cluster_id: e.target.value || null })}>
                  <option value="">De andere nodes van dit cluster</option>
                  {otherClusters.map((c) => (
                    <option key={c.id} value={c.id}>
                      Alle nodes van {c.name}
                    </option>
                  ))}
                </Select>
                {err("sync.to_cluster_id")}
              </Field>
              <Field label="Map op het doel" htmlFor="target" hint="Leeg is dezelfde map als op de bron.">
                <Input id="target" className="font-mono" value={sync.target ?? ""} onChange={(e) => setSync({ target: e.target.value })} />
                {err("sync.target")}
              </Field>
              <Field label="Niet meenemen" htmlFor="exclude" hint="Eén patroon per regel, zoals *.part of cache/.">
                <Textarea
                  id="exclude"
                  rows={3}
                  className="font-mono"
                  value={excludeText}
                  onChange={(e) => {
                    setExcludeText(e.target.value);
                    setSync({ exclude: e.target.value.split("\n").map((l) => l.trim()).filter((l) => l !== "") });
                  }}
                />
                {err("sync.exclude")}
              </Field>
              <Field label="Bandbreedte in MB/s" htmlFor="bwlimit" hint="0 is onbeperkt.">
                <Input id="bwlimit" type="number" min={0} className="w-32" value={sync.bwlimit_mb ?? 0} onChange={(e) => setSync({ bwlimit_mb: Number(e.target.value) })} />
                {err("sync.bwlimit_mb")}
              </Field>
              <label className="flex items-center gap-2 text-sm">
                <input type="checkbox" checked={sync.delete ?? false} onChange={(e) => setSync({ delete: e.target.checked })} />
                Op het doel weghalen wat op de bron weg is (doe eerst een proefrun)
              </label>
              {err("sync")}
            </>
          ) : (
            <>
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
            </>
          )}
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
            <Button disabled={clusterId === "" || save.isPending || (!!origin && form.command.includes("***")) || (prod && !prodConfirmed(cluster?.slug ?? "", confirm, totp))} onClick={submit}>
              {save.isPending ? "Bezig…" : id ? "Opslaan" : origin ? "Overnemen" : "Toevoegen"}
            </Button>
          </div>
        </div>
      </Card>
    </div>
  );
}
