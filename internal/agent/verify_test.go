package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

func TestReadVerifyRequest(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{`{`, "geen geldige JSON"},
		{`{"services":["-x"]}`, "ongeldige unit"},
		{`{"services":["a b"]}`, "ongeldige unit"},
		{`{"tcp":[{"host":"10.0.0.1","port":80}]}`, "alleen 127.0.0.1 of ::1"},
		{`{"tcp":[{"port":0}]}`, "ongeldige poort"},
		{`{"http":[{"url":"https://127.0.0.1/"}]}`, "alleen http://127.0.0.1"},
		{`{"http":[{"url":"http://localhost/"}]}`, "alleen http://127.0.0.1"},
		{`{"http":[{"url":"http://u:p@127.0.0.1/"}]}`, "alleen http://127.0.0.1"},
		{`{"http":[{"url":"http://127.0.0.1/","expect":42}]}`, "ongeldige statuscode"},
	} {
		if _, err := ReadVerifyRequest(strings.NewReader(tc.in)); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v", tc.in, err)
		}
	}
	req, err := ReadVerifyRequest(strings.NewReader(`{"services":["nginx","nginx","postgresql@16-main"],"tcp":[{"port":80}],"http":[{"url":"http://[::1]:8080/x"}],"nieuw":1}`))
	if err != nil || len(req.Services) != 2 || req.TCP[0].Host != "127.0.0.1" || req.HTTP[0].Expect != 200 {
		t.Fatalf("geldige aanvraag: %+v %v", req, err)
	}
}

func TestVerifyMain(t *testing.T) {
	v := &Verifier{Version: "1.2.3", Exec: (&fakeSystem{}).exec, BootWait: time.Millisecond}
	var out bytes.Buffer
	if code := v.Main(context.Background(), strings.NewReader(`{"services":[1]}`), &out); code != protocol.VerifyInvalid {
		t.Fatalf("exitcode bij een ongeldige aanvraag: %d", code)
	}
	var res protocol.VerifyResult
	if err := json.Unmarshal(out.Bytes(), &res); err != nil || res.Error == "" || res.ProtocolVersion != protocol.Version || res.AgentVersion != "1.2.3" {
		t.Fatalf("antwoord bij een ongeldige aanvraag: %s %v", out.String(), err)
	}
	out.Reset()
	res = protocol.VerifyResult{}
	if code := v.Main(context.Background(), strings.NewReader(`{"services":["ontbreekt"]}`), &out); code != 0 {
		t.Fatalf("exitcode: %d", code)
	}
	if err := json.Unmarshal(out.Bytes(), &res); err != nil || res.Error != "" || len(res.Checks) != 2 || res.Checks[0].Status != protocol.VerifyWarning {
		t.Fatalf("antwoord: %s %v", out.String(), err)
	}
}

// fakeSystem speelt systemctl, journalctl, psql en mariadb na.
type fakeSystem struct {
	units   map[string]string // unit -> ActiveState
	failed  []string
	journal map[string]string
	psql    map[string]string // laatste argumenten -> uitvoer; "!" ervoor is een fout
	mariadb map[string]string
	calls   []string
}

