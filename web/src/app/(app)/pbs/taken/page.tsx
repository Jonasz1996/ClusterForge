"use client";

import { Suspense, useMemo, useState } from "react";
import { QueryState } from "@/components/inventory/bits";
import { PbsGate } from "@/components/pbs/bits";
import { PbsTaskTable } from "@/components/pbs/Tasks";
import { Card, Field, Input, PageHeader, Select } from "@/components/ui";
import { pbsTaskLabel, pbsWorkerID, usePbsGet, type PbsServer, type PbsTask } from "@/lib/pbs";

export default function PbsTasksPage() {
  return (
    <div className="space-y-6">
      <PageHeader title="PBS-taken" description="Wat de Backup Server deed: back-ups, verificaties, garbage collection, prune en sync. Klik een taak voor het volledige log; een lopende taak kun je stoppen." />
      <Suspense>
        <PbsGate>{(s) => <TasksView key={s.node_id} srv={s} />}</PbsGate>
      </Suspense>
    </div>
  );
}

function TasksView({ srv }: { srv: PbsServer }) {
  const [errors, setErrors] = useState(false);
  const [running, setRunning] = useState(false);
  const [type, setType] = useState("");
  const [store, setStore] = useState("");
  const [search, setSearch] = useState("");
  const params: Record<string, string> = { limit: "500" };
  if (errors) params.errors = "1";
  if (running) params.running = "1";
  if (type) params.typefilter = type;
  if (store) params.store = store;
  const q = usePbsGet<PbsTask[]>(srv.node_id, "/nodes/localhost/tasks", { params, refetch: 10_000 });
  const list = useMemo(() => {
    const f = search.trim().toLowerCase();
    return (q.data ?? [])
      .filter((t) => !f || pbsWorkerID(t.worker_id).toLowerCase().includes(f) || t.user.toLowerCase().includes(f) || pbsTaskLabel(t.worker_type).toLowerCase().includes(f))
      .sort((a, b) => b.starttime - a.starttime);
  }, [q.data, search]);
  const types = useMemo(() => [...new Set([...(q.data ?? []).map((t) => t.worker_type), type].filter(Boolean))].sort(), [q.data, type]);
  return (
    <Card>
      <div className="space-y-4">
        <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-5">
          <Field label="Soort" htmlFor="t-type">
            <Select id="t-type" value={type} onChange={(e) => setType(e.target.value)}>
              <option value="">Alle</option>
              {types.map((t) => (
                <option key={t} value={t}>
                  {pbsTaskLabel(t)}
                </option>
              ))}
            </Select>
          </Field>
          <Field label="Datastore" htmlFor="t-store">
            <Select id="t-store" value={store} onChange={(e) => setStore(e.target.value)}>
              <option value="">Alle</option>
              {srv.datastores.map((d) => (
                <option key={d} value={d}>
                  {d}
                </option>
              ))}
            </Select>
          </Field>
          <Field label="Zoeken" htmlFor="t-search">
            <Input id="t-search" value={search} onChange={(e) => setSearch(e.target.value)} placeholder="VMID, gebruiker, soort" />
          </Field>
          <label className="flex items-end gap-2 pb-2 text-sm">
            <input type="checkbox" checked={errors} onChange={(e) => setErrors(e.target.checked)} />
            Alleen mislukte
          </label>
          <label className="flex items-end gap-2 pb-2 text-sm">
            <input type="checkbox" checked={running} onChange={(e) => setRunning(e.target.checked)} />
            Alleen lopende
          </label>
        </div>
        <QueryState q={q}>
          <PbsTaskTable id={srv.node_id} tasks={list} />
        </QueryState>
      </div>
    </Card>
  );
}
