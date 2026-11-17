package zabbixmon

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/internal/store/storetest"
	"github.com/Jonasz1996/clusterforge/internal/zabbixagent"
)

// fakeZabbix bootst api_jsonrpc.php na met antwoorden zoals Zabbix 8.0 ze
// geeft (getallen als tekst).
type fakeZabbix struct {
	mu      sync.Mutex
	calls   map[string]int
	failHA  bool
	token   string
	answers map[string]string
}

func newFake() *fakeZabbix {
	return &fakeZabbix{calls: map[string]int{}, failHA: true, token: "tok", answers: map[string]string{
		"apiinfo.version": `"8.0.1"`,
		"host.get": `[
			{"hostid":"10084","host":"zabbix-server-01","name":"Zabbix server","status":"0","maintenance_status":"0","active_available":"1","monitored_by":"0","proxyid":"0",
			 "interfaces":[{"interfaceid":"1","ip":"127.0.0.1","dns":"","type":"1","main":"1","available":"1","error":""}],"hostgroups":[{"name":"Zabbix servers"}]},
			{"hostid":"10101","host":"cf-db1","name":"db1","status":"0","maintenance_status":"0","active_available":"0","monitored_by":"1","proxyid":"7",
			 "interfaces":[{"interfaceid":"2","ip":"192.168.10.191","dns":"","type":"1","main":"1","available":"2","error":"Get value from agent failed: cannot connect"}],"hostgroups":[{"name":"ClusterForge"}]},
			{"hostid":"10102","host":"Database Node2","name":"Database Node2","status":"0","maintenance_status":"1","active_available":"0","monitored_by":"1","proxyid":"7",
			 "interfaces":[{"interfaceid":"3","ip":"192.168.10.192","dns":"","type":"1","main":"1","available":"1","error":""}],"hostgroups":[]},
			{"hostid":"10103","host":"opnsense","name":"OPNsense","status":"0","maintenance_status":"0","active_available":"0","monitored_by":"0","proxyid":"0",
			 "interfaces":[{"interfaceid":"4","ip":"192.168.199.2","dns":"","type":"2","main":"1","available":"0","error":""}],"hostgroups":[]},
			{"hostid":"10104","host":"oud","name":"oud","status":"1","maintenance_status":"0","active_available":"0","monitored_by":"0","proxyid":"0","interfaces":[],"hostgroups":[]}
		]`,
		"problem.get": `[
			{"eventid":"501","objectid":"9001","clock":"1763340000","name":"Zabbix agent is not available","severity":"3","acknowledged":"0","suppressed":"0","opdata":"","tags":[{"tag":"scope","value":"availability"}]},
			{"eventid":"502","objectid":"9002","clock":"1763341000","name":"High CPU","severity":"4","acknowledged":"1","suppressed":"0","opdata":"95 %","tags":[]},
			{"eventid":"503","objectid":"9003","clock":"1763342000","name":"Onderhoud","severity":"5","acknowledged":"0","suppressed":"1","opdata":"","tags":[]}
		]`,
		"trigger.get": `[
			{"triggerid":"9001","hosts":[{"hostid":"10101"}]},
			{"triggerid":"9002","hosts":[{"hostid":"10101"}]},
			{"triggerid":"9003","hosts":[{"hostid":"10102"}]}
		]`,
		"proxy.get": `[{"proxyid":"7","name":"zabbix-proxy-01","operating_mode":"0","lastaccess":"1763342100","version":"80001","compatibility":"1","state":"2"}]`,
		"hanode.get": `[
			{"name":"zabbix-server-01","address":"192.168.199.11","port":"10051","lastaccess":"1763342100","status":"3"},
			{"name":"zabbix-server-02","address":"192.168.199.12","port":"10051","lastaccess":"1763342100","status":"0"}
		]`,
		"item.get": `[
			{"itemid":"1","hostid":"10084","name":"Required performance","key_":"zabbix[requiredperformance]","lastvalue":"42.5","lastclock":"1763342100","units":"","state":"0"},
			{"itemid":"2","hostid":"10084","name":"Queue","key_":"zabbix[queue]","lastvalue":"3","lastclock":"1763342100","units":"","state":"0"},
			{"itemid":"3","hostid":"10084","name":"Queue 10m","key_":"zabbix[queue,10m]","lastvalue":"0","lastclock":"1763342100","units":"","state":"0"},
			{"itemid":"4","hostid":"10084","name":"History syncer busy","key_":"zabbix[process,history syncer,avg,busy]","lastvalue":"12.5","lastclock":"1763342100","units":"%","state":"0"},
			{"itemid":"5","hostid":"10084","name":"Poller busy","key_":"zabbix[process,poller,avg,busy]","lastvalue":"30","lastclock":"1763342100","units":"%","state":"0"},
			{"itemid":"6","hostid":"10084","name":"Iets anders","key_":"zabbix[host,,items]","lastvalue":"30","lastclock":"1763342100","units":"","state":"0"}
		]`,
	}}
}

