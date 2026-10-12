"use client";

import Link from "next/link";
import { usePathname, useRouter, useSearchParams } from "next/navigation";
import { Suspense, useMemo, useState } from "react";
import { EnvBadge, QueryState } from "@/components/inventory/bits";
import { DocSummaryButtons } from "@/components/docs/DocSummary";
import { Diff } from "@/components/gitops/bits";
import { Markdown, parse, slug } from "@/components/Markdown";
import { Alert, Badge, Button, cx, PageHeader, Select } from "@/components/ui";
import {
  docHref,
  downloadUrl,
  formatTime,
  formatWhen,
  kindLabels,
  resolveDocLink,
  useDoc,
  useDocDiff,
  useDocHistory,
  useDocIndex,
  useDocsTarget,
  useGenerateDocs,
  type DocClusterIndex,
  type DocIndex,
  type DocRef,
  type DocSummaryMeta,
} from "@/lib/docs";
import { useIsAdmin } from "@/lib/inventory";

const chapters = ["architectuur", "netwerk", "configuratie", "herstel", "runbooks"];

// chaptersOf zijn de hoofdstukken van een cluster, zoals de server ze
// noemt: een Kubernetes-cluster heeft er een meer.
function chaptersOf(c: DocClusterIndex | null | undefined) {
  const kinds = c?.docs.map((d) => d.kind) ?? [];
  return kinds.length > 0 ? kinds : chapters;
}

export default function DocsPage() {
  return (
    <Suspense>
      <DocsInner />
    </Suspense>
  );
}

// Het document staat in de URL: ?cluster=<slug>&hoofdstuk=<soort>, en
// eventueel &versie=<id> voor een oudere versie.
function DocsInner() {
  const params = useSearchParams();
  const router = useRouter();
  const pathname = usePathname();
  const isAdmin = useIsAdmin();
  const index = useDocIndex();
  const generate = useGenerateDocs();
  const [generated, setGenerated] = useState<number | null>(null);

  const slugParam = params.get("cluster");
  const kindParam = params.get("hoofdstuk") ?? "";
  const versionParam = Number(params.get("versie")) || undefined;
  const cluster = index.data?.clusters.find((c) => c.slug === slugParam) ?? null;
  const ref: DocRef | null = !index.data
    ? null
    : cluster
      ? { scope: "cluster", kind: chaptersOf(cluster).includes(kindParam) ? kindParam : "architectuur", cluster: cluster.id }
      : { scope: "overview", kind: "overzicht" };

  const open = (s: string | null, kind: string, version?: number) => {
    const href = docHref(s, kind);
    router.push(version ? `${href}${href.includes("?") ? "&" : "?"}versie=${version}` : href || pathname, { scroll: true });
  };

  return (
    <div className="space-y-5">
      <div className="print:hidden">
        <PageHeader
          title="Documentatie"
          description="ClusterForge schrijft dit zelf uit wat er draait: tien minuten na een wijziging en elke nacht opnieuw. Geheimen staan er nooit in."
          actions={
            <>
              <a
                href={downloadUrl}
                download
                className="inline-flex items-center justify-center rounded-md border border-slate-300 bg-white px-3.5 py-2 text-sm font-medium text-slate-800 hover:bg-slate-50 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-100 dark:hover:bg-slate-800"
              >
                Downloaden
              </a>
              <Button variant="secondary" onClick={() => window.print()}>
                Afdrukken
              </Button>
              {isAdmin && (
                <Button
                  disabled={generate.isPending}
                  onClick={() => generate.mutate(undefined, { onSuccess: (r) => setGenerated(r.changed) })}
                >
                  {generate.isPending ? "Bijwerken…" : "Nu bijwerken"}
                </Button>
              )}
            </>
          }
        />
      </div>
      <GitLine isAdmin={isAdmin} />
      {generate.isError && <Alert>{generate.error.message}</Alert>}
      {generated !== null && (
        <Alert kind="success">
          {generated === 0
            ? "Alles was al bij: geen enkel document veranderde."
            : generated === 1
              ? "Eén document kreeg een nieuwe versie."
              : `${generated} documenten kregen een nieuwe versie.`}
        </Alert>
      )}
      <QueryState q={index}>
        {index.data && ref && (
          <div className="grid gap-6 lg:grid-cols-[14rem_minmax(0,1fr)] print:block">
            <Tree index={index.data} current={ref} onOpen={open} />
            <DocView
              key={`${ref.scope}/${ref.kind}/${ref.cluster ?? ""}`}
              docRef={ref}
              chapters={chaptersOf(cluster)}
              slug={cluster?.slug ?? null}
              clusterName={cluster?.name}
              env={cluster?.environment}
              version={versionParam}
              queuedUntil={cluster ? cluster.queued_until : index.data.queued_until}
              summary={isAdmin ? (cluster?.summary ?? null) : undefined}
              onOpen={open}
            />
          </div>
        )}
      </QueryState>
    </div>
  );
}

