"use client";

import { useQueryClient } from "@tanstack/react-query";
import Link from "next/link";
import { Markdown } from "@/components/Markdown";
import { Alert, Badge, Button, Card } from "@/components/ui";
import { analyzeIncident, dollars, modelLabel, useAssistantSettings, type IncidentAI } from "@/lib/assistant";
import { auditTimeFmt } from "@/lib/audit";
import { Answer, LiveAnswer, useStream } from "./parts";

const linkClass = "text-brand-600 hover:underline dark:text-brand-500";

// IncidentAnalysisCard is de analyse van de assistent bij een incident: in
// het kort wat er gebeurde, de waarschijnlijke oorzaak met bewijs, wat je
// nog moet controleren en wat je kunt doen. Analyseer vraagt een nieuwe; ze
// komt live binnen en vervangt de vorige als ze af is.
export function IncidentAnalysisCard({ id, ai }: { id: string; ai: IncidentAI | null }) {
  const settings = useAssistantSettings();
  const qc = useQueryClient();
  const stream = useStream();
  const canUse = settings.data?.can_use ?? false;
  // Een analyse met de rechten van een beheerder vervangt een viewer niet.
  const canAnalyze = canUse && !ai?.hidden;
  if (!ai && !canUse && !stream.live) return null;

  const analyze = async () => {
    const res = await stream.run((onEvent, signal) => analyzeIncident(id, onEvent, signal), "De analyse kon niet beginnen.");
    if (!res) return;
    await Promise.all([qc.invalidateQueries({ queryKey: ["incidents", id] }), qc.invalidateQueries({ queryKey: ["assistant"] })]);
    // Bewaard: de analyse staat nu hieronder. Anders blijft staan waarom niet.
    if (res.done && !res.done.note && !res.error) stream.reset();
  };

  return (
    <Card
      title={
        <span className="flex flex-wrap items-center justify-between gap-2">
          <span>Analyse door de assistent</span>
          {canAnalyze && (
            <Button variant="secondary" onClick={analyze} disabled={stream.busy}>
              {stream.busy ? "Analyseert…" : ai ? "Opnieuw analyseren" : "Analyseer"}
            </Button>
          )}
        </span>
      }
    >
      <div className="space-y-3">
        {stream.live && (
          <Answer>
            <LiveAnswer live={stream.live} catalog={settings.data?.models} />
          </Answer>
        )}
        {stream.error && <Alert>{stream.error}</Alert>}
        {!stream.busy && ai && <Stored ai={ai} catalog={settings.data?.models} />}
        {!stream.live && !ai && (
          <p className="text-sm text-slate-500">
            De assistent leest het incident, de analyse hierboven, de tijdlijn en de metrics, en kijkt verder waar dat helpt. Hij
            schrijft wat er gebeurde, de waarschijnlijke oorzaak met het bewijs, wat je nog moet controleren en wat je kunt doen.
            Hij voert zelf niets uit.
          </p>
        )}
      </div>
    </Card>
  );
}

function Stored({ ai, catalog }: { ai: IncidentAI; catalog?: { id: string; label: string }[] }) {
  const when = auditTimeFmt.format(new Date(ai.at));
  if (ai.hidden || ai.text === null) {
    return (
      <p className="text-sm text-slate-500">
        Er is een analyse van {when}, gemaakt met de rechten van een beheerder. Die zie je als viewer niet; een vraag over dit
        incident stel je met Vraag de assistent.
      </p>
    );
  }
  return (
    <>
      <Markdown text={ai.text} />
      <p className="flex flex-wrap items-center gap-x-1.5 gap-y-1 border-t border-slate-100 pt-2 text-xs text-slate-500 dark:border-slate-800">
        {ai.automatic && <Badge tone="blue">automatisch</Badge>}
        <span>
          {ai.model
            .split(", ")
            .filter(Boolean)
            .map((m) => modelLabel(m, catalog))
            .join(", ")}{" "}
          · {when}
          {ai.by && ` · gevraagd door ${ai.by}`} · {dollars(ai.cost_usd)}
        </span>
        {ai.conversation_id && (
          <>
            ·{" "}
            <Link href={`/assistent?gesprek=${ai.conversation_id}`} className={linkClass}>
              Het gesprek met de tools
            </Link>
          </>
        )}
      </p>
    </>
  );
}
