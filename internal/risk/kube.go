package risk

import (
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Hoe vaak de pods van een app in het afgelopen etmaal herstart moeten zijn
// voor ze 'blijven herstarten'.
const kubeRestarts = 3

func kubeLink(id uuid.UUID, tab string) string {
	l := "/kubernetes/cluster?id=" + id.String()
	if tab != "" {
		l += "&tab=" + tab
	}
	return l
}

// kubeAppLink is de pagina van een app in het dashboard.
func kubeAppLink(clusterID uuid.UUID, a KubeAppInput) string {
	q := url.Values{"id": {clusterID.String()}, "g": {"apps"}, "v": {"v1"}, "r": {strings.ToLower(a.Kind) + "s"}, "ns": {a.Namespace}, "name": {a.Name}}
	return "/kubernetes/object?" + q.Encode()
}

func kubeAppName(a KubeAppInput) string { return a.Namespace + "/" + a.Name }

func kubeAppSubject(a KubeAppInput) Subject { return Subject{"kube_app", a.ID, kubeAppName(a)} }

// kubeNodeSubject is de ClusterForge-node als die gekoppeld is, anders een
// vast id uit het cluster en de naam in Kubernetes.
func kubeNodeSubject(in ClusterInput, n KubeNodeInput) Subject {
	if n.NodeID != nil {
		return Subject{"node", *n.NodeID, n.Name}
	}
	return Subject{"kube_node", uuid.NewSHA1(spofNamespace, []byte("kube-node:"+in.ID.String()+":"+n.Name)), n.Name}
}

// names zet een paar namen in een zin: a, b en 3 meer.
func names(list []string, max int) string {
	if len(list) > max {
		return strings.Join(list[:max], ", ") + " en " + strconv.Itoa(len(list)-max) + " meer"
	}
	if len(list) <= 1 {
		return strings.Join(list, "")
	}
	return strings.Join(list[:len(list)-1], ", ") + " en " + list[len(list)-1]
}

// unpinned zegt of een image geen vaste versie heeft: zonder tag, of met
// latest. Een digest (@sha256:...) is vast.
func unpinned(image string) bool {
	if strings.Contains(image, "@") {
		return false
	}
	ref := image
	if i := strings.LastIndexByte(ref, '/'); i >= 0 {
		ref = ref[i+1:]
	}
	_, tag, ok := strings.Cut(ref, ":")
	return !ok || tag == "latest"
}

// --- Kubernetes ---

func kubernetesCard(in ClusterInput, now time.Time) part {
	var p part
	k := in.Kube
	if !k.Synced {
		p.unknown = true
		p.note = "ClusterForge kon dit cluster nog niet lezen in Kubernetes."
		return p
	}
	eb := envBump(in.Env)
	cs := clusterSubject(in)

	var servers []string
	for _, n := range k.Nodes {
		if n.Server {
			servers = append(servers, n.Name)
		}
		if !n.Ready {
			p.add("kubernetes.node_not_ready", bump(Medium, eb, High), kubeNodeSubject(in, n),
				n.Name+" is NotReady in Kubernetes: er komen geen nieuwe pods op, en na vijf minuten zet Kubernetes zijn pods op een andere node",
				kubeLink(in.ID, "nodes"))
		}
	}
	if in.Env == "prod" && len(servers) == 1 {
		p.add("kubernetes.single_server", Medium, cs,
			in.Name+" heeft maar één Kubernetes-server: valt "+servers[0]+" uit, dan antwoordt de API niet meer en start niets "+
				"opnieuw op een andere node. Met drie servers mag er één uitvallen", kubeLink(in.ID, "nodes"))
	}

	switch {
	case !k.SnapshotsRead:
	case k.NewestSnapshot == nil:
		p.add("kubernetes.snapshot_missing", bump(High, eb, Critical), cs,
			in.Name+" heeft geen etcd-snapshot: gaat de database van Kubernetes kapot, dan is er niets om terug te zetten", kubeLink(in.ID, "snapshots"))
	case now.Sub(*k.NewestSnapshot) > 24*time.Hour:
		p.add("kubernetes.snapshot_stale", bump(Medium, eb, High), cs,
			"De nieuwste etcd-snapshot van "+in.Name+" is "+days(now.Sub(*k.NewestSnapshot))+" oud; k3s maakt er normaal elke twaalf uur een",
			kubeLink(in.ID, "snapshots"))
	}

	if minor, end, ok := KubeEOL(k.Version); ok {
		switch {
		case now.After(end):
			p.add("kubernetes.version_eol", High, cs,
				in.Name+" draait Kubernetes "+minor+", sinds "+month(end)+" zonder updates; werk bij op het overzicht van het cluster",
				kubeLink(in.ID, ""))
		case end.Sub(now) < 90*day:
			p.add("kubernetes.version_eol", bump(Low, eb, Medium), cs,
				in.Name+" draait Kubernetes "+minor+", dat vanaf "+month(end)+" geen updates meer krijgt; werk bij op het overzicht van het cluster",
				kubeLink(in.ID, ""))
		}
	}

	var single, latest, unlimited []string
	for _, a := range k.Apps {
		recent := a.RestartedAt != nil && now.Sub(*a.RestartedAt) <= 24*time.Hour
		if crash := strings.Contains(a.Trouble, "CrashLoopBackOff"); crash || (a.Restarts >= kubeRestarts && recent) {
			text := kubeAppName(a) + " blijft herstarten: " + plural(a.Restarts, "herstart", "herstarts") + " van de pods die er nu zijn"
			if crash {
				text += ", nu in CrashLoopBackOff"
			}
			p.add("kubernetes.restarts", bump(Medium, eb, High), kubeAppSubject(a), text+"; de logs en events zeggen waarom", kubeAppLink(in.ID, a))
		}
		if a.System {
			continue
		}
		if in.Env == "prod" && a.Kind != "DaemonSet" && a.Desired == 1 {
			single = append(single, kubeAppName(a))
		}
		for _, img := range a.Images {
			if unpinned(img) {
				latest = append(latest, kubeAppName(a)+" ("+img+")")
				break
			}
		}
		if len(a.NoMemoryLimit) > 0 {
			unlimited = append(unlimited, kubeAppName(a))
		}
	}
	if len(single) > 0 {
		p.add("kubernetes.single_pod", Low, cs,
			plural(len(single), "app", "apps")+" op prod met één pod: "+names(single, 3)+
				". Valt die pod of zijn node weg, dan is de app even weg; zet replicas op 2 of meer", kubeLink(in.ID, "apps"))
	}
	if len(latest) > 0 {
		p.add("kubernetes.latest_image", Low, cs,
			plural(len(latest), "app gebruikt", "apps gebruiken")+" een image zonder vaste versie: "+names(latest, 3)+
				". Een herstart kan zo ongemerkt een andere versie ophalen; zet een versie in de tag", kubeLink(in.ID, "apps"))
	}
	if len(unlimited) > 0 {
		p.add("kubernetes.memory_limit", Low, cs,
			plural(len(unlimited), "app heeft", "apps hebben")+" pods zonder geheugenlimiet: "+names(unlimited, 3)+
				". Zo'n pod kan het geheugen van zijn node opmaken en andere pods laten stoppen; zet resources.limits.memory",
			kubeLink(in.ID, "apps"))
	}
	return p
}