func (f *fakeZabbix) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Method string `json:"method"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls[req.Method]++
	if req.Method != "apiinfo.version" && r.Header.Get("Authorization") != "Bearer "+f.token {
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","error":{"code":-32602,"message":"Invalid params.","data":"Not authorized."},"id":1}`))
		return
	}
	if req.Method == "hanode.get" && f.failHA {
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","error":{"code":-32500,"message":"Application error.","data":"No permissions to call \"hanode.get\"."},"id":1}`))
		return
	}
	a, ok := f.answers[req.Method]
	if !ok {
		a = "[]"
	}
	_, _ = w.Write([]byte(`{"jsonrpc":"2.0","result":` + a + `,"id":1}`))
}

func nodes() []CFNode {
	c := uuid.New()
	return []CFNode{
		{ID: uuid.New(), Hostname: "DatabaseClusterNode1", IP: "192.168.10.191", ClusterID: &c, ClusterName: "databasecluster", Lifecycle: "active", ZabbixHostID: "10101"},
		{ID: uuid.New(), Hostname: "database-node2.lan", IP: "192.168.10.250", ClusterID: &c, ClusterName: "databasecluster", Lifecycle: "active"},
		{ID: uuid.New(), Hostname: "nc1", IP: "192.168.10.186", Lifecycle: "active"},
		{ID: uuid.New(), Hostname: "weg", IP: "192.168.10.9", Lifecycle: "decommissioned"},
	}
}

func TestCollect(t *testing.T) {
	f := newFake()
	srv := httptest.NewServer(f)
	defer srv.Close()
	api := &zabbixagent.Client{URL: srv.URL, Token: "tok"}
	ns := nodes()
	// De tweede node heeft een ander IP en een andere naam: alleen een
	// handmatige koppeling vindt hem.
	links := map[string]*uuid.UUID{"10102": &ns[1].ID, "10103": nil}
	snap, err := Collect(context.Background(), api, ns, links, time.Unix(1763342200, 0))
	if err != nil {
		t.Fatal(err)
	}
	if snap.Version != "8.0.1" {
		t.Errorf("versie %q", snap.Version)
	}
	c := snap.Counts
	if c.High != 1 || c.Average != 1 || c.Disaster != 0 || c.Suppressed != 1 || c.Unacked != 1 {
		t.Errorf("problemen: %+v", c)
	}
	if c.Hosts != 4 || c.HostsDisabled != 1 || c.HostsDown != 1 || c.HostsOK != 2 || c.HostsUnknown != 1 || c.HostsMaintenance != 1 {
		t.Errorf("hosts: %+v", c)
	}
	if c.Nodes != 3 || c.Watched != 2 || len(snap.Unwatched) != 1 || snap.Unwatched[0].Hostname != "nc1" {
		t.Errorf("dekking: %+v %+v", c, snap.Unwatched)
	}
	byID := map[string]Host{}
	for _, h := range snap.Hosts {
		byID[h.HostID] = h
	}
	if h := byID["10101"]; h.Node == nil || h.Node.Source != "agent" || h.Node.Hostname != "DatabaseClusterNode1" || h.Problems != 2 || h.Worst != SevHigh || h.Proxy != "zabbix-proxy-01" || h.Available != AvailDown {
		t.Errorf("db1: %+v %+v", h, h.Node)
	}
	if h := byID["10102"]; h.Node == nil || h.Node.Source != "manual" || h.Problems != 0 || !h.Maintenance {
		t.Errorf("node2: %+v", h)
	}
	if h := byID["10103"]; h.Node != nil || h.Available != AvailUnknown || h.Interfaces[0].Type != "snmp" {
		t.Errorf("opnsense hoort bewust bij geen node: %+v", h)
	}
	if h := byID["10104"]; h.Enabled || h.Available != AvailDisabled {
		t.Errorf("uitgeschakeld: %+v", h)
	}
	if len(snap.Problems) != 3 || snap.Problems[0].Severity != SevDisaster || snap.Problems[1].Name != "High CPU" || snap.Problems[1].Hosts[0].Node == nil {
		t.Errorf("volgorde of hosts van de problemen: %+v", snap.Problems)
	}
	if len(snap.Proxies) != 1 || snap.Proxies[0].State != "online" || snap.Proxies[0].Version != "8.0.1" || snap.Proxies[0].Hosts != 2 || snap.Proxies[0].Mode != "active" {
		t.Errorf("proxy: %+v", snap.Proxies)
	}
	if len(snap.HA) != 0 || !strings.Contains(snap.HAError, "geen rechten") {
		t.Errorf("HA zonder rechten: %+v %q", snap.HA, snap.HAError)
	}
	if len(snap.Health) != 1 {
		t.Fatalf("gezondheid: %+v", snap.Health)
	}
	h := snap.Health[0]
	if h.Name != "Zabbix server" || h.NVPS == nil || *h.NVPS != 42.5 || h.Queue == nil || *h.Queue != 3 || len(h.Busiest) != 2 || h.Busiest[0].Name != "poller" || h.Busiest[1].Name != "history syncer" {
		t.Errorf("gezondheid: %+v", h)
	}

	st := Evaluate(&snap, "", snap.At, snap.At)
	if st.Level != LevelProblem || !strings.Contains(strings.Join(st.Reasons, "|"), "ernst hoog") || !strings.Contains(strings.Join(st.Reasons, "|"), "onbereikbaar voor Zabbix") {
		t.Errorf("toestand: %+v", st)
	}

	// Met rechten voor hanode.get: twee nodes, één actief.
	f.mu.Lock()
	f.failHA = false
	f.mu.Unlock()
	snap, err = Collect(context.Background(), api, ns, links, time.Unix(1763342200, 0))
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.HA) != 2 || snap.HA[0].Status != "active" || snap.HA[0].Address != "192.168.199.11:10051" || snap.HAError != "" {
		t.Errorf("HA: %+v %q", snap.HA, snap.HAError)
	}

	// Fout token: de ronde faalt.
	if _, err := Collect(context.Background(), &zabbixagent.Client{URL: srv.URL, Token: "fout"}, ns, links, time.Now()); err == nil {
		t.Error("fout token werd aanvaard")
	}
}

func TestMatchByNameAndIP(t *testing.T) {
	a, b := uuid.New(), uuid.New()
	ns := []CFNode{{ID: a, Hostname: "web01.lan", IP: "10.0.0.1"}, {ID: b, Hostname: "web02", IP: "10.0.0.2"}}
	hosts := []rawHost{
		{HostID: "1", Host: "WEB01", Name: "Web 1"},
		{HostID: "2", Host: "iets", Name: "iets", Interfaces: []rawInterface{{IP: "10.0.0.2"}}},
		{HostID: "3", Host: "web02", Name: "web02"},
	}
	m := match(ns, nil, hosts)
	if m["1"] == nil || m["1"].NodeID != a || m["1"].Source != "name" {
		t.Errorf("naam: %+v", m["1"])
	}
	// Naam gaat voor IP: host 3 krijgt web02, host 2 dus niet meer.
	if m["3"] == nil || m["3"].NodeID != b || m["2"] != nil {
		t.Errorf("één host per node: %+v %+v", m["2"], m["3"])
	}
}

func TestEvaluate(t *testing.T) {
	now := time.Now()
	if st := Evaluate(nil, "", time.Time{}, now); st.Level != LevelUnknown {
		t.Errorf("zonder gegevens: %+v", st)
	}
	snap := &Snapshot{Counts: Counts{Disaster: 1}, HA: []HANode{{Name: "a", Status: "standby"}}}
	st := Evaluate(snap, "", now, now)
	if st.Level != LevelCritical || len(st.Reasons) != 2 {
		t.Errorf("kritiek: %+v", st)
	}
	snap = &Snapshot{}
	if st := Evaluate(snap, "time-out", now.Add(-10*time.Minute), now); st.Level != LevelProblem {
		t.Errorf("API faalt lang: %+v", st)
	}
	if st := Evaluate(snap, "time-out", now.Add(-time.Minute), now); st.Level != LevelNormal {
		t.Errorf("één mislukte ronde is nog geen probleem: %+v", st)
	}
	snap = &Snapshot{Unwatched: []NodeRef{{Hostname: "x"}}}
	if st := Evaluate(snap, "", now, now); st.Level != LevelWarning {
		t.Errorf("ongedekt: %+v", st)
	}
}

func TestFrontendURL(t *testing.T) {
	for in, want := range map[string]string{
		"http://192.168.199.50/zabbix/api_jsonrpc.php": "http://192.168.199.50/zabbix/",
		"https://zbx.lan/api_jsonrpc.php":              "https://zbx.lan/",
		"":                                             "",
	} {
		if got := FrontendURL(in); got != want {
			t.Errorf("%q: %q", in, got)
		}
	}
}

func TestService(t *testing.T) {
	pool := storetest.DB(t)
	ctx := context.Background()
	f := newFake()
	srv := httptest.NewServer(f)
	defer srv.Close()

	var nodeID uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO nodes (hostname, primary_ip) VALUES ('db1', '192.168.10.191') RETURNING id`).Scan(&nodeID); err != nil {
		t.Fatal(err)
	}
	s := NewService(pool, events.NewWriter(store.New(pool), nil), nil, nil)
	configured := false
	s.Client = func(context.Context) (API, string, error) {
		if !configured {
			return nil, "", zabbixagent.ErrNotConfigured
		}
		return &zabbixagent.Client{URL: srv.URL, Token: "tok"}, srv.URL + "/zabbix/api_jsonrpc.php", nil
	}
	now := time.Date(2026, 11, 17, 4, 7, 0, 0, time.UTC)
	s.Now = func() time.Time { return now }

	if err := s.Tick(ctx); !errors.Is(err, zabbixagent.ErrNotConfigured) {
		t.Fatalf("zonder instellingen: %v", err)
	}
	o, err := s.Overview(ctx, "24h")
	if err != nil || o.Configured || o.State.Level != LevelUnknown || o.Hosts == nil {
		t.Fatalf("niet ingesteld: %+v %v", o, err)
	}

	configured = true
	if err := s.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	o, err = s.Overview(ctx, "24h")
	if err != nil {
		t.Fatal(err)
	}
	if !o.Configured || o.FrontendURL != srv.URL+"/zabbix/" || len(o.Trend) != 1 || o.Trend[0].High != 1 || o.Trend[0].NVPS == nil || o.UpdatedAt == nil {
		t.Fatalf("overzicht: %+v", o)
	}
	var linked *NodeLink
	for _, h := range o.Hosts {
		if h.HostID == "10101" {
			linked = h.Node
		}
	}
	if linked == nil || linked.NodeID != nodeID || linked.Source != "name" {
		t.Errorf("db1 op naam (cf-db1 → db1 lukt niet, het IP wel): %+v", linked)
	}

	// Handmatig: db1 hoort bij de OPNsense-host (onzin, maar het moet kunnen).
	if err := s.SetLink(ctx, events.System(), nil, "10103", &nodeID); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	if err := s.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	o, _ = s.Overview(ctx, "24h")
	for _, h := range o.Hosts {
		if h.HostID == "10103" && (h.Node == nil || h.Node.Source != "manual") {
			t.Errorf("handmatig: %+v", h.Node)
		}
		if h.HostID == "10101" && h.Node != nil {
			t.Errorf("de node hoort nu bij een andere host: %+v", h.Node)
		}
	}
	if len(o.Trend) != 1 {
		t.Errorf("zelfde kwartier = één punt: %d", len(o.Trend))
	}
	if err := s.DeleteLink(ctx, events.System(), "10103"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteLink(ctx, events.System(), "10103"); !errors.Is(err, ErrNotFound) {
		t.Errorf("twee keer wissen: %v", err)
	}
	if err := s.SetLink(ctx, events.System(), nil, "10103", ptr(uuid.New())); !errors.Is(err, ErrNotFound) {
		t.Errorf("onbekende node: %v", err)
	}

	// Zabbix weg: de laatste stand blijft, met de fout erbij.
	srv.Close()
	now = now.Add(time.Minute)
	if err := s.Tick(ctx); err == nil {
		t.Fatal("ronde zonder Zabbix lukte")
	}
	o, _ = s.Overview(ctx, "24h")
	if o.Error == "" || len(o.Hosts) == 0 {
		t.Errorf("laatste stand weg of geen fout: %q %d", o.Error, len(o.Hosts))
	}
}

