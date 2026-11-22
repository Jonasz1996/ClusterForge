package agent

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

const testOPNConfig = `<?xml version="1.0"?>
<opnsense>
  <system>
    <hostname>fw1</hostname>
    <domain>home.arpa</domain>
    <webgui><protocol>https</protocol><port>8443</port></webgui>
    <user><name>root</name><password>$2y$10$abc</password></user>
  </system>
  <interfaces>
    <wan><if>vtnet0</if><descr>WAN</descr><enable>1</enable></wan>
    <lan><if>vtnet1</if><enable>1</enable></lan>
    <opt1><if>vtnet2</if><descr>PFSYNC</descr></opt1>
  </interfaces>
  <virtualip>
    <vip><mode>carp</mode><interface>lan</interface><vhid>1</vhid><advbase>1</advbase><advskew>0</advskew><subnet>192.168.10.1</subnet><subnet_bits>24</subnet_bits><descr>LAN-gateway</descr><password>geheimcarp</password></vip>
    <vip><mode>carp</mode><interface>wan</interface><vhid>2</vhid><advbase>1</advbase><advskew>0</advskew><subnet>203.0.113.10</subnet><subnet_bits>29</subnet_bits></vip>
    <vip><mode>ipalias</mode><interface>lan</interface><subnet>192.168.10.5</subnet><subnet_bits>32</subnet_bits></vip>
  </virtualip>
  <hasync>
    <pfsyncinterface>opt1</pfsyncinterface>
    <pfsyncpeerip>10.99.0.2</pfsyncpeerip>
    <synchronizetoip>10.99.0.2</synchronizetoip>
    <username>root</username>
    <password>hageheim</password>
    <syncitems>aliases,rules,nat</syncitems>
  </hasync>
  <revision><username>root@192.168.90.5</username><time>1763350000.12</time><description>/firewall_rules.php made changes</description></revision>
</opnsense>`

const testOPNIfconfig = `{
 "vtnet0": {"flags":["up","broadcast"],"status":"active","ipv4":[{"ipaddr":"203.0.113.11","subnetbits":29},{"ipaddr":"203.0.113.10","subnetbits":29,"vhid":"2"}],"carp":{"2":{"status":"MASTER","vhid":"2","advbase":"1","advskew":"0"}}},
 "vtnet1": {"flags":["up"],"status":"active","ipv4":[{"ipaddr":"192.168.10.2","subnetbits":24},{"ipaddr":"192.168.10.1","subnetbits":24,"vhid":"1"}],"carp":[{"status":"BACKUP","vhid":"1","advbase":"1","advskew":"100"}]},
 "vtnet2": {"flags":["up"],"status":"no carrier","ipv4":[{"ipaddr":"10.99.0.1","subnetbits":30}]},
 "pfsync0": {"flags":["up"],"pfsync":{"syncdev":"vtnet2","syncpeer":"10.99.0.2","syncok":"1"}}
}`

