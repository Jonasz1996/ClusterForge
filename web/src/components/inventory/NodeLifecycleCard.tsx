"use client";

import Link from "next/link";
import { useState } from "react";
import { NodeImpact } from "@/components/deps/ImpactList";
import { Modal } from "@/components/Modal";
import { Alert, Button, Card, Field, Input } from "@/components/ui";
import { ApiError } from "@/lib/api/client";
import { useCluster, type Node, type NodeRuntime } from "@/lib/inventory";
import { useNodeAction, type NodeAction } from "@/lib/lifecycle";
import { jobActive, type Job } from "@/lib/proxmox";

type Dialog = Exclude<NodeAction, "refresh_facts">;

const lifecycleText: Record<string, string> = {
  active: "Actief: deze node telt mee voor de status van het cluster.",
  maintenance: "In onderhoud: deze node telt niet mee voor het cluster. Keepalived staat uit tot je het onderhoud beëindigt.",
  draining: "De VIP's worden naar een andere node verhuisd.",
  provisioning: "Wordt opgezet: deze node telt nog niet mee voor het cluster.",
  decommissioned: "Uit dienst.",
};

// NodeLifecycleCard toont of een node actief of in onderhoud is, met de
// knoppen om hem in onderhoud te zetten, te herstarten of af te sluiten.
export function NodeLifecycleCard({
  node,
  runtime,
  jobs,
  isAdmin,
}: {
  node: Node;
  runtime?: NodeRuntime;
  jobs?: Job[];
  isAdmin: boolean;
}) {
  const cluster = useCluster(node.cluster_id ?? "");
  const [dialog, setDialog] = useState<Dialog | null>(null);
  const [started, setStarted] = useState<Job | null>(null);

  const held = (cluster.data?.vips ?? []).filter((v) => v.owner_node_id === node.id).map((v) => v.address);
  const keepalived = runtime?.heartbeat?.services?.keepalived !== undefined;
  const busy = jobs?.find((j) => j.kind === "node.action" && jobActive(j.status));
  // De melding blijft tot de taak in de lijst verschijnt; daarna toont "Bezig" hem.
  const startedSeen = started && jobs?.some((j) => j.id === started.id);
  const a = node.agent;
  const usable = !!a && a.commands && a.connection !== "offline";
  const inMaintenance = node.lifecycle === "maintenance" || node.lifecycle === "draining";

  let agentNote = "";
  if (!a) agentNote = "Zonder agent kun je deze node hier alleen in onderhoud zetten; herstarten en afsluiten gaan via de agent.";
  else if (!a.commands) agentNote = "De agent op deze node is te oud voor herstarten en onderhoud. Installeer hem opnieuw met het installatiescript.";
  else if (a.connection === "offline") agentNote = "De agent is offline, dus herstarten en afsluiten kan nu niet.";

  return (
    <Card title="Beheer">
      <div className="space-y-3 text-sm">
        <p className="text-slate-600 dark:text-slate-400">{lifecycleText[node.lifecycle] ?? node.lifecycle}</p>
        {held.length > 0 && (
          <p>
            Deze node heeft nu {held.length === 1 ? "VIP" : "de VIP's"}{" "}
            <span className="font-mono text-xs">{held.join(", ")}</span>.
          </p>
        )}
        {busy && (
          <Alert kind="info">
            Bezig: {busy.title}.{" "}
            <Link href={`/taken/detail?id=${busy.id}`} className="font-medium underline">
              Volgen
            </Link>
          </Alert>
        )}
        {started && !startedSeen && (
          <Alert kind="success">
            Taak gestart.{" "}
            <Link href={`/taken/detail?id=${started.id}`} className="font-medium underline">
              Bekijken
            </Link>
          </Alert>
        )}
        {isAdmin && agentNote && <p className="text-xs text-slate-500">{agentNote}</p>}
        {isAdmin && node.lifecycle !== "decommissioned" && (
          <div className="flex flex-wrap gap-2">
            {inMaintenance ? (
              <Button variant="secondary" disabled={!!busy || (!!a && !usable)} onClick={() => setDialog("activate")}>
                Onderhoud beëindigen…
              </Button>
            ) : (
              <Button variant="secondary" disabled={!!busy || (!!a && !usable)} onClick={() => setDialog("maintenance")}>
                Onderhoud…
              </Button>
            )}
            <Button variant="secondary" disabled={!!busy || !usable} onClick={() => setDialog("reboot")}>
              Herstarten…
            </Button>
            <Button variant="secondary-danger" disabled={!!busy || !usable} onClick={() => setDialog("shutdown")}>
              Afsluiten…
            </Button>
          </div>
        )}
      </div>
      {dialog && (
        <ActionDialog
          action={dialog}
          node={node}
          held={held}
          keepalived={keepalived}
          onClose={() => setDialog(null)}
          onStarted={(j) => {
            setStarted(j);
            setDialog(null);
          }}
        />
      )}
    </Card>
  );
}