func ptr[T any](v T) *T { return &v }

// paramFake geeft antwoorden op basis van methode en parameters.
type paramFake struct {
	mu    sync.Mutex
	calls []string
	fn    func(method string, params map[string]any) string
}

func (f *paramFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Method string         `json:"method"`
		Params map[string]any `json:"params"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	f.mu.Lock()
	f.calls = append(f.calls, req.Method)
	f.mu.Unlock()
	_, _ = w.Write([]byte(`{"jsonrpc":"2.0","result":` + f.fn(req.Method, req.Params) + `,"id":1}`))
}

func TestItemsAndSeries(t *testing.T) {
	now := time.Unix(1763350000, 0)
	var histParams map[string]any
	f := &paramFake{fn: func(m string, p map[string]any) string {
		switch m {
		case "item.get":
			if _, ok := p["itemids"]; ok {
				id := p["itemids"].([]any)[0].(string)
				switch id {
				case "1":
					return `[{"itemid":"1","hostid":"10101","name":"CPU utilization","key_":"system.cpu.util","value_type":"0","units":"%"}]`
				case "2":
					return `[{"itemid":"2","hostid":"10101","name":"OS","key_":"system.sw.os","value_type":"4","units":""}]`
				case "3":
					return `[{"itemid":"3","hostid":"99999","name":"vreemd","key_":"x","value_type":"0","units":""}]`
				}
				return `[]`
			}
			return `[
				{"itemid":"1","hostid":"10101","name":"CPU utilization","key_":"system.cpu.util","value_type":"0","units":"%","lastvalue":"12.5","prevvalue":"11","lastclock":"1763349990","state":"0","error":"","tags":[{"tag":"component","value":"cpu"}]},
				{"itemid":"2","hostid":"10101","name":"OS","key_":"system.sw.os","value_type":"4","units":"","lastvalue":"Debian 13","prevvalue":"","lastclock":"1763349000","state":"0","error":"","tags":[]},
				{"itemid":"4","hostid":"10101","name":"Kapot","key_":"x.y","value_type":"3","units":"","lastvalue":"","prevvalue":"","lastclock":"0","state":"1","error":"Unsupported item key.","tags":[]}
			]`
		case "history.get":
			histParams = p
			// Drie metingen in het eerste stukje, één in het laatste.
			s := now.Add(-time.Hour).Unix()
			return `[{"clock":"` + strconv.FormatInt(s+1, 10) + `","value":"10"},{"clock":"` + strconv.FormatInt(s+2, 10) + `","value":"20"},{"clock":"` + strconv.FormatInt(s+3, 10) + `","value":"30"},{"clock":"` + strconv.FormatInt(now.Unix()-1, 10) + `","value":"5"}]`
		case "trend.get":
			return `[{"clock":"` + strconv.FormatInt(now.Add(-48*time.Hour).Unix(), 10) + `","value_min":"1","value_avg":"2","value_max":"9"}]`
		}
		return `[]`
	}}
	srv := httptest.NewServer(f)
	defer srv.Close()
	s := &Service{Now: func() time.Time { return now }}
	s.Client = func(context.Context) (API, string, error) {
		return &zabbixagent.Client{URL: srv.URL, Token: "t"}, srv.URL, nil
	}
	s.snap = &Snapshot{Hosts: []Host{{HostID: "10101"}}}

	if _, err := s.Items(context.Background(), "555"); !errors.Is(err, ErrNotFound) {
		t.Errorf("onbekende host: %v", err)
	}
	hi, err := s.Items(context.Background(), "10101")
	if err != nil {
		t.Fatal(err)
	}
	if len(hi.Items) != 3 || !hi.Items[0].Numeric || hi.Items[0].Component != "cpu" || hi.Items[1].Numeric || hi.Items[2].Error != "Unsupported item key." || hi.Items[2].LastClock != nil {
		t.Errorf("items: %+v", hi.Items)
	}
	n := len(f.calls)
	if _, err := s.Items(context.Background(), "10101"); err != nil || len(f.calls) != n {
		t.Error("tweede vraag binnen 30 s moet uit de cache komen")
	}

	se, err := s.ItemSeries(context.Background(), "1", "1h")
	if err != nil {
		t.Fatal(err)
	}
	if se.Source != "history" || se.Units != "%" || len(se.Timestamps) != len(se.Avg) || (len(se.Avg) != SeriesPoints && len(se.Avg) != SeriesPoints+1) {
		t.Fatalf("reeks: %+v", se)
	}
	if se.Avg[0] == nil || *se.Avg[0] != 20 || *se.Min[0] != 10 || *se.Max[0] != 30 || se.Avg[1] != nil {
		t.Errorf("eerste stukje: %v %v %v, tweede %v", se.Avg[0], se.Min[0], se.Max[0], se.Avg[1])
	}
	if last := se.Avg[len(se.Avg)-1]; last == nil || *last != 5 {
		t.Errorf("laatste stukje: %v", last)
	}
	if histParams["history"].(float64) != 0 {
		t.Errorf("history-type: %v", histParams["history"])
	}
	se, err = s.ItemSeries(context.Background(), "1", "7d")
	if err != nil || se.Source != "trends" {
		t.Fatalf("trends: %+v %v", se, err)
	}
	found := false
	for i := range se.Avg {
		if se.Avg[i] != nil && *se.Avg[i] == 2 && *se.Max[i] == 9 {
			found = true
		}
	}
	if !found {
		t.Error("trendpunt niet gevonden")
	}
	if _, err := s.ItemSeries(context.Background(), "2", "1h"); !errors.Is(err, ErrNotNumeric) {
		t.Errorf("tekst: %v", err)
	}
	if _, err := s.ItemSeries(context.Background(), "3", "1h"); !errors.Is(err, ErrNotFound) {
		t.Errorf("item van een onbekende host: %v", err)
	}
	if _, err := s.ItemSeries(context.Background(), "1;drop", "1h"); !errors.Is(err, ErrNotFound) {
		t.Errorf("ongeldig itemid: %v", err)
	}
}

func TestSummarize(t *testing.T) {
	loc := brussels()
	now := time.Date(2026, 11, 17, 5, 30, 0, 0, loc) // dinsdag
	from := now.Add(-24 * time.Hour)
	at := func(h, m int) zint { return zint(time.Date(2026, 11, 17, h, m, 0, 0, loc).Unix()) }
	host := func(id, n string) []struct {
		HostID string `json:"hostid"`
		Name   string `json:"name"`
	} {
		return []struct {
			HostID string `json:"hostid"`
			Name   string `json:"name"`
		}{{id, n}}
	}
	evs := []rawEvent{
		{EventID: "1", ObjectID: "t1", Clock: at(4, 10), Name: "CPU hoog", Severity: 4, REventID: "11", Hosts: host("h1", "web01")},
		{EventID: "2", ObjectID: "t1", Clock: at(4, 50), Name: "CPU hoog", Severity: 4, REventID: "12", Hosts: host("h1", "web01")},
		{EventID: "3", ObjectID: "t2", Clock: at(5, 5), Name: "Schijf", Severity: 2, Hosts: host("h2", "db1")},
	}
	rc := map[string]int64{"11": int64(at(4, 20)), "12": int64(at(5, 10))}
	n := uuid.New()
	e := summarize(evs, rc, map[string]*NodeLink{"h2": {NodeID: n, Hostname: "db1"}}, "24h", from, now)
	if e.Total != 3 || e.Resolved != 2 || e.MTTR == nil || *e.MTTR != 900 {
		t.Fatalf("tellers: %+v mttr %v", e, e.MTTR)
	}
	if len(e.Buckets) != 25 {
		t.Errorf("stukjes: %d", len(e.Buckets))
	}
	last := e.Buckets[len(e.Buckets)-1]
	if last.Severity[2] != 1 || e.Buckets[len(e.Buckets)-2].Severity[4] != 2 {
		t.Errorf("laatste stukjes: %+v %+v", e.Buckets[len(e.Buckets)-2], last)
	}
	if e.Heatmap[1][4] != 2 || e.Heatmap[1][5] != 1 {
		t.Errorf("heatmap dinsdag: %v", e.Heatmap[1])
	}
	if e.TopTriggers[0].Name != "CPU hoog" || e.TopTriggers[0].Count != 2 || e.TopHosts[1].Node == nil || e.TopHosts[1].Node.NodeID != n {
		t.Errorf("top: %+v %+v", e.TopTriggers, e.TopHosts)
	}
	w := summarize(evs, rc, nil, "7d", now.Add(-7*24*time.Hour), now)
	if len(w.Buckets) != 8 || w.Buckets[7].Severity[4] != 2 {
		t.Errorf("per dag: %d %+v", len(w.Buckets), w.Buckets[len(w.Buckets)-1])
	}
}

func TestBoardNormalize(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	in := BoardInput{Name: "  Databanken ", Refresh: 60, Panels: []Panel{
		{Type: PanelGraph, Title: "CPU", W: 9, H: 0, Items: []BoardItem{{ItemID: " 42 ", Label: "db1"}}, Range: "2h", Warn: f(80), Text: "weg"},
		{ID: "a", Type: PanelGauge, Items: []BoardItem{{ItemID: "7"}}, Max: f(100), Crit: f(90)},
		{ID: "a", Type: PanelProblems, MinSeverity: 9, Items: []BoardItem{{ItemID: "1"}}},
		{Type: PanelText, Text: " uitleg "},
	}}
	if err := in.Normalize(); err != nil {
		t.Fatal(err)
	}
	g := in.Panels[0]
	if in.Name != "Databanken" || g.W != 4 || g.H != 1 || g.Range != "24h" || g.Warn != nil || g.Text != "" || g.Items[0].ItemID != "42" || g.ID == "" {
		t.Errorf("grafiek: %+v", g)
	}
	if in.Panels[1].ID != "a" || in.Panels[2].ID == "a" || in.Panels[2].MinSeverity != 5 || len(in.Panels[2].Items) != 0 || in.Panels[3].Text != "uitleg" {
		t.Errorf("panelen: %+v", in.Panels)
	}

	bad := []BoardInput{
		{Name: "", Refresh: 60},
		{Name: "x", Refresh: 45},
		{Name: "x", Refresh: 60, Panels: []Panel{{Type: "kaart"}}},
		{Name: "x", Refresh: 60, Panels: []Panel{{Type: PanelGraph}}},
		{Name: "x", Refresh: 60, Panels: []Panel{{Type: PanelStat, Items: []BoardItem{{ItemID: "1"}, {ItemID: "2"}}}}},
		{Name: "x", Refresh: 60, Panels: []Panel{{Type: PanelStat, Items: []BoardItem{{ItemID: "abc"}}}}},
		{Name: "x", Refresh: 60, Panels: []Panel{{Type: PanelGauge, Items: []BoardItem{{ItemID: "1"}}, Max: f(0)}}},
		{Name: "x", Refresh: 60, Panels: make([]Panel, MaxPanels+1)},
	}
	for i, b := range bad {
		var fe *FieldError
		if err := b.Normalize(); !errors.As(err, &fe) {
			t.Errorf("%d: verwacht een fout, kreeg %v", i, err)
		}
	}
}

func TestBoards(t *testing.T) {
	pool := storetest.DB(t)
	ctx := context.Background()
	s := NewService(pool, events.NewWriter(store.New(pool), nil), nil, nil)
	in := BoardInput{Name: "Databanken", Refresh: 60, Panels: []Panel{{Type: PanelStat, Items: []BoardItem{{ItemID: "5"}}}}}
	b, err := s.CreateBoard(ctx, events.System(), nil, in)
	if err != nil {
		t.Fatal(err)
	}
	if b.Name != "Databanken" || len(b.Panels) != 1 || b.Panels[0].ID == "" || b.Panels[0].W != 1 {
		t.Fatalf("bewaard: %+v", b)
	}
	if _, err := s.CreateBoard(ctx, events.System(), nil, BoardInput{Name: "databanken", Refresh: 0}); !errors.Is(err, ErrBoardExists) {
		t.Errorf("dubbele naam: %v", err)
	}
	in.Name, in.Panels = "Databank", append(in.Panels, Panel{Type: PanelText, Text: "hallo"})
	b2, err := s.UpdateBoard(ctx, events.System(), nil, b.ID, in)
	if err != nil || b2.Name != "Databank" || len(b2.Panels) != 2 || !b2.UpdatedAt.After(b.CreatedAt.Add(-time.Second)) {
		t.Fatalf("bijgewerkt: %+v %v", b2, err)
	}
	all, err := s.Boards(ctx)
	if err != nil || len(all) != 1 {
		t.Fatalf("lijst: %v %v", all, err)
	}
	if err := s.DeleteBoard(ctx, events.System(), b.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Board(ctx, b.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("na weggooien: %v", err)
	}
	if _, err := s.UpdateBoard(ctx, events.System(), nil, b.ID, in); !errors.Is(err, ErrNotFound) {
		t.Errorf("bijwerken van een weg dashboard: %v", err)
	}
}

func TestNotices(t *testing.T) {
	now := time.Date(2026, 11, 17, 6, 0, 0, 0, time.UTC)
	node := &NodeLink{Hostname: "db1"}
	snap := &Snapshot{
		HA:      []HANode{{Name: "a", Status: "standby"}, {Name: "b", Status: "unavailable"}},
		Proxies: []Proxy{{Name: "p1", State: "offline"}, {Name: "p2", State: "online"}},
		Problems: []Problem{
			{Name: "oud en hoog", Severity: 4, Since: now.Add(-time.Hour), Hosts: []HostRef{{Name: "db1"}}},
			{Name: "net nieuw", Severity: 5, Since: now.Add(-time.Minute), Hosts: []HostRef{{Name: "db2"}}},
			{Name: "bevestigd", Severity: 5, Since: now.Add(-time.Hour), Acknowledged: true},
			{Name: "gemiddeld", Severity: 3, Since: now.Add(-time.Hour)},
		},
		Hosts:     []Host{{Node: node, Enabled: true, Available: AvailDown}, {Node: node, Enabled: true, Maintenance: true, Available: AvailDown}},
		Unwatched: []NodeRef{{Hostname: "plex1"}},
	}
	got := map[string]Notice{}
	for _, n := range notices(snap, "", now, now) {
		got[n.ID] = n
	}
	if len(got) != 5 || !got["zabbix-ha"].Red || got["zabbix-proxy"].Count != 1 || got["zabbix-unacked"].Count != 1 ||
		got["zabbix-unacked"].Names[0] != "db1" || got["zabbix-nodes-down"].Count != 1 || got["zabbix-unwatched"].Red {
		t.Errorf("regels: %+v", got)
	}
	if _, ok := got["zabbix-api"]; ok {
		t.Error("API werkt; geen regel verwacht")
	}
	got = map[string]Notice{}
	for _, n := range notices(nil, "connection refused", now.Add(-10*time.Minute), now) {
		got[n.ID] = n
	}
	if n := got["zabbix-api"]; len(got) != 1 || !n.Red || !strings.Contains(n.Detail, "connection refused") {
		t.Errorf("API weg: %+v", got)
	}
	if n := notices(nil, "even weg", now.Add(-time.Minute), now); len(n) != 0 {
		t.Errorf("kort weg telt niet: %+v", n)
	}
}

func TestSecurityProblem(t *testing.T) {
	for _, c := range []struct {
		tags []Tag
		want bool
	}{
		{[]Tag{{"scope", "security"}}, true},
		{[]Tag{{"Scope", "Security"}}, true},
		{[]Tag{{"security", ""}}, true},
		{[]Tag{{"component", "security"}}, true},
		{[]Tag{{"scope", "availability"}}, false},
		{nil, false},
	} {
		if got := securityProblem(Problem{Tags: c.tags}); got != c.want {
			t.Errorf("%v: %v", c.tags, got)
		}
	}
}

func TestLogProblems(t *testing.T) {
	pool := storetest.DB(t)
	ctx := context.Background()
	var nodeID uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO nodes (hostname, primary_ip) VALUES ('db1', '192.168.10.191') RETURNING id`).Scan(&nodeID); err != nil {
		t.Fatal(err)
	}
	s := NewService(pool, events.NewWriter(store.New(pool), nil), nil, nil)
	now := time.Date(2026, 11, 17, 6, 0, 0, 0, time.UTC)
	node := &NodeLink{NodeID: nodeID, Hostname: "db1"}
	old := Problem{EventID: "1", Name: "Al lang", Severity: 4, Since: now.Add(-time.Hour), Hosts: []HostRef{{Name: "db1", Node: node}}}
	fresh := Problem{EventID: "2", Name: "Net begonnen", Severity: 3, Since: now.Add(-30 * time.Second), Hosts: []HostRef{{Name: "db1", Node: node}}}
	low := Problem{EventID: "3", Name: "Info", Severity: 1, Since: now, Hosts: []HostRef{{Name: "db1", Node: node}}}
	other := Problem{EventID: "4", Name: "Geen node", Severity: 5, Since: now, Hosts: []HostRef{{Name: "printer"}}}
	count := func(action string) int {
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM events WHERE action = $1 AND subject_id = $2`, action, nodeID.String()).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	// Eerste ronde: alleen wat net begon.
	s.logProblems(ctx, &Snapshot{Problems: []Problem{old, fresh, low, other}}, now)
	if count("zabbix.problem_started") != 1 {
		t.Fatalf("na de eerste ronde: %d", count("zabbix.problem_started"))
	}
	// Tweede ronde: niets nieuws; derde: het oude is opgelost.
	s.logProblems(ctx, &Snapshot{Problems: []Problem{old, fresh}}, now.Add(time.Minute))
	s.logProblems(ctx, &Snapshot{Problems: []Problem{fresh}}, now.Add(2*time.Minute))
	if count("zabbix.problem_started") != 1 || count("zabbix.problem_resolved") != 1 {
		t.Fatalf("begonnen %d, opgelost %d", count("zabbix.problem_started"), count("zabbix.problem_resolved"))
	}
	var payload []byte
	if err := pool.QueryRow(ctx, `SELECT payload FROM events WHERE action = 'zabbix.problem_resolved'`).Scan(&payload); err != nil || !strings.Contains(string(payload), `"duration_s": 3720`) && !strings.Contains(string(payload), `"duration_s":3720`) {
		t.Fatalf("payload %s %v", payload, err)
	}
}
