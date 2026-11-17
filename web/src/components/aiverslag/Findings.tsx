"use client";

import { useMemo, useState } from "react";
import { useAIVRunner, webURL } from "@/components/aiverslag/bits";
import { Empty, QueryState } from "@/components/inventory/bits";
import { Alert, Badge, Button, Input, Select } from "@/components/ui";
import { useMe } from "@/lib/auth";
import {
  sevTone,
  useAIVGet,
  type AIVInstance,
  type AIVItem,
} from "@/lib/aiverslag";

// Fase 38 M2: alle bevindingen van AI-verslag per categorie, met dezelfde
// knoppen als in AI-verslag zelf: onderzoeken, goedkeuren (is normaal),
// verwijderen en een voorgestelde fix uitvoeren.

type RawCat = {
  id: string;
  label: string;
  model?: string | null;
  err?: string;
  items: Omit<AIVItem, "cat">[];
};
type RawState = {
  running: boolean;
  last?: string;
  sam?: string;
  busy: string[];
  cats: RawCat[];
};

type FixInfo = {
  titel: string;
  soort: string;
  waarom: string;
  curl?: string;
  call?: string;
  note?: string;
  can_run: boolean;
};

const sevOrder: Record<string, number> = { hoog: 3, midden: 2, laag: 1 };

function when(v: unknown) {
  if (typeof v === "number" && v > 0)
    return new Date(v > 1e12 ? v : v * 1000).toLocaleString("nl-BE", {
      dateStyle: "short",
      timeStyle: "short",
    });
  if (typeof v === "string") return v;
  return "–";
}

export function FindingsView({ v }: { v: AIVInstance }) {
  const q = useAIVGet<RawState>(v.node_id, "/api/state", undefined, {
    refetch: 20_000,
  });
  const me = useMe();
  const admin = me.data?.user.role === "admin";
  const totp = !!me.data?.user.totp_enabled;
  const runner = useAIVRunner(v.node_id);
  const [cat, setCat] = useState("");
  const [sev, setSev] = useState("midden");
  const [text, setText] = useState("");
  const [open, setOpen] = useState<string | null>(null);

  const items = useMemo(() => {
    const all: AIVItem[] = [];
    for (const c of q.data?.cats ?? []) {
      if (cat && c.id !== cat) continue;
      for (const it of c.items) all.push({ ...it, cat: c.id });
    }
    const t = text.trim().toLowerCase();
    return all
      .filter((it) => (sevOrder[it.sev] ?? 1) >= (sevOrder[sev] ?? 1))
      .filter(
        (it) =>
          !t ||
          `${it.titel} ${it.uitleg} ${it.actie}`.toLowerCase().includes(t),
      )
      .sort(
        (a, b) =>
          (sevOrder[b.sev] ?? 0) - (sevOrder[a.sev] ?? 0) ||
          Number(b.actie_nodig) - Number(a.actie_nodig),
      );
  }, [q.data, cat, sev, text]);
  const label = Object.fromEntries(
    (q.data?.cats ?? []).map((c) => [c.id, c.label]),
  );
  const busy = new Set(q.data?.busy ?? []);

  return (
    <QueryState q={q}>
      <div className="space-y-4">
        {runner.banner}
        <div className="flex flex-wrap items-end gap-3 text-sm">
          <label className="space-y-1">
            <span className="block text-xs text-slate-500">Categorie</span>
            <Select
              value={cat}
              onChange={(e) => setCat(e.target.value)}
              className="w-auto"
            >
              <option value="">Alle</option>
              {(q.data?.cats ?? []).map((c) => (
                <option key={c.id} value={c.id}>
                  {c.label} ({c.items.length})
                </option>
              ))}
            </Select>
          </label>
          <label className="space-y-1">
            <span className="block text-xs text-slate-500">Ernst</span>
            <Select
              value={sev}
              onChange={(e) => setSev(e.target.value)}
              className="w-auto"
            >
              <option value="hoog">Alleen hoog</option>
              <option value="midden">Hoog en midden</option>
              <option value="laag">Alles</option>
            </Select>
          </label>
          <label className="min-w-48 flex-1 space-y-1">
            <span className="block text-xs text-slate-500">Zoeken</span>
            <Input
              value={text}
              onChange={(e) => setText(e.target.value)}
              placeholder="IP, toestel, domein …"
            />
          </label>
          <span className="text-xs text-slate-500">
            {items.length} bevinding(en)
            {q.data?.running ? " · analyse loopt" : ""}
          </span>
        </div>
        {items.length === 0 ? (
          <Empty>Geen bevindingen met deze filter.</Empty>
        ) : (
          <ul className="divide-y divide-slate-100 rounded-lg border border-slate-200 bg-white dark:divide-slate-800 dark:border-slate-800 dark:bg-slate-900">
            {items.map((it) => {
              const key = `${it.cat}:${it.id}`;
              return (
                <li key={key} className="p-4">
                  <button
                    type="button"
                    className="flex w-full items-start gap-2 text-left"
                    onClick={() => setOpen(open === key ? null : key)}
                    aria-expanded={open === key}
                  >
                    <Badge tone={sevTone[it.sev] ?? "slate"}>{it.sev}</Badge>
                    <span className="min-w-0 flex-1">
                      <span className="font-medium">{it.titel}</span>
                      <span className="block text-xs text-slate-500">
                        {label[it.cat] ?? it.cat} · {it.status} · {it.count}× ·
                        laatst {when(it.last)}
                        {it.actie_nodig ? " · actie nodig" : ""}
                        {busy.has(key) ? " · wordt onderzocht" : ""}
                      </span>
                    </span>
                  </button>
                  {open === key && (
                    <ItemDetail
                      v={v}
                      it={it}
                      admin={admin}
                      totp={totp}
                      runner={runner}
                      busy={busy.has(key)}
                    />
                  )}
                </li>
              );
            })}
          </ul>
        )}
      </div>
    </QueryState>
  );
}

