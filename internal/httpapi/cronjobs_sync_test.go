package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/agent"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// TestMain laat het testprogramma ook sync-receive spelen: het nep-ssh van
// TestCronSync start het met CF_TEST_SYNC_RECEIVE, zoals sshd de forced
// command van authorized_keys start.
func TestMain(m *testing.M) {
	if root := os.Getenv("CF_TEST_SYNC_RECEIVE"); root != "" {
		addrs := strings.Split(os.Getenv("CF_TEST_ADDRS"), ",")
		os.Exit((&agent.SyncReceiver{Root: root, Addresses: func() []string { return addrs }}).Main(os.Getenv("CF_TEST_FROM"), os.Getenv("SSH_ORIGINAL_COMMAND")))
	}
	os.Exit(m.Run())
}

// syncTarget is een doel voor fakeSyncSSH: zijn adres, de root van zijn
// agent en de adressen op de node.
type syncTarget struct {
	address, root string
	addrs         []string
}

// fakeSyncSSH schrijft een ssh voor rsync -e dat sync-receive van het
// testprogramma start op de root van het doel met dat adres.
func fakeSyncSSH(t *testing.T, from string, targets ...syncTarget) string {
	t.Helper()
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	var cases strings.Builder
	for _, tg := range targets {
		fmt.Fprintf(&cases, "%s) root=%q; addrs=%q;;\n", tg.address, tg.root, strings.Join(tg.addrs, ","))
	}
	p := filepath.Join(t.TempDir(), "ssh")
	script := fmt.Sprintf(`#!/bin/sh
[ "$1" = "-l" ] && shift 2
host=$1
shift
case $host in
%s*) echo "onbekend doel $host" >&2; exit 255;;
esac
SSH_ORIGINAL_COMMAND="$*" CF_TEST_SYNC_RECEIVE="$root" CF_TEST_FROM=%q CF_TEST_ADDRS="$addrs" exec %q -test.run='^$'
`, cases.String(), from, bin)
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// syncNode is een managedNode met ssh en sshd zoals op een Debian-node.
type syncNode struct{ managedNode }

func (h *syncNode) exec(ctx context.Context, name string, args ...string) ([]byte, error) {
	switch {
	case name == "systemctl" && len(args) == 2 && args[0] == "is-active" && args[1] == "ssh":
		return []byte("active\n"), nil
	case name == "sshd" && len(args) == 1 && args[0] == "-T":
		return []byte("port 22\npermitrootlogin prohibit-password\n"), nil
	}
	return h.managedNode.exec(ctx, name, args...)
}

type planNodeView struct {
	Hostname string `json:"hostname"`
	Role     string `json:"role"`
	OK       bool   `json:"ok"`
	Checks   []struct {
		Name string `json:"name"`
		OK   bool   `json:"ok"`
		Text string `json:"text"`
	} `json:"checks"`
}

type syncRunView struct {
	cronRunView
	Sync []struct {
		Hostname string `json:"hostname"`
		Result   string `json:"result"`
		Files    int    `json:"files"`
		Bytes    int64  `json:"bytes"`
		Error    string `json:"error"`
	} `json:"sync"`
}

// TestCronSync is de acceptatie van mijlpaal 6 van fase 6: een sync van de
// data van Nextcloud naar een backupcluster om de vier uur, met het plan,
// een proefrun en een echte rsync via sync-receive.
func TestCronSync(t *testing.T) {
	if _, err := exec.LookPath("rsync"); err != nil {
		t.Skip("rsync staat niet op deze machine")
	}
	e, c := adminClient(t)
	ctx := context.Background()

	var nc, bk cluster
	if s := c.do("POST", "/api/v1/clusters", map[string]any{"slug": "nextcloud", "name": "Nextcloud", "type": "keepalived", "environment": "test"}, &nc); s != http.StatusCreated {
		t.Fatalf("cluster: %d", s)
	}
	if s := c.do("POST", "/api/v1/clusters", map[string]any{"slug": "backup", "name": "Backup", "type": "keepalived", "environment": "test"}, &bk); s != http.StatusCreated {
		t.Fatalf("cluster: %d", s)
	}
	if s := c.do("POST", "/api/v1/clusters/"+nc.ID+"/vips", map[string]any{"address": "10.0.20.185"}, nil); s != http.StatusCreated {
		t.Fatalf("vip: %d", s)
	}
	hosts, ids := map[string]*syncNode{}, map[string]string{}
	for i, spec := range []struct{ name, cluster, ip string }{{"bk1", bk.ID, "192.0.2.11"}, {"nc1", nc.ID, "192.0.2.1"}} {
		var n node
		if s := c.do("POST", "/api/v1/nodes", map[string]any{"hostname": spec.name, "cluster_id": spec.cluster}, &n); s != http.StatusCreated {
			t.Fatalf("node %s: %d", spec.name, s)
		}
		ids[spec.name] = n.ID
		if _, err := e.pool.Exec(ctx, "UPDATE nodes SET primary_ip = $2 WHERE id = $1", n.ID, spec.ip); err != nil {
			t.Fatal(err)
		}
		a := &addrs{}
		a.set(spec.ip)
		if spec.name == "nc1" {
			a.set(spec.ip, "10.0.20.185")
		}
		h := &syncNode{managedNode{addrs: a}}
		hosts[spec.name] = h
		startAgentWith(t, e, c, n.ID, strings.Repeat(string(rune('a'+i)), 32), a, func(ag *agent.Agent, root string) {
			h.root = root
			ag.Root, ag.Exec = root, h.exec
			ag.CronRunsInterval = 100 * time.Millisecond
			if spec.name == "nc1" {
				bk1 := hosts["bk1"]
				h.ssh = fakeSyncSSH(t, n.ID, syncTarget{"192.0.2.11", bk1.root, []string{"192.0.2.11"}})
				ag.SyncSSH = h.ssh
			}
			writeFile(t, filepath.Join(root, "etc/ssh/ssh_host_ed25519_key.pub"), "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOTe root@"+spec.name+"\n", 0o644)
		})
		t.Cleanup(h.wg.Wait)
	}
	bk1 := hosts["bk1"]
	eventually(t, "VIP op nc1", func() bool {
		var d statusView
		c.do("GET", "/api/v1/clusters/"+nc.ID, nil, &d)
		return len(d.Vips) == 1 && d.Vips[0].OwnerHostname != nil && *d.Vips[0].OwnerHostname == "nc1"
	})
	eventually(t, "agent op bk1", func() bool {
		var d statusView
		c.do("GET", "/api/v1/clusters/"+bk.ID, nil, &d)
		return len(d.Nodes) == 1 && d.Nodes[0].Status == "healthy"
	})

	src, dst := t.TempDir()+"/data/", t.TempDir()+"/kopie/"
	writeFile(t, src+"a.txt", "hallo", 0o644)
	writeFile(t, src+"map/b.txt", "wereld", 0o644)
	writeFile(t, src+"c.part", "half", 0o644)

	// Een fout in het formulier: alles op elke node kan niet bij een sync.
	var bad struct {
		Field string `json:"field"`
	}
	in := map[string]any{"cluster_id": nc.ID, "name": "data naar backup", "schedule": "0 */4 * * *", "run_on": "all", "kind": "sync",
		"sync":            map[string]any{"source": strings.TrimSuffix(src, "/"), "target": dst, "to_cluster_id": bk.ID, "exclude": []string{"*.part"}},
		"timeout_seconds": 600, "enabled": true}
	if s := c.do("POST", "/api/v1/cron-jobs", in, &bad); s != http.StatusBadRequest || bad.Field != "run_on" {
		t.Errorf("sync op alle nodes: %d %+v", s, bad)
	}
	in["run_on"] = "vip"
	var job struct {
		managedJobView
		Kind string `json:"kind"`
		User string `json:"user"`
		Sync struct {
			Source        string `json:"source"`
			ToClusterName string `json:"to_cluster_name"`
		} `json:"sync"`
	}
	if s := c.do("POST", "/api/v1/cron-jobs", in, &job); s != http.StatusCreated {
		t.Fatalf("maken: %d", s)
	}
	if job.Kind != "sync" || job.User != "root" || job.Sync.Source != src || job.Sync.ToClusterName != "Backup" {
		t.Errorf("job: %+v", job)
	}

	// Het doel laat alleen de sleutel van nc1 toe, vanaf zijn adressen.
	eventually(t, "authorized_keys op bk1", func() bool {
		auth, _ := os.ReadFile(filepath.Join(bk1.root, protocol.SyncAuthorizedKeys))
		return strings.Contains(string(auth), `restrict,from="10.0.20.185,192.0.2.1",command="/usr/local/bin/cf-agent sync-receive --from `+ids["nc1"]+`"`)
	})

	// Het plan: nc1 is bron, bk1 doel, en alles klopt.
	var plan struct {
		Nodes []planNodeView `json:"nodes"`
	}
	if s := c.do("GET", "/api/v1/cron-jobs/"+job.ID+"/plan", nil, &plan); s != http.StatusOK || len(plan.Nodes) != 2 {
		t.Fatalf("plan: %d %+v", s, plan)
	}
	for _, n := range plan.Nodes {
		want := map[string]string{"nc1": "bron", "bk1": "doel"}[n.Hostname]
		if n.Role != want || !n.OK {
			t.Errorf("plan van %s: %+v", n.Hostname, n)
		}
	}

	// Een proefrun kopieert niets.
	var dry struct {
		Source string   `json:"source"`
		Lines  []string `json:"lines"`
	}
	if s := c.do("POST", "/api/v1/cron-jobs/"+job.ID+"/dry-run", map[string]any{"target": ids["bk1"]}, &dry); s != http.StatusOK ||
		dry.Source != "nc1" || !slices.ContainsFunc(dry.Lines, func(l string) bool { return strings.HasSuffix(l, " a.txt") }) {
		t.Fatalf("proefrun: %d %+v", s, dry)
	}
	if _, err := os.Stat(dst + "a.txt"); !os.IsNotExist(err) {
		t.Error("de proefrun kopieerde")
	}

	// Nu uitvoeren: nc1 heeft het VIP en kopieert naar bk1, zonder .part.
	if s := c.do("POST", "/api/v1/cron-jobs/"+job.ID+"/run", nil, nil); s != http.StatusAccepted {
		t.Fatalf("nu uitvoeren: %d", s)
	}
	var detail struct {
		Runs []syncRunView `json:"runs"`
	}
	eventually(t, "de sync", func() bool {
		c.do("GET", "/api/v1/cron-jobs/"+job.ID, nil, &detail)
		return len(detail.Runs) == 1 && detail.Runs[0].Result != "running"
	})
	r := detail.Runs[0]
	if r.Result != "ok" || r.Hostname != "nc1" || len(r.Sync) != 1 || r.Sync[0].Hostname != "bk1" || r.Sync[0].Files != 2 || r.Sync[0].Bytes != 11 {
		t.Fatalf("run: %+v", r)
	}
	if b, _ := os.ReadFile(dst + "map/b.txt"); string(b) != "wereld" {
		t.Errorf("b.txt: %q", b)
	}
	if _, err := os.Stat(dst + "c.part"); !os.IsNotExist(err) {
		t.Error("c.part is meegekopieerd")
	}
	var proef int
	if err := e.pool.QueryRow(ctx, "SELECT count(*) FROM events WHERE action = 'cron_job.dry_run' AND subject_id = $1", job.ID).Scan(&proef); err != nil || proef != 1 {
		t.Errorf("%d proefrun-events: %v", proef, err)
	}

	// De assistent ziet de sync en zijn runs.
	inputs := toolInputs("Nextcloud", "nc1", uuid.NewString())
	inputs["cron_runs"] = []string{`{"job":"data naar backup","cluster":""}`, `{"job":"bestaat niet","cluster":"Nextcloud"}`}
	out := e.runTools(t, "admin", inputs)
	for tool, want := range map[string][]string{
		"cronjobs":  {"data naar backup", `"to_cluster_name":"Backup"`, `"kind":"sync"`},
		"cron_runs": {`"hostname":"bk1"`, `"files":2`, "FOUT geen beheerde cronjob bestaat niet; er zijn: data naar backup"},
	} {
		for _, w := range want {
			if !strings.Contains(out[tool], w) {
				t.Errorf("%s mist %s:\n%s", tool, w, out[tool])
			}
		}
	}

	// Een agent met protocol 20 op het doel krijgt een melding.
	if _, err := e.pool.Exec(ctx, "UPDATE agents SET protocol_version = 20 WHERE node_id = $1", ids["bk1"]); err != nil {
		t.Fatal(err)
	}
	if s := c.do("GET", "/api/v1/cron-jobs/"+job.ID+"/plan", nil, &plan); s != http.StatusOK {
		t.Fatalf("plan: %d", s)
	}
	i := slices.IndexFunc(plan.Nodes, func(n planNodeView) bool {
		return n.Hostname == "bk1"
	})
	if i < 0 || plan.Nodes[i].OK || !strings.Contains(plan.Nodes[i].Checks[0].Text, "agent te oud") {
		t.Errorf("oude agent: %+v", plan.Nodes)
	}
}
