package kubelessons

import (
	"cmp"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/Jonasz1996/clusterforge/internal/kube"
)

// De lessen. Elke opdracht kan in ClusterForge en met kubectl op een
// server; de controle kijkt naar het resultaat, niet naar hoe het gedaan
// is.

var (
	webRef      = &kube.Ref{Group: "apps", Version: "v1", Resource: "deployments", Namespace: Namespace, Name: "web"}
	notitiesRef = &kube.Ref{Group: "apps", Version: "v1", Resource: "deployments", Namespace: Namespace, Name: "notities"}
	appsLink    = &Link{Label: "Apps in leren", Tab: "apps", Namespace: Namespace}
	podsLink    = &Link{Label: "Pods in leren", Tab: "pods", Namespace: Namespace}
	nodesLink   = &Link{Label: "Nodes", Tab: "nodes"}
)

var lessons = []*Lesson{
	verkennen, eersteApp, schalen, bereikbaar, nieuweVersie, configuratie, opslag, onderhoud, nodeErbij, serverUitval,
}

var verkennen = &Lesson{
	ID: "verkennen", Number: 1, Title: "Je cluster verkennen",
	Summary: "Nodes, namespaces en de systeempods, en wat elk onderdeel doet.",
	Intro: []string{
		"Een Kubernetes-cluster bestaat uit nodes: de machines waarop pods draaien. Een pod is een of meer containers die bij elkaar horen. Ook Kubernetes zelf draait voor een deel als pods.",
		"In deze les kijk je rond en verander je nog niets. Elke vraag beantwoord je door te kijken in ClusterForge of met kubectl op een server.",
	},
	Outro: []string{
		"Je kent nu de bouwstenen: nodes, namespaces en pods, en de systeempods die k3s meebrengt. In les 2 zet je je eerste eigen app neer.",
	},
	Steps: []*Step{
		{
			ID: "nodes", Title: "Tel de nodes", Kind: KindQuiz,
			Text: []string{
				"Een node is een VM waarop k3s draait. Het tabblad Nodes toont ze allemaal, met hun rol en of ze Ready zijn: klaar om pods te draaien.",
			},
			Web:      "Open het tabblad Nodes.",
			Kubectl:  []string{"sudo kubectl get nodes"},
			Question: "Hoeveel nodes heeft je cluster?",
			Hint:     "Tel de rijen op het tabblad Nodes, of de regels onder NAME bij kubectl get nodes.",
			Link:     nodesLink,
			answer: func(x *run) ([]string, string, string, error) {
				nodes, err := x.kube.Nodes(x.ctx, x.env.ClusterID)
				if err != nil {
					return nil, "", "", err
				}
				servers := 0
				for _, n := range nodes {
					if isServer(n) {
						servers++
					}
				}
				return []string{strconv.Itoa(len(nodes))},
					fmt.Sprintf("Juist: %s, waarvan %s.", plural(len(nodes), "node", "nodes"), plural(servers, "server", "servers")), "", nil
			},
		},
		{
			ID: "server", Title: "Zoek een server", Kind: KindQuiz,
			Text: []string{
				"Een server draait het brein van het cluster: de API waarmee kubectl en ClusterForge praten, de planner die beslist op welke node een pod komt, en etcd, de database met alles wat er in het cluster staat. Bij zijn rollen staat `control-plane`.",
				"Een agent, ook worker genoemd, draait alleen pods. Bij k3s draait een server ook pods, tenzij je dat uitzet.",
			},
			Web:      "Kijk op het tabblad Nodes in de kolom Rollen.",
			Kubectl:  []string{"sudo kubectl get nodes"},
			Question: "Geef de naam van een server.",
			Hint:     "Zoek een node met de rol control-plane.",
			Link:     nodesLink,
			answer: func(x *run) ([]string, string, string, error) {
				nodes, err := x.kube.Nodes(x.ctx, x.env.ClusterID)
				if err != nil {
					return nil, "", "", err
				}
				var servers []string
				for _, n := range nodes {
					if isServer(n) {
						servers = append(servers, n.Metadata.Name)
					}
				}
				if len(servers) == 0 {
					return nil, "", "Dit cluster meldt geen node met de rol control-plane; deze vraag slaan we over.", nil
				}
				explain := "Juist. Dit cluster heeft één server: valt die uit, dan werken de pods door, maar kan niemand iets veranderen."
				if len(servers) > 1 {
					explain = fmt.Sprintf("Juist. De servers zijn %s; etcd blijft werken zolang de meerderheid draait.", and(servers))
				}
				return servers, explain, "", nil
			},
		},
		{
			ID: "dns", Title: "Vind de DNS van het cluster", Kind: KindQuiz,
			Text: []string{
				"Pods vinden elkaar op naam, zoals `web` of `web.leren.svc.cluster.local`. Dat doet CoreDNS, en die draait zelf als pod.",
				"De onderdelen van Kubernetes staan in een eigen namespace, apart van je apps. Een namespace is een map voor objecten: een naam hoeft alleen binnen zijn namespace uniek te zijn.",
			},
			Web:      "Open het tabblad Pods en vink Systeem tonen aan.",
			Kubectl:  []string{"sudo kubectl get pods -A"},
			Question: "In welke namespace draait de pod van CoreDNS?",
			Hint:     "De pod heet coredns-…; de namespace staat bij Systeem tonen in de eerste kolom, en bij kubectl get pods -A ook.",
			Link:     &Link{Label: "Pods met systeem", Tab: "pods", System: true},
			answer: func(x *run) ([]string, string, string, error) {
				all, err := x.kube.Pods(x.ctx, x.env.ClusterID, "", "")
				if err != nil {
					return nil, "", "", err
				}
				var ns []string
				for _, p := range all {
					if (strings.HasPrefix(p.Metadata.Name, "coredns-") || p.Metadata.Labels["k8s-app"] == "kube-dns") && !slices.Contains(ns, p.Metadata.Namespace) {
						ns = append(ns, p.Metadata.Namespace)
					}
				}
				if len(ns) == 0 {
					return nil, "", "Er draait geen pod van CoreDNS; deze vraag slaan we over.", nil
				}
				return ns, fmt.Sprintf("Juist: %s. Daar staan ook Traefik, local-path voor volumes en metrics-server voor het gebruik van CPU en geheugen.", ns[0]), "", nil
			},
		},
		{
			ID: "traefik", Title: "Zoek waar Traefik draait", Kind: KindQuiz,
			Text: []string{
				"Traefik ontvangt het webverkeer op poort 80 en 443 en stuurt het naar de juiste app. Je gebruikt hem in les 4.",
				"Kubernetes kiest zelf op welke node een pod komt. Op het tabblad Pods staat dat in de kolom Node; kubectl toont het met `-o wide`.",
				"De pods `svclb-traefik-…` zijn iets anders: die draaien op elke node en sturen poort 80 en 443 van de node door naar Traefik.",
			},
			Web:      "Open het tabblad Pods met Systeem tonen, en zoek de pod traefik-….",
			Kubectl:  []string{"sudo kubectl get pods -n kube-system -o wide"},
			Question: "Op welke node draait de pod van Traefik?",
			Hint:     "Zoek de pod waarvan de naam met traefik- begint, niet met svclb-traefik-, en kijk in de kolom Node.",
			Link:     &Link{Label: "Pods in kube-system", Tab: "pods", Namespace: "kube-system", System: true},
			answer: func(x *run) ([]string, string, string, error) {
				pods, err := x.systemPods("traefik-")
				if err != nil {
					return nil, "", "", err
				}
				nodes := nodesOf(pods)
				if len(nodes) == 0 {
					return nil, "", "Op dit cluster draait geen Traefik; deze vraag slaan we over.", nil
				}
				return nodes, fmt.Sprintf("Juist: Traefik draait op %s. Valt die node uit, dan zet Kubernetes Traefik op een andere.", and(nodes)), "", nil
			},
		},
	},
}

// isServer is true voor een node met de rol control-plane, of master bij
// oudere versies.
func isServer(n kube.Node) bool {
	roles := n.Roles()
	return slices.Contains(roles, "control-plane") || slices.Contains(roles, "master")
}

const webYAML = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: web
  namespace: leren
  labels:
    app: web
