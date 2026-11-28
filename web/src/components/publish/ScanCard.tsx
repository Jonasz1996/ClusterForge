"use client";

import { useMemo, useState } from "react";
import { ActionModal } from "@/components/publish/ActionModal";
import { ago } from "@/components/publish/bits";
import { Alert, Badge, Button, Card, Input, Select } from "@/components/ui";
import {
  usePublishOwners,
  usePublishScan,
  useDeletePolicy,
  useRunScan,
  useSavePolicy,
  useSetOwner,
  type PolicyMode,
  type PublishAction,
  type PublishOverview,
  type PublishPolicy,
  type ScanItem,
} from "@/lib/publish";

const kinds: { kind: ScanItem["kind"]; title: string; tone: "green" | "amber" | "red" }[] = [
  { kind: "add", title: "Toevoegen", tone: "green" },
  { kind: "update", title: "Bijwerken", tone: "amber" },
  { kind: "remove", title: "Verwijderen (eerst uitschakelen met een kopie)", tone: "red" },
];

const modeText: Record<PolicyMode, string> = {
  authentik: "Achter Authentik",
  never: "Nooit Authentik",
  own_login: "Eigen login (geen Authentik)",
  except_paths: "Authentik behalve paden",
};

// ScanCard is Scannen (fase 49): alleen op klik leest ClusterForge NPM,
// Authentik en de doelen en maakt het één plan in drie soorten. Goedkeuren
// (alles of per regel) wordt één taak: kopie van NPM, per host een echte
// aanvraag als controle, terugdraaien bij een fout.
export function ScanCard({ ov, admin }: { ov: PublishOverview; admin: boolean }) {
  const q = usePublishScan();
  const run = useRunScan();
  const [picked, setPicked] = useState<Set<string>>(new Set());
  const [apply, setApply] = useState<{ title: string; action: PublishAction } | null>(null);
  const v = q.data;
  const sc = v?.scan;
  const items = sc?.items ?? [];

  const toggle = (id: string) =>
    setPicked((p) => {
      const n = new Set(p);
      if (n.has(id)) n.delete(id);
      else n.add(id);
      return n;
    });
  const approve = (ids: string[], title: string) => sc && setApply({ title, action: { action: "scan", scan_id: sc.id, items: ids } });

  return (
    <Card title="Scannen">
      <p className="mb-3 text-sm text-slate-600 dark:text-slate-400">
        ClusterForge verandert niets vanzelf: pas als je op <b>Scannen</b> klikt leest het NPM, Authentik en elk doel opnieuw en maakt het één plan.
        Goedkeuren kan alles samen of per regel; dat wordt één taak met eerst een kopie van NPM, per host een echte aanvraag als controle en
        terugdraaien bij een fout. Verwijderen is eerst uitschakelen met een kopie; definitief verwijderen doe je later onder Uitgeschakeld.
      </p>
      {v && !v.manual_only && (
        <Alert kind="info">
          Automatisch publiceren en aanvullen staan nog aan (Instellingen → Domeinen en publicatie → &quot;Alleen via Scannen&quot;).
        </Alert>
      )}
      {run.isError && <Alert>{(run.error as Error).message}</Alert>}
      <div className="mb-3 flex flex-wrap items-center gap-3 text-sm">
        {admin && (
          <Button
            onClick={() => {
              setPicked(new Set());
              run.mutate();
            }}
            disabled={run.isPending || !ov.npm.linked}
          >
            {run.isPending ? "Scannen… (NPM, Authentik en doelen lezen)" : "Scannen"}
          </Button>
        )}
        {sc && (
          <span className="text-slate-500">
            Laatste scan {ago(sc.created_at)}: {sc.counts.add} toevoegen, {sc.counts.update} bijwerken, {sc.counts.remove} verwijderen
          </span>
        )}
        {admin && items.length > 0 && (
          <>
            <Button variant="secondary" onClick={() => approve([], `Alles goedkeuren (${items.length})`)}>
              Alles goedkeuren
            </Button>
            <Button variant="secondary" disabled={picked.size === 0} onClick={() => approve([...picked], `Gekozen regels goedkeuren (${picked.size})`)}>
              Gekozen goedkeuren ({picked.size})
            </Button>
          </>
        )}
      </div>
      {sc && items.length === 0 && <p className="text-sm text-slate-500">Niets te doen: alles klopt.</p>}
      {sc &&
        kinds.map((k) => {
          const list = items.filter((i) => i.kind === k.kind);
          if (list.length === 0) return null;
          const all = list.every((i) => picked.has(i.id));
          return (
            <div key={k.kind} className="mb-4">
              <div className="mb-1 flex items-center gap-2 text-sm font-medium">
                {admin && (
                  <input
                    type="checkbox"
                    aria-label={`Alle regels ${k.title}`}
                    checked={all}
                    onChange={() =>
                      setPicked((p) => {
                        const n = new Set(p);
                        list.forEach((i) => (all ? n.delete(i.id) : n.add(i.id)));
                        return n;
                      })
                    }
                  />
                )}
                <Badge tone={k.tone}>{list.length}</Badge> {k.title}
              </div>
              <ul className="divide-y divide-slate-100 text-sm dark:divide-slate-800">
                {list.map((i) => (
                  <li key={i.id} className="flex flex-wrap items-start justify-between gap-2 py-1.5">
                    <label className="flex min-w-0 flex-1 items-start gap-2">
                      {admin && <input type="checkbox" className="mt-1" checked={picked.has(i.id)} onChange={() => toggle(i.id)} />}
                      <span className="min-w-0">
                        <span className="font-medium">{i.title}</span>
                        {i.owner && <span className="ml-2 text-xs text-slate-500">eigenaar: {i.owner}</span>}
                        {i.detail && <span className="block text-xs break-words text-slate-500">{i.detail}</span>}
                      </span>
                    </label>
                    {admin && (
                      <button type="button" className="text-xs text-brand-700 underline dark:text-sky-300" onClick={() => approve([i.id], i.title)}>
                        goedkeuren
                      </button>
                    )}
                  </li>
                ))}
              </ul>
            </div>
          );
        })}
      {sc && sc.notes.length > 0 && (
        <details className="text-sm">
          <summary className="cursor-pointer text-slate-500">Bewust overgeslagen ({sc.notes.length})</summary>
          <ul className="mt-1 list-disc space-y-1 pl-5 text-xs text-slate-500">
            {sc.notes.map((n, i) => (
              <li key={i}>{n}</li>
            ))}
          </ul>
        </details>
      )}
      {v && v.disabled.length > 0 && (
        <div className="mt-4">
          <div className="mb-1 text-sm font-medium">Uitgeschakeld ({v.disabled.length})</div>
          <ul className="divide-y divide-slate-100 text-sm dark:divide-slate-800">
            {v.disabled.map((d) => (
              <li key={d.id} className="flex flex-wrap items-center justify-between gap-2 py-1.5">
                <span>
                  {d.domain} <Badge tone="slate">{d.kind === "npm" ? "NPM-host" : "Authentik"}</Badge>
                  <span className="ml-2 text-xs text-slate-500">{ago(d.disabled_at)}</span>
                </span>
                {admin && (
                  <span className="flex gap-3 text-xs">
                    <button
                      type="button"
                      className="text-brand-700 underline dark:text-sky-300"
                      onClick={() => setApply({ title: `${d.domain} terugzetten`, action: { action: "restore_disabled", disabled_id: d.id } })}
                    >
                      terugzetten
                    </button>
                    <button
                      type="button"
                      className="text-red-700 underline dark:text-red-300"
                      onClick={() => setApply({ title: `${d.domain} definitief verwijderen`, action: { action: "purge_disabled", disabled_id: d.id } })}
                    >
                      definitief verwijderen
                    </button>
                  </span>
                )}
              </li>
            ))}
          </ul>
        </div>
      )}
      {v && <Policies policies={v.policies} admin={admin} />}
      <Owners ov={ov} admin={admin} onDropFallback={(hostID, domain) => setApply({ title: `Terugval weghalen: ${domain}`, action: { action: "drop_fallback", host_id: hostID } })} />
      {apply && <ActionModal title={apply.title} action={apply.action} onClose={() => setApply(null)} />}
    </Card>
  );
}

