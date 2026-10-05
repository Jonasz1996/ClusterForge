"use client";

import Link from "next/link";
import { useRouter, useSearchParams } from "next/navigation";
import { Suspense, useState } from "react";
import { NodeForm } from "@/components/inventory/NodeForm";
import { LifecycleBadge, QueryState, Tags } from "@/components/inventory/bits";
import { Alert, Button, Card, PageHeader } from "@/components/ui";
import { useDeleteNode, useIsAdmin, useNode, useUpdateNode } from "@/lib/inventory";

export default function NodeDetailPage() {
  return (
    <Suspense>
      <NodeDetailInner />
    </Suspense>
  );
}

function NodeDetailInner() {
  const id = useSearchParams().get("id") ?? "";
  const node = useNode(id);
  const isAdmin = useIsAdmin();
  const update = useUpdateNode();
  const remove = useDeleteNode();
  const router = useRouter();
  const [editing, setEditing] = useState(false);

  if (id === "") return <Alert>Geen node opgegeven.</Alert>;
  const fmt = new Intl.DateTimeFormat("nl-BE", { dateStyle: "medium", timeStyle: "short" });
  const none = <span className="text-slate-400">Geen</span>;

  return (
    <QueryState q={node}>
      {node.data && (
        <div className="space-y-6">
          <div className="text-sm">
            <Link href="/nodes" className="text-slate-500 hover:underline">
              ← Nodes
            </Link>
          </div>
          <PageHeader
            title={node.data.hostname}
            description={
              <span className="inline-flex flex-wrap items-center gap-2">
                <LifecycleBadge lifecycle={node.data.lifecycle} />
                {node.data.cluster_id ? (
                  <Link href={`/clusters/detail?id=${node.data.cluster_id}`} className="hover:underline">
                    {node.data.cluster_name}
                  </Link>
                ) : (
                  <span>Zonder cluster</span>
                )}
              </span>
            }
            actions={
              isAdmin &&
              !editing && (
                <>
                  <Button variant="secondary" onClick={() => setEditing(true)}>
                    Bewerken
                  </Button>
                  <Button
                    variant="danger"
                    disabled={remove.isPending}
                    onClick={async () => {
                      if (!window.confirm(`Node ${node.data!.hostname} verwijderen?`)) return;
                      await remove.mutateAsync(node.data!.id);
                      router.push("/nodes");
                    }}
                  >
                    Verwijderen
                  </Button>
                </>
              )
            }
          />

          {editing ? (
            <Card title="Node bewerken">
              <NodeForm
                initial={node.data}
                submitLabel="Opslaan"
                onCancel={() => setEditing(false)}
                onSubmit={async (v) => {
                  await update.mutateAsync({ id, body: v });
                  setEditing(false);
                }}
              />
            </Card>
          ) : (
            <Card>
              <dl className="grid gap-x-6 gap-y-3 text-sm sm:grid-cols-[10rem_1fr]">
                <dt className="text-slate-500">Primair IP-adres</dt>
                <dd className="font-mono text-xs">{node.data.primary_ip ?? none}</dd>
                <dt className="text-slate-500">Rol</dt>
                <dd>{node.data.role || none}</dd>
                <dt className="text-slate-500">Beschrijving</dt>
                <dd>{node.data.description || none}</dd>
                <dt className="text-slate-500">Tags</dt>
                <dd>{node.data.tags.length ? <Tags tags={node.data.tags} /> : none}</dd>
                <dt className="text-slate-500">Aangemaakt</dt>
                <dd>{fmt.format(new Date(node.data.created_at))}</dd>
                <dt className="text-slate-500">Laatst gewijzigd</dt>
                <dd>{fmt.format(new Date(node.data.updated_at))}</dd>
              </dl>
            </Card>
          )}

          <Alert kind="info">
            Hardware, OS-versie, services en metrics verschijnen hier zodra de agent op deze node draait (mijlpaal 3 en 4).
          </Alert>
        </div>
      )}
    </QueryState>
  );
}