spec:
  replicas: %d
  selector:
    matchLabels:
      app: web
  template:
    metadata:
      labels:
        app: web
    spec:
      containers:
        - name: nginx
          image: %s
          ports:
            - name: http
              containerPort: 80
`

var eersteApp = &Lesson{
	ID: "eerste-app", Number: 2, Title: "Je eerste app",
	Summary: "Een Deployment met nginx, en kijken hoe de pod ontstaat.",
	Intro: []string{
		"Je zet een webserver neer: nginx. Je maakt daarvoor niet zelf een pod, maar een Deployment. Die zegt wat er moet draaien en hoe vaak, en Kubernetes zorgt dat het zo blijft.",
		"Alles uit de lessen komt in de namespace `leren`. Met Opruimen haal je die weg, met alles erin.",
	},
	Outro: []string{
		"Je hebt nu een Deployment, die een ReplicaSet maakte, die een pod maakte. Haal je de pod weg, dan maakt de ReplicaSet een nieuwe: je beheert pods altijd via hun eigenaar. Dat zie je in les 3.",
	},
	Steps: []*Step{
		{
			ID: "namespace", Title: "Maak de namespace leren", Kind: KindDo,
			Text: []string{"Een namespace houdt je oefeningen apart van de rest van het cluster."},
			Web:  "Klik hieronder op Controleren en dan op Toepassen.",
			YAML: "apiVersion: v1\nkind: Namespace\nmetadata:\n  name: leren\n",
			Kubectl: []string{
				"sudo kubectl create namespace leren",
				"sudo kubectl get namespaces",
			},
			check: func(x *run, _ Data) (Outcome, error) {
				ok, why, err := x.namespaceExists()
				switch {
				case err != nil:
					return Outcome{}, err
				case ok:
					return Outcome{Done: true, Detail: "De namespace leren bestaat."}, nil
				case why != "":
					return Outcome{Detail: why}, nil
				}
				return Outcome{Detail: "Er is nog geen namespace leren."}, nil
			},
		},
		{
			ID: "deployment", Title: "Maak de Deployment web", Kind: KindDo,
			Text: []string{
				"De Deployment heet `web` en draait één pod met de container `nginx` en het image `nginx:1.28`.",
				"Het label `app: web` verbindt de Deployment met zijn pods: de `selector` zoekt pods met dat label, en de `template` maakt ze ermee.",
			},
			Web:     "Klik hieronder op Controleren en dan op Toepassen.",
			YAML:    fmt.Sprintf(webYAML, 1, "nginx:1.28"),
			Kubectl: []string{"sudo kubectl create deployment web --image=nginx:1.28 -n leren"},
			Link:    appsLink,
			check: func(x *run, _ Data) (Outcome, error) {
				w, _, err := x.deployment("web")
				switch {
				case err != nil:
					return Outcome{}, err
				case w == nil:
					return Outcome{Detail: "Er is nog geen Deployment web in leren."}, nil
				case !strings.HasPrefix(shortImage(image(w)), "nginx"):
					return Outcome{Detail: fmt.Sprintf("web draait %s; voor deze les hoort dat nginx:1.28 te zijn.", image(w))}, nil
				}
				return Outcome{Done: true, Detail: fmt.Sprintf("De Deployment web bestaat, met %s.", image(w))}, nil
			},
		},
		{
			ID: "pod", Title: "Kijk hoe de pod ontstaat", Kind: KindDo,
			Text: []string{
				"De Deployment maakt een ReplicaSet, en die maakt de pod. De planner kiest een node, die node haalt het image op en start de container. Op het tabblad Pods gaat de status van Pending via ContainerCreating naar Running.",
				"De pod heet `web-` met twee stukjes erachter: het eerste hoort bij de ReplicaSet, het tweede bij de pod zelf.",
			},
			Web: "Open het tabblad Pods met namespace leren, en klik op de pod voor zijn gebeurtenissen.",
			Kubectl: []string{
				"sudo kubectl get pods -n leren -w",
				"sudo kubectl describe deployment web -n leren",
			},
			Link: podsLink,
			check: func(x *run, _ Data) (Outcome, error) {
				w, _, err := x.deployment("web")
				if err != nil || w == nil {
					return Outcome{Detail: noWeb}, err
				}
				pods, err := x.pods("web")
				if err != nil {
					return Outcome{}, err
				}
				if w.ReadyPods() < 1 {
					if len(pods) == 0 {
						return Outcome{Detail: "web heeft nog geen pod. Staat hij op 0 pods?"}, nil
					}
					return Outcome{Detail: "De pod is er, maar nog niet klaar: " + podStates(pods) + "." + waitHint(pods)}, nil
				}
				for _, p := range pods {
					if ready(p) {
						return Outcome{Done: true, Detail: fmt.Sprintf("%s draait op %s.", p.Metadata.Name, p.Spec.NodeName)}, nil
					}
				}
				return Outcome{Done: true, Detail: "De pod van web draait."}, nil
			},
		},
	},
}

var schalen = &Lesson{
	ID: "schalen", Number: 3, Title: "Schalen en zelfherstel",
	Summary: "Naar drie pods, één weghalen en zien dat hij terugkomt, en op welke nodes ze staan.",
	Intro: []string{
		"Een Deployment houdt een aantal pods draaiende. Je zet er drie, kijkt waar ze staan, en haalt er een weg om te zien wat Kubernetes dan doet.",
	},
	Outro: []string{
		"Dat is zelfherstel: je zegt hoeveel pods er moeten zijn, niet welke. Valt een pod of zelfs een hele node weg, dan zet Kubernetes er ergens anders een nieuwe neer.",
	},
	Steps: []*Step{
		{
			ID: "drie", Title: "Schaal web naar 3 pods", Kind: KindDo,
			Text: []string{
				"Het aantal staat in `spec.replicas` van de Deployment. Verander je het, dan maakt de ReplicaSet pods bij of haalt hij er weg.",
			},
			Web: "Open het tabblad Apps met namespace leren en kies bij web Schalen.",
			Kubectl: []string{
				"sudo kubectl scale deployment web --replicas=3 -n leren",
				"sudo kubectl get pods -n leren -o wide",
			},
			Link: appsLink,
			check: func(x *run, _ Data) (Outcome, error) {
				w, _, err := x.deployment("web")
				if err != nil || w == nil {
					return Outcome{Detail: noWeb}, err
				}
				switch {
				case w.Desired() != 3:
					return Outcome{Detail: fmt.Sprintf("web staat op %s; zet hem op 3.", plural(w.Desired(), "pod", "pods"))}, nil
				case w.ReadyPods() < 3:
					pods, err := x.pods("web")
					if err != nil {
						return Outcome{}, err
					}
					return Outcome{Detail: fmt.Sprintf("%d van 3 pods klaar.%s", w.ReadyPods(), waitHint(pods))}, nil
				}
				return Outcome{Done: true, Detail: "web draait met 3 pods."}, nil
			},
		},
		{
			ID: "spreiding", Title: "Kijk waar ze staan", Kind: KindQuiz,
			Text: []string{
				"De planner zet pods van één app het liefst op verschillende nodes, zodat het uitvallen van één node ze niet allemaal meeneemt. Hij kijkt daarbij ook naar wat er op elke node vrij is.",
			},
			Web:      "Kijk op het tabblad Pods met namespace leren in de kolom Node.",
			Kubectl:  []string{"sudo kubectl get pods -n leren -o wide"},
			Question: "Op hoeveel verschillende nodes draaien de pods van web?",
			Hint:     "Tel de verschillende namen in de kolom Node.",
			Link:     podsLink,
			answer: func(x *run) ([]string, string, string, error) {
				pods, err := x.pods("web")
				if err != nil {
					return nil, "", "", err
				}
				nodes := nodesOf(pods)
				if len(nodes) == 0 {
					return nil, "", "web heeft geen pods op een node; deze vraag slaan we over.", nil
				}
				return []string{strconv.Itoa(len(nodes))}, fmt.Sprintf("Juist: %s, namelijk %s.", plural(len(nodes), "node", "nodes"), and(nodes)), "", nil
			},
		},
		{
			ID: "zelfherstel", Title: "Haal een pod weg", Kind: KindDo,
			Text: []string{
				"Haal een van de pods van web weg, bijvoorbeeld `{pod}`. De ReplicaSet ziet dat er nog maar twee zijn en maakt meteen een nieuwe, met een andere naam en misschien op een andere node.",
			},
			Web: "Open de pod en kies Weghalen.",
			Kubectl: []string{
				"sudo kubectl delete pod {pod} -n leren",
				"sudo kubectl get pods -n leren",
			},
			Link: &Link{Label: "Pod {pod}", Ref: &kube.Ref{Version: "v1", Resource: "pods", Namespace: Namespace, Name: "{pod}"}},
			start: func(x *run) (Data, error) {
				pods, err := x.pods("web")
				if err != nil {
					return nil, err
				}
				d := Data{"pods": names(pods)}
				if len(pods) > 0 {
					d["pod"] = pods[0].Metadata.Name
				}
				return d, nil
			},
			check: func(x *run, d Data) (Outcome, error) {
				w, _, err := x.deployment("web")
				if err != nil || w == nil {
					return Outcome{Detail: noWeb}, err
				}
				pods, err := x.pods("web")
				if err != nil {
					return Outcome{}, err
				}
				now := names(pods)
				var gone, fresh []string
				for _, n := range d.Strs("pods") {
					if !slices.Contains(now, n) {
						gone = append(gone, n)
					}
				}
				for _, n := range now {
					if !slices.Contains(d.Strs("pods"), n) {
						fresh = append(fresh, n)
					}
				}
				switch {
				case len(gone) == 0:
					return Outcome{Detail: "Alle pods van daarnet draaien nog."}, nil
				case w.ReadyPods() < w.Desired() || len(fresh) == 0:
					return Outcome{Detail: fmt.Sprintf("%s is weg; wachten op de nieuwe: %d van %d klaar.", and(gone), w.ReadyPods(), w.Desired())}, nil
				}
				return Outcome{Done: true, Detail: fmt.Sprintf("%s is weg, en %s kwam ervoor in de plaats.", and(gone), and(fresh))}, nil
			},
		},
	},
}

var bereikbaar = &Lesson{
	ID: "bereikbaar", Number: 4, Title: "Bereikbaar maken",
	Summary: "Een Service en een Ingress, en de app openen in de browser.",
	Intro: []string{
		"Een pod krijgt een adres dat verandert als hij vervangen wordt. Een Service geeft de pods van een app één vast adres en een naam in het cluster. Een Ingress maakt die Service van buiten bereikbaar met een webadres, via Traefik.",
	},
	Outro: []string{
		"Een Service is het vaste adres binnen het cluster, een Ingress het webadres van buiten. De pods erachter mogen komen en gaan; het adres blijft.",
	},
	Steps: []*Step{
		{
			ID: "service", Title: "Maak een Service voor web", Kind: KindDo,
			Text: []string{
				"De Service kiest pods op hun label, `app: web`. Verkeer op poort 80 van de Service gaat naar poort 80 van een van die pods. In het cluster heet hij `web.leren.svc.cluster.local`, of kort `web`.",
			},
			Web: "Klik hieronder op Controleren en dan op Toepassen. Daarna staat hij op het tabblad Netwerk.",
			YAML: `apiVersion: v1
