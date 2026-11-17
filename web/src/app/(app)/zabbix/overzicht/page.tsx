"use client";

import { useRouter, useSearchParams } from "next/navigation";
import { Suspense } from "react";
import { CoverageCard, HostMap, NowCard, ProblemTrend, StateBand, Tiles, ZabbixPage, ZabbixSelf } from "@/components/zabbix/Zabbix";
import type { ZabbixWindow } from "@/lib/zabbixMonitoring";

export default function ZabbixOverviewPage() {
  return (
    <Suspense>
      <Overview />
    </Suspense>
  );
}

function Overview() {
  const params = useSearchParams();
  const router = useRouter();
  const v = params.get("venster");
  const win: ZabbixWindow = v === "7d" || v === "30d" ? v : "24h";
  const setWin = (w: ZabbixWindow) => router.replace(w === "24h" ? "/zabbix/overzicht" : `/zabbix/overzicht?venster=${w}`, { scroll: false });
  return (
    <ZabbixPage
      title="Zabbix"
      description="Wat Zabbix ziet, gekoppeld aan je clusters en nodes: open problemen, bereikbaarheid, de proxy en de gezondheid van Zabbix zelf. Elke minuut opgehaald."
      window={win}
      onWindow={setWin}
    >
      {(o) => (
        <>
          <StateBand o={o} />
          <Tiles o={o} />
          <div className="grid gap-6 xl:grid-cols-5">
            <NowCard o={o} className="xl:col-span-3" />
            <ZabbixSelf o={o} className="xl:col-span-2" />
          </div>
          <ProblemTrend o={o} />
          <HostMap o={o} />
          <CoverageCard o={o} />
        </>
      )}
    </ZabbixPage>
  );
}
