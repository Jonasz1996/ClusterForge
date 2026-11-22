import dynamic from "next/dynamic";
import Link from "next/link";
import type { ReactNode } from "react";

// Markdown toont tekst van de assistent en de documentatie met een kleine,
// veilige set: koppen, alinea's, lijsten, tabellen, citaten, code, vet,
// cursief, links en Mermaid-diagrammen. Alles wordt als React-elementen
// gebouwd, zodat HTML in de tekst gewoon tekst blijft. Links mogen alleen
// naar ClusterForge zelf (een pad dat met één / begint), of waar
// resolveHref ze heen stuurt; een andere link of een afbeelding wordt alleen
// zijn tekst.
export function Markdown({
  text,
  className,
  resolveHref,
  anchors,
}: {
  text: string;
  className?: string;
  // resolveHref maakt van een relatieve link, zoals netwerk.md, een pad in
  // ClusterForge; null laat alleen de tekst staan.
  resolveHref?: (href: string) => string | null;
  // anchors geeft koppen een id, voor een inhoudsopgave.
  anchors?: boolean;
}) {
  return <div className={className ?? "space-y-3 text-sm leading-relaxed"}>{blocks(text, { resolveHref, anchors })}</div>;
}

type Opts = { resolveHref?: (href: string) => string | null; anchors?: boolean };

// Diagrammen laden dagre alleen als er een is.
const MermaidDiagram = dynamic(() => import("@/components/docs/MermaidDiagram"), {
  ssr: false,
  loading: () => <div className="h-24 animate-pulse rounded-md bg-slate-100 dark:bg-slate-800" />,
});

type Block =
  | { kind: "heading"; level: number; text: string }
  | { kind: "para"; text: string }
  | { kind: "code"; lang: string; text: string }
  | { kind: "quote"; text: string }
  | { kind: "rule" }
  | { kind: "list"; ordered: boolean; start: number; items: ListItem[] }
  | { kind: "table"; head: string[]; align: ("left" | "right" | "center")[]; rows: string[][] };

type ListItem = { text: string; children: ListItem[] };

const listRe = /^(\s*)([-*+]|\d{1,3}[.)])\s+(.*)$/;

