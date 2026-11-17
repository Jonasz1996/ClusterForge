package agent

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

const ctID = "1111111111111111111111111111111111111111111111111111111111111111"

func TestWebDiscover(t *testing.T) {
	f := newFakeDockerAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/containers/json" {
			_, _ = io.WriteString(w, `[{"Id":"`+ctID+`","Names":["/authentik-server-1"],"Image":"ghcr.io/goauthentik/server:2025.6.2","State":"running",
				"Labels":{"com.docker.compose.project":"authentik"},"Ports":[]},
				{"Id":"2222","Names":["/vaultwarden"],"Image":"vaultwarden/server:1.34","State":"running","Labels":{},
				"Ports":[{"IP":"0.0.0.0","PrivatePort":80,"PublicPort":8081,"Type":"tcp"},{"IP":"127.0.0.1","PrivatePort":3012,"PublicPort":3012,"Type":"tcp"}]}]`)
			return
		}
		http.NotFound(w, r)
	})
	root := f.root
	writeFile(t, root, "/proc/sys/net/ipv4/ip_local_port_range", "32768\t60999\n")
	// 9000 (Authentik in host-netwerk), 80 (nginx), 22 (sshd), 5432 alleen
	// op loopback en een tijdelijke poort.
	writeFile(t, root, "/proc/net/tcp", "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n"+
		"   0: 00000000:2328 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 2001 1 0 100 0 0 10 0\n"+
		"   1: 00000000:0050 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 2002 1 0 100 0 0 10 0\n"+
		"   2: 00000000:0016 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 2003 1 0 100 0 0 10 0\n"+
		"   3: 0100007F:1538 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 2004 1 0 100 0 0 10 0\n"+
		"   4: 00000000:8235 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 2005 1 0 100 0 0 10 0\n")
	writeFile(t, root, "/proc/net/tcp6", "  sl  local_address rem_address st\n"+
		"   0: 00000000000000000000000000000000:2328 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 2006 1 0\n")
	writeFile(t, root, "/proc/10/cgroup", "0::/system.slice/docker-"+ctID+".scope\n")
	symlink(t, root, "socket:[2001]", "/proc/10/fd/3")
	symlink(t, root, "socket:[2006]", "/proc/10/fd/4")
	writeFile(t, root, "/proc/20/cgroup", "0::/system.slice/nginx.service\n")
	symlink(t, root, "socket:[2002]", "/proc/20/fd/3")

	a := &Agent{Root: root, Log: slog.New(slog.DiscardHandler)}
	got := a.discover(context.Background(), false)
	byPort := map[int]protocol.WebService{}
	for _, s := range got {
		byPort[s.Port] = s
	}
	if len(got) != 4 {
		t.Fatalf("diensten: %+v", got)
	}
	if s := byPort[9000]; s.Container != "authentik-server-1" || s.Project != "authentik" || s.Unit != "" || s.Image == "" {
		t.Errorf("9000: %+v", s)
	}
	if s := byPort[80]; s.Unit != "nginx" || s.Container != "" {
		t.Errorf("80: %+v", s)
	}
	if s := byPort[8081]; s.Container != "vaultwarden" || !s.Published {
		t.Errorf("8081: %+v", s)
	}
	if _, ok := byPort[22]; !ok {
		t.Errorf("22 ontbreekt")
	}
	if _, ok := byPort[5432]; ok {
		t.Errorf("loopback hoort er niet bij")
	}
	if _, ok := byPort[3012]; ok {
		t.Errorf("een poort op 127.0.0.1 hoort er niet bij")
	}
}

