"use client";

import { Suspense } from "react";
import { ifaceOptions, ItemTiles, type ItemSpec } from "@/components/opnsense/ItemTable";
import { OpnGate } from "@/components/opnsense/bits";
import { PageHeader } from "@/components/ui";

export default function OpnDhcpDnsPage() {
  return (
    <div className="space-y-6">
      <PageHeader
        title="DHCP en DNS"
        description="Unbound-overrides en doorsturen (DNS over TLS), Kea-subnets, Dnsmasq-bereiken, reservaties en de leases van OPNsense. Wat je firewall niet gebruikt, staat als niet beschikbaar. Wijzigingen gaan met herstelpunt en terugdraaitimer en daarna naar de backup."
      />
      <Suspense>
        <OpnGate>{(c) => <ItemTiles key={c.key} c={c} specs={specs} />}</OpnGate>
      </Suspense>
    </div>
  );
}

const specs: ItemSpec[] = [
  {
    title: "Unbound: host-overrides",
    search: "unbound/settings/searchHostOverride",
    base: "unbound/settings",
    x: "HostOverride",
    root: "host",
    what: "DNS-override",
    name: "hostname",
    cols: [
      ["hostname", "Host"],
      ["domain", "Domein"],
      ["rr", "Type"],
      ["server", "Adres"],
      ["description", "Omschrijving"],
    ],
    fields: () => [
      { key: "enabled", label: "Aan", kind: "check" },
      { key: "hostname", label: "Host", hint: "* voor het hele domein" },
      { key: "domain", label: "Domein" },
      {
        key: "rr",
        label: "Type",
        kind: "select",
        options: [
          ["A", "A (IPv4)"],
          ["AAAA", "AAAA (IPv6)"],
          ["MX", "MX"],
        ],
      },
      { key: "server", label: "Adres" },
      { key: "description", label: "Omschrijving", wide: true },
    ],
    defaults: { enabled: "1", rr: "A" },
  },
  {
    title: "Unbound: domein-overrides",
    search: "unbound/settings/searchDomainOverride",
    base: "unbound/settings",
    x: "DomainOverride",
    root: "domain",
    what: "domein-override",
    name: "domain",
    cols: [
      ["domain", "Domein"],
      ["server", "Server"],
      ["description", "Omschrijving"],
    ],
    fields: () => [
      { key: "enabled", label: "Aan", kind: "check" },
      { key: "domain", label: "Domein" },
      { key: "server", label: "Server", hint: "Bijvoorbeeld 192.168.10.10 of 192.168.10.10@5353" },
      { key: "description", label: "Omschrijving", wide: true },
    ],
    defaults: { enabled: "1" },
  },
  {
    title: "Kea: DHCPv4-subnets",
    search: "kea/dhcpv4/searchSubnet",
    base: "kea/dhcpv4",
    x: "Subnet",
    root: "subnet4",
    what: "Kea-subnet",
    name: "subnet",
    toggle: false,
    cols: [
      ["subnet", "Subnet"],
      ["pools", "Bereik"],
      ["description", "Omschrijving"],
    ],
    fields: () => [
      { key: "subnet", label: "Subnet", hint: "Zoals 192.168.20.0/24" },
      { key: "pools", label: "Bereik", hint: "Zoals 192.168.20.100-192.168.20.200" },
      { key: "option_data_autocollect", label: "Router en DNS vanzelf", kind: "check" },
      { key: "option_data.routers", label: "Router", hint: "Alleen als vanzelf uit staat" },
      { key: "option_data.domain_name_servers", label: "DNS-servers", hint: "Met komma's" },
      { key: "description", label: "Omschrijving", wide: true },
    ],
    defaults: { option_data_autocollect: "1" },
    hint: "Vergeet niet Kea aan te zetten op de interface (Diensten → Kea in OPNsense-termen: dhcpv4.general).",
  },
  {
    title: "Dnsmasq: DHCP-bereiken",
    search: "dnsmasq/settings/searchRange",
    base: "dnsmasq/settings",
    x: "Range",
    root: "range",
    what: "DHCP-bereik",
    name: "start_addr",
    toggle: false,
    cols: [
      ["interface", "Interface"],
      ["start_addr", "Van"],
      ["end_addr", "Tot"],
      ["description", "Omschrijving"],
    ],
    fields: (n, r) => [
      { key: "interface", label: "Interface", kind: "select", options: ifaceOptions(n, r?.interface) },
      { key: "start_addr", label: "Van" },
      { key: "end_addr", label: "Tot" },
      { key: "lease_time", label: "Leasetijd (s)" },
      { key: "domain", label: "Domein" },
      { key: "description", label: "Omschrijving", wide: true },
    ],
  },
  {
    title: "Unbound: doorsturen (DNS over TLS)",
    search: "unbound/settings/searchForward",
    base: "unbound/settings",
    x: "Forward",
    root: "dot",
    what: "DNS-doorsturing",
    name: "server",
    cols: [
      ["type", "Soort"],
      ["domain", "Domein"],
      ["server", "Server"],
      ["port", "Poort"],
      ["verify", "TLS-naam"],
    ],
    fields: () => [
      { key: "enabled", label: "Aan", kind: "check" },
      {
        key: "type",
        label: "Soort",
        kind: "select",
        options: [
          ["dot", "DNS over TLS"],
          ["forward", "Gewoon doorsturen"],
        ],
      },
      { key: "domain", label: "Domein", hint: "Leeg voor alles" },
      { key: "server", label: "Server", hint: "Zoals 9.9.9.9" },
      { key: "port", label: "Poort", hint: "853 voor TLS" },
      { key: "verify", label: "TLS-naam", hint: "Zoals dns.quad9.net" },
      { key: "description", label: "Omschrijving", wide: true },
    ],
    defaults: { enabled: "1", type: "dot", port: "853" },
  },
  {
    title: "Kea: DHCPv4-reservaties",
    search: "kea/dhcpv4/searchReservation",
    base: "kea/dhcpv4",
    x: "Reservation",
    root: "reservation",
    what: "DHCP-reservatie",
    name: "hostname",
    toggle: false,
    cols: [
      ["subnet", "Subnet"],
      ["ip_address", "Adres"],
      ["hw_address", "MAC"],
      ["hostname", "Host"],
      ["description", "Omschrijving"],
    ],
    fields: () => [
      { key: "subnet", label: "Subnet (uuid of net)", hint: "Zoals Kea het subnet kent" },
      { key: "ip_address", label: "Adres" },
      { key: "hw_address", label: "MAC-adres" },
      { key: "hostname", label: "Host" },
      { key: "description", label: "Omschrijving", wide: true },
    ],
  },
  {
    title: "Kea: leases",
    search: "kea/leases4/search",
    base: "kea/leases4",
    x: "",
    root: "",
    what: "lease",
    name: "address",
    readonly: true,
    cols: [
      ["address", "Adres"],
      ["hwaddr", "MAC"],
      ["hostname", "Host"],
      ["if_descr", "Interface"],
    ],
  },
  {
    title: "Dnsmasq: hosts",
    search: "dnsmasq/settings/searchHost",
    base: "dnsmasq/settings",
    x: "Host",
    root: "host",
    what: "Dnsmasq-host",
    name: "host",
    toggle: false,
    cols: [
      ["host", "Host"],
      ["domain", "Domein"],
      ["ip", "Adres"],
      ["hwaddr", "MAC"],
      ["descr", "Omschrijving"],
    ],
    fields: () => [
      { key: "host", label: "Host" },
      { key: "domain", label: "Domein" },
      { key: "ip", label: "Adres" },
      { key: "hwaddr", label: "MAC-adres (voor DHCP)" },
      { key: "descr", label: "Omschrijving", wide: true },
    ],
  },
  {
    title: "Dnsmasq: leases",
    search: "dnsmasq/leases/search",
    base: "dnsmasq/leases",
    x: "",
    root: "",
    what: "lease",
    name: "address",
    readonly: true,
    cols: [
      ["address", "Adres"],
      ["hwaddr", "MAC"],
      ["hostname", "Host"],
      ["if_descr", "Interface"],
    ],
  },
];
