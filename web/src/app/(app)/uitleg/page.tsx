"use client";

import Link from "next/link";
import { useState } from "react";
import { Card, Input, PageHeader } from "@/components/ui";
import { glossary } from "@/lib/help";

// De woordenlijst (fase 41): elke vakterm in gewone taal.
export default function GlossaryPage() {
  const [q, setQ] = useState("");
  const terms = Object.entries(glossary)
    .filter(([, t]) => !q || (t.term + " " + t.uitleg).toLowerCase().includes(q.toLowerCase()))
    .sort((a, b) => a[1].term.localeCompare(b[1].term, "nl"));
  return (
    <div className="space-y-6">
      <PageHeader
        title="Woordenlijst"
        description={
          <>
            De vaktermen uit ClusterForge, in gewone taal. Zie je ergens een woord met een stippellijn, ga erover of tik erop voor dezelfde
            uitleg. Wil je iets doen? Kijk bij{" "}
            <Link href="/aan-de-slag" className="underline">
              Aan de slag
            </Link>
            .
          </>
        }
      />
      <Input placeholder="Zoek een woord…" value={q} onChange={(e) => setQ(e.target.value)} aria-label="Zoek een woord" className="max-w-sm" />
      <Card>
        <dl className="divide-y divide-slate-100 dark:divide-slate-800">
          {terms.map(([id, t]) => (
            <div key={id} id={id} className="scroll-mt-20 py-3 first:pt-0 last:pb-0">
              <dt className="font-medium">{t.term}</dt>
              <dd className="mt-0.5 text-sm text-slate-600 dark:text-slate-400">{t.uitleg}</dd>
            </div>
          ))}
          {terms.length === 0 && <p className="text-sm text-slate-500">Geen woord gevonden.</p>}
        </dl>
      </Card>
    </div>
  );
}
