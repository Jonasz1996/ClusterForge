package httpapi

import (
	"context"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Jonasz1996/clusterforge/internal/agent"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

type managedJobView struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Describe   string `json:"describe"`
	OnCalendar string `json:"on_calendar"`
	RunOn      string `json:"run_on"`
	Enabled    bool   `json:"enabled"`
	Revision   int    `json:"revision"`
	NextRun    *string
	Nodes      []struct {
		Hostname string `json:"hostname"`
		Applied  bool   `json:"applied"`
		HasVIP   bool   `json:"has_vip"`
		Error    string `json:"error"`
		LastRun  *struct {
			ID     string `json:"id"`
			Result string `json:"result"`
			Reason string `json:"reason"`
		} `json:"last_run"`
	} `json:"nodes"`
}

type cronRunView struct {
	ID           string `json:"id"`
	Hostname     string `json:"hostname"`
	Result       string `json:"result"`
	Reason       string `json:"reason"`
	ExitCode     *int   `json:"exit_code"`
	Output       string `json:"output"`
	OutputHidden bool   `json:"output_hidden"`
}

// managedNode speelt systemd na voor beheerde jobs: systemctl start van
// cf-cron@<id>.service draait cf-agent cron-run zoals de echte service.
type managedNode struct {
	mu    sync.Mutex
	root  string
	addrs *addrs
	calls []string
	wg    sync.WaitGroup
}

func (h *managedNode) runner() *agent.CronRunner {
	return &agent.CronRunner{Root: h.root, Addresses: h.addrs.get, Stderr: os.Stderr}
}

func (h *managedNode) exec(_ context.Context, name string, args ...string) ([]byte, error) {
	h.mu.Lock()
	h.calls = append(h.calls, name+" "+strings.Join(args, " "))
	h.mu.Unlock()
	if name == "systemctl" && len(args) == 3 && args[0] == "start" && strings.HasPrefix(args[2], "cf-cron@") {
		id := strings.TrimSuffix(strings.TrimPrefix(args[2], "cf-cron@"), ".service")
		h.wg.Go(func() { h.runner().Main(context.Background(), id) })
	}
	return nil, nil
}

