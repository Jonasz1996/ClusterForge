package httpapi

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Jonasz1996/clusterforge/internal/agent"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

type cronGroupView struct {
	ClusterName string `json:"cluster_name"`
	Nodes       []struct {
		Hostname  string `json:"hostname"`
		Error     string `json:"error"`
		ErrorKind string `json:"error_kind"`
	} `json:"nodes"`
	Jobs []struct {
		Key      string  `json:"key"`
		Source   string  `json:"source"`
		File     string  `json:"file"`
		User     string  `json:"user"`
		Schedule string  `json:"schedule"`
		Describe string  `json:"describe"`
		Command  string  `json:"command"`
		NextRun  *string `json:"next_run"`
		Nodes    []struct {
			Hostname string `json:"hostname"`
		} `json:"nodes"`
	} `json:"jobs"`
}

// TestCronjobs is de acceptatie van mijlpaal 1 van fase 6: de cronjobs van
// nc1 tot nc3 staan per cluster in het menu Cronjobs, dezelfde job op meer
// nodes één keer, met het schema in gewone taal en zonder wachtwoord. Een
// gewijzigde job geeft een event, en een te oude agent een melding.
func TestCronjobs(t *testing.T) {
	e, c := adminClient(t)
	ctx := context.Background()

	var nc cluster
	if s := c.do("POST", "/api/v1/clusters", map[string]any{"slug": "nextcloud", "name": "Nextcloud", "type": "generic", "environment": "prod"}, &nc); s != http.StatusCreated {
		t.Fatalf("cluster: %d", s)
	}
	roots, stops, ids := map[string]string{}, map[string]func(){}, map[string]string{}
	for i, name := range []string{"nc1", "nc2", "nc3"} {
		var n node
		if s := c.do("POST", "/api/v1/nodes", map[string]any{"hostname": name, "cluster_id": nc.ID}, &n); s != http.StatusCreated {
			t.Fatalf("node %s: %d", name, s)
		}
		ids[name] = n.ID
		stops[name] = startAgentWith(t, e, c, n.ID, strings.Repeat(string(rune('a'+i)), 32), &addrs{}, func(_ *agent.Agent, root string) {
			roots[name] = root
			galera := "*/5 * * * * root /usr/local/bin/galera-check.sh\n"
			if name == "nc3" {
				galera = "*/10 * * * * root /usr/local/bin/galera-check.sh\n"
			}
			files := map[string]string{"etc/cron.d/galera": galera}
			if name == "nc1" {
				files["etc/cron.d/nextcloud-sync"] = "0 */6 * * * root rsync -a /var/www/data/ root@nc2:/var/www/data/\n"
				files["var/spool/cron/crontabs/root"] = "15 2 * * * mysqldump -uroot -pGeheim nextcloud > /backup/nc.sql\n"
			}
			for p, content := range files {
				if err := os.MkdirAll(filepath.Join(root, filepath.Dir(p)), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(root, p), []byte(content), 0o644); err != nil {
					t.Fatal(err)
				}
			}
		})
	}

	var groups list[cronGroupView]
	eventually(t, "de cronjobs van de drie nodes", func() bool {
		if err := e.crons.SyncAll(ctx); err != nil {
			t.Fatal(err)
		}
		if s := c.do("GET", "/api/v1/cronjobs", nil, &groups); s != http.StatusOK {
			t.Fatalf("cronjobs: %d", s)
		}
		return len(groups.Items) == 1 && len(groups.Items[0].Nodes) == 3 && len(groups.Items[0].Jobs) == 4
	})
	g := groups.Items[0]
	if g.ClusterName != "Nextcloud" {
		t.Errorf("cluster %q", g.ClusterName)
	}
	byCmd := map[string]int{}
	for i, j := range g.Jobs {
		byCmd[j.Command+" "+j.Schedule] = i
	}
	galera := g.Jobs[byCmd["/usr/local/bin/galera-check.sh */5 * * * *"]]
	if len(galera.Nodes) != 2 || galera.Nodes[0].Hostname != "nc1" || galera.Nodes[1].Hostname != "nc2" || galera.Describe != "elke 5 minuten" || galera.NextRun == nil {
		t.Errorf("galera op nc1 en nc2: %+v", galera)
	}
	if j := g.Jobs[byCmd["/usr/local/bin/galera-check.sh */10 * * * *"]]; len(j.Nodes) != 1 || j.Nodes[0].Hostname != "nc3" {
		t.Errorf("galera op nc3: %+v", j)
	}
	dump, ok := byCmd["mysqldump -uroot -p*** nextcloud > /backup/nc.sql 15 2 * * *"]
	if !ok || g.Jobs[dump].Source != "user" || g.Jobs[dump].User != "root" || g.Jobs[dump].Describe != "elke dag om 02:15" {
		t.Errorf("dump zonder wachtwoord: %+v", g.Jobs)
	}

	// Een kijker ziet ze ook.
	e.createUser("kijker", "een-lang-wachtwoord", store.UserRoleViewer)
	viewer := e.client()
	if s, _, _ := viewer.login("kijker", "een-lang-wachtwoord", ""); s != http.StatusOK {
		t.Fatalf("login kijker: %d", s)
	}
	if s := viewer.do("GET", "/api/v1/cronjobs", nil, &groups); s != http.StatusOK {
		t.Fatalf("kijker: %d", s)
	}

	// Jonas zet de sync op nc1 op elke 4 uur: een event.
	if err := os.WriteFile(filepath.Join(roots["nc1"], "etc/cron.d/nextcloud-sync"), []byte("0 */4 * * * root rsync -a /var/www/data/ root@nc2:/var/www/data/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := e.crons.SyncAll(ctx); err != nil {
		t.Fatal(err)
	}
	var payload string
	if err := e.pool.QueryRow(ctx, "SELECT payload::text FROM events WHERE action = 'node.cron_changed' AND subject_id = $1", ids["nc1"]).Scan(&payload); err != nil {
		t.Fatalf("event: %v", err)
	}
	if !strings.Contains(payload, `"old_schedule": "0 */6 * * *"`) && !strings.Contains(payload, `"old_schedule":"0 */6 * * *"`) {
		t.Errorf("payload %s", payload)
	}
	var n int
	if err := e.pool.QueryRow(ctx, "SELECT count(*) FROM events WHERE action LIKE 'node.cron_%'").Scan(&n); err != nil || n != 1 {
		t.Errorf("%d events, wil 1 (de eerste keer lezen is geen wijziging): %v", n, err)
	}

	// Een agent met protocol 15 krijgt een melding; zijn jobs blijven staan.
	stops["nc3"]()
	if _, err := e.pool.Exec(ctx, "UPDATE agents SET protocol_version = 15 WHERE node_id = $1", ids["nc3"]); err != nil {
		t.Fatal(err)
	}
	if err := e.crons.SyncAll(ctx); err != nil {
		t.Fatal(err)
	}
	if s := c.do("GET", "/api/v1/cronjobs", nil, &groups); s != http.StatusOK {
		t.Fatal(s)
	}
	for _, nr := range groups.Items[0].Nodes {
		if nr.Hostname == "nc3" && (nr.ErrorKind != "old_agent" || !strings.Contains(nr.Error, "protocol 16")) {
			t.Errorf("nc3: %+v", nr)
		}
	}
	if len(groups.Items[0].Jobs) != 4 {
		t.Errorf("%d jobs na een te oude agent", len(groups.Items[0].Jobs))
	}
}
