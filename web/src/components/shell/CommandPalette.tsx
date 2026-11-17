"use client";

import { useRouter } from "next/navigation";
import { useEffect, useMemo, useRef, useState } from "react";
import { cx } from "@/components/ui";
import { useClusters, useNodes } from "@/lib/inventory";
import { allNavItems } from "@/lib/nav";

type Hit = { key: string; label: string; sub: string; href: string; kind: "Pagina" | "Cluster" | "Node" | "Actie" };

// norm maakt zoeken ongevoelig voor hoofdletters en accenten.
function norm(s: string) {
  return s
    .toLowerCase()
    .normalize("NFD")
    .replace(/[̀-ͯ]/g, "");
}

// score: 0 = past niet; hoger is beter. Begin van een woord telt meer dan
// ergens in het midden.
function score(q: string, hay: string) {
  if (!q) return 1;
  const h = norm(hay);
  let total = 0;
  for (const part of q.split(/\s+/).filter(Boolean)) {
    const i = h.indexOf(part);
    if (i < 0) return 0;
    total += i === 0 ? 3 : /[\s/.\-_(]/.test(h[i - 1] ?? "") ? 2 : 1;
  }
  return total;
}

function actions(isAdmin: boolean): Hit[] {
  const out: Hit[] = [
    { key: "a-totp", label: "Tweestapsverificatie, wachtwoord en thema", sub: "Instellingen", href: "/instellingen", kind: "Actie" },
  ];
  if (isAdmin) {
    out.unshift(
      { key: "a-cluster", label: "Nieuw cluster", sub: "Clusters", href: "/clusters?nieuw=1", kind: "Actie" },
      { key: "a-uitrollen", label: "Cluster uitrollen vanuit een template", sub: "Templates", href: "/templates", kind: "Actie" },
      { key: "a-agent", label: "Agent installeren op een nieuwe node", sub: "Nodes", href: "/nodes?agent=1", kind: "Actie" },
      { key: "a-logs", label: "Logs doorzoeken", sub: "Logs", href: "/logs", kind: "Actie" },
    );
  }
  return out;
}

// CommandPalette (alleen getekend als ze open is) opent met Ctrl K (of ⌘ K, of /) en zoekt in pagina's,
// clusters en nodes. Pijltjes kiezen, Enter opent.
export function CommandPalette({ onClose, isAdmin }: { onClose: () => void; isAdmin: boolean }) {
  const router = useRouter();
  const [q, setQ] = useState("");
  const [sel, setSel] = useState(0);
  const inputRef = useRef<HTMLInputElement>(null);
  const listRef = useRef<HTMLUListElement>(null);
  const clusters = useClusters();
  const nodes = useNodes();

  // De palette wordt pas getekend als ze opent, dus begint ze altijd leeg.
  useEffect(() => {
    // Na het tekenen, anders pakt de focus niet op mobiel.
    requestAnimationFrame(() => inputRef.current?.focus());
  }, []);

  const hits = useMemo(() => {
    const nq = norm(q.trim());
    const all: (Hit & { hay: string })[] = [
      ...allNavItems(isAdmin).map((i) => ({
        key: "p" + i.href,
        label: i.label,
        sub: i.group || "Start",
        href: i.href,
        kind: "Pagina" as const,
        hay: `${i.label} ${i.group} ${i.keywords ?? ""}`,
      })),
      ...actions(isAdmin).map((a) => ({ ...a, hay: `${a.label} ${a.sub}` })),
      ...(clusters.data ?? []).map((c) => ({
        key: "c" + c.id,
        label: c.name,
        sub: [c.slug, c.environment, ...(c.vips ?? []).map((v) => v.address)].filter(Boolean).join(" · "),
        href: `/clusters/detail?id=${c.id}`,
        kind: "Cluster" as const,
        hay: `${c.name} ${c.slug} ${(c.vips ?? []).map((v) => v.address).join(" ")} ${(c.tags ?? []).join(" ")}`,
      })),
      ...(nodes.data ?? []).map((n) => ({
        key: "n" + n.id,
        label: n.hostname,
        sub: [n.primary_ip, n.cluster_name, n.agent?.connection === "online" ? "online" : n.agent ? "offline" : "geen agent"]
          .filter(Boolean)
          .join(" · "),
        href: `/nodes/detail?id=${n.id}`,
        kind: "Node" as const,
        hay: `${n.hostname} ${n.primary_ip ?? ""} ${n.cluster_name ?? ""} ${n.role} ${n.tags.join(" ")}`,
      })),
    ];
    if (!nq) return all.filter((h) => h.kind === "Pagina" || h.kind === "Actie").slice(0, 40);
    const hits = all
      .map((h) => ({ h, s: score(nq, h.hay) + (norm(h.label) === nq ? 5 : 0) }))
      .filter((x) => x.s > 0)
      .sort((a, b) => b.s - a.s)
      .slice(0, 40)
      .map((x) => x.h);
    // Een volledig IP-adres: ook opzoeken in het beveiligingsoverzicht (wat
    // CrowdSec, de firewall en de login van dit adres zagen).
    const ip = q.trim();
    if (/^(\d{1,3}\.){3}\d{1,3}$/.test(ip) || (/^[0-9a-f:]+$/i.test(ip) && ip.includes(":") && ip.length > 2)) {
      hits.push({ key: `soc-${ip}`, label: `Adres ${ip} in het SOC`, sub: "Aanvallen, bans en firewallverkeer", href: `/soc?ip=${encodeURIComponent(ip)}`, kind: "Actie", hay: ip });
      hits.push({ key: `aiv-${ip}`, label: `Adres ${ip} in AI-verslag`, sub: "Toestel, risico, land en dreigingslijsten", href: `/aiverslag?ip=${encodeURIComponent(ip)}`, kind: "Actie", hay: ip });
    }
    return hits;
  }, [q, isAdmin, clusters.data, nodes.data]);

  useEffect(() => {
    listRef.current?.querySelector<HTMLElement>(`[data-i="${sel}"]`)?.scrollIntoView({ block: "nearest" });
  }, [sel]);

  const go = (h: Hit | undefined) => {
    if (!h) return;
    onClose();
    router.push(h.href);
  };

  return (
    <div
      className="fixed inset-0 z-50 flex items-start justify-center bg-slate-900/40 p-4 pt-[10vh]"
      onMouseDown={(e) => {
        if (e.target === e.currentTarget) onClose();
      }}
    >
      <div
        role="dialog"
        aria-modal="true"
        aria-label="Zoeken"
        className="w-full max-w-xl overflow-hidden rounded-lg bg-white shadow-2xl dark:bg-slate-900"
      >
        <input
          ref={inputRef}
          value={q}
          onChange={(e) => {
            setQ(e.target.value);
            setSel(0);
          }}
          onKeyDown={(e) => {
            if (e.key === "ArrowDown") {
              e.preventDefault();
              setSel((s) => Math.min(s + 1, hits.length - 1));
            } else if (e.key === "ArrowUp") {
              e.preventDefault();
              setSel((s) => Math.max(s - 1, 0));
            } else if (e.key === "Enter") {
              e.preventDefault();
              go(hits[sel]);
            } else if (e.key === "Escape") {
              onClose();
            }
          }}
          placeholder="Zoek een pagina, cluster, node of IP-adres…"
          aria-label="Zoeken"
          role="combobox"
          aria-expanded="true"
          aria-controls="cf-palette-list"
          aria-activedescendant={hits[sel] ? `cf-hit-${sel}` : undefined}
          className="w-full border-b border-slate-200 bg-transparent px-4 py-3 text-base outline-none dark:border-slate-800"
        />
        <ul id="cf-palette-list" ref={listRef} role="listbox" className="max-h-[60vh] overflow-y-auto py-1">
          {hits.length === 0 && <li className="px-4 py-6 text-center text-sm text-slate-500">Niets gevonden.</li>}
          {hits.map((h, i) => (
            <li
              key={h.key}
              id={`cf-hit-${i}`}
              data-i={i}
              role="option"
              aria-selected={i === sel}
              onMouseMove={() => setSel(i)}
              onClick={() => go(h)}
              className={cx(
                "flex cursor-pointer items-center justify-between gap-3 px-4 py-2 text-sm",
                i === sel && "bg-brand-50 dark:bg-slate-800",
              )}
            >
              <span className="min-w-0">
                <span className="block truncate font-medium">{h.label}</span>
                {h.sub && <span className="block truncate text-xs text-slate-500">{h.sub}</span>}
              </span>
              <span className="shrink-0 rounded bg-slate-100 px-1.5 py-0.5 text-[10px] tracking-wide text-slate-600 uppercase dark:bg-slate-800 dark:text-slate-400">
                {h.kind}
              </span>
            </li>
          ))}
        </ul>
        <div className="hidden border-t border-slate-200 px-4 py-2 text-xs text-slate-500 sm:block dark:border-slate-800">
          ↑ ↓ kiezen · Enter openen · Esc sluiten
        </div>
      </div>
    </div>
  );
}
