"use client";

import { useState, type FormEvent } from "react";
import { Alert, Button, Card, Field, Input, Select } from "@/components/ui";
import { ApiError } from "@/lib/api/client";
import {
  dollars,
  modelLabel,
  useAssistantSettings,
  useSaveAssistantSettings,
  useTestAssistantKey,
  type AssistantSettings,
} from "@/lib/assistant";
import { auditTimeFmt } from "@/lib/audit";
import { useMe } from "@/lib/auth";

// AssistantSettingsCard zet de assistent aan of uit en kiest de sleutel, het
// model, het budget en of viewers hem mogen gebruiken. Opslaan vraagt je
// wachtwoord, of je code als tweestapsverificatie aan staat.
export function AssistantSettingsCard({ isAdmin }: { isAdmin: boolean }) {
  const q = useAssistantSettings();
  const [saved, setSaved] = useState(false);
  const s = q.data;
  if (!s) return null;
  if (!isAdmin) {
    return (
      <Card title="Assistent">
        <p className="text-sm text-slate-600 dark:text-slate-400">
          {s.can_use ? "Je kunt de assistent gebruiken." : s.reason} Alleen een beheerder wijzigt deze instellingen.
        </p>
      </Card>
    );
  }
  // Na opslaan begint het formulier opnieuw met wat de server teruggaf.
  return <SettingsForm key={s.updated_at ?? "nieuw"} s={s} saved={saved} onSaved={setSaved} />;
}

function SettingsForm({ s, saved, onSaved }: { s: AssistantSettings; saved: boolean; onSaved: (v: boolean) => void }) {
  const me = useMe();
  const save = useSaveAssistantSettings();
  const test = useTestAssistantKey();
  const [enabled, setEnabled] = useState(s.enabled);
  const [key, setKey] = useState("");
  const [model, setModel] = useState(s.model);
  const [budget, setBudget] = useState(String(s.monthly_budget_usd));
  const [viewers, setViewers] = useState(s.viewer_access);
  const [confirm, setConfirm] = useState("");
  const totp = me.data?.user.totp_enabled ?? false;

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    onSaved(false);
    try {
      await save.mutateAsync({
        enabled,
        api_key: key.trim() || undefined,
        model,
        monthly_budget_usd: parseInt(budget, 10) || 0,
        viewer_access: viewers,
        ...(totp ? { totp_code: confirm } : { password: confirm }),
      });
      setKey("");
      setConfirm("");
      onSaved(true);
    } catch {
      // De fout staat onder het formulier.
    }
  };

  const error = save.error instanceof ApiError ? errorText(save.error) : save.error ? "Opslaan mislukt." : null;

  return (
    <Card title="Assistent">
      <div id="assistent" className="space-y-4">
        {!s.available && <Alert>{s.reason}</Alert>}
        <Alert kind="info">
          De assistent beantwoordt vragen met de Anthropic API. Naar die API gaan je vragen en wat de tools teruggeven: namen,
          statussen, adressen, events, pakketnamen en metrics. Geheimen, bestandsinhoud, vingerafdrukken, tokens en wachtwoorden
          gaan er nooit heen. De assistent leest alleen; hij kan niets starten, stoppen of wijzigen.
        </Alert>
        <form onSubmit={submit} className="space-y-4">
          <fieldset disabled={!s.available} className="space-y-4">
            <label className="flex items-center gap-2 text-sm font-medium">
              <input type="checkbox" checked={enabled} onChange={(e) => setEnabled(e.target.checked)} />
              Assistent aan
            </label>
            <div className="grid gap-4 sm:grid-cols-2">
              <Field
                label="API-sleutel"
                htmlFor="assistant-key"
                hint={
                  s.has_key
                    ? "Er is een sleutel opgeslagen. Leeg laten om hem te houden."
                    : "Maak een sleutel op console.anthropic.com bij API Keys. Hij wordt versleuteld bewaard en komt nooit terug."
                }
              >
                <Input
                  id="assistant-key"
                  type="password"
                  autoComplete="off"
                  value={key}
                  onChange={(e) => setKey(e.target.value)}
                  placeholder={s.has_key ? "••••••••" : "sk-ant-…"}
                  className="font-mono"
                />
              </Field>
              <Field label="Model" htmlFor="assistant-model" hint="Prijs per miljoen tokens invoer en uitvoer.">
                <Select id="assistant-model" value={model} onChange={(e) => setModel(e.target.value)}>
                  {s.models.map((m) => (
                    <option key={m.id} value={m.id}>
                      {m.label} ({dollars(m.input_usd)} / {dollars(m.output_usd)})
                    </option>
                  ))}
                </Select>
              </Field>
              <Field
                label="Maandbudget in dollar"
                htmlFor="assistant-budget"
                hint="Is het op, dan weigert de assistent nieuwe vragen tot de eerste van de volgende maand."
              >
                <Input
                  id="assistant-budget"
                  type="number"
                  min={1}
                  max={10000}
                  required
                  value={budget}
                  onChange={(e) => setBudget(e.target.value)}
                />
              </Field>
              <div className="flex items-start pt-7">
                <label className="flex items-center gap-2 text-sm">
                  <input type="checkbox" checked={viewers} onChange={(e) => setViewers(e.target.checked)} />
                  Ook viewers mogen vragen stellen
                </label>
              </div>
            </div>
            <div className="max-w-xs">
              <Field
                label={totp ? "Code uit de app" : "Je wachtwoord"}
                htmlFor="assistant-confirm"
                hint="Ter bevestiging: de sleutel kost geld en leest de hele fleet."
              >
                <Input
                  id="assistant-confirm"
                  type={totp ? "text" : "password"}
                  inputMode={totp ? "numeric" : undefined}
                  autoComplete={totp ? "one-time-code" : "current-password"}
                  required
                  value={confirm}
                  onChange={(e) => setConfirm(totp ? e.target.value.replace(/\D/g, "") : e.target.value)}
                  className={totp ? "font-mono tracking-widest" : undefined}
                />
              </Field>
            </div>
          </fieldset>
          {error && <Alert>{error}</Alert>}
          {saved && !save.isError && <Alert kind="success">Opgeslagen.</Alert>}
          {test.data &&
            (test.data.ok ? (
              <Alert kind="success">De sleutel werkt; {modelLabel(test.data.model, s.models)} antwoordde.</Alert>
            ) : (
              <Alert>{test.data.message}</Alert>
            ))}
          {test.error && <Alert>{test.error.message}</Alert>}
          <div className="flex flex-wrap items-center justify-between gap-2">
            <p className="text-xs text-slate-500">
              {s.updated_at &&
                `Laatst gewijzigd ${auditTimeFmt.format(new Date(s.updated_at))}${s.updated_by ? ` door ${s.updated_by}` : ""}.`}
            </p>
            <div className="flex gap-2">
              <Button
                type="button"
                variant="secondary"
                disabled={!s.available || test.isPending || (!s.has_key && key.trim() === "")}
                onClick={() => test.mutate({ model, api_key: key.trim() || undefined })}
              >
                {test.isPending ? "Testen…" : "Sleutel testen"}
              </Button>
              <Button type="submit" disabled={!s.available || save.isPending}>
                Opslaan
              </Button>
            </div>
          </div>
        </form>
      </div>
    </Card>
  );
}

function errorText(err: ApiError) {
  if (err.code === "invalid_totp") return "Deze code klopt niet of is al gebruikt.";
  if (err.code === "invalid_credentials") return "Het wachtwoord klopt niet.";
  return err.message;
}
