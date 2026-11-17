"use client";

import Link from "next/link";
import { Suspense, useState } from "react";
import { tableClass, tdClass, thClass } from "@/components/inventory/bits";
import { ActionDialog, ago, ClusterStrip, JobBanner, OpnGate } from "@/components/opnsense/bits";
import { Tile as Win, TileGrid } from "@/components/Tiles";
import { Alert, Badge, Button, Card, Field, Input, PageHeader, Select } from "@/components/ui";
import { useMe } from "@/lib/auth";
import {
  authBody,
  readNode,
  useOpnAuto,
  useOpnAutoApply,
  useOpnAutoSave,
  type OpnAutoItem,
  type OpnAutoMode,
  type OpnAutoSettings,
  type OpnCluster,
} from "@/lib/opnsense";
import type { Job } from "@/lib/proxmox";

export default function OpnAutoPage() {
  return (
    <div className="space-y-6">
      <PageHeader
        title="Automatisch"
        description="ClusterForge kent elke node, elk cluster en elk VIP en houdt daarmee aliassen, DNS-overrides en DHCP-reservaties op de firewall bij. Per soort kies je: uit, voorbeeld (alleen voorstellen) of beheerd (ClusterForge past zelf toe, elke 10 minuten, met herstelpunt en terugdraaitimer). ClusterForge raakt alleen aliassen die met cf_ beginnen en overrides en reservaties met een omschrijving die met cf: begint."
      />
      <Suspense>
        <OpnGate>{(c) => <Auto key={c.key} c={c} />}</OpnGate>
      </Suspense>
    </div>
  );
}

type Result = { kind: "success"; job: Job } | { kind: "error"; message: string } | null;

const kindLabel: Record<OpnAutoItem["kind"], string> = {
  alias: "Alias",
  dns: "DNS",
  dhcp: "DHCP",
};
const opLabel: Record<OpnAutoItem["op"], { text: string; tone: "green" | "blue" | "red" | "amber" }> = {
  add: { text: "nieuw", tone: "green" },
  set: { text: "bijwerken", tone: "blue" },
  del: { text: "weg", tone: "red" },
  conflict: { text: "conflict", tone: "amber" },
};

