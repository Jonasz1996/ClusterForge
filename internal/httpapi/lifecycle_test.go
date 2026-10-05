package httpapi

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/Jonasz1996/clusterforge/internal/agent"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

// keepalivedSim speelt keepalived na op een paar nodes: de VIP staat op de
// eerste node (in volgorde van prioriteit) waarop keepalived draait.
type keepalivedSim struct {
	mu    sync.Mutex
	vip   string
	nodes []*simNode
}

type simNode struct {
	sim             *keepalivedSim
	name            string
	own             string
	addrs           *addrs
	root            string
	enabled, active bool
	failDisable     bool
	stop            func()
	calls           []string
}

func (s *keepalivedSim) add(name, own string) *simNode {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := &simNode{sim: s, name: name, own: own, addrs: &addrs{}, enabled: true, active: true}
	s.nodes = append(s.nodes, n)
	s.update()
	return n
}

// update verdeelt de VIP; de aanroeper heeft het slot.
func (s *keepalivedSim) update() {
	holder := -1
	for i, n := range s.nodes {
		if n.active && holder < 0 {
			holder = i
		}
	}
	for i, n := range s.nodes {
		if i == holder {
			n.addrs.set(n.own, s.vip)
		} else {
			n.addrs.set(n.own)
		}
	}
}

// exec speelt systemctl op deze node, voor de agent en de collector.
func (n *simNode) exec(_ context.Context, name string, args ...string) ([]byte, error) {
	s := n.sim
	s.mu.Lock()
	defer s.mu.Unlock()
	if name != "systemctl" || len(args) == 0 {
		return nil, errors.New("geen commando's in tests")
	}
	state := func() string {
		enabled, active := "disabled", "inactive"
		if n.enabled {
			enabled = "enabled"
		}
		if n.active {
			active = "active"
		}
		return "LoadState=loaded\nUnitFileState=" + enabled + "\nActiveState=" + active + "\n"
	}
	if args[0] != "show" {
		n.calls = append(n.calls, strings.Join(args, " "))
	}
	switch args[0] {
	case "show":
		if args[1] == "keepalived" {
			return []byte(state()), nil
		}
		// De collector vraagt alle gevolgde units tegelijk op.
		return []byte("Id=keepalived.service\n" + state()), nil
	case "disable":
		if n.failDisable {
			return []byte("Failed to disable unit: Access denied\n"), errors.New("exit status 1")
		}
		n.enabled = false
		if slices.Contains(args, "--now") {
			n.active = false
		}
	case "enable":
		n.enabled = true
	case "start":
		n.active = true
	case "stop":
		n.active = false
	case "reboot":
		// Na de herstart begint de uptime opnieuw en start keepalived als het
		// enabled is.
		_ = os.WriteFile(filepath.Join(n.root, "proc/uptime"), []byte("1.00 1.00\n"), 0o644)
		n.active = n.enabled
	case "poweroff":
		n.active = false
		go n.stop()
	}
	s.update()
	return nil, nil
}

func (n *simNode) called() []string {
	n.sim.mu.Lock()
	defer n.sim.mu.Unlock()
	return slices.Clone(n.calls)
}

func (n *simNode) setFailDisable(v bool) {
	n.sim.mu.Lock()
	defer n.sim.mu.Unlock()
	n.failDisable = v
}

func (n *simNode) keepalived() (enabled, active bool) {
	n.sim.mu.Lock()
	defer n.sim.mu.Unlock()
	return n.enabled, n.active
}