export function parse(src: string): Block[] {
  const lines = src.replace(/\r\n?/g, "\n").split("\n");
  const out: Block[] = [];
  let i = 0;
  while (i < lines.length) {
    const line = lines[i];
    if (line.trim() === "") {
      i++;
      continue;
    }
    // Code tussen ``` of een langere rij backticks; alleen een even lange
    // of langere rij sluit hem.
    const fence = /^\s*(`{3,})\s*([\w+-]*)/.exec(line);
    if (fence) {
      const code: string[] = [];
      const close = new RegExp("^\\s*`{" + fence[1].length + ",}\\s*$");
      i++;
      while (i < lines.length && !close.test(lines[i])) code.push(lines[i++]);
      i++;
      out.push({ kind: "code", lang: fence[2].toLowerCase(), text: code.join("\n") });
      continue;
    }
    const h = /^(#{1,6})\s+(.*?)\s*#*\s*$/.exec(line);
    if (h) {
      out.push({ kind: "heading", level: h[1].length, text: h[2] });
      i++;
      continue;
    }
    if (/^\s*([-*_])(\s*\1){2,}\s*$/.test(line)) {
      out.push({ kind: "rule" });
      i++;
      continue;
    }
    // Een tabel: een kopregel met | en daaronder een scheidingsregel.
    if (line.includes("|") && i + 1 < lines.length && /^\s*\|?\s*:?-{2,}:?\s*(\|\s*:?-{2,}:?\s*)*\|?\s*$/.test(lines[i + 1])) {
      const head = cells(line);
      const align = cells(lines[i + 1]).map((c) =>
        c.startsWith(":") && c.endsWith(":") ? "center" : c.endsWith(":") ? "right" : "left",
      ) as ("left" | "right" | "center")[];
      const rows: string[][] = [];
      i += 2;
      while (i < lines.length && lines[i].includes("|") && lines[i].trim() !== "") rows.push(cells(lines[i++]));
      out.push({ kind: "table", head, align, rows });
      continue;
    }
    if (/^\s*>/.test(line)) {
      const quote: string[] = [];
      while (i < lines.length && /^\s*>/.test(lines[i])) quote.push(lines[i++].replace(/^\s*>\s?/, ""));
      out.push({ kind: "quote", text: quote.join("\n") });
      continue;
    }
    const m = listRe.exec(line);
    if (m) {
      const ordered = /\d/.test(m[2]);
      const base = m[1].length;
      const items: ListItem[] = [];
      while (i < lines.length) {
        const lm = listRe.exec(lines[i]);
        if (lm) {
          const item = { text: lm[3], children: [] };
          if (lm[1].length > base && items.length > 0) items[items.length - 1].children.push(item);
          else if (/\d/.test(lm[2]) === ordered || lm[1].length > base) items.push(item);
          else break;
          i++;
        } else if (lines[i].trim() !== "" && /^\s+/.test(lines[i]) && items.length > 0) {
          // Een vervolgregel van het vorige punt.
          const last = items[items.length - 1];
          const target = last.children.length > 0 ? last.children[last.children.length - 1] : last;
          target.text += " " + lines[i].trim();
          i++;
        } else break;
      }
      out.push({ kind: "list", ordered, start: ordered ? parseInt(m[2], 10) || 1 : 1, items });
      continue;
    }
    const para: string[] = [];
    while (
      i < lines.length &&
      lines[i].trim() !== "" &&
      !/^\s*```/.test(lines[i]) &&
      !/^#{1,6}\s/.test(lines[i]) &&
      !/^\s*>/.test(lines[i]) &&
      !(para.length > 0 && listRe.test(lines[i]))
    ) {
      para.push(lines[i++].trim());
    }
    if (para.length === 0) {
      // Een regel die nergens past; als tekst.
      para.push(lines[i++].trim());
    }
    out.push({ kind: "para", text: para.join("\n") });
  }
  return out;
}

function cells(line: string): string[] {
  let s = line.trim();
  if (s.startsWith("|")) s = s.slice(1);
  if (s.endsWith("|") && !s.endsWith("\\|")) s = s.slice(0, -1);
  return s.split(/(?<!\\)\|/).map((c) => c.trim().replace(/\\\|/g, "|"));
}

// slug maakt van een kop een id: kleine letters, cijfers en streepjes.
export function slug(text: string): string {
  return text
    .replace(/\\(.)/g, "$1")
    .replace(/[`*_[\]()]/g, "")
    .toLowerCase()
    .normalize("NFD")
    .replace(/[\u0300-\u036f]/g, "")
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-|-$/g, "");
}

const headingClass: Record<number, string> = {
  1: "text-lg font-semibold",
  2: "text-base font-semibold",
  3: "text-sm font-semibold",
};

