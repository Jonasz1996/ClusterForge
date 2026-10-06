package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/agent/agenttest"
	"github.com/Jonasz1996/clusterforge/internal/drift"
	"github.com/Jonasz1996/clusterforge/internal/proxmox/pvefake"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/internal/templates"
)

// deployWeb rolt keepalived-nginx uit op twee nagespeelde VM's en wacht tot
// het cluster gezond is. Het geeft de machines, het cluster en de node-id's
// per hostname.
// kaLatest is de versie van keepalived-nginx waarmee een nieuwe uitrol
// gebeurt.
func kaLatest() string {
	t, _ := templates.Latest("keepalived-nginx")
	return t.Version
}

func deployWeb(t *testing.T, e *testEnv, c *client) (*fleet, string, map[string]string) {
	t.Helper()
	pve, srv, fp := newPVE(t)
	pve.AddGuest(pvefake.Guest{
		Type: "qemu", VMID: 9001, Name: "debian-13-cf", Node: "pve1", Status: "stopped", Template: true, MaxCPU: 1, MaxMem: 1 << 30,
		Config: map[string]string{
			"scsi0": "ceph:base-9001-disk-0,size=3G", "ide2": "ceph:vm-9001-cloudinit,media=cdrom",
			"boot": "order=scsi0", "agent": "enabled=1", "name": "debian-13-cf",
		},
	})
	f := newFleet(t, map[string]string{"web-01": "10.0.20.11", "web-02": "10.0.20.12"})
	pve.OnFileWrite(f.boot)
	var conn pveConn
	if s := c.do("POST", "/api/v1/proxmox", map[string]any{
		"name": "Thuislab", "api_url": srv.URL, "token_id": "clusterforge@pve!cf", "token_secret": "geheim-secret", "tls_fingerprint": fp,
	}, &conn); s != 201 {
		t.Fatalf("koppelen: %d", s)
	}
	var res struct {
		Job       job    `json:"job"`
		ClusterID string `json:"cluster_id"`
	}
	if s := c.do("POST", "/api/v1/deployments", map[string]any{
		"template": "keepalived-nginx",
		"cluster":  map[string]any{"name": "Web", "slug": "web", "environment": "prod"},
		"params":   map[string]any{"vip": "10.0.20.100", "auth_pass": "geheim12"},
		"target": map[string]any{
			"proxmox_id": conn.ID, "image_vmid": 9001, "network": "static",
			"first_ip": "10.0.20.11/24", "gateway": "10.0.20.1", "dns": []string{"10.0.20.1"},
			"ssh_keys": "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGJvbmFzIGtleQ jonas@laptop",
		},
		"server_url": e.srv.URL,
	}, &res); s != http.StatusAccepted {
		t.Fatalf("uitrollen: %d", s)
	}
	if j := waitJob(t, c, res.Job.ID); j.Status != "succeeded" {
		t.Fatalf("uitrol: %+v", j)
	}
	var cl struct {
		Status string `json:"status"`
		Nodes  []node `json:"nodes"`
	}
	eventually(t, "cluster gezond", func() bool {
		c.do("GET", "/api/v1/clusters/"+res.ClusterID, nil, &cl)
		return cl.Status == "healthy"
	})
	ids := map[string]string{}
	for _, n := range cl.Nodes {
		ids[n.Hostname] = n.ID
	}
	return f, res.ClusterID, ids
}

type driftFinding struct {
	Key         string     `json:"key"`
	Step        string     `json:"step"`
	Kind        string     `json:"kind"`
	Aspect      string     `json:"aspect"`
	Expected    string     `json:"expected"`
	Actual      string     `json:"actual"`
	Detail      string     `json:"detail"`
	Mtime       *time.Time `json:"mtime"`
	Since       time.Time  `json:"since"`
	Fingerprint string     `json:"fingerprint"`
}

type driftNode struct {
	NodeID       string         `json:"node_id"`
	Hostname     string         `json:"hostname"`
	Status       string         `json:"status"`
	CheckedAt    *time.Time     `json:"checked_at"`
	DriftSince   *time.Time     `json:"drift_since"`
	SpecRevision int            `json:"spec_revision"`
	Findings     []driftFinding `json:"findings"`
	Unchecked    []struct {
		Step   string `json:"step"`
		Reason string `json:"reason"`
	} `json:"unchecked"`
	Error       string `json:"error"`
	Skipped     string `json:"skipped"`
	AgentTooOld bool   `json:"agent_too_old"`
}

