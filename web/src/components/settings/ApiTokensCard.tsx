"use client";

import { useState, type FormEvent } from "react";
import { ago } from "@/components/inventory/bits";
import { CopyBlock } from "@/components/inventory/InstallAgent";
import { Alert, Badge, Button, Card, Field, Input, Select } from "@/components/ui";
import { ApiError } from "@/lib/api/client";
import {
  homepageYaml,
  useApiTokens,
  useCreateApiToken,
  useDashboardPreview,
  useDeleteApiToken,
  type ApiToken,
  type DashboardCluster,
} from "@/lib/apitokens";
import { useClusters } from "@/lib/inventory";

const title = "Dashboard-API (homepage)";
const defaultBase = "/api/dashboard/v1";

// ApiTokensCard beheert de API-tokens waarmee een eigen dashboard (homepage)
// per cluster de belangrijkste cijfers leest. Een token kan alleen lezen; het
// token zelf ziet de beheerder één keer, met een kant-en-klare services.yaml.
export function ApiTokensCard({ isAdmin }: { isAdmin: boolean }) {
  const tokens = useApiTokens(isAdmin);
  const [creating, setCreating] = useState(false);
  const [origin] = useState(() => (typeof window === "undefined" ? "" : window.location.origin));
  const base = tokens.data?.base_path ?? defaultBase;
  return (
    <Card
      title={
        <span className="flex flex-wrap items-center justify-between gap-2">
          <span>{title}</span>
          {isAdmin && !creating && <Button onClick={() => setCreating(true)}>Token maken</Button>}
        </span>
      }
    >
      <div className="space-y-4 text-sm">
        <p className="text-slate-600 dark:text-slate-400">
          Een eigen dashboard leest per cluster de status, health score, nodes online, wie het VIP heeft, de database, open
          incidenten, updates, CrowdSec en de firewall. Met een API-token, alleen lezen, als{" "}
          <code className="font-mono text-xs">Authorization: Bearer cfa_…</code>. De velden zijn plat, zodat de customapi-tegel
          van homepage ze rechtstreeks toont.
        </p>
        <dl className="grid gap-x-6 gap-y-1 sm:grid-cols-[14rem_1fr]">
          <dt className="text-slate-500">Eén cluster</dt>
          <dd>
            <code className="text-xs break-all">
              {origin}
              {base}/clusters/&lt;slug&gt;
            </code>
            <span className="text-slate-500"> (met ?strict=1: HTTP 503 als het cluster niet in orde is)</span>
          </dd>
          <dt className="text-slate-500">Alle clusters (lijst)</dt>
          <dd>
            <code className="text-xs break-all">
              {origin}
              {base}/clusters
            </code>
          </dd>
          <dt className="text-slate-500">Samenvatting + clusters</dt>
          <dd>
            <code className="text-xs break-all">
              {origin}
              {base}/
            </code>
          </dd>
        </dl>
        {isAdmin && creating && <CreateToken origin={origin} base={base} onClose={() => setCreating(false)} />}
        {isAdmin && tokens.data && <TokenList items={tokens.data.items} />}
        {!isAdmin && <p className="text-slate-500">Alleen een beheerder maakt of trekt tokens in.</p>}
        <Preview />
      </div>
    </Card>
  );
}

