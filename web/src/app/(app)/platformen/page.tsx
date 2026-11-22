"use client";

import Link from "next/link";
import { Badge, Card, PageHeader } from "@/components/ui";
import { useMe } from "@/lib/auth";
import { useAttentionBadges } from "@/lib/attention";
import { inUse, itemHrefs, navPlatforms, type NavPlatform } from "@/lib/nav";
import { useNavUsage } from "@/lib/navUsage";

const about: Record<string, string> = {
  proxmox: "De hypervisor: VM's en containers, opslag, HA, netwerk en de updates van de hosts.",
  pbs: "Proxmox Backup Server: datastores, back-ups, jobs en hoeveel van je nodes een back-up heeft.",
  truenas: "De NAS: pools, schijven, datasets, snapshots en shares.",
  opnsense: "De firewall en router: CARP, regels, NAT, DHCP en DNS, VPN en firmware.",
  docker: "Containers, Compose-stacks, images en volumes op elke node met Docker.",
};

// Platformen (fase 47): wat vroeger vijf groepen in het menu waren, staat
// hier als één ingang. Elk platform heeft zijn tabbladen bovenaan zijn
// pagina's; een platform dat nog niets beheert, kun je hier koppelen.
export default function PlatformsPage() {
  const isAdmin = useMe().data?.user.role === "admin";
  const usage = useNavUsage();
  const badges = useAttentionBadges(true);
  const used = navPlatforms.filter((p) => inUse(p, usage.data));
  const unused = navPlatforms.filter((p) => !inUse(p, usage.data));
  const count = (p: NavPlatform) =>
    p.items.flatMap(itemHrefs).reduce((n, h) => n + (badges?.[h]?.count ?? 0), 0);
  const card = (p: NavPlatform, on: boolean) => (
    <Card
      key={p.id}
      title={
        <span className="flex items-center justify-between gap-2">
          <Link href={p.items[0].href} className="hover:underline">
            {p.label}
          </Link>
          {on ? count(p) > 0 ? <Badge tone="amber">{count(p)} aandacht</Badge> : <Badge tone="green">in gebruik</Badge> : <Badge>niet gekoppeld</Badge>}
        </span>
      }
    >
      <p className="text-sm text-slate-600 dark:text-slate-400">{about[p.id]}</p>
      <div className="mt-3 flex flex-wrap gap-1.5">
        {p.items
          .filter((i) => !i.adminOnly || isAdmin)
          .flatMap((i) => (p.items.length > 1 ? [i] : (i.tabs ?? [i])))
          .slice(0, 10)
          .map((i) => (
            <Link
              key={i.href}
              href={i.href}
              className="rounded-full border border-slate-200 px-2.5 py-0.5 text-xs hover:border-brand-500 hover:text-brand-700 dark:border-slate-700 dark:hover:text-sky-300"
            >
              {i.label}
            </Link>
          ))}
      </div>
    </Card>
  );
  return (
    <div className="space-y-6">
      <PageHeader
        title="Platformen"
        description="De systemen onder je clusters: Proxmox, Backup Server, TrueNAS, OPNsense en Docker. Op de pagina van een node vind je dezelfde onderdelen bij die node."
      />
      <div className="grid gap-4 md:grid-cols-2">{used.map((p) => card(p, true))}</div>
      {usage.data && unused.length > 0 && (
        <section className="space-y-3">
          <h2 className="text-sm font-semibold text-slate-500">Nog niet gekoppeld</h2>
          <div className="grid gap-4 md:grid-cols-2">{unused.map((p) => card(p, false))}</div>
        </section>
      )}
    </div>
  );
}
