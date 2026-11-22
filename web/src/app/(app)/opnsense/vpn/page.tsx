"use client";

import { Suspense } from "react";
import { formatBytes, tableClass, tdClass, thClass } from "@/components/inventory/bits";
import { ago, OpnGate } from "@/components/opnsense/bits";
import { ItemTiles, type ItemSpec } from "@/components/opnsense/ItemTable";
import { Tile as Win } from "@/components/Tiles";
import { Badge, Card, PageHeader } from "@/components/ui";
import { useOpnGet, type OpnFirewall, type OpnSearch } from "@/lib/opnsense";

export default function OpnVpnPage() {
  return (
    <div className="space-y-6">
      <PageHeader
        title="VPN"
        description="WireGuard, OpenVPN en IPsec op de OPNsense-master: wie er verbonden is, en de peers en instanties beheren. Sleutels gaan versleuteld mee en komen nooit in een taak of het logboek."
      />
      <Suspense>
        <OpnGate>
          {(c) => (
            <ItemTiles
              key={c.key}
              c={c}
              specs={specs}
              extra={(n) => (
                <>
                  <Win wide>
                    <WireGuardStatus n={n} />
                  </Win>
                  <Win>
                    <OpenVPNSessions n={n} />
                  </Win>
                  <Win>
                    <IPsecSessions n={n} />
                  </Win>
                </>
              )}
            />
          )}
        </OpnGate>
      </Suspense>
    </div>
  );
}

const specs: ItemSpec[] = [
  {
    title: "WireGuard: peers",
    search: "wireguard/client/searchClient",
    base: "wireguard/client",
    x: "Client",
    root: "client",
    what: "WireGuard-peer",
    name: "name",
    cols: [
      ["name", "Naam"],
      ["tunneladdress", "Tunneladres"],
      ["serveraddress", "Endpoint"],
      ["keepalive", "Keepalive"],
    ],
    fields: () => [
      { key: "enabled", label: "Aan", kind: "check" },
      { key: "name", label: "Naam" },
      { key: "pubkey", label: "Publieke sleutel", wide: true },
      { key: "psk", label: "Pre-shared key", hint: "Leeg laten om hem te houden", wide: true },
      { key: "tunneladdress", label: "Toegelaten adressen", hint: "Bijvoorbeeld 10.10.10.2/32" },
      { key: "serveraddress", label: "Endpoint", hint: "Alleen als de firewall zelf verbindt" },
      { key: "serverport", label: "Endpointpoort" },
      { key: "keepalive", label: "Keepalive (s)" },
    ],
    defaults: { enabled: "1", keepalive: "25" },
  },
  {
    title: "WireGuard: instanties",
    search: "wireguard/server/searchServer",
    base: "wireguard/server",
    x: "Server",
    root: "server",
    what: "WireGuard-instantie",
    name: "name",
    noAdd: true,
    hint: "Een nieuwe instantie maak je in OPNsense (die maakt de sleutels); hier pas je ze aan.",
    cols: [
      ["name", "Naam"],
      ["interface", "Apparaat"],
      ["port", "Poort"],
      ["tunneladdress", "Tunneladres"],
    ],
    fields: () => [
      { key: "enabled", label: "Aan", kind: "check" },
      { key: "name", label: "Naam" },
      { key: "port", label: "Luisterpoort" },
      { key: "tunneladdress", label: "Tunneladres" },
      { key: "mtu", label: "MTU" },
      { key: "dns", label: "DNS" },
    ],
  },
  {
    title: "OpenVPN: instanties",
    search: "openvpn/instances/search",
    base: "openvpn/instances",
    x: "",
    root: "instance",
    what: "OpenVPN-instantie",
    name: "description",
    noAdd: true,
    hint: "Instanties met certificaten maak je in OPNsense; hier zet je ze aan of uit en pas je de basis aan.",
    cols: [
      ["description", "Naam"],
      ["role", "Rol"],
      ["dev_type", "Soort"],
      ["proto", "Protocol"],
      ["port", "Poort"],
    ],
    fields: () => [
      { key: "enabled", label: "Aan", kind: "check" },
      { key: "description", label: "Naam" },
      { key: "port", label: "Poort" },
      { key: "server", label: "Tunnelnet (server)" },
    ],
  },
];

type WGRow = {
  type: string;
  name?: string;
  ifname?: string;
  "public-key"?: string;
  endpoint?: string;
  "allowed-ips"?: string;
  "latest-handshake"?: number;
  "transfer-rx"?: number;
  "transfer-tx"?: number;
  "peer-status"?: string;
};