function TokenList({ items }: { items: ApiToken[] }) {
  const remove = useDeleteApiToken();
  const [now] = useState(() => Date.now());
  if (items.length === 0) return <p className="text-slate-500">Nog geen tokens.</p>;
  return (
    <div className="overflow-x-auto">
      <table className="w-full text-left text-sm">
        <thead className="text-xs text-slate-500">
          <tr>
            <th className="py-1 pr-3 font-medium">Naam</th>
            <th className="py-1 pr-3 font-medium">Token</th>
            <th className="py-1 pr-3 font-medium">Clusters</th>
            <th className="py-1 pr-3 font-medium">Verloopt</th>
            <th className="py-1 pr-3 font-medium">Laatst gebruikt</th>
            <th />
          </tr>
        </thead>
        <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
          {items.map((t) => {
            const expired = !!t.expires_at && new Date(t.expires_at).getTime() < now;
            return (
              <tr key={t.id}>
                <td className="py-2 pr-3">{t.name}</td>
                <td className="py-2 pr-3">
                  <code className="text-xs">{t.prefix}…</code>
                </td>
                <td className="py-2 pr-3">{t.cluster_names.length ? t.cluster_names.join(", ") : "alle"}</td>
                <td className="py-2 pr-3">
                  {expired ? (
                    <Badge tone="red">verlopen</Badge>
                  ) : t.expires_at ? (
                    new Date(t.expires_at).toLocaleDateString("nl-BE")
                  ) : (
                    "nooit"
                  )}
                </td>
                <td className="py-2 pr-3">
                  {ago(t.last_used_at, now)}
                  {t.last_used_ip && <span className="text-slate-500"> · {t.last_used_ip}</span>}
                </td>
                <td className="py-2 text-right">
                  <Button
                    variant="secondary-danger"
                    className="px-2 py-1 text-xs"
                    disabled={remove.isPending}
                    onClick={() => {
                      if (window.confirm(`Token "${t.name}" intrekken? Het dashboard dat het gebruikt, krijgt meteen 401.`))
                        remove.mutate(t.id);
                    }}
                  >
                    Intrekken
                  </Button>
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}

function CreateToken({ origin, base, onClose }: { origin: string; base: string; onClose: () => void }) {
  const clusters = useClusters();
  const create = useCreateApiToken();
  const [name, setName] = useState("homepage");
  const [picked, setPicked] = useState<string[]>([]);
  const [ttl, setTtl] = useState("0");
  const [error, setError] = useState<string | null>(null);
  const [token, setToken] = useState<string | null>(null);

  async function submit(e: FormEvent) {
    e.preventDefault();
    setError(null);
    try {
      const res = await create.mutateAsync({ name, cluster_ids: picked, ttl_days: Number(ttl) });
      setToken(res.token);
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "Er ging iets mis.");
    }
  }

  if (token) {
    const shown = (clusters.data ?? []).filter((c) => picked.length === 0 || picked.includes(c.id));
    return (
      <div className="space-y-3 rounded-md border border-slate-200 p-4 dark:border-slate-800">
        <Alert kind="success">Token gemaakt. Kopieer het nu: ClusterForge toont het niet opnieuw.</Alert>
        <CopyBlock text={token} />
        <p className="text-slate-600 dark:text-slate-400">
          Voor homepage: plak dit in Dashboard bewerken → importeren (services.yaml). Elke cluster wordt een tegel; het token
          komt versleuteld bij de geheimen. Kies zelf welke velden je wilt (hoogstens 8 per tegel).
        </p>
        <CopyBlock text={homepageYaml(origin, base, token, shown)} />
        <Button variant="secondary" onClick={onClose}>
          Sluiten
        </Button>
      </div>
    );
  }

  return (
    <form onSubmit={submit} className="space-y-3 rounded-md border border-slate-200 p-4 dark:border-slate-800">
      {error && <Alert>{error}</Alert>}
      <Field label="Naam" htmlFor="api-token-name" hint="Waarvoor het token dient, bv. homepage.">
        <Input id="api-token-name" value={name} maxLength={100} required onChange={(e) => setName(e.target.value)} />
      </Field>
      <div>
        <span className="mb-1 block text-sm font-medium">Clusters</span>
        <p className="mb-2 text-xs text-slate-500">Niets aangevinkt = alle clusters, ook clusters die er later bijkomen.</p>
        <div className="grid gap-1 sm:grid-cols-2 lg:grid-cols-3">
          {(clusters.data ?? []).map((c) => (
            <label key={c.id} className="flex items-center gap-2">
              <input
                type="checkbox"
                checked={picked.includes(c.id)}
                onChange={(e) => setPicked(e.target.checked ? [...picked, c.id] : picked.filter((id) => id !== c.id))}
              />
              {c.name}
            </label>
          ))}
        </div>
      </div>
      <Field label="Geldig" htmlFor="api-token-ttl">
        <Select id="api-token-ttl" value={ttl} onChange={(e) => setTtl(e.target.value)}>
          <option value="0">Altijd (tot je het intrekt)</option>
          <option value="30">30 dagen</option>
          <option value="90">90 dagen</option>
          <option value="365">1 jaar</option>
        </Select>
      </Field>
      <div className="flex gap-2">
        <Button type="submit" disabled={create.isPending}>
          {create.isPending ? "Maken…" : "Token maken"}
        </Button>
        <Button type="button" variant="secondary" onClick={onClose}>
          Annuleren
        </Button>
      </div>
    </form>
  );
}

// Preview toont wat de API nu per cluster teruggeeft.
function Preview() {
  const [open, setOpen] = useState(false);
  const [slug, setSlug] = useState("");
  const q = useDashboardPreview(open);
  const clusters = q.data?.clusters ?? [];
  const cur: DashboardCluster | undefined = clusters.find((c) => c.slug === slug) ?? clusters[0];
  if (!open) {
    return (
      <Button variant="secondary" onClick={() => setOpen(true)}>
        Voorbeeld van het antwoord
      </Button>
    );
  }
  return (
    <div className="space-y-2">
      <div className="flex flex-wrap items-center gap-2">
        <Select value={cur?.slug ?? ""} onChange={(e) => setSlug(e.target.value)} className="max-w-xs">
          {clusters.map((c) => (
            <option key={c.id} value={c.slug}>
              {c.name}
            </option>
          ))}
        </Select>
        <Button variant="ghost" onClick={() => setOpen(false)}>
          Verbergen
        </Button>
      </div>
      {cur ? (
        <pre className="max-h-96 overflow-auto rounded-md bg-slate-900 p-3 text-xs text-slate-100 dark:bg-black">
          {JSON.stringify(cur, null, 2)}
        </pre>
      ) : (
        <p className="text-slate-500">{q.isLoading ? "Laden…" : "Nog geen clusters."}</p>
      )}
    </div>
  );
}
