"use client";

import { useEffect, useRef, useState } from "react";
import { Markdown } from "@/components/Markdown";
import { Alert, Button, Textarea } from "@/components/ui";
import { useMe } from "@/lib/auth";
import { useAIVGet, useAIVNow, type AIVInstance } from "@/lib/aiverslag";

// Fase 38 M2: een vraag stellen aan de AI-analist van AI-verslag. Die
// zoekt zelf in zijn gegevens (stappen) en geeft een antwoord; ClusterForge
// stelt de vraag en leest het antwoord tot het klaar is.

type Msg = { role: "u" | "a"; text: string; steps?: string[]; error?: string };
type ChatPoll = {
  steps: unknown[];
  answer: string | null;
  done: boolean;
  error: string | null;
};

const examples = [
  "Waarom staat het dreigingsniveau zo?",
  "Welke toestellen deden vannacht iets ongewoons?",
  "Wat blokkeerde de firewall het meest de laatste 24 uur?",
];

function stepText(s: unknown) {
  if (typeof s === "string") return s;
  if (s && typeof s === "object") {
    const o = s as Record<string, unknown>;
    return String(
      o.tekst ?? o.text ?? o.tool ?? o.naam ?? JSON.stringify(o),
    ).slice(0, 200);
  }
  return String(s);
}

export function AskView({ v }: { v: AIVInstance }) {
  const me = useMe();
  const admin = me.data?.user.role === "admin";
  const start = useAIVNow<{ id: string }>(v.node_id);
  const [msgs, setMsgs] = useState<Msg[]>([]);
  const [q, setQ] = useState("");
  const [cid, setCid] = useState("");
  const poll = useAIVGet<ChatPoll>(
    v.node_id,
    cid ? `/api/chat/${cid}` : "/api/chat/000000",
    undefined,
    { enabled: !!cid, refetch: 1_500 },
  );
  const end = useRef<HTMLDivElement>(null);

  useEffect(() => {
    const d = poll.data;
    if (!cid || !d) return;
    setMsgs((m) => {
      const last = m[m.length - 1];
      if (!last || last.role !== "a") return m;
      const next = {
        ...last,
        steps: d.steps.map(stepText),
        text: d.answer ?? "",
        error: d.error ?? undefined,
      };
      return [...m.slice(0, -1), next];
    });
    if (d.done) setCid("");
  }, [poll.data, cid]);

  useEffect(() => {
    end.current?.scrollIntoView({ block: "nearest" });
  }, [msgs]);

  const ask = async (question: string) => {
    const text = question.trim();
    if (!text || cid) return;
    const history = msgs
      .filter((m) => m.text)
      .map((m) => ({ role: m.role, text: m.text }));
    setMsgs((m) => [
      ...m,
      { role: "u", text },
      { role: "a", text: "", steps: [] },
    ]);
    setQ("");
    try {
      const r = await start.mutateAsync({
        path: "/api/chat",
        body: { question: text, history },
      });
      setCid(r.id);
    } catch (e) {
      setMsgs((m) => [
        ...m.slice(0, -1),
        {
          role: "a",
          text: "",
          error: e instanceof Error ? e.message : "Mislukt",
        },
      ]);
    }
  };

  if (!admin)
    return (
      <Alert kind="info">
        Alleen een beheerder kan vragen stellen aan AI-verslag.
      </Alert>
    );
  const busy = !!cid || start.isPending;
  return (
    <div className="space-y-4">
      <div className="space-y-3">
        {msgs.length === 0 && (
          <div className="space-y-2 text-sm text-slate-600 dark:text-slate-400">
            <p>
              Stel een vraag over je netwerk. De AI van AI-verslag zoekt het
              antwoord in zijn eigen gegevens (firewall, DNS, CrowdSec,
              toestellen).
            </p>
            <div className="flex flex-wrap gap-2">
              {examples.map((e) => (
                <Button key={e} variant="secondary" onClick={() => ask(e)}>
                  {e}
                </Button>
              ))}
            </div>
          </div>
        )}
        {msgs.map((m, i) => (
          <div
            key={i}
            className={
              m.role === "u"
                ? "ml-auto max-w-[85%] rounded-lg bg-brand-600 px-3 py-2 text-sm text-white"
                : "max-w-full rounded-lg border border-slate-200 bg-white px-3 py-2 text-sm dark:border-slate-800 dark:bg-slate-900"
            }
          >
            {m.role === "u" ? (
              m.text
            ) : (
              <div className="space-y-2">
                {(m.steps ?? []).length > 0 && (
                  <ul className="space-y-0.5 text-xs text-slate-500">
                    {m.steps!.map((s, j) => (
                      <li key={j}>↳ {s}</li>
                    ))}
                  </ul>
                )}
                {m.error ? (
                  <Alert kind="error">{m.error}</Alert>
                ) : m.text ? (
                  <Markdown text={m.text} />
                ) : (
                  <p className="text-slate-500">Bezig met zoeken …</p>
                )}
              </div>
            )}
          </div>
        ))}
        <div ref={end} />
      </div>
      <form
        className="flex flex-col gap-2 sm:flex-row sm:items-end"
        onSubmit={(e) => {
          e.preventDefault();
          void ask(q);
        }}
      >
        <Textarea
          value={q}
          onChange={(e) => setQ(e.target.value)}
          rows={2}
          maxLength={800}
          placeholder="Bijvoorbeeld: wat deed 192.168.30.24 vannacht?"
          className="flex-1"
          onKeyDown={(e) => {
            if (e.key === "Enter" && !e.shiftKey) {
              e.preventDefault();
              void ask(q);
            }
          }}
        />
        <Button type="submit" disabled={busy || !q.trim()}>
          {busy ? "Bezig …" : "Vragen"}
        </Button>
      </form>
    </div>
  );
}
