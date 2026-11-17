"use client";

import { createContext, useCallback, useContext, useEffect, useRef, useState, useSyncExternalStore, type ReactNode } from "react";
function cx(...parts: (string | false | null | undefined)[]) {
  return parts.filter(Boolean).join(" ");
}

// Vensters (fase 30). Een lange pagina met kaarten onder elkaar wordt een
// raster van compacte vensters naast en onder elkaar. Elk venster toont het
// begin van zijn kaart (of een korte samenvatting) en gaat met één klik open
// over het hele scherm, met alle details. Het is hetzelfde stuk pagina dat
// groter wordt: een formulier of een gekozen tabblad blijft staan bij openen
// en sluiten. Terug in de browser of Escape sluit het venster weer.
//
// Een Card binnen een Tile neemt de rand van het venster over en zet de
// openknop naast zijn titel; andere inhoud krijgt de rand van het venster.
// Wie liever de lange lijst heeft, zet de weergave om naar Lijst; dat blijft
// bewaard in deze browser.

type TileCtx = { open: boolean; expand: () => void; collapse: () => void } | null;

const Ctx = createContext<TileCtx>(null);

// useTile geeft het venster waarin een kaart staat, of null erbuiten. Een
// kaart in een kaart ziet null: alleen de buitenste kaart is het venster.
export function useTile() {
  return useContext(Ctx);
}

// TileReset zet het venster uit voor wat eronder staat.
export function TileReset({ children }: { children: ReactNode }) {
  return <Ctx.Provider value={null}>{children}</Ctx.Provider>;
}

const MODE_KEY = "cf-weergave";
type Mode = "vensters" | "lijst";
const listeners = new Set<() => void>();

function readMode(): Mode {
  try {
    return localStorage.getItem(MODE_KEY) === "lijst" ? "lijst" : "vensters";
  } catch {
    return "vensters";
  }
}

function setMode(m: Mode) {
  try {
    localStorage.setItem(MODE_KEY, m);
  } catch {
    // Zonder opslag geldt de keuze alleen tot de pagina herlaadt.
  }
  listeners.forEach((l) => l());
}

export function useViewMode(): [Mode, (m: Mode) => void] {
  const mode = useSyncExternalStore(
    (l) => {
      listeners.add(l);
      const onStorage = (e: StorageEvent) => e.key === MODE_KEY && l();
      window.addEventListener("storage", onStorage);
      return () => {
        listeners.delete(l);
        window.removeEventListener("storage", onStorage);
      };
    },
    readMode,
    () => "vensters" as Mode,
  );
  return [mode, setMode];
}

const GridCtx = createContext<Mode | null>(null);

// TileGrid is het raster. Op een gsm is het één kolom, op een breed scherm
// tot drie naast elkaar.
export function TileGrid({ children, className }: { children: ReactNode; className?: string }) {
  const [mode] = useViewMode();
  return (
    <GridCtx.Provider value={mode}>
      <div
        className={cx(
          mode === "vensters" ? "cf-tilegrid grid grid-flow-row-dense grid-cols-1 gap-4 md:grid-cols-2 2xl:grid-cols-3" : "space-y-6",
          className,
        )}
      >
        {children}
      </div>
    </GridCtx.Provider>
  );
}

// ViewToggle wisselt tussen vensters en de lange lijst.
export function ViewToggle() {
  const [mode, set] = useViewMode();
  const btn = (m: Mode, label: string) => (
    <button
      type="button"
      aria-pressed={mode === m}
      onClick={() => set(m)}
      className={cx(
        "rounded px-2.5 py-1",
        mode === m
          ? "bg-slate-200 font-medium text-slate-900 dark:bg-slate-700 dark:text-white"
          : "text-slate-600 hover:text-slate-900 dark:text-slate-300 dark:hover:text-white",
      )}
    >
      {label}
    </button>
  );
  return (
    <div
      role="group"
      aria-label="Weergave"
      className="inline-flex shrink-0 rounded-md border border-slate-200 p-0.5 text-xs print:hidden dark:border-slate-700"
    >
      {btn("vensters", "Vensters")}
      {btn("lijst", "Lijst")}
    </div>
  );
}

function slug(s: string) {
  return (
    "venster-" +
    s
      .toLowerCase()
      .normalize("NFD")
      .replace(/[̀-ͯ]/g, "")
      .replace(/[^a-z0-9]+/g, "-")
      .replace(/^-|-$/g, "")
  );
}

