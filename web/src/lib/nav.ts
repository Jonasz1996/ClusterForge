// De navigatie van ClusterForge, in groepen. Dezelfde lijst voedt de zijbalk,
// het menu op mobiel, de zoekfunctie (Ctrl K) en de titel van het tabblad.

export type NavItem = {
  href: string;
  label: string;
  adminOnly?: boolean;
  // keywords zijn extra zoekwoorden voor Ctrl K.
  keywords?: string;
};

export type NavGroup = { id: string; label: string; items: NavItem[] };

export const navHome: NavItem = { href: "/", label: "Overzicht", keywords: "dashboard start home aandacht" };

export const navGroups: NavGroup[] = [
  {
    id: "infra",
    label: "Infrastructuur",
    items: [
      { href: "/clusters", label: "Clusters", keywords: "vip keepalived uitrollen" },
      { href: "/nodes", label: "Nodes", keywords: "servers agents machines ct lxc vm" },
      { href: "/kubernetes", label: "Kubernetes", keywords: "k3s pods" },
      { href: "/kaart", label: "Kaart", keywords: "netwerk vlan map" },
      { href: "/afhankelijkheden", label: "Afhankelijkheden", keywords: "graaf dependencies" },
    ],
  },
  {
    id: "proxmox",
    label: "Proxmox",
    items: [
      { href: "/proxmox/datacenter", label: "Datacenter", keywords: "pve proxmox quorum corosync capaciteit overcommit onderhoudsmodus host herstarten" },
      { href: "/proxmox/gasten", label: "Gasten", keywords: "pve vm ct lxc qemu aanmaken klonen tags cloud-init template snapshot verhuizen" },
      { href: "/proxmox/backups", label: "Back-ups", keywords: "pve vzdump pbs proxmox backup server terugzetten restore terugzettest retentie" },
      { href: "/proxmox/opslag", label: "Opslag", keywords: "pve storage nfs smb cifs pbs zfs lvm schijf smart slijtage iso template opruimen wees" },
      { href: "/proxmox/ha", label: "HA", keywords: "pve high availability ha-groep regel affiniteit fencing herstart" },
      { href: "/proxmox/replicatie", label: "Replicatie", keywords: "pve zfs replicatie storage replication schema" },
      { href: "/proxmox/host-updates", label: "Host-updates", keywords: "pve apt upgrade bijwerken kernel herstart onderhoudsmodus rolling" },
      { href: "/proxmox/hardware", label: "Hardware", keywords: "pve passthrough gpu pci usb iommu vfio temperatuur ecc herstart" },
      { href: "/proxmox/hosts", label: "Hosts en gasten", keywords: "pve hypervisor vm ct lxc qemu pct qm snapshot verhuizen migreren" },
      { href: "/proxmox/taken", label: "Proxmox-taken", keywords: "pve task log upid vzdump" },
      { href: "/proxmox", label: "Via de API-koppeling", keywords: "pve proxmox token api koppeling" },
    ],
  },
  {
    id: "software",
    label: "Software",
    items: [
      { href: "/databases", label: "Databases", keywords: "mariadb galera postgresql patroni dump" },
      { href: "/containers", label: "Containers", keywords: "docker compose stacks" },
      { href: "/cronjobs", label: "Cronjobs", keywords: "cron rsync planning" },
      { href: "/updates", label: "Updates", keywords: "apt upgrade versies pakketten" },
      { href: "/commandos", label: "Commando's", keywords: "shell uitvoeren script" },
      { href: "/templates", label: "Templates", keywords: "uitrollen blauwdruk" },
      { href: "/recepten", label: "Recepten", keywords: "stappen" },
    ],
  },
  {
    id: "security",
    label: "Beveiliging",
    items: [
      { href: "/soc", label: "SOC", keywords: "beveiliging dreiging aanvallers security dashboard score" },
      { href: "/firewall", label: "Firewall", keywords: "ufw poorten" },
      { href: "/crowdsec", label: "CrowdSec", keywords: "bans bouncer alerts" },
    ],
  },
  {
    id: "watch",
    label: "Bewaking",
    items: [
      { href: "/monitoring", label: "Monitoring", keywords: "cpu ram grafiek metrics" },
      { href: "/logs", label: "Logs", adminOnly: true, keywords: "journald syslog docker logs" },
      { href: "/incidenten", label: "Incidenten", keywords: "storing problemen" },
      { href: "/risico", label: "Risico", keywords: "spof single point of failure" },
      { href: "/rapporten", label: "Rapporten", keywords: "scorecard pdf" },
    ],
  },
  {
    id: "recovery",
    label: "Herstel",
    items: [
      { href: "/back-ups", label: "Back-ups", keywords: "backup restore controle vzdump" },
      { href: "/tests", label: "Tests", keywords: "failover disaster recovery dr" },
    ],
  },
  {
    id: "admin",
    label: "Beheer",
    items: [
      { href: "/taken", label: "Taken", keywords: "jobs voortgang" },
      { href: "/gitops", label: "GitOps", keywords: "git github wijzigingen" },
      { href: "/documentatie", label: "Documentatie", keywords: "docs markdown" },
      { href: "/assistent", label: "Assistent", keywords: "ai vraag" },
      { href: "/logboek", label: "Logboek", adminOnly: true, keywords: "audit wie wat" },
      { href: "/instellingen", label: "Instellingen", keywords: "wachtwoord totp gebruikers sessies thema api-tokens" },
    ],
  },
];

// Pagina's die niet in de zijbalk staan maar wel een eigen titel hebben.
const extraTitles: { href: string; label: string }[] = [{ href: "/tijdlijn", label: "Tijdlijn" }];

// Detailpagina's die bij een menu-item horen maar niet onder zijn pad staan.
const parentOf: Record<string, string> = { "/proxmox/gast": "/proxmox/gasten" };

export function allNavItems(isAdmin: boolean): (NavItem & { group: string })[] {
  const out: (NavItem & { group: string })[] = [{ ...navHome, group: "" }];
  for (const g of navGroups) {
    for (const i of g.items) {
      if (!i.adminOnly || isAdmin) out.push({ ...i, group: g.label });
    }
  }
  return out;
}

// activeHref is het menu-item dat bij een pad hoort: de langste href die
// past, zodat /proxmox/hosts niet ook Proxmox oplicht en
// /clusters/detail bij Clusters hoort.
export function activeHref(pathname: string): string | null {
  if (pathname === "/" || pathname === "") return "/";
  const parent = parentOf[pathname.replace(/\/$/, "")];
  if (parent) return parent;
  let best: string | null = null;
  const all = [...navGroups.flatMap((g) => g.items), ...extraTitles];
  for (const i of all) {
    if (pathname === i.href || pathname.startsWith(i.href + "/")) {
      if (!best || i.href.length > best.length) best = i.href;
    }
  }
  return best;
}

export function titleFor(pathname: string): string {
  const href = activeHref(pathname);
  if (href === "/") return navHome.label;
  const all = [...navGroups.flatMap((g) => g.items), ...extraTitles];
  return all.find((i) => i.href === href)?.label ?? "";
}

export function groupOf(href: string | null): string | null {
  if (!href) return null;
  return navGroups.find((g) => g.items.some((i) => i.href === href))?.id ?? null;
}
