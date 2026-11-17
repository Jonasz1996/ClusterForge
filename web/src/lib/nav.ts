// De navigatie van ClusterForge, in groepen. Dezelfde lijst voedt de zijbalk,
// het menu op mobiel, de zoekfunctie (Ctrl K) en de titel van het tabblad.

// NavTab is een pagina die als tabblad onder een menu-item staat. Zo blijft
// het menu kort: onderwerpen met veel pagina's zijn één item, en de
// tabbladen staan bovenaan de pagina (SectionTabs). Elk tabblad houdt zijn
// eigen adres, zodat oude links blijven werken.
export type NavTab = {
  href: string;
  label: string;
  adminOnly?: boolean;
  keywords?: string;
};

export type NavItem = {
  href: string;
  label: string;
  adminOnly?: boolean;
  // keywords zijn extra zoekwoorden voor Ctrl K.
  keywords?: string;
  // tabs: het eerste tabblad is het item zelf (zelfde href).
  tabs?: NavTab[];
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
      {
        href: "/kaart",
        label: "Kaart",
        keywords: "netwerk vlan map",
        tabs: [
          { href: "/kaart", label: "Netwerk" },
          { href: "/afhankelijkheden", label: "Afhankelijkheden", keywords: "graaf dependencies" },
        ],
      },
    ],
  },
  {
    id: "proxmox",
    label: "Proxmox",
    items: [
      {
        href: "/proxmox/datacenter",
        label: "Datacenter",
        keywords: "pve proxmox quorum corosync capaciteit overcommit onderhoudsmodus host herstarten",
        tabs: [
          { href: "/proxmox/datacenter", label: "Datacenter" },
          { href: "/proxmox/taken", label: "Taken", keywords: "pve task log upid vzdump" },
          { href: "/proxmox/syslog", label: "Syslog", keywords: "pve journal syslog logboek host" },
          { href: "/proxmox/hardware", label: "Hardware", keywords: "pve passthrough gpu pci usb iommu vfio temperatuur ecc herstart" },
        ],
      },
      {
        href: "/proxmox/gasten",
        label: "Gasten",
        keywords: "pve vm ct lxc qemu aanmaken klonen tags cloud-init template snapshot verhuizen",
        tabs: [
          { href: "/proxmox/gasten", label: "Gasten" },
          { href: "/proxmox/hosts", label: "Per host", keywords: "pve hypervisor vm ct lxc qemu pct qm snapshot verhuizen migreren" },
        ],
      },
      { href: "/proxmox/opslag", label: "Opslag", keywords: "pve storage nfs smb cifs pbs zfs lvm schijf smart slijtage iso template opruimen wees" },
      {
        href: "/proxmox/ha",
        label: "HA en replicatie",
        keywords: "pve high availability ha-groep regel affiniteit fencing herstart",
        tabs: [
          { href: "/proxmox/ha", label: "HA" },
          { href: "/proxmox/replicatie", label: "Replicatie", keywords: "pve zfs replicatie storage replication schema" },
        ],
      },
      {
        href: "/proxmox/netwerk",
        label: "Netwerk en firewall",
        keywords: "pve bridge vmbr vlan bond sdn zone vnet subnet interfaces",
        tabs: [
          { href: "/proxmox/netwerk", label: "Netwerk" },
          { href: "/proxmox/firewall", label: "Proxmox-firewall", keywords: "pve firewall regels ipset alias beveiligingsgroep" },
        ],
      },
      {
        href: "/proxmox/toegang",
        label: "Toegang en certificaten",
        keywords: "pve gebruiker groep rechten acl rol api token realm wachtwoord",
        tabs: [
          { href: "/proxmox/toegang", label: "Gebruikers en tokens" },
          { href: "/proxmox/certificaten", label: "Certificaten", keywords: "pve acme lets encrypt ssl tls certificaat dns plugin" },
        ],
      },
      { href: "/proxmox/host-updates", label: "Host-updates", keywords: "pve apt upgrade bijwerken kernel herstart onderhoudsmodus rolling" },
      {
        href: "/pbs",
        label: "Backup Server",
        keywords: "pbs proxmox backup server datastore back-up",
        tabs: [
          { href: "/pbs", label: "Overzicht" },
          { href: "/pbs/datastores", label: "Datastores", keywords: "pbs datastore gc garbage collection prune verify onderhoud" },
          { href: "/pbs/inhoud", label: "Inhoud", keywords: "pbs snapshot groep namespace back-up beschermen notitie verifieer" },
          { href: "/pbs/jobs", label: "Jobs", keywords: "pbs prune verify sync remote gc schema bandbreedte traffic" },
          { href: "/pbs/dekking", label: "Dekking", keywords: "pbs back-up dekking clusternode laatste back-up geverifieerd ontbreekt" },
          { href: "/pbs/taken", label: "Taken", keywords: "pbs task log upid back-up verify gc" },
          { href: "/pbs/toegang", label: "Toegang", adminOnly: true, keywords: "pbs gebruiker token rechten acl rol" },
          { href: "/pbs/systeem", label: "Systeem", keywords: "pbs schijf smart zfs netwerk dns dienst update certificaat syslog herstarten" },
        ],
      },
    ],
  },
  {
    id: "services",
    label: "Diensten",
    items: [
      {
        href: "/docker",
        label: "Docker",
        keywords: "docker containers compose portainer",
        tabs: [
          { href: "/docker", label: "Overzicht" },
          { href: "/docker/containers", label: "Containers", keywords: "docker container start stop herstart logs stats" },
          { href: "/docker/stacks", label: "Stacks", keywords: "docker compose stacks overnemen" },
          { href: "/docker/images", label: "Images", keywords: "docker image pull update registry digest" },
          { href: "/docker/volumes", label: "Volumes", keywords: "docker volume opslag" },
          { href: "/docker/networks", label: "Netwerken", keywords: "docker network subnet bridge macvlan" },
          { href: "/docker/events", label: "Gebeurtenissen", keywords: "docker events crash oom die gestopt" },
          { href: "/docker/cleanup", label: "Opruimen", adminOnly: true, keywords: "docker prune opruimen schijf ruimte" },
        ],
      },
      { href: "/databases", label: "Databases", keywords: "mariadb galera postgresql patroni dump" },
      { href: "/cronjobs", label: "Cronjobs", keywords: "cron rsync planning" },
    ],
  },
  {
    id: "deploy",
    label: "Uitrollen en onderhoud",
    items: [
      {
        href: "/templates",
        label: "Templates",
        keywords: "uitrollen blauwdruk",
        tabs: [
          { href: "/templates", label: "Templates" },
          { href: "/recepten", label: "Recepten", keywords: "stappen" },
        ],
      },
      { href: "/updates", label: "Updates", keywords: "apt upgrade versies pakketten" },
      { href: "/commandos", label: "Commando's", keywords: "shell uitvoeren script" },
      { href: "/gitops", label: "GitOps", keywords: "git github wijzigingen" },
    ],
  },
  {
    id: "security",
    label: "Beveiliging",
    items: [
      { href: "/soc", label: "SOC", keywords: "beveiliging dreiging aanvallers security dashboard score" },
      { href: "/firewall", label: "Firewall", keywords: "ufw poorten" },
      { href: "/crowdsec", label: "CrowdSec", keywords: "bans bouncer alerts" },
      {
        href: "/kwetsbaarheden",
        label: "Kwetsbaarheden",
        keywords:
          "cve kev exploit debian security tracker pakketten lek patch verharding hardening cis lynis ssh sysctl suid scopes scan nmap subnet vlan pentest",
      },
    ],
  },
  {
    id: "watch",
    label: "Bewaking en herstel",
    items: [
      { href: "/monitoring", label: "Monitoring", keywords: "cpu ram grafiek metrics" },
      {
        href: "/zabbix/overzicht",
        label: "Zabbix",
        keywords: "zabbix problemen triggers beschikbaarheid hosts proxy ha dashboard",
        tabs: [
          { href: "/zabbix/overzicht", label: "Overzicht" },
          { href: "/zabbix/dashboards", label: "Dashboards", keywords: "grafana grafieken kiosk tv top cpu geheugen mttr heatmap" },
          { href: "/zabbix/problemen", label: "Problemen", keywords: "triggers alerts ernst" },
          { href: "/zabbix/hosts", label: "Hosts", keywords: "beschikbaarheid koppelen" },
          { href: "/zabbix", label: "Agents", keywords: "zabbix-agents agent2 psk sleutel" },
        ],
      },
      { href: "/logs", label: "Logs", adminOnly: true, keywords: "journald syslog docker logs" },
      {
        href: "/incidenten",
        label: "Incidenten",
        keywords: "storing problemen",
        tabs: [
          { href: "/incidenten", label: "Incidenten" },
          { href: "/tijdlijn", label: "Tijdlijn", keywords: "wijzigingen wat veranderde" },
        ],
      },
      {
        href: "/rapporten",
        label: "Rapporten",
        keywords: "scorecard pdf",
        tabs: [
          { href: "/rapporten", label: "Rapporten" },
          { href: "/risico", label: "Risico", keywords: "spof single point of failure" },
        ],
      },
      {
        href: "/back-ups",
        label: "Back-ups",
        keywords: "backup restore controle",
        tabs: [
          { href: "/back-ups", label: "Controle" },
          { href: "/proxmox/backups", label: "Proxmox", keywords: "pve vzdump pbs proxmox backup server terugzetten restore terugzettest retentie" },
        ],
      },
      { href: "/tests", label: "Tests", keywords: "failover disaster recovery dr" },
    ],
  },
  {
    id: "admin",
    label: "Beheer",
    items: [
      { href: "/taken", label: "Taken", keywords: "jobs voortgang" },
      { href: "/documentatie", label: "Documentatie", keywords: "docs markdown" },
      { href: "/assistent", label: "Assistent", keywords: "ai vraag" },
      { href: "/logboek", label: "Logboek", adminOnly: true, keywords: "audit wie wat" },
      {
        href: "/instellingen",
        label: "Instellingen",
        keywords: "wachtwoord totp gebruikers sessies thema api-tokens",
        tabs: [
          { href: "/instellingen", label: "Instellingen" },
          { href: "/proxmox", label: "Proxmox-koppeling", keywords: "pve proxmox token api koppeling" },
        ],
      },
    ],
  },
];

