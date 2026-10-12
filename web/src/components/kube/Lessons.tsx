"use client";

import Link from "next/link";
import { useState, type FormEvent, type ReactNode } from "react";
import { DoneAlert, KubeActions, type ActionDone } from "@/components/kube/Actions";
import { ApplyPanel, useApplyFlow } from "@/components/kube/Apply";
import { CodeBlock, KubeError } from "@/components/kube/Kube";
import { Alert, Badge, Button, Card, Input, cx } from "@/components/ui";
import { useIsAdmin } from "@/lib/inventory";
import {
  objectHref,
  useKubeLesson,
  useKubeLessonAnswer,
  useKubeLessonReset,
  useKubeLessons,
  type KubeLesson,
  type KubeLessonLink,
  type KubeLessonStep,
  type KubeLessonSummary,
} from "@/lib/kube";

// De namespace waarin de lessen werken; Opruimen haalt hem weg.
const learnNamespace = "leren";

// rich zet `code` tussen backticks om in code.
function rich(text: string) {
  return text.split("`").map((part, i) =>
    i % 2 === 1 ? (
      <code key={i} className="rounded bg-slate-100 px-1 font-mono text-[0.85em] break-words dark:bg-slate-800">
        {part}
      </code>
    ) : (
      part
    ),
  );
}

// LessonsTab toont de lessen, of één les als er een gekozen is.
export function LessonsTab({
  id,
  lesson,
  namespaces,
  onLesson,
}: {
  id: string;
  lesson: string;
  namespaces: string[];
  onLesson: (lesson: string | undefined) => void;
}) {
  const lessons = useKubeLessons(id);
  if (lessons.isLoading) return <p className="text-sm text-slate-500">Laden…</p>;
  if (lessons.isError) return <KubeError error={lessons.error} />;
  const all = lessons.data ?? [];
  if (lesson !== "" && all.some((l) => l.id === lesson)) {
    return <LessonView id={id} lessonId={lesson} all={all} onLesson={onLesson} />;
  }
  return <LessonList id={id} all={all} hasNamespace={namespaces.includes(learnNamespace)} onLesson={onLesson} />;
}

function Progress({ done, total }: { done: number; total: number }) {
  return (
    <div className="h-1.5 overflow-hidden rounded-full bg-slate-100 dark:bg-slate-800" aria-hidden>
      <div
        className={cx("h-full rounded-full", done === total ? "bg-emerald-500" : "bg-brand-600 dark:bg-sky-400")}
        style={{ width: `${total > 0 ? Math.round((done / total) * 100) : 0}%` }}
      />
    </div>
  );
}

function lessonState(l: KubeLessonSummary) {
  if (l.unavailable) return { label: "Later", tone: "slate" as const };
  if (l.finished_at) return { label: "Af", tone: "green" as const };
  if (l.started_at) return { label: "Bezig", tone: "blue" as const };
  return null;
}

