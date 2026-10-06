"use client";

import Link from "next/link";
import { useState } from "react";
import { ChangeStatusBadge, CommitLine, CommitLink, FileStateBadge } from "@/components/gitops/bits";
import { LinkButton } from "@/components/gitops/LinkButton";
import { RepoForm, TokenHelp } from "@/components/gitops/RepoForm";
import { ago, Empty, EnvBadge, QueryState, tableClass, tdClass, thClass } from "@/components/inventory/bits";
import { Alert, Button, Card, PageHeader } from "@/components/ui";
import {
  commitTitle,
  exportUrl,
  useDeleteGitRepo,
  useGitChanges,
  useGitFiles,
  useGitRepo,
  useSaveGitRepo,
  useSyncGit,
  type GitRepo,
} from "@/lib/gitops";
import { useClusters, useIsAdmin } from "@/lib/inventory";

export default function GitOpsPage() {
  const status = useGitRepo();
  const isAdmin = useIsAdmin();
  const repo = status.data?.repo ?? null;
  const enabled = status.data?.enabled ?? true;

  return (
    <div className="space-y-6">
      <PageHeader
        title="GitOps"
        description="Clusters uit een template beheren vanuit één GitHub-repository. ClusterForge leest elke minuut, controleert elk bestand en toont wat een wijziging op elke node zou doen."
      />
      {!enabled && (
        <Alert kind="info">
          De server heeft nog geen masterkey om het GitHub-token versleuteld te bewaren. Zet <code className="font-mono">CF_MASTER_KEY</code> in de
          omgeving van de server (maak er een met <code className="font-mono">openssl rand -hex 32</code>) en herstart hem.
        </Alert>
      )}
      <QueryState q={status}>
        {status.data && enabled && <RepoCard repo={repo} isAdmin={isAdmin} interval={status.data.interval_seconds} />}
        {repo && (
          <>
            <FilesCard repo={repo} isAdmin={isAdmin} />
            <ChangesCard />
          </>
        )}
        {status.data && <NotInGitCard isAdmin={isAdmin} hasRepo={repo !== null} />}
      </QueryState>
    </div>
  );
}

function RepoCard({ repo, isAdmin, interval }: { repo: GitRepo | null; isAdmin: boolean; interval: number }) {
  const save = useSaveGitRepo();
  const remove = useDeleteGitRepo();
  const sync = useSyncGit();
  const [editing, setEditing] = useState(false);

  if (!repo || editing) {
    if (!isAdmin) return <Empty>Er is nog geen Git-repository gekoppeld. Een beheerder kan dat hier doen.</Empty>;
    return (
      <Card title={repo ? "Koppeling wijzigen" : "Repository koppelen"}>
        <TokenHelp />
        <RepoForm
          initial={repo ?? undefined}
          onCancel={repo ? () => setEditing(false) : undefined}
          onSubmit={async (v) => {
            await save.mutateAsync(v);
            setEditing(false);
          }}
        />
      </Card>
    );
  }
  return (
    <Card
      title={
        <span className="flex flex-wrap items-center justify-between gap-2">
          <span>Koppeling</span>
          {isAdmin && (
            <span className="flex flex-wrap gap-2">
              <Button variant="secondary" disabled={sync.isPending} onClick={() => sync.mutate()}>
                {sync.isPending ? "Synchroniseren…" : "Nu synchroniseren"}
              </Button>
              <Button variant="secondary" onClick={() => setEditing(true)}>
                Bewerken
              </Button>
              <Button
                variant="ghost"
                disabled={remove.isPending}
                onClick={() => {
                  if (
                    window.confirm(
                      `Koppeling met ${repo.repository} verwijderen? Alle clusters worden ontkoppeld en zijn daarna weer in ClusterForge te wijzigen; wachtende wijzigingen vervallen.`,
                    )
                  )
                    remove.mutate();
                }}
              >
                Verwijderen
              </Button>
            </span>
          )}
        </span>
      }
    >
      <dl className="grid gap-x-6 gap-y-2 text-sm sm:grid-cols-[10rem_1fr]">
        <dt className="text-slate-500">Repository</dt>
        <dd>
          <a href={repo.web_url} target="_blank" rel="noreferrer" className="text-brand-700 hover:underline dark:text-sky-300">
            {repo.repository}
          </a>
        </dd>
        <dt className="text-slate-500">Branch en map</dt>
        <dd>
          <code className="text-xs">{repo.branch}</code> · <code className="text-xs">{repo.path}/</code>
        </dd>
        <dt className="text-slate-500">Laatste commit</dt>
        <dd>{repo.head ? <CommitLine c={repo.head} /> : <span className="text-slate-400">Nog niet gelezen</span>}</dd>
        <dt className="text-slate-500">Gelezen</dt>
        <dd>
          {repo.last_sync_at ? ago(repo.last_sync_at) : "nog niet"}{" "}
          <span className="text-slate-500">· elke {interval >= 60 ? `${Math.round(interval / 60)} minuut` : `${interval} seconden`}</span>
        </dd>
      </dl>
      {repo.last_error && (
        <div className="mt-4">
          <Alert>De laatste synchronisatie mislukte: {repo.last_error}. ClusterForge houdt de vorige stand tot het weer lukt.</Alert>
        </div>
      )}
      {sync.isError && (
        <div className="mt-4">
          <Alert>{sync.error.message}</Alert>
        </div>
      )}
    </Card>
  );
}

