"use client";

import { useState } from "react";
import { Empty, formatBytes, tableClass, tdClass, thClass } from "@/components/inventory/bits";
import { TypedConfirm, useCallRunner } from "@/components/pve/bits";
import { Check, FormModal } from "@/components/pve/guest/common";
import { Alert, Button, Card, Field, Input, Select } from "@/components/ui";
import { useIsAdmin } from "@/lib/inventory";
import { storesWith, usePerStorage, usePveGet, type AplTemplate, type Datacenter, type PveResource, type StorageItem } from "@/lib/pve";

// ISO's en CT-templates (fase 26 M5): wat er op de opslag staat, downloaden
// van een URL of uit de Proxmox-templatelijst, en weghalen.

type Row = StorageItem & { node: string; storage: string; shared: boolean };

export function MediaView({ dc, res }: { dc: Datacenter; res: PveResource[] }) {
  const isAdmin = useIsAdmin();
  const stores = storesWith(res, ["iso", "vztmpl"]);
  const lists = usePerStorage<StorageItem, Store>(dc.id, stores, {});
  const { run, pending, banner } = useCallRunner(dc.id);
  const [dialog, setDialog] = useState<{ kind: "url" } | { kind: "apl" } | { kind: "delete"; r: Row } | null>(null);
  const [filter, setFilter] = useState("");
  const close = () => setDialog(null);

  const rows: Row[] = lists
    .flatMap((l) =>
      (l.data ?? [])
        .filter((x) => x.content === "iso" || x.content === "vztmpl")
        .map((x) => ({
          ...x,
          node: l.node,
          storage: l.storage,
          shared: l.shared,
        })),
    )
    .filter((r) => !filter || r.volid.toLowerCase().includes(filter.toLowerCase()))
    .sort((a, b) => a.content.localeCompare(b.content) || a.volid.localeCompare(b.volid));
  const errors = lists.filter((l) => l.error);

  return (
    <Card
      title={
        <span className="flex flex-wrap items-center justify-between gap-2">
          <span>ISO&apos;s en CT-templates</span>
          {isAdmin && stores.length > 0 && (
            <span className="flex gap-2">
              <Button variant="secondary" disabled={pending} onClick={() => setDialog({ kind: "url" })}>
                Downloaden van URL
              </Button>
              <Button variant="secondary" disabled={pending} onClick={() => setDialog({ kind: "apl" })}>
                CT-template kiezen
              </Button>
            </span>
          )}
        </span>
      }
    >
      <div className="space-y-3">
        {banner}
        {errors.length > 0 && <Alert kind="info">Niet te lezen: {errors.map((e) => `${e.storage} op ${e.node} (${e.error})`).join(", ")}.</Alert>}
        {stores.length === 0 ? (
          <Empty>Geen opslag voor ISO&apos;s of templates. Zet bij een opslag de inhoud ISO&apos;s of CT-templates aan.</Empty>
        ) : (
          <>
            <Input aria-label="Zoeken" placeholder="Zoeken…" value={filter} onChange={(e) => setFilter(e.target.value)} className="max-w-xs" />
            {rows.length === 0 ? (
              <Empty>{lists.some((l) => l.loading) ? "Laden…" : "Nog geen ISO's of templates."}</Empty>
            ) : (
              <div className="relative -mx-4 overflow-x-auto sm:mx-0">
                <table className={tableClass}>
                  <thead>
                    <tr>
                      <th className={thClass}>Bestand</th>
                      <th className={thClass}>Soort</th>
                      <th className={thClass}>Opslag</th>
                      <th className={thClass}>Grootte</th>
                      <th className={thClass}>Datum</th>
                      <th className={thClass}>
                        <span className="sr-only">Acties</span>
                      </th>
                    </tr>
                  </thead>
                  <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
                    {rows.map((r) => (
                      <tr key={`${r.node}-${r.volid}`}>
                        <td className={tdClass + " break-all"}>{fileName(r.volid)}</td>
                        <td className={tdClass}>{r.content === "iso" ? "ISO" : "CT-template"}</td>
                        <td className={tdClass}>
                          {r.storage}
                          {!r.shared && <span className="block text-xs text-slate-500">{r.node}</span>}
                        </td>
                        <td className={tdClass + " tabular-nums"}>{formatBytes(r.size)}</td>
                        <td className={tdClass + " whitespace-nowrap"}>{r.ctime ? new Date(r.ctime * 1000).toLocaleDateString("nl-BE") : "–"}</td>
                        <td className={tdClass + " text-right"}>
                          {isAdmin && (
                            <Button variant="secondary" disabled={pending} onClick={() => setDialog({ kind: "delete", r })}>
                              Weghalen
                            </Button>
                          )}
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </>
        )}
      </div>
      {dialog?.kind === "url" && <UrlDialog stores={stores} res={res} run={run} pending={pending} onClose={close} />}
      {dialog?.kind === "apl" && <AplDialog dc={dc} stores={storesWith(res, ["vztmpl"])} run={run} pending={pending} onClose={close} />}
      {dialog?.kind === "delete" && (
        <TypedConfirm
          title={`${fileName(dialog.r.volid)} weghalen`}
          expect={dialog.r.volid}
          confirmLabel="Weghalen"
          pending={pending}
          onClose={close}
          onConfirm={(typed) =>
            void run({
              method: "delete",
              path: `/nodes/${dialog.r.node}/storage/${dialog.r.storage}/content/${dialog.r.volid}`,
              confirm: typed,
            }).then((ok) => ok && close())
          }
        >
          <p className="text-sm">Een gast die deze ISO nog in zijn cd-station heeft, start daarna niet meer tot je het station leegmaakt.</p>
        </TypedConfirm>
      )}
    </Card>
  );
}

export const fileName = (volid: string) => volid.replace(/^[^:]+:(iso|vztmpl|images|rootdir|backup|import|snippets)?\/?/, "");

type Runner = ReturnType<typeof useCallRunner>["run"];
type Store = { node: string; storage: string; shared: boolean };

function storeKey(s: Store) {
  return `${s.node}|${s.storage}`;
}

function UrlDialog({ stores, res, run, pending, onClose }: { stores: Store[]; res: PveResource[]; run: Runner; pending: boolean; onClose: () => void }) {
  const [content, setContent] = useState<"iso" | "vztmpl">("iso");
  const fit = stores.filter((s) =>
    res.some((r) => r.type === "storage" && r.storage === s.storage && r.node === s.node && (r.content ?? "").split(",").includes(content)),
  );
  const [store, setStore] = useState("");
  const chosen = fit.find((s) => storeKey(s) === store) ?? fit[0];
  const [url, setUrl] = useState("");
  const [name, setName] = useState("");
  const [algo, setAlgo] = useState("sha256");
  const [sum, setSum] = useState("");
  const [verify, setVerify] = useState(true);
  const guess = (() => {
    try {
      return decodeURIComponent(new URL(url).pathname.split("/").pop() ?? "");
    } catch {
      return "";
    }
  })();
  const filename = name || guess;
  const error = !chosen
    ? "Geen opslag voor deze soort."
    : !/^https?:\/\/\S+$/.test(url)
      ? "Geef een http- of https-adres."
      : !/^[\w.+-]+$/.test(filename)
        ? "Geef een bestandsnaam (letters, cijfers, . - _ +)."
        : content === "iso" && !/\.(iso|img)$/i.test(filename)
          ? "Een ISO eindigt op .iso of .img."
          : content === "vztmpl" && !/\.tar\.(gz|xz|zst)$/i.test(filename)
            ? "Een CT-template eindigt op .tar.gz, .tar.xz of .tar.zst."
            : sum && !/^[0-9a-fA-F]+$/.test(sum.trim())
              ? "De checksum is hexadecimaal."
              : null;
  const submit = () => {
    const p: Record<string, string> = {
      url,
      filename,
      content,
      "verify-certificates": verify ? "1" : "0",
    };
    if (sum.trim())
      Object.assign(p, {
        checksum: sum.trim().toLowerCase(),
        "checksum-algorithm": algo,
      });
    void run({
      method: "create",
      path: `/nodes/${chosen!.node}/storage/${chosen!.storage}/download-url`,
      params: p,
    }).then((ok) => ok && onClose());
  };
  return (
    <FormModal title="Downloaden van URL" onClose={onClose} onSubmit={submit} submitLabel="Downloaden" disabled={pending} error={error} wide>
      <div className="grid gap-3 sm:grid-cols-2">
        <Field label="Soort" htmlFor="md-c">
          <Select id="md-c" value={content} onChange={(e) => setContent(e.target.value as "iso" | "vztmpl")}>
            <option value="iso">ISO</option>
            <option value="vztmpl">CT-template</option>
          </Select>
        </Field>
        <Field label="Naar opslag" htmlFor="md-s">
          <Select id="md-s" value={chosen ? storeKey(chosen) : ""} onChange={(e) => setStore(e.target.value)}>
            {fit.map((s) => (
              <option key={storeKey(s)} value={storeKey(s)}>
                {s.storage}
                {s.shared ? "" : ` (op ${s.node})`}
              </option>
            ))}
          </Select>
        </Field>
      </div>
      <Field label="Adres" htmlFor="md-u">
        <Input
          id="md-u"
          value={url}
          onChange={(e) => setUrl(e.target.value.trim())}
          placeholder="https://cdimage.debian.org/…/debian-13.2.0-amd64-netinst.iso"
        />
      </Field>
      <Field label="Bestandsnaam" htmlFor="md-n" hint={guess ? `Leeg is ${guess}` : undefined}>
        <Input id="md-n" value={name} onChange={(e) => setName(e.target.value.trim())} />
      </Field>
      <div className="grid gap-3 sm:grid-cols-[10rem_1fr]">
        <Field label="Checksum" htmlFor="md-a">
          <Select id="md-a" value={algo} onChange={(e) => setAlgo(e.target.value)}>
            <option>sha256</option>
            <option>sha512</option>
            <option>sha1</option>
            <option>md5</option>
          </Select>
        </Field>
        <Field label="Waarde" htmlFor="md-sum" hint="Optioneel; Proxmox gooit het bestand weg als het niet klopt">
          <Input id="md-sum" value={sum} onChange={(e) => setSum(e.target.value)} className="font-mono" />
        </Field>
      </div>
      <Check id="md-v" checked={verify} onChange={setVerify}>
        Certificaat van de server controleren
      </Check>
    </FormModal>
  );
}

function AplDialog({ dc, stores, run, pending, onClose }: { dc: Datacenter; stores: Store[]; run: Runner; pending: boolean; onClose: () => void }) {
  const [store, setStore] = useState("");
  const chosen = stores.find((s) => storeKey(s) === store) ?? stores[0];
  const list = usePveGet<AplTemplate[]>(dc.id, `/nodes/${chosen?.node ?? dc.hosts[0]?.pve_node}/aplinfo`, { refetch: false, enabled: !!chosen });
  const [q, setQ] = useState("");
  const [tpl, setTpl] = useState("");
  const items = (list.data ?? [])
    .filter((t) => !q || `${t.template} ${t.headline ?? ""} ${t.os ?? ""}`.toLowerCase().includes(q.toLowerCase()))
    .sort((a, b) => (a.section ?? "").localeCompare(b.section ?? "") || a.template.localeCompare(b.template));
  const error = !chosen ? "Geen opslag voor CT-templates." : !tpl ? "Kies een template." : null;
  return (
    <FormModal
      title="CT-template downloaden"
      onClose={onClose}
      onSubmit={() =>
        void run({
          method: "create",
          path: `/nodes/${chosen!.node}/aplinfo`,
          params: { storage: chosen!.storage, template: tpl },
        }).then((ok) => ok && onClose())
      }
      submitLabel="Downloaden"
      disabled={pending}
      error={error}
      wide
    >
      <div className="grid gap-3 sm:grid-cols-2">
        <Field label="Naar opslag" htmlFor="apl-s">
          <Select id="apl-s" value={chosen ? storeKey(chosen) : ""} onChange={(e) => setStore(e.target.value)}>
            {stores.map((s) => (
              <option key={storeKey(s)} value={storeKey(s)}>
                {s.storage}
                {s.shared ? "" : ` (op ${s.node})`}
              </option>
            ))}
          </Select>
        </Field>
        <Field label="Zoeken" htmlFor="apl-q">
          <Input id="apl-q" value={q} onChange={(e) => setQ(e.target.value)} placeholder="debian, ubuntu, alpine…" />
        </Field>
      </div>
      {list.isLoading ? (
        <p className="text-sm text-slate-500">Lijst laden…</p>
      ) : list.isError ? (
        <Alert kind="error">De templatelijst is niet te lezen. Proxmox vernieuwt hem elke nacht vanzelf; probeer later opnieuw.</Alert>
      ) : (
        <ul className="max-h-80 divide-y divide-slate-100 overflow-y-auto rounded border border-slate-200 text-sm dark:divide-slate-800 dark:border-slate-700">
          {items.map((t) => (
            <li key={t.template}>
              <label className="flex cursor-pointer items-start gap-2 px-3 py-2 hover:bg-slate-50 dark:hover:bg-slate-800/50">
                <input type="radio" name="apl" className="mt-1" checked={tpl === t.template} onChange={() => setTpl(t.template)} />
                <span>
                  <span className="font-medium">{t.headline ?? t.package}</span>
                  <span className="block font-mono text-xs text-slate-500">{t.template}</span>
                </span>
              </label>
            </li>
          ))}
        </ul>
      )}
    </FormModal>
  );
}
