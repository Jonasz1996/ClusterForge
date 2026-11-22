"use client";

import Link from "next/link";
import { useState } from "react";
import { Term } from "@/components/help/Term";
import { Badge, Card, PageHeader, cx } from "@/components/ui";
import { useAttention } from "@/lib/attention";
import { useMe } from "@/lib/auth";
import { useClusters, useNodes } from "@/lib/inventory";
import { useProxmoxConnections } from "@/lib/proxmox";
import type { ReactNode } from "react";

// Aan de slag (fase 41): de gewone taken stap voor stap, met per stap wat er
// gebeurt en, waar ClusterForge het kan nagaan, of hij al gedaan is.

type Step = {
  title: string;
  uitleg: ReactNode;
  href?: string;
  link?: string;
  // done: true klaar, false nog te doen, undefined niet na te gaan.
  done?: boolean;
};

type Task = { id: string; title: string; wanneer: string; steps: Step[] };

export default function GettingStartedPage() {
  const me = useMe();
  const nodes = useNodes();
  const clusters = useClusters();
  const pve = useProxmoxConnections();
  const att = useAttention();

  const withAgent = nodes.data?.filter((n) => n.agent) ?? [];
  const online = withAgent.filter((n) => n.agent?.connection === "online");
  const items = att.data?.items ?? [];
  const has = (prefix: string) => items.some((i) => i.id.startsWith(prefix));

  const tasks: Task[] = [
    {
      id: "eerste-keer",
      title: "De eerste keer instellen",
      wanneer: "Eén keer, als je ClusterForge net hebt.",
      steps: [
        {
          title: "Zet tweestapsverificatie aan",
          uitleg: (
            <>
              Naast je wachtwoord een code uit een app op je gsm. Zo kan niemand binnen met alleen je wachtwoord. Nodig voor acties op
              productie. <Term id="totp">Wat is dat?</Term>
            </>
          ),
          href: "/instellingen#tweestap",
          link: "Naar Instellingen",
          done: me.data ? me.data.user.totp_enabled : undefined,
        },
        {
          title: "Koppel Proxmox",
          uitleg: "Met een token van Proxmox kan ClusterForge je machines zien, maken, starten en back-uppen.",
          href: "/proxmox",
          link: "Proxmox koppelen",
          done: pve.data ? pve.data.items.length > 0 : undefined,
        },
        {
          title: "Installeer de agent op je eerste server",
          uitleg: (
            <>
              Bij Nodes → Agent installeren krijg je één commando. Plak het één keer op de server; daarna gaat alles via deze website.{" "}
              <Term id="agent">Wat is een agent?</Term>
            </>
          ),
          href: "/nodes",
          link: "Naar Nodes",
          done: nodes.data ? withAgent.length > 0 : undefined,
        },
        {
          title: "Kies wanneer een schijf 'te vol' is",
          uitleg: "Standaard krijg je een melding vanaf 80 % en een rode vanaf 90 %. Pas dat aan als je wilt.",
          href: "/instellingen#schijfbewaking",
          link: "Schijfbewaking",
        },
      ],
    },
    {
      id: "server-toevoegen",
      title: "Een server toevoegen",
      wanneer: "Als je een nieuwe machine wilt laten beheren.",
      steps: [
        {
          title: "Maak de machine in Proxmox (of gebruik een bestaande)",
          uitleg: (
            <>
              Een <Term id="lxc">container (CT)</Term> is licht en snel; een <Term id="vm">VM</Term> is een volledige computer. Voor de meeste
              diensten volstaat een container met Debian.
            </>
          ),
          href: "/proxmox/gasten",
          link: "Naar Gasten",
        },
        {
          title: "Installeer de agent",
          uitleg: "Nodes → Agent installeren. Kopieer het commando en plak het op de nieuwe server. Na een halve minuut staat hij in de lijst.",
          href: "/nodes",
          link: "Agent installeren",
        },
        {
          title: "Controleer dat hij online is",
          uitleg: "Een groen bolletje bij de node betekent dat de agent verbonden is. Rood? Kijk of de server ClusterForge kan bereiken (poort 4222 en 8080).",
          href: "/nodes",
          link: "Nodes bekijken",
          done: nodes.data ? online.length > 0 : undefined,
        },
        {
          title: "Zet hem in een cluster (als dat nodig is)",
          uitleg: "Open de node, klik Bewerken en kies het cluster. Hoort hij nergens bij, laat het dan leeg.",
        },
      ],
    },
    {
      id: "cluster-maken",
      title: "Een nieuw cluster maken",
      wanneer: "Als je een dienst wilt die blijft werken als één server uitvalt.",
      steps: [
        {
          title: "Kies een template",
          uitleg: (
            <>
              Een <Term id="template">template</Term> is een kant-en-klaar recept, zoals een MariaDB-cluster of een webcluster.
            </>
          ),
          href: "/templates",
          link: "Naar Templates",
        },
        {
          title: "Vul naam, aantal servers en het gedeelde adres in",
          uitleg: (
            <>
              Het <Term id="vip">VIP</Term> is het adres dat gebruikers gebruiken; kies een vrij adres in hetzelfde netwerk, nooit .1 tot .3 (die
              zijn voor de router).
            </>
          ),
        },
        {
          title: "Rol uit en volg de taak",
          uitleg: "ClusterForge maakt de machines, installeert alles en zet het VIP op. Bij Taken zie je elke stap.",
          href: "/taken",
          link: "Naar Taken",
          done: clusters.data ? clusters.data.length > 0 : undefined,
        },
        {
          title: "Test of overnemen werkt",
          uitleg: (
            <>
              Met een <Term id="failover">failover</Term>-test zet ClusterForge de actieve server even uit en meet hoe snel een andere overneemt.
            </>
          ),
          href: "/tests",
          link: "Naar Tests",
        },
      ],
    },
    {
      id: "herstarten",
      title: "Een server veilig herstarten of onderhouden",
      wanneer: "Voor een update, een kernel of als een server raar doet.",
      steps: [
        {
          title: "Zet de node in onderhoud",
          uitleg: (
            <>
              Open de node → Levenscyclus → Onderhoud. In een cluster verhuist het werk (het VIP) eerst naar een andere node, en er komen geen
              valse alarmen. <Term id="onderhoud">Meer uitleg</Term>
            </>
          ),
          href: "/nodes",
          link: "Kies een node",
        },
        { title: "Herstart", uitleg: "Op dezelfde kaart: Herstarten. ClusterForge wacht tot de agent terug is." },
        { title: "Zet onderhoud weer uit", uitleg: "Dan doet de node weer mee. Controleer dat het cluster groen staat." },
      ],
    },
    {
      id: "updates",
      title: "Updates installeren",
      wanneer: "Elke week of twee, en meteen bij een lek dat actief misbruikt wordt.",
      steps: [
        {
          title: "Bekijk welke updates er zijn",
          uitleg: "Bij Updates staat per server wat er klaar staat. Rood zijn beveiligingsupdates.",
          href: "/updates",
          link: "Naar Updates",
        },
        {
          title: "Werk bij, cluster per cluster",
          uitleg: "ClusterForge doet in een cluster één server tegelijk, zodat de dienst blijft draaien. Op productie vraagt hij een extra bevestiging.",
        },
        {
          title: "Herstart waar het gevraagd wordt",
          uitleg: "Na sommige updates (zoals de kernel) staat 'herstart nodig'. Volg dan 'Een server veilig herstarten'.",
        },
      ],
    },
    {
      id: "schijf",
      title: "De schijf raakt vol",
      wanneer: "Als Aandacht nodig 'schijf raakt vol' of 'schijf vol' toont.",
      steps: [
        {
          title: "Open de node bij Schijfruimte",
          uitleg: "Klik op de melding bij Aandacht nodig; je komt meteen op de goede plek.",
          href: "/",
          link: "Naar het overzicht",
          done: att.data ? !has("disk-") : undefined,
        },
        { title: "Kijk wat ruimte inneemt", uitleg: "Dit meet alleen en verandert niets. Je ziet wat veilig weg kan en de grootste mappen." },
        { title: "Maak ruimte vrij", uitleg: "Wat aangevinkt staat, is veilig: oude logboeken, gedownloade updates, crashbestanden." },
        {
          title: "Nog te vol? Maak de schijf groter",
          uitleg: "Is het je eigen data (zoals AI-modellen of een database), vergroot dan de schijf in Proxmox → Gasten.",
          href: "/proxmox/gasten",
          link: "Naar Gasten",
        },
      ],
    },
    {
      id: "rood",
      title: "Er staat iets in het rood: wat nu?",
      wanneer: "Bij een melding of als een dienst niet werkt.",
      steps: [
        {
          title: "Kijk bij Aandacht nodig",
          uitleg: "Op het overzicht. Klik op 'Wat doen?' naast de melding voor uitleg in gewone taal.",
          href: "/",
          link: "Naar het overzicht",
          done: att.data ? !items.some((i) => i.tone === "red") : undefined,
        },
        {
          title: "Open het incident",
          uitleg: "ClusterForge toont wat er net voordien veranderde. De laatste wijziging is meestal de oorzaak.",
          href: "/incidenten",
          link: "Naar Incidenten",
        },
        {
          title: "Vraag het de assistent",
          uitleg: "Typ in gewone taal, zoals 'waarom ligt web plat?'. De assistent kijkt in de gegevens van ClusterForge.",
          href: "/assistent",
          link: "Naar de Assistent",
        },
      ],
    },
    {
      id: "backups",
      title: "Back-ups controleren",
      wanneer: "Elke maand, en zeker voor een grote wijziging.",
      steps: [
        {
          title: "Heeft elke machine een recente back-up?",
          uitleg: "Bij Backup Server → Dekking zie je welke machines geen of een te oude back-up hebben.",
          href: "/pbs/dekking",
          link: "Naar Dekking",
          done: att.data ? !has("pbs-backup") : undefined,
        },
        {
          title: "Zet een back-up proef terug",
          uitleg: "Bij Back-ups → Controle zet ClusterForge een back-up terug in een afgeschermde proefomgeving en kijkt of hij werkt.",
          href: "/back-ups",
          link: "Naar Back-ups",
        },
      ],
    },
  ];

  return (
    <div className="space-y-6">
      <PageHeader
        title="Aan de slag"
        description="De gewone taken, stap voor stap. Bij elke stap staat wat er gebeurt; een vinkje betekent dat ClusterForge zag dat hij al gedaan is."
      />
      <div className="space-y-3">
        {tasks.map((t, i) => (
          <TaskCard key={t.id} task={t} defaultOpen={i === 0 && t.steps.some((s) => s.done === false)} />
        ))}
      </div>
      <p className="text-sm text-slate-500">
        Een woord niet begrepen? Kijk in de{" "}
        <Link href="/uitleg" className="underline">
          woordenlijst
        </Link>
        .
      </p>
    </div>
  );
}

