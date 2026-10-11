"use client";

import { useQueryClient } from "@tanstack/react-query";
import Link from "next/link";
import { useRouter, useSearchParams } from "next/navigation";
import { Suspense, useEffect, useRef, useState, type FormEvent, type KeyboardEvent } from "react";
import { Markdown } from "@/components/Markdown";
import { ago, QueryState } from "@/components/inventory/bits";
import { Alert, Badge, Button, cx, PageHeader, Textarea } from "@/components/ui";
import { ApiError } from "@/lib/api/client";
import {
  ask,
  dollars,
  modelLabel,
  tokens,
  useAssistantSettings,
  useAssistantUsage,
  useConversation,
  useConversations,
  useCreateConversation,
  useDeleteConversation,
  type AssistantSettings,
  type Conversation,
  type StreamEvent,
  type Subject,
  type Turn,
} from "@/lib/assistant";
import { auditTimeFmt } from "@/lib/audit";
import { useIsAdmin } from "@/lib/inventory";

export default function AssistantPage() {
  return (
    <Suspense>
      <AssistantInner />
    </Suspense>
  );
}

const examples = [
  "Waarom was er vannacht een incident?",
  "Welke nodes hebben beveiligingsupdates open?",
  "Wat gebeurt er als web-01 uitvalt?",
  "Wat veranderde er de laatste 24 uur in prod?",
];

function AssistantInner() {
  const params = useSearchParams();
  const router = useRouter();
  const isAdmin = useIsAdmin();
  const settings = useAssistantSettings();
  const [everyone, setEveryone] = useState(false);
  const canUse = settings.data?.can_use ?? false;
  const list = useConversations(everyone && isAdmin, settings.isSuccess);
  const usage = useAssistantUsage(settings.isSuccess);
  const create = useCreateConversation();
  const selected = params.get("gesprek") ?? "";

  // Een vraagknop op een cluster, node of incident opent een nieuw gesprek
  // over dat onderwerp.
  const subject = (["cluster", "node", "incident"] as Subject[])
    .map((type) => ({ type, id: params.get(type) ?? "" }))
    .find((s) => s.id !== "");
  const started = useRef("");
  useEffect(() => {
    if (!subject || !canUse || started.current === subject.type + subject.id) return;
    started.current = subject.type + subject.id;
    create.mutate(subject, { onSuccess: (c) => router.replace(`/assistent?gesprek=${c.id}`) });
  }, [subject, canUse, create, router]);

  const newConversation = () => create.mutate(undefined, { onSuccess: (c) => router.push(`/assistent?gesprek=${c.id}`) });

  return (
    <div className="space-y-6">
      <PageHeader
        title="Assistent"
        description="Stel een vraag over je fleet. De assistent leest dezelfde gegevens als jij, en voert nooit iets uit."
        actions={
          canUse && (
            <Button onClick={newConversation} disabled={create.isPending}>
              Nieuw gesprek
            </Button>
          )
        }
      />
      <QueryState q={settings}>
        {settings.data && (
          <>
            {!canUse && (
              <Alert kind="info">
                {settings.data.reason}
                {isAdmin && (
                  <>
                    {" "}
                    <Link href="/instellingen#assistent" className="font-medium underline">
                      Naar de instellingen
                    </Link>
                  </>
                )}
              </Alert>
            )}
            {create.isError && <Alert>{create.error.message}</Alert>}
            {usage.data && <UsageLine usage={usage.data} isAdmin={isAdmin} />}
            <div className="grid gap-6 md:grid-cols-[16rem_minmax(0,1fr)]">
              <aside className="space-y-2">
                <div className="flex items-center justify-between">
                  <h2 className="text-sm font-semibold">Gesprekken</h2>
                  {isAdmin && (
                    <label className="flex items-center gap-1.5 text-xs text-slate-500">
                      <input type="checkbox" checked={everyone} onChange={(e) => setEveryone(e.target.checked)} />
                      van iedereen
                    </label>
                  )}
                </div>
                <ConversationList items={list.data ?? []} selected={selected} showUser={everyone && isAdmin} />
              </aside>
              <section className="min-w-0">
                {selected ? (
                  <Chat key={selected} id={selected} settings={settings.data} />
                ) : (
                  <Welcome canUse={canUse} onAsk={newConversation} />
                )}
              </section>
            </div>
          </>
        )}
      </QueryState>
    </div>
  );
}

