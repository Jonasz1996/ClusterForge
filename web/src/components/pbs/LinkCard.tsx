"use client";

import Link from "next/link";
import { useQueries } from "@tanstack/react-query";
import { useState } from "react";
import { Modal } from "@/components/Modal";
import { Alert, Button, Card, Field, Input, Select } from "@/components/ui";
import { useIsAdmin } from "@/lib/inventory";
import { pbsLink, type PbsServer } from "@/lib/pbs";
import { pveGet, useDatacenters, type Datacenter } from "@/lib/pve";
import type { Job } from "@/lib/proxmox";

type PveStorage = { storage: string; type: string; server?: string; datastore?: string; namespace?: string; fingerprint?: string; disable?: number };

// LinkCard toont welke Proxmox-clusters deze PBS al als storage hebben en
// koppelt hem met één klik aan een ander (fase 32 M5).
export function LinkCard({ srv, fingerprint }: { srv: PbsServer; fingerprint?: string }) {
  const isAdmin = useIsAdmin();
  const dcs = useDatacenters();
  const list = (dcs.data?.items ?? []).filter((d) => d.via);
  const storages = useQueries({
    queries: list.map((d) => ({
      queryKey: ["pve", d.id, "get", "/storage", { type: "pbs" }],
      queryFn: () => pveGet<PveStorage[]>(d.id, "/storage", { type: "pbs" }),
      refetchInterval: 60_000,
      retry: false,
    })),
  });
  const [open, setOpen] = useState<Datacenter | null>(null);
  const matches = (s: PveStorage) =>
    s.type === "pbs" && ((!!fingerprint && s.fingerprint?.toLowerCase() === fingerprint.toLowerCase()) || s.server === srv.address || s.server === srv.hostname || s.server?.startsWith(srv.hostname + "."));
  return (
    <Card title="Proxmox koppelen">
      <div className="space-y-3 text-sm">
        {list.length === 0 ? (
          <p className="text-slate-500">
            Geen Proxmox-cluster met een agent. Installeer de agent op je Proxmox-hosts; daarna koppel je deze PBS hier met één klik.
          </p>
        ) : (
          <ul className="divide-y divide-slate-100 dark:divide-slate-800">
            {list.map((d, i) => {
              const linked = (storages[i]?.data ?? []).filter(matches);
              return (
                <li key={d.id} className="flex flex-wrap items-center gap-2 py-2">
                  <span className="min-w-0 flex-1">
                    <span className="font-medium">{d.name}</span>
                    <span className="block text-xs text-slate-500">
                      {storages[i]?.isLoading
                        ? "Laden…"
                        : linked.length > 0
                          ? "Gekoppeld als " + linked.map((s) => `${s.storage} (${s.datastore}${s.namespace ? "/" + s.namespace : ""})`).join(", ")
                          : "Nog niet gekoppeld"}
                    </span>
                  </span>
                  {isAdmin && (
                    <Button variant={linked.length ? "secondary" : "primary"} onClick={() => setOpen(d)}>
                      {linked.length ? "Nog een datastore" : "Koppelen"}
                    </Button>
                  )}
                </li>
              );
            })}
          </ul>
        )}
      </div>
      {open && <LinkDialog srv={srv} dc={open} onClose={() => setOpen(null)} />}
    </Card>
  );
}

function LinkDialog({ srv, dc, onClose }: { srv: PbsServer; dc: Datacenter; onClose: () => void }) {
  const [store, setStore] = useState(srv.datastores[0] ?? "");
  const [name, setName] = useState(`pbs-${srv.datastores[0] ?? "backup"}`);
  const [ns, setNs] = useState("");
  const [server, setServer] = useState(srv.address ?? srv.hostname);
  const [role, setRole] = useState<"DatastoreBackup" | "DatastorePowerUser">("DatastoreBackup");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  const [job, setJob] = useState<Job | null>(null);
  const submit = async () => {
    setBusy(true);
    setErr("");
    try {
      setJob(await pbsLink(srv.node_id, { datacenter: dc.id, datacenter_name: dc.name, datastore: store, storage: name.trim(), namespace: ns.trim() || undefined, server: server.trim(), role }));
    } catch (e) {
      setErr(e instanceof Error ? e.message : "Mislukt");
    } finally {
      setBusy(false);
    }
  };
  return (
    <Modal title={`${srv.hostname} koppelen aan ${dc.name}`} onClose={onClose}>
      <form
        className="space-y-3 text-sm"
        onSubmit={(e) => {
          e.preventDefault();
          void submit();
        }}
      >
        {err && <Alert>{err}</Alert>}
        {job ? (
          <Alert kind="success">
            Op de PBS staan een eigen gebruiker en token voor {dc.name} met rechten op {store}. De storage wordt nu in Proxmox gezet.{" "}
            <Link href={`/taken/detail?id=${job.id}`} className="underline">
              Volgen
            </Link>
          </Alert>
        ) : (
          <>
            <p className="text-slate-600 dark:text-slate-300">
              ClusterForge maakt op de PBS een gebruiker en API-token voor dit cluster, geeft dat token alleen rechten op de gekozen datastore, en zet de storage in Proxmox
              met de fingerprint van de PBS. Het geheim gaat versleuteld mee en komt nergens in een log. Daarna kies je de storage bij een back-upjob onder Proxmox → Back-ups.
            </p>
            <Field label="Datastore" htmlFor="l-store">
              <Select
                id="l-store"
                value={store}
                onChange={(e) => {
                  setStore(e.target.value);
                  setName(`pbs-${e.target.value}`);
                }}
              >
                {srv.datastores.map((d) => (
                  <option key={d}>{d}</option>
                ))}
              </Select>
            </Field>
            <Field label="Namespace (leeg = hoofd)" htmlFor="l-ns" hint="Handig met meer Proxmox-clusters op één datastore, zoals de naam van het cluster.">
              <Input id="l-ns" value={ns} onChange={(e) => setNs(e.target.value)} placeholder={dc.name} />
            </Field>
            <Field label="Naam van de storage in Proxmox" htmlFor="l-name">
              <Input id="l-name" value={name} onChange={(e) => setName(e.target.value)} />
            </Field>
            <Field label="Adres van de PBS voor Proxmox" htmlFor="l-server">
              <Input id="l-server" value={server} onChange={(e) => setServer(e.target.value)} />
            </Field>
            <Field label="Rechten" htmlFor="l-role">
              <Select id="l-role" value={role} onChange={(e) => setRole(e.target.value as typeof role)}>
                <option value="DatastoreBackup">Back-ups maken en terugzetten (aanbevolen)</option>
                <option value="DatastorePowerUser">Ook prune en verwijderen vanuit Proxmox</option>
              </Select>
            </Field>
          </>
        )}
        <div className="flex justify-end gap-2">
          <Button type="button" variant="secondary" onClick={onClose}>
            {job ? "Sluiten" : "Annuleren"}
          </Button>
          {!job && (
            <Button type="submit" disabled={busy || !store || !name.trim() || !server.trim()}>
              {busy ? "Bezig…" : "Koppelen"}
            </Button>
          )}
        </div>
      </form>
    </Modal>
  );
}