// opnFake maakt een OPNsense-wortel met config.xml en een nep-configctl.
func opnFake(t *testing.T) (*Collector, *[]string) {
	t.Helper()
	root := t.TempDir()
	for _, d := range []string{"usr/local/opnsense/version", "conf/backup"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	_ = os.WriteFile(filepath.Join(root, opnVersionFile), []byte(`{"product_version":"26.1.2_1","product_series":"26.1","product_name":"OPNsense"}`), 0o644)
	_ = os.WriteFile(filepath.Join(root, opnConfigXML), []byte(testOPNConfig), 0o644)
	_ = os.WriteFile(filepath.Join(root, "conf/backup/config-1763340000.5.xml"), []byte(strings.Replace(testOPNConfig, "1763350000.12", "1763340000.5", 1)), 0o644)
	var mu sync.Mutex
	calls := &[]string{}
	c := &Collector{Root: root, OS: "freebsd"}
	c.Run = func(_ context.Context, name string, args ...string) ([]byte, error) {
		mu.Lock()
		*calls = append(*calls, name+" "+strings.Join(args, " "))
		mu.Unlock()
		switch strings.TrimSpace(name + " " + strings.Join(args, " ")) {
		case opnConfigctl + " interface list ifconfig":
			return []byte(testOPNIfconfig), nil
		case opnConfigctl + " interface show carp":
			return []byte(`{"demotion":"0","allow":"1","maintenancemode":false,"status_msg":""}`), nil
		case opnConfigctl + " service list":
			return []byte(`[{"name":"unbound","description":"Unbound DNS","status":"unbound is running as pid 123."},{"name":"openvpn","id":3,"status":"openvpn is not running."}]`), nil
		case "freebsd-version":
			return []byte("14.3-RELEASE-p4\n"), nil
		}
		return nil, os.ErrNotExist
	}
	return c, calls
}

func TestOPNsenseFacts(t *testing.T) {
	c, _ := opnFake(t)
	o := c.opnAPI()
	o.last = time.Now() // geen sleutel maken in deze test
	f := c.opnsense(context.Background())
	if f == nil {
		t.Fatal("geen OPNsense herkend")
	}
	if f.Version != "26.1.2" || f.Series != "26.1" || f.FreeBSD != "14.3-RELEASE-p4" || f.Hostname != "fw1.home.arpa" {
		t.Fatalf("versie/host %+v", f)
	}
	if !f.CARP.Allow || f.CARP.Demotion != 0 || f.CARP.Maintenance {
		t.Fatalf("carp %+v", f.CARP)
	}
	if len(f.VIPs) != 2 { // de IP-alias zonder vhid telt niet
		t.Fatalf("vips %+v", f.VIPs)
	}
	lan := f.VIPs[0]
	if lan.Device != "vtnet1" || lan.State != "BACKUP" || lan.AdvSkew != 100 || lan.Address != "192.168.10.1/24" || lan.Descr != "LAN-gateway" {
		t.Fatalf("lan-vip %+v", lan)
	}
	if f.VIPs[1].State != "MASTER" {
		t.Fatalf("vips %+v", f.VIPs)
	}
	if f.Role() != "mixed" {
		t.Fatalf("rol %q", f.Role())
	}
	if f.HA.PfsyncInterface != "opt1" || f.HA.SyncTo != "10.99.0.2" || !f.HA.PfsyncUp || !slices.Equal(f.HA.SyncItems, []string{"aliases", "rules", "nat"}) {
		t.Fatalf("ha %+v", f.HA)
	}
	if len(f.Interfaces) != 3 || f.Interfaces[1].Name != "lan" || !f.Interfaces[1].Up || !slices.Equal(f.Interfaces[1].Addresses, []string{"192.168.10.2/24"}) {
		t.Fatalf("interfaces %+v", f.Interfaces)
	}
	if f.Interfaces[2].Enabled || f.Interfaces[2].Up {
		t.Fatalf("opt1 %+v", f.Interfaces[2])
	}
	if f.Config.Time != 1763350000 || f.Config.User != "root@192.168.90.5" || f.Config.Backups != 1 {
		t.Fatalf("config %+v", f.Config)
	}
	if f.API.OK || f.API.Error == "" {
		t.Fatalf("api zonder sleutel %+v", f.API)
	}
}

func TestOPNsenseRole(t *testing.T) {
	f := &protocol.OPNsenseFacts{VIPs: []protocol.OPNsenseVIP{{Mode: "carp", State: "MASTER"}, {Mode: "carp", State: "MASTER"}, {Mode: "ipalias", State: "DISABLED"}}}
	if f.Role() != "master" {
		t.Fatal(f.Role())
	}
	f.VIPs[1].State = "INIT"
	if f.Role() != "mixed" {
		t.Fatal(f.Role())
	}
	if (&protocol.OPNsenseFacts{}).Role() != "" {
		t.Fatal("zonder carp geen rol")
	}
}

func TestBSDServicesAndHeartbeat(t *testing.T) {
	c, _ := opnFake(t)
	inner := c.Run
	c.Run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name == "sysctl" {
			return []byte("kern.boottime={ sec = 1763300000, usec = 5 } Mon Nov 17\nvm.loadavg={ 0.25 0.50 0.75 }\n"), nil
		}
		return inner(ctx, name, args...)
	}
	hb := c.Heartbeat(context.Background(), "test")
	if hb.Load != [3]float64{0.25, 0.5, 0.75} || hb.UptimeSeconds <= 0 {
		t.Fatalf("heartbeat %+v", hb)
	}
	if hb.Services["unbound"] != "active" || hb.Services["openvpn/3"] != "inactive" {
		t.Fatalf("diensten %+v", hb.Services)
	}
}

