"use client";

import Link from "next/link";
import { useState, type FormEvent } from "react";
import { Alert, Button, Card, Field, Select } from "@/components/ui";
import { ApiError } from "@/lib/api/client";
import { useClearSelfLocation, useSelfLocation, useSetSelfLocation, type SelfGuest } from "@/lib/risk";

const guestKey = (g: { connection_id: string; vmid: number }) => `${g.connection_id}:${g.vmid}`;

function guestLabel(g: SelfGuest) {
  return `${g.name || (g.type === "lxc" ? "container" : "VM")} (${g.vmid}) op ${g.host}`;
}

// SelfLocationCard toont op welke VM of container ClusterForge zelf draait.
// De wat-als en de score gebruiken dat: valt die VM uit, dan zie je niets
// meer. ClusterForge zoekt het zelf; een beheerder kan het invullen.
export function SelfLocationCard({ isAdmin }: { isAdmin: boolean }) {
  const q = useSelfLocation();
  const set = useSetSelfLocation();
  const clear = useClearSelfLocation();
  const [choice, setChoice] = useState("");
  const d = q.data;
  const many = new Set(d?.guests.map((g) => g.connection_id)).size > 1;
  const current = d?.guest ? guestKey(d.guest) : "";
  const value = choice || current;

  const submit = (e: FormEvent) => {
    e.preventDefault();
    const g = d?.guests.find((x) => guestKey(x) === value);
    if (g) set.mutate({ connection_id: g.connection_id, vmid: g.vmid }, { onSuccess: () => setChoice("") });
  };
  const err = set.error ?? clear.error;

  return (
    <section id="waar-clusterforge-draait" className="scroll-mt-4">
      <Card title="Waar ClusterForge draait">
        {q.isLoading && <p className="text-sm text-slate-500">Laden…</p>}
        {q.isError && <Alert>Laden mislukt.</Alert>}
        {d && (
          <div className="space-y-4 text-sm">
            <p className="text-slate-600 dark:text-slate-400">
              De wat-als en de health score willen weten op welke VM of container ClusterForge zelf draait: valt die uit, dan zie
              je niets meer en lopen er geen failovers. ClusterForge zoekt dat zelf aan zijn MAC- en IP-adressen. Lukt dat niet,
              zoals in Docker zonder <code className="text-xs">CF_NATS_ADVERTISE</code>, kies de VM dan hier.
            </p>
            <Current d={d} />
            {d.guests.length === 0 ? (
              <p className="text-slate-500">
                Er staan nog geen VM&apos;s of containers uit Proxmox in ClusterForge.{" "}
                <Link href="/proxmox" className="text-brand-600 hover:underline dark:text-sky-300">
                  Koppel eerst Proxmox
                </Link>
                .
              </p>
            ) : isAdmin ? (
              <form onSubmit={submit} className="space-y-3">
                <Field label="VM of container van ClusterForge" htmlFor="self-guest">
                  <Select id="self-guest" value={value} onChange={(e) => setChoice(e.target.value)}>
                    <option value="">Kies…</option>
                    {d.guests.map((g) => (
                      <option key={guestKey(g)} value={guestKey(g)}>
                        {guestLabel(g)}
                        {many && ` · ${g.connection}`}
                      </option>
                    ))}
                  </Select>
                </Field>
                {err && <Alert>{err instanceof ApiError ? err.message : "Bewaren mislukt"}</Alert>}
                <div className="flex flex-wrap gap-2">
                  <Button type="submit" disabled={!value || (value === current && d.source === "manual") || set.isPending}>
                    {set.isPending ? "Bewaren…" : "Bewaren"}
                  </Button>
                  {d.source === "manual" && (
                    <Button type="button" variant="ghost" disabled={clear.isPending} onClick={() => clear.mutate()}>
                      Zelf laten zoeken
                    </Button>
                  )}
                </div>
              </form>
            ) : (
              <p className="text-slate-500">Alleen een beheerder vult dit in.</p>
            )}
          </div>
        )}
      </Card>
    </section>
  );
}

function Current({ d }: { d: NonNullable<ReturnType<typeof useSelfLocation>["data"]> }) {
  const g = d.guest;
  if (d.source === "none") {
    return <Alert kind="info">Niet gevonden. {d.note}</Alert>;
  }
  if (!g) {
    return <Alert>{d.note}</Alert>;
  }
  return (
    <p>
      <span className="font-medium">{guestLabel(g)}</span>
      <span className="text-slate-500">
        {" "}
        · {g.connection} · {d.source === "manual" ? "ingevuld door een beheerder" : `gevonden aan het ${d.matched_on}`}
      </span>
    </p>
  );
}