type driftReport struct {
	Source *struct {
		Kind            string `json:"kind"`
		Template        string `json:"template"`
		TemplateVersion string `json:"template_version"`
		SpecRevision    int    `json:"spec_revision"`
	} `json:"source"`
	Notes []string    `json:"notes"`
	Nodes []driftNode `json:"nodes"`
}

func (r driftReport) node(t *testing.T, hostname string) driftNode {
	t.Helper()
	i := slices.IndexFunc(r.Nodes, func(n driftNode) bool { return n.Hostname == hostname })
	if i < 0 {
		t.Fatalf("%s niet in het driftrapport: %+v", hostname, r)
	}
	return r.Nodes[i]
}

func findingKeys(fs []driftFinding) []string {
	out := []string{}
	for _, f := range fs {
		out = append(out, f.Key)
	}
	slices.Sort(out)
	return out
}

func TestDrift(t *testing.T) {
	e, c := adminClient(t)
	f, clusterID, ids := deployWeb(t, e, c)
	ctx := context.Background()
	web02 := f.host("web-02")
	driftURL := "/api/v1/clusters/" + clusterID + "/drift"

	// Nog nooit gecontroleerd.
	var rep driftReport
	if s := c.do("GET", driftURL, nil, &rep); s != 200 || rep.Source == nil || rep.Source.Kind != "template" ||
		rep.Source.Template != "keepalived-nginx" || rep.Source.TemplateVersion != kaLatest() || rep.Source.SpecRevision != 1 ||
		len(rep.Notes) != 0 || len(rep.Nodes) != 2 {
		t.Fatalf("drift voor de eerste controle: %d %+v", s, rep)
	}
	for _, n := range rep.Nodes {
		if n.Status != "unknown" || n.CheckedAt != nil || n.Skipped != "" || n.AgentTooOld {
			t.Fatalf("%s voor de eerste controle: %+v", n.Hostname, n)
		}
	}

	// Nu controleren: direct na de uitrol is alles in orde.
	before := map[string][]string{"web-01": f.host("web-01").Calls(), "web-02": web02.Calls()}
	if s := c.do("POST", driftURL+"/check", nil, &rep); s != 200 {
		t.Fatalf("controleren: %d", s)
	}
	for _, n := range rep.Nodes {
		if n.Status != "in_sync" || n.CheckedAt == nil || len(n.Findings) != 0 || n.SpecRevision != 1 || n.DriftSince != nil {
			t.Fatalf("%s na de uitrol: %+v", n.Hostname, n)
		}
		for _, u := range n.Unchecked {
			if !strings.HasPrefix(u.Step, "command:") {
				t.Errorf("%s niet gecontroleerd: %+v", n.Hostname, u)
			}
		}
	}
	driftEvents := func() []string {
		t.Helper()
		var out []string
		rows, err := e.pool.Query(ctx, "SELECT action FROM events WHERE action LIKE 'drift.%' ORDER BY id")
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		for rows.Next() {
			var a string
			_ = rows.Scan(&a)
			out = append(out, a)
		}
		return out
	}
	if ev := driftEvents(); len(ev) != 0 {
		t.Fatalf("events bij een cluster zonder drift: %v", ev)
	}

	// Drie wijzigingen met de hand op web-02.
	conf := filepath.Join(web02.Root, "etc/keepalived/keepalived.conf")
	index := filepath.Join(web02.Root, "var/www/html/index.html")
	if err := os.WriteFile(conf, []byte("vrrp_instance VI_1 {}\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(index, 0o600); err != nil {
		t.Fatal(err)
	}
	web02.SetUnit("nginx", agenttest.Unit{})

	// De scanner ziet het, bevestigt het en schrijft precies één event.
	e.drift.Interval, e.drift.StartDelay, e.drift.ConfirmDelay, e.drift.Timeout = time.Hour, 10*time.Millisecond, 300*time.Millisecond, 5*time.Second
	runCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); e.drift.Run(runCtx) }()
	t.Cleanup(func() { stop(); <-done })

	want := []string{
		"file:/etc/keepalived/keepalived.conf:content", "file:/var/www/html/index.html:mode",
		"service:nginx:active", "service:nginx:enabled",
	}
	eventually(t, "drift op web-02", func() bool {
		c.do("GET", driftURL, nil, &rep)
		return rep.node(t, "web-02").Status == "drift"
	})
	w2 := rep.node(t, "web-02")
	if got := findingKeys(w2.Findings); !slices.Equal(got, want) || w2.DriftSince == nil {
		t.Fatalf("afwijkingen op web-02: %q %+v", got, w2)
	}
	for _, fd := range w2.Findings {
		switch fd.Key {
		case "file:/var/www/html/index.html:mode":
			if fd.Expected != "0644" || fd.Actual != "0600" {
				t.Errorf("rechten: %+v", fd)
			}
		case "service:nginx:active":
			if fd.Expected != "active" || fd.Actual != "inactive" {
				t.Errorf("nginx: %+v", fd)
			}
		case "file:/etc/keepalived/keepalived.conf:content":
			if fd.Mtime == nil || !strings.HasPrefix(fd.Detail, "22 bytes in plaats van ") {
				t.Errorf("inhoud: %+v", fd)
			}
		}
		if len(fd.Fingerprint) != 64 || !fd.Since.Equal(*w2.DriftSince) {
			t.Errorf("afwijking: %+v", fd)
		}
	}
	if w1 := rep.node(t, "web-01"); w1.Status != "in_sync" {
		t.Fatalf("web-01: %+v", w1)
	}
	if ev := driftEvents(); !slices.Equal(ev, []string{"drift.detected"}) {
		t.Fatalf("events: %v", ev)
	}
	var payload struct {
		Count    int      `json:"count"`
		Keys     []string `json:"keys"`
		Hostname string   `json:"hostname"`
		Revision int      `json:"spec_revision"`
	}
	var subject string
	if err := e.pool.QueryRow(ctx, "SELECT payload, subject_id FROM events WHERE action = 'drift.detected'").Scan(&payload, &subject); err != nil {
		t.Fatal(err)
	}
	if payload.Count != 4 || len(payload.Keys) != 4 || payload.Hostname != "web-02" || payload.Revision != 1 || subject != ids["web-02"] {
		t.Fatalf("drift.detected: %+v %s", payload, subject)
	}

	// Controleren veranderde niets op de nodes.
	for name, calls := range before {
		if got := f.host(name).Calls(); !slices.Equal(got, calls) {
			t.Fatalf("%s: de controle voerde %v uit", name, got[len(calls):])
		}
	}
	if b, _ := os.ReadFile(conf); string(b) != "vrrp_instance VI_1 {}\n" {
		t.Fatalf("keepalived.conf veranderd: %q", b)
	}
	if fi, _ := os.Stat(index); fi.Mode().Perm() != 0o600 {
		t.Fatalf("index.html: %v", fi.Mode())
	}
	if u, _ := web02.Unit("nginx"); u.Enabled || u.Active {
		t.Fatalf("nginx: %+v", u)
	}
	var commands int
	_ = e.pool.QueryRow(ctx, "SELECT count(*) FROM events WHERE action = 'agent.command' AND payload::text LIKE '%state.inspect%'").Scan(&commands)
	if commands != 0 {
		t.Fatalf("%d controles in het logboek", commands)
	}

	// Geen geheim en geen hash van de inhoud, in de API noch in de database.
	clusterUUID := uuid.MustParse(clusterID)
	desired, err := e.deploy.Desired(ctx, clusterUUID)
	if err != nil {
		t.Fatal(err)
	}
	steps, err := desired.Render(uuid.MustParse(ids["web-02"]))
	if err != nil {
		t.Fatal(err)
	}
	hidden := []string{"geheim12", sha("vrrp_instance VI_1 {}\n")}
	for _, st := range steps {
		if st.File != nil {
			hidden = append(hidden, sha(st.File.Content))
		}
	}
	var raw json.RawMessage
	c.do("GET", driftURL, nil, &raw)
	var nodeRaw json.RawMessage
	c.do("GET", "/api/v1/nodes/"+ids["web-02"]+"/drift", nil, &nodeRaw)
	var stored, events string
	_ = e.pool.QueryRow(ctx, "SELECT coalesce(string_agg(d::text, ' '), '') FROM drift_checks d").Scan(&stored)
	_ = e.pool.QueryRow(ctx, "SELECT coalesce(string_agg(payload::text, ' '), '') FROM events WHERE action LIKE 'drift.%'").Scan(&events)
	for _, s := range hidden {
		for where, text := range map[string]string{"cluster-API": string(raw), "node-API": string(nodeRaw), "drift_checks": stored, "events": events} {
			if strings.Contains(text, s) {
				t.Fatalf("%s bevat %q", where, s)
			}
		}
	}

	// Nog een ronde met dezelfde afwijkingen: geen nieuw event.
	checked := *w2.CheckedAt
	if err := e.drift.Kick(ctx, &clusterUUID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "tweede ronde", func() bool {
		c.do("GET", driftURL, nil, &rep)
		n := rep.node(t, "web-02")
		return n.CheckedAt != nil && n.CheckedAt.After(checked)
	})
	if ev := driftEvents(); len(ev) != 1 || !rep.node(t, "web-02").DriftSince.Equal(*w2.DriftSince) {
		t.Fatalf("tweede ronde: %v %+v", ev, rep.node(t, "web-02"))
	}

	// Lezen mag iedereen, controleren alleen een beheerder.
	e.createUser("viewer", "een-lang-wachtwoord", store.UserRoleViewer)
	v := e.client()
	v.login("viewer", "een-lang-wachtwoord", "")
	var vrep driftReport
	if s := v.do("GET", "/api/v1/nodes/"+ids["web-02"]+"/drift", nil, &vrep); s != 200 || len(vrep.Nodes) != 1 || vrep.Nodes[0].Status != "drift" {
		t.Fatalf("viewer leest: %d %+v", s, vrep)
	}
	if s := v.do("POST", driftURL+"/check", nil, nil); s != http.StatusForbidden {
		t.Fatalf("viewer controleert cluster: %d", s)
	}
	if s := v.do("POST", "/api/v1/nodes/"+ids["web-02"]+"/drift/check", nil, nil); s != http.StatusForbidden {
		t.Fatalf("viewer controleert node: %d", s)
	}
	if s := c.do("GET", "/api/v1/clusters/"+uuid.NewString()+"/drift", nil, nil); s != http.StatusNotFound {
		t.Fatalf("onbekend cluster: %d", s)
	}

	// De samenvatting in de lijsten.
	var list struct {
		Items []struct {
			ID    string `json:"id"`
			Drift struct {
				Status         string     `json:"status"`
				NodesWithDrift int        `json:"nodes_with_drift"`
				CheckedAt      *time.Time `json:"checked_at"`
			} `json:"drift"`
		} `json:"items"`
	}
	v.do("GET", "/api/v1/clusters", nil, &list)
	if len(list.Items) != 1 || list.Items[0].Drift.Status != "drift" || list.Items[0].Drift.NodesWithDrift != 1 || list.Items[0].Drift.CheckedAt == nil {
		t.Fatalf("clusterlijst: %+v", list)
	}
	nodeDrift := func(id string) *string {
		var n struct {
			DriftStatus *string `json:"drift_status"`
		}
		v.do("GET", "/api/v1/nodes/"+id, nil, &n)
		return n.DriftStatus
	}
	if s := nodeDrift(ids["web-02"]); s == nil || *s != "drift" {
		t.Fatalf("drift_status web-02: %v", s)
	}
	if s := nodeDrift(ids["web-01"]); s == nil || *s != "in_sync" {
		t.Fatalf("drift_status web-01: %v", s)
	}

	// nginx weer aan: de set verandert, met één drift.changed.
	web02.SetUnit("nginx", agenttest.Unit{Enabled: true, Active: true})
	if err := e.drift.Kick(ctx, &clusterUUID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "nginx hersteld", func() bool {
		c.do("GET", driftURL, nil, &rep)
		return len(rep.node(t, "web-02").Findings) == 2
	})
	w2b := rep.node(t, "web-02")
	if ev := driftEvents(); !slices.Equal(ev, []string{"drift.detected", "drift.changed"}) || !w2b.DriftSince.Equal(*w2.DriftSince) {
		t.Fatalf("na nginx: %v %+v", ev, w2b)
	}
	for _, fd := range w2b.Findings {
		if !fd.Since.Equal(*w2.DriftSince) {
			t.Fatalf("sinds veranderde: %+v", fd)
		}
	}

	// Een gewenste staat die niet te lezen is: de controle mislukt één keer
	// met een event, en de afwijkingen blijven staan.
	setVersion := func(v string) {
		t.Helper()
		if _, err := e.pool.Exec(ctx, `UPDATE clusters SET template_version = $2,
			spec = jsonb_set(spec, '{template,version}', to_jsonb($2::text)) WHERE id = $1`, clusterID, v); err != nil {
			t.Fatal(err)
		}
	}
	setVersion("0.9.0")
	for range 2 {
		c.do("POST", driftURL+"/check", nil, &rep)
	}
	if w := rep.node(t, "web-02"); w.Status != "error" || !strings.Contains(w.Error, "gewenste staat") || len(w.Findings) != 2 ||
		len(rep.Notes) != 1 || !strings.HasPrefix(rep.Notes[0], "De gewenste staat is niet te lezen") {
		t.Fatalf("onleesbare gewenste staat: %+v", rep)
	}
	if ev := driftEvents(); countOf(ev, "drift.check_failed") != 2 {
		// Eén per node, niet per controle.
		t.Fatalf("mislukte controles: %v", ev)
	}
	setVersion(kaLatest())

	// Alles terug zoals de template het wil: in orde, met drift.resolved.
	for _, st := range steps {
		if st.File == nil || st.File.Path != "/etc/keepalived/keepalived.conf" {
			continue
		}
		if err := os.WriteFile(conf, []byte(st.File.Content), 0o640); err != nil {
			t.Fatal(err)
		}
		m, _ := strconv.ParseUint(st.File.Mode, 8, 32)
		if err := os.Chmod(conf, os.FileMode(m)); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(index, 0o644); err != nil {
		t.Fatal(err)
	}
	if s := c.do("POST", "/api/v1/nodes/"+ids["web-02"]+"/drift/check", nil, &rep); s != 200 || len(rep.Nodes) != 1 || rep.Nodes[0].Status != "in_sync" ||
		rep.Nodes[0].DriftSince != nil {
		t.Fatalf("na herstel: %d %+v", s, rep)
	}
	var resolved struct {
		Removed  []string `json:"removed"`
		Duration *int     `json:"duration_seconds"`
	}
	if err := e.pool.QueryRow(ctx, "SELECT payload FROM events WHERE action = 'drift.resolved'").Scan(&resolved); err != nil ||
		len(resolved.Removed) != 2 || resolved.Duration == nil {
		t.Fatalf("drift.resolved: %+v %v", resolved, err)
	}

	// Een agent zonder state.inspect wordt overgeslagen, met de reden.
	var extra node
	c.do("POST", "/api/v1/nodes", map[string]any{"hostname": "web-09", "cluster_id": clusterID}, &extra)
	if _, err := e.pool.Exec(ctx, `INSERT INTO agents (id, node_id, nkey_public, protocol_version) VALUES ($1, $2, 'UOUDEAGENT', 3)`,
		uuid.New(), extra.ID); err != nil {
		t.Fatal(err)
	}
	c.do("POST", driftURL+"/check", nil, &rep)
	if old := rep.node(t, "web-09"); !old.AgentTooOld || old.Skipped != drift.AgentTooOld || old.Status != "unknown" ||
		!slices.Contains(rep.Notes, "Het lidmaatschap wijkt af van de specificatie:") {
		t.Fatalf("oude agent: %+v %q", old, rep.Notes)
	}

	// Een node die naar een ander cluster gaat, verliest zijn controle.
	var other cluster
	if c.do("POST", "/api/v1/clusters", map[string]any{"name": "Los", "slug": "los", "type": "generic", "environment": "test"}, &other); other.ID == "" {
		t.Fatal("ander cluster niet gemaakt")
	}
	if s := c.do("PATCH", "/api/v1/nodes/"+ids["web-02"], map[string]any{"cluster_id": other.ID}, nil); s != 200 {
		t.Fatalf("verplaatsen: %d", s)
	}
	var rows int
	_ = e.pool.QueryRow(ctx, "SELECT count(*) FROM drift_checks WHERE node_id = $1", ids["web-02"]).Scan(&rows)
	if rows != 0 || nodeDrift(ids["web-02"]) != nil {
		t.Fatalf("controle na verplaatsen: %d rijen", rows)
	}
	if s := c.do("GET", "/api/v1/nodes/"+ids["web-02"]+"/drift", nil, &rep); s != 200 || rep.Source != nil || len(rep.Nodes) != 0 {
		t.Fatalf("drift zonder gewenste staat: %d %+v", s, rep)
	}
}

func sha(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