func TestBSDMetrics(t *testing.T) {
	var m metricSet
	bsdCPUMetrics(&m, "100 0 50 10 840 200 0 100 20 680", 100)
	if len(m.samples) != 10 || m.samples[4].Value != 8.4 || m.samples[5].Labels["cpu"] != "1" || m.samples[9].Labels["mode"] != "idle" {
		t.Fatalf("cpu %+v", m.samples)
	}
	m = metricSet{}
	bsdMemMetrics(&m, map[string]string{
		"hw.pagesize": "4096", "hw.physmem": "8589934592", "vm.stats.vm.v_free_count": "100000",
		"vm.stats.vm.v_inactive_count": "50000", "vm.stats.vm.v_laundry_count": "0", "kstat.zfs.misc.arcstats.size": "1000000000",
	})
	got := map[string]float64{}
	for _, s := range m.samples {
		got[s.Name] = s.Value
	}
	if got["node_memory_MemTotal_bytes"] != 8589934592 || got["node_memory_MemFree_bytes"] != 409600000 ||
		got["node_memory_MemAvailable_bytes"] != 409600000+204800000+1000000000 {
		t.Fatalf("geheugen %+v", got)
	}
	m = metricSet{}
	bsdNetMetrics(&m, []byte(`{"statistics":{"interface":[
		{"name":"vtnet0","network":"<Link#1>","address":"aa:bb","received-packets":10,"received-errors":1,"received-bytes":1000,"sent-packets":5,"send-errors":0,"sent-bytes":500},
		{"name":"vtnet0","network":"203.0.113.0/29","address":"203.0.113.11","received-bytes":900},
		{"name":"lo0","network":"<Link#2>","received-bytes":5},
		{"name":"pfsync0*","network":"<Link#3>","received-bytes":5}]}}`))
	if len(m.samples) != 4 || m.samples[0].Labels["device"] != "vtnet0" || m.samples[0].Value != 1000 {
		t.Fatalf("netwerk %+v", m.samples)
	}
	m = metricSet{}
	bsdTempMetrics(&m, map[string]string{"dev.cpu.0.temperature": "41.0C", "dev.cpu.1.temperature": "43.5C", "kern.hostname": "fw1"})
	if len(m.samples) != 2 || m.samples[1].Value != 43.5 || m.samples[1].Labels["sensor"] != "core1" {
		t.Fatalf("temperatuur %+v", m.samples)
	}
	if hz := parseClockrate("{ hz = 1000, tick = 1000, profhz = 8128, stathz = 127 }"); hz != 127 {
		t.Fatal(hz)
	}
}

func TestParseMountP(t *testing.T) {
	out := "zroot/ROOT/default\t/\tzfs\trw\t0 0\nzroot/tmp\t/tmp\tzfs\trw\t0 0\nzroot\t/zroot\tzfs\trw\t0 0\ndevfs\t/dev\tdevfs\trw\t0 0\n/dev/gpt/data\t/data\tufs\trw\t2 2\ntank/backup\t/tank\tzfs\trw\t0 0\n"
	ms := parseMountP([]byte(out))
	var paths []string
	for _, m := range ms {
		paths = append(paths, m.path)
	}
	if !slices.Equal(paths, []string{"/", "/data", "/tank"}) {
		t.Fatalf("mounts %v", paths)
	}
}

