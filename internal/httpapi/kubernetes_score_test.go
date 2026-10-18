package httpapi

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/agent"
	"github.com/Jonasz1996/clusterforge/internal/docs"
	"github.com/Jonasz1996/clusterforge/internal/kube/kubefake"
)

// TestKubernetesScore is de acceptatie van mijlpaal 12 van fase 4: de
// scorekaart Kubernetes met de bevindingen uit het cluster, en het
// hoofdstuk Kubernetes in de documentatie met de runbooks.
func TestKubernetesScore(t *testing.T) {
	e, c := adminClient(t)
	ctx := context.Background()
	fake := kubeCluster(t)
	// Bovenop Leren: shop/api met twee pods en een geheugenlimiet, en
	// shop/db met één pod, postgres:latest, een volume en vier herstarts.
	fake.Add(
		kubefake.Namespace("shop"),
		kubefake.Deployment(kubefake.WorkloadSpec{Namespace: "shop", Name: "api", Image: "ghcr.io/jonas/api:2.1.0", Replicas: 2, Ready: 2}),
		kubefake.Pod(kubefake.PodSpec{
			Namespace: "shop", Name: "api-5f6-a", Node: "leren-worker-01", Image: "ghcr.io/jonas/api:2.1.0", OwnerKind: "ReplicaSet", OwnerName: "api-5f6",
			Memory: "256Mi",
		}),
		kubefake.Pod(kubefake.PodSpec{
			Namespace: "shop", Name: "api-5f6-b", Node: "leren-server-01", Image: "ghcr.io/jonas/api:2.1.0", OwnerKind: "ReplicaSet", OwnerName: "api-5f6",
			Memory: "256Mi", Restarts: 1, LastReason: "OOMKilled",
		}),
		kubefake.StatefulSet(kubefake.WorkloadSpec{Namespace: "shop", Name: "db", Image: "postgres:latest", Replicas: 1, Ready: 1}),
		kubefake.Pod(kubefake.PodSpec{
			Namespace: "shop", Name: "db-0", Node: "leren-worker-01", Image: "postgres:latest", OwnerKind: "StatefulSet", OwnerName: "db",
			Claim: "data-db-0", Restarts: 4, LastReason: "Error",
		}),
		kubefake.Ingress(kubefake.IngressSpec{Namespace: "shop", Name: "api", Host: "shop.lab.thuis", Service: "api", Port: 80}),
		kubefake.SnapshotFile(kubefake.SnapshotSpec{
			Node: "leren-server-01", File: "etcd-snapshot-leren-server-01-1", Taken: time.Now().Add(-30 * time.Hour), Size: 6283296,
		}),
	)
	fake.Add(kubefake.Volume(kubefake.VolumeSpec{Namespace: "shop", Claim: "data-db-0", Volume: "pvc-3f1c", Size: "5Gi", Node: "leren-worker-01"})...)

	var cl cluster
	if s := c.do("POST", "/api/v1/clusters", map[string]any{"slug": "leren", "name": "Leren", "type": "kubernetes", "environment": "prod"}, &cl); s != 201 {
		t.Fatalf("cluster: %d", s)
	}
	c.do("POST", "/api/v1/clusters/"+cl.ID+"/vips", map[string]any{"address": "10.0.40.100"}, nil)
	var server node
	c.do("POST", "/api/v1/nodes", map[string]any{"hostname": "leren-server-01", "cluster_id": cl.ID, "primary_ip": "10.0.40.11"}, &server)
	startAgentWith(t, e, c, server.ID, "a1000000000000000000000000000041", &addrs{}, func(ag *agent.Agent, root string) {
		if err := fake.WriteKubeconfig(root); err != nil {
			t.Fatal(err)
		}
		ag.KubeletHealthz = fake.KubeletURL()
	})
	id := uuid.MustParse(cl.ID)
	eventually(t, "sync", func() bool { return e.kube.Sync(ctx, id) == nil })

	e.risk.Warmup = 0
	compute := func() clusterRiskView {
		t.Helper()
		if err := e.risk.Compute(ctx); err != nil {
			t.Fatal(err)
		}
		var v clusterRiskView
		if s := c.do("GET", "/api/v1/clusters/"+cl.ID+"/risk", nil, &v); s != http.StatusOK {
			t.Fatalf("risico: %d", s)
		}
		return v
	}
	v := compute()
	card := v.card(t, "kubernetes")
	if card.Weight != 15 || card.Score == nil || *card.Score >= 100 {
		t.Fatalf("kaart: %+v", card)
	}
	one := func(check string) riskFinding {
		t.Helper()
		fs := v.findings(check)
		if len(fs) != 1 {
			t.Fatalf("%s: %+v", check, fs)
		}
		return fs[0]
	}

	// worker-02 is niet gekoppeld aan een ClusterForge-node; prod maakt
	// het erger.
	nr := one("kubernetes.node_not_ready")
	if nr.Card != "kubernetes" || nr.Severity != "high" || nr.Subject.Type != "kube_node" || nr.Subject.Name != "leren-worker-02" ||
		nr.Link != "/kubernetes/cluster?id="+cl.ID+"&tab=nodes" {
		t.Fatalf("NotReady: %+v", nr)
	}
	if f := one("kubernetes.single_server"); f.Severity != "medium" || !strings.Contains(f.Text, "maar één Kubernetes-server: valt leren-server-01 uit") {
		t.Fatalf("één server: %+v", f)
	}
	if f := one("kubernetes.snapshot_stale"); f.Severity != "high" || !strings.Contains(f.Text, "De nieuwste etcd-snapshot van Leren is") ||
		f.Link != "/kubernetes/cluster?id="+cl.ID+"&tab=snapshots" {
		t.Fatalf("snapshot: %+v", f)
	}
	if fs := v.findings("kubernetes.snapshot_missing"); len(fs) != 0 {
		t.Fatalf("snapshot ontbreekt: %+v", fs)
	}
	// k3s 1.35 krijgt nog updates tot februari 2027.
	if fs := v.findings("kubernetes.version_eol"); len(fs) != 0 {
		t.Fatalf("versie: %+v", fs)
	}

	// demo/web in CrashLoopBackOff en shop/db met vier herstarts; shop/api
	// herstartte één keer, dat is geen patroon.
	restarts := v.findings("kubernetes.restarts")
	if len(restarts) != 2 || restarts[0].Subject.Name != "demo/web" || restarts[1].Subject.Name != "shop/db" || restarts[0].Subject.Type != "kube_app" ||
		!strings.Contains(restarts[0].Text, "7 herstarts van de pods die er nu zijn, nu in CrashLoopBackOff") ||
		!strings.Contains(restarts[1].Text, "shop/db blijft herstarten: 4 herstarts") || strings.Contains(restarts[1].Text, "CrashLoopBackOff") {
		t.Fatalf("herstarts: %+v", restarts)
	}
	if l := restarts[1].Link; !strings.HasPrefix(l, "/kubernetes/object?") || !strings.Contains(l, "r=statefulsets") || !strings.Contains(l, "name=db") ||
		!strings.Contains(l, "ns=shop") {
		t.Fatalf("link naar de app: %s", l)
	}
	if f := one("kubernetes.single_pod"); f.Severity != "low" || !strings.Contains(f.Text, "1 app op prod met één pod: shop/db.") {
		t.Fatalf("één pod: %+v", f)
	}
	if f := one("kubernetes.latest_image"); !strings.Contains(f.Text, "1 app gebruikt een image zonder vaste versie: shop/db (postgres:latest).") {
		t.Fatalf("latest: %+v", f)
	}
	// shop/api heeft een limiet; de apps van k3s zelf tellen niet mee.
	if f := one("kubernetes.memory_limit"); !strings.Contains(f.Text, "2 apps hebben pods zonder geheugenlimiet: demo/web en shop/db.") {
		t.Fatalf("geheugenlimiet: %+v", f)
	}

	// De herstarts van shop/db accepteren werkt zoals bij elke bevinding.
	if s := c.do("POST", "/api/v1/risk/acceptances", map[string]any{
		"check": "kubernetes.restarts", "subject_type": "kube_app", "subject_id": restarts[1].Subject.ID, "reason": "Migratie loopt, weet ik",
	}, nil); s != http.StatusNoContent {
		t.Fatalf("accepteren: %d", s)
	}
	before := *card.Score
	v = compute()
	card = v.card(t, "kubernetes")
	if rs := v.findings("kubernetes.restarts"); len(rs) != 2 || rs[1].Accepted == nil || rs[0].Accepted != nil || *card.Score <= before {
		t.Fatalf("na accepteren: %d -> %d %+v", before, *card.Score, rs)
	}

	// Een ander cluster heeft geen kaart Kubernetes.
	var other cluster
	c.do("POST", "/api/v1/clusters", map[string]any{"slug": "web", "name": "Web", "type": "keepalived", "environment": "lab"}, &other)
	if err := e.risk.Compute(ctx); err != nil {
		t.Fatal(err)
	}
	var ov clusterRiskView
	c.do("GET", "/api/v1/clusters/"+other.ID+"/risk", nil, &ov)
	if slices.ContainsFunc(ov.Cards, func(c riskCard) bool { return c.ID == "kubernetes" }) {
		t.Fatalf("kaart Kubernetes bij keepalived: %+v", ov.Cards)
	}

	// De documentatie heeft een hoofdstuk Kubernetes, na architectuur.
	var idx testDocIndex
	c.do("GET", "/api/v1/docs", nil, &idx)
	i := -1
	for j, x := range idx.Clusters {
		if x.ID == cl.ID {
			i = j
		}
	}
	if i < 0 || len(idx.Clusters[i].Docs) != 7 || idx.Clusters[i].Docs[1].Kind != "kubernetes" || idx.Clusters[i].Docs[1].Path != "clusters/leren/kubernetes.md" {
		t.Fatalf("index: %+v", idx)
	}
	if s := c.do("GET", "/api/v1/docs/cluster/kubernetes?cluster="+other.ID, nil, nil); s != http.StatusNotFound {
		t.Fatalf("hoofdstuk Kubernetes bij keepalived: %d", s)
	}
	if n, err := e.docs.GenerateCluster(ctx, id, docs.CauseManual); err != nil || n != 7 {
		t.Fatalf("schrijven: %d %v", n, err)
	}
	get := func(kind string) string {
		t.Helper()
		var d testDoc
		if s := c.do("GET", "/api/v1/docs/cluster/"+kind+"?cluster="+cl.ID, nil, &d); s != http.StatusOK || d.Version == nil {
			t.Fatalf("%s: %d %+v", kind, s, d.testDocMeta)
		}
		return d.Content
	}
	kd := get("kubernetes")
	for _, w := range []string{
		"# Leren: kubernetes",
		"k3s `v1.35.9`", "Kubernetes 1.35 krijgt updates tot februari 2027", "`https://10.0.40.100:6443`, via het VIP",
		"| **leren-server-01** | server | leren-server-01 | 10.0.40.11 | `v1.35.9` |", "| **leren-worker-02** | worker |",
		"Eigen namespaces: `demo` en `shop`.",
		"| **shop/db** | StatefulSet | 1 | `postgres:latest` |", "| **shop/api** | Deployment | 2 | `ghcr.io/jonas/api:2.1.0` |", "`coredns`",
		"shop.lab.thuis", "Ingress shop/api", "Service `api`, poort 80", "Service kube-system/traefik",
		"| **shop/data-db-0** | local-path | 5Gi | op leren-worker-01 in `/var/lib/rancher/k3s/storage/pvc-3f1c_shop_data-db-0` | db-0 |",
		"Een claim zonder klasse krijgt `local-path`.",
		"De nieuwste is ouder dan een dag",
		"## Een node vervangen", "**Worker toevoegen**", "Met één server is die server het cluster", "**Servers naar 3**",
		"## Bijwerken", "eerst leren-server-01, dan leren-worker-01 en leren-worker-02", "sudo journalctl -u k3s-agent -n 50",
		"## etcd terugzetten", "Op prod zet ClusterForge geen snapshot terug",
		"sudo k3s etcd-snapshot save --name voor-terugzetten",
		"--cluster-reset-restore-path=/var/lib/rancher/k3s/server/db/snapshots/<bestand>",
	} {
		if !strings.Contains(kd, w) {
			t.Errorf("kubernetes mist %q", w)
		}
	}
	// Geen secret, en niets wat elke minuut verandert.
	for _, bad := range []string{"Z2VoZWltLXdhY2h0d29vcmQ=", "CrashLoopBackOff", "NotReady", "OOMKilled"} {
		if strings.Contains(kd, bad) {
			t.Errorf("kubernetes bevat %q", bad)
		}
	}
	if t.Failed() {
		t.Fatalf("hoofdstuk:\n%s", kd)
	}
	rb := get("runbooks")
	if !strings.Contains(rb, "kubernetes.md#een-node-vervangen") || !strings.Contains(rb, "(cordon en drain)") {
		t.Fatalf("runbooks:\n%s", rb)
	}

	// Meer herstarts en een andere pod veranderen het hoofdstuk niet.
	fake.Add(kubefake.Pod(kubefake.PodSpec{
		Namespace: "demo", Name: "web-7c9-c", Node: "leren-worker-02", Waiting: "CrashLoopBackOff", Restarts: 9,
		OwnerKind: "ReplicaSet", OwnerName: "web-7c9",
	}))
	e.kube.Now = func() time.Time { return time.Now().Add(time.Minute) }
	if err := e.kube.Sync(ctx, id); err != nil {
		t.Fatal(err)
	}
	if n, err := e.docs.GenerateCluster(ctx, id, docs.CauseManual); err != nil || n != 0 {
		t.Fatalf("opnieuw schrijven: %d %v", n, err)
	}
}
