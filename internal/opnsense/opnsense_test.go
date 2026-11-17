package opnsense

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/jobs"
	"github.com/Jonasz1996/clusterforge/internal/secrets"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/internal/store/storetest"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

const aliasUUID = "0b6f8a4e-7c1d-4b8e-9f00-1234567890ab"

func TestCheck(t *testing.T) {
	p, err := check([]protocol.OPNCall{
		{Path: "firewall/alias/setItem/" + aliasUUID, Body: map[string]any{"alias": map[string]any{"name": "nas", "description": "NAS"}}},
		{Path: "firewall/alias/toggleItem/" + aliasUUID + "/0"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if p.Title() != "Alias nas wijzigen (+1)" || len(p.Apply) != 1 || p.Apply[0].Path != "firewall/alias/reconfigure" || p.Level != LevelNormal {
		t.Fatalf("plan: %+v", p)
	}
	for _, bad := range []protocol.OPNCall{
		{Path: "core/firmware/upgrade"},
		{Path: "firewall/alias/setItem/" + aliasUUID, Body: map[string]any{"alias": map[string]any{"name": "x", "evil": "1"}}},
		{Path: "firewall/alias/setItem/" + aliasUUID, Body: map[string]any{"other": map[string]any{}}},
		{Path: "firewall/alias/setItem/" + aliasUUID, Body: map[string]any{"alias": map[string]any{"name": map[string]any{"x": 1}}}},
		{Path: "firewall/alias/delItem/" + aliasUUID, Body: map[string]any{"alias": map[string]any{}}},
		{Path: "firewall/alias/setItem/../../x"},
	} {
		if _, err := check([]protocol.OPNCall{bad}); err == nil {
			t.Errorf("toegelaten: %+v", bad)
		}
	}
	p, err = check([]protocol.OPNCall{
		{Path: "firewall/alias/setItem/" + aliasUUID, Body: map[string]any{"alias": map[string]any{"name": "nas", "content": "192.168.10.50"}}},
		{Path: "firewall/filter/setRule/" + aliasUUID, Body: map[string]any{"rule": map[string]any{"action": "pass", "description": "NAS"}}},
		{Path: "firewall/d_nat/addRule", Body: map[string]any{"rule": map[string]any{"destination": map[string]any{"port": "443"}, "descr": "web"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if p.Level != LevelAccess || len(p.Apply) != 3 || p.Apply[0].Path != "firewall/alias/reconfigure" || p.Titles[1] != "firewallregel “NAS” wijzigen" {
		t.Fatalf("plan M2: %+v", p)
	}
	if _, err := check([]protocol.OPNCall{{Path: "firewall/d_nat/addRule", Body: map[string]any{"rule": map[string]any{"a": map[string]any{"b": map[string]any{"c": "1"}}}}}}); err == nil {
		t.Error("te diep genest toegelaten")
	}
	if readAllowed("auth/user/searchApiKey", true) || readAllowed("system/user/get", false) || !readAllowed("firewall/alias/searchItem", false) {
		t.Error("readAllowed")
	}
}

func fw(host, addr, role string, sync string) Firewall {
	f := &protocol.OPNsenseFacts{Hostname: host, Version: "26.1", GUI: "https:443", API: protocol.OPNsenseAPIState{OK: true}, CARP: protocol.OPNsenseCARP{Allow: true}}
	f.HA.SyncTo = sync
	f.HA.PfsyncPeer = sync
	if role != "" {
		st := map[string]string{"master": "MASTER", "backup": "BACKUP"}[role]
		f.VIPs = []protocol.OPNsenseVIP{{Interface: "lan", Address: "192.168.10.1", VHID: 1, Mode: "carp", State: st}}
	}
	f.Interfaces = []protocol.OPNsenseInterface{{Name: "lan", Addresses: []string{addr + "/24"}}}
	return Firewall{NodeID: uuid.New(), Hostname: host, Address: addr, Facts: f, Role: f.Role(), Online: true, Protocol: protocol.OPNsenseSince}
}

func TestClustersAndPreflight(t *testing.T) {
	a := fw("fw1", "192.168.10.2", "master", "192.168.10.3")
	b := fw("fw2", "192.168.10.3", "backup", "")
	single := fw("los", "192.168.20.1", "", "")
	cs := groupClusters([]Firewall{a, single, b})
	if len(cs) != 2 || len(cs[0].Nodes) != 2 || cs[0].Master == nil || *cs[0].Master != a.NodeID || cs[0].Level != "ok" {
		t.Fatalf("clusters: %+v", cs)
	}
	ck := preflight(cs[0])
	if !ck.OK || ck.Master.NodeID != a.NodeID || len(ck.Backup) != 1 {
		t.Fatalf("preflight: %+v", ck)
	}
	if t2 := probeTargets(cs[0]); strings.Join(t2, ",") != "192.168.10.2:443,192.168.10.1:443,192.168.10.3:443" {
		t.Errorf("proeven: %v", t2)
	}
	if ck := preflight(cs[1]); !ck.OK || ck.Master.Hostname != "los" {
		t.Errorf("losse firewall: %+v", ck)
	}

	// Split-brain blokkeert.
	b2 := fw("fw2", "192.168.10.3", "master", "")
	cs = groupClusters([]Firewall{a, b2})
	if cs[0].Level != "crit" || preflight(cs[0]).OK || !strings.Contains(preflight(cs[0]).Why, "split-brain") {
		t.Fatalf("split-brain: %+v", cs[0])
	}
	// Agent zonder sleutel blokkeert.
	b3 := fw("fw2", "192.168.10.3", "backup", "")
	b3.Facts.API = protocol.OPNsenseAPIState{Error: "401"}
	if ck := preflight(groupClusters([]Firewall{a, b3})[0]); ck.OK {
		t.Error("geen sleutel en toch ok")
	}
}

func TestParseGatewaysDown(t *testing.T) {
	list := `[{"name":"WAN_DHCP","status":"none"},{"name":"WAN2","status":"down"}]`
	obj := `{"WAN_DHCP":{"status":"none"},"VPN":{"status":"force_down"}}`
	if got := parseGatewaysDown(json.RawMessage(list)); strings.Join(got, ",") != "WAN2" {
		t.Errorf("lijst: %v", got)
	}
	if got := parseGatewaysDown(json.RawMessage(obj)); strings.Join(got, ",") != "VPN" {
		t.Errorf("object: %v", got)
	}
}

func TestStripVolatile(t *testing.T) {
	a := "<opnsense>\n  <revision>\n    <time>1</time>\n  </revision>\n  <system>\n    <hostname>fw1</hostname>\n    <x>1</x>\n  </system>\n</opnsense>"
	b := strings.ReplaceAll(strings.ReplaceAll(a, "fw1", "fw2"), "<time>1", "<time>2")
	if stripVolatile(a) != stripVolatile(b) {
		t.Error("revisie of hostname niet weg")
	}
	if d, n := textDiff(stripVolatile(a), strings.ReplaceAll(stripVolatile(b), "<x>1", "<x>2"), "a", "b"); n != 1 || !strings.Contains(d, "+    <x>2</x>") {
		t.Errorf("diff %d\n%s", n, d)
	}
}

// fakeFW speelt de agents van een paar OPNsense-firewalls na.
type fakeFW struct {
	mu      sync.Mutex
	facts   map[uuid.UUID]*protocol.OPNsenseFacts
	master  uuid.UUID
	backup  uuid.UUID
	actions []string
	pending string
	// afterApply loopt na het toepassen (bijvoorbeeld een proef laten falen).
	afterApply func()
	down       string
}

func (f *fakeFW) Run(_ context.Context, id uuid.UUID, req protocol.RunRequest) (protocol.RunResponse, error) {
	now := time.Now()
	exit := 0
	state := protocol.RunOK
	if req.Op != protocol.RunStart {
		return protocol.RunResponse{Status: &protocol.RunStatus{State: state, StartedAt: now, EndedAt: &now, Exit: &exit}}, nil
	}
	f.mu.Lock()
	op := req.Spec.PVE
	f.actions = append(f.actions, op.Action)
	var after func()
	switch op.Action {
	case protocol.OPNAPI:
		if id != f.master || !op.OPN.RequireMaster {
			state, exit = protocol.RunFailed, 1
		}
		f.pending = op.OPN.ID
		f.facts[id].Config.Time++
		after = f.afterApply
	case protocol.OPNConfirm, protocol.OPNRollback:
		if op.Change != f.pending {
			state, exit = protocol.RunFailed, 1
		}
		f.pending = ""
	case protocol.OPNHASync:
		f.facts[f.backup].Config.Time = f.facts[f.master].Config.Time + 100
	case protocol.OPNKey:
		if op.OPNKey.Key == "" {
			state, exit = protocol.RunFailed, 1
		}
	}
	f.mu.Unlock()
	if after != nil {
		after()
	}
	return protocol.RunResponse{Status: &protocol.RunStatus{State: state, StartedAt: now, EndedAt: &now, Exit: &exit}, Output: "ok\n", Offset: 3}, nil
}

func (f *fakeFW) PVE(_ context.Context, id uuid.UUID, req protocol.PVERequest) (protocol.PVEResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch req.Op {
	case protocol.OPNOpFacts:
		b, _ := json.Marshal(f.facts[id])
		return protocol.PVEResponse{Data: b}, nil
	case protocol.OPNOpConfigctl:
		b, _ := json.Marshal([]map[string]string{{"name": "WAN", "status": f.down}})
		return protocol.PVEResponse{Data: b}, nil
	}
	return protocol.PVEResponse{}, errors.New("onbekend " + req.Op)
}

func seedFW(t *testing.T, pool *pgxpool.Pool, cluster *uuid.UUID, f *protocol.OPNsenseFacts, ip string) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	var id uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO nodes (hostname, cluster_id, primary_ip) VALUES ($1, $2, $3) RETURNING id`, f.Hostname, cluster, ip).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO agents (id, node_id, nkey_public, protocol_version) VALUES (gen_random_uuid(), $1, $2, $3)`,
		id, "U"+strings.ToUpper(f.Hostname), protocol.Version); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO node_status (node_id, heartbeat_at, addresses) VALUES ($1, now(), '{}')`, id); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(protocol.Facts{Hostname: f.Hostname, OPNsense: f})
	if _, err := pool.Exec(ctx, `INSERT INTO node_facts (node_id, hash, facts, collected_at) VALUES ($1, 'x', $2, now())`, id, b); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestChangeFlow(t *testing.T) {
	pool := storetest.DB(t)
	ctx := context.Background()
	log := slog.New(slog.DiscardHandler)
	ev := events.NewWriter(store.New(pool), log)
	runner := jobs.NewRunner(pool, ev, log)
	runner.Poll = 50 * time.Millisecond
	rctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go runner.Run(rctx)

	a := fw("fw1", "192.168.10.2", "master", "192.168.10.3")
	b := fw("fw2", "192.168.10.3", "backup", "")
	var cl uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO clusters (slug, name, type, environment, template_name) VALUES ('fw', 'Firewalls', 'generic', 'prod', 'generic') RETURNING id`).Scan(&cl); err != nil {
		t.Fatal(err)
	}
	ida := seedFW(t, pool, &cl, a.Facts, a.Address)
	idb := seedFW(t, pool, &cl, b.Facts, b.Address)
	bus := &fakeFW{facts: map[uuid.UUID]*protocol.OPNsenseFacts{ida: a.Facts, idb: b.Facts}, master: ida, backup: idb, down: "none"}
	s := NewService(pool, ev, log, runner, bus)
	s.Run.Poll = 10 * time.Millisecond
	s.Wait = 0
	s.Settle = 500 * time.Millisecond
	var probeMu sync.Mutex
	failing := map[string]bool{}
	s.Probe = func(_ context.Context, addr string) error {
		probeMu.Lock()
		defer probeMu.Unlock()
		if failing[addr] {
			return errors.New("connection refused")
		}
		return nil
	}

	cs, err := s.Clusters(ctx)
	if err != nil || len(cs) != 1 || cs[0].Master == nil || *cs[0].Master != ida || cs[0].Environment != "prod" {
		t.Fatalf("clusters: %+v %v", cs, err)
	}

	wait := func(id uuid.UUID) store.Job {
		t.Helper()
		deadline := time.Now().Add(20 * time.Second)
		for {
			row, err := store.New(pool).GetJob(ctx, id)
			j := row.Job
			if err != nil {
				t.Fatal(err)
			}
			if j.Status == store.JobStatusSucceeded || j.Status == store.JobStatusFailed {
				return j
			}
			if time.Now().After(deadline) {
				t.Fatalf("taak blijft %s", j.Status)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	change := Change{Calls: []protocol.OPNCall{{Path: "firewall/alias/setItem/" + aliasUUID, Body: map[string]any{"alias": map[string]any{"description": "NAS"}}}}}

	// Gaat via de backup naar de master; bevestigd en gesynchroniseerd.
	j, err := s.StartChange(ctx, events.System(), idb, change)
	if err != nil {
		t.Fatal(err)
	}
	if j.NodeID == nil || *j.NodeID != ida {
		t.Fatalf("taak niet op de master: %+v", j)
	}
	if j = wait(j.ID); j.Status != store.JobStatusSucceeded {
		t.Fatalf("taak: %s %s", j.Status, j.Error)
	}
	bus.mu.Lock()
	got := strings.Join(bus.actions, ",")
	bus.actions = nil
	bus.mu.Unlock()
	if got != "opn_api,opn_confirm,opn_hasync" {
		t.Fatalf("acties: %s", got)
	}

	// Een proef faalt na het toepassen: terugdraaien, geen sync.
	bus.afterApply = func() { probeMu.Lock(); failing["192.168.10.1:443"] = true; probeMu.Unlock() }
	change.ConfirmWithin = 60
	j, err = s.StartChange(ctx, events.System(), ida, change)
	if err != nil {
		t.Fatal(err)
	}
	if j = wait(j.ID); j.Status != store.JobStatusFailed || !strings.Contains(j.Error, "192.168.10.1:443 antwoordt niet meer") {
		t.Fatalf("taak: %s %s", j.Status, j.Error)
	}
	bus.mu.Lock()
	got = strings.Join(bus.actions, ",")
	bus.mu.Unlock()
	if got != "opn_api,opn_rollback" {
		t.Fatalf("acties: %s", got)
	}

	// Een wijziging die niet mag, wordt geweigerd voor er iets gebeurt.
	if _, err := s.StartChange(ctx, events.System(), ida, Change{Calls: []protocol.OPNCall{{Path: "core/firmware/upgrade"}}}); err == nil {
		t.Fatal("firmware-upgrade toegelaten")
	}
	// Sync nu.
	bus.afterApply = nil
	j, err = s.StartSync(ctx, events.System(), ida)
	if err != nil {
		t.Fatal(err)
	}
	if j = wait(j.ID); j.Status != store.JobStatusSucceeded {
		t.Fatalf("sync: %s %v", j.Status, j.Error)
	}

	// Sleutel plakken: zonder masterkey geweigerd, met masterkey versleuteld.
	in := protocol.OPNKeyInput{Key: strings.Repeat("k", 80), Secret: strings.Repeat("s", 80)}
	if _, err := s.StartKey(ctx, events.System(), ida, in); err == nil {
		t.Fatal("sleutel zonder masterkey")
	}
	box, _ := secrets.New(make([]byte, 32))
	s.Box = box
	j, err = s.StartKey(ctx, events.System(), ida, in)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(j.Params), strings.Repeat("s", 80)) {
		t.Fatal("geheim in de taak")
	}
	if j = wait(j.ID); j.Status != store.JobStatusSucceeded {
		t.Fatalf("sleutel: %s %v", j.Status, j.Error)
	}
}
