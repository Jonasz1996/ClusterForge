"use client";

import Link from "next/link";
import { useState } from "react";
import { ago, tableClass, tdClass, thClass } from "@/components/inventory/bits";
import { Alert, Badge, Button, Field, Select, Textarea, cx } from "@/components/ui";
import { isCVEDetail, severityTone, urgencyLabel, useAcceptFinding, useReopenFinding, type CVEDetail, type SecurityFinding } from "@/lib/security";

// Eén beveiligingsbevinding, open te klappen, met aanvaarden en heropenen.
// Kwetsbaarheden (pakketten) en Scans (netwerk) tonen ze allebei.

export function FindingRow({
  f,
  open: openProp,
  toggle: toggleProp,
  isAdmin,
  compact = false,
}: {
  f: SecurityFinding;
  open: boolean;
  toggle: () => void;
  isAdmin: boolean;
  compact?: boolean;
}) {
  // Binnen een samengevoegd pakket klapt elke node zelf open.
  const [own, setOwn] = useState(false);
  const open = compact ? own : openProp;
  const toggle = compact ? () => setOwn(!own) : toggleProp;
  const d = isCVEDetail(f.detail) ? f.detail : null;
  return (
    <div
      className={cx(
        "rounded-lg border bg-white dark:bg-slate-900",
        f.status === "open" ? "border-slate-200 dark:border-slate-800" : "border-dashed border-slate-300 opacity-80 dark:border-slate-700",
      )}
    >
      <button className="flex w-full flex-wrap items-start gap-x-3 gap-y-1 p-3 text-left" onClick={toggle} aria-expanded={open}>
        <Badge tone={severityTone[f.severity]}>{f.severity}</Badge>
        {d && d.kev > 0 && <Badge tone="red">actief misbruikt</Badge>}
        {f.status !== "open" && <Badge tone={f.status === "opgelost" ? "green" : "slate"}>{f.status}</Badge>}
        <span className="min-w-0 basis-full sm:basis-0 sm:flex-1">
          {compact ? (
            <span className="font-medium">
              {f.hostname || f.subject}
              {f.cluster_name && <span className="font-normal text-slate-500"> · {f.cluster_name}</span>}
            </span>
          ) : (
            <>
              <span className="font-medium">{f.title}</span>
              <span className="block text-sm text-slate-500">
                {f.hostname || f.subject}
                {f.cluster_name ? ` · ${f.cluster_name}` : ""} · {f.summary}
              </span>
            </>
          )}
        </span>
        <span className="text-xs text-slate-500">{open ? "▲" : "▼"}</span>
      </button>
      {open && (
        <div className="space-y-4 border-t border-slate-200 p-3 text-sm dark:border-slate-800">
          <div className={cx(compact && "hidden")}>
            <div className="font-medium">Oplossing</div>
            <p className="text-slate-700 dark:text-slate-300">{f.fix}</p>
            {d && d.fixable > 0 && f.node_id && (
              <Link href={`/updates`} className="text-sm font-medium text-blue-600 hover:underline dark:text-blue-400">
                Naar Updates →
              </Link>
            )}
          </div>
          {d && !compact && <CVETable d={d} />}
          <div className="text-xs text-slate-500">
            Eerst gezien {ago(f.first_seen)}, laatst {ago(f.last_seen)}
            {f.resolved_at && `, opgelost ${ago(f.resolved_at)}`}
          </div>
          {f.status === "aanvaard" && (
            <Alert kind="info">
              Aanvaard door {f.accepted_by || "?"} {ago(f.accepted_at)}: {f.accepted_note}
              {f.accepted_until && ` (tot ${new Date(f.accepted_until).toLocaleDateString("nl-BE")})`}
            </Alert>
          )}
          {isAdmin && <FindingActions f={f} />}
        </div>
      )}
    </div>
  );
}

