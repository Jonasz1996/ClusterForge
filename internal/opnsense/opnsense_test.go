package opnsense

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	p, err = check([]protocol.OPNCall{
		{Path: "unbound/settings/addHostOverride", Body: map[string]any{"host": map[string]any{"hostname": "nas", "domain": "thuis", "server": "192.168.10.50"}}},
		{Path: "kea/dhcpv4/setReservation/" + aliasUUID, Body: map[string]any{"reservation": map[string]any{"hw_address": "aa:bb:cc:dd:ee:ff"}}},
	})
	if err != nil || p.Level != LevelNormal || len(p.Apply) != 2 || p.Titles[0] != "DNS-override nas toevoegen" {
		t.Fatalf("plan M3: %+v %v", p, err)
	}
	if p, err := check([]protocol.OPNCall{{Path: "interfaces/vip_settings/delItem/" + aliasUUID}}); err != nil || p.Level != LevelAccess {
		t.Fatalf("VIP verwijderen: %+v %v", p, err)
	}
	if _, err := check([]protocol.OPNCall{{Path: "interfaces/vip_settings/toggleItem/" + aliasUUID}}); err == nil {
		t.Error("toggleItem op VIP's bestaat niet en hoort geweigerd")
	}
	p, err = check([]protocol.OPNCall{{Path: "core/service/restart/unbound"}, {Path: "core/service/stop/openvpn/1"}})
	if err != nil || len(p.Apply) != 0 || p.Titles[0] != "dienst unbound herstarten" {
		t.Fatalf("plan M4: %+v %v", p, err)
	}
	for _, bad := range []string{"core/firmware/install/pkg", "core/firmware/upgrade", "core/service/restart/a;b", "wireguard/server/setServer/x"} {
		if _, err := check([]protocol.OPNCall{{Path: bad}}); err == nil {
			t.Errorf("toegelaten: %s", bad)
		}
	}
	if !IsSearch("diagnostics/firewall/query_states") || IsSearch("core/firmware/info") {
		t.Error("IsSearch")
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
	gotBody []map[string]any
	// afterApply loopt na het toepassen (bijvoorbeeld een proef laten falen).
	afterApply func()
	down       string
	// carpFail: de backup neemt niet over.
	carpFail bool
}

// setMaster maakt m MASTER voor de VIP en de andere BACKUP.
func (f *fakeFW) setMaster(m uuid.UUID) {
	for id, fa := range f.facts {
		for i := range fa.VIPs {
			if id == m {
				fa.VIPs[i].State = "MASTER"
			} else {
				fa.VIPs[i].State = "BACKUP"
			}
		}
	}
}

