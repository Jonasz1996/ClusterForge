"use client";

import Link from "next/link";
import { useEffect, useState, type ReactNode } from "react";
import { QueryState } from "@/components/inventory/bits";
import { Modal } from "@/components/Modal";
import { Alert, Badge, Button, Field, Input, Select } from "@/components/ui";
import {
  OPNSENSE_PROTOCOL,
  opnPlan,
  roleLabel,
  useOpnChange,
  useOpnClusterKey,
  useOpnClusters,
  type OpnCall,
  type OpnCluster,
  type OpnPlan,
} from "@/lib/opnsense";
import type { Job } from "@/lib/proxmox";

// OpnGate kiest het firewallcluster (een HA-paar of een losse firewall) en
// toont de kinderen met dat cluster.
export function OpnGate({ children }: { children: (c: OpnCluster) => ReactNode }) {
  const q = useOpnClusters();
  const list = q.data?.items ?? [];
  const [key, setKey] = useOpnClusterKey(list);
  const c = list.find((x) => x.key === key);
  return (
    <QueryState q={q}>
      {list.length === 0 ? (
        <Alert kind="info">
          Nog geen OPNsense-firewall met een agent. Installeer de agent één keer op elke firewall via{" "}
          <Link href="/nodes" className="underline">
            Nodes → Agent installeren
          </Link>{" "}
          (de regel voor OPNsense, in de shell van menu-optie 8). ClusterForge herkent de firewall vanzelf, maakt zelf een API-gebruiker aan
          en vindt de backup via CARP en pfsync. Staat de agent er al, werk hem dan bij (protocol {OPNSENSE_PROTOCOL} nodig).
        </Alert>
      ) : (
        <div className="space-y-4">
          {list.length > 1 && (
            <div className="flex flex-wrap items-center gap-2 text-sm">
              <label htmlFor="opn-cluster" className="text-slate-500">
                Firewall
              </label>
              <Select id="opn-cluster" value={key} onChange={(e) => setKey(e.target.value)} className="w-auto">
                {list.map((x) => (
                  <option key={x.key} value={x.key}>
                    {x.name}
                    {x.level === "crit" ? " (aandacht)" : ""}
                  </option>
                ))}
              </Select>
            </div>
          )}
          {c && children(c)}
        </div>
      )}
    </QueryState>
  );
}

const levelTone = { ok: "green", warn: "amber", crit: "red" } as const;
const levelText = { ok: "Gezond", warn: "Let op", crit: "Aandacht nodig" };

// ClusterStrip is de kop van een firewallcluster: rol per node en de stand.
export function ClusterStrip({ c }: { c: OpnCluster }) {
  return (
    <div className="flex flex-wrap items-center gap-x-4 gap-y-2 rounded-lg border border-slate-200 bg-white px-4 py-3 text-sm dark:border-slate-800 dark:bg-slate-900">
      <span className="font-semibold">{c.name}</span>
      <Badge tone={levelTone[c.level]}>{levelText[c.level]}</Badge>
      {c.environment && <Badge tone={c.environment === "prod" ? "red" : "slate"}>{c.environment}</Badge>}
      {c.nodes.map((n) => (
        <span key={n.node_id} className="inline-flex items-center gap-1.5">
          <span className={n.online ? "" : "text-slate-400 line-through"}>{n.hostname}</span>
          <Badge tone={n.role === "master" ? "green" : n.role === "backup" ? "blue" : n.role === "mixed" ? "red" : "slate"}>
            {roleLabel[n.role]}
          </Badge>
        </span>
      ))}
      <span className="text-slate-500">
        {c.nodes[0]?.facts.version ? `OPNsense ${c.nodes[0].facts.version}` : ""}
        {c.ha ? " · HA met CARP en pfsync" : " · losse firewall"}
      </span>
    </div>
  );
}

export function JobBanner({ result }: { result: { kind: "success"; job: Job } | { kind: "error"; message: string } | null }) {
  if (!result) return null;
  return result.kind === "success" ? (
    <Alert kind="success">
      Taak gestart: {result.job.title}.{" "}
      <Link href={`/taken/detail?id=${result.job.id}`} className="underline">
        Volgen
      </Link>
    </Alert>
  ) : (
    <Alert kind="error">{result.message}</Alert>
  );
}

