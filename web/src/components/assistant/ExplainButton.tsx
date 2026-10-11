"use client";

import { useQueryClient } from "@tanstack/react-query";
import Link from "next/link";
import { useState } from "react";
import { Modal } from "@/components/Modal";
import { Alert, Button } from "@/components/ui";
import { explainRisk, useAssistantSettings, type ExplainInput } from "@/lib/assistant";
import { Answer, LiveAnswer, useStream } from "./parts";

// ExplainButton laat de assistent een bevinding of een SPOF uitleggen: wat
// het betekent, waarom het telt in deze fleet en hoe je het oplost. De
// uitleg komt live binnen in een venster en is een gesprek waarin je kunt
// doorvragen. Hij staat er alleen als de assistent voor jou aan staat.
export function ExplainButton({ input, title, button = false }: { input: ExplainInput; title: string; button?: boolean }) {
  const settings = useAssistantSettings();
  const qc = useQueryClient();
  const stream = useStream();
  const [open, setOpen] = useState(false);
  if (!settings.data?.can_use) return null;

  const show = () => {
    setOpen(true);
    // Een tweede klik toont dezelfde uitleg weer.
    if (stream.live) return;
    void stream
      .run((onEvent, signal) => explainRisk(input, onEvent, signal), "De uitleg kon niet beginnen.")
      .then((res) => res && qc.invalidateQueries({ queryKey: ["assistant"] }));
  };
  const conv = stream.live?.done?.conversation_id;

  return (
    <>
      {button ? (
        <Button variant="secondary" onClick={show}>
          Leg uit
        </Button>
      ) : (
        <button type="button" className="text-xs text-brand-600 hover:underline dark:text-sky-300" onClick={show}>
          Leg uit
        </button>
      )}
      {open && (
        <Modal title={`Uitleg: ${title.length > 90 ? title.slice(0, 89) + "…" : title}`} onClose={() => setOpen(false)} wide>
          <div className="space-y-3">
            {stream.live && (
              <Answer>
                <LiveAnswer live={stream.live} catalog={settings.data.models} />
              </Answer>
            )}
            {stream.error && <Alert>{stream.error}</Alert>}
            <div className="flex flex-wrap items-center justify-between gap-2">
              <p className="text-xs text-slate-500">De assistent leest alleen; hij voert niets uit.</p>
              <div className="flex gap-2">
                {conv && (
                  <Link
                    href={`/assistent?gesprek=${conv}`}
                    className="inline-flex items-center rounded-md px-3.5 py-2 text-sm font-medium text-brand-600 hover:underline dark:text-brand-500"
                  >
                    Doorvragen
                  </Link>
                )}
                <Button variant="secondary" onClick={() => setOpen(false)}>
                  Sluiten
                </Button>
              </div>
            </div>
          </div>
        </Modal>
      )}
    </>
  );
}