func TestRedactConfigXML(t *testing.T) {
	out := redactConfigXML(testOPNConfig + `<cert><crt>QUJD</crt><prv>UFJJVg==</prv></cert><ipsec><pre-shared-key>s3cr3t</pre-shared-key></ipsec>`)
	for _, s := range []string{"$2y$10$abc", "geheimcarp", "hageheim", "UFJJVg==", "s3cr3t"} {
		if strings.Contains(out, s) {
			t.Fatalf("geheim %q staat er nog in", s)
		}
	}
	if !strings.Contains(out, "<crt>QUJD</crt>") || !strings.Contains(out, "<password>[geheim]</password>") {
		t.Fatalf("te veel weggehaald:\n%s", out)
	}
}

func TestOPNCheckResult(t *testing.T) {
	if err := opnCheckResult(json.RawMessage(`{"result":"saved","uuid":"x"}`)); err != nil {
		t.Fatal(err)
	}
	if err := opnCheckResult(json.RawMessage(`{"result":"failed","validations":{"alias.name":"Naam bestaat al"}}`)); err == nil || !strings.Contains(err.Error(), "alias.name: Naam bestaat al") {
		t.Fatal(err)
	}
	if err := opnCheckResult(json.RawMessage(`{"status":"error","message":"kapot"}`)); err == nil {
		t.Fatal("fout niet herkend")
	}
	if err := opnCheckResult(json.RawMessage(`{"status":"ok"}`)); err != nil {
		t.Fatal(err)
	}
}

// opnAPIFake is een nep-API van OPNsense.
type opnAPIFake struct {
	mu    sync.Mutex
	calls []string
	fail  string
}

func (f *opnAPIFake) handler(t *testing.T, key, secret string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := "Basic " + base64.StdEncoding.EncodeToString([]byte(key+":"+secret))
		if r.Header.Get("Authorization") != auth {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.calls = append(f.calls, r.Method+" "+r.URL.Path+" "+string(body))
		fail := f.fail
		f.mu.Unlock()
		switch {
		case r.URL.Path == "/api/"+opnPing:
			_, _ = w.Write([]byte(`{"status":"ready"}`))
		case fail != "" && strings.HasSuffix(r.URL.Path, fail):
			_, _ = w.Write([]byte(`{"result":"failed","validations":{"alias.content":"ongeldig"}}`))
		case strings.HasSuffix(r.URL.Path, "/reconfigure"):
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		default:
			_, _ = w.Write([]byte(`{"result":"saved"}`))
		}
	})
}