function FilesCard({ repo, isAdmin }: { repo: GitRepo; isAdmin: boolean }) {
  const files = useGitFiles();
  return (
    <Card title="Bestanden">
      <QueryState q={files}>
        {files.data?.length === 0 ? (
          <Empty>
            Geen clusterbestanden onder <code className="font-mono">{repo.path}/</code>. Exporteer hieronder een cluster en commit het als{" "}
            <code className="font-mono">{repo.path}/&lt;slug&gt;/cluster.yaml</code>.
          </Empty>
        ) : (
          <div className="overflow-x-auto">
            <table className={tableClass}>
              <thead className="border-b border-slate-200 dark:border-slate-800">
                <tr>
                  <th className={thClass}>Bestand</th>
                  <th className={thClass}>Cluster</th>
                  <th className={thClass}>Commit</th>
                  <th className={thClass}>Toestand</th>
                  <th className={thClass} />
                </tr>
              </thead>
              <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
                {files.data?.map((f) => (
                  <tr key={f.path}>
                    <td className={tdClass}>
                      <a href={f.url} target="_blank" rel="noreferrer" className="font-mono text-xs text-brand-700 hover:underline dark:text-sky-300">
                        {f.path}
                      </a>
                      {f.errors.length > 0 && (
                        <ul className="mt-2 space-y-1 text-xs text-red-700 dark:text-red-300">
                          {f.errors.map((e, i) => (
                            <li key={i}>
                              {e.line > 0 && <span className="font-medium">regel {e.line}</span>}
                              {e.line > 0 && " · "}
                              <code>{e.field}</code>: {e.message}
                            </li>
                          ))}
                        </ul>
                      )}
                      {f.secret && (
                        <p className="mt-1 text-xs text-slate-500">
                          Er staat een geheim in dit bestand. ClusterForge bewaart de inhoud niet; haal het geheim uit de geschiedenis van de
                          repository en vervang het.
                        </p>
                      )}
                    </td>
                    <td className={tdClass}>
                      {f.cluster_id ? (
                        <Link href={`/clusters/detail?id=${f.cluster_id}`} className="text-brand-700 hover:underline dark:text-sky-300">
                          {f.cluster_name}
                        </Link>
                      ) : (
                        <span className="text-slate-400">{f.slug}</span>
                      )}
                    </td>
                    <td className={tdClass}>
                      <CommitLink sha={f.commit} />
                    </td>
                    <td className={tdClass}>
                      <FileStateBadge state={f.state} />
                    </td>
                    <td className={`${tdClass} text-right whitespace-nowrap`}>
                      {f.change_id && (
                        <Link href={`/gitops/wijziging?id=${f.change_id}`} className="text-sm text-brand-700 hover:underline dark:text-sky-300">
                          Plan bekijken
                        </Link>
                      )}
                      {f.state === "unlinked" && isAdmin && f.cluster_id && <LinkButton clusterId={f.cluster_id} path={f.path} small />}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </QueryState>
    </Card>
  );
}

function ChangesCard() {
  const changes = useGitChanges({});
  return (
    <Card title="Wijzigingen">
      <QueryState q={changes}>
        {changes.data?.length === 0 ? (
          <Empty>Nog geen wijzigingen. Een commit die een gekoppeld cluster verandert, verschijnt hier als plan.</Empty>
        ) : (
          <ul className="divide-y divide-slate-100 dark:divide-slate-800">
            {changes.data?.map((c) => (
              <li key={c.id} className="flex flex-wrap items-start justify-between gap-3 py-3 text-sm">
                <div className="min-w-0 space-y-1">
                  <div className="flex flex-wrap items-center gap-2">
                    <Link href={`/gitops/wijziging?id=${c.id}`} className="font-medium text-brand-700 hover:underline dark:text-sky-300">
                      {c.kind === "create" ? `Nieuw cluster ${c.cluster_name || c.slug}` : `Wijziging voor ${c.cluster_name || c.slug}`}
                    </Link>
                    <EnvBadge env={c.cluster_environment} />
                    <ChangeStatusBadge status={c.status} />
                  </div>
                  <p className="text-slate-600 dark:text-slate-400">{c.summary}</p>
                  <p className="text-xs text-slate-500">
                    <CommitLink sha={c.commit.sha} url={c.commit.url} /> &ldquo;
                    {commitTitle(c.commit)}&rdquo; · {c.commit.author}
                    {c.reason && ` · ${c.reason}`}
                  </p>
                </div>
                <span className="text-xs text-slate-500">{ago(c.created_at)}</span>
              </li>
            ))}
          </ul>
        )}
      </QueryState>
    </Card>
  );
}

// NotInGitCard toont de clusters uit een template die nog niet in Git staan,
// met hun export.
function NotInGitCard({ isAdmin, hasRepo }: { isAdmin: boolean; hasRepo: boolean }) {
  const clusters = useClusters();
  const list = (clusters.data ?? []).filter((c) => c.template_name && !c.git_managed);
  if (list.length === 0) return null;
  return (
    <Card title="Clusters nog niet in Git">
      <p className="mb-3 text-sm text-slate-600 dark:text-slate-400">
        Exporteer een cluster, commit het bestand ongewijzigd
        {hasRepo ? " en koppel het daarna bij Bestanden" : ""}. Vanaf dan is Git de bron van waarheid. Geheimen staan niet in de export.
      </p>
      <ul className="divide-y divide-slate-100 dark:divide-slate-800">
        {list.map((c) => (
          <li key={c.id} className="flex flex-wrap items-center justify-between gap-3 py-2 text-sm">
            <span className="flex flex-wrap items-center gap-2">
              <Link href={`/clusters/detail?id=${c.id}`} className="font-medium text-brand-700 hover:underline dark:text-sky-300">
                {c.name}
              </Link>
              <EnvBadge env={c.environment} />
              <span className="text-slate-500">
                {c.template_name} {c.template_version}
              </span>
            </span>
            <a
              href={exportUrl(c.id)}
              download
              className="rounded-md border border-slate-300 bg-white px-3 py-1.5 text-xs font-medium text-slate-800 hover:bg-slate-50 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-100"
            >
              Exporteren
            </a>
          </li>
        ))}
      </ul>
      {!isAdmin && <p className="mt-3 text-xs text-slate-500">Koppelen kan alleen een beheerder.</p>}
    </Card>
  );
}