function Policies({ policies, admin }: { policies: PublishPolicy[]; admin: boolean }) {
  const save = useSavePolicy();
  const del = useDeletePolicy();
  const [name, setName] = useState("");
  const [mode, setMode] = useState<PolicyMode>("never");
  const [paths, setPaths] = useState("");
  return (
    <details className="mt-4 text-sm">
      <summary className="cursor-pointer font-medium">Uitzonderingen per dienst ({policies.length})</summary>
      <p className="my-2 text-xs text-slate-500">
        Op het eerste deel van de naam (matomo voor matomo.jbogaert.be). Zonder uitzondering geldt de standaard: achter Authentik, behalve de woorden
        bij &quot;Niet achter Authentik&quot; in de instellingen.
      </p>
      {(save.isError || del.isError) && <Alert>{((save.error ?? del.error) as Error).message}</Alert>}
      <ul className="divide-y divide-slate-100 dark:divide-slate-800">
        {policies.map((p) => (
          <li key={p.name} className="flex flex-wrap items-center justify-between gap-2 py-1.5">
            <span>
              <span className="font-medium">{p.name}</span> <Badge tone={p.mode === "authentik" ? "green" : p.mode === "except_paths" ? "amber" : "slate"}>{modeText[p.mode]}</Badge>
              {p.paths.length > 0 && <code className="ml-2 text-xs">{p.paths.join(" ")}</code>}
              {p.note && <span className="ml-2 text-xs text-slate-500">{p.note}</span>}
            </span>
            {admin && (
              <span className="flex gap-3 text-xs">
                <button
                  type="button"
                  className="text-brand-700 underline dark:text-sky-300"
                  onClick={() => {
                    setName(p.name);
                    setMode(p.mode);
                    setPaths(p.paths.join(" "));
                  }}
                >
                  wijzigen
                </button>
                <button type="button" className="text-red-700 underline dark:text-red-300" onClick={() => del.mutate(p.name)}>
                  weghalen
                </button>
              </span>
            )}
          </li>
        ))}
      </ul>
      {admin && (
        <div className="mt-2 flex flex-wrap items-center gap-2">
          <Input placeholder="naam, bv. matomo" value={name} onChange={(e) => setName(e.target.value)} className="max-w-[12rem]" aria-label="Naam" />
          <Select value={mode} onChange={(e) => setMode(e.target.value as PolicyMode)} className="max-w-xs" aria-label="Soort">
            {(Object.keys(modeText) as PolicyMode[]).map((m) => (
              <option key={m} value={m}>
                {modeText[m]}
              </option>
            ))}
          </Select>
          {mode === "except_paths" && (
            <Input placeholder="/api /guide.xml" value={paths} onChange={(e) => setPaths(e.target.value)} className="max-w-xs" aria-label="Paden" />
          )}
          <Button
            variant="secondary"
            disabled={!name.trim() || save.isPending}
            onClick={() =>
              save.mutate(
                { name: name.trim(), mode, paths: paths.split(/[\s,]+/).filter(Boolean) },
                {
                  onSuccess: () => {
                    setName("");
                    setPaths("");
                  },
                },
              )
            }
          >
            Bewaren
          </Button>
        </div>
      )}
    </details>
  );
}