func TestOPNApplyConfirmAndRollback(t *testing.T) {
	c, calls := opnFake(t)
	fake := &opnAPIFake{}
	srv := httptest.NewServer(fake.handler(t, "k", "s"))
	defer srv.Close()
	o := c.opnAPI()
	o.Base, o.Client = srv.URL, srv.Client()
	if err := o.saveKey(opnKey{Key: "k", Secret: "s", User: "clusterforge"}); err != nil {
		t.Fatal(err)
	}
	if st := c.opnAPIState(context.Background()); !st.OK {
		t.Fatalf("api %+v", st)
	}
	inner := c.Run
	legacy := ""
	c.Run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name == opnConfigctl && len(args) > 1 && (args[0] == "filter" || args[1] == "reconfigure" || args[0] == "service") {
			_, _ = inner(ctx, name, args...)
			return []byte("OK\n"), nil
		}
		if name == opnPHP && strings.HasSuffix(args[0], "legacy.php") {
			b, _ := os.ReadFile(args[1])
			legacy = string(b)
			return []byte(`{"status":"ok"}`), nil
		}
		if name == opnPHP {
			// restore.php: zet het herstelpunt terug zoals OPNsense zou doen.
			b, _ := os.ReadFile(args[1])
			_ = os.WriteFile(filepath.Join(c.Root, opnConfigXML), b, 0o644)
			return []byte(`{"status":"ok"}`), nil
		}
		return inner(ctx, name, args...)
	}
	r := &RunExecutor{Root: c.Root, OPN: o}
	r.Cmd = func(ctx context.Context, _ string, argv, _ []string, out io.Writer) (int, error) {
		b, err := c.Run(ctx, argv[0], argv[1:]...)
		_, _ = out.Write(b)
		if err != nil && argv[0] != bsdDaemon && argv[0] != "/bin/pkill" {
			return 1, nil
		}
		return 0, nil
	}
	old := hostOS
	hostOS = "freebsd"
	defer func() { hostOS = old }()

	ch := &protocol.OPNChange{
		ID: "abcd1234-0001", ConfirmWithin: 120, Description: "alias",
		Calls:  []protocol.OPNCall{{Path: "firewall/alias/addItem", Body: map[string]any{"alias": map[string]any{"name": "test", "content": "1.2.3.4"}}}},
		Apply:  []protocol.OPNCall{{Path: "firewall/alias/reconfigure"}},
		Secret: []string{"alias.content"},
	}
	// Op een node die niet voor alles master is, weigert de agent.
	ch.RequireMaster = true
	var out bytes.Buffer
	if _, err := r.opn(context.Background(), &protocol.PVERun{Action: protocol.OPNAPI, OPN: ch}, &out, &protocol.RunStatus{}); err == nil || !strings.Contains(err.Error(), "niet de master") {
		t.Fatalf("geen weigering: %v", err)
	}
	ch.RequireMaster = false
	out.Reset()
	if code, err := r.opn(context.Background(), &protocol.PVERun{Action: protocol.OPNAPI, OPN: ch}, &out, &protocol.RunStatus{}); err != nil || code != 0 {
		t.Fatalf("toepassen %d %v\n%s", code, err, out.String())
	}
	if strings.Contains(out.String(), "1.2.3.4") || !strings.Contains(out.String(), "[geheim]") {
		t.Fatalf("geheim in de uitvoer:\n%s", out.String())
	}
	if !slices.ContainsFunc(*calls, func(s string) bool {
		return strings.HasPrefix(s, bsdDaemon+" -f -p /var/run/cf-opn-rollback-abcd1234-0001.pid")
	}) {
		t.Fatalf("geen terugdraaitimer: %v", *calls)
	}
	if p := opnPending(c.Root); p == nil || p.ID != ch.ID {
		t.Fatalf("lopende wijziging %+v", p)
	}
	// Een tweede wijziging moet wachten.
	ch2 := *ch
	ch2.ID = "abcd1234-0002"
	if _, err := r.opn(context.Background(), &protocol.PVERun{Action: protocol.OPNAPI, OPN: &ch2}, io.Discard, &protocol.RunStatus{}); err == nil {
		t.Fatal("tweede wijziging mocht tijdens de eerste")
	}
	if _, err := r.opn(context.Background(), &protocol.PVERun{Action: protocol.OPNConfirm, Change: ch.ID}, io.Discard, &protocol.RunStatus{}); err != nil {
		t.Fatal(err)
	}
	if opnPending(c.Root) != nil {
		t.Fatal("na bevestigen loopt er nog een wijziging")
	}

	// Een aanroep die OPNsense afkeurt: meteen terug naar het herstelpunt.
	_ = os.WriteFile(filepath.Join(c.Root, opnConfigXML), []byte(testOPNConfig), 0o644)
	fake.fail = "setItem/x"
	ch3 := &protocol.OPNChange{ID: "abcd1234-0003", ConfirmWithin: 60,
		Calls: []protocol.OPNCall{{Path: "firewall/alias/addItem"}, {Path: "firewall/alias/setItem/x"}},
		Apply: []protocol.OPNCall{{Path: "firewall/alias/reconfigure"}}}
	out.Reset()
	code, err := r.opn(context.Background(), &protocol.PVERun{Action: protocol.OPNAPI, OPN: ch3}, &out, &protocol.RunStatus{})
	if err == nil || code != 1 || !strings.Contains(err.Error(), "alias.content: ongeldig") {
		t.Fatalf("afgekeurd %d %v\n%s", code, err, out.String())
	}
	st, _ := loadOPNChange(opnChangePath(c.Root, ch3.ID))
	if st == nil || st.Status != "rolled_back" || !strings.Contains(out.String(), "oude config.xml teruggezet") {
		t.Fatalf("niet teruggedraaid %+v\n%s", st, out.String())
	}
	last := fake.calls[len(fake.calls)-1]
	if !strings.HasPrefix(last, "POST /api/firewall/alias/reconfigure") {
		t.Fatalf("oude toestand niet toegepast: %v", fake.calls)
	}

	// Fase 40 M4: velden zonder API (interface en systeem).
	_ = os.WriteFile(filepath.Join(c.Root, opnConfigXML), []byte(testOPNConfig), 0o644)
	fake.fail = ""
	ch4 := &protocol.OPNChange{ID: "abcd1234-0004", ConfirmWithin: 60, Description: "interface",
		Legacy: []protocol.OPNLegacyEdit{{Section: "interfaces", Key: "opt3", Field: "ipaddr", Value: "192.168.50.1"}, {Section: "system", Field: "dnsserver", Value: "9.9.9.9,1.1.1.1"}}}
	out.Reset()
	if code, err := r.opn(context.Background(), &protocol.PVERun{Action: protocol.OPNAPI, OPN: ch4}, &out, &protocol.RunStatus{}); err != nil || code != 0 {
		t.Fatalf("velden %d %v\n%s", code, err, out.String())
	}
	if !strings.Contains(legacy, `"key":"opt3"`) || !strings.Contains(out.String(), "configctl interface reconfigure opt3") ||
		!slices.Contains(*calls, opnConfigctl+" interface reconfigure opt3") || !slices.Contains(*calls, opnConfigctl+" service reload all") {
		t.Fatalf("velden niet toegepast: %s\n%v\n%s", legacy, *calls, out.String())
	}
	if st, _ := loadOPNChange(opnChangePath(c.Root, ch4.ID)); st == nil || !st.ReloadAll {
		t.Fatalf("terugdraaien herlaadt niet alles: %+v", st)
	}
	bad := protocol.OPNChange{ID: "abcd1234-0005", ConfirmWithin: 60, Legacy: []protocol.OPNLegacyEdit{{Section: "interfaces", Key: "lan", Field: "ipaddr", Value: "1.2.3.4<x>"}}}
	if bad.Validate() == nil {
		t.Fatal("ongeldige waarde aanvaard")
	}
	bad.Legacy[0] = protocol.OPNLegacyEdit{Section: "system", Field: "webgui", Value: "x"}
	if bad.Validate() == nil {
		t.Fatal("webgui mag niet")
	}
}

