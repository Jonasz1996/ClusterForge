"use client";

import { useQueryClient } from "@tanstack/react-query";
import { useEffect } from "react";

// useLiveUpdates luistert naar /api/v1/stream en laadt na elke wijziging de
// lijsten, details en eventlog opnieuw. Valt de stream weg, dan verbindt de
// browser zelf opnieuw; de schermen verversen intussen ook op een timer.
export function useLiveUpdates(enabled: boolean) {
  const qc = useQueryClient();
  useEffect(() => {
    if (!enabled || typeof EventSource === "undefined") return;
    const es = new EventSource("/api/v1/stream");
    let timer: ReturnType<typeof setTimeout> | undefined;
    const onChange = () => {
      // Een reeks wijzigingen tegelijk geeft één keer opnieuw laden.
      clearTimeout(timer);
      timer = setTimeout(() => {
        void qc.invalidateQueries({ queryKey: ["clusters"] });
        void qc.invalidateQueries({ queryKey: ["nodes"] });
        void qc.invalidateQueries({ queryKey: ["events"] });
      }, 300);
    };
    es.addEventListener("change", onChange);
    return () => {
      clearTimeout(timer);
      es.close();
    };
  }, [enabled, qc]);
}
