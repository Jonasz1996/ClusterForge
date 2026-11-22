package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/deploy"
	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/httpapi/gen"
	"github.com/Jonasz1996/clusterforge/internal/kube"
)

type kubeUpgrade struct {
	Version    string `json:"version"`
	K3s        string `json:"k3s"`
	Next       *struct{ Version, K3s string }
	Blocked    string `json:"blocked"`
	Behind     bool   `json:"behind"`
	GitManaged bool   `json:"git_managed"`
	Nodes      []struct {
		Name          string `json:"name"`
		Server        bool   `json:"server"`
		Kubelet       string `json:"kubelet"`
		Ready         bool   `json:"ready"`
		Unschedulable bool   `json:"unschedulable"`
	} `json:"nodes"`
	NodesError string `json:"nodes_error"`
}

type upgradeStep struct {
	Step    string `json:"step"`
	Change  string `json:"change"`
	Summary string `json:"summary"`
}

type upgradePlan struct {
	Plan struct {
		Params   []struct{ Field, From, To string } `json:"params"`
		Warnings []string                           `json:"warnings"`
		Nodes    []struct {
			Hostname string        `json:"hostname"`
			Role     string        `json:"role"`
			Steps    []upgradeStep `json:"steps"`
		} `json:"nodes"`
		Summary string `json:"summary"`
	} `json:"plan"`
	Hash    string `json:"hash"`
	Blocked string `json:"blocked"`
}

