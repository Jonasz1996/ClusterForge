"use client";

import { useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { Answer, LiveAnswer, useStream } from "@/components/assistant/parts";
import { Modal } from "@/components/Modal";
import { Alert, Button } from "@/components/ui";
import { summarizeDocs, useAssistantSettings, useDeleteDocSummary } from "@/lib/assistant";
import type { DocSummaryMeta } from "@/lib/docs";

// DocSummaryButtons laat de assistent de samenvatting 'In het kort' van een
// cluster schrijven, of haalt ze weg. Ze staat bovenaan het hoofdstuk
// Architectuur en gaat mee naar GitHub. Alleen voor een beheerder.
export function DocSummaryButtons({ cluster, name, summary }: { cluster: string; name: string; summary: DocSummaryMeta | null }) {
  const settings = useAssistantSettings();
  const qc = useQueryClient();
  const stream = useStream();
  const remove = useDeleteDocSummary();
  const [open, setOpen] = useState(false);
  const canUse = settings.data?.can_use ?? false;
  if (!canUse && !summary) return null;

  const write = () => {
    setOpen(true);
    void stream
      .run((onEvent, signal) => summarizeDocs(cluster, onEvent, signal), "De samenvatting kon niet beginnen.")
      .then(
        (res) =>
          res && Promise.all([qc.invalidateQueries({ queryKey: ["docs"] }), qc.invalidateQueries({ queryKey: ["assistant"] })]),
      );
  };
  const saved = stream.live?.done && !stream.live.done.note && !stream.live.error;

  return (
    <>
      {canUse && (
        <Button variant="secondary" onClick={write} disabled={stream.busy}>
          {stream.busy ? "Schrijft…" : summary ? "In het kort opnieuw" : "In het kort laten schrijven"}
        </Button>
      )}
      {summary && (
        <Button
          variant="ghost"
          disabled={remove.isPending || stream.busy}
          onClick={() => {
            if (window.confirm(`De samenvatting 'In het kort' van ${name} weghalen?`)) remove.mutate(cluster);
          }}
        >
          Weghalen
        </Button>
      )}
      {open && (
        <Modal title={`In het kort: ${name}`} onClose={() => setOpen(false)} wide>
          <div className="space-y-3">
            <p className="text-sm text-slate-600 dark:text-slate-400">
              De assistent leest de vijf hoofdstukken en schrijft een samenvatting van een paar alinea&apos;s. Ze komt bovenaan
              het hoofdstuk Architectuur, met het model en de datum, en gaat mee naar GitHub.
            </p>
            {stream.live && (
              <Answer>
                <LiveAnswer live={stream.live} catalog={settings.data?.models} />
              </Answer>
            )}
            {stream.error && <Alert>{stream.error}</Alert>}
            {saved && <Alert kind="success">Bewaard; ze staat nu bovenaan het hoofdstuk Architectuur.</Alert>}
            <div className="flex justify-end">
              <Button variant="secondary" onClick={() => setOpen(false)}>
                Sluiten
              </Button>
            </div>
          </div>
        </Modal>
      )}
      {remove.isError && <span className="self-center text-xs text-red-600">Weghalen mislukt: {remove.error.message}</span>}
    </>
  );
}
