"use client";

import Link from "next/link";
import { useRouter, useSearchParams } from "next/navigation";
import { Suspense, useState } from "react";
import { JobStatusBadge } from "@/components/jobs/JobList";
import { Modal } from "@/components/Modal";
import { ProdConfirm, prodConfirmed, useProdTotp } from "@/components/ProdConfirm";
import { Alert, Badge, Button, Card, Field, Input, PageHeader, Select, cx } from "@/components/ui";
import { ApiError } from "@/lib/api/client";
import { useIsAdmin } from "@/lib/inventory";
import {
  useCommand,
  useCommands,
  useDeleteCommand,
  useRunCommand,
  useSaveCommand,
  useUpdates,
  zabbixExample,
  type CommandDetail,
  type UpdateNode,
} from "@/lib/updates";

export default function CommandsPage() {
  return (
    <Suspense>
      <Commands />
    </Suspense>
  );
}

type Form = { name: string; description: string; script: string; run_as: string; timeout: string };
const empty: Form = { name: "", description: "", script: "", run_as: "root", timeout: "30" };

function Commands() {
  const search = useSearchParams();
  const router = useRouter();
  const id = search.get("id");
  const list = useCommands();
  const current = useCommand(id);
  const isAdmin = useIsAdmin();
  const save = useSaveCommand();
  const remove = useDeleteCommand();
  const [form, setForm] = useState<Form>(empty);
  const [loadedFor, setLoadedFor] = useState<string | null>(null);
  const [saved, setSaved] = useState<string | null>(null);
  const [running, setRunning] = useState(false);

  // Het gekozen commando één keer per keuze in het formulier zetten.
  const key = current.data ? `${current.data.id}@${current.data.revision}@${current.data.updated_at}` : null;
  if (id && current.data && current.data.id === id && loadedFor !== key) {
    setForm(toForm(current.data));
    setLoadedFor(key);
  }
  const dirty = !!current.data && id === current.data.id && JSON.stringify(form) !== JSON.stringify(toForm(current.data));

  const open = (cid: string | null) => {
    setSaved(null);
    save.reset();
    setLoadedFor(null);
    if (!cid) setForm(empty);
    router.push(cid ? `/commandos?id=${cid}` : "/commandos");
  };
  const onSave = () => {
    setSaved(null);
    const minutes = Number(form.timeout);
    save.mutate(
      {
        id,
        body: {
          name: form.name,
          description: form.description,
          script: form.script,
          run_as: form.run_as,
          timeout_seconds: Number.isFinite(minutes) ? Math.round(minutes * 60) : 0,
        },
      },
      {
        onSuccess: (c) => {
          setSaved(`Bewaard als revisie ${c.revision}.`);
          if (c.id !== id) router.push(`/commandos?id=${c.id}`);
        },
      },
    );
  };

  return (
    <div className="space-y-6">
      <PageHeader
        title="Commando's"
        description="Eigen scripts die je hier schrijft en op gekozen nodes laat draaien, één node tegelijk, met de uitvoer live in de taak. Elke andere tekst is een nieuwe revisie; het logboek onthoudt welke revisie waar draaide."
      />
      <div className="grid gap-6 lg:grid-cols-[18rem_1fr]">
        <div className="space-y-4">
          <Card title="Jouw commando's">
            {list.data?.length === 0 && <p className="text-sm text-slate-500">{"Nog geen commando's."}</p>}
            <ul className="space-y-1 text-sm">
              {list.data?.map((c) => (
                <li key={c.id}>
                  <button
                    type="button"
                    onClick={() => open(c.id)}
                    className={cx(
                      "flex w-full items-center justify-between gap-2 rounded px-2 py-1 text-left hover:bg-slate-100 dark:hover:bg-slate-800",
                      c.id === id && "bg-slate-100 font-medium dark:bg-slate-800",
                    )}
                  >
                    <span>{c.name}</span>
                    {c.last_run && <JobStatusBadge status={c.last_run.status} />}
                  </button>
                </li>
              ))}
            </ul>
          </Card>
          {isAdmin && (
            <Card title="Beginnen">
              <div className="flex flex-col gap-2">
                <Button variant="secondary" onClick={() => open(null)}>
                  Nieuw commando
                </Button>
                <Button
                  variant="secondary"
                  onClick={() => {
                    open(null);
                    setForm({ ...empty, name: "Zabbix-database omzetten", description: "Na een grote Zabbix-versie de database omzetten met een eigen configuratie zonder HA. Stop de run als het logboek zegt dat de database bij is.", script: zabbixExample, timeout: "120" });
                  }}
                >
                  Voorbeeld: Zabbix-database omzetten
                </Button>
              </div>
            </Card>
          )}
        </div>

        <div className="space-y-4">
          <Card
            title={
              <span className="flex flex-wrap items-center justify-between gap-2">
                <span className="flex items-center gap-2">
                  {id ? (current.data?.name ?? "…") : "Nieuw commando"}
                  {current.data && id && <Badge>revisie {current.data.revision}</Badge>}
                  {dirty && <Badge tone="amber">gewijzigd</Badge>}
                </span>
                {isAdmin && (
                  <span className="flex gap-2">
                    {id && !dirty && (
                      <Button
                        variant="secondary-danger"
                        disabled={remove.isPending}
                        onClick={() => {
                          if (confirm(`${current.data?.name ?? "Dit commando"} weghalen? De taken van de runs blijven.`)) {
                            remove.mutate(id, { onSuccess: () => open(null) });
                          }
                        }}
                      >
                        Weghalen
                      </Button>
                    )}
                    {id && !dirty && <Button onClick={() => setRunning(true)}>Uitvoeren…</Button>}
                    <Button
                      variant={id && !dirty ? "secondary" : "primary"}
                      onClick={onSave}
                      disabled={save.isPending || form.name.trim() === "" || form.script.trim() === "" || (!!id && !dirty)}
                    >
                      Bewaren
                    </Button>
                  </span>
                )}
              </span>
            }
          >
            <div className="grid gap-3 sm:grid-cols-[1fr_10rem_8rem]">
              <Field label="Naam" htmlFor="cmd-name">
                <Input id="cmd-name" value={form.name} readOnly={!isAdmin} onChange={(e) => setForm({ ...form, name: e.target.value })} />
              </Field>
              <Field label="Als gebruiker" htmlFor="cmd-user">
                <Input id="cmd-user" value={form.run_as} readOnly={!isAdmin} className="font-mono" onChange={(e) => setForm({ ...form, run_as: e.target.value })} />
              </Field>
              <Field label="Limiet (min)" htmlFor="cmd-timeout">
                <Input id="cmd-timeout" type="number" min={1} max={1440} value={form.timeout} readOnly={!isAdmin} onChange={(e) => setForm({ ...form, timeout: e.target.value })} />
              </Field>
            </div>
            <div className="mt-3">
              <Field label="Omschrijving" htmlFor="cmd-desc">
                <Input id="cmd-desc" value={form.description} readOnly={!isAdmin} onChange={(e) => setForm({ ...form, description: e.target.value })} />
              </Field>
            </div>
            <textarea
              aria-label="Script"
              value={form.script}
              onChange={(e) => setForm({ ...form, script: e.target.value })}
              spellCheck={false}
              readOnly={!isAdmin}
              rows={Math.max(12, Math.min(40, form.script.split("\n").length + 2))}
              className="mt-3 w-full rounded-md border border-slate-300 bg-white p-3 font-mono text-xs leading-5 dark:border-slate-700 dark:bg-slate-950"
              placeholder={"#!/bin/sh\nset -e\n…"}
            />
            <p className="mt-1 text-xs text-slate-500">
              Zonder #!-regel draait het met /bin/sh. Wachtwoorden zoals PASSWORD=… of -p… worden in de uitvoer gemaskeerd; zet ze liever niet in het script.
            </p>
            {save.error && (
              <div className="mt-2">
                <Alert>{save.error instanceof ApiError ? save.error.message : "Bewaren mislukte"}</Alert>
              </div>
            )}
            {saved && (
              <div className="mt-2">
                <Alert kind="success">{saved}</Alert>
              </div>
            )}
          </Card>
          {current.data && id && <Runs detail={current.data} />}
          {current.data && id && current.data.revisions.length > 1 && <Revisions detail={current.data} />}
        </div>
      </div>
      {running && current.data && <RunDialog detail={current.data} onClose={() => setRunning(false)} />}
    </div>
  );
}

