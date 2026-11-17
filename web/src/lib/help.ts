// Uitleg in gewone taal (fase 41): per pagina "Wat is dit?" en een
// woordenlijst met de vaktermen. Zo kan ook wie de techniek niet kent een
// cluster beheren. Een pagina zonder eigen uitleg krijgt die van het
// dichtstbijzijnde menu-item erboven.

export type PageHelp = {
  // wat: wat je op deze pagina ziet, in één of twee zinnen.
  wat: string;
  // waarom: waarom het nuttig is.
  waarom?: string;
  // doen: wat je hier kunt doen, als korte stappen.
  doen?: string[];
  // termen: woorden uit de woordenlijst die hier vaak voorkomen.
  termen?: string[];
};

export const pageHelp: Record<string, PageHelp> = {
  "/": {
    wat: "Het overzicht van al je servers en clusters in één scherm.",
    waarom: "Je ziet meteen of alles werkt. Bovenaan staat Aandacht nodig: alleen wat nu een actie vraagt.",
    doen: [
      "Staat er iets bij Aandacht nodig? Klik op 'Wat doen?' voor uitleg, of op de regel om het op te lossen.",
      "Nieuw hier? Begin bij Aan de slag: daar staan de gewone taken stap voor stap.",
    ],
    termen: ["cluster", "node", "agent"],
  },
  "/aan-de-slag": {
    wat: "Begeleide taken, stap voor stap: van de eerste keer instellen tot een server veilig herstarten.",
    waarom: "Je hoeft niet te weten waar alles zit. Elke stap zegt wat hij doet en of hij al gedaan is.",
  },
  "/uitleg": {
    wat: "De woordenlijst: de vaktermen die je in ClusterForge tegenkomt, in gewone taal.",
  },
  "/clusters": {
    wat: "Je clusters: groepen servers die samen één dienst leveren, zoals een website of een database.",
    waarom: "Valt één server uit, dan neemt een andere het over. Hier zie je of dat nog kan.",
    doen: [
      "Groen is gezond, oranje werkt met minder reserve, rood ligt plat.",
      "Klik op een cluster voor de details, of op 'Nieuw cluster' om er een te maken uit een template.",
    ],
    termen: ["cluster", "vip", "failover", "keepalived"],
  },
  "/clusters/detail": {
    wat: "Alles over één cluster: de servers erin, wie het gedeelde adres (VIP) heeft, de diensten en wat er recent veranderde.",
    doen: [
      "Bovenaan staat de status en waarom.",
      "Onderhoud aan een server? Zet die node eerst in onderhoud: dan verhuist het werk netjes naar de andere.",
      "Met 'Failover testen' controleer je of het overnemen echt werkt.",
    ],
    termen: ["vip", "failover", "split-brain", "drift"],
  },
  "/nodes": {
    wat: "Al je servers (nodes): virtuele machines, containers en fysieke machines met de ClusterForge-agent.",
    waarom: "Hier zie je per server of hij bereikbaar is, hoe druk hij het heeft en of er updates zijn.",
    doen: [
      "Een nieuwe server toevoegen: 'Agent installeren' geeft één commando dat je op die server plakt.",
      "Klik op een node voor de details, herstarten, onderhoud en schijfruimte.",
    ],
    termen: ["node", "agent", "lxc", "vm"],
  },
  "/nodes/detail": {
    wat: "Alles over één server: de agent, schijfruimte, updates, diensten, netwerk en wat er recent veranderde.",
    doen: [
      "Herstarten of onderhoud: bij Levenscyclus. In een cluster verhuist het werk eerst naar een andere node.",
      "Schijf vol? Bij Schijfruimte meet je wat ruimte inneemt en maak je veilig ruimte vrij.",
    ],
    termen: ["agent", "onderhoud", "drift"],
  },
  "/kubernetes": { wat: "Kubernetes-clusters (k3s) en hun pods, als je die gebruikt. Gebruik je geen Kubernetes, dan kun je deze pagina negeren.", termen: ["kubernetes"] },
  "/kaart": {
    wat: "Een tekening van je netwerk: welke servers in welk netwerk (VLAN) zitten en hoe ze verbonden zijn.",
    waarom: "Handig om te zien wat waar staat, en om te documenteren.",
    termen: ["vlan", "vip"],
  },
  "/afhankelijkheden": {
    wat: "Welke dienst van welke andere afhangt, bijvoorbeeld een website van een database.",
    waarom: "Zo zie je wat er allemaal stilvalt als één onderdeel uitvalt.",
  },
  "/proxmox/datacenter": {
    wat: "Je Proxmox-servers: de fysieke machines waarop je virtuele machines en containers draaien.",
    waarom: "Hier zie je of ze samen nog een geheel vormen (quorum) en hoeveel ruimte en geheugen er over is.",
    doen: ["Een host onderhouden of herstarten: zet hem eerst in onderhoudsmodus; zijn gasten verhuizen dan naar een andere host."],
    termen: ["proxmox", "quorum", "host"],
  },
  "/proxmox/gasten": {
    wat: "Alle virtuele machines (VM) en containers (CT) op Proxmox.",
    doen: ["Starten, stoppen, een momentopname (snapshot) maken, een schijf vergroten of verhuizen naar een andere host."],
    termen: ["vm", "lxc", "snapshot"],
  },
  "/proxmox/opslag": { wat: "De opslag van Proxmox: waar de schijven van je machines en de back-ups staan, en hoe vol die is.", termen: ["opslag"] },
  "/proxmox/ha": { wat: "Proxmox-HA: machines die Proxmox vanzelf op een andere host herstart als hun host uitvalt.", termen: ["ha", "failover"] },
  "/proxmox/netwerk": { wat: "De netwerkinstellingen van de Proxmox-hosts: bridges en VLAN's.", termen: ["vlan"] },
  "/proxmox/toegang": { wat: "Wie in Proxmox mag inloggen en welke tokens ClusterForge gebruikt." },
  "/proxmox/host-updates": { wat: "Updates voor de Proxmox-hosts zelf. ClusterForge doet ze één host tegelijk, zodat je machines blijven draaien." },
  "/proxmox/backups": { wat: "De back-ups die Proxmox maakt van je machines.", termen: ["back-up"] },
  "/proxmox/taken": { wat: "Wat Proxmox zelf uitvoerde of nog uitvoert, zoals back-ups, verhuizingen en het aanmaken van machines." },
  "/proxmox/syslog": { wat: "Het systeemlogboek van de Proxmox-hosts, om te zoeken wat er op een host gebeurde.", termen: ["log"] },
  "/proxmox/hardware": { wat: "De hardware van je hosts: temperatuur, schijven, doorgegeven kaarten (zoals een GPU) en of een herstart nodig is." },
  "/proxmox/hosts": { wat: "Je machines gegroepeerd per Proxmox-host, zodat je ziet wat waar draait.", termen: ["host"] },
  "/proxmox/replicatie": { wat: "Proxmox kopieert de schijven van machines regelmatig naar een andere host, zodat die kan overnemen.", termen: ["replicatie"] },
  "/proxmox/firewall": { wat: "De firewall die Proxmox zelf rond je hosts en machines legt.", termen: ["firewall"] },
  "/proxmox/certificaten": { wat: "De certificaten (het slotje) van de Proxmox-webinterface en wanneer ze verlopen." },
  "/proxmox": { wat: "De koppeling met Proxmox: het adres en het token waarmee ClusterForge Proxmox bestuurt.", termen: ["proxmox"] },
  "/pbs": {
    wat: "Proxmox Backup Server: de server die je back-ups bewaart.",
    waarom: "Zonder werkende back-ups kun je niets terugzetten na een fout. Hier zie je of elke machine een recente back-up heeft.",
    termen: ["back-up"],
  },
  "/truenas": { wat: "TrueNAS: je opslagserver. Pools, schijven, shares en de bescherming met snapshots.", termen: ["snapshot"] },
  "/opnsense": {
    wat: "Je firewall (OPNsense): regels, netwerken, DHCP, DNS en VPN, zonder de OPNsense-webinterface.",
    waarom: "Elke wijziging maakt eerst een herstelpunt en draait vanzelf terug als je de verbinding verliest.",
    termen: ["firewall", "vlan", "dhcp", "dns"],
  },
  "/docker": {
    wat: "Docker op je servers: containers (apps), stacks (groepen apps), images en volumes (data).",
    doen: ["Een app herstarten, logs bekijken of bijwerken; ongebruikte images opruimen bij Opruimen."],
    termen: ["docker", "container"],
  },
  "/publicatie": {
    wat: "Wat je naar het internet publiceert: websites achter NPM (de reverse proxy) en de login via Authentik.",
    termen: ["reverse proxy", "sso"],
  },
  "/homepage-apps": { wat: "De apps die op je startpagina (homepage) staan, met hun status." },
  "/databases": {
    wat: "Je databases (MariaDB/Galera en PostgreSQL): of ze gezond zijn, back-ups en gebruikers.",
    termen: ["galera", "patroni", "replicatie"],
  },
  "/cronjobs": { wat: "Geplande taken (cronjobs) op je servers, zoals een nachtelijke kopie met rsync.", termen: ["cronjob"] },
  "/templates": {
    wat: "Templates: kant-en-klare recepten om een nieuw cluster te maken, zoals een MariaDB-cluster of een webcluster.",
    doen: ["Kies een template, vul naam en adressen in, en ClusterForge maakt de machines en zet alles op."],
    termen: ["template", "vip"],
  },
  "/recepten": { wat: "Recepten: vaste reeksen stappen die je op een node of cluster kunt uitvoeren." },
  "/updates": {
    wat: "Software-updates voor je servers.",
    waarom: "Updates dichten beveiligingslekken. In een cluster doet ClusterForge één server tegelijk, zodat de dienst blijft draaien.",
    termen: ["update"],
  },
  "/commandos": { wat: "Eigen commando's uitvoeren op één of meer servers, met de uitvoer hier. Alleen voor wie weet wat het commando doet." },
  "/gitops": { wat: "GitOps: de instellingen van je clusters staan ook in Git. Een wijziging daar komt hier eerst ter goedkeuring.", termen: ["gitops"] },
  "/soc": {
    wat: "Het beveiligingsoverzicht: aanvallen, verdachte toestellen en blokkeringen, samengevat in één dreigingsniveau.",
    doen: ["Klik op een regel voor de uitleg; met 'Blokkeren' houd je een IP-adres buiten."],
    termen: ["crowdsec", "firewall"],
  },
  "/aiverslag": { wat: "Het AI-verslag analyseert je netwerk (firewall, CrowdSec, dreigingslijsten) en vat samen wat belangrijk is." },
  "/firewall": { wat: "De firewall op elke server (ufw): welke poorten open staan.", termen: ["firewall", "poort"] },
  "/crowdsec": { wat: "CrowdSec herkent aanvallen in de logboeken en blokkeert de aanvallers automatisch.", termen: ["crowdsec"] },
  "/kwetsbaarheden": {
    wat: "Gekende lekken in de software op je servers, en de verharding (veilige instellingen).",
    doen: ["Begin bij 'actief misbruikt': die lekken gebruiken aanvallers nu. Meestal lost een update het op."],
    termen: ["cve", "update"],
  },
  "/monitoring": { wat: "Grafieken van processor, geheugen, schijf en netwerk per server.", waarom: "Zo zie je of een server het te druk heeft of vol raakt voor het een probleem wordt." },
  "/zabbix": { wat: "Zabbix: een tweede monitoringsysteem dat je servers bewaakt en meldingen geeft.", termen: ["monitoring"] },
  "/logs": { wat: "De logboeken van al je servers op één plek, om te zoeken wat er gebeurde.", termen: ["log"] },
  "/incidenten": {
    wat: "Storingen die ClusterForge vaststelde, met wat er net voordien veranderde.",
    waarom: "Zo vind je sneller de oorzaak: meestal is dat de laatste wijziging.",
  },
  "/tijdlijn": { wat: "Alles wat er gebeurde, op een tijdlijn: wijzigingen, storingen en taken." },
  "/rapporten": { wat: "Rapporten per cluster: beschikbaarheid, back-ups, beveiliging, met een score." },
  "/risico": { wat: "Wat er mis kan gaan voor het misgaat: volle schijven, verouderde software, clusters zonder reserve." },
  "/back-ups": {
    wat: "Controle van je back-ups: ClusterForge zet een back-up terug in een proefomgeving en kijkt of hij werkt.",
    waarom: "Een back-up die je nooit terugzette, is een hoop, geen zekerheid.",
    termen: ["back-up"],
  },
  "/tests": { wat: "Proeven: failover testen en rampen nabootsen, om te zien of je clusters echt overnemen.", termen: ["failover"] },
  "/taken": {
    wat: "Taken: alles wat ClusterForge voor je uitvoert, zoals een update of een herstart, met de voortgang.",
    doen: ["Klik op een taak voor de stappen en de uitvoer. Een mislukte taak zegt onderaan wat er misging."],
  },
  "/documentatie": { wat: "Automatisch gemaakte documentatie van je clusters en netwerk, ook als export naar GitHub." },
  "/assistent": { wat: "De AI-assistent: stel een vraag in gewone taal, zoals 'waarom staat web rood?'." },
  "/logboek": { wat: "Het logboek: wie wat deed en wanneer. Alles wat via ClusterForge gebeurt, staat hier." },
  "/instellingen": { wat: "Je account (wachtwoord, tweestapsverificatie) en de instellingen van ClusterForge zelf.", termen: ["totp"] },
};