// GitLine zegt of en waar de documentatie in GitHub staat.
function GitLine({ isAdmin }: { isAdmin: boolean }) {
  const q = useDocsTarget();
  const t = q.data?.target;
  if (!q.data?.enabled || (!t && !isAdmin)) return null;
  const settings = (
    <Link href="/instellingen#documentatie-github" className="text-brand-700 hover:underline dark:text-sky-300">
      {t ? "Instellingen" : "Naar GitHub schrijven instellen"}
    </Link>
  );
  if (!t) return <p className="text-sm print:hidden">{settings}</p>;
  return (
    <p className="flex flex-wrap items-center gap-x-2 gap-y-1 text-sm text-slate-600 print:hidden dark:text-slate-400">
      <span>
        In GitHub:{" "}
        <a href={t.folder_url} target="_blank" rel="noreferrer" className="text-brand-700 hover:underline dark:text-sky-300">
          {t.repository}
          {t.path ? `/${t.path}` : ""}
        </a>{" "}
        op {t.branch}
      </span>
      {t.last_commit && (
        <span>
          · laatste commit{" "}
          <a href={t.last_commit.url} target="_blank" rel="noreferrer" className="font-mono text-xs hover:underline">
            {t.last_commit.sha.slice(0, 7)}
          </a>{" "}
          op {formatWhen(t.last_commit.at)}
        </span>
      )}
      {!t.enabled && <Badge tone="amber">Gepauzeerd</Badge>}
      {t.last_error && <Badge tone="red">Schrijven mislukt</Badge>}
      <span>· {settings}</span>
    </p>
  );
}

function Tree({
  index,
  current,
  onOpen,
}: {
  index: DocIndex;
  current: DocRef;
  onOpen: (slug: string | null, kind: string) => void;
}) {
  const value = current.scope === "overview" ? "overzicht" : `${current.cluster}|${current.kind}`;
  const item = (active: boolean) =>
    cx(
      "block rounded-md px-2.5 py-1.5 text-sm",
      active
        ? "bg-brand-50 font-medium text-brand-700 dark:bg-slate-800 dark:text-white"
        : "text-slate-700 hover:bg-slate-100 dark:text-slate-300 dark:hover:bg-slate-800",
    );
  return (
    <nav aria-label="Documenten" className="print:hidden">
      {/* Op een smal scherm een keuzelijst. */}
      <Select
        className="w-full lg:hidden"
        aria-label="Document"
        value={value}
        onChange={(e) => {
          const [id, kind] = e.target.value.split("|");
          if (e.target.value === "overzicht") onOpen(null, "overzicht");
          else onOpen(index.clusters.find((c) => c.id === id)?.slug ?? null, kind);
        }}
      >
        <option value="overzicht">Overzicht</option>
        {index.clusters.map((c) => (
          <optgroup key={c.id} label={c.name}>
            {chaptersOf(c).map((k) => (
              <option key={k} value={`${c.id}|${k}`}>
                {c.name}: {kindLabels[k].toLowerCase()}
              </option>
            ))}
          </optgroup>
        ))}
      </Select>
      <div className="hidden space-y-4 lg:block">
        <Link href={docHref(null, "overzicht")} className={item(current.scope === "overview")}>
          Overzicht
        </Link>
        <div>
          <div className="px-2.5 pb-1 text-xs font-medium uppercase tracking-wide text-slate-500">Clusters</div>
          {index.clusters.length === 0 && <p className="px-2.5 text-sm text-slate-500">Nog geen clusters.</p>}
          <ul className="space-y-0.5">
            {index.clusters.map((c) => {
              const here = current.cluster === c.id;
              return (
                <li key={c.id}>
                  <Link
                    href={docHref(c.slug, "architectuur")}
                    className={cx(item(here && current.kind === "architectuur"), "flex items-center justify-between gap-2")}
                  >
                    <span className="truncate">{c.name}</span>
                    <EnvBadge env={c.environment} />
                  </Link>
                  {here && (
                    <ul className="mt-0.5 ml-3 border-l border-slate-200 pl-2 dark:border-slate-800">
                      {chaptersOf(c)
                        .slice(1)
                        .map((k) => (
                          <li key={k}>
                            <Link href={docHref(c.slug, k)} className={item(current.kind === k)}>
                              {kindLabels[k]}
                            </Link>
                          </li>
                        ))}
                    </ul>
                  )}
                </li>
              );
            })}
          </ul>
        </div>
      </div>
    </nav>
  );
}