func (f *fakeSystem) exec(_ context.Context, name string, args ...string) ([]byte, error) {
	call := name + " " + strings.Join(args, " ")
	f.calls = append(f.calls, call)
	answer := func(m map[string]string, key string) ([]byte, error) {
		out, ok := m[key]
		switch {
		case !ok:
			return nil, fmt.Errorf("%s: %w", name, exec.ErrNotFound)
		case strings.HasPrefix(out, "!"):
			return nil, errors.New("exit status 2: " + out[1:])
		}
		return []byte(out), nil
	}
	switch {
	case call == "systemctl is-system-running --wait":
		return []byte("degraded\n"), nil
	case strings.HasPrefix(call, "systemctl show"):
		var b strings.Builder
		for _, u := range args[2:] {
			state, ok := f.units[u]
			if !ok {
				fmt.Fprintf(&b, "Id=%s\nLoadState=not-found\nActiveState=inactive\nSubState=dead\nResult=success\n\n", u)
				continue
			}
			sub, result := map[string]string{"active": "running", "failed": "failed"}[state], "success"
			if state == "failed" {
				result = "exit-code"
			}
			fmt.Fprintf(&b, "Id=%s\nLoadState=loaded\nActiveState=%s\nSubState=%s\nResult=%s\n\n", u, state, sub, result)
		}
		return []byte(b.String()), nil
	case call == "systemctl --failed --plain --no-legend":
		var b strings.Builder
		for _, u := range f.failed {
			fmt.Fprintf(&b, "%s loaded failed failed Iets\n", u)
		}
		return []byte(b.String()), nil
	case name == "journalctl":
		if j, ok := f.journal[args[1]]; ok {
			return []byte(j), nil
		}
		return []byte("-- No entries --\n"), nil
	case name == "runuser":
		return answer(f.psql, strings.Join(args[5:], " "))
	case name == "mariadb-admin" || name == "mysqladmin" || name == "mariadb" || name == "mysql":
		return answer(f.mariadb, call)
	}
	return nil, fmt.Errorf("%s: %w", name, exec.ErrNotFound)
}

