package agent

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

func TestLogParse(t *testing.T) {
	brussel := time.FixedZone("CET", 3600)
	p := logParser{now: time.Date(2026, 11, 16, 13, 0, 0, 0, brussel), loc: brussel}
	cases := []struct {
		line          string
		ts            time.Time
		level, proc   string
		msg           string
		wantTimestamp bool
	}{
		{"2026-11-16T12:00:01.123456+01:00 nc1 mariadbd[812]: [ERROR] WSREP: failed to open gcomm", time.Date(2026, 11, 16, 12, 0, 1, 123456000, brussel), "error", "mariadbd", "[ERROR] WSREP: failed to open gcomm", true},
		{"Nov 16 11:59:59 nc2 sshd[1234]: Failed password for root from 10.0.0.5", time.Date(2026, 11, 16, 11, 59, 59, 0, brussel), "warn", "sshd", "Failed password for root from 10.0.0.5", true},
		{"Dec 31 23:00:00 nc2 CRON[1]: (root) CMD (true)", time.Date(2025, 12, 31, 23, 0, 0, 0, brussel), "", "CRON", "(root) CMD (true)", true},
		{"2026/11/16 12:00:02 [error] 77#77: *1 connect() failed", time.Date(2026, 11, 16, 12, 0, 2, 0, brussel), "error", "", "[error] 77#77: *1 connect() failed", true},
		{`10.0.0.1 - - [16/Nov/2026:12:00:03 +0100] "GET / HTTP/1.1" 502 157 "-" "curl"`, time.Date(2026, 11, 16, 12, 0, 3, 0, brussel), "error", "", "", true},
		{"[Sun Nov 16 12:00:04.5 2026] [php:warn] [pid 9] something", time.Date(2026, 11, 16, 12, 0, 4, 500000000, brussel), "warn", "", "[php:warn] [pid 9] something", true},
		{`{"time":"2026-11-16T11:00:05Z","level":"WARN","msg":"traag","ms":812}`, time.Date(2026, 11, 16, 11, 0, 5, 0, time.UTC), "warn", "", "traag ms=812", true},
		{"2026-11-16 12:00:06,250 INFO werker gestart", time.Date(2026, 11, 16, 12, 0, 6, 250000000, brussel), "info", "", "INFO werker gestart", true},
		{"    at java.lang.Thread.run(Thread.java:750)", time.Time{}, "", "", "    at java.lang.Thread.run(Thread.java:750)", false},
	}
	for _, c := range cases {
		e, ok := p.parse(c.line)
		if ok != c.wantTimestamp || !e.TS.Equal(c.ts) || e.Level != c.level || e.Proc != c.proc || (c.msg != "" && e.Msg != c.msg) {
			t.Errorf("%q\n  kreeg ok=%v ts=%v level=%q proc=%q msg=%q", c.line, ok, e.TS, e.Level, e.Proc, e.Msg)
		}
	}
}

func readLogs(t *testing.T, a *Agent, src protocol.LogSource, f protocol.LogFilter) protocol.LogsResponse {
	t.Helper()
	body, _ := json.Marshal(protocol.LogsRequest{ID: "l1", Op: protocol.LogsOpRead, Source: src, Filter: f, Deadline: time.Now().Add(time.Minute)})
	env, _ := json.Marshal(protocol.Envelope{V: protocol.Version, Type: protocol.TypeLogsRequest, Body: body})
	return a.handleLogs(env)
}

