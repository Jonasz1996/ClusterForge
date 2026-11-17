"use client";

import { useEffect, useRef, useState } from "react";
import { ViewToggle } from "@/components/Tiles";

function slug(s: string) {
  return (
    "s-" +
    s
      .toLowerCase()
      .normalize("NFD")
      .replace(/[̀-ͯ]/g, "")
      .replace(/[^a-z0-9]+/g, "-")
      .replace(/^-|-$/g, "")
  );
}

function firstText(el: HTMLElement): string {
  const walker = document.createTreeWalker(el, NodeFilter.SHOW_TEXT);
  for (let n = walker.nextNode(); n; n = walker.nextNode()) {
    const t = (n.textContent ?? "").trim();
    if (t) return t;
  }
  return "";
}

// SectionNav zet onder de kop van een lange pagina (node, cluster) een rij
// sprongen naar de kaarten op die pagina. Het leest de koppen (h2) van de
// kaarten zelf, zodat een nieuwe kaart vanzelf meekomt. Staat de kaart in een
// venster (fase 30), dan opent een sprong dat venster. views zet rechts de
// keuze tussen vensters en lijst.
export function SectionNav({ views = false }: { views?: boolean }) {
  const ref = useRef<HTMLElement>(null);
  const [links, setLinks] = useState<{ id: string; label: string }[]>([]);
  useEffect(() => {
    let root = ref.current?.parentElement;
    if (root?.hasAttribute("data-section-wrap")) root = root.parentElement;
    if (!root) return;
    const scan = () => {
      const seen = new Set<string>();
      const out: { id: string; label: string }[] = [];
      for (const h of root.querySelectorAll<HTMLElement>("h2")) {
        if (h.closest("[role=dialog]")) continue;
        // De kop kan een badge, link of knop bevatten; het eerste stuk tekst is de naam.
        const label = firstText(h).slice(0, 40);
        if (!label || seen.has(label)) continue;
        seen.add(label);
        const target = (h.closest("section") as HTMLElement | null) ?? h;
        if (!target.id) target.id = slug(label);
        target.style.scrollMarginTop = "5rem";
        out.push({ id: target.id, label });
      }
      setLinks((cur) => (cur.length === out.length && cur.every((l, i) => l.id === out[i].id && l.label === out[i].label) ? cur : out));
    };
    scan();
    const mo = new MutationObserver(() => scan());
    mo.observe(root, { childList: true, subtree: true });
    return () => mo.disconnect();
  }, []);
  const nav = (
    <nav ref={ref} aria-label="Op deze pagina" className={links.length < 4 ? "hidden" : "-mx-1 flex min-w-0 flex-1 gap-1 overflow-x-auto pb-1 text-sm print:hidden"}>
      {links.map((l) => (
        <a
          key={l.id}
          href={`#${l.id}`}
          onClick={(e) => {
            const tile = document.getElementById(l.id)?.closest<HTMLElement>("[data-tile]");
            if (!tile) return;
            e.preventDefault();
            tile.dispatchEvent(new Event("cf-tile-open"));
          }}
          className="shrink-0 rounded-full border border-slate-200 px-3 py-1 text-slate-600 hover:border-slate-400 hover:text-slate-900 dark:border-slate-700 dark:text-slate-300 dark:hover:text-white"
        >
          {l.label}
        </a>
      ))}
    </nav>
  );
  if (!views) return nav;
  return (
    <div data-section-wrap="" className="flex items-start gap-3">
      {nav}
      <span className="ml-auto">
        <ViewToggle />
      </span>
    </div>
  );
}