// ChangeDialog toont wat er gaat gebeuren en start de wijziging veilig:
// voorcontrole, terugdraaitimer, proeven, bevestigen en sync.
export function ChangeDialog({
  nodeId,
  calls,
  before,
  onClose,
  onDone,
}: {
  nodeId: string;
  calls: OpnCall[];
  // before toont oud → nieuw per veld.
  before?: ReactNode;
  onClose: () => void;
  onDone: (job: Job) => void;
}) {
  const [plan, setPlan] = useState<OpnPlan | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [within, setWithin] = useState("120");
  const [typed, setTyped] = useState("");
  const [code, setCode] = useState("");
  const change = useOpnChange(nodeId);
  useEffect(() => {
    let live = true;
    opnPlan(nodeId, calls)
      .then((p) => live && setPlan(p))
      .catch((e) => live && setError(e instanceof Error ? e.message : "Voorcontrole mislukt"));
    return () => {
      live = false;
    };
  }, [nodeId, calls]);
  const start = async () => {
    setError(null);
    try {
      const auth = plan?.prod ? (/^\d{6}$/.test(code) ? { totp_code: code, password: code } : { password: code }) : {};
      const job = await change.mutateAsync({
        calls,
        confirm_within: Number(within),
        confirm: typed || undefined,
        ...auth,
      });
      onDone(job);
    } catch (e) {
      setError(e instanceof Error ? e.message : "Starten mislukt");
    }
  };
  const ready = plan?.ok && (!plan.confirm || typed.trim() === plan.confirm) && (!plan.prod || code !== "");
  return (
    <Modal title="Wijziging op de firewall" onClose={onClose} wide>
      <div className="space-y-4 text-sm">
        {error && <Alert>{error}</Alert>}
        {!plan && !error && <p className="text-slate-500">Voorcontrole…</p>}
        {plan && (
          <>
            <div>
              <p className="font-medium">Wat er verandert</p>
              <ul className="mt-1 list-disc pl-5">
                {plan.titles.map((t, i) => (
                  <li key={i}>{t}</li>
                ))}
              </ul>
              {before}
              {plan.apply && plan.apply.length > 0 && (
                <p className="mt-2 text-slate-500">Daarna opnieuw geladen: {plan.apply.join(", ")}</p>
              )}
            </div>
            {!plan.ok ? (
              <Alert>Kan nu niet: {plan.why}</Alert>
            ) : (
              <ol className="list-decimal space-y-1 pl-5 text-slate-600 dark:text-slate-300">
                <li>
                  Voorcontrole op {plan.cluster} en een herstelpunt van config.xml op {plan.master}.
                </li>
                <li>
                  Toepassen op {plan.master}; de firewall zet zelf de oude configuratie terug als ClusterForge niet binnen de tijd
                  bevestigt.
                </li>
                <li>
                  Proeven: de webinterface en de CARP-VIP&apos;s antwoorden nog, CARP wisselt niet, geen gateway valt weg, de agents
                  antwoorden.
                </li>
                <li>
                  {plan.backups.length > 0
                    ? `Bevestigen en synchroniseren naar ${plan.backups.join(", ")}; die moet de nieuwe configuratie krijgen.`
                    : "Bevestigen."}
                </li>
              </ol>
            )}
            {plan.level === "access" && (
              <Alert>
                Deze wijziging kan de toegang tot de firewall raken. Als een proef faalt, draait de firewall zelf terug, maar je verliest
                misschien even de verbinding.
              </Alert>
            )}
            <div className="grid gap-4 sm:grid-cols-2">
              <Field label="Terugdraaien zonder bevestiging na" htmlFor="opn-within">
                <Select id="opn-within" value={within} onChange={(e) => setWithin(e.target.value)}>
                  <option value="60">1 minuut</option>
                  <option value="120">2 minuten</option>
                  <option value="300">5 minuten</option>
                  <option value="600">10 minuten</option>
                </Select>
              </Field>
              {plan.confirm && (
                <Field label={`Tik ${plan.confirm} over`} htmlFor="opn-typed">
                  <Input id="opn-typed" value={typed} onChange={(e) => setTyped(e.target.value)} autoComplete="off" />
                </Field>
              )}
              {plan.prod && (
                <Field
                  label="Code van je tweestapsverificatie (of wachtwoord)"
                  htmlFor="opn-code"
                  hint="Deze firewall zit in een prodcluster"
                >
                  <Input
                    id="opn-code"
                    type="password"
                    value={code}
                    onChange={(e) => setCode(e.target.value)}
                    autoComplete="one-time-code"
                  />
                </Field>
              )}
            </div>
          </>
        )}
        <div className="flex gap-2">
          <Button onClick={start} disabled={!ready || change.isPending}>
            {change.isPending ? "Bezig…" : "Veilig toepassen"}
          </Button>
          <Button variant="secondary" onClick={onClose}>
            Annuleren
          </Button>
        </div>
      </div>
    </Modal>
  );
}

// ago zegt hoe lang geleden een tijd (in seconden) was.
export function ago(t: number) {
  const s = Math.max(0, Date.now() / 1000 - t);
  if (s < 90) return "zojuist";
  if (s < 5400) return `${Math.round(s / 60)} min geleden`;
  if (s < 129600) return `${Math.round(s / 3600)} u geleden`;
  return `${Math.round(s / 86400)} d geleden`;
}
