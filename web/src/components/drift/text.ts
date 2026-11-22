import type { DriftFinding } from "@/lib/drift";

const timeFmt = new Intl.DateTimeFormat("nl-BE", { dateStyle: "medium", timeStyle: "short" });

const aspectPrefix: Partial<Record<DriftFinding["aspect"], string>> = {
  mode: "rechten ",
  owner: "eigenaar ",
  group: "groep ",
};

export function expectedText(f: DriftFinding) {
  return (aspectPrefix[f.aspect] ?? "") + f.expected;
}

export function actualText(f: DriftFinding) {
  const extra = [f.detail, f.mtime && `gewijzigd op de node om ${timeFmt.format(new Date(f.mtime))}`].filter(Boolean);
  return (aspectPrefix[f.aspect] ?? "") + f.actual + (extra.length ? ` (${extra.join(", ")})` : "");
}

// shortActual is wat er werkelijk staat, zonder grootte en tijd.
export function shortActual(f: DriftFinding) {
  return (aspectPrefix[f.aspect] ?? "") + f.actual;
}
