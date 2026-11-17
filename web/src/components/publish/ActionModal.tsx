"use client";

import { useQuery } from "@tanstack/react-query";
import Link from "next/link";
import { useState, type ReactNode } from "react";
import { Modal } from "@/components/Modal";
import { Alert, Button, Field, Input } from "@/components/ui";
import { planPublish, useStartPublish, type PublishAction } from "@/lib/publish";

// ActionModal toont eerst wat een actie zou doen (het plan van de server) en
// voert ze pas uit na een klik. Bij verwijderen moet de naam overgetikt
// worden. Daarna verwijst het naar de taak met alle stappen: kopie van NPM,
// de wijziging via de API, de vijf controles en zo nodig het terugdraaien.
export function ActionModal({
  title,
  action,
  onClose,
  children,
  blocked = false,
}: {
  title: string;
  action: PublishAction;
  onClose: () => void;
  // children zijn velden boven het plan (bij publiceren); die passen action aan.
  children?: ReactNode;
  // blocked: de velden zijn veranderd en het plan is nog niet bijgewerkt.
  blocked?: boolean;
}) {
  const start = useStartPublish();
  // Na het starten verandert NPM; het plan blijft dan staan zoals het was.
  const plan = useQuery({
    queryKey: ["publish", "plan", action],
    queryFn: () => planPublish(action),
    retry: false,
    enabled: !start.data,
    staleTime: Infinity,
  });
  const [confirm, setConfirm] = useState("");
  const p = plan.data;
  const needConfirm = !!p?.confirm;
  const job = start.data?.job;

  return (
    <Modal title={title} onClose={onClose} wide>
      <div className="space-y-4 text-sm">
        {children}
        {plan.isLoading && <p className="text-slate-500">Plan ophalen…</p>}
        {plan.isError && <Alert>{(plan.error as Error).message}</Alert>}
        {p && (
          <div className="space-y-3">
            <p className="font-medium">{p.title}</p>
            {(p.lines ?? []).length > 0 && (
              <ol className="list-decimal space-y-1 pl-5 text-slate-700 dark:text-slate-300">
                {(p.lines ?? []).map((l, i) => (
                  <li key={i}>{l}</li>
                ))}
              </ol>
            )}
            {(p.warnings ?? []).map((w, i) => (
              <Alert key={i} kind="info">
                {w}
              </Alert>
            ))}
            <p className="text-xs text-slate-500">
              ClusterForge maakt eerst een kopie van de NPM-database en de config, verandert alles via de API van NPM (database én config samen),
              controleert daarna vijf dingen en draait terug als iets niet klopt.
            </p>
            {needConfirm && !job && (
              <Field label={`Tik ${p.confirm} over om te bevestigen`} htmlFor="publish-confirm">
                <Input id="publish-confirm" value={confirm} onChange={(e) => setConfirm(e.target.value)} autoComplete="off" />
              </Field>
            )}
          </div>
        )}
        {start.isError && <Alert>{(start.error as Error).message}</Alert>}
        {job ? (
          <Alert kind="success">
            Gestart.{" "}
            <Link href={`/taken/detail?id=${job.id}`} className="underline">
              Volg de taak
            </Link>
            .
          </Alert>
        ) : null}
        <div className="flex justify-end gap-2">
          <Button variant="secondary" onClick={onClose}>
            {job ? "Sluiten" : "Annuleren"}
          </Button>
          {!job && (
            <Button
              variant={action.action === "delete" ? "danger" : "primary"}
              disabled={!p || blocked || start.isPending || (needConfirm && confirm.trim() !== p?.confirm)}
              onClick={() =>
                start.mutate({
                  ...action,
                  confirm: needConfirm ? confirm.trim() : undefined,
                })
              }
            >
              {start.isPending ? "Bezig…" : "Uitvoeren"}
            </Button>
          )}
        </div>
      </div>
    </Modal>
  );
}
