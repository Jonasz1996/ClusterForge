"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { askConfirm } from "@/components/Confirm";
import { ago } from "@/components/inventory/bits";
import { toast } from "@/components/Toast";
import { Alert, Badge, Button, Card } from "@/components/ui";
import { api, unwrap } from "@/lib/api/client";

const key = ["auth", "sessions"] as const;

// device maakt van een user-agent iets leesbaars, zoals "Firefox op Windows".
export function device(ua: string): string {
  if (!ua) return "Onbekend apparaat";
  const browser = /Edg\//.test(ua)
    ? "Edge"
    : /OPR\//.test(ua)
      ? "Opera"
      : /Firefox\//.test(ua)
        ? "Firefox"
        : /Chrome\//.test(ua)
          ? "Chrome"
          : /Safari\//.test(ua)
            ? "Safari"
            : /curl|Wget|Go-http|python/i.test(ua)
              ? "Script"
              : "Browser";
  const os = /iPhone|iPad/.test(ua)
    ? "iOS"
    : /Android/.test(ua)
      ? "Android"
      : /Windows/.test(ua)
        ? "Windows"
        : /Mac OS X|Macintosh/.test(ua)
          ? "macOS"
          : /Linux/.test(ua)
            ? "Linux"
            : "";
  return os ? `${browser} op ${os}` : browser;
}

const dateFmt = new Intl.DateTimeFormat("nl-BE", { dateStyle: "medium", timeStyle: "short" });

// SessionsCard toont waar je ingelogd bent, met een knop om een vergeten
// sessie (bv. op een andere pc) te beëindigen.
export function SessionsCard() {
  const qc = useQueryClient();
  const q = useQuery({ queryKey: key, queryFn: async () => unwrap(await api.GET("/auth/sessions")), refetchInterval: 60_000 });
  const end = useMutation({
    mutationFn: async (id: string) => unwrap(await api.DELETE("/auth/sessions/{id}", { params: { path: { id } } })),
    onSuccess: () => {
      toast("Sessie beëindigd");
      void qc.invalidateQueries({ queryKey: key });
    },
  });
  const endOthers = useMutation({
    mutationFn: async () => unwrap(await api.DELETE("/auth/sessions/others")),
    onSuccess: (r) => {
      toast(r.ended === 1 ? "1 andere sessie beëindigd" : `${r.ended} andere sessies beëindigd`);
      void qc.invalidateQueries({ queryKey: key });
    },
  });
  const items = q.data?.items ?? [];
  const others = items.filter((s) => !s.current).length;
  const err = end.error ?? endOthers.error;

  return (
    <Card
      title={
        <span className="flex flex-wrap items-center justify-between gap-2">
          <span>Sessies</span>
          {others > 0 && (
            <Button
              variant="secondary-danger"
              disabled={endOthers.isPending}
              onClick={async () => {
                if (await askConfirm(`Alle ${others} andere sessies beëindigen? Daar moet je dan opnieuw inloggen.`)) endOthers.mutate();
              }}
            >
              Alle andere beëindigen
            </Button>
          )}
        </span>
      }
    >
      <p className="mb-3 text-sm text-slate-600 dark:text-slate-400">
        Waar je nu ingelogd bent. Een sessie verloopt na een tijd zonder activiteit
        {q.data && q.data.max_age_hours > 0 && <>, en hoe dan ook {Math.round(q.data.max_age_hours / 24)} dagen na het inloggen</>}.
        Herken je er een niet, beëindig hem en wijzig je wachtwoord.
      </p>
      {q.isLoading && <p className="text-sm text-slate-500">Laden…</p>}
      {q.isError && <Alert>Sessies konden niet geladen worden.</Alert>}
      {err && <Alert>{err.message}</Alert>}
      <ul className="divide-y divide-slate-100 dark:divide-slate-800">
        {items.map((s) => (
          <li key={s.id} className="flex flex-wrap items-center justify-between gap-3 py-2.5">
            <div className="min-w-0">
              <div className="flex flex-wrap items-center gap-2 text-sm font-medium">
                {device(s.user_agent)}
                {s.current && <Badge tone="green">deze sessie</Badge>}
              </div>
              <div className="text-xs text-slate-500">
                {s.ip || "onbekend IP"} · ingelogd {dateFmt.format(new Date(s.created_at))} · actief {ago(s.last_seen_at)} · verloopt{" "}
                {dateFmt.format(new Date(s.expires_at))}
              </div>
            </div>
            {!s.current && (
              <Button
                variant="ghost"
                className="px-2 py-1 text-xs text-red-600!"
                disabled={end.isPending}
                onClick={async () => {
                  if (await askConfirm(`Sessie van ${device(s.user_agent)} (${s.ip || "onbekend IP"}) beëindigen?`)) end.mutate(s.id);
                }}
              >
                Beëindigen
              </Button>
            )}
          </li>
        ))}
      </ul>
    </Card>
  );
}
