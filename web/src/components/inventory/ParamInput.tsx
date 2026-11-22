"use client";

import { cx, Input, Select } from "@/components/ui";
import type { TemplateParam } from "@/lib/deploy";

// fieldId is het id van het invoerveld bij een veld uit een foutmelding.
export const fieldId = (field: string) => `f-${field}`;

// paramHint is de uitleg bij een parameter, met zijn bereik.
export function paramHint(p: TemplateParam) {
  const range =
    p.choices.length > 0
      ? ""
      : p.min && p.max
        ? `Van ${p.min} tot ${p.max}.`
        : p.min
          ? `Minstens ${p.min}.`
          : p.max
            ? `Hoogstens ${p.max}.`
            : "";
  return [p.help, range].filter(Boolean).join(" ") || undefined;
}

// ParamInput is het invoerveld van een parameter van een template.
export function ParamInput({
  p,
  value,
  onChange,
  className,
  disabled,
}: {
  p: TemplateParam;
  value: string;
  onChange: (v: string) => void;
  className?: string;
  disabled?: boolean;
}) {
  const id = fieldId(`params.${p.name}`);
  switch (p.type) {
    case "bool":
      return (
        <label className="inline-flex items-center gap-2 text-sm">
          <input
            id={id}
            type="checkbox"
            disabled={disabled}
            checked={value === "true"}
            onChange={(e) => onChange(e.target.checked ? "true" : "false")}
          />
          Aan
        </label>
      );
    case "int":
      if (p.choices.length > 0)
        return (
          <Select id={id} className={className} disabled={disabled} value={value} onChange={(e) => onChange(e.target.value)}>
            {p.choices.map((c) => (
              <option key={c} value={String(c)}>
                {c}
              </option>
            ))}
          </Select>
        );
      return (
        <Input
          id={id}
          className={className}
          disabled={disabled}
          type="number"
          min={p.min ?? undefined}
          max={p.max ?? undefined}
          required={!p.optional}
          placeholder={p.optional ? "automatisch" : undefined}
          value={value}
          onChange={(e) => onChange(e.target.value)}
        />
      );
    case "secret":
      return (
        <Input
          id={id}
          className={cx("font-mono", className)}
          disabled={disabled}
          autoComplete="off"
          placeholder="wordt gegenereerd"
          value={value}
          onChange={(e) => onChange(e.target.value)}
        />
      );
    default:
      return (
        <Input
          id={id}
          className={className}
          disabled={disabled}
          required={!p.optional && p.default === null}
          placeholder={
            p.type === "ipv4" ? "10.0.20.100" : p.type === "size" ? "2G" : p.type === "cidr" ? "10.0.20.0/24" : undefined
          }
          value={value}
          onChange={(e) => onChange(e.target.value)}
        />
      );
  }
}
