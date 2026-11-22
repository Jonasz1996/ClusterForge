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
  // needs: het item staat alleen in het menu als dit onderdeel in gebruik
  // is (fase 47). Zoeken en "Alles tonen" vinden het altijd.
  needs?: UsageKey;
};

// UsageKey is een veld van GET /nav/usage.
export type UsageKey =
  | "kubernetes"
  | "gitops"
  | "dependencies"
  | "proxmox"
  | "docker"
  | "pbs"
  | "truenas"
  | "opnsense"
  | "zabbix"
  | "aiverslag";

export type NavUsage = Partial<Record<UsageKey, boolean>>;

export type NavGroup = { id: string; label: string; items: NavItem[] };

// NavPlatform is een platform met zijn eigen onderdelen (fase 47): het
// staat als tabblad onder Platformen in plaats van als groep in het menu.
export type NavPlatform = { id: string; label: string; needs: UsageKey; items: NavItem[] };

// inUse: zonder gegevens over gebruik (nog laden) telt alles als in gebruik
// behalve wat een onderdeel nodig heeft.
export function inUse(i: { needs?: UsageKey }, usage: NavUsage | undefined): boolean {
  return !i.needs || !!usage?.[i.needs];
}

export const navHome: NavItem = {
  href: "/",
  label: "Wat moet ik doen",
  keywords: "overzicht dashboard start home aandacht problemen oorzaken",
};