// TestManagedCronJobs is de acceptatie van mijlpaal 3 van fase 6: een job
// op het VIP van Nextcloud staat als timer op nc1 tot nc3, draait alleen op
// de node met het VIP en verhuist mee; Nu uitvoeren en de historie werken.
func TestManagedCronJobs(t *testing.T) {
	e, c := adminClient(t)
	ctx := context.Background()
	me, _ := user.Current()

	var nc cluster
	if s := c.do("POST", "/api/v1/clusters", map[string]any{"slug": "nextcloud", "name": "Nextcloud", "type": "keepalived", "environment": "test"}, &nc); s != http.StatusCreated {
		t.Fatalf("cluster: %d", s)
	}
	if s := c.do("POST", "/api/v1/clusters/"+nc.ID+"/vips", map[string]any{"address": "10.0.20.185"}, nil); s != http.StatusCreated {
		t.Fatalf("vip: %d", s)
	}
	hosts, ids := map[string]*managedNode{}, map[string]string{}
	for i, name := range []string{"nc1", "nc2", "nc3"} {
		var n node
		if s := c.do("POST", "/api/v1/nodes", map[string]any{"hostname": name, "cluster_id": nc.ID}, &n); s != http.StatusCreated {
			t.Fatalf("node %s: %d", name, s)
		}
		ids[name] = n.ID
		a := &addrs{}
		a.set("192.0.2." + string(rune('1'+i)))
		if name == "nc1" {
			a.set("192.0.2.1", "10.0.20.185")
		}
		h := &managedNode{addrs: a}
		hosts[name] = h
		startAgentWith(t, e, c, n.ID, strings.Repeat(string(rune('7'+i)), 32), a, func(ag *agent.Agent, root string) {
			h.root = root
			ag.Root, ag.Exec = root, h.exec
			ag.CronRunsInterval = 100 * time.Millisecond
		})
		t.Cleanup(h.wg.Wait)
	}
	eventually(t, "VIP op nc1", func() bool {
		var d statusView
		c.do("GET", "/api/v1/clusters/"+nc.ID, nil, &d)
		return len(d.Vips) == 1 && d.Vips[0].OwnerHostname != nil && *d.Vips[0].OwnerHostname == "nc1"
	})

	// Een fout in het formulier zegt welk veld.
	var bad struct {
		Field string `json:"field"`
	}
	in := map[string]any{"cluster_id": nc.ID, "name": "opruimen", "schedule": "0 1 1 * 1", "run_on": "vip", "user": me.Username,
		"command": "echo hallo", "timeout_seconds": 600, "enabled": true}
	if s := c.do("POST", "/api/v1/cron-jobs", in, &bad); s != http.StatusBadRequest || bad.Field != "schedule" {
		t.Errorf("dag en weekdag samen: %d %+v", s, bad)
	}
	var sched struct {
		Describe   string   `json:"describe"`
		OnCalendar string   `json:"on_calendar"`
		Next       []string `json:"next"`
	}
	if s := c.do("GET", "/api/v1/cron-jobs/schedule?schedule=0+*/6+*+*+*", nil, &sched); s != http.StatusOK || sched.Describe != "elke 6 uur, op minuut 0" ||
		sched.OnCalendar != "*-*-* 00,06,12,18:00:00" || len(sched.Next) != 5 {
		t.Errorf("schema: %d %+v", s, sched)
	}

	// De job komt op alle drie de nodes.
	in["schedule"] = "0 */6 * * *"
	in["command"] = "echo opgeruimd op $(hostname)"
	var job managedJobView
	if s := c.do("POST", "/api/v1/cron-jobs", in, &job); s != http.StatusCreated {
		t.Fatalf("maken: %d", s)
	}
	if job.Describe != "elke 6 uur, op minuut 0" || len(job.Nodes) != 3 {
		t.Fatalf("job: %+v", job)
	}
	for _, n := range job.Nodes {
		if !n.Applied || n.HasVIP != (n.Hostname == "nc1") {
			t.Errorf("node %+v", n)
		}
	}
	for name, h := range hosts {
		timer, err := os.ReadFile(filepath.Join(h.root, "etc/systemd/system/cf-cron-"+job.ID+".timer"))
		if err != nil || !strings.Contains(string(timer), "OnCalendar=*-*-* 00,06,12,18:00:00") {
			t.Errorf("%s: timer %q %v", name, timer, err)
		}
	}
	if s := c.do("POST", "/api/v1/cron-jobs", in, &bad); s != http.StatusBadRequest || bad.Field != "name" {
		t.Errorf("dubbele naam: %d %+v", s, bad)
	}

	// Nu uitvoeren draait op nc1, de node met het VIP.
	var started struct {
		Nodes []string `json:"nodes"`
	}
	if s := c.do("POST", "/api/v1/cron-jobs/"+job.ID+"/run", nil, &started); s != http.StatusAccepted || strings.Join(started.Nodes, ",") != "nc1" {
		t.Fatalf("nu uitvoeren: %d %v", s, started)
	}
	var detail struct {
		Job  managedJobView `json:"job"`
		Runs []cronRunView  `json:"runs"`
	}
	eventually(t, "de run van nc1", func() bool {
		c.do("GET", "/api/v1/cron-jobs/"+job.ID, nil, &detail)
		return len(detail.Runs) == 1 && detail.Runs[0].Result == "ok"
	})
	if r := detail.Runs[0]; r.Hostname != "nc1" || r.Reason != "manual" || r.ExitCode == nil || *r.ExitCode != 0 {
		t.Errorf("run: %+v", r)
	}
	var run cronRunView
	if s := c.do("GET", "/api/v1/cron-runs/"+detail.Runs[0].ID, nil, &run); s != http.StatusOK || !strings.HasPrefix(run.Output, "opgeruimd op ") {
		t.Errorf("uitvoer: %d %+v", s, run)
	}

	// Het VIP verhuist naar nc2: de timer draait op elke node, maar alleen
	// nc2 voert de job uit.
	hosts["nc1"].addrs.set("192.0.2.1")
	hosts["nc2"].addrs.set("192.0.2.2", "10.0.20.185")
	for _, h := range hosts {
		h.runner().Main(ctx, job.ID)
	}
	eventually(t, "de run van nc2", func() bool {
		c.do("GET", "/api/v1/cron-jobs/"+job.ID, nil, &detail)
		return len(detail.Runs) == 2
	})
	if r := detail.Runs[0]; r.Hostname != "nc2" || r.Reason != "schedule" || r.Result != "ok" {
		t.Errorf("run na de verhuizing: %+v", r)
	}

	// Een mislukte run: event, en de historie zegt het.
	clusterID := in["cluster_id"]
	delete(in, "cluster_id")
	in["command"] = "echo kapot; exit 4"
	if s := c.do("PUT", "/api/v1/cron-jobs/"+job.ID, in, &job); s != http.StatusOK || job.Revision != 2 {
		t.Fatalf("wijzigen: %d %+v", s, job)
	}
	hosts["nc2"].runner().Main(ctx, job.ID)
	eventually(t, "de mislukte run", func() bool {
		c.do("GET", "/api/v1/cron-jobs/"+job.ID, nil, &detail)
		return len(detail.Runs) == 3 && detail.Runs[0].Result == "failed"
	})
	var payload string
	if err := e.pool.QueryRow(ctx, "SELECT payload::text FROM events WHERE action = 'cron_job.run_failed' AND subject_id = $1", job.ID).Scan(&payload); err != nil ||
		!strings.Contains(payload, "nc2") {
		t.Errorf("event: %s %v", payload, err)
	}
	if err := e.pool.QueryRow(ctx, "SELECT payload::text FROM events WHERE action = 'cron_job.updated' AND subject_id = $1", job.ID).Scan(&payload); err != nil ||
		!strings.Contains(payload, "exit 4") {
		t.Errorf("wijziging: %s %v", payload, err)
	}

	// Een kijker ziet de jobs en runs, maar niet de uitvoer van een job als
	// root.
	e.createUser("kijker", "een-lang-wachtwoord", store.UserRoleViewer)
	viewer := e.client()
	if s, _, _ := viewer.login("kijker", "een-lang-wachtwoord", ""); s != http.StatusOK {
		t.Fatalf("login kijker: %d", s)
	}
	if _, err := e.pool.Exec(ctx, "UPDATE cron_jobs SET user_name = 'root' WHERE id = $1", job.ID); err != nil {
		t.Fatal(err)
	}
	if s := viewer.do("GET", "/api/v1/cron-runs/"+detail.Runs[0].ID, nil, &run); s != http.StatusOK || !run.OutputHidden || run.Output != "" {
		t.Errorf("kijker: %d %+v", s, run)
	}
	if s := viewer.do("POST", "/api/v1/cron-jobs/"+job.ID+"/run", nil, nil); s != http.StatusForbidden {
		t.Errorf("kijker voert uit: %d", s)
	}

	// Uitzetten en weghalen: de timer gaat weg van de nodes. De gebruiker
	// gaat eerst terug, anders telt de wijziging als meer dan uitzetten
	// (in CI draait de test niet als root).
	if _, err := e.pool.Exec(ctx, "UPDATE cron_jobs SET user_name = $2 WHERE id = $1", job.ID, me.Username); err != nil {
		t.Fatal(err)
	}
	in["enabled"], in["user"] = false, me.Username
	if s := c.do("PUT", "/api/v1/cron-jobs/"+job.ID, in, &job); s != http.StatusOK || job.Enabled {
		t.Fatalf("uitzetten: %d", s)
	}
	h := hosts["nc3"]
	h.mu.Lock()
	calls := strings.Join(h.calls, "\n")
	h.mu.Unlock()
	if !strings.Contains(calls, "systemctl disable --now cf-cron-"+job.ID+".timer") {
		t.Errorf("uitzetten op nc3:\n%s", calls)
	}
	if s := c.do("DELETE", "/api/v1/cron-jobs/"+job.ID, nil, nil); s != http.StatusNoContent {
		t.Fatalf("weghalen: %d", s)
	}
	for name, h := range hosts {
		if _, err := os.Stat(filepath.Join(h.root, "etc/systemd/system/cf-cron-"+job.ID+".timer")); !os.IsNotExist(err) {
			t.Errorf("%s: timer staat er nog: %v", name, err)
		}
	}
	var n int
	if err := e.pool.QueryRow(ctx, "SELECT count(*) FROM events WHERE action IN ('cron_job.created', 'cron_job.disabled', 'cron_job.deleted', 'cron_job.run_requested')").Scan(&n); err != nil || n != 4 {
		t.Errorf("%d events: %v", n, err)
	}

	// Een agent met protocol 17 krijgt een melding.
	if _, err := e.pool.Exec(ctx, "UPDATE agents SET protocol_version = 17 WHERE node_id = $1", ids["nc3"]); err != nil {
		t.Fatal(err)
	}
	in["enabled"], in["name"], in["cluster_id"] = true, "tweede", clusterID
	if s := c.do("POST", "/api/v1/cron-jobs", in, &job); s != http.StatusCreated {
		t.Fatalf("tweede job: %d", s)
	}
	for _, n := range job.Nodes {
		if n.Hostname == "nc3" && (n.Applied || !strings.Contains(n.Error, "protocol 18")) {
			t.Errorf("nc3 met een oude agent: %+v", n)
		}
	}
}
