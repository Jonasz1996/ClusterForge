"use client";

import { useQuery } from "@tanstack/react-query";
import { useEffect, useRef, useState } from "react";
import { api } from "@/lib/api/client";

// useServerUpdated zegt of de server een andere versie draait dan toen deze
// pagina laadde. Na een serverupdate verwijst een open tabblad nog naar oude
// bestanden; dan blijven schermen hangen op "Laden…" tot je vernieuwt.
export function useServerUpdated(enabled: boolean) {
  const q = useQuery({
    queryKey: ["health", "version"],
    queryFn: async () => (await api.GET("/health")).data?.version ?? "",
    refetchInterval: 60_000,
    refetchOnWindowFocus: true,
    enabled,
  });
  const first = useRef<string | null>(null);
  const [changed, setChanged] = useState(false);
  useEffect(() => {
    const v = q.data;
    if (!v) return;
    if (first.current === null) first.current = v;
    else if (v !== first.current) setChanged(true);
  }, [q.data]);
  return changed;
}

const reloadKey = "cf-chunk-reload";

// useReloadOnStaleChunks vernieuwt de pagina één keer als een stuk van de
// interface niet meer laadt (oude bestanden na een update), in plaats van
// te blijven hangen.
export function useReloadOnStaleChunks() {
  useEffect(() => {
    const stale = (msg: string) => /ChunkLoadError|Loading chunk|Failed to fetch dynamically imported module|Importing a module script failed/i.test(msg);
    const handle = (msg: string) => {
      if (!stale(msg)) return;
      try {
        const last = Number(sessionStorage.getItem(reloadKey) ?? "0");
        if (Date.now() - last < 60_000) return;
        sessionStorage.setItem(reloadKey, String(Date.now()));
      } catch {
        return;
      }
      window.location.reload();
    };
    const onError = (e: ErrorEvent) => handle(String(e.message ?? e.error ?? ""));
    const onRejection = (e: PromiseRejectionEvent) => handle(String((e.reason as Error)?.message ?? e.reason ?? ""));
    window.addEventListener("error", onError);
    window.addEventListener("unhandledrejection", onRejection);
    return () => {
      window.removeEventListener("error", onError);
      window.removeEventListener("unhandledrejection", onRejection);
    };
  }, []);
}
