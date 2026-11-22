package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Jonasz1996/clusterforge/internal/agent"
	"github.com/Jonasz1996/clusterforge/internal/agent/agenttest"
	"github.com/Jonasz1996/clusterforge/internal/proxmox/pvefake"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// sandboxAgent speelt cf-agent verify na in een teruggezette kopie van host:
// dezelfde systemd en bestanden. dial leidt loopback om; nil laat loopback
// zoals het is. requests onthoudt elke aanvraag.
type sandboxAgent struct {
	host *agenttest.Host
	dial func(ctx context.Context, network, address string) (net.Conn, error)

	mu       sync.Mutex
	requests []string
	commands [][]string
}

func (s *sandboxAgent) exec(_ int, _ string, command []string, input string) (int, string, string) {
	s.mu.Lock()
	s.requests = append(s.requests, input)
	s.commands = append(s.commands, command)
	s.mu.Unlock()
	if !slices.Equal(command, []string{protocol.VerifyPath, "verify", "-"}) {
		return 127, "", "onbekend programma"
	}
	v := &agent.Verifier{
		Version: "test", Exec: s.host.Exec, Root: s.host.Root, Dial: s.dial,
		BootWait: time.Second, Settle: 300 * time.Millisecond, Retry: 20 * time.Millisecond,
	}
	var out bytes.Buffer
	code := v.Main(context.Background(), strings.NewReader(input), &out)
	return code, out.String(), ""
}

func (s *sandboxAgent) last(t *testing.T) (string, protocol.VerifyRequest) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.requests) == 0 {
		t.Fatal("cf-agent verify is niet gestart")
	}
	raw := s.requests[len(s.requests)-1]
	var req protocol.VerifyRequest
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		t.Fatal(err)
	}
	return raw, req
}

