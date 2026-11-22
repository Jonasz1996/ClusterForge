"use client";

import { Suspense, useState } from "react";
import { ActionDialog, ClusterStrip, JobBanner, OpnGate } from "@/components/opnsense/bits";
import { Alert, Badge, Button, Card, PageHeader } from "@/components/ui";
import { useMe } from "@/lib/auth";
import { readNode, useOpnAdvice, useOpnAdviceApply, useOpnAdviceIgnore, type OpnAdviceItem, type OpnCluster } from "@/lib/opnsense";
import type { Job } from "@/lib/proxmox";

export default function OpnAdvicePage() {
  return (
    <div className="space-y-6">
      <PageHeader
        title="Regeladvies"
        description="Voorstellen voor je firewallregels uit wat ClusterForge weet: verkeer dat geblokkeerd werd naar een poort waarop een node echt luistert, dubbele regels, regels die nooit raken omdat een eerdere alles al afvangt, regels zonder treffer in 30 dagen en regels die veel van internet doorlaten. Niets gebeurt vanzelf: je kiest, tikt de naam van het cluster over en de wijziging gaat met herstelpunt en terugdraaitimer."
      />
      <Suspense>
        <OpnGate>{(c) => <Advice key={c.key} c={c} />}</OpnGate>
      </Suspense>
    </div>
  );
}

type Result = { kind: "success"; job: Job } | { kind: "error"; message: string } | null;

const groups: { kind: OpnAdviceItem["kind"]; title: string; tone: "green" | "blue" | "red" | "amber" | "slate" }[] = [
  { kind: "wan", title: "Let op: open naar internet", tone: "red" },
  { kind: "open", title: "Geblokkeerd, maar er luistert iets", tone: "green" },
  { kind: "shadow", title: "Regels die nooit raken", tone: "amber" },
  { kind: "dup", title: "Dubbele regels", tone: "amber" },
  { kind: "unused", title: "Geen treffer in 30 dagen", tone: "slate" },
];

function Advice({ c }: { c: OpnCluster }) {
  const node = readNode(c);
  const me = useMe();
  const admin = me.data?.user.role === "admin";
  const a = useOpnAdvice(node?.node_id);
  const apply = useOpnAdviceApply(node?.node_id ?? "");
  const ignore = useOpnAdviceIgnore(node?.node_id ?? "");
  const [picked, setPicked] = useState<string[]>([]);
  const [open, setOpen] = useState(false);
  const [result, setResult] = useState<Result>(null);
  const items = a.data?.items ?? [];
  const chosen = items.filter((i) => picked.includes(i.key));

  return (
    <div className="space-y-4">
      <ClusterStrip c={c} />
      <JobBanner result={result} />
      {a.isError && <Alert>{(a.error as Error).message}</Alert>}
      {a.data?.notes.map((n) => (
        <Alert key={n} kind="info">
          {n}
        </Alert>
      ))}
      {admin && items.length > 0 && (
        <div className="flex flex-wrap items-center gap-2 text-sm">
          <Button disabled={picked.length === 0} onClick={() => setOpen(true)}>
            Gekozen toepassen ({picked.length})
          </Button>
          <span className="text-slate-500">Kies de voorstellen die je wilt; hoogstens 50 tegelijk.</span>
        </div>
      )}
      {!a.data ? (
        <Card title="Regeladvies">
          <p className="text-sm text-slate-500">Laden…</p>
        </Card>
      ) : items.length === 0 ? (
        <Card title="Regeladvies">
          <p className="text-sm text-slate-500">Geen voorstellen. De regels zien er netjes uit.</p>
        </Card>
      ) : (
        groups
          .filter((g) => items.some((i) => i.kind === g.kind))
          .map((g) => (
            <Card key={g.kind} title={`${g.title} · ${items.filter((i) => i.kind === g.kind).length}`}>
              <ul className="divide-y divide-slate-100 text-sm dark:divide-slate-800">
                {items
                  .filter((i) => i.kind === g.kind)
                  .map((it) => (
                    <li key={it.key} className={`flex items-start gap-3 py-2 ${it.ignored ? "opacity-50" : ""}`}>
                      {admin && it.op !== "conflict" ? (
                        <input
                          type="checkbox"
                          className="mt-1"
                          aria-label={`Kies ${it.title}`}
                          checked={picked.includes(it.key)}
                          onChange={(e) => setPicked((p) => (e.target.checked ? [...p, it.key] : p.filter((k) => k !== it.key)))}
                        />
                      ) : (
                        <span className="w-[13px]" />
                      )}
                      <div className="min-w-0 flex-1">
                        <div>
                          <Badge tone={g.tone}>
                            {it.op === "add" ? "nieuwe regel" : it.op === "del" ? "weg" : it.op === "set" ? "uitzetten" : "let op"}
                          </Badge>{" "}
                          {it.title}
                        </div>
                        {it.detail && <div className="mt-0.5 text-xs text-slate-500">{it.detail}</div>}
                      </div>
                      {admin && (
                        <button
                          type="button"
                          className="text-xs whitespace-nowrap text-slate-500 underline"
                          onClick={() => ignore.mutate({ key: it.key, ignore: !it.ignored })}
                        >
                          {it.ignored ? "niet meer negeren" : "negeren"}
                        </button>
                      )}
                    </li>
                  ))}
              </ul>
            </Card>
          ))
      )}
      {open && a.data && (
        <ActionDialog
          title="Regeladvies toepassen"
          confirm={a.data.confirm}
          prod={c.environment === "prod"}
          label="Toepassen"
          start={(b) => apply.mutateAsync({ keys: picked, ...b })}
          onClose={() => setOpen(false)}
          onDone={(j) => {
            setOpen(false);
            setPicked([]);
            setResult({ kind: "success", job: j });
          }}
        >
          <p>Deze wijzigingen gaan als één wijziging naar {a.data.firewall}, met herstelpunt, terugdraaitimer en proeven:</p>
          <ul className="list-disc pl-5">
            {chosen.map((i) => (
              <li key={i.key}>{i.title}</li>
            ))}
          </ul>
        </ActionDialog>
      )}
    </div>
  );
}
