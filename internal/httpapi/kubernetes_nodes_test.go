package httpapi

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

type specEditForm struct {
	Template string   `json:"template"`
	Version  string   `json:"version"`
	Versions []string `json:"versions"`
	Blocked  string   `json:"blocked"`
	Params   []struct {
		Param struct {
			Name string `json:"name"`
			Type string `json:"type"`
		} `json:"param"`
		Value  any    `json:"value"`
		Locked string `json:"locked"`
	} `json:"params"`
}

type specEditPlan struct {
	Plan              gitPlan `json:"plan"`
	Hash              string  `json:"hash"`
	NeedsConfirmation bool    `json:"needs_confirmation"`
	FullApply         bool    `json:"full_apply"`
	Blocked           string  `json:"blocked"`
	BlockedCode       string  `json:"blocked_code"`
}

type specApplied struct {
	Revision int  `json:"revision"`
	Job      *job `json:"job"`
}

type removeNodePlan struct {
	Node     string `json:"node"`
	Hostname string `json:"hostname"`
	Ready    bool   `json:"ready"`
	Vmid     int    `json:"vmid"`
	Host     string `json:"host"`
	Workers  int    `json:"workers"`
	Drain    struct {
		Evict   []drainPod `json:"evict"`
		Blocked []drainPod `json:"blocked"`
	} `json:"drain"`
}

