package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/agent"
	"github.com/Jonasz1996/clusterforge/internal/kube/kubefake"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

type kubeRef struct {
	Group     string `json:"group"`
	Version   string `json:"version"`
	Resource  string `json:"resource"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

type kubeEvent struct {
	Namespace string   `json:"namespace"`
	Kind      string   `json:"kind"`
	Name      string   `json:"name"`
	Type      string   `json:"type"`
	Reason    string   `json:"reason"`
	Count     int      `json:"count"`
	Source    string   `json:"source"`
	Object    *kubeRef `json:"object"`
}

type kubeChainLink struct {
	Role   string   `json:"role"`
	Kind   string   `json:"kind"`
	Name   string   `json:"name"`
	Ref    *kubeRef `json:"ref"`
	NodeID *string  `json:"node_id"`
	Text   string   `json:"text"`
	URL    string   `json:"url"`
}

type kubeContainer struct {
	Name         string `json:"name"`
	Init         bool   `json:"init"`
	Ready        bool   `json:"ready"`
	Restarts     int    `json:"restarts"`
	State        string `json:"state"`
	Reason       string `json:"reason"`
	LastReason   string `json:"last_reason"`
	LastExitCode *int   `json:"last_exit_code"`
}

type kubeObject struct {
	Ref        kubeRef           `json:"ref"`
	APIVersion string            `json:"api_version"`
	Kind       string            `json:"kind"`
	UID        string            `json:"uid"`
	Labels     map[string]string `json:"labels"`
	Summary    string            `json:"summary"`
	Problem    bool              `json:"problem"`
	Conditions []struct {
		Type, Status string
	} `json:"conditions"`
	Chain  []kubeChainLink `json:"chain"`
	Events []kubeEvent     `json:"events"`
	YAML   string          `json:"yaml"`
	Pod    *struct {
		Node       string          `json:"node"`
		Qos        string          `json:"qos"`
		Containers []kubeContainer `json:"containers"`
	} `json:"pod"`
}

func (o kubeObject) link(role string) []kubeChainLink {
	var out []kubeChainLink
	for _, l := range o.Chain {
		if l.Role == role {
			out = append(out, l)
		}
	}
	return out
}

// detailCluster is het cluster Leren met een app die je kunt volgen: de
// pods van web horen bij een ReplicaSet van de Deployment, een Service en
// een Ingress maken hem bereikbaar, en een pod heeft een volume.
func detailCluster(t *testing.T) *kubefake.Server {
	t.Helper()
	fake := kubeCluster(t)
	web := map[string]string{"app": "web"}
	fake.Add(
		kubefake.ReplicaSet("demo", "web-7c9", "web", 3),
		kubefake.Pod(kubefake.PodSpec{
			Namespace: "demo", Name: "web-7c9-a", Node: "leren-worker-01", OwnerKind: "ReplicaSet", OwnerName: "web-7c9", Labels: web, Claim: "data",
		}),
		kubefake.Pod(kubefake.PodSpec{Namespace: "demo", Name: "web-7c9-b", Node: "leren-server-01", OwnerKind: "ReplicaSet", OwnerName: "web-7c9", Labels: web}),
		kubefake.Pod(kubefake.PodSpec{
			Namespace: "demo", Name: "web-7c9-c", Node: "leren-worker-02", Waiting: "CrashLoopBackOff", Restarts: 7,
			OwnerKind: "ReplicaSet", OwnerName: "web-7c9", Labels: web, LastReason: "Error", Init: "wacht-op-db",
		}),
		kubefake.Service(kubefake.ServiceSpec{
			Namespace: "demo", Name: "web", ClusterIP: "10.43.20.5", Selector: web, Ports: []kubefake.ServicePort{{Name: "http", Port: 80, TargetPort: 8080}},
		}),
		kubefake.Ingress(kubefake.IngressSpec{Namespace: "demo", Name: "web", Host: "web.leren.lab", Service: "web", Port: 80, Address: []string{"10.0.40.100"}}),
		kubefake.ConfigMap("demo", "web-config", map[string]string{"nginx.conf": "server {}"}),
		kubefake.Secret("demo", "api", "", map[string]string{"token": "abc123"}),
		map[string]any{
			"apiVersion": "traefik.io/v1alpha1", "kind": "IngressRoute", "metadata": map[string]any{"name": "dashboard", "namespace": "demo"},
			"spec": map[string]any{"entryPoints": []any{"web"}},
		},
	)
	fake.Add(kubefake.Volume(kubefake.VolumeSpec{Namespace: "demo", Claim: "data", Volume: "pvc-1234", Size: "1Gi", Node: "leren-worker-01"})...)
	fake.Add(kubefake.Volume(kubefake.VolumeSpec{Namespace: "demo", Claim: "cache", Size: "512Mi", Pending: true})...)
	uid := fake.Get("v1", "Pod", "demo", "web-7c9-c")["metadata"].(map[string]any)["uid"].(string)
	fake.Add(
		kubefake.Event(kubefake.EventSpec{
			Namespace: "demo", Name: "web-7c9-c.1", Reason: "Pulled", Message: "Container image already present", Kind: "Pod", ObjectName: "web-7c9-c",
			UID: uid, Ago: 20 * time.Minute,
		}),
		kubefake.Event(kubefake.EventSpec{
			Namespace: "demo", Name: "web-7c9-c.2", Type: "Warning", Reason: "BackOff", Message: "Back-off restarting failed container main",
			Count: 12, Kind: "Pod", ObjectName: "web-7c9-c", UID: uid, Ago: time.Minute,
		}),
	)
	fake.SetLog("demo", "web-7c9-c", "main", false, "start", "fout: kan db niet bereiken")
	fake.SetLog("demo", "web-7c9-c", "main", true, "vorige run", "panic: geen verbinding")
	return fake
}

// Een object openen geeft zijn toestand, keten, events, YAML en logs;
// netwerk, opslag, configuratie, gebeurtenissen en alle soorten werken.
func TestKubernetesDetails(t *testing.T) {
	e, c := adminClient(t)
	fake := detailCluster(t)
	ctx := context.Background()

	var cl cluster
	c.do("POST", "/api/v1/clusters", map[string]any{"slug": "leren", "name": "Leren", "type": "kubernetes", "environment": "lab"}, &cl)
	var server, worker node
	c.do("POST", "/api/v1/nodes", map[string]any{"hostname": "leren-server-01", "cluster_id": cl.ID, "primary_ip": "10.0.40.11"}, &server)
	c.do("POST", "/api/v1/nodes", map[string]any{"hostname": "leren-worker-01", "cluster_id": cl.ID, "primary_ip": "10.0.40.12"}, &worker)
	// De worker is VM 412 op pve2.
	conn := uuid.New()
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO proxmox_connections (id, name, api_url, token_id, token_secret_enc, key_id)
			VALUES ($1, 'lab', 'https://pve.example:8006', 'cf@pve!t', '\x00', 'k')`, []any{conn}},
		{`UPDATE nodes SET proxmox_id = $1, pve_vmid = 412 WHERE id = $2`, []any{conn, worker.ID}},
		{`INSERT INTO proxmox_resources (connection_id, pve_id, type, pve_node, vmid, name, status, data, synced_at)
			VALUES ($1, 'qemu/412', 'qemu', 'pve2', 412, 'leren-worker-01', 'running', '{}', now())`, []any{conn}},
	} {
		if _, err := e.pool.Exec(ctx, q.sql, q.args...); err != nil {
			t.Fatal(err)
		}
	}
	startAgentWith(t, e, c, server.ID, "a1000000000000000000000000000004", &addrs{}, func(ag *agent.Agent, root string) {
		if err := fake.WriteKubeconfig(root); err != nil {
			t.Fatal(err)
		}
	})
	base := "/api/v1/kubernetes/clusters/" + cl.ID
	eventually(t, "overzicht", func() bool { return c.do("GET", base, nil, nil) == http.StatusOK })

	// De pod in CrashLoopBackOff: van ReplicaSet tot Deployment, bereikbaar
	// via de Service en de Ingress, met de vorige container en de events.
	var o kubeObject
	if s := c.do("GET", base+"/object?version=v1&resource=pods&namespace=demo&name=web-7c9-c", nil, &o); s != http.StatusOK {
		t.Fatalf("pod: %d", s)
	}
	if o.Kind != "Pod" || o.APIVersion != "v1" || !o.Problem || !strings.HasPrefix(o.Summary, "CrashLoopBackOff op leren-worker-02") ||
		!strings.Contains(o.Summary, "7 herstarts") || o.Labels["app"] != "web" || o.UID == "" {
		t.Fatalf("pod: %+v", o)
	}
	owners := o.link("owner")
	if len(owners) != 2 || owners[0].Kind != "ReplicaSet" || owners[0].Ref == nil ||
		*owners[0].Ref != (kubeRef{Group: "apps", Version: "v1", Resource: "replicasets", Namespace: "demo", Name: "web-7c9"}) ||
		owners[1].Kind != "Deployment" || owners[1].Name != "web" || owners[1].Ref.Resource != "deployments" {
		t.Fatalf("eigenaars: %+v", owners)
	}
	// worker-02 staat niet in ClusterForge: alleen de node zelf.
	if n := o.link("node"); len(n) != 1 || n[0].Name != "leren-worker-02" || len(o.link("clusterforge")) != 0 {
		t.Fatalf("node: %+v", o.Chain)
	}
	if sv := o.link("service"); len(sv) != 1 || sv[0].Name != "web" || sv[0].Text != "ClusterIP op 10.43.20.5, poort 80" {
		t.Fatalf("service: %+v", sv)
	}
	if in := o.link("ingress"); len(in) != 1 || in[0].URL != "http://web.leren.lab/" || in[0].Ref.Group != "networking.k8s.io" {
		t.Fatalf("ingress: %+v", in)
	}
	if o.Pod == nil || o.Pod.Node != "leren-worker-02" || o.Pod.Qos != "BestEffort" || len(o.Pod.Containers) != 2 {
		t.Fatalf("pod-detail: %+v", o.Pod)
	}
	initC, main := o.Pod.Containers[0], o.Pod.Containers[1]
	if !initC.Init || initC.Name != "wacht-op-db" || initC.State != "terminated" || initC.Reason != "Completed" {
		t.Fatalf("init-container: %+v", initC)
	}
	if main.Init || main.State != "waiting" || main.Reason != "CrashLoopBackOff" || main.Restarts != 7 || main.LastReason != "Error" ||
		main.LastExitCode == nil || *main.LastExitCode != 1 {
		t.Fatalf("container: %+v", main)
	}
	if len(o.Events) != 2 || o.Events[0].Reason != "BackOff" || o.Events[0].Type != "Warning" || o.Events[0].Count != 12 ||
		o.Events[0].Source != "kubelet" || o.Events[0].Object == nil || o.Events[0].Object.Name != "web-7c9-c" {
		t.Fatalf("events: %+v", o.Events)
	}
	if !strings.Contains(o.YAML, "kind: Pod\n") || !strings.Contains(o.YAML, "  name: web-7c9-c\n") || strings.Contains(o.YAML, "managedFields") {
		t.Fatalf("yaml:\n%s", o.YAML)
	}

	// Een pod op een node in ClusterForge: tot de VM op de Proxmox-host,
	// en zijn volume.
	c.do("GET", base+"/object?version=v1&resource=pods&namespace=demo&name=web-7c9-a", nil, &o)
	cf, vm := o.link("clusterforge"), o.link("vm")
	if len(cf) != 1 || cf[0].Name != "leren-worker-01" || cf[0].NodeID == nil || *cf[0].NodeID != worker.ID ||
		len(vm) != 1 || vm[0].Text != "VM 412 op pve2" || o.Problem || !strings.HasPrefix(o.Summary, "Draait op leren-worker-01") {
		t.Fatalf("keten naar de VM: %+v %s", o.Chain, o.Summary)
	}
	if v := o.link("volume"); len(v) != 1 || v[0].Name != "data" || v[0].Ref.Resource != "persistentvolumeclaims" {
		t.Fatalf("volume: %+v", v)
	}
	c.do("GET", base+"/object?version=v1&resource=nodes&name=leren-worker-01", nil, &o)
	if o.Kind != "Node" || o.Summary != "Ready" || len(o.link("vm")) != 1 || len(o.link("node")) != 0 {
		t.Fatalf("node: %+v", o)
	}
	c.do("GET", base+"/object?group=apps&version=v1&resource=deployments&namespace=demo&name=web", nil, &o)
	if !o.Problem || !strings.HasPrefix(o.Summary, "Niet alles draait") || len(o.link("service")) != 1 || len(o.link("ingress")) != 1 {
		t.Fatalf("deployment: %+v", o)
	}
	c.do("GET", base+"/object?version=v1&resource=services&namespace=demo&name=web", nil, &o)
	if p := o.link("pods"); len(p) != 1 || p[0].Name != "app=web" || p[0].Text != "3 pods, 2 klaar" {
		t.Fatalf("service: %+v", o.Chain)
	}
	c.do("GET", base+"/object?version=v1&resource=persistentvolumes&name=pvc-1234", nil, &o)
	if cl := o.link("claim"); len(cl) != 1 || cl[0].Name != "demo/data" || len(o.link("node")) != 1 {
		t.Fatalf("volume: %+v", o.Chain)
	}
	// Een Secret toont nooit zijn waarden, ook niet in de YAML.
	c.do("GET", base+"/object?version=v1&resource=secrets&namespace=demo&name=api", nil, &o)
	if strings.Contains(o.YAML, "YWJjMTIz") || !strings.Contains(o.YAML, "token: (verborgen, 6 bytes)") {
		t.Fatalf("secret:\n%s", o.YAML)
	}
	// Een CRD van Traefik opent net zo.
	c.do("GET", base+"/object?group=traefik.io&version=v1alpha1&resource=ingressroutes&namespace=demo&name=dashboard", nil, &o)
	if o.Kind != "IngressRoute" || !strings.Contains(o.YAML, "entryPoints:\n    - web\n") {
		t.Fatalf("ingressroute: %+v", o)
	}

	var ae apiErr
	for _, bad := range []string{
		"version=v1;x&resource=pods&namespace=demo&name=a", "version=v1&resource=pods%2Fexec&namespace=demo&name=a",
		"version=v1&resource=pods&namespace=demo&name=..%2Fx", "group=Apps&version=v1&resource=deployments&namespace=demo&name=web",
		"group=apps&version=1&resource=deployments&namespace=demo&name=web",
	} {
		if s := c.do("GET", base+"/object?"+bad, nil, &ae); s != http.StatusBadRequest {
			t.Errorf("%s: %d %+v", bad, s, ae)
		}
	}
	if s := c.do("GET", base+"/object?version=v1&resource=pods&namespace=demo&name=weg", nil, &ae); s != http.StatusNotFound {
		t.Fatalf("onbekende pod: %d %+v", s, ae)
	}

	// Logs, ook van de container voor de laatste herstart.
	type logs struct {
		Text      string `json:"text"`
		Truncated bool   `json:"truncated"`
	}
	var l logs
	if s := c.do("GET", base+"/logs?namespace=demo&pod=web-7c9-c", nil, &l); s != http.StatusOK || l.Text != "start\nfout: kan db niet bereiken\n" || l.Truncated {
		t.Fatalf("log: %d %+v", s, l)
	}
	c.do("GET", base+"/logs?namespace=demo&pod=web-7c9-c&previous=true&lines=1", nil, &l)
	if l.Text != "panic: geen verbinding\n" {
		t.Fatalf("vorige log: %+v", l)
	}
	c.do("GET", base+"/logs?namespace=demo&pod=web-7c9-c&container=main&timestamps=true&lines=1", nil, &l)
	if !strings.HasSuffix(l.Text, "Z fout: kan db niet bereiken\n") {
		t.Fatalf("log met tijd: %+v", l)
	}
	// Van een log groter dan 2 MB blijven de nieuwste regels over.
	big := make([]string, 3000)
	for i := range big {
		big[i] = fmt.Sprintf("regel %04d %s", i, strings.Repeat("x", 990))
	}
	fake.SetLog("demo", "web-7c9-b", "main", false, big...)
	c.do("GET", base+"/logs?namespace=demo&pod=web-7c9-b&lines=5000", nil, &l)
	if !l.Truncated || !strings.HasPrefix(l.Text, "regel ") || !strings.HasSuffix(l.Text, big[2999]+"\n") || len(l.Text) > 2<<20 {
		t.Fatalf("grote log: %v %d %.20q", l.Truncated, len(l.Text), l.Text)
	}
	if s := c.do("GET", base+"/logs?namespace=demo&pod=web-7c9-a&previous=true", nil, &ae); s != http.StatusNotFound ||
		!strings.Contains(ae.Message, "nog niet herstart") {
		t.Fatalf("geen vorige: %d %+v", s, ae)
	}
	for _, bad := range []string{
		"namespace=demo&pod=web-7c9-c&container=..%2Fx", "namespace=demo&pod=web-7c9-c&lines=0", "namespace=demo&pod=web-7c9-c&lines=5001",
		"namespace=demo&pod=web-7c9-c&container=zijspan", "namespace=Demo&pod=web-7c9-c",
	} {
		if s := c.do("GET", base+"/logs?"+bad, nil, &ae); s != http.StatusBadRequest {
			t.Errorf("log %s: %d %+v", bad, s, ae)
		}
	}

	// Netwerk: de Service kiest drie pods, de Ingress heeft een adres.
	var nw struct {
		Services []struct {
			Namespace, Name, Type string
			ClusterIP             string   `json:"cluster_ip"`
			External              []string `json:"external"`
			Pods                  int      `json:"pods"`
			URL                   string   `json:"url"`
			Ports                 []struct {
				Port       int    `json:"port"`
				TargetPort string `json:"target_port"`
				NodePort   int    `json:"node_port"`
			} `json:"ports"`
		} `json:"services"`
		Ingresses []struct {
			Name    string   `json:"name"`
			Class   string   `json:"class"`
			Address []string `json:"address"`
			Rules   []struct {
				Host, Service, Port, URL string
			} `json:"rules"`
		} `json:"ingresses"`
	}
	c.do("GET", base+"/network", nil, &nw)
	// Zonder systeem: de Service kubernetes in default en web in demo.
	if len(nw.Services) != 2 || nw.Services[1].Name != "web" || nw.Services[1].Pods != 3 || nw.Services[1].Ports[0].TargetPort != "8080" ||
		len(nw.Ingresses) != 1 || nw.Ingresses[0].Class != "traefik" || nw.Ingresses[0].Address[0] != "10.0.40.100" ||
		nw.Ingresses[0].Rules[0].URL != "http://web.leren.lab/" || nw.Ingresses[0].Rules[0].Port != "80" {
		t.Fatalf("netwerk: %+v", nw)
	}
	c.do("GET", base+"/network?namespace=kube-system", nil, &nw)
	if len(nw.Services) != 1 || nw.Services[0].Name != "traefik" || nw.Services[0].Type != "LoadBalancer" ||
		!slices.Contains(nw.Services[0].External, "10.0.40.11") || nw.Services[0].URL != "http://10.0.40.11/" || nw.Services[0].Ports[0].NodePort != 30080 {
		t.Fatalf("traefik: %+v", nw.Services)
	}

	// Opslag: een gebonden claim op een node, een die wacht, en local-path.
	var st struct {
		Claims []struct {
			Name, Phase, Volume, Class string
			Bytes                      int64    `json:"bytes"`
			UsedBy                     []string `json:"used_by"`
		} `json:"claims"`
		Volumes []struct {
			Name, Claim, Node, Path, Reclaim string
		} `json:"volumes"`
		Classes []struct {
			Name, Provisioner, Binding string
			Default                    bool `json:"default"`
		} `json:"classes"`
	}
	c.do("GET", base+"/storage?namespace=demo", nil, &st)
	if len(st.Claims) != 2 || st.Claims[0].Name != "cache" || st.Claims[0].Phase != "Pending" || st.Claims[0].Bytes != 512<<20 ||
		st.Claims[1].Volume != "pvc-1234" || st.Claims[1].Bytes != 1<<30 || !slices.Equal(st.Claims[1].UsedBy, []string{"web-7c9-a"}) {
		t.Fatalf("claims: %+v", st.Claims)
	}
	if len(st.Volumes) != 1 || st.Volumes[0].Claim != "demo/data" || st.Volumes[0].Node != "leren-worker-01" || st.Volumes[0].Reclaim != "Delete" ||
		!strings.HasPrefix(st.Volumes[0].Path, "/var/lib/rancher/k3s/storage/pvc-1234") {
		t.Fatalf("volumes: %+v", st.Volumes)
	}
	if len(st.Classes) != 1 || st.Classes[0].Name != "local-path" || !st.Classes[0].Default || st.Classes[0].Binding != "WaitForFirstConsumer" {
		t.Fatalf("klassen: %+v", st.Classes)
	}

	// Configuratie: van een Secret alleen de sleutels en hun lengte.
	var cfg struct {
		ConfigMaps []struct {
			Namespace, Name string
			Keys            []string `json:"keys"`
			Bytes           int      `json:"bytes"`
		} `json:"config_maps"`
		Secrets []struct {
			Name, Type string
			Keys       []struct {
				Name  string `json:"name"`
				Bytes int    `json:"bytes"`
			} `json:"keys"`
		} `json:"secrets"`
	}
	if s := c.do("GET", base+"/config?namespace=demo", nil, &cfg); s != http.StatusOK || len(cfg.ConfigMaps) != 1 || cfg.ConfigMaps[0].Bytes != 9 ||
		len(cfg.Secrets) != 2 || cfg.Secrets[0].Name != "api" || cfg.Secrets[0].Type != "Opaque" || cfg.Secrets[0].Keys[0].Bytes != 6 ||
		cfg.Secrets[1].Keys[0].Name != "wachtwoord" || cfg.Secrets[1].Keys[0].Bytes != 17 {
		t.Fatalf("configuratie: %d %+v", s, cfg)
	}
	_, _, body := c.get(base + "/config?system=true")
	for _, secret := range []string{"YWJjMTIz", "abc123", "Z2VoZWltLXdhY2h0d29vcmQ=", "LS0tLS1CRUdJTi"} {
		if strings.Contains(body, secret) {
			t.Fatalf("secretwaarde %s in %s", secret, body)
		}
	}
	if !strings.Contains(body, `"k3s-serving"`) || !strings.Contains(body, `"coredns"`) {
		t.Fatalf("systeem: %s", body)
	}

	// Gebeurtenissen: waarschuwingen eerst.
	var evs struct{ Items []kubeEvent }
	c.do("GET", base+"/events", nil, &evs)
	if len(evs.Items) != 3 || evs.Items[0].Type != "Warning" || evs.Items[1].Type != "Warning" || evs.Items[2].Reason != "Pulled" ||
		evs.Items[2].Object == nil || evs.Items[2].Object.Resource != "pods" {
		t.Fatalf("events: %+v", evs.Items)
	}
	c.do("GET", base+"/events?warnings=true", nil, &evs)
	if len(evs.Items) != 2 || slices.ContainsFunc(evs.Items, func(e kubeEvent) bool { return e.Type != "Warning" }) {
		t.Fatalf("waarschuwingen: %+v", evs.Items)
	}

	// Alles: de soorten, ook die van k3s en Traefik, en hun objecten.
	var types struct {
		Items []struct {
			Group, Version, Resource, Kind string
			Namespaced                     bool
		}
	}
	c.do("GET", base+"/resources", nil, &types)
	kinds := map[string]bool{}
	for _, ty := range types.Items {
		kinds[ty.Group+"/"+ty.Resource] = true
		if strings.Contains(ty.Resource, "/") {
			t.Fatalf("subresource: %+v", ty)
		}
	}
	if types.Items[0].Group != "" || !kinds["/pods"] || !kinds["apps/deployments"] || !kinds["helm.cattle.io/helmcharts"] ||
		!kinds["traefik.io/ingressroutes"] || !kinds["metrics.k8s.io/nodes"] {
		t.Fatalf("soorten: %+v", types.Items)
	}
	var objs struct {
		Items []struct{ Namespace, Name, Status string }
	}
	c.do("GET", base+"/objects?group=traefik.io&version=v1alpha1&resource=ingressroutes", nil, &objs)
	if len(objs.Items) != 1 || objs.Items[0].Name != "dashboard" {
		t.Fatalf("ingressroutes: %+v", objs.Items)
	}
	c.do("GET", base+"/objects?version=v1&resource=namespaces", nil, &objs)
	// Een Namespace heeft zelf geen namespace: ze staan er alle vijf.
	if len(objs.Items) != 5 || objs.Items[0].Name != "default" || objs.Items[0].Status != "Active" {
		t.Fatalf("namespaces: %+v", objs.Items)
	}
	c.do("GET", base+"/objects?group=apps&version=v1&resource=deployments&system=true", nil, &objs)
	if len(objs.Items) != 5 {
		t.Fatalf("deployments: %+v", objs.Items)
	}
	if s := c.do("GET", base+"/objects?version=v1&resource=pods%2Flog", nil, &ae); s != http.StatusBadRequest {
		t.Fatalf("subresource: %d", s)
	}

	// Een viewer leest ook de logs.
	e.createUser("kijker", "een-lang-wachtwoord", store.UserRoleViewer)
	v := e.client()
	v.login("kijker", "een-lang-wachtwoord", "")
	if s := v.do("GET", base+"/logs?namespace=demo&pod=web-7c9-c", nil, &l); s != http.StatusOK || l.Text == "" {
		t.Fatalf("viewer: %d", s)
	}
}
