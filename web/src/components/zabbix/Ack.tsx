"use client";

import { useState } from "react";
import { Modal } from "@/components/Modal";
import { toast } from "@/components/Toast";
import { Alert, Button, Field, Input, Select, Textarea } from "@/components/ui";
import { ApiError } from "@/lib/api/client";
import { useIsAdmin } from "@/lib/inventory";
import { severities, severityLabel, useAckZabbixProblem, type ZabbixProblem } from "@/lib/zabbixMonitoring";

// AckButton bevestigt een probleem in Zabbix (fase 33 M6), alleen voor een
// admin. Raakt het een prod-node, dan vraagt de server de code van
// tweestapsverificatie of het wachtwoord.
export function AckButton({ p }: { p: ZabbixProblem }) {
  const admin = useIsAdmin();
  const [open, setOpen] = useState(false);
  if (!admin) return null;
  return (
    <>
      <button
        type="button"
        onClick={() => setOpen(true)}
        className="shrink-0 rounded-md border border-slate-300 px-2.5 py-1 text-xs font-medium text-slate-700 hover:bg-slate-50 dark:border-slate-700 dark:text-slate-200 dark:hover:bg-slate-800"
      >
        {p.acknowledged ? "Bericht" : "Bevestigen"}
      </button>
      {open && <AckDialog p={p} onClose={() => setOpen(false)} />}
    </>
  );
}

function AckDialog({ p, onClose }: { p: ZabbixProblem; onClose: () => void }) {
  const ack = useAckZabbixProblem();
  const [message, setMessage] = useState("");
  const [close, setClose] = useState(false);
  const [severity, setSeverity] = useState<number>(p.severity);
  const [confirm, setConfirm] = useState("");
  const [needCode, setNeedCode] = useState<string | null>(null);
  const err = !ack.error ? null : ack.error instanceof ApiError ? (ack.error.code === "needs_confirmation" ? null : ack.error.message) : "Er ging iets mis.";

  const submit = () =>
    ack.mutate(
      {
        eventid: p.eventid,
        body: {
          message,
          close,
          severity: severity === p.severity ? null : severity,
          ...(confirm ? (/^\d{6,8}$/.test(confirm) ? { totp_code: confirm } : { password: confirm }) : {}),
        },
      },
      {
        onSuccess: () => {
          toast(close ? "Bevestigd en gesloten in Zabbix." : "Bevestigd in Zabbix.");
          onClose();
        },
        onError: (e) => {
          if (e instanceof ApiError && e.code === "needs_confirmation") setNeedCode(e.message);
        },
      },
    );

  return (
    <Modal title="Probleem bevestigen in Zabbix" onClose={onClose}>
      <div className="space-y-4">
        <p className="text-sm break-words">{p.name}</p>
        <Field label="Bericht" htmlFor="ack-msg" hint="Komt in Zabbix bij het probleem, met je naam erachter.">
          <Textarea id="ack-msg" rows={3} maxLength={1800} value={message} onChange={(e) => setMessage(e.target.value)} placeholder="bv. ik kijk ernaar" autoFocus />
        </Field>
        <Field label="Ernst" htmlFor="ack-sev">
          <Select id="ack-sev" value={severity} onChange={(e) => setSeverity(Number(e.target.value))}>
            {[...severities].map((s) => (
              <option key={s} value={s}>
                {severityLabel[s]}
                {s === p.severity ? " (nu)" : ""}
              </option>
            ))}
          </Select>
        </Field>
        <label className="flex items-start gap-2 text-sm">
          <input type="checkbox" className="mt-0.5 size-4 rounded border-slate-300" checked={close} onChange={(e) => setClose(e.target.checked)} />
          <span>
            Probleem sluiten
            <span className="block text-xs text-slate-500">Alleen als de trigger handmatig sluiten toelaat; komt het probleem terug, dan opent Zabbix het opnieuw.</span>
          </span>
        </label>
        {needCode && (
          <Field label="Bevestigen" htmlFor="ack-code" hint={needCode}>
            <Input id="ack-code" type="password" autoComplete="one-time-code" value={confirm} onChange={(e) => setConfirm(e.target.value)} />
          </Field>
        )}
        {err && <Alert>{err}</Alert>}
        <div className="flex justify-end gap-2">
          <Button variant="secondary" onClick={onClose}>
            Annuleren
          </Button>
          <Button onClick={submit} disabled={ack.isPending || (!!needCode && confirm === "")}>
            {ack.isPending ? "Bezig…" : close ? "Bevestigen en sluiten" : "Bevestigen"}
          </Button>
        </div>
      </div>
    </Modal>
  );
}