// Detailpagina's die bij een menu-item horen maar niet onder zijn pad staan.
const parentOf: Record<string, string> = {
  "/proxmox/gast": "/proxmox/gasten",
  "/proxmox/console": "/proxmox/gasten",
  "/docker/container": "/docker/containers",
  "/docker/console": "/docker/containers",
  // De stacks van één cluster (fase 21) horen onder Docker → Stacks.
  "/containers": "/docker/stacks",
};

// Elke pagina (item of tabblad) met het menu-item waar ze onder valt.
type Entry = { href: string; label: string; item: NavItem; group: NavGroup; tab?: NavTab };

function entries(): Entry[] {
  const out: Entry[] = [];
  for (const g of navGroups) {
    for (const i of g.items) {
      out.push({ href: i.href, label: i.label, item: i, group: g });
      for (const t of i.tabs ?? []) {
        if (t.href !== i.href) out.push({ href: t.href, label: t.label, item: i, group: g, tab: t });
      }
    }
  }
  return out;
}

// entryFor is de pagina die bij een pad hoort: de langste href die past,
// zodat /proxmox/hosts niet ook de Proxmox-koppeling (/proxmox) oplicht en
// /clusters/detail bij Clusters hoort.
function entryFor(pathname: string): Entry | null {
  const path = pathname.replace(/\/$/, "") || "/";
  const parent = parentOf[path];
  const p = parent ?? path;
  let best: Entry | null = null;
  for (const e of entries()) {
    if (p === e.href || p.startsWith(e.href + "/")) {
      if (!best || e.href.length > best.href.length) best = e;
    }
  }
  return best;
}