func TestBackupVerifyDeep(t *testing.T) {
	e, c := adminClient(t)
	f, _, ids, pve, conn := deployWebPVE(t, e, c)
	ctx := context.Background()
	web01 := f.host("web-01")
	var vmid int
	if err := e.pool.QueryRow(ctx, "SELECT pve_vmid FROM nodes WHERE id = $1", ids["web-01"]).Scan(&vmid); err != nil {
		t.Fatal(err)
	}
	pve.AddPool("cf-sandbox")
	pve.AddStorage(pvefake.Storage{Name: "pbs", Node: "pve1", Shared: true, Content: "backup"})
	pve.AddBackup(pvefake.Backup{Storage: "pbs", VMID: vmid, Time: time.Now().Add(-6 * time.Hour).Truncate(time.Second), Size: 4 << 30})
	if s := c.do("POST", "/api/v1/proxmox/"+conn.ID+"/sync", nil, nil); s != 200 {
		t.Fatalf("sync: %d", s)
	}

	// De website in de sandbox is index.html van web-01, of een fout.
	var status atomic.Int32
	status.Store(http.StatusOK)
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if st := int(status.Load()); st != http.StatusOK {
			w.WriteHeader(st)
			return
		}
		b, err := os.ReadFile(filepath.Join(web01.Root, "var/www/html/index.html"))
		if err != nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write(b)
	}))
	defer site.Close()
	sandbox := &sandboxAgent{host: web01, dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
		if addr == "127.0.0.1:80" {
			addr = site.Listener.Addr().String()
		}
		var d net.Dialer
		return d.DialContext(ctx, network, addr)
	}}
	pve.OnExec(sandbox.exec)

	verify := func() verifyRun {
		t.Helper()
		var run verifyRun
		if s := c.do("POST", "/api/v1/nodes/"+ids["web-01"]+"/backups/verify", nil, &run); s != 202 {
			t.Fatalf("starten: %d", s)
		}
		var r verifyRun
		eventually(t, "controle klaar", func() bool {
			c.do("GET", "/api/v1/test-runs/"+run.ID, nil, &r)
			return r.Result != nil && r.JobStatus != nil && *r.JobStatus != "queued" && *r.JobStatus != "running"
		})
		return r
	}

	t.Run("geslaagd", func(t *testing.T) {
		before := len(pve.Calls())
		r := verify()
		if *r.Result != "pass" || !strings.Contains(r.Summary, ", 2 services actief.") {
			t.Fatalf("uitkomst: %s %q %+v", *r.Result, r.Summary, r.Checks)
		}
		want := []string{
			"Terugzetten", "Isolatie", "Opstarten", "Hostname", "Besturingssysteem", "Bestandssystemen",
			"Service nginx", "Service keepalived", "Gefaalde units", "Poort 127.0.0.1:80", "HTTP http://127.0.0.1/",
			"Agentverbinding", "Opruimen",
		}
		if !slices.Equal(r.checkNames(), want) {
			t.Fatalf("controles: %v", r.checkNames())
		}
		for _, ch := range r.Checks {
			if !ch.OK {
				t.Errorf("controle %s: %+v", ch.Name, ch)
			}
		}
		if ok, _, detail := r.check("HTTP http://127.0.0.1/"); !ok || detail != "gaf 200" {
			t.Fatalf("HTTP: %s", detail)
		}
		m := r.Backup.Measurements
		if m.AgentVersion != "test" || m.ServicesExpected != 2 || m.ServicesActive != 2 || len(m.Databases) != 0 {
			t.Fatalf("metingen: %+v", m)
		}
		// De aanvraag: services uit de template en de facts, de poort van
		// nginx en de HTTP-controle op loopback. Geen geheimen.
		raw, req := sandbox.last(t)
		if !slices.Contains(req.Services, "nginx") || !slices.Contains(req.Services, "keepalived") || len(req.Services) != 2 ||
			!slices.Equal(req.TCP, []protocol.VerifyTCP{{Host: "127.0.0.1", Port: 80, Service: "nginx"}}) ||
			!slices.Equal(req.HTTP, []protocol.VerifyHTTP{{URL: "http://127.0.0.1/", Expect: 200}}) || req.PostgreSQL || req.MariaDB {
			t.Fatalf("aanvraag: %s", raw)
		}
		if strings.Contains(raw, "geheim12") || strings.Contains(raw, "10.0.20.100") {
			t.Fatalf("geheim of VIP in de aanvraag: %s", raw)
		}
		// Alleen in de sandbox, nooit op de productie-VM.
		var execs []string
		for _, call := range pve.Calls()[before:] {
			if strings.HasSuffix(call, "/agent/exec") {
				execs = append(execs, call)
			}
		}
		sb := r.Backup.Measurements.SandboxVMID
		if len(execs) != 1 || execs[0] != fmt.Sprintf("POST /nodes/pve1/qemu/%d/agent/exec", sb) || sb == vmid {
			t.Fatalf("exec: %v (sandbox %d, bron %d)", execs, sb, vmid)
		}
		var j job
		c.do("GET", "/api/v1/jobs/"+*r.JobID, nil, &j)
		var log []string
		for _, st := range j.Steps {
			if st.Name == "Controleren" {
				log = st.Log
			}
		}
		joined := strings.Join(log, "\n")
		if !strings.Contains(joined, "cf-agent verify: services nginx, keepalived; poort 80; http://127.0.0.1/") ||
			!strings.Contains(joined, "cf-agent test antwoordde na ") {
			t.Fatalf("log van Controleren: %v", log)
		}
	})

	t.Run("website kapot", func(t *testing.T) {
		status.Store(http.StatusServiceUnavailable)
		defer status.Store(http.StatusOK)
		r := verify()
		if *r.Result != "fail" || !strings.Contains(r.Summary, "back-up afgekeurd: 1 controle mislukt. HTTP http://127.0.0.1/: gaf 503 in plaats van 200.") ||
			!strings.Contains(r.Summary, "verwijderd om") {
			t.Fatalf("uitkomst: %s %q", *r.Result, r.Summary)
		}
	})

	for _, tc := range []struct {
		name, code, detail string
		setup              func()
	}{
		{"agent zonder verify", "agent_too_old", "cf-agent in deze back-up kent verify nog niet (exitcode 2)", func() {
			pve.OnExec(func(int, string, []string, string) (int, string, string) {
				return 2, "", "cf-agent: de ClusterForge-agent\n\nGebruik:\n"
			})
		}},
		{"geen cf-agent", "agent_too_old", "er staat geen cf-agent in deze back-up (/usr/local/bin/cf-agent ontbreekt)", func() { pve.OnExec(nil) }},
		{"token zonder exec", "exec_forbidden", "VM.GuestAgent.Unrestricted op /pool/cf-sandbox", func() { pve.SetExecForbidden(true) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.setup()
			defer func() {
				pve.OnExec(sandbox.exec)
				pve.SetExecForbidden(false)
			}()
			r := verify()
			var deep *struct {
				Name    string `json:"name"`
				OK      bool   `json:"ok"`
				Warning bool   `json:"warning"`
				Detail  string `json:"detail"`
				Code    string `json:"code"`
			}
			for i := range r.Checks {
				if r.Checks[i].Name == "Diepe controle" {
					deep = &r.Checks[i]
				}
			}
			if *r.Result != "warning" || deep == nil || deep.OK || !deep.Warning || deep.Code != tc.code || !strings.Contains(deep.Detail, tc.detail) ||
				!strings.Contains(r.Summary, "geslaagd met waarschuwing") {
				t.Fatalf("uitkomst: %s %q %+v", *r.Result, r.Summary, r.Checks)
			}
			if r.Backup.Measurements.AgentVersion != "" || r.Backup.Measurements.ServicesExpected != 0 {
				t.Fatalf("metingen: %+v", r.Backup.Measurements)
			}
		})
	}
}
