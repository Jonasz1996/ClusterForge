package agentbus

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/internal/store/storetest"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// testBus is een Bus zonder NATS, genoeg voor de handlers.
func testBus(t *testing.T) (*Bus, store.Node) {
	t.Helper()
	pool := storetest.DB(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	q := store.New(pool)
	b := &Bus{log: log, pool: pool, q: q, ev: events.NewWriter(q, log), started: time.Now().Add(-time.Hour)}
	ctx := context.Background()
	cl, err := q.CreateCluster(ctx, store.CreateClusterParams{Slug: "web", Name: "Web", Type: "keepalived", Environment: store.EnvironmentLab, Tags: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	node, err := q.CreateNode(ctx, store.CreateNodeParams{ClusterID: &cl.ID, Hostname: "web-01", Lifecycle: store.NodeLifecycleActive, Tags: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	return b, node
}

type row struct {
	TS                                     time.Time
	Source, Kind, Subject, Action, Summary string
	Detail                                 map[string]string
	JobID                                  *uuid.UUID
}

func changesOf(t *testing.T, b *Bus, nodeID uuid.UUID) []row {
	t.Helper()
	rows, err := b.pool.Query(context.Background(),
		"SELECT ts, source, kind, subject, action, summary, detail, job_id FROM node_changes WHERE node_id = $1 ORDER BY ts, id", nodeID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []row
	for rows.Next() {
		var r row
		var d []byte
		if err := rows.Scan(&r.TS, &r.Source, &r.Kind, &r.Subject, &r.Action, &r.Summary, &d, &r.JobID); err != nil {
			t.Fatal(err)
		}
		_ = json.Unmarshal(d, &r.Detail)
		out = append(out, r)
	}
	return out
}

func near(a, b time.Time) bool { return a.Sub(b).Abs() < 2*time.Second }

func TestHandleChanges(t *testing.T) {
	b, node := testBus(t)
	ctx := context.Background()
	received := time.Now()
	// De klok van de node loopt een uur voor.
	offset := clockOffset(received.Add(time.Hour), received)
	if offset < 3_599_000 || offset > 3_601_000 {
		t.Fatalf("klokafwijking: %d", offset)
	}
	nodeTime := received.Add(time.Hour)
	c := protocol.Changes{Truncated: true, Entries: []protocol.Change{
		{TS: nodeTime.Add(-10 * time.Minute), Kind: protocol.ChangePackage, Subject: "nginx", Action: "upgrade", From: "1", To: "2", By: "root"},
		{TS: nodeTime, Kind: "facts", Subject: "os", Action: "changed"},                 // geen soort van de agent
		{TS: nodeTime, Kind: protocol.ChangeService, Subject: "nginx.service"},          // zonder actie
		{TS: time.Time{}, Kind: protocol.ChangeService, Subject: "x", Action: "failed"}, // zonder tijd
		{TS: nodeTime.Add(-9 * 24 * time.Hour), Kind: protocol.ChangeFile, Subject: "/etc/oud", Action: "modified"},
		{TS: nodeTime.Add(5 * time.Hour), Kind: protocol.ChangeLogin, Subject: "root", Action: "login",
			Detail: map[string]string{"ip": "10.0.0.5", "method": "publickey"}},
		{TS: nodeTime.Add(-time.Minute), Kind: protocol.ChangeFile, Subject: "/etc/apt/sources.list\x1b",
			Action: "modified", By: "deb https://jonas:geheim@apt.example/"},
	}}
	if err := b.handleChanges(ctx, node.ID, c, offset, received); err != nil {
		t.Fatal(err)
	}
	// Nog een keer, zoals na een verloren ack: geen dubbele regels.
	if err := b.handleChanges(ctx, node.ID, c, offset, received); err != nil {
		t.Fatal(err)
	}
	got := changesOf(t, b, node.ID)
	var keys []string
	for _, r := range got {
		keys = append(keys, r.Kind+":"+r.Action)
	}
	if want := []string{"package:upgrade", "file:modified", "login:login", "agent:truncated"}; !slices.Equal(keys, want) {
		t.Fatalf("regels: %v", keys)
	}
	pkg, file, login := got[0], got[1], got[2]
	if !near(pkg.TS, received.Add(-10*time.Minute)) || pkg.Source != "agent" || pkg.Summary != "nginx bijgewerkt van 1 naar 2 (root)" {
		t.Errorf("pakket: %+v", pkg)
	}
	if strings.ContainsRune(file.Subject, 0x1b) || strings.Contains(file.Detail["by"], "geheim") {
		t.Errorf("niet opgeschoond: %+v", file)
	}
	// Een regel uit de toekomst krijgt de tijd van ontvangst.
	if !near(login.TS, received) || login.Summary != "SSH-login als root vanaf 10.0.0.5 (sleutel)" {
		t.Errorf("login: %+v", login)
	}
	if got[3].Source != "server" {
		t.Errorf("overgelopen: %+v", got[3])
	}

	// Met CF_JOURNAL_SSH_LOGINS=false komen er geen logins meer bij.
	b.SetJournalLogins(false)
	c = protocol.Changes{Entries: []protocol.Change{
		{TS: nodeTime.Add(-time.Minute), Kind: protocol.ChangeLogin, Subject: "jonas", Action: "login"},
		{TS: nodeTime.Add(-time.Minute), Kind: protocol.ChangeBoot, Subject: "boot", Action: "boot"},
	}}
	if err := b.handleChanges(ctx, node.ID, c, offset, received); err != nil {
		t.Fatal(err)
	}
	keys = nil
	for _, r := range changesOf(t, b, node.ID) {
		keys = append(keys, r.Kind+":"+r.Subject)
	}
	if slices.Contains(keys, "login:jonas") || !slices.Contains(keys, "boot:boot") {
		t.Fatalf("zonder logins: %v", keys)
	}
}

func TestHandleConns(t *testing.T) {
	b, node := testBus(t)
	ctx := context.Background()
	other, err := b.q.CreateNode(ctx, store.CreateNodeParams{Hostname: "db-01", Lifecycle: store.NodeLifecycleActive, Tags: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.pool.Exec(ctx, "UPDATE nodes SET primary_ip = '10.0.30.11' WHERE id = $1", other.ID); err != nil {
		t.Fatal(err)
	}
	c := protocol.Conns{
		Out: []protocol.ConnOut{
			{Unit: "php8.2-fpm.service", Addr: "10.0.30.100", Port: 3306, Count: 3},
			{Unit: "php8.2-fpm.service", Addr: "10.0.30.100", Port: 3306, Count: 2},
			{Unit: "x", Addr: "geen-ip", Port: 1, Count: 1},
		},
		In: []protocol.ConnIn{
			{Unit: "nginx.service", Port: 80, Peer: "10.0.30.11", Count: 4},
			{Unit: "nginx.service", Port: 80, Peer: "203.0.113.9", Count: 7},
		},
	}
	for range 2 {
		if err := b.handleConns(ctx, node.ID, c, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := b.pool.Query(ctx, `SELECT unit, direction, peer_addr, peer_port, local_port, samples
		FROM observed_connections WHERE node_id = $1 ORDER BY direction, peer_addr`, node.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var unit, dir, peer string
		var pp, lp int32
		var n int64
		_ = rows.Scan(&unit, &dir, &peer, &pp, &lp, &n)
		got = append(got, strings.Join([]string{unit, dir, peer, strconv.Itoa(int(pp)), strconv.Itoa(int(lp)), strconv.FormatInt(n, 10)}, " "))
	}
	// Een onbekende afzender blijft anoniem.
	want := []string{
		"nginx.service in  0 80 14",
		"nginx.service in 10.0.30.11 0 80 8",
		"php8.2-fpm.service out 10.0.30.100 3306 0 10",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("verbindingen:\n %q\n wilde %q", got, want)
	}
}

func TestHeartbeatChanges(t *testing.T) {
	b, node := testBus(t)
	ctx := context.Background()
	if _, err := b.q.CreateAgent(ctx, store.CreateAgentParams{
		ID: uuid.New(), NodeID: node.ID, NkeyPublic: "UTEST", Version: "0.4.0", ProtocolVersion: 4,
	}); err != nil {
		t.Fatal(err)
	}
	hb := protocol.Heartbeat{AgentVersion: "0.4.0", ProtocolVersion: 4, Services: map[string]string{"nginx": "active", "keepalived": "active"}}
	if err := b.handleHeartbeat(ctx, node.ID, hb, 1500); err != nil {
		t.Fatal(err)
	}
	if st, _ := b.q.GetNodeStatus(ctx, node.ID); st.ClockOffsetMs != 1500 {
		t.Fatalf("klokafwijking: %d", st.ClockOffsetMs)
	}
	hb.Services = map[string]string{"nginx": "failed", "keepalived": "activating"}
	if err := b.handleHeartbeat(ctx, node.ID, hb, 0); err != nil {
		t.Fatal(err)
	}
	// Een nieuwe agent op protocol 5 meldt diensten zelf.
	hb = protocol.Heartbeat{AgentVersion: "0.5.0", ProtocolVersion: 5, Services: map[string]string{"nginx": "active", "keepalived": "active"}}
	if err := b.handleHeartbeat(ctx, node.ID, hb, 0); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range changesOf(t, b, node.ID) {
		got = append(got, r.Summary)
	}
	if want := []string{"nginx mislukt", "Agent bijgewerkt van 0.4.0 naar 0.5.0"}; !slices.Equal(got, want) {
		t.Fatalf("regels: %q", got)
	}
}

func TestFactsChangesFromServer(t *testing.T) {
	b, node := testBus(t)
	ctx := context.Background()
	if _, err := b.q.CreateAgent(ctx, store.CreateAgentParams{
		ID: uuid.New(), NodeID: node.ID, NkeyPublic: "UTEST", Version: "0.4.0", ProtocolVersion: 4,
	}); err != nil {
		t.Fatal(err)
	}
	boot := time.Now().Add(-48 * time.Hour).Truncate(time.Second)
	f := protocol.Facts{
		MachineID: "1", Hostname: "web-01", Kernel: "6.1.0-25-amd64", BootTime: boot,
		OS: protocol.OSInfo{PrettyName: "Debian GNU/Linux 12 (bookworm)"}, Upgrades: &protocol.Upgrades{Total: 4, Security: 1},
	}
	if err := b.handleFacts(ctx, node.ID, f); err != nil {
		t.Fatal(err)
	}
	f.Kernel, f.BootTime = "6.1.0-26-amd64", boot.Add(24*time.Hour)
	f.Upgrades = &protocol.Upgrades{Total: 0}
	if err := b.handleFacts(ctx, node.ID, f); err != nil {
		t.Fatal(err)
	}
	got := changesOf(t, b, node.ID)
	if len(got) != 2 {
		t.Fatalf("regels: %+v", got)
	}
	if got[0].Summary != "Herstart, kernel van 6.1.0-25-amd64 naar 6.1.0-26-amd64" || !got[0].TS.Equal(f.BootTime) {
		t.Errorf("herstart: %+v", got[0])
	}
	if got[1].Summary != "0 updates open (was 4)" || got[1].Detail["since"] == "" {
		t.Errorf("updates: %+v", got[1])
	}
}

func TestNoteReconnect(t *testing.T) {
	b, node := testBus(t)
	now := time.Now()
	b.noteReconnect(node.ID, now.Add(-10*time.Second))    // te kort
	b.noteReconnect(node.ID, b.started.Add(-time.Minute)) // voor de server startte
	b.noteReconnect(node.ID, time.Time{})                 // nooit gezien
	last := now.Add(-5*time.Minute - 30*time.Second)
	b.noteReconnect(node.ID, last)
	got := changesOf(t, b, node.ID)
	if len(got) != 1 {
		t.Fatalf("regels: %+v", got)
	}
	if !near(got[0].TS, last) || got[0].Summary != "Geen contact met de agent gedurende 5 min" || got[0].Source != "server" {
		t.Fatalf("onderbreking: %+v", got[0])
	}
}

func TestRecordCommand(t *testing.T) {
	b, node := testBus(t)
	jobID := uuid.New()
	ctx := events.WithJob(context.Background(), jobID)
	on := true
	cmd := protocol.Command{Action: protocol.CmdApply, Steps: []protocol.Step{
		{Package: &protocol.PackageStep{Names: []string{"nginx"}}},
		{File: &protocol.FileStep{Path: "/etc/nginx/nginx.conf", Content: "geheim"}},
		{File: &protocol.FileStep{Path: "/etc/nginx/ongewijzigd.conf"}},
		{Service: &protocol.ServiceStep{Name: "nginx", Enabled: &on}},
		{Command: &protocol.CommandStep{Run: "echo geheim", Creates: "/srv/.done"}},
	}}
	res := protocol.Result{OK: true, Steps: []protocol.StepResult{{Changed: true}, {Changed: true}, {Changed: false}, {Changed: true}, {Changed: true}}}
	b.recordCommand(ctx, node.ID, cmd, res)
	// Een mislukt commando schrijft niets.
	b.recordCommand(ctx, node.ID, protocol.Command{Action: protocol.CmdReboot}, protocol.Result{OK: false})
	b.recordCommand(ctx, node.ID, protocol.Command{Action: protocol.CmdMaintenanceEnter}, protocol.Result{OK: true})
	var got []string
	for _, r := range changesOf(t, b, node.ID) {
		if r.Source != "clusterforge" || r.JobID == nil || *r.JobID != jobID {
			t.Errorf("herkomst: %+v", r)
		}
		got = append(got, r.Summary)
	}
	want := []string{
		"nginx geïnstalleerd (ClusterForge)", "/etc/nginx/nginx.conf geschreven door ClusterForge",
		"nginx ingeschakeld door ClusterForge", "Commando uitgevoerd (maakt /srv/.done) door ClusterForge",
		"keepalived gestopt door ClusterForge (onderhoud)",
	}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("regels:\n %q\n wilde %q", got, want)
	}
}
