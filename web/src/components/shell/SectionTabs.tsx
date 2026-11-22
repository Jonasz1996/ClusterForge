"use client";

import { useRouter } from "next/navigation";
import { Tabs } from "@/components/Tabs";
import { inUse, navPlatforms, platformOf, sectionTabs } from "@/lib/nav";
import { useNavUsage } from "@/lib/navUsage";

// SectionTabs zet de tabbladen van een menu-item bovenaan de pagina, zoals
// Gasten | Per host onder Proxmox. De tabbladen komen uit lib/nav, zodat het
// menu en de tabbladen altijd overeenkomen. Op een platform (fase 47) staan
// erboven de platformen zelf (Proxmox, Backup Server, TrueNAS, OPNsense,
// Docker) en bij Proxmox zijn onderdelen.
export function SectionTabs({ pathname, isAdmin }: { pathname: string; isAdmin: boolean }) {
  const router = useRouter();
  const usage = useNavUsage().data;
  const s = sectionTabs(pathname, isAdmin);
  const pf = platformOf(pathname);
  const platforms = pf ? navPlatforms.filter((p) => p === pf.platform || inUse(p, usage)) : [];
  return (
    <>
      {pf && (
        <Tabs
          className="mb-3 print:hidden"
          tabs={[{ key: "/platformen", label: "← Platformen" }, ...platforms.map((p) => ({ key: p.id, label: p.label }))]}
          value={pf.platform.id}
          onChange={(k) => router.push(k === "/platformen" ? k : navPlatforms.find((p) => p.id === k)!.items[0].href)}
          label="Platformen"
        />
      )}
      {pf && pf.platform.items.length > 1 && (
        <Tabs
          className="mb-3 print:hidden"
          tabs={pf.platform.items.filter((i) => !i.adminOnly || isAdmin).map((i) => ({ key: i.href, label: i.label }))}
          value={pf.item.href}
          onChange={(k) => router.push(k)}
          label={`Onderdelen van ${pf.platform.label}`}
        />
      )}
      {s && s.tabs.length >= 2 && (
        <Tabs
          className="mb-6 print:hidden"
          tabs={s.tabs.map((t) => ({ key: t.href, label: t.label }))}
          value={s.active}
          onChange={(k) => router.push(k)}
          label={`Onderdelen van ${s.item.label}`}
        />
      )}
      {pf && !(s && s.tabs.length >= 2) && <div className="mb-3" />}
    </>
  );
}
