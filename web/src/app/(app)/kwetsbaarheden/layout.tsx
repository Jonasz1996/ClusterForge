"use client";

import { usePathname, useRouter } from "next/navigation";
import type { ReactNode } from "react";
import { Tabs } from "@/components/Tabs";

// Kwetsbaarheden is één menu-item met tabbladen: pakketten (CVE's),
// verharding, netwerkscans, de eigen scopes en het rapport. Elk tabblad heeft een eigen adres, zodat
// een link of de terugknop op het juiste tabblad uitkomt.
const sections = [
  { key: "/kwetsbaarheden", label: "Pakketten" },
  { key: "/kwetsbaarheden/verharding", label: "Verharding" },
  { key: "/kwetsbaarheden/scans", label: "Scans" },
  { key: "/kwetsbaarheden/scopes", label: "Eigen scopes" },
  { key: "/kwetsbaarheden/rapport", label: "Rapport" },
] as const;

type Section = (typeof sections)[number]["key"];

export default function SecurityLayout({ children }: { children: ReactNode }) {
  const pathname = usePathname().replace(/\/$/, "");
  const router = useRouter();
  const value = ([...sections].reverse().find((s) => pathname === s.key || pathname.startsWith(s.key + "/"))?.key ?? sections[0].key) as Section;
  return (
    <div className="space-y-6">
      <Tabs className="print:hidden" tabs={sections} value={value} onChange={(k) => router.push(k)} label="Onderdelen van Kwetsbaarheden" />
      {children}
    </div>
  );
}