kind: Service
metadata:
  name: web
  namespace: leren
spec:
  selector:
    app: web
  ports:
    - name: http
      port: 80
      targetPort: 80
`,
			Kubectl: []string{
				"sudo kubectl expose deployment web --port=80 -n leren",
				"sudo kubectl get service,endpointslices -n leren",
			},
			Link: &Link{Label: "Netwerk in leren", Tab: "netwerk", Namespace: Namespace},
			check: func(x *run, _ Data) (Outcome, error) {
				svcs, _, err := x.kube.Network(x.ctx, x.env.ClusterID, Namespace)
				if err != nil {
					return Outcome{}, err
				}
				for _, sv := range svcs {
					if sv.Selects(map[string]string{"app": "web"}) {
						return Outcome{Done: true, Detail: fmt.Sprintf("De Service %s kiest de pods van web.", sv.Metadata.Name),
							Data: Data{"service": sv.Metadata.Name}}, nil
					}
				}
				if len(svcs) > 0 {
					return Outcome{Detail: fmt.Sprintf("De Service %s kiest de pods van web niet: zijn selector moet app: web zijn.", svcs[0].Metadata.Name)}, nil
				}
				return Outcome{Detail: "Er is nog geen Service in leren."}, nil
			},
		},
		{
			ID: "ingress", Title: "Maak een Ingress", Kind: KindDo,
			Text: []string{
				"De Ingress zegt tegen Traefik: verkeer voor `{host}` gaat naar de Service `{service}`.",
				"Dat adres gebruikt nip.io: elke naam die eindigt op `{vip}.nip.io` wijst naar {vip}, het VIP van je cluster. Zo hoef je zelf geen DNS in te stellen.",
			},
			Web: "Klik hieronder op Controleren en dan op Toepassen.",
			YAML: `apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: web
  namespace: leren
spec:
  ingressClassName: traefik
  rules:
    - host: {host}
      http:
        paths:
          - path: /
            pathType: Prefix
            backend:
              service:
                name: {service}
                port:
                  number: 80
