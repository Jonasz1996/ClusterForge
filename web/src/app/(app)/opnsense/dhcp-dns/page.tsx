"use client";

import { Suspense } from "react";
import { ItemTiles, type ItemSpec } from "@/components/opnsense/ItemTable";
import { OpnGate } from "@/components/opnsense/bits";
import { PageHeader } from "@/components/ui";

export default function OpnDhcpDnsPage() {
  return (
    <div className="space-y-6">
      <PageHeader
        title="DHCP en DNS"
        description="Unbound-overrides, Kea- en Dnsmasq-reservaties en de leases van OPNsense. Wat je firewall niet gebruikt, staat als niet beschikbaar. Wijzigingen gaan met herstelpunt en terugdraaitimer en daarna naar de backup."
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