// body laat de titel en de navigatieregel weg: die staan al op de pagina.
function body(content: string) {
  const lines = content.split("\n");
  let i = 0;
  if (lines[i]?.startsWith("# ")) i++;
  while (lines[i] === "") i++;
  if (lines[i]?.startsWith("[Overzicht](")) i++;
  return lines.slice(i).join("\n");
}

function DocView({
  docRef,
  chapters,
  slug: clusterSlug,
  clusterName,
  env,
  version,
  queuedUntil,
  summary,
  onOpen,
}: {
  docRef: DocRef;
  chapters: string[];
  slug: string | null;
  clusterName?: string;
  env?: string;
  version?: number;
  queuedUntil?: string;
  // De samenvatting 'In het kort', of null als er geen is; undefined voor
  // wie ze niet mag laten schrijven.
  summary?: DocSummaryMeta | null;
  onOpen: (slug: string | null, kind: string, version?: number) => void;
}) {
  const doc = useDoc(docRef, version);
  const [showHistory, setShowHistory] = useState(!!version);
  const content = doc.data ? body(doc.data.content) : "";
  const toc = useMemo(
    () =>
      parse(content).flatMap((b) =>
        b.kind === "heading" && b.level === 2 ? [{ id: slug(b.text), text: b.text.replace(/\\(.)/g, "$1") }] : [],
      ),
    [content],
  );
  const title = docRef.scope === "overview" ? "Overzicht" : `${clusterName}: ${kindLabels[docRef.kind].toLowerCase()}`;
  const old = version !== undefined && doc.data?.version === version;

  return (
    <div className="min-w-0 space-y-4">
      {docRef.scope === "cluster" && (
        <div
          className="hidden gap-1 overflow-x-auto border-b border-slate-200 lg:flex print:hidden dark:border-slate-800"
          role="tablist"
        >
          {chapters.map((k) => (
            <button
              key={k}
              type="button"
              role="tab"
              aria-selected={docRef.kind === k}
              onClick={() => onOpen(clusterSlug, k)}
              className={cx(
                "-mb-px shrink-0 border-b-2 px-3 py-2 text-sm font-medium",
                docRef.kind === k
                  ? "border-brand-600 text-brand-700 dark:border-sky-400 dark:text-white"
                  : "border-transparent text-slate-600 hover:text-slate-900 dark:text-slate-400 dark:hover:text-white",
              )}
            >
              {kindLabels[k]}
            </button>
          ))}
        </div>
      )}
      <article className="rounded-lg border border-slate-200 bg-white p-5 shadow-xs sm:p-6 print:border-0 print:p-0 print:shadow-none dark:border-slate-800 dark:bg-slate-900">
        <header className="mb-5 flex flex-wrap items-start justify-between gap-3 border-b border-slate-100 pb-4 dark:border-slate-800">
          <div className="min-w-0">
            <h2 className="flex flex-wrap items-center gap-2 text-xl font-semibold tracking-tight">
              {title}
              {env && (
                <span className="print:hidden">
                  <EnvBadge env={env} />
                </span>
              )}
              {old && <Badge tone="amber">Oudere versie</Badge>}
            </h2>
            <p className="mt-1 text-xs text-slate-500">
              {doc.data?.generated_at ? (
                <>
                  Geschreven op {formatWhen(doc.data.generated_at)}
                  {doc.data.cause_text && <span className="print:hidden"> · {doc.data.cause_text}</span>}
                </>
              ) : doc.data ? (
                "Nog niet bewaard: zo ziet het er nu uit."
              ) : null}
              {queuedUntil && !old && (
                <span className="print:hidden"> · Een wijziging komt erin rond {formatTime(queuedUntil)}.</span>
              )}
            </p>
          </div>
          <div className="flex flex-wrap gap-2 print:hidden">
            {summary !== undefined && docRef.scope === "cluster" && docRef.kind === "architectuur" && !old && docRef.cluster && (
              <DocSummaryButtons cluster={docRef.cluster} name={clusterName ?? ""} summary={summary} />
            )}
            {old && (
              <Button variant="secondary" onClick={() => onOpen(clusterSlug, docRef.kind)}>
                Nieuwste versie
              </Button>
            )}
            <Button variant="ghost" aria-expanded={showHistory} onClick={() => setShowHistory((v) => !v)}>
              Geschiedenis
            </Button>
          </div>
        </header>
        {showHistory && <History docRef={docRef} slug={clusterSlug} current={doc.data?.version} onOpen={onOpen} />}
        <QueryState q={doc}>
          {toc.length > 2 && (
            <nav aria-label="Op deze pagina" className="mb-5 flex flex-wrap gap-x-3 gap-y-1 text-xs print:hidden">
              <span className="text-slate-500">Op deze pagina:</span>
              {toc.map((t) => (
                <a key={t.id} href={`#${t.id}`} className="text-brand-600 hover:underline dark:text-sky-300">
                  {t.text}
                </a>
              ))}
            </nav>
          )}
          <Markdown
            text={content}
            anchors
            resolveHref={(h) => resolveDocLink(h, clusterSlug)}
            className="docs space-y-4 text-sm leading-relaxed [&_h3]:mt-8 [&_h3]:border-b [&_h3]:border-slate-100 [&_h3]:pb-1 [&_h3]:text-base dark:[&_h3]:border-slate-800 [&_h4]:mt-5"
          />
        </QueryState>
      </article>
    </div>
  );
}

