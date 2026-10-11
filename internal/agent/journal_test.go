package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

func testJournal(t *testing.T, root string, now *time.Time) *Journal {
	t.Helper()
	loc := time.FixedZone("CEST", 2*3600)
	return &Journal{
		Root: root, Location: loc, Now: func() time.Time { return *now },
		Run: func(context.Context, string, ...string) ([]byte, error) { return nil, os.ErrNotExist },
	}
}

func appendFile(t *testing.T, root, p, content string) {
	t.Helper()
	full := filepath.Join(root, p)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(full, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(content); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
}

func pending(j *Journal) []protocol.Change { return j.Next().Entries }

// Een apt-run als root geeft per pakket een regel met oude en nieuwe versie
// en wie het deed; de commandoregel zelf wordt niet bewaard.
func TestJournalAptUpgrade(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 10, 10, 12, 5, 0, 0, time.UTC)
	j := testJournal(t, root, &now)
	appendFile(t, root, "/var/log/apt/history.log", `
Start-Date: 2026-10-10  14:02:11
Commandline: apt-get install --only-upgrade nginx -o Acquire::http::Proxy=http://jonas:geheim@proxy:3128
Upgrade: nginx:amd64 (1.26.0-1, 1.26.3-1), nginx-common:all (1.26.0-1, 1.26.3-1)
End-Date: 2026-10-10  14:02:15
`)
	appendFile(t, root, "/var/log/dpkg.log", "2026-10-10 14:02:12 upgrade nginx:amd64 1.26.0-1 1.26.3-1\n"+
		"2026-10-10 14:02:12 status half-configured nginx:amd64 1.26.3-1\n")
	if err := j.Collect(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := pending(j)
	if len(got) != 2 {
		t.Fatalf("regels: %+v", got)
	}
	c := got[0]
	if c.Kind != "package" || c.Subject != "nginx" || c.Action != "upgrade" || c.From != "1.26.0-1" || c.To != "1.26.3-1" || c.By != "root" {
		t.Fatalf("regel: %+v", c)
	}
	if c.Detail["via"] != "apt-get" || !c.TS.Equal(time.Date(2026, 10, 10, 12, 2, 11, 0, time.UTC)) {
		t.Fatalf("detail of tijd: %+v", c)
	}
	b, _ := json.Marshal(got)
	if strings.Contains(string(b), "geheim") || strings.Contains(string(b), "only-upgrade") {
		t.Fatalf("commandoregel in het journaal: %s", b)
	}

	// Een tweede ronde zonder nieuwe regels voegt niets toe.
	if err := j.Collect(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := len(pending(j)); n != 2 {
		t.Fatalf("na tweede ronde: %d", n)
	}
}

// Een apt-run die nog loopt, houdt ook de dpkg-regels erna tegen; dpkg -i
// buiten apt komt er met via dpkg.
func TestJournalAptRunningAndDpkg(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 10, 10, 12, 5, 0, 0, time.UTC)
	j := testJournal(t, root, &now)
	appendFile(t, root, "/var/log/dpkg.log", "2026-10-10 13:00:00 install cf-tool:amd64 <none> 0.3\n")
	appendFile(t, root, "/var/log/apt/history.log", "\nStart-Date: 2026-10-10  14:00:00\nCommandline: apt upgrade\nRequested-By: jonas (1000)\n")
	appendFile(t, root, "/var/log/dpkg.log", "2026-10-10 14:00:01 upgrade curl:amd64 7.88.1-10 7.88.1-10+deb12u8\n")
	if err := j.Collect(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := pending(j)
	if len(got) != 1 || got[0].Subject != "cf-tool" || got[0].Action != "install" || got[0].To != "0.3" || got[0].Detail["via"] != "dpkg" {
		t.Fatalf("tijdens de apt-run: %+v", got)
	}
	appendFile(t, root, "/var/log/apt/history.log", "Upgrade: curl:amd64 (7.88.1-10, 7.88.1-10+deb12u8)\nEnd-Date: 2026-10-10  14:00:03\n")
	if err := j.Collect(context.Background()); err != nil {
		t.Fatal(err)
	}
	got = pending(j)
	if len(got) != 2 || got[1].Subject != "curl" || got[1].By != "jonas" || got[1].Detail["via"] != "apt" {
		t.Fatalf("na de apt-run: %+v", got)
	}
}

// Na logrotate leest de agent eerst de rest van het oude bestand.
func TestJournalRotation(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 10, 10, 12, 5, 0, 0, time.UTC)
	j := testJournal(t, root, &now)
	appendFile(t, root, "/var/log/dpkg.log", "2026-10-10 10:00:00 install a:amd64 <none> 1\n")
	if err := j.Collect(context.Background()); err != nil {
		t.Fatal(err)
	}
	appendFile(t, root, "/var/log/dpkg.log", "2026-10-10 10:01:00 install b:amd64 <none> 1\n")
	if err := os.Rename(filepath.Join(root, "var/log/dpkg.log"), filepath.Join(root, "var/log/dpkg.log.1")); err != nil {
		t.Fatal(err)
	}
	appendFile(t, root, "/var/log/dpkg.log", "2026-10-10 10:02:00 remove c:amd64 2 <none>\n")
	if err := j.Collect(context.Background()); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, c := range pending(j) {
		names = append(names, c.Action+" "+c.Subject)
	}
	if !slices.Equal(names, []string{"install a", "install b", "remove c"}) {
		t.Fatalf("regels: %v", names)
	}
}

func TestJournalBootAndPersistence(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	writeFile(t, root, "/proc/sys/kernel/random/boot_id", "boot-1\n")
	writeFile(t, root, "/proc/sys/kernel/osrelease", "6.12.38-amd64\n")
	j := testJournal(t, root, &now)
	if err := j.Collect(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := len(pending(j)); n != 0 {
		t.Fatalf("eerste ronde: %d regels", n)
	}

	// Een nieuwe agent leest de state terug en ziet de herstart.
	writeFile(t, root, "/proc/sys/kernel/random/boot_id", "boot-2\n")
	writeFile(t, root, "/proc/sys/kernel/osrelease", "6.12.48-amd64\n")
	writeFile(t, root, "/proc/stat", "btime 1791633000\n")
	now = now.Add(10 * time.Minute)
	j2 := testJournal(t, root, &now)
	if err := j2.Collect(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := pending(j2)
	if len(got) != 1 || got[0].Kind != "boot" || got[0].From != "6.12.38-amd64" || got[0].To != "6.12.48-amd64" || got[0].TS.Unix() != 1791633000 {
		t.Fatalf("herstart: %+v", got)
	}
	if err := j2.Sent(j2.Next()); err != nil {
		t.Fatal(err)
	}
	j3 := testJournal(t, root, &now)
	if n := len(pending(j3)); n != 0 {
		t.Fatalf("na versturen: %d regels", n)
	}
}

func TestJournalUnits(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	writeFile(t, root, "/proc/sys/kernel/random/boot_id", "boot-1\n")
	writeFile(t, root, "/proc/stat", "btime 1791630000\n")
	j := testJournal(t, root, &now)
	unit := func(active, result string, restarts int, enter, exit uint64) UnitInfo {
		return UnitInfo{Name: "nginx", Active: active, Enabled: "enabled", Result: result, NRestarts: restarts, ActiveEnter: enter, InactiveEnter: exit}
	}
	steps := []struct {
		u    UnitInfo
		want string
	}{
		{unit("active", "success", 0, 1_000_000, 0), ""},
		{unit("failed", "exit-code", 0, 1_000_000, 5_000_000), "failed"},
		{unit("active", "success", 0, 9_000_000, 5_000_000), "started"},
		{unit("active", "success", 0, 12_000_000, 11_000_000), "restarted"},
		{unit("active", "success", 1, 20_000_000, 19_000_000), "auto_restarted"},
		{unit("inactive", "success", 1, 20_000_000, 30_000_000), "stopped"},
		{unit("inactive", "success", 1, 20_000_000, 30_000_000), ""},
	}
	count := 0
	for i, s := range steps {
		j.ObserveUnits([]UnitInfo{s.u})
		got := pending(j)
		if s.want != "" {
			count++
		}
		if len(got) != count {
			t.Fatalf("stap %d: %+v", i, got)
		}
		if s.want != "" && (got[count-1].Action != s.want || got[count-1].Subject != "nginx") {
			t.Fatalf("stap %d: %+v", i, got[count-1])
		}
	}
	got := pending(j)
	if len(got) != 5 {
		t.Fatalf("regels: %+v", got)
	}
	if got[0].Detail["result"] != "exit-code" || got[0].TS.Unix() != 1791630005 {
		t.Fatalf("mislukt: %+v", got[0])
	}

	// Na een herstart van de machine begint de vergelijking opnieuw.
	writeFile(t, root, "/proc/sys/kernel/random/boot_id", "boot-2\n")
	j.ObserveUnits([]UnitInfo{unit("active", "success", 0, 2_000_000, 0)})
	if n := len(pending(j)); n != 5 {
		t.Fatalf("na herstart: %d regels", n)
	}
	j.ObserveUnits([]UnitInfo{{Name: "nginx", Active: "active", Enabled: "disabled", ActiveEnter: 2_000_000}})
	if got := pending(j); got[len(got)-1].Action != "disabled" {
		t.Fatalf("uitgeschakeld: %+v", got[len(got)-1])
	}
}

func TestJournalFiles(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	j := testJournal(t, root, &now)
	writeFile(t, root, "/etc/nginx/nginx.conf", "worker_processes 2;\n")
	writeFile(t, root, "/etc/keepalived/keepalived.conf", "vrrp_instance X {}\n")
	writeFile(t, root, "/etc/hosts", "127.0.0.1 localhost\n")
	if err := j.Collect(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := len(pending(j)); n != 0 {
		t.Fatalf("eerste ronde: %d", n)
	}

	mtime := time.Date(2026, 10, 10, 11, 58, 0, 0, time.UTC)
	writeFile(t, root, "/etc/nginx/nginx.conf", "worker_processes 4;\n")
	_ = os.Chtimes(filepath.Join(root, "etc/nginx/nginx.conf"), mtime, mtime)
	if err := os.Chmod(filepath.Join(root, "etc/keepalived/keepalived.conf"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "etc/hosts")); err != nil {
		t.Fatal(err)
	}
	if err := j.Collect(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := map[string]protocol.Change{}
	for _, c := range pending(j) {
		got[c.Subject] = c
	}
	if c := got["/etc/nginx/nginx.conf"]; c.Action != "modified" || !c.TS.Equal(mtime) {
		t.Fatalf("nginx.conf: %+v", c)
	}
	if c := got["/etc/keepalived/keepalived.conf"]; c.Action != "mode" || c.From != "644" || c.To != "600" {
		t.Fatalf("keepalived.conf: %+v", c)
	}
	if c := got["/etc/hosts"]; c.Action != "deleted" {
		t.Fatalf("hosts: %+v", c)
	}
	b, _ := json.Marshal(pending(j))
	if strings.Contains(string(b), "worker_processes") {
		t.Fatalf("inhoud in het journaal: %s", b)
	}

	// Veel bestanden tegelijk onder één map worden één regel.
	for i := range 8 {
		writeFile(t, root, "/etc/nginx/sites-available/site"+string(rune('a'+i)), "x")
	}
	before := len(pending(j))
	if err := j.Collect(context.Background()); err != nil {
		t.Fatal(err)
	}
	all := pending(j)
	if len(all) != before+1 || all[len(all)-1].Subject != "/etc/nginx" || all[len(all)-1].Detail["count"] != "8" {
		t.Fatalf("groep: %+v", all[before:])
	}
}

func TestParseJournalctl(t *testing.T) {
	out := "1760090400.123456 web-02 sshd[812]: Accepted publickey for root from 10.0.0.5 port 50422 ssh2: ED25519 SHA256:abc\n" +
		"1760090401.000000 web-02 sshd[813]: Failed password for invalid user admin from 203.0.113.9 port 4444 ssh2\n" +
		"1760090500.500000 web-02 kernel: Out of memory: Killed process 4321 (mysqld) total-vm:123kB, anon-rss:99kB\n"
	got := parseJournalctl([]byte(out))
	if len(got) != 2 {
		t.Fatalf("regels: %+v", got)
	}
	if got[0].Kind != "login" || got[0].Subject != "root" || got[0].Detail["ip"] != "10.0.0.5" || got[0].Detail["method"] != "publickey" {
		t.Fatalf("login: %+v", got[0])
	}
	if strings.Contains(got[0].Detail["method"], "SHA256") {
		t.Fatalf("sleutel in het journaal: %+v", got[0])
	}
	if got[1].Kind != "oom" || got[1].Subject != "mysqld" || got[1].TS.Unix() != 1760090500 {
		t.Fatalf("oom: %+v", got[1])
	}
}

// journalctl krijgt de eerste keer --since, daarna alleen de cursor.
func TestJournaldArgs(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	j := testJournal(t, root, &now)
	var calls [][]string
	j.Run = func(_ context.Context, name string, args ...string) ([]byte, error) {
		calls = append(calls, args)
		for _, a := range args {
			if p, ok := strings.CutPrefix(a, "--cursor-file="); ok {
				_ = os.WriteFile(p, []byte("s=1"), 0o600)
			}
		}
		return nil, nil
	}
	_ = j.Collect(context.Background())
	_ = j.Collect(context.Background())
	if len(calls) != 2 || !slices.ContainsFunc(calls[0], func(a string) bool { return strings.HasPrefix(a, "--since=@") }) ||
		slices.ContainsFunc(calls[1], func(a string) bool { return strings.HasPrefix(a, "--since=") }) {
		t.Fatalf("aanroepen: %v", calls)
	}
}

func TestJournalPendingLimit(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	j := testJournal(t, root, &now)
	j.load()
	for i := range maxPending + 10 {
		j.add(protocol.Change{TS: now.Add(time.Duration(i) * time.Second), Kind: "login", Subject: "root", Action: "ssh"})
	}
	// Ouder dan een week valt meteen weg.
	j.add(protocol.Change{TS: now.Add(-8 * 24 * time.Hour), Kind: "login", Subject: "oud", Action: "ssh"})
	next := j.Next()
	if len(next.Entries) != protocol.MaxChanges || !next.Truncated || !next.Entries[0].TS.Equal(now.Add(10*time.Second)) {
		t.Fatalf("eerste portie: %d %v %v", len(next.Entries), next.Truncated, next.Entries[0].TS)
	}
	if err := j.Sent(next); err != nil {
		t.Fatal(err)
	}
	if n := len(j.st.Pending); n != maxPending-protocol.MaxChanges || j.st.Truncated {
		t.Fatalf("na versturen: %d %v", n, j.st.Truncated)
	}
}