`,
			Kubectl: []string{
				"sudo kubectl create ingress web --class=traefik --rule='{host}/*={service}:80' -n leren",
				"sudo kubectl get ingress -n leren",
			},
			Link: &Link{Label: "Netwerk in leren", Tab: "netwerk", Namespace: Namespace},
			check: func(x *run, _ Data) (Outcome, error) {
				svc := x.data["service"].Str("service")
				_, ings, err := x.kube.Network(x.ctx, x.env.ClusterID, Namespace)
				if err != nil {
					return Outcome{}, err
				}
				for _, in := range ings {
					for _, r := range in.Rules() {
						if r.Service == svc {
							d := Data{}
							if r.Host != "" {
								d["host"] = r.Host
							}
							where := r.Host
							if where == "" {
								where = "alle webadressen"
							}
							return Outcome{Done: true, Detail: fmt.Sprintf("De Ingress %s stuurt %s naar de Service %s.", in.Metadata.Name, where, svc), Data: d}, nil
						}
					}
				}
				if len(ings) > 0 {
					return Outcome{Detail: fmt.Sprintf("De Ingress %s stuurt niets naar de Service %s.", ings[0].Metadata.Name, svc)}, nil
				}
				return Outcome{Detail: "Er is nog geen Ingress in leren."}, nil
			},
		},
		{
			ID: "openen", Title: "Open je app", Kind: KindRead,
			Text: []string{
				"Open http://{host}/ in je browser. Je ziet de welkomstpagina van nginx.",
				"Zo gaat het verkeer: je browser vraagt nip.io om het adres van `{host}` en krijgt {vip}. Daar luistert Traefik. Die leest de Ingress en stuurt het verzoek naar de Service web, en die naar een van de pods.",
				"Lukt het niet? Kijk of de pods van web klaar zijn, en of je pc het VIP kan bereiken.",
			},
			Kubectl: []string{"curl -i http://{host}/"},
			Done:    "Ik zie nginx",
			Link:    &Link{Label: "http://{host}/", URL: "http://{host}/"},
		},
	},
}

var nieuweVersie = &Lesson{
	ID: "nieuwe-versie", Number: 5, Title: "Een nieuwe versie",
	Summary: "Een nieuw image uitrollen, de uitrol volgen en terugdraaien.",
	Intro: []string{
		"Een nieuwe versie uitrollen is een ander image zetten in de Deployment. Kubernetes vervangt de pods dan een voor een: pas als een nieuwe klaar is, stopt er een oude. Zo blijft de app bereikbaar. Gaat het mis, dan draai je terug.",
	},
	Outro: []string{
		"Een uitrol is een nieuwe ReplicaSet naast de oude. De Deployment bewaart standaard tien oude versies (`revisionHistoryLimit`), zodat je altijd terug kunt.",
	},
	Steps: []*Step{
		{
			ID: "bijwerken", Title: "Rol nginx 1.29 uit", Kind: KindDo,
			Text: []string{
				"web draait nu `{oud}`. Zet het image op `nginx:1.29` en kijk naar de pods: er komt een nieuwe ReplicaSet bij die groeit, terwijl de oude krimpt.",
			},
			Web:  "Klik hieronder op Controleren: in het verschil verandert alleen het image. Dan Toepassen.",
			YAML: fmt.Sprintf(webYAML, 3, "nginx:1.29"),
			Kubectl: []string{
				"sudo kubectl set image deployment/web nginx=nginx:1.29 -n leren",
				"sudo kubectl rollout status deployment/web -n leren",
			},
			Link: appsLink,
			start: func(x *run) (Data, error) {
				w, _, err := x.deployment("web")
				if err != nil || w == nil {
					return Data{}, err
				}
				return Data{"oud": image(w)}, nil
			},
			check: func(x *run, d Data) (Outcome, error) {
				w, _, err := x.deployment("web")
				if err != nil || w == nil {
					return Outcome{Detail: noWeb}, err
				}
				img, old := image(w), d.Str("oud")
				if old == "" {
					return Outcome{Detail: noWeb + " Begin deze les daarna opnieuw."}, nil
				}
				if img == old {
					return Outcome{Detail: fmt.Sprintf("web draait nog %s.", old)}, nil
				}
				if ok, why := rolledOut(w); !ok {
					return Outcome{Detail: fmt.Sprintf("De uitrol naar %s loopt: %s.", img, why)}, nil
				}
				return Outcome{Done: true, Detail: fmt.Sprintf("web draait nu %s op alle %s.", img, plural(w.Desired(), "pod", "pods"))}, nil
			},
		},
		{
			ID: "versie", Title: "Zoek het versienummer", Kind: KindQuiz,
			Text: []string{
				"Elke wijziging aan de template van de pods geeft een nieuwe ReplicaSet met een versienummer. De oude ReplicaSets blijven bestaan met nul pods, zodat je terug kunt. Het nummer staat in de annotatie `deployment.kubernetes.io/revision`.",
			},
			Web:      "Open web en kies Terugdraaien: daar staan de versies, de nieuwste eerst. Sluit het venster daarna zonder iets te doen.",
			Kubectl:  []string{"sudo kubectl rollout history deployment/web -n leren"},
			Question: "Wat is het versienummer van web nu?",
			Hint:     "Neem het hoogste nummer bij Terugdraaien, of de laatste regel van kubectl rollout history.",
			Link:     &Link{Label: "Deployment web", Ref: webRef},
			answer: func(x *run) ([]string, string, string, error) {
				w, _, err := x.deployment("web")
				if err != nil {
					return nil, "", "", err
				}
				if w == nil || w.Metadata.Annotations["deployment.kubernetes.io/revision"] == "" {
					return nil, "", "web heeft geen versienummer; deze vraag slaan we over.", nil
				}
				rev := w.Metadata.Annotations["deployment.kubernetes.io/revision"]
				return []string{rev}, fmt.Sprintf("Juist: versie %s.", rev), "", nil
			},
		},
		{
			ID: "terugdraaien", Title: "Draai terug", Kind: KindDo,
			Text: []string{
				"Stel dat de nieuwe versie een fout heeft. Draai terug naar `{oud}`: de Deployment zet de template van de vorige ReplicaSet terug, en die krijgt het volgende versienummer.",
			},
			Web: "Open web op het tabblad Apps en kies Terugdraaien.",
			Kubectl: []string{
				"sudo kubectl rollout undo deployment/web -n leren",
				"sudo kubectl rollout status deployment/web -n leren",
			},
			Link: appsLink,
			check: func(x *run, _ Data) (Outcome, error) {
				w, _, err := x.deployment("web")
				if err != nil || w == nil {
					return Outcome{Detail: noWeb}, err
				}
				img, old := image(w), x.data["bijwerken"].Str("oud")
				if img != old {
					return Outcome{Detail: fmt.Sprintf("web draait %s; terug naar %s.", img, old)}, nil
				}
				if ok, why := rolledOut(w); !ok {
					return Outcome{Detail: fmt.Sprintf("Het terugdraaien loopt: %s.", why)}, nil
				}
				rev := w.Metadata.Annotations["deployment.kubernetes.io/revision"]
				return Outcome{Done: true, Detail: fmt.Sprintf("web draait weer %s, nu als versie %s.", img, rev)}, nil
			},
		},
	},
}

var configuratie = &Lesson{
	ID: "configuratie", Number: 6, Title: "Configuratie en geheimen",
	Summary: "Een ConfigMap en een Secret als omgevingsvariabelen.",
	Intro: []string{
		"Een image is overal hetzelfde. Wat per omgeving verschilt, zoals een taal of een wachtwoord, zet je erbuiten: gewone instellingen in een ConfigMap, geheimen in een Secret. Een pod leest ze bijvoorbeeld als omgevingsvariabelen.",
	},
	Outro: []string{
		"Dezelfde Deployment kan nu in een andere namespace draaien met andere instellingen. Een Secret is niet versleuteld voor wie Secrets in de namespace mag lezen: geef die rechten dus zuinig.",
	},
	Steps: []*Step{
		{
			ID: "configmap", Title: "Maak de ConfigMap instellingen", Kind: KindDo,
			Text: []string{"Een ConfigMap is een lijst sleutels met waarden. Deze heeft er twee: `TAAL` en `KLEUR`."},
			Web:  "Klik hieronder op Controleren en dan op Toepassen. Daarna staat hij op het tabblad Configuratie.",
			YAML: `apiVersion: v1
kind: ConfigMap
metadata:
  name: instellingen
  namespace: leren
data:
  TAAL: nl
  KLEUR: blauw
`,
			Kubectl: []string{
				"sudo kubectl create configmap instellingen --from-literal=TAAL=nl --from-literal=KLEUR=blauw -n leren",
				"sudo kubectl describe configmap instellingen -n leren",
			},
			Link: &Link{Label: "Configuratie in leren", Tab: "configuratie", Namespace: Namespace},
			check: func(x *run, _ Data) (Outcome, error) {
				maps, _, err := x.kube.Config(x.ctx, x.env.ClusterID, Namespace)
				if err != nil {
					return Outcome{}, err
				}
				for _, m := range maps {
					if m.Metadata.Name != "instellingen" {
						continue
					}
					if len(m.Data) == 0 {
						return Outcome{Detail: "De ConfigMap instellingen is leeg; geef hem minstens één sleutel."}, nil
					}
					keys := make([]string, 0, len(m.Data))
					for k := range m.Data {
						keys = append(keys, k)
					}
					slices.Sort(keys)
					return Outcome{Done: true, Detail: fmt.Sprintf("instellingen heeft %s.", and(keys))}, nil
				}
				return Outcome{Detail: "Er is nog geen ConfigMap instellingen in leren."}, nil
			},
		},
		{
			ID: "secret", Title: "Maak het Secret geheim", Kind: KindDo,
			Text: []string{
				"Een Secret lijkt op een ConfigMap, maar alleen wie Secrets mag lezen, ziet de waarden. ClusterForge krijgt ze niet eens: op het tabblad Configuratie staat alleen hoe lang elke waarde is.",
				"Onder `stringData` schrijf je een waarde gewoon uit; Kubernetes bewaart haar in base64 onder `data`.",
			},
			Web: "Klik hieronder op Controleren en dan op Toepassen.",
			YAML: `apiVersion: v1
kind: Secret
metadata:
  name: geheim
  namespace: leren
type: Opaque
stringData:
  WACHTWOORD: verander-mij