const dialogs: Record<Dialog, { title: (h: string) => string; confirm: string; danger?: boolean; impact?: (h: string) => string }> = {
  maintenance: { title: (h) => `${h} in onderhoud zetten`, confirm: "In onderhoud zetten", impact: (h) => `Wat raakt het onderhoud van ${h}?` },
  activate: { title: (h) => `Onderhoud van ${h} beëindigen`, confirm: "Onderhoud beëindigen" },
  reboot: { title: (h) => `${h} herstarten`, confirm: "Herstarten", danger: true, impact: (h) => `Wat raakt het herstarten van ${h}?` },
  shutdown: { title: (h) => `${h} afsluiten`, confirm: "Afsluiten", danger: true, impact: (h) => `Wat raakt het afsluiten van ${h}?` },
};

function ActionDialog({
  action,
  node,
  held,
  keepalived,
  onClose,
  onStarted,
}: {
  action: Dialog;
  node: Node;
  held: string[];
  keepalived: boolean;
  onClose: () => void;
  onStarted: (j: Job) => void;
}) {
  const run = useNodeAction();
  const [reason, setReason] = useState("");
  const [drain, setDrain] = useState(true);
  const [error, setError] = useState<{ message: string; force: boolean } | null>(null);
  const d = dialogs[action];
  const host = node.hostname;
  const hasAgent = !!node.agent;
  const inMaintenance = node.lifecycle === "maintenance";
  const canDrain = (action === "reboot" || action === "shutdown") && keepalived && !inMaintenance;
  const vips = held.join(", ");

  const submit = async (force: boolean) => {
    setError(null);
    try {
      const j = await run.mutateAsync({
        nodeId: node.id,
        body: { action, reason: reason.trim() || undefined, drain: canDrain ? drain : undefined, force: force || undefined },
      });
      onStarted(j);
    } catch (e) {
      if (e instanceof ApiError) setError({ message: e.message, force: e.code === "needs_force" });
      else setError({ message: "Actie mislukt", force: false });
    }
  };

  let text: string;
  switch (action) {
    case "maintenance":
      text = !hasAgent
        ? `${host} heeft geen agent. ClusterForge zet hem alleen in onderhoud; haal eventuele VIP's zelf weg.`
        : keepalived
          ? `ClusterForge zet keepalived op ${host} uit${held.length ? `, wacht tot ${vips} op een andere node staat` : ""} en zet de node daarna in onderhoud. Keepalived blijft uit, ook na een herstart, tot je het onderhoud beëindigt.`
          : `Er draait geen keepalived op ${host}; ClusterForge zet de node alleen in onderhoud.`;
      break;
    case "activate":
      text = hasAgent
        ? `ClusterForge zet keepalived terug zoals het voor het onderhoud stond en maakt ${host} weer actief. Heeft deze node de hoogste prioriteit, dan komt de VIP terug.`
        : `${host} telt daarna weer mee voor de status van het cluster.`;
      break;
    case "reboot":
      text = `${host} herstart en ClusterForge wacht tot de node terug is${inMaintenance ? "; hij blijft in onderhoud" : " en maakt hem daarna weer actief"}.`;
      break;
    case "shutdown":
      text = `${host} gaat uit en blijft in onderhoud. Start hem later in Proxmox of op de machine zelf en beëindig daarna het onderhoud.`;
      break;
  }

  return (
    <Modal title={d.title(host)} onClose={onClose}>
      <form
        className="space-y-4"
        onSubmit={(e) => {
          e.preventDefault();
          void submit(false);
        }}
      >
        <p className="text-sm">{text}</p>
        {d.impact && <NodeImpact nodeId={node.id} title={d.impact(host)} />}
        {canDrain && (
          <label className="flex items-start gap-2 text-sm">
            <input
              type="checkbox"
              className="mt-0.5"
              checked={drain}
              onChange={(e) => {
                setDrain(e.target.checked);
                setError(null);
              }}
            />
            <span>
              Eerst de VIP&apos;s weghalen
              <span className="block text-xs text-slate-500">
                {held.length
                  ? `Keepalived gaat uit zodat ${vips} netjes naar een andere node gaat voor ${host} herstart.`
                  : "Keepalived gaat netjes uit voor de node herstart."}
                {action === "reboot" && " Als de node terug is, gaat het weer aan."}
              </span>
            </span>
          </label>
        )}
        <Field label="Reden" htmlFor="node-action-reason" hint="Optioneel; komt in de activiteitenlog en in het systeemlog van de node.">
          <Input
            id="node-action-reason"
            value={reason}
            maxLength={200}
            onChange={(e) => setReason(e.target.value)}
            placeholder="bijvoorbeeld kernelupdate"
          />
        </Field>
        {error && (
          <Alert>
            {error.message.charAt(0).toUpperCase() + error.message.slice(1)}
            {/[.!?]$/.test(error.message) ? "" : "."}
          </Alert>
        )}
        <div className="flex flex-wrap justify-end gap-2">
          <Button type="button" variant="secondary" onClick={onClose}>
            Annuleren
          </Button>
          {error?.force ? (
            <Button type="button" variant="danger" disabled={run.isPending} onClick={() => void submit(true)}>
              Toch doorgaan
            </Button>
          ) : (
            <Button type="submit" variant={d.danger ? "danger" : "primary"} disabled={run.isPending}>
              {d.confirm}
            </Button>
          )}
        </div>
      </form>
    </Modal>
  );
}
