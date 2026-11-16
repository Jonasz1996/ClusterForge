"use client";

import { useEffect, useRef, useState } from "react";

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
// kaarten zelf, zodat een nieuwe kaart vanzelf meekomt.
export function SectionNav() {
  const ref = useRef<HTMLElement>(null);
  const [links, setLinks] = useState<{ id: string; label: string }[]>([]);
  useEffect(() => {
    const root = ref.current?.parentElement;
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
  return (
    <nav ref={ref} aria-label="Op deze pagina" className={links.length < 4 ? "hidden" : "-mx-1 flex gap-1 overflow-x-auto pb-1 text-sm print:hidden"}>
      {links.map((l) => (
        <a
          key={l.id}
          href={`#${l.id}`}
          className="shrink-0 rounded-full border border-slate-200 px-3 py-1 text-slate-600 hover:border-slate-400 hover:text-slate-900 dark:border-slate-700 dark:text-slate-300 dark:hover:text-white"
        >
          {l.label}
        </a>
      ))}
    </nav>
  );
}
