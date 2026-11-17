"use client";

import Link from "next/link";
import { Suspense, useMemo, useState } from "react";
import { askConfirm } from "@/components/Confirm";
import { Empty, formatBytes, QueryState, tableClass, tdClass, thClass } from "@/components/inventory/bits";
import { CallForm, PbsGate, usePbsRunner, when } from "@/components/pbs/bits";
import { ActionMenu, TypedConfirm } from "@/components/pve/bits";
import { Tile as Win, TileGrid } from "@/components/Tiles";
import { Alert, Badge, Button, Card, cx, Input, PageHeader } from "@/components/ui";
import { useIsAdmin } from "@/lib/inventory";
import { usePbsGet, type PbsServer } from "@/lib/pbs";

export default function PbsSystemPage() {
  return (
    <div className="space-y-6">
      <PageHeader
        title="Systeem"
        description="De machine onder de Backup Server: schijven met SMART, ZFS-pools en mappen voor datastores, netwerk en DNS, diensten, updates, certificaten en het systeemlog."
      />
      <Suspense>
        <PbsGate>
          {(s) => (
            <TileGrid key={s.node_id}>
              <Win wide>
                <DisksCard srv={s} />
              </Win>
              <Win>
                <ServicesCard srv={s} />
              </Win>
              <Win>
                <UpdatesCard srv={s} />
              </Win>
              <Win wide>
                <NetworkCard srv={s} />
              </Win>
              <Win>
                <CertsCard srv={s} />
              </Win>
              <Win>
                <PowerCard srv={s} />
              </Win>
              <Win wide tall>
                <SyslogCard srv={s} />
              </Win>
            </TileGrid>
          )}
        </PbsGate>
      </Suspense>
    </div>
  );
}

type Disk = {
  name: string;
  devpath: string;
  "disk-type"?: string;
  size: number;
  model?: string;
  serial?: string;
  used?: string;
  status?: string;
  wearout?: number;
  gpt?: boolean;
};

const usedLabel: Record<string, string> = {
  unused: "vrij",
  mounted: "in gebruik (mount)",
  "lvm-pv": "LVM",
  "zfs-pool": "ZFS",
  partitions: "partities",
  filesystem: "bestandssysteem",
  "device-mapper": "device-mapper",
  "disk-usage": "in gebruik",
  "zfs-reserved": "ZFS",
  "partition": "partitie",
};

