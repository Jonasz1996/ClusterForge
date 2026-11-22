"use client";

import { useState, type FormEvent } from "react";
import { Alert, Button, Field, Input, Select, Textarea } from "@/components/ui";
import { lifecycles, parseTags, useClusters, type Node, type NodeInput, type NodeLifecycle } from "@/lib/inventory";

export function NodeForm({
  initial,
  clusterId,
  submitLabel,
  onSubmit,
  onCancel,
}: {
  initial?: Node;
  // clusterId zet het cluster vast, bijvoorbeeld bij toevoegen vanuit een cluster.
  clusterId?: string;
  submitLabel: string;
  onSubmit: (v: NodeInput) => Promise<unknown>;
  onCancel?: () => void;
}) {
  const clusters = useClusters();
  const [hostname, setHostname] = useState(initial?.hostname ?? "");
  const [cluster, setCluster] = useState(clusterId ?? initial?.cluster_id ?? "");
  const [role, setRole] = useState(initial?.role ?? "");
  const [lifecycle, setLifecycle] = useState<NodeLifecycle>(initial?.lifecycle ?? "active");
  const [primaryIp, setPrimaryIp] = useState(initial?.primary_ip ?? "");
  const [description, setDescription] = useState(initial?.description ?? "");
  const [tags, setTags] = useState(initial?.tags.join(", ") ?? "");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  async function submit(e: FormEvent) {
    e.preventDefault();
    setError(null);
    setBusy(true);
    try {
      await onSubmit({
        hostname,
        cluster_id: cluster === "" ? null : cluster,
        role,
        lifecycle,
        primary_ip: primaryIp.trim() === "" ? null : primaryIp.trim(),
        description,
        tags: parseTags(tags),
      });
    } catch (err) {
      setError(err instanceof Error ? err.message : "Opslaan mislukt");
    } finally {
      setBusy(false);
    }
  }

  return (
    <form onSubmit={submit} className="space-y-4">
      {error && <Alert>{error}</Alert>}
      <div className="grid gap-4 sm:grid-cols-2">
        <Field label="Hostname" htmlFor="n-host">
          <Input id="n-host" required value={hostname} onChange={(e) => setHostname(e.target.value)} placeholder="lb1.lab.lan" />
        </Field>
        <Field label="Primair IP-adres" htmlFor="n-ip">
          <Input id="n-ip" value={primaryIp} onChange={(e) => setPrimaryIp(e.target.value)} placeholder="10.0.10.11" />
        </Field>
        {clusterId === undefined && (
          <Field label="Cluster" htmlFor="n-cluster">
            <Select id="n-cluster" value={cluster} onChange={(e) => setCluster(e.target.value)}>
              <option value="">Geen cluster</option>
              {clusters.data?.map((c) => (
                <option key={c.id} value={c.id}>
                  {c.name}
                </option>
              ))}
            </Select>
          </Field>
        )}
        <Field label="Rol" htmlFor="n-role" hint="Bijvoorbeeld master, backup of worker">
          <Input id="n-role" maxLength={64} value={role} onChange={(e) => setRole(e.target.value)} />
        </Field>
        <Field label="Lifecycle" htmlFor="n-life">
          <Select id="n-life" value={lifecycle} onChange={(e) => setLifecycle(e.target.value as NodeLifecycle)}>
            {lifecycles.map((l) => (
              <option key={l.value} value={l.value}>
                {l.label}
              </option>
            ))}
          </Select>
        </Field>
        <Field label="Tags" htmlFor="n-tags" hint="Gescheiden door komma's of spaties">
          <Input id="n-tags" value={tags} onChange={(e) => setTags(e.target.value)} />
        </Field>
      </div>
      <Field label="Beschrijving" htmlFor="n-desc">
        <Textarea id="n-desc" rows={2} maxLength={2000} value={description} onChange={(e) => setDescription(e.target.value)} />
      </Field>
      <div className="flex gap-2">
        <Button type="submit" disabled={busy}>
          {busy ? "Opslaan…" : submitLabel}
        </Button>
        {onCancel && (
          <Button type="button" variant="secondary" onClick={onCancel}>
            Annuleren
          </Button>
        )}
      </div>
    </form>
  );
}