func TestWebProbe(t *testing.T) {
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Server", "nginx")
		_, _ = io.WriteString(w, "<html><head><title>Mijn &amp; homepage</title></head></html>")
	}))
	defer plain.Close()
	tlsSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/login", http.StatusFound)
	}))
	defer tlsSrv.Close()
	port := func(s *httptest.Server) int { return s.Listener.Addr().(*net.TCPAddr).Port }
	svcs := []protocol.WebService{
		{Proto: "tcp", Addr: "127.0.0.1", Port: port(plain)},
		{Proto: "tcp", Addr: "127.0.0.1", Port: port(tlsSrv)},
		{Proto: "tcp", Addr: "0.0.0.0", Port: 3306},
	}
	a := &Agent{Log: slog.New(slog.DiscardHandler)}
	a.webProbe(context.Background(), svcs)
	if s := svcs[0]; s.Kind != "http" || s.Status != 200 || s.Title != "Mijn & homepage" || s.Server != "nginx" {
		t.Errorf("http: %+v", s)
	}
	if s := svcs[1]; s.Kind != "https" || s.Status != 302 || s.Location != "/login" {
		t.Errorf("https: %+v", s)
	}
	if s := svcs[2]; s.Kind != "tcp" || s.Status != 0 {
		t.Errorf("3306 hoort niet aangesproken te worden: %+v", s)
	}
}

