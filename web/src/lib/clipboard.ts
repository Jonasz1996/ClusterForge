"use client";

import { toast } from "@/components/Toast";

// copyText zet tekst op het klembord. navigator.clipboard bestaat alleen
// over https (of localhost); ClusterForge draait vaak over http in het LAN.
// Dan valt het terug op een verborgen tekstvak met execCommand("copy").
// Geeft true als het lukte; anders meldt een toast dat je zelf moet kopiëren.
export async function copyText(text: string, done = "Gekopieerd"): Promise<boolean> {
  let ok = false;
  try {
    if (window.isSecureContext && navigator.clipboard) {
      await navigator.clipboard.writeText(text);
      ok = true;
    }
  } catch {
    ok = false;
  }
  if (!ok) {
    const ta = document.createElement("textarea");
    ta.value = text;
    ta.setAttribute("readonly", "");
    ta.style.position = "fixed";
    ta.style.top = "-1000px";
    document.body.appendChild(ta);
    ta.select();
    try {
      ok = document.execCommand("copy");
    } catch {
      ok = false;
    }
    document.body.removeChild(ta);
  }
  toast(ok ? done : "Kopiëren lukte niet; selecteer de tekst en kopieer hem zelf.", ok ? "success" : "error");
  return ok;
}