function UsageLine({ usage, isAdmin }: { usage: NonNullable<ReturnType<typeof useAssistantUsage>["data"]>; isAdmin: boolean }) {
  const pct = usage.budget_usd > 0 ? Math.min(100, (usage.spent_usd / usage.budget_usd) * 100) : 0;
  return (
    <div className="rounded-lg border border-slate-200 bg-white px-4 py-3 text-sm dark:border-slate-800 dark:bg-slate-900">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <span>
          Deze maand {dollars(usage.spent_usd)} van het budget van {dollars(usage.budget_usd)}
        </span>
        {isAdmin && usage.users.length > 0 && (
          <span className="text-xs text-slate-500">
            {usage.users
              .map(
                (u) =>
                  `${u.username || "verwijderd"}: ${u.questions} ${u.questions === 1 ? "vraag" : "vragen"}, ${dollars(u.cost_usd)}`,
              )
              .join(" · ")}
          </span>
        )}
      </div>
      <div className="mt-2 h-1.5 overflow-hidden rounded bg-slate-100 dark:bg-slate-800">
        <div
          className={cx("h-full", pct >= 90 ? "bg-red-500" : pct >= 70 ? "bg-amber-500" : "bg-brand-500")}
          style={{ width: `${pct}%` }}
        />
      </div>
    </div>
  );
}

function ConversationList({ items, selected, showUser }: { items: Conversation[]; selected: string; showUser: boolean }) {
  if (items.length === 0) return <p className="text-sm text-slate-500">Nog geen gesprekken.</p>;
  return (
    <ul className="space-y-1">
      {items.map((c) => (
        <li key={c.id}>
          <Link
            href={`/assistent?gesprek=${c.id}`}
            className={cx(
              "block rounded-md px-3 py-2 text-sm",
              c.id === selected
                ? "bg-brand-50 text-brand-700 dark:bg-slate-800 dark:text-white"
                : "hover:bg-slate-100 dark:hover:bg-slate-800",
            )}
          >
            <span className="line-clamp-2 font-medium">{c.title || "Nieuw gesprek"}</span>
            <span className="mt-0.5 block text-xs text-slate-500">
              {showUser && `${c.username} · `}
              {ago(c.updated_at)}
              {c.cost_usd > 0 && ` · ${dollars(c.cost_usd)}`}
            </span>
          </Link>
        </li>
      ))}
    </ul>
  );
}

function Welcome({ canUse, onAsk }: { canUse: boolean; onAsk: () => void }) {
  return (
    <div className="rounded-lg border border-dashed border-slate-300 p-6 text-sm text-slate-600 dark:border-slate-700 dark:text-slate-400">
      <p className="font-medium text-slate-800 dark:text-slate-200">Wat wil je weten?</p>
      <p className="mt-1">Bijvoorbeeld:</p>
      <ul className="mt-2 list-disc space-y-1 pl-5">
        {examples.map((e) => (
          <li key={e}>{e}</li>
        ))}
      </ul>
      <p className="mt-3">
        Op de pagina van een cluster, node of incident begint de knop Vraag de assistent een gesprek over dat onderwerp.
      </p>
      {canUse && (
        <Button className="mt-4" onClick={onAsk}>
          Nieuw gesprek
        </Button>
      )}
    </div>
  );
}