function ItemDetail({
  v,
  it,
  admin,
  totp,
  runner,
  busy,
}: {
  v: AIVInstance;
  it: AIVItem;
  admin: boolean;
  totp: boolean;
  runner: ReturnType<typeof useAIVRunner>;
  busy: boolean;
}) {
  const [fixOpen, setFixOpen] = useState(false);
  const base = `${it.cat}/${it.id}`;
  return (
    <div className="mt-3 space-y-3 pl-1 text-sm">
      {it.uitleg && <p className="whitespace-pre-line">{it.uitleg}</p>}
      {it.actie && (
        <p>
          <span className="font-medium">Advies: </span>
          {it.actie}
        </p>
      )}
      {it.ond && (
        <div>
          <div className="text-xs font-medium tracking-wide text-slate-500 uppercase">
            Onderzoek door de AI
          </div>
          <p className="mt-1 max-h-72 overflow-y-auto rounded bg-slate-50 p-3 whitespace-pre-line dark:bg-slate-800/60">
            {it.ond}
          </p>
        </div>
      )}
      <p className="text-xs text-slate-500">
        Eerst gezien {when(it.first)} · laatst {when(it.last)} · {it.ev}{" "}
        gebeurtenis(sen) in de laatste ronde
        {it.vervalt ? ` · verdwijnt vanzelf ${when(it.vervalt)}` : ""}
      </p>
      {admin && (
        <div className="flex flex-wrap gap-2">
          <Button
            variant="secondary"
            disabled={runner.pending || busy}
            onClick={() => runner.run({ path: `/api/investigate/${base}` })}
          >
            {busy ? "Wordt onderzocht" : "Laten onderzoeken"}
          </Button>
          <Button
            variant="secondary"
            disabled={runner.pending}
            onClick={() => runner.run({ path: `/api/approve/${base}` })}
          >
            Is normaal
          </Button>
          <Button
            variant="secondary-danger"
            disabled={runner.pending}
            onClick={() => runner.run({ path: `/api/delete/${base}` })}
          >
            Verwijderen
          </Button>
          {it.fix && (
            <Button variant="secondary" onClick={() => setFixOpen((x) => !x)}>
              Fix …
            </Button>
          )}
          {webURL(v) && (
            <a
              href={webURL(v)}
              target="_blank"
              rel="noreferrer"
              className="self-center text-xs underline"
            >
              Openen in AI-verslag
            </a>
          )}
        </div>
      )}
      {fixOpen && <FixPanel v={v} base={base} totp={totp} runner={runner} />}
    </div>
  );
}

function FixPanel({
  v,
  base,
  totp,
  runner,
}: {
  v: AIVInstance;
  base: string;
  totp: boolean;
  runner: ReturnType<typeof useAIVRunner>;
}) {
  const q = useAIVGet<FixInfo>(v.node_id, `/api/fix/${base}`, undefined, {
    refetch: false,
  });
  const [confirm, setConfirm] = useState("");
  return (
    <div className="rounded-md border border-amber-300 p-3 dark:border-amber-900">
      <QueryState q={q}>
        {q.data && (
          <div className="space-y-2">
            <div className="font-medium">
              {q.data.soort}: {q.data.titel}
            </div>
            {q.data.waarom && <p>{q.data.waarom}</p>}
            {q.data.note && (
              <p className="text-xs text-slate-500">{q.data.note}</p>
            )}
            {q.data.call && (
              <code className="block overflow-x-auto rounded bg-slate-50 p-2 text-xs dark:bg-slate-800">
                {q.data.call}
              </code>
            )}
            {!q.data.can_run ? (
              <Alert kind="info">
                AI-verslag kan deze fix niet zelf uitvoeren (geen schrijfsleutel
                voor OPNsense of een handmatige stap).
              </Alert>
            ) : !totp ? (
              <Alert kind="info">
                Een fix verandert de firewall; zet eerst tweestapsverificatie
                aan bij Instellingen.
              </Alert>
            ) : (
              <div className="flex flex-wrap items-end gap-2">
                <label className="space-y-1">
                  <span className="block text-xs text-slate-500">
                    Tik fix over om op de firewall uit te voeren
                  </span>
                  <Input
                    value={confirm}
                    onChange={(e) => setConfirm(e.target.value)}
                    className="w-32"
                  />
                </label>
                <Button
                  variant="danger"
                  disabled={confirm.trim() !== "fix" || runner.pending}
                  onClick={() =>
                    runner.run({
                      path: `/api/fix/${base}/run`,
                      confirm: confirm.trim(),
                    })
                  }
                >
                  Fix uitvoeren
                </Button>
              </div>
            )}
          </div>
        )}
      </QueryState>
    </div>
  );
}