function FindingActions({ f }: { f: SecurityFinding }) {
  const accept = useAcceptFinding();
  const reopen = useReopenFinding();
  const [note, setNote] = useState("");
  const [days, setDays] = useState("90");
  if (f.status === "opgelost") return null;
  if (f.status === "aanvaard") {
    return (
      <div>
        <Button variant="secondary" onClick={() => reopen.mutate(f.id)} disabled={reopen.isPending}>
          Heropenen
        </Button>
        {reopen.isError && <Alert>{String(reopen.error)}</Alert>}
      </div>
    );
  }
  return (
    <div className="space-y-2 rounded-md bg-slate-50 p-3 dark:bg-slate-800/50">
      <div className="font-medium">Aanvaarden</div>
      <p className="text-xs text-slate-500">
        Een aanvaarde bevinding blijft zichtbaar maar telt niet meer mee. Schrijf waarom, bijvoorbeeld &quot;alleen intern bereikbaar&quot;.
      </p>
      <div className="grid gap-2 sm:grid-cols-[1fr_10rem_auto] sm:items-end">
        <Field label="Reden" htmlFor={`note-${f.id}`}>
          <Textarea id={`note-${f.id}`} rows={2} value={note} onChange={(e) => setNote(e.target.value)} maxLength={500} />
        </Field>
        <Field label="Weer open na" htmlFor={`days-${f.id}`}>
          <Select id={`days-${f.id}`} value={days} onChange={(e) => setDays(e.target.value)}>
            <option value="30">30 dagen</option>
            <option value="90">90 dagen</option>
            <option value="365">een jaar</option>
            <option value="">nooit</option>
          </Select>
        </Field>
        <Button
          onClick={() =>
            accept.mutate({
              id: f.id,
              note,
              days: days ? Number(days) : undefined,
            })
          }
          disabled={accept.isPending || note.trim().length < 3}
        >
          Aanvaarden
        </Button>
      </div>
      {accept.isError && <Alert>{accept.error instanceof Error ? accept.error.message : String(accept.error)}</Alert>}
    </div>
  );
}

export function CVETable({ d }: { d: CVEDetail }) {
  return (
    <div className="overflow-x-auto">
      <table className={tableClass}>
        <thead>
          <tr>
            <th className={thClass}>CVE</th>
            <th className={thClass}>Ernst</th>
            <th className={thClass}>Debian</th>
            <th className={thClass}>Opgelost in</th>
            <th className={thClass}>Omschrijving</th>
          </tr>
        </thead>
        <tbody>
          {d.cves.map((c) => (
            <tr key={c.cve} className="border-t border-slate-100 dark:border-slate-800">
              <td className={cx(tdClass, "whitespace-nowrap")}>
                <a
                  href={`https://security-tracker.debian.org/tracker/${c.cve}`}
                  target="_blank"
                  rel="noreferrer"
                  className="font-mono text-blue-600 hover:underline dark:text-blue-400"
                >
                  {c.cve}
                </a>
                {c.kev && (
                  <div className="mt-1">
                    <Badge tone="red">KEV{c.ransomware ? " · ransomware" : ""}</Badge>
                  </div>
                )}
              </td>
              <td className={tdClass}>
                <Badge tone={severityTone[c.severity]}>{c.severity}</Badge>
              </td>
              <td className={cx(tdClass, "whitespace-nowrap")}>
                {urgencyLabel[c.urgency] ?? c.urgency}
                {c.scope === "remote" && <div className="text-xs text-slate-500">van op afstand</div>}
              </td>
              <td className={cx(tdClass, "whitespace-nowrap font-mono text-xs")}>{c.fixable ? c.fixed_version : "nog geen fix"}</td>
              <td className={cx(tdClass, "text-slate-600 dark:text-slate-400")}>{c.description}</td>
            </tr>
          ))}
        </tbody>
      </table>
      <p className="mt-2 text-xs text-slate-500">
        Geïnstalleerd: {d.package} {d.version} ({d.release}) · binaire pakketten: {d.binaries.join(", ")}
      </p>
    </div>
  );
}
