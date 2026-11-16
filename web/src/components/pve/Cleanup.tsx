"use client";

import Link from "next/link";
import { useState, type ReactNode } from "react";
import { Empty, formatBytes, tableClass, tdClass, thClass } from "@/components/inventory/bits";
import { TypedConfirm, useCallRunner } from "@/components/pve/bits";
import { fileName } from "@/components/pve/Media";
import { Alert, Button, Card } from "@/components/ui";
import { useIsAdmin } from "@/lib/inventory";
import { guestHref, guestLabel, storesWith, useGuestConfigs, usePerStorage, type Datacenter, type PveResource, type StorageItem } from "@/lib/pve";

// Opruimen (fase 26 M5): wat ruimte inneemt maar door niemand gebruikt wordt.
//  - wezen: schijven van een gast-ID die niet meer bestaat;
//  - ongebruikte schijven (unusedN) die nog aan een gast hangen;
//  - ISO's die in geen enkel cd-station zitten;
//  - achtergebleven terugzettests.

type Vol = StorageItem & { node: string; storage: string; shared: boolean };

export function CleanupView({ dc, res }: { dc: Datacenter; res: PveResource[] }) {
  const isAdmin = useIsAdmin();
  const guests = res.filter((r) => (r.type === "qemu" || r.type === "lxc") && r.vmid);
  const ids = new Set(guests.map((g) => g.vmid));
  const stores = storesWith(res, ["images", "rootdir", "iso"]);
  const lists = usePerStorage<StorageItem, (typeof stores)[number]>(dc.id, stores, {});
  const { configs, loading } = useGuestConfigs(dc.id, guests);
  const { run, pending, banner } = useCallRunner(dc.id);
  const [del, setDel] = useState<Vol | null>(null);

  const vols: Vol[] = lists.flatMap((l) =>
    (l.data ?? []).map((x) => ({
      ...x,
      node: l.node,
      storage: l.storage,
      shared: l.shared,
    })),
  );
  const orphans = vols.filter((v) => (v.content === "images" || v.content === "rootdir") && v.vmid && !ids.has(v.vmid));
  const unused = configs.flatMap(({ guest, config }) =>
    Object.entries(config ?? {})
      .filter(([k]) => /^unused\d+$/.test(k))
      .map(([k, v]) => ({
        guest,
        key: k,
        volid: String(v),
        size: vols.find((x) => x.volid === String(v))?.size,
      })),
  );
  const mounted = new Set(
    configs.flatMap(({ config }) =>
      Object.entries(config ?? {})
        .filter(([k, v]) => /^(ide|sata|scsi)\d+$/.test(k) && typeof v === "string" && v.includes("media=cdrom"))
        .map(([, v]) => String(v).split(",")[0]),
    ),
  );
  // Alleen als elke configuratie gelezen is, weten we zeker dat een ISO nergens in zit.
  const allConfigs = configs.every((c) => c.config);
  const isos = allConfigs ? vols.filter((v) => v.content === "iso" && !mounted.has(v.volid)) : [];
  const leftovers = configs.filter(({ config }) => String(config?.description ?? "").includes("clusterforge-terugzettest"));
  const errors = lists.filter((l) => l.error);
  const busy = loading || lists.some((l) => l.loading);
  const total = (xs: { size?: number }[]) => formatBytes(xs.reduce((n, x) => n + (x.size ?? 0), 0));

  return (
    <div className="space-y-4">
      {banner}
      {errors.length > 0 && (
        <Alert kind="info">Niet te lezen: {errors.map((e) => `${e.storage} op ${e.node}`).join(", ")}. Wat daar staat, ontbreekt hieronder.</Alert>
      )}

      <Card title={`Schijven zonder gast${orphans.length ? ` (${orphans.length}, ${total(orphans)})` : ""}`}>
        <p className="mb-3 text-sm text-slate-500">
          Schijven van een gast-ID dat niet meer bestaat, bijvoorbeeld na een mislukte verhuizing of een half verwijderde gast.
        </p>
        {orphans.length === 0 ? (
          <Empty>{busy ? "Zoeken…" : "Niets gevonden."}</Empty>
        ) : (
          <VolTable
            rows={orphans.map((v) => ({
              key: v.node + v.volid,
              cells: [
                <span key="v" className="font-mono text-xs break-all">
                  {v.volid}
                </span>,
                `ID ${v.vmid}`,
                v.shared ? v.storage : `${v.storage} op ${v.node}`,
                formatBytes(v.size),
              ],
              v,
            }))}
            head={["Schijf", "Van", "Opslag", "Grootte"]}
            isAdmin={isAdmin}
            pending={pending}
            onDelete={setDel}
          />
        )}
      </Card>

      <Card title={`Ongebruikte schijven${unused.length ? ` (${unused.length})` : ""}`}>
        <p className="mb-3 text-sm text-slate-500">
          Losgekoppelde schijven die nog bij een gast horen. Weghalen gaat bij de gast zelf, op het tabblad Hardware.
        </p>
        {unused.length === 0 ? (
          <Empty>{busy ? "Zoeken…" : "Niets gevonden."}</Empty>
        ) : (
          <div className="relative -mx-4 overflow-x-auto sm:mx-0">
            <table className={tableClass}>
              <thead>
                <tr>
                  <th className={thClass}>Gast</th>
                  <th className={thClass}>Schijf</th>
                  <th className={thClass}>Grootte</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
                {unused.map((u) => (
                  <tr key={`${u.guest.vmid}-${u.key}`}>
                    <td className={tdClass}>
                      <Link
                        href={
                          guestHref(dc.id, {
                            node: u.guest.node,
                            type: u.guest.type,
                            vmid: u.guest.vmid,
                          }) + "&tab=hardware"
                        }
                        className="font-medium hover:underline"
                      >
                        {guestLabel(u.guest.type as "qemu" | "lxc")} {u.guest.vmid} {u.guest.name}
                      </Link>
                    </td>
                    <td className={tdClass + " font-mono text-xs break-all"}>
                      {u.key}: {u.volid}
                    </td>
                    <td className={tdClass}>{u.size ? formatBytes(u.size) : "–"}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Card>

      <Card title={`ISO's die nergens in zitten${isos.length ? ` (${isos.length}, ${total(isos)})` : ""}`}>
        {isos.length === 0 ? (
          <Empty>
            {busy
              ? "Zoeken…"
              : !allConfigs
                ? "Niet van elke gast de configuratie te lezen; daarom toont ClusterForge hier niets."
                : "Elke ISO zit in een cd-station, of er zijn geen ISO's."}
          </Empty>
        ) : (
          <VolTable
            rows={isos.map((v) => ({
              key: v.node + v.volid,
              cells: [
                <span key="v" className="break-all">
                  {fileName(v.volid)}
                </span>,
                v.shared ? v.storage : `${v.storage} op ${v.node}`,
                formatBytes(v.size),
              ],
              v,
            }))}
            head={["ISO", "Opslag", "Grootte"]}
            isAdmin={isAdmin}
            pending={pending}
            onDelete={setDel}
          />
        )}
      </Card>

      {leftovers.length > 0 && (
        <Card title="Achtergebleven terugzettests">
          <p className="mb-3 text-sm text-slate-500">
            Een terugzettest ruimt zichzelf op; deze gasten bleven staan omdat de test halverwege stopte. Verwijder ze bij de gast zelf.
          </p>
          <ul className="space-y-1 text-sm">
            {leftovers.map(({ guest }) => (
              <li key={guest.vmid}>
                <Link
                  href={guestHref(dc.id, {
                    node: guest.node,
                    type: guest.type,
                    vmid: guest.vmid,
                  })}
                  className="hover:underline"
                >
                  {guestLabel(guest.type as "qemu" | "lxc")} {guest.vmid} {guest.name} op {guest.node}
                </Link>
              </li>
            ))}
          </ul>
        </Card>
      )}

      {del && (
        <TypedConfirm
          title={`${fileName(del.volid)} weghalen`}
          expect={del.volid}
          confirmLabel="Weghalen"
          pending={pending}
          onClose={() => setDel(null)}
          onConfirm={(typed) =>
            void run({
              method: "delete",
              path: `/nodes/${del.node}/storage/${del.storage}/content/${del.volid}`,
              confirm: typed,
            }).then((ok) => ok && setDel(null))
          }
        >
          <p className="text-sm">
            {formatBytes(del.size)} op {del.storage} wordt gewist. Dat kan niet ongedaan gemaakt worden.
          </p>
          {del.shared && del.vmid ? (
            <p className="text-sm">Deze opslag is gedeeld. Gebruikt een ander Proxmox-cluster hem ook, kijk dan eerst of ID {del.vmid} daar niet bestaat.</p>
          ) : null}
        </TypedConfirm>
      )}
    </div>
  );
}

function VolTable({
  head,
  rows,
  isAdmin,
  pending,
  onDelete,
}: {
  head: string[];
  rows: { key: string; cells: ReactNode[]; v: Vol }[];
  isAdmin: boolean;
  pending: boolean;
  onDelete: (v: Vol) => void;
}) {
  return (
    <div className="relative -mx-4 overflow-x-auto sm:mx-0">
      <table className={tableClass}>
        <thead>
          <tr>
            {head.map((h) => (
              <th key={h} className={thClass}>
                {h}
              </th>
            ))}
            <th className={thClass}>
              <span className="sr-only">Acties</span>
            </th>
          </tr>
        </thead>
        <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
          {rows.map((r) => (
            <tr key={r.key}>
              {r.cells.map((c, i) => (
                <td key={i} className={tdClass}>
                  {c}
                </td>
              ))}
              <td className={tdClass + " text-right"}>
                {isAdmin && (
                  <Button variant="secondary" disabled={pending} onClick={() => onDelete(r.v)}>
                    Weghalen
                  </Button>
                )}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
