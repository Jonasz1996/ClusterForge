"use client";

import { useQuery } from "@tanstack/react-query";
import Link from "next/link";
import { Badge, Card } from "@/components/ui";
import { call } from "@/lib/replace";

// Testlijst 3, N5: wat ClusterForge de laatste 24 uur vanzelf veranderde,
// met waar je het terugdraait. Zo blijft een automatische wijziging op de
// proxy van alle sites niet onopgemerkt.

type AutoChange = {
  job_id: string;
  kind: string;
  title: string;
  status: string;
  at: string;
  node_id?: string;
  hostname?: string;
  cluster?: string;
  undo: string;
  undo_href: string;
};

export function AutoChangesCard() {
  const q = useQuery({
    queryKey: ["auto-changes"],
    queryFn: () => call<{ items: AutoChange[] }>("GET", "/auto-changes"),
    refetchInterval: 60_000,
  });
  const items = q.data?.items ?? [];
  if (items.length === 0) return null;
  return (
    <Card title={`Wat ClusterForge vanzelf veranderde (laatste 24 uur) · ${items.length}`}>
      <ul className="divide-y divide-slate-100 text-sm dark:divide-slate-800">
        {items.slice(0, 30).map((c) => (
          <li key={c.job_id} className="flex flex-wrap items-center justify-between gap-2 py-1.5">
            <span>
              <span className="mr-2 text-xs text-slate-500">
                {new Date(c.at).toLocaleString("nl-BE", { day: "2-digit", month: "2-digit", hour: "2-digit", minute: "2-digit" })}
              </span>
              <Link href={`/taken/detail?id=${c.job_id}`} className="underline">
                {c.title}
              </Link>
              {c.cluster && <span className="ml-2 text-xs text-slate-500">{c.cluster}</span>}
              {c.status === "failed" && (
                <span className="ml-2">
                  <Badge tone="red">mislukt</Badge>
                </span>
              )}
            </span>
            {c.undo && (
              <Link href={c.undo_href} className="text-xs text-slate-500 underline" title={c.undo}>
                terugdraaien: {c.undo}
              </Link>
            )}
          </li>
        ))}
      </ul>
    </Card>
  );
}
