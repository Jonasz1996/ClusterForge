"use client";

import { useRouter, useSearchParams } from "next/navigation";
import { Suspense, useState } from "react";
import { Tabs } from "@/components/Tabs";
import { cx } from "@/components/ui";
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
import type { ZabbixOverview, ZabbixWindow } from "@/lib/zabbixMonitoring";

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
  const d = (dashboards.find((x) => x.key === params.get("d"))?.key ?? "infra") as DashKey;
  const v = params.get("venster");
  const win: ZabbixWindow = v === "7d" || v === "30d" ? v : "24h";
  const go = (nd: DashKey, nw: ZabbixWindow) => {
    const q = new URLSearchParams();
    if (nd !== "infra") q.set("d", nd);
    if (nw !== "24h") q.set("venster", nw);
    const s = q.toString();
    router.replace(s ? `/zabbix/dashboards?${s}` : "/zabbix/dashboards", { scroll: false });
  };
  const [refresh, setRefreshState] = useState(readRefresh);
  const setRefresh = (n: number) => {
    setRefreshState(n);
    try {
      localStorage.setItem("cf-zabbix-verversen", String(n));
    } catch {
      // zonder opslag geldt de keuze tot de pagina herlaadt
    }
  };
  useAutoRefresh(refresh);
  const kiosk = useKiosk();
  const info = dashboards.find((x) => x.key === d)!;

  return (
    <ZabbixPage
      title="Zabbix-dashboards"
      description="Vaste dashboards met de gegevens uit Zabbix, zoals in Grafana: kies een dashboard, de periode en hoe vaak het ververst. Kiosk zet het op volledig scherm voor een tv."
      window={win}
      onWindow={(w) => go(d, w)}
    >
      {(o: ZabbixOverview) => (
        <div
          ref={kiosk.ref}
          className={cx("space-y-4", kiosk.on && "overflow-auto bg-slate-50 p-6 dark:bg-slate-950")}
        >
          <div className="flex flex-wrap items-end justify-between gap-3">
            <Tabs tabs={dashboards.map((x) => ({ key: x.key, label: x.label }))} value={d} onChange={(k) => go(k, win)} label="Dashboard" className="min-w-0 flex-1" />
            <div className="flex items-center gap-2">
              <RefreshPicker value={refresh} onChange={setRefresh} />
              <KioskButton on={kiosk.on} toggle={kiosk.toggle} />
            </div>
          </div>
          <p className="text-sm text-slate-600 dark:text-slate-400">{info.description}</p>
          {d === "infra" && <InfraDashboard o={o} win={win} />}
          {d === "nodes" && <NodesDashboard o={o} />}
          {d === "zabbix" && <ZabbixDashboard o={o} win={win} />}
          {d === "problemen" && <ProblemsDashboard win={win} />}
        </div>
      )}
    </ZabbixPage>
  );
}