func startSimAgent(t *testing.T, e *testEnv, c *client, nodeID, machineID string, n *simNode) {
	t.Helper()
	n.stop = startAgentWith(t, e, c, nodeID, machineID, n.addrs, func(ag *agent.Agent, root string) {
		n.root = root
		if err := os.WriteFile(filepath.Join(root, "proc/uptime"), []byte("86400.00 80000.00\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		ag.Exec = n.exec
		ag.Collector.Run = n.exec
	})
}

func TestNodeLifecycle(t *testing.T) {
	e, c := adminClient(t)

	var cl cluster
	c.do("POST", "/api/v1/clusters", map[string]any{"slug": "lb", "name": "LB", "type": "keepalived", "environment": "prod"}, &cl)
	c.do("POST", "/api/v1/clusters/"+cl.ID+"/vips", map[string]any{"address": "10.99.0.30"}, nil)
	var lb01, lb02, lb03 node
	c.do("POST", "/api/v1/nodes", map[string]any{"hostname": "lb01", "cluster_id": cl.ID}, &lb01)
	c.do("POST", "/api/v1/nodes", map[string]any{"hostname": "lb02", "cluster_id": cl.ID}, &lb02)
	c.do("POST", "/api/v1/nodes", map[string]any{"hostname": "lb03", "cluster_id": cl.ID}, &lb03)

	sim := &keepalivedSim{vip: "10.99.0.30"}
	k1, k2 := sim.add("lb01", "192.0.2.31"), sim.add("lb02", "192.0.2.32")
	startSimAgent(t, e, c, lb01.ID, strings.Repeat("1", 32), k1)
	startSimAgent(t, e, c, lb02.ID, strings.Repeat("2", 32), k2)

	var d statusView
	clusterIs := func(what, status, owner string) {
		t.Helper()
		eventually(t, what, func() bool {
			c.do("GET", "/api/v1/clusters/"+cl.ID, nil, &d)
			o := ""
			if d.Vips[0].OwnerHostname != nil {
				o = *d.Vips[0].OwnerHostname
			}
			return (status == "" || d.Status == status) && o == owner
		})
	}
	lifecycleOf := func(id string) string {
		var n struct {
			Lifecycle string `json:"lifecycle"`
		}
		c.do("GET", "/api/v1/nodes/"+id, nil, &n)
		return n.Lifecycle
	}
	act := func(id string, body map[string]any) (int, job, apiErr) {
		t.Helper()
		var raw struct {
			job
			apiErr
		}
		s := c.do("POST", "/api/v1/nodes/"+id+"/actions", body, &raw)
		return s, raw.job, raw.apiErr
	}
	run := func(id string, body map[string]any) job {
		t.Helper()
		s, j, ae := act(id, body)
		if s != http.StatusAccepted {
			t.Fatalf("%v: %d %+v", body, s, ae)
		}
		return waitJob(t, c, j.ID)
	}
	stepNames := func(j job) []string {
		var out []string
		for _, s := range j.Steps {
			out = append(out, s.Name)
		}
		return out
	}
	// Er draait één taak per node tegelijk; wacht tot de agents verbonden zijn.
	clusterIs("gezond cluster", "healthy", "lb01")
	var n1 struct {
		Agent struct {
			Commands bool `json:"commands"`
		} `json:"agent"`
	}
	c.do("GET", "/api/v1/nodes/"+lb01.ID, nil, &n1)
	if !n1.Agent.Commands {
		t.Fatal("agent zou commando's moeten kunnen")
	}

	// Een viewer mag niets doen.
	e.createUser("kijker", "een-lang-wachtwoord", store.UserRoleViewer)
	viewer := e.client()
	viewer.login("kijker", "een-lang-wachtwoord", "")
	if s := viewer.do("POST", "/api/v1/nodes/"+lb01.ID+"/actions", map[string]any{"action": "maintenance"}, nil); s != http.StatusForbidden {
		t.Fatalf("viewer: %d", s)
	}

	// Onderhoud: keepalived uit, de VIP verhuist, daarna pas in onderhoud.
	j := run(lb01.ID, map[string]any{"action": "maintenance", "reason": "nieuwe schijf"})
	if j.Status != "succeeded" || !slices.Equal(stepNames(j), []string{"VIP's naar een andere node verhuizen", "Node in onderhoud zetten"}) {
		t.Fatalf("onderhoud: %+v", j)
	}
	if log := strings.Join(j.Steps[0].Log, "\n"); !strings.Contains(log, "10.99.0.30 staat nu op lb02") {
		t.Fatalf("log: %s", log)
	}
	if l := lifecycleOf(lb01.ID); l != "maintenance" {
		t.Fatalf("lifecycle %s", l)
	}
	clusterIs("VIP op lb02", "healthy", "lb02")
	if got := k1.called(); !slices.Equal(got, []string{"disable --now keepalived"}) {
		t.Fatalf("lb01: %q", got)
	}
	if got := eventActions(t, e, lb01.ID); countOf(got, "node.lifecycle_changed") != 2 {
		t.Fatalf("events: %q", got)
	}
	if s, _, ae := act(lb01.ID, map[string]any{"action": "maintenance"}); s != http.StatusConflict || ae.Code != "conflict" {
		t.Fatalf("dubbel onderhoud: %d %+v", s, ae)
	}

	// lb02 heeft nu de VIP en niemand kan hem overnemen.
	s, _, ae := act(lb02.ID, map[string]any{"action": "reboot"})
	if s != http.StatusConflict || ae.Code != "needs_force" || !strings.Contains(ae.Message, "10.99.0.30") {
		t.Fatalf("reboot zonder overnemer: %d %+v", s, ae)
	}

	// Onderhoud beëindigen: keepalived terug zoals het was.
	j = run(lb01.ID, map[string]any{"action": "activate"})
	if j.Status != "succeeded" || !slices.Equal(stepNames(j), []string{"Keepalived weer aanzetten", "Node weer actief maken"}) {
		t.Fatalf("activate: %+v", j)
	}
	if en, ac := k1.keepalived(); !en || !ac || lifecycleOf(lb01.ID) != "active" {
		t.Fatalf("na activate: enabled %v active %v lifecycle %s", en, ac, lifecycleOf(lb01.ID))
	}
	clusterIs("VIP terug op lb01", "healthy", "lb01")

	// Lukt het uitzetten niet, dan draait de taak het terug.
	k1.setFailDisable(true)
	j = run(lb01.ID, map[string]any{"action": "maintenance"})
	if j.Status != "failed" || !strings.Contains(j.Error, "keepalived uitzetten mislukt") {
		t.Fatalf("mislukt onderhoud: %+v", j)
	}
	if log := strings.Join(j.Steps[0].Log, "\n"); !strings.Contains(log, "Access denied") || !strings.Contains(log, "terugdraaien") {
		t.Fatalf("log: %s", log)
	}
	if l := lifecycleOf(lb01.ID); l != "active" {
		t.Fatalf("lifecycle na terugdraaien: %s", l)
	}
	k1.setFailDisable(false)
	clusterIs("VIP nog op lb01", "healthy", "lb01")

	// Herstarten: VIP weg, herstart, wachten, keepalived terug, weer actief.
	j = run(lb01.ID, map[string]any{"action": "reboot", "reason": "kernelupdate"})
	want := []string{
		"VIP's naar een andere node verhuizen", "Node in onderhoud zetten", "Herstarten en wachten tot de node terug is",
		"Keepalived weer aanzetten", "Node weer actief maken",
	}
	if j.Status != "succeeded" || !slices.Equal(stepNames(j), want) {
		t.Fatalf("reboot: %+v", j)
	}
	if !slices.Contains(k1.called(), "reboot --message=ClusterForge: kernelupdate") {
		t.Fatalf("lb01: %q", k1.called())
	}
	if l := lifecycleOf(lb01.ID); l != "active" {
		t.Fatalf("lifecycle na reboot: %s", l)
	}
	// In het logboek: wie de taak vroeg en vanaf waar, en elk commando aan de
	// agent als deel van de taak.
	byJob := c.audit("job=" + j.ID).Items
	if q := find(t, byJob, "job.queued"); q.IP == nil || *q.IP != "127.0.0.1" || q.Session == nil || q.Actor.Name != "admin" {
		t.Fatalf("job.queued: %+v", q)
	}
	var commands []string
	for _, it := range byJob {
		if it.Action != "agent.command" {
			continue
		}
		commands = append(commands, it.Summary)
		if it.Actor.Type != "system" || it.OnBehalfOf == nil || it.OnBehalfOf.Name != "admin" || it.Node == nil || it.Node.ID != lb01.ID ||
			it.Job == nil || it.Job.ID != j.ID {
			t.Fatalf("agent.command: %+v", it)
		}
	}
	if !slices.Contains(commands, "Commando aan lb01: herstarten (reden: kernelupdate)") || len(commands) < 3 {
		t.Fatalf("commando's in het logboek: %q", commands)
	}
	clusterIs("VIP weer op lb01", "healthy", "lb01")

	// Afsluiten zonder de VIP eerst weg te halen; lb02 heeft hem niet.
	j = run(lb02.ID, map[string]any{"action": "shutdown", "drain": false})
	if j.Status != "succeeded" || !slices.Equal(stepNames(j), []string{"Node in onderhoud zetten", "Afsluiten en wachten tot de node uit is"}) {
		t.Fatalf("shutdown: %+v", j)
	}
	if l := lifecycleOf(lb02.ID); l != "maintenance" {
		t.Fatalf("lifecycle na shutdown: %s", l)
	}
	eventually(t, "lb02 offline", func() bool {
		s, _, ae := act(lb02.ID, map[string]any{"action": "refresh_facts"})
		return s == http.StatusConflict && strings.Contains(ae.Message, "offline")
	})
	if s, _, ae := act(lb02.ID, map[string]any{"action": "activate"}); s != http.StatusConflict || !strings.Contains(ae.Message, "start de node eerst") {
		t.Fatalf("activate offline: %d %+v", s, ae)
	}

	j = run(lb01.ID, map[string]any{"action": "refresh_facts"})
	if j.Status != "succeeded" || j.Steps[0].Log[0] != "facts verzameld en naar de server gestuurd" {
		t.Fatalf("facts: %+v", j)
	}

	// Met force gaat onderhoud door, ook al is de VIP daarna weg.
	if s, _, ae := act(lb01.ID, map[string]any{"action": "maintenance"}); s != http.StatusConflict || ae.Code != "needs_force" {
		t.Fatalf("onderhoud zonder overnemer: %d %+v", s, ae)
	}
	j = run(lb01.ID, map[string]any{"action": "maintenance", "force": true})
	if j.Status != "succeeded" {
		t.Fatalf("onderhoud met force: %+v", j)
	}
	// lb03 heeft geen agent en zou de VIP kunnen hebben, dus de status is
	// onbekend; een eigenaar is er niet meer.
	clusterIs("VIP zonder eigenaar", "", "")

	// Een node zonder agent zet je alleen op onderhoud.
	j = run(lb03.ID, map[string]any{"action": "maintenance"})
	if j.Status != "succeeded" || !slices.Equal(stepNames(j), []string{"Node in onderhoud zetten"}) {
		t.Fatalf("zonder agent: %+v", j)
	}
	if s, _, ae := act(lb03.ID, map[string]any{"action": "reboot"}); s != http.StatusConflict || ae.Message != "deze node heeft geen agent" {
		t.Fatalf("reboot zonder agent: %d %+v", s, ae)
	}
	if s, _, _ := act(lb03.ID, map[string]any{"action": "dansen"}); s != http.StatusBadRequest {
		t.Fatalf("onbekende actie: %d", s)
	}
}
