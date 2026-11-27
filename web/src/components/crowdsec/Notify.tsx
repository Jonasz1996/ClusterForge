"use client";

import { useState } from "react";
import { Alert, Button, Card, Field, Input, Select } from "@/components/ui";
import { ApiError } from "@/lib/api/client";
import { useCrowdSecConsoleOff, useCrowdSecNotify, useTestCrowdSecNotify, type CrowdSecOverview } from "@/lib/crowdsec";

// Eigen meldingen en de Console loskoppelen (fase 47).

function errText(e: unknown) {
  return e instanceof ApiError ? e.message : "Er ging iets mis.";
}

type Kind = "ntfy" | "discord" | "slack" | "telegram" | "webhook";
type Row = { id?: string; kind: Kind; name: string; url: string; token: string; target?: string };

const kinds: { k: Kind; label: string; url: string; token?: string }[] = [
  { k: "ntfy", label: "ntfy", url: "https://ntfy.sh/mijn-geheim-topic (of je eigen ntfy-server)", token: "Toegangstoken (optioneel)" },
  { k: "discord", label: "Discord", url: "Webhook-URL van het kanaal" },
  { k: "slack", label: "Slack", url: "Incoming-webhook-URL" },
  { k: "telegram", label: "Telegram", url: "Chat-id", token: "Bot-token" },
  { k: "webhook", label: "Eigen webhook (JSON)", url: "http(s)-adres", token: "Bearer-token (optioneel)" },
];

export function NotifyTab({ o, isAdmin }: { o: CrowdSecOverview; isAdmin: boolean }) {
  const n = o.settings.notify;
  const save = useCrowdSecNotify();
  const test = useTestCrowdSecNotify();
  const [rows, setRows] = useState<Row[]>(n.channels.map((c) => ({ id: c.id, kind: c.kind, name: c.name, url: "", token: "", target: c.target })));
  const [f, setF] = useState({
    auto_block: n.auto_block,
    new_alerts: n.new_alerts,
    alert_burst: n.alert_burst,
    node_problems: n.node_problems,
    list_problems: n.list_problems,
    manual_bans: n.manual_bans,
  });
  const upd = (i: number, p: Partial<Row>) => setRows(rows.map((r, j) => (j === i ? { ...r, ...p } : r)));
  const check = (k: keyof typeof f, label: string, hint: string) => (
    <label className="flex items-start gap-2">
      <input type="checkbox" className="mt-1" checked={Boolean(f[k])} disabled={!isAdmin} onChange={(e) => setF({ ...f, [k]: e.target.checked })} />
      <span>
        <span className="font-medium">{label}</span>
        <span className="block text-xs text-slate-500">{hint}</span>
      </span>
    </label>
  );
  return (
    <div className="space-y-6">
      <Card title="Meldingen">
        <div className="max-w-3xl space-y-4 text-sm">
          <p className="text-xs text-slate-500">
            ClusterForge stuurt zelf meldingen, zonder de CrowdSec Console en zonder limiet. Adressen en tokens staan versleuteld en zijn hier niet meer te lezen;
            laat ze leeg om ze te houden.
          </p>
          {!o.settings.console_available && <Alert>De server heeft geen masterkey (CF_MASTER_KEY), dus hij kan kanalen niet bewaren.</Alert>}
          {rows.map((r, i) => {
            const k = kinds.find((x) => x.k === r.kind)!;
            return (
              <div key={r.id ?? `nieuw-${i}`} className="flex flex-wrap items-end gap-2 rounded-md border border-slate-200 p-3 dark:border-slate-700">
                <Field label="Soort" htmlFor={`nf-kind-${i}`}>
                  <Select id={`nf-kind-${i}`} value={r.kind} disabled={!isAdmin || Boolean(r.id)} onChange={(e) => upd(i, { kind: e.target.value as Kind })}>
                    {kinds.map((x) => (
                      <option key={x.k} value={x.k}>
                        {x.label}
                      </option>
                    ))}
                  </Select>
                </Field>
                <Field label="Naam" htmlFor={`nf-name-${i}`}>
                  <Input id={`nf-name-${i}`} className="w-36" value={r.name} disabled={!isAdmin} onChange={(e) => upd(i, { name: e.target.value })} />
                </Field>
                <Field label={r.kind === "telegram" ? "Chat-id" : "Adres"} htmlFor={`nf-url-${i}`}>
                  <Input
                    id={`nf-url-${i}`}
                    className="w-80"
                    type="password"
                    autoComplete="off"
                    placeholder={r.target ? `ingesteld: ${r.target}` : k.url}
                    value={r.url}
                    disabled={!isAdmin}
                    onChange={(e) => upd(i, { url: e.target.value })}
                  />
                </Field>
                {k.token && (
                  <Field label={k.token} htmlFor={`nf-token-${i}`}>
                    <Input
                      id={`nf-token-${i}`}
                      className="w-56"
                      type="password"
                      autoComplete="off"
                      placeholder={r.id ? "leeg = houden" : ""}
                      value={r.token}
                      disabled={!isAdmin}
                      onChange={(e) => upd(i, { token: e.target.value })}
                    />
                  </Field>
                )}
                {isAdmin && (
                  <Button variant="ghost" onClick={() => setRows(rows.filter((_, j) => j !== i))}>
                    Weghalen
                  </Button>
                )}
              </div>
            );
          })}
          {isAdmin && (
            <Button variant="secondary" onClick={() => setRows([...rows, { kind: "ntfy", name: "", url: "", token: "" }])}>
              Kanaal toevoegen
            </Button>
          )}
          <div className="space-y-3 border-t border-slate-200 pt-4 dark:border-slate-700">
            {check("auto_block", "Automatische blocks", "Elk IP dat de regels over alle nodes heen blokkeren.")}
            {check("node_problems", "CrowdSec of bouncer staat stil", "Hoogstens om de 6 uur per node.")}
            {check("list_problems", "Blocklist ophalen of zetten mislukt", "Hoogstens om de 6 uur per lijst of node.")}
            {check("new_alerts", "Elke nieuwe alert", "Een samenvatting per scenario, hoogstens om de 5 minuten.")}
            {check("manual_bans", "Blokkeren en deblokkeren met de hand", "Ook wat je zelf doet, als logboek in je telefoon.")}
            <div className="flex flex-wrap items-center gap-2">
              Veel alerts: melden vanaf
              <Input
                type="number"
                className="w-24"
                value={f.alert_burst}
                disabled={!isAdmin}
                onChange={(e) => setF({ ...f, alert_burst: Math.max(0, Math.round(Number(e.target.value) || 0)) })}
              />
              alerts in 10 minuten (0 is uit; hoogstens één melding per uur).
            </div>
          </div>
          {save.isError && <Alert>{errText(save.error)}</Alert>}
          {save.isSuccess && <Alert kind="success">Bewaard.</Alert>}
          {test.isError && <Alert>{errText(test.error)}</Alert>}
          {test.data && (
            <Alert kind={Object.values(test.data).every((v) => !v) ? "success" : "error"}>
              {Object.entries(test.data).map(([name, e]) => (
                <span key={name} className="block">
                  {name}: {e || "aangekomen"}
                </span>
              ))}
            </Alert>
          )}
          {isAdmin && (
            <div className="flex gap-2">
              <Button
                disabled={save.isPending}
                onClick={() =>
                  save.mutate(
                    {
                      ...f,
                      channels: rows.map((r) => ({ id: r.id, kind: r.kind, name: r.name || undefined, url: r.url || undefined, token: r.token || undefined })),
                    },
                    {
                      onSuccess: (s) =>
                        setRows(s.notify.channels.map((c) => ({ id: c.id, kind: c.kind, name: c.name, url: "", token: "", target: c.target }))),
                    },
                  )
                }
              >
                Bewaren
              </Button>
              <Button variant="secondary" disabled={test.isPending || n.channels.length === 0} onClick={() => test.mutate()}>
                Testmelding sturen
              </Button>
            </div>
          )}
        </div>
      </Card>
    </div>
  );
}