function LessonList({
  id,
  all,
  hasNamespace,
  onLesson,
}: {
  id: string;
  all: KubeLessonSummary[];
  hasNamespace: boolean;
  onLesson: (lesson: string) => void;
}) {
  const isAdmin = useIsAdmin();
  const [done, setDone] = useState<ActionDone | null>(null);
  const finished = all.filter((l) => l.finished_at).length;
  const open = all.filter((l) => !l.unavailable).length;
  return (
    <div className="space-y-4">
      <Card>
        <div className="space-y-2 text-sm leading-relaxed">
          <p className="text-base font-semibold">Kubernetes leren op je eigen cluster</p>
          <p>
            Elke les heeft een paar opdrachten. Je doet ze hier in ClusterForge of met kubectl op een server, en ClusterForge
            kijkt op het cluster of het gelukt is. Het verandert zelf niets. De lessen bouwen op elkaar voort: begin bij les 1.
          </p>
          <p className="text-slate-600 dark:text-slate-400">
            {finished} van {open} lessen af. Alles wat je maakt, komt in de namespace <code className="font-mono">leren</code>.
          </p>
        </div>
      </Card>
      <DoneAlert done={done} onClose={() => setDone(null)} />

      <ol className="grid gap-3 sm:grid-cols-2">
        {all.map((l) => {
          const st = lessonState(l);
          return (
            <li key={l.id}>
              <button
                type="button"
                disabled={!!l.unavailable}
                onClick={() => onLesson(l.id)}
                className={cx(
                  "flex h-full w-full flex-col gap-2 rounded-lg border bg-white p-4 text-left shadow-xs transition dark:bg-slate-900",
                  l.unavailable
                    ? "cursor-not-allowed border-slate-200 opacity-70 dark:border-slate-800"
                    : "border-slate-200 hover:border-sky-400 hover:shadow-sm dark:border-slate-800 dark:hover:border-sky-600",
                )}
              >
                <span className="flex items-start justify-between gap-2">
                  <span className="font-medium">
                    <span className="text-slate-500">{l.number}.</span> {l.title}
                  </span>
                  {st && <Badge tone={st.tone}>{st.label}</Badge>}
                </span>
                <span className="text-sm text-slate-600 dark:text-slate-400">{l.summary}</span>
                {l.unavailable ? (
                  <span className="text-xs text-slate-500">{l.unavailable}</span>
                ) : (
                  <span className="mt-auto space-y-1">
                    <span className="block text-xs text-slate-500">
                      {l.done} van {l.steps} opdrachten
                    </span>
                    <Progress done={l.done} total={l.steps} />
                  </span>
                )}
              </button>
            </li>
          );
        })}
      </ol>

      {isAdmin && hasNamespace && (
        <Card>
          <div className="flex flex-wrap items-center justify-between gap-3 text-sm">
            <p className="max-w-prose">
              <span className="font-medium">Klaar met oefenen?</span> Opruimen haalt de namespace leren weg, met alles wat je in
              de lessen maakte, ook de volumes. Je voortgang blijft bewaard.
            </p>
            <KubeActions
              clusterId={id}
              target={{
                kind: "Namespace",
                ref: { group: "", version: "v1", resource: "namespaces", namespace: "", name: learnNamespace },
                actions: ["delete"],
              }}
              names={{ delete: "Opruimen" }}
              onDone={setDone}
            />
          </div>
        </Card>
      )}
    </div>
  );
}

function LessonView({
  id,
  lessonId,
  all,
  onLesson,
}: {
  id: string;
  lessonId: string;
  all: KubeLessonSummary[];
  onLesson: (lesson: string | undefined) => void;
}) {
  const q = useKubeLesson(id, lessonId);
  const answer = useKubeLessonAnswer(id, lessonId);
  const reset = useKubeLessonReset(id, lessonId);
  const l = q.data;
  const index = all.findIndex((x) => x.id === lessonId);
  const next = all.slice(index + 1).find((x) => !x.unavailable);
  const summary = all[index];

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <Button type="button" variant="ghost" onClick={() => onLesson(undefined)}>
          ← Alle lessen
        </Button>
        {l && l.steps.some((s) => s.state === "done") && (
          <Button
            type="button"
            variant="secondary"
            disabled={reset.isPending}
            onClick={() => {
              if (
                window.confirm("Deze les opnieuw beginnen? Je voortgang in de les gaat weg; wat er op het cluster staat, blijft.")
              )
                reset.mutate(undefined, { onSuccess: () => void q.refetch() });
            }}
          >
            Opnieuw beginnen
          </Button>
        )}
      </div>

      <Card>
        <div className="space-y-3">
          <div>
            <p className="text-xs font-medium tracking-wide text-slate-500 uppercase">Les {summary?.number}</p>
            <h2 className="text-lg font-semibold">{summary?.title}</h2>
          </div>
          {l && (
            <div className="space-y-1">
              <p className="text-xs text-slate-500">
                {l.done} van {l.steps.length} opdrachten af
              </p>
              <Progress done={l.done} total={l.steps.length} />
            </div>
          )}
          {l?.intro.map((p, i) => (
            <p key={i} className="max-w-prose text-sm leading-relaxed">
              {rich(p)}
            </p>
          ))}
        </div>
      </Card>

      {q.isLoading && <p className="text-sm text-slate-500">Laden…</p>}
      {q.isError && <KubeError error={q.error} />}
      {reset.isError && <KubeError error={reset.error} />}
      {l?.unavailable && <Alert kind="info">{l.unavailable}</Alert>}
      {l?.problem && (
        <Alert>Het cluster is nu niet te lezen: {l.problem}. ClusterForge probeert het elke vijf seconden opnieuw.</Alert>
      )}

      {l && (
        <ol className="space-y-3">
          {l.steps.map((s, i) => (
            <StepItem key={s.id} id={id} lesson={l} step={s} index={i} answer={answer} />
          ))}
        </ol>
      )}

      {l?.finished_at && (
        <div className="space-y-2 rounded-lg border border-emerald-200 bg-emerald-50 p-4 text-sm text-emerald-950 dark:border-emerald-900 dark:bg-emerald-950/40 dark:text-emerald-100">
          <p className="font-semibold">Les {summary?.number} is af.</p>
          {l.outro.map((p, i) => (
            <p key={i} className="max-w-prose leading-relaxed">
              {rich(p)}
            </p>
          ))}
          {next && (
            <Button type="button" onClick={() => onLesson(next.id)}>
              Naar les {next.number}: {next.title}
            </Button>
          )}
        </div>
      )}
    </div>
  );
}

