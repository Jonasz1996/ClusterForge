package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Jonasz1996/clusterforge/internal/agent"
	"github.com/Jonasz1996/clusterforge/internal/agent/agenttest"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// TestDatabaseReapplyUnfinished speelt het zabbix-cluster van Jonas op 19
// oktober na: de uitrol van MariaDB Galera kwam op nc1 niet verder, omdat
// apt op nc2 een kapotte pakketbron had. Zelfherstel meldt dat de uitrol
// niet af is. Opnieuw toepassen herstart nc1 dan niet, want hij doet als
// enige mee. Stopte nc1 toch, dan begint Opnieuw toepassen het cluster daar
// opnieuw, behalve als er databases met tabellen op staan, en maakt het de
// uitrol af op nc2 en nc3.
func TestDatabaseReapplyUnfinished(t *testing.T) {
	e, c := adminClient(t)
	ctx := context.Background()
	var nc cluster
	if s := c.do("POST", "/api/v1/clusters", map[string]any{"slug": "nextcloud", "name": "Nextcloud", "type": "generic", "environment": "lab"}, &nc); s != http.StatusCreated {
		t.Fatalf("cluster: %d", s)
	}
	g := &galeraHosts{hosts: map[string]*agenttest.Host{}}
	ids := map[string]string{}
	// data: op nc1 staat een database met tabellen.
	var data atomic.Bool
	for i, name := range []string{"nc1", "nc2", "nc3"} {
		ip := fmt.Sprintf("10.0.20.%d", 186+i)
		var n node
		if s := c.do("POST", "/api/v1/nodes", map[string]any{"hostname": name, "cluster_id": nc.ID, "primary_ip": ip}, &n); s != http.StatusCreated {
			t.Fatalf("node %s: %d", name, s)
		}
		ids[name] = n.ID
		startAgentWith(t, e, c, n.ID, strings.Repeat(string(rune('a'+i)), 32), &addrs{}, func(ag *agent.Agent, root string) {
			writeFile(t, filepath.Join(root, "etc/os-release"), "ID=debian\nVERSION_ID=\"13\"\nPRETTY_NAME=\"Debian GNU/Linux 13 (trixie)\"\n", 0o644)
			writeFile(t, filepath.Join(root, "proc/meminfo"), "MemTotal: 4194304 kB\nMemAvailable: 2097152 kB\n", 0o644)
			h := agenttest.NewHost(root, ip)
			g.hosts[name] = h
			h.SetAfter(g.after(name, h))
			if name == "nc1" {
				// Het nakijken van de datamap: de unless mislukt en het
				// commando noemt de database.
				h.Shell = func(cmd string) (bool, []byte, error) {
					if !strings.Contains(cmd, "db.opt") || !data.Load() {
						return false, nil, nil
					}
					if strings.HasPrefix(cmd, protocol.DBCheckCommand+" || ") {
						return true, nil, errors.New("exit status 1")
					}
					return true, []byte("databases met tabellen: zabbix_proef \n"), errors.New("exit status 3")
				}
			}
			ag.Exec, ag.Root, ag.Collector.Run = h.Exec, root, h.Exec
			ag.Collector.Interfaces, ag.Collector.PrimaryAddress = h.Interfaces, func() string { return ip }
		})
	}
	eventually(t, "facts van de drie nodes", func() bool {
		var n int
		_ = e.pool.QueryRow(ctx, "SELECT count(*) FROM node_facts f JOIN nodes n ON n.id = f.node_id WHERE n.cluster_id = $1 AND f.facts->'os'->>'id' = 'debian'", nc.ID).Scan(&n)
		return n == 3
	})
	const broken = "E: The repository 'https://repo.zabbix.com/zabbix/8.0/stable/debian trixie Release' does not have a Release file.\n"
	g.hosts["nc2"].FailTimes("apt-get update", broken, 1<<20)

	base := "/api/v1/clusters/" + nc.ID + "/components"
	in := map[string]any{"engine": "mariadb", "nodes": []string{ids["nc1"], ids["nc2"], ids["nc3"]}}
	var plan componentPlanView
	if s := c.do("POST", base+"/plan", in, &plan); s != http.StatusOK || plan.Blocked {
		t.Fatalf("plan: %d %+v", s, plan)
	}
	in["hash"] = plan.Hash
	var j job
	if s := c.do("POST", base, in, &j); s != http.StatusAccepted {
		t.Fatalf("toevoegen: %d", s)
	}
	if j = waitJobLong(t, c, j.ID); j.Status != "failed" || !strings.Contains(j.Error, "does not have a Release file") {
		t.Fatalf("uitrol: %s %s", j.Status, j.Error)
	}
	members := func() []string {
		g.mu.Lock()
		defer g.mu.Unlock()
		return slices.Clone(g.members)
	}
	if m := members(); !slices.Equal(m, []string{"nc1"}) {
		t.Fatalf("na de uitrol: %v", m)
	}
	var comps list[componentView]
	c.do("GET", base, nil, &comps)
	cid := comps.Items[0].ID
	if comps.Items[0].AppliedRevision != nil {
		t.Fatalf("onderdeel: %+v", comps.Items[0])
	}

	// Zelfherstel meldt dat de uitrol niet af is, met de fout van de taak.
	unfinished := func(want bool) {
		t.Helper()
		eventually(t, fmt.Sprintf("Zelfherstel meldt de uitrol: %v", want), func() bool {
			if err := e.dbs.SyncAll(ctx); err != nil {
				t.Fatal(err)
			}
			if err := e.heal.InspectAll(ctx); err != nil {
				t.Fatal(err)
			}
			var raw json.RawMessage
			c.do("GET", "/api/v1/heal", nil, &raw)
			var l list[healView]
			_ = json.Unmarshal(raw, &l)
			for _, r := range l.Items {
				if r.Key != "mariadb/"+nc.ID {
					continue
				}
				got := false
				for _, p := range r.Problems {
					if p.Code == "component_unfinished" && strings.Contains(p.Why, "does not have a Release file") && strings.Contains(p.Why, "Kies Opnieuw toepassen") {
						got = true
					}
				}
				return got == want
			}
			return false
		})
	}
	unfinished(true)

	reapply := func() job {
		t.Helper()
		var j job
		if s := c.do("POST", base+"/"+cid+"/apply", map[string]any{}, &j); s != http.StatusAccepted {
			t.Fatalf("opnieuw toepassen: %d", s)
		}
		return waitJobLong(t, c, j.ID)
	}
	since := func() map[string]int {
		out := map[string]int{}
		for name, h := range g.hosts {
			out[name] = len(h.Calls())
		}
		return out
	}
	calls := func(name string, from map[string]int) string {
		return strings.Join(g.hosts[name].Calls()[from[name]:], "\n")
	}
	stepLog := func(j job, name string) string {
		for _, st := range j.Steps {
			if st.Name == name {
				return strings.Join(st.Log, "\n")
			}
		}
		return ""
	}

	// Een nieuwe configuratie op nc1, terwijl hij als enige meedoet: hij
	// herstart niet, anders stopt het cluster.
	cnf := filepath.Join(g.hosts["nc1"].Root, "etc/mysql/mariadb.conf.d/99-clusterforge-galera.cnf")
	b, err := os.ReadFile(cnf)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, cnf, string(b)+"max_connections = 999\n", 0o644)
	from := since()
	j = reapply()
	if j.Status != "failed" || !strings.Contains(j.Error, "does not have a Release file") {
		t.Fatalf("opnieuw met nc1 alleen: %s %s", j.Status, j.Error)
	}
	if c := calls("nc1", from); strings.Contains(c, "systemctl restart mariadb") || strings.Contains(c, "galera_new_cluster") {
		t.Errorf("nc1 herstartte:\n%s", c)
	}
	if log := stepLog(j, "nc1: MariaDB"); !strings.Contains(log, "nc1 doet als enige mee") {
		t.Errorf("nc1:\n%s", log)
	}
	if m := members(); !slices.Equal(m, []string{"nc1"}) {
		t.Fatalf("na opnieuw toepassen: %v", m)
	}

	// Stopt nc1 toch (zoals bij Jonas om 22:26), dan staat zijn merkteken er
	// nog. Met een database met tabellen begint Opnieuw toepassen het
	// cluster niet opnieuw, en vraagt het om bevestiging via Zelfherstel.
	g.restart("nc1", g.hosts["nc1"])
	if m := members(); len(m) != 0 {
		t.Fatalf("nc1 stopte niet: %v", m)
	}
	g.hosts["nc2"].Unfail("apt-get update")
	data.Store(true)
	from = since()
	j = reapply()
	if j.Status != "failed" || !strings.Contains(j.Error, "op nc1 staan al databases met tabellen") || !strings.Contains(j.Error, "Zelfherstel") {
		t.Fatalf("met data: %s %s", j.Status, j.Error)
	}
	if log := stepLog(j, "nc1: MariaDB"); !strings.Contains(log, "databases met tabellen: zabbix_proef") {
		t.Errorf("nc1 met data:\n%s", log)
	}
	if c := calls("nc1", from); strings.Contains(c, "galera_new_cluster") {
		t.Errorf("nc1 begon het cluster met data:\n%s", c)
	}
	if c := calls("nc2", from); c != "" {
		t.Errorf("nc2 kwam aan de beurt:\n%s", c)
	}

	// Zonder data begint nc1 het cluster opnieuw, en komen nc2 en nc3 erbij.
	data.Store(false)
	from = since()
	if j = reapply(); j.Status != "succeeded" {
		t.Fatalf("opnieuw zonder data: %s %s\n%+v", j.Status, j.Error, j.Steps)
	}
	begun := 0
	for _, call := range g.hosts["nc1"].Calls()[from["nc1"]:] {
		if strings.Contains(call, "galera_new_cluster") {
			begun++
		}
	}
	if begun != 1 {
		t.Errorf("nc1 begon het cluster %d keer opnieuw:\n%s", begun, calls("nc1", from))
	}
	if log := stepLog(j, "nc1: MariaDB"); !strings.Contains(log, "het Galera-cluster opnieuw beginnen") {
		t.Errorf("nc1 zonder data:\n%s", log)
	}
	for _, name := range []string{"nc2", "nc3"} {
		if c := calls(name, from); strings.Contains(c, "galera_new_cluster") || !strings.Contains(c, "apt-get install") {
			t.Errorf("%s:\n%s", name, c)
		}
	}
	if m := members(); len(m) != 3 {
		t.Fatalf("leden: %v", m)
	}
	c.do("GET", base, nil, &comps)
	if comps.Items[0].AppliedRevision == nil || *comps.Items[0].AppliedRevision != 1 || comps.Items[0].State != "ready" {
		t.Fatalf("onderdeel na de uitrol: %+v", comps.Items[0])
	}
	unfinished(false)
}