func TestOPNReadRequests(t *testing.T) {
	c, _ := opnFake(t)
	a := &Agent{Collector: c}
	res := a.handleOPN(context.Background(), protocol.PVERequest{Op: protocol.OPNOpConfigctl, Path: "interface show carp"})
	if res.Error != "" || !strings.Contains(string(res.Data), "demotion") {
		t.Fatalf("configctl %+v", res)
	}
	if res := a.handleOPN(context.Background(), protocol.PVERequest{Op: protocol.OPNOpConfigctl, Path: "system reboot"}); res.Error == "" {
		t.Fatal("reboot mocht als leesverzoek")
	}
	if res := a.handleOPN(context.Background(), protocol.PVERequest{Op: protocol.OPNOpPost, Path: "firewall/alias/addItem"}); res.Error == "" {
		t.Fatal("addItem mocht als leesverzoek")
	}
	res = a.handleOPN(context.Background(), protocol.PVERequest{Op: protocol.OPNOpRevisions})
	var revs []opnRevision
	if res.Error != "" || json.Unmarshal(res.Data, &revs) != nil || len(revs) != 2 || revs[0].File != "current" || revs[1].Time != 1763340000.5 {
		t.Fatalf("revisies %+v %s", res, res.Data)
	}
	res = a.handleOPN(context.Background(), protocol.PVERequest{Op: protocol.OPNOpRevision, Path: "config-1763340000.5.xml"})
	if res.Error != "" || strings.Contains(string(res.Data), "hageheim") || !strings.Contains(string(res.Data), "fw1") {
		t.Fatalf("revisie %+v", res)
	}
	if res := a.handleOPN(context.Background(), protocol.PVERequest{Op: protocol.OPNOpRevision, Path: "../../etc/passwd"}); res.Error == "" {
		t.Fatal("pad buiten /conf/backup")
	}
}

