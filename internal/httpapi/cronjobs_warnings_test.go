package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Jonasz1996/clusterforge/internal/agent"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

// cronNode speelt cron op een node na: de journal en de vlag -L van cron
// zijn te veranderen terwijl de agent draait.
type cronNode struct {
	mu      sync.Mutex
	root    string
	journal []string
	calls   []string
}

func (h *cronNode) log(lines ...string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.journal = append(h.journal, lines...)
}

func (h *cronNode) run(_ context.Context, name string, args ...string) ([]byte, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	all := strings.Join(args, " ")
	switch {
	case name == "systemctl" && strings.Contains(all, "cron.service") && strings.Contains(all, "show"):
		return []byte("ActiveState=active\nMainPID=812\nExecStart={ path=/usr/sbin/cron ; argv[]=/usr/sbin/cron -f -P $EXTRA_OPTS ; ignore_errors=no }\n"), nil
	case name == "systemctl" && strings.HasPrefix(all, "show"):
		return []byte("ActiveState=inactive\nMainPID=0\n"), nil
	case name == "systemctl" && strings.HasPrefix(all, "list-"):
		return nil, nil
	case name == "systemctl":
		h.calls = append(h.calls, name+" "+all)
		return nil, nil
	case name == "journalctl":
		// Alles sinds de vorige keer, met een cursor.
		out := strings.Join(h.journal, "\n")
		if out != "" {
			out += "\n"
		}
		h.journal = nil
		return []byte(out + "-- cursor: c" + fmt.Sprint(time.Now().UnixNano()) + "\n"), nil
	}
	return nil, fmt.Errorf("%s bestaat niet in deze test", name)
}

func (h *cronNode) setLogging(t *testing.T, on bool) {
	t.Helper()
	cmd := "/usr/sbin/cron\x00-f\x00-P\x00"
	if on {
		cmd += "-L\x0015\x00"
	}
	writeFiles(t, h.root, map[string]string{"proc/812/cmdline": cmd})
}

func writeFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for p, content := range files {
		if err := os.MkdirAll(filepath.Join(root, filepath.Dir(p)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, p), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

type cronJobView struct {
	Command  string `json:"command"`
	Schedule string `json:"schedule"`
	Differs  []struct {
		Hostname string `json:"hostname"`
		Schedule string `json:"schedule"`
	} `json:"differs"`
	Warnings []struct {
		Kind  string `json:"kind"`
		Level string `json:"level"`
		Text  string `json:"text"`
	} `json:"warnings"`
	Nodes []struct {
		Hostname   string `json:"hostname"`
		LastResult string `json:"last_result"`
		LastExit   *int   `json:"last_exit"`
	} `json:"nodes"`
}

// TestCronjobsWarnings is de acceptatie van mijlpaal 2 van fase 6: de oude
// Galera-cron van Jonas naast het onderdeel Database krijgt een rode
// waarschuwing, een job die op een node anders staat toont het verschil,
// een mislukte job geeft een event en een incident tot hij weer lukt, en
// een beheerder zet met één knop de exitcodes van cron aan.
func TestCronjobsWarnings(t *testing.T) {
	e, c := adminClient(t)
	ctx := context.Background()

	var db cluster
	if s := c.do("POST", "/api/v1/clusters", map[string]any{"slug": "db", "name": "DB", "type": "generic", "environment": "prod"}, &db); s != http.StatusCreated {
		t.Fatalf("cluster: %d", s)
	}
	if _, err := e.pool.Exec(ctx, `INSERT INTO cluster_components (cluster_id, engine, spec) VALUES ($1, 'mariadb', '{}')`, db.ID); err != nil {
		t.Fatal(err)
	}
	hosts, ids := map[string]*cronNode{}, map[string]string{}
	for i, name := range []string{"db1", "db2", "db3"} {
		var n node
		if s := c.do("POST", "/api/v1/nodes", map[string]any{"hostname": name, "cluster_id": db.ID}, &n); s != http.StatusCreated {
			t.Fatalf("node %s: %d", name, s)
		}
		ids[name] = n.ID
		h := &cronNode{}
		hosts[name] = h
		startAgentWith(t, e, c, n.ID, strings.Repeat(string(rune('d'+i)), 32), &addrs{}, func(ag *agent.Agent, root string) {
			h.root = root
			ag.Root, ag.Collector.Run, ag.Exec = root, h.run, h.run
			files := map[string]string{"etc/cron.d/galera": "*/5 * * * * root /usr/local/bin/galera_bootstrap.sh\n"}
			if name == "db3" {
				files["etc/cron.d/galera"] = "*/15 * * * * root /usr/local/bin/galera_bootstrap.sh\n"
			}
			if name == "db1" {
				files["var/spool/cron/crontabs/root"] = "0 3 * * * /usr/local/bin/backup.sh\n"
			}
			writeFiles(t, root, files)
			h.setLogging(t, name != "db2")
		})
	}
	ts := time.Now().Add(-time.Hour).Unix()
	hosts["db1"].log(
		fmt.Sprintf("%d.000000 db1 CRON[100]: (root) CMD (/usr/local/bin/backup.sh)", ts),
		fmt.Sprintf("%d.000000 db1 CRON[100]: (CRON) error (grandchild #101 failed with exit status 2)", ts+3),
		fmt.Sprintf("%d.000000 db1 CRON[100]: (root) END (/usr/local/bin/backup.sh)", ts+3),
	)

	jobs := func() map[string]cronJobView {
		var groups list[struct {
			Jobs []cronJobView `json:"jobs"`
		}]
		if s := c.do("GET", "/api/v1/cronjobs", nil, &groups); s != http.StatusOK {
			t.Fatalf("cronjobs: %d", s)
		}
		out := map[string]cronJobView{}
		for _, g := range groups.Items {
			for _, j := range g.Jobs {
				out[j.Command+" "+j.Schedule] = j
			}
		}
		return out
	}
	var got map[string]cronJobView
	eventually(t, "de cronjobs van db1 tot db3", func() bool {
		if err := e.crons.SyncAll(ctx); err != nil {
			t.Fatal(err)
		}
		got = jobs()
		return len(got) == 3
	})

	galera := got["/usr/local/bin/galera_bootstrap.sh */5 * * * *"]
	if len(galera.Warnings) != 1 || galera.Warnings[0].Kind != "galera_bootstrap" || galera.Warnings[0].Level != "danger" {
		t.Errorf("waarschuwing bij de Galera-cron: %+v", galera.Warnings)
	}
	if len(galera.Differs) != 1 || galera.Differs[0].Hostname != "db3" || galera.Differs[0].Schedule != "*/15 * * * *" {
		t.Errorf("verschil met db3: %+v", galera.Differs)
	}
	if other := got["/usr/local/bin/galera_bootstrap.sh */15 * * * *"]; len(other.Differs) != 2 {
		t.Errorf("db3 ziet db1 en db2 niet: %+v", other.Differs)
	}
	backup := got["/usr/local/bin/backup.sh 0 3 * * *"]
	if len(backup.Warnings) != 0 || len(backup.Nodes) != 1 || backup.Nodes[0].LastResult != "failed" || backup.Nodes[0].LastExit == nil || *backup.Nodes[0].LastExit != 2 {
		t.Errorf("mislukte back-up: %+v", backup)
	}

	// De mislukte back-up: een event en een incident op het cluster.
	var payload string
	if err := e.pool.QueryRow(ctx, "SELECT payload::text FROM events WHERE action = 'node.cron_failed' AND subject_id = $1", ids["db1"]).Scan(&payload); err != nil {
		t.Fatalf("event: %v", err)
	}
	if !strings.Contains(payload, "backup.sh") || !strings.Contains(payload, `"exit": 2`) {
		t.Errorf("payload %s", payload)
	}
	var inc incidentView
	eventually(t, "incident van de back-up", func() bool {
		for _, i := range c.incidents("") {
			if slices.ContainsFunc(i.Subjects, func(s incidentSubjectView) bool { return s.Type == "cron" }) {
				inc = i
				return true
			}
		}
		return false
	})
	if inc.Title != "Cronjob /usr/local/bin/backup.sh op db1 mislukt" || inc.Status != "open" {
		t.Errorf("incident: %+v", inc)
	}

	// De volgende nacht lukt hij: weer goed, en het incident sluit.
	hosts["db1"].log(
		fmt.Sprintf("%d.000000 db1 CRON[200]: (root) CMD (/usr/local/bin/backup.sh)", ts+600),
		fmt.Sprintf("%d.000000 db1 CRON[200]: (root) END (/usr/local/bin/backup.sh)", ts+660),
	)
	if err := e.crons.SyncAll(ctx); err != nil {
		t.Fatal(err)
	}
	if r := jobs()["/usr/local/bin/backup.sh 0 3 * * *"].Nodes[0].LastResult; r != "ok" {
		t.Errorf("na een goede run: %q", r)
	}
	var n int
	if err := e.pool.QueryRow(ctx, "SELECT count(*) FROM events WHERE action = 'node.cron_recovered'").Scan(&n); err != nil || n != 1 {
		t.Errorf("%d events cron_recovered: %v", n, err)
	}
	eventually(t, "incident gesloten", func() bool {
		return slices.ContainsFunc(c.incidents("status=resolved"), func(i incidentView) bool { return i.ID == inc.ID })
	})

	// db2 logt geen exitcodes; een kijker mag dat niet aanzetten, een
	// beheerder wel.
	e.createUser("kijker", "een-lang-wachtwoord", store.UserRoleViewer)
	viewer := e.client()
	if s, _, _ := viewer.login("kijker", "een-lang-wachtwoord", ""); s != http.StatusOK {
		t.Fatalf("login kijker: %d", s)
	}
	if s := viewer.do("POST", "/api/v1/nodes/"+ids["db2"]+"/cron-logging", map[string]any{"enabled": true}, nil); s != http.StatusForbidden {
		t.Errorf("kijker: %d", s)
	}
	var out struct {
		Output []string `json:"output"`
	}
	if s := c.do("POST", "/api/v1/nodes/"+ids["db2"]+"/cron-logging", map[string]any{"enabled": true}, &out); s != http.StatusOK {
		t.Fatalf("logging aan: %d", s)
	}
	dropIn, err := os.ReadFile(filepath.Join(hosts["db2"].root, "etc/systemd/system/cron.service.d/50-clusterforge-log.conf"))
	if err != nil || !strings.Contains(string(dropIn), "ExecStart=/usr/sbin/cron -f -P $EXTRA_OPTS -L 15") {
		t.Errorf("drop-in: %q %v", dropIn, err)
	}
	hosts["db2"].mu.Lock()
	calls := slices.Clone(hosts["db2"].calls)
	hosts["db2"].mu.Unlock()
	if !slices.Equal(calls, []string{"systemctl daemon-reload", "systemctl restart cron.service"}) {
		t.Errorf("systemctl: %v", calls)
	}
	if len(out.Output) == 0 {
		t.Error("geen uitvoer")
	}
	if err := e.pool.QueryRow(ctx, "SELECT count(*) FROM events WHERE action = 'node.cron_logging' AND subject_id = $1", ids["db2"]).Scan(&n); err != nil || n != 1 {
		t.Errorf("%d events cron_logging: %v", n, err)
	}

	// Een agent met protocol 16 kent het verzoek nog niet.
	if _, err := e.pool.Exec(ctx, "UPDATE agents SET protocol_version = 16 WHERE node_id = $1", ids["db3"]); err != nil {
		t.Fatal(err)
	}
	if s := c.do("POST", "/api/v1/nodes/"+ids["db3"]+"/cron-logging", map[string]any{"enabled": true}, nil); s != http.StatusConflict {
		t.Errorf("oude agent: %d", s)
	}
}