type AnswerMutation = ReturnType<typeof useKubeLessonAnswer>;

function StepIcon({ state, n }: { state: KubeLessonStep["state"]; n: number }) {
  return (
    <span
      aria-hidden
      className={cx(
        "flex size-7 shrink-0 items-center justify-center rounded-full text-sm font-semibold",
        state === "done" && "bg-emerald-100 text-emerald-700 dark:bg-emerald-900 dark:text-emerald-200",
        state === "current" && "bg-brand-600 text-white dark:bg-sky-500",
        state === "locked" && "bg-slate-100 text-slate-400 dark:bg-slate-800 dark:text-slate-500",
      )}
    >
      {state === "done" ? "✓" : n}
    </span>
  );
}

const stateLabel: Record<KubeLessonStep["state"], string> = {
  done: "af",
  current: "aan de beurt",
  locked: "nog niet aan de beurt",
};

function StepItem({
  id,
  lesson,
  step,
  index,
  answer,
}: {
  id: string;
  lesson: KubeLesson;
  step: KubeLessonStep;
  index: number;
  answer: AnswerMutation;
}) {
  const current = step.state === "current";
  return (
    <li
      className={cx(
        "rounded-lg border bg-white p-4 dark:bg-slate-900",
        current ? "border-sky-300 shadow-sm dark:border-sky-700" : "border-slate-200 dark:border-slate-800",
      )}
      aria-current={current ? "step" : undefined}
    >
      <div className="flex items-start gap-3">
        <StepIcon state={step.state} n={index + 1} />
        <div className="min-w-0 flex-1 space-y-3">
          <p className={cx("pt-0.5 font-medium", step.state === "locked" && "text-slate-500")}>
            {step.title}
            <span className="sr-only"> ({stateLabel[step.state]})</span>
          </p>
          {step.state === "done" && <DoneStep step={step} />}
          {current && <CurrentStep id={id} lesson={lesson} step={step} answer={answer} />}
        </div>
      </div>
    </li>
  );
}

function DoneStep({ step }: { step: KubeLessonStep }) {
  if (!step.detail && !step.answer) return null;
  return (
    <div className="space-y-1 text-sm text-slate-600 dark:text-slate-400">
      {step.kind === "quiz" && step.answer && (
        <p>
          {step.question} <span className="font-medium text-slate-800 dark:text-slate-200">{step.answer}</span>
        </p>
      )}
      {step.detail && <p>{rich(step.detail)}</p>}
    </div>
  );
}

function CurrentStep({
  id,
  lesson,
  step,
  answer,
}: {
  id: string;
  lesson: KubeLesson;
  step: KubeLessonStep;
  answer: AnswerMutation;
}) {
  const isAdmin = useIsAdmin();
  return (
    <div className="space-y-4">
      <div className="max-w-prose space-y-2 text-sm leading-relaxed">
        {step.text.map((p, i) => (
          <p key={i}>{rich(p)}</p>
        ))}
      </div>

      {step.kind === "quiz" && <Quiz step={step} answer={answer} />}

      {(step.web || step.link || step.yaml) && (
        <div className="space-y-2">
          <p className="text-xs font-medium tracking-wide text-slate-500 uppercase">In ClusterForge</p>
          {/* Bij YAML zegt de uitleg hoe je toepast; dat kan alleen een beheerder. */}
          {step.web && (isAdmin || !step.yaml) && <p className="text-sm">{rich(step.web)}</p>}
          {step.link && <LessonLink id={id} lesson={lesson.id} link={step.link} />}
          {step.yaml &&
            (isAdmin ? (
              <YamlApply id={id} yaml={step.yaml} />
            ) : (
              <div className="space-y-2">
                <CodeBlock text={step.yaml} />
                <p className="text-xs text-slate-500">Alleen een beheerder kan dit hier toepassen; met kubectl kan het ook.</p>
              </div>
            ))}
        </div>
      )}

      {step.kubectl.length > 0 && (
        <div className="space-y-2">
          <p className="text-xs font-medium tracking-wide text-slate-500 uppercase">Met kubectl op een server</p>
          <CodeBlock text={step.kubectl.join("\n")} />
          {step.yaml && (
            <p className="text-xs text-slate-500">
              Of zet de YAML in een bestand, bijvoorbeeld <code className="font-mono">les.yaml</code>, en doe{" "}
              <code className="font-mono">sudo kubectl apply -f les.yaml</code>.
            </p>
          )}
        </div>
      )}

      {step.kind === "do" && <Watching detail={step.detail} problem={lesson.problem !== ""} />}

      {step.kind === "read" && (
        <div className="space-y-2">
          <Button type="button" disabled={answer.isPending} onClick={() => answer.mutate({ [step.id]: "ok" })}>
            {step.done_label || "Gelukt"}
          </Button>
          {answer.isError && <KubeError error={answer.error} />}
        </div>
      )}
    </div>
  );
}

