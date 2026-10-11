"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, ApiError, getCsrfToken, unwrap } from "./api/client";
import type { components } from "./api/schema";

export type AssistantSettings = components["schemas"]["AssistantSettings"];
export type AssistantSettingsInput = components["schemas"]["AssistantSettingsInput"];
export type AssistantUsage = components["schemas"]["AssistantUsage"];
export type Conversation = components["schemas"]["AssistantConversation"];
export type Turn = components["schemas"]["AssistantTurn"];
export type Subject = "cluster" | "node" | "incident";

export const settingsKey = ["assistant", "settings"] as const;

export function useAssistantSettings() {
  return useQuery({
    queryKey: settingsKey,
    queryFn: async () => unwrap(await api.GET("/assistant/settings")),
    staleTime: 60_000,
  });
}

export function useSaveAssistantSettings() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (body: AssistantSettingsInput) => unwrap(await api.PUT("/assistant/settings", { body })),
    onSuccess: (data) => {
      qc.setQueryData(settingsKey, data);
      void qc.invalidateQueries({ queryKey: ["assistant"] });
    },
  });
}

export function useTestAssistantKey() {
  return useMutation({
    mutationFn: async (body: { model: string; api_key?: string }) => unwrap(await api.POST("/assistant/settings/test", { body })),
  });
}

export function useAssistantUsage(enabled = true) {
  return useQuery({
    queryKey: ["assistant", "usage"],
    queryFn: async () => unwrap(await api.GET("/assistant/usage")),
    enabled,
  });
}

export function useConversations(all: boolean, enabled = true) {
  return useQuery({
    queryKey: ["assistant", "conversations", all],
    queryFn: async () => unwrap(await api.GET("/assistant/conversations", { params: { query: { all } } })).items,
    enabled,
  });
}

export function useConversation(id: string) {
  return useQuery({
    queryKey: ["assistant", "conversation", id],
    queryFn: async () =>
      unwrap(await api.GET("/assistant/conversations/{conversationId}", { params: { path: { conversationId: id } } })),
    enabled: id !== "",
  });
}

export function useCreateConversation() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (subject?: { type: Subject; id: string }) =>
      unwrap(
        await api.POST("/assistant/conversations", {
          body: subject ? { subject_type: subject.type, subject_id: subject.id } : {},
        }),
      ),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["assistant", "conversations"] }),
  });
}

export function useDeleteConversation() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (id: string) =>
      unwrap(await api.DELETE("/assistant/conversations/{conversationId}", { params: { path: { conversationId: id } } })),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["assistant", "conversations"] }),
  });
}

// StreamEvent is een stuk van een antwoord dat binnenkomt.
export type StreamEvent =
  | { type: "text"; text: string }
  | { type: "tool"; name: string; label: string }
  | { type: "error"; message: string }
  | {
      type: "done";
      models: string[];
      input_tokens: number;
      output_tokens: number;
      cache_read_tokens: number;
      cache_write_tokens: number;
      cost_usd: number;
      note: string;
    };

// ask stelt een vraag en geeft elk stuk van het antwoord aan onEvent terwijl
// het binnenkomt. Een fout voor het antwoord begint, zoals het budget, is
// een ApiError.
export async function ask(conversationId: string, question: string, onEvent: (ev: StreamEvent) => void, signal?: AbortSignal) {
  const headers: Record<string, string> = { "Content-Type": "application/json", Accept: "text/event-stream" };
  const csrf = getCsrfToken();
  if (csrf) headers["X-CSRF-Token"] = csrf;
  const res = await fetch(`/api/v1/assistant/conversations/${encodeURIComponent(conversationId)}/messages`, {
    method: "POST",
    credentials: "same-origin",
    headers,
    body: JSON.stringify({ question }),
    signal,
  });
  if (!res.ok || !res.body) {
    const body = (await res.json().catch(() => null)) as { code?: string; message?: string } | null;
    throw new ApiError(res.status, body?.code ?? "unknown", body?.message ?? `Onverwachte fout (${res.status})`);
  }
  const reader = res.body.pipeThrough(new TextDecoderStream()).getReader();
  let buf = "";
  for (;;) {
    const { value, done } = await reader.read();
    if (done) break;
    buf += value;
    // Een event eindigt met een lege regel.
    let end: number;
    while ((end = buf.indexOf("\n\n")) >= 0) {
      const chunk = buf.slice(0, end);
      buf = buf.slice(end + 2);
      const ev = parseEvent(chunk);
      if (ev) onEvent(ev);
    }
  }
}

export function parseEvent(chunk: string): StreamEvent | null {
  let type = "";
  let data = "";
  for (const line of chunk.split("\n")) {
    if (line.startsWith("event: ")) type = line.slice(7);
    else if (line.startsWith("data: ")) data += line.slice(6);
  }
  if (!type || !data) return null;
  try {
    return { type, ...JSON.parse(data) } as StreamEvent;
  } catch {
    return null;
  }
}

export function dollars(v: number) {
  if (v > 0 && v < 0.01) return "< $0,01";
  return "$" + v.toLocaleString("nl-BE", { minimumFractionDigits: 2, maximumFractionDigits: 2 });
}

export function tokens(n: number) {
  return n.toLocaleString("nl-BE");
}

// modelLabel is de naam van een model, zoals Claude Opus 5.5.
export function modelLabel(id: string, models?: { id: string; label: string }[]) {
  const m = models?.find((x) => id === x.id || id.startsWith(x.id + "-"));
  if (m) return m.label;
  // Een fallbackmodel buiten de lijst, zoals claude-opus-4-8.
  const parts = id.split("-");
  if (parts[0] !== "claude" || parts.length < 3) return id;
  const name = parts[1].charAt(0).toUpperCase() + parts[1].slice(1);
  const version = parts.slice(2).filter((p) => /^\d{1,2}$/.test(p));
  return `Claude ${name} ${version.join(".")}`.trim();
}

export const subjectLabels: Record<Subject, string> = { cluster: "cluster", node: "node", incident: "incident" };
