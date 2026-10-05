"use client";

import { useState, type FormEvent } from "react";
import { Alert, Button, Field, Input, Select } from "@/components/ui";
import { useClusters, useCreateEnrollmentToken, type EnrollmentToken } from "@/lib/inventory";

// installCommand is de regel die je op de node plakt.
export function installCommand(token: string) {
  const origin = typeof window === "undefined" ? "https://clusterforge.example" : window.location.origin;
  return `curl -fsSL ${origin}/install/agent.sh | sudo sh -s -- --server ${origin} --token ${token}`;
}

export function CopyBlock({ text }: { text: string }) {
  const [copied, setCopied] = useState(false);
  return (
    <div className="relative">
      <pre className="rounded-md bg-slate-900 p-3 pr-24 text-xs leading-relaxed break-all whitespace-pre-wrap text-slate-100 dark:bg-black">
        <code>{text}</code>
      </pre>
      <Button
        type="button"
        variant="secondary"
        className="absolute top-2 right-2 px-2 py-1 text-xs"
        onClick={async () => {
          await navigator.clipboard.writeText(text);
          setCopied(true);
          setTimeout(() => setCopied(false), 2000);
        }}
      >
        {copied ? "Gekopieerd" : "Kopiëren"}
      </Button>
    </div>
  );
}

// InstallAgent maakt een enrollmenttoken en toont het installatiecommando.
// Met nodeId geldt het token alleen voor die node.
export function InstallAgent({ nodeId, onClose }: { nodeId?: string; onClose: () => void }) {
  const clusters = useClusters();
  const create = useCreateEnrollmentToken();
  const [description, setDescription] = useState("");
  const [clusterId, setClusterId] = useState("");
  const [maxUses, setMaxUses] = useState("1");
  const [ttl, setTtl] = useState("24");
  const [error, setError] = useState<string | null>(null);
  const [result, setResult] = useState<(EnrollmentToken & { token: string }) | null>(null);

  async function submit(e: FormEvent) {
    e.preventDefault();
    setError(null);
    try {
      const t = await create.mutateAsync({
        description,
        node_id: nodeId ?? null,
        cluster_id: nodeId || clusterId === "" ? null : clusterId,
        max_uses: nodeId ? 1 : Number(maxUses),
        ttl_hours: Number(ttl),
      });
      setResult(t);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Token maken mislukt");
    }
  }

  if (result) {
    const expires = new Intl.DateTimeFormat("nl-BE", { dateStyle: "medium", timeStyle: "short" }).format(
      new Date(result.expires_at),
    );
    return (
      <div className="space-y-3">
        <p className="text-sm">
          Draai dit als root op de {nodeId ? "node" : "nieuwe node"}. Het token is{" "}
          {result.max_uses === 1 ? "één keer" : `${result.max_uses} keer`} bruikbaar tot {expires} en wordt hierna niet
          meer getoond.
        </p>
        <CopyBlock text={installCommand(result.token)} />
        <p className="text-xs text-slate-500">
          Het script haalt cf-agent van deze server, controleert de checksum, installeert de systemd-service en meldt de
          node aan. De node moet poort 4222 van deze server kunnen bereiken.
        </p>
        <Button variant="secondary" onClick={onClose}>
          Sluiten
        </Button>
      </div>
    );
  }

  return (
    <form onSubmit={submit} className="space-y-4">
      {error && <Alert>{error}</Alert>}
      <div className="grid gap-4 sm:grid-cols-2">
        <Field label="Omschrijving" htmlFor="t-desc" hint="Optioneel, bijvoorbeeld voor welke servers">
          <Input id="t-desc" maxLength={200} value={description} onChange={(e) => setDescription(e.target.value)} />
        </Field>
        {!nodeId && (
          <Field label="Nieuwe nodes in cluster" htmlFor="t-cluster" hint="Bestaande nodes met dezelfde hostname blijven waar ze zijn">
            <Select id="t-cluster" value={clusterId} onChange={(e) => setClusterId(e.target.value)}>
              <option value="">Geen cluster</option>
              {clusters.data?.map((c) => (
                <option key={c.id} value={c.id}>
                  {c.name}
                </option>
              ))}
            </Select>
          </Field>
        )}
        {!nodeId && (
          <Field label="Aantal keer bruikbaar" htmlFor="t-uses" hint="Eén token voor meerdere servers tegelijk">
            <Input id="t-uses" type="number" min={1} max={100} value={maxUses} onChange={(e) => setMaxUses(e.target.value)} />
          </Field>
        )}
        <Field label="Geldig" htmlFor="t-ttl">
          <Select id="t-ttl" value={ttl} onChange={(e) => setTtl(e.target.value)}>
            <option value="1">1 uur</option>
            <option value="24">24 uur</option>
            <option value="168">7 dagen</option>
            <option value="720">30 dagen</option>
          </Select>
        </Field>
      </div>
      <div className="flex gap-2">
        <Button type="submit" disabled={create.isPending}>
          {create.isPending ? "Bezig…" : "Installatiecommando maken"}
        </Button>
        <Button type="button" variant="secondary" onClick={onClose}>
          Annuleren
        </Button>
      </div>
    </form>
  );
}
