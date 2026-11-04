"use client";

import { useMutation, useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { Alert, Button, Card, Input } from "@/components/ui";
import { ApiError, api, unwrap } from "@/lib/api/client";

// De kaart Verbinding (fase 9): hoe een app de database van een zelf
// samengesteld cluster bereikt. Een wachtwoord komt pas na je code of
// wachtwoord, en dat staat in het logboek.
export function ConnectionCard({ clusterId, isAdmin }: { clusterId: string; isAdmin: boolean }) {
  const conn = useQuery({
    queryKey: ["clusters", clusterId, "connection"],
    queryFn: async () =>
      unwrap(await api.GET("/clusters/{clusterId}/connection", { params: { path: { clusterId } } })),
  });
  const [asking, setAsking] = useState<string | null>(null);
  const [code, setCode] = useState("");
  const [shown, setShown] = useState<Record<string, string>>({});
  const reveal = useMutation({
    mutationFn: async (secret: string) =>
      unwrap(
        await api.POST("/clusters/{clusterId}/connection/reveal", {
          params: { path: { clusterId } },
          // Met tweestapsverificatie is het je code, anders je wachtwoord;
          // de server neemt wat bij je account hoort.
          body: /^\d{6}$/.test(code) ? { secret, password: code, totp_code: code } : { secret, password: code },
        }),
      ),
    onSuccess: (r) => {
      setShown((s) => ({ ...s, [r.secret]: r.value }));
      setAsking(null);
      setCode("");
    },
  });

  const c = conn.data;
  if (!c?.available) return null;
  return (
    <Card title="Verbinding">
      <p className="mb-3 text-sm text-slate-500">
        Zo bereikt een app de database op de nodes met de rol {c.role}. Vul dit in bij de installatie van je app, als het recept het niet
        zelf al deed.
      </p>
      <dl className="grid grid-cols-[auto_1fr] gap-x-6 gap-y-1 text-sm">
        <dt className="text-slate-500">Server</dt>
        <dd className="font-mono">{c.host}</dd>
        <dt className="text-slate-500">Poort</dt>
        <dd className="font-mono">{c.port}</dd>
        <dt className="text-slate-500">Database</dt>
        <dd className="font-mono">{c.databases.join(", ") || "—"}</dd>
      </dl>
      {c.users.length > 0 && (
        <table className="mt-4 w-full text-sm">
          <thead>
            <tr className="text-left text-slate-500">
              <th className="py-1 font-normal">Gebruiker</th>
              <th className="py-1 font-normal">Rechten op</th>
              <th className="py-1 font-normal">Wachtwoord</th>
            </tr>
          </thead>
          <tbody>
            {c.users.map((u) => (
              <tr key={u.name} className="border-t border-slate-100 align-top dark:border-slate-800">
                <td className="py-2 font-mono">{u.name}</td>
                <td className="py-2 font-mono">{u.database || "—"}</td>
                <td className="py-2">
                  {shown[u.secret] ? (
                    <span className="flex flex-wrap items-center gap-2">
                      <code className="break-all">{shown[u.secret]}</code>
                      <Button variant="ghost" onClick={() => void navigator.clipboard?.writeText(shown[u.secret])}>
                        Kopiëren
                      </Button>
                    </span>
                  ) : isAdmin ? (
                    asking === u.secret ? (
                      <form
                        className="flex flex-wrap gap-2"
                        onSubmit={(e) => {
                          e.preventDefault();
                          reveal.mutate(u.secret);
                        }}
                      >
                        <Input
                          type="password"
                          autoComplete="off"
                          className="w-48"
                          placeholder="Code of wachtwoord"
                          value={code}
                          onChange={(e) => setCode(e.target.value)}
                          autoFocus
                        />
                        <Button type="submit" disabled={reveal.isPending || code === ""}>
                          Tonen
                        </Button>
                        <Button variant="ghost" type="button" onClick={() => setAsking(null)}>
                          Annuleren
                        </Button>
                      </form>
                    ) : (
                      <Button variant="secondary" onClick={() => setAsking(u.secret)}>
                        Tonen
                      </Button>
                    )
                  ) : (
                    <span className="text-slate-500">alleen voor beheerders</span>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      {reveal.error && (
        <div className="mt-3">
          <Alert>{reveal.error instanceof ApiError ? reveal.error.message : "Tonen mislukt"}</Alert>
        </div>
      )}
      <p className="mt-3 text-xs text-slate-500">Tonen vraagt je code van tweestapsverificatie, of je wachtwoord als je die niet hebt, en komt in het logboek.</p>
    </Card>
  );
}
