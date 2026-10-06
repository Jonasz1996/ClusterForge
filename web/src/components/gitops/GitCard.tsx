"use client";

import Link from "next/link";
import { Button, Card } from "@/components/ui";
import { exportUrl, useGitChanges, useGitFiles, useGitRepo, useUnlinkCluster } from "@/lib/gitops";
import type { ClusterDetail } from "@/lib/inventory";
import { CommitLink, FileStateBadge } from "./bits";
import { LinkButton } from "./LinkButton";

// GitCard toont bij een cluster uit een template of het in Git staat, met
// de toestand van zijn bestand, een wachtend plan en export, koppelen of
// ontkoppelen.
export function GitCard({ c, isAdmin }: { c: ClusterDetail; isAdmin: boolean }) {
  const repo = useGitRepo();
  const hasRepo = !!repo.data?.repo;
  const files = useGitFiles(hasRepo);
  const pending = useGitChanges({ cluster_id: c.id, status: "pending" }, hasRepo);
  const unlink = useUnlinkCluster();
  const file = files.data?.find((f) => f.cluster_id === c.id);
  const change = pending.data?.[0];

  return (
    <Card
      title={
        <span className="flex flex-wrap items-center justify-between gap-2">
          <span>Git</span>
          <span className="flex flex-wrap gap-2">
            <a
              href={exportUrl(c.id)}
              download
              className="rounded-md border border-slate-300 bg-white px-3.5 py-2 text-sm font-medium text-slate-800 hover:bg-slate-50 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-100"
            >
              Exporteren
            </a>
            {isAdmin && !c.git_managed && file?.state === "unlinked" && <LinkButton clusterId={c.id} path={file.path} />}
            {isAdmin && c.git_managed && (
              <Button
                variant="secondary"
                disabled={unlink.isPending}
                onClick={() => {
                  if (
                    window.confirm(
                      `${c.name} van Git ontkoppelen? ClusterForge negeert het bestand daarna en het cluster is weer hier te wijzigen. Een wachtend plan vervalt.`,
                    )
                  )
                    unlink.mutate(c.id);
                }}
              >
                Ontkoppelen
              </Button>
            )}
          </span>
        </span>
      }
    >
      <div className="space-y-2 text-sm">
        {c.git_managed ? (
          <>
            <p>
              Dit cluster komt uit{" "}
              <a
                href={c.git_repo_url}
                target="_blank"
                rel="noreferrer"
                className="font-mono text-xs text-brand-700 hover:underline dark:text-sky-300"
              >
                {file?.path ?? c.git_repo_url}
              </a>
              . Naam, omgeving, tags, nodes en VIP&apos;s wijzig je daar; node-acties zoals herstarten en onderhoud blijven hier.
            </p>
            {file && (
              <p className="flex flex-wrap items-center gap-2">
                <FileStateBadge state={file.state} />
                <span className="text-slate-500">
                  commit <CommitLink sha={file.commit} />
                </span>
              </p>
            )}
            {file?.errors.length ? (
              <ul className="space-y-1 text-xs text-red-700 dark:text-red-300">
                {file.errors.map((e, i) => (
                  <li key={i}>
                    {e.line > 0 && `regel ${e.line} · `}
                    <code>{e.field}</code>: {e.message}
                  </li>
                ))}
              </ul>
            ) : null}
            {change && (
              <p>
                <Link href={`/gitops/wijziging?id=${change.id}`} className="font-medium text-brand-700 hover:underline dark:text-sky-300">
                  Plan uit commit {change.commit.sha.slice(0, 7)} bekijken
                </Link>
                <span className="text-slate-500"> · {change.summary}</span>
              </p>
            )}
          </>
        ) : (
          <p className="text-slate-600 dark:text-slate-400">
            {!hasRepo ? (
              <>
                Dit cluster staat niet in Git. Koppel eerst een repository bij{" "}
                <Link href="/gitops" className="text-brand-700 hover:underline dark:text-sky-300">
                  GitOps
                </Link>
                .
              </>
            ) : file ? (
              <>
                <span className="font-mono text-xs">{file.path}</span> staat in Git maar is niet gekoppeld
                {file.state === "invalid" ? " en heeft fouten; zie GitOps." : ". Koppelen lukt als het bestand gelijk is aan de export."}
              </>
            ) : (
              <>
                Dit cluster staat niet in Git. Exporteer het, commit het bestand als{" "}
                <code className="font-mono text-xs">
                  {repo.data?.repo?.path}/{c.slug}/cluster.yaml
                </code>{" "}
                en koppel het daarna.
              </>
            )}
          </p>
        )}
      </div>
    </Card>
  );
}
