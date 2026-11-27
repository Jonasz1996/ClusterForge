"use client";

import Link from "next/link";
import { useState } from "react";
import { Modal } from "@/components/Modal";
import { Alert, Button, Field, Input } from "@/components/ui";
import { api, unwrap } from "@/lib/api/client";
import type { Datacenter, PveResource } from "@/lib/pve";
import { defaultServerURL } from "@/lib/pvehosts";

type Result = { name: string; job?: string; error?: string };

// AgentInstallDialog zet cf-agent in een of meer CT's via de Proxmox-host
// (pct push/exec), zonder per CT een commando te plakken (fase 48). De host
// test eerst vanuit de CT of ClusterForge en NATS (4222) bereikbaar zijn; zo
// niet, dan komt er niets in de CT en zegt de taak welke poort dicht is.
export function AgentInstallDialog({ dc, guests, onClose }: { dc: Datacenter; guests: PveResource[]; onClose: () => void }) {
  const [server, setServer] = useState(defaultServerURL);
  const [busy, setBusy] = useState(false);
  const [results, setResults] = useState<Result[] | null>(null);
  const cts = guests.filter((g) => g.type === "lxc" && g.template !== 1);
  const skipped = guests.length - cts.length;

  async function start() {
    setBusy(true);
    const out: Result[] = [];
    for (const g of cts) {
      const name = g.name || `CT ${g.vmid}`;
      const host = dc.hosts.find((h) => h.pve_node === g.node);
      if (!host) {
        out.push({ name, error: `geen agent op host ${g.node}` });
        continue;
      }
      try {
        const j = unwrap(
          await api.POST("/pve-hosts/{nodeId}/actions", {
            params: { path: { nodeId: host.node_id } },
            body: { op: { action: "bootstrap", type: "lxc", vmid: g.vmid }, server_url: server.trim() } as never,
          }),
        ) as unknown as { id: string };
        out.push({ name, job: j.id });
      } catch (e) {
        out.push({ name, error: e instanceof Error ? e.message : String(e) });
      }
    }
    setResults(out);
    setBusy(false);
  }

  return (
    <Modal title={`Agent installeren in ${cts.length} CT('s)`} onClose={onClose}>
      {results ? (
        <div className="space-y-3 text-sm">
          <ul className="space-y-1">
            {results.map((r) => (
              <li key={r.name}>
                {r.name}:{" "}
                {r.job ? (
                  <Link href={`/taken/detail?id=${r.job}`} className="underline">
                    taak gestart
                  </Link>
                ) : (
                  <span className="text-red-600 dark:text-red-400">{r.error}</span>
                )}
              </li>
            ))}
          </ul>
          <p className="text-slate-500">Zodra de agent zich aanmeldt, staat de CT bij Nodes.</p>
          <div className="flex justify-end">
            <Button onClick={onClose}>Sluiten</Button>
          </div>
        </div>
      ) : (
        <form
          className="space-y-4"
          onSubmit={(e) => {
            e.preventDefault();
            void start();
          }}
        >
          <p className="text-sm text-slate-600 dark:text-slate-300">
            De Proxmox-host zet cf-agent in elke CT met pct push en meldt hem aan met een eenmalig token. Eerst test hij vanuit de CT of
            ClusterForge en NATS (poort 4222) bereikbaar zijn; lukt dat niet, dan komt er niets in de CT en zegt de taak welke poort dicht
            staat. De CT&apos;s moeten draaien.
          </p>
          {skipped > 0 && <Alert kind="info">{skipped} gekozen VM(s) of templates slaat ClusterForge over: een VM krijgt de agent via de golden image.</Alert>}
          <Field label="Adres van ClusterForge" htmlFor="ai-server" hint="Zoals de CT het bereikt, bijvoorbeeld http://192.168.113.153:8080.">
            <Input id="ai-server" value={server} onChange={(e) => setServer(e.target.value)} placeholder="http://192.168.113.153:8080" />
          </Field>
          <div className="flex justify-end gap-2">
            <Button type="button" variant="secondary" onClick={onClose}>
              Annuleren
            </Button>
            <Button type="submit" disabled={busy || cts.length === 0 || !server.trim()}>
              Agent installeren
            </Button>
          </div>
        </form>
      )}
    </Modal>
  );
}