function blocks(src: string, o: Opts): ReactNode[] {
  return parse(src).map((b, i) => {
    switch (b.kind) {
      case "heading": {
        const cls = headingClass[b.level] ?? "text-sm font-semibold";
        const id = o.anchors ? slug(b.text) || undefined : undefined;
        return b.level <= 2 ? (
          <h3 key={i} id={id} className={cls + (id ? " scroll-mt-20" : "")}>
            {inline(b.text, o)}
          </h3>
        ) : (
          <h4 key={i} id={id} className={cls + (id ? " scroll-mt-20" : "")}>
            {inline(b.text, o)}
          </h4>
        );
      }
      case "para":
        return (
          <p key={i} className="whitespace-pre-line">
            {inline(b.text, o)}
          </p>
        );
      case "code":
        if (b.lang === "mermaid") return <MermaidDiagram key={i} source={b.text} />;
        return (
          <pre key={i} className="overflow-x-auto rounded-md bg-slate-100 px-3 py-2 font-mono text-xs dark:bg-slate-800">
            <code>{b.text}</code>
          </pre>
        );
      case "quote":
        return (
          <blockquote
            key={i}
            className="border-l-2 border-slate-300 pl-3 text-slate-600 dark:border-slate-600 dark:text-slate-400"
          >
            {blocks(b.text, o)}
          </blockquote>
        );
      case "rule":
        return <hr key={i} className="border-slate-200 dark:border-slate-700" />;
      case "list":
        return list(b.items, b.ordered, b.start, i, o);
      case "table":
        return (
          <div key={i} className="overflow-x-auto">
            <table className={b.head.some((h) => h !== "") ? "w-full text-left text-sm" : "text-left text-sm"}>
              {b.head.some((h) => h !== "") && (
                <thead className="border-b border-slate-200 text-xs text-slate-500 dark:border-slate-700">
                  <tr>
                    {b.head.map((h, j) => (
                      <th key={j} className="px-2 py-1.5 font-medium" style={{ textAlign: b.align[j] ?? "left" }}>
                        {inline(h, o)}
                      </th>
                    ))}
                  </tr>
                </thead>
              )}
              <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
                {b.rows.map((r, j) => (
                  <tr key={j}>
                    {b.head.map((_, k) => (
                      <td
                        key={k}
                        className={k === 0 ? "px-2 py-1.5 align-top whitespace-nowrap" : "px-2 py-1.5 align-top"}
                        style={{ textAlign: b.align[k] ?? "left" }}
                      >
                        {inline(r[k] ?? "", o)}
                      </td>
                    ))}
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        );
    }
    return null;
  });
}

function list(items: ListItem[], ordered: boolean, start: number, key: number | string, o: Opts): ReactNode {
  const children = items.map((it, j) => (
    <li key={j}>
      {inline(it.text, o)}
      {it.children.length > 0 && list(it.children, false, 1, "sub", o)}
    </li>
  ));
  return ordered ? (
    <ol key={key} start={start} className="list-decimal space-y-1 pl-5">
      {children}
    </ol>
  ) : (
    <ul key={key} className="list-disc space-y-1 pl-5">
      {children}
    </ul>
  );
}

// safeHref geeft een link binnen ClusterForge terug, of null.
export function safeHref(href: string): string | null {
  const h = href.trim();
  if (!h.startsWith("/") || h.startsWith("//") || h.startsWith("/\\") || /[\s<>"']/.test(h)) return null;
  return h;
}

// Een backslash voor een leesteken maakt het gewone tekst, zoals \* of \[.
const inlineRe =
  /\\([!-/:-@[-`{-~])|(`+)([\s\S]*?[^`])\2(?!`)|!\[([^\]]*)\]\([^)]*\)|\[((?:\\.|[^\]\\])+)\]\(([^)\s]+)(?:\s+"[^"]*")?\)|\*\*([\s\S]+?)\*\*|__([\s\S]+?)__|\*([^*\s][^*]*?)\*/g;

function inline(text: string, o: Opts): ReactNode[] {
  const out: ReactNode[] = [];
  let last = 0;
  let n = 0;
  for (const m of text.matchAll(inlineRe)) {
    const at = m.index ?? 0;
    if (at > last) out.push(text.slice(last, at));
    last = at + m[0].length;
    const key = n++;
    if (m[1] !== undefined) {
      out.push(m[1]);
    } else if (m[2] !== undefined) {
      out.push(
        <code key={key} className="rounded bg-slate-100 px-1 py-0.5 font-mono text-[0.85em] dark:bg-slate-800">
          {/^ .* $/.test(m[3]) && m[3].trim() !== "" ? m[3].slice(1, -1) : m[3]}
        </code>,
      );
    } else if (m[4] !== undefined) {
      // Een afbeelding: alleen de beschrijving.
      out.push(m[4]);
    } else if (m[5] !== undefined) {
      const href = safeHref(m[6]) ?? (o.resolveHref && !/^[a-z][a-z0-9+.-]*:/i.test(m[6]) ? o.resolveHref(m[6]) : null);
      out.push(
        href ? (
          <Link
            key={key}
            href={href}
            className="text-brand-700 underline decoration-brand-300 underline-offset-2 hover:decoration-brand-700 dark:text-sky-300"
          >
            {inline(m[5], o)}
          </Link>
        ) : (
          <span key={key}>{inline(m[5], o)}</span>
        ),
      );
    } else if (m[7] !== undefined || m[8] !== undefined) {
      out.push(
        <strong key={key} className="font-semibold">
          {inline(m[7] ?? m[8], o)}
        </strong>,
      );
    } else if (m[9] !== undefined) {
      out.push(<em key={key}>{inline(m[9], o)}</em>);
    }
  }
  if (last < text.length) out.push(text.slice(last));
  return out;
}
