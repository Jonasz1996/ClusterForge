"use client";

import { useRouter } from "next/navigation";
import { useState } from "react";
import { Modal } from "@/components/Modal";
import { ProdConfirm, prodConfirmed, useProdTotp } from "@/components/ProdConfirm";
import { Alert, Badge, Button, Field, Textarea } from "@/components/ui";
import { ApiError } from "@/lib/api/client";
import { shortSha, useApproveGitChange, useReapplyCluster, useRejectGitChange, type GitChangeDetail } from "@/lib/gitops";

// errorText geeft de melding van de server, met een hint als het plan
// intussen verviel.
function errorText(err: unknown, fallback: string) {
  if (!err) return null;
  if (!(err instanceof ApiError)) return fallback;
  if (err.code === "plan_changed" || err.code === "not_pending") return `${err.message}. Ga terug naar GitOps voor het nieuwe plan.`;
  return err.message;
}

// ApproveDialog laat zien wat goedkeuren doet: de nieuwe revisie, en de
// nodes in de volgorde van de taak. Op prod tikt de beheerder de slug in.
export function ApproveDialog({ c, onClose }: { c: GitChangeDetail; onClose: () => void }) {
  const router = useRouter();
  const approve = useApproveGitChange(c.id);
  const totp = useProdTotp();
  const [confirm, setConfirm] = useState("");
  const p = c.plan;
  const name = c.cluster_name || c.slug;
  const noJob = p.no_steps && !c.full_apply;
  const ok = !c.needs_confirmation || prodConfirmed(c.slug, confirm, totp);
  const start = () =>
    approve.mutate(c.needs_confirmation ? confirm : undefined, {
      onSuccess: (d) => {
        if (d.job) router.push(`/taken/detail?id=${d.job.id}`);
        else onClose();
      },
    });
  const err = errorText(approve.error, "Goedkeuren mislukt");

  return (
    <Modal title="Goedkeuren en toepassen" onClose={onClose} wide>
      <div className="space-y-4 text-sm">
        <p>
          ClusterForge schrijft revisie {p.base_revision + 1} van {name} met bron Git en commit <code className="text-xs">{shortSha(c.commit.sha)}</code>
          {p.metadata.length > 0 && <>, en zet {p.metadata.map((f) => `${f.label.toLowerCase()} op ${f.to || "leeg"}`).join(", ")}</>}.
        </p>
        {noJob ? (
          <p className="text-slate-600 dark:text-slate-400">Op de nodes verandert niets, dus de wijziging is meteen toegepast.</p>
        ) : (
          <>
            <p className="text-slate-600 dark:text-slate-400">Daarna past een taak de wijziging toe, node voor node in deze volgorde:</p>
            <ol className="space-y-2">
              {p.nodes.map((n, i) => (
                <li key={n.node_id} className="rounded-md border border-slate-200 px-3 py-2 dark:border-slate-800">
                  <div className="flex flex-wrap items-center gap-2">
                    <span className="text-slate-500">{i + 1}.</span>
                    <span className="font-medium">{n.hostname}</span>
                    {n.vips.length > 0 && <Badge tone="amber">VIP-eigenaar · {n.vips.join(", ")}</Badge>}
                  </div>
                  <p className="mt-1 pl-5 text-slate-600 dark:text-slate-400">{n.steps.map((s) => s.title).join(", ")}</p>
                </li>
              ))}
            </ol>
            {c.full_apply && (
              <Alert kind="info">
                Een eerdere revisie staat nog niet op alle nodes. Daarom past de taak op elke node alle stappen van de template opnieuw toe, niet
                alleen wat hierboven verandert. Wat genegeerd wordt, blijft zoals het is.
              </Alert>
            )}
            <p className="rounded border border-amber-200 bg-amber-50 px-3 py-2 text-amber-900 dark:border-amber-900 dark:bg-amber-950 dark:text-amber-200">
              Na elke node wacht de taak tot die node gezond is, elk VIP één houder heeft en de controles van de template slagen. Lukt dat niet,
              dan stopt ze, en blijven de nodes daarna ongemoeid; de VIP-eigenaar komt als laatste. Terugdraaien doe je met git revert.
            </p>
          </>
        )}
        {c.needs_confirmation && <ProdConfirm slug={c.slug} value={confirm} onChange={setConfirm} />}
        {err && <Alert>{err}</Alert>}
        <div className="flex justify-end gap-2">
          <Button type="button" variant="secondary" onClick={onClose}>
            Annuleren
          </Button>
          <Button type="button" disabled={!ok || approve.isPending} onClick={start}>
            {approve.isPending ? "Goedkeuren…" : "Goedkeuren en toepassen"}
          </Button>
        </div>
      </div>
    </Modal>
  );
}

