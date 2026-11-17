"use client";

import { useRouter, useSearchParams } from "next/navigation";
import { Suspense, useRef, useState } from "react";
import { Tabs } from "@/components/Tabs";
import { Button, cx } from "@/components/ui";
import { BoardEditor, BoardGrid, exportBoard, useBoardActions } from "@/components/zabbix/Boards";
import {
  InfraDashboard,
  KioskButton,
  NodesDashboard,
  ProblemsDashboard,
  RefreshPicker,
  ZabbixDashboard,
  dashboards,
  useAutoRefresh,
  useKiosk,
  type DashKey,
} from "@/components/zabbix/Dashboards";
import { ZabbixPage } from "@/components/zabbix/Zabbix";
import { useIsAdmin } from "@/lib/inventory";
import { useZabbixBoards, type ZabbixOverview, type ZabbixWindow } from "@/lib/zabbixMonitoring";

export default function ZabbixDashboardsPage() {
  return (
    <Suspense>
      <Dashboards />
    </Suspense>
  );
}

function readRefresh() {
  try {
    return Number(localStorage.getItem("cf-zabbix-verversen") ?? "60") || 0;
  } catch {
    return 60;
  }
}

function Dashboards() {
  const params = useSearchParams();
  const router = useRouter();
  const admin = useIsAdmin();
  const boards = useZabbixBoards();
  const raw = params.get("d") ?? "infra";
  const own = boards.data?.find((b) => b.id === raw) ?? null;
  const fixed = dashboards.find((x) => x.key === raw)?.key as DashKey | undefined;
  // Een eigen dashboard dat (nog) niet geladen is: wachten; anders infra.
  const d: string = own ? own.id : fixed ?? (boards.isLoading ? raw : "infra");
  const v = params.get("venster");
  const win: ZabbixWindow = v === "7d" || v === "30d" ? v : "24h";
  const go = (nd: string, nw: ZabbixWindow) => {
    const q = new URLSearchParams();
    if (nd !== "infra") q.set("d", nd);
    if (nw !== "24h") q.set("venster", nw);
    const s = q.toString();
    router.replace(s ? `/zabbix/dashboards?${s}` : "/zabbix/dashboards", { scroll: false });
  };
  const [editing, setEditing] = useState<"new" | string | null>(null);
  const [stored, setStored] = useState(readRefresh);
  // Een eigen dashboard ververst zoals bewaard, tot je hier iets anders kiest.
  const [override, setOverride] = useState<{ id: string; v: number } | null>(null);
  const refresh = own ? (override?.id === own.id ? override.v : own.refresh) : stored;
  const setRefresh = (n: number) => {
    if (own) {
      setOverride({ id: own.id, v: n });
      return;
    }
    setStored(n);
    try {
      localStorage.setItem("cf-zabbix-verversen", String(n));
    } catch {
      // zonder opslag geldt de keuze tot de pagina herlaadt
    }
  };
  useAutoRefresh(editing ? 0 : refresh);
  const kiosk = useKiosk();
  const actions = useBoardActions((id) => {
    setEditing(null);
    go(id ?? "infra", win);
  });
  const file = useRef<HTMLInputElement>(null);
  const info = dashboards.find((x) => x.key === d);
  const tabs = [...dashboards.map((x) => ({ key: x.key as string, label: x.label })), ...(boards.data ?? []).map((b) => ({ key: b.id, label: b.name }))];

  return (
    <ZabbixPage
      title="Zabbix-dashboards"
      description="Vaste dashboards met de gegevens uit Zabbix, zoals in Grafana, en je eigen dashboards: kies een dashboard, de periode en hoe vaak het ververst. Kiosk zet het op volledig scherm voor een tv."
      window={win}
      onWindow={(w) => go(d, w)}
    >
      {(o: ZabbixOverview) =>
        editing ? (
          <BoardEditor
            key={editing}
            board={editing === "new" ? null : (boards.data?.find((b) => b.id === editing) ?? null)}
            o={o}
            onDone={(b) => {
              setEditing(null);
              if (b) go(b.id, win);
            }}
          />
        ) : (
          <div ref={kiosk.ref} className={cx("space-y-4", kiosk.on && "overflow-auto bg-slate-50 p-6 dark:bg-slate-950")}>
            <div className="flex flex-wrap items-end justify-between gap-3">
              <Tabs tabs={tabs} value={d} onChange={(k) => go(k, win)} label="Dashboard" className="w-full min-w-0 lg:w-auto lg:flex-1" />
              <div className="flex flex-wrap items-center gap-2">
                <RefreshPicker value={refresh} onChange={setRefresh} />
                <KioskButton on={kiosk.on} toggle={kiosk.toggle} />
              </div>
            </div>
            {!kiosk.on && (admin || own) && (
              <div className="flex flex-wrap items-center gap-2">
                {own && admin && (
                  <>
                    <Button variant="secondary" onClick={() => setEditing(own.id)}>
                      Bewerken
                    </Button>
                    <Button variant="secondary" onClick={() => actions.copy(own)} disabled={actions.busy}>
                      Kopie maken
                    </Button>
                  </>
                )}
                {own && (
                  <Button variant="secondary" onClick={() => exportBoard(own)}>
                    Exporteren
                  </Button>
                )}
                {own && admin && (
                  <Button variant="secondary-danger" onClick={() => void actions.remove(own)} disabled={actions.busy}>
                    Weggooien
                  </Button>
                )}
                {admin && (
                  <span className="ml-auto flex flex-wrap gap-2">
                    <Button variant="secondary" onClick={() => file.current?.click()} disabled={actions.busy}>
                      Importeren
                    </Button>
                    <Button onClick={() => setEditing("new")}>+ Nieuw dashboard</Button>
                    <input
                      ref={file}
                      type="file"
                      accept="application/json,.json"
                      className="hidden"
                      onChange={(e) => {
                        const f = e.target.files?.[0];
                        e.target.value = "";
                        if (f) void actions.importFile(f);
                      }}
                    />
                  </span>
                )}
              </div>
            )}
            {own ? (
              <>
                {own.description && <p className="text-sm text-slate-600 dark:text-slate-400">{own.description}</p>}
                <BoardGrid panels={own.panels} o={o} />
              </>
            ) : info ? (
              <>
                <p className="text-sm text-slate-600 dark:text-slate-400">{info.description}</p>
                {d === "infra" && <InfraDashboard o={o} win={win} />}
                {d === "nodes" && <NodesDashboard o={o} />}
                {d === "zabbix" && <ZabbixDashboard o={o} win={win} />}
                {d === "problemen" && <ProblemsDashboard win={win} />}
              </>
            ) : (
              <p className="text-sm text-slate-500">Laden…</p>
            )}
          </div>
        )
      }
    </ZabbixPage>
  );
}
