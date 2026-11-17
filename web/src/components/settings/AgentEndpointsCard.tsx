"use client";

import Link from "next/link";
import { useMemo, useState } from "react";
import { useProdTotp } from "@/components/ProdConfirm";
import { Alert, Badge, Button, Card, Field, Input } from "@/components/ui";
import {
  applicable,
  endpointStates,
  useAgentEndpoints,
  useApplyAgentEndpoints,
  useSetAgentEndpoints,
  type AgentEndpointAgent,
} from "@/lib/agentEndpoints";
import { ApiError } from "@/lib/api/client";

const sourceLabel = {
  setting: "ingesteld hier",
  advertise: "intern adres uit CF_NATS_ADVERTISE",
  request: "het adres van deze pagina",
} as const;

// AgentEndpointsCard stelt het adres in waarop agents ClusterForge bereiken
// en zet bestaande agents om. Een agent test het nieuwe adres eerst en zet
// zijn oude agent.json terug als hij de server daar niet bereikt.
export function AgentEndpointsCard({ isAdmin }: { isAdmin: boolean }) {
  const q = useAgentEndpoints();
  const save = useSetAgentEndpoints();
  const apply = useApplyAgentEndpoints();
  const totp = useProdTotp();
  const ov = q.data;
  const [server, setServer] = useState("");
  const [nats, setNats] = useState("");
  const [selected, setSelected] = useState<string[]>([]);
  const [withProd, setWithProd] = useState(false);
  const [code, setCode] = useState("");
  const [dirty, setDirty] = useState(false);
  // Zolang er niets getypt is, tonen de velden wat er bewaard is.
  const shownServer = dirty ? server : (ov?.server_setting ?? "");
  const shownNats = dirty ? nats : (ov?.nats_setting ?? "").replace(/^tls:\/\//, "");
  const edit = () => {
    if (!dirty) {
      setServer(shownServer);
      setNats(shownNats);
      setDirty(true);
    }
  };

  const groups = useMemo(() => {
    const m = new Map<string, AgentEndpointAgent[]>();
    for (const a of ov?.agents ?? []) {
      const k = a.cluster_name || "Zonder cluster";
      m.set(k, [...(m.get(k) ?? []), a]);
    }
    return [...m.entries()];
  }, [ov]);

  if (!ov) return null;
  const agents = ov.agents;
  const count = (s: AgentEndpointAgent["state"]) => agents.filter((a) => a.state === s).length;
  const open = agents.filter(applicable);
  const openProd = open.filter((a) => a.cluster_environment === "prod");
  const chosen = agents.filter((a) => selected.includes(a.node_id));
  const needsProd = (selected.length > 0 ? chosen : withProd ? open : []).some((a) => a.cluster_environment === "prod");
  const prodBlocked = needsProd && (!totp || code.length !== 6);

  const run = (body: { node_ids?: string[]; all?: boolean; include_prod?: boolean }) =>
    apply.mutate(needsProd ? { ...body, totp_code: code } : body, {
      onSuccess: () => {
        setSelected([]);
        setCode("");
      },
    });

  return (
    <Card title="Adres voor agents">
      <div className="space-y-4 text-sm">
        <p className="text-slate-600 dark:text-slate-400">
          Op dit adres bereiken de nodes ClusterForge: in het installcommando, bij nieuwe aanmeldingen en voor het downloaden van nieuwe agents.
          Standaard is dat het interne adres (CF_NATS_ADVERTISE), niet de naam achter je reverse proxy. Bestaande agents zet je hieronder om; elke
          agent test het nieuwe adres eerst en houdt zijn oude als hij de server daar niet bereikt.
        </p>
        <div className="grid gap-3 sm:grid-cols-2">
          <Field
            label="Server-URL voor agents"
            htmlFor="agent-server-url"
            hint={`Nu: ${ov.server_url || "het adres van deze pagina"} (${sourceLabel[ov.server_source]}). Leeg is de standaard.`}
          >
            <Input
              id="agent-server-url"
              placeholder={ov.server_default || "http://192.168.113.153:8080"}
              value={shownServer}
              disabled={!isAdmin}
              onChange={(e) => {
                edit();
                setServer(e.target.value);
              }}
            />
          </Field>
          <Field
            label="NATS-adres voor agents"
            htmlFor="agent-nats-url"
            hint={`Nu: ${ov.nats_url.replace(/^tls:\/\//, "") || "onbekend"} (${sourceLabel[ov.nats_source]}). Leeg is de standaard.`}
          >
            <Input
              id="agent-nats-url"
              placeholder={ov.nats_default.replace(/^tls:\/\//, "") || "192.168.113.153:4222"}
              value={shownNats}
              disabled={!isAdmin}
              onChange={(e) => {
                edit();
                setNats(e.target.value);
              }}
            />
          </Field>
        </div>
        {isAdmin && (
          <div className="flex items-center gap-2">
            <Button
              disabled={!dirty || save.isPending}
              onClick={() => save.mutate({ server_url: server.trim(), nats_url: nats.trim() }, { onSuccess: () => setDirty(false) })}
            >
              Bewaren
            </Button>
            {dirty && (
              <Button variant="secondary" onClick={() => setDirty(false)}>
                Annuleren
              </Button>
            )}
          </div>
        )}
        {save.isError && <Alert>{save.error.message}</Alert>}

        <div className="space-y-2 border-t border-slate-200 pt-4 dark:border-slate-800">
          <p className="font-medium">
            Agents: {count("current")} op het juiste adres, {count("differs") + count("failed")} op een ander adres
            {count("manual") > 0 && `, ${count("manual")} eerst bijwerken (protocol ${ov.protocol})`}
            {count("unknown") > 0 && `, ${count("unknown")} nog onbekend`}.
          </p>
          {ov.running.length > 0 && (
            <Alert kind="info">
              Omzetten loopt:{" "}
              {ov.running.map((r, i) => (
                <span key={r.job_id}>
                  {i > 0 && ", "}
                  <Link href={`/taken/detail?id=${r.job_id}`} className="underline">
                    taak
                  </Link>
                </span>
              ))}
              . Per cluster één node tegelijk.
            </Alert>
          )}
          {count("manual") > 0 && (
            <p className="text-slate-500">
              Agents ouder dan protocol {ov.protocol} kennen omzetten nog niet; werk ze eerst bij (Nodes, Agents bijwerken). Daarna melden ze hun
              adres en kun je ze hier omzetten.
            </p>
          )}
          <div className="max-h-96 overflow-auto rounded-md border border-slate-200 dark:border-slate-800">
            <table className="w-full text-left text-xs">
              <thead className="sticky top-0 bg-slate-50 text-slate-500 dark:bg-slate-900">
                <tr>
                  <th className="w-8 p-2" />
                  <th className="p-2">Node</th>
                  <th className="p-2">NATS</th>
                  <th className="hidden p-2 md:table-cell">Server</th>
                  <th className="p-2">Toestand</th>
                </tr>
              </thead>
              <tbody>
                {groups.map(([name, list]) => [
                  <tr key={`g-${name}`} className="bg-slate-50/60 dark:bg-slate-900/60">
                    <td colSpan={5} className="p-2 font-medium">
                      {name}
                      {list[0].cluster_environment === "prod" && (
                        <span className="ml-2">
                          <Badge tone="red">prod</Badge>
                        </span>
                      )}
                    </td>
                  </tr>,
                  ...list.map((a) => (
                    <tr key={a.node_id} className="border-t border-slate-100 align-top dark:border-slate-800">
                      <td className="p-2">
                        <input
                          type="checkbox"
                          aria-label={`${a.hostname} omzetten`}
                          disabled={!isAdmin || !applicable(a)}
                          checked={selected.includes(a.node_id)}
                          onChange={(e) => setSelected((s) => (e.target.checked ? [...s, a.node_id] : s.filter((x) => x !== a.node_id)))}
                        />
                      </td>
                      <td className="p-2">
                        <Link href={`/nodes/detail?id=${a.node_id}`} className="font-medium hover:underline">
                          {a.hostname}
                        </Link>
                        {!a.online && <span className="block text-slate-500">niet verbonden</span>}
                      </td>
                      <td className="p-2 font-mono break-all">{a.nats_url.replace(/^tls:\/\//, "") || "?"}</td>
                      <td className="hidden p-2 font-mono break-all md:table-cell">{a.server_url || "?"}</td>
                      <td className="p-2">
                        <Badge tone={endpointStates[a.state].tone}>{endpointStates[a.state].label}</Badge>
                        {a.why && <span className="mt-1 block text-slate-500">{a.why}</span>}
                      </td>
                    </tr>
                  )),
                ])}
                {agents.length === 0 && (
                  <tr>
                    <td colSpan={5} className="p-3 text-slate-500">
                      Nog geen agents.
                    </td>
                  </tr>
                )}
              </tbody>
            </table>
          </div>
        </div>

        {isAdmin && open.length > 0 && (
          <div className="space-y-3">
            {selected.length === 0 && openProd.length > 0 && (
              <label className="flex items-start gap-2">
                <input type="checkbox" className="mt-0.5" checked={withProd} onChange={(e) => setWithProd(e.target.checked)} />
                <span>
                  Ook productie ({openProd.length} {openProd.length === 1 ? "agent" : "agents"})
                  <span className="block text-slate-500">Vraagt een code uit de app.</span>
                </span>
              </label>
            )}
            {needsProd &&
              (totp ? (
                <Field label="Code uit de app (prod)" htmlFor="agent-endpoints-code">
                  <Input
                    id="agent-endpoints-code"
                    inputMode="numeric"
                    autoComplete="one-time-code"
                    className="w-40 font-mono tracking-widest"
                    maxLength={6}
                    value={code}
                    onChange={(e) => setCode(e.target.value.replace(/\D/g, ""))}
                  />
                </Field>
              ) : (
                <Alert>Op prod kan dit alleen met tweestapsverificatie. Zet die hierboven aan bij Tweestapsverificatie.</Alert>
              ))}
            <div className="flex flex-wrap gap-2">
              {selected.length > 0 ? (
                <Button disabled={apply.isPending || prodBlocked} onClick={() => run({ node_ids: selected })}>
                  {selected.length === 1 ? "Deze agent omzetten" : `${selected.length} agents omzetten`}
                </Button>
              ) : (
                <Button
                  disabled={apply.isPending || prodBlocked || (!withProd && open.length === openProd.length)}
                  onClick={() => run({ all: true, include_prod: withProd })}
                >
                  Alle afwijkende agents omzetten
                </Button>
              )}
            </div>
            <p className="text-xs text-slate-500">
              Per cluster één node tegelijk. De agent herstart, de services op de node niet. Bereikt een agent de server niet binnen 2 minuten op het
              nieuwe adres, dan zet de node zijn oude agent.json terug en stopt de taak van dat cluster; de andere nodes blijven op hun oude adres.
            </p>
          </div>
        )}
        {apply.isError && (
          <Alert>
            {apply.error instanceof ApiError && apply.error.code === "invalid_totp" ? "Deze code klopt niet of is al gebruikt." : apply.error.message}
          </Alert>
        )}
        {apply.data && (
          <Alert kind="success">
            {apply.data.jobs.length === 1 ? "Taak gestart: " : `${apply.data.jobs.length} taken gestart: `}
            {apply.data.jobs.map((j, i) => (
              <span key={j.id}>
                {i > 0 && ", "}
                <Link href={`/taken/detail?id=${j.id}`} className="underline">
                  {j.title}
                </Link>
              </span>
            ))}
            .{apply.data.skipped.length > 0 && <span className="block">Overgeslagen: {apply.data.skipped.join("; ")}</span>}
          </Alert>
        )}
      </div>
    </Card>
  );
}