export const navGroups: NavGroup[] = [
  {
    id: "overzicht",
    label: "Overzicht",
    items: [
      navHome,
      {
        href: "/incidenten",
        label: "Incidenten",
        keywords: "storing problemen",
        tabs: [
          { href: "/incidenten", label: "Incidenten" },
          {
            href: "/tijdlijn",
            label: "Tijdlijn",
            keywords: "wijzigingen wat veranderde",
          },
        ],
      },
      {
        href: "/monitoring",
        label: "Monitoring",
        keywords: "cpu ram grafiek metrics",
      },
      {
        href: "/logs",
        label: "Logs",
        adminOnly: true,
        keywords: "journald syslog docker logs",
      },
      {
        href: "/rapporten",
        label: "Rapporten",
        keywords: "scorecard pdf",
        tabs: [
          { href: "/rapporten", label: "Rapporten" },
          {
            href: "/risico",
            label: "Risico",
            keywords: "spof single point of failure",
          },
        ],
      },
      {
        href: "/zabbix/overzicht",
        label: "Zabbix", needs: "zabbix",
        keywords:
          "zabbix problemen triggers beschikbaarheid hosts proxy ha dashboard",
        tabs: [
          { href: "/zabbix/overzicht", label: "Overzicht" },
          {
            href: "/zabbix/dashboards",
            label: "Dashboards",
            keywords:
              "grafana grafieken kiosk tv top cpu geheugen mttr heatmap",
          },
          {
            href: "/zabbix/problemen",
            label: "Problemen",
            keywords: "triggers alerts ernst",
          },
          {
            href: "/zabbix/hosts",
            label: "Hosts",
            keywords: "beschikbaarheid koppelen",
          },
          {
            href: "/zabbix",
            label: "Agents",
            keywords: "zabbix-agents agent2 psk sleutel",
          },
        ],
      },
    ],
  },
  {
    id: "infra",
    label: "Clusters & nodes",
    items: [
      {
        href: "/clusters",
        label: "Clusters",
        keywords: "vip keepalived uitrollen",
      },
      {
        href: "/nodes",
        label: "Nodes",
        keywords: "servers agents machines ct lxc vm",
      },
      {
        href: "/platformen",
        label: "Platformen",
        keywords: "proxmox pbs backup server truenas opnsense docker platform hypervisor nas firewall",
      },
      {
        href: "/kaart",
        label: "Kaart", needs: "dependencies",
        keywords: "netwerk vlan map",
        tabs: [
          { href: "/kaart", label: "Netwerk" },
          {
            href: "/afhankelijkheden",
            label: "Afhankelijkheden",
            keywords: "graaf dependencies",
          },
        ],
      },
      { href: "/kubernetes", label: "Kubernetes", needs: "kubernetes", keywords: "k3s pods" },
    ],
  },
  {
    id: "services",
    label: "Diensten",
    items: [
      {
        href: "/publicatie",
        label: "Publicatie",
        keywords:
          "npm nginx proxy manager authentik subdomein domein jbogaert publiceren ssl certificaat forward auth",
        tabs: [
          {
            href: "/publicatie",
            label: "Diensten",
            keywords: "diensten poorten containers vip publiceren",
          },
          {
            href: "/publicatie/npm",
            label: "Proxy hosts",
            keywords: "npm nginx proxy host overnemen drift ssl",
          },
          {
            href: "/publicatie/authentik",
            label: "Authentik",
            keywords: "authentik sso applicatie provider outpost forward auth",
          },
          {
            href: "/publicatie/verkeer",
            label: "Verkeer",
            keywords: "npm logs verkeer aanvragen bezoekers ip crowdsec ban verbindingen 5xx",
          },
          {
            href: "/publicatie/logins",
            label: "Logins",
            keywords: "authentik logins mislukte aanmeldingen brute force gebruikers",
          },
        ],
      },
      {
        href: "/databases",
        label: "Databases",
        keywords: "mariadb galera postgresql patroni dump",
      },
      {
        href: "/homepage-apps",
        label: "Homepage-apps",
        keywords:
          "homepage tegels apps api v2 dashboard plex nextcloud herkennen",
      },
    ],
  },
  {
    id: "onderhoud",
    label: "Onderhoud",
    items: [
      {
        href: "/updates",
        label: "Updates",
        keywords: "apt upgrade versies pakketten",
      },
      {
        href: "/onderhoud",
        label: "Automatisch onderhoud",
        keywords: "opruimen schijf logs journal docker prune database optimize vacuum analyze index automatisch",
      },
      {
        href: "/back-ups",
        label: "Back-ups",
        keywords: "backup restore controle",
        tabs: [
          { href: "/back-ups", label: "Controle" },
          {
            href: "/proxmox/backups",
            label: "Proxmox",
            keywords:
              "pve vzdump pbs proxmox backup server terugzetten restore terugzettest retentie",
          },
        ],
      },
      { href: "/cronjobs", label: "Cronjobs", keywords: "cron rsync planning" },
      {
        href: "/commandos",
        label: "Commando's",
        keywords: "shell uitvoeren script",
      },
      { href: "/taken", label: "Taken", keywords: "jobs voortgang" },
      {
        href: "/templates",
        label: "Templates",
        keywords: "uitrollen blauwdruk",
        tabs: [
          { href: "/templates", label: "Templates" },
          { href: "/recepten", label: "Recepten", keywords: "stappen" },
        ],
      },
      {
        href: "/tests",
        label: "Tests",
        keywords: "failover disaster recovery dr",
      },
      { href: "/gitops", label: "GitOps", needs: "gitops", keywords: "git github wijzigingen" },
    ],
  },
  {
    id: "security",
    label: "Beveiliging",
    items: [
      {
        href: "/soc",
        label: "SOC",
        keywords: "beveiliging dreiging aanvallers security dashboard score",
      },
      {
        href: "/kwetsbaarheden",
        label: "Kwetsbaarheden",
        keywords:
          "cve kev exploit debian security tracker pakketten lek patch verharding hardening cis lynis ssh sysctl suid scopes scan nmap subnet vlan pentest",
      },
      { href: "/firewall", label: "Firewall", keywords: "ufw poorten" },
      { href: "/crowdsec", label: "CrowdSec", keywords: "bans bouncer alerts" },
      {
        href: "/aiverslag",
        label: "AI-verslag", needs: "aiverslag",
        keywords:
          "aiverslag ai verslag opnsense dreigingsniveau incident alarm analyse ollama bevinding fix lokaleai",
      },
    ],
  },
  {
    id: "admin",
    label: "Instellingen",
    items: [
      {
        href: "/instellingen",
        label: "Instellingen",
        keywords: "wachtwoord totp gebruikers sessies thema api-tokens",
        tabs: [
          { href: "/instellingen", label: "Instellingen" },
          {
            href: "/proxmox",
            label: "Proxmox-koppeling",
            keywords: "pve proxmox token api koppeling",
          },
        ],
      },
      {
        href: "/logboek",
        label: "Logboek",
        adminOnly: true,
        keywords: "audit wie wat",
      },
      {
        href: "/documentatie",
        label: "Documentatie",
        keywords: "docs markdown",
      },
      { href: "/assistent", label: "Assistent", keywords: "ai vraag" },
      {
        href: "/aan-de-slag",
        label: "Aan de slag",
        keywords: "hulp uitleg beginnen wizard stappen hoe leek nieuw help",
      },
      {
        href: "/uitleg",
        label: "Woordenlijst",
        keywords: "uitleg begrippen vakterm betekenis wat is vip failover drift quorum",
      },
    ],
  },
];