function TaskCard({ task, defaultOpen }: { task: Task; defaultOpen: boolean }) {
  const [open, setOpen] = useState(defaultOpen);
  const checked = task.steps.filter((s) => s.done !== undefined);
  const done = checked.filter((s) => s.done).length;
  const allDone = checked.length > 0 && done === checked.length;
  return (
    <Card>
      <button type="button" onClick={() => setOpen((o) => !o)} aria-expanded={open} className="flex w-full items-start gap-3 text-left">
        <span className="min-w-0 flex-1">
          <span className="flex flex-wrap items-center gap-2">
            <span className="text-base font-semibold">{task.title}</span>
            {checked.length > 0 && (
              <Badge tone={allDone ? "green" : "amber"}>{allDone ? "klaar" : `${done} van ${checked.length} gedaan`}</Badge>
            )}
          </span>
          <span className="mt-0.5 block text-sm text-slate-500">{task.wanneer}</span>
        </span>
        <span aria-hidden className={cx("mt-1 text-slate-400 transition", open && "rotate-90")}>
          ›
        </span>
      </button>
      {open && (
        <ol className="mt-4 space-y-3">
          {task.steps.map((s, i) => (
            <li key={s.title} className="flex gap-3">
              <span
                className={cx(
                  "flex size-6 shrink-0 items-center justify-center rounded-full text-xs font-semibold",
                  s.done ? "bg-emerald-500 text-white" : "bg-slate-200 text-slate-700 dark:bg-slate-700 dark:text-slate-200",
                )}
                aria-label={s.done ? "gedaan" : `stap ${i + 1}`}
              >
                {s.done ? "✓" : i + 1}
              </span>
              <div className="min-w-0 flex-1 text-sm">
                <p className="font-medium">{s.title}</p>
                <p className="mt-0.5 text-slate-600 dark:text-slate-400">{s.uitleg}</p>
                {s.href && (
                  <Link href={s.href} className="mt-1 inline-block text-brand-700 hover:underline dark:text-sky-300">
                    {s.link ?? "Openen"} ›
                  </Link>
                )}
              </div>
            </li>
          ))}
        </ol>
      )}
    </Card>
  );
}