function DisksCard({ srv }: { srv: PbsServer }) {
  const id = srv.node_id;
  const isAdmin = useIsAdmin();
  const disks = usePbsGet<Disk[]>(id, "/nodes/localhost/disks/list", { params: { smart: "1" }, refetch: 60_000 });
  const zfs = usePbsGet<{ name: string; size: number; alloc: number; free: number; health: string; frag?: number }[]>(id, "/nodes/localhost/disks/zfs", { refetch: 60_000 });
  const dirs = usePbsGet<{ path: string; device: string; filesystem: string; options?: string }[]>(id, "/nodes/localhost/disks/directory", { refetch: 60_000 });
  const { run, pending, banner } = usePbsRunner(id);
  const [dialog, setDialog] = useState<{ kind: "zfs" | "dir" } | { kind: "wipe"; disk: string } | null>(null);
  const free = (disks.data ?? []).filter((d) => d.used === "unused");
  return (
    <Card title="Schijven">
      <div className="space-y-4">
        {banner}
        {isAdmin && (
          <div className="flex flex-wrap justify-end gap-2">
            <Button variant="secondary" disabled={free.length === 0} onClick={() => setDialog({ kind: "zfs" })}>
              ZFS-pool maken
            </Button>
            <Button variant="secondary" disabled={free.length === 0} onClick={() => setDialog({ kind: "dir" })}>
              Map op schijf maken
            </Button>
          </div>
        )}
        <QueryState q={disks}>
          {(disks.data ?? []).length === 0 ? (
            <Empty>Geen schijven gevonden.</Empty>
          ) : (
            <div className="relative -mx-4 overflow-x-auto sm:mx-0">
              <table className={tableClass}>
                <thead>
                  <tr>
                    <th className={thClass}>Schijf</th>
                    <th className={thClass}>Grootte</th>
                    <th className={cx(thClass, "hidden sm:table-cell")}>Gebruik</th>
                    <th className={thClass}>SMART</th>
                    <th className={cx(thClass, "hidden md:table-cell")}>Slijtage</th>
                    {isAdmin && <th className={thClass} />}
                  </tr>
                </thead>
                <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
                  {(disks.data ?? []).map((d) => (
                    <tr key={d.devpath}>
                      <td className={tdClass}>
                        <span className="font-mono text-xs">{d.devpath}</span>
                        <span className="block text-xs text-slate-500">
                          {[d["disk-type"]?.toUpperCase(), d.model, d.serial].filter(Boolean).join(" · ")}
                        </span>
                      </td>
                      <td className={cx(tdClass, "whitespace-nowrap tabular-nums")}>{formatBytes(d.size)}</td>
                      <td className={cx(tdClass, "hidden sm:table-cell")}>{usedLabel[d.used ?? ""] ?? d.used ?? "–"}</td>
                      <td className={tdClass}>
                        {d.status ? <Badge tone={d.status === "passed" || d.status === "ok" ? "green" : d.status === "unknown" ? "slate" : "red"}>{d.status}</Badge> : "–"}
                      </td>
                      <td className={cx(tdClass, "hidden tabular-nums md:table-cell")}>
                        {d.wearout !== undefined && d.wearout !== null ? (
                          <span className={cx(d.wearout < 20 && "text-red-600 dark:text-red-400")}>{d.wearout}% over</span>
                        ) : (
                          "–"
                        )}
                      </td>
                      {isAdmin && (
                        <td className={cx(tdClass, "text-right")}>
                          {d.used === "unused" || d.used === "partitions" || d.used === "filesystem" ? (
                            <ActionMenu
                              disabled={pending}
                              items={[
                                ...(d.gpt ? [] : [{ label: "GPT geven", onClick: () => void run({ method: "create", path: "/nodes/localhost/disks/initgpt", params: { disk: d.name } }) }]),
                                { label: "Wissen…", danger: true, onClick: () => setDialog({ kind: "wipe", disk: d.name }) },
                              ]}
                            />
                          ) : null}
                        </td>
                      )}
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </QueryState>
        {((zfs.data ?? []).length > 0 || (dirs.data ?? []).length > 0) && (
          <div className="grid gap-3 text-sm md:grid-cols-2">
            {(zfs.data ?? []).map((p) => (
              <div key={p.name} className="rounded-md border border-slate-200 p-2 dark:border-slate-800">
                <span className="font-medium">ZFS {p.name}</span> <Badge tone={p.health === "ONLINE" ? "green" : "red"}>{p.health}</Badge>
                <span className="block text-xs text-slate-500">
                  {formatBytes(p.alloc)} van {formatBytes(p.size)}
                  {p.frag !== undefined ? ` · fragmentatie ${p.frag}%` : ""}
                </span>
              </div>
            ))}
            {(dirs.data ?? []).map((d) => (
              <div key={d.path} className="rounded-md border border-slate-200 p-2 dark:border-slate-800">
                <span className="font-mono text-xs">{d.path}</span>
                <span className="block text-xs text-slate-500">
                  {d.filesystem} op {d.device}
                </span>
              </div>
            ))}
          </div>
        )}
      </div>
      {dialog?.kind === "zfs" && (
        <CallForm
          id={id}
          title="ZFS-pool maken"
          intro={<>Vrije schijven: {free.map((d) => d.name).join(", ")}. Geef ze gescheiden door een komma, zoals sdb,sdc. Met &quot;Datastore erop&quot; staat er meteen een datastore op de pool.</>}
          fields={[
            { key: "name", label: "Naam", required: true, placeholder: "backup" },
            { key: "devices", label: "Schijven", required: true, placeholder: free.map((d) => d.name).slice(0, 2).join(",") },
            {
              key: "raidlevel",
              label: "Opbouw",
              type: "select",
              options: [
                { value: "single", label: "Eén schijf" },
                { value: "mirror", label: "Spiegel (mirror)" },
                { value: "raid10", label: "RAID10" },
                { value: "raidz", label: "RAIDZ" },
                { value: "raidz2", label: "RAIDZ2" },
                { value: "raidz3", label: "RAIDZ3" },
              ],
            },
            {
              key: "compression",
              label: "Compressie",
              type: "select",
              options: ["on", "lz4", "zstd", "off"].map((v) => ({ value: v, label: v })),
            },
            { key: "ashift", label: "ashift", type: "number" },
            { key: "add-datastore", label: "Datastore erop", type: "checkbox" },
          ]}
          initial={{ raidlevel: free.length > 1 ? "mirror" : "single", compression: "on", ashift: "12", "add-datastore": "1" }}
          method="create"
          path="/nodes/localhost/disks/zfs"
          submitLabel="Pool maken"
          onClose={() => setDialog(null)}
        />
      )}
      {dialog?.kind === "dir" && (
        <CallForm
          id={id}
          title="Map op schijf maken"
          intro="Formatteert een vrije schijf, koppelt hem aan onder /mnt/datastore/<naam> en zet er desgewenst meteen een datastore op."
          fields={[
            { key: "name", label: "Naam", required: true, placeholder: "store2" },
            { key: "disk", label: "Schijf", type: "select", options: free.map((d) => ({ value: d.name, label: `${d.name} (${formatBytes(d.size)})` })) },
            { key: "filesystem", label: "Bestandssysteem", type: "select", options: ["ext4", "xfs"].map((v) => ({ value: v, label: v })) },
            { key: "add-datastore", label: "Datastore erop", type: "checkbox" },
            { key: "removable-datastore", label: "Verwijderbare datastore (USB, PBS 3.3+)", type: "checkbox" },
          ]}
          initial={{ disk: free[0]?.name ?? "", filesystem: "ext4", "add-datastore": "1", "removable-datastore": "0" }}
          method="create"
          path="/nodes/localhost/disks/directory"
          submitLabel="Maken"
          onClose={() => setDialog(null)}
        />
      )}
      {dialog?.kind === "wipe" && (
        <TypedConfirm
          title={`Schijf ${dialog.disk} wissen`}
          expect={dialog.disk}
          confirmLabel="Wissen"
          pending={pending}
          onClose={() => setDialog(null)}
          onConfirm={(typed) => {
            void run({ method: "set", path: "/nodes/localhost/disks/wipedisk", params: { disk: dialog.disk }, confirm: typed });
            setDialog(null);
          }}
        >
          <p>Alle partities en data op {dialog.disk} gaan weg. Dit kan niet terug.</p>
        </TypedConfirm>
      )}
    </Card>
  );
}

type Service = { service: string; name?: string; desc?: string; state: string; "unit-state"?: string };

function ServicesCard({ srv }: { srv: PbsServer }) {
  const id = srv.node_id;
  const isAdmin = useIsAdmin();
  const q = usePbsGet<Service[]>(id, "/nodes/localhost/services", { refetch: 30_000 });
  const { run, pending, banner } = usePbsRunner(id);
  return (
    <Card title="Diensten">
      <div className="space-y-3">
        {banner}
        <QueryState q={q}>
          <ul className="divide-y divide-slate-100 text-sm dark:divide-slate-800">
            {(q.data ?? []).map((s) => (
              <li key={s.service} className="flex items-center gap-2 py-1.5">
                <span className="min-w-0 flex-1">
                  <span className="font-medium">{s.service}</span>
                  {s.desc && <span className="block truncate text-xs text-slate-500">{s.desc}</span>}
                </span>
                <Badge tone={s.state === "running" ? "green" : s["unit-state"] === "disabled" ? "slate" : "red"}>{s.state}</Badge>
                {isAdmin && (
                  <ActionMenu
                    disabled={pending}
                    items={(["start", "stop", "restart", "reload"] as const).map((c) => ({
                      label: { start: "Starten", stop: "Stoppen", restart: "Herstarten", reload: "Herladen" }[c],
                      danger: c === "stop",
                      onClick: async () => {
                        if (c === "stop" && !(await askConfirm(`${s.service} stoppen?`, { danger: true, confirmLabel: "Stoppen" }))) return;
                        void run({ method: "create", path: `/nodes/localhost/services/${s.service}/${c}` });
                      },
                    }))}
                  />
                )}
              </li>
            ))}
          </ul>
        </QueryState>
      </div>
    </Card>
  );
}

type AptPkg = { Package: string; Title?: string; Version: string; OldVersion?: string; Origin?: string; Priority?: string };

function UpdatesCard({ srv }: { srv: PbsServer }) {
  const id = srv.node_id;
  const isAdmin = useIsAdmin();
  const q = usePbsGet<AptPkg[]>(id, "/nodes/localhost/apt/update", { refetch: 300_000 });
  const ver = usePbsGet<{ Package: string; Version: string; OldVersion?: string; CurrentState?: string }[]>(id, "/nodes/localhost/apt/versions", { refetch: 300_000 });
  const { run, pending, banner } = usePbsRunner(id);
  const pbsPkg = (ver.data ?? []).find((p) => p.Package === "proxmox-backup-server");
  return (
    <Card title={`Updates${q.data?.length ? ` · ${q.data.length}` : ""}`}>
      <div className="space-y-3 text-sm">
        {banner}
        {pbsPkg && (
          <p>
            proxmox-backup-server <span className="font-mono">{pbsPkg.OldVersion || pbsPkg.Version}</span>
          </p>
        )}
        <QueryState q={q}>
          {(q.data ?? []).length === 0 ? (
            <p className="text-slate-500">Alles is bij.</p>
          ) : (
            <ul className="max-h-48 space-y-0.5 overflow-auto font-mono text-xs">
              {(q.data ?? []).map((p) => (
                <li key={p.Package}>
                  {p.Package} {p.OldVersion} → {p.Version}
                </li>
              ))}
            </ul>
          )}
        </QueryState>
        <p className="text-slate-500">
          Installeren gaat zoals bij elke node, met apt via de agent:{" "}
          <Link href={`/nodes/detail?id=${id}#updates`} className="underline">
            {srv.hostname} → Updates
          </Link>
          .
        </p>
        {isAdmin && (
          <Button variant="secondary" disabled={pending} onClick={() => void run({ method: "create", path: "/nodes/localhost/apt/update" })}>
            Pakketlijsten verversen
          </Button>
        )}
      </div>
    </Card>
  );
}

type Iface = { name: string; type: string; method?: string; cidr?: string; gateway?: string; autostart?: boolean; active?: boolean; comments?: string; bridge_ports?: string[] };

function NetworkCard({ srv }: { srv: PbsServer }) {
  const id = srv.node_id;
  const isAdmin = useIsAdmin();
  const q = usePbsGet<Iface[]>(id, "/nodes/localhost/network", { refetch: 60_000 });
  const dns = usePbsGet<{ search?: string; dns1?: string; dns2?: string; dns3?: string }>(id, "/nodes/localhost/dns", { refetch: 300_000 });
  const time = usePbsGet<{ timezone?: string; localtime?: number }>(id, "/nodes/localhost/time", { refetch: 300_000 });
  const { run, pending, banner } = usePbsRunner(id);
  const [edit, setEdit] = useState<Iface | null>(null);
  const [dnsEdit, setDnsEdit] = useState(false);
  const [apply, setApply] = useState(false);
  const list = (q.data ?? []).filter((i) => i.type !== "loopback").sort((a, b) => a.name.localeCompare(b.name));
  return (
    <Card title="Netwerk">
      <div className="space-y-3">
        {banner}
        <QueryState q={q}>
          <div className="relative -mx-4 overflow-x-auto sm:mx-0">
            <table className={tableClass}>
              <thead>
                <tr>
                  <th className={thClass}>Kaart</th>
                  <th className={thClass}>Adres</th>
                  <th className={cx(thClass, "hidden sm:table-cell")}>Gateway</th>
                  <th className={cx(thClass, "hidden md:table-cell")}>Soort</th>
                  {isAdmin && <th className={thClass} />}
                </tr>
              </thead>
              <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
                {list.map((i) => (
                  <tr key={i.name}>
                    <td className={tdClass}>
                      <span className="font-medium">{i.name}</span>
                      {i.active === false && (
                        <span className="ml-2">
                          <Badge>niet actief</Badge>
                        </span>
                      )}
                      {i.comments && <span className="block text-xs text-slate-500">{i.comments}</span>}
                    </td>
                    <td className={cx(tdClass, "font-mono text-xs")}>{i.cidr || i.method || "–"}</td>
                    <td className={cx(tdClass, "hidden font-mono text-xs sm:table-cell")}>{i.gateway || "–"}</td>
                    <td className={cx(tdClass, "hidden md:table-cell")}>
                      {i.type}
                      {i.bridge_ports?.length ? ` (${i.bridge_ports.join(", ")})` : ""}
                    </td>
                    {isAdmin && (
                      <td className={cx(tdClass, "text-right")}>
                        <Button variant="secondary" onClick={() => setEdit(i)}>
                          Wijzigen
                        </Button>
                      </td>
                    )}
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </QueryState>
        <div className="flex flex-wrap items-center gap-x-6 gap-y-1 text-sm">
          <span>
            <span className="text-slate-500">DNS </span>
            {[dns.data?.dns1, dns.data?.dns2, dns.data?.dns3].filter(Boolean).join(", ") || "–"}
            {dns.data?.search && <span className="text-slate-500"> · zoekdomein {dns.data.search}</span>}
          </span>
          <span>
            <span className="text-slate-500">Tijdzone </span>
            {time.data?.timezone ?? "–"}
          </span>
        </div>
        {isAdmin && (
          <div className="flex flex-wrap gap-2">
            <Button variant="secondary" onClick={() => setDnsEdit(true)}>
              DNS wijzigen
            </Button>
            <Button variant="secondary" onClick={() => setApply(true)}>
              Netwerkwijzigingen toepassen
            </Button>
            <Button
              variant="secondary"
              disabled={pending}
              onClick={async () => {
                if (await askConfirm("Niet toegepaste netwerkwijzigingen weggooien?", { confirmLabel: "Terugdraaien" })) void run({ method: "delete", path: "/nodes/localhost/network" });
              }}
            >
              Terugdraaien
            </Button>
          </div>
        )}
      </div>
      {edit && (
        <CallForm
          id={id}
          title={`${edit.name} wijzigen`}
          intro="De wijziging staat klaar tot je ze toepast. Een fout adres kan ClusterForge de verbinding met de agent kosten; dan herstel je het op de console van de PBS."
          fields={[
            { key: "cidr", label: "Adres (CIDR)", placeholder: "192.168.10.20/24" },
            { key: "gateway", label: "Gateway" },
            { key: "comments", label: "Omschrijving" },
            { key: "autostart", label: "Bij opstarten aan", type: "checkbox" },
          ]}
          initial={{ cidr: edit.cidr ?? "", gateway: edit.gateway ?? "", comments: edit.comments ?? "", autostart: edit.autostart ? "1" : "0" }}
          method="set"
          path={`/nodes/localhost/network/${edit.name}`}
          fixed={{ type: edit.type }}
          submitLabel="Klaarzetten"
          onClose={() => setEdit(null)}
        />
      )}
      {dnsEdit && (
        <CallForm
          id={id}
          title="DNS van de PBS"
          fields={[
            { key: "search", label: "Zoekdomein", required: true },
            { key: "dns1", label: "DNS 1" },
            { key: "dns2", label: "DNS 2" },
            { key: "dns3", label: "DNS 3" },
          ]}
          initial={{ search: dns.data?.search ?? "", dns1: dns.data?.dns1 ?? "", dns2: dns.data?.dns2 ?? "", dns3: dns.data?.dns3 ?? "" }}
          method="set"
          path="/nodes/localhost/dns"
          onlyChanged={false}
          submitLabel="Opslaan"
          onClose={() => setDnsEdit(false)}
        />
      )}
      {apply && (
        <TypedConfirm
          title="Netwerkwijzigingen toepassen"
          expect="netwerk"
          confirmLabel="Toepassen"
          pending={pending}
          onClose={() => setApply(false)}
          onConfirm={(typed) => {
            void run({ method: "set", path: "/nodes/localhost/network", confirm: typed });
            setApply(false);
          }}
        >
          <p>PBS herlaadt het netwerk met ifupdown2. Lopende back-ups kunnen even haperen.</p>
        </TypedConfirm>
      )}
    </Card>
  );
}

type Cert = { filename: string; fingerprint?: string; issuer?: string; subject?: string; notafter?: number; san?: string[] };

function CertsCard({ srv }: { srv: PbsServer }) {
  const id = srv.node_id;
  const isAdmin = useIsAdmin();
  const q = usePbsGet<Cert[]>(id, "/nodes/localhost/certificates/info", { enabled: isAdmin, refetch: 300_000 });
  const acme = usePbsGet<{ acme?: string; acmedomain0?: string }>(id, "/nodes/localhost/config", { enabled: isAdmin, refetch: 300_000 });
  const { run, pending, banner } = usePbsRunner(id);
  const [now] = useState(() => Date.now() / 1000);
  if (!isAdmin) return null;
  return (
    <Card title="Certificaten">
      <div className="space-y-3 text-sm">
        {banner}
        <QueryState q={q}>
          {(q.data ?? [])
            .filter((c) => c.filename === "proxy.pem")
            .map((c) => {
              const days = c.notafter ? Math.round((c.notafter - now) / 86400) : undefined;
              return (
                <div key={c.filename} className="space-y-1">
                  <p>
                    <span className="text-slate-500">Uitgever </span>
                    {c.issuer}
                  </p>
                  <p>
                    <span className="text-slate-500">Namen </span>
                    {(c.san ?? []).join(", ") || c.subject}
                  </p>
                  <p>
                    <span className="text-slate-500">Geldig tot </span>
                    {when(c.notafter)}{" "}
                    {days !== undefined && <Badge tone={days < 14 ? "red" : days < 30 ? "amber" : "green"}>{days} dagen</Badge>}
                  </p>
                  <p className="font-mono text-xs break-all text-slate-500">{c.fingerprint}</p>
                </div>
              );
            })}
        </QueryState>
        {acme.data?.acmedomain0 ? (
          <Button variant="secondary" disabled={pending} onClick={() => void run({ method: "set", path: "/nodes/localhost/certificates/acme/certificate", params: { force: "1" } })}>
            ACME vernieuwen
          </Button>
        ) : (
          <p className="text-slate-500">Geen ACME-domein ingesteld; een eigen certificaat zet je op de PBS of achter NPM.</p>
        )}
      </div>
    </Card>
  );
}

function PowerCard({ srv }: { srv: PbsServer }) {
  const id = srv.node_id;
  const isAdmin = useIsAdmin();
  const { run, pending, banner } = usePbsRunner(id);
  const [what, setWhat] = useState<"reboot" | "shutdown" | null>(null);
  if (!isAdmin) return null;
  return (
    <Card title="Aan en uit">
      <div className="space-y-3 text-sm">
        {banner}
        <p className="text-slate-500">Lopende back-ups en jobs breken af. Na een herstart komt de agent vanzelf terug.</p>
        <div className="flex flex-wrap gap-2">
          <Button variant="secondary" onClick={() => setWhat("reboot")}>
            Herstarten
          </Button>
          <Button variant="secondary-danger" onClick={() => setWhat("shutdown")}>
            Uitzetten
          </Button>
        </div>
      </div>
      {what && (
        <TypedConfirm
          title={what === "reboot" ? `${srv.hostname} herstarten` : `${srv.hostname} uitzetten`}
          expect="localhost"
          confirmLabel={what === "reboot" ? "Herstarten" : "Uitzetten"}
          pending={pending}
          onClose={() => setWhat(null)}
          onConfirm={(typed) => {
            void run({ method: "create", path: "/nodes/localhost/status", params: { command: what }, confirm: typed });
            setWhat(null);
          }}
        >
          <p>{what === "shutdown" ? "De PBS gaat uit en komt niet vanzelf terug: zet hem weer aan in Proxmox of op de machine." : "De PBS start opnieuw op."}</p>
        </TypedConfirm>
      )}
    </Card>
  );
}

function SyslogCard({ srv }: { srv: PbsServer }) {
  const q = usePbsGet<{ n: number; t: string }[]>(srv.node_id, "/nodes/localhost/syslog", { params: { limit: "500" }, refetch: 15_000 });
  const [filter, setFilter] = useState("");
  const lines = useMemo(() => {
    const f = filter.trim().toLowerCase();
    return (q.data ?? []).map((l) => l.t).filter((t) => !f || t.toLowerCase().includes(f));
  }, [q.data, filter]);
  return (
    <Card title="Systeemlog">
      <div className="space-y-2">
        <Input value={filter} onChange={(e) => setFilter(e.target.value)} placeholder="Filter, zoals error of proxmox-backup-proxy" aria-label="Filter op het systeemlog" />
        <QueryState q={q}>
          {lines.length === 0 ? (
            <Alert kind="info">Geen regels.</Alert>
          ) : (
            <pre className="max-h-[60vh] overflow-auto rounded-md bg-slate-950 p-3 font-mono text-xs leading-relaxed whitespace-pre-wrap text-slate-100">{lines.join("\n")}</pre>
          )}
        </QueryState>
      </div>
    </Card>
  );
}