// helpFor zoekt de uitleg voor een pad: exact, dan het langste begin.
export function helpFor(path: string | null): PageHelp | null {
  if (!path) return null;
  const p = path.replace(/\/+$/, "") || "/";
  if (pageHelp[p]) return pageHelp[p];
  let best: string | null = null;
  for (const k of Object.keys(pageHelp)) {
    if (k !== "/" && p.startsWith(k + "/") && (!best || k.length > best.length)) best = k;
  }
  return best ? pageHelp[best] : null;
}

export type Term = { term: string; uitleg: string; zie?: string };

// De woordenlijst, alfabetisch op sleutel.
export const glossary: Record<string, Term> = {
  agent: {
    term: "Agent",
    uitleg:
      "Een klein programma van ClusterForge op elke server. Het meldt hoe het met de server gaat en voert uit wat je in ClusterForge vraagt. Je installeert het één keer; daarna gaat alles via deze website.",
  },
  "back-up": { term: "Back-up", uitleg: "Een kopie van een machine of database waarmee je kunt terugzetten na een fout, een hack of een kapotte schijf." },
  cluster: {
    term: "Cluster",
    uitleg: "Een groep servers die samen één dienst leveren. Valt er één uit, dan neemt een andere het over, zodat gebruikers niets merken.",
  },
  container: { term: "Container", uitleg: "Een app die afgeschermd draait met alles wat ze nodig heeft, meestal met Docker." },
  cronjob: { term: "Cronjob", uitleg: "Een taak die een server vanzelf op een vast tijdstip uitvoert, zoals elke nacht een kopie maken." },
  crowdsec: { term: "CrowdSec", uitleg: "Beveiliging die aanvallen herkent in de logboeken en het IP-adres van de aanvaller blokkeert." },
  cve: { term: "CVE / lek", uitleg: "Een gekend beveiligingslek in software, met een nummer. Een update van die software dicht het meestal." },
  dhcp: { term: "DHCP", uitleg: "Geeft toestellen in je netwerk vanzelf een IP-adres. Een reservatie zorgt dat een toestel altijd hetzelfde adres krijgt." },
  dns: { term: "DNS", uitleg: "Zet een naam (zoals nas.thuis) om in een IP-adres, zodat je geen nummers hoeft te onthouden." },
  docker: { term: "Docker", uitleg: "Software om apps in containers te draaien, zodat ze makkelijk te installeren, bij te werken en te verhuizen zijn." },
  drift: {
    term: "Drift (afwijking)",
    uitleg: "De server staat anders ingesteld dan ClusterForge verwacht, omdat iemand of iets er buiten ClusterForge om iets wijzigde.",
  },
  failover: {
    term: "Failover",
    uitleg: "Het overnemen: valt de server met het werk uit, dan neemt een andere server het werk (en het VIP) binnen enkele seconden over.",
  },
  firewall: { term: "Firewall", uitleg: "Bepaalt welk netwerkverkeer binnen of buiten mag. Alles wat niet nodig is, hoort dicht." },
  galera: { term: "Galera", uitleg: "Een manier om MariaDB op drie servers tegelijk te laten draaien, met overal dezelfde gegevens." },
  gitops: { term: "GitOps", uitleg: "De instellingen staan als tekst in Git. Zo zie je wie wat veranderde en kun je een oude versie terugzetten." },
  ha: { term: "HA (hoge beschikbaarheid)", uitleg: "Zo opgezet dat een dienst blijft werken als een onderdeel uitvalt." },
  host: { term: "Host", uitleg: "De fysieke machine waarop virtuele machines en containers draaien." },
  keepalived: { term: "Keepalived", uitleg: "Het programma dat bepaalt welke server in een cluster het VIP heeft, en het verhuist bij uitval." },
  kubernetes: { term: "Kubernetes", uitleg: "Een groot systeem om veel containers over veel servers te verdelen. ClusterForge heeft het niet nodig." },
  log: { term: "Logboek (log)", uitleg: "De regels die een programma wegschrijft over wat het doet en wat er misgaat." },
  lxc: { term: "LXC / CT (container in Proxmox)", uitleg: "Een lichte virtuele server in Proxmox die de kernel van de host deelt. Sneller en zuiniger dan een VM." },
  monitoring: { term: "Monitoring", uitleg: "Voortdurend meten hoe het met je servers gaat, en melden als iets misgaat." },
  node: { term: "Node", uitleg: "Eén server: een virtuele machine, een container of een fysieke machine. Een cluster bestaat uit nodes." },
  onderhoud: {
    term: "Onderhoud",
    uitleg: "Een node in onderhoud krijgt geen werk meer en geeft geen alarm. Zo kun je hem rustig bijwerken of herstarten.",
  },
  opslag: { term: "Opslag", uitleg: "Waar de schijven van je machines en je bestanden staan: lokaal, op een NAS of verdeeld over servers." },
  patroni: { term: "Patroni", uitleg: "Beheert een PostgreSQL-cluster: kiest welke server de hoofddatabase is en wisselt bij uitval." },
  poort: { term: "Poort", uitleg: "Een nummer waarop een dienst luistert, zoals 443 voor websites. Een open poort is bereikbaar van buitenaf." },
  proxmox: { term: "Proxmox", uitleg: "De software op je fysieke servers waarmee je virtuele machines en containers maakt en beheert." },
  quorum: {
    term: "Quorum",
    uitleg: "De meerderheid. Een cluster beslist alleen als meer dan de helft van de leden elkaar ziet; zo kunnen twee helften niet elk hun eigen gang gaan.",
  },
  replicatie: { term: "Replicatie", uitleg: "Gegevens worden voortdurend naar een andere server gekopieerd, zodat die kan overnemen." },
  "reverse proxy": {
    term: "Reverse proxy (NPM)",
    uitleg: "Eén ingang voor al je websites: ze vangt het verkeer van internet op, zorgt voor het slotje (HTTPS) en stuurt door naar de juiste server.",
  },
  snapshot: { term: "Snapshot (momentopname)", uitleg: "De toestand van een machine op één moment. Ging er iets mis, dan zet je ze snel terug. Geen vervanging voor een back-up." },
  "split-brain": {
    term: "Split-brain",
    uitleg: "Twee servers denken allebei dat zij het werk (het VIP) hebben, meestal omdat ze elkaar niet meer zien. Bezoekers komen willekeurig bij de ene of de andere.",
  },
  sso: { term: "SSO (Authentik)", uitleg: "Eén keer inloggen voor al je apps. Authentik is het programma dat die login regelt." },
  template: { term: "Template", uitleg: "Een kant-en-klaar recept voor een cluster. Je vult een paar gegevens in, ClusterForge doet de rest." },
  totp: {
    term: "Tweestapsverificatie (TOTP)",
    uitleg: "Naast je wachtwoord een code van zes cijfers uit een app op je gsm. Iemand die je wachtwoord kent, kan dan nog niet binnen.",
  },
  update: { term: "Update", uitleg: "Een nieuwere versie van software, meestal met fixes voor fouten en lekken." },
  vip: {
    term: "VIP (virtueel IP-adres)",
    uitleg: "Het gedeelde adres van een cluster. Gebruikers gebruiken altijd dit adres; het hangt aan de server die nu het werk doet en verhuist bij uitval.",
  },
  vlan: { term: "VLAN", uitleg: "Een afgescheiden netwerk binnen hetzelfde fysieke netwerk, bijvoorbeeld apart voor servers, gasten en camera's." },
  vm: { term: "VM (virtuele machine)", uitleg: "Een volledige computer in software, met een eigen besturingssysteem, op een Proxmox-host." },
};
