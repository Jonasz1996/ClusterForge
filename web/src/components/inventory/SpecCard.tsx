"use client";

import Link from "next/link";
import { useState } from "react";
import { useMutation } from "@tanstack/react-query";
import { Alert, Badge, Button, Card, Input } from "@/components/ui";
import { copyText } from "@/lib/clipboard";
import { call } from "@/lib/replace";
import { QueryState, tableClass, tdClass, thClass } from "@/components/inventory/bits";
import { specSources, useSpecHistory, type SpecRevision } from "@/lib/deploy";
import { CommitLink } from "@/components/gitops/bits";
import { SpecEditDialog } from "@/components/inventory/SpecEdit";
import type { ClusterDetail } from "@/lib/inventory";

const timeFmt = new Intl.DateTimeFormat("nl-BE", { dateStyle: "medium", timeStyle: "short" });
const none = <span className="text-slate-400">–</span>;

// SpecCard toont de gewenste staat van een cluster uit een template: de
// templateversie, de parameters, wat er niet klopt en elke revisie. Een
// beheerder wijzigt hier de spec van een cluster dat geen Git volgt.
export function SpecCard({ cluster, isAdmin }: { cluster: ClusterDetail; isAdmin: boolean }) {
  const clusterId = cluster.id;
  const q = useSpecHistory(clusterId);
  const h = q.data;
  const [editing, setEditing] = useState(false);
  return (
    <Card
      title={
        <span className="flex flex-wrap items-center justify-between gap-2">
          <span className="flex flex-wrap items-center gap-2">
            Specificatie
            {h && h.revision > 0 && <span className="text-sm font-normal text-slate-500">revisie {h.revision}</span>}
          </span>
          {isAdmin && h?.template && !cluster.git_managed && (
            <Button variant="secondary" onClick={() => setEditing(true)}>
              Wijzigen…
            </Button>
          )}
        </span>
      }
    >
      {editing && <SpecEditDialog cluster={cluster} onClose={() => setEditing(false)} />}
      <QueryState q={q}>
        {h && !h.template && <p className="text-sm text-slate-500">Dit cluster komt niet uit een template en heeft geen gewenste staat.</p>}
        {h && h.template && (
          <div className="space-y-5">
            <p className="flex flex-wrap items-center gap-2 text-sm">
              <Link href="/templates" className="font-medium text-brand-700 hover:underline dark:text-sky-300">
                {h.template.name}
              </Link>
              <span>{h.template.version}</span>
              {!h.template.available && <Badge tone="red">Niet in deze server</Badge>}
              {h.template.available && h.template.latest && h.template.latest !== h.template.version && (
                <Badge tone="amber">Nieuwer: {h.template.latest}</Badge>
              )}
            </p>

            {h.notes.length > 0 && (
              <ul className="space-y-1 rounded border border-amber-200 bg-amber-50 px-3 py-2 text-sm text-amber-900 dark:border-amber-900 dark:bg-amber-950 dark:text-amber-200">
                {h.notes.map((n) => (
                  <li key={n}>{n}</li>
                ))}
              </ul>
            )}

            {h.params.length > 0 && (
              <dl className="grid gap-x-6 gap-y-2 text-sm sm:grid-cols-[12rem_1fr]">
                {h.params.map((p) => (
                  <div key={p.name} className="contents">
                    <dt className="text-slate-500">{p.label}</dt>
                    <dd>
                      {p.secret ? (
                        isAdmin ? (
                          <RevealParam clusterId={clusterId} name={p.name} />
                        ) : (
                          <span className="text-slate-500">versleuteld opgeslagen</span>
                        )
                      ) : p.value === null ? none : <code className="text-xs">{p.value}</code>}
                    </dd>
                  </div>
                ))}
              </dl>
            )}

            <div>
              <h3 className="mb-2 text-sm font-medium">Historie</h3>
              {h.items.length === 0 ? (
                <p className="text-sm text-slate-500">Nog geen revisies.</p>
              ) : (
                <div className="overflow-x-auto">
                  <table className={`${tableClass} min-w-[40rem]`}>
                    <thead className="border-b border-slate-200 dark:border-slate-800">
                      <tr>
                        <th className={thClass}>Revisie</th>
                        <th className={thClass}>Wanneer</th>
                        <th className={thClass}>Door</th>
                        <th className={thClass}>Bron</th>
                        <th className={thClass}>Wat veranderde</th>
                      </tr>
                    </thead>
                    <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
                      {h.items.map((r, i) => (
                        <tr key={r.revision}>
                          <td className={tdClass}>{r.revision}</td>
                          <td className={`${tdClass} whitespace-nowrap`}>{timeFmt.format(new Date(r.created_at))}</td>
                          <td className={tdClass}>
                            {r.created_by ? <span className={r.created_by.deleted ? "text-slate-500 line-through" : undefined}>{r.created_by.name}</span> : none}
                          </td>
                          <td className={tdClass}>
                            {specSources[r.source]}
                            {r.commit_sha && (
                              <>
                                {" · "}
                                <CommitLink sha={r.commit_sha} url={r.commit_url || undefined} />
                              </>
                            )}
                          </td>
                          <td className={tdClass}>
                            <Changes r={r} first={i === h.items.length - 1} />
                          </td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              )}
            </div>
          </div>
        )}
      </QueryState>
    </Card>
  );
}

function Changes({ r, first }: { r: SpecRevision; first: boolean }) {
  if (first) {
    return (
      <span>
        Uitgerold uit {r.template} {r.template_version}
        {r.nodes.length > 0 && <> met {r.nodes.join(", ")}</>}
      </span>
    );
  }
  if (r.changes.length === 0) return <span className="text-slate-500">Niets veranderd</span>;
  return (
    <ul className="space-y-0.5">
      {r.changes.map((c) => (
        <li key={c.label}>
          {c.label}{" "}
          {c.from === "" ? (
            <>
              gezet op <code className="text-xs">{c.to}</code>
            </>
          ) : c.to === "" ? (
            <>
              weggehaald (was <code className="text-xs">{c.from}</code>)
            </>
          ) : (
            <>
              van <code className="text-xs">{c.from}</code> naar <code className="text-xs">{c.to}</code>
            </>
          )}
        </li>
      ))}
    </ul>
  );
}

// RevealParam toont een versleutelde parameter (fase 48, 4.4), zoals het
// beheerderswachtwoord van de RustDesk-API: pas na je code (of je
// wachtwoord zonder tweestapsverificatie), en het komt in het logboek.
function RevealParam({ clusterId, name }: { clusterId: string; name: string }) {
  const [asking, setAsking] = useState(false);
  const [code, setCode] = useState("");
  const [value, setValue] = useState<string | null>(null);
  const reveal = useMutation({
    mutationFn: () =>
      call<{ name: string; value: string }>(
        "POST",
        `/clusters/${clusterId}/params/reveal`,
        /^\d{6}$/.test(code) ? { name, password: code, totp_code: code } : { name, password: code },
      ),
    onSuccess: (r) => {
      setValue(r.value);
      setAsking(false);
      setCode("");
    },
  });
  if (value !== null) {
    return (
      <span className="flex flex-wrap items-center gap-2">
        <code className="break-all text-xs">{value}</code>
        <Button variant="ghost" onClick={() => void copyText(value)}>
          Kopiëren
        </Button>
        <Button variant="ghost" onClick={() => setValue(null)}>
          Verbergen
        </Button>
      </span>
    );
  }
  if (!asking) {
    return (
      <span className="flex flex-wrap items-center gap-2">
        <span className="text-slate-500">versleuteld opgeslagen</span>
        <Button variant="secondary" onClick={() => setAsking(true)}>
          Tonen…
        </Button>
      </span>
    );
  }
  return (
    <div className="space-y-1">
      <form
        className="flex flex-wrap gap-2"
        onSubmit={(e) => {
          e.preventDefault();
          reveal.mutate();
        }}
      >
        <Input
          type="password"
          autoComplete="off"
          className="w-48"
          placeholder="Code of wachtwoord"
          value={code}
          onChange={(e) => setCode(e.target.value)}
          autoFocus
        />
        <Button type="submit" disabled={reveal.isPending || code === ""}>
          Tonen
        </Button>
        <Button variant="ghost" type="button" onClick={() => setAsking(false)}>
          Annuleren
        </Button>
      </form>
      <p className="text-xs text-slate-500">Tonen vraagt je code van tweestapsverificatie (of je wachtwoord) en komt in het logboek.</p>
      {reveal.error && <Alert>{reveal.error.message}</Alert>}
    </div>
  );
}