func TestVerify(t *testing.T) {
	root := t.TempDir()
	// Iets luistert op poort 81, maar alleen op 10.0.20.11.
	writeFile(t, root, "proc/net/tcp", "  sl  local_address rem_address   st\n"+
		"   0: 0B14000A:0051 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 1\n"+
		"   1: 0100007F:0035 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 2\n")
	open, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = open.Close() }()
	openPort := open.Addr().(*net.TCPAddr).Port
	closed, _ := net.Listen("tcp", "127.0.0.1:0")
	closed2, _ := net.Listen("tcp", "127.0.0.1:0")
	closedPort, closedPort2 := closed.Addr().(*net.TCPAddr).Port, closed2.Addr().(*net.TCPAddr).Port
	_, _ = closed.Close(), closed2.Close()
	web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			_, _ = fmt.Fprint(w, "<h1>Web</h1>")
		case "/weg":
			http.Redirect(w, r, "http://10.0.0.1/", http.StatusFound)
		default:
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	defer web.Close()

	sys := &fakeSystem{
		units: map[string]string{
			"nginx.service": "active", "keepalived.service": "active", "postgresql@16-main.service": "active",
			"haproxy.service": "failed", "redis-server.service": "failed", "patroni.service": "failed",
		},
		failed: []string{"networking.service", "haproxy.service", "redis-server.service", "patroni.service", "postfix.service"},
		journal: map[string]string{
			"haproxy.service":      "[ALERT] Starting frontend web: cannot bind socket (Cannot assign requested address) [10.0.20.100:80]\n",
			"redis-server.service": "Fatal error loading the DB\nredis-server.service: Main process exited, code=exited, status=1/FAILURE\n",
		},
		psql: map[string]string{
			"-c SELECT 1": "1\n", "-c SELECT pg_is_in_recovery()": "t\n",
			"-c SELECT datname FROM pg_database WHERE datallowconn AND NOT datistemplate ORDER BY 1": "app\npostgres\nhost=x\n",
			"-d app -c SELECT 1": "1\n", "-d postgres -c SELECT 1": "1\n",
		},
		mariadb: map[string]string{
			"mysqladmin ping":               "mysqld is alive\n",
			"mysql -N -B -e SHOW DATABASES": "information_schema\nmysql\nperformance_schema\nshop\nsys\nwiki\n",
		},
	}
	v := &Verifier{Version: "1.2.3", Exec: sys.exec, Root: root, BootWait: time.Second, Settle: 200 * time.Millisecond, Retry: 20 * time.Millisecond}
	res := v.Verify(context.Background(), protocol.VerifyRequest{
		Services: []string{"nginx", "keepalived", "postgresql@16-main", "haproxy", "redis-server", "patroni", "apache2"},
		TCP: []protocol.VerifyTCP{
			{Host: "127.0.0.1", Port: openPort, Service: "nginx"},
			{Host: "127.0.0.1", Port: closedPort},
			{Host: "127.0.0.1", Port: 81},
			{Host: "127.0.0.1", Port: closedPort2, Service: "haproxy"},
		},
		HTTP: []protocol.VerifyHTTP{
			{URL: web.URL + "/", Expect: 200}, {URL: web.URL + "/kapot", Expect: 200}, {URL: web.URL + "/weg", Expect: 200},
		},
		PostgreSQL: true, MariaDB: true,
	})
	if res.ProtocolVersion != protocol.Version || res.AgentVersion != "1.2.3" || res.Error != "" {
		t.Fatalf("kop: %+v", res)
	}
	got := map[string]protocol.VerifyCheck{}
	var order []string
	for _, c := range res.Checks {
		got[c.Kind+" "+c.Name] = c
		order = append(order, c.Kind+" "+c.Name)
	}
	want := []struct{ key, status, detail string }{
		{"service nginx", "ok", "active (running)"},
		{"service keepalived", "ok", "active (running)"},
		{"service postgresql@16-main", "ok", "active"},
		{"service haproxy", "warning", "failed, exit-code: hij bindt aan een adres dat in de afgesloten sandbox ontbreekt"},
		{"service redis-server", "fail", "failed, exit-code; laatste regel in de journal: redis-server.service: Main process exited"},
		{"service patroni", "warning", "Patroni start zonder de andere nodes"},
		{"service apache2", "warning", "niet geïnstalleerd in deze back-up"},
		{"units gefaalde units", "warning", "1 gefaalde unit: postfix.service"},
		{fmt.Sprintf("tcp 127.0.0.1:%d", openPort), "ok", "neemt verbindingen aan"},
		{fmt.Sprintf("tcp 127.0.0.1:%d", closedPort), "fail", "geen verbinding: "},
		{"tcp 127.0.0.1:81", "warning", "luistert alleen op 10.0.20.11:81, niet op loopback"},
		{"http " + web.URL + "/", "ok", "gaf 200"},
		{"http " + web.URL + "/kapot", "fail", "gaf 503 in plaats van 200"},
		{"http " + web.URL + "/weg", "fail", "gaf 302 in plaats van 200"},
		{"postgresql PostgreSQL", "ok", "bereikbaar, 3 databases (app, postgres, host=x) en elk antwoordt op SELECT 1; niet gecontroleerd wegens een ongewone naam: host=x; in herstelmodus"},
		{"mariadb MariaDB", "ok", "bereikbaar, 2 databases naast de systeemdatabases (shop, wiki)"},
	}
	for _, w := range want {
		c, ok := got[w.key]
		if !ok || c.Status != w.status || !strings.Contains(c.Detail, w.detail) {
			t.Errorf("%s: %+v", w.key, c)
		}
	}
	if len(res.Checks) != len(want)+1 {
		t.Errorf("controles: %v", order)
	}
	// De poort van haproxy is niet te controleren zolang haproxy niet draait.
	if c := res.Checks[11]; c.Kind != "tcp" || c.Status != "warning" || !strings.Contains(c.Detail, "niet te controleren: haproxy draait niet") {
		t.Errorf("poort van haproxy: %+v", c)
	}
	if got["postgresql PostgreSQL"].Count != 3 || got["mariadb MariaDB"].Count != 2 {
		t.Errorf("aantallen: %+v %+v", got["postgresql PostgreSQL"], got["mariadb MariaDB"])
	}
	for _, call := range sys.calls {
		if strings.Contains(call, "host=x") {
			t.Errorf("ongewone databasenaam als argument gebruikt: %s", call)
		}
	}
}

