"use client";

import { useEffect, useRef, useState, type ReactNode } from "react";
import { Markdown } from "@/components/Markdown";
import { Alert, Badge, cx } from "@/components/ui";
import { ApiError } from "@/lib/api/client";
import { dollars, modelLabel, tokens, type AssistantSettings, type StreamEvent } from "@/lib/assistant";

type Models = AssistantSettings["models"];

export function ToolList({ tools, running }: { tools: { label: string; error?: boolean }[]; running?: boolean }) {
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

export function Meta({
  models,
  input,
  output,
  cacheRead,
  cost,
  catalog,
}: {
  models: string[];
  input: number;
  output: number;
  cacheRead: number;
  cost: number;
  catalog?: Models;
}) {
  if (models.length === 0 && cost === 0) return null;
  return (
    <p className="text-xs text-slate-500">
      {models.map((m) => modelLabel(m, catalog)).join(", ")}
      {" · "}
      {tokens(input + cacheRead)} tokens in{cacheRead > 0 && ` (${tokens(cacheRead)} uit de cache)`}, {tokens(output)} uit ·{" "}
      {dollars(cost)}
    </p>
  );
}

export function Answer({ children }: { children: ReactNode }) {
  return (
    <div className="space-y-2 rounded-lg border border-slate-200 bg-white px-4 py-3 dark:border-slate-800 dark:bg-slate-900">
      {children}
    </div>
  );
}

// Live is een antwoord dat nu binnenkomt.
export type Live = {
  text: string;
  tools: { name: string; label: string }[];
  error: string;
  done?: Extract<StreamEvent, { type: "done" }>;
};

type Start = (onEvent: (ev: StreamEvent) => void, signal: AbortSignal) => Promise<void>;

// useStream volgt een antwoord van de assistent terwijl het binnenkomt. run
// geeft het antwoord terug als het klaar is, of null als het niet begon;
// error is dan waarom.
export function useStream() {
  const [live, setLive] = useState<Live | null>(null);
  const [error, setError] = useState<string | null>(null);
  const abort = useRef<AbortController | null>(null);
  useEffect(() => () => abort.current?.abort(), []);

  const run = async (start: Start, failed = "De vraag kon niet verstuurd worden."): Promise<Live | null> => {
    setError(null);
    const state: Live = { text: "", tools: [], error: "" };
    setLive(state);
    abort.current = new AbortController();
    try {
      await start((ev) => {
        if (ev.type === "text") state.text += ev.text;
        else if (ev.type === "tool") state.tools = [...state.tools, { name: ev.name, label: ev.label }];
        else if (ev.type === "error") state.error = ev.message;
        else if (ev.type === "done") state.done = ev;
        setLive({ ...state });
      }, abort.current.signal);
      return state;
    } catch (err) {
      if (err instanceof DOMException && err.name === "AbortError") return null;
      setLive(null);
      setError(err instanceof ApiError ? err.message : failed);
      return null;
    }
  };
  const reset = () => {
    setLive(null);
    setError(null);
  };
  const busy = live !== null && !live.done && !live.error;
  return { live, error, busy, run, reset };
}

// LiveAnswer toont een antwoord terwijl het binnenkomt: de tools die de
// assistent gebruikt, de tekst, en als het klaar is het model en de kosten.
export function LiveAnswer({ live, catalog }: { live: Live; catalog?: Models }) {
  const running = !live.done && !live.error;
  return (
    <>
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
          catalog={catalog}
        />
      )}
    </>
  );
}