func TestWebHTTPAndValidate(t *testing.T) {
	var gotAuth, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		if r.URL.Path == "/tekst" {
			_, _ = io.WriteString(w, "geen json")
			return
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"id":7}`)
	}))
	defer srv.Close()
	port := srv.Listener.Addr().(*net.TCPAddr).Port
	a := &Agent{Log: slog.New(slog.DiscardHandler)}
	req := protocol.WebRequest{Op: protocol.WebOpHTTP, HTTP: &protocol.WebHTTP{Method: "POST", Port: port, Path: "/api/nginx/proxy-hosts",
		Headers: map[string]string{"Authorization": "Bearer x"}, Body: json.RawMessage(`{"a":1}`)}}
	if err := req.Validate(); err != nil {
		t.Fatal(err)
	}
	res := a.web(context.Background(), req)
	if res.Error != "" || res.Status != 201 || string(res.Body) != `{"id":7}` || gotAuth != "Bearer x" || gotBody != `{"a":1}` {
		t.Fatalf("%+v auth=%q body=%q", res, gotAuth, gotBody)
	}
	res = a.web(context.Background(), protocol.WebRequest{Op: protocol.WebOpHTTP, HTTP: &protocol.WebHTTP{Method: "GET", Port: port, Path: "/tekst"}})
	if string(res.Body) != `"geen json"` {
		t.Errorf("tekst: %s", res.Body)
	}
	for _, bad := range []protocol.WebRequest{
		{Op: protocol.WebOpHTTP, HTTP: &protocol.WebHTTP{Method: "TRACE", Port: 81, Path: "/"}},
		{Op: protocol.WebOpHTTP, HTTP: &protocol.WebHTTP{Method: "GET", Port: 0, Path: "/"}},
		{Op: protocol.WebOpHTTP, HTTP: &protocol.WebHTTP{Method: "GET", Port: 81, Path: "/../etc"}},
		{Op: protocol.WebOpHTTP, HTTP: &protocol.WebHTTP{Method: "GET", Port: 81, Path: "/", Headers: map[string]string{"Host": "x"}}},
		{Op: protocol.WebOpHTTP, HTTP: &protocol.WebHTTP{Method: "GET", Port: 81, Path: "/", Headers: map[string]string{"Authorization": "a\r\nb"}}},
		{Op: protocol.WebOpCheckURL, Check: &protocol.WebCheck{URL: "https://user@x.jbogaert.be/"}},
		{Op: protocol.WebOpCheckURL, Check: &protocol.WebCheck{URL: "ftp://x.jbogaert.be/"}},
		{Op: protocol.WebOpNPMRestore, NPM: &protocol.WebNPM{Snapshot: "../x"}},
		{Op: "rm"},
	} {
		if bad.Validate() == nil {
			t.Errorf("geen fout voor %+v", bad)
		}
	}
}

func TestWebCheckURL(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "homepage.jbogaert.be" {
			w.WriteHeader(http.StatusMisdirectedRequest)
			return
		}
		w.Header().Set("Location", "https://auth.jbogaert.be/application/o/authorize/")
		w.WriteHeader(http.StatusFound)
	}))
	defer srv.Close()
	addr := srv.Listener.Addr().String()
	a := &Agent{Log: slog.New(slog.DiscardHandler), WebDial: func(ctx context.Context, network, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, network, addr)
	}}
	res := a.web(context.Background(), protocol.WebRequest{Op: protocol.WebOpCheckURL, Check: &protocol.WebCheck{URL: "https://homepage.jbogaert.be/", Connect: "127.0.0.1"}})
	c := res.Check
	if c == nil || c.Status != 302 || !strings.Contains(c.Location, "authorize") {
		t.Fatalf("%+v", res)
	}
	// Het testcertificaat is niet voor deze naam en niet vertrouwd.
	if c.CertOK || c.CertError == "" || c.CertNotAfter == nil {
		t.Errorf("certificaat: %+v", c)
	}
}

// npmFixture is een NPM-node: container cf-npm met /data in root.
func npmFixture(t *testing.T) (*Agent, string, *[]string) {
	t.Helper()
	f := newFakeDockerAPI(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/containers/json":
			_, _ = io.WriteString(w, `[{"Id":"npmid","Names":["/cf-npm"],"Image":"jc21/nginx-proxy-manager:2.12.6","State":"running","Labels":{}}]`)
		case "/containers/npmid/json":
			_, _ = io.WriteString(w, `{"Mounts":[{"Source":"/opt/cf-npm/data","Destination":"/data"}],"NetworkSettings":{"Ports":{"81/tcp":[{"HostIp":"0.0.0.0","HostPort":"81"}]}}}`)
		default:
			http.NotFound(w, r)
		}
	})
	root := f.root
	data := filepath.Join(root, "opt/cf-npm/data")
	writeFile(t, root, "/opt/cf-npm/data/database.sqlite", "DB-NIEUW")
	writeFile(t, root, "/opt/cf-npm/data/database.sqlite-wal", "WAL")
	writeFile(t, root, "/opt/cf-npm/data/nginx/proxy_host/7.conf", "server {\n  server_name homepage.jbogaert.be;\n}\n")
	var calls []string
	a := &Agent{Root: root, Log: slog.New(slog.DiscardHandler)}
	rows := map[string]string{}
	a.Exec = func(_ context.Context, name string, args ...string) ([]byte, error) {
		calls = append(calls, name+" "+strings.Join(args, " "))
		switch {
		case name == "sqlite3" && strings.HasPrefix(args[len(args)-1], ".backup '"):
			dst := strings.TrimSuffix(strings.TrimPrefix(args[len(args)-1], ".backup '"), "'")
			b, err := os.ReadFile(args[0])
			if err != nil {
				return []byte("Error: unable to open database"), err
			}
			return nil, os.WriteFile(dst, b, 0o600)
		case name == "sqlite3" && args[len(args)-1] == "PRAGMA integrity_check;":
			return []byte("ok\n"), nil
		case name == "sqlite3":
			b, _ := os.ReadFile(args[len(args)-2])
			return []byte(rows[string(b)]), nil
		case name == "docker" && args[0] == "exec":
			return []byte("nginx: the configuration file /etc/nginx/nginx.conf syntax is ok\nnginx: configuration file /etc/nginx/nginx.conf test is successful\n"), nil
		}
		return nil, nil
	}
	rows["DB-NIEUW"] = `0|1|["homepage.jbogaert.be"]` + "\n"
	return a, data, &calls
}

func TestWebNPMSnapshotCheckRestore(t *testing.T) {
	a, data, calls := npmFixture(t)
	a.Now = func() time.Time { return time.Date(2026, 11, 17, 8, 0, 0, 0, time.UTC) }
	ctx := context.Background()

	snap := a.web(ctx, protocol.WebRequest{Op: protocol.WebOpNPMSnapshot})
	if snap.Error != "" || snap.NPM == nil || !protocol.ValidWebSnapshot(snap.NPM.Snapshot) || snap.NPM.Container != "cf-npm" {
		t.Fatalf("kopie: %+v", snap)
	}
	dir := filepath.Join(a.Root, npmSnapshotDir, snap.NPM.Snapshot)
	if b, _ := os.ReadFile(filepath.Join(dir, "nginx/proxy_host/7.conf")); !strings.Contains(string(b), "homepage") {
		t.Errorf("config niet in de kopie")
	}

	chk := a.web(ctx, protocol.WebRequest{Op: protocol.WebOpNPMCheck, NPM: &protocol.WebNPM{HostID: 7, Domain: "homepage.jbogaert.be"}})
	if chk.Error != "" || !chk.NPM.AllOK() || len(chk.NPM.Checks) != 4 {
		t.Fatalf("controle: %+v", chk.NPM)
	}
	// Een andere naam in de database dan gevraagd, en een .err.
	writeFile(t, a.Root, "/opt/cf-npm/data/nginx/proxy_host/7.conf.err", "x")
	chk = a.web(ctx, protocol.WebRequest{Op: protocol.WebOpNPMCheck, NPM: &protocol.WebNPM{HostID: 7, Domain: "nas.jbogaert.be"}})
	failed := map[string]bool{}
	for _, c := range chk.NPM.Checks {
		if !c.OK {
			failed[c.Name] = true
		}
	}
	if !failed["database"] || !failed["config"] || !failed["geen fout"] || failed["nginx -t"] {
		t.Errorf("verwacht database, config en .err fout: %+v", chk.NPM.Checks)
	}

	// Terugzetten: de database wordt die van de kopie, zonder -wal, en de
	// nginx-map van de kopie komt terug.
	writeFile(t, a.Root, "/opt/cf-npm/data/database.sqlite", "DB-KAPOT")
	writeFile(t, a.Root, "/opt/cf-npm/data/nginx/proxy_host/8.conf", "nieuw")
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"status":"OK"}`)
	}))
	defer api.Close()
	a.WebDial = func(ctx context.Context, network, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, network, api.Listener.Addr().String())
	}
	*calls = nil
	res := a.web(ctx, protocol.WebRequest{Op: protocol.WebOpNPMRestore, NPM: &protocol.WebNPM{Snapshot: snap.NPM.Snapshot}})
	if res.Error != "" || !res.NPM.AllOK() {
		t.Fatalf("terugzetten: %+v", res)
	}
	if b, _ := os.ReadFile(filepath.Join(data, "database.sqlite")); string(b) != "DB-NIEUW" {
		t.Errorf("database: %q", b)
	}
	if _, err := os.Stat(filepath.Join(data, "database.sqlite-wal")); !os.IsNotExist(err) {
		t.Errorf("-wal hoort weg te zijn")
	}
	if _, err := os.Stat(filepath.Join(data, "nginx/proxy_host/8.conf")); !os.IsNotExist(err) {
		t.Errorf("8.conf hoort weg te zijn")
	}
	joined := strings.Join(*calls, "\n")
	if !strings.Contains(joined, "docker stop -t 30 cf-npm") || !strings.Contains(joined, "docker start cf-npm") ||
		strings.Index(joined, "docker stop") > strings.Index(joined, "docker start") {
		t.Errorf("stop/start: %s", joined)
	}
}

func TestPruneSnapshots(t *testing.T) {
	base := t.TempDir()
	for _, n := range []string{"20261101-000000-aaaa", "20261102-000000-aaaa", "20261103-000000-aaaa", "anders"} {
		_ = os.MkdirAll(filepath.Join(base, n), 0o700)
	}
	pruneSnapshots(base, 2)
	entries, _ := os.ReadDir(base)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if strings.Join(names, ",") != "20261102-000000-aaaa,20261103-000000-aaaa,anders" {
		t.Errorf("%v", names)
	}
}