/** Console loskoppelen: bovenaan de Console-instellingen. */
export function ConsoleOff({ o, isAdmin }: { o: CrowdSecOverview; isAdmin: boolean }) {
  const off = useCrowdSecConsoleOff();
  const managed = o.nodes.filter((n) => n.managed);
  const done = managed.filter((n) => n.console_off_done).length;
  const old = managed.filter((n) => n.protocol < 57).length;
  if (o.settings.console_off) {
    return (
      <Alert kind="success">
        De CrowdSec Console is losgekoppeld: ClusterForge is de centrale server. Uitgezet op {done} van {managed.length} beheerde nodes
        {old > 0 && `; ${old} node(s) hebben eerst een nieuwere agent nodig (Nodes → Agents bijwerken)`}. Detectie, bans en de community-blocklist werken
        gewoon lokaal.
        {isAdmin && (
          <button className="ml-2 font-medium underline" disabled={off.isPending} onClick={() => off.mutate(false)}>
            Console weer toelaten
          </button>
        )}
      </Alert>
    );
  }
  return (
    <div className="space-y-2">
      <Alert kind="info">
        De gratis Console toont 500 alerts per maand; daarna verbergt ze nieuwe alerts tot de 1e. ClusterForge haalt de alerts rechtstreeks van de nodes, dus je
        hebt de Console niet nodig: blocklists, automatisch blokkeren en meldingen zitten in ClusterForge.
      </Alert>
      {isAdmin && (
        <Button variant="secondary-danger" disabled={off.isPending} onClick={() => off.mutate(true)}>
          Console loskoppelen
        </Button>
      )}
      {off.isError && <Alert>{errText(off.error)}</Alert>}
    </div>
  );
}