func (f *fakeFW) config(id uuid.UUID) string {
	extra := ""
	if id == f.backup {
		extra = "<nat><rule>oud</rule></nat>"
	}
	return fmt.Sprintf("<?xml version=\"1.0\"?>\n<opnsense>\n<system>\n<hostname>%s</hostname>\n</system>\n<filter><rule>1</rule></filter>%s\n<virtualip><vip><advskew>%d</advskew></vip></virtualip>\n<OPNsense><Firewall><a>1</a></Firewall><Hasync><x>%s</x></Hasync></OPNsense>\n</opnsense>\n",
		f.facts[id].Hostname, extra, len(extra), id)
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
		if op.OPN.RequireMaster != (id == f.master) {
			state, exit = protocol.RunFailed, 1
		}
		f.pending = op.OPN.ID
		for _, c := range op.OPN.Calls {
			f.gotBody = append(f.gotBody, c.Body)
		}
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
	case protocol.OPNCarp:
		on := *op.CARPMaintenance
		f.facts[id].CARP.Maintenance = on
		if !f.carpFail {
			m := f.master
			if on && id == f.master {
				m = f.backup
			}
			f.setMaster(m)
		}
	case protocol.OPNFirmware:
		if op.Firmware != "check" {
			f.facts[id].Version = "26.1.9"
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
	case protocol.OPNOpFilterLog:
		h := time.Now().UTC().Truncate(time.Hour)
		b, _ := json.Marshal(protocol.FilterLogResult{Lines: 7, Flows: []protocol.FilterFlow{
			{Hour: h, Action: "block", Interface: "vtnet1", Dir: "in", Proto: "tcp", Src: "192.168.90.20", Dst: "192.168.10.50", Port: 445, Rule: "r1", Count: 5},
			{Hour: h, Action: "pass", Interface: "vtnet1", Dir: "in", Proto: "tcp", Src: "192.168.10.2", Dst: "192.168.10.1", Port: 443, Rule: "r2", Count: 2},
		}})
		return protocol.PVEResponse{Data: b}, nil
	case protocol.OPNOpGet:
		if req.Path == "diagnostics/firewall/list_rule_ids" {
			return protocol.PVEResponse{Data: json.RawMessage(`{"r1":"Standaard blokkeren","r2":"LAN naar alles"}`)}, nil
		}
		switch req.Path {
		case "core/firmware/upgradestatus":
			return protocol.PVEResponse{Data: json.RawMessage(`{"status":"done"}`)}, nil
		case "core/firmware/status":
			return protocol.PVEResponse{Data: json.RawMessage(`{"status":"update","status_msg":"Er zijn 3 updates."}`)}, nil
		}
	case protocol.OPNOpPost:
		switch req.Path {
		case "firewall/alias/searchItem", "unbound/settings/searchHostOverride", "kea/dhcpv4/searchReservation":
			return protocol.PVEResponse{Data: json.RawMessage(`{"rows":[]}`)}, nil
		case "kea/dhcpv4/searchSubnet":
			return protocol.PVEResponse{Data: json.RawMessage(`{"rows":[{"uuid":"55555555-5555-4555-8555-555555555555","subnet":"192.168.10.0/24"}]}`)}, nil
		}
	case protocol.OPNOpBackup, protocol.OPNOpRevision:
		b, _ := json.Marshal(f.config(id))
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

	// Een plugin gaat op elke firewall apart, zonder sync.
	bus.mu.Lock()
	bus.actions = nil
	bus.mu.Unlock()
	j, err = s.StartChange(ctx, events.System(), ida, Change{Calls: []protocol.OPNCall{{Path: "core/firmware/install/os-crowdsec"}}})
	if err != nil {
		t.Fatal(err)
	}
	if j = wait(j.ID); j.Status != store.JobStatusSucceeded {
		t.Fatalf("plugin: %s %s", j.Status, j.Error)
	}
	bus.mu.Lock()
	got = strings.Join(bus.actions, ",")
	bus.mu.Unlock()
	if got != "opn_api,opn_confirm,opn_api,opn_confirm" {
		t.Fatalf("acties plugin: %s", got)
	}
	if _, err := s.StartChange(ctx, events.System(), ida, Change{Calls: []protocol.OPNCall{{Path: "core/firmware/install/os-crowdsec"}, {Path: "core/service/restart/unbound"}}}); err == nil {
		t.Fatal("plugin en dienst samen toegelaten")
	}

	// Een CARP-wachtwoord gaat versleuteld in de taak en komt aan bij de agent.
	vip := Change{Calls: []protocol.OPNCall{{Path: "interfaces/vip_settings/setItem/" + aliasUUID, Body: map[string]any{"vip": map[string]any{"subnet": "192.168.10.1", "password": "carp-geheim"}}}}, Confirm: "Firewalls"}
	j, err = s.StartChange(ctx, events.System(), ida, vip)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(j.Params), "carp-geheim") {
		t.Fatal("CARP-wachtwoord leesbaar in de taak")
	}
	if j = wait(j.ID); j.Status != store.JobStatusSucceeded {
		t.Fatalf("VIP: %s %s", j.Status, j.Error)
	}
	bus.mu.Lock()
	last := bus.gotBody[len(bus.gotBody)-1]
	bus.mu.Unlock()
	if last["vip"].(map[string]any)["password"] != "carp-geheim" {
		t.Fatalf("agent kreeg %v", last)
	}
	var payload string
	if err := pool.QueryRow(ctx, `SELECT payload::text FROM events WHERE action = 'opnsense.change_requested' ORDER BY id DESC LIMIT 1`).Scan(&payload); err != nil || !strings.Contains(payload, "192.168.10.1") {
		t.Fatalf("logboek: %v %s", err, payload)
	}
	if strings.Contains(payload, "carp-geheim") {
		t.Fatal("CARP-wachtwoord in het logboek")
	}

	// M5: na een wijziging staat er een back-up van elke firewall.
	list, err := s.Backups(ctx, ida)
	if err != nil || len(list) != 1 || list[0].Reason != "na wijziging" {
		t.Fatalf("back-ups: %v %+v", err, list)
	}
	var raw []byte
	if err := pool.QueryRow(ctx, `SELECT data FROM opnsense_backups WHERE id = $1`, list[0].ID).Scan(&raw); err != nil || strings.Contains(string(raw), "<opnsense>") {
		t.Fatalf("back-up niet versleuteld: %v", err)
	}
	name, data, err := s.OpenBackup(ctx, events.System(), list[0].ID)
	if err != nil || !strings.HasPrefix(name, "config-fw1-") || !strings.Contains(string(data), "<hostname>fw1</hostname>") {
		t.Fatalf("download: %v %s", err, name)
	}
	// Nog een keer met dezelfde inhoud geeft geen nieuwe rij.
	j, err = s.StartBackup(ctx, events.System(), ida)
	if err != nil {
		t.Fatal(err)
	}
	if j = wait(j.ID); j.Status != store.JobStatusSucceeded {
		t.Fatalf("back-up: %s %s", j.Status, j.Error)
	}
	if list, _ = s.Backups(ctx, ida); len(list) != 1 || list[0].Reason != "met de hand" {
		t.Fatalf("ongewijzigde back-up: %+v", list)
	}

	// Aandacht nodig: beide firewalls hebben een verse back-up.
	ns, err := s.Notices(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range ns {
		if strings.HasPrefix(n.ID, "opnsense-backup-") {
			t.Fatalf("back-up gemeld als oud: %+v", n)
		}
	}

	// Fase 40: het filterlog, twee keer gelezen, telt op en krijgt namen.
	for range 2 {
		if err := s.CollectFlows(ctx, ida); err != nil {
			t.Fatal(err)
		}
	}
	fv, err := s.Flows(ctx, idb, FlowQuery{Action: "block"})
	if err != nil || len(fv.Rows) != 1 || fv.Rows[0].Count != 10 || fv.Rows[0].RuleName != "Standaard blokkeren" || fv.Rows[0].DstName != "lan-net" || fv.Blocked != 10 || fv.Passed != 0 {
		t.Fatalf("stromen: %v %+v", err, fv)
	}
	if fv, _ = s.Flows(ctx, ida, FlowQuery{Q: "fw1"}); len(fv.Rows) != 1 || fv.Rows[0].Port != 443 || fv.Rows[0].SrcName == "" || len(fv.Rules) != 2 {
		t.Fatalf("zoeken: %+v", fv)
	}

	// Fase 40 M2: automatisch beheer van aliassen, DNS en DHCP.
	var db1 uuid.UUID
	nf, _ := json.Marshal(protocol.Facts{Hostname: "db1", Interfaces: []protocol.Interface{{Name: "eth0", MAC: "BC:24:11:AA:BB:01", Addresses: []string{"192.168.10.191/24"}}}})
	if err := pool.QueryRow(ctx, `INSERT INTO nodes (hostname, cluster_id, primary_ip) VALUES ('db1', $1, '192.168.10.191') RETURNING id`, cl).Scan(&db1); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO node_facts (node_id, hash, facts, collected_at) VALUES ($1, 'y', $2, now())`, db1, nf); err != nil {
		t.Fatal(err)
	}
	av, err := s.Auto(ctx, ida)
	if err != nil || av.Settings.DNS != ModePreview || av.Domain != "lan.jbogaert.be" || av.Pending != 4 {
		t.Fatalf("automatisch: %v %+v", err, av)
	}
	if _, err := s.SetAuto(ctx, events.System(), nil, ida, AutoSettings{Aliases: ModeOff, DNS: ModeOff, DHCP: ModeManaged, Domain: "lan.test"}); err != nil {
		t.Fatal(err)
	}
	if to, _ := s.AutoChangesToManaged(ctx, ida, AutoSettings{Aliases: ModeOff, DNS: ModeOff, DHCP: ModeManaged}); to {
		t.Fatal("DHCP was al beheerd")
	}
	bus.mu.Lock()
	bus.gotBody = nil
	bus.mu.Unlock()
	s.RunAuto(ctx)
	var lastJob *uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT last_job FROM opnsense_auto`).Scan(&lastJob); err != nil || lastJob == nil {
		t.Fatalf("geen automatische wijziging: %v", err)
	}
	if j := wait(*lastJob); j.Status != store.JobStatusSucceeded {
		t.Fatalf("automatisch toepassen: %s %s", j.Status, j.Error)
	}
	bus.mu.Lock()
	if len(bus.gotBody) != 1 || fmt.Sprint(bus.gotBody[0]["reservation"]) != "map[description:cf: node db1 hostname:db1 hw_address:bc:24:11:aa:bb:01 ip_address:192.168.10.191 subnet:55555555-5555-4555-8555-555555555555]" {
		t.Fatalf("lichaam: %v", bus.gotBody)
	}
	bus.mu.Unlock()
	// M3: regeladvies; de nep-firewall kent searchRule niet.
	adv, err := s.Advice(ctx, ida)
	if err != nil || adv.Confirm != "Firewalls" || len(adv.Notes) == 0 {
		t.Fatalf("advies: %v %+v", err, adv)
	}
	if err := s.IgnoreAdvice(ctx, events.System(), ida, "open:x", true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyAdvice(ctx, events.System(), ida, []string{"open:x"}, "Firewalls"); err == nil {
		t.Fatal("een voorstel dat niet bestaat toegepast")
	}
	// M4: velden zonder API alleen op de gekozen firewall, zonder sync.
	leg := []protocol.OPNLegacyEdit{{Section: "system", Field: "hostname", Value: "fw2b"}}
	if _, err := s.StartChange(ctx, events.System(), ida, Change{Legacy: leg, Node: &idb}); err == nil {
		t.Fatal("velden zonder overtikken")
	}
	bus.mu.Lock()
	bus.actions = nil
	bus.mu.Unlock()
	j, err = s.StartChange(ctx, events.System(), ida, Change{Legacy: leg, Node: &idb, Confirm: "Firewalls"})
	if err != nil {
		t.Fatal(err)
	}
	if j = wait(j.ID); j.Status != store.JobStatusSucceeded {
		t.Fatalf("velden: %s %s", j.Status, j.Error)
	}
	bus.mu.Lock()
	if strings.Join(bus.actions, ",") != "opn_api,opn_confirm" {
		t.Fatalf("velden: %v", bus.actions)
	}
	// De nep-sync rekent met de revisietijd van de master; zet die van de
	// backup terug.
	bus.facts[idb].Config.Time--
	bus.actions = nil
	bus.mu.Unlock()
	if _, err := pool.Exec(ctx, `DELETE FROM nodes WHERE id = $1`, db1); err != nil {
		t.Fatal(err)
	}

	// Drift: de backup heeft een NAT-regel meer; hostnaam, skew en Hasync
	// tellen niet mee.
	d, err := s.Drift(ctx, ida)
	if err != nil || len(d.Rows) != 1 || strings.Join(d.Rows[0].Sections, ",") != "nat" {
		t.Fatalf("drift: %v %+v", err, d)
	}

	// Een revisie terugzetten vraagt het overtikken en gaat als wijziging.
	if _, err := s.StartChange(ctx, events.System(), ida, Change{Restore: "config-1763340000.5.xml"}); err == nil {
		t.Fatal("terugzetten zonder overtikken")
	}
	if _, err := s.StartChange(ctx, events.System(), ida, Change{Restore: "../config.xml", Confirm: "Firewalls"}); err == nil {
		t.Fatal("ongeldige revisie toegelaten")
	}
	j, err = s.StartChange(ctx, events.System(), ida, Change{Restore: "config-1763340000.5.xml", Confirm: "Firewalls"})
	if err != nil {
		t.Fatal(err)
	}
	if j = wait(j.ID); j.Status != store.JobStatusSucceeded {
		t.Fatalf("terugzetten: %s %s", j.Status, j.Error)
	}

	// Failovertest: de backup neemt over en geeft terug.
	s.Poll = 10 * time.Millisecond
	s.Takeover = 300 * time.Millisecond
	if _, err := s.StartCarp(ctx, events.System(), ida, CarpInput{Action: CarpTest}); err == nil {
		t.Fatal("failovertest zonder overtikken")
	}
	bus.mu.Lock()
	bus.actions = nil
	bus.mu.Unlock()
	j, err = s.StartCarp(ctx, events.System(), idb, CarpInput{Action: CarpTest, Confirm: "Firewalls"})
	if err != nil {
		t.Fatal(err)
	}
	if j = wait(j.ID); j.Status != store.JobStatusSucceeded {
		t.Fatalf("failovertest: %s %s", j.Status, j.Error)
	}
	bus.mu.Lock()
	if strings.Join(bus.actions, ",") != "opn_carp,opn_carp" || bus.facts[ida].Role() != "master" || bus.facts[ida].CARP.Maintenance {
		t.Fatalf("failovertest: %v", bus.actions)
	}
	// De backup neemt niet over: de onderhoudsmodus gaat vanzelf weer uit.
	bus.carpFail = true
	bus.mu.Unlock()
	j, err = s.StartCarp(ctx, events.System(), ida, CarpInput{Action: CarpOn, Confirm: "Firewalls"})
	if err != nil {
		t.Fatal(err)
	}
	if j = wait(j.ID); j.Status != store.JobStatusFailed || !strings.Contains(j.Error, "staat weer uit") {
		t.Fatalf("mislukte overname: %s %s", j.Status, j.Error)
	}
	bus.mu.Lock()
	if bus.facts[ida].CARP.Maintenance {
		t.Fatal("onderhoudsmodus bleef aan")
	}
	bus.carpFail = false
	bus.actions = nil
	bus.mu.Unlock()

	// Firmware: eerst de backup, dan de master in onderhoudsmodus.
	if _, err := s.StartFirmware(ctx, events.System(), ida, FirmwareInput{Mode: "update"}); err == nil {
		t.Fatal("update zonder overtikken")
	}
	j, err = s.StartFirmware(ctx, events.System(), ida, FirmwareInput{Mode: "update", Confirm: "Firewalls"})
	if err != nil {
		t.Fatal(err)
	}
	if j = wait(j.ID); j.Status != store.JobStatusSucceeded {
		t.Fatalf("firmware: %s %s", j.Status, j.Error)
	}
	bus.mu.Lock()
	defer bus.mu.Unlock()
	if strings.Join(bus.actions, ",") != "opn_firmware,opn_carp,opn_firmware,opn_carp" || bus.facts[ida].Version != "26.1.9" || bus.facts[idb].Version != "26.1.9" || bus.facts[ida].Role() != "master" {
		t.Fatalf("firmware: %v", bus.actions)
	}
}
