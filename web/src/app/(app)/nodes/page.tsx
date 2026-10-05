"use client";

import Link from "next/link";
import { useRouter } from "next/navigation";
import { useMemo, useState } from "react";
import { InstallAgent } from "@/components/inventory/InstallAgent";
import { NodeForm } from "@/components/inventory/NodeForm";
import { AgentBadge, Empty, LifecycleBadge, QueryState, StatusBadge, Tags } from "@/components/inventory/bits";
import { Button, Card, Input, PageHeader, Select } from "@/components/ui";
import {
  lifecycles,
  useCreateNode,
  useDeleteEnrollmentToken,
  useEnrollmentTokens,
  useIsAdmin,
  useNodes,
  type Node,
} from "@/lib/inventory";

type Group = { key: string; title: string; href?: string; nodes: Node[] };

export default function NodesPage() {
  const nodes = useNodes(true);
  const isAdmin = useIsAdmin();
  const create = useCreateNode();
  const router = useRouter();
  const [adding, setAdding] = useState(false);
  const [installing, setInstalling] = useState(false);
  const [search, setSearch] = useState("");
  const [lifecycle, setLifecycle] = useState("");

  // Nodes gegroepeerd per cluster, als boom; losse nodes onderaan.
  const groups = useMemo(() => {
    const s = search.trim().toLowerCase();
    const match = (n: Node) =>
      (lifecycle === "" || n.lifecycle === lifecycle) &&
      (s === "" ||
        n.hostname.toLowerCase().includes(s) ||
        (n.primary_ip ?? "").includes(s) ||
        n.role.toLowerCase().includes(s) ||
        n.tags.some((t) => t.includes(s)) ||
        (n.cluster_name ?? "").toLowerCase().includes(s));
    const byCluster = new Map<string, Group>();
    const loose: Group = { key: "", title: "Zonder cluster", nodes: [] };
    for (const n of nodes.data ?? []) {
      if (!match(n)) continue;
      if (n.cluster_id === null) {
        loose.nodes.push(n);
        continue;
      }
      let g = byCluster.get(n.cluster_id);
      if (!g) {
        g = { key: n.cluster_id, title: n.cluster_name ?? "", href: `/clusters/detail?id=${n.cluster_id}`, nodes: [] };
        byCluster.set(n.cluster_id, g);
      }
      g.nodes.push(n);
    }
    const out = [...byCluster.values()].sort((a, b) => a.title.localeCompare(b.title, "nl"));
    if (loose.nodes.length) out.push(loose);
    return out;
  }, [nodes.data, search, lifecycle]);

  return (
    <div className="space-y-6">
      <PageHeader
        title="Nodes"
        description="Alle servers, per cluster."
        actions={
          isAdmin &&
          !adding &&
          !installing && (
            <>
              <Button variant="secondary" onClick={() => setAdding(true)}>
                Nieuwe node
              </Button>
              <Button onClick={() => setInstalling(true)}>Agent installeren</Button>
            </>
          )
        }
      />

      {installing && (
        <Card title="Agent installeren">
          <InstallAgent onClose={() => setInstalling(false)} />
        </Card>
      )}

      {adding && (
        <Card title="Nieuwe node">
          <NodeForm
            submitLabel="Aanmaken"
            onCancel={() => setAdding(false)}
            onSubmit={async (v) => {
              const n = await create.mutateAsync(v);
              router.push(`/nodes/detail?id=${n.id}`);
            }}
          />
        </Card>
      )}

      <QueryState q={nodes}>
        {nodes.data?.length === 0 ? (
          <Empty>
            Nog geen nodes.{" "}
            {isAdmin
              ? "Installeer de agent op een server; die meldt zich dan zelf aan. Of voeg een node met de hand toe."
              : "Een beheerder kan ze toevoegen."}
          </Empty>
        ) : (
          <>
            <div className="flex flex-wrap gap-2">
              <Input
                className="max-w-xs"
                placeholder="Zoeken op hostname, IP, rol of tag"
                aria-label="Zoeken"
                value={search}
                onChange={(e) => setSearch(e.target.value)}
              />
              <Select className="w-auto" aria-label="Lifecycle" value={lifecycle} onChange={(e) => setLifecycle(e.target.value)}>
                <option value="">Elke lifecycle</option>
                {lifecycles.map((l) => (
                  <option key={l.value} value={l.value}>
                    {l.label}
                  </option>
                ))}
              </Select>
            </div>
            {groups.length === 0 && <p className="text-sm text-slate-500">Geen nodes gevonden met dit filter.</p>}
            <div className="space-y-4">
              {groups.map((g) => (
                <section key={g.key} className="rounded-lg border border-slate-200 bg-white dark:border-slate-800 dark:bg-slate-900">
                  <h2 className="flex items-center justify-between border-b border-slate-200 px-4 py-2.5 text-sm font-semibold dark:border-slate-800">
                    {g.href ? (
                      <Link href={g.href} className="hover:underline">
                        {g.title}
                      </Link>
                    ) : (
                      <span className="text-slate-500">{g.title}</span>
                    )}
                    <span className="text-xs font-normal text-slate-500">
                      {g.nodes.length} {g.nodes.length === 1 ? "node" : "nodes"}
                    </span>
                  </h2>
                  <ul className="divide-y divide-slate-100 dark:divide-slate-800">
                    {g.nodes.map((n) => (
                      <li key={n.id} className="flex flex-wrap items-center gap-x-4 gap-y-1 px-4 py-2.5 text-sm">
                        <span className="text-slate-300 dark:text-slate-600" aria-hidden>
                          └
                        </span>
                        <Link
                          href={`/nodes/detail?id=${n.id}`}
                          className="min-w-40 font-medium text-brand-700 hover:underline dark:text-sky-300"
                        >
                          {n.hostname}
                        </Link>
                        <span className="min-w-28 font-mono text-xs text-slate-600 dark:text-slate-400">{n.primary_ip ?? "–"}</span>
                        {n.role && <span className="text-slate-600 dark:text-slate-400">{n.role}</span>}
                        {n.agent ? <StatusBadge status={n.status} reason={n.status_reason} /> : <AgentBadge agent={null} />}
                        {n.lifecycle !== "active" && <LifecycleBadge lifecycle={n.lifecycle} />}
                        <Tags tags={n.tags} />
                      </li>
                    ))}
                  </ul>
                </section>
              ))}
            </div>
          </>
        )}
      </QueryState>

      {isAdmin && <ActiveTokens />}
    </div>
  );
}