func TestVerifyDatabases(t *testing.T) {
	root := t.TempDir()
	sys := &fakeSystem{
		units: map[string]string{"mariadb.service": "failed"},
		psql: map[string]string{
			"-c SELECT 1": "1\n", "-c SELECT pg_is_in_recovery()": "f\n",
			"-c SELECT datname FROM pg_database WHERE datallowconn AND NOT datistemplate ORDER BY 1": "app\nkapot\n",
			"-d app -c SELECT 1": "1\n", "-d kapot -c SELECT 1": "!psql: error: FATAL: database \"kapot\" is corrupt\nmeer",
		},
		mariadb: map[string]string{"mariadb-admin ping": "!mariadb-admin: connect to server at 'localhost' failed"},
	}
	v := &Verifier{Exec: sys.exec, Root: root, BootWait: time.Second, Retry: 10 * time.Millisecond}
	res := v.Verify(context.Background(), protocol.VerifyRequest{Services: []string{"mariadb"}, PostgreSQL: true, MariaDB: true})
	pg, my, svc := res.Checks[2], res.Checks[3], res.Checks[0]
	if pg.Status != "fail" || !strings.Contains(pg.Detail, `1 van 2 databases antwoorden niet op SELECT 1: kapot (exit status 2: psql: error: FATAL: database "kapot" is corrupt)`) {
		t.Errorf("PostgreSQL: %+v", pg)
	}
	if svc.Status != "fail" || my.Status != "fail" || !strings.Contains(my.Detail, "antwoordt niet") {
		t.Errorf("MariaDB zonder Galera: %+v %+v", svc, my)
	}
	// Met Galera is het te verwachten.
	writeFile(t, root, "etc/mysql/mariadb.conf.d/60-galera.cnf", "[galera]\nwsrep_on = ON\nwsrep_cluster_address = gcomm://10.0.30.11\n")
	res = v.Verify(context.Background(), protocol.VerifyRequest{Services: []string{"mariadb"}, MariaDB: true})
	svc, my = res.Checks[0], res.Checks[2]
	if svc.Status != "warning" || !strings.Contains(svc.Detail, "MariaDB Galera start zonder de andere nodes niet") ||
		my.Status != "warning" || !strings.Contains(my.Detail, "Galera") {
		t.Errorf("MariaDB met Galera: %+v %+v", svc, my)
	}
	// Zonder psql of mariadb-admin is het een waarschuwing.
	v.Exec = (&fakeSystem{}).exec
	res = v.Verify(context.Background(), protocol.VerifyRequest{PostgreSQL: true, MariaDB: true})
	if res.Checks[1].Status != "warning" || !strings.Contains(res.Checks[1].Detail, "psql ontbreekt") || res.Checks[2].Status != "warning" {
		t.Errorf("zonder programma's: %+v", res.Checks)
	}
}

func TestVerifyOnlyLoopback(t *testing.T) {
	v := &Verifier{}
	if _, err := v.dial(context.Background(), "tcp", "10.0.0.1:80"); err == nil || !strings.Contains(err.Error(), "alleen met loopback") {
		t.Fatalf("dial naar buiten: %v", err)
	}
}

func TestVerifyLimit(t *testing.T) {
	block := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	v := &Verifier{Exec: block, Limit: 300 * time.Millisecond, CheckLimit: time.Minute, BootWait: time.Minute}
	start := time.Now()
	res := v.Verify(context.Background(), protocol.VerifyRequest{Services: []string{"nginx"}})
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("de limiet hield niet: %s", d)
	}
	if c := res.Checks[0]; c.Status != "fail" {
		t.Fatalf("na de limiet: %+v", res.Checks)
	}
}

func TestInSandbox(t *testing.T) {
	root := t.TempDir()
	if InSandbox(root) {
		t.Fatal("zonder serienummer een sandbox")
	}
	writeFile(t, root, "sys/class/dmi/id/product_serial", "VMware-42\n")
	if InSandbox(root) {
		t.Fatal("ander serienummer een sandbox")
	}
	writeFile(t, root, "sys/class/dmi/id/product_serial", "cf-sandbox\n")
	if !InSandbox(root) {
		t.Fatal("cf-sandbox niet herkend")
	}
}

func TestParseProcAddr(t *testing.T) {
	for in, want := range map[string]string{
		"0100007F:0050":                         "127.0.0.1:80",
		"0B14000A:1F90":                         "10.0.20.11:8080",
		"00000000000000000000000001000000:0050": "[::1]:80",
		"0000000000000000FFFF00000B14000A:01BB": "10.0.20.11:443",
	} {
		ip, port, ok := parseProcAddr(in)
		if !ok || net.JoinHostPort(ip.String(), fmt.Sprint(port)) != want {
			t.Errorf("%s: %v %d %v, verwacht %s", in, ip, port, ok, want)
		}
	}
}
