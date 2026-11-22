"use client";

import { useState } from "react";
import { MetricsView } from "@/components/charts/MetricsPanels";
import type { MetricsRange } from "@/lib/inventory";
import { kubectl, nsFlag, useKubeMetrics, type KubeMetricsScope } from "@/lib/kube";
import { Learn } from "./Kube";

// KubeMetrics toont CPU en geheugen uit metrics-server: per namespace voor
// het cluster, per pod voor een app of één pod. ClusterForge leest ze elke
// minuut en bewaart ze in VictoriaMetrics, dus ook van gisteren.
export function KubeMetrics({ clusterId, scope, title }: { clusterId: string; scope: KubeMetricsScope; title: string }) {
  const [range, setRange] = useState<MetricsRange>("1h");
  const q = useKubeMetrics(clusterId, scope, range);
  const top = scope.namespace
    ? kubectl(`top pods ${nsFlag(scope.namespace)}${scope.pod ? ` ${scope.pod}` : ""}`)
    : kubectl("top pods -A");
  return (
    <div className="space-y-3">
      <MetricsView
        q={q}
        range={range}
        onRange={setRange}
        title={title}
        empty="Nog geen metingen. ClusterForge leest CPU en geheugen elke minuut uit metrics-server."
      />
      <Learn title="Wat zie je hier?" commands={[top]}>
        <p>
          CPU staat in millicores: 1000 m is één volle core, 250 m een kwart. Geheugen is de working set, het geheugen dat een pod
          echt gebruikt; daar kijkt Kubernetes naar als een pod over zijn limiet gaat. De metingen komen van metrics-server, dat
          k3s meelevert; <code>kubectl top</code> toont alleen het moment zelf, hier zie je ook het verloop.
        </p>
      </Learn>
    </div>
  );
}