// Watching zegt wat ClusterForge op het cluster ziet, zolang de opdracht
// niet af is.
function Watching({ detail, problem }: { detail: string; problem: boolean }) {
  if (problem) return null;
  return (
    <div className="flex items-start gap-2 rounded-md bg-slate-50 px-3 py-2 text-sm dark:bg-slate-800/60" role="status">
      <span className="mt-1.5 size-2 shrink-0 animate-pulse rounded-full bg-brand-500 dark:bg-sky-400" aria-hidden />
      <span>
        {detail ? <>{rich(detail)} </> : null}
        <span className="text-slate-500">ClusterForge kijkt elke vijf seconden of het gelukt is.</span>
      </span>
    </div>
  );
}

function Quiz({ step, answer }: { step: KubeLessonStep; answer: AnswerMutation }) {
  const [value, setValue] = useState("");
  const result = answer.data?.steps.find((s) => s.id === step.id);
  const wrong = result?.wrong && answer.variables?.[step.id] === value.trim() ? result.detail : "";
  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (value.trim() !== "") answer.mutate({ [step.id]: value.trim() });
  };
  const field = `les-${step.id}`;
  return (
    <form onSubmit={submit} className="max-w-prose space-y-2">
      <label htmlFor={field} className="block text-sm font-medium">
        {step.question}
      </label>
      <div className="flex gap-2">
        <Input
          id={field}
          autoComplete="off"
          spellCheck={false}
          value={value}
          onChange={(e) => setValue(e.target.value)}
          aria-invalid={wrong ? true : undefined}
          className={cx(wrong && "ring-1 ring-red-400 dark:ring-red-700")}
        />
        <Button type="submit" disabled={answer.isPending || value.trim() === ""}>
          Antwoorden
        </Button>
      </div>
      {wrong && <p className="text-sm text-red-700 dark:text-red-300">{wrong}</p>}
      {answer.isError && <KubeError error={answer.error} />}
    </form>
  );
}

// LessonLink opent het tabblad, het object of het webadres van een
// opdracht. Een tabblad onthoudt de les, zodat je terug kunt.
function LessonLink({ id, lesson, link }: { id: string; lesson: string; link: KubeLessonLink }) {
  const cls =
    "inline-flex items-center rounded-md border border-slate-300 bg-white px-3 py-1.5 text-sm font-medium text-slate-800 hover:bg-slate-50 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-100 dark:hover:bg-slate-800";
  if (link.url) {
    return (
      <a href={link.url} target="_blank" rel="noreferrer" className={cls}>
        {link.label} ↗
      </a>
    );
  }
  if (link.ref) {
    return (
      <Link href={objectHref(id, link.ref)} className={cls}>
        {link.label} →
      </Link>
    );
  }
  const q = new URLSearchParams({ id, tab: link.tab, les: lesson });
  if (link.namespace) q.set("ns", link.namespace);
  if (link.system) q.set("system", "1");
  return (
    <Link href={`/kubernetes/cluster?${q.toString()}`} className={cls}>
      {link.label} →
    </Link>
  );
}

// YamlApply is de YAML van een opdracht met Controleren en Toepassen, zoals
// op het tabblad Toepassen.
function YamlApply({ id, yaml }: { id: string; yaml: string }): ReactNode {
  const flow = useApplyFlow(id, yaml, learnNamespace);
  return (
    <div className="space-y-3">
      <CodeBlock text={yaml} className="max-h-96" />
      <ApplyPanel clusterId={id} flow={flow} yaml={yaml} blocker={null} />
    </div>
  );
}
