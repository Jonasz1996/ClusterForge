"use client";

import { useRouter } from "next/navigation";
import { Tabs } from "@/components/Tabs";
import { sectionTabs } from "@/lib/nav";

// SectionTabs zet de tabbladen van een menu-item bovenaan de pagina, zoals
// Gasten | Per host onder Proxmox. De tabbladen komen uit lib/nav, zodat het
// menu en de tabbladen altijd overeenkomen.
export function SectionTabs({ pathname, isAdmin }: { pathname: string; isAdmin: boolean }) {
  const router = useRouter();
  const s = sectionTabs(pathname, isAdmin);
  if (!s || s.tabs.length < 2) return null;
  return (
    <Tabs
      className="mb-6 print:hidden"
      tabs={s.tabs.map((t) => ({ key: t.href, label: t.label }))}
      value={s.active}
      onChange={(k) => router.push(k)}
      label={`Onderdelen van ${s.item.label}`}
    />
  );
}
