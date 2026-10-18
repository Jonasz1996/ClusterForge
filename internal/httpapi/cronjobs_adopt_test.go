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
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// TestCronToggleFound is de acceptatie van mijlpaal 5 van fase 6: een
// gevonden cronjob gaat op elke node in commentaar, met het merkteken, en
// komt met één klik terug.
func TestCronToggleFound(t *testing.T) {
	e, c := adminClient(t)
	ctx := context.Background()

	var nc cluster
	if s := c.do("POST", "/api/v1/clusters", map[string]any{"slug": "nc", "name": "Nextcloud", "type": "generic", "environment": "test"}, &nc); s != http.StatusCreated {
		t.Fatalf("cluster: %d", s)
	}
	hosts, ids := map[string]*cronNode{}, map[string]string{}
	const line = "0 */6 * * * root sudo -u www-data php /var/www/nextcloud/occ files:scan --all"
	for i, name := range []string{"nc1", "nc2"} {
		var n node
		if s := c.do("POST", "/api/v1/nodes", map[string]any{"hostname": name, "cluster_id": nc.ID}, &n); s != http.StatusCreated {
			t.Fatalf("node %s: %d", name, s)
		}
		ids[name] = n.ID
		h := &cronNode{}
		hosts[name] = h
		startAgentWith(t, e, c, n.ID, strings.Repeat(string(rune('a'+i)), 32), &addrs{}, func(ag *agent.Agent, root string) {
			h.root = root
			ag.Root, ag.Collector.Run, ag.Exec = root, h.run, h.run
			writeFiles(t, root, map[string]string{"etc/cron.d/nextcloud": "MAILTO=\"\"\n" + line + "\n"})
			h.setLogging(t, true)
		})
	}

	type found struct {
		Key   string `json:"key"`
		Nodes []struct {
			NodeID   string `json:"node_id"`
			Hostname string `json:"hostname"`
			Enabled  bool   `json:"enabled"`
		} `json:"nodes"`
	}
	job := func() found {
		t.Helper()
		if err := e.crons.SyncAll(ctx); err != nil {
			t.Fatal(err)
		}
		var groups list[struct {
			Jobs []found `json:"jobs"`
		}]
		c.do("GET", "/api/v1/cronjobs", nil, &groups)
		for _, g := range groups.Items {
			if len(g.Jobs) == 1 {
				return g.Jobs[0]
			}
		}
		return found{}
	}
	var j found
	eventually(t, "de cronjob op nc1 en nc2", func() bool {
		j = job()
		return len(j.Nodes) == 2
	})

	// Uitzetten op elke node.
	for _, n := range j.Nodes {
		var out struct {
			Output []string `json:"output"`
		}
		if s := c.do("POST", "/api/v1/nodes/"+n.NodeID+"/crons/"+j.Key+"/toggle", map[string]any{"enabled": false}, &out); s != http.StatusOK ||
			len(out.Output) != 1 || !strings.Contains(out.Output[0], "regel 2") {
			t.Fatalf("uitzetten op %s: %d %v", n.Hostname, s, out)
		}
	}
	for name, h := range hosts {
		data, _ := os.ReadFile(filepath.Join(h.root, "etc/cron.d/nextcloud"))
		if string(data) != "MAILTO=\"\"\n"+protocol.CronOffPrefix+line+"\n" {
			t.Errorf("%s:\n%s", name, data)
		}
	}
	// De agent las de node meteen opnieuw: de job staat er nog, maar uit.
	j = job()
	for _, n := range j.Nodes {
		if n.Enabled {
			t.Errorf("%s staat nog aan", n.Hostname)
		}
	}
	var events int
	if err := e.pool.QueryRow(ctx, "SELECT count(*) FROM events WHERE action = 'node.cron_disabled'").Scan(&events); err != nil || events != 2 {
		t.Errorf("%d events: %v", events, err)
	}

	// Een kijker mag het niet; een oude agent kan het niet.
	e.createUser("kijker", "een-lang-wachtwoord", store.UserRoleViewer)
	viewer := e.client()
	if s, _, _ := viewer.login("kijker", "een-lang-wachtwoord", ""); s != http.StatusOK {
		t.Fatalf("login kijker: %d", s)
	}
	if s := viewer.do("POST", "/api/v1/nodes/"+ids["nc1"]+"/crons/"+j.Key+"/toggle", map[string]any{"enabled": true}, nil); s != http.StatusForbidden {
		t.Errorf("kijker: %d", s)
	}
	if s := c.do("POST", "/api/v1/nodes/"+ids["nc1"]+"/crons/bestaat-niet/toggle", map[string]any{"enabled": true}, nil); s != http.StatusNotFound {
		t.Errorf("onbekende job: %d", s)
	}
	if _, err := e.pool.Exec(ctx, "UPDATE agents SET protocol_version = 19 WHERE node_id = $1", ids["nc2"]); err != nil {
		t.Fatal(err)
	}
	var bad struct {
		Message string `json:"message"`
	}
	if s := c.do("POST", "/api/v1/nodes/"+ids["nc2"]+"/crons/"+j.Key+"/toggle", map[string]any{"enabled": true}, &bad); s != http.StatusConflict ||
		!strings.Contains(bad.Message, "protocol 20") {
		t.Errorf("oude agent: %d %+v", s, bad)
	}

	// Weer aan op nc1.
	if s := c.do("POST", "/api/v1/nodes/"+ids["nc1"]+"/crons/"+j.Key+"/toggle", map[string]any{"enabled": true}, nil); s != http.StatusOK {
		t.Fatalf("aanzetten: %d", s)
	}
	data, _ := os.ReadFile(filepath.Join(hosts["nc1"].root, "etc/cron.d/nextcloud"))
	if string(data) != "MAILTO=\"\"\n"+line+"\n" {
		t.Errorf("nc1 weer aan:\n%s", data)
	}
}