// Live is het antwoord dat nu binnenkomt.
type Live = {
  question: string;
  text: string;
  tools: { name: string; label: string }[];
  error: string;
  done?: Extract<StreamEvent, { type: "done" }>;
};

function Chat({ id, settings }: { id: string; settings: AssistantSettings }) {
  const q = useConversation(id);
  const qc = useQueryClient();
  const del = useDeleteConversation();
  const router = useRouter();
  const [question, setQuestion] = useState("");
  const [live, setLive] = useState<Live | null>(null);
  const [error, setError] = useState<string | null>(null);
  const busy = live !== null && !live.done && !live.error;
  const bottom = useRef<HTMLDivElement>(null);
  const abort = useRef<AbortController | null>(null);

  useEffect(() => () => abort.current?.abort(), []);
  useEffect(() => {
    bottom.current?.scrollIntoView({ block: "end", behavior: "smooth" });
  }, [live?.text, live?.tools.length, q.data?.turns.length]);

  const send = async (e?: FormEvent) => {
    e?.preventDefault();
    const text = question.trim();
    if (!text || busy) return;
    setError(null);
    setQuestion("");
    const state: Live = { question: text, text: "", tools: [], error: "" };
    setLive(state);
    abort.current = new AbortController();
    try {
      await ask(
        id,
        text,
        (ev) => {
          if (ev.type === "text") state.text += ev.text;
          else if (ev.type === "tool") state.tools = [...state.tools, { name: ev.name, label: ev.label }];
          else if (ev.type === "error") state.error = ev.message;
          else if (ev.type === "done") state.done = ev;
          setLive({ ...state });
        },
        abort.current.signal,
      );
      await qc.invalidateQueries({ queryKey: ["assistant"] });
      setLive(null);
    } catch (err) {
      if (err instanceof DOMException && err.name === "AbortError") return;
      setLive(null);
      setQuestion(text);
      setError(err instanceof ApiError ? err.message : "De vraag kon niet verstuurd worden.");
    }
  };

  const onKey = (e: KeyboardEvent<HTMLTextAreaElement>) => {
    if (e.key === "Enter" && !e.shiftKey && !e.nativeEvent.isComposing) {
      e.preventDefault();
      void send();
    }
  };

  const conv = q.data?.conversation;
  return (
    <QueryState q={q}>
      {q.data && conv && (
        <div className="space-y-4">
          <div className="flex flex-wrap items-center justify-between gap-2">
            <div className="min-w-0">
              <h2 className="truncate text-base font-semibold">{conv.title || "Nieuw gesprek"}</h2>
              <p className="text-xs text-slate-500">
                {conv.username} · begonnen {auditTimeFmt.format(new Date(conv.created_at))}
                {conv.cost_usd > 0 && ` · ${dollars(conv.cost_usd)}`}
              </p>
            </div>
            <Button
              variant="secondary-danger"
              disabled={del.isPending || busy}
              onClick={() => del.mutate(id, { onSuccess: () => router.push("/assistent") })}
            >
              Wissen
            </Button>
          </div>
          {q.data.turns.length === 0 && !live && (
            <p className="text-sm text-slate-500">Stel je vraag hieronder. Voorbeelden: {examples.slice(0, 2).join(" ")}</p>
          )}
          {q.data.turns.map((t, i) => (
            <TurnView key={i} turn={t} settings={settings} />
          ))}
          {live && <LiveView live={live} settings={settings} />}
          {error && <Alert>{error}</Alert>}
          {settings.can_use ? (
            <form onSubmit={send} className="space-y-2">
              <Textarea
                rows={3}
                value={question}
                onChange={(e) => setQuestion(e.target.value)}
                onKeyDown={onKey}
                maxLength={4000}
                placeholder="Stel een vraag over je clusters, nodes, incidenten…"
                aria-label="Vraag"
              />
              <div className="flex items-center justify-between gap-2">
                <p className="text-xs text-slate-500">Enter verstuurt, Shift+Enter is een nieuwe regel.</p>
                <Button type="submit" disabled={busy || question.trim() === ""}>
                  {busy ? "Bezig…" : "Vraag"}
                </Button>
              </div>
            </form>
          ) : (
            <Alert kind="info">{settings.reason}</Alert>
          )}
          <div ref={bottom} />
        </div>
      )}
    </QueryState>
  );
}