func TestLogsFile(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "var/log/nginx")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	start := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)
	for i := range 30000 {
		ts := start.Add(time.Duration(i) * time.Second).Format(time.RFC3339)
		if i%1000 == 0 {
			fmt.Fprintf(&b, "%s web1 app[1]: ERROR nummer %d password=geheim123\n  vervolgregel %d\n", ts, i, i)
		} else {
			fmt.Fprintf(&b, "%s web1 app[1]: info regel %d met wat opvulling om het bestand groter te maken\n", ts, i)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "var/log/syslog"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "access.log"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "var/log/wtmp"), []byte{0, 1, 2}, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/shadow", filepath.Join(root, "var/log/schaduw")); err != nil {
		t.Fatal(err)
	}
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	_, _ = zw.Write([]byte("Nov 15 08:00:00 web1 cron[2]: oud\nNov 15 08:00:01 web1 cron[2]: ERROR oud\n"))
	_ = zw.Close()
	if err := os.WriteFile(filepath.Join(root, "var/log/syslog.2.gz"), gz.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	a := &Agent{Root: root, Exec: func(context.Context, string, ...string) ([]byte, error) {
		return nil, fmt.Errorf("niet aanwezig")
	}}

	// Bronnen: geen binaire bestanden, geen symlinks, geen journaal of Docker.
	body, _ := json.Marshal(protocol.LogsRequest{Op: protocol.LogsOpSources})
	env, _ := json.Marshal(protocol.Envelope{Type: protocol.TypeLogsRequest, Body: body})
	src := a.handleLogs(env)
	var paths []string
	for _, f := range src.Files {
		paths = append(paths, f.Path)
	}
	if !slices.Equal(paths, []string{"/var/log/nginx/access.log", "/var/log/syslog", "/var/log/syslog.2.gz"}) || src.Journal || src.Docker {
		t.Fatalf("bronnen: %v %+v", paths, src)
	}

	// Zonder filter: de laatste 500 regels, nieuwste achteraan.
	res := readLogs(t, a, protocol.LogSource{Kind: protocol.LogKindFile, Name: "/var/log/syslog"}, protocol.LogFilter{})
	if res.Error != "" || len(res.Entries) != 500 || !res.Truncated || !strings.Contains(res.Entries[499].Msg, "regel 29999") {
		t.Fatalf("zonder filter: %q %d %v", res.Error, len(res.Entries), res.Truncated)
	}
	// Alleen fouten: die staan verspreid over heel het bestand (groter dan
	// het eerste venster van 1 MB), met de vervolgregel erbij en het
	// wachtwoord gemaskeerd.
	res = readLogs(t, a, protocol.LogSource{Kind: protocol.LogKindFile, Name: "/var/log/syslog"}, protocol.LogFilter{Levels: []string{"error"}})
	if res.Error != "" || len(res.Entries) != 30 || res.Truncated {
		t.Fatalf("fouten: %q %d %v", res.Error, len(res.Entries), res.Truncated)
	}
	if e := res.Entries[0]; !strings.Contains(e.Msg, "nummer 0") || !strings.Contains(e.Msg, "\n  vervolgregel 0") || strings.Contains(e.Msg, "geheim123") || e.Proc != "app" {
		t.Fatalf("eerste fout: %+v", e)
	}
	// Tijdvenster en zoekterm.
	res = readLogs(t, a, protocol.LogSource{Kind: protocol.LogKindFile, Name: "/var/log/syslog"}, protocol.LogFilter{
		Since: start.Add(100 * time.Second), Until: start.Add(110 * time.Second), Pattern: "REGEL 10[0-5]", Regex: true, IgnoreCase: true,
	})
	if res.Error != "" || len(res.Entries) != 6 {
		t.Fatalf("venster: %q %d", res.Error, len(res.Entries))
	}
	// Uitsluiten.
	res = readLogs(t, a, protocol.LogSource{Kind: protocol.LogKindFile, Name: "/var/log/syslog"}, protocol.LogFilter{
		Since: start.Add(100 * time.Second), Until: start.Add(110 * time.Second), Exclude: "regel 105",
	})
	if len(res.Entries) != 10 {
		t.Fatalf("uitsluiten: %d", len(res.Entries))
	}
	// Gzip.
	res = readLogs(t, a, protocol.LogSource{Kind: protocol.LogKindFile, Name: "/var/log/syslog.2.gz"}, protocol.LogFilter{Pattern: "ERROR"})
	if res.Error != "" || len(res.Entries) != 1 || res.Entries[0].Level != "error" {
		t.Fatalf("gzip: %+v", res)
	}
	// Geweigerd: symlink, buiten /var/log, binair.
	for name, want := range map[string]string{
		"/var/log/schaduw":       "symlink",
		"/var/log/../etc/passwd": "alleen bestanden onder /var/log",
		"/etc/passwd":            "alleen bestanden onder /var/log",
		"/var/log/wtmp":          "geen tekstbestand",
		"/var/log/weg":           "bestaat niet",
	} {
		res := readLogs(t, a, protocol.LogSource{Kind: protocol.LogKindFile, Name: name}, protocol.LogFilter{})
		if !strings.Contains(res.Error, want) {
			t.Errorf("%s: %q, wilde %q", name, res.Error, want)
		}
	}
	// Ongeldige regex.
	res = readLogs(t, a, protocol.LogSource{Kind: protocol.LogKindFile, Name: "/var/log/syslog"}, protocol.LogFilter{Pattern: "(", Regex: true})
	if !strings.Contains(res.Error, "reguliere expressie") {
		t.Fatalf("regex: %q", res.Error)
	}
}

