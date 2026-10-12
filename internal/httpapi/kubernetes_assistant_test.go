package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/agent"
	"github.com/Jonasz1996/clusterforge/internal/kube/kubefake"
)

// De assistent bij Kubernetes: 'Waarom start deze pod niet?' vindt de reden
// in de events en de log van voor de herstart, 'Ik zit vast' geeft een hint
// zonder het antwoord, en geen tool schrijft in het cluster of geeft een
// geheim terug.
func TestKubernetesAssistant(t *testing.T) {
	e, c := adminClient(t)
	fake := kubeCluster(t)
	ctx := context.Background()

	// Geheimen op vier plekken: het Secret db uit kubeCluster, een
	// env-variabele van de pod met zijn last-applied, een ConfigMap en de
	// log.
	const secret = "Sup3rGeheim"
	pod := kubefake.Pod(kubefake.PodSpec{
		Namespace: "demo", Name: "web-7c9-c", Node: "leren-worker-02", Waiting: "CrashLoopBackOff", Restarts: 7, LastReason: "Error",
		OwnerKind: "ReplicaSet", OwnerName: "web-7c9",
	})
	ctr := pod["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)
	ctr["env"] = []any{
		map[string]any{"name": "DB_PASSWORD", "value": secret},
		map[string]any{"name": "DATABASE_URL", "value": "postgres://web:" + secret + "@db.demo:5432/shop"},
		map[string]any{"name": "LOG_LEVEL", "value": "debug"},
	}
	const podUID = "6f1c2a9e-3b7d-4c55-8e21-9a0d4f7b1c3e"
	pod["metadata"].(map[string]any)["uid"] = podUID
	pod["metadata"].(map[string]any)["annotations"] = map[string]any{
		"kubectl.kubernetes.io/last-applied-configuration": `{"apiVersion":"v1","kind":"Pod","spec":{"containers":[{"env":[{"name":"DB_PASSWORD","value":"` +
			secret + `"}],"name":"main"}]}}` + "\n",
	}
	fake.Add(pod, map[string]any{
		"apiVersion": "v1", "kind": "Event", "metadata": map[string]any{"name": "web-7c9-c.17f", "namespace": "demo"},
		"type": "Warning", "reason": "BackOff", "message": "Back-off restarting failed container main in pod web-7c9-c_demo(" + podUID + ")",
		"count": 12, "lastTimestamp": time.Now().UTC().Format(time.RFC3339),
		"involvedObject": map[string]any{"kind": "Pod", "namespace": "demo", "name": "web-7c9-c", "uid": podUID},
	}, map[string]any{
		"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]any{"name": "web-config", "namespace": "demo"},
		"data": map[string]any{"admin_password": secret, "log_level": "debug"},
	})
	fake.SetLog("demo", "web-7c9-c", "main", true,
		"2026/10/12 09:14:02 config geladen: LOG_LEVEL=debug DB_PASSWORD="+secret,
		"2026/10/12 09:14:02 verbinden met postgres://web:"+secret+"@db.demo:5432/shop",
		"panic: dial tcp 10.43.0.15:5432: connect: connection refused",
	)
	fake.SetLog("demo", "web-7c9-c", "main", false, "2026/10/12 09:15:40 config geladen: LOG_LEVEL=debug DB_PASSWORD="+secret)
	leaked := func(where, s string) {
		t.Helper()
		for _, v := range []string{secret, "Z2VoZWltLXdhY2h0d29vcmQ=", "geheim-wachtwoord"} {
			if strings.Contains(s, v) {
				t.Errorf("%s bevat het geheim %q:\n%s", where, v, s)
			}
		}
	}

	var cl cluster
	c.do("POST", "/api/v1/clusters", map[string]any{"slug": "leren", "name": "Leren", "type": "kubernetes", "environment": "lab"}, &cl)
	c.do("POST", "/api/v1/clusters/"+cl.ID+"/vips", map[string]any{"address": "10.0.40.100"}, nil)
	var server node
	c.do("POST", "/api/v1/nodes", map[string]any{"hostname": "leren-server-01", "cluster_id": cl.ID, "primary_ip": "10.0.40.11"}, &server)
	startAgentWith(t, e, c, server.ID, "a1000000000000000000000000000042", &addrs{}, func(ag *agent.Agent, root string) {
		if err := fake.WriteKubeconfig(root); err != nil {
			t.Fatal(err)
		}
		ag.KubeletHealthz = fake.KubeletURL()
	})
	base := "/api/v1/kubernetes/clusters/" + cl.ID
	eventually(t, "overzicht", func() bool { return c.do("GET", base, nil, nil) == http.StatusOK })
	if err := e.kube.Sync(ctx, uuid.MustParse(cl.ID)); err != nil {
		t.Fatal(err)
	}

	podRef := map[string]any{"group": "", "version": "v1", "resource": "pods", "namespace": "demo", "name": "web-7c9-c"}
	if s, _ := c.streamPost(base+"/explain", podRef); s != http.StatusForbidden {
		t.Fatalf("uitleg met de assistent uit: %d", s)
	}
	if s, _ := c.streamPost(base+"/lessons/verkennen/hint", nil); s != http.StatusForbidden {
		t.Fatalf("hint met de assistent uit: %d", s)
	}
	fc := enableAssistant(t, e, c, false)

	// Elke tool met de invoer van toolInputs, en de ConfigMap en het Secret.
	inputs := toolInputs("Leren", "leren-server-01", uuid.NewString())
	inputs["kube_object"] = append(inputs["kube_object"],
		`{"cluster":"Leren","soort":"configmaps","namespace":"demo","naam":"web-config"}`,
		`{"cluster":"Leren","soort":"Secret","namespace":"demo","naam":"db"}`,
		`{"cluster":"Leren","soort":"nodes","namespace":"","naam":"leren-worker-02"}`,
		`{"cluster":"Leren","soort":"deployments","namespace":"","naam":"web"}`,
		`{"cluster":"Leren","soort":"kasten","namespace":"demo","naam":"web"}`,
	)
	out := e.runTools(t, "admin", inputs)
	for name, want := range map[string][]string{
		"kubernetes": {
			`"versie":"v1.35.9+k3s1"`, `"leren-worker-02"`, `"Kubelet stopped posting node status."`, `"CrashLoopBackOff"`, `"BackOff"`,
			`"opmerking_pods"`, `"name":"web-7c9-a"`,
		},
		"kube_object": {
			`"toestand":`, `"message":"Back-off restarting failed container main in pod web-7c9-c_demo(`, `"last_reason":"Error"`, "DB_PASSWORD\\n          value: [geheim]",
			`"name\":\"DB_PASSWORD\",\"value\":\"[geheim]\"`, "postgres://web:[geheim]@db.demo", "LOG_LEVEL\\n          value: debug",
			"admin_password: [geheim]", "log_level: debug", "wachtwoord: (verborgen, 17 bytes)", `"soort":"Node"`,
			"FOUT deployments hebben een namespace; geef die mee", `FOUT het cluster kent geen soort "kasten"`,
		},
		"kube_logs": {
			`"pod":"demo/web-7c9-c"`, "DB_PASSWORD=[geheim]", "postgres://web:[geheim]@db.demo:5432/shop",
			"connect: connection refused", `"vorige":true`, `"vorige":false`,
		},
	} {
		for _, w := range want {
			if !strings.Contains(out[name], w) {
				t.Errorf("%s mist %s:\n%s", name, w, out[name])
			}
		}
	}
	for name, o := range out {
		leaked(name, o)
	}
	if strings.Contains(out["kube_object"], "managedFields") || strings.Contains(out["kube_object"], `"annotations"`) {
		t.Errorf("kube_object houdt annotaties of managedFields:\n%s", out["kube_object"])
	}

	// Waarom start deze pod niet? Het model leest de vorige log en
	// antwoordt met de regel eruit.
	fc.then(
		func(claudeRequest) (int, string) {
			return 200, sse("claude-opus-5-5", "tool_use", 4000, 100, block{
				id: "toolu_1", name: "kube_logs", input: `{"cluster":"Leren","namespace":"demo","pod":"web-7c9-c","container":"","vorige":true}`,
			})
		},
		func(claudeRequest) (int, string) {
			return 200, sse("claude-opus-5-5", "end_turn", 5000, 200, block{
				text: "De container stopt omdat hij de database niet bereikt: `panic: dial tcp 10.43.0.15:5432: connect: connection refused`.",
			})
		},
	)
	before := len(fc.requests())
	s, evs := c.streamPost(base+"/explain", podRef)
	done := streamOf(evs, "done")
	tools := streamOf(evs, "tool")
	if s != http.StatusOK || len(done) != 1 || len(streamOf(evs, "error")) != 0 || !strings.Contains(streamText(evs), "connection refused") ||
		len(tools) != 1 || tools[0]["label"] != "Vorige logs van demo/web-7c9-c lezen" {
		t.Fatalf("uitleg: %d %+v", s, evs)
	}
	reqs := fc.requests()[before:]
	if len(reqs) != 2 {
		t.Fatalf("%d aanvragen", len(reqs))
	}
	_, prompt, _ := firstPrompt(t, reqs[0])
	for _, w := range []string{
		"Waarom start pod demo/web-7c9-c niet?", "Kubernetes-cluster Leren (lab)", "/kubernetes/object?g=&id=" + cl.ID + "&name=web-7c9-c&ns=demo&r=pods&v=v1",
		"CrashLoopBackOff", "Back-off restarting failed container main", "kube_logs", "DB_PASSWORD\\n          value: [geheim]",
	} {
		if !strings.Contains(prompt, w) {
			t.Errorf("opdracht mist %q:\n%s", w, prompt)
		}
	}
	var second struct {
		Messages []struct {
			Content []struct {
				ToolUseID string `json:"tool_use_id"`
				Content   any    `json:"content"`
			} `json:"content"`
		} `json:"messages"`
	}
	_ = json.Unmarshal([]byte(reqs[1].Raw), &second)
	m := second.Messages
	if len(m) != 3 || len(m[2].Content) != 1 || m[2].Content[0].ToolUseID != "toolu_1" {
		t.Fatalf("ronde 2: %s", reqs[1].Raw)
	}
	if res := fmt.Sprint(m[2].Content[0].Content); !strings.Contains(res, "connect: connection refused") || !strings.Contains(res, "DB_PASSWORD=[geheim]") {
		t.Fatalf("resultaat van kube_logs: %s", res)
	}
	_, conv, turns := c.conversation(done[0]["conversation_id"].(string))
	if conv.SubjectType != "cluster" || len(turns) != 1 || turns[0].Question != "Waarom start pod demo/web-7c9-c niet?" {
		t.Fatalf("gesprek: %+v %+v", conv, turns)
	}

	// Een object dat er niet is, of een cluster dat geen Kubernetes is.
	gone := maps.Clone(podRef)
	gone["name"] = "weg"
	if s, evs := c.streamPost(base+"/explain", gone); s != http.StatusNotFound {
		t.Fatalf("onbekende pod: %d %+v", s, evs)
	}
	var other cluster
	c.do("POST", "/api/v1/clusters", map[string]any{"slug": "web", "name": "Web", "type": "keepalived", "environment": "lab"}, &other)
	if s, _ := c.streamPost("/api/v1/kubernetes/clusters/"+other.ID+"/explain", podRef); s != http.StatusNotFound {
		t.Fatalf("keepalived-cluster: %d", s)
	}

	// Ik zit vast: bij een vraag gaat de hint van de les mee, het antwoord
	// niet.
	check := func(answers map[string]string) kubeLesson {
		t.Helper()
		var l kubeLesson
		if s := c.do("POST", base+"/lessons/verkennen/check", map[string]any{"answers": answers}, &l); s != http.StatusOK {
			t.Fatalf("controleren: %d", s)
		}
		return l
	}
	check(map[string]string{"nodes": "3"})
	fc.then(func(claudeRequest) (int, string) {
		return 200, sse("claude-opus-5-5", "end_turn", 2000, 80, block{text: "Kijk op het tabblad Nodes naar de kolom Rollen."})
	})
	before = len(fc.requests())
	s, evs = c.streamPost(base+"/lessons/verkennen/hint", nil)
	done = streamOf(evs, "done")
	if s != http.StatusOK || len(done) != 1 || streamText(evs) != "Kijk op het tabblad Nodes naar de kolom Rollen." {
		t.Fatalf("hint: %d %+v", s, evs)
	}
	_, prompt, _ = firstPrompt(t, fc.requests()[before])
	for _, w := range []string{
		`les 1, "Je cluster verkennen"`, `de opdracht "Zoek een server"`, "Geef de naam van een server.", "Zoek een node met de rol control-plane.",
		"Kijk op het tabblad Nodes in de kolom Rollen.", "zonder het antwoord voor te zeggen",
	} {
		if !strings.Contains(prompt, w) {
			t.Errorf("hint-opdracht mist %q:\n%s", w, prompt)
		}
	}
	if strings.Contains(prompt, "leren-server-01") || strings.Contains(prompt, "Juist") {
		t.Errorf("hint-opdracht verklapt het antwoord:\n%s", prompt)
	}
	_, conv, turns = c.conversation(done[0]["conversation_id"].(string))
	if conv.SubjectType != "cluster" || len(turns) != 1 || turns[0].Question != "Ik zit vast bij les 1, Je cluster verkennen: Zoek een server" {
		t.Fatalf("gesprek hint: %+v %+v", conv, turns)
	}

	// Een les die af is, die niet kan op dit cluster, of die niet bestaat.
	check(map[string]string{"server": "leren-server-01"})
	check(map[string]string{"dns": "kube-system"})
	if l := check(map[string]string{"traefik": "leren-server-01"}); l.Done != 4 {
		t.Fatalf("les 1 niet af: %v %+v", l.states(), l.current())
	}
	for path, want := range map[string]struct {
		status int
		code   string
	}{
		"verkennen":       {http.StatusConflict, "done"},
		"server-valt-uit": {http.StatusConflict, "unavailable"},
		"bestaat-niet":    {http.StatusNotFound, "not_found"},
	} {
		s, evs := c.streamPost(base+"/lessons/"+path+"/hint", nil)
		if s != want.status || evs[0].Data["code"] != want.code {
			t.Errorf("hint %s: %d %+v", path, s, evs)
		}
	}

	// Geen enkele aanvraag aan het model bevat een geheim, en niets schreef
	// in het cluster.
	for i, r := range fc.requests() {
		leaked(fmt.Sprintf("aanvraag %d", i), r.Raw)
	}
	for _, r := range fake.Requests() {
		if !strings.HasPrefix(r, "GET ") {
			t.Errorf("de assistent schreef in het cluster: %s", r)
		}
	}
}
