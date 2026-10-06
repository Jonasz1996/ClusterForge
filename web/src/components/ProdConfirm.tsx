"use client";

import Link from "next/link";
import { Alert, Field, Input } from "@/components/ui";
import { useMe } from "@/lib/auth";

// prodConfirmed zegt of de knop mag: de slug is ingetikt en
// tweestapsverificatie staat aan. De server controleert hetzelfde.
export function prodConfirmed(slug: string, typed: string, totp: boolean) {
  return totp && typed === slug;
}

// ProdConfirm is de bevestiging bij prod: de beheerder tikt de slug van het
// cluster in, en moet tweestapsverificatie aan hebben.
export function ProdConfirm({ slug, value, onChange }: { slug: string; value: string; onChange: (v: string) => void }) {
  const me = useMe();
  const totp = me.data?.user.totp_enabled ?? false;
  return (
    <div className="space-y-3 rounded-md border border-red-200 bg-red-50/60 p-3 dark:border-red-900 dark:bg-red-950/40">
      <p className="font-medium text-red-800 dark:text-red-200">Dit is een prodcluster.</p>
      {!totp && me.data && (
        <Alert>
          Op prod kan dit alleen met tweestapsverificatie.{" "}
          <Link href="/instellingen" className="font-medium underline">
            Zet het aan bij Instellingen
          </Link>
          .
        </Alert>
      )}
      <Field label={`Tik ${slug} om te bevestigen`} htmlFor="prod-confirm">
        <Input
          id="prod-confirm"
          autoComplete="off"
          spellCheck={false}
          className="font-mono"
          value={value}
          disabled={!totp}
          onChange={(e) => onChange(e.target.value.trim())}
        />
      </Field>
    </div>
  );
}

// useProdTotp zegt of de ingelogde gebruiker tweestapsverificatie aan heeft.
export function useProdTotp() {
  return useMe().data?.user.totp_enabled ?? false;
}