function WireGuardStatus({ n }: { n: OpnFirewall }) {
  const q = useOpnGet<{ rows: WGRow[] }>(n.node_id, "wireguard/service/show");
  const peers = (q.data?.rows ?? []).filter((r) => r.type === "peer");
  return (
    <Card title={`WireGuard: verbindingen${q.data ? ` · ${peers.filter((p) => p["peer-status"] === "online").length} online` : ""}`}>
      {q.isError ? (
        <p className="text-sm text-slate-500">Geen WireGuard op deze firewall ({(q.error as Error).message}).</p>
      ) : peers.length === 0 ? (
        <p className="text-sm text-slate-500">Geen peers.</p>
      ) : (
        <div className="overflow-x-auto">
          <table className={tableClass}>
            <thead>
              <tr>
                <th className={thClass}>Peer</th>
                <th className={thClass}>Instantie</th>
                <th className={thClass}>Endpoint</th>
                <th className={thClass}>Laatste handshake</th>
                <th className={thClass}>In / uit</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
              {peers.map((p) => (
                <tr key={`${p.ifname}-${p["public-key"]}`}>
                  <td className={tdClass}>
                    <span className="inline-flex items-center gap-2">
                      {p.name || p["public-key"]?.slice(0, 10)}
                      <Badge tone={p["peer-status"] === "online" ? "green" : "slate"}>{p["peer-status"] ?? "?"}</Badge>
                    </span>
                  </td>
                  <td className={tdClass}>{p.ifname}</td>
                  <td className={`${tdClass} font-mono text-xs`}>{p.endpoint || "–"}</td>
                  <td className={tdClass}>{p["latest-handshake"] ? ago(p["latest-handshake"]) : "nooit"}</td>
                  <td className={tdClass}>
                    {formatBytes(p["transfer-rx"] ?? 0)} / {formatBytes(p["transfer-tx"] ?? 0)}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </Card>
  );
}

type OVPNSession = {
  description?: string;
  common_name?: string;
  real_address?: string;
  virtual_address?: string;
  connected_since?: string;
  status?: string;
};

function OpenVPNSessions({ n }: { n: OpnFirewall }) {
  const q = useOpnGet<OpnSearch<OVPNSession>>(n.node_id, "openvpn/service/searchSessions");
  const rows = q.data?.rows ?? [];
  return (
    <Card title={`OpenVPN: sessies${q.data ? ` · ${rows.length}` : ""}`}>
      {q.isError ? (
        <p className="text-sm text-slate-500">Geen OpenVPN op deze firewall.</p>
      ) : rows.length === 0 ? (
        <p className="text-sm text-slate-500">Niemand verbonden.</p>
      ) : (
        <ul className="divide-y divide-slate-100 text-sm dark:divide-slate-800">
          {rows.map((r, i) => (
            <li key={i} className="py-1.5">
              <span className="font-medium">{r.common_name || r.description}</span>
              <span className="ml-2 font-mono text-xs text-slate-500">
                {r.real_address} → {r.virtual_address}
              </span>
              {r.connected_since && <span className="block text-xs text-slate-500">sinds {r.connected_since}</span>}
            </li>
          ))}
        </ul>
      )}
    </Card>
  );
}

type IPsecSA = { name?: string; "local-addrs"?: string; "remote-addrs"?: string; connected?: boolean; phase1desc?: string };

function IPsecSessions({ n }: { n: OpnFirewall }) {
  const q = useOpnGet<OpnSearch<IPsecSA>>(n.node_id, "ipsec/sessions/searchPhase1");
  const rows = q.data?.rows ?? [];
  return (
    <Card title={`IPsec: tunnels${q.data ? ` · ${rows.length}` : ""}`}>
      {q.isError ? (
        <p className="text-sm text-slate-500">Geen IPsec op deze firewall.</p>
      ) : rows.length === 0 ? (
        <p className="text-sm text-slate-500">Geen tunnels.</p>
      ) : (
        <ul className="divide-y divide-slate-100 text-sm dark:divide-slate-800">
          {rows.map((r, i) => (
            <li key={i} className="flex items-center justify-between gap-2 py-1.5">
              <span>
                {r.phase1desc || r.name}
                <span className="ml-2 font-mono text-xs text-slate-500">
                  {r["local-addrs"]} ↔ {r["remote-addrs"]}
                </span>
              </span>
              <Badge tone={r.connected ? "green" : "red"}>{r.connected ? "verbonden" : "niet verbonden"}</Badge>
            </li>
          ))}
        </ul>
      )}
    </Card>
  );
}