// TestKubernetesNodes is de acceptatietest van mijlpaal 7: een worker
// toevoegen maakt een VM die Ready wordt, van één naar drie servers lukt in
// één plan, en een worker weghalen maakt hem leeg en verwijdert node en VM.
// Een server weghalen en minder workers via de spec worden geweigerd. De VM
// verdwijnt alleen als hij nog de VM van die worker is, uit staat en geen
// protection heeft.
func TestKubernetesNodes(t *testing.T) {
	e, c := adminClient(t)
	ctx := context.Background()
	l := newLeren(t, e, c)
	l.f.mu.Lock()
	l.f.addrs["leren-worker-03"], l.f.addrs["leren-server-02"], l.f.addrs["leren-server-03"] = "10.0.40.14", "10.0.40.15", "10.0.40.16"
	l.f.mu.Unlock()
	clusterID, j := l.deploy(t, c, nil)
	if j.Status != "succeeded" {
		t.Fatalf("uitrol: %+v", j)
	}
	base := "/api/v1/clusters/" + clusterID
	var cl struct {
		Status          string `json:"status"`
		SpecRevision    int    `json:"spec_revision"`
		AppliedRevision int    `json:"applied_revision"`
		Nodes           []node `json:"nodes"`
	}
	healthy := func(what string, nodes, revision int) {
		t.Helper()
		eventually(t, what, func() bool {
			c.do("GET", base, nil, &cl)
			return cl.Status == "healthy" && len(cl.Nodes) == nodes && cl.AppliedRevision == revision && cl.SpecRevision == revision
		})
	}
	healthy("Leren gezond", 3, 1)
	revisions := func() []string {
		t.Helper()
		var hist struct {
			Items []struct {
				Revision int    `json:"revision"`
				Source   string `json:"source"`
			} `json:"items"`
		}
		c.do("GET", base+"/spec-revisions", nil, &hist)
		var out []string
		for _, it := range hist.Items {
			out = append(out, it.Source)
		}
		return out
	}

	// Het formulier: de waarden van nu, met wat vastligt.
	var form specEditForm
	if s := c.do("GET", base+"/spec/edit", nil, &form); s != 200 || form.Template != "kubernetes" || form.Version != "1.1.0" || form.Blocked != "" {
		t.Fatalf("formulier: %d %+v", s, form)
	}
	locked := map[string]string{}
	values := map[string]any{}
	for _, p := range form.Params {
		locked[p.Param.Name], values[p.Param.Name] = p.Locked, p.Value
	}
	if values["workers"] != float64(2) || values["servers"] != float64(1) || values["token"] != nil || locked["workers"] != "" ||
		!strings.Contains(locked["vip"], "Ligt vast") || !strings.Contains(locked["worker_memory"], "VM") || !strings.Contains(locked["token"], "geheim") {
		t.Fatalf("waarden %v, vast %v", values, locked)
	}

	// Minder workers kan niet via de spec, en een VM groter maken ook niet.
	var er deployErr
	plan := func(params map[string]any, out any) int {
		t.Helper()
		return c.do("POST", base+"/spec/plan", map[string]any{"params": params}, out)
	}
	if s := plan(map[string]any{"workers": 1}, &er); s != 400 || er.Field == nil || *er.Field != "params.workers" ||
		!strings.Contains(er.Message, "tabblad Nodes") {
		t.Fatalf("minder workers: %d %+v", s, er)
	}
	if s := plan(map[string]any{"worker_memory": "8G"}, &er); s != 400 || er.Field == nil || *er.Field != "params.worker_memory" {
		t.Fatalf("groter geheugen: %d %+v", s, er)
	}
	if s := plan(map[string]any{"workers": 2}, &er); s != 409 || er.Code != "no_change" {
		t.Fatalf("niets: %d %+v", s, er)
	}

	// Een worker erbij: alleen een nieuwe node, de andere veranderen niet.
	var sp specEditPlan
	if s := plan(map[string]any{"workers": 3}, &sp); s != 200 || sp.Blocked != "" || len(sp.Plan.NewNodes) != 1 ||
		sp.Plan.NewNodes[0].Hostname != "leren-worker-03" || sp.Plan.NewNodes[0].Address != "10.0.40.14" || len(sp.Plan.Nodes) != 1 ||
		!sp.Plan.Nodes[0].New || sp.FullApply || sp.NeedsConfirmation {
		t.Fatalf("plan: %d %+v", s, sp)
	}
	apply := func(params map[string]any, hash, serverURL string, out any) int {
		t.Helper()
		return c.do("POST", base+"/spec", map[string]any{"params": params, "hash": hash, "server_url": serverURL}, out)
	}
	if s := apply(map[string]any{"workers": 3}, "oud", e.srv.URL, &er); s != 409 || er.Code != "plan_changed" {
		t.Fatalf("ander plan: %d %+v", s, er)
	}
	if s := apply(map[string]any{"workers": 3}, sp.Hash, "", &er); s != 400 || er.Field == nil || *er.Field != "server_url" {
		t.Fatalf("zonder adres: %d %+v", s, er)
	}
	var ap specApplied
	if s := apply(map[string]any{"workers": 3}, sp.Hash, e.srv.URL, &ap); s != http.StatusAccepted || ap.Revision != 2 || ap.Job == nil ||
		ap.Job.Title != "1 node toevoegen aan Leren" {
		t.Fatalf("toepassen: %d %+v", s, ap)
	}
	j = waitJob(t, c, ap.Job.ID)
	steps := func(j job) []string {
		var out []string
		for _, st := range j.Steps {
			out = append(out, st.Name)
		}
		return out
	}
	logs := func(j job) string {
		out := ""
		for _, st := range j.Steps {
			out += strings.Join(st.Log, "\n") + "\n"
		}
		return out
	}
	if want := []string{"VM leren-worker-03 maken", "Agents aanmelden", "Toepassen op leren-worker-03 (nieuw)"}; j.Status != "succeeded" ||
		!slices.Equal(steps(j), want) || !strings.Contains(logs(j), "alle 4 nodes zijn Ready") {
		t.Fatalf("worker erbij: %s %v %s\n%s", j.Status, steps(j), j.Error, logs(j))
	}
	healthy("vier nodes", 4, 2)
	if got := revisions(); !slices.Equal(got, []string{"ui", "ui"}) {
		t.Fatalf("revisies: %v", got)
	}

	// Van één naar drie servers in één plan: twee nieuwe servers, en
	// keepalived.conf op server-01 kent ze als peers. Het VIP blijft op
	// server-01, ook al kan niemand het overnemen terwijl die bijwerkt.
	if s := plan(map[string]any{"servers": 3}, &sp); s != 200 || sp.Blocked != "" || len(sp.Plan.NewNodes) != 2 ||
		sp.Plan.NewNodes[0].Hostname != "leren-server-02" || sp.Plan.NewNodes[1].Address != "10.0.40.16" {
		t.Fatalf("drie servers: %d %+v", s, sp)
	}
	var order []string
	for _, n := range sp.Plan.Nodes {
		order = append(order, n.Hostname)
	}
	if !slices.Equal(order, []string{"leren-server-01", "leren-server-02", "leren-server-03"}) || len(sp.Plan.Nodes[0].Steps) != 1 ||
		sp.Plan.Nodes[0].Steps[0].Step != "file:/etc/keepalived/keepalived.conf" {
		t.Fatalf("volgorde %v, stappen op server-01 %+v", order, sp.Plan.Nodes[0].Steps)
	}
	if s := apply(map[string]any{"servers": 3}, sp.Hash, e.srv.URL, &ap); s != http.StatusAccepted || ap.Job == nil ||
		ap.Job.Title != "2 nodes toevoegen aan Leren" {
		t.Fatalf("servers toepassen: %d %+v", s, ap)
	}
	j = waitJob(t, c, ap.Job.ID)
	if want := []string{"VM leren-server-02 maken", "VM leren-server-03 maken", "Agents aanmelden", "Toepassen op leren-server-01 (VIP-eigenaar)",
		"Toepassen op leren-server-02 (nieuw)", "Toepassen op leren-server-03 (nieuw)"}; j.Status != "succeeded" || !slices.Equal(steps(j), want) {
		t.Fatalf("servers erbij: %s %v %s\n%s", j.Status, steps(j), j.Error, logs(j))
	}
	if !strings.Contains(logs(j), "leren-server-01 is de enige node met keepalived") {
		t.Fatalf("log zonder uitleg over het VIP:\n%s", logs(j))
	}
	healthy("zes nodes", 6, 3)
	conf, err := os.ReadFile(filepath.Join(l.f.host("leren-server-01").Root, "etc/keepalived/keepalived.conf"))
	if err != nil {
		t.Fatal(err)
	}
	peers := regexp.MustCompile(`(?s)unicast_peer \{\s*([^}]*?)\s*\}`).FindStringSubmatch(string(conf))
	if peers == nil || !slices.Equal(strings.Fields(peers[1]), []string{"10.0.40.15", "10.0.40.16"}) {
		t.Fatalf("keepalived.conf op server-01:\n%s", conf)
	}
	if l.f.group.Owner("10.0.40.100") != l.f.host("leren-server-01") {
		t.Fatal("het VIP staat niet meer op server-01")
	}
	scfg, _ := os.ReadFile(filepath.Join(l.f.host("leren-server-03").Root, "etc/rancher/k3s/config.yaml"))
	if !strings.Contains(string(scfg), "server: https://10.0.40.100:6443") || strings.Contains(string(scfg), "cluster-init") {
		t.Fatalf("config van server-03:\n%s", scfg)
	}

	// Een server weghalen kan niet, en ook niet minder servers via de spec.
	kbase := "/api/v1/kubernetes/clusters/" + clusterID
	remove := func(in map[string]any, out any) int {
		t.Helper()
		return c.do("POST", kbase+"/remove-node", in, out)
	}
	if s := remove(map[string]any{"node": "leren-server-02", "preview": true}, &er); s != 409 || !strings.Contains(er.Message, "etcd") {
		t.Fatalf("server weghalen: %d %+v", s, er)
	}
	if s := plan(map[string]any{"servers": 1}, &er); s != 400 || !strings.Contains(er.Message, "minder servers kan niet") {
		t.Fatalf("minder servers: %d %+v", s, er)
	}

	// Worker-01 weghalen: zijn pod gaat eerst weg, dan de VM, de node in
	// Kubernetes en in ClusterForge, met een revisie met twee workers.
	l.fake.Add(webPod("web-5d9c-a1b2c", "leren-worker-01"))
	var vmid int
	if err := e.pool.QueryRow(ctx, "SELECT pve_vmid FROM nodes WHERE hostname = 'leren-worker-01'").Scan(&vmid); err != nil {
		t.Fatal(err)
	}
	l.pve.OnPower(func(id int, status string) {
		if id == vmid && status == "stopped" {
			l.f.halt("leren-worker-01")
		}
	})
	var rp removeNodePlan
	if s := remove(map[string]any{"node": "leren-worker-01", "preview": true}, &rp); s != 200 || rp.Hostname != "leren-worker-01" || !rp.Ready ||
		rp.Vmid != vmid || rp.Host == "" || rp.Workers != 2 || !slices.Contains(podNames(rp.Drain.Evict), "leren/web-5d9c-a1b2c") {
		t.Fatalf("plan weghalen: %d %+v", s, rp)
	}
	var rj job
	if s := remove(map[string]any{"node": "leren-worker-01"}, &rj); s != http.StatusAccepted || rj.Title != "Worker weghalen: leren-worker-01" {
		t.Fatalf("weghalen: %d %+v", s, rj)
	}
	rj = waitJob(t, c, rj.ID)
	if want := []string{"Leegmaken", "VM uitzetten", "Node uit Kubernetes halen", "VM verwijderen", "Uit ClusterForge halen"}; rj.Status != "succeeded" ||
		!slices.Equal(steps(rj), want) {
		t.Fatalf("weghalen: %s %v %s\n%s", rj.Status, steps(rj), rj.Error, logs(rj))
	}
	for _, w := range []string{"leren/web-5d9c-a1b2c", "VM " + strconv.Itoa(vmid) + " staat uit", "VM " + strconv.Itoa(vmid) + " is verwijderd",
		"revisie 4 van de specificatie heeft een worker minder"} {
		if !strings.Contains(logs(rj), w) {
			t.Errorf("log mist %q:\n%s", w, logs(rj))
		}
	}
	if _, ok := l.pve.Guest(vmid); ok {
		t.Fatal("de VM bestaat nog in Proxmox")
	}
	if l.fake.Get("v1", "Node", "", "leren-worker-01") != nil || l.fake.Get("v1", "Pod", "leren", "web-5d9c-a1b2c") != nil {
		t.Fatal("de node of zijn pod staat nog in Kubernetes")
	}
	healthy("vijf nodes", 5, 4)
	if slices.ContainsFunc(cl.Nodes, func(n node) bool { return n.Hostname == "leren-worker-01" }) {
		t.Fatal("worker-01 staat nog in het cluster")
	}
	c.do("GET", base+"/spec/edit", nil, &form)
	for _, p := range form.Params {
		if p.Param.Name == "workers" && p.Value != float64(2) {
			t.Fatalf("workers na weghalen: %v", p.Value)
		}
	}
	if got := revisions(); len(got) != 4 || got[0] != "ui" {
		t.Fatalf("revisies: %v", got)
	}
	auditHas(t, c, "kube.node_removed", "Worker leren-worker-01 weggehaald: leeggemaakt, uit Kubernetes en VM "+strconv.Itoa(vmid)+
		" verwijderd; revisie 4 van de specificatie")

	// Een VM die in Proxmox anders heet, raakt ClusterForge niet aan.
	var vm2 int
	if err := e.pool.QueryRow(ctx, "SELECT pve_vmid FROM nodes WHERE hostname = 'leren-worker-02'").Scan(&vm2); err != nil {
		t.Fatal(err)
	}
	l.pve.RenameGuest(vm2, "database")
	if s := remove(map[string]any{"node": "leren-worker-02"}, &er); s != 409 || !strings.Contains(er.Message, `heet "database" in Proxmox`) {
		t.Fatalf("hernoemde VM: %d %+v", s, er)
	}
	l.pve.RenameGuest(vm2, "leren-worker-02")

	// Met protection in Proxmox kan het niet. Gaat protection aan terwijl de
	// taak wacht, dan gaat de VM uit en de node uit Kubernetes, maar de VM
	// blijft. Zonder protection lukt opnieuw proberen.
	l.pve.SetGuestConfig(vm2, "protection", "1")
	if s := remove(map[string]any{"node": "leren-worker-02"}, &er); s != 409 || !strings.Contains(er.Message, "protection") {
		t.Fatalf("protection: %d %+v", s, er)
	}
	l.pve.SetGuestConfig(vm2, "protection", "0")
	e.stopRunner()
	if s := remove(map[string]any{"node": "leren-worker-02"}, &rj); s != http.StatusAccepted {
		t.Fatalf("worker-02 weghalen: %d", s)
	}
	l.pve.SetGuestConfig(vm2, "protection", "1")
	e.startRunner()
	rj = waitJob(t, c, rj.ID)
	if g, ok := l.pve.Guest(vm2); rj.Status != "failed" || !strings.Contains(rj.Error, "protection") || !ok || g.Status != "stopped" {
		t.Fatalf("protection: %s %s, VM %v %s", rj.Status, rj.Error, ok, g.Status)
	}
	if l.fake.Get("v1", "Node", "", "leren-worker-02") != nil {
		t.Fatal("worker-02 staat nog in Kubernetes")
	}
	l.pve.SetGuestConfig(vm2, "protection", "0")
	if s := c.do("POST", "/api/v1/jobs/"+rj.ID+"/retry", nil, nil); s != http.StatusAccepted {
		t.Fatalf("opnieuw: %d", s)
	}
	if rj = waitJob(t, c, rj.ID); rj.Status != "succeeded" {
		t.Fatalf("na protection: %s %s\n%s", rj.Status, rj.Error, logs(rj))
	}
	if _, ok := l.pve.Guest(vm2); ok {
		t.Fatal("de VM van worker-02 bestaat nog")
	}
	healthy("vier nodes", 4, 5)

	// Een cluster dat Git volgt: het formulier zegt waarom het niet kan,
	// en een worker weghalen ook.
	gitLinked(t, e, c, clusterID)
	c.do("GET", base+"/spec/edit", nil, &form)
	if !strings.Contains(form.Blocked, "cluster.yaml is de bron") {
		t.Fatalf("met Git: %q", form.Blocked)
	}
	if s := plan(map[string]any{"workers": 3}, &er); s != 409 || er.Code != "git_managed" {
		t.Fatalf("plan met Git: %d %+v", s, er)
	}
	if s := remove(map[string]any{"node": "leren-worker-03", "preview": true}, &er); s != 409 || !strings.Contains(er.Message, "volgt Git") {
		t.Fatalf("weghalen met Git: %d %+v", s, er)
	}
}