func TestLogsJournalAndDocker(t *testing.T) {
	var calls [][]string
	journal := []string{}
	for i := 5; i >= 1; i-- {
		prio := "6"
		if i == 3 {
			prio = "3"
		}
		journal = append(journal, fmt.Sprintf(`{"MESSAGE":"bericht %d","PRIORITY":"%s","SYSLOG_IDENTIFIER":"keepalived","_SYSTEMD_UNIT":"keepalived.service","__REALTIME_TIMESTAMP":"%d"}`,
			i, prio, time.Date(2026, 11, 16, 12, 0, i, 0, time.UTC).UnixMicro()))
	}
	a := &Agent{Exec: func(_ context.Context, name string, args ...string) ([]byte, error) {
		calls = append(calls, append([]string{name}, args...))
		switch {
		case name == "journalctl" && slices.Contains(args, "--field=_SYSTEMD_UNIT"):
			return []byte("ssh.service\nkeepalived.service\nbad unit\n"), nil
		case name == "journalctl":
			return []byte(strings.Join(journal, "\n") + "\n"), nil
		case name == "docker" && args[0] == "ps":
			return []byte("web-1\tnginx:1.27\trunning\tweb\n"), nil
		case name == "docker" && args[len(args)-1] == "web-1":
			return []byte("2026-11-16T12:00:01.000000001Z {\"level\":\"error\",\"msg\":\"upstream weg\"}\n" +
				"2026-11-16T12:00:02Z 10.0.0.1 - - [16/Nov/2026:13:00:02 +0100] \"GET / HTTP/1.1\" 200 5 \"-\" \"x\"\n"), nil
		case name == "docker":
			return []byte("Error response from daemon: No such container: weg\n"), fmt.Errorf("exit status 1")
		}
		return nil, fmt.Errorf("onverwacht")
	}}
	body, _ := json.Marshal(protocol.LogsRequest{Op: protocol.LogsOpSources})
	env, _ := json.Marshal(protocol.Envelope{Type: protocol.TypeLogsRequest, Body: body})
	src := a.handleLogs(env)
	if !src.Journal || !src.Docker || !slices.Equal(src.Units, []string{"keepalived.service", "ssh.service"}) || len(src.Containers) != 1 || src.Containers[0].Project != "web" {
		t.Fatalf("bronnen: %+v", src)
	}

	res := readLogs(t, a, protocol.LogSource{Kind: protocol.LogKindJournal, Name: "keepalived.service"}, protocol.LogFilter{Limit: 3, Since: time.Date(2026, 11, 16, 11, 0, 0, 0, time.UTC)})
	if res.Error != "" || len(res.Entries) != 3 || res.Entries[0].Msg != "bericht 3" || res.Entries[0].Level != "error" || res.Entries[2].Msg != "bericht 5" || res.Entries[0].Proc != "keepalived" || !res.Truncated {
		t.Fatalf("journaal: %+v", res)
	}
	last := calls[len(calls)-1]
	if !slices.Contains(last, "-u") || !slices.Contains(last, "keepalived.service") || !slices.Contains(last, "-r") || !slices.Contains(last, "--since") {
		t.Fatalf("journalctl-argumenten: %v", last)
	}
	res = readLogs(t, a, protocol.LogSource{Kind: protocol.LogKindJournal}, protocol.LogFilter{Levels: []string{"error"}})
	if len(res.Entries) != 1 || res.Entries[0].Msg != "bericht 3" {
		t.Fatalf("journaal fouten: %+v", res)
	}
	// Een unit die een optie is, wordt geweigerd.
	if res := readLogs(t, a, protocol.LogSource{Kind: protocol.LogKindJournal, Name: "--file=/etc/shadow"}, protocol.LogFilter{}); res.Error == "" {
		t.Fatal("optie als unit aanvaard")
	}

	res = readLogs(t, a, protocol.LogSource{Kind: protocol.LogKindDocker, Name: "web-1"}, protocol.LogFilter{})
	if res.Error != "" || len(res.Entries) != 2 || res.Entries[0].Level != "error" || !strings.HasPrefix(res.Entries[0].Msg, "upstream weg") || res.Entries[1].Level != "info" ||
		!res.Entries[0].TS.Equal(time.Date(2026, 11, 16, 12, 0, 1, 1, time.UTC)) {
		t.Fatalf("docker: %+v", res)
	}
	res = readLogs(t, a, protocol.LogSource{Kind: protocol.LogKindDocker, Name: "weg"}, protocol.LogFilter{})
	if !strings.Contains(res.Error, "bestaat niet") {
		t.Fatalf("docker weg: %+v", res)
	}
}
