"use client";

import { useEffect } from "react";

// useOpenFlag roept open() aan als de pagina geopend werd met ?<name>=1, bv.
// /nodes?agent=1 vanuit de zoekfunctie. Daarna gaat de vlag uit de adresbalk,
// zodat herladen het formulier niet opnieuw opent.
export function useOpenFlag(name: string, enabled: boolean, open: () => void) {
  useEffect(() => {
    if (!enabled) return;
    const url = new URL(window.location.href);
    if (url.searchParams.get(name) !== "1") return;
    url.searchParams.delete(name);
    window.history.replaceState(window.history.state, "", url.pathname + url.search + url.hash);
    open();
    // open hoort maar één keer te lopen, bij het laden.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [name, enabled]);
}