function Auto({ c }: { c: OpnCluster }) {
  const node = readNode(c);
  const me = useMe();
  const admin = me.data?.user.role === "admin";
  const prod = c.environment === "prod";
  const a = useOpnAuto(node?.node_id);
  const [result, setResult] = useState<Result>(null);
  const [picked, setPicked] = useState<string[]>([]);
  const [apply, setApply] = useState<string[] | null>(null);
  const applyM = useOpnAutoApply(node?.node_id ?? "");
  const save = useOpnAutoSave(node?.node_id ?? "");
  const items = a.data?.items ?? [];
  const actionable = items.filter((i) => i.op !== "conflict");

  const toggleIgnore = async (key: string) => {
    if (!a.data) return;
    const s = a.data.settings;
    const ignored = s.ignored.includes(key) ? s.ignored.filter((k) => k !== key) : [...s.ignored, key];
    try {
      await save.mutateAsync({ ...s, ignored });
    } catch (e) {
      setResult({
        kind: "error",
        message: e instanceof Error ? e.message : "Bewaren mislukt",
      });
    }
  };

  return (
    <div className="space-y-4">
      <ClusterStrip c={c} />
      <JobBanner result={result} />
      {a.isError && <Alert>{(a.error as Error).message}</Alert>}
      {a.data?.last_error && <Alert>Laatste automatische ronde: {a.data.last_error}</Alert>}
      {a.data?.notes.map((n) => (
        <Alert key={n} kind="info">
          {n}
        </Alert>
      ))}
      <TileGrid>
        <Win tall>
          {a.data ? (
            <Settings
              key={JSON.stringify(a.data.settings)}
              s={a.data.settings}
              domain={a.data.domain}
              prod={prod}
              admin={admin}
              id={node?.node_id ?? ""}
            />
          ) : (
            <Card title="Stand">
              <p className="text-sm text-slate-500">Laden…</p>
            </Card>
          )}
        </Win>
        <Win>
          <Card title="Wat al klopt">
            {!a.data ? (
              <p className="text-sm text-slate-500">Laden…</p>
            ) : (
              <ul className="space-y-1 text-sm">
                {(["alias", "dns", "dhcp"] as const).map((k) => (
                  <li key={k} className="flex justify-between">
                    <span>{kindLabel[k]}</span>
                    <span className="text-slate-500">
                      {a.data.in_order[k] ?? 0} in orde · {items.filter((i) => i.kind === k && !i.ignored).length} voorstellen
                    </span>
                  </li>
                ))}
                <li className="pt-2 text-xs text-slate-500">
                  Gelezen op {a.data.firewall || "–"}
                  {a.data.last_run && ` · laatste automatische ronde ${ago(Math.floor(new Date(a.data.last_run).getTime() / 1000))}`}
                  {a.data.last_job && (
                    <>
                      {" · "}
                      <Link className="underline" href={`/taken/detail?id=${a.data.last_job}`}>
                        laatste wijziging
                      </Link>
                    </>
                  )}
                </li>
              </ul>
            )}
          </Card>
        </Win>
        <Win wide tall>
          <Card title={`Voorstellen${a.data ? ` · ${items.length}` : ""}`}>
            {!a.data ? (
              <p className="text-sm text-slate-500">Laden…</p>
            ) : items.length === 0 ? (
              <p className="text-sm text-slate-500">Alles klopt. Er is niets voor te stellen.</p>
            ) : (
              <div className="space-y-3">
                {admin && (
                  <div className="flex flex-wrap items-center gap-2 text-sm">
                    <Button disabled={a.data.pending === 0} onClick={() => setApply([])}>
                      Alles toepassen ({a.data.pending})
                    </Button>
                    <Button variant="secondary" disabled={picked.length === 0} onClick={() => setApply(picked)}>
                      Gekozen toepassen ({picked.length})
                    </Button>
                    <span className="text-slate-500">
                      Genegeerde voorstellen en het weghalen van aliassen gebeuren alleen als je ze kiest.
                    </span>
                  </div>
                )}
                <div className="max-h-[36rem] overflow-auto">
                  <table className={tableClass}>
                    <thead>
                      <tr>
                        {admin && <th className={thClass} />}
                        <th className={thClass}>Soort</th>
                        <th className={thClass}>Wat</th>
                        <th className={thClass}>Uitleg</th>
                        {admin && <th className={thClass} />}
                      </tr>
                    </thead>
                    <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
                      {items.map((it) => (
                        <tr key={it.key} className={it.ignored ? "opacity-50" : ""}>
                          {admin && (
                            <td className={tdClass}>
                              {it.op !== "conflict" && (
                                <input
                                  type="checkbox"
                                  aria-label={`Kies ${it.title}`}
                                  checked={picked.includes(it.key)}
                                  onChange={(e) => setPicked((p) => (e.target.checked ? [...p, it.key] : p.filter((k) => k !== it.key)))}
                                />
                              )}
                            </td>
                          )}
                          <td className={`${tdClass} whitespace-nowrap`}>
                            {kindLabel[it.kind]} <Badge tone={opLabel[it.op].tone}>{opLabel[it.op].text}</Badge>
                          </td>
                          <td className={tdClass}>
                            {it.title}
                            {it.manual && <span className="ml-1 text-xs text-slate-500">(alleen met de hand)</span>}
                          </td>
                          <td className={`${tdClass} text-xs text-slate-500`}>{it.detail}</td>
                          {admin && (
                            <td className={`${tdClass} text-right`}>
                              <button type="button" className="text-xs text-slate-500 underline" onClick={() => toggleIgnore(it.key)}>
                                {it.ignored ? "niet meer negeren" : "negeren"}
                              </button>
                            </td>
                          )}
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
                {actionable.length === 0 && <p className="text-sm text-slate-500">Alleen conflicten: die past ClusterForge nooit aan.</p>}
              </div>
            )}
          </Card>
        </Win>
      </TileGrid>
      {apply !== null && a.data && (
        <ActionDialog
          title="Voorstellen toepassen"
          prod={prod}
          label="Toepassen"
          start={(b) => applyM.mutateAsync({ keys: apply, ...b })}
          onClose={() => setApply(null)}
          onDone={(j) => {
            setApply(null);
            setPicked([]);
            setResult({ kind: "success", job: j });
          }}
        >
          <p>
            {apply.length > 0 ? `${apply.length} gekozen voorstellen` : `${a.data.pending} voorstellen`} gaan als één wijziging naar{" "}
            {a.data.firewall}: met herstelpunt, terugdraaitimer en proeven, daarna de sync naar de backup. Meer dan 50 tegelijk gaat in de
            volgende ronde.
          </p>
        </ActionDialog>
      )}
    </div>
  );
}

function Settings({ s, domain, prod, admin, id }: { s: OpnAutoSettings; domain: string; prod: boolean; admin: boolean; id: string }) {
  const [v, setV] = useState(s);
  const [code, setCode] = useState("");
  const [msg, setMsg] = useState<{ kind: "ok" | "error"; text: string } | null>(null);
  const save = useOpnAutoSave(id);
  const toManaged = (["aliases", "dns", "dhcp"] as const).some((k) => v[k] === "beheerd" && s[k] !== "beheerd");
  const needCode = prod && toManaged;
  const changed = JSON.stringify(v) !== JSON.stringify(s);
  const row = (k: "aliases" | "dns" | "dhcp", label: string) => (
    <Field label={label} htmlFor={`auto-${k}`}>
      <Select id={`auto-${k}`} value={v[k]} disabled={!admin} onChange={(e) => setV({ ...v, [k]: e.target.value as OpnAutoMode })}>
        <option value="uit">Uit</option>
        <option value="voorbeeld">Voorbeeld (alleen voorstellen)</option>
        <option value="beheerd">Beheerd (zelf toepassen)</option>
      </Select>
    </Field>
  );
  return (
    <Card title="Stand">
      <form
        className="space-y-3 text-sm"
        onSubmit={async (e) => {
          e.preventDefault();
          setMsg(null);
          try {
            await save.mutateAsync({
              ...v,
              ...(needCode ? authBody(code) : {}),
            });
            setCode("");
            setMsg({ kind: "ok", text: "Bewaard." });
          } catch (err) {
            setMsg({
              kind: "error",
              text: err instanceof Error ? err.message : "Bewaren mislukt",
            });
          }
        }}
      >
        {msg && <Alert kind={msg.kind === "ok" ? "success" : "error"}>{msg.text}</Alert>}
        {row("aliases", "Aliassen (cf_nodes, cf_vips, cf_<cluster>)")}
        {row("dns", "DNS-overrides in Unbound (node en VIP)")}
        {row("dhcp", "DHCP-reservaties in Kea of Dnsmasq")}
        <Field label="Domein van de DNS-overrides" htmlFor="auto-domain">
          <Input
            id="auto-domain"
            value={v.domain}
            placeholder={domain}
            disabled={!admin}
            onChange={(e) => setV({ ...v, domain: e.target.value })}
          />
        </Field>
        {needCode && (
          <Field label="Code van je tweestapsverificatie (of wachtwoord)" htmlFor="auto-code">
            <Input id="auto-code" type="password" value={code} onChange={(e) => setCode(e.target.value)} autoComplete="one-time-code" />
          </Field>
        )}
        {admin && (
          <div className="flex justify-end">
            <Button type="submit" disabled={!changed || save.isPending || (needCode && code === "")}>
              Bewaren
            </Button>
          </div>
        )}
      </form>
    </Card>
  );
}