`,
			Kubectl: []string{
				"sudo kubectl create secret generic geheim --from-literal=WACHTWOORD=verander-mij -n leren",
				"sudo kubectl describe secret geheim -n leren",
			},
			Link: &Link{Label: "Configuratie in leren", Tab: "configuratie", Namespace: Namespace},
			check: func(x *run, _ Data) (Outcome, error) {
				_, secrets, err := x.kube.Config(x.ctx, x.env.ClusterID, Namespace)
				if err != nil {
					return Outcome{}, err
				}
				for _, sec := range secrets {
					if sec.Metadata.Name == "geheim" {
						keys := sec.Keys()
						list := make([]string, 0, len(keys))
						for _, k := range keys {
							list = append(list, k.Name)
						}
						if len(list) == 0 {
							return Outcome{Detail: "Het Secret geheim is leeg; geef het minstens één sleutel."}, nil
						}
						return Outcome{Done: true, Detail: fmt.Sprintf("geheim heeft %s.", and(list))}, nil
					}
				}
				return Outcome{Detail: "Er is nog geen Secret geheim in leren."}, nil
			},
		},
		{
			ID: "gebruiken", Title: "Geef ze aan web", Kind: KindDo,
			Text: []string{
				"Met `envFrom` wordt elke sleutel een omgevingsvariabele in de container. Een container leest zijn omgeving maar één keer, bij het starten. Daarom rolt Kubernetes nieuwe pods uit als je dit verandert.",
			},
			Web: "Klik hieronder op Controleren: het verschil toont de nieuwe regels. Dan Toepassen.",
			YAML: `apiVersion: apps/v1
kind: Deployment
metadata:
  name: web
  namespace: leren
  labels:
    app: web
spec:
  replicas: 3
  selector:
    matchLabels:
      app: web
  template:
    metadata:
      labels:
        app: web
    spec:
      containers:
        - name: nginx
          image: nginx:1.28
          ports:
            - name: http
              containerPort: 80
          envFrom:
            - configMapRef:
                name: instellingen
            - secretRef:
                name: geheim
`,
			Kubectl: []string{
				"sudo kubectl set env deployment/web --from=configmap/instellingen -n leren",
				"sudo kubectl set env deployment/web --from=secret/geheim -n leren",
				"sudo kubectl exec deploy/web -n leren -- env",
			},
			Link: appsLink,
			check: func(x *run, _ Data) (Outcome, error) {
				w, body, err := x.deployment("web")
				if err != nil || w == nil {
					return Outcome{Detail: noWeb}, err
				}
				maps, secrets := configRefs(body)
				var missing []string
				if !slices.Contains(maps, "instellingen") {
					missing = append(missing, "de ConfigMap instellingen")
				}
				if !slices.Contains(secrets, "geheim") {
					missing = append(missing, "het Secret geheim")
				}
				if len(missing) > 0 {
					return Outcome{Detail: fmt.Sprintf("web leest %s nog niet.", and(missing))}, nil
				}
				if ok, why := rolledOut(w); !ok {
					return Outcome{Detail: fmt.Sprintf("web leest ze, en de nieuwe pods komen: %s.", why)}, nil
				}
				return Outcome{Done: true, Detail: "web leest instellingen en geheim, en alle pods zijn vervangen."}, nil
			},
		},
	},
}

var opslag = &Lesson{
	ID: "opslag", Number: 7, Title: "Opslag die blijft",
	Summary: "Een volume met local-path, de pod herstarten en zien dat de data er nog is.",
	Intro: []string{
		"Een container begint altijd met een schone schijf: wat hij schrijft, is weg als de pod vervangen wordt. Voor gegevens die moeten blijven, vraag je een volume met een PersistentVolumeClaim. In k3s maakt local-path dat volume als map op de schijf van een node.",
	},
	Outro: []string{
		"Gegevens horen op een volume, niet in de container. Een volume van local-path is snel en eenvoudig, maar zit aan één node vast. Dat merk je in les 8.",
	},
	Steps: []*Step{
		{
			ID: "claim", Title: "Vraag een volume", Kind: KindDo,
			Text: []string{
				"De claim `data` vraagt 1 GiB. Zonder `storageClassName` krijg je de standaardklasse, bij k3s local-path.",
				"De claim blijft eerst Pending. Dat hoort zo: local-path maakt het volume pas als een pod het gebruikt, op de node van die pod.",
			},
			Web: "Klik hieronder op Controleren en dan op Toepassen. Daarna staat de claim op het tabblad Opslag.",
			YAML: `apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: data
  namespace: leren
spec:
  accessModes:
    - ReadWriteOnce
  resources:
    requests:
      storage: 1Gi
`,
			Kubectl: []string{
				"sudo kubectl apply -f data.yaml",
				"sudo kubectl get pvc -n leren",
			},
			Link: &Link{Label: "Opslag in leren", Tab: "opslag", Namespace: Namespace},
			check: func(x *run, _ Data) (Outcome, error) {
				st, err := x.kube.Storage(x.ctx, x.env.ClusterID, Namespace)
				if err != nil {
					return Outcome{}, err
				}
				for _, c := range st.Claims {
					if c.Metadata.Name == "data" {
						return Outcome{Done: true, Detail: fmt.Sprintf("De claim data bestaat en is %s.", c.Status.Phase)}, nil
					}
				}
				return Outcome{Detail: "Er is nog geen claim data in leren."}, nil
			},
		},
		{
			ID: "notities", Title: "Start een app die schrijft", Kind: KindDo,
			Text: []string{
				"Deze app schrijft bij elke start de tijd in `/data/starts.txt` op het volume, en zet het hele bestand in zijn log.",
				"`Recreate` stopt de oude pod voordat de nieuwe start, zodat ze nooit samen in hetzelfde bestand schrijven.",
			},
			Web: "Klik hieronder op Controleren en dan op Toepassen.",
			YAML: `apiVersion: apps/v1
kind: Deployment
metadata:
  name: notities
  namespace: leren
  labels:
    app: notities
spec:
  replicas: 1
  strategy:
    type: Recreate
  selector:
    matchLabels:
      app: notities
  template:
    metadata:
      labels:
        app: notities
    spec:
      containers:
        - name: notities
          image: busybox:1.37
          command: ["sh", "-c", "date >> /data/starts.txt && cat /data/starts.txt && sleep 86400"]
          volumeMounts:
            - name: data
              mountPath: /data
      volumes:
        - name: data
          persistentVolumeClaim:
            claimName: data
