"use client";

import Link from "next/link";
import { useRouter } from "next/navigation";
import { useMemo, useState } from "react";
import { ClusterForm } from "@/components/inventory/ClusterForm";
import { Empty, EnvBadge, QueryState, Tags, tableClass, tdClass, thClass } from "@/components/inventory/bits";
import { Button, Card, Input, PageHeader, Select } from "@/components/ui";
import { environments, typeLabel, useClusters, useCreateCluster, useIsAdmin } from "@/lib/inventory";

export default function ClustersPage() {
  const clusters = useClusters();
  const isAdmin = useIsAdmin();
  const create = useCreateCluster();
  const router = useRouter();
  const [adding, setAdding] = useState(false);
  const [search, setSearch] = useState("");
  const [env, setEnv] = useState("");

  const filtered = useMemo(() => {
    const s = search.trim().toLowerCase();
    return (clusters.data ?? []).filter(
      (c) =>
        (env === "" || c.environment === env) &&
        (s === "" ||
          c.name.toLowerCase().includes(s) ||
          c.slug.includes(s) ||
          c.tags.some((t) => t.includes(s)) ||
          typeLabel(c.type).toLowerCase().includes(s)),
    );
  }, [clusters.data, search, env]);

  return (
    <div className="space-y-6">
      <PageHeader
        title="Clusters"
        description="Groepen nodes die samen één dienst leveren."
        actions={isAdmin && !adding && <Button onClick={() => setAdding(true)}>Nieuw cluster</Button>}
      />

      {adding && (
        <Card title="Nieuw cluster">
          <ClusterForm
            submitLabel="Aanmaken"
            onCancel={() => setAdding(false)}
            onSubmit={async (v) => {
              const c = await create.mutateAsync(v);
              router.push(`/clusters/detail?id=${c.id}`);
            }}
          />
        </Card>
      )}

      <QueryState q={clusters}>
        {clusters.data?.length === 0 ? (
          <Empty>
            Nog geen clusters.{" "}
            {isAdmin ? "Maak er een aan met de knop hierboven." : "Een beheerder kan er een aanmaken."}
          </Empty>
        ) : (
          <>
            <div className="flex flex-wrap gap-2">
              <Input
                className="max-w-xs"
                placeholder="Zoeken op naam, type of tag"
                aria-label="Zoeken"
                value={search}
                onChange={(e) => setSearch(e.target.value)}
              />
              <Select className="w-auto" aria-label="Omgeving" value={env} onChange={(e) => setEnv(e.target.value)}>
                <option value="">Alle omgevingen</option>
                {environments.map((e) => (
                  <option key={e.value} value={e.value}>
                    {e.label}
                  </option>
                ))}
              </Select>
            </div>
            <div className="overflow-x-auto rounded-lg border border-slate-200 bg-white dark:border-slate-800 dark:bg-slate-900">
              <table className={tableClass}>
                <thead className="border-b border-slate-200 dark:border-slate-800">
                  <tr>
                    <th className={thClass}>Naam</th>
                    <th className={thClass}>Type</th>
                    <th className={thClass}>Omgeving</th>
                    <th className={`${thClass} text-right`}>Nodes</th>
                    <th className={`${thClass} text-right`}>VIP&apos;s</th>
                    <th className={thClass}>Tags</th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
                  {filtered.map((c) => (
                    <tr key={c.id} className="hover:bg-slate-50 dark:hover:bg-slate-800/50">
                      <td className={tdClass}>
                        <Link href={`/clusters/detail?id=${c.id}`} className="font-medium text-brand-700 hover:underline dark:text-sky-300">
                          {c.name}
                        </Link>
                        <div className="text-xs text-slate-500">{c.slug}</div>
                      </td>
                      <td className={tdClass}>{typeLabel(c.type)}</td>
                      <td className={tdClass}>
                        <EnvBadge env={c.environment} />
                      </td>
                      <td className={`${tdClass} text-right tabular-nums`}>{c.node_count}</td>
                      <td className={`${tdClass} text-right tabular-nums`}>{c.vip_count}</td>
                      <td className={tdClass}>
                        <Tags tags={c.tags} />
                      </td>
                    </tr>
                  ))}
                  {filtered.length === 0 && (
                    <tr>
                      <td colSpan={6} className="px-3 py-6 text-center text-sm text-slate-500">
                        Geen clusters gevonden met dit filter.
                      </td>
                    </tr>
                  )}
                </tbody>
              </table>
            </div>
          </>
        )}
      </QueryState>
    </div>
  );
}
