"use client";

import { Suspense, useMemo, useState } from "react";
import { QueryState } from "@/components/inventory/bits";
import { DatacenterGate } from "@/components/pve/bits";
import { PveTaskTable } from "@/components/pve/Tasks";
import { Card, Field, Input, PageHeader, Select } from "@/components/ui";
import { taskFailed, taskTypeLabel, usePveGet, type Datacenter, type PveTask } from "@/lib/pve";

export default function PveTasksPage() {
  return (
    <div className="space-y-6">
      <PageHeader
        title="Proxmox-taken"
        description="Wat Proxmox deed: back-ups, starten, verhuizen, terugzetten. Klik een taak voor het volledige log; een lopende taak kun je stoppen."
      />
      <Suspense>
        <DatacenterGate>{(dc) => <TasksView key={dc.id} dc={dc} />}</DatacenterGate>
      </Suspense>
    </div>
  );
}

function TasksView({ dc }: { dc: Datacenter }) {
  const [node, setNode] = useState("");
  const [errors, setErrors] = useState(false);
  const [type, setType] = useState("");
  const [search, setSearch] = useState("");
  // Alle hosts samen: /cluster/tasks (de recente). Eén host: zijn
  // geschiedenis, met de filters van Proxmox zelf.
  const params: Record<string, string> = { limit: "500" };
  if (errors) params.errors = "1";
  if (type) params.typefilter = type;
  const one = usePveGet<PveTask[]>(dc.id, `/nodes/${node}/tasks`, { params, enabled: !!node, refetch: 10_000 });
  const all = usePveGet<PveTask[]>(dc.id, "/cluster/tasks", { enabled: !node, refetch: 10_000 });
  const q = node ? one : all;
  const list = useMemo(() => {
    const f = search.trim().toLowerCase();
    return (q.data ?? [])
      .filter((t) => node || ((!errors || taskFailed(t)) && (!type || t.type === type)))
      .filter((t) => !f || (t.id ?? "").includes(f) || t.user.toLowerCase().includes(f) || taskTypeLabel(t.type).toLowerCase().includes(f))
      .sort((a, b) => b.starttime - a.starttime);
  }, [q.data, node, errors, type, search]);
  const types = useMemo(() => [...new Set((q.data ?? []).map((t) => t.type))].sort(), [q.data]);

  return (
    <Card>
      <div className="space-y-4">
        <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
          <Field label="Host" htmlFor="t-node">
            <Select id="t-node" value={node} onChange={(e) => setNode(e.target.value)}>
              <option value="">Alle hosts (recent)</option>
              {dc.hosts.map((h) => (
                <option key={h.pve_node} value={h.pve_node}>
                  {h.pve_node} (geschiedenis)
                </option>
              ))}
            </Select>
          </Field>
          <Field label="Soort" htmlFor="t-type">
            <Select id="t-type" value={type} onChange={(e) => setType(e.target.value)}>
              <option value="">Alle</option>
              {[...new Set([...types, type].filter(Boolean))].map((t) => (
                <option key={t} value={t}>
                  {taskTypeLabel(t)}
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
        </div>
        <QueryState q={q}>
          <PveTaskTable dc={dc.id} tasks={list} />
        </QueryState>
      </div>
    </Card>
  );
}