// De platformen (fase 47) staan niet in het menu maar als tabbladen onder
// Clusters & nodes → Platformen, en bij de node die het platform is.
export const navPlatforms: NavPlatform[] = [
  {
    id: "proxmox",
    label: "Proxmox",
    needs: "proxmox",
    items: [
      {
        href: "/proxmox/datacenter",
        label: "Datacenter",
        keywords:
          "pve proxmox quorum corosync capaciteit overcommit onderhoudsmodus host herstarten",
        tabs: [
          { href: "/proxmox/datacenter", label: "Datacenter" },
          {
            href: "/proxmox/taken",
            label: "Taken",
            keywords: "pve task log upid vzdump",
          },
          {
            href: "/proxmox/syslog",
            label: "Syslog",
            keywords: "pve journal syslog logboek host",
          },
          {
            href: "/proxmox/hardware",
            label: "Hardware",
            keywords:
              "pve passthrough gpu pci usb iommu vfio temperatuur ecc herstart",
          },
        ],
      },
      {
        href: "/proxmox/gasten",
        label: "Gasten",
        keywords:
          "pve vm ct lxc qemu aanmaken klonen tags cloud-init template snapshot verhuizen",
        tabs: [
          { href: "/proxmox/gasten", label: "Gasten" },
          {
            href: "/proxmox/hosts",
            label: "Per host",
            keywords:
              "pve hypervisor vm ct lxc qemu pct qm snapshot verhuizen migreren",
          },
        ],
      },
      {
        href: "/proxmox/opslag",
        label: "Opslag",
        keywords:
          "pve storage nfs smb cifs pbs zfs lvm schijf smart slijtage iso template opruimen wees",
      },
      {
        href: "/proxmox/ha",
        label: "HA en replicatie",
        keywords:
          "pve high availability ha-groep regel affiniteit fencing herstart",
        tabs: [
          { href: "/proxmox/ha", label: "HA" },
          {
            href: "/proxmox/replicatie",
            label: "Replicatie",
            keywords: "pve zfs replicatie storage replication schema",
          },
        ],
      },
      {
        href: "/proxmox/netwerk",
        label: "Netwerk en firewall",
        keywords: "pve bridge vmbr vlan bond sdn zone vnet subnet interfaces",
        tabs: [
          { href: "/proxmox/netwerk", label: "Netwerk" },
          {
            href: "/proxmox/firewall",
            label: "Proxmox-firewall",
            keywords: "pve firewall regels ipset alias beveiligingsgroep",
          },
        ],
      },
      {
        href: "/proxmox/toegang",
        label: "Toegang en certificaten",
        keywords:
          "pve gebruiker groep rechten acl rol api token realm wachtwoord",
        tabs: [
          { href: "/proxmox/toegang", label: "Gebruikers en tokens" },
          {
            href: "/proxmox/certificaten",
            label: "Certificaten",
            keywords: "pve acme lets encrypt ssl tls certificaat dns plugin",
          },
        ],
      },
      {
        href: "/proxmox/host-updates",
        label: "Host-updates",
        keywords:
          "pve apt upgrade bijwerken kernel herstart onderhoudsmodus rolling",
      },
    ],
  },
  {
    id: "pbs",
    label: "Backup Server",
    needs: "pbs",
    items: [
      {
        href: "/pbs",
        label: "Backup Server",
        keywords: "pbs proxmox backup server datastore back-up",
        tabs: [
          { href: "/pbs", label: "Overzicht" },
          {
            href: "/pbs/datastores",
            label: "Datastores",
            keywords:
              "pbs datastore gc garbage collection prune verify onderhoud",
          },
          {
            href: "/pbs/inhoud",
            label: "Inhoud",
            keywords:
              "pbs snapshot groep namespace back-up beschermen notitie verifieer",
          },
          {
            href: "/pbs/jobs",
            label: "Jobs",
            keywords:
              "pbs prune verify sync remote gc schema bandbreedte traffic",
          },
          {
            href: "/pbs/dekking",
            label: "Dekking",
            keywords:
              "pbs back-up dekking clusternode laatste back-up geverifieerd ontbreekt",
          },
          {
            href: "/pbs/taken",
            label: "Taken",
            keywords: "pbs task log upid back-up verify gc",
          },
          {
            href: "/pbs/toegang",
            label: "Toegang",
            adminOnly: true,
            keywords: "pbs gebruiker token rechten acl rol",
          },
          {
            href: "/pbs/systeem",
            label: "Systeem",
            keywords:
              "pbs schijf smart zfs netwerk dns dienst update certificaat syslog herstarten",
          },
        ],
      },
    ],
  },
  {
    id: "truenas",
    label: "TrueNAS",
    needs: "truenas",
    items: [
      {
        href: "/truenas",
        label: "TrueNAS",
        keywords:
          "truenas nas opslag zfs pool scrub schijf smart temperatuur melding alert",
        tabs: [
          { href: "/truenas", label: "Overzicht" },
          {
            href: "/truenas/datasets",
            label: "Datasets",
            keywords: "truenas dataset snapshot terugrollen quota zvol",
          },
          {
            href: "/truenas/bescherming",
            label: "Bescherming",
            keywords:
              "truenas snapshot-taak cloud sync rsync replicatie back-up",
          },
          {
            href: "/truenas/shares",
            label: "Shares",
            keywords:
              "truenas smb nfs share dienst service ssh starten stoppen",
          },
        ],
      },
    ],
  },
  {
    id: "opnsense",
    label: "OPNsense",
    needs: "opnsense",
    items: [
      {
        href: "/opnsense",
        label: "OPNsense",
        keywords: "opnsense firewall carp pfsync ha vip gateway router",
        tabs: [
          {
            href: "/opnsense",
            label: "Overzicht",
            keywords:
              "opnsense carp vip master backup pfsync sync gateways interfaces diensten",
          },
          {
            href: "/opnsense/regels",
            label: "Regels en aliassen",
            keywords: "opnsense firewall regels rules alias",
          },
          {
            href: "/opnsense/nat",
            label: "NAT",
            keywords: "opnsense nat port forward doorsturen outbound 1:1 npt",
          },
          {
            href: "/opnsense/netwerk",
            label: "Netwerk",
            keywords: "opnsense vip carp vlan gateway route routering",
          },
          {
            href: "/opnsense/dhcp-dns",
            label: "DHCP en DNS",
            keywords:
              "opnsense dhcp kea dnsmasq lease reservatie unbound dns override",
          },
          {
            href: "/opnsense/vpn",
            label: "VPN",
            keywords: "opnsense wireguard openvpn ipsec peer tunnel",
          },
          {
            href: "/opnsense/diensten",
            label: "Diensten en plugins",
            keywords: "opnsense dienst service herstarten plugin installeren",
          },
          {
            href: "/opnsense/beveiliging",
            label: "Gebruikers en VPN",
            keywords: "opnsense gebruikers groepen certificaten ca ipsec vpn sleutel ids ips suricata shaper",
          },
          {
            href: "/opnsense/systeem",
            label: "Interfaces en systeem",
            keywords: "opnsense interfaces toewijzen ip adres gateway hostnaam domein dns ntp tijdzone",
          },
          {
            href: "/opnsense/advies",
            label: "Regeladvies",
            keywords: "opnsense regels advies voorstellen dubbel overschaduwd ongebruikt geblokkeerd toelaten opruimen",
          },
          {
            href: "/opnsense/automatisch",
            label: "Automatisch",
            keywords: "opnsense automatisch aliassen dns overrides unbound dhcp reservaties kea dnsmasq beheerd",
          },
          {
            href: "/opnsense/verkeer",
            label: "Verkeer",
            keywords: "opnsense filterlog verkeer geblokkeerd toegelaten stromen regels treffers",
          },
          {
            href: "/opnsense/diagnose",
            label: "Diagnose",
            keywords: "opnsense firewalllog log staten states verbinding arp",
          },
          {
            href: "/opnsense/ha",
            label: "HA en firmware",
            keywords:
              "opnsense carp onderhoudsmodus failover test firmware update upgrade drift back-up backup config.xml",
          },
          {
            href: "/opnsense/revisies",
            label: "Revisies",
            keywords:
              "opnsense config.xml revisie geschiedenis diff vergelijken master backup",
          },
        ],
      },
    ],
  },
  {
    id: "docker",
    label: "Docker",
    needs: "docker",
    items: [
      {
        href: "/docker",
        label: "Docker",
        keywords: "docker containers compose portainer",
        tabs: [
          { href: "/docker", label: "Overzicht" },
          {
            href: "/docker/containers",
            label: "Containers",
            keywords: "docker container start stop herstart logs stats",
          },
          {
            href: "/docker/stacks",
            label: "Stacks",
            keywords: "docker compose stacks overnemen",
          },
          {
            href: "/docker/images",
            label: "Images",
            keywords: "docker image pull update registry digest",
          },
          {
            href: "/docker/volumes",
            label: "Volumes",
            keywords: "docker volume opslag",
          },
          {
            href: "/docker/networks",
            label: "Netwerken",
            keywords: "docker network subnet bridge macvlan",
          },
          {
            href: "/docker/events",
            label: "Gebeurtenissen",
            keywords: "docker events crash oom die gestopt",
          },
          {
            href: "/docker/cleanup",
            label: "Opruimen",
            adminOnly: true,
            keywords: "docker prune opruimen schijf ruimte",
          },
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

// Elke pagina (item of tabblad) met het menu-item waar ze onder valt. Een
// pagina van een platform hoort in het menu bij Platformen.
type Entry = {
  href: string;
  label: string;
  item: NavItem;
  group: NavGroup;
  tab?: NavTab;
  platform?: NavPlatform;
};

const platformsItem = "/platformen";

let entryCache: Entry[] | null = null;

function entries(): Entry[] {
  if (entryCache) return entryCache;
  const out: Entry[] = [];
  const push = (g: NavGroup, i: NavItem, platform?: NavPlatform) => {
    out.push({ href: i.href, label: i.label, item: i, group: g, platform });
    for (const t of i.tabs ?? []) {
      if (t.href !== i.href) out.push({ href: t.href, label: t.label, item: i, group: g, tab: t, platform });
    }
  };
  for (const g of navGroups) for (const i of g.items) push(g, i);
  const infra = navGroups.find((g) => g.items.some((i) => i.href === platformsItem))!;
  for (const p of navPlatforms) for (const i of p.items) push(infra, i, p);
  entryCache = out;
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
    if (p === e.href || (e.href !== "/" && p.startsWith(e.href + "/"))) {
      if (!best || e.href.length > best.href.length) best = e;
    }
  }
  return best;
}

// allNavItems is alles wat Ctrl K vindt: de menu-items, de platformen en
// hun tabbladen, ook wat niet in gebruik is.
export function allNavItems(isAdmin: boolean): (NavItem & { group: string })[] {
  const out: (NavItem & { group: string })[] = [];
  const add = (i: NavItem, group: string, prefix = "") => {
    if (i.adminOnly && !isAdmin) return;
    out.push({ href: i.href, label: prefix + i.label, keywords: i.keywords, adminOnly: i.adminOnly, group });
    for (const t of i.tabs ?? []) {
      if (t.href === i.href || (t.adminOnly && !isAdmin)) continue;
      out.push({ href: t.href, label: `${prefix}${i.label} › ${t.label}`, keywords: t.keywords, group });
    }
  };
  for (const g of navGroups) for (const i of g.items) add(i, i === navHome ? "" : g.label);
  for (const p of navPlatforms) for (const i of p.items) add(i, p.label, p.items.length > 1 ? `${p.label} › ` : "");
  return out;
}

// activeHref is het menu-item dat bij een pad hoort (bij een tabblad: zijn
// item; bij een platform: Platformen).
export function activeHref(pathname: string): string | null {
  if (pathname === "/" || pathname === "") return "/";
  const e = entryFor(pathname);
  if (!e) return null;
  return e.platform ? platformsItem : e.item.href;
}

// platformOf geeft het platform van een pagina met het onderdeel dat open
// is; null buiten de platformen.
export function platformOf(pathname: string): { platform: NavPlatform; item: NavItem } | null {
  const e = entryFor(pathname);
  return e?.platform ? { platform: e.platform, item: e.item } : null;
}

// sectionTabs geeft de tabbladen van het menu-item van deze pagina, en welk
// tabblad nu open is; null als het item geen tabbladen heeft.
export function sectionTabs(
  pathname: string,
  isAdmin: boolean,
): { item: NavItem; tabs: NavTab[]; active: string } | null {
  if (pathname === "/" || pathname === "") return null;
  const e = entryFor(pathname);
  if (!e?.item.tabs) return null;
  return {
    item: e.item,
    tabs: e.item.tabs.filter((t) => !t.adminOnly || isAdmin),
    active: e.tab?.href ?? e.item.href,
  };
}

// itemHrefs zijn de adressen die bij een menu-item horen (voor de tellers).
// Platformen telt de meldingen van elk platform.
export function itemHrefs(i: NavItem): string[] {
  if (i.href === platformsItem) return navPlatforms.flatMap((p) => p.items.flatMap(itemHrefs));
  return i.tabs ? i.tabs.map((t) => t.href) : [i.href];
}

export function titleFor(pathname: string): string {
  if (pathname === "/" || pathname === "") return "Overzicht";
  const e = entryFor(pathname);
  if (!e) return "";
  const item = e.platform && e.platform.items.length > 1 ? `${e.item.label} · ${e.platform.label}` : e.item.label;
  return e.tab ? `${e.tab.label} · ${item}` : item;
}

export function groupOf(href: string | null): string | null {
  if (!href) return null;
  return navGroups.find((g) => g.items.some((i) => i.href === href))?.id ?? null;
}