function toForm(c: CommandDetail): Form {
  return {
    name: c.name,
    description: c.description,
    script: c.script,
    run_as: c.run_as,
    timeout: String(Math.round(c.timeout_seconds / 60)),
  };
}

function Runs({ detail }: { detail: CommandDetail }) {
  return (
    <Card title="Laatste runs">
      {detail.runs.length === 0 ? (
        <p className="text-sm text-slate-500">Nog niet gedraaid.</p>
      ) : (
        <ul className="space-y-1 text-sm">
          {detail.runs.map((r) => (
            <li key={r.id} className="flex flex-wrap items-center gap-2">
              <JobStatusBadge status={r.status} />
              <Link href={`/taken/detail?id=${r.id}`} className="text-brand-700 hover:underline dark:text-sky-300">
                {r.title}
              </Link>
              <span className="text-xs text-slate-500">
                {new Date(r.created_at).toLocaleString("nl-BE")}
                {r.requested_by && ` door ${r.requested_by}`}
              </span>
              {r.error && <span className="text-xs text-red-700 dark:text-red-300">{r.error}</span>}
            </li>
          ))}
        </ul>
      )}
    </Card>
  );
}

function Revisions({ detail }: { detail: CommandDetail }) {
  return (
    <Card title="Revisies">
      <div className="space-y-2">
        {detail.revisions.map((r) => (
          <details key={r.revision} className="text-sm">
            <summary className="cursor-pointer">
              Revisie {r.revision} <span className="text-xs text-slate-500">
                {new Date(r.created_at).toLocaleString("nl-BE")}
                {r.created_by && ` door ${r.created_by}`} · sha256 {r.sha256.slice(0, 12)}
              </span>
            </summary>
            <pre className="mt-1 max-h-64 overflow-auto rounded bg-slate-900 p-3 font-mono text-xs text-slate-100">{r.script}</pre>
          </details>
        ))}
      </div>
    </Card>
  );
}