`,
			Kubectl: []string{
				"sudo kubectl apply -f notities.yaml",
				"sudo kubectl get pvc,pods -n leren",
				"sudo kubectl logs deploy/notities -n leren",
			},
			Link: appsLink,
			check: func(x *run, _ Data) (Outcome, error) {
				w, _, err := x.deployment("notities")
				if err != nil {
					return Outcome{}, err
				}
				if w == nil {
					return Outcome{Detail: "Er is nog geen Deployment notities in leren."}, nil
				}
				pods, err := x.pods("notities")
				if err != nil {
					return Outcome{}, err
				}
				var p *kube.Pod
				for i := range pods {
					if ready(pods[i]) {
						p = &pods[i]
					}
				}
				if p == nil {
					return Outcome{Detail: "notities draait nog niet: " + cmp.Or(podStates(pods), "nog geen pod") + "." + waitHint(pods)}, nil
				}
				if !usesClaim(*p, "data") {
					return Outcome{Detail: "notities draait, maar zonder de claim data als volume."}, nil
				}
				st, err := x.kube.Storage(x.ctx, x.env.ClusterID, Namespace)
				if err != nil {
					return Outcome{}, err
				}
				for _, c := range st.Claims {
					if c.Metadata.Name == "data" && c.Status.Phase != "Bound" {
						return Outcome{Detail: fmt.Sprintf("De claim data is %s.", c.Status.Phase)}, nil
					}
				}
				return Outcome{Done: true, Detail: fmt.Sprintf("notities draait op %s, met het volume van data.", p.Spec.NodeName)}, nil
			},
		},
		{
			ID: "herstarten", Title: "Herstart en kijk of de data blijft", Kind: KindDo,
			Text: []string{
				"Herstart notities. De oude pod stopt en een nieuwe start, met een nieuwe container maar hetzelfde volume. In zijn log staan dan twee tijden: die van de eerste start en die van nu.",
			},
			Web: "Open notities op het tabblad Apps en kies Herstarten. De log staat daarna op de pagina van de nieuwe pod.",
			Kubectl: []string{
				"sudo kubectl rollout restart deployment/notities -n leren",
				"sudo kubectl logs deploy/notities -n leren",
			},
			Link: &Link{Label: "Deployment notities", Ref: notitiesRef},
			start: func(x *run) (Data, error) {
				pods, err := x.pods("notities")
				if err != nil {
					return nil, err
				}
				return Data{"voor": names(pods)}, nil
			},
			check: func(x *run, d Data) (Outcome, error) {
				pods, err := x.pods("notities")
				if err != nil {
					return Outcome{}, err
				}
				var p *kube.Pod
				for i := range pods {
					if !slices.Contains(d.Strs("voor"), pods[i].Metadata.Name) {
						p = &pods[i]
					}
				}
				switch {
				case p == nil:
					return Outcome{Detail: "notities draait nog met dezelfde pod."}, nil
				case !ready(*p):
					return Outcome{Detail: fmt.Sprintf("De nieuwe pod %s start: %s.%s", p.Metadata.Name, p.StatusText(), waitHint([]kube.Pod{*p}))}, nil
				}
				text, _, err := x.kube.Logs(x.ctx, x.env.ClusterID, Namespace, p.Metadata.Name, kube.LogOptions{Lines: 20})
				if err != nil {
					return Outcome{}, err
				}
				lines := logLines(text)
				if len(lines) < 2 {
					return Outcome{Detail: fmt.Sprintf("De nieuwe pod %s ziet maar één start. Staat /data op het volume?", p.Metadata.Name)}, nil
				}
				// Alleen de laatste regels, kort: de uitleg wordt bewaard.
				if len(lines) > 5 {
					lines = lines[len(lines)-5:]
				}
				for i, line := range lines {
					lines[i] = clip(line, 80)
				}
				return Outcome{Done: true, Detail: fmt.Sprintf("De nieuwe pod %s vond het bestand terug: %s.", p.Metadata.Name, strings.Join(lines, "; "))}, nil
			},
		},
		{
			ID: "node", Title: "Zoek waar het volume staat", Kind: KindQuiz,
			Text: []string{
				"local-path maakt een map op de node waar de pod het eerst draaide, onder `/var/lib/rancher/k3s/storage`. Het volume zit daardoor aan die node vast: het PersistentVolume heeft een `nodeAffinity`, en de planner zet een pod met dit volume altijd op die node. Gaat de node uit, dan wacht de pod tot hij terug is.",
				"Wil je een volume dat met de pod meeverhuist, dan heb je gedeelde opslag nodig, zoals Longhorn of NFS.",
			},
			Web:      "Open het tabblad Opslag met namespace leren: de node staat bij het volume.",
			Kubectl:  []string{"sudo kubectl get pv", "sudo kubectl describe pv"},
			Question: "Op welke node staat het volume van data?",
			Hint:     "Kijk op het tabblad Opslag bij het volume, of bij Node Affinity in kubectl describe pv.",
			Link:     &Link{Label: "Opslag in leren", Tab: "opslag", Namespace: Namespace},
			answer: func(x *run) ([]string, string, string, error) {
				st, err := x.kube.Storage(x.ctx, x.env.ClusterID, Namespace)
				if err != nil {
					return nil, "", "", err
				}
				for _, v := range st.Volumes {
					if v.Spec.ClaimRef != nil && v.Spec.ClaimRef.Name == "data" {
						if n := v.Node(); n != "" {
							return []string{n}, fmt.Sprintf("Juist: %s, in %s.", n, cmp.Or(v.Path(), "een map op die node")), "", nil
						}
					}
				}
				return nil, "", "Het volume van data zit niet aan één node vast; deze vraag slaan we over.", nil
			},
		},
	},
}

var onderhoud = &Lesson{
	ID: "onderhoud", Number: 8, Title: "Een node in onderhoud",
	Summary:  "Cordon en leegmaken, zien waar de pods heen gaan, en terugzetten.",
	MinNodes: 2,
	Intro: []string{
		"Moet een node herstarten, bijvoorbeeld voor updates, dan haal je eerst de pods eraf. Cordon zegt: hier geen nieuwe pods meer. Leegmaken zet de pods die er draaien ergens anders neer. Na het onderhoud zet je de node terug.",
	},
	Outro: []string{
		"Zo doe je onderhoud zonder dat je apps stilvallen: cordon, leegmaken, onderhoud, uncordon. ClusterForge doet hetzelfde als je een node van een Kubernetes-cluster in onderhoud zet.",
	},
	Steps: []*Step{
		{
			ID: "cordon", Title: "Zet een node op cordon", Kind: KindDo,
			Text: []string{
				"Kies een node waarop een pod van web draait, bijvoorbeeld `{voorbeeld}`, en zet hem op cordon. De pods die er draaien, blijven, maar er komen er geen nieuwe bij. kubectl toont dan `SchedulingDisabled`.",
			},
			Web:     "Open het tabblad Nodes, kies de node en dan Cordon.",
			Kubectl: []string{"sudo kubectl cordon {voorbeeld}", "sudo kubectl get nodes"},
			Link:    nodesLink,
			start: func(x *run) (Data, error) {
				pods, err := x.pods("web")
				if err != nil {
					return nil, err
				}
				d := Data{}
				if nodes := nodesOf(pods); len(nodes) > 0 {
					d["voorbeeld"] = nodes[0]
				}
				return d, nil
			},
			check: func(x *run, _ Data) (Outcome, error) {
				nodes, err := x.kube.Nodes(x.ctx, x.env.ClusterID)
				if err != nil {
					return Outcome{}, err
				}
				pods, err := x.pods("web")
				if err != nil {
					return Outcome{}, err
				}
				if len(pods) == 0 {
					return Outcome{Detail: "web heeft geen pods. Zet hem eerst op 3 pods, zoals in les 3."}, nil
				}
				var cordoned []string
				for _, n := range nodes {
					if n.Spec.Unschedulable {
						cordoned = append(cordoned, n.Metadata.Name)
					}
				}
				if len(nodes) > 0 && len(cordoned) == len(nodes) {
					return Outcome{Detail: "Alle nodes staan op cordon: dan kunnen de pods nergens heen. Zet er een terug met uncordon."}, nil
				}
				webNodes := nodesOf(pods)
				for _, n := range cordoned {
					if slices.Contains(webNodes, n) {
						count := 0
						for _, p := range pods {
							if p.Spec.NodeName == n {
								count++
							}
						}
						return Outcome{Done: true, Detail: fmt.Sprintf("%s staat op cordon, met %s van web erop.", n, plural(count, "pod", "pods")),
							Data: Data{"node": n}}, nil
					}
				}
				if len(cordoned) > 0 {
					return Outcome{Detail: fmt.Sprintf("%s staat op cordon, maar daar draait geen pod van web. Kies %s.", and(cordoned), and(webNodes))}, nil
				}
				return Outcome{Detail: "Er staat nog geen node op cordon."}, nil
			},
		},
		{
			ID: "leegmaken", Title: "Maak {node} leeg", Kind: KindDo,
			Text: []string{
				"Leegmaken haalt de pods van de node weg met de eviction-API. Die houdt rekening met een PodDisruptionBudget, zodat er nooit te veel pods van een app tegelijk weg zijn. De ReplicaSet van web maakt ze meteen opnieuw, op een andere node.",
				"Pods van een DaemonSet, zoals svclb-traefik, blijven staan: die horen op elke node. Draait notities uit les 7 op deze node, dan blijft zijn nieuwe pod Pending tot de node terug is, want zijn volume zit hier vast.",
			},
			Web: "Open {node} op het tabblad Nodes en kies Leegmaken.",
			Kubectl: []string{
				"sudo kubectl drain {node} --ignore-daemonsets --delete-emptydir-data",
				"sudo kubectl get pods -n leren -o wide",
			},
			Link: &Link{Label: "Node {node}", Ref: &kube.Ref{Version: "v1", Resource: "nodes", Name: "{node}"}},
			check: func(x *run, _ Data) (Outcome, error) {
				node := x.data["cordon"].Str("node")
				w, _, err := x.deployment("web")
				if err != nil || w == nil {
					return Outcome{Detail: noWeb}, err
				}
				pods, err := x.pods("web")
				if err != nil {
					return Outcome{}, err
				}
				left := 0
				for _, p := range pods {
					if p.Spec.NodeName == node {
						left++
					}
				}
				switch {
				case left > 0:
					return Outcome{Detail: fmt.Sprintf("Er draait nog %s van web op %s.", plural(left, "pod", "pods"), node)}, nil
				case w.ReadyPods() < w.Desired():
					return Outcome{Detail: fmt.Sprintf("De pods van web zijn van %s af; wachten tot ze elders klaar zijn: %d van %d.", node, w.ReadyPods(), w.Desired())}, nil
				}
				return Outcome{Done: true, Detail: fmt.Sprintf("Geen pod van web meer op %s; ze draaien op %s.", node, and(nodesOf(pods)))}, nil
			},
		},
		{
			ID: "terugzetten", Title: "Zet {node} terug", Kind: KindDo,
			Text: []string{
				"Na het onderhoud krijgt de node weer pods. De pods van web gaan niet vanzelf terug: Kubernetes verhuist een draaiende pod nooit uit zichzelf. Pas bij een herstart of een nieuwe versie verdeelt de planner ze opnieuw.",
			},
			Web:     "Open {node} op het tabblad Nodes en kies Uncordon.",
			Kubectl: []string{"sudo kubectl uncordon {node}", "sudo kubectl get nodes"},
			Link:    &Link{Label: "Node {node}", Ref: &kube.Ref{Version: "v1", Resource: "nodes", Name: "{node}"}},
			check: func(x *run, _ Data) (Outcome, error) {
				node := x.data["cordon"].Str("node")
				nodes, err := x.kube.Nodes(x.ctx, x.env.ClusterID)
				if err != nil {
					return Outcome{}, err
				}
				for _, n := range nodes {
					if n.Metadata.Name != node {
						continue
					}
					if n.Spec.Unschedulable {
						return Outcome{Detail: fmt.Sprintf("%s staat nog op cordon.", node)}, nil
					}
					return Outcome{Done: true, Detail: fmt.Sprintf("%s krijgt weer pods.", node)}, nil
				}
				return Outcome{Done: true, Detail: fmt.Sprintf("%s is geen node meer van dit cluster.", node)}, nil
			},
		},
	},
}

var nodeErbij = &Lesson{
	ID: "node-erbij", Number: 9, Title: "Een node erbij",
	Summary: "Een worker toevoegen en zien dat er pods naartoe gaan.",
	Intro: []string{
		"Heeft je cluster te weinig ruimte, dan zet je er een node bij. Een worker draait alleen pods: hij heeft geen etcd en geen API, dus er kunnen er zoveel bij als je wilt.",
		"Een node is een machine, dus met kubectl alleen lukt dit niet. ClusterForge maakt een VM uit het golden image en installeert k3s als agent. Die meldt zich met het token van het cluster aan bij de API op het VIP.",
	},
	Outro: []string{
		"Een node erbij is een VM erbij. Kubernetes zet er zelf pods op, maar alleen nieuwe. In les 10 zie je wat er gebeurt als een server uitvalt.",
	},
	Steps: []*Step{
		{
			ID: "toevoegen", Title: "Voeg een worker toe", Kind: KindDo,
			Text: []string{
				"Kies op het tabblad Nodes Worker toevoegen. ClusterForge toont eerst het plan: welke VM erbij komt, met welke naam en welk adres. Na Toepassen volg je de taak: VM maken, k3s installeren en wachten tot de node Ready is.",
				"Met kubectl zie je de node verschijnen: eerst NotReady, en Ready zodra k3s en het podnetwerk draaien.",
				"Volgt het cluster Git, dan zet je in `cluster.yaml` het aantal workers een hoger; ClusterForge doet dan hetzelfde na je commit. Nodes toevoegen kan alleen een beheerder.",
			},
			Web:     "Open het tabblad Nodes en kies Worker toevoegen.",
			Kubectl: []string{"sudo kubectl get nodes -o wide"},
			Link:    nodesLink,
			start: func(x *run) (Data, error) {
				nodes, err := x.kube.Nodes(x.ctx, x.env.ClusterID)
				if err != nil {
					return nil, err
				}
				before := make([]string, 0, len(nodes))
				for _, n := range nodes {
					before = append(before, n.Metadata.Name)
				}
				return Data{"voor": before}, nil
			},
			check: func(x *run, d Data) (Outcome, error) {
				nodes, err := x.kube.Nodes(x.ctx, x.env.ClusterID)
				if err != nil {
					return Outcome{}, err
				}
				before := d.Strs("voor")
				var fresh []kube.Node
				for _, n := range nodes {
					if !slices.Contains(before, n.Metadata.Name) {
						fresh = append(fresh, n)
					}
				}
				slices.SortFunc(fresh, func(a, b kube.Node) int { return cmp.Compare(a.Metadata.Name, b.Metadata.Name) })
				for _, n := range fresh {
					if n.Ready() {
						return Outcome{Done: true, Detail: fmt.Sprintf("%s is erbij en Ready. Je cluster heeft nu %s.", n.Metadata.Name, plural(len(nodes), "node", "nodes")),
							Data: Data{"node": n.Metadata.Name}}, nil
					}
				}
				if len(fresh) > 0 {
					return Outcome{Detail: fmt.Sprintf("%s is erbij, maar nog niet Ready: k3s en het podnetwerk starten nog.", fresh[0].Metadata.Name)}, nil
				}
				return Outcome{Detail: fmt.Sprintf("Er is nog geen node bij gekomen; je cluster heeft er %d.", len(nodes))}, nil
			},
		},
		{
			ID: "pods", Title: "Zet pods op {node}", Kind: KindDo,
			Text: []string{
				"De nieuwe node is leeg. Kubernetes verhuist geen draaiende pods, dat zag je in les 8; alleen nieuwe pods kunnen erop komen. De planner kiest de node met de meeste ruimte, en dat is nu {node}.",
				"Zet web op 6 pods en kijk in de kolom Node waar de nieuwe heen gaan. Heeft web er al 6 of meer, herstart hem dan: Kubernetes maakt alle pods nieuw en verdeelt ze opnieuw.",
			},
			Web: "Open web op het tabblad Apps en zet hem op 6 pods, of kies Herstarten.",
			Kubectl: []string{
				"sudo kubectl scale deployment web -n leren --replicas=6",
				"sudo kubectl rollout restart deployment web -n leren",
				"sudo kubectl get pods -n leren -o wide",
			},
			Link: &Link{Label: "Deployment web", Ref: webRef},
			check: func(x *run, _ Data) (Outcome, error) {
				node := x.data["toevoegen"].Str("node")
				w, _, err := x.deployment("web")
				if err != nil || w == nil {
					return Outcome{Detail: noWeb}, err
				}
				pods, err := x.pods("web")
				if err != nil {
					return Outcome{}, err
				}
				on, starting := 0, 0
				for _, p := range pods {
					switch {
					case p.Spec.NodeName != node:
					case ready(p):
						on++
					default:
						starting++
					}
				}
				switch {
				case on > 0:
					verb := "draaien"
					if on == 1 {
						verb = "draait"
					}
					detail := fmt.Sprintf("%s van web %s op %s.", plural(on, "pod", "pods"), verb, node)
					if others := slices.DeleteFunc(nodesOf(pods), func(n string) bool { return n == node }); len(others) > 0 {
						detail += fmt.Sprintf(" De andere staan op %s.", and(others))
					}
					return Outcome{Done: true, Detail: detail}, nil
				case starting > 0:
					return Outcome{Detail: fmt.Sprintf("Er start een pod van web op %s; die haalt eerst het image binnen.%s", node, waitHint(pods))}, nil
				}
				nodes, err := x.kube.Nodes(x.ctx, x.env.ClusterID)
				if err != nil {
					return Outcome{}, err
				}
				if !slices.ContainsFunc(nodes, func(n kube.Node) bool { return n.Metadata.Name == node }) {
					return Outcome{Done: true, Detail: fmt.Sprintf("%s is geen node meer van dit cluster; deze opdracht slaan we over.", node)}, nil
				}
				return Outcome{Detail: fmt.Sprintf("Nog geen pod van web op %s; de %s draaien op %s.", node, plural(len(pods), "pod", "pods"), and(nodesOf(pods)))}, nil
			},
		},
		{
			ID: "weghalen", Title: "Lees hoe weghalen gaat", Kind: KindRead,
			Text: []string{
				"Weghalen gaat andersom. ClusterForge maakt de node leeg zoals in les 8, zet de VM uit, haalt de node uit Kubernetes en verwijdert dan de VM. Zo verdwijnt er geen pod voor hij ergens anders draait.",
				"Met kubectl haal je de node alleen uit Kubernetes. De VM draait dan nog, en k3s meldt hem bij de volgende herstart weer aan. Daarom doet ClusterForge het hele rijtje.",
				"Een server haalt ClusterForge zo niet weg: dan verandert de meerderheid van etcd. Servers gaan van 1 naar 3 of 5, niet terug.",
				"Wil je {node} houden, laat hem dan staan. Weghalen kan altijd later.",
			},
			Web: "Open het tabblad Nodes en kies bij {node} Weghalen.",
			Kubectl: []string{
				"sudo kubectl drain {node} --ignore-daemonsets --delete-emptydir-data",
				"sudo kubectl delete node {node}",
			},
			Done: "Gelezen",
			Link: nodesLink,
		},
	},
}

var serverUitval = &Lesson{
	ID: "server-valt-uit", Number: 10, Title: "Een server valt uit",
	Summary:  "Een server hard stoppen en zien dat de API via het VIP blijft antwoorden.",
	MinNodes: 3, MinServers: 3, Lab: true,
	Intro: []string{
		"Met drie servers heeft je cluster drie keer etcd en drie keer de API. etcd werkt zolang de meerderheid van de servers draait, en het VIP staat altijd op een server die werkt: keepalived verhuist het binnen een paar seconden.",
		"In deze les zet je een server hard uit, alsof de stroom uitvalt, en kijk je wat er blijft werken. Daarom kan deze les alleen op een cluster met omgeving lab.",
	},
	Outro: []string{
		"Een cluster met drie servers overleeft het uitvallen van één server: de API blijft via het VIP bereikbaar, en pods van de uitgevallen node komen na vijf minuten op een andere. Vallen er twee uit, dan stopt etcd en kan niemand nog iets veranderen; de pods die draaien, draaien wel door.",
	},
	Steps: []*Step{
		{
			ID: "meerderheid", Title: "Reken de meerderheid uit", Kind: KindQuiz,
			Text: []string{
				"etcd legt iets pas vast als de meerderheid van de servers het heeft. Zo kunnen de twee helften van een gesplitst netwerk nooit allebei verder: maar één helft heeft de meerderheid.",
				"Daarom kiest ClusterForge 1, 3 of 5 servers. Met 2 servers is de meerderheid ook 2, en mag er dus geen uitvallen.",
			},
			Web:      "Tel de servers op het tabblad Nodes.",
			Kubectl:  []string{"sudo kubectl get nodes"},
			Question: "Hoeveel servers mogen er bij jouw cluster tegelijk uitvallen?",
			Hint:     "De meerderheid van 3 is 2, van 5 is 3. Wat erboven zit, mag uitvallen.",
			Link:     nodesLink,
			answer: func(x *run) ([]string, string, string, error) {
				servers, _, err := x.servers()
				if err != nil {
					return nil, "", "", err
				}
				n := len(servers)
				if n == 0 {
					return nil, "", "Dit cluster meldt geen node met de rol control-plane; deze vraag slaan we over.", nil
				}
				k := (n - 1) / 2
				verb := "mogen"
				if k == 1 {
					verb = "mag"
				}
				return []string{strconv.Itoa(k)}, fmt.Sprintf("Juist: met %d servers is de meerderheid %d, dus er %s er %d uitvallen.", n, n/2+1, verb, k), "", nil
			},
		},
		{
			ID: "uitzetten", Title: "Zet een server hard uit", Kind: KindDo,
			Text: []string{
				"Kies een server, bijvoorbeeld `{voorbeeld}`, en zet zijn VM hard uit: niet afsluiten, maar de stekker eruit. Kubernetes noemt hem na ongeveer 40 seconden NotReady.",
				"Stond het VIP {vip} op die server, dan neemt een andere server het binnen een paar seconden over. ClusterForge en kubectl op je pc praten met het VIP en merken daar niets van.",
			},
			Web:     "Kies in het menu Nodes de node {voorbeeld}, en bij Proxmox Hard uitzetten. In Proxmox zelf heet dat Stop.",
			Kubectl: []string{"sudo kubectl get nodes"},
			Link:    nodesLink,
			start: func(x *run) (Data, error) {
				servers, _, err := x.servers()
				if err != nil || len(servers) == 0 {
					return Data{}, err
				}
				return Data{"voorbeeld": servers[0].Metadata.Name}, nil
			},
			check: func(x *run, _ Data) (Outcome, error) {
				servers, down, err := x.servers()
				if err != nil {
					return Outcome{}, err
				}
				if len(down) == 0 {
					return Outcome{Detail: "Alle servers zijn nog Ready. Na het uitzetten duurt het ongeveer 40 seconden voor Kubernetes het merkt."}, nil
				}
				return Outcome{Done: true, Detail: fmt.Sprintf("%s is NotReady, en de API antwoordt nog: %d van de %d servers draaien.", down[0], len(servers)-len(down), len(servers)),
					Data: Data{"node": down[0]}}, nil
			},
		},
		{
			ID: "api", Title: "Kijk wat er nog werkt", Kind: KindQuiz,
			Text: []string{
				"Open het tabblad Nodes, of draai kubectl op een server die nog aan staat. Dat werkt: etcd heeft zijn meerderheid, en de API draait op elke server.",
				"De pods op {node} blijven nog even in de lijst staan. Kubernetes wacht vijf minuten voor het ze op een andere node opnieuw maakt: een node die even weg is, komt vaak terug.",
			},
			Web:      "Open het tabblad Nodes.",
			Kubectl:  []string{"sudo kubectl get nodes", "sudo kubectl get pods -A -o wide"},
			Question: "Hoeveel servers zijn er nu Ready?",
			Hint:     "Tel de servers met de status Ready op het tabblad Nodes.",
			Link:     nodesLink,
			answer: func(x *run) ([]string, string, string, error) {
				servers, down, err := x.servers()
				if err != nil {
					return nil, "", "", err
				}
				up := len(servers) - len(down)
				return []string{strconv.Itoa(up)}, fmt.Sprintf("Juist: %d van de %d. Dat ClusterForge dit kon nakijken, bewijst het al: de API antwoordt.", up, len(servers)), "", nil
			},
		},
		{
			ID: "aanzetten", Title: "Zet {node} weer aan", Kind: KindDo,
			Text: []string{
				"Start de VM weer. k3s start vanzelf, de server meldt zich aan en etcd haalt bij wat hij miste. Na ongeveer een minuut is de node weer Ready.",
			},
			Web:     "Kies in het menu Nodes de node {node}, en bij Proxmox Starten.",
			Kubectl: []string{"sudo kubectl get nodes"},
			Link:    &Link{Label: "Node {node}", Ref: &kube.Ref{Version: "v1", Resource: "nodes", Name: "{node}"}},
			check: func(x *run, _ Data) (Outcome, error) {
				node := x.data["uitzetten"].Str("node")
				nodes, err := x.kube.Nodes(x.ctx, x.env.ClusterID)
				if err != nil {
					return Outcome{}, err
				}
				for _, n := range nodes {
					if n.Metadata.Name != node {
						continue
					}
					if !n.Ready() {
						return Outcome{Detail: fmt.Sprintf("%s is nog NotReady.", node)}, nil
					}
					return Outcome{Done: true, Detail: fmt.Sprintf("%s is weer Ready.", node)}, nil
				}
				return Outcome{Done: true, Detail: fmt.Sprintf("%s is geen node meer van dit cluster.", node)}, nil
			},
		},
	},
}

// servers zijn de servers op naam, en de namen van die niet Ready zijn.
func (x *run) servers() ([]kube.Node, []string, error) {
	nodes, err := x.kube.Nodes(x.ctx, x.env.ClusterID)
	if err != nil {
		return nil, nil, err
	}
	var servers []kube.Node
	var down []string
	for _, n := range nodes {
		if !isServer(n) {
			continue
		}
		servers = append(servers, n)
		if !n.Ready() {
			down = append(down, n.Metadata.Name)
		}
	}
	slices.SortFunc(servers, func(a, b kube.Node) int { return cmp.Compare(a.Metadata.Name, b.Metadata.Name) })
	slices.Sort(down)
	return servers, down, nil
}