// allNavItems is alles wat Ctrl K vindt: de menu-items en hun tabbladen.
export function allNavItems(isAdmin: boolean): (NavItem & { group: string })[] {
  const out: (NavItem & { group: string })[] = [{ ...navHome, group: "" }];
  for (const g of navGroups) {
    for (const i of g.items) {
      if (i.adminOnly && !isAdmin) continue;
      out.push({ href: i.href, label: i.label, keywords: i.keywords, adminOnly: i.adminOnly, group: g.label });
      for (const t of i.tabs ?? []) {
        if (t.href === i.href || (t.adminOnly && !isAdmin)) continue;
        out.push({ href: t.href, label: `${i.label} › ${t.label}`, keywords: t.keywords, group: g.label });
      }
    }
  }
  return out;
}

// activeHref is het menu-item dat bij een pad hoort (bij een tabblad: zijn
// item).
export function activeHref(pathname: string): string | null {
  if (pathname === "/" || pathname === "") return "/";
  return entryFor(pathname)?.item.href ?? null;
}

// sectionTabs geeft de tabbladen van het menu-item van deze pagina, en welk
// tabblad nu open is; null als het item geen tabbladen heeft.
export function sectionTabs(pathname: string, isAdmin: boolean): { item: NavItem; tabs: NavTab[]; active: string } | null {
  if (pathname === "/" || pathname === "") return null;
  const e = entryFor(pathname);
  if (!e?.item.tabs) return null;
  return { item: e.item, tabs: e.item.tabs.filter((t) => !t.adminOnly || isAdmin), active: e.tab?.href ?? e.item.href };
}

// itemHrefs zijn de adressen die bij een menu-item horen (voor de tellers).
export function itemHrefs(i: NavItem): string[] {
  return i.tabs ? i.tabs.map((t) => t.href) : [i.href];
}

export function titleFor(pathname: string): string {
  if (pathname === "/" || pathname === "") return navHome.label;
  const e = entryFor(pathname);
  if (!e) return "";
  return e.tab ? `${e.tab.label} · ${e.item.label}` : e.item.label;
}

export function groupOf(href: string | null): string | null {
  if (!href) return null;
  return navGroups.find((g) => g.items.some((i) => i.href === href))?.id ?? null;
}