function ActiveTokens() {
  const tokens = useEnrollmentTokens(true);
  const remove = useDeleteEnrollmentToken();
  if (!tokens.data?.length) return null;
  const fmt = new Intl.DateTimeFormat("nl-BE", { dateStyle: "short", timeStyle: "short" });
  return (
    <Card title="Bruikbare enrollmenttokens">
      <ul className="divide-y divide-slate-100 text-sm dark:divide-slate-800">
        {tokens.data.map((t) => (
          <li key={t.id} className="flex flex-wrap items-center justify-between gap-2 py-2">
            <span>
              {t.description || (t.node_hostname ? `Voor ${t.node_hostname}` : "Zonder omschrijving")}
              <span className="text-slate-500">
                {" "}
                · {t.uses} van {t.max_uses} gebruikt · geldig tot {fmt.format(new Date(t.expires_at))}
                {t.cluster_name && ` · naar ${t.cluster_name}`}
              </span>
            </span>
            <Button
              variant="ghost"
              className="px-2 py-1 text-xs text-red-600!"
              onClick={() => {
                if (window.confirm("Dit token intrekken? Servers die het nog niet gebruikten kunnen zich er dan niet meer mee aanmelden."))
                  void remove.mutateAsync(t.id);
              }}
            >
              Intrekken
            </Button>
          </li>
        ))}
      </ul>
    </Card>
  );
}
