"use client";

import { Badge } from "@/components/ui";
import { changeStatuses, commitTitle, fileStates, shortSha, type GitChangeStatus, type GitCommit, type GitFileState } from "@/lib/gitops";

export function FileStateBadge({ state }: { state: GitFileState }) {
  const s = fileStates[state];
  return <Badge tone={s.tone}>{s.label}</Badge>;
}

export function ChangeStatusBadge({ status }: { status: GitChangeStatus }) {
  const s = changeStatuses[status];
  return <Badge tone={s.tone}>{s.label}</Badge>;
}

// CommitLink is de korte sha met een link naar de commit.
export function CommitLink({ sha, url }: { sha: string; url?: string }) {
  if (!sha) return <span className="text-slate-400">–</span>;
  return url ? (
    <a href={url} target="_blank" rel="noreferrer" className="font-mono text-xs text-brand-700 hover:underline dark:text-sky-300">
      {shortSha(sha)}
    </a>
  ) : (
    <code className="text-xs">{shortSha(sha)}</code>
  );
}

// CommitLine toont sha, bericht, auteur en of GitHub de commit geverifieerd vindt.
export function CommitLine({ c }: { c: GitCommit }) {
  const fmt = new Intl.DateTimeFormat("nl-BE", {
    dateStyle: "medium",
    timeStyle: "short",
  });
  return (
    <span className="inline-flex flex-wrap items-center gap-x-2 gap-y-1">
      <CommitLink sha={c.sha} url={c.url} />
      <span className="font-medium">&ldquo;{commitTitle(c)}&rdquo;</span>
      <span className="text-slate-500">
        {c.author} · {fmt.format(new Date(c.date))}
      </span>
      {c.verified ? <Badge tone="green">Geverifieerd</Badge> : <Badge>Niet ondertekend</Badge>}
    </span>
  );
}

// Diff toont een unified diff met kleur per regel.
export function Diff({ text }: { text: string }) {
  return (
    <pre className="overflow-x-auto rounded-md bg-slate-950 p-3 font-mono text-xs leading-5 text-slate-200">
      {text
        .replace(/\n$/, "")
        .split("\n")
        .map((line, i) => {
          const cls =
            line.startsWith("+++") || line.startsWith("---")
              ? "text-slate-400"
              : line.startsWith("@@")
                ? "text-sky-300"
                : line.startsWith("+")
                  ? "bg-emerald-950 text-emerald-300"
                  : line.startsWith("-")
                    ? "bg-red-950 text-red-300"
                    : "";
          return (
            <span key={i} className={`block ${cls}`}>
              {line || " "}
            </span>
          );
        })}
    </pre>
  );
}