function headingText(el: HTMLElement): string {
  const h = el.querySelector("h2");
  if (!h) return "";
  const walker = document.createTreeWalker(h, NodeFilter.SHOW_TEXT);
  for (let n = walker.nextNode(); n; n = walker.nextNode()) {
    const t = (n.textContent ?? "").trim();
    if (t) return t;
  }
  return "";
}

const FIELDS = "form, input:not([type=hidden]):not([type=checkbox]):not([type=radio]), textarea, select";

function countFields(el: HTMLElement) {
  let n = 0;
  for (const f of el.querySelectorAll(FIELDS)) if (!f.closest("[role=dialog]")) n++;
  return n;
}

// Tile is één venster. wide laat het twee kolommen breed zijn (een tabel met
// nodes, grafieken); tall maakt het hoger.
export function Tile({ children, wide = false, tall = false }: { children: ReactNode; wide?: boolean; tall?: boolean }) {
  const mode = useContext(GridCtx);
  const ref = useRef<HTMLDivElement>(null);
  const bodyRef = useRef<HTMLDivElement>(null);
  const [open, setOpen] = useState(false);
  const [overflow, setOverflow] = useState(false);
  const [label, setLabel] = useState("");
  const pushed = useRef(false);

  const expand = useCallback(() => {
    const el = ref.current;
    if (!el) return;
    const key = slug(headingText(el) || "venster");
    setOpen(true);
    if (location.hash !== `#${key}`) {
      history.pushState({ cfTile: key }, "", `#${key}`);
      pushed.current = true;
    }
  }, []);

  const collapse = useCallback(() => {
    setOpen(false);
    if (pushed.current) {
      pushed.current = false;
      history.back();
    } else if (location.hash.startsWith("#venster-")) {
      history.replaceState(history.state, "", location.pathname + location.search);
    }
    requestAnimationFrame(() => ref.current?.scrollIntoView({ block: "nearest" }));
  }, []);

  // Terug in de browser sluit het venster; een adres met #venster-… opent het.
  useEffect(() => {
    if (mode !== "vensters") return;
    const el = ref.current;
    if (!el) return;
    const sync = () => {
      const key = slug(headingText(el) || "venster");
      const want = location.hash === `#${key}`;
      setOpen((o) => {
        if (o && !want) pushed.current = false;
        return want;
      });
    };
    window.addEventListener("popstate", sync);
    window.addEventListener("hashchange", sync);
    // Bij het laden staat de kop er misschien nog niet: kijk opnieuw zodra
    // de inhoud er is.
    let tries = 0;
    const mo = new MutationObserver(() => {
      if (headingText(el) || ++tries > 50) {
        mo.disconnect();
        if (location.hash.startsWith("#venster-")) sync();
      }
    });
    if (headingText(el)) {
      if (location.hash.startsWith("#venster-")) sync();
    } else mo.observe(el, { childList: true, subtree: true });
    const onOpen = () => expand();
    el.addEventListener("cf-tile-open", onOpen);
    return () => {
      window.removeEventListener("popstate", sync);
      window.removeEventListener("hashchange", sync);
      el.removeEventListener("cf-tile-open", onOpen);
      mo.disconnect();
    };
  }, [mode, expand]);

  // Escape sluit, behalve als er een dialoogvenster open staat. Achter het
  // open venster scrolt de pagina niet.
  useEffect(() => {
    if (!open) return;
    const el = ref.current;
    if (el) setLabel(headingText(el));
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== "Escape" || e.defaultPrevented) return;
      if (document.querySelector("[role=dialog]")) return;
      collapse();
    };
    window.addEventListener("keydown", onKey);
    const prev = document.body.style.overflow;
    document.body.style.overflow = "hidden";
    return () => {
      window.removeEventListener("keydown", onKey);
      document.body.style.overflow = prev;
    };
  }, [open, collapse]);

  // Is er meer dan in het venster past? Dan komt onderaan "Alles tonen".
  // Opent iemand in het venster een formulier (Bewerken, Toevoegen), dan
  // gaat het venster vanzelf open, want een half formulier helpt niemand.
  useEffect(() => {
    if (mode !== "vensters") return;
    const body = bodyRef.current;
    if (!body) return;
    const measure = () => setOverflow(body.scrollHeight > body.clientHeight + 4);
    const ro = new ResizeObserver(measure);
    ro.observe(body);
    let fields = countFields(body);
    let clickedAt = 0;
    const onDown = () => {
      clickedAt = Date.now();
    };
    body.addEventListener("pointerdown", onDown);
    body.addEventListener("keydown", onDown);
    const mo = new MutationObserver(() => {
      measure();
      for (const c of body.children) ro.observe(c);
      const now = countFields(body);
      if (now > fields && Date.now() - clickedAt < 1500 && !body.closest("[data-tile-open]")) expand();
      fields = now;
    });
    mo.observe(body, { childList: true, subtree: true });
    for (const c of body.children) ro.observe(c);
    measure();
    return () => {
      ro.disconnect();
      mo.disconnect();
      body.removeEventListener("pointerdown", onDown);
      body.removeEventListener("keydown", onDown);
    };
  }, [mode, expand]);

  if (mode !== "vensters") return <>{children}</>;

  return (
    <Ctx.Provider value={{ open, expand, collapse }}>
      <div
        ref={ref}
        data-tile=""
        data-tile-open={open ? "" : undefined}
        className={cx(
          "cf-tile",
          open
            ? "fixed inset-0 z-40 overflow-y-auto bg-slate-50 dark:bg-slate-950"
            : cx(
                "relative flex flex-col overflow-hidden rounded-lg border border-slate-200 bg-white shadow-xs dark:border-slate-800 dark:bg-slate-900",
                // Op een gsm zo hoog als nodig (tot de grens), op een breed scherm
                // allemaal even hoog zodat het raster netjes blijft.
                tall ? "max-h-[30rem] md:h-[30rem]" : "max-h-80 md:h-80",
                wide && "md:col-span-2",
              ),
        )}
      >
        {open && (
          <div className="sticky top-0 z-10 flex items-center gap-3 border-b border-slate-200 bg-white/95 px-4 py-2 backdrop-blur-sm sm:px-8 dark:border-slate-800 dark:bg-slate-900/95">
            <button
              type="button"
              onClick={collapse}
              className="rounded-md px-2 py-1 text-sm text-slate-600 hover:bg-slate-100 dark:text-slate-300 dark:hover:bg-slate-800"
            >
              ← Terug naar het overzicht
            </button>
            <span className="min-w-0 flex-1 truncate text-sm font-medium">{label}</span>
            <span className="hidden text-xs text-slate-400 sm:inline">Esc sluit</span>
          </div>
        )}
        <div
          ref={bodyRef}
          className={cx(
            "cf-tile-body",
            open ? "mx-auto w-full max-w-7xl p-4 sm:p-8" : "min-h-0 flex-1 overflow-hidden p-4",
          )}
        >
          {children}
        </div>
        {!open && overflow && (
          <div className="pointer-events-none absolute inset-x-0 bottom-0 flex h-16 items-end justify-center bg-gradient-to-t from-white via-white/90 to-transparent pb-2 dark:from-slate-900 dark:via-slate-900/90">
            <button
              type="button"
              onClick={expand}
              className="pointer-events-auto rounded-full border border-slate-200 bg-white px-3 py-1 text-xs font-medium text-slate-700 shadow-xs hover:border-slate-400 hover:text-slate-900 dark:border-slate-700 dark:bg-slate-800 dark:text-slate-200 dark:hover:text-white"
            >
              Alles tonen
            </button>
          </div>
        )}
      </div>
    </Ctx.Provider>
  );
}

function ExpandIcon() {
  return (
    <svg viewBox="0 0 20 20" fill="none" stroke="currentColor" strokeWidth="1.6" className="h-4 w-4" aria-hidden="true">
      <path d="M12 3h5v5M8 17H3v-5M17 3l-6 6M3 17l6-6" strokeLinecap="round" strokeLinejoin="round" />
    </svg>
  );
}

// TileButton is de openknop naast de titel van een kaart in een venster;
// buiten een venster of als het al open is, toont ze niets.
export function TileButton() {
  const t = useTile();
  if (!t || t.open) return null;
  return (
    <button
      type="button"
      onClick={t.expand}
      title="Openen op het hele scherm"
      aria-label="Openen op het hele scherm"
      className="-mr-1 shrink-0 rounded-md p-1 text-slate-400 hover:bg-slate-100 hover:text-slate-700 print:hidden dark:hover:bg-slate-800 dark:hover:text-slate-200"
    >
      <ExpandIcon />
    </button>
  );
}
