"use client";

import { Suspense } from "react";
import { ItemTiles, type ItemSpec } from "@/components/opnsense/ItemTable";
import { OpnGate } from "@/components/opnsense/bits";
import { PageHeader } from "@/components/ui";

export default function OpnSecurityPage() {
  return (
    <div className="space-y-6">
      <PageHeader
        title="Gebruikers, certificaten en VPN"
        description="Gebruikers en groepen van de firewall, certificaten en hun autoriteiten, IPsec-verbindingen en hun sleutels, het IDS/IPS-beleid en de traffic shaper. De gebruikers root en clusterforge en de groep admins raakt ClusterForge nooit. Wachtwoorden, privésleutels en IPsec-sleutels gaan versleuteld in de taak en komen nooit in de uitvoer. Elke wijziging gaat met herstelpunt en terugdraaitimer en daarna naar de backup."
      />
      <Suspense>
        <OpnGate>{(c) => <ItemTiles key={c.key} c={c} specs={specs} />}</OpnGate>
      </Suspense>
    </div>
  );
}

const specs: ItemSpec[] = [
  {
    title: "Gebruikers",
    search: "system/user/search",
    base: "system/user",
    x: "",
    root: "user",
    what: "gebruiker",
    name: "name",
    toggle: false,
    cols: [
      ["name", "Naam"],
      ["descr", "Volledige naam"],
      ["email", "E-mail"],
      ["group_memberships", "Groepen"],
      ["disabled", "Uit"],
    ],
    fields: () => [
      { key: "name", label: "Gebruikersnaam" },
      { key: "password", label: "Wachtwoord", hint: "Leeg laten om het te houden" },
      { key: "descr", label: "Volledige naam" },
      { key: "email", label: "E-mail" },
      { key: "group_memberships", label: "Groepen", hint: "Met komma's, zoals admins" },
      { key: "disabled", label: "Uitgeschakeld", kind: "check" },
      { key: "authorizedkeys", label: "SSH-sleutels", kind: "textarea", wide: true },
    ],
    hint: "root en clusterforge beheer je op de firewall zelf.",
  },
  {
    title: "Groepen",
    search: "system/group/search",
    base: "system/group",
    x: "",
    root: "group",
    what: "groep",
    name: "name",
    toggle: false,
    cols: [
      ["name", "Naam"],
      ["description", "Omschrijving"],
      ["member", "Leden"],
    ],
    fields: () => [
      { key: "name", label: "Naam" },
      { key: "description", label: "Omschrijving" },
      { key: "member", label: "Leden", hint: "Met komma's" },
      { key: "priv", label: "Rechten", hint: "Met komma's, zoals page-all" },
    ],
  },
  {
    title: "Certificaten",
    search: "trust/cert/search",
    base: "trust/cert",
    x: "",
    root: "cert",
    what: "certificaat",
    name: "descr",
    toggle: false,
    cols: [
      ["descr", "Naam"],
      ["commonname", "Common name"],
      ["caref", "Uitgever"],
      ["valid_to", "Geldig tot"],
      ["in_use", "In gebruik"],
    ],
    fields: () => [
      { key: "action", label: "Soort", kind: "select", options: [["import", "Importeren"], ["internal", "Zelf maken (met een CA)"]] },
      { key: "descr", label: "Naam" },
      { key: "caref", label: "CA (bij zelf maken)", hint: "De refid van de CA" },
      { key: "commonname", label: "Common name" },
      { key: "altnames_dns", label: "Andere namen (DNS)", hint: "Met komma's" },
      { key: "crt_payload", label: "Certificaat (PEM, bij importeren)", kind: "textarea", wide: true },
      { key: "prv_payload", label: "Privésleutel (PEM, bij importeren)", kind: "textarea", wide: true },
    ],
    defaults: { action: "import" },
    hint: "Een certificaat dat de webinterface gebruikt, verwijder je niet.",
  },
  {
    title: "Certificaatautoriteiten",
    search: "trust/ca/search",
    base: "trust/ca",
    x: "",
    root: "ca",
    what: "certificaatautoriteit",
    name: "descr",
    toggle: false,
    cols: [
      ["descr", "Naam"],
      ["commonname", "Common name"],
      ["valid_to", "Geldig tot"],
    ],
    fields: () => [
      { key: "action", label: "Soort", kind: "select", options: [["internal", "Zelf maken"], ["existing", "Importeren"]] },
      { key: "descr", label: "Naam" },
      { key: "commonname", label: "Common name" },
      { key: "lifetime", label: "Geldig (dagen)" },
      { key: "crt_payload", label: "Certificaat (PEM, bij importeren)", kind: "textarea", wide: true },
      { key: "prv_payload", label: "Privésleutel (PEM, bij importeren)", kind: "textarea", wide: true },
    ],
    defaults: { action: "internal", lifetime: "3650" },
  },
  {
    title: "IPsec-verbindingen",
    search: "ipsec/connections/searchConnection",
    base: "ipsec/connections",
    x: "Connection",
    root: "connection",
    what: "IPsec-verbinding",
    name: "description",
    cols: [
      ["description", "Naam"],
      ["local_addrs", "Hier"],
      ["remote_addrs", "Andere kant"],
      ["version", "IKE"],
    ],
    fields: () => [
      { key: "enabled", label: "Aan", kind: "check" },
      { key: "description", label: "Naam" },
      { key: "version", label: "IKE-versie", kind: "select", options: [["2", "IKEv2"], ["1", "IKEv1"], ["0", "Beide"]] },
      { key: "local_addrs", label: "Eigen adres" },
      { key: "remote_addrs", label: "Adres van de andere kant" },
      { key: "proposals", label: "Voorstellen", hint: "default of zoals aes256-sha256-modp2048" },
    ],
    defaults: { enabled: "1", version: "2", proposals: "default" },
    hint: "Eigen en andere authenticatie en de tunnels (children) voeg je per verbinding toe; de sleutels hieronder.",
  },
  {
    title: "IPsec-tunnels (children)",
    search: "ipsec/connections/searchChild",
    base: "ipsec/connections",
    x: "Child",
    root: "child",
    what: "IPsec-tunnel",
    name: "description",
    cols: [
      ["description", "Naam"],
      ["connection", "Verbinding"],
      ["local_ts", "Eigen net"],
      ["remote_ts", "Net aan de andere kant"],
    ],
    fields: () => [
      { key: "enabled", label: "Aan", kind: "check" },
      { key: "connection", label: "Verbinding (uuid)" },
      { key: "description", label: "Naam" },
      { key: "local_ts", label: "Eigen net", hint: "Zoals 192.168.10.0/24" },
      { key: "remote_ts", label: "Net aan de andere kant" },
    ],
    defaults: { enabled: "1" },
  },
  {
    title: "IPsec-sleutels",
    search: "ipsec/pre_shared_keys/searchItem",
    base: "ipsec/pre_shared_keys",
    x: "Item",
    root: "preSharedKey",
    what: "IPsec-sleutel",
    name: "ident",
    toggle: false,
    cols: [
      ["ident", "Eigen id"],
      ["remote_ident", "Id andere kant"],
      ["keyType", "Soort"],
      ["description", "Omschrijving"],
    ],
    fields: () => [
      { key: "ident", label: "Eigen id" },
      { key: "remote_ident", label: "Id andere kant" },
      { key: "keyType", label: "Soort", kind: "select", options: [["PSK", "Gedeelde sleutel"], ["EAP", "EAP"]] },
      { key: "Key", label: "Sleutel" },
      { key: "description", label: "Omschrijving", wide: true },
    ],
    defaults: { keyType: "PSK" },
  },
  {
    title: "IDS/IPS-beleid",
    search: "ids/settings/searchPolicy",
    base: "ids/settings",
    x: "Policy",
    root: "policy",
    what: "IDS-beleid",
    name: "description",
    cols: [
      ["description", "Naam"],
      ["prio", "Prioriteit"],
      ["action", "Bij treffer"],
      ["new_action", "Wordt"],
    ],
    fields: () => [
      { key: "enabled", label: "Aan", kind: "check" },
      { key: "description", label: "Naam" },
      { key: "prio", label: "Prioriteit" },
      { key: "action", label: "Bij treffer (alert, drop, …)", hint: "Met komma's" },
      { key: "new_action", label: "Wordt", kind: "select", options: [["default", "Standaard"], ["alert", "Melden"], ["drop", "Tegenhouden"], ["disable", "Uitzetten"]] },
      { key: "rulesets", label: "Regelsets", hint: "Met komma's" },
    ],
    defaults: { enabled: "1", new_action: "alert" },
    hint: "Drop houdt verkeer tegen als IPS aanstaat; begin met melden.",
  },
  {
    title: "Traffic shaper: pipes",
    search: "trafficshaper/settings/searchPipes",
    base: "trafficshaper/settings",
    x: "Pipe",
    root: "pipe",
    what: "shaper-pipe",
    name: "description",
    cols: [
      ["description", "Naam"],
      ["bandwidth", "Bandbreedte"],
      ["bandwidthMetric", "Eenheid"],
    ],
    fields: () => [
      { key: "enabled", label: "Aan", kind: "check" },
      { key: "description", label: "Naam" },
      { key: "bandwidth", label: "Bandbreedte" },
      { key: "bandwidthMetric", label: "Eenheid", kind: "select", options: [["Mbit", "Mbit/s"], ["Kbit", "Kbit/s"], ["Gbit", "Gbit/s"]] },
    ],
    defaults: { enabled: "1", bandwidthMetric: "Mbit" },
  },
];
