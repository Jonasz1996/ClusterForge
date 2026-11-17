"use client";

import Link from "next/link";
import { Badge } from "@/components/ui";
import {
  aivDate,
  niveauTone,
  useAIVGet,
  useAIVInstances,
  useAIVSummary,
  type AIVInstance,
} from "@/lib/aiverslag";

// Fase 38 M3: wat AI-verslag over een IP, domein of CVE weet
// (/api/inzicht/opzoeken): bij een intern adres het toestel, de risicoscore
// en de detecties, bij een extern adres land, AS en dreigingslijsten.

type Lookup = {
  q: string;
  soort?: "ip" | "domein" | "cve" | "certificaat";
  intern?: boolean;
  naam?: string;
  vendor?: string | null;
  risico?: { score?: number; gedrag_tekst?: string } | null;
  detecties?: { titel: string; prio?: number; icoon?: string }[];
  land?: string | null;
  land_naam?: string | null;
  vlag?: string;
  asn?: number | null;
  asn_naam?: string | null;
  lijsten?: { label: string; cat?: string; sinds?: number }[];
  uitleg?: string;
  gezien?: {
    eerst?: number;
    laatst?: number;
    n?: number;
    blok?: number;
    toestellen?: string[];
  } | null;
  datalek?: { titel: string; datum?: string }[];
};

// useAIVReady geeft de eerste machine waarop AI-verslag te lezen is.
export function useAIVReady(): AIVInstance | undefined {
  const q = useAIVInstances();
  return q.data?.items.find((v) => !v.why);
}

// AIVLookupSection toont het oordeel van AI-verslag, of niets als er geen
// AI-verslag is (zodat het SOC er niet op wacht).
export function AIVLookupSection({ q }: { q: string }) {
  const v = useAIVReady();
  if (!v) return null;
  return <AIVLookup v={v} q={q} />;
}

export function AIVLookup({ v, q }: { v: AIVInstance; q: string }) {
  const r = useAIVGet<Lookup>(
    v.node_id,
    "/api/inzicht/opzoeken",
    { q },
    { refetch: false },
  );
  return (
    <section className="space-y-2 rounded-md border border-slate-200 p-3 text-sm dark:border-slate-800">
      <div className="flex items-center justify-between gap-2">
        <h3 className="font-medium">Wat AI-verslag weet</h3>
        <Link
          href={`/aiverslag?ip=${encodeURIComponent(q)}`}
          className="text-xs underline"
        >
          AI-verslag
        </Link>
      </div>
      {r.isLoading && <p className="text-slate-500">Opzoeken…</p>}
      {r.isError && (
        <p className="text-slate-500">
          Niet gelukt:{" "}
          {r.error instanceof Error ? r.error.message : "onbekende fout"}
        </p>
      )}
      {r.data && <LookupBody d={r.data} />}
    </section>
  );
}

function LookupBody({ d }: { d: Lookup }) {
  const lists = d.lijsten ?? [];
  return (
    <div className="space-y-2">
      {d.soort === "ip" && d.intern && (
        <p>
          Eigen toestel: <span className="font-medium">{d.naam || d.q}</span>
          {d.vendor ? ` (${d.vendor})` : ""}
          {d.risico?.score != null && (
            <>
              {" · risico "}
              <Badge
                tone={
                  d.risico.score >= 70
                    ? "red"
                    : d.risico.score >= 40
                      ? "amber"
                      : "slate"
                }
              >
                {Math.round(d.risico.score)}
              </Badge>
            </>
          )}
        </p>
      )}
      {d.risico?.gedrag_tekst && (
        <p className="text-slate-600 dark:text-slate-400">
          {d.risico.gedrag_tekst}
        </p>
      )}
      {d.soort === "ip" && !d.intern && (
        <p>
          {d.vlag} {d.land_naam || d.land || "land onbekend"}
          {d.asn ? ` · AS${d.asn}` : ""}
          {d.asn_naam ? ` ${d.asn_naam}` : ""}
        </p>
      )}
      {d.uitleg && <p>{d.uitleg}</p>}
      {(d.soort === "ip" || d.soort === "domein") &&
        (lists.length > 0 ? (
          <div className="flex flex-wrap gap-1.5">
            {lists.map((l) => (
              <Badge key={`${l.label}-${l.cat}`} tone="red">
                {l.label}
                {l.cat ? ` · ${l.cat}` : ""}
              </Badge>
            ))}
          </div>
        ) : (
          <p className="text-slate-500">Op geen enkele dreigingslijst.</p>
        ))}
      {(d.detecties ?? []).length > 0 && (
        <ul className="space-y-0.5">
          {d.detecties!.map((x) => (
            <li key={x.titel}>
              {x.icoon} {x.titel}
              {x.prio != null ? (
                <span className="text-xs text-slate-500"> prio {x.prio}</span>
              ) : null}
            </li>
          ))}
        </ul>
      )}
      {d.gezien && (
        <p className="text-slate-600 dark:text-slate-400">
          {d.gezien.n ?? 0}× opgevraagd in het netwerk (
          {aivDate(d.gezien.eerst)} – {aivDate(d.gezien.laatst)})
          {d.gezien.blok ? `, ${d.gezien.blok}× geblokkeerd` : ""}
          {(d.gezien.toestellen ?? []).length > 0
            ? ` door ${d.gezien.toestellen!.join(", ")}`
            : ""}
        </p>
      )}
      {(d.datalek ?? []).length > 0 && (
        <p>
          Datalekken:{" "}
          {d
            .datalek!.map((b) => `${b.titel}${b.datum ? ` (${b.datum})` : ""}`)
            .join(", ")}
        </p>
      )}
    </div>
  );
}

// AIVSocStrip is de regel van AI-verslag op de SOC-pagina: het
// dreigingsniveau volgens de AI, de netwerkscore en wat open staat.
export function AIVSocStrip() {
  const q = useAIVSummary();
  const s = q.data?.items.find((x) => x.niveau) ?? q.data?.items[0];
  if (!s) return null;
  return (
    <section
      aria-label="AI-verslag"
      className="flex flex-wrap items-center gap-x-4 gap-y-1 rounded-lg border border-slate-200 bg-white px-4 py-2.5 text-sm dark:border-slate-800 dark:bg-slate-900"
    >
      <span className="flex items-center gap-2">
        <span className="font-medium">AI-verslag</span>
        {s.niveau ? (
          <Badge tone={niveauTone[s.niveau] ?? "slate"}>{s.niveau}</Badge>
        ) : (
          <Badge>{s.error ? "niet te lezen" : "onbekend"}</Badge>
        )}
      </span>
      <span className="min-w-0 flex-1 text-slate-600 dark:text-slate-400">
        {s.redenen?.length ? s.redenen.join(" · ") : (s.error ?? "")}
      </span>
      <span className="text-xs text-slate-500">
        {s.score != null ? `score ${Math.round(s.score)} · ` : ""}
        {s.incidenten} incident(en) · {s.open_hoog} hoog open
        {s.unseen ? ` · ${s.unseen} alarm(en) niet gezien` : ""}
      </span>
      <Link href="/aiverslag" className="text-xs underline">
        Openen
      </Link>
    </section>
  );
}