function History({
  docRef,
  slug: clusterSlug,
  current,
  onOpen,
}: {
  docRef: DocRef;
  slug: string | null;
  current?: number;
  onOpen: (slug: string | null, kind: string, version?: number) => void;
}) {
  const hist = useDocHistory(docRef, true);
  const [pair, setPair] = useState<[number, number] | null>(null);
  const diff = useDocDiff(docRef, pair?.[0], pair?.[1]);
  const versions = hist.data?.versions ?? [];
  return (
    <div className="mb-6 space-y-3 rounded-md border border-slate-200 bg-slate-50 p-4 print:hidden dark:border-slate-800 dark:bg-slate-950/40">
      <h3 className="text-sm font-semibold">Geschiedenis</h3>
      <QueryState q={hist}>
        {versions.length === 0 ? (
          <p className="text-sm text-slate-500">Er is nog geen versie bewaard. De eerste komt na de start of na een wijziging.</p>
        ) : (
          <>
            <p className="text-xs text-slate-500">
              Een nieuwe versie komt er alleen als de tekst veranderde. ClusterForge bewaart de laatste {hist.data?.keep}.
            </p>
            <ul className="divide-y divide-slate-200 text-sm dark:divide-slate-800">
              {versions.map((v, i) => {
                const prev = versions[i + 1];
                const shown = current === v.id;
                return (
                  <li key={v.id} className="flex flex-wrap items-center justify-between gap-2 py-2">
                    <div className="min-w-0">
                      <span className={cx("font-medium", shown && "text-brand-700 dark:text-sky-300")}>
                        {formatWhen(v.generated_at)}
                      </span>
                      {i === 0 && (
                        <span className="ml-2">
                          <Badge tone="green">Nieuwste</Badge>
                        </span>
                      )}
                      <div className="text-xs text-slate-500">{v.cause_text}</div>
                    </div>
                    <div className="flex gap-1">
                      {!shown && (
                        <Button
                          variant="ghost"
                          className="px-2 py-1 text-xs"
                          onClick={() => onOpen(clusterSlug, docRef.kind, i === 0 ? undefined : v.id)}
                        >
                          Bekijken
                        </Button>
                      )}
                      {prev && (
                        <Button
                          variant={pair?.[1] === v.id ? "secondary" : "ghost"}
                          className="px-2 py-1 text-xs"
                          onClick={() => setPair(pair?.[1] === v.id ? null : [prev.id, v.id])}
                        >
                          Wat veranderde
                        </Button>
                      )}
                    </div>
                  </li>
                );
              })}
            </ul>
          </>
        )}
      </QueryState>
      {pair && (
        <QueryState q={diff}>
          {diff.data &&
            (diff.data.changed < 0 ? (
              <Alert kind="info">Deze versies zijn te groot om te vergelijken.</Alert>
            ) : (
              <div className="space-y-1">
                <p className="text-xs text-slate-500">
                  {diff.data.changed === 1 ? "1 regel" : `${diff.data.changed} regels`} veranderd tussen{" "}
                  {formatWhen(versions.find((v) => v.id === pair[0])!.generated_at)} en{" "}
                  {formatWhen(versions.find((v) => v.id === pair[1])!.generated_at)}.
                </p>
                <Diff text={diff.data.diff} />
              </div>
            ))}
        </QueryState>
      )}
    </div>
  );
}