function Owners({ ov, admin, onDropFallback }: { ov: PublishOverview; admin: boolean; onDropFallback: (hostID: number, domain: string) => void }) {
  const q = usePublishOwners();
  const set = useSetOwner();
  const [filter, setFilter] = useState("");
  const rows = useMemo(() => {
    const f = filter.trim().toLowerCase();
    return (q.data?.owners ?? []).filter((o) => !f || `${o.domain} ${o.cluster_name ?? ""} ${o.service_name ?? ""}`.toLowerCase().includes(f));
  }, [q.data, filter]);
  const moved = rows.filter((o) => o.fallback);
  if (!q.data) return null;
  return (
    <details className="mt-4 text-sm">
      <summary className="cursor-pointer font-medium">
        Eigenaars ({q.data.owners.length}
        {q.data.without.length > 0 ? `, ${q.data.without.length} zonder` : ""}
        {moved.length > 0 ? `, ${moved.length} met terugval` : ""})
      </summary>
      <p className="my-2 text-xs text-slate-500">
        Elke NPM-host en Authentik-app hoort bij één cluster of dienst. Wil een scan een naam aanmaken die al van een andere cluster is, dan wordt het een
        verhuizing (doel omzetten) met de oude als terugval tot je die weghaalt. Testclusters krijgen standaard een eigen subdomein (app.test.
        {ov.settings.domain}).
      </p>
      {set.isError && <Alert>{(set.error as Error).message}</Alert>}
      <Input placeholder="Zoeken" value={filter} onChange={(e) => setFilter(e.target.value)} className="mb-2 max-w-xs" />
      <ul className="divide-y divide-slate-100 dark:divide-slate-800">
        {rows.map((o) => (
          <li key={o.domain} className="flex flex-wrap items-center justify-between gap-2 py-1.5">
            <span className="min-w-0">
              <span className="font-medium">{o.domain}</span>
              <span className="ml-2 text-xs text-slate-500">
                {[o.cluster_name, o.service_name].filter(Boolean).join(" / ")}
                {o.source === "hand" ? " (met de hand)" : ""}
                {o.host_id ? "" : " · geen NPM-host"}
                {o.ak_slug ? ` · app ${o.ak_slug}` : ""}
              </span>
              {o.fallback && (
                <span className="block text-xs text-amber-700 dark:text-amber-300">
                  terugval naar {o.fallback.scheme}://{o.fallback.host}:{o.fallback.port}
                  {o.fallback.cluster ? ` (${o.fallback.cluster})` : ""}
                </span>
              )}
            </span>
            {admin && (
              <span className="flex flex-wrap items-center gap-3 text-xs">
                {o.fallback && o.host_id && (
                  <button type="button" className="text-brand-700 underline dark:text-sky-300" onClick={() => onDropFallback(o.host_id!, o.domain)}>
                    terugval weghalen
                  </button>
                )}
                <Select
                  value={o.cluster_id ?? ""}
                  onChange={(e) => e.target.value && set.mutate({ domain: o.domain, cluster_id: e.target.value })}
                  className="max-w-[12rem] text-xs"
                  aria-label={`Eigenaar van ${o.domain}`}
                >
                  <option value="">cluster kiezen</option>
                  {ov.clusters.map((c) => (
                    <option key={c.id} value={c.id}>
                      {c.name}
                    </option>
                  ))}
                </Select>
              </span>
            )}
          </li>
        ))}
      </ul>
      {q.data.without.length > 0 && (
        <p className="mt-2 text-xs text-slate-500">
          Zonder eigenaar (ClusterForge kent de dienst niet): {q.data.without.join(", ")}. Kies een cluster via een scan of hieronder.
        </p>
      )}
      {admin && q.data.without.length > 0 && (
        <WithoutOwner names={q.data.without} ov={ov} onSet={(domain, cluster) => set.mutate({ domain, cluster_id: cluster })} />
      )}
    </details>
  );
}

function WithoutOwner({ names, ov, onSet }: { names: string[]; ov: PublishOverview; onSet: (domain: string, cluster: string) => void }) {
  const [domain, setDomain] = useState("");
  const [cluster, setCluster] = useState("");
  return (
    <div className="mt-2 flex flex-wrap items-center gap-2">
      <Select value={domain} onChange={(e) => setDomain(e.target.value)} className="max-w-xs" aria-label="Naam zonder eigenaar">
        <option value="">naam</option>
        {names.map((n) => (
          <option key={n} value={n}>
            {n}
          </option>
        ))}
      </Select>
      <Select value={cluster} onChange={(e) => setCluster(e.target.value)} className="max-w-xs" aria-label="Cluster">
        <option value="">cluster</option>
        {ov.clusters.map((c) => (
          <option key={c.id} value={c.id}>
            {c.name}
          </option>
        ))}
      </Select>
      <Button variant="secondary" disabled={!domain || !cluster} onClick={() => onSet(domain, cluster)}>
        Eigenaar zetten
      </Button>
    </div>
  );
}
