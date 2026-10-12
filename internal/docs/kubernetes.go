package docs

import (
	"slices"
	"strconv"
	"strings"

	"github.com/Jonasz1996/clusterforge/internal/kube"
	"github.com/Jonasz1996/clusterforge/internal/risk"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// snapshotDir is waar k3s de etcd-snapshots van een server bewaart.
const snapshotDir = "/var/lib/rancher/k3s/server/db/snapshots"

// shortK3s maakt van v1.36.5+k3s1 v1.36.5.
func shortK3s(v string) string {
	if i := strings.Index(v, "+"); i > 0 {
		return v[:i]
	}
	return v
}

// kubeHost is de ClusterForge-node van een node in Kubernetes, of zijn naam
// daar.
func kubeHost(n KubeNode) string { return or(n.Hostname, n.Name) }

// kubernetes is het hoofdstuk Kubernetes: wat er in het cluster draait,
// en de runbooks voor een node vervangen, bijwerken en etcd terugzetten.
func kubernetes(c *Cluster, o Opts) string {
	d := &doc{}
	header(d, c, KindKubernetes)
	k := c.Kube
	d.p(esc(c.Name)+" is een Kubernetes-cluster met k3s.",
		"ClusterForge leest het elke 30 seconden via de agent op een server, en de webadressen en de opslag elke vijf minuten.",
		"Dit hoofdstuk beschrijft wat het laatst zag; hoe het er nu voor staat, zie je in ClusterForge onder Kubernetes.")
	if !k.Synced {
		d.p("ClusterForge kon dit cluster nog niet lezen. Draait de agent op een server, met protocol 8 of nieuwer?")
	}

	var servers, workers []string
	for _, n := range k.Nodes {
		if n.Server {
			servers = append(servers, esc(kubeHost(n)))
		} else {
			workers = append(workers, esc(kubeHost(n)))
		}
	}
	version, support, upgrade := "", "", ""
	if k.Version != "" {
		version = "k3s " + code(shortK3s(k.Version))
		if k.TemplateK3s != "" && k.TemplateK3s != k.Version {
			version += "; de template hoort bij " + code(shortK3s(k.TemplateK3s)) + ", dus het bijwerken is niet af"
		}
		if minor, end, ok := risk.KubeEOL(k.Version); ok {
			support = "Kubernetes " + minor + " krijgt updates tot " + months[end.Month()-1] + " " + strconv.Itoa(end.Year())
		}
	}
	if k.NextK3s != "" {
		upgrade = "naar k3s " + code(shortK3s(k.NextK3s)) + " met " + code(c.Template.Name) + " " + esc(k.NextVersion) + "; zie hieronder"
	}
	api := ""
	if len(c.VIPs) > 0 {
		api = code("https://"+c.VIPs[0].Address+":6443") + ", via het VIP"
	}
	d.facts([][2]string{
		{"Versie", version},
		{"Ondersteuning", support},
		{"Bijwerken", upgrade},
		{"API", api},
		{"Servers", list(servers)},
		{"Workers", list(workers)},
	})

	d.h2("Nodes")
	if len(k.Nodes) == 0 {
		d.p("Nog geen nodes gelezen.")
	} else {
		var rows [][]string
		for _, n := range k.Nodes {
			role := "worker"
			if n.Server {
				role = "server"
			}
			cpu := ""
			if n.CPUMillis > 0 {
				cpu = decimal(float64(n.CPUMillis)/1000, 1) + " cores"
				cpu = strings.Replace(cpu, ",0 ", " ", 1)
			}
			mem := ""
			if n.MemoryBytes > 0 {
				mem = size(n.MemoryBytes)
			}
			rows = append(rows, []string{"**" + esc(n.Name) + "**", role, esc(n.Hostname), esc(n.IP), code(shortK3s(n.Kubelet)), cpu, mem})
		}
		d.table([]string{"Node", "Rol", "ClusterForge-node", "Adres", "k3s", "CPU", "Geheugen"}, rows)
		d.p("CPU en geheugen zijn wat Kubernetes aan pods kan geven (allocatable), iets minder dan de VM heeft.",
			"Een server draait de API, de scheduler en etcd, en ook gewone pods; een worker alleen pods.")
	}

	d.h2("Namespaces")
	if inv := k.Inventory; inv != nil {
		var own, system []string
		for _, ns := range inv.Namespaces {
			switch {
			case kube.IsSystem(ns) || ns == "default":
				system = append(system, code(ns))
			case ns == protocol.KubeAccessNamespace:
				system = append(system, code(ns)+" (de kubeconfigs van ClusterForge)")
			default:
				own = append(own, code(ns))
			}
		}
		if len(own) > 0 {
			d.p("Eigen namespaces: " + list(own) + ".")
		} else {
			d.p("Er zijn nog geen eigen namespaces.")
		}
		d.p("Van Kubernetes en ClusterForge: " + list(system) + ".")
	} else {
		d.p("Nog niet gelezen.")
	}

	d.h2("Apps")
	var rows [][]string
	var system []string
	for _, a := range k.Apps {
		if a.System {
			system = append(system, code(a.Name))
			continue
		}
		images := make([]string, len(a.Images))
		for i, img := range a.Images {
			images[i] = code(img)
		}
		pods := strconv.Itoa(a.Desired)
		if a.Kind == "DaemonSet" {
			pods = "één per node"
		}
		rows = append(rows, []string{"**" + esc(a.Namespace+"/"+a.Name) + "**", esc(a.Kind), pods, strings.Join(images, ", ")})
	}
	if len(rows) > 0 {
		d.table([]string{"App", "Soort", "Pods", "Images"}, rows)
	} else {
		d.p("Er draaien nog geen eigen apps. Zet er een neer in ClusterForge bij Kubernetes, tabblad Toepassen.")
	}
	if len(system) > 0 {
		slices.Sort(system)
		system = slices.Compact(system)
		d.p("Van k3s zelf, in " + code("kube-system") + ": " + list(system) + ".")
	}

	d.h2("Webadressen")
	switch inv := k.Inventory; {
	case inv == nil:
		d.p("Nog niet gelezen.")
	case len(inv.Addresses) == 0:
		d.p("Er is nog geen Ingress en geen Service met een adres buiten het cluster.")
	default:
		var rows [][]string
		for _, a := range inv.Addresses {
			where := a.URL
			if where == "" {
				where = a.Host + a.Path
			}
			to := ""
			if a.Kind == "Ingress" {
				to = "Service " + code(a.Service)
				if a.Port != "" {
					to += ", poort " + esc(a.Port)
				}
			} else {
				to = esc(a.Text)
			}
			rows = append(rows, []string{code(or(where, "–")), esc(a.Kind + " " + a.Namespace + "/" + a.Name), to})
		}
		d.table([]string{"Adres", "Via", "Naar"}, rows)
		d.p("Traefik, de ingress-controller van k3s, luistert op poort 80 en 443 van elke node. Wijst een DNS-naam naar het VIP, dan blijft het adres werken als een node uitvalt.")
	}

	d.h2("Opslag")
	switch inv := k.Inventory; {
	case inv == nil:
		d.p("Nog niet gelezen.")
	case len(inv.Volumes) == 0:
		d.p("Er is nog geen PersistentVolumeClaim: geen app bewaart data op een volume.")
	default:
		var rows [][]string
		local := false
		for _, v := range inv.Volumes {
			where := ""
			if v.Node != "" {
				where = "op " + esc(v.Node)
				if v.Path != "" {
					where += " in " + code(v.Path)
				}
				local = true
			}
			rows = append(rows, []string{"**" + esc(v.Namespace+"/"+v.Name) + "**", esc(v.Class), esc(v.Size), where, esc(strings.Join(v.UsedBy, ", "))})
		}
		d.table([]string{"Claim", "Klasse", "Grootte", "Waar", "Gebruikt door"}, rows)
		if local {
			d.p("Een volume met een node staat op de schijf van die node, zoals bij local-path.",
				"Valt die node weg, dan kan de pod nergens anders starten; de back-up van de node is dan de back-up van de data.")
		}
	}
	if inv := k.Inventory; inv != nil && inv.DefaultClass != "" {
		d.p("Een claim zonder klasse krijgt " + code(inv.DefaultClass) + ".")
	}

	d.h2("etcd-snapshots")
	// De server die het cluster begon; met één server is dat hij.
	init := "de eerste server (die met cluster-init)"
	switch {
	case k.Init != "":
		init = esc(k.Init)
	case len(servers) == 1:
		init = servers[0]
	}
	snap := "k3s maakt elke twaalf uur een snapshot van zijn database (etcd) op elke server, en bewaart er vijf, in " + code(snapshotDir) + "."
	switch k.Fresh {
	case "ok":
		snap += " Er " + map[bool]string{true: "is er", false: "zijn er"}[k.Snapshots == 1] + " " + strconv.Itoa(k.Snapshots) + "; de nieuwste is minder dan een dag oud."
	case "stale":
		snap += " De nieuwste is ouder dan een dag: kijk in ClusterForge bij Kubernetes, tabblad Snapshots."
	case "none":
		snap += " Er is er nog geen."
	default:
		snap += " ClusterForge kon de lijst nog niet lezen."
	}
	d.p(snap, "Alleen een snapshot op "+init+" kan terug: die server begon het cluster, de andere melden zich bij hem aan.",
		"De back-ups van de VM's in Proxmox blijven de eerste lijn; een snapshot zet alleen Kubernetes terug, niet de data in de volumes.")

	d.h2("Een node vervangen")
	git := c.GitLinked
	if git {
		d.p("Dit cluster volgt Git: verander het aantal nodes in " + code(yamlName(c)) + " en keur de wijziging goed. ClusterForge doet dan wat hieronder staat.")
	}
	d.h3("Een worker")
	d.steps([]string{
		"Zet een nieuwe worker erbij: in ClusterForge bij Kubernetes, tabblad Nodes, **Worker toevoegen**. ClusterForge maakt de VM uit het golden image, installeert k3s en wacht tot hij Ready is.",
		"Haal de oude weg met **Weghalen** bij die worker. ClusterForge maakt hem eerst leeg (cordon en drain): zijn pods starten op de andere nodes. Daarna haalt het hem uit Kubernetes en verwijdert het de VM.",
		"Kijk of alles weer draait:",
	})
	d.code("sh", "sudo kubectl get nodes\nsudo kubectl get pods -A -o wide")
	d.h3("Een server")
	if len(servers) <= 1 {
		d.p("Met één server is die server het cluster: is hij stuk, zet hem dan terug uit de back-up (hoofdstuk "+link("Runbooks", KindRunbooks+".md")+"), of zet een etcd-snapshot terug zoals hieronder.",
			"Wil je dat een server mag uitvallen, ga dan naar drie servers: tabblad Nodes, **Servers naar 3**.")
	} else {
		d.p("Een server van "+strconv.Itoa(len(servers))+" mag uitvallen: de andere houden de API en etcd in de lucht.",
			"Is de VM stuk, zet hem dan terug uit de back-up (hoofdstuk "+link("Runbooks", KindRunbooks+".md")+"); bij het starten haalt etcd bij wat hij miste.",
			"Vallen er meer uit dan de helft, dan stopt etcd: zet dan een etcd-snapshot terug zoals hieronder.")
	}

	d.h2("Bijwerken")
	how := "Kies in ClusterForge op het overzicht van het cluster, bij Kubernetes-versie, **Bijwerken naar k3s …**. Het plan toont wat er per node verandert."
	if k.NextK3s != "" {
		how = "Kies in ClusterForge op het overzicht van het cluster, bij Kubernetes-versie, **Bijwerken naar k3s " + esc(shortK3s(k.NextK3s)) + "…**. Het plan toont wat er per node verandert."
	}
	if git {
		how = "Zet in " + code(yamlName(c)) + " de templateversie op de nieuwe versie en keur de wijziging goed; het plan toont wat er per node verandert."
	}
	order := "eerst de servers, dan de workers"
	if len(servers) > 0 {
		order = "eerst " + list(servers)
		if len(workers) > 0 {
			order += ", dan " + list(workers)
		}
	}
	d.steps([]string{
		"Maak een snapshot: tabblad Snapshots, **Snapshot nu**.",
		how,
		"ClusterForge werkt de nodes één voor één bij, " + order + ". Per node: leegmaken, de nieuwe k3s, herstarten, wachten tot de node Ready is met de nieuwe versie, en weer pods.",
		"Komt een node niet terug, dan stopt de taak en blijft die node op cordon; de nodes erna houden de oude versie. Kijk op de node wat er misging, los het op en kies **Opnieuw toepassen** op de pagina van het cluster.",
	})
	d.code("sh", "sudo journalctl -u k3s -n 50        # op een server\nsudo journalctl -u k3s-agent -n 50  # op een worker\nsudo kubectl get nodes              # kolom VERSION")
	d.p("Kubernetes gaat één minorversie per keer omhoog en nooit terug; ClusterForge biedt daarom alleen de volgende aan.")

	d.h2("etcd terugzetten")
	if c.Env == "prod" {
		d.p("Op prod zet ClusterForge geen snapshot terug: het cluster ligt dan een paar minuten stil, en alles wat na de snapshot veranderde, is weg. Moet het toch, doe het dan zo, als root op de servers.")
	} else {
		d.p("Kies in ClusterForge bij Kubernetes, tabblad Snapshots, **Terugzetten** bij een snapshot op " + init + ". ClusterForge doet dan deze stappen; zo kun je ze ook zelf doen als ClusterForge er niet bij kan.")
	}
	all := list(servers)
	if all == "" {
		all = "elke server"
	}
	d.p("**1.** Maak eerst een snapshot van nu, op " + init + ":")
	d.code("sh", "sudo k3s etcd-snapshot save --name voor-terugzetten")
	d.p("**2.** Stop k3s op " + all + ":")
	d.code("sh", "sudo systemctl stop k3s")
	d.p("**3.** Zet de snapshot terug op " + init + ". Kies het bestand met " + code("ls") + ":")
	d.code("sh", "ls -lh "+snapshotDir+"\nsudo k3s server --cluster-reset \\\n  --cluster-reset-restore-path="+snapshotDir+"/<bestand>")
	d.p("**4.** Start k3s op " + init + ":")
	d.code("sh", "sudo systemctl start k3s\nsudo kubectl get nodes")
	if len(servers) > 1 {
		var others []string
		for _, n := range k.Nodes {
			if n.Server && kubeHost(n) != k.Init {
				others = append(others, esc(kubeHost(n)))
			}
		}
		d.p("**5.** Zet op " + list(others) + " de database opzij en start k3s, zodat ze zich opnieuw aanmelden:")
		d.code("sh", "sudo mv /var/lib/rancher/k3s/server/db /var/lib/rancher/k3s/server/db-voor-terugzetten\n"+
			"sudo mkdir -m 700 /var/lib/rancher/k3s/server/db\n"+
			"sudo mv /var/lib/rancher/k3s/server/db-voor-terugzetten/snapshots /var/lib/rancher/k3s/server/db/\n"+
			"sudo systemctl start k3s")
	}
	d.p("De workers melden zich vanzelf weer. Kijk tot slot met "+code("sudo kubectl get nodes")+" of elke node Ready is.",
		"Gaat het mis, dan staat de oude database van "+init+" in "+code("/var/lib/rancher/k3s/server/db/etcd-old-…")+".")
	return d.String()
}
