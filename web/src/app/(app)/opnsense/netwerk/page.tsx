"use client";

import { Suspense } from "react";
import { ifaceOptions, ItemTiles, type ItemSpec } from "@/components/opnsense/ItemTable";
import { OpnGate } from "@/components/opnsense/bits";
import { PageHeader } from "@/components/ui";

export default function OpnNetworkPage() {
  return (
    <div className="space-y-6">
      <PageHeader
        title="Netwerk"
        description="Virtuele IP's (CARP en IP-alias), VLAN's, gateways en statische routes van OPNsense. Elke wijziging hier kan de toegang raken: ze gaat met herstelpunt, terugdraaitimer en proeven, en je tikt de naam van het cluster over. Interfaces toewijzen blijft in OPNsense zelf; dat heeft geen API."
      />
      <Suspense>
        <OpnGate>{(c) => <ItemTiles key={c.key} c={c} specs={specs} />}</OpnGate>
      </Suspense>
    </div>
  );
}

const specs: ItemSpec[] = [
  {
    title: "Virtuele IP's",
    search: "interfaces/vip_settings/searchItem",
    base: "interfaces/vip_settings",
    x: "Item",
    root: "vip",
    what: "VIP",
    name: "subnet",
    toggle: false,
    hint: "Een CARP-VIP verandert op beide firewalls via de sync; de backup krijgt automatisch zijn eigen skew.",
    cols: [
      ["interface", "Interface"],
      ["mode", "Soort"],
      ["subnet", "Adres"],
      ["subnet_bits", "Bits"],
      ["vhid", "VHID"],
      ["descr", "Omschrijving"],
    ],
    fields: (n, r) => [
      {
        key: "mode",
        label: "Soort",
        kind: "select",
        options: [
          ["carp", "CARP"],
          ["ipalias", "IP-alias"],
          ["proxyarp", "Proxy ARP"],
          ["other", "Anders"],
        ],
      },
      { key: "interface", label: "Interface", kind: "select", options: ifaceOptions(n, r?.interface) },
      { key: "subnet", label: "Adres" },
      { key: "subnet_bits", label: "Prefix (bits)" },
      { key: "vhid", label: "VHID", hint: "Uniek per netwerk, 1-255" },
      { key: "advbase", label: "Advertentie-interval (s)" },
      { key: "advskew", label: "Skew", hint: "0 op de master; de backup hoger" },
      { key: "password", label: "Wachtwoord (CARP)", hint: "Leeg laten om het te houden" },
      { key: "descr", label: "Omschrijving", wide: true },
    ],
    defaults: { mode: "carp", subnet_bits: "24", advbase: "1", advskew: "0" },
  },
  {
    title: "VLAN's",
    search: "interfaces/vlan_settings/searchItem",
    base: "interfaces/vlan_settings",
    x: "Item",
    root: "vlan",
    what: "VLAN",
    name: "vlanif",
    toggle: false,
    cols: [
      ["vlanif", "Apparaat"],
      ["if", "Op"],
      ["tag", "Tag"],
      ["pcp", "Prioriteit"],
      ["descr", "Omschrijving"],
    ],
    fields: () => [
      { key: "if", label: "Fysieke interface", hint: "Bijvoorbeeld vtnet1" },
      { key: "tag", label: "Tag", hint: "1-4094" },
      { key: "pcp", label: "Prioriteit (PCP)" },
      { key: "descr", label: "Omschrijving", wide: true },
    ],
    defaults: { pcp: "0" },
  },
  {
    title: "Gateways",
    search: "routing/settings/searchGateway",
    base: "routing/settings",
    x: "Gateway",
    root: "gateway_item",
    what: "gateway",
    name: "name",
    cols: [
      ["name", "Naam"],
      ["interface", "Interface"],
      ["gateway", "Adres"],
      ["monitor", "Monitor"],
      ["defaultgw", "Standaard"],
      ["descr", "Omschrijving"],
    ],
    fields: (n, r) => [
      { key: "name", label: "Naam" },
      { key: "interface", label: "Interface", kind: "select", options: ifaceOptions(n, r?.interface) },
      {
        key: "ipprotocol",
        label: "IP-versie",
        kind: "select",
        options: [
          ["inet", "IPv4"],
          ["inet6", "IPv6"],
        ],
      },
      { key: "gateway", label: "Adres", hint: "Leeg of dynamic bij DHCP/PPPoE" },
      { key: "monitor", label: "Monitoradres", hint: "Bijvoorbeeld 1.1.1.1" },
      { key: "defaultgw", label: "Standaardgateway", kind: "check" },
      { key: "monitor_disable", label: "Niet monitoren", kind: "check" },
      { key: "descr", label: "Omschrijving", wide: true },
    ],
    defaults: { ipprotocol: "inet" },
  },
  {
    title: "Statische routes",
    search: "routes/routes/searchroute",
    base: "routes/routes",
    x: "route",
    root: "route",
    what: "statische route",
    name: "network",
    cols: [
      ["network", "Netwerk"],
      ["gateway", "Gateway"],
      ["descr", "Omschrijving"],
    ],
    fields: () => [
      { key: "network", label: "Netwerk", hint: "Bijvoorbeeld 10.20.0.0/16" },
      { key: "gateway", label: "Gateway", hint: "Naam van de gateway" },
      { key: "descr", label: "Omschrijving", wide: true },
    ],
  },
];