function Question({ text }: { text: string }) {
  return (
    <div className="flex justify-end">
      <div className="max-w-[85%] rounded-lg bg-brand-600 px-3 py-2 text-sm whitespace-pre-wrap text-white">{text}</div>
    </div>
  );
}

function ToolList({ tools, running }: { tools: { label: string; error?: boolean }[]; running?: boolean }) {
  if (tools.length === 0) return null;
  return (
    <ul className="space-y-0.5 text-xs text-slate-500">
      {tools.map((t, i) => (
        <li key={i} className="flex items-center gap-1.5">
          <span
            className={cx(
              "inline-block h-1.5 w-1.5 shrink-0 rounded-full",
              t.error ? "bg-red-500" : running && i === tools.length - 1 ? "animate-pulse bg-brand-500" : "bg-slate-400",
            )}
          />
          {t.label}
          {running && i === tools.length - 1 && "…"}
          {t.error && <Badge tone="red">mislukt</Badge>}
        </li>
      ))}
    </ul>
  );
}

function Meta({
  models,
  input,
  output,
  cacheRead,
  cost,
  settings,
}: {
  models: string[];
  input: number;
  output: number;
  cacheRead: number;
  cost: number;
  settings: AssistantSettings;
}) {
  if (models.length === 0 && cost === 0) return null;
  return (
    <p className="text-xs text-slate-500">
      {models.map((m) => modelLabel(m, settings.models)).join(", ")}
      {" · "}
      {tokens(input + cacheRead)} tokens in{cacheRead > 0 && ` (${tokens(cacheRead)} uit de cache)`}, {tokens(output)} uit ·{" "}
      {dollars(cost)}
    </p>
  );
}

function Answer({ children }: { children: React.ReactNode }) {
  return (
    <div className="space-y-2 rounded-lg border border-slate-200 bg-white px-4 py-3 dark:border-slate-800 dark:bg-slate-900">
      {children}
    </div>
  );
}

function TurnView({ turn, settings }: { turn: Turn; settings: AssistantSettings }) {
  return (
    <div className="space-y-2">
      <Question text={turn.question} />
      <Answer>
        <ToolList tools={turn.tools} />
        {turn.answer && <Markdown text={turn.answer} />}
        {turn.note && <Alert kind="info">{turn.note}</Alert>}
        <Meta
          models={turn.models}
          input={turn.input_tokens}
          output={turn.output_tokens}
          cacheRead={turn.cache_read_tokens}
          cost={turn.cost_usd}
          settings={settings}
        />
      </Answer>
    </div>
  );
}

function LiveView({ live, settings }: { live: Live; settings: AssistantSettings }) {
  const running = !live.done && !live.error;
  return (
    <div className="space-y-2">
      <Question text={live.question} />
      <Answer>
        <ToolList tools={live.tools} running={running && live.text === ""} />
        {live.text ? (
          <Markdown text={live.text} />
        ) : (
          running && <p className="animate-pulse text-sm text-slate-500">{live.tools.length === 0 ? "Denkt na…" : "Leest…"}</p>
        )}
        {live.error && <Alert>{live.error}</Alert>}
        {live.done?.note && <Alert kind="info">{live.done.note}</Alert>}
        {live.done && (
          <Meta
            models={live.done.models}
            input={live.done.input_tokens}
            output={live.done.output_tokens}
            cacheRead={live.done.cache_read_tokens}
            cost={live.done.cost_usd}
            settings={settings}
          />
        )}
      </Answer>
    </div>
  );
}