// TestOPNRestoreAndBackup: een revisie terugzetten gaat met herstelpunt en
// herlaadt alles; de back-up geeft de volledige config.xml met geheimen.
func TestOPNRestoreAndBackup(t *testing.T) {
	c, calls := opnFake(t)
	a := &Agent{Collector: c}
	res := a.handleOPN(context.Background(), protocol.PVERequest{Op: protocol.OPNOpBackup})
	var xml string
	if res.Error != "" || json.Unmarshal(res.Data, &xml) != nil || !strings.Contains(xml, "hageheim") {
		t.Fatalf("back-up %+v", res)
	}
	fake := &opnAPIFake{}
	srv := httptest.NewServer(fake.handler(t, "k", "s"))
	defer srv.Close()
	o := c.opnAPI()
	o.Base, o.Client = srv.URL, srv.Client()
	if err := o.saveKey(opnKey{Key: "k", Secret: "s", User: "clusterforge"}); err != nil {
		t.Fatal(err)
	}
	inner := c.Run
	restored := ""
	c.Run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name == opnPHP {
			restored = strings.Join(args[1:], " ")
			return []byte(`{"status":"ok"}`), nil
		}
		return inner(ctx, name, args...)
	}
	r := &RunExecutor{Root: c.Root, OPN: o}
	r.Cmd = func(ctx context.Context, _ string, argv, _ []string, out io.Writer) (int, error) {
		b, _ := c.Run(ctx, argv[0], argv[1:]...)
		_, _ = out.Write(b)
		return 0, nil
	}
	old := hostOS
	hostOS = "freebsd"
	defer func() { hostOS = old }()
	ch := &protocol.OPNChange{ID: "abcd1234-0009", ConfirmWithin: 120, Description: "terug", Restore: "config-1763340000.5.xml"}
	if err := ch.Validate(); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if code, err := r.opn(context.Background(), &protocol.PVERun{Action: protocol.OPNAPI, OPN: ch}, &out, &protocol.RunStatus{}); err != nil || code != 0 {
		t.Fatalf("terugzetten %d %v\n%s", code, err, out.String())
	}
	if !strings.Contains(restored, "config-1763340000.5.xml") {
		t.Fatalf("geen restore: %q\n%s", restored, out.String())
	}
	if !slices.ContainsFunc(*calls, func(s string) bool { return strings.Contains(s, "service reload all") }) {
		t.Fatalf("niet herladen: %v", *calls)
	}
	if p := opnPending(c.Root); p == nil || !p.ReloadAll {
		t.Fatalf("terugdraaien herlaadt niet alles: %+v", p)
	}
	bad := &protocol.OPNChange{ID: "abcd1234-0010", ConfirmWithin: 120, Restore: "../../config.xml"}
	if bad.Validate() == nil {
		t.Fatal("ongeldige revisie toegelaten")
	}
}