function RunDialog({ detail, onClose }: { detail: CommandDetail; onClose: () => void }) {
  const updates = useUpdates();
  const run = useRunCommand();
  const totp = useProdTotp();
  const [mode, setMode] = useState<"nodes" | "cluster">("nodes");
  const [clusterId, setClusterId] = useState("");
  const [picked, setPicked] = useState<string[]>([]);
  const [cont, setCont] = useState(false);
  const [confirm, setConfirm] = useState("");
  const nodes = updates.data?.nodes ?? [];
  const clusters = [...new Map(nodes.filter((n) => n.cluster_id).map((n) => [n.cluster_id!, n])).values()];
  const chosen: UpdateNode[] = mode === "cluster" ? nodes.filter((n) => n.cluster_id === clusterId) : nodes.filter((n) => picked.includes(n.node_id));
  const prodSlugs = [...new Set(chosen.filter((n) => n.cluster_environment === "prod").map((n) => n.cluster_slug))];
  const prodSlug = prodSlugs.length === 1 ? prodSlugs[0] : undefined;
  const blocked =
    chosen.length === 0 || chosen.some((n) => !n.online || !n.supported) || prodSlugs.length > 1 || (!!prodSlug && !prodConfirmed(prodSlug, confirm, totp));
  const submit = () =>
    run.mutate({
      id: detail.id,
      body: {
        ...(mode === "cluster" ? { cluster_id: clusterId } : { node_ids: picked }),
        continue_on_error: cont,
        ...(prodSlug ? { confirm } : {}),
      },
    });

  return (
    <Modal title={`${detail.name} uitvoeren`} onClose={onClose} wide>
      {run.data ? (
        <div className="space-y-3 text-sm">
          <Alert kind="success">Gestart.</Alert>
          <p>
            <Link href={`/taken/detail?id=${run.data.id}`} className="font-medium text-brand-700 underline dark:text-sky-300">
              {run.data.title}
            </Link>{" "}
            — de uitvoer verschijnt daar live; Annuleren stopt het commando op de node.
          </p>
          <div className="flex justify-end">
            <Button onClick={onClose}>Sluiten</Button>
          </div>
        </div>
      ) : (
        <div className="space-y-4 text-sm">
          <p className="text-slate-600 dark:text-slate-400">
            Revisie {detail.revision}, als {detail.run_as}, hoogstens {Math.round(detail.timeout_seconds / 60)} minuten per node. Eén node tegelijk; bij een cluster de VIP-houder als laatste.
          </p>
          <div className="flex gap-4">
            <label className="flex items-center gap-2">
              <input type="radio" checked={mode === "nodes"} onChange={() => setMode("nodes")} /> Nodes kiezen
            </label>
            <label className="flex items-center gap-2">
              <input type="radio" checked={mode === "cluster"} onChange={() => setMode("cluster")} /> Heel cluster
            </label>
          </div>
          {mode === "cluster" ? (
            <Select aria-label="Cluster" value={clusterId} onChange={(e) => setClusterId(e.target.value)}>
              <option value="">Kies een cluster</option>
              {clusters.map((c) => (
                <option key={c.cluster_id!} value={c.cluster_id!}>
                  {c.cluster_name} ({c.cluster_environment})
                </option>
              ))}
            </Select>
          ) : (
            <div className="max-h-64 space-y-1 overflow-y-auto rounded border border-slate-200 p-2 dark:border-slate-700">
              {nodes.map((n) => (
                <label key={n.node_id} className="flex items-center gap-2">
                  <input
                    type="checkbox"
                    disabled={!n.online || !n.supported}
                    checked={picked.includes(n.node_id)}
                    onChange={(e) => setPicked(e.target.checked ? [...picked, n.node_id] : picked.filter((x) => x !== n.node_id))}
                  />
                  <span className="font-medium">{n.hostname}</span>
                  <span className="text-xs text-slate-500">{n.cluster_name || "zonder cluster"}</span>
                  {!n.online && <Badge tone="red">offline</Badge>}
                  {n.online && !n.supported && <Badge tone="amber">agent te oud</Badge>}
                </label>
              ))}
            </div>
          )}
          <label className="flex items-center gap-2">
            <input type="checkbox" checked={cont} onChange={(e) => setCont(e.target.checked)} /> Verder op de volgende nodes als het op een node mislukt
          </label>
          {prodSlugs.length > 1 && <Alert>Kies nodes uit hoogstens één prodcluster tegelijk.</Alert>}
          {prodSlug && <ProdConfirm slug={prodSlug} value={confirm} onChange={setConfirm} />}
          {run.error && <Alert>{run.error instanceof ApiError ? run.error.message : "Starten mislukte"}</Alert>}
          <div className="flex justify-end gap-2">
            <Button variant="secondary" onClick={onClose}>
              Annuleren
            </Button>
            <Button disabled={blocked || run.isPending} onClick={submit}>
              Uitvoeren op {chosen.length} {chosen.length === 1 ? "node" : "nodes"}
            </Button>
          </div>
        </div>
      )}
    </Modal>
  );
}
