package httpapi

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/cronjobs"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// TestTemplateSyncs: de sync die een template vraagt (fase 11, Nginx Proxy
// Manager) komt als beheerde cronjob van de soort sync vanaf het VIP, met
// zijn voorbereiding; een volgende keer past ze de job aan, laat een job die
// de beheerder uitzette uit, en een agent die de voorbereiding niet kent,
// krijgt de job niet.
func TestTemplateSyncs(t *testing.T) {
	e, c := adminClient(t)
	ctx := context.Background()
	var cl cluster
	if s := c.do("POST", "/api/v1/clusters", map[string]any{"slug": "proxy", "name": "Proxy", "type": "generic", "environment": "test"}, &cl); s != http.StatusCreated {
		t.Fatalf("cluster: %d", s)
	}
	if s := c.do("POST", "/api/v1/clusters/"+cl.ID+"/vips", map[string]any{"address": "192.0.2.50"}, nil); s != http.StatusCreated {
		t.Fatalf("vip: %d", s)
	}
	for i, name := range []string{"npm-01", "npm-02"} {
		var n node
		if s := c.do("POST", "/api/v1/nodes", map[string]any{"hostname": name, "cluster_id": cl.ID}, &n); s != http.StatusCreated {
			t.Fatalf("node: %d", s)
		}
		proto := protocol.Version
		if i == 1 {
			proto = protocol.SyncBeforeSince - 1
		}
		if _, err := e.pool.Exec(ctx, `INSERT INTO agents (id, node_id, nkey_public, protocol_version) VALUES ($1, $2, $3, $4)`,
			uuid.New(), n.ID, "UNPM"+strings.Repeat("A", i+1), proto); err != nil {
			t.Fatal(err)
		}
	}
	id := uuid.MustParse(cl.ID)
	want := cronjobs.TemplateSync{Name: "NPM-configuratie", Schedule: "*/5 * * * *", Source: "/var/lib/cf-npm/outgoing/",
		Target: "/var/lib/cf-npm/incoming/", Before: "/usr/local/lib/clusterforge/npm-ctl.sh snapshot", Delete: true, Alert: true, TimeoutSeconds: 900}
	e.crons.EnsureTemplateSyncs(ctx, id, []cronjobs.TemplateSync{want})

	type syncView struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Kind    string `json:"kind"`
		RunOn   string `json:"run_on"`
		Enabled bool   `json:"enabled"`
		Sched   string `json:"schedule"`
		Rev     int    `json:"revision"`
		Sync    struct {
			Before string `json:"before"`
			Delete bool   `json:"delete"`
			Target string `json:"target"`
		} `json:"sync"`
	}
	list := func() []syncView {
		var out struct {
			Jobs []syncView `json:"items"`
		}
		if s := c.do("GET", "/api/v1/cron-jobs", nil, &out); s != http.StatusOK {
			t.Fatalf("lijst: %d", s)
		}
		return out.Jobs
	}
	jobs := list()
	if len(jobs) != 1 {
		t.Fatalf("jobs: %+v", jobs)
	}
	j := jobs[0]
	if j.Name != "NPM-configuratie" || j.Kind != "sync" || j.RunOn != "vip" || !j.Enabled || j.Sync.Before != want.Before || !j.Sync.Delete ||
		j.Sync.Target != want.Target {
		t.Errorf("job: %+v", j)
	}

	// Nog eens hetzelfde: geen nieuwe revisie.
	e.crons.EnsureTemplateSyncs(ctx, id, []cronjobs.TemplateSync{want})
	if again := list(); len(again) != 1 || again[0].Rev != j.Rev {
		t.Errorf("tweede keer: %+v", again)
	}

	// De beheerder zet hem uit; een nieuw schema uit de template laat hem uit.
	if _, err := e.pool.Exec(ctx, "UPDATE cron_jobs SET enabled = false WHERE id = $1", j.ID); err != nil {
		t.Fatal(err)
	}
	want.Schedule = "0 * * * *"
	e.crons.EnsureTemplateSyncs(ctx, id, []cronjobs.TemplateSync{want})
	if after := list(); len(after) != 1 || after[0].Sched != "0 * * * *" || after[0].Enabled || after[0].Rev == j.Rev {
		t.Errorf("na een nieuw schema: %+v", after)
	}

	// Het plan zegt dat de agent op npm-02 te oud is voor de voorbereiding.
	var plan struct {
		Nodes []planNodeView `json:"nodes"`
	}
	if s := c.do("GET", "/api/v1/cron-jobs/"+j.ID+"/plan", nil, &plan); s != http.StatusOK {
		t.Fatalf("plan: %d", s)
	}
	found := false
	for _, n := range plan.Nodes {
		for _, ch := range n.Checks {
			if n.Hostname == "npm-02" && strings.Contains(ch.Text, "voorbereiding vraagt") {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("plan: %+v", plan.Nodes)
	}
}
