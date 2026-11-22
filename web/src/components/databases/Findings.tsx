"use client";

import { CopyBlock } from "@/components/inventory/InstallAgent";
import { Badge, cx } from "@/components/ui";
import type { ComponentFinding } from "@/lib/components";

// Findings toont de bevindingen van een plan: wat blokkeert, waar je op moet
// letten en wat goed is om te weten, met een commando om te kopiëren.
export function Findings({ list }: { list: ComponentFinding[] }) {
  return (
    <ul className="space-y-2">
      {list.map((f, i) => (
        <li
          key={i}
          className={cx(
            "space-y-2 rounded border px-3 py-2",
            f.level === "blocked"
              ? "border-red-200 bg-red-50 text-red-900 dark:border-red-900 dark:bg-red-950 dark:text-red-200"
              : f.level === "info"
                ? "border-sky-200 bg-sky-50 text-sky-900 dark:border-sky-900 dark:bg-sky-950 dark:text-sky-200"
                : "border-amber-200 bg-amber-50 text-amber-900 dark:border-amber-900 dark:bg-amber-950 dark:text-amber-200",
          )}
        >
          <p>
            <Badge tone={f.level === "blocked" ? "red" : f.level === "info" ? "blue" : "amber"}>
              {f.level === "blocked" ? "Blokkeert" : f.level === "info" ? "Goed om te weten" : "Let op"}
            </Badge>{" "}
            {f.message}
          </p>
          {f.command && <CopyBlock text={f.command} />}
        </li>
      ))}
    </ul>
  );
}
