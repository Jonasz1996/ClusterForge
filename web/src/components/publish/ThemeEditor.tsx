"use client";

import { useState } from "react";
import { Modal } from "@/components/Modal";
import { ActionModal } from "@/components/publish/ActionModal";
import { Alert, Button, Field, Input } from "@/components/ui";
import { usePublishTheme, useSavePublishTheme, type PublishOverview, type PublishTheme } from "@/lib/publish";

// ThemeEditor (fase 42b): het algemene sjabloon voor Advanced van de proxy
// hosts, in een versie met Authentik en een zonder, en de standaard-css.
// Daarna "Op alle hosts toepassen", met eerst een voorbeeld per host.

const area = "w-full rounded border border-slate-300 bg-white p-2 font-mono text-xs dark:border-slate-700 dark:bg-slate-900";

export function ThemeEditor({ ov, onClose }: { ov: PublishOverview; onClose: () => void }) {
  const q = usePublishTheme();
  if (!q.data) {
    return (
      <Modal title="Sjabloon voor Advanced" onClose={onClose} wide>
        {q.isError ? <Alert>{(q.error as Error).message}</Alert> : <p className="text-sm text-slate-500">Laden…</p>}
      </Modal>
    );
  }
  return <Form t={q.data} ov={ov} onClose={onClose} />;
}

function Form({ t, ov, onClose }: { t: PublishTheme; ov: PublishOverview; onClose: () => void }) {
  const [auth, setAuth] = useState(t.template_auth || t.default_auth);
  const [plain, setPlain] = useState(t.template_plain || t.default_plain);
  const [css, setCss] = useState(t.css_default);
  const [apply, setApply] = useState(false);
  const save = useSavePublishTheme();
  const dirty = auth !== (t.template_auth || t.default_auth) || plain !== (t.template_plain || t.default_plain) || css !== t.css_default;
  const themed = ov.hosts.filter((h) => h.css).length;

  if (apply) {
    return <ActionModal title="Sjabloon op de proxy hosts zetten" action={{ action: "theme_many" }} onClose={onClose} />;
  }
  return (
    <Modal title="Sjabloon voor Advanced" onClose={onClose} wide>
      <div className="space-y-4 text-sm">
        <p className="text-slate-600 dark:text-slate-400">
          Eén sjabloon voor de Advanced van al je proxy hosts. Hosts achter Authentik krijgen de versie met Authentik, de andere die zonder. In het
          sjabloon vult ClusterForge <code>{"{css}"}</code> (het css-bestand van de host), <code>{"{outpost}"}</code> (
          {ov.outpost || "de outpost uit de instellingen"}), <code>{"{naam}"}</code> en <code>{"{label}"}</code> in. Een host zonder css krijgt het
          sjabloon niet. Eigen regels in Advanced blijven staan; een eigen &apos;location /&apos; maakt plaats voor het sjabloon.
        </p>
        <Field label="Standaard css-bestand" htmlFor="th-css">
          <Input id="th-css" value={css} placeholder={`https://css.${ov.settings.domain}/{naam}.css`} onChange={(e) => setCss(e.target.value)} />
        </Field>
        <p className="text-xs text-slate-500">
          {"{naam}"} is het eerste deel van de naam zonder cijfers (proxmox50 wordt proxmox), {"{label}"} het eerste deel zoals het is. Leeg: alleen
          hosts met een eigen css (instellen bij Bewerken) krijgen het sjabloon.
        </p>
        <Field label="Met Authentik" htmlFor="th-auth">
          <textarea id="th-auth" rows={14} className={area} value={auth} onChange={(e) => setAuth(e.target.value)} />
        </Field>
        <Button variant="secondary" onClick={() => setAuth(t.default_auth)}>
          Standaard met Authentik terugzetten
        </Button>
        <Field label="Zonder Authentik" htmlFor="th-plain">
          <textarea id="th-plain" rows={10} className={area} value={plain} onChange={(e) => setPlain(e.target.value)} />
        </Field>
        <Button variant="secondary" onClick={() => setPlain(t.default_plain)}>
          Standaard zonder Authentik terugzetten
        </Button>
        {save.isError && <Alert>{(save.error as Error).message}</Alert>}
        {save.isSuccess && !dirty && <Alert kind="info">Sjabloon bewaard. Het staat pas op de hosts na &quot;Op hosts toepassen&quot;.</Alert>}
        <div className="flex flex-wrap justify-end gap-2">
          <Button variant="secondary" onClick={onClose}>
            Sluiten
          </Button>
          <Button
            variant="secondary"
            disabled={!dirty || save.isPending}
            onClick={() => save.mutate({ template_auth: auth, template_plain: plain, css_default: css.trim() })}
          >
            Bewaren
          </Button>
          <Button disabled={dirty} onClick={() => setApply(true)}>
            Op hosts toepassen{themed ? ` (${themed} met css)` : ""}
          </Button>
        </div>
        {dirty && <p className="text-right text-xs text-slate-500">Bewaar eerst; daarna zie je per host wat er verandert.</p>}
      </div>
    </Modal>
  );
}