// RejectDialog vraagt een reden. Het bestand blijft afgewezen tot een
// nieuwe commit het wijzigt.
export function RejectDialog({ c, onClose }: { c: GitChangeDetail; onClose: () => void }) {
  const reject = useRejectGitChange(c.id);
  const [reason, setReason] = useState("");
  const err = errorText(reject.error, "Afwijzen mislukt");
  return (
    <Modal title="Wijziging afwijzen" onClose={onClose}>
      <form
        className="space-y-4 text-sm"
        onSubmit={(e) => {
          e.preventDefault();
          reject.mutate(reason, { onSuccess: onClose });
        }}
      >
        <p className="text-slate-600 dark:text-slate-400">
          Er verandert niets aan het cluster. Het bestand blijft afgewezen tot een nieuwe commit het wijzigt; draai de commit dus ook in Git terug.
        </p>
        <Field label="Reden" htmlFor="reject-reason">
          <Textarea id="reject-reason" rows={3} required maxLength={500} value={reason} onChange={(e) => setReason(e.target.value)} />
        </Field>
        {err && <Alert>{err}</Alert>}
        <div className="flex justify-end gap-2">
          <Button type="button" variant="secondary" onClick={onClose}>
            Annuleren
          </Button>
          <Button type="submit" variant="danger" disabled={reason.trim() === "" || reject.isPending}>
            {reject.isPending ? "Afwijzen…" : "Afwijzen"}
          </Button>
        </div>
      </form>
    </Modal>
  );
}

// ReapplyDialog past de huidige revisie opnieuw toe, met alle stappen.
export function ReapplyDialog({
  clusterId,
  name,
  slug,
  prod,
  revision,
  onClose,
}: {
  clusterId: string;
  name: string;
  slug: string;
  prod: boolean;
  revision: number;
  onClose: () => void;
}) {
  const router = useRouter();
  const reapply = useReapplyCluster(clusterId);
  const totp = useProdTotp();
  const [confirm, setConfirm] = useState("");
  const ok = !prod || prodConfirmed(slug, confirm, totp);
  const err = errorText(reapply.error, "Opnieuw toepassen mislukt");
  return (
    <Modal title="Opnieuw toepassen" onClose={onClose}>
      <div className="space-y-4 text-sm">
        <p>
          Een taak past revisie {revision} van {name} opnieuw toe: op elke node alle stappen van de template, node voor node, met de
          VIP-eigenaar als laatste. Wat genegeerd wordt, blijft zoals het is. Na elke node wacht de taak tot die gezond is; lukt dat niet, dan
          stopt ze.
        </p>
        {prod && <ProdConfirm slug={slug} value={confirm} onChange={setConfirm} />}
        {err && <Alert>{err}</Alert>}
        <div className="flex justify-end gap-2">
          <Button type="button" variant="secondary" onClick={onClose}>
            Annuleren
          </Button>
          <Button
            type="button"
            disabled={!ok || reapply.isPending}
            onClick={() => reapply.mutate(prod ? confirm : undefined, { onSuccess: (j) => router.push(`/taken/detail?id=${j.id}`) })}
          >
            {reapply.isPending ? "Starten…" : "Opnieuw toepassen"}
          </Button>
        </div>
      </div>
    </Modal>
  );
}