// waitJobLong wacht als waitJob, maar langer: een uitrol per node wacht
// op elke node tot hij gezond is.
func waitJobLong(t *testing.T, c *client, id string) job {
	t.Helper()
	var j job
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		c.do("GET", "/api/v1/jobs/"+id, nil, &j)
		if j.Status != "queued" && j.Status != "running" {
			return j
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("taak %s niet klaar: %+v", id, j)
	return j
}

// TestKubernetesUpgrade is de acceptatietest van mijlpaal 11: een cluster
// op kubernetes 1.0.0 gaat naar 1.1.0, node voor node met leegmaken, en
// stopt bij de eerste node die niet terugkomt. Na de reparatie maakt
// Opnieuw toepassen het af.
func TestKubernetesUpgrade(t *testing.T) {
	e, c := adminClient(t)
	ctx := context.Background()
	l := newLeren(t, e, c)

	// Leren op 1.0.0, met k3s v1.35.9.
	var in gen.DeployInput
	b, _ := json.Marshal(l.request(nil))
	if err := json.Unmarshal(b, &in); err != nil {
		t.Fatal(err)
	}
	var admin uuid.UUID
	if err := e.pool.QueryRow(ctx, "SELECT id FROM users WHERE username = 'admin'").Scan(&admin); err != nil {
		t.Fatal(err)
	}
	dj, id, err := e.deploy.RequestWith(ctx, events.User(admin), deployRequest(in), deploy.RequestOptions{Version: "1.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	if j := waitJobLong(t, c, dj.ID.String()); j.Status != "succeeded" {
		t.Fatalf("uitrol: %+v", j)
	}
	clusterID := id.String()
	base, kbase := "/api/v1/clusters/"+clusterID, "/api/v1/kubernetes/clusters/"+clusterID
	var cl struct {
		Status          string `json:"status"`
		SpecRevision    int    `json:"spec_revision"`
		AppliedRevision int    `json:"applied_revision"`
	}
	eventually(t, "Leren gezond", func() bool {
		c.do("GET", base, nil, &cl)
		return cl.Status == "healthy" && cl.AppliedRevision == 1
	})

	// Het overzicht biedt 1.1.0 aan, met k3s v1.36.5.
	var up kubeUpgrade
	if s := c.do("GET", kbase+"/upgrade", nil, &up); s != 200 || up.Version != "1.0.0" || up.K3s != "v1.35.9+k3s1" || up.Next == nil ||
		up.Next.Version != "1.1.0" || up.Next.K3s != "v1.36.5+k3s1" || up.Blocked != "" || len(up.Nodes) != 3 ||
		up.Nodes[0].Name != "leren-server-01" || !up.Nodes[0].Server || up.Nodes[2].Kubelet != "v1.35.9+k3s1" {
		t.Fatalf("bijwerken: %d %+v", s, up)
	}

	// Het plan: eerst de server, dan de workers, met de nieuwe k3s erbij.
	var sp upgradePlan
	if s := c.do("POST", base+"/spec/plan", map[string]any{"version": "1.1.0", "params": map[string]any{}}, &sp); s != 200 || sp.Blocked != "" {
		t.Fatalf("plan: %d %+v", s, sp)
	}
	var order []string
	for _, n := range sp.Plan.Nodes {
		order = append(order, n.Role+" "+n.Hostname)
	}
	if !slices.Equal(order, []string{"server leren-server-01", "worker leren-worker-01", "worker leren-worker-02"}) ||
		!slices.ContainsFunc(sp.Plan.Params, func(p struct{ Field, From, To string }) bool {
			return p.Field == "kubernetes.version" && p.From == "v1.35.9+k3s1" && p.To == "v1.36.5+k3s1"
		}) {
		t.Fatalf("volgorde %v, parameters %+v", order, sp.Plan.Params)
	}
	if w := strings.Join(sp.Plan.Warnings, "\n"); !strings.Contains(w, "Kubernetes gaat naar v1.36.5, node voor node") ||
		!strings.Contains(w, "Er is maar één server, leren-server-01") || strings.Contains(w, "staat niet meer in de template") {
		t.Fatalf("waarschuwingen: %s", w)
	}
	// Het installatiecommando is één gewijzigde stap per node, geen nieuwe
	// stap naast een die weg is.
	for _, n := range sp.Plan.Nodes {
		var commands []upgradeStep
		for _, st := range n.Steps {
			if strings.HasPrefix(st.Step, "command:") {
				commands = append(commands, st)
			}
		}
		if len(commands) != 1 || commands[0].Change != "changed" ||
			!strings.Contains(commands[0].Summary, "maakt nu /usr/local/lib/clusterforge/k3s-v1.36.5+k3s1") {
			t.Fatalf("commando's op %s: %+v", n.Hostname, commands)
		}
	}
	if !strings.Contains(sp.Plan.Summary, "k3s van v1.35.9+k3s1 naar v1.36.5+k3s1") || !strings.Contains(sp.Plan.Summary, "commando install-k3s.sh op 3 nodes") {
		t.Fatalf("samenvatting: %s", sp.Plan.Summary)
	}

	// Op worker-01 komt k3s-agent na de herstart niet terug.
	l.fake.Add(webPod("web-5d9c-w2", "leren-worker-02"))
	w2 := l.f.host("leren-worker-02")
	w2Calls := len(w2.Calls())
	l.breakK3s("leren-worker-01", true)
	var ap specApplied
	if s := c.do("POST", base+"/spec", map[string]any{"version": "1.1.0", "params": map[string]any{}, "hash": sp.Hash}, &ap); s != http.StatusAccepted ||
		ap.Job == nil {
		t.Fatalf("toepassen: %d %+v", s, ap)
	}
	j := waitJobLong(t, c, ap.Job.ID)
	var names []string
	logs := map[string]string{}
	for _, st := range j.Steps {
		names = append(names, st.Name+": "+st.Status)
		logs[st.Name] = strings.Join(st.Log, "\n")
	}
	if want := []string{"Toepassen op leren-server-01 (VIP-eigenaar): succeeded", "Toepassen op leren-worker-01: failed"}; j.Status != "failed" ||
		!slices.Equal(names, want) || !strings.Contains(j.Error, "leren-worker-01") || !strings.Contains(j.Error, "blijft op cordon") {
		t.Fatalf("eerste poging: %s %v %s\n%v", j.Status, names, j.Error, logs)
	}
	srvLog := logs["Toepassen op leren-server-01 (VIP-eigenaar)"]
	for _, w := range []string{"leren-server-01 is de enige server", "leren-server-01 draait k3s v1.35.9+k3s1 en krijgt v1.36.5+k3s1",
		"leren-server-01 staat op cordon", "leren-server-01 is Ready met k3s v1.36.5+k3s1", "leren-server-01 krijgt weer pods"} {
		if !strings.Contains(srvLog, w) {
			t.Errorf("log van server-01 mist %q:\n%s", w, srvLog)
		}
	}
	node := func(name string) kube.Node {
		t.Helper()
		var n kube.Node
		b, _ := json.Marshal(l.fake.Get("v1", "Node", "", name))
		if err := json.Unmarshal(b, &n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	ours := func(n kube.Node) bool { _, ok := n.Metadata.Annotations[kube.MarkAnnotation]; return ok }
	if n := node("leren-server-01"); n.Status.NodeInfo.KubeletVersion != "v1.36.5+k3s1" || n.Spec.Unschedulable || ours(n) {
		t.Fatalf("server-01: %+v %+v", n.Status.NodeInfo, n.Spec)
	}
	if n := node("leren-worker-01"); n.Status.NodeInfo.KubeletVersion != "v1.35.9+k3s1" || !n.Spec.Unschedulable || !ours(n) {
		t.Fatalf("worker-01 hoort op cordon te blijven: %+v %+v", n.Status.NodeInfo, n.Spec)
	}
	if n := node("leren-worker-02"); n.Status.NodeInfo.KubeletVersion != "v1.35.9+k3s1" || n.Spec.Unschedulable {
		t.Fatalf("worker-02 is aangeraakt: %+v %+v", n.Status.NodeInfo, n.Spec)
	}
	if calls := w2.Calls()[w2Calls:]; slices.ContainsFunc(calls, func(c string) bool { return !strings.HasPrefix(c, "nft") }) {
		t.Fatalf("op worker-02 gebeurde iets: %v", calls)
	}
	if l.fake.Get("v1", "Pod", "leren", "web-5d9c-w2") == nil {
		t.Fatal("de pod op worker-02 is weg")
	}

	// Het overzicht zegt dat de toepassing niet af is; bijwerken kan nu
	// niet, en terug naar 1.0.0 ook niet.
	if s := c.do("GET", kbase+"/upgrade", nil, &up); s != 200 || up.Version != "1.1.0" || up.Next != nil || !up.Behind ||
		!strings.Contains(up.Blocked, "Opnieuw toepassen") {
		t.Fatalf("na de fout: %d %+v", s, up)
	}

	// Gerepareerd: Opnieuw toepassen maakt het af. Server-01 heeft de nieuwe
	// k3s al en blijft staan, worker-01 en worker-02 gaan leeg en krijgen
	// daarna weer pods.
	l.breakK3s("leren-worker-01", false)
	var rj job
	if s := c.do("POST", base+"/git/reapply", map[string]any{}, &rj); s != http.StatusAccepted {
		t.Fatalf("opnieuw toepassen: %d %+v", s, rj)
	}
	j = waitJobLong(t, c, rj.ID)
	logs = map[string]string{}
	for _, st := range j.Steps {
		logs[st.Name] = strings.Join(st.Log, "\n")
	}
	if j.Status != "succeeded" {
		t.Fatalf("tweede poging: %s %s\n%v", j.Status, j.Error, logs)
	}
	if srv := logs["Toepassen op leren-server-01 (VIP-eigenaar)"]; strings.Contains(srv, "staat op cordon") || !strings.Contains(srv, "is Ready met k3s v1.36.5+k3s1") {
		t.Fatalf("server-01 opnieuw leeggemaakt:\n%s", srv)
	}
	if !strings.Contains(logs["Toepassen op leren-worker-01"], "leren-worker-01 stond al op cordon") {
		t.Fatalf("worker-01:\n%s", logs["Toepassen op leren-worker-01"])
	}
	// Op worker-02 in de juiste volgorde: leeg, de nieuwe k3s, herstarten,
	// Ready met de nieuwe versie, weer pods.
	w2Log := logs["Toepassen op leren-worker-02"]
	last := -1
	for _, w := range []string{"leren-worker-02 staat op cordon", "web-5d9c-w2", "install-k3s.sh", "systemctl restart k3s-agent",
		"leren-worker-02 is Ready met k3s v1.36.5+k3s1", "leren-worker-02 krijgt weer pods"} {
		i := strings.Index(w2Log, w)
		if i <= last {
			t.Fatalf("worker-02: %q niet na het vorige:\n%s", w, w2Log)
		}
		last = i
	}
	for _, name := range []string{"leren-server-01", "leren-worker-01", "leren-worker-02"} {
		if n := node(name); n.Status.NodeInfo.KubeletVersion != "v1.36.5+k3s1" || n.Spec.Unschedulable || ours(n) {
			t.Fatalf("%s: %+v %+v", name, n.Status.NodeInfo, n.Spec)
		}
	}
	eventually(t, "Leren weer gezond", func() bool {
		c.do("GET", base, nil, &cl)
		return cl.Status == "healthy" && cl.AppliedRevision == 2 && cl.SpecRevision == 2
	})
	if s := c.do("GET", kbase+"/upgrade", nil, &up); s != 200 || up.K3s != "v1.36.5+k3s1" || up.Next != nil || up.Behind || up.Blocked != "" ||
		slices.ContainsFunc(up.Nodes, func(n struct {
			Name          string `json:"name"`
			Server        bool   `json:"server"`
			Kubelet       string `json:"kubelet"`
			Ready         bool   `json:"ready"`
			Unschedulable bool   `json:"unschedulable"`
		}) bool {
			return n.Kubelet != "v1.36.5+k3s1"
		}) {
		t.Fatalf("bijgewerkt: %d %+v", s, up)
	}

	// Terug naar 1.0.0 kan niet.
	var er deployErr
	if s := c.do("POST", base+"/spec/plan", map[string]any{"version": "1.0.0", "params": map[string]any{}}, &er); s != 400 || er.Field == nil ||
		*er.Field != "version" || !strings.Contains(er.Message, "kan niet terug") {
		t.Fatalf("terug: %d %+v", s, er)
	}
}
